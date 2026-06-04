package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/shindakun/goclawkit/pkg/ipc"
)

// Control and feature topics this runtime handles. Reserved areas (channel.*,
// host.*) are documented in the spec but not handled here.
const (
	topicHello     = "hello"
	topicHelloOK   = "hello.ok"
	topicShutdown  = "shutdown"
	topicHeartbeat = "heartbeat"
	topicInvoke    = "tool.invoke"
)

// Hello is the host's handshake payload (FrameControl, topic "hello").
type Hello struct {
	Magic       string `json:"magic"`
	ProtocolVer int    `json:"protocol_ver"`
}

// HelloOK is the plugin's handshake reply (FrameControl, topic "hello.ok").
type HelloOK struct {
	Magic       string `json:"magic"`
	ProtocolVer int    `json:"protocol_ver"`
	Info        Info   `json:"info"`
}

// Invoke is the host's tool-call payload (FrameRequest, topic "tool.invoke").
type Invoke struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Result is the plugin's tool-call reply (FrameResult, topic "tool.invoke").
type Result struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

// ErrHandshake is returned by serve when the host's hello is missing or mismatched.
var ErrHandshake = errors.New("goclawkit/plugin: handshake failed")

// Serve runs the plugin protocol over the plugin's stdin/stdout until the host
// sends a shutdown control frame or stdin closes. It is the only function a tool
// plugin's main() needs (or use ServeTool for the one-tool case).
//
// If stdin is an interactive terminal (a human ran the binary directly rather than
// the host launching it), Serve first prints a one-line hint to stderr so the
// blocking-on-handshake wait does not look like a hang. A non-TTY stdin (the host's
// pipe, a file, /dev/null) stays silent, so host launches and scripts are
// unaffected.
func Serve(ts ToolSet) error {
	if stdinIsTTY() {
		name := ts.Name
		if name == "" {
			name = "plugin"
		}
		fmt.Fprintf(os.Stderr, "%s: goclaw plugin, waiting for the host handshake on stdin.\n", name)
		fmt.Fprintln(os.Stderr, "  Not meant to be run directly; the goclaw host launches it. Ctrl-D or Ctrl-C to exit.")
	}
	return serve(ts, ipc.StdioTransport{})
}

// stdinIsTTY reports whether stdin is an interactive terminal, as opposed to a pipe
// or file the host or a redirect would attach. It checks for a character device but
// excludes /dev/null (also a char device, yet not a terminal) so a `</dev/null` run
// stays silent like any other redirect.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

// ServeTool wraps a single Tool into a ToolSet and serves it, so a one-tool
// plugin's main is one line: plugin.ServeTool(myTool{}, "name", "1.0.0").
func ServeTool(t Tool, name, version string) error {
	return Serve(ToolSet{Name: name, Version: version, Tools: []Tool{t}})
}

// serve is the testable core: it takes an explicit Transport instead of stdio.
func serve(ts ToolSet, t ipc.Transport) error {
	sess := ipc.NewSession(t)

	// 1. Handshake: the first frame must be a hello control frame whose magic and
	//    protocol version match ours, or we refuse.
	first, err := sess.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil // host went away before saying hello
		}
		return err
	}
	if err := handshake(sess, buildInfo(ts), first); err != nil {
		return err
	}

	// 2. Dispatch loop. Each tool.invoke runs in its own goroutine; only writes are
	//    serialized (by the Session mutex), so results may complete out of order and
	//    the ID correlates them.
	tools := indexTools(ts)
	for {
		f, err := sess.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // host died / stdin closed: a clean exit
			}
			return err
		}
		switch {
		case f.Type == ipc.FrameControl && f.Topic == topicShutdown:
			return nil
		case f.Type == ipc.FrameControl && f.Topic == topicHeartbeat:
			// Answer with a same-ID heartbeat. Modeled now so host-side liveness
			// checks need no plugin change later; the plugin never originates one.
			_ = sess.Send(ipc.Frame{Type: ipc.FrameControl, ID: f.ID, Topic: topicHeartbeat})
		case f.Type == ipc.FrameRequest && f.Topic == topicInvoke:
			go handleInvoke(sess, tools, f)
		case f.Type == ipc.FrameRequest:
			// Unknown request topic: reply with an error result, never crash. This
			// tolerance is what lets a newer host talk to an older plugin.
			sendResult(sess, f.ID, f.Topic, Result{IsError: true, Text: "unknown topic: " + f.Topic})
		default:
			// Unknown Event/Control: ignore.
		}
	}
}

// handshake validates the host's hello frame and replies with hello.ok carrying
// this plugin's Info. A magic/version mismatch is a clean refusal: the plugin still
// sends a hello.ok marked as a mismatch (so the host can log a reason) and then
// returns ErrHandshake so main exits non-zero. It takes a pre-built Info so both the
// tool runtime (Kind=tool) and the channel runtime (Kind=channel) share it.
func handshake(sess *ipc.Session, info Info, f ipc.Frame) error {
	if f.Type != ipc.FrameControl || f.Topic != topicHello {
		return fmt.Errorf("%w: expected hello control frame, got type=%d topic=%q", ErrHandshake, f.Type, f.Topic)
	}
	var h Hello
	if err := ipc.Unmarshal(f.Payload, &h); err != nil {
		return fmt.Errorf("%w: bad hello payload: %v", ErrHandshake, err)
	}

	if h.Magic != ipc.Magic || h.ProtocolVer != ipc.ProtocolVer {
		// Reply so the host learns who refused and why, then refuse.
		replyHelloOK(sess, f.ID, info)
		return fmt.Errorf("%w: host magic=%q ver=%d, want magic=%q ver=%d",
			ErrHandshake, h.Magic, h.ProtocolVer, ipc.Magic, ipc.ProtocolVer)
	}
	replyHelloOK(sess, f.ID, info)
	return nil
}

// buildInfo assembles the plugin's Info from its ToolSet.
func buildInfo(ts ToolSet) Info {
	info := Info{
		Name:        ts.Name,
		Kind:        KindTool,
		Version:     ts.Version,
		ProtocolVer: ipc.ProtocolVer,
	}
	for _, t := range ts.Tools {
		info.Tools = append(info.Tools, t.Info())
	}
	return info
}

func replyHelloOK(sess *ipc.Session, id uint64, info Info) {
	payload, _ := ipc.Marshal(HelloOK{Magic: ipc.Magic, ProtocolVer: ipc.ProtocolVer, Info: info})
	_ = sess.Send(ipc.Frame{Type: ipc.FrameControl, ID: id, Topic: topicHelloOK, Payload: payload})
}

// indexTools builds a name->Tool lookup for invoke dispatch.
func indexTools(ts ToolSet) map[string]Tool {
	m := make(map[string]Tool, len(ts.Tools))
	for _, t := range ts.Tools {
		m[t.Info().Name] = t
	}
	return m
}

// handleInvoke runs one tool.invoke and writes its correlated result. A tool error
// or a panic both become an IsError result; the loop never crashes.
func handleInvoke(sess *ipc.Session, tools map[string]Tool, f ipc.Frame) {
	var in Invoke
	if err := ipc.Unmarshal(f.Payload, &in); err != nil {
		sendResult(sess, f.ID, f.Topic, Result{IsError: true, Text: "bad invoke payload: " + err.Error()})
		return
	}
	tool, ok := tools[in.Tool]
	if !ok {
		sendResult(sess, f.ID, f.Topic, Result{IsError: true, Text: "unknown tool: " + in.Tool})
		return
	}

	text, err := invokeSafely(tool, in.Args)
	if err != nil {
		sendResult(sess, f.ID, f.Topic, Result{IsError: true, Text: err.Error()})
		return
	}
	sendResult(sess, f.ID, f.Topic, Result{Text: text})
}

// invokeSafely calls a tool's Invoke, recovering a panic into an error so a buggy
// tool cannot take down the plugin's dispatch loop.
func invokeSafely(tool Tool, args json.RawMessage) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()
	return tool.Invoke(context.Background(), args)
}

func sendResult(sess *ipc.Session, id uint64, topic string, res Result) {
	payload, _ := ipc.Marshal(res)
	_ = sess.Send(ipc.Frame{Type: ipc.FrameResult, ID: id, Topic: topic, Payload: payload})
}

// Logf writes a plugin log line to stderr (the host captures it) with a
// [plugin-name] prefix. A plugin must never write logs to stdout: that is the frame
// channel.
func Logf(name, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "["+name+"] "+format+"\n", args...)
}
