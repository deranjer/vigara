package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/apps/api/internal/inventory"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/go-chi/chi/v5"
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
	for _, name := range []string{"000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql", "000004_inventory.up.sql"} {
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

	requestBytes, _ := json.Marshal(EnrollmentRequest{Token: token.Token, DisplayName: "test-device", Capability: protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: []string{"module.cpu"}}, Identifiers: []protocol.DeviceIdentifier{{Kind: "machine-id", Value: "abcdef0123456789abcdef0123456789"}}})
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
	var deviceID string
	if err := pool.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, identity.AgentID).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	var identifierCount, sourceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_identifiers WHERE device_id=$1`, deviceID).Scan(&identifierCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_sources WHERE device_id=$1 AND source_type='agent'`, deviceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if identifierCount != 1 || sourceCount != 1 {
		t.Fatalf("enrollment inventory records: identifiers=%d agent-sources=%d", identifierCount, sourceCount)
	}
	var provenanceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_identifier_sources WHERE provenance=$1`, inventory.SourceProvenance("agent", identity.AgentID)).Scan(&provenanceCount); err != nil {
		t.Fatal(err)
	}
	if provenanceCount != 1 {
		t.Fatalf("enrollment identifier provenance rows=%d", provenanceCount)
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

func TestInventoryReconciliationConcurrencyAndConflict(t *testing.T) {
	pool, _ := agentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const n = 8
	start := make(chan struct{})
	ids := make(chan string, n)
	errs := make(chan error, n)
	var group sync.WaitGroup
	for i := 0; i < n; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			id, err := inventory.ResolveDevice(ctx, tx, inventory.Observation{DisplayName: fmt.Sprintf("host-%d", i), Provenance: "agent", Identifiers: []protocol.DeviceIdentifier{{Kind: "machine-id", Value: "abcdef0123456789abcdef0123456789"}}})
			if err == nil {
				err = inventory.AttachSource(ctx, tx, id, "agent", fmt.Sprintf("agent-%d", i), "agent-enrollment")
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				errs <- err
				return
			}
			ids <- id
		}(i)
	}
	close(start)
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	want := ""
	for id := range ids {
		if want == "" {
			want = id
		} else if id != want {
			t.Fatalf("race created multiple devices: %s and %s", want, id)
		}
	}

	// The same stable key reported with another provenance is retained, and
	// hostname-only observations remain separate devices.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := inventory.ResolveDevice(ctx, tx, inventory.Observation{DisplayName: "renamed", Provenance: "scanner", Identifiers: []protocol.DeviceIdentifier{{Kind: "machine-id", Value: "abcdef0123456789abcdef0123456789"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if secondID != want {
		t.Fatalf("stable ID created %s, expected %s", secondID, want)
	}
	var provenanceCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM device_identifier_sources`).Scan(&provenanceCount); err != nil {
		t.Fatal(err)
	}
	if provenanceCount != 2 {
		t.Fatalf("got %d provenance records, want 2", provenanceCount)
	}
	otherTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := inventory.ResolveDevice(ctx, otherTx, inventory.Observation{DisplayName: "other", Provenance: "scanner", Identifiers: []protocol.DeviceIdentifier{{Kind: "machine-id", Value: "fedcba9876543210fedcba9876543210"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = otherTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	conflictTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = inventory.ResolveDevice(ctx, conflictTx, inventory.Observation{DisplayName: "ambiguous", Provenance: "scanner", Identifiers: []protocol.DeviceIdentifier{{Kind: "machine-id", Value: "abcdef0123456789abcdef0123456789"}, {Kind: "machine-id", Value: "fedcba9876543210fedcba9876543210"}}})
	_ = conflictTx.Rollback(ctx)
	if !errors.Is(err, inventory.ErrAmbiguousIdentity) {
		t.Fatalf("conflicting keys returned %v", err)
	}
	if otherID == want {
		t.Fatal("expected separate stable IDs to remain separate")
	}
	nameOnlyTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nameOnlyA, err := inventory.ResolveDevice(ctx, nameOnlyTx, inventory.Observation{DisplayName: "same-host", Provenance: "scanner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = nameOnlyTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	nameOnlyTx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nameOnlyB, err := inventory.ResolveDevice(ctx, nameOnlyTx, inventory.Observation{DisplayName: "same-host", Provenance: "scanner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = nameOnlyTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if nameOnlyA == nameOnlyB {
		t.Fatal("hostname alone merged separate device observations")
	}
}

func TestDiscoveryMountedThroughChiRouter(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "operator", "Operator", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	agentMux := http.NewServeMux()
	NewHandler(pool, sessions).Register(agentMux)
	router := chi.NewRouter()
	router.Handle("/api/devices/*", agentMux)
	request := func(sourceID string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"sourceId":%q,"displayName":"Host","identifiers":[{"kind":"machine-id","value":"abcdef0123456789abcdef0123456789"}],"addresses":[{"value":"192.0.2.10","type":"ipv4"}]}`, sourceID)
		r := httptest.NewRequest(http.MethodPost, "/api/devices/discovery", bytes.NewBufferString(body))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	forgedRequest := httptest.NewRequest(http.MethodPost, "/api/devices/discovery", bytes.NewBufferString(`{"sourceId":"scanner-1","displayName":"Host","addresses":[{"value":"192.0.2.10","type":"ipv4","provenance":"caller-forgery"}]}`))
	forgedRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	forgedResponse := httptest.NewRecorder()
	router.ServeHTTP(forgedResponse, forgedRequest)
	if forgedResponse.Code != http.StatusBadRequest {
		t.Fatalf("caller-controlled provenance returned %d", forgedResponse.Code)
	}
	first := request("scanner-1")
	if first.Code != http.StatusOK {
		t.Fatalf("discovery returned %d: %s", first.Code, first.Body.String())
	}
	second := request("scanner-2")
	if second.Code != http.StatusOK {
		t.Fatalf("repeat discovery returned %d: %s", second.Code, second.Body.String())
	}
	var one, two struct {
		DeviceID string `json:"deviceId"`
	}
	if err = json.Unmarshal(first.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(second.Body.Bytes(), &two); err != nil {
		t.Fatal(err)
	}
	if one.DeviceID == "" || one.DeviceID != two.DeviceID {
		t.Fatalf("source re-observation changed device: %q -> %q", one.DeviceID, two.DeviceID)
	}
	var sources, identifierSources, addresses, forged int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM device_sources WHERE device_id=$1 AND source_type='external' AND source_id IN ('scanner-1','scanner-2')`, one.DeviceID).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM device_identifier_sources`).Scan(&identifierSources); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM device_addresses WHERE device_id=$1`, one.DeviceID).Scan(&addresses); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM device_addresses WHERE provenance='caller-forgery'`).Scan(&forged); err != nil {
		t.Fatal(err)
	}
	if sources != 2 || identifierSources != 2 || addresses != 2 || forged != 0 {
		t.Fatalf("external provenance was not retained or caller controlled: sources=%d identifiers=%d addresses=%d forged=%d", sources, identifierSources, addresses, forged)
	}
}
