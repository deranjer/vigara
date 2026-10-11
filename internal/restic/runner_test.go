package restic

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type fakeProcess struct {
	outputs      [][]byte
	errors       []error
	commands     [][]string
	environments [][]string
}

func (f *fakeProcess) run(_ context.Context, executable string, args, env []string) ([]byte, []byte, error) {
	f.commands = append(f.commands, append([]string{executable}, args...))
	f.environments = append(f.environments, append([]string(nil), env...))
	i := len(f.commands) - 1
	var output []byte
	if i < len(f.outputs) {
		output = f.outputs[i]
	}
	var err error
	if i < len(f.errors) {
		err = f.errors[i]
	}
	return output, []byte("private backend detail"), err
}

type exitError int

func (e exitError) Error() string { return "secret=should-not-leak" }
func (e exitError) ExitCode() int { return int(e) }

func testRunner(fake *fakeProcess) *Runner {
	runner := New(Config{Environment: map[string]string{"RESTIC_PASSWORD": "super-secret", "AWS_SECRET_ACCESS_KEY": "cloud-secret"}})
	runner.process = fake
	return runner
}

func TestBackupRecordsVersionAndSnapshotAndUsesArgumentArray(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{
		[]byte("restic 0.19.1 compiled with go1.24.0 on windows/amd64\n"),
		[]byte("{\"message_type\":\"status\",\"error_count\":0}\n{\"message_type\":\"summary\",\"snapshot_id\":\"abcdef1234\"}\n"),
	}}
	got := testRunner(fake).Backup(context.Background(), "s3:https://bucket/repo", []string{"C:\\Users\\name with spaces", "--odd-path"})
	if got.Status != Succeeded || got.ResticVersion != PinnedVersion || !reflect.DeepEqual(got.SnapshotIDs, []string{"abcdef1234"}) {
		t.Fatalf("unexpected result: %#v", got)
	}
	want := []string{"restic", "--repo", "s3:https://bucket/repo", "--json", "--quiet", "backup", "--", "C:\\Users\\name with spaces", "--odd-path"}
	if !reflect.DeepEqual(fake.commands[1], want) {
		t.Fatalf("args = %#v, want %#v", fake.commands[1], want)
	}
	if strings.Contains(strings.Join(fake.commands[1], " "), "super-secret") {
		t.Fatal("credential was placed in process arguments")
	}
	joinedEnv := strings.Join(fake.environments[1], "\n")
	if !strings.Contains(joinedEnv, "RESTIC_PASSWORD=super-secret") || !strings.Contains(joinedEnv, "AWS_SECRET_ACCESS_KEY=cloud-secret") {
		t.Fatal("credential environment was not forwarded")
	}
}

func TestBackupClassifiesPartialFailureAndInterruption(t *testing.T) {
	tests := []struct {
		name    string
		runErr  error
		context context.Context
		want    Status
		code    string
	}{
		{name: "partial exit", runErr: exitError(3), context: context.Background(), want: Partial, code: "partial_backup"},
		{name: "restic interrupted", runErr: exitError(130), context: context.Background(), want: Interrupted, code: "interrupted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1\n"), []byte("{\"message_type\":\"summary\",\"snapshot_id\":\"abc123\"}\n")}, errors: []error{nil, tt.runErr}}
			got := testRunner(fake).Backup(tt.context, "/private/repository", []string{"/data"})
			if got.Status != tt.want || got.ErrorCode != tt.code {
				t.Fatalf("result = %#v", got)
			}
			if tt.want == Partial && !reflect.DeepEqual(got.SnapshotIDs, []string{"abc123"}) {
				t.Fatalf("partial snapshot not retained: %#v", got)
			}
			if tt.want == Interrupted && !reflect.DeepEqual(got.SnapshotIDs, []string{"abc123"}) {
				t.Fatalf("interrupted snapshot not retained: %#v", got.SnapshotIDs)
			}
			if strings.Contains(strings.Join([]string{got.ErrorCode, string(got.Status)}, " "), "secret") {
				t.Fatal("unsafe error surfaced")
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1\n")}, errors: []error{context.Canceled}}
	got := testRunner(fake).Backup(canceled, "/repo", []string{"/data"})
	if got.Status != Canceled || got.ErrorCode != "canceled" {
		t.Fatalf("canceled result = %#v", got)
	}
}

func TestBackupRejectsUnpinnedBinaryAndUnstructuredOutput(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.18.1\n")}}
	got := testRunner(fake).Backup(context.Background(), "/repo", []string{"/data"})
	if got.Status != Failed || got.ResticVersion != "0.18.1" || len(fake.commands) != 1 {
		t.Fatalf("unexpected version handling: %#v (%d commands)", got, len(fake.commands))
	}

	fake = &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1\n"), []byte("plain text")}}
	got = testRunner(fake).Backup(context.Background(), "/repo", []string{"/data"})
	if got.Status != Failed || got.ErrorCode != "invalid_restic_output" {
		t.Fatalf("unexpected parse handling: %#v", got)
	}
}

func TestValidateRepositoryReadsConfigAndDoesNotLeakCommandError(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1\n"), []byte("{\"version\":2}")}}
	if err := testRunner(fake).ValidateRepository(context.Background(), "local:/repo"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.commands[1], []string{"restic", "--repo", "local:/repo", "cat", "config"}) {
		t.Fatalf("validation args = %#v", fake.commands[1])
	}

	fake = &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1\n")}, errors: []error{nil, exitError(12)}}
	err := testRunner(fake).ValidateRepository(context.Background(), "private://user:password@host/repo")
	if err == nil || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe validation error: %v", err)
	}
}

func TestParseBackupPartialFromJSONErrors(t *testing.T) {
	summary, partial, err := parseBackup([]byte("{\"message_type\":\"error\"}\n{\"message_type\":\"summary\",\"snapshot_id\":\"abc123\"}\n"))
	if err != nil || !partial || summary == nil || summary.SnapshotID != "abc123" {
		t.Fatalf("got (%#v, %v, %v)", summary, partial, err)
	}
	if _, _, err := parseBackup([]byte("not-json\n")); err == nil {
		t.Fatal("expected malformed JSON to fail")
	}
	if _, _, err := parseBackup([]byte("{\"message_type\":\"summary\",\"snapshot_id\":\"not-a-hex-id\"}\n")); err == nil {
		t.Fatal("expected malformed snapshot ID to fail")
	}
}

func TestEnvironmentStripsAmbientResticSettingsAndUsesOnlyConfiguredCredentials(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD_COMMAND", "do-not-run")
	t.Setenv("RESTIC_REPOSITORY_FILE", "ambient-repo-file")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-cloud-secret")
	got := environment(map[string]string{"RESTIC_PASSWORD": "configured", "RESTIC_PASSWORD_COMMAND": "also-do-not-run", "AWS_SECRET_ACCESS_KEY": "configured-cloud"})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "do-not-run") || strings.Contains(joined, "ambient-repo-file") || strings.Contains(joined, "ambient-cloud-secret") {
		t.Fatalf("unsafe ambient setting inherited: %s", joined)
	}
	if !strings.Contains(joined, "RESTIC_PASSWORD=configured") || !strings.Contains(joined, "AWS_SECRET_ACCESS_KEY=configured-cloud") {
		t.Fatalf("configured env missing: %s", joined)
	}
}

func TestRealSubprocessInvocation(t *testing.T) {
	runner := New(Config{Executable: os.Args[0], Environment: map[string]string{"RESTIC_TEST_HELPER": "1", "RESTIC_TEST_PROGRESS": "1", "RESTIC_PASSWORD": "secret"}})
	runner.process = testBinaryProcess{}
	var progress []Progress
	got := runner.BackupWithProgress(context.Background(), "/repo", []string{"/source"}, func(value Progress) error { progress = append(progress, value); return nil })
	if got.Status != Succeeded || !reflect.DeepEqual(got.SnapshotIDs, []string{"abc123"}) {
		t.Fatalf("subprocess result = %#v", got)
	}
	if len(progress) != 1 || progress[0].FilesDone != 2 || progress[0].FilesTotal != 5 || progress[0].BytesDone != 1024 || progress[0].BytesTotal != 2048 || progress[0].PercentDone != 50 {
		t.Fatalf("streamed progress = %#v", progress)
	}
}

type testBinaryProcess struct{}

func (testBinaryProcess) run(ctx context.Context, executable string, args, env []string) ([]byte, []byte, error) {
	commandArgs := []string{"-test.run=TestResticHelper", "--"}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, executable, commandArgs...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func TestResticHelper(t *testing.T) {
	if os.Getenv("RESTIC_TEST_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args, " ")
	if strings.Contains(args, "version") {
		_, _ = os.Stdout.WriteString("restic 0.19.1\n")
	} else {
		if os.Getenv("RESTIC_TEST_PROGRESS") == "1" {
			_, _ = os.Stdout.WriteString("{\"message_type\":\"status\",\"files_done\":2,\"total_files\":5,\"bytes_done\":1024,\"total_bytes\":2048,\"percent_done\":50}\n")
		}
		_, _ = os.Stdout.WriteString("{\"message_type\":\"summary\",\"snapshot_id\":\"abc123\"}\n")
	}
	os.Exit(0)
}

func TestOutputLimit(t *testing.T) {
	buffer := &boundedBuffer{}
	input := make([]byte, maxOutputBytes+1)
	written, err := buffer.Write(input)
	if err != nil || written != len(input) || !buffer.exceeded || buffer.Len() != maxOutputBytes {
		t.Fatalf("output was not safely drained and bounded: written=%d len=%d exceeded=%v err=%v", written, buffer.Len(), buffer.exceeded, err)
	}
}
