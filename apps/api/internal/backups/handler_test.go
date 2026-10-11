package backups

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateAndDSTSchedule(t *testing.T) {
	p := Policy{Name: "nightly", ProfileID: "archive_1", Destination: "/mnt/backup/restic", IncludedPaths: []string{"/home/alice"}, ExcludedPaths: []string{}, Schedule: Schedule{Weekdays: []string{"Sunday"}, Time: "02:30", Timezone: "America/New_York"}, MissedRunPolicy: "run_once", DisconnectedDevicePolicy: "wait"}
	if e := validate(p); e != nil {
		t.Fatal(e)
	}
	p.ProfileID = "Archive"
	if validate(p) == nil {
		t.Fatal("uppercase profile ID should not match agent profile contract")
	}
	p.ProfileID = "archive_1"
	p.ExcludedPaths = []string{"/home/alice/tmp"}
	if executionStatus(p) != "blocked_unsupported_exclusions" {
		t.Fatal("unsupported exclusions must block execution")
	}
	after := time.Date(2026, 3, 7, 8, 0, 0, 0, time.UTC)
	next, e := nextOccurrence(p.Schedule, after)
	if e != nil {
		t.Fatal(e)
	}
	loc, _ := time.LoadLocation(p.Schedule.Timezone)
	if got := next.In(loc); got.Day() != 15 || got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("nonexistent spring-forward slot not skipped: %s", got)
	}
	p.Schedule.Time = "01:30"
	after = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	first, e := nextOccurrence(p.Schedule, after)
	if e != nil {
		t.Fatal(e)
	}
	second, e := nextOccurrence(p.Schedule, first)
	if e != nil {
		t.Fatal(e)
	}
	if second.Sub(first) < 6*24*time.Hour {
		t.Fatalf("ambiguous fall-back wall time scheduled twice: %s then %s", first, second)
	}
}

func backupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set VIGIRA_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	if _, e = admin.Exec(ctx, `BEGIN; SELECT pg_advisory_xact_lock(138139136); CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public; COMMIT;`); e != nil {
		t.Fatal(e)
	}
	schema := pgx.Identifier{time.Now().Format("backup_test_150405.000000000")}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	for _, f := range []string{"000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql", "000009_backup_policies.up.sql"} {
		b, e := os.ReadFile("../../migrations/" + f)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(b)); e != nil {
			t.Fatalf("migration %s: %v", f, e)
		}
	}
	return pool
}

func TestAuthenticatedPolicyCRUDAndDurableScheduling(t *testing.T) {
	pool := backupTestDB(t)
	ctx := context.Background()
	service := auth.NewService(pool, db.New(pool))
	session, e := service.CreateFirstUser(ctx, "backupuser", "Backup User", "very-long-test-password-1", "very-long-test-password-1")
	if e != nil {
		t.Fatal(e)
	}
	var device, agentID string
	if e = pool.QueryRow(ctx, `INSERT INTO devices(display_name,last_seen_at) VALUES('backup-policy-test',now()) RETURNING id::text`).Scan(&device); e != nil {
		t.Fatal(e)
	}
	credential := sha256.Sum256([]byte("backup-test-credential"))
	if e = pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','["restic.backup"]','{restic.backup}') RETURNING id::text`, device, credential[:]).Scan(&agentID); e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	NewHandler(pool, service).Register(mux)
	p := Policy{DeviceID: device, AgentID: agentID, ProfileID: "home", Name: "Home backup", Destination: "/srv/backup", IncludedPaths: []string{"/home/test"}, Schedule: Schedule{Weekdays: []string{"Sunday"}, Time: "02:30", Timezone: "America/New_York"}, MissedRunPolicy: "run_once", DisconnectedDevicePolicy: "wait", Enabled: true}
	body, _ := json.Marshal(PolicyInput{DeviceID: p.DeviceID, AgentID: p.AgentID, ProfileID: p.ProfileID, Name: p.Name, Destination: p.Destination, IncludedPaths: p.IncludedPaths, Schedule: p.Schedule, MissedRunPolicy: p.MissedRunPolicy, DisconnectedDevicePolicy: p.DisconnectedDevicePolicy, Enabled: p.Enabled})
	req := httptest.NewRequest(http.MethodPost, "/api/backups/policies", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create status %d: %s", w.Code, w.Body.String())
	}
	var saved Policy
	if e = json.Unmarshal(w.Body.Bytes(), &saved); e != nil {
		t.Fatal(e)
	}
	if saved.ID == "" || saved.Revision != 1 || saved.ExecutionStatus != "awaiting_local_profile" {
		t.Fatalf("unexpected created policy: %+v", saved)
	}
	if _, e = pool.Exec(ctx, `UPDATE backup_policies SET next_run_at=$2 WHERE id=$1`, saved.ID, time.Now().UTC().Add(-2*time.Hour)); e != nil {
		t.Fatal(e)
	}
	n, e := ScheduleOnce(ctx, pool, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	if n != 1 {
		t.Fatalf("generated %d pending jobs", n)
	}
	var jobID string
	var rev int
	var snap []byte
	var status string
	if e = pool.QueryRow(ctx, `SELECT id::text,policy_revision,policy_snapshot,status FROM backup_jobs WHERE policy_id=$1`, saved.ID).Scan(&jobID, &rev, &snap, &status); e != nil {
		t.Fatal(e)
	}
	if jobID == "" || rev != 1 || status != "pending" {
		t.Fatalf("unexpected durable job %s revision=%d status=%s", jobID, rev, status)
	}
	var snapshot map[string]any
	if json.Unmarshal(snap, &snapshot) != nil || snapshot["profile_id"] != "home" {
		t.Fatalf("job snapshot lacks original policy: %s", snap)
	}
	input := PolicyInput{DeviceID: p.DeviceID, AgentID: p.AgentID, ProfileID: p.ProfileID, Name: "Edited backup", Destination: p.Destination, IncludedPaths: p.IncludedPaths, Schedule: p.Schedule, MissedRunPolicy: p.MissedRunPolicy, DisconnectedDevicePolicy: p.DisconnectedDevicePolicy, Enabled: p.Enabled}
	body, _ = json.Marshal(input)
	put := httptest.NewRequest(http.MethodPut, "/api/backups/policies/"+saved.ID, bytes.NewReader(body))
	put.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	putResult := httptest.NewRecorder()
	mux.ServeHTTP(putResult, put)
	if putResult.Code != 200 {
		t.Fatalf("update status %d: %s", putResult.Code, putResult.Body.String())
	}
	var updated Policy
	if e = json.Unmarshal(putResult.Body.Bytes(), &updated); e != nil {
		t.Fatal(e)
	}
	if updated.Revision != 2 || updated.Name != "Edited backup" {
		t.Fatalf("update result: %+v", updated)
	}
	if e = pool.QueryRow(ctx, `SELECT policy_revision FROM backup_jobs WHERE id=$1`, jobID).Scan(&rev); e != nil || rev != 1 {
		t.Fatalf("job revision changed after policy edit: %d %v", rev, e)
	}
	// A late skipped run and a disconnected wait retain their scheduled identity.
	if _, e = pool.Exec(ctx, `UPDATE devices SET last_seen_at=now()-interval '1 hour' WHERE id=$1`, device); e != nil {
		t.Fatal(e)
	}
	for _, q := range []struct{ profile, missed, disconnected string }{{"waitcase", "skip", "wait"}, {"skipcase", "run_once", "skip"}} {
		_, e = pool.Exec(ctx, `INSERT INTO backup_policies(device_id,agent_id,profile_id,name,destination,included_paths,excluded_paths,retention,schedule,missed_run_policy,disconnected_device_policy,enabled,next_run_at) SELECT device_id,agent_id,$2,name,destination,included_paths,excluded_paths,retention,schedule,$3,$4,true,now()-interval '2 hours' FROM backup_policies WHERE id=$1`, saved.ID, q.profile, q.missed, q.disconnected)
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e = ScheduleOnce(ctx, pool, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	var waitStatus, skipStatus string
	if e = pool.QueryRow(ctx, `SELECT status FROM backup_jobs WHERE policy_id=(SELECT id FROM backup_policies WHERE agent_id=$1 AND profile_id='waitcase')`, agentID).Scan(&waitStatus); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, `SELECT status FROM backup_jobs WHERE policy_id=(SELECT id FROM backup_policies WHERE agent_id=$1 AND profile_id='skipcase')`, agentID).Scan(&skipStatus); e != nil {
		t.Fatal(e)
	}
	if waitStatus != "waiting" || skipStatus != "skipped" {
		t.Fatalf("disconnected policies status wait=%s skip=%s", waitStatus, skipStatus)
	}
	if _, e = pool.Exec(ctx, `UPDATE devices SET last_seen_at=now() WHERE id=$1`, device); e != nil {
		t.Fatal(e)
	}
	_, e = pool.Exec(ctx, `INSERT INTO backup_policies(device_id,agent_id,profile_id,name,destination,included_paths,excluded_paths,retention,schedule,missed_run_policy,disconnected_device_policy,enabled,next_run_at) SELECT device_id,agent_id,'concurrent','concurrent',destination,included_paths,excluded_paths,retention,schedule,'run_once','wait',true,now()-interval '1 hour' FROM backup_policies WHERE id=$1`, saved.ID)
	if e != nil {
		t.Fatal(e)
	}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); _, e := ScheduleOnce(ctx, pool, time.Now().UTC()); errs <- e }()
	}
	group.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var concurrentCount int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM backup_jobs WHERE policy_id=(SELECT id FROM backup_policies WHERE agent_id=$1 AND profile_id='concurrent')`, agentID).Scan(&concurrentCount); e != nil {
		t.Fatal(e)
	}
	if concurrentCount != 1 {
		t.Fatalf("concurrent scheduler created %d duplicate jobs", concurrentCount)
	}
	badCredential := sha256.Sum256([]byte("unauthorized-backup-agent"))
	var badAgent string
	if e = pool.QueryRow(ctx, `INSERT INTO agent_identities(device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,'v1','linux','amd64','test','["module.cpu"]','{module.cpu}') RETURNING id::text`, device, badCredential[:]).Scan(&badAgent); e != nil {
		t.Fatal(e)
	}
	badInput := input
	badInput.AgentID = badAgent
	body, _ = json.Marshal(badInput)
	badReq := httptest.NewRequest(http.MethodPost, "/api/backups/policies", bytes.NewReader(body))
	badReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	badResult := httptest.NewRecorder()
	mux.ServeHTTP(badResult, badReq)
	if badResult.Code != 403 {
		t.Fatalf("unauthorized capability policy status %d", badResult.Code)
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/backups/policies/"+saved.ID, nil)
	deleteReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	deleteResult := httptest.NewRecorder()
	mux.ServeHTTP(deleteResult, deleteReq)
	if deleteResult.Code != 204 {
		t.Fatalf("retire status %d: %s", deleteResult.Code, deleteResult.Body.String())
	}
	var stillExists bool
	if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM backup_policies WHERE id=$1 AND retired_at IS NOT NULL)`, saved.ID).Scan(&stillExists); e != nil || !stillExists {
		t.Fatalf("retired policy history absent: %v %v", stillExists, e)
	}
	unauth := httptest.NewRecorder()
	mux.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/backups/policies", nil))
	if unauth.Code != 401 {
		t.Fatalf("unauthenticated policy access=%d", unauth.Code)
	}
}
