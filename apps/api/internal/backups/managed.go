package backups

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/restic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ManagedInput describes a single server-managed profile per device. Repository
// and password are fixed after provisioning; paths and schedule can be changed.
type ManagedInput struct {
	Repository       string   `json:"repository"`
	ServerRepository string   `json:"serverRepository"`
	SourcePaths      []string `json:"sourcePaths"`
	Password         string   `json:"password,omitempty"`
	Initialize       bool     `json:"initialize"`
	Schedule         Schedule `json:"schedule"`
	Enabled          bool     `json:"enabled"`
	ExpectedRevision int64    `json:"expectedRevision"`
}

type ManagedView struct {
	Configured      bool          `json:"configured"`
	Initialized     bool          `json:"initialized"`
	Available       bool          `json:"available"`
	AgentID         string        `json:"agentId,omitempty"`
	Revision        int64         `json:"revision"`
	AppliedRevision int64         `json:"appliedRevision"`
	ErrorCode       string        `json:"errorCode,omitempty"`
	ReportedAt      *time.Time    `json:"reportedAt,omitempty"`
	Config          *ManagedInput `json:"config,omitempty"`
}

type Manager struct {
	pool       *pgxpool.Pool
	aead       cipher.AEAD
	executable string
	operations chan struct{}
}

// NewManager accepts a base64-encoded 32-byte master key kept outside PostgreSQL.
// An unset key disables managed provisioning, without affecting local profiles.
func NewManager(pool *pgxpool.Pool, key, executable string) (*Manager, error) {
	m := &Manager{pool: pool, executable: executable, operations: make(chan struct{}, 2)}
	if key == "" {
		return m, nil
	}
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return nil, errors.New("VIGIRA_BACKUP_MASTER_KEY must encode exactly 32 bytes in base64")
	}
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	m.aead, err = cipher.NewGCM(block)
	return m, err
}

func (m *Manager) Available() bool { return m != nil && m.aead != nil }

func (m *Manager) Seal(v ManagedInput) ([]byte, error) {
	if !m.Available() {
		return nil, errors.New("managed backups require VIGIRA_BACKUP_MASTER_KEY")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return m.aead.Seal(nonce, nonce, data, []byte("vigara-managed-backup-v1")), nil
}

func (m *Manager) Open(data []byte) (ManagedInput, error) {
	var v ManagedInput
	if !m.Available() || len(data) < m.aead.NonceSize() {
		return v, errors.New("backup secret unavailable")
	}
	n := m.aead.NonceSize()
	plain, err := m.aead.Open(nil, data[:n], data[n:], []byte("vigara-managed-backup-v1"))
	if err != nil {
		return v, errors.New("backup secret unavailable")
	}
	err = json.Unmarshal(plain, &v)
	return v, err
}

func absoluteHostPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func ValidateManaged(v ManagedInput) error {
	if !absoluteHostPath(v.Repository) || len(v.Repository) > 2048 || strings.ContainsRune(v.Repository, 0) {
		return errors.New("repository must be an absolute local path or network share")
	}
	if v.ServerRepository != "" && (!filepath.IsAbs(v.ServerRepository) || len(v.ServerRepository) > 2048 || strings.ContainsRune(v.ServerRepository, 0)) {
		return errors.New("server repository must be an absolute path on the server")
	}
	if len(v.SourcePaths) == 0 || len(v.SourcePaths) > 128 {
		return errors.New("select between 1 and 128 source paths")
	}
	for _, p := range v.SourcePaths {
		if !absoluteHostPath(p) || len(p) > 4096 || strings.ContainsRune(p, 0) {
			return errors.New("source paths must be absolute paths on the device")
		}
	}
	if len(v.Password) > 8192 || strings.ContainsRune(v.Password, 0) {
		return errors.New("invalid repository password")
	}
	p := Policy{ProfileID: "managed", Name: "Managed backup", Destination: v.Repository, IncludedPaths: v.SourcePaths, ExcludedPaths: []string{}, Schedule: v.Schedule, MissedRunPolicy: "run_once", DisconnectedDevicePolicy: "wait"}
	return validate(p)
}

func (m *Manager) Prepare(v ManagedInput) ([]byte, error) {
	if err := ValidateManaged(v); err != nil {
		return nil, err
	}
	if v.Initialize {
		if v.Password == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return nil, err
			}
			v.Password = base64.RawURLEncoding.EncodeToString(b)
		}
	} else if strings.TrimSpace(v.Password) == "" {
		return nil, errors.New("existing repositories require their password")
	}
	return m.Seal(v)
}

// Install is called within the enrollment transaction; no secret is put into a
// policy, bootstrap archive, job snapshot, or API response.
func (m *Manager) Install(ctx context.Context, tx pgx.Tx, agentID, deviceID string, sealed []byte) error {
	v, err := m.Open(sealed)
	if err != nil {
		return err
	}
	if err = ValidateManaged(v); err != nil {
		return err
	}
	next, err := nextOccurrence(v.Schedule, time.Now().UTC())
	if err != nil {
		return err
	}
	paths, _ := json.Marshal(v.SourcePaths)
	schedule, _ := json.Marshal(v.Schedule)
	var policyID string
	err = tx.QueryRow(ctx, `INSERT INTO backup_policies(device_id,agent_id,profile_id,name,destination,included_paths,excluded_paths,retention,schedule,missed_run_policy,disconnected_device_policy,enabled,next_run_at) VALUES($1,$2,'managed','Managed backup',$3,$4,'[]','{}',$5,'run_once','wait',$6,$7) RETURNING id::text`, deviceID, agentID, v.Repository, paths, schedule, v.Enabled, next).Scan(&policyID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_backup_profiles(agent_id,policy_id,config) VALUES($1,$2,$3)`, agentID, policyID, sealed)
	return err
}

// Poll reports the previous revision before delivering desired state. A job
// cannot run until the same revision is acknowledged as ready.
func (m *Manager) Poll(ctx context.Context, agentID string, status *protocol.ManagedBackupStatus) (*protocol.ManagedBackup, bool, error) {
	if !m.Available() {
		return nil, false, nil
	}
	if status != nil {
		code := status.ErrorCode
		switch code {
		case "", "invalid_configuration", "source_unavailable", "repository_unavailable", "restic_unavailable", "state_unavailable":
		default:
			code = "configuration_failed"
		}
		_, err := m.pool.Exec(ctx, `UPDATE managed_backup_profiles SET applied_revision=CASE WHEN $3='' THEN $2 ELSE applied_revision END, initialized=initialized OR $3='',error_code=$3,reported_at=now() WHERE agent_id=$1 AND revision=$2`, agentID, status.Revision, code)
		if err != nil {
			return nil, false, err
		}
	}
	var sealed []byte
	var revision, applied int64
	var initialized bool
	var code string
	err := m.pool.QueryRow(ctx, `SELECT config,revision,applied_revision,initialized,error_code FROM managed_backup_profiles WHERE agent_id=$1`, agentID).Scan(&sealed, &revision, &applied, &initialized, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := m.Open(sealed)
	if err != nil {
		return nil, false, err
	}
	return &protocol.ManagedBackup{Revision: revision, Repository: v.Repository, SourcePaths: v.SourcePaths, Password: v.Password, Initialize: v.Initialize && !initialized}, applied == revision && code == "", nil
}

func (m *Manager) Register(mux *http.ServeMux, service *auth.Service) {
	wrap := func(permission auth.Permission, fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store, private")
			if o := r.Header.Get("Origin"); o != "" { // Reuse the policy handler's origin and session checks.
				h := NewHandler(m.pool, service)
				if !h.user(w, r) {
					return
				}
			}
			c, err := r.Cookie(auth.SessionCookieName)
			if err != nil {
				http.Error(w, "login required", 401)
				return
			}
			u, err := service.Authenticate(r.Context(), c.Value)
			if err != nil {
				http.Error(w, "login required", 401)
				return
			}
			if !u.Can(permission) {
				http.Error(w, "permission denied", 403)
				return
			}
			if !uuidLike(r.PathValue("deviceID")) {
				http.Error(w, "invalid device", 400)
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /api/backups/devices/{deviceID}/managed", wrap(auth.PermBackupsRead, m.getManaged))
	mux.HandleFunc("PUT /api/backups/devices/{deviceID}/managed", wrap(auth.PermHostsManage, m.saveManaged))
	mux.HandleFunc("GET /api/backups/devices/{deviceID}/managed/recovery", wrap(auth.PermSecretsReveal, m.recovery))
	mux.HandleFunc("GET /api/backups/devices/{deviceID}/snapshots", wrap(auth.PermBackupsRead, m.snapshots))
	mux.HandleFunc("GET /api/backups/devices/{deviceID}/snapshots/{snapshotID}/entries", wrap(auth.PermBackupsRead, m.entries))
	mux.HandleFunc("GET /api/backups/devices/{deviceID}/snapshots/{snapshotID}/download", wrap(auth.PermBackupsRead, m.download))
}

func (m *Manager) recovery(w http.ResponseWriter, r *http.Request) {
	id, err := m.agent(r.Context(), r.PathValue("deviceID"))
	if err != nil {
		http.Error(w, "active agent not found", 404)
		return
	}
	var sealed []byte
	if err = m.pool.QueryRow(r.Context(), `SELECT config FROM managed_backup_profiles WHERE agent_id=$1`, id).Scan(&sealed); err != nil {
		http.Error(w, "managed backup not found", 404)
		return
	}
	v, err := m.Open(sealed)
	if err != nil {
		http.Error(w, "backup secret unavailable", 503)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="vigara-backup-recovery.json"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	write(w, map[string]any{"repository": v.Repository, "serverRepository": v.ServerRepository, "password": v.Password, "resticVersion": restic.PinnedVersion})
}

func (m *Manager) agent(ctx context.Context, device string) (string, error) {
	var id string
	err := m.pool.QueryRow(ctx, `SELECT id::text FROM agent_identities WHERE device_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 1`, device).Scan(&id)
	return id, err
}

func (m *Manager) getManaged(w http.ResponseWriter, r *http.Request) {
	v := ManagedView{Available: m.Available()}
	id, err := m.agent(r.Context(), r.PathValue("deviceID"))
	if err != nil {
		http.Error(w, "active agent not found", 404)
		return
	}
	v.AgentID = id
	var sealed []byte
	err = m.pool.QueryRow(r.Context(), `SELECT config,revision,applied_revision,error_code,reported_at,initialized FROM managed_backup_profiles WHERE agent_id=$1`, id).Scan(&sealed, &v.Revision, &v.AppliedRevision, &v.ErrorCode, &v.ReportedAt, &v.Initialized)
	if errors.Is(err, pgx.ErrNoRows) {
		write(w, v)
		return
	}
	if err != nil {
		http.Error(w, "could not read backup configuration", 500)
		return
	}
	c, err := m.Open(sealed)
	if err != nil {
		http.Error(w, "backup secret unavailable; check server master key", 503)
		return
	}
	c.Password = ""
	c.ExpectedRevision = v.Revision
	v.Config = &c
	v.Configured = true
	write(w, v)
}

func (m *Manager) saveManaged(w http.ResponseWriter, r *http.Request) {
	if !m.Available() {
		http.Error(w, "managed backups require VIGIRA_BACKUP_MASTER_KEY", 503)
		return
	}
	var input ManagedInput
	if !decode(w, r, &input) {
		return
	}
	if err := ValidateManaged(input); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tx, err := m.pool.Begin(r.Context())
	if err != nil {
		http.Error(w, "could not save backups", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), `SELECT id::text FROM agent_identities WHERE device_id=$1 AND revoked_at IS NULL AND 'restic.backup'=ANY(allowed_capabilities) ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, r.PathValue("deviceID")).Scan(&id)
	if err != nil {
		http.Error(w, "agent must be enrolled with backup permission", 409)
		return
	}
	var blocked bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE agent_id=$1 AND status IN ('leased','unknown'))`, id).Scan(&blocked)
	if err != nil {
		http.Error(w, "could not check active backups", 500)
		return
	}
	if blocked {
		http.Error(w, "wait for the active backup or reconcile its unknown outcome before editing", 409)
		return
	}
	var old []byte
	var revision int64
	var policyID string
	var initialized bool
	err = tx.QueryRow(r.Context(), `SELECT config,revision,policy_id::text,initialized FROM managed_backup_profiles WHERE agent_id=$1 FOR UPDATE`, id).Scan(&old, &revision, &policyID, &initialized)
	if errors.Is(err, pgx.ErrNoRows) {
		if input.ExpectedRevision != 0 {
			http.Error(w, "configuration changed; reload before saving", 409)
			return
		}
		sealed, e := m.Prepare(input)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		err = m.Install(r.Context(), tx, id, r.PathValue("deviceID"), sealed)
	} else if err == nil {
		previous, e := m.Open(old)
		if e != nil {
			http.Error(w, "backup secret unavailable", 503)
			return
		}
		if input.ExpectedRevision != revision {
			http.Error(w, "configuration changed; reload before saving", 409)
			return
		}
		if initialized && (input.Repository != previous.Repository || input.Initialize != previous.Initialize || input.Password != "") {
			http.Error(w, "repository identity and password cannot be changed; edit source paths or schedule", 400)
			return
		}
		if input.Repository == previous.Repository && input.Initialize == previous.Initialize && input.Password == "" {
			input.Password = previous.Password
		}
		sealed, e := m.Prepare(input)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		paths, _ := json.Marshal(input.SourcePaths)
		schedule, _ := json.Marshal(input.Schedule)
		next, _ := nextOccurrence(input.Schedule, time.Now().UTC())
		_, err = tx.Exec(r.Context(), `UPDATE backup_policies SET included_paths=$2,schedule=$3,enabled=$4,next_run_at=$5,destination=$6,revision=revision+1,updated_at=now() WHERE id=$1`, policyID, paths, schedule, input.Enabled, next, input.Repository)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE backup_jobs SET status='skipped' WHERE policy_id=$1 AND status IN ('pending','waiting')`, policyID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE managed_backup_profiles SET config=$2,revision=revision+1,error_code='' WHERE agent_id=$1`, id, sealed)
		}
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		http.Error(w, "could not save backups", 500)
		return
	}
	m.getManaged(w, r)
}

func (m *Manager) withRepository(w http.ResponseWriter, r *http.Request, fn func(context.Context, *restic.Runner, string)) {
	select {
	case m.operations <- struct{}{}:
		defer func() { <-m.operations }()
	default:
		http.Error(w, "backup browser busy; try again", 429)
		return
	}
	id, err := m.agent(r.Context(), r.PathValue("deviceID"))
	if err != nil {
		http.Error(w, "active agent not found", 404)
		return
	}
	var sealed []byte
	err = m.pool.QueryRow(r.Context(), `SELECT config FROM managed_backup_profiles WHERE agent_id=$1`, id).Scan(&sealed)
	if err != nil {
		http.Error(w, "managed backup not found", 404)
		return
	}
	v, err := m.Open(sealed)
	if err != nil {
		http.Error(w, "backup secret unavailable", 503)
		return
	}
	if v.ServerRepository == "" {
		http.Error(w, "configure a server-accessible repository path to browse backups", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	fn(ctx, restic.New(restic.Config{Executable: m.executable, Environment: map[string]string{"RESTIC_PASSWORD": v.Password}}), v.ServerRepository)
}

func (m *Manager) snapshots(w http.ResponseWriter, r *http.Request) {
	m.withRepository(w, r, func(ctx context.Context, runner *restic.Runner, repo string) {
		v, err := runner.ListSnapshots(ctx, repo)
		if err != nil {
			http.Error(w, "cannot read repository; check server path, access, password and restic version", 502)
			return
		}
		write(w, v)
	})
}

func (m *Manager) entries(w http.ResponseWriter, r *http.Request) {
	m.withRepository(w, r, func(ctx context.Context, runner *restic.Runner, repo string) {
		v, err := runner.BrowseDirectory(ctx, repo, r.PathValue("snapshotID"), r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, "could not browse snapshot", 502)
			return
		}
		write(w, v)
	})
}

func (m *Manager) download(w http.ResponseWriter, r *http.Request) {
	m.withRepository(w, r, func(ctx context.Context, runner *restic.Runner, repo string) {
		source := r.URL.Query().Get("path")
		if source == "" {
			source = "/"
		}
		name := "snapshot.zip"
		if source != "/" {
			entries, err := runner.BrowseDirectory(ctx, repo, r.PathValue("snapshotID"), path.Dir(source))
			if err != nil {
				http.Error(w, "could not inspect download", 502)
				return
			}
			found := false
			for _, entry := range entries {
				if entry.Path == source && (entry.Type == "file" || entry.Type == "dir") {
					found = true
					name = entry.Name
					if entry.Type == "dir" {
						name += ".zip"
					}
					break
				}
			}
			if !found {
				http.Error(w, "file or directory not found", 404)
				return
			}
		}
		// Stage to a temporary file before committing HTTP headers, so command failures
		// never become successful downloads. Restic streams to disk with a bounded size.
		f, err := os.CreateTemp("", "vigara-restore-*.zip")
		if err != nil {
			http.Error(w, "download storage unavailable", 503)
			return
		}
		defer os.Remove(f.Name())
		defer f.Close()
		err = runner.DumpSnapshot(ctx, repo, r.PathValue("snapshotID"), source, f, 2<<30)
		if err != nil {
			http.Error(w, "could not prepare download (limit 2 GiB)", 502)
			return
		}
		if _, err = f.Seek(0, 0); err != nil {
			http.Error(w, "download storage unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		http.ServeContent(w, r, "backup.download", time.Time{}, f)
	})
}
