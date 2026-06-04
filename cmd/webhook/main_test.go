package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/shindakun/goclawkit/pkg/ipc"
	"github.com/shindakun/goclawkit/pkg/plugin"
)

func TestDecodeInbound(t *testing.T) {
	c := newWebhookChannel(":0", "", "tok", "")
	c.now = func() time.Time { return time.Unix(1000, 0) }

	body, _ := json.Marshal(inboundBody{ChatID: "7", Sender: "alice", Text: "hi"})
	in, err := c.decodeInbound(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decodeInbound: %v", err)
	}
	// SenderID is namespaced, NOT the verbatim asserted "alice", so a webhook caller
	// can't collide with another channel's owner id. Sender keeps the display name.
	if in.Channel != channelName || in.ChatID != "7" || in.Sender != "alice" || in.SenderID != "webhook:alice" || in.Text != "hi" {
		t.Errorf("Inbound = %+v, want webhook/7/alice with SenderID webhook:alice", in)
	}
	if !in.Timestamp.Equal(time.Unix(1000, 0)) {
		t.Errorf("Timestamp = %v, want injected now", in.Timestamp)
	}

	// Missing chat_id is rejected.
	if _, err := c.decodeInbound(bytes.NewReader([]byte(`{"text":"x"}`))); err == nil {
		t.Error("decodeInbound with no chat_id = nil error, want rejection")
	}
}

func TestDecodeInboundPinnedSenderID(t *testing.T) {
	c := newWebhookChannel(":0", "", "tok", "fixed-owner")
	body, _ := json.Marshal(inboundBody{ChatID: "7", Sender: "alice", Text: "hi"})
	in, _ := c.decodeInbound(bytes.NewReader(body))
	if in.SenderID != "fixed-owner" {
		t.Errorf("SenderID = %q, want pinned fixed-owner", in.SenderID)
	}
}

func TestAuthOK(t *testing.T) {
	c := newWebhookChannel(":0", "", "secret", "")
	mk := func(h, v string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/inbound", nil)
		if h != "" {
			r.Header.Set(h, v)
		}
		return r
	}
	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{"x-webhook-token match", mk("X-Webhook-Token", "secret"), true},
		{"bearer match", mk("Authorization", "Bearer secret"), true},
		{"wrong token", mk("X-Webhook-Token", "nope"), false},
		{"no token header", mk("", ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.authOK(tc.req); got != tc.want {
				t.Errorf("authOK = %v, want %v", got, tc.want)
			}
		})
	}

	// Fail closed: an unconfigured token rejects even a request that carries one.
	noTok := newWebhookChannel(":0", "", "", "")
	if noTok.authOK(mk("X-Webhook-Token", "anything")) {
		t.Error("authOK with no configured token = true, want false (fail closed)")
	}
}

func TestSendPostsToOutboundURL(t *testing.T) {
	got := make(chan outboundBody, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var b outboundBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		got <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	c := newWebhookChannel(":0", sink.URL, "tok", "")
	if err := c.Send(context.Background(), plugin.Outbound{ChatID: "7", Text: "reply"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b := <-got
	if b.ChatID != "7" || b.Text != "reply" {
		t.Errorf("sink got %+v, want chat 7 / reply", b)
	}
}

func TestSendErrorsWithoutOutboundURL(t *testing.T) {
	c := newWebhookChannel(":0", "", "tok", "")
	if err := c.Send(context.Background(), plugin.Outbound{ChatID: "7", Text: "x"}); err == nil {
		t.Error("Send with no OUTBOUND_URL = nil, want error")
	}
}

func TestSendErrorsOnNon2xx(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer sink.Close()
	c := newWebhookChannel(":0", sink.URL, "tok", "")
	if err := c.Send(context.Background(), plugin.Outbound{ChatID: "7", Text: "x"}); err == nil {
		t.Error("Send to a 500 sink = nil, want error")
	}
}

// TestWireProtocolEndToEnd is the channel analogue of roll's wire test: it execs the
// built binary, handshakes as a channel, POSTs an inbound to the plugin's HTTP
// listener and reads the resulting channel.inbound event off stdout, then sends a
// channel.send request and asserts both the SendResult and that the sink received
// the POST. Proves the channel.* protocol works over real OS pipes + HTTP.
func TestWireProtocolEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "webhook")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building webhook binary: %v", err)
	}

	// A sink to receive outbound, and a free port for the plugin's inbound listener.
	sinkGot := make(chan outboundBody, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b outboundBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		sinkGot <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	addr := freeAddr(t)

	cmd := exec.Command(bin)
	const token = "test-secret"
	cmd.Env = append(os.Environ(), "WEBHOOK_ADDR="+addr, "OUTBOUND_URL="+sink.URL, "WEBHOOK_TOKEN="+token)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting webhook: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	sess := ipc.NewSession(rw{r: stdout, w: stdin})

	// Handshake; assert Kind=channel.
	hello, _ := ipc.Marshal(plugin.Hello{Magic: ipc.Magic, ProtocolVer: ipc.ProtocolVer})
	if err := sess.Send(ipc.Frame{Type: ipc.FrameControl, Topic: "hello", Payload: hello}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	helloOK, err := sess.Recv()
	if err != nil {
		t.Fatalf("recv hello.ok: %v", err)
	}
	var hok plugin.HelloOK
	_ = ipc.Unmarshal(helloOK.Payload, &hok)
	if hok.Info.Kind != plugin.KindChannel || hok.Info.Name != "webhook" {
		t.Fatalf("hello.ok Info = %+v, want webhook/channel", hok.Info)
	}

	// A POST with no/wrong token is rejected (401) and injects nothing.
	assertUnauthorized(t, addr, "", inboundBody{ChatID: "7", Sender: "mallory", Text: "inject"})
	assertUnauthorized(t, addr, "wrong", inboundBody{ChatID: "7", Sender: "mallory", Text: "inject"})

	// A POST with the right token is accepted (retry until the listener is bound).
	postInbound(t, addr, token, inboundBody{ChatID: "7", Sender: "alice", Text: "hi"})

	// Read the channel.inbound event the plugin pushes up.
	ev, err := sess.Recv()
	if err != nil {
		t.Fatalf("recv inbound event: %v", err)
	}
	if ev.Type != ipc.FrameEvent || ev.Topic != "channel.inbound" {
		t.Fatalf("expected channel.inbound event, got type=%d topic=%q", ev.Type, ev.Topic)
	}
	var in plugin.Inbound
	_ = ipc.Unmarshal(ev.Payload, &in)
	if in.ChatID != "7" || in.Text != "hi" {
		t.Errorf("inbound event = %+v, want chat 7 / hi", in)
	}

	// Send a channel.send; assert the correlated SendResult and that the sink got it.
	const id = 1
	sendPayload, _ := ipc.Marshal(plugin.Outbound{ChatID: "7", Text: "echo: hi"})
	_ = sess.Send(ipc.Frame{Type: ipc.FrameRequest, ID: id, Topic: "channel.send", Payload: sendPayload})
	res, err := sess.Recv()
	if err != nil {
		t.Fatalf("recv send result: %v", err)
	}
	if res.Type != ipc.FrameResult || res.ID != id {
		t.Fatalf("expected result id=%d, got type=%d id=%d", id, res.Type, res.ID)
	}
	var sr plugin.SendResult
	_ = ipc.Unmarshal(res.Payload, &sr)
	if !sr.OK {
		t.Errorf("SendResult = %+v, want OK", sr)
	}
	select {
	case b := <-sinkGot:
		if b.ChatID != "7" || b.Text != "echo: hi" {
			t.Errorf("sink got %+v, want chat 7 / echo: hi", b)
		}
	case <-time.After(3 * time.Second):
		t.Error("sink never received the outbound POST")
	}

	// Graceful shutdown.
	_ = sess.Send(ipc.Frame{Type: ipc.FrameControl, Topic: "shutdown"})
}

// freeAddr returns a currently-free localhost address (host:port).
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// doPost sends one inbound POST with the given token (empty = no token header) and
// returns the status code, retrying only the transport until the listener is bound.
func doPost(t *testing.T, addr, token string, b inboundBody) int {
	t.Helper()
	body, _ := json.Marshal(b)
	url := fmt.Sprintf("http://%s/inbound", addr)
	deadline := time.Now().Add(3 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Webhook-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("posting inbound to %s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// postInbound POSTs with a valid token and requires 202 Accepted.
func postInbound(t *testing.T, addr, token string, b inboundBody) {
	t.Helper()
	if code := doPost(t, addr, token, b); code != http.StatusAccepted {
		t.Fatalf("authorized inbound POST = %d, want 202", code)
	}
}

// assertUnauthorized POSTs with a bad/absent token and requires 401.
func assertUnauthorized(t *testing.T, addr, token string, b inboundBody) {
	t.Helper()
	if code := doPost(t, addr, token, b); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated inbound POST = %d, want 401", code)
	}
}

// rw adapts the child's stdout/stdin pipes into a Transport.
type rw struct {
	r interface{ Read([]byte) (int, error) }
	w interface{ Write([]byte) (int, error) }
}

func (x rw) Read(p []byte) (int, error)  { return x.r.Read(p) }
func (x rw) Write(p []byte) (int, error) { return x.w.Write(p) }
