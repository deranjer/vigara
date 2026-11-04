package stacks

import (
	"context"
	"errors"
	"net/http"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errPlacementInactive = errors.New("This profile assignment requires an initial binding to its existing approved device, exclusive ownership at epoch 1, and an updated agent. Replacement and replica deployment require a separate fenced transfer.")

func (a *API) getPlacements(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorize(w, r, false); !ok {
		return
	}
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	repo, ok := a.requireRepository(w, ctx)
	if !ok {
		return
	}
	p, err := repo.Placements(ctx, "")
	if err != nil {
		fail(w, err)
		return
	}
	c, validation := placement.Parse(p.Files)
	if validation == nil {
		validation = repo.CheckPlacements(ctx, p)
	}
	if validation != nil && !errors.Is(validation, placement.ErrInvalid) {
		fail(w, validation)
		return
	}
	message := ""
	if validation != nil {
		message = validation.Error()
	}
	bindings, err := a.profileBindings(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]any{"commit": p.Commit, "files": p.Files, "catalog": c, "validationError": message, "branch": repo.Branch, "activationSupported": true, "bindings": bindings})
}

func (a *API) savePlacements(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var p stackgit.Placements
	if !decode(w, r, &p) {
		return
	}
	c, err := placement.Parse(p.Files)
	if err != nil {
		fail(w, err)
		return
	}
	if a.Pool == nil {
		http.Error(w, "Bind a saved profile before activating; keep enabled: false until then.", 422)
		return
	}
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	if err = a.validateActivePlacements(ctx, p); err != nil {
		fail(w, err)
		return
	}
	repo, ok := a.requireRepository(w, ctx)
	if !ok {
		return
	}
	var active bool
	err = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_deployments WHERE status IN ('queued','running','unknown'))").Scan(&active)
	if err != nil {
		fail(w, err)
		return
	}
	if active {
		http.Error(w, "Resolve active or uncertain deployments before changing profiles and assignments.", 409)
		return
	}
	saved, err := repo.SavePlacements(ctx, p, u.Username)
	if err != nil {
		fail(w, err)
		return
	}
	a.record(ctx, u, "profiles", "placements-save", audit.OutcomeSuccess, map[string]any{"commit": saved.Commit, "state": saved.State, "profiles": len(c.Profiles), "assignments": len(c.Assignments)})
	respond(w, 200, saved)
}

// Consult current desired placement even when deploying an older stack commit:
// selecting history must not bypass a newly introduced assignment requirement.
func directPlacement(ctx context.Context, repo *stackgit.Repository, id string) error {
	p, err := repo.Placements(ctx, "")
	if err != nil {
		return err
	}
	c, err := placement.Parse(p.Files)
	if err != nil {
		return err
	}
	for _, a := range c.Assignments {
		if a.StackID == id && a.Enabled {
			return errPlacementInactive
		}
	}
	return nil
}

func (a *API) checkCapturedDeployment(ctx context.Context, captured stack.Snapshot, conn *pgxpool.Conn) error {
	ctx, cancel := stackgit.DefaultTimeout(ctx)
	defer cancel()
	// The worker already holds its session lock on conn. Reuse that connection
	// instead of requiring additional pool capacity while dispatching a job.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(731946281)"); err != nil {
		return err
	}
	defer func() {
		release, cancel := stackgit.DefaultTimeout(context.Background())
		defer cancel()
		if _, err := conn.Exec(release, "SELECT pg_advisory_unlock(731946281)"); err != nil {
			_ = conn.Conn().Close(release)
		}
	}()
	s, err := a.readSettings(ctx, conn)
	if err != nil {
		return err
	}
	if s.Remote == "" {
		return stackgit.ErrGit
	}
	actual, err := a.deploymentSnapshot(ctx, a.repositoryFromSettings(s), captured.ID, captured.Commit, conn)
	if err != nil {
		return err
	}
	if stack.Digest(actual) != stack.Digest(captured) {
		return stackgit.ErrConflict
	}
	return nil
}
