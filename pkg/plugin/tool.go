package plugin

import (
	"context"
	"encoding/json"
)

// ToolInfo describes one tool to the host (and through it, the agent), so the host
// can present the tool without invoking anything.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"` // shown to the agent so it knows when to call this
	// InputSchema is a JSON Schema (as raw JSON) describing the tool's args, so the
	// agent/host can validate before invoking. Keep it simple; a tool that takes no
	// args may return an empty object schema.
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// Tool is one callable capability. A single plugin process may expose several tools
// (see ToolSet), but the simplest plugin exposes exactly one.
//
// A minimal implementation, the whole of what an author writes (Serve owns the
// protocol):
//
//	type echoTool struct{}
//
//	func (echoTool) Info() plugin.ToolInfo {
//	    return plugin.ToolInfo{
//	        Name:        "echo",
//	        Description: "Echo the supplied text back.",
//	        InputSchema: json.RawMessage(`{"type":"object",` +
//	            `"properties":{"text":{"type":"string"}},"required":["text"]}`),
//	    }
//	}
//
//	func (echoTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
//	    var in struct{ Text string `json:"text"` }
//	    if err := json.Unmarshal(args, &in); err != nil {
//	        return "", err // becomes an error result the agent sees
//	    }
//	    return in.Text, nil
//	}
//
//	func main() { plugin.ServeTool(echoTool{}, "echo", "1.0.0") }
type Tool interface {
	// Info returns the tool's name, description, and input JSON Schema.
	Info() ToolInfo
	// Invoke runs the tool. args is the raw JSON the agent supplied (validated
	// against InputSchema host-side, but re-validate what matters). Return the
	// result text; return a non-nil error to signal failure (the host maps it to an
	// error result the agent sees).
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolSet is what a plugin hands to Serve: one or more tools under a single plugin
// Info. For a one-tool plugin, ServeTool constructs this from a single Tool.
type ToolSet struct {
	Name    string // plugin name (Info.Name)
	Version string
	Tools   []Tool
}
