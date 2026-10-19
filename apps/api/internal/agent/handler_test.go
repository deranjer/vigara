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
	if _, err := admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); err != nil {
		t.Fatalf("bootstrap shared test extension: %v", err)
	}
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
	for _, name := range []string{"000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql", "000004_inventory.up.sql", "000009_backup_policies.up.sql", "000010_backup_coordination.up.sql", "000011_backup_progress.up.sql", "000014_agent_download_sessions.up.sql"} {
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

func TestBackupLeaseRedeliveryAndIdempotentResult(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	device, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := createTestBackupJob(ctx, pool, device.agentID)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, sessions).Register(mux)
	poll := func(supportsProgress ...bool) protocol.AgentResponse {
		request := protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: device.agentID, AgentVersion: "test", Platform: "linux", Architecture: "amd64", Capabilities: []string{"restic.backup"}}
		if len(supportsProgress) > 0 {
			request.SupportsBackupProgress = supportsProgress[0]
		}
		body, _ := json.Marshal(request)
		r := httptest.NewRequest(http.MethodPost, "/api/agent/poll", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+device.credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("poll status %d: %s", w.Code, w.Body.String())
		}
		var response protocol.AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := poll()
	second := poll()
	if first.Job == nil || second.Job == nil || first.Job.ID != jobID || second.Job.ID != jobID || first.Job.AttemptToken == "" || first.Job.AttemptToken != second.Job.AttemptToken {
		t.Fatalf("active lease was not redelivered consistently: first=%+v second=%+v", first.Job, second.Job)
	}
	legacy := poll()
	modern := poll(true)
	if legacy.BackupProgressEnabled || legacy.Job == nil || legacy.Job.TimeoutSeconds != int(backupLease/time.Second) {
		t.Fatalf("legacy negotiation changed bounded job behavior: %+v", legacy)
	}
	if !modern.BackupProgressEnabled || modern.Job == nil || modern.Job.TimeoutSeconds != int(backupExecutionLimit/time.Second) {
		t.Fatalf("progress negotiation did not enable extended lease deadline: %+v", modern)
	}
	now := time.Now().UTC()
	result := protocol.JobResult{ProtocolVersion: protocol.CurrentVersion, ID: "stable-result", JobID: jobID, AttemptToken: first.Job.AttemptToken, AgentID: device.agentID, ModuleID: "restic.backup", ModuleVersion: "1.0.0", Status: protocol.JobSucceeded, StartedAt: now, FinishedAt: now}
	postResult := func() int {
		body, _ := json.Marshal(ReportBatch{Results: []protocol.JobResult{result}})
		r := httptest.NewRequest(http.MethodPost, "/api/agent/reports", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+device.credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if status := postResult(); status != http.StatusAccepted {
		t.Fatalf("result status %d", status)
	}
	if status := postResult(); status != http.StatusAccepted {
		t.Fatalf("idempotent result retry status %d", status)
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM backup_jobs WHERE id=$1`, jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("job status after result=%s", status)
	}
	var uncertainJob string
	if err = pool.QueryRow(ctx, `INSERT INTO backup_jobs(policy_id,agent_id,profile_id,policy_revision,policy_snapshot,scheduled_for,status) SELECT policy_id,agent_id,profile_id,1,'{"excluded_paths":[]}',now()+interval '1 minute','pending' FROM backup_jobs WHERE id=$1 RETURNING id::text`, jobID).Scan(&uncertainJob); err != nil {
		t.Fatal(err)
	}
	issued := poll()
	if issued.Job == nil || issued.Job.ID != uncertainJob {
		t.Fatalf("next job was not leased: %+v", issued.Job)
	}
	if _, err = pool.Exec(ctx, `UPDATE backup_job_attempts SET leased_at=now()-interval '2 seconds',lease_expires_at=now()-interval '1 second' WHERE job_id=$1`, uncertainJob); err != nil {
		t.Fatal(err)
	}
	reconciled := poll()
	if reconciled.Job != nil {
		t.Fatalf("expired attempt was blindly replayed: %+v", reconciled.Job)
	}
	var unknownStatus string
	if err = pool.QueryRow(ctx, `SELECT status FROM backup_jobs WHERE id=$1`, uncertainJob).Scan(&unknownStatus); err != nil {
		t.Fatal(err)
	}
	if unknownStatus != "unknown" {
		t.Fatalf("expired attempt status=%s", unknownStatus)
	}
}

func TestBackupProgressIsLeaseBoundSequencedAndIdempotent(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	device, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := createTestBackupJob(ctx, pool, device.agentID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := leaseBackupJob(ctx, pool, device.agentID)
	if err != nil || job == nil || job.ID != jobID {
		t.Fatalf("lease=%+v err=%v", job, err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, sessions).Register(mux)
	p := protocol.BackupProgress{ProtocolVersion: protocol.CurrentVersion, ID: "event-1", JobID: job.ID, AttemptToken: job.AttemptToken, AgentID: device.agentID, Sequence: 1, ObservedAt: time.Now().UTC(), Phase: "backup", FilesDone: 2, FilesTotal: 5, BytesDone: 1024, BytesTotal: 2048, PercentDone: 50}
	post := func() int {
		b, _ := json.Marshal(p)
		r := httptest.NewRequest(http.MethodPost, "/api/agent/backups/progress", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+device.credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := post(); code != http.StatusAccepted {
		t.Fatalf("first progress status=%d", code)
	}
	if code := post(); code != http.StatusAccepted {
		t.Fatalf("replayed progress status=%d", code)
	}
	p.AttemptToken = "00000000-0000-4000-8000-000000000001"
	p.Sequence = 2
	p.ID = "forged-event"
	if code := post(); code != http.StatusConflict {
		t.Fatalf("foreign attempt token status=%d", code)
	}
	p.AttemptToken = job.AttemptToken
	p.Sequence = 1
	p.ID = "event-1"
	var sequence int64
	var payload []byte
	if err = pool.QueryRow(ctx, `SELECT progress_sequence,progress_payload FROM backup_job_attempts WHERE job_id=$1`, jobID).Scan(&sequence, &payload); err != nil {
		t.Fatal(err)
	}
	var stored protocol.BackupProgress
	if err = json.Unmarshal(payload, &stored); err != nil {
		t.Fatalf("decode stored progress: %v", err)
	}
	if sequence != 1 || stored != p {
		t.Fatalf("stored progress sequence=%d got=%+v want=%+v", sequence, stored, p)
	}
	p.BytesDone = 2048
	if code := post(); code != http.StatusConflict {
		t.Fatalf("conflicting sequence status=%d", code)
	}
	p.Sequence = 2
	p.ID = "event-2"
	p.ObservedAt = time.Now().UTC()
	if _, err = pool.Exec(ctx, `UPDATE backup_job_attempts SET leased_at=now()-interval '2 seconds',lease_expires_at=now()-interval '1 second' WHERE job_id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if code := post(); code != http.StatusConflict {
		t.Fatalf("expired lease progress status=%d", code)
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM backup_jobs WHERE id=$1`, jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "leased" {
		t.Fatalf("expired heartbeat resurrected/reclassified job prematurely: %s", status)
	}
}

type testBackupAgent struct{ agentID, credential string }

func createTestBackupAgent(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (testBackupAgent, error) {
	t.Helper()
	var device string
	if err := pool.QueryRow(ctx, `INSERT INTO devices(display_name,last_seen_at) VALUES('backup-coordinator-test',now()) RETURNING id::text`).Scan(&device); err != nil {
		return testBackupAgent{}, err
	}
	credential := "backup-coordinator-credential"
	digest := hash(credential)
	var agentID string
	err := pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','["restic.backup"]','{restic.backup}') RETURNING id::text`, device, digest).Scan(&agentID)
	return testBackupAgent{agentID, credential}, err
}
func createTestBackupJob(ctx context.Context, pool *pgxpool.Pool, agentID string) (string, error) {
	var device string
	if err := pool.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, agentID).Scan(&device); err != nil {
		return "", err
	}
	var policy string
	err := pool.QueryRow(ctx, `INSERT INTO backup_policies(device_id,agent_id,profile_id,name,destination,included_paths,excluded_paths,retention,schedule,missed_run_policy,disconnected_device_policy,enabled,next_run_at) VALUES($1,$2,'home','test','/tmp/restic-test','["/data"]','[]','{}','{}','run_once','wait',true,now()) RETURNING id::text`, device, agentID).Scan(&policy)
	if err != nil {
		return "", err
	}
	var jobID string
	err = pool.QueryRow(ctx, `INSERT INTO backup_jobs(policy_id,agent_id,profile_id,policy_revision,policy_snapshot,scheduled_for,status) VALUES($1,$2,'home',1,'{"excluded_paths":[]}',now(),'pending') RETURNING id::text`, policy, agentID).Scan(&jobID)
	return jobID, err
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

func TestBackupProgressAcceptsPreparationCompatiblePhases(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	device, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createTestBackupJob(ctx, pool, device.agentID); err != nil {
		t.Fatal(err)
	}
	job, err := leaseBackupJob(ctx, pool, device.agentID)
	if err != nil || job == nil {
		t.Fatalf("lease=%+v err=%v", job, err)
	}
	mux := http.NewServeMux()
	NewHandler(pool, sessions).Register(mux)
	for index, phase := range []string{"starting", "validating", "backup"} {
		progress := protocol.BackupProgress{ProtocolVersion: protocol.CurrentVersion, ID: phase, JobID: job.ID, AttemptToken: job.AttemptToken, AgentID: device.agentID, Sequence: uint64(index + 1), ObservedAt: time.Now().UTC(), Phase: phase}
		body, err := json.Marshal(progress)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/agent/backups/progress", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+device.credential)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("phase %s status=%d body=%s", phase, response.Code, response.Body.String())
		}
		// Advance the stored receipt time instead of sleeping through rate limits.
		if _, err = pool.Exec(ctx, `UPDATE backup_job_attempts SET progress_at=now()-interval '6 seconds' WHERE job_id=$1`, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	var stored protocol.BackupProgress
	var payload []byte
	if err = pool.QueryRow(ctx, `SELECT progress_payload FROM backup_job_attempts WHERE job_id=$1`, job.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(payload, &stored); err != nil || stored.Phase != "backup" || stored.Sequence != 3 {
		t.Fatalf("stored progress=%+v err=%v", stored, err)
	}
}

func TestDockerTierCapabilitiesAreBoundedByEnrollmentScope(t *testing.T) {
	pool, sessions := agentTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sessions.CreateFirstUser(ctx, "tier-admin", "Tier Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, sessions)
	mux := http.NewServeMux()
	handler.Register(mux)

	create := httptest.NewRequest(http.MethodPost, "/api/agents/enrollment-tokens", bytes.NewBufferString(`{"capabilities":["docker.manage"],"expiresInHours":1}`))
	create.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, create)
	var token EnrollmentTokenResponse
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &token) != nil {
		t.Fatalf("create token returned %d: %s", created.Code, created.Body.String())
	}
	report := func(caps []string) protocol.CapabilityReport {
		return protocol.CapabilityReport{ProtocolVersion: protocol.CurrentVersion, Platform: "linux", Architecture: "amd64", AgentVersion: "test", Capabilities: caps}
	}
	// An agent cannot enroll claiming a tier above its enrollment scope.
	body, _ := json.Marshal(EnrollmentRequest{Token: token.Token, DisplayName: "tiered", Capability: report([]string{"docker.manage", "docker.exec"})})
	over := httptest.NewRecorder()
	mux.ServeHTTP(over, httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body)))
	if over.Code != http.StatusForbidden {
		t.Fatalf("enrollment above scope returned %d, want 403", over.Code)
	}
	body, _ = json.Marshal(EnrollmentRequest{Token: token.Token, DisplayName: "tiered", Capability: report([]string{"docker.manage"})})
	enrolled := httptest.NewRecorder()
	mux.ServeHTTP(enrolled, httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body)))
	var identity EnrollmentResponse
	if enrolled.Code != http.StatusOK || json.Unmarshal(enrolled.Body.Bytes(), &identity) != nil {
		t.Fatalf("enrollment within scope returned %d: %s", enrolled.Code, enrolled.Body.String())
	}

	poll := func(caps []string) int {
		raw, _ := json.Marshal(protocol.AgentRequest{ProtocolVersion: protocol.CurrentVersion, AgentID: identity.AgentID, AgentVersion: "test", Platform: "linux", Architecture: "amd64", Capabilities: caps})
		r := httptest.NewRequest(http.MethodPost, "/api/agent/poll", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+identity.Credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := poll([]string{"docker.manage", "docker.deploy"}); code != http.StatusForbidden {
		t.Fatalf("poll above scope returned %d, want 403", code)
	}
	if code := poll([]string{"docker.manage"}); code != http.StatusOK {
		t.Fatalf("poll within scope returned %d", code)
	}

	// The channel reads the stored capabilities, so the tier it grants is the scoped one.
	request := httptest.NewRequest(http.MethodGet, "/api/agent/channel", nil)
	request.Header.Set("Authorization", "Bearer "+identity.Credential)
	agentID, err := handler.AuthenticateAgentRequest(request)
	if err != nil || agentID != identity.AgentID {
		t.Fatalf("AuthenticateAgentRequest = %q, %v", agentID, err)
	}
	caps, err := handler.AgentCapabilities(ctx, agentID)
	if err != nil || protocol.DockerTier(caps) != protocol.DockerModeManage {
		t.Fatalf("AgentCapabilities = %v, %v", caps, err)
	}
	bad := httptest.NewRequest(http.MethodGet, "/api/agent/channel", nil)
	bad.Header.Set("Authorization", "Bearer not-a-real-credential")
	if _, err := handler.AuthenticateAgentRequest(bad); err == nil {
		t.Fatal("accepted an unknown credential")
	}
	if _, err := handler.AuthenticateAgentRequest(httptest.NewRequest(http.MethodGet, "/api/agent/channel", nil)); err == nil {
		t.Fatal("accepted a request without a credential")
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/agents/"+identity.AgentID+"/revoke", nil)
	revoke.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: admin.Token})
	mux.ServeHTTP(httptest.NewRecorder(), revoke)
	if _, err := handler.AuthenticateAgentRequest(request); err == nil {
		t.Fatal("a revoked agent can still open the channel")
	}
	if _, err := handler.AgentCapabilities(ctx, agentID); err == nil {
		t.Fatal("capabilities were returned for a revoked agent")
	}
}
