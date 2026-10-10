//go:build windows

package runtime

import (
	"testing"

	"golang.org/x/sys/windows"
)

func assertPrivateStateFile(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("agent state DACL is not protected from inherited access")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatal("agent state DACL is missing")
	}
	if dacl.AceCount != 3 {
		t.Fatalf("agent state ACL has %d entries, want account, administrators, and LocalSystem", dacl.AceCount)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != windows.ACCESS_MASK(0x1f01ff) {
			t.Fatalf("unexpected agent state ACL entry: type=%d mask=%#x", ace.Header.AceType, ace.Mask)
		}
	}
}
