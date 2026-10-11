//go:build !windows

package composesecrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"filippo.io/age"
)

func protectDirectory(path string) error { return os.Chmod(path, 0700) }
func protectFile(path string) error      { return os.Chmod(path, 0600) }

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0700 != 0700 || info.Mode().Perm()&0077 != 0 {
		return errors.New("identity directory is not private")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return errors.New("identity directory ownership is invalid")
	}
	return nil
}

func validateIdentityFile(path string) error {
	_, err := recipientFromIdentityFile(path)
	return err
}

func recipientFromIdentityFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0400 == 0 || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return "", errors.New("identity file is not private")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return "", errors.New("identity file ownership is invalid")
	}
	if err := validatePrivateDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("identity file is invalid")
	}
	identities, err := age.ParseIdentities(bytes.NewReader(contents))
	if err != nil || len(identities) != 1 {
		return "", errors.New("identity file is invalid")
	}
	x25519Identity, ok := identities[0].(*age.X25519Identity)
	if !ok {
		return "", errors.New("identity file is invalid")
	}
	return x25519Identity.Recipient().String(), nil
}
