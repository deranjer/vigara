package stacks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

type imageTagsHub struct {
	t     *testing.T
	calls int
}

func (h *imageTagsHub) Call(_ context.Context, agent, method string, in, out any) error {
	h.calls++
	q := in.(protocol.ComposeImageTagsRequest)
	if agent != "assigned-agent" || method != protocol.MethodComposeImageTags || q.Image != "nginx:latest" || q.Last != "v1" {
		h.t.Fatalf("wrong device or lookup: %s %s %+v", agent, method, q)
	}
	*out.(*protocol.ComposeImageTags) = protocol.ComposeImageTags{Repository: "index.docker.io/library/nginx", Tags: []string{"v2"}, Next: "v2"}
	return nil
}

func TestStackTagLookupUsesAssignedDeviceAndDeployPermission(t *testing.T) {
	for _, test := range []struct {
		role, id, body string
		status         int
	}{
		{auth.RoleAdmin, "demo", `{"image":"nginx:latest","last":"v1"}`, 200},
		{auth.RoleViewer, "demo", `{"image":"nginx:latest"}`, 403},
		{auth.RoleOperator, "demo", `{"image":"nginx:latest"}`, 403},
		{auth.RoleAdmin, "unassigned", `{"image":"nginx:latest"}`, 409},
		{auth.RoleAdmin, "demo", `{"image":""}`, 422},
	} {
		t.Run(test.role+test.id+test.body, func(t *testing.T) {
			hub := &imageTagsHub{t: t}
			mux := http.NewServeMux()
			Register(mux, Config{Authz: authorizer{auth.User{Role: test.role}}, Targets: map[string]Binding{"demo": {AgentID: "assigned-agent", ProjectName: "demo"}}, Hub: hub})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/"+test.id+"/image-tags", strings.NewReader(test.body)))
			if w.Code != test.status {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			if (hub.calls == 1) != (test.status == 200) {
				t.Fatalf("unexpected registry access: %d", hub.calls)
			}
			if test.status == 200 && !strings.Contains(w.Body.String(), `"next":"v2"`) {
				t.Fatalf("lost pagination cursor: %s", w.Body)
			}
		})
	}
}
