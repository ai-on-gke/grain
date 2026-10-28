package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Client is the daemon's side of the socket.
type Client struct {
	// Socket is the path of hostd's socket inside the daemon's container,
	// which is the same path it has on the host.
	Socket string
	// Detach, for a detached start or an attach, reports whether a
	// cancelled ctx is the daemon stopping rather than the run being
	// cancelled: the connection is then dropped with the process left
	// running for the next daemon, and ErrDetached returned, as a run
	// host's tail does (runhost.Host.Detach). Nil means never.
	Detach func(ctx context.Context) bool
	// Build is this daemon's version, for Hello.
	Build string
	// OnStarted, when set, is told the pid and id of a process Run has
	// started, before any of its output: a detached start's id is what
	// the daemon re-attaches with.
	OnStarted func(Started)
}

// ErrDetached is Run's and Attach's error for a detached process left
// running because the daemon is stopping (Client.Detach).
var ErrDetached = errors.New("hostd: detached; the process keeps running for the next daemon")

// Hello exchanges versions with hostd. A hostd that predates OpHello is
// protocol 1 (legacyHello). The caller decides with Compatible.
func (c Client) Hello(ctx context.Context) (Hello, error) {
	mine := ThisHello(c.Build)
	var out Hello
	err := c.oneShot(ctx, Request{Op: OpHello, Hello: &mine}, frameHello, &out)
	var r *RefusedError
	if errors.As(err, &r) && strings.HasPrefix(r.Reason, "unknown op") {
		return legacyHello, nil
	}
	return out, err
}

// Processes is hostd's listing of what it runs (OpProcesses).
func (c Client) Processes(ctx context.Context) ([]Process, error) {
	var out []Process
	err := c.oneShot(ctx, Request{Op: OpProcesses}, frameProcesses, &out)
	return out, err
}

// Cancel releases the process id: its group is killed if it still runs,
// and its spool dropped. An id hostd no longer has is not an error.
func (c Client) Cancel(ctx context.Context, id string) error {
	return c.oneShot(ctx, Request{Op: OpCancel, ID: id}, frameOK, nil)
}

// Drain asks hostd to drain for an upgrade (Server.Drain).
func (c Client) Drain(ctx context.Context) error {
	return c.oneShot(ctx, Request{Op: OpDrain}, frameOK, nil)
}

// Cutover asks hostd to cut over to a new build at the first moment
// nothing runs (Server.Cutover); a hostd that predates it drains.
func (c Client) Cutover(ctx context.Context) error {
	return c.oneShot(ctx, Request{Op: OpDrain, Idle: true}, frameOK, nil)
}

// SocketOwner asks hostd which process in a process tree it started holds
// socket inode open (OpSocketOwner). It fails closed when hostd is
// unreachable, speaks an older protocol that does not serve OpSocketOwner,
// or finds no process of its own holding the socket.
func (c Client) SocketOwner(ctx context.Context, inode uint64) (int, error) {
	if inode == 0 {
		return 0, errors.New("hostd: socket inode is 0")
	}
	var out SocketOwner
	err := c.oneShot(ctx, Request{Op: OpSocketOwner, Inode: inode}, frameSocketOwner, &out)
	var r *RefusedError
	if errors.As(err, &r) && strings.HasPrefix(r.Reason, "unknown op") {
		return 0, errors.New("grain hostd is on an older protocol that cannot check socket ownership")
	}
	if err != nil {
		return 0, err
	}
	if out.PID <= 0 {
		return 0, fmt.Errorf("no process started by grain hostd holds socket %d", inode)
	}
	return out.PID, nil
}

// oneShot sends req and reads one answer frame of type want into out.
func (c Client) oneShot(ctx context.Context, req Request, want byte, out any) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := writeJSON(conn, frameRequest, req); err != nil {
		return err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return err
	}
	switch typ {
	case want:
		if out == nil {
			return nil
		}
		return json.Unmarshal(payload, out)
	case frameRefused:
		return refusal(payload)
	}
	return fmt.Errorf("hostd: unexpected frame %q", typ)
}

// dialRetry is how long dial keeps trying a socket that is not there or
// not listening, within the caller's deadline: a hostd restarting -- a
// drain onto a new binary, systemd's Restart= -- removes its socket and
// makes it again a second or two later, and a request in that window
// should wait it out rather than fail.
var dialRetry = 3 * time.Second

func (c Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	give := time.Now().Add(dialRetry)
	for wait := 100 * time.Millisecond; ; wait *= 2 {
		conn, err := d.DialContext(ctx, "unix", c.Socket)
		if err == nil {
			return conn, nil
		}
		down := errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
		if !down || time.Now().Add(wait).After(give) || ctx.Err() != nil {
			return nil, c.unreachable(err, down)
		}
		select {
		case <-ctx.Done():
			return nil, c.unreachable(err, down)
		case <-time.After(wait):
		}
	}
}

// unreachable says why hostd did not answer, and what to do about it.
func (c Client) unreachable(err error, down bool) error {
	if !down {
		return fmt.Errorf("grain hostd is not reachable at %s: %w", c.Socket, err)
	}
	why := "grain hostd is not running (grain-hostd.service is stopped, failing, or restarting for an upgrade)"
	if socketDirRemoved(c.Socket) {
		// The directory this process sees is not the one hostd makes its
		// socket in any more: a container that mounted it before the
		// host removed and remade it keeps the old, empty one.
		why = "the socket directory this process mounted was removed on the host since it started, so a socket hostd makes there now is not visible here; restart grain-daemon to mount it again"
	}
	return fmt.Errorf("grain hostd is not reachable at %s: %s; on the host, systemctl status grain-hostd and journalctl -u grain-hostd say why (%w)", c.Socket, why, err)
}

// Offers asks hostd which entries this host's allowlist offers.
func (c Client) Offers(ctx context.Context) ([]Offer, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := writeJSON(conn, frameRequest, Request{Op: OpList}); err != nil {
		return nil, err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return nil, err
	}
	switch typ {
	case frameOffers:
		var out []Offer
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, err
		}
		return out, nil
	case frameRefused:
		return nil, refusal(payload)
	}
	return nil, fmt.Errorf("hostd: unexpected frame %q", typ)
}

func refusal(payload []byte) error {
	var r Refusal
	_ = json.Unmarshal(payload, &r)
	return &RefusedError{Reason: r.Reason, Draining: r.Draining}
}

// ErrNoExit is returned when the connection ended without hostd saying how
// the process did.
var ErrNoExit = errors.New("hostd: the connection closed before the process's exit status arrived")

// Run starts req on the host and streams its stdio until it exits. stdin
// may be nil. A refused request is a *RefusedError and never ran.
//
// Cancelling ctx kills the process's whole group on the host -- the same
// thing cancelling agent.LocalHost's context does -- and Run then waits up
// to WaitDelay plus a second for hostd's exit status before giving up on
// the connection. A ctx deadline is also sent as the request's Deadline,
// so hostd enforces it even if this side goes away.
func (c Client) Run(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) (Exit, error) {
	req.Op = OpStart
	if dl, ok := ctx.Deadline(); ok && (req.Deadline.IsZero() || dl.Before(req.Deadline)) {
		req.Deadline = dl
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return Exit{Code: -1}, err
	}
	defer conn.Close()

	var sendMu sync.Mutex
	send := func(typ byte, p []byte) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return writeFrame(conn, typ, p)
	}
	b, _ := json.Marshal(req)
	if err := send(frameRequest, b); err != nil {
		return Exit{Code: -1}, err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return Exit{Code: -1}, fmt.Errorf("hostd: reading its answer: %w", err)
	}
	switch typ {
	case frameRefused:
		return Exit{Code: -1}, refusal(payload)
	case frameExit:
		var e Exit
		_ = json.Unmarshal(payload, &e)
		return e, fmt.Errorf("hostd could not start %s %q: %s", req.Kind, req.Name, e.Error)
	case frameStarted:
	default:
		return Exit{Code: -1}, fmt.Errorf("hostd: unexpected frame %q", typ)
	}
	var st Started
	_ = json.Unmarshal(payload, &st)
	if c.OnStarted != nil {
		c.OnStarted(st)
	}

	go func() {
		if stdin != nil {
			_, _ = io.Copy(chunkWriter{typ: frameStdin, send: send}, stdin)
		}
		_ = send(frameStdinClose, nil)
	}()
	exit, _, err := c.stream(ctx, conn, send, req.Detach, Cursor{}, stdout, stderr)
	return exit, err
}

// Attach re-attaches to the detached process id from cur: what its spool
// holds past cur is written to stdout and stderr, and then how it ended
// is returned, with the cursor reached -- which, persisted, is where the
// next Attach resumes when this one is cut off. Cancelling ctx kills the
// process, unless Client.Detach says the daemon is stopping.
func (c Client) Attach(ctx context.Context, id string, cur Cursor, stdout, stderr io.Writer) (Exit, Cursor, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return Exit{Code: -1}, cur, err
	}
	defer conn.Close()
	var sendMu sync.Mutex
	send := func(typ byte, p []byte) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return writeFrame(conn, typ, p)
	}
	b, _ := json.Marshal(Request{Op: OpAttach, ID: id, Cursor: cur})
	if err := send(frameRequest, b); err != nil {
		return Exit{Code: -1}, cur, err
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return Exit{Code: -1}, cur, fmt.Errorf("hostd: reading its answer: %w", err)
	}
	switch typ {
	case frameRefused:
		return Exit{Code: -1}, cur, refusal(payload)
	case frameStarted:
	default:
		return Exit{Code: -1}, cur, fmt.Errorf("hostd: unexpected frame %q", typ)
	}
	return c.stream(ctx, conn, send, true, cur, stdout, stderr)
}

// stream reads a started or attached process's frames until its exit,
// advancing cur by what it writes to stdout and stderr.
func (c Client) stream(ctx context.Context, conn net.Conn, send func(byte, []byte) error, detachable bool,
	cur Cursor, stdout, stderr io.Writer) (Exit, Cursor, error) {
	finished := make(chan struct{})
	defer close(finished)
	detached := make(chan struct{})
	go func() {
		select {
		case <-finished:
		case <-ctx.Done():
			if detachable && c.Detach != nil && c.Detach(ctx) {
				close(detached)
				conn.Close()
				return
			}
			b, _ := json.Marshal(SignalMsg{Signal: "KILL"})
			_ = send(frameSignal, b)
			select {
			case <-finished:
			case <-time.After(WaitDelay + time.Second):
				conn.Close()
			}
		}
	}()

	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			select {
			case <-detached:
				return Exit{Code: -1}, cur, ErrDetached
			default:
			}
			if ctx.Err() != nil {
				return Exit{Code: -1}, cur, ctx.Err()
			}
			return Exit{Code: -1}, cur, ErrNoExit
		}
		switch typ {
		case frameStdout:
			if stdout != nil {
				_, _ = stdout.Write(payload)
			}
			cur.Stdout += int64(len(payload))
		case frameStderr:
			if stderr != nil {
				_, _ = stderr.Write(payload)
			}
			cur.Stderr += int64(len(payload))
		case frameExit:
			var e Exit
			if err := json.Unmarshal(payload, &e); err != nil {
				return Exit{Code: -1}, cur, err
			}
			return e, cur, nil
		}
	}
}

// String says how the process ended, for an error message.
func (e Exit) String() string {
	switch {
	case e.Error != "":
		return e.Error
	case e.Deadline:
		return "killed at its deadline"
	case e.Signal != "":
		return "killed by SIG" + e.Signal
	default:
		return fmt.Sprintf("exit status %d", e.Code)
	}
}
