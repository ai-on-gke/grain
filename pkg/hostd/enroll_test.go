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

package hostd_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func TestPrivateKeyPermissionsAndPairingCode(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "hostd.key")

	priv, err := hostd.GenerateOrLoadPrivateKey(keyPath)
	if err != nil {
		t.Fatalf("GenerateOrLoadPrivateKey: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %04o, want 0600", info.Mode().Perm())
	}

	// Reloading returns the same keypair.
	priv2, err := hostd.GenerateOrLoadPrivateKey(keyPath)
	if err != nil || !priv.Equal(priv2) {
		t.Fatalf("reload mismatch: %v", err)
	}

	// Loosening permissions to 0644 is refused.
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hostd.LoadPrivateKey(keyPath); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("LoadPrivateKey(0644) = %v, want 0600 error", err)
	}
	_ = os.Chmod(keyPath, 0o600)

	// Symlinks are refused.
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(keyPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := hostd.LoadPrivateKey(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadPrivateKey(symlink) = %v, want symlink error", err)
	}

	pub := priv.Public().(ed25519.PublicKey)
	pubStr := hostd.PublicKeyString(pub)
	code1 := hostd.DerivePairingCode("hjr_123", "nonce_abc", pubStr, "MyHost.local")
	code2 := hostd.DerivePairingCode("hjr_123", "nonce_abc", pubStr, "myhost.local")
	if code1 != code2 || len(code1) != 9 || code1[4] != '-' {
		t.Fatalf("DerivePairingCode = %q / %q", code1, code2)
	}
	if diff := hostd.DerivePairingCode("hjr_123", "nonce_xyz", pubStr, "myhost.local"); diff == code1 {
		t.Fatalf("DerivePairingCode did not change with nonce: %q", diff)
	}
}

func TestKeyBoundConnectRejectsCopiedTokenAndRenewsSilently(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubStr := hostd.PublicKeyString(pub)

	var tokMu sync.Mutex
	validTokens := map[string]hostd.ResolvedCredential{
		"ghd_initial_valid_token": {
			Name:      "ws-keybound",
			Owner:     "alice@example.com",
			PublicKey: pubStr,
		},
		"ghd_legacy_token": {
			Name:  "ws-legacy",
			Owner: "bob@example.com",
		},
	}

	remote := &hostd.Remote{
		ResolveCredential: func(_ context.Context, tok string) (hostd.ResolvedCredential, bool, error) {
			tokMu.Lock()
			defer tokMu.Unlock()
			c, ok := validTokens[tok]
			return c, ok, nil
		},
		Renew: func(_ context.Context, name, curTok string) (string, time.Time, error) {
			tokMu.Lock()
			defer tokMu.Unlock()
			next := "ghd_renewed_valid_token"
			exp := time.Now().UTC().Add(30 * 24 * time.Hour)
			validTokens[next] = hostd.ResolvedCredential{
				Name:      name,
				Owner:     "alice@example.com",
				PublicKey: pubStr,
				ExpiresAt: exp,
			}
			delete(validTokens, curTok)
			return next, exp, nil
		},
	}

	srv := httptest.NewServer(remote)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Copied token WITHOUT private key must fail.
	if conn, err := (hostd.Dialer{Server: srv.URL, Token: "ghd_initial_valid_token", Host: "box"}).Dial(ctx); err == nil {
		_ = conn.Close()
		t.Fatal("copied key-bound token connected without private key")
	}

	// 2. Copied token WITH wrong private key must fail.
	if conn, err := (hostd.Dialer{Server: srv.URL, Token: "ghd_initial_valid_token", PrivateKey: wrongPriv, Host: "box"}).Dial(ctx); err == nil {
		_ = conn.Close()
		t.Fatal("key-bound token connected with wrong private key")
	}

	// 3. Replayed signed HTTP request headers must fail.
	req, err := http.NewRequest(http.MethodGet, srv.URL+hostd.RemotePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := hostd.SetConnectHeaders(req, "ghd_initial_valid_token", priv, now); err != nil {
		t.Fatal(err)
	}
	var replay hostd.NonceReplayCache
	if err := hostd.VerifyConnectHeaders(req, "ghd_initial_valid_token", pubStr, now, &replay); err != nil {
		t.Fatalf("first VerifyConnectHeaders failed: %v", err)
	}
	if err := hostd.VerifyConnectHeaders(req, "ghd_initial_valid_token", pubStr, now, &replay); err == nil {
		t.Fatal("second VerifyConnectHeaders accepted a replayed nonce")
	}

	// 4. Legacy per-host token (PublicKey == "") keeps working without key headers.
	legacyConn, err := (hostd.Dialer{Server: srv.URL, Token: "ghd_legacy_token", Host: "legacy-box"}).Dial(ctx)
	if err != nil {
		t.Fatalf("legacy token dial failed: %v", err)
	}
	_ = legacyConn.Close()

	// 5. Key-bound Server connects, silently renews in-band via OpRenew, and drops immediately on Disconnect.
	dir := t.TempDir()
	allowPath := filepath.Join(dir, "allow.yml")
	if err := os.WriteFile(allowPath, []byte("clis: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var curMu sync.Mutex
	curTok := "ghd_initial_valid_token"
	renewedCh := make(chan string, 1)

	dialer := hostd.Dialer{
		Server:     srv.URL,
		PrivateKey: priv,
		Host:       "alice-box",
		TokenFunc: func() string {
			curMu.Lock()
			defer curMu.Unlock()
			return curTok
		},
	}
	hServer := &hostd.Server{
		Allowlist:       allowPath,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Remote:          dialer.Dial,
		RenewKey:        priv,
		RenewInterval:   40 * time.Millisecond,
		CurrentToken:    dialer.TokenFunc,
		OnTokenRenewed: func(tok string, _ time.Time) {
			curMu.Lock()
			curTok = tok
			curMu.Unlock()
			select {
			case renewedCh <- tok:
			default:
			}
		},
	}

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	go func() { _ = hServer.Serve(serveCtx) }()

	if err := remote.WaitHostd(ctx, "ws-keybound"); err != nil {
		t.Fatalf("WaitHostd: %v", err)
	}
	select {
	case got := <-renewedCh:
		if got != "ghd_renewed_valid_token" {
			t.Fatalf("renewed token = %q, want ghd_renewed_valid_token", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for silent in-band OpRenew")
	}

	// Revoking/dropping via Disconnect drops the active connection immediately
	// (and stopServe stops the reconnect loop).
	stopServe()
	remote.Disconnect("ws-keybound")
	deadline := time.Now().Add(2 * time.Second)
	for remote.IsConnected("ws-keybound") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if remote.IsConnected("ws-keybound") {
		t.Fatal("ws-keybound remained connected after Disconnect")
	}
}

// POST /api/hostd/join records a nonce for any self-signed request before
// its enrollment code is checked, so the replay cache an anonymous caller
// can write to must be bounded: once it holds 1<<16 live nonces it refuses
// new ones (failing closed), and it takes them again once those expire.
func TestNonceReplayCacheIsBounded(t *testing.T) {
	var c hostd.NonceReplayCache
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	const limit = 1 << 16
	for i := 0; i < limit; i++ {
		if !c.CheckAndRecord("join:k", fmt.Sprintf("nonce-%016d", i), now, time.Minute) {
			t.Fatalf("nonce %d refused below the bound", i)
		}
	}
	if c.CheckAndRecord("join:k", "one-past-the-bound", now.Add(time.Second), time.Minute) {
		t.Fatal("a full cache of live nonces took another")
	}
	if c.CheckAndRecord("join:k", fmt.Sprintf("nonce-%016d", 7), now.Add(time.Second), time.Minute) {
		t.Fatal("a replayed nonce was taken")
	}
	later := now.Add(2 * time.Minute)
	if !c.CheckAndRecord("join:k", "after-expiry", later, time.Minute) {
		t.Fatal("the cache did not take a new nonce once the old ones expired")
	}
}

// A session dialed in on a legacy per-host token has no bound key, so
// OpRenew over it is refused before the controller's Renew is reached, even
// when the host signs the proof with some key of its own.
func TestInBandRenewIsRefusedOnALegacySession(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var renewMu sync.Mutex
	renewCalls := 0
	remote := &hostd.Remote{
		ResolveCredential: func(_ context.Context, tok string) (hostd.ResolvedCredential, bool, error) {
			if tok != "ghd_legacy_token" {
				return hostd.ResolvedCredential{}, false, nil
			}
			return hostd.ResolvedCredential{Name: "ws-legacy", Owner: "bob@example.com"}, true, nil
		},
		Renew: func(_ context.Context, name, curTok string) (string, time.Time, error) {
			renewMu.Lock()
			renewCalls++
			renewMu.Unlock()
			return "ghd_should_not_be_minted", time.Now().Add(time.Hour), nil
		},
	}
	srv := httptest.NewServer(remote)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()
	allowPath := filepath.Join(dir, "allow.yml")
	if err := os.WriteFile(allowPath, []byte("clis: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dialer := hostd.Dialer{Server: srv.URL, Token: "ghd_legacy_token", Host: "legacy-box"}
	renewed := make(chan string, 1)
	hServer := &hostd.Server{
		Allowlist:       allowPath,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Remote:          dialer.Dial,
		RenewKey:        hostPriv,
		RenewInterval:   20 * time.Millisecond,
		CurrentToken:    func() string { return "ghd_legacy_token" },
		OnTokenRenewed: func(tok string, _ time.Time) {
			select {
			case renewed <- tok:
			default:
			}
		},
	}
	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	go func() { _ = hServer.Serve(serveCtx) }()

	if err := remote.WaitHostd(ctx, "ws-legacy"); err != nil {
		t.Fatalf("WaitHostd: %v", err)
	}
	select {
	case tok := <-renewed:
		t.Fatalf("a legacy session renewed its credential in-band: %q", tok)
	case <-time.After(400 * time.Millisecond):
	}
	renewMu.Lock()
	defer renewMu.Unlock()
	if renewCalls != 0 {
		t.Fatalf("Remote.Renew called %d times for a legacy session", renewCalls)
	}
}
