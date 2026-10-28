// Package sandbox implements the granule sandbox access API (exec, files,
// forwards, and health/info) in both host-local listen mode and controller
// dial-out mode.
//
// The pod-side wrapper (cmd/granule-shim) runs Server outside the VM and
// speaks to cmd/granule-agent inside the guest over virtio-vsock. External
// callers (grain, grit, or local tooling) use Client (or Acceptor in
// dial-out mode) to run commands with detach/reattach across reconnects,
// upload batches of files in one round trip, read files, and check sandbox
// health without going through kubectl exec.
package sandbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

const (
	// ProtocolVersion identifies the wire handshake between Client/Acceptor
	// and Server.
	ProtocolVersion = "granule-sandbox/1"

	// MaxControlLine bounds a JSON handshake or control line on a stream.
	MaxControlLine = 4 << 20
)

// Stream operations carried in StreamRequest.Op when opening a multiplexed
// stream over a guestforward.Session.
const (
	OpExec     = "exec"
	OpReattach = "reattach"
	OpSignal   = "signal"
	OpPutFiles = "put-files"
	OpGetFile  = "get-file"
	OpHealth   = "health"
	OpForward  = "forward"
)

var (
	// ErrTransportCut is returned by Exec or Wait when the underlying
	// stream or session closes before the guest command's terminal exit
	// frame arrives. Callers can distinguish a transport cut from any real
	// command exit code (including 125 or 255) with errors.Is(err,
	// ErrTransportCut) and resume via Client.Reattach.
	ErrTransportCut = errors.New("sandbox: transport cut before command exited")

	// ErrAuthFailed is returned when a handshake fails token, sandbox ID,
	// expiry, or mTLS verification.
	ErrAuthFailed = errors.New("sandbox: authentication failed")

	// ErrExecNotFound is returned by Reattach or Signal when the requested
	// exec ID is unknown to the wrapper.
	ErrExecNotFound = errors.New("sandbox: exec session not found")

	// ErrTokenRequired is returned when a tcp:// listener is started
	// without a per-sandbox token.
	ErrTokenRequired = errors.New("sandbox: a tcp:// listener requires a per-sandbox token")

	// ErrTLSRequired is returned when a tcp:// listener is started without
	// a server certificate.
	ErrTLSRequired = errors.New("sandbox: a tcp:// listener requires a TLS server certificate")

	// ErrTargetRefused is returned by RelayClient when the relay refuses
	// to open a stream to the named sandbox: unknown, not ready, outside
	// the relay's namespace or label selector, or unreachable.
	ErrTargetRefused = errors.New("sandbox: relay refused target")
)

// HandshakeRequest is the opening JSON line sent by the initiator of the
// transport connection (Client in listen mode, Server in dial-out mode).
type HandshakeRequest struct {
	Version   string `json:"version"`
	SandboxID string `json:"sandbox_id,omitempty"`
	Token     string `json:"token,omitempty"`
}

// HandshakeResponse is the opening JSON reply sent by the responder (Server
// in listen mode, Acceptor in dial-out mode).
type HandshakeResponse struct {
	OK        bool   `json:"ok"`
	Version   string `json:"version,omitempty"`
	SandboxID string `json:"sandbox_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// StreamRequest is the JSON payload carried in guestforward.Session.Open's
// metadata when opening a stream for an operation.
type StreamRequest struct {
	Op       string           `json:"op"`
	Exec     *ExecRequest     `json:"exec,omitempty"`
	Reattach *ReattachRequest `json:"reattach,omitempty"`
	Signal   *SignalRequest   `json:"signal,omitempty"`
	GetFile  *GetFileRequest  `json:"get_file,omitempty"`
	Forward  *ForwardRequest  `json:"forward,omitempty"`
}

// ExecRequest describes a command to start inside the sandbox guest.
type ExecRequest struct {
	// ID is an optional caller-chosen identifier for the exec session.
	// When empty, the server generates a unique ID and returns it in
	// ExecReply.ID.
	ID string `json:"id,omitempty"`

	// Argv is the command and arguments to run. When Container is false,
	// Argv is shell-quoted into a command line for the guest account's
	// login shell unless Line is set directly.
	Argv []string `json:"argv,omitempty"`

	// Line is an already shell-quoted command line for the guest's shell.
	// If both Argv and Line are empty, an interactive login shell is run.
	Line string `json:"line,omitempty"`

	// Workdir is the absolute working directory inside the guest (or
	// container). Empty uses the account's home directory (or container
	// WORKDIR).
	Workdir string `json:"workdir,omitempty"`

	// Env are KEY=value entries overlaid on the guest account's default
	// login environment.
	Env []string `json:"env,omitempty"`

	// User is the guest (or container) account to run as. Empty uses the
	// wrapper's configured default user (root).
	User string `json:"user,omitempty"`

	// Container runs the command inside the wrapped container rootfs
	// rather than in the outer guest environment.
	Container bool `json:"container,omitempty"`

	// TTY allocates a pseudo-terminal in the guest.
	TTY  bool   `json:"tty,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Term string `json:"term,omitempty"`
}

// ExecReply is the JSON payload returned in Stream.Confirm when an OpExec
// stream is accepted.
type ExecReply struct {
	ID string `json:"id"`
}

// ReattachRequest asks the wrapper to attach to an existing exec session by
// ID and replay output starting at byte Offset.
type ReattachRequest struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
}

// ReattachReply is returned in Stream.Confirm for an OpReattach stream.
type ReattachReply struct {
	ID       string `json:"id"`
	Exited   bool   `json:"exited"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// SignalRequest asks the wrapper to deliver Signal to the process group of
// exec session ID.
type SignalRequest struct {
	ID     string `json:"id"`
	Signal int    `json:"signal"`
}

// FileUpload is one file in a Client.PutFiles batch.
type FileUpload struct {
	// Path is the destination path inside the guest (must be absolute).
	Path string

	// Content is the file payload. If Reader is nil, Content is sent.
	Content []byte

	// Reader, when non-nil, streams Size bytes as the file payload instead
	// of Content.
	Reader io.Reader
	Size   int64

	// Mode is the file permission bits (e.g. 0755 or 0644). Zero defaults
	// to 0644.
	Mode fs.FileMode

	// Owner is an optional "user", "uid", "user:group", or "uid:gid"
	// ownership specification applied before atomic rename.
	Owner string
}

// FileMeta is the wire header for one file in a PutFilesRequest.
type FileMeta struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mode  uint32 `json:"mode"`
	Owner string `json:"owner,omitempty"`
}

// PutFilesRequest is sent as the opening JSON line on an OpPutFiles stream,
// followed immediately by the raw concatenated bytes of each file.
type PutFilesRequest struct {
	Files []FileMeta `json:"files"`
}

// PutFilesReply is sent as the concluding JSON line on an OpPutFiles stream
// after all files have been atomically placed inside the guest.
type PutFilesReply struct {
	OK      bool   `json:"ok"`
	Written int    `json:"written"`
	Error   string `json:"error,omitempty"`
}

// GetFileRequest asks to read a single file from the guest.
type GetFileRequest struct {
	Path string `json:"path"`
}

// GetFileReply is sent as the opening JSON line on an OpGetFile stream,
// followed by the raw file bytes until EOF.
type GetFileReply struct {
	OK    bool   `json:"ok"`
	Mode  uint32 `json:"mode"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"`
}

// HealthInfo is returned in Stream.Confirm for an OpHealth stream.
type HealthInfo struct {
	Ready         bool     `json:"ready"`
	SandboxID     string   `json:"sandbox_id,omitempty"`
	WrapperVer    string   `json:"wrapper_version,omitempty"`
	AgentFeatures []string `json:"agent_features,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// ForwardRequest asks to open a TCP connection to Host:Port inside the guest.
type ForwardRequest struct {
	Host string `json:"host,omitempty"`
	Port uint16 `json:"port"`
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func readJSONLine(r *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > MaxControlLine {
			return fmt.Errorf("sandbox: control line exceeds %d bytes", MaxControlLine)
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	return json.Unmarshal(line, v)
}

func shellJoin(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
