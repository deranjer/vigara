package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/imageupdates"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/jackc/pgx/v5"
)

type imageSettings struct {
	Interval    int                 `json:"intervalMinutes"`
	Maintenance imageupdates.Window `json:"maintenance"`
	Version     int64               `json:"version"`
}
type updatePolicy struct {
	Tracking      map[string]string    `json:"tracking,omitempty"`
	StackID       string               `json:"stackId"`
	Enabled       bool                 `json:"enabled"`
	Maintenance   *imageupdates.Window `json:"maintenance"`
	Excluded      []string             `json:"excludedServices"`
	Configuration string               `json:"configuration"`
	Services      []string             `json:"services"`
	Paused        string               `json:"pausedReason"`
	Version       int64                `json:"version"`
}

var updateUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (a *API) registerImageUpdates(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/image-updates", a.imageUpdateList)
	mux.HandleFunc("GET /api/image-updates/settings", a.getImageSettings)
	mux.HandleFunc("PUT /api/image-updates/settings", a.putImageSettings)
	mux.HandleFunc("POST /api/image-updates/check", a.requestImageCheck)
	mux.HandleFunc("PUT /api/image-updates/containers/{agent}/{container}", a.ignoreImage)
	mux.HandleFunc("GET /api/stacks/{id}/update-policy", a.getUpdatePolicy)
	mux.HandleFunc("PUT /api/stacks/{id}/update-policy", a.putUpdatePolicy)
}
func (a *API) imageSettings(ctx context.Context) (imageSettings, error) {
	var s imageSettings
	var raw []byte
	err := a.Pool.QueryRow(ctx, "SELECT interval_minutes,maintenance,version FROM image_update_settings WHERE singleton").Scan(&s.Interval, &raw, &s.Version)
	if err == nil {
		err = json.Unmarshal(raw, &s.Maintenance)
	}
	return s, err
}
func (a *API) getImageSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersRead); !ok {
		return
	}
	s, err := a.imageSettings(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, s)
}
func (a *API) putImageSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy)
	if !ok {
		return
	}
	var s imageSettings
	if !decode(w, r, &s) {
		return
	}
	if s.Interval < 15 || s.Interval > 10080 || s.Maintenance.Validate() != nil {
		http.Error(w, "Choose a check interval from 15 to 10080 minutes and a valid maintenance window.", 422)
		return
	}
	unlock, err := a.updateLock(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	raw, _ := json.Marshal(s.Maintenance)
	err = a.Pool.QueryRow(r.Context(), "UPDATE image_update_settings SET interval_minutes=$1,maintenance=$2,version=version+1 WHERE singleton AND version=$3 RETURNING version", s.Interval, raw, s.Version).Scan(&s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Settings changed. Reload before saving.", 409)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	a.record(r.Context(), u, "image-updates", "settings", audit.OutcomeSuccess, nil)
	respond(w, 200, s)
}
func (a *API) imageUpdateList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersRead); !ok {
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT u.agent_id::text,d.display_name,u.container_id,u.result,u.checked_at,u.last_success_at,u.last_success,u.ignored,COALESCE(b.stack_id,'') FROM container_image_updates u JOIN agent_identities ai ON ai.id=u.agent_id JOIN devices d ON d.id=ai.device_id LEFT JOIN stack_bindings b ON b.agent_id=u.agent_id AND b.project_name=u.result->>'project' WHERE ai.revoked_at IS NULL ORDER BY d.display_name,u.result->>'name' LIMIT 10000`)
	if err != nil {
		fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var agent, name, id, stackID string
		var raw, last []byte
		var checked time.Time
		var success *time.Time
		var ignored bool
		if err = rows.Scan(&agent, &name, &id, &raw, &checked, &success, &last, &ignored, &stackID); err != nil {
			rows.Close()
			fail(w, err)
			return
		}
		var result protocol.DockerImageCheck
		_ = json.Unmarshal(raw, &result)
		var previous *protocol.DockerImageCheck
		if len(last) > 0 {
			_ = json.Unmarshal(last, &previous)
		}
		items = append(items, map[string]any{"agentId": agent, "device": name, "containerId": id, "stackId": stackID, "result": result, "checkedAt": checked, "lastSuccessAt": success, "lastSuccess": previous, "ignored": ignored})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(w, err)
		return
	}
	hosts := []map[string]any{}
	rows, err = a.Pool.Query(r.Context(), `SELECT h.agent_id::text,d.display_name,h.checked_at,h.next_check,h.message FROM image_update_hosts h JOIN agent_identities ai ON ai.id=h.agent_id JOIN devices d ON d.id=ai.device_id WHERE ai.revoked_at IS NULL ORDER BY d.display_name`)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, message string
		var checked *time.Time
		var next time.Time
		if rows.Scan(&id, &name, &checked, &next, &message) != nil {
			fail(w, stack.ErrInvalid)
			return
		}
		hosts = append(hosts, map[string]any{"agentId": id, "device": name, "checkedAt": checked, "nextCheck": next, "message": message})
	}
	if rows.Err() != nil {
		fail(w, rows.Err())
		return
	}
	respond(w, 200, map[string]any{"items": items, "hosts": hosts})
}
func (a *API) requestImageCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersManage); !ok {
		return
	}
	var q struct {
		AgentID string `json:"agentId"`
	}
	if !decode(w, r, &q) {
		return
	}
	if q.AgentID != "" && !updateUUID.MatchString(q.AgentID) {
		http.Error(w, "Choose an enrolled device.", 422)
		return
	}
	_, err := a.Pool.Exec(r.Context(), `INSERT INTO image_update_hosts(agent_id,next_check) SELECT id,now() FROM agent_identities WHERE revoked_at IS NULL AND ($1='' OR id::text=$1) ON CONFLICT(agent_id) DO UPDATE SET next_check=now()`, q.AgentID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 202, map[string]bool{"queued": true})
}
func (a *API) ignoreImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermContainersManage); !ok {
		return
	}
	var q struct {
		Ignored bool `json:"ignored"`
	}
	if !decode(w, r, &q) {
		return
	}
	if !updateUUID.MatchString(r.PathValue("agent")) {
		http.Error(w, "Device not found.", 404)
		return
	}
	tag, err := a.Pool.Exec(r.Context(), "UPDATE container_image_updates SET ignored=$3 WHERE agent_id=$1 AND container_id=$2", r.PathValue("agent"), r.PathValue("container"), q.Ignored)
	if err != nil {
		fail(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "Container not found.", 404)
		return
	}
	respond(w, 200, q)
}
func (a *API) updatePolicy(ctx context.Context, id string) (updatePolicy, error) {
	p := updatePolicy{StackID: id, Excluded: []string{}, Services: []string{}}
	var window, excluded, services []byte
	err := a.Pool.QueryRow(ctx, "SELECT enabled,maintenance,excluded_services,approved_configuration,approved_services,paused_reason,version FROM stack_update_policies WHERE stack_id=$1", id).Scan(&p.Enabled, &window, &excluded, &p.Configuration, &services, &p.Paused, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if len(window) > 0 {
		err = json.Unmarshal(window, &p.Maintenance)
	}
	if err == nil {
		err = json.Unmarshal(excluded, &p.Excluded)
	}
	if err == nil {
		err = json.Unmarshal(services, &p.Services)
	}
	return p, err
}
func (a *API) getUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorize(w, r, false); !ok {
		return
	}
	p, err := a.updatePolicy(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if len(p.Services) == 0 {
		var raw []byte
		if a.Pool.QueryRow(r.Context(), "SELECT images FROM stack_deployments WHERE stack_id=$1 AND status='succeeded' ORDER BY created_at DESC LIMIT 1", p.StackID).Scan(&raw) == nil {
			images := map[string]string{}
			if json.Unmarshal(raw, &images) == nil {
				for service := range images {
					p.Services = append(p.Services, service)
				}
				sort.Strings(p.Services)
			}
		}
	}
	var tracking []byte
	if a.Pool.QueryRow(r.Context(), "SELECT tracking FROM stack_deployments WHERE stack_id=$1 AND status='succeeded' ORDER BY created_at DESC LIMIT 1", p.StackID).Scan(&tracking) == nil {
		_ = json.Unmarshal(tracking, &p.Tracking)
	}
	respond(w, 200, p)
}
func (a *API) putUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q updatePolicy
	if !decode(w, r, &q) {
		return
	}
	q.StackID = r.PathValue("id")
	if q.Maintenance != nil && q.Maintenance.Validate() != nil {
		http.Error(w, "Choose a valid maintenance window.", 422)
		return
	}
	// Serialize policy edits with scheduling and dispatch. Disabling cannot race
	// an automatic job's final permission check.
	unlock, err := a.updateLock(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	configuration := ""
	services := []string{}
	if q.Enabled {
		var raw, trackingRaw []byte
		err = a.Pool.QueryRow(r.Context(), "SELECT configuration_digest,images,tracking FROM stack_deployments WHERE stack_id=$1 AND status='succeeded' ORDER BY created_at DESC LIMIT 1", q.StackID).Scan(&configuration, &raw, &trackingRaw)
		images, tracking := map[string]string{}, map[string]string{}
		if err != nil || json.Unmarshal(raw, &images) != nil || json.Unmarshal(trackingRaw, &tracking) != nil || len(tracking) == 0 {
			http.Error(w, "Deploy this stack successfully with an updated agent before enabling automatic updates.", 409)
			return
		}
		for service := range images {
			services = append(services, service)
		}
		sort.Strings(services)
		seen := map[string]bool{}
		for _, s := range q.Excluded {
			if _, ok := images[s]; !ok || seen[s] {
				http.Error(w, "Choose existing services to exclude.", 422)
				return
			}
			seen[s] = true
		}
		trackable := false
		for s, ref := range tracking {
			if !seen[s] && !strings.Contains(ref, "@") {
				trackable = true
			}
		}
		if !trackable {
			http.Error(w, "This stack has no included services tracking mutable image tags.", 422)
			return
		}
	}
	if q.Excluded == nil {
		q.Excluded = []string{}
	}
	var window any
	if q.Maintenance != nil {
		b, _ := json.Marshal(q.Maintenance)
		window = b
	}
	excluded, _ := json.Marshal(q.Excluded)
	serviceBytes, _ := json.Marshal(services)
	err = a.Pool.QueryRow(r.Context(), `INSERT INTO stack_update_policies(stack_id,enabled,maintenance,excluded_services,approved_configuration,approved_services) SELECT $1,$2,$3,$4,$5,$6 WHERE $7::bigint=0 ON CONFLICT(stack_id) DO NOTHING RETURNING version`, q.StackID, q.Enabled, window, excluded, configuration, serviceBytes, q.Version).Scan(&q.Version)
	if errors.Is(err, pgx.ErrNoRows) && q.Version > 0 {
		err = a.Pool.QueryRow(r.Context(), `UPDATE stack_update_policies SET enabled=$2,maintenance=$3,excluded_services=$4,approved_configuration=$5,approved_services=$6,paused_reason='',version=version+1,updated_at=now() WHERE stack_id=$1 AND version=$7 RETURNING version`, q.StackID, q.Enabled, window, excluded, configuration, serviceBytes, q.Version).Scan(&q.Version)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Policy changed. Reload before saving.", 409)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	_, err = a.Pool.Exec(r.Context(), "UPDATE stack_deployments SET status='failed',stage='policy',message='Automatic update canceled because its policy changed.',finished_at=now() WHERE stack_id=$1 AND automatic AND status='queued'", q.StackID)
	if err != nil {
		fail(w, err)
		return
	}
	a.record(r.Context(), u, q.StackID, "update-policy", audit.OutcomeSuccess, map[string]any{"enabled": q.Enabled, "version": q.Version})
	q.Configuration, q.Services, q.Paused = configuration, services, ""
	respond(w, 200, q)
}

// A separate advisory lock coordinates automatic dispatch and policy changes.
func (a *API) updateLock(ctx context.Context) (func(), error) {
	for {
		c, err := a.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		if err = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(731946284)").Scan(&locked); err != nil {
			_ = c.Conn().Close(context.Background())
			c.Release()
			return nil, err
		}
		if !locked {
			c.Release()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
		return func() {
			release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.Exec(release, "SELECT pg_advisory_unlock(731946284)"); err != nil {
				_ = c.Conn().Close(release)
			}
			c.Release()
		}, nil
	}
}
