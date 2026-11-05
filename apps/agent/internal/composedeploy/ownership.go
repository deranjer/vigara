package composedeploy

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var errStackOwner = errors.New("the configured Compose stack owner is unavailable or ownership could not be applied; check the local user/group and agent permissions")
var stackOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?$`)

type stackOwnership struct{ uid, gid int }

// The owner comes exclusively from the operator's local agent configuration.
// Empty keeps the service account's existing behavior.
func (r *Runner) stackOwnership() (*stackOwnership, error) {
	if r.StackOwner == "" {
		return nil, nil
	}
	if !stackOwnerPattern.MatchString(r.StackOwner) {
		return nil, errStackOwner
	}
	parts := strings.Split(r.StackOwner, ":")
	var account *user.User
	var err error
	if _, e := strconv.Atoi(parts[0]); e == nil {
		account, err = user.LookupId(parts[0])
	} else {
		account, err = user.Lookup(parts[0])
	}
	if err != nil {
		return nil, errStackOwner
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return nil, errStackOwner
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return nil, errStackOwner
	}
	if len(parts) == 2 {
		var group *user.Group
		if _, e := strconv.Atoi(parts[1]); e == nil {
			group, err = user.LookupGroupId(parts[1])
		} else {
			group, err = user.LookupGroup(parts[1])
		}
		if err != nil {
			return nil, errStackOwner
		}
		gid, err = strconv.Atoi(group.Gid)
		if err != nil {
			return nil, errStackOwner
		}
	}
	return &stackOwnership{uid: uid, gid: gid}, nil
}

func (o *stackOwnership) file(f *os.File) error {
	if o == nil {
		return nil
	}
	if err := f.Chown(o.uid, o.gid); err != nil {
		return errStackOwner
	}
	return nil
}

// Change only this directory, confined to its approved root. Never recurse into
// bind sources, volumes, private journals, identities, or imported application data.
func (o *stackOwnership) directory(approvedRoot, path string) error {
	if o == nil {
		return nil
	}
	relative, err := filepath.Rel(approvedRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errStackOwner
	}
	root, err := os.OpenRoot(approvedRoot)
	if err != nil {
		return errStackOwner
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errStackOwner
	}
	if err := root.Lchown(relative, o.uid, o.gid); err != nil {
		return errStackOwner
	}
	return nil
}

// Create only configuration parents. Existing directories retain their owners,
// since they may also be application data or bind mount sources.
func (o *stackOwnership) parents(rootPath, relativeFile string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return errTarget
	}
	defer root.Close()
	parent := filepath.Dir(filepath.FromSlash(relativeFile))
	if parent == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(parent, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		if part == ".." || part == "" {
			return errTarget
		}
		created := false
		if err := root.Mkdir(current, 0700); err == nil {
			created = true
		} else if !os.IsExist(err) {
			return errTarget
		}
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errTarget
		}
		if created && o != nil {
			if err := root.Lchown(current, o.uid, o.gid); err != nil {
				return errStackOwner
			}
		}
	}
	return nil
}
