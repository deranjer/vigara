package placement

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/stack"
)

func profile(id string) string {
	return `{"apiVersion":"vigara.io/v1","kind":"ServerProfile","profileId":"` + id + `","displayName":"DNS server","requirements":{}}`
}
func assignment(id, stackID, profileID, mode, project string, enabled bool) string {
	a := map[string]any{"apiVersion": "vigara.io/v1", "kind": "DeploymentAssignment", "assignmentId": id, "stackId": stackID, "profileId": profileID, "environment": "production", "ownership": map[string]any{"mode": mode, "ownershipEpoch": 1}, "enabled": enabled}
	if mode == "stateless" {
		a["replicaKey"] = id
	}
	if project != "" {
		a["projectNameOverride"] = project
	}
	b, _ := json.Marshal(a)
	return string(b)
}
func savedStack(id string) stack.Snapshot {
	return stack.Snapshot{ID: id, Files: map[string]string{
		"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: " + id + "\ndisplayName: AdGuard\nprojectName: adguard\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n",
		"compose.yaml":      "services:\n  app:\n    image: example/app\n",
	}}
}
func TestIndependentAdguardInstances(t *testing.T) {
	files := map[string]string{"profiles/primary/vigara-profile.yaml": profile("primary"), "profiles/secondary/vigara-profile.yaml": profile("secondary"), "assignments/primary/adguard-primary.yaml": assignment("dns-primary", "adguard-primary", "primary", "exclusive", "", true), "assignments/secondary/adguard-secondary.yaml": assignment("dns-secondary", "adguard-secondary", "secondary", "exclusive", "", true)}
	stacks := map[string]stack.Snapshot{"adguard-primary": savedStack("adguard-primary"), "adguard-secondary": savedStack("adguard-secondary")}
	if _, err := Validate(files, stacks); err != nil {
		t.Fatal(err)
	}
	// Same project is allowed on distinct profiles, but not the same one.
	delete(files, "assignments/secondary/adguard-secondary.yaml")
	files["assignments/primary/adguard-secondary.yaml"] = assignment("dns-secondary", "adguard-secondary", "primary", "exclusive", "", true)
	if _, err := Validate(files, stacks); !errors.Is(err, ErrInvalid) {
		t.Fatalf("project collision accepted: %v", err)
	}
}
func TestOwnershipAndReferences(t *testing.T) {
	stacks := map[string]stack.Snapshot{"web": savedStack("web")}
	for _, tc := range []struct {
		name, mode1, mode2, project2 string
		enabled2, want               bool
	}{
		{"exclusive conflict", "exclusive", "exclusive", "other", true, false},
		{"mixed ownership", "exclusive", "stateless", "other", true, false},
		{"replicas require distinct projects", "stateless", "stateless", "adguard", true, false},
		{"stateless replicas", "stateless", "stateless", "other", true, true},
		{"disabled placement does not own", "exclusive", "exclusive", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"profiles/north/vigara-profile.yaml": profile("north"), "profiles/south/vigara-profile.yaml": profile("south"), "assignments/north/web.yaml": assignment("web-north", "web", "north", tc.mode1, "", true), "assignments/south/web.yaml": assignment("web-south", "web", "south", tc.mode2, tc.project2, tc.enabled2)}
			_, err := Validate(files, stacks)
			if (err == nil) != tc.want {
				t.Fatalf("valid=%v: %v", tc.want, err)
			}
		})
	}
	files := map[string]string{"assignments/north/web.yaml": assignment("web-north", "web", "north", "exclusive", "", false)}
	if _, err := Validate(files, stacks); err == nil {
		t.Fatal("missing profile accepted")
	}
	files["profiles/north/vigara-profile.yaml"] = profile("north")
	if _, err := Validate(files, nil); err == nil {
		t.Fatal("missing stack accepted")
	}
}
func TestStrictManifestInputs(t *testing.T) {
	valid := assignment("web-north", "web", "north", "exclusive", "", false)
	for _, text := range []string{
		strings.Replace(valid, `"enabled":false,`, "", 1),
		strings.Replace(valid, `"ownershipEpoch":1`, `"ownershipEpoch":0`, 1),
		strings.Replace(valid, `"kind":"DeploymentAssignment"`, `"kind":"ServerProfile"`, 1),
		strings.TrimSuffix(valid, "}") + `,"agentId":"do-not-trust"}`,
		valid + "\n---\n" + valid,
		"apiVersion: vigara.io/v1\nkind: DeploymentAssignment\nkind: Stack\n",
	} {
		if _, err := Parse(map[string]string{"assignments/north/web.yaml": text}); err == nil {
			t.Fatalf("invalid accepted: %s", text)
		}
	}
	for _, path := range []string{"../profiles/north/vigara-profile.yaml", "profiles/north/other.yaml", "assignments/north/../web.yaml", "stacks/web/compose.yaml", "assignments/north/Web.yaml"} {
		if ValidPath(path) {
			t.Fatalf("unsafe path %s", path)
		}
	}
	if _, err := Parse(map[string]string{"assignments/wrong/web.yaml": valid}); err == nil {
		t.Fatal("identity mismatch accepted")
	}
}
func TestAssignmentInputsAndDependencies(t *testing.T) {
	s := savedStack("web")
	s.Files["secrets/target.env.sops"] = "ciphertext fixture"
	files := map[string]string{"profiles/north/vigara-profile.yaml": profile("north"), "assignments/north/web.yaml": assignment("web-north", "web", "north", "exclusive", "", false)}
	var a map[string]any
	_ = json.Unmarshal([]byte(files["assignments/north/web.yaml"]), &a)
	a["interpolationFiles"] = []any{map[string]any{"path": "secrets/target.env.sops", "purpose": "deployment", "sensitivity": "encrypted"}}
	b, _ := json.Marshal(a)
	files["assignments/north/web.yaml"] = string(b)
	if _, err := Validate(files, map[string]stack.Snapshot{"web": s}); err == nil {
		t.Fatal("missing recipients accepted")
	}
	a["secretRecipientRefs"] = []any{map[string]any{"class": "central-editor", "keyRef": "editor"}, map[string]any{"class": "target-agent", "keyRef": "target"}, map[string]any{"class": "operator-recovery", "keyRef": "recovery"}}
	b, _ = json.Marshal(a)
	files["assignments/north/web.yaml"] = string(b)
	if _, err := Validate(files, map[string]stack.Snapshot{"web": s}); err != nil {
		t.Fatal(err)
	}
	delete(s.Files, "secrets/target.env.sops")
	if _, err := Validate(files, map[string]stack.Snapshot{"web": s}); err == nil {
		t.Fatal("missing input accepted")
	}
	files["assignments/north/web.yaml"] = assignment("web-north", "web", "north", "exclusive", "", true)
	files["profiles/north/vigara-profile.yaml"] = strings.Replace(profile("north"), `"requirements":{}`, `"requirements":{"startupDependencies":[{"before":"web","after":"web","reason":"cycle"}]}`, 1)
	if _, err := Validate(files, map[string]stack.Snapshot{"web": s}); err == nil {
		t.Fatal("dependency cycle accepted")
	}
}
