package inventoryview

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// trendPlan describes how one overview interval is summarised. Every plan keeps
// the result at roughly 60-125 points per series, which is plenty for a small
// sparkline and keeps the response bounded no matter how long the interval is.
type trendPlan struct {
	duration time.Duration
	step     time.Duration
	// ttl is how long a computed result is reused. It is a fraction of the step,
	// so charts never lag by a visible amount, yet repeated refreshes and several
	// open browsers share one query.
	ttl time.Duration
	// hourly is true when the interval is read from hourly summaries (plus the
	// not-yet-summarised raw rows) instead of expanding raw snapshots.
	hourly bool
}

var trendPlans = map[string]trendPlan{
	"1h":   {time.Hour, time.Minute, 15 * time.Second, false},
	"6h":   {6 * time.Hour, 5 * time.Minute, 30 * time.Second, false},
	"24h":  {24 * time.Hour, 15 * time.Minute, 60 * time.Second, false},
	"7d":   {7 * 24 * time.Hour, time.Hour, 3 * time.Minute, true},
	"30d":  {30 * 24 * time.Hour, 6 * time.Hour, 5 * time.Minute, true},
	"90d":  {90 * 24 * time.Hour, 18 * time.Hour, 5 * time.Minute, true},
	"365d": {365 * 24 * time.Hour, 72 * time.Hour, 10 * time.Minute, true},
}

// trendValue renders a number with six significant digits. Full float64 text is
// about twice as long and a 110px sparkline cannot show the difference.
type trendValue float64

func (v trendValue) MarshalJSON() ([]byte, error) {
	return strconv.AppendFloat(nil, float64(v), 'g', 6, 64), nil
}

type TrendSeries struct {
	Name   string          `json:"name"`
	Unit   string          `json:"unit"`
	Labels json.RawMessage `json:"labels"`
	// Values has one entry per bin; null marks a bin with no measurement.
	Values []*trendValue `json:"values"`
}

type Trends struct {
	Range       string        `json:"range"`
	WindowStart time.Time     `json:"windowStart"`
	WindowEnd   time.Time     `json:"windowEnd"`
	StepSeconds int64         `json:"stepSeconds"`
	Bins        int           `json:"bins"`
	Series      []TrendSeries `json:"series"`
}

// trendGrid aligns bins to multiples of step since the Unix epoch. Steps of an
// hour or more are therefore whole UTC hours and match the hourly summaries.
func trendGrid(plan trendPlan, now time.Time) (start, end time.Time, bins int) {
	step := int64(plan.step / time.Second)
	bins = int((plan.duration + plan.step - 1) / plan.step)
	last := now.UTC().Unix() / step * step
	first := last - int64(bins-1)*step
	return time.Unix(first, 0).UTC(), time.Unix(last+step, 0).UTC(), bins
}

const trendSQL = `WITH device_agents AS (
	SELECT id FROM agent_identities WHERE device_id=$1 AND revoked_at IS NULL
), cur AS (
	-- Only series the device reports now. Recreated containers and unplugged
	-- interfaces leave thousands of dead series that no card can display.
	SELECT DISTINCT ON (m->>'name', COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb))
		m->>'name' AS name, COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb) AS labels, COALESCE(m->>'unit','') AS unit
	FROM agent_telemetry_latest l JOIN device_agents a ON a.id=l.agent_id
	CROSS JOIN LATERAL jsonb_array_elements(COALESCE(l.payload->'metrics','[]'::jsonb)) m
), parts AS (
	SELECT ((extract(epoch FROM t.observed_at))::bigint - $4) / $5 AS bin, c.name, c.labels, (m->>'value')::double precision AS s, 1::bigint AS n
	FROM agent_telemetry t JOIN device_agents a ON a.id=t.agent_id
	CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.payload->'metrics','[]'::jsonb)) m
	JOIN cur c ON c.name=m->>'name' AND c.labels=COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb)
	WHERE t.observed_at >= $2 AND t.observed_at < $3 AND (NOT $6 OR t.rolled_at IS NULL)
	UNION ALL
	SELECT ((extract(epoch FROM r.bucket_start))::bigint - $4) / $5, r.metric_name, r.labels, r.sum_value, r.sample_count
	FROM telemetry_metric_rollups r JOIN device_agents a ON a.id=r.agent_id
	JOIN cur c ON c.name=r.metric_name AND c.labels=r.labels
	WHERE $6 AND r.bucket_start >= $2 AND r.bucket_start < $3
), grouped AS (
	SELECT bin, name, labels, sum(s) AS s, sum(n) AS n FROM parts GROUP BY 1,2,3
)
SELECT g.bin, g.name, c.unit, g.labels, g.s, g.n FROM grouped g JOIN cur c USING (name, labels) ORDER BY g.name, g.labels, g.bin`

func (h *Handler) buildTrends(ctx context.Context, deviceID, rangeName string, plan trendPlan) ([]byte, error) {
	start, end, bins := trendGrid(plan, h.now())
	rows, err := h.pool.Query(ctx, trendSQL, deviceID, start, end, start.Unix(), int64(plan.step/time.Second), plan.hourly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Trends{Range: rangeName, WindowStart: start, WindowEnd: end, StepSeconds: int64(plan.step / time.Second), Bins: bins, Series: []TrendSeries{}}
	index := map[string]int{}
	for rows.Next() {
		var bin int64
		var name, unit string
		var labels []byte
		var sum float64
		var count int64
		if err = rows.Scan(&bin, &name, &unit, &labels, &sum, &count); err != nil {
			return nil, err
		}
		if bin < 0 || bin >= int64(bins) || count <= 0 {
			continue
		}
		key := name + "\x00" + string(labels)
		at, ok := index[key]
		if !ok {
			at = len(out.Series)
			index[key] = at
			out.Series = append(out.Series, TrendSeries{Name: name, Unit: unit, Labels: labels, Values: make([]*trendValue, bins)})
		}
		value := trendValue(sum / float64(count))
		out.Series[at].Values[bin] = &value
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out.Series, func(i, j int) bool {
		if out.Series[i].Name != out.Series[j].Name {
			return out.Series[i].Name < out.Series[j].Name
		}
		return string(out.Series[i].Labels) < string(out.Series[j].Labels)
	})
	return json.Marshal(out)
}

// trends returns every current series of a device for one overview interval in
// a single, bounded response. The overview used to issue up to 17 history
// requests per refresh, most rejected after doing their work.
func (h *Handler) trends(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	rangeName := r.URL.Query().Get("range")
	plan, ok := trendPlans[rangeName]
	if !ok {
		http.Error(w, "range must be 1h, 6h, 24h, 7d, 30d, 90d, or 365d", 400)
		return
	}
	deviceID := r.PathValue("deviceID")
	if !uuidRE.MatchString(deviceID) {
		http.Error(w, "invalid device ID", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var exists bool
	if err := h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE id=$1)`, deviceID).Scan(&exists); err != nil {
		http.Error(w, "could not load trends", 500)
		return
	}
	if !exists {
		http.Error(w, "device not found", 404)
		return
	}
	body, err := h.trendCache.get(ctx, deviceID+"|"+rangeName, plan.ttl, h.now, func() ([]byte, error) {
		// Detached from this request: other viewers are waiting on the same result.
		buildCtx, buildCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer buildCancel()
		return h.buildTrends(buildCtx, deviceID, rangeName, plan)
	})
	if err != nil {
		http.Error(w, "could not load trends", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// resultCache keeps recent encoded responses and lets concurrent callers share
// one computation. It holds at most a few hundred small entries.
type resultCache struct {
	mu    sync.Mutex
	items map[string]*cacheItem
}

type cacheItem struct {
	body  []byte
	at    time.Time
	ready chan struct{}
	err   error
}

const maxCacheItems = 256

func (c *resultCache) get(ctx context.Context, key string, ttl time.Duration, now func() time.Time, build func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if c.items == nil {
		c.items = map[string]*cacheItem{}
	}
	if item, ok := c.items[key]; ok {
		select {
		case <-item.ready:
			if item.err == nil && now().Sub(item.at) < ttl {
				c.mu.Unlock()
				return item.body, nil
			}
		default:
			// Someone is already computing this result; wait for it.
			c.mu.Unlock()
			select {
			case <-item.ready:
				return item.body, item.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if len(c.items) >= maxCacheItems {
		for k, item := range c.items {
			select {
			case <-item.ready:
				delete(c.items, k)
			default:
			}
		}
	}
	item := &cacheItem{ready: make(chan struct{})}
	c.items[key] = item
	c.mu.Unlock()
	item.body, item.err = build()
	item.at = now()
	if item.err != nil {
		c.mu.Lock()
		if c.items[key] == item {
			delete(c.items, key)
		}
		c.mu.Unlock()
	}
	close(item.ready)
	return item.body, item.err
}
