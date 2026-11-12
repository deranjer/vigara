package stacks

import "testing"

func TestPinnedVersionTag(t *testing.T) {
	for ref, want := range map[string]bool{
		"postgres:17.2.1":                            true,
		"ghcr.io/org/app:v2.4.0-alpine":              true,
		"lscr.io/linuxserver/plex:1.40.0.7998-ls245": true,
		"registry:5000/app:1.2.3":                    true,
		"postgres:17-alpine":                         false,
		"postgres:17":                                false,
		"nginx:1.27":                                 false,
		"nginx:latest":                               false,
		"nginx":                                      false,
		"registry:5000/app":                          false,
		"postgres:17.2.1@sha256:abc":                 false,
	} {
		if got := pinnedVersionTag(ref); got != want {
			t.Errorf("pinnedVersionTag(%q)=%v want %v", ref, got, want)
		}
	}
}
