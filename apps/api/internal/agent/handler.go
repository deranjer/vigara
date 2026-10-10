package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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
	pool *pgxpool.Pool
	auth *auth.Service
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
	Addresses   []inventory.Address         `json:"addresses,omitempty"`
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
	mux.HandleFunc("POST /api/agent/enroll", h.enroll)
	mux.HandleFunc("POST /api/agent/poll", h.poll)
	mux.HandleFunc("POST /api/agent/reports", h.reports)
	mux.HandleFunc("POST /api/agents/enrollment-tokens", h.createEnrollmentToken)
	mux.HandleFunc("POST /api/agents/{agentID}/revoke", h.revoke)
	mux.HandleFunc("POST /api/devices/discovery", h.discover)
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
	deviceID, err := inventory.ResolveDevice(r.Context(), tx, inventory.Observation{DisplayName: req.DisplayName, Identifiers: req.Identifiers, Addresses: req.Addresses, Provenance: "external-discovery", SourceType: "external", SourceID: req.SourceID})
	if errors.Is(err, inventory.ErrAmbiguousIdentity) {
		http.Error(w, "discovery identifiers conflict", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "invalid or unavailable discovery observation", http.StatusBadRequest)
		return
	}
	if err = inventory.AttachSource(r.Context(), tx, deviceID, "external", req.SourceID, "external-discovery"); err != nil {
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
	var allowed []string
	err = tx.QueryRow(r.Context(), `SELECT id::text, capabilities FROM agent_enrollment_tokens WHERE token_hash=$1 AND used_at IS NULL AND expires_at > now() FOR UPDATE`, hash(req.Token)).Scan(&tokenID, &allowed)
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
	deviceID, err = inventory.ResolveDevice(r.Context(), tx, inventory.Observation{DisplayName: req.DisplayName, Identifiers: req.Identifiers, Provenance: "agent-enrollment"})
	if err == nil {
		err = tx.QueryRow(r.Context(), `INSERT INTO agent_identities(device_id, credential_hash, protocol_version, platform, architecture, agent_version, capabilities, allowed_capabilities) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`, deviceID, credentialHash, capability.ProtocolVersion, capability.Platform, capability.Architecture, capability.AgentVersion, capabilitiesJSON, allowed).Scan(&agentID)
	}
	if err == nil {
		err = inventory.AttachSource(r.Context(), tx, deviceID, "agent", agentID, "agent-enrollment")
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE agent_enrollment_tokens SET used_at=now() WHERE id=$1`, tokenID)
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
	writeJSON(w, protocol.AgentResponse{ProtocolVersion: protocol.CurrentVersion, ServerTime: time.Now().UTC(), PollAfterSeconds: 30})
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

func hash(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }

func newCredential() (string, []byte, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(data)
	return token, hash(token), nil
}
