//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const Directory = "/var/lib/vigara-updater"
const Binary = "/usr/local/bin/vigara-agent"

func tryLock(f *os.File, exclusive bool) error {
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	return unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
}
func unlock(f *os.File)       { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func lockBusy(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) }
func service(action string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/systemctl", action, "vigara-agent.service").Run()
}
func privileged() bool { return os.Geteuid() == 0 }

func syncParent(path string) error {
	f, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
