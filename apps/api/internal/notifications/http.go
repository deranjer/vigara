package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler serves notification settings, channel tests, and the signed
// Acknowledge / Mute links that notifications carry.
type Handler struct {
	pool         *pgxpool.Pool
	auth         *auth.Service
	destinations []Destination
	// pushClient sends to phones; nil uses the bounded default client.
	pushClient *http.Client
	vapid      *notifytransport.VAPID
}

func NewHandler(pool *pgxpool.Pool, service *auth.Service, destinations []Destination) *Handler {
	return &Handler{pool: pool, auth: service, destinations: destinations}
}

// Register serves /api/notifications/*; RegisterActions serves the
// notification buttons under /api/alerts/actions/, next to the alert API.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/notifications", h.get)
	mux.HandleFunc("PUT /api/notifications/settings", h.put)
	mux.HandleFunc("POST /api/notifications/channels/{id}/test", h.test)
	h.registerPush(mux)
}
func (h *Handler) RegisterActions(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/alerts/actions/{token}", h.action)
}

func (h *Handler) user(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "request origin rejected", 403)
			return auth.User{}, false
		}
	}
	c, e := r.Cookie(auth.SessionCookieName)
	if e != nil {
		http.Error(w, "login required", 401)
		return auth.User{}, false
	}
	u, e := h.auth.Authenticate(r.Context(), c.Value)
	if e != nil {
		http.Error(w, "login required", 401)
		return auth.User{}, false
	}
	return u, true
}

// Settings for the whole server, so changing them needs the admin role.
func (h *Handler) admin(w http.ResponseWriter, r *http.Request) bool {
	u, ok := h.user(w, r)
	if ok && !u.Can(auth.PermHostsManage) {
		http.Error(w, "only administrators can change notifications", 403)
		return false
	}
	return ok
}

type Channel struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Target          string     `json:"target"`
	Route           Route      `json:"route"`
	LastDeliveredAt *time.Time `json:"lastDeliveredAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	LastErrorAt     *time.Time `json:"lastErrorAt,omitempty"`
	Pending         int        `json:"pending"`
	Held            int        `json:"held"`
	// Phones lists the app installs behind the Android channel that the
	// signed-in user can see: their own, or all of them for an admin.
	Phones     []Phone `json:"phones,omitempty"`
	PhoneCount int     `json:"phoneCount,omitempty"`
}
type Settings struct {
	Channels   []Channel  `json:"channels"`
	QuietHours QuietHours `json:"quietHours"`
	// LinksEnabled is false without VIGIRA_PUBLIC_URL: no tap target or action buttons.
	LinksEnabled bool `json:"linksEnabled"`
	CanManage    bool `json:"canManage"`
}

func (h *Handler) read(ctx context.Context) (Settings, error) {
	out := Settings{Channels: []Channel{}, LinksEnabled: configuredPublicURL() != ""}
	if err := h.pool.QueryRow(ctx, `SELECT quiet_enabled,to_char(quiet_start,'HH24:MI'),to_char(quiet_end,'HH24:MI'),timezone FROM notification_settings`).Scan(&out.QuietHours.Enabled, &out.QuietHours.Start, &out.QuietHours.End, &out.QuietHours.Timezone); err != nil {
		return out, err
	}
	for _, d := range h.destinations {
		c := Channel{ID: d.ID, Kind: d.Kind, Target: d.Target, Route: Route{true, true, true}}
		var lastError *string
		err := h.pool.QueryRow(ctx, `SELECT d.notify_critical,d.notify_warning,d.notify_resolved,
 (SELECT max(updated_at) FROM notification_deliveries WHERE destination_id=d.id AND status='delivered'),
 (SELECT count(*) FROM notification_deliveries WHERE destination_id=d.id AND status IN ('pending','sending') AND NOT (COALESCE(last_error_code,'')='quiet_hours' AND next_attempt_at>now())),
 (SELECT count(*) FROM notification_deliveries WHERE destination_id=d.id AND status='pending' AND last_error_code='quiet_hours' AND next_attempt_at>now()),
 f.last_error_code,f.updated_at
 FROM notification_destinations d LEFT JOIN LATERAL (SELECT last_error_code,updated_at FROM notification_deliveries
   WHERE destination_id=d.id AND last_error_code IS NOT NULL AND status IN ('pending','dead') AND last_error_code<>'quiet_hours' ORDER BY updated_at DESC LIMIT 1) f ON true
 WHERE d.id=$1`, d.ID).Scan(&c.Route.Critical, &c.Route.Warning, &c.Route.Resolved, &c.LastDeliveredAt, &c.Pending, &c.Held, &lastError, &c.LastErrorAt)
		if err != nil {
			return out, err
		}
		if lastError != nil {
			c.LastError = *lastError
		}
		if d.ID == PushChannelID {
			var phones int
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM push_phones`).Scan(&phones); err != nil {
				return out, err
			}
			c.PhoneCount = phones
			switch phones {
			case 0:
				// Alerts "delivered" to no phones are not deliveries.
				c.Target, c.LastDeliveredAt = "No phones yet", nil
			case 1:
				c.Target = "1 phone"
			default:
				c.Target = fmt.Sprintf("%d phones", phones)
			}
		}
		// A failure older than the last delivery is history, not current status.
		if c.LastErrorAt != nil && c.LastDeliveredAt != nil && c.LastDeliveredAt.After(*c.LastErrorAt) {
			c.LastError, c.LastErrorAt = "", nil
		}
		out.Channels = append(out.Channels, c)
	}
	return out, nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	out, err := h.read(r.Context())
	if err != nil {
		http.Error(w, "could not read notification settings", 500)
		return
	}
	out.CanManage = u.Can(auth.PermHostsManage)
	if err := h.attachPhones(r.Context(), u, &out); err != nil {
		http.Error(w, "could not read notification settings", 500)
		return
	}
	writeJSON(w, out)
}

type settingsRequest struct {
	QuietHours QuietHours       `json:"quietHours"`
	Routes     map[string]Route `json:"routes"`
}

func validQuiet(q QuietHours) error {
	if _, ok := clockMinutes(q.Start); !ok {
		return errors.New("quiet hours start must be HH:MM")
	}
	if _, ok := clockMinutes(q.End); !ok {
		return errors.New("quiet hours end must be HH:MM")
	}
	if q.Start == q.End {
		return errors.New("quiet hours must start and end at different times")
	}
	if q.Timezone == "" || q.Timezone == "Local" || len(q.Timezone) > 128 {
		return errors.New("timezone must be an IANA name such as Europe/Berlin")
	}
	if _, err := time.LoadLocation(q.Timezone); err != nil {
		return errors.New("timezone must be an IANA name such as Europe/Berlin")
	}
	return nil
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	if !u.Can(auth.PermHostsManage) {
		http.Error(w, "only administrators can change notifications", 403)
		return
	}
	var q settingsRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	if err := validQuiet(q.QuietHours); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	known := map[string]bool{}
	for _, d := range h.destinations {
		known[d.ID] = true
	}
	for id := range q.Routes {
		if !known[id] {
			http.Error(w, "unknown channel "+id, 400)
			return
		}
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		http.Error(w, "could not save notification settings", 500)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE notification_settings SET quiet_enabled=$1,quiet_start=$2::time,quiet_end=$3::time,timezone=$4,updated_at=now()`, q.QuietHours.Enabled, q.QuietHours.Start, q.QuietHours.End, q.QuietHours.Timezone)
	for id, route := range q.Routes {
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE notification_destinations SET notify_critical=$2,notify_warning=$3,notify_resolved=$4 WHERE id=$1`, id, route.Critical, route.Warning, route.Resolved)
		}
	}
	// Turning quiet hours off releases anything they were holding.
	if err == nil && !q.QuietHours.Enabled {
		_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at=now(),updated_at=now() WHERE status='pending' AND last_error_code='quiet_hours'`)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		http.Error(w, "could not save notification settings", 500)
		return
	}
	out, err := h.read(ctx)
	if err != nil {
		http.Error(w, "could not read notification settings", 500)
		return
	}
	out.CanManage = true
	if err := h.attachPhones(ctx, u, &out); err != nil {
		http.Error(w, "could not read notification settings", 500)
		return
	}
	writeJSON(w, out)
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	for _, d := range h.destinations {
		if d.ID != r.PathValue("id") {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), SendTimeout)
		defer cancel()
		if d.ID == PushChannelID {
			var phones int
			if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM push_phones`).Scan(&phones); err != nil || phones == 0 {
				writeJSON(w, map[string]any{"ok": false, "error": "no_phones"})
				return
			}
		}
		open, _, _ := policy{public: configuredPublicURL()}.links("", time.Now())
		err := d.Send(ctx, Message{EventID: "test", Transition: "test", RuleName: "Test notification", Severity: "warning",
			Summary: "This is a test from Vigara. Alerts will arrive here.", OccurredAt: time.Now().UTC(), OpenURL: open})
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": errorCode(err)})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	http.Error(w, "channel not found", 404)
}

// action handles the Acknowledge and Mute buttons inside a notification. The
// signed link is the credential; it names one alert and one action and expires.
func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
	var key []byte
	if err := h.pool.QueryRow(r.Context(), `SELECT action_key FROM notification_settings`).Scan(&key); err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	incident, action, err := verifyAction(key, r.PathValue("token"), time.Now())
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	query := `UPDATE alert_incidents SET acknowledged_at=COALESCE(acknowledged_at,now()),updated_at=now() WHERE id=$1`
	done := "Acknowledged"
	if action == "mute" {
		query = `UPDATE alert_incidents SET muted_until=GREATEST(COALESCE(muted_until,now()),now()+interval '24 hours'),updated_at=now() WHERE id=$1`
		done = "Muted for 24 hours"
	}
	tag, err := h.pool.Exec(r.Context(), query, incident)
	if err != nil {
		http.Error(w, "could not update the alert", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "alert not found", 404)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, done+"\n")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
