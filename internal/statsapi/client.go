package statsapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Transport names reported in status.
const (
	TransportWS  = "ws"
	TransportTCP = "tcp"
)

// Client connects to the Rocket League Stats API forever, auto-detecting
// WebSocket vs raw TCP, and dispatches decoded events.
type Client struct {
	Host    string // default 127.0.0.1
	Port    int    // default 49123
	WebPort int    // default 49124; tried when Port is not reachable (0 = disabled)

	OnEvent      func(Event)
	OnConnect    func(transport string)
	OnDisconnect func()

	Log              *slog.Logger
	RetryInterval    time.Duration // default 3s
	HandshakeTimeout time.Duration // default 2s
	DialTimeout      time.Duration // default 2s

	connected atomic.Bool
	transport atomic.Value // string

	mu       sync.Mutex
	rawFirst map[int]bool // per port: skip the WS handshake on the next attempt

	// Port preference: after repeated useless sessions (accepted but closed
	// without any data) on the preferred port, the other one is tried first.
	preferWeb bool
	useless   int
}

// uselessSessionsBeforeSwitch is the number of consecutive sessions that
// produced nothing before the client prefers the other port.
const uselessSessionsBeforeSwitch = 2

// Status returns whether we are connected and the transport in use.
func (c *Client) Status() (bool, string) {
	t, _ := c.transport.Load().(string)
	return c.connected.Load(), t
}

func (c *Client) defaults() {
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port == 0 {
		c.Port = 49123
	}
	if c.RetryInterval <= 0 {
		c.RetryInterval = 3 * time.Second
	}
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = 2 * time.Second
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 2 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.rawFirst == nil {
		c.rawFirst = map[int]bool{}
	}
}

// Run blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	c.defaults()
	c.transport.Store("")
	waitingLogged := false
	for ctx.Err() == nil {
		ports := []int{c.Port}
		if c.WebPort > 0 && c.WebPort != c.Port {
			if c.preferWeb {
				ports = []int{c.WebPort, c.Port}
			} else {
				ports = append(ports, c.WebPort)
			}
		}
		var lastErr error
		connected := false
		for _, p := range ports {
			if ctx.Err() != nil {
				return
			}
			conn, err := (&net.Dialer{Timeout: c.DialTimeout, KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(p)))
			if err != nil {
				lastErr = err
				continue
			}
			connected = true
			waitingLogged = false
			if c.session(ctx, conn, p) {
				c.useless = 0
			} else if c.useless++; c.useless >= uselessSessionsBeforeSwitch && len(ports) > 1 {
				// The port accepts connections but never gives us anything
				// (e.g. it is not the Stats API, or it rejects both
				// transports): try the other port first from now on.
				c.useless = 0
				c.preferWeb = !c.preferWeb
				c.Log.Info("stats api port gives no data; switching port preference", "port", p)
			}
			break
		}
		if !connected {
			if !waitingLogged {
				c.Log.Info("waiting for Rocket League Stats API (game not running?)", "port", c.Port, "web_port", c.WebPort, "err", shortErr(lastErr))
				waitingLogged = true
			} else {
				c.Log.Debug("stats api not reachable", "err", shortErr(lastErr))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.RetryInterval):
		}
	}
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, syscall.ECONNREFUSED) || isRefused(err) {
		return "connection refused"
	}
	return err.Error()
}

func isRefused(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) {
		var se syscall.Errno
		if errors.As(op.Err, &se) {
			return se == 10061 // WSAECONNREFUSED
		}
	}
	return false
}

// session handles one TCP connection until it drops. It reports whether the
// session was useful (some data received, or the connection stayed up for a
// while, e.g. a silent server between matches).
func (c *Client) session(ctx context.Context, conn net.Conn, port int) bool {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	c.mu.Lock()
	raw := c.rawFirst[port]
	c.mu.Unlock()

	var (
		mode     string
		leftover []byte
		silent   bool
		err      error
	)
	if raw {
		mode = TransportTCP
	} else {
		mode, leftover, silent, err = c.detect(conn)
		if err != nil {
			if ctx.Err() == nil {
				c.Log.Info("stats api handshake failed; will try raw TCP next time", "port", port, "err", err)
				c.mu.Lock()
				c.rawFirst[port] = true
				c.mu.Unlock()
			}
			return false
		}
	}

	c.Log.Info("connected to Rocket League Stats API", "port", port, "transport", mode)
	c.transport.Store(mode)
	c.connected.Store(true)
	if c.OnConnect != nil {
		safeCall(c.Log, func() { c.OnConnect(mode) })
	}
	defer func() {
		c.connected.Store(false)
		c.transport.Store("")
		if c.OnDisconnect != nil {
			safeCall(c.Log, c.OnDisconnect)
		}
	}()

	var received int64
	split := &ObjectSplitter{Emit: func(obj []byte) {
		received++
		c.dispatch(obj)
	}}
	start := time.Now()
	useful := func() bool { return received > 0 || time.Since(start) >= 5*time.Second }

	if mode == TransportWS {
		err := c.readWS(conn, leftover, split)
		c.Log.Info("stats api disconnected", "transport", mode, "err", err)
		return useful()
	}

	// Raw TCP. When the upgrade request got no answer within the handshake
	// timeout (silent server), the answer may still come late from a slow
	// WebSocket server: sniff the first bytes before committing to raw mode,
	// otherwise WS frames would be parsed as a raw stream forever.
	var head []byte
	sniff := silent
	if !sniff {
		split.Feed(leftover)
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if !sniff {
				split.Feed(buf[:n])
			} else {
				head = append(head, buf[:n]...)
				m, rest, done, cerr := classify(head)
				switch {
				case cerr != nil:
					c.Log.Info("stats api: unexpected late handshake answer", "err", cerr)
					return useful()
				case done && m == TransportWS:
					c.Log.Info("stats api answered the upgrade late; switching to WebSocket", "port", port)
					c.transport.Store(TransportWS)
					werr := c.readWS(conn, rest, split)
					c.Log.Info("stats api disconnected", "transport", TransportWS, "err", werr)
					return useful()
				case done:
					sniff = false
					split.Feed(head)
					head = nil
				}
			}
		}
		if err != nil {
			c.Log.Info("stats api disconnected", "transport", mode, "err", err)
			// A raw session that produced nothing and died immediately:
			// next time go back to auto-detection.
			if raw && received == 0 && time.Since(start) < 5*time.Second {
				c.mu.Lock()
				c.rawFirst[port] = false
				c.mu.Unlock()
			}
			return useful()
		}
	}
}

// readWS reads WebSocket messages until the connection drops.
func (c *Client) readWS(conn net.Conn, leftover []byte, split *ObjectSplitter) error {
	r := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(leftover), conn), 64<<10)
	wr := &WSReader{R: r, W: conn, Mask: true}
	for {
		msg, err := wr.ReadMessage()
		if err != nil {
			return err
		}
		split.Feed(msg)
	}
}

// classify inspects the first bytes received after the upgrade request.
// done=false means more bytes are needed. For raw TCP, rest is the whole
// buffer (it is stream data); for WebSocket it is what follows the headers.
func classify(buf []byte) (mode string, rest []byte, done bool, err error) {
	trimmed := bytes.TrimLeft(buf, " \t\r\n\x00")
	if len(trimmed) == 0 {
		return "", nil, false, nil
	}
	if trimmed[0] == '{' {
		return TransportTCP, buf, true, nil
	}
	httpPrefix := []byte("HTTP/")
	if !bytes.HasPrefix(httpPrefix, trimmed[:min(len(trimmed), len(httpPrefix))]) {
		return TransportTCP, buf, true, nil // non-HTTP answer => raw stream
	}
	if len(trimmed) < len(httpPrefix) {
		return "", nil, false, nil
	}
	i := bytes.Index(trimmed, []byte("\r\n\r\n"))
	if i < 0 {
		if len(trimmed) > 16<<10 {
			return "", nil, false, errors.New("HTTP header too large")
		}
		return "", nil, false, nil
	}
	line := trimmed
	if j := bytes.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	if f := bytes.Fields(line); len(f) >= 2 && string(f[1]) == "101" {
		return TransportWS, append([]byte(nil), trimmed[i+4:]...), true, nil
	}
	return "", nil, false, fmt.Errorf("unexpected HTTP answer %q", bytes.TrimSpace(line))
}

// detect sends a WebSocket upgrade and looks at the answer. silent=true means
// nothing was received within the handshake timeout (raw TCP server idle
// between matches, or a slow WebSocket server).
func (c *Client) detect(conn net.Conn) (mode string, leftover []byte, silent bool, err error) {
	_ = conn.SetWriteDeadline(time.Now().Add(c.HandshakeTimeout))
	if _, err := conn.Write(UpgradeRequest(conn.RemoteAddr().String(), NewWSKey())); err != nil {
		return "", nil, false, err
	}
	_ = conn.SetWriteDeadline(time.Time{})
	_ = conn.SetReadDeadline(time.Now().Add(c.HandshakeTimeout))
	defer conn.SetReadDeadline(time.Time{})
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := conn.Read(tmp)
		buf = append(buf, tmp[:n]...)
		m, rest, done, cerr := classify(buf)
		if cerr != nil {
			return "", nil, false, cerr
		}
		if done {
			return m, rest, false, nil
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if len(bytes.TrimSpace(buf)) == 0 {
					// Silent server (raw TCP in menus): keep the connection.
					return TransportTCP, nil, true, nil
				}
				return "", nil, false, errors.New("incomplete handshake answer")
			}
			return "", nil, false, err // closed by server
		}
	}
}

func (c *Client) dispatch(obj []byte) {
	ev, err := DecodeEnvelope(obj)
	if err != nil {
		c.Log.Debug("ignoring undecodable object", "err", err, "len", len(obj))
		return
	}
	if c.OnEvent != nil {
		safeCall(c.Log, func() { c.OnEvent(ev) })
	}
}

func safeCall(log *slog.Logger, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered panic in stats api callback", "panic", fmt.Sprint(r))
		}
	}()
	f()
}
