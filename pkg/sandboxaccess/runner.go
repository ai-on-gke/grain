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

package sandboxaccess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/granule/pkg/guestforward"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// DefaultReattachTimeout is how long an in-flight step waits for a dropped
// sandbox access channel to reconnect before concluding the channel is lost.
const DefaultReattachTimeout = 2 * time.Minute

// DefaultUser is the guest account Runner executes commands as when User is
// not set explicitly, matching granule's "-u debian" exec prefix.
const DefaultUser = "debian"

// DefaultHomeDir is the default guest home directory for DefaultUser.
const DefaultHomeDir = "/home/debian"

// ChannelProvider is the subset of Acceptor that Runner needs to look up or
// wait for a sandbox's live dial-out channel.
type ChannelProvider interface {
	Client(sandboxName string) (*sandbox.Client, bool)
	WaitClient(ctx context.Context, sandboxName string) (*sandbox.Client, error)
}

// ErrChannelLost is returned when a sandbox's dial-out access channel drops
// and does not reconnect within ReattachTimeout.
var ErrChannelLost = errors.New("sandbox access channel lost")

// ChannelLostFault starts the error a CI job concludes errored with when one
// of its steps ended in ErrChannelLost: the engine writes it
// (pkg/grit/engine), and the orchestrator reads it to re-run the job
// uncached rather than charge the tree (design/design/grain-on-gke.design.md#GKE-3).
const ChannelLostFault = "infra: sandbox channel lost"

// ChannelLostError is the typed error returned for ErrChannelLost, carrying
// the sandbox name and how long its channel was unreachable.
type ChannelLostError struct {
	Sandbox string
	Down    time.Duration
	Cause   error
}

func (e *ChannelLostError) Error() string {
	if e == nil {
		return ErrChannelLost.Error()
	}
	down := e.Down.Round(time.Millisecond)
	if e.Cause != nil {
		return fmt.Sprintf("sandbox %s: %s after %s (%v)", e.Sandbox, ErrChannelLost.Error(), down, e.Cause)
	}
	return fmt.Sprintf("sandbox %s: %s after %s", e.Sandbox, ErrChannelLost.Error(), down)
}

// Is matches ErrChannelLost so callers can check errors.Is(err, ErrChannelLost).
func (e *ChannelLostError) Is(target error) bool {
	return target == ErrChannelLost
}

func (e *ChannelLostError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Runner implements the Run and RunStream command runner shape over a
// sandbox's dial-out channel (sandbox.Client from Acceptor), with automatic
// reattach from the byte cursor when the channel drops and reconnects within
// ReattachTimeout.
//
// It also exposes batched file uploads (PutFiles, PlaceFile) and channel-backed
// port forwarding (StartForward, DialPort) over the same multiplexed session.
type Runner struct {
	Acceptor        ChannelProvider
	Sandbox         string
	User            string
	HomeDir         string
	ReattachTimeout time.Duration
	Now             func() time.Time
	// OnChannelLost, when non-nil, is invoked once if the channel stays down
	// past ReattachTimeout and an operation ends with ErrChannelLost.
	OnChannelLost func(err error)

	reattaches atomic.Uint64
	lossOnce   sync.Once
}

// EffectiveHomeDir returns HomeDir when set, or DefaultHomeDir ("/home/debian").
func (r *Runner) EffectiveHomeDir() string {
	if r != nil && r.HomeDir != "" {
		return r.HomeDir
	}
	return DefaultHomeDir
}

// Reattaches returns the number of times an in-flight exec on this Runner
// reattached after a transport cut.
func (r *Runner) Reattaches() uint64 {
	return r.reattaches.Load()
}

func (r *Runner) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) reattachTimeout() time.Duration {
	if r != nil && r.ReattachTimeout > 0 {
		return r.ReattachTimeout
	}
	return DefaultReattachTimeout
}

func (r *Runner) user() string {
	if r != nil && r.User != "" {
		return r.User
	}
	return DefaultUser
}

func (r *Runner) notifyChannelLost(err error) {
	if r == nil || r.OnChannelLost == nil {
		return
	}
	r.lossOnce.Do(func() {
		r.OnChannelLost(err)
	})
}

// Run executes argv in the guest over the sandbox access channel.
func (r *Runner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	return r.RunStream(ctx, argv, stdin, nil)
}

// RunStream executes argv in the guest over the sandbox access channel,
// streaming stdout chunks to out as they arrive when out is non-nil.
func (r *Runner) RunStream(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int) {
	stdout, stderr, exitCode, _ = r.Exec(ctx, argv, stdin, out)
	return stdout, stderr, exitCode
}

// Attachment ties one exec to a record that outlives this process, so a
// grain that restarts mid-step can find the command again
// (design/design/grain-on-gke.design.md, "Restart: reattach rather than interrupt").
//
// The wrapper keeps an exec, its spool and, once it ends, its exit status
// across the acceptor going away; what it cannot keep is which of grain's
// steps the exec was. That is what the caller persists, from Started and
// Progress, and hands back as ExecID and Cursor after a restart.
type Attachment struct {
	// ExecID, when set, names an exec already started in the sandbox --
	// by a grain process that has since gone -- to reattach to rather than
	// starting argv again. A wrapper that no longer knows it, or a
	// sandbox whose channel never comes back, ends the call with
	// ErrChannelLost.
	ExecID string
	// Cursor is how many bytes of the exec's output the caller already
	// has, the offset a reattach resumes from.
	Cursor int64
	// Started, when non-nil, is told the exec id once the wrapper has
	// started the command (or, for a reattach, the id reattached to).
	Started func(execID string)
	// Progress, when non-nil, is told the cursor -- the bytes of output
	// delivered so far, from the start of the exec -- each time more
	// arrives. It is called from the goroutine reading the exec and must
	// not block for long.
	Progress func(cursor int64)
}

// Exec executes argv in the guest over the sandbox access channel and returns
// the terminal exit code along with any transport error (such as ErrChannelLost
// when a dropped channel does not reconnect within ReattachTimeout).
func (r *Runner) Exec(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int, err error) {
	return r.ExecAttached(ctx, argv, stdin, out, nil)
}

// ExecAttached is Exec with an Attachment: it reports the exec's id and
// cursor as it goes, and when att.ExecID is set it reattaches to that exec
// from att.Cursor instead of starting argv, waiting up to ReattachTimeout
// for the sandbox's wrapper to dial in first. A reattached exec that has
// already finished answers with its real exit status, which the wrapper
// keeps for a while after the command ends.
func (r *Runner) ExecAttached(ctx context.Context, argv []string, stdin string, out io.Writer, att *Attachment) (stdout, stderr string, exitCode int, err error) {
	if r == nil || r.Acceptor == nil {
		err := errors.New("sandboxaccess: Runner has no Acceptor")
		return "", err.Error(), -1, err
	}
	if err := ctx.Err(); err != nil {
		return "", err.Error(), -1, err
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutWriter := io.Writer(&stdoutBuf)
	stderrWriter := io.Writer(&stderrBuf)
	if out != nil {
		stdoutWriter = io.MultiWriter(&stdoutBuf, out)
		stderrWriter = io.MultiWriter(&stderrBuf, out)
	}

	var (
		client *sandbox.Client
		proc   *sandbox.ExecProcess
		offset int64
	)
	if att != nil && att.ExecID != "" {
		offset = att.Cursor
		proc, err = r.reattachWithinWindow(ctx, nil, att.ExecID, offset, nil)
		if err != nil {
			return "", err.Error(), -1, err
		}
		if offset > 0 {
			fmt.Fprintf(stderrWriter, "[grain reattached to this step after a restart; the first %d bytes of its output were streamed before it and are not repeated here]\n", offset)
		}
	} else {
		client, proc, err = r.startExecWithReattachWindow(ctx, sandbox.ExecRequest{
			Argv: argv,
			User: r.user(),
		})
		if err != nil {
			return "", err.Error(), -1, err
		}
		if stdin != "" {
			go func(p *sandbox.ExecProcess) {
				_, _ = io.Copy(p.Stdin(), strings.NewReader(stdin))
				_ = p.Stdin().Close()
			}(proc)
		} else {
			_ = proc.Stdin().Close()
		}
	}

	procID := proc.ID()
	curProc := proc
	if att != nil && att.Started != nil {
		att.Started(procID)
	}
	if att != nil && att.Progress != nil {
		// The wrapper's own count rather than the bytes written here: a
		// note frame reaches stderr without moving the offset a reattach
		// resumes from, and ReceivedBytes is already up to date by the
		// time a chunk is handed on. Wait writes on this goroutine, so
		// offset and curProc are read here as they stand.
		report := func() { att.Progress(offset + curProc.ReceivedBytes()) }
		stdoutWriter = progressWriter{stdoutWriter, report}
		stderrWriter = progressWriter{stderrWriter, report}
	}

	for {
		done := make(chan struct{})
		go func(p *sandbox.ExecProcess) {
			select {
			case <-ctx.Done():
				_ = p.Detach()
			case <-done:
			}
		}(curProc)

		code, n, waitErr := curProc.Wait(stdoutWriter, stderrWriter)
		close(done)
		offset += n

		if waitErr == nil {
			return stdoutBuf.String(), stderrBuf.String(), code, nil
		}
		if ctx.Err() != nil {
			cErr := ctx.Err()
			return stdoutBuf.String(), appendStderr(stderrBuf.String(), cErr), -1, cErr
		}
		if !errors.Is(waitErr, sandbox.ErrTransportCut) {
			return stdoutBuf.String(), appendStderr(stderrBuf.String(), waitErr), -1, waitErr
		}

		// The transport dropped mid-step. Wait up to ReattachTimeout for the
		// wrapper to reconnect and reattach from offset.
		reattached, rErr := r.reattachWithinWindow(ctx, client, procID, offset, waitErr)
		if rErr != nil {
			return stdoutBuf.String(), appendStderr(stderrBuf.String(), rErr), -1, rErr
		}
		curProc = reattached
	}
}

func (r *Runner) startExecWithReattachWindow(ctx context.Context, req sandbox.ExecRequest) (*sandbox.Client, *sandbox.ExecProcess, error) {
	if client, ok := r.Acceptor.Client(r.Sandbox); ok {
		proc, err := client.StartExec(ctx, req)
		if err == nil {
			return client, proc, nil
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}

	// Channel is not currently up (or dropped just as StartExec began). Wait up
	// to ReattachTimeout for it to reconnect.
	droppedAt := r.now()
	timeout := r.reattachTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		client, err := r.Acceptor.WaitClient(waitCtx, r.Sandbox)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   lastErrOr(lastErr, err),
			}
			r.notifyChannelLost(lossErr)
			return nil, nil, lossErr
		}
		proc, err := client.StartExec(waitCtx, req)
		if err == nil {
			return client, proc, nil
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		lastErr = err
		if waitCtx.Err() != nil {
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   lastErr,
			}
			r.notifyChannelLost(lossErr)
			return nil, nil, lossErr
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (r *Runner) reattachWithinWindow(ctx context.Context, client *sandbox.Client, procID string, offset int64, cutErr error) (*sandbox.ExecProcess, error) {
	droppedAt := r.now()
	timeout := r.reattachTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lastErr := cutErr
	for {
		c, err := r.Acceptor.WaitClient(waitCtx, r.Sandbox)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   lastErr,
			}
			r.notifyChannelLost(lossErr)
			return nil, lossErr
		}
		if c != nil {
			client = c
		}

		reattached, err := client.StartReattach(waitCtx, procID, offset)
		if err == nil {
			count := r.reattaches.Add(1)
			slog.Info("sandboxaccess: reattached exec after channel drop",
				"sandbox", r.Sandbox,
				"exec_id", procID,
				"offset", offset,
				"down", r.now().Sub(droppedAt).Round(time.Millisecond),
				"reattaches", count,
			)
			return reattached, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		if errors.Is(err, sandbox.ErrExecNotFound) {
			// The wrapper reconnected without this exec session (e.g. wrapper
			// restarted), so the step cannot resume.
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   err,
			}
			r.notifyChannelLost(lossErr)
			return nil, lossErr
		}
		if waitCtx.Err() != nil {
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   lastErr,
			}
			r.notifyChannelLost(lossErr)
			return nil, lossErr
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// PutFiles uploads a batch of files to the guest in a single round trip over
// the sandbox access channel. If the channel is transiently disconnected, it
// waits up to ReattachTimeout for it to reconnect.
func (r *Runner) PutFiles(ctx context.Context, files []sandbox.FileUpload) error {
	if len(files) == 0 {
		return nil
	}
	if r == nil || r.Acceptor == nil {
		return errors.New("sandboxaccess: Runner has no Acceptor")
	}
	if client, ok := r.Acceptor.Client(r.Sandbox); ok {
		err := client.PutFiles(ctx, files)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Non-transport validation errors (such as a relative path) should be
		// returned directly without waiting on a reconnect.
		if strings.HasPrefix(err.Error(), "sandbox: file path") || strings.HasPrefix(err.Error(), "sandbox put-files:") {
			return err
		}
	}

	droppedAt := r.now()
	waitCtx, cancel := context.WithTimeout(ctx, r.reattachTimeout())
	defer cancel()

	var lastErr error
	for {
		client, err := r.Acceptor.WaitClient(waitCtx, r.Sandbox)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   lastErrOr(lastErr, err),
			}
			r.notifyChannelLost(lossErr)
			return lossErr
		}
		err = client.PutFiles(waitCtx, files)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.HasPrefix(err.Error(), "sandbox: file path") || strings.HasPrefix(err.Error(), "sandbox put-files:") {
			return err
		}
		lastErr = err
		if waitCtx.Err() != nil {
			lossErr := &ChannelLostError{
				Sandbox: r.Sandbox,
				Down:    r.now().Sub(droppedAt),
				Cause:   lastErr,
			}
			r.notifyChannelLost(lossErr)
			return lossErr
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// PlaceFile writes a single file at path with octal mode (default "0600") over
// the sandbox access channel using PutFiles.
func (r *Runner) PlaceFile(ctx context.Context, path, content, mode string) error {
	fm, err := ParseFileMode(mode)
	if err != nil {
		return err
	}
	return r.PutFiles(ctx, []sandbox.FileUpload{{
		Path:    path,
		Content: []byte(content),
		Mode:    fm,
	}})
}

// ParseFileMode parses an octal permission string like "0600" or "0644" into
// an fs.FileMode (defaulting to 0600 when empty).
func ParseFileMode(mode string) (fs.FileMode, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return 0o600, nil
	}
	u, err := strconv.ParseUint(mode, 8, 12)
	if err != nil || u == 0 {
		return 0, fmt.Errorf("sandboxaccess: invalid file mode %q", mode)
	}
	return fs.FileMode(u), nil
}

// StartForward starts guest port forwarding (remote and/or local forwards)
// over the sandbox's dial-out channel, reconnecting automatically when the
// channel reconnects.
func (r *Runner) StartForward(ctx context.Context, remote []guestforward.RemoteForward, local []guestforward.LocalForward) (*guestforward.Forwarder, error) {
	if len(remote) == 0 && len(local) == 0 {
		return nil, nil
	}
	if r == nil || r.Acceptor == nil {
		return nil, errors.New("sandboxaccess: Runner has no Acceptor")
	}
	return guestforward.Start(ctx, guestforward.Options{
		Transport: r.openForwardStream,
		Remote:    remote,
		Local:     local,
		Reconnect: true,
	})
}

func (r *Runner) openForwardStream(ctx context.Context) (io.ReadWriteCloser, error) {
	waitCtx, cancel := context.WithTimeout(ctx, r.reattachTimeout())
	defer cancel()
	client, err := r.Acceptor.WaitClient(waitCtx, r.Sandbox)
	if err != nil {
		return nil, err
	}
	return client.OpenForwardStream(waitCtx)
}

// DialPort connects to 127.0.0.1:port inside the sandbox guest over the
// dial-out channel without starting a kubectl exec subprocess.
func (r *Runner) DialPort(ctx context.Context, port int) (net.Conn, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("sandboxaccess: invalid guest port %d", port)
	}
	// The forwarder binds the loopback port itself (port 0) and reports
	// the one it got, rather than being handed a port probed and freed
	// here first: in between, another process on this host could bind it
	// and be dialled in the guest's place.
	fwdCtx, cancel := context.WithCancel(context.Background())
	fwd, err := r.StartForward(fwdCtx, nil, []guestforward.LocalForward{{
		Listen: "127.0.0.1:0",
		Guest:  fmt.Sprintf("127.0.0.1:%d", port),
	}})
	if err != nil {
		cancel()
		return nil, err
	}
	var localAddr string
	for _, b := range fwd.Bindings() {
		if b.Direction == "local" {
			localAddr = b.Listen
		}
	}
	if localAddr == "" {
		_ = fwd.Close()
		cancel()
		return nil, errors.New("sandboxaccess: the guest port forward reported no local listener")
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", localAddr)
	if err != nil {
		_ = fwd.Close()
		cancel()
		return nil, err
	}
	return &forwardedConn{
		Conn: conn,
		closeFn: func() {
			_ = fwd.Close()
			cancel()
		},
	}, nil
}

type forwardedConn struct {
	net.Conn
	once    sync.Once
	closeFn func()
}

func (c *forwardedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.closeFn)
	return err
}

// progressWriter hands every write on and then reports, for
// Attachment.Progress.
type progressWriter struct {
	w      io.Writer
	report func()
}

func (p progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.report()
	return n, err
}

func appendStderr(stderr string, err error) string {
	if err == nil {
		return stderr
	}
	if stderr == "" {
		return err.Error()
	}
	return strings.TrimRight(stderr, "\n") + "\n" + err.Error()
}

func lastErrOr(lastErr, fallback error) error {
	if lastErr != nil {
		return lastErr
	}
	return fallback
}
