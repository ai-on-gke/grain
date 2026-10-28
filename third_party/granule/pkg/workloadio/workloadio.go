// Package workloadio carries a wrapped container's own output out of its
// VM, separately from the VM's console.
//
// The console is the guest's serial port, which cloud-hypervisor hands to
// its own stdio, so everything that reaches it -- the kernel, init, and
// the workload's stdout and stderr through granule-container's unit --
// arrives on the host as one merged stream. That is the right thing for
// `kubectl logs` on a VM container, which is a machine's log, and it is
// still what happens by default. It is the wrong thing for a caller that
// runs the VM as a container in its own right and has to report that
// container's output the way a container runtime does: CRI's log format
// keeps stdout and stderr apart, and a line the kernel printed is not
// the workload's.
//
// So granule-container also serves the workload's two streams on a vsock
// port of their own (Port), and the host's granule-shim, when asked
// (GRANULE_WORKLOAD_STDOUT and GRANULE_WORKLOAD_STDERR), connects to it
// and writes each stream to a file or FIFO of the caller's naming.
//
// # One channel
//
// The connection speaks execwire's framing rather than a format of its
// own: the guest opens with an execwire.Response line naming what it
// implements, then sends TypeStdout and TypeStderr frames for as long as
// the workload writes. The host's TypeStdin, TypeStdinClose and
// TypeWinsize frames go the other way on the same connection, to a
// workload started with its stdin open or on a terminal (see "Stdin" in
// stdin.go).
// (Its exit status does not: that has to reach a host which never asked
// for the streams, so it travels the other way, on a port of its own --
// see "Exit status" in exit.go.) Such additions arrive as features named in
// the opening line, the same way execwire's own optional frames do (see
// execwire's "Frame types need the same line"): a host checks for the
// feature before relying on the frame, and a guest that predates one is
// a guest whose host falls back rather than one that quietly misbehaves.
// Frames neither end recognizes are stepped over.
//
// # Nothing attached
//
// The workload starts at boot, before anything on the host can have
// connected, and it may write a great deal before anything does -- or
// nothing may ever connect, which is the default. The guest keeps what
// was written while detached in a bounded backlog (BacklogLimit) and
// sends it, in order, to the next connection before anything new. Past
// the bound it drops the oldest output rather than stalling the
// workload, since a workload whose output nobody asked for must run
// exactly as it did before this existed; the next connection is told,
// with a TypeNote frame, how much it missed. While a host is attached
// the workload's writes go straight to it and wait for it, which is the
// backpressure a pipe to a container runtime gives.
//
// There is one attachment at a time. A second connection replaces the
// first, which is what a host that lost its connection and redialled
// looks like from here.
//
// # Trust
//
// The same as execwire's: only the VM's own hypervisor process can open
// a connection, and the guest refuses any that does not come from the
// host's context id.
package workloadio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

// Port is the vsock port granule-container serves the workload's streams
// on, next to granule-agent's execwire.Port. Fixed rather than
// configurable for the same reason that one is: both ends ship together.
const Port = execwire.Port + 1

// FeatureStreams is the feature a guest names when it sends the
// workload's stdout and stderr as TypeStdout and TypeStderr frames.
const FeatureStreams = "streams"

// Features is everything a guest built from this commit implements, and
// so what it names in the connection's opening line.
var Features = []string{FeatureStreams}

// CmdlineParam is the kernel parameter the host sets, as
// CmdlineParam+"=1", when it is going to attach to the workload's
// streams. It is what lets the guest wait for that host before letting a
// short-lived workload's VM power off with its output still in the
// backlog -- and not wait for anyone otherwise, which is every VM that
// never asked. (A guest booted from firmware never sees a kernel command
// line; its output reaches a host that is attached by the time the
// workload exits, and the backlog of one that is not is lost with it.)
const CmdlineParam = "granule.workload_streams"

// ExitWait is how long a guest whose host asked for the streams waits,
// once the workload has finished, for that host to have everything.
const ExitWait = 5 * time.Second

// Requested reports whether cmdline, the guest's /proc/cmdline, carries
// CmdlineParam switched on.
func Requested(cmdline string) bool {
	for _, field := range strings.Fields(cmdline) {
		name, value, _ := strings.Cut(field, "=")
		if name == CmdlineParam {
			return value == "1"
		}
	}
	return false
}

// BacklogLimit bounds how much of the workload's output the guest keeps
// while nothing is attached. Enough for a workload's whole startup
// chatter many times over, and small next to the memory of the smallest
// VM granule boots.
const BacklogLimit = 4 << 20

type frame struct {
	typ     byte
	payload []byte
}

// Broker is the guest's end: the workload's two streams are written to
// it (Stdout and Stderr), and it sends them to whichever host connection
// is attached (Serve), keeping them in its backlog while none is.
type Broker struct {
	limit int

	mu      sync.Mutex
	changed *sync.Cond // signalled whenever conn or backlog changes
	conn    io.WriteCloser
	backlog []frame
	queued  int   // bytes in backlog
	dropped int64 // bytes dropped from the backlog since the last attach
	closed  bool

	// What the workload takes from the host (see stdin.go): the
	// features it is announced as, and the Input it reaches once the
	// workload has one.
	features     []string
	offerStdin   bool
	offerWinsize bool
	input        Input

	inputMu   sync.Mutex // held across each delivery to input
	stdinDone bool       // stdin failed or was closed; later stdin is dropped
}

// NewBroker returns a broker that keeps up to limit bytes of output while
// nothing is attached. A limit of zero or less means BacklogLimit.
func NewBroker(limit int) *Broker {
	if limit <= 0 {
		limit = BacklogLimit
	}
	b := &Broker{limit: limit, features: Features}
	b.changed = sync.NewCond(&b.mu)
	return b
}

// Stdout is the writer the workload's stdout is copied to.
func (b *Broker) Stdout() io.Writer { return streamWriter{b, execwire.TypeStdout} }

// Stderr is the writer the workload's stderr is copied to.
func (b *Broker) Stderr() io.Writer { return streamWriter{b, execwire.TypeStderr} }

type streamWriter struct {
	b   *Broker
	typ byte
}

// Write never fails: output that cannot reach the host goes to the
// backlog instead, and the console copy the caller writes alongside this
// one must not be cut short by a host that went away.
func (w streamWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		chunk := p
		if len(chunk) > execwire.MaxPayload {
			chunk = chunk[:execwire.MaxPayload]
		}
		p = p[len(chunk):]
		w.b.send(w.typ, chunk)
	}
	return n, nil
}

func (b *Broker) send(typ byte, payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	// Held across the write on purpose: it is what keeps the two
	// streams' frames in the order the workload wrote them, and what
	// makes a slow host slow the workload down rather than reorder it.
	if b.conn != nil && len(b.backlog) == 0 {
		if err := execwire.WriteFrame(b.conn, typ, payload); err == nil {
			return
		}
		b.detachLocked(b.conn)
	}
	b.queueLocked(frame{typ, append([]byte(nil), payload...)})
}

// queueLocked appends f to the backlog, dropping the oldest output to
// stay within the limit.
func (b *Broker) queueLocked(f frame) {
	b.backlog = append(b.backlog, f)
	b.queued += len(f.payload)
	for b.queued > b.limit && len(b.backlog) > 0 {
		old := b.backlog[0]
		b.backlog[0] = frame{}
		b.backlog = b.backlog[1:]
		b.queued -= len(old.payload)
		b.dropped += int64(len(old.payload))
	}
	b.changed.Broadcast()
}

func (b *Broker) detachLocked(conn io.WriteCloser) {
	if b.conn != conn || conn == nil {
		return
	}
	_ = conn.Close()
	b.conn = nil
	b.changed.Broadcast()
}

// Serve attaches conn, a connection from the host, and returns once it
// has gone: when the host hangs up, when a later connection replaces it,
// or when the broker is closed. Frames the host sends are read and
// handed to the workload's Input, if it was offered one (see stdin.go),
// and stepped over otherwise -- reading them is also how a hang-up is
// noticed while the workload is quiet.
func (b *Broker) Serve(conn io.ReadWriteCloser) error {
	if err := b.attach(conn); err != nil {
		_ = conn.Close()
		return err
	}
	defer func() {
		b.mu.Lock()
		b.detachLocked(conn)
		b.mu.Unlock()
	}()
	for {
		typ, payload, err := execwire.ReadFrame(conn)
		if err != nil {
			b.mu.Lock()
			replaced := b.conn != conn
			b.mu.Unlock()
			// A connection this broker hung up on itself -- replaced,
			// closed, or found broken by a write -- fails its read for
			// that reason, which is not news.
			if replaced || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		b.deliver(typ, payload)
	}
}

func (b *Broker) attach(conn io.WriteCloser) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("the workload has finished")
	}
	if b.conn != nil {
		b.detachLocked(b.conn)
	}
	if err := execwire.WriteResponse(conn, execwire.Response{OK: true, Features: b.features}); err != nil {
		return err
	}
	if b.dropped > 0 {
		note := fmt.Sprintf("%d bytes of the workload's output were dropped while nothing was attached", b.dropped)
		if err := execwire.WriteFrame(conn, execwire.TypeNote, []byte(note)); err != nil {
			return err
		}
		b.dropped = 0
	}
	for len(b.backlog) > 0 {
		f := b.backlog[0]
		if err := execwire.WriteFrame(conn, f.typ, f.payload); err != nil {
			// What was not sent stays for the next connection.
			return err
		}
		b.backlog[0] = frame{}
		b.backlog = b.backlog[1:]
		b.queued -= len(f.payload)
	}
	b.conn = conn
	b.changed.Broadcast()
	return nil
}

// Close ends the streams once the workload has finished writing them. It
// waits, up to within, for a host to be attached with nothing left in
// the backlog -- so that a VM about to power off does not take the
// workload's last words with it -- and then hangs up on that host, which
// is how it learns the streams are over. It reports whether everything
// was delivered.
func (b *Broker) Close(within time.Duration) bool {
	deadline := time.Now().Add(within)
	timer := time.AfterFunc(within, func() {
		b.mu.Lock()
		b.changed.Broadcast()
		b.mu.Unlock()
	})
	defer timer.Stop()

	b.mu.Lock()
	defer b.mu.Unlock()
	for !(b.conn != nil && len(b.backlog) == 0) && time.Now().Before(deadline) {
		b.changed.Wait()
	}
	delivered := b.conn != nil && len(b.backlog) == 0
	b.closed = true
	b.changed.Broadcast()
	if b.conn != nil {
		b.detachLocked(b.conn)
	}
	return delivered
}

// ReadOpening reads the line a guest opens a stream connection with, and
// refuses one that does not offer the workload's streams.
func ReadOpening(br *bufio.Reader) (execwire.Response, error) {
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		return resp, fmt.Errorf("reading the guest's opening line: %w", err)
	}
	if !resp.OK {
		return resp, fmt.Errorf("the guest refused the workload's streams: %s", resp.Error)
	}
	if !resp.Supports(FeatureStreams) {
		return resp, fmt.Errorf("the guest does not offer the workload's streams (it names %q)", resp.Features)
	}
	return resp, nil
}
