package protocol

import "regexp"

// Docker resource methods: images, networks, and volumes. Listing and inspecting
// need no tier; pulling, removing, and pruning change the host and need manage.
const (
	MethodDockerImages          = "docker.images"
	MethodDockerNetworks        = "docker.networks"
	MethodDockerVolumes         = "docker.volumes"
	MethodDockerResourceInspect = "docker.resource.inspect" // call
	MethodDockerPull            = "docker.image.pull"       // stream of DockerPullEvent
	MethodDockerResourceRemove  = "docker.resource.remove"  // call
	MethodDockerPrune           = "docker.prune"            // call
)

func init() {
	for _, method := range []string{MethodDockerImages, MethodDockerNetworks, MethodDockerVolumes, MethodDockerResourceInspect} {
		channelMethodTiers[method] = ""
	}
	for _, method := range []string{MethodDockerPull, MethodDockerResourceRemove, MethodDockerPrune} {
		channelMethodTiers[method] = DockerModeManage
	}
}

// ErrCodePullFailed reports an image pull that did not complete; the message is the
// registry's own explanation, such as access being denied.
const ErrCodePullFailed = "pull_failed"

// Resource kinds.
const (
	KindImage   = "image"
	KindNetwork = "network"
	KindVolume  = "volume"
)

// ValidResourceKind reports whether kind names a resource type.
func ValidResourceKind(kind string) bool {
	return kind == KindImage || kind == KindNetwork || kind == KindVolume
}

var (
	imageRef   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,254}$`)
	networkRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	volumeRef  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
)

// ValidResourceRef reports whether ref is an acceptable identifier for kind. These
// rules are checked on the server and again on the agent before anything is sent to
// Docker. An image reference may be an ID or a repository and tag; a path with
// empty or dot segments is refused.
func ValidResourceRef(kind, ref string) bool {
	switch kind {
	case KindImage:
		if !imageRef.MatchString(ref) {
			return false
		}
		for i := 0; i < len(ref); i++ {
			if ref[i] == '/' && (i+1 >= len(ref) || ref[i+1] == '/' || ref[i+1] == '.') {
				return false
			}
		}
		return true
	case KindNetwork:
		return networkRef.MatchString(ref)
	case KindVolume:
		return volumeRef.MatchString(ref)
	}
	return false
}

// DockerUser names a container that uses a resource.
type DockerUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DockerImage is one image in the image list.
type DockerImage struct {
	ID       string       `json:"id"`
	Tags     []string     `json:"tags,omitempty"`
	Created  int64        `json:"created"`
	Size     int64        `json:"size"`
	Dangling bool         `json:"dangling"`
	UsedBy   []DockerUser `json:"usedBy,omitempty"`
}

// DockerNetwork is one network in the network list.
type DockerNetwork struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Driver     string       `json:"driver"`
	Scope      string       `json:"scope"`
	Internal   bool         `json:"internal"`
	Attachable bool         `json:"attachable"`
	IPv6       bool         `json:"ipv6"`
	Subnets    []string     `json:"subnets,omitempty"`
	Gateways   []string     `json:"gateways,omitempty"`
	Builtin    bool         `json:"builtin"`
	Created    string       `json:"created,omitempty"`
	UsedBy     []DockerUser `json:"usedBy,omitempty"`
}

// DockerVolume is one volume in the volume list. Size is -1 when Docker did not report it.
type DockerVolume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope,omitempty"`
	Mountpoint string            `json:"mountpoint,omitempty"`
	Created    string            `json:"created,omitempty"`
	Size       int64             `json:"size"`
	Anonymous  bool              `json:"anonymous"`
	Labels     map[string]string `json:"labels,omitempty"`
	UsedBy     []DockerUser      `json:"usedBy,omitempty"`
}

// DockerImagesResult, DockerNetworksResult, and DockerVolumesResult are the list replies.
type DockerImagesResult struct {
	Images []DockerImage `json:"images"`
}
type DockerNetworksResult struct {
	Networks []DockerNetwork `json:"networks"`
}
type DockerVolumesResult struct {
	Volumes []DockerVolume `json:"volumes"`
}

// DockerResourceParams selects one resource, by ID or name.
type DockerResourceParams struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Force applies to remove only: for an image it removes it even when it has several
	// tags, for a volume it ignores a missing volume. It never removes a resource in use.
	Force bool `json:"force,omitempty"`
}

// DockerImageDetail is a curated view of one image. Environment values are not
// included, only their names, because images sometimes carry defaults that are secrets.
type DockerImageDetail struct {
	DockerImage
	Digests      []string          `json:"digests,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	OS           string            `json:"os,omitempty"`
	Author       string            `json:"author,omitempty"`
	Layers       int               `json:"layers"`
	User         string            `json:"user,omitempty"`
	WorkingDir   string            `json:"workingDir,omitempty"`
	Entrypoint   []string          `json:"entrypoint,omitempty"`
	Command      []string          `json:"command,omitempty"`
	ExposedPorts []string          `json:"exposedPorts,omitempty"`
	EnvNames     []string          `json:"envNames,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// DockerNetworkMember is a container attached to a network.
type DockerNetworkMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
	MAC  string `json:"mac,omitempty"`
}

// DockerNetworkDetail is a curated view of one network.
type DockerNetworkDetail struct {
	DockerNetwork
	Members []DockerNetworkMember `json:"members,omitempty"`
	Options map[string]string     `json:"options,omitempty"`
	Labels  map[string]string     `json:"labels,omitempty"`
}

// DockerVolumeDetail is a curated view of one volume.
type DockerVolumeDetail struct {
	DockerVolume
	Options map[string]string `json:"options,omitempty"`
}

// DockerResourceInspectResult carries the detail for the requested kind.
type DockerResourceInspectResult struct {
	Kind    string               `json:"kind"`
	Image   *DockerImageDetail   `json:"image,omitempty"`
	Network *DockerNetworkDetail `json:"network,omitempty"`
	Volume  *DockerVolumeDetail  `json:"volume,omitempty"`
}

// DockerPullParams starts an image pull.
type DockerPullParams struct {
	Ref string `json:"ref"`
}

// DockerPullEvent is one record of a pull's progress stream; it has the shape of
// the recreate events, using only the pull steps.
type DockerPullEvent = DockerRecreateEvent

// DockerRemoveResult describes what a removal did. For an image it lists the tags
// removed and the layers' images deleted.
type DockerRemoveResult struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Untagged []string `json:"untagged,omitempty"`
	Deleted  []string `json:"deleted,omitempty"`
}

// DockerPruneParams selects what to prune. With DryRun nothing changes and the
// reply lists what a prune would remove. All widens the prune: for images it also
// removes unused tagged images (by default only dangling ones), and for volumes it
// also removes unused named volumes (by default only anonymous ones).
type DockerPruneParams struct {
	Kind   string `json:"kind"`
	All    bool   `json:"all,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// DockerPruneItem is something a prune would remove, or did.
type DockerPruneItem struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// DockerPruneResult is the reply to a prune. Candidates is filled for a dry run, and
// Removed and SpaceReclaimed for a real one.
type DockerPruneResult struct {
	Kind           string            `json:"kind"`
	DryRun         bool              `json:"dryRun"`
	Candidates     []DockerPruneItem `json:"candidates,omitempty"`
	Removed        []string          `json:"removed,omitempty"`
	SpaceReclaimed int64             `json:"spaceReclaimed,omitempty"`
}
