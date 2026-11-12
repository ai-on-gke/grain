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

	frameStaged    byte = 'Y' // JSON StageReply: the answer to OpStage
	frameListening byte = 'N' // JSON Listening: the answer to OpListen
	frameRenewed   byte = 'T' // JSON RenewReply: the answer to OpRenew
)

// ProtocolVersion is the hostd protocol this build speaks, and
// MinCompatible the oldest one it still serves and still drives.
//
// Protocol 4 is host agent contract v1
// (design/design/agentd-stable-contract.design.md), and it is frozen: new
// behaviour is a new Feature (features.go) the other side checks with
// Hello.Has, never a ProtocolVersion bump, and a new optional JSON field
// an old peer ignores needs neither. ProtocolVersion moves only for a
// major contract change, which the design doc says when (if ever) is
// allowed. Raise MinCompatible only when this build stops serving, or
// stops being able to drive, a version: it must stay at most
// ProtocolVersion-1 for at least the deprecation window after a bump, so
// a daemon and a hostd one version apart keep re-attaching to what is
// already running (the upgrade order is never guaranteed: either side
// may be the newer one). contract_test.go holds both to the golden file.
const (
	ProtocolVersion = 4
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
	// Capacity and Labels are the allowlist's capacity: and labels:, for
	// the daemon holding host work to this hostd's capacity and showing
	// its labels. Labels never route host work here: only the registry's
	// labels for the hostd do. Set by hostd alone.
	Capacity int               `json:"capacity,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`

	// Remote is set by a hostd answering over the session it dialed out
	// to the controller on (grain hostd -server): a machine that shares
	// neither the daemon's filesystem nor its loopback, so a run's files
	// are staged there with OpStage and grain's endpoints reach it with
	// OpListen (carry.go). The three below are said only then.
	Remote bool `json:"remote,omitempty"`
	// Grain is the grain binary on hostd's machine, for the hooks a
	// CLI there forks: the installer's current link when hostd was
	// started through one, else hostd's own executable.
	Grain string `json:"grain,omitempty"`
	// StageRoot is the directory OpStage confines itself to, which
	// every CLI entry's paths_under and cwd_under take on as well for a
	// start over the session.
	StageRoot string `json:"stage_root,omitempty"`
	// CodexAuth is the codex login on hostd's machine, when there is
	// one: what an older daemon links a remote codex run to.
	//
	// Deprecated: use LoginTargets (FeatureLoginsMap). Kept for peers
	// without logins_map until the contract's deprecation window passes.
	CodexAuth string `json:"codex_auth,omitempty"`
	// Logins are the named CLI logins present on hostd's machine, from
	// the allowlist's logins: map (or the built-in table when absent), as
	// names only and never file contents: what a model connection with
	// ambient credentials placed on this hostd spends. Set by hostd
	// alone, local or remote; Has(FeatureLogins) distinguishes "none"
	// (empty with FeatureLogins) from "not known" (without it).
	Logins []string `json:"logins,omitempty"`
	// LoginTargets maps each existing regular-file login in the
	// allowlist's logins: map (or the built-in table when absent) to its
	// resolved path on hostd's machine, set on remote sessions for
	// StageSymlink (FeatureLoginsMap).
	LoginTargets map[string]string `json:"login_targets,omitempty"`
	// AgentsOnly is set by a host agent that runs agent CLIs and nothing
	// else (grain-agentd, Server.AgentsOnly): its allowlist has no
	// capabilities: and may not have any, so a run: host capability
	// server placed on it needs a `grain host` instead. An older build
	// says nothing, which is "not known".
	AgentsOnly bool `json:"agents_only,omitempty"`

	// Features are the contract features this side's build implements
	// (features.go). Check one with Has, never by comparing Protocol: a
	// peer that predates the list is credited from its protocol version.
	Features []Feature `json:"features,omitempty"`
}

// ThisHello is this build's Hello; build is its version string.
func ThisHello(build string) Hello {
	return Hello{Protocol: ProtocolVersion, MinCompatible: MinCompatible, Build: build,
		Features: append([]Feature(nil), Features...)}
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
func (h Hello) SupportsAttach() bool { return h.Has(FeatureDetach) }

// SupportsSocketOwner reports whether a hostd that said h serves
// OpSocketOwner.
func (h Hello) SupportsSocketOwner() bool { return h.Has(FeatureSocketOwner) }

// SupportsCarry reports whether a hostd that said h serves OpStage and
// OpListen, and opens OpCarry streams back to the daemon.
func (h Hello) SupportsCarry() bool { return h.Has(FeatureStage) && h.Has(FeatureListen) }

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

	// OpStage is one filesystem operation on hostd's machine, confined
	// to Hello.StageRoot (Request.Stage): how a run's files are staged
	// where its CLI runs, when that is not where the daemon is. Served
	// only over a session hostd dialed out on.
	OpStage Op = "stage"
	// OpListen asks hostd to listen on its own loopback and carry every
	// connection made there back to the daemon, as a stream hostd opens
	// over the session, beginning with an OpCarry request naming ID. It
	// is answered with frameListening, and the listener lives until the
	// daemon closes the request's stream. Served only over a session.
	OpListen Op = "listen"
	// OpCarry is the first frame of a stream hostd opens to the daemon:
	// a connection made to the OpListen listener whose ID it names.
	OpCarry Op = "carry"
	// OpRenew silently renews a connected host's 30-day credential in-band
	// over its active mux session, signed by the host's Ed25519 private key.
	OpRenew Op = "renew"
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
	// Handover, on OpDrain with Idle, says the binary replacing this hostd
	// adopts live detached spools (FeatureHandover): a local hostd with a
	// Reexec continuation hands running detached CLI processes over across
	// its own restart rather than waiting for them to end or draining.
	Handover bool `json:"handover,omitempty"`
	// Inode is the socket inode OpSocketOwner asks about.
	Inode uint64 `json:"inode,omitempty"`
	// Stage is OpStage's operation.
	Stage *StageOp `json:"stage,omitempty"`
	// Renew is OpRenew's Ed25519 proof-of-possession payload.
	Renew *RenewProof `json:"renew,omitempty"`
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
	// Draining says hostd ended it for its own upgrade or restart (a drain
	// reaching drain_cap, or a previous hostd stopping without handing it
	// over), rather than the task or its CLI failing.
	Draining bool `json:"draining,omitempty"`
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
