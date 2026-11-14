package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/migrations"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func logValueTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("log_values_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		admin.Close(context.Background())
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if err := migrations.Apply(u.String()); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var device string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('capture test') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_identities(id,device_id,credential_hash,protocol_version) VALUES($1::uuid,$2::uuid,$3,'1')`, agentDocker, device, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestSavedValuesPersistAndInvalidateAcrossServerRuns(t *testing.T) {
	pool := logValueTestPool(t)
	ctx := context.Background()
	gameID := strings.Repeat("a", 64)
	var mu sync.Mutex
	started := "2026-11-11T01:00:57Z"
	code := "ABCD-1234"
	available := true
	editDuringSearch := false
	searches := []protocol.DockerLogSearchParams{}
	f := newFixture(t, func(c *Config) {
		c.Pool = pool
		c.Grants = fixedGrants{{AgentID: agentDocker, Kind: "stack", Resource: "dragonwilds"}}
	})
	agent := f.connectAgent(agentDocker)
	agent.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		mu.Lock()
		defer mu.Unlock()
		var result any
		switch call.Method {
		case protocol.MethodDockerList:
			result = protocol.DockerListResult{Containers: []protocol.DockerContainerInfo{{ID: gameID, Project: "dragonwilds", Service: "game", State: "running"}}}
		case protocol.MethodDockerInspect:
			result = protocol.DockerInspectResult{ID: gameID, State: protocol.DockerState{Running: true, StartedAt: started}, Labels: map[string]string{"com.docker.compose.project": "dragonwilds", "com.docker.compose.service": "game"}}
		case protocol.MethodDockerLogSearch:
			if editDuringSearch {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, err := pool.Exec(ctx, `UPDATE container_log_values SET match_text='NewMatch',value='',run_id='',cursor_at='',captured_at='',state='waiting',version=version+1`); err != nil {
					t.Error("capture held a database connection or edit failed", err)
				}
			}
			var p protocol.DockerLogSearchParams
			_ = json.Unmarshal(call.Params, &p)
			searches = append(searches, p)
			lines := []protocol.DockerLogLine{}
			if available {
				stamp, _ := time.Parse(time.RFC3339, started)
				lines = append(lines, protocol.DockerLogLine{T: stamp.Add(30 * time.Second).Format(time.RFC3339), M: "JoinCode value[" + code + "]"})
			}
			result = protocol.DockerLogSearchResult{Lines: lines, NextSince: started}
		default:
			t.Errorf("unexpected method %s", call.Method)
		}
		data, _ := json.Marshal(result)
		return protocol.ChannelFrame{Result: data}
	})
	path := "/api/containers/" + agentDocker + "/" + gameID + "/values"
	resp := f.post(path, auth.RoleAdmin, `{"label":"Join code","match":"JoinCode","prefix":"value[","suffix":"]","shared":true}`, nil)
	var created struct{ ID string }
	decode(t, resp, &created)
	if resp.StatusCode != 200 || created.ID == "" {
		t.Fatalf("create rule: %d %+v", resp.StatusCode, created)
	}
	cfg := Config{Pool: pool, Hub: f.hub}
	a := &api{Config: cfg}
	a.captureLogValue(ctx, created.ID)
	// A second API instance sees the persisted capture, with no browser or raw-log permission.
	a = &api{Config: cfg}
	resp = f.get(path, auth.RoleRestricted)
	body := bodyText(resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "ABCD-1234") || strings.Contains(body, "prefix") || strings.Contains(body, "match") {
		t.Fatalf("shared value response: %d %s", resp.StatusCode, body)
	}
	mu.Lock()
	available = false
	mu.Unlock()
	a.captureLogValue(ctx, created.ID)
	if body := bodyText(f.get(path, auth.RoleRestricted)); !strings.Contains(body, "ABCD-1234") {
		t.Fatalf("rotated logs lost persisted code: %s", body)
	}
	// Sharing or renaming a card must not discard a value whose logs have rotated away.
	requestBody := `{"label":"World code","match":"JoinCode","prefix":"value[","suffix":"]","shared":true,"version":1}`
	req, err := http.NewRequest(http.MethodPut, f.server.URL+path+"/"+created.ID, strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "role", Value: auth.RoleAdmin})
	updated, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer updated.Body.Close()
	if updated.StatusCode != 200 {
		t.Fatalf("edit card: %d %s", updated.StatusCode, bodyText(updated))
	}
	if body := bodyText(f.get(path, auth.RoleRestricted)); !strings.Contains(body, "ABCD-1234") {
		t.Fatal("rename discarded captured value", body)
	}
	// The read path invalidates the old code even before the worker notices a restart.
	mu.Lock()
	started = "2026-11-13T02:00:00Z"
	mu.Unlock()
	if body := bodyText(f.get(path, auth.RoleRestricted)); strings.Contains(body, "ABCD-1234") || !strings.Contains(body, "waiting") {
		t.Fatalf("stale run remained visible: %s", body)
	}
	a.captureLogValue(ctx, created.ID)
	mu.Lock()
	code = "NEW1-CODE"
	available = true
	mu.Unlock()
	a.captureLogValue(ctx, created.ID)
	if body := bodyText(f.get(path, auth.RoleRestricted)); !strings.Contains(body, "NEW1-CODE") || strings.Contains(body, "ABCD-1234") {
		t.Fatalf("new run code: %s", body)
	}
	mu.Lock()
	last := searches[len(searches)-1]
	mu.Unlock()
	if last.Since != "2026-11-13T02:00:00Z" {
		t.Fatalf("new run scanned old history: %+v", last)
	}
	if _, err := pool.Exec(ctx, `UPDATE container_log_values SET checked_at=now()-interval '2 minutes' WHERE id=$1::uuid`, created.ID); err != nil {
		t.Fatal(err)
	}
	if body := bodyText(f.get(path, auth.RoleRestricted)); strings.Contains(body, "NEW1-CODE") || !strings.Contains(body, "stale") {
		t.Fatalf("old capture status remained current: %s", body)
	}
	mu.Lock()
	editDuringSearch = true
	mu.Unlock()
	a.captureLogValue(ctx, created.ID)
	var storedValue, storedState string
	if err := pool.QueryRow(ctx, `SELECT value,state FROM container_log_values WHERE id=$1::uuid`, created.ID).Scan(&storedValue, &storedState); err != nil {
		t.Fatal(err)
	}
	if storedValue != "" || storedState != "waiting" {
		t.Fatalf("late scan overwrote an edited rule: value=%q state=%s", storedValue, storedState)
	}
}
