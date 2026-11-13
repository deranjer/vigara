package notifytransport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// vapidTokenLifetime is how long a signed token is valid. RFC 8292 allows up
// to 24 hours; tokens are reused per push server until an hour before expiry.
const vapidTokenLifetime = 12 * time.Hour

// VAPID identifies the Vigara server to push servers (RFC 8292). A phone that
// registered with the public key can only be pushed to with tokens signed by
// the matching private key.
type VAPID struct {
	key     *ecdsa.PrivateKey
	public  string
	subject string

	mu     sync.Mutex
	tokens map[string]vapidToken
	now    func() time.Time
}

type vapidToken struct {
	header  string
	expires time.Time
}

// GenerateVAPIDKey returns a new P-256 private key as its 32-byte scalar.
func GenerateVAPIDKey() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return key.Bytes()
}

// NewVAPID loads a 32-byte P-256 private key. subject is the contact sent to
// push servers: an https: URL or a mailto: address.
func NewVAPID(private []byte, subject string) (*VAPID, error) {
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), private)
	if err != nil {
		return nil, errors.New("invalid VAPID private key")
	}
	if !ValidVAPIDSubject(subject) {
		return nil, errors.New("VAPID subject must be an https: URL or a mailto: address")
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	return &VAPID{key: key, public: base64.RawURLEncoding.EncodeToString(pub), subject: subject, tokens: map[string]vapidToken{}, now: time.Now}, nil
}

// ValidVAPIDSubject accepts the two contact forms RFC 8292 allows.
func ValidVAPIDSubject(s string) bool {
	if len(s) > 256 || s != strings.TrimSpace(s) {
		return false
	}
	if rest, ok := strings.CutPrefix(s, "mailto:"); ok {
		return strings.Contains(rest, "@") && !strings.ContainsAny(rest, " \t\r\n<>")
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// PublicKey is the uncompressed public key, base64url without padding: the
// form the UnifiedPush connector takes as its VAPID key.
func (v *VAPID) PublicKey() string { return v.public }

// Authorization returns the header value for a push to endpoint.
func (v *VAPID) Authorization(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid push endpoint")
	}
	audience := u.Scheme + "://" + u.Host
	now := v.now()
	v.mu.Lock()
	defer v.mu.Unlock()
	if t, ok := v.tokens[audience]; ok && now.Add(time.Hour).Before(t.expires) {
		return t.header, nil
	}
	expires := now.Add(vapidTokenLifetime)
	token, err := v.sign(audience, expires)
	if err != nil {
		return "", err
	}
	header := "vapid t=" + token + ", k=" + v.public
	if len(v.tokens) > 64 {
		v.tokens = map[string]vapidToken{}
	}
	v.tokens[audience] = vapidToken{header: header, expires: expires}
	return header, nil
}

// sign builds an ES256 JWT with the audience, expiry and contact claims.
func (v *VAPID) sign(audience string, expires time.Time) (string, error) {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{"aud": audience, "exp": expires.Unix(), "sub": v.subject})
	if err != nil {
		return "", err
	}
	unsigned := head + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 signatures are r and s as fixed 32-byte big-endian values.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
