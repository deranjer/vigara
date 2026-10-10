-- name: GetSetupState :one
SELECT completed_at
FROM app_setup
WHERE singleton_id = 1;

-- name: LockSetupState :one
SELECT completed_at
FROM app_setup
WHERE singleton_id = 1
FOR UPDATE;

-- name: CountUsers :one
SELECT count(*)::bigint
FROM users;

-- name: CreateFirstUser :one
INSERT INTO users (username, display_name, password_hash, role)
VALUES ($1, $2, $3, 'admin')
RETURNING id::text AS id, username, display_name, role, created_at;

-- name: CompleteSetup :exec
UPDATE app_setup
SET completed_at = now()
WHERE singleton_id = 1;

-- name: GetUserByUsername :one
SELECT id::text AS id, username, display_name, password_hash, role, created_at
FROM users
WHERE username = $1;

-- name: CreateSession :exec
INSERT INTO user_sessions (user_id, token_hash, expires_at)
VALUES ($1, $2, $3);

-- name: GetUserBySessionHash :one
SELECT u.id::text AS id, u.username, u.display_name, u.role
FROM user_sessions AS s
JOIN users AS u ON u.id = s.user_id
WHERE s.token_hash = $1
  AND s.expires_at > now();

-- name: DeleteSessionByHash :exec
DELETE FROM user_sessions
WHERE token_hash = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM user_sessions
WHERE expires_at <= now();
