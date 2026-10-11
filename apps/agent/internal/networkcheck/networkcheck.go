// Package networkcheck runs explicitly configured, bounded HTTP and TCP health checks.
package networkcheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MaxChecks      = 16
	MaxConfigBytes = 16 * 1024
	maxBodyBytes   = 1024
	defaultTimeout = 1500 * time.Millisecond
	maxTimeout     = 3 * time.Second
	batchTimeout   = 5 * time.Second
)

type Check struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url,omitempty"`
	Address string `json:"address,omitempty"`
	Timeout int    `json:"timeoutMs,omitempty"`
}
type config struct {
	Checks []Check `json:"checks"`
}

// Result deliberately contains no endpoint or response data, so it is safe to
// turn into telemetry labels and errors.
type Result struct {
	Name    string
	Type    string
	OK      bool
	Latency time.Duration
}

func Load(path string) ([]Check, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("network checks configuration unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil || len(data) > MaxConfigBytes {
		return nil, errors.New("network checks configuration exceeds size limit")
	}
	var cfg config
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return nil, errors.New("invalid network checks configuration")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid network checks configuration")
	}
	if len(cfg.Checks) > MaxChecks {
		return nil, errors.New("too many network checks")
	}
	seen := map[string]bool{}
	for i := range cfg.Checks {
		c := &cfg.Checks[i]
		if !validName(c.Name) || seen[c.Name] {
			return nil, errors.New("invalid network check name")
		}
		seen[c.Name] = true
		if c.Timeout == 0 {
			c.Timeout = int(defaultTimeout / time.Millisecond)
		}
		if c.Timeout < 100 || time.Duration(c.Timeout)*time.Millisecond > maxTimeout {
			return nil, errors.New("network check timeout outside allowed range")
		}
		switch c.Type {
		case "http":
			if c.Address != "" || validateURL(c.URL) != nil {
				return nil, errors.New("invalid HTTP network check target")
			}
		case "tcp":
			if c.URL != "" || validateAddress(c.Address) != nil {
				return nil, errors.New("invalid TCP network check target")
			}
		default:
			return nil, errors.New("unsupported network check type")
		}
	}
	return cfg.Checks, nil
}

func Run(ctx context.Context, checks []Check) []Result {
	if len(checks) > MaxChecks {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()
	type indexedResult struct {
		index  int
		result Result
	}
	jobs := make(chan int, len(checks))
	results := make(chan indexedResult, len(checks))
	workers := 4
	if len(checks) < workers {
		workers = len(checks)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				result := runOne(ctx, checks[index])
				results <- indexedResult{index: index, result: result}
			}
		}()
	}
	for i := range checks {
		jobs <- i
	}
	close(jobs)
	go func() { wg.Wait(); close(results) }()
	completed := make([]*Result, len(checks))
	count := 0
	for count < len(checks) {
		select {
		case item, ok := <-results:
			if !ok {
				return compact(completed)
			}
			result := item.result
			completed[item.index] = &result
			count++
		case <-ctx.Done():
			return compact(completed)
		}
	}
	return compact(completed)
}

func compact(items []*Result) []Result {
	out := make([]Result, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, *item)
		}
	}
	return out
}

func runOne(ctx context.Context, check Check) Result {
	checkCtx, stop := context.WithTimeout(ctx, time.Duration(check.Timeout)*time.Millisecond)
	defer stop()
	start := time.Now()
	ok := false
	if check.Type == "tcp" {
		conn, err := (&net.Dialer{}).DialContext(checkCtx, "tcp", check.Address)
		if err == nil {
			ok = true
			_ = conn.Close()
		}
	} else {
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
		client := &http.Client{Transport: transport, Timeout: time.Duration(check.Timeout) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequestWithContext(checkCtx, http.MethodGet, check.URL, nil)
		resp, err := client.Do(req)
		if err == nil {
			ok = resp.StatusCode >= 200 && resp.StatusCode < 300
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
			_ = resp.Body.Close()
		}
		transport.CloseIdleConnections()
	}
	return Result{Name: check.Name, Type: check.Type, OK: ok, Latency: time.Since(start)}
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("invalid URL")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	if err := validatePort(port); err != nil {
		return err
	}
	return validateIP(host)
}

func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("invalid address")
	}
	if err := validatePort(port); err != nil {
		return err
	}
	return validateIP(host)
}

func validatePort(port string) error {
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return errors.New("invalid port")
	}
	return nil
}

func validateIP(host string) error {
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return errors.New("target must be a local IP address")
	}
	return nil
}
