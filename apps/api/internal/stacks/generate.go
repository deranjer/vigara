package stacks

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/deranjer/vigara/apps/api/internal/audit"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/stack"
	"github.com/deranjer/vigara/internal/stackgit"
)

// Generated secrets are created, merged into the dotenv and encrypted entirely on
// the server. Responses carry variable names and outcomes, never values.

const (
	maxGeneratedVariables = 50
	alphanumeric          = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	// Symbols are limited to characters that are literal in unquoted dotenv values
	// and in Compose interpolation (no $, #, quotes, backslash, space or braces).
	passwordSymbols = "-_.!@^*+=?"
)

var generatedName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type generateVariable struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Length    int    `json:"length,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type generateRequest struct {
	Path      string             `json:"path"`
	Source    environmentSource  `json:"source"`
	Variables []generateVariable `json:"variables"`
}

type generatedResult struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // created, replaced or kept
}

func randomString(alphabet string, n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

// generateValue returns a dotenv-safe value. length meaning depends on kind:
// characters for password/alphanumeric/pin, random bytes for hex/base64/jwt_secret.
func generateValue(kind string, length int) (string, error) {
	between := func(def, lo, hi int) (int, error) {
		if length == 0 {
			return def, nil
		}
		if length < lo || length > hi {
			return 0, fmt.Errorf("length for %s must be between %d and %d", kind, lo, hi)
		}
		return length, nil
	}
	switch kind {
	case "password":
		n, err := between(32, 12, 256)
		if err != nil {
			return "", err
		}
		return randomString(alphanumeric+passwordSymbols, n)
	case "alphanumeric":
		n, err := between(32, 8, 256)
		if err != nil {
			return "", err
		}
		return randomString(alphanumeric, n)
	case "pin":
		n, err := between(6, 4, 32)
		if err != nil {
			return "", err
		}
		return randomString("0123456789", n)
	case "hex":
		n, err := between(32, 8, 128)
		if err != nil {
			return "", err
		}
		b, err := randomBytes(n)
		return hex.EncodeToString(b), err
	case "base64":
		n, err := between(32, 8, 128)
		if err != nil {
			return "", err
		}
		b, err := randomBytes(n)
		return base64.StdEncoding.EncodeToString(b), err
	case "jwt_secret":
		// A signing secret (HS256/HS512), base64url without padding. 32 bytes minimum.
		n, err := between(64, 32, 128)
		if err != nil {
			return "", err
		}
		b, err := randomBytes(n)
		return base64.RawURLEncoding.EncodeToString(b), err
	case "uuid":
		b, err := randomBytes(16)
		if err != nil {
			return "", err
		}
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		h := hex.EncodeToString(b)
		return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
	}
	return "", errors.New("kind must be password, alphanumeric, pin, hex, base64, jwt_secret or uuid")
}

// mergeGenerated sets name=value in a dotenv document, replacing the last
// assignment of that name when present. Existing non-empty values are kept
// unless overwrite is set.
func mergeGenerated(doc, name, value string, overwrite bool) (string, string) {
	lines := strings.Split(doc, "\n")
	assignment := regexp.MustCompile(`^\s*(?:export\s+)?` + regexp.QuoteMeta(name) + `\s*=(.*)$`)
	found := -1
	existing := ""
	for i, line := range lines {
		if m := assignment.FindStringSubmatch(line); m != nil {
			found, existing = i, strings.Trim(strings.TrimSpace(m[1]), `"'`)
		}
	}
	switch {
	case found < 0:
		if doc != "" && !strings.HasSuffix(doc, "\n") {
			doc += "\n"
		}
		return doc + name + "=" + value + "\n", "created"
	case existing != "" && !overwrite:
		return doc, "kept"
	}
	lines[found] = name + "=" + value
	return strings.Join(lines, "\n"), "replaced"
}

func (a *API) generateEnvironment(w http.ResponseWriter, r *http.Request) {
	secretHeaders(w)
	u, ok := a.authorize(w, r, true)
	if !ok {
		return
	}
	var q generateRequest
	if !decode(w, r, &q) {
		return
	}
	if !q.Source.valid() || !validEnvironmentPath(q.Path) || len(q.Variables) == 0 || len(q.Variables) > maxGeneratedVariables {
		http.Error(w, "Select an encrypted file, an exact draft or revision, and 1 to 50 variables.", 422)
		return
	}
	seen := map[string]bool{}
	for _, v := range q.Variables {
		if !generatedName.MatchString(v.Name) || seen[v.Name] {
			http.Error(w, "Variable names must be unique and match [A-Za-z_][A-Za-z0-9_]*.", 422)
			return
		}
		seen[v.Name] = true
		if _, err := generateValue(v.Kind, v.Length); err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
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
	if len(b.Recipients) == 0 {
		b, err = a.ensureEnvironmentRecipients(ctx, s, b)
		if err != nil {
			http.Error(w, errEnvironmentSetup.Error(), 422)
			return
		}
	}
	_, operatorManaged := a.Targets[id]
	existing := s.Files[q.Path] != ""
	if !operatorManaged && !existing && strings.HasSuffix(q.Path, ".sops") && automaticEnvironmentPolicy(b) {
		q.Path = strings.TrimSuffix(q.Path, ".sops") + ".age"
	}
	names := make([]string, 0, len(q.Variables))
	for _, v := range q.Variables {
		names = append(names, v.Name)
	}
	record := func(outcome string) error {
		return a.Audit.Record(ctx, audit.Event{ActorID: u.ID, ActorName: u.Username,
			Action: "stack.environment.generate", TargetKind: "stack", TargetID: id, Outcome: outcome,
			Detail: map[string]any{"path": q.Path, "source": q.Source, "variables": names}})
	}
	var doc []byte
	if existing {
		ciphertext, err := encryptedInput(s, q.Path)
		if err != nil {
			http.Error(w, "Select an encrypted input declared in this version's manifest.", 422)
			return
		}
		if a.CentralAgeIdentity == "" {
			http.Error(w, "Central secret editing is unavailable. Configure the protected editor identity.", 503)
			return
		}
		doc, err = composesecrets.DecryptDotenv(ctx, a.SOPS, a.CentralAgeIdentity, []byte(ciphertext))
		if err != nil || len(doc) > 64<<10 || !utf8.Valid(doc) {
			clear(doc)
			_ = record(audit.OutcomeFailure)
			http.Error(w, "Environment decryption failed. Check the configured editor identity and encrypted input.", 422)
			return
		}
	}
	text := string(doc)
	clear(doc)
	results := make([]generatedResult, 0, len(q.Variables))
	for _, v := range q.Variables {
		value, err := generateValue(v.Kind, v.Length)
		if err != nil {
			_ = record(audit.OutcomeFailure)
			http.Error(w, "Secret generation failed.", 500)
			return
		}
		var status string
		text, status = mergeGenerated(text, v.Name, value, v.Overwrite)
		results = append(results, generatedResult{Name: v.Name, Kind: v.Kind, Status: status})
	}
	if len(text) > 64<<10 {
		_ = record(audit.OutcomeFailure)
		http.Error(w, "The resulting dotenv exceeds 64 KiB.", 422)
		return
	}
	plain := []byte(text)
	text = ""
	defer clear(plain)
	format := "sops-binary"
	if strings.HasSuffix(q.Path, ".age") {
		format = composesecrets.AgeFormat
	}
	ciphertext, err := composesecrets.EncryptForFormat(ctx, a.SOPS, format, plain, b.Recipients)
	if err != nil || len(ciphertext) > 128<<10 {
		_ = record(audit.OutcomeFailure)
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
	if record(audit.OutcomeSuccess) != nil {
		http.Error(w, "Secret generation could not be audited. Try again when audit storage is available.", 503)
		return
	}
	respond(w, 200, map[string]any{"path": q.Path, "ciphertext": string(ciphertext), "manifest": manifest, "source": q.Source, "generated": results})
}
