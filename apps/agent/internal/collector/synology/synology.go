// Package synology collects a small, fixed set of DSM metrics over SNMP.
package synology

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

const (
	SystemStatusOID = ".1.3.6.1.4.1.6574.1.1.0"
	TemperatureOID  = ".1.3.6.1.4.1.6574.1.2.0"
	DiskStatusOID   = ".1.3.6.1.4.1.6574.2.1.1.5"
	DiskTempOID     = ".1.3.6.1.4.1.6574.2.1.1.6"
	maxDisks        = 32
)

var errRowLimit = errors.New("Synology disk table truncated at configured limit")

type Config struct {
	Enabled           bool
	Target, Community string
	Timeout           time.Duration
}
type Metric struct {
	Name   string
	Value  float64
	Unit   string
	Labels map[string]string
}
type Result struct {
	Metrics      []Metric
	Capabilities []string
	Errors       []string
}

// Client is intentionally small so the collector can be tested with local fixtures.
type Client interface {
	Get(context.Context, []string) (map[string]any, error)
	Walk(context.Context, string) (map[string]any, error)
}

type Collector struct {
	target  string
	client  Client
	timeout time.Duration
}

func New(cfg Config) (*Collector, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	target, community := strings.TrimSpace(cfg.Target), cfg.Community
	if target == "" || strings.TrimSpace(community) == "" {
		return nil, errors.New("Synology SNMP target and community are required")
	}
	if len(target) > 253 || len(community) > 128 {
		return nil, errors.New("Synology SNMP configuration exceeds limits")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return nil, errors.New("Synology SNMP target must be host:port")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return nil, errors.New("invalid Synology SNMP port")
	}
	timeout := cfg.Timeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	client := &goSNMPClient{config: &gosnmp.GoSNMP{Target: host, Port: uint16(portNum), Community: community, Version: gosnmp.Version2c, Timeout: timeout, Retries: 0, MaxOids: 16}}
	return &Collector{target: host, client: client, timeout: timeout}, nil
}

func NewWithClient(target string, client Client) *Collector {
	return &Collector{target: target, client: client, timeout: 2 * time.Second}
}

func (c *Collector) Collect(ctx context.Context) (Result, error) {
	if c == nil || c.client == nil {
		return Result{}, nil
	}
	timeout := c.timeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	base := map[string]string{"nas_target": c.target}
	values, err := c.client.Get(ctx, []string{SystemStatusOID, TemperatureOID})
	if err != nil {
		return Result{}, errors.New("Synology SNMP system query failed")
	}
	r := Result{Metrics: []Metric{}, Capabilities: []string{}, Errors: []string{}}
	for _, spec := range []struct{ oid, metric, unit string }{{SystemStatusOID, "synology_system_status", "1"}, {TemperatureOID, "synology_temperature_celsius", "Cel"}} {
		v, ok := number(values[spec.oid])
		valid := ok
		if spec.metric == "synology_system_status" && ok && v != 1 && v != 2 {
			valid = false
		}
		if spec.metric == "synology_temperature_celsius" && ok && v > 150 {
			valid = false
		}
		if valid {
			r.Metrics = append(r.Metrics, Metric{Name: spec.metric, Value: v, Unit: spec.unit, Labels: clone(base)})
		} else {
			r.Errors = append(r.Errors, "Synology system metric unavailable or invalid")
		}
	}
	if len(r.Metrics) > 0 {
		r.Capabilities = append(r.Capabilities, "synology_system_monitoring")
	}
	for _, spec := range []struct{ oid, metric, unit string }{{DiskStatusOID, "synology_disk_status", "1"}, {DiskTempOID, "synology_disk_temperature_celsius", "Cel"}} {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		rows, err := c.client.Walk(ctx, spec.oid)
		if err != nil && !errors.Is(err, errRowLimit) {
			r.Errors = append(r.Errors, "Synology SNMP disk query failed")
			continue
		}
		if errors.Is(err, errRowLimit) {
			r.Errors = append(r.Errors, "Synology disk table truncated at configured limit")
		}
		count := 0
		invalidRows := false
		oids := make([]string, 0, len(rows))
		for oid := range rows {
			oids = append(oids, oid)
		}
		sort.Slice(oids, func(i, j int) bool { return oidLess(oids[i], oids[j]) })
		for _, oid := range oids {
			raw := rows[oid]
			idx := strings.TrimPrefix(oid, spec.oid+".")
			if idx == oid || len(idx) > 16 {
				invalidRows = true
				continue
			}
			if !decimalIndex(idx) {
				invalidRows = true
				continue
			}
			value, ok := number(raw)
			if !ok {
				invalidRows = true
				continue
			}
			if spec.metric == "synology_disk_status" && (value < 1 || value > 5) {
				invalidRows = true
				continue
			}
			if spec.metric == "synology_disk_temperature_celsius" && value > 150 {
				invalidRows = true
				continue
			}
			labels := clone(base)
			labels["disk_index"] = idx
			r.Metrics = append(r.Metrics, Metric{Name: spec.metric, Value: value, Unit: spec.unit, Labels: labels})
			count++
			if count == maxDisks {
				break
			}
		}
		if invalidRows {
			r.Errors = append(r.Errors, "Synology disk metric unavailable or invalid")
		}
		if count > 0 && !contains(r.Capabilities, "synology_disk_monitoring") {
			r.Capabilities = append(r.Capabilities, "synology_disk_monitoring")
		}
	}
	return r, nil
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), v >= 0
	case int32:
		return float64(v), v >= 0
	case int64:
		return float64(v), v >= 0
	case uint:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	}
	return 0, false
}

func clone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type goSNMPClient struct{ config *gosnmp.GoSNMP }

func oidLess(a, b string) bool {
	aa, errA := parseOID(a)
	bb, errB := parseOID(b)
	if errA != nil || errB != nil {
		return false
	}
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return len(aa) < len(bb)
}

func parseOID(value string) ([]uint64, error) {
	parts := strings.Split(strings.TrimPrefix(value, "."), ".")
	if len(parts) < 2 {
		return nil, errors.New("invalid OID")
	}
	result := make([]uint64, len(parts))
	for i, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return nil, errors.New("invalid OID")
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, err
		}
		result[i] = uint64(n)
	}
	return result, nil
}

func decimalIndex(value string) bool { return value != "" && strings.Trim(value, "0123456789") == "" }

func (c *goSNMPClient) connect(ctx context.Context) (*gosnmp.GoSNMP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := *c.config
	s.Context = ctx
	if err := s.Connect(); err != nil {
		return nil, err
	}
	return &s, nil
}
func (c *goSNMPClient) Get(ctx context.Context, oids []string) (map[string]any, error) {
	s, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = s.Conn.Close() })
	defer stopCancel()
	p, err := s.Get(oids)
	if err != nil {
		return nil, err
	}
	if p.PDUType != gosnmp.GetResponse || p.Error != gosnmp.NoError || len(p.Variables) != len(oids) {
		return nil, errors.New("invalid SNMP response")
	}
	out := map[string]any{}
	for _, v := range p.Variables {
		if v.Type != gosnmp.Integer {
			return nil, errors.New("invalid SNMP value type")
		}
		out[v.Name] = v.Value
	}
	for _, oid := range oids {
		if _, ok := out[oid]; !ok {
			return nil, errors.New("SNMP response OID mismatch")
		}
	}
	return out, nil
}

func (c *goSNMPClient) Walk(ctx context.Context, root string) (map[string]any, error) {
	s, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = s.Conn.Close() })
	defer stopCancel()
	out := map[string]any{}
	cursor := root
	last := root
	for request := 0; request < maxDisks+1; request++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := s.GetBulk([]string{cursor}, 0, 1)
		if err != nil {
			return nil, err
		}
		if p.PDUType != gosnmp.GetResponse || p.Error != gosnmp.NoError {
			return nil, errors.New("SNMP walk returned error status")
		}
		if len(p.Variables) == 0 {
			return out, nil
		}
		for _, v := range p.Variables {
			if v.Type == gosnmp.EndOfMibView || v.Type == gosnmp.NoSuchObject || v.Type == gosnmp.NoSuchInstance {
				return out, nil
			}
			if !strings.HasPrefix(v.Name, root+".") {
				return out, nil
			}
			if !oidLess(last, v.Name) || v.Type != gosnmp.Integer {
				return nil, errors.New("invalid SNMP walk response")
			}
			if len(out) >= maxDisks {
				return out, errRowLimit
			}
			out[v.Name] = v.Value
			cursor = v.Name
			last = v.Name
		}
		if len(out) >= maxDisks && request == maxDisks {
			return out, errRowLimit
		}
	}
	return out, nil
}
