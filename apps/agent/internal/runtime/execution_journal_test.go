package runtime

import (
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestRestartedBackupJournalBecomesUnknownAndIsNotReplayed(t *testing.T) {
	path := t.TempDir() + "/agent-state.json"
	started := time.Now().UTC().Add(-time.Minute)
	want := ExecutionJournal{Job: protocol.Job{ID: "job-1", AttemptToken: "attempt-1", ModuleID: "restic.backup", ModuleVersion: "1.0.0"}, ResultID: "result-1", StartedAt: started}
	if err := Save(path, State{AgentID: "agent-1", ActiveJob: &want}); err != nil {
		t.Fatal(err)
	}
	state, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = recoverInterruptedBackup(&state); err != nil {
		t.Fatal(err)
	}
	if err = Save(path, state); err != nil {
		t.Fatal(err)
	}
	recovered, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ActiveJob != nil || len(recovered.Pending) != 1 {
		t.Fatalf("journal recovery state: active=%+v pending=%+v", recovered.ActiveJob, recovered.Pending)
	}
	result := recovered.Pending[0]
	if result.ID != want.ResultID || result.JobID != want.Job.ID || result.AttemptToken != want.Job.AttemptToken || result.Error == nil || result.Error.Code != "unknown_outcome" || result.Error.Retryable {
		t.Fatalf("restart report did not preserve attempt identity as unknown: %+v", result)
	}
	if err = recoverInterruptedBackup(&recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Pending) != 1 {
		t.Fatalf("recovery duplicated the report: %d pending", len(recovered.Pending))
	}
}
