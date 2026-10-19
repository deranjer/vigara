package dockerapi

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/deranjer/vigara/internal/protocol"
)

func (e *fakeExecEngine) snapshot() (creates, detached []map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.creates...), append([]map[string]any(nil), e.detached...)
}

func TestTheShellsPIDAnnouncementIsStrippedAndUsedToHangItUpOnClose(t *testing.T) {
	client, e := newExecClient(t)
	e.announce = "4242"
	session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web", User: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	// The announcement never reaches the terminal, only what follows it.
	if got := readSome(t, session, "$ "); got != "$ " {
		t.Fatalf("output = %q, want only the prompt (the announcement must be stripped)", got)
	}
	if creates, _ := e.snapshot(); len(creates) != 1 {
		t.Fatalf("no second exec may exist before the terminal closes: %d", len(creates))
	}

	// Closing the terminal hangs the shell up from a second, detached exec.
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	creates, detached := e.snapshot()
	if len(creates) != 2 || len(detached) != 1 {
		t.Fatalf("expected one hang-up exec, got %d creates and %d detached starts", len(creates), len(detached))
	}
	hangup := creates[1]
	cmd := hangup["Cmd"].([]any)
	script := cmd[2].(string)
	if cmd[0] != "/bin/sh" || cmd[1] != "-c" || !strings.Contains(script, "kill -HUP 4242") || !strings.Contains(script, "kill -KILL 4242") {
		t.Fatalf("hang-up command = %v", cmd)
	}
	if hangup["User"] != "1000" || hangup["Tty"] != false || hangup["AttachStdin"] != false || hangup["AttachStdout"] != false {
		t.Fatalf("the hang-up must run detached as the terminal's own user: %v", hangup)
	}
	if detached[0]["Detach"] != true {
		t.Fatalf("detached start = %v", detached[0])
	}
	// Closing twice must not hang up twice.
	_ = session.Close()
	if creates, _ := e.snapshot(); len(creates) != 2 {
		t.Fatalf("a second Close created another exec: %d", len(creates))
	}
}

func TestAnAnnouncementSplitAcrossReadsIsStillStripped(t *testing.T) {
	client, e := newExecClient(t)
	e.announce, e.splitAnnounce = "777", true
	session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readSome(t, session, "$ "); got != "$ " {
		t.Fatalf("output = %q", got)
	}
	_ = session.Close()
	if _, detached := e.snapshot(); len(detached) != 1 {
		t.Fatalf("the split announcement was not understood: %d hang-ups", len(detached))
	}
}

func TestNothingIsHungUpWhenTheShellExitedItselfOrNeverAnnounced(t *testing.T) {
	// The shell exits by itself: there is nothing left to signal.
	client, e := newExecClient(t)
	e.announce = "4242"
	session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	readSome(t, session, "$ ")
	if _, err := session.Write([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(session); err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
	if creates, _ := e.snapshot(); len(creates) != 1 {
		t.Fatalf("a shell that already exited was signalled anyway: %d execs", len(creates))
	}

	// No announcement (another kind of shell, or output that never contained one).
	client2, e2 := newExecClient(t)
	session2, err := client2.StartExec(context.Background(), protocol.DockerExecParams{ID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readSome(t, session2, "$ "); got != "$ " {
		t.Fatalf("output = %q", got)
	}
	_ = session2.Close()
	if creates, _ := e2.snapshot(); len(creates) != 1 {
		t.Fatalf("with no announcement there is nothing to signal: %d execs", len(creates))
	}
}

func TestMalformedOrHostileAnnouncementsAreHarmless(t *testing.T) {
	for name, announce := range map[string]string{
		"not a number": "abc", "pid one": "1", "zero": "0", "negative": "-5", "huge": "99999999999999999999", "injection": "1;reboot",
	} {
		t.Run(name, func(t *testing.T) {
			client, e := newExecClient(t)
			e.announce = announce
			session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web"})
			if err != nil {
				t.Fatal(err)
			}
			if got := readSome(t, session, "$ "); got != "$ " {
				t.Fatalf("output = %q", got)
			}
			_ = session.Close()
			if creates, _ := e.snapshot(); len(creates) != 1 {
				t.Fatalf("an invalid PID %q led to a hang-up: %d execs", announce, len(creates))
			}
		})
	}
}

func TestOutputThatLooksLikeTheStartOfAnAnnouncementIsPassedThrough(t *testing.T) {
	client, e := newExecClient(t)
	// An unfinished announcement followed by ordinary output must not be swallowed forever.
	e.greeting = "\x1b]777;vigara-pid=" + strings.Repeat("x", 400) + "after"
	session, err := client.StartExec(context.Background(), protocol.DockerExecParams{ID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	buf := make([]byte, 512)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasSuffix(got, "after") && time.Now().Before(deadline) {
		n, err := session.Read(buf)
		got += string(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.HasPrefix(got, "\x1b]777;vigara-pid=") || !strings.HasSuffix(got, "after") || len(got) != len("\x1b]777;vigara-pid=")+400+len("after") {
		t.Fatalf("output was altered or lost: %d bytes", len(got))
	}
	_ = session.Close()
	if creates, _ := e.snapshot(); len(creates) != 1 {
		t.Fatalf("an unfinished announcement must not trigger a hang-up: %d execs", len(creates))
	}
}
