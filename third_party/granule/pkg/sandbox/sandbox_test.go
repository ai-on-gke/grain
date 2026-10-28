package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/guestexec"
)

func currentUsername(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	return u.Username
}

// inProcessGuestDialer returns a DialGuest function that runs the production
// pkg/agent.Serve over a net.Pipe() for each vsock connection.
func inProcessGuestDialer(ctx context.Context) func(context.Context) (net.Conn, error) {
	return func(dialCtx context.Context) (net.Conn, error) {
		if err := dialCtx.Err(); err != nil {
			return nil, err
		}
		c1, c2 := net.Pipe()
		go func() {
			defer c2.Close()
			_ = agent.Serve(ctx, c2)
		}()
		return c1, nil
	}
}

type pkiBundle struct {
	caCert     *x509.Certificate
	caKey      *ecdsa.PrivateKey
	caPool     *x509.CertPool
	caPEM      []byte
	serverCert tls.Certificate
}

func newTestPKI(t *testing.T) *pkiBundle {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "granule-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "controller.local"},
		DNSNames:     []string{"localhost", "controller.local"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return &pkiBundle{
		caCert: caCert,
		caKey:  caKey,
		caPool: pool,
		caPEM:  caPEM,
		serverCert: tls.Certificate{
			Certificate: [][]byte{srvDER},
			PrivateKey:  srvKey,
		},
	}
}

func (p *pkiBundle) issueClientCert(t *testing.T, commonName string) tls.Certificate {
	t.Helper()
	cliKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cliTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     []string{commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cliDER, err := x509.CreateCertificate(rand.Reader, cliTmpl, p.caCert, &cliKey.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{
		Certificate: [][]byte{cliDER},
		PrivateKey:  cliKey,
	}
}

func TestFraming_ExitCodeDistinguishableFromTransportCut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Custom guest dialer: when line contains "CUT", it sends stdout and
	// then abruptly closes the vsock connection without sending TypeExit.
	dialer := func(dialCtx context.Context) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go func() {
			defer c2.Close()
			br := bufio.NewReader(c2)
			req, err := execwire.ReadRequest(br)
			if err != nil {
				return
			}
			_ = execwire.WriteResponse(c2, execwire.Response{OK: true, Features: execwire.Features})
			if strings.Contains(req.Line, "CUT") {
				_ = execwire.WriteFrame(c2, execwire.TypeStdout, []byte("partial output before crash\n"))
				// Close c2 without writing TypeExit!
				return
			}
			_ = execwire.WriteFrame(c2, execwire.TypeStdout, []byte("clean output\n"))
			_ = execwire.WriteFrame(c2, execwire.TypeExit, execwire.EncodeExit(125))
		}()
		return c1, nil
	}

	srv := NewServer(ServerConfig{
		SandboxID: "sbx-framing",
		Guest:     guestexec.Config{User: currentUsername(t)},
		DialGuest: dialer,
	})

	sockDir, err := os.MkdirTemp("", "sbx-framing")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "access.sock")

	go func() {
		_ = srv.ListenAndServe(ctx, "unix:"+sockPath)
	}()

	var cli *Client
	for range 50 {
		cli, err = Dial(ctx, "unix:"+sockPath, DialOptions{})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	// 1. Real exit status 125 must return code == 125 and err == nil.
	var out bytes.Buffer
	code, err := cli.Exec(ctx, ExecRequest{Line: "exit 125"}, nil, &out, nil)
	if err != nil {
		t.Fatalf("Exec(exit 125) unexpected error: %v", err)
	}
	if code != 125 {
		t.Fatalf("Exec(exit 125) code = %d, want 125", code)
	}
	if got := out.String(); got != "clean output\n" {
		t.Fatalf("stdout = %q, want %q", got, "clean output\n")
	}

	// 2. Abrupt guest connection drop without TypeExit must return ErrTransportCut.
	out.Reset()
	_, err = cli.Exec(ctx, ExecRequest{Line: "CUT"}, nil, &out, nil)
	if !errors.Is(err, ErrTransportCut) {
		t.Fatalf("Exec(CUT) error = %v, want errors.Is(..., ErrTransportCut)", err)
	}
}

func TestListenMode_ExecFilesSignalAndReattach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srv := NewServer(ServerConfig{
		SandboxID:      "sbx-listen-1",
		Token:          "secret-listen-token",
		WrapperVersion: "test-1.0",
		Guest:          guestexec.Config{User: currentUsername(t)},
		DialGuest:      inProcessGuestDialer(ctx),
	})

	sockDir, err := os.MkdirTemp("", "sbx-listen")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "access.sock")

	go func() {
		_ = srv.ListenAndServe(ctx, "unix:"+sockPath)
	}()

	var cli *Client
	for range 50 {
		cli, err = Dial(ctx, "unix:"+sockPath, DialOptions{
			SandboxID: "sbx-listen-1",
			Token:     "secret-listen-token",
		})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	// Wrong token in listen mode must fail with ErrAuthFailed.
	if _, badErr := Dial(ctx, "unix:"+sockPath, DialOptions{Token: "wrong-token"}); !errors.Is(badErr, ErrAuthFailed) {
		t.Fatalf("Dial with wrong token = %v, want ErrAuthFailed", badErr)
	}

	runClientSuite(t, ctx, cli, func() *Client {
		c, derr := Dial(ctx, "unix:"+sockPath, DialOptions{
			SandboxID: "sbx-listen-1",
			Token:     "secret-listen-token",
		})
		if derr != nil {
			t.Fatalf("re-dial listen server: %v", derr)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}, nil)
}

func TestDialOutMode_ExecFilesSignalAndReconnectMidCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pki := newTestPKI(t)
	acceptor, err := ListenTLS("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
	})
	if err != nil {
		t.Fatalf("ListenTLS: %v", err)
	}
	defer acceptor.Close()
	go func() {
		_ = acceptor.Serve(ctx)
	}()

	token, err := acceptor.Issue("sbx-dialout-1", 5*time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	srv := NewServer(ServerConfig{
		SandboxID:      "sbx-dialout-1",
		Token:          token,
		WrapperVersion: "test-1.0",
		Guest:          guestexec.Config{User: currentUsername(t)},
		DialGuest:      inProcessGuestDialer(ctx),
	})

	go func() {
		_ = srv.DialOut(ctx, "tls://"+acceptor.Addr().String(), DialOutOptions{
			TLSConfig:  &tls.Config{RootCAs: pki.caPool, ServerName: "controller.local"},
			MinBackoff: 20 * time.Millisecond,
			MaxBackoff: 100 * time.Millisecond,
		})
	}()

	cli, err := acceptor.WaitClient(ctx, "sbx-dialout-1")
	if err != nil {
		t.Fatalf("WaitClient: %v", err)
	}

	runClientSuite(t, ctx, cli, func() *Client {
		return cli
	}, func() {
		if err := acceptor.DropActiveSession("sbx-dialout-1"); err != nil {
			t.Fatalf("DropActiveSession: %v", err)
		}
	})
}

func runClientSuite(
	t *testing.T,
	ctx context.Context,
	cli *Client,
	reconnectClient func() *Client,
	dropTransport func(),
) {
	t.Helper()

	// 1. Health check.
	health, err := cli.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Ready {
		t.Fatalf("Health.Ready = false, error = %s", health.Error)
	}
	if health.WrapperVer != "test-1.0" {
		t.Fatalf("Health.WrapperVer = %q, want test-1.0", health.WrapperVer)
	}

	// 2. Exec with argv, env, workdir, stdin, stdout, stderr, and non-zero exit status.
	workDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code, err := cli.Exec(ctx, ExecRequest{
		Argv:    []string{"sh", "-c", `cat; echo "env=$FOO cwd=$(pwd)"; echo "err-line" >&2; exit 23`},
		Workdir: workDir,
		Env:     []string{"FOO=bar-baz"},
	}, strings.NewReader("hello-stdin\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 23 {
		t.Fatalf("Exec exit code = %d, want 23", code)
	}
	wantOut := fmt.Sprintf("hello-stdin\nenv=bar-baz cwd=%s\n", workDir)
	if stdout.String() != wantOut {
		t.Fatalf("Exec stdout = %q, want %q", stdout.String(), wantOut)
	}
	if stderr.String() != "err-line\n" {
		t.Fatalf("Exec stderr = %q, want %q", stderr.String(), "err-line\n")
	}

	// 3. Batched PutFiles (nested dirs, modes, atomic replacement) + GetFile.
	fileA := filepath.Join(workDir, "nested/bin/run.sh")
	fileB := filepath.Join(workDir, "nested/cfg/config.json")
	if err := os.MkdirAll(filepath.Dir(fileA), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-populate fileA to verify atomic replacement overwrites it cleanly.
	if err := os.WriteFile(fileA, []byte("old-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = cli.PutFiles(ctx, []FileUpload{
		{
			Path:    fileA,
			Content: []byte("#!/bin/sh\necho 'from-put-files'\n"),
			Mode:    0o755,
		},
		{
			Path:    fileB,
			Content: []byte(`{"enabled":true}`),
			Mode:    0o640,
		},
	})
	if err != nil {
		t.Fatalf("PutFiles: %v", err)
	}

	gotA, modeA, err := cli.GetFile(ctx, fileA)
	if err != nil {
		t.Fatalf("GetFile(fileA): %v", err)
	}
	if string(gotA) != "#!/bin/sh\necho 'from-put-files'\n" {
		t.Fatalf("GetFile(fileA) content = %q", string(gotA))
	}
	if modeA.Perm() != 0o755 {
		t.Fatalf("GetFile(fileA) mode = %04o, want 0755", modeA.Perm())
	}

	gotB, modeB, err := cli.GetFile(ctx, fileB)
	if err != nil {
		t.Fatalf("GetFile(fileB): %v", err)
	}
	if string(gotB) != `{"enabled":true}` {
		t.Fatalf("GetFile(fileB) content = %q", string(gotB))
	}
	if modeB.Perm() != 0o640 {
		t.Fatalf("GetFile(fileB) mode = %04o, want 0640", modeB.Perm())
	}

	// Ensure no temporary .granule-put-* files remain.
	entries, err := os.ReadDir(filepath.Dir(fileA))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".granule-put-") {
			t.Fatalf("leftover temporary file %s", e.Name())
		}
	}

	// 3b. PutFiles and GetFile with spaces, single quotes, and shell metacharacters
	// must not break trap quoting or allow command injection.
	injectedMarker := filepath.Join(workDir, "pwned")
	trickyFile := filepath.Join(workDir, "dir with 'quote' and $(id)", "file 'a' EXIT; touch "+injectedMarker+"; trap true.txt")
	if err := cli.PutFiles(ctx, []FileUpload{
		{
			Path:    trickyFile,
			Content: []byte("safe-content\n"),
			Mode:    0o600,
		},
	}); err != nil {
		t.Fatalf("PutFiles(trickyFile): %v", err)
	}
	if _, statErr := os.Stat(injectedMarker); !os.IsNotExist(statErr) {
		t.Fatalf("command injection marker %s was created!", injectedMarker)
	}
	gotTricky, modeTricky, err := cli.GetFile(ctx, trickyFile)
	if err != nil {
		t.Fatalf("GetFile(trickyFile): %v", err)
	}
	if string(gotTricky) != "safe-content\n" || modeTricky.Perm() != 0o600 {
		t.Fatalf("GetFile(trickyFile) = %q (%04o), want safe-content (0600)", string(gotTricky), modeTricky.Perm())
	}

	// Refusing to overwrite an existing directory whose name contains a single quote
	// must return a clean error without shell syntax errors.
	existingDirWithQuote := filepath.Join(workDir, "dir 'existing'")
	if err := os.MkdirAll(existingDirWithQuote, 0o755); err != nil {
		t.Fatal(err)
	}
	dirErr := cli.PutFiles(ctx, []FileUpload{
		{
			Path:    existingDirWithQuote,
			Content: []byte("nope"),
			Mode:    0o644,
		},
	})
	if dirErr == nil || !strings.Contains(dirErr.Error(), "is a directory") {
		t.Fatalf("PutFiles onto directory with quote = %v, want error containing 'is a directory'", dirErr)
	}

	// 4. Reconnect / Detach in the middle of a running command + Reattach.
	gateFile := filepath.Join(workDir, "gate")
	proc, err := cli.StartExec(ctx, ExecRequest{
		Line: fmt.Sprintf(
			`echo "phase-1"; while [ ! -f %s ]; do sleep 0.02; done; echo "phase-2"; exit 42`,
			shellQuote(gateFile),
		),
	})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	_ = proc.Stdin().Close()

	// Wait until "phase-1\n" arrives on stdout, then cut the connection!
	var combinedOut syncBuffer
	firstLineReady := make(chan struct{})
	var once sync.Once
	writer := &notifyWriter{
		onWrite: func(p []byte) {
			combinedOut.Write(p)
			if strings.Contains(combinedOut.String(), "phase-1\n") {
				once.Do(func() { close(firstLineReady) })
			}
		},
	}

	waitDone := make(chan struct {
		code int
		n    int64
		err  error
	}, 1)
	go func() {
		c, n, werr := proc.Wait(writer, io.Discard)
		waitDone <- struct {
			code int
			n    int64
			err  error
		}{c, n, werr}
	}()

	select {
	case <-firstLineReady:
	case <-ctx.Done():
		t.Fatal("timed out waiting for phase-1 output")
	}

	if dropTransport != nil {
		dropTransport()
	} else {
		_ = proc.Detach()
	}

	firstRes := <-waitDone
	if !errors.Is(firstRes.err, ErrTransportCut) {
		t.Fatalf("Wait after transport drop returned err = %v, want ErrTransportCut", firstRes.err)
	}
	if firstRes.n != int64(len("phase-1\n")) {
		t.Fatalf("bytesRead before cut = %d, want %d", firstRes.n, len("phase-1\n"))
	}

	// Release the command while detached so it emits "phase-2\n" and exits 42
	// with no client attached.
	if err := os.WriteFile(gateFile, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	cli2 := reconnectClient()
	reattachCode, err := cli2.Reattach(ctx, proc.ID(), firstRes.n, writer, io.Discard)
	if err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if reattachCode != 42 {
		t.Fatalf("Reattach exit code = %d, want 42", reattachCode)
	}
	if got, want := combinedOut.String(), "phase-1\nphase-2\n"; got != want {
		t.Fatalf("combined output across reconnect = %q, want %q", got, want)
	}

	// 5. Signal and Kill on a running command.
	sigProc, err := cli2.StartExec(ctx, ExecRequest{
		Line: "trap 'echo got-term; exit 143' TERM; while true; do sleep 0.05; done",
	})
	if err != nil {
		t.Fatalf("StartExec(trap): %v", err)
	}
	_ = sigProc.Stdin().Close()
	time.Sleep(100 * time.Millisecond)
	if err := sigProc.Signal(ctx, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}
	var sigOut bytes.Buffer
	sigCode, _, err := sigProc.Wait(&sigOut, io.Discard)
	if err != nil {
		t.Fatalf("Wait after Signal: %v", err)
	}
	if sigCode != 143 || !strings.Contains(sigOut.String(), "got-term") {
		t.Fatalf("Wait after Signal = code %d, out %q; want 143 and got-term", sigCode, sigOut.String())
	}

	killProc, err := cli2.StartExec(ctx, ExecRequest{
		Line: "sleep 60",
	})
	if err != nil {
		t.Fatalf("StartExec(sleep): %v", err)
	}
	_ = killProc.Stdin().Close()
	if err := killProc.Kill(ctx); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	killCode, _, err := killProc.Wait(io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Wait after Kill: %v", err)
	}
	if killCode == 0 {
		t.Fatalf("Wait after Kill returned exit code 0, want non-zero")
	}
}

func TestAcceptor_AuthRefusalsAndMTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pki := newTestPKI(t)
	acceptor, err := ListenTLS("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pki.caPool,
	})
	if err != nil {
		t.Fatalf("ListenTLS: %v", err)
	}
	defer acceptor.Close()
	go func() {
		_ = acceptor.Serve(ctx)
	}()

	tokenA, err := acceptor.Issue("sandbox-a", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = acceptor.Issue("sandbox-b", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := acceptor.RegisterToken("sandbox-expired", "expired-tok", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	tokenRevoked, err := acceptor.Issue("sandbox-revoked", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	acceptor.Revoke("sandbox-revoked")

	tryDialOut := func(sandboxID, token string, clientCert *tls.Certificate) error {
		srv := NewServer(ServerConfig{
			SandboxID: sandboxID,
			Token:     token,
			Guest:     guestexec.Config{User: currentUsername(t)},
			DialGuest: inProcessGuestDialer(ctx),
		})
		tlsCfg := &tls.Config{
			RootCAs:    pki.caPool,
			ServerName: "controller.local",
		}
		if clientCert != nil {
			tlsCfg.Certificates = []tls.Certificate{*clientCert}
		}
		dctx, dcancel := context.WithTimeout(ctx, 2*time.Second)
		defer dcancel()
		return srv.DialOut(dctx, "tls://"+acceptor.Addr().String(), DialOutOptions{
			TLSConfig: tlsCfg,
		})
	}

	// 1. Unknown sandbox ID -> ErrAuthFailed.
	if err := tryDialOut("sandbox-unknown", tokenA, nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("unknown sandbox: got %v, want ErrAuthFailed", err)
	}

	// 2. Wrong token for sandbox-a -> ErrAuthFailed.
	if err := tryDialOut("sandbox-a", "wrong-token", nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("wrong token: got %v, want ErrAuthFailed", err)
	}

	// 3. Token minted for sandbox-a presented by sandbox-b -> ErrAuthFailed.
	if err := tryDialOut("sandbox-b", tokenA, nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("cross-sandbox token: got %v, want ErrAuthFailed", err)
	}

	// 4. Expired token -> ErrAuthFailed.
	if err := tryDialOut("sandbox-expired", "expired-tok", nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("expired token: got %v, want ErrAuthFailed", err)
	}

	// 5. Revoked token -> ErrAuthFailed.
	if err := tryDialOut("sandbox-revoked", tokenRevoked, nil); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("revoked token: got %v, want ErrAuthFailed", err)
	}

	// 6. mTLS client cert for sandbox-b used by sandbox-a -> ErrAuthFailed.
	certB := pki.issueClientCert(t, "sandbox-b")
	if err := tryDialOut("sandbox-a", tokenA, &certB); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("mTLS CN mismatch: got %v, want ErrAuthFailed", err)
	}

	// 7. Matching mTLS client cert for sandbox-a + tokenA -> succeeds!
	certA := pki.issueClientCert(t, "sandbox-a")
	srvA := NewServer(ServerConfig{
		SandboxID: "sandbox-a",
		Token:     tokenA,
		Guest:     guestexec.Config{User: currentUsername(t)},
		DialGuest: inProcessGuestDialer(ctx),
	})
	go func() {
		_ = srvA.DialOut(ctx, "tls://"+acceptor.Addr().String(), DialOutOptions{
			TLSConfig: &tls.Config{
				RootCAs:      pki.caPool,
				ServerName:   "controller.local",
				Certificates: []tls.Certificate{certA},
			},
		})
	}()

	cliA, err := acceptor.WaitClient(ctx, "sandbox-a")
	if err != nil {
		t.Fatalf("WaitClient(sandbox-a) with matching mTLS: %v", err)
	}
	info, err := cliA.Health(ctx)
	if err != nil || !info.Ready {
		t.Fatalf("Health on mTLS sandbox-a: info=%+v, err=%v", info, err)
	}
	if acceptor.Refusals() < 6 {
		t.Fatalf("Acceptor.Refusals() = %d, want >= 6", acceptor.Refusals())
	}
}

type notifyWriter struct {
	onWrite func(p []byte)
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.onWrite(p)
	return len(p), nil
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestExecReplayBufferCapAndRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	srv := NewServer(ServerConfig{
		SandboxID:              "sbx-cap",
		Guest:                  guestexec.Config{User: currentUsername(t)},
		DialGuest:              inProcessGuestDialer(ctx),
		MaxExecBufferBytes:     64,
		CompletedExecRetention: 150 * time.Millisecond,
	})

	sockDir, err := os.MkdirTemp("", "sbx-cap")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "access.sock")

	go func() {
		_ = srv.ListenAndServe(ctx, "unix:"+sockPath)
	}()

	var cli *Client
	for range 50 {
		cli, err = Dial(ctx, "unix:"+sockPath, DialOptions{})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	// Emit several distinct chunks totaling > 64 bytes so old chunks are evicted
	// from the wrapper's replay buffer.
	proc, err := cli.StartExec(ctx, ExecRequest{
		ID:   "exec-bounded",
		Line: `for i in 1 2 3 4 5 6; do printf 'chunk-%d-0123456789\n' "$i"; sleep 0.01; done; exit 7`,
	})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	_ = proc.Stdin().Close()

	var fullOut bytes.Buffer
	code, _, err := proc.Wait(&fullOut, io.Discard)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}

	srv.mu.Lock()
	ex := srv.execs["exec-bounded"]
	srv.mu.Unlock()
	if ex == nil {
		t.Fatal("expected exec-bounded to still be retained immediately after exit")
	}
	ex.mu.Lock()
	bufBytes := ex.bufferedBytes
	baseIdx := ex.baseIdx
	ex.mu.Unlock()
	if bufBytes > 64 && len(ex.chunks) > 1 {
		t.Fatalf("bufferedBytes = %d, want <= 64", bufBytes)
	}
	if baseIdx == 0 {
		t.Fatalf("expected baseIdx > 0 after ring-buffer trimming, got %d", baseIdx)
	}

	// Reattaching immediately after exit should still succeed and return the tail.
	var tailOut bytes.Buffer
	reCode, err := cli.Reattach(ctx, "exec-bounded", 0, &tailOut, io.Discard)
	if err != nil || reCode != 7 {
		t.Fatalf("Reattach = code %d, err %v; want 7, nil", reCode, err)
	}
	if !strings.HasSuffix(fullOut.String(), tailOut.String()) || tailOut.Len() == 0 {
		t.Fatalf("Reattach tail %q is not a non-empty suffix of full output %q", tailOut.String(), fullOut.String())
	}

	// After CompletedExecRetention (150ms) elapses, the completed session must be
	// evicted from s.execs and Reattach must return ErrExecNotFound.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, rerr := cli.Reattach(ctx, "exec-bounded", 0, io.Discard, io.Discard)
		if errors.Is(rerr, ErrExecNotFound) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("completed exec session was not evicted after CompletedExecRetention")
}
