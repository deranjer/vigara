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

func TestBindMountCreationAcrossComposeEncodings(t *testing.T) {
	trueFlag, falseFlag := true, false
	for _, tc := range []struct {
		name, probe string
		flag        *bool
		wantError   bool
	}{
		{"legacy true", `{"services":{"create":{"volumes":[{"bind":{"create_host_path":true}}]},"require":{"volumes":[{"bind":{}}]}}}`, &trueFlag, false},
		{"legacy omitted false", `{"services":{"create":{"volumes":[{"bind":{"create_host_path":true}}]},"require":{"volumes":[{"bind":{}}]}}}`, nil, true},
		{"modern omitted true", `{"services":{"create":{"volumes":[{"bind":{}}]},"require":{"volumes":[{"bind":{"create_host_path":false}}]}}}`, nil, false},
		{"modern false", `{"services":{"create":{"volumes":[{"bind":{}}]},"require":{"volumes":[{"bind":{"create_host_path":false}}]}}}`, &falseFlag, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRunner(t)
			s := testSnapshot()
			m, err := stack.Validate(s)
			if err != nil {
				t.Fatal(err)
			}
			probes := 0
			r.runCommand = func(_ context.Context, dir string, args ...string) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "-p vigara-bind-encoding") {
					probes++
					return []byte(tc.probe), nil
				}
				bind := map[string]any{}
				if tc.flag != nil {
					bind["create_host_path"] = *tc.flag
				}
				return json.Marshal(map[string]any{"services": map[string]any{"cookira": map[string]any{
					"image": testImage,
					"volumes": []any{
						map[string]any{"type": "bind", "source": filepath.Join(dir, "images"), "target": "/data/images", "bind": bind},
						map[string]any{"type": "bind", "source": filepath.Join(dir, "files"), "target": "/data/files", "bind": bind},
					},
				}}})
			}
			for _, pinned := range []bool{false, true} {
				var images map[string]string
				if pinned {
					images = map[string]string{"cookira": testImage}
				}
				_, err := r.modelWithImages(context.Background(), s, r.Targets["demo"], m, images)
				if tc.wantError {
					var bindErr *bindMountError
					if !errors.As(err, &bindErr) || bindErr.Service != "cookira" || bindErr.Source != filepath.Join(r.Targets["demo"].Directory, "images") {
						t.Fatalf("expected actionable missing bind error, pinned=%t: %v", pinned, err)
					}
				} else if err != nil {
					t.Fatalf("pinned=%t: %v", pinned, err)
				}
			}
			wantProbes := 0
			if tc.flag == nil {
				wantProbes = 2
			}
			if probes != wantProbes {
				t.Fatalf("got %d probes, want %d (at most one per model)", probes, wantProbes)
			}
			for _, name := range []string{"images", "files"} {
				if _, err := os.Lstat(filepath.Join(r.Targets["demo"].Directory, name)); !os.IsNotExist(err) {
					t.Fatalf("validation created storage: %v", err)
				}
			}
		})
	}
}

func TestBindCreationProbeRejectsUnknownOutputAndCleansUp(t *testing.T) {
	for _, output := range []string{
		`not json`, `{}`, `{"services":{"create":{"volumes":[{"bind":{}}]},"require":{"volumes":[{"bind":{}}]}}}`,
		`{"services":{"create":{"volumes":[{"bind":{"create_host_path":false}}]},"require":{"volumes":[{"bind":{"create_host_path":true}}]}}}`,
	} {
		r := testRunner(t)
		dir := t.TempDir()
		r.runCommand = func(context.Context, string, ...string) ([]byte, error) { return []byte(output), nil }
		if _, err := r.omittedBindCreation(context.Background(), dir); err != errBindCreationEncoding {
			t.Fatalf("unrecognized output accepted: %v", err)
		}
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 0 {
			t.Fatalf("probe left files behind: %v, %v", files, err)
		}
	}
}

// Exercise actual Compose normalization without contacting a Docker daemon or
// pulling an image. The model must preserve short syntax and explicit opt-in.
func TestBindMountCreationWithInstalledCompose(t *testing.T) {
	program, prefix := "docker", []string{"compose"}
	// Allow the same integration cases to exercise pinned standalone releases
	// without replacing the developer's Docker CLI plugin.
	if binary := os.Getenv("VIGARA_TEST_COMPOSE_BINARY"); binary != "" {
		program, prefix = binary, nil
	}
	if _, err := exec.LookPath(program); err != nil {
		t.Skip("Docker CLI is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, program, append(prefix, "version")...).Run(); err != nil {
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
				cmd := exec.CommandContext(ctx, program, append(prefix, filtered[1:]...)...)
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
