// Package collector gathers bounded host telemetry for the Vigira agent.
package collector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	dockercollector "github.com/deranjer/vigara/apps/agent/internal/collector/docker"
	"github.com/deranjer/vigara/apps/agent/internal/networkcheck"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	gopsnet "github.com/shirou/gopsutil/v4/net"
)

const (
	maxDevices              = 64
	minNetworkCheckInterval = 15 * time.Second
)

type counters struct {
	read, write uint64
}

// Collector retains only the immediately previous I/O counters, needed to
// calculate rates without reporting cumulative counters as throughput.
type Collector struct {
	previousDisk        map[string]counters
	previousDiskAt      time.Time
	previousNet         map[string]counters
	previousNetworkAt   time.Time
	networkChecksAt     time.Time
	networkChecks       []networkcheck.Check
	networkCheckResults []networkcheck.Result
	docker              *dockercollector.Collector
	dockerConfigError   error
}

func New() *Collector {
	c := &Collector{}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("VIGIRA_DOCKER_ENABLED")), "true") {
		c.docker, c.dockerConfigError = dockercollector.New(dockercollector.Config{Enabled: true, Host: strings.TrimSpace(os.Getenv("VIGIRA_DOCKER_HOST"))})
	}
	return c
}

// NewWithDockerConfig makes the opt-in boundary explicit for embedders.
func NewWithDockerConfig(enabled bool, host string, client *http.Client) *Collector {
	c := &Collector{}
	if enabled {
		c.docker, c.dockerConfigError = dockercollector.New(dockercollector.Config{Enabled: true, Host: host, HTTPClient: client})
	}
	return c
}

// Collect makes one bounded pass over supported host APIs. Every failed
// capability is represented as an error and produces no fabricated metric.
func (c *Collector) Collect(ctx context.Context) protocol.TelemetrySnapshot {
	snapshot := protocol.TelemetrySnapshot{ProtocolVersion: protocol.CurrentVersion, ObservedAt: time.Now().UTC(), Metrics: []protocol.TelemetryMetric{}, Capabilities: []string{}, Errors: []protocol.CollectionError{}}
	capabilities := map[string]bool{}
	fail := func(capability string, err error) {
		message := truncate(err.Error(), 256)
		snapshot.Errors = append(snapshot.Errors, protocol.CollectionError{Capability: capability, Message: message})
	}
	add := func(capability, name string, value float64, unit string, labels map[string]string) {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return
		}
		if strings.HasSuffix(name, "percent") && value > 100 {
			fail(capability, fmt.Errorf("%s outside 0..100", name))
			return
		}
		for key, label := range labels {
			if key == "" || len(key) > 64 || len(label) > 256 {
				fail("metric_labels", fmt.Errorf("label for %s exceeds collection limits", name))
				return
			}
		}
		snapshot.Metrics = append(snapshot.Metrics, protocol.TelemetryMetric{Name: name, Value: value, Unit: unit, Labels: labels})
		capabilities[capability] = true
	}

	info, err := host.InfoWithContext(ctx)
	if err != nil {
		fail("host_info", err)
	} else {
		snapshot.Host.OS, snapshot.Host.OSVersion = runtime.GOOS, truncate(info.PlatformVersion, 256)
		snapshot.Host.Architecture = runtime.GOARCH
		capabilities["host_info"] = true
	}
	if snapshot.Host.OS == "" {
		snapshot.Host.OS = runtime.GOOS
		snapshot.Host.Architecture = runtime.GOARCH
	}
	if uptime, err := host.UptimeWithContext(ctx); err != nil {
		fail("uptime", err)
	} else {
		snapshot.Host.UptimeSeconds = uptime
		add("uptime", "uptime_seconds", float64(uptime), "s", nil)
		capabilities["uptime"] = true
	}

	if cpus, err := cpu.InfoWithContext(ctx); err != nil {
		fail("cpu_info", err)
	} else {
		if len(cpus) > 0 {
			snapshot.Host.CPUModel = truncate(cpus[0].ModelName, 256)
		} else {
			fail("cpu_info", fmt.Errorf("no CPU information returned"))
		}
		count, countErr := cpu.CountsWithContext(ctx, true)
		if countErr != nil {
			fail("cpu_info", countErr)
		} else {
			snapshot.Host.LogicalCPUs = count
		}
		capabilities["cpu_info"] = true
	}
	if usage, err := mem.VirtualMemoryWithContext(ctx); err != nil {
		fail("memory", err)
	} else {
		snapshot.Host.TotalMemoryByte = usage.Total
		add("memory", "memory_total_bytes", float64(usage.Total), "By", nil)
		add("memory", "memory_available_bytes", float64(usage.Available), "By", nil)
		add("memory", "memory_used_percent", usage.UsedPercent, "%", nil)
	}
	if percentages, err := cpu.PercentWithContext(ctx, 0, false); err != nil {
		fail("cpu_utilization", err)
	} else if len(percentages) > 0 {
		add("cpu_utilization", "cpu_used_percent", percentages[0], "%", nil)
	}

	if partitions, err := disk.PartitionsWithContext(ctx, false); err != nil {
		fail("filesystem_capacity", err)
	} else {
		if len(partitions) == 0 {
			fail("filesystem_capacity", fmt.Errorf("no filesystem partitions returned"))
		}
		if len(partitions) > maxDevices {
			partitions = partitions[:maxDevices]
		}
		for _, part := range partitions {
			if part.Mountpoint == "" {
				continue
			}
			usage, err := disk.UsageWithContext(ctx, part.Mountpoint)
			if err != nil {
				fail("filesystem_capacity", err)
				continue
			}
			labels := map[string]string{"mount_path": part.Mountpoint, "filesystem": part.Fstype}
			add("filesystem_capacity", "filesystem_total_bytes", float64(usage.Total), "By", labels)
			add("filesystem_capacity", "filesystem_free_bytes", float64(usage.Free), "By", labels)
		}
	}

	c.collectDiskRates(ctx, snapshot.ObservedAt, add, fail)
	c.collectNetworkRates(ctx, snapshot.ObservedAt, add, fail)
	checks, checkErr := networkcheck.Load(os.Getenv("VIGIRA_NETWORK_CHECKS_CONFIG"))
	if checkErr != nil {
		fail("network_checks", checkErr)
	} else if len(checks) > 0 {
		if !reflect.DeepEqual(c.networkChecks, checks) || c.networkChecksAt.IsZero() || snapshot.ObservedAt.Sub(c.networkChecksAt) >= minNetworkCheckInterval {
			c.networkCheckResults = networkcheck.Run(ctx, checks)
			c.networkChecksAt = snapshot.ObservedAt
			c.networkChecks = append([]networkcheck.Check(nil), checks...)
		}
		for _, result := range c.networkCheckResults {
			labels := map[string]string{"check_name": result.Name, "check_type": result.Type}
			value := 0.0
			if result.OK {
				value = 1
			}
			add("network_checks", "network_check_success", value, "1", labels)
			if result.Latency > 0 {
				add("network_checks", "network_check_latency_milliseconds", float64(result.Latency)/float64(time.Millisecond), "ms", labels)
			}
		}
	} else {
		c.networkChecks = nil
		c.networkCheckResults = nil
		c.networkChecksAt = time.Time{}
	}
	c.collectDocker(&snapshot, capabilities, fail, ctx)
	for capability := range capabilities {
		snapshot.Capabilities = append(snapshot.Capabilities, capability)
	}
	// Stable ordering makes reports easier to inspect and deterministic in tests.
	sortStrings(snapshot.Capabilities)
	if len(snapshot.Errors) > 128 {
		snapshot.Errors = snapshot.Errors[:128]
	}
	return snapshot
}

func (c *Collector) collectDocker(snapshot *protocol.TelemetrySnapshot, capabilities map[string]bool, fail func(string, error), ctx context.Context) {
	if c.dockerConfigError != nil {
		fail("docker_inventory", c.dockerConfigError)
		return
	}
	if c.docker == nil {
		return
	}
	result := c.docker.Collect(ctx)
	for _, metric := range result.Metrics {
		if len(snapshot.Metrics) < 512 {
			snapshot.Metrics = append(snapshot.Metrics, metric)
		} else {
			fail("docker_container_resources", errors.New("snapshot metric limit reached; Docker metrics omitted"))
			break
		}
	}
	snapshot.DockerContainers = result.Containers
	for _, capability := range result.Capabilities {
		capabilities[capability] = true
	}
	for _, collectionError := range result.Errors {
		fail(collectionError.Capability, errors.New(collectionError.Message))
	}
}

func (c *Collector) collectDiskRates(ctx context.Context, now time.Time, add func(string, string, float64, string, map[string]string), fail func(string, error)) {
	current, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		fail("disk_throughput", err)
		return
	}
	if len(current) == 0 {
		fail("disk_throughput", fmt.Errorf("no disk counters returned"))
		return
	}
	if len(current) > maxDevices {
		current = firstDiskCounters(current, maxDevices)
	}
	if !c.previousDiskAt.IsZero() {
		seconds := now.Sub(c.previousDiskAt).Seconds()
		if seconds > 0 {
			for name, value := range current {
				if previous, ok := c.previousDisk[name]; ok {
					labels := map[string]string{"device": name}
					if value, ok := rate(value.ReadBytes, previous.read, now.Sub(c.previousDiskAt)); ok {
						add("disk_throughput", "disk_read_bytes_per_second", value, "By/s", labels)
					}
					if value, ok := rate(value.WriteBytes, previous.write, now.Sub(c.previousDiskAt)); ok {
						add("disk_throughput", "disk_write_bytes_per_second", value, "By/s", labels)
					}
				}
			}
		}
	}
	c.previousDisk = make(map[string]counters, len(current))
	for name, value := range current {
		c.previousDisk[name] = counters{value.ReadBytes, value.WriteBytes}
	}
	c.previousDiskAt = now
}

func (c *Collector) collectNetworkRates(ctx context.Context, now time.Time, add func(string, string, float64, string, map[string]string), fail func(string, error)) {
	current, err := gopsnet.IOCountersWithContext(ctx, true)
	if err != nil {
		fail("network_throughput", err)
		return
	}
	if len(current) == 0 {
		fail("network_throughput", fmt.Errorf("no network interfaces returned"))
		return
	}
	if len(current) > maxDevices {
		current = current[:maxDevices]
	}
	if !c.previousNetworkAt.IsZero() {
		seconds := now.Sub(c.previousNetworkAt).Seconds()
		if seconds > 0 {
			for _, value := range current {
				if previous, ok := c.previousNet[value.Name]; ok {
					labels := map[string]string{"interface": value.Name}
					if rateValue, ok := rate(value.BytesRecv, previous.read, now.Sub(c.previousNetworkAt)); ok {
						add("network_throughput", "network_receive_bytes_per_second", rateValue, "By/s", labels)
					}
					if rateValue, ok := rate(value.BytesSent, previous.write, now.Sub(c.previousNetworkAt)); ok {
						add("network_throughput", "network_send_bytes_per_second", rateValue, "By/s", labels)
					}
				}
			}
		}
	}
	c.previousNet = make(map[string]counters, len(current))
	for _, value := range current {
		c.previousNet[value.Name] = counters{value.BytesRecv, value.BytesSent}
	}
	c.previousNetworkAt = now
}

func rate(current, previous uint64, elapsed time.Duration) (float64, bool) {
	if elapsed <= 0 || current < previous {
		return 0, false
	}
	return float64(current-previous) / elapsed.Seconds(), true
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func firstDiskCounters(source map[string]disk.IOCountersStat, count int) map[string]disk.IOCountersStat {
	result := make(map[string]disk.IOCountersStat, count)
	for name, value := range source {
		if len(result) == count {
			break
		}
		result[name] = value
	}
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
