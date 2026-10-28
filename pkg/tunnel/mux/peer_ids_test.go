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
	"context"
	"io"
	"testing"
	"time"
)

func newIDTestMux(isServer bool) *Mux {
	startID, peerStartID := uint32(2), uint32(1)
	if isServer {
		startID, peerStartID = 1, 2
	}
	return &Mux{isServer: isServer, nextID: startID, peerNextID: peerStartID, peerUnseen: map[uint32]struct{}{}}
}

// Peer ids can arrive out of order: one allocated earlier may send its
// first frame after a later one has. Each id is new exactly once.
func TestClaimPeerIDAcceptsOutOfOrderIDsOnce(t *testing.T) {
	m := newIDTestMux(true)
	for _, step := range []struct {
		id   uint32
		want bool
	}{
		{6, true}, // 2 and 4 are skipped, not seen
		{4, true},
		{6, false},
		{4, false},
		{2, true},
		{2, false},
		{8, true},
		{8, false},
	} {
		if got := m.claimPeerID(step.id); got != step.want {
			t.Fatalf("claimPeerID(%d) = %v, want %v", step.id, got, step.want)
		}
	}
	if len(m.peerUnseen) != 0 {
		t.Fatalf("peerUnseen = %v, want empty", m.peerUnseen)
	}
}

// A gap no real peer would leave -- one huge id -- must not grow the
// unseen set past its bound.
func TestClaimPeerIDBoundsTheUnseenSet(t *testing.T) {
	m := newIDTestMux(false)
	if !m.claimPeerID(1<<31 + 1) {
		t.Fatal("first frame of a new id was not new")
	}
	if len(m.peerUnseen) > maxUnseenPeerIDs {
		t.Fatalf("peerUnseen holds %d ids, bound is %d", len(m.peerUnseen), maxUnseenPeerIDs)
	}
	// The ids just below it are still remembered as unseen; the oldest
	// beyond the bound are dropped and so treated as closed.
	if !m.claimPeerID(1<<31 - 1) {
		t.Fatal("the id just below the highest was not remembered as unseen")
	}
	if m.claimPeerID(1) {
		t.Fatal("an id beyond the bound was still remembered")
	}

	for id := uint32(1<<31 + 3); id < 1<<31+3+4*maxUnseenPeerIDs; id += 4 {
		m.claimPeerID(id)
		if len(m.peerUnseen) > maxUnseenPeerIDs {
			t.Fatalf("peerUnseen holds %d ids, bound is %d", len(m.peerUnseen), maxUnseenPeerIDs)
		}
	}
}

// A session that has opened and closed many streams remembers none of
// them individually, and a late frame for a closed one opens nothing.
func TestMuxForgetsClosedStreams(t *testing.T) {
	c1, c2 := pipeNetConn()
	client := NewMux(c1, Config{IsServer: false})
	server := NewMux(c2, Config{IsServer: true})
	defer client.Close()
	defer server.Close()

	go func() {
		for {
			s, err := server.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(io.Discard, s)
				_ = s.Close()
			}()
		}
	}()

	const n = 200
	var lastID uint32
	for i := 0; i < n; i++ {
		s, err := client.OpenStream(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		lastID = s.ID()
		if _, err := s.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = s.CloseWrite()
		_, _ = io.Copy(io.Discard, s)
		_ = s.Close()
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		server.streamsMu.RLock()
		open, unseen := len(server.streams), len(server.peerUnseen)
		server.streamsMu.RUnlock()
		if open == 0 && unseen == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server holds %d streams and %d unseen ids after %d closed streams", open, unseen, n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A data frame for a stream that has closed must not reopen it.
	server.handleFrame(FrameData, lastID, []byte("late"))
	server.streamsMu.RLock()
	_, reopened := server.streams[lastID]
	server.streamsMu.RUnlock()
	if reopened {
		t.Fatal("a late frame reopened a closed stream")
	}
}

// Claimed ids do not pile up in the eviction order either.
func TestClaimPeerIDCompactsItsOrder(t *testing.T) {
	m := newIDTestMux(true)
	for id := uint32(4); id < 40000; id += 4 {
		m.claimPeerID(id)     // skips id-2
		m.claimPeerID(id - 2) // which then turns up
	}
	if len(m.peerUnseen) != 0 || len(m.peerUnseenOrder) > 64 {
		t.Fatalf("%d unseen, %d in order", len(m.peerUnseen), len(m.peerUnseenOrder))
	}
}
