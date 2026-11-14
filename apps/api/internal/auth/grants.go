package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/deranjer/vigara/apps/api/internal/audit"
)

const RoleRestricted = "restricted"

// ResourceGrant uses the host and Compose project/service identity, never a
// caller's container name, to retain access across Compose replacements.
type ResourceGrant struct {
	AgentID      string `json:"agentId"`
	Kind         string `json:"kind"`
	Resource     string `json:"resource"`
	Service      string `json:"service,omitempty"`
	Logs         bool   `json:"logs"`
	Restart      bool   `json:"restart"`
	Recreate     bool   `json:"recreate"`
	PullRecreate bool   `json:"pullRecreate"`
}

func (g ResourceGrant) Matches(agent, id, project, service string) bool {
	if g.AgentID != agent {
		return false
	}
	switch g.Kind {
	case "stack":
		return project != "" && project == g.Resource
	case "service":
		return project != "" && project == g.Resource && service == g.Service
	case "container":
		return id != "" && id == g.Resource
	}
	return false
}

func (g ResourceGrant) Allows(action string) bool {
	switch action {
	case "view":
		return true
	case "logs":
		return g.Logs
	case "restart":
		return g.Restart
	case "recreate":
		return g.Recreate
	case "pull-recreate":
		return g.PullRecreate
	}
	return false
}

var resourceRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidateGrants(grants []ResourceGrant) error {
	if len(grants) > 100 {
		return fmt.Errorf("%w: at most 100 resource grants", ErrInvalidInput)
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if !uuidPattern.MatchString(g.AgentID) || !resourceRef.MatchString(g.Resource) {
			return fmt.Errorf("%w: invalid resource identity", ErrInvalidInput)
		}
		if g.Kind != "stack" && g.Kind != "service" && g.Kind != "container" {
			return fmt.Errorf("%w: invalid resource kind", ErrInvalidInput)
		}
		if (g.Kind == "service" && !resourceRef.MatchString(g.Service)) || (g.Kind != "service" && g.Service != "") || (g.Kind == "container" && !containerID.MatchString(g.Resource)) {
			return fmt.Errorf("%w: use a full container ID or Compose service identity", ErrInvalidInput)
		}
		key := g.AgentID + "/" + g.Kind + "/" + g.Resource + "/" + g.Service
		if seen[key] {
			return fmt.Errorf("%w: duplicate resource grant", ErrInvalidInput)
		}
		seen[key] = true
	}
	return nil
}

func (s *Service) ResourceGrants(ctx context.Context, id string) ([]ResourceGrant, error) {
	rows, err := s.pool.Query(ctx, `SELECT agent_id::text,kind,resource,service,logs,restart,recreate,pull_recreate FROM user_resource_grants WHERE user_id=$1::uuid ORDER BY agent_id,kind,resource,service`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ResourceGrant{}
	for rows.Next() {
		var g ResourceGrant
		if err := rows.Scan(&g.AgentID, &g.Kind, &g.Resource, &g.Service, &g.Logs, &g.Restart, &g.Recreate, &g.PullRecreate); err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}

func (s *Service) SetResourceGrants(ctx context.Context, id string, grants []ResourceGrant) error {
	if !uuidPattern.MatchString(id) {
		return fmt.Errorf("%w: invalid user", ErrInvalidInput)
	}
	if err := ValidateGrants(grants); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1::uuid FOR UPDATE`, id).Scan(&role); err != nil {
		return err
	}
	if role != RoleRestricted {
		return fmt.Errorf("%w: resource grants require a selected-resources account", ErrInvalidInput)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_resource_grants WHERE user_id=$1::uuid`, id); err != nil {
		return err
	}
	for _, g := range grants {
		if _, err = tx.Exec(ctx, `INSERT INTO user_resource_grants(user_id,agent_id,kind,resource,service,logs,restart,recreate,pull_recreate) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)`, id, g.AgentID, g.Kind, g.Resource, g.Service, g.Logs, g.Restart, g.Recreate, g.PullRecreate); err != nil {
			return err
		}
	}
	// Sessions and their OAuth grants must not retain superseded access.
	if _, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1::uuid`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) registerGrants(mux *http.ServeMux, recorder *audit.Recorder) {
	mux.HandleFunc("GET /api/users/{id}/resources", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.Authorize(w, r, PermUsersManage); !ok {
			return
		}
		if !uuidPattern.MatchString(r.PathValue("id")) {
			http.Error(w, "invalid user", 400)
			return
		}
		grants, err := s.ResourceGrants(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "could not read resource access", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(grants)
	})
	mux.HandleFunc("PUT /api/users/{id}/resources", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.Authorize(w, r, PermUsersManage)
		if !ok {
			return
		}
		var req struct {
			Grants []ResourceGrant `json:"grants"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid resource grants", 400)
			return
		}
		err := s.SetResourceGrants(r.Context(), r.PathValue("id"), req.Grants)
		outcome := audit.OutcomeSuccess
		if err != nil {
			outcome = audit.OutcomeFailure
		}
		if recorder != nil {
			_ = recorder.Record(r.Context(), audit.Event{ActorID: actor.ID, ActorName: actor.Username, Action: "user.resources", TargetKind: "user", TargetID: r.PathValue("id"), Outcome: outcome})
		}
		if err != nil {
			http.Error(w, "could not save resource access; check the user and selected resources", 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// restrictedRoute is a default-deny boundary for existing and future APIs.
// Individual container handlers still check the target and exact operation.
func restrictedRoute(method, path string) bool {
	switch path {
	case "/api/auth/me", "/api/auth/logout", "/api/auth/password", "/api/auth/login", "/api/version", "/api/setup/status":
		return true
	case "/api/containers":
		return method == http.MethodGet
	}
	parts := strings.Split(path, "/")
	if len(parts) < 6 || parts[1] != "api" || parts[2] != "containers" {
		return false
	}
	if !uuidPattern.MatchString(parts[3]) || !resourceRef.MatchString(parts[4]) {
		return false
	}
	if method == http.MethodGet && len(parts) == 6 {
		switch parts[5] {
		case "inspect", "stats", "logs", "values":
			return true
		}
	}
	if method == http.MethodGet && len(parts) == 7 && parts[5] == "logs" {
		return parts[6] == "snapshot" || parts[6] == "search"
	}
	return method == http.MethodPost && len(parts) == 6 && (parts[5] == "actions" || parts[5] == "recreate")
}
