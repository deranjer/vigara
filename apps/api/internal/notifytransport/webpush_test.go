package notifytransport

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The worked example in RFC 8291 section 5.
func TestEncryptPushMatchesRFC8291Example(t *testing.T) {
	private, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encryptPushWith([]byte("When I grow up, I want to be a watermelon"),
		b64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		b64(t, "BTBZMqHH6r4Tts7J_aSIgg"), private, b64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if base64.RawURLEncoding.EncodeToString(got) != want {
		t.Fatalf("ciphertext differs from RFC 8291 example:\n got %s\nwant %s", base64.RawURLEncoding.EncodeToString(got), want)
	}
}

func testKeys(t *testing.T) PushKeys {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return PushKeys{P256DH: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), Auth: "BTBZMqHH6r4Tts7J_aSIgg"}
}

func TestSendPushStatuses(t *testing.T) {
	keys := PushKeys{P256DH: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", Auth: "BTBZMqHH6r4Tts7J_aSIgg"}
	for _, tc := range []struct {
		status          int
		ok, gone, retry bool
	}{{201, true, false, false}, {200, true, false, false}, {410, false, true, false}, {404, false, true, false}, {429, false, false, true}, {503, false, false, true}, {400, false, false, false}} {
		var headers http.Header
		var size int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers = r.Header.Clone()
			b, _ := io.ReadAll(r.Body)
			size = len(b)
			w.WriteHeader(tc.status)
		}))
		err := SendPush(context.Background(), server.Client(), server.URL+"/up/abc", keys, []byte(`{"kind":"test"}`), true)
		server.Close()
		if (err == nil) != tc.ok || IsGone(err) != tc.gone {
			t.Fatalf("status %d: err=%v", tc.status, err)
		}
		if err != nil {
			if d, _ := err.(*DeliveryError); d.Temporary() != tc.retry {
				t.Fatalf("status %d: temporary=%v", tc.status, d.Temporary())
			}
		}
		if headers.Get("Content-Encoding") != "aes128gcm" || headers.Get("Urgency") != "high" || headers.Get("TTL") == "" {
			t.Fatalf("headers %v", headers)
		}
		// salt + rs + idlen + key + plaintext + delimiter + tag
		if size != 16+4+1+65+len(`{"kind":"test"}`)+1+16 {
			t.Fatalf("body size %d", size)
		}
	}
}

func TestPushValidation(t *testing.T) {
	good := testKeys(t)
	if ValidatePushKeys(good) != nil {
		t.Fatal("valid keys rejected")
	}
	for _, bad := range []PushKeys{{P256DH: "abc", Auth: good.Auth}, {P256DH: good.P256DH, Auth: "short"}} {
		if ValidatePushKeys(bad) == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	for _, u := range []string{"ftp://x/y", "https://user:pw@ntfy.example/up", "", " https://x/"} {
		if ValidatePushEndpoint(u) == nil {
			t.Fatalf("accepted %q", u)
		}
	}
	if ValidatePushEndpoint("http://192.168.1.10:8080/upAbc?up=1") != nil {
		t.Fatal("LAN distributor rejected")
	}
}

func TestRenderPush(t *testing.T) {
	var p pushPayload
	_ = json.Unmarshal(RenderPush(Message{Transition: "resolved", RuleName: "CPU busy", Summary: "CPU back to 20%", DeviceName: "nas", IncidentID: "inc-1"}), &p)
	if p.Kind != "resolved" || p.Title != "CPU busy cleared" || p.Body != "nas: CPU back to 20%" || p.Incident != "inc-1" {
		t.Fatalf("%+v", p)
	}
}
