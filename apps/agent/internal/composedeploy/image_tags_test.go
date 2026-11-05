package composedeploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestImageTagPagesPreserveRepositoryAndCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/web/tags/list" || r.URL.Query().Get("n") != "500" {
			t.Errorf("unexpected registry request: %s", r.URL)
		}
		switch r.URL.Query().Get("last") {
		case "":
			w.Header().Set("Link", `<https://untrusted.example/v2/team/web/tags/list?n=500&last=v2>; rel="next"`)
			_, _ = w.Write([]byte(`{"tags":["v2","latest","v2"]}`))
		case "v2":
			_, _ = w.Write([]byte(`{"tags":["v3"]}`))
		default:
			t.Errorf("unexpected cursor: %s", r.URL)
		}
	}))
	defer server.Close()
	repo, err := name.NewRepository(strings.TrimPrefix(server.URL, "http://") + "/team/web")
	if err != nil {
		t.Fatal(err)
	}
	page, err := listImageTags(context.Background(), repo, "", server.Client())
	if err != nil || strings.Join(page.Tags, ",") != "latest,v2" || page.Next != "v2" || page.Repository != repo.Name() {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page, err = listImageTags(context.Background(), repo, page.Next, server.Client())
	if err != nil || strings.Join(page.Tags, ",") != "v3" || page.Next != "" {
		t.Fatalf("last page: %+v %v", page, err)
	}
}

func TestImageTagPagesRejectRegistryErrorsAndInvalidCursors(t *testing.T) {
	for _, test := range []struct {
		body, link string
		status     int
	}{
		{`private registry diagnostic`, "", 401},
		{`not json`, "", 200},
		{`{"tags":["bad/tag"]}`, "", 200},
		{`{"tags":["v2"]}`, `</tags?last=already>; rel="next"`, 200},
	} {
		t.Run(test.body+test.link, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", test.link)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			repo, _ := name.NewRepository(strings.TrimPrefix(server.URL, "http://") + "/web")
			if _, err := listImageTags(context.Background(), repo, "already", server.Client()); err == nil {
				t.Fatal("accepted invalid registry response")
			}
		})
	}
}

func TestImageTagHandlerRejectsInvalidInput(t *testing.T) {
	calls := map[string]channel.Handler{}
	registerImageTags(calls)
	for _, raw := range []string{`{`, `{"image":""}`, `{"image":"https://example.com/web"}`, `{"image":"nginx","last":"../bad"}`} {
		if _, err := calls[protocol.MethodComposeImageTags](context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestRecreateDeploymentUsesReviewedImagesWithoutRemovingStorage(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		args := strings.Join(deploymentUpArgs(recreate), " ")
		if strings.Contains(args, "--force-recreate") != recreate || !strings.Contains(args, "--pull never") || !strings.Contains(args, "--no-build") || !strings.Contains(args, "--wait") || strings.Contains(args, "--renew-anon-volumes") || strings.Contains(args, "down") {
			t.Fatalf("unsafe deployment arguments: %s", args)
		}
	}
}

func TestImageSourcesDoNotResolveOrPullCurrentTags(t *testing.T) {
	r := testRunner(t)
	s := testSnapshot()
	s.Files["compose.yaml"] = "services:\n  web:\n    image: example/web:${VERSION}\n"
	r.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--resolve-image-digests") || strings.Contains(joined, " pull ") || strings.Contains(joined, " up ") {
			t.Fatalf("tag browsing accessed the current image or changed containers: %s", joined)
		}
		return []byte(`{"services":{"web":{"image":"example/web:removed-tag"}}}`), nil
	}
	m, err := stack.Validate(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.composeModel(context.Background(), s, r.Targets[s.ID], m, nil, true)
	if err != nil || plan.Tracking["web"] != "example/web:removed-tag" || plan.Token != "" {
		t.Fatalf("source lookup: %+v %v", plan, err)
	}
}
