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

// POST /api/sandbox-probe: what `grain sandbox probe` asks the daemon
// for. The backend that builds the sandbox is pkg/daemon's; these tests
// stand a fake in its place, the way sandbox_recreate_test.go does for
// its neighbour.

import (
	"context"
	"errors"
	"io"
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

// A health check reaching the daemon over loopback is not trusted on
// its own: the caller has to present the local API token, which the
// grain CLI does when GRAIN_DATA_DIR names the daemon's data directory.
// A bare request without a token is refused.
func TestLoopbackWithoutTheLocalAPITokenIsRefused(t *testing.T) {
	t.Parallel()
	client := sandboxProbeClient(t, &fakeSandboxProber{})
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ method, path string }{
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

type unavailableSandboxProber struct{ fakeSandboxProber }

func (unavailableSandboxProber) SandboxProbeAvailable() bool { return false }

// GET /api/settings says whether a probe can be built here, so
// Settings -> Sandboxes offers its button only where pressing it can
// work: never without a prober, never where the prober says its backend
// cannot build one, and otherwise yes.
func TestSettingsReportWhetherASandboxProbeCanRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		prober ui.SandboxProber
		want   bool
	}{
		{"no prober", nil, false},
		{"backend cannot probe", &unavailableSandboxProber{}, false},
		{"prober", &fakeSandboxProber{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := sandboxProbeClient(t, tc.prober)
			srv := httptest.NewServer(ui.NewServerWithClient(client))
			t.Cleanup(srv.Close)
			got, err := accessCodeClient(client, srv.URL).GetSettings(context.Background())
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}
			if got.SandboxProbe != tc.want {
				t.Fatalf("sandboxProbe = %v, want %v", got.SandboxProbe, tc.want)
			}
		})
	}
}

func TestSandboxProbeUnsupportedReturns501Not500(t *testing.T) {
	t.Parallel()
	prober := &unavailableSandboxProber{
		fakeSandboxProber: fakeSandboxProber{
			err: ui.NewNotImplementedError(errors.New("sandbox probes need the Kubernetes granule sandbox backend")),
		},
	}
	client := sandboxProbeClient(t, prober)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	token := accessCodeClient(client, srv.URL).Token
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sandbox-probe", strings.NewReader(`{"id":"check","argv":["true"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/sandbox-probe: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d (%s), want 501", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "Kubernetes granule sandbox backend") {
		t.Errorf("body = %s, want the unsupported explanation", string(body))
	}
}

func TestSandboxProbeRejectsMalformedRequestWith400(t *testing.T) {
	t.Parallel()
	prober := &fakeSandboxProber{}
	client := sandboxProbeClient(t, prober)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	token := accessCodeClient(client, srv.URL).Token
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"empty id", `{"id":"","argv":["true"]}`, "probe id is required"},
		{"empty argv", `{"id":"check","argv":[]}`, "command to run"},
		{"unknown access", `{"id":"check","argv":["true"],"access":"ssh"}`, "unknown access mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sandbox-probe", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /api/sandbox-probe: %v", err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d (%s), want 400", resp.StatusCode, string(raw))
			}
			if !strings.Contains(string(raw), tc.want) {
				t.Errorf("body = %s, want %q", string(raw), tc.want)
			}
		})
	}
	if len(prober.asked) != 0 {
		t.Errorf("prober was called %d times on malformed requests, want 0", len(prober.asked))
	}
}
