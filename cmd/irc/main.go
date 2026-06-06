// Command irc is the worked goclaw CHANNEL plugin demo: a minimal IRC bridge. The
// goclaw host launches this binary and speaks the framed stdio protocol to it;
// main() wires the channel and calls plugin.ServeChannel.
//
// It is a channel in the OUTBOUND-ONLY sense the design settled on: the bot DIALS
// OUT to an IRC server over TLS (no inbound listener, no open port), then reads and
// writes over that single connection. It joins a channel, forwards messages that
// MENTION the bot or are sent to it directly (queries) up to the agent as Inbound,
// and posts the agent's replies back as Outbound. General channel chatter is ignored.
//
// Stdlib only (crypto/tls, bufio, net); no external IRC library. Env:
//
//	IRC_SERVER      host:port (default irc.libera.chat:6697, TLS)
//	IRC_NICK        the bot's nick (default goclawbot)
//	IRC_CHANNEL     channel to join (default #goclawtester)
//	IRC_OWNER_NICK  optional, informational; goclaw's host access gate is the real
//	                authority over who may talk to the agent
//
// Run it standalone (the godoorkit -local parallel):
//
//	go build -o irc ./cmd/irc
//	./irc -selftest
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shindakun/goclawkit/pkg/plugin"
)

const (
	version     = "1.0.0"
	channelName = "irc"

	defaultServer  = "irc.libera.chat:6697"
	defaultNick    = "goclawbot"
	defaultChannel = "#goclawtester"
)

// ircChannel implements plugin.Channel over a dialed IRC connection.
type ircChannel struct {
	server  string
	nick    string
	channel string

	dial dialer // injected; tlsDialer in production, a fake-server dialer in tests

	// now is injected so tests can pin timestamps; defaults to time.Now.
	now func() time.Time

	mu  sync.Mutex // guards cur (Send and the read loop touch it concurrently)
	cur *client    // the live connection, swapped on reconnect
}

func newIRCChannel(server, nick, channel string) *ircChannel {
	return &ircChannel{
		server:  server,
		nick:    nick,
		channel: channel,
		dial:    tlsDialer,
		now:     time.Now,
	}
}

func (c *ircChannel) Info() plugin.Info {
	return plugin.Info{Name: channelName, Version: version}
}

// Start dials the server (retrying with backoff), registers, joins, and streams
// Inbound until ctx is cancelled. It owns reconnect: a dropped connection is retried
// with capped exponential backoff, re-registering and re-joining each time.
func (c *ircChannel) Start(ctx context.Context) (<-chan plugin.Inbound, error) {
	out := make(chan plugin.Inbound)
	go func() {
		defer close(out)
		backoff := time.Second
		for ctx.Err() == nil {
			cl, err := connectAndRegister(ctx, c.dial, c.server, c.nick, c.channel)
			if err != nil {
				plugin.Logf(channelName, "connect failed: %v (retry in %s)", err, backoff)
				if !sleepCtx(ctx, backoff) {
					return
				}
				backoff = capBackoff(backoff * 2)
				continue
			}
			plugin.Logf(channelName, "connected to %s as %s, joined %s", c.server, c.nick, c.channel)
			c.setClient(cl)
			backoff = time.Second // reset after a successful connect
			c.readLoop(ctx, cl, out)
			c.setClient(nil)
			// readLoop returned: connection dropped or ctx cancelled. Loop reconnects
			// unless ctx is done.
		}
	}()
	return out, nil
}

// readLoop reads lines until the connection drops or ctx is cancelled, answering
// PINGs and forwarding mentions/queries as Inbound.
func (c *ircChannel) readLoop(ctx context.Context, cl *client, out chan<- plugin.Inbound) {
	for {
		if ctx.Err() != nil {
			cl.close("shutting down")
			return
		}
		line, err := cl.readLine()
		if err != nil {
			return // connection dropped; Start will reconnect
		}
		if ping, ok := pingToken(line); ok {
			_ = cl.send("PONG :" + ping)
			continue
		}
		pm, ok := parsePrivMsg(line)
		if !ok {
			continue
		}
		in, forward := c.toInbound(pm)
		if !forward {
			continue
		}
		select {
		case out <- in:
		case <-ctx.Done():
			cl.close("shutting down")
			return
		}
	}
}

// toInbound decides whether a PRIVMSG should reach the agent and maps it to an
// Inbound. It forwards a direct query (target is the bot's nick) or a channel
// message that mentions the bot; it ignores everything else (and the bot's own
// lines). For a channel message the ChatID is the channel (so replies go there);
// for a query the ChatID is the sender's nick (so replies go back to them).
func (c *ircChannel) toInbound(pm privMsg) (plugin.Inbound, bool) {
	if strings.EqualFold(pm.Nick, c.nick) {
		return plugin.Inbound{}, false // ignore our own messages
	}

	isQuery := strings.EqualFold(pm.Target, c.nick)
	var chatID, text string
	switch {
	case isQuery:
		chatID = pm.Nick // reply back to the sender via a query
		text = pm.Text
	case mentions(pm.Text, c.nick):
		chatID = pm.Target // reply in the channel
		text = stripMention(pm.Text, c.nick)
	default:
		return plugin.Inbound{}, false // channel chatter that does not mention us
	}

	return plugin.Inbound{
		Channel:   channelName,
		ChatID:    chatID,
		SenderID:  pm.Nick, // NOTE: IRC nicks are not authenticated without SASL (spoofable)
		Sender:    pm.Nick,
		Text:      text,
		Timestamp: c.now(),
	}, true
}

// Send posts an Outbound as a PRIVMSG to its ChatID (a channel or a nick). It is
// called concurrently with the read loop; the mutex guards the live client.
func (c *ircChannel) Send(ctx context.Context, out plugin.Outbound) error {
	cl := c.getClient()
	if cl == nil {
		return fmt.Errorf("irc: not connected")
	}
	if out.ChatID == "" {
		return fmt.Errorf("irc: outbound missing chat_id (target)")
	}
	return cl.privmsg(out.ChatID, out.Text)
}

func (c *ircChannel) setClient(cl *client) {
	c.mu.Lock()
	c.cur = cl
	c.mu.Unlock()
}

func (c *ircChannel) getClient() *client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

// sleepCtx sleeps for d unless ctx is cancelled first; it reports false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// capBackoff caps the reconnect backoff at 30s.
func capBackoff(d time.Duration) time.Duration {
	const max = 30 * time.Second
	if d > max {
		return max
	}
	return d
}

func main() {
	selftest := flag.Bool("selftest", false, "run a local connect/join/mention/reply round trip against an in-process fake IRC server, then exit")
	flag.Parse()

	if *selftest {
		if err := runSelftest(); err != nil {
			fmt.Fprintf(os.Stderr, "selftest error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ch := newIRCChannel(
		envOr("IRC_SERVER", defaultServer),
		envOr("IRC_NICK", defaultNick),
		envOr("IRC_CHANNEL", defaultChannel),
	)
	// IRC_PLAINTEXT=1 dials without TLS. This is for the wire test (against a local
	// fake server) and local experimentation ONLY; a real deployment always uses TLS
	// (the default). Documented as test-only in the README.
	if os.Getenv("IRC_PLAINTEXT") == "1" {
		ch.dial = plainDialer
		plugin.Logf(channelName, "WARNING: IRC_PLAINTEXT=1, dialing without TLS (test/dev only)")
	}
	if err := plugin.ServeChannel(ch); err != nil {
		plugin.Logf(channelName, "exited: %v", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// plainDialer dials addr over plain TCP (no TLS). Used by -selftest and tests
// against the in-process fake IRC server.
func plainDialer(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}
