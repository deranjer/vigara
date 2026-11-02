package mcpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// An opt-in local browser harness: no account login, database, tokens or real
// OAuth grants. The POST exercises the real signed CSRF check and then simulates
// its successful redirect. Separate loopback ports model an external client.
func TestBrowserConsentCallbackFixture(t *testing.T) {
	if os.Getenv("VIGIRA_BROWSER_CONSENT_FIXTURE") != "1" {
		t.Skip("opt-in local browser fixture")
	}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "LOCAL CALLBACK REACHED — no real authorization granted")
	}))
	defer callback.Close()
	h, err := New(nil, nil, nil, "http://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			h.consentPage(w, r, authorization{Client: client{Name: "Local dummy browser fixture"}, Redirect: callback.URL + "/callback", Scopes: []string{"devices.read"}}, "")
			return
		}
		if !parseForm(w, r) || !h.checkCSRF(w, r) {
			return
		}
		h.clearCSRF(w)
		http.Redirect(w, r, callback.URL+"/callback?code=dummy", http.StatusSeeOther)
	}))
	defer server.Close()
	h.issuer = server.URL
	fmt.Printf("LOCAL_BROWSER_FIXTURE=%s/oauth/authorize\n", server.URL)
	time.Sleep(8 * time.Minute)
}
