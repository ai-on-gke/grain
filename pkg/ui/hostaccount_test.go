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

//go:build linux

package ui_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// serveAPISocket serves srv on a unix socket the way the daemon does
// (ListenAPISocket, HostAccountConnContext, HostAccountHandler) with
// accounts as its map, and returns a client that dials it.
func serveAPISocket(t *testing.T, srv *ui.Server, accounts ui.HostAccounts) *http.Client {
	t.Helper()
	// Short: a socket's path has to fit in sun_path's 108 bytes.
	dir, err := os.MkdirTemp("", "gsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := ui.APISocketPath(dir)
	ln, err := ui.ListenAPISocket(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory %s: %v, %v; want mode 0700", filepath.Dir(path), fi.Mode(), err)
	}
	hs := &http.Server{
		Handler:     srv.HostAccountHandler(func() ui.HostAccounts { return accounts }),
		ConnContext: ui.HostAccountConnContext,
	}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func socketDo(t *testing.T, c *http.Client, method, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://grain"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func pausedBy(t *testing.T, srv *ui.Server) ui.PipelinePauseStatus {
	t.Helper()
	rec := serveFrom(srv, "", http.MethodGet, "/api/pipeline-pause", "", nil)
	var got struct {
		Pause ui.PipelinePauseStatus `json:"pause"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/pipeline-pause: %v (%s)", err, rec.Body)
	}
	return got.Pause
}

// A request on the socket is recorded as the person its uid maps to,
// with a source of its own -- not as the access code, even when it sends
// one, and not as the deployment's in-process actor.
func TestAPISocketRequestIsRecordedAsTheMappedOwner(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	client.Config.DeploymentOwner = "alice@example.com"
	uid := uint32(os.Getuid())
	c := serveAPISocket(t, srv, ui.HostAccounts{}.WithDaemonAccount(uid, "alice@example.com"))

	code, body := socketDo(t, c, http.MethodPost, "/api/pipeline-pause", map[string]string{
		"Authorization": "Bearer " + testAccessCode, ui.AccessNameHeader: "ops@build-host",
	})
	if code/100 != 2 {
		t.Fatalf("POST /api/pipeline-pause on the socket: %d %s", code, body)
	}
	got := pausedBy(t, srv)
	if !got.Paused || got.ByID != "alice@example.com" || got.BySource != string(model.SourceHostAccount) {
		t.Fatalf("pause recorded as %s (%s); want alice@example.com (%s)", got.ByID, got.BySource, model.SourceHostAccount)
	}

	code, body = socketDo(t, c, http.MethodGet, "/api/auth/whoami", nil)
	if code != http.StatusOK || !strings.Contains(body, `"principal":"human:alice@example.com"`) {
		t.Fatalf("GET /api/auth/whoami on the socket: %d %s", code, body)
	}
}

// A uid the map names nobody for is refused, whatever it presents --
// the access code included.
func TestAPISocketRefusesAnUnmappedUID(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	client.Config.DeploymentOwner = "alice@example.com"
	other := uint32(os.Getuid()) + 1
	c := serveAPISocket(t, srv, ui.HostAccounts{other: "bob@example.com"})

	for _, h := range []map[string]string{
		nil,
		{"Authorization": "Bearer " + testAccessCode},
		{ui.AccessCodeHeader: testAccessCode},
	} {
		code, body := socketDo(t, c, http.MethodPost, "/api/pipeline-pause", h)
		if code != http.StatusForbidden || !strings.Contains(body, "-host-accounts") {
			t.Fatalf("POST /api/pipeline-pause from an unmapped uid with %v: %d %s; want 403 naming -host-accounts", h, code, body)
		}
	}
	if got := pausedBy(t, srv); got.Paused {
		t.Fatalf("an unmapped uid paused the pipeline: %+v", got)
	}
}

// Nothing on the socket is trusted without a mapping: with no map at all
// even the open routes are refused, and a mapped person the allowlist
// does not admit is not let in as the deployment's own actor by the code
// it sends or by the socket's lack of a network address.
func TestAPISocketTrustsNothingWithoutAMapping(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ui.SetAccessCodeForTest(srv, testAccessCode)

	none := serveAPISocket(t, srv, nil)
	for _, p := range []string{"/api/config", "/api/auth/whoami", "/api/pipeline-pause"} {
		if code, body := socketDo(t, none, http.MethodGet, p, map[string]string{"Authorization": "Bearer " + testAccessCode}); code != http.StatusForbidden {
			t.Fatalf("GET %s on a socket with no map: %d %s; want 403", p, code, body)
		}
	}

	// Mapped, but to somebody no allowlist entry (and no
	// -deployment-owner) admits.
	stranger := serveAPISocket(t, srv, ui.HostAccounts{uint32(os.Getuid()): "mallory@example.com"})
	code, body := socketDo(t, stranger, http.MethodPost, "/api/pipeline-pause", map[string]string{
		"Authorization": "Bearer " + testAccessCode, ui.AccessCodeHeader: testAccessCode,
	})
	if code/100 == 2 {
		t.Fatalf("POST /api/pipeline-pause as a person the allowlist does not admit: %d %s; want a refusal", code, body)
	}
	if got := pausedBy(t, srv); got.Paused {
		t.Fatalf("a socket caller the allowlist does not admit paused the pipeline: %+v", got)
	}
}

func TestParseHostAccounts(t *testing.T) {
	got, err := ui.ParseHostAccounts(" 1000=Alice@Example.com, 1001=user:bob@example.com ,")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "1000=alice@example.com,1001=bob@example.com" {
		t.Fatalf("ParseHostAccounts = %s", got)
	}
	for _, bad := range []string{"alice@example.com", "x=alice@example.com", "1000=alice", "1000=a@example.com,1000=b@example.com"} {
		if _, err := ui.ParseHostAccounts(bad); err == nil {
			t.Errorf("ParseHostAccounts(%q) accepted it", bad)
		}
	}
	m := ui.HostAccounts{7: "bob@example.com"}.WithDaemonAccount(7, "alice@example.com").WithDaemonAccount(8, "alice@example.com")
	if m.String() != "7=bob@example.com,8=alice@example.com" {
		t.Fatalf("WithDaemonAccount = %s; an explicit mapping should win", m)
	}
}
