package stackgit

import (
	"errors"
	"io/fs"
	"os/exec"
	"strings"
)

// CommandError contains only fixed diagnostic text. Git output, arguments and
// remote URLs can contain credentials or file contents and must not be exposed.
type CommandError struct {
	Operation string
	Reason    string
}

func (e *CommandError) Error() string { return "Git " + e.Operation + " failed: " + e.Reason }
func (e *CommandError) Unwrap() error { return ErrGit }

func commandFailure(operation, stderr string, cause error) error {
	text := strings.ToLower(stderr)
	reason := "Git did not report a recognized cause. Check the repository configuration and server Git access."
	contains := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(text, part) {
				return true
			}
		}
		return false
	}
	switch {
	case errors.Is(cause, exec.ErrNotFound):
		reason = "Git is not installed or is unavailable to the API service."
	case contains("couldn't find remote ref", "could not find remote branch", "remote branch", "invalid refspec"):
		reason = "The configured branch or reference is unavailable. Verify the branch exists in the remote repository."
	case contains("authentication failed", "permission denied (publickey", "could not read username", "terminal prompts disabled", "http 401", "error: 401"):
		reason = "Repository authentication failed. Check the service account credentials or GitHub App repository access."
	case contains("repository not found", "does not appear to be a git repository", "error: 404"):
		reason = "The repository was not found or is inaccessible to the service account. Check its URL and access permissions."
	case contains("error: 403", "access denied", "write access to repository not granted"):
		reason = "The Git server denied access. Check repository permissions and branch protection rules."
	case contains("host key verification failed"):
		reason = "SSH host verification failed. Configure the Git host's trusted key for the API service account."
	case contains("could not resolve host", "could not resolve hostname", "could not resolve proxy"):
		reason = "The Git host or proxy could not be resolved. Check server DNS and the repository URL."
	case contains("ssl certificate problem", "certificate verification failed", "server certificate verification failed"):
		reason = "TLS certificate verification failed. Check the Git server certificate and the API service's trusted certificates."
	case contains("failed to connect", "connection refused", "connection timed out", "network is unreachable", "connection reset"):
		reason = "The API service could not connect to the Git server. Check network access and Git server availability."
	case contains("no space left on device", "disk quota exceeded"):
		reason = "The API service has insufficient storage for the local Git repository."
	case errors.Is(cause, fs.ErrPermission), contains("permission denied", "read-only file system"):
		reason = "The API service cannot access the local Git repository files. Check directory ownership and write permissions."
	case contains("cannot lock ref", "unable to create", "index.lock"):
		reason = "Git could not create or lock repository files. Check directory permissions and concurrent Git processes."
	case contains("not a git repository", "bad object", "corrupt"):
		reason = "The local Git repository is invalid or damaged. Check the configured Git storage directory."
	case contains("[rejected]", "[remote rejected]", "pre-receive hook declined"):
		reason = "The remote rejected the push. Check branch protection rules and concurrent repository changes."
	case operation == "check-ref-format":
		reason = "The configured branch name is invalid. Correct it in Git settings."
	}
	return &CommandError{Operation: operation, Reason: reason}
}
