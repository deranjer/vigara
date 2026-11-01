package stacks

import (
	"github.com/deranjer/vigara/internal/stack"
	"regexp"
)

var releaseImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)

func validReleaseImages(images map[string]string, services []string) bool {
	if len(images) == 0 || len(images) > 256 || len(images) != len(services) {
		return false
	}
	seen := map[string]bool{}
	for _, service := range services {
		if service == "" || seen[service] || len(service) > 128 || !releaseImagePattern.MatchString(images[service]) {
			return false
		}
		seen[service] = true
	}
	return true
}

// A reply or journal belongs to this exact operation, configuration and image
// set. Old agents cannot turn an uncorrelated acknowledgement into success.
func correlatedResult(result stack.Result, s stack.Snapshot, id, token string, images map[string]string) stack.Result {
	if result.Status != "succeeded" && result.Status != "failed" {
		return stack.Result{Status: "unknown", Stage: "reconcile", Message: "The device has not confirmed a final release result."}
	}
	if result.JobID != id || result.Commit != s.Commit || result.ConfigurationRevision != stack.ConfigurationRevision(s) || result.PlanToken != token || !stack.Equal(result.Images, images) {
		return stack.Result{Status: "unknown", Stage: "reconcile", Message: "The device result does not match this release. Inspect the device before another deployment."}
	}
	if result.Status == "succeeded" && (result.Readiness != "compose-wait-passed" || result.Stage != "complete") {
		return stack.Result{Status: "unknown", Stage: "reconcile", Message: "The device has not confirmed release readiness."}
	}
	return result
}
