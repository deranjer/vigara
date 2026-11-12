package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MasterKeyVar names the key that encrypts channel secrets at rest.
const MasterKeyVar = "VIGIRA_NOTIFICATION_MASTER_KEY"

// Where a channel is defined, which decides whether settings can edit it.
const (
	SourceSettings = "settings" // stored in the database; editable
	SourceServer   = "server"   // environment variables not yet imported; read-only
	SourceApp      = "app"      // the built-in Vigara app channel
)

const maxChannels = 20

// Sealer encrypts channel configurations with AES-GCM. The channel ID is the
// associated data, so a sealed config cannot be moved to another channel.
type Sealer struct{ aead cipher.AEAD }

// NewSealer reads a base64-encoded 32-byte key. An empty key returns nil:
// channels then cannot be stored, and environment channels stay read-only.
func NewSealer(encoded string) (*Sealer, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New(MasterKeyVar + " must encode exactly 32 bytes in base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) seal(id string, c notifytransport.ChannelConfig) ([]byte, error) {
	plain, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte(id)), nil
}

func (s *Sealer) open(id string, sealed []byte) (notifytransport.ChannelConfig, error) {
	var c notifytransport.ChannelConfig
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return c, errors.New("sealed channel too short")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], []byte(id))
	if err != nil {
		return c, errors.New("channel secrets cannot be decrypted with this key")
	}
	return c, json.Unmarshal(plain, &c)
}

// Registry is the live set of channels: stored ones, environment ones that
// could not be imported, and the app channel. Run keeps one delivery worker
// per channel and restarts workers when a channel changes.
type Registry struct {
	pool    *pgxpool.Pool
	sealer  *Sealer
	builtin []Destination

	mu      sync.Mutex
	stored  []storedChannel
	broken  map[string]string // channel ID → why it cannot send
	running map[string]*worker
	version string
	runCtx  context.Context
	logger  Logger
}

type storedChannel struct {
	dest    Destination
	config  notifytransport.ChannelConfig
	updated time.Time
}

type worker struct {
	cancel  context.CancelFunc
	updated time.Time
}

// NewRegistry starts with the built-in destinations (the app channel and any
// environment channels that stay read-only). Call Reload to add stored ones.
func NewRegistry(pool *pgxpool.Pool, sealer *Sealer, builtin []Destination) *Registry {
	return &Registry{pool: pool, sealer: sealer, builtin: builtin, broken: map[string]string{}, running: map[string]*worker{}}
}

// CanStore reports whether channels can be added or edited.
func (r *Registry) CanStore() bool { return r.sealer != nil }

// Destinations lists every channel: stored ones by name, then built-in ones.
func (r *Registry) Destinations() []Destination {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Destination, 0, len(r.stored)+len(r.builtin))
	for _, s := range r.stored {
		out = append(out, s.dest)
	}
	return append(out, r.builtin...)
}

func (r *Registry) config(id string) (notifytransport.ChannelConfig, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.stored {
		if s.dest.ID == id {
			return s.config, true
		}
	}
	return notifytransport.ChannelConfig{}, false
}

// Reload reads stored channels and, while Run is active, starts, restarts or
// stops their workers to match.
func (r *Registry) Reload(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `SELECT id,kind,name,sealed,updated_at FROM notification_channels ORDER BY lower(name),id`)
	if err != nil {
		return err
	}
	type row struct {
		id, kind, name string
		sealed         []byte
		updated        time.Time
	}
	list, err := pgx.CollectRows(rows, func(c pgx.CollectableRow) (row, error) {
		var x row
		return x, c.Scan(&x.id, &x.kind, &x.name, &x.sealed, &x.updated)
	})
	if err != nil {
		return err
	}
	stored := make([]storedChannel, 0, len(list))
	broken := map[string]string{}
	for _, x := range list {
		s := storedChannel{updated: x.updated}
		s.dest = Destination{ID: x.id, Kind: x.kind, Name: x.name, Source: SourceSettings}
		var target notifytransport.Target
		if r.sealer == nil {
			broken[x.id] = "no_master_key"
		} else if s.config, err = r.sealer.open(x.id, x.sealed); err != nil {
			broken[x.id] = "secrets_unreadable"
		} else if target, err = notifytransport.NewTarget(x.id, x.kind, s.config); err != nil {
			broken[x.id] = "invalid_destination"
		}
		if reason := broken[x.id]; reason != "" {
			s.dest.Target = "Cannot send: " + strings.ReplaceAll(reason, "_", " ")
			s.dest.Send = func(context.Context, Message) error { return codedError(reason) }
		} else {
			s.dest.Target = target.Display
			s.dest.Send = transportSend(target)
		}
		stored = append(stored, s)
	}
	r.mu.Lock()
	r.stored, r.broken = stored, broken
	r.mu.Unlock()
	r.sync()
	return nil
}

// Run delivers for every channel until ctx ends, picking up channel changes
// made here (through Reload) or by another server every 30 seconds.
func (r *Registry) Run(ctx context.Context, logger Logger) {
	r.mu.Lock()
	r.runCtx, r.logger = ctx, logger
	r.mu.Unlock()
	if err := Initialize(ctx, r.pool, r.builtin); err != nil {
		safeLog(logger, "notification destination initialization failed", "", "initialization_failed")
	}
	r.sync()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			for id, w := range r.running {
				w.cancel()
				delete(r.running, id)
			}
			r.mu.Unlock()
			return
		case <-ticker.C:
			var stamp string
			if err := r.pool.QueryRow(ctx, `SELECT COALESCE(max(updated_at)::text,'')||count(*)::text FROM notification_channels`).Scan(&stamp); err == nil && stamp != r.stamp() {
				if r.Reload(ctx) == nil {
					r.setStamp(stamp)
				}
			}
		}
	}
}

func (r *Registry) stamp() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}
func (r *Registry) setStamp(s string) {
	r.mu.Lock()
	r.version = s
	r.mu.Unlock()
}

// sync makes the running workers match the channel list. Channels that cannot
// send get no worker, so their alerts wait instead of failing.
func (r *Registry) sync() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runCtx == nil || r.runCtx.Err() != nil {
		return
	}
	want := map[string]Destination{}
	updated := map[string]time.Time{}
	for _, d := range r.builtin {
		want[d.ID] = d
	}
	for _, s := range r.stored {
		if r.broken[s.dest.ID] == "" {
			want[s.dest.ID], updated[s.dest.ID] = s.dest, s.updated
		}
	}
	for id, w := range r.running {
		if d, ok := want[id]; !ok || !updated[d.ID].Equal(w.updated) {
			w.cancel()
			delete(r.running, id)
		}
	}
	for id, d := range want {
		if _, ok := r.running[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(r.runCtx)
		r.running[id] = &worker{cancel: cancel, updated: updated[id]}
		go runDestination(ctx, r.pool, d, r.logger)
	}
}

func transportSend(target notifytransport.Target) func(context.Context, Message) error {
	return func(ctx context.Context, m Message) error {
		return target.Send(ctx, notifytransport.Message{
			EventID: m.EventID, IncidentID: m.IncidentID, Transition: m.Transition,
			DeviceName: m.DeviceName, RuleName: m.RuleName, Summary: m.Summary, OccurredAt: m.OccurredAt,
			Severity: m.Severity, OpenURL: m.OpenURL, AcknowledgeURL: m.AcknowledgeURL, MuteURL: m.MuteURL,
		})
	}
}

// EnvDestinations turns environment channels into read-only destinations, for
// servers without a master key to import them with.
func EnvDestinations(channels []notifytransport.EnvChannel) ([]Destination, error) {
	out := make([]Destination, 0, len(channels))
	for _, c := range channels {
		t, err := notifytransport.NewTarget(c.ID, c.Kind, c.Config)
		if err != nil {
			return nil, err
		}
		out = append(out, Destination{ID: c.ID, Kind: c.Kind, Name: kindName(c.Kind), Target: t.Display, Source: SourceServer, Send: transportSend(t)})
	}
	return out, nil
}

// ImportEnv copies environment channels into the database once each, keeping
// their IDs so routing and delivery history carry over. It returns the
// channels that are ignored from now on because they were imported earlier,
// and those it could not import (no master key), which stay read-only.
func ImportEnv(ctx context.Context, pool *pgxpool.Pool, sealer *Sealer, channels []notifytransport.EnvChannel) (ignored, readOnly []notifytransport.EnvChannel, err error) {
	if len(channels) == 0 {
		return nil, nil, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	var done []string
	if err := tx.QueryRow(ctx, `SELECT env_imported FROM notification_settings FOR UPDATE`).Scan(&done); err != nil {
		return nil, nil, err
	}
	for _, c := range channels {
		if contains(done, c.ID) {
			ignored = append(ignored, c)
			continue
		}
		if sealer == nil {
			readOnly = append(readOnly, c)
			continue
		}
		sealed, err := sealer.seal(c.ID, c.Config)
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_destinations(id,activated_at) VALUES($1,clock_timestamp()) ON CONFLICT(id) DO NOTHING`, c.ID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_channels(id,kind,name,sealed) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, c.ID, c.Kind, kindName(c.Kind), sealed); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE notification_settings SET env_imported=array_append(env_imported,$1)`, c.ID); err != nil {
			return nil, nil, err
		}
	}
	return ignored, readOnly, tx.Commit(ctx)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func kindName(kind string) string {
	switch kind {
	case notifytransport.KindGotify:
		return "Gotify"
	case notifytransport.KindDiscord:
		return "Discord"
	}
	return kind
}

type codedError string

func (e codedError) Error() string     { return "notification channel unavailable (" + string(e) + ")" }
func (e codedError) ErrorCode() string { return string(e) }
func (e codedError) Temporary() bool   { return true }

// ---- HTTP ----

// ChannelForm is the editable part of a channel. Secrets are write-only: a
// nil Token or WebhookURL on update keeps the stored value.
type ChannelForm struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	URL        string  `json:"url"`
	Topic      string  `json:"topic"`
	Token      *string `json:"token"`
	WebhookURL *string `json:"webhookUrl"`
}

// ChannelConfigView is what the edit form shows; secrets only say whether set.
type ChannelConfigView struct {
	URL      string `json:"url,omitempty"`
	Topic    string `json:"topic,omitempty"`
	HasToken bool   `json:"hasToken"`
}

func (h *Handler) registerChannels(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/notifications/channels", h.createChannel)
	mux.HandleFunc("POST /api/notifications/channels/check", h.checkChannel)
	mux.HandleFunc("PUT /api/notifications/channels/{id}", h.updateChannel)
	mux.HandleFunc("DELETE /api/notifications/channels/{id}", h.deleteChannel)
}

func (h *Handler) storeReady(w http.ResponseWriter, r *http.Request) bool {
	if !h.admin(w, r) {
		return false
	}
	if h.registry == nil || !h.registry.CanStore() {
		http.Error(w, "Set "+MasterKeyVar+" on the server to add or edit channels here.", 503)
		return false
	}
	return true
}

func decodeForm(w http.ResponseWriter, r *http.Request) (ChannelForm, bool) {
	var f ChannelForm
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil {
		http.Error(w, "invalid JSON request", 400)
		return f, false
	}
	f.Name = strings.Join(strings.Fields(f.Name), " ")
	if f.Name == "" || utf8.RuneCountInString(f.Name) > 80 {
		http.Error(w, "Name must be 1-80 characters", 400)
		return f, false
	}
	return f, true
}

// configFrom applies the form to a previous configuration.
func configFrom(f ChannelForm, prev notifytransport.ChannelConfig) notifytransport.ChannelConfig {
	c := notifytransport.ChannelConfig{URL: strings.TrimSpace(f.URL), Topic: strings.TrimSpace(f.Topic), Token: prev.Token, WebhookURL: prev.WebhookURL}
	if f.Token != nil {
		c.Token = strings.TrimSpace(*f.Token)
	}
	if f.WebhookURL != nil {
		c.WebhookURL = strings.TrimSpace(*f.WebhookURL)
	}
	switch f.Kind {
	case notifytransport.KindDiscord:
		c.URL, c.Topic, c.Token = "", "", ""
	case notifytransport.KindGotify:
		c.Topic, c.WebhookURL = "", ""
	default:
		c.WebhookURL = ""
	}
	return c
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	if !h.storeReady(w, r) {
		return
	}
	f, ok := decodeForm(w, r)
	if !ok {
		return
	}
	if !notifytransport.ValidKind(f.Kind) {
		http.Error(w, "Unknown channel kind", 400)
		return
	}
	c := configFrom(f, notifytransport.ChannelConfig{})
	if err := notifytransport.ValidateChannel(f.Kind, c); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := f.Kind + ":" + newChannelSuffix()
	sealed, err := h.registry.sealer.seal(id, c)
	if err != nil {
		http.Error(w, "could not save the channel", 500)
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		http.Error(w, "could not save the channel", 500)
		return
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notification_channels`).Scan(&count); err == nil && count >= maxChannels {
		http.Error(w, "A server can have at most 20 channels", 409)
		return
	}
	if err == nil {
		// New channels get alerts from now on, not the backlog.
		_, err = tx.Exec(ctx, `INSERT INTO notification_destinations(id,activated_at) VALUES($1,clock_timestamp())`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO notification_channels(id,kind,name,sealed) VALUES($1,$2,$3,$4)`, id, f.Kind, f.Name, sealed)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		http.Error(w, "could not save the channel", 500)
		return
	}
	_ = h.registry.Reload(ctx)
	w.WriteHeader(201)
	writeJSON(w, map[string]string{"id": id})
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	if !h.storeReady(w, r) {
		return
	}
	id := r.PathValue("id")
	prev, found := h.registry.config(id)
	var kind string
	if err := h.pool.QueryRow(r.Context(), `SELECT kind FROM notification_channels WHERE id=$1`, id).Scan(&kind); err != nil {
		http.Error(w, "channel not found", 404)
		return
	}
	f, ok := decodeForm(w, r)
	if !ok {
		return
	}
	if f.Kind != "" && f.Kind != kind {
		http.Error(w, "A channel's kind cannot change; add a new channel instead", 400)
		return
	}
	f.Kind = kind
	if !found {
		// Unreadable secrets (key changed): they must be entered again.
		prev = notifytransport.ChannelConfig{}
	}
	c := configFrom(f, prev)
	if err := notifytransport.ValidateChannel(kind, c); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sealed, err := h.registry.sealer.seal(id, c)
	if err == nil {
		_, err = h.pool.Exec(r.Context(), `UPDATE notification_channels SET name=$2,sealed=$3,updated_at=clock_timestamp() WHERE id=$1`, id, f.Name, sealed)
	}
	if err != nil {
		http.Error(w, "could not save the channel", 500)
		return
	}
	_ = h.registry.Reload(r.Context())
	writeJSON(w, map[string]string{"id": id})
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id := r.PathValue("id")
	// Removing the destination also removes its stored channel and deliveries.
	tag, err := h.pool.Exec(r.Context(), `DELETE FROM notification_destinations d USING notification_channels c WHERE c.id=d.id AND d.id=$1`, id)
	if err != nil {
		http.Error(w, "could not remove the channel", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "channel not found", 404)
		return
	}
	if h.registry != nil {
		_ = h.registry.Reload(r.Context())
	}
	w.WriteHeader(204)
}

// checkChannel sends a test with settings that are not saved yet. For an
// existing channel, omitted secrets come from the stored configuration.
func (h *Handler) checkChannel(w http.ResponseWriter, r *http.Request) {
	if !h.storeReady(w, r) {
		return
	}
	var body struct {
		ID string `json:"id"`
		ChannelForm
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "invalid JSON request", 400)
		return
	}
	f := body.ChannelForm
	prev := notifytransport.ChannelConfig{}
	if body.ID != "" {
		var kind string
		if err := h.pool.QueryRow(r.Context(), `SELECT kind FROM notification_channels WHERE id=$1`, body.ID).Scan(&kind); err != nil {
			http.Error(w, "channel not found", 404)
			return
		}
		f.Kind = kind
		prev, _ = h.registry.config(body.ID)
	}
	if !notifytransport.ValidKind(f.Kind) {
		http.Error(w, "Unknown channel kind", 400)
		return
	}
	c := configFrom(f, prev)
	target, err := notifytransport.NewTarget(f.Kind, f.Kind, c)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), SendTimeout)
	defer cancel()
	open, _, _ := policy{public: configuredPublicURL()}.links("", time.Now())
	err = transportSend(target)(ctx, Message{EventID: "test", Transition: "test", RuleName: "Test notification", Severity: "warning",
		Summary: "This is a test from Vigara. Alerts will arrive here.", OccurredAt: time.Now().UTC(), OpenURL: open})
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": errorCode(err)})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func newChannelSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// configView is the edit form's view of a stored channel.
func (h *Handler) configView(id string) *ChannelConfigView {
	if h.registry == nil {
		return nil
	}
	c, ok := h.registry.config(id)
	if !ok {
		return nil
	}
	return &ChannelConfigView{URL: c.URL, Topic: c.Topic, HasToken: c.Token != "" || c.WebhookURL != ""}
}
