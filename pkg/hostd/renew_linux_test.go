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
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// renewHarness is a controller that knows one key-bound host, ws-renew,
// and mints a fresh token on every in-band renewal. A token it has
// superseded still answers, as the store's does for hostdRenewGrace
// (model.RenewHostdCredential): a session can drop after the controller
// has renewed but before the reply reaches the host, which then
// reconnects -- and renews -- with the token it still holds. Revoking
// that token at once instead shut the host out for good with "does not
// take that host token", which no real controller does, and failed
// TestSilentRenewalSurvivesReconnectsShorterThanTheInterval whenever a
// drop landed in that gap.
type renewHarness struct {
	remote  *hostd.Remote
	url     string
	priv    ed25519.PrivateKey
	mu      sync.Mutex
	current string
	issued  map[string]bool
	renews  int
}

func newRenewHarness(t *testing.T) *renewHarness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubStr := hostd.PublicKeyString(pub)
	h := &renewHarness{priv: priv, current: "ghd_renew_0", issued: map[string]bool{"ghd_renew_0": true}}
	h.remote = &hostd.Remote{
		ResolveCredential: func(_ context.Context, tok string) (hostd.ResolvedCredential, bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if !h.issued[tok] {
				return hostd.ResolvedCredential{}, false, nil
			}
			return hostd.ResolvedCredential{Name: "ws-renew", Owner: "alice@example.com", PublicKey: pubStr}, true, nil
		},
		Renew: func(_ context.Context, name, curTok string) (string, time.Time, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if !h.issued[curTok] {
				return "", time.Time{}, fmt.Errorf("unknown token %q", curTok)
			}
			h.renews++
			h.current = fmt.Sprintf("ghd_renew_%d", h.renews)
			h.issued[h.current] = true
			return h.current, time.Now().Add(30 * 24 * time.Hour), nil
		},
	}
	srv := httptest.NewServer(h.remote)
	t.Cleanup(srv.Close)
	h.url = srv.URL
	return h
}

func (h *renewHarness) token() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

// server is a key-bound host agent for h, renewing every interval.
func (h *renewHarness) server(t *testing.T, interval time.Duration, renewedAt time.Time, renewed chan<- string) *hostd.Server {
	t.Helper()
	dir := t.TempDir()
	allowPath := filepath.Join(dir, "allow.yml")
	if err := os.WriteFile(allowPath, []byte("clis: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	tok := h.token()
	cur := func() string {
		mu.Lock()
		defer mu.Unlock()
		return tok
	}
	dialer := hostd.Dialer{Server: h.url, PrivateKey: h.priv, Host: "renew-box", TokenFunc: cur}
	return &hostd.Server{
		Allowlist:        allowPath,
		AllowlistOwners:  []uint32{uint32(os.Getuid())},
		StateDir:         filepath.Join(dir, "state"),
		Remote:           dialer.Dial,
		RemoteMinBackoff: 5 * time.Millisecond,
		RemoteMaxBackoff: 10 * time.Millisecond,
		RenewKey:         h.priv,
		RenewInterval:    interval,
		RenewedAt:        renewedAt,
		CurrentToken:     cur,
		OnTokenRenewed: func(next string, _ time.Time) {
			mu.Lock()
			tok = next
			mu.Unlock()
			select {
			case renewed <- next:
			default:
			}
		},
	}
}

// A host whose connection drops more often than its renewal interval still
// renews. The schedule used to be a ticker started afresh on every
// connection, so a host reconnected by a daemon restarted or upgraded
// more often than every 12 hours never renewed, and its 30-day credential
// ran out while it was connected (docs/test-runs/2026-11-07-hosts-
// enrollment-placement.md, finding 1).
func TestSilentRenewalSurvivesReconnectsShorterThanTheInterval(t *testing.T) {
	h := newRenewHarness(t)
	renewed := make(chan string, 1)
	const interval = 400 * time.Millisecond
	srv := h.server(t, interval, time.Time{}, renewed)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	if err := h.remote.WaitHostd(ctx, "ws-renew"); err != nil {
		t.Fatalf("WaitHostd: %v", err)
	}
	// Drop the connection every interval/4, the way repeated daemon
	// restarts would, for long enough that a per-connection ticker never
	// gets to fire.
	stopFlap := make(chan struct{})
	flapped := make(chan struct{})
	go func() {
		defer close(flapped)
		tick := time.NewTicker(interval / 4)
		defer tick.Stop()
		for {
			select {
			case <-stopFlap:
				return
			case <-tick.C:
				h.remote.Disconnect("ws-renew")
			}
		}
	}()
	defer func() { close(stopFlap); <-flapped }()

	// Bounded by ctx rather than by a few intervals: a ticker started
	// afresh on every connection never fires here however long it is
	// given, while a schedule that survives the drops can still take
	// several tries on a loaded runner, since each one a drop cuts short
	// is tried again an interval later.
	select {
	case tok := <-renewed:
		if tok != h.token() {
			t.Fatalf("host holds %q, controller issued %q", tok, h.token())
		}
	case <-ctx.Done():
		t.Fatalf("no silent renewal while the connection dropped every %v", interval/4)
	}
}

// A restarted host agent whose credential was issued more than an
// interval ago (RenewedAt, from its token file) renews as soon as it
// connects, rather than a full interval after it started.
func TestSilentRenewalIsDueFromWhenTheCredentialWasIssued(t *testing.T) {
	h := newRenewHarness(t)
	renewed := make(chan string, 1)
	srv := h.server(t, time.Hour, time.Now().Add(-2*time.Hour), renewed)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	select {
	case <-renewed:
	case <-ctx.Done():
		t.Fatal("a credential issued two intervals ago was not renewed on connecting")
	}

	// One issued just now is left alone until its interval is up.
	h2 := newRenewHarness(t)
	renewed2 := make(chan string, 1)
	srv2 := h2.server(t, time.Hour, time.Now(), renewed2)
	go func() { _ = srv2.Serve(ctx) }()
	if err := h2.remote.WaitHostd(ctx, "ws-renew"); err != nil {
		t.Fatalf("WaitHostd: %v", err)
	}
	select {
	case tok := <-renewed2:
		t.Fatalf("a fresh credential was renewed at once: %q", tok)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestTokenFileIssuedAt(t *testing.T) {
	if got := hostd.TokenFileIssuedAt(""); !got.IsZero() {
		t.Fatalf("TokenFileIssuedAt(\"\") = %v, want zero", got)
	}
	path := filepath.Join(t.TempDir(), "hostd-token")
	if got := hostd.TokenFileIssuedAt(path); !got.IsZero() {
		t.Fatalf("TokenFileIssuedAt(missing) = %v, want zero", got)
	}
	if err := hostd.WriteSecretFile0600(path, []byte("ghd_x\n")); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	if got := hostd.TokenFileIssuedAt(path); !got.Equal(when) {
		t.Fatalf("TokenFileIssuedAt = %v, want %v", got, when)
	}
}
