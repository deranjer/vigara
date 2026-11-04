// Package envcompat preserves the original environment variable names while
// making VIGARA_* the preferred configuration namespace for executables.
package envcompat

import (
	"os"
	"strings"
)

// Getenv also supports libraries invoked directly, without executable startup.
func Getenv(key string) string {
	canonical := strings.Replace(key, "VIGIRA_", "VIGARA_", 1)
	if value, ok := os.LookupEnv(canonical); ok {
		return value
	}
	legacy := strings.Replace(key, "VIGARA_", "VIGIRA_", 1)
	return os.Getenv(legacy)
}

// ApplyAliases runs once at executable startup. Explicit VIGARA_* settings,
// including empty values, take precedence over their VIGIRA_* aliases.
// Existing installations using only VIGIRA_* settings remain unchanged.
func ApplyAliases() {
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, "VIGARA_") {
			_ = os.Setenv("VIGIRA_"+strings.TrimPrefix(name, "VIGARA_"), value)
		}
	}
}
