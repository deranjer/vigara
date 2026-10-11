package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const MaxQueuedReports = 500
const MaxQueuedTelemetry = 100
const MaxResticProfiles = 32

// ResticProfile is installed by a trusted local operator in the private agent
// state file. Job settings can select its ID but cannot supply these values.
type ResticProfile struct {
	Enabled     bool              `json:"enabled"`
	Repository  string            `json:"repository"`
	SourcePaths []string          `json:"sourcePaths"`
	Environment map[string]string `json:"environment,omitempty"`
	Executable  string            `json:"executable,omitempty"`
}

type State struct {
	ServerURL      string                       `json:"serverUrl"`
	AgentID        string                       `json:"agentId"`
	Credential     string                       `json:"credential"`
	Configuration  *protocol.AgentConfiguration `json:"configuration,omitempty"`
	ResticProfiles map[string]ResticProfile     `json:"resticProfiles,omitempty"`
	Pending        []protocol.JobResult         `json:"pending,omitempty"`
	Telemetry      []protocol.TelemetrySnapshot `json:"telemetryPending,omitempty"`
	ActiveJob      *ExecutionJournal            `json:"activeBackup,omitempty"`
}

// ExecutionJournal is fsynced through Save before the backup subprocess starts.
// A surviving journal after restart is always reported as unknown; it is never replayed.
type ExecutionJournal struct {
	Job              protocol.Job             `json:"job"`
	ResultID         string                   `json:"resultId"`
	StartedAt        time.Time                `json:"startedAt"`
	ProgressSequence uint64                   `json:"progressSequence,omitempty"`
	LastProgress     *protocol.BackupProgress `json:"lastProgress,omitempty"`
}

func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read agent state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode agent state: %w", err)
	}
	if len(state.Pending) > MaxQueuedReports {
		return State{}, fmt.Errorf("agent state contains more than %d queued reports", MaxQueuedReports)
	}
	if len(state.Telemetry) > MaxQueuedTelemetry {
		return State{}, fmt.Errorf("agent state contains more than %d queued telemetry snapshots", MaxQueuedTelemetry)
	}
	if len(state.ResticProfiles) > MaxResticProfiles {
		return State{}, fmt.Errorf("agent state contains more than %d restic profiles", MaxResticProfiles)
	}
	return state, nil
}

// Save writes through a private temporary file and renames it atomically so a
// reboot cannot leave half-written credentials or queued reports.
func Save(path string, state State) error {
	if len(state.Pending) > MaxQueuedReports {
		return fmt.Errorf("offline report queue is full (%d)", MaxQueuedReports)
	}
	if len(state.Telemetry) > MaxQueuedTelemetry {
		return fmt.Errorf("telemetry queue is full (%d)", MaxQueuedTelemetry)
	}
	if len(state.ResticProfiles) > MaxResticProfiles {
		return fmt.Errorf("agent state contains more than %d restic profiles", MaxResticProfiles)
	}
	directory := filepath.Dir(path)
	_, statErr := os.Stat(directory)
	directoryWasMissing := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !directoryWasMissing {
		return fmt.Errorf("inspect state directory: %w", statErr)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if directoryWasMissing {
		if err := securePrivatePath(directory, true); err != nil {
			return fmt.Errorf("secure state directory: %w", err)
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode agent state: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := securePrivatePath(tmp, false); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure temporary state file: %w", err)
	}
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write agent state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace agent state: %w", err)
	}
	return nil
}

func (s *State) Enqueue(report protocol.JobResult) error {
	if len(s.Pending) >= MaxQueuedReports {
		return fmt.Errorf("offline report queue is full (%d)", MaxQueuedReports)
	}
	s.Pending = append(s.Pending, report)
	return nil
}

func (s *State) EnqueueTelemetry(snapshot protocol.TelemetrySnapshot) error {
	if len(s.Telemetry) >= MaxQueuedTelemetry {
		return fmt.Errorf("telemetry queue is full (%d)", MaxQueuedTelemetry)
	}
	s.Telemetry = append(s.Telemetry, snapshot)
	return nil
}
