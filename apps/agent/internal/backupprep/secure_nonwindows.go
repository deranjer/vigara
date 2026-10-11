//go:build !windows

package backupprep

func securePrivatePath(string, bool) error { return nil }
