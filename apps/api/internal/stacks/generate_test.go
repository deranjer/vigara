package stacks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/stack"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateValueKinds(t *testing.T) {
	safe := regexp.MustCompile(`^[A-Za-z0-9\-_.!@^*+=?]+$`)
	for kind, check := range map[string]func(string) bool{
		"password":     func(v string) bool { return len(v) == 32 && safe.MatchString(v) },
		"alphanumeric": func(v string) bool { return len(v) == 32 && regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(v) },
		"pin":          func(v string) bool { return len(v) == 6 && regexp.MustCompile(`^[0-9]+$`).MatchString(v) },
		"hex":          func(v string) bool { return len(v) == 64 && regexp.MustCompile(`^[0-9a-f]+$`).MatchString(v) },
		"base64":       func(v string) bool { b, err := base64.StdEncoding.DecodeString(v); return err == nil && len(b) == 32 },
		"jwt_secret": func(v string) bool {
			b, err := base64.RawURLEncoding.DecodeString(v)
			return err == nil && len(b) == 64
		},
		"uuid": func(v string) bool {
			return regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(v)
		},
	} {
		a, err := generateValue(kind, 0)
		b, _ := generateValue(kind, 0)
		if err != nil || !check(a) || (kind != "pin" && a == b) {
			t.Fatalf("%s produced %q / %q (%v)", kind, a, b, err)
		}
	}
	for _, bad := range []struct {
		kind   string
		length int
	}{{"password", 4}, {"jwt_secret", 8}, {"nope", 0}} {
		if _, err := generateValue(bad.kind, bad.length); err == nil {
			t.Fatalf("%v should be rejected", bad)
		}
	}
}

func TestMergeGenerated(t *testing.T) {
	doc, status := mergeGenerated("A=1\n", "B", "x", false)
	if doc != "A=1\nB=x\n" || status != "created" {
		t.Fatalf("%q %s", doc, status)
	}
	doc, status = mergeGenerated("A=1\nB=keep\n", "B", "x", false)
	if !strings.Contains(doc, "B=keep") || status != "kept" {
		t.Fatalf("%q %s", doc, status)
	}
	doc, status = mergeGenerated("A=1\nB=old\nC=3", "B", "x", true)
	if doc != "A=1\nB=x\nC=3" || status != "replaced" {
		t.Fatalf("%q %s", doc, status)
	}
	doc, status = mergeGenerated("export B=\n", "B", "x", false)
	if doc != "B=x\n" || status != "replaced" {
		t.Fatalf("empty value should be filled: %q %s", doc, status)
	}
	doc, _ = mergeGenerated("", "B", "x", false)
	if doc != "B=x\n" {
		t.Fatalf("%q", doc)
	}
}

func TestGenerateEnvironmentEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("protected environment decryption requires Linux")
	}
	pool := testPool(t)
	ctx := context.Background()
	repo, _ := importRepository(t)
	var user, device, agent string
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,display_name,password_hash) VALUES('gen-owner','Owner','test') RETURNING id::text").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO devices(display_name) VALUES('Docker') RETURNING id::text").Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO agent_identities(device_id,credential_hash,protocol_version) VALUES($1,decode('abcdef','hex'),'v1') RETURNING id::text", device).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	key, err := composesecrets.EnsureAgeIdentity(filepath.Join(t.TempDir(), "agent.age"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api := Register(mux, Config{Pool: pool, Repository: repo, Hub: &environmentAgent{key: key}, Audit: audit.NewRecorder(pool), Authz: authorizer{auth.User{ID: user, Username: "gen-owner", Role: auth.RoleAdmin}}})
	if _, _, err = api.importKeys(); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(data))))
		return w
	}
	if w := call("POST", "/api/stacks", map[string]string{"id": "gen", "name": "gen", "agentId": agent, "projectName": "gen"}); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var raw []byte
	if err = pool.QueryRow(ctx, "SELECT files FROM stack_drafts WHERE stack_id='gen'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	s := stack.Snapshot{ID: "gen"}
	if err = json.Unmarshal(raw, &s.Files); err != nil {
		t.Fatal(err)
	}
	type encrypted struct {
		Path, Ciphertext, Manifest string
		Generated                  []generatedResult
	}
	// Generation into a file that does not exist yet creates and provisions it.
	w := call("POST", "/api/stacks/gen/environment/generate", generateRequest{Path: "secrets/app.env.age", Source: environmentSource{Version: 1, Digest: stack.Digest(s)},
		Variables: []generateVariable{{Name: "DB_PASSWORD", Kind: "password"}, {Name: "JWT_SECRET", Kind: "jwt_secret"}, {Name: "OLD", Kind: "hex", Length: 16}}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var first encrypted
	if err = json.Unmarshal(w.Body.Bytes(), &first); err != nil || len(first.Generated) != 3 || first.Generated[0].Status != "created" {
		t.Fatal("first generation", err, w.Body)
	}
	decrypt := func(ciphertext string) map[string]string {
		plain, err := composesecrets.DecryptDotenv(ctx, "", api.CentralAgeIdentity, []byte(ciphertext))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(plain)), "\n") {
			k, v, _ := strings.Cut(line, "=")
			out[k] = v
		}
		return out
	}
	one := decrypt(first.Ciphertext)
	for name, v := range one {
		if v == "" || strings.Contains(w.Body.String(), v) {
			t.Fatalf("%s empty or leaked in response", name)
		}
	}
	// Save into the draft, then add to the existing file: keep one, replace one, add one.
	s.Files[first.Path], s.Files["vigara-stack.yaml"] = first.Ciphertext, first.Manifest
	if w = call("PUT", "/api/stacks/gen/draft", draft{Files: s.Files, Version: 1}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call("POST", "/api/stacks/gen/environment/generate", generateRequest{Path: first.Path, Source: environmentSource{Version: 2, Digest: stack.Digest(s)},
		Variables: []generateVariable{{Name: "DB_PASSWORD", Kind: "password"}, {Name: "OLD", Kind: "hex", Overwrite: true}, {Name: "API_KEY", Kind: "uuid"}}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var second encrypted
	if err = json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	got := []string{second.Generated[0].Status, second.Generated[1].Status, second.Generated[2].Status}
	if strings.Join(got, ",") != "kept,replaced,created" {
		t.Fatal("statuses", got)
	}
	two := decrypt(second.Ciphertext)
	if two["DB_PASSWORD"] != one["DB_PASSWORD"] || two["JWT_SECRET"] != one["JWT_SECRET"] || two["OLD"] == one["OLD"] || two["API_KEY"] == "" {
		t.Fatal("merge result wrong")
	}
	for _, v := range two {
		if strings.Contains(w.Body.String(), v) {
			t.Fatal("value leaked in response")
		}
	}
	// Stale source, bad name and bad kind are refused.
	for _, bad := range []generateRequest{
		{Path: first.Path, Source: environmentSource{Version: 1, Digest: stack.Digest(s)}, Variables: []generateVariable{{Name: "X", Kind: "hex"}}},
		{Path: first.Path, Source: environmentSource{Version: 2, Digest: stack.Digest(s)}, Variables: []generateVariable{{Name: "bad name", Kind: "hex"}}},
		{Path: first.Path, Source: environmentSource{Version: 2, Digest: stack.Digest(s)}, Variables: []generateVariable{{Name: "X", Kind: "rot13"}}},
	} {
		if w = call("POST", "/api/stacks/gen/environment/generate", bad); w.Code < 400 {
			t.Fatal("bad request accepted", bad)
		}
	}
	var details string
	if err = pool.QueryRow(ctx, "SELECT coalesce(string_agg(detail::text,','),'') FROM audit_events").Scan(&details); err != nil || !strings.Contains(details, "stack.environment.generate") && !strings.Contains(details, "DB_PASSWORD") {
		t.Fatal("audit missing", err)
	}
	for _, v := range two {
		if strings.Contains(details, v) {
			t.Fatal("audit leaked a value")
		}
	}
}
