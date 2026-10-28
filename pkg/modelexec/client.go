package modelexec

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/api/idtoken"

	"github.com/ai-on-gke/grain/pkg/procgroup"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

// Daemon is the remote executor side: it runs on a host separate from the Grain
// controller, connects outbound to Grain, and executes agent CLIs with ambient
// permissions while tunneling MCP tool calls back to Grain's sandboxes.
type Daemon struct {
	Addr             string
	Token            string
	Name             string
	InstanceID       string
	Binaries         map[string]string
	ServerURL        string
	CodexAuthFile    string
	Concurrency      int
	TLS              *tls.Config
	IAPAudience      string
	Audience         string
	IAPTokenProvider TokenProvider
	TokenProvider    func(ctx context.Context) (string, error)
	HTTPUpgrade      bool
	Logf             func(format string, args ...any)
	Dial             func(network, addr string) (net.Conn, error)

	MinBackoff time.Duration
	MaxBackoff time.Duration

	PingInterval time.Duration
	IdleTimeout  time.Duration

	tokenMu      sync.Mutex
	cachedToken  string
	cachedExpiry time.Time
}

func (d *Daemon) iapAudience() string {
	if d.IAPAudience != "" {
		return d.IAPAudience
	}
	return d.Audience
}

// CheckBinaries validates that every named binary exists and is executable.
func (d *Daemon) CheckBinaries() error {
	for name, path := range d.Binaries {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return fmt.Errorf("modelexec: -binary %s=%s: %w", name, path, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("modelexec: -binary %s=%s: %w", name, path, err)
		}
		if info.IsDir() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("modelexec: -binary %s=%s is not executable", name, path)
		}
		d.Binaries[name] = resolved
	}
	return nil
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Daemon) dial(ctx context.Context) (net.Conn, error) {
	addr := strings.TrimSpace(d.Addr)
	isURL := strings.Contains(addr, "://")
	if isURL || d.HTTPUpgrade {
		return d.dialHTTPUpgrade(ctx, addr)
	}

	var rawConn net.Conn
	var err error
	if d.Dial != nil {
		rawConn, err = d.Dial("tcp", d.Addr)
	} else {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		if d.TLS != nil {
			rawConn, err = tls.DialWithDialer(dialer, "tcp", d.Addr, d.TLS)
		} else {
			rawConn, err = dialer.DialContext(ctx, "tcp", d.Addr)
		}
	}
	if err != nil {
		return nil, err
	}

	if d.iapAudience() != "" || d.IAPTokenProvider != nil || d.TokenProvider != nil {
		conn, err := d.performIAPHandshake(ctx, rawConn)
		if err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		return conn, nil
	}
	return rawConn, nil
}

func (d *Daemon) performIAPHandshake(ctx context.Context, rawConn net.Conn) (net.Conn, error) {
	token, err := d.resolveToken(ctx, d.Addr)
	if err != nil {
		return nil, fmt.Errorf("modelexec: acquiring IAP OIDC token: %w", err)
	}

	target := d.Addr
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "//"+target, nil)
	if err != nil {
		return nil, fmt.Errorf("modelexec: building IAP connect request: %w", err)
	}
	req.Host = target
	if token != "" {
		token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
		req.Header.Set("Proxy-Authorization", "Bearer "+token)
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "grain-executor")

	if err := req.Write(rawConn); err != nil {
		return nil, fmt.Errorf("modelexec: writing IAP connect request: %w", err)
	}

	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("modelexec: reading IAP connect response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("modelexec: IAP proxy authentication failed with status %d (%s): %s", resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
	}

	return wrapConnWithReader(rawConn, br), nil
}

func (d *Daemon) dialHTTPUpgrade(ctx context.Context, rawURL string) (net.Conn, error) {
	if !strings.Contains(rawURL, "://") {
		if d.TLS != nil || strings.HasSuffix(rawURL, ":443") {
			rawURL = "https://" + rawURL
		} else {
			rawURL = "http://" + rawURL
		}
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("modelexec: parsing server URL %q: %w", rawURL, err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" || u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	var rawConn net.Conn
	if d.Dial != nil {
		rawConn, err = d.Dial("tcp", host)
	} else {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		if u.Scheme == "https" || u.Scheme == "wss" || d.TLS != nil {
			tlsConfig := d.TLS
			if tlsConfig == nil {
				tlsConfig = &tls.Config{
					ServerName: u.Hostname(),
					MinVersion: tls.VersionTLS12,
				}
			} else if tlsConfig.ServerName == "" {
				tlsConfig = tlsConfig.Clone()
				tlsConfig.ServerName = u.Hostname()
			}
			rawConn, err = tls.DialWithDialer(dialer, "tcp", host, tlsConfig)
		} else {
			rawConn, err = dialer.DialContext(ctx, "tcp", host)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("modelexec: connecting to %s: %w", host, err)
	}

	// Resolve IAP / Cloud Run authentication token
	token, err := d.resolveToken(ctx, rawURL)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("modelexec: resolving auth token: %w", err)
	}

	path := u.Path
	if path == "" || path == "/" {
		path = "/_grain/executor"
	}
	if u.RawQuery != "" {
		if !strings.Contains(path, "?") {
			path += "?" + u.RawQuery
		} else {
			path += "&" + u.RawQuery
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", path, nil)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("modelexec: creating upgrade request: %w", err)
	}
	req.Host = u.Host
	wsupgrade.SetRequestHeaders(req.Header)
	req.Header.Set("User-Agent", "grain-executor")

	if token != "" {
		token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if d.Token != "" {
		req.Header.Set("X-Grain-Executor-Token", d.Token)
		req.Header.Set("X-Grain-Tunnel-Token", d.Token)
		if token == "" {
			req.Header.Set("Authorization", "Bearer "+d.Token)
		}
	}

	if err := req.Write(rawConn); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("modelexec: writing upgrade request: %w", err)
	}

	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("modelexec: reading upgrade response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		_ = rawConn.Close()
		return nil, fmt.Errorf("modelexec: server rejected upgrade with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return wrapConnWithReader(rawConn, br), nil
}

func (d *Daemon) resolveToken(ctx context.Context, targetURL string) (string, error) {
	if d.IAPTokenProvider != nil {
		return d.IAPTokenProvider.Token(ctx)
	}
	if d.TokenProvider != nil {
		return d.TokenProvider(ctx)
	}
	if aud := d.iapAudience(); aud != "" {
		provider := NewIAPTokenProvider(aud)
		d.IAPTokenProvider = provider
		return provider.Token(ctx)
	}
	for _, env := range []string{"GRAIN_IAP_TOKEN", "GRAIN_ID_TOKEN", "GRAIN_AUTH_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(env)); val != "" {
			return val, nil
		}
	}

	if d.shouldAttemptCloudRunAuth(targetURL) {
		return d.getGoogleIDToken(ctx, targetURL)
	}
	return "", nil
}

func (d *Daemon) shouldAttemptCloudRunAuth(targetURL string) bool {
	if d.iapAudience() != "" {
		return true
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	if u.Scheme == "https" || strings.Contains(u.Host, ".run.app") {
		return true
	}
	return false
}

func (d *Daemon) getGoogleIDToken(ctx context.Context, targetURL string) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()

	if d.cachedToken != "" && time.Now().Before(d.cachedExpiry) {
		return d.cachedToken, nil
	}

	audience := d.iapAudience()
	if audience == "" {
		if u, err := url.Parse(targetURL); err == nil && u.Scheme != "" && u.Host != "" {
			audience = fmt.Sprintf("%s://%s", u.Scheme, u.Host)
		} else {
			audience = targetURL
		}
	}

	// 1. Try Google Application Default Credentials via idtoken.NewTokenSource
	ts, err := idtoken.NewTokenSource(ctx, audience)
	if err == nil && ts != nil {
		tok, err := ts.Token()
		if err == nil && tok != nil && tok.AccessToken != "" {
			d.cachedToken = tok.AccessToken
			if !tok.Expiry.IsZero() {
				d.cachedExpiry = tok.Expiry.Add(-time.Minute)
			} else {
				d.cachedExpiry = time.Now().Add(50 * time.Minute)
			}
			return d.cachedToken, nil
		}
	}

	// 2. Try gcloud auth print-identity-token
	if gcloudPath, err := exec.LookPath("gcloud"); err == nil {
		cmd := exec.CommandContext(ctx, gcloudPath, "auth", "print-identity-token", "--audiences="+audience)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			tok := strings.TrimSpace(out.String())
			if tok != "" {
				d.cachedToken = tok
				d.cachedExpiry = time.Now().Add(30 * time.Minute)
				return d.cachedToken, nil
			}
		}
	}

	return "", nil
}

type readerConn struct {
	net.Conn
	r io.Reader
}

func (c *readerConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

func wrapConnWithReader(conn net.Conn, br *bufio.Reader) net.Conn {
	if br == nil || br.Buffered() == 0 {
		return conn
	}
	return &readerConn{
		Conn: conn,
		r:    io.MultiReader(br, conn),
	}
}

func (d *Daemon) minBackoff() time.Duration {
	if d.MinBackoff > 0 {
		return d.MinBackoff
	}
	return 1 * time.Second
}

func (d *Daemon) maxBackoff() time.Duration {
	if d.MaxBackoff > 0 {
		return d.MaxBackoff
	}
	return 30 * time.Second
}

func generateInstanceID(hostName string) string {
	var b [8]byte
	if _, err := crand.Read(b[:]); err == nil {
		return fmt.Sprintf("%s-%x", hostName, b)
	}
	return fmt.Sprintf("%s-%d", hostName, time.Now().UnixNano())
}

func (d *Daemon) pingInterval() time.Duration {
	if d.PingInterval > 0 {
		return d.PingInterval
	}
	return pingInterval
}

func (d *Daemon) idleTimeout() time.Duration {
	if d.IdleTimeout > 0 {
		return d.IdleTimeout
	}
	return idleTimeout
}

// Run runs the daemon loop: it starts an MCP tunnel listener on a local socket,
// connects to the Grain daemon, and serves execution requests until ctx is done.
func (d *Daemon) Run(ctx context.Context) error {
	hostName := d.Name
	if hostName == "" {
		if h, err := os.Hostname(); err == nil {
			hostName = h
		} else {
			hostName = "remote-host"
		}
	}
	if strings.TrimSpace(d.InstanceID) == "" {
		d.InstanceID = generateInstanceID(hostName)
	}

	// Create a temporary directory for local unix domain sockets.
	tmpDir, err := os.MkdirTemp("", "grain-executor-*")
	if err != nil {
		return fmt.Errorf("modelexec: creating socket dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "mcp.sock")
	sockLn, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("modelexec: listening on unix socket: %w", err)
	}
	defer sockLn.Close()

	binaries, err := scopeExecutorSock(tmpDir, sockPath, d.Binaries)
	if err != nil {
		return err
	}

	backoff := d.minBackoff()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := d.runOnce(ctx, hostName, sockLn, binaries)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			d.logf("modelexec: connection error: %v; reconnecting in %s", err, backoff)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff = time.Duration(float64(backoff) * (1.5 + rand.Float64()*0.5))
			if backoff > d.maxBackoff() {
				backoff = d.maxBackoff()
			}
		}
	}
}

func (d *Daemon) runOnce(ctx context.Context, hostName string, sockLn net.Listener, binaries map[string]string) error {
	rawConn, err := d.dial(ctx)
	if err != nil {
		return err
	}
	defer rawConn.Close()

	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = rawConn.Close()
		case <-stopWatcher:
		}
	}()

	// Initialize stream multiplexer over transport connection.
	m := mux.NewMux(rawConn, mux.Config{
		IsServer:     false,
		PingInterval: d.pingInterval(),
		PingTimeout:  15 * time.Second,
	})
	defer m.Close()

	// Start local Git proxy HTTP listener on 127.0.0.1:<port>
	gitProxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("modelexec: listening on local git proxy port: %w", err)
	}
	defer gitProxyLn.Close()

	gitProxyAddr := gitProxyLn.Addr().(*net.TCPAddr)
	gitProxyURL := fmt.Sprintf("http://127.0.0.1:%d", gitProxyAddr.Port)

	// And one for the capability MCP servers grain carries to the CLIs it
	// runs here (capabilitymcp.go): a CLI is handed a URL on it, and
	// whatever arrives is carried to grain, which serves it.
	capabilityMCPLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("modelexec: listening on local capability MCP port: %w", err)
	}
	defer capabilityMCPLn.Close()
	capabilityMCPURL := fmt.Sprintf("http://127.0.0.1:%d", capabilityMCPLn.Addr().(*net.TCPAddr).Port)

	// Open control stream.
	ctrlStream, err := m.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("opening control stream: %w", err)
	}
	defer ctrlStream.Close()

	instanceID := strings.TrimSpace(d.InstanceID)
	if instanceID == "" {
		instanceID = generateInstanceID(hostName)
	}

	codec := newCodec(ctrlStream)
	hello := frame{
		Type:            frameHello,
		ProtocolVersion: ProtocolVersion,
		Token:           d.Token,
		Host:            hostName,
		InstanceID:      instanceID,
		Binaries:        binaries,
		ServerURL:       d.ServerURL,
		CodexAuthFile:   d.CodexAuthFile,
		GitProxyURL:     gitProxyURL,
		Concurrency:     d.Concurrency,

		CapabilityMCPURL: capabilityMCPURL,
	}
	if err := codec.write(hello); err != nil {
		return fmt.Errorf("handshake write: %w", err)
	}

	welcome, err := codec.read(writeTimeout)
	if err != nil {
		return fmt.Errorf("handshake read: %w", err)
	}
	if welcome.Type != frameWelcome {
		return fmt.Errorf("handshake expected welcome frame, got %q", welcome.Type)
	}
	if welcome.Error != "" {
		return fmt.Errorf("handshake rejected: %s", welcome.Error)
	}

	d.logf("modelexec: connected to %s as %s", d.Addr, hostName)

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()

	// Start bridging incoming local MCP connections to the multiplexer.
	go d.serveMCPBridge(sessionCtx, sockLn, m)

	// Start bridging incoming local Git proxy connections to the multiplexer.
	go d.serveTunnelBridge(sessionCtx, gitProxyLn, m, StreamTypeGitProxy)

	// And capability MCP connections.
	go d.serveTunnelBridge(sessionCtx, capabilityMCPLn, m, StreamTypeCapabilityMCP)

	execs := &execTracker{cancels: make(map[uint64]context.CancelFunc)}

	pingTicker := time.NewTicker(d.pingInterval())
	defer pingTicker.Stop()
	go func() {
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-pingTicker.C:
				if err := codec.write(frame{Type: framePing}); err != nil {
					return
				}
			}
		}
	}()

	for {
		f, err := codec.read(d.idleTimeout())
		if err != nil {
			return err
		}
		switch f.Type {
		case framePing:
			_ = codec.write(frame{Type: framePong})
		case framePong:
			// keepalive ack
		case frameCancel:
			execs.cancel(f.ID)
		default:
			go d.handleRequest(sessionCtx, codec, f, execs)
		}
	}
}

// serveMCPBridge accepts local unix socket connections from child `grain mcpserver`
// processes and forwards them over fresh streams on the multiplexer to Grain.
func (d *Daemon) serveMCPBridge(ctx context.Context, ln net.Listener, m *mux.Mux) {
	if ln == nil {
		return
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			return
		}
		go d.handleMCPConn(ctx, conn, m)
	}
}

func (d *Daemon) handleMCPConn(ctx context.Context, localConn net.Conn, m *mux.Mux) {
	defer localConn.Close()

	// Read args from local connection.
	dec := json.NewDecoder(localConn)
	var args []string
	if err := dec.Decode(&args); err != nil {
		d.logf("modelexec: reading MCP args from child: %v", err)
		return
	}

	// Open a new stream on the multiplexer to Grain.
	stream, err := m.OpenStream(ctx)
	if err != nil {
		d.logf("modelexec: opening MCP tunnel stream: %v", err)
		return
	}
	defer stream.Close()

	// Send StreamHeader to Grain.
	enc := json.NewEncoder(stream)
	if err := enc.Encode(StreamHeader{Type: StreamTypeMCP, Args: args}); err != nil {
		d.logf("modelexec: sending MCP stream header: %v", err)
		return
	}

	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-ctx.Done():
			_ = localConn.Close()
			_ = stream.Close()
		case <-doneCh:
		}
	}()

	// Bidirectional copy between localConn and stream with graceful half-close coordination.
	localReader := io.MultiReader(dec.Buffered(), localConn)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(stream, localReader)
		_ = stream.CloseWrite()
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(localConn, stream)
		if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = localConn.Close()
		}
		_ = localConn.Close()
	}()

	wg.Wait()
}

// serveTunnelBridge accepts local TCP connections -- from git commands
// (StreamTypeGitProxy) or from a CLI's capability MCP client
// (StreamTypeCapabilityMCP) -- and forwards each over a fresh stream of
// streamType on the multiplexer to Grain.
func (d *Daemon) serveTunnelBridge(ctx context.Context, ln net.Listener, m *mux.Mux, streamType string) {
	if ln == nil {
		return
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			return
		}
		go d.handleTunnelConn(ctx, conn, m, streamType)
	}
}

func (d *Daemon) handleTunnelConn(ctx context.Context, localConn net.Conn, m *mux.Mux, streamType string) {
	defer localConn.Close()

	// Open a new stream on the multiplexer to Grain.
	stream, err := m.OpenStream(ctx)
	if err != nil {
		d.logf("modelexec: opening %s tunnel stream: %v", streamType, err)
		return
	}
	defer stream.Close()

	// Send StreamHeader to Grain.
	enc := json.NewEncoder(stream)
	if err := enc.Encode(StreamHeader{Type: streamType}); err != nil {
		d.logf("modelexec: sending %s stream header: %v", streamType, err)
		return
	}

	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-ctx.Done():
			_ = localConn.Close()
			_ = stream.Close()
		case <-doneCh:
		}
	}()

	// Bidirectional copy between localConn and stream with graceful half-close coordination.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(stream, localConn)
		_ = stream.CloseWrite()
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(localConn, stream)
		if cw, ok := localConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = localConn.Close()
		}
		_ = localConn.Close()
	}()

	wg.Wait()
}

type execTracker struct {
	mu      sync.Mutex
	cancels map[uint64]context.CancelFunc
}

func (t *execTracker) add(id uint64, cancel context.CancelFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancels[id] = cancel
}

func (t *execTracker) remove(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.cancels, id)
}

func (t *execTracker) cancel(id uint64) {
	t.mu.Lock()
	cancel, ok := t.cancels[id]
	t.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func expandHome(path string) string {
	if path == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	} else if strings.HasPrefix(path, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, path[2:])
		}
	}
	return path
}

func (d *Daemon) handleRequest(ctx context.Context, codec *codec, f frame, execs *execTracker) {
	switch f.Type {
	case frameMkdirTemp:
		dir, err := os.MkdirTemp("", f.Pattern)
		if err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		_ = codec.write(frame{Type: frameResult, ID: f.ID, Path: dir})

	case frameMkdirAll:
		if err := os.MkdirAll(expandHome(f.Path), fs.FileMode(f.Mode)); err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		_ = codec.write(frame{Type: frameResult, ID: f.ID})

	case frameWriteFile:
		if err := os.WriteFile(expandHome(f.Path), f.Data, fs.FileMode(f.Mode)); err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		_ = codec.write(frame{Type: frameResult, ID: f.ID})

	case frameCreate:
		file, err := os.CreateTemp(expandHome(f.Dir), f.Pattern)
		if err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		name := file.Name()
		_ = file.Close()
		_ = codec.write(frame{Type: frameResult, ID: f.ID, Path: name})

	case frameSymlink:
		if err := os.Symlink(expandHome(f.Target), expandHome(f.Path)); err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		_ = codec.write(frame{Type: frameResult, ID: f.ID})

	case frameReadTail:
		data, truncated, err := readTail(expandHome(f.Path), f.Limit)
		if err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		_ = codec.write(frame{Type: frameResult, ID: f.ID, Data: data, Truncated: truncated})

	case frameRemoveAll:
		if err := os.RemoveAll(expandHome(f.Path)); err != nil {
			_ = codec.write(errorf(f.ID, "%v", err))
			return
		}
		_ = codec.write(frame{Type: frameResult, ID: f.ID})

	case frameExec:
		d.exec(ctx, codec, f, execs)
	}
}

// textBusyRetries and textBusyBackoff bound how long exec waits out an
// ETXTBSY on start: a dozen tries 25ms apart, far longer than the
// fork-to-exec window that causes it.
const (
	textBusyRetries = 12
	textBusyBackoff = 25 * time.Millisecond
)

func (d *Daemon) exec(sessionCtx context.Context, codec *codec, f frame, execs *execTracker) {
	cmdCtx, cancelCmd := context.WithCancel(sessionCtx)
	defer cancelCmd()

	execs.add(f.ID, cancelCmd)
	defer execs.remove(f.ID)

	cmd := exec.CommandContext(cmdCtx, expandHome(f.Path), f.Args...)
	procgroup.Prepare(cmd)

	var cleanupDir func()
	if f.Dir != "" {
		dir := expandHome(f.Dir)
		_ = os.MkdirAll(dir, 0o700)
		cmd.Dir = dir
	} else {
		agentDir, err := os.MkdirTemp("", "grain-agent-*")
		if err != nil {
			_ = codec.write(errorf(f.ID, "creating agent folder: %v", err))
			return
		}
		cmd.Dir = agentDir
		cleanupDir = func() { _ = os.RemoveAll(agentDir) }
	}
	if cleanupDir != nil {
		defer cleanupDir()
	}

	var env []string
	for _, e := range f.Env {
		if strings.HasPrefix(e, executorSockEnv+"=") {
			continue
		}
		if strings.HasPrefix(e, "HOME=~") {
			if h, err := os.UserHomeDir(); err == nil {
				val := e[5:]
				if val == "~" {
					e = "HOME=" + h
				} else if strings.HasPrefix(val, "~/") {
					e = "HOME=" + filepath.Join(h, val[2:])
				}
			}
		}
		env = append(env, e)
	}
	cmd.Env = append(withoutExecutorSock(os.Environ()), env...)

	if f.Stdin != "" {
		cmd.Stdin = strings.NewReader(f.Stdin)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = codec.write(errorf(f.ID, "stdout pipe: %v", err))
		return
	}

	stderrBuf := &boundedBuffer{limit: stderrLimit}
	cmd.Stderr = stderrBuf

	// A binary written moments ago -- the grain shim scopeExecutorSock
	// leaves in its socket dir -- can briefly refuse to exec with
	// ETXTBSY: a fork elsewhere in this process inherited the write fd
	// that wrote it and has not reached its own exec yet
	// (golang/go#22315). That clears in milliseconds, so it is retried
	// with a fresh Cmd rather than reported as the run failing.
	err = cmd.Start()
	for attempt := 0; err != nil && errors.Is(err, syscall.ETXTBSY) && attempt < textBusyRetries; attempt++ {
		time.Sleep(textBusyBackoff)
		retry := exec.CommandContext(cmdCtx, cmd.Path, f.Args...)
		procgroup.Prepare(retry)
		retry.Dir, retry.Env = cmd.Dir, cmd.Env
		if f.Stdin != "" {
			retry.Stdin = strings.NewReader(f.Stdin)
		}
		if stdoutPipe, err = retry.StdoutPipe(); err != nil {
			break
		}
		retry.Stderr = stderrBuf
		cmd = retry
		err = cmd.Start()
	}
	if err != nil {
		_ = codec.write(errorf(f.ID, "start: %v", err))
		return
	}

	// Stream stdout chunks back over control stream.
	buf := make([]byte, stdoutChunk)
	for {
		n, err := stdoutPipe.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			_ = codec.write(frame{Type: frameStdout, ID: f.ID, Data: chunk})
		}
		if err != nil {
			break
		}
	}

	waitErr := cmd.Wait()
	res := frame{
		Type:   frameResult,
		ID:     f.ID,
		Stderr: stderrBuf.String(),
	}
	if waitErr != nil {
		res.Error = waitErr.Error()
	}
	_ = codec.write(res)
}

// executorSockEnv names the MCP tunnel socket to the one process that is
// meant to dial it: `grain mcpserver`, forked by a run's CLI.
//
// It is deliberately not in the environment of anything this executor
// starts. A run's CLI execs shell commands of the model's choosing --
// antigravity with --dangerously-skip-permissions -- and those inherit the
// CLI's environment, so an ambient variable handed every one of them a
// live tunnel back to the controller (#2369). Instead the socket rides on
// the grain binary itself: scopeExecutorSock advertises a shim in its place
// that sets the variable only for the grain process it execs.
const executorSockEnv = "GRAIN_EXECUTOR_SOCK"

// scopeExecutorSock returns the binaries this executor offers grain, with
// "grain" -- the path every Framework writes into its CLI's MCP config --
// replaced by a shim in dir that execs the real binary with
// executorSockEnv set to sockPath. Every other binary is offered as
// configured. With no grain binary configured there is nothing for the
// socket to be scoped to, and the map is returned unchanged.
//
// This hides the socket from the agent's environment rather than making
// it unreachable: anything running as this user can still list dir. What
// reaching it buys is bounded on the controller's side, which serves a
// tunnel only for an mcpserver argv grain itself issued (mcpgrants.go).
func scopeExecutorSock(dir, sockPath string, binaries map[string]string) (map[string]string, error) {
	grain, ok := binaries["grain"]
	if !ok {
		return binaries, nil
	}
	shim := filepath.Join(dir, "grain")
	script := "#!/bin/sh\n" +
		executorSockEnv + "=" + shellQuote(sockPath) + "\n" +
		"export " + executorSockEnv + "\n" +
		"exec " + shellQuote(grain) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		return nil, fmt.Errorf("modelexec: writing grain shim: %w", err)
	}
	out := make(map[string]string, len(binaries))
	for name, path := range binaries {
		out[name] = path
	}
	out["grain"] = shim
	return out, nil
}

// shellQuote quotes s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// withoutExecutorSock is env with any executorSockEnv entry removed, so an
// executor that was itself started with one does not pass it on.
func withoutExecutorSock(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, executorSockEnv+"=") {
			out = append(out, e)
		}
	}
	return out
}

func readTail(path string, limit int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()
	if limit <= 0 || size <= limit {
		data, err := io.ReadAll(f)
		return data, false, err
	}
	if _, err := f.Seek(size-limit, io.SeekStart); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(f)
	return data, true, err
}

type boundedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() >= b.limit {
		return len(p), nil
	}
	avail := b.limit - b.buf.Len()
	if len(p) > avail {
		p = p[:avail]
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
