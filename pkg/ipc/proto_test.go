package ipc

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// frameEqual compares two frames field for field, treating a nil and an empty
// payload as equal (a round-tripped empty payload reads back as nil).
func frameEqual(a, b Frame) bool {
	return a.Type == b.Type &&
		a.Flags == b.Flags &&
		a.ID == b.ID &&
		a.Topic == b.Topic &&
		bytes.Equal(a.Payload, b.Payload)
}

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		f    Frame
	}{
		{"control hello", Frame{Type: FrameControl, ID: 0, Topic: "hello", Payload: []byte(`{"magic":"GCLW","protocol_ver":1}`)}},
		{"request invoke", Frame{Type: FrameRequest, ID: 42, Topic: "tool.invoke", Payload: []byte(`{"tool":"roll","args":{"notation":"2d6"}}`)}},
		{"result", Frame{Type: FrameResult, ID: 42, Topic: "tool.invoke", Payload: []byte(`{"text":"2d6 -> [4, 5] = 9","is_error":false}`)}},
		{"event", Frame{Type: FrameEvent, ID: 7, Topic: "channel.inbound", Payload: []byte(`{"text":"hi"}`)}},
		{"flags set", Frame{Type: FrameControl, Flags: 0xAB, ID: 1, Topic: "heartbeat", Payload: nil}},
		{"empty topic and payload", Frame{Type: FrameControl, ID: 0, Topic: "", Payload: nil}},
		{"max-length topic", Frame{Type: FrameRequest, ID: 99, Topic: string(bytes.Repeat([]byte("a"), MaxTopicLen)), Payload: []byte("x")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tc.f); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			got, err := ReadFrame(&buf)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if !frameEqual(got, tc.f) {
				t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, tc.f)
			}
		})
	}
}

func TestReadFrameRejectsBadMagic(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Type: FrameControl, Topic: "hello"}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	b[0] = 'X' // corrupt the magic
	if _, err := ReadFrame(bytes.NewReader(b)); !errors.Is(err, ErrInvalidMagic) {
		t.Fatalf("got %v, want ErrInvalidMagic", err)
	}
}

func TestReadFrameRejectsBadVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Type: FrameControl, Topic: "hello"}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	b[4] = 99 // corrupt the version byte
	if _, err := ReadFrame(bytes.NewReader(b)); !errors.Is(err, ErrUnsupportedVer) {
		t.Fatalf("got %v, want ErrUnsupportedVer", err)
	}
}

func TestWriteFrameRejectsOverLongTopic(t *testing.T) {
	f := Frame{Type: FrameRequest, Topic: string(bytes.Repeat([]byte("a"), MaxTopicLen+1))}
	if err := WriteFrame(io.Discard, f); !errors.Is(err, ErrTopicTooLong) {
		t.Fatalf("got %v, want ErrTopicTooLong", err)
	}
}

func TestWriteFrameRejectsOverCapPayload(t *testing.T) {
	f := Frame{Type: FrameRequest, Topic: "tool.invoke", Payload: make([]byte, MaxPayload+1)}
	if err := WriteFrame(io.Discard, f); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v, want ErrPayloadTooLarge", err)
	}
}

// TestReadFrameRejectsOverCapPayload hand-builds a frame whose header DECLARES a
// payload larger than MaxPayload, to prove ReadFrame rejects it without allocating,
// even though WriteFrame would never emit such a frame.
func TestReadFrameRejectsOverCapPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Type: FrameRequest, Topic: "x", Payload: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// The payload-length uint32 sits right after the header + 1-byte topic.
	off := headerLen + 1
	b[off], b[off+1], b[off+2], b[off+3] = 0xFF, 0xFF, 0xFF, 0xFF
	if _, err := ReadFrame(bytes.NewReader(b)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v, want ErrPayloadTooLarge", err)
	}
}

// TestReadFrameRejectsOverLongTopic hand-builds a header declaring a topic longer
// than MaxTopicLen, proving ReadFrame guards the topic length too.
func TestReadFrameRejectsOverLongTopic(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Type: FrameRequest, Topic: "x", Payload: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// The topic-length uint16 is the last two header bytes.
	b[15], b[16] = 0x01, 0x00 // 256, just over MaxTopicLen (255)
	if _, err := ReadFrame(bytes.NewReader(b)); !errors.Is(err, ErrTopicTooLong) {
		t.Fatalf("got %v, want ErrTopicTooLong", err)
	}
}

// TestMultiMegabytePayload proves length-prefix framing handles frames far past any
// line-oriented limit.
func TestMultiMegabytePayload(t *testing.T) {
	payload := bytes.Repeat([]byte("D"), 5*1024*1024) // 5 MiB, under the 8 MiB cap
	f := Frame{Type: FrameResult, ID: 1234567890, Topic: "tool.invoke", Payload: payload}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, f); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !frameEqual(got, f) {
		t.Errorf("large-frame round-trip mismatch (id=%d topic=%q len=%d)", got.ID, got.Topic, len(got.Payload))
	}
}

// byteAtATimeReader hands out one byte per Read, so a frame is necessarily split
// across many underlying reads. This proves ReadFrame's io.ReadFull usage
// reassembles a header (and payload) that arrive in pieces.
type byteAtATimeReader struct {
	b   []byte
	pos int
}

func (s *byteAtATimeReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.b) {
		return 0, io.EOF
	}
	p[0] = s.b[s.pos]
	s.pos++
	return 1, nil
}

func TestSplitReadReassembly(t *testing.T) {
	f := Frame{Type: FrameRequest, ID: 7, Topic: "tool.invoke", Payload: []byte(`{"tool":"roll"}`)}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, f); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&byteAtATimeReader{b: buf.Bytes()})
	if err != nil {
		t.Fatalf("ReadFrame over a one-byte-at-a-time reader: %v", err)
	}
	if !frameEqual(got, f) {
		t.Errorf("split-read mismatch:\n got %+v\nwant %+v", got, f)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	// A Session over an in-memory pipe: Send on one end, Recv on the other.
	pr, pw := io.Pipe()
	send := NewSession(rw{w: pw})
	recv := NewSession(rw{r: pr})

	want := Frame{Type: FrameRequest, ID: 5, Topic: "tool.invoke", Payload: []byte(`{}`)}
	go func() {
		_ = send.Send(want)
		_ = pw.Close()
	}()
	got, err := recv.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !frameEqual(got, want) {
		t.Errorf("session round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// rw adapts separate reader/writer halves into a Transport for the Session test.
type rw struct {
	r io.Reader
	w io.Writer
}

func (x rw) Read(p []byte) (int, error)  { return x.r.Read(p) }
func (x rw) Write(p []byte) (int, error) { return x.w.Write(p) }
