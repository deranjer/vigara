package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/apps/api/internal/db"
)

func TestQuietUntil(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 12, h, m, 0, 0, berlin) }
	cases := []struct {
		now        time.Time
		start, end string
		quiet      bool
		until      time.Time
	}{
		{at(23, 30), "23:00", "07:00", true, time.Date(2026, 11, 13, 7, 0, 0, 0, berlin)},
		{at(2, 0), "23:00", "07:00", true, at(7, 0)},
		{at(7, 0), "23:00", "07:00", false, time.Time{}},
		{at(12, 0), "23:00", "07:00", false, time.Time{}},
		{at(13, 0), "12:00", "14:00", true, at(14, 0)},
		{at(13, 0), "12:00", "12:00", false, time.Time{}},
	}
	for _, c := range cases {
		until, quiet := quietUntil(c.now, c.start, c.end, berlin)
		if quiet != c.quiet || !until.Equal(c.until) {
			t.Errorf("quietUntil(%s, %s-%s) = %v %v, want %v %v", c.now.Format("15:04"), c.start, c.end, until, quiet, c.until, c.quiet)
		}
	}
}

func TestDecide(t *testing.T) {
	night := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	p := policy{route: Route{Critical: true, Warning: true, Resolved: false}, quiet: QuietHours{Enabled: true, Start: "23:00", End: "07:00"}, loc: time.UTC}
	check := func(name string, it item, now time.Time, want verdict) {
		t.Helper()
		if got, _ := p.decide(it, now); got != want {
			t.Errorf("%s: %q want %q", name, got, want)
		}
	}
	check("muted alert is never sent", item{transition: "firing", severity: "critical", muted: true}, night, skipMuted)
	check("critical alert notifies during quiet hours", item{transition: "firing", severity: "critical"}, night, send)
	check("warning waits for morning", item{transition: "firing", severity: "warning"}, night, holdQuiet)
	check("warning by day", item{transition: "firing", severity: "warning"}, night.Add(10*time.Hour), send)
	check("resolved not routed", item{transition: "resolved", severity: "warning"}, night.Add(10*time.Hour), skipRoute)
	check("warning that cleared overnight is not sent in the morning", item{transition: "firing", severity: "warning", superseded: true}, night.Add(10*time.Hour), skipSuperseded)
	check("superseded is dropped at once, even in quiet hours", item{transition: "firing", severity: "warning", superseded: true}, night, skipSuperseded)
	_, until := p.decide(item{transition: "firing", severity: "warning"}, night)
	if !until.Equal(time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)) {
		t.Fatal(until)
	}
}

func TestActionLinks(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	incident := "7113dd5d-0000-4000-8000-000000000001"
	now := time.Now()
	p := policy{key: key, public: "https://vigara.example"}
	open, ack, mute := p.links(incident, now)
	if open != "https://vigara.example/#/alerts" || !strings.HasPrefix(ack, "https://vigara.example/api/alerts/actions/") || ack == mute {
		t.Fatal(open, ack, mute)
	}
	token := strings.TrimPrefix(ack, "https://vigara.example/api/alerts/actions/")
	if id, action, err := verifyAction(key, token, now); err != nil || id != incident || action != "ack" {
		t.Fatal(id, action, err)
	}
	if _, _, err := verifyAction(key, token, now.Add(actionLifetime+time.Minute)); err == nil {
		t.Fatal("expired link accepted")
	}
	if _, _, err := verifyAction([]byte("another key another key another"), token, now); err == nil {
		t.Fatal("link signed with another key accepted")
	}
	if _, _, err := verifyAction(key, token[:len(token)-2]+"AA", now); err == nil {
		t.Fatal("tampered link accepted")
	}
	if o, a, m := (policy{key: key}).links(incident, now); o != "" || a != "" || m != "" {
		t.Fatal("links without a public URL")
	}
}

func TestRoutingQuietHoursAndActionEndpoint(t *testing.T) {
	p := notificationTestPool(t)
	ctx := context.Background()
	var device, rule, incident string
	if err := p.QueryRow(ctx, `INSERT INTO devices(display_name) VALUES('Lab gateway') RETURNING id::text`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_rules(name,kind,config,severity) VALUES('Offline check','device_unavailable','{}','warning') RETURNING id::text`).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,$2,'firing','offline',now()) RETURNING id::text`, rule, device).Scan(&incident); err != nil {
		t.Fatal(err)
	}
	Configure("https://vigara.example")
	t.Cleanup(func() { Configure("") })
	var sent []Message
	d := Destination{ID: "ntfy", Kind: "ntfy", Target: "https://ntfy.example · topic t", Send: func(_ context.Context, m Message) error { sent = append(sent, m); return nil }}
	if err := Initialize(ctx, p, []Destination{d}); err != nil {
		t.Fatal(err)
	}

	// Quiet all day: the warning is held, not sent, and not counted as an attempt.
	if _, err := p.Exec(ctx, `UPDATE notification_settings SET quiet_enabled=true,quiet_start='00:00',quiet_end='23:59',timezone='UTC'`); err != nil {
		t.Fatal(err)
	}
	first := insertEvent(t, p, incident, "firing", time.Now().UTC().Add(time.Second))
	if _, err := RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatal("warning sent during quiet hours")
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='pending' AND last_error_code='quiet_hours' AND attempts=0 AND next_attempt_at>now()`, 1, first)

	// Saving settings with quiet hours off releases it, with signed buttons.
	service := auth.NewService(p, db.New(p))
	session, err := service.CreateFirstUser(ctx, "notify-admin", "Admin", "correct horse battery staple", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h := NewHandler(p, service, []Destination{d})
	h.Register(mux)
	h.RegisterActions(mux)
	request := func(method, path, body string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if signedIn {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Token})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := request(http.MethodPut, "/api/notifications/settings", `{"quietHours":{"enabled":false,"start":"23:00","end":"07:00","timezone":"Not/AZone"},"routes":{}}`, true); w.Code != 400 {
		t.Fatalf("bad timezone status=%d", w.Code)
	}
	if w := request(http.MethodPut, "/api/notifications/settings", `{"quietHours":{"enabled":false,"start":"23:00","end":"07:00","timezone":"UTC"},"routes":{"ntfy":{"critical":true,"warning":true,"resolved":false}}}`, true); w.Code != 200 {
		t.Fatalf("settings status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Severity != "warning" || sent[0].OpenURL != "https://vigara.example/#/alerts" || sent[0].AcknowledgeURL == "" {
		t.Fatalf("sent=%+v", sent)
	}

	// Resolved is not routed to this channel: skipped, and later events are not blocked.
	resolved := insertEvent(t, p, incident, "resolved", time.Now().UTC().Add(2*time.Second))
	if _, err = RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='skipped' AND last_error_code='not_routed'`, 1, resolved)

	// The Acknowledge button works without a session; a forged link does not.
	ack := strings.TrimPrefix(sent[0].AcknowledgeURL, "https://vigara.example")
	if w := request(http.MethodPost, ack+"x", "", false); w.Code != 403 {
		t.Fatalf("forged action status=%d", w.Code)
	}
	if w := request(http.MethodPost, ack, "", false); w.Code != 200 {
		t.Fatalf("action status=%d body=%s", w.Code, w.Body.String())
	}
	assertInt(t, p, `SELECT count(*) FROM alert_incidents WHERE id=$1 AND acknowledged_at IS NOT NULL`, 1, incident)
	mute := strings.TrimPrefix(sent[0].MuteURL, "https://vigara.example")
	if w := request(http.MethodPost, mute, "", false); w.Code != 200 {
		t.Fatalf("mute status=%d", w.Code)
	}

	// Muted: the next firing is skipped.
	again := insertEvent(t, p, incident, "firing", time.Now().UTC().Add(3*time.Second))
	if _, err = RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='skipped' AND last_error_code='muted'`, 1, again)
	if len(sent) != 1 {
		t.Fatalf("muted alert sent: %d", len(sent))
	}

	w := request(http.MethodGet, "/api/notifications", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"target":"https://ntfy.example · topic t"`) || !strings.Contains(w.Body.String(), `"resolved":false`) || !strings.Contains(w.Body.String(), `"linksEnabled":true`) {
		t.Fatalf("settings=%s", w.Body.String())
	}
	if w = request(http.MethodPost, "/api/notifications/channels/ntfy/test", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || sent[len(sent)-1].Transition != "test" {
		t.Fatalf("test status=%d body=%s", w.Code, w.Body.String())
	}

	// A warning held overnight that cleared before morning is dropped, not sent late.
	var other string
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,'other','firing','offline',now()) RETURNING id::text`, rule).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE notification_settings SET quiet_enabled=true,quiet_start='00:00',quiet_end='23:59'`); err != nil {
		t.Fatal(err)
	}
	held := insertEvent(t, p, other, "firing", time.Now().UTC().Add(4*time.Second))
	if _, err = RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE notification_destinations SET notify_resolved=true WHERE id='ntfy'`); err != nil {
		t.Fatal(err)
	}
	cleared := insertEvent(t, p, other, "resolved", time.Now().UTC().Add(5*time.Second))
	before := len(sent)
	if _, err = RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	if w = request(http.MethodPut, "/api/notifications/settings", `{"quietHours":{"enabled":false,"start":"23:00","end":"07:00","timezone":"UTC"},"routes":{}}`, true); w.Code != 200 {
		t.Fatalf("release status=%d", w.Code)
	}
	for range 2 {
		if _, err = RunOneBatch(ctx, p, d); err != nil {
			t.Fatal(err)
		}
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id IN ($1,$2) AND status='skipped' AND last_error_code='superseded'`, 2, held, cleared)
	if len(sent) != before {
		t.Fatalf("stale alert sent: %+v", sent[before:])
	}

	// A critical alert that clears at night and refires is sent at once, not kept
	// behind the held "cleared"; that "cleared" is then dropped.
	var critRule, crit string
	if err := p.QueryRow(ctx, `INSERT INTO alert_rules(name,kind,config,severity) VALUES('Down','device_unavailable','{}','critical') RETURNING id::text`).Scan(&critRule); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO alert_incidents(rule_id,dimension,status,summary,started_at) VALUES($1,'crit','firing','down',now()) RETURNING id::text`, critRule).Scan(&crit); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE notification_settings SET quiet_enabled=true,quiet_start='00:00',quiet_end='23:59'`); err != nil {
		t.Fatal(err)
	}
	insertEvent(t, p, crit, "firing", time.Now().UTC().Add(6*time.Second))
	nightClear := insertEvent(t, p, crit, "resolved", time.Now().UTC().Add(7*time.Second))
	insertEvent(t, p, crit, "firing", time.Now().UTC().Add(8*time.Second))
	for range 3 {
		if _, err = RunOneBatch(ctx, p, d); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != before+2 || sent[before+1].Transition != "firing" || sent[before+1].Severity != "critical" {
		t.Fatalf("critical refire not sent: %+v", sent[before:])
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='pending' AND last_error_code='quiet_hours'`, 1, nightClear)
	if w = request(http.MethodPut, "/api/notifications/settings", `{"quietHours":{"enabled":false,"start":"23:00","end":"07:00","timezone":"UTC"},"routes":{}}`, true); w.Code != 200 {
		t.Fatalf("release status=%d", w.Code)
	}
	if _, err = RunOneBatch(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	assertInt(t, p, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1 AND status='skipped' AND last_error_code='superseded'`, 1, nightClear)
	if len(sent) != before+2 {
		t.Fatalf("stale cleared sent: %+v", sent[before+2:])
	}
}
