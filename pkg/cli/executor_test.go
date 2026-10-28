package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/modelexec"
)

func TestExecutorSubcommandIntegration(t *testing.T) {
	t.Parallel()
	token := "test-secret-token-123"
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatalf("writing token file: %v", err)
	}

	var logMu sync.Mutex
	testDone := false
	safeLogf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		if !testDone {
			t.Logf(format, args...)
		}
	}
	defer func() {
		logMu.Lock()
		testDone = true
		logMu.Unlock()
	}()

	server, err := modelexec.NewServer(modelexec.Config{
		Token: token,
		Wait:  5 * time.Second,
		Logf:  safeLogf,
		ServeMCP: func(rwc io.ReadWriteCloser, args []string) error {
			defer rwc.Close()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen error: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Serve(ctx, ln)
	}()

	// Start daemon client pointing to server
	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()

	d := &modelexec.Daemon{
		Addr:  ln.Addr().String(),
		Token: token,

		Binaries: map[string]string{"agy": "/usr/bin/echo", "claude": "/usr/bin/echo", "codex": "/usr/bin/echo", "echo": "/bin/echo"},
		Logf:     safeLogf,
	}

	daemonErrCh := make(chan error, 1)
	go func() {
		daemonErrCh <- d.Run(daemonCtx)
	}()

	// Wait for host to connect
	var host *modelexec.RemoteHost
	deadline := time.Now().Add(daemonWaitLimit)
	for time.Now().Before(deadline) {
		host = server.ConnectedHost("agy")
		if host != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if host == nil {
		t.Fatalf("timed out waiting for remote host to connect")
	}

	backends := server.AgentBackends()
	if len(backends) == 0 {
		t.Fatalf("expected backends, got none")
	}

	// Test executing a command via host
	res, err := host.Run(ctx, agent.Command{
		Path: "echo",
		Args: []string{"hello", "remote", "executor"},
	}, nil)
	if err != nil {
		t.Fatalf("host.Run error: %v", err)
	}
	if strings.TrimSpace(res) != "hello remote executor" {
		t.Fatalf("host.Run stdout = %q, want %q", res, "hello remote executor")
	}
}

func TestStartExecutorServerConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(tokenFile, []byte("my-test-token"), 0600); err != nil {
		t.Fatalf("writing token file: %v", err)
	}

	cfg := daemon.Config{
		ExecutorAddr:      "127.0.0.1:0",
		ExecutorTokenFile: tokenFile,
		ExecutorWait:      2 * time.Second,
	}

	srv, stop, err := daemon.StartExecutorServer(cfg, nil)
	if err != nil {
		t.Fatalf("startExecutorServer error: %v", err)
	}
	defer stop(context.Background())

	if srv == nil {
		t.Fatalf("expected non-nil server")
	}
}

func TestBinaryFlagParsing(t *testing.T) {
	t.Parallel()
	bf := binaryFlag{}

	// Valid flag
	if err := bf.Set("agy=/usr/bin/agy"); err != nil {
		t.Fatalf("Set valid binary flag: %v", err)
	}
	if err := bf.Set("claude=/usr/bin/claude"); err != nil {
		t.Fatalf("Set valid binary flag 2: %v", err)
	}
	if len(bf) != 2 || bf["agy"] != "/usr/bin/agy" || bf["claude"] != "/usr/bin/claude" {
		t.Fatalf("unexpected binaryFlag content: %+v", bf)
	}

	// Duplicate name
	if err := bf.Set("agy=/usr/local/bin/agy"); err == nil {
		t.Fatal("expected error on duplicate binary name, got nil")
	}

	// Malformed formats
	for _, malformed := range []string{"just-a-name", "=path", "name=", "   "} {
		if err := (binaryFlag{}).Set(malformed); err == nil {
			t.Errorf("expected error for malformed flag %q, got nil", malformed)
		}
	}
}

func TestRunExecutorFlagValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token.txt")
	_ = os.WriteFile(tokenFile, []byte("secret-token"), 0600)

	emptyTokenFile := filepath.Join(dir, "empty-token.txt")
	_ = os.WriteFile(emptyTokenFile, []byte("   \n"), 0600)

	validBinaries := binaryFlag{"echo": "/bin/echo"}

	// 1. Missing addr
	if err := runExecutor("", tokenFile, "", "worker", "", "", "", "", validBinaries, 1, false, "", ""); err == nil {
		t.Error("expected error for missing addr, got nil")
	}

	// 2. Missing token file and token
	if err := runExecutor("127.0.0.1:8080", "", "", "worker", "", "", "", "", validBinaries, 1, false, "", ""); err == nil {
		t.Error("expected error for missing token file, got nil")
	}

	// 3. Empty token file and no token string
	if err := runExecutor("127.0.0.1:8080", emptyTokenFile, "", "worker", "", "", "", "", validBinaries, 1, false, "", ""); err == nil {
		t.Error("expected error for empty token file, got nil")
	}

	// 4. Missing binaries
	if err := runExecutor("127.0.0.1:8080", tokenFile, "", "worker", "", "", "", "", binaryFlag{}, 1, false, "", ""); err == nil {
		t.Error("expected error for empty binaries, got nil")
	}

	// 5. Non-existent binary
	badBinaries := binaryFlag{"agy": "/path/does/not/exist/agy"}
	if err := runExecutor("127.0.0.1:8080", tokenFile, "", "worker", "", "", "", "", badBinaries, 1, false, "", ""); err == nil {
		t.Error("expected error for non-existent binary, got nil")
	}

	// 6. Invalid TLS CA file
	if err := runExecutor("127.0.0.1:8080", tokenFile, "", "worker", "", "", "", "", validBinaries, 1, true, "/non/existent/ca.pem", ""); err == nil {
		t.Error("expected error for non-existent CA file with TLS, got nil")
	}

	// 7. Token string works directly without token file
	if err := runExecutor("", "", "direct-token-string", "worker", "", "", "", "", validBinaries, 1, false, "", ""); err == nil {
		// Should still fail missing addr, but not token
		t.Error("expected error for missing addr, got nil")
	}
}

func TestExecutorClientTLSConfig(t *testing.T) {
	t.Parallel()

	// 1. Without CA file (system roots)
	cfg, err := executorClientTLS("", "server.domain.com")
	if err != nil {
		t.Fatalf("executorClientTLS without CA: %v", err)
	}
	if cfg.ServerName != "server.domain.com" || cfg.RootCAs != nil {
		t.Errorf("unexpected TLS config: %+v", cfg)
	}

	// 2. With valid CA PEM
	dir := t.TempDir()
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Custom CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caBytes, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPriv.PublicKey, caPriv)
	caFile := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes}), 0644)

	cfg2, err := executorClientTLS(caFile, "custom.local")
	if err != nil {
		t.Fatalf("executorClientTLS with CA: %v", err)
	}
	if cfg2.ServerName != "custom.local" || cfg2.RootCAs == nil {
		t.Errorf("unexpected TLS config with CA: %+v", cfg2)
	}

	// 3. With non-PEM file
	badCAFile := filepath.Join(dir, "bad-ca.pem")
	_ = os.WriteFile(badCAFile, []byte("not a pem certificate"), 0644)
	if _, err := executorClientTLS(badCAFile, "custom.local"); err == nil {
		t.Error("expected error on non-PEM CA file, got nil")
	}
}
