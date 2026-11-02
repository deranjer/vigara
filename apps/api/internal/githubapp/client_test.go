package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Client, Credentials, *rsa.PrivateKey) {
	t.Helper()
	c, err := New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v := Credentials{ID: 42, ClientID: "client-id", Slug: "vigara-test", InstallationID: 123, PEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
	return c, v, key
}

func TestCredentialEncryption(t *testing.T) {
	c, v, _ := fixture(t)
	one, err := c.Seal(v)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := c.Seal(v)
	if string(one) == string(two) || strings.Contains(string(one), "PRIVATE KEY") {
		t.Fatal("plaintext or nonce reuse")
	}
	got, err := c.Open(one)
	if err != nil || got.PEM != v.PEM {
		t.Fatal("round trip", err)
	}
	one[len(one)-1] ^= 1
	if _, err = c.Open(one); err == nil {
		t.Fatal("tampering accepted")
	}
	wrong, _ := New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	if _, err = wrong.Open(two); err == nil {
		t.Fatal("wrong master accepted")
	}
	disabled, _ := New("")
	if _, err = disabled.Seal(v); err == nil {
		t.Fatal("missing master accepted")
	}
	if _, err = New("bad"); err == nil {
		t.Fatal("invalid master accepted")
	}
}

func TestRepositoryScopeValidation(t *testing.T) {
	for _, remote := range []string{"https://github.com/owner/repo.git", "https://github.com/owner/repo"} {
		owner, name, err := Repository(remote)
		if err != nil || owner != "owner" || name != "repo" {
			t.Fatal(remote, err)
		}
	}
	for _, remote := range []string{"http://github.com/owner/repo", "https://github.com.evil/owner/repo", "https://github.com:443/owner/repo", "https://token@github.com/owner/repo", "https://github.com/owner/repo?token=bad", "https://github.com/owner/repo#bad", "https://github.com/owner/%2erepo", "https://github.com/owner/../repo", "https://github.com/owner/..", "https://github.com/owner/repo/", "git@github.com:owner/repo.git"} {
		if _, _, err := Repository(remote); err == nil {
			t.Fatal("unsafe remote accepted", remote)
		}
	}
}

func TestJWTSignatureAndLifetime(t *testing.T) {
	_, v, key := fixture(t)
	encoded, err := jwt(v)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(encoded, ".")
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
	data, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(data, &claims) != nil || claims.Iss != v.ClientID || claims.Iat > time.Now().Unix()-50 || claims.Exp > time.Now().Add(10*time.Minute).Unix() {
		t.Fatal("invalid claims", string(data))
	}
}

func TestTokenScopeCacheRenewalAndNoRedirects(t *testing.T) {
	c, v, _ := fixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Error("missing app JWT")
		}
		switch r.URL.Path {
		case "/repos/owner/repo/installation":
			_, _ = w.Write([]byte(`{"id":123,"app_id":42,"permissions":{"contents":"write"},"suspended_at":null}`))
		case "/app/installations/123/access_tokens":
			calls.Add(1)
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Repositories) != 1 || body.Repositories[0] != "repo" || len(body.Permissions) != 1 || body.Permissions["contents"] != "write" {
				t.Error("token wasn't limited to one repo and contents write")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_secret", "expires_at": time.Now().Add(time.Hour)})
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c.base = server.URL
	sealed, _ := c.Seal(v)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env, err := c.GitEnvironment(context.Background(), sealed, "https://github.com/owner/repo.git")
			if err != nil {
				t.Error(err)
				return
			}
			joined := strings.Join(env, "\n")
			if !strings.Contains(joined, "http.https://github.com/owner/repo.git.extraHeader") || !strings.Contains(joined, "GIT_CONFIG_VALUE_1=false") || strings.Contains(joined, "ghs_secret") {
				t.Error("unsafe environment")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent cache misses", calls.Load())
	}
	for key, token := range c.tokens {
		token.expires = time.Now().Add(time.Minute)
		c.tokens[key] = token
	}
	if _, err := c.GitEnvironment(context.Background(), sealed, "https://github.com/owner/repo.git"); err != nil || calls.Load() != 2 {
		t.Fatal("token not renewed", err)
	}
	if _, err := c.GitEnvironment(context.Background(), sealed, "https://evil.example/owner/repo"); err == nil {
		t.Fatal("foreign host accepted")
	}
	v.InstallationID = 456
	invalid, _ := c.Seal(v)
	if _, err := c.GitEnvironment(context.Background(), invalid, "https://github.com/owner/repo.git"); err == nil {
		t.Fatal("spoofed installation accepted")
	}
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	c.base = redirect.URL
	if err := c.ValidateInstallation(context.Background(), v, "https://github.com/owner/repo.git"); err == nil || leaked.Load() {
		t.Fatal("followed API redirect")
	}
}

func TestGitHubErrorsDoNotExposeResponseSecrets(t *testing.T) {
	c, _, _ := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"secret-private-key"}`))
	}))
	defer server.Close()
	c.base = server.URL
	_, err := c.Convert(context.Background(), "manifest-code")
	if err == nil || strings.Contains(err.Error(), "secret-private-key") {
		t.Fatal("unsafe error", err)
	}
}
