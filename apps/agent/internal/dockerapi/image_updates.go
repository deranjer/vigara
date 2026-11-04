package dockerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/imageupdates"
	"github.com/deranjer/vigara/internal/protocol"
)

var updateRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,511}$`)
var updateDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type imagePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}
type imageManifest struct {
	Digest   string        `json:"digest,omitempty"`
	Platform imagePlatform `json:"platform,omitempty"`
	Config   struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		Digest   string        `json:"digest"`
		Platform imagePlatform `json:"platform"`
	} `json:"manifests"`
}

// cappedManifest stops a command from consuming unbounded memory. Stderr is
// discarded because registry diagnostics can contain credentials or URLs.
type cappedManifest struct{ bytes.Buffer }

func (b *cappedManifest) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("manifest too large")
	}
	return b.Buffer.Write(p)
}
func registryManifest(ctx context.Context, ref string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "manifest", "inspect", "--verbose", ref)
	cmd.WaitDelay = time.Second
	var out cappedManifest
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("registry check failed; check Docker CLI, registry credentials, connectivity and rate limits")
	}
	return normalizeRegistryManifest(out.Bytes())
}

// Verbose inspection provides the registry's actual descriptor digest. The
// non-verbose CLI pretty-prints JSON, so hashing its output would be incorrect.
func normalizeRegistryManifest(data []byte) ([]byte, error) {
	type descriptor struct {
		Digest   string
		Platform imagePlatform
	}
	type entry struct {
		Descriptor       descriptor
		Digest           string
		Platform         imagePlatform
		SchemaV2Manifest *imageManifest
		OCIManifest      *imageManifest
	}
	convert := func(e entry) (imageManifest, imagePlatform, error) {
		m := e.SchemaV2Manifest
		if m == nil {
			m = e.OCIManifest
		}
		if m == nil {
			return imageManifest{}, imagePlatform{}, errors.New("registry returned an unsupported manifest")
		}
		m.Digest = e.Descriptor.Digest
		platform := e.Descriptor.Platform
		if m.Digest == "" {
			m.Digest = e.Digest
			platform = e.Platform
		}
		if !updateDigestPattern.MatchString(m.Digest) {
			return imageManifest{}, platform, errors.New("registry manifest digest is unavailable")
		}
		m.Platform = platform
		return *m, platform, nil
	}
	var entries []entry
	if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '[' {
		if json.Unmarshal(data, &entries) != nil {
			return nil, errors.New("registry returned invalid manifests")
		}
		var index imageManifest
		for _, e := range entries {
			m, p, err := convert(e)
			if err != nil {
				return nil, err
			}
			index.Manifests = append(index.Manifests, struct {
				Digest   string        `json:"digest"`
				Platform imagePlatform `json:"platform"`
			}{m.Digest, p})
		}
		return json.Marshal(index)
	}
	var e entry
	if json.Unmarshal(data, &e) != nil {
		return nil, errors.New("registry returned invalid manifest")
	}
	m, _, err := convert(e)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// ImageCheck reads the actual container image ID, not the image currently
// cached under its tag. Only manifest metadata is fetched; no layers are pulled.
func (c *Client) ImageCheck(ctx context.Context, p protocol.DockerImageCheckParams) (protocol.DockerImageCheck, error) {
	return c.imageCheck(ctx, p, c.cachedRegistryManifest)
}

type cachedImageManifest struct {
	data    []byte
	err     error
	expires time.Time
}

func (c *Client) cachedRegistryManifest(ctx context.Context, ref string) ([]byte, error) {
	c.manifestMu.Lock()
	defer c.manifestMu.Unlock()
	if cached, ok := c.manifestCache[ref]; ok && time.Now().Before(cached.expires) {
		return cached.data, cached.err
	}
	data, err := registryManifest(ctx, ref)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if c.manifestCache == nil || len(c.manifestCache) >= 256 {
		c.manifestCache = map[string]cachedImageManifest{}
	}
	c.manifestCache[ref] = cachedImageManifest{data: data, err: err, expires: time.Now().Add(time.Minute)}
	return data, err
}
func (c *Client) imageCheck(ctx context.Context, p protocol.DockerImageCheckParams, fetch func(context.Context, string) ([]byte, error)) (protocol.DockerImageCheck, error) {
	if checkRef(p.ID) != nil {
		return protocol.DockerImageCheck{}, ErrBadRequest
	}
	var raw inspectResponse
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(p.ID)+"/json", nil, maxInspectBytes, &raw); err != nil {
		return protocol.DockerImageCheck{}, err
	}
	r := protocol.DockerImageCheck{ID: raw.ID, Name: strings.TrimPrefix(raw.Name, "/"), Image: raw.Config.Image, RunningImage: raw.Config.Image, Current: raw.Image, Project: raw.Config.Labels["com.docker.compose.project"], Service: raw.Config.Labels["com.docker.compose.service"], Status: "unknown"}
	ref := raw.Config.Image
	if p.Ref != "" {
		ref = p.Ref
	}
	r.Image = ref
	if !updateRefPattern.MatchString(ref) || strings.HasPrefix(ref, "sha256:") {
		r.Message = "No registry tag is available for this image."
		return r, nil
	}
	monitor := imageupdates.MonitoringReference(ref)
	if monitor == "" && strings.Contains(ref, "@") {
		r.Status = "pinned"
		r.Message = "No tracking tag is available. Set a monitoring tag to check this pinned image. Its deployed digest will stay unchanged."
		return r, nil
	}
	if monitor == "" {
		r.Message = "No registry tag is available for this image."
		return r, nil
	}
	ref = monitor
	r.Image = ref
	var platform imagePlatform
	if err := c.getJSON(ctx, "/images/"+url.PathEscape(raw.Image)+"/json", nil, maxInspectBytes, &platform); err != nil {
		r.Message = "The running image platform could not be read."
		return r, nil
	}
	if platform.OS == "" || platform.Architecture == "" || !updateDigestPattern.MatchString(raw.Image) {
		r.Message = "The running image identity is unavailable."
		return r, nil
	}
	r.Platform = platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		r.Platform += "/" + platform.Variant
	}
	data, err := fetch(ctx, ref)
	if err != nil {
		r.Message = err.Error()
		return r, nil
	}
	var manifest imageManifest
	if json.Unmarshal(data, &manifest) != nil {
		r.Message = "Registry returned an unsupported manifest."
		return r, nil
	}
	r.Manifest = manifest.Digest
	if len(manifest.Manifests) > 0 {
		digest := ""
		for _, m := range manifest.Manifests {
			if m.Platform.OS == platform.OS && m.Platform.Architecture == platform.Architecture && (m.Platform.Variant == platform.Variant || platform.Variant == "" && m.Platform.Variant == "v8" && platform.Architecture == "arm64") {
				if digest != "" {
					r.Message = "Registry platform selection is ambiguous."
					return r, nil
				}
				digest = m.Digest
			}
		}
		if !updateDigestPattern.MatchString(digest) {
			r.Message = "No matching platform manifest is available."
			return r, nil
		}
		r.Manifest = digest
		repository := ref
		if i := strings.LastIndex(repository, ":"); i > strings.LastIndex(repository, "/") {
			repository = repository[:i]
		}
		data, err = fetch(ctx, repository+"@"+digest)
		if err != nil {
			r.Message = err.Error()
			return r, nil
		}
		if json.Unmarshal(data, &manifest) != nil {
			r.Message = "Registry returned an invalid platform manifest."
			return r, nil
		}
		if manifest.Digest != "" && manifest.Digest != digest {
			r.Message = "Registry returned a different platform manifest."
			return r, nil
		}
	}
	if !updateDigestPattern.MatchString(manifest.Config.Digest) {
		r.Message = "Registry image identity is unavailable."
		return r, nil
	}
	if manifest.Platform.OS != "" && (manifest.Platform.OS != platform.OS || manifest.Platform.Architecture != platform.Architecture || manifest.Platform.Variant != platform.Variant && !(platform.Architecture == "arm64" && platform.Variant == "" && manifest.Platform.Variant == "v8")) {
		r.Message = "The registry image platform does not match the running image."
		return r, nil
	}
	r.Available = manifest.Config.Digest
	r.Status = "current"
	if r.Current != r.Available {
		r.Status = "available"
	}
	return r, nil
}
