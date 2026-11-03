package stackgit

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestCommandFailureDiagnosticsDoNotExposeGitOutput(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"auth", "fatal: Authentication failed for 'https://user:secret@example.com/repo'", "authentication failed"},
		{"branch", "fatal: couldn't find remote ref refs/heads/private", "configured branch"},
		{"ssh", "git@example.com: Permission denied (publickey).", "authentication failed"},
		{"missing", "fatal: repository not found: https://user:secret@example.com/repo", "repository was not found"},
		{"dns", "fatal: Could not resolve host: internal.example.com", "could not be resolved"},
		{"tls", "fatal: SSL certificate problem: untrusted", "TLS certificate verification failed"},
		{"network", "fatal: Failed to connect to private.example.com", "could not connect"},
		{"disk", "fatal: No space left on device", "insufficient storage"},
		{"permissions", "fatal: /private/repo: Permission denied", "local Git repository files"},
		{"unknown", "fatal: unexpected secret-content", "did not report a recognized cause"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := commandFailure("fetch", tc.output, errors.New("exit status 128"))
			if !errors.Is(err, ErrGit) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Git fetch failed") {
				t.Fatalf("unexpected diagnostic: %v", err)
			}
			for _, secret := range []string{"secret", "example.com", "/private/repo", "refs/heads/private"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic exposed Git output: %v", err)
				}
			}
		})
	}
	err := commandFailure("fetch", "", exec.ErrNotFound)
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatal(err)
	}
}

func TestRepositoryReportsFailedCommand(t *testing.T) {
	r, _ := fixture(t)
	r.Branch = "missing-branch"
	_, err := r.List(context.Background())
	var diagnostic *CommandError
	if !errors.As(err, &diagnostic) || diagnostic.Operation != "fetch" || !strings.Contains(diagnostic.Reason, "configured branch") {
		t.Fatalf("missing branch diagnostic: %v", err)
	}
}

func TestRepositoryPreservesCancellation(t *testing.T) {
	r, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
