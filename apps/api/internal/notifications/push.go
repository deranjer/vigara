package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PushChannelID is the built-in channel that delivers alerts to phones running
// the Vigara app. Unlike the other channels it needs no environment settings:
// it is always present and sends to every phone registered below.
const PushChannelID = notifytransport.PushTargetID

const maxPhonesPerUser = 10

// Phone is one registered app install, as shown in settings. Endpoints and keys
// never leave the server.
type Phone struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	User          string     `json:"user"`
	Mine          bool       `json:"mine"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
}

// PushDestination sends each alert to every registered phone. With no phones
// it delivers to nobody and reports success, so nothing piles up as failed.
// vapid, when set, signs each push so push apps that require a server key work.
func PushDestination(pool *pgxpool.Pool, client *http.Client, vapid *notifytransport.VAPID) Destination {
	return Destination{ID: PushChannelID, Kind: "android", Target: "Vigara app", Send: func(ctx context.Context, m Message) error {
		return sendToPhones(ctx, pool, client, vapid, m, "")
	}}
}

type phoneRow struct {
	id, endpoint string
	keys         notifytransport.PushKeys
}

// sendToPhones delivers to all phones, or one when phoneID is set. A phone
// the distributor no longer knows is forgotten. Delivery succeeds when at
// least one phone took the message, so a retry never repeats it on phones
// that already have it.
func sendToPhones(ctx context.Context, pool *pgxpool.Pool, client *http.Client, vapid *notifytransport.VAPID, m Message, phoneID string) error {
	query := `SELECT id::text,endpoint,p256dh,auth FROM push_phones ORDER BY created_at`
	args := []any{}
	if phoneID != "" {
		query = `SELECT id::text,endpoint,p256dh,auth FROM push_phones WHERE id=$1`
		args = append(args, phoneID)
	}
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	phones, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (phoneRow, error) {
		var p phoneRow
		return p, r.Scan(&p.id, &p.endpoint, &p.keys.P256DH, &p.keys.Auth)
	})
	if err != nil {
		return err
	}
	payload := notifytransport.RenderPush(notifytransport.Message{
		EventID: m.EventID, IncidentID: m.IncidentID, Transition: m.Transition, DeviceName: m.DeviceName,
		RuleName: m.RuleName, Summary: m.Summary, OccurredAt: m.OccurredAt, Severity: m.Severity, OpenURL: m.OpenURL,
	})
	urgent := m.Transition == "firing" && m.Severity == "critical"
	var firstErr error
	delivered := false
	for _, p := range phones {
		err := notifytransport.SendPush(ctx, client, vapid, p.endpoint, p.keys, payload, urgent)
		switch {
		case err == nil:
			delivered = true
			_, _ = pool.Exec(ctx, `UPDATE push_phones SET last_success_at=now(),last_error=NULL WHERE id=$1`, p.id)
		case notifytransport.IsGone(err):
			_, _ = pool.Exec(ctx, `DELETE FROM push_phones WHERE id=$1`, p.id)
			// A test to one phone must say it did not arrive.
			if phoneID != "" && firstErr == nil {
				firstErr = err
			}
		default:
			_, _ = pool.Exec(ctx, `UPDATE push_phones SET last_error=$2 WHERE id=$1`, p.id, errorCode(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if delivered {
		return nil
	}
	return firstErr
}

// attachPhones adds the phones the user may see to the Android channel.
func (h *Handler) attachPhones(ctx context.Context, u auth.User, out *Settings) error {
	for i := range out.Channels {
		if out.Channels[i].ID != PushChannelID {
			continue
		}
		list, err := h.phones(ctx, u)
		if err != nil {
			return err
		}
		out.Channels[i].Phones = list
	}
	return nil
}

// registerPush serves the phone endpoints. Every signed-in user may register
// their own phones; admins also see and remove everyone's.
func (h *Handler) registerPush(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/notifications/phones", h.listPhones)
	mux.HandleFunc("PUT /api/notifications/phones", h.putPhone)
	mux.HandleFunc("DELETE /api/notifications/phones/{id}", h.deletePhone)
	mux.HandleFunc("POST /api/notifications/phones/{id}/test", h.testPhone)
	mux.HandleFunc("GET /api/notifications/push-key", h.pushKey)
}

// SetVAPID gives the handler the server key that phones register with and
// that signs test pushes.
func (h *Handler) SetVAPID(v *notifytransport.VAPID) { h.vapid = v }

// pushKey returns the public VAPID key the app hands its push app when it
// registers. An empty key means the server sends without one.
func (h *Handler) pushKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	key := ""
	if h.vapid != nil {
		key = h.vapid.PublicKey()
	}
	writeJSON(w, map[string]string{"vapidKey": key})
}

// LoadVAPID returns the server's VAPID key, creating it on first use. It lives
// in notification_settings so every replica and restart signs with the same
// key; a new key would orphan every phone registered with the old one.
func LoadVAPID(ctx context.Context, pool *pgxpool.Pool, subject string) (*notifytransport.VAPID, error) {
	fresh, err := notifytransport.GenerateVAPIDKey()
	if err != nil {
		return nil, err
	}
	var key []byte
	if err := pool.QueryRow(ctx, `UPDATE notification_settings SET vapid_key=COALESCE(vapid_key,$1) RETURNING vapid_key`, fresh).Scan(&key); err != nil {
		return nil, err
	}
	return notifytransport.NewVAPID(key, subject)
}

// VAPIDSubject picks the contact sent to push servers: an explicit setting,
// else the server's public https URL, else a placeholder address.
func VAPIDSubject(explicit, publicURL string) string {
	if notifytransport.ValidVAPIDSubject(explicit) {
		return explicit
	}
	if u := strings.TrimRight(strings.TrimSpace(publicURL), "/"); strings.HasPrefix(u, "https://") && notifytransport.ValidVAPIDSubject(u) {
		return u
	}
	return "mailto:vigara@localhost"
}

func (h *Handler) phones(ctx context.Context, u auth.User) ([]Phone, error) {
	rows, err := h.pool.Query(ctx, `SELECT p.id::text,p.name,COALESCE(NULLIF(u.display_name,''),u.username),p.user_id::text=$1,p.created_at,p.last_success_at,COALESCE(p.last_error,'')
 FROM push_phones p JOIN users u ON u.id=p.user_id WHERE $2 OR p.user_id::text=$1 ORDER BY p.created_at`, u.ID, u.Can(auth.PermHostsManage))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Phone, error) {
		var p Phone
		return p, r.Scan(&p.ID, &p.Name, &p.User, &p.Mine, &p.CreatedAt, &p.LastSuccessAt, &p.LastError)
	})
}

func (h *Handler) listPhones(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	list, err := h.phones(r.Context(), u)
	if err != nil {
		http.Error(w, "could not read phones", 500)
		return
	}
	writeJSON(w, list)
}

type phoneRequest struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func cleanPhoneName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "Android phone"
	}
	for utf8.RuneCountInString(name) > 80 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// putPhone registers this phone, or refreshes it when the distributor hands
// out a new endpoint or keys. The endpoint identifies the install.
func (h *Handler) putPhone(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	var q phoneRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	if err := notifytransport.ValidatePushEndpoint(q.Endpoint); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	keys := notifytransport.PushKeys{P256DH: strings.TrimRight(q.P256DH, "="), Auth: strings.TrimRight(q.Auth, "=")}
	if err := notifytransport.ValidatePushKeys(keys); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var count int
	if err := h.pool.QueryRow(r.Context(), `SELECT count(*) FROM push_phones WHERE user_id=$1 AND endpoint<>$2`, u.ID, q.Endpoint).Scan(&count); err != nil {
		http.Error(w, "could not register the phone", 500)
		return
	}
	if count >= maxPhonesPerUser {
		http.Error(w, "you already have 10 phones registered; remove one in Settings › Notifications first", 409)
		return
	}
	var id string
	err := h.pool.QueryRow(r.Context(), `INSERT INTO push_phones(user_id,name,endpoint,p256dh,auth) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(endpoint) DO UPDATE SET user_id=EXCLUDED.user_id,name=EXCLUDED.name,p256dh=EXCLUDED.p256dh,auth=EXCLUDED.auth,updated_at=now(),last_error=NULL
 RETURNING id::text`, u.ID, cleanPhoneName(q.Name), q.Endpoint, keys.P256DH, keys.Auth).Scan(&id)
	if err != nil {
		http.Error(w, "could not register the phone", 500)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

// ownPhone answers whether the signed-in user may act on a phone: their own,
// or any phone for an admin.
func (h *Handler) ownPhone(w http.ResponseWriter, r *http.Request) (string, bool) {
	u, ok := h.user(w, r)
	if !ok {
		return "", false
	}
	id := r.PathValue("id")
	var owner string
	err := h.pool.QueryRow(r.Context(), `SELECT user_id::text FROM push_phones WHERE id::text=$1`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != u.ID && !u.Can(auth.PermHostsManage)) {
		http.Error(w, "phone not found", 404)
		return "", false
	}
	if err != nil {
		http.Error(w, "could not read the phone", 500)
		return "", false
	}
	return id, true
}

func (h *Handler) deletePhone(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ownPhone(w, r)
	if !ok {
		return
	}
	if _, err := h.pool.Exec(r.Context(), `DELETE FROM push_phones WHERE id::text=$1`, id); err != nil {
		http.Error(w, "could not remove the phone", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testPhone(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ownPhone(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), SendTimeout)
	defer cancel()
	open, _, _ := policy{public: configuredPublicURL()}.links("", time.Now())
	err := sendToPhones(ctx, h.pool, h.pushClient, h.vapid, Message{EventID: "test", Transition: "test", RuleName: "Test notification", Severity: "warning",
		Summary: "This is a test from Vigara. Alerts will arrive on this phone.", OccurredAt: time.Now().UTC(), OpenURL: open}, id)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": errorCode(err)})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
