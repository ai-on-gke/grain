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

//go:build linux

package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// exitDroppingConn is a daemon connection that loses the Exit frame:
// hostd's write of it succeeds -- the frame is taken off the socket --
// and then the connection drops before the daemon reads it, as when the
// daemon crashes or a dial-out session is torn down at that moment.
type exitDroppingConn struct {
	net.Conn
	pending []byte
	dropped chan struct{}
}

func (c *exitDroppingConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		typ, payload, err := readFrame(c.Conn)
		if err != nil {
			return 0, err
		}
		if typ == frameExit {
			c.Conn.Close()
			close(c.dropped)
			return 0, io.ErrUnexpectedEOF
		}
		var b bytes.Buffer
		_ = writeFrame(&b, typ, payload)
		c.pending = b.Bytes()
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// waitReleased waits for hostd to drop id's spool and listing.
func waitReleased(t *testing.T, ts *testServer, id, spool string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, err := os.Stat(spool)
		ps, _ := ts.client.Processes(context.Background())
		if os.IsNotExist(err) && len(ps) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("5s after %s's exit was acknowledged: spool %s stat = %v, still listed: %+v", id, spool, err, ps)
		}
	}
}

func TestAnExitWrittenButNeverReadIsReadByTheNextAttach(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	dropped := make(chan struct{})
	c.Dial = func(ctx context.Context) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", ts.srv.Socket)
		if err != nil {
			return nil, err
		}
		return &exitDroppingConn{Conn: conn, dropped: dropped}, nil
	}
	var first bytes.Buffer
	_, err := c.Run(context.Background(), Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"},
		Detach: true, Run: "run-ack"}, nil, &first, nil)
	if !errors.Is(err, ErrNoExit) {
		t.Fatalf("Run whose exit frame was lost = %v, want ErrNoExit", err)
	}
	<-dropped
	st := <-started
	if !st.AckExit {
		t.Fatalf("Started = %+v, want AckExit from a hostd with %s", st, FeatureExitAck)
	}
	// hostd notices the connection going and keeps the exit for the next
	// attach -- which is what runhost's reattach does after ErrNoExit.
	var p Process
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		p = listed(t, ts, st.ID)
		if p.Exited && !p.Attached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing 5s after the connection dropped = %+v, want exited and unattached", p)
		}
	}
	var rest bytes.Buffer
	exit, cur, err := ts.client.Attach(context.Background(), st.ID, Cursor{Stdout: int64(first.Len())}, &rest, nil)
	if err != nil {
		t.Fatalf("Attach after the exit frame was lost = %v, want the exit", err)
	}
	if exit.Code != 3 {
		t.Errorf("exit = %+v, want code 3", exit)
	}
	if got := first.String() + rest.String(); got != "out\n" || cur.Stdout != 4 {
		t.Errorf("stdout across both attaches = %q (cursor %+v), want \"out\\n\" once", got, cur)
	}
	// This attach acknowledged the exit: now the spool goes.
	waitReleased(t, ts, st.ID, p.Spool)
}

// rawAttach attaches to id over a bare connection, reading frames up to
// and including the Exit frame, and returns the connection still open.
func rawAttach(t *testing.T, ts *testServer, id string, ackExit bool) (net.Conn, Started) {
	t.Helper()
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := writeJSON(conn, frameRequest, Request{Op: OpAttach, ID: id, AckExit: ackExit}); err != nil {
		t.Fatal(err)
	}
	var st Started
	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			t.Fatalf("reading attach to %s: %v", id, err)
		}
		switch typ {
		case frameStarted:
			_ = json.Unmarshal(payload, &st)
		case frameExit:
			return conn, st
		case frameRefused:
			t.Fatalf("attach to %s refused: %s", id, payload)
		}
	}
}

// finishedDetached starts a detached process and leaves it to end with no
// daemon attached.
func finishedDetached(t *testing.T, ts *testServer) Process {
	t.Helper()
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 0.3"}, Detach: true, Run: "run-raw"}, nil, nil, nil)
		done <- err
	}()
	st := <-started
	cancel()
	<-done
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		p := listed(t, ts, st.ID)
		if p.Exited && !p.Attached {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing = %+v, want exited and unattached", p)
		}
	}
}

func TestADaemonThatDoesNotAskToAcknowledgeHasTheSpoolDroppedOnceTheExitIsWritten(t *testing.T) {
	ts := startServer(t, "", nil)
	p := finishedDetached(t, ts)
	conn, st := rawAttach(t, ts, p.ID, false)
	if st.AckExit {
		t.Errorf("Started = %+v, want no AckExit for a daemon that did not ask", st)
	}
	// An older daemon never sends frameExitAck: hostd must not wait for it.
	waitReleased(t, ts, p.ID, p.Spool)
	conn.Close()
}

func TestAnAcknowledgementFollowedAtOnceByCloseReleasesTheSpool(t *testing.T) {
	// The client acknowledges and closes its end straight away, so hostd
	// can see the acknowledgement and the connection's end together.
	ts := startServer(t, "", nil)
	for i := 0; i < 5; i++ {
		p := finishedDetached(t, ts)
		conn, _ := rawAttach(t, ts, p.ID, true)
		if err := writeFrame(conn, frameExitAck, nil); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		waitReleased(t, ts, p.ID, p.Spool)
	}
}

func TestAnUnacknowledgedExitIsKeptWhileTheConnectionStaysOpen(t *testing.T) {
	ts := startServer(t, "", nil)
	old := exitAckWait
	exitAckWait = 300 * time.Millisecond
	t.Cleanup(func() { exitAckWait = old })
	p := finishedDetached(t, ts)
	conn, st := rawAttach(t, ts, p.ID, true)
	if !st.AckExit {
		t.Fatalf("Started = %+v, want AckExit", st)
	}
	time.Sleep(exitAckWait + 300*time.Millisecond)
	if q := listed(t, ts, p.ID); !q.Exited || q.Attached {
		t.Errorf("listing after an exit nobody acknowledged = %+v, want kept, exited and unattached", q)
	}
	if _, err := os.Stat(p.Spool); err != nil {
		t.Errorf("spool after an exit nobody acknowledged: %v", err)
	}
	conn.Close()
	// The acknowledgement, when it comes over a later attach, drops it.
	conn, _ = rawAttach(t, ts, p.ID, true)
	if err := writeFrame(conn, frameExitAck, nil); err != nil {
		t.Fatal(err)
	}
	waitReleased(t, ts, p.ID, p.Spool)
}
