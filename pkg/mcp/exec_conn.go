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

// A sandbox's exec as a net.Conn.
//
// Grain has no route to a sandbox's guest, and deliberately has not had
// one since the SSH-to-a-forwarded-port transport went away: pkg/granule
// "never resolves an address for a VM, because nothing here needs one",
// and under NetModeFlat the guest answers to the namespace's own address,
// so there is not an address to dial even from inside. A guest is reached
// by exec'ing into its own container -- `docker exec <vm> kontur exec`,
// `kubectl exec`, `docker exec` -- which is what the three runners in
// this package do.
//
// So "the definition listens on a REST interface" cannot mean "grain
// connects to a port". It means the definition binds a unix socket inside
// the guest and grain dials it by exec'ing a one-connection forwarder
// there, treating that exec's stdio as a connection: Read is the
// command's stdout, Write is its stdin (docs/task-lifecycle-sandbox.md,
// "Transport"). Every backend can implement it, because every backend
// already has an exec.
//
// The forwarder is grain's own (cmd/grain-connect), installed in the
// stock sandbox guest, not something a declaration provides -- and it is
// provisional: envd's process API is reached over exactly this dial
// (docs/envd-adoption.md), so the forwarder becomes one more thing envd
// provides. Which side of the socket grain arrives from is not something
// a repository writing a reducer can tell either way.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// execConnStderrLimit bounds what is kept of a forwarder's stderr. It
// exists to explain a dial that failed, so the first couple of kilobytes
// are all of it that matters, and a command that decided to be chatty
// must not grow in memory for as long as the connection lives.
const execConnStderrLimit = 2 << 10

// diagnosticGrace is how long annotate waits for the exec's stderr to
// finish arriving before reporting an error without it. Long enough for a
// goroutine that already has the bytes to be scheduled, short enough that
// a forked grandchild holding the pipe open costs one pause rather than
// the connection.
const diagnosticGrace = 250 * time.Millisecond

// DialStream opens a connection to something inside the guest by exec'ing
// argv there.
//
// The returned conn is the exec: writing to it writes the command's
// stdin, reading from it reads the command's stdout, and closing it ends
// the command. One exec per connection, which is why an http.Transport
// over this wants MaxConnsPerHost set -- a request beyond the cap should
// queue behind it rather than boot another exec.
func (r *DockerExecRunner) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	return dialExec(ctx, "docker", r.execArgs(argv, true), argv)
}

// DialStream is DockerExecRunner.DialStream under the k8s backend, where
// the exec is `kubectl exec`.
func (r *KubectlExecRunner) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	return dialExec(ctx, r.binary(), r.dialArgs(argv), argv)
}

// DialStream is DockerExecRunner.DialStream under the container backend,
// where there is no VM and the exec goes straight into the container.
func (r *ContainerExecRunner) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	return dialExec(ctx, r.binary(), r.execArgs(argv, true), argv)
}

// dialExec starts local (docker, kubectl) with args and hands back its
// stdio. described is what the caller asked to run inside the guest, kept
// only so that an address and an error can name it.
func dialExec(ctx context.Context, local string, args, described []string) (net.Conn, error) {
	if len(described) == 0 {
		return nil, errors.New("dial: no command to exec in the guest")
	}
	// os.Pipe rather than cmd.StdinPipe/StdoutPipe: os/exec hands a
	// *os.File straight to the child instead of copying it through a
	// goroutine, and keeping our own ends as *os.File is what makes
	// SetDeadline below a real deadline rather than a lie. A net.Conn
	// whose deadlines silently did nothing would be one an http.Transport
	// could block on forever.
	toGuest, ourWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("dial: opening the stdin pipe: %w", err)
	}
	ourRead, fromGuest, err := os.Pipe()
	if err != nil {
		toGuest.Close()
		ourWrite.Close()
		return nil, fmt.Errorf("dial: opening the stdout pipe: %w", err)
	}
	// Stderr gets a pipe of its own for one specific reason: os/exec
	// copies a non-*os.File stderr on a goroutine, and Wait does not
	// return until that goroutine does -- which means until every process
	// holding the write end has gone, grandchildren included. Ending a
	// connection must not wait on something the exec happened to fork, so
	// the bounded copy below is ours and nothing waits on it.
	diagnostics, childErr, err := os.Pipe()
	if err != nil {
		toGuest.Close()
		ourWrite.Close()
		ourRead.Close()
		fromGuest.Close()
		return nil, fmt.Errorf("dial: opening the stderr pipe: %w", err)
	}

	// The command outlives this call and is ended by Close, so it gets a
	// cancel of its own rather than the dial's context. A dial context
	// that was cancelled the moment the dial returned -- which is what
	// http.Transport's is -- would otherwise kill the connection it just
	// opened.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(runCtx, local, args...)
	procgroup.Prepare(cmd)
	stderr := &boundedBuffer{limit: execConnStderrLimit}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = toGuest, fromGuest, childErr

	if err := cmd.Start(); err != nil {
		cancel()
		for _, f := range []*os.File{toGuest, ourWrite, ourRead, fromGuest, diagnostics, childErr} {
			f.Close()
		}
		return nil, fmt.Errorf("dial: starting %s: %w", local, err)
	}
	// Our copies of the child's ends, so that the child's exit is an EOF
	// on our read and our close is an EOF on its stdin.
	toGuest.Close()
	fromGuest.Close()
	childErr.Close()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(stderr, diagnostics)
	}()

	conn := &execConn{
		cmd: cmd, cancel: cancel, in: ourWrite, out: ourRead, stderr: stderr,
		diagnostics: diagnostics, drained: drained, addr: execAddr(strings.Join(described, " ")),
	}
	// If the dial's context is already done, honour it rather than
	// handing back a connection nobody asked for any more.
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("dial: %w", err)
	}
	return conn, nil
}

// execConn is one exec, as a connection.
type execConn struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	in     *os.File
	out    *os.File
	stderr *boundedBuffer
	// diagnostics is the read end of the exec's stderr, closed so that
	// the copy above stops rather than outliving the connection.
	diagnostics *os.File
	// drained closes once the exec's stderr has been copied into stderr
	// above. annotate waits on it, briefly, because the diagnostic is the
	// whole reason that copy exists and a read that reached EOF first
	// would otherwise report a bare io.EOF for a command that had just
	// explained itself.
	drained <-chan struct{}
	addr    execAddr

	closeOnce sync.Once
	closeErr  error
}

func (c *execConn) Read(p []byte) (int, error) {
	n, err := c.out.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, c.annotate("read", err)
	}
	if errors.Is(err, io.EOF) {
		// The far end going away is an ordinary EOF to whoever is reading
		// a connection, and something else entirely when the forwarder
		// never got there: a guest with no grain-connect in it, a socket
		// nothing is listening on, an exec that could not authenticate.
		// Saying which costs one wait on a process that has already
		// stopped writing.
		return n, c.annotate("read", err)
	}
	return n, err
}

func (c *execConn) Write(p []byte) (int, error) {
	n, err := c.in.Write(p)
	if err != nil {
		return n, c.annotate("write", err)
	}
	return n, nil
}

// annotate turns a pipe's own error into one that says what the exec had
// to say for itself, which is the difference between "file already
// closed" and "granule: exec: dial tcp: connection refused".
//
// It waits for the stderr copy first, and has to: the exec writes its
// complaint and exits, which closes stdout, so a reader can reach EOF
// before that copy has run at all -- and then the one error that needed
// the explanation is the one that would not carry it.
//
// Bounded, because the write end of that pipe is held by the exec and by
// anything it forked, so an unbounded wait here is a read that hangs on a
// grandchild. What is being waited for has already been written by the
// time stdout closed; this is scheduling, not I/O.
func (c *execConn) annotate(op string, err error) error {
	if c.drained != nil {
		select {
		case <-c.drained:
		case <-time.After(diagnosticGrace):
		}
	}
	if diagnostic := strings.TrimSpace(c.stderr.String()); diagnostic != "" {
		return fmt.Errorf("%s %s: %w: %s", op, c.addr, err, diagnostic)
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	return fmt.Errorf("%s %s: %w", op, c.addr, err)
}

// Close ends the exec. Stdin is closed first so that a forwarder which
// would rather finish flushing gets the chance, and the process is then
// cancelled rather than waited on: this is a connection being hung up,
// not a command whose exit status anybody wants.
func (c *execConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.in.Close()
		c.cancel()
		_ = c.cmd.Wait()
		for _, f := range []*os.File{c.out, c.diagnostics} {
			if err := f.Close(); c.closeErr == nil && !errors.Is(err, os.ErrClosed) {
				c.closeErr = err
			}
		}
	})
	return c.closeErr
}

func (c *execConn) LocalAddr() net.Addr  { return execAddr("grain") }
func (c *execConn) RemoteAddr() net.Addr { return c.addr }

func (c *execConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}
func (c *execConn) SetReadDeadline(t time.Time) error  { return c.out.SetReadDeadline(t) }
func (c *execConn) SetWriteDeadline(t time.Time) error { return c.in.SetWriteDeadline(t) }

// execAddr names one end of the dial. Not routable, and deliberately
// says so: there is no address for a guest, which is the whole reason
// this file exists.
type execAddr string

func (a execAddr) Network() string { return "exec" }
func (a execAddr) String() string  { return string(a) }

// boundedBuffer keeps the first limit bytes written to it and counts the
// rest. Concurrency-safe because os/exec writes to it from its own
// goroutine while a reader may be asking what went wrong.
type boundedBuffer struct {
	mu      sync.Mutex
	limit   int
	buf     bytes.Buffer
	dropped int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf.Write(p[:room])
		b.dropped += len(p) - room
	} else {
		b.dropped += len(p)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return b.buf.String()
	}
	return fmt.Sprintf("%s... (%d more bytes)", b.buf.String(), b.dropped)
}
