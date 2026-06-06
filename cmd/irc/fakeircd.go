package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
)

// fakeIRCd is a tiny in-process IRC server: enough of the protocol for a client to
// register, join, exchange PRIVMSG, and answer PING. It is used by -selftest and by
// the tests so the bridge can be exercised offline with no network. It is NOT a real
// ircd: one connection, no multi-user routing.
type fakeIRCd struct {
	ln net.Listener

	mu       sync.Mutex
	clientWr *bufio.Writer // the connected client's write side, for the server to push lines
	received []string      // PRIVMSG/JOIN lines the server saw from the client
	joinedCh chan string   // signals a JOIN (channel name)
	privCh   chan string   // signals a client PRIVMSG (raw text)
}

// newFakeIRCd starts a fake server on an ephemeral localhost port.
func newFakeIRCd() (*fakeIRCd, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &fakeIRCd{
		ln:       ln,
		joinedCh: make(chan string, 1),
		privCh:   make(chan string, 8),
	}
	go f.accept()
	return f, nil
}

func (f *fakeIRCd) addr() string { return f.ln.Addr().String() }

func (f *fakeIRCd) close() { _ = f.ln.Close() }

func (f *fakeIRCd) accept() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	go f.serve(conn)
}

func (f *fakeIRCd) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	f.mu.Lock()
	f.clientWr = bw
	f.mu.Unlock()

	var nick string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.received = append(f.received, line)
		f.mu.Unlock()

		switch {
		case strings.HasPrefix(line, "NICK "):
			nick = strings.TrimSpace(strings.TrimPrefix(line, "NICK "))
		case strings.HasPrefix(line, "USER "):
			// Registration complete: send the welcome numeric.
			f.writeLine(fmt.Sprintf(":fake 001 %s :Welcome to the fake IRC network", nick))
		case strings.HasPrefix(line, "JOIN "):
			ch := strings.TrimSpace(strings.TrimPrefix(line, "JOIN "))
			f.writeLine(fmt.Sprintf(":%s!u@h JOIN %s", nick, ch))
			select {
			case f.joinedCh <- ch:
			default:
			}
		case strings.HasPrefix(line, "PRIVMSG "):
			select {
			case f.privCh <- strings.TrimPrefix(line, "PRIVMSG "):
			default:
			}
		case strings.HasPrefix(line, "QUIT"):
			return
		}
	}
}

// writeLine pushes one server->client line (adds CRLF).
func (f *fakeIRCd) writeLine(line string) {
	f.mu.Lock()
	w := f.clientWr
	f.mu.Unlock()
	if w == nil {
		return
	}
	_, _ = w.WriteString(line + "\r\n")
	_ = w.Flush()
}

// injectChannelMessage simulates `from` sending `text` to channel `ch`.
func (f *fakeIRCd) injectChannelMessage(from, ch, text string) {
	f.writeLine(fmt.Sprintf(":%s!u@h PRIVMSG %s :%s", from, ch, text))
}

// injectPing simulates a server PING.
func (f *fakeIRCd) injectPing(token string) {
	f.writeLine("PING :" + token)
}
