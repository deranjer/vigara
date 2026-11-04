package stacks

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

var errEnvironmentSetup = errors.New("automatic environment encryption needs an online, updated deployment agent and writable private key storage; existing operator-managed policies must be configured by the operator")

func automaticEnvironmentPolicy(b Binding) bool {
	if len(b.Recipients) != 3 {
		return false
	}
	refs := map[composesecrets.RecipientClass]string{composesecrets.CentralEditor: "import-editor", composesecrets.TargetAgent: "import-agent", composesecrets.OperatorRecovery: "import-recovery"}
	for _, recipient := range b.Recipients {
		if refs[recipient.Class] != recipient.KeyRef {
			return false
		}
	}
	return composesecrets.ValidateRecipientPolicy(b.Recipients) == nil
}

// Set up a new managed stack on first encryption, including stacks created by
// older versions. Existing policies and encrypted revisions are never replaced.
// The caller holds the repository lock; subsequent edits work with the host offline.
func (a *API) ensureEnvironmentRecipients(ctx context.Context, s stack.Snapshot, b Binding) (Binding, error) {
	if len(b.Recipients) != 0 {
		return b, nil
	}
	m, err := stack.Validate(s)
	if err != nil || len(m.SecretRecipientRefs) != 0 || a.Hub == nil || a.Pool == nil || b.Directory == "" {
		return b, errEnvironmentSetup
	}
	if _, configured := a.Targets[s.ID]; configured {
		return b, errEnvironmentSetup
	}
	editor, recovery, err := a.importKeys()
	if err != nil {
		return b, errEnvironmentSetup
	}
	var agent protocol.ComposeRecipient
	if err = a.Hub.Call(ctx, b.AgentID, protocol.MethodComposeRecipient, protocol.ComposeCreateTarget{ID: s.ID, ProjectName: b.ProjectName}, &agent); err != nil {
		return b, errEnvironmentSetup
	}
	policy := []composesecrets.RecipientRecord{
		{Class: composesecrets.CentralEditor, KeyRef: "import-editor", Recipient: editor},
		{Class: composesecrets.TargetAgent, KeyRef: "import-agent", Recipient: agent.Recipient},
		{Class: composesecrets.OperatorRecovery, KeyRef: "import-recovery", Recipient: recovery},
	}
	if composesecrets.ValidateRecipientPolicy(policy) != nil {
		return b, errEnvironmentSetup
	}
	raw, _ := json.Marshal(policy)
	result, err := a.Pool.Exec(ctx, `UPDATE stack_bindings SET recipients=$2 WHERE stack_id=$1 AND agent_id::text=$3 AND project_name=$4 AND directory=$5 AND recipients='[]'::jsonb`, s.ID, raw, b.AgentID, b.ProjectName, b.Directory)
	if err != nil || result.RowsAffected() != 1 {
		return b, errEnvironmentSetup
	}
	b.Recipients = policy
	return b, nil
}
