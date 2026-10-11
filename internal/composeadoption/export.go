package composeadoption

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const encryptedArtifactFormat = "sops-binary"

type DotenvEncryptor func(context.Context, []byte, []string) ([]byte, error)
type CiphertextValidator func([]byte) bool

type ExportSelection struct {
	InterpolationOrder          []string
	InterpolationOrderConfirmed bool
	AcknowledgedReview          []string
	ApprovedPlainCompose        []string
	ApprovedPlainConfigs        []string
}

// ExportArtifact is a prepared in-memory file for a later repository writer.
// Content is excluded from JSON so plaintext Compose/config assets and
// ciphertext can never enter routine diagnostics through serialization.
type ExportArtifact struct {
	Path        string `json:"path"`
	SourcePath  string `json:"sourcePath"`
	Sensitivity string `json:"sensitivity"`
	Format      string `json:"format,omitempty"`
	Content     []byte `json:"-"`
}

type ExportReceipt struct {
	ProjectName        string               `json:"projectName"`
	Directory          string               `json:"directory"`
	ProjectDirectory   string               `json:"projectDirectory"`
	RuntimeStatus      string               `json:"runtimeStatus"`
	ComposeFiles       []string             `json:"composeFiles"`
	ComposeSourceFiles []string             `json:"composeSourceFiles"`
	Inputs             []ExportInputMapping `json:"inputs"`
	RuntimeObservation RuntimeSnapshot      `json:"runtimeObservation"`
	SharedEnvVariables []string             `json:"sharedEnvVariables"`
	ObservedAt         string               `json:"observedAt"`
}

type ExportInputMapping struct {
	OriginPath         string `json:"originPath"`
	SourcePath         string `json:"sourcePath"`
	EncryptedPath      string `json:"encryptedPath"`
	Purpose            string `json:"purpose"`
	Format             string `json:"format"`
	ComposeFile        string `json:"composeFile,omitempty"`
	Service            string `json:"service,omitempty"`
	Order              int    `json:"order,omitempty"`
	InterpolationOrder int    `json:"interpolationOrder,omitempty"`
}

type PreparedImport struct {
	Files   []ExportArtifact `json:"files"`
	Receipt ExportReceipt    `json:"receipt"`
}

// PrepareImport prepares the explicitly selected application's Compose and
// reviewed supporting files, encrypting every selected dotenv file in memory.
// It does not write Git, update management ownership, or contact Docker. The
// caller must persist the returned bundle through the repository writer and
// verify a fresh after-observation with VerifyUnchanged before recording
// adoption.
func PrepareImport(ctx context.Context, root string, app Application, before RuntimeSnapshot, selection ExportSelection, recipients []string, encrypt DotenvEncryptor, validateCiphertext CiphertextValidator) (PreparedImport, error) {
	if err := ctx.Err(); err != nil {
		return PreparedImport{}, err
	}
	if encrypt == nil || validateCiphertext == nil {
		return PreparedImport{}, errors.New("SOPS dotenv encryption and ciphertext validation are unavailable")
	}
	if !before.Complete || validateRuntimeSnapshot(before) != nil {
		return PreparedImport{}, errors.New("complete runtime observation is required for import preparation")
	}
	if app.ProjectName == "" || (app.RuntimeStatus != "running" && app.RuntimeStatus != "stopped") {
		return PreparedImport{}, errors.New("one matched running or stopped Compose application must be selected")
	}
	if len(app.Unsupported) > 0 {
		return PreparedImport{}, errors.New("application contains unsupported Compose inputs")
	}
	for _, review := range app.ReviewRequired {
		if !contains(selection.AcknowledgedReview, review) {
			return PreparedImport{}, errors.New("application review items must be explicitly acknowledged")
		}
	}
	if !hasRuntimeProject(before, app) {
		return PreparedImport{}, errors.New("selected application does not match the captured runtime observation")
	}
	if len(recipients) < 3 || hasDuplicatesOrEmpty(recipients) {
		return PreparedImport{}, errors.New("dotenv import requires three distinct recipient references resolved to age recipients")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return PreparedImport{}, errors.New("could not resolve import root")
	}
	rootReal, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return PreparedImport{}, errors.New("could not resolve import root safely")
	}
	rootReal, err = filepath.Abs(rootReal)
	if err != nil {
		return PreparedImport{}, errors.New("could not resolve import root safely")
	}

	approvedCompose := stringSet(selection.ApprovedPlainCompose)
	if len(approvedCompose) != len(app.ComposeFiles) {
		return PreparedImport{}, errors.New("every plaintext Compose file requires explicit non-secret review")
	}
	files := []ExportArtifact{}
	composeDestinations := []string{}
	for _, compose := range app.ComposeFiles {
		if err := ctx.Err(); err != nil {
			return PreparedImport{}, err
		}
		source := filepath.Join(rootReal, filepath.FromSlash(compose))
		data, err := safeReadWithin(rootReal, source, maxComposeBytes)
		if err != nil || app.SourceHashes[compose] == "" || contentHash(data) != app.SourceHashes[compose] {
			return PreparedImport{}, errors.New("Compose file changed or became unreadable during import preparation")
		}
		if !approvedCompose[compose] {
			return PreparedImport{}, errors.New("Compose file requires explicit non-secret review")
		}
		dest := stackRelative(app.Directory, compose)
		files = append(files, ExportArtifact{Path: dest, SourcePath: compose, Sensitivity: "plaintext", Content: data})
		composeDestinations = append(composeDestinations, dest)
	}

	if (len(app.SharedEnvFiles) > 0 || hasSourceKind(app.DotenvFiles, "localInterpolation")) && !selection.InterpolationOrderConfirmed {
		return PreparedImport{}, errors.New("the explicit interpolation input order must be confirmed, including any shared root dotenv")
	}
	if len(app.SharedEnvVariables) > 0 && !contains(selection.InterpolationOrder, ".env") {
		return PreparedImport{}, errors.New("Compose references root-shared variables; preserve the shared .env input or resolve those variables before export")
	}
	candidates := map[string]InputFile{}
	for _, input := range app.DotenvFiles {
		if input.SourceKind == "localInterpolation" {
			candidates[input.Path] = input
		}
	}
	for _, input := range app.SharedEnvFiles {
		candidates[input.Path] = input
	}
	interpSeen := map[string]bool{}
	for _, path := range selection.InterpolationOrder {
		if interpSeen[path] {
			return PreparedImport{}, errors.New("interpolation input order contains duplicate paths")
		}
		interpSeen[path] = true
		if _, ok := candidates[path]; !ok {
			return PreparedImport{}, errors.New("interpolation input order names an unobserved file")
		}
	}
	inputsByPath := map[string]InputFile{}
	for _, input := range app.DotenvFiles {
		if input.SourceKind == "serviceEnvFile" {
			inputsByPath[input.Path] = input
		}
	}
	inputPaths := []string{}
	inputMappings := []ExportInputMapping{}
	for _, ref := range app.EnvFileReferences {
		origin, target := resolveEnvReference(rootReal, app, ref.SourcePath)
		if origin == "" {
			return PreparedImport{}, errors.New("service env_file path could not be mapped safely")
		}
		input, ok := inputsByPath[origin]
		if !ok {
			if !ref.Required {
				continue
			}
			return PreparedImport{}, errors.New("required service env_file was not discovered")
		}
		if input.Exists {
			inputPaths = appendUnique(inputPaths, origin)
		}
		inputMappings = append(inputMappings, ExportInputMapping{OriginPath: origin, SourcePath: target, EncryptedPath: encryptedPath(stackRelative(app.Directory, origin)), Purpose: "deployment", Format: encryptedArtifactFormat, ComposeFile: stackRelative(app.Directory, ref.ComposeFile), Service: ref.Service, Order: ref.Order})
	}
	for order, path := range selection.InterpolationOrder {
		input := candidates[path]
		if !input.Exists {
			return PreparedImport{}, errors.New("selected interpolation input is missing")
		}
		inputPaths = appendUnique(inputPaths, path)
		target := filepath.ToSlash(filepath.Join("secrets", "imported", "interpolation-"+twoDigits(order)+".env.sops"))
		if _, alsoDeploymentInput := inputsByPath[path]; alsoDeploymentInput {
			target = encryptedPath(stackRelative(app.Directory, path))
		}
		inputMappings = append(inputMappings, ExportInputMapping{OriginPath: path, SourcePath: ".env", EncryptedPath: target, Purpose: input.Purpose, Format: encryptedArtifactFormat, InterpolationOrder: order})
	}
	seen := map[string]bool{}
	for _, path := range inputPaths {
		if err := ctx.Err(); err != nil {
			return PreparedImport{}, err
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		input, ok := inputsByPath[path]
		if !ok {
			for _, candidate := range candidates {
				if candidate.Path == path {
					input = candidate
					ok = true
					break
				}
			}
		}
		if !ok {
			return PreparedImport{}, errors.New("dotenv input metadata is missing")
		}
		if input.Sensitivity != "encrypted" || !input.Exists {
			return PreparedImport{}, errors.New("required dotenv input is missing or not marked for encryption")
		}
		source := filepath.Join(rootReal, filepath.FromSlash(input.Path))
		data, err := safeReadWithin(rootReal, source, maxComposeBytes)
		if err != nil {
			return PreparedImport{}, errors.New("dotenv input changed or became unreadable during import preparation")
		}
		ciphertext, err := encrypt(ctx, data, append([]string(nil), recipients...))
		if err != nil || !validateCiphertext(ciphertext) {
			return PreparedImport{}, errors.New("dotenv encryption failed")
		}
		destination := ""
		for _, mapping := range inputMappings {
			if mapping.OriginPath == input.Path {
				destination = mapping.EncryptedPath
				break
			}
		}
		if destination == "" {
			destination = encryptedPath(stackRelative(app.Directory, input.Path))
		}
		files = append(files, ExportArtifact{Path: destination, SourcePath: input.Path, Sensitivity: "encrypted", Format: encryptedArtifactFormat, Content: ciphertext})
	}

	approved := map[string]bool{}
	for _, path := range selection.ApprovedPlainConfigs {
		approved[path] = true
	}
	for _, input := range app.ConfigFiles {
		if !approved[input.Path] {
			return PreparedImport{}, errors.New("unclassified configuration files require explicit non-secret review")
		}
		source := filepath.Join(rootReal, filepath.FromSlash(input.Path))
		data, err := safeReadWithin(rootReal, source, maxComposeBytes)
		if err != nil || app.SourceHashes[input.Path] == "" || contentHash(data) != app.SourceHashes[input.Path] {
			return PreparedImport{}, errors.New("configuration file changed or became unreadable during import preparation")
		}
		files = append(files, ExportArtifact{Path: stackRelative(app.Directory, input.Path), SourcePath: input.Path, Sensitivity: "plaintext", Content: data})
	}
	selectedBefore := RuntimeSnapshot{ObservedAt: before.ObservedAt, Complete: true, Containers: cloneContainers(app.Containers)}
	return PreparedImport{Files: files, Receipt: ExportReceipt{ProjectName: app.ProjectName, Directory: app.Directory, ProjectDirectory: ".", RuntimeStatus: app.RuntimeStatus, ComposeFiles: composeDestinations, ComposeSourceFiles: append([]string(nil), app.ComposeFiles...), Inputs: inputMappings, RuntimeObservation: selectedBefore, SharedEnvVariables: append([]string(nil), app.SharedEnvVariables...), ObservedAt: before.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}}, nil
}

// VerifyPreparedImportUnchanged narrows a fresh complete observation to the
// selected Compose project and checks container IDs, start times, labels,
// configuration paths, state, and every storage binding against the receipt.
func VerifyPreparedImportUnchanged(receipt ExportReceipt, after RuntimeSnapshot) error {
	if !after.Complete {
		return errors.New("complete after-observation is required")
	}
	selected := []RuntimeContainer{}
	for _, c := range after.Containers {
		if c.ProjectName == receipt.ProjectName {
			selected = append(selected, c)
		}
	}
	return VerifyUnchanged(receipt.RuntimeObservation, RuntimeSnapshot{ObservedAt: after.ObservedAt, Complete: true, Containers: selected})
}

func hasRuntimeProject(snapshot RuntimeSnapshot, app Application) bool {
	containers := groupRuntimeByProject(snapshot.Containers)[app.ProjectName]
	if len(containers) == 0 || len(containers) != len(app.Containers) {
		return false
	}
	return observationDigest(RuntimeSnapshot{Containers: containers}) == observationDigest(RuntimeSnapshot{Containers: app.Containers})
}

func safeReadWithin(root, path string, max int64) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || !within(root, absolute) {
		return nil, errors.New("path escapes root")
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil || !within(root, real) {
		return nil, errors.New("path escapes root")
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() || info.Size() > max {
		return nil, errors.New("file unavailable")
	}
	return readBounded(real, max)
}

func unsafeComposeBytes(data []byte) bool {
	var model map[string]any
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&model) != nil || model == nil {
		return true
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return true
	}
	return containsSensitiveField(model) || containsCredentialURL(model)
}

func hasDuplicatesOrEmpty(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if strings.TrimSpace(v) == "" || seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		if v != "" {
			out[v] = true
		}
	}
	return out
}
func hasSourceKind(inputs []InputFile, kind string) bool {
	for _, v := range inputs {
		if v.SourceKind == kind {
			return true
		}
	}
	return false
}
func stackRelative(stackDirectory, source string) string {
	base := filepath.FromSlash(stackDirectory)
	if base == "." {
		base = ""
	}
	relative, err := filepath.Rel(base, filepath.FromSlash(source))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return ""
	}
	return filepath.ToSlash(relative)
}
func encryptedPath(path string) string {
	return filepath.ToSlash(filepath.Join("secrets", "imported", filepath.FromSlash(path)+".sops"))
}
func twoDigits(i int) string { return fmt.Sprintf("%02d", i) }
func resolveEnvReference(root string, app Application, source string) (string, string) {
	if source == "" || filepath.IsAbs(filepath.FromSlash(source)) || filepath.VolumeName(source) != "" || strings.Contains(source, "${") {
		return "", ""
	}
	clean := filepath.Clean(filepath.FromSlash(source))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ""
	}
	base := app.ProjectDirectory
	if base == "" {
		base = app.Directory
	}
	absolute := filepath.Join(root, filepath.FromSlash(base), clean)
	if !within(root, absolute) {
		return "", ""
	}
	return rel(root, absolute), filepath.ToSlash(clean)
}
func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
func cloneContainers(source []RuntimeContainer) []RuntimeContainer {
	out := append([]RuntimeContainer(nil), source...)
	for i := range out {
		out[i].ComposeFiles = append([]string(nil), source[i].ComposeFiles...)
		out[i].StorageBinds = append([]StorageBind(nil), source[i].StorageBinds...)
	}
	return out
}
