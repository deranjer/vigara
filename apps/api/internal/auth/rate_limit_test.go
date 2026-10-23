package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAttemptLimiterPeerBudget(t *testing.T) {
	limiter := NewAttemptLimiter()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	called := 0
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < authPeerAttempts+1; i++ {
		path := []string{"/api/auth/login", "/api/setup/first-user", "/api/auth/password"}[i%3]
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.RemoteAddr = fmt.Sprintf("192.0.2.1:%d", 1000+i)
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i+2))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if i < authPeerAttempts && w.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status %d", i, w.Code)
		}
		if i == authPeerAttempts && (w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60") {
			t.Fatalf("throttled response: status %d, headers %v", w.Code, w.Header())
		}
	}
	if called != authPeerAttempts {
		t.Fatalf("handler called %d times", called)
	}
	// Exhausting password attempts must not prevent logout or normal reads.
	for _, path := range []string{"/api/auth/logout", "/api/auth/me", "/api/setup/status", "/healthz"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.RemoteAddr = "192.0.2.1:2000"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Errorf("%s was throttled", path)
		}
	}
	now = now.Add(authAttemptWindow)
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "192.0.2.1:2000"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatal("peer budget did not reset")
	}
}

func TestAttemptLimiterGlobalBudget(t *testing.T) {
	limiter := NewAttemptLimiter()
	now := time.Now()
	for i := 0; i < authTotalAttempts; i++ {
		if retry := limiter.allow(fmt.Sprintf("peer-%d", i), now); retry != 0 {
			t.Fatalf("peer %d unexpectedly throttled", i)
		}
	}
	if retry := limiter.allow("another-peer", now); retry != authAttemptWindow {
		t.Fatalf("global budget: retry %s", retry)
	}
	if retry := limiter.allow("another-peer", now.Add(authAttemptWindow)); retry != 0 {
		t.Fatal("global budget did not reset")
	}
}

func TestAttemptLimiterConcurrency(t *testing.T) {
	limiter := NewAttemptLimiter()
	entered := make(chan struct{}, cap(limiter.active))
	release := make(chan struct{})
	done := make(chan struct{}, cap(limiter.active))
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < cap(limiter.active); i++ {
		go func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
			done <- struct{}{}
		}()
	}
	for i := 0; i < cap(limiter.active); i++ {
		<-entered
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/setup/first-user", nil))
	close(release)
	for i := 0; i < cap(limiter.active); i++ {
		<-done
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("concurrency limit: status %d, headers %v", w.Code, w.Header())
	}
	// A completed invocation releases its slot.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
}

func TestAttemptLimiterPeerStorageBound(t *testing.T) {
	limiter := NewAttemptLimiter()
	now := time.Now()
	for i := 0; i < maxAuthPeers; i++ {
		limiter.peers[fmt.Sprint(i)] = attemptWindow{started: now}
	}
	if retry := limiter.allow("new-peer", now); retry == 0 || len(limiter.peers) != maxAuthPeers {
		t.Fatal("peer storage exceeded its bound")
	}
	if retry := limiter.allow("new-peer", now.Add(authAttemptWindow)); retry != 0 || len(limiter.peers) != 1 {
		t.Fatal("expired peer entries were not reclaimed")
	}
}
