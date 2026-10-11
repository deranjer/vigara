package backups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	pool *pgxpool.Pool
	auth *auth.Service
}

func NewHandler(pool *pgxpool.Pool, service *auth.Service) *Handler { return &Handler{pool, service} }
func (h *Handler) Register(m *http.ServeMux) {
	m.HandleFunc("GET /api/backups/repositories", h.repositories)
	m.HandleFunc("GET /api/backups/policies", h.list)
	m.HandleFunc("POST /api/backups/policies", h.create)
	m.HandleFunc("GET /api/backups/policies/{id}", h.get)
	m.HandleFunc("PUT /api/backups/policies/{id}", h.update)
	m.HandleFunc("DELETE /api/backups/policies/{id}", h.retire)
	m.HandleFunc("GET /api/backups/policies/{id}/jobs", h.jobs)
	m.HandleFunc("GET /api/backups/policies/{id}/history", h.history)
	m.HandleFunc("GET /api/backups/policies/{id}/profile-template", h.template)
}

type Schedule struct {
	Weekdays []string `json:"weekdays"`
	Time     string   `json:"time"`
	Timezone string   `json:"timezone"`
}
type Retention struct {
	KeepLast    int `json:"keepLast"`
	KeepDaily   int `json:"keepDaily"`
	KeepWeekly  int `json:"keepWeekly"`
	KeepMonthly int `json:"keepMonthly"`
}
type Policy struct {
	ID                       string    `json:"id,omitempty"`
	Revision                 int32     `json:"revision,omitempty"`
	DeviceID                 string    `json:"deviceId"`
	AgentID                  string    `json:"agentId"`
	ProfileID                string    `json:"profileId"`
	Name                     string    `json:"name"`
	Destination              string    `json:"destination"`
	IncludedPaths            []string  `json:"includedPaths"`
	ExcludedPaths            []string  `json:"excludedPaths"`
	Retention                Retention `json:"retention"`
	Schedule                 Schedule  `json:"schedule"`
	MissedRunPolicy          string    `json:"missedRunPolicy"`
	DisconnectedDevicePolicy string    `json:"disconnectedDevicePolicy"`
	Enabled                  bool      `json:"enabled"`
	NextRunAt                time.Time `json:"nextRunAt"`
	ExecutionStatus          string    `json:"executionStatus"`
	CreatedAt                time.Time `json:"createdAt,omitempty"`
	UpdatedAt                time.Time `json:"updatedAt,omitempty"`
}
type PolicyInput struct {
	DeviceID                 string    `json:"deviceId"`
	AgentID                  string    `json:"agentId"`
	ProfileID                string    `json:"profileId"`
	Name                     string    `json:"name"`
	Destination              string    `json:"destination"`
	IncludedPaths            []string  `json:"includedPaths"`
	ExcludedPaths            []string  `json:"excludedPaths"`
	Retention                Retention `json:"retention"`
	Schedule                 Schedule  `json:"schedule"`
	MissedRunPolicy          string    `json:"missedRunPolicy"`
	DisconnectedDevicePolicy string    `json:"disconnectedDevicePolicy"`
	Enabled                  bool      `json:"enabled"`
}
type Job struct {
	ID              string          `json:"id"`
	Revision        int32           `json:"revision"`
	PolicySnapshot  json.RawMessage `json:"policySnapshot"`
	PolicyID        string          `json:"policyId"`
	AgentID         string          `json:"agentId"`
	ProfileID       string          `json:"profileId"`
	ScheduledFor    time.Time       `json:"scheduledFor"`
	Status          string          `json:"status"`
	AttemptCount    int             `json:"attemptCount"`
	CreatedAt       time.Time       `json:"createdAt"`
	ExecutionStatus string          `json:"executionStatus"`
	Outcome         string          `json:"outcome,omitempty"`
	SnapshotIDs     []string        `json:"snapshotIds,omitempty"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	ErrorMessage    string          `json:"errorMessage,omitempty"`
	Progress        *Progress       `json:"progress,omitempty"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
}

type Progress struct {
	ObservedAt  time.Time `json:"observedAt"`
	Phase       string    `json:"phase"`
	FilesDone   uint64    `json:"filesDone,omitempty"`
	FilesTotal  uint64    `json:"filesTotal,omitempty"`
	BytesDone   uint64    `json:"bytesDone,omitempty"`
	BytesTotal  uint64    `json:"bytesTotal,omitempty"`
	PercentDone float64   `json:"percentDone,omitempty"`
}

type HistoryEntry struct {
	ID              string     `json:"id"`
	PolicyID        string     `json:"policyId"`
	AgentID         string     `json:"agentId"`
	ProfileID       string     `json:"profileId"`
	Revision        int32      `json:"revision"`
	ScheduledFor    time.Time  `json:"scheduledFor"`
	Status          string     `json:"status"`
	AttemptCount    int        `json:"attemptCount"`
	CreatedAt       time.Time  `json:"createdAt"`
	ExecutionStatus string     `json:"executionStatus"`
	Outcome         string     `json:"outcome,omitempty"`
	SnapshotIDs     []string   `json:"snapshotIds,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	Progress        *Progress  `json:"progress,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

func (h *Handler) user(w http.ResponseWriter, r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, e := url.Parse(o)
		if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "request origin rejected", 403)
			return false
		}
	}
	c, e := r.Cookie(auth.SessionCookieName)
	if e != nil {
		http.Error(w, "login required", 401)
		return false
	}
	if _, e = h.auth.Authenticate(r.Context(), c.Value); e != nil {
		http.Error(w, "login required", 401)
		return false
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", 400)
		return false
	}
	return true
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func validDestination(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	if strings.HasPrefix(s, "local:") {
		return len(s) > 6
	}
	return strings.HasPrefix(s, "\\\\") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/") || (len(s) > 2 && ((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) && s[1] == ':' && (s[2] == '\\' || s[2] == '/'))
}
func validPath(s string) bool {
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\\\") {
		return true
	}
	return len(s) > 2 && ((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z')) && s[1] == ':' && (s[2] == '\\' || s[2] == '/')
}
func validate(p Policy) error {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 120 {
		return errors.New("name must be 1..120 characters")
	}
	if p.ProfileID == "" || len(p.ProfileID) > 64 {
		return errors.New("profileId must be 1..64 characters")
	}
	for _, c := range p.ProfileID {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return errors.New("profileId may contain letters, numbers, underscore and hyphen")
		}
	}
	if !validDestination(p.Destination) {
		return errors.New("destination must be a local or network path; remote backend URLs are unsupported")
	}
	if len(p.IncludedPaths) == 0 || len(p.IncludedPaths) > 128 || len(p.ExcludedPaths) > 128 {
		return errors.New("includedPaths must contain 1..128 paths; excludedPaths allows at most 128")
	}
	for _, a := range [][]string{p.IncludedPaths, p.ExcludedPaths} {
		for _, x := range a {
			if !validPath(x) || len(x) > 2048 {
				return errors.New("paths must be 1..2048 characters")
			}
		}
	}
	for _, n := range []int{p.Retention.KeepLast, p.Retention.KeepDaily, p.Retention.KeepWeekly, p.Retention.KeepMonthly} {
		if n < 0 || n > 10000 {
			return errors.New("retention counts must be 0..10000")
		}
	}
	if p.MissedRunPolicy != "skip" && p.MissedRunPolicy != "run_once" {
		return errors.New("missedRunPolicy must be skip or run_once")
	}
	if p.DisconnectedDevicePolicy != "wait" && p.DisconnectedDevicePolicy != "skip" {
		return errors.New("disconnectedDevicePolicy must be wait or skip")
	}
	return validateSchedule(p.Schedule)
}
func validateSchedule(s Schedule) error {
	if len(s.Weekdays) == 0 || len(s.Weekdays) > 7 {
		return errors.New("weekdays must contain 1..7 days")
	}
	valid := map[string]bool{"Sunday": true, "Monday": true, "Tuesday": true, "Wednesday": true, "Thursday": true, "Friday": true, "Saturday": true}
	seen := map[string]bool{}
	for _, d := range s.Weekdays {
		if !valid[d] || seen[d] {
			return errors.New("weekdays must be unique day names")
		}
		seen[d] = true
	}
	if _, e := time.Parse("15:04", s.Time); e != nil || len(s.Time) != 5 {
		return errors.New("time must use HH:MM")
	}
	if s.Timezone == "" || s.Timezone == "Local" || len(s.Timezone) > 128 {
		return errors.New("timezone must be an IANA timezone")
	}
	if _, e := time.LoadLocation(s.Timezone); e != nil {
		return errors.New("timezone must be valid")
	}
	return nil
}
func nextOccurrence(s Schedule, after time.Time) (time.Time, error) {
	loc, e := time.LoadLocation(s.Timezone)
	if e != nil {
		return time.Time{}, e
	}
	t, _ := time.Parse("15:04", s.Time)
	days := map[string]bool{}
	for _, d := range s.Weekdays {
		days[d] = true
	}
	l := after.In(loc)
	for i := 0; i <= 14; i++ {
		d := l.AddDate(0, 0, i)
		if !days[d.Weekday().String()] {
			continue
		}
		x := time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if x.Year() != d.Year() || x.Month() != d.Month() || x.Day() != d.Day() || x.Hour() != t.Hour() || x.Minute() != t.Minute() || !x.After(after) {
			continue
		}
		return x.UTC(), nil
	}
	return time.Time{}, errors.New("could not calculate next schedule")
}

const sel = `SELECT id::text,device_id::text,agent_id::text,profile_id,name,destination,included_paths,excluded_paths,retention,schedule,missed_run_policy,disconnected_device_policy,enabled,next_run_at,created_at,updated_at,revision FROM backup_policies`

func executionStatus(p Policy) string {
	if len(p.ExcludedPaths) > 0 {
		return "blocked_unsupported_exclusions"
	}
	return "awaiting_local_profile"
}

func scan(row pgx.Row) (p Policy, e error) {
	var a, b, c, d []byte
	e = row.Scan(&p.ID, &p.DeviceID, &p.AgentID, &p.ProfileID, &p.Name, &p.Destination, &a, &b, &c, &d, &p.MissedRunPolicy, &p.DisconnectedDevicePolicy, &p.Enabled, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt, &p.Revision)
	if e == nil {
		_ = json.Unmarshal(a, &p.IncludedPaths)
		_ = json.Unmarshal(b, &p.ExcludedPaths)
		_ = json.Unmarshal(c, &p.Retention)
		_ = json.Unmarshal(d, &p.Schedule)
		if len(p.ExcludedPaths) > 0 {
			p.ExecutionStatus = "blocked_unsupported_exclusions"
		} else {
			p.ExecutionStatus = "awaiting_local_profile"
		}
	}
	return
}
func page(r *http.Request) (limit, offset int, ok bool) {
	limit, offset = 100, 0
	if q := r.URL.Query().Get("limit"); q != "" {
		n, e := strconv.Atoi(q)
		if e != nil || n < 1 || n > 100 {
			return 0, 0, false
		}
		limit = n
	}
	if q := r.URL.Query().Get("offset"); q != "" {
		n, e := strconv.Atoi(q)
		if e != nil || n < 0 {
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}
func pageHeaders(w http.ResponseWriter, limit, offset, count int) {
	more := count > limit
	w.Header().Set("X-Has-More", strconv.FormatBool(more))
	if more {
		w.Header().Set("X-Next-Offset", strconv.Itoa(offset+limit))
	}
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.user(w, r) {
		return
	}
	limit, offset, ok := page(r)
	if !ok {
		http.Error(w, "limit must be 1..100 and offset non-negative", 400)
		return
	}
	rows, e := h.pool.Query(r.Context(), sel+` WHERE retired_at IS NULL ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, limit+1, offset)
	if e != nil {
		http.Error(w, "could not read policies", 500)
		return
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		p, e := scan(rows)
		if e != nil {
			http.Error(w, "could not read policies", 500)
			return
		}
		out = append(out, p)
	}
	if rows.Err() != nil {
		http.Error(w, "could not read policies", 500)
		return
	}
	pageHeaders(w, limit, offset, len(out))
	if len(out) > limit {
		out = out[:limit]
	}
	write(w, out)
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !h.user(w, r) {
		return
	}
	if !uuidLike(r.PathValue("id")) {
		http.Error(w, "policy ID must be a UUID", 400)
		return
	}
	p, e := scan(h.pool.QueryRow(r.Context(), sel+` WHERE id=$1 AND retired_at IS NULL`, r.PathValue("id")))
	if errors.Is(e, pgx.ErrNoRows) {
		http.Error(w, "policy not found", 404)
	} else if e != nil {
		http.Error(w, "could not read policy", 500)
	} else {
		write(w, p)
	}
}
func (h *Handler) save(w http.ResponseWriter, r *http.Request, create bool) {
	if !h.user(w, r) {
		return
	}
	if !create && !uuidLike(r.PathValue("id")) {
		http.Error(w, "policy ID must be a UUID", 400)
		return
	}
	var input PolicyInput
	if !decode(w, r, &input) {
		return
	}
	p := Policy{DeviceID: input.DeviceID, AgentID: input.AgentID, ProfileID: input.ProfileID, Name: input.Name, Destination: input.Destination, IncludedPaths: input.IncludedPaths, ExcludedPaths: input.ExcludedPaths, Retention: input.Retention, Schedule: input.Schedule, MissedRunPolicy: input.MissedRunPolicy, DisconnectedDevicePolicy: input.DisconnectedDevicePolicy, Enabled: input.Enabled}
	if !uuidLike(p.DeviceID) || !uuidLike(p.AgentID) {
		http.Error(w, "deviceId and agentId must be UUIDs", 400)
		return
	}
	if p.ExcludedPaths == nil {
		p.ExcludedPaths = []string{}
	}
	if p.IncludedPaths == nil {
		p.IncludedPaths = []string{}
	}
	if e := validate(p); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	next, e := nextOccurrence(p.Schedule, time.Now().UTC())
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	inc, _ := json.Marshal(p.IncludedPaths)
	exc, _ := json.Marshal(p.ExcludedPaths)
	ret, _ := json.Marshal(p.Retention)
	sch, _ := json.Marshal(p.Schedule)
	query := `INSERT INTO backup_policies(device_id,agent_id,profile_id,name,destination,included_paths,excluded_paths,retention,schedule,missed_run_policy,disconnected_device_policy,enabled,next_run_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13 WHERE EXISTS(SELECT 1 FROM agent_identities i WHERE i.id=$2 AND i.device_id=$1 AND i.revoked_at IS NULL AND i.capabilities ? 'restic.backup' AND 'restic.backup'=ANY(i.allowed_capabilities))`
	args := []any{p.DeviceID, p.AgentID, p.ProfileID, p.Name, p.Destination, inc, exc, ret, sch, p.MissedRunPolicy, p.DisconnectedDevicePolicy, p.Enabled, next}
	if !create {
		query = `UPDATE backup_policies p SET device_id=$2,agent_id=$3,profile_id=$4,name=$5,destination=$6,included_paths=$7,excluded_paths=$8,retention=$9,schedule=$10,missed_run_policy=$11,disconnected_device_policy=$12,enabled=$13,next_run_at=$14,revision=revision+1,updated_at=now() WHERE p.id=$1 AND p.retired_at IS NULL AND EXISTS(SELECT 1 FROM agent_identities i WHERE i.id=$3 AND i.device_id=$2 AND i.revoked_at IS NULL AND i.capabilities ? 'restic.backup' AND 'restic.backup'=ANY(i.allowed_capabilities))`
		args = append([]any{r.PathValue("id")}, args...)
	}
	tag, e := h.pool.Exec(r.Context(), query, args...)
	if e != nil {
		http.Error(w, "policy conflict or invalid scope", 409)
		return
	}
	if tag.RowsAffected() == 0 {
		if create {
			http.Error(w, "agent must be active, belong to device, and be authorized for restic.backup", 403)
		} else {
			http.Error(w, "policy not found or agent scope invalid", 404)
		}
		return
	}
	p, e = scan(h.pool.QueryRow(r.Context(), sel+` WHERE agent_id=$1 AND profile_id=$2 AND retired_at IS NULL`, p.AgentID, p.ProfileID))
	if !create {
		p, e = scan(h.pool.QueryRow(r.Context(), sel+` WHERE id=$1`, r.PathValue("id")))
	}
	if e != nil {
		http.Error(w, "could not read saved policy", 500)
		return
	}
	if create {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
	}
	write(w, p)
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) { h.save(w, r, true) }
func (h *Handler) update(w http.ResponseWriter, r *http.Request) { h.save(w, r, false) }
func (h *Handler) retire(w http.ResponseWriter, r *http.Request) {
	if !h.user(w, r) {
		return
	}
	if !uuidLike(r.PathValue("id")) {
		http.Error(w, "policy ID must be a UUID", 400)
		return
	}
	tag, e := h.pool.Exec(r.Context(), `UPDATE backup_policies SET enabled=false,retired_at=now(),updated_at=now() WHERE id=$1 AND retired_at IS NULL`, r.PathValue("id"))
	if e != nil {
		http.Error(w, "could not retire policy", 500)
	} else if tag.RowsAffected() == 0 {
		http.Error(w, "policy not found", 404)
	} else {
		w.WriteHeader(204)
	}
}
func (h *Handler) jobs(w http.ResponseWriter, r *http.Request) {
	h.listJobs(w, r, false)
}
func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	h.listJobs(w, r, true)
}
func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request, historyOnly bool) {
	if !h.user(w, r) {
		return
	}
	if !uuidLike(r.PathValue("id")) {
		http.Error(w, "policy ID must be a UUID", 400)
		return
	}
	limit, offset, ok := page(r)
	if !ok {
		http.Error(w, "limit must be 1..100 and offset non-negative", 400)
		return
	}
	rows, e := h.pool.Query(r.Context(), `SELECT j.id::text,j.policy_id::text,j.agent_id::text,j.profile_id,j.policy_revision,j.policy_snapshot,j.scheduled_for,j.status,j.attempt_count,j.created_at,a.result_payload,a.progress_payload,a.progress_at,a.leased_at,a.lease_expires_at FROM backup_jobs j LEFT JOIN LATERAL (SELECT result_payload,progress_payload,progress_at,leased_at,lease_expires_at FROM backup_job_attempts WHERE job_id=j.id ORDER BY attempt_number DESC LIMIT 1) a ON true WHERE j.policy_id=$1 ORDER BY j.scheduled_for DESC,j.id DESC LIMIT $2 OFFSET $3`, r.PathValue("id"), limit+1, offset)
	if e != nil {
		http.Error(w, "could not read jobs", 500)
		return
	}
	defer rows.Close()
	jobs := []Job{}
	entries := []HistoryEntry{}
	for rows.Next() {
		var j Job
		var resultPayload, progressPayload []byte
		var progressAt, leasedAt, leaseExpiresAt *time.Time
		if rows.Scan(&j.ID, &j.PolicyID, &j.AgentID, &j.ProfileID, &j.Revision, &j.PolicySnapshot, &j.ScheduledFor, &j.Status, &j.AttemptCount, &j.CreatedAt, &resultPayload, &progressPayload, &progressAt, &leasedAt, &leaseExpiresAt) != nil {
			http.Error(w, "could not read jobs", 500)
			return
		}
		j.ExecutionStatus = historyStatus(j.Status, progressAt, leaseExpiresAt, time.Now().UTC())
		if leasedAt != nil {
			j.StartedAt = leasedAt
		}
		if len(progressPayload) > 0 {
			var raw protocol.BackupProgress
			if json.Unmarshal(progressPayload, &raw) == nil {
				j.Progress = &Progress{ObservedAt: raw.ObservedAt, Phase: raw.Phase, FilesDone: raw.FilesDone, FilesTotal: raw.FilesTotal, BytesDone: raw.BytesDone, BytesTotal: raw.BytesTotal, PercentDone: raw.PercentDone}
			}
		}
		if len(resultPayload) > 0 {
			populateJobResult(&j, resultPayload)
		}
		jobs = append(jobs, j)
		entries = append(entries, HistoryEntry{ID: j.ID, PolicyID: j.PolicyID, AgentID: j.AgentID, ProfileID: j.ProfileID, Revision: j.Revision, ScheduledFor: j.ScheduledFor, Status: j.Status, AttemptCount: j.AttemptCount, CreatedAt: j.CreatedAt, ExecutionStatus: j.ExecutionStatus, Outcome: j.Outcome, SnapshotIDs: j.SnapshotIDs, ErrorCode: j.ErrorCode, ErrorMessage: j.ErrorMessage, Progress: j.Progress, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt})
	}
	if rows.Err() != nil {
		http.Error(w, "could not read jobs", 500)
		return
	}
	count := len(jobs)
	pageHeaders(w, limit, offset, count)
	if count > limit {
		jobs = jobs[:limit]
		entries = entries[:limit]
	}
	if historyOnly {
		write(w, entries)
	} else {
		write(w, jobs)
	}
}

func historyStatus(jobStatus string, progressAt, leaseExpiresAt *time.Time, now time.Time) string {
	switch jobStatus {
	case "unknown":
		return "unknown"
	case "completed":
		return "completed"
	case "skipped":
		return "skipped"
	case "pending", "waiting":
		return jobStatus
	case "leased":
		if leaseExpiresAt != nil && leaseExpiresAt.After(now) && progressAt != nil && now.Sub(*progressAt) >= 0 && now.Sub(*progressAt) <= 60*time.Second {
			return "running"
		}
		return "stale"
	default:
		return "unknown"
	}
}

func populateJobResult(j *Job, payload []byte) {
	var result struct {
		Status string `json:"status"`
		Output struct {
			ResticStatus  string    `json:"resticStatus"`
			BackupOutcome string    `json:"backupOutcome"`
			SnapshotIDs   []string  `json:"snapshotIds"`
			LastProgress  *Progress `json:"lastProgress"`
		} `json:"output"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		FinishedAt time.Time `json:"finishedAt"`
	}
	if json.Unmarshal(payload, &result) != nil {
		return
	}
	j.Outcome = result.Output.BackupOutcome
	if j.Outcome == "" {
		j.Outcome = result.Output.ResticStatus
	}
	j.SnapshotIDs = result.Output.SnapshotIDs
	if result.Output.LastProgress != nil && (j.Progress == nil || result.Output.LastProgress.ObservedAt.After(j.Progress.ObservedAt)) {
		j.Progress = result.Output.LastProgress
	}
	if result.Error != nil {
		j.ErrorCode = result.Error.Code
		j.ErrorMessage = result.Error.Message
	}
	if !result.FinishedAt.IsZero() {
		j.FinishedAt = &result.FinishedAt
	}
	if j.Status == "completed" {
		if j.Outcome != "" {
			j.ExecutionStatus = j.Outcome
		} else {
			switch result.Status {
			case "succeeded":
				j.ExecutionStatus = "succeeded"
			case "timed_out":
				j.ExecutionStatus = "timed_out"
			case "cancelled":
				j.ExecutionStatus = "canceled"
			default:
				j.ExecutionStatus = "failed"
			}
		}
	}
}
func (h *Handler) repositories(w http.ResponseWriter, r *http.Request) {
	if !h.user(w, r) {
		return
	}
	limit, offset, ok := page(r)
	if !ok {
		http.Error(w, "limit must be 1..100 and offset non-negative", 400)
		return
	}
	rows, e := h.pool.Query(r.Context(), `SELECT p.agent_id::text,p.device_id::text,p.profile_id,p.destination,p.name,d.display_name FROM backup_policies p JOIN devices d ON d.id=p.device_id WHERE p.retired_at IS NULL ORDER BY d.display_name,p.profile_id,p.agent_id LIMIT $1 OFFSET $2`, limit+1, offset)
	if e != nil {
		http.Error(w, "could not read repositories", 500)
		return
	}
	defer rows.Close()
	type R struct {
		AgentID     string `json:"agentId"`
		DeviceID    string `json:"deviceId"`
		ProfileID   string `json:"profileId"`
		Destination string `json:"destination"`
		Name        string `json:"name"`
		DeviceName  string `json:"deviceName"`
	}
	out := []R{}
	for rows.Next() {
		var x R
		if rows.Scan(&x.AgentID, &x.DeviceID, &x.ProfileID, &x.Destination, &x.Name, &x.DeviceName) != nil {
			http.Error(w, "could not read repositories", 500)
			return
		}
		out = append(out, x)
	}
	pageHeaders(w, limit, offset, len(out))
	if len(out) > limit {
		out = out[:limit]
	}
	write(w, out)
}
func (h *Handler) template(w http.ResponseWriter, r *http.Request) {
	if !h.user(w, r) {
		return
	}
	if !uuidLike(r.PathValue("id")) {
		http.Error(w, "policy ID must be a UUID", 400)
		return
	}
	p, e := scan(h.pool.QueryRow(r.Context(), sel+` WHERE id=$1 AND retired_at IS NULL`, r.PathValue("id")))
	if e != nil {
		http.Error(w, "policy not found", 404)
		return
	}
	if len(p.ExcludedPaths) > 0 {
		http.Error(w, "excludedPaths cannot be represented by the current local runner", 409)
		return
	}
	write(w, map[string]any{"operatorAction": "Apply locally and set RESTIC_PASSWORD only in the private agent state", "profileId": p.ProfileID, "executionStatus": p.ExecutionStatus, "jobDeliveryAvailable": false, "retentionApplied": false, "profile": map[string]any{"enabled": true, "repository": p.Destination, "sourcePaths": p.IncludedPaths}})
}

func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ScheduleOnce persists no more than 100 due occurrences per call. ENG-149 owns job delivery and execution leases.
func ScheduleOnce(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT p.id::text,p.agent_id::text,p.profile_id,p.schedule,p.missed_run_policy,p.disconnected_device_policy,p.revision,to_jsonb(p),p.next_run_at,d.last_seen_at FROM backup_policies p JOIN agent_identities i ON i.id=p.agent_id AND i.revoked_at IS NULL AND i.capabilities ? 'restic.backup' AND 'restic.backup'=ANY(i.allowed_capabilities) JOIN devices d ON d.id=p.device_id WHERE p.enabled AND p.retired_at IS NULL AND p.excluded_paths='[]'::jsonb AND p.next_run_at<=$1 ORDER BY p.next_run_at LIMIT 100 FOR UPDATE OF p SKIP LOCKED`, now)
	if e != nil {
		return 0, e
	}
	type D struct {
		id, agent, profile, miss, disc string
		raw, snapshot                  []byte
		revision                       int32
		scheduled, last                pgtype.Timestamptz
	}
	_ = pgtype.Timestamptz{}
	items := []D{}
	for rows.Next() {
		var x D
		if e = rows.Scan(&x.id, &x.agent, &x.profile, &x.raw, &x.miss, &x.disc, &x.revision, &x.snapshot, &x.scheduled, &x.last); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, x)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return 0, e
	}
	rows.Close()
	count := 0
	for _, x := range items {
		var s Schedule
		if json.Unmarshal(x.raw, &s) != nil {
			continue
		}
		scheduled := x.scheduled.Time
		nextAfter := scheduled
		if now.Sub(scheduled) > time.Minute {
			nextAfter = now
		}
		next, e := nextOccurrence(s, nextAfter)
		if e != nil {
			return count, e
		}
		online := x.last.Valid && now.Sub(x.last.Time) <= 5*time.Minute
		status := "pending"
		if !online && x.disc == "wait" {
			status = "waiting"
		} else if !online || x.miss == "skip" && now.Sub(scheduled) > time.Minute {
			status = "skipped"
		}
		_, e = tx.Exec(ctx, `INSERT INTO backup_jobs(policy_id,agent_id,profile_id,policy_revision,policy_snapshot,scheduled_for,status) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(policy_id,scheduled_for) DO NOTHING`, x.id, x.agent, x.profile, x.revision, x.snapshot, scheduled, status)
		if e != nil {
			return count, e
		}
		if status == "pending" {
			count++
		}
		if _, e = tx.Exec(ctx, `UPDATE backup_policies SET next_run_at=$2 WHERE id=$1`, x.id, next); e != nil {
			return count, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return count, nil
}
func RunScheduler(ctx context.Context, pool *pgxpool.Pool, logger interface{ Error(string, ...any) }) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if _, e := ScheduleOnce(ctx, pool, now.UTC()); e != nil {
				logger.Error("backup scheduler failed", "error", e)
			}
		}
	}
}
