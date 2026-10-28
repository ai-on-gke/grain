package hostd

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// The wire protocol is one process per connection, in frames: a one-byte
// type, a four-byte big-endian length, and that many bytes of payload.
// The daemon's first frame is a Request; everything after it is stdio and
// control until hostd's Exit (or Refused) frame ends the connection.
//
// Protocol 2 adds detached starts (Request.Detach): a process whose
// stdout and stderr go to a spool on the host rather than down the
// connection, which outlives the daemon until its own deadline, and which
// a daemon -- the one that started it or the next -- reads from a cursor
// (OpAttach). OpProcesses lists them for the daemon's adoption pass,
// OpCancel releases one, OpDrain starts a graceful drain, and OpHello
// exchanges versions (Hello).
const (
	// daemon -> hostd
	frameRequest    byte = 'R' // JSON Request
	frameStdin      byte = 'I' // bytes for the process's stdin
	frameStdinClose byte = 'C' // stdin is done
	frameSignal     byte = 'S' // JSON SignalMsg

	// hostd -> daemon
	frameStarted byte = 'P' // JSON Started: the process is running
	frameStdout  byte = 'O'
	frameStderr  byte = 'E'
	frameExit    byte = 'X' // JSON Exit: the last frame of a started process
	frameRefused byte = 'F' // JSON Refusal: the request was not run
	frameOffers  byte = 'L' // JSON []Offer: the answer to OpList

	// Protocol 2.
	frameHello     byte = 'H' // JSON Hello: the answer to OpHello
	frameProcesses byte = 'Q' // JSON []Process: the answer to OpProcesses
	frameOK        byte = 'K' // empty: OpCancel or OpDrain was done

	// Protocol 3.
	frameSocketOwner byte = 'W' // JSON SocketOwner: the answer to OpSocketOwner
)

// ProtocolVersion is the hostd protocol this build speaks, and
// MinCompatible the oldest one it still serves and still drives. Bump
// ProtocolVersion for any change a peer of the previous version would
// misread or could not use -- a new op or frame the other side must
// understand, a changed meaning for a field, a different spool layout --
// never for a new optional JSON field an old peer ignores. Raise
// MinCompatible only when this build stops serving, or stops being able
// to drive, a version: it must stay at most ProtocolVersion-1 for at
// least one release after a bump, so a daemon and a hostd one version
// apart keep re-attaching to what is already running (the upgrade order
// is never guaranteed: either side may be the newer one).
const (
	ProtocolVersion = 3
	MinCompatible   = 1
)

// Hello is what each side says about itself on OpHello.
type Hello struct {
	Protocol      int    `json:"protocol_version"`
	MinCompatible int    `json:"min_compatible"`
	Build         string `json:"build,omitempty"`
	// Draining is set by a hostd that is draining for an upgrade: it
	// refuses new starts, and still lists, attaches and cancels.
	Draining bool `json:"draining,omitempty"`
	// Cutover is set by a hostd cutting over to a new build
	// (Server.Cutover): it still takes starts, and exits the first moment
	// nothing runs.
	Cutover bool `json:"cutover,omitempty"`
	// Running is the installer's version of grain this hostd runs, and
	// Current the version the installer's current link names now, with
	// that version's build: the two differ while an upgrade waits on this
	// hostd's drain. Set by hostd alone, and only where it was started
	// through that link (CurrentLinkFor).
	Running      string `json:"running,omitempty"`
	Current      string `json:"current,omitempty"`
	CurrentBuild string `json:"current_build,omitempty"`
}

// ThisHello is this build's Hello; build is its version string.
func ThisHello(build string) Hello {
	return Hello{Protocol: ProtocolVersion, MinCompatible: MinCompatible, Build: build}
}

// legacyHello is what a hostd that does not know OpHello is taken to
// speak: protocol 1, the first one, which only starts and lists.
var legacyHello = Hello{Protocol: 1, MinCompatible: 1}

// Compatible reports whether two peers can talk: each must speak at
// least the other's minimum. nil means they can; otherwise the error
// names the mismatch, for the run and for Settings.
func Compatible(local, peer Hello) error {
	if peer.Protocol >= local.MinCompatible && local.Protocol >= peer.MinCompatible {
		return nil
	}
	return &MismatchError{Local: local, Peer: peer}
}

// MismatchError is two protocol versions that cannot talk.
type MismatchError struct{ Local, Peer Hello }

func (e *MismatchError) Error() string {
	peer := e.Peer.Build
	if peer == "" {
		peer = "an unknown build"
	}
	return fmt.Sprintf("grain hostd is updating or mismatched: it speaks hostd protocol %d (needs at least %d; %s), "+
		"this daemon speaks %d (needs at least %d)", e.Peer.Protocol, e.Peer.MinCompatible, peer,
		e.Local.Protocol, e.Local.MinCompatible)
}

// SupportsAttach reports whether a hostd that said peer serves detached
// starts, OpProcesses, OpAttach and OpCancel.
func (h Hello) SupportsAttach() bool { return h.Protocol >= 2 }

// SupportsSocketOwner reports whether a hostd that said h serves
// OpSocketOwner.
func (h Hello) SupportsSocketOwner() bool { return h.Protocol >= 3 }

// WaitDelay bounds how long hostd waits for a process's stdout and stderr
// to close once the process itself has exited or been killed -- the same
// 5 s agent.LocalHost uses, for the same reason: a descendant that
// inherited them must not hold a run open after its CLI is gone.
const WaitDelay = 5 * time.Second

// DefaultDisconnectGrace is how long a process outlives the connection
// that started it. Losing the daemon closes the process's stdin at once;
// a process still running this long afterwards has its group killed.
const DefaultDisconnectGrace = 10 * time.Second

// maxFrame bounds one frame's payload, so a peer cannot make the other
// side allocate without limit.
const maxFrame = 1 << 20

// Op is what a Request asks for.
type Op string

const (
	// OpStart starts an allowlist entry.
	OpStart Op = "start"
	// OpList asks which entries this host offers.
	OpList Op = "list"

	// Protocol 2.

	// OpHello exchanges Hello.
	OpHello Op = "hello"
	// OpProcesses lists the processes hostd is running or holds the
	// final spool of.
	OpProcesses Op = "processes"
	// OpAttach streams a detached process's spool from a cursor, then its
	// exit.
	OpAttach Op = "attach"
	// OpCancel kills a process's group if it still runs and drops its
	// spool: what a daemon does with a process nobody claims.
	OpCancel Op = "cancel"
	// OpDrain starts a graceful drain (Server.Drain), or with Idle a
	// cutover (Server.Cutover).
	OpDrain Op = "drain"

	// Protocol 3.

	// OpSocketOwner asks which process in a tree hostd started holds a
	// socket inode open (portforward.Relay.InodeOwner).
	OpSocketOwner Op = "socket_owner"
)

// Request is the daemon's first frame.
type Request struct {
	Op   Op       `json:"op"`
	Kind Kind     `json:"kind,omitempty"`
	Name string   `json:"name,omitempty"`
	Args []string `json:"args,omitempty"`
	Env  []string `json:"env,omitempty"`
	Dir  string   `json:"dir,omitempty"`
	// Deadline, when set, is the latest the process may run until. hostd
	// also applies its entry's max_duration, and enforces whichever is
	// sooner itself.
	Deadline time.Time `json:"deadline,omitzero"`

	// Hello is the daemon's, on OpHello.
	Hello *Hello `json:"hello,omitempty"`
	// Detach, on OpStart, spools the process's output on the host and
	// keeps it running when the connection drops, until its deadline:
	// the daemon re-attaches with OpAttach. Run and Task name the
	// task_run row and task it belongs to, for the listing.
	Detach bool   `json:"detach,omitempty"`
	Run    string `json:"run,omitempty"`
	Task   string `json:"task,omitempty"`
	// ID names the process, on OpAttach and OpCancel.
	ID string `json:"id,omitempty"`
	// Cursor is where OpAttach resumes each stream.
	Cursor Cursor `json:"cursor,omitzero"`
	// Idle, on OpDrain, asks for a cutover rather than a drain: new
	// starts are still taken, and hostd exits the moment nothing runs. A
	// hostd that predates it drains.
	Idle bool `json:"idle,omitempty"`
	// Inode is the socket inode OpSocketOwner asks about.
	Inode uint64 `json:"inode,omitempty"`
}

// SocketOwner is the answer to OpSocketOwner: the pid holding Request.Inode
// open, when that pid belongs to a process tree this hostd started and is
// still running, or 0 when none does.
type SocketOwner struct {
	PID int `json:"pid,omitempty"`
}

// Cursor is a byte offset into each of a spool's streams. Like a run
// host's (runhost.ReadStdout), it counts bytes a daemon has already been
// given: resuming from it neither repeats nor skips anything.
type Cursor struct {
	Stdout int64 `json:"stdout,omitempty"`
	Stderr int64 `json:"stderr,omitempty"`
}

// Process is one entry of OpProcesses' listing.
type Process struct {
	ID       string    `json:"id"`
	Run      string    `json:"run,omitempty"`
	Task     string    `json:"task,omitempty"`
	Kind     Kind      `json:"kind"`
	Name     string    `json:"name"`
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline,omitzero"`
	// Detached is false for a protocol-1 start, which streams over its
	// connection and is not attachable.
	Detached bool `json:"detached,omitempty"`
	// Spool is the spool directory on the host, and Cursor where each
	// stream in it ends now.
	Spool  string `json:"spool,omitempty"`
	Cursor Cursor `json:"cursor,omitzero"`
	// Exited is set once the process has ended: Exit says how, and the
	// spool is final until a daemon attaches and reads the rest.
	Exited bool  `json:"exited,omitempty"`
	Exit   *Exit `json:"exit,omitempty"`
	// Attached says a daemon connection is reading it now.
	Attached bool `json:"attached,omitempty"`
}

// SignalMsg asks hostd to signal the process's whole group.
type SignalMsg struct {
	// Signal is one of TERM, INT, HUP or KILL.
	Signal string `json:"signal"`
}

// Started says the process is running.
type Started struct {
	PID int `json:"pid"`
	// ID is the process's hostd id, for a detached start or an attach.
	ID string `json:"id,omitempty"`
}

// Exit is how a started process ended.
type Exit struct {
	// Code is the exit status, or -1 when the process was killed by a
	// signal or never ran.
	Code int `json:"code"`
	// Signal names the signal that killed it, if one did.
	Signal string `json:"signal,omitempty"`
	// Deadline says hostd killed it for running past its deadline.
	Deadline bool `json:"deadline,omitempty"`
	// WaitDelay says the process exited but something it started still
	// held its output open, and hostd stopped waiting for that (the same
	// bound agent.LocalHost puts on a local CLI).
	WaitDelay bool `json:"wait_delay,omitempty"`
	// Error is set when the process could not be started or waited for.
	Error string `json:"error,omitempty"`
	// FinishedAt is when it ended. With Code and Error it is the same
	// exit.json a run host's spool ends with (runhost.Exit).
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

// Refusal is a request hostd would not run.
type Refusal struct {
	Reason string `json:"reason"`
	// Draining says the refusal is a draining hostd's: try again on the
	// hostd that replaces it.
	Draining bool `json:"draining,omitempty"`
}

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > maxFrame {
		return fmt.Errorf("hostd: frame of %d bytes exceeds %d", len(payload), maxFrame)
	}
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(append(hdr[:], payload...)); err != nil {
		return err
	}
	return nil
}

func writeJSON(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, typ, b)
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("hostd: frame of %d bytes exceeds %d", n, maxFrame)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return hdr[0], buf, nil
}

// chunkWriter turns writes into frames of one type, split at maxFrame.
type chunkWriter struct {
	typ  byte
	send func(typ byte, payload []byte) error
}

func (c chunkWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), maxFrame)
		if err := c.send(c.typ, p[:k]); err != nil {
			return n, err
		}
		n += k
		p = p[k:]
	}
	return n, nil
}
