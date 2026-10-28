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
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Regression tests for audit finding SA1-H2 (bwsalmon/silo
// docs/security/audits/2026-10-16-hub-audit-1.md, ported from silo's copy of
// this package): one peer must not be able to grow the other side's memory or
// goroutines without bound.

// waitClosed waits for m's session to end and returns its error.
func waitClosed(t *testing.T, m *Mux, d time.Duration) error {
	t.Helper()
	select {
	case <-m.Done():
		return m.getCloseErr()
	case <-time.After(d):
		t.Fatalf("session still open after %v", d)
		return nil
	}
}

// readDiscard reads and discards frames from a raw peer connection without
// answering anything.
func readDiscard(conn net.Conn) {
	var hdr [headerSize]byte
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(hdr[0:4]))); err != nil {
			return
		}
	}
}

// A peer that floods pings and never reads used to park one goroutine, and
// one copy of the ping's payload, per ping.
func TestPingFloodFromNonReadingPeerIsBounded(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	m := NewMux(hub, Config{IsServer: true, PingInterval: time.Hour, PingTimeout: 5 * time.Second})
	defer m.Close()

	before := runtime.NumGoroutine()
	payload := make([]byte, maxPayloadLen)
	for i := 0; i < 2000; i++ {
		if _, err := peer.Write(encodeFrame(FramePing, 0, payload)); err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
	}
	if grown := runtime.NumGoroutine() - before; grown > 20 {
		t.Fatalf("goroutines grew by %d for 2000 pings", grown)
	}
}

// A peer that stops reading has the session closed once a write has waited
// for the ping timeout, rather than holding the session's writers forever.
func TestNonReadingPeerHitsWriteDeadline(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	m := NewMux(hub, Config{IsServer: true, PingInterval: time.Hour, PingTimeout: 100 * time.Millisecond})
	defer m.Close()

	err := waitClosed(t, m, 5*time.Second)
	if !strings.Contains(err.Error(), "write error") {
		t.Fatalf("session closed with %v, want a write error", err)
	}
}

// Pongs that answer none of our pings do not count: a peer that never
// answers is timed out however many pongs of its own it sends.
func TestUnsolicitedPongsDoNotKeepSessionAlive(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	m := NewMux(hub, Config{IsServer: true, PingInterval: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond})
	defer m.Close()
	go readDiscard(peer)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		var guess [8]byte
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			binary.BigEndian.PutUint64(guess[:], uint64(time.Now().UnixNano()))
			if _, err := peer.Write(encodeFrame(FramePong, 0, nil)); err != nil {
				return
			}
			if _, err := peer.Write(encodeFrame(FramePong, 0, guess[:])); err != nil {
				return
			}
		}
	}()

	err := waitClosed(t, m, 5*time.Second)
	if !strings.Contains(err.Error(), "ping timeout") {
		t.Fatalf("session closed with %v, want a ping timeout", err)
	}
}

// A peer that opts out of flow control (its first frame is not the window
// update) used to be allowed 16 MiB per stream; it now gets the window.
func TestLegacyPeerBuffersNoMoreThanWindow(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	go readDiscard(peer)
	m := NewMux(hub, Config{IsServer: true})
	defer m.Close()

	payload := make([]byte, maxPayloadLen)
	for sent := 0; sent+len(payload) <= 16<<20; sent += len(payload) {
		if _, err := peer.Write(encodeFrame(FrameData, 2, payload)); err != nil {
			t.Fatal(err)
		}
	}
	st, err := m.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	n, closeErr := st.bufLen, st.closeErr
	st.mu.Unlock()
	if n > initialWindow || !errors.Is(closeErr, ErrBufferFull) {
		t.Fatalf("buffered %d (closeErr %v), want the stream reset at the %d window", n, closeErr, initialWindow)
	}
	if got := m.buffered.Load(); got != 0 {
		t.Fatalf("session accounts %d buffered bytes after the reset, want 0", got)
	}
}

// settle returns once the mux has handled every frame the peer wrote before
// it: net.Pipe's Write returns only when the read loop has read the sentinel,
// and the read loop handles frames in order.
func settle(t *testing.T, peer net.Conn) {
	t.Helper()
	writeRawFrame(t, peer, FramePing, 0, nil)
}

// Unread data across all of a session's streams is capped, not just per
// stream: streams that would take the session past it are reset.
func TestSessionCapsBufferedBytes(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	m := NewMux(hub, Config{IsServer: true, MaxBufferedBytes: 4 * maxPayloadLen})
	defer m.Close()
	writeRawFrame(t, peer, FrameWindowUpdate, 0, nil)
	go readDiscard(peer)

	payload := make([]byte, maxPayloadLen)
	for id := uint32(2); id <= 20; id += 2 {
		writeRawFrame(t, peer, FrameData, id, payload)
	}
	settle(t, peer)

	if got := m.buffered.Load(); got > 4*maxPayloadLen {
		t.Fatalf("session buffered %d bytes, cap %d", got, 4*maxPayloadLen)
	}
	var reset int
	for i := 0; i < 10; i++ {
		st, err := m.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		st.mu.Lock()
		if errors.Is(st.closeErr, ErrBufferFull) {
			reset++
		}
		st.mu.Unlock()
	}
	if reset != 6 {
		t.Fatalf("%d of 10 streams reset, want the 6 past the cap", reset)
	}
	select {
	case <-m.Done():
		t.Fatalf("session closed: %v", m.getCloseErr())
	default:
	}
}

// A peer cannot open more streams at once than the session allows.
func TestSessionCapsStreams(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	m := NewMux(hub, Config{IsServer: true, MaxStreams: 4})
	defer m.Close()
	writeRawFrame(t, peer, FrameWindowUpdate, 0, nil)
	go readDiscard(peer)

	for id := uint32(2); id <= 20; id += 2 {
		writeRawFrame(t, peer, FrameData, id, []byte("x"))
	}
	settle(t, peer)

	m.streamsMu.RLock()
	open := len(m.streams)
	m.streamsMu.RUnlock()
	if open != 4 || len(m.acceptCh) != 4 {
		t.Fatalf("%d streams open, %d to accept; want 4 of each", open, len(m.acceptCh))
	}
}

// Resets are queued for one writer rather than spawned a goroutine each; a
// peer that provokes more than the queue holds without reading them loses its
// session.
func TestControlQueueOverflowClosesSession(t *testing.T) {
	hub, peer := net.Pipe()
	defer peer.Close()
	m := NewMux(hub, Config{IsServer: true, MaxStreams: 1, PingInterval: time.Hour, PingTimeout: time.Minute})
	defer m.Close()

	before := runtime.NumGoroutine()
	for id := uint32(2); id < 2*(ctrlQueueLen+16); id += 2 {
		if _, err := peer.Write(encodeFrame(FrameData, id, []byte("x"))); err != nil {
			break
		}
	}
	err := waitClosed(t, m, 5*time.Second)
	if !strings.Contains(err.Error(), "control queue overflow") {
		t.Fatalf("session closed with %v, want a control queue overflow", err)
	}
	if grown := runtime.NumGoroutine() - before; grown > 20 {
		t.Fatalf("goroutines grew by %d", grown)
	}
}
