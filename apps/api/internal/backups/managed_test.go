package backups

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"reflect"
	"testing"
)

func managedFixture() ManagedInput {
	return ManagedInput{Repository: "/backup/host", SourcePaths: []string{"/home/user/Documents"}, Initialize: true, Enabled: true, Schedule: Schedule{Weekdays: []string{"Monday"}, Time: "02:00", Timezone: "UTC"}}
}

func TestManagedSecretsRequireKeyAndAuthenticateCiphertext(t *testing.T) {
	v := managedFixture()
	disabled, err := NewManager(nil, "", "")
	if err != nil || disabled.Available() {
		t.Fatal("unset key must disable management")
	}
	if _, err = disabled.Prepare(v); err == nil {
		t.Fatal("provisioned without encryption key")
	}
	if _, err = NewManager(nil, "short", ""); err == nil {
		t.Fatal("accepted invalid key")
	}
	m, err := NewManager(nil, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := m.Prepare(v)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := m.Open(sealed)
	if err != nil || len(opened.Password) < 32 {
		t.Fatal("missing generated recovery secret", err)
	}
	if bytes.Contains(sealed, []byte(opened.Password)) {
		t.Fatal("password stored as plaintext")
	}
	again, err := m.Seal(opened)
	if err != nil || bytes.Equal(sealed, again) {
		t.Fatal("encryption nonce reused")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = m.Open(sealed); err == nil {
		t.Fatal("accepted modified ciphertext")
	}
	wrong, _ := NewManager(nil, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), "")
	if _, err = wrong.Open(again); err == nil {
		t.Fatal("accepted incorrect master key")
	}
}

func TestManagedValidationAndExistingRepositoryPassword(t *testing.T) {
	m, _ := NewManager(nil, base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	v := managedFixture()
	v.ServerRepository = filepath.Join(t.TempDir(), "backup")
	if err := ValidateManaged(v); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"relative", "s3:bucket", "rest:https://example.com", "C:relative"} {
		bad := v
		bad.Repository = repo
		if ValidateManaged(bad) == nil {
			t.Fatalf("accepted %q", repo)
		}
	}
	v.Initialize = false
	if _, err := m.Prepare(v); err == nil {
		t.Fatal("existing repository accepted without its password")
	}
	v.Password = "existing-secret"
	sealed, err := m.Prepare(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Open(sealed)
	if err != nil || !reflect.DeepEqual(v, got) {
		t.Fatal("existing credentials changed", err)
	}
}
