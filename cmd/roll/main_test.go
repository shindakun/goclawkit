package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shindakun/goclawkit/pkg/ipc"
	"github.com/shindakun/goclawkit/pkg/plugin"
)

// resultPattern matches the demo's output, e.g. "2d6 -> [4, 5] = 9" or
// "d20 -> [8] = 8" (the count is optional, defaulting to 1).
var resultPattern = regexp.MustCompile(`^\d*d\d+ -> \[(\d+, )*\d+\] = \d+$`)

func TestRollParsing(t *testing.T) {
	cases := []struct {
		notation string
		wantErr  bool
		count    int // expected number of individual rolls (when no error)
	}{
		{"2d6", false, 2},
		{"d20", false, 1}, // N defaults to 1
		{"3d8", false, 3},
		{"", true, 0},
		{"2d", true, 0},     // missing sides
		{"xd6", true, 0},    // bad count
		{"0d6", true, 0},    // count below range
		{"101d6", true, 0},  // count above range
		{"1d1", true, 0},    // sides below range
		{"1d1001", true, 0}, // sides above range
	}
	for _, tc := range cases {
		t.Run(tc.notation, func(t *testing.T) {
			got, err := roll(tc.notation)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("roll(%q) = %q, want error", tc.notation, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("roll(%q) error: %v", tc.notation, err)
			}
			if !resultPattern.MatchString(got) {
				t.Errorf("roll(%q) = %q, does not match result pattern", tc.notation, got)
			}
			// The bracketed list should hold exactly `count` numbers.
			inner := got[strings.Index(got, "[")+1 : strings.Index(got, "]")]
			if n := len(strings.Split(inner, ", ")); n != tc.count {
				t.Errorf("roll(%q) rolled %d dice, want %d", tc.notation, n, tc.count)
			}
		})
	}
}

// TestWireProtocolEndToEnd is the "talk to it by hand" smoke test (checklist item
// 4), automated: it builds the roll binary, launches it, writes a real hello control
// frame then a tool.invoke request over its stdin, and asserts a hello.ok (with the
// right Info) then a correlated result come back on its stdout. This proves the
// length-prefixed binary protocol works over actual OS pipes, which -selftest (a
// direct in-process call) does not exercise.
func TestWireProtocolEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "roll")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building roll binary: %v", err)
	}

	cmd := exec.Command(bin)
	cmd.Stderr = os.Stderr // plugin logs go to stderr; surface them on failure
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting roll: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	out := ipc.NewSession(rw{r: stdout, w: stdin})

	// Handshake.
	hello, _ := ipc.Marshal(plugin.Hello{Magic: ipc.Magic, ProtocolVer: ipc.ProtocolVer})
	if err := out.Send(ipc.Frame{Type: ipc.FrameControl, Topic: "hello", Payload: hello}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	helloOK, err := out.Recv()
	if err != nil {
		t.Fatalf("recv hello.ok: %v", err)
	}
	if helloOK.Type != ipc.FrameControl || helloOK.Topic != "hello.ok" {
		t.Fatalf("expected hello.ok, got type=%d topic=%q", helloOK.Type, helloOK.Topic)
	}
	var hok plugin.HelloOK
	if err := ipc.Unmarshal(helloOK.Payload, &hok); err != nil {
		t.Fatalf("decode hello.ok: %v", err)
	}
	if hok.Info.Name != "roll" || hok.Info.Kind != plugin.KindTool || len(hok.Info.Tools) != 1 {
		t.Errorf("hello.ok Info = %+v, want roll/tool with one tool", hok.Info)
	}

	// Invoke.
	const reqID = 1
	invoke, _ := ipc.Marshal(plugin.Invoke{Tool: "roll", Args: []byte(`{"notation":"2d6"}`)})
	if err := out.Send(ipc.Frame{Type: ipc.FrameRequest, ID: reqID, Topic: "tool.invoke", Payload: invoke}); err != nil {
		t.Fatalf("send invoke: %v", err)
	}
	res, err := out.Recv()
	if err != nil {
		t.Fatalf("recv result: %v", err)
	}
	if res.Type != ipc.FrameResult || res.ID != reqID {
		t.Fatalf("expected result id=%d, got type=%d id=%d", reqID, res.Type, res.ID)
	}
	var r plugin.Result
	_ = ipc.Unmarshal(res.Payload, &r)
	if r.IsError || !resultPattern.MatchString(r.Text) {
		t.Errorf("result = %+v, want a clean roll result", r)
	}

	// Graceful shutdown.
	_ = out.Send(ipc.Frame{Type: ipc.FrameControl, Topic: "shutdown"})
}

// rw adapts the child's stdout/stdin pipes into a Transport.
type rw struct {
	r interface{ Read([]byte) (int, error) }
	w interface{ Write([]byte) (int, error) }
}

func (x rw) Read(p []byte) (int, error)  { return x.r.Read(p) }
func (x rw) Write(p []byte) (int, error) { return x.w.Write(p) }
