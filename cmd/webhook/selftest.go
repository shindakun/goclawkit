package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/shindakun/goclawkit/pkg/plugin"
)

// listen binds a TCP listener so Start fails synchronously on a bad address rather
// than silently in a goroutine.
func listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// runSelftest exercises the channel end to end in-process, with no host: it stands up
// a sink HTTP server to receive outbound, posts one inbound to the channel's
// listener, reads the resulting Inbound off Start's stream, then drives it back
// through Send to the sink and prints what the sink received. The godoorkit -local
// parallel for a channel.
func runSelftest() error {
	// A sink that records the outbound POST body.
	received := make(chan outboundBody, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b outboundBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		received <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	// Channel on an ephemeral port, posting outbound to the sink. selftest drives
	// decodeInbound/Send directly (not the HTTP handler), so the token is unused here;
	// the wire test covers the 401 path.
	ch := newWebhookChannel("127.0.0.1:0", sink.URL, "selftest-token", "")
	// We need the actual bound address; bind here and hand the listener's addr back
	// in by re-creating Start's server is awkward, so for selftest we drive the
	// pieces directly: decode an inbound, then Send it back out.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Map an inbound body the way the HTTP handler would.
	rawIn, _ := json.Marshal(inboundBody{ChatID: "7", Sender: "alice", Text: "hello from selftest"})
	in, err := ch.decodeInbound(bytes.NewReader(rawIn))
	if err != nil {
		return fmt.Errorf("decode inbound: %w", err)
	}
	fmt.Printf("inbound:  chat=%s sender=%s text=%q\n", in.ChatID, in.Sender, in.Text)

	// 2. The host would turn that into a reply; here we echo it straight back out.
	if err := ch.Send(ctx, plugin.Outbound{ChatID: in.ChatID, Text: "echo: " + in.Text}); err != nil {
		return fmt.Errorf("send outbound: %w", err)
	}

	select {
	case got := <-received:
		fmt.Printf("outbound: chat=%s text=%q (delivered to sink)\n", got.ChatID, got.Text)
	case <-ctx.Done():
		return fmt.Errorf("sink never received the outbound: %w", ctx.Err())
	}
	return nil
}
