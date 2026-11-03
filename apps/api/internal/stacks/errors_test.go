package stacks

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stackgit"
)

func TestFailExplainsStackFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"git", &stackgit.CommandError{Operation: "fetch", Reason: "The configured branch is unavailable."}, 502, "Git fetch failed: The configured branch"},
		{"legacy git", stackgit.ErrGit, 502, "Git operation failed"},
		{"timeout", context.DeadlineExceeded, 504, "timed out"},
		{"canceled", context.Canceled, 502, "canceled"},
		{"offline", channel.ErrAgentOffline, 503, "target device is offline"},
		{"disconnected", channel.ErrAgentDisconnected, 502, "device disconnected"},
		{"agent", &protocol.ChannelError{Code: protocol.ErrCodeTierInsufficient, Message: "this host allows Docker read access, but the request needs deploy"}, 502, "tier_insufficient: this host allows Docker read access"},
		{"unknown", errors.New("database credentials secret"), 502, "Could not complete the stack operation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			fail(w, fmt.Errorf("wrapped: %w", tc.err))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("exposed internal error details")
			}
		})
	}
}
