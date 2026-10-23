package stack

import (
	"strings"
	"testing"
)

func TestPortablePaths(t *testing.T) {
	for _, p := range []string{"../outside", "a/../../b", "/root", "a\\b", ".git/config", ".GIT/config", "a/.vigara-jobs/x", "CON", "NUL.txt", "dir/COM1.yaml", "dir./x", "a//b", "a/./b"} {
		if ValidPath(p) {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"compose.yaml", "config/app.yaml", "secrets/runtime.env.sops"} {
		if !ValidPath(p) {
			t.Errorf("rejected %q", p)
		}
	}
}
func TestRejectAmbiguousAndUnsupportedInputs(t *testing.T) {
	original := Snapshot{ID: "demo", Files: map[string]string{"vigara-stack.yaml": "apiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: demo\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n", "compose.yaml": "services:\n  web:\n    image: nginx\n"}}
	if _, e := Validate(original); e != nil {
		t.Fatal(e)
	}
	cases := []func(map[string]string){
		func(f map[string]string) { f["Compose.yaml"] = f["compose.yaml"] },
		func(f map[string]string) { f["compose.yaml/nested"] = "x" },
		func(f map[string]string) { f[".env"] = "PASSWORD=x" },
		func(f map[string]string) { f["compose.yaml"] += "---\nservices: {}\n" },
		func(f map[string]string) { f["vigara-stack.yaml"] += "unknown: true\n" },
		func(f map[string]string) { f["config.yaml"] = strings.Repeat("x", 129<<10) },
	}
	for i, change := range cases {
		f := map[string]string{}
		for p, v := range original.Files {
			f[p] = v
		}
		change(f)
		if _, e := Validate(Snapshot{ID: "demo", Files: f}); e == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	a := Digest(original)
	original.Files["compose.yaml"] = "# comment\n" + original.Files["compose.yaml"]
	if a == Digest(original) {
		t.Fatal("raw content must affect digest")
	}
}

func TestEnvironmentMetadataPreservesCommentsAndInputOrder(t *testing.T) {
	source := "# Keep the stack description\napiVersion: vigara.io/v1\nkind: Stack\nstackId: demo\ndisplayName: Demo\nprojectName: demo\ncompose:\n  files: [compose.yaml]\n  projectDirectory: .\n  interpolationFiles: []\nconfigurationRevisionPolicy: sha256-git-tree-and-placement-inputs-v1\n"
	refs := []RecipientRef{{Class: "central-editor", KeyRef: "editor-main"}, {Class: "target-agent", KeyRef: "agent-main"}, {Class: "operator-recovery", KeyRef: "recovery-main"}}
	one, e := WithEncryptedInput(source, "secrets/first.env.sops", refs)
	if e != nil {
		t.Fatal(e)
	}
	two, e := WithEncryptedInput(one, "secrets/second.env.sops", refs)
	if e != nil {
		t.Fatal(e)
	}
	updated, e := WithEncryptedInput(two, "secrets/first.env.sops", refs)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(updated, "# Keep the stack description") {
		t.Fatal("metadata comment lost")
	}
	m, e := Validate(Snapshot{ID: "demo", Files: map[string]string{"vigara-stack.yaml": updated, "compose.yaml": "services:\n  web:\n    image: nginx\n", "secrets/first.env.sops": "encrypted fixture", "secrets/second.env.sops": "encrypted fixture"}})
	if e != nil || len(m.Compose.InterpolationFiles) != 2 || m.Compose.InterpolationFiles[0].Path != "secrets/first.env.sops" {
		t.Fatalf("input order or metadata changed: %+v %v", m.Compose.InterpolationFiles, e)
	}
}
