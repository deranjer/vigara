package composeadoption

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareImportEncryptsDotenvAndReturnsRedactedReceipt(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "lore")
	mustMkdir(t, appDir)
	compose := "services:\n  web:\n    image: example/web\n    env_file: .env\n    environment:\n      LOG_LEVEL: info\n"
	mustWrite(t, filepath.Join(appDir, "compose.yaml"), compose)
	mustWrite(t, filepath.Join(appDir, ".env"), "PASSWORD=not-for-git-or-output\n")
	container := RuntimeContainer{ID: "abcdef0123456789", ProjectName: "lore-server", ServiceName: "web", WorkingDir: appDir, ComposeFiles: []string{"compose.yaml"}, State: "running", StartedAt: "2026-10-11T10:00:00Z"}
	before := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{container}}
	preview, err := Discover(root, before)
	if err != nil {
		t.Fatal(err)
	}
	app := preview.Applications[0]
	if len(app.ReviewRequired) == 0 {
		t.Fatal("expected environment review requirement")
	}
	var seenPlain string
	prepared, err := PrepareImport(context.Background(), root, app, before, ExportSelection{AcknowledgedReview: append([]string(nil), app.ReviewRequired...), ApprovedPlainCompose: append([]string(nil), app.ComposeFiles...), InterpolationOrder: []string{"lore/.env"}, InterpolationOrderConfirmed: true}, []string{"age1editor", "age1target", "age1recovery"}, func(_ context.Context, plain []byte, recipients []string) ([]byte, error) {
		seenPlain = string(plain)
		if len(recipients) != 3 {
			t.Fatalf("recipients = %d", len(recipients))
		}
		return []byte("SOPS-CIPHERTEXT"), nil
	}, func(data []byte) bool { return string(data) == "SOPS-CIPHERTEXT" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenPlain, "not-for-git-or-output") {
		t.Fatal("encryptor did not receive full dotenv")
	}
	var encrypted *ExportArtifact
	for i := range prepared.Files {
		if prepared.Files[i].Sensitivity == "encrypted" {
			encrypted = &prepared.Files[i]
		}
	}
	if encrypted == nil || encrypted.Format != encryptedArtifactFormat || string(encrypted.Content) != "SOPS-CIPHERTEXT" {
		t.Fatalf("encrypted artifact = %#v", encrypted)
	}
	if encrypted.Path != "secrets/imported/.env.sops" {
		t.Fatalf("encrypted path = %q", encrypted.Path)
	}
	if len(prepared.Receipt.Inputs) != 2 || prepared.Receipt.Inputs[0].SourcePath != ".env" || prepared.Receipt.Inputs[0].EncryptedPath != encrypted.Path || prepared.Receipt.Inputs[0].Service != "web" || prepared.Receipt.Inputs[0].Order != 0 {
		t.Fatalf("env_file source mapping was not preserved: %#v", prepared.Receipt.Inputs)
	}
	if prepared.Receipt.Inputs[1].EncryptedPath != encrypted.Path || prepared.Receipt.Inputs[1].InterpolationOrder != 0 {
		t.Fatalf("interpolation mapping does not point to the emitted artifact: %#v", prepared.Receipt.Inputs[1])
	}
	for _, mapping := range prepared.Receipt.Inputs {
		found := false
		for _, artifact := range prepared.Files {
			if artifact.Path == mapping.EncryptedPath {
				found = true
			}
		}
		if !found {
			t.Fatalf("receipt points to missing encrypted artifact %q", mapping.EncryptedPath)
		}
	}
	serialized, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "not-for-git-or-output") || strings.Contains(string(serialized), "SOPS-CIPHERTEXT") {
		t.Fatal("plaintext or ciphertext content leaked to diagnostics")
	}
	after := RuntimeSnapshot{Complete: true, ObservedAt: before.ObservedAt.Add(time.Minute), Containers: []RuntimeContainer{container}}
	if err := VerifyPreparedImportUnchanged(prepared.Receipt, after); err != nil {
		t.Fatalf("unchanged runtime: %v", err)
	}
	after.Containers[0].ID = "0123456789abcdef"
	if err := VerifyPreparedImportUnchanged(prepared.Receipt, after); err == nil {
		t.Fatal("container recreation was not detected")
	}
}

func TestPrepareImportRequiresExplicitSharedEnvAndReview(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	mustMkdir(t, appDir)
	mustWrite(t, filepath.Join(appDir, "compose.yaml"), "services:\n  web:\n    image: example/web\n    environment:\n      LOG_LEVEL: info\n")
	mustWrite(t, filepath.Join(appDir, ".env"), "LOCAL=value\n")
	mustWrite(t, filepath.Join(root, ".env"), "SHARED=value\n")
	c := RuntimeContainer{ID: "abcdef0123456789", ProjectName: "app-runtime", ServiceName: "web", WorkingDir: appDir, ComposeFiles: []string{"compose.yaml"}, State: "running", StartedAt: "2026-10-11T10:00:00Z"}
	before := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{c}}
	preview, err := Discover(root, before)
	if err != nil {
		t.Fatal(err)
	}
	app := preview.Applications[0]
	encrypt := func(context.Context, []byte, []string) ([]byte, error) { return []byte("cipher"), nil }
	validate := func([]byte) bool { return true }
	if _, err = PrepareImport(context.Background(), root, app, before, ExportSelection{}, []string{"a", "b", "c"}, encrypt, validate); err == nil {
		t.Fatal("unacknowledged review was accepted")
	}
	selection := ExportSelection{AcknowledgedReview: append([]string(nil), app.ReviewRequired...), ApprovedPlainCompose: append([]string(nil), app.ComposeFiles...), InterpolationOrderConfirmed: true}
	prepared, err := PrepareImport(context.Background(), root, app, before, selection, []string{"a", "b", "c"}, encrypt, validate)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range prepared.Files {
		if f.Sensitivity == "encrypted" {
			t.Fatal("unselected shared dotenv was included")
		}
	}
	selection.InterpolationOrder = []string{".env", "app/.env"}
	prepared, err = PrepareImport(context.Background(), root, app, before, selection, []string{"a", "b", "c"}, encrypt, validate)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, f := range prepared.Files {
		if f.Sensitivity == "encrypted" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("encrypted input count = %d", count)
	}
}

func TestPrepareImportRejectsChangedComposeAndInvalidCiphertext(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "stack")
	mustMkdir(t, appDir)
	composePath := filepath.Join(appDir, "compose.yaml")
	mustWrite(t, composePath, "services:\n  web:\n    image: example/web\n    env_file: .env\n")
	mustWrite(t, filepath.Join(appDir, ".env"), "TOKEN=hidden\n")
	c := RuntimeContainer{ID: "abcdef0123456789", ProjectName: "stack-runtime", ServiceName: "web", WorkingDir: appDir, ComposeFiles: []string{"compose.yaml"}, State: "running", StartedAt: "2026-10-11T10:00:00Z"}
	snapshot := RuntimeSnapshot{Complete: true, ObservedAt: time.Now().UTC(), Containers: []RuntimeContainer{c}}
	preview, err := Discover(root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	app := preview.Applications[0]
	selection := ExportSelection{AcknowledgedReview: app.ReviewRequired, ApprovedPlainCompose: app.ComposeFiles, InterpolationOrderConfirmed: true}
	encrypt := func(context.Context, []byte, []string) ([]byte, error) {
		return []byte("plaintext-is-not-ciphertext"), nil
	}
	valid := func([]byte) bool { return false }
	if _, err = PrepareImport(context.Background(), root, app, snapshot, selection, []string{"age-a", "age-b", "age-c"}, encrypt, valid); err == nil {
		t.Fatal("unvalidated ciphertext accepted")
	}
	mustWrite(t, composePath, "services:\n  web:\n    image: changed/image\n    env_file: .env\n")
	valid = func([]byte) bool { return true }
	if _, err = PrepareImport(context.Background(), root, app, snapshot, selection, []string{"age-a", "age-b", "age-c"}, encrypt, valid); err == nil {
		t.Fatal("changed Compose file accepted after review")
	}
}
