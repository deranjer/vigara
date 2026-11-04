package stacks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/deranjer/vigara/internal/placement"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/jackc/pgx/v5"
)

// RunImageUpdates uses a session lock so multiple server replicas never run the
// same check concurrently. Schedules and results survive process restarts.
func (a *API) RunImageUpdates(ctx context.Context) {
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		a.imageUpdateTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (a *API) imageUpdateTick(ctx context.Context) {
	s, err := a.imageSettings(ctx)
	if err != nil {
		return
	}
	a.scheduleImageUpdate(ctx, s)
	c, err := a.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer c.Release()
	var locked bool
	if c.QueryRow(ctx, "SELECT pg_try_advisory_lock(731946283)").Scan(&locked) != nil || !locked {
		return
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Exec(release, "SELECT pg_advisory_unlock(731946283)"); err != nil {
			_ = c.Conn().Close(release)
		}
	}()
	if a.Agents == nil {
		return
	}
	for _, agent := range a.Agents() {
		if agent.DockerTier == "" {
			continue
		}
		_, _ = a.Pool.Exec(ctx, "INSERT INTO image_update_hosts(agent_id) VALUES($1) ON CONFLICT DO NOTHING", agent.AgentID)
	}
	var agent string
	err = a.Pool.QueryRow(ctx, `SELECT h.agent_id::text FROM image_update_hosts h JOIN agent_identities ai ON ai.id=h.agent_id WHERE h.next_check<=now() AND ai.revoked_at IS NULL ORDER BY h.next_check LIMIT 1`).Scan(&agent)
	if err != nil {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err = a.checkImageHost(checkCtx, agent)
	message := ""
	failures := 0
	if err != nil {
		message = "Image check incomplete. Device may be offline, need an agent upgrade, or lack registry access."
		_ = a.Pool.QueryRow(ctx, "SELECT failures+1 FROM image_update_hosts WHERE agent_id=$1", agent).Scan(&failures)
	}
	delay := time.Duration(s.Interval) * time.Minute
	if err != nil {
		delay = time.Duration(15*(1<<min(failures, 5))) * time.Minute
		if delay > 6*time.Hour {
			delay = 6 * time.Hour
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(agent))
	delay += time.Duration(h.Sum32()%121) * time.Second
	_, _ = a.Pool.Exec(ctx, "UPDATE image_update_hosts SET next_check=now()+$2::interval,checked_at=now(),message=$3,failures=$4 WHERE agent_id=$1", agent, delay.String(), message, failures)
}
func (a *API) checkImageHost(ctx context.Context, agent string) error {
	var list protocol.DockerListResult
	if err := a.Hub.Call(ctx, agent, protocol.MethodDockerList, nil, &list); err != nil {
		return err
	}
	// Original tags are retained with successful managed releases. This works
	// even though those containers were launched using immutable digests.
	rows, err := a.Pool.Query(ctx, `SELECT DISTINCT ON (d.stack_id) d.project_name,d.tracking FROM stack_deployments d WHERE d.agent_id=$1 AND d.status='succeeded' ORDER BY d.stack_id,d.created_at DESC`, agent)
	if err != nil {
		return err
	}
	tracking := map[string]map[string]string{}
	for rows.Next() {
		var project string
		var raw []byte
		if err = rows.Scan(&project, &raw); err != nil {
			rows.Close()
			return err
		}
		refs := map[string]string{}
		if json.Unmarshal(raw, &refs) == nil {
			tracking[project] = refs
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	complete := !list.Truncated
	var cursor string
	if err = a.Pool.QueryRow(ctx, "SELECT cursor_id FROM image_update_hosts WHERE agent_id=$1", agent).Scan(&cursor); err != nil {
		return err
	}
	sort.Slice(list.Containers, func(i, j int) bool { return list.Containers[i].ID < list.Containers[j].ID })
	ids := []string{}
	for _, container := range list.Containers {
		ids = append(ids, container.ID)
		if container.ID <= cursor {
			continue
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 65*time.Second {
			return errors.New("image check will continue with remaining containers")
		}
		var ignored bool
		_ = a.Pool.QueryRow(ctx, "SELECT ignored FROM container_image_updates WHERE agent_id=$1 AND container_id=$2", agent, container.ID).Scan(&ignored)
		if ignored {
			continue
		}
		params := protocol.DockerImageCheckParams{ID: container.ID, Ref: tracking[container.Project][container.Service]}
		var result protocol.DockerImageCheck
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err = a.Hub.Call(callCtx, agent, protocol.MethodDockerImageCheck, params, &result)
		cancel()
		if err != nil {
			complete = false
			result = protocol.DockerImageCheck{ID: container.ID, Name: container.Name, Image: container.Image, Project: container.Project, Service: container.Service, Status: "unknown", Message: "Device could not check this image. Check connectivity and agent version."}
		}
		if result.ID != container.ID {
			return errors.New("uncorrelated image result")
		}
		if result.Status != "current" && result.Status != "available" && result.Status != "pinned" && result.Status != "unknown" {
			return errors.New("invalid image status")
		}
		if result.Status == "unknown" {
			complete = false
		}
		if err = a.storeImageCheck(ctx, agent, result); err != nil {
			return err
		}
		if _, err = a.Pool.Exec(ctx, "UPDATE image_update_hosts SET cursor_id=$2 WHERE agent_id=$1", agent, container.ID); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if !list.Truncated {
		_, err = a.Pool.Exec(ctx, "DELETE FROM container_image_updates WHERE agent_id=$1 AND NOT(container_id=ANY($2::text[]))", agent, ids)
		if err != nil {
			return err
		}
	}
	if _, err = a.Pool.Exec(ctx, "UPDATE image_update_hosts SET cursor_id='' WHERE agent_id=$1", agent); err != nil {
		return err
	}
	if !complete {
		return errors.New("image check incomplete")
	}
	return nil
}
func (a *API) storeImageCheck(ctx context.Context, agent string, result protocol.DockerImageCheck) error {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	raw, _ := json.Marshal(result)
	success := result.Status != "unknown"
	_, err = tx.Exec(ctx, `INSERT INTO container_image_updates(agent_id,container_id,result,last_success_at,last_success) VALUES($1,$2,$3,CASE WHEN $4 THEN now() END,CASE WHEN $4 THEN $3::jsonb END) ON CONFLICT(agent_id,container_id) DO UPDATE SET result=$3,checked_at=now(),last_success_at=CASE WHEN $4 THEN now() ELSE container_image_updates.last_success_at END,last_success=CASE WHEN $4 THEN $3::jsonb ELSE container_image_updates.last_success END`, agent, result.ID, raw, success)
	if err != nil {
		return err
	}
	if result.Status == "available" {
		tag, e := tx.Exec(ctx, "INSERT INTO image_update_notices(agent_id,container_id,digest) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", agent, result.ID, result.Available)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 1 {
			var device string
			if err = tx.QueryRow(ctx, "SELECT device_id::text FROM agent_identities WHERE id=$1", agent).Scan(&device); err != nil {
				return err
			}
			dimension := device + "/image/" + result.ID
			summary := fmt.Sprintf("Image changed for %.80s (%.120s).", result.Name, result.Image)
			var incident string
			err = tx.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at,last_observed_at) VALUES('00000000-0000-0000-0000-000000000024',$1,'firing',$2,now(),now()) ON CONFLICT(rule_id,dimension) DO UPDATE SET status='firing',summary=$2,last_observed_at=now(),resolved_at=NULL RETURNING id::text`, dimension, summary).Scan(&incident)
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"rule": "Container image updates", "summary": summary, "dimension": dimension})
			if _, err = tx.Exec(ctx, "INSERT INTO alert_events(incident_id,transition,payload) VALUES($1,'firing',$2)", incident, payload); err != nil {
				return err
			}
		}
	} else if result.Status == "current" {
		_, err = tx.Exec(ctx, `UPDATE alert_incidents SET status='resolved',resolved_at=now(),last_observed_at=now() WHERE rule_id='00000000-0000-0000-0000-000000000024' AND dimension=(SELECT device_id::text FROM agent_identities WHERE id=$1)||'/image/'||$2 AND status='firing'`, agent, result.ID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *API) pauseImagePolicy(ctx context.Context, id, reason string) {
	_, _ = a.Pool.Exec(ctx, "UPDATE stack_update_policies SET paused_reason=$2 WHERE stack_id=$1 AND enabled", id, reason)
}
func (a *API) scheduleImageUpdate(ctx context.Context, settings imageSettings) {
	unlock, err := a.updateLock(ctx)
	if err != nil {
		return
	}
	defer unlock()
	a.notifyAutomaticImageFailures(ctx)
	// A failed or uncertain automatic release requires explicit re-enablement.
	_, _ = a.Pool.Exec(ctx, `UPDATE stack_update_policies p SET paused_reason='An automatic deployment failed or has an uncertain outcome. Inspect deployment history, then save this policy to resume.' WHERE p.enabled AND p.paused_reason='' AND EXISTS(SELECT 1 FROM stack_deployments d WHERE d.stack_id=p.stack_id AND d.automatic AND d.policy_version=p.version AND d.stage<>'policy' AND d.status IN ('failed','unknown'))`)
	rows, err := a.Pool.Query(ctx, "SELECT stack_id FROM stack_update_policies WHERE enabled AND paused_reason='' ORDER BY stack_id")
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if a.queueImageUpdate(ctx, id, settings) == nil {
			return
		}
	}
}

func (a *API) notifyAutomaticImageFailures(ctx context.Context) {
	rows, err := a.Pool.Query(ctx, `SELECT d.id,d.stack_id,d.agent_id::text FROM stack_deployments d WHERE d.automatic AND d.status IN ('failed','unknown') AND d.stage<>'policy' AND NOT EXISTS(SELECT 1 FROM image_update_notices n WHERE n.agent_id=d.agent_id AND n.container_id='stack:'||d.stack_id AND n.digest=d.id) ORDER BY d.created_at LIMIT 100`)
	if err != nil {
		return
	}
	type failure struct{ id, stack, agent string }
	items := []failure{}
	for rows.Next() {
		var f failure
		if rows.Scan(&f.id, &f.stack, &f.agent) != nil {
			rows.Close()
			return
		}
		items = append(items, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, f := range items {
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return
		}
		tag, err := tx.Exec(ctx, "INSERT INTO image_update_notices(agent_id,container_id,digest) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", f.agent, "stack:"+f.stack, f.id)
		if err == nil && tag.RowsAffected() == 1 {
			summary := "Automatic image update failed or has an uncertain outcome for stack " + f.stack + ". Review deployment history before resuming."
			var device, incident string
			err = tx.QueryRow(ctx, "SELECT device_id::text FROM agent_identities WHERE id=$1", f.agent).Scan(&device)
			dimension := device + "/image-update-failure/" + f.stack
			if err == nil {
				err = tx.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at,last_observed_at) VALUES('00000000-0000-0000-0000-000000000024',$1,'firing',$2,now(),now()) ON CONFLICT(rule_id,dimension) DO UPDATE SET status='firing',summary=$2,last_observed_at=now(),resolved_at=NULL RETURNING id::text`, dimension, summary).Scan(&incident)
			}
			payload, _ := json.Marshal(map[string]string{"rule": "Automatic image update needs attention", "summary": summary, "dimension": dimension})
			if err == nil {
				_, err = tx.Exec(ctx, "INSERT INTO alert_events(incident_id,transition,payload) VALUES($1,'firing',$2)", incident, payload)
			}
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(context.Background())
		}
		if err != nil {
			return
		}
	}
}
func (a *API) queueImageUpdate(ctx context.Context, id string, settings imageSettings) error {
	p, err := a.updatePolicy(ctx, id)
	if err != nil {
		return err
	}
	window := settings.Maintenance
	if p.Maintenance != nil {
		window = *p.Maintenance
	}
	slot := window.Slot(time.Now())
	if slot == "" {
		return errors.New("outside maintenance window")
	}
	var lastSlot string
	if err = a.Pool.QueryRow(ctx, "SELECT last_slot FROM stack_update_policies WHERE stack_id=$1", id).Scan(&lastSlot); err != nil {
		return err
	}
	if lastSlot == slot {
		return errors.New("already attempted this window")
	}
	var baseline, trackingBytes, imageBytes []byte
	var agent, commit, configuration string
	err = a.Pool.QueryRow(ctx, `SELECT snapshot,tracking,images,agent_id::text,commit_id,configuration_digest FROM stack_deployments WHERE stack_id=$1 AND status='succeeded' ORDER BY created_at DESC LIMIT 1`, id).Scan(&baseline, &trackingBytes, &imageBytes, &agent, &commit, &configuration)
	if err != nil {
		return err
	}
	if configuration != p.Configuration {
		a.pauseImagePolicy(ctx, id, "Stack configuration changed. Review and save the update policy again.")
		return stack.ErrInvalid
	}
	var active bool
	if a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_deployments WHERE stack_id=$1 AND status IN ('queued','running','unknown'))", id).Scan(&active) != nil || active {
		return errors.New("active deployment")
	}
	var snapshot stack.Snapshot
	tracking, images := map[string]string{}, map[string]string{}
	if json.Unmarshal(baseline, &snapshot) != nil || json.Unmarshal(trackingBytes, &tracking) != nil || json.Unmarshal(imageBytes, &images) != nil {
		return stack.ErrInvalid
	}
	// Scheduling must not turn a Git edit into an unattended configuration rollout.
	repoUnlock, err := a.repoLock(ctx)
	if err != nil {
		return err
	}
	defer repoUnlock()
	repo, err := a.repository(ctx)
	if err != nil || repo == nil {
		return errors.New("repository unavailable")
	}
	current, err := a.deploymentSnapshot(ctx, repo, id, "", nil)
	if err != nil {
		return err
	}
	if stack.Digest(current) != configuration {
		a.pauseImagePolicy(ctx, id, "Saved configuration changed. Deploy and review the update policy again.")
		return stack.ErrInvalid
	}
	excluded := map[string]bool{}
	for _, s := range p.Excluded {
		excluded[s] = true
	}
	changed := false
	for service, ref := range tracking {
		if excluded[service] || strings.Contains(ref, "@") {
			continue
		}
		var raw []byte
		err = a.Pool.QueryRow(ctx, `SELECT result FROM container_image_updates WHERE agent_id=$1 AND NOT ignored AND result->>'project'=$2 AND result->>'service'=$3 AND checked_at>now()-($4::integer * interval '1 minute') AND result->>'status'='available' ORDER BY checked_at DESC LIMIT 1`, agent, aProject(snapshot), service, settings.Interval+15).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var result protocol.DockerImageCheck
		if json.Unmarshal(raw, &result) != nil || result.Image != ref || !imageManifestDigest.MatchString(result.Manifest) {
			return stack.ErrInvalid
		}
		repository := ref
		if i := strings.LastIndex(repository, ":"); i > strings.LastIndex(repository, "/") {
			repository = repository[:i]
		}
		candidate := repository + "@" + result.Manifest
		if images[service] != candidate {
			images[service] = candidate
			changed = true
		}
	}
	if !changed {
		return errors.New("no update available")
	}
	var plan stack.Plan
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err = a.Hub.Call(callCtx, agent, protocol.MethodComposeImagePlan, map[string]any{"snapshot": snapshot, "images": images}, &plan)
	if err != nil {
		return err
	}
	if !validReleaseImages(plan.Images, p.Services) || !stack.Equal(plan.Images, images) || len(plan.Token) != 64 {
		return stack.ErrInvalid
	}
	if window.Slot(time.Now()) != slot {
		return errors.New("maintenance window ended")
	}
	binding, ok := a.lookupTarget(ctx, id)
	if !ok || binding.AgentID != agent {
		return stack.ErrInvalid
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	jobID := hex.EncodeToString(random)
	imagesRaw, _ := json.Marshal(images)
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `INSERT INTO stack_deployments(id,stack_id,agent_id,project_name,actor_name,commit_id,configuration_digest,snapshot,plan_token,pull,status,images,tracking,automatic,policy_version) VALUES($1,$2,$3,$4,'Automatic image update',$5,$6,$7,$8,true,'queued',$9,$10,true,$11)`, jobID, id, agent, binding.ProjectName, commit, configuration, baseline, plan.Token, imagesRaw, trackingBytes, p.Version)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE stack_update_policies SET last_slot=$2 WHERE stack_id=$1", id, slot); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_name,action,target_kind,target_id,agent_id,outcome,detail) VALUES('Automatic image update','stack.auto-update','stack',$1,$2,'success',$3)`, id, agent, []byte(fmt.Sprintf(`{"job":"%s"}`, jobID))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var imageManifestDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func aProject(s stack.Snapshot) string {
	_, _, m, err := placement.ResolveRuntime(s)
	if err != nil {
		return ""
	}
	return m.ProjectName
}

func (a *API) automaticImageAllowed(ctx context.Context, s stack.Snapshot, version *int64) bool {
	if version == nil {
		return false
	}
	p, err := a.updatePolicy(ctx, s.ID)
	if err != nil || !p.Enabled || p.Paused != "" || p.Version != *version || p.Configuration != stack.Digest(s) {
		return false
	}
	settings, err := a.imageSettings(ctx)
	if err != nil {
		return false
	}
	w := settings.Maintenance
	if p.Maintenance != nil {
		w = *p.Maintenance
	}
	return w.Slot(time.Now()) != ""
}
