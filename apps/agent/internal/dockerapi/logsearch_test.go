package dockerapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestLogSearchFindsStartupCodeBeyondRecentTail(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			fmt.Fprint(w, `{"Config":{"Tty":false}}`)
			return
		}
		if r.URL.Query().Get("tail") != "all" || r.URL.Query().Get("since") != "1794358857.000000000" || r.URL.Query().Get("until") != "1794528000.000000000" {
			t.Errorf("wrong search window %v", r.URL.Query())
		}
		w.Write(frame(1, "2026-11-11T01:01:25.281Z JoinCode value[ABCD-1234]\n"))
		for i := 0; i < 6000; i++ {
			w.Write(frame(1, "2026-11-12T00:00:00Z noisy game event\n"))
		}
	})
	out, err := client.SearchLogs(context.Background(), protocol.DockerLogSearchParams{ID: "game", Match: "JoinCode", Since: "2026-11-11T01:00:57Z", Until: "2026-11-13T00:00:00Z"})
	if err != nil || len(out.Lines) != 1 || !strings.Contains(out.Lines[0].M, "ABCD-1234") || out.Truncated || out.NextSince != "2026-11-12T00:00:00Z" {
		t.Fatalf("search: %+v %v", out, err)
	}
}

func TestDockerLogTimestampPreservesNanosecondsAndOffsets(t *testing.T) {
	for _, raw := range []string{"2026-11-14T00:02:12.484967004Z", "2026-11-13T19:02:12.484967004-05:00"} {
		got, err := dockerLogTimestamp(raw)
		if err != nil || got != "1794614532.484967004" {
			t.Fatalf("%s became %s (%v)", raw, got, err)
		}
	}
	if _, err := dockerLogTimestamp("yesterday"); err == nil {
		t.Fatal("invalid timestamp accepted")
	}
}

func TestRecentLogReadConvertsSinceForDockerHTTPAPI(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			fmt.Fprint(w, `{"Config":{"Tty":true}}`)
			return
		}
		if r.URL.Query().Get("since") != "1794614532.484967004" {
			t.Errorf("Docker received unsupported timestamp: %v", r.URL.Query())
		}
		fmt.Fprint(w, "2026-11-14T00:02:13Z JoinCode value[TEST-CODE]\n")
	})
	lines, _ := collectLogs(t, client, protocol.DockerLogsParams{ID: "game", Since: "2026-11-14T00:02:12.484967004Z", Timestamps: true})
	if len(lines) != 1 {
		t.Fatalf("expected one log line, got %v", lines)
	}
}
func TestLogSearchBoundsMatchesAndRejectsInvalidWindows(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			fmt.Fprint(w, `{"Config":{"Tty":true}}`)
			return
		}
		for i := 0; i < 230; i++ {
			fmt.Fprintf(w, "2026-11-11T01:01:25Z code %03d\n", i)
		}
	})
	out, err := client.SearchLogs(context.Background(), protocol.DockerLogSearchParams{ID: "game", Match: "code"})
	if err != nil || !out.MoreMatches || out.Truncated || len(out.Lines) != 200 || !strings.Contains(out.Lines[0].M, "030") {
		t.Fatalf("bounded matches %+v %v", out, err)
	}
	for _, p := range []protocol.DockerLogSearchParams{{ID: "../host", Match: "code"}, {ID: "game"}, {ID: "game", Match: "x", Since: "yesterday"}, {ID: "game", Match: "x", Since: "2026-11-12T00:00:00Z", Until: "2026-11-11T00:00:00Z"}} {
		if _, err := client.SearchLogs(context.Background(), p); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}
func TestScanBudgetDoesNotEmitPartialValue(t *testing.T) {
	reader := &scanReader{r: strings.NewReader("2026-11-11T01:00:00Z code value[incomplete"), remaining: 30}
	count := 0
	err := pumpLogs(reader, true, func(_, _ string, _ bool) error { count++; return nil })
	if err != errLogScanLimit || count != 0 {
		t.Fatalf("partial line emitted: count=%d err=%v", count, err)
	}
}
