package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// UserFromRequest resolves the signed-in user from the session cookie.
func (s *Service) UserFromRequest(r *http.Request) (User, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return User{}, false
	}
	user, err := s.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		return User{}, false
	}
	return user, true
}

// SameOrigin reports whether a request that carries an Origin header came from
// this server's own pages. Requests without an Origin header are not browser
// cross-site requests and pass.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "https" || u.Scheme == "http")
}

// Authorize requires a signed-in user holding permission. It writes the error
// response itself and returns false when access is refused, so callers simply
// return. Refused permission is reported as 403, a missing session as 401.
func (s *Service) Authorize(w http.ResponseWriter, r *http.Request, permission Permission) (User, bool) {
	if !SameOrigin(r) {
		http.Error(w, "request origin rejected", http.StatusForbidden)
		return User{}, false
	}
	user, ok := s.UserFromRequest(r)
	if !ok {
		http.Error(w, "login required", http.StatusUnauthorized)
		return User{}, false
	}
	if !user.Can(permission) {
		http.Error(w, "your role does not allow this action", http.StatusForbidden)
		return User{}, false
	}
	return user, true
}

// writeGuardOpen lists the state-changing routes any signed-in role may call.
// Everything else that changes state needs the admin role, so a viewer or
// operator can never reach an older admin-only endpoint that predates roles.
// Routes under these prefixes must check their own, narrower permission.
var writeGuardOpen = []string{"/api/auth/", "/api/setup/", "/api/agent/", "/api/channel/", "/api/containers/", "/api/docker/"}

// WriteGuard rejects state-changing /api requests from signed-in users who are
// not administrators, except on routes that enforce their own permission.
// Requests without a valid session are passed through for their handlers to
// refuse.
func (s *Service) WriteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		for _, prefix := range writeGuardOpen {
			if strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}
		user, err := s.userForGuard(r)
		if err == nil && user.Role != RoleAdmin {
			http.Error(w, "your role does not allow this action", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) userForGuard(r *http.Request) (User, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return User{}, err
	}
	user, err := s.Authenticate(r.Context(), cookie.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, err
	}
	return user, err
}
