package stackgit

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthenticationOnlyForNetworkOperationsAndNeverPersisted(t *testing.T) {
	const secret = "ephemeral-test-token"
	header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != strings.TrimPrefix(header, "AUTHORIZATION: ") {
			t.Error("missing ephemeral authentication")
		}
		if strings.Contains(r.URL.String(), secret) {
			t.Error("secret in URL")
		}
		w.WriteHeader(403)
	}))
	defer server.Close()
	remote := server.URL + "/owner/repo.git"
	calls := 0
	repo := &Repository{Directory: filepath.Join(t.TempDir(), "cache.git"), Remote: remote, Branch: "main", Authenticate: func(_ context.Context, got string) ([]string, error) {
		calls++
		if got != remote {
			t.Error("incorrect remote")
		}
		return []string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=http." + got + ".extraHeader", "GIT_CONFIG_VALUE_0=" + header, "GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=credential.helper", "GIT_CONFIG_VALUE_2="}, nil
	}}
	ctx := context.Background()
	if err := repo.init(ctx); err != nil {
		t.Fatal(err)
	}
	tree, err := repo.run(ctx, "", nil, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost"}
	commit, err := repo.run(ctx, "fixture\n", env, "commit-tree", strings.TrimSpace(tree))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.run(ctx, "", nil, "fetch", remote, "main"); err == nil {
		t.Fatal("denied fetch succeeded")
	}
	if _, err = repo.run(ctx, "", nil, "push", remote, strings.TrimSpace(commit)+":refs/heads/main"); err == nil {
		t.Fatal("denied push succeeded")
	}
	if calls != 2 || requests.Load() != 2 {
		t.Fatal("authentication missing on fetch/push", calls, requests.Load())
	}
	config, err := repo.run(ctx, "", nil, "config", "--local", "--list")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Contains(config, secret) || strings.Contains(config, "extraheader") {
		t.Fatal("credentials used locally or persisted")
	}
}

func TestAuthenticatedGitDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+r.URL.Path, 302) }))
	defer server.Close()
	remote := server.URL + "/repo.git"
	repo := &Repository{Directory: filepath.Join(t.TempDir(), "cache.git"), Remote: remote, Branch: "main", Authenticate: func(context.Context, string) ([]string, error) {
		return []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.followRedirects", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_KEY_1=http." + remote + ".extraHeader", "GIT_CONFIG_VALUE_1=AUTHORIZATION: basic dGVzdA=="}, nil
	}}
	ctx := context.Background()
	if err := repo.init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "", nil, "fetch", remote, "main"); err == nil || followed.Load() {
		t.Fatal("Git followed authenticated redirect", err)
	}
}
