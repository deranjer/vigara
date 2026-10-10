package inventoryview

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
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
	mux.HandleFunc("GET /api/inventory/devices", h.devices)
	mux.HandleFunc("GET /api/inventory/devices/{deviceID}/history", h.history)
}

func (h *Handler) authorized(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "https" && u.Scheme != "http") {
			http.Error(w, "request origin rejected", 403)
			return false
		}
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		http.Error(w, "login required", 401)
		return false
	}
	if _, err = h.auth.Authenticate(r.Context(), cookie.Value); err != nil {
		http.Error(w, "login required", 401)
		return false
	}
	return true
}

type Device struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	PlatformID          *string         `json:"platformId"`
	CreatedAt           time.Time       `json:"createdAt"`
	LastSeenAt          *time.Time      `json:"lastSeenAt"`
	Status              string          `json:"status"`
	AgentID             *string         `json:"agentId"`
	Revoked             bool            `json:"revoked"`
	Platform            string          `json:"platform"`
	Architecture        string          `json:"architecture"`
	AgentVersion        string          `json:"agentVersion"`
	Capabilities        []string        `json:"capabilities"`
	Latest              json.RawMessage `json:"latest"`
	TelemetryObservedAt *time.Time      `json:"telemetryObservedAt"`
	CollectionErrors    json.RawMessage `json:"collectionErrors"`
}

type DevicePage struct {
	Devices []Device `json:"devices"`
	Total   int64    `json:"total"`
	Offset  int      `json:"offset"`
	Limit   int      `json:"limit"`
}

func (h *Handler) devices(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	limit, offset := 100, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			http.Error(w, "limit must be between 1 and 100", 400)
			return
		}
		limit = n
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 1000000 {
			http.Error(w, "invalid offset", 400)
			return
		}
		offset = n
	}
	var total int64
	if err := h.pool.QueryRow(r.Context(), `SELECT count(*) FROM devices`).Scan(&total); err != nil {
		http.Error(w, "could not load devices", 500)
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT d.id::text,d.display_name,d.platform_id,d.created_at,d.last_seen_at,
		i.id::text,i.revoked_at IS NOT NULL,COALESCE(i.platform,''),COALESCE(i.architecture,''),COALESCE(i.agent_version,''),COALESCE(i.capabilities,'[]'::jsonb),l.payload,l.observed_at
		FROM devices d LEFT JOIN LATERAL (SELECT * FROM agent_identities ai WHERE ai.device_id=d.id ORDER BY (ai.revoked_at IS NULL) DESC,ai.created_at DESC LIMIT 1) i ON true
		LEFT JOIN agent_telemetry_latest l ON l.agent_id=i.id ORDER BY d.display_name,d.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		http.Error(w, "could not load devices", 500)
		return
	}
	defer rows.Close()
	result := make([]Device, 0)
	for rows.Next() {
		var d Device
		var caps, payload []byte
		var obs *time.Time
		if err = rows.Scan(&d.ID, &d.Name, &d.PlatformID, &d.CreatedAt, &d.LastSeenAt, &d.AgentID, &d.Revoked, &d.Platform, &d.Architecture, &d.AgentVersion, &caps, &payload, &obs); err != nil {
			http.Error(w, "could not load devices", 500)
			return
		}
		_ = json.Unmarshal(caps, &d.Capabilities)
		if d.Capabilities == nil {
			d.Capabilities = []string{}
		}
		if payload != nil {
			d.Latest = payload
			d.TelemetryObservedAt = obs
			var snapshot struct {
				Errors json.RawMessage `json:"errors"`
			}
			_ = json.Unmarshal(payload, &snapshot)
			d.CollectionErrors = snapshot.Errors
		}
		d.Status = "unsupported"
		if d.AgentID != nil {
			d.Status = "healthy"
			if d.Revoked {
				d.Status = "failed"
			} else if d.LastSeenAt == nil || time.Since(*d.LastSeenAt) > 5*time.Minute {
				d.Status = "stale"
			}
		}
		freshTelemetry := d.TelemetryObservedAt != nil && !d.TelemetryObservedAt.After(time.Now()) && time.Since(*d.TelemetryObservedAt) <= 5*time.Minute
		if len(d.CollectionErrors) > 0 && string(d.CollectionErrors) != "null" && string(d.CollectionErrors) != "[]" && freshTelemetry {
			d.Status = "failed"
		}
		if !d.Revoked && d.AgentID != nil && (!freshTelemetry || d.LastSeenAt == nil || time.Since(*d.LastSeenAt) > 5*time.Minute) {
			d.Status = "stale"
		}
		result = append(result, d)
	}
	if err = rows.Err(); err != nil {
		http.Error(w, "could not load devices", 500)
		return
	}
	writeJSON(w, DevicePage{Devices: result, Total: total, Offset: offset, Limit: limit})
}

type Point struct {
	At      time.Time `json:"at"`
	Value   *float64  `json:"value"`
	Min     *float64  `json:"min,omitempty"`
	Max     *float64  `json:"max,omitempty"`
	Samples int64     `json:"samples"`
}
type Series struct {
	Name   string          `json:"name"`
	Unit   string          `json:"unit"`
	Labels json.RawMessage `json:"labels"`
	Points []Point         `json:"points"`
}
type History struct {
	Range       string    `json:"range"`
	WindowStart time.Time `json:"windowStart"`
	WindowEnd   time.Time `json:"windowEnd"`
	Metrics     []string  `json:"metrics"`
	Series      []Series  `json:"series"`
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	rangeName := r.URL.Query().Get("range")
	ranges := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	duration, ok := ranges[rangeName]
	if !ok {
		http.Error(w, "range must be 1h, 6h, 24h, 7d, or 30d", 400)
		return
	}
	deviceID := r.PathValue("deviceID")
	if !uuidRE.MatchString(deviceID) {
		http.Error(w, "invalid device ID", 400)
		return
	}
	var exists bool
	if err := h.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM devices WHERE id=$1)`, deviceID).Scan(&exists); err != nil {
		http.Error(w, "could not load history", 500)
		return
	}
	if !exists {
		http.Error(w, "device not found", 404)
		return
	}
	metric := r.URL.Query().Get("metric")
	if len(metric) > 80 || (metric != "" && !metricRE.MatchString(metric)) {
		http.Error(w, "invalid metric name", 400)
		return
	}
	metricRows, err := h.pool.Query(r.Context(), `SELECT DISTINCT metric_name FROM telemetry_metric_series s JOIN agent_identities i ON i.id=s.agent_id WHERE i.device_id=$1 AND i.revoked_at IS NULL UNION SELECT DISTINCT metric_name FROM telemetry_metric_rollups r JOIN agent_identities i ON i.id=r.agent_id WHERE i.device_id=$1 AND i.revoked_at IS NULL UNION SELECT DISTINCT m->>'name' FROM agent_telemetry_latest t JOIN agent_identities i ON i.id=t.agent_id CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.payload->'metrics','[]'::jsonb)) m WHERE i.device_id=$1 AND i.revoked_at IS NULL ORDER BY 1 LIMIT 129`, deviceID)
	if err != nil {
		http.Error(w, "could not load history", 500)
		return
	}
	metrics := []string{}
	for metricRows.Next() {
		var name string
		if err = metricRows.Scan(&name); err != nil {
			metricRows.Close()
			http.Error(w, "could not load history", 500)
			return
		}
		metrics = append(metrics, name)
	}
	metricRows.Close()
	if err = metricRows.Err(); err != nil {
		http.Error(w, "could not load history", 500)
		return
	}
	if len(metrics) > 128 {
		http.Error(w, "device has too many distinct metrics to list", 422)
		return
	}
	if metric == "" {
		now := time.Now().UTC()
		writeJSON(w, History{Range: rangeName, WindowStart: now.Add(-duration), WindowEnd: now, Metrics: metrics, Series: []Series{}})
		return
	}
	allowed := false
	for _, name := range metrics {
		if name == metric {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "metric not available for device", 404)
		return
	}
	binSeconds := int64(60)
	if duration > 24*time.Hour {
		binSeconds = 3600
	}
	windowEnd := time.Now().UTC()
	windowStart := windowEnd.Add(-duration)
	rows, err := h.pool.Query(r.Context(), `WITH device_agents AS (SELECT id FROM agent_identities WHERE device_id=$1 AND revoked_at IS NULL), raw AS (
		SELECT t.observed_at AS at,m->>'name' name,COALESCE(m->>'unit',CASE m->>'name' WHEN 'cpu_used_percent' THEN '%' WHEN 'memory_available_bytes' THEN 'By' WHEN 'memory_total_bytes' THEN 'By' WHEN 'memory_used_percent' THEN '%' WHEN 'uptime_seconds' THEN 's' WHEN 'filesystem_total_bytes' THEN 'By' WHEN 'filesystem_free_bytes' THEN 'By' WHEN 'network_receive_bytes_per_second' THEN 'By/s' WHEN 'network_send_bytes_per_second' THEN 'By/s' WHEN 'disk_read_bytes_per_second' THEN 'By/s' WHEN 'disk_write_bytes_per_second' THEN 'By/s' ELSE '' END) unit,COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb) labels,(m->>'value')::double precision value,NULL::double precision min,NULL::double precision max,1::bigint samples
	 FROM agent_telemetry t JOIN device_agents a ON a.id=t.agent_id CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.payload->'metrics','[]'::jsonb)) m WHERE t.observed_at>=now()-($2 * interval '1 second') AND t.observed_at<=now() AND m->>'name'=$4
		UNION ALL SELECT r.bucket_start,r.metric_name,CASE r.metric_name WHEN 'cpu_used_percent' THEN '%' WHEN 'memory_available_bytes' THEN 'By' WHEN 'memory_total_bytes' THEN 'By' WHEN 'memory_used_percent' THEN '%' WHEN 'uptime_seconds' THEN 's' WHEN 'filesystem_total_bytes' THEN 'By' WHEN 'filesystem_free_bytes' THEN 'By' WHEN 'network_receive_bytes_per_second' THEN 'By/s' WHEN 'network_send_bytes_per_second' THEN 'By/s' WHEN 'disk_read_bytes_per_second' THEN 'By/s' WHEN 'disk_write_bytes_per_second' THEN 'By/s' ELSE '' END,r.labels,r.sum_value/r.sample_count,r.min_value,r.max_value,r.sample_count FROM telemetry_metric_rollups r JOIN device_agents a ON a.id=r.agent_id WHERE r.bucket_start>=now()-($2 * interval '1 second') AND r.bucket_start<=now() AND r.metric_name=$4
	), binned AS (SELECT date_bin(($3 * interval '1 second'),raw.at,timestamptz '2000-01-01') at,name,unit,labels,sum(value*samples)/sum(samples) value,min(COALESCE(min,value)) min,max(COALESCE(max,value)) max,sum(samples)::bigint samples FROM raw GROUP BY 1,2,3,4)
	SELECT at,name,unit,labels,value,min,max,samples FROM binned ORDER BY name,labels,at LIMIT 20001`, deviceID, int64(duration.Seconds()), binSeconds, metric)
	if err != nil {
		http.Error(w, "could not load history", 500)
		return
	}
	defer rows.Close()
	seriesMap := map[string]*Series{}
	ordered := []*Series{}
	pointCount := 0
	for rows.Next() {
		pointCount++
		if pointCount > 20000 {
			http.Error(w, "metric history exceeds the response limit; choose a shorter range", 422)
			return
		}
		var at time.Time
		var name, unit string
		var labels []byte
		var value float64
		var min, max *float64
		var samples int64
		if err = rows.Scan(&at, &name, &unit, &labels, &value, &min, &max, &samples); err != nil {
			http.Error(w, "could not load history", 500)
			return
		}
		key := name + "\x00" + string(labels)
		s := seriesMap[key]
		if s == nil {
			s = &Series{Name: name, Unit: unit, Labels: labels, Points: []Point{}}
			seriesMap[key] = s
			ordered = append(ordered, s)
		}
		v := value
		s.Points = append(s.Points, Point{At: at, Value: &v, Min: min, Max: max, Samples: samples})
	}
	if err = rows.Err(); err != nil {
		http.Error(w, "could not load history", 500)
		return
	}
	out := make([]Series, len(ordered))
	for i, s := range ordered {
		out[i] = *s
	}
	writeJSON(w, History{Range: rangeName, WindowStart: windowStart, WindowEnd: windowEnd, Metrics: metrics, Series: out})
}

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var metricRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
