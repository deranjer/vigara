package main

import (
	"strings"
	"testing"
	"time"
)

func TestCLIForcesSuppliedEvidence(t *testing.T) {
	now := time.Now().UTC()
	body := `{"requirements":{"projectName":"media","projectDirectory":"/srv/media","networks":[{"name":"web","external":true}]},"host":{"source":"observed-agent","capturedAt":"` + now.Format(time.RFC3339Nano) + `","complete":true,"networks":[{"name":"web"}]}}`
	r, err := evaluateInput(strings.NewReader(body), now)
	if err != nil {
		t.Fatal("evaluateInput rejected valid JSON")
	}
	if r.EvidenceSource != "supplied" || r.Ready {
		t.Fatalf("CLI trusted self-asserted provenance: %#v", r)
	}
}
func TestCLIRejectsOversizeInput(t *testing.T) {
	input := strings.NewReader(strings.Repeat(" ", 1<<20+1))
	if _, err := evaluateInput(input, time.Now()); err == nil {
		t.Fatal("oversize input was accepted")
	}
}
