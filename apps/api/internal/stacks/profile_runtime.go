package stacks

import (
	"context"
	"net/http"

	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
)

type profileBinding struct {
	ProfileID string `json:"profileId"`
	AgentID   string `json:"agentId"`
	Epoch     uint64 `json:"epoch"`
}

func (a *API) profileBindings(ctx context.Context) ([]profileBinding, error) {
	result := []profileBinding{}
	rows, err := a.Pool.Query(ctx, "SELECT profile_id,agent_id::text,ownership_epoch FROM stack_profile_bindings ORDER BY profile_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b profileBinding
		if err = rows.Scan(&b.ProfileID, &b.AgentID, &b.Epoch); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

// Bind only an initial logical profile. Neither this endpoint nor Git can move
// an existing application or grant a second host ownership.
func (a *API) bindProfile(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q struct {
		ProfileID string `json:"profileId"`
		AgentID   string `json:"agentId"`
		Commit    string `json:"commit"`
	}
	if !decode(w, r, &q) {
		return
	}
	if !stack.ValidID(q.ProfileID) || !stack.CommitPattern.MatchString(q.Commit) || len(q.AgentID) != 36 {
		http.Error(w, "Select a saved profile and enrolled device.", 422)
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
	if p.Commit != q.Commit {
		fail(w, stackgit.ErrConflict)
		return
	}
	c, err := placement.Parse(p.Files)
	if err == nil {
		err = repo.CheckPlacements(ctx, p)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if _, ok := c.Profiles[q.ProfileID]; !ok {
		http.Error(w, "Saved profile not found.", 404)
		return
	}
	var enrolled, active bool
	err = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_identities WHERE id::text=$1 AND revoked_at IS NULL),EXISTS(SELECT 1 FROM stack_deployments WHERE status IN ('queued','running','unknown'))", q.AgentID).Scan(&enrolled, &active)
	if err != nil {
		fail(w, err)
		return
	}
	if !enrolled || active {
		http.Error(w, "Select an enrolled device and resolve active deployments before binding.", 409)
		return
	}
	for _, x := range c.Assignments {
		if x.ProfileID == q.ProfileID {
			b, ok := a.lookupTarget(ctx, x.StackID)
			if !ok || b.AgentID != q.AgentID {
				http.Error(w, "Every assigned stack must already be approved on this device. Moving ownership requires a fenced transfer.", 409)
				return
			}
		}
	}
	var targets protocol.ComposeTargets
	if err = a.Hub.Call(ctx, q.AgentID, protocol.MethodComposeTargets, nil, &targets); err != nil {
		http.Error(w, "The device must be online with Compose deployment access.", 409)
		return
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var agent string
	err = tx.QueryRow(ctx, `INSERT INTO stack_profile_bindings(profile_id,agent_id) VALUES($1,$2) ON CONFLICT(profile_id) DO UPDATE SET profile_id=EXCLUDED.profile_id RETURNING agent_id::text`, q.ProfileID, q.AgentID).Scan(&agent)
	if err != nil {
		fail(w, err)
		return
	}
	if agent != q.AgentID {
		http.Error(w, "This profile is already bound. Replacement requires a fenced transfer.", 409)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,actor_name,action,target_kind,target_id,agent_id,outcome,detail) VALUES($1,$2,'profile.bind','profile',$3,$4,'success','{}')`, u.ID, u.Username, q.ProfileID, q.AgentID)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, profileBinding{q.ProfileID, q.AgentID, 1})
}

// Capture the profile from the same confirmed Git revision as the stack and
// require its ownership intent to match current Git, even for historical deploys.
func (a *API) deploymentSnapshot(ctx context.Context, repo *stackgit.Repository, id, commit string, reader settingsReader) (stack.Snapshot, error) {
	s, err := repo.Read(ctx, id, commit)
	if err != nil {
		return s, err
	}
	current, err := repo.Placements(ctx, "")
	if err != nil {
		return s, err
	}
	if err = repo.CheckPlacements(ctx, current); err != nil {
		return s, err
	}
	c, err := placement.Parse(current.Files)
	if err != nil {
		return s, err
	}
	var selected *placement.Assignment
	for _, x := range c.Assignments {
		if x.StackID == id && x.Enabled {
			if selected != nil {
				return s, errPlacementInactive
			}
			y := x
			selected = &y
		}
	}
	if selected == nil {
		return s, nil
	}
	x := selected
	old, err := repo.Placements(ctx, commit)
	if err != nil {
		return s, err
	}
	profilePath := "profiles/" + x.ProfileID + "/vigara-profile.yaml"
	assignmentPath := "assignments/" + x.ProfileID + "/" + id + ".yaml"
	if current.Files[profilePath] != old.Files[profilePath] || current.Files[assignmentPath] != old.Files[assignmentPath] {
		return s, stackgit.ErrConflict
	}
	s.Placement = &stack.Placement{ProfileID: x.ProfileID, AssignmentID: x.ID, Epoch: x.Ownership.Epoch, ProfileManifest: old.Files[profilePath], AssignmentManifest: old.Files[assignmentPath]}
	_, _, m, err := placement.ResolveRuntime(s)
	if err != nil {
		return s, err
	}
	if reader == nil {
		reader = a.Pool
	}
	b, ok := a.lookupTargetWithReader(ctx, id, reader)
	if !ok || b.ProjectName != m.ProjectName {
		return s, errPlacementInactive
	}
	var agent string
	var epoch uint64
	err = reader.QueryRow(ctx, "SELECT agent_id::text,ownership_epoch FROM stack_profile_bindings WHERE profile_id=$1", x.ProfileID).Scan(&agent, &epoch)
	if err != nil || epoch != x.Ownership.Epoch || agent != b.AgentID {
		return s, errPlacementInactive
	}
	return s, nil
}

func (a *API) lookupTargetWithReader(ctx context.Context, id string, reader settingsReader) (Binding, bool) {
	if b, ok := a.Targets[id]; ok {
		return b, true
	}
	var b Binding
	if reader == nil {
		return b, false
	}
	err := reader.QueryRow(ctx, "SELECT agent_id::text,project_name,directory,display_name FROM stack_bindings WHERE stack_id=$1 AND directory<>''", id).Scan(&b.AgentID, &b.ProjectName, &b.Directory, &b.DisplayName)
	return b, err == nil
}

func (a *API) validateActivePlacements(ctx context.Context, p stackgit.Placements) error {
	c, err := placement.Parse(p.Files)
	if err != nil {
		return err
	}
	for _, x := range c.Assignments {
		if x.Enabled {
			b, ok := a.lookupTarget(ctx, x.StackID)
			if !ok {
				return errPlacementInactive
			}
			var agent string
			var epoch uint64
			err = a.Pool.QueryRow(ctx, "SELECT agent_id::text,ownership_epoch FROM stack_profile_bindings WHERE profile_id=$1", x.ProfileID).Scan(&agent, &epoch)
			if err != nil || agent != b.AgentID || epoch != x.Ownership.Epoch || epoch != 1 || x.Ownership.Mode != "exclusive" || x.ProjectName != "" || x.ReplicaKey != "" {
				return errPlacementInactive
			}
		}
	}
	return nil
}
