package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// This records outcomes independently of the rejected ingestion transaction.
// Queue metadata is updated separately by authenticated agent polls.
func (h *Handler) recordDelivery(ctx context.Context, agentID string, status int) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	now := time.Now().UTC()
	var retryAt, acceptedAt *time.Time
	if status == http.StatusTooManyRequests {
		retry := now.Add(time.Minute)
		retryAt = &retry
	}
	if status >= 200 && status < 300 {
		acceptedAt = &now
	}
	_, err := h.pool.Exec(ctx, `INSERT INTO agent_telemetry_delivery(agent_id,last_attempt_at,last_status_code,retry_at,last_accepted_at) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(agent_id) DO UPDATE SET last_attempt_at=EXCLUDED.last_attempt_at,last_status_code=EXCLUDED.last_status_code,retry_at=EXCLUDED.retry_at,last_accepted_at=COALESCE(EXCLUDED.last_accepted_at,agent_telemetry_delivery.last_accepted_at)`, agentID, now, status, retryAt, acceptedAt)
	if err != nil {
		slog.Warn("could not record telemetry delivery status", "agentId", agentID)
	}
}
