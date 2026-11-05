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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// notUpRunner is pkg/daemon's CiGate before runDaemon has published the
// CI service: wired, but not ready, and refusing to start anything.
type notUpRunner struct {
	ci.NopRunner
}

func (notUpRunner) Ready() bool { return false }

func (notUpRunner) Start(context.Context, ci.Request) (ci.Run, error) {
	return ci.Run{}, ci.ErrNotUp
}

// grain/task-1863: between a daemon's API coming up and its CI service
// coming up, GET /api/ci/runs/{id} answered 404 "remembered for a while
// and then forgotten" -- which ended every wait that was polling a run
// across the restart. It is 503 now, saying why, and reads back through
// HTTPClient as something ci.IsNotUp recognises.
func TestCIRunsAreUnavailableNotForgottenWhileCIIsNotUp(t *testing.T) {
	t.Parallel()
	srv, store := ciServer(t, notUpRunner{})

	rec := do(t, srv, http.MethodGet, "/api/ci/runs/ci-1", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want 503: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not up yet") || strings.Contains(body, "forgotten") {
		t.Errorf("GET body = %s, want it to say CI is not up yet", body)
	}

	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})
	rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status = %d, want 503: %s", rec.Code, rec.Body)
	}

	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	_, err := serverAccessClient(srv, hs.URL).CIRun(context.Background(), "ci-1")
	if !ci.IsNotUp(err) {
		t.Fatalf("HTTPClient.CIRun err = %v, want one ci.IsNotUp recognises", err)
	}
}

// The list /ci reads is not an empty page while CI is not up: an empty
// page read as "No CI runs yet" on a fresh load and blanked a table
// already on screen at its next poll, across every restart. It is the
// same 503 as a single run's, with the same sentence.
func TestCIRunListIsUnavailableNotEmptyWhileCIIsNotUp(t *testing.T) {
	t.Parallel()
	srv, _ := ciServer(t, notUpRunner{})

	for _, path := range []string{"/api/ci/runs", "/api/ci/runs?repo=acme/widgets", "/api/ci/runs?conclusion=running"} {
		rec := do(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s status = %d, want 503: %s", path, rec.Code, rec.Body)
		}
		if body := rec.Body.String(); !strings.Contains(body, "not up yet") || strings.Contains(body, `"runs"`) {
			t.Errorf("GET %s body = %s, want the not-up sentence and no run list", path, body)
		}
	}

	// Once up, the same request is the list again, empty or not.
	srv, _ = ciServer(t, &fakeCIRunner{})
	if rec := do(t, srv, http.MethodGet, "/api/ci/runs", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ci/runs on a runner that is up: status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// A runner that is up still says a run it does not have is forgotten.
func TestAnUnknownRunIsStillA404OnceCIIsUp(t *testing.T) {
	t.Parallel()
	srv, _ := ciServer(t, &fakeCIRunner{})
	if rec := do(t, srv, http.MethodGet, "/api/ci/runs/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ci.IsUp(notUpRunner{}) || !ci.IsUp(&fakeCIRunner{}) {
		t.Fatal("ci.IsUp does not read Readiness")
	}
	var _ ci.Readiness = ui.SandboxCLIActions{}
}

// queuedRunner is a ci.BackendRunner whose grit server cannot be reached:
// the start is in its outbox and goes ahead once grit is back.
type queuedRunner struct {
	ci.NopRunner
}

func (queuedRunner) Start(context.Context, ci.Request) (ci.Run, error) {
	return ci.Run{}, &ci.StartQueuedError{RequestID: "req-1", Err: errors.New("Post http://10.0.0.7:4244/v1/trees:negotiate: EOF")}
}

// The 2026-10-29 walkthrough's U4: a start queued for an unreachable grit
// was answered 400 with the transport error, though it was going to run,
// so a caller that retried got a second run. It is a 503 naming the
// queued request now -- one a waiting caller retries, which joins it --
// and the grit address the transport error carried stays out of it.
func TestAStartQueuedForGritIsUnavailableNotRefused(t *testing.T) {
	t.Parallel()
	srv, store := ciServer(t, queuedRunner{})
	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status = %d, want 503: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "queued") || !strings.Contains(body, "req-1") {
		t.Errorf("POST body = %s, want it to say the run is queued and name the request", body)
	}
	if strings.Contains(body, "10.0.0.7") {
		t.Errorf("POST body = %s, leaks the grit server's address", body)
	}
}
