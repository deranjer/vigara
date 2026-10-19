package dockerapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

// engine is a fake Docker Engine API that records every request.
type engine struct {
	mu       sync.Mutex
	requests []string
	methods  map[string]bool
	handler  func(w http.ResponseWriter, r *http.Request)
}

func newEngine(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *engine) {
	t.Helper()
	e := &engine{methods: map[string]bool{}, handler: handler}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.requests = append(e.requests, r.URL.RequestURI())
		e.methods[r.Method] = true
		e.mu.Unlock()
		e.handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, e
}

func frame(stream byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

const inspectJSON = `{
 "Id":"abc123def4567890","Created":"2026-10-01T10:00:00Z","Path":"/entry.sh","Args":["--serve","--port=80"],"Image":"sha256:img","Name":"/web","RestartCount":2,
 "State":{"Status":"running","Running":true,"Pid":4242,"ExitCode":0,"StartedAt":"2026-10-18T09:00:00Z","FinishedAt":"0001-01-01T00:00:00Z","Health":{"Status":"healthy"}},
 "HostConfig":{"NetworkMode":"bridge","Privileged":true,"ReadonlyRootfs":true,"Memory":268435456,"NanoCpus":1500000000,"CapAdd":["NET_ADMIN"],"CapDrop":["ALL"],"RestartPolicy":{"Name":"unless-stopped"},"LogConfig":{"Type":"json-file"}},
 "Mounts":[{"Type":"bind","Source":"/srv/data","Destination":"/data","RW":true},{"Type":"volume","Name":"cache","Source":"/var/lib/docker/volumes/cache/_data","Destination":"/cache","RW":false}],
 "Config":{"Hostname":"web-host","User":"1000","Tty":false,"Env":["DB_PASSWORD=hunter2","PATH=/usr/bin","EMPTY="],"Cmd":["--serve"],"Image":"nginx:1.27","WorkingDir":"/app","Entrypoint":["/entry.sh"],"Labels":{"com.docker.compose.project":"site"}},
 "NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}],"443/tcp":null},"Networks":{"site_default":{"Aliases":["web"],"Gateway":"172.18.0.1","IPAddress":"172.18.0.2","MacAddress":"02:42:ac:12:00:02"}}}
}`

func TestListMapsComposeLabelsAndOrdersByName(t *testing.T) {
	client, e := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.44/containers/json" || r.URL.Query().Get("all") != "1" {
			t.Errorf("unexpected request %s", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`[
 {"Id":"bbb","Names":["/zeta"],"Image":"redis","State":"exited","Status":"Exited (0) 2 hours ago","Created":100,"Ports":[],"Labels":{}},
 {"Id":"aaa","Names":["/alpha"],"Image":"nginx","State":"running","Status":"Up 3 hours","Created":200,"Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],"Labels":{"com.docker.compose.project":"site","com.docker.compose.service":"web"}}
]`))
	})
	result, err := client.List(context.Background())
	if err != nil || len(result.Containers) != 2 {
		t.Fatalf("List = %+v, %v", result, err)
	}
	first := result.Containers[0]
	if first.Name != "alpha" || first.Project != "site" || first.Service != "web" || len(first.Ports) != 1 || first.Ports[0].PublicPort != 8080 || result.Containers[1].Name != "zeta" {
		t.Fatalf("containers = %+v", result.Containers)
	}
	if len(e.methods) != 1 || !e.methods[http.MethodGet] {
		t.Fatalf("methods used: %v", e.methods)
	}
}

func TestInspectRedactsEnvironmentUnlessRevealed(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(inspectJSON)) })
	hidden, err := client.Inspect(context.Background(), "web", false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(hidden)
	if strings.Contains(string(encoded), "hunter2") || hidden.EnvRevealed || len(hidden.Env) != 3 || hidden.Env[0].Name != "DB_PASSWORD" || hidden.Env[0].Value != "" {
		t.Fatalf("redacted env = %+v revealed=%v body=%s", hidden.Env, hidden.EnvRevealed, encoded)
	}
	shown, err := client.Inspect(context.Background(), "web", true)
	if err != nil || !shown.EnvRevealed || shown.Env[0].Value != "hunter2" || shown.Env[1].Name != "EMPTY" || shown.Env[2].Value != "/usr/bin" {
		t.Fatalf("revealed env = %+v, %v", shown.Env, err)
	}
	if shown.Name != "web" || shown.Image != "nginx:1.27" || shown.State.Health != "healthy" || shown.State.FinishedAt != "" || shown.State.StartedAt == "" ||
		!shown.Privileged || !shown.ReadonlyRoot || shown.RestartPolicy != "unless-stopped" || shown.RestartCount != 2 || shown.MemoryLimit != 268435456 {
		t.Fatalf("inspect = %+v", shown)
	}
	if len(shown.Command) != 3 || shown.Command[0] != "/entry.sh" || len(shown.Mounts) != 2 || shown.Mounts[1].ReadWrite || len(shown.Networks) != 1 || shown.Networks[0].IPAddress != "172.18.0.2" {
		t.Fatalf("command/mounts/networks = %+v %+v %+v", shown.Command, shown.Mounts, shown.Networks)
	}
	if len(shown.Ports) != 2 || shown.Ports[0].PrivatePort != 80 || shown.Ports[0].PublicPort != 8080 || shown.Ports[1].PrivatePort != 443 || shown.Ports[1].PublicPort != 0 {
		t.Fatalf("ports = %+v", shown.Ports)
	}
}

func TestContainerReferencesAreValidatedBeforeAnyRequest(t *testing.T) {
	client, e := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing/json") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(inspectJSON))
	})
	for _, bad := range []string{"", "../etc", "a/b", "a b", "-flag", "web?x=1", "web%2f..", strings.Repeat("a", 129), "web\n"} {
		if _, err := client.Inspect(context.Background(), bad, false); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Inspect(%q) error = %v, want ErrBadRequest", bad, err)
		}
		if err := client.Logs(context.Background(), protocol.DockerLogsParams{ID: bad}, func([]byte) error { return nil }); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Logs(%q) error = %v", bad, err)
		}
		if err := client.Stats(context.Background(), protocol.DockerStatsParams{ID: bad}, func([]byte) error { return nil }); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Stats(%q) error = %v", bad, err)
		}
	}
	if len(e.requests) != 0 {
		t.Fatalf("invalid references reached Docker: %v", e.requests)
	}
	if _, err := client.Inspect(context.Background(), "missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing container error = %v", err)
	}
	if len(e.methods) != 1 || !e.methods[http.MethodGet] {
		t.Fatalf("methods used: %v", e.methods)
	}
}

func collectLogs(t *testing.T, client *Client, params protocol.DockerLogsParams) ([]protocol.DockerLogLine, [][]byte) {
	t.Helper()
	var chunks [][]byte
	var lines []protocol.DockerLogLine
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := client.Logs(ctx, params, func(data []byte) error {
		chunks = append(chunks, append([]byte(nil), data...))
		for _, raw := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
			var line protocol.DockerLogLine
			if err := json.Unmarshal(raw, &line); err != nil {
				t.Fatalf("chunk is not whole NDJSON lines: %v: %q", err, raw)
			}
			lines = append(lines, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	return lines, chunks
}

func TestLogsDemultiplexFramesAndAssembleLines(t *testing.T) {
	var query map[string][]string
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"Config":{"Tty":false}}`))
			return
		}
		query = r.URL.Query()
		var body bytes.Buffer
		body.Write(frame(1, "2026-10-18T10:00:00.000000001Z first line\n2026-10-18T10:00:01Z sec"))
		body.Write(frame(2, "2026-10-18T10:00:02Z oops on stderr\r\n"))
		body.Write(frame(1, "ond line\n2026-10-18T10:00:03Z no newline at end"))
		_, _ = w.Write(body.Bytes())
	})
	lines, _ := collectLogs(t, client, protocol.DockerLogsParams{ID: "web", Tail: 99999, Follow: true, Timestamps: true})
	if query["tail"][0] != "5000" || query["follow"][0] != "1" || query["timestamps"][0] != "1" || query["stdout"][0] != "1" || query["stderr"][0] != "1" {
		t.Fatalf("query = %v", query)
	}
	want := []protocol.DockerLogLine{
		{S: "o", T: "2026-10-18T10:00:00.000000001Z", M: "first line"},
		{S: "e", T: "2026-10-18T10:00:02Z", M: "oops on stderr"},
		{S: "o", T: "2026-10-18T10:00:01Z", M: "second line"},
		{S: "o", T: "2026-10-18T10:00:03Z", M: "no newline at end"},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

func TestLogsHandleTTYLongLinesAndInvalidUTF8(t *testing.T) {
	long := strings.Repeat("x", protocol.MaxLogLineBytes*2+500)
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"Config":{"Tty":true}}`))
			return
		}
		if r.URL.Query().Get("tail") != "200" || r.URL.Query().Get("follow") != "" {
			t.Errorf("defaults not applied: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte("tty line\n" + long + "\nafter long\nbad\xff\xfebytes\n"))
	})
	lines, chunks := collectLogs(t, client, protocol.DockerLogsParams{ID: "web"})
	if len(lines) != 4 || lines[0].M != "tty line" || lines[0].S != "o" {
		t.Fatalf("lines = %d: %+v", len(lines), lines[:1])
	}
	if !lines[1].X || len(lines[1].M) != protocol.MaxLogLineBytes || lines[2].M != "after long" || lines[2].X {
		t.Fatalf("long line handling: truncated=%v len=%d next=%+v", lines[1].X, len(lines[1].M), lines[2])
	}
	if !strings.Contains(lines[3].M, "bytes") {
		t.Fatalf("invalid UTF-8 line = %q", lines[3].M)
	}
	for _, chunk := range chunks {
		if len(chunk) > protocol.MaxStreamChunkBytes || !bytes.HasSuffix(chunk, []byte("\n")) {
			t.Fatalf("chunk of %d bytes violates the channel limit or line boundary", len(chunk))
		}
	}
}

func TestLogsBatchesManyLinesWithinChunkLimits(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"Config":{"Tty":true}}`))
			return
		}
		var body strings.Builder
		for i := 0; i < 5000; i++ {
			body.WriteString(strings.Repeat("log-", 20) + "\n")
		}
		_, _ = w.Write([]byte(body.String()))
	})
	lines, chunks := collectLogs(t, client, protocol.DockerLogsParams{ID: "web"})
	if len(lines) != 5000 || len(chunks) < 2 {
		t.Fatalf("%d lines in %d chunks", len(lines), len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > protocol.MaxStreamChunkBytes {
			t.Fatalf("chunk of %d bytes exceeds the channel limit", len(chunk))
		}
	}
}

func TestFollowingLogsFlushPromptlyAndStopWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"Config":{"Tty":true}}`))
			return
		}
		_, _ = w.Write([]byte("live line\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.Logs(ctx, protocol.DockerLogsParams{ID: "web", Follow: true}, func(data []byte) error {
			select {
			case got <- string(data):
			default:
			}
			return nil
		})
	}()
	select {
	case chunk := <-got:
		if !strings.Contains(chunk, "live line") {
			t.Fatalf("first chunk = %q", chunk)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a followed line was not delivered promptly")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled follow returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling did not stop the log stream")
	}
}

func TestStatsComputesCPUMemoryAndRates(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "1" {
			t.Errorf("stats must stream: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"read":"2026-10-18T10:00:00Z","pids_stats":{"current":7},
 "cpu_stats":{"cpu_usage":{"total_usage":2000},"system_cpu_usage":20000,"online_cpus":4},"precpu_stats":{"cpu_usage":{"total_usage":1000},"system_cpu_usage":10000},
 "memory_stats":{"usage":1000,"limit":4000,"stats":{"inactive_file":200}},
 "networks":{"eth0":{"rx_bytes":1000,"tx_bytes":500},"eth1":{"rx_bytes":1,"tx_bytes":1}},
 "blkio_stats":{"io_service_bytes_recursive":[{"op":"Read","value":10},{"op":"write","value":20},{"op":"Total","value":30}]}}
{"read":"2026-10-18T10:00:02Z","pids_stats":{"current":8},
 "cpu_stats":{"cpu_usage":{"total_usage":3000},"system_cpu_usage":30000,"online_cpus":4},"precpu_stats":{"cpu_usage":{"total_usage":2000},"system_cpu_usage":20000},
 "memory_stats":{"usage":2000,"limit":4000,"stats":{"total_inactive_file":0}},
 "networks":{"eth0":{"rx_bytes":3001,"tx_bytes":1500}}}`))
	})
	var samples []protocol.DockerStatsSample
	err := client.Stats(context.Background(), protocol.DockerStatsParams{ID: "web"}, func(data []byte) error {
		var s protocol.DockerStatsSample
		if err := json.Unmarshal(bytes.TrimSpace(data), &s); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, s)
		return nil
	})
	if err != nil || len(samples) != 2 {
		t.Fatalf("Stats = %d samples, %v", len(samples), err)
	}
	first, second := samples[0], samples[1]
	if first.CPU != 40 || first.CPUCores != 4 || first.MemUsed != 800 || first.MemPct != 20 || first.NetRx != 1001 || first.NetTx != 501 || first.BlkRead != 10 || first.BlkWrite != 20 || first.Pids != 7 {
		t.Fatalf("first = %+v", first)
	}
	if first.NetRxBps != nil || first.NetTxBps != nil {
		t.Fatalf("a rate was reported with no previous sample: %+v", first)
	}
	if second.MemUsed != 2000 || second.NetRxBps == nil || *second.NetRxBps != (3001-1001)/2.0 || second.NetTxBps == nil || *second.NetTxBps != (1500-501)/2.0 {
		t.Fatalf("second = %+v", second)
	}
}

func TestSendFailuresStopTheStreams(t *testing.T) {
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"Config":{"Tty":true}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/stats") {
			for i := 0; i < 3; i++ {
				_, _ = w.Write([]byte(`{"read":"2026-10-18T10:00:00Z"}`))
			}
			return
		}
		_, _ = w.Write([]byte("a\nb\n"))
	})
	boom := errors.New("channel closed")
	if err := client.Logs(context.Background(), protocol.DockerLogsParams{ID: "web"}, func([]byte) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("Logs error = %v", err)
	}
	calls := 0
	if err := client.Stats(context.Background(), protocol.DockerStatsParams{ID: "web"}, func([]byte) error { calls++; return boom }); !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("Stats error = %v after %d sends", err, calls)
	}
}
