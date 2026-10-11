package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/composeadoption"
)

func TestObserveComposeReadsAllLabeledContainersAndProjectsOnlySafeMetadata(t *testing.T) {
	working, err := filepath.Abs(filepath.Join("fixture", "project"))
	if err != nil {
		t.Fatal(err)
	}
	containers := make([]container, 60)
	for i := range containers {
		id := fmt.Sprintf("%064x", i+1)
		project := fmt.Sprintf("project-%02d", i)
		if i >= 18 {
			project = fmt.Sprintf("project-%02d", i-18)
		}
		containers[i] = container{ID: id, State: "running", Labels: map[string]string{"com.docker.compose.project": project}}
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Errorf("HTTP method = %s", r.Method)
			http.Error(w, "unsupported", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1.44/containers/json" {
			_ = json.NewEncoder(w).Encode(containers)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.44/containers/"), "/json")
		index, parseErr := strconv.ParseInt(id, 16, 64)
		if parseErr != nil {
			http.NotFound(w, r)
			return
		}
		project := fmt.Sprintf("project-%02d", index-1)
		if index-1 >= 18 {
			project = fmt.Sprintf("project-%02d", index-19)
		}
		payload := map[string]any{
			"Id":    id,
			"State": map[string]any{"Status": "running", "StartedAt": "2026-10-11T10:00:00Z"},
			"Config": map[string]any{"Env": []string{"PASSWORD=never-return-this"}, "Labels": map[string]string{
				"com.docker.compose.project": project, "com.docker.compose.service": "web",
				"com.docker.compose.project.working_dir": working, "com.docker.compose.project.config_files": "compose.yaml",
				"unrelated.private_token": "never-return-label",
			}},
			"Mounts": []map[string]any{{"Type": "volume", "Name": "data-vol", "Driver": "local", "Source": "data-vol", "Destination": "/data", "RW": true, "Propagation": "rprivate"}},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer s.Close()
	c, err := New(Config{Enabled: true, Host: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.ObserveCompose(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.Containers) != 60 {
		t.Fatalf("snapshot complete/count = %v/%d", snapshot.Complete, len(snapshot.Containers))
	}
	if calls != 61 {
		t.Fatalf("Docker GET count = %d", calls)
	}
	first := snapshot.Containers[0]
	if first.StorageBinds[0].Type != "volume" || first.StorageBinds[0].Name != "data-vol" || first.StorageBinds[0].Driver != "local" {
		t.Fatalf("mount identity = %#v", first.StorageBinds[0])
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "never-return-this") || strings.Contains(string(encoded), "never-return-label") {
		t.Fatal("Docker secrets or arbitrary labels leaked into projected runtime data")
	}
	after := composeadoption.RuntimeSnapshot{ObservedAt: snapshot.ObservedAt.Add(1), Complete: true, Containers: append([]composeadoption.RuntimeContainer(nil), snapshot.Containers...)}
	if err := composeadoption.VerifyUnchanged(snapshot, after); err != nil {
		t.Fatalf("unchanged complete inventory: %v", err)
	}
	after.Containers[0].State = "exited"
	if err := composeadoption.VerifyUnchanged(snapshot, after); err == nil {
		t.Fatal("state change was accepted")
	}
}
