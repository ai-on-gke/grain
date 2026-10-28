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

package sandboxaccess

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

const testDeployment = "dep-1"

// liveSet is grain's view of which sandboxes are live.
type liveSet struct {
	mu   sync.Mutex
	live map[string]bool
}

func (l *liveSet) set(name string, live bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live[name] = live
}

func (l *liveSet) has(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live[name]
}

func newTestAcceptor(t *testing.T, ctx context.Context, now func() time.Time) (*Acceptor, *liveSet) {
	t.Helper()
	ca, err := NewCA(testDeployment, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	live := &liveSet{live: map[string]bool{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{
		Deployment:  testDeployment,
		CA:          ca,
		ServerNames: []string{"127.0.0.1", "grain-access.grain.svc"},
		Live:        live.has,
		Now:         now,
	}, ln)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Serve(ctx) }()
	t.Cleanup(func() { _ = a.Close() })
	return a, live
}

// wrapper is granule's own wrapper-side sandbox.Server, dialling out
// exactly as granule-shim does, with the guest agent run in-process over
// a pipe instead of inside a VM -- granule's fake wrapper.
type wrapper struct {
	done chan error
}

func currentUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}

// writeCreds lays creds out as the Secret mount would, leaving out the
// files named in omit.
func writeCreds(t *testing.T, creds Credentials, omit ...string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range creds.SecretData() {
		skip := false
		for _, o := range omit {
			skip = skip || o == name
		}
		if skip {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func startWrapper(t *testing.T, ctx context.Context, a *Acceptor, sandboxID, token, dir string) *wrapper {
	t.Helper()
	srv := sandbox.NewServer(sandbox.ServerConfig{
		SandboxID: sandboxID,
		Token:     token,
		Guest:     guestexec.Config{User: currentUser(t)},
		DialGuest: func(dialCtx context.Context) (net.Conn, error) {
			c1, c2 := net.Pipe()
			go func() {
				defer c2.Close()
				_ = agent.Serve(ctx, c2)
			}()
			return c1, nil
		},
	})
	opts := sandbox.DialOutOptions{CAFile: filepath.Join(dir, KeyCA), MaxBackoff: 100 * time.Millisecond}
	if _, err := os.Stat(filepath.Join(dir, KeyCert)); err == nil {
		opts.CertFile = filepath.Join(dir, KeyCert)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyKey)); err == nil {
		opts.KeyFile = filepath.Join(dir, KeyKey)
	}
	w := &wrapper{done: make(chan error, 1)}
	go func() { w.done <- srv.DialOut(ctx, a.Addr().String(), opts) }()
	return w
}

// refused waits for a wrapper's dial-out to be turned away: granule's
// DialOut gives up with ErrAuthFailed when the acceptor answers the
// handshake with a refusal.
func (w *wrapper) refused(t *testing.T) error {
	t.Helper()
	select {
	case err := <-w.done:
		if !errors.Is(err, sandbox.ErrAuthFailed) {
			t.Fatalf("DialOut = %v, want ErrAuthFailed", err)
		}
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("wrapper was not refused")
		return nil
	}
}

// neverConnects checks sandboxID never gets a channel while the wrapper
// keeps retrying, and that the acceptor counted refusals.
func neverConnects(t *testing.T, a *Acceptor, sandboxID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := a.WaitClient(ctx, sandboxID); err == nil {
		t.Fatalf("sandbox %q connected", sandboxID)
	}
	if a.Refusals() == 0 {
		t.Fatal("acceptor counted no refusals")
	}
}

func TestAdmittedWrapperRunsExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, live := newTestAcceptor(t, ctx, nil)
	creds, err := a.Mint("sbx-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live.set("sbx-a", true)
	startWrapper(t, ctx, a, "sbx-a", creds.Token, writeCreds(t, creds))

	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	cli, err := a.WaitClient(wctx, "sbx-a")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code, err := cli.Exec(wctx, sandbox.ExecRequest{Argv: []string{"echo", "hello"}}, nil, &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("Exec = %d, %v (stderr %q)", code, err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "hello" {
		t.Fatalf("stdout = %q", got)
	}
	if got := a.Connected(); len(got) != 1 || got[0] != "sbx-a" {
		t.Fatalf("Connected = %v", got)
	}
}

func TestAnotherSandboxsTokenIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, live := newTestAcceptor(t, ctx, nil)
	credsA, err := a.Mint("sbx-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	credsB, err := a.Mint("sbx-b", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live.set("sbx-a", true)
	live.set("sbx-b", true)

	// B's own certificate with A's token.
	startWrapper(t, ctx, a, "sbx-b", credsA.Token, writeCreds(t, credsB)).refused(t)
	// A's certificate and token, claiming to be B.
	startWrapper(t, ctx, a, "sbx-b", credsA.Token, writeCreds(t, credsA)).refused(t)
	if _, ok := a.Client("sbx-b"); ok {
		t.Fatal("sbx-b got a channel")
	}
}

func TestReapedSandboxIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, live := newTestAcceptor(t, ctx, nil)

	// Valid credentials, but grain no longer counts the sandbox live.
	creds, err := a.Mint("sbx-gone", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	startWrapper(t, ctx, a, "sbx-gone", creds.Token, writeCreds(t, creds)).refused(t)

	// A connected sandbox that is then reaped loses its channel, and its
	// credentials no longer admit it.
	creds, err = a.Mint("sbx-reaped", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live.set("sbx-reaped", true)
	dir := writeCreds(t, creds)
	startWrapper(t, ctx, a, "sbx-reaped", creds.Token, dir)
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	if _, err := a.WaitClient(wctx, "sbx-reaped"); err != nil {
		t.Fatal(err)
	}
	live.set("sbx-reaped", false)
	a.Revoke("sbx-reaped")
	if _, ok := a.Client("sbx-reaped"); ok {
		t.Fatal("reaped sandbox kept its channel")
	}
	startWrapper(t, ctx, a, "sbx-reaped", creds.Token, dir).refused(t)
}

func TestExpiredCertificateIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	a, live := newTestAcceptor(t, ctx, now)
	creds, err := a.Mint("sbx-old", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live.set("sbx-old", true)
	mu.Lock()
	clock = clock.Add(2 * time.Hour)
	mu.Unlock()
	// The certificate no longer verifies, so the handshake fails before
	// any token is read, and the wrapper keeps retrying in vain.
	startWrapper(t, ctx, a, "sbx-old", creds.Token, writeCreds(t, creds))
	neverConnects(t, a, "sbx-old")
}

func TestWrapperWithoutClientKeyIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, live := newTestAcceptor(t, ctx, nil)
	creds, err := a.Mint("sbx-nokey", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live.set("sbx-nokey", true)
	// The right token and CA, but no client certificate or key: what a
	// guest that somehow reached the port would have at best.
	startWrapper(t, ctx, a, "sbx-nokey", creds.Token, writeCreds(t, creds, KeyCert, KeyKey))
	neverConnects(t, a, "sbx-nokey")
}

func TestCertificateFromAnotherCAIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, live := newTestAcceptor(t, ctx, nil)
	other, _ := newTestAcceptor(t, ctx, nil)
	creds, err := a.Mint("sbx-x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := other.Mint("sbx-x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	live.set("sbx-x", true)
	forged.Token = creds.Token
	forged.CAPEM = creds.CAPEM
	startWrapper(t, ctx, a, "sbx-x", creds.Token, writeCreds(t, forged))
	neverConnects(t, a, "sbx-x")
}

func TestRegisterReadmitsFromHashAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, live := newTestAcceptor(t, ctx, nil)
	creds, err := a.Mint("sbx-r", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// A restarted grain knows only the stored hash.
	a.Revoke("sbx-r")
	if err := a.Register("sbx-r", TokenHash(creds.Token), creds.NotAfter); err != nil {
		t.Fatal(err)
	}
	live.set("sbx-r", true)
	startWrapper(t, ctx, a, "sbx-r", creds.Token, writeCreds(t, creds))
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	if _, err := a.WaitClient(wctx, "sbx-r"); err != nil {
		t.Fatal(err)
	}
}

func TestMintCapsLifeAndBindsIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, _ := newTestAcceptor(t, ctx, nil)
	creds, err := a.Mint("sbx-long", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if life := time.Until(creds.NotAfter); life > MaxCredentialLife {
		t.Fatalf("credentials live %v, want at most %v", life, MaxCredentialLife)
	}
	if len(creds.Token) != 64 {
		t.Fatalf("token is %d hex chars, want 64 (256 bits)", len(creds.Token))
	}
	if strings.Contains(string(creds.CAPEM), "PRIVATE KEY") {
		t.Fatal("credentials carry the CA key")
	}
	if _, err := a.Mint("bad/name", time.Hour); err == nil {
		t.Fatal("Mint accepted a sandbox name with a slash")
	}
}
