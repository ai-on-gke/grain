package sandbox_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/relay"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

const (
	tcpSandboxID    = "sbx-tcp-1"
	tcpSandboxToken = "per-sandbox-token-1"
	controllerToken = "controller-token-1"
)

// startTCPSandbox serves a sandbox on a TLS TCP listener on 127.0.0.1 and
// returns its port.
func startTCPSandbox(t *testing.T, ctx context.Context, pki sandbox.TestPKI) int {
	t.Helper()
	srv := sandbox.NewServer(sandbox.ServerConfig{
		SandboxID:      tcpSandboxID,
		Token:          tcpSandboxToken,
		WrapperVersion: "test-1.0",
		TLSConfig:      &tls.Config{Certificates: []tls.Certificate{pki.ServerCert()}},
		Guest:          guestexec.Config{User: sandbox.CurrentUsername(t)},
		DialGuest:      sandbox.InProcessGuestDialer(ctx),
	})
	ln, err := sandbox.Listen("tcp://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, ln) }()
	return ln.Addr().(*net.TCPAddr).Port
}

// cutProxy forwards TCP connections to target and can sever every
// connection it carries, standing in for a network drop.
type cutProxy struct {
	ln     net.Listener
	mu     sync.Mutex
	conns  []net.Conn
	target string
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{ln: ln, target: target}
	t.Cleanup(func() { ln.Close(); p.cut() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); up.Close() }()
			go func() { _, _ = io.Copy(c, up); c.Close() }()
		}
	}()
	return p
}

func (p *cutProxy) addr() string { return p.ln.Addr().String() }

func (p *cutProxy) cut() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func sandboxTLS(pki sandbox.TestPKI) *tls.Config {
	return &tls.Config{RootCAs: pki.Pool(), ServerName: "localhost"}
}

// TestTCPListen_DirectDial is the in-cluster path: a controller dials a
// sandbox's TLS TCP listener directly, runs the full client suite, and
// resumes an exec on the same Client after the connection is severed.
func TestTCPListen_DirectDial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pki := sandbox.NewTestPKI(t)
	port := startTCPSandbox(t, ctx, pki)
	proxy := newCutProxy(t, fmt.Sprintf("127.0.0.1:%d", port))

	cli, err := sandbox.Dial(ctx, "tls://"+proxy.addr(), sandbox.DialOptions{
		SandboxID: tcpSandboxID,
		Token:     tcpSandboxToken,
		TLSConfig: sandboxTLS(pki),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	sandbox.RunClientSuite(t, ctx, cli, func() *sandbox.Client { return cli }, proxy.cut)
}

// TestTCPListen_Refusals covers the hardening of tcp:// listen mode.
func TestTCPListen_Refusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pki := sandbox.NewTestPKI(t)
	cert := &tls.Config{Certificates: []tls.Certificate{pki.ServerCert()}}

	// A tcp:// listener without a token, or without a certificate, does
	// not start.
	noToken := sandbox.NewServer(sandbox.ServerConfig{SandboxID: "x", TLSConfig: cert})
	if err := noToken.ListenAndServe(ctx, "tcp://127.0.0.1:0"); !errors.Is(err, sandbox.ErrTokenRequired) {
		t.Fatalf("ListenAndServe without token = %v, want ErrTokenRequired", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := noToken.Serve(ctx, ln); !errors.Is(err, sandbox.ErrTokenRequired) {
		t.Fatalf("Serve(tcp) without token = %v, want ErrTokenRequired", err)
	}
	noTLS := sandbox.NewServer(sandbox.ServerConfig{SandboxID: "x", Token: "t"})
	if err := noTLS.ListenAndServe(ctx, "tcp://127.0.0.1:0"); !errors.Is(err, sandbox.ErrTLSRequired) {
		t.Fatalf("ListenAndServe without certificate = %v, want ErrTLSRequired", err)
	}

	// A server with no token handed a TCP connection directly refuses it
	// rather than admitting it unauthenticated.
	l2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	go func() {
		c, err := l2.Accept()
		if err == nil {
			_ = noToken.ServeListenConn(ctx, c)
		}
	}()
	if _, err := sandbox.Dial(ctx, "tcp://"+l2.Addr().String(), sandbox.DialOptions{}); !errors.Is(err, sandbox.ErrAuthFailed) {
		t.Fatalf("Dial to tokenless server over TCP = %v, want ErrAuthFailed", err)
	}

	port := startTCPSandbox(t, ctx, pki)
	addr := fmt.Sprintf("tls://127.0.0.1:%d", port)
	for _, tok := range []string{"", "wrong-token"} {
		if _, err := sandbox.Dial(ctx, addr, sandbox.DialOptions{Token: tok, TLSConfig: sandboxTLS(pki)}); !errors.Is(err, sandbox.ErrAuthFailed) {
			t.Fatalf("Dial with token %q = %v, want ErrAuthFailed", tok, err)
		}
	}
	// Plain TCP to the TLS listener gets nowhere.
	dctx, dcancel := context.WithTimeout(ctx, 3*time.Second)
	defer dcancel()
	if _, err := sandbox.Dial(dctx, fmt.Sprintf("tcp://127.0.0.1:%d", port), sandbox.DialOptions{Token: tcpSandboxToken}); err == nil {
		t.Fatal("plain-TCP Dial to the TLS listener succeeded")
	}
	// A sandbox certificate the controller does not trust is refused.
	if _, err := sandbox.Dial(ctx, addr, sandbox.DialOptions{Token: tcpSandboxToken, TLSConfig: &tls.Config{ServerName: "localhost"}}); err == nil {
		t.Fatal("Dial trusting no CA succeeded")
	}
}

// staticResolver routes known names to 127.0.0.1 and refuses everything
// else, standing in for the Kubernetes pod resolver (tested in pkg/relay).
type staticResolver map[string]string

func (r staticResolver) Resolve(_ context.Context, target string) (string, error) {
	if ip, ok := r[target]; ok {
		return ip, nil
	}
	return "", fmt.Errorf("target %q is outside the relay's selector", target)
}

func startRelay(t *testing.T, ctx context.Context, pki sandbox.TestPKI, sandboxPort int, tlsCfg *tls.Config, token string) (*relay.Server, string) {
	t.Helper()
	if tlsCfg == nil {
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{pki.ServerCert()}}
	}
	r, err := relay.New(relay.Config{
		Token:       token,
		TLSConfig:   tlsCfg,
		Resolver:    staticResolver{"sbx-relay-1": "127.0.0.1"},
		SandboxPort: sandboxPort,
		Logf:        t.Logf,
	})
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = r.Serve(ctx, ln) }()
	return r, ln.Addr().String()
}

// TestRelay_DialRelay is the off-cluster path: the controller holds one
// session to granule relay, reaches the listening sandbox by name, runs the
// full client suite, and reattaches to an in-flight exec after the relay
// drops the controller session and the client reconnects.
func TestRelay_DialRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pki := sandbox.NewTestPKI(t)
	port := startTCPSandbox(t, ctx, pki)
	r, relayAddr := startRelay(t, ctx, pki, port, nil, controllerToken)

	rc, err := sandbox.DialRelay(ctx, relayAddr, sandbox.RelayCredentials{
		Token:     controllerToken,
		TLSConfig: &tls.Config{RootCAs: pki.Pool(), ServerName: "localhost"},
	})
	if err != nil {
		t.Fatalf("DialRelay: %v", err)
	}
	defer rc.Close()

	var conn sandbox.Connector = sandbox.RelayConnector{Relay: rc, Options: func(string) sandbox.DialOptions {
		return sandbox.DialOptions{Token: tcpSandboxToken, TLSConfig: sandboxTLS(pki)}
	}}
	cli, err := conn.Connect(ctx, "sbx-relay-1")
	if err != nil {
		t.Fatalf("Connect through relay: %v", err)
	}
	defer cli.Close()
	if cli.SandboxID() != tcpSandboxID {
		t.Fatalf("SandboxID = %q, want %q", cli.SandboxID(), tcpSandboxID)
	}

	sandbox.RunClientSuite(t, ctx, cli, func() *sandbox.Client { return cli }, r.DropSessions)
}

// TestRelay_Refusals covers controller authentication, per-sandbox tokens
// through the relay, and targets the relay will not route to.
func TestRelay_Refusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pki := sandbox.NewTestPKI(t)
	port := startTCPSandbox(t, ctx, pki)
	relayTLS := &tls.Config{RootCAs: pki.Pool(), ServerName: "localhost"}

	// A relay will not start without some controller authentication.
	if _, err := relay.New(relay.Config{
		TLSConfig:   &tls.Config{Certificates: []tls.Certificate{pki.ServerCert()}},
		Resolver:    staticResolver{},
		SandboxPort: port,
	}); err == nil {
		t.Fatal("relay.New with no token and no mTLS succeeded")
	}

	r, relayAddr := startRelay(t, ctx, pki, port, nil, controllerToken)
	for _, tok := range []string{"", "wrong"} {
		if _, err := sandbox.DialRelay(ctx, relayAddr, sandbox.RelayCredentials{Token: tok, TLSConfig: relayTLS}); !errors.Is(err, sandbox.ErrAuthFailed) {
			t.Fatalf("DialRelay with controller token %q = %v, want ErrAuthFailed", tok, err)
		}
	}
	if r.Refusals() < 2 {
		t.Fatalf("relay refusals = %d, want >= 2", r.Refusals())
	}

	rc, err := sandbox.DialRelay(ctx, relayAddr, sandbox.RelayCredentials{Token: controllerToken, TLSConfig: relayTLS})
	if err != nil {
		t.Fatalf("DialRelay: %v", err)
	}
	defer rc.Close()

	// The relay refuses names its resolver will not route to, including
	// anything outside its namespace or selector.
	for _, target := range []string{"other-pod", "kube-system/kube-dns", ""} {
		if _, err := rc.Client(ctx, target, sandbox.DialOptions{Token: tcpSandboxToken, TLSConfig: sandboxTLS(pki)}); !errors.Is(err, sandbox.ErrTargetRefused) {
			t.Fatalf("Client(%q) = %v, want ErrTargetRefused", target, err)
		}
	}
	// The per-sandbox token is checked end to end by the sandbox itself.
	for _, tok := range []string{"", "wrong-token"} {
		if _, err := rc.Client(ctx, "sbx-relay-1", sandbox.DialOptions{Token: tok, TLSConfig: sandboxTLS(pki)}); !errors.Is(err, sandbox.ErrAuthFailed) {
			t.Fatalf("Client with sandbox token %q = %v, want ErrAuthFailed", tok, err)
		}
	}
}

// TestRelay_MTLS authenticates the controller by client certificate alone.
func TestRelay_MTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pki := sandbox.NewTestPKI(t)
	port := startTCPSandbox(t, ctx, pki)
	_, relayAddr := startRelay(t, ctx, pki, port, &tls.Config{
		Certificates: []tls.Certificate{pki.ServerCert()},
		ClientCAs:    pki.Pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, "")

	if _, err := sandbox.DialRelay(ctx, relayAddr, sandbox.RelayCredentials{
		TLSConfig: &tls.Config{RootCAs: pki.Pool(), ServerName: "localhost"},
	}); err == nil {
		t.Fatal("DialRelay without a client certificate succeeded")
	}

	rc, err := sandbox.DialRelay(ctx, relayAddr, sandbox.RelayCredentials{
		TLSConfig: &tls.Config{
			RootCAs:      pki.Pool(),
			ServerName:   "localhost",
			Certificates: []tls.Certificate{pki.ClientCert(t, "controller")},
		},
	})
	if err != nil {
		t.Fatalf("DialRelay with client certificate: %v", err)
	}
	defer rc.Close()
	cli, err := rc.Client(ctx, "sbx-relay-1", sandbox.DialOptions{Token: tcpSandboxToken, TLSConfig: sandboxTLS(pki)})
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	defer cli.Close()
	var out strings.Builder
	code, err := cli.Exec(ctx, sandbox.ExecRequest{Line: "echo over-mtls"}, nil, &out, io.Discard)
	if err != nil || code != 0 || strings.TrimSpace(out.String()) != "over-mtls" {
		t.Fatalf("Exec = %d, %q, %v", code, out.String(), err)
	}
}
