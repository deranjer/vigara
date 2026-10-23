package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// Keep old credentials readable until the installer migrates them. Prefer
// service-wide state on Windows so a manual run uses the installed identity.
func defaultStatePath(userConfigDir string) string {
	var candidates []string
	if runtime.GOOS == "windows" && os.Getenv("ProgramData") != "" {
		candidates = append(candidates,
			filepath.Join(os.Getenv("ProgramData"), "Vigara", "agent.json"),
			filepath.Join(os.Getenv("ProgramData"), "Vigira", "agent.json"))
	}
	candidates = append(candidates,
		filepath.Join(userConfigDir, "vigara", "agent.json"),
		filepath.Join(userConfigDir, "vigira", "agent.json"))
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil || !os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(userConfigDir, "vigara", "agent.json")
}
