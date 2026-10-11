package androidbackup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	capability       = "android.files.upload"
	perAgentMaxByte  = int64(10 << 30)
	perAgentMaxFiles = 5000
	serverMaxFiles   = 100000
	staleUploadAge   = time.Hour
)

type Handler struct {
	pool          *pgxpool.Pool
	auth          *auth.Service
	root          string
	maxFileBytes  int64
	maxTotalBytes int64
	uploadSlots   chan struct{}
}

func NewHandler(pool *pgxpool.Pool, service *auth.Service, root string, maxFileBytes, maxTotalBytes int64) *Handler {
	return &Handler{pool: pool, auth: service, root: root, maxFileBytes: maxFileBytes, maxTotalBytes: maxTotalBytes, uploadSlots: make(chan struct{}, 4)}
}

func (h *Handler) Register(agentMux, operatorMux *http.ServeMux) {
	agentMux.HandleFunc("PUT /api/agent/android/files", h.upload)
	operatorMux.HandleFunc("GET /api/backups/android/files", h.list)
	operatorMux.HandleFunc("GET /api/backups/android/files/{objectID}", h.download)
}

type File struct {
	ObjectID    string    `json:"objectId"`
	AgentID     string    `json:"agentId"`
	BackupID    string    `json:"backupId"`
	FileID      string    `json:"fileId"`
	DisplayName string    `json:"displayName"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
	CommittedAt time.Time `json:"committedAt"`
}

var (
	errConflict = errors.New("conflicting upload")
	errQuota    = errors.New("quota exceeded")
	errBody     = errors.New("invalid upload body")
)

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	if h.root == "" {
		http.Error(w, "Android file backup storage is disabled", http.StatusServiceUnavailable)
		return
	}
	if r.ContentLength <= 0 || r.ContentLength > h.maxFileBytes {
		http.Error(w, "file size is missing or exceeds the configured limit", http.StatusRequestEntityTooLarge)
		return
	}
	agentID, ok := h.authenticatedAndroidAgent(w, r)
	if !ok {
		return
	}
	backupID := strings.ToLower(r.Header.Get("X-Vigara-Backup-ID"))
	fileID := strings.ToLower(r.Header.Get("X-Vigara-File-ID"))
	nameBytes, nameErr := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Vigara-File-Name"))
	name := string(nameBytes)
	shaText := strings.ToLower(r.Header.Get("X-Vigara-Content-SHA256"))
	sha, shaErr := hex.DecodeString(shaText)
	if !validUUID(backupID) || !validUUID(fileID) || nameErr != nil || !validDisplayName(name) || shaErr != nil || len(sha) != sha256.Size {
		http.Error(w, "invalid upload metadata", http.StatusBadRequest)
		return
	}
	select {
	case h.uploadSlots <- struct{}{}:
		defer func() { <-h.uploadSlots }()
	default:
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Android file backup upload capacity is busy", http.StatusTooManyRequests)
		return
	}
	if err := os.MkdirAll(filepath.Join(h.root, "objects", ".staging"), 0700); err != nil {
		http.Error(w, "Android file backup storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	status, err := h.reserveAndUpload(w, r, agentID, backupID, fileID, name, sha)
	if err != nil {
		switch {
		case errors.Is(err, errConflict):
			http.Error(w, "upload conflicts with an existing file version", http.StatusConflict)
		case errors.Is(err, errQuota):
			http.Error(w, "Android file backup storage quota exceeded", http.StatusInsufficientStorage)
		case errors.Is(err, errBody):
			http.Error(w, "file body does not match declared size or checksum", http.StatusBadRequest)
		default:
			http.Error(w, "could not store Android file backup", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(status)
}

func (h *Handler) reserveAndUpload(w http.ResponseWriter, r *http.Request, agentID, backupID, fileID, name string, expectedHash []byte) (int, error) {
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(159012)`); err != nil {
		return 0, err
	}
	var objectID, status, existingName string
	var hash []byte
	var size int64
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT object_id::text,status,display_name,content_sha256,byte_size,created_at FROM android_file_backups WHERE agent_id=$1 AND backup_id=$2 AND file_id=$3 FOR UPDATE`, agentID, backupID, fileID).Scan(&objectID, &status, &existingName, &hash, &size, &created)
	if err == nil {
		if existingName != name || size != r.ContentLength || string(hash) != string(expectedHash) {
			return 0, errConflict
		}
		if status == "committed" {
			if err := tx.Commit(ctx); err != nil {
				return 0, err
			}
			return http.StatusNoContent, nil
		}
		if time.Since(created) < staleUploadAge {
			return 0, errConflict
		}
		final := h.objectPath(agentID, objectID)
		if info, statErr := os.Stat(final); statErr == nil {
			actualHash, actualSize, checkErr := digestFile(final)
			if checkErr != nil || info.Size() != r.ContentLength || actualSize != size || string(actualHash) != string(hash) {
				return 0, errConflict
			}
			if _, err = tx.Exec(ctx, `UPDATE android_file_backups SET status='committed',committed_at=now() WHERE object_id=$1`, objectID); err != nil {
				return 0, err
			}
			if err = tx.Commit(ctx); err != nil {
				return 0, err
			}
			return http.StatusNoContent, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return 0, statErr
		}
		_ = os.Remove(h.partialPath(objectID))
		if _, err = tx.Exec(ctx, `DELETE FROM android_file_backups WHERE object_id=$1 AND status='uploading'`, objectID); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var serverBytes, agentBytes, serverFiles, agentFiles int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(byte_size),0),count(*) FROM android_file_backups WHERE status IN ('uploading','committed')`).Scan(&serverBytes, &serverFiles); err != nil {
		return 0, err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(byte_size),0),count(*) FROM android_file_backups WHERE agent_id=$1 AND status IN ('uploading','committed')`, agentID).Scan(&agentBytes, &agentFiles); err != nil {
		return 0, err
	}
	if r.ContentLength > h.maxTotalBytes-serverBytes || r.ContentLength > perAgentMaxByte-agentBytes || serverFiles >= serverMaxFiles || agentFiles >= perAgentMaxFiles {
		return 0, errQuota
	}
	if err = tx.QueryRow(ctx, `INSERT INTO android_file_backups(agent_id,backup_id,file_id,display_name,content_sha256,byte_size,status) VALUES($1,$2,$3,$4,$5,$6,'uploading') RETURNING object_id::text`, agentID, backupID, fileID, name, expectedHash, r.ContentLength).Scan(&objectID); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	if err = h.writeObject(w, r, agentID, objectID, r.ContentLength, expectedHash); err != nil {
		h.releaseReservation(objectID)
		_ = os.Remove(h.partialPath(objectID))
		return 0, err
	}
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFinalize()
	_, err = h.pool.Exec(finalizeCtx, `UPDATE android_file_backups SET status='committed',committed_at=now() WHERE object_id=$1 AND status='uploading'`, objectID)
	if err != nil {
		_ = os.Remove(h.objectPath(agentID, objectID))
		h.releaseReservation(objectID)
		return 0, err
	}
	return http.StatusCreated, nil
}

func (h *Handler) releaseReservation(objectID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = h.pool.Exec(ctx, `DELETE FROM android_file_backups WHERE object_id=$1 AND status='uploading'`, objectID)
}

func (h *Handler) writeObject(w http.ResponseWriter, r *http.Request, agentID, objectID string, size int64, expectedHash []byte) error {
	partial := h.partialPath(objectID)
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(idleDeadlineReader{reader: r.Body, controller: http.NewResponseController(w)}, size+1))
	if copyErr == nil && written != size {
		copyErr = errBody
	}
	if copyErr == nil && string(hasher.Sum(nil)) != string(expectedHash) {
		copyErr = errBody
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		objectDir := filepath.Dir(h.objectPath(agentID, objectID))
		if err := os.MkdirAll(objectDir, 0700); err != nil {
			copyErr = err
		} else {
			copyErr = os.Rename(partial, h.objectPath(agentID, objectID))
		}
	}
	if copyErr != nil {
		_ = os.Remove(partial)
		if errors.Is(copyErr, errBody) {
			return errBody
		}
		return copyErr
	}
	return nil
}

type idleDeadlineReader struct {
	reader     io.Reader
	controller *http.ResponseController
}

func (r idleDeadlineReader) Read(buffer []byte) (int, error) {
	_ = r.controller.SetReadDeadline(time.Now().Add(2 * time.Minute))
	return r.reader.Read(buffer)
}

func (h *Handler) authenticatedAndroidAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	authorization := r.Header.Get("Authorization")
	credential := strings.TrimPrefix(authorization, "Bearer ")
	if credential == authorization || credential == "" || len(credential) > 256 {
		http.Error(w, "agent credential required", http.StatusUnauthorized)
		return "", false
	}
	sum := sha256.Sum256([]byte(credential))
	var id, platform string
	var capabilities, allowed []string
	err := h.pool.QueryRow(r.Context(), `SELECT id::text,platform,capabilities,allowed_capabilities FROM agent_identities WHERE credential_hash=$1 AND revoked_at IS NULL`, sum[:]).Scan(&id, &platform, &capabilities, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invalid or revoked agent credential", http.StatusUnauthorized)
		return "", false
	}
	if err != nil {
		http.Error(w, "could not authenticate agent", http.StatusInternalServerError)
		return "", false
	}
	if platform != "android" || !contains(capabilities, capability) || !contains(allowed, capability) {
		http.Error(w, "agent is not authorized for Android file backup", http.StatusForbidden)
		return "", false
	}
	return id, true
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscan(raw, &limit); err != nil || limit < 1 || limit > 500 {
			http.Error(w, "limit must be between 1 and 500", http.StatusBadRequest)
			return
		}
	}
	agentID := strings.ToLower(r.URL.Query().Get("agentId"))
	query := `SELECT object_id::text,agent_id::text,backup_id::text,file_id::text,display_name,encode(content_sha256,'hex'),byte_size,created_at,committed_at FROM android_file_backups WHERE status='committed'`
	args := []any{}
	if agentID != "" {
		if !validUUID(agentID) {
			http.Error(w, "invalid agent id", http.StatusBadRequest)
			return
		}
		query += ` AND agent_id=$1`
		args = append(args, agentID)
	}
	query += ` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit)
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "could not list Android file backups", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	files := make([]File, 0)
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ObjectID, &f.AgentID, &f.BackupID, &f.FileID, &f.DisplayName, &f.SHA256, &f.SizeBytes, &f.CreatedAt, &f.CommittedAt); err != nil {
			http.Error(w, "could not list Android file backups", http.StatusInternalServerError)
			return
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "could not list Android file backups", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	objectID := strings.ToLower(r.PathValue("objectID"))
	if !validUUID(objectID) {
		http.NotFound(w, r)
		return
	}
	var f File
	err := h.pool.QueryRow(r.Context(), `SELECT object_id::text,agent_id::text,backup_id::text,file_id::text,display_name,encode(content_sha256,'hex'),byte_size,created_at,committed_at FROM android_file_backups WHERE object_id=$1 AND status='committed'`, objectID).Scan(&f.ObjectID, &f.AgentID, &f.BackupID, &f.FileID, &f.DisplayName, &f.SHA256, &f.SizeBytes, &f.CreatedAt, &f.CommittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not read Android file backup metadata", http.StatusInternalServerError)
		return
	}
	file, err := os.Open(h.objectPath(f.AgentID, f.ObjectID))
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "Android file backup data is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "could not read Android file backup", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-SHA256", f.SHA256)
	w.Header().Set("X-Vigara-Object-ID", f.ObjectID)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.DisplayName}))
	http.ServeContent(w, r, "backup", f.CommittedAt, file)
}

func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) bool {
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

func (h *Handler) objectPath(agentID, objectID string) string {
	return filepath.Join(h.root, "objects", agentID, objectID+".blob")
}
func (h *Handler) partialPath(objectID string) string {
	return filepath.Join(h.root, "objects", ".staging", objectID+".part")
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validDisplayName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func digestFile(path string) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, file)
	return h.Sum(nil), n, err
}
