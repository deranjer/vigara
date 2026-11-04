//go:build !windows && !linux

package selfupdate

import (
	"errors"
	"os"
)

const Directory = "/var/lib/vigara-updater"
const Binary = "/usr/local/bin/vigara-agent"

func tryLock(*os.File, bool) error { return errors.New("self updates require Windows or Linux") }
func unlock(*os.File)              {}
func lockBusy(error) bool          { return false }
func privileged() bool             { return false }
func service(string) error         { return errors.New("unsupported platform") }
func syncParent(string) error      { return nil }
