package notifytransport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const (
	PushTargetID = "android"
	// A Web Push record must fit in 4096 bytes including the 16-byte tag and
	// the delimiter; payloads are kept well below that.
	maxPushPayload = 3000
	pushRecordSize = 4096
	pushTTLSeconds = "86400"
)

// PushKeys are a phone's Web Push keys, base64url encoded without padding as
// the UnifiedPush connector reports them.
type PushKeys struct{ P256DH, Auth string }

// pushPayload is what the Vigara app decodes and shows. It is encrypted to the
// phone, so the push distributor only relays ciphertext.
type pushPayload struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Incident string `json:"incident,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ValidatePushEndpoint accepts the HTTP(S) URL a distributor hands out.
func ValidatePushEndpoint(raw string) error {
	if raw == "" || len(raw) > 2048 || raw != strings.TrimSpace(raw) {
		return errors.New("push endpoint must be an HTTP or HTTPS URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("push endpoint must be an HTTP or HTTPS URL without credentials")
	}
	return nil
}

// ValidatePushKeys checks the phone's P-256 public key and 16-byte auth secret.
func ValidatePushKeys(keys PushKeys) error {
	pub, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(keys.P256DH, "="))
	if err != nil || len(pub) != 65 {
		return errors.New("push key must be an uncompressed P-256 public key")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return errors.New("push key must be an uncompressed P-256 public key")
	}
	auth, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(keys.Auth, "="))
	if err != nil || len(auth) != 16 {
		return errors.New("push auth secret must be 16 bytes")
	}
	return nil
}

// IsGone reports that the distributor no longer knows the endpoint, so the
// registration should be forgotten.
func IsGone(err error) bool {
	var d *DeliveryError
	return errors.As(err, &d) && d.reason == "gone"
}

// RenderPush builds the app payload for one alert.
func RenderPush(message Message) []byte {
	rule := cleanField(message.RuleName, 120)
	summary := cleanField(message.Summary, 1600)
	device := cleanField(message.DeviceName, 120)
	p := pushPayload{Kind: message.Transition, Severity: message.Severity, Title: rule, Incident: message.IncidentID}
	switch message.Transition {
	case "firing":
	case "test":
		p.Title = "Vigara test notification"
	default:
		p.Kind = "resolved"
		p.Title = rule + " cleared"
	}
	if strings.TrimSpace(p.Title) == "" || p.Title == " cleared" {
		p.Title = "Vigara alert"
	}
	p.Body = summary
	if device != "" && device != "Device" && !strings.Contains(summary, device) {
		p.Body = device + ": " + summary
	}
	p.Body = truncateUTF8(fallback(p.Body), 1800)
	if safeLink(message.OpenURL) {
		p.URL = message.OpenURL
	}
	body, _ := json.Marshal(p)
	return body
}

// SendPush encrypts payload to the phone (RFC 8291, aes128gcm) and posts it to
// its push endpoint (RFC 8030).
func SendPush(ctx context.Context, client *http.Client, endpoint string, keys PushKeys, payload []byte, urgent bool) error {
	if len(payload) > maxPushPayload {
		return &DeliveryError{target: PushTargetID, reason: "payload_too_large"}
	}
	if ValidatePushEndpoint(endpoint) != nil || ValidatePushKeys(keys) != nil {
		return &DeliveryError{target: PushTargetID, reason: "invalid_registration"}
	}
	body, err := encryptPush(payload, keys)
	if err != nil {
		return &DeliveryError{target: PushTargetID, reason: "encryption_failed"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &DeliveryError{target: PushTargetID, reason: "request_failed", temporary: true}
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Content-Encoding", "aes128gcm")
	request.Header.Set("TTL", pushTTLSeconds)
	if urgent {
		request.Header.Set("Urgency", "high")
	} else {
		request.Header.Set("Urgency", "normal")
	}
	if client == nil {
		client = newHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return &DeliveryError{target: PushTargetID, reason: "request_failed", temporary: true}
	}
	_, _, _ = consumeResponse(response)
	switch code := response.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusNotFound || code == http.StatusGone:
		return &DeliveryError{target: PushTargetID, reason: "gone"}
	case code == http.StatusTooManyRequests:
		return &DeliveryError{target: PushTargetID, reason: "rate_limited", temporary: true, retryAfter: retryAfter(response.Header, nil)}
	case code >= 500:
		return &DeliveryError{target: PushTargetID, reason: "server_error", temporary: true}
	default:
		return &DeliveryError{target: PushTargetID, reason: "rejected"}
	}
}

func encryptPush(plaintext []byte, keys PushKeys) ([]byte, error) {
	uaRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(keys.P256DH, "="))
	if err != nil {
		return nil, err
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(keys.Auth, "="))
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encryptPushWith(plaintext, uaRaw, authSecret, private, salt)
}

// encryptPushWith is the deterministic core, split out so tests can check it
// against the RFC 8291 example.
func encryptPushWith(plaintext, uaRaw, authSecret []byte, private *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, err
	}
	shared, err := private.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := private.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), uaRaw...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record: the plaintext, then the 0x02 last-record delimiter.
	sealed := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 2), nil)
	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, pushRecordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return append(header, sealed...), nil
}
