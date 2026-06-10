package plugin

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/shindakun/goclawkit/pkg/ipc"
)

// fakeChannel is a configurable Channel for driving serveChannel. inboundCh is the
// stream Start returns (the test pushes Inbound onto it); sendFn handles Send;
// started/stopped observe the ctx lifecycle.
type fakeChannel struct {
	name       string
	inboundCh  chan Inbound
	sendFn     func(ctx context.Context, out Outbound) error
	startErr   error
	startedCtx context.Context
}

func (f *fakeChannel) Info() Info { return Info{Name: f.name} }

func (f *fakeChannel) Start(ctx context.Context) (<-chan Inbound, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.startedCtx = ctx
	return f.inboundCh, nil
}

func (f *fakeChannel) Send(ctx context.Context, out Outbound) error {
	if f.sendFn != nil {
		return f.sendFn(ctx, out)
	}
	return nil
}

// actionChannel adds ActionSender to fakeChannel.
type actionChannel struct {
	*fakeChannel
	actionFn func(ctx context.Context, chatID, kind string) error
}

func (a *actionChannel) SendAction(ctx context.Context, chatID, kind string) error {
	return a.actionFn(ctx, chatID, kind)
}

// chanHarness drives serveChannel over an in-memory pipe pair.
type chanHarness struct {
	host *ipc.Session
	done chan error
	ch   *fakeChannel
}

func newChanHarness(t *testing.T, ch Channel, fc *fakeChannel) *chanHarness {
	t.Helper()
	hostR, pluginW := io.Pipe()
	pluginR, hostW := io.Pipe()
	host := ipc.NewSession(rwc{r: hostR, w: hostW})

	done := make(chan error, 1)
	go func() { done <- serveChannel(ch, rwc{r: pluginR, w: pluginW}) }()

	t.Cleanup(func() {
		_ = pluginW.Close()
		_ = hostW.Close()
	})
	return &chanHarness{host: host, done: done, ch: fc}
}

func (h *chanHarness) handshake(t *testing.T) HelloOK {
	t.Helper()
	payload, _ := ipc.Marshal(Hello{Magic: ipc.Magic, ProtocolVer: ipc.ProtocolVer})
	if err := h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicHello, Payload: payload}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	f := h.recv(t)
	if f.Topic != topicHelloOK {
		t.Fatalf("expected hello.ok, got topic=%q", f.Topic)
	}
	var hok HelloOK
	if err := ipc.Unmarshal(f.Payload, &hok); err != nil {
		t.Fatalf("decode hello.ok: %v", err)
	}
	return hok
}

func (h *chanHarness) recv(t *testing.T) ipc.Frame {
	t.Helper()
	f, err := h.host.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return f
}

func newFakeChannel(name string) *fakeChannel {
	return &fakeChannel{name: name, inboundCh: make(chan Inbound, 4)}
}

func TestServeChannelHandshakeAnnouncesKindChannel(t *testing.T) {
	fc := newFakeChannel("fakechan")
	h := newChanHarness(t, fc, fc)
	hok := h.handshake(t)
	if hok.Info.Kind != KindChannel {
		t.Errorf("Info.Kind = %q, want %q", hok.Info.Kind, KindChannel)
	}
	if hok.Info.Name != "fakechan" || hok.Info.ProtocolVer != ipc.ProtocolVer {
		t.Errorf("Info = %+v, want name=fakechan protocol_ver=%d", hok.Info, ipc.ProtocolVer)
	}
}

func TestServeChannelInboundBecomesEvent(t *testing.T) {
	fc := newFakeChannel("fakechan")
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	fc.inboundCh <- Inbound{Channel: "fakechan", ChatID: "7", Sender: "alice", Text: "hi"}

	f := h.recv(t)
	if f.Type != ipc.FrameEvent || f.Topic != topicInbound {
		t.Fatalf("expected channel.inbound event, got type=%d topic=%q", f.Type, f.Topic)
	}
	if f.ID != 0 {
		t.Errorf("inbound event ID = %d, want 0 (unsolicited)", f.ID)
	}
	var in Inbound
	if err := ipc.Unmarshal(f.Payload, &in); err != nil {
		t.Fatalf("decode inbound: %v", err)
	}
	if in.ChatID != "7" || in.Text != "hi" || in.Sender != "alice" {
		t.Errorf("inbound = %+v, want chat 7 / alice / hi", in)
	}
}

func TestServeChannelSendReachesChannel(t *testing.T) {
	got := make(chan Outbound, 1)
	fc := newFakeChannel("fakechan")
	fc.sendFn = func(ctx context.Context, out Outbound) error {
		got <- out
		return nil
	}
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	const id = 55
	payload, _ := ipc.Marshal(Outbound{Channel: "fakechan", ChatID: "7", Text: "reply"})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: id, Topic: topicSend, Payload: payload})

	f := h.recv(t)
	if f.Type != ipc.FrameResult || f.ID != id {
		t.Fatalf("expected result id=%d, got type=%d id=%d", id, f.Type, f.ID)
	}
	var res SendResult
	_ = ipc.Unmarshal(f.Payload, &res)
	if !res.OK || res.Error != "" {
		t.Errorf("SendResult = %+v, want OK", res)
	}
	select {
	case out := <-got:
		if out.ChatID != "7" || out.Text != "reply" {
			t.Errorf("Send got %+v, want chat 7 / reply", out)
		}
	default:
		t.Error("Send was not called")
	}
}

func TestServeChannelSendErrorYieldsErrorResult(t *testing.T) {
	fc := newFakeChannel("fakechan")
	fc.sendFn = func(ctx context.Context, out Outbound) error {
		return errors.New("post failed")
	}
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	payload, _ := ipc.Marshal(Outbound{ChatID: "7", Text: "x"})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 1, Topic: topicSend, Payload: payload})

	var res SendResult
	_ = ipc.Unmarshal(h.recv(t).Payload, &res)
	if res.OK || res.Error != "post failed" {
		t.Errorf("SendResult = %+v, want OK=false Error=post failed", res)
	}
}

func TestServeChannelSendPanicRecovered(t *testing.T) {
	fc := newFakeChannel("fakechan")
	fc.sendFn = func(ctx context.Context, out Outbound) error { panic("boom") }
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	payload, _ := ipc.Marshal(Outbound{ChatID: "7", Text: "x"})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 1, Topic: topicSend, Payload: payload})
	var res SendResult
	_ = ipc.Unmarshal(h.recv(t).Payload, &res)
	if res.OK {
		t.Fatalf("panic should yield an error result, got %+v", res)
	}
	// Loop survives: a heartbeat is still answered.
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, ID: 2, Topic: topicHeartbeat})
	hb := h.recv(t)
	if hb.Topic != topicHeartbeat || hb.ID != 2 {
		t.Errorf("after panic, heartbeat reply = topic=%q id=%d", hb.Topic, hb.ID)
	}
}

func TestServeChannelActionWithSender(t *testing.T) {
	gotKind := make(chan string, 1)
	fc := newFakeChannel("fakechan")
	ac := &actionChannel{fakeChannel: fc, actionFn: func(ctx context.Context, chatID, kind string) error {
		gotKind <- kind
		return nil
	}}
	h := newChanHarness(t, ac, fc)
	h.handshake(t)

	payload, _ := ipc.Marshal(Action{ChatID: "7", Kind: "typing"})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 3, Topic: topicAction, Payload: payload})
	var res SendResult
	_ = ipc.Unmarshal(h.recv(t).Payload, &res)
	if !res.OK {
		t.Errorf("action SendResult = %+v, want OK", res)
	}
	if k := <-gotKind; k != "typing" {
		t.Errorf("SendAction kind = %q, want typing", k)
	}
}

func TestServeChannelActionWithoutSenderIsNoOpSuccess(t *testing.T) {
	fc := newFakeChannel("fakechan") // plain fakeChannel: no ActionSender
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	payload, _ := ipc.Marshal(Action{ChatID: "7", Kind: "typing"})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 4, Topic: topicAction, Payload: payload})
	var res SendResult
	_ = ipc.Unmarshal(h.recv(t).Payload, &res)
	if !res.OK || res.Error != "" {
		t.Errorf("action without ActionSender = %+v, want no-op OK", res)
	}
}

func TestServeChannelShutdownCancelsStartAndReturnsNil(t *testing.T) {
	fc := newFakeChannel("fakechan")
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicShutdown})
	if err := <-h.done; err != nil {
		t.Errorf("serveChannel after shutdown = %v, want nil", err)
	}
	// Start's ctx must have been cancelled.
	select {
	case <-fc.startedCtx.Done():
	default:
		t.Error("Start's ctx was not cancelled on shutdown")
	}
}

func TestServeChannelHeartbeatReply(t *testing.T) {
	fc := newFakeChannel("fakechan")
	h := newChanHarness(t, fc, fc)
	h.handshake(t)

	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, ID: 88, Topic: topicHeartbeat})
	f := h.recv(t)
	if f.Type != ipc.FrameControl || f.Topic != topicHeartbeat || f.ID != 88 {
		t.Errorf("heartbeat reply = type=%d topic=%q id=%d", f.Type, f.Topic, f.ID)
	}
}

func TestServeChannelStartErrorReturns(t *testing.T) {
	fc := newFakeChannel("fakechan")
	fc.startErr = errors.New("cannot bind")
	h := newChanHarness(t, fc, fc)
	// Handshake succeeds, then Start fails and serveChannel returns the error.
	payload, _ := ipc.Marshal(Hello{Magic: ipc.Magic, ProtocolVer: ipc.ProtocolVer})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicHello, Payload: payload})
	h.recv(t) // hello.ok still sent
	if err := <-h.done; err == nil {
		t.Error("serveChannel with Start error = nil, want error")
	}
}
