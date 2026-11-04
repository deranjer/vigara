package stackgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/stack"
)

type Placements struct {
	Commit string            `json:"commit"`
	Files  map[string]string `json:"files"`
}

func (r *Repository) readPlacements(ctx context.Context, commit string) (Placements, error) {
	p := Placements{Commit: commit, Files: map[string]string{}}
	listing, err := r.run(ctx, "", nil, "ls-tree", "-r", "-z", commit, "--", "profiles/", "assignments/")
	if err != nil {
		return p, err
	}
	total := 0
	for _, record := range strings.Split(listing, "\x00") {
		if record == "" {
			continue
		}
		info, path, ok := strings.Cut(record, "\t")
		// Documentation and other files remain untouched; YAML in the manifest
		// namespaces must use a canonical, portable manifest path.
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			continue
		}
		fields := strings.Fields(info)
		if !ok || !placement.ValidPath(path) || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || len(p.Files) >= placement.MaxFiles {
			return p, placement.ErrInvalid
		}
		content, e := r.run(ctx, "", nil, "cat-file", "blob", fields[2])
		if e != nil {
			return p, e
		}
		total += len(path) + len(content)
		if len(content) > placement.MaxFileBytes || total > placement.MaxBytes {
			return p, placement.ErrInvalid
		}
		p.Files[path] = content
	}
	return p, nil
}

// Placements reads only confirmed branch history, without checking out files or
// executing repository code. An empty commit selects the current remote head.
func (r *Repository) Placements(ctx context.Context, commit string) (Placements, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, err := r.sync(ctx)
	if err != nil {
		return Placements{}, err
	}
	if commit == "" {
		commit = head
	}
	if !stack.CommitPattern.MatchString(commit) {
		return Placements{}, ErrNotFound
	}
	if _, err = r.run(ctx, "", nil, "merge-base", "--is-ancestor", commit, head); err != nil {
		return Placements{}, ErrNotFound
	}
	return r.readPlacements(ctx, commit)
}

func (r *Repository) validatePlacements(ctx context.Context, p Placements, head string) error {
	c, err := placement.Parse(p.Files)
	if err != nil {
		return err
	}
	stacks := map[string]stack.Snapshot{}
	for _, a := range c.Assignments {
		if _, ok := stacks[a.StackID]; ok {
			continue
		}
		s, e := r.read(ctx, a.StackID, head)
		if errors.Is(e, ErrNotFound) || errors.Is(e, stack.ErrInvalid) {
			return placement.ErrInvalid
		}
		if e != nil {
			return e
		}
		stacks[a.StackID] = s
	}
	_, err = placement.Validate(p.Files, stacks)
	return err
}

// CheckPlacements validates references against the captured, confirmed commit.
func (r *Repository) CheckPlacements(ctx context.Context, p Placements) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, err := r.sync(ctx)
	if err != nil {
		return err
	}
	if !stack.CommitPattern.MatchString(p.Commit) {
		return ErrNotFound
	}
	if _, err = r.run(ctx, "", nil, "merge-base", "--is-ancestor", p.Commit, head); err != nil {
		return ErrNotFound
	}
	return r.validatePlacements(ctx, p, p.Commit)
}

// SavePlacements writes the complete manifest set atomically and preserves all
// unrelated repository content. A stale manifest set requires explicit review.
// It never changes runtime agent bindings or grants deployment authority.
func (r *Repository) SavePlacements(ctx context.Context, p Placements, actor string) (Saved, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := placement.Parse(p.Files); err != nil {
		return Saved{}, err
	}
	head, err := r.sync(ctx)
	if err != nil {
		return Saved{}, err
	}
	current, err := r.readPlacements(ctx, head)
	if err != nil {
		return Saved{}, err
	}
	if err = r.validatePlacements(ctx, p, head); err != nil {
		return Saved{}, err
	}
	if stack.Equal(p.Files, current.Files) {
		return Saved{head, "saved"}, nil
	}
	if !stack.CommitPattern.MatchString(p.Commit) {
		return Saved{}, ErrConflict
	}
	if _, err = r.run(ctx, "", nil, "merge-base", "--is-ancestor", p.Commit, head); err != nil {
		return Saved{}, ErrConflict
	}
	base, err := r.readPlacements(ctx, p.Commit)
	if err != nil || !stack.Equal(base.Files, current.Files) {
		return Saved{}, ErrConflict
	}
	dir, err := os.MkdirTemp("", "vigara-placement-index-")
	if err != nil {
		return Saved{}, ErrGit
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index"), "GIT_AUTHOR_NAME=Vigara", "GIT_AUTHOR_EMAIL=vigara@localhost", "GIT_COMMITTER_NAME=Vigara", "GIT_COMMITTER_EMAIL=vigara@localhost"}
	if _, err = r.run(ctx, "", env, "read-tree", head); err != nil {
		return Saved{}, err
	}
	for path := range current.Files {
		if _, ok := p.Files[path]; !ok {
			if _, err = r.run(ctx, "0 "+strings.Repeat("0", len(head))+"\t"+path+"\n", env, "update-index", "--index-info"); err != nil {
				return Saved{}, err
			}
		}
	}
	paths := make([]string, 0, len(p.Files))
	for path := range p.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		hash, e := r.run(ctx, p.Files[path], env, "hash-object", "-w", "--stdin")
		if e != nil {
			return Saved{}, e
		}
		if _, err = r.run(ctx, "", env, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(hash), path); err != nil {
			return Saved{}, err
		}
	}
	tree, err := r.run(ctx, "", env, "write-tree")
	if err != nil {
		return Saved{}, err
	}
	actor = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, actor)
	if len(actor) > 128 {
		actor = actor[:128]
	}
	commit, err := r.run(ctx, "Update profiles and assignments through Vigara\n\nChanged by: "+actor+"\n", env, "commit-tree", strings.TrimSpace(tree), "-p", head)
	if err != nil {
		return Saved{}, err
	}
	commit = strings.TrimSpace(commit)
	if _, err = r.run(ctx, "", nil, "update-ref", "refs/vigara/pending/"+commit, commit); err != nil {
		return Saved{}, err
	}
	pending := Saved{commit, "pendingPush"}
	if _, err = r.run(ctx, "", nil, "push", r.Remote, commit+":refs/heads/"+r.Branch); err != nil {
		return pending, nil
	}
	confirmed, err := r.sync(ctx)
	if err != nil {
		return pending, nil
	}
	if _, err = r.run(ctx, "", nil, "merge-base", "--is-ancestor", commit, confirmed); err != nil {
		return pending, nil
	}
	_, _ = r.run(ctx, "", nil, "update-ref", "-d", "refs/vigara/pending/"+commit)
	return Saved{commit, "saved"}, nil
}
