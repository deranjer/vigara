package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestManagedBackupRequiresInstallerOptIn(t *testing.T) {
	c := &Client{}
	c.applyManagedBackup(context.Background(), &protocol.ManagedBackup{Revision: 1, Password: "secret"})
	if c.State.ManagedBackupStatus != nil || c.State.ManagedBackupProfile != nil {
		t.Fatal("server enabled managed backups without local authorization")
	}
	statePath := filepath.Join(t.TempDir(), "state", "agent.json")
	if err := ConfigureManagedBackups(statePath); err != nil {
		t.Fatal(err)
	}
	s, err := Load(statePath)
	if err != nil || !s.ManagedBackups {
		t.Fatal("installer opt-in was not persisted", err)
	}
	c.State = s
	found := false
	for _, capability := range c.Capabilities() {
		found = found || capability == "restic.backup"
	}
	if !found {
		t.Fatal("bootstrap cannot advertise backup capability")
	}
	c.applyManagedBackup(context.Background(), &protocol.ManagedBackup{Revision: 1, Repository: "relative", Password: "secret"})
	if c.State.ManagedBackupStatus.ErrorCode != "invalid_configuration" || c.State.ManagedBackupProfile != nil {
		t.Fatal("invalid configuration was applied")
	}
}

func TestManagedProfileDoesNotReplaceLocalProfiles(t *testing.T) {
	c := &Client{State: State{ManagedBackups: true, ResticProfiles: map[string]ResticProfile{"home": {Repository: "local"}}, ManagedBackupProfile: &ResticProfile{Repository: "managed"}}}
	profiles := c.backupProfiles()
	delete(profiles, "home")
	if len(c.State.ResticProfiles) != 1 || profiles["managed"].Repository != "managed" {
		t.Fatal("local profiles changed")
	}
}
