package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) updateArtifact(target string) ([]byte, *protocol.AgentUpdatePlan, error) {
	h.releaseFilesMu.RLock()
	defer h.releaseFilesMu.RUnlock()
	directory := h.artifactDirectory()
	if directory == "" {
		return nil, nil, errors.New("no agent releases configured")
	}
	manifestBytes, err := readBoundedFile(filepath.Join(directory, "manifest.json"), 1<<20)
	var manifest releaseManifest
	if err != nil || json.Unmarshal(manifestBytes, &manifest) != nil || manifest.SelfUpdateProtocol != "v1" || manifest.ProtocolVersion != protocol.CurrentVersion || manifest.ResticVersion != restic.PinnedVersion {
		return nil, nil, errors.New("package does not support this updater, agent protocol, or bundled Restic")
	}
	data, version, err := verifiedArtifactIn(directory, target)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	return data, &protocol.AgentUpdatePlan{Version: version, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}, nil
}

func (h *Handler) deviceUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.requireUser(w, r) {
			return
		}
	} else if !h.requireAdmin(w, r) {
		return
	}
	if !validUUID(r.PathValue("deviceID")) {
		http.Error(w, "invalid device ID", 400)
		return
	}
	var id, platform, arch, version string
	if err := h.pool.QueryRow(r.Context(), `SELECT id::text,platform,architecture,agent_version FROM agent_identities WHERE device_id=$1 AND revoked_at IS NULL`, r.PathValue("deviceID")).Scan(&id, &platform, &arch, &version); err != nil {
		http.Error(w, "enrolled agent not found", 404)
		return
	}
	_, available, _ := h.updateArtifact(platform + "-" + arch)
	if r.Method == http.MethodPost {
		var req struct {
			Automatic *bool `json:"automatic"`
			UpdateNow bool  `json:"updateNow"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Automatic == nil && !req.UpdateNow {
			http.Error(w, "choose an update action", 400)
			return
		}
		if req.UpdateNow && available == nil {
			http.Error(w, "no verified compatible package available", 409)
			return
		}
		digest := ""
		if req.UpdateNow {
			digest = available.SHA256
		}
		_, err := h.pool.Exec(r.Context(), `INSERT INTO agent_updates(agent_id,automatic,requested_digest,generation) VALUES($1,COALESCE($2,false),$3,CASE WHEN $4 THEN 1 ELSE 0 END)
ON CONFLICT(agent_id) DO UPDATE SET automatic=COALESCE($2,agent_updates.automatic),requested_digest=CASE WHEN $4 THEN $3 ELSE agent_updates.requested_digest END,generation=agent_updates.generation+CASE WHEN $4 THEN 1 ELSE 0 END`, id, req.Automatic, digest, req.UpdateNow)
		if err != nil {
			http.Error(w, "could not save update policy", 500)
			return
		}
	}
	var automatic bool
	var status json.RawMessage
	var reported *time.Time
	err := h.pool.QueryRow(r.Context(), `SELECT automatic,status,reported_at FROM agent_updates WHERE agent_id=$1`, id).Scan(&automatic, &status, &reported)
	if errors.Is(err, pgx.ErrNoRows) {
		status = json.RawMessage(`{"state":"not_installed"}`)
	} else if err != nil {
		http.Error(w, "could not read update policy", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, _ := r.Cookie(auth.SessionCookieName)
	user, _ := h.auth.Authenticate(r.Context(), cookie.Value)
	writeJSON(w, map[string]any{"automatic": automatic, "currentVersion": version, "available": available, "status": status, "reportedAt": reported, "canManage": user.Role == auth.RoleAdmin})
}

func (h *Handler) agentUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	var target string
	var automatic bool
	var requested string
	var generation int64
	err := h.pool.QueryRow(r.Context(), `SELECT i.platform||'-'||i.architecture,COALESCE(u.automatic,false),COALESCE(u.requested_digest,''),COALESCE(u.generation,0) FROM agent_identities i LEFT JOIN agent_updates u ON u.agent_id=i.id WHERE i.id=$1`, id).Scan(&target, &automatic, &requested, &generation)
	if err != nil {
		http.Error(w, "could not read update plan", 500)
		return
	}
	_, plan, err := h.updateArtifact(target)
	if err != nil || (!automatic && (plan == nil || requested != plan.SHA256)) {
		w.WriteHeader(204)
		return
	}
	plan.Generation = generation
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, plan)
}

func (h *Handler) agentUpdatePackage(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	var target string
	if h.pool.QueryRow(r.Context(), `SELECT platform||'-'||architecture FROM agent_identities WHERE id=$1`, id).Scan(&target) != nil {
		http.Error(w, "agent not found", 404)
		return
	}
	data, plan, err := h.updateArtifact(target)
	if err != nil || plan.SHA256 != r.PathValue("digest") {
		http.Error(w, "package changed; check update plan again", 409)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

func (h *Handler) agentUpdateStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	var status protocol.AgentUpdateStatus
	if !decodeJSON(w, r, &status) {
		return
	}
	valid := map[string]bool{"idle": true, "downloading": true, "waiting": true, "installing": true, "checking": true, "updated": true, "rolled_back": true, "failed": true}
	if !valid[status.State] || len(status.Message) > 300 || len(status.Version) > 80 || len(status.SHA256) > 64 || status.Generation < 0 {
		http.Error(w, "invalid update status", 400)
		return
	}
	payload, _ := json.Marshal(status)
	if _, err := h.pool.Exec(r.Context(), `INSERT INTO agent_updates(agent_id,status,reported_at) VALUES($1,$2,now()) ON CONFLICT(agent_id) DO UPDATE SET status=$2,reported_at=now()`, id, payload); err != nil {
		http.Error(w, "could not record update status", 500)
		return
	}
	w.WriteHeader(204)
}

func (h *Handler) agentUpdateHealth(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("version") == "" {
		var lastSeen *time.Time
		if err := h.pool.QueryRow(r.Context(), `SELECT d.last_seen_at FROM devices d JOIN agent_identities i ON i.device_id=d.id WHERE i.id=$1`, id).Scan(&lastSeen); err != nil {
			http.Error(w, "could not read health baseline", 500)
			return
		}
		if lastSeen == nil {
			zero := time.Unix(0, 0).UTC()
			lastSeen = &zero
		}
		writeJSON(w, map[string]any{"lastSeenAt": lastSeen})
		return
	}
	since, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, "invalid health timestamp", 400)
		return
	}
	var healthy bool
	err = h.pool.QueryRow(r.Context(), `SELECT i.agent_version=$2 AND d.last_seen_at>$3 FROM agent_identities i JOIN devices d ON d.id=i.device_id WHERE i.id=$1`, id, r.URL.Query().Get("version"), since).Scan(&healthy)
	if err != nil {
		http.Error(w, "could not check update health", 500)
		return
	}
	writeJSON(w, map[string]bool{"healthy": healthy})
}
