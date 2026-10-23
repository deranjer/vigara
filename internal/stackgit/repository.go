// Package stackgit stores stack revisions in an operator-configured Git branch.
// No checkout, hooks, filters, or repository-supplied commands are executed.
package stackgit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deranjer/vigara/internal/stack"
)

var ErrConflict = errors.New("the saved stack changed; reload it and review your draft before saving")
var ErrGit = errors.New("Git operation failed; check the configured repository, branch and credentials")
var ErrNotFound = errors.New("stack or revision not found")

type Repository struct {
	Directory, Remote, Branch string
	mu                        sync.Mutex
}
type Revision struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
	At      string `json:"at"`
}
type Saved struct {
	Commit string `json:"commit"`
	State  string `json:"state"`
}

type bounded struct {
	bytes.Buffer
	limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, ErrGit
	}
	return b.Buffer.Write(p)
}

func (r *Repository) run(ctx context.Context, input string, extra []string, args ...string) (string, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=always", "-c", "credential.interactive=false", "-c", "core.quotePath=false", "--git-dir=" + r.Directory}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	// Credentials come from the service account's Git/SSH configuration. Never from request parameters.
	env := []string{}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, extra...)
	cmd.Stdin = strings.NewReader(input)
	out := &bounded{limit: 4 << 20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return "", ErrGit
	}
	return out.String(), nil
}
func (r *Repository) init(ctx context.Context) error {
	if r.Directory == "" || r.Remote == "" || r.Branch == "" || !filepath.IsAbs(r.Directory) || strings.HasPrefix(r.Remote, "-") || strings.ContainsAny(r.Remote, "\r\n\x00") {
		return ErrGit
	}
	if _, e := r.run(ctx, "", nil, "check-ref-format", "refs/heads/"+r.Branch); e != nil {
		return e
	}
	if _, e := os.Stat(filepath.Join(r.Directory, "HEAD")); os.IsNotExist(e) {
		if e = os.MkdirAll(r.Directory, 0700); e != nil {
			return ErrGit
		}
		if _, e = r.run(ctx, "", nil, "init", "--bare", r.Directory); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) sync(ctx context.Context) (string, error) {
	if e := r.init(ctx); e != nil {
		return "", e
	}
	if _, e := r.run(ctx, "", nil, "fetch", "--no-tags", r.Remote, "+refs/heads/"+r.Branch+":refs/remotes/configured"); e != nil {
		return "", e
	}
	out, e := r.run(ctx, "", nil, "rev-parse", "refs/remotes/configured")
	return strings.TrimSpace(out), e
}
func (r *Repository) read(ctx context.Context, id, commit string) (stack.Snapshot, error) {
	s := stack.Snapshot{ID: id, Commit: commit, Files: map[string]string{}}
	if !stack.ValidID(id) || !stack.CommitPattern.MatchString(commit) {
		return s, ErrNotFound
	}
	listing, e := r.run(ctx, "", nil, "ls-tree", "-r", "-z", commit, "--", "stacks/"+id+"/")
	if e != nil {
		return s, e
	}
	total := 0
	for _, record := range strings.Split(listing, "\x00") {
		if record == "" {
			continue
		}
		info, p, ok := strings.Cut(record, "\t")
		fields := strings.Fields(info)
		if !ok || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
			return s, stack.ErrInvalid
		}
		p = strings.TrimPrefix(p, "stacks/"+id+"/")
		if !stack.ValidPath(p) {
			return s, stack.ErrInvalid
		}
		size, e := r.run(ctx, "", nil, "cat-file", "-s", fields[2])
		if e != nil {
			return s, e
		}
		var n int
		if _, e = fmt.Sscan(size, &n); e != nil || n > 128<<10 {
			return s, stack.ErrInvalid
		}
		total += n
		if total > stack.MaxBytes || len(s.Files) >= 64 {
			return s, stack.ErrInvalid
		}
		content, e := r.run(ctx, "", nil, "cat-file", "blob", fields[2])
		if e != nil {
			return s, e
		}
		s.Files[p] = content
	}
	if len(s.Files) == 0 {
		return s, ErrNotFound
	}
	return s, nil
}

func (r *Repository) List(ctx context.Context) ([]stack.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, e := r.sync(ctx)
	if e != nil {
		return nil, e
	}
	out, e := r.run(ctx, "", nil, "ls-tree", "--name-only", head+":stacks")
	if e != nil {
		return []stack.Snapshot{}, nil
	}
	result := []stack.Snapshot{}
	for _, id := range strings.Split(strings.TrimSpace(out), "\n") {
		if !stack.ValidID(id) {
			continue
		}
		s, e := r.read(ctx, id, head)
		if errors.Is(e, stack.ErrInvalid) {
			result = append(result, stack.Snapshot{ID: id, Commit: head, Files: map[string]string{}})
			continue
		}
		if e != nil {
			return nil, e
		}
		result = append(result, s)
	}
	return result, nil
}
func (r *Repository) Read(ctx context.Context, id, commit string) (stack.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, e := r.sync(ctx)
	if e != nil {
		return stack.Snapshot{}, e
	}
	if commit == "" {
		commit = head
	}
	if !stack.CommitPattern.MatchString(commit) {
		return stack.Snapshot{}, ErrNotFound
	}
	if _, e = r.run(ctx, "", nil, "merge-base", "--is-ancestor", commit, head); e != nil {
		return stack.Snapshot{}, ErrNotFound
	}
	return r.read(ctx, id, commit)
}
func (r *Repository) History(ctx context.Context, id string) ([]Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !stack.ValidID(id) {
		return nil, ErrNotFound
	}
	head, e := r.sync(ctx)
	if e != nil {
		return nil, e
	}
	out, e := r.run(ctx, "", nil, "log", "-30", "--format=%H%x00%aI%x00%s", head, "--", "stacks/"+id+"/")
	if e != nil {
		return nil, e
	}
	revisions := []Revision{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p := strings.SplitN(line, "\x00", 3)
		if len(p) == 3 {
			revisions = append(revisions, Revision{p[0], p[2], p[1]})
		}
	}
	return revisions, nil
}

// Save uses a private index and preserves unrelated files from the latest remote tree.
// A normal fast-forward push protects against remote writers racing this operation.
func (r *Repository) Save(ctx context.Context, s stack.Snapshot, actor string) (Saved, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := Saved{}
	if _, e := stack.Validate(s); e != nil {
		return result, e
	}
	head, e := r.sync(ctx)
	if e != nil {
		return result, e
	}
	current, e := r.read(ctx, s.ID, head)
	if e != nil && !errors.Is(e, ErrNotFound) {
		return result, e
	}
	// A lost push response is safe to retry: an identical remote stack is already saved.
	if stack.Equal(s.Files, current.Files) {
		return Saved{head, "saved"}, nil
	}
	if s.Commit != "" {
		if !stack.CommitPattern.MatchString(s.Commit) {
			return result, ErrConflict
		}
		if _, e = r.run(ctx, "", nil, "merge-base", "--is-ancestor", s.Commit, head); e != nil {
			return result, ErrConflict
		}
		base, e := r.read(ctx, s.ID, s.Commit)
		if e != nil || !stack.Equal(base.Files, current.Files) {
			return result, ErrConflict
		}
	} else if len(current.Files) > 0 {
		return result, ErrConflict
	}
	if stack.Equal(s.Files, current.Files) {
		return Saved{head, "saved"}, nil
	}
	dir, e := os.MkdirTemp("", "vigara-git-index-")
	if e != nil {
		return result, ErrGit
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index"), "GIT_AUTHOR_NAME=Vigara", "GIT_AUTHOR_EMAIL=vigara@localhost", "GIT_COMMITTER_NAME=Vigara", "GIT_COMMITTER_EMAIL=vigara@localhost"}
	if _, e = r.run(ctx, "", env, "read-tree", head); e != nil {
		return result, e
	}
	for p := range current.Files {
		if _, ok := s.Files[p]; !ok {
			if _, e = r.run(ctx, "", env, "update-index", "--force-remove", "--", "stacks/"+s.ID+"/"+p); e != nil {
				return result, e
			}
		}
	}
	keys := []string{}
	for p := range s.Files {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		hash, e := r.run(ctx, s.Files[p], env, "hash-object", "-w", "--stdin")
		if e != nil {
			return result, e
		}
		if _, e = r.run(ctx, "", env, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(hash), "stacks/"+s.ID+"/"+p); e != nil {
			return result, e
		}
	}
	tree, e := r.run(ctx, "", env, "write-tree")
	if e != nil {
		return result, e
	}
	actor = regexp.MustCompile(`[\r\n\x00-\x1f]`).ReplaceAllString(actor, "")
	if len(actor) > 128 {
		actor = actor[:128]
	}
	commit, e := r.run(ctx, "Update "+s.ID+" through Vigara\n\nChanged by: "+actor+"\n", env, "commit-tree", strings.TrimSpace(tree), "-p", head)
	if e != nil {
		return result, e
	}
	commit = strings.TrimSpace(commit)
	// Keep failed pushes reachable and retryable without exposing them as saved revisions.
	if _, e = r.run(ctx, "", nil, "update-ref", "refs/vigara/pending/"+commit, commit); e != nil {
		return result, e
	}
	result = Saved{commit, "pendingPush"}
	if _, e = r.run(ctx, "", nil, "push", r.Remote, commit+":refs/heads/"+r.Branch); e != nil {
		return result, nil
	}
	// Confirm reachability even if a second remote writer has already advanced the branch.
	confirmed, e := r.sync(ctx)
	if e != nil {
		return result, nil
	}
	if _, e = r.run(ctx, "", nil, "merge-base", "--is-ancestor", commit, confirmed); e != nil {
		return result, nil
	}
	_, _ = r.run(ctx, "", nil, "update-ref", "-d", "refs/vigara/pending/"+commit)
	return Saved{commit, "saved"}, nil
}

func DefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 90*time.Second)
}
