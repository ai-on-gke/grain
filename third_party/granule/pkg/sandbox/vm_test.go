package sandbox_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/relay"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// TestVM_AccessOverTCPListen drives a real guest booted under
// cmd/granule-shim with GRANULE_ACCESS_LISTEN=tcp://..., directly and
// through granule relay, and checks the guest cannot reach the listener or
// see the token. test/sandbox-access-vm.sh boots the VM and runs this from
// a peer network namespace (standing in for the relay's or controller's
// pod); it is skipped anywhere else.
//
//	GRANULE_ACCESS_VM_ADDR   the sandbox's IP:port as the peer reaches it
//	GRANULE_ACCESS_VM_TOKEN  its per-sandbox token
//	GRANULE_ACCESS_VM_CA     PEM CA that signed its listener certificate
//	GRANULE_ACCESS_VM_NAME   the certificate's DNS name
func TestVM_AccessOverTCPListen(t *testing.T) {
	addr := os.Getenv("GRANULE_ACCESS_VM_ADDR")
	if addr == "" {
		t.Skip("GRANULE_ACCESS_VM_ADDR unset; run test/sandbox-access-vm.sh")
	}
	token := os.Getenv("GRANULE_ACCESS_VM_TOKEN")
	caPEM, err := os.ReadFile(os.Getenv("GRANULE_ACCESS_VM_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("no certificates in GRANULE_ACCESS_VM_CA")
	}
	name := os.Getenv("GRANULE_ACCESS_VM_NAME")
	sbxTLS := &tls.Config{RootCAs: pool, ServerName: name}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Refusals against the real listener.
	for _, tok := range []string{"", "not-the-token"} {
		if _, err := sandbox.Dial(ctx, "tls://"+addr, sandbox.DialOptions{Token: tok, TLSConfig: sbxTLS}); !errors.Is(err, sandbox.ErrAuthFailed) {
			t.Fatalf("Dial with token %q = %v, want ErrAuthFailed", tok, err)
		}
	}

	t.Run("direct", func(t *testing.T) {
		proxy := newCutProxy(t, addr)
		cli, err := sandbox.Dial(ctx, "tls://"+proxy.addr(), sandbox.DialOptions{Token: token, TLSConfig: sbxTLS})
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer cli.Close()
		vmScenarios(t, ctx, cli, proxy.cut, token, host, port)
	})

	t.Run("relay", func(t *testing.T) {
		pki := sandbox.NewTestPKI(t)
		r, err := relay.New(relay.Config{
			Token:       controllerToken,
			TLSConfig:   &tls.Config{Certificates: []tls.Certificate{pki.ServerCert()}},
			Resolver:    staticResolver{name: host},
			SandboxPort: port,
			Logf:        t.Logf,
		})
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = r.Serve(ctx, ln) }()
		rc, err := sandbox.DialRelay(ctx, ln.Addr().String(), sandbox.RelayCredentials{
			Token:     controllerToken,
			TLSConfig: &tls.Config{RootCAs: pki.Pool(), ServerName: "localhost"},
		})
		if err != nil {
			t.Fatalf("DialRelay: %v", err)
		}
		defer rc.Close()
		cli, err := rc.Client(ctx, name, sandbox.DialOptions{Token: token, TLSConfig: &tls.Config{RootCAs: pool}})
		if err != nil {
			t.Fatalf("Client through relay: %v", err)
		}
		defer cli.Close()
		vmScenarios(t, ctx, cli, r.DropSessions, token, host, port)
	})
}

func vmScenarios(t *testing.T, ctx context.Context, cli *sandbox.Client, drop func(), token, podIP string, port int) {
	t.Helper()
	info, err := cli.Health(ctx)
	if err != nil || !info.Ready {
		t.Fatalf("Health = %+v, %v", info, err)
	}

	var out, errOut bytes.Buffer
	code, err := cli.Exec(ctx, sandbox.ExecRequest{Line: "cat; echo to-stderr >&2; uname -s; exit 3"}, strings.NewReader("from-stdin\n"), &out, &errOut)
	if err != nil || code != 3 || out.String() != "from-stdin\nLinux\n" || errOut.String() != "to-stderr\n" {
		t.Fatalf("Exec = %d, %v, stdout %q, stderr %q", code, err, out.String(), errOut.String())
	}

	if err := cli.PutFiles(ctx, []sandbox.FileUpload{
		{Path: "/tmp/access-e2e/a.txt", Content: []byte("alpha\n"), Mode: 0o644},
		{Path: "/tmp/access-e2e/bin/b.sh", Content: []byte("#!/bin/sh\necho beta\n"), Mode: 0o755},
	}); err != nil {
		t.Fatalf("PutFiles: %v", err)
	}
	out.Reset()
	if code, err := cli.Exec(ctx, sandbox.ExecRequest{Line: "cat /tmp/access-e2e/a.txt && /tmp/access-e2e/bin/b.sh"}, nil, &out, io.Discard); err != nil || code != 0 || out.String() != "alpha\nbeta\n" {
		t.Fatalf("reading put files = %d, %v, %q", code, err, out.String())
	}

	// Reattach to an in-flight command after the transport drops.
	proc, err := cli.StartExec(ctx, sandbox.ExecRequest{Line: "echo phase-1; sleep 2; echo phase-2; exit 42"})
	if err != nil {
		t.Fatalf("StartExec: %v", err)
	}
	_ = proc.Stdin().Close()
	var combined syncBuffer
	first := make(chan struct{})
	var once sync.Once
	w := writerFunc(func(p []byte) (int, error) {
		n, err := combined.Write(p)
		if strings.Contains(combined.String(), "phase-1\n") {
			once.Do(func() { close(first) })
		}
		return n, err
	})
	done := make(chan error, 1)
	var got int64
	go func() {
		_, n, err := proc.Wait(w, io.Discard)
		got = n
		done <- err
	}()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("no phase-1 output")
	}
	drop()
	if err := <-done; !errors.Is(err, sandbox.ErrTransportCut) {
		t.Fatalf("Wait after drop = %v, want ErrTransportCut", err)
	}
	code, err = cli.Reattach(ctx, proc.ID(), got, w, io.Discard)
	if err != nil || code != 42 || combined.String() != "phase-1\nphase-2\n" {
		t.Fatalf("Reattach = %d, %v, output %q", code, err, combined.String())
	}

	// The guest workload cannot see the token: not on its kernel command
	// line, in any process's environment, or in its files.
	out.Reset()
	if _, err := cli.Exec(ctx, sandbox.ExecRequest{User: "root", Line: `cat /proc/cmdline; for f in /proc/[0-9]*/environ; do tr '\0' '\n' <"$f"; done 2>/dev/null; true`}, nil, &out, io.Discard); err != nil {
		t.Fatalf("guest token scan: %v", err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("the per-sandbox token is visible inside the guest:\n%s", out.String())
	}
	out.Reset()
	if _, err := cli.Exec(ctx, sandbox.ExecRequest{User: "root", Line: fmt.Sprintf(`grep -rIl -- %q /etc /run /tmp /root /home 2>/dev/null; true`, token)}, nil, &out, io.Discard); err != nil {
		t.Fatalf("guest file scan: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("the per-sandbox token is in guest files: %s", out.String())
	}

	// The guest cannot reach the access listener: the pod IP is the
	// guest's own, so a connection to it stays inside the guest, where
	// nothing listens on the access port.
	out.Reset()
	probe := fmt.Sprintf(`if command -v nc >/dev/null; then if echo | nc -w 3 %s %d >/dev/null 2>&1; then echo CONNECTED; else echo REFUSED; fi; else echo NO-NC; fi`, podIP, port)
	if _, err := cli.Exec(ctx, sandbox.ExecRequest{Line: probe}, nil, &out, io.Discard); err != nil {
		t.Fatalf("guest probe: %v", err)
	}
	switch strings.TrimSpace(out.String()) {
	case "REFUSED":
	case "NO-NC":
		t.Log("guest has no nc; skipped the guest-to-listener probe")
	default:
		t.Fatalf("guest connection to %s:%d = %q, want REFUSED", podIP, port, out.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
