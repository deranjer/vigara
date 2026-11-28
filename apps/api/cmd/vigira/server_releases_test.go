package main

import "testing"

func TestIsStagingVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		staging bool
	}{
		{"staging", true},
		{"0.1.0-staging.323", true},
		{"v1.2.3", false},
		{"v1.2.3-staging.1", false},
		{"dev", false},
	} {
		if got := isStagingVersion(tc.version); got != tc.staging {
			t.Errorf("isStagingVersion(%q) = %t, want %t", tc.version, got, tc.staging)
		}
	}
}
