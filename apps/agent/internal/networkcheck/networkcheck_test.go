package networkcheck

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoopbackHTTPAndTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	checks, err := Load(writeConfig(t, config{Checks: []Check{{Name: "web", Type: "http", URL: srv.URL + "/health"}, {Name: "tcp", Type: "tcp", Address: l.Addr().String()}}}))
	if err != nil {
		t.Fatal(err)
	}
	got := Run(context.Background(), checks)
	if len(got) != 2 || !got[0].OK || !got[1].OK || got[0].Latency <= 0 {
		t.Fatalf("results = %+v", got)
	}
}

func TestRejectsUnsafeTargetsAndBounds(t *testing.T) {
	cases := []config{
		{Checks: []Check{{Name: "x", Type: "http", URL: "http://user:secret@127.0.0.1/"}}},
		{Checks: []Check{{Name: "x", Type: "http", URL: "http://127.0.0.1/?token=secret"}}},
		{Checks: []Check{{Name: "x", Type: "http", URL: "http://example.com/"}}},
		{Checks: []Check{{Name: "x", Type: "tcp", Address: "8.8.8.8:53"}}},
		{Checks: []Check{{Name: "x", Type: "tcp", Address: "127.0.0.1:80", Timeout: 3001}}},
		{Checks: []Check{{Name: "http://user:secret", Type: "tcp", Address: "127.0.0.1:80"}}},
		{Checks: []Check{{Name: "x", Type: "tcp", Address: "127.0.0.1:80"}, {Name: "x", Type: "tcp", Address: "127.0.0.1:81"}}},
	}
	for _, cfg := range cases {
		if _, err := Load(writeConfig(t, cfg)); err == nil {
			t.Fatalf("unsafe config accepted: %+v", cfg)
		}
	}
	many := make([]Check, MaxChecks+1)
	for i := range many {
		many[i] = Check{Name: string(rune('a' + i)), Type: "tcp", Address: "127.0.0.1:80"}
	}
	if _, err := Load(writeConfig(t, config{Checks: many})); err == nil {
		t.Fatal("too many checks accepted")
	}
}

func TestRedirectNotFollowedAndResultDoesNotExposeTarget(t *testing.T) {
	secret := "private-endpoint-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/"+secret, http.StatusFound) }))
	defer srv.Close()
	checks, err := Load(writeConfig(t, config{Checks: []Check{{Name: "safe", Type: "http", URL: srv.URL}}}))
	if err != nil {
		t.Fatal(err)
	}
	got := Run(context.Background(), checks)
	if len(got) != 1 || got[0].OK {
		t.Fatalf("redirect counted healthy: %+v", got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), secret) || strings.Contains(string(data), srv.URL) {
		t.Fatalf("target leaked: %s", data)
	}
}

func TestPerCheckTimeoutIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	checks, err := Load(writeConfig(t, config{Checks: []Check{{Name: "slow", Type: "http", URL: srv.URL, Timeout: 150}}}))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := Run(context.Background(), checks)
	if time.Since(start) > time.Second || len(got) != 1 || got[0].OK {
		t.Fatalf("timeout not bounded: %s %+v", time.Since(start), got)
	}
}
func writeConfig(t *testing.T, cfg config) string {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "checks.json")
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
