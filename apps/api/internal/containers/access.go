package containers

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

type grantReader interface {
	ResourceGrants(context.Context, string) ([]auth.ResourceGrant, error)
}

var fullContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (a *api) grants(ctx context.Context, user auth.User) ([]auth.ResourceGrant, error) {
	if a.Grants != nil {
		return a.Grants.ResourceGrants(ctx, user.ID)
	}
	if reader, ok := a.Authz.(grantReader); ok {
		return reader.ResourceGrants(ctx, user.ID)
	}
	// Missing wiring must fail closed, including private MCP handler copies.
	return nil, nil
}

func allowed(grants []auth.ResourceGrant, agent, id, project, service, operation string) bool {
	for _, g := range grants {
		if g.Matches(agent, id, project, service) && g.Allows(operation) {
			return true
		}
	}
	return false
}

func (a *api) resourceAllowed(ctx context.Context, user auth.User, agent, id, operation string) bool {
	if user.Role != auth.RoleRestricted {
		return true
	}
	// Names can be reassigned between authorization and execution. Restricted
	// requests always use the immutable, full ID listed by the fleet endpoint.
	if !fullContainerID.MatchString(id) {
		return false
	}
	grants, err := a.grants(ctx, user)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	var meta protocol.DockerInspectResult
	if a.Hub.Call(ctx, agent, protocol.MethodDockerInspect, protocol.DockerInspectParams{ID: id}, &meta) != nil {
		return false
	}
	return meta.ID == id && allowed(grants, agent, meta.ID, meta.Labels["com.docker.compose.project"], meta.Labels["com.docker.compose.service"], operation)
}

func (a *api) requireResource(w http.ResponseWriter, r *http.Request, user auth.User, agent, id, operation string) bool {
	if a.resourceAllowed(r.Context(), user, agent, id, operation) {
		return true
	}
	http.Error(w, "resource not found or access is not allowed", http.StatusNotFound)
	return false
}

// A short-lived recheck closes existing streams when a session or grant is revoked.
func (a *api) streamStillAllowed(r *http.Request, user auth.User, agent, id, operation string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if verifier, ok := a.Authz.(interface {
		UserFromRequest(*http.Request) (auth.User, bool)
	}); ok {
		current, valid := verifier.UserFromRequest(r.WithContext(ctx))
		if !valid || current.Role != user.Role {
			return false
		}
		user = current
	}
	return a.resourceAllowed(ctx, user, agent, id, operation)
}
