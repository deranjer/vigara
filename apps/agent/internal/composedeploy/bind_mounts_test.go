package composedeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

func TestBindMountCreationUsesComposeModelWithoutCreatingDirectories(t *testing.T) {
	for _, tc := range []struct {
		name, existing                        string
		create, absolute, imported, wantError bool
	}{
		{name: "relative automatic", create: true},
		{name: "absolute automatic", create: true, absolute: true},
		{name: "imported automatic", create: true, imported: true},
		{name: "relative strict", wantError: true},
		{name: "absolute strict", absolute: true, wantError: true},
		{name: "imported strict", imported: true, wantError: true},
		{name: "existing directory", existing: "directory"},
		{name: "existing file", existing: "file"},
		{name: "existing file with automatic creation", existing: "file", create: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRunner(t)
			s := testSnapshot()
			m, err := stack.Validate(s)
			if err != nil {
				t.Fatal(err)
			}
			target := r.Targets["demo"]
			target.Imported = tc.imported
			source := filepath.Join(target.Directory, "data")
			if tc.absolute {
				source = filepath.Join(t.TempDir(), "data")
			}
			switch tc.existing {
			case "directory":
				err = os.Mkdir(source, 0700)
			case "file":
				err = os.WriteFile(source, []byte("existing application data"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			r.runCommand = func(_ context.Context, dir string, args ...string) ([]byte, error) {
				resolved := source
				if !tc.absolute && !tc.imported {
					resolved = filepath.Join(dir, "data")
				}
				return json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{
					"image":   testImage,
					"volumes": []any{map[string]any{"type": "bind", "source": resolved, "target": "/data", "bind": map[string]any{"create_host_path": tc.create}}},
				}}})
			}
			_, err = r.model(context.Background(), s, target, m)
			if tc.wantError {
				var bindErr *bindMountError
				if !errors.As(err, &bindErr) || bindErr.Service != "web" || bindErr.Source != source || bindErr.Target != "/data" || !strings.Contains(err.Error(), "automatic directory creation") {
					t.Fatalf("missing actionable bind diagnostic: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.existing == "" {
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					t.Fatalf("preview created application storage: %v", err)
				}
			} else if tc.existing == "file" {
				data, err := os.ReadFile(source)
				if err != nil || string(data) != "existing application data" {
					t.Fatalf("preview changed an existing file: %v", err)
				}
			}
		})
	}
}

func TestBindMountFailureCrossesChannelWithoutRawErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"missing", os.ErrNotExist, "does not exist"},
		{"permission", os.ErrPermission, "cannot access"},
		{"other", errors.New("secret host diagnostic"), "could not inspect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := &os.PathError{Op: "stat", Path: "secret unrelated path", Err: tc.cause}
			err := newBindMountError("web", "/srv/app/data", "/data", cause)
			var channelErr *protocol.ChannelError
			if !errors.As(validationFailure(fmt.Errorf("secret wrapper: %w", err)), &channelErr) {
				t.Fatal("lost bind error on channel")
			}
			for _, want := range []string{strconv.Quote("web"), strconv.Quote("/srv/app/data"), strconv.Quote("/data"), tc.want} {
				if !strings.Contains(channelErr.Message, want) {
					t.Fatalf("missing %q in %s", want, channelErr.Message)
				}
			}
			if strings.Contains(channelErr.Message, "secret") {
				t.Fatal("raw error leaked into diagnostic")
			}
		})
	}
}

// Exercise actual Compose normalization without contacting a Docker daemon or
// pulling an image. The model must preserve short syntax and explicit opt-in.
func TestBindMountCreationWithInstalledCompose(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose is not installed")
	}
	for _, tc := range []struct {
		name, volume string
		wantError    bool
	}{
		{"short", "      - ./data:/data\n", false},
		{"explicit true", "      - type: bind\n        source: ./data\n        target: /data\n        bind:\n          create_host_path: true\n", false},
		{"explicit false", "      - type: bind\n        source: ./data\n        target: /data\n        bind:\n          create_host_path: false\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRunner(t)
			s := testSnapshot()
			s.Files["compose.yaml"] = "services:\n  web:\n    image: " + testImage + "\n    volumes:\n" + tc.volume
			m, err := stack.Validate(s)
			if err != nil {
				t.Fatal(err)
			}
			r.runCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
				// No registry or Docker daemon access is required for this test.
				filtered := []string{}
				for _, arg := range args {
					if arg != "--resolve-image-digests" {
						filtered = append(filtered, arg)
					}
				}
				cmd := exec.CommandContext(ctx, "docker", filtered...)
				cmd.Dir = dir
				return cmd.Output()
			}
			_, err = r.model(ctx, s, r.Targets["demo"], m)
			if tc.wantError {
				var bindErr *bindMountError
				if !errors.As(err, &bindErr) {
					t.Fatalf("expected missing bind diagnostic: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(r.Targets["demo"].Directory, "data")); !os.IsNotExist(err) {
				t.Fatalf("preview created storage: %v", err)
			}
		})
	}
}
