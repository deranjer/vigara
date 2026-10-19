package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/jackc/pgx/v5"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func roleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 2
	case RoleOperator:
		return 1
	}
	return 0
}

// ErrLastAdmin is returned when a change would leave the server without an administrator.
var ErrLastAdmin = errors.New("at least one administrator must remain")

// ManagedUser is a user as shown to administrators; it never includes credentials.
type ManagedUser struct {
	User
	CreatedAt time.Time `json:"createdAt"`
}

// ListUsers returns all users, oldest first.
func (s *Service) ListUsers(ctx context.Context) ([]ManagedUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, username, display_name, role, created_at FROM users ORDER BY created_at, username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := []ManagedUser{}
	for rows.Next() {
		var u ManagedUser
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		u.CreatedAt = u.CreatedAt.UTC()
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUser adds a user with an explicit role. The first administrator is
// created by the one-time setup flow, so this requires an existing installation.
func (s *Service) CreateUser(ctx context.Context, username, displayName, password, role string) (ManagedUser, error) {
	if !ValidRole(role) {
		return ManagedUser{}, fmt.Errorf("%w: role must be viewer, operator, or admin", ErrInvalidInput)
	}
	username, displayName, err := validateIdentity(username, displayName)
	if err != nil {
		return ManagedUser{}, err
	}
	if len(password) < 12 || len(password) > 1024 {
		return ManagedUser{}, fmt.Errorf("%w: password must be at least 12 characters", ErrInvalidInput)
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return ManagedUser{}, fmt.Errorf("hash password: %w", err)
	}
	var u ManagedUser
	err = s.pool.QueryRow(ctx, `INSERT INTO users(username, display_name, password_hash, role) VALUES ($1,$2,$3,$4) RETURNING id::text, username, display_name, role, created_at`,
		username, displayName, passwordHash, role).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ManagedUser{}, ErrUsernameTaken
		}
		return ManagedUser{}, fmt.Errorf("create user: %w", err)
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, nil
}

// SetUserRole changes a user's role, refusing to remove the last administrator.
// It revokes the user's sessions when the role is lowered so old sessions cannot
// keep elevated access.
func (s *Service) SetUserRole(ctx context.Context, id, role string) (ManagedUser, error) {
	if !ValidRole(role) {
		return ManagedUser{}, fmt.Errorf("%w: role must be viewer, operator, or admin", ErrInvalidInput)
	}
	if !uuidPattern.MatchString(id) {
		return ManagedUser{}, pgx.ErrNoRows
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin role change: %w", err)
	}
	defer tx.Rollback(ctx)
	// Lock every administrator row so two concurrent demotions cannot both pass the check.
	rows, err := tx.Query(ctx, `SELECT id::text FROM users WHERE role='admin' FOR UPDATE`)
	if err != nil {
		return ManagedUser{}, fmt.Errorf("lock administrators: %w", err)
	}
	admins := map[string]bool{}
	for rows.Next() {
		var adminID string
		if err := rows.Scan(&adminID); err != nil {
			rows.Close()
			return ManagedUser{}, fmt.Errorf("scan administrator: %w", err)
		}
		admins[adminID] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ManagedUser{}, fmt.Errorf("lock administrators: %w", err)
	}
	if admins[id] && role != RoleAdmin && len(admins) == 1 {
		return ManagedUser{}, ErrLastAdmin
	}
	var previous string
	if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1::uuid FOR UPDATE`, id).Scan(&previous); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedUser{}, pgx.ErrNoRows
		}
		return ManagedUser{}, fmt.Errorf("read current role: %w", err)
	}
	var u ManagedUser
	err = tx.QueryRow(ctx, `UPDATE users SET role=$2 WHERE id=$1::uuid RETURNING id::text, username, display_name, role, created_at`, id, role).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, pgx.ErrNoRows
	}
	if err != nil {
		return ManagedUser{}, fmt.Errorf("change role: %w", err)
	}
	if roleRank(role) < roleRank(previous) {
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1::uuid`, id); err != nil {
			return ManagedUser{}, fmt.Errorf("revoke sessions: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit role change: %w", err)
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, nil
}

// RegisterAdmin mounts the administrator-only user management and audit-log routes.
func (s *Service) RegisterAdmin(mux *http.ServeMux, recorder *audit.Recorder) {
	respond := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	decode := func(w http.ResponseWriter, r *http.Request, dst any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(dst); err != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return false
		}
		return true
	}
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, ErrInvalidInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrUsernameTaken), errors.Is(err, ErrLastAdmin):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, pgx.ErrNoRows):
			http.Error(w, "user not found", http.StatusNotFound)
		default:
			http.Error(w, "could not complete the request", http.StatusInternalServerError)
		}
	}
	record := func(r *http.Request, actor User, action, targetID, outcome string, detail map[string]any) {
		_ = recorder.Record(r.Context(), audit.Event{ActorID: actor.ID, ActorName: actor.Username, Action: action, TargetKind: "user", TargetID: targetID, Outcome: outcome, Detail: detail})
	}

	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.Authorize(w, r, PermUsersManage); !ok {
			return
		}
		users, err := s.ListUsers(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, http.StatusOK, users)
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.Authorize(w, r, PermUsersManage)
		if !ok {
			return
		}
		var req struct {
			Username    string `json:"username"`
			DisplayName string `json:"displayName"`
			Password    string `json:"password"`
			Role        string `json:"role"`
		}
		if !decode(w, r, &req) {
			return
		}
		created, err := s.CreateUser(r.Context(), req.Username, req.DisplayName, req.Password, req.Role)
		if err != nil {
			record(r, actor, "user.create", req.Username, audit.OutcomeFailure, map[string]any{"role": req.Role})
			fail(w, err)
			return
		}
		record(r, actor, "user.create", created.ID, audit.OutcomeSuccess, map[string]any{"username": created.Username, "role": created.Role})
		respond(w, http.StatusCreated, created)
	})
	mux.HandleFunc("PATCH /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.Authorize(w, r, PermUsersManage)
		if !ok {
			return
		}
		var req struct {
			Role string `json:"role"`
		}
		if !decode(w, r, &req) {
			return
		}
		updated, err := s.SetUserRole(r.Context(), r.PathValue("id"), req.Role)
		if err != nil {
			record(r, actor, "user.role", r.PathValue("id"), audit.OutcomeFailure, map[string]any{"role": req.Role})
			fail(w, err)
			return
		}
		record(r, actor, "user.role", updated.ID, audit.OutcomeSuccess, map[string]any{"username": updated.Username, "role": updated.Role})
		respond(w, http.StatusOK, updated)
	})
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.Authorize(w, r, PermAuditRead); !ok {
			return
		}
		query := r.URL.Query()
		filter := audit.ListFilter{AgentID: query.Get("agentId"), ActorID: query.Get("actorId"), Action: query.Get("action")}
		if value := query.Get("limit"); value != "" {
			if n, err := strconv.Atoi(value); err == nil {
				filter.Limit = n
			}
		}
		if value := query.Get("before"); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "invalid before cursor", http.StatusBadRequest)
				return
			}
			filter.BeforeID = n
		}
		entries, err := recorder.List(r.Context(), filter)
		if err != nil {
			// Malformed filter identifiers surface as database errors; do not leak them.
			http.Error(w, "could not read the audit log", http.StatusBadRequest)
			return
		}
		respond(w, http.StatusOK, entries)
	})
}
