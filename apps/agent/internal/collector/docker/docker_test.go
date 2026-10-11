package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deranjer/vigara/internal/modules"
)

const id1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDisabledDoesNotCallEndpoint(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected", 500) }))
	defer s.Close()
	c, err := New(Config{Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	r := c.Collect(context.Background())
	if calls != 0 || len(r.Metrics) != 0 || len(r.Containers) != 0 || len(r.Errors) != 0 {
		t.Fatalf("disabled collector made calls or reported data: calls=%d result=%+v", calls, r)
	}
}

func TestEndpointRequiresExplicitLocalAccess(t *testing.T) {
	for _, host := range []string{"", "http://example.com:2375", "http://192.0.2.1:2375", "http://user:pass@127.0.0.1:2375"} {
		if _, err := New(Config{Enabled: true, Host: host}); err == nil {
			t.Errorf("accepted unsafe or empty endpoint %q", host)
		}
	}
	if _, err := New(Config{Enabled: true, Host: "unix:///var/run/docker.sock"}); err != nil {
		t.Fatalf("rejected explicit unix socket: %v", err)
	}
}

func TestInvalidInventoryEntryIsSkippedAndStringsStayUTF8(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `[{"Id":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","State":"unknown"},{"Id":%q,"Names":["/%s"],"Image":"demo","State":"exited"}]`, id1, strings.Repeat("🧪", 40))
	}))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	r := c.Collect(context.Background())
	if len(r.Containers) != 1 || len(r.Errors) != 1 {
		t.Fatalf("invalid record was not isolated: inventory=%+v errors=%+v", r.Containers, r.Errors)
	}
	if len(r.Containers[0].Name) > 128 || !utf8.ValidString(r.Containers[0].Name) {
		t.Fatalf("container name is not bounded valid UTF-8: %q", r.Containers[0].Name)
	}
}

func TestModuleDescriptorAndPermissionGate(t *testing.T) {
	c, err := New(Config{Enabled: true, Host: "unix:///var/run/docker.sock"})
	if err != nil {
		t.Fatal(err)
	}
	d := c.Descriptor()
	if d.ID != ModuleID || len(d.Permissions) != 1 || d.Permissions[0].Name != "docker.read" {
		t.Fatalf("unexpected module descriptor: %+v", d)
	}
	if _, err := c.Execute(context.Background(), modules.JobContext{}); err == nil {
		t.Fatal("executed Docker module without explicit permission")
	}
}

func TestCollectInventoryResourceBaselinesAndFailureIsolation(t *testing.T) {
	cycle := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected Docker method %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1.44/") {
			t.Errorf("unexpected API version path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1.44/containers/json":
			_, _ = fmt.Fprintf(w, `[{"Id":%q,"Names":["/web"],"Image":"demo:latest","State":"running"},{"Id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","Names":["/offline"],"Image":"demo:old","State":"exited"},{"Id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","Names":["/broken"],"Image":"demo:bad","State":"running"}]`, id1)
		case strings.Contains(r.URL.Path, id1):
			cycle++
			cpu := uint64(100)
			sys := uint64(1000)
			rx0, tx0, rx1, tx1 := uint64(100), uint64(200), uint64(50), uint64(20)
			if cycle > 1 {
				cpu = 300
				sys = 2000
				rx0, tx0, rx1, tx1 = 300, 400, 100, 120
			}
			pids := uint64(3)
			usage, limit := uint64(1000), uint64(2000)
			payload := map[string]any{"cpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": cpu, "percpu_usage": []uint64{cpu / 2, cpu / 2}}, "system_cpu_usage": sys, "online_cpus": 2}, "memory_stats": map[string]any{"usage": usage, "limit": limit}, "pids_stats": map[string]any{"current": pids}, "networks": map[string]any{"eth0": map[string]uint64{"rx_bytes": rx0, "tx_bytes": tx0}, "eth1": map[string]uint64{"rx_bytes": rx1, "tx_bytes": tx1}}}
			_ = json.NewEncoder(w).Encode(payload)
		default:
			http.Error(w, "synthetic failure", http.StatusInternalServerError)
		}
	}))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	first := c.Collect(context.Background())
	if len(first.Containers) != 3 {
		t.Fatalf("inventory count=%d, want 3", len(first.Containers))
	}
	if metric(first, "docker_network_receive_bytes_per_second") || metric(first, "docker_cpu_capacity_used_percent") {
		t.Fatal("reported rate without a baseline")
	}
	if len(first.Errors) != 1 || first.Errors[0].Capability != "docker_container_resources" {
		t.Fatalf("failed container did not remain isolated: %+v", first.Errors)
	}
	time.Sleep(20 * time.Millisecond)
	second := c.Collect(context.Background())
	if got := metricValue(second, "docker_memory_used_percent"); got != 50 {
		t.Fatalf("memory percent=%v, want 50", got)
	}
	if got := metricValue(second, "docker_cpu_capacity_used_percent"); got < 19.9 || got > 20.1 {
		t.Fatalf("host capacity CPU percent=%v, want about 20", got)
	}
	if got := metricValue(second, "docker_network_receive_bytes_per_second"); got < 1 {
		t.Fatalf("network receive rate=%v, expected positive aggregate delta", got)
	}
	if metric(second, "docker_processes") == false {
		t.Fatal("missing process gauge")
	}
	if len(second.Errors) != 1 {
		t.Fatalf("resource failure was not isolated: %+v", second.Errors)
	}
	for _, m := range second.Metrics {
		if strings.HasPrefix(m.Name, "docker_") && len(m.Labels) != 1 {
			t.Errorf("resource labels should contain stable ID only: %+v", m)
		}
	}
}

func TestMissingDataIsNotReportedAsZero(t *testing.T) {
	state := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1.44/containers/json" {
			_, _ = fmt.Fprintf(w, `[{"Id":%q,"State":"running"}]`, id1)
			return
		}
		state++
		_, _ = w.Write([]byte(`{"memory_stats":{},"cpu_stats":{},"pids_stats":{},"networks":{}}`))
	}))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	result := c.Collect(context.Background())
	if state != 1 || len(result.Metrics) != 0 {
		t.Fatalf("missing fields produced measurements: %+v", result.Metrics)
	}
}

func metric(r Result, name string) bool {
	for _, m := range r.Metrics {
		if m.Name == name {
			return true
		}
	}
	return false
}
func metricValue(r Result, name string) float64 {
	for _, m := range r.Metrics {
		if m.Name == name {
			return m.Value
		}
	}
	return -1
}

func TestInvalidInventoryIsIsolatedAndStringsRemainValidUTF8(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]container{{ID: id1, Names: []string{strings.Repeat("猫", 60)}, Image: strings.Repeat("犬", 60), State: "exited"}, {ID: strings.Repeat("b", 64), State: "unknown"}})
	}))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	result := c.Collect(context.Background())
	if len(result.Containers) != 1 || len(result.Errors) != 1 {
		t.Fatalf("malformed inventory not isolated: %+v", result)
	}
	item := result.Containers[0]
	if len(item.Name) > 128 || len(item.Image) > 128 || !utf8.ValidString(item.Name) || !utf8.ValidString(item.Image) {
		t.Fatalf("inventory strings exceed wire bounds: %+v", item)
	}
}

func TestInventoryCapIsStableAndExplicit(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []container{}
		for i := 40; i > 0; i-- {
			items = append(items, container{ID: fmt.Sprintf("%064x", i), State: "exited"})
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	result := c.Collect(context.Background())
	if len(result.Containers) != 32 || len(result.Errors) != 1 || result.Containers[0].ID != fmt.Sprintf("%064x", 1) || result.Containers[31].ID != fmt.Sprintf("%064x", 32) {
		t.Fatalf("inventory cap or order incorrect: %+v", result)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	reached := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; _, _ = w.Write([]byte("[]")) }))
	defer destination.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	result := c.Collect(context.Background())
	if reached || len(result.Errors) != 1 {
		t.Fatalf("redirect followed or failure missing: %+v", result)
	}
}
