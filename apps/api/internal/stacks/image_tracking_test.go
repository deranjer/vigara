package stacks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestLegacyDeploymentRecoversMonitoringTagsWithoutRedeploy(t *testing.T) {
	a, _, s, agent, hub := imageFixture(t)
	ctx := context.Background()
	if _, err := a.Pool.Exec(ctx, "UPDATE stack_deployments SET tracking='{}' WHERE stack_id='demo'"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(ctx, "INSERT INTO image_update_hosts(agent_id) VALUES($1)", agent); err != nil {
		t.Fatal(err)
	}
	pin := "@sha256:" + strings.Repeat("a", 64)
	hub.containers = []protocol.DockerContainerInfo{{ID: "web", Name: "web", Image: "nginx:stable" + pin, Project: "demo", Service: "web"}, {ID: "db", Name: "db", Image: "postgres" + pin, Project: "demo", Service: "db"}}
	if err := a.checkImageHost(ctx, agent); err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{}
	for _, p := range hub.requestedChecks {
		requests[p.ID] = p.Ref
	}
	if requests["web"] != "nginx:stable" || requests["db"] != "postgres:17" {
		t.Fatal("source tags were not recovered", requests)
	}
	var raw []byte
	if err := a.Pool.QueryRow(ctx, "SELECT tracking FROM stack_deployments WHERE stack_id=$1", s.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	tracking := map[string]string{}
	_ = json.Unmarshal(raw, &tracking)
	if tracking["db"] != "postgres:17" {
		t.Fatal("recovered tracking was not retained", tracking)
	}
	if len(hub.deployed) != 0 {
		t.Fatal("monitoring redeployed a container")
	}
}

func TestMonitoringTagOverrideAndTaggedRuntimePin(t *testing.T) {
	a, mux, _, agent, hub := imageFixture(t)
	ctx := context.Background()
	pin := "@sha256:" + strings.Repeat("a", 64)
	// Neither external container has a managed deployment from which to recover a tag.
	hub.containers = []protocol.DockerContainerInfo{{ID: "web", Name: "web", Image: "nginx:stable" + pin}, {ID: "db", Name: "db", Image: "postgres" + pin}}
	if _, err := a.Pool.Exec(ctx, "INSERT INTO image_update_hosts(agent_id) VALUES($1)", agent); err != nil {
		t.Fatal(err)
	}
	for _, c := range hub.containers {
		r := protocol.DockerImageCheck{ID: c.ID, Name: c.Name, Image: c.Image, Status: "pinned"}
		if err := a.storeImageCheck(ctx, agent, r); err != nil {
			t.Fatal(err)
		}
	}
	url := "/api/image-updates/containers/" + agent + "/db/tracking"
	if w := imageCall(mux, "PUT", url, map[string]string{"tag": "other/repo:latest"}); w.Code != 422 {
		t.Fatal("repository change accepted", w.Code, w.Body)
	}
	if w := imageCall(mux, "PUT", url, map[string]string{"tag": "17"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := a.checkImageHost(ctx, agent); err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{}
	for _, p := range hub.requestedChecks {
		requests[p.ID] = p.Ref
	}
	if requests["web"] != "nginx:stable" || requests["db"] != "postgres:17" {
		t.Fatal(requests)
	}
	var count int
	if err := a.Pool.QueryRow(ctx, "SELECT count(*) FROM stack_update_policies WHERE enabled").Scan(&count); err != nil || count != 0 {
		t.Fatal("monitoring enabled automatic updates", count, err)
	}
	if w := imageCall(mux, "PUT", url, map[string]string{"tag": ""}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var tag string
	if err := a.Pool.QueryRow(ctx, "SELECT tracking_tag FROM container_image_updates WHERE container_id='db'").Scan(&tag); err != nil || tag != "" {
		t.Fatal("override was not cleared", tag, err)
	}
}

func TestRecoveryPreservesExplicitPinsAndRejectsUnresolvedInterpolation(t *testing.T) {
	_, _, s, _, _ := imageFixture(t)
	s.Files["compose.yaml"] = "services:\n  web:\n    image: ${WEB_IMAGE}\n  db:\n    image: postgres:18\n"
	known := map[string]string{"db": "postgres@sha256:" + strings.Repeat("a", 64)}
	got := recoverImageTracking(s, known)
	if _, ok := got["web"]; ok {
		t.Fatal("unresolved interpolation was treated as a tag")
	}
	if got["db"] != known["db"] {
		t.Fatal("existing explicit pin overwritten", got)
	}
}

func TestMonitoringOverrideDoesNotAuthorizeDifferentAutomaticTag(t *testing.T) {
	a, mux, _, agent, hub := imageFixture(t)
	ctx := context.Background()
	result := hub.checks["web"]
	result.Image = "nginx:other-channel"
	if err := a.storeImageCheck(ctx, agent, result); err != nil {
		t.Fatal(err)
	}
	window := openImageWindow()
	if w := imageCall(mux, "PUT", "/api/stacks/demo/update-policy", updatePolicy{Enabled: true, Maintenance: &window, Excluded: []string{"db"}}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := a.queueImageUpdate(ctx, "demo", imageSettings{Interval: 360, Maintenance: window}); err == nil {
		t.Fatal("monitoring override authorized a different deployment channel")
	}
	var count int
	if err := a.Pool.QueryRow(ctx, "SELECT count(*) FROM stack_deployments WHERE automatic").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
