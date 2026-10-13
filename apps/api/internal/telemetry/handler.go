package telemetry

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxRequestBytes = 1 << 20

type Handler struct {
	pool  *pgxpool.Pool
	store Store
}

func NewHandler(pool *pgxpool.Pool, stores ...Store) *Handler {
	var store Store = postgresStore{pool: pool}
	if len(stores) > 0 && stores[0] != nil {
		store = stores[0]
	}
	return &Handler{pool: pool, store: store}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/telemetry", h.ingest)
}

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	var batch protocol.TelemetrySnapshotBatch
	if !decode(w, r, &batch) {
		return
	}
	if batch.ProtocolVersion != protocol.CurrentVersion || batch.AgentID == "" || len(batch.Snapshots) == 0 || len(batch.Snapshots) > 10 {
		http.Error(w, "invalid telemetry batch", http.StatusBadRequest)
		return
	}
	agentID, ok := h.authenticatedAgent(w, r)
	if !ok {
		return
	}
	if batch.AgentID != agentID {
		http.Error(w, "credential does not belong to this agent", http.StatusForbidden)
		return
	}
	seen := make(map[string]bool, len(batch.Snapshots))
	now := time.Now().UTC()
	for _, snapshot := range batch.Snapshots {
		if err := validate(snapshot, agentID, now); err != nil {
			http.Error(w, "invalid telemetry snapshot: "+err.Error(), http.StatusBadRequest)
			return
		}
		if seen[snapshot.ID] {
			http.Error(w, "duplicate telemetry snapshot id", http.StatusBadRequest)
			return
		}
		seen[snapshot.ID] = true
	}
	err := h.store.Ingest(r.Context(), agentID, batch.Snapshots, now)
	if errors.Is(err, ErrRateLimited) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "telemetry ingestion rate exceeded", http.StatusTooManyRequests)
		return
	}
	if errors.Is(err, ErrSeriesLimit) {
		http.Error(w, "telemetry metric series limit exceeded", http.StatusUnprocessableEntity)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invalid or revoked agent credential", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "could not store telemetry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) authenticatedAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
		http.Error(w, "agent credential required", http.StatusUnauthorized)
		return "", false
	}
	credential := strings.TrimPrefix(header, "Bearer ")
	sum := sha256.Sum256([]byte(credential))
	var id string
	err := h.pool.QueryRow(r.Context(), `SELECT id::text FROM agent_identities WHERE credential_hash=$1 AND revoked_at IS NULL`, sum[:]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invalid or revoked agent credential", http.StatusUnauthorized)
		return "", false
	}
	if err != nil {
		http.Error(w, "could not authenticate agent", http.StatusInternalServerError)
		return "", false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "request must contain one JSON value", http.StatusBadRequest)
		return false
	}
	return true
}

var metricUnits = map[string]string{
	"cpu_used_percent":   "%",
	"memory_total_bytes": "By", "memory_available_bytes": "By",
	"memory_used_percent": "%", "uptime_seconds": "s",
	"filesystem_total_bytes": "By", "filesystem_free_bytes": "By",
	"disk_read_bytes_per_second": "By/s", "disk_write_bytes_per_second": "By/s",
	"network_receive_bytes_per_second": "By/s", "network_send_bytes_per_second": "By/s",
	"network_check_success": "1", "network_check_latency_milliseconds": "ms",
	"docker_memory_usage_bytes": "By", "docker_memory_limit_bytes": "By", "docker_memory_used_percent": "%",
	"docker_cpu_capacity_used_percent": "%", "docker_processes": "1",
	"docker_network_receive_bytes_per_second": "By/s", "docker_network_transmit_bytes_per_second": "By/s",
	"synology_system_status": "1", "synology_temperature_celsius": "Cel",
	"synology_disk_status": "1", "synology_disk_temperature_celsius": "Cel",
}

var dockerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,128}$`)

func validate(s protocol.TelemetrySnapshot, agentID string, now time.Time) error {
	if s.ProtocolVersion != protocol.CurrentVersion || s.ID == "" || len(s.ID) > 128 || s.AgentID != agentID {
		return errors.New("invalid identity")
	}
	if s.ObservedAt.IsZero() || s.ObservedAt.After(now.Add(5*time.Minute)) {
		return errors.New("observation time must not be in the future")
	}
	if s.Host.OS == "" || len(s.Host.OS) > 64 || s.Host.Architecture == "" || len(s.Host.Architecture) > 64 || s.Host.AgentVersion == "" || len(s.Host.AgentVersion) > 64 || len(s.Host.OSVersion) > 256 || len(s.Host.CPUModel) > 256 || s.Host.LogicalCPUs < 0 {
		return errors.New("invalid host summary")
	}
	if s.HardwareInventory != nil {
		if !s.HardwareInventory.ObservedAt.Equal(s.ObservedAt) {
			return errors.New("hardware inventory observation time must match snapshot")
		}
		if err := validateHardwareInventory(*s.HardwareInventory); err != nil {
			return err
		}
	}
	if len(s.Metrics) > 512 || len(s.Capabilities) > 128 || len(s.Errors) > 128 {
		return errors.New("snapshot exceeds item limits")
	}
	if len(s.DockerContainers) > 32 {
		return errors.New("too many Docker containers")
	}
	seenDocker := map[string]bool{}
	for _, c := range s.DockerContainers {
		if !dockerIDPattern.MatchString(c.ID) || seenDocker[c.ID] || len(c.Name) > 128 || len(c.Image) > 128 || len(c.State) > 32 {
			return errors.New("invalid Docker container inventory")
		}
		if c.State != "created" && c.State != "restarting" && c.State != "running" && c.State != "removing" && c.State != "paused" && c.State != "exited" && c.State != "dead" {
			return errors.New("invalid Docker container state")
		}
		seenDocker[c.ID] = true
	}
	seenCaps := map[string]bool{}
	for _, cap := range s.Capabilities {
		if cap == "" || len(cap) > 128 || seenCaps[cap] {
			return errors.New("invalid capability")
		}
		seenCaps[cap] = true
	}
	for _, item := range s.Errors {
		if item.Capability == "" || len(item.Capability) > 128 || item.Message == "" || len(item.Message) > 256 {
			return errors.New("invalid collection error")
		}
	}
	for _, metric := range s.Metrics {
		unit, ok := metricUnits[metric.Name]
		if !ok || metric.Unit != unit || math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) || metric.Value < 0 {
			return errors.New("invalid metric name, unit, or value")
		}
		if strings.HasSuffix(metric.Name, "percent") && metric.Value > 100 {
			return errors.New("percentage outside 0..100")
		}
		if strings.HasPrefix(metric.Name, "synology_") {
			if metric.Labels["nas_target"] == "" {
				return errors.New("Synology metric requires a NAS target label")
			}
			for key := range metric.Labels {
				if key != "nas_target" && key != "disk_index" {
					return errors.New("invalid Synology metric label")
				}
			}
			if strings.HasPrefix(metric.Name, "synology_disk_") {
				if metric.Labels["disk_index"] == "" {
					return errors.New("Synology disk metric requires a disk index")
				}
				if len(metric.Labels["disk_index"]) > 16 || !decimalMetricLabel(metric.Labels["disk_index"]) {
					return errors.New("invalid Synology disk index")
				}
			} else if _, ok := metric.Labels["disk_index"]; ok {
				return errors.New("unexpected Synology disk index")
			}
			if (metric.Name == "synology_system_status" && (metric.Value < 1 || metric.Value > 2 || math.Trunc(metric.Value) != metric.Value)) || (metric.Name == "synology_disk_status" && (metric.Value < 1 || metric.Value > 5 || math.Trunc(metric.Value) != metric.Value)) {
				return errors.New("invalid Synology status value")
			}
			if strings.Contains(metric.Name, "temperature") && metric.Value > 150 {
				return errors.New("invalid Synology temperature")
			}
		}
		if len(metric.Labels) > 8 {
			return errors.New("too many metric labels")
		}
		for key, value := range metric.Labels {
			if key == "" || len(key) > 64 || len(value) > 256 {
				return errors.New("invalid metric label")
			}
		}
	}
	return nil
}

func validateHardwareInventory(h protocol.HardwareInventory) error {
	if h.ObservedAt.IsZero() {
		return errors.New("hardware inventory requires an observation time")
	}
	validStatus := func(status string) bool {
		return status == "available" || status == "partial" || status == "unsupported" || status == "unavailable"
	}
	if !validStatus(h.CPUStatus) || !validStatus(h.GPUStatus) || !validStatus(h.MemoryStatus) || !validStatus(h.BoardStatus) || !validStatus(h.DiskStatus) {
		return errors.New("invalid hardware inventory status")
	}
	if h.CPU.PhysicalPackages < 0 || h.CPU.PhysicalPackages > 256 || h.CPU.PhysicalCores < 0 || h.CPU.PhysicalCores > 4096 || h.CPU.LogicalThreads < 0 || h.CPU.LogicalThreads > 8192 || len(h.CPU.Vendor) > 128 || len(h.CPU.Model) > 256 {
		return errors.New("invalid hardware CPU inventory")
	}
	if len(h.GPUs) > 8 || len(h.MemoryModules) > 64 || len(h.Disks) > 32 {
		return errors.New("hardware inventory exceeds item limits")
	}
	for _, g := range h.GPUs {
		if len(g.Vendor) > 128 || len(g.Model) > 256 {
			return errors.New("invalid GPU inventory")
		}
	}
	for _, m := range h.MemoryModules {
		if len(m.Type) > 64 || m.SpeedMHz > 100000 {
			return errors.New("invalid memory module inventory")
		}
	}
	if len(h.Motherboard.Manufacturer) > 128 || len(h.Motherboard.Model) > 256 || len(h.Firmware.Manufacturer) > 128 || len(h.Firmware.Version) > 128 || len(h.Firmware.ReleaseDate) > 32 {
		return errors.New("invalid motherboard or firmware inventory")
	}
	for _, d := range h.Disks {
		if len(d.Model) > 256 || len(d.Type) > 64 {
			return errors.New("invalid disk inventory")
		}
	}
	return nil
}

func decimalMetricLabel(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}
