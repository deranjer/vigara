package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ChangePassword requires both a live session and the exact current password.
// The password update and replacement of every session are one transaction.
func (s *Service) ChangePassword(ctx context.Context, token, current, password, confirmation string) (Session, error) {
	if token == "" || len(token) > 128 {
		return Session{}, pgx.ErrNoRows
	}
	if len(current) == 0 || len(current) > 1024 {
		return Session{}, ErrInvalidCredentials
	}
	if len(password) < 12 || len(password) > 1024 {
		return Session{}, fmt.Errorf("%w: new password must be between 12 and 1024 characters", ErrInvalidInput)
	}
	if password != confirmation {
		return Session{}, fmt.Errorf("%w: new passwords do not match", ErrInvalidInput)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin password change: %w", err)
	}
	defer tx.Rollback(ctx)
	var user User
	var previousHash string
	err = tx.QueryRow(ctx, `SELECT u.id::text,u.username,u.display_name,u.role,u.password_hash
 FROM users u JOIN user_sessions us ON us.user_id=u.id
 WHERE us.token_hash=$1 AND us.expires_at>now() FOR UPDATE OF u`, hashSessionToken(token)).Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &previousHash)
	if err != nil {
		return Session{}, fmt.Errorf("read password-change account: %w", err)
	}
	// A concurrent password change may have revoked this session while the row lock waited.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_sessions WHERE user_id=$1::uuid AND token_hash=$2 AND expires_at>now())`, user.ID, hashSessionToken(token)).Scan(&active); err != nil {
		return Session{}, err
	}
	if !active {
		return Session{}, pgx.ErrNoRows
	}
	if !verifyPassword(current, previousHash) {
		return Session{}, ErrInvalidCredentials
	}
	nextHash, err := hashPassword(password)
	if err != nil {
		return Session{}, fmt.Errorf("hash replacement password: %w", err)
	}
	nextToken, tokenHash, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	expires := time.Now().UTC().Add(sessionLifetime)
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1::uuid`, user.ID, nextHash); err != nil {
		return Session{}, fmt.Errorf("update password: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1::uuid`, user.ID); err != nil {
		return Session{}, fmt.Errorf("revoke password-change sessions: %w", err)
	}
	var id pgtype.UUID
	if err = id.Scan(user.ID); err != nil {
		return Session{}, err
	}
	if err = s.queries.WithTx(tx).CreateSession(ctx, db.CreateSessionParams{UserID: id, TokenHash: tokenHash, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}}); err != nil {
		return Session{}, fmt.Errorf("create replacement session: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit password change: %w", err)
	}
	return Session{User: user, Token: nextToken, ExpiresAt: expires}, nil
}

type ChangePasswordInput struct {
	Cookie string `header:"Cookie"`
	Body   struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		ConfirmPassword string `json:"confirmPassword"`
	}
}

func registerPasswordChange(api huma.API, s *Service, secure bool, recorder *audit.Recorder) {
	huma.Register(api, huma.Operation{OperationID: "changeOwnPassword", Method: http.MethodPost, Path: "/api/auth/password", Summary: "Change the signed-in user's password", Tags: []string{"Authentication"}, MaxBodyBytes: 16 << 10}, func(ctx context.Context, input *ChangePasswordInput) (*UserOutput, error) {
		user, authErr, ok := userFromContext(ctx)
		if authErr != nil {
			return nil, huma.Error500InternalServerError("Could not validate login session")
		}
		if !ok {
			return nil, huma.Error401Unauthorized("Login required")
		}
		r := &http.Request{Header: make(http.Header)}
		r.Header.Set("Cookie", input.Cookie)
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			return nil, huma.Error401Unauthorized("Login required")
		}
		session, err := s.ChangePassword(ctx, cookie.Value, input.Body.CurrentPassword, input.Body.NewPassword, input.Body.ConfirmPassword)
		outcome := audit.OutcomeSuccess
		if err != nil {
			outcome = audit.OutcomeFailure
		}
		_ = recorder.Record(ctx, audit.Event{ActorID: user.ID, ActorName: user.Username, Action: "user.password.change", TargetKind: "user", TargetID: user.ID, Outcome: outcome})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, huma.Error401Unauthorized("Your session expired. Log in again.")
		case errors.Is(err, ErrInvalidCredentials):
			return nil, huma.Error400BadRequest("Current password is incorrect.")
		case errors.Is(err, ErrInvalidInput):
			return nil, huma.Error400BadRequest(err.Error())
		case err != nil:
			return nil, huma.Error500InternalServerError("Could not change password")
		}
		return userOutput(session, secure), nil
	})
}
