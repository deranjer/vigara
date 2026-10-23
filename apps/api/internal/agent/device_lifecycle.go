package agent

import (
	"errors"
	"net/http"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !h.requireUser(w, r) {
		return false
	}
	cookie, _ := r.Cookie(auth.SessionCookieName)
	user, err := h.auth.Authenticate(r.Context(), cookie.Value)
	if err != nil || user.Role != auth.RoleAdmin {
		http.Error(w, "administrator access required", http.StatusForbidden)
		return false
	}
	return true
}

func (h *Handler) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("deviceID")
	if !validUUID(id) {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not delete device", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var name string
	err = tx.QueryRow(r.Context(), "SELECT display_name FROM devices WHERE id=$1 FOR UPDATE", id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "device not found", 404)
		return
	}
	if err != nil {
		http.Error(w, "could not delete device", 500)
		return
	}
	if req.Name != name {
		http.Error(w, "Type the exact device name to confirm deletion.", http.StatusConflict)
		return
	}
	// Fence new jobs and credential changes while checking dependent work.
	_, err = tx.Exec(r.Context(), "SELECT id FROM agent_identities WHERE device_id=$1 ORDER BY id FOR UPDATE", id)
	var active bool
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT
     EXISTS(SELECT 1 FROM backup_jobs j JOIN agent_identities a ON a.id=j.agent_id WHERE a.device_id=$1 AND j.status IN ('leased','unknown'))
     OR EXISTS(SELECT 1 FROM stack_deployments s JOIN agent_identities a ON a.id=s.agent_id WHERE a.device_id=$1 AND s.status IN ('queued','running','unknown'))`, id).Scan(&active)
	}
	if err != nil {
		http.Error(w, "could not check active device work", 500)
		return
	}
	if active {
		http.Error(w, "Resolve active or uncertain backup jobs and stack deployments before deleting this device.", http.StatusConflict)
		return
	}
	for _, statement := range []string{
		"DELETE FROM backup_job_attempts WHERE job_id IN (SELECT j.id FROM backup_jobs j JOIN agent_identities a ON a.id=j.agent_id WHERE a.device_id=$1)",
		"DELETE FROM backup_jobs WHERE agent_id IN (SELECT id FROM agent_identities WHERE device_id=$1)",
		"DELETE FROM stack_deployments WHERE agent_id IN (SELECT id FROM agent_identities WHERE device_id=$1) OR stack_id IN (SELECT stack_id FROM stack_bindings WHERE agent_id IN (SELECT id FROM agent_identities WHERE device_id=$1))",
		"DELETE FROM stack_bindings WHERE agent_id IN (SELECT id FROM agent_identities WHERE device_id=$1)",
		"DELETE FROM alert_incidents WHERE dimension=$1::text OR dimension LIKE $1::text || '/%'",
		"DELETE FROM devices WHERE id=$1",
	} {
		if _, err = tx.Exec(r.Context(), statement, id); err != nil {
			http.Error(w, "could not delete device; nothing was removed", 500)
			return
		}
	}
	cookie, _ := r.Cookie(auth.SessionCookieName)
	user, err := h.auth.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		http.Error(w, "login required", 401)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(actor_user_id,actor_name,action,target_kind,target_id,outcome,detail) VALUES($1,$2,'device.delete','device',$3,'success','{}')`, user.ID, user.Username, id)
	if err != nil {
		http.Error(w, "could not record device deletion; nothing was removed", 500)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		http.Error(w, "could not delete device", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
