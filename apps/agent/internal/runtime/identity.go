package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func stableMachineID(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", "{", "", "}", "").Replace(value)
	if len(value) != 32 {
		return "", false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || allHex(decoded, 0) || allHex(decoded, 0xff) {
		return "", false
	}
	digest := sha256.Sum256([]byte("machine-id\x00" + value))
	return hex.EncodeToString(digest[:]), true
}

func allHex(value []byte, want byte) bool {
	if len(value) == 0 {
		return false
	}
	for _, b := range value {
		if b != want {
			return false
		}
	}
	return true
}
