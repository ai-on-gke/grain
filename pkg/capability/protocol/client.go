package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// mcpProtocolVersion is the MCP revision Grain asks for in initialize and
// sends as MCP-Protocol-Version over HTTP.
const mcpProtocolVersion = "2025-06-18"

// transport carries JSON-RPC 2.0 to one capability server.
type transport interface {
	// roundTrip sends one request (or, with id 0, a notification) and
	// returns the response line for id, or nil for a notification.
	roundTrip(ctx context.Context, id int64, msg []byte) ([]byte, error)
	close() error
}

// Client is an MCP client of one capability server's control plane
// (§4.0), over stdio or Streamable HTTP.
type Client struct {
	t      transport
	nextID atomic.Int64
	// ServerName is serverInfo.name from initialize.
	ServerName string
}

// RPCError is a JSON-RPC error a server answered with.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp: %s (code %d)", e.Message, e.Code) }

// DialOptions are what attaching to a configured server needs beyond its
// entry.
type DialOptions struct {
	// ConfigRoot is the checked-out grain/config a relative stdio command
	// is resolved under.
	ConfigRoot string
	// Home is the private HOME a stdio server is given; empty means a
	// fresh temporary directory, removed on Close.
	Home string
	// HTTPClient carries HTTP servers' requests; nil is a plain client.
	// Its CheckRedirect is replaced: redirects are never followed.
	HTTPClient *http.Client
	// Stderr receives a stdio server's diagnostics; nil discards them.
	Stderr io.Writer
	// Hostd is how a run: host server is started on the bare machine,
	// for a daemon in the release container. Nil refuses such a server.
	Hostd *hostd.Client
	// Identity is the Grain attaching the server, filled into its
	// configuration's identity fields (WithIdentity, §4.5.7).
	Identity Identity
	// Managed keeps the values Grain generates for the server's managed
	// configure fields (WithManagedSecrets); nil generates none.
	Managed ManagedSecrets
}

// Dial connects to a server whose secrets are already resolved
// (ServerConfig.Resolve) and performs the initialize handshake (§4.0.1).
// A stdio server is spawned with its isolated environment (§3.1.3) and
// lives until Close.
func Dial(ctx context.Context, cfg ServerConfig, opts DialOptions) (*Client, error) {
	var t transport
	if cfg.OnHost() {
		st, err := startHostd(ctx, cfg, opts)
		if err != nil {
			return nil, err
		}
		t = st
	} else if cfg.Stdio() {
		st, err := startStdio(cfg, opts)
		if err != nil {
			return nil, err
		}
		t = st
	} else {
		// A capability server is one fixed, operator-configured endpoint
		// and never has a reason to redirect. Following one would re-send
		// the resolved secret headers (Go strips only Authorization and
		// cookies across hosts) and, on a 307/308, the mint request to
		// wherever Location points, so a 3xx surfaces as an error instead.
		// A caller's client is copied rather than modified.
		hc := &http.Client{}
		if opts.HTTPClient != nil {
			copied := *opts.HTTPClient
			hc = &copied
		}
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		t = &httpTransport{url: cfg.ServerURL, headers: cfg.Headers, client: hc}
	}
	c := &Client{t: t}
	if err := c.initialize(ctx); err != nil {
		t.close()
		return nil, err
	}
	return c, nil
}

// Close ends the session; a stdio server's process is stopped.
func (c *Client) Close() error { return c.t.close() }

// Ended reports whether a stdio server's process has ended: it exited by
// itself, or, for a run: host server, its run through grain hostd ended,
// as a drain for hostd's own upgrade ends it. Every call over the session
// fails from then on, so whoever holds it starts the server again. An
// HTTP server's session never reports ended; a failed refresh is how its
// loss shows.
func (c *Client) Ended() bool {
	if t, ok := c.t.(*stdioTransport); ok {
		select {
		case <-t.done:
			return true
		default:
		}
	}
	return false
}

// ServiceProcess identifies the server's own service, which its ports'
// listeners must belong to (§5.3.2): a stdio server's spawned child's pid,
// or, for an HTTP server, 0 and its serverUrl.
func (c *Client) ServiceProcess() (pid int, serverURL string) {
	switch t := c.t.(type) {
	case *stdioTransport:
		if t.cmd != nil && t.cmd.Process != nil {
			return t.cmd.Process.Pid, ""
		}
		if pid := t.hostPID.Load(); pid > 0 {
			return int(pid), ""
		}
	case *httpTransport:
		return 0, t.url
	}
	return 0, ""
}

func (c *Client) initialize(ctx context.Context) error {
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "grain", "version": "1"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var res struct {
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("initialize: decoding result: %w", err)
	}
	c.ServerName = res.ServerInfo.Name
	return c.notify(ctx, "notifications/initialized")
}

func (c *Client) notify(ctx context.Context, method string) error {
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	_, err := c.t.roundTrip(ctx, 0, msg)
	return err
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	msg, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	line, err := c.t.roundTrip(ctx, id, msg)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("mcp: decoding response: %w", err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// ListTools issues tools/list, following nextCursor to the end (§4.0.1).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for page := 0; ; page++ {
		if page > 1000 {
			return nil, errors.New("tools/list: server keeps returning nextCursor")
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("tools/list: decoding result: %w", err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
}

// CallTool issues tools/call and returns the result's JSON: structuredContent
// when present, else content[0].text (§4.0). A result with isError is an
// error carrying its text.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s: decoding result: %w", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		text = res.Content[0].Text
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: server reported an error: %s", name, truncate(text, 4096))
	}
	if len(res.StructuredContent) > 0 && string(res.StructuredContent) != "null" {
		return res.StructuredContent, nil
	}
	if text == "" {
		return nil, fmt.Errorf("%s: result carries neither structuredContent nor text", name)
	}
	return json.RawMessage(text), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// stdioTransport is newline-delimited JSON-RPC over a child's pipes. Calls
// are serialized: one request is outstanding at a time.
type stdioTransport struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	// stop, when set, kills a server that is not this process's child:
	// one grain hostd started (startHostd), whose cmd is nil.
	stop    func()
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	tmpHome string
	done    chan struct{}
	// hostPID is a run: host server's pid as grain hostd reported it
	// starting (hostd.Client.OnStarted): the release container shares the
	// host's pid namespace, so it is the process its ports' listeners
	// belong to. Atomic, not under mu, which a round trip holds while
	// hostd reports the start.
	hostPID atomic.Int64
}

// startHostd starts a run: host server through grain hostd, with its
// stdio carried over hostd's socket: the transport writes the server's
// stdin into one pipe and reads its stdout out of another, and
// hostd.Client.Run streams both. The entry is checked against what hostd
// offers first, so a server hostd's allowlist lacks fails to attach
// saying so, rather than as a bare refusal.
func startHostd(ctx context.Context, cfg ServerConfig, opts DialOptions) (*stdioTransport, error) {
	if opts.Hostd == nil {
		return nil, fmt.Errorf("capability server %s is set to run: host, which needs a daemon in the release "+
			"container with grain hostd beside it; this daemon has no hostd to start it through", cfg.Command)
	}
	offers, err := opts.Hostd.Offers(ctx)
	if err != nil {
		return nil, fmt.Errorf("capability server %s is set to run: host: %w", cfg.Command, err)
	}
	offered := false
	for _, o := range offers {
		offered = offered || (o.Kind == hostd.KindCapability && o.Name == cfg.Command)
	}
	if !offered {
		return nil, fmt.Errorf("capability server %s is set to run: host, but grain hostd's allowlist (%s) offers no capability entry %q: %s",
			cfg.Command, hostd.DefaultAllowlistPath, cfg.Command, hostd.MissingEntryHelp(cfg.Command))
	}
	env := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	// The server outlives the attach's own ctx, exactly as a spawned
	// child does: it is stopped by close, never by the dial returning.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t := &stdioTransport{stop: cancel, stdin: stdinW, stdout: bufio.NewReaderSize(stdoutR, 1<<20), done: make(chan struct{})}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	client := *opts.Hostd
	onStarted := client.OnStarted
	client.OnStarted = func(st hostd.Started) {
		t.hostPID.Store(int64(st.PID))
		if onStarted != nil {
			onStarted(st)
		}
	}
	go func() {
		defer close(t.done)
		defer cancel()
		_, err := client.Run(runCtx, hostd.Request{Kind: hostd.KindCapability, Name: cfg.Command, Args: cfg.Args, Env: env},
			stdinR, stdoutW, stderr)
		if err == nil {
			err = io.EOF
		}
		// Whatever is still reading the server's output -- an
		// outstanding call, or the next one -- sees it gone.
		stdoutW.CloseWithError(err)
		stdinR.CloseWithError(err)
	}()
	return t, nil
}

func startStdio(cfg ServerConfig, opts DialOptions) (*stdioTransport, error) {
	bin, err := cfg.resolveCommand(opts.ConfigRoot)
	if err != nil {
		return nil, err
	}
	bin = resolveBuiltinExecutable(bin, os.Executable)
	home, tmpHome := opts.Home, ""
	if home == "" {
		if tmpHome, err = os.MkdirTemp("", "grain-capability-home-"); err != nil {
			return nil, err
		}
		home = tmpHome
	}
	cmd := exec.Command(bin, cfg.Args...)
	cmd.Env = childEnv(osEnviron(), home, cfg.Env)
	cmd.Dir = home
	cmd.Stderr = opts.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		if tmpHome != "" {
			os.RemoveAll(tmpHome)
		}
		return nil, fmt.Errorf("starting capability server %s: %w", cfg.Command, err)
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20), tmpHome: tmpHome, done: make(chan struct{})}
	go func() { cmd.Wait(); close(t.done) }()
	return t, nil
}

// resolveBuiltinExecutable runs a built-in capability entry that names
// "grain" with the running grain binary rather than whatever "grain" is
// first on PATH. The two need not be the same program: a sandbox image's
// /usr/local/bin/grain is the in-sandbox host-script shim, which has no
// "capability" command, so grain demo started by path there (local runs,
// the E2E fixtures) attached its built-ins to a binary that exited at
// once. Only when the running binary is not grain -- a go test binary
// (*.test), say, so unit tests that expect an unresolvable "grain" command
// to fail keep failing -- is the bare name left for PATH to resolve.
func resolveBuiltinExecutable(bin string, executable func() (string, error)) string {
	if bin != "grain" || executable == nil {
		return bin
	}
	exe, err := executable()
	if err != nil || exe == "" {
		return bin
	}
	base := filepath.Base(exe)
	if base == "grain" || (strings.HasPrefix(base, "grain-") && !strings.HasSuffix(base, ".test")) {
		return exe
	}
	return bin
}

func (t *stdioTransport) roundTrip(ctx context.Context, id int64, msg []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.stdin.Write(append(msg, '\n')); err != nil {
		return nil, fmt.Errorf("mcp: writing to capability server: %w", err)
	}
	if id == 0 {
		return nil, nil
	}
	type answer struct {
		line []byte
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		want := strconv.FormatInt(id, 10)
		for {
			line, err := t.stdout.ReadBytes('\n')
			if err != nil {
				ch <- answer{err: fmt.Errorf("mcp: capability server closed its output: %w", err)}
				return
			}
			var probe struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(line, &probe) != nil {
				continue
			}
			// Notifications and server-to-client requests are not
			// answers; skip them.
			if probe.Method != "" || string(probe.ID) != want {
				continue
			}
			ch <- answer{line: line}
			return
		}
	}()
	select {
	case a := <-ch:
		return a.line, a.err
	case <-ctx.Done():
		// The reader goroutine is left blocked on a stream whose
		// framing can no longer be trusted, so the server is stopped.
		t.kill()
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) kill() {
	if t.stop != nil {
		t.stop()
		return
	}
	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
}

func (t *stdioTransport) close() error {
	t.stdin.Close()
	select {
	case <-t.done:
	default:
		t.kill()
		<-t.done
	}
	if t.tmpHome != "" {
		os.RemoveAll(t.tmpHome)
	}
	return nil
}

// httpTransport is the Streamable HTTP transport: one POST per message,
// answered with JSON or an SSE stream.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	session string
}

func (t *httpTransport) roundTrip(ctx context.Context, id int64, msg []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	t.mu.Unlock()
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		t.mu.Lock()
		t.session = s
		t.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mcp: capability server answered HTTP %d", resp.StatusCode)
	}
	if id == 0 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, nil
	}
	body := io.LimitReader(resp.Body, 32<<20)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEAnswer(body, id)
	}
	return io.ReadAll(body)
}

// readSSEAnswer reads an SSE stream until the event carrying the response
// to id.
func readSSEAnswer(r io.Reader, id int64) ([]byte, error) {
	want := strconv.FormatInt(id, 10)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	var data bytes.Buffer
	flush := func() []byte {
		defer data.Reset()
		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if data.Len() == 0 || json.Unmarshal(data.Bytes(), &probe) != nil {
			return nil
		}
		if probe.Method == "" && string(probe.ID) == want {
			return append([]byte(nil), data.Bytes()...)
		}
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if got := flush(); got != nil {
				return got, nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	if got := flush(); got != nil {
		return got, nil
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("mcp: reading event stream: %w", err)
	}
	return nil, errors.New("mcp: event stream ended without a response")
}

func (t *httpTransport) close() error {
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	if session == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, t.url, nil)
	if err != nil {
		return nil
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", session)
	if resp, err := t.client.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}
