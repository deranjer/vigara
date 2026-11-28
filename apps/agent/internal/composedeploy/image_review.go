package composedeploy

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/deranjer/vigara/internal/stack"
)

type liveContainerImage struct {
	Name    string
	Service string
	State   string
	Ref     string
	ID      string
	OneOff  string
}

type localPlanImage struct {
	ID       string
	Created  time.Time
	Version  string
	Revision string
}

func (r *Runner) reviewImages(ctx context.Context, dir string, plan Plan) map[string]stack.ServiceImageReview {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := r.command(ctx, dir, "container", "ls", "--all", "--no-trunc", "--quiet", "--filter", "label=com.docker.compose.project="+plan.ProjectName)
	if err != nil {
		return nil
	}

	byService := map[string][]liveContainerImage{}
	if ids := strings.Fields(string(list)); len(ids) > 0 {
		// Inspect only the fields displayed or compared. Avoid returning container environments.
		const format = `{"name":{{json .Name}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"state":{{json .State.Status}},"ref":{{json .Config.Image}},"id":{{json .Image}},"oneOff":{{json (index .Config.Labels "com.docker.compose.oneoff")}}}`
		args := append([]string{"container", "inspect", "--format", format, "--"}, ids...)
		raw, err := r.command(ctx, dir, args...)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var live liveContainerImage
			if json.Unmarshal([]byte(line), &live) != nil || live.Name == "" {
				return nil
			}
			if !strings.EqualFold(live.OneOff, "true") {
				byService[live.Service] = append(byService[live.Service], live)
			}
		}
	}

	review := make(map[string]stack.ServiceImageReview, len(plan.Services))
	cache := map[string]localPlanImage{}
	for _, service := range plan.Services {
		target := plan.Images[service]
		image, cached := cache[target]
		if !cached && ctx.Err() == nil {
			const format = `{"id":{{json .Id}},"created":{{json .Created}},"version":{{json (index .Config.Labels "org.opencontainers.image.version")}},"revision":{{json (index .Config.Labels "org.opencontainers.image.revision")}}}`
			raw, err := r.command(ctx, dir, "image", "inspect", "--format", format, "--", target)
			if err == nil {
				_ = json.Unmarshal(raw, &image)
			}
			cache[target] = image
		}
		entry := stack.ServiceImageReview{
			Version:    strings.TrimSpace(image.Version),
			Revision:   strings.TrimSpace(image.Revision),
			Containers: []stack.ContainerImageReview{},
		}
		if !image.Created.IsZero() {
			entry.CreatedAt = image.Created.UTC().Format(time.RFC3339)
		}
		for _, live := range byService[service] {
			entry.Containers = append(entry.Containers, stack.ContainerImageReview{
				Name: strings.TrimPrefix(live.Name, "/"), State: live.State,
				CurrentImage: live.Ref, Change: imageChange(live, target, image.ID),
			})
		}
		review[service] = entry
	}
	return review
}

func imageChange(live liveContainerImage, target, targetID string) string {
	if targetID != "" && live.ID != "" {
		if live.ID == targetID {
			return "same"
		}
		return "changed"
	}
	_, oldDigest, pinned := strings.Cut(live.Ref, "@")
	_, newDigest, resolved := strings.Cut(target, "@")
	if pinned && resolved {
		if oldDigest == newDigest {
			return "same"
		}
		return "digest_changed"
	}
	return "unknown"
}
