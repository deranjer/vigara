package mountfiles

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

const safeOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func privateBackup(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0
}

func fileIdentity(info fs.FileInfo) string {
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Uid, stat.Gid, stat.Ctim.Sec, stat.Ctim.Nsec)
}
