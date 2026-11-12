package notifytransport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNtfyPublishesWithPriorityAndActions(t *testing.T) {
	var got ntfyPayload
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"id":"abc","event":"message"}`))
	}))
	defer server.Close()
	target, err := newNtfyTarget(server.URL+"/", "vigara-alerts", "tk_secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if target.Kind != "ntfy" || strings.Contains(target.Display, "tk_secret") || !strings.HasSuffix(target.Display, "· topic vigara-alerts") {
		t.Fatalf("display=%q", target.Display)
	}
	err = target.Send(context.Background(), Message{Transition: "firing", Severity: "critical", RuleName: "Device offline", DeviceName: "nas-01", Summary: "has not checked in",
		OpenURL: "https://v.example/#/alerts", AcknowledgeURL: "https://v.example/api/alerts/actions/a", MuteURL: "https://v.example/api/alerts/actions/m"})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tk_secret" || got.Topic != "vigara-alerts" || got.Priority != 5 || got.Title != "Device offline" || got.Message != "nas-01: has not checked in" || got.Click != "https://v.example/#/alerts" {
		t.Fatalf("payload=%+v auth=%q", got, auth)
	}
	if len(got.Actions) != 2 || got.Actions[0].Label != "Acknowledge" || got.Actions[0].Method != "POST" || got.Actions[1].Label != "Mute 24 h" {
		t.Fatalf("actions=%+v", got.Actions)
	}
	got = ntfyPayload{}
	_ = target.Send(context.Background(), Message{Transition: "resolved", Severity: "critical", RuleName: "Device offline", Summary: "nas-01 is back"})
	if got.Priority != 2 || got.Title != "Device offline cleared" || len(got.Actions) != 0 || got.Click != "" {
		t.Fatalf("resolved payload=%+v", got)
	}
}

func TestNtfyConfiguration(t *testing.T) {
	env := func(values map[string]string) func(string) string { return func(k string) string { return values[k] } }
	if _, err := fromEnv(env(map[string]string{NtfyURLVar: "http://ntfy.local"})); err == nil {
		t.Fatal("ntfy without topic accepted")
	}
	if _, err := fromEnv(env(map[string]string{NtfyURLVar: "http://ntfy.local", NtfyTopicVar: "bad topic"})); err == nil {
		t.Fatal("invalid topic accepted")
	}
	targets, err := fromEnv(env(map[string]string{NtfyURLVar: "http://ntfy.local:8090", NtfyTopicVar: "vigara"}))
	if err != nil || len(targets) != 1 || targets[0].ID != NtfyTargetID || targets[0].Display != "http://ntfy.local:8090 · topic vigara" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
}
