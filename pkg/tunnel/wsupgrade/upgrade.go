// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wsupgrade

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	opcodeContinuation = 0x0
	opcodeText         = 0x1
	opcodeBinary       = 0x2
	opcodeClose        = 0x8
	opcodePing         = 0x9
	opcodePong         = 0xA

	finBit  = 0x80
	maskBit = 0x80

	maxWriteChunk       = 32 << 10 // 32 KiB per outgoing binary frame
	maxReadFramePayload = 16 << 20 // 16 MiB cap per incoming frame header
)

// BufferedConn returns a net.Conn that yields any unread bytes already buffered
// in br before reading from conn. When br has no buffered bytes, conn is
// returned directly without keeping br pinned in memory.
func BufferedConn(conn net.Conn, br *bufio.Reader) net.Conn {
	if br == nil || br.Buffered() == 0 {
		return conn
	}
	buf := make([]byte, br.Buffered())
	n, _ := io.ReadFull(br, buf)
	if n == 0 {
		return conn
	}
	return &prefixedConn{
		Conn:   conn,
		prefix: bytes.NewReader(buf[:n]),
	}
}

type prefixedConn struct {
	net.Conn
	prefix *bytes.Reader
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if c.prefix != nil && c.prefix.Len() > 0 {
		n, err := c.prefix.Read(p)
		if c.prefix.Len() == 0 {
			c.prefix = nil
		}
		if n > 0 {
			return n, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}
	return c.Conn.Read(p)
}

func connectionHasUpgrade(h http.Header) bool {
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "Upgrade") {
				return true
			}
		}
	}
	return false
}

func sanitizeHeaderValue(v string) string {
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, "\n", "")
	return v
}

// Upgrade validates an RFC 6455 WebSocket upgrade request, hijacks the
// underlying connection (preserving any pre-read bytes), writes the 101
// Switching Protocols response including any extra headers in respHeader, and
// wraps the hijacked connection in an RFC 6455 binary-frame net.Conn.
func Upgrade(w http.ResponseWriter, r *http.Request, respHeader http.Header) (net.Conn, error) {
	if r.Method != http.MethodGet {
		http.Error(w, "websocket upgrade requires GET", http.StatusMethodNotAllowed)
		return nil, errors.New("wsupgrade: method must be GET")
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") || !connectionHasUpgrade(r.Header) {
		w.Header().Set("Upgrade", "websocket")
		http.Error(w, "websocket upgrade required", http.StatusUpgradeRequired)
		return nil, errors.New("wsupgrade: missing Upgrade: websocket or Connection: Upgrade header")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "unsupported Sec-WebSocket-Version", http.StatusBadRequest)
		return nil, errors.New("wsupgrade: unsupported Sec-WebSocket-Version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("wsupgrade: missing Sec-WebSocket-Key")
	}

	rc := http.NewResponseController(w)
	conn, bufrw, err := rc.Hijack()
	if err != nil {
		return nil, fmt.Errorf("wsupgrade: hijack: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	var resp strings.Builder
	resp.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	resp.WriteString("Upgrade: websocket\r\n")
	resp.WriteString("Connection: Upgrade\r\n")
	resp.WriteString("Sec-WebSocket-Accept: ")
	resp.WriteString(Accept(key))
	resp.WriteString("\r\n")
	for k, vals := range respHeader {
		ck := http.CanonicalHeaderKey(strings.TrimSpace(k))
		if ck == "" || strings.EqualFold(ck, "Upgrade") || strings.EqualFold(ck, "Connection") || strings.EqualFold(ck, "Sec-WebSocket-Accept") {
			continue
		}
		for _, val := range vals {
			resp.WriteString(ck)
			resp.WriteString(": ")
			resp.WriteString(sanitizeHeaderValue(val))
			resp.WriteString("\r\n")
		}
	}
	resp.WriteString("\r\n")

	if _, err := bufrw.WriteString(resp.String()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("wsupgrade: write 101 response: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("wsupgrade: flush 101 response: %w", err)
	}

	return NewServerConn(BufferedConn(conn, bufrw.Reader)), nil
}

// NewClientConn wraps an upgraded connection in an RFC 6455 binary-frame stream
// where outgoing frames are masked (client-to-server) and incoming frames must
// be unmasked (server-to-client).
func NewClientConn(conn net.Conn) net.Conn {
	return &wsConn{Conn: conn, isClient: true}
}

// NewServerConn wraps an upgraded connection in an RFC 6455 binary-frame stream
// where incoming frames must be masked (client-to-server) and outgoing frames
// are unmasked (server-to-client).
func NewServerConn(conn net.Conn) net.Conn {
	return &wsConn{Conn: conn, isClient: false}
}

type wsConn struct {
	net.Conn
	isClient bool

	readMu   sync.Mutex
	remBytes int64
	masked   bool
	mask     [4]byte
	maskPos  int
	inFrag   bool
	sawEOF   bool

	writeMu   sync.Mutex
	writeBuf  []byte
	closeSent atomic.Bool
}

func (c *wsConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for c.remBytes == 0 {
		if c.sawEOF {
			return 0, io.EOF
		}
		if err := c.nextDataFrame(); err != nil {
			if errors.Is(err, io.EOF) {
				c.sawEOF = true
			}
			return 0, err
		}
	}

	toRead := len(p)
	if int64(toRead) > c.remBytes {
		toRead = int(c.remBytes)
	}
	n, err := c.Conn.Read(p[:toRead])
	if n > 0 {
		if c.masked {
			for i := 0; i < n; i++ {
				p[i] ^= c.mask[(c.maskPos+i)&3]
			}
			c.maskPos = (c.maskPos + n) & 3
		}
		c.remBytes -= int64(n)
		if errors.Is(err, io.EOF) && c.remBytes == 0 {
			return n, nil
		}
	}
	if err != nil && errors.Is(err, io.EOF) && c.remBytes > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (c *wsConn) nextDataFrame() error {
	var hdr [2]byte
	for {
		if _, err := io.ReadFull(c.Conn, hdr[:]); err != nil {
			return err
		}
		if hdr[0]&0x70 != 0 {
			return errors.New("wsupgrade: non-zero RSV bits in WebSocket frame")
		}
		fin := (hdr[0] & finBit) != 0
		opcode := hdr[0] & 0x0f
		masked := (hdr[1] & maskBit) != 0
		if c.isClient && masked {
			return errors.New("wsupgrade: server sent masked WebSocket frame")
		}
		if !c.isClient && !masked {
			return errors.New("wsupgrade: client sent unmasked WebSocket frame")
		}

		length := int64(hdr[1] & 0x7f)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.Conn, ext[:]); err != nil {
				return collapseUnexpectedEOF(err)
			}
			length = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.Conn, ext[:]); err != nil {
				return collapseUnexpectedEOF(err)
			}
			u := binary.BigEndian.Uint64(ext[:])
			if u > maxReadFramePayload {
				return fmt.Errorf("wsupgrade: frame payload %d exceeds limit %d", u, maxReadFramePayload)
			}
			length = int64(u)
		}
		if length > maxReadFramePayload {
			return fmt.Errorf("wsupgrade: frame payload %d exceeds limit %d", length, maxReadFramePayload)
		}

		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.Conn, mask[:]); err != nil {
				return collapseUnexpectedEOF(err)
			}
		}

		// Control frames (RFC 6455 §5.5) may be interleaved with fragmented data frames.
		if opcode >= 0x08 {
			if !fin || length > 125 {
				return errors.New("wsupgrade: invalid WebSocket control frame")
			}
			var payload [125]byte
			if length > 0 {
				if _, err := io.ReadFull(c.Conn, payload[:length]); err != nil {
					return collapseUnexpectedEOF(err)
				}
				if masked {
					for i := int64(0); i < length; i++ {
						payload[i] ^= mask[i&3]
					}
				}
			}
			switch opcode {
			case opcodeClose:
				c.sendCloseBestEffort()
				return io.EOF
			case opcodePing:
				_ = c.writeFrame(opcodePong, payload[:length])
				continue
			case opcodePong:
				continue
			default:
				return fmt.Errorf("wsupgrade: unsupported WebSocket control opcode 0x%x", opcode)
			}
		}

		switch opcode {
		case opcodeBinary, opcodeText:
			if c.inFrag {
				return errors.New("wsupgrade: new data frame before fragmented message completed")
			}
			c.inFrag = !fin
		case opcodeContinuation:
			if !c.inFrag {
				return errors.New("wsupgrade: unexpected continuation frame")
			}
			if fin {
				c.inFrag = false
			}
		default:
			return fmt.Errorf("wsupgrade: unsupported WebSocket data opcode 0x%x", opcode)
		}

		if length == 0 {
			continue
		}
		c.remBytes = length
		c.masked = masked
		c.mask = mask
		c.maskPos = 0
		return nil
	}
}

func collapseUnexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (c *wsConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxWriteChunk {
			chunk = p[:maxWriteChunk]
		}
		if err := c.writeFrame(opcodeBinary, chunk); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	hdrLen := 2
	n := len(payload)
	switch {
	case n >= 65536:
		hdrLen += 8
	case n >= 126:
		hdrLen += 2
	}
	if c.isClient {
		hdrLen += 4
	}
	totalLen := hdrLen + n
	if cap(c.writeBuf) < totalLen {
		c.writeBuf = make([]byte, totalLen)
	}
	buf := c.writeBuf[:totalLen]
	buf[0] = finBit | (opcode & 0x0f)

	pos := 2
	maskFlag := byte(0)
	if c.isClient {
		maskFlag = maskBit
	}
	switch {
	case n < 126:
		buf[1] = maskFlag | byte(n)
	case n < 65536:
		buf[1] = maskFlag | 126
		binary.BigEndian.PutUint16(buf[2:4], uint16(n))
		pos = 4
	default:
		buf[1] = maskFlag | 127
		binary.BigEndian.PutUint64(buf[2:10], uint64(n))
		pos = 10
	}

	if c.isClient {
		var maskKey [4]byte
		if _, err := rand.Read(maskKey[:]); err != nil {
			return fmt.Errorf("wsupgrade: mask key: %w", err)
		}
		copy(buf[pos:pos+4], maskKey[:])
		pos += 4
		for i := 0; i < n; i++ {
			buf[pos+i] = payload[i] ^ maskKey[i&3]
		}
	} else {
		copy(buf[pos:], payload)
	}

	written := 0
	for written < len(buf) {
		nw, err := c.Conn.Write(buf[written:])
		written += nw
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *wsConn) sendCloseBestEffort() {
	if !c.closeSent.CompareAndSwap(false, true) {
		return
	}
	_ = c.Conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	_ = c.writeFrame(opcodeClose, nil)
}

func (c *wsConn) Close() error {
	c.sendCloseBestEffort()
	return c.Conn.Close()
}
