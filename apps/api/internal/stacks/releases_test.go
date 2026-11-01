package stacks

import (
	"github.com/deranjer/vigara/internal/stack"
	"strings"
	"testing"
)

func TestReleaseRepliesMustMatchCapturedOperation(t *testing.T) {
	s := stack.Snapshot{ID: "web", Commit: strings.Repeat("a", 40), Files: map[string]string{"compose.yaml": "services: {}"}}
	images := map[string]string{"web": "nginx@sha256:" + strings.Repeat("b", 64)}
	valid := stack.Result{Status: "succeeded", Stage: "complete", Readiness: "compose-wait-passed", JobID: strings.Repeat("c", 64), Commit: s.Commit, ConfigurationRevision: stack.ConfigurationRevision(s), PlanToken: strings.Repeat("d", 64), Images: images}
	if got := correlatedResult(valid, s, valid.JobID, valid.PlanToken, images); got.Status != "succeeded" {
		t.Fatal("valid result refused")
	}
	for _, change := range []func(*stack.Result){
		func(r *stack.Result) { r.Readiness = "" }, func(r *stack.Result) { r.Stage = "apply" },
		func(r *stack.Result) { r.JobID = "different" }, func(r *stack.Result) { r.Commit = "different" }, func(r *stack.Result) { r.ConfigurationRevision = "different" }, func(r *stack.Result) { r.PlanToken = "different" }, func(r *stack.Result) { r.Images = map[string]string{"web": "nginx:latest"} },
	} {
		r := valid
		change(&r)
		if correlatedResult(r, s, valid.JobID, valid.PlanToken, images).Status != "unknown" {
			t.Fatal("uncorrelated release accepted")
		}
	}
	if !validReleaseImages(images, []string{"web"}) || validReleaseImages(images, []string{"web", "database"}) || validReleaseImages(map[string]string{"web": "nginx:latest"}, []string{"web"}) {
		t.Fatal("invalid release image validation")
	}
}
