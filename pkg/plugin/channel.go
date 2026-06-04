package plugin

import (
	"context"
	"time"
)

// channel.go is the channel contract. Tools use Serve; channels use ServeChannel
// (serve_channel.go). Both ride the SAME frame protocol (package ipc); channels just
// add the reserved channel.* topics (channel.inbound as a one-way FrameEvent,
// channel.send and channel.action as FrameRequests), so they are additive, no
// wire-format change. The shape mirrors goclaw's channels.ChannelAdapter so a plugin
// channel is indistinguishable to the host router once the host-side shim lands.

// Channel is a long-lived, bidirectional plugin: it streams inbound messages up (as
// channel.inbound events) while accepting outbound sends concurrently (as
// channel.send requests), for the life of the host. Run it with ServeChannel.
type Channel interface {
	Info() Info
	// Start connects/listens and streams normalized inbound messages until ctx is
	// cancelled (ServeChannel cancels ctx on shutdown). The implementation owns
	// reconnect/backoff; return an error only for an unrecoverable setup failure.
	Start(ctx context.Context) (<-chan Inbound, error)
	// Send delivers one outbound message. It is called concurrently with the inbound
	// stream, so the implementation must be safe for that.
	Send(ctx context.Context, out Outbound) error
}

// ActionSender is an OPTIONAL channel capability: a transient chat action (e.g.
// "typing"). ServeChannel checks for it with a type assertion, so a channel that
// does not implement it simply shows no indicator. Mirrors goclaw's
// channels.ActionSender.
type ActionSender interface {
	SendAction(ctx context.Context, chatID, kind string) error
}

// Inbound mirrors goclaw's channels.InboundMsg field for field so the host-side
// shim is a trivial mapping. (Raw is intentionally omitted: it is a host-side
// debugging field, not something a plugin channel needs to populate over the wire.)
type Inbound struct {
	Channel     string       `json:"channel"`   // e.g. "telegram"
	ChatID      string       `json:"chat_id"`   // channel-native conversation id
	SenderID    string       `json:"sender_id"` // channel-native, STABLE user id
	Sender      string       `json:"sender"`    // display name (best-effort)
	Text        string       `json:"text"`      // message body
	Attachments []Attachment `json:"attachments,omitempty"`
	Timestamp   time.Time    `json:"timestamp"`
}

// Outbound mirrors goclaw's channels.OutboundMsg field for field.
type Outbound struct {
	Channel     string       `json:"channel"`
	ChatID      string       `json:"chat_id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is a file or image carried by a message, mirroring
// goclaw's channels.Attachment.
type Attachment struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	URL      string `json:"url"`             // channel-hosted URL, or a local path once downloaded
	Bytes    []byte `json:"bytes,omitempty"` // populated only when inlined
}
