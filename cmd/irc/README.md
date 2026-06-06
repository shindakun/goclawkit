# irc: a worked goclaw channel (IRC bridge)

`irc` is a reference goclaw **channel** plugin: a minimal IRC bridge. It is a worked
demo for the channel side of [goclawkit](../../README.md), exercising the
`ServeChannel` runtime and the `channel.*` topics over a real (TLS) network
connection, with **zero external dependencies** (stdlib `crypto/tls`, `bufio`, `net`).

## Channel, not tool, and outbound-only in the sense that matters

A **tool** is agent-initiated (the LLM calls it; see [`cmd/roll/`](../roll/)). A
**channel** is fed by the outside world: messages arrive, the agent replies, the
reply goes back out. This bridge is a channel.

It is "outbound-only" in the sense the plugin design settled on: **the bot DIALS OUT**
to the IRC server and there is **no inbound listener / open port**. Everything happens
over the single client-initiated TLS connection the bot opened, which is full-duplex,
so it both reads mentions and writes replies. Because nothing can connect *to* the
plugin, there is no inbound endpoint to authenticate (the security headache an inbound
webhook has simply does not exist here).

```text
bot  --TLS dial-->  irc.libera.chat:6697
  <- PRIVMSG mentioning the bot / DM  ==>  Inbound  -> agent
  -> PRIVMSG (reply)                  <==  Outbound <- agent
```

## What it forwards

To avoid flooding the agent, it forwards only:

- a channel line that **mentions the bot's nick** (`goclawbot: ...`, `goclawbot, ...`,
  or the nick as a word), with the mention prefix stripped, and
- a **direct query** (a PRIVMSG whose target is the bot's nick).

General channel chatter and the bot's own lines are ignored. The reply routes back to
where the message came from: the channel for a mention, the sender for a query.

## Config

All via env (the host supplies the values; see [`plugin.yml`](plugin.yml)):

| Env | Default | Meaning |
|---|---|---|
| `IRC_SERVER` | `irc.libera.chat:6697` | server `host:port` (TLS) |
| `IRC_NICK` | `goclawbot` | the bot's nick |
| `IRC_CHANNEL` | `#goclawtester` | channel to join |
| `IRC_OWNER_NICK` | (unset) | optional, informational |
| `IRC_PLAINTEXT` | (unset) | **test/dev only**: `1` dials without TLS (for the wire test against a local fake server); never use in production |

## Build and run standalone (`-selftest`)

`-selftest` runs a full connect → join → mention → reply round trip in-process against
a built-in fake IRC server, offline (the godoorkit `-local` parallel):

```sh
go build -o irc ./cmd/irc
./irc -selftest
# [irc] connected to 127.0.0.1:NNNNN as goclawbot, joined #goclawtester
# joined:   #goclawtester
# inbound:  from=steve chat=#goclawtester text="ping?"
# outbound: PRIVMSG #goclawtester :pong!
```

To install into goclaw, build for **Linux** (plugins run in the agent's Linux
container): `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o irc ./cmd/irc`, or use
`scripts/build-plugin.sh` (defaults to `linux/amd64`).

## Register it in goclaw

The host walks a `plugins/` directory; each plugin is one subdir shipping a
[`plugin.yml`](plugin.yml) read before launching. A channel's manifest has
`kind: channel` and no `command:`. The host launches the binary, does the `hello`
handshake (reading `kind: channel`), and wraps it onto the host's
`channels.ChannelAdapter` so routing treats it like any built-in channel.

## Owner gating and the identity caveat

The bridge forwards mentions/DMs with `SenderID` set to the IRC nick and lets
**goclaw's host access gate authorize** (the same model as the Telegram/Discord
adapters): the owner is configured host-side, not enforced in the plugin.

**Caveat:** plain IRC nicks are NOT authenticated. Without SASL / NickServ, anyone can
use any nick, so `SenderID` is spoofable. For a `#goclawtester` demo that is
acceptable (and the host gate is the real authority), but a production deployment
should add SASL and treat the NickServ-verified account as the identity. That is the
upgrade path; this demo ships the unauthenticated-nick baseline.

## Talk to it by hand (the wire smoke test)

[`main_test.go`](main_test.go)'s `TestWireProtocolEndToEnd` execs the built binary
pointed at an in-process fake IRC server, does the `hello`/`hello.ok` handshake
(asserting `kind: channel`), has the fake send a mention, reads the correlated
`channel.inbound` event off stdout, then sends a `channel.send` and asserts the reply
reached the server as a `PRIVMSG`.

```sh
go test ./cmd/irc -run TestWireProtocolEndToEnd -v
```
