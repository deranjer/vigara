package imageupdates

import (
	"regexp"
	"strings"
)

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// MonitoringReference keeps deployment pins separate from the tag being
// observed. A digest-only reference has no implied channel: never guess latest.
func MonitoringReference(ref string) string {
	name, digest, pinned := strings.Cut(ref, "@")
	if !referencePattern.MatchString(name) || strings.HasPrefix(name, "sha256:") {
		return ""
	}
	if !pinned {
		return ref
	}
	if !digestPattern.MatchString(digest) || strings.LastIndex(name, ":") <= strings.LastIndex(name, "/") {
		return ""
	}
	if !tagPattern.MatchString(name[strings.LastIndex(name, ":")+1:]) {
		return ""
	}
	return name
}

// WithMonitoringTag observes a tag of the deployed repository without changing
// the deployed reference. Registry ports are not mistaken for image tags.
func WithMonitoringTag(ref, tag string) string {
	if !tagPattern.MatchString(tag) {
		return ""
	}
	name, _, _ := strings.Cut(ref, "@")
	if !referencePattern.MatchString(name) || strings.HasPrefix(name, "sha256:") {
		return ""
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	if name == "" {
		return ""
	}
	return name + ":" + tag
}
