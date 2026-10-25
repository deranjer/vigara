// Package placement validates portable server profiles and stack assignments.
// It does not bind agents, transfer ownership, provision storage, or deploy stacks.
package placement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deranjer/vigara/internal/stack"
	schemas "github.com/deranjer/vigara/schemas/compose-management"
	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

const MaxFiles = 256
const MaxFileBytes = 64 << 10
const MaxBytes = 512 << 10

var ErrInvalid = errors.New("invalid profile or assignment manifest")

type Profile struct {
	ID           string         `json:"profileId"`
	DisplayName  string         `json:"displayName"`
	Requirements map[string]any `json:"requirements"`
}

type Assignment struct {
	ID          string `json:"assignmentId"`
	StackID     string `json:"stackId"`
	ProfileID   string `json:"profileId"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
	Ownership   struct {
		Mode  string `json:"mode"`
		Epoch uint64 `json:"ownershipEpoch"`
	} `json:"ownership"`
	ReplicaKey  string               `json:"replicaKey,omitempty"`
	ProjectName string               `json:"projectNameOverride,omitempty"`
	Inputs      []stack.Input        `json:"interpolationFiles,omitempty"`
	Recipients  []stack.RecipientRef `json:"secretRecipientRefs,omitempty"`
}

type Catalog struct {
	Profiles    map[string]Profile    `json:"profiles"`
	Assignments map[string]Assignment `json:"assignments"`
}

var validators = sync.OnceValues(func() (map[string]*jsonschema.Resolved, error) {
	result := map[string]*jsonschema.Resolved{}
	for _, name := range []string{"server-profile", "assignment"} {
		data, err := schemas.Schemas.ReadFile(name + ".schema.json")
		if err != nil {
			return nil, err
		}
		var schema jsonschema.Schema
		if err = json.Unmarshal(data, &schema); err != nil {
			return nil, err
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, err
		}
		result[name] = resolved
	}
	return result, nil
})

// ValidPath restricts all repository writes to the two manifest namespaces.
func ValidPath(path string) bool {
	if !stack.ValidPath(path) {
		return false
	}
	p := strings.Split(path, "/")
	return len(p) == 3 && ((p[0] == "profiles" && stack.ValidID(p[1]) && p[2] == "vigara-profile.yaml") ||
		(p[0] == "assignments" && stack.ValidID(p[1]) && strings.HasSuffix(p[2], ".yaml") && stack.ValidID(strings.TrimSuffix(p[2], ".yaml"))))
}

// Parse validates schemas and local semantics while preserving the original YAML
// in the caller's file map. Errors never include manifest values or parser output.
func Parse(files map[string]string) (Catalog, error) {
	c := Catalog{Profiles: map[string]Profile{}, Assignments: map[string]Assignment{}}
	if len(files) > MaxFiles {
		return c, ErrInvalid
	}
	vs, err := validators()
	if err != nil {
		return c, fmt.Errorf("%w: schema unavailable", ErrInvalid)
	}
	total := 0
	for path, text := range files {
		total += len(path) + len(text)
		if !ValidPath(path) || len(text) > MaxFileBytes || total > MaxBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return c, ErrInvalid
		}
		var node yaml.Node
		dec := yaml.NewDecoder(strings.NewReader(text))
		if dec.Decode(&node) != nil || dec.Decode(new(any)) != io.EOF || !plainYAML(&node) {
			return c, ErrInvalid
		}
		var value map[string]any
		if node.Decode(&value) != nil || value == nil {
			return c, ErrInvalid
		}
		data, err := json.Marshal(value)
		if err != nil {
			return c, ErrInvalid
		}
		// Validate JSON-shaped values, including the presence of required booleans.
		var instance any
		if json.Unmarshal(data, &instance) != nil {
			return c, ErrInvalid
		}
		parts := strings.Split(path, "/")
		if parts[0] == "profiles" {
			var p Profile
			if vs["server-profile"].Validate(instance) != nil || json.Unmarshal(data, &p) != nil || p.ID != parts[1] {
				return c, ErrInvalid
			}
			c.Profiles[p.ID] = p
		} else {
			var a Assignment
			if vs["assignment"].Validate(instance) != nil || json.Unmarshal(data, &a) != nil || a.ProfileID != parts[1] || a.StackID+".yaml" != parts[2] {
				return c, ErrInvalid
			}
			if _, exists := c.Assignments[a.ID]; exists {
				return c, ErrInvalid
			}
			seen := map[string]bool{}
			encrypted := false
			for _, input := range a.Inputs {
				if !stack.ValidPath(input.Path) || seen[input.Path] || input.Purpose != "deployment" {
					return c, ErrInvalid
				}
				seen[input.Path] = true
				if input.Sensitivity == "encrypted" {
					encrypted = true
					if !strings.HasSuffix(input.Path, ".sops") {
						return c, ErrInvalid
					}
				}
			}
			if encrypted || len(a.Recipients) > 0 {
				classes := map[string]bool{}
				for _, ref := range a.Recipients {
					if classes[ref.Class] {
						return c, ErrInvalid
					}
					classes[ref.Class] = true
				}
				if len(classes) != 3 || !classes["central-editor"] || !classes["target-agent"] || !classes["operator-recovery"] {
					return c, ErrInvalid
				}
			}
			c.Assignments[a.ID] = a
		}
	}
	return c, nil
}

func plainYAML(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return false
	}
	for _, child := range n.Content {
		if !plainYAML(child) {
			return false
		}
	}
	return true
}

// Validate checks references and simultaneous placements against saved stack
// definitions from the SAME Git commit. Disabled assignments retain valid
// references but do not claim active ownership or project names.
func Validate(files map[string]string, stacks map[string]stack.Snapshot) (Catalog, error) {
	c, err := Parse(files)
	if err != nil {
		return c, err
	}
	byStack := map[string][]Assignment{}
	projects := map[string]bool{}
	for _, a := range c.Assignments {
		if _, ok := c.Profiles[a.ProfileID]; !ok {
			return c, fmt.Errorf("%w: assignment references a missing profile", ErrInvalid)
		}
		s, ok := stacks[a.StackID]
		if !ok {
			return c, fmt.Errorf("%w: assignment references a missing stack", ErrInvalid)
		}
		m, e := stack.Validate(s)
		if e != nil {
			return c, fmt.Errorf("%w: assigned stack is invalid or unsupported", ErrInvalid)
		}
		paths := map[string]bool{}
		for _, p := range m.Compose.Files {
			paths[p] = true
		}
		for _, input := range m.Compose.InterpolationFiles {
			paths[input.Path] = true
		}
		for _, input := range a.Inputs {
			if paths[input.Path] || s.Files[input.Path] == "" {
				return c, fmt.Errorf("%w: missing or duplicate assignment input", ErrInvalid)
			}
			paths[input.Path] = true
		}
		if !a.Enabled {
			continue
		}
		project := a.ProjectName
		if project == "" {
			project = m.ProjectName
		}
		key := a.ProfileID + "/" + project
		if projects[key] {
			return c, fmt.Errorf("%w: project name already assigned on this profile", ErrInvalid)
		}
		projects[key] = true
		for _, other := range byStack[a.StackID] {
			otherProject := other.ProjectName
			if otherProject == "" {
				otherProject = m.ProjectName
			}
			if a.Ownership.Mode != "stateless" || other.Ownership.Mode != "stateless" || a.ReplicaKey == other.ReplicaKey || project == otherProject || a.ProfileID == other.ProfileID {
				return c, fmt.Errorf("%w: concurrent stack ownership conflicts", ErrInvalid)
			}
		}
		byStack[a.StackID] = append(byStack[a.StackID], a)
	}
	// Startup edges reference assignments on the same profile, and must
	// be acyclic. They express order, never an implicit multi-stack deployment.
	for id, p := range c.Profiles {
		active := map[string]bool{}
		for _, a := range c.Assignments {
			if a.ProfileID == id {
				active[a.StackID] = true
			}
		}
		edges := map[string][]string{}
		if dependencies, ok := p.Requirements["startupDependencies"].([]any); ok {
			for _, item := range dependencies {
				d := item.(map[string]any)
				before := d["before"].(string)
				after := d["after"].(string)
				if !active[before] || !active[after] {
					return c, fmt.Errorf("%w: startup dependency references an unassigned stack", ErrInvalid)
				}
				edges[before] = append(edges[before], after)
			}
		}
		state := map[string]int{}
		var visit func(string) bool
		visit = func(id string) bool {
			if state[id] == 1 {
				return false
			}
			if state[id] == 2 {
				return true
			}
			state[id] = 1
			for _, next := range edges[id] {
				if !visit(next) {
					return false
				}
			}
			state[id] = 2
			return true
		}
		for id := range edges {
			if !visit(id) {
				return c, fmt.Errorf("%w: startup dependencies contain a cycle", ErrInvalid)
			}
		}
	}
	return c, nil
}
