# goclawkit

The SDK for writing **goclaw plugins**: small Go binaries the goclaw host launches
and talks to over a length-prefixed binary frame protocol on stdin/stdout. You
implement one interface, call `Serve` (a tool) or `ServeChannel` (a channel), and
your binary is a plugin.

## Why a separate process per plugin

goclaw adds a plugin without rebuilding the host, and adds or reconfigures a plugin
without restarting the host. That works because each plugin is its own process: the
host launches it, speaks a small framed protocol over stdin/stdout, and "reloads"
simply by killing and relaunching it. This avoids Go's `plugin`/`.so` buildmode,
which cannot unload, demands an exact toolchain match, and is Linux/macOS only. A
subprocess model means a version mismatch is a clean handshake refusal rather than a
panic, and a crashing plugin cannot take down the host.

This mirrors the author's godoorkit, where a BBS door is a standalone binary the BBS
launches: here the plugin is the command, and goclawkit is the kit.

## Two kinds of plugin

- **Tools** are request/response: the agent calls the tool with args and gets a
  result back. Agent-initiated. (A tool can also be exposed as a slash command.)
- **Channels** are long-lived and bidirectional: messages arrive from the outside
  world unprompted, the agent replies, and the reply goes back out. The chat-gateway
  shape (Telegram, Discord, an inbound webhook). The agent never "calls" a channel;
  the channel feeds the agent.

Both ride the same wire protocol; a channel just adds new topics, no format change.

## Quickstart (a tool)

A one-tool plugin's `main` is one line:

```go
package main

import "github.com/shindakun/goclawkit/pkg/plugin"

func main() {
    plugin.ServeTool(myTool{}, "mytool", "1.0.0")
}
```

`myTool` implements `plugin.Tool` (an `Info()` and an `Invoke(ctx, args)`). `Serve`
owns the protocol, handshake, concurrency, and panic recovery, so you write only
`Invoke`. A channel is the same idea with `plugin.Channel` (`Start`/`Send`) and
`plugin.ServeChannel`.

Build and run the worked dice-roller tool standalone (no host needed):

```sh
go build -o roll ./cmd/roll
./roll -selftest          # 2d6 -> [4, 5] = 9
```

## Packaging and installing a plugin

A plugin ships as its own directory with the built binary and a declarative
`plugin.yml` the host reads before launching (kind, version, the env var names it
needs, any slash command). The host walks a `plugins/` directory; each plugin is one
subdir. `scripts/build-plugin.sh` stages a plugin (binary + its `plugin.yml`) into
`build/<name>/`, ready to copy into goclaw:

```sh
scripts/build-plugin.sh roll      # -> build/roll/{roll, plugin.yml}
scripts/build-plugin.sh           # build every cmd/<name>/ that has a plugin.yml
```

Secrets are never written into `plugin.yml`: it lists env var *names*; the host
supplies the values at launch.

## Layout

Following godoorkit, importable code lives under `pkg/` and runnable binaries
(plugins) under `cmd/`:

- [`pkg/ipc/`](pkg/ipc/) — the shared wire protocol (frames, framing, the Session).
- [`pkg/plugin/`](pkg/plugin/) — the author-facing SDK (`Tool`, `Channel`, `Serve`,
  `ServeChannel`, `ServeTool`).
- [`cmd/roll/`](cmd/roll/) — the worked **tool** demo (a dice roller).
- [`cmd/webhook/`](cmd/webhook/) — the worked **channel** demo (an inbound HTTP
  webhook gateway, authenticated and fail-closed).

## Documentation

- [`docs/sdk-spec.md`](docs/sdk-spec.md) — the SDK + wire-protocol reference: frame
  format, the tool and channel contracts, the `plugin.yml` schema, topic conventions.
  The authoritative contract.
- [`cmd/roll/README.md`](cmd/roll/README.md) — build, run, register, and the wire
  smoke test for the tool demo.
- [`cmd/webhook/README.md`](cmd/webhook/README.md) — the channel demo, including its
  inbound auth and identity model.

The host side (the manifest walk, launching, supervision, hot add/reload) lives in
goclaw at `docs/plugins-design.md`; goclawkit is only the plugin-author SDK plus the
shared protocol.
