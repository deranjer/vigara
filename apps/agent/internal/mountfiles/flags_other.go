//go:build !linux

package mountfiles

import "io/fs"

// Registration is Linux-only. This keeps the shared agent build portable.
const safeOpenFlags = 0

func privateBackup(info fs.FileInfo) bool { return false }

func fileIdentity(info fs.FileInfo) string { return "" }
