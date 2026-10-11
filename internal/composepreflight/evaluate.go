package composepreflight

import (
	"fmt"
	"net"
	"net/netip"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxEvidenceAge = 5 * time.Minute

// Evaluate builds a review report from caller-supplied data. It never treats
// a snapshot's self-declared Source field as authenticated agent evidence.
func Evaluate(model RequirementModel, host HostSnapshot, now time.Time) Report {
	host.Source = "supplied"
	return evaluate(model, host, now)
}

// EvaluateObserved is for a trusted server integration after it has
// authenticated the target agent and bound this snapshot to the requested
// profile/assignment. The package itself cannot authenticate a remote caller.
func EvaluateObserved(model RequirementModel, host HostSnapshot, now time.Time) Report {
	host.Source = "observed-agent"
	return evaluate(model, host, now)
}

// evaluate compares reduced requirements with evidence. `source` is assigned
// by the caller-facing entry point, never taken from snapshot input.
func evaluate(model RequirementModel, host HostSnapshot, now time.Time) Report {
	r := Report{ProjectName: safeToken(model.ProjectName, 63), GeneratedAt: now.UTC(), EvidenceSource: validSource(host.Source), Ready: true, Findings: []Finding{}}
	if !host.CapturedAt.IsZero() {
		t := host.CapturedAt.UTC()
		r.EvidenceCapturedAt = &t
	}
	usable := host.Source == "observed-agent" && host.Complete && !host.CollectionError && !host.CapturedAt.IsZero() && !host.CapturedAt.After(now.Add(time.Minute)) && now.Sub(host.CapturedAt) <= maxEvidenceAge
	if !usable {
		code := "HOST_EVIDENCE_UNKNOWN"
		msg := "Host evidence is missing, supplied, incomplete, stale, or could not be collected; host requirements remain unverified."
		if host.CollectionError {
			msg = "Host evidence collection failed; host requirements remain unverified."
		}
		if host.CapturedAt.IsZero() || host.Source != "observed-agent" {
			msg = "Host evidence was not observed by the target agent; host requirements remain unverified."
		}
		r.add(code, StatusUnknown, SeverityBlocker, "host", msg)
	}
	if strings.TrimSpace(model.ProjectName) == "" || strings.TrimSpace(model.ProjectDirectory) == "" || !isTargetAbs(model.ProjectDirectory) {
		r.add("COMPOSE_PROJECT_IDENTITY_MISSING", StatusBlocked, SeverityBlocker, "project", "A stable Compose project name and absolute project directory are required.")
	} else if !projectNameRE.MatchString(model.ProjectName) {
		r.add("COMPOSE_PROJECT_IDENTITY_INVALID", StatusBlocked, SeverityBlocker, "project", "Compose project name is invalid.")
	}
	checkVersion(&r, "docker", model.DockerVersion, host.DockerVersion, usable)
	checkVersion(&r, "compose", model.ComposeVersion, host.ComposeVersion, usable)
	switch model.ConfigurationValid {
	case EvidenceAvailable:
		r.add("CONFIGURATION_VALID", StatusSatisfied, SeverityInfo, "compose", "Compose configuration syntax was validated.")
	case EvidenceUnavailable:
		r.add("CONFIGURATION_INVALID", StatusBlocked, SeverityBlocker, "compose", "Compose configuration validation failed; details were omitted.")
	default:
		r.add("CONFIGURATION_UNKNOWN", StatusUnknown, SeverityBlocker, "compose", "Compose configuration syntax has not been validated.")
	}
	if model.HostNetwork {
		if !usable || host.HostNetworking != EvidenceAvailable && host.HostNetworking != EvidenceUnavailable {
			r.add("HOST_NETWORK_UNKNOWN", StatusUnknown, SeverityBlocker, "host-network", "Host network capability could not be verified.")
		} else if host.HostNetworking == EvidenceUnavailable {
			r.add("HOST_NETWORK_UNAVAILABLE", StatusBlocked, SeverityBlocker, "host-network", "The host network requirement is unavailable.")
		} else {
			r.add("HOST_NETWORK_REQUIRED", StatusSatisfied, SeverityWarning, "host-network", "Host networking is available and bypasses port isolation.")
		}
	}
	for _, e := range model.RequiredEnvironment {
		state := e.State
		if usable {
			if v, ok := host.Environment[e.Name]; ok {
				state = validEvidence(v)
			} else {
				state = EvidenceUnknown
			}
		} else {
			state = EvidenceUnknown
		}
		resource := safeToken(e.Name, 96)
		if resource == "" {
			resource = "environment-input"
		}
		switch state {
		case EvidenceAvailable:
			r.add("ENVIRONMENT_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "A required environment input is available; its value is never reported.")
		case EvidenceUnavailable:
			r.add("ENVIRONMENT_MISSING", StatusBlocked, SeverityBlocker, resource, "A required environment input is unavailable; its value is never reported.")
		default:
			r.add("ENVIRONMENT_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "Availability of a required environment input is unknown; its value is never reported.")
		}
	}
	for _, registry := range model.RequiredRegistries {
		identity, valid := canonicalRegistry(registry)
		if !valid {
			r.add("REGISTRY_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, "registry", "Registry requirement must be a hostname or IP address with an optional port; credentials and paths are not accepted.")
			continue
		}
		state := EvidenceUnknown
		if usable {
			for observed, v := range host.RegistryAccess {
				observedIdentity, ok := canonicalRegistry(observed)
				if ok && observedIdentity == identity {
					state = validEvidence(v)
					break
				}
			}
		}
		if state == EvidenceAvailable {
			r.add("REGISTRY_PREREQUISITE_AVAILABLE", StatusSatisfied, SeverityInfo, identity, "Registry access prerequisite is available.")
		} else if state == EvidenceUnavailable {
			r.add("REGISTRY_PREREQUISITE_MISSING", StatusBlocked, SeverityBlocker, identity, "Registry access prerequisite is unavailable.")
		} else {
			r.add("REGISTRY_PREREQUISITE_UNKNOWN", StatusUnknown, SeverityBlocker, identity, "Registry access prerequisite was not verified; credentials and raw errors are omitted.")
		}
	}
	for _, n := range model.Networks {
		checkNetwork(&r, n, host.Networks, usable)
	}
	for _, v := range model.Volumes {
		checkVolume(&r, v, host.Volumes, usable)
		if v.Persistent && v.BackupPolicyID == "" {
			r.add("BACKUP_POLICY_UNASSIGNED", StatusBlocked, SeverityBlocker, safeToken(v.Name, 255), "Persistent volume has no linked backup policy.")
		}
		if v.BackupPolicyID != "" {
			checkBackupPolicy(&r, BackupRequirement{Resource: "volume " + safeToken(v.Name, 255), PolicyID: v.BackupPolicyID}, host.BackupPolicies, usable)
		}
	}
	for _, m := range model.Mounts {
		checkMount(&r, m, model.ProjectDirectory, host.Paths, usable)
		if m.Persistent && m.BackupPolicyID == "" {
			r.add("BACKUP_POLICY_UNASSIGNED", StatusBlocked, SeverityBlocker, safeToken(m.Target, 255), "Persistent bind data has no linked backup policy.")
		}
		if m.BackupPolicyID != "" {
			checkBackupPolicy(&r, BackupRequirement{Resource: safeToken(m.Source, 1024), PolicyID: m.BackupPolicyID}, host.BackupPolicies, usable)
		}
	}
	for _, p := range model.Ports {
		checkPort(&r, p, model.ProjectName, host.Ports, usable)
	}
	checkModelPortConflicts(&r, model.Ports)
	for _, d := range model.Devices {
		checkDevice(&r, d, host.Devices, usable)
	}
	for _, b := range model.BackupPolicies {
		checkBackupPolicy(&r, b, host.BackupPolicies, usable)
	}
	for _, a := range model.ConfigurationAssets {
		checkAsset(&r, a, model.ProjectDirectory, host, usable)
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		if r.Findings[i].Code == r.Findings[j].Code {
			return r.Findings[i].Resource < r.Findings[j].Resource
		}
		return r.Findings[i].Code < r.Findings[j].Code
	})
	for _, f := range r.Findings {
		if f.Severity == SeverityBlocker && (f.Status == StatusBlocked || f.Status == StatusUnknown || f.Status == StatusPlanned) {
			r.Ready = false
			break
		}
	}
	return r
}

func (r *Report) add(code string, status FindingStatus, severity Severity, resource, message string) {
	r.Findings = append(r.Findings, Finding{Code: code, Status: status, Severity: severity, Resource: resource, Message: message})
}

func canonicalRegistry(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/@\\?#") {
		return "", false
	}
	host, port := "", ""
	if strings.HasPrefix(value, "[") {
		var err error
		host, port, err = net.SplitHostPort(value)
		if err != nil {
			host = strings.Trim(value, "[]")
			if host == value {
				return "", false
			}
		}
	} else if strings.Count(value, ":") == 1 {
		var err error
		host, port, err = net.SplitHostPort(value)
		if err != nil {
			return "", false
		}
	} else {
		host = value
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		port = strconv.Itoa(n)
	}
	addr, err := netip.ParseAddr(host)
	if err == nil {
		host = addr.Unmap().String()
	} else {
		host = strings.ToLower(host)
		if len(host) > 253 || !dnsNameRE.MatchString(host) {
			return "", false
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		return net.JoinHostPort(strings.Trim(host, "[]"), port), true
	}
	return host, true
}
func validEvidence(s EvidenceState) EvidenceState {
	if s == EvidenceAvailable || s == EvidenceUnavailable {
		return s
	}
	return EvidenceUnknown
}
func validSource(s string) string {
	if s == "observed-agent" || s == "supplied" {
		return s
	}
	return "unknown"
}
func safeToken(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

var projectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var dnsNameRE = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)
var versionRE = regexp.MustCompile(`(?:^|[^0-9])v?([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)

func checkVersion(r *Report, name, minimum, observed string, usable bool) {
	if minimum == "" {
		return
	}
	if !usable {
		r.add("CAPABILITY_VERSION_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Required Docker or Compose version could not be verified.")
		return
	}
	need, got := versionRE.FindStringSubmatch(minimum), versionRE.FindStringSubmatch(observed)
	if need == nil || got == nil {
		r.add("CAPABILITY_VERSION_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Required Docker or Compose version could not be compared.")
		return
	}
	for i := 1; i <= 3; i++ {
		a, b := 0, 0
		if need[i] != "" {
			a, _ = strconv.Atoi(need[i])
		}
		if got[i] != "" {
			b, _ = strconv.Atoi(got[i])
		}
		if b > a {
			r.add("CAPABILITY_VERSION_AVAILABLE", StatusSatisfied, SeverityInfo, name, "Required capability version is available.")
			return
		}
		if b < a {
			r.add("CAPABILITY_VERSION_TOO_OLD", StatusBlocked, SeverityBlocker, name, "Installed Docker or Compose version is below the required version.")
			return
		}
	}
	r.add("CAPABILITY_VERSION_AVAILABLE", StatusSatisfied, SeverityInfo, name, "Required capability version is available.")
}
func checkAsset(r *Report, a AssetRequirement, projectDir string, host HostSnapshot, usable bool) {
	resource := safeToken(a.Path, 1024)
	if resource == "" {
		resource = "asset"
	}
	severity := SeverityBlocker
	if !a.Required {
		severity = SeverityWarning
	}
	switch a.Class {
	case AssetPortableConfig:
		state := validEvidence(a.State)
		if state == EvidenceAvailable {
			r.add("PORTABLE_CONFIG_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "Portable configuration asset is present in the selected saved revision; contents are not included.")
		} else if state == EvidenceUnavailable {
			r.add("PORTABLE_CONFIG_MISSING", StatusBlocked, severity, resource, "Required portable configuration asset is unavailable.")
		} else {
			r.add("PORTABLE_CONFIG_UNKNOWN", StatusUnknown, severity, resource, "Portable configuration asset availability was not verified.")
		}
	case AssetHostFile:
		checkHostFile(r, a, projectDir, host.Paths, usable, resource, severity)
	case AssetHostDevice:
		checkDevice(r, DeviceRequirement{HostPath: a.Path, Required: a.Required}, host.Devices, usable)
	case AssetPersistentData:
		if a.BackupPolicyID == "" {
			r.add("BACKUP_POLICY_UNASSIGNED", StatusBlocked, severity, resource, "Persistent data has no linked backup policy.")
			return
		}
		checkBackupPolicy(r, BackupRequirement{Resource: resource, PolicyID: a.BackupPolicyID}, host.BackupPolicies, usable)
	default:
		r.add("ASSET_CLASS_INVALID", StatusBlocked, SeverityBlocker, resource, "Configuration asset has an unsupported class.")
	}
}
func checkHostFile(r *Report, a AssetRequirement, projectDir string, observations []ObservedPath, usable bool, resource string, severity Severity) {
	requested, relative, err := requestedPath(a.Path, projectDir)
	if err != nil {
		r.add("HOST_FILE_PATH_INVALID", StatusBlocked, severity, resource, "Host-provided file path is invalid.")
		return
	}
	if !usable {
		r.add("HOST_FILE_UNKNOWN", StatusUnknown, severity, resource, "Host-provided file availability was not observed.")
		return
	}
	for _, o := range observations {
		if cleanTargetPath(o.Path) != requested {
			continue
		}
		if !o.Exists {
			r.add("HOST_FILE_MISSING", StatusBlocked, severity, resource, "Host-provided file is unavailable.")
			return
		}
		if !o.SymlinkSafe || relative && !within(projectDir, o.ResolvedPath) {
			r.add("HOST_FILE_UNSAFE", StatusBlocked, severity, resource, "Host-provided file resolves outside its allowed path.")
			return
		}
		r.add("HOST_FILE_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "Host-provided file is available; contents are not included.")
		return
	}
	r.add("HOST_FILE_UNKNOWN", StatusUnknown, severity, resource, "Host-provided file was not observed by the target agent.")
}

func checkBackupPolicy(r *Report, b BackupRequirement, observed map[string]EvidenceState, usable bool) {
	state := EvidenceUnknown
	if usable {
		if s, ok := observed[b.PolicyID]; ok {
			state = validEvidence(s)
		}
	}
	resource := safeToken(b.Resource, 1024)
	if resource == "" {
		resource = "data"
	}
	if state == EvidenceAvailable {
		r.add("BACKUP_POLICY_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "The required backup policy is available.")
	} else if state == EvidenceUnavailable {
		r.add("BACKUP_POLICY_MISSING", StatusBlocked, SeverityBlocker, resource, "The required backup policy is unavailable.")
	} else {
		r.add("BACKUP_POLICY_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "Backup policy availability was not verified.")
	}
}
func checkNetwork(r *Report, req NetworkRequirement, observed []ObservedNetwork, usable bool) {
	name := safeToken(req.Name, 255)
	if name == "" {
		r.add("NETWORK_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, "network", "A network requirement has an invalid name.")
		return
	}
	if !usable {
		r.add("NETWORK_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Network inventory was not verified.")
		return
	}
	for _, n := range observed {
		if n.Name == name {
			if req.Driver != "" {
				if n.Driver == "" {
					r.add("NETWORK_DRIVER_UNKNOWN", StatusUnknown, SeverityBlocker, name, "The existing network driver could not be verified.")
					return
				}
				if req.Driver != n.Driver {
					r.add("NETWORK_INCOMPATIBLE", StatusBlocked, SeverityBlocker, name, "An existing network has an incompatible driver.")
					return
				}
			}
			if req.Scope != "" {
				if n.Scope == "" {
					r.add("NETWORK_SCOPE_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Existing network scope could not be verified.")
					return
				}
				if req.Scope != n.Scope {
					r.add("NETWORK_INCOMPATIBLE", StatusBlocked, SeverityBlocker, name, "An existing network has an incompatible scope.")
					return
				}
			}
			if req.Internal != nil {
				if n.Internal == nil {
					r.add("NETWORK_OPTIONS_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Existing network options could not be verified.")
					return
				}
				if *req.Internal != *n.Internal {
					r.add("NETWORK_INCOMPATIBLE", StatusBlocked, SeverityBlocker, name, "An existing network has incompatible internal routing.")
					return
				}
			}
			if req.Attachable != nil {
				if n.Attachable == nil {
					r.add("NETWORK_OPTIONS_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Existing network options could not be verified.")
					return
				}
				if *req.Attachable != *n.Attachable {
					r.add("NETWORK_INCOMPATIBLE", StatusBlocked, SeverityBlocker, name, "An existing network has incompatible attachment options.")
					return
				}
			}
			r.add("NETWORK_PRESENT", StatusSatisfied, SeverityInfo, name, "The existing network is available and will be preserved.")
			return
		}
	}
	if req.External || !req.Managed {
		r.add("NETWORK_MISSING", StatusBlocked, SeverityBlocker, name, "A required external or shared network is missing; preflight will not create it.")
		return
	}
	r.add("NETWORK_CREATE_REQUIRED", StatusPlanned, SeverityBlocker, name, "A missing network is explicitly managed and requires a separately approved apply before deployment.")
}
func checkVolume(r *Report, req VolumeRequirement, observed []ObservedVolume, usable bool) {
	name := safeToken(req.Name, 255)
	if name == "" {
		r.add("VOLUME_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, "volume", "A volume requirement has an invalid name.")
		return
	}
	if !usable {
		r.add("VOLUME_UNKNOWN", StatusUnknown, SeverityBlocker, name, "Volume inventory was not verified.")
		return
	}
	for _, v := range observed {
		if v.Name == name {
			if req.Driver != "" {
				if v.Driver == "" {
					r.add("VOLUME_DRIVER_UNKNOWN", StatusUnknown, SeverityBlocker, name, "The existing volume driver could not be verified.")
					return
				}
				if req.Driver != v.Driver {
					r.add("VOLUME_INCOMPATIBLE", StatusBlocked, SeverityBlocker, name, "An existing volume has an incompatible driver.")
					return
				}
			}
			r.add("VOLUME_PRESENT", StatusSatisfied, SeverityInfo, name, "The existing volume is available and will be preserved.")
			return
		}
	}
	if req.External || !req.Managed {
		r.add("VOLUME_MISSING", StatusBlocked, SeverityBlocker, name, "A required external or shared volume is missing; preflight will not create it.")
		return
	}
	r.add("VOLUME_CREATE_REQUIRED", StatusPlanned, SeverityBlocker, name, "A missing volume is explicitly managed and requires a separately approved apply before deployment.")
}
func checkMount(r *Report, req BindMount, projectDir string, observations []ObservedPath, usable bool) {
	if req.Source == "" {
		r.add("BIND_PATH_INVALID", StatusBlocked, SeverityBlocker, "bind-mount", "A bind mount has an empty source path.")
		return
	}
	requested, relative, err := requestedPath(req.Source, projectDir)
	resource := safeToken(requested, 1024)
	if resource == "" {
		resource = "bind-mount"
	}
	if err != nil {
		r.add("BIND_PATH_INVALID", StatusBlocked, SeverityBlocker, resource, "Bind path could not be safely resolved relative to the Compose project directory.")
		return
	}
	if !usable {
		r.add("BIND_PATH_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "Bind path was not observed by the target agent.")
		return
	}
	var observed *ObservedPath
	for i := range observations {
		if cleanTargetPath(observations[i].Path) == requested {
			observed = &observations[i]
			break
		}
	}
	if observed == nil {
		r.add("BIND_PATH_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "Bind path was not observed by the target agent.")
		return
	}
	if !observed.Exists {
		r.add("BIND_PATH_MISSING", StatusBlocked, SeverityBlocker, resource, "Required bind path is unavailable.")
		return
	}
	if !observed.SymlinkSafe || relative && !within(projectDir, observed.ResolvedPath) {
		r.add("BIND_PATH_UNSAFE", StatusBlocked, SeverityBlocker, resource, "Bind path symlink resolution escapes the Compose project directory or could not be verified.")
		return
	}
	if req.Device || !req.Required {
		return
	}
	if !req.RequireMountedFilesystem {
		r.add("BIND_PATH_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "Required bind path exists on the target host.")
		return
	}
	if req.ExpectedMountPath == "" {
		r.add("MOUNT_REQUIREMENT_INCOMPLETE", StatusBlocked, SeverityBlocker, resource, "Mounted storage requirement does not name its expected mountpoint.")
		return
	}
	expected, _, err := requestedPath(req.ExpectedMountPath, projectDir)
	if err != nil || !isTargetAbs(expected) {
		r.add("MOUNT_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, resource, "Expected mountpoint is invalid.")
		return
	}
	if cleanTargetPath(observed.ExpectedMountPath) != expected || observed.ExpectedMountResolved == "" || cleanTargetPath(observed.ExpectedMountResolved) != expected || cleanTargetPath(observed.MountPoint) != expected || expected == "/" {
		r.add("MOUNT_NOT_MOUNTED", StatusBlocked, SeverityBlocker, resource, "The declared storage mountpoint is not an active filesystem boundary for this path.")
		return
	}
	r.add("MOUNT_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "Required storage path exists on its declared mounted filesystem.")
}
func requestedPath(source, projectDir string) (string, bool, error) {
	base := cleanTargetPath(projectDir)
	if !isTargetAbs(base) {
		return "", false, fmt.Errorf("project directory must be absolute")
	}
	relative := !isTargetAbs(source)
	p := source
	if relative {
		p = joinTargetPath(base, source)
	}
	p = cleanTargetPath(p)
	if !isTargetAbs(p) {
		return "", relative, fmt.Errorf("path must be absolute after resolution")
	}
	if relative && !within(base, p) {
		return p, relative, fmt.Errorf("relative path escapes project directory")
	}
	return p, relative, nil
}

var windowsDrivePathRE = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func isWindowsTargetPath(p string) bool {
	return windowsDrivePathRE.MatchString(p) || strings.HasPrefix(p, `\\\\`)
}
func isTargetAbs(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || isWindowsTargetPath(p)
}
func cleanTargetPath(p string) string {
	if isWindowsTargetPath(p) {
		p = strings.ReplaceAll(p, `\`, `/`)
		return path.Clean(p)
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return filepath.Clean(p)
}
func joinTargetPath(base, child string) string {
	if isWindowsTargetPath(base) || strings.HasPrefix(base, "/") {
		return path.Join(cleanTargetPath(base), strings.ReplaceAll(child, `\`, `/`))
	}
	return filepath.Join(base, child)
}
func relativeTargetPath(base, target string) (string, error) {
	windows := isWindowsTargetPath(base) || isWindowsTargetPath(target)
	b, t := cleanTargetPath(base), cleanTargetPath(target)
	if windows {
		b, t = strings.ToLower(b), strings.ToLower(t)
	}
	if windows && strings.SplitN(b, "/", 2)[0] != strings.SplitN(t, "/", 2)[0] {
		return "", fmt.Errorf("different path volumes")
	}
	bp, tp := splitTargetPath(b), splitTargetPath(t)
	common := 0
	for common < len(bp) && common < len(tp) && bp[common] == tp[common] {
		common++
	}
	parts := append([]string{}, make([]string, len(bp)-common)...)
	for i := range parts {
		parts[i] = ".."
	}
	parts = append(parts, tp[common:]...)
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, "/"), nil
}
func splitTargetPath(p string) []string {
	p = strings.Trim(cleanTargetPath(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func within(base, path string) bool {
	rel, err := relativeTargetPath(cleanTargetPath(base), cleanTargetPath(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// ObserveBindPath must run on the target host. It returns only path state,
// never raw operating-system errors or mountinfo text.
func ObserveBindPath(source, projectDir, expectedMountPath, mountInfo string) ObservedPath {
	requested, relative, err := requestedPath(source, projectDir)
	o := ObservedPath{Path: requested}
	if err != nil {
		return o
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return o
	}
	o.ResolvedPath = resolved
	o.Exists = true
	o.SymlinkSafe = !relative || within(projectDir, resolved)
	if expectedMountPath != "" {
		expected, _, e := requestedPath(expectedMountPath, projectDir)
		if e == nil {
			if rp, e := filepath.EvalSymlinks(expected); e == nil && filepath.Clean(rp) == filepath.Clean(expected) {
				o.ExpectedMountPath = expected
				o.ExpectedMountResolved = rp
			}
		}
	}
	o.MountPoint, o.Filesystem = mountForPath(resolved, mountInfo)
	return o
}
func mountForPath(path, mountInfo string) (string, string) {
	best, fs := "", ""
	for _, line := range strings.Split(mountInfo, "\n") {
		fields := strings.Fields(line)
		sep := -1
		for i, s := range fields {
			if s == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(fields) <= sep+2 {
			continue
		}
		mp := filepath.Clean(unescapeMountField(fields[4]))
		if !filepath.IsAbs(mp) || !within(mp, path) {
			continue
		}
		if len(mp) > len(best) {
			best = mp
			fs = fields[sep+1]
		}
	}
	return best, fs
}
func unescapeMountField(s string) string {
	r := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return r.Replace(s)
}
func checkPort(r *Report, req PortRequirement, project string, ports []ObservedPort, usable bool) {
	protocol := strings.ToLower(req.Protocol)
	if protocol == "" {
		protocol = "tcp"
	}
	if protocol != "tcp" && protocol != "udp" {
		r.add("PORT_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, "port", "Port protocol is unsupported.")
		return
	}
	if req.HostPort == 0 {
		return
	}
	resource := fmt.Sprintf("%s/%d", protocol, req.HostPort)
	if req.ServiceName == "" {
		r.add("PORT_SERVICE_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "Compose port requirement has no service identity.")
		return
	}
	if _, ok := parseHostIP(req.HostIP); !ok {
		r.add("PORT_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, resource, "Host IP requirement is invalid.")
		return
	}
	if !usable {
		r.add("PORT_STATUS_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "Host port availability was not verified.")
		return
	}
	for _, p := range ports {
		pp := strings.ToLower(p.Protocol)
		if pp == "" {
			pp = "tcp"
		}
		if p.HostPort != req.HostPort || pp != protocol {
			continue
		}
		overlap, valid := hostIPsOverlap(req.HostIP, p.HostIP)
		if !valid {
			r.add("PORT_OWNER_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "The occupied host address could not be safely compared.")
			return
		}
		if !overlap {
			continue
		}
		if p.OwnerKnown && p.ProjectName == project && p.ServiceName == req.ServiceName && p.Target != "" && p.Target == req.Target && req.Target != "" {
			continue
		}
		if !p.OwnerKnown || p.ProjectName == project && (p.ServiceName == "" || p.Target == "" || p.ServiceName == req.ServiceName && p.Target != req.Target) {
			r.add("PORT_OWNER_UNKNOWN", StatusUnknown, SeverityBlocker, resource, "The requested host port is occupied but its service binding could not be matched.")
			return
		}
		r.add("PORT_CONFLICT", StatusBlocked, SeverityBlocker, resource, "The requested host port is occupied by another service or project.")
		return
	}
	r.add("PORT_AVAILABLE", StatusSatisfied, SeverityInfo, resource, "The requested host port is available or already belongs to this service binding.")
}
func checkModelPortConflicts(r *Report, ports []PortRequirement) {
	for i := 0; i < len(ports); i++ {
		a := ports[i]
		if a.HostPort == 0 {
			continue
		}
		ap := strings.ToLower(a.Protocol)
		if ap == "" {
			ap = "tcp"
		}
		if ap != "tcp" && ap != "udp" {
			continue
		}
		if _, ok := parseHostIP(a.HostIP); !ok {
			continue
		}
		for j := i + 1; j < len(ports); j++ {
			b := ports[j]
			bp := strings.ToLower(b.Protocol)
			if bp == "" {
				bp = "tcp"
			}
			if a.HostPort != b.HostPort || ap != bp {
				continue
			}
			overlap, valid := hostIPsOverlap(a.HostIP, b.HostIP)
			if !valid || !overlap {
				continue
			}
			if a.ServiceName != "" && a.ServiceName == b.ServiceName && a.Target != "" && a.Target == b.Target {
				continue
			}
			resource := fmt.Sprintf("%s/%d", ap, a.HostPort)
			r.add("PORT_CONFIGURATION_CONFLICT", StatusBlocked, SeverityBlocker, resource, "Multiple Compose services require the same host port and address.")
		}
	}
}
func parseHostIP(s string) (netip.Addr, bool) {
	if strings.TrimSpace(s) == "" {
		return netip.Addr{}, true
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
func hostIPsOverlap(a, b string) (bool, bool) {
	aa, ok := parseHostIP(a)
	if !ok {
		return false, false
	}
	bb, ok := parseHostIP(b)
	if !ok {
		return false, false
	}
	wild := func(s string, ip netip.Addr) bool {
		return strings.TrimSpace(s) == "" || ip.IsValid() && ip.IsUnspecified()
	}
	if wild(a, aa) || wild(b, bb) {
		return true, true
	}
	return aa == bb, true
}
func checkDevice(r *Report, req DeviceRequirement, devices map[string]EvidenceState, usable bool) {
	path := safeToken(req.HostPath, 1024)
	if path == "" {
		r.add("DEVICE_REQUIREMENT_INVALID", StatusBlocked, SeverityBlocker, "device", "A device requirement has an invalid host path.")
		return
	}
	if !req.Required {
		return
	}
	state := EvidenceUnknown
	if usable {
		if v, ok := devices[path]; ok {
			state = v
		}
	}
	if state == EvidenceAvailable {
		r.add("DEVICE_AVAILABLE", StatusSatisfied, SeverityInfo, path, "Required host device is available.")
	} else if state == EvidenceUnavailable {
		r.add("DEVICE_UNAVAILABLE", StatusBlocked, SeverityBlocker, path, "Required host device is unavailable.")
	} else {
		r.add("DEVICE_UNKNOWN", StatusUnknown, SeverityBlocker, path, "Required host device availability was not verified.")
	}
}
