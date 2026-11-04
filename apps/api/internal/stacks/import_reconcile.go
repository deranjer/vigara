package stacks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Self-updates may replace the API or its proxy while the host keeps deploying.
// Read the correlated durable result after reconnecting; never replay a job.
func (a *API) reconcileImported(ctx context.Context, c *pgxpool.Conn) {
	rows, err := c.Query(ctx, `SELECT d.id,d.agent_id::text,d.snapshot,d.plan_token,d.images FROM stack_deployments d JOIN stack_bindings b ON b.stack_id=d.stack_id WHERE d.status='unknown' AND b.imported ORDER BY d.created_at LIMIT 3`)
	if err != nil {
		return
	}
	type pending struct {
		id, agent, token string
		snapshot, images []byte
	}
	jobs := []pending{}
	for rows.Next() {
		var job pending
		if rows.Scan(&job.id, &job.agent, &job.snapshot, &job.token, &job.images) != nil {
			rows.Close()
			return
		}
		jobs = append(jobs, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, job := range jobs {
		var snapshot stack.Snapshot
		images := map[string]string{}
		if json.Unmarshal(job.snapshot, &snapshot) != nil || json.Unmarshal(job.images, &images) != nil {
			continue
		}
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		var result stack.Result
		err = a.Hub.Call(check, job.agent, protocol.MethodComposeStatus, map[string]string{"stackId": snapshot.ID, "jobId": job.id}, &result)
		cancel()
		if err != nil {
			continue
		}
		result = correlatedResult(result, snapshot, job.id, job.token, images)
		if result.Status != "succeeded" && result.Status != "failed" {
			continue
		}
		raw, _ := json.Marshal(result)
		_, _ = c.Exec(ctx, "UPDATE stack_deployments SET status=$2,stage=$3,message=$4,result=$5,finished_at=now() WHERE id=$1 AND status='unknown'", job.id, result.Status, result.Stage, result.Message, raw)
	}
}
