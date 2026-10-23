// Package stack defines the bounded, portable inputs for Git-backed Compose jobs.
package stack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const MaxBytes = 384 << 10

var IDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var CommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var filePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$`)
var reservedPattern = regexp.MustCompile(`^(COM|LPT)[1-9]$`)
var ErrInvalid = errors.New("invalid or unsupported stack configuration")

type Input struct {
	Path             string `yaml:"path" json:"path"`
	Purpose          string `yaml:"purpose" json:"purpose"`
	Sensitivity      string `yaml:"sensitivity" json:"sensitivity"`
	Format           string `yaml:"format,omitempty" json:"format,omitempty"`
	PreserveOnImport bool   `yaml:"preserveOnImport,omitempty" json:"preserveOnImport,omitempty"`
}
type RecipientRef struct {
	Class  string `yaml:"class"`
	KeyRef string `yaml:"keyRef"`
}
type Manifest struct {
	APIVersion  string `yaml:"apiVersion"`
	Kind        string `yaml:"kind"`
	StackID     string `yaml:"stackId"`
	DisplayName string `yaml:"displayName"`
	ProjectName string `yaml:"projectName"`
	Compose     struct {
		Files                       []string `yaml:"files"`
		ProjectDirectory            string   `yaml:"projectDirectory"`
		InterpolationFiles          []Input  `yaml:"interpolationFiles"`
		ControlEnvironmentAllowlist []string `yaml:"controlEnvironmentAllowlist,omitempty"`
	} `yaml:"compose"`
	SecretRecipientRefs         []RecipientRef `yaml:"secretRecipientRefs,omitempty"`
	ConfigurationRevisionPolicy string         `yaml:"configurationRevisionPolicy"`
	Notes                       string         `yaml:"notes,omitempty"`
}
type Snapshot struct {
	ID     string            `json:"id"`
	Commit string            `json:"commit"`
	Files  map[string]string `json:"files"`
}
type Job struct {
	ID       string   `json:"id"`
	Snapshot Snapshot `json:"snapshot"`
	Pull     bool     `json:"pull"`
}
type Result struct {
	Status  string            `json:"status"`
	Stage   string            `json:"stage"`
	Message string            `json:"message,omitempty"`
	Images  map[string]string `json:"images,omitempty"`
}

func ValidProject(name string) bool { return projectPattern.MatchString(name) }

func ValidID(id string) bool { return len(id) <= 63 && IDPattern.MatchString(id) }
func ValidPath(p string) bool {
	if len(p) > 240 || !filePattern.MatchString(p) || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || strings.HasPrefix(strings.ToLower(part), ".git") || strings.HasPrefix(strings.ToLower(part), ".vigara") || strings.HasSuffix(part, ".") {
			return false
		}
		// Portable files must also be safe on Windows.
		base := strings.ToUpper(strings.Split(part, ".")[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || reservedPattern.MatchString(base) {
			return false
		}
	}
	return true
}

// Validate checks portable metadata and raw YAML. Compose itself performs semantic validation on the target.
// Unsupported manifests are rejected, never partially interpreted.
func Validate(s Snapshot) (Manifest, error) {
	var m Manifest
	if !ValidID(s.ID) || len(s.Files) == 0 || len(s.Files) > 64 {
		return m, ErrInvalid
	}
	size := 0
	folded := map[string]bool{}
	for p, v := range s.Files {
		if !ValidPath(p) || !utf8.ValidString(v) || strings.ContainsRune(v, 0) || len(v) > 128<<10 || folded[strings.ToLower(p)] {
			return m, ErrInvalid
		}
		folded[strings.ToLower(p)] = true
		size += len(p) + len(v)
		for q := range s.Files {
			if strings.HasPrefix(strings.ToLower(q), strings.ToLower(p)+"/") {
				return m, ErrInvalid
			}
		}
	}
	if size > MaxBytes {
		return m, ErrInvalid
	}
	encoded, _ := json.Marshal(s)
	if len(encoded) > 768<<10 {
		return m, ErrInvalid
	}
	dec := yaml.NewDecoder(strings.NewReader(s.Files["vigara-stack.yaml"]))
	dec.KnownFields(true)
	if dec.Decode(&m) != nil || dec.Decode(new(any)) != io.EOF || m.APIVersion != "vigara.io/v1" || m.Kind != "Stack" || m.StackID != s.ID || len(m.DisplayName) == 0 || len(m.DisplayName) > 120 || !projectPattern.MatchString(m.ProjectName) || m.Compose.ProjectDirectory != "." || len(m.Compose.Files) == 0 || len(m.Compose.ControlEnvironmentAllowlist) > 0 || m.ConfigurationRevisionPolicy != "sha256-git-tree-and-placement-inputs-v1" || len(m.Notes) > 2000 {
		return m, ErrInvalid
	}
	seen := map[string]bool{}
	hasServices := false
	for _, p := range m.Compose.Files {
		if !ValidPath(p) || seen[p] || s.Files[p] == "" {
			return m, ErrInvalid
		}
		seen[p] = true
		var doc map[string]any
		d := yaml.NewDecoder(strings.NewReader(s.Files[p]))
		if d.Decode(&doc) != nil || d.Decode(new(any)) != io.EOF || doc == nil {
			return m, ErrInvalid
		}
		if doc["services"] != nil {
			hasServices = true
		}
	}
	if !hasServices {
		return m, ErrInvalid
	}
	for _, input := range m.Compose.InterpolationFiles {
		if !ValidPath(input.Path) || seen[input.Path] || s.Files[input.Path] == "" || input.Sensitivity != "encrypted" || (input.Format != "sops-binary" && input.Format != "sops-dotenv" && input.Format != "") || (input.Purpose != "stack" && input.Purpose != "shared" && input.Purpose != "deployment") {
			return m, ErrInvalid
		}
		seen[input.Path] = true
		if !strings.HasSuffix(input.Path, ".sops") {
			return m, ErrInvalid
		}
	}
	// Dotenv inputs must be encrypted; do not accidentally commit plaintext environment files.
	for p := range s.Files {
		if strings.ToLower(path.Base(p)) == ".env" || strings.HasSuffix(strings.ToLower(p), ".env") {
			return m, ErrInvalid
		}
	}
	if len(m.Compose.InterpolationFiles) > 0 {
		classes := map[string]int{}
		refs := map[string]bool{}
		for _, ref := range m.SecretRecipientRefs {
			if ref.Class != "central-editor" && ref.Class != "target-agent" && ref.Class != "operator-recovery" || !regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{1,127}$`).MatchString(ref.KeyRef) || refs[ref.Class+":"+ref.KeyRef] {
				return m, ErrInvalid
			}
			classes[ref.Class]++
			refs[ref.Class+":"+ref.KeyRef] = true
		}
		if classes["central-editor"] != 1 || classes["operator-recovery"] != 1 || classes["target-agent"] < 1 {
			return m, ErrInvalid
		}
	}
	return m, nil
}

// Digest identifies all captured stack files without decoding encrypted inputs.
// Placement fencing is provided separately by the immutable local target binding in this first runner.
func Digest(s Snapshot) string {
	keys := make([]string, 0, len(s.Files))
	for p := range s.Files {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte("VIGARA-STACK-FILES-V1\x00"))
	for _, p := range keys {
		b, _ := json.Marshal([]string{p, s.Files[p]})
		h.Write(b)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func Equal(a, b map[string]string) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

type Plan struct {
	Token       string   `json:"token"`
	Services    []string `json:"services"`
	ProjectName string   `json:"projectName"`
}
type DeployRequest struct {
	Job   Job    `json:"job"`
	Token string `json:"token"`
}
