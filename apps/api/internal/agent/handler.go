package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/inventory"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxRequestBytes = 1 << 20

// Handler serves the device-facing API. Device credentials are random bearer
// secrets; only their SHA-256 hashes are stored in PostgreSQL.
type Handler struct {
	pool       *pgxpool.Pool
	auth       *auth.Service
	releaseDir string
	// serverURLs are the validated, operator-approved URLs for agent bundles; the
	// first is the default. allowInsecureHTTP records the explicit plain-HTTP opt-in.
	serverURLs        []string
	allowInsecureHTTP bool
}

func NewHandler(pool *pgxpool.Pool, authService *auth.Service) *Handler {
	return &Handler{pool: pool, auth: authService}
}

type EnrollmentRequest struct {
	Token       string                      `json:"token"`
	DisplayName string                      `json:"displayName"`
	Capability  protocol.CapabilityReport   `json:"capability"`
	Identifiers []protocol.DeviceIdentifier `json:"identifiers,omitempty"`
}

type DiscoveryRequest struct {
	SourceID    string                      `json:"sourceId"`
	DisplayName string                      `json:"displayName"`
	Identifiers []protocol.DeviceIdentifier `json:"identifiers,omitempty"`
	Addresses   []DiscoveryAddress          `json:"addresses,omitempty"`
}

type DiscoveryAddress struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type EnrollmentResponse struct {
	AgentID    string `json:"agentId"`
	Credential string `json:"credential"`
}

type ReportBatch struct {
	Results   []protocol.JobResult      `json:"results,omitempty"`
	Telemetry []protocol.TelemetryEvent `json:"telemetry,omitempty"`
}

type EnrollmentTokenRequest struct {
	Capabilities   []string `json:"capabilities"`
	ExpiresInHours int      `json:"expiresInHours"`
}

type EnrollmentTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agent-downloads/availability", h.downloadAvailability)
	mux.HandleFunc("POST /api/agent-downloads", h.createAgentDownload)
	mux.HandleFunc("GET /api/agent-downloads/{id}", h.agentDownloadStatus)
	mux.HandleFunc("POST /api/agent-downloads/{id}/cancel", h.cancelAgentDownload)
	mux.HandleFunc("POST /api/agent/enroll", h.enroll)
	mux.HandleFunc("POST /api/agent/poll", h.poll)
	mux.HandleFunc("POST /api/agent/reports", h.reports)
	mux.HandleFunc("POST /api/agent/backups/progress", h.backupProgress)
	mux.HandleFunc("POST /api/agents/enrollment-tokens", h.createEnrollmentToken)
	mux.HandleFunc("POST /api/agents/{agentID}/revoke", h.revoke)
	mux.HandleFunc("POST /api/devices/discovery", h.discover)
	mux.HandleFunc("DELETE /api/devices/{deviceID}", h.deleteDevice)
}

func (h *Handler) backupProgress(w http.ResponseWriter, r *http.Request) {
	var p protocol.BackupProgress
	if !decodeJSON(w, r, &p) {
		return
	}
	agentID, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	if p.ProtocolVersion != protocol.CurrentVersion || p.AgentID != agentID || p.ID == "" || len(p.ID) > 128 || !validUUID(p.JobID) || !validUUID(p.AttemptToken) || p.Sequence == 0 || p.Sequence > 1_000_000_000_000 || p.Phase != "starting" && p.Phase != "validating" && p.Phase != "backup" || p.FilesDone > 1_000_000_000_000 || p.FilesTotal > 1_000_000_000_000 || p.BytesDone > 1<<60 || p.BytesTotal > 1<<60 || p.FilesTotal > 0 && p.FilesDone > p.FilesTotal || p.BytesTotal > 0 && p.BytesDone > p.BytesTotal || math.IsNaN(p.PercentDone) || math.IsInf(p.PercentDone, 0) || p.PercentDone < 0 || p.PercentDone > 100 || p.ObservedAt.IsZero() || p.ObservedAt.After(time.Now().UTC().Add(2*time.Minute)) || p.ObservedAt.Before(time.Now().UTC().Add(-5*time.Minute)) {
		http.Error(w, "invalid backup progress", http.StatusBadRequest)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not accept backup progress", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var attemptID string
	var sequence int64
	var oldPayload []byte
	var progressAt *time.Time
	err = tx.QueryRow(r.Context(), `SELECT a.id::text,a.progress_sequence,a.progress_payload,a.progress_at FROM backup_job_attempts a JOIN backup_jobs j ON j.id=a.job_id WHERE a.job_id=$1 AND a.lease_token=$2::uuid AND j.agent_id=$3 AND j.status='leased' AND a.finished_at IS NULL AND a.lease_expires_at>now() FOR UPDATE OF a,j`, p.JobID, p.AttemptToken, agentID).Scan(&attemptID, &sequence, &oldPayload, &progressAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "backup attempt is no longer active", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "could not accept backup progress", 500)
		return
	}
	if p.Sequence == uint64(sequence) {
		var existing protocol.BackupProgress
		if json.Unmarshal(oldPayload, &existing) == nil && reflect.DeepEqual(existing, p) {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.Error(w, "conflicting backup progress sequence", http.StatusConflict)
		return
	}
	if p.Sequence < uint64(sequence) {
		http.Error(w, "stale backup progress sequence", http.StatusConflict)
		return
	}
	if progressAt != nil && time.Since(*progressAt) < 5*time.Second {
		http.Error(w, "backup progress rate limited", http.StatusTooManyRequests)
		return
	}
	payload, _ := json.Marshal(p)
	_, err = tx.Exec(r.Context(), `UPDATE backup_job_attempts SET progress_sequence=$2,progress_payload=$3,progress_at=now(),lease_expires_at=now()+($4 * interval '1 second') WHERE id=$1 AND finished_at IS NULL AND lease_expires_at>now()`, attemptID, int64(p.Sequence), payload, int(backupLease/time.Second))
	if err != nil {
		http.Error(w, "could not accept backup progress", 500)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		http.Error(w, "could not accept backup progress", 500)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// discover accepts an authenticated external inventory observation. The source
// key is scoped to its source type and cannot move an existing source record.
func (h *Handler) discover(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	var req DiscoveryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.SourceID) == "" || len(req.SourceID) > 256 {
		http.Error(w, "invalid discovery source", http.StatusBadRequest)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not reconcile discovery", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())
	provenance := inventory.SourceProvenance("external", req.SourceID)
	addresses := make([]inventory.Address, 0, len(req.Addresses))
	for _, address := range req.Addresses {
		addresses = append(addresses, inventory.Address{Value: address.Value, Type: address.Type, Provenance: provenance})
	}
	deviceID, err := inventory.ResolveDevice(r.Context(), tx, inventory.Observation{DisplayName: req.DisplayName, Identifiers: req.Identifiers, Addresses: addresses, Provenance: provenance, SourceType: "external", SourceID: req.SourceID})
	if errors.Is(err, inventory.ErrAmbiguousIdentity) {
		http.Error(w, "discovery identifiers conflict", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "invalid or unavailable discovery observation", http.StatusBadRequest)
		return
	}
	if err = inventory.AttachSource(r.Context(), tx, deviceID, "external", req.SourceID, provenance); err != nil {
		http.Error(w, "could not record discovery source", http.StatusConflict)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		http.Error(w, "could not reconcile discovery", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"deviceId": deviceID})
}

func (h *Handler) createEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	var req EnrollmentTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ExpiresInHours < 1 || req.ExpiresInHours > 720 || len(req.Capabilities) > 128 {
		http.Error(w, "invalid enrollment token scope or lifetime", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, item := range req.Capabilities {
		if item == "" || len(item) > 128 || seen[item] {
			http.Error(w, "invalid enrollment token capabilities", http.StatusBadRequest)
			return
		}
		seen[item] = true
	}
	token, tokenHash, err := newCredential()
	if err != nil {
		http.Error(w, "could not create enrollment token", http.StatusInternalServerError)
		return
	}
	expires := time.Now().UTC().Add(time.Duration(req.ExpiresInHours) * time.Hour)
	if _, err := h.pool.Exec(r.Context(), `INSERT INTO agent_enrollment_tokens(token_hash, capabilities, expires_at) VALUES($1,$2,$3)`, tokenHash, req.Capabilities, expires); err != nil {
		http.Error(w, "could not create enrollment token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, EnrollmentTokenResponse{Token: token, ExpiresAt: expires})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	id := r.PathValue("agentID")
	if id == "" {
		http.Error(w, "agent identity is required", http.StatusBadRequest)
		return
	}
	commandTag, err := h.pool.Exec(r.Context(), `UPDATE agent_identities SET revoked_at=COALESCE(revoked_at, now()) WHERE id=$1`, id)
	if err != nil {
		http.Error(w, "could not revoke agent identity", http.StatusInternalServerError)
		return
	}
	if commandTag.RowsAffected() == 0 {
		http.Error(w, "agent identity not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || u.Scheme != "https" && u.Scheme != "http" {
			http.Error(w, "request origin rejected", http.StatusForbidden)
			return false
		}
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		http.Error(w, "login required", http.StatusUnauthorized)
		return false
	}
	if _, err := h.auth.Authenticate(r.Context(), cookie.Value); err != nil {
		http.Error(w, "login required", http.StatusUnauthorized)
		return false
	}
	return true
}

func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	var req EnrollmentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	capability := req.Capability
	if len(req.Token) < 32 || len(req.Token) > 128 || len(req.DisplayName) == 0 || len(req.DisplayName) > 120 || capability.ProtocolVersion != protocol.CurrentVersion || capability.Platform == "" || capability.Architecture == "" || capability.AgentVersion == "" || len(capability.Capabilities) > 128 {
		http.Error(w, "invalid enrollment request", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, c := range capability.Capabilities {
		if c == "" || len(c) > 128 || seen[c] {
			http.Error(w, "invalid capability report", http.StatusBadRequest)
			return
		}
		seen[c] = true
	}
	credential, credentialHash, err := newCredential()
	if err != nil {
		http.Error(w, "could not enroll agent", http.StatusInternalServerError)
		return
	}
	capabilitiesJSON, _ := json.Marshal(capability.Capabilities)
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not enroll agent", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())
	var tokenID string
	var replacementID *string
	var allowed []string
	err = tx.QueryRow(r.Context(), `SELECT id::text, capabilities, replacement_agent_id::text FROM agent_enrollment_tokens WHERE token_hash=$1 AND used_at IS NULL AND expires_at > now() FOR UPDATE`, hash(req.Token)).Scan(&tokenID, &allowed, &replacementID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invalid or expired enrollment token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "could not enroll agent", http.StatusInternalServerError)
		return
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, item := range allowed {
		allowedSet[item] = true
	}
	for _, item := range capability.Capabilities {
		if !allowedSet[item] {
			http.Error(w, "agent reported a capability outside the enrollment scope", http.StatusForbidden)
			return
		}
	}
	var deviceID, agentID string
	if replacementID != nil {
		agentID = *replacementID
		var platform, architecture, deviceName string
		err = tx.QueryRow(r.Context(), "SELECT i.device_id::text,i.platform,i.architecture,d.display_name FROM agent_identities i JOIN devices d ON d.id=i.device_id WHERE i.id=$1 AND i.revoked_at IS NULL FOR UPDATE OF i,d", agentID).Scan(&deviceID, &platform, &architecture, &deviceName)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "agent is no longer available for reinstallation", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "could not reinstall agent", 500)
			return
		}
		if capability.Platform != platform || capability.Architecture != architecture {
			http.Error(w, "reinstall bundle belongs to a different device platform", http.StatusConflict)
			return
		}
		resolved, resolveErr := inventory.ResolveDevice(r.Context(), tx, inventory.Observation{DisplayName: deviceName, Identifiers: req.Identifiers, Provenance: inventory.SourceProvenance("agent", agentID), SourceType: "agent", SourceID: agentID})
		if resolveErr != nil || resolved != deviceID {
			http.Error(w, "device identifiers conflict with the selected device", http.StatusConflict)
			return
		}
		_, err = tx.Exec(r.Context(), "UPDATE agent_identities SET credential_hash=$2,protocol_version=$3,agent_version=$4,capabilities=$5 WHERE id=$1", agentID, credentialHash, capability.ProtocolVersion, capability.AgentVersion, capabilitiesJSON)
	} else {
		_, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended(lower($1),0))", req.DisplayName)
		var exists bool
		if err == nil {
			err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM devices WHERE lower(btrim(display_name))=lower($1))", req.DisplayName).Scan(&exists)
		}
		if err == nil && exists {
			http.Error(w, "device name already exists; use Reinstall agent from device info", http.StatusConflict)
			return
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), "SELECT gen_random_uuid()::text").Scan(&agentID)
		}
		if err == nil {
			deviceID, err = inventory.ResolveDevice(r.Context(), tx, inventory.Observation{DisplayName: req.DisplayName, Identifiers: req.Identifiers, Provenance: inventory.SourceProvenance("agent", agentID)})
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), "INSERT INTO agent_identities(id,device_id,credential_hash,protocol_version,platform,architecture,agent_version,capabilities,allowed_capabilities) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)", agentID, deviceID, credentialHash, capability.ProtocolVersion, capability.Platform, capability.Architecture, capability.AgentVersion, capabilitiesJSON, allowed)
		}
		if err == nil {
			err = inventory.AttachSource(r.Context(), tx, deviceID, "agent", agentID, inventory.SourceProvenance("agent", agentID))
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE agent_enrollment_tokens SET used_at=now() WHERE id=$1`, tokenID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE agent_download_sessions SET status='enrolled' WHERE id=$1 AND status='waiting' AND expires_at>now()`, tokenID)
	}
	if err != nil {
		http.Error(w, "could not enroll agent", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "could not enroll agent", http.StatusInternalServerError)
		return
	}
	writeJSON(w, EnrollmentResponse{AgentID: agentID, Credential: credential})
}

func (h *Handler) poll(w http.ResponseWriter, r *http.Request) {
	var req protocol.AgentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ProtocolVersion != protocol.CurrentVersion || req.AgentID == "" {
		http.Error(w, "unsupported protocol or missing agent id", http.StatusBadRequest)
		return
	}
	agentID, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	if req.AgentID != agentID {
		http.Error(w, "credential does not belong to this agent", http.StatusForbidden)
		return
	}
	var platform, architecture string
	var allowed []string
	if err := h.pool.QueryRow(r.Context(), `SELECT platform, architecture, allowed_capabilities FROM agent_identities WHERE id=$1`, agentID).Scan(&platform, &architecture, &allowed); err != nil {
		http.Error(w, "could not read agent scope", http.StatusInternalServerError)
		return
	}
	if req.Platform != platform || req.Architecture != architecture {
		http.Error(w, "agent platform identity cannot be changed", http.StatusForbidden)
		return
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, item := range allowed {
		allowedSet[item] = true
	}
	for _, item := range req.Capabilities {
		if !allowedSet[item] {
			http.Error(w, "agent capability exceeds enrollment scope", http.StatusForbidden)
			return
		}
	}
	capabilities, _ := json.Marshal(req.Capabilities)
	commandTag, err := h.pool.Exec(r.Context(), `UPDATE devices d SET last_seen_at=now() FROM agent_identities i WHERE i.id=$1 AND d.id=i.device_id`, agentID)
	if err == nil && commandTag.RowsAffected() == 1 {
		_, err = h.pool.Exec(r.Context(), `UPDATE agent_identities SET agent_version=$1, capabilities=$2 WHERE id=$3`, req.AgentVersion, capabilities, agentID)
	}
	if err != nil || commandTag.RowsAffected() != 1 {
		http.Error(w, "could not update agent status", http.StatusInternalServerError)
		return
	}
	job, leaseErr := leaseBackupJob(r.Context(), h.pool, agentID)
	if leaseErr != nil {
		http.Error(w, "could not coordinate backup job", http.StatusInternalServerError)
		return
	}
	if req.SupportsBackupProgress && job != nil {
		job.TimeoutSeconds = int(backupExecutionLimit / time.Second)
		job.Deadline = time.Now().UTC().Add(backupExecutionLimit)
	}
	configuration := &protocol.AgentConfiguration{Version: "backup-v1", GeneratedAt: time.Now().UTC(), Modules: []protocol.ModuleConfiguration{{ModuleID: "restic.backup", ModuleVersion: "1.0.0", Enabled: true}}}
	writeJSON(w, protocol.AgentResponse{ProtocolVersion: protocol.CurrentVersion, ServerTime: time.Now().UTC(), PollAfterSeconds: 30, Configuration: configuration, Job: job, BackupProgressEnabled: req.SupportsBackupProgress})
}

func (h *Handler) reports(w http.ResponseWriter, r *http.Request) {
	var batch ReportBatch
	if !decodeJSON(w, r, &batch) {
		return
	}
	agentID, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	if len(batch.Results)+len(batch.Telemetry) == 0 || len(batch.Results)+len(batch.Telemetry) > 100 {
		http.Error(w, "report batch must contain between 1 and 100 records", http.StatusBadRequest)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not store reports", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())
	for _, result := range batch.Results {
		if result.ID == "" || result.AgentID != agentID || result.ProtocolVersion != protocol.CurrentVersion {
			http.Error(w, "invalid job result identity", http.StatusBadRequest)
			return
		}
		if result.ModuleID == "restic.backup" {
			if err = recordBackupResult(r.Context(), tx, agentID, result); err != nil {
				http.Error(w, "invalid or conflicting backup result", http.StatusConflict)
				return
			}
		}
		payload, _ := json.Marshal(result)
		if _, err = tx.Exec(r.Context(), `INSERT INTO agent_reports(agent_id, report_id, payload) VALUES($1,$2,$3) ON CONFLICT(agent_id,report_id) DO NOTHING`, agentID, result.ID, payload); err != nil {
			break
		}
	}
	if err == nil {
		for _, event := range batch.Telemetry {
			if event.ID == "" {
				err = errors.New("missing telemetry id")
				break
			}
			payload, _ := json.Marshal(event)
			if _, err = tx.Exec(r.Context(), `INSERT INTO agent_reports(agent_id, report_id, payload) VALUES($1,$2,$3) ON CONFLICT(agent_id,report_id) DO NOTHING`, agentID, event.ID, payload); err != nil {
				break
			}
		}
	}
	if err != nil {
		http.Error(w, "could not store reports", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "could not store reports", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) authenticatedAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
		http.Error(w, "agent credential required", http.StatusUnauthorized)
		return "", false
	}
	var id string
	err := h.pool.QueryRow(r.Context(), `SELECT id::text FROM agent_identities WHERE credential_hash=$1 AND revoked_at IS NULL`, hash(strings.TrimPrefix(header, "Bearer "))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invalid or revoked agent credential", http.StatusUnauthorized)
		return "", false
	}
	if err != nil {
		http.Error(w, "could not authenticate agent", http.StatusInternalServerError)
		return "", false
	}
	return id, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "request must contain one JSON value", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// AuthenticateAgentRequest resolves the agent behind a bearer credential without
// writing a response, for endpoints such as the live channel that answer with
// their own protocol.
func (h *Handler) AuthenticateAgentRequest(r *http.Request) (string, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
		return "", errors.New("agent credential required")
	}
	var id string
	err := h.pool.QueryRow(r.Context(), `SELECT id::text FROM agent_identities WHERE credential_hash=$1 AND revoked_at IS NULL`, hash(strings.TrimPrefix(header, "Bearer "))).Scan(&id)
	if err != nil {
		return "", errors.New("invalid or revoked agent credential")
	}
	return id, nil
}

// AgentCapabilities returns the capabilities the server accepted for an agent.
func (h *Handler) AgentCapabilities(ctx context.Context, agentID string) ([]string, error) {
	var raw []byte
	if err := h.pool.QueryRow(ctx, `SELECT capabilities FROM agent_identities WHERE id=$1 AND revoked_at IS NULL`, agentID).Scan(&raw); err != nil {
		return nil, err
	}
	var capabilities []string
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		return nil, err
	}
	return capabilities, nil
}

func hash(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func newCredential() (string, []byte, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(data)
	return token, hash(token), nil
}
