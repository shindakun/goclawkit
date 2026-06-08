# webhook: a worked goclaw channel

`webhook` is the reference goclaw **channel** plugin: a generic inbound HTTP webhook
gateway. It is the worked demo for the channel side of [goclawkit](../../README.md),
exercising the `ServeChannel` runtime and the `channel.*` topics end to end.

## Channel, not tool: which direction is which

A **tool** is agent-initiated: the LLM calls it, args go in, a result comes back
(see [`goclaw-roll`](https://github.com/shindakun/goclaw-roll)). A **channel** is the opposite: messages arrive from
the *outside world* unprompted, the agent replies, and the reply goes back out. The
agent never "calls" a channel; the channel feeds the agent. That is the chat-gateway
shape (Telegram, Discord, ...).

This plugin is an **inbound** webhook channel:

```text
external system  --POST /inbound-->  webhook plugin  --(channel.inbound event)-->  host/agent
                                                                                       |
external system  <--POST OUTBOUND_URL--  webhook plugin  <--(channel.send request)--  agent reply
```

So `WEBHOOK_ADDR` is the address that *receives external POSTs*, and `OUTBOUND_URL`
is where the agent's reply is *delivered*. Use this when something external (a custom
chat frontend, an app relay, a service emitting events) needs to push messages at the
agent.

Note: "let the LLM fire a webhook" is the *outbound* direction and would be a **tool**
(request/response, agent-initiated), not this channel. This plugin does not do that.

## Build

```sh
go build -o webhook ./cmd/webhook
```

## Run it standalone (`-selftest`)

`-selftest` runs a full inbound -> outbound round trip in-process against a built-in
sink, with no host and no second service (the godoorkit `-local` parallel):

```sh
./webhook -selftest
# inbound:  chat=7 sender=alice text="hello from selftest"
# outbound: chat=7 text="echo: hello from selftest" (delivered to sink)
```

## Security: the inbound POST is an open door, so it is gated

An inbound POST becomes an agent prompt, so an unauthenticated endpoint would let
anyone who reaches the port inject messages into the agent (burning tokens, driving
tool calls) and spoof identity. This channel fails closed, matching goclaw's security
posture (unknown/malformed input is denied, not allowed):

- AUTHENTICATION. Every POST must carry the shared secret `WEBHOOK_TOKEN` in
  `X-Webhook-Token` (or `Authorization: Bearer <token>`), compared in constant time. A
  missing/wrong token, or an unset `WEBHOOK_TOKEN`, returns **401** and the body never
  reaches the agent. The host supplies the value via env; it is never in `plugin.yml`.
  For a PUBLIC endpoint, upgrade to HMAC body signatures (`X-Signature: sha256=...`
  plus a timestamp, the GitHub/Stripe model); this demo ships the bearer token.
- IDENTITY. The body's `sender` is treated as a display name only. The access-gate
  `SenderID` is NOT taken verbatim from the body: it is namespaced (`webhook:<sender>`)
  or pinned via `WEBHOOK_SENDER_ID`, so a webhook caller can never collide with a
  Telegram/Discord owner's id at the host's access gate.

The host still applies its own access gate on top: the plugin authenticates the
transport and pins identity; the host authorizes the resulting sender (defense in
depth).

## Run it for real

```sh
export WEBHOOK_ADDR=":8080"
export OUTBOUND_URL="https://your-app.example/agent-replies"
export WEBHOOK_TOKEN="a-long-random-shared-secret"   # required, or every POST is 401
./webhook        # the host normally launches this; running by hand, it waits on the handshake
```

Then an external system posts an inbound message (WITH the token):

```sh
curl -X POST localhost:8080/inbound \
  -H 'Content-Type: application/json' \
  -H 'X-Webhook-Token: a-long-random-shared-secret' \
  -d '{"chat_id":"7","sender":"alice","text":"hello"}'
```

That becomes a `channel.inbound` event the agent sees (with `SenderID` = `webhook:alice`);
the agent's reply is POSTed to `OUTBOUND_URL` as `{"chat_id":"7","text":"..."}`. A POST
without a valid token gets `401 unauthorized` and is never seen by the agent.

## Register it in goclaw

The host walks a `plugins/` directory; each plugin is one subdir shipping a
[`plugin.yml`](plugin.yml) read before launching. A channel's manifest has
`kind: channel` and **no** `command:` (a channel is not a slash command), and lists
the env var names it needs:

```yaml
name: webhook
kind: channel
version: "1.0.0"
exec: webhook
env:
  - WEBHOOK_ADDR
  - OUTBOUND_URL
```

The host launches the binary, does the `hello` handshake (reading `kind: channel`),
and wraps it onto the host's `channels.ChannelAdapter` so routing treats it like any
built-in channel. Secrets are never written here: `env` lists names and the host
supplies values. See goclaw's `docs/plugins-design.md` for the host side.

## Talk to it by hand (the wire smoke test)

[`main_test.go`](main_test.go)'s `TestWireProtocolEndToEnd` execs the built binary,
does the `hello`/`hello.ok` handshake (asserting `kind: channel`), POSTs an inbound
to the plugin's listener and reads the correlated `channel.inbound` event off stdout,
then sends a `channel.send` request and asserts the reply reached a test sink. It is
the channel analogue of roll's end-to-end wire test.

```sh
go test ./cmd/webhook -run TestWireProtocolEndToEnd -v
```
