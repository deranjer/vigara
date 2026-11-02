// Package githubapp implements server-to-server GitHub App authentication.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("GitHub App credentials are unavailable; check the server encryption key and reconnect the App")
var ErrAccess = errors.New("GitHub App access failed; check the installation, selected repository, Contents write permission and server connectivity")
var component = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type Credentials struct {
	ID             int64  `json:"id"`
	ClientID       string `json:"client_id"`
	Slug           string `json:"slug"`
	PEM            string `json:"pem"`
	InstallationID int64  `json:"installation_id"`
	SetupState     string `json:"setup_state,omitempty"`
}

type cachedToken struct {
	value   string
	expires time.Time
}
type Client struct {
	aead   cipher.AEAD
	http   *http.Client
	base   string
	mu     sync.Mutex
	tokens map[string]cachedToken
}

// New keeps the master key outside the database. An empty key disables setup.
func New(master string) (*Client, error) {
	c := &Client{http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, base: "https://api.github.com", tokens: map[string]cachedToken{}}
	if master == "" {
		return c, nil
	}
	key, err := base64.StdEncoding.DecodeString(master)
	if err != nil || len(key) != 32 {
		return nil, errors.New("VIGIRA_GITHUB_APP_MASTER_KEY must encode exactly 32 bytes in base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	c.aead, err = cipher.NewGCM(block)
	return c, err
}

func (c *Client) Available() bool { return c != nil && c.aead != nil }
func (c *Client) Seal(v Credentials) ([]byte, error) {
	if !c.Available() {
		return nil, ErrUnavailable
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, ErrUnavailable
	}
	return c.aead.Seal(nonce, nonce, data, []byte("vigara-github-app-v1")), nil
}
func (c *Client) Open(data []byte) (Credentials, error) {
	var v Credentials
	if !c.Available() || len(data) < c.aead.NonceSize() {
		return v, ErrUnavailable
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, data[:n], data[n:], []byte("vigara-github-app-v1"))
	if err != nil || json.Unmarshal(plain, &v) != nil {
		return Credentials{}, ErrUnavailable
	}
	return v, nil
}

// Repository accepts only canonical GitHub HTTPS remotes. Credentials are never
// sent to another host, a redirect, or an operator-supplied API URL.
func Repository(remote string) (owner, name string, err error) {
	if len(remote) > 2048 {
		return "", "", ErrAccess
	}
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", "", ErrAccess
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", ErrAccess
	}
	owner, name = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !component.MatchString(owner) || !component.MatchString(name) || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", "", ErrAccess
	}
	return owner, name, nil
}

func privateKey(v Credentials) (*rsa.PrivateKey, error) {
	b, rest := pem.Decode([]byte(v.PEM))
	if b == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrUnavailable
	}
	if key, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, ErrUnavailable
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, ErrUnavailable
	}
	return rsaKey, nil
}

func jwt(v Credentials) (string, error) {
	key, err := privateKey(v)
	if err != nil {
		return "", err
	}
	issuer := v.ClientID
	if issuer == "" {
		issuer = strconv.FormatInt(v.ID, 10)
	}
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(8 * time.Minute).Unix(), "iss": issuer})
	data := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", ErrUnavailable
	}
	return data + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *Client) request(ctx context.Context, method, path, bearer string, body, result any) error {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return ErrAccess
		}
		input = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, input)
	if err != nil {
		return ErrAccess
	}
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	r.Header.Set("User-Agent", "Vigara-GitHub-App")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return ErrAccess
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrAccess
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result) != nil {
		return ErrAccess
	}
	return nil
}

func (c *Client) Convert(ctx context.Context, code string) (Credentials, error) {
	var v Credentials
	if !component.MatchString(code) || len(code) > 200 {
		return v, ErrAccess
	}
	if err := c.request(ctx, "POST", "/app-manifests/"+code+"/conversions", "", nil, &v); err != nil {
		return v, err
	}
	if v.ID <= 0 || !component.MatchString(v.Slug) {
		return Credentials{}, ErrAccess
	}
	if _, err := privateKey(v); err != nil {
		return Credentials{}, err
	}
	return v, nil
}

// ValidateInstallation resolves the expected repository using the App's JWT;
// the browser's installation_id alone is never trusted.
func (c *Client) ValidateInstallation(ctx context.Context, v Credentials, remote string) error {
	owner, name, err := Repository(remote)
	if err != nil {
		return err
	}
	bearer, err := jwt(v)
	if err != nil {
		return err
	}
	var install struct {
		ID          int64             `json:"id"`
		AppID       int64             `json:"app_id"`
		SuspendedAt *time.Time        `json:"suspended_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := c.request(ctx, "GET", "/repos/"+owner+"/"+name+"/installation", bearer, nil, &install); err != nil {
		return err
	}
	if install.ID != v.InstallationID || install.AppID != v.ID || install.SuspendedAt != nil || install.Permissions["contents"] != "write" {
		return ErrAccess
	}
	return nil
}

func (c *Client) GitEnvironment(ctx context.Context, sealed []byte, remote string) ([]string, error) {
	v, err := c.Open(sealed)
	if err != nil {
		return nil, err
	}
	owner, name, err := Repository(remote)
	if err != nil {
		return nil, err
	}
	// Serialize renewal so concurrent operations don't mint redundant tokens.
	c.mu.Lock()
	defer c.mu.Unlock()
	fingerprint := sha256.Sum256(sealed)
	cacheKey := fmt.Sprintf("%x/%s/%s", fingerprint, owner, name)
	token := c.tokens[cacheKey]
	if time.Until(token.expires) < 5*time.Minute {
		if err := c.ValidateInstallation(ctx, v, remote); err != nil {
			return nil, err
		}
		bearer, err := jwt(v)
		if err != nil {
			return nil, err
		}
		var response struct {
			Token   string    `json:"token"`
			Expires time.Time `json:"expires_at"`
		}
		body := map[string]any{"repositories": []string{name}, "permissions": map[string]string{"contents": "write"}}
		if err := c.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", v.InstallationID), bearer, body, &response); err != nil {
			return nil, err
		}
		if response.Token == "" || time.Until(response.Expires) < time.Minute || strings.ContainsAny(response.Token, "\r\n") {
			return nil, ErrAccess
		}
		token = cachedToken{response.Token, response.Expires}
		for key, old := range c.tokens {
			if time.Until(old.expires) < 5*time.Minute {
				delete(c.tokens, key)
			}
		}
		c.tokens[cacheKey] = token
	}
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token.value))
	// Environment-only Git config: no secret in process arguments, remotes or
	// repository config. Disable redirects and inherited credential helpers.
	return []string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=http." + remote + ".extraHeader", "GIT_CONFIG_VALUE_0=" + header, "GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=credential.helper", "GIT_CONFIG_VALUE_2="}, nil
}
