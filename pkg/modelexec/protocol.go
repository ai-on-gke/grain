// Package modelexec runs an agent CLI on a remote executor machine separate
// from the main grain installation.
//
// Grain's built-in frameworks (pkg/agent/antigravity, pkg/agent/claude,
// pkg/agent/codex) drive a real CLI as a subprocess. By default that
// subprocess is a child of the daemon, so the CLI, its credential and its
// private per-run HOME all live inside the deployment image.
//
// With remote execution, a daemon (`grain executor` or `grain modelexec`)
// runs on a remote host, is configured with ambient permissions and local CLI
// binaries, and establishes an outbound connection to Grain.
// Each remote executor shows up as a source associated with a specific team.
//
// When a task is dispatched, the CLI runs on the remote host while its MCP tool
// calls are tunneled back to Grain to execute in Grain's sandboxes.
package modelexec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ProtocolVersion is the wire version both ends announce in the handshake.
const ProtocolVersion = 2

// Frame types for control stream.
const (
	frameHello     = "hello"
	frameWelcome   = "welcome"
	framePing      = "ping"
	framePong      = "pong"
	frameMkdirTemp = "mkdir_temp"
	frameMkdirAll  = "mkdir_all"
	frameWriteFile = "write_file"
	frameCreate    = "create_temp"
	frameSymlink   = "symlink"
	frameReadTail  = "read_tail"
	frameRemoveAll = "remove_all"
	frameExec      = "exec"
	frameCancel    = "cancel"
	frameStdout    = "stdout"
	frameResult    = "result"
)

// frame is every message on the control connection.
type frame struct {
	Type string `json:"type"`
	ID   uint64 `json:"id,omitempty"`

	// Handshake.
	ProtocolVersion int               `json:"protocol_version,omitempty"`
	Token           string            `json:"token,omitempty"`
	Host            string            `json:"host,omitempty"`
	InstanceID      string            `json:"instance_id,omitempty"`
	Binaries        map[string]string `json:"binaries,omitempty"`
	ServerURL       string            `json:"server_url,omitempty"`
	CodexAuthFile   string            `json:"codex_auth_file,omitempty"`
	GitProxyURL     string            `json:"git_proxy_url,omitempty"`
	// CapabilityMCPURL is the loopback origin the executor carries
	// capability MCP requests from (capabilitymcp.go); empty from an
	// executor that predates it, which is not given capability servers.
	CapabilityMCPURL string `json:"capability_mcp_url,omitempty"`
	Concurrency      int    `json:"concurrency,omitempty"`

	// Filesystem operations.
	Path      string `json:"path,omitempty"`
	Target    string `json:"target,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	Mode      uint32 `json:"mode,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// Exec.
	Args   []string `json:"args,omitempty"`
	Env    []string `json:"env,omitempty"`
	Stdin  string   `json:"stdin,omitempty"`
	Stderr string   `json:"stderr,omitempty"`

	// Error is set on a result frame that failed, and on a welcome frame refusing the connection.
	Error string `json:"error,omitempty"`
}

// Stream types across the multiplexer.
const (
	StreamTypeMCP      = "mcp"
	StreamTypeGitProxy = "gitproxy"
	// StreamTypeCapabilityMCP is one connection to the executor's
	// capability MCP listener, carried to grain (capabilitymcp.go).
	StreamTypeCapabilityMCP = "capability-mcp"
)

// StreamHeader is sent at the beginning of a secondary tunnel stream across the multiplexer.
type StreamHeader struct {
	Type  string   `json:"type,omitempty"`
	RunID string   `json:"run_id,omitempty"`
	Args  []string `json:"args,omitempty"`
}

// codec is an io.ReadWriteCloser plus newline-delimited JSON framing.
type codec struct {
	rwc io.ReadWriteCloser
	dec *json.Decoder
	mu  sync.Mutex
	enc *json.Encoder
}

func newCodec(rwc io.ReadWriteCloser) *codec {
	return &codec{
		rwc: rwc,
		dec: json.NewDecoder(bufio.NewReaderSize(rwc, 64<<10)),
		enc: json.NewEncoder(rwc),
	}
}

func (c *codec) read(idle time.Duration) (frame, error) {
	if conn, ok := c.rwc.(net.Conn); ok && idle > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return frame{}, err
		}
	}
	var f frame
	if err := c.dec.Decode(&f); err != nil {
		return frame{}, err
	}
	return f, nil
}

func (c *codec) write(f frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.rwc.(net.Conn); ok {
		if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return err
		}
	}
	return c.enc.Encode(f)
}

func (c *codec) close() error { return c.rwc.Close() }

const (
	writeTimeout = 30 * time.Second
	pingInterval = 30 * time.Second
	idleTimeout  = 3 * pingInterval
	cancelGrace  = 15 * time.Second
	stdoutChunk  = 32 << 10
	stderrLimit  = 64 << 10
)

func errorf(id uint64, format string, args ...any) frame {
	return frame{Type: frameResult, ID: id, Error: fmt.Sprintf(format, args...)}
}
