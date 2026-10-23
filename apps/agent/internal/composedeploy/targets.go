package composedeploy

import (
	"context"
	"encoding/json"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// New targets are confined to a local operator's managed root. An existing project
// or nonempty folder cannot be adopted through this API.
func (r *Runner) managedRoot() (string, error) {
	if runtime.GOOS == "windows" || !filepath.IsAbs(r.ManagedRoot) {
		return "", errTarget
	}
	clean := filepath.Clean(r.ManagedRoot)
	// Check every existing ancestor before creating anything.
	for p := clean; ; p = filepath.Dir(p) {
		if info, e := os.Lstat(p); e == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errTarget
			}
		} else if !os.IsNotExist(e) {
			return "", errTarget
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return clean, nil
}
func (r *Runner) lookup(id string) (Target, bool) {
	if t, ok := r.Targets[id]; ok {
		return t, true
	}
	if !stack.ValidID(id) {
		return Target{}, false
	}
	root, e := r.managedRoot()
	if e != nil {
		return Target{}, false
	}
	dir := filepath.Join(root, id)
	if actual, e := filepath.EvalSymlinks(dir); e != nil || actual != dir {
		return Target{}, false
	}
	marker := filepath.Join(dir, ".vigara-target.json")
	info, e := os.Lstat(marker)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return Target{}, false
	}
	b, e := os.ReadFile(marker)
	var t Target
	if e != nil || json.Unmarshal(b, &t) != nil || t.Directory != dir || !stack.ValidProject(t.ProjectName) {
		return Target{}, false
	}
	return t, true
}
func (r *Runner) TargetInfo() protocol.ComposeTargets {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := protocol.ComposeTargets{Targets: []protocol.ComposeTargetInfo{}}
	if root, e := r.managedRoot(); e == nil {
		out.ManagedRoot = root
	}
	for id, t := range r.Targets {
		out.Targets = append(out.Targets, protocol.ComposeTargetInfo{ID: id, ProjectName: t.ProjectName, Directory: t.Directory})
	}
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].ID < out.Targets[j].ID })
	return out
}
func (r *Runner) CreateTarget(ctx context.Context, q protocol.ComposeCreateTarget) (protocol.ComposeTargetInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := protocol.ComposeTargetInfo{ID: q.ID, ProjectName: q.ProjectName}
	if !stack.ValidID(q.ID) || !stack.ValidProject(q.ProjectName) {
		return out, stack.ErrInvalid
	}
	if _, ok := r.Targets[q.ID]; ok {
		return out, errTarget
	}
	root, e := r.managedRoot()
	if e != nil {
		return out, e
	}
	out.Directory = filepath.Join(root, q.ID)
	if existing, ok := r.lookup(q.ID); ok {
		if existing.ProjectName == q.ProjectName {
			return out, nil
		}
		return out, errTarget
	}
	b, e := r.command(ctx, "", "ps", "-a", "--filter", "label=com.docker.compose.project="+q.ProjectName, "--format", "{{.ID}}")
	if e != nil || len(bytesTrim(b)) != 0 {
		return out, errTarget
	}
	// A second managed stack cannot claim this Compose project name.
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if t, ok := r.lookup(entry.Name()); ok && t.ProjectName == q.ProjectName {
			return out, errTarget
		}
	}
	for _, t := range r.Targets {
		if t.ProjectName == q.ProjectName {
			return out, errTarget
		}
	}
	if os.MkdirAll(root, 0700) != nil {
		return out, errTarget
	}
	if os.Mkdir(out.Directory, 0700) != nil {
		return out, errTarget
	}
	t := Target{ProjectName: q.ProjectName, Directory: out.Directory}
	raw, _ := json.Marshal(t)
	f, e := os.OpenFile(filepath.Join(out.Directory, ".vigara-target.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return out, errTarget
	}
	_, e = f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if e != nil || syncErr != nil || closeErr != nil {
		return out, errTarget
	}
	return out, nil
}
func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
