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

package mcp

// The dial that is an exec (docs/task-lifecycle-sandbox.md, "Transport").
//
// No docker and no cluster here: what is under test is that a command's
// stdio behaves like a net.Conn well enough for an http.Transport to
// round-trip over it, which is the only property the lifecycle reducer
// asks of this. The three runners' own DialStream methods are one line
// each over dialExec, and their invocations are pinned by the execArgs
// tests beside them.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// readWaitLimit is the ceiling on a read these tests expect an answer to,
// and it is a hang-guard rather than a measurement: what is under test is
// that a command's stdio behaves like a net.Conn, never how quickly a
// machine forks a shell and gets a line back through a pipe. Five seconds
// was enough on an idle machine and is not something a CI host running
// ten sandboxes' worth of suites at once can promise (grain/task-317's
// shared-fault list, grain/task-431); a generous ceiling costs a genuinely
// hung exec nothing but a slower red.
const readWaitLimit = 30 * time.Second

func dialSh(t *testing.T, ctx context.Context, script string) net.Conn {
	t.Helper()
	conn, err := dialExec(ctx, "sh", []string{"-c", script}, []string{"grain-connect", "/run/grain/lifecycle.sock"})
	if err != nil {
		t.Fatalf("dialExec: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// The whole point of the seam: an ordinary http.Client, whose transport's
// dial is an exec, talking to something on the far side of it.
func TestAnHTTPTransportRoundTripsOverAnExec(t *testing.T) {
	// The stand-in server reads the request head before it answers, the
	// way a server does. A script that printed its canned response at
	// once raced the transport: under load sh could answer before the
	// transport had finished writing and handed the connection to its
	// read loop, which sees bytes on a connection it still thinks is
	// idle, logs "Unsolicited response received on idle HTTP channel"
	// and throws the connection away.
	//
	// The `cat >/dev/null &` that used to stand in for this read was not
	// a read at all, which is the other half of why the old script never
	// waited for anything: POSIX assigns a background command's stdin to
	// /dev/null when job control is off, which is what /bin/sh does here,
	// so the drain drained nothing and the shell was gone the moment
	// printf was done (grain/task-431).
	const script = `cr=$(printf '\r')
while IFS= read -r line; do
	case $line in "" | "$cr") break ;; esac
done
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n` +
		`Content-Length: 40\r\n\r\n{"protocol":"1","ready":true,"d":"acme"}'`
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialExec(ctx, "sh", []string{"-c", script}, []string{"grain-connect", "/run/grain/lifecycle.sock"})
		},
		MaxConnsPerHost:    1,
		DisableCompression: true,
	}}
	// The host is a label: the transport ignores it and dials the sandbox
	// it was built for, which is why nothing has to resolve it.
	response, err := client.Get("http://lifecycle/v1/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ready":true`) {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
}

// Write is the command's stdin and Read is its stdout, which is the whole
// of the contract.
func TestExecConnWritesStdinAndReadsStdout(t *testing.T) {
	conn := dialSh(t, context.Background(), "cat")
	if _, err := conn.Write([]byte("POST /v1/reduce\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 64)
	if err := conn.SetReadDeadline(time.Now().Add(readWaitLimit)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := string(buf[:n]); got != "POST /v1/reduce\n" {
		t.Errorf("read back %q", got)
	}
}

// Deadlines have to be real. A net.Conn whose SetDeadline silently did
// nothing is one an http.Transport can block on forever, which is why the
// pipes here are os.Pipe's rather than os/exec's copied ones.
func TestExecConnDeadlinesAreReal(t *testing.T) {
	conn := dialSh(t, context.Background(), "sleep 30")
	if err := conn.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	start := time.Now()
	_, err := conn.Read(make([]byte, 16))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("error = %v, want a deadline", err)
	}
	// The error above is the assertion; this only says the read came back
	// at all rather than blocking for ever, which is what a deadline that
	// did nothing would look like. The bound is readWaitLimit's and not the
	// 50ms one, because how long after its deadline a loaded machine
	// schedules this goroutine is not this test's business.
	if elapsed := time.Since(start); elapsed > readWaitLimit {
		t.Errorf("the read blocked for %s past a 50ms deadline", elapsed)
	}
}

// A dial's context is cancelled the moment the dial returns -- it is what
// http.Transport hands DialContext -- so the exec must not be hung off it.
func TestExecConnOutlivesTheDialContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := dialSh(t, ctx, "cat")
	cancel()
	if _, err := conn.Write([]byte("still here\n")); err != nil {
		t.Fatalf("Write after the dial context was cancelled: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(readWaitLimit)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "still here\n" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
}

// A dial whose context is already done hands back nothing, rather than a
// connection nobody is waiting for.
func TestExecDialHonoursAnAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := dialExec(ctx, "sh", []string{"-c", "cat"}, []string{"grain-connect", "/x.sock"})
	if err == nil {
		conn.Close()
		t.Fatal("a cancelled dial returned a connection")
	}
}

// The far end going away is an EOF, and what the exec said on the way is
// what tells "no grain-connect in this guest" from "nothing listening on
// that socket".
func TestExecConnSurfacesWhatTheExecSaid(t *testing.T) {
	conn := dialSh(t, context.Background(), "echo 'granule: exec: connection refused' >&2; exit 1")
	if err := conn.SetReadDeadline(time.Now().Add(readWaitLimit)); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Read(make([]byte, 16))
	if err == nil {
		t.Fatal("a command that exited without writing looked like a live connection")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("%v does not carry the exec's own diagnostic", err)
	}
	if !strings.Contains(err.Error(), "grain-connect /run/grain/lifecycle.sock") {
		t.Errorf("%v does not name what was dialled", err)
	}
}

// Close ends the exec rather than leaving a forwarder behind per
// connection.
func TestExecConnCloseEndsTheCommand(t *testing.T) {
	conn, err := dialExec(context.Background(), "sh", []string{"-c", "sleep 30"}, []string{"grain-connect", "/x.sock"})
	if err != nil {
		t.Fatal(err)
	}
	pid := conn.(*execConn).cmd.Process.Pid
	if err := conn.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := syscallAlive(pid); err == nil {
		t.Errorf("pid %d is still running after Close", pid)
	}
}

func TestExecDialNeedsACommand(t *testing.T) {
	if _, err := dialExec(context.Background(), "sh", nil, nil); err == nil {
		t.Fatal("a dial with nothing to exec was accepted")
	}
}

func TestExecAddrSaysThereIsNoAddress(t *testing.T) {
	conn := dialSh(t, context.Background(), "cat")
	if conn.RemoteAddr().Network() != "exec" {
		t.Errorf("network = %q", conn.RemoteAddr().Network())
	}
	if !strings.Contains(conn.RemoteAddr().String(), "grain-connect") {
		t.Errorf("remote = %q", conn.RemoteAddr())
	}
}

func TestBoundedBufferKeepsTheFrontAndCountsTheRest(t *testing.T) {
	buf := &boundedBuffer{limit: 8}
	fmt.Fprint(buf, "12345")
	fmt.Fprint(buf, "67890")
	if got := buf.String(); got != "12345678... (2 more bytes)" {
		t.Errorf("String() = %q", got)
	}
}
