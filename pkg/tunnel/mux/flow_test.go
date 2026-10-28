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
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A stream nobody reads must stop its sender at the window rather than grow the
// receiver's buffer, and must not hold up other streams on the same session.
func TestFlowControlStalledReaderBoundsBufferAndSparesOtherStreams(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	serverMux := NewMux(c1, Config{IsServer: true})
	defer serverMux.Close()
	clientMux := NewMux(c2, Config{IsServer: false})
	defer clientMux.Close()

	stalled, err := serverMux.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	payload := bytes.Repeat([]byte("x"), 4*initialWindow)
	writeDone := make(chan error, 1)
	go func() {
		_, err := stalled.Write(payload)
		writeDone <- err
	}()

	peerStalled, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}

	// Let the writer run into the window.
	deadline := time.Now().Add(5 * time.Second)
	for {
		peerStalled.mu.Lock()
		buffered := peerStalled.bufLen
		peerStalled.mu.Unlock()
		if buffered == initialWindow {
			break
		}
		if buffered > initialWindow {
			t.Fatalf("receiver buffered %d bytes, more than window %d", buffered, initialWindow)
		}
		if time.Now().After(deadline) {
			t.Fatalf("receiver buffered only %d bytes", buffered)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-writeDone:
		t.Fatalf("write to stalled stream finished early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// A second stream still carries data both ways.
	other, err := serverMux.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	go func() {
		_, _ = other.Write([]byte("ping"))
		_ = other.CloseWrite()
	}()
	peerOther, err := clientMux.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	got, err := io.ReadAll(peerOther)
	if err != nil || string(got) != "ping" {
		t.Fatalf("other stream: got %q, err %v", got, err)
	}

	// Draining the stalled stream releases the writer and delivers everything.
	received, err := io.ReadAll(io.LimitReader(peerStalled, int64(len(payload))))
	if err != nil {
		t.Fatalf("draining stalled stream: %v", err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("received %d bytes, want %d", len(received), len(payload))
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func writeRawFrame(t *testing.T, w io.Writer, frameType byte, streamID uint32, payload []byte) {
	t.Helper()
	if _, err := w.Write(encodeFrame(frameType, streamID, payload)); err != nil {
		t.Fatalf("writing raw frame: %v", err)
	}
}

// drainRaw reads and discards frames from a raw peer connection, answering pings.
func drainRaw(conn net.Conn) {
	var hdr [headerSize]byte
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(hdr[0:4]))
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		if hdr[4] == FramePing {
			go func() { _, _ = conn.Write(encodeFrame(FramePong, 0, payload)) }()
		}
	}
}

// A peer that predates flow control is not throttled by windows it never
// grants, but it gets no more buffering than the window either: opting out of
// flow control must not buy a peer more of our memory.
func TestFlowControlLegacyPeer(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	m := NewMux(c1, Config{IsServer: true})
	defer m.Close()
	go drainRaw(c2)

	// Legacy peer: its first frame is not the flow-control announcement.
	writeRawFrame(t, c2, FramePing, 0, make([]byte, 8))

	// Writes to the legacy peer are not limited by the window.
	out, err := m.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := out.Write(make([]byte, 3*initialWindow))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write to legacy peer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write to legacy peer blocked on flow control")
	}

	// Data beyond the window on an unread stream resets that stream.
	chunk := make([]byte, maxPayloadLen)
	for sent := 0; sent <= initialWindow; sent += len(chunk) {
		writeRawFrame(t, c2, FrameData, 2, chunk)
	}
	in, err := m.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	select {
	case <-in.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("overflowing stream was not reset")
	}
	in.mu.Lock()
	closeErr, bufLen := in.closeErr, in.bufLen
	in.mu.Unlock()
	if !errors.Is(closeErr, ErrBufferFull) || bufLen != 0 {
		t.Fatalf("closeErr=%v bufLen=%d, want ErrBufferFull and an empty buffer", closeErr, bufLen)
	}

	// The session itself is unaffected.
	select {
	case <-m.Done():
		t.Fatalf("session closed: %v", m.getCloseErr())
	default:
	}
}

// A peer that announces flow control and then ignores the window has the
// offending stream reset instead of buffered.
func TestFlowControlWindowViolationResetsStream(t *testing.T) {
	c1, c2 := pipeNetConn()
	defer c1.Close()
	defer c2.Close()

	m := NewMux(c1, Config{IsServer: true})
	defer m.Close()
	// The announcement goes out before drainRaw starts: drainRaw answers
	// the mux's opening ping with a pong of its own, and a pong that
	// reached the mux first would settle negotiation as a legacy peer.
	writeRawFrame(t, c2, FrameWindowUpdate, 0, nil)
	go drainRaw(c2)

	chunk := make([]byte, maxPayloadLen)
	for sent := 0; sent <= initialWindow; sent += len(chunk) {
		writeRawFrame(t, c2, FrameData, 2, chunk)
	}

	in, err := m.AcceptStream()
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	select {
	case <-in.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stream exceeding its window was not reset")
	}
	if _, err := in.Read(make([]byte, 1)); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("Read after overflow: %v, want ErrBufferFull", err)
	}
}
