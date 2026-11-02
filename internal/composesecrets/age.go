package composesecrets

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

const AgeFormat = "age-dotenv"

func IsAgeArtifact(data []byte) bool { return bytes.HasPrefix(data, []byte(armor.Header+"\n")) }

// EnsureAgeIdentity provisions an exclusive, private key file. Existing keys are
// reused; symlink ancestors and world/group-readable key files are rejected.
func EnsureAgeIdentity(file string) (string, error) {
	if !filepath.IsAbs(file) {
		return "", ErrInvalidRecipients
	}
	for p := file; ; p = filepath.Dir(p) {
		if info, err := os.Lstat(p); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", ErrInvalidRecipients
			}
		} else if !os.IsNotExist(err) {
			return "", ErrInvalidRecipients
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return "", ErrInvalidRecipients
	}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		identity, err := age.GenerateX25519Identity()
		if err != nil {
			return "", ErrInvalidRecipients
		}
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.WriteString(identity.String() + "\n")
			syncErr := f.Sync()
			closeErr := f.Close()
			if err != nil || syncErr != nil || closeErr != nil {
				return "", ErrInvalidRecipients
			}
		} else if !os.IsExist(err) {
			return "", ErrInvalidRecipients
		}
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", ErrInvalidRecipients
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", ErrInvalidRecipients
	}
	identities, err := age.ParseIdentities(strings.NewReader(string(data)))
	if err != nil || len(identities) != 1 {
		return "", ErrInvalidRecipients
	}
	identity, ok := identities[0].(*age.X25519Identity)
	if !ok {
		return "", ErrInvalidRecipients
	}
	return identity.Recipient().String(), nil
}

// EncryptAgeDotenv uses age's native interoperable format, avoiding an external
// encryption executable on hosts imported through the browser.
func EncryptAgeDotenv(ctx context.Context, plaintext []byte, recipients []string) ([]byte, error) {
	if ctx.Err() != nil || validateDocument(plaintext) != nil {
		return nil, ErrEncryptFailed
	}
	clean, err := validateRecipients(recipients)
	if err != nil {
		return nil, err
	}
	keys := make([]age.Recipient, 0, len(clean))
	for _, value := range clean {
		key, err := age.ParseX25519Recipient(value)
		if err != nil {
			return nil, ErrInvalidRecipients
		}
		keys = append(keys, key)
	}
	var buffer bytes.Buffer
	armored := armor.NewWriter(&buffer)
	w, err := age.Encrypt(armored, keys...)
	if err != nil {
		return nil, ErrEncryptFailed
	}
	if _, err = w.Write(plaintext); err != nil {
		return nil, ErrEncryptFailed
	}
	if err = w.Close(); err != nil {
		return nil, ErrEncryptFailed
	}
	if err = armored.Close(); err != nil {
		return nil, ErrEncryptFailed
	}
	return buffer.Bytes(), nil
}

func decryptAgeDotenv(ctx context.Context, file string, ciphertext []byte) ([]byte, error) {
	if ctx.Err() != nil || len(ciphertext) > maxDocumentBytes {
		return nil, ErrDecryptFailed
	}
	data, err := os.ReadFile(file)
	if err != nil || len(data) > 8192 {
		return nil, ErrDecryptFailed
	}
	identities, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, ErrDecryptFailed
	}
	r, err := age.Decrypt(armor.NewReader(bytes.NewReader(ciphertext)), identities...)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	plain, err := io.ReadAll(io.LimitReader(r, maxDocumentBytes+1))
	if err != nil || validateDocument(plain) != nil {
		return nil, ErrDecryptFailed
	}
	return plain, nil
}

func EncryptForFormat(ctx context.Context, sopsBinary, format string, plaintext []byte, policy []RecipientRecord) ([]byte, error) {
	if format != AgeFormat {
		return EncryptForPolicy(ctx, sopsBinary, plaintext, policy)
	}
	if err := ValidateRecipientPolicy(policy); err != nil {
		return nil, err
	}
	keys := []string{}
	for _, entry := range policy {
		keys = append(keys, entry.Recipient)
	}
	return EncryptAgeDotenv(ctx, plaintext, keys)
}
