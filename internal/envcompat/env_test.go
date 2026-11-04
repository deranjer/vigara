package envcompat

import (
	"os"
	"testing"
)

func TestCanonicalEnvironmentOverridesLegacyIncludingEmpty(t *testing.T) {
	t.Setenv("VIGIRA_ALIAS_TEST", "old")
	t.Setenv("VIGARA_ALIAS_TEST", "new")
	ApplyAliases()
	if got := os.Getenv("VIGIRA_ALIAS_TEST"); got != "new" {
		t.Fatalf("alias = %q", got)
	}
	t.Setenv("VIGARA_ALIAS_TEST", "")
	ApplyAliases()
	if got := os.Getenv("VIGIRA_ALIAS_TEST"); got != "" {
		t.Fatalf("explicit empty value ignored: %q", got)
	}
}

func TestGetenvAcceptsEitherNamespaceAndPreservesPrecedence(t *testing.T) {
	t.Setenv("VIGIRA_LOOKUP_TEST", "legacy")
	if got := Getenv("VIGARA_LOOKUP_TEST"); got != "legacy" {
		t.Fatalf("legacy lookup = %q", got)
	}
	t.Setenv("VIGARA_LOOKUP_TEST", "canonical")
	if got := Getenv("VIGIRA_LOOKUP_TEST"); got != "canonical" {
		t.Fatalf("canonical precedence = %q", got)
	}
	t.Setenv("VIGARA_LOOKUP_TEST", "")
	if got := Getenv("VIGIRA_LOOKUP_TEST"); got != "" {
		t.Fatalf("empty canonical precedence = %q", got)
	}
}
