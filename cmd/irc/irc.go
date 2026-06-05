package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// This is a deliberately minimal IRC client: just the slice of RFC 1459/2812 a
// read-and-respond bot needs (register, ping/pong, join, PRIVMSG in/out). No SASL,
// CTCP, DCC, or IRCv3 capabilities. Stdlib only (crypto/tls, bufio, net). The bot
// DIALS OUT to the server, so there is no inbound listener and no open port.

// ircLineMax is IRC's 512-byte line cap INCLUDING the trailing CRLF. We pace and
// split outbound text to stay under it; this is the text budget after protocol
// overhead is subtracted at the call site.
const ircLineMax = 512

// privMsg is a parsed inbound PRIVMSG.
type privMsg struct {
	Nick   string // sender nick (from the prefix)
	Target string // channel ("#x") or the bot's nick (a direct query)
	Text   string
}

// parsePrivMsg parses one raw IRC line into a privMsg, reporting ok=false for any
// line that is not a PRIVMSG. Format: ":nick!user@host PRIVMSG <target> :<text>".
func parsePrivMsg(line string) (privMsg, bool) {
	if !strings.HasPrefix(line, ":") {
		return privMsg{}, false
	}
	// Split off the source prefix (":nick!user@host") from the rest.
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return privMsg{}, false
	}
	prefix := line[1:sp]
	rest := line[sp+1:]

	// rest = "PRIVMSG <target> :<text>"
	const cmd = "PRIVMSG "
	if !strings.HasPrefix(rest, cmd) {
		return privMsg{}, false
	}
	rest = rest[len(cmd):]
	tsp := strings.IndexByte(rest, ' ')
	if tsp < 0 {
		return privMsg{}, false
	}
	target := rest[:tsp]
	text := rest[tsp+1:]
	text = strings.TrimPrefix(text, ":")

	nick := prefix
	if bang := strings.IndexByte(prefix, '!'); bang >= 0 {
		nick = prefix[:bang]
	}
	return privMsg{Nick: nick, Target: target, Text: text}, true
}

// mentions reports whether text addresses the bot by nick, in the common IRC forms
// "nick: ..." / "nick, ..." / a standalone "nick" token. Case-insensitive.
func mentions(text, nick string) bool {
	lt := strings.ToLower(text)
	ln := strings.ToLower(nick)
	if !strings.Contains(lt, ln) {
		return false
	}
	// Require the nick as a word, not a substring of a longer word.
	for _, f := range strings.FieldsFunc(lt, func(r rune) bool {
		return r == ' ' || r == ':' || r == ',' || r == '.' || r == '!' || r == '?'
	}) {
		if f == ln {
			return true
		}
	}
	return false
}

// stripMention removes a leading "nick:" / "nick," address from text so the agent
// sees the user's actual words.
func stripMention(text, nick string) string {
	t := strings.TrimSpace(text)
	low := strings.ToLower(t)
	ln := strings.ToLower(nick)
	if strings.HasPrefix(low, ln) {
		rest := t[len(nick):]
		rest = strings.TrimLeft(rest, ":, ")
		if rest != "" {
			return rest
		}
	}
	return t
}

// dialer opens a connection to the IRC server. Real use is TLS; tests inject a
// plain-net dialer pointed at an in-process fake server.
type dialer func(ctx context.Context, addr string) (net.Conn, error)

// tlsDialer dials addr ("host:port") over TLS using the system roots.
func tlsDialer(ctx context.Context, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("irc: bad server address %q: %w", addr, err)
	}
	d := &tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	return d.DialContext(ctx, "tcp", addr)
}

// client is a connected IRC session: a conn plus a line reader/writer.
type client struct {
	conn net.Conn
	br   *bufio.Reader
}

// connectAndRegister dials, registers (NICK/USER), waits for the welcome (001), and
// joins channel. It returns a ready client or an error.
func connectAndRegister(ctx context.Context, dial dialer, addr, nick, channel string) (*client, error) {
	conn, err := dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("irc: dial %s: %w", addr, err)
	}
	c := &client{conn: conn, br: bufio.NewReader(conn)}

	if err := c.send("NICK " + nick); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := c.send(fmt.Sprintf("USER %s 0 * :%s", nick, nick)); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// Read until the welcome numeric (001), answering PINGs that arrive during
	// registration. Bound the wait so a dead server doesn't hang Start forever.
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		line, err := c.readLine()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("irc: registration read: %w", err)
		}
		if ping, ok := pingToken(line); ok {
			if err := c.send("PONG :" + ping); err != nil {
				_ = conn.Close()
				return nil, err
			}
			continue
		}
		if isWelcome(line) {
			break
		}
	}
	_ = conn.SetReadDeadline(time.Time{}) // clear the deadline for the steady state

	if err := c.send("JOIN " + channel); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// isWelcome reports whether line is the RPL_WELCOME (001) numeric.
func isWelcome(line string) bool {
	// ":server 001 nick :Welcome..."
	parts := strings.SplitN(line, " ", 3)
	return len(parts) >= 2 && parts[1] == "001"
}

// pingToken returns the token of a server PING ("PING :token"), if line is one.
func pingToken(line string) (string, bool) {
	if strings.HasPrefix(line, "PING ") {
		return strings.TrimPrefix(strings.TrimPrefix(line, "PING "), ":"), true
	}
	return "", false
}

// readLine reads one CRLF-terminated line, trimming the terminator.
func (c *client) readLine() (string, error) {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// send writes one line, appending CRLF.
func (c *client) send(line string) error {
	_, err := io.WriteString(c.conn, line+"\r\n")
	return err
}

// privmsg sends text to target as one or more PRIVMSGs. IRC has no multi-line message:
// a literal newline in a PRIVMSG terminates the line, so the server (or client) drops
// everything after the first '\n'. So split on newlines FIRST, one PRIVMSG per line
// (this is what makes a multi-line reply like /commands render correctly), then
// length-split each line to stay under the IRC 512-byte limit. Empty lines are skipped
// (an empty PRIVMSG is rejected by the server).
func (c *client) privmsg(target, text string) error {
	// Budget = 512 - len("PRIVMSG ") - len(target) - len(" :") - len("\r\n").
	overhead := len("PRIVMSG ") + len(target) + len(" :") + 2
	budget := ircLineMax - overhead
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r") // tolerate CRLF-style input
		if line == "" {
			continue // IRC rejects an empty PRIVMSG; a blank line carries nothing
		}
		for _, chunk := range splitForLine(line, budget) {
			if err := c.send("PRIVMSG " + target + " :" + chunk); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitForLine breaks s into chunks of at most max bytes on rune boundaries, so a
// long message is sent as several IRC lines instead of being truncated by the
// server. A non-positive max or a short string yields one chunk.
func splitForLine(s string, max int) []string {
	if max <= 0 || len(s) <= max {
		return []string{s}
	}
	var chunks []string
	var cur strings.Builder
	for _, r := range s { // ranging a string yields runes, never splitting one
		rb := len(string(r))
		if cur.Len()+rb > max {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// close sends QUIT and closes the connection (best-effort).
func (c *client) close(reason string) {
	_ = c.send("QUIT :" + reason)
	_ = c.conn.Close()
}
