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

package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// hostdOwnerHeader is the signed header the test verifier reads, as a
// proxy in front of the daemon would add it.
const hostdOwnerHeader = "X-Test-Hostd-Proxy-Assertion"

// hostdOwnerFixture is a daemon with a test identity verifier, the hostd
// registry as its hostd.Remote's resolver, and a front end that plays the
// verifying proxy: it adds the assertion (or only a forwarding header)
// the way a Gateway would before the request reaches the daemon.
type hostdOwnerFixture struct {
	remote *hostd.Remote
	store  *model.Store
	url    string
	mu     sync.Mutex
	// assert is the identity the proxy vouches for on the next dial, ""
	// for none; proxied adds X-Forwarded-For whether or not it vouches.
	assert  string
	proxied bool
}

// via sets what the proxy adds on the next dial.
func (f *hostdOwnerFixture) via(assert string, proxied bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assert, f.proxied = assert, proxied
}

func newHostdOwnerFixture(t *testing.T) *hostdOwnerFixture {
	t.Helper()
	t.Cleanup(ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
		name:     "test-proxy",
		headers:  []string{hostdOwnerHeader},
		verifyFn: parseSimpleSignedHeader,
	}))
	client, store, _ := testClient(t)
	client.Config.AllowedPrincipals = []string{
		"user:alice@example.com", "user:bob@example.com",
		"serviceAccount:hostd@proj.iam.gserviceaccount.com",
	}
	f := &hostdOwnerFixture{store: store}
	f.remote = &hostd.Remote{
		Resolve: func(ctx context.Context, token string) (string, string, bool, error) {
			h, err := store.HostdForToken(ctx, token)
			if err != nil || h == nil {
				return "", "", false, err
			}
			return h.Name, h.Owner, true, nil
		},
	}
	client.Config.HostdRemote = f.remote
	srv := ui.NewServerWithClient(client)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		assert, proxied := f.assert, f.proxied
		f.mu.Unlock()
		if assert != "" {
			r.Header.Set(hostdOwnerHeader, "ok:"+assert)
		}
		if proxied || assert != "" {
			r.Header.Set("X-Forwarded-For", "203.0.113.7")
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	f.url = ts.URL
	return f
}

// register makes hostd name with owner and returns its token.
func (f *hostdOwnerFixture) register(t *testing.T, name, owner string) string {
	t.Helper()
	_, tok, err := f.store.CreateHostd(context.Background(), model.Hostd{Name: name, Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// dial connects as hostd name with token, and reports the error, or
// waits for the session and returns the peer the daemon recorded.
func (f *hostdOwnerFixture) dial(t *testing.T, name, token string) (hostd.RemoteStatus, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := hostd.Dialer{Server: f.url, Token: token, Host: "workstation"}.Dial(ctx)
	if err != nil {
		return hostd.RemoteStatus{}, err
	}
	t.Cleanup(func() { conn.Close() })
	if err := f.remote.WaitHostd(ctx, name); err != nil {
		t.Fatal(err)
	}
	return f.remote.HostdStatus(name), nil
}

// A remote hostd -- one whose request came through a verifying proxy --
// is admitted only when its verified identity is its row's owner; one
// that comes through the proxy with no identity, or as somebody else, is
// refused whatever its token. Over loopback with no proxy headers the
// token alone names it, as before.
func TestRemoteHostdMustConnectAsItsOwner(t *testing.T) {
	f := newHostdOwnerFixture(t)
	aliceTok := f.register(t, "ws-alice", "alice@example.com")
	saTok := f.register(t, "ws-sa", "hostd@proj.iam.gserviceaccount.com")
	ownerlessTok := f.register(t, "ws-none", "")

	t.Run("matching owner is admitted", func(t *testing.T) {
		f.via("Alice@Example.com", true)
		st, err := f.dial(t, "ws-alice", aliceTok)
		if err != nil {
			t.Fatalf("dial as the owner: %v", err)
		}
		if !st.Connected || st.Owner != "alice@example.com" || st.Principal != "alice@example.com" {
			t.Fatalf("status = %+v, want connected with owner and principal alice@example.com", st)
		}
	})

	t.Run("a different verified identity is refused", func(t *testing.T) {
		f.via("bob@example.com", true)
		_, err := f.dial(t, "ws-alice", aliceTok)
		if err == nil || !strings.Contains(err.Error(), "403") ||
			!strings.Contains(err.Error(), `"bob@example.com"`) || !strings.Contains(err.Error(), `"alice@example.com"`) {
			t.Fatalf("dial as bob = %v, want a 403 naming bob and the owner alice", err)
		}
	})

	t.Run("no identity on the proxied path is refused", func(t *testing.T) {
		f.via("", true)
		_, err := f.dial(t, "ws-alice", aliceTok)
		if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "no verified identity") {
			t.Fatalf("dial through the proxy with no identity = %v, want a 401", err)
		}
	})

	t.Run("an identity the allowlist does not admit is refused", func(t *testing.T) {
		f.via("mallory@example.com", true)
		_, err := f.dial(t, "ws-alice", aliceTok)
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "mallory@example.com") {
			t.Fatalf("dial as mallory = %v, want a 403 from the allowlist", err)
		}
	})

	t.Run("a service-account owner is admitted", func(t *testing.T) {
		f.via("hostd@proj.iam.gserviceaccount.com", true)
		st, err := f.dial(t, "ws-sa", saTok)
		if err != nil {
			t.Fatalf("dial as the service account: %v", err)
		}
		if st.Principal != "hostd@proj.iam.gserviceaccount.com" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("a remote hostd with no owner is refused with how to set one", func(t *testing.T) {
		f.via("alice@example.com", true)
		_, err := f.dial(t, "ws-none", ownerlessTok)
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "has no owner") ||
			!strings.Contains(err.Error(), "set-owner ws-none") || !strings.Contains(err.Error(), "PATCH /api/hostds/ws-none") {
			t.Fatalf("dial an ownerless hostd remotely = %v, want a 403 saying how to set its owner", err)
		}
	})

	t.Run("a local hostd with the token alone is admitted", func(t *testing.T) {
		f.via("", false)
		st, err := f.dial(t, "ws-none", ownerlessTok)
		if err != nil {
			t.Fatalf("dial an ownerless hostd over loopback: %v", err)
		}
		if !st.Connected || st.Principal != "" {
			t.Fatalf("status = %+v, want connected with no principal", st)
		}
		f.via("", false)
		if _, err := f.dial(t, "ws-alice", aliceTok); err != nil {
			t.Fatalf("dial an owned hostd over loopback: %v", err)
		}
	})
}

// The deprecated shared token's hostd takes its owner from
// -hostd-token-owner (Remote.SharedOwner), and is held to it remotely.
func TestRemoteSharedTokenHostdMustConnectAsItsOwner(t *testing.T) {
	f := newHostdOwnerFixture(t)
	f.remote.Token = "shared-token"

	f.via("alice@example.com", true)
	if _, err := f.dial(t, hostd.DefaultSharedName, "shared-token"); err == nil || !strings.Contains(err.Error(), "-hostd-token-owner") {
		t.Fatalf("dial the ownerless shared hostd remotely = %v, want a refusal naming -hostd-token-owner", err)
	}
	f.remote.SharedOwner = "alice@example.com"
	if _, err := f.dial(t, hostd.DefaultSharedName, "shared-token"); err != nil {
		t.Fatalf("dial the shared hostd as its owner: %v", err)
	}
}
