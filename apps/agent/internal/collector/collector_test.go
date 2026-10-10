package collector

import (
	"context"
	"math"
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

func TestCollectReturnsBoundedFiniteMeasurements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	snapshot := New().Collect(ctx)
	if snapshot.ObservedAt.IsZero() || snapshot.Host.OS == "" || snapshot.Host.Architecture == "" {
		t.Fatalf("missing observation or host identity: %+v", snapshot)
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
