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

package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Loopback is not one person. A deployment that onboards people through
// SSH port-forwards to localhost hands every one of them a loopback
// address, so two things have to hold (grain/task-2078):
//
//   - a request with no verified principal, trusted only for where it
//     came from, acts as the deployment's own actor and nobody else: a
//     "user" (or "creator", or "author") it names is never who a
//     permission check asks about;
//   - loopback is no trusted network: such a request presents the
//     access code -- while a signed-in session, an in-process request
//     and the CLI on the host (through the local API token only the
//     daemon's account can read) keep working.

const loopbackAddr = "127.0.0.1:4242"

// loopbackFixture is a ui.Server over a store with a system admin
// (rootUser) and a team only leadUser administers, built from cfg.
func loopbackFixture(t *testing.T, cfg ui.Config) (*ui.Server, *model.Store) {
	t.Helper()
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	conf, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil {
		d := model.NewDeploymentConfig()
		conf = &d
	}
	if err := store.PutConfig(ctx, *conf); err != nil {
		t.Fatal(err)
	}
	// The deployment's own actor: whatever it is allowed is what a
	// trusted route is allowed.
	cfg.Actor = ui.DefaultActor("operator")
	// Somewhere for the credential routes to write, so they reach their
	// check on the caller.
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Credentials = credentials
	return ui.NewServer(cfg, store), store
}

func serveFrom(srv http.Handler, remoteAddr, method, target, body string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = remoteAddr
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, values := range header {
		r.Header[name] = values
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

// The grain CLI on the host: ui.HTTPClient reads the token the daemon
// wrote (named by GRAIN_LOCAL_API_TOKEN_FILE, as the daemon exports it,
// or found under GRAIN_DATA_DIR, as setup.sh's wrapper sets it) and is
// let in over real loopback TCP. Without a readable token it is refused.
func TestTheHostCLIReachesAnUntrustedLoopbackWithTheLocalToken(t *testing.T) {
	dataDir := t.TempDir()
	path := ui.LocalAPITokenPath(dataDir)
	token, err := ui.EnsureLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("local API token file mode %v, want 0600", mode)
	}
	if again, err := ui.EnsureLocalAPIToken(path); err != nil || again != token {
		t.Errorf("EnsureLocalAPIToken rewrote an existing token: %q, %v", again, err)
	}

	srv, _ := loopbackFixture(t, ui.Config{LocalAPIToken: token})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	ctx := context.Background()
	for _, env := range []string{"GRAIN_TOKEN", "GRAIN_AUTH_TOKEN", "GRAIN_ID_TOKEN", "GRAIN_TUNNEL_TOKEN"} {
		t.Setenv(env, "")
	}

	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv("GRAIN_DATA_DIR", "")
	if _, err := ui.NewHTTPClient(ts.URL).ListTasks(ctx); err == nil {
		t.Fatal("the CLI with no local API token reached an untrusted loopback")
	}

	t.Setenv(ui.LocalAPITokenFileEnv, path)
	if _, err := ui.NewHTTPClient(ts.URL).ListTasks(ctx); err != nil {
		t.Errorf("the CLI with GRAIN_LOCAL_API_TOKEN_FILE: %v", err)
	}

	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv("GRAIN_DATA_DIR", dataDir)
	if _, err := ui.NewHTTPClient(ts.URL).ListTasks(ctx); err != nil {
		t.Errorf("the CLI with GRAIN_DATA_DIR: %v", err)
	}

	// An unreadable token is no token: a user on the host who is not the
	// daemon's account is refused like anybody else. (Root reads it
	// anyway, so this half only means something as somebody else.)
	if os.Geteuid() != 0 {
		unreadable := filepath.Join(t.TempDir(), ui.LocalAPITokenFileName)
		if err := os.WriteFile(unreadable, []byte(token), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GRAIN_DATA_DIR", "")
		t.Setenv(ui.LocalAPITokenFileEnv, unreadable)
		if _, err := ui.NewHTTPClient(ts.URL).ListTasks(ctx); err == nil {
			t.Error("the CLI reached an untrusted loopback with a token file it cannot read")
		}
	}
}
