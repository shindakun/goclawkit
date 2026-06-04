package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/shindakun/goclawkit/pkg/ipc"
)

// Channel feature topics. Reserved and documented in the spec; handled here.
const (
	topicInbound = "channel.inbound" // FrameEvent, plugin -> host (no reply)
	topicSend    = "channel.send"    // FrameRequest, host -> plugin
	topicAction  = "channel.action"  // FrameRequest, host -> plugin
)

// SendResult is the reply payload for channel.send and channel.action: OK on
// success, Error set on failure.
type SendResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Action is the channel.action request payload: show a transient action (e.g.
// "typing") in a chat.
type Action struct {
	ChatID string `json:"chat_id"`
	Kind   string `json:"kind"`
}

// ServeChannel runs the channel protocol over the plugin's stdin/stdout until the
// host sends shutdown or stdin closes. It is to a channel what Serve is to a tool:
// it owns the protocol so the author writes only Start/Send. The handshake announces
// Kind=channel; then an inbound pump and a request pump share the one Session.
//
// Like Serve, if stdin is an interactive terminal the stdio-backed ServeChannel
// prints a one-line hint to stderr first (so the handshake wait does not look like a
// hang); a non-TTY stdin stays silent.
func ServeChannel(ch Channel) error {
	if stdinIsTTY() {
		name := ch.Info().Name
		if name == "" {
			name = "plugin"
		}
		fmt.Fprintf(os.Stderr, "%s: goclaw channel plugin, waiting for the host handshake on stdin.\n", name)
		fmt.Fprintln(os.Stderr, "  Not meant to be run directly; the goclaw host launches it. Ctrl-D or Ctrl-C to exit.")
	}
	return serveChannel(ch, ipc.StdioTransport{})
}

// serveChannel is the testable core: an explicit Transport instead of stdio.
func serveChannel(ch Channel, t ipc.Transport) error {
	sess := ipc.NewSession(t)

	// 1. Handshake (Kind=channel).
	first, err := sess.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil // host went away before saying hello
		}
		return err
	}
	info := ch.Info()
	info.Kind = KindChannel
	info.ProtocolVer = ipc.ProtocolVer
	if err := handshake(sess, info, first); err != nil {
		return err
	}

	// 2. Start the channel. ctx is cancelled on shutdown/EOF so Start unwinds.
	ctx, cancel := context.WithCancel(context.Background())
	inbound, err := ch.Start(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("channel start: %w", err)
	}

	// 3a. Inbound pump: each Inbound -> a channel.inbound event (unsolicited, ID 0,
	//     no reply). Writes go through the Session mutex, so they never interleave
	//     with the request pump's result writes.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return // shutdown/EOF: unwind even if a slow Channel has not closed its chan yet
			case in, ok := <-inbound:
				if !ok {
					return // the channel closed its inbound stream
				}
				payload, err := ipc.Marshal(in)
				if err != nil {
					Logf(info.Name, "drop inbound: marshal: %v", err)
					continue
				}
				if err := sess.Send(ipc.Frame{Type: ipc.FrameEvent, Topic: topicInbound, Payload: payload}); err != nil {
					return // transport gone; the request pump will also see EOF and exit
				}
			}
		}
	}()

	// 3b. Request pump: the read loop. On return, cancel ctx FIRST (stops Start and
	//     unblocks the inbound pump), THEN wait for the pump to finish. Order matters:
	//     waiting before cancelling would deadlock against a pump blocked on its
	//     select.
	defer func() {
		cancel()
		wg.Wait()
	}()
	for {
		f, err := sess.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // host died / stdin closed: clean exit (ctx cancel via defer)
			}
			return err
		}
		switch {
		case f.Type == ipc.FrameControl && f.Topic == topicShutdown:
			return nil
		case f.Type == ipc.FrameControl && f.Topic == topicHeartbeat:
			_ = sess.Send(ipc.Frame{Type: ipc.FrameControl, ID: f.ID, Topic: topicHeartbeat})
		case f.Type == ipc.FrameRequest && f.Topic == topicSend:
			go handleSend(ctx, sess, ch, f)
		case f.Type == ipc.FrameRequest && f.Topic == topicAction:
			go handleAction(ctx, sess, ch, f)
		case f.Type == ipc.FrameRequest:
			// Unknown request topic: correlated error, never crash.
			sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: false, Error: "unknown topic: " + f.Topic})
		default:
			// Unknown Event/Control: ignore.
		}
	}
}

// handleSend runs one channel.send and writes its correlated result. A Send error or
// panic both become a SendResult with Error set; the loop never crashes.
func handleSend(ctx context.Context, sess *ipc.Session, ch Channel, f ipc.Frame) {
	var out Outbound
	if err := ipc.Unmarshal(f.Payload, &out); err != nil {
		sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: false, Error: "bad send payload: " + err.Error()})
		return
	}
	if err := sendSafely(ctx, ch, out); err != nil {
		sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: false, Error: err.Error()})
		return
	}
	sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: true})
}

// handleAction runs one channel.action. If the channel implements ActionSender it is
// called; otherwise the action is a no-op success (an unknown action is not an
// error, mirroring goclaw's SendAction).
func handleAction(ctx context.Context, sess *ipc.Session, ch Channel, f ipc.Frame) {
	var a Action
	if err := ipc.Unmarshal(f.Payload, &a); err != nil {
		sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: false, Error: "bad action payload: " + err.Error()})
		return
	}
	as, ok := ch.(ActionSender)
	if !ok {
		sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: true}) // no-op success
		return
	}
	if err := actionSafely(ctx, as, a); err != nil {
		sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: false, Error: err.Error()})
		return
	}
	sendChannelResult(sess, f.ID, f.Topic, SendResult{OK: true})
}

// sendSafely calls ch.Send, recovering a panic into an error so a buggy channel
// cannot take down the dispatch loop.
func sendSafely(ctx context.Context, ch Channel, out Outbound) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("channel Send panicked: %v", r)
		}
	}()
	return ch.Send(ctx, out)
}

func actionSafely(ctx context.Context, as ActionSender, a Action) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("channel SendAction panicked: %v", r)
		}
	}()
	return as.SendAction(ctx, a.ChatID, a.Kind)
}

func sendChannelResult(sess *ipc.Session, id uint64, topic string, res SendResult) {
	payload, _ := ipc.Marshal(res)
	_ = sess.Send(ipc.Frame{Type: ipc.FrameResult, ID: id, Topic: topic, Payload: payload})
}
