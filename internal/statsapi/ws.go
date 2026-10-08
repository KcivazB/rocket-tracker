package statsapi

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Minimal RFC 6455 implementation: enough for a client talking to the game
// (and for the simulator acting as a server).

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket opcodes.
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// MaxWSMessage bounds a reassembled message.
const MaxWSMessage = 16 << 20

// ComputeAccept returns the Sec-WebSocket-Accept value for a key.
func ComputeAccept(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// NewWSKey returns a random Sec-WebSocket-Key.
func NewWSKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}

// UpgradeRequest builds the client handshake.
func UpgradeRequest(host, key string) []byte {
	return []byte("GET / HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"User-Agent: rltracker\r\n\r\n")
}

// Frame is a single WebSocket frame.
type Frame struct {
	Fin     bool
	Opcode  byte
	Payload []byte
}

var errFrameTooLarge = errors.New("websocket: frame too large")

// ReadFrame reads one frame (masked or not).
func ReadFrame(r *bufio.Reader) (Frame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Fin: hdr[0]&0x80 != 0, Opcode: hdr[0] & 0x0F}
	masked := hdr[1]&0x80 != 0
	n := uint64(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return Frame{}, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return Frame{}, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > MaxWSMessage {
		return Frame{}, errFrameTooLarge
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return Frame{}, err
		}
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return Frame{}, err
	}
	if masked {
		for i := range f.Payload {
			f.Payload[i] ^= mask[i%4]
		}
	}
	return f, nil
}

// WriteFrame writes a single final frame. Clients must mask (RFC 6455 5.3).
func WriteFrame(w io.Writer, opcode byte, payload []byte, mask bool) error {
	hdr := make([]byte, 0, 14)
	hdr = append(hdr, 0x80|opcode&0x0F)
	var mbit byte
	if mask {
		mbit = 0x80
	}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, mbit|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, mbit|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, mbit|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	body := payload
	if mask {
		var key [4]byte
		_, _ = rand.Read(key[:])
		hdr = append(hdr, key[:]...)
		body = make([]byte, n)
		for i := range payload {
			body[i] = payload[i] ^ key[i%4]
		}
	}
	buf := append(hdr, body...)
	_, err := w.Write(buf)
	return err
}

// WSReader reassembles messages and answers control frames.
type WSReader struct {
	R *bufio.Reader
	// W is used for pong / close replies (client side => masked).
	W    io.Writer
	Mask bool
}

// ErrWSClosed is returned when the peer sent a close frame.
var ErrWSClosed = errors.New("websocket: closed by peer")

// ReadMessage returns the next text/binary message payload.
func (wr *WSReader) ReadMessage() ([]byte, error) {
	var msg []byte
	inMsg := false
	for {
		f, err := ReadFrame(wr.R)
		if err != nil {
			return nil, err
		}
		switch f.Opcode {
		case OpPing:
			if wr.W != nil {
				_ = WriteFrame(wr.W, OpPong, f.Payload, wr.Mask)
			}
		case OpPong:
		case OpClose:
			if wr.W != nil {
				_ = WriteFrame(wr.W, OpClose, nil, wr.Mask)
			}
			return nil, ErrWSClosed
		case OpText, OpBinary:
			msg = append(msg[:0], f.Payload...)
			inMsg = true
			if f.Fin {
				return msg, nil
			}
		case OpContinuation:
			if !inMsg {
				continue // stray continuation, ignore
			}
			if len(msg)+len(f.Payload) > MaxWSMessage {
				return nil, errFrameTooLarge
			}
			msg = append(msg, f.Payload...)
			if f.Fin {
				return msg, nil
			}
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %d", f.Opcode)
		}
	}
}
