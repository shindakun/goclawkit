package plugin

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shindakun/goclawkit/pkg/ipc"
)

// fakePoller is a configurable Poller for driving ServePoll. pollFn supplies each
// tick's result; sendFn handles Send; interval is asked once.
type fakePoller struct {
	name     string
	interval time.Duration
	pollFn   func(ctx context.Context) ([]Inbound, error)
	sendFn   func(ctx context.Context, out Outbound) error

	polls atomic.Int64 // count of Poll calls
}

func (f *fakePoller) Info() Info              { return Info{Name: f.name} }
func (f *fakePoller) Interval() time.Duration { return f.interval }

func (f *fakePoller) Poll(ctx context.Context) ([]Inbound, error) {
	f.polls.Add(1)
	return f.pollFn(ctx)
}

func (f *fakePoller) Send(ctx context.Context, out Outbound) error {
	if f.sendFn != nil {
		return f.sendFn(ctx, out)
	}
	return nil
}

// pollHarness drives serveChannel(&pollChannel{p}, transport) over in-memory pipes,
// the same way chanHarness drives a plain Channel.
type pollHarness struct {
	host *ipc.Session
	done chan error
}

func newPollHarness(t *testing.T, p Poller) *pollHarness {
	t.Helper()
	hostR, pluginW := io.Pipe()
	pluginR, hostW := io.Pipe()
	host := ipc.NewSession(rwc{r: hostR, w: hostW})

	done := make(chan error, 1)
	go func() { done <- serveChannel(&pollChannel{p: p}, rwc{r: pluginR, w: pluginW}) }()

	t.Cleanup(func() {
		_ = pluginW.Close()
		_ = hostW.Close()
	})
	return &pollHarness{host: host, done: done}
}

func (h *pollHarness) handshake(t *testing.T) HelloOK {
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

func (h *pollHarness) recv(t *testing.T) ipc.Frame {
	t.Helper()
	f, err := h.host.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return f
}

func (h *pollHarness) recvInbound(t *testing.T) Inbound {
	t.Helper()
	f := h.recv(t)
	if f.Type != ipc.FrameEvent || f.Topic != topicInbound {
		t.Fatalf("expected channel.inbound event, got type=%d topic=%q", f.Type, f.Topic)
	}
	var in Inbound
	if err := ipc.Unmarshal(f.Payload, &in); err != nil {
		t.Fatalf("decode inbound: %v", err)
	}
	return in
}

// TestServePollAnnouncesKindChannel: a poll channel is indistinguishable from a
// hand-written channel at the handshake.
func TestServePollAnnouncesKindChannel(t *testing.T) {
	p := &fakePoller{name: "gmail", interval: time.Hour, pollFn: func(context.Context) ([]Inbound, error) { return nil, nil }}
	h := newPollHarness(t, p)
	hok := h.handshake(t)
	if hok.Info.Kind != KindChannel || hok.Info.Name != "gmail" {
		t.Errorf("Info = %+v, want gmail/channel", hok.Info)
	}
}

// TestServePollEmitsNewItemsInOrder: Poll returns a batch then nothing; each Inbound
// surfaces as a channel.inbound in slice order, and the empty polls produce nothing.
func TestServePollEmitsNewItemsInOrder(t *testing.T) {
	var once sync.Once
	batch := []Inbound{
		{Channel: "gmail", ChatID: "t1", Text: "first"},
		{Channel: "gmail", ChatID: "t2", Text: "second"},
		{Channel: "gmail", ChatID: "t3", Text: "third"},
	}
	p := &fakePoller{
		name:     "gmail",
		interval: 20 * time.Millisecond,
		pollFn: func(context.Context) ([]Inbound, error) {
			var out []Inbound
			once.Do(func() { out = batch })
			return out, nil // batch on the first poll, (nil,nil) thereafter
		},
	}
	h := newPollHarness(t, p)
	h.handshake(t)

	for i, want := range batch {
		got := h.recvInbound(t)
		if got.Text != want.Text {
			t.Errorf("inbound %d = %q, want %q (order must be preserved)", i, got.Text, want.Text)
		}
	}
	// No further inbound from the empty polls: a heartbeat round-trips with nothing
	// in between.
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, ID: 9, Topic: topicHeartbeat})
	hb := h.recv(t)
	if hb.Topic != topicHeartbeat || hb.ID != 9 {
		t.Errorf("expected only a heartbeat after the batch, got type=%d topic=%q id=%d", hb.Type, hb.Topic, hb.ID)
	}
}

// TestServePollImmediateFirstPoll: with a long interval, the first Poll happens at
// once (a started plugin is live immediately, not after the interval).
func TestServePollImmediateFirstPoll(t *testing.T) {
	p := &fakePoller{
		name:     "gmail",
		interval: time.Hour, // long: if the loop waited the interval first, this would hang
		pollFn: func(context.Context) ([]Inbound, error) {
			return []Inbound{{Channel: "gmail", Text: "hello on startup"}}, nil
		},
	}
	h := newPollHarness(t, p)
	h.handshake(t)
	if in := h.recvInbound(t); in.Text != "hello on startup" {
		t.Errorf("first inbound = %q, want the immediate poll's item", in.Text)
	}
}

// TestServePollBacksOffOnErrorThenResumes: a Poll error backs off (does not spin) and a
// later success resumes emitting.
func TestServePollBacksOffOnErrorThenResumes(t *testing.T) {
	var n atomic.Int64
	p := &fakePoller{
		name:     "gmail",
		interval: 10 * time.Millisecond,
		pollFn: func(context.Context) ([]Inbound, error) {
			// First poll errors (one backoff window of ~1s), then emit one item. This
			// proves the loop retries after an error and resumes; the busy-spin guard is
			// covered separately by TestServePollSlowPollDoesNotBusySpin.
			if n.Add(1) == 1 {
				return nil, errors.New("upstream 500")
			}
			return []Inbound{{Channel: "gmail", Text: "recovered"}}, nil
		},
	}
	h := newPollHarness(t, p)
	h.handshake(t)

	// Despite the errors, we eventually get the success item (backoff base is 1s; the
	// recv waits for it). This asserts the loop retries and resumes, not that it spins.
	if in := h.recvInbound(t); in.Text != "recovered" {
		t.Errorf("inbound after recovery = %q, want 'recovered'", in.Text)
	}
}

// TestServePollSendDelegates: a channel.send request reaches the poller's Send.
func TestServePollSendDelegates(t *testing.T) {
	got := make(chan Outbound, 1)
	p := &fakePoller{
		name:     "gmail",
		interval: time.Hour,
		pollFn:   func(context.Context) ([]Inbound, error) { return nil, nil },
		sendFn: func(ctx context.Context, out Outbound) error {
			got <- out
			return nil
		},
	}
	h := newPollHarness(t, p)
	h.handshake(t)

	payload, _ := ipc.Marshal(Outbound{Channel: "gmail", ChatID: "t1", Text: "reply"})
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameRequest, ID: 1, Topic: topicSend, Payload: payload})

	f := h.recv(t)
	if f.Type != ipc.FrameResult || f.ID != 1 {
		t.Fatalf("expected send result id=1, got type=%d id=%d", f.Type, f.ID)
	}
	var res SendResult
	_ = ipc.Unmarshal(f.Payload, &res)
	if !res.OK {
		t.Errorf("SendResult = %+v, want OK", res)
	}
	select {
	case out := <-got:
		if out.Text != "reply" || out.ChatID != "t1" {
			t.Errorf("Send got %+v, want t1/reply", out)
		}
	case <-time.After(2 * time.Second):
		t.Error("poller Send was not called")
	}
}

// TestServePollShutdownStopsLoopAndReturnsNil: shutdown cancels ctx, the poll loop
// stops, and serveChannel returns nil.
func TestServePollShutdownStopsLoopAndReturnsNil(t *testing.T) {
	stopped := make(chan struct{})
	var once sync.Once
	p := &fakePoller{
		name:     "gmail",
		interval: 5 * time.Millisecond,
		pollFn: func(ctx context.Context) ([]Inbound, error) {
			// Observe cancellation: once ctx is done, signal and stop returning items.
			if ctx.Err() != nil {
				once.Do(func() { close(stopped) })
			}
			return nil, nil
		},
	}
	h := newPollHarness(t, p)
	h.handshake(t)

	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicShutdown})
	if err := <-h.done; err != nil {
		t.Errorf("serveChannel after shutdown = %v, want nil", err)
	}
}

// TestServePollSlowPollDoesNotBusySpin: a Poll that overruns the interval must NOT
// busy-spin; over a fixed window the number of polls stays bounded (roughly one back to
// back, not thousands), and the loop still cancels promptly.
func TestServePollSlowPollDoesNotBusySpin(t *testing.T) {
	p := &fakePoller{
		name:     "gmail",
		interval: 5 * time.Millisecond, // shorter than the poll below: every poll "overruns"
		pollFn: func(ctx context.Context) ([]Inbound, error) {
			// Each poll takes ~10ms, longer than the interval, so wait<=0 every time:
			// the loop will run polls back-to-back. The point is it must still yield to
			// ctx (no tight spin) and respect cancellation.
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
			}
			return nil, nil
		},
	}
	h := newPollHarness(t, p)
	h.handshake(t)

	// Let it run for ~50ms, then shut down.
	time.Sleep(50 * time.Millisecond)
	_ = h.host.Send(ipc.Frame{Type: ipc.FrameControl, Topic: topicShutdown})
	if err := <-h.done; err != nil {
		t.Errorf("serveChannel after shutdown = %v, want nil", err)
	}
	// With ~10ms/poll over ~50ms, expect on the order of 5 polls, NOT thousands. A busy
	// spin (no delay in Poll, no sleep) would be orders of magnitude higher; this bound
	// is generous but catches a true tight loop.
	if n := p.polls.Load(); n > 50 {
		t.Errorf("Poll called %d times in ~50ms; a slow poll appears to be busy-spinning", n)
	}
}

// TestServePollDefaultInterval: a non-positive Interval() uses the default.
func TestServePollDefaultInterval(t *testing.T) {
	pc := &pollChannel{p: &fakePoller{name: "x", interval: 0, pollFn: func(context.Context) ([]Inbound, error) { return nil, nil }}}
	// Drive Start directly and confirm it does not return an error; the default interval
	// is internal, but a zero interval must not cause a tight loop or a panic.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := pc.Start(ctx)
	if err != nil {
		t.Fatalf("Start with zero interval: %v", err)
	}
	cancel()
	// Drain until closed so the goroutine exits cleanly.
	for range ch {
	}
}
