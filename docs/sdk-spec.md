# goclawkit: SDK and wire-protocol reference

The authoritative contract for **goclaw plugins**: separate compiled binaries the
goclaw side launches and talks to over stdio. It specifies the wire protocol, the
tool and channel contracts, the `plugin.yml` manifest schema, and the topic
conventions. The worked demos are their own repos
([`goclaw-roll`](https://github.com/shindakun/goclaw-roll) for a tool,
[`goclaw-irc`](https://github.com/shindakun/goclaw-irc) for a channel); their READMEs
cover build/run/register. Start at the repo [README](../README.md) for the overview;
read this when you need the exact contract.

> Note on "the host" vs the launcher: this spec says "the host launches the plugin"
> for brevity, but a plugin is launcher-AGNOSTIC. It speaks frames over stdin/stdout
> to whatever parent process started it. In goclaw's current deployment that parent
> is the in-container agent RUNNER (plugins run inside the agent's sandbox, not on
> the host machine), which is a security choice on goclaw's side. The SDK and a
> plugin author do not need to care: read "the host" below as "whatever goclaw
> process launched me."

Module path: `github.com/shindakun/goclawkit`. Pure Go, no external dependencies
for the core (stdlib only). Match Go 1.26 (goclaw is on 1.26.3).

## Why this exists (context, do not re-derive)

goclaw runs its agent in a Podman container and talks to it only over SQLite
files: a process boundary with narrow, explicit communication. Plugins follow the
same philosophy one notch smaller: each plugin is its own process the host
launches and speaks a small framed protocol to. This deliberately avoids Go's
`plugin`/`.so` buildmode (cannot unload, demands an exact toolchain match, panics
on mismatch, Linux/macOS only). A subprocess model means "reload" is "kill and
relaunch", a version mismatch is a clean handshake refusal, and a crashing plugin
cannot take down the host.

The companion design lives in goclaw at `docs/plugins-design.md`. Read it for the
host side. This file is the SDK (plugin-author) side plus the protocol both sides
share.

### Mirror the spirit of godoorkit, not just its frame format

goclawkit is goclaw's `godoorkit`: the kit a third party uses to author a binary
the host launches. Borrow the philosophy, not only the `pkg/ipc` framing. Concretely,
keep these godoorkit properties true in goclawkit:

- **The plugin IS the command.** Just as every godoorkit door is a standalone
  `cmd/<name>-door` binary the BBS launches ("your binary is the door, like
  LORD.EXE"), a goclaw plugin is a standalone binary the host launches: its `main` is
  the plugin, at a repo root (e.g. `goclaw-roll`) or under `cmd/<name>/` in a monorepo,
  never under an `examples/` tree.
- **A minimal interface; the framework owns the loop.** A godoorkit door implements
  a tight contract (`Init`/`Run`/`Cleanup`/`Info`) and `pkg/door` owns terminal
  state and lifecycle so the author "just reads input and writes output, no
  `MakeRaw()` needed." goclawkit's parallel: a tool implements only `Info`/`Invoke`,
  and `Serve` owns the handshake, framing, concurrency, and panic recovery so the
  author writes only `Invoke`. Resist adding author-facing ceremony.
- **Tight, simple, no bloat.** Stdlib only; small single-purpose files; the contract
  files carry doc-comment usage examples (as `pkg/door/door.go` does).
- **Standalone local testing.** A door runs with `-local` without a BBS; a plugin
  runs with `-selftest` without the host.

If a future decision is unclear, prefer the option that keeps these true.

## Scope

The SDK covers two plugin kinds (`Kind` is `tool` or `channel`), both implemented:

- **Tools** (request/response): `plugin.Tool` + `Serve`/`ServeTool`. Worked demo:
  the [`goclaw-roll`](https://github.com/shindakun/goclaw-roll) repo (a dice roller).
- **Channels** (long-lived, bidirectional): `plugin.Channel` + `ServeChannel`. Worked
  demo: the [`goclaw-irc`](https://github.com/shindakun/goclaw-irc) repo (an IRC bridge
  that dials OUT). A POLL channel (inbound from polling an upstream, e.g. Gmail) is a
  variant of the channel kind: implement `plugin.Poller` and call `ServePoll`, which
  owns the poll loop and adapts onto a channel (see "Poll channels").

A plugin that calls an external HTTPS API should use `plugin.HTTPClient()` (see "Making
external HTTPS calls").

Both ride the same frame format; a channel adds only new topics, never a wire-format
change. What is deliberately NOT here: Layer 2 (the cross-plugin socket coordination
bus) is still deferred (see the two-layers section), and the host side (the plugins
directory walk, launching, supervision, hot reload) lives in goclaw, not this SDK.

## Repository layout

```text
goclawkit/
  go.mod                      module github.com/shindakun/goclawkit
  .gitignore                  test/coverage output, editor/OS noise (SDK-only module)
  README.md                   short: what it is, link to docs/sdk-spec.md, quickstart
  docs/
    sdk-spec.md               this file: the SDK + wire-protocol reference
  pkg/
    ipc/                      package ipc: the shared wire protocol
      proto.go                frame header, types, topics, framing, Session, Transport
      proto_test.go           round-trip framing tests
    plugin/                   package plugin: the author-facing SDK
      plugin.go               Info, Kind, capability detection, shared types
      tool.go                 the Tool interface + arg/result types
      channel.go              the Channel + ActionSender interfaces, Inbound/Outbound
      serve.go                Serve(): the tool runtime a tool plugin's main() calls
      serve_test.go           drive Serve with scripted stdin, assert stdout frames
      serve_channel.go        ServeChannel(): the channel runtime (inbound + send pumps)
      serve_channel_test.go   drive ServeChannel over in-memory pipes
      serve_poll.go           ServePoll(): poll-channel runtime (a thin Channel adapter)
      serve_poll_test.go      drive ServePoll over in-memory pipes
      http.go                 HTTPClient(): proxy-correct *http.Client for external HTTPS
```

goclawkit is the SDK only; it bundles no plugins (no `cmd/`).

The worked tool and channel demos are their OWN repos (each a real single-plugin
repo, the same way a third party ships a plugin), not bundled here:

```text
github.com/shindakun/goclaw-roll/    the worked TOOL demo (dice roller)
github.com/shindakun/goclaw-irc/     the worked CHANNEL demo (IRC bridge, dials OUT)
```

Layout follows godoorkit's `pkg/` split: importable code lives under `pkg/<name>/`
(godoorkit has `pkg/door`, `pkg/ipc`, ...). goclawkit mirrors it: `pkg/ipc` is the
shared wire protocol (the parallel to godoorkit's `pkg/ipc`), and `pkg/plugin` is the
author-facing SDK (the parallel to `pkg/door`). `pkg/plugin` imports `pkg/ipc` for the
frame types. A plugin author imports `github.com/shindakun/goclawkit/pkg/plugin` (and
rarely `pkg/ipc` directly). The runnable-binary half of the convention (a plugin's
`main` under `cmd/<name>/`, godoorkit's `cmd/<name>-door`) lives in the PLUGIN's repo,
not here, goclawkit ships no binaries.

An author's OWN plugin repo follows the same `cmd/<name>/` convention, and a single
repo MAY ship several plugins that share a `go.mod` and an `internal/` (e.g. a service's
channel plus its tools). goclaw installs one at a time by naming the subdir:
`/plugin add <git-url>#cmd/<name>`. A repo with exactly one plugin can instead put
`plugin.yml` at its root and install with a bare `/plugin add <git-url>`. See the
README "Two repo layouts" and goclaw `docs/security.md` (the whole repo is scanned even
for a subdir build).

The plugin is the command. Every godoorkit door is a standalone binary under
`cmd/<name>-door/` that the BBS launches; goclaw launches a plugin the same way, so a
plugin's `main` is the binary (at a repo root like `goclaw-roll`, or under `cmd/<name>/`
in a monorepo), not under an `examples/` tree. The thin `main()` imports `pkg/plugin`,
wires the tool, and calls `plugin.ServeTool`; the tool logic can live inline for a small
plugin (godoorkit splits larger door logic into a `doors/<name>/` package, a pattern a
real plugin can follow but a small one does not need).

No LICENSE for now (the host repo goclaw ships none either); add one later if the
module is published.

## The wire protocol (pkg/ipc/proto.go, package ipc)

This protocol is designed to be EXTENSIBLE without changing the wire format. The
design follows a proven pattern (godoorkit's `pkg/ipc`): a small fixed set of
message PATTERNS (a binary header), and an open-ended set of FEATURES carried as a
`Topic` string plus an opaque JSON payload. Adding a capability later (channels,
new tool behaviors, host callbacks) means defining a new topic, never a new frame
type or a format bump. Do not deviate from this; the whole point is that the format
freezes early.

### Two layers, one frame format

We borrow two distinct things from godoorkit, and they live at two layers. The
frame FORMAT (below) is shared by both; only the transport differs.

- **Layer 1, host <-> plugin control (this SDK).** How the host launches a
  plugin and exchanges requests/results with it. The transport is the plugin's
  **stdin/stdout pipes**. This is the parallel to a godoorkit door: a door's stdio
  carries its terminal session, but a goclaw plugin has no terminal, so its stdio
  is free to carry the control protocol instead. Every plugin needs this. It is the
  baseline and the whole of what this SDK implements.

- **Layer 2, plugin <-> plugin / plugin <-> host coordination (deferred).** A shared
  bus for cross-plugin state, presence, or broadcast, the exact
  role of godoorkit's `pkg/ipc` hub: a Unix-socket daemon that plugins dial. It is
  OPT-IN; a single plugin never needs it. goclaw adds it only if/when plugins must
  coordinate with each other. When it lands it reuses the SAME frame format over a
  socket `Transport`, so it is purely additive: a new `Transport` implementation,
  not a new wire format.

The `Transport` interface exists from day one precisely so Layer 2 (and a future
networked/cross-container plugin) is a new implementation, not a rewrite:

```go
// Transport is the byte stream a Session reads frames from and writes frames to.
// Layer 1 uses StdioTransport (the plugin's stdin/stdout). A later Layer 2 socket
// bus would provide a Dial/Listen-based Transport carrying the same frames.
type Transport interface {
    io.Reader
    io.Writer
}

// StdioTransport reads os.Stdin and writes os.Stdout. The plugin's Layer 1
// default. stderr is reserved for the plugin's own logs (the host captures them);
// a plugin must never write anything but frames to stdout.
type StdioTransport struct{}
```

### Framing (length-prefixed, binary header)

Each frame is a fixed binary header followed by an opaque payload. Length-prefixed
framing (not newline-delimited) means the payload is arbitrary bytes with no
escaping and an explicit size cap, which is what makes "opaque payload" safe.

```text
+-----------------------------------------------+
| Magic:    "GCLW" (4 bytes)                    |
| Version:  uint8  (protocol version, = 1)      |
| Type:     uint8  (frame pattern, see below)   |
| Flags:    uint8  (reserved, 0 for now)        |
| ID:       uint64 (correlates request->result) |
| TopicLen: uint16                              |
| Topic:    string (TopicLen bytes, UTF-8)      |
| PayLen:   uint32                              |
| Payload:  bytes  (PayLen bytes, opaque JSON)  |
+-----------------------------------------------+
```

All integers big-endian. Constants and caps:

```go
const (
    Magic       = "GCLW"
    ProtocolVer = 1               // bump ONLY for a breaking header change; topics
                                  // and payload shapes are NOT breaking changes
    MaxTopicLen = 255
    MaxPayload  = 8 * 1024 * 1024 // 8 MiB; reject larger frames
)

var (
    ErrInvalidMagic    = errors.New("goclawkit: invalid magic")
    ErrUnsupportedVer  = errors.New("goclawkit: unsupported protocol version")
    ErrTopicTooLong    = errors.New("goclawkit: topic too long")
    ErrPayloadTooLarge = errors.New("goclawkit: payload too large")
)
```

### Frame types (the small fixed set of PATTERNS)

There are only a handful, and they are PATTERNS, not features. Features live in
`Topic`.

```go
type FrameType uint8

const (
    FrameControl  FrameType = 0 // handshake, shutdown, heartbeat: Topic names which
    FrameRequest  FrameType = 1 // a request expecting a Result (correlated by ID)
    FrameResult   FrameType = 2 // the reply to a Request (same ID)
    FrameEvent    FrameType = 3 // a one-way push (no reply), e.g. a channel inbound msg
)
```

That is the entire frame vocabulary, and it should stay that way. How the four map
onto behavior:

- Handshake is `FrameControl` with `Topic="hello"` (host to plugin) and the reply
  `FrameControl` `Topic="hello.ok"` (plugin to host). The hello payload carries the
  magic check result + plugin `Info`; a version/magic mismatch is a clean refusal,
  not a crash.
- A tool call is `FrameRequest` `Topic="tool.invoke"`, payload
  `{"tool":"roll","args":{...}}`; the reply is `FrameResult` (same ID), payload
  `{"text":"...","is_error":false}`.
- Graceful stop is `FrameControl` `Topic="shutdown"`.
- Liveness is `FrameControl` `Topic="heartbeat"`: the host sends a `heartbeat`
  control frame and the plugin replies with a `heartbeat` control frame carrying the
  SAME ID and an empty payload. v1 models the reply even though nothing depends on
  it yet, so adding host-side liveness checks later needs no plugin change. A plugin
  must never originate a heartbeat; it only answers one.
- A channel inbound message is `FrameEvent` `Topic="channel.inbound"`; an outbound
  send is `FrameRequest` `Topic="channel.send"` with a `FrameResult`; a typing action
  is `FrameRequest` `Topic="channel.action"`. None of these needed a new FrameType or
  a version bump when channels landed; they are just new topics. This is the
  extensibility guarantee, in action.

### Topic namespace convention

Dot-namespaced, `area.verb`. Reserve these areas: `hello`, `shutdown`,
`heartbeat` (control); `tool.*` (tool plugins); `channel.*` (channel plugins);
`host.*` (reserved for plugin-to-host callbacks later, e.g. `host.get_config`). A plugin receiving an UNKNOWN topic must reply (for a Request)
with an error Result, or ignore it (for an Event), never crash. The host does the
same. Unknown-topic tolerance is what lets a newer peer talk to an older one.

### Payloads (opaque JSON, decoded by topic)

The header carries no Go types; the payload is JSON decoded according to `Topic`.
Define payload structs in proto.go and document which topic uses which:

- `hello` -> `Hello{Magic string; ProtocolVer int}`
- `hello.ok` -> `HelloOK{Magic string; ProtocolVer int; Info Info}`
- `tool.invoke` -> `Invoke{Tool string; Args json.RawMessage}`
- `tool.invoke` result -> `Result{Text string; IsError bool}` (text-first for v1;
  richer typed results can be a NEW topic like `tool.invoke2` later, not a format
  change)
- `shutdown` -> empty payload
- `heartbeat` -> empty payload (both the host's ping and the plugin's reply; the
  ID correlates them, so no fields are needed)

### proto.go provides

- `WriteFrame(w io.Writer, f Frame) error`: validate caps, write header
  (big-endian) + topic + payload. One `Frame` struct holds Type, ID, Topic,
  Payload(`[]byte`).
- `ReadFrame(r io.Reader) (Frame, error)`: read + verify magic/version, enforce
  `MaxTopicLen`/`MaxPayload`, read exactly the declared bytes with `io.ReadFull`.
  Use a buffered reader; because the length is explicit there is NO line-length
  problem (this sidesteps the bufio.Scanner 64KB trap by construction).
- `Marshal(v any) ([]byte, error)` / `Unmarshal(b []byte, v any) error`: thin JSON
  helpers so call sites building/reading payloads stay terse.
- A `Session` helper wrapping a Transport with a write mutex (frames must never
  interleave) and `Send(Frame)` / `Recv() (Frame, error)`.

proto_test.go: encode then decode every FrameType and assert field-for-field
equality (mirror godoorkit's table test); reject a frame with bad magic, bad
version, an over-long topic, and an over-cap payload (assert the specific error);
round-trip a payload at the multi-megabyte scale to prove length-prefixing handles
large frames; and a partial-read test (a header split across two Reads) to prove
`io.ReadFull` reassembles it.

## Plugin identity and capabilities (pkg/plugin/plugin.go, package plugin)

```go
type Kind string

const (
    KindTool    Kind = "tool"
    KindChannel Kind = "channel"
)

type Info struct {
    Name        string `json:"name"`         // stable id, e.g. "roll"
    Kind        Kind   `json:"kind"`
    Version     string `json:"version"`      // the plugin's own version: semver,
                                             // bumped on any behavior change (see the
                                             // plugin.yml schema)
    ProtocolVer int    `json:"protocol_ver"` // must equal ipc.ProtocolVer
    // Tools advertises the tool names + descriptions this plugin exposes, so the
    // host can present them to the agent without invoking anything. Empty for a
    // channel plugin.
    Tools []ToolInfo `json:"tools,omitempty"`
}

type ToolInfo struct {
    Name        string `json:"name"`
    Description string `json:"description"` // shown to the agent so it knows when to call this
    // InputSchema is a JSON Schema (as raw JSON) describing the tool's args, so
    // the agent/host can validate before invoking. Keep it simple; a tool may
    // return an empty object schema if it takes no args.
    InputSchema json.RawMessage `json:"input_schema,omitempty"`
}
```

## The tool contract (pkg/plugin/tool.go, package plugin)

A tool plugin implements one interface. Keep it minimal:

```go
// Tool is one callable capability. A single plugin process may expose several
// tools (see ToolSet), but the simplest plugin exposes exactly one.
type Tool interface {
    // Info returns the tool's name, description, and input JSON Schema.
    Info() ToolInfo
    // Invoke runs the tool. args is the raw JSON the agent supplied (validated
    // against InputSchema host-side, but re-validate what matters). Return the
    // result text; return a non-nil error to signal failure (the host maps it to
    // an error result the agent sees).
    Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolSet is what a plugin hands to Serve: one or more tools under a single
// plugin Info. For a one-tool plugin, a small helper constructs this from a
// single Tool.
type ToolSet struct {
    Name    string // plugin name (Info.Name)
    Version string
    Tools   []Tool
}
```

Provide a convenience: `ServeTool(t Tool, name, version string)` that wraps a
single Tool into a ToolSet and calls Serve, so a one-tool plugin's main is one
line.

Following godoorkit's `pkg/door/door.go`, which embeds runnable usage examples in
the contract file's doc comments, tool.go should carry a short doc-comment example
of a minimal `Invoke` (unmarshal args, do the work, return text or error) right
above the `Tool` interface, so an author opening the file sees the shape without
hunting for the demo.

## The runtime loop (pkg/plugin/serve.go, package plugin)

`Serve` is what a plugin's `main()` calls. It owns the protocol so the author
writes only `Invoke`. Package `plugin` imports package `ipc`, so the frame types
below (`Frame`, `FrameControl`, `Transport`, `StdioTransport`, `Session`,
`ReadFrame`/`WriteFrame`, `Magic`, `ProtocolVer`) are `ipc.`-qualified in the real
code; the outline drops the qualifier for readability. Outline:

```go
// Serve runs the plugin protocol over StdioTransport until the host sends a
// shutdown control frame or stdin closes. It is the only function a plugin's
// main() needs.
func Serve(ts ToolSet) error {
    return serve(ts, StdioTransport{}) // split for testability
}

func serve(ts ToolSet, t Transport) error {
    // Wrap t in a Session (write mutex + ReadFrame/WriteFrame).
    //
    // 1. Read the first frame: it must be FrameControl Topic="hello" carrying
    //    Hello{Magic, ProtocolVer}. Verify Magic == ipc.Magic and
    //    ProtocolVer == ipc.ProtocolVer. On mismatch, optionally write a
    //    hello.ok with an error marker, then return an error and exit non-zero
    //    (the host kills mismatched plugins regardless).
    // 2. Reply with FrameControl Topic="hello.ok" carrying HelloOK{Magic,
    //    ProtocolVer, Info}. Info is built here: Name, Kind=KindTool, Version,
    //    ProtocolVer, and Tools[] from each Tool's Info().
    // 3. Loop on ReadFrame, dispatch by (Type, Topic):
    //      - FrameRequest, Topic="tool.invoke": decode Invoke{Tool, Args}, look up
    //        the named tool, call Invoke with a per-call ctx in its own goroutine,
    //        then write a FrameResult with the SAME ID carrying Result{Text,
    //        IsError}. A tool error -> Result{IsError:true, Text:err.Error()}. A
    //        panic in a tool MUST be recovered and turned into an error Result,
    //        never crash the loop.
    //      - FrameControl, Topic="shutdown": return nil (clean exit).
    //      - FrameControl, Topic="heartbeat": reply with a FrameControl
    //        Topic="heartbeat", SAME ID, empty payload. Modeled now so host-side
    //        liveness checks need no plugin change later (the plugin only answers a
    //        heartbeat, it never originates one).
    //      - any unknown Topic on a FrameRequest: reply FrameResult IsError=true
    //        ("unknown topic"); unknown Event/Control: ignore. Never crash. This
    //        unknown-topic tolerance is the forward-compat guarantee.
    // 4. If ReadFrame returns io.EOF (host died / stdin closed), return nil.
}
```

Concurrency: requests may be pipelined by the host. Dispatch each `tool.invoke` in
its own goroutine and serialize WRITES through the Session's mutex (one frame per
write, never interleave bytes from two frames). The result frame carries the
request's ID so the host correlates it regardless of completion order. v1 runs each
invoke in its own unbounded goroutine; only writes are serialized by the Session
mutex. A bounded worker pool is a later refinement if a flood of slow invokes ever
warrants it, but the write mutex is the only thing mandatory for correctness.

Logging: a plugin logs to **stderr** with the standard `log`/`slog`; the host
captures it. Never write logs to stdout (that is the frame channel). Optionally
provide a tiny `Logf(format, args...)` helper that writes to stderr with a
`[plugin-name]` prefix.

TTY hint (in the SDK, not per-plugin): `Serve` blocks waiting for the host's hello
on stdin, which looks like a hang to a person who runs a plugin binary directly. So
`Serve` (the real one over StdioTransport, NOT the testable `serve(ts, Transport)`)
checks whether os.Stdin is an interactive terminal and, if so, prints ONE line to
stderr before entering the loop: that this is a goclaw plugin waiting for the host
handshake, that Ctrl-D/Ctrl-C exits, and (generically) that the binary may offer a
self-test flag. Then it serves normally. A non-TTY stdin (the host's pipe, a file,
or `/dev/null`) stays silent, so host launches and scripts are unaffected. Because
this lives in `Serve`, EVERY plugin gets the hint for free; a plugin's main does not
reimplement it. Detect the terminal with the stdin FileInfo mode
(`os.ModeCharDevice`), excluding `/dev/null` (also a char device) via
`os.SameFile`; no external dependency. The hint must be emitted only by the
stdio-backed `Serve`, never inside `serve(ts, Transport)`, so the in-memory tests
stay quiet and deterministic.

serve_test.go: construct a ToolSet with a fake tool, drive `serve` with an
in-memory Transport (a pair of pipes): write a `hello` control frame in, assert a
`hello.ok` comes out with the right Info and ProtocolVer; write a `tool.invoke`
request, assert a FrameResult with the matching ID and expected text; write a
`shutdown`, assert serve returns nil. Also: a tool returning an error yields
Result.IsError=true; a tool that panics yields an error Result and the loop
survives the next frame; an unknown topic on a request yields an error Result, not
a crash; a hello with the wrong ProtocolVer is refused; and a `heartbeat` control
frame yields a `heartbeat` control frame back with the same ID and empty payload.

## Channel contract (pkg/plugin/channel.go, package plugin)

The `Channel` interface and the `Inbound`/`Outbound` types. `Serve` (the tool
runtime) does NOT handle channels; a channel plugin uses `ServeChannel` (below) over
the SAME frame protocol, adding only the reserved `channel.*` topics, no wire-format
change.

```go
// Channel is a long-lived, bidirectional plugin: it streams inbound messages up
// (as channel.inbound events) while accepting outbound sends concurrently (as
// channel.send requests), for the life of the host. Run it with ServeChannel.
type Channel interface {
    Info() Info
    // Start connects/listens and streams normalized inbound messages until ctx is
    // cancelled (ServeChannel cancels ctx on shutdown). The implementation owns
    // reconnect/backoff; return an error only for unrecoverable setup failure.
    Start(ctx context.Context) (<-chan Inbound, error)
    // Send delivers one outbound message. Called concurrently with the inbound
    // stream; the implementation must be safe for that.
    Send(ctx context.Context, out Outbound) error
}

// ActionSender is an OPTIONAL channel capability: a transient chat action (e.g.
// "typing"). ServeChannel checks for it with a type assertion, so a channel that
// does not implement it simply shows no indicator. Mirrors goclaw's
// channels.ActionSender.
type ActionSender interface {
    SendAction(ctx context.Context, chatID, kind string) error
}

// Inbound/Outbound mirror goclaw's channels.InboundMsg/OutboundMsg field for field
// (Channel, ChatID, SenderID, Sender, Text, Attachments, Timestamp for Inbound;
// Channel, ChatID, Text, Attachments for Outbound), so the host-side shim onto
// channels.ChannelAdapter is a trivial mapping.
```

## The channel runtime (pkg/plugin/serve_channel.go, package plugin)

`ServeChannel(ch Channel) error` is to a channel what `Serve` is to a tool: it owns
the protocol so the author writes only `Start`/`Send`. It runs over the SAME frame
protocol and StdioTransport; the wire format does not change, channels are just new
topics over the four fixed frame patterns.

```go
// ServeChannel runs the channel protocol over the plugin's stdin/stdout until the
// host sends shutdown or stdin closes. The handshake announces Kind=channel; then
// two concurrent pumps share the one Session.
func ServeChannel(ch Channel) error { return serveChannel(ch, ipc.StdioTransport{}) }
```

`serveChannel(ch, Transport)` outline:

1. Handshake: read the host's `hello`, verify magic/version, reply `hello.ok` with
   `Info{Kind: KindChannel, ...}` from `ch.Info()`. Same handshake helper as the tool
   runtime, generalized so the Kind is not hard-coded to tool.
2. `out, err := ch.Start(ctx)` with a cancellable ctx. On error, fail the handshake
   path / return.
3. Run two pumps concurrently over the one `Session` (writes serialized by the
   Session mutex, exactly as for tools):
   - INBOUND pump: `for in := range out { send a FrameEvent Topic="channel.inbound",
     payload = the Inbound as JSON, ID=0 (events are unsolicited, no reply) }`. When
     `out` closes, the inbound stream is done.
   - REQUEST pump: `for { ReadFrame; dispatch }`:
       - `FrameRequest Topic="channel.send"`: decode `Outbound`, call `ch.Send(ctx,
         out)` (in its own goroutine, panic-recovered), reply a correlated
         `FrameResult` (`{"ok":true}` or an IsError result on failure).
       - `FrameRequest Topic="channel.action"`: decode `{chat_id, kind}`; if `ch`
         implements `ActionSender`, call it; reply a correlated result (a no-op
         success if it does not implement it).
       - `FrameControl Topic="shutdown"`: cancel ctx (stops `Start`), return nil.
       - `FrameControl Topic="heartbeat"`: reply heartbeat, same as the tool runtime.
       - unknown request topic: correlated IsError result; unknown event/control:
         ignore. Never crash.
       - `io.EOF`: cancel ctx, return nil.
4. Like `Serve`, the stdio-backed `ServeChannel` prints the TTY hint when stdin is a
   terminal; `serveChannel(ch, Transport)` (the testable core) does not.

### channel.* topic payloads

- `channel.inbound` (FrameEvent, plugin -> host, no reply) -> `Inbound` as JSON.
- `channel.send` (FrameRequest, host -> plugin) -> `Outbound` as JSON; the reply is
  a FrameResult, payload `SendResult{OK bool; Error string}` (Error set on failure).
- `channel.action` (FrameRequest, host -> plugin) -> `Action{ChatID string; Kind
  string}`; reply a FrameResult `SendResult` (OK true even when the channel has no
  ActionSender, since an unknown action is a no-op, not an error).

These are new topics only; FrameType, the header, and ProtocolVer are unchanged.

### Inbound channel security (fail closed)

This applies only to a channel that accepts INBOUND connections (a listener). In
goclaw's deployment a plugin runs in the agent's container, so an inbound port is not
reachable from outside, which is exactly why the canonical channel (`goclaw-irc`) DIALS
OUT instead. Prefer a dial-out channel; the guidance below is for the inbound case
where it is unavoidable.

A channel that accepts INBOUND traffic from the outside world is an open door into
the agent: an inbound message becomes an agent prompt, so an unauthenticated source
lets anyone reach the agent (burning tokens, driving tool calls) and, worse, spoof
identity, since the host's access gate keys on `SenderID`. goclaw's rule is that
security code fails closed (unknown/malformed input is denied), so any inbound channel
plugin must too. Two principles for a channel author:

- AUTHENTICATE the transport. Require a shared secret (or stronger) on every inbound
  message; reject anything unauthenticated, and reject when no secret is configured
  (an unconfigured channel denies everything rather than defaulting open). Secrets
  come from the host env (a NAME in `plugin.yml`, the host supplies the value), never
  the manifest.
- NEVER TRUST AN ASSERTED IDENTITY verbatim. An inbound payload that claims who sent
  it must not set the access-gate `SenderID` directly; namespace it (so it cannot
  collide with another channel's owner id) or pin it to a configured value. The
  asserted name may be kept as a display-only field.

This is defense in depth: the plugin authenticates the transport and pins identity,
and the host still applies its own access gate on top. (In practice goclaw's deployment
favors dial-out channels, where this whole inbound concern does not arise.)

serve_channel_test.go: drive `serveChannel` over in-memory pipes. Handshake announces
Kind=channel; an Inbound pushed by a fake Channel's Start surfaces as a
channel.inbound event; a channel.send request reaches the fake's Send and yields a
correlated SendResult{OK:true}; a Send that errors yields SendResult with Error set;
a channel.action reaches an ActionSender (and is a no-op success without one);
shutdown cancels Start's ctx and serveChannel returns nil; a heartbeat is answered.

A short doc comment at the top of channel.go must say: tools use Serve, channels use
ServeChannel; both ride the same frames, channels just add the channel.* topics.

## Poll channels (pkg/plugin/serve_poll.go, package plugin)

Many channels do not hold a connection; their inbound comes from POLLING an upstream on
an interval (Gmail, an RSS feed, a status API). That `Start` is always the same shape:
tick, fetch, emit what is new. `ServePoll` owns that loop so the author implements only
`Poll` + `Send`, the way `ServeChannel` owns the framing so an author writes only
`Start` + `Send`.

`ServePoll(p Poller)` is a THIN ADAPTER over `ServeChannel`, not a parallel runtime: it
wraps the `Poller` in a `pollChannel` (whose `Start` runs the poll loop) and delegates
to `ServeChannel`. The handshake still announces `Kind=channel`, so the host cannot tell
a poll channel from a hand-written one, which is correct: it IS just a channel, with no
new wire protocol.

The `Poller` interface: `Info()`, `Interval() time.Duration` (asked once; non-positive
uses the 60s default; read it from env in the constructor), `Poll(ctx) ([]Inbound,
error)`, and `Send(ctx, Outbound) error` (same as `Channel.Send`).

The loop `ServePoll` runs:

- POLLS IMMEDIATELY, then spaces polls by `interval` measured from each poll's START (so
  poll cost does not compound the interval). A freshly-started plugin is live at once,
  not after a full interval.
- BACKS OFF on a `Poll` error (capped exponential, 1s base to 1m), resets to the base
  after any clean poll, and logs each retry. A failing upstream is not hammered.
- EMITS each returned `Inbound` up the `channel.inbound` event stream IN SLICE ORDER.
- YIELDS to ctx every iteration (so a poll that overruns the interval runs back-to-back
  but can never busy-spin), and unwinds promptly when `ServeChannel` cancels the ctx on
  shutdown/EOF.

DEDUP IS THE AUTHOR'S JOB, and this is the one thing to get right: `ServePoll` has NO
memory of what it emitted, so `Poll` must return only genuinely-new items or the agent
is flooded with repeats every interval. A source you can mutate dedups by mutating it,
e.g. Gmail queries `is:unread` and MARKS each returned message read, so the next query
excludes it. A source you cannot mutate (RSS) needs `Poll` to track seen ids itself.
(There is deliberately no SDK `SeenSet` helper yet; add one only when a second poll
plugin needs it.)

Author rule: for a channel whose inbound comes from polling, implement `Poller` and call
`ServePoll`; you write only `Poll` and `Send`.

> **Direction note (planned, not yet built).** `Poller` has a `Send`, so it models a poll
> source that ALSO has a reply path (Gmail: you reply by email). A one-way poll SOURCE,
> a feed you read but cannot reply to (RSS, a status page, a "new event" signal), has no
> reply path, so a forced no-op `Send` is the wrong shape. The planned fix is a separate
> `Source` kind: `Info` + `Interval` + `Poll` only (NO `Send`), run by a `ServeSource`
> that reuses this same poll loop. A Source PRODUCES signals; routing them to a session
> (and any human-visible relay) is the host's job, not the plugin's. Until `Source` lands,
> `Poller` is the only poll abstraction, and the RSS example above is transitional, a true
> RSS plugin will be a `Source`, not a `Poller`.

## Making external HTTPS calls (pkg/plugin/http.go)

A tool or channel that calls an external HTTPS API (Gmail, a weather API, any poll
channel) must route through goclaw's credential proxy: the proxy injects the real
credential on the way out, so the plugin sends NO auth header and never holds a token.
goclaw sets the container env that makes this work (`HTTPS_PROXY`/`NO_PROXY` and
`SSL_CERT_FILE` -> the proxy CA). The plugin just makes a plain
`GET https://api.example.com/...` with no auth; the proxy terminates TLS with a leaf
it minted (trusted via the CA above) and forwards upstream over real TLS.

The footgun: Go's `http.DefaultClient` already honors that env, so it works, but an
author who builds a custom `Transport` for timeouts/retries and forgets
`Proxy: http.ProxyFromEnvironment` (or overrides `TLSClientConfig.RootCAs`) silently
BYPASSES the proxy: the request goes direct with no injected auth and fails with an
opaque 401 or TLS error.

So the SDK provides the correct client as a one-call default:

- `plugin.HTTPClient()` -> an `*http.Client` (30s timeout) configured by construction:
  `Proxy: http.ProxyFromEnvironment`, and RootCAs = the SYSTEM roots PLUS the proxy CA
  from `SSL_CERT_FILE`.
- `plugin.HTTPClientTimeout(d)` -> the same with a chosen timeout.

Rule for authors: **for any external HTTPS call, use `plugin.HTTPClient()`; do not
hand-roll a Transport.** Wrap it for retries if needed, but keep the proxy + CA wiring.

Two correctness points the helper bakes in: (1) it starts from the system roots and
APPENDS the proxy CA, never replaces, because a plugin may also hit hosts with no
stored credential, which the proxy blind-tunnels unintercepted, so those present their
REAL public cert and must validate against system roots; (2) when `SSL_CERT_FILE` is
unset (proxy off / dev mode) it leaves `RootCAs` nil so the system roots are used
unchanged, so the same code is correct in both modes with no branching. The helper has
NO OAuth or auth logic: credential injection lives entirely host-side in goclaw.

## The worked demos (goclaw-roll, goclaw-irc)

Reference plugins exercise the SDK end to end. They are their OWN repos (each a real,
installable single-plugin repo, the plugin IS the command), with their own
`plugin.yml`, `-selftest`, and an end-to-end wire test. The build/run/register details
live in each plugin's README, not here, so this spec stays the general SDK reference.

- **[`goclaw-roll`](https://github.com/shindakun/goclaw-roll)** — the worked TOOL demo:
  a dice roller (NdM notation), the smallest thing that exercises typed args, input
  validation, and a returned result.
- **[`goclaw-irc`](https://github.com/shindakun/goclaw-irc)** — the worked CHANNEL demo
  and the canonical one: a minimal IRC bridge. It DIALS OUT to an IRC server over TLS
  (stdlib only, no IRC library), joins a channel, forwards messages that mention the bot
  or are sent to it directly up to the agent as `Inbound`, and posts replies back as
  `Outbound`. This is the right shape for a goclaw channel: the bot opens ONE outbound
  connection, so there is no inbound listener and no open port. It owns
  reconnect-with-backoff and defers owner authorization to goclaw's access gate (IRC
  nicks are spoofable without SASL, a documented caveat).

## Plugin manifest (plugin.yml): the at-rest, pre-launch description

The host does NOT use a single central manifest. It walks a `plugins/` directory
where each plugin is one subdir shipping a declarative `plugin.yml`, which the host
reads BEFORE launching to learn the plugin's kind, version, the env var NAMES it
needs, and any slash command it registers. The runtime `hello` handshake remains the
source of truth for the live tool list; `plugin.yml` is the at-rest description used
before the process starts. The host parses it, so there is no Go code for it in the
SDK, but the SDK is the natural home for the schema (each demo repo ships its own
`plugin.yml` as a worked example).

Schema:

```yaml
name: roll                 # stable id; MUST match Info.Name from the handshake
kind: tool                 # "tool" or "channel"; MUST match Info.Kind
version: "1.0.0"           # semver, bumped on change; MUST match Info.Version
author: shindakun          # plugin author (free-form); shown in plugin listings
git: https://github.com/shindakun/goclaw-roll  # source repo; /plugin add builds from here
exec: roll                 # built binary, relative to this plugin dir
description: Roll dice in NdM notation (e.g. 2d6).
command: roll              # registers the /roll slash command; omit for no command
env: []                    # env var NAMES the plugin needs; host supplies VALUES
```

Rules:

- `name`/`kind`/`version` MUST agree with what the plugin reports in its handshake
  `Info`. Keep them in sync (for roll: `roll` / `tool` / `1.0.0`).
- `version` MUST be **semver** (`MAJOR.MINOR.PATCH`, no `v` prefix; the `v` belongs on
  a release tag, not in the manifest) and MUST be bumped on any behavior change: PATCH
  for a fix, MINOR for a backward-compatible capability, MAJOR for a breaking change to
  the plugin's tools/inputs/behavior. A version that does not move across a real change
  is a BUG: it makes the manifest-version update signal lie. (See
  `plugin-versioning-and-releases-handoff.md` for the release-tag and update story.)
- `author` and `git` are at-rest provenance for plugin listings. They are NOT part of
  the handshake `Info` and the SDK does not read them; the host shows `author` in
  `/plugin list`-style output, and `git` is the source URL `/plugin add` builds or
  pulls from (and what `/plugin update` re-fetches).
- `env` lists NAMES only; the host supplies values from its own config at launch.
  NEVER put a secret value in `plugin.yml`.
- Enable/disable is HOST-owned state kept OUT of `plugin.yml` (a host sidecar), so
  the host never rewrites the author's file. The SDK/plugin does not deal with
  enable state.

### Optional `oauth:` block (plugin-declared OAuth2 provider)

A plugin that talks to an OAuth2 upstream (Gmail, Microsoft Graph, Spotify, ...) can
DECLARE the provider in an `oauth:` block. The host (goclaw) owns the OAuth MECHANISM
(the `goclaw auth add-oauth --plugin <name>` consent flow, code exchange, encrypted
storage, refresh/rotation, and proxy injection); the plugin owns the provider FACTS.
This is what lets a NEW OAuth service ship entirely as a plugin, with no host change.

Everything in the block is provider CONFIG, never a secret: the operator still
supplies the client id/secret at `add-oauth` time; they are never stored in the
manifest. The plugin itself still holds no token (target mode): it makes plain HTTPS
calls and the host's credential proxy injects the Bearer (see "Making external HTTPS
calls"). The `oauth:` block is only consumed by `add-oauth`, not at plugin launch.

```yaml
oauth:
  provider: google                                    # label for messages (free-form)
  auth_url: https://accounts.google.com/o/oauth2/v2/auth   # consent endpoint
  token_url: https://oauth2.googleapis.com/token           # token + refresh endpoint
  target_host: gmail.googleapis.com                   # API host this credential authenticates
  scopes:                                             # scopes this plugin needs
    - https://www.googleapis.com/auth/gmail.modify
  auth_params:                                        # provider params to FORCE a refresh token
    access_type: offline                              #   (Google; omit for providers that
    prompt: consent                                   #    always issue one or use a scope)
  scope_separator: " "                                # how scopes join: " " (default) or ","
  client_auth: body                                   # client creds at token endpoint:
                                                      #   body (default) or basic (HTTP Basic)
```

Field notes (each is a provider FACT, look it up in the provider's OAuth2 docs):

- `auth_url` / `token_url` / `target_host` are required; `auth_url`, `token_url` and
  `https://<target_host>` MUST be https (the host rejects anything else).
- `auth_params` is how you force a long-lived refresh token. It varies by provider:
  Google uses `access_type: offline` + `prompt: consent`; Microsoft instead adds an
  `offline_access` SCOPE and needs no params; some providers always issue one (omit
  the block). If consent returns no refresh token, this is usually what is wrong.
- `scope_separator` is " " for most providers, "," for a few. `client_auth` is `body`
  (client id/secret as form fields, e.g. Google) or `basic` (HTTP Basic header, e.g.
  Spotify, Reddit).
- The operator can override any of `--auth-url` / `--token-url` / `--target-api-url`
  / `--scopes` at `add-oauth` time (e.g. for a self-hosted instance), so declare the
  common case and let overrides handle the edges.

## Releasing a plugin (version, tags, install pins)

A plugin is distributed as a git repo, there is no plugin registry. An operator installs
it with `/plugin add <git-url>`; goclaw clones, scans, builds, and stages it. Releasing is
how an author marks a point in that repo as a blessed version, and how an operator pins to
it. This is a deliberate author action (like goclaw never auto-applying an update); the SDK
defines only the CONVENTION.

### The version field is the at-rest signal

`plugin.yml` `version` is semver, bumped on every behavior change (see the manifest schema
above). goclaw's update check (`docs/plugin-updates.md`) compares the installed `version`
against the upstream `version` at the checked ref: a bump is a deliberate "new release"
signal. A version that does not move across a real change makes that signal lie, so the
bump is mandatory, not optional.

The manifest `version` MUST agree with the version the binary reports (its
`Info.Version`). The SDK makes that checkable with `plugin.HandleVersionFlag(version)`:
a plugin's `main` calls it at the very TOP, before its own `flag.Parse()`, and then
`./<plugin> -version` (or `--version`) prints the bare semver and exits 0.

```go
func main() {
    plugin.HandleVersionFlag(version) // MUST be before flag.Parse()
    selftest := flag.Bool("selftest", false, "...")
    flag.Parse()
    // ...
}
```

```sh
./roll -version   # -> 1.0.0
```

This is the one line the author wires; it can NOT live inside `Serve`/`ServeChannel`/
`ServePoll`, because the plugin's own `flag.Parse()` runs first and would reject the
unknown `-version` flag before the runtime is reached. So call `HandleVersionFlag` ahead
of any flag parsing.

Before tagging a release you can then confirm the three agree, build the binary, run
`./<name> -version`, and check it equals `plugin.yml` `version` and the tag you are about
to cut (the reference Makefile's `release` target does exactly this). Today that is a
manual (or `make release`) check; there is no standalone release tool in goclawkit.

### Release tags are the blessed signal: `v<semver>`

A release is a semver git tag of the form **`v<semver>`** (e.g. `v1.3.0`), one tag line per
repo. The `v` lives on the tag; `plugin.yml` holds the bare semver (`1.3.0`), and the two
MUST correspond, a `v1.3.0` tag means `version: "1.3.0"` in the manifest at that commit.

A tag is the STRONGEST update signal: it is explicit, immutable, and pins exactly what an
update would install (a tag, not a moving branch). goclaw's check prefers the latest semver
tag (`git ls-remote --tags`, highest semver) and falls back to the manifest `version` for a
repo with no tags. There is NO commit-drift signal: an author who publishes neither a tag
nor a version bump simply advertises no update (rather than every README edit reading as
"update available").

One plugin per repo, so a bare `v<semver>` is unambiguous, the tag names the repo's single
plugin. (A repo that ships several plugins from one `go.mod` would need a per-plugin tag
prefix to disambiguate; that is out of scope here, the convention is one plugin per repo.)

### Install-by-ref notation

An operator may pin the install/update to a specific ref:

```text
/plugin add <git-url>@<ref>          # one plugin at the repo root
/plugin add <git-url>#<subdir>@<ref> # a subdir within the repo
```

- `<ref>` is a release tag (`v1.3.0`) or a raw commit sha (to pin without a release).
- No `@<ref>` means the default branch HEAD (today's behavior), which gets only the WEAKER
  manifest-version signal and should be discouraged in favor of a tag.
- `/plugin update <name>` re-installs at the newer tag the check reported, through the full
  sandbox (an update is re-vetted untrusted code, not a fast path).

Author guidance: cut a `v<semver>` tag per release and keep `plugin.yml` `version` in step,
so the plugin gets precise tag-based update checks. Steer operators to install by tag.

### CHANGELOG.md (recommended, not required)

A plugin SHOULD keep a `CHANGELOG.md` with a section per version (Keep a Changelog style is
fine), so goclaw's update check can show WHAT changed, not just that something did. It is
recommended, not enforced, the version bump and tag are the load-bearing signals.

### A reference Makefile

[`docs/plugin.Makefile`](plugin.Makefile) is a copy-this-into-your-repo Makefile that
makes the above one-command and drift-proof. It is self-contained (no goclawkit dependency
at release time); set `NAME` to your plugin and you get:

- `make bump VERSION=x.y.z` — edit `plugin.yml` `version` and commit it.
- `make release` — tag `v<version>` for the version ALREADY in `plugin.yml` (no bump),
  AFTER building the binary and asserting `./<name> -version` equals the manifest (so the
  manifest, the code, and the tag cannot drift). `PUSH=1` also pushes the tag.

A repo whose `plugin.yml` is already at `1.0.0` but never tagged just runs `make release`
to cut `v1.0.0`, no bump. The example plugin repos each carry a copy.

## Two ways a tool is triggered (agent invoke and slash command)

A plugin's tool is reachable by two host paths, and BOTH end at the exact same
`tool.invoke` request frame the SDK already handles. The SDK does not change to
support this; design tool args with both in mind:

- Agent-invoked (the LLM calls the tool): args arrive as the JSON the model built,
  matching the tool's `InputSchema`.
- User slash command (a person types `/roll 2d6`): the host maps the command's
  argument STRING to the tool's args before sending `tool.invoke`. For a tool whose
  input is a single string field, the host passes the raw remainder as that field;
  richer schemas need a host convention to parse the line.

Guidance: a tool intended to ALSO be a slash command should keep a simple input,
ideally a single string field (or a shape the host can fill from one argument
string). roll's input is `{ "notation": "2d6" }`, a single string field, so
`/roll 2d6` maps cleanly: that is why roll works as both an agent tool and a `/roll`
command.

The plugin advertises its slash command in `plugin.yml` (`command:`), not in the
handshake `Info`. v1 deliberately does NOT add a `Command` field to `Info`/`ToolInfo`:
the wire format and handshake stay frozen, and `plugin.yml` is the authoritative
pre-launch source for the command. (A future version could add an optional handshake
field if the live and at-rest views ever need to agree, but the host does not need
it for v1.)

## A plugin MUST be a Linux binary (it runs in the container)

This is a hard requirement, not a preference. goclaw launches plugins INSIDE the
agent's Linux container (the in-container runner is the launcher), so a plugin must
be compiled for the container's OS/arch, NOT the author's machine. A macOS or
Windows build will fail at launch with `exec format error` and the plugin will not
load. Build for Linux:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o <name> .
```

Use `GOARCH=arm64` if the goclaw host runs an arm64 container engine. A plain
`go build` produces a binary for the author's platform and is fine ONLY for
`-selftest`/local development; it will not run inside goclaw unless that platform is
linux. Pure-Go plugins (stdlib only, as the SDK core is) cross-compile cleanly with
`CGO_ENABLED=0`. The staged install is the built binary plus its `plugin.yml` in one
directory, the per-plugin layout the host walks.

## Notes for the host side (goclaw), not built here

The goclaw side (separate work, tracked in goclaw's `docs/plugins-design.md`): walks a
`plugins/` directory (the directory IS the registry, there is no central
`plugins.yaml`), reads each subdir's `plugin.yml`, launches the binary with
`exec.CommandContext`, performs the hello handshake over the Layer 1 frame protocol
(the plugin's stdin/stdout), and registers a tool plugin's advertised tools (and any
slash command) so the agent or a user can call them; an `fsnotify` watch on the
directory gives hot add/remove by diffing desired-vs-running and launching/stopping
processes. Tokens a plugin needs are passed in its environment by the host (never
written into the manifest); the credential proxy fronts an OAuth credential. None of
that is built in goclawkit; goclawkit is only the plugin-author SDK plus the shared
protocol.

Layer 2 (the `pkg/ipc`-style socket coordination bus) is out of scope and unbuilt. It
is recorded here only so the frame format and the `Transport` interface stay compatible
with it: when goclaw later needs plugin-to-plugin or plugin-to-host coordination, it
adds a Dial/Listen-based `Transport` carrying these same frames, plus a small host-side
hub/registry (mirroring godoorkit's `pkg/ipc`). No wire-format change, no change to
Layer 1.
