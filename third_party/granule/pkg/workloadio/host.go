package workloadio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

const (
	dialTimeout = 2 * time.Second

	// retryInterval is how long Forward waits between attempts to reach
	// the guest's stream port: while the VM boots, and again after a
	// connection ends, which is a guest whose granule-container is
	// restarting or rebooting.
	retryInterval = 250 * time.Millisecond
)

// Forward connects to the guest's stream port through cloud-hypervisor's
// vsock socket and writes the workload's stdout and stderr to the writers
// of the same names. A connection that ends is redialled, and a guest
// that is not listening yet is retried: a VM's granule-container comes
// and goes with its restarts, and its backlog keeps what it wrote in
// between.
//
// It stops in one of three ways. Cancelling ctx stops it at once, with
// ctx's error. Closing finish -- the VM has stopped -- stops it redialling:
// the connection it has is read to its end, which is everything the guest
// managed to send, and it returns nil. And a failure to write to stdout
// or stderr is returned as a *WriteError, since that is the caller's to
// act on; the guest going away is never an error, only a reason to dial
// again.
//
// A TypeNote from the guest -- output that was dropped before anything
// attached -- is logged, since it is about the streams rather than in
// them. Every other frame is stepped over.
//
// in, when not nil, is the workload's input: its stdin and terminal size
// are sent on each connection as the guest accepts them (see "Stdin" in
// stdin.go).
func Forward(ctx context.Context, finish <-chan struct{}, socket string, stdout, stderr io.Writer, in *HostInput) error {
	for {
		conn, err := dial(ctx, finish, socket)
		if err != nil || conn == nil {
			return err
		}
		err = pump(ctx, conn, stdout, stderr, in)
		conn.Close()
		var werr *WriteError
		if errors.As(err, &werr) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		case <-time.After(retryInterval):
		}
	}
}

// WriteError is a failure to write a stream to its destination -- a FIFO
// whose reader went away, a full disk -- as opposed to anything about the
// guest.
type WriteError struct {
	Stream string
	Err    error
}

func (e *WriteError) Error() string {
	return fmt.Sprintf("writing the workload's %s: %v", e.Stream, e.Err)
}

func (e *WriteError) Unwrap() error { return e.Err }

// pump reads one connection to its end.
func pump(ctx context.Context, conn net.Conn, stdout, stderr io.Writer, in *HostInput) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	br := bufio.NewReader(conn)
	resp, err := ReadOpening(br)
	if err != nil {
		return err
	}
	if in != nil {
		// Stopped, and waited for, before the next connection's feed
		// starts, so that two never take turns at the same input. The
		// caller's Close of conn is what ends a write it is blocked in;
		// closing it here first makes sure of that whoever returns.
		done := make(chan struct{})
		fed := make(chan struct{})
		go func() {
			defer close(fed)
			in.feed(conn, resp, done)
		}()
		defer func() {
			close(done)
			conn.Close()
			<-fed
		}()
	}
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			return err
		}
		switch typ {
		case execwire.TypeStdout:
			if _, err := stdout.Write(payload); err != nil {
				return &WriteError{"stdout", err}
			}
		case execwire.TypeStderr:
			if _, err := stderr.Write(payload); err != nil {
				return &WriteError{"stderr", err}
			}
		case execwire.TypeNote:
			log.Printf("workload output: %s", payload)
		}
	}
}

// dial connects to the guest's stream port, retrying until it is
// listening or ctx is done. Once finish is closed it gives up instead,
// returning neither a connection nor an error.
func dial(ctx context.Context, finish <-chan struct{}, socket string) (net.Conn, error) {
	for {
		conn, err := dialOnce(socket)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-finish:
			return nil, nil
		case <-time.After(retryInterval):
		}
	}
}

// dialOnce is cloud-hypervisor's hybrid vsock handshake -- "CONNECT
// <port>\n", answered by "OK <port>\n" -- for Port. See pkg/guestexec's
// dialOnce, which does the same for granule-agent's.
func dialOnce(socket string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socket, dialTimeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", Port); err != nil {
		conn.Close()
		return nil, err
	}
	// Byte at a time, so that nothing past the handshake's own line --
	// the guest's opening line may follow it at once -- is read here.
	var line []byte
	var b [1]byte
	for len(line) < 64 {
		if _, err := conn.Read(b[:]); err != nil {
			conn.Close()
			return nil, fmt.Errorf("vsock port %d is not being listened on inside the guest yet: %w", Port, err)
		}
		line = append(line, b[0])
		if b[0] == '\n' {
			break
		}
	}
	if !strings.HasPrefix(string(line), "OK ") {
		conn.Close()
		return nil, fmt.Errorf("cloud-hypervisor refused the vsock connection: %q", strings.TrimSpace(string(line)))
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// OpenOutputs opens the destinations a caller named for the workload's
// stdout and stderr: regular files are created or appended to, and a
// FIFO is opened for writing, which waits for its reader. Both are opened
// at once, so that a reader which opens them one at a time, in either
// order, does not wait on this for the one it is not opening. An empty
// path discards that stream; the same path for both opens it once.
//
// The returned close function closes whatever was opened.
func OpenOutputs(ctx context.Context, stdoutPath, stderrPath string) (stdout, stderr io.Writer, closeAll func(), err error) {
	type result struct {
		f   *os.File
		err error
	}
	open := func(path string) <-chan result {
		ch := make(chan result, 1)
		if path == "" {
			ch <- result{}
			return ch
		}
		go func() {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
			ch <- result{f, err}
		}()
		return ch
	}
	outCh := open(stdoutPath)
	errCh := outCh
	if stderrPath != stdoutPath {
		errCh = open(stderrPath)
	}

	var files []*os.File
	closeAll = func() {
		for _, f := range files {
			f.Close()
		}
	}
	wait := func(ch <-chan result, path string) (io.Writer, error) {
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			if r.f == nil {
				return io.Discard, nil
			}
			files = append(files, r.f)
			return r.f, nil
		case <-ctx.Done():
			// The open still blocked in its goroutine is abandoned; it
			// lasts no longer than this process does.
			return nil, fmt.Errorf("waiting for a reader on %s: %w", path, ctx.Err())
		}
	}
	if stdout, err = wait(outCh, stdoutPath); err != nil {
		return nil, nil, nil, err
	}
	if stderrPath == stdoutPath {
		return stdout, stdout, closeAll, nil
	}
	if stderr, err = wait(errCh, stderrPath); err != nil {
		closeAll()
		return nil, nil, nil, err
	}
	return stdout, stderr, closeAll, nil
}

// IsBrokenReader reports whether err is what writing to a FIFO or pipe
// whose reader has gone looks like -- the case a caller reopens the
// destination for, rather than giving up on it.
func IsBrokenReader(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
