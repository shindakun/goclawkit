# roll: a worked goclaw plugin

`roll` is the reference goclaw plugin: a dice-roller **tool**. It is the worked demo
for [goclawkit](../../README.md), exercising typed args, input validation, and a
returned result over the plugin protocol.

The plugin IS the command (mirroring godoorkit's `cmd/<name>-door`): the goclaw host
launches this binary and speaks the framed stdio protocol to it. `main()` just wires
the tool and calls `plugin.ServeTool`.

## Build

```sh
go build -o roll ./cmd/roll
```

## Run it standalone (`-selftest`)

`-selftest` invokes the tool once locally and prints the result, with no host
needed. This is the parallel to a godoorkit door's `-local` flag: a quick hands-on
sanity check while developing.

```sh
./roll -selftest                 # 2d6 -> [4, 5] = 9
./roll -selftest -notation 3d8   # 3d8 -> [5, 4, 1] = 10
./roll -selftest -notation d20   # d20 -> [7] = 7   (count defaults to 1)
./roll -selftest -notation junk  # prints an error to stderr, exits non-zero
```

The tool accepts NdM notation: `N` dice (1..100, default 1) of `M` sides (2..1000).

## Two ways to trigger it: agent tool and `/roll`

roll is reachable two ways, and both end at the same `tool.invoke` frame the plugin
already handles:

- **Agent tool:** the LLM calls `roll` with JSON matching its `InputSchema`, e.g.
  `{ "notation": "2d6" }`.
- **Slash command:** a person types `/roll 2d6`. The host maps the argument string to
  the tool's args before sending `tool.invoke`.

The slash path works cleanly *because* roll's input is a single string field
(`notation`): the host fills it from the raw argument string (`2d6`) with no parsing
convention needed. A tool meant to be both an agent tool and a slash command should
keep this kind of simple, single-string input.

## Register it in goclaw

The host walks a `plugins/` directory; each plugin is one subdir shipping a
[`plugin.yml`](plugin.yml) the host reads before launching. roll's:

```yaml
name: roll
kind: tool
version: "1.0.0"
author: shindakun
url: https://github.com/shindakun/goclawkit
exec: roll                 # the binary built above
description: Roll dice in NdM notation (e.g. 2d6).
command: roll              # registers the /roll slash command
env: []                    # names only; the host supplies any values at launch
```

`name`/`kind`/`version` must match what the plugin reports in its `hello` handshake
`Info`. The host then launches the binary, does the handshake, reads the live tool
list, and registers `roll` so the agent can call it (and `/roll` so a person can).
Secrets are never written here: `env` lists variable names and the host supplies the
values. See goclaw's `docs/plugins-design.md` for the host side (the manager, the
directory watch, and hot add/reload).

## Talk to it by hand (the wire smoke test)

Frames are length-prefixed binary, not lines you can type, so the "talk to it by
hand" check is automated in [`main_test.go`](main_test.go): `TestWireProtocolEndToEnd`
builds this binary, launches it, writes a real `hello` control frame then a
`tool.invoke` request to its stdin, and asserts a `hello.ok` (with the right `Info`)
then a correlated result come back on its stdout. It proves the binary protocol
works over actual OS pipes, which `-selftest` (a direct in-process call) does not
exercise.

```sh
go test ./cmd/roll -run TestWireProtocolEndToEnd -v
```
