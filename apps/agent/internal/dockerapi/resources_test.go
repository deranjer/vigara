package dockerapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

const anonID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// resEngine is a fake Docker Engine holding a fixed inventory of containers,
// images, networks, and volumes, and recording every request.
type resEngine struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []string
	queries []string
	dfFails bool
	pull    string
	// removals records what was deleted so tests can assert on side effects.
	removals []string
}

func newResClient(t *testing.T) (*Client, *resEngine) {
	t.Helper()
	e := &resEngine{t: t}
	server := httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(server.Close)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, e
}

func (e *resEngine) writeCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, call := range e.calls {
		if !strings.HasPrefix(call, "GET ") {
			out = append(out, call)
		}
	}
	return out
}

func (e *resEngine) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/v1.44")
	e.mu.Lock()
	e.calls = append(e.calls, r.Method+" "+path)
	e.queries = append(e.queries, r.URL.RawQuery)
	e.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		writeJSON(w, 200, []map[string]any{
			{"Id": "c1", "Names": []string{"/web"}, "ImageID": "sha256:imgweb", "Mounts": []map[string]any{{"Type": "volume", "Name": "pgdata"}, {"Type": "bind", "Name": "", "Source": "/srv"}},
				"NetworkSettings": map[string]any{"Networks": map[string]any{"site_default": map[string]any{}}}},
			{"Id": "c2", "Names": []string{"/stopped-job"}, "ImageID": "sha256:imgold", "Mounts": []map[string]any{{"Type": "volume", "Name": anonID}},
				"NetworkSettings": map[string]any{"Networks": map[string]any{"bridge": map[string]any{}}}},
		})
	case r.Method == http.MethodGet && path == "/images/json":
		writeJSON(w, 200, []map[string]any{
			{"Id": "sha256:imgweb", "RepoTags": []string{"nginx:1.27", "nginx:latest"}, "Created": 300, "Size": 1000},
			{"Id": "sha256:imgold", "RepoTags": []string{"<none>:<none>"}, "Created": 100, "Size": 500},
			{"Id": "sha256:imgunused", "RepoTags": []string{"redis:7"}, "Created": 200, "Size": 700},
			{"Id": "sha256:imgdangling", "RepoTags": nil, "Created": 50, "Size": 300},
		})
	case r.Method == http.MethodGet && path == "/images/ghcr.io/x/y:tag/json", r.Method == http.MethodGet && path == "/images/sha256:imgweb/json":
		writeJSON(w, 200, map[string]any{
			"Id": "sha256:imgweb", "RepoTags": []string{"ghcr.io/x/y:tag"}, "RepoDigests": []string{"ghcr.io/x/y@sha256:dig"}, "Created": "2026-10-01T10:00:00Z", "Size": 1000,
			"Architecture": "amd64", "Os": "linux", "Author": "someone", "RootFS": map[string]any{"Layers": []string{"a", "b", "c"}},
			"Config": map[string]any{"User": "app", "WorkingDir": "/srv", "Entrypoint": []string{"/entry"}, "Cmd": []string{"serve"},
				"ExposedPorts": map[string]any{"8080/tcp": map[string]any{}, "80/tcp": map[string]any{}}, "Env": []string{"API_KEY=hunter2", "PATH=/usr/bin"}, "Labels": map[string]string{"org": "x"}},
		})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/images/"):
		ref := strings.TrimPrefix(path, "/images/")
		e.mu.Lock()
		e.removals = append(e.removals, ref)
		e.mu.Unlock()
		if ref == "sha256:imgweb" {
			writeJSON(w, 409, map[string]string{"message": "conflict: unable to delete sha256:imgweb (cannot be forced) - image is being used by running container c1"})
			return
		}
		writeJSON(w, 200, []map[string]string{{"Untagged": "redis:7"}, {"Deleted": "sha256:imgunused"}})
	case r.Method == http.MethodPost && path == "/images/create":
		_, _ = w.Write([]byte(e.pull))
	case r.Method == http.MethodPost && path == "/images/prune":
		writeJSON(w, 200, map[string]any{"ImagesDeleted": []map[string]string{{"Untagged": "redis:7"}, {"Deleted": "sha256:imgunused"}}, "SpaceReclaimed": 700})
	case r.Method == http.MethodGet && path == "/networks":
		writeJSON(w, 200, []map[string]any{
			{"Id": "n1", "Name": "bridge", "Driver": "bridge", "Scope": "local", "IPAM": map[string]any{"Config": []map[string]string{{"Subnet": "172.17.0.0/16", "Gateway": "172.17.0.1"}}}},
			{"Id": "n2", "Name": "site_default", "Driver": "bridge", "Scope": "local", "Attachable": true, "Created": "2026-10-01T10:00:00Z", "IPAM": map[string]any{"Config": []map[string]string{{"Subnet": "172.18.0.0/16"}}}},
			{"Id": "n3", "Name": "orphan", "Driver": "bridge", "Scope": "local", "Internal": true},
			{"Id": "n4", "Name": "host", "Driver": "host", "Scope": "local"},
		})
	case r.Method == http.MethodGet && path == "/networks/n2":
		writeJSON(w, 200, map[string]any{"Id": "n2", "Name": "site_default", "Driver": "bridge", "Scope": "local",
			"Containers": map[string]any{"c1": map[string]string{"Name": "web", "IPv4Address": "172.18.0.2/16", "MacAddress": "02:42:ac:12:00:02"}},
			"Options":    map[string]string{"com.docker.network.bridge.name": "br-site"}, "Labels": map[string]string{"com.docker.compose.project": "site"}})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/networks/"):
		ref := strings.TrimPrefix(path, "/networks/")
		e.mu.Lock()
		e.removals = append(e.removals, ref)
		e.mu.Unlock()
		switch ref {
		case "bridge":
			writeJSON(w, 403, map[string]string{"message": "bridge is a pre-defined network and cannot be removed"})
		case "site_default":
			writeJSON(w, 403, map[string]string{"message": "error while removing network: network site_default id n2 has active endpoints"})
		default:
			w.WriteHeader(204)
		}
	case r.Method == http.MethodPost && path == "/networks/prune":
		writeJSON(w, 200, map[string]any{"NetworksDeleted": []string{"orphan"}})
	case r.Method == http.MethodGet && path == "/volumes":
		writeJSON(w, 200, map[string]any{"Volumes": []map[string]any{
			{"Name": "pgdata", "Driver": "local", "Scope": "local", "Mountpoint": "/var/lib/docker/volumes/pgdata/_data", "CreatedAt": "2026-10-01T10:00:00Z", "Labels": map[string]string{"com.docker.compose.project": "site"}},
			{"Name": anonID, "Driver": "local", "Scope": "local"},
			{"Name": "cache-old", "Driver": "local", "Scope": "local"},
			{"Name": "anon-unused", "Driver": "local", "Scope": "local"},
		}})
	case r.Method == http.MethodGet && path == "/volumes/pgdata":
		writeJSON(w, 200, map[string]any{"Name": "pgdata", "Driver": "local", "Scope": "local", "Mountpoint": "/var/lib/docker/volumes/pgdata/_data", "Options": map[string]string{"type": "none"}})
	case r.Method == http.MethodGet && path == "/system/df":
		if e.dfFails {
			writeJSON(w, 500, map[string]string{"message": "df failed"})
			return
		}
		writeJSON(w, 200, map[string]any{"Volumes": []map[string]any{
			{"Name": "pgdata", "UsageData": map[string]any{"Size": 4096, "RefCount": 1}},
			{"Name": "cache-old", "UsageData": map[string]any{"Size": 2048, "RefCount": 0}},
			{"Name": "anon-unused", "UsageData": map[string]any{"Size": -1, "RefCount": 0}},
		}})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/volumes/"):
		ref := strings.TrimPrefix(path, "/volumes/")
		e.mu.Lock()
		e.removals = append(e.removals, ref)
		e.mu.Unlock()
		if ref == "pgdata" {
			writeJSON(w, 409, map[string]string{"message": "remove pgdata: volume is in use - [c1]"})
			return
		}
		w.WriteHeader(204)
	case r.Method == http.MethodPost && path == "/volumes/prune":
		writeJSON(w, 200, map[string]any{"VolumesDeleted": []string{"cache-old"}, "SpaceReclaimed": 2048})
	default:
		e.t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

func TestImagesShowWhichContainersUseThemAndWhichAreDangling(t *testing.T) {
	client, e := newResClient(t)
	result, err := client.Images(context.Background())
	if err != nil || len(result.Images) != 4 {
		t.Fatalf("Images = %+v, %v", result, err)
	}
	byID := map[string]protocol.DockerImage{}
	for _, image := range result.Images {
		byID[image.ID] = image
	}
	if web := byID["sha256:imgweb"]; len(web.UsedBy) != 1 || web.UsedBy[0].Name != "web" || web.Dangling || strings.Join(web.Tags, ",") != "nginx:1.27,nginx:latest" {
		t.Fatalf("web image = %+v", web)
	}
	// A stopped container still holds its image, so it must not look unused.
	if old := byID["sha256:imgold"]; len(old.UsedBy) != 1 || old.UsedBy[0].Name != "stopped-job" || !old.Dangling || len(old.Tags) != 0 {
		t.Fatalf("old image = %+v", old)
	}
	if unused := byID["sha256:imgunused"]; len(unused.UsedBy) != 0 || unused.Dangling {
		t.Fatalf("unused image = %+v", unused)
	}
	if !byID["sha256:imgdangling"].Dangling {
		t.Fatal("an image with no tags is dangling")
	}
	if result.Images[0].ID != "sha256:imgweb" || result.Images[3].ID != "sha256:imgdangling" {
		t.Fatalf("images must be newest first: %v", result.Images)
	}
	if len(e.writeCalls()) != 0 {
		t.Fatalf("listing must not change anything: %v", e.writeCalls())
	}
}

func TestNetworksAndVolumesReportUsageSizesAndKinds(t *testing.T) {
	client, _ := newResClient(t)
	networks, err := client.Networks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]protocol.DockerNetwork{}
	for _, n := range networks.Networks {
		byName[n.Name] = n
	}
	if !byName["bridge"].Builtin || !byName["host"].Builtin || byName["orphan"].Builtin || !byName["orphan"].Internal {
		t.Fatalf("builtin flags: %+v", networks.Networks)
	}
	if site := byName["site_default"]; len(site.UsedBy) != 1 || site.UsedBy[0].Name != "web" || !site.Attachable || len(site.Subnets) != 1 || site.Subnets[0] != "172.18.0.0/16" || site.Created == "" {
		t.Fatalf("site network = %+v", site)
	}
	if bridge := byName["bridge"]; len(bridge.UsedBy) != 1 || bridge.UsedBy[0].Name != "stopped-job" || bridge.Gateways[0] != "172.17.0.1" {
		t.Fatalf("bridge = %+v", bridge)
	}
	if networks.Networks[0].Name != "bridge" || networks.Networks[len(networks.Networks)-1].Name != "site_default" {
		t.Fatalf("networks must be sorted by name: %v", networks.Networks)
	}

	volumes, err := client.Volumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byVolume := map[string]protocol.DockerVolume{}
	for _, v := range volumes.Volumes {
		byVolume[v.Name] = v
	}
	if pg := byVolume["pgdata"]; pg.Size != 4096 || len(pg.UsedBy) != 1 || pg.UsedBy[0].Name != "web" || pg.Anonymous || pg.Labels["com.docker.compose.project"] == "" {
		t.Fatalf("pgdata = %+v (a bind mount must not count as a volume use)", pg)
	}
	if anon := byVolume[anonID]; !anon.Anonymous || len(anon.UsedBy) != 1 || anon.UsedBy[0].Name != "stopped-job" || anon.Size != -1 {
		t.Fatalf("anonymous volume = %+v", anon)
	}
	if byVolume["anon-unused"].Size != -1 || byVolume["cache-old"].Size != 2048 {
		t.Fatalf("sizes: %+v", volumes.Volumes)
	}
}

func TestVolumeSizesAreOptional(t *testing.T) {
	client, e := newResClient(t)
	e.dfFails = true
	volumes, err := client.Volumes(context.Background())
	if err != nil || len(volumes.Volumes) != 4 {
		t.Fatalf("a failing disk-usage report must not break the list: %+v %v", volumes, err)
	}
	for _, v := range volumes.Volumes {
		if v.Size != -1 {
			t.Errorf("%s size = %d, want unknown (-1)", v.Name, v.Size)
		}
	}
}

func TestInspectKeepsEnvironmentValuesOutAndRegistryPathsIntact(t *testing.T) {
	client, e := newResClient(t)
	result, err := client.InspectResource(context.Background(), protocol.KindImage, "ghcr.io/x/y:tag")
	if err != nil || result.Image == nil {
		t.Fatalf("inspect image = %+v, %v", result, err)
	}
	image := result.Image
	if strings.Join(image.EnvNames, ",") != "API_KEY,PATH" || image.Layers != 3 || strings.Join(image.ExposedPorts, ",") != "80/tcp,8080/tcp" ||
		image.Architecture != "amd64" || image.User != "app" || image.Created == 0 || len(image.UsedBy) != 1 {
		t.Fatalf("image detail = %+v", image)
	}
	if strings.Contains(strings.Join(image.EnvNames, ""), "hunter2") {
		t.Fatal("an environment value leaked")
	}
	found := false
	for _, call := range e.calls {
		if call == "GET /images/ghcr.io/x/y:tag/json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the registry path must keep its slashes: %v", e.calls)
	}

	network, err := client.InspectResource(context.Background(), protocol.KindNetwork, "n2")
	if err != nil || network.Network == nil || len(network.Network.Members) != 1 || network.Network.Members[0].Name != "web" || network.Network.Options["com.docker.network.bridge.name"] != "br-site" {
		t.Fatalf("network detail = %+v %v", network, err)
	}
	volume, err := client.InspectResource(context.Background(), protocol.KindVolume, "pgdata")
	if err != nil || volume.Volume == nil || volume.Volume.Size != 4096 || volume.Volume.Options["type"] != "none" || len(volume.Volume.UsedBy) != 1 {
		t.Fatalf("volume detail = %+v %v", volume, err)
	}
}

func TestResourceOperationsRefuseBadReferencesBeforeTouchingDocker(t *testing.T) {
	client, e := newResClient(t)
	for _, tc := range []struct{ kind, ref string }{
		{"image", "../etc"}, {"image", "a b"}, {"image", "repo//x"}, {"image", ""}, {"network", "a/b"}, {"volume", "/var/lib"}, {"volume", ".."}, {"secret", "x"}, {"", "x"},
	} {
		if _, err := client.InspectResource(context.Background(), tc.kind, tc.ref); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Inspect(%q,%q) = %v", tc.kind, tc.ref, err)
		}
		if _, err := client.RemoveResource(context.Background(), protocol.DockerResourceParams{Kind: tc.kind, ID: tc.ref}); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Remove(%q,%q) = %v", tc.kind, tc.ref, err)
		}
	}
	for _, ref := range []string{"", "sha256:0123456789ab", "a b", "-x"} {
		if err := client.PullImage(context.Background(), ref, nil); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Pull(%q) = %v", ref, err)
		}
	}
	if _, err := client.Prune(context.Background(), protocol.DockerPruneParams{Kind: "container"}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("Prune of an unknown kind = %v", err)
	}
	if len(e.calls) != 0 {
		t.Fatalf("invalid requests reached Docker: %v", e.calls)
	}
}

func TestRemovalReportsWhatWasRemovedAndKeepsDockersRefusals(t *testing.T) {
	client, e := newResClient(t)
	ctx := context.Background()
	image, err := client.RemoveResource(ctx, protocol.DockerResourceParams{Kind: "image", ID: "redis:7", Force: true})
	if err != nil || len(image.Untagged) != 1 || image.Untagged[0] != "redis:7" || len(image.Deleted) != 1 {
		t.Fatalf("image removal = %+v %v", image, err)
	}
	if !strings.Contains(e.queries[len(e.queries)-1], "force=1") {
		t.Fatalf("force was not sent: %v", e.queries)
	}
	if _, err := client.RemoveResource(ctx, protocol.DockerResourceParams{Kind: "network", ID: "orphan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RemoveResource(ctx, protocol.DockerResourceParams{Kind: "volume", ID: "cache-old"}); err != nil {
		t.Fatal(err)
	}
	var apiErr *APIError
	for _, tc := range []struct {
		params protocol.DockerResourceParams
		status int
		text   string
	}{
		{protocol.DockerResourceParams{Kind: "image", ID: "sha256:imgweb", Force: true}, 409, "being used by running container"},
		{protocol.DockerResourceParams{Kind: "network", ID: "bridge"}, 403, "pre-defined network"},
		{protocol.DockerResourceParams{Kind: "network", ID: "site_default"}, 403, "active endpoints"},
		{protocol.DockerResourceParams{Kind: "volume", ID: "pgdata"}, 409, "in use"},
	} {
		if _, err := client.RemoveResource(ctx, tc.params); !errors.As(err, &apiErr) || apiErr.Status != tc.status || !strings.Contains(apiErr.Message, tc.text) {
			t.Errorf("%+v = %v, want %d containing %q", tc.params, err, tc.status, tc.text)
		}
	}
	// A network removal never sends force, since Docker has no such option for networks.
	for i, call := range e.calls {
		if strings.HasPrefix(call, "DELETE /networks/") && strings.Contains(e.queries[i], "force") {
			t.Errorf("force sent for a network: %s?%s", call, e.queries[i])
		}
	}
}

func TestDryRunPrunesListOnlyWhatIsUnusedAndChangeNothing(t *testing.T) {
	client, e := newResClient(t)
	ctx := context.Background()
	names := func(items []protocol.DockerPruneItem) string {
		var out []string
		for _, item := range items {
			out = append(out, item.ID)
		}
		return strings.Join(out, ",")
	}
	dangling, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "image", DryRun: true})
	if err != nil || !dangling.DryRun || names(dangling.Candidates) != "sha256:imgdangling" {
		t.Fatalf("dangling images: %+v %v (an image used by a stopped container must not be listed)", dangling, err)
	}
	all, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "image", All: true, DryRun: true})
	if err != nil || names(all.Candidates) != "sha256:imgunused,sha256:imgdangling" {
		t.Fatalf("all unused images: %+v %v", all, err)
	}
	if all.Candidates[0].Size != 700 || all.Candidates[0].Name != "redis:7" {
		t.Fatalf("candidate details: %+v", all.Candidates[0])
	}
	networks, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "network", DryRun: true})
	if err != nil || names(networks.Candidates) != "n3" {
		t.Fatalf("networks: %+v %v (built-in and attached networks must never be candidates)", networks, err)
	}
	anon, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "volume", DryRun: true})
	// The only anonymous volume is mounted by a stopped container, and "anon-unused" is a name, not an anonymous ID.
	if err != nil || names(anon.Candidates) != "" {
		t.Fatalf("anonymous volumes: %+v %v", anon, err)
	}
	every, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "volume", All: true, DryRun: true})
	if err != nil || names(every.Candidates) != "anon-unused,cache-old" {
		t.Fatalf("all unused volumes: %+v %v (volumes in use, including by a stopped container, must not be listed)", every, err)
	}
	if every.Candidates[1].Size != 2048 || every.Candidates[0].Size != 0 {
		t.Fatalf("volume candidate sizes = %d, %d (unknown sizes count as zero)", every.Candidates[0].Size, every.Candidates[1].Size)
	}
	if len(e.writeCalls()) != 0 {
		t.Fatalf("a dry run changed something: %v", e.writeCalls())
	}
}

func TestRealPrunesSendTheRightFiltersAndReportTheOutcome(t *testing.T) {
	client, e := newResClient(t)
	ctx := context.Background()
	images, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "image"})
	if err != nil || images.DryRun || len(images.Removed) != 2 || images.SpaceReclaimed != 700 {
		t.Fatalf("image prune = %+v %v", images, err)
	}
	if _, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "image", All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "network"}); err != nil {
		t.Fatal(err)
	}
	volumes, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "volume", All: true})
	if err != nil || len(volumes.Removed) != 1 || volumes.Removed[0] != "cache-old" || volumes.SpaceReclaimed != 2048 {
		t.Fatalf("volume prune = %+v %v", volumes, err)
	}
	if _, err := client.Prune(ctx, protocol.DockerPruneParams{Kind: "volume"}); err != nil {
		t.Fatal(err)
	}
	var filters []string
	for i, call := range e.calls {
		if strings.HasSuffix(call, "/prune") {
			filters = append(filters, call+"?"+e.queries[i])
		}
	}
	joined := strings.Join(filters, "\n")
	for _, want := range []string{"/images/prune?filters=%7B%22dangling%22%3A%5B%22true%22%5D%7D", "/images/prune?filters=%7B%22dangling%22%3A%5B%22false%22%5D%7D", "/networks/prune?", "/volumes/prune?filters=%7B%22all%22%3A%5B%22true%22%5D%7D", "/volumes/prune?\n"} {
		if !strings.Contains(joined+"\n", want) {
			t.Errorf("missing prune request %q in:\n%s", want, joined)
		}
	}
}

func TestPullingStreamsProgressAndFinishes(t *testing.T) {
	client, e := newResClient(t)
	e.pull = `{"status":"Pulling from library/redis","id":"7"}` + "\n" + `{"status":"Downloading","id":"layer1","progressDetail":{"current":5,"total":10}}` + "\n" + `{"status":"Status: Downloaded newer image for redis:7"}` + "\n"
	var steps []string
	if err := client.PullImage(context.Background(), "redis:7", func(ev protocol.DockerPullEvent) { steps = append(steps, ev.Step) }); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(steps, ","); got != "pull,pull,pull_layer,pull,done" {
		t.Fatalf("steps = %s", got)
	}
	e.pull = `{"error":"pull access denied for private/app, repository does not exist"}` + "\n"
	err := client.PullImage(context.Background(), "private/app:1", nil)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("a failed pull must explain why: %v", err)
	}
}
