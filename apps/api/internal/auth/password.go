package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"
)

const (
	passwordIterations = 600_000
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyBytes)
	if err != nil {
		return "", err
	}
	encode := base64.RawStdEncoding.EncodeToString
	return "pbkdf2-sha256$" + strconv.Itoa(passwordIterations) + "$" + encode(salt) + "$" + encode(key), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations != passwordIterations {
		return false
	}
	decode := base64.RawStdEncoding.DecodeString
	salt, err := decode(parts[2])
	if err != nil || len(salt) != passwordSaltBytes {
		return false
	}
	expected, err := decode(parts[3])
	if err != nil || len(expected) != passwordKeyBytes {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, passwordKeyBytes)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}

// dummyPasswordHash ensures an unknown username still incurs one derivation.
const dummyPasswordHash = "pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
