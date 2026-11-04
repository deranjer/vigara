package stacks

import (
	"strings"

	"github.com/deranjer/vigara/internal/imageupdates"
	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/stack"
	"gopkg.in/yaml.v3"
)

// Recover source references only from the exact recorded deployment snapshot,
// never a newer Git revision. Interpolated images need agent-side rendering;
// leave them unresolved instead of reading secrets or guessing a version.
func recoverImageTracking(s stack.Snapshot, known map[string]string) map[string]string {
	refs := map[string]string{}
	_, _, m, err := placement.ResolveRuntime(s)
	if err == nil {
		for _, file := range m.Compose.Files {
			var model struct {
				Services map[string]struct {
					Image string `yaml:"image"`
				} `yaml:"services"`
			}
			if yaml.Unmarshal([]byte(s.Files[file]), &model) != nil {
				return known
			}
			for service, v := range model.Services {
				if v.Image != "" {
					refs[service] = v.Image
				}
			}
		}
	}
	for service, ref := range refs {
		if strings.Contains(ref, "$") || imageupdates.MonitoringReference(ref) == "" {
			delete(refs, service)
		}
	}
	for service, ref := range known {
		if ref != "" {
			refs[service] = ref
		}
	}
	return refs
}
