package auth

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	authAttemptWindow = time.Minute
	authPeerAttempts  = 5
	authTotalAttempts = 30
	maxAuthPeers      = 1024
)

type attemptWindow struct {
	started time.Time
	count   int
}

// AttemptLimiter bounds password work per peer, across all peers, and in
// parallel. It is process-local and resets on restart. Peer identity comes
// from the connection, never from untrusted forwarding headers.
type AttemptLimiter struct {
	mu     sync.Mutex
	peers  map[string]attemptWindow
	total  attemptWindow
	active chan struct{}
	now    func() time.Time
}

func NewAttemptLimiter() *AttemptLimiter {
	return &AttemptLimiter{
		peers: make(map[string]attemptWindow), active: make(chan struct{}, 2), now: time.Now,
	}
}

func (l *AttemptLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/api/auth/login" && r.URL.Path != "/api/setup/first-user" && r.URL.Path != "/api/auth/password") {
			next.ServeHTTP(w, r)
			return
		}
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		if ip := net.ParseIP(peer); ip != nil {
			peer = ip.String()
		}
		if retry := l.allow(peer, l.now()); retry > 0 {
			writeThrottled(w, retry)
			return
		}
		select {
		case l.active <- struct{}{}:
			defer func() { <-l.active }()
			next.ServeHTTP(w, r)
		default:
			writeThrottled(w, time.Second)
		}
	})
}

func (l *AttemptLimiter) allow(peer string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !now.Before(l.total.started.Add(authAttemptWindow)) {
		l.total = attemptWindow{started: now}
	}
	if l.total.count >= authTotalAttempts {
		return l.total.started.Add(authAttemptWindow).Sub(now)
	}
	window, exists := l.peers[peer]
	if !exists && len(l.peers) >= maxAuthPeers {
		for key, previous := range l.peers {
			if !now.Before(previous.started.Add(authAttemptWindow)) {
				delete(l.peers, key)
			}
		}
		if len(l.peers) >= maxAuthPeers {
			return authAttemptWindow
		}
	}
	if !exists || !now.Before(window.started.Add(authAttemptWindow)) {
		window = attemptWindow{started: now}
	}
	if window.count >= authPeerAttempts {
		return window.started.Add(authAttemptWindow).Sub(now)
	}
	window.count++
	l.peers[peer] = window
	l.total.count++
	return 0
}

func writeThrottled(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(int64((retry+time.Second-1)/time.Second), 10))
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"title": "Too Many Requests", "status": http.StatusTooManyRequests,
		"detail": "Too many authentication attempts. Please wait and try again.",
	})
}
