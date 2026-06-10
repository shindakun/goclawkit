package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/shindakun/goclawkit/pkg/ipc"
)

// fakeTool is a configurable Tool for driving serve.
type fakeTool struct {
	name   string
	invoke func(ctx context.Context, args json.RawMessage) (string, error)
}

func (f fakeTool) Info() ToolInfo {
	return ToolInfo{Name: f.name, Description: "fake " + f.name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (f fakeTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	return f.invoke(ctx, args)
}

// harness drives serve over an in-memory pipe pair. The test writes frames to the
// plugin via `host` and reads the plugin's frames back from it; serve runs in a
// goroutine. done carries serve's return value.
type harness struct {
	host *ipc.Session
	done chan error
}

func newHarness(t *testing.T, ts ToolSet) *harness {
	t.Helper()
	// hostW -> pluginR (host writes, plugin reads); pluginW -> hostR (plugin writes,
	// host reads).
	hostR, pluginW := io.Pipe()
	pluginR, hostW := io.Pipe()

	pluginSide := rwc{r: pluginR, w: pluginW}
	host := ipc.NewSession(rwc{r: hostR, w: hostW})

	done := make(chan error, 1)
	go func() { done <- serve(ts, pluginSide) }()

	t.Cleanup(func() {
		_ = pluginW.Close()
		_ = hostW.Close()
	})
	return &harness{host: host, done: done}
}

func (h *harness) sendHello(t *testing.T, magic string, ver int) {
	t.Helper()
	payload, _ := ipc.Marshal(Hello{Magic: magic, ProtocolVer: ver})
	if err := h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicHello, Payload: payload}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
}

func (h *harness) recv(t *testing.T) ipc.Frame {
	t.Helper()
	f, err := h.host.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return f
}

func oneTool(name string, fn func(ctx context.Context, args json.RawMessage) (string, error)) ToolSet {
	return ToolSet{Name: name, Version: "1.0.0", Tools: []Tool{fakeTool{name: name, invoke: fn}}}
}

func okTool() ToolSet {
	return oneTool("roll", func(ctx context.Context, args json.RawMessage) (string, error) {
		return "2d6 -> [4, 5] = 9", nil
	})
}

func TestServeHandshake(t *testing.T) {
	h := newHarness(t, okTool())
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)

	f := h.recv(t)
	if f.Type != ipc.FrameControl || f.Topic != topicHelloOK {
		t.Fatalf("expected hello.ok control frame, got type=%d topic=%q", f.Type, f.Topic)
	}
	var hok HelloOK
	if err := ipc.Unmarshal(f.Payload, &hok); err != nil {
		t.Fatalf("decode hello.ok: %v", err)
	}
	if hok.Magic != ipc.Magic || hok.ProtocolVer != ipc.ProtocolVer {
		t.Errorf("hello.ok magic/ver = %q/%d, want %q/%d", hok.Magic, hok.ProtocolVer, ipc.Magic, ipc.ProtocolVer)
	}
	if hok.Info.Name != "roll" || hok.Info.Kind != KindTool {
		t.Errorf("Info = %+v, want name=roll kind=tool", hok.Info)
	}
	if len(hok.Info.Tools) != 1 || hok.Info.Tools[0].Name != "roll" {
		t.Errorf("Info.Tools = %+v, want one tool named roll", hok.Info.Tools)
	}
}

func TestServeInvokeCorrelatedResult(t *testing.T) {
	h := newHarness(t, okTool())
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)
	h.recv(t) // hello.ok

	const reqID = 4242
	payload, _ := ipc.Marshal(Invoke{Tool: "roll", Args: json.RawMessage(`{"notation":"2d6"}`)})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: reqID, Topic: topicInvoke, Payload: payload})

	f := h.recv(t)
	if f.Type != ipc.FrameResult || f.ID != reqID {
		t.Fatalf("expected result frame id=%d, got type=%d id=%d", reqID, f.Type, f.ID)
	}
	var res Result
	_ = ipc.Unmarshal(f.Payload, &res)
	if res.IsError || res.Text != "2d6 -> [4, 5] = 9" {
		t.Errorf("result = %+v, want text and no error", res)
	}
}

func TestServeToolErrorYieldsErrorResult(t *testing.T) {
	ts := oneTool("boom", func(ctx context.Context, args json.RawMessage) (string, error) {
		return "", errors.New("kaboom")
	})
	h := newHarness(t, ts)
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)
	h.recv(t)

	payload, _ := ipc.Marshal(Invoke{Tool: "boom", Args: json.RawMessage(`{}`)})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 1, Topic: topicInvoke, Payload: payload})

	var res Result
	_ = ipc.Unmarshal(h.recv(t).Payload, &res)
	if !res.IsError || res.Text != "kaboom" {
		t.Errorf("result = %+v, want IsError with text kaboom", res)
	}
}

func TestServePanicRecoveredAndLoopSurvives(t *testing.T) {
	ts := oneTool("panicky", func(ctx context.Context, args json.RawMessage) (string, error) {
		panic("oops")
	})
	h := newHarness(t, ts)
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)
	h.recv(t)

	// A panicking invoke yields an error result, not a crash.
	payload, _ := ipc.Marshal(Invoke{Tool: "panicky", Args: json.RawMessage(`{}`)})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 1, Topic: topicInvoke, Payload: payload})
	var res Result
	_ = ipc.Unmarshal(h.recv(t).Payload, &res)
	if !res.IsError {
		t.Fatalf("panic should yield an error result, got %+v", res)
	}

	// The loop must still be alive: a following heartbeat is answered.
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, ID: 2, Topic: topicHeartbeat})
	hb := h.recv(t)
	if hb.Type != ipc.FrameControl || hb.Topic != topicHeartbeat || hb.ID != 2 {
		t.Errorf("after panic, heartbeat reply = type=%d topic=%q id=%d, want control/heartbeat/2", hb.Type, hb.Topic, hb.ID)
	}
}

func TestServeUnknownTopicYieldsErrorResult(t *testing.T) {
	h := newHarness(t, okTool())
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)
	h.recv(t)

	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 9, Topic: "tool.bogus", Payload: []byte(`{}`)})
	f := h.recv(t)
	if f.Type != ipc.FrameResult || f.ID != 9 {
		t.Fatalf("expected error result id=9, got type=%d id=%d", f.Type, f.ID)
	}
	var res Result
	_ = ipc.Unmarshal(f.Payload, &res)
	if !res.IsError {
		t.Errorf("unknown topic should yield IsError, got %+v", res)
	}
}

func TestServeHeartbeatReply(t *testing.T) {
	h := newHarness(t, okTool())
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)
	h.recv(t)

	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, ID: 77, Topic: topicHeartbeat})
	f := h.recv(t)
	if f.Type != ipc.FrameControl || f.Topic != topicHeartbeat || f.ID != 77 {
		t.Errorf("heartbeat reply = type=%d topic=%q id=%d, want control/heartbeat/77", f.Type, f.Topic, f.ID)
	}
	if len(f.Payload) != 0 {
		t.Errorf("heartbeat reply payload = %q, want empty", f.Payload)
	}
}

func TestServeShutdownReturnsNil(t *testing.T) {
	h := newHarness(t, okTool())
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer)
	h.recv(t)

	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicShutdown})
	if err := <-h.done; err != nil {
		t.Errorf("serve after shutdown = %v, want nil", err)
	}
}

func TestServeVersionMismatchRefused(t *testing.T) {
	h := newHarness(t, okTool())
	h.sendHello(t, ipc.Magic, ipc.ProtocolVer+1) // wrong version

	// The plugin still sends a hello.ok (so the host can log a reason)...
	f := h.recv(t)
	if f.Topic != topicHelloOK {
		t.Fatalf("expected a hello.ok on mismatch, got topic=%q", f.Topic)
	}
	// ...then serve returns a handshake error.
	err := <-h.done
	if !errors.Is(err, ErrHandshake) {
		t.Errorf("serve = %v, want ErrHandshake", err)
	}
}

func TestServeEOFBeforeHelloReturnsNil(t *testing.T) {
	ts := okTool()
	hostR, pluginW := io.Pipe()
	pluginR, hostW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(ts, rwc{r: pluginR, w: pluginW}) }()

	// Close the host's write end without sending hello: the plugin sees EOF.
	_ = hostW.Close()
	if err := <-done; err != nil {
		t.Errorf("serve on pre-hello EOF = %v, want nil", err)
	}
	_ = hostR.Close()
	_ = pluginW.Close()
}

// rwc adapts a reader and a writer into a Transport.
type rwc struct {
	r io.Reader
	w io.Writer
}

func (x rwc) Read(p []byte) (int, error)  { return x.r.Read(p) }
func (x rwc) Write(p []byte) (int, error) { return x.w.Write(p) }

func TestWantsVersionFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"-version", []string{"-version"}, true},
		{"--version", []string{"--version"}, true},
		{"-version among others", []string{"-x", "-version", "-y"}, true},
		{"only -selftest", []string{"-selftest"}, false},
		{"unrelated flags", []string{"-selftest", "-notation", "2d6"}, false},
		{"-version after -- is a tool arg, not the flag", []string{"--", "-version"}, false},
		{"-version before -- still counts", []string{"-version", "--", "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsVersionFlag(tc.args); got != tc.want {
				t.Errorf("wantsVersionFlag(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
