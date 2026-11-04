package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

type updateFixture struct {
	r            *runner
	plan         protocol.AgentUpdatePlan
	packageData  []byte
	healthy      bool
	noPlan       bool
	serviceCalls []string
	statuses     []string
	mu           sync.Mutex
}

func fixture(t *testing.T) *updateFixture {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"operations.lock", "agent"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old executable"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	f := &updateFixture{packageData: []byte("new executable"), healthy: true}
	digest := sha256.Sum256(f.packageData)
	f.plan = protocol.AgentUpdatePlan{Version: "new", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(f.packageData)), Generation: 1}
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-device-credential" {
			t.Error("missing enrolled credential")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/agent/update-plan":
			if f.noPlan {
				w.WriteHeader(204)
			} else {
				json.NewEncoder(w).Encode(f.plan)
			}
		case "/api/agent/update-package/" + f.plan.SHA256:
			w.Write(f.packageData)
		case "/api/agent/update-health":
			if r.URL.Query().Get("version") == "" {
				json.NewEncoder(w).Encode(map[string]any{"lastSeenAt": time.Unix(100, 0).UTC()})
			} else {
				json.NewEncoder(w).Encode(map[string]bool{"healthy": f.healthy})
			}
		case "/api/agent/update-status":
			var status protocol.AgentUpdateStatus
			if json.NewDecoder(r.Body).Decode(&status) != nil {
				t.Error("invalid status")
			}
			f.mu.Lock()
			f.statuses = append(f.statuses, status.State)
			f.mu.Unlock()
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	client := s.Client()
	client.Timeout = time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	f.r = &runner{config: enrollment{ServerURL: s.URL, Credential: "private-device-credential"}, client: client, directory: dir, binary: filepath.Join(dir, "agent"), healthTimeout: 30 * time.Millisecond, service: func(action string) error { f.serviceCalls = append(f.serviceCalls, action); return nil }}
	return f
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpdateReplacesBinaryAndRetainsRecoveryCopy(t *testing.T) {
	f := fixture(t)
	if err := f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.r.binary); got != "new executable" {
		t.Fatalf("installed %q", got)
	}
	if got := readFile(t, f.r.binary+".update-old"); got != "old executable" {
		t.Fatalf("backup %q", got)
	}
	if !reflect.DeepEqual(f.serviceCalls, []string{"stop", "start"}) {
		t.Fatalf("service calls %v", f.serviceCalls)
	}
	if err := f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.serviceCalls) != 2 {
		t.Fatal("same package restarted service twice")
	}
}

func TestInvalidPackagesNeverStopService(t *testing.T) {
	for _, kind := range []string{"wrong checksum", "truncated", "oversized", "invalid plan"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t)
			switch kind {
			case "wrong checksum":
				f.packageData = []byte("bad executable")
			case "truncated":
				f.packageData = []byte("short")
			case "oversized":
				f.packageData = append(f.packageData, 'x')
			case "invalid plan":
				f.plan.SHA256 = "../../outside"
			}
			if err := f.r.run(context.Background()); err == nil {
				t.Fatal("accepted invalid package")
			}
			if len(f.serviceCalls) != 0 || readFile(t, f.r.binary) != "old executable" {
				t.Fatal("invalid download modified running service")
			}
		})
	}
}

func TestFailedHealthRollsBackAndRequiresExplicitRetry(t *testing.T) {
	f := fixture(t)
	f.healthy = false
	if err := f.r.run(context.Background()); err == nil {
		t.Fatal("expected health failure")
	}
	if readFile(t, f.r.binary) != "old executable" {
		t.Fatal("old executable was not restored")
	}
	if !reflect.DeepEqual(f.serviceCalls, []string{"stop", "start", "stop", "start"}) {
		t.Fatalf("service calls %v", f.serviceCalls)
	}
	if err := f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.serviceCalls) != 4 {
		t.Fatal("failed release retried without approval")
	}
	f.healthy = true
	f.plan.Generation++
	if err := f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readFile(t, f.r.binary) != "new executable" {
		t.Fatal("explicit retry did not install package")
	}
}

func TestInterruptedInstallRecoversBeforeFetchingPlan(t *testing.T) {
	for _, phase := range []string{"installing", "checking"} {
		t.Run(phase, func(t *testing.T) {
			f := fixture(t)
			f.noPlan = true
			if err := os.WriteFile(f.r.binary+".update-old", []byte("recovery executable"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(f.r.directory, "journal.json"), journal{Plan: f.plan, Phase: phase}); err != nil {
				t.Fatal(err)
			}
			if err := f.r.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if readFile(t, f.r.binary) != "recovery executable" {
				t.Fatal("interrupted install was not recovered")
			}
		})
	}
}

func TestStopFailurePreservesExecutable(t *testing.T) {
	f := fixture(t)
	f.r.service = func(string) error { return errors.New("service busy") }
	if err := f.r.run(context.Background()); err == nil {
		t.Fatal("expected stop failure")
	}
	if readFile(t, f.r.binary) != "old executable" {
		t.Fatal("replaced binary despite stop failure")
	}
	f.noPlan = true
	f.r.service = func(action string) error { f.serviceCalls = append(f.serviceCalls, action); return nil }
	if err := f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.serviceCalls, []string{"stop", "start"}) {
		t.Fatalf("stop timeout did not recover service: %v", f.serviceCalls)
	}
}

func TestActiveWorkDefersInstallationAndRecovery(t *testing.T) {
	f := fixture(t)
	release, err := acquire(context.Background(), filepath.Join(f.r.directory, "operations.lock"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = f.r.run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.serviceCalls) != 0 {
		t.Fatal("stopped agent while active work held lock")
	}
	release()
	if err = f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readFile(t, f.r.binary) != "new executable" {
		t.Fatal("deferred update never resumed")
	}
}

func TestOperationsAreSharedAndExclusiveLockBlocksNewWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	a, err := acquire(context.Background(), path, false, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := acquire(context.Background(), path, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = acquire(ctx, path, true, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive acquisition %v", err)
	}
	a()
	b()
	x, err := acquire(context.Background(), path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer x()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err = acquire(ctx2, path, false, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared acquisition %v", err)
	}
}

func TestUpdaterRejectsUntrustedOrigins(t *testing.T) {
	for _, origin := range []string{"http://localhost", "http://192.168.1.2", "https://user:secret@example.com", "https://example.com?token=secret", "https://example.com#fragment", "file:///tmp/app"} {
		if validateOrigin(origin) == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	if err := validateOrigin("https://example.com/vigara"); err != nil {
		t.Fatal(err)
	}
}

func TestPackageRedirectDoesNotForwardCredentials(t *testing.T) {
	f := fixture(t)
	var forwarded bool
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer other.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer origin.Close()
	f.r.config.ServerURL = origin.URL
	f.r.client = origin.Client()
	f.r.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if err := f.r.download(context.Background(), f.plan, filepath.Join(f.r.directory, "download")); err == nil {
		t.Fatal("accepted redirect")
	}
	if forwarded {
		t.Fatal("enrollment credential forwarded to another origin")
	}
}
