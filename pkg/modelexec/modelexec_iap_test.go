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
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// mockIAPProxy creates a TCP listener simulating a GCP IAP / HTTPS proxy that accepts
// CONNECT requests, validates Proxy-Authorization / Authorization headers, and bridges
// to upstream.
func startMockIAPProxy(t *testing.T, expectedAudience, expectedToken string, upstreamAddr string) (string, *atomic.Int32, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening mock IAP proxy: %v", err)
	}

	var authCount atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for {
			clientConn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}

				proxyAuth := req.Header.Get("Proxy-Authorization")
				auth := req.Header.Get("Authorization")
				wantHeader := "Bearer " + expectedToken

				if req.Method != http.MethodConnect || (proxyAuth != wantHeader && auth != wantHeader) {
					resp := "HTTP/1.1 401 Unauthorized\r\nContent-Type: text/plain\r\nContent-Length: 12\r\n\r\nUnauthorized"
					_, _ = c.Write([]byte(resp))
					return
				}

				authCount.Add(1)

				// Dial upstream server
				upstreamConn, err := net.Dial("tcp", upstreamAddr)
				if err != nil {
					resp := "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"
					_, _ = c.Write([]byte(resp))
					return
				}
				defer upstreamConn.Close()

				// Send 200 Connection Established
				_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

				// Forward bidirectionally
				errc := make(chan error, 2)
				go func() {
					_, err := io.Copy(upstreamConn, io.MultiReader(br, c))
					errc <- err
				}()
				go func() {
					_, err := io.Copy(c, upstreamConn)
					errc <- err
				}()
				select {
				case <-errc:
				case <-ctx.Done():
				}
			}(clientConn)
		}
	}()

	cleanup := func() {
		cancel()
		_ = ln.Close()
	}

	return ln.Addr().String(), &authCount, cleanup
}

type mockTokenProvider struct {
	token string
	err   error
}

func (m *mockTokenProvider) Token(ctx context.Context) (string, error) {
	return m.token, m.err
}

func TestDaemonWithIAPProxyAuthentication(t *testing.T) {
	t.Parallel()

	token := "shared-daemon-token-xyz"
	iapAudience := "test-iap-client-id-999"
	iapIDToken := createTestJWT(t, time.Now().Add(1*time.Hour), "iap-client-user@example.com")

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

	server, err := NewServer(Config{
		Token: token,
		Wait:  5 * time.Second,
		Logf:  safeLogf,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer serverLn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Serve(ctx, serverLn)
	}()

	// Start Mock IAP Proxy in front of server
	proxyAddr, authCount, closeProxy := startMockIAPProxy(t, iapAudience, iapIDToken, serverLn.Addr().String())
	defer closeProxy()

	// Start Daemon pointing at proxy with IAP audience and token provider
	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()

	d := &Daemon{
		Addr:  proxyAddr,
		Token: token,

		Binaries:         map[string]string{"echo": "/bin/echo"},
		IAPAudience:      iapAudience,
		IAPTokenProvider: &mockTokenProvider{token: iapIDToken},
		Logf:             safeLogf,
	}

	daemonErrCh := make(chan error, 1)
	go func() {
		daemonErrCh <- d.Run(daemonCtx)
	}()

	// Wait for host to connect to server through IAP proxy
	var host *RemoteHost
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		host = server.ConnectedHost("echo")
		if host != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if host == nil {
		t.Fatalf("host failed to register with server through IAP proxy")
	}

	if authCount.Load() == 0 {
		t.Fatalf("expected IAP proxy to authenticate connection, got 0 auths")
	}

	// Verify command execution over IAP-authenticated connection
	res, err := host.Run(ctx, agent.Command{
		Path: "/bin/echo",
		Args: []string{"iap", "connection", "works"},
	}, nil)
	if err != nil {
		t.Fatalf("host.Run error: %v", err)
	}
	if strings.TrimSpace(res) != "iap connection works" {
		t.Errorf("expected 'iap connection works', got %q", res)
	}
}

func TestDaemonIAPAuthenticationRejection(t *testing.T) {
	t.Parallel()

	token := "shared-daemon-token-xyz"
	iapAudience := "test-iap-client-id-999"
	validToken := "correct-id-token"
	invalidToken := "wrong-id-token"

	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer serverLn.Close()

	proxyAddr, _, closeProxy := startMockIAPProxy(t, iapAudience, validToken, serverLn.Addr().String())
	defer closeProxy()

	d := &Daemon{
		Addr:  proxyAddr,
		Token: token,

		Binaries:         map[string]string{"echo": "/bin/echo"},
		IAPAudience:      iapAudience,
		IAPTokenProvider: &mockTokenProvider{token: invalidToken},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The socket directory is made the way Daemon.Run makes its own,
	// rather than with t.TempDir, because a unix socket's path has to fit
	// in sockaddr_un.sun_path -- 104 bytes on darwin, 108 on Linux -- and
	// t.TempDir's does not always. On Linux it is /tmp/<test name><digits>/001,
	// which leaves room; on macOS the per-user temp area is itself
	// /var/folders/<2 chars>/<30 chars>/T/, and that plus this test's long
	// name plus /001/mcp.sock comes to 107 characters. Over the limit,
	// bind(2) answers EINVAL, so the whole of what the test reports is
	//
	//	Listen unix /var/folders/...: bind: invalid argument
	//
	// which names neither the length nor the limit. The daemon itself is
	// not exposed to this -- os.MkdirTemp("", "grain-executor-*") is 25
	// characters, not 50 -- so what is being kept short here is the test's
	// own setup, not production's.
	sockDir, err := os.MkdirTemp("", "grain-executor-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	sockLn, err := net.Listen("unix", filepath.Join(sockDir, "mcp.sock"))
	if err != nil {
		t.Fatalf("Listen unix: %v", err)
	}
	defer sockLn.Close()

	err = d.runOnce(ctx, "test-host", sockLn, d.Binaries)
	if err == nil {
		t.Fatal("expected error due to IAP rejection, got nil")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("expected 401 Unauthorized in error, got: %v", err)
	}
}
