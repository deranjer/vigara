package collector

import (
	"context"
	"fmt"
	"github.com/deranjer/vigara/apps/agent/internal/collector/synology"
	"github.com/deranjer/vigara/internal/protocol"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRateRequiresBaselineAndMonotonicCounters(t *testing.T) {
	if _, ok := rate(500, 0, 0); ok {
		t.Fatal("reported a throughput rate without an elapsed baseline")
	}
	if _, ok := rate(499, 500, time.Second); ok {
		t.Fatal("reported a rate after a counter reset")
	}
	got, ok := rate(1_500, 500, 2*time.Second)
	if !ok || got != 500 {
		t.Fatalf("rate = %v, %v; want 500 bytes/second", got, ok)
	}
}

func TestDockerCollectionIsOptInAndAppearsInSnapshotWhenConfigured(t *testing.T) {
	t.Setenv("VIGIRA_DOCKER_ENABLED", "")
	t.Setenv("VIGIRA_DOCKER_HOST", "")
	disabled := New()
	disabledSnapshot := &protocol.TelemetrySnapshot{Metrics: []protocol.TelemetryMetric{}}
	disabled.collectDocker(disabledSnapshot, map[string]bool{}, func(string, error) { t.Fatal("disabled collector emitted a Docker error") }, context.Background())
	if disabledSnapshot.DockerContainers != nil || len(disabledSnapshot.Metrics) != 0 {
		t.Fatal("disabled Docker module added inventory or metrics to the snapshot")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/v1.44/containers/json") {
			t.Errorf("unexpected fixture request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `[{"Id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Names":["/fixture"],"Image":"fixture:1","State":"exited"}]`)
	}))
	defer server.Close()
	enabledSnapshot := &protocol.TelemetrySnapshot{Metrics: []protocol.TelemetryMetric{}}
	enabledCapabilities := map[string]bool{}
	NewWithDockerConfig(true, server.URL, server.Client()).collectDocker(enabledSnapshot, enabledCapabilities, func(capability string, err error) { t.Fatalf("%s collection failed: %v", capability, err) }, context.Background())
	if len(enabledSnapshot.DockerContainers) != 1 || enabledSnapshot.DockerContainers[0].Name != "fixture" {
		t.Fatalf("configured Docker inventory missing: %+v", enabledSnapshot.DockerContainers)
	}
	if !enabledCapabilities["docker_inventory"] {
		t.Fatalf("Docker capability missing: %v", enabledCapabilities)
	}
}

func TestCollectReturnsBoundedFiniteMeasurements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	snapshot := New().Collect(ctx)
	if snapshot.ObservedAt.IsZero() || snapshot.Host.OS == "" || snapshot.Host.Architecture == "" {
		t.Fatalf("missing observation or host identity: %+v", snapshot)
	}
	if snapshot.HardwareInventory == nil || snapshot.HardwareInventory.ObservedAt.IsZero() || snapshot.HardwareInventory.CPUStatus == "" || snapshot.HardwareInventory.GPUStatus == "" || snapshot.HardwareInventory.MemoryStatus == "" || snapshot.HardwareInventory.BoardStatus == "" || snapshot.HardwareInventory.DiskStatus == "" {
		t.Fatalf("hardware inventory must report an observation and explicit section statuses: %+v", snapshot.HardwareInventory)
	}
	if len(snapshot.HardwareInventory.GPUs) > 8 || len(snapshot.HardwareInventory.MemoryModules) > 64 || len(snapshot.HardwareInventory.Disks) > 32 {
		t.Fatalf("hardware inventory exceeded item bounds: %+v", snapshot.HardwareInventory)
	}
	if len(snapshot.Metrics) > 512 || len(snapshot.Errors) > 128 || len(snapshot.Capabilities) > 128 {
		t.Fatalf("collector exceeded wire bounds: metrics=%d errors=%d capabilities=%d", len(snapshot.Metrics), len(snapshot.Errors), len(snapshot.Capabilities))
	}
	for _, metric := range snapshot.Metrics {
		if math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) || metric.Value < 0 {
			t.Fatalf("invalid measurement: %+v", metric)
		}
		if metric.Name == "cpu_used_percent" || metric.Name == "memory_used_percent" {
			if metric.Unit != "%" || metric.Value > 100 {
				t.Fatalf("invalid percent measurement: %+v", metric)
			}
		}
		for key, value := range metric.Labels {
			if len(key) > 64 || len(value) > 256 {
				t.Fatalf("invalid collection label: %q=%q", key, value)
			}
		}
	}
}

type synologyFixture struct{}

func (synologyFixture) Get(context.Context, []string) (map[string]any, error) {
	return map[string]any{synology.SystemStatusOID: 1, synology.TemperatureOID: 43}, nil
}
func (synologyFixture) Walk(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}

type failingSynologyFixture struct{ err error }

func (f failingSynologyFixture) Get(context.Context, []string) (map[string]any, error) {
	return nil, f.err
}
func (f failingSynologyFixture) Walk(context.Context, string) (map[string]any, error) {
	return nil, f.err
}

func TestCollectIncludesRemoteSynologyMetricsWithoutRelabelingHost(t *testing.T) {
	c := &Collector{synology: synology.NewWithClient("nas.example", synologyFixture{})}
	snapshot := protocol.TelemetrySnapshot{Metrics: []protocol.TelemetryMetric{{Name: "cpu_used_percent", Value: 12, Unit: "%"}}}
	capabilities := map[string]bool{"host_info": true}
	c.collectSynology(&snapshot, capabilities, func(capability string, err error) {
		t.Fatalf("Synology collection failed: %s: %v", capability, err)
	}, context.Background())
	foundNAS, foundHost := false, false
	for _, metric := range snapshot.Metrics {
		if metric.Name == "synology_temperature_celsius" && metric.Labels["nas_target"] == "nas.example" {
			foundNAS = true
		}
		if metric.Name == "cpu_used_percent" && metric.Labels["nas_target"] == "" {
			foundHost = true
		}
	}
	if !foundNAS || !foundHost {
		t.Fatalf("missing remote NAS or unlabeled local host measurements: %+v", snapshot.Metrics)
	}
	if !capabilities["synology_system_monitoring"] {
		t.Fatalf("NAS capability absent from snapshot: %v", capabilities)
	}
}

func TestCollectSynologyFailurePreservesHostMetricsAndRedactsError(t *testing.T) {
	const secret = "snmp-community-never-log-this"
	c := &Collector{synology: synology.NewWithClient("nas.example", failingSynologyFixture{err: fmt.Errorf("community %s timed out", secret)})}
	snapshot := protocol.TelemetrySnapshot{Metrics: []protocol.TelemetryMetric{{Name: "cpu_used_percent", Value: 12, Unit: "%"}}, Errors: []protocol.CollectionError{}}
	errorsSeen := []protocol.CollectionError{}
	c.collectSynology(&snapshot, map[string]bool{}, func(capability string, err error) {
		errorsSeen = append(errorsSeen, protocol.CollectionError{Capability: capability, Message: err.Error()})
	}, context.Background())
	if len(snapshot.Metrics) != 1 || snapshot.Metrics[0].Name != "cpu_used_percent" {
		t.Fatalf("NAS error removed or fabricated host metrics: %+v", snapshot.Metrics)
	}
	if len(errorsSeen) != 1 || errorsSeen[0].Capability != "synology_monitoring" {
		t.Fatalf("NAS failure was not reported: %+v", errorsSeen)
	}
	if strings.Contains(errorsSeen[0].Message, secret) {
		t.Fatal("SNMP community leaked into collection errors")
	}
}
