//go:build windows

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Match the canonical installer locations; server responses cannot change them.
var Directory = filepath.Join(os.Getenv("ProgramFiles"), "Vigara", "updater")
var Binary = filepath.Join(os.Getenv("ProgramFiles"), "Vigara", "vigara-agent.exe")

func tryLock(f *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
}
func unlock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
func lockBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}
func privileged() bool        { return windows.GetCurrentProcessToken().IsElevated() }
func syncParent(string) error { return nil }
func service(action string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService("VigaraAgent")
	if err != nil {
		return err
	}
	defer s.Close()
	wanted := svc.Running
	if action == "stop" {
		wanted = svc.Stopped
		_, err = s.Control(svc.Stop)
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
	} else {
		err = s.Start()
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		status, e := s.Query()
		if e != nil {
			return e
		}
		if status.State == wanted {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("agent service transition timed out")
}
