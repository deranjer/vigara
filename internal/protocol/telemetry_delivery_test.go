package protocol

import (
	"testing"
	"time"
)

func TestTelemetryQueueMetadataBounds(t *testing.T) {
	now := time.Now()
	older := now.Add(-time.Hour)
	future := now.Add(6 * time.Minute)
	for _, s := range []TelemetryQueueStatus{{}, {QueuedSnapshots: 100, OldestQueuedAt: &older, NewestQueuedAt: &now}} {
		if err := s.Validate(now); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []TelemetryQueueStatus{{QueuedSnapshots: -1}, {QueuedSnapshots: 101}, {QueuedSnapshots: 1}, {OldestQueuedAt: &older}, {QueuedSnapshots: 1, OldestQueuedAt: &now, NewestQueuedAt: &older}, {QueuedSnapshots: 1, OldestQueuedAt: &now, NewestQueuedAt: &future}} {
		if s.Validate(now) == nil {
			t.Fatalf("accepted invalid status: %+v", s)
		}
	}
}
