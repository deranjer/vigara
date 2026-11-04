//go:build linux

package selfupdate

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRestrictiveServiceUmaskPreservesExecutablePermissions(t *testing.T) {
	old := unix.Umask(0077)
	defer unix.Umask(old)
	f := fixture(t)
	if err := f.r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.r.binary, f.r.binary + ".update-old"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0755 {
			t.Fatalf("%s permissions = %o; restricted agent could not execute", path, info.Mode().Perm())
		}
	}
}
