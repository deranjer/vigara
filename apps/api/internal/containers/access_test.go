package containers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/deranjer/vigara/apps/api/internal/auth"
	"github.com/deranjer/vigara/internal/protocol"
)

type fixedGrants []auth.ResourceGrant

func (g fixedGrants) ResourceGrants(context.Context, string) ([]auth.ResourceGrant, error) {
	return g, nil
}

func TestRestrictedContainerAccessAndActionBoundaries(t *testing.T) {
	gameID := strings.Repeat("a", 64)
	dbID := strings.Repeat("b", 64)
	f := newFixture(t, func(c *Config) {
		c.Grants = fixedGrants{{AgentID: agentManage, Kind: "service", Resource: "dragonwilds", Service: "game", Restart: true}}
	})
	agent := f.connectAgent(agentManage)
	agent.serve(func(call protocol.ChannelFrame) protocol.ChannelFrame {
		switch call.Method {
		case protocol.MethodDockerList:
			return protocol.ChannelFrame{Result: json.RawMessage(fmt.Sprintf(`{"containers":[{"id":%q,"name":"game","project":"dragonwilds","service":"game"},{"id":%q,"name":"private-db","project":"dragonwilds","service":"db"}]}`, gameID, dbID))}
		case protocol.MethodDockerInspect:
			var p protocol.DockerInspectParams
			_ = json.Unmarshal(call.Params, &p)
			service := "game"
			if p.ID == dbID {
				service = "db"
			}
			return protocol.ChannelFrame{Result: json.RawMessage(fmt.Sprintf(`{"id":%q,"name":"game","image":"game:1","state":{"running":true},"command":["secret-command"],"mounts":[{"source":"private-path"}],"labels":{"com.docker.compose.project":"dragonwilds","com.docker.compose.service":%q}}`, p.ID, service))}
		case protocol.MethodDockerAction:
			var p protocol.DockerActionParams
			_ = json.Unmarshal(call.Params, &p)
			if p.ID != gameID || p.Action != "restart" {
				t.Errorf("unauthorized command reached agent: %+v", p)
			}
			return protocol.ChannelFrame{Result: json.RawMessage(`{"changed":true}`)}
		default:
			t.Errorf("unexpected agent call %s", call.Method)
			return protocol.ChannelFrame{}
		}
	})
	var fleet struct{ Hosts []Host }
	decode(t, f.get("/api/containers", auth.RoleRestricted), &fleet)
	if len(fleet.Hosts) != 1 || len(fleet.Hosts[0].Containers) != 1 || fleet.Hosts[0].Containers[0].ID != gameID {
		t.Fatalf("hidden resources exposed: %+v", fleet)
	}
	path := "/api/containers/" + agentManage + "/" + gameID
	resp := f.get(path+"/inspect", auth.RoleRestricted)
	body := bodyText(resp)
	if resp.StatusCode != 200 || strings.Contains(body, "private-path") || strings.Contains(body, "secret-command") {
		t.Fatalf("inspect leaked configuration: %d %s", resp.StatusCode, body)
	}
	for _, suffix := range []string{"/logs/snapshot", "/logs/search?match=JoinCode", "/exec"} {
		resp := f.get(path+suffix, auth.RoleRestricted)
		if resp.StatusCode == 200 || resp.StatusCode == 101 {
			t.Errorf("allowed %s", suffix)
		}
	}
	if resp := f.get("/api/containers/"+agentManage+"/"+dbID+"/inspect", auth.RoleRestricted); resp.StatusCode != 404 {
		t.Fatalf("other service accessible: %d", resp.StatusCode)
	}
	if resp := f.get("/api/containers/"+agentManage+"/game/inspect", auth.RoleRestricted); resp.StatusCode != 404 {
		t.Fatal("restricted account can target mutable name")
	}
	for _, body := range []string{`{"action":"remove"}`, `{"action":"kill"}`, `{"action":"stop"}`, `{"action":"restart","force":true}`, `{"action":"restart","signal":"SIGKILL"}`} {
		resp := f.post(path+"/actions", auth.RoleRestricted, body, nil)
		if resp.StatusCode == 200 {
			t.Errorf("allowed %s", body)
		}
	}
	if resp := f.post(path+"/recreate", auth.RoleRestricted, `{"pull":true}`, nil); resp.StatusCode == 200 {
		t.Fatal("restart grant permitted pull and recreate")
	}
	if resp := f.post(path+"/actions", auth.RoleRestricted, `{"action":"restart"}`, nil); resp.StatusCode != 200 {
		t.Fatalf("restart denied: %d %s", resp.StatusCode, bodyText(resp))
	}
}

func TestExtractLogValuesRejectsPartialOrInvalidValues(t *testing.T) {
	rule := logValueRule{Label: "Join code", Match: "JoinCode", Prefix: "value[", Suffix: "]"}
	if err := validateLogValue(rule); err != nil {
		t.Fatal(err)
	}
	line := protocol.DockerLogLine{M: `LogNetSessionSettings: Setting ["JoinCode"] written with key[xz] value[ABCD-1234]`}
	if got := extractLogValue(rule, line); got != "ABCD-1234" {
		t.Fatalf("extracted %q", got)
	}
	line.X = true
	if extractLogValue(rule, line) != "" {
		t.Fatal("accepted shortened line")
	}
	rule.Pattern = `value\[([^]]+)\]`
	rule.Prefix = ""
	rule.Suffix = ""
	line.X = false
	if err := validateLogValue(rule); err != nil || extractLogValue(rule, line) != "ABCD-1234" {
		t.Fatalf("regex extraction failed %v", err)
	}
	rule.Pattern = `(one)(two)`
	if validateLogValue(rule) == nil {
		t.Fatal("ambiguous capture groups accepted")
	}
}
