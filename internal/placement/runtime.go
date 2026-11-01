package placement

import (
	"fmt"
	"github.com/deranjer/vigara/internal/stack"
)

// ResolveRuntime verifies the exact manifests carried with a captured job.
// It deliberately supports exclusive initial ownership only. Reassignment and
// replicas require a separate fenced binding implementation.
func ResolveRuntime(s stack.Snapshot) (Profile, Assignment, stack.Manifest, error) {
	m, err := stack.Validate(s)
	var p Profile
	var a Assignment
	if err != nil || s.Placement == nil {
		return p, a, m, err
	}
	x := s.Placement
	if !stack.ValidID(x.ProfileID) || !stack.ValidID(x.AssignmentID) || x.Epoch != 1 {
		return p, a, m, fmt.Errorf("%w: unsupported assignment ownership", ErrInvalid)
	}
	files := map[string]string{
		"profiles/" + x.ProfileID + "/vigara-profile.yaml":  x.ProfileManifest,
		"assignments/" + x.ProfileID + "/" + s.ID + ".yaml": x.AssignmentManifest,
	}
	c, err := Parse(files)
	if err != nil {
		return p, a, m, err
	}
	p = c.Profiles[x.ProfileID]
	a, ok := c.Assignments[x.AssignmentID]
	if !ok || !a.Enabled || a.StackID != s.ID || a.ProfileID != p.ID || a.Ownership.Epoch != x.Epoch || a.Ownership.Mode != "exclusive" || a.ProjectName != "" || a.ReplicaKey != "" {
		return p, a, m, fmt.Errorf("%w: unsupported assignment placement", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, f := range m.Compose.Files {
		seen[f] = true
	}
	for _, f := range m.Compose.InterpolationFiles {
		seen[f.Path] = true
	}
	for _, input := range a.Inputs {
		if seen[input.Path] || s.Files[input.Path] == "" || input.Sensitivity != "encrypted" || (input.Format != "" && input.Format != "sops-binary" && input.Format != "sops-dotenv") {
			return p, a, m, fmt.Errorf("%w: unsupported assignment input", ErrInvalid)
		}
		seen[input.Path] = true
		m.Compose.InterpolationFiles = append(m.Compose.InterpolationFiles, input)
	}
	return p, a, m, nil
}
