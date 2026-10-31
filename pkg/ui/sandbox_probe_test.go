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

// POST /api/sandbox-probe: what `grain sandbox probe`, and through it
// scripts/deploy-gke.sh --check, asks the daemon for. The backend that
// builds the sandbox is pkg/daemon's; these tests stand a fake in its
// place, the way sandbox_recreate_test.go does for its neighbour.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type fakeSandboxProber struct {
	result ui.SandboxProbeResult
	err    error
	asked  []ui.SandboxProbeRequest
}

func (f *fakeSandboxProber) ProbeSandbox(_ context.Context, req ui.SandboxProbeRequest) (ui.SandboxProbeResult, error) {
	f.asked = append(f.asked, req)
	return f.result, f.err
}

func sandboxProbeClient(t *testing.T, prober ui.SandboxProber) *ui.Client {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	cfg := ui.Config{Actor: ui.DefaultActor("alice")}
	if prober != nil {
		cfg.SandboxProbe = prober
	}
	return ui.NewClient(cfg, store)
}

func TestHTTPClientSandboxProbeRoundTrip(t *testing.T) {
	t.Parallel()
	prober := &fakeSandboxProber{result: ui.SandboxProbeResult{
		Sandbox: "probe-gke-check-1", Pod: "grain-granule-probe-gke-check-1", Access: "dialout",
		Stdout: "dialout-ok:gke-check-1\n",
	}}
	client := sandboxProbeClient(t, prober)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	req := ui.SandboxProbeRequest{ID: "gke-check-1", Access: "dialout", Argv: []string{"echo", "dialout-ok:gke-check-1"}}
	got, err := accessCodeClient(client, srv.URL).ProbeSandbox(context.Background(), req)
	if err != nil {
		t.Fatalf("ProbeSandbox over HTTP: %v", err)
	}
	if got != prober.result {
		t.Fatalf("result = %+v, want %+v", got, prober.result)
	}
	if len(prober.asked) != 1 || !reflect.DeepEqual(prober.asked[0], req) {
		t.Fatalf("prober asked %+v, want exactly %+v", prober.asked, req)
	}
}

func TestHTTPClientSandboxProbeReportsARefusal(t *testing.T) {
	t.Parallel()
	prober := &fakeSandboxProber{err: errors.New(`this deployment reaches sandboxes over "kubectl"`)}
	client := sandboxProbeClient(t, prober)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	_, err := accessCodeClient(client, srv.URL).ProbeSandbox(context.Background(), ui.SandboxProbeRequest{ID: "x", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), `over "kubectl"`) {
		t.Fatalf("err = %v, want the backend's own refusal", err)
	}
}

func TestSandboxProbeUnavailableWithoutAProber(t *testing.T) {
	t.Parallel()
	client := sandboxProbeClient(t, nil)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	_, err := accessCodeClient(client, srv.URL).ProbeSandbox(context.Background(), ui.SandboxProbeRequest{ID: "x", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want the 404's explanation", err)
	}
}

// scripts/deploy-gke.sh --check reaches the daemon from inside its own
// pod, over loopback. Loopback is not trusted on its own: the check has
// to present the local API token, which the grain CLI does when
// GRAIN_DATA_DIR names the daemon's data directory. A bare request --
// what the check used to make with wget -- is refused.
func TestLoopbackWithoutTheLocalAPITokenIsRefused(t *testing.T) {
	t.Parallel()
	client := sandboxProbeClient(t, &fakeSandboxProber{})
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/executors"},
		{http.MethodPost, "/api/sandbox-probe"},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(`{"id":"x","argv":["true"]}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s over loopback with no token: status %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}
