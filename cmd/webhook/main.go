// Command webhook is the worked goclaw CHANNEL plugin demo: a generic HTTP webhook
// gateway. The goclaw host launches this binary and speaks the framed stdio protocol
// to it; main() wires the channel and calls plugin.ServeChannel.
//
// Unlike a tool (request/response), a channel is long-lived and bidirectional:
//   - INBOUND: a POST /inbound to this plugin's HTTP listener becomes an Inbound
//     message streamed up to the host (a channel.inbound event).
//   - OUTBOUND: the host's reply arrives as a channel.send request; the plugin POSTs
//     it to a configured OUTBOUND_URL.
//
// It needs no third-party account, so it is curl-testable. Env:
//
//	WEBHOOK_ADDR   listen address for inbound (default ":8080")
//	OUTBOUND_URL   where Send POSTs outbound messages (required unless -selftest)
//
// Run it standalone (the godoorkit -local parallel):
//
//	go build -o webhook ./cmd/webhook
//	./webhook -selftest
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shindakun/goclawkit/pkg/plugin"
)

const (
	version     = "1.0.0"
	channelName = "webhook"
)

// inboundBody is the JSON a client POSTs to /inbound.
type inboundBody struct {
	ChatID string `json:"chat_id"`
	Sender string `json:"sender"`
	Text   string `json:"text"`
}

// outboundBody is the JSON the plugin POSTs to OUTBOUND_URL on Send.
type outboundBody struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// webhookChannel implements plugin.Channel: an HTTP listener for inbound, an HTTP
// POST for outbound.
type webhookChannel struct {
	addr        string       // listen address for inbound
	outboundURL string       // where Send posts
	token       string       // shared secret required on every inbound POST (fail closed)
	pinSenderID string       // if set, every inbound's SenderID is forced to this value
	client      *http.Client // reused for outbound posts

	// now is injected so tests can pin timestamps; defaults to time.Now.
	now func() time.Time
}

func newWebhookChannel(addr, outboundURL, token, pinSenderID string) *webhookChannel {
	return &webhookChannel{
		addr:        addr,
		outboundURL: outboundURL,
		token:       token,
		pinSenderID: pinSenderID,
		// Use the SDK's HTTPClient so outbound POSTs route through goclaw's credential
		// proxy (and trust the proxy CA) when running in the sandbox. Never hand-roll a
		// Transport for an external call; see plugin.HTTPClient.
		client: plugin.HTTPClientTimeout(10 * time.Second),
		now:    time.Now,
	}
}

func (c *webhookChannel) Info() plugin.Info {
	return plugin.Info{Name: channelName, Version: version}
}

// Start runs the inbound HTTP listener and streams Inbound until ctx is cancelled.
func (c *webhookChannel) Start(ctx context.Context) (<-chan plugin.Inbound, error) {
	out := make(chan plugin.Inbound)

	mux := http.NewServeMux()
	mux.HandleFunc("/inbound", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		// Fail closed: a missing/unset/wrong token rejects with 401 and the body is
		// never decoded into an Inbound, so nothing reaches the agent.
		if !c.authOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		in, err := c.decodeInbound(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case out <- in:
			w.WriteHeader(http.StatusAccepted)
		case <-ctx.Done():
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
		}
	})

	srv := &http.Server{Addr: c.addr, Handler: mux}
	// Bind synchronously so a bad address fails Start (not silently in a goroutine).
	ln, err := listen(c.addr)
	if err != nil {
		return nil, fmt.Errorf("webhook: listen %s: %w", c.addr, err)
	}

	go func() {
		_ = srv.Serve(ln) // returns on Shutdown
	}()
	go func() {
		<-ctx.Done()
		// Stop accepting, drain briefly, then close the stream.
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		close(out)
	}()

	return out, nil
}

// authOK reports whether the request carries the shared secret. It compares in
// constant time and fails closed: if no token is configured, NOTHING authenticates,
// so every POST is rejected (an open inbound is never the default). The token may
// arrive as "X-Webhook-Token: <t>" or "Authorization: Bearer <t>".
func (c *webhookChannel) authOK(r *http.Request) bool {
	if c.token == "" {
		return false // fail closed: an unconfigured token denies everything
	}
	got := r.Header.Get("X-Webhook-Token")
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(c.token)) == 1
}

// decodeInbound maps a POST body to a normalized Inbound. It NEVER trusts the body's
// asserted identity as the access-gate SenderID: the body's sender is only a display
// name, and SenderID is pinned (WEBHOOK_SENDER_ID) or namespaced ("webhook:"+id) so a
// webhook caller can never collide with a Telegram/Discord owner's id.
func (c *webhookChannel) decodeInbound(body io.Reader) (plugin.Inbound, error) {
	var b inboundBody
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		return plugin.Inbound{}, fmt.Errorf("bad inbound json: %w", err)
	}
	if b.ChatID == "" {
		return plugin.Inbound{}, fmt.Errorf("inbound missing chat_id")
	}
	senderID := c.pinSenderID
	if senderID == "" {
		senderID = "webhook:" + b.Sender // namespaced; never the verbatim asserted id
	}
	return plugin.Inbound{
		Channel:   channelName,
		ChatID:    b.ChatID,
		SenderID:  senderID,
		Sender:    b.Sender, // display name only
		Text:      b.Text,
		Timestamp: c.now(),
	}, nil
}

// Send POSTs the outbound message to OUTBOUND_URL. Safe to call concurrently with
// the inbound listener.
func (c *webhookChannel) Send(ctx context.Context, out plugin.Outbound) error {
	if c.outboundURL == "" {
		return fmt.Errorf("webhook: OUTBOUND_URL not set")
	}
	body, err := json.Marshal(outboundBody{ChatID: out.ChatID, Text: out.Text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.outboundURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: post outbound: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: outbound returned %s", resp.Status)
	}
	return nil
}

func main() {
	selftest := flag.Bool("selftest", false, "run a local inbound->outbound round trip and exit (no host needed)")
	flag.Parse()

	if *selftest {
		if err := runSelftest(); err != nil {
			fmt.Fprintf(os.Stderr, "selftest error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	addr := envOr("WEBHOOK_ADDR", ":8080")
	ch := newWebhookChannel(addr, os.Getenv("OUTBOUND_URL"), os.Getenv("WEBHOOK_TOKEN"), os.Getenv("WEBHOOK_SENDER_ID"))
	if ch.token == "" {
		// Fail closed and say why: without a token every inbound POST is rejected, so
		// running without one is almost certainly a misconfiguration.
		plugin.Logf(channelName, "WARNING: WEBHOOK_TOKEN is unset; all inbound POSTs will be rejected (401)")
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
