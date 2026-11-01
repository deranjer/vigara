package composedeploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/placement"
)

func TestMountedFilesystemRequiresExactMountBoundary(t *testing.T) {
	data := []byte("23 1 8:1 / / rw - ext4 /dev/sda rw\n24 23 8:2 / /srv/data rw - ext4 /dev/sdb rw\n25 23 8:3 / /srv/with\\040space rw - ext4 /dev/sdc rw\n")
	for _, path := range []string{"/", "/srv/data", "/srv/with space"} {
		if !mountedExactly(data, path) {
			t.Fatal("mount missed", path)
		}
	}
	for _, path := range []string{"/srv", "/srv/data/ordinary-folder", "/srv/missing"} {
		if mountedExactly(data, path) {
			t.Fatal("directory mistaken for mount", path)
		}
	}
}

func TestExactPathRejectsSymlinkParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires host privileges")
	}
	root := t.TempDir()
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := exactPath(filepath.Join(root, "link", "missing")); !errors.Is(err, errRequirements) {
		t.Fatal("symlink parent accepted", err)
	}
}

func TestPreflightMatchesExistingNetworkAndVolumeAttributes(t *testing.T) {
	r := testRunner(t)
	p := placement.Profile{ID: "test", Requirements: map[string]any{
		"networks": []any{map[string]any{"name": "existing", "external": true, "driver": "bridge", "scope": "local", "internal": false, "attachable": true}},
		"volumes":  []any{map[string]any{"name": "storage", "external": true, "driver": "local"}},
	}}
	calls := 0
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if strings.Join(args[1:4], " ") != "inspect --format {{json .}}" {
			t.Fatal(args)
		}
		if args[0] == "network" {
			return []byte(`{"Name":"existing","Driver":"bridge","Scope":"local","Internal":false,"Attachable":true}`), nil
		}
		if args[0] == "volume" {
			return []byte(`{"Name":"storage","Driver":"local"}`), nil
		}
		t.Fatal(args)
		return nil, nil
	}
	if err := r.preflight(context.Background(), "", p); err != nil || calls != 2 {
		t.Fatal("matching profile rejected", err, calls)
	}
	p.Requirements["networks"].([]any)[0].(map[string]any)["driver"] = "overlay"
	if err := r.preflight(context.Background(), "", p); !errors.Is(err, errRequirements) {
		t.Fatal("driver mismatch accepted", err)
	}
	p.Requirements["networks"].([]any)[0].(map[string]any)["driver"] = "bridge"
	p.Requirements["networks"].([]any)[0].(map[string]any)["attachable"] = false
	if err := r.preflight(context.Background(), "", p); !errors.Is(err, errRequirements) {
		t.Fatal("boolean mismatch accepted", err)
	}
}

func TestPreflightFailsClosedForUnsupportedAndMissingRequirements(t *testing.T) {
	for _, requirements := range []map[string]any{
		{"capabilities": map[string]any{"hostNetwork": false}},
		{"startupDependencies": []any{map[string]any{"before": "a", "after": "b"}}},
		{"unknown": true},
		{"mounts": []any{map[string]any{"hostPath": "relative", "required": true}}},
		{"devices": []any{map[string]any{"hostPath": t.TempDir(), "required": true}}},
		{"networks": []any{map[string]any{"name": "new", "external": false, "managed": true}}},
	} {
		r := testRunner(t)
		r.runCommand = func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unsupported preflight issued Docker command")
			return nil, nil
		}
		if err := r.preflight(context.Background(), "", placement.Profile{ID: "test", Requirements: requirements}); !errors.Is(err, errRequirements) {
			t.Fatal("unsupported requirements accepted", requirements, err)
		}
	}
	r := testRunner(t)
	p := placement.Profile{ID: "test", Requirements: map[string]any{"mounts": []any{map[string]any{"hostPath": filepath.Join(t.TempDir(), "missing"), "required": true}}}}
	if err := r.preflight(context.Background(), "", p); !errors.Is(err, errRequirements) {
		t.Fatal("required missing mount accepted", err)
	}
	p.Requirements["mounts"].([]any)[0].(map[string]any)["required"] = false
	if err := r.preflight(context.Background(), "", p); err != nil {
		t.Fatal("optional missing mount rejected", err)
	}
	r.DockerHost = "tcp://remote:2376"
	if err := r.preflight(context.Background(), "", p); !errors.Is(err, errRequirements) {
		t.Fatal("remote engine filesystem check accepted", err)
	}
}
