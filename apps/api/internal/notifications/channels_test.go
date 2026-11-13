package notifications

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
)

var testMasterKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func TestSealerBindsToChannel(t *testing.T) {
	if s, err := NewSealer(""); s != nil || err != nil {
		t.Fatalf("empty key: %v %v", s, err)
	}
	if _, err := NewSealer("c2hvcnQ="); err == nil {
		t.Fatal("short key accepted")
	}
	s, err := NewSealer(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	c := notifytransport.ChannelConfig{URL: "https://ntfy.sh", Topic: "alerts", Token: "tk_secret"}
	sealed, err := s.seal("ntfy:a", c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "tk_secret") {
		t.Fatal("token stored in the clear")
	}
	if got, err := s.open("ntfy:a", sealed); err != nil || got != c {
		t.Fatalf("open: %+v %v", got, err)
	}
	if _, err := s.open("ntfy:b", sealed); err == nil {
		t.Fatal("sealed config opened under another channel ID")
	}
	other, _ := NewSealer(base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210")))
	if _, err := other.open("ntfy:a", sealed); err == nil {
		t.Fatal("sealed config opened with another key")
	}
}

func TestImportEnvOnce(t *testing.T) {
	p := notificationTestPool(t)
	ctx := context.Background()
	env := []notifytransport.EnvChannel{{ID: "ntfy", Kind: "ntfy", Config: notifytransport.ChannelConfig{URL: "https://ntfy.sh", Topic: "alerts", Token: "tk_env"}}}

	// Without a key nothing is imported and the channel stays read-only.
	ignored, readOnly, err := ImportEnv(ctx, p, nil, env)
	if err != nil || len(ignored) != 0 || len(readOnly) != 1 {
		t.Fatalf("no key: %v %v %v", ignored, readOnly, err)
	}
	sealer, _ := NewSealer(testMasterKey)
	if ignored, readOnly, err = ImportEnv(ctx, p, sealer, env); err != nil || len(ignored) != 0 || len(readOnly) != 0 {
		t.Fatalf("import: %v %v %v", ignored, readOnly, err)
	}
	r := NewRegistry(p, sealer, nil)
	if err := r.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if d := r.Destinations(); len(d) != 1 || d[0].ID != "ntfy" || d[0].Source != SourceSettings || d[0].Target != "https://ntfy.sh · topic alerts" {
		t.Fatalf("destinations %+v", d)
	}
	if c, _ := r.config("ntfy"); c.Token != "tk_env" {
		t.Fatalf("imported config %+v", c)
	}
	// Removed later, it stays removed: the variables are ignored from now on.
	if _, err := p.Exec(ctx, `DELETE FROM notification_destinations WHERE id='ntfy'`); err != nil {
		t.Fatal(err)
	}
	if ignored, readOnly, err = ImportEnv(ctx, p, sealer, env); err != nil || len(ignored) != 1 || len(readOnly) != 0 {
		t.Fatalf("second import: %v %v %v", ignored, readOnly, err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_channels WHERE id=$1`, 0, "ntfy")
}

func TestChannelAPI(t *testing.T) {
	p := notificationTestPool(t)
	ctx := context.Background()
	var mu sync.Mutex
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	defer server.Close()
	lastAuth := func() string { mu.Lock(); defer mu.Unlock(); return auths[len(auths)-1] }

	service := auth.NewService(p, db.New(p))
	session, err := service.CreateFirstUser(ctx, "channel-admin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	newMux := func(sealer *Sealer) (*http.ServeMux, *Registry) {
		r := NewRegistry(p, sealer, nil)
		if err := r.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		h := NewHandler(p, service, nil)
		h.SetRegistry(r)
		mux := http.NewServeMux()
		h.Register(mux)
		return mux, r
	}
	call := func(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Without a master key channels cannot be added.
	locked, _ := newMux(nil)
	if w := call(locked, "POST", "/api/notifications/channels", `{"kind":"ntfy","name":"Phone","url":"`+server.URL+`","topic":"alerts"}`); w.Code != 503 {
		t.Fatalf("no key status=%d", w.Code)
	}
	if w := call(locked, "GET", "/api/notifications", ""); !strings.Contains(w.Body.String(), `"canStore":false`) {
		t.Fatalf("settings %s", w.Body.String())
	}

	sealer, _ := NewSealer(testMasterKey)
	mux, registry := newMux(sealer)
	if w := call(mux, "POST", "/api/notifications/channels", `{"kind":"ntfy","name":"Phone","url":"`+server.URL+`","topic":"bad topic!"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "Topic") {
		t.Fatalf("bad topic status=%d %s", w.Code, w.Body.String())
	}
	// Check before saving reaches the server with the token.
	if w := call(mux, "POST", "/api/notifications/channels/check", `{"kind":"ntfy","name":"Phone","url":"`+server.URL+`","topic":"alerts","token":"tk_one"}`); !strings.Contains(w.Body.String(), `"ok":true`) || lastAuth() != "Bearer tk_one" {
		t.Fatalf("check %s auth=%q", w.Body.String(), lastAuth())
	}
	w := call(mux, "POST", "/api/notifications/channels", `{"kind":"ntfy","name":"  Phone   alerts ","url":"`+server.URL+`","topic":"alerts","token":"tk_one"}`)
	if w.Code != 201 {
		t.Fatalf("create status=%d %s", w.Code, w.Body.String())
	}
	var created struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if !strings.HasPrefix(created.ID, "ntfy:") {
		t.Fatalf("id %q", created.ID)
	}

	w = call(mux, "GET", "/api/notifications", "")
	body := w.Body.String()
	if strings.Contains(body, "tk_one") || !strings.Contains(body, `"name":"Phone alerts"`) || !strings.Contains(body, `"source":"settings"`) || !strings.Contains(body, `"hasToken":true`) || !strings.Contains(body, `"topic":"alerts"`) {
		t.Fatalf("settings %s", body)
	}

	// Renaming without a token keeps it; the saved channel's test uses it.
	if w = call(mux, "PUT", "/api/notifications/channels/"+created.ID, `{"name":"Desk","url":"`+server.URL+`","topic":"alerts2"}`); w.Code != 200 {
		t.Fatalf("update status=%d %s", w.Code, w.Body.String())
	}
	if c, _ := registry.config(created.ID); c.Token != "tk_one" || c.Topic != "alerts2" {
		t.Fatalf("after update %+v", c)
	}
	if w = call(mux, "POST", "/api/notifications/channels/"+created.ID+"/test", ""); !strings.Contains(w.Body.String(), `"ok":true`) || lastAuth() != "Bearer tk_one" {
		t.Fatalf("test %s auth=%q", w.Body.String(), lastAuth())
	}
	// An empty token clears it.
	if w = call(mux, "PUT", "/api/notifications/channels/"+created.ID, `{"name":"Desk","url":"`+server.URL+`","topic":"alerts2","token":""}`); w.Code != 200 {
		t.Fatalf("clear token status=%d", w.Code)
	}
	if c, _ := registry.config(created.ID); c.Token != "" {
		t.Fatalf("token not cleared %+v", c)
	}
	if w = call(mux, "PUT", "/api/notifications/channels/"+created.ID, `{"kind":"gotify","name":"Desk"}`); w.Code != 400 {
		t.Fatalf("kind change status=%d", w.Code)
	}
	// Routing for the new channel saves like any other.
	if w = call(mux, "PUT", "/api/notifications/settings", `{"quietHours":{"enabled":false,"start":"23:00","end":"07:00","timezone":"UTC"},"routes":{"`+created.ID+`":{"critical":true,"warning":false,"resolved":false}}}`); w.Code != 200 {
		t.Fatalf("routes status=%d %s", w.Code, w.Body.String())
	}

	// A different key cannot read it: listed as unable to send, not dropped.
	wrong, _ := NewSealer(base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210")))
	other := NewRegistry(p, wrong, nil)
	if err := other.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if d := other.Destinations(); len(d) != 1 || !strings.Contains(d[0].Target, "Cannot send") {
		t.Fatalf("wrong key destinations %+v", d)
	}

	if w = call(mux, "DELETE", "/api/notifications/channels/"+created.ID, ""); w.Code != 204 {
		t.Fatalf("delete status=%d", w.Code)
	}
	if len(registry.Destinations()) != 0 {
		t.Fatal("deleted channel still listed")
	}
	assertInt(t, p, `SELECT count(*) FROM notification_destinations WHERE id=$1`, 0, created.ID)
	if w = call(mux, "DELETE", "/api/notifications/channels/android", ""); w.Code != 404 {
		t.Fatalf("delete built-in status=%d", w.Code)
	}
}
