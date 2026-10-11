package composesecrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestRecipientPolicy(t *testing.T) {
	valid := []RecipientRecord{
		{Class: CentralEditor, KeyRef: "editor-1", Recipient: fakeRecipient(t)},
		{Class: TargetAgent, KeyRef: "agent-1", Recipient: fakeRecipient(t)},
		{Class: OperatorRecovery, KeyRef: "recovery-1", Recipient: fakeRecipient(t)},
	}
	if err := ValidateRecipientPolicy(valid); err != nil {
		t.Fatalf("valid policy: %v (%+v)", err, valid)
	}
	for name, policy := range map[string][]RecipientRecord{
		"no recovery":         valid[:2],
		"no editor":           valid[1:],
		"duplicate class":     append(append([]RecipientRecord{}, valid...), RecipientRecord{Class: CentralEditor, KeyRef: "editor-2", Recipient: fakeRecipient(t)}),
		"duplicate recipient": append(append([]RecipientRecord{}, valid...), RecipientRecord{Class: TargetAgent, KeyRef: "agent-2", Recipient: valid[1].Recipient}),
		"malformed ref":       {{Class: CentralEditor, KeyRef: "../private", Recipient: fakeRecipient(t)}, valid[1], valid[2]},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRecipientPolicy(policy); !errors.Is(err, ErrInvalidRecipients) {
				t.Fatalf("expected invalid recipient policy, got %v", err)
			}
		})
	}
}

func TestSOPSArtifactClassifierRejectsExternalProviders(t *testing.T) {
	recipient := fakeRecipient(t)
	good := []byte(`{"data":"ENC[AES256_GCM,data:abc,iv:abc,tag:abc,type:str]","sops":{"age":[{"recipient":"` + recipient + `","enc":"age-stanza"}],"lastmodified":"2026-10-11T00:00:00Z","mac":"ENC[AES256_GCM,data:abc,iv:abc,tag:abc,type:str]","version":"3.13.3"}}`)
	if !IsEncryptedBinaryArtifact(good) {
		t.Fatal("age-only SOPS binary artifact rejected")
	}
	withKMS := bytes.Replace(good, []byte(`"version":"3.13.3"`), []byte(`"version":"3.13.3","kms":[]`), 1)
	if IsEncryptedBinaryArtifact(withKMS) {
		t.Fatal("SOPS artifact with KMS metadata accepted")
	}
	legacy := []byte("TOKEN=ENC[AES256_GCM,data:abc,iv:abc,tag:abc,type:str]\nsops_age__list_0__map_recipient=" + recipient + "\nsops_age__list_0__map_enc=-----BEGIN AGE ENCRYPTED FILE-----\nsops_lastmodified=2026-10-11T00:00:00Z\nsops_mac=ENC[AES256_GCM,data:abc,iv:abc,tag:abc,type:str]\nsops_version=3.13.3\n")
	if !isSOPSDotenv(legacy) {
		t.Fatal("age-only legacy SOPS dotenv artifact rejected")
	}
	legacy = append(legacy, []byte("sops_azure_kv__list_0__map_vault_url=https://kms.invalid\n")...)
	if isSOPSDotenv(legacy) {
		t.Fatal("legacy SOPS artifact with external provider metadata accepted")
	}
}

func TestSOPSAgeRoundTripRotationAndRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("protected secret subprocess workspaces are Linux-first")
	}
	sops, keygen := cryptoTools(t)
	ctx := context.Background()
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	central := mustIdentity(t, ctx, keygen, root, CentralEditor, "editor")
	target := mustIdentity(t, ctx, keygen, root, TargetAgent, "agent")
	recovery := mustIdentity(t, ctx, keygen, root, OperatorRecovery, "recovery")
	unrelated := mustIdentity(t, ctx, keygen, root, TargetAgent, "unrelated")

	// Compose supports physical newlines inside quoted env values. Encrypting the
	// whole document as SOPS binary must preserve the exact bytes and hide comments.
	plain := []byte("PASSWORD=\"line one\nline two\"\nQUOTED='a \"quote\" # value'\nHASH=\"#not-a-comment\"\n# sensitive comment\nEMPTY=\n")
	policy := []RecipientRecord{
		{Class: CentralEditor, KeyRef: "editor", Recipient: central.Recipient},
		{Class: TargetAgent, KeyRef: "agent", Recipient: target.Recipient},
		{Class: OperatorRecovery, KeyRef: "recovery", Recipient: recovery.Recipient},
	}
	ciphertext, err := EncryptForPolicy(ctx, sops, plain, policy)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !isSOPSBinary(ciphertext) {
		t.Fatal("ciphertext is not an SOPS binary artifact")
	}
	for _, secret := range [][]byte{[]byte("PASSWORD"), []byte("line one"), []byte("sensitive comment"), []byte("a \"quote\"")} {
		if bytes.Contains(ciphertext, secret) {
			t.Fatalf("ciphertext exposed plaintext fragment %q", secret)
		}
	}
	for _, identity := range []Identity{central, target, recovery} {
		got, err := DecryptDotenv(ctx, sops, identity.PrivatePath, ciphertext)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("roundtrip for %s failed: %v", identity.Class, err)
		}
	}
	if _, err := DecryptDotenv(ctx, sops, unrelated.PrivatePath, ciphertext); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("unrelated identity should fail generically, got %v", err)
	}

	// Independently invoke SOPS, the same interoperable tool used by operators.
	cmd := exec.Command(sops, "decrypt", "--input-type", "binary", "--output-type", "binary")
	cmd.Stdin = bytes.NewReader(ciphertext)
	cmd.Env = []string{"HOME=" + t.TempDir(), "SOPS_AGE_KEY_FILE=" + recovery.PrivatePath}
	independent, err := cmd.Output()
	if err != nil || !bytes.Equal(independent, plain) {
		t.Fatalf("independent SOPS decrypt failed: %v", err)
	}

	// Tampering fails authentication. The returned error is fixed and contains
	// neither the plaintext nor SOPS's potentially sensitive diagnostic context.
	var envelope map[string]any
	if err := json.Unmarshal(ciphertext, &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(string)
	idx := strings.Index(data, "data:") + len("data:")
	if idx < len("data:") || idx >= len(data) {
		t.Fatal("unexpected SOPS ciphertext shape")
	}
	if data[idx] == 'A' {
		data = data[:idx] + "B" + data[idx+1:]
	} else {
		data = data[:idx] + "A" + data[idx+1:]
	}
	envelope["data"] = data
	tampered, _ := json.Marshal(envelope)
	if _, err := DecryptDotenv(ctx, sops, recovery.PrivatePath, tampered); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("tampered document should fail generically, got %v", err)
	}

	newTarget := mustIdentity(t, ctx, keygen, root, TargetAgent, "agent-next")
	rotatedPolicy := []RecipientRecord{
		{Class: CentralEditor, KeyRef: "editor", Recipient: central.Recipient},
		{Class: TargetAgent, KeyRef: "agent-next", Recipient: newTarget.Recipient},
		{Class: OperatorRecovery, KeyRef: "recovery", Recipient: recovery.Recipient},
	}
	rotated, err := RotateForPolicy(ctx, sops, central.PrivatePath, ciphertext, rotatedPolicy)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	for _, identity := range []Identity{central, newTarget, recovery} {
		got, err := DecryptDotenv(ctx, sops, identity.PrivatePath, rotated)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("rotated decrypt for %s: %v", identity.Class, err)
		}
	}
	if _, err := DecryptDotenv(ctx, sops, target.PrivatePath, rotated); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("removed recipient should not decrypt new ciphertext, got %v", err)
	}
}

func TestMaterializePermissionsAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACL implementation is not enabled; materialization fails closed")
	}
	m, err := Materialize(t.TempDir(), []byte("TOKEN=private\n"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file mode is not 0600: %v %v", info, err)
	}
	dirInfo, err := os.Stat(filepath.Dir(m.Path))
	if err != nil || dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("directory mode is not 0700: %v %v", dirInfo, err)
	}
	oldPath := m.Path
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("file disappeared before cleanup: %v", err)
	}
	if err := m.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove file: %v", err)
	}
}

func TestRejectUnsafeIdentityFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACL implementation is not enabled")
	}
	ctx := context.Background()
	sops, keygen := cryptoTools(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	id := mustIdentity(t, ctx, keygen, root, CentralEditor, "editor")
	if err := os.Chmod(id.PrivatePath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateIdentityFile(id.PrivatePath); err == nil {
		t.Fatal("group-readable identity accepted")
	}
	if err := os.Chmod(id.PrivatePath, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "identity-link")
	if err := os.Symlink(id.PrivatePath, link); err != nil {
		t.Fatal(err)
	}
	if err := validateIdentityFile(link); err == nil {
		t.Fatal("identity symlink accepted")
	}
	large := filepath.Join(root, "large-identity")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), 4097), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateIdentityFile(large); err == nil {
		t.Fatal("oversized identity accepted")
	}
	_ = sops // both tools are deliberately required in CI; only age-keygen is used here
}

func cryptoTools(t *testing.T) (string, string) {
	t.Helper()
	sops := os.Getenv("VIGIRA_SOPS_BINARY")
	keygen := os.Getenv("VIGIRA_AGE_KEYGEN_BINARY")
	if sops == "" {
		sops, _ = exec.LookPath("sops")
	}
	if keygen == "" {
		keygen, _ = exec.LookPath("age-keygen")
	}
	if sops == "" || keygen == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("CI must install pinned SOPS and age-keygen binaries")
		}
		t.Skip("real SOPS/age tests run when pinned CI tools are installed")
	}
	return sops, keygen
}

func mustIdentity(t *testing.T, ctx context.Context, keygen, root string, class RecipientClass, ref string) Identity {
	t.Helper()
	id, err := ProvisionIdentity(ctx, keygen, root, class, ref)
	if err != nil {
		t.Fatalf("provision %s identity: %v", class, err)
	}
	return id
}

func fakeRecipient(t *testing.T) string {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return identity.Recipient().String()
}
