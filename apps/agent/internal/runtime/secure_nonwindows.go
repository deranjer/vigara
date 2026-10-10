//go:build !windows

package runtime

func securePrivatePath(string, bool) error { return nil }
