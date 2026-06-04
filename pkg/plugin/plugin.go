// Package plugin is the author-facing SDK for goclaw plugins. A plugin author
// implements one small interface (Tool) and calls Serve (or ServeTool); this
// package owns the wire handshake, framing, concurrency, and panic recovery, so the
// author writes only the interesting part.
//
// This is the parallel to godoorkit's pkg/door: there a door implements a tight
// Init/Run/Cleanup/Info contract and the framework owns terminal state and
// lifecycle; here a tool implements Info/Invoke and Serve owns the protocol. The
// plugin is the command: its main() imports this package and calls Serve, exactly
// as a godoorkit door's cmd/<name>-door binary calls into pkg/door.
//
// The shared wire protocol lives in package ipc; this package builds on it.
package plugin

import "github.com/shindakun/goclawkit/pkg/ipc"

// Kind names the plugin shape so the host can dispatch on it. Tools ship first;
// channels reuse the same process model, manifest, and manager later.
type Kind string

const (
	KindTool    Kind = "tool"
	KindChannel Kind = "channel" // LATER
)

// Info is the identity a plugin announces in the handshake (the hello.ok payload).
// The host reads it to learn the plugin's kind, version, and (for a tool plugin)
// the tools it exposes, without invoking anything.
type Info struct {
	Name        string `json:"name"` // stable id, e.g. "roll"
	Kind        Kind   `json:"kind"`
	Version     string `json:"version"`      // the plugin's own version, free-form
	ProtocolVer int    `json:"protocol_ver"` // must equal ipc.ProtocolVer
	// Tools advertises the tool names + descriptions this plugin exposes, so the
	// host can present them to the agent without invoking anything. Empty for a
	// channel plugin.
	Tools []ToolInfo `json:"tools,omitempty"`
}

// ProtocolVersion is ipc.ProtocolVer, re-exported so plugin authors and the host
// can reference the handshake version without importing package ipc directly.
const ProtocolVersion = ipc.ProtocolVer
