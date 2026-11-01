package stacks

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
	"github.com/jackc/pgx/v5"
)

// A source selects server-owned ciphertext, never a client-supplied document or
// filesystem path. Exactly one immutable revision or user-owned draft is required.
type environmentSource struct {
	Commit  string `json:"commit,omitempty"`
	Version int64  `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

func (s environmentSource) valid() bool {
	if s.Version > 0 && s.Commit == "" && len(s.Digest) == 71 && strings.HasPrefix(s.Digest, "sha256:") {
		_, err := hex.DecodeString(s.Digest[7:])
		return err == nil
	}
	return s.Version == 0 && s.Digest == "" && stack.CommitPattern.MatchString(s.Commit)
}

type environmentRequest struct {
	Path   string            `json:"path"`
	Source environmentSource `json:"source"`
	Value  *string           `json:"value,omitempty"`
}

func secretHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func validEnvironmentPath(path string) bool {
	return stack.ValidPath(path) && strings.HasSuffix(path, ".sops")
}

func (a *API) environmentSnapshot(ctx context.Context, id, user string, source environmentSource) (stack.Snapshot, error) {
	if !source.valid() {
		return stack.Snapshot{}, stack.ErrInvalid
	}
	if source.Version > 0 {
		s, err := a.readDraft(ctx, id, user, source.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return s, stackgit.ErrConflict
		}
		if err == nil && stack.Digest(s) != source.Digest {
			return s, stackgit.ErrConflict
		}
		return s, err
	}
	repo, err := a.repository(ctx)
	if err != nil {
		return stack.Snapshot{}, err
	}
	if repo == nil {
		return stack.Snapshot{}, stackgit.ErrNotFound
	}
	return repo.Read(ctx, id, source.Commit)
}

func encryptedInput(s stack.Snapshot, path string) (string, error) {
	if !validEnvironmentPath(path) {
		return "", stack.ErrInvalid
	}
	m, err := stack.Validate(s)
	if err != nil {
		return "", err
	}
	for _, input := range m.Compose.InterpolationFiles {
		if input.Path == path && input.Sensitivity == "encrypted" {
			return s.Files[path], nil
		}
	}
	return "", stack.ErrInvalid
}

// Secret operations require a durable metadata-only audit entry. No plaintext,
// ciphertext, diagnostics or private identity path enters the event.
func (a *API) secretAudit(ctx context.Context, user auth.User, id, action, outcome string, q environmentRequest) error {
	if a.Audit == nil {
		return errors.New("secret audit unavailable")
	}
	return a.Audit.Record(ctx, audit.Event{ActorID: user.ID, ActorName: user.Username,
		Action: "stack.environment." + action, TargetKind: "stack", TargetID: id, Outcome: outcome,
		Detail: map[string]any{"path": q.Path, "source": q.Source}})
}

func (a *API) revealEnvironment(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	u, ok := a.authorize(w, r, false)
	if !ok {
		return
	}
	var q environmentRequest
	if !decode(w, r, &q) {
		return
	}
	if !q.Source.valid() || !validEnvironmentPath(q.Path) || q.Value != nil {
		http.Error(w, "Select an encrypted input and an exact saved draft or revision.", 422)
		return
	}
	id := r.PathValue("id")
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	// Serialize source reads with edits so the version is checked before decryption.
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	s, err := a.environmentSnapshot(ctx, id, u.ID, q.Source)
	if err != nil {
		fail(w, err)
		return
	}
	ciphertext, err := encryptedInput(s, q.Path)
	if err != nil {
		http.Error(w, "Select an encrypted interpolation input declared in this version's manifest.", 422)
		return
	}
	if a.CentralAgeIdentity == "" || a.Audit == nil {
		http.Error(w, "Central secret editing is unavailable. Configure the protected editor identity and audit storage.", 503)
		return
	}
	plain, err := composesecrets.DecryptDotenv(ctx, a.SOPS, a.CentralAgeIdentity, []byte(ciphertext))
	defer clear(plain)
	if err != nil || len(plain) > 64<<10 || !utf8.Valid(plain) {
		_ = a.secretAudit(ctx, u, id, "reveal", audit.OutcomeFailure, q)
		http.Error(w, "Environment reveal failed. Check the configured editor identity and encrypted input.", 422)
		return
	}
	if a.secretAudit(ctx, u, id, "reveal", audit.OutcomeSuccess, q) != nil {
		http.Error(w, "Secret access could not be audited. Try again when audit storage is available.", 503)
		return
	}
	respond(w, 200, map[string]any{"path": q.Path, "source": q.Source, "value": string(plain)})
}

func (a *API) editEnvironment(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q environmentRequest
	if !decode(w, r, &q) {
		return
	}
	if !q.Source.valid() || !validEnvironmentPath(q.Path) || q.Value == nil || len(*q.Value) > 64<<10 || !utf8.ValidString(*q.Value) {
		http.Error(w, "Select an encrypted file, exact draft or revision, and dotenv contents up to 64 KiB.", 422)
		return
	}
	id := r.PathValue("id")
	ctx, cancel := stackgit.DefaultTimeout(r.Context())
	defer cancel()
	unlock, err := a.repoLock(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	s, err := a.environmentSnapshot(ctx, id, u.ID, q.Source)
	if err != nil {
		fail(w, err)
		return
	}
	if a.Audit == nil {
		http.Error(w, "Secret editing requires audit storage.", 503)
		return
	}
	b, ok := a.lookupTarget(ctx, id)
	if !ok {
		http.Error(w, "Stack recipient policy is not configured.", 422)
		return
	}
	plain := []byte(*q.Value)
	q.Value = nil
	defer clear(plain)
	ciphertext, err := composesecrets.EncryptForPolicy(ctx, a.SOPS, plain, b.Recipients)
	if err != nil || len(ciphertext) > 128<<10 {
		_ = a.secretAudit(ctx, u, id, "edit", audit.OutcomeFailure, q)
		http.Error(w, "Environment encryption failed. Configure editor, target and recovery recipients.", 422)
		return
	}
	refs := []stack.RecipientRef{}
	for _, recipient := range b.Recipients {
		refs = append(refs, stack.RecipientRef{Class: string(recipient.Class), KeyRef: recipient.KeyRef})
	}
	manifest, err := stack.WithEncryptedInput(s.Files["vigara-stack.yaml"], q.Path, refs)
	if err != nil {
		http.Error(w, "The stack manifest must contain a valid compose section before adding environment inputs.", 422)
		return
	}
	// The response returns encrypted bytes only; saving still uses the source's
	// optimistic draft version and cannot silently overwrite another browser.
	if a.secretAudit(ctx, u, id, "edit", audit.OutcomeSuccess, q) != nil {
		http.Error(w, "Secret editing could not be audited. Try again when audit storage is available.", 503)
		return
	}
	respond(w, 200, map[string]any{"path": q.Path, "ciphertext": string(ciphertext), "manifest": manifest, "source": q.Source})
}
