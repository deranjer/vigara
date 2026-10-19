package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

const (
	dfTimeout    = 30 * time.Second
	pruneTimeout = 5 * time.Minute
)

var (
	anonymousVolume = regexp.MustCompile(`^[a-f0-9]{64}$`)
	builtinNetworks = map[string]bool{"bridge": true, "host": true, "none": true}
)

// containerUse records which images, volumes, and networks one container refers to.
type containerUse struct {
	user     protocol.DockerUser
	imageID  string
	volumes  []string
	networks []string
}

// uses reads every container's references, which is how "used by" is worked out for
// all three resource types. Stopped containers count: Docker will not remove a
// resource a stopped container still refers to.
func (c *Client) uses(ctx context.Context) ([]containerUse, error) {
	var raw []struct {
		ID      string   `json:"Id"`
		Names   []string `json:"Names"`
		ImageID string   `json:"ImageID"`
		Mounts  []struct {
			Type string `json:"Type"`
			Name string `json:"Name"`
		} `json:"Mounts"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, maxListBytes, &raw); err != nil {
		return nil, err
	}
	out := make([]containerUse, 0, len(raw))
	for _, item := range raw {
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		use := containerUse{user: protocol.DockerUser{ID: item.ID, Name: name}, imageID: item.ImageID}
		for _, mount := range item.Mounts {
			if mount.Type == "volume" && mount.Name != "" {
				use.volumes = append(use.volumes, mount.Name)
			}
		}
		for network := range item.NetworkSettings.Networks {
			use.networks = append(use.networks, network)
		}
		out = append(out, use)
	}
	return out, nil
}

func usersOf(uses []containerUse, matches func(containerUse) bool) []protocol.DockerUser {
	var users []protocol.DockerUser
	for _, use := range uses {
		if matches(use) {
			users = append(users, use.user)
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func isDangling(tags []string) bool {
	for _, tag := range tags {
		if tag != "" && tag != "<none>:<none>" {
			return false
		}
	}
	return true
}

// Images lists every image with the containers that use it.
func (c *Client) Images(ctx context.Context) (protocol.DockerImagesResult, error) {
	var raw []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Created  int64    `json:"Created"`
		Size     int64    `json:"Size"`
	}
	if err := c.getJSON(ctx, "/images/json", nil, maxListBytes, &raw); err != nil {
		return protocol.DockerImagesResult{}, err
	}
	uses, err := c.uses(ctx)
	if err != nil {
		return protocol.DockerImagesResult{}, err
	}
	images := make([]protocol.DockerImage, 0, len(raw))
	for _, item := range raw {
		id := item.ID
		tags := make([]string, 0, len(item.RepoTags))
		for _, tag := range item.RepoTags {
			if tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		sort.Strings(tags)
		images = append(images, protocol.DockerImage{ID: id, Tags: tags, Created: item.Created, Size: item.Size, Dangling: isDangling(item.RepoTags),
			UsedBy: usersOf(uses, func(u containerUse) bool { return u.imageID == id })})
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Created > images[j].Created })
	return protocol.DockerImagesResult{Images: images}, nil
}

// Networks lists every network with the containers attached to it.
func (c *Client) Networks(ctx context.Context) (protocol.DockerNetworksResult, error) {
	var raw []struct {
		ID         string `json:"Id"`
		Name       string `json:"Name"`
		Driver     string `json:"Driver"`
		Scope      string `json:"Scope"`
		Internal   bool   `json:"Internal"`
		Attachable bool   `json:"Attachable"`
		IPv6       bool   `json:"EnableIPv6"`
		Created    string `json:"Created"`
		IPAM       struct {
			Config []struct {
				Subnet  string `json:"Subnet"`
				Gateway string `json:"Gateway"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := c.getJSON(ctx, "/networks", nil, maxListBytes, &raw); err != nil {
		return protocol.DockerNetworksResult{}, err
	}
	uses, err := c.uses(ctx)
	if err != nil {
		return protocol.DockerNetworksResult{}, err
	}
	networks := make([]protocol.DockerNetwork, 0, len(raw))
	for _, item := range raw {
		name := item.Name
		network := protocol.DockerNetwork{ID: item.ID, Name: name, Driver: item.Driver, Scope: item.Scope, Internal: item.Internal, Attachable: item.Attachable,
			IPv6: item.IPv6, Builtin: builtinNetworks[name], Created: zeroTime(item.Created),
			UsedBy: usersOf(uses, func(u containerUse) bool { return contains(u.networks, name) })}
		for _, config := range item.IPAM.Config {
			if config.Subnet != "" {
				network.Subnets = append(network.Subnets, config.Subnet)
			}
			if config.Gateway != "" {
				network.Gateways = append(network.Gateways, config.Gateway)
			}
		}
		networks = append(networks, network)
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	return protocol.DockerNetworksResult{Networks: networks}, nil
}

// volumeSizes returns sizes by volume name from Docker's disk-usage report, which
// can be slow on a large host, so a failure just leaves sizes unknown.
func (c *Client) volumeSizes(ctx context.Context) map[string]int64 {
	ctx, cancel := context.WithTimeout(ctx, dfTimeout)
	defer cancel()
	var df struct {
		Volumes []struct {
			Name      string `json:"Name"`
			UsageData *struct {
				Size int64 `json:"Size"`
			} `json:"UsageData"`
		} `json:"Volumes"`
	}
	if err := c.getJSON(ctx, "/system/df", url.Values{"type": {"volume"}}, maxListBytes, &df); err != nil {
		return nil
	}
	sizes := map[string]int64{}
	for _, volume := range df.Volumes {
		if volume.UsageData != nil && volume.UsageData.Size >= 0 {
			sizes[volume.Name] = volume.UsageData.Size
		}
	}
	return sizes
}

type volumeJSON struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	Options    map[string]string `json:"Options"`
}

func buildVolume(v volumeJSON, uses []containerUse, sizes map[string]int64) protocol.DockerVolume {
	name := v.Name
	size := int64(-1)
	if known, ok := sizes[name]; ok {
		size = known
	}
	return protocol.DockerVolume{Name: name, Driver: v.Driver, Scope: v.Scope, Mountpoint: v.Mountpoint, Created: zeroTime(v.CreatedAt), Size: size,
		Anonymous: anonymousVolume.MatchString(name), Labels: v.Labels, UsedBy: usersOf(uses, func(u containerUse) bool { return contains(u.volumes, name) })}
}

// Volumes lists every volume with its size, when known, and the containers using it.
func (c *Client) Volumes(ctx context.Context) (protocol.DockerVolumesResult, error) {
	var raw struct {
		Volumes []volumeJSON `json:"Volumes"`
	}
	if err := c.getJSON(ctx, "/volumes", nil, maxListBytes, &raw); err != nil {
		return protocol.DockerVolumesResult{}, err
	}
	uses, err := c.uses(ctx)
	if err != nil {
		return protocol.DockerVolumesResult{}, err
	}
	sizes := c.volumeSizes(ctx)
	volumes := make([]protocol.DockerVolume, 0, len(raw.Volumes))
	for _, v := range raw.Volumes {
		volumes = append(volumes, buildVolume(v, uses, sizes))
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
	return protocol.DockerVolumesResult{Volumes: volumes}, nil
}

// escapeImageRef escapes each path segment of an image reference but keeps its
// slashes, because Docker's routes match a repository path, not one escaped segment.
func escapeImageRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func resourcePath(kind, ref string) string {
	switch kind {
	case protocol.KindImage:
		return "/images/" + escapeImageRef(ref)
	case protocol.KindNetwork:
		return "/networks/" + url.PathEscape(ref)
	}
	return "/volumes/" + url.PathEscape(ref)
}

func checkResource(kind, ref string) error {
	if !protocol.ValidResourceKind(kind) || !protocol.ValidResourceRef(kind, ref) {
		return ErrBadRequest
	}
	return nil
}

// InspectResource returns a curated view of one image, network, or volume.
func (c *Client) InspectResource(ctx context.Context, kind, ref string) (protocol.DockerResourceInspectResult, error) {
	if err := checkResource(kind, ref); err != nil {
		return protocol.DockerResourceInspectResult{}, err
	}
	uses, err := c.uses(ctx)
	if err != nil {
		return protocol.DockerResourceInspectResult{}, err
	}
	result := protocol.DockerResourceInspectResult{Kind: kind}
	switch kind {
	case protocol.KindImage:
		var raw struct {
			ID           string   `json:"Id"`
			RepoTags     []string `json:"RepoTags"`
			RepoDigests  []string `json:"RepoDigests"`
			Created      string   `json:"Created"`
			Size         int64    `json:"Size"`
			Architecture string   `json:"Architecture"`
			OS           string   `json:"Os"`
			Author       string   `json:"Author"`
			RootFS       struct {
				Layers []string `json:"Layers"`
			} `json:"RootFS"`
			Config struct {
				User         string              `json:"User"`
				WorkingDir   string              `json:"WorkingDir"`
				Entrypoint   []string            `json:"Entrypoint"`
				Cmd          []string            `json:"Cmd"`
				ExposedPorts map[string]struct{} `json:"ExposedPorts"`
				Env          []string            `json:"Env"`
				Labels       map[string]string   `json:"Labels"`
			} `json:"Config"`
		}
		if err := c.getJSON(ctx, resourcePath(kind, ref)+"/json", nil, maxInspectBytes, &raw); err != nil {
			return result, err
		}
		created := int64(0)
		if parsed, err := time.Parse(time.RFC3339Nano, raw.Created); err == nil {
			created = parsed.Unix()
		}
		detail := &protocol.DockerImageDetail{
			DockerImage: protocol.DockerImage{ID: raw.ID, Tags: raw.RepoTags, Created: created, Size: raw.Size, Dangling: isDangling(raw.RepoTags),
				UsedBy: usersOf(uses, func(u containerUse) bool { return u.imageID == raw.ID })},
			Digests: raw.RepoDigests, Architecture: raw.Architecture, OS: raw.OS, Author: raw.Author, Layers: len(raw.RootFS.Layers),
			User: raw.Config.User, WorkingDir: raw.Config.WorkingDir, Entrypoint: raw.Config.Entrypoint, Command: raw.Config.Cmd, Labels: raw.Config.Labels,
		}
		for port := range raw.Config.ExposedPorts {
			detail.ExposedPorts = append(detail.ExposedPorts, port)
		}
		sort.Strings(detail.ExposedPorts)
		for _, entry := range raw.Config.Env {
			name, _, _ := strings.Cut(entry, "=")
			detail.EnvNames = append(detail.EnvNames, name)
		}
		sort.Strings(detail.EnvNames)
		result.Image = detail
	case protocol.KindNetwork:
		var raw struct {
			ID         string `json:"Id"`
			Name       string `json:"Name"`
			Driver     string `json:"Driver"`
			Scope      string `json:"Scope"`
			Internal   bool   `json:"Internal"`
			Attachable bool   `json:"Attachable"`
			IPv6       bool   `json:"EnableIPv6"`
			Created    string `json:"Created"`
			IPAM       struct {
				Config []struct {
					Subnet  string `json:"Subnet"`
					Gateway string `json:"Gateway"`
				} `json:"Config"`
			} `json:"IPAM"`
			Containers map[string]struct {
				Name        string `json:"Name"`
				IPv4Address string `json:"IPv4Address"`
				IPv6Address string `json:"IPv6Address"`
				MacAddress  string `json:"MacAddress"`
			} `json:"Containers"`
			Options map[string]string `json:"Options"`
			Labels  map[string]string `json:"Labels"`
		}
		if err := c.getJSON(ctx, resourcePath(kind, ref), nil, maxInspectBytes, &raw); err != nil {
			return result, err
		}
		detail := &protocol.DockerNetworkDetail{
			DockerNetwork: protocol.DockerNetwork{ID: raw.ID, Name: raw.Name, Driver: raw.Driver, Scope: raw.Scope, Internal: raw.Internal, Attachable: raw.Attachable,
				IPv6: raw.IPv6, Builtin: builtinNetworks[raw.Name], Created: zeroTime(raw.Created),
				UsedBy: usersOf(uses, func(u containerUse) bool { return contains(u.networks, raw.Name) })},
			Options: raw.Options, Labels: raw.Labels,
		}
		for _, config := range raw.IPAM.Config {
			if config.Subnet != "" {
				detail.Subnets = append(detail.Subnets, config.Subnet)
			}
			if config.Gateway != "" {
				detail.Gateways = append(detail.Gateways, config.Gateway)
			}
		}
		for id, member := range raw.Containers {
			detail.Members = append(detail.Members, protocol.DockerNetworkMember{ID: id, Name: member.Name, IPv4: member.IPv4Address, IPv6: member.IPv6Address, MAC: member.MacAddress})
		}
		sort.Slice(detail.Members, func(i, j int) bool { return detail.Members[i].Name < detail.Members[j].Name })
		result.Network = detail
	default:
		var raw volumeJSON
		if err := c.getJSON(ctx, resourcePath(kind, ref), nil, maxInspectBytes, &raw); err != nil {
			return result, err
		}
		result.Volume = &protocol.DockerVolumeDetail{DockerVolume: buildVolume(raw, uses, c.volumeSizes(ctx)), Options: raw.Options}
	}
	return result, nil
}

// PullImage pulls an image reference, streaming progress. Only an image reference
// is accepted, never a bare ID, since an ID cannot be pulled.
func (c *Client) PullImage(ctx context.Context, ref string, emit func(protocol.DockerPullEvent)) error {
	if !protocol.ValidResourceRef(protocol.KindImage, ref) || strings.HasPrefix(ref, "sha256:") {
		return ErrBadRequest
	}
	if emit == nil {
		emit = func(protocol.DockerPullEvent) {}
	}
	if err := c.pull(ctx, ref, emit); err != nil {
		return err
	}
	emit(protocol.DockerPullEvent{Step: protocol.StepDone, Message: "Pulled " + ref + "."})
	return nil
}

// RemoveResource removes one image, network, or volume. Docker refuses to remove
// anything in use, and its explanation is passed back as an *APIError.
func (c *Client) RemoveResource(ctx context.Context, p protocol.DockerResourceParams) (protocol.DockerRemoveResult, error) {
	if err := checkResource(p.Kind, p.ID); err != nil {
		return protocol.DockerRemoveResult{}, err
	}
	query := url.Values{}
	if p.Force && p.Kind != protocol.KindNetwork {
		query.Set("force", "1")
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodDelete, resourcePath(p.Kind, p.ID), query, nil)
	if err != nil {
		return protocol.DockerRemoveResult{}, err
	}
	defer resp.Body.Close()
	result := protocol.DockerRemoveResult{Kind: p.Kind, ID: p.ID}
	if p.Kind == protocol.KindImage {
		var items []struct {
			Untagged string `json:"Untagged"`
			Deleted  string `json:"Deleted"`
		}
		if json.NewDecoder(resp.Body).Decode(&items) == nil {
			for _, item := range items {
				if item.Untagged != "" {
					result.Untagged = append(result.Untagged, item.Untagged)
				}
				if item.Deleted != "" {
					result.Deleted = append(result.Deleted, item.Deleted)
				}
			}
		}
	}
	return result, nil
}

// Prune removes unused resources of one kind. With DryRun it changes nothing and
// reports what would go, computed the same way Docker decides: an image is unused
// when no container refers to it, and by default only untagged ("dangling") ones
// are removed; a network is unused when nothing is attached, except the built-in
// ones; a volume is unused when no container mounts it, and by default only
// anonymous volumes are removed.
func (c *Client) Prune(ctx context.Context, p protocol.DockerPruneParams) (protocol.DockerPruneResult, error) {
	if !protocol.ValidResourceKind(p.Kind) {
		return protocol.DockerPruneResult{}, ErrBadRequest
	}
	result := protocol.DockerPruneResult{Kind: p.Kind, DryRun: p.DryRun}
	if p.DryRun {
		switch p.Kind {
		case protocol.KindImage:
			images, err := c.Images(ctx)
			if err != nil {
				return result, err
			}
			for _, image := range images.Images {
				if len(image.UsedBy) == 0 && (p.All || image.Dangling) {
					name := strings.Join(image.Tags, ", ")
					result.Candidates = append(result.Candidates, protocol.DockerPruneItem{ID: image.ID, Name: name, Size: image.Size})
				}
			}
		case protocol.KindNetwork:
			networks, err := c.Networks(ctx)
			if err != nil {
				return result, err
			}
			for _, network := range networks.Networks {
				if !network.Builtin && len(network.UsedBy) == 0 {
					result.Candidates = append(result.Candidates, protocol.DockerPruneItem{ID: network.ID, Name: network.Name})
				}
			}
		default:
			volumes, err := c.Volumes(ctx)
			if err != nil {
				return result, err
			}
			for _, volume := range volumes.Volumes {
				if len(volume.UsedBy) == 0 && (p.All || volume.Anonymous) {
					size := volume.Size
					if size < 0 {
						size = 0
					}
					result.Candidates = append(result.Candidates, protocol.DockerPruneItem{ID: volume.Name, Name: volume.Name, Size: size})
				}
			}
		}
		return result, nil
	}

	ctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	query := url.Values{}
	var path string
	switch p.Kind {
	case protocol.KindImage:
		path = "/images/prune"
		dangling := "true"
		if p.All {
			dangling = "false"
		}
		query.Set("filters", `{"dangling":["`+dangling+`"]}`)
	case protocol.KindNetwork:
		path = "/networks/prune"
	default:
		path = "/volumes/prune"
		if p.All {
			query.Set("filters", `{"all":["true"]}`)
		}
	}
	resp, err := c.do(ctx, http.MethodPost, path, query, nil)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	var reply struct {
		ImagesDeleted   []struct{ Untagged, Deleted string }
		NetworksDeleted []string
		VolumesDeleted  []string
		SpaceReclaimed  int64
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil && !errors.Is(err, io.EOF) {
		return result, err
	}
	for _, item := range reply.ImagesDeleted {
		if item.Untagged != "" {
			result.Removed = append(result.Removed, item.Untagged)
		} else if item.Deleted != "" {
			result.Removed = append(result.Removed, item.Deleted)
		}
	}
	result.Removed = append(append(result.Removed, reply.NetworksDeleted...), reply.VolumesDeleted...)
	result.SpaceReclaimed = reply.SpaceReclaimed
	return result, nil
}
