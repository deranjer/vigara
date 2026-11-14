package containers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

type logValueRule struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agentId"`
	Kind       string     `json:"kind"`
	Resource   string     `json:"resource"`
	Service    string     `json:"service,omitempty"`
	Label      string     `json:"label"`
	Match      string     `json:"match"`
	Prefix     string     `json:"prefix"`
	Suffix     string     `json:"suffix"`
	Pattern    string     `json:"pattern"`
	Shared     bool       `json:"shared"`
	Value      string     `json:"value"`
	RunID      string     `json:"-"`
	Cursor     string     `json:"-"`
	CapturedAt string     `json:"capturedAt,omitempty"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	State      string     `json:"state"`
	Version    int64      `json:"version"`
}

const logValueColumns = `id::text,agent_id::text,kind,resource,service,label,match_text,prefix,suffix,pattern,shared,value,run_id,cursor_at,captured_at,checked_at,state,version`

func scanLogValue(row interface{ Scan(...any) error }) (logValueRule, error) {
	var v logValueRule
	err := row.Scan(&v.ID, &v.AgentID, &v.Kind, &v.Resource, &v.Service, &v.Label, &v.Match, &v.Prefix, &v.Suffix, &v.Pattern, &v.Shared, &v.Value, &v.RunID, &v.Cursor, &v.CapturedAt, &v.CheckedAt, &v.State, &v.Version)
	return v, err
}
func validateLogValue(v logValueRule) error {
	if len(strings.TrimSpace(v.Label)) < 1 || len(v.Label) > 80 || len(v.Match) < 1 || len(v.Match) > 256 || len(v.Prefix) > 256 || len(v.Suffix) > 256 || len(v.Pattern) > 512 {
		return errors.New("label, match text, or extraction is too long or empty")
	}
	if v.Pattern != "" {
		r, err := regexp.Compile(v.Pattern)
		if err != nil || r.NumSubexp() != 1 {
			return errors.New("regular expression must contain exactly one capture group")
		}
		if v.Prefix != "" || v.Suffix != "" {
			return errors.New("choose text delimiters or a regular expression")
		}
	}
	if v.Pattern == "" && (v.Prefix == "" || v.Suffix == "") {
		return errors.New("enter both extraction delimiters")
	}
	return nil
}
func extractLogValue(v logValueRule, line protocol.DockerLogLine) string {
	if line.X || !strings.Contains(line.M, v.Match) {
		return ""
	}
	value := ""
	if v.Pattern != "" {
		r, err := regexp.Compile(v.Pattern)
		if err != nil {
			return ""
		}
		m := r.FindStringSubmatch(line.M)
		if len(m) == 2 {
			value = m[1]
		}
	} else {
		_, after, ok := strings.Cut(line.M, v.Prefix)
		if !ok {
			return ""
		}
		before, _, ok := strings.Cut(after, v.Suffix)
		if ok {
			value = before
		}
	}
	value = strings.TrimSpace(value)
	if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func (a *api) registerLogFeatures(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/containers/{agent}/{id}/logs/search", a.searchLogs)
	mux.HandleFunc("GET /api/containers/{agent}/{id}/values", a.logValues)
	mux.HandleFunc("POST /api/containers/{agent}/{id}/values/preview", a.previewLogValue)
	mux.HandleFunc("POST /api/containers/{agent}/{id}/values", a.saveLogValue)
	mux.HandleFunc("PUT /api/containers/{agent}/{id}/values/{value}", a.saveLogValue)
	mux.HandleFunc("DELETE /api/containers/{agent}/{id}/values/{value}", a.deleteLogValue)
}
func (a *api) searchLogs(w http.ResponseWriter, r *http.Request) {
	user, ok := a.Authz.Authorize(w, r, auth.PermContainersRead)
	if !ok {
		return
	}
	agent, id, ok := target(w, r)
	if !ok {
		return
	}
	if !a.requireResource(w, r, user, agent, id, "logs") {
		return
	}
	q := r.URL.Query()
	p := protocol.DockerLogSearchParams{ID: id, Match: q.Get("match"), Since: q.Get("since"), Until: q.Get("until")}
	if len(p.Match) < 1 || len(p.Match) > 256 {
		http.Error(w, "enter match text (up to 256 characters)", 400)
		return
	}
	for _, stamp := range []string{p.Since, p.Until} {
		if stamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				http.Error(w, "invalid search time", 400)
				return
			}
		}
	}
	if p.Since != "" && p.Until != "" {
		since, _ := time.Parse(time.RFC3339Nano, p.Since)
		until, _ := time.Parse(time.RFC3339Nano, p.Until)
		if !since.Before(until) {
			http.Error(w, "From must be earlier than Until", 400)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var result protocol.DockerLogSearchResult
	if err := a.Hub.Call(ctx, agent, protocol.MethodDockerLogSearch, p, &result); err != nil {
		logSearchError(w, err)
		return
	}
	respond(w, 200, result)
}
func logSearchError(w http.ResponseWriter, err error) {
	var ch *protocol.ChannelError
	if errors.As(err, &ch) && ch.Code == protocol.ErrCodeUnknownMethod {
		http.Error(w, "Update the agent on this host to enable retained log search and saved values.", 409)
		return
	}
	_, message := describe(err)
	http.Error(w, message, statusFor(err))
}

func ruleForMeta(v logValueRule, meta protocol.DockerInspectResult) bool {
	if v.Kind == "container" {
		return v.Resource == meta.ID
	}
	return v.Kind == "service" && v.Resource == meta.Labels["com.docker.compose.project"] && v.Service == meta.Labels["com.docker.compose.service"]
}
func (a *api) logValues(w http.ResponseWriter, r *http.Request) {
	user, ok := a.Authz.Authorize(w, r, auth.PermContainersRead)
	if !ok {
		return
	}
	agent, id, ok := target(w, r)
	if !ok {
		return
	}
	if !a.requireResource(w, r, user, agent, id, "view") {
		return
	}
	if a.Pool == nil {
		respond(w, 200, []any{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
	defer cancel()
	var meta protocol.DockerInspectResult
	err := a.Hub.Call(ctx, agent, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: id}, &meta)
	if err != nil {
		_, message := describe(err)
		http.Error(w, message, statusFor(err))
		return
	}
	rows, err := a.Pool.Query(ctx, `SELECT `+logValueColumns+` FROM container_log_values WHERE agent_id=$1::uuid ORDER BY label,id`, agent)
	if err != nil {
		http.Error(w, "could not read saved values", 500)
		return
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		v, err := scanLogValue(rows)
		if err != nil {
			http.Error(w, "could not read saved values", 500)
			return
		}
		if !ruleForMeta(v, meta) {
			continue
		}
		if user.Role != auth.RoleAdmin && !v.Shared {
			continue
		}
		// Validate the current run on every read; worker lag must not expose an old code.
		if v.RunID != containerRun(meta) || !meta.State.Running {
			v.State = "waiting"
			v.Value = ""
		}
		if v.CheckedAt == nil || time.Since(*v.CheckedAt) > 45*time.Second {
			v.State = "stale"
			v.Value = ""
		}
		if v.State != "current" {
			v.Value = ""
		}
		if user.Role == auth.RoleAdmin {
			result = append(result, v)
		} else {
			result = append(result, map[string]any{"id": v.ID, "label": v.Label, "value": v.Value, "state": v.State, "capturedAt": v.CapturedAt, "checkedAt": v.CheckedAt})
		}
	}
	if rows.Err() != nil {
		http.Error(w, "could not read saved values", 500)
		return
	}
	respond(w, 200, result)
}

func (a *api) previewLogValue(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Authz.Authorize(w, r, auth.PermUsersManage); !ok {
		return
	}
	agent, id, ok := target(w, r)
	if !ok {
		return
	}
	var v logValueRule
	if !decodeBody(w, r, &v) {
		return
	}
	if err := validateLogValue(v); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var meta protocol.DockerInspectResult
	if err := a.Hub.Call(ctx, agent, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: id}, &meta); err != nil {
		logSearchError(w, err)
		return
	}
	var logs protocol.DockerLogSearchResult
	if err := a.Hub.Call(ctx, agent, protocol.MethodDockerLogSearch, protocol.DockerLogSearchParams{ID: meta.ID, Match: v.Match, Since: meta.State.StartedAt}, &logs); err != nil {
		logSearchError(w, err)
		return
	}
	var line protocol.DockerLogLine
	value := ""
	for _, candidate := range logs.Lines {
		if parsed := extractLogValue(v, candidate); parsed != "" && (line.T == "" || timestampAfter(candidate.T, line.T)) {
			value = parsed
			line = candidate
		}
	}
	respond(w, 200, map[string]any{"value": value, "line": line, "truncated": logs.Truncated, "moreMatches": logs.MoreMatches})
}
func (a *api) saveLogValue(w http.ResponseWriter, r *http.Request) {
	user, ok := a.Authz.Authorize(w, r, auth.PermUsersManage)
	if !ok {
		return
	}
	agent, id, ok := target(w, r)
	if !ok {
		return
	}
	if a.Pool == nil {
		http.Error(w, "saved values are unavailable", 503)
		return
	}
	var v logValueRule
	if !decodeBody(w, r, &v) {
		return
	}
	if err := validateLogValue(v); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inspectTimeout)
	defer cancel()
	var meta protocol.DockerInspectResult
	if err := a.Hub.Call(ctx, agent, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: id}, &meta); err != nil {
		logSearchError(w, err)
		return
	}
	kind, resource, service := "container", meta.ID, ""
	if project, svc := meta.Labels["com.docker.compose.project"], meta.Labels["com.docker.compose.service"]; project != "" && svc != "" {
		kind, resource, service = "service", project, svc
	}
	var savedID string
	var err error
	if r.Method == http.MethodPost {
		// Serialize creation so the worker's total rule count remains bounded.
		tx, e := a.Pool.Begin(ctx)
		if e != nil {
			http.Error(w, "could not save log value", 500)
			return
		}
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(8493021)`)
		if err == nil {
			var count int
			err = tx.QueryRow(ctx, `SELECT count(*) FROM container_log_values`).Scan(&count)
			if count >= 100 {
				http.Error(w, "at most 100 saved log values are supported", 400)
				return
			}
		}
		if err == nil {
			err = tx.QueryRow(ctx, `INSERT INTO container_log_values(agent_id,kind,resource,service,label,match_text,prefix,suffix,pattern,shared) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`, agent, kind, resource, service, v.Label, v.Match, v.Prefix, v.Suffix, v.Pattern, v.Shared).Scan(&savedID)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
	} else {
		if !uuidPattern.MatchString(r.PathValue("value")) {
			http.Error(w, "invalid saved value", 400)
			return
		}
		err = a.Pool.QueryRow(ctx, `UPDATE container_log_values SET label=$6,match_text=$7,prefix=$8,suffix=$9,pattern=$10,shared=$11,
 value=CASE WHEN (match_text,prefix,suffix,pattern) IS DISTINCT FROM ($7,$8,$9,$10) THEN '' ELSE value END,
 run_id=CASE WHEN (match_text,prefix,suffix,pattern) IS DISTINCT FROM ($7,$8,$9,$10) THEN '' ELSE run_id END,
 cursor_at=CASE WHEN (match_text,prefix,suffix,pattern) IS DISTINCT FROM ($7,$8,$9,$10) THEN '' ELSE cursor_at END,
 captured_at=CASE WHEN (match_text,prefix,suffix,pattern) IS DISTINCT FROM ($7,$8,$9,$10) THEN '' ELSE captured_at END,
 state=CASE WHEN (match_text,prefix,suffix,pattern) IS DISTINCT FROM ($7,$8,$9,$10) THEN 'waiting' ELSE state END,
 version=version+1 WHERE id=$1::uuid AND agent_id=$2::uuid AND kind=$3 AND resource=$4 AND service=$5 AND version=$12 RETURNING id::text`, r.PathValue("value"), agent, kind, resource, service, v.Label, v.Match, v.Prefix, v.Suffix, v.Pattern, v.Shared, v.Version).Scan(&savedID)
	}
	if err != nil {
		http.Error(w, "could not save log value; refresh and check for a duplicate label", 409)
		return
	}
	a.record(r.Context(), audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "container.log_value.save", TargetKind: "log_value", TargetID: savedID, AgentID: agent, Outcome: audit.OutcomeSuccess})
	respond(w, 200, map[string]string{"id": savedID})
}
func (a *api) deleteLogValue(w http.ResponseWriter, r *http.Request) {
	user, ok := a.Authz.Authorize(w, r, auth.PermUsersManage)
	if !ok {
		return
	}
	agent, _, ok := target(w, r)
	if !ok {
		return
	}
	if a.Pool == nil {
		http.Error(w, "saved values are unavailable", 503)
		return
	}
	valueID := r.PathValue("value")
	if !uuidPattern.MatchString(valueID) {
		http.Error(w, "invalid saved value", 400)
		return
	}
	tag, err := a.Pool.Exec(r.Context(), `DELETE FROM container_log_values WHERE id=$1::uuid AND agent_id=$2::uuid`, valueID, agent)
	if err != nil {
		http.Error(w, "could not delete saved value", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "saved value not found", 404)
		return
	}
	a.record(r.Context(), audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "container.log_value.delete", TargetKind: "log_value", TargetID: valueID, AgentID: agent, Outcome: audit.OutcomeSuccess})
	w.WriteHeader(204)
}

func containerRun(meta protocol.DockerInspectResult) string {
	return meta.ID + "/" + meta.State.StartedAt
}
func timestampAfter(a, b string) bool {
	ta, ea := time.Parse(time.RFC3339Nano, a)
	tb, eb := time.Parse(time.RFC3339Nano, b)
	return ea == nil && (eb != nil || ta.After(tb))
}

// RunLogValues captures values without an open browser and resumes from a
// persisted timestamp after API restarts. No raw log lines are persisted.
func RunLogValues(ctx context.Context, cfg Config) {
	if cfg.Pool == nil {
		return
	}
	a := &api{Config: cfg}
	for ctx.Err() == nil {
		rows, err := cfg.Pool.Query(ctx, `SELECT id::text FROM container_log_values ORDER BY checked_at NULLS FIRST,id LIMIT 100`)
		ids := []string{}
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
		}
		// Four bounded readers keep a noisy/offline host from starving every rule.
		sem := make(chan struct{}, 4)
		done := make(chan struct{}, len(ids))
		for _, id := range ids {
			if ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			go func(id string) { defer func() { <-sem; done <- struct{}{} }(); a.captureLogValue(ctx, id) }(id)
		}
		for i := 0; i < len(ids) && ctx.Err() == nil; i++ {
			select {
			case <-done:
			case <-ctx.Done():
				return
			}
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (a *api) captureLogValue(parent context.Context, id string) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	v, err := scanLogValue(a.Pool.QueryRow(ctx, `SELECT `+logValueColumns+` FROM container_log_values WHERE id=$1::uuid`, id))
	if err != nil {
		return
	}
	// Do not hold a database connection while waiting for an offline/noisy host.
	// Compare the original capture state as well as the editor version: concurrent
	// workers, edits, or deletes must never be overwritten by a late scan result.
	expectedRun, expectedCursor, expectedCheck := v.RunID, v.Cursor, v.CheckedAt
	persist := func(state string) {
		_, _ = a.Pool.Exec(ctx, `UPDATE container_log_values SET value=$2,run_id=$3,cursor_at=$4,captured_at=$5,state=$6,checked_at=clock_timestamp()
 WHERE id=$1::uuid AND version=$7 AND run_id=$8 AND cursor_at=$9 AND checked_at IS NOT DISTINCT FROM $10::timestamptz`, id, v.Value, v.RunID, v.Cursor, v.CapturedAt, state, v.Version, expectedRun, expectedCursor, expectedCheck)
	}
	var list protocol.DockerListResult
	if a.Hub.Call(ctx, v.AgentID, protocol.MethodDockerList, nil, &list) != nil {
		persist("offline")
		return
	}
	var found *protocol.DockerContainerInfo
	for i := range list.Containers {
		c := &list.Containers[i]
		match := v.Kind == "container" && v.Resource == c.ID || v.Kind == "service" && v.Resource == c.Project && v.Service == c.Service
		if match {
			if found != nil {
				persist("ambiguous")
				return
			}
			found = c
		}
	}
	if found == nil {
		persist("waiting")
		return
	}
	var meta protocol.DockerInspectResult
	if a.Hub.Call(ctx, v.AgentID, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: found.ID}, &meta) != nil {
		persist("offline")
		return
	}
	if !meta.State.Running {
		v.Value = ""
		v.CapturedAt = ""
		persist("waiting")
		return
	}
	run := containerRun(meta)
	if v.RunID != run {
		v.Value = ""
		v.CapturedAt = ""
		v.Cursor = meta.State.StartedAt
		v.RunID = run
	}
	var logs protocol.DockerLogSearchResult
	if err := a.Hub.Call(ctx, v.AgentID, protocol.MethodDockerLogSearch, protocol.DockerLogSearchParams{ID: meta.ID, Match: v.Match, Since: v.Cursor}, &logs); err != nil {
		state := "offline"
		var ce *protocol.ChannelError
		if errors.As(err, &ce) && ce.Code == protocol.ErrCodeUnknownMethod {
			state = "agent-update-required"
		}
		persist(state)
		return
	}
	for _, line := range logs.Lines {
		if value := extractLogValue(v, line); value != "" && (v.CapturedAt == "" || timestampAfter(line.T, v.CapturedAt)) {
			v.Value = value
			v.CapturedAt = line.T
		}
	}
	previousCursor := v.Cursor
	if logs.NextSince != "" && timestampAfter(logs.NextSince, v.Cursor) {
		v.Cursor = logs.NextSince
	}
	// Reinspect to avoid publishing a value if a restart happened while scanning.
	var current protocol.DockerInspectResult
	if a.Hub.Call(ctx, v.AgentID, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: meta.ID}, &current) != nil {
		persist("offline")
		return
	}
	if containerRun(current) != run || !current.State.Running {
		v.Value = ""
		v.CapturedAt = ""
		v.Cursor = ""
		v.RunID = ""
		persist("waiting")
		return
	}
	state := "waiting"
	if v.Value != "" {
		state = "current"
	}
	if logs.Truncated {
		state = "scanning"
		if previousCursor == v.Cursor {
			state = "scan-limit"
		}
	}
	persist(state)
}
