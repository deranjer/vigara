package docker

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/composeadoption"
)

const (
	maxComposeContainers = 4096
	maxComposeResponse   = 16 << 20
)

// ObserveCompose performs only Docker Engine GET requests and returns the
// complete Compose-labeled runtime metadata required by adoption preview. It
// deliberately decodes no container environment/config or arbitrary labels.
func (c *Collector) ObserveCompose(ctx context.Context) (composeadoption.RuntimeSnapshot, error) {
	if !c.cfg.Enabled {
		return composeadoption.RuntimeSnapshot{}, errors.New("Docker observation is disabled by local configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var items []container
	if err := c.getLimit(ctx, "/containers/json?all=1", &items, maxComposeResponse); err != nil {
		return composeadoption.RuntimeSnapshot{}, errors.New("could not obtain complete Docker container inventory")
	}
	if len(items) > maxComposeContainers {
		return composeadoption.RuntimeSnapshot{}, errors.New("Docker container inventory exceeds the adoption limit")
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.Labels["com.docker.compose.project"] != "" {
			if !safeID(item.ID) {
				return composeadoption.RuntimeSnapshot{}, errors.New("Docker returned an invalid Compose container identity")
			}
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	snapshot := composeadoption.RuntimeSnapshot{Complete: true, Containers: make([]composeadoption.RuntimeContainer, 0, len(ids))}
	for _, id := range ids {
		var observed composeInspect
		if err := c.getLimit(ctx, "/containers/"+url.PathEscape(id)+"/json", &observed, maxComposeResponse); err != nil {
			return composeadoption.RuntimeSnapshot{}, errors.New("could not inspect complete Compose container metadata")
		}
		labels := observed.Config.Labels
		project, service := labels["com.docker.compose.project"], labels["com.docker.compose.service"]
		workingDir, configFiles := labels["com.docker.compose.project.working_dir"], labels["com.docker.compose.project.config_files"]
		files := splitConfigFiles(configFiles)
		if observed.ID != id || project == "" || service == "" || workingDir == "" || len(files) == 0 || observed.State.Status == "" || observed.State.StartedAt == "" {
			return composeadoption.RuntimeSnapshot{}, errors.New("Compose container labels or runtime identity are incomplete")
		}
		entry := composeadoption.RuntimeContainer{ID: observed.ID, ProjectName: project, ServiceName: service, WorkingDir: workingDir, ComposeFiles: files, State: observed.State.Status, StartedAt: observed.State.StartedAt, StorageBinds: []composeadoption.StorageBind{}}
		for _, mount := range observed.Mounts {
			entry.StorageBinds = append(entry.StorageBinds, composeadoption.StorageBind{Source: mount.Source, Destination: mount.Destination, ReadWrite: mount.RW, Type: mount.Type, Name: mount.Name, Driver: mount.Driver, Propagation: mount.Propagation})
		}
		snapshot.Containers = append(snapshot.Containers, entry)
	}
	snapshot.ObservedAt = time.Now().UTC()
	return snapshot, nil
}

type composeInspect struct {
	ID    string `json:"Id"`
	State struct {
		Status    string `json:"Status"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
		Name        string `json:"Name"`
		Driver      string `json:"Driver"`
		Propagation string `json:"Propagation"`
	} `json:"Mounts"`
}

func splitConfigFiles(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
