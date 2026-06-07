package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shindakun/goclawkit/pkg/plugin"
)

// loadPublicRootPool must IGNORE SSL_CERT_FILE (which a goclaw deployment points at the
// credential-proxy CA): a direct TLS dial to the IRC server must verify against the real
// public roots, not the proxy CA. This is the regression guard for the bug where the
// proxy-env change made the IRC plugin trust only the proxy CA and fail to verify Libera.
func TestLoadPublicRootPool_IgnoresSSLCertFileOverride(t *testing.T) {
	// A self-signed CA standing in for the proxy CA an operator would put in SSL_CERT_FILE.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake-proxy-ca.test"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caFile := filepath.Join(t.TempDir(), "fake-proxy-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	// Sanity: confirm THIS platform's SystemCertPool actually honors SSL_CERT_FILE, i.e. a
	// naive (env-honoring) load WOULD trust the fake CA. Only then is there a regression for
	// loadPublicRootPool to prevent. On macOS the system pool ignores SSL_CERT_FILE entirely
	// (roots come from the Security framework), so there is nothing to catch: skip honestly
	// rather than pretend the assertion has teeth. The Linux container (where the plugin
	// runs, and CI) DOES honor it, so the real guard runs there.
	t.Setenv("SSL_CERT_FILE", caFile)
	naive, err := x509.SystemCertPool()
	if err != nil {
		t.Skipf("no system cert pool on this platform: %v", err)
	}
	if naive != nil {
		if _, verr := caCert.Verify(x509.VerifyOptions{Roots: naive}); verr != nil {
			t.Skip("this platform's SystemCertPool ignores SSL_CERT_FILE (e.g. macOS); the override regression cannot occur here, the Linux guard runs in CI")
		}
	}

	// Here the platform honors the override, so the bug is possible. The fix is proven if the
	// pool from loadPublicRootPool does NOT trust the fake CA (it loaded OS public roots, not
	// the override). A buggy/env-honoring load would trust it and Verify would succeed.
	pool := loadPublicRootPool()
	if pool == nil {
		t.Fatal("loadPublicRootPool returned nil where SystemCertPool succeeded")
	}
	if _, verr := caCert.Verify(x509.VerifyOptions{Roots: pool}); verr == nil {
		t.Fatal("loadPublicRootPool trusted the SSL_CERT_FILE CA; it must ignore the override and use public roots")
	}
}

func TestParsePrivMsg(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		wantOK bool
		nick   string
		target string
		text   string
	}{
		{"channel msg", ":steve!u@h PRIVMSG #goclawtester :hello there", true, "steve", "#goclawtester", "hello there"},
		{"query", ":steve!u@h PRIVMSG goclawbot :psst", true, "steve", "goclawbot", "psst"},
		{"colon in text", ":a!u@h PRIVMSG #c :ratio is 3:1", true, "a", "#c", "ratio is 3:1"},
		{"not privmsg (JOIN)", ":a!u@h JOIN #c", false, "", "", ""},
		{"server notice", ":server NOTICE * :hi", false, "", "", ""},
		{"no prefix", "PING :x", false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pm, ok := parsePrivMsg(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if pm.Nick != tc.nick || pm.Target != tc.target || pm.Text != tc.text {
				t.Errorf("got %+v, want nick=%q target=%q text=%q", pm, tc.nick, tc.target, tc.text)
			}
		})
	}
}

func TestMentions(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"goclawbot: hi", true},
		{"goclawbot hi", true},
		{"hey goclawbot, you there?", true},
		{"GOCLAWBOT: caps", true},
		{"goclawbottle of water", false}, // substring, not a word
		{"just chatting", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := mentions(tc.text, "goclawbot"); got != tc.want {
			t.Errorf("mentions(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestStripMention(t *testing.T) {
	cases := []struct{ in, want string }{
		{"goclawbot: what time is it", "what time is it"},
		{"goclawbot, hello", "hello"},
		{"goclawbot hi", "hi"},
		{"hey goclawbot you there", "hey goclawbot you there"}, // mention not at start: left as-is
	}
	for _, tc := range cases {
		if got := stripMention(tc.in, "goclawbot"); got != tc.want {
			t.Errorf("stripMention(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitForLine(t *testing.T) {
	if got := splitForLine("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("short string should be one chunk, got %v", got)
	}
	chunks := splitForLine(strings.Repeat("a", 25), 10)
	if len(chunks) != 3 {
		t.Fatalf("25/10 should be 3 chunks, got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != strings.Repeat("a", 10) || chunks[2] != strings.Repeat("a", 5) {
		t.Errorf("unexpected chunking: %v", chunks)
	}
	// Multi-byte runes are not split mid-rune.
	mb := splitForLine("héllo wörld", 5)
	for _, c := range mb {
		if len(c) > 5 {
			t.Errorf("chunk %q exceeds byte budget", c)
		}
	}
}

// startTestChannel wires an ircChannel to a fresh fake ircd over plain TCP and waits
// for the JOIN, returning the channel, its inbound stream, the server, and a cancel.
func startTestChannel(t *testing.T, nick string) (*ircChannel, <-chan plugin.Inbound, *fakeIRCd, context.CancelFunc) {
	t.Helper()
	srv, err := newFakeIRCd()
	if err != nil {
		t.Fatalf("fake ircd: %v", err)
	}
	ch := newIRCChannel(srv.addr(), nick, "#goclawtester")
	ch.dial = plainDialer
	ch.now = func() time.Time { return time.Unix(1000, 0) }

	ctx, cancel := context.WithCancel(context.Background())
	inbound, err := ch.Start(ctx)
	if err != nil {
		cancel()
		srv.close()
		t.Fatalf("start: %v", err)
	}
	select {
	case <-srv.joinedCh:
	case <-time.After(3 * time.Second):
		cancel()
		srv.close()
		t.Fatal("channel never joined")
	}
	// The fake signals joinedCh when it RECEIVES the JOIN, which happens inside
	// connectAndRegister, BEFORE Start's goroutine records the live client via
	// setClient. So a JOIN signal does not guarantee getClient() is non-nil yet; wait
	// for the client to be recorded so a following Send does not race with the connect
	// goroutine and get "irc: not connected".
	waitClientReady(t, ch)
	t.Cleanup(func() { cancel(); srv.close() })
	return ch, inbound, srv, cancel
}

// waitClientReady blocks until the channel has recorded its live client (so Send will
// reach the server), failing if it does not happen promptly.
func waitClientReady(t *testing.T, ch *ircChannel) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ch.getClient() != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("client was not ready (setClient never recorded the connection)")
}

func TestChannelForwardsMention(t *testing.T) {
	_, inbound, srv, _ := startTestChannel(t, "goclawbot")
	srv.injectChannelMessage("steve", "#goclawtester", "goclawbot: status?")

	select {
	case in := <-inbound:
		if in.Channel != "irc" || in.SenderID != "steve" || in.ChatID != "#goclawtester" || in.Text != "status?" {
			t.Errorf("inbound = %+v, want irc/steve/#goclawtester/status?", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mention was not forwarded")
	}
}

func TestChannelForwardsQuery(t *testing.T) {
	_, inbound, srv, _ := startTestChannel(t, "goclawbot")
	// A direct query: target is the bot's nick, so the reply ChatID is the sender.
	srv.injectChannelMessage("steve", "goclawbot", "hello bot")

	select {
	case in := <-inbound:
		if in.ChatID != "steve" || in.Text != "hello bot" {
			t.Errorf("query inbound = %+v, want ChatID=steve text='hello bot'", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("query was not forwarded")
	}
}

func TestChannelIgnoresChatterAndSelf(t *testing.T) {
	_, inbound, srv, _ := startTestChannel(t, "goclawbot")
	srv.injectChannelMessage("steve", "#goclawtester", "just chatting, no mention")
	srv.injectChannelMessage("goclawbot", "#goclawtester", "goclawbot: my own line") // our own msg

	// Then a real mention, which MUST be the first thing we receive (proving the
	// earlier two were dropped, not merely delayed).
	srv.injectChannelMessage("steve", "#goclawtester", "goclawbot: real one")
	select {
	case in := <-inbound:
		if in.Text != "real one" {
			t.Errorf("first forwarded inbound = %q, want 'real one' (chatter/self should be ignored)", in.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the real mention was not forwarded")
	}
}

func TestChannelSendPostsPrivmsg(t *testing.T) {
	ch, _, srv, _ := startTestChannel(t, "goclawbot")
	if err := ch.Send(context.Background(), plugin.Outbound{Channel: "irc", ChatID: "#goclawtester", Text: "hi all"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case raw := <-srv.privCh: // "<target> :<text>"
		if raw != "#goclawtester :hi all" {
			t.Errorf("server got PRIVMSG %q, want '#goclawtester :hi all'", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send did not reach the server")
	}
}

// A multi-line reply (e.g. the /commands listing) must be sent as ONE PRIVMSG PER
// LINE, since IRC has no multi-line message: a literal newline would terminate the line
// and the rest would be lost. Blank lines are skipped (an empty PRIVMSG is invalid).
func TestChannelSendSplitsOnNewlines(t *testing.T) {
	ch, _, srv, _ := startTestChannel(t, "goclawbot")
	text := "Commands:\n  /reset - start fresh\n\n  /compact - shrink"
	if err := ch.Send(context.Background(), plugin.Outbound{Channel: "irc", ChatID: "#goclawtester", Text: text}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := []string{
		"#goclawtester :Commands:",
		"#goclawtester :  /reset - start fresh",
		"#goclawtester :  /compact - shrink",
	}
	for i, w := range want {
		select {
		case raw := <-srv.privCh:
			if raw != w {
				t.Errorf("PRIVMSG %d = %q, want %q", i, raw, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing PRIVMSG %d (%q)", i, w)
		}
	}
	// No extra PRIVMSG (the blank line produced nothing).
	select {
	case extra := <-srv.privCh:
		t.Errorf("unexpected extra PRIVMSG %q (blank line should be skipped)", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestChannelAnswersPing(t *testing.T) {
	_, _, srv, _ := startTestChannel(t, "goclawbot")
	srv.injectPing("token123")
	// The client should answer with PONG; give the read loop a moment, then check the
	// server's received lines.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		var sawPong bool
		for _, l := range srv.received {
			if l == "PONG :token123" {
				sawPong = true
				break
			}
		}
		srv.mu.Unlock()
		if sawPong {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("client did not answer PING with PONG")
}
