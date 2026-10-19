package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

func init() { verifyDelay = 5 * time.Millisecond }

type fakeContainer struct {
	id, name, imageID string
	running, paused   bool
	exits             bool
	config            map[string]any
	hostConfig        map[string]any
	networks          map[string]any
}

// stateful fake Docker Engine: it tracks containers, names and images so the
// tests can assert what a sequence of calls actually leaves behind.
type fakeEngine struct {
	t          *testing.T
	mu         sync.Mutex
	containers map[string]*fakeContainer
	images     map[string]string // image reference -> current image ID
	nextID     int
	calls      []string
	createBody map[string]any
	createName string
	pullLines  string
	failures   map[string]func(w http.ResponseWriter) // "METHOD /path-suffix" -> custom response
	newExits   bool                                   // containers created through the API exit as soon as they start
}

func newFakeEngine(t *testing.T) (*Client, *fakeEngine) {
	t.Helper()
	e := &fakeEngine{t: t, containers: map[string]*fakeContainer{}, images: map[string]string{"app:1": "sha256:old"}, failures: map[string]func(http.ResponseWriter){}}
	server := httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(server.Close)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, e
}

func (e *fakeEngine) add(c *fakeContainer) *fakeContainer {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.id == "" {
		e.nextID++
		c.id = fmt.Sprintf("%064x", e.nextID)
	}
	e.containers[c.id] = c
	return c
}

func (e *fakeEngine) find(ref string) *fakeContainer {
	for _, c := range e.containers {
		if c.id == ref || c.name == ref {
			return c
		}
	}
	return nil
}

func (e *fakeEngine) byName(name string) *fakeContainer {
	for _, c := range e.containers {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (e *fakeEngine) callsMatching(prefix string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, call := range e.calls {
		if strings.HasPrefix(call, prefix) {
			out = append(out, call)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (e *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.44")
	// Containers are recorded by name so assertions do not depend on generated IDs.
	label := path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "containers" && parts[1] != "create" {
		if c := e.find(parts[1]); c != nil {
			parts[1] = c.name
			label = "/" + strings.Join(parts, "/")
		}
	}
	query := r.URL.Query()
	call := r.Method + " " + label
	if r.URL.RawQuery != "" {
		keys := []string{}
		for _, key := range []string{"t", "signal", "force", "v", "name", "fromImage"} {
			if v := query.Get(key); v != "" {
				keys = append(keys, key+"="+v)
			}
		}
		if len(keys) > 0 {
			call += "?" + strings.Join(keys, "&")
		}
	}
	e.calls = append(e.calls, call)
	for pattern, respond := range e.failures {
		method, suffix, _ := strings.Cut(pattern, " ")
		if r.Method == method && strings.HasSuffix(label, suffix) {
			respond(w)
			return
		}
	}

	switch {
	case r.Method == http.MethodPost && path == "/containers/create":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.createBody, e.createName = body, query.Get("name")
		if ref, _ := body["Image"].(string); e.images[ref] == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: " + ref})
			return
		}
		e.nextID++
		c := &fakeContainer{id: fmt.Sprintf("%064x", e.nextID), name: query.Get("name"), imageID: e.images[body["Image"].(string)], config: body, hostConfig: body["HostConfig"].(map[string]any), exits: e.newExits}
		e.containers[c.id] = c
		writeJSON(w, http.StatusCreated, map[string]any{"Id": c.id, "Warnings": []string{}})
	case r.Method == http.MethodPost && path == "/images/create":
		ref := query.Get("fromImage")
		if e.pullLines != "" {
			_, _ = io.WriteString(w, e.pullLines)
		}
		if !strings.Contains(e.pullLines, `"error"`) {
			e.images[ref] = "sha256:new"
		}
	case len(parts) >= 2 && parts[0] == "containers":
		c := e.find(parts[1])
		if c == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container"})
			return
		}
		action := ""
		if len(parts) > 2 {
			action = parts[2]
		}
		switch {
		case r.Method == http.MethodGet && action == "json":
			writeJSON(w, http.StatusOK, map[string]any{
				"Id": c.id, "Name": "/" + c.name, "Image": c.imageID, "Config": c.config, "HostConfig": c.hostConfig,
				"State":           map[string]any{"Running": c.running, "Paused": c.paused, "Restarting": false, "Status": map[bool]string{true: "running", false: "exited"}[c.running]},
				"NetworkSettings": map[string]any{"Networks": c.networks},
			})
		case r.Method == http.MethodPost && action == "start":
			if c.running {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			c.running = !c.exits
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && action == "stop":
			if !c.running {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			c.running = false
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && (action == "restart" || action == "kill"):
			c.running = action == "restart"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && action == "pause":
			c.paused = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && action == "unpause":
			c.paused = false
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && action == "rename":
			if other := e.byName(query.Get("name")); other != nil && other != c {
				writeJSON(w, http.StatusConflict, map[string]string{"message": "Conflict. The container name is already in use"})
				return
			}
			c.name = query.Get("name")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && action == "":
			if c.running && query.Get("force") == "" {
				writeJSON(w, http.StatusConflict, map[string]string{"message": "You cannot remove a running container. Stop the container before attempting removal or force remove"})
				return
			}
			delete(e.containers, c.id)
			w.WriteHeader(http.StatusNoContent)
		default:
			e.t.Errorf("unexpected request %s", call)
			http.NotFound(w, r)
		}
	default:
		e.t.Errorf("unexpected request %s", call)
		http.NotFound(w, r)
	}
}

func appContainer(running bool) *fakeContainer {
	return &fakeContainer{
		id: "abcdef123456" + strings.Repeat("0", 52), name: "web", imageID: "sha256:old", running: running,
		config: map[string]any{
			"Image": "app:1", "Hostname": "abcdef123456", "Env": []any{"TOKEN=s3cret", "MODE=prod"}, "Cmd": []any{"serve"},
			"Labels": map[string]any{"com.docker.compose.project": "site"},
		},
		hostConfig: map[string]any{"NetworkMode": "site_default", "Binds": []any{"/srv/data:/data:rw"}, "RestartPolicy": map[string]any{"Name": "unless-stopped"}, "Memory": 268435456},
		networks: map[string]any{
			"site_default": map[string]any{"IPAMConfig": map[string]any{"IPv4Address": "172.18.0.50"}, "Aliases": []any{"web", "abcdef123456"}, "IPAddress": "172.18.0.50", "Gateway": "172.18.0.1", "MacAddress": "02:42:ac:12:00:32", "NetworkID": "net1", "EndpointID": "ep1"},
		},
	}
}

func TestActionsIssueTheExpectedDockerRequests(t *testing.T) {
	client, e := newFakeEngine(t)
	e.add(appContainer(true))
	stopped := e.add(&fakeContainer{name: "idle", config: map[string]any{}, hostConfig: map[string]any{}})
	ctx := context.Background()

	for _, tc := range []struct {
		params  protocol.DockerActionParams
		call    string
		changed bool
	}{
		{protocol.DockerActionParams{ID: "idle", Action: "start"}, "POST /containers/idle/start", true},
		{protocol.DockerActionParams{ID: "web", Action: "start"}, "POST /containers/web/start", false},
		{protocol.DockerActionParams{ID: "web", Action: "stop"}, "POST /containers/web/stop?t=10", true},
		{protocol.DockerActionParams{ID: "web", Action: "stop"}, "POST /containers/web/stop?t=10", false},
		{protocol.DockerActionParams{ID: "web", Action: "start"}, "POST /containers/web/start", true},
		{protocol.DockerActionParams{ID: "web", Action: "restart", TimeoutSeconds: 25}, "POST /containers/web/restart?t=25", true},
		{protocol.DockerActionParams{ID: "web", Action: "stop", TimeoutSeconds: 99999}, "POST /containers/web/stop?t=300", true},
		{protocol.DockerActionParams{ID: "web", Action: "start"}, "POST /containers/web/start", true},
		{protocol.DockerActionParams{ID: "web", Action: "pause"}, "POST /containers/web/pause", true},
		{protocol.DockerActionParams{ID: "web", Action: "unpause"}, "POST /containers/web/unpause", true},
		{protocol.DockerActionParams{ID: "web", Action: "kill"}, "POST /containers/web/kill?signal=KILL", true},
		{protocol.DockerActionParams{ID: "web", Action: "kill", Signal: "sigterm"}, "POST /containers/web/kill?signal=TERM", true},
		{protocol.DockerActionParams{ID: "idle", Action: "remove", Force: true, RemoveVolumes: true}, "DELETE /containers/idle?force=1&v=1", true},
	} {
		before := len(e.callsMatching(""))
		result, err := client.Action(ctx, tc.params)
		if err != nil {
			t.Fatalf("%+v: %v", tc.params, err)
		}
		calls := e.callsMatching("")[before:]
		if len(calls) != 2 || !strings.HasPrefix(calls[0], "GET /containers/") || calls[1] != tc.call {
			t.Errorf("%+v sent %v, want lookup then %q", tc.params, calls, tc.call)
		}
		if result.Changed != tc.changed || result.Action != tc.params.Action || result.Name == "" {
			t.Errorf("%+v result = %+v, want changed=%v", tc.params, result, tc.changed)
		}
	}
	if e.byName("idle") != nil || stopped == nil {
		t.Fatal("remove did not delete the container")
	}
}

func TestActionsRefuseBadInputWithoutTouchingDocker(t *testing.T) {
	client, e := newFakeEngine(t)
	e.add(appContainer(true))
	for _, p := range []protocol.DockerActionParams{
		{ID: "../etc", Action: "start"}, {ID: "", Action: "start"}, {ID: "web", Action: "exec"}, {ID: "web", Action: ""}, {ID: "web", Action: "restart; rm"},
		{ID: "web", Action: "kill", Signal: "SEGV"}, {ID: "web", Action: "kill", Signal: "9; rm -rf"},
	} {
		if _, err := client.Action(context.Background(), p); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Action(%+v) error = %v, want ErrBadRequest", p, err)
		}
	}
	if len(e.callsMatching("")) != 0 {
		t.Fatalf("invalid requests reached Docker: %v", e.callsMatching(""))
	}
}

func TestActionErrorsKeepDockerExplanations(t *testing.T) {
	client, e := newFakeEngine(t)
	e.add(appContainer(true))
	_, err := client.Action(context.Background(), protocol.DockerActionParams{ID: "web", Action: "remove"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || !strings.Contains(apiErr.Message, "running container") {
		t.Fatalf("removing a running container: %v", err)
	}
	if e.byName("web") == nil {
		t.Fatal("a refused remove deleted the container")
	}
	if _, err := client.Action(context.Background(), protocol.DockerActionParams{ID: "ghost", Action: "stop"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing container: %v", err)
	}
	e.failures["POST /containers/web/pause"] = func(w http.ResponseWriter) {
		writeJSON(w, http.StatusConflict, map[string]string{"message": strings.Repeat("x", 1000)})
	}
	if _, err := client.Action(context.Background(), protocol.DockerActionParams{ID: "web", Action: "pause"}); !errors.As(err, &apiErr) || len(apiErr.Message) != 300 {
		t.Fatalf("long messages must be clipped: %v", err)
	}
}

func collect(events *[]protocol.DockerRecreateEvent) func(protocol.DockerRecreateEvent) {
	return func(e protocol.DockerRecreateEvent) { *events = append(*events, e) }
}

func steps(events []protocol.DockerRecreateEvent) string {
	var out []string
	for _, e := range events {
		out = append(out, e.Step)
	}
	return strings.Join(out, ",")
}

func TestRecreateBuildsAReplacementBeforeTouchingTheOriginal(t *testing.T) {
	client, e := newFakeEngine(t)
	original := e.add(appContainer(true))
	e.pullLines = `{"status":"Pulling from library/app","id":"1"}` + "\n" + `{"status":"Downloading","id":"layer1","progressDetail":{"current":10,"total":100}}` + "\n" + `{"status":"Status: Downloaded newer image for app:1"}` + "\n"
	var events []protocol.DockerRecreateEvent
	result, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web", Pull: true}, collect(&events))
	if err != nil {
		t.Fatal(err)
	}

	calls := e.callsMatching("")
	generated := regexp.MustCompile(`(-vigara-(?:new|old)-)[0-9a-z]+`)
	suffix := func(call string) string { return generated.ReplaceAllString(call, "${1}*") } // hide random and timestamp parts
	var order []string
	for _, call := range calls {
		order = append(order, suffix(call))
	}
	want := []string{
		"GET /containers/web/json", "POST /images/create?fromImage=app:1", "POST /containers/create?name=web-vigara-new-*", "GET /containers/web-vigara-new-*/json",
		"POST /containers/web/stop?t=10", "POST /containers/web/rename?name=web-vigara-old-*", "POST /containers/web-vigara-new-*/rename?name=web", "POST /containers/web/start",
		"GET /containers/web/json", "DELETE /containers/web-vigara-old-*",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("call order:\n%s\nwant:\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(strings.Join(calls, " "), "force=") && strings.Contains(calls[len(calls)-1], "force=") {
		t.Fatal("the original must be removed without force")
	}

	// The replacement is built from the original's configuration.
	body := e.createBody
	if body["Image"] != "app:1" || body["Hostname"] != nil {
		t.Fatalf("Image=%v Hostname=%v (a generated hostname must not be copied)", body["Image"], body["Hostname"])
	}
	host, _ := body["HostConfig"].(map[string]any)
	if host["Memory"] != float64(268435456) || len(host["Binds"].([]any)) != 1 || host["NetworkMode"] != "site_default" {
		t.Fatalf("HostConfig = %v", host)
	}
	endpoint := body["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)["site_default"].(map[string]any)
	if endpoint["IPAMConfig"] == nil || len(endpoint["Aliases"].([]any)) != 1 || endpoint["Aliases"].([]any)[0] != "web" {
		t.Fatalf("endpoint = %v", endpoint)
	}
	for _, dropped := range []string{"IPAddress", "Gateway", "MacAddress", "NetworkID", "EndpointID"} {
		if _, present := endpoint[dropped]; present {
			t.Errorf("per-container field %s was copied to the replacement", dropped)
		}
	}

	if e.byName("web") == nil || e.byName("web").id == original.id || !e.byName("web").running || len(e.containers) != 1 {
		t.Fatalf("containers after recreate: %d", len(e.containers))
	}
	if result.ID != e.byName("web").id || !result.ImageChanged || result.PreviousImageID != "sha256:old" || result.ImageID != "sha256:new" || !result.Started || result.RolledBack {
		t.Fatalf("result = %+v", result)
	}
	if got := steps(events); got != "pull,pull,pull_layer,pull,create,stop_old,swap,start,verify,cleanup,done" {
		t.Fatalf("events = %s", got)
	}
	if events[len(events)-1].Result == nil || events[len(events)-1].Result.ID != result.ID {
		t.Fatalf("final event = %+v", events[len(events)-1])
	}
}

func TestRecreateKeepsAStoppedContainerStopped(t *testing.T) {
	client, e := newFakeEngine(t)
	e.add(appContainer(false))
	result, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web"}, nil)
	if err != nil || result.Started || result.ImageChanged {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if len(e.callsMatching("POST /containers/web/start")) != 0 || len(e.callsMatching("POST /containers/web/stop")) != 0 || len(e.callsMatching("POST /images/create")) != 0 {
		t.Fatalf("a stopped container must not be stopped or started, nor pulled without being asked: %v", e.callsMatching(""))
	}
	if c := e.byName("web"); c == nil || c.running {
		t.Fatal("the replacement should exist and stay stopped")
	}
}

func TestRecreateFailuresBeforeTheSwapLeaveTheOriginalUntouched(t *testing.T) {
	for name, setup := range map[string]func(e *fakeEngine){
		"pull error":    func(e *fakeEngine) { e.pullLines = `{"error":"unauthorized: authentication required"}` + "\n" },
		"missing image": func(e *fakeEngine) { delete(e.images, "app:1") },
		"create rejected": func(e *fakeEngine) {
			e.failures["POST /containers/create"] = func(w http.ResponseWriter) { writeJSON(w, 500, map[string]string{"message": "boom"}) }
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, e := newFakeEngine(t)
			original := e.add(appContainer(true))
			setup(e)
			_, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web", Pull: name == "pull error"}, nil)
			var failure *RecreateError
			if !errors.As(err, &failure) || failure.RolledBack {
				t.Fatalf("error = %v", err)
			}
			if c := e.byName("web"); c == nil || c.id != original.id || !c.running || len(e.containers) != 1 {
				t.Fatalf("the original changed: %d containers", len(e.containers))
			}
			for _, prefix := range []string{"POST /containers/web/stop", "POST /containers/web/rename", "DELETE"} {
				if got := e.callsMatching(prefix); len(got) != 0 {
					t.Errorf("a failure before the swap still sent %v", got)
				}
			}
		})
	}
}

func TestRecreateRollsBackWhenTheReplacementFails(t *testing.T) {
	for name, setup := range map[string]func(e *fakeEngine){
		"start fails": func(e *fakeEngine) {
			e.failures["POST /containers/web/start"] = func(w http.ResponseWriter) {
				// Only the replacement's start fails: it is the one named web at that point.
				writeJSON(w, 500, map[string]string{"message": "port is already allocated"})
			}
		},
		"exits immediately": func(e *fakeEngine) { e.newExits = true },
	} {
		t.Run(name, func(t *testing.T) {
			client, e := newFakeEngine(t)
			original := e.add(appContainer(true))
			if name == "start fails" {
				// The original is restarted by the rollback, so let that one succeed.
				calls := 0
				e.failures["POST /containers/web/start"] = func(w http.ResponseWriter) {
					calls++
					if calls == 1 {
						writeJSON(w, 500, map[string]string{"message": "port is already allocated"})
						return
					}
					e.containers[original.id].running = true
					w.WriteHeader(http.StatusNoContent)
				}
			}
			setup2 := setup
			if name != "start fails" {
				setup2(e)
			}
			var events []protocol.DockerRecreateEvent
			result, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web"}, collect(&events))
			var failure *RecreateError
			if !errors.As(err, &failure) || !failure.RolledBack {
				t.Fatalf("error = %v", err)
			}
			if !result.RolledBack {
				t.Fatalf("result = %+v", result)
			}
			if c := e.byName("web"); c == nil || c.id != original.id || !c.running {
				t.Fatalf("the original was not restored: %+v", c)
			}
			if len(e.containers) != 1 {
				t.Fatalf("a failed replacement was left behind: %d containers", len(e.containers))
			}
			if !strings.Contains(steps(events), "rollback") {
				t.Fatalf("events = %s", steps(events))
			}
		})
	}
}

func TestRecreateSkipsPullingPinnedImagesAndToleratesCleanupFailure(t *testing.T) {
	client, e := newFakeEngine(t)
	pinned := appContainer(true)
	pinned.config["Image"] = "app@sha256:" + strings.Repeat("a", 64)
	e.images[pinned.config["Image"].(string)] = "sha256:old"
	e.add(pinned)
	e.failures["DELETE web-vigara-old"] = nil
	delete(e.failures, "DELETE web-vigara-old")
	var events []protocol.DockerRecreateEvent
	if _, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web", Pull: true}, collect(&events)); err != nil {
		t.Fatal(err)
	}
	if len(e.callsMatching("POST /images/create")) != 0 || !strings.Contains(steps(events), "skipped") {
		t.Fatalf("a digest-pinned image must not be pulled: %v / %s", e.callsMatching("POST /images"), steps(events))
	}

	client2, e2 := newFakeEngine(t)
	e2.add(appContainer(true))
	e2.failures["DELETE "] = func(w http.ResponseWriter) { writeJSON(w, 500, map[string]string{"message": "device busy"}) }
	var events2 []protocol.DockerRecreateEvent
	result, err := client2.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web"}, collect(&events2))
	if err != nil || result.RolledBack || !strings.Contains(steps(events2), "cleanup,skipped,done") {
		t.Fatalf("a failed cleanup must not undo a working replacement: %+v %v %s", result, err, steps(events2))
	}
	if c := e2.byName("web"); c == nil || !c.running {
		t.Fatal("the replacement should be running")
	}
}

func TestRecreateOmitsNetworkingForHostMode(t *testing.T) {
	client, e := newFakeEngine(t)
	host := appContainer(true)
	host.hostConfig["NetworkMode"] = "host"
	host.networks = map[string]any{"host": map[string]any{"NetworkID": "h"}}
	e.add(host)
	if _, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, present := e.createBody["NetworkingConfig"]; present {
		t.Fatalf("host-mode containers cannot be given endpoints: %v", e.createBody["NetworkingConfig"])
	}
}

func TestCancellingDuringThePullStopsBeforeAnyChange(t *testing.T) {
	client, e := newFakeEngine(t)
	e.add(appContainer(true))
	ctx, cancel := context.WithCancel(context.Background())
	e.failures["POST /images/create"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		cancel() // the caller goes away mid-pull
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := client.Recreate(ctx, protocol.DockerRecreateParams{ID: "web", Pull: true}, nil); err == nil {
		t.Fatal("a cancelled recreate reported success")
	}
	if len(e.callsMatching("POST /containers/create")) != 0 || len(e.callsMatching("POST /containers/web/stop")) != 0 {
		t.Fatalf("a cancelled pull still changed things: %v", e.callsMatching(""))
	}
}

func TestRecreateRejectsBadReferences(t *testing.T) {
	client, e := newFakeEngine(t)
	for _, id := range []string{"", "../x", "a b"} {
		if _, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: id}, nil); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Recreate(%q) = %v", id, err)
		}
	}
	if len(e.callsMatching("")) != 0 {
		t.Fatal("bad references reached Docker")
	}
}

func TestAPullFailureKeepsDockersExplanation(t *testing.T) {
	client, e := newFakeEngine(t)
	original := e.add(appContainer(true))
	e.failures["POST /images/create"] = func(w http.ResponseWriter) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "pull access denied for app, repository does not exist or may require 'docker login'"})
	}
	_, err := client.Recreate(context.Background(), protocol.DockerRecreateParams{ID: "web", Pull: true}, nil)
	var failure *RecreateError
	if !errors.As(err, &failure) || failure.Step != protocol.StepPull || failure.RolledBack {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "pull access denied") || strings.Contains(err.Error(), "container not found") {
		t.Fatalf("the user must see why the pull failed, got %q", err)
	}
	if c := e.byName("web"); c == nil || c.id != original.id || !c.running {
		t.Fatal("the original changed")
	}
}

func TestSlowStopsAreNotCutOffByTheTransport(t *testing.T) {
	// Docker answers a stop only after the container exits, which for a process that ignores
	// SIGTERM is the full grace period. The default transport must not impose a shorter limit.
	client, err := New("http://127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport %T", client.http.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 || client.http.Timeout != 0 {
		t.Fatalf("response header timeout %v and client timeout %v would cut slow stops short", transport.ResponseHeaderTimeout, client.http.Timeout)
	}
}
