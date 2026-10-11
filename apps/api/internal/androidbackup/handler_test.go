package androidbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUploadStorageAndIdempotency(t *testing.T) {
	pool := testPool(t)
	root := t.TempDir()
	h := NewHandler(pool, nil, root, 1024, 1024)
	const agentID = "3fc8efb1-447a-4ef2-a084-0d277eb87109"
	const credential = "synthetic-android-agent-credential"
	seedAndroidAgent(t, pool, agentID, credential, true)
	const unscopedID = "68ad30bd-d11d-49a9-958c-8c976e9e7f9f"
	const unscopedCredential = "synthetic-android-agent-without-upload-scope"
	seedAndroidAgent(t, pool, unscopedID, unscopedCredential, false)
	mux := http.NewServeMux()
	h.Register(mux, http.NewServeMux())

	payload := []byte("synthetic selected file contents")
	backupID := "646a70cf-761c-4458-a335-387f2d7587d4"
	fileID := "57a393bb-0d9c-49b1-bb92-43e4a883eec4"
	unscoped := uploadRequest(mux, unscopedCredential, backupID, fileID, "notes.txt", payload)
	if unscoped.Code != http.StatusForbidden {
		t.Fatalf("unscoped agent status = %d, want 403", unscoped.Code)
	}
	for range cap(h.uploadSlots) {
		h.uploadSlots <- struct{}{}
	}
	busy := uploadRequest(mux, credential, backupID, fileID, "notes.txt", payload)
	for range cap(h.uploadSlots) {
		<-h.uploadSlots
	}
	if busy.Code != http.StatusTooManyRequests || busy.Header().Get("Retry-After") == "" {
		t.Fatalf("concurrent upload status = %d, Retry-After=%q", busy.Code, busy.Header().Get("Retry-After"))
	}
	badHash := sha256.Sum256([]byte("wrong hash"))
	badDigest := uploadRequestWithHash(mux, credential, backupID, fileID, "notes.txt", payload, hex.EncodeToString(badHash[:]))
	if badDigest.Code != http.StatusBadRequest {
		t.Fatalf("checksum mismatch status = %d, want 400", badDigest.Code)
	}
	var failedCount int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM android_file_backups WHERE agent_id=$1 AND status='uploading'`, agentID).Scan(&failedCount); err != nil || failedCount != 0 {
		t.Fatalf("failed upload left an active reservation: count=%d err=%v", failedCount, err)
	}
	first := uploadRequest(mux, credential, backupID, fileID, "notes.txt", payload)
	if first.Code != http.StatusCreated {
		t.Fatalf("first upload status = %d, body = %s", first.Code, first.Body.String())
	}
	retry := uploadRequest(mux, credential, backupID, fileID, "notes.txt", payload)
	if retry.Code != http.StatusNoContent {
		t.Fatalf("identical retry status = %d, want 204", retry.Code)
	}
	conflict := uploadRequest(mux, credential, backupID, fileID, "other.txt", []byte("different payload"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting duplicate status = %d, want 409", conflict.Code)
	}
	var objectID string
	if err := pool.QueryRow(context.Background(), `SELECT object_id::text FROM android_file_backups WHERE agent_id=$1 AND backup_id=$2 AND file_id=$3 AND status='committed'`, agentID, backupID, fileID).Scan(&objectID); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(root, "objects", agentID, objectID+".blob"))
	if err != nil || !bytes.Equal(stored, payload) {
		t.Fatalf("stored content mismatch: err=%v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "objects", agentID, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("client display name was used as a filesystem path")
	}
	operatorAuth := auth.NewService(pool, db.New(pool))
	session, err := operatorAuth.CreateFirstUser(context.Background(), "operator", "Test Operator", "synthetic-test-password", "synthetic-test-password")
	if err != nil {
		t.Fatal(err)
	}
	h.auth = operatorAuth
	operatorMux := http.NewServeMux()
	h.Register(http.NewServeMux(), operatorMux)
	unauthenticated := httptest.NewRecorder()
	operatorMux.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/backups/android/files/"+objectID, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download status = %d, want 401", unauthenticated.Code)
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/backups/android/files?agentId="+agentID, nil)
	listRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	listed := httptest.NewRecorder()
	operatorMux.ServeHTTP(listed, listRequest)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"displayName":"notes.txt"`) {
		t.Fatalf("authenticated recovery list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/backups/android/files/"+objectID, nil)
	downloadRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
	downloaded := httptest.NewRecorder()
	operatorMux.ServeHTTP(downloaded, downloadRequest)
	if downloaded.Code != http.StatusOK || !bytes.Equal(downloaded.Body.Bytes(), payload) || downloaded.Header().Get("X-Content-SHA256") == "" {
		t.Fatalf("authenticated recovery download status = %d, bytes=%d", downloaded.Code, downloaded.Body.Len())
	}

	quotaHandler := NewHandler(pool, nil, root, 1024, int64(len(payload)))
	quotaMux := http.NewServeMux()
	quotaHandler.Register(quotaMux, http.NewServeMux())
	quota := uploadRequest(quotaMux, credential, "853fbc12-91c0-44b7-aefa-1f68f99d2ee2", "7fa6c6bd-62c2-43e1-a7f2-fccca2ec017d", "next.bin", []byte("next"))
	if quota.Code != http.StatusInsufficientStorage {
		t.Fatalf("quota status = %d, want 507", quota.Code)
	}

	if _, err := pool.Exec(context.Background(), `UPDATE agent_identities SET revoked_at=now() WHERE id=$1`, agentID); err != nil {
		t.Fatal(err)
	}
	revoked := uploadRequest(mux, credential, "efb75aec-46d7-42db-96f2-083b385c75f8", "503ab36b-c4b5-40aa-8b2b-6dd824f60795", "next.bin", []byte("next"))
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked agent status = %d, want 401", revoked.Code)
	}
}

func TestUploadValidationAndDisabledStorage(t *testing.T) {
	if !validUUID("f9cd3470-9a80-4d7d-9f7c-4624d5039e1a") || validUUID("../../etc/passwd") {
		t.Fatal("UUID validation did not reject a path")
	}
	for _, name := range []string{"", "..", "../private", `folder\\file`, "bad\nname"} {
		if validDisplayName(name) {
			t.Fatalf("accepted unsafe display name %q", name)
		}
	}
	if !validDisplayName("selected photo.jpg") {
		t.Fatal("rejected normal display name")
	}
	// Storage-disabled behavior is checked before database access or filesystem creation.
	h := NewHandler(nil, nil, "", 1024, 2048)
	mux := http.NewServeMux()
	h.Register(mux, http.NewServeMux())
	response := uploadRequest(mux, "unused", "646a70cf-761c-4458-a335-387f2d7587d4", "57a393bb-0d9c-49b1-bb92-43e4a883eec4", "x.bin", []byte("x"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled storage status = %d, want 503", response.Code)
	}
}

func uploadRequest(mux *http.ServeMux, credential, backupID, fileID, name string, body []byte) *httptest.ResponseRecorder {
	hash := sha256.Sum256(body)
	return uploadRequestWithHash(mux, credential, backupID, fileID, name, body, hex.EncodeToString(hash[:]))
}

func uploadRequestWithHash(mux *http.ServeMux, credential, backupID, fileID, name string, body []byte, digest string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPut, "/api/agent/android/files", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("X-Vigara-Backup-ID", backupID)
	request.Header.Set("X-Vigara-File-ID", fileID)
	request.Header.Set("X-Vigara-File-Name", base64.RawURLEncoding.EncodeToString([]byte(name)))
	request.Header.Set("X-Vigara-Content-SHA256", digest)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func seedAndroidAgent(t *testing.T, pool *pgxpool.Pool, agentID, credential string, authorized bool) {
	t.Helper()
	var deviceID string
	if err := pool.QueryRow(context.Background(), `INSERT INTO devices(display_name) VALUES('synthetic Android test') RETURNING id::text`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(credential))
	capabilities, allowed := `[]`, []string{}
	if authorized {
		capabilities, allowed = `["android.files.upload"]`, []string{"android.files.upload"}
	}
	query := `INSERT INTO agent_identities(id,device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES($1,$2,$3,'v1','android','arm64-v8a','test',$4::jsonb,$5)`
	if _, err := pool.Exec(context.Background(), query, agentID, deviceID, sum[:], capabilities, allowed); err != nil {
		t.Fatal(err)
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("VIGIRA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("VIGIRA_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	bootstrap, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("android_backup_test_%d", time.Now().UnixNano())
	if _, err := bootstrap.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		bootstrap.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = bootstrap.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		bootstrap.Close()
	})
	for _, name := range []string{"000001_initial_schema.up.sql", "000002_local_auth.up.sql", "000003_agent_enrollment.up.sql", "000012_android_file_backups.up.sql"} {
		migration, readErr := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return pool
}
