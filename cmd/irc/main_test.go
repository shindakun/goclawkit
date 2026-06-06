package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shindakun/goclawkit/pkg/ipc"
	"github.com/shindakun/goclawkit/pkg/plugin"
)

// TestWireProtocolEndToEnd is the channel analogue of roll's wire test: it execs the
// built binary, handshakes as a channel, points the binary at an in-process fake IRC
// server, has the fake send a mention, reads the resulting channel.inbound event off
// stdout, then drives a channel.send and asserts the reply reached the fake as a
// PRIVMSG. Proves the channel.* protocol works over real OS pipes + a real (fake) IRC
// connection.
func TestWireProtocolEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "irc")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building irc binary: %v", err)
	}

	srv, err := newFakeIRCd()
	if err != nil {
		t.Fatalf("fake ircd: %v", err)
	}
	defer srv.close()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"IRC_SERVER="+srv.addr(),
		"IRC_NICK=goclawbot",
		"IRC_CHANNEL=#goclawtester",
		"IRC_PLAINTEXT=1", // the fake server speaks plain TCP
	)
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
		t.Fatalf("starting irc: %v", err)
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
	if hok.Info.Kind != plugin.KindChannel || hok.Info.Name != "irc" {
		t.Fatalf("hello.ok Info = %+v, want irc/channel", hok.Info)
	}

	// Wait for the bot to JOIN, then have the fake send a mention.
	select {
	case <-srv.joinedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("bot never joined the channel")
	}
	srv.injectChannelMessage("steve", "#goclawtester", "goclawbot: ping over the wire")

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
	if in.SenderID != "steve" || in.ChatID != "#goclawtester" || in.Text != "ping over the wire" {
		t.Errorf("inbound event = %+v, want steve/#goclawtester/'ping over the wire'", in)
	}

	// Send a channel.send; assert SendResult OK and that the fake got the PRIVMSG.
	const id = 1
	sendPayload, _ := ipc.Marshal(plugin.Outbound{Channel: "irc", ChatID: "#goclawtester", Text: "pong over the wire"})
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
	case raw := <-srv.privCh:
		if strings.TrimSpace(raw) != "#goclawtester :pong over the wire" {
			t.Errorf("fake got PRIVMSG %q, want '#goclawtester :pong over the wire'", raw)
		}
	case <-time.After(5 * time.Second):
		t.Error("reply never reached the IRC server")
	}

	// Graceful shutdown.
	_ = sess.Send(ipc.Frame{Type: ipc.FrameControl, Topic: "shutdown"})
}

// rw adapts the child's stdout/stdin pipes into a Transport.
type rw struct {
	r interface{ Read([]byte) (int, error) }
	w interface{ Write([]byte) (int, error) }
}

func (x rw) Read(p []byte) (int, error)  { return x.r.Read(p) }
func (x rw) Write(p []byte) (int, error) { return x.w.Write(p) }
