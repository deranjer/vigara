package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deranjer/vigara/internal/protocol"
)

const MaxQueuedReports = 500

type State struct {
	ServerURL     string                       `json:"serverUrl"`
	AgentID       string                       `json:"agentId"`
	Credential    string                       `json:"credential"`
	Configuration *protocol.AgentConfiguration `json:"configuration,omitempty"`
	Pending       []protocol.JobResult         `json:"pending,omitempty"`
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
	return state, nil
}

// Save writes through a private temporary file and renames it atomically so a
// reboot cannot leave half-written credentials or queued reports.
func Save(path string, state State) error {
	if len(state.Pending) > MaxQueuedReports {
		return fmt.Errorf("offline report queue is full (%d)", MaxQueuedReports)
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
