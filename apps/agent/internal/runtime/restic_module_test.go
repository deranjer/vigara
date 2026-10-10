package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
)

func TestResticJobUsesLocalProfileAndPersistsSafeSnapshotReport(t *testing.T) {
	executable := buildResticTestHelper(t)
	secret := "restic-test-password-never-report"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/poll" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.AgentResponse{
				ProtocolVersion: protocol.CurrentVersion, PollAfterSeconds: 30,
				Configuration: &protocol.AgentConfiguration{Version: "cfg1", Modules: []protocol.ModuleConfiguration{{ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Enabled: true}}},
				Job:           &protocol.Job{ID: "job-1", ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Kind: "backup", TimeoutSeconds: 30, Deadline: time.Now().Add(time.Minute), Settings: map[string]any{"profileId": "home"}, Permissions: []protocol.Permission{{Name: resticCapability, Scope: "profile:home"}}},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	client.State = State{
		ServerURL: server.URL, AgentID: "agent", Credential: "credential",
		ResticProfiles: map[string]ResticProfile{"home": {Enabled: true, Repository: filepath.Join(t.TempDir(), "repository"), SourcePaths: []string{filepath.Join(t.TempDir(), "source")}, Executable: executable, Environment: map[string]string{"RESTIC_PASSWORD": secret}}},
	}
	client.HTTP.Transport = server.Client().Transport
	if _, err := client.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.State.Pending) != 1 {
		t.Fatalf("pending report count = %d, want 1", len(client.State.Pending))
	}
	got := client.State.Pending[0]
	if got.Status != protocol.JobSucceeded || got.JobID != "job-1" {
		t.Fatalf("result = %+v", got)
	}
	if got.Output["resticVersion"] != "0.19.1" || got.Output["resticStatus"] != restic.Succeeded {
		t.Fatalf("restic outcome missing from report: %#v", got.Output)
	}
	ids, ok := got.Output["snapshotIds"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "abc123" {
		t.Fatalf("snapshot IDs = %#v", got.Output["snapshotIds"])
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), executable) {
		t.Fatal("report contains local credential or executable path")
	}
	reloaded, err := Load(path)
	if err != nil || len(reloaded.Pending) != 1 || reloaded.Pending[0].ID != got.ID {
		t.Fatalf("report was not persisted: state=%+v err=%v", reloaded, err)
	}
}

func buildResticTestHelper(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "restic-helper")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	source := filepath.Join("testdata", "restic-helper", "main.go")
	command := exec.Command("go", "build", "-o", output, source)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Restic helper: %v: %s", err, data)
	}
	return output
}

func TestResticExecutionRequiresExactProfileGrantAndEnabledConfiguration(t *testing.T) {
	client := &Client{State: State{AgentID: "agent", ResticProfiles: map[string]ResticProfile{"home": {Enabled: true, Repository: "/repo", SourcePaths: []string{"/source"}}}}}
	job := protocol.Job{ID: "job", ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Kind: "backup", TimeoutSeconds: 10, Settings: map[string]any{"profileId": "home"}, Permissions: []protocol.Permission{{Name: resticCapability, Scope: "profile:other"}}}
	client.State.Configuration = &protocol.AgentConfiguration{Modules: []protocol.ModuleConfiguration{{ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Enabled: true}}}
	result := client.executeJob(context.Background(), job, "result")
	if result.Status != protocol.JobFailed || result.Error == nil || result.Error.Code != "permission_denied" {
		t.Fatalf("wrong-scope grant result = %+v", result)
	}
	job.Permissions[0].Scope = "profile:home"
	client.State.Configuration.Modules[0].Enabled = false
	result = client.executeJob(context.Background(), job, "result")
	if result.Status != protocol.JobFailed || result.Error == nil || result.Error.Code != "module_disabled" {
		t.Fatalf("disabled-module result = %+v", result)
	}
}

func TestJobTimeoutIsBoundedBeforeDurationConversion(t *testing.T) {
	client := &Client{State: State{AgentID: "agent", Configuration: &protocol.AgentConfiguration{Modules: []protocol.ModuleConfiguration{{ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Enabled: true}}}}}
	job := protocol.Job{ID: "job", ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Kind: "backup", TimeoutSeconds: int(^uint(0) >> 1)}
	result := client.executeJob(context.Background(), job, "result")
	if result.Status != protocol.JobFailed || result.Error == nil || result.Error.Code != "invalid_timeout" {
		t.Fatalf("oversized timeout result = %+v", result)
	}
}

func TestResticDispatchMapsPartialInterruptedCanceledAndVersionMismatch(t *testing.T) {
	executable := buildResticTestHelper(t)
	for _, test := range []struct {
		name       string
		mode       string
		cancel     bool
		wantStatus protocol.JobStatus
		wantRunner restic.Status
		wantCode   string
	}{
		{name: "partial", mode: "partial", wantStatus: protocol.JobFailed, wantRunner: restic.Partial, wantCode: "partial_backup"},
		{name: "interrupted", mode: "interrupted", wantStatus: protocol.JobFailed, wantRunner: restic.Interrupted, wantCode: "interrupted"},
		{name: "canceled", cancel: true, wantStatus: protocol.JobCancelled, wantRunner: restic.Canceled, wantCode: "canceled"},
		{name: "version mismatch", mode: "wrong-version", wantStatus: protocol.JobFailed, wantRunner: restic.Failed, wantCode: "repository_validation_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{State: State{AgentID: "agent", Configuration: &protocol.AgentConfiguration{Modules: []protocol.ModuleConfiguration{{ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Enabled: true}}}, ResticProfiles: map[string]ResticProfile{"home": {Enabled: true, Repository: "/repo", SourcePaths: []string{"/source"}, Executable: executable, Environment: map[string]string{"RESTIC_HELPER_MODE": test.mode}}}}}
			job := protocol.Job{ID: "job", ModuleID: resticModuleID, ModuleVersion: resticModuleVersion, Kind: "backup", TimeoutSeconds: 30, Settings: map[string]any{"profileId": "home"}, Permissions: []protocol.Permission{{Name: resticCapability, Scope: "profile:home"}}}
			ctx := context.Background()
			if test.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			result := client.executeJob(ctx, job, "result")
			if result.Status != test.wantStatus || result.Error == nil || result.Error.Code != test.wantCode {
				t.Fatalf("job result = %+v", result)
			}
			if result.Output["resticStatus"] != test.wantRunner {
				t.Fatalf("runner status = %#v, want %q", result.Output["resticStatus"], test.wantRunner)
			}
		})
	}
}

func TestResticProfileRejectsRemoteRepositorySchemes(t *testing.T) {
	for _, repository := range []string{"local:/repo", "C:\\backup\\repo", "\\\\server\\share\\repo", "/var/backups/repo"} {
		if !validLocalRepository(repository) {
			t.Errorf("rejected supported local/network path %q", repository)
		}
	}
	for _, repository := range []string{"s3:bucket/repo", "sftp:user@host:/repo", "rest:https://host/repo", "gs:bucket/repo"} {
		if validLocalRepository(repository) {
			t.Errorf("accepted remote repository scheme %q", repository)
		}
	}
}
