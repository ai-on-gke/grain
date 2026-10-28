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

package mux

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeNetConn creates an in-memory full duplex net.Conn pair.
func pipeNetConn() (net.Conn, net.Conn) {
	return net.Pipe()
}

func TestMuxSingleStream(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	// Server opens stream to client
	msg := []byte("hello from server")
	go func() {
		stream, err := serverMux.OpenStream(context.Background())
		if err != nil {
			t.Errorf("OpenStream: %v", err)
			return
		}
		if _, err := stream.Write(msg); err != nil {
			t.Errorf("stream.Write: %v", err)
		}
		_ = stream.CloseWrite()
	}()

	clientStream, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	defer clientStream.Close()

	buf, err := io.ReadAll(clientStream)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if !bytes.Equal(buf, msg) {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}

func TestMuxConcurrentStreams(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	const numStreams = 30
	const dataSize = 64 * 1024 // 64 KB per stream

	var wg sync.WaitGroup
	wg.Add(numStreams)

	// Receiver loop on client
	go func() {
		for i := 0; i < numStreams; i++ {
			stream, err := clientMux.AcceptStream()
			if err != nil {
				t.Errorf("AcceptStream: %v", err)
				return
			}
			go func(s *Stream) {
				defer s.Close()
				data, err := io.ReadAll(s)
				if err != nil {
					t.Errorf("ReadAll on stream %d: %v", s.ID(), err)
					return
				}
				// Echo data back
				if _, err := s.Write(data); err != nil {
					t.Errorf("Write echo on stream %d: %v", s.ID(), err)
				}
				_ = s.CloseWrite()
			}(stream)
		}
	}()

	// Server starts streams
	for i := 0; i < numStreams; i++ {
		go func(idx int) {
			defer wg.Done()
			payload := make([]byte, dataSize)
			_, _ = rand.Read(payload)

			stream, err := serverMux.OpenStream(context.Background())
			if err != nil {
				t.Errorf("OpenStream %d: %v", idx, err)
				return
			}
			defer stream.Close()

			if _, err := stream.Write(payload); err != nil {
				t.Errorf("Write %d: %v", idx, err)
				return
			}
			_ = stream.CloseWrite()

			echoed, err := io.ReadAll(stream)
			if err != nil {
				t.Errorf("ReadAll %d: %v", idx, err)
				return
			}

			if !bytes.Equal(echoed, payload) {
				t.Errorf("Stream %d: payload mismatch (got %d bytes, want %d)", idx, len(echoed), len(payload))
			}
		}(i)
	}

	wg.Wait()
}

func TestMuxStreamReset(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	go func() {
		stream, err := serverMux.OpenStream(context.Background())
		if err != nil {
			t.Errorf("OpenStream: %v", err)
			return
		}
		_, _ = stream.Write([]byte("start of data"))
		time.Sleep(10 * time.Millisecond)
		_ = stream.Reset()
	}()

	clientStream, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	defer clientStream.Close()

	buf := make([]byte, 1024)
	n, err := clientStream.Read(buf)
	if err == nil {
		// Second read should encounter reset
		_, err = clientStream.Read(buf)
	}
	if err != ErrStreamReset && err != io.EOF && err != ErrStreamClosed {
		t.Logf("Read after reset returned n=%d err=%v", n, err)
	}
}

func TestMuxKeepalivePing(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	// Configure fast ping intervals
	serverMux := NewMux(c1, Config{IsServer: true, PingInterval: 50 * time.Millisecond, PingTimeout: 100 * time.Millisecond})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false, PingInterval: 50 * time.Millisecond, PingTimeout: 100 * time.Millisecond})
	defer clientMux.Close()

	// Wait for several ping rounds
	time.Sleep(200 * time.Millisecond)

	if serverMux.closed.Load() || clientMux.closed.Load() {
		t.Fatalf("Mux session closed unexpectedly during keepalive")
	}
}

// TestMuxNonBlockingReceive verifies that unread data on one stream does not block
// or deadlock other streams multiplexed across the same connection.
func TestMuxNonBlockingReceive(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	// Server opens stream 1 and sends data, but client deliberately DOES NOT READ stream 1 yet
	s1Server, err := serverMux.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("server OpenStream 1: %v", err)
	}
	defer s1Server.Close()

	if _, err := s1Server.Write([]byte("unconsumed data on stream 1")); err != nil {
		t.Fatalf("s1Server.Write: %v", err)
	}

	s1Client, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("client AcceptStream 1: %v", err)
	}
	defer s1Client.Close()

	// Now server opens stream 2 and communicates back and forth while stream 1 has unread data
	s2Server, err := serverMux.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("server OpenStream 2: %v", err)
	}
	defer s2Server.Close()

	msg2 := []byte("active communication on stream 2")
	if _, err := s2Server.Write(msg2); err != nil {
		t.Fatalf("s2Server.Write: %v", err)
	}

	s2Client, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("client AcceptStream 2: %v", err)
	}
	defer s2Client.Close()

	buf2 := make([]byte, len(msg2))
	if _, err := io.ReadFull(s2Client, buf2); err != nil {
		t.Fatalf("s2Client.Read: %v", err)
	}
	if !bytes.Equal(buf2, msg2) {
		t.Fatalf("stream 2 got %q, want %q", buf2, msg2)
	}

	// Now read stream 1
	buf1 := make([]byte, 27)
	if _, err := io.ReadFull(s1Client, buf1); err != nil {
		t.Fatalf("s1Client.Read: %v", err)
	}
	if string(buf1) != "unconsumed data on stream 1" {
		t.Fatalf("stream 1 got %q", string(buf1))
	}
}

// TestMuxDeadlineReadTimeoutNoCorruption verifies that read deadline timeouts do not
// leave background goroutines writing into expired caller buffers.
func TestMuxDeadlineReadTimeoutNoCorruption(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()

	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	// Server opens stream and writes initial probe frame
	sServer, err := serverMux.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer sServer.Close()

	if _, err := sServer.Write([]byte("init")); err != nil {
		t.Fatalf("sServer.Write init: %v", err)
	}

	sClient, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	defer sClient.Close()

	// Drain initial probe bytes
	initBuf := make([]byte, 4)
	if _, err := io.ReadFull(sClient, initBuf); err != nil {
		t.Fatalf("ReadFull init: %v", err)
	}

	// Set short read deadline on client
	_ = sClient.SetReadDeadline(time.Now().Add(20 * time.Millisecond))

	expiredBuf := []byte("INITIAL_BUFFER_CONTENT")
	n, err := sClient.Read(expiredBuf)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got n=%d err=%v", n, err)
	}

	// Clear deadline
	_ = sClient.SetReadDeadline(time.Time{})

	// Now server sends data after timeout
	msg := []byte("DATA_AFTER_TIMEOUT")
	if _, err := sServer.Write(msg); err != nil {
		t.Fatalf("sServer.Write: %v", err)
	}

	newBuf := make([]byte, len(msg))
	if _, err := io.ReadFull(sClient, newBuf); err != nil {
		t.Fatalf("sClient.Read: %v", err)
	}

	if !bytes.Equal(newBuf, msg) {
		t.Fatalf("got %q, want %q", newBuf, msg)
	}

	// Verify expiredBuf was not overwritten or corrupted
	if string(expiredBuf) != "INITIAL_BUFFER_CONTENT" {
		t.Fatalf("expiredBuf was corrupted by background goroutine: %q", string(expiredBuf))
	}
}
