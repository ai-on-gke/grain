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

// bwsalmon/agents#444: the UI's System pane reads back core system
// logs -- these cover the API surface behind it, the same
// enabled-flag/404 conventions upgrade_test.go's own settings tests and
// server_test.go's secrets tests already exercise for their own optional
// panes.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeLogSource is a pointer type so that asked survives the call: the
// count the handler settles on -- the default it substitutes, the cap it
// clamps to -- is the thing under test as much as the lines coming back.
type fakeLogSource struct {
	lines []string
	err   error
	asked int
}

func (f *fakeLogSource) Tail(ctx context.Context, n int) ([]string, error) {
	f.asked = n
	if f.err != nil {
		return nil, f.err
	}
	if n >= len(f.lines) {
		return f.lines, nil
	}
	return f.lines[len(f.lines)-n:], nil
}

func TestListLogSourcesReportsUnavailableWithNoneConfigured(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)

	rec := do(t, srv, http.MethodGet, "/api/logs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}
}

func TestListLogSourcesNamesEveryConfiguredSource(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{
		"daemon":          &fakeLogSource{lines: []string{"a"}},
		"git-proxy-audit": &fakeLogSource{lines: []string{"b"}},
	}

	rec := do(t, srv, http.MethodGet, "/api/logs", "")
	got := decode[struct {
		Enabled bool     `json:"enabled"`
		Sources []string `json:"sources"`
	}](t, rec)
	if !got.Enabled {
		t.Fatalf("enabled = false, want true")
	}
	want := []string{"daemon", "git-proxy-audit"}
	if len(got.Sources) != len(want) || got.Sources[0] != want[0] || got.Sources[1] != want[1] {
		t.Fatalf("sources = %v, want %v", got.Sources, want)
	}
}

func TestGetLogLinesReturnsTheSourcesLines(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{
		"daemon": &fakeLogSource{lines: []string{"one", "two", "three"}},
	}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon?lines=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Lines []string `json:"lines"`
	}](t, rec)
	want := []string{"two", "three"}
	if len(got.Lines) != len(want) || got.Lines[0] != want[0] || got.Lines[1] != want[1] {
		t.Fatalf("lines = %v, want %v", got.Lines, want)
	}
}

func TestGetLogLinesOfUnknownSourceIs404(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{"daemon": &fakeLogSource{lines: []string{"one"}}}

	rec := do(t, srv, http.MethodGet, "/api/logs/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestGetLogLinesRejectsANonPositiveLinesParam(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	source := &fakeLogSource{lines: []string{"one"}}
	client.Config.Logs = map[string]ui.LogSource{"daemon": source}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon?lines=0", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if source.asked != 0 {
		t.Fatalf("the source was asked for %d lines for a request that should have been rejected", source.asked)
	}
}

// A ?lines= that is not a number at all is the other half of the same
// rejection -- 0 covers the non-positive side, "none" the unparseable one.
func TestGetLogLinesRejectsAMalformedLinesParam(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	source := &fakeLogSource{lines: []string{"one"}}
	client.Config.Logs = map[string]ui.LogSource{"daemon": source}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon?lines=none", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if source.asked != 0 {
		t.Fatalf("the source was asked for %d lines for a request that should have been rejected", source.asked)
	}
}

// What the handler settles on when the caller does not say, and when the
// caller asks for more than it will carry. This endpoint deliberately
// differs from GET /api/host/top beside it (host_test.go:57): top passes
// an absent ?lines= through as 0 and lets the source pick, where logs
// substitutes defaultLogLines itself, because a journal with no bound is
// the thing being avoided rather than a screenful of processes.
func TestGetLogLinesDefaultsAndCapsTheLineCount(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	source := &fakeLogSource{lines: []string{"one", "two", "three"}}
	client.Config.Logs = map[string]ui.LogSource{"daemon": source}

	do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if source.asked != 500 {
		t.Fatalf("lines = %d with no ?lines=, want the default 500", source.asked)
	}

	do(t, srv, http.MethodGet, "/api/logs/daemon?lines=12", "")
	if source.asked != 12 {
		t.Fatalf("lines = %d, want 12", source.asked)
	}

	do(t, srv, http.MethodGet, "/api/logs/daemon?lines=100000", "")
	if source.asked != 5000 {
		t.Fatalf("lines = %d, want it capped at 5000", source.asked)
	}
}

// A journal a source cannot read is worth showing rather than swallowing:
// pkg/systemlog writes its errors to say which read failed, and this is
// the point an operator reads them at.
func TestGetLogLinesSurfacesTheSourcesError(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{
		"daemon": &fakeLogSource{err: errors.New(`journalctl -u grain.service: exit status 1: No journal files were found`)},
	}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "No journal files were found") {
		t.Fatalf("body = %s, want the underlying error message", rec.Body)
	}
}

// bwsalmon/grain#2405: the journal carries whatever anything here ever
// logged, and this route hands it to a browser. Credential shapes are cut
// on the server even with no LogRedactor configured, and a private key's
// body goes with its header rather than leaking line by line.
func TestGetLogLinesRedactsCredentials(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{"daemon": &fakeLogSource{lines: []string{
		"grain[1]: request failed: Authorization: Bearer abcdefghijklmnop1234",
		"grain[1]: cloning https://x-access-token:ghs_0123456789abcdefABCDEF@github.com/acme/w",
		"grain[1]: -----BEGIN OPENSSH PRIVATE KEY-----",
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW",
		"QyNTUxOQAAACDkeysecretmaterialgoeshere0000000000000000000000000000000000",
		"-----END OPENSSH PRIVATE KEY-----",
		"grain[1]: orchestrator: filed task 226",
	}}}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, leaked := range []string{"abcdefghijklmnop1234", "ghs_0123456789", "b3BlbnNzaC1rZXkt", "keysecretmaterial", "END OPENSSH"} {
		if strings.Contains(body, leaked) {
			t.Errorf("response carries %q unredacted: %s", leaked, body)
		}
	}
	if !strings.Contains(body, "filed task 226") {
		t.Errorf("an ordinary line was lost: %s", body)
	}
}

// A configured LogRedactor names the values this host holds, which no
// shape could recognize; and one that cannot be built refuses the read
// rather than falling back to the raw lines.
func TestGetLogLinesUsesTheConfiguredRedactorAndRefusesWithoutOne(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Logs = map[string]ui.LogSource{"daemon": &fakeLogSource{lines: []string{
		"grain[1]: webhook secret is plain-shapeless-value",
	}}}
	client.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{{Label: "secret webhook", Value: "plain-shapeless-value"}}), nil
	}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, "plain-shapeless-value") || !strings.Contains(body, "secret webhook") {
		t.Fatalf("known value was not redacted by name: %s", body)
	}

	client.Config.LogRedactor = func() (*grainlog.Redactor, error) { return nil, errors.New("store unreadable") }
	rec = do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "plain-shapeless-value") {
		t.Fatalf("a refused read still carried the line: %s", rec.Body)
	}
}

// A database upgrade's confirmation token is written to the daemon log
// so that only someone on the daemon's host can read it; the HTTP log
// viewer, which the same administrators can reach, must not show it.
func TestGetLogLinesRedactsDatabaseConfirmationTokens(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	const token = "abcdefghijklmnopqrstuvwx"
	client.Config.Logs = map[string]ui.LogSource{
		"daemon": &fakeLogSource{lines: []string{
			`ui: database upgrade requested by "admin@example.com": ...; if you made this request, resend it with "confirmationToken": "` + token + `" to proceed`,
		}},
	}

	rec := do(t, srv, http.MethodGet, "/api/logs/daemon", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, token) || !strings.Contains(body, "admin@example.com") {
		t.Fatalf("token should be redacted and the rest of the line kept: %s", body)
	}
}
