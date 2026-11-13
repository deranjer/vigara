package notifytransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVAPIDTokenVerifiesAndIsReused(t *testing.T) {
	raw, err := GenerateVAPIDKey()
	if err != nil || len(raw) != 32 {
		t.Fatalf("key %d %v", len(raw), err)
	}
	v, err := NewVAPID(raw, "mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	v.now = func() time.Time { return now }
	header, err := v.Authorization("https://push.example.com/up/abc?x=1")
	if err != nil {
		t.Fatal(err)
	}
	token, key, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(header, "vapid t=") || key != v.PublicKey() {
		t.Fatalf("header %q", header)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	pubBytes, _ := base64.RawURLEncoding.DecodeString(key)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubBytes)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	var head map[string]string
	var claims map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(hb, &head) != nil || head["alg"] != "ES256" || json.Unmarshal(cb, &claims) != nil {
		t.Fatalf("head %s claims %s", hb, cb)
	}
	if claims["aud"] != "https://push.example.com" || claims["sub"] != "mailto:ops@example.com" || claims["exp"].(float64) != float64(now.Add(12*time.Hour).Unix()) {
		t.Fatalf("claims %v", claims)
	}
	// Same push server, shortly after: the cached token is reused.
	now = now.Add(time.Hour)
	again, _ := v.Authorization("https://push.example.com/up/other")
	if again != header {
		t.Fatal("token not reused for the same audience")
	}
	// Close to expiry a new one is signed.
	now = now.Add(10 * time.Hour)
	if fresh, _ := v.Authorization("https://push.example.com/up/abc"); fresh == header {
		t.Fatal("token reused near expiry")
	}
	// The key round-trips: a restart signs with the same public key.
	same, _ := NewVAPID(raw, "https://vigara.example.com")
	if same.PublicKey() != v.PublicKey() {
		t.Fatal("public key changed after reload")
	}
}

func TestVAPIDValidation(t *testing.T) {
	raw, _ := GenerateVAPIDKey()
	for _, s := range []string{"", "http://vigara.lan", "mailto:nobody", "vigara", " mailto:a@b"} {
		if _, err := NewVAPID(raw, s); err == nil {
			t.Fatalf("subject %q accepted", s)
		}
	}
	if _, err := NewVAPID(make([]byte, 32), "mailto:a@b.c"); err == nil {
		t.Fatal("zero key accepted")
	}
	if _, err := NewVAPID(raw[:31], "mailto:a@b.c"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestSendPushCarriesVAPID(t *testing.T) {
	keys := testKeys(t)
	raw, _ := GenerateVAPIDKey()
	v, _ := NewVAPID(raw, "mailto:a@b.c")
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(201)
	}))
	defer server.Close()
	if err := SendPush(context.Background(), server.Client(), v, server.URL+"/up/abc", keys, []byte(`{}`), false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "vapid t=") || !strings.HasSuffix(got, ", k="+v.PublicKey()) {
		t.Fatalf("authorization %q", got)
	}
}
