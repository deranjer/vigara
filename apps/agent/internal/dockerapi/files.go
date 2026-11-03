package dockerapi

import (
	"context"
	"net/url"
	"path/filepath"

	"github.com/deranjer/vigara/internal/protocol"
)

// StorageSource resolves only Docker-inspected storage on the local host.
// Plugin/network volumes need a driver-aware helper and are deliberately refused.
func (c *Client) StorageSource(ctx context.Context, p protocol.DockerFileParams) (string, error) {
	if p.Kind == "volume" && p.Mount == "" {
		return c.localVolumeSource(ctx, p.ID)
	}
	if p.Kind != "mount" || checkRef(p.ID) != nil || p.Mount == "" {
		return "", ErrBadRequest
	}
	var raw struct {
		Mounts []struct{ Type, Name, Source, Destination string }
	}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(p.ID)+"/json", nil, maxInspectBytes, &raw); err != nil {
		return "", err
	}
	for _, mount := range raw.Mounts {
		if mount.Destination != p.Mount {
			continue
		}
		if mount.Type == "bind" && filepath.IsAbs(mount.Source) {
			return mount.Source, nil
		}
		if mount.Type == "volume" {
			source, err := c.localVolumeSource(ctx, mount.Name)
			if err != nil {
				return "", err
			}
			if source == mount.Source {
				return source, nil
			}
		}
		return "", &protocol.ChannelError{Code: protocol.ErrCodeFileAccess, Message: "This mount type or volume subpath does not support file browsing."}
	}
	return "", &protocol.ChannelError{Code: protocol.ErrCodeFileAccess, Message: "That mount no longer exists. Refresh the container."}
}

func (c *Client) localVolumeSource(ctx context.Context, name string) (string, error) {
	if !protocol.ValidResourceRef(protocol.KindVolume, name) {
		return "", ErrBadRequest
	}
	var volume volumeJSON
	if err := c.getJSON(ctx, "/volumes/"+url.PathEscape(name), nil, maxInspectBytes, &volume); err != nil {
		return "", err
	}
	if volume.Driver != "local" || len(volume.Options) != 0 || !filepath.IsAbs(volume.Mountpoint) {
		return "", &protocol.ChannelError{Code: protocol.ErrCodeFileAccess, Message: "File browsing supports ordinary local volumes. Volumes with driver options or plugins are not supported."}
	}
	return volume.Mountpoint, nil
}
