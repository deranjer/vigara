// Package composerecovery creates metadata-only, offline recovery plans.
// A plan is never an instruction stream and cannot invoke Docker or restore tools.
package composerecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/placement"
	"github.com/deranjer/vigara/internal/stack"
	"gopkg.in/yaml.v3"
)

const (
	maxRepoBytes     = 2 << 20
	maxFileBytes     = 128 << 10
	maxSnapshotBytes = 64 << 10
)

var ErrInvalid = errors.New("invalid recovery-plan input")
var errOutputLimit = errors.New("recovery input exceeds size limit")

type SnapshotMetadata struct {
	ID               string            `json:"snapshotId"`
	ProfileID        string            `json:"profileId"`
	BackupReferences map[string]string `json:"backupReferences"`
	RecoveryKeyRefs  []string          `json:"recoveryKeyRefs,omitempty"`
}

type Plan struct {
	APIVersion    string               `json:"apiVersion"`
	Kind          string               `json:"kind"`
	Status        string               `json:"status"`
	Executable    bool                 `json:"executable"`
	ProfileID     string               `json:"profileId"`
	ProfileName   string               `json:"profileName"`
	Commit        string               `json:"commit"`
	Snapshot      *SnapshotSummary     `json:"snapshot,omitempty"`
	Releases      []ReleaseSummary     `json:"releases,omitempty"`
	Stacks        []StackPlan          `json:"stacks"`
	Requirements  ResourceRequirements `json:"requirements"`
	Prerequisites []Prerequisite       `json:"prerequisites"`
}
type ResourceRequirements struct {
	Networks            []map[string]any `json:"networks,omitempty"`
	Mounts              []map[string]any `json:"mounts,omitempty"`
	Volumes             []map[string]any `json:"volumes,omitempty"`
	Devices             []map[string]any `json:"devices,omitempty"`
	StartupDependencies []map[string]any `json:"startupDependencies,omitempty"`
}
type SnapshotSummary struct {
	ID               string            `json:"snapshotId"`
	BackupReferences []BackupReference `json:"backupReferences"`
	RecoveryKeyRefs  []string          `json:"recoveryKeyRefs,omitempty"`
}
type BackupReference struct {
	PolicyID  string `json:"backupPolicyId"`
	Reference string `json:"reference"`
}
type ReleaseSummary struct {
	ReleaseID string `json:"releaseId"`
	StackID   string `json:"stackId"`
	Commit    string `json:"gitCommit"`
	Outcome   string `json:"outcome"`
}
type StackPlan struct {
	StackID         string   `json:"stackId"`
	AssignmentID    string   `json:"assignmentId"`
	ProjectName     string   `json:"projectName"`
	Services        []string `json:"services"`
	RecoveryKeyRefs []string `json:"recoveryKeyRefs,omitempty"`
	PersistentPaths []string `json:"persistentPaths,omitempty"`
	BackupPolicyIDs []string `json:"backupPolicyIds,omitempty"`
}
type Prerequisite struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// Build inspects a local, checked-out portable Git repository. It only reads
// committed blobs and metadata; it does not access host paths or run recovery.
func Build(ctx context.Context, repo, profileID string, snapshot *SnapshotMetadata, releaseIDs []string) (Plan, error) {
	var out Plan
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !stack.ValidID(profileID) || len(releaseIDs) > 64 {
		return out, ErrInvalid
	}
	releaseIDs = append([]string(nil), releaseIDs...)
	sort.Strings(releaseIDs)
	for i, id := range releaseIDs {
		if !validReleaseID(id) || (i > 0 && id == releaseIDs[i-1]) {
			return out, ErrInvalid
		}
	}
	root, err := filepath.Abs(repo)
	if err != nil {
		return out, ErrInvalid
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return out, ErrInvalid
	}
	root, err = git(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return out, ErrInvalid
	}
	commit, err := git(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil || !stack.CommitPattern.MatchString(commit) {
		return out, ErrInvalid
	}
	// Relevant manifests and stack inputs must be exactly the committed state.
	status, err := git(ctx, root, "status", "--porcelain", "-z", "--untracked-files=no", "--", "profiles/", "assignments/", "stacks/", "releases/")
	if err != nil || status != "" {
		return out, ErrInvalid
	}
	blobs, err := readTree(ctx, root, commit)
	if err != nil {
		return out, err
	}
	profilePath := "profiles/" + profileID + "/vigara-profile.yaml"
	if _, ok := blobs[profilePath]; !ok {
		return out, ErrInvalid
	}
	placementFiles := map[string]string{}
	for p, body := range blobs {
		if placement.ValidPath(p) {
			placementFiles[p] = body
		}
	}
	catalog, err := placement.Parse(placementFiles)
	if err != nil {
		return out, ErrInvalid
	}
	profile, ok := catalog.Profiles[profileID]
	if !ok {
		return out, ErrInvalid
	}
	assignments := make([]placement.Assignment, 0)
	allStackSnaps := map[string]stack.Snapshot{}
	stackSnaps := map[string]stack.Snapshot{}
	for _, a := range catalog.Assignments {
		if _, ok := allStackSnaps[a.StackID]; !ok {
			ss, err := readStack(blobs, a.StackID)
			if err != nil {
				return out, err
			}
			allStackSnaps[a.StackID] = ss
		}
		if a.ProfileID == profileID {
			assignments = append(assignments, a)
			stackSnaps[a.StackID] = allStackSnaps[a.StackID]
		}
	}
	if len(assignments) == 0 {
		return out, ErrInvalid
	}
	placementStacks := make(map[string]stack.Snapshot, len(allStackSnaps))
	for id, s := range allStackSnaps {
		clean, err := withoutConfigurationAssets(s)
		if err != nil {
			return out, ErrInvalid
		}
		placementStacks[id] = clean
	}
	if _, err := placement.Validate(placementFiles, placementStacks); err != nil {
		return out, ErrInvalid
	}
	ordered, err := orderStacks(profile, assignments)
	if err != nil {
		return out, ErrInvalid
	}
	if snapshot != nil && (!validSnapshotID(snapshot.ID) || snapshot.ProfileID != profileID || len(snapshot.BackupReferences) > 128 || len(snapshot.RecoveryKeyRefs) > 32) {
		return out, ErrInvalid
	}
	if snapshot != nil {
		for _, keyRef := range snapshot.RecoveryKeyRefs {
			if !safeReference(keyRef) {
				return out, ErrInvalid
			}
		}
	}

	resources, err := profileRequirements(profile)
	if err != nil {
		return out, ErrInvalid
	}
	plan := Plan{APIVersion: "vigara.io/v1", Kind: "ComposeRecoveryPlan", Status: "NOT_EXECUTABLE", Executable: false, ProfileID: profileID, ProfileName: profile.DisplayName, Commit: commit, Requirements: resources, Stacks: []StackPlan{}, Prerequisites: []Prerequisite{
		{ID: "target-host-evidence", State: "required", Detail: "Collect current OS, storage, Docker, network, device, and mount evidence on the replacement host."},
		{ID: "independent-recovery-identity", State: "required", Detail: "Verify operator custody and successful offline access to the independent recovery identity."},
		{ID: "backup-restore-validation", State: "required", Detail: "Validate backup integrity and a restore point before any recovery action."},
		{ID: "ownership-fencing", State: "required", Detail: "Prove the prior owner is fenced and obtain an authorized ownership transfer with a new epoch."},
		{ID: "agent-enrollment", State: "required", Detail: "Enroll and bind a replacement agent only after host identity and access controls are reviewed."},
	}}
	if snapshot != nil {
		policies, err := requiredBackupPolicies(profile, stackSnaps)
		if err != nil {
			return out, err
		}
		refs := make([]BackupReference, 0, len(policies))
		for _, policy := range policies {
			ref := snapshot.BackupReferences[policy]
			if !safeReference(ref) {
				return out, fmt.Errorf("%w: selected snapshot lacks backup reference", ErrInvalid)
			}
			refs = append(refs, BackupReference{PolicyID: policy, Reference: ref})
		}
		for policy := range snapshot.BackupReferences {
			if !contains(policies, policy) {
				return out, ErrInvalid
			}
		}
		keyRefs := append([]string(nil), snapshot.RecoveryKeyRefs...)
		sort.Strings(keyRefs)
		plan.Snapshot = &SnapshotSummary{ID: snapshot.ID, BackupReferences: refs, RecoveryKeyRefs: keyRefs}
	} else {
		policies, err := requiredBackupPolicies(profile, stackSnaps)
		if err != nil {
			return out, err
		}
		if len(policies) > 0 {
			return out, fmt.Errorf("%w: persistent resources require selected snapshot metadata", ErrInvalid)
		}
	}
	for _, a := range ordered {
		ss := stackSnaps[a.StackID]
		m, err := validateStack(ss)
		if err != nil {
			return out, ErrInvalid
		}
		services, paths, policies, err := inspectStack(blobs, a.StackID, ss, m)
		if err != nil {
			return out, err
		}
		if snapshot != nil {
			for _, p := range policies {
				if !safeReference(snapshot.BackupReferences[p]) {
					return out, fmt.Errorf("%w: selected snapshot lacks backup reference", ErrInvalid)
				}
			}
		}
		keys := recoveryKeys(m.SecretRecipientRefs, a.Recipients)
		plan.Stacks = append(plan.Stacks, StackPlan{StackID: a.StackID, AssignmentID: a.ID, ProjectName: effectiveProject(m.ProjectName, a.ProjectName), Services: services, RecoveryKeyRefs: keys, PersistentPaths: paths, BackupPolicyIDs: policies})
	}
	for _, id := range releaseIDs {
		summary, err := readRelease(blobs, profileID, id, plan.Stacks)
		if err != nil {
			return out, err
		}
		plan.Releases = append(plan.Releases, summary)
	}
	return plan, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}
func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5_000_000_000)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout boundedBuffer
	stdout.limit = maxRepoBytes
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || stdout.Len() > maxRepoBytes {
		return "", ErrInvalid
	}
	return stdout.String(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		return 0, errOutputLimit
	}
	if len(p) > remaining {
		n, _ := b.Buffer.Write(p[:remaining])
		return n, errOutputLimit
	}
	return b.Buffer.Write(p)
}

func readTree(ctx context.Context, root, commit string) (map[string]string, error) {
	listing, err := git(ctx, root, "ls-tree", "-r", "-z", commit, "--", "profiles/", "assignments/", "stacks/", "releases/")
	if err != nil {
		return nil, ErrInvalid
	}
	blobs := map[string]string{}
	total := 0
	for _, record := range strings.Split(listing, "\x00") {
		if record == "" {
			continue
		}
		meta, p, ok := strings.Cut(record, "\t")
		if !ok {
			return nil, ErrInvalid
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, ErrInvalid
		}
		if !relevant(p) {
			continue
		}
		if fields[0] != "100644" {
			return nil, ErrInvalid
		}
		if !strings.HasSuffix(strings.ToLower(p), ".sops") && secretOrKeyPath(p) {
			return nil, ErrInvalid
		}
		if len(blobs) >= placement.MaxFiles+512 {
			return nil, ErrInvalid
		}
		body := ""
		if strings.HasSuffix(strings.ToLower(p), ".sops") {
			// Stack validation only needs presence. Never read encrypted payloads.
			body = "opaque encrypted input"
		} else {
			var e error
			body, e = gitRaw(ctx, root, "cat-file", "blob", fields[2])
			if e != nil || len(body) > maxFileBytes {
				return nil, ErrInvalid
			}
		}
		total += len(body) + len(p)
		if total > maxRepoBytes {
			return nil, ErrInvalid
		}
		blobs[p] = body
	}
	return blobs, nil
}
func relevant(p string) bool {
	return strings.HasPrefix(p, "profiles/") || strings.HasPrefix(p, "assignments/") || strings.HasPrefix(p, "stacks/") || strings.HasPrefix(p, "releases/")
}
func secretOrKeyPath(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	if strings.Contains(base, ".env") || strings.Contains(base, "private") || strings.Contains(base, "secret") || strings.Contains(base, "credential") || strings.Contains(base, "identity") || strings.Contains(base, "id_ed25519") || strings.Contains(base, "id_rsa") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") || strings.HasSuffix(base, ".age") || strings.HasSuffix(base, ".agekey") {
		return true
	}
	return false
}
func readStack(blobs map[string]string, id string) (stack.Snapshot, error) {
	prefix := "stacks/" + id + "/"
	s := stack.Snapshot{ID: id, Files: map[string]string{}}
	for p, b := range blobs {
		if strings.HasPrefix(p, prefix) {
			rel := strings.TrimPrefix(p, prefix)
			if !stack.ValidPath(rel) {
				return s, ErrInvalid
			}
			s.Files[rel] = b
		}
	}
	if _, err := validateStack(s); err != nil {
		return s, ErrInvalid
	}
	return s, nil
}

// validateStack delegates all existing stack checks to stack.Validate. Until
// that manifest struct supports configurationAssets, this offline planner
// removes only that known schema field for the call and strictly validates its
// bounded metadata separately. It does not change the live stack contract.
func validateStack(s stack.Snapshot) (stack.Manifest, error) {
	clean, err := withoutConfigurationAssets(s)
	if err != nil {
		return stack.Manifest{}, err
	}
	manifest, err := stack.Validate(clean)
	if err != nil {
		return stack.Manifest{}, ErrInvalid
	}
	return manifest, nil
}

func withoutConfigurationAssets(s stack.Snapshot) (stack.Snapshot, error) {
	var meta map[string]any
	if yaml.Unmarshal([]byte(s.Files["vigara-stack.yaml"]), &meta) != nil {
		return stack.Snapshot{}, ErrInvalid
	}
	assets, hasAssets := meta["configurationAssets"]
	if hasAssets {
		if err := validateAssets(assets); err != nil {
			return stack.Snapshot{}, err
		}
		delete(meta, "configurationAssets")
	}
	clean, err := yaml.Marshal(meta)
	if err != nil {
		return stack.Snapshot{}, ErrInvalid
	}
	copy := stack.Snapshot{ID: s.ID, Commit: s.Commit, Files: make(map[string]string, len(s.Files))}
	for p, b := range s.Files {
		copy.Files[p] = b
	}
	copy.Files["vigara-stack.yaml"] = string(clean)
	return copy, nil
}

func validateAssets(value any) error {
	items, ok := value.([]any)
	if !ok || len(items) > 128 {
		return ErrInvalid
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		for key := range m {
			if key != "path" && key != "class" && key != "required" && key != "backupPolicyId" && key != "purpose" {
				return ErrInvalid
			}
		}
		path, ok := m["path"].(string)
		if !ok || !safeHostPath(path) {
			return ErrInvalid
		}
		class, ok := m["class"].(string)
		if !ok || (class != "portable-config" && class != "persistent-data" && class != "host-file" && class != "host-device") {
			return ErrInvalid
		}
		if _, ok := m["required"].(bool); !ok {
			return ErrInvalid
		}
		if purpose, exists := m["purpose"]; exists {
			s, ok := purpose.(string)
			if !ok || len(s) > 200 {
				return ErrInvalid
			}
		}
		if policy, exists := m["backupPolicyId"]; exists {
			s, ok := policy.(string)
			if !ok || !stack.ValidID(s) {
				return ErrInvalid
			}
		}
		if class == "portable-config" && !stack.ValidPath(path) {
			return ErrInvalid
		}
		if class == "persistent-data" {
			policy, ok := m["backupPolicyId"].(string)
			if !ok || !stack.ValidID(policy) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func orderStacks(p placement.Profile, assignments []placement.Assignment) ([]placement.Assignment, error) {
	byID := map[string]placement.Assignment{}
	ids := []string{}
	for _, a := range assignments {
		if _, ok := byID[a.StackID]; ok {
			return nil, ErrInvalid
		}
		byID[a.StackID] = a
		ids = append(ids, a.StackID)
	}
	sort.Strings(ids)
	edges := map[string][]string{}
	indegree := map[string]int{}
	for _, id := range ids {
		indegree[id] = 0
	}
	deps, ok := p.Requirements["startupDependencies"].([]any)
	if ok {
		for _, raw := range deps {
			d, ok := raw.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			before, bok := d["before"].(string)
			after, aok := d["after"].(string)
			if !bok || !aok || byID[before].StackID == "" || byID[after].StackID == "" || before == after {
				return nil, ErrInvalid
			}
			edges[before] = append(edges[before], after)
			indegree[after]++
		}
	}
	ready := []string{}
	for _, id := range ids {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	out := make([]placement.Assignment, 0, len(ids))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, byID[id])
		sort.Strings(edges[id])
		for _, next := range edges[id] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
				sort.Strings(ready)
			}
		}
	}
	if len(out) != len(ids) {
		return nil, ErrInvalid
	}
	return out, nil
}

func inspectStack(blobs map[string]string, id string, s stack.Snapshot, m stack.Manifest) ([]string, []string, []string, error) {
	servicesSet := map[string]bool{}
	paths := []string{}
	policies := map[string]bool{}
	for _, f := range m.Compose.Files {
		var doc map[string]any
		if yaml.Unmarshal([]byte(s.Files[f]), &doc) != nil {
			return nil, nil, nil, ErrInvalid
		}
		serviceMap, ok := doc["services"].(map[string]any)
		if !ok {
			return nil, nil, nil, ErrInvalid
		}
		for name := range serviceMap {
			if !safeServiceName(name) {
				return nil, nil, nil, ErrInvalid
			}
			servicesSet[name] = true
		}
	}
	// Only explicit Vigara asset metadata describes persistent paths. Never stat,
	// read, or resolve those host paths while constructing an offline plan.
	var raw map[string]any
	if yaml.Unmarshal([]byte(s.Files["vigara-stack.yaml"]), &raw) != nil {
		return nil, nil, nil, ErrInvalid
	}
	assets, _ := raw["configurationAssets"].([]any)
	for _, item := range assets {
		a, ok := item.(map[string]any)
		if !ok {
			return nil, nil, nil, ErrInvalid
		}
		class, _ := a["class"].(string)
		if class != "persistent-data" {
			continue
		}
		path, _ := a["path"].(string)
		policy, _ := a["backupPolicyId"].(string)
		if !safeHostPath(path) || !stack.ValidID(policy) {
			return nil, nil, nil, ErrInvalid
		}
		paths = append(paths, path)
		policies[policy] = true
	}
	services := make([]string, 0, len(servicesSet))
	for v := range servicesSet {
		services = append(services, v)
	}
	sort.Strings(services)
	sort.Strings(paths)
	ps := make([]string, 0, len(policies))
	for p := range policies {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return services, paths, ps, nil
}
func profileRequirements(p placement.Profile) (ResourceRequirements, error) {
	r := ResourceRequirements{}
	for key, dst := range map[string]*[]map[string]any{"networks": &r.Networks, "mounts": &r.Mounts, "volumes": &r.Volumes, "devices": &r.Devices, "startupDependencies": &r.StartupDependencies} {
		items, ok := p.Requirements[key].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return r, ErrInvalid
			}
			if key == "mounts" || key == "devices" {
				hostPath, ok := m["hostPath"].(string)
				if !ok || !safeHostPath(hostPath) {
					return r, ErrInvalid
				}
			}
			*dst = append(*dst, m)
		}
	}
	return r, nil
}

func requiredBackupPolicies(p placement.Profile, stacks map[string]stack.Snapshot) ([]string, error) {
	set := map[string]bool{}
	for _, kind := range []string{"mounts", "volumes"} {
		if req, ok := p.Requirements[kind].([]any); ok {
			for _, v := range req {
				m, ok := v.(map[string]any)
				if !ok {
					return nil, ErrInvalid
				}
				if m["persistent"] == true {
					id, ok := m["backupPolicyId"].(string)
					if !ok || !stack.ValidID(id) {
						return nil, fmt.Errorf("%w: persistent profile requirement lacks backup policy", ErrInvalid)
					}
					set[id] = true
				}
			}
		}
	}
	for _, s := range stacks {
		var raw map[string]any
		_ = yaml.Unmarshal([]byte(s.Files["vigara-stack.yaml"]), &raw)
		assets, _ := raw["configurationAssets"].([]any)
		for _, v := range assets {
			if a, ok := v.(map[string]any); ok && a["class"] == "persistent-data" {
				if id, ok := a["backupPolicyId"].(string); ok && stack.ValidID(id) {
					set[id] = true
				} else {
					return nil, fmt.Errorf("%w: persistent path lacks backup policy", ErrInvalid)
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
func recoveryKeys(a, b []stack.RecipientRef) []string {
	set := map[string]bool{}
	for _, refs := range [][]stack.RecipientRef{a, b} {
		for _, r := range refs {
			if r.Class == "operator-recovery" {
				set[r.KeyRef] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func effectiveProject(base, override string) string {
	if override != "" {
		return override
	}
	return base
}
func safeReference(s string) bool {
	if len(s) == 0 || len(s) > 128 || strings.TrimSpace(s) != s || s == "." || s == ".." || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._:-", r) {
			return false
		}
	}
	return true
}
func safeHostPath(value string) bool {
	if len(value) == 0 || len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func safeServiceName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("_.-", r) {
			return false
		}
	}
	return true
}
func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func readRelease(blobs map[string]string, profile, id string, stacks []StackPlan) (ReleaseSummary, error) {
	if !validReleaseID(id) {
		return ReleaseSummary{}, ErrInvalid
	}
	for _, s := range stacks {
		p := "releases/" + profile + "/" + s.StackID + "/" + id + ".yaml"
		body, ok := blobs[p]
		if !ok {
			continue
		}
		var doc struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			ReleaseID  string `yaml:"releaseId"`
			ProfileID  string `yaml:"profileId"`
			StackID    string `yaml:"stackId"`
			GitCommit  string `yaml:"gitCommit"`
			Outcome    string `yaml:"outcome"`
		}
		if yaml.Unmarshal([]byte(body), &doc) != nil || doc.APIVersion != "vigara.io/v1" || doc.Kind != "Release" || doc.ReleaseID != id || doc.ProfileID != profile || doc.StackID != s.StackID || !stack.CommitPattern.MatchString(doc.GitCommit) || !validOutcome(doc.Outcome) {
			return ReleaseSummary{}, ErrInvalid
		}
		return ReleaseSummary{ReleaseID: id, StackID: s.StackID, Commit: doc.GitCommit, Outcome: doc.Outcome}, nil
	}
	return ReleaseSummary{}, ErrInvalid
}
func validReleaseID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validSnapshotID(id string) bool { return stack.ValidID(id) || validReleaseID(id) }
func validOutcome(value string) bool {
	switch value {
	case "succeeded", "failed", "partial", "cancelled", "timed_out":
		return true
	}
	return false
}

// ReadSnapshotMetadata reads a small JSON metadata file and rejects links and secrets-shaped fields.
func ReadSnapshotMetadata(path string) (SnapshotMetadata, error) {
	var out SnapshotMetadata
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxSnapshotBytes {
		return out, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return out, ErrInvalid
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSnapshotBytes+1))
	if err != nil || len(b) > maxSnapshotBytes {
		return out, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF {
		return out, ErrInvalid
	}
	return out, nil
}
