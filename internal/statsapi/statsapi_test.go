package statsapi

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecodeEnvelopeObjectAndString(t *testing.T) {
	obj := `{"Event":"UpdateState","Data":{"MatchGuid":"ABC","Players":[{"Name":"X","PrimaryId":"Epic|1|0","TeamNum":1,"Score":"120","Goals":1.0,"Boost":55.5,"bOnGround":1}],"Game":{"TimeSeconds":250.0,"bOvertime":"false","Arena":"Stadium_P","bHasTarget":true,"Target":{"Name":"X","Shortcut":3,"TeamNum":1},"Unknown":{"x":1}}}}`
	inner := `{"MatchGuid":"ABC","Players":[{"Name":"X","PrimaryId":"Epic|1|0","TeamNum":1,"Score":120,"Goals":1,"Boost":55.5,"bOnGround":true}],"Game":{"TimeSeconds":250,"bOvertime":false,"Arena":"Stadium_P","bHasTarget":true,"Target":{"Name":"X","Shortcut":3,"TeamNum":1}}}`
	str := `{"Event":"UpdateState","Data":` + quote(inner) + `}`
	for name, raw := range map[string]string{"object": obj, "string": str} {
		ev, err := DecodeEnvelope([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ev.Name != EvUpdateState {
			t.Fatalf("%s: name %q", name, ev.Name)
		}
		var u UpdateState
		if err := ev.Decode(&u); err != nil {
			t.Fatalf("%s: decode %v", name, err)
		}
		if u.MatchGUID != "ABC" || len(u.Players) != 1 || u.Game == nil {
			t.Fatalf("%s: %+v", name, u)
		}
		p := u.Players[0]
		if p.Score != 120 || p.Goals != 1 || p.TeamNum != 1 || p.Boost == nil || *p.Boost != 55.5 || p.OnGround == nil || !bool(*p.OnGround) {
			t.Fatalf("%s: player %+v", name, p)
		}
		if p.Speed != nil || p.HasCar != nil {
			t.Fatalf("%s: absent spectator fields must stay nil", name)
		}
		if *u.Game.TimeSeconds != 250 || bool(u.Game.Overtime) || !bool(u.Game.HasTarget) || u.Game.Target.Shortcut != 3 {
			t.Fatalf("%s: game %+v", name, u.Game)
		}
	}
}

func quote(s string) string {
	var b bytes.Buffer
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestDecodeLenientGarbage(t *testing.T) {
	cases := []string{
		`{"Event":"GoalScored","Data":{"Scorer":"oops","GoalSpeed":"fast","Assister":null}}`,
		`{"Event":"GoalScored","Data":"not json at all"}`,
		`{"Event":"GoalScored","Data":null}`,
		`{"Event":"GoalScored"}`,
		`{"Event":"BallHit","Data":{"Players":{"Name":"A","Shortcut":1,"TeamNum":0},"Ball":{"PostHitSpeed":"1500.5"}}}`,
	}
	for _, c := range cases {
		ev, err := DecodeEnvelope([]byte(c))
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		var g GoalScored
		_ = ev.Decode(&g)
		var h BallHit
		_ = ev.Decode(&h)
	}
	ev, _ := DecodeEnvelope([]byte(cases[4]))
	var h BallHit
	if err := ev.Decode(&h); err != nil || len(h.Players) != 1 || h.Players[0].Name != "A" || h.Ball.PostHitSpeed != 1500.5 {
		t.Fatalf("single-object Players: %+v err=%v", h, err)
	}
	if _, err := DecodeEnvelope([]byte(`{"Foo":1}`)); err != ErrNotEnvelope {
		t.Fatalf("expected ErrNotEnvelope, got %v", err)
	}
	if _, err := DecodeEnvelope([]byte(`{"Event":`)); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestSplitterRawConcatenatedStream(t *testing.T) {
	stream := `{"Event":"A","Data":{"s":"brace } in { string \" quote"}}{"Event":"B","Data":{}}` + "\r\n" +
		`  {"Event":"C","Data":"{\"x\":1}"}garbage{"Event":"D","Data":{"arr":[{"a":[1,2]}]}}` + "\n"
	for _, chunk := range []int{1, 3, 7, 1000} {
		var got []string
		sp := &ObjectSplitter{Emit: func(o []byte) {
			ev, err := DecodeEnvelope(o)
			if err != nil {
				t.Fatalf("chunk %d: %v (%s)", chunk, err, o)
			}
			got = append(got, ev.Name)
		}}
		for i := 0; i < len(stream); i += chunk {
			end := min(i+chunk, len(stream))
			sp.Feed([]byte(stream[i:end]))
		}
		if strings.Join(got, ",") != "A,B,C,D" || sp.Pending() {
			t.Fatalf("chunk %d: got %v pending=%v", chunk, got, sp.Pending())
		}
	}
}

func TestSplitterOversizedObjectRecovers(t *testing.T) {
	var got []string
	sp := &ObjectSplitter{MaxSize: 64, Emit: func(o []byte) { got = append(got, string(o)) }}
	sp.Feed([]byte(`{"big":"` + strings.Repeat("x", 200) + `"}{"ok":1}`))
	// The oversized object is dropped; parsing resynchronises on the next '{'.
	if len(got) == 0 || got[len(got)-1] != `{"ok":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestWSFrames(t *testing.T) {
	var buf bytes.Buffer
	big := bytes.Repeat([]byte("a"), 70000)
	mid := bytes.Repeat([]byte("b"), 300)
	_ = WriteFrame(&buf, OpText, []byte("hello"), true)
	_ = WriteFrame(&buf, OpText, mid, false)
	_ = WriteFrame(&buf, OpBinary, big, true)
	r := bufio.NewReader(&buf)
	for _, want := range [][]byte{[]byte("hello"), mid, big} {
		f, err := ReadFrame(r)
		if err != nil || !f.Fin || !bytes.Equal(f.Payload, want) {
			t.Fatalf("frame len %d err %v", len(f.Payload), err)
		}
	}

	// Fragmented message + interleaved ping => pong is written back (masked).
	var in, out bytes.Buffer
	writeRaw := func(fin bool, op byte, p []byte) {
		b := byte(op)
		if fin {
			b |= 0x80
		}
		in.WriteByte(b)
		in.WriteByte(byte(len(p)))
		in.Write(p)
	}
	writeRaw(false, OpText, []byte(`{"Event":"A",`))
	writeRaw(true, OpPing, []byte("hi"))
	writeRaw(false, OpContinuation, []byte(`"Data":`))
	writeRaw(true, OpContinuation, []byte(`{}}`))
	writeRaw(true, OpClose, nil)
	wr := &WSReader{R: bufio.NewReader(&in), W: &out, Mask: true}
	msg, err := wr.ReadMessage()
	if err != nil || string(msg) != `{"Event":"A","Data":{}}` {
		t.Fatalf("msg %q err %v", msg, err)
	}
	pong, err := ReadFrame(bufio.NewReader(bytes.NewReader(out.Bytes())))
	if err != nil || pong.Opcode != OpPong || string(pong.Payload) != "hi" || out.Bytes()[1]&0x80 == 0 {
		t.Fatalf("pong %+v err %v masked=%v", pong, err, out.Bytes()[1]&0x80 != 0)
	}
	if _, err := wr.ReadMessage(); err != ErrWSClosed {
		t.Fatalf("expected close, got %v", err)
	}
	if ComputeAccept("dGhlIHNhbXBsZSBub25jZQ==") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal("bad accept computation")
	}
}

// ---- client end to end ----

type collector struct {
	mu        sync.Mutex
	names     []string
	connects  []string
	disconns  int
	gotEvents chan struct{}
	want      int
}

func newCollector(want int) *collector {
	return &collector{gotEvents: make(chan struct{}), want: want}
}

func (c *collector) client(port int) *Client {
	return &Client{
		Port: port, RetryInterval: 50 * time.Millisecond, HandshakeTimeout: 300 * time.Millisecond,
		OnEvent: func(ev Event) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.names = append(c.names, ev.Name)
			if len(c.names) == c.want {
				close(c.gotEvents)
			}
		},
		OnConnect:    func(tr string) { c.mu.Lock(); c.connects = append(c.connects, tr); c.mu.Unlock() },
		OnDisconnect: func() { c.mu.Lock(); c.disconns++; c.mu.Unlock() },
	}
}

func (c *collector) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.gotEvents:
	case <-time.After(5 * time.Second):
		c.mu.Lock()
		defer c.mu.Unlock()
		t.Fatalf("timeout; got %v connects %v", c.names, c.connects)
	}
}

func listen(t *testing.T) (net.Listener, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, ln.Addr().(*net.TCPAddr).Port
}

const twoEvents = `{"Event":"MatchCreated","Data":{"MatchGuid":"X"}}{"Event":"UpdateState","Data":"{\"MatchGuid\":\"X\"}"}` + "\n"

func TestClientRawTCPImmediate(t *testing.T) {
	ln, port := listen(t)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(twoEvents)) // server talks first; ignores our upgrade request
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	cl := col.client(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)
	col.wait(t)
	if ok, tr := cl.Status(); !ok || tr != TransportTCP {
		t.Fatalf("status %v %q", ok, tr)
	}
	if col.names[0] != "MatchCreated" || col.names[1] != "UpdateState" {
		t.Fatalf("names %v", col.names)
	}
}

func TestClientRawTCPSilentThenData(t *testing.T) {
	ln, port := listen(t)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(700 * time.Millisecond) // longer than the handshake timeout
		half := len(twoEvents) / 2
		_, _ = conn.Write([]byte(twoEvents[:half]))
		time.Sleep(50 * time.Millisecond)
		_, _ = conn.Write([]byte(twoEvents[half:]))
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := col.client(port)
	go cl.Run(ctx)
	col.wait(t)
	if _, tr := cl.Status(); tr != TransportTCP {
		t.Fatalf("transport %q", tr)
	}
}

func TestClientRawTCPClosesOnUpgrade(t *testing.T) {
	ln, port := listen(t)
	go func() {
		// 1st connection: server hangs up when it receives the HTTP request.
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		conn.Close()
		// 2nd connection: the client must not send anything now.
		conn, err = ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if n, _ := conn.Read(buf); n > 0 {
			return // client sent a handshake again: test will time out
		}
		_, _ = conn.Write([]byte(twoEvents))
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.client(port).Run(ctx)
	col.wait(t)
}

func TestClientWebSocket(t *testing.T) {
	ln, port := listen(t)
	pongs := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+
			ComputeAccept(req.Header.Get("Sec-WebSocket-Key"))+"\r\n\r\n")
		_ = WriteFrame(conn, OpPing, []byte("p"), false)
		// One message containing a single event, then one fragmented message.
		_ = WriteFrame(conn, OpText, []byte(`{"Event":"MatchCreated","Data":{"MatchGuid":"X"}}`), false)
		_, _ = conn.Write([]byte{0x01, 10})
		_, _ = conn.Write([]byte(`{"Event":"`))
		_ = WriteFrame(conn, OpContinuation, []byte(`UpdateState","Data":"{}"}`), false)
		f, err := ReadFrame(br)
		if err == nil && f.Opcode == OpPong {
			pongs <- string(f.Payload)
		}
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := col.client(port)
	go cl.Run(ctx)
	col.wait(t)
	if _, tr := cl.Status(); tr != TransportWS {
		t.Fatalf("transport %q", tr)
	}
	select {
	case p := <-pongs:
		if p != "p" {
			t.Fatalf("pong payload %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no pong")
	}
}

func TestClientFallsBackToWebPort(t *testing.T) {
	// Port is closed; WebPort accepts.
	dead, deadPort := listen(t)
	dead.Close()
	ln, webPort := listen(t)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(twoEvents))
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	cl := col.client(deadPort)
	cl.WebPort = webPort
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)
	col.wait(t)
}

func TestClientDisconnectCallback(t *testing.T) {
	ln, port := listen(t)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte(twoEvents))
		time.Sleep(100 * time.Millisecond)
		conn.Close() // game closed
	}()
	col := newCollector(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := col.client(port)
	go cl.Run(ctx)
	col.wait(t)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		col.mu.Lock()
		d := col.disconns
		col.mu.Unlock()
		if d > 0 {
			if ok, _ := cl.Status(); ok {
				t.Fatal("still connected after disconnect")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("OnDisconnect not called")
}

// Regression: a WebSocket server that answers the upgrade after the
// handshake timeout used to be classified as raw TCP forever (WS frames
// parsed as a raw stream, pings never answered).
func TestClientLateWebSocketAnswer(t *testing.T) {
	ln, port := listen(t)
	pongs := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		time.Sleep(700 * time.Millisecond) // > HandshakeTimeout (300ms)
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+
			ComputeAccept(req.Header.Get("Sec-WebSocket-Key"))+"\r\n\r\n")
		_ = WriteFrame(conn, OpPing, []byte("late"), false)
		// A message > 125 bytes so the frame header carries an extended length.
		big := `{"Event":"MatchCreated","Data":{"MatchGuid":"` + strings.Repeat("{", 200) + `"}}`
		_ = WriteFrame(conn, OpText, []byte(big), false)
		_ = WriteFrame(conn, OpText, []byte(`{"Event":"UpdateState","Data":{}}`), false)
		if f, err := ReadFrame(br); err == nil && f.Opcode == OpPong {
			pongs <- string(f.Payload)
		}
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := col.client(port)
	go cl.Run(ctx)
	col.wait(t)
	if _, tr := cl.Status(); tr != TransportWS {
		t.Fatalf("transport %q", tr)
	}
	col.mu.Lock()
	names := append([]string(nil), col.names...)
	col.mu.Unlock()
	if names[0] != "MatchCreated" || names[1] != "UpdateState" {
		t.Fatalf("names %v", names)
	}
	select {
	case p := <-pongs:
		if p != "late" {
			t.Fatalf("pong %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no pong")
	}
}

// Regression: when Port accepts connections but always hangs up without
// data, WebPort was never tried (only a refused Port fell back to it).
func TestClientSwitchesToWebPortWhenPortUseless(t *testing.T) {
	bad, badPort := listen(t)
	go func() {
		for {
			conn, err := bad.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	ln, webPort := listen(t)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(twoEvents))
		time.Sleep(2 * time.Second)
	}()
	col := newCollector(2)
	cl := col.client(badPort)
	cl.WebPort = webPort
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.Run(ctx)
	col.wait(t)
}
