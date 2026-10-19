// Package docker reads bounded container inventory and resource statistics
// from an explicitly configured Docker Engine API endpoint. It only issues GETs.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deranjer/vigara/internal/modules"
	"github.com/deranjer/vigara/internal/protocol"
)

const (
	ModuleID         = "docker_inventory"
	maxContainers    = 32
	maxResponseBytes = 1 << 20
	apiVersion       = "v1.44"
)

// Config is opt-in. Host accepts unix:///absolute/socket/path or a loopback
// HTTP URL, normally pointing to a restricted read-only API proxy.
type Config struct {
	Enabled    bool
	Host       string
	HTTPClient *http.Client // optional client injection for controlled proxies and tests
}

type sample struct {
	cpu, system, rx, tx uint64
	hasCPU, hasNetwork  bool
	networkInterfaces   string
	at                  time.Time
}
type Collector struct {
	cfg      Config
	client   *http.Client
	baseURL  string
	previous map[string]sample
}
type Result struct {
	Metrics      []protocol.TelemetryMetric
	Containers   []protocol.DockerContainer
	Capabilities []string
	Errors       []protocol.CollectionError
}

func New(cfg Config) (*Collector, error) {
	c := &Collector{cfg: cfg}
	if !cfg.Enabled {
		return c, nil
	}
	base, socket, err := endpoint(cfg.Host)
	if err != nil {
		return nil, err
	}
	c.baseURL = base
	if cfg.HTTPClient != nil {
		c.client = cfg.HTTPClient
		return c, nil
	}
	transport := &http.Transport{ResponseHeaderTimeout: 2 * time.Second, IdleConnTimeout: 5 * time.Second}
	if socket != "" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		}
	}
	c.client = &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

func (c *Collector) Descriptor() modules.Descriptor {
	return modules.Descriptor{
		ID: ModuleID, Version: "1.0.0", Platforms: []string{"linux", "darwin", "windows"},
		Capabilities:  []string{"docker_inventory", "docker_container_resources"},
		Configuration: []modules.ConfigField{{Name: "VIGIRA_DOCKER_ENABLED", Type: "boolean", Description: "Explicit local opt-in; disabled by default"}, {Name: "VIGIRA_DOCKER_HOST", Type: "string", Required: true, Description: "Explicit unix socket or loopback API proxy endpoint"}},
		Permissions:   []modules.PermissionSpec{{Name: "docker.read", Description: "Read inventory and resource stats from the locally configured endpoint"}},
		Metrics:       []modules.MetricSpec{{Name: "docker_memory_usage_bytes", Unit: "By"}, {Name: "docker_memory_limit_bytes", Unit: "By"}, {Name: "docker_memory_used_percent", Unit: "%"}, {Name: "docker_cpu_capacity_used_percent", Unit: "%"}, {Name: "docker_processes", Unit: "1"}, {Name: "docker_network_receive_bytes_per_second", Unit: "By/s"}, {Name: "docker_network_transmit_bytes_per_second", Unit: "By/s"}},
	}
}

// Execute is read-only and accepts no endpoint or credentials from job settings;
// the endpoint comes only from local agent configuration.
func (c *Collector) Execute(ctx context.Context, job modules.JobContext) (modules.Result, error) {
	if !c.cfg.Enabled {
		return modules.Result{}, errors.New("Docker module is disabled by local configuration")
	}
	granted := false
	for _, p := range job.Permissions {
		if p.Name == "docker.read" && p.Scope == "configured-endpoint" {
			granted = true
			break
		}
	}
	if !granted {
		return modules.Result{}, errors.New("Docker module requires explicit docker.read permission for the configured endpoint")
	}
	result := c.Collect(ctx)
	if len(result.Errors) > 0 && len(result.Containers) == 0 {
		return modules.Result{}, errors.New(result.Errors[0].Message)
	}
	metrics := make([]modules.Metric, 0, len(result.Metrics))
	observedAt := time.Now().UTC()
	for _, metric := range result.Metrics {
		metrics = append(metrics, modules.Metric{Name: metric.Name, Value: metric.Value, Unit: metric.Unit, Labels: metric.Labels, ObservedAt: observedAt})
	}
	return modules.Result{Output: map[string]any{"containers": result.Containers, "capabilities": result.Capabilities, "errors": result.Errors}, Metrics: metrics}, nil
}

var _ modules.Module = (*Collector)(nil)

// Endpoint validates a configured Docker host and returns the base URL and, for a
// unix socket, its path. Other Docker clients in the agent share these rules.
func Endpoint(host string) (baseURL, socket string, err error) { return endpoint(host) }

func endpoint(host string) (string, string, error) {
	if strings.HasPrefix(host, "unix://") {
		path := strings.TrimPrefix(host, "unix://")
		if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
			return "", "", errors.New("Docker unix endpoint must be an absolute socket path")
		}
		return "http://docker", path, nil
	}
	u, err := url.Parse(host)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("Docker host must be unix:///path or a loopback http:// IP endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", "", errors.New("Docker HTTP endpoint must use a loopback IP address")
	}
	return strings.TrimRight(host, "/"), "", nil
}

type container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}
type cpuStat struct {
	Total  *uint64  `json:"total_usage"`
	PerCPU []uint64 `json:"percpu_usage"`
}
type cpuStats struct {
	Usage  cpuStat `json:"cpu_usage"`
	System *uint64 `json:"system_cpu_usage"`
	Online int     `json:"online_cpus"`
}
type memStats struct {
	Usage *uint64 `json:"usage"`
	Limit *uint64 `json:"limit"`
}
type networkStat struct {
	Rx *uint64 `json:"rx_bytes"`
	Tx *uint64 `json:"tx_bytes"`
}
type stats struct {
	CPU    cpuStats `json:"cpu_stats"`
	Memory memStats `json:"memory_stats"`
	PIDs   struct {
		Current *uint64 `json:"current"`
	} `json:"pids_stats"`
	Networks map[string]networkStat `json:"networks"`
}

func (c *Collector) Collect(ctx context.Context) Result {
	r := Result{Metrics: []protocol.TelemetryMetric{}, Containers: []protocol.DockerContainer{}, Capabilities: []string{}, Errors: []protocol.CollectionError{}}
	if !c.cfg.Enabled {
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fail := func(capability string, err error) {
		if len(r.Errors) < 32 {
			r.Errors = append(r.Errors, protocol.CollectionError{Capability: capability, Message: truncate(err.Error(), 256)})
		}
	}
	var containers []container
	if err := c.get(ctx, "/containers/json?all=1", &containers); err != nil {
		fail("docker_inventory", err)
		return r
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })
	if len(containers) > maxContainers {
		fail("docker_inventory", fmt.Errorf("container list capped at %d entries", maxContainers))
		containers = containers[:maxContainers]
	}
	currentSamples := make(map[string]sample, len(containers))
	for _, item := range containers {
		if !safeID(item.ID) {
			fail("docker_inventory", errors.New("Docker returned an invalid container ID"))
			continue
		}
		if !validState(item.State) {
			fail("docker_inventory", errors.New("Docker returned an unsupported container state"))
			continue
		}
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		state := truncate(item.State, 32)
		r.Containers = append(r.Containers, protocol.DockerContainer{ID: item.ID, Name: truncate(name, 128), Image: truncate(item.Image, 128), State: state})
		if item.State != "running" {
			continue
		}
		var s stats
		if err := c.get(ctx, "/containers/"+url.PathEscape(item.ID)+"/stats?stream=false&one-shot=true", &s); err != nil {
			fail("docker_container_resources", fmt.Errorf("container %s: %w", shortID(item.ID), err))
			continue
		}
		labels := map[string]string{"container_id": item.ID}
		add := func(name string, value float64, unit string) {
			if value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) && (!strings.HasSuffix(name, "percent") || value <= 100) {
				r.Metrics = append(r.Metrics, protocol.TelemetryMetric{Name: name, Value: value, Unit: unit, Labels: labels})
			}
		}
		current := sample{at: time.Now().UTC()}
		if s.Memory.Usage != nil {
			add("docker_memory_usage_bytes", float64(*s.Memory.Usage), "By")
		}
		if s.Memory.Limit != nil && *s.Memory.Limit > 0 {
			add("docker_memory_limit_bytes", float64(*s.Memory.Limit), "By")
		}
		if s.Memory.Usage != nil && s.Memory.Limit != nil && *s.Memory.Limit > 0 {
			add("docker_memory_used_percent", 100*float64(*s.Memory.Usage)/float64(*s.Memory.Limit), "%")
		}
		if s.CPU.Usage.Total != nil && s.CPU.System != nil {
			current.cpu = *s.CPU.Usage.Total
			current.system = *s.CPU.System
			current.hasCPU = true
		}
		if previous, ok := c.previous[item.ID]; ok && previous.hasCPU && current.hasCPU && current.cpu >= previous.cpu && current.system > previous.system {
			// system_cpu_usage already accumulates across host CPUs, so this
			// ratio is percent of total host CPU capacity (bounded at 100).
			add("docker_cpu_capacity_used_percent", 100*float64(current.cpu-previous.cpu)/float64(current.system-previous.system), "%")
		}
		if s.PIDs.Current != nil {
			add("docker_processes", float64(*s.PIDs.Current), "1")
		}
		if len(s.Networks) > 0 {
			current.hasNetwork = true
			interfaces := make([]string, 0, len(s.Networks))
			for name, n := range s.Networks {
				interfaces = append(interfaces, name)
				if n.Rx == nil || n.Tx == nil {
					current.hasNetwork = false
					break
				}
				if ^uint64(0)-current.rx < *n.Rx || ^uint64(0)-current.tx < *n.Tx {
					current.hasNetwork = false
					break
				}
				current.rx += *n.Rx
				current.tx += *n.Tx
			}
			sort.Strings(interfaces)
			current.networkInterfaces = strings.Join(interfaces, ",")
		}
		if previous, ok := c.previous[item.ID]; ok && previous.hasNetwork && current.hasNetwork && previous.networkInterfaces == current.networkInterfaces && current.at.Sub(previous.at) > 0 {
			seconds := current.at.Sub(previous.at).Seconds()
			if current.rx >= previous.rx {
				add("docker_network_receive_bytes_per_second", float64(current.rx-previous.rx)/seconds, "By/s")
			}
			if current.tx >= previous.tx {
				add("docker_network_transmit_bytes_per_second", float64(current.tx-previous.tx)/seconds, "By/s")
			}
		}
		currentSamples[item.ID] = current
	}
	c.previous = currentSamples
	r.Capabilities = append(r.Capabilities, "docker_inventory")
	if len(r.Metrics) > 0 {
		r.Capabilities = appendUnique(r.Capabilities, "docker_container_resources")
	}
	return r
}

func (c *Collector) get(ctx context.Context, path string, dst any) error {
	return c.getLimit(ctx, path, dst, maxResponseBytes)
}

func (c *Collector) getLimit(ctx context.Context, path string, dst any, maxBytes int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+apiVersion+path, nil)
	if err != nil {
		return errors.New("could not build Docker API request")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("Docker API request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("Docker API returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return errors.New("could not read Docker API response")
	}
	if int64(len(data)) > maxBytes {
		return errors.New("Docker API response exceeds configured limit")
	}
	if json.Unmarshal(data, dst) != nil {
		return errors.New("Docker API returned invalid JSON")
	}
	return nil
}

func safeID(v string) bool {
	if len(v) < 12 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func validState(v string) bool {
	switch v {
	case "created", "restarting", "running", "removing", "paused", "exited", "dead":
		return true
	default:
		return false
	}
}
func shortID(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func appendUnique(values []string, v string) []string {
	for _, x := range values {
		if x == v {
			return values
		}
	}
	return append(values, v)
}
