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
	"encoding/binary"
	"net"
	"syscall"
	"testing"
	"time"
)

// peekedExit reports whether the frames waiting unread in buf end with
// an Exit frame, whole.
func peekedExit(buf []byte) bool {
	for len(buf) >= 5 {
		n := int(binary.BigEndian.Uint32(buf[1:5]))
		if len(buf) < 5+n {
			return false
		}
		if buf[0] == frameExit {
			return true
		}
		buf = buf[5+n:]
	}
	return false
}

// A daemon that dies -- its socket closed by the kernel -- with the Exit
// frame written into its receive buffer but never read has not got the
// exit, and the next attach must still find it (task 4908). Whether the
// frames are there is watched with MSG_PEEK, which leaves them unread, so
// the drop always lands after hostd's write and before the daemon's read.
func TestADaemonThatDiesWithTheExitUnreadLeavesItForTheNextAttach(t *testing.T) {
	ts := startServer(t, "", nil)
	p := finishedDetached(t, ts)
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeJSON(conn, frameRequest, Request{Op: OpAttach, ID: p.ID}); err != nil {
		t.Fatal(err)
	}
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<16)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var n int
		_ = raw.Read(func(fd uintptr) bool {
			n, _, _ = syscall.Recvfrom(int(fd), buf, syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
			return true
		})
		if n > 0 && peekedExit(buf[:n]) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no Exit frame waiting on the attach to %s after 5s", p.ID)
		}
	}
	conn.Close() // the daemon dies without reading it
	var out bytes.Buffer
	exit, _, err := ts.client.Attach(context.Background(), p.ID, Cursor{}, &out, nil)
	if err != nil {
		t.Fatalf("Attach after a daemon died with the Exit unread = %v, want the exit", err)
	}
	if exit.Code != 0 || out.String() != "done\n" {
		t.Errorf("Attach = %+v, stdout %q; want code 0 and \"done\\n\"", exit, out.String())
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
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 0.3; echo done"}, Detach: true, Run: "run-unread"}, nil, nil, nil)
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
