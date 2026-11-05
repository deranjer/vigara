package protocol

import "testing"

func TestHostRunNormalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   HostRunParams
		ok   bool
	}{
		{"df", HostRunParams{Command: "df"}, true},
		{"case and space", HostRunParams{Command: " DF "}, true},
		{"ls path", HostRunParams{Command: "ls", Path: "/srv/stacks/web"}, true},
		{"du root", HostRunParams{Command: "du", Path: "/"}, true},
		{"unknown", HostRunParams{Command: "rm", Path: "/x"}, false},
		{"shell word", HostRunParams{Command: "df; id"}, false},
		{"path on pathless command", HostRunParams{Command: "df", Path: "/"}, false},
		{"ls without path", HostRunParams{Command: "ls"}, false},
		{"relative", HostRunParams{Command: "ls", Path: "etc"}, false},
		{"dotdot", HostRunParams{Command: "ls", Path: "/srv/../etc"}, false},
		{"trailing slash", HostRunParams{Command: "ls", Path: "/srv/"}, false},
		{"double slash", HostRunParams{Command: "ls", Path: "//etc"}, false},
		{"metachar", HostRunParams{Command: "ls", Path: "/tmp/$(id)"}, false},
		{"newline", HostRunParams{Command: "ls", Path: "/tmp/a\nb"}, false},
		{"too long", HostRunParams{Command: "ls", Path: "/" + string(make([]byte, 2000))}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.Normalize(); (err == nil) != tc.ok {
				t.Fatalf("Normalize() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestHostRunNeedsHostExecCapability(t *testing.T) {
	if CheckHostExec(MethodHostRun, []string{CapabilityDockerExec}) == nil {
		t.Fatal("host.run allowed without the device shell opt-in")
	}
	if err := CheckHostExec(MethodHostRun, []string{CapabilityHostExec}); err != nil {
		t.Fatal(err)
	}
}
