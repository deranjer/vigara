package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	publicMu  sync.RWMutex
	publicURL string
)

// Configure sets the address notifications link back to (VIGIRA_PUBLIC_URL).
// Without it, notifications carry no tap target or action buttons.
func Configure(url string) {
	publicMu.Lock()
	publicURL = strings.TrimRight(strings.TrimSpace(url), "/")
	publicMu.Unlock()
}
func configuredPublicURL() string {
	publicMu.RLock()
	defer publicMu.RUnlock()
	return publicURL
}

type verdict string

const (
	send      verdict = ""
	skipMuted verdict = "muted"
	skipRoute verdict = "not_routed"
	holdQuiet verdict = "quiet_hours"
	// Quiet hours made it stale: a held event the alert has moved on from, or the
	// "cleared" for a firing that quiet hours kept back. Dropped, not sent late.
	skipSuperseded verdict = "superseded"
)

// QuietHours hold every notification except a critical alert firing.
type QuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// Route is which alerts one channel receives.
type Route struct {
	Critical bool `json:"critical"`
	Warning  bool `json:"warning"`
	Resolved bool `json:"resolved"`
}

type policy struct {
	route  Route
	quiet  QuietHours
	loc    *time.Location
	key    []byte
	public string
}

func loadPolicy(ctx context.Context, pool *pgxpool.Pool, destination string) (policy, error) {
	p := policy{public: configuredPublicURL()}
	err := pool.QueryRow(ctx, `SELECT d.notify_critical,d.notify_warning,d.notify_resolved,s.quiet_enabled,to_char(s.quiet_start,'HH24:MI'),to_char(s.quiet_end,'HH24:MI'),s.timezone,s.action_key
 FROM notification_destinations d CROSS JOIN notification_settings s WHERE d.id=$1`, destination).Scan(&p.route.Critical, &p.route.Warning, &p.route.Resolved, &p.quiet.Enabled, &p.quiet.Start, &p.quiet.End, &p.quiet.Timezone, &p.key)
	if err != nil {
		return policy{}, err
	}
	if p.loc, err = time.LoadLocation(p.quiet.Timezone); err != nil {
		p.loc = time.UTC
	}
	return p, nil
}

func (p policy) decide(it item, now time.Time) (verdict, time.Time) {
	if it.muted {
		return skipMuted, time.Time{}
	}
	firing := it.transition == "firing"
	critical := it.severity == "critical"
	routed := p.route.Resolved
	if firing {
		routed = p.route.Warning
		if critical {
			routed = p.route.Critical
		}
	}
	if !routed {
		return skipRoute, time.Time{}
	}
	if it.superseded {
		return skipSuperseded, time.Time{}
	}
	if p.quiet.Enabled && !(firing && critical) {
		if until, quiet := quietUntil(now, p.quiet.Start, p.quiet.End, p.loc); quiet {
			return holdQuiet, until
		}
	}
	return send, time.Time{}
}

// quietUntil reports whether now falls in the daily start–end window (which may
// cross midnight) in loc, and when that window ends.
func quietUntil(now time.Time, start, end string, loc *time.Location) (time.Time, bool) {
	s, okS := clockMinutes(start)
	e, okE := clockMinutes(end)
	if !okS || !okE || s == e {
		return time.Time{}, false
	}
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	at := func(day time.Time, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), m/60, m%60, 0, 0, loc)
	}
	if s < e {
		if minute >= s && minute < e {
			return at(midnight, e), true
		}
		return time.Time{}, false
	}
	if minute >= s {
		return at(midnight.AddDate(0, 0, 1), e), true
	}
	if minute < e {
		return at(midnight, e), true
	}
	return time.Time{}, false
}

func clockMinutes(v string) (int, bool) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

const actionLifetime = 7 * 24 * time.Hour

func (p policy) links(incident string, now time.Time) (open, ack, mute string) {
	if p.public == "" {
		return "", "", ""
	}
	open = p.public + "/#/alerts"
	if incident == "" || len(p.key) == 0 {
		return open, "", ""
	}
	exp := now.Add(actionLifetime)
	return open, p.public + "/api/alerts/actions/" + signAction(p.key, incident, "ack", exp), p.public + "/api/alerts/actions/" + signAction(p.key, incident, "mute", exp)
}

// signAction makes an unguessable link that acknowledges or mutes one alert
// without a session, so a notification's buttons work from a locked phone.
func signAction(key []byte, incident, action string, exp time.Time) string {
	payload := incident + "|" + action + "|" + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(mac.Sum(nil))
}

func verifyAction(key []byte, token string, now time.Time) (incident, action string, err error) {
	bad := errors.New("invalid or expired action link")
	if len(token) > 512 {
		return "", "", bad
	}
	encPayload, encMac, ok := strings.Cut(token, ".")
	if !ok {
		return "", "", bad
	}
	enc := base64.RawURLEncoding
	payload, e1 := enc.DecodeString(encPayload)
	sum, e2 := enc.DecodeString(encMac)
	if e1 != nil || e2 != nil {
		return "", "", bad
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(sum, mac.Sum(nil)) {
		return "", "", bad
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 || !uuidText(parts[0]) || (parts[1] != "ack" && parts[1] != "mute") {
		return "", "", bad
	}
	expUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.After(time.Unix(expUnix, 0)) {
		return "", "", bad
	}
	return parts[0], parts[1], nil
}
