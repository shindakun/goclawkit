// Package ipc is the shared wire protocol for goclaw plugins: a length-prefixed
// binary frame format carried over a Transport (the plugin's stdin/stdout on Layer
// 1). It is the parallel to godoorkit's pkg/ipc, borrowing that design: a small
// fixed set of frame PATTERNS (a binary header) plus an open-ended set of FEATURES
// carried as a Topic string and an opaque JSON payload. Adding a capability later
// means a new topic, never a new frame type or a format bump, so the wire format
// freezes early.
//
// Both the goclaw host and a plugin (via package plugin) speak these frames.
package ipc

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
)

// Protocol constants. The header is frozen at ProtocolVer 1; features extend by
// Topic, not by changing these. Bump ProtocolVer ONLY for a breaking header change;
// new topics and payload shapes are NOT breaking changes.
const (
	Magic       = "GCLW"
	ProtocolVer = 1
	MaxTopicLen = 255
	MaxPayload  = 8 * 1024 * 1024 // 8 MiB; reject larger frames
)

var (
	ErrInvalidMagic    = errors.New("goclawkit/ipc: invalid magic")
	ErrUnsupportedVer  = errors.New("goclawkit/ipc: unsupported protocol version")
	ErrTopicTooLong    = errors.New("goclawkit/ipc: topic too long")
	ErrPayloadTooLarge = errors.New("goclawkit/ipc: payload too large")
)

// FrameType is the small fixed set of message PATTERNS. Features live in Topic, not
// here; this vocabulary is meant to stay exactly four.
type FrameType uint8

const (
	FrameControl FrameType = 0 // handshake, shutdown, heartbeat: Topic names which
	FrameRequest FrameType = 1 // a request expecting a Result (correlated by ID)
	FrameResult  FrameType = 2 // the reply to a Request (same ID)
	FrameEvent   FrameType = 3 // a one-way push (no reply), e.g. a channel inbound msg
)

// Frame is one wire message. The header carries no Go types; Payload is opaque
// bytes (JSON, decoded according to Topic by the receiver).
//
// Wire format (all integers big-endian):
//
//	+-----------------------------------------------+
//	| Magic:    "GCLW" (4 bytes)                    |
//	| Version:  uint8  (protocol version, = 1)      |
//	| Type:     uint8  (frame pattern)              |
//	| Flags:    uint8  (reserved, 0 for now)        |
//	| ID:       uint64 (correlates request->result) |
//	| TopicLen: uint16                              |
//	| Topic:    string (TopicLen bytes, UTF-8)      |
//	| PayLen:   uint32                              |
//	| Payload:  bytes  (PayLen bytes, opaque JSON)  |
//	+-----------------------------------------------+
type Frame struct {
	Type    FrameType
	Flags   uint8  // reserved, 0 for now
	ID      uint64 // correlates a Request to its Result; 0 for unsolicited frames
	Topic   string
	Payload []byte
}

// headerLen is the fixed-size prefix: magic(4)+ver(1)+type(1)+flags(1)+id(8)+topicLen(2).
const headerLen = 17

// Transport is the byte stream a Session reads frames from and writes frames to.
// Layer 1 uses StdioTransport (the plugin's stdin/stdout). A later Layer 2 socket
// bus would provide a Dial/Listen-based Transport carrying the same frames, so it
// is purely additive: a new Transport, not a new wire format.
type Transport interface {
	io.Reader
	io.Writer
}

// StdioTransport reads os.Stdin and writes os.Stdout. The plugin's Layer 1 default.
// stderr is reserved for the plugin's own logs (the host captures them); a plugin
// must never write anything but frames to stdout.
type StdioTransport struct{}

func (StdioTransport) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (StdioTransport) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// WriteFrame validates the caps and writes f's header (big-endian) followed by its
// topic and payload. It does not flush or lock; callers that share a writer must
// serialize WriteFrame calls themselves (Session does this).
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Topic) > MaxTopicLen {
		return ErrTopicTooLong
	}
	if len(f.Payload) > MaxPayload {
		return ErrPayloadTooLarge
	}

	var hdr [headerLen]byte
	copy(hdr[0:4], Magic)
	hdr[4] = ProtocolVer
	hdr[5] = byte(f.Type)
	hdr[6] = f.Flags
	binary.BigEndian.PutUint64(hdr[7:15], f.ID)
	binary.BigEndian.PutUint16(hdr[15:17], uint16(len(f.Topic)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, f.Topic); err != nil {
		return err
	}

	var plen [4]byte
	binary.BigEndian.PutUint32(plen[:], uint32(len(f.Payload)))
	if _, err := w.Write(plen[:]); err != nil {
		return err
	}
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads one frame from r, verifying magic and version and enforcing
// MaxTopicLen/MaxPayload. It uses io.ReadFull so a frame split across multiple
// underlying reads is reassembled; because every length is explicit there is no
// line-length limit (this sidesteps the bufio.Scanner 64KB trap by construction).
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	if string(hdr[0:4]) != Magic {
		return Frame{}, ErrInvalidMagic
	}
	if hdr[4] != ProtocolVer {
		return Frame{}, ErrUnsupportedVer
	}

	f := Frame{
		Type:  FrameType(hdr[5]),
		Flags: hdr[6],
		ID:    binary.BigEndian.Uint64(hdr[7:15]),
	}
	topicLen := binary.BigEndian.Uint16(hdr[15:17])
	// topicLen is a uint16 but MaxTopicLen is 255, so a bad peer could declare a
	// longer topic; reject it rather than allocating for it.
	if int(topicLen) > MaxTopicLen {
		return Frame{}, ErrTopicTooLong
	}
	if topicLen > 0 {
		topic := make([]byte, topicLen)
		if _, err := io.ReadFull(r, topic); err != nil {
			return Frame{}, err
		}
		f.Topic = string(topic)
	}

	var plen [4]byte
	if _, err := io.ReadFull(r, plen[:]); err != nil {
		return Frame{}, err
	}
	payLen := binary.BigEndian.Uint32(plen[:])
	if payLen > MaxPayload {
		return Frame{}, ErrPayloadTooLarge
	}
	if payLen > 0 {
		payload := make([]byte, payLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
		f.Payload = payload
	}
	return f, nil
}

// Marshal is a thin JSON helper so payload-building call sites stay terse.
func Marshal(v any) ([]byte, error) { return json.Marshal(v) }

// Unmarshal is a thin JSON helper so payload-reading call sites stay terse.
func Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Session wraps a Transport with a write mutex (frames must never interleave on the
// wire) and a buffered reader. Send is safe to call from multiple goroutines; Recv
// is expected to be called from a single read loop.
type Session struct {
	t   Transport
	r   *bufio.Reader
	wmu sync.Mutex
}

// NewSession wraps a Transport for framed send/receive.
func NewSession(t Transport) *Session {
	return &Session{t: t, r: bufio.NewReader(t)}
}

// Send writes one frame, holding the write mutex so concurrent senders never
// interleave bytes from two frames.
func (s *Session) Send(f Frame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return WriteFrame(s.t, f)
}

// Recv reads the next frame from the buffered reader. It returns io.EOF when the
// transport closes (e.g. the host died or stdin closed).
func (s *Session) Recv() (Frame, error) {
	return ReadFrame(s.r)
}
