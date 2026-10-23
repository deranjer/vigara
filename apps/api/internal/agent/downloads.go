package agent

import (
	"archive/zip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/serverurl"
	"github.com/jackc/pgx/v5"
)

// ConfigureDownloads sets operator-owned bundle provenance and the reachable
// URL included in bootstrap files. Callers cannot select artifact paths or URLs.
func (h *Handler) ConfigureDownloads(publicURL, releaseDir string) {
	h.ConfigureServerURLs(publicURL, nil, false)
	h.releaseDir = releaseDir
}

// ConfigureServerURLs sets the operator-approved URLs that bundles may point
// agents at: a primary URL (the default) plus optional alternates, for example a
// LAN address and a Tailscale address. Plain HTTP is accepted only when
// allowInsecureHTTP is set and the host is a private-network or Tailscale
// address; anything else must be HTTPS. Entries that fail validation are
// dropped and returned as errors so the caller can report them. Requests can
// only select among these URLs and never supply their own.
func (h *Handler) ConfigureServerURLs(primary string, extra []string, allowInsecureHTTP bool) []error {
	h.allowInsecureHTTP = allowInsecureHTTP
	h.serverURLs = nil
	var rejected []error
	seen := map[string]bool{}
	for _, raw := range append([]string{primary}, extra...) {
		raw = strings.TrimRight(strings.TrimSpace(raw), "/")
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		if err := serverurl.Validate(raw, allowInsecureHTTP); err != nil {
			rejected = append(rejected, fmt.Errorf("%s: %w", raw, err))
			continue
		}
		h.serverURLs = append(h.serverURLs, raw)
	}
	return rejected
}

// selectServerURL returns the requested URL if the operator approved it, or the
// primary configured URL when the request names none.
func (h *Handler) selectServerURL(requested string) (string, bool) {
	requested = strings.TrimRight(strings.TrimSpace(requested), "/")
	if requested == "" {
		if len(h.serverURLs) == 0 {
			return "", false
		}
		return h.serverURLs[0], true
	}
	for _, candidate := range h.serverURLs {
		if candidate == requested {
			return candidate, true
		}
	}
	return "", false
}

type releaseManifest struct {
	Version   string `json:"version"`
	Artifacts map[string]struct {
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

var supportedDownloads = map[string]string{"windows-amd64": "vigara-agent-windows-amd64.exe", "linux-amd64": "vigara-agent-linux-amd64", "linux-arm64": "vigara-agent-linux-arm64"}

var selectableFeatures = map[string]bool{
	"restic.backup":                 true,
	protocol.CapabilityDockerManage: true,
	protocol.CapabilityDockerExec:   true,
	protocol.CapabilityDockerDeploy: true,
}

type boundedBundleWriter struct {
	w         http.ResponseWriter
	remaining int64
}

func (w *boundedBundleWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		n, err := w.w.Write(p[:w.remaining])
		w.remaining -= int64(n)
		if err != nil {
			return n, err
		}
		return n, errors.New("agent bundle exceeded response limit")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func (h *Handler) verifiedArtifact(target string) ([]byte, string, error) {
	if _, ok := supportedDownloads[target]; !ok || h.releaseDir == "" {
		return nil, "", os.ErrNotExist
	}
	manifestPath := filepath.Join(h.releaseDir, "manifest.json")
	manifestBytes, err := readBoundedFile(manifestPath, 1<<20)
	if err != nil {
		return nil, "", err
	}
	var manifest releaseManifest
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Version == "" || len(manifest.Version) > 80 {
		return nil, "", errors.New("invalid release manifest")
	}
	entry, ok := manifest.Artifacts[target]
	expected := supportedDownloads[target]
	legacyName := strings.Replace(expected, "vigara-agent", "vigira-agent", 1)
	if !ok || (entry.File != expected && entry.File != legacyName) || len(entry.SHA256) != 64 {
		return nil, "", os.ErrNotExist
	}
	for _, c := range entry.SHA256 {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, "", errors.New("invalid release digest")
		}
	}
	artifactPath := filepath.Join(h.releaseDir, entry.File)
	data, err := readBoundedFile(artifactPath, 128<<20)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", errors.New("empty release artifact")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != entry.SHA256 {
		return nil, "", errors.New("release digest mismatch")
	}
	return data, manifest.Version, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release file exceeds configured size limit")
	}
	return data, nil
}

func (h *Handler) downloadAvailability(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	available := make([]string, 0, len(supportedDownloads))
	for target := range supportedDownloads {
		if _, _, err := h.verifiedArtifact(target); err == nil {
			available = append(available, target)
		}
	}
	type serverOption struct {
		URL    string `json:"url"`
		Secure bool   `json:"secure"`
	}
	options := make([]serverOption, 0, len(h.serverURLs))
	primary := ""
	for i, candidate := range h.serverURLs {
		if i == 0 {
			primary = candidate
		}
		options = append(options, serverOption{URL: candidate, Secure: serverurl.IsSecure(candidate)})
	}
	writeJSON(w, map[string]any{"serverUrl": primary, "serverUrls": options, "allowInsecureHttp": h.allowInsecureHTTP, "targets": available})
}

type agentDownloadRequest struct {
	DeviceID  string   `json:"deviceId,omitempty"`
	Target    string   `json:"target"`
	ServerURL string   `json:"serverUrl"`
	Name      string   `json:"name"`
	Features  []string `json:"features"`
}

func (h *Handler) createAgentDownload(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var req agentDownloadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.DeviceID != "" && !validUUID(req.DeviceID) {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}
	if len(req.Name) == 0 || len(req.Name) > 120 || strings.ContainsAny(req.Name, "\\/\x00") || len(req.Features) > 128 {
		http.Error(w, "invalid device request", http.StatusBadRequest)
		return
	}
	if len(h.serverURLs) == 0 {
		http.Error(w, "downloads require a configured server URL (HTTPS, or HTTP on a private network when explicitly allowed)", http.StatusServiceUnavailable)
		return
	}
	serverURL, ok := h.selectServerURL(req.ServerURL)
	if !ok {
		http.Error(w, "server URL is not one of the configured URLs", http.StatusBadRequest)
		return
	}
	artifact, version, err := h.verifiedArtifact(req.Target)
	if err != nil {
		http.Error(w, "requested agent bundle is unavailable", http.StatusNotFound)
		return
	}
	seen := map[string]bool{}
	for _, feature := range req.Features {
		if !selectableFeatures[feature] || seen[feature] {
			http.Error(w, "invalid feature selection", http.StatusBadRequest)
			return
		}
		seen[feature] = true
	}
	token, tokenHash, err := newCredential()
	if err != nil {
		http.Error(w, "could not prepare device bundle", 500)
		return
	}
	expires := time.Now().UTC().Add(30 * time.Minute)
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not prepare device bundle", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var replacementID *string
	if req.DeviceID != "" {
		var id, platform, architecture, deviceName string
		err = tx.QueryRow(r.Context(), "SELECT i.id::text,i.platform,i.architecture,d.display_name,i.allowed_capabilities FROM agent_identities i JOIN devices d ON d.id=i.device_id WHERE d.id=$1::uuid AND i.revoked_at IS NULL ORDER BY i.created_at DESC LIMIT 1 FOR UPDATE OF i,d", req.DeviceID).Scan(&id, &platform, &architecture, &deviceName, &req.Features)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "device has no active agent to reinstall", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "could not prepare reinstall bundle", 500)
			return
		}
		if platform+"-"+architecture != req.Target {
			http.Error(w, "reinstall bundle must match the existing device platform", http.StatusConflict)
			return
		}
		req.Name = deviceName
		replacementID = &id
		// Only the newest pending reinstall may rotate this identity's credentials.
		_, err = tx.Exec(r.Context(), "UPDATE agent_download_sessions SET status='canceled' WHERE id IN (SELECT id FROM agent_enrollment_tokens WHERE replacement_agent_id=$1 AND used_at IS NULL) AND status='waiting'", id)
		if err == nil {
			_, err = tx.Exec(r.Context(), "UPDATE agent_enrollment_tokens SET used_at=now() WHERE replacement_agent_id=$1 AND used_at IS NULL", id)
		}
	} else {
		_, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended(lower($1),0))", req.Name)
		var exists bool
		if err == nil {
			err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM devices WHERE lower(btrim(display_name))=lower($1)) OR EXISTS(SELECT 1 FROM agent_download_sessions WHERE lower(btrim(device_name))=lower($1) AND status='waiting' AND expires_at>now())", req.Name).Scan(&exists)
		}
		if err == nil && exists {
			http.Error(w, "This device name is already in use. Open the existing device and choose Reinstall agent, or use a different name.", http.StatusConflict)
			return
		}
	}
	if err != nil {
		http.Error(w, "could not prepare device bundle", 500)
		return
	}
	var sessionID string
	err = tx.QueryRow(r.Context(), `INSERT INTO agent_enrollment_tokens(token_hash,capabilities,expires_at,replacement_agent_id) VALUES($1,$2,$3,$4) RETURNING id::text`, tokenHash, req.Features, expires, replacementID).Scan(&sessionID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO agent_download_sessions(id,device_name,target,version,status,expires_at) VALUES($1,$2,$3,$4,'waiting',$5)`, sessionID, req.Name, req.Target, version, expires)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		http.Error(w, "could not prepare device bundle", 500)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=vigara-agent-"+req.Target+".zip")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Vigira-Session-ID", sessionID)
	w.Header().Set("X-Vigara-Session-ID", sessionID)
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(&boundedBundleWriter{w: w, remaining: 160 << 20})
	add := func(name string, data []byte) error {
		f, e := zw.Create(name)
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		return e
	}
	bootstrap, _ := json.Marshal(map[string]any{"serverUrl": serverURL, "insecureHttp": !serverurl.IsSecure(serverURL), "token": token, "name": req.Name, "sessionId": sessionID, "reinstall": replacementID != nil, "agentId": replacementID})
	binaryName := "vigara-agent"
	if strings.HasPrefix(req.Target, "windows-") {
		binaryName = "vigara-agent.exe"
	}
	if err = add(binaryName, artifact); err == nil {
		err = add("bootstrap.json", bootstrap)
	}
	if err == nil && strings.HasPrefix(req.Target, "linux-") {
		err = add("install.sh", []byte(linuxAgentInstaller))
		if err == nil {
			err = add("vigara-agent.service", []byte(linuxAgentService))
		}
	}
	if err == nil && strings.HasPrefix(req.Target, "windows-") {
		err = add("install.ps1", []byte(windowsAgentInstaller))
		if err == nil {
			err = add("install.cmd", []byte(windowsAgentLauncher))
		}
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return
	}
}

func (h *Handler) agentDownloadStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	var status string
	err := h.pool.QueryRow(r.Context(), `SELECT CASE WHEN status='waiting' AND expires_at<=now() THEN 'expired' ELSE status END FROM agent_download_sessions WHERE id=$1::uuid`, r.PathValue("id")).Scan(&status)
	if err != nil {
		http.Error(w, "download session not found", 404)
		return
	}
	writeJSON(w, map[string]string{"status": status})
}
func (h *Handler) cancelAgentDownload(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	tag, err := h.pool.Exec(r.Context(), `UPDATE agent_download_sessions SET status='canceled' WHERE id=$1::uuid AND status='waiting' AND expires_at>now()`, r.PathValue("id"))
	if err != nil {
		http.Error(w, "could not cancel download", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "waiting download session not found", 404)
		return
	}
	_, _ = h.pool.Exec(r.Context(), `UPDATE agent_enrollment_tokens SET used_at=now() WHERE id=$1::uuid AND used_at IS NULL`, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

//go:embed installers/linux.sh
var linuxAgentInstaller string

//go:embed installers/linux.service
var linuxAgentService string

//go:embed installers/windows.ps1
var windowsAgentInstaller string

//go:embed installers/windows.cmd
var windowsAgentLauncher string
