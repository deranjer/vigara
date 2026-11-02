package composesecrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAgeEnvironmentRoundTripAndRecovery(t *testing.T) {
	dir := t.TempDir()
	recipients := []string{}
	files := []string{}
	for _, name := range []string{"editor", "agent", "recovery", "replacement"} {
		file := filepath.Join(dir, name+".agekey")
		key, err := EnsureAgeIdentity(file)
		if err != nil {
			t.Fatal(err)
		}
		again, err := EnsureAgeIdentity(file)
		if err != nil || key != again {
			t.Fatal("identity changed", err)
		}
		recipients = append(recipients, key)
		files = append(files, file)
	}
	plain := []byte("# Keep comments\nPASSWORD='import-secret'\nMULTILINE=\"first\nsecond\"\n")
	ciphertext, err := EncryptAgeDotenv(context.Background(), plain, recipients[:3])
	if err != nil {
		t.Fatal(err)
	}
	if !IsAgeArtifact(ciphertext) || strings.Contains(string(ciphertext), "import-secret") {
		t.Fatal("plaintext leaked")
	}
	for _, file := range files[:3] {
		got, err := DecryptDotenv(context.Background(), "not-installed", file, ciphertext)
		if err != nil || string(got) != string(plain) {
			t.Fatal("recipient cannot decrypt", err)
		}
	}
	rotated, err := RotateDotenv(context.Background(), "not-installed", files[0], ciphertext, []string{recipients[0], recipients[1], recipients[3]})
	if err != nil || !IsAgeArtifact(rotated) {
		t.Fatal("rotation changed format", err)
	}
	if _, err = DecryptDotenv(context.Background(), "", files[2], rotated); err == nil {
		t.Fatal("removed recipient could decrypt new revision")
	}
	ciphertext[len(ciphertext)/2] ^= 1
	if _, err = DecryptDotenv(context.Background(), "", files[0], ciphertext); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestImportIdentityRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := EnsureAgeIdentity(filepath.Join(link, "key")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}
