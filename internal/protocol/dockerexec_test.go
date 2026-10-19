package protocol

import "testing"

func TestExecParamsNormalize(t *testing.T) {
	for _, tc := range []struct {
		in   DockerExecParams
		want DockerExecParams
		ok   bool
	}{
		{DockerExecParams{ID: "web"}, DockerExecParams{ID: "web", Shell: "auto", Cols: 80, Rows: 24}, true},
		{DockerExecParams{ID: "web", Shell: " BASH ", User: " root ", Cols: 200, Rows: 50}, DockerExecParams{ID: "web", Shell: "bash", User: "root", Cols: 200, Rows: 50}, true},
		{DockerExecParams{ID: "web", Shell: "sh", User: "1000:1000", Cols: 3, Rows: 9000}, DockerExecParams{ID: "web", Shell: "sh", User: "1000:1000", Cols: 10, Rows: 500}, true},
		{DockerExecParams{ID: "web", User: "www-data:www.data"}, DockerExecParams{ID: "web", Shell: "auto", User: "www-data:www.data", Cols: 80, Rows: 24}, true},
		{DockerExecParams{ID: "web", Shell: "zsh"}, DockerExecParams{}, false},
		{DockerExecParams{ID: "web", Shell: "/bin/sh -c reboot"}, DockerExecParams{}, false},
		{DockerExecParams{ID: "web", User: "root; id"}, DockerExecParams{}, false},
		{DockerExecParams{ID: "web", User: "a:b:c"}, DockerExecParams{}, false},
		{DockerExecParams{ID: "web", User: "$(id)"}, DockerExecParams{}, false},
		{DockerExecParams{ID: "web", User: "-u"}, DockerExecParams{ID: "web", Shell: "auto", User: "-u", Cols: 80, Rows: 24}, true},
	} {
		got := tc.in
		err := got.Normalize()
		if (err == nil) != tc.ok {
			t.Errorf("Normalize(%+v) error = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("Normalize(%+v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestExecIsAnExecTierMethod(t *testing.T) {
	tier, known := ChannelMethodTier(MethodDockerExec)
	if !known || tier != DockerModeExec {
		t.Fatalf("docker.exec tier = %q known=%v", tier, known)
	}
	if err := CheckChannelTier(tier, DockerModeManage); err == nil {
		t.Fatal("the manage tier must not open terminals")
	}
	if err := CheckChannelTier(tier, DockerModeExec); err != nil {
		t.Fatal(err)
	}
	if ClampExecSize(0, 80) != 80 || ClampExecSize(5, 80) != MinExecSize || ClampExecSize(9999, 80) != MaxExecSize {
		t.Fatal("ClampExecSize bounds")
	}
}
