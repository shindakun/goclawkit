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
  shape (Telegram, Discord, IRC). The agent never "calls" a channel; the channel feeds
  the agent.

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

The worked example plugins live in their own repos (each is a real, installable
plugin, which is also how a third party would ship one):

- [`goclaw-roll`](https://github.com/shindakun/goclaw-roll) — the worked **tool**
  demo: a dice roller. `go build -o roll . && ./roll -selftest`.
- [`goclaw-irc`](https://github.com/shindakun/goclaw-irc) — the worked **channel**
  demo: a dial-out IRC bridge.

To INSTALL a plugin into goclaw, build it for **Linux** (plugins run in the agent's
Linux container, so a host-platform binary fails with `exec format error`):
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o <name> .`.

## Packaging and installing a plugin

A plugin ships as its own directory with the built binary and a declarative
`plugin.yml` the host reads before launching (kind, version, the env var names it
needs, any slash command). The host walks a `plugins/` directory; each installed plugin
is one subdir, the Linux binary plus its `plugin.yml`.

Secrets are never written into `plugin.yml`: it lists env var *names*; the host
supplies the values at launch.

### Two repo layouts (both are supported)

A plugin's SOURCE repo can be laid out two ways, and goclaw's installer
(`/plugin add`) handles both:

1. **One plugin at the repo root** (e.g. `goclaw-roll`): `plugin.yml` and the package
   sit at the repo root.

   ```text
   goclaw-roll/
     go.mod
     plugin.yml
     main.go
   ```

   Install: `/plugin add https://github.com/you/goclaw-roll`

2. **A monorepo with several plugins under `cmd/`** (e.g. `goclaw-gmail`, which ships a
   `gmail` channel and a `gmail-tools` tool that share one `go.mod` and an `internal/`):
   each plugin is `cmd/<name>/` with its OWN `plugin.yml`.

   ```text
   goclaw-gmail/
     go.mod
     internal/...            # shared code
     cmd/gmail/plugin.yml    # the channel
     cmd/gmail-tools/plugin.yml  # the tool
   ```

   Install ONE plugin at a time by naming its subdir with `#<subdir>`:

   ```text
   /plugin add https://github.com/you/goclaw-gmail#cmd/gmail
   /plugin add https://github.com/you/goclaw-gmail#cmd/gmail-tools
   ```

The monorepo form is the right choice when plugins share auth/config/code (a service's
channel + its tools); the one-per-repo form is simplest for a standalone plugin. In both
cases the build is sandboxed in a throwaway container and goclaw scans the WHOLE repo for
red flags even when only one subdir is built (see goclaw `docs/security.md`).

## Layout

goclawkit is the SDK only. Following godoorkit, importable code lives under `pkg/`:

- [`pkg/ipc/`](pkg/ipc/) — the shared wire protocol (frames, framing, the Session).
- [`pkg/plugin/`](pkg/plugin/) — the author-facing SDK (`Tool`, `Channel`, `Poller`,
  `Serve`, `ServeChannel`, `ServePoll`, `ServeTool`, `HTTPClient`).

goclawkit is the SDK only; it bundles no plugins. The worked demos are their own repos:
[`goclaw-roll`](https://github.com/shindakun/goclaw-roll) (tool) and
[`goclaw-irc`](https://github.com/shindakun/goclaw-irc) (channel), each a real
single-plugin repo you can clone, build, and install.

## Documentation

- [`docs/sdk-spec.md`](docs/sdk-spec.md) — the SDK + wire-protocol reference: frame
  format, the tool and channel contracts, the `plugin.yml` schema, topic conventions.
  The authoritative contract.
- [`goclaw-roll`](https://github.com/shindakun/goclaw-roll) /
  [`goclaw-irc`](https://github.com/shindakun/goclaw-irc) — the worked tool and channel
  demos, each with its own build/run/register README.

The host side (the manifest walk, launching, supervision, hot add/reload) lives in
goclaw at `docs/plugins-design.md`; goclawkit is only the plugin-author SDK plus the
shared protocol.
