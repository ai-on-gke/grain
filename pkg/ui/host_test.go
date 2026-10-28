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

// GET /api/host/top: the System overlay's Top tab (grain/task-120), which
// says which processes are spending the machine GET /api/sandboxes' own
// host section only says is busy. Same enabled-flag/?lines= conventions
// logs_test.go already exercises for the Logs pane beside it.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestHostTopReportsUnavailableWithNoneConfigured(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)

	rec := do(t, srv, http.MethodGet, "/api/host/top", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}
}

func TestHostTopReturnsTheConfiguredSnapshot(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	storeSettingsRow(t, client)
	client.Config.HostTop = func(ctx context.Context, lines int) ([]string, error) {
		return []string{"top - 12:00:00 up 1 day", "  PID USER  %CPU COMMAND", " 1 root  0.3 grain"}, nil
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/host/top", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Enabled bool     `json:"enabled"`
		Lines   []string `json:"lines"`
	}](t, rec)
	if !got.Enabled {
		t.Fatalf("enabled = false, want true")
	}
	if len(got.Lines) != 3 || !strings.HasPrefix(got.Lines[0], "top - ") {
		t.Fatalf("lines = %v, want the three lines the source gave", got.Lines)
	}
}

// ?lines= is passed straight through, and an absent one is passed as 0 --
// which the source reads as its own default rather than a number this
// package picked (handleGetHostTop's own maxTopLines comment).
func TestHostTopPassesTheLineCountThrough(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	storeSettingsRow(t, client)
	asked := -1
	client.Config.HostTop = func(ctx context.Context, lines int) ([]string, error) {
		asked = lines
		return nil, nil
	}
	srv := ui.NewServerWithClient(client)

	do(t, srv, http.MethodGet, "/api/host/top", "")
	if asked != 0 {
		t.Fatalf("lines = %d with no ?lines=, want 0", asked)
	}

	do(t, srv, http.MethodGet, "/api/host/top?lines=12", "")
	if asked != 12 {
		t.Fatalf("lines = %d, want 12", asked)
	}

	do(t, srv, http.MethodGet, "/api/host/top?lines=100000", "")
	if asked != 500 {
		t.Fatalf("lines = %d, want it capped at 500", asked)
	}
}

func TestHostTopRejectsANonsenseLineCount(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	storeSettingsRow(t, client)
	client.Config.HostTop = func(ctx context.Context, lines int) ([]string, error) {
		t.Error("HostTop was called for a request that should have been rejected")
		return nil, nil
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/host/top?lines=none", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// A machine whose image has no procps is the case worth showing rather
// than swallowing: an empty pane looks like an idle machine.
func TestHostTopSurfacesTheReadersError(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	storeSettingsRow(t, client)
	client.Config.HostTop = func(ctx context.Context, lines int) ([]string, error) {
		return nil, errors.New(`top: exec: "top": executable file not found in $PATH`)
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/host/top", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "executable file not found") {
		t.Fatalf("body = %s, want the underlying error message", rec.Body)
	}
}
