package composedeploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/composeadoption"
	"github.com/deranjer/vigara/internal/composesecrets"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
	"gopkg.in/yaml.v3"
)

var errImport = errors.New("existing stack changed, is outside the approved Compose root, or needs unsupported file inputs; rescan and review it before importing")

// inspectImport emits only the exact labels and runtime fields used to match
// projects. Container environment and arbitrary labels never leave Docker.
const inspectImport = `{"id":{{json .Id}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"directory":{{json (index .Config.Labels "com.docker.compose.project.working_dir")}},"files":{{json (index .Config.Labels "com.docker.compose.project.config_files")}},"state":{{json .State.Status}},"started":{{json .State.StartedAt}},"mounts":{{json .Mounts}}}`

func (r *Runner) observeImports(ctx context.Context) (composeadoption.RuntimeSnapshot, error) {
	out := composeadoption.RuntimeSnapshot{ObservedAt: time.Now().UTC(), Complete: true, Containers: []composeadoption.RuntimeContainer{}}
	data, err := r.command(ctx, "", "ps", "-a", "--filter", "label=com.docker.compose.project", "--format", "{{.ID}}")
	if err != nil {
		return out, errImport
	}
	ids := strings.Fields(string(data))
	if len(ids) > 256 {
		return out, errImport
	}
	if len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		if len(id) < 12 || len(id) > 64 || strings.Trim(id, "abcdef0123456789") != "" {
			return out, errImport
		}
	}
	args := append([]string{"inspect", "--format", inspectImport}, ids...)
	data, err = r.command(ctx, "", args...)
	if err != nil {
		return out, errImport
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for range ids {
		var raw struct {
			ID, Project, Service, Directory, Files, State, Started string
			Mounts                                                 []struct {
				Source, Destination, Type, Name, Driver, Propagation string
				RW                                                   bool
			}
		}
		if decoder.Decode(&raw) != nil {
			return out, errImport
		}
		c := composeadoption.RuntimeContainer{ID: raw.ID, ProjectName: raw.Project, ServiceName: raw.Service, WorkingDir: raw.Directory, ComposeFiles: strings.Split(raw.Files, ","), State: raw.State, StartedAt: raw.Started}
		for _, m := range raw.Mounts {
			c.StorageBinds = append(c.StorageBinds, composeadoption.StorageBind{Source: m.Source, Destination: m.Destination, Type: m.Type, Name: m.Name, Driver: m.Driver, Propagation: m.Propagation, ReadWrite: m.RW})
		}
		out.Containers = append(out.Containers, c)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return out, errImport
	}
	// Only projects touching the locally approved root participate in this
	// observation. Include every container in those projects so conflicting
	// working-directory labels cannot turn a partial match into an adoption.
	root, err := r.managedRoot()
	if err != nil {
		return out, errImport
	}
	projects := map[string]bool{}
	for _, container := range out.Containers {
		if filepath.IsAbs(container.WorkingDir) {
			rel, err := filepath.Rel(root, container.WorkingDir)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				projects[container.ProjectName] = true
			}
		}
	}
	selected := out.Containers[:0]
	for _, container := range out.Containers {
		if projects[container.ProjectName] {
			selected = append(selected, container)
		}
	}
	out.Containers = selected
	return out, nil
}

func importRead(root, relative string) ([]byte, error) {
	if len(relative) > 1024 || filepath.IsAbs(filepath.FromSlash(relative)) || strings.ContainsAny(relative, "\x00\\") {
		return nil, errImport
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, errImport
	}
	actual, err := filepath.EvalSymlinks(full)
	if err != nil || actual != full {
		return nil, errImport
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, errImport
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errImport
	}
	data, err := io.ReadAll(io.LimitReader(f, 128<<10+1))
	if err != nil || len(data) > 128<<10 {
		return nil, errImport
	}
	return data, nil
}

func candidateToken(root string, app composeadoption.Application) (string, error) {
	hashes := map[string]string{}
	paths := append([]string(nil), app.ComposeFiles...)
	for _, input := range append(append([]composeadoption.InputFile{}, app.DotenvFiles...), app.SharedEnvFiles...) {
		if input.Exists {
			paths = append(paths, input.Path)
		}
	}
	for _, path := range paths {
		data, err := importRead(root, path)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		hashes[path] = hex.EncodeToString(sum[:])
	}
	containers := append([]composeadoption.RuntimeContainer(nil), app.Containers...)
	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })
	data, _ := json.Marshal(struct {
		Directory, Project string
		Hashes             map[string]string
		Containers         []composeadoption.RuntimeContainer
	}{app.Directory, app.ProjectName, hashes, containers})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r *Runner) scanImports(ctx context.Context) (string, composeadoption.Preview, error) {
	root, err := r.managedRoot()
	if err != nil {
		return "", composeadoption.Preview{}, err
	}
	observed, err := r.observeImports(ctx)
	if err != nil {
		return "", composeadoption.Preview{}, err
	}
	preview, err := composeadoption.DiscoverContext(ctx, root, observed)
	return root, preview, err
}

func importCandidate(root string, app composeadoption.Application) protocol.ComposeImportCandidate {
	c := protocol.ComposeImportCandidate{Directory: app.Directory, ProjectName: app.ProjectName, State: app.RuntimeStatus, Containers: len(app.Containers), ComposeFiles: app.ComposeFiles, EnvironmentFiles: []string{}, Issues: append([]string{}, app.Unsupported...)}
	if app.Directory == "." {
		c.Issues = append(c.Issues, "The project must have its own folder inside the approved Compose root.")
	}
	for _, container := range app.Containers {
		for _, mount := range container.StorageBinds {
			source := mount.Source
			if mount.Type == "volume" {
				source = mount.Name
			}
			c.Storage = append(c.Storage, mount.Type+": "+source+" → "+mount.Destination)
		}
	}
	if app.RuntimeStatus != "running" && app.RuntimeStatus != "stopped" {
		c.Issues = append(c.Issues, "A complete match to the running Compose project is required.")
	}
	if len(app.ConfigFiles) > 0 {
		c.Issues = append(c.Issues, "File-based configs and secrets need migration before import.")
	}
	if len(app.SharedEnvVariables) > 0 {
		c.Issues = append(c.Issues, "Resolve shared-root interpolation into a project-local .env before importing.")
	}
	for _, input := range app.DotenvFiles {
		if input.Exists {
			c.EnvironmentFiles = append(c.EnvironmentFiles, input.Path)
		}
	}
	for _, path := range app.ComposeFiles {
		data, err := importRead(root, path)
		if err != nil {
			c.Issues = append(c.Issues, "A source file could not be read safely.")
			continue
		}
		var doc map[string]any
		if yaml.Unmarshal(data, &doc) != nil {
			c.Issues = append(c.Issues, "Invalid Compose YAML.")
			continue
		}
		for _, key := range []string{"include", "configs", "secrets"} {
			if _, ok := doc[key]; ok {
				c.Issues = append(c.Issues, "Includes and file-based configs or secrets require migration.")
			}
		}
		if services, ok := doc["services"].(map[string]any); ok {
			for _, value := range services {
				if service, ok := value.(map[string]any); ok {
					if raw, exists := service["env_file"]; exists {
						if _, err := environmentReferences(raw); err != nil {
							c.Issues = append(c.Issues, "Service environment files must use required, relative file paths.")
						}
					}
					for _, key := range []string{"build", "develop", "extends", "post_start", "pre_start", "pre_stop", "label_file", "credential_spec", "use_api_socket", "provider", "profiles"} {
						if _, exists := service[key]; exists {
							c.Issues = append(c.Issues, "This stack uses a Compose feature that the deployment runner does not support.")
						}
					}
				}
			}
		}
	}
	token, err := candidateToken(root, app)
	if err != nil {
		c.Issues = append(c.Issues, "An environment file could not be read safely.")
	} else {
		c.Token = token
	}
	return c
}

func (r *Runner) DiscoverImports(ctx context.Context) (protocol.ComposeDiscovery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, preview, err := r.scanImports(ctx)
	out := protocol.ComposeDiscovery{Root: root, Candidates: []protocol.ComposeImportCandidate{}}
	if err != nil {
		return out, errImport
	}
	for _, app := range preview.Applications {
		if app.ProjectName != "" {
			out.Candidates = append(out.Candidates, importCandidate(root, app))
		}
	}
	return out, nil
}

func (r *Runner) findImport(ctx context.Context, q protocol.ComposeImportRequest) (string, composeadoption.Application, error) {
	root, preview, err := r.scanImports(ctx)
	if err != nil {
		return root, composeadoption.Application{}, errImport
	}
	for _, app := range preview.Applications {
		if app.Directory != q.Directory {
			continue
		}
		candidate := importCandidate(root, app)
		if len(candidate.Issues) > 0 || candidate.Token == "" || candidate.Token != q.Token || !stack.ValidProject(app.ProjectName) {
			return root, app, errImport
		}
		return root, app, nil
	}
	return root, composeadoption.Application{}, errImport
}

func (r *Runner) PreviewImport(ctx context.Context, q protocol.ComposeImportRequest) (protocol.ComposeImportPreview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, app, err := r.findImport(ctx, q)
	if err != nil {
		return protocol.ComposeImportPreview{}, err
	}
	out := protocol.ComposeImportPreview{Candidate: importCandidate(root, app), Files: map[string]string{}}
	for _, path := range app.ComposeFiles {
		data, err := importRead(root, path)
		if err != nil {
			return out, err
		}
		out.Files[path] = string(data)
	}
	return out, nil
}

func (r *Runner) identityPath() string {
	if r.Identity != "" {
		return r.Identity
	}
	return filepath.Join(r.ManagedRoot, ".vigara-import", "identity.age")
}

func (r *Runner) PrepareImport(ctx context.Context, q protocol.ComposeImportRequest) (protocol.ComposeImportPrepared, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out protocol.ComposeImportPrepared
	if !stack.ValidID(q.ID) || len(strings.TrimSpace(q.Name)) == 0 || len(q.Name) > 120 {
		return out, stack.ErrInvalid
	}
	root, app, err := r.findImport(ctx, q)
	if err != nil {
		return out, err
	}
	key, err := composesecrets.EnsureAgeIdentity(r.identityPath())
	if err != nil {
		return out, err
	}
	recipients := []string{q.EditorRecipient, key, q.RecoveryRecipient}
	selection := composeadoption.ExportSelection{AcknowledgedReview: app.ReviewRequired, ApprovedPlainCompose: app.ComposeFiles, InterpolationOrderConfirmed: true}
	for _, input := range app.DotenvFiles {
		if input.SourceKind == "localInterpolation" {
			selection.InterpolationOrder = append(selection.InterpolationOrder, input.Path)
		}
	}
	before := composeadoption.RuntimeSnapshot{ObservedAt: time.Now().UTC(), Complete: true, Containers: app.Containers}
	prepared, err := composeadoption.PrepareImport(ctx, root, app, before, selection, recipients, composesecrets.EncryptAgeDotenv, composesecrets.IsAgeArtifact)
	if err != nil {
		return out, errImport
	}
	out.Files = map[string]string{}
	// The preparation contract defaults to SOPS for existing callers. Imported
	// browser stacks use native age armor and do not require a host SOPS binary.
	for _, artifact := range prepared.Files {
		path := artifact.Path
		if artifact.Sensitivity == "encrypted" {
			path = strings.TrimSuffix(path, ".sops") + ".age"
		}
		out.Files[path] = string(artifact.Content)
	}
	m := stack.Manifest{APIVersion: "vigara.io/v1", Kind: "Stack", StackID: q.ID, DisplayName: q.Name, ProjectName: app.ProjectName, ConfigurationRevisionPolicy: "sha256-git-tree-and-placement-inputs-v1"}
	m.Compose.Files = prepared.Receipt.ComposeFiles
	m.Compose.ProjectDirectory = "."
	m.Compose.InterpolationFiles = []stack.Input{}
	m.SecretRecipientRefs = []stack.RecipientRef{{Class: "central-editor", KeyRef: "import-editor"}, {Class: "target-agent", KeyRef: "import-agent"}, {Class: "operator-recovery", KeyRef: "import-recovery"}}
	seenInputs, seenTargets := map[string]bool{}, map[string]bool{}
	for _, input := range prepared.Receipt.Inputs {
		path := strings.TrimSuffix(input.EncryptedPath, ".sops") + ".age"
		if out.Files[path] == "" {
			return out, errImport
		}
		if input.Purpose != "deployment" && !seenInputs[path] {
			m.Compose.InterpolationFiles = append(m.Compose.InterpolationFiles, stack.Input{Path: path, Purpose: "stack", Sensitivity: "encrypted", Format: composesecrets.AgeFormat})
			seenInputs[path] = true
		}
		if !seenTargets[input.SourcePath] && input.SourcePath != "" {
			m.Compose.EnvironmentFiles = append(m.Compose.EnvironmentFiles, stack.Input{Path: path, Target: input.SourcePath, Purpose: "deployment", Sensitivity: "encrypted", Format: composesecrets.AgeFormat})
			seenTargets[input.SourcePath] = true
		}
	}
	raw, _ := yaml.Marshal(m)
	out.Files["vigara-stack.yaml"] = string(raw)
	if _, err = stack.Validate(stack.Snapshot{ID: q.ID, Files: out.Files}); err != nil {
		return out, err
	}
	if _, _, err = r.findImport(ctx, q); err != nil {
		return out, err
	}
	out.AgentRecipient = key
	out.Target = protocol.ComposeTargetInfo{ID: q.ID, ProjectName: app.ProjectName, Directory: filepath.Join(root, filepath.FromSlash(app.Directory))}
	return out, nil
}

func (r *Runner) ConfirmImport(ctx context.Context, q protocol.ComposeImportRequest) (protocol.ComposeTargetInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !stack.ValidID(q.ID) {
		return protocol.ComposeTargetInfo{}, stack.ErrInvalid
	}
	root, app, err := r.findImport(ctx, q)
	if err != nil {
		return protocol.ComposeTargetInfo{}, err
	}
	target := Target{ProjectName: app.ProjectName, Directory: filepath.Join(root, filepath.FromSlash(app.Directory)), Imported: true}
	if existing, ok := r.lookup(q.ID); ok {
		if existing == target {
			return protocol.ComposeTargetInfo{ID: q.ID, ProjectName: target.ProjectName, Directory: target.Directory}, nil
		}
		return protocol.ComposeTargetInfo{}, errTarget
	}
	for id, t := range r.Targets {
		if id != q.ID && (t.Directory == target.Directory || t.ProjectName == target.ProjectName) {
			return protocol.ComposeTargetInfo{}, errTarget
		}
	}
	directory := filepath.Join(root, ".vigara-import", "targets")
	current := root
	for _, part := range []string{".vigara-import", "targets"} {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return protocol.ComposeTargetInfo{}, errTarget
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return protocol.ComposeTargetInfo{}, errTarget
		}
	}
	if actual, err := filepath.EvalSymlinks(directory); err != nil || actual != directory {
		return protocol.ComposeTargetInfo{}, errTarget
	}
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if t, ok := r.lookup(id); ok && (t.Directory == target.Directory || t.ProjectName == target.ProjectName) {
			return protocol.ComposeTargetInfo{}, errTarget
		}
	}
	file := filepath.Join(directory, q.ID+".json")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return protocol.ComposeTargetInfo{}, errTarget
	}
	data, _ := json.Marshal(target)
	_, err = f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err != nil || syncErr != nil || closeErr != nil {
		return protocol.ComposeTargetInfo{}, errTarget
	}
	return protocol.ComposeTargetInfo{ID: q.ID, ProjectName: target.ProjectName, Directory: target.Directory}, nil
}
