// Package dockerapi is the agent's client for the local Docker Engine API. The
// read methods in this file (container lists, curated inspect, log and statistics
// streams) only issue GET requests. The methods in write.go change the host; the
// agent registers them only at the manage tier. Every container reference is
// validated and every response bounded, so a compromised server cannot use the
// client to reach anything beyond its fixed set of operations.
package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	dockercollector "github.com/deranjer/vigara/apps/agent/internal/collector/docker"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	apiVersion        = "v1.44"
	requestTimeout    = 15 * time.Second
	maxListBytes      = 8 << 20
	maxInspectBytes   = 2 << 20
	flushInterval     = 150 * time.Millisecond
	maxBatchBytes     = protocol.MaxStreamChunkBytes - 1024
	maxEncodedLogLine = 24 << 10
)

// Errors callers map to protocol error codes.
var (
	ErrNotFound   = errors.New("container not found")
	ErrBadRequest = errors.New("invalid container reference")
)

var containerRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Client talks to one explicitly configured Docker endpoint.
type Client struct {
	base string
	http *http.Client
	// dial opens a raw connection to the Docker endpoint for requests that upgrade to a stream.
	dial func(ctx context.Context) (net.Conn, error)
}

// New connects to host (unix:///path or a loopback http:// IP), exactly as the
// collector accepts it. A nil httpClient builds the standard transport.
func New(host string, httpClient *http.Client) (*Client, error) {
	base, socket, err := dockercollector.Endpoint(host)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		// No response-header timeout: stopping a container that ignores SIGTERM only answers after
		// Docker's grace period (up to 300 s), and every request already carries its own deadline.
		transport := &http.Transport{IdleConnTimeout: 30 * time.Second}
		if socket != "" {
			transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
			}
		}
		httpClient = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	c := &Client{base: base, http: httpClient}
	if socket != "" {
		c.dial = func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		}
	} else {
		address := strings.TrimPrefix(strings.TrimRight(base, "/"), "http://")
		c.dial = func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		}
	}
	return c, nil
}

// APIError is a refusal from the Docker Engine: Status is its HTTP status and
// Message the explanation it gave, which Docker words for operators and which
// carries no credentials.
type APIError struct {
	Status  int
	Message string
}

// Is lets callers keep testing errors.Is(err, ErrNotFound) for a 404 while still
// seeing Docker's explanation, which for a missing image says why it could not be pulled.
func (e *APIError) Is(target error) bool { return target == ErrNotFound && e.Status == http.StatusNotFound }

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("docker returned HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("docker returned HTTP %d", e.Status)
}

// do issues one request. A 304 (Docker's "already in that state") is returned as a
// response; 404 becomes ErrNotFound and any other failure an *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	endpoint := c.base + "/" + apiVersion + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotModified || resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	var failure struct {
		Message string `json:"message"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	_ = json.Unmarshal(data, &failure)
	if len(failure.Message) > 300 {
		failure.Message = failure.Message[:300]
	}
	return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(failure.Message)}
}

// open issues a GET; every read method goes through it.
func (c *Client) open(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, path, query, nil)
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, limit int64, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.open(ctx, path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("docker response exceeds the size limit")
	}
	return json.Unmarshal(data, out)
}

func checkRef(id string) error {
	if !containerRef.MatchString(id) {
		return ErrBadRequest
	}
	return nil
}

type apiPort struct {
	IP          string `json:"IP"`
	PrivatePort uint16 `json:"PrivatePort"`
	PublicPort  uint16 `json:"PublicPort"`
	Type        string `json:"Type"`
}

// List returns every container, running or not, ordered by name.
func (c *Client) List(ctx context.Context) (protocol.DockerListResult, error) {
	var raw []struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		State   string            `json:"State"`
		Status  string            `json:"Status"`
		Created int64             `json:"Created"`
		Ports   []apiPort         `json:"Ports"`
		Labels  map[string]string `json:"Labels"`
	}
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, maxListBytes, &raw); err != nil {
		return protocol.DockerListResult{}, err
	}
	result := protocol.DockerListResult{Containers: make([]protocol.DockerContainerInfo, 0, len(raw))}
	for _, item := range raw {
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		info := protocol.DockerContainerInfo{ID: item.ID, Name: name, Image: item.Image, State: item.State, Status: item.Status, Created: item.Created,
			Project: item.Labels["com.docker.compose.project"], Service: item.Labels["com.docker.compose.service"]}
		for _, port := range item.Ports {
			info.Ports = append(info.Ports, protocol.DockerPort{IP: port.IP, PrivatePort: port.PrivatePort, PublicPort: port.PublicPort, Type: port.Type})
		}
		result.Containers = append(result.Containers, info)
	}
	sort.Slice(result.Containers, func(i, j int) bool {
		a, b := result.Containers[i], result.Containers[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	if len(result.Containers) > protocol.MaxDockerContainers {
		result.Containers, result.Truncated = result.Containers[:protocol.MaxDockerContainers], true
	}
	return result, nil
}

type inspectResponse struct {
	ID           string `json:"Id"`
	Created      string `json:"Created"`
	Path         string `json:"Path"`
	Args         []string
	Image        string `json:"Image"`
	Name         string `json:"Name"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		Running    bool
		Paused     bool
		Restarting bool
		OOMKilled  bool
		Pid        int
		ExitCode   int
		Error      string
		StartedAt  string
		FinishedAt string
		Health     *struct{ Status string }
	} `json:"State"`
	HostConfig struct {
		NetworkMode    string
		Privileged     bool
		ReadonlyRootfs bool
		Memory         int64
		NanoCpus       int64
		CapAdd         []string
		CapDrop        []string
		RestartPolicy  struct {
			Name              string
			MaximumRetryCount int
		}
		LogConfig struct{ Type string }
	} `json:"HostConfig"`
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	} `json:"Mounts"`
	Config struct {
		Hostname   string
		User       string
		Tty        bool
		Env        []string
		Cmd        []string
		Image      string
		WorkingDir string
		Entrypoint []string
		Labels     map[string]string
	} `json:"Config"`
	NetworkSettings struct {
		Ports    map[string][]struct{ HostIp, HostPort string }
		Networks map[string]struct {
			Aliases    []string
			Gateway    string
			IPAddress  string
			MacAddress string
		}
	} `json:"NetworkSettings"`
	ImageID string `json:"ImageID"`
}

// Inspect returns a curated view of one container. Environment values are blank
// unless revealEnv is set.
func (c *Client) Inspect(ctx context.Context, id string, revealEnv bool) (protocol.DockerInspectResult, error) {
	if err := checkRef(id); err != nil {
		return protocol.DockerInspectResult{}, err
	}
	var raw inspectResponse
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, maxInspectBytes, &raw); err != nil {
		return protocol.DockerInspectResult{}, err
	}
	out := protocol.DockerInspectResult{
		ID: raw.ID, Name: strings.TrimPrefix(raw.Name, "/"), Image: raw.Config.Image, ImageID: raw.Image, Created: raw.Created,
		State: protocol.DockerState{Status: raw.State.Status, Running: raw.State.Running, Paused: raw.State.Paused, Restarting: raw.State.Restarting,
			OOMKilled: raw.State.OOMKilled, ExitCode: raw.State.ExitCode, Error: raw.State.Error, StartedAt: zeroTime(raw.State.StartedAt), FinishedAt: zeroTime(raw.State.FinishedAt), Pid: raw.State.Pid},
		Command: append([]string{raw.Path}, raw.Args...), Entrypoint: raw.Config.Entrypoint, WorkingDir: raw.Config.WorkingDir, User: raw.Config.User,
		Hostname: raw.Config.Hostname, TTY: raw.Config.Tty, Env: []protocol.DockerEnvVar{}, EnvRevealed: revealEnv, Labels: raw.Config.Labels,
		RestartPolicy: raw.HostConfig.RestartPolicy.Name, NetworkMode: raw.HostConfig.NetworkMode, Privileged: raw.HostConfig.Privileged,
		ReadonlyRoot: raw.HostConfig.ReadonlyRootfs, MemoryLimit: raw.HostConfig.Memory, NanoCPUs: raw.HostConfig.NanoCpus,
		CapAdd: raw.HostConfig.CapAdd, CapDrop: raw.HostConfig.CapDrop, LogDriver: raw.HostConfig.LogConfig.Type, RestartCount: raw.RestartCount,
	}
	if raw.State.Health != nil {
		out.State.Health = raw.State.Health.Status
	}
	if len(raw.Config.Cmd) == 0 && raw.Path == "" {
		out.Command = nil
	}
	for _, entry := range raw.Config.Env {
		name, value, _ := strings.Cut(entry, "=")
		item := protocol.DockerEnvVar{Name: name}
		if revealEnv {
			item.Value = value
		}
		out.Env = append(out.Env, item)
	}
	sort.Slice(out.Env, func(i, j int) bool { return out.Env[i].Name < out.Env[j].Name })
	for _, mount := range raw.Mounts {
		out.Mounts = append(out.Mounts, protocol.DockerMount{Type: mount.Type, Name: mount.Name, Source: mount.Source, Destination: mount.Destination, ReadWrite: mount.RW})
	}
	for key, bindings := range raw.NetworkSettings.Ports {
		portNumber, proto, _ := strings.Cut(key, "/")
		private, err := strconv.ParseUint(portNumber, 10, 16)
		if err != nil {
			continue
		}
		if len(bindings) == 0 {
			out.Ports = append(out.Ports, protocol.DockerPort{PrivatePort: uint16(private), Type: proto})
			continue
		}
		for _, binding := range bindings {
			public, _ := strconv.ParseUint(binding.HostPort, 10, 16)
			out.Ports = append(out.Ports, protocol.DockerPort{IP: binding.HostIp, PrivatePort: uint16(private), PublicPort: uint16(public), Type: proto})
		}
	}
	sort.Slice(out.Ports, func(i, j int) bool {
		a, b := out.Ports[i], out.Ports[j]
		if a.PrivatePort != b.PrivatePort {
			return a.PrivatePort < b.PrivatePort
		}
		if a.PublicPort != b.PublicPort {
			return a.PublicPort < b.PublicPort
		}
		return a.IP < b.IP
	})
	for name, network := range raw.NetworkSettings.Networks {
		out.Networks = append(out.Networks, protocol.DockerNetworkAttachment{Name: name, IPAddress: network.IPAddress, Gateway: network.Gateway, MacAddress: network.MacAddress, Aliases: network.Aliases})
	}
	sort.Slice(out.Networks, func(i, j int) bool { return out.Networks[i].Name < out.Networks[j].Name })
	return out, nil
}

// zeroTime hides Docker's placeholder for "never".
func zeroTime(value string) string {
	if strings.HasPrefix(value, "0001-01-01") {
		return ""
	}
	return value
}

// batcher collects newline-delimited JSON and sends it in chunks that always end
// on a line boundary and never exceed one channel chunk, so a consumer can split
// each chunk on newlines without reassembly.
type batcher struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	send func([]byte) error
	err  error
}

func (b *batcher) add(line []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if b.buf.Len()+len(line)+1 > maxBatchBytes {
		b.flushLocked()
	}
	b.buf.Write(line)
	b.buf.WriteByte('\n')
	return b.err
}

func (b *batcher) flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
	return b.err
}

func (b *batcher) flushLocked() {
	if b.buf.Len() == 0 || b.err != nil {
		return
	}
	b.err = b.send(append([]byte(nil), b.buf.Bytes()...))
	b.buf.Reset()
}

// Logs streams a container's log lines as newline-delimited DockerLogLine JSON.
// It returns when the log ends (not following), the context ends, or sending fails.
func (c *Client) Logs(ctx context.Context, params protocol.DockerLogsParams, send func([]byte) error) error {
	if err := checkRef(params.ID); err != nil {
		return err
	}
	tail := params.Tail
	if tail <= 0 {
		tail = protocol.DefaultLogTail
	}
	if tail > protocol.MaxLogTail {
		tail = protocol.MaxLogTail
	}
	var meta struct {
		Config struct{ Tty bool }
	}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(params.ID)+"/json", nil, maxInspectBytes, &meta); err != nil {
		return err
	}
	query := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)}}
	if params.Follow {
		query.Set("follow", "1")
	}
	if params.Timestamps {
		query.Set("timestamps", "1")
	}
	if params.Since != "" {
		if _, err := time.Parse(time.RFC3339Nano, params.Since); err != nil {
			return ErrBadRequest
		}
		query.Set("since", params.Since)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resp, err := c.open(ctx, "/containers/"+url.PathEscape(params.ID)+"/logs", query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out := &batcher{send: send}
	emit := func(stream, text string, truncated bool) error {
		line := protocol.DockerLogLine{S: stream, M: text, X: truncated}
		if params.Timestamps {
			if stamp, rest, ok := strings.Cut(text, " "); ok && len(stamp) >= 20 && strings.Contains(stamp, "T") {
				line.T, line.M = stamp, rest
			}
		}
		encoded, _ := json.Marshal(line)
		for len(encoded) > maxEncodedLogLine && len(line.M) > 1 {
			line.M, line.X = trimUTF8(line.M, len(line.M)/2), true
			encoded, _ = json.Marshal(line)
		}
		return out.add(encoded)
	}
	// A ticker flushes lines promptly while following; it stops with the stream.
	done := make(chan struct{})
	var ticks sync.WaitGroup
	ticks.Add(1)
	go func() {
		defer ticks.Done()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if out.flush() != nil {
					cancel()
					return
				}
			}
		}
	}()
	readErr := pumpLogs(resp.Body, meta.Config.Tty, emit)
	close(done)
	ticks.Wait()
	flushErr := out.flush()
	if flushErr != nil {
		return flushErr
	}
	if readErr != nil && ctx.Err() == nil {
		return readErr
	}
	return nil
}

// pumpLogs demultiplexes Docker's log framing (or reads raw lines for a TTY) and
// calls emit once per line.
func pumpLogs(body io.Reader, tty bool, emit func(stream, text string, truncated bool) error) error {
	assemblers := map[string]*lineAssembler{"o": {stream: "o", emit: emit}, "e": {stream: "e", emit: emit}}
	reader := bufio.NewReaderSize(body, 64<<10)
	if tty {
		return assemblers["o"].feedAll(reader)
	}
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:8]))
		stream := "o"
		if header[0] == 2 {
			stream = "e"
		}
		if size > 1<<24 {
			return errors.New("docker log frame is too large")
		}
		payload := io.LimitReader(reader, size)
		if err := assemblers[stream].feedAll(payload); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, payload); err != nil {
			return err
		}
	}
	for _, a := range assemblers {
		if err := a.finish(); err != nil {
			return err
		}
	}
	return nil
}

// lineAssembler turns arbitrary byte chunks into whole lines.
type lineAssembler struct {
	stream  string
	buf     []byte
	skip    bool // discarding the remainder of an over-long line
	emit    func(stream, text string, truncated bool) error
	pending bool
}

func (a *lineAssembler) feedAll(r io.Reader) error {
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			if ferr := a.feed(chunk[:n]); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (a *lineAssembler) feed(data []byte) error {
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			if !a.skip {
				a.buf = append(a.buf, data...)
				if len(a.buf) > protocol.MaxLogLineBytes {
					text := trimUTF8(string(a.buf), protocol.MaxLogLineBytes)
					a.buf, a.skip = a.buf[:0], true
					if err := a.emit(a.stream, strings.TrimSuffix(text, "\r"), true); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if a.skip {
			a.skip = false
		} else {
			a.buf = append(a.buf, data[:index]...)
			text := string(a.buf)
			truncated := false
			if len(text) > protocol.MaxLogLineBytes {
				text, truncated = trimUTF8(text, protocol.MaxLogLineBytes), true
			}
			if err := a.emit(a.stream, strings.TrimSuffix(text, "\r"), truncated); err != nil {
				return err
			}
		}
		a.buf = a.buf[:0]
		data = data[index+1:]
	}
	return nil
}

func (a *lineAssembler) finish() error {
	if len(a.buf) == 0 || a.skip {
		return nil
	}
	text := string(a.buf)
	a.buf = a.buf[:0]
	return a.emit(a.stream, strings.TrimSuffix(text, "\r"), false)
}

func trimUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

type statsResponse struct {
	Read     string `json:"read"`
	PidsStat struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytes []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  int    `json:"online_cpus"`
}

// Stats streams live resource readings as newline-delimited DockerStatsSample
// JSON, roughly one per second, until the context ends or the container stops.
func (c *Client) Stats(ctx context.Context, params protocol.DockerStatsParams, send func([]byte) error) error {
	if err := checkRef(params.ID); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resp, err := c.open(ctx, "/containers/"+url.PathEscape(params.ID)+"/stats", url.Values{"stream": {"1"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<40))
	var previous *protocol.DockerStatsSample
	var previousAt time.Time
	for {
		var raw statsResponse
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		sample, at := sampleFrom(raw)
		if previous != nil && !at.IsZero() && !previousAt.IsZero() {
			if seconds := at.Sub(previousAt).Seconds(); seconds > 0 {
				if sample.NetRx >= previous.NetRx {
					rate := float64(sample.NetRx-previous.NetRx) / seconds
					sample.NetRxBps = &rate
				}
				if sample.NetTx >= previous.NetTx {
					rate := float64(sample.NetTx-previous.NetTx) / seconds
					sample.NetTxBps = &rate
				}
			}
		}
		previous, previousAt = &sample, at
		encoded, _ := json.Marshal(sample)
		if err := send(append(encoded, '\n')); err != nil {
			return err
		}
	}
}

func sampleFrom(raw statsResponse) (protocol.DockerStatsSample, time.Time) {
	at, _ := time.Parse(time.RFC3339Nano, raw.Read)
	cores := raw.CPUStats.OnlineCPUs
	if cores == 0 {
		cores = len(raw.CPUStats.CPUUsage.PercpuUsage)
	}
	if cores == 0 {
		cores = 1
	}
	sample := protocol.DockerStatsSample{T: raw.Read, CPUCores: cores, MemLimit: raw.MemoryStats.Limit, Pids: raw.PidsStat.Current}
	if cpuDelta, systemDelta := float64(raw.CPUStats.CPUUsage.TotalUsage)-float64(raw.PreCPUStats.CPUUsage.TotalUsage), float64(raw.CPUStats.SystemUsage)-float64(raw.PreCPUStats.SystemUsage); cpuDelta >= 0 && systemDelta > 0 {
		sample.CPU = cpuDelta / systemDelta * float64(cores) * 100
	}
	used := raw.MemoryStats.Usage
	reclaimable := raw.MemoryStats.Stats["inactive_file"]
	if reclaimable == 0 {
		reclaimable = raw.MemoryStats.Stats["total_inactive_file"]
	}
	if used >= reclaimable {
		used -= reclaimable
	}
	sample.MemUsed = used
	if sample.MemLimit > 0 {
		sample.MemPct = float64(used) / float64(sample.MemLimit) * 100
	}
	for _, network := range raw.Networks {
		sample.NetRx += network.RxBytes
		sample.NetTx += network.TxBytes
	}
	for _, entry := range raw.BlkioStats.IOServiceBytes {
		switch strings.ToLower(entry.Op) {
		case "read":
			sample.BlkRead += entry.Value
		case "write":
			sample.BlkWrite += entry.Value
		}
	}
	return sample, at
}
