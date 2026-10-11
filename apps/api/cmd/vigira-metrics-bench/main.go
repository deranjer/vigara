// Command vigira-metrics-bench reports read-only PostgreSQL observations or
// measures the current telemetry store against a short synthetic workload.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/telemetry"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dsnEnv = "VIGIRA_METRICS_BENCH_DSN"

type report struct {
	Mode           string       `json:"mode"`
	GeneratedAt    time.Time    `json:"generated_at"`
	Postgres       string       `json:"postgres_version"`
	Database       string       `json:"database"`
	Classification string       `json:"classification"`
	Notes          []string     `json:"notes,omitempty"`
	Tables         []tableStats `json:"tables,omitempty"`
	Plans          []plan       `json:"plans,omitempty"`
	Scenarios      []scenario   `json:"scenarios,omitempty"`
}
type tableStats struct {
	Name          string `json:"name"`
	EstimatedRows int64  `json:"estimated_rows"`
	TotalBytes    int64  `json:"total_bytes"`
	TableBytes    int64  `json:"table_bytes"`
	IndexBytes    int64  `json:"index_bytes"`
}
type plan struct {
	Name  string   `json:"name"`
	Lines []string `json:"explain_analyze_buffers"`
}
type scenario struct {
	Devices                          int    `json:"devices"`
	SamplingInterval                 string `json:"sampling_interval"`
	RawRetention                     string `json:"raw_retention"`
	RollupRetention                  string `json:"rollup_retention"`
	SnapshotsPerDevice               int    `json:"snapshots_per_device"`
	MetricSeries                     int64  `json:"metric_series"`
	RawRowsAfterRetention            int64  `json:"raw_rows_after_retention"`
	RollupRows                       int64  `json:"rollup_rows"`
	TelemetryRelationBytes           int64  `json:"telemetry_relation_bytes"`
	RawRowsBeforeRetention           int64  `json:"raw_rows_before_retention"`
	RawBytesBeforeRetention          int64  `json:"raw_bytes_before_retention"`
	ProjectedRawSnapshotsAtRetention int64  `json:"projected_raw_snapshots_at_retention"`
	ProjectedRawBytesAtRetention     int64  `json:"projected_raw_bytes_at_retention"`
	ProjectedHourlyRollupRows        int64  `json:"projected_hourly_rollup_rows"`
	IngestBatchMedian                string `json:"ingest_batch_median"`
	IngestBatchP95                   string `json:"ingest_batch_p95"`
	RetentionElapsed                 string `json:"retention_elapsed"`
	FirstPostIngestRead              string `json:"first_post_ingest_read"`
	WarmReadMedian                   string `json:"warm_read_median"`
	WarmReadP95                      string `json:"warm_read_p95"`
	Plans                            []plan `json:"plans,omitempty"`
}

var schemaNamePattern = regexp.MustCompile(`^vigira_metrics_bench_[a-f0-9]{16}$`)
var databaseMarkerPattern = regexp.MustCompile(`(^|[_-])(test|dev|bench)([_-]|$)`)

func main() {
	mode := flag.String("mode", "audit", "audit or synthetic")
	timeout := flag.Duration("statement-timeout", 2*time.Second, "per-query timeout")
	confirm := flag.Bool("confirm-synthetic", false, "allow synthetic mode to create and clean up its unique schema")
	deviceCSV := flag.String("devices", "12,36,60", "synthetic device counts")
	interval := flag.Duration("interval", 30*time.Second, "synthetic sampling interval (6s..1h)")
	minutes := flag.Int("minutes", 10, "synthetic duration (1..60)")
	rawCSV := flag.String("raw-retentions", "25h,48h,168h", "raw retention scenarios")
	rollupCSV := flag.String("rollup-retentions", "90d,365d", "rollup retention scenarios")
	flag.Parse()
	dsn := os.Getenv(dsnEnv)
	if strings.TrimSpace(dsn) == "" {
		fail(fmt.Errorf("set %s to an explicit PostgreSQL DSN; credentials are read only from the environment", dsnEnv))
	}
	if *timeout < 100*time.Millisecond || *timeout > 30*time.Second {
		fail(errors.New("statement-timeout must be between 100ms and 30s"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var r report
	var err error
	switch *mode {
	case "audit":
		r, err = audit(ctx, dsn, *timeout)
	case "synthetic":
		if !*confirm {
			fail(errors.New("synthetic mode requires -confirm-synthetic"))
		}
		r, err = synthetic(ctx, dsn, *timeout, *deviceCSV, *interval, *minutes, *rawCSV, *rollupCSV)
	default:
		err = fmt.Errorf("unknown mode %q; use audit or synthetic", *mode)
	}
	if err != nil {
		fail(err)
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err = e.Encode(r); err != nil {
		fail(err)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, "vigira-metrics-bench:", err); os.Exit(2) }

func openPool(ctx context.Context, dsn, searchPath string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	if searchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return p, nil
}

func openTimedPool(ctx context.Context, dsn, searchPath string, timeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", timeout.Milliseconds())
	if searchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return p, nil
}

func audit(ctx context.Context, dsn string, timeout time.Duration) (report, error) {
	p, err := openTimedPool(ctx, dsn, "", timeout)
	if err != nil {
		return report{}, err
	}
	defer p.Close()
	tx, err := p.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return report{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout='%dms'", timeout.Milliseconds())); err != nil {
		return report{}, err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='250ms'"); err != nil {
		return report{}, err
	}
	r := report{Mode: "audit", GeneratedAt: time.Now().UTC(), Classification: "observed database; real workload representativeness is unknown", Notes: []string{
		"All work ran in a read-only transaction with a per-statement timeout.",
		"Row counts are catalog estimates and can be stale; relation sizes are current measurements.",
		"EXPLAIN ANALYZE used bounded SELECT statements only. No ANALYZE, VACUUM, or maintenance ran.",
		"Observed data does not establish production load or justify a separate metrics database.",
	}}
	if err = tx.QueryRow(ctx, "SELECT version(),current_database()").Scan(&r.Postgres, &r.Database); err != nil {
		return report{}, err
	}
	rows, err := tx.Query(ctx, `SELECT c.relname,GREATEST(COALESCE(s.n_live_tup,0),0)::bigint,pg_total_relation_size(c.oid)::bigint,pg_relation_size(c.oid)::bigint,pg_indexes_size(c.oid)::bigint
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_stat_user_tables s ON s.relid=c.oid
WHERE n.nspname=current_schema() AND c.relname IN ('agent_telemetry','agent_telemetry_latest','telemetry_metric_series','telemetry_metric_rollups','telemetry_report_dedup','telemetry_ingest_limits') ORDER BY c.relname`)
	if err != nil {
		return report{}, fmt.Errorf("read relation sizes: %w", err)
	}
	for rows.Next() {
		var t tableStats
		if err = rows.Scan(&t.Name, &t.EstimatedRows, &t.TotalBytes, &t.TableBytes, &t.IndexBytes); err != nil {
			rows.Close()
			return report{}, err
		}
		r.Tables = append(r.Tables, t)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return report{}, err
	}
	rows.Close()
	if _, err = tx.Exec(ctx, "SAVEPOINT raw_range"); err != nil {
		return report{}, err
	}
	var oldest, newest *time.Time
	err = tx.QueryRow(ctx, `SELECT min(observed_at),max(observed_at) FROM agent_telemetry WHERE observed_at>=now()-interval '168 hours'`).Scan(&oldest, &newest)
	if err != nil {
		r.Notes = append(r.Notes, "Raw observation range unavailable (timeout or telemetry table absent): "+brief(err))
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT raw_range")
	} else if oldest == nil {
		r.Notes = append(r.Notes, "No raw samples found in the last 168h; this does not show that telemetry tables or longer-term workload are absent.")
	} else {
		r.Notes = append(r.Notes, fmt.Sprintf("Raw samples in the last 168h span %s to %s.", oldest.UTC().Format(time.RFC3339), newest.UTC().Format(time.RFC3339)))
	}
	queries := []struct{ name, sql string }{
		{"latest device page", `SELECT d.id FROM devices d LEFT JOIN LATERAL (SELECT id FROM agent_identities ai WHERE ai.device_id=d.id ORDER BY (ai.revoked_at IS NULL) DESC,ai.created_at DESC LIMIT 1) i ON true LEFT JOIN agent_telemetry_latest l ON l.agent_id=i.id ORDER BY d.display_name,d.id LIMIT 100`},
		{"raw recent samples", `SELECT t.observed_at,t.payload FROM agent_telemetry t WHERE t.observed_at>=now()-interval '1 hour' AND t.observed_at<=now() ORDER BY t.observed_at DESC LIMIT 500`},
		{"rollup recent samples", `SELECT r.bucket_start,r.sample_count,r.sum_value FROM telemetry_metric_rollups r WHERE r.bucket_start>=now()-interval '30 days' AND r.bucket_start<=now() ORDER BY r.bucket_start DESC LIMIT 500`},
		{"bounded retention candidates", `SELECT id FROM agent_telemetry WHERE observed_at<now()-interval '48 hours' ORDER BY observed_at LIMIT 500`},
	}
	for _, q := range queries {
		_, _ = tx.Exec(ctx, "SAVEPOINT plan_read")
		lines, e := explain(ctx, tx, q.sql)
		if e != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("Plan %q unavailable: %s", q.name, brief(e)))
			_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT plan_read")
			continue
		}
		r.Plans = append(r.Plans, plan{Name: q.name, Lines: lines})
	}
	return r, nil
}
func brief(err error) string {
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(s) > 180 {
		return s[:180]
	}
	return s
}
func explain(ctx context.Context, tx pgx.Tx, sql string) ([]string, error) {
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readPlan(rows)
}
func explainPool(ctx context.Context, p *pgxpool.Pool, sql string, args ...any) ([]string, error) {
	rows, err := p.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readPlan(rows)
}
func readPlan(rows pgx.Rows) ([]string, error) {
	lines := make([]string, 0, 16)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		if len(line) > 1200 {
			line = line[:1200] + "…"
		}
		lines = append(lines, line)
		if len(lines) >= 40 {
			lines = append(lines, "… truncated at 40 lines")
			break
		}
	}
	return lines, rows.Err()
}

func synthetic(ctx context.Context, dsn string, timeout time.Duration, deviceCSV string, interval time.Duration, minutes int, rawCSV, rollupCSV string) (report, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return report{}, err
	}
	if !localTestTarget(cfg.Host, cfg.Database) {
		return report{}, fmt.Errorf("synthetic mode requires loopback PostgreSQL and a test/dev/bench database marker, with no production/public marker (got %q)", cfg.Database)
	}
	deviceCounts, err := deviceCounts(deviceCSV)
	if err != nil {
		return report{}, err
	}
	if interval < 6*time.Second || interval > time.Hour {
		return report{}, errors.New("interval must be between 6s and 1h")
	}
	if minutes < 1 || minutes > 60 {
		return report{}, errors.New("minutes must be between 1 and 60")
	}
	rawAges, err := durationList(rawCSV)
	if err != nil {
		return report{}, fmt.Errorf("raw-retentions: %w", err)
	}
	rollupAges, err := durationList(rollupCSV)
	if err != nil {
		return report{}, fmt.Errorf("rollup-retentions: %w", err)
	}
	for _, raw := range rawAges {
		if raw < 25*time.Hour || raw > 168*time.Hour {
			return report{}, errors.New("raw retentions must be 25h..168h")
		}
		for _, roll := range rollupAges {
			if roll < raw || roll > 365*24*time.Hour {
				return report{}, errors.New("rollup retentions must be at least all raw retentions and at most 365d")
			}
		}
	}
	admin, err := openPool(ctx, dsn, "")
	if err != nil {
		return report{}, err
	}
	defer admin.Close()
	r := report{Mode: "synthetic", GeneratedAt: time.Now().UTC(), Classification: "synthetic local workload; not observed user or production load", Notes: []string{
		"Every case uses a random new schema and the current telemetry Store.Ingest and Store.Retain methods.",
		"Cleanup drops only that schema after its ownership token is verified; no existing table is truncated or dropped.",
		"Short datasets measure path cost and row/index growth, not a complete 48h raw or 90d rollup population.",
		"Projection fields apply short-run bytes-per-snapshot and hourly-series counts linearly to the configured horizons; labels, PostgreSQL settings, and workload mix can change the result substantially.",
		"First read follows ingest and is not guaranteed storage-cold; warm figures repeat the same bounded query.",
		"Each snapshot has 11 metrics: host CPU/memory/uptime, two filesystems, one disk, and one network interface, with per-device labels.",
	}}
	if err = admin.QueryRow(ctx, "SELECT version(),current_database()").Scan(&r.Postgres, &r.Database); err != nil {
		return report{}, err
	}
	for _, n := range deviceCounts {
		for _, raw := range rawAges {
			for _, roll := range rollupAges {
				s, e := runCase(ctx, admin, dsn, timeout, n, interval, minutes, raw, roll)
				if e != nil {
					return report{}, e
				}
				r.Scenarios = append(r.Scenarios, s)
			}
		}
	}
	return r, nil
}
func localTestTarget(host, db string) bool {
	local := strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
	return local && markedDatabase(db)
}
func markedDatabase(db string) bool {
	db = strings.ToLower(db)
	if strings.Contains(db, "prod") || strings.Contains(db, "live") || strings.Contains(db, "public") {
		return false
	}
	return databaseMarkerPattern.MatchString(db)
}

func runCase(ctx context.Context, admin *pgxpool.Pool, dsn string, timeout time.Duration, devices int, interval time.Duration, minutes int, rawAge, rollupAge time.Duration) (_ scenario, retErr error) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return scenario{}, err
	}
	token := hex.EncodeToString(bytes[:])
	schema := "vigira_metrics_bench_" + token
	if !schemaNamePattern.MatchString(schema) {
		return scenario{}, errors.New("invalid internal schema identifier")
	}
	conn, err := admin.Acquire(ctx)
	if err != nil {
		return scenario{}, err
	}
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('vigira-metrics-bench-schema',0))`); err != nil {
		conn.Release()
		return scenario{}, err
	}
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('vigira-metrics-bench-schema',0))`)
		conn.Release()
		return scenario{}, err
	}
	_, err = conn.Exec(ctx, "CREATE TABLE "+schema+`.bench_owner(owner_token text PRIMARY KEY)`)
	if err == nil {
		_, err = conn.Exec(ctx, "INSERT INTO "+schema+`.bench_owner VALUES($1)`, token)
	}
	_, unlockErr := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('vigira-metrics-bench-schema',0))`)
	conn.Release()
	if err != nil {
		return scenario{}, err
	}
	if unlockErr != nil {
		return scenario{}, unlockErr
	}
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var actual string
		if e := admin.QueryRow(cleanCtx, "SELECT owner_token FROM "+schema+`.bench_owner`).Scan(&actual); e != nil || actual != token {
			if retErr == nil {
				retErr = fmt.Errorf("refusing cleanup: owner token for %s is absent or changed", schema)
			}
			return
		}
		if _, e := admin.Exec(cleanCtx, "DROP SCHEMA "+schema+" CASCADE"); e != nil && retErr == nil {
			retErr = fmt.Errorf("drop owned schema %s: %w", schema, e)
		}
	}()
	p, err := openTimedPool(ctx, dsn, schema, timeout)
	if err != nil {
		return scenario{}, err
	}
	defer p.Close()
	if _, err = p.Exec(ctx, fmt.Sprintf("SET statement_timeout='%dms'", timeout.Milliseconds())); err != nil {
		return scenario{}, err
	}
	if err = fixture(ctx, p); err != nil {
		return scenario{}, err
	}
	ids, err := seed(ctx, p, devices)
	if err != nil {
		return scenario{}, err
	}
	totalDuration := time.Duration(minutes) * time.Minute
	steps := int((totalDuration + interval - 1) / interval)
	if steps > 600 {
		return scenario{}, errors.New("scenario exceeds 600 snapshots per device")
	}
	step := interval
	start := time.Now().UTC().Add(-rawAge).Add(-2 * time.Minute)
	store := telemetry.NewPostgresStore(p)
	ingest := make([]time.Duration, 0)
	for ai, id := range ids {
		for first := 0; first < steps; first += 5 {
			end := first + 5
			if end > steps {
				end = steps
			}
			batch := make([]protocol.TelemetrySnapshot, 0, end-first)
			for n := first; n < end; n++ {
				batch = append(batch, snapshot(id, ai, n, start.Add(time.Duration(n)*step)))
			}
			now := batch[len(batch)-1].ObservedAt.Add(5 * time.Second)
			began := time.Now()
			if err = store.Ingest(ctx, id, batch, now); err != nil {
				return scenario{}, fmt.Errorf("synthetic ingest: %w", err)
			}
			ingest = append(ingest, time.Since(began))
		}
	}
	var rawRowsBefore, rawBytesBefore int64
	if err = p.QueryRow(ctx, `SELECT count(*) FROM agent_telemetry`).Scan(&rawRowsBefore); err != nil {
		return scenario{}, err
	}
	if err = p.QueryRow(ctx, `SELECT pg_total_relation_size('agent_telemetry'::regclass)::bigint`).Scan(&rawBytesBefore); err != nil {
		return scenario{}, err
	}
	retainNow := start.Add(time.Duration(steps) * step).Add(rawAge).Add(time.Minute)
	began := time.Now()
	if err = store.Retain(ctx, telemetry.RetentionConfig{RawAge: rawAge, RollupAge: rollupAge}, retainNow); err != nil {
		return scenario{}, fmt.Errorf("synthetic retention: %w", err)
	}
	retentionTime := time.Since(began)
	var series, rawRows, rollups, bytesUsed int64
	stats := []struct {
		sql  string
		dest *int64
	}{{`SELECT count(*) FROM telemetry_metric_series`, &series}, {`SELECT count(*) FROM agent_telemetry`, &rawRows}, {`SELECT count(*) FROM telemetry_metric_rollups`, &rollups}, {`SELECT COALESCE(sum(pg_total_relation_size(quote_ident(relname)::regclass)),0)::bigint FROM pg_stat_user_tables WHERE schemaname=current_schema() AND relname IN ('agent_telemetry','agent_telemetry_latest','telemetry_metric_series','telemetry_metric_rollups','telemetry_report_dedup','telemetry_ingest_limits')`, &bytesUsed}}
	for _, s := range stats {
		if err = p.QueryRow(ctx, s.sql).Scan(s.dest); err != nil {
			return scenario{}, err
		}
	}
	var deviceID string
	if err = p.QueryRow(ctx, `SELECT device_id::text FROM agent_identities WHERE id=$1`, ids[0]).Scan(&deviceID); err != nil {
		return scenario{}, err
	}
	readSQL := `WITH device_agents AS (SELECT id FROM agent_identities WHERE device_id=$1 AND revoked_at IS NULL), raw AS (
 SELECT t.observed_at AS at,m->>'name' AS name,COALESCE(m->>'unit','') AS unit,COALESCE(NULLIF(m->'labels','null'::jsonb),'{}'::jsonb) AS labels,(m->>'value')::double precision AS value,NULL::double precision AS min,NULL::double precision AS max,1::bigint AS samples
 FROM agent_telemetry t JOIN device_agents a ON a.id=t.agent_id CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.payload->'metrics','[]'::jsonb)) m
 WHERE t.observed_at>=now()-($2*interval '1 second') AND t.observed_at<=now() AND m->>'name'=$4
 UNION ALL SELECT r.bucket_start,r.metric_name,''::text,r.labels,r.sum_value/r.sample_count,r.min_value,r.max_value,r.sample_count FROM telemetry_metric_rollups r JOIN device_agents a ON a.id=r.agent_id
 WHERE r.bucket_start>=now()-($2*interval '1 second') AND r.bucket_start<=now() AND r.metric_name=$4
), binned AS (SELECT date_bin(($3*interval '1 second'),raw.at,timestamptz '2000-01-01') AS at,name,unit,labels,sum(value*samples)/sum(samples) AS value,min(COALESCE(min,value)) AS min,max(COALESCE(max,value)) AS max,sum(samples)::bigint AS samples FROM raw GROUP BY 1,2,3,4)
SELECT at,name,unit,labels,value,min,max,samples FROM binned ORDER BY name,labels,at LIMIT 20001`
	readArgs := []any{deviceID, int64((30 * 24 * time.Hour).Seconds()), int64(time.Hour.Seconds()), "cpu_used_percent"}
	readStart := time.Now()
	if _, err = explainPool(ctx, p, readSQL, readArgs...); err != nil {
		return scenario{}, err
	}
	firstRead := time.Since(readStart)
	warm := make([]time.Duration, 7)
	for i := range warm {
		began = time.Now()
		if _, err = explainPool(ctx, p, readSQL, readArgs...); err != nil {
			return scenario{}, err
		}
		warm[i] = time.Since(began)
	}
	plans := []plan{}
	for _, q := range []struct {
		name, sql string
		args      []any
	}{{"latest device page", `SELECT d.id FROM devices d LEFT JOIN LATERAL (SELECT id FROM agent_identities ai WHERE ai.device_id=d.id ORDER BY (ai.revoked_at IS NULL) DESC,ai.created_at DESC LIMIT 1) i ON true LEFT JOIN agent_telemetry_latest l ON l.agent_id=i.id ORDER BY d.display_name,d.id LIMIT 100`, nil}, {"raw recent samples", `SELECT observed_at,payload FROM agent_telemetry WHERE agent_id=$1 AND observed_at >= $2 AND observed_at <= $3 ORDER BY observed_at LIMIT 20001`, []any{ids[0], start, retainNow}}, {"hourly rollup history", readSQL, readArgs}} {
		ls, e := explainPool(ctx, p, q.sql, q.args...)
		if e != nil {
			return scenario{}, e
		}
		plans = append(plans, plan{Name: q.name, Lines: ls})
	}
	projectedSnapshots := int64(float64(devices) * rawAge.Seconds() / interval.Seconds())
	projectedRawBytes := int64(0)
	if rawRowsBefore > 0 {
		projectedRawBytes = projectedSnapshots * rawBytesBefore / rawRowsBefore
	}
	projectedRollups := series * int64(rollupAge/time.Hour)
	return scenario{Devices: devices, SamplingInterval: interval.String(), RawRetention: fmt.Sprintf("%dh", int(rawAge/time.Hour)), RollupRetention: durationLabel(rollupAge), SnapshotsPerDevice: steps, MetricSeries: series, RawRowsBeforeRetention: rawRowsBefore, RawBytesBeforeRetention: rawBytesBefore, ProjectedRawSnapshotsAtRetention: projectedSnapshots, ProjectedRawBytesAtRetention: projectedRawBytes, ProjectedHourlyRollupRows: projectedRollups, RawRowsAfterRetention: rawRows, RollupRows: rollups, TelemetryRelationBytes: bytesUsed, IngestBatchMedian: percentile(ingest, .5).String(), IngestBatchP95: percentile(ingest, .95).String(), RetentionElapsed: retentionTime.String(), FirstPostIngestRead: firstRead.String(), WarmReadMedian: percentile(warm, .5).String(), WarmReadP95: percentile(warm, .95).String(), Plans: plans}, nil
}

func fixture(ctx context.Context, p *pgxpool.Pool) error {
	ddl := []string{
		`CREATE TABLE devices(id uuid PRIMARY KEY,display_name text NOT NULL,platform_id text,created_at timestamptz NOT NULL,last_seen_at timestamptz)`,
		`CREATE TABLE agent_identities(id uuid PRIMARY KEY,device_id uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,credential_hash bytea NOT NULL,protocol_version text NOT NULL,revoked_at timestamptz,created_at timestamptz NOT NULL,platform text NOT NULL DEFAULT '',architecture text NOT NULL DEFAULT '',agent_version text NOT NULL DEFAULT '',capabilities jsonb NOT NULL DEFAULT '[]'::jsonb)`,
		`CREATE TABLE agent_telemetry(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),agent_id uuid NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,report_id text NOT NULL,observed_at timestamptz NOT NULL,payload jsonb NOT NULL,received_at timestamptz NOT NULL DEFAULT now(),UNIQUE(agent_id,report_id))`,
		`CREATE INDEX agent_telemetry_agent_observed_at_idx ON agent_telemetry(agent_id,observed_at DESC)`, `CREATE INDEX agent_telemetry_observed_at_idx ON agent_telemetry(observed_at,id)`,
		`CREATE TABLE telemetry_ingest_limits(agent_id uuid PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,window_started_at timestamptz NOT NULL,snapshot_count integer NOT NULL DEFAULT 0 CHECK(snapshot_count>=0))`,
		`CREATE TABLE telemetry_report_dedup(agent_id uuid NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,report_id text NOT NULL,received_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(agent_id,report_id))`, `CREATE INDEX telemetry_report_dedup_received_idx ON telemetry_report_dedup(received_at)`,
		`CREATE TABLE telemetry_metric_series(agent_id uuid NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,metric_name text NOT NULL,labels jsonb NOT NULL DEFAULT '{}'::jsonb,legacy boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(agent_id,metric_name,labels))`,
		`CREATE TABLE telemetry_metric_rollups(agent_id uuid NOT NULL REFERENCES agent_identities(id) ON DELETE CASCADE,metric_name text NOT NULL,labels jsonb NOT NULL DEFAULT '{}'::jsonb,bucket_start timestamptz NOT NULL,sample_count bigint NOT NULL CHECK(sample_count>0),sum_value double precision NOT NULL,min_value double precision NOT NULL,max_value double precision NOT NULL,PRIMARY KEY(agent_id,metric_name,labels,bucket_start))`, `CREATE INDEX telemetry_metric_rollups_bucket_idx ON telemetry_metric_rollups(bucket_start)`,
		`CREATE TABLE agent_telemetry_latest(agent_id uuid PRIMARY KEY REFERENCES agent_identities(id) ON DELETE CASCADE,report_id text NOT NULL,observed_at timestamptz NOT NULL,payload jsonb NOT NULL,received_at timestamptz NOT NULL)`,
	}
	for _, s := range ddl {
		if _, err := p.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
func seed(ctx context.Context, p *pgxpool.Pool, n int) ([]string, error) {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		d, a := uuid(), uuid()
		if _, err := p.Exec(ctx, `INSERT INTO devices(id,display_name,created_at) VALUES($1,$2,now())`, d, fmt.Sprintf("synthetic-%03d", i)); err != nil {
			return nil, err
		}
		if _, err := p.Exec(ctx, `INSERT INTO agent_identities(id,device_id,credential_hash,protocol_version,created_at) VALUES($1,$2,$3,'current',now())`, a, d, []byte("bench"+a)); err != nil {
			return nil, err
		}
		ids = append(ids, a)
	}
	return ids, nil
}
func snapshot(agent string, device, seq int, at time.Time) protocol.TelemetrySnapshot {
	m := []protocol.TelemetryMetric{{Name: "cpu_used_percent", Value: float64((seq*7 + device*3) % 100), Unit: "%"}, {Name: "memory_total_bytes", Value: 17179869184, Unit: "By"}, {Name: "memory_available_bytes", Value: 8589934592 - float64(seq%200)*1048576, Unit: "By"}, {Name: "memory_used_percent", Value: float64((seq*3 + 40) % 100), Unit: "%"}, {Name: "uptime_seconds", Value: float64(86400 + seq*30), Unit: "s"}, {Name: "filesystem_total_bytes", Value: 536870912000, Unit: "By", Labels: map[string]string{"mount": "/"}}, {Name: "filesystem_free_bytes", Value: 268435456000 - float64(seq%100)*1048576, Unit: "By", Labels: map[string]string{"mount": "/"}}, {Name: "filesystem_total_bytes", Value: 1073741824000, Unit: "By", Labels: map[string]string{"mount": "/data"}}, {Name: "filesystem_free_bytes", Value: 536870912000 - float64(seq%100)*1048576, Unit: "By", Labels: map[string]string{"mount": "/data"}}, {Name: "disk_read_bytes_per_second", Value: float64((seq*100 + device*7) % 10000000), Unit: "By/s", Labels: map[string]string{"device": "nvme0n1"}}, {Name: "network_receive_bytes_per_second", Value: float64((seq*80 + device*5) % 10000000), Unit: "By/s", Labels: map[string]string{"interface": "eth0"}}}
	return protocol.TelemetrySnapshot{ProtocolVersion: protocol.CurrentVersion, ID: fmt.Sprintf("%d-%d", device, seq), AgentID: agent, ObservedAt: at, Host: protocol.HostSummary{OS: "linux", Architecture: "amd64", AgentVersion: "bench", LogicalCPUs: 8}, Metrics: m, Capabilities: []string{"cpu", "memory", "filesystem", "disk", "network"}}
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func percentile(v []time.Duration, p float64) time.Duration {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[int(float64(len(v)-1)*p)]
}
func durationLabel(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return d.String()
}
func deviceCounts(csv string) ([]int, error) {
	var out []int
	for _, s := range strings.Split(csv, ",") {
		var n int
		if _, e := fmt.Sscan(strings.TrimSpace(s), &n); e != nil || n < 1 || n > 60 {
			return nil, errors.New("device counts must be 1..60")
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one device count is required")
	}
	return out, nil
}
func durationList(csv string) ([]time.Duration, error) {
	var out []time.Duration
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		var d time.Duration
		var e error
		if strings.HasSuffix(s, "d") {
			var days int
			_, e = fmt.Sscan(strings.TrimSuffix(s, "d"), &days)
			d = time.Duration(days) * 24 * time.Hour
		} else {
			d, e = time.ParseDuration(s)
		}
		if e != nil || d <= 0 {
			return nil, fmt.Errorf("invalid duration %q", s)
		}
		out = append(out, d)
	}
	return out, nil
}
