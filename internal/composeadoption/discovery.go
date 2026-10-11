// Package composeadoption builds a read-only preview of existing Compose
// applications. It never invokes Compose or returns or prints dotenv values.
package composeadoption

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxComposeBytes = 2 << 20
const maxRuntimeSnapshotAge = 5 * time.Minute
const maxRuntimeSnapshotFutureSkew = time.Minute

var interpolationPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)`)
var dotenvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var sensitiveKeyPattern = regexp.MustCompile(`(?i)(pass(word)?|secret|token|credential|private[_-]?key|api[_-]?key|access[_-]?key)`)
var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(pass(word)?|secret|token|credential|private[_-]?key|api[_-]?key|access[_-]?key)\s*[:=]`)
var plainInterpolationPattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

type RuntimeSnapshot struct {
	ObservedAt time.Time          `json:"observedAt"`
	Complete   bool               `json:"complete"`
	Containers []RuntimeContainer `json:"containers"`
}

// RuntimeContainer is intentionally limited to safe runtime metadata. It has
// no env/config field, so callers cannot serialize container environment data.
type RuntimeContainer struct {
	ID           string        `json:"id"`
	ProjectName  string        `json:"projectName"`
	ServiceName  string        `json:"serviceName"`
	WorkingDir   string        `json:"workingDir"`
	ComposeFiles []string      `json:"composeFiles"`
	State        string        `json:"state"`
	StartedAt    string        `json:"startedAt"`
	StorageBinds []StorageBind `json:"storageBinds"`
}

type StorageBind struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadWrite   bool   `json:"readWrite"`
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Driver      string `json:"driver,omitempty"`
	Propagation string `json:"propagation,omitempty"`
}

type Preview struct {
	Root             string            `json:"root"`
	ObservedAt       time.Time         `json:"observedAt,omitempty"`
	RuntimeComplete  bool              `json:"runtimeComplete"`
	Applications     []Application     `json:"applications"`
	OrphanedProjects []OrphanedProject `json:"orphanedProjects"`
}

type Application struct {
	Directory              string             `json:"directory"`
	ComposeFiles           []string           `json:"composeFiles"`
	ProjectName            string             `json:"projectName,omitempty"`
	RuntimeStatus          string             `json:"runtimeStatus"`
	ProjectDirectory       string             `json:"projectDirectory,omitempty"`
	Containers             []RuntimeContainer `json:"containers"`
	DotenvFiles            []InputFile        `json:"dotenvFiles"`
	ConfigFiles            []InputFile        `json:"configFiles"`
	SharedEnvFiles         []InputFile        `json:"sharedEnvCandidates"`
	Requirements           Requirements       `json:"requirements"`
	Unsupported            []string           `json:"unsupported"`
	ReviewRequired         []string           `json:"reviewRequired"`
	InterpolationVariables []string           `json:"interpolationVariables"`
	SharedEnvVariables     []string           `json:"sharedEnvVariables"`
	EnvFileReferences      []EnvFileReference `json:"envFileReferences"`
	SourceHashes           map[string]string  `json:"-"`
}

type EnvFileReference struct {
	ComposeFile string `json:"composeFile"`
	Service     string `json:"service"`
	Order       int    `json:"order"`
	SourcePath  string `json:"sourcePath"`
	Required    bool   `json:"required"`
}

type InputFile struct {
	Path        string `json:"path"`
	Purpose     string `json:"purpose"`
	Sensitivity string `json:"sensitivity"`
	Exists      bool   `json:"exists"`
	SourceKind  string `json:"sourceKind,omitempty"`
	Format      string `json:"format,omitempty"`
	Service     string `json:"service,omitempty"`
	ComposeFile string `json:"composeFile,omitempty"`
	Order       int    `json:"order,omitempty"`
}
type Requirements struct {
	Networks    []string `json:"networks"`
	Ports       []string `json:"ports"`
	Storage     []string `json:"storage"`
	HostNetwork bool     `json:"hostNetwork"`
	Devices     []string `json:"devices"`
}
type OrphanedProject struct {
	ProjectName string             `json:"projectName"`
	Containers  []RuntimeContainer `json:"containers"`
}

// Discover scans local files and matches a complete, caller-provided runtime
// observation. It makes no Docker calls and treats an incomplete inventory as
// unknown rather than claiming stopped or absent workloads.
func Discover(root string, runtime RuntimeSnapshot) (Preview, error) {
	return DiscoverContext(context.Background(), root, runtime)
}

func DiscoverContext(ctx context.Context, root string, runtime RuntimeSnapshot) (Preview, error) {
	if runtime.Complete {
		if err := validateRuntimeSnapshot(runtime); err != nil {
			return Preview{}, err
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Preview{}, errors.New("could not resolve Compose root")
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return Preview{}, errors.New("Compose root must be an accessible directory")
	}
	rootReal, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return Preview{}, errors.New("could not resolve Compose root safely")
	}
	rootReal, err = filepath.Abs(rootReal)
	if err != nil {
		return Preview{}, errors.New("could not resolve Compose root safely")
	}

	composeByDir := map[string][]string{}
	walked := 0
	err = filepath.WalkDir(rootReal, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		walked++
		if walked > 50000 {
			return errors.New("Compose inventory exceeds the scan limit")
		}
		if walkErr != nil {
			return errors.New("cannot read Compose inventory")
		}
		if d.IsDir() && path != rootReal {
			switch strings.ToLower(d.Name()) {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
		}
		if d.IsDir() || !isComposeName(d.Name()) {
			return nil
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil {
			return nil
		}
		real, e = filepath.Abs(real)
		if e != nil || !within(rootReal, real) {
			return nil
		}
		dir := filepath.Dir(real)
		composeByDir[dir] = append(composeByDir[dir], real)
		if len(composeByDir[dir]) > 64 {
			return errors.New("Compose file count exceeds the per-application limit")
		}
		return nil
	})
	if err != nil {
		return Preview{}, err
	}
	dirs := make([]string, 0, len(composeByDir))
	for dir := range composeByDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	preview := Preview{Root: rootReal, ObservedAt: runtime.ObservedAt, RuntimeComplete: runtime.Complete, Applications: []Application{}, OrphanedProjects: []OrphanedProject{}}
	byProject := groupRuntimeByProject(runtime.Containers)
	matched := map[string]bool{}
	for _, dir := range dirs {
		files := composeByDir[dir]
		sort.Slice(files, func(i, j int) bool { return composeFileOrder(files[i]) < composeFileOrder(files[j]) })
		app := Application{Directory: rel(rootReal, dir), ComposeFiles: rels(rootReal, files), RuntimeStatus: "unmatched", Containers: []RuntimeContainer{}, DotenvFiles: []InputFile{}, ConfigFiles: []InputFile{}, SharedEnvFiles: []InputFile{}, Unsupported: []string{}, ReviewRequired: []string{}, InterpolationVariables: []string{}, SharedEnvVariables: []string{}, EnvFileReferences: []EnvFileReference{}, SourceHashes: map[string]string{}, Requirements: Requirements{Networks: []string{}, Ports: []string{}, Storage: []string{}, Devices: []string{}}}
		if !runtime.Complete {
			app.RuntimeStatus = "runtime_unknown"
		}
		if len(files) > 1 && !hasBaseOverride(files) {
			app.Unsupported = appendOnce(app.Unsupported, "multiple_standard_compose_files_require_explicit_order")
		}
		for _, file := range files {
			inspectCompose(rootReal, dir, file, &app)
		}
		if safeFileWithin(rootReal, filepath.Join(dir, ".env")) {
			app.DotenvFiles = appendUniqueInput(app.DotenvFiles, InputFile{Path: rel(rootReal, filepath.Join(dir, ".env")), Purpose: "stack", Sensitivity: "encrypted", Exists: true, SourceKind: "localInterpolation", Format: encryptedArtifactFormat})
		}
		if dir != rootReal && safeFileWithin(rootReal, filepath.Join(rootReal, ".env")) {
			// Parent .env files are only candidates. Compose does not implicitly
			// inherit them; preserving one requires explicit operator selection.
			app.SharedEnvFiles = append(app.SharedEnvFiles, InputFile{Path: ".env", Purpose: "shared", Sensitivity: "encrypted", Exists: true, SourceKind: "sharedInterpolation", Format: encryptedArtifactFormat})
			app.ReviewRequired = appendOnce(app.ReviewRequired, "root_shared_dotenv_dependency_requires_confirmation")
		}
		app.SharedEnvVariables = discoverSharedVariables(rootReal, app)
		for project, containers := range byProject {
			if !runtime.Complete {
				break
			}
			if !projectHasFiles(containers, rootReal, files) {
				continue
			}
			if app.ProjectName != "" && app.ProjectName != project {
				app.Unsupported = appendOnce(app.Unsupported, "conflicting_runtime_project_labels")
				app.ProjectName = ""
				break
			}
			app.ProjectName, app.Containers = project, containers
			app.RuntimeStatus = projectStatus(containers)
			app.ProjectDirectory = rel(rootReal, filepath.Clean(containers[0].WorkingDir))
			app.ComposeFiles = runtimeFileOrder(rootReal, containers[0])
			matched[project] = true
			if canonical(containers[0].WorkingDir) != canonical(dir) {
				app.Unsupported = appendOnce(app.Unsupported, "compose_project_directory_differs_from_application_directory")
			}
		}
		if len(app.Unsupported) > 0 {
			app.RuntimeStatus = "unsupported"
		}
		preview.Applications = append(preview.Applications, app)
	}
	if runtime.Complete {
		for project, containers := range groupRuntimeByProject(runtime.Containers) {
			if !matched[project] {
				preview.OrphanedProjects = append(preview.OrphanedProjects, OrphanedProject{ProjectName: project, Containers: containers})
			}
		}
		sort.Slice(preview.OrphanedProjects, func(i, j int) bool {
			return preview.OrphanedProjects[i].ProjectName < preview.OrphanedProjects[j].ProjectName
		})
	}
	return preview, nil
}

func inspectCompose(root, dir, file string, app *Application) {
	data, err := readBounded(file, maxComposeBytes)
	if err != nil {
		app.Unsupported = appendOnce(app.Unsupported, "compose_file_unreadable_or_too_large")
		return
	}
	app.SourceHashes[rel(root, file)] = contentHash(data)
	var model map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&model) != nil || model == nil {
		app.Unsupported = appendOnce(app.Unsupported, "compose_yaml_invalid")
		return
	}
	for _, variable := range interpolationVariables(data) {
		app.InterpolationVariables = appendUnique(app.InterpolationVariables, variable)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		app.Unsupported = appendOnce(app.Unsupported, "multiple_compose_yaml_documents_unsupported")
		return
	}
	if _, ok := model["include"]; ok {
		app.Unsupported = appendOnce(app.Unsupported, "compose_include_unsupported")
	}
	if _, ok := model["extends"]; ok {
		app.Unsupported = appendOnce(app.Unsupported, "compose_extends_unsupported")
	}
	base := dir // Compose relative paths are resolved from the observed project directory.
	services, ok := model["services"].(map[string]any)
	if !ok || len(services) == 0 {
		app.Unsupported = appendOnce(app.Unsupported, "compose_services_missing_or_invalid")
		return
	}
	serviceNames := make([]string, 0, len(services))
	for name := range services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	for _, serviceName := range serviceNames {
		raw := services[serviceName]
		service, ok := raw.(map[string]any)
		if !ok || service == nil {
			app.Unsupported = appendOnce(app.Unsupported, "compose_service_definition_invalid")
			continue
		}
		if _, ok := service["build"]; ok {
			app.Unsupported = appendOnce(app.Unsupported, "compose_build_unsupported")
		}
		if _, ok := service["extends"]; ok {
			app.Unsupported = appendOnce(app.Unsupported, "compose_extends_unsupported")
		}
		for order, rawEnv := range list(service["env_file"]) {
			path, optional, ok := envPath(rawEnv)
			if !ok {
				app.Unsupported = appendOnce(app.Unsupported, "unsupported_env_file_syntax")
				continue
			}
			relCompose := rel(root, file)
			app.EnvFileReferences = append(app.EnvFileReferences, EnvFileReference{ComposeFile: relCompose, Service: serviceName, Order: order, SourcePath: path, Required: !optional})
			beforeCount := len(app.DotenvFiles)
			addInput(root, base, path, "deployment", true, optional, &app.DotenvFiles, app)
			if len(app.DotenvFiles) > beforeCount {
				last := &app.DotenvFiles[len(app.DotenvFiles)-1]
				last.SourceKind, last.Format, last.Service, last.ComposeFile, last.Order = "serviceEnvFile", encryptedArtifactFormat, serviceName, relCompose, order
			}
		}
		if _, ok := service["environment"]; ok {
			app.ReviewRequired = appendOnce(app.ReviewRequired, "service_environment_may_contain_plaintext_secrets")
		}
		for _, key := range []string{"labels", "command", "entrypoint", "healthcheck"} {
			if _, ok := service[key]; ok {
				app.ReviewRequired = appendOnce(app.ReviewRequired, "compose_metadata_requires_plaintext_secret_review")
			}
		}
		for _, v := range list(service["ports"]) {
			if port, ok := safePort(v); ok {
				app.Requirements.Ports = appendUnique(app.Requirements.Ports, port)
			} else {
				app.ReviewRequired = appendOnce(app.ReviewRequired, "port_mapping_requires_review")
			}
		}
		if scalar(service["network_mode"]) == "host" {
			app.Requirements.HostNetwork = true
		}
		for _, v := range list(service["devices"]) {
			if path, ok := safeHostPath(v); ok {
				app.Requirements.Devices = appendUnique(app.Requirements.Devices, path)
			} else {
				app.ReviewRequired = appendOnce(app.ReviewRequired, "device_mapping_requires_review")
			}
		}
		for _, v := range list(service["volumes"]) {
			if source, ok := safeStorageSource(v); ok && source != "" {
				app.Requirements.Storage = appendUnique(app.Requirements.Storage, source)
				if isRelativeBindSource(source) {
					app.ReviewRequired = appendOnce(app.ReviewRequired, "relative_bind_storage_requires_verified_host_mapping")
				}
			} else if source != "" {
				app.ReviewRequired = appendOnce(app.ReviewRequired, "storage_source_requires_review")
			}
		}
	}
	for name, raw := range mapValue(model["networks"]) {
		network, _ := raw.(map[string]any)
		if network != nil && truthy(network["external"]) {
			app.Requirements.Networks = appendUnique(app.Requirements.Networks, name)
		}
	}
	for _, key := range []string{"configs", "secrets"} {
		for _, raw := range mapValue(model[key]) {
			obj, _ := raw.(map[string]any)
			if obj == nil {
				continue
			}
			if path, ok := obj["file"].(string); ok {
				addInput(root, base, path, "stack", false, false, &app.ConfigFiles, app)
			}
		}
	}
	if containsCredentialURL(model) {
		app.Unsupported = appendOnce(app.Unsupported, "compose_contains_url_credentials")
	}
	if containsSensitiveField(model) {
		app.Unsupported = appendOnce(app.Unsupported, "compose_contains_secret_like_field")
	}
}

func addInput(root, base, input, purpose string, encrypted, optional bool, dst *[]InputFile, app *Application) {
	input = strings.TrimSpace(input)
	if input == "" || filepath.IsAbs(input) || filepath.VolumeName(input) != "" || strings.Contains(input, "${") {
		app.Unsupported = appendOnce(app.Unsupported, "dynamic_or_absolute_configuration_path")
		return
	}
	for _, segment := range strings.Split(filepath.ToSlash(input), "/") {
		if segment == ".." || segment == "." || segment == "" {
			app.Unsupported = appendOnce(app.Unsupported, "nonportable_configuration_path")
			return
		}
	}
	full := filepath.Join(base, filepath.Clean(filepath.FromSlash(input)))
	absolute, absErr := filepath.Abs(full)
	if absErr != nil || !within(root, absolute) {
		app.Unsupported = appendOnce(app.Unsupported, "configuration_path_escapes_root")
		return
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		path := rel(root, full)
		inputFile := InputFile{Path: path, Purpose: purpose, Sensitivity: sensitivity(encrypted), Exists: false}
		if encrypted {
			inputFile.Format, inputFile.SourceKind = encryptedArtifactFormat, "serviceEnvFile"
		}
		*dst = appendUniqueInput(*dst, inputFile)
		if !optional {
			app.Unsupported = appendOnce(app.Unsupported, "required_configuration_file_missing")
		}
		return
	}
	real, _ = filepath.Abs(real)
	if !within(root, real) {
		app.Unsupported = appendOnce(app.Unsupported, "configuration_path_escapes_root")
		return
	}
	path := rel(root, real)
	inputFile := InputFile{Path: path, Purpose: purpose, Sensitivity: sensitivity(encrypted), Exists: true}
	if encrypted {
		inputFile.Format, inputFile.SourceKind = encryptedArtifactFormat, "serviceEnvFile"
	}
	if !encrypted {
		data, e := readBounded(real, maxComposeBytes)
		if e != nil {
			app.Unsupported = appendOnce(app.Unsupported, "configuration_file_unreadable_or_too_large")
		} else {
			app.SourceHashes[path] = contentHash(data)
		}
	}
	*dst = appendUniqueInput(*dst, inputFile)
}

// VerifyUnchanged fails if a complete before/after observation differs in any
// container ID, start time, project/service identity, Compose path, or mount.
func VerifyUnchanged(before, after RuntimeSnapshot) error {
	if !before.Complete || !after.Complete {
		return errors.New("complete before and after runtime observations are required")
	}
	if before.ObservedAt.IsZero() || after.ObservedAt.IsZero() || !after.ObservedAt.After(before.ObservedAt) {
		return errors.New("runtime observations must have ordered capture times")
	}
	if err := validateRuntimeSnapshot(before); err != nil {
		return err
	}
	if err := validateRuntimeSnapshot(after); err != nil {
		return err
	}
	if observationDigest(before) != observationDigest(after) {
		return errors.New("runtime identity, start time, project labels, or storage bindings changed during adoption")
	}
	return nil
}
func validateRuntimeSnapshot(s RuntimeSnapshot) error {
	age := time.Since(s.ObservedAt)
	if s.ObservedAt.IsZero() || age > maxRuntimeSnapshotAge || age < -maxRuntimeSnapshotFutureSkew {
		return errors.New("runtime observation is missing, stale, or has an invalid future capture time")
	}
	seen := map[string]bool{}
	for _, c := range s.Containers {
		if c.ID == "" || c.ProjectName == "" || c.ServiceName == "" || c.WorkingDir == "" || !filepath.IsAbs(c.WorkingDir) || len(c.ComposeFiles) == 0 || c.StartedAt == "" {
			return errors.New("complete runtime observation is missing container identity, labels, paths, or start time")
		}
		if seen[c.ID] {
			return errors.New("complete runtime observation contains duplicate container IDs")
		}
		seen[c.ID] = true
		switch c.State {
		case "created", "restarting", "running", "paused", "exited", "dead", "removing":
		default:
			return errors.New("complete runtime observation contains an unsupported container state")
		}
		if _, err := time.Parse(time.RFC3339Nano, c.StartedAt); err != nil {
			return errors.New("complete runtime observation contains an invalid start time")
		}
		for _, f := range c.ComposeFiles {
			if strings.TrimSpace(f) == "" {
				return errors.New("complete runtime observation contains an empty Compose file label")
			}
		}
	}
	return nil
}
func observationDigest(s RuntimeSnapshot) string {
	items := append([]RuntimeContainer(nil), s.Containers...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	h := sha256.New()
	for _, c := range items {
		fmt.Fprintf(h, "%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00", c.ID, c.ProjectName, c.ServiceName, canonical(c.WorkingDir), c.StartedAt, c.State)
		for _, f := range c.ComposeFiles {
			fmt.Fprintf(h, "%q\x00", canonical(resolveRuntimePath(c.WorkingDir, f)))
		}
		binds := append([]StorageBind(nil), c.StorageBinds...)
		sort.Slice(binds, func(i, j int) bool {
			if binds[i].Source == binds[j].Source {
				return binds[i].Destination < binds[j].Destination
			}
			return binds[i].Source < binds[j].Source
		})
		for _, b := range binds {
			fmt.Fprintf(h, "%q\x00%q\x00%t\x00%q\x00%q\x00%q\x00%q\x00", b.Source, b.Destination, b.ReadWrite, b.Type, b.Name, b.Driver, b.Propagation)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func projectStatus(cs []RuntimeContainer) string {
	if len(cs) == 0 {
		return "unmatched"
	}
	running, stopped := true, true
	for _, c := range cs {
		if c.State != "running" {
			running = false
		}
		if c.State != "exited" && c.State != "dead" && c.State != "created" {
			stopped = false
		}
	}
	if running {
		return "running"
	}
	if stopped {
		return "stopped"
	}
	return "runtime_unknown"
}
func groupRuntimeByProject(cs []RuntimeContainer) map[string][]RuntimeContainer {
	out := map[string][]RuntimeContainer{}
	for _, c := range cs {
		if c.ProjectName != "" {
			out[c.ProjectName] = append(out[c.ProjectName], c)
		}
	}
	return out
}
func projectHasFiles(cs []RuntimeContainer, root string, files []string) bool {
	if len(cs) == 0 {
		return false
	}
	expected := make([]string, len(files))
	for i, f := range files {
		expected[i] = canonical(f)
	}
	for _, c := range cs {
		if canonical(c.WorkingDir) != canonical(filepath.Dir(files[0])) {
			return false
		}
		if len(c.ComposeFiles) != len(expected) {
			return false
		}
		for i, f := range c.ComposeFiles {
			if canonical(resolveRuntimePath(c.WorkingDir, f)) != expected[i] {
				return false
			}
		}
	}
	return true
}
func isComposeName(n string) bool {
	switch strings.ToLower(n) {
	case "compose.yaml", "compose.yml", "compose.override.yaml", "compose.override.yml", "docker-compose.yaml", "docker-compose.yml", "docker-compose.override.yaml", "docker-compose.override.yml":
		return true
	}
	return false
}
func hasBaseOverride(files []string) bool {
	base, override := false, false
	for _, file := range files {
		name := strings.ToLower(filepath.Base(file))
		if name == "compose.yaml" || name == "compose.yml" || name == "docker-compose.yaml" || name == "docker-compose.yml" {
			base = true
		}
		if strings.Contains(name, "override") {
			override = true
		}
	}
	return base && override
}
func composeFileOrder(path string) int {
	n := strings.ToLower(filepath.Base(path))
	if strings.Contains(n, "override") {
		return 1
	}
	return 0
}
func runtimeFileOrder(root string, c RuntimeContainer) []string {
	out := make([]string, 0, len(c.ComposeFiles))
	for _, f := range c.ComposeFiles {
		out = append(out, rel(root, resolveRuntimePath(c.WorkingDir, f)))
	}
	return out
}
func envPath(v any) (string, bool, bool) {
	if s, ok := v.(string); ok {
		return s, false, true
	}
	if m, ok := v.(map[string]any); ok {
		s, ok := m["path"].(string)
		required, exists := m["required"].(bool)
		return s, exists && !required, ok
	}
	return "", false, false
}
func list(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if v == nil {
		return nil
	}
	return []any{v}
}
func mapValue(v any) map[string]any { m, _ := v.(map[string]any); return m }
func scalar(v any) string           { s, _ := v.(string); return s }
func truthy(v any) bool             { b, _ := v.(bool); return b }
func safePort(v any) (string, bool) {
	if s, ok := v.(string); ok {
		if numericPort(s) {
			return s, true
		}
		return "", false
	}
	if m, ok := v.(map[string]any); ok {
		t, okT := m["target"].(int)
		p, okP := m["published"].(string)
		if okT && okP && t > 0 && numericPort(p) {
			return p + ":" + fmt.Sprint(t), true
		}
	}
	return "", false
}
func numericPort(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' || r == ':' || r == '/' || r >= 'a' && r <= 'z' {
			continue
		}
		return false
	}
	return s != ""
}
func safeHostPath(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && strings.HasPrefix(s, "/") && !strings.Contains(s, "$")
}
func safeStorageSource(v any) (string, bool) {
	if m, ok := v.(map[string]any); ok {
		src, _ := m["source"].(string)
		typ, _ := m["type"].(string)
		if typ == "volume" {
			return "", true
		}
		return src, src != "" && !strings.Contains(src, "$")
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", true
	}
	src := parts[0]
	return src, src != "" && !strings.Contains(src, "$")
}
func isRelativeBindSource(source string) bool {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return true
	}
	return false
}
func containsCredentialURL(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, "://") && strings.Contains(strings.SplitN(x, "://", 2)[1], "@")
	case []any:
		for _, y := range x {
			if containsCredentialURL(y) {
				return true
			}
		}
	case map[string]any:
		for _, y := range x {
			if containsCredentialURL(y) {
				return true
			}
		}
	}
	return false
}
func containsSensitiveField(v any) bool {
	switch x := v.(type) {
	case string:
		return sensitiveAssignmentPattern.MatchString(x) && !plainInterpolationPattern.MatchString(strings.TrimSpace(x))
	case []any:
		for _, child := range x {
			if containsSensitiveField(child) {
				return true
			}
		}
	case map[string]any:
		for key, child := range x {
			if strings.EqualFold(key, "secrets") {
				if containsSensitiveField(child) {
					return true
				}
				continue
			}
			if sensitiveKeyPattern.MatchString(key) {
				if value, ok := child.(string); ok && plainInterpolationPattern.MatchString(strings.TrimSpace(value)) {
					continue
				}
				return true
			}
			if containsSensitiveField(child) {
				return true
			}
		}
	}
	return false
}
func sensitivity(enc bool) string {
	if enc {
		return "encrypted"
	}
	return "unclassified"
}
func appendOnce(a []string, v string) []string {
	for _, x := range a {
		if x == v {
			return a
		}
	}
	return append(a, v)
}
func appendUnique(a []string, v string) []string { return appendOnce(a, v) }
func appendUniqueInput(a []InputFile, v InputFile) []InputFile {
	for _, x := range a {
		if x.Path == v.Path && x.SourceKind == v.SourceKind {
			return a
		}
	}
	return append(a, v)
}
func contentHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func rel(root, path string) string {
	r, e := filepath.Rel(root, path)
	if e != nil {
		return ""
	}
	return filepath.ToSlash(r)
}
func rels(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, rel(root, f))
	}
	return out
}
func canonical(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	a, e := filepath.Abs(p)
	if e == nil {
		p = a
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return filepath.Clean(p)
}
func resolveRuntimePath(w, f string) string {
	if filepath.IsAbs(f) {
		return filepath.Clean(f)
	}
	return filepath.Join(w, f)
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || s.Size() > limit {
		return nil, errors.New("file unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errors.New("file unavailable")
	}
	return b, nil
}
func safeFileWithin(root, path string) bool {
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() {
		return false
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil {
		return false
	}
	real, e = filepath.Abs(real)
	return e == nil && within(root, real)
}

func interpolationVariables(compose []byte) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, match := range interpolationPattern.FindAllSubmatch(compose, -1) {
		name := string(match[1])
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// discoverSharedVariables compares only dotenv key names to interpolation
// references. Values are read into a bounded buffer for parsing, never kept or
// returned in the preview.
func discoverSharedVariables(root string, app Application) []string {
	sharedPath := filepath.Join(root, ".env")
	if !safeFileWithin(root, sharedPath) {
		return []string{}
	}
	shared, err := dotenvKeys(sharedPath)
	if err != nil {
		return []string{}
	}
	local := map[string]bool{}
	for _, input := range app.DotenvFiles {
		path := filepath.Join(root, filepath.FromSlash(input.Path))
		if canonical(path) == canonical(sharedPath) || !safeFileWithin(root, path) {
			continue
		}
		keys, err := dotenvKeys(path)
		if err == nil {
			for key := range keys {
				local[key] = true
			}
		}
	}
	result := []string{}
	for _, name := range app.InterpolationVariables {
		if shared[name] && !local[name] {
			result = append(result, name)
		}
	}
	return result
}

func dotenvKeys(path string) (map[string]bool, error) {
	data, err := readBounded(path, maxComposeBytes)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if dotenvKeyPattern.MatchString(key) {
			keys[key] = true
		}
	}
	return keys, nil
}
