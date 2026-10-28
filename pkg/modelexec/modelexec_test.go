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

package modelexec

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

func testServerAndDaemon(t *testing.T, team string, serveMCP func(io.ReadWriteCloser, []string) error) (*Server, *RemoteHost, context.CancelFunc) {
	t.Helper()
	token := "test-secret-token"

	srv, err := NewServer(Config{
		Token:    token,
		Wait:     5 * time.Second,
		ServeMCP: serveMCP,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()

	binaries := map[string]string{
		"agy":    "/bin/sh",
		"claude": "/bin/sh",
		"grain":  "/bin/sh",
	}

	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "worker-1",

		Binaries: binaries,
	}

	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil {
		cancel()
		t.Fatalf("srv.Host: %v", err)
	}
	if host == nil {
		cancel()
		t.Fatal("srv.Host returned nil host")
	}

	return srv, host, cancel
}

// grainPath is the grain binary host's executor offers: the shim that
// carries the MCP tunnel socket to `grain mcpserver` and to nothing else
// (scopeExecutorSock). A test command standing in for that mcpserver has
// to be started through it to find the socket at all.
func grainPath(t *testing.T, host *RemoteHost) string {
	t.Helper()
	path, err := host.BinaryPath("grain")
	if err != nil {
		t.Fatalf("BinaryPath(grain): %v", err)
	}
	return path
}

func generateTestPKI(t *testing.T, dnsName string) (caPEM, certPEM, keyPEM []byte, serverTLS *tls.Config, clientPool *x509.CertPool) {
	t.Helper()
	// 1. Generate CA
	caPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Modelexec CA"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPriv.PublicKey, caPriv)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes})

	// 2. Generate Server Certificate signed by CA
	serverPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{dnsName, "localhost"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverCertBytes, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverPriv.PublicKey, caPriv)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertBytes})

	serverKeyBytes, err := x509.MarshalECPrivateKey(serverPriv)
	if err != nil {
		t.Fatalf("marshal server private key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyBytes})

	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load X509KeyPair: %v", err)
	}
	serverTLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	}

	clientPool = x509.NewCertPool()
	if !clientPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to append CA PEM to client pool")
	}

	return caPEM, certPEM, keyPEM, serverTLS, clientPool
}

func TestHandshakeAndAuthentication(t *testing.T) {
	t.Parallel()
	token := "correct-token"
	srv, err := NewServer(Config{
		Token: token,
		Wait:  2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	// 1. Wrong token
	dWrong := &Daemon{
		Addr:     ln.Addr().String(),
		Token:    "wrong-token",
		Name:     "bad-worker",
		Binaries: map[string]string{"agy": "/bin/sh"},
	}
	dctx, dcancel := context.WithTimeout(ctx, 1*time.Second)
	defer dcancel()
	err = dWrong.runOnce(dctx, "bad-worker", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "authentication failure") {
		t.Fatalf("expected auth failure, got: %v", err)
	}

	// 2. Correct token
	dRight := &Daemon{
		Addr:     ln.Addr().String(),
		Token:    token,
		Name:     "good-worker",
		Binaries: map[string]string{"agy": "/bin/sh"},
	}
	go func() { _ = dRight.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil {
		t.Fatalf("srv.Host error: %v", err)
	}
	if host == nil || host.Name() != "good-worker" {
		t.Fatalf("expected good-worker, got: %+v", host)
	}
}

func TestTLSConnectionAndVerification(t *testing.T) {
	t.Parallel()
	token := "tls-secret-token"
	_, _, _, serverTLS, clientPool := generateTestPKI(t, "modelexec.grain.local")

	srv, err := NewServer(Config{
		Token: token,
		Wait:  2 * time.Second,
		TLS:   serverTLS,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	// 1. Client connects with correct TLS CA and ServerName
	dValid := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "tls-worker-1",

		Binaries: map[string]string{"agy": "/bin/sh"},
		TLS: &tls.Config{
			RootCAs:    clientPool,
			ServerName: "modelexec.grain.local",
			MinVersion: tls.VersionTLS12,
		},
	}
	go func() { _ = dValid.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host over TLS: %v, host=%v", err, host)
	}
	out, err := host.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo TLS_SUCCESS"}}, nil)
	if err != nil || strings.TrimSpace(out) != "TLS_SUCCESS" {
		t.Fatalf("host.Run over TLS = %q, %v; want TLS_SUCCESS", out, err)
	}

	// 2. Client connects without trusting the CA -> verification fails
	dUntrusted := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "untrusted-worker",

		Binaries: map[string]string{"agy": "/bin/sh"},
		TLS: &tls.Config{
			RootCAs:    x509.NewCertPool(), // Empty pool
			ServerName: "modelexec.grain.local",
		},
	}
	dctx, dcancel := context.WithTimeout(ctx, 1*time.Second)
	defer dcancel()
	err = dUntrusted.runOnce(dctx, "untrusted-worker", nil, nil)
	if err == nil {
		t.Fatal("expected TLS verification failure with empty CA pool, got nil error")
	}

	// 3. Client connects with mismatched ServerName -> verification fails
	dBadHost := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "badhost-worker",

		Binaries: map[string]string{"agy": "/bin/sh"},
		TLS: &tls.Config{
			RootCAs:    clientPool,
			ServerName: "wrong.domain.local",
		},
	}
	dctx2, dcancel2 := context.WithTimeout(ctx, 1*time.Second)
	defer dcancel2()
	err = dBadHost.runOnce(dctx2, "badhost-worker", nil, nil)
	if err == nil {
		t.Fatal("expected TLS hostname verification failure with wrong ServerName, got nil error")
	}
}

func TestRegistrationAndAgentBackends(t *testing.T) {
	t.Parallel()
	srv, _, cancel := testServerAndDaemon(t, "engineering", nil)
	defer cancel()

	status := srv.Connected()
	if len(status) != 1 || status[0].Host != "worker-1" {
		t.Fatalf("unexpected status: %+v", status)
	}

	backends := srv.AgentBackends()
	if len(backends) < 2 {
		t.Fatalf("expected at least 2 backends for agy and claude, got: %+v", backends)
	}

	var foundAgy, foundClaude bool
	for _, b := range backends {
		if b.AuthMode != model.AntigravityAuthAmbient {
			t.Errorf("backend authMode = %q, want ambient", b.AuthMode)
		}
		if b.Framework == model.AgentFrameworkAntigravity {
			foundAgy = true
		}
		if b.Framework == model.AgentFrameworkClaude {
			foundClaude = true
		}
	}
	if !foundAgy || !foundClaude {
		t.Fatalf("missing agy (%t) or claude (%t) backends: %+v", foundAgy, foundClaude, backends)
	}
}

func TestRemoteHostFilesystem(t *testing.T) {
	t.Parallel()
	_, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	ctx := context.Background()

	tmpDir, err := host.MkdirTemp(ctx, "modelexec-fs-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = host.RemoveAll(ctx, tmpDir) }()

	subDir := filepath.Join(tmpDir, "nested", "dir")
	if err := host.MkdirAll(ctx, subDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	filePath := filepath.Join(subDir, "hello.txt")
	content := []byte("Hello, Remote Executor!")
	if err := host.WriteFile(ctx, filePath, content, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, truncated, err := host.ReadTail(ctx, filePath, 1024)
	if err != nil {
		t.Fatalf("ReadTail: %v", err)
	}
	if truncated || string(data) != string(content) {
		t.Fatalf("ReadTail = %q (truncated=%t), want %q", data, truncated, content)
	}

	linkPath := filepath.Join(tmpDir, "link.txt")
	if err := host.Symlink(ctx, filePath, linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	linkData, _, err := host.ReadTail(ctx, linkPath, 1024)
	if err != nil {
		t.Fatalf("ReadTail on symlink: %v", err)
	}
	if string(linkData) != string(content) {
		t.Fatalf("symlink content = %q, want %q", linkData, content)
	}

	if err := host.RemoveAll(ctx, tmpDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
}

func TestRemoteHostRun(t *testing.T) {
	t.Parallel()
	_, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	ctx := context.Background()

	// 1. Successful execution and stdout streaming
	var streamed strings.Builder
	cmd := agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo line1; sleep 0.05; echo line2"},
	}
	out, err := host.Run(ctx, cmd, &streamed)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	want := "line1\nline2\n"
	if out != want {
		t.Fatalf("Run output = %q, want %q", out, want)
	}
	if streamed.String() != want {
		t.Fatalf("Streamed output = %q, want %q", streamed.String(), want)
	}

	// 2. Cancellation
	runCtx, runCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer runCancel()
	cmdSleep := agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 10"},
	}
	_, err = host.Run(runCtx, cmdSleep, nil)
	if err == nil {
		t.Fatal("expected error on cancelled Run, got nil")
	}
}

func TestRemoteHostRunEnvironmentAndStderr(t *testing.T) {
	t.Parallel()
	_, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	ctx := context.Background()

	// Test passing custom environment variables and capturing stderr
	cmd := agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo out=$TEST_CUSTOM_VAR; echo err=warning_msg >&2"},
		Env:  []string{"TEST_CUSTOM_VAR=remote_custom_val"},
	}
	out, err := host.Run(ctx, cmd, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(out, "out=remote_custom_val") {
		t.Fatalf("Run output %q does not contain expected env var value", out)
	}

	// Test non-zero exit code including stderr in error message
	cmdFail := agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo 'fatal: something broke' >&2; exit 42"},
	}
	_, errFail := host.Run(ctx, cmdFail, nil)
	if errFail == nil {
		t.Fatal("expected error on non-zero exit, got nil")
	}
	if !strings.Contains(errFail.Error(), "fatal: something broke") {
		t.Fatalf("expected stderr output in error message, got: %v", errFail)
	}
}

func TestRemoteHostExpandsHome(t *testing.T) {
	t.Parallel()
	_, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	ctx := context.Background()

	// Test MkdirAll with tilde path
	sessionDir := "~/.grain/test_sessions_mkdir"
	if err := host.MkdirAll(ctx, sessionDir, 0700); err != nil {
		t.Fatalf("MkdirAll on ~ path failed: %v", err)
	}
	defer func() { _ = host.RemoveAll(ctx, sessionDir) }()

	// Test Run with HOME=~
	cmd := agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo home=$HOME"},
		Env:  []string{"HOME=~/.grain/test_sessions_mkdir"},
	}
	out, err := host.Run(ctx, cmd, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Contains(out, "home=~") {
		t.Fatalf("HOME was not expanded, got %q", out)
	}
	if !strings.Contains(out, ".grain/test_sessions_mkdir") {
		t.Fatalf("Run output %q does not contain expected home path", out)
	}
}

func TestMCPTunneling(t *testing.T) {
	t.Parallel()

	tunnelCalled := make(chan []string, 1)
	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		tunnelCalled <- args
		// Read a message from client
		dec := json.NewDecoder(rwc)
		enc := json.NewEncoder(rwc)
		var msg map[string]string
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		// Reply
		return enc.Encode(map[string]string{"reply": "echo:" + msg["msg"]})
	}

	_, host, cancel := testServerAndDaemon(t, "default", serveMCP)
	defer cancel()
	grain := grainPath(t, host)
	ctx := context.Background()

	// We run a small script on the remote executor that dials $GRAIN_EXECUTOR_SOCK,
	// sends args, and exchanges JSON with Grain's serveMCP.
	script := `
python3 -c '
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
if not sock_path:
    sys.exit("missing GRAIN_EXECUTOR_SOCK")

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)

# Send args
s.sendall(json.dumps(["mcpserver", "-sandbox-root", "/tmp"]).encode("utf-8") + b"\n")

# Send MCP message
s.sendall(json.dumps({"msg": "hello-tunnel"}).encode("utf-8") + b"\n")

# Read response
data = b""
while b"\n" not in data:
    chunk = s.recv(1024)
    if not chunk:
        break
    data += chunk

s.close()
resp = json.loads(data.decode("utf-8"))
print("RESPONSE:" + resp.get("reply", ""))
'
`
	cmd := agent.Command{
		Path: grain,
		Args: []string{"-c", script},
	}
	host.AuthorizeMCPArgs([]string{"mcpserver", "-sandbox-root", "/tmp"})
	out, err := host.Run(ctx, cmd, nil)
	if err != nil {
		t.Fatalf("Run failed: %v, output: %s", err, out)
	}

	select {
	case args := <-tunnelCalled:
		if len(args) != 3 || args[0] != "mcpserver" || args[2] != "/tmp" {
			t.Fatalf("unexpected tunnel args: %+v", args)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tunnel to be called")
	}

	if !strings.Contains(out, "RESPONSE:echo:hello-tunnel") {
		t.Fatalf("unexpected command output: %q", out)
	}
}

// TestExecutorSockScopedToGrain is #2369: the MCP tunnel socket is not in
// the environment of what an executor runs -- a run's CLI and every shell
// command it starts -- but only of the grain binary it offers, which is what
// that CLI forks as `grain mcpserver`.
func TestExecutorSockScopedToGrain(t *testing.T) {
	t.Setenv(executorSockEnv, "/inherited/by/the/executor")
	_, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	ctx := context.Background()

	probe := []string{"-c", `echo "sock=[$GRAIN_EXECUTOR_SOCK]"`}
	out, err := host.Run(ctx, agent.Command{Path: "/bin/sh", Args: probe}, nil)
	if err != nil {
		t.Fatalf("Run: %v, output: %s", err, out)
	}
	if !strings.Contains(out, "sock=[]") {
		t.Fatalf("an ordinary command saw the tunnel socket: %q", out)
	}

	out, err = host.Run(ctx, agent.Command{Path: grainPath(t, host), Args: probe}, nil)
	if err != nil {
		t.Fatalf("Run via grain: %v, output: %s", err, out)
	}
	if !strings.Contains(out, "sock=[/") || !strings.Contains(out, "mcp.sock]") {
		t.Fatalf("the grain binary was not given the tunnel socket: %q", out)
	}
}

// TestMCPTunnelRefusesArgsGrainDidNotIssue is the executor choosing its own
// mcpserver arguments: a -sandbox-root naming the controller's own root
// directory, when grain issued it a run in /tmp. Serving that would be a bash
// shell on the controller (run_command), so the stream must be closed without
// ServeMCP ever being called with it.
func TestMCPTunnelRefusesArgsGrainDidNotIssue(t *testing.T) {
	t.Parallel()

	tunnelCalled := make(chan []string, 4)
	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		tunnelCalled <- args
		return nil
	}

	_, host, cancel := testServerAndDaemon(t, "default", serveMCP)
	defer cancel()
	ctx := context.Background()

	host.AuthorizeMCPArgs([]string{"mcpserver", "-sandbox-root", "/tmp"})
	script := `
python3 -c '
import os, socket, json

for args in (["mcpserver", "-sandbox-root", "/"], ["-sandbox-root", "/tmp", "-data-dir", "/"], []):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(os.environ["GRAIN_EXECUTOR_SOCK"])
    s.sendall(json.dumps(args).encode("utf-8") + b"\n")
    s.recv(1024)
    s.close()
print("done")
'
`
	out, err := host.Run(ctx, agent.Command{Path: grainPath(t, host), Args: []string{"-c", script}}, nil)
	if err != nil {
		t.Fatalf("host.Run: %v, output: %s", err, out)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("output = %q, want done", out)
	}
	select {
	case args := <-tunnelCalled:
		t.Fatalf("ServeMCP was called with arguments grain never issued: %q", args)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestMCPTunnelGracefulHalfClose(t *testing.T) {
	t.Parallel()

	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		dec := json.NewDecoder(rwc)
		enc := json.NewEncoder(rwc)

		var msg map[string]string
		if err := dec.Decode(&msg); err != nil {
			return err
		}

		// Ensure we read until EOF from client before replying (proving client closed write side)
		var trailing map[string]string
		if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
			return fmt.Errorf("expected EOF after first message, got %v", err)
		}

		// Simulate server-side processing delay after client has half-closed its write side
		time.Sleep(50 * time.Millisecond)

		// Send multiple response messages to verify no truncation
		for i := 0; i < 5; i++ {
			if err := enc.Encode(map[string]any{
				"seq":   i,
				"reply": fmt.Sprintf("chunk-%d-%s", i, msg["query"]),
			}); err != nil {
				return err
			}
		}
		return nil
	}

	_, host, cancel := testServerAndDaemon(t, "default", serveMCP)
	defer cancel()
	grain := grainPath(t, host)
	ctx := context.Background()

	script := `
python3 -c '
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
if not sock_path:
    sys.exit("missing GRAIN_EXECUTOR_SOCK")

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)

# Send args header
s.sendall(json.dumps(["mcpserver", "test-half-close"]).encode("utf-8") + b"\n")

# Send single request payload
s.sendall(json.dumps({"query": "verify-half-close"}).encode("utf-8") + b"\n")

# Half-close write side to signal EOF to server
s.shutdown(socket.SHUT_WR)

# Read all responses until EOF
received = []
buf = b""
while True:
    chunk = s.recv(1024)
    if not chunk:
        break
    buf += chunk
    while b"\n" in buf:
        line, buf = buf.split(b"\n", 1)
        if line.strip():
            received.append(json.loads(line.decode("utf-8")))

s.close()

if len(received) != 5:
    sys.exit(f"expected 5 chunks, got {len(received)}: {received}")

for i, obj in enumerate(received):
    if obj.get("seq") != i or obj.get("reply") != f"chunk-{i}-verify-half-close":
        sys.exit(f"mismatched chunk at {i}: {obj}")

print("HALF_CLOSE_SUCCESS")
'
`
	cmd := agent.Command{
		Path: grain,
		Args: []string{"-c", script},
	}
	host.AuthorizeMCPArgs([]string{"mcpserver", "test-half-close"})
	out, err := host.Run(ctx, cmd, nil)
	if err != nil {
		t.Fatalf("Run failed: %v, output: %s", err, out)
	}

	if !strings.Contains(out, "HALF_CLOSE_SUCCESS") {
		t.Fatalf("expected HALF_CLOSE_SUCCESS, got output: %q", out)
	}
}

func TestMCPTunnelGracefulHalfCloseConcurrent(t *testing.T) {
	t.Parallel()

	const concurrency = 6
	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		dec := json.NewDecoder(rwc)
		enc := json.NewEncoder(rwc)

		var msg map[string]string
		if err := dec.Decode(&msg); err != nil {
			return err
		}

		// Read until EOF to confirm client closed write half.
		var trailing map[string]string
		if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
			return fmt.Errorf("expected EOF after first message, got %v", err)
		}

		// Respond with multiple chunks.
		sender := msg["sender_id"]
		for i := 0; i < 4; i++ {
			if err := enc.Encode(map[string]any{
				"sender": sender,
				"seq":    i,
				"data":   strings.Repeat("x", 512),
			}); err != nil {
				return err
			}
		}
		return nil
	}

	_, host, cancel := testServerAndDaemon(t, "default", serveMCP)
	defer cancel()
	grain := grainPath(t, host)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		senderID := fmt.Sprintf("sender-%02d", i)
		go func(id string) {
			defer wg.Done()
			script := fmt.Sprintf(`
python3 -c '
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.sendall(json.dumps(["mcpserver"]).encode("utf-8") + b"\n")
s.sendall(json.dumps({"sender_id": "%s"}).encode("utf-8") + b"\n")
s.shutdown(socket.SHUT_WR)

received = []
buf = b""
while True:
    chunk = s.recv(1024)
    if not chunk:
        break
    buf += chunk
    while b"\n" in buf:
        line, buf = buf.split(b"\n", 1)
        if line.strip():
            received.append(json.loads(line.decode("utf-8")))
s.close()

if len(received) != 4:
    sys.exit(f"expected 4 chunks for %s, got {len(received)}")

for idx, item in enumerate(received):
    if item.get("sender") != "%s" or item.get("seq") != idx:
        sys.exit(f"invalid item at {idx} for %s: {item}")

print("DONE:%s")
'
`, id, id, id, id, id)
			host.AuthorizeMCPArgs([]string{"mcpserver"})
			out, err := host.Run(ctx, agent.Command{
				Path: grain,
				Args: []string{"-c", script},
			}, nil)
			if err != nil {
				errs <- fmt.Errorf("worker %s failed: %w, output: %s", id, err, out)
				return
			}
			if !strings.Contains(out, "DONE:"+id) {
				errs <- fmt.Errorf("worker %s missing DONE output: %s", id, out)
				return
			}
		}(senderID)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestMultiplexedConcurrentMCPStreams(t *testing.T) {
	t.Parallel()

	const concurrency = 8
	var mu sync.Mutex
	received := make(map[string]int)

	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		dec := json.NewDecoder(rwc)
		enc := json.NewEncoder(rwc)
		for {
			var msg map[string]string
			if err := dec.Decode(&msg); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			senderID := msg["sender_id"]
			mu.Lock()
			received[senderID]++
			mu.Unlock()

			if err := enc.Encode(map[string]string{"reply": "ack:" + senderID}); err != nil {
				return err
			}
		}
	}

	_, host, cancel := testServerAndDaemon(t, "default", serveMCP)
	defer cancel()
	grain := grainPath(t, host)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		senderID := fmt.Sprintf("sender-%02d", i)
		go func(id string) {
			defer wg.Done()
			script := fmt.Sprintf(`
python3 -c '
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.sendall(json.dumps(["mcpserver"]).encode("utf-8") + b"\n")

for i in range(3):
    s.sendall(json.dumps({"sender_id": "%s"}).encode("utf-8") + b"\n")
    data = b""
    while b"\n" not in data:
        chunk = s.recv(1024)
        if not chunk:
            break
        data += chunk
    resp = json.loads(data.decode("utf-8"))
    if resp.get("reply") != "ack:%s":
        sys.exit("mismatch reply: " + repr(resp))
s.close()
print("DONE:%s")
'
`, id, id, id)
			host.AuthorizeMCPArgs([]string{"mcpserver"})
			out, err := host.Run(ctx, agent.Command{
				Path: grain,
				Args: []string{"-c", script},
			}, nil)
			if err != nil {
				errs <- fmt.Errorf("worker %s failed: %w, output: %s", id, err, out)
				return
			}
			if !strings.Contains(out, "DONE:"+id) {
				errs <- fmt.Errorf("worker %s missing DONE output: %s", id, out)
				return
			}
		}(senderID)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < concurrency; i++ {
		id := fmt.Sprintf("sender-%02d", i)
		if count := received[id]; count != 3 {
			t.Errorf("sender %s count = %d, want 3", id, count)
		}
	}
}

func TestRemoteHostPlaceFile(t *testing.T) {
	t.Parallel()
	_, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	ctx := context.Background()

	tmpDir, err := host.MkdirTemp(ctx, "modelexec-placefile-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = host.RemoveAll(ctx, tmpDir) }()

	target := filepath.Join(tmpDir, "sub", "nested", ".gemini-api-key")
	content := "secret-api-key-value"
	if err := host.PlaceFile(ctx, target, content, "600"); err != nil {
		t.Fatalf("PlaceFile: %v", err)
	}

	data, truncated, err := host.ReadTail(ctx, target, 1024)
	if err != nil {
		t.Fatalf("ReadTail: %v", err)
	}
	if truncated || string(data) != content {
		t.Fatalf("ReadTail = %q, want %q", string(data), content)
	}

	if !host.Unconfined() {
		t.Fatal("expected RemoteHost.Unconfined() to be true")
	}
}

func TestRemoteHostDisconnectionDuringExecution(t *testing.T) {
	t.Parallel()
	token := "disconnect-secret-token"

	srv, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	dCtx, dCancel := context.WithCancel(ctx)
	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "disconnect-worker",

		Binaries: map[string]string{"agy": "/bin/sh"},
	}
	go func() { _ = d.Run(dCtx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v", err)
	}

	// Start a sleep command on remote host
	errCh := make(chan error, 1)
	go func() {
		_, err := host.Run(ctx, agent.Command{
			Path: "/bin/sh",
			Args: []string{"-c", "sleep 10"},
		}, nil)
		errCh <- err
	}()

	// Wait briefly for command to start, then cancel daemon context (simulating sudden disconnection)
	time.Sleep(100 * time.Millisecond)
	dCancel()

	select {
	case runErr := <-errCh:
		if runErr == nil {
			t.Fatal("expected error on disconnected host, got nil")
		}
		if !strings.Contains(runErr.Error(), "disconnected") && !strings.Contains(runErr.Error(), "closed") {
			t.Logf("got expected disconnection error: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for host.Run to fail on disconnection")
	}
}

func TestDaemonReconnectOnServerRestart(t *testing.T) {
	t.Parallel()
	token := "reconnect-secret-token"

	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen 1: %v", err)
	}
	serverAddr := ln1.Addr().String()

	srv1, err := NewServer(Config{Token: token, Wait: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewServer 1: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv1Ctx, srv1Cancel := context.WithCancel(ctx)
	go func() { _ = srv1.Serve(srv1Ctx, ln1) }()

	d := &Daemon{
		Addr:  serverAddr,
		Token: token,
		Name:  "reconnect-worker",

		Binaries:   map[string]string{"agy": "/bin/sh"},
		MinBackoff: 50 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond,
	}
	go func() { _ = d.Run(ctx) }()

	host1, err := srv1.Host(ctx, "agy")
	if err != nil || host1 == nil {
		t.Fatalf("srv1.Host: %v", err)
	}

	// Terminate server 1
	srv1Cancel()
	_ = ln1.Close()

	// Start server 2 on same address
	time.Sleep(100 * time.Millisecond)
	ln2, err := net.Listen("tcp", serverAddr)
	if err != nil {
		t.Fatalf("net.Listen 2 on %s: %v", serverAddr, err)
	}
	defer ln2.Close()

	srv2, err := NewServer(Config{Token: token, Wait: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewServer 2: %v", err)
	}
	go func() { _ = srv2.Serve(ctx, ln2) }()

	// Verify daemon reconnects to server 2
	host2, err := srv2.Host(ctx, "agy")
	if err != nil || host2 == nil {
		t.Fatalf("srv2.Host after reconnect: %v", err)
	}
	out, err := host2.Run(ctx, agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo RECONNECTED"},
	}, nil)
	if err != nil || strings.TrimSpace(out) != "RECONNECTED" {
		t.Fatalf("host.Run after reconnect = %q, %v; want RECONNECTED", out, err)
	}
}

func TestDaemonHTTPUpgradeDial(t *testing.T) {
	t.Parallel()
	token := "http-upgrade-secret-token"

	srv, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var receivedAuthHeader string
	var receivedExecutorToken string
	var receivedTunnelToken string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedExecutorToken = r.Header.Get("X-Grain-Executor-Token")
		receivedTunnelToken = r.Header.Get("X-Grain-Tunnel-Token")

		if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") && r.Header.Get("Upgrade") == "" {
			http.Error(w, "expected upgrade", http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusInternalServerError)
			return
		}

		conn, brw, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()

		_, _ = fmt.Fprint(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: grain-executor\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()

		var clientReader io.Reader = conn
		if brw != nil && brw.Reader.Buffered() > 0 {
			clientReader = io.MultiReader(brw.Reader, conn)
		}

		wrapped := &testReaderConn{Conn: conn, r: clientReader}
		srv.HandleConn(r.Context(), wrapped)
	}))
	defer ts.Close()

	d := &Daemon{
		Addr:  ts.URL,
		Token: token,
		Name:  "upgrade-worker",

		Binaries: map[string]string{
			"echo": "/bin/echo",
		},
		TokenProvider: func(ctx context.Context) (string, error) {
			return "iap-bearer-jwt-token", nil
		},
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "echo")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v", err)
	}

	out, err := host.Run(ctx, agent.Command{
		Path: "/bin/echo",
		Args: []string{"hello-http-upgrade"},
	}, nil)
	if err != nil {
		t.Fatalf("host.Run: %v, out = %q", err, out)
	}
	if !strings.Contains(out, "hello-http-upgrade") {
		t.Fatalf("unexpected command output: %q", out)
	}

	if receivedAuthHeader != "Bearer iap-bearer-jwt-token" {
		t.Errorf("received Authorization header = %q, want Bearer iap-bearer-jwt-token", receivedAuthHeader)
	}
	if receivedExecutorToken != token {
		t.Errorf("received X-Grain-Executor-Token = %q, want %q", receivedExecutorToken, token)
	}
	if receivedTunnelToken != token {
		t.Errorf("received X-Grain-Tunnel-Token = %q, want %q", receivedTunnelToken, token)
	}
}

type testReaderConn struct {
	net.Conn
	r io.Reader
}

func (c *testReaderConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

func TestControlStreamIdleKeepalive(t *testing.T) {
	t.Parallel()
	token := "keepalive-secret-token"

	srv, err := NewServer(Config{
		Token:        token,
		Wait:         2 * time.Second,
		IdleTimeout:  2 * time.Second,
		PingInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "keepalive-worker",

		Binaries:     map[string]string{"agy": "/bin/sh"},
		IdleTimeout:  2 * time.Second,
		PingInterval: 100 * time.Millisecond,
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v, host=%v", err, host)
	}

	// Sleep past the idle timeout duration (4.5s > 2 * 2s idleTimeout). The
	// margin between PingInterval and IdleTimeout is kept wide (20x) so that
	// scheduling jitter from other tests running in parallel under load
	// cannot make this test flake: periodic keepalive pings over the control
	// stream should keep the connection alive.
	time.Sleep(4500 * time.Millisecond)

	out, err := host.Run(ctx, agent.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo KEEPALIVE_OK"},
	}, nil)
	if err != nil || strings.TrimSpace(out) != "KEEPALIVE_OK" {
		t.Fatalf("host.Run after idle period failed: out=%q, err=%v", out, err)
	}

	// Test filesystem operation on the same kept-alive host.
	tmp, err := host.MkdirTemp(ctx, "keepalive-*")
	if err != nil {
		t.Fatalf("host.MkdirTemp failed: %v", err)
	}
	if err := host.RemoveAll(ctx, tmp); err != nil {
		t.Fatalf("host.RemoveAll failed: %v", err)
	}
}

func TestControlStreamIdleTimeoutEnforcement(t *testing.T) {
	t.Parallel()
	token := "timeout-secret-token"

	srv, err := NewServer(Config{
		Token:       token,
		Wait:        2 * time.Second,
		IdleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	// Connect a raw client that handshakes but sends no pings and no requests.
	rawConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	defer rawConn.Close()

	m := mux.NewMux(rawConn, mux.Config{
		IsServer:     false,
		PingInterval: 1 * time.Second,
		PingTimeout:  2 * time.Second,
	})
	defer m.Close()

	ctrlStream, err := m.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer ctrlStream.Close()

	codec := newCodec(ctrlStream)
	hello := frame{
		Type:            frameHello,
		ProtocolVersion: ProtocolVersion,
		Token:           token,
		Host:            "silent-worker",
		Binaries:        map[string]string{"agy": "/bin/sh"},
	}
	if err := codec.write(hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	welcome, err := codec.read(2 * time.Second)
	if err != nil || welcome.Type != frameWelcome {
		t.Fatalf("read welcome: %v, frame=%+v", err, welcome)
	}

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v, host=%v", err, host)
	}

	// Server's idle timeout is 100ms. Wait 250ms with no activity.
	time.Sleep(250 * time.Millisecond)

	// Since silent-worker sent no pings, the server's idle timeout must have closed the connection.
	_, err = host.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo SHOULD_FAIL"}}, nil)
	if err == nil {
		t.Fatal("expected error on timed out connection, got nil")
	}
}

func testGitHTTPBackend(t *testing.T, projectRoot string) http.Handler {
	execPathOut, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	gitHttpBackend := filepath.Join(strings.TrimSpace(string(execPathOut)), "git-http-backend")
	return &cgi.Handler{
		Path: gitHttpBackend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
}

func TestGitProxyTunneling(t *testing.T) {
	t.Parallel()
	token := "secret-test-token"

	gitProxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Echo", r.Header.Get("X-Custom-Header"))
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(fmt.Sprintf("echo post: %s (path %s)", string(body), r.URL.Path)))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("echo get path: %s", r.URL.Path)))
	})

	srv, err := NewServer(Config{
		Token:    token,
		Wait:     5 * time.Second,
		GitProxy: gitProxyHandler,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Serve(ctx, ln) }()

	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "worker-gitproxy",

		Binaries: map[string]string{
			"agy": "/bin/sh",
		},
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil {
		t.Fatalf("srv.Host: %v", err)
	}
	if host == nil {
		t.Fatal("srv.Host returned nil host")
	}

	proxyURL := host.GitProxyURL()
	if proxyURL == "" || !strings.HasPrefix(proxyURL, "http://127.0.0.1:") {
		t.Fatalf("host.GitProxyURL() = %q, want http://127.0.0.1:<port>", proxyURL)
	}

	// Verify srv.Connected() reports GitProxyURL
	connStatuses := srv.Connected()
	if len(connStatuses) == 0 || connStatuses[0].GitProxyURL != proxyURL {
		t.Fatalf("srv.Connected() GitProxyURL = %q, want %q", connStatuses[0].GitProxyURL, proxyURL)
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Test GET request over tunneled git proxy
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL+"/test/repo/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("X-Custom-Header", "test-value-123")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do GET over tunnel: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	if echo := resp.Header.Get("X-Custom-Echo"); echo != "test-value-123" {
		t.Fatalf("X-Custom-Echo = %q, want test-value-123", echo)
	}
	body, _ := io.ReadAll(resp.Body)
	if want := "echo get path: /test/repo/info/refs"; string(body) != want {
		t.Fatalf("GET body = %q, want %q", string(body), want)
	}

	// 2. Test POST request over tunneled git proxy
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+"/test/repo/git-upload-pack", strings.NewReader("hello-packfile-bytes"))
	if err != nil {
		t.Fatalf("http.NewRequest POST: %v", err)
	}
	postReq.Header.Set("X-Custom-Header", "test-post-header")
	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("client.Do POST over tunnel: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", postResp.StatusCode)
	}
	if echo := postResp.Header.Get("X-Custom-Echo"); echo != "test-post-header" {
		t.Fatalf("X-Custom-Echo = %q, want test-post-header", echo)
	}
	postBody, _ := io.ReadAll(postResp.Body)
	if want := "echo post: hello-packfile-bytes (path /test/repo/git-upload-pack)"; string(postBody) != want {
		t.Fatalf("POST body = %q, want %q", string(postBody), want)
	}
}

func TestGitProxyCloneFetchPush(t *testing.T) {
	t.Parallel()

	// 1. Initialize a bare Git upstream repo on the "server" side
	upstreamDir := t.TempDir()
	repoPath := filepath.Join(upstreamDir, "repo.git")
	runCmd := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		t.Logf("cmd %v in %q -> out: %s, err: %v", args, dir, string(out), err)
		if err != nil {
			t.Fatalf("command %v failed in %s: %v\noutput: %s", args, dir, err, out)
		}
	}

	runCmd(upstreamDir, "git", "init", "--bare", "-b", "main", repoPath)
	runCmd(repoPath, "git", "config", "http.receivepack", "true")

	// Seed with an initial commit in a temp work tree
	seedDir := t.TempDir()
	runCmd(seedDir, "git", "init", "-b", "main")
	runCmd(seedDir, "git", "config", "user.name", "Seed Author")
	runCmd(seedDir, "git", "config", "user.email", "seed@example.com")
	if err := os.WriteFile(filepath.Join(seedDir, "hello.txt"), []byte("initial commit content\n"), 0644); err != nil {
		t.Fatalf("write hello.txt: %v", err)
	}
	runCmd(seedDir, "git", "add", "hello.txt")
	runCmd(seedDir, "git", "commit", "-m", "initial commit")
	runCmd(seedDir, "git", "remote", "add", "origin", repoPath)
	runCmd(seedDir, "git", "push", "origin", "main")

	// 2. Start Grain server with GitProxy backend
	token := "test-git-token"
	srv, err := NewServer(Config{
		Token:    token,
		Wait:     5 * time.Second,
		GitProxy: testGitHTTPBackend(t, upstreamDir),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Serve(ctx, ln) }()

	// 3. Start remote executor daemon
	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "worker-git-clone",

		Binaries: map[string]string{
			"agy": "/bin/sh",
		},
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v", err)
	}

	proxyURL := host.GitProxyURL()
	if proxyURL == "" {
		t.Fatal("host.GitProxyURL() is empty")
	}

	// 4. On the client side, git clone through the multiplexed git proxy tunnel
	clientWorkDir := filepath.Join(t.TempDir(), "client-work")
	cloneURL := fmt.Sprintf("%s/repo.git", proxyURL)

	runCmd("", "git", "clone", cloneURL, clientWorkDir)

	// Verify cloned content
	helloBytes, err := os.ReadFile(filepath.Join(clientWorkDir, "hello.txt"))
	if err != nil || string(helloBytes) != "initial commit content\n" {
		t.Fatalf("hello.txt content = %q, want 'initial commit content\\n'", string(helloBytes))
	}

	// 5. Make a new commit on the client side and git push through the multiplexed tunnel
	runCmd(clientWorkDir, "git", "config", "user.name", "Client Committer")
	runCmd(clientWorkDir, "git", "config", "user.email", "client@example.com")
	if err := os.WriteFile(filepath.Join(clientWorkDir, "pushed.txt"), []byte("pushed content over multiplexed tunnel\n"), 0644); err != nil {
		t.Fatalf("write pushed.txt: %v", err)
	}
	runCmd(clientWorkDir, "git", "add", "pushed.txt")
	runCmd(clientWorkDir, "git", "commit", "-m", "second commit from client")
	runCmd(clientWorkDir, "git", "push", "origin", "main")

	// 6. Verify the upstream repo received the commit by cloning into a separate verification directory
	verifyDir := filepath.Join(t.TempDir(), "verify-work")
	runCmd("", "git", "clone", cloneURL, verifyDir)
	pushedBytes, err := os.ReadFile(filepath.Join(verifyDir, "pushed.txt"))
	if err != nil || string(pushedBytes) != "pushed content over multiplexed tunnel\n" {
		t.Fatalf("verify pushed.txt content = %q, want 'pushed content over multiplexed tunnel\\n'", string(pushedBytes))
	}

	// 7. Verify git fetch works through tunnel
	runCmd(clientWorkDir, "git", "fetch", "origin")
}

func TestMultiplexedConcurrentGitProxyAndMCPStreams(t *testing.T) {
	t.Parallel()
	token := "concurrent-test-token"

	gitProxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("gitproxy-ack:" + string(body)))
	})

	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		dec := json.NewDecoder(rwc)
		enc := json.NewEncoder(rwc)
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		resp := map[string]any{"mcp_reply": msg["query"], "args": args}
		return enc.Encode(resp)
	}

	srv, err := NewServer(Config{
		Token:    token,
		Wait:     5 * time.Second,
		GitProxy: gitProxyHandler,
		ServeMCP: serveMCP,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Serve(ctx, ln) }()

	d := &Daemon{
		Addr:  ln.Addr().String(),
		Token: token,
		Name:  "worker-concurrent",

		Binaries: map[string]string{
			"agy": "/bin/sh",
		},
	}
	go func() { _ = d.Run(ctx) }()

	host, err := srv.Host(ctx, "agy")
	if err != nil || host == nil {
		t.Fatalf("srv.Host: %v", err)
	}

	proxyURL := host.GitProxyURL()
	httpClient := &http.Client{Timeout: 5 * time.Second}

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	// Launch 10 concurrent HTTP requests over git proxy
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := fmt.Sprintf("req-%d", idx)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+fmt.Sprintf("/repo-%d.git/info/refs", idx), strings.NewReader(payload))
			if err != nil {
				errCh <- fmt.Errorf("gitproxy req %d: %w", idx, err)
				return
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				errCh <- fmt.Errorf("gitproxy do %d: %w", idx, err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != "gitproxy-ack:"+payload {
				errCh <- fmt.Errorf("gitproxy resp %d = %q, want %q", idx, string(body), "gitproxy-ack:"+payload)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent test error: %v", err)
	}
}

func TestRemoteHostConfigureGitCredentials(t *testing.T) {
	t.Parallel()
	srv, host, cancel := testServerAndDaemon(t, "default", nil)
	defer cancel()
	_ = srv

	ctx := context.Background()
	remoteDir, err := host.MkdirTemp(ctx, "gitcred-test-*")
	if err != nil {
		t.Fatalf("host.MkdirTemp: %v", err)
	}
	defer func() { _ = host.RemoveAll(ctx, remoteDir) }()

	identity := mcp.GitIdentity{
		Name:  "Test Bot",
		Email: "bot@example.com",
	}
	proxyURL := "http://127.0.0.1:9999/owner/repo.git"
	token := "test-secret-token"

	if err := host.ConfigureGitCredentials(ctx, remoteDir, proxyURL, token, identity); err != nil {
		t.Fatalf("host.ConfigureGitCredentials: %v", err)
	}

	// Verify .git-credentials file was created on remote host
	credData, _, err := host.ReadTail(ctx, filepath.Join(remoteDir, ".git-credentials"), 4096)
	if err != nil {
		t.Fatalf("host.ReadTail .git-credentials: %v", err)
	}
	wantCred := "http://sandbox:test-secret-token@127.0.0.1:9999\n"
	if string(credData) != wantCred {
		t.Fatalf(".git-credentials content = %q, want %q", string(credData), wantCred)
	}

	// Verify .gitconfig file was created on remote host
	configData, _, err := host.ReadTail(ctx, filepath.Join(remoteDir, ".gitconfig"), 4096)
	if err != nil {
		t.Fatalf("host.ReadTail .gitconfig: %v", err)
	}
	if !strings.Contains(string(configData), "helper = store") || !strings.Contains(string(configData), "name = Test Bot") || !strings.Contains(string(configData), "email = bot@example.com") {
		t.Fatalf(".gitconfig content = %q, missing expected lines", string(configData))
	}
}

func TestMCPTunnelMalformedArgsReturnsError(t *testing.T) {
	t.Parallel()

	serveMCP := func(rwc io.ReadWriteCloser, args []string) error {
		defer rwc.Close()
		if len(args) == 0 || args[0] == "malformed" {
			return errors.New("malformed args")
		}
		return nil
	}

	_, host, cancel := testServerAndDaemon(t, "default", serveMCP)
	defer cancel()
	grain := grainPath(t, host)
	ctx := context.Background()

	script := `
python3 -c '
import os, sys, socket, json

sock_path = os.environ.get("GRAIN_EXECUTOR_SOCK")
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)

# Send malformed args
s.sendall(json.dumps(["malformed"]).encode("utf-8") + b"\n")

# Read response - expect connection close / EOF
data = s.recv(1024)
s.close()
print("done")
'
`
	out, err := host.Run(ctx, agent.Command{
		Path: grain,
		Args: []string{"-c", script},
	}, nil)
	if err != nil {
		t.Fatalf("host.Run: %v", err)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("output = %q, want 'done'", out)
	}
}
