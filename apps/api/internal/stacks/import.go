package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
)

type importRequest struct {
	AgentID       string `json:"agentId"`
	Directory     string `json:"directory"`
	Token         string `json:"token"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Reviewed      bool   `json:"reviewed"`
	RecoverySaved bool   `json:"recoverySaved"`
}

func importKeyDirectory(c Config) string {
	dir := os.Getenv("VIGIRA_STACK_GIT_DIR")
	if c.Repository != nil {
		dir = c.Repository.Directory
	}
	if dir == "" {
		dir = "/var/lib/vigira/stacks.git"
	}
	return filepath.Join(dir, "import-keys")
}

func (a *API) importKeys() (string, string, error) {
	editor, err := composesecrets.EnsureAgeIdentity(a.CentralAgeIdentity)
	if err != nil {
		return "", "", err
	}
	recovery, err := composesecrets.EnsureAgeIdentity(filepath.Join(importKeyDirectory(a.Config), "recovery.age"))
	return editor, recovery, err
}

func (a *API) enrolledImport(ctx context.Context, agent string) bool {
	var enrolled bool
	err := a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent_identities WHERE id::text=$1 AND revoked_at IS NULL)", agent).Scan(&enrolled)
	return err == nil && enrolled
}

func (a *API) discoverImports(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	if _, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy); !ok {
		return
	}
	var q importRequest
	if !decode(w, r, &q) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !a.enrolledImport(ctx, q.AgentID) {
		http.Error(w, "Choose an enrolled device.", 422)
		return
	}
	var discovery protocol.ComposeDiscovery
	if err := a.Hub.Call(ctx, q.AgentID, protocol.MethodComposeDiscover, nil, &discovery); err != nil {
		http.Error(w, "Discovery requires the updated agent, Docker deployment access, and a readable managed Compose root.", 422)
		return
	}
	bindings, err := a.bindings(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	for i := range discovery.Candidates {
		for _, b := range bindings {
			if b.AgentID == q.AgentID && b.ProjectName == discovery.Candidates[i].ProjectName {
				discovery.Candidates[i].Imported = true
			}
		}
	}
	respond(w, 200, discovery)
}

func (a *API) previewImport(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	u, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy)
	if !ok {
		return
	}
	var q importRequest
	if !decode(w, r, &q) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !a.enrolledImport(ctx, q.AgentID) {
		http.Error(w, "Choose an enrolled device.", 422)
		return
	}
	var preview protocol.ComposeImportPreview
	if err := a.Hub.Call(ctx, q.AgentID, protocol.MethodComposeImportPreview, protocol.ComposeImportRequest{Directory: q.Directory, Token: q.Token}, &preview); err != nil {
		http.Error(w, "The existing stack changed or cannot be imported. Rescan this device and review its files.", 409)
		return
	}
	if _, _, err := a.importKeys(); err != nil {
		http.Error(w, "Could not prepare private import keys. Check the API service's writable Git storage and editor identity.", 422)
		return
	}
	a.record(ctx, u, preview.Candidate.ProjectName, "import-preview", audit.OutcomeSuccess, map[string]any{"agentId": q.AgentID})
	respond(w, 200, preview)
}

func (a *API) importRecovery(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	u, ok := a.Authz.Authorize(w, r, auth.PermSecretsReveal)
	if !ok {
		return
	}
	file := filepath.Join(importKeyDirectory(a.Config), "recovery.age")
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		http.Error(w, "Encrypt a stack environment or review an import before downloading the recovery key.", 409)
		return
	}
	if actual, err := filepath.EvalSymlinks(file); err != nil || actual != file {
		http.Error(w, "Recovery key path is unavailable.", 503)
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		http.Error(w, "Recovery key is unavailable.", 503)
		return
	}
	if a.Audit == nil || a.Audit.Record(r.Context(), audit.Event{ActorID: u.ID, ActorName: u.Username, Action: "stack.import-recovery-download", TargetKind: "repository", TargetID: "import-recovery", Outcome: audit.OutcomeSuccess}) != nil {
		http.Error(w, "Recovery key access could not be audited.", 503)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="vigara-compose-recovery.agekey"`)
	_, _ = w.Write(data)
}

func (a *API) importExisting(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	u, ok := a.Authz.Authorize(w, r, auth.PermStacksDeploy)
	if !ok {
		return
	}
	var q importRequest
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if !stack.ValidID(q.ID) || q.Name == "" || len(q.Name) > 120 || !q.Reviewed || !q.RecoverySaved || len(q.Token) != 64 {
		http.Error(w, "Review the Compose files and storage, save the recovery key, and choose a stack name and unused ID.", 422)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	repo, ok := a.requireRepository(w, ctx)
	if !ok {
		return
	}
	if !a.enrolledImport(ctx, q.AgentID) {
		http.Error(w, "Choose an enrolled device.", 422)
		return
	}
	if _, exists := a.lookupTarget(ctx, q.ID); exists {
		http.Error(w, "This stack is already registered. Open it to finish saving its draft.", 409)
		return
	}
	if _, err = repo.Read(ctx, q.ID, ""); !errors.Is(err, stackgit.ErrNotFound) {
		if err != nil {
			fail(w, err)
		} else {
			http.Error(w, "This stack ID already exists in Git. Choose another ID.", 409)
		}
		return
	}
	editor, recovery, err := a.importKeys()
	if err != nil {
		http.Error(w, "Import encryption keys are unavailable.", 422)
		return
	}
	request := protocol.ComposeImportRequest{Directory: q.Directory, Token: q.Token, ID: q.ID, Name: q.Name, EditorRecipient: editor, RecoveryRecipient: recovery}
	var prepared protocol.ComposeImportPrepared
	if err = a.Hub.Call(ctx, q.AgentID, protocol.MethodComposeImportPrepare, request, &prepared); err != nil {
		http.Error(w, "The device could not prepare this import. Rescan and review the unchanged project, readable environment files, and storage.", 409)
		return
	}
	snapshot := stack.Snapshot{ID: q.ID, Files: prepared.Files}
	manifest, err := stack.Validate(snapshot)
	if err != nil || prepared.Target.ID != q.ID || manifest.ProjectName != prepared.Target.ProjectName || prepared.Target.Directory == "" {
		http.Error(w, "The device returned an invalid import.", 502)
		return
	}
	policy := []composesecrets.RecipientRecord{{Class: composesecrets.CentralEditor, KeyRef: "import-editor", Recipient: editor}, {Class: composesecrets.TargetAgent, KeyRef: "import-agent", Recipient: prepared.AgentRecipient}, {Class: composesecrets.OperatorRecovery, KeyRef: "import-recovery", Recipient: recovery}}
	if composesecrets.ValidateRecipientPolicy(policy) != nil {
		http.Error(w, "The device returned an invalid encryption recipient.", 502)
		return
	}
	allowed := map[string]bool{"vigara-stack.yaml": true}
	for _, path := range manifest.Compose.Files {
		allowed[path] = true
	}
	for _, input := range append(append([]stack.Input{}, manifest.Compose.InterpolationFiles...), manifest.Compose.EnvironmentFiles...) {
		if !composesecrets.IsAgeArtifact([]byte(snapshot.Files[input.Path])) {
			http.Error(w, "Import environment files must be encrypted.", 502)
			return
		}
		plain, err := composesecrets.DecryptDotenv(ctx, "", a.CentralAgeIdentity, []byte(snapshot.Files[input.Path]))
		clear(plain)
		if err != nil {
			http.Error(w, "The imported environment could not be verified with this server's editor key.", 502)
			return
		}
		allowed[input.Path] = true
	}
	for path := range snapshot.Files {
		if !allowed[path] {
			http.Error(w, "The device returned an undeclared import file.", 502)
			return
		}
	}
	var conflict bool
	if err = a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM stack_bindings WHERE agent_id::text=$1 AND (project_name=$2 OR directory=$3))", q.AgentID, prepared.Target.ProjectName, prepared.Target.Directory).Scan(&conflict); err != nil {
		fail(w, err)
		return
	}
	if conflict {
		http.Error(w, "This device project or directory is already registered.", 409)
		return
	}
	var confirmed protocol.ComposeTargetInfo
	if err = a.Hub.Call(ctx, q.AgentID, protocol.MethodComposeImportConfirm, request, &confirmed); err != nil || confirmed != prepared.Target {
		http.Error(w, "The device files or containers changed before import could be confirmed. Nothing was deployed; rescan before retrying.", 409)
		return
	}
	files, _ := json.Marshal(snapshot.Files)
	recipients, _ := json.Marshal(policy)
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO stack_bindings(stack_id,agent_id,project_name,directory,display_name,recipients,imported) VALUES($1,$2,$3,$4,$5,$6,true)", q.ID, q.AgentID, confirmed.ProjectName, confirmed.Directory, q.Name, recipients)
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO stack_drafts(stack_id,user_id,files) VALUES($1,$2,$3)", q.ID, u.ID, files)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		fail(w, err)
		return
	}
	// Preserve a recoverable encrypted draft before pushing. A failed push can
	// be retried through normal Save without importing or recreating containers.
	saved, err := repo.Save(ctx, snapshot, u.Username)
	if err == nil && saved.State == "saved" {
		_, err = a.Pool.Exec(ctx, "DELETE FROM stack_drafts WHERE stack_id=$1 AND user_id=$2 AND version=1", q.ID, u.ID)
	}
	state := "draft"
	if err == nil {
		state = saved.State
	}
	a.record(ctx, u, q.ID, "import", audit.OutcomeSuccess, map[string]any{"agentId": q.AgentID, "projectName": manifest.ProjectName, "state": state, "commit": saved.Commit})
	status := 201
	if state != "saved" {
		status = 202
	}
	respond(w, status, map[string]string{"id": q.ID, "state": state, "commit": saved.Commit, "message": "Existing stack registered without restarting containers. If Git saving is incomplete, open the stack and retry Save."})
}
