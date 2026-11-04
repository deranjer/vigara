package imageupdates

import (
	"strings"
	"testing"
)

func TestMonitoringReferenceSeparatesPinsAndTags(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ ref, want string }{
		{"ghcr.io/bookorbit/bookorbit:latest@" + digest, "ghcr.io/bookorbit/bookorbit:latest"},
		{"docker.io/pgvector/pgvector@" + digest, ""},
		{"registry:5000/team/database@" + digest, ""},
		{"registry:5000/team/database:pg17@" + digest, "registry:5000/team/database:pg17"},
		{"postgres:17", "postgres:17"}, {"nginx", "nginx"}, {digest, ""}, {"repo:latest@sha256:bad", ""},
	} {
		if got := MonitoringReference(tc.ref); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.ref, got, tc.want)
		}
	}
}
func TestMonitoringTagUsesDeployedRepository(t *testing.T) {
	ref := "registry:5000/team/database@sha256:" + strings.Repeat("a", 64)
	if got := WithMonitoringTag(ref, "pg17"); got != "registry:5000/team/database:pg17" {
		t.Fatal(got)
	}
	for _, tag := range []string{"latest@sha256:bad", "other/repo:latest", "--help", "v1 v2", "../latest", ""} {
		if got := WithMonitoringTag(ref, tag); got != "" {
			t.Fatal("invalid tag accepted", tag, got)
		}
	}
}
