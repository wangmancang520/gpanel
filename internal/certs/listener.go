package certs

import (
	"bufio"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

// Listen serves HTTP and HTTPS on one port. Each new connection's first byte
// decides: 0x16 starts a TLS handshake (served with the current certificate
// while HTTPS is enabled, dropped otherwise); anything else is plain HTTP.
func (m *Manager) Listen(addr string) (net.Listener, error) {
	inner, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return m.wrap(inner), nil
}

func (m *Manager) wrap(inner net.Listener) *muxListener {
	l := &muxListener{Listener: inner, m: m, cfg: m.TLSConfig(), conns: make(chan net.Conn), done: make(chan struct{})}
	go l.acceptLoop()
	return l
}

type muxListener struct {
	net.Listener
	m     *Manager
	cfg   *tls.Config
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once

	mu  sync.Mutex
	err error // why the inner listener stopped, if not via Close
}

const (
	sniffTimeout   = 10 * time.Second
	maxAcceptDelay = time.Second
	minAcceptDelay = 5 * time.Millisecond
)

func (l *muxListener) acceptLoop() {
	var delay time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			select {
			case <-l.done: // closed by us; Accept reports net.ErrClosed
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				l.mu.Lock()
				l.err = err
				l.mu.Unlock()
				l.Close()
				return
			}
			// Anything else (EMFILE, ECONNABORTED, ...) is transient. Back off
			// and retry like http.Server does: giving up would stop the panel,
			// and under systemd every game server with it.
			delay = min(max(delay*2, minAcceptDelay), maxAcceptDelay)
			log.Printf("接受连接失败，%v 后重试: %v", delay, err)
			select {
			case <-time.After(delay):
			case <-l.done:
				return
			}
			continue
		}
		delay = 0
		// Sniff off the accept loop so a slow client can't stall others.
		go l.classify(c)
	}
}

func (l *muxListener) classify(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 { // TLS handshake record
		if !l.m.Active() {
			c.Close()
			return
		}
		out = tls.Server(out, l.cfg)
	}
	select {
	case l.conns <- out:
	case <-l.done:
		c.Close()
	}
}

func (l *muxListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return nil, net.ErrClosed
	}
}

func (l *muxListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.Listener.Close()
	})
	return err
}

// peekedConn replays the sniffed byte(s) before reading from the socket.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
