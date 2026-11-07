package stacks

import (
	"net/http"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stackgit"
)

// remove deletes a stack entirely: its containers and networks on the device,
// its files in Git, and its drafts, deployment history and binding. Named
// volumes and host data are never deleted.
func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		Confirm string `json:"confirm"`
		// SkipDevice forgets the stack without stopping containers, for a device
		// that is gone or permanently offline.
		SkipDevice bool `json:"skipDevice"`
	}
	if !decode(w, r, &q) {
		return
	}
	id := r.PathValue("id")
	if q.Confirm != id {
		http.Error(w, "Type the stack ID to confirm deletion.", 422)
		return
	}
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	unlock, e := a.repoLock(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer unlock()
	repo, ok := a.requireRepository(w, ctx)
	if !ok {
		return
	}
	var active bool
	if e = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_deployments WHERE stack_id=$1 AND status IN ('queued','running','unknown'))", id).Scan(&active); e != nil {
		fail(w, e)
		return
	}
	if active {
		http.Error(w, "A deployment is still active or needs reconciling. Resolve it before deleting this stack.", 409)
		return
	}
	// Stop containers first: a failure here leaves the stack fully intact and retryable.
	if b, bound := a.lookupTarget(ctx, id); bound && !q.SkipDevice {
		if e = a.Hub.Call(ctx, b.AgentID, protocol.MethodComposeRemove, protocol.ComposeRemoveRequest{StackID: id, ProjectName: b.ProjectName}, nil); e != nil {
			a.record(ctx, u, id, "delete", audit.OutcomeFailure, map[string]any{"stage": "device"})
			fail(w, e)
			return
		}
	}
	saved, e := repo.Remove(ctx, id, u.Username)
	if e != nil {
		a.record(ctx, u, id, "delete", audit.OutcomeFailure, map[string]any{"stage": "git"})
		fail(w, e)
		return
	}
	if saved.State != "saved" {
		a.record(ctx, u, id, "delete", audit.OutcomeFailure, map[string]any{"stage": "git", "state": saved.State})
		http.Error(w, "Git has not confirmed the deletion. Containers were stopped; retry Delete to finish.", 502)
		return
	}
	tx, e := a.Pool.Begin(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(ctx)
	for _, statement := range []string{
		"DELETE FROM stack_deployment_previews WHERE stack_id=$1",
		"DELETE FROM stack_drafts WHERE stack_id=$1",
		"DELETE FROM stack_deployments WHERE stack_id=$1",
		"DELETE FROM stack_bindings WHERE stack_id=$1",
	} {
		if _, e = tx.Exec(ctx, statement, id); e != nil {
			fail(w, e)
			return
		}
	}
	if e = tx.Commit(ctx); e != nil {
		fail(w, e)
		return
	}
	a.record(ctx, u, id, "delete", audit.OutcomeSuccess, map[string]any{"commit": saved.Commit, "skipDevice": q.SkipDevice})
	respond(w, 200, map[string]any{"deleted": true, "commit": saved.Commit})
}
