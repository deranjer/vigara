package composedeploy

import (
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

// EnvironmentRecipient provisions only a locally approved target's encryption
// identity. The private identity remains on this host; no Compose action occurs.
func (r *Runner) EnvironmentRecipient(q protocol.ComposeCreateTarget) (protocol.ComposeRecipient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !stack.ValidID(q.ID) || !stack.ValidProject(q.ProjectName) {
		return protocol.ComposeRecipient{}, stack.ErrInvalid
	}
	target, ok := r.lookup(q.ID)
	if !ok || target.ProjectName != q.ProjectName {
		return protocol.ComposeRecipient{}, errTarget
	}
	key, err := composesecrets.EnsureAgeIdentity(r.identityPath())
	return protocol.ComposeRecipient{Recipient: key}, err
}
