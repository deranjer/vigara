package stacks

import (
	"context"
	"net/http"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

// Registry access runs on the assigned device, using that device's Docker login.
func (a *API) imageTags(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authorize(w, r, true); !ok {
		return
	}
	var q protocol.ComposeImageTagsRequest
	if !decode(w, r, &q) {
		return
	}
	if q.Image == "" || len(q.Image) > 512 {
		http.Error(w, "Choose an image repository.", http.StatusUnprocessableEntity)
		return
	}
	b, ok := a.lookupTarget(r.Context(), r.PathValue("id"))
	if !ok {
		http.Error(w, "Assign this stack to a device first.", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var result protocol.ComposeImageTags
	if err := a.Hub.Call(ctx, b.AgentID, protocol.MethodComposeImageTags, q, &result); err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}
