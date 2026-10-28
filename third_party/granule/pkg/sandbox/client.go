package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/guestforward"
)

// DialOptions configures Dial when connecting to a listen-mode wrapper, and
// RelayClient.Client when reaching one through granule relay.
type DialOptions struct {
	// SandboxID is optional; when set, the wrapper verifies that it
	// matches its own sandbox ID.
	SandboxID string

	// Token is the per-sandbox bearer token. A tcp:// wrapper always
	// requires it; a unix-socket wrapper requires it when configured
	// with one.
	Token string

	// TLSConfig verifies the wrapper's certificate. It is used for
	// tls:// and https:// addresses, for tcp:// addresses when non-nil,
	// and always through a relay (where an empty ServerName defaults to
	// the target name). TLS 1.3 is enforced.
	TLSConfig *tls.Config

	// MuxConfig tunes the underlying guestforward.Session.
	MuxConfig guestforward.Config
}

// Client is a multiplexed client connection to a sandbox's pod-side wrapper,
// created by dialing a listen-mode endpoint (Dial), by opening a stream to
// a named sandbox through granule relay (RelayClient.Client), or by
// accepting an incoming dial-out connection on an Acceptor. Callers use the
// same methods whichever transport made it.
//
// A Client made by Dial or RelayClient.Client redials its transport when
// the session drops, on the next operation that needs one, so an exec cut
// by a dropped connection can be resumed with Reattach on the same Client.
type Client struct {
	sandboxID     string
	reconnectable bool

	// redial, when set, makes a fresh session after the current one dies.
	redial func(ctx context.Context) (*guestforward.Session, error)

	mu      sync.Mutex
	cond    *sync.Cond
	sess    *guestforward.Session
	closed  bool
	dialing bool
}

func newClient(sandboxID string, sess *guestforward.Session, reconnectable bool) *Client {
	c := &Client{
		sandboxID:     sandboxID,
		sess:          sess,
		reconnectable: reconnectable,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// newRedialClient dials once through dial and returns a Client that calls
// dial again whenever its session has died.
func newRedialClient(ctx context.Context, dial func(ctx context.Context) (*guestforward.Session, string, error)) (*Client, error) {
	sess, id, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	c := newClient(id, sess, false)
	c.redial = func(ctx context.Context) (*guestforward.Session, error) {
		sess, _, err := dial(ctx)
		return sess, err
	}
	return c, nil
}

// Dial connects to a listen-mode sandbox server at addr ("unix:///path",
// "unix:/path", "/path", "tcp://host:port", or "tls://host:port") and
// returns a ready *Client. This is the in-cluster path: a controller
// running in the cluster dials a sandbox pod's TCP listener directly
// ("tls://<podIP>:<port>" with opts.Token and opts.TLSConfig).
func Dial(ctx context.Context, addr string, opts DialOptions) (*Client, error) {
	return newRedialClient(ctx, func(ctx context.Context) (*guestforward.Session, string, error) {
		conn, err := dialRaw(ctx, addr, opts.TLSConfig)
		if err != nil {
			return nil, "", err
		}
		return clientHandshake(ctx, conn, opts)
	})
}

// clientHandshake runs the listen-mode handshake over conn (already TLS
// where TLS is wanted) and starts the client side of the session.
func clientHandshake(ctx context.Context, conn net.Conn, opts DialOptions) (*guestforward.Session, string, error) {
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, "", err
		}
	}
	if err := writeJSONLine(conn, HandshakeRequest{
		Version:   ProtocolVersion,
		SandboxID: opts.SandboxID,
		Token:     opts.Token,
	}); err != nil {
		conn.Close()
		return nil, "", err
	}
	br := bufio.NewReader(conn)
	var resp HandshakeResponse
	if err := readJSONLine(br, &resp); err != nil {
		conn.Close()
		return nil, "", err
	}
	if !resp.OK {
		conn.Close()
		return nil, "", fmt.Errorf("%w: %s", ErrAuthFailed, resp.Error)
	}
	_ = conn.SetDeadline(time.Time{})

	sess := guestforward.NewSession(&bufferedConn{Reader: br, Conn: conn}, true, opts.MuxConfig)
	return sess, resp.SandboxID, nil
}

func dialRaw(ctx context.Context, spec string, tlsCfg *tls.Config) (net.Conn, error) {
	spec = strings.TrimSpace(spec)
	var d net.Dialer
	switch {
	case strings.HasPrefix(spec, "tls://") || strings.HasPrefix(spec, "https://"):
		cfg, hostPort, err := buildDialOutTLS(spec, DialOutOptions{TLSConfig: tlsCfg})
		if err != nil {
			return nil, err
		}
		td := &tls.Dialer{NetDialer: &d, Config: cfg}
		return td.DialContext(ctx, "tcp", hostPort)
	case strings.HasPrefix(spec, "tcp://"):
		if tlsCfg != nil {
			return dialRaw(ctx, "tls://"+strings.TrimPrefix(spec, "tcp://"), tlsCfg)
		}
		return d.DialContext(ctx, "tcp", strings.TrimPrefix(spec, "tcp://"))
	case strings.HasPrefix(spec, "unix://"):
		return d.DialContext(ctx, "unix", strings.TrimPrefix(spec, "unix://"))
	case strings.HasPrefix(spec, "unix:"):
		return d.DialContext(ctx, "unix", strings.TrimPrefix(spec, "unix:"))
	case strings.HasPrefix(spec, "/"):
		return d.DialContext(ctx, "unix", spec)
	default:
		return nil, fmt.Errorf("sandbox: unsupported dial address %q", spec)
	}
}

// SandboxID returns the sandbox ID reported during the connection handshake.
func (c *Client) SandboxID() string {
	return c.sandboxID
}

// Close closes the client and its underlying multiplexed session.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	sess := c.sess
	c.cond.Broadcast()
	c.mu.Unlock()
	if sess != nil {
		return sess.Close()
	}
	return nil
}

func (c *Client) setSession(sess *guestforward.Session) {
	c.mu.Lock()
	old := c.sess
	c.sess = sess
	c.cond.Broadcast()
	c.mu.Unlock()
	if old != nil && old != sess {
		_ = old.Close()
	}
}

func (c *Client) activeSession(ctx context.Context) (*guestforward.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.reconnectable && c.redial == nil {
		if c.closed || c.sess == nil {
			return nil, guestforward.ErrSessionClosed
		}
		return c.sess, nil
	}

	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer stop()

	for {
		if c.closed {
			return nil, guestforward.ErrSessionClosed
		}
		if c.sess != nil && c.sess.Err() == nil {
			return c.sess, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if c.redial != nil && !c.dialing {
			c.dialing = true
			old := c.sess
			c.mu.Unlock()
			if old != nil {
				_ = old.Close()
			}
			sess, err := c.redial(ctx)
			c.mu.Lock()
			c.dialing = false
			c.cond.Broadcast()
			if err != nil {
				return nil, err
			}
			if c.closed {
				_ = sess.Close()
				return nil, guestforward.ErrSessionClosed
			}
			c.sess = sess
			return sess, nil
		}
		c.cond.Wait()
	}
}

func (c *Client) openStream(ctx context.Context, req StreamRequest) (*guestforward.Stream, error) {
	meta, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		sess, err := c.activeSession(ctx)
		if err != nil {
			return nil, err
		}
		st, err := sess.Open(ctx, meta)
		if err != nil {
			var rej *guestforward.RejectedError
			if errors.As(err, &rej) {
				if strings.Contains(rej.Reason, ErrExecNotFound.Error()) {
					return nil, fmt.Errorf("%w: %s", ErrExecNotFound, rej.Reason)
				}
				return nil, err
			}
			// If this is a dial-out client and the session died while
			// opening the stream, wait for the wrapper to reconnect.
			if (c.reconnectable || (c.redial != nil && attempt < 3)) && sess.Err() != nil && ctx.Err() == nil {
				c.mu.Lock()
				if c.sess == sess {
					c.sess = nil
				}
				c.mu.Unlock()
				continue
			}
			return nil, err
		}
		return st, nil
	}
}

// Health probes the sandbox wrapper and guest agent readiness.
func (c *Client) Health(ctx context.Context) (*HealthInfo, error) {
	st, err := c.openStream(ctx, StreamRequest{Op: OpHealth})
	if err != nil {
		return nil, err
	}
	defer st.Close()

	var info HealthInfo
	if err := json.Unmarshal(st.Reply(), &info); err != nil {
		return nil, fmt.Errorf("decoding health response: %w", err)
	}
	return &info, nil
}

// ExecProcess represents a running or reattached command inside the sandbox.
type ExecProcess struct {
	id     string
	client *Client
	st     *guestforward.Stream
	stdin  *execStdinWriter

	mu        sync.Mutex
	bytesRead int64
}

// ID returns the unique identifier for this exec session, used with Reattach,
// Signal, and Kill.
func (p *ExecProcess) ID() string {
	return p.id
}

// Stdin returns an io.WriteCloser that streams standard input to the guest
// command. Closing it sends TypeStdinClose (EOF) to the guest command without
// closing the output stream.
func (p *ExecProcess) Stdin() io.WriteCloser {
	return p.stdin
}

// Resize updates the terminal window size for a TTY exec session.
func (p *ExecProcess) Resize(cols, rows uint16) error {
	return p.stdin.writeFrame(execwire.TypeWinsize, execwire.EncodeWinsize(cols, rows))
}

// Signal sends sig to the command's process group inside the guest.
func (p *ExecProcess) Signal(ctx context.Context, sig syscall.Signal) error {
	return p.client.Signal(ctx, p.id, sig)
}

// Kill sends SIGKILL to the command's process group inside the guest.
func (p *ExecProcess) Kill(ctx context.Context) error {
	return p.client.Kill(ctx, p.id)
}

// Detach closes the local stream attachment without signalling or closing the
// guest command, leaving it running in the wrapper for a subsequent Reattach.
func (p *ExecProcess) Detach() error {
	p.st.Reset("client detached")
	return nil
}

// ReceivedBytes returns the total number of stdout and stderr bytes received
// by this ExecProcess handle so far.
func (p *ExecProcess) ReceivedBytes() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bytesRead
}

// Wait streams the command's stdout and stderr into the provided writers and
// waits for the terminal exit frame.
//
// If the underlying transport drops before the command exits, Wait returns
// an error wrapping ErrTransportCut along with the total number of stdout and
// stderr bytes delivered on this stream. Pass that offset (added to any prior
// offset) to Client.Reattach to resume without losing or duplicating output.
func (p *ExecProcess) Wait(stdout, stderr io.Writer) (exitCode int, bytesRead int64, err error) {
	defer p.st.Close()
	for {
		typ, payload, rerr := execwire.ReadFrame(p.st)
		if rerr != nil {
			return 0, p.ReceivedBytes(), fmt.Errorf("%w: %v", ErrTransportCut, rerr)
		}
		switch typ {
		case execwire.TypeStdout:
			p.mu.Lock()
			p.bytesRead += int64(len(payload))
			p.mu.Unlock()
			if stdout != nil && len(payload) > 0 {
				if _, werr := stdout.Write(payload); werr != nil {
					return 0, p.ReceivedBytes(), werr
				}
			}
		case execwire.TypeStderr:
			p.mu.Lock()
			p.bytesRead += int64(len(payload))
			p.mu.Unlock()
			if stderr != nil && len(payload) > 0 {
				if _, werr := stderr.Write(payload); werr != nil {
					return 0, p.ReceivedBytes(), werr
				}
			}
		case execwire.TypeNote:
			if stderr != nil && len(payload) > 0 {
				_, _ = fmt.Fprintf(stderr, "%s\n", strings.TrimRight(string(payload), "\n"))
			}
		case execwire.TypeExit:
			code, decErr := execwire.DecodeExit(payload)
			if decErr != nil {
				return 0, p.ReceivedBytes(), fmt.Errorf("%w: %v", ErrTransportCut, decErr)
			}
			return code, p.ReceivedBytes(), nil
		}
	}
}

type execStdinWriter struct {
	mu     sync.Mutex
	st     *guestforward.Stream
	closed bool
}

func (w *execStdinWriter) writeFrame(typ byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return execwire.WriteFrame(w.st, typ, payload)
}

func (w *execStdinWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > 32<<10 {
			n = 32 << 10
		}
		if err := w.writeFrame(execwire.TypeStdin, p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (w *execStdinWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return execwire.WriteFrame(w.st, execwire.TypeStdinClose, nil)
}

// StartExec starts a command in the sandbox and returns an *ExecProcess
// handle for streaming I/O, signalling, detaching, or waiting.
func (c *Client) StartExec(ctx context.Context, req ExecRequest) (*ExecProcess, error) {
	st, err := c.openStream(ctx, StreamRequest{
		Op:   OpExec,
		Exec: &req,
	})
	if err != nil {
		return nil, err
	}
	var reply ExecReply
	if err := json.Unmarshal(st.Reply(), &reply); err != nil {
		st.Close()
		return nil, fmt.Errorf("decoding exec reply: %w", err)
	}
	return &ExecProcess{
		id:     reply.ID,
		client: c,
		st:     st,
		stdin:  &execStdinWriter{st: st},
	}, nil
}

// Exec starts a command, streams stdin/stdout/stderr, and waits for it to
// finish. A non-zero command exit status is returned as exitCode with err ==
// nil; a broken connection before the command exits returns an error wrapping
// ErrTransportCut.
func (c *Client) Exec(ctx context.Context, req ExecRequest, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	proc, err := c.StartExec(ctx, req)
	if err != nil {
		return 0, err
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = proc.Detach()
		case <-done:
		}
	}()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(proc.Stdin(), stdin)
			_ = proc.Stdin().Close()
		}()
	} else {
		_ = proc.Stdin().Close()
	}

	code, _, err := proc.Wait(stdout, stderr)
	if ctx.Err() != nil && errors.Is(err, ErrTransportCut) {
		return 0, fmt.Errorf("%w: %v", ErrTransportCut, ctx.Err())
	}
	return code, err
}

// StartReattach attaches to a running or completed exec session by ID,
// replaying buffered stdout/stderr starting at byte offset.
func (c *Client) StartReattach(ctx context.Context, id string, offset int64) (*ExecProcess, error) {
	st, err := c.openStream(ctx, StreamRequest{
		Op: OpReattach,
		Reattach: &ReattachRequest{
			ID:     id,
			Offset: offset,
		},
	})
	if err != nil {
		return nil, err
	}
	var reply ReattachReply
	if err := json.Unmarshal(st.Reply(), &reply); err != nil {
		st.Close()
		return nil, fmt.Errorf("decoding reattach reply: %w", err)
	}
	return &ExecProcess{
		id:     reply.ID,
		client: c,
		st:     st,
		stdin:  &execStdinWriter{st: st},
	}, nil
}

// Reattach attaches to exec session id at byte offset, streams remaining
// output to stdout/stderr, and waits for its terminal exit code. If a
// reconnect happens again while reattached, Reattach automatically resumes
// from the updated offset if c is a dial-out client.
func (c *Client) Reattach(ctx context.Context, id string, offset int64, stdout, stderr io.Writer) (int, error) {
	curOffset := offset
	for {
		proc, err := c.StartReattach(ctx, id, curOffset)
		if err != nil {
			return 0, err
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = proc.Detach()
			case <-done:
			}
		}()
		code, n, err := proc.Wait(stdout, stderr)
		close(done)
		curOffset += n
		if err == nil {
			return code, nil
		}
		if c.reconnectable && errors.Is(err, ErrTransportCut) && ctx.Err() == nil {
			continue
		}
		return 0, err
	}
}

// Signal delivers sig to the process group of exec session id.
func (c *Client) Signal(ctx context.Context, id string, sig syscall.Signal) error {
	st, err := c.openStream(ctx, StreamRequest{
		Op: OpSignal,
		Signal: &SignalRequest{
			ID:     id,
			Signal: int(sig),
		},
	})
	if err != nil {
		return err
	}
	defer st.Close()
	return nil
}

// Kill delivers SIGKILL to the process group of exec session id.
func (c *Client) Kill(ctx context.Context, id string) error {
	return c.Signal(ctx, id, syscall.SIGKILL)
}

// PutFiles uploads a batch of files to the guest in a single round trip. Each
// file is written to a sibling temporary file, given its mode and optional
// owner, and atomically renamed into place.
func (c *Client) PutFiles(ctx context.Context, files []FileUpload) error {
	if len(files) == 0 {
		return nil
	}
	metas := make([]FileMeta, len(files))
	for i, f := range files {
		if f.Path == "" || !strings.HasPrefix(f.Path, "/") {
			return fmt.Errorf("sandbox: file path %q must be absolute", f.Path)
		}
		size := f.Size
		if f.Reader == nil {
			size = int64(len(f.Content))
		}
		mode := uint32(f.Mode.Perm())
		if mode == 0 {
			mode = 0o644
		}
		metas[i] = FileMeta{
			Path:  f.Path,
			Size:  size,
			Mode:  mode,
			Owner: f.Owner,
		}
	}

	st, err := c.openStream(ctx, StreamRequest{Op: OpPutFiles})
	if err != nil {
		return err
	}
	defer st.Close()

	if err := writeJSONLine(st, PutFilesRequest{Files: metas}); err != nil {
		return fmt.Errorf("sending put-files header: %w", err)
	}
	for i, f := range files {
		var r io.Reader
		if f.Reader != nil {
			r = io.LimitReader(f.Reader, metas[i].Size)
		} else {
			r = bytes.NewReader(f.Content)
		}
		n, err := io.Copy(st, r)
		if err != nil {
			return fmt.Errorf("sending file %s: %w", f.Path, err)
		}
		if n != metas[i].Size {
			return fmt.Errorf("file %s: wrote %d bytes, expected %d", f.Path, n, metas[i].Size)
		}
	}
	if err := st.CloseWrite(); err != nil {
		return err
	}

	br := bufio.NewReader(st)
	var reply PutFilesReply
	if err := readJSONLine(br, &reply); err != nil {
		return fmt.Errorf("reading put-files reply: %w", err)
	}
	if !reply.OK {
		return fmt.Errorf("sandbox put-files: %s", reply.Error)
	}
	return nil
}

// PutFile uploads a single file to the guest with atomic replacement.
func (c *Client) PutFile(ctx context.Context, dstPath string, content []byte, mode fs.FileMode, owner string) error {
	return c.PutFiles(ctx, []FileUpload{{
		Path:    dstPath,
		Content: content,
		Mode:    mode,
		Owner:   owner,
	}})
}

// GetFile reads a single file and its permission bits from the guest.
func (c *Client) GetFile(ctx context.Context, srcPath string) ([]byte, fs.FileMode, error) {
	st, err := c.openStream(ctx, StreamRequest{
		Op:      OpGetFile,
		GetFile: &GetFileRequest{Path: srcPath},
	})
	if err != nil {
		return nil, 0, err
	}
	defer st.Close()
	_ = st.CloseWrite()

	br := bufio.NewReader(st)
	var reply GetFileReply
	if err := readJSONLine(br, &reply); err != nil {
		return nil, 0, fmt.Errorf("reading get-file reply: %w", err)
	}
	if !reply.OK {
		return nil, 0, fmt.Errorf("sandbox get-file %s: %s", srcPath, reply.Error)
	}
	data, err := io.ReadAll(br)
	if err != nil {
		return nil, 0, fmt.Errorf("reading file content for %s: %w", srcPath, err)
	}
	return data, fs.FileMode(reply.Mode), nil
}

// OpenForwardStream opens a raw stream to the guest's port-forwarding agent
// (pkg/guestforward).
func (c *Client) OpenForwardStream(ctx context.Context) (*guestforward.Stream, error) {
	return c.openStream(ctx, StreamRequest{
		Op:      OpForward,
		Forward: &ForwardRequest{},
	})
}
