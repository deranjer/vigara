package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func agentTestService(t *testing.T) (*pgxpool.Pool, *auth.Service) {
	t.Helper()
	url := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	schema := pgx.Identifier{fmt.Sprintf("agent_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql"} {
		migration, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool, auth.NewService(pool, db.New(pool))
}

func TestEnrollmentScopeSingleUsePollAndRevocation(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sessions.CreateFirstUser(ctx, "operator", "Operator", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, sessions)
	mux := http.NewServeMux()
	handler.Register(mux)

	create := httptest.NewRequest(http.MethodPost, "/api/agents/enrollment-tokens", bytes.NewBufferString(`{"capabilities":["module.cpu"],"expiresInHours":2}`))
	create.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	createResult := httptest.NewRecorder()
	mux.ServeHTTP(createResult, create)
	if createResult.Code != http.StatusOK {
		t.Fatalf("create enrollment token returned %d: %s", createResult.Code, createResult.Body.String())
	}
	var token EnrollmentTokenResponse
	if err := json.Unmarshal(createResult.Body.Bytes(), &token); err != nil || token.Token == "" {
		t.Fatalf("decode enrollment token: %v", err)
	}

	requestBytes, _ := json.Marshal(EnrollmentRequest{Token: token.Token, DisplayName: "test-device", Capability: protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"module.cpu"}}})
	start := make(chan struct{})
	statuses := make(chan int, 2)
	identities := make(chan EnrollmentResponse, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			r := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(requestBytes))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			statuses <- w.Code
			if w.Code == http.StatusOK {
				var identity EnrollmentResponse
				_ = json.Unmarshal(w.Body.Bytes(), &identity)
				identities <- identity
			}
		}()
	}
	close(start)
	group.Wait()
	close(statuses)
	close(identities)
	twins, failures := 0, 0
	for status := range statuses {
		if status == http.StatusOK {
			twins++
		} else if status == http.StatusUnauthorized {
			failures++
		} else {
			t.Fatalf("unexpected concurrent enrollment status %d", status)
		}
	}
	if twins != 1 || failures != 1 {
		t.Fatalf("single-use token outcomes: success=%d rejected=%d", twins, failures)
	}
	identity := <-identities
	var storedHash []byte
	if err := pool.QueryRow(ctx, `SELECT credential_hash FROM agent_identities WHERE id=$1`, identity.AgentID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(storedHash, []byte(identity.Credential)) || len(storedHash) != 32 {
		t.Fatal("agent credential was not stored as a SHA-256 hash")
	}

	poll := func(caps []string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: identity.AgentID, AgentVersion: "test", Platform: "linux", Architecture: "amd64", Capabilities: caps})
		r := httptest.NewRequest(http.MethodPost, "/api/agent/poll", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+identity.Credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if response := poll([]string{"module.cpu"}); response.Code != http.StatusOK {
		t.Fatalf("poll returned %d: %s", response.Code, response.Body.String())
	}
	if response := poll([]string{"module.cpu", "module.admin"}); response.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope poll returned %d", response.Code)
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/agents/"+identity.AgentID+"/revoke", nil)
	revoke.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	revokeResult := httptest.NewRecorder()
	mux.ServeHTTP(revokeResult, revoke)
	if revokeResult.Code != http.StatusNoContent {
		t.Fatalf("revoke returned %d: %s", revokeResult.Code, revokeResult.Body.String())
	}
	if response := poll([]string{"module.cpu"}); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked identity poll returned %d", response.Code)
	}
}
