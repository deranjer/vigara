package protocol

import (
	"errors"
	"time"
)

const MaxQueuedTelemetrySnapshots = 100

// TelemetryQueueStatus contains queue metadata only, never snapshot payloads.
// It is sent only after a server advertises support in its poll response.
type TelemetryQueueStatus struct {
	QueuedSnapshots int        `json:"queuedSnapshots"`
	OldestQueuedAt  *time.Time `json:"oldestQueuedAt,omitempty"`
	NewestQueuedAt  *time.Time `json:"newestQueuedAt,omitempty"`
}

func (s TelemetryQueueStatus) Validate(now time.Time) error {
	if s.QueuedSnapshots < 0 || s.QueuedSnapshots > MaxQueuedTelemetrySnapshots {
		return errors.New("invalid telemetry queue count")
	}
	if s.QueuedSnapshots == 0 {
		if s.OldestQueuedAt != nil || s.NewestQueuedAt != nil {
			return errors.New("empty telemetry queue has timestamps")
		}
		return nil
	}
	if s.OldestQueuedAt == nil || s.NewestQueuedAt == nil || s.OldestQueuedAt.IsZero() || s.NewestQueuedAt.IsZero() || s.OldestQueuedAt.After(*s.NewestQueuedAt) || s.NewestQueuedAt.After(now.Add(5*time.Minute)) {
		return errors.New("invalid telemetry queue timestamps")
	}
	return nil
}
