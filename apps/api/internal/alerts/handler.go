package alerts

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	pool *pgxpool.Pool
	auth *auth.Service
}

func NewHandler(pool *pgxpool.Pool, service *auth.Service) *Handler {
	return &Handler{pool: pool, auth: service}
}
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/alerts/rules", h.listRules)
	mux.HandleFunc("POST /api/alerts/rules", h.createRule)
	mux.HandleFunc("POST /api/alerts/rules/{id}/disable", h.disableRule)
	mux.HandleFunc("GET /api/alerts/incidents", h.listIncidents)
	mux.HandleFunc("GET /api/alerts/events", h.listEvents)
	mux.HandleFunc("POST /api/agent/backups/status", h.backupStatus)
}
func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
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
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		http.Error(w, "invalid JSON request", 400)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		http.Error(w, "request must contain one JSON value", 400)
		return false
	}
	return true
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Enabled   bool      `json:"enabled"`
	Config    Config    `json:"config"`
	CreatedAt time.Time `json:"createdAt"`
}
type Config struct {
	DeviceID          string            `json:"deviceId,omitempty"`
	StaleAfterSeconds int               `json:"staleAfterSeconds,omitempty"`
	Metric            string            `json:"metric,omitempty"`
	Operator          string            `json:"operator,omitempty"`
	Threshold         float64           `json:"threshold,omitempty"`
	SustainSeconds    int               `json:"sustainSeconds,omitempty"`
	GraceSeconds      int               `json:"graceSeconds,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	PolicyKey         string            `json:"policyKey,omitempty"`
	IntervalSeconds   int               `json:"intervalSeconds,omitempty"`
}
type ruleRequest struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Config Config `json:"config"`
}

func validateRule(q ruleRequest) error {
	if strings.TrimSpace(q.Name) == "" || len(q.Name) > 120 {
		return errors.New("name must be 1..120 characters")
	}
	c := q.Config
	if c.DeviceID != "" && !validUUID(c.DeviceID) {
		return errors.New("deviceId must be a UUID")
	}
	if c.GraceSeconds < 0 || c.GraceSeconds > 86400 {
		return errors.New("graceSeconds must be between 0 and 86400")
	}
	switch q.Kind {
	case "device_unavailable":
		if c.StaleAfterSeconds < 30 || c.StaleAfterSeconds > 604800 {
			return errors.New("staleAfterSeconds must be 30..604800")
		}
	case "threshold":
		allowed := map[string]bool{"cpu_used_percent": true, "memory_used_percent": true, "filesystem_free_bytes": true, "filesystem_total_bytes": true, "memory_available_bytes": true}
		if !allowed[c.Metric] || (c.Operator != "gt" && c.Operator != "gte" && c.Operator != "lt" && c.Operator != "lte") || c.Threshold < 0 || math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || c.SustainSeconds < 0 || c.SustainSeconds > 86400 || c.SustainSeconds+c.GraceSeconds > 86400 || len(c.Labels) > 8 {
			return errors.New("threshold rule requires a supported metric, operator, non-negative finite threshold, sustainSeconds 0..86400, and at most 8 labels")
		}
		for k, v := range c.Labels {
			if k == "" || len(k) > 64 || len(v) > 256 {
				return errors.New("invalid threshold label")
			}
		}
	case "backup_failure":
		if c.DeviceID == "" || strings.TrimSpace(c.PolicyKey) == "" || len(c.PolicyKey) > 128 {
			return errors.New("backup_failure requires deviceId and policyKey")
		}
	case "backup_overdue":
		if c.DeviceID == "" || strings.TrimSpace(c.PolicyKey) == "" || len(c.PolicyKey) > 128 || c.IntervalSeconds < 60 || c.IntervalSeconds > 31536000 {
			return errors.New("backup_overdue requires deviceId, policyKey, and intervalSeconds 60..31536000")
		}
	default:
		return errors.New("kind must be device_unavailable, threshold, backup_failure, or backup_overdue")
	}
	return nil
}
func validUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	rows, e := h.pool.Query(r.Context(), `SELECT id::text,name,kind,enabled,config,created_at FROM alert_rules ORDER BY created_at DESC`)
	if e != nil {
		http.Error(w, "could not read alert rules", 500)
		return
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var a Rule
		var b []byte
		if e = rows.Scan(&a.ID, &a.Name, &a.Kind, &a.Enabled, &b, &a.CreatedAt); e != nil {
			http.Error(w, "could not read alert rules", 500)
			return
		}
		_ = json.Unmarshal(b, &a.Config)
		out = append(out, a)
	}
	if e = rows.Err(); e != nil {
		http.Error(w, "could not read alert rules", 500)
		return
	}
	write(w, out)
}
func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	var q ruleRequest
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if e := validateRule(q); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	b, _ := json.Marshal(q.Config)
	ctx := r.Context()
	tx, e := h.pool.Begin(ctx)
	if e != nil {
		http.Error(w, "could not create alert rule", 500)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(143001)`); e != nil {
		http.Error(w, "could not create alert rule", 500)
		return
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM alert_rules`).Scan(&count); e != nil {
		http.Error(w, "could not create alert rule", 500)
		return
	}
	if count >= 1000 {
		http.Error(w, "at most 1000 alert rules are supported", 409)
		return
	}
	var a Rule
	e = tx.QueryRow(ctx, `INSERT INTO alert_rules(name,kind,config) VALUES($1,$2,$3) RETURNING id::text,name,kind,enabled,config,created_at`, q.Name, q.Kind, b).Scan(&a.ID, &a.Name, &a.Kind, &a.Enabled, &b, &a.CreatedAt)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		http.Error(w, "could not create alert rule", 500)
		return
	}
	_ = json.Unmarshal(b, &a.Config)
	write(w, a)
}
func (h *Handler) disableRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	ctx := r.Context()
	tx, e := h.pool.Begin(ctx)
	if e != nil {
		http.Error(w, "could not disable alert rule", 500)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(143001)`); e != nil {
		http.Error(w, "could not disable alert rule", 500)
		return
	}
	tag, e := tx.Exec(ctx, `UPDATE alert_rules SET enabled=false,updated_at=now() WHERE id=$1`, r.PathValue("id"))
	if e != nil {
		http.Error(w, "could not disable alert rule", 400)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "alert rule not found", 404)
		return
	}
	rows, e := tx.Query(ctx, `SELECT id::text,dimension FROM alert_incidents WHERE rule_id=$1 AND status='firing' FOR UPDATE`, r.PathValue("id"))
	if e != nil {
		http.Error(w, "could not resolve disabled alerts", 500)
		return
	}
	type item struct{ id, dimension string }
	items := []item{}
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.dimension); e != nil {
			rows.Close()
			http.Error(w, "could not resolve disabled alerts", 500)
			return
		}
		items = append(items, x)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		http.Error(w, "could not resolve disabled alerts", 500)
		return
	}
	rows.Close()
	for _, x := range items {
		payload, _ := json.Marshal(map[string]any{"ruleId": r.PathValue("id"), "dimension": x.dimension, "summary": "rule disabled"})
		if _, e = tx.Exec(ctx, `UPDATE alert_incidents SET status='resolved',resolved_at=now(),updated_at=now() WHERE id=$1`, x.id); e == nil {
			_, e = tx.Exec(ctx, `INSERT INTO alert_events(incident_id,transition,payload) VALUES($1,'resolved',$2)`, x.id, payload)
		}
		if e != nil {
			http.Error(w, "could not resolve disabled alerts", 500)
			return
		}
	}
	if e = tx.Commit(ctx); e != nil {
		http.Error(w, "could not disable alert rule", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type Incident struct {
	ID             string     `json:"id"`
	RuleID         string     `json:"ruleId"`
	Dimension      string     `json:"dimension"`
	Status         string     `json:"status"`
	Summary        string     `json:"summary"`
	StartedAt      time.Time  `json:"startedAt"`
	LastObservedAt *time.Time `json:"lastObservedAt,omitempty"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
}

func (h *Handler) listIncidents(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	rows, e := h.pool.Query(r.Context(), `SELECT id::text,rule_id::text,dimension,status,summary,started_at,last_observed_at,resolved_at FROM alert_incidents ORDER BY updated_at DESC LIMIT 500`)
	if e != nil {
		http.Error(w, "could not read alert incidents", 500)
		return
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var a Incident
		if e = rows.Scan(&a.ID, &a.RuleID, &a.Dimension, &a.Status, &a.Summary, &a.StartedAt, &a.LastObservedAt, &a.ResolvedAt); e != nil {
			http.Error(w, "could not read alert incidents", 500)
			return
		}
		out = append(out, a)
	}
	if e = rows.Err(); e != nil {
		http.Error(w, "could not read alert incidents", 500)
		return
	}
	write(w, out)
}

type Event struct {
	ID         string          `json:"id"`
	IncidentID string          `json:"incidentId"`
	Transition string          `json:"transition"`
	OccurredAt time.Time       `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	rows, e := h.pool.Query(r.Context(), `SELECT id::text,incident_id::text,transition,occurred_at,payload FROM alert_events WHERE delivered_at IS NULL ORDER BY occurred_at LIMIT 200`)
	if e != nil {
		http.Error(w, "could not read alert events", 500)
		return
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var a Event
		if e = rows.Scan(&a.ID, &a.IncidentID, &a.Transition, &a.OccurredAt, &a.Payload); e != nil {
			http.Error(w, "could not read alert events", 500)
			return
		}
		out = append(out, a)
	}
	write(w, out)
}

type BackupSignal struct {
	DeviceID   string    `json:"deviceId"`
	PolicyKey  string    `json:"policyKey"`
	Status     string    `json:"status"`
	ObservedAt time.Time `json:"observedAt"`
	Message    string    `json:"message,omitempty"`
	SourceID   string    `json:"sourceId"`
}

func (h *Handler) backupStatus(w http.ResponseWriter, r *http.Request) {
	var q BackupSignal
	if !decode(w, r, &q) {
		return
	}
	agentID, ok := h.authAgent(r)
	if !ok {
		http.Error(w, "agent credential required", 401)
		return
	}
	if q.DeviceID == "" || q.PolicyKey == "" || len(q.PolicyKey) > 128 || (q.Status != "success" && q.Status != "failure" && q.Status != "running") || q.ObservedAt.IsZero() || q.ObservedAt.After(time.Now().Add(5*time.Minute)) || q.SourceID == "" || len(q.SourceID) > 128 || len(q.Message) > 512 {
		http.Error(w, "invalid backup status", 400)
		return
	}
	tx, e := h.pool.Begin(r.Context())
	if e != nil {
		http.Error(w, "could not store backup status", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var owner string
	e = tx.QueryRow(r.Context(), `SELECT device_id::text FROM agent_identities WHERE id=$1 AND revoked_at IS NULL FOR SHARE`, agentID).Scan(&owner)
	if e != nil || owner != q.DeviceID {
		http.Error(w, "backup signal device mismatch", 403)
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO backup_status(device_id,policy_key,status,observed_at,message,source_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(device_id,source_id) DO NOTHING`, q.DeviceID, q.PolicyKey, q.Status, q.ObservedAt, q.Message, q.SourceID)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		http.Error(w, "could not store backup status", 500)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func (h *Handler) authAgent(r *http.Request) (string, bool) {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(v, "Bearer ") || len(v) > 256 {
		return "", false
	}
	var id string
	sum := sha256.Sum256([]byte(strings.TrimPrefix(v, "Bearer ")))
	e := h.pool.QueryRow(r.Context(), `SELECT id::text FROM agent_identities WHERE credential_hash=$1 AND revoked_at IS NULL`, sum[:]).Scan(&id)
	return id, e == nil
}

// Run starts the durable lifecycle evaluator. A PostgreSQL advisory lock makes
// multiple server replicas safe; incident transitions and outbox rows commit atomically.
func Run(ctx context.Context, pool *pgxpool.Pool, logger interface{ Error(string, ...any) }) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := Evaluate(ctx, pool); err != nil && ctx.Err() == nil {
			logger.Error("alert evaluation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func Evaluate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var locked bool
	if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(143001)`).Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return nil
	}
	rows, e := tx.Query(ctx, `SELECT id::text,name,kind,config FROM alert_rules WHERE enabled=true ORDER BY id LIMIT 1001`)
	if e != nil {
		return e
	}
	rules := []Rule{}
	for rows.Next() {
		var a Rule
		var b []byte
		if e = rows.Scan(&a.ID, &a.Name, &a.Kind, &b); e != nil {
			rows.Close()
			return e
		}
		_ = json.Unmarshal(b, &a.Config)
		rules = append(rules, a)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	if len(rules) > 1000 {
		return errors.New("more than 1000 alert rules; reduce the configured rule set")
	}
	now := time.Now().UTC()
	var ruleErrors []error
	for _, a := range rules {
		ruleTx, beginErr := tx.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		if e = evaluateRule(ctx, ruleTx, a, now); e != nil {
			_ = ruleTx.Rollback(ctx)
			ruleErrors = append(ruleErrors, fmt.Errorf("rule %s: %w", a.ID, e))
			continue
		}
		if e = ruleTx.Commit(ctx); e != nil {
			ruleErrors = append(ruleErrors, fmt.Errorf("rule %s commit: %w", a.ID, e))
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return errors.Join(ruleErrors...)
}
func evaluateRule(ctx context.Context, tx pgx.Tx, r Rule, now time.Time) error {
	switch r.Kind {
	case "device_unavailable":
		return evalDevice(ctx, tx, r, now)
	case "threshold":
		return evalThreshold(ctx, tx, r, now)
	case "backup_failure", "backup_overdue":
		return evalBackup(ctx, tx, r, now)
	}
	return nil
}
func evalDevice(ctx context.Context, tx pgx.Tx, r Rule, now time.Time) error {
	rows, e := tx.Query(ctx, `SELECT id::text,last_seen_at,created_at FROM devices WHERE ($1='' OR id::text=$1)`, r.Config.DeviceID)
	if e != nil {
		return e
	}
	type d struct {
		id      string
		seen    *time.Time
		created time.Time
	}
	ds := []d{}
	for rows.Next() {
		var x d
		if e = rows.Scan(&x.id, &x.seen, &x.created); e != nil {
			rows.Close()
			return e
		}
		ds = append(ds, x)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	for _, x := range ds {
		since := x.created
		summary := "device has never checked in"
		if x.seen != nil {
			since = *x.seen
			summary = "device has not checked in since " + since.UTC().Format(time.RFC3339)
		}
		age := now.Sub(since)
		due := age > time.Duration(r.Config.StaleAfterSeconds+r.Config.GraceSeconds)*time.Second
		known := age <= time.Duration(r.Config.StaleAfterSeconds)*time.Second
		if e = transition(ctx, tx, r, x.id, due, known, summary, &since, now, true); e != nil {
			return e
		}
	}
	return nil
}
func evalThreshold(ctx context.Context, tx pgx.Tx, r Rule, now time.Time) error {
	labelFilter, _ := json.Marshal(r.Config.Labels)
	if len(r.Config.Labels) == 0 {
		labelFilter = []byte(`{}`)
	}
	lookback := r.Config.SustainSeconds + r.Config.GraceSeconds + 420
	rows, e := tx.Query(ctx, `SELECT i.device_id::text,t.observed_at,(m->>'value')::double precision,COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb) FROM agent_telemetry t JOIN agent_identities i ON i.id=t.agent_id CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.payload->'metrics','[]'::jsonb)) m WHERE i.revoked_at IS NULL AND ($1='' OR i.device_id::text=$1) AND t.observed_at>now()-make_interval(secs=>$3) AND m->>'name'=$2 AND COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb) @> $4::jsonb ORDER BY t.observed_at DESC,t.agent_id,t.report_id DESC LIMIT 100001`, r.Config.DeviceID, r.Config.Metric, lookback, labelFilter)
	if e != nil {
		return e
	}
	type sample struct {
		at    time.Time
		value float64
	}
	grouped := map[string][]sample{}
	summaries := map[string]string{}
	seenRows := 0
	for rows.Next() {
		seenRows++
		if seenRows > 100000 {
			rows.Close()
			return errors.New("more than 100000 matching metric observations in the evaluation window")
		}
		var dev string
		var at time.Time
		var value float64
		var labelData []byte
		if e = rows.Scan(&dev, &at, &value, &labelData); e != nil {
			rows.Close()
			return e
		}
		if at.After(now) {
			continue
		}
		var labels map[string]string
		if len(labelData) > 0 {
			if e = json.Unmarshal(labelData, &labels); e != nil {
				continue
			}
		}
		if labels == nil {
			labels = map[string]string{}
		}
		b, _ := json.Marshal(labels)
		dim := dev + "/" + string(b)
		grouped[dim] = append(grouped[dim], sample{at, value})
		if _, ok := summaries[dim]; !ok {
			summaries[dim] = fmt.Sprintf("%s value %g (condition %s %g)", r.Config.Metric, value, r.Config.Operator, r.Config.Threshold)
		}
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	for dim, ss := range grouped {
		latest := ss[0]
		if latest.at.After(now) || now.Sub(latest.at) > 5*time.Minute {
			continue
		}
		condition := compare(latest.value, r.Config.Operator, r.Config.Threshold)
		due, known := false, !condition
		if condition {
			oldest := latest.at
			prev := latest.at
			for _, sm := range ss[1:] {
				if prev.Sub(sm.at) > 2*time.Minute || !compare(sm.value, r.Config.Operator, r.Config.Threshold) {
					break
				}
				oldest = sm.at
				prev = sm.at
			}
			due = latest.at.Sub(oldest) >= time.Duration(r.Config.SustainSeconds)*time.Second && now.Sub(oldest) >= time.Duration(r.Config.SustainSeconds+r.Config.GraceSeconds)*time.Second
		}
		observed := latest.at
		if e = transition(ctx, tx, r, dim, due, known, summaries[dim], &observed, now, false); e != nil {
			return e
		}
	}
	return nil
}
func compare(v float64, op string, t float64) bool {
	switch op {
	case "gt":
		return v > t
	case "gte":
		return v >= t
	case "lt":
		return v < t
	case "lte":
		return v <= t
	}
	return false
}
func evalBackup(ctx context.Context, tx pgx.Tx, r Rule, now time.Time) error {
	var dev, key, status, msg string
	var at time.Time
	var observed *time.Time
	due, known := false, false
	summary := ""
	if r.Kind == "backup_overdue" {
		e := tx.QueryRow(ctx, `SELECT device_id::text,policy_key,status,observed_at,message FROM backup_status WHERE device_id=$1 AND policy_key=$2 AND status='success' ORDER BY observed_at DESC,id DESC LIMIT 1`, r.Config.DeviceID, r.Config.PolicyKey).Scan(&dev, &key, &status, &at, &msg)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		observed = &at
		if at.After(now) {
			return nil
		}
		due = now.Sub(at) > time.Duration(r.Config.IntervalSeconds+r.Config.GraceSeconds)*time.Second
		known = true
		summary = fmt.Sprintf("backup %q is overdue; last success was %s", key, at.UTC().Format(time.RFC3339))
	} else {
		e := tx.QueryRow(ctx, `SELECT device_id::text,policy_key,status,observed_at,message FROM backup_status WHERE device_id=$1 AND policy_key=$2 ORDER BY observed_at DESC,id DESC LIMIT 1`, r.Config.DeviceID, r.Config.PolicyKey).Scan(&dev, &key, &status, &at, &msg)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		observed = &at
		if at.After(now) {
			return nil
		}
		known = status == "success" || status == "failure"
		due = status == "failure"
		summary = fmt.Sprintf("backup %q failed", key)
		if msg != "" {
			summary += ": " + msg
		}
		if due && r.Config.GraceSeconds > 0 && now.Sub(at) < time.Duration(r.Config.GraceSeconds)*time.Second {
			due = false
			known = false
		}
	}
	return transition(ctx, tx, r, dev+"/"+key, due, known, summary, observed, now, r.Kind == "backup_overdue")
}
func transition(ctx context.Context, tx pgx.Tx, r Rule, dim string, due, known bool, summary string, observed *time.Time, now time.Time, timeDriven bool) error {
	var id, status string
	var last *time.Time
	e := tx.QueryRow(ctx, `SELECT id::text,status,last_observed_at FROM alert_incidents WHERE rule_id=$1 AND dimension=$2 FOR UPDATE`, r.ID, dim).Scan(&id, &status, &last)
	if errors.Is(e, pgx.ErrNoRows) {
		if !due {
			return nil
		}
		var raw []byte
		raw, _ = json.Marshal(map[string]any{"ruleId": r.ID, "rule": r.Name, "kind": r.Kind, "dimension": dim, "summary": summary, "observedAt": observed})
		e = tx.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at,last_observed_at) VALUES($1,$2,'firing',$3,$4,$5) RETURNING id::text`, r.ID, dim, summary, now, observed).Scan(&id)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO alert_events(incident_id,transition,payload) VALUES($1,'firing',$2)`, id, raw)
		return e
	}
	if e != nil {
		return e
	}
	if observed != nil && last != nil && !observed.After(*last) && !(due && timeDriven && status == "resolved") {
		return nil
	}
	if due && status == "firing" {
		if observed != nil && (last == nil || observed.After(*last)) {
			_, e = tx.Exec(ctx, `UPDATE alert_incidents SET summary=$1,last_observed_at=$2,updated_at=now() WHERE id=$3`, summary, observed, id)
		}
		return e
	}
	if due && status == "resolved" {
		var raw []byte
		raw, _ = json.Marshal(map[string]any{"ruleId": r.ID, "rule": r.Name, "kind": r.Kind, "dimension": dim, "summary": summary, "observedAt": observed})
		_, e = tx.Exec(ctx, `UPDATE alert_incidents SET status='firing',summary=$1,started_at=$2,last_observed_at=COALESCE($3,last_observed_at),resolved_at=NULL,updated_at=now() WHERE id=$4`, summary, now, observed, id)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO alert_events(incident_id,transition,payload) VALUES($1,'firing',$2)`, id, raw)
		return e
	}
	if !due && known && status == "firing" {
		resolved := now
		if observed != nil {
			resolved = *observed
		}
		var raw []byte
		raw, _ = json.Marshal(map[string]any{"ruleId": r.ID, "rule": r.Name, "kind": r.Kind, "dimension": dim, "summary": "condition recovered", "observedAt": observed})
		_, e = tx.Exec(ctx, `UPDATE alert_incidents SET status='resolved',resolved_at=$1,updated_at=now(),last_observed_at=COALESCE($2,last_observed_at) WHERE id=$3`, resolved, observed, id)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO alert_events(incident_id,transition,payload) VALUES($1,'resolved',$2)`, id, raw)
		return e
	}
	return nil
}
