package alerts

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Built-in rules whose incidents are raised by the image update worker rather
// than by Evaluate. Their configuration is fixed; only name, severity and the
// on/off switch can change.
var builtInKinds = map[string]bool{"image_update": true, "pinned_image": true}

const maxMuteMinutes = 30 * 24 * 60

func (h *Handler) registerInbox(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/alerts/incidents/{id}/acknowledge", h.acknowledge)
	mux.HandleFunc("DELETE /api/alerts/incidents/{id}/acknowledge", h.unacknowledge)
	mux.HandleFunc("PUT /api/alerts/incidents/{id}/mute", h.mute)
	mux.HandleFunc("DELETE /api/alerts/incidents/{id}/mute", h.unmute)
	mux.HandleFunc("POST /api/alerts/incidents/{id}/notes", h.addNote)
	mux.HandleFunc("PUT /api/alerts/rules/{id}", h.updateRule)
	mux.HandleFunc("POST /api/alerts/rules/{id}/enable", h.enableRule)
	mux.HandleFunc("GET /api/alerts/maintenance", h.listMaintenance)
	mux.HandleFunc("GET /api/alerts/timeline", h.timeline)
}

// sessionUser is requireUser that also returns who is signed in.
func (h *Handler) sessionUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	if !h.requireUser(w, r) {
		return auth.User{}, false
	}
	c, _ := r.Cookie(auth.SessionCookieName)
	u, e := h.auth.Authenticate(r.Context(), c.Value)
	if e != nil {
		http.Error(w, "login required", 401)
		return auth.User{}, false
	}
	return u, true
}

// updateIncident runs one UPDATE against an incident and answers 404 when it does not exist.
func (h *Handler) updateIncident(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	id := r.PathValue("id")
	if !validUUID(id) {
		http.Error(w, "incident id must be a UUID", 400)
		return
	}
	tag, e := h.pool.Exec(r.Context(), query, append([]any{id}, args...)...)
	if e != nil {
		http.Error(w, "could not update the alert", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "alert not found", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) acknowledge(w http.ResponseWriter, r *http.Request) {
	u, ok := h.sessionUser(w, r)
	if !ok {
		return
	}
	h.updateIncident(w, r, `UPDATE alert_incidents SET acknowledged_at=COALESCE(acknowledged_at,now()),acknowledged_by=COALESCE(acknowledged_by,$2::uuid),updated_at=now() WHERE id=$1`, u.ID)
}
func (h *Handler) unacknowledge(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	h.updateIncident(w, r, `UPDATE alert_incidents SET acknowledged_at=NULL,acknowledged_by=NULL,updated_at=now() WHERE id=$1`)
}
func (h *Handler) mute(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	var q struct {
		Minutes int `json:"minutes"`
	}
	if !decode(w, r, &q) {
		return
	}
	if q.Minutes < 1 || q.Minutes > maxMuteMinutes {
		http.Error(w, "minutes must be between 1 and 43200 (30 days)", 400)
		return
	}
	h.updateIncident(w, r, `UPDATE alert_incidents SET muted_until=now()+make_interval(mins=>$2),updated_at=now() WHERE id=$1`, q.Minutes)
}
func (h *Handler) unmute(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	h.updateIncident(w, r, `UPDATE alert_incidents SET muted_until=NULL,updated_at=now() WHERE id=$1`)
}

type Note struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

func (h *Handler) addNote(w http.ResponseWriter, r *http.Request) {
	u, ok := h.sessionUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		http.Error(w, "incident id must be a UUID", 400)
		return
	}
	var q struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Body = strings.TrimSpace(q.Body)
	if q.Body == "" || len(q.Body) > 2000 {
		http.Error(w, "a note must be 1..2000 characters", 400)
		return
	}
	author := u.DisplayName
	if author == "" {
		author = u.Username
	}
	var n Note
	e := h.pool.QueryRow(r.Context(), `INSERT INTO alert_notes(incident_id,author_id,author_name,body) SELECT id,$2,$3,$4 FROM alert_incidents WHERE id=$1 RETURNING id::text,author_name,body,created_at`, id, u.ID, author, q.Body).Scan(&n.ID, &n.Author, &n.Body, &n.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		http.Error(w, "alert not found", 404)
		return
	}
	if e != nil {
		http.Error(w, "could not save the note", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	write(w, n)
}

func validSeverity(s string) bool { return s == "warning" || s == "critical" }

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		http.Error(w, "rule id must be a UUID", 400)
		return
	}
	var q struct {
		Name     string  `json:"name"`
		Severity string  `json:"severity"`
		Config   *Config `json:"config,omitempty"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if !validSeverity(q.Severity) {
		http.Error(w, "severity must be warning or critical", 400)
		return
	}
	ctx := r.Context()
	tx, e := h.pool.Begin(ctx)
	if e != nil {
		http.Error(w, "could not update alert rule", 500)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(143001)`); e != nil {
		http.Error(w, "could not update alert rule", 500)
		return
	}
	var kind string
	var stored []byte
	e = tx.QueryRow(ctx, `SELECT kind,config FROM alert_rules WHERE id=$1 FOR UPDATE`, id).Scan(&kind, &stored)
	if errors.Is(e, pgx.ErrNoRows) {
		http.Error(w, "alert rule not found", 404)
		return
	}
	if e != nil {
		http.Error(w, "could not update alert rule", 500)
		return
	}
	config := stored
	if builtInKinds[kind] {
		if q.Name == "" || len(q.Name) > 120 {
			http.Error(w, "name must be 1..120 characters", 400)
			return
		}
		if q.Config != nil {
			http.Error(w, "this built-in rule has no settings to change", 400)
			return
		}
	} else {
		var c Config
		if q.Config != nil {
			c = *q.Config
		} else {
			_ = json.Unmarshal(stored, &c)
		}
		if e = validateRule(ruleRequest{Name: q.Name, Kind: kind, Config: c}); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		config, _ = json.Marshal(c)
	}
	var a Rule
	var b []byte
	e = tx.QueryRow(ctx, `UPDATE alert_rules SET name=$2,severity=$3,config=$4,updated_at=now() WHERE id=$1 RETURNING id::text,name,kind,enabled,severity,config,created_at`, id, q.Name, q.Severity, config).Scan(&a.ID, &a.Name, &a.Kind, &a.Enabled, &a.Severity, &b, &a.CreatedAt)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		http.Error(w, "could not update alert rule", 500)
		return
	}
	_ = json.Unmarshal(b, &a.Config)
	write(w, a)
}

func (h *Handler) enableRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		http.Error(w, "rule id must be a UUID", 400)
		return
	}
	tag, e := h.pool.Exec(r.Context(), `UPDATE alert_rules SET enabled=true,updated_at=now() WHERE id=$1`, id)
	if e != nil {
		http.Error(w, "could not enable alert rule", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "alert rule not found", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type FleetMaintenanceWindow struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"deviceId"`
	DeviceName string    `json:"deviceName"`
	StartsAt   time.Time `json:"startsAt"`
	EndsAt     time.Time `json:"endsAt"`
	Active     bool      `json:"active"`
}

// listMaintenance lists current and upcoming maintenance windows on every device.
func (h *Handler) listMaintenance(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	rows, e := h.pool.Query(r.Context(), `SELECT m.id::text,m.device_id::text,d.display_name,m.starts_at,m.ends_at,m.starts_at<=now() FROM device_maintenance_windows m JOIN devices d ON d.id=m.device_id WHERE m.ends_at>now() ORDER BY m.starts_at,d.display_name LIMIT 500`)
	if e != nil {
		http.Error(w, "could not read maintenance windows", 500)
		return
	}
	defer rows.Close()
	out := []FleetMaintenanceWindow{}
	for rows.Next() {
		var x FleetMaintenanceWindow
		if e = rows.Scan(&x.ID, &x.DeviceID, &x.DeviceName, &x.StartsAt, &x.EndsAt, &x.Active); e != nil {
			http.Error(w, "could not read maintenance windows", 500)
			return
		}
		out = append(out, x)
	}
	if e = rows.Err(); e != nil {
		http.Error(w, "could not read maintenance windows", 500)
		return
	}
	write(w, out)
}

// TimelineItem is one thing that happened: an alert firing or clearing, or a
// newer image found for a container.
type TimelineItem struct {
	Kind       string    `json:"kind"` // firing, resolved, image
	At         time.Time `json:"at"`
	Summary    string    `json:"summary"`
	Rule       string    `json:"rule,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	Dimension  string    `json:"dimension,omitempty"`
	IncidentID string    `json:"incidentId,omitempty"`
	DeviceID   string    `json:"deviceId,omitempty"`
	DeviceName string    `json:"deviceName,omitempty"`
	AgentID    string    `json:"agentId,omitempty"`
	Container  string    `json:"container,omitempty"`
	Image      string    `json:"image,omitempty"`
}

// timeline merges alert transitions and new-image notices, newest first.
func (h *Handler) timeline(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	rows, e := h.pool.Query(r.Context(), `(SELECT e.transition,e.occurred_at,COALESCE(NULLIF(e.payload->>'summary',''),i.summary),COALESCE(r.name,''),COALESCE(r.severity,''),i.dimension,i.id::text,'','','','','' FROM alert_events e JOIN alert_incidents i ON i.id=e.incident_id LEFT JOIN alert_rules r ON r.id=i.rule_id ORDER BY e.occurred_at DESC LIMIT 200)
UNION ALL
(SELECT 'image',n.created_at,'',''::text,''::text,'','',d.id::text,d.display_name,n.agent_id::text,COALESCE(u.result->>'name',''),COALESCE(u.result->>'image','') FROM image_update_notices n JOIN agent_identities ai ON ai.id=n.agent_id JOIN devices d ON d.id=ai.device_id LEFT JOIN container_image_updates u ON u.agent_id=n.agent_id AND u.container_id=n.container_id WHERE n.container_id NOT LIKE 'stack:%' ORDER BY n.created_at DESC LIMIT 200)
ORDER BY 2 DESC LIMIT 300`)
	if e != nil {
		http.Error(w, "could not read alert history", 500)
		return
	}
	defer rows.Close()
	out := []TimelineItem{}
	for rows.Next() {
		var x TimelineItem
		if e = rows.Scan(&x.Kind, &x.At, &x.Summary, &x.Rule, &x.Severity, &x.Dimension, &x.IncidentID, &x.DeviceID, &x.DeviceName, &x.AgentID, &x.Container, &x.Image); e != nil {
			http.Error(w, "could not read alert history", 500)
			return
		}
		out = append(out, x)
	}
	if e = rows.Err(); e != nil {
		http.Error(w, "could not read alert history", 500)
		return
	}
	write(w, out)
}
