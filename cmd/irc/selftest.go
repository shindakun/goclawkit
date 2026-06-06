package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shindakun/goclawkit/pkg/plugin"
)

// runSelftest exercises the bridge end to end in-process against a fake IRC server,
// with no network: it connects/registers/joins, the fake sends a mention, the
// resulting Inbound is read off Start's stream, then a reply is driven back through
// Send and the fake's receipt of the PRIVMSG is confirmed. The godoorkit -local
// parallel for a channel.
func runSelftest() error {
	srv, err := newFakeIRCd()
	if err != nil {
		return fmt.Errorf("start fake ircd: %w", err)
	}
	defer srv.close()

	ch := newIRCChannel(srv.addr(), "goclawbot", "#goclawtester")
	ch.dial = plainDialer // the fake server speaks plain TCP, not TLS

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	inbound, err := ch.Start(ctx)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Wait for the JOIN so the bot is in-channel before the fake speaks.
	select {
	case joined := <-srv.joinedCh:
		fmt.Printf("joined:   %s\n", joined)
	case <-ctx.Done():
		return fmt.Errorf("never joined: %w", ctx.Err())
	}

	// The fake sends a mention; it should surface as an Inbound.
	srv.injectChannelMessage("steve", "#goclawtester", "goclawbot: ping?")
	var in plugin.Inbound
	select {
	case in = <-inbound:
		fmt.Printf("inbound:  from=%s chat=%s text=%q\n", in.SenderID, in.ChatID, in.Text)
	case <-ctx.Done():
		return fmt.Errorf("no inbound from the mention: %w", ctx.Err())
	}

	// The host would have the agent reply; here we echo straight back out.
	if err := ch.Send(ctx, plugin.Outbound{Channel: channelName, ChatID: in.ChatID, Text: "pong!"}); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	select {
	case raw := <-srv.privCh:
		// raw is "<target> :<text>"
		fmt.Printf("outbound: PRIVMSG %s\n", strings.TrimSpace(raw))
	case <-ctx.Done():
		return fmt.Errorf("reply never reached the server: %w", ctx.Err())
	}
	return nil
}
