package composedeploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
)

func TestEnvironmentRecipientRequiresApprovedTargetAndReusesIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux deployment target")
	}
	commands := 0
	r := &Runner{ManagedRoot: t.TempDir(), runCommand: func(context.Context, string, ...string) ([]byte, error) { commands++; return nil, nil }}
	q := protocol.ComposeCreateTarget{ID: "plex", ProjectName: "plex"}
	if _, err := r.EnvironmentRecipient(q); err == nil {
		t.Fatal("unapproved target obtained encryption identity")
	}
	if _, err := os.Stat(r.identityPath()); !os.IsNotExist(err) {
		t.Fatal("unapproved target created a key")
	}
	if _, err := r.CreateTarget(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	before := commands
	key, err := r.EnvironmentRecipient(q)
	if err != nil || !strings.HasPrefix(key.Recipient, "age1") {
		t.Fatal(key, err)
	}
	encoded, _ := json.Marshal(key)
	if strings.Contains(string(encoded), "AGE-SECRET-KEY") || commands != before {
		t.Fatal("recipient setup exposed identity or invoked Docker")
	}
	if _, err := r.EnvironmentRecipient(protocol.ComposeCreateTarget{ID: "plex", ProjectName: "other"}); err == nil {
		t.Fatal("wrong project claimed a key")
	}
	restarted := &Runner{ManagedRoot: r.ManagedRoot}
	again, err := restarted.EnvironmentRecipient(q)
	if err != nil || again != key {
		t.Fatal("identity changed on restart", err)
	}
	public, err := composesecrets.EnsureAgeIdentity(filepath.Join(r.ManagedRoot, ".vigara-import", "identity.age"))
	if err != nil || public != key.Recipient {
		t.Fatal("import and new stacks use different agent identities", err)
	}
}
