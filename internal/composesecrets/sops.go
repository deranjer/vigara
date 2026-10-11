// Package composesecrets provides the SOPS/age boundary used by Compose imports
// and deployments. Cryptography is delegated to the interoperable SOPS CLI.
package composesecrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"filippo.io/age"
)

const maxDocumentBytes = 8 << 20
const ArtifactFormat = "sops-binary"

type RecipientClass string

const (
	CentralEditor    RecipientClass = "central-editor"
	TargetAgent      RecipientClass = "target-agent"
	OperatorRecovery RecipientClass = "operator-recovery"
)

type RecipientRecord struct {
	Class     RecipientClass `json:"class"`
	KeyRef    string         `json:"keyRef"`
	Recipient string         `json:"recipient"`
}

// Identity contains only an inventory reference, public recipient and protected
// private-key path. Private key bytes are never returned to callers.
type Identity struct {
	Class       RecipientClass `json:"class"`
	KeyRef      string         `json:"keyRef"`
	Recipient   string         `json:"recipient"`
	PrivatePath string         `json:"-"`
}

var (
	ErrInvalidRecipients = errors.New("invalid secret recipients")
	ErrInvalidDocument   = errors.New("invalid SOPS dotenv document")
	ErrDecryptFailed     = errors.New("secret decryption failed")
	ErrEncryptFailed     = errors.New("secret encryption failed")
	ErrOutputTooLarge    = errors.New("SOPS output exceeds the configured limit")
)

// EncryptDotenv encrypts a dotenv document for the supplied age recipients.
// SOPS binary storage is used so quoted physical newlines, comments and every
// other byte remain lossless and no comments or values are exposed in metadata.
// The content enters SOPS over stdin and is never written to a temporary file.
func EncryptDotenv(ctx context.Context, sopsBinary string, plaintext []byte, recipients []string) ([]byte, error) {
	if err := validateDocument(plaintext); err != nil {
		return nil, err
	}
	clean, err := validateRecipients(recipients)
	if err != nil {
		return nil, err
	}
	args := []string{"encrypt", "--input-type", "binary", "--output-type", "binary", "--filename-override", "vigara.env", "--age", strings.Join(clean, ",")}
	out, err := runSOPS(ctx, sopsBinary, "", plaintext, args...)
	if err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return nil, ErrOutputTooLarge
		}
		return nil, ErrEncryptFailed
	}
	if !isSOPSBinary(out) {
		return nil, ErrEncryptFailed
	}
	return out, nil
}

// EncryptForPolicy enforces the schema's editor + one-or-more target + recovery
// recipient classes before encrypting. References are opaque inventory IDs;
// this package never places private identity bytes in a manifest or DB.
func EncryptForPolicy(ctx context.Context, sopsBinary string, plaintext []byte, policy []RecipientRecord) ([]byte, error) {
	if err := ValidateRecipientPolicy(policy); err != nil {
		return nil, err
	}
	recipients := make([]string, 0, len(policy))
	for _, entry := range policy {
		recipients = append(recipients, entry.Recipient)
	}
	return EncryptDotenv(ctx, sopsBinary, plaintext, recipients)
}

// ValidateRecipientPolicy requires exactly one central editor and operator
// recovery identity, and at least one authorized target identity.
func ValidateRecipientPolicy(policy []RecipientRecord) error {
	refPattern := regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{1,127}$`)
	classes := map[RecipientClass]int{}
	refs, recipients := map[string]bool{}, map[string]bool{}
	for _, entry := range policy {
		if entry.Class != CentralEditor && entry.Class != TargetAgent && entry.Class != OperatorRecovery || !refPattern.MatchString(entry.KeyRef) || !validAgeRecipient(entry.Recipient) {
			return ErrInvalidRecipients
		}
		ref := string(entry.Class) + ":" + entry.KeyRef
		if refs[ref] || recipients[entry.Recipient] {
			return ErrInvalidRecipients
		}
		refs[ref], recipients[entry.Recipient] = true, true
		classes[entry.Class]++
	}
	if classes[CentralEditor] != 1 || classes[OperatorRecovery] != 1 || classes[TargetAgent] < 1 {
		return ErrInvalidRecipients
	}
	return nil
}

// ProvisionIdentity creates a distinct age identity in an already-protected
// operator-managed directory. It uses age-keygen, refuses an existing path,
// and returns only the public recipient and private path. The root must be a
// non-symlink directory owned/protected by the service operator.
func ProvisionIdentity(ctx context.Context, ageKeygen, identityRoot string, class RecipientClass, keyRef string) (Identity, error) {
	var empty Identity
	if class != CentralEditor && class != TargetAgent && class != OperatorRecovery || !regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{1,127}$`).MatchString(keyRef) {
		return empty, ErrInvalidRecipients
	}
	identityRoot, err := filepath.Abs(identityRoot)
	if err != nil {
		return empty, err
	}
	if err := validatePrivateDirectory(identityRoot); err != nil {
		return empty, err
	}
	path := filepath.Join(identityRoot, string(class)+"-"+keyRef+".txt")
	if _, err := os.Lstat(path); err == nil {
		return empty, errors.New("identity already exists")
	} else if !os.IsNotExist(err) {
		return empty, errors.New("identity path unavailable")
	}
	cmd := exec.CommandContext(ctx, ageKeygen, "-o", path)
	cmd.Dir = identityRoot
	cmd.Env = isolatedEnvironment(identityRoot, "")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		_ = os.Remove(path)
		return empty, errors.New("age identity generation failed")
	}
	if err := protectFile(path); err != nil {
		_ = os.Remove(path)
		return empty, err
	}
	recipient, err := recipientFromIdentityFile(path)
	if err != nil {
		_ = os.Remove(path)
		return empty, err
	}
	if !validAgeRecipient(recipient) {
		_ = os.Remove(path)
		return empty, errors.New("age identity invalid")
	}
	return Identity{Class: class, KeyRef: keyRef, Recipient: recipient, PrivatePath: path}, nil
}

// DecryptDotenv verifies the SOPS MAC and decrypts using exactly identityFile.
// All SOPS diagnostics are discarded and only a generic error is returned.
func DecryptDotenv(ctx context.Context, sopsBinary, identityFile string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext) > maxDocumentBytes {
		return nil, ErrInvalidDocument
	}
	args := []string{"decrypt", "--input-type", "binary", "--output-type", "binary"}
	if !isSOPSBinary(ciphertext) {
		if !isSOPSDotenv(ciphertext) {
			return nil, ErrInvalidDocument
		}
		args = []string{"decrypt", "--input-type", "dotenv", "--output-type", "dotenv"}
	}
	identityFile, err := filepath.Abs(identityFile)
	if err != nil || validateIdentityFile(identityFile) != nil {
		return nil, ErrDecryptFailed
	}
	out, err := runSOPS(ctx, sopsBinary, identityFile, ciphertext, args...)
	if err != nil || validateDocument(out) != nil {
		return nil, ErrDecryptFailed
	}
	return out, nil
}

// RotateDotenv re-encrypts an existing file for the new recipients. Rotation
// cannot revoke old copies of ciphertext, plaintext, or Git history.
func RotateDotenv(ctx context.Context, sopsBinary, identityFile string, ciphertext []byte, recipients []string) ([]byte, error) {
	plain, err := DecryptDotenv(ctx, sopsBinary, identityFile, ciphertext)
	if err != nil {
		return nil, err
	}
	return EncryptDotenv(ctx, sopsBinary, plain, recipients)
}

// RotateForPolicy is the role-checked rotation entry point.
func RotateForPolicy(ctx context.Context, sopsBinary, identityFile string, ciphertext []byte, policy []RecipientRecord) ([]byte, error) {
	if err := ValidateRecipientPolicy(policy); err != nil {
		return nil, err
	}
	recipients := make([]string, 0, len(policy))
	for _, entry := range policy {
		recipients = append(recipients, entry.Recipient)
	}
	return RotateDotenv(ctx, sopsBinary, identityFile, ciphertext, recipients)
}

func validateRecipients(recipients []string) ([]string, error) {
	if len(recipients) < 3 || len(recipients) > 128 {
		return nil, ErrInvalidRecipients
	}
	seen := make(map[string]struct{}, len(recipients))
	clean := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		r := strings.TrimSpace(recipient)
		if !validAgeRecipient(r) {
			return nil, ErrInvalidRecipients
		}
		if _, ok := seen[r]; ok {
			return nil, ErrInvalidRecipients
		}
		seen[r] = struct{}{}
		clean = append(clean, r)
	}
	if len(clean) < 3 {
		return nil, ErrInvalidRecipients
	}
	return clean, nil
}

func validateDocument(data []byte) error {
	if len(data) > maxDocumentBytes || bytes.IndexByte(data, 0) >= 0 {
		return ErrInvalidDocument
	}
	return nil
}

func isSOPSBinary(doc []byte) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(doc, &envelope) != nil || len(envelope) != 2 {
		return false
	}
	var data string
	if json.Unmarshal(envelope["data"], &data) != nil || !strings.HasPrefix(data, "ENC[AES256_GCM,") {
		return false
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(envelope["sops"], &metadata) != nil || len(metadata["age"]) == 0 || len(metadata["mac"]) == 0 || len(metadata["version"]) == 0 {
		return false
	}
	for key := range metadata {
		switch key {
		case "age", "lastmodified", "mac", "version":
		default:
			return false
		}
	}
	var ageEntries []struct {
		Recipient string `json:"recipient"`
		Enc       string `json:"enc"`
	}
	if json.Unmarshal(metadata["age"], &ageEntries) != nil || len(ageEntries) == 0 {
		return false
	}
	for _, entry := range ageEntries {
		if !validAgeRecipient(entry.Recipient) || entry.Enc == "" {
			return false
		}
	}
	return true
}

// IsEncryptedBinaryArtifact checks the envelope shape and age-only metadata.
// It does not authenticate the MAC; DecryptDotenv verifies that with an identity.
func IsEncryptedBinaryArtifact(doc []byte) bool { return isSOPSBinary(doc) }

func isSOPSDotenv(doc []byte) bool {
	// Legacy dotenv files flatten SOPS metadata into dotenv keys. Strictly allow
	// only the SOPS age-only fields; unknown keys can enable cloud key services.
	known := map[string]bool{"sops_version": false, "sops_lastmodified": false, "sops_mac": false,
		"sops_unencrypted_suffix": true, "sops_encrypted_suffix": true, "sops_unencrypted_regex": true,
		"sops_encrypted_regex": true, "sops_unencrypted_comment_regex": true, "sops_encrypted_comment_regex": true,
		"sops_shamir_threshold": true}
	ageField := regexp.MustCompile(`^sops_age__list_[0-9]+__map_(recipient|enc)$`)
	ageRecipients, ageCiphertexts := 0, 0
	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(line, "sops_") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false
		}
		if field := ageField.FindStringSubmatch(key); field != nil {
			if field[1] == "recipient" {
				if !validAgeRecipient(strings.TrimSpace(value)) {
					return false
				}
				ageRecipients++
			} else {
				if !strings.Contains(value, "BEGIN AGE ENCRYPTED FILE") {
					return false
				}
				ageCiphertexts++
			}
			continue
		}
		if _, ok := known[key]; !ok {
			return false
		}
		switch key {
		case "sops_version":
			known[key] = value != ""
		case "sops_lastmodified":
			known[key] = value != ""
		case "sops_mac":
			known[key] = strings.HasPrefix(value, "ENC[AES256_GCM,")
		}
	}
	for _, key := range []string{"sops_version", "sops_lastmodified", "sops_mac"} {
		if !known[key] {
			return false
		}
	}
	return ageRecipients > 0 && ageRecipients == ageCiphertexts
}

func validAgeRecipient(value string) bool {
	if !strings.HasPrefix(value, "age1") || strings.ContainsAny(value, ",\r\n\x00 \t") {
		return false
	}
	_, err := age.ParseX25519Recipient(value)
	return err == nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, ErrOutputTooLarge
	}
	return b.Buffer.Write(p)
}

func runSOPS(ctx context.Context, binary, identityFile string, input []byte, args ...string) ([]byte, error) {
	if binary == "" {
		return nil, ErrEncryptFailed
	}
	work, err := os.MkdirTemp("", "vigara-sops-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if err := protectDirectory(work); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = work // no repository .sops.yaml can influence recipient policy
	cmd.Stdin = bytes.NewReader(input)
	stdout := &boundedBuffer{limit: maxDocumentBytes}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	cmd.Env = isolatedEnvironment(work, identityFile)
	if err := cmd.Run(); err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return nil, ErrOutputTooLarge
		}
		return nil, errors.New("SOPS operation failed")
	}
	return stdout.Bytes(), nil
}

func isolatedEnvironment(work, identityFile string) []string {
	// Do not pass inherited SOPS_AGE_KEY, cloud KMS credentials, configuration
	// paths, HOME or any other ambient secret to the SOPS process.
	env := []string{"HOME=" + work, "XDG_CONFIG_HOME=" + filepath.Join(work, "config"), "TMPDIR=" + work, "TMP=" + work, "TEMP=" + work, "SOPS_DISABLE_VERSION_CHECK=true"}
	if identityFile != "" {
		env = append(env, "SOPS_AGE_KEY_FILE="+identityFile)
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "PATH"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return env
}

// Materialized is a private plaintext dotenv file for one deployment. Keep it
// alive until Compose exits and any containers/bind mounts no longer need it.
type Materialized struct {
	Path string
	dir  string
}

// Materialize writes plaintext with owner-only permissions. The caller owns
// its lifetime and must call Cleanup after dependent Compose processes end.
func Materialize(parent string, plaintext []byte) (*Materialized, error) {
	if err := validateDocument(plaintext); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, "vigara-secrets-")
	if err != nil {
		return nil, err
	}
	if err := protectDirectory(dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	path := filepath.Join(dir, "runtime.env")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	_, writeErr := f.Write(plaintext)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.Join(writeErr, closeErr)
	}
	if err := protectFile(path); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Materialized{Path: path, dir: dir}, nil
}

// Cleanup deletes a materialized secret after the caller confirms it is no
// longer required by Compose or a mounted container file.
func (m *Materialized) Cleanup() error {
	if m == nil || m.dir == "" {
		return nil
	}
	if err := os.RemoveAll(m.dir); err != nil {
		return err
	}
	m.dir, m.Path = "", ""
	return nil
}
