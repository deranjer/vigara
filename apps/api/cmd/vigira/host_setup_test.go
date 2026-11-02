package main

import (
	"bytes"
	"testing"
)

func TestHostAgentBundleRequiresExplicitOptions(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}, {"--name", "host"}} {
		var output, diagnostics bytes.Buffer
		if err := hostAgentBundle(args, &output, &diagnostics); err == nil || output.Len() != 0 {
			t.Fatalf("invalid CLI options accepted or wrote credentials: %v", args)
		}
	}
}
