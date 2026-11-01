package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/backups"
	"github.com/deranjer/vigara/internal/restic"
)

func TestManagedBackupBrowserWithRealRepository(t *testing.T) {
	exe := os.Getenv("VIGIRA_RESTIC_EXECUTABLE")
	if exe == "" {
		t.Skip("set VIGIRA_RESTIC_EXECUTABLE for HTTP browser/download integration")
	}
	pool, sessions := agentTestService(t)
	ctx := context.Background()
	admin, err := sessions.CreateFirstUser(ctx, "browseradmin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := sessions.CreateUser(ctx, "browserviewer", "Viewer", "correct horse battery staple", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	viewerSession, err := sessions.Login(ctx, viewer.Username, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := createTestBackupAgent(ctx, t, pool)
	if err != nil {
		t.Fatal(err)
	}
	var device string
	if err = pool.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, agent.agentID).Scan(&device); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	contents := []byte("download through authenticated HTTP\n")
	if err = os.WriteFile(filepath.Join(source, "fixture.txt"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	password := "browser-test-secret"
	runner := restic.New(restic.Config{Executable: exe, Environment: map[string]string{"RESTIC_PASSWORD": password}})
	if err = runner.InitializeMissing(ctx, repo); err != nil {
		t.Fatal(err)
	}
	result := runner.Backup(ctx, repo, []string{source})
	if result.Status != restic.Succeeded || len(result.SnapshotIDs) != 1 {
		t.Fatal(result)
	}
	id := result.SnapshotIDs[0]
	tree, err := runner.BrowseSnapshot(ctx, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	var file string
	for _, entry := range tree {
		if entry.Name == "fixture.txt" {
			file = entry.Path
		}
	}
	if file == "" {
		t.Fatal("missing fixture")
	}
	manager, err := backups.NewManager(pool, base64.StdEncoding.EncodeToString(make([]byte, 32)), exe)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := manager.Prepare(backups.ManagedInput{Repository: repo, ServerRepository: repo, SourcePaths: []string{source}, Password: password, Schedule: backups.Schedule{Weekdays: []string{"Monday"}, Time: "02:00", Timezone: "UTC"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = manager.Install(ctx, tx, agent.agentID, device, sealed); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	manager.Register(mux, sessions)
	call := func(method, target, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	base := "/api/backups/devices/" + device
	w := call("GET", base+"/snapshots", viewerSession.Token)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(id)) {
		t.Fatalf("snapshots: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", base+"/snapshots/"+id+"/entries?path="+url.QueryEscape(path.Dir(file)), viewerSession.Token)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("fixture.txt")) {
		t.Fatalf("entries: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", base+"/snapshots/"+id+"/download?path="+url.QueryEscape(file), viewerSession.Token)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), contents) || w.Header().Get("Content-Disposition") != "attachment; filename=fixture.txt" {
		t.Fatalf("file: %d %s %s", w.Code, w.Header(), w.Body.String())
	}
	w = call("GET", base+"/snapshots/"+id+"/download?path="+url.QueryEscape(path.Dir(file)), admin.Token)
	if w.Code != 200 {
		t.Fatalf("ZIP: %d %s", w.Code, w.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range z.File {
		if path.Base(entry.Name) == "fixture.txt" {
			reader, _ := entry.Open()
			data, e := io.ReadAll(reader)
			reader.Close()
			if e != nil || !bytes.Equal(data, contents) {
				t.Fatal("ZIP data mismatch", e)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("ZIP fixture missing")
	}
	if call("GET", base+"/snapshots", "").Code != 401 {
		t.Fatal("anonymous browser access")
	}
	if call("GET", base+"/managed/recovery", viewerSession.Token).Code != 403 {
		t.Fatal("viewer can export secrets")
	}
	if call("PUT", base+"/managed", viewerSession.Token).Code != 403 {
		t.Fatal("viewer can edit backup sources")
	}
	if call("GET", base+"/snapshots/invalid/download?path=/", admin.Token).Code == 200 {
		t.Fatal("invalid snapshot returned successful download")
	}
}
