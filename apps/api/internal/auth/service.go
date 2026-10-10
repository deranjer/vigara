package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrSetupComplete      = errors.New("initial setup is already complete")
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUsernameTaken      = errors.New("username is already in use")
)

const sessionLifetime = 30 * 24 * time.Hour

type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type Session struct {
	User      User
	Token     string
	ExpiresAt time.Time
}

type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

type contextKey uint8

const userContextKey contextKey = iota

type sessionContext struct {
	user User
	err  error
	ok   bool
}

func NewService(pool *pgxpool.Pool, queries *db.Queries) *Service {
	return &Service{pool: pool, queries: queries}
}

func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	completedAt, err := s.queries.GetSetupState(ctx)
	if err != nil {
		return false, fmt.Errorf("read setup state: %w", err)
	}
	if completedAt.Valid {
		return false, nil
	}
	count, err := s.queries.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return count == 0, nil
}

func (s *Service) CreateFirstUser(ctx context.Context, username, displayName, password, confirmation string) (Session, error) {
	// Reject completed setup before deriving a password. The transaction below
	// still rechecks under a row lock to serialize concurrent first visits.
	required, err := s.SetupRequired(ctx)
	if err != nil {
		return Session{}, err
	}
	if !required {
		return Session{}, ErrSetupComplete
	}
	username, displayName, err = validateIdentity(username, displayName)
	if err != nil {
		return Session{}, err
	}
	if len(password) < 12 || len(password) > 1024 || password != confirmation {
		return Session{}, fmt.Errorf("%w: password must be at least 12 characters and match the confirmation", ErrInvalidInput)
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return Session{}, fmt.Errorf("hash initial password: %w", err)
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionLifetime)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin initial setup: %w", err)
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	completedAt, err := queries.LockSetupState(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("lock initial setup state: %w", err)
	}
	if completedAt.Valid {
		return Session{}, ErrSetupComplete
	}
	count, err := queries.CountUsers(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("verify initial user state: %w", err)
	}
	if count != 0 {
		return Session{}, ErrSetupComplete
	}
	created, err := queries.CreateFirstUser(ctx, db.CreateFirstUserParams{
		Username: username, DisplayName: displayName, PasswordHash: passwordHash,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Session{}, ErrUsernameTaken
		}
		return Session{}, fmt.Errorf("create initial user: %w", err)
	}
	var userID pgtype.UUID
	if err := userID.Scan(created.ID); err != nil {
		return Session{}, fmt.Errorf("parse created user id: %w", err)
	}
	if err := queries.CreateSession(ctx, db.CreateSessionParams{
		UserID: userID, TokenHash: tokenHash, ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return Session{}, fmt.Errorf("create initial session: %w", err)
	}
	if err := queries.CompleteSetup(ctx); err != nil {
		return Session{}, fmt.Errorf("complete initial setup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit initial setup: %w", err)
	}
	return Session{
		User:  User{ID: created.ID, Username: created.Username, DisplayName: created.DisplayName, Role: created.Role},
		Token: token, ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) Login(ctx context.Context, username, password string) (Session, error) {
	normalized, _, err := validateIdentity(username, username)
	if err != nil {
		_ = verifyPassword(password, dummyPasswordHash)
		return Session{}, ErrInvalidCredentials
	}
	row, err := s.queries.GetUserByUsername(ctx, normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = verifyPassword(password, dummyPasswordHash)
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("find local user: %w", err)
	}
	if !verifyPassword(password, row.PasswordHash) {
		return Session{}, ErrInvalidCredentials
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionLifetime)
	var userID pgtype.UUID
	if err := userID.Scan(row.ID); err != nil {
		return Session{}, fmt.Errorf("parse user id: %w", err)
	}
	if err := s.queries.CreateSession(ctx, db.CreateSessionParams{
		UserID: userID, TokenHash: tokenHash, ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return Session{}, fmt.Errorf("create login session: %w", err)
	}
	return Session{
		User:  User{ID: row.ID, Username: row.Username, DisplayName: row.DisplayName, Role: row.Role},
		Token: token, ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" || len(token) > 128 {
		return User{}, pgx.ErrNoRows
	}
	row, err := s.queries.GetUserBySessionHash(ctx, hashSessionToken(token))
	if err != nil {
		return User{}, err
	}
	return User{ID: row.ID, Username: row.Username, DisplayName: row.DisplayName, Role: row.Role}, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" || len(token) > 128 {
		return nil
	}
	if err := s.queries.DeleteSessionByHash(ctx, hashSessionToken(token)); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// SessionMiddleware resolves the local session for the current-user endpoint.
func (s *Service) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			next.ServeHTTP(w, r)
			return
		}
		state := sessionContext{}
		if cookie, err := r.Cookie(SessionCookieName); err == nil {
			state.user, state.err = s.Authenticate(r.Context(), cookie.Value)
			if errors.Is(state.err, pgx.ErrNoRows) {
				state.err = nil
			} else if state.err == nil {
				state.ok = true
			}
		}
		ctx := context.WithValue(r.Context(), userContextKey, state)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userFromContext(ctx context.Context) (User, error, bool) {
	state, ok := ctx.Value(userContextKey).(sessionContext)
	if !ok {
		return User{}, nil, false
	}
	return state.user, state.err, state.ok
}

func newSessionToken() (string, []byte, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	return token, hashSessionToken(token), nil
}

func hashSessionToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func validateIdentity(username, displayName string) (string, string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 || len(username) > 64 {
		return "", "", fmt.Errorf("%w: username must be 3 to 64 characters", ErrInvalidInput)
	}
	for _, char := range username {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
			return "", "", fmt.Errorf("%w: username may use letters, numbers, dot, underscore, and hyphen", ErrInvalidInput)
		}
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if len(displayName) > 80 {
		return "", "", fmt.Errorf("%w: display name must be at most 80 characters", ErrInvalidInput)
	}
	return username, displayName, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
