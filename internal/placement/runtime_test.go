package placement

import (
	"github.com/deranjer/vigara/internal/stack"
	"strings"
	"testing"
)

func runtimeFixture() stack.Snapshot {
	return stack.Snapshot{ID: "web", Files: map[string]string{
		"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: web\ndisplayName: Web\nprojectName: web\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
		"compose.yaml":      "services:\n  web:\n    image: nginx\n",
	}, Placement: &stack.Placement{ProfileID: "north", AssignmentID: "web-north", Epoch: 1, ProfileManifest: "apiVersion: vigara.io/v1\nkind: ServerProfile\nprofileId: north\ndisplayName: North\nrequirements: {}\n", AssignmentManifest: "apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nassignmentId: web-north\nstackId: web\nprofileId: north\nenvironment: production\nownership:\n  mode: exclusive\n  ownershipEpoch: 1\nenabled: true\n"}}
}

func TestRuntimeOwnershipAndInputBoundaries(t *testing.T) {
	s := runtimeFixture()
	p, a, m, err := ResolveRuntime(s)
	if err != nil || p.ID != "north" || a.ID != "web-north" || m.ProjectName != "web" {
		t.Fatalf("valid: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*stack.Snapshot)
	}{
		{"wrong epoch", func(s *stack.Snapshot) { s.Placement.Epoch = 2 }},
		{"wrong assignment", func(s *stack.Snapshot) { s.Placement.AssignmentID = "other" }},
		{"disabled", func(s *stack.Snapshot) {
			s.Placement.AssignmentManifest = strings.Replace(s.Placement.AssignmentManifest, "enabled: true", "enabled: false", 1)
		}},
		{"replicas", func(s *stack.Snapshot) {
			s.Placement.AssignmentManifest = strings.Replace(s.Placement.AssignmentManifest, "exclusive", "stateless", 1)
		}},
		{"project move", func(s *stack.Snapshot) { s.Placement.AssignmentManifest += "projectNameOverride: other\n" }},
		{"missing profile", func(s *stack.Snapshot) { s.Placement.ProfileManifest = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := runtimeFixture()
			test.change(&s)
			if _, _, _, err := ResolveRuntime(s); err == nil {
				t.Fatal("unsafe placement accepted")
			}
		})
	}
}

func TestPlacementIsBoundIntoDeploymentDigests(t *testing.T) {
	s := runtimeFixture()
	before := stack.Digest(s)
	revision := stack.ConfigurationRevision(s)
	s.Placement.ProfileManifest += "# Changed storage requirements\n"
	if stack.Digest(s) == before || stack.ConfigurationRevision(s) == revision {
		t.Fatal("profile change not bound")
	}
	s = runtimeFixture()
	s.Commit = strings.Repeat("f", 40)
	if stack.ConfigurationRevision(s) != revision {
		t.Fatal("content revision incorrectly depends on commit")
	}
}
