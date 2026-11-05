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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptRFC6455Example(t *testing.T) {
	// RFC 6455 section 1.3.
	if got, want := Accept("dGhlIHNhbXBsZSBub25jZQ=="), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; got != want {
		t.Fatalf("Accept = %q, want %q", got, want)
	}
}

func TestSetRequestHeaders(t *testing.T) {
	h := http.Header{}
	SetRequestHeaders(h)
	if h.Get("Upgrade") != "websocket" || h.Get("Connection") != "Upgrade" || h.Get("Sec-WebSocket-Version") != "13" {
		t.Fatalf("headers = %v", h)
	}
	if len(h.Get("Sec-WebSocket-Key")) != 24 {
		t.Fatalf("Sec-WebSocket-Key = %q, want 16 bytes base64", h.Get("Sec-WebSocket-Key"))
	}
	r := &http.Request{Header: h}
	if got, want := AcceptHeader(r), "Sec-WebSocket-Accept: "+Accept(h.Get("Sec-WebSocket-Key"))+"\r\n"; got != want {
		t.Fatalf("AcceptHeader = %q, want %q", got, want)
	}
	if AcceptHeader(&http.Request{Header: http.Header{}}) != "" {
		t.Fatal("AcceptHeader without a key should be empty")
	}
}

func TestBinaryFramingRoundTripAndMasking(t *testing.T) {
	c1, c2 := net.Pipe()
	client := NewClientConn(c1)
	server := NewServerConn(c2)
	defer client.Close()
	defer server.Close()

	payload := make([]byte, 100<<10) // 100 KiB -> spans multiple 32 KiB frames
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := client.Write(payload)
		errCh <- err
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("server ReadFull: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client Write: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("server received payload mismatch")
	}

	// Server -> Client unmasked response.
	reply := []byte("pong-from-server-payload")
	go func() {
		_, err := server.Write(reply)
		errCh <- err
	}()
	gotReply := make([]byte, len(reply))
	if _, err := io.ReadFull(client, gotReply); err != nil {
		t.Fatalf("client ReadFull: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server Write: %v", err)
	}
	if !bytes.Equal(gotReply, reply) {
		t.Fatalf("gotReply = %q, want %q", gotReply, reply)
	}
}

func TestServerRejectsUnmaskedClientFrame(t *testing.T) {
	rawClient, rawServer := net.Pipe()
	defer rawClient.Close()
	server := NewServerConn(rawServer)
	defer server.Close()

	go func() {
		// Unmasked binary frame: FIN|0x2, len=3, "abc"
		_, _ = rawClient.Write([]byte{0x82, 0x03, 'a', 'b', 'c'})
	}()

	buf := make([]byte, 8)
	if _, err := server.Read(buf); err == nil || !strings.Contains(err.Error(), "unmasked") {
		t.Fatalf("server.Read err = %v, want unmasked error", err)
	}
}

func TestUpgradeHTTPAndPingClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r, http.Header{"X-Test-Session": []string{"sess-1"}})
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}))
	defer srv.Close()

	raw, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer raw.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/connect", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	SetRequestHeaders(req.Header)
	secKey := req.Header.Get("Sec-WebSocket-Key")
	if err := req.Write(raw); err != nil {
		t.Fatalf("req.Write: %v", err)
	}

	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("StatusCode = %d, want 101", resp.StatusCode)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != Accept(secKey) {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", resp.Header.Get("Sec-WebSocket-Accept"), Accept(secKey))
	}
	if resp.Header.Get("X-Test-Session") != "sess-1" {
		t.Fatalf("X-Test-Session = %q, want sess-1", resp.Header.Get("X-Test-Session"))
	}

	ws := NewClientConn(BufferedConn(raw, br))
	msg := []byte("hello-over-upgraded-websocket")
	if _, err := ws.Write(msg); err != nil {
		t.Fatalf("ws.Write: %v", err)
	}
	echo := make([]byte, len(msg))
	if _, err := io.ReadFull(ws, echo); err != nil {
		t.Fatalf("ws.ReadFull: %v", err)
	}
	if !bytes.Equal(echo, msg) {
		t.Fatalf("echo = %q, want %q", echo, msg)
	}
	_ = ws.Close()
}
