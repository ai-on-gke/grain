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

package uiapi

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/demo/demotest"
)

const png1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

var hardeningHeaders = map[string]string{
	"X-Frame-Options":        "DENY",
	"X-Content-Type-Options": "nosniff",
	"Referrer-Policy":        "same-origin",
}

// ported from ui/e2e/security-perimeter-walkthrough.spec.js:131, with
// the beforeAll's task and its PNG attachment.
func TestClassic_SecurityPerimeter_EveryResponseClassCarriesTheSecurityHeaders(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	id := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title": "Perimeter walkthrough",
		"repo":  srv.Seed.Repo,
		"attachments": []map[string]any{
			{"filename": "dot.png", "contentType": "image/png", "content": png1x1},
		},
	}).WantStatus(t, http.StatusCreated).ID(t)
	var detail struct {
		Attachments []struct {
			ID       json.Number `json:"id"`
			Filename string      `json:"filename"`
		} `json:"attachments"`
	}
	srv.Do(t, http.MethodGet, "/api/tasks/"+id, nil).WantOK(t).JSON(t, &detail)
	if len(detail.Attachments) != 1 {
		t.Fatalf("task %s has attachments %+v, want dot.png", id, detail.Attachments)
	}
	attachment := "/api/tasks/" + id + "/attachments/" + detail.Attachments[0].ID.String()

	for _, path := range []string{"/", "/api/config", "/api/no-such-route", attachment} {
		res := srv.Do(t, http.MethodGet, path, nil)
		csp := res.Header.Get("Content-Security-Policy")
		if path == attachment {
			// An attachment is a document of its own, held to a stricter
			// policy than the UI's: nothing loads, nothing runs.
			for _, want := range []string{"default-src 'none'", "sandbox"} {
				if !strings.Contains(csp, want) {
					t.Errorf("%s: CSP %q lacks %q", path, csp, want)
				}
			}
		} else {
			for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'"} {
				if !strings.Contains(csp, want) {
					t.Errorf("%s: CSP %q lacks %q", path, csp, want)
				}
			}
		}
		for name, want := range hardeningHeaders {
			if got := res.Header.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", path, name, got, want)
			}
		}
	}
}

// rawGet sends GET path with exactly the Host header given, which
// net/http's client would otherwise take from the URL.
func rawGet(t *testing.T, srv *demotest.Server, path, host string) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", path, host, srv.AccessCode)
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// ported from ui/e2e/security-perimeter-walkthrough.spec.js:185.
func TestClassic_SecurityPerimeter_AHostTheDaemonDoesNotAnswerToIsRefused(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	for host, want := range map[string]int{
		"evil.example":   http.StatusMisdirectedRequest,
		"localhost":      http.StatusOK,
		"localhost:8420": http.StatusOK,
	} {
		if got := rawGet(t, srv, "/api/config", host); got != want {
			t.Errorf("GET /api/config with Host %s = %d, want %d", host, got, want)
		}
	}
}

// The server half of ui/e2e/security-perimeter-walkthrough.spec.js:195:
// a cross-site write is refused, the victim stays open, and a client
// with no Origin -- the CLI, curl -- still writes. The browser half (a
// real attacker page, and the UI's own Close button) stays classic.
func TestClassic_SecurityPerimeter_ACrossSitePageCannotDriveTheDaemon(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	victim := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{"title": "CSRF victim", "repo": srv.Seed.Repo}).
		WantStatus(t, http.StatusCreated).ID(t)
	crossSite := []demotest.Header{
		{Name: "Origin", Value: "http://localhost:1"},
		{Name: "Sec-Fetch-Site", Value: "cross-site"},
		{Name: "Content-Type", Value: "text/plain"},
	}
	for _, write := range []struct{ path, body string }{
		{"/api/tasks", `{"title":"csrf","repo":"acme/widgets"}`},
		{"/api/tasks/" + victim + "/close", "{}"},
	} {
		res := srv.Do(t, http.MethodPost, write.path, write.body, crossSite...)
		if res.Status != http.StatusForbidden {
			t.Errorf("cross-site POST %s = %d %s, want 403", write.path, res.Status, res.Body)
		}
	}
	var tasks []struct{ Title string }
	srv.Do(t, http.MethodGet, "/api/tasks", nil).WantOK(t).Items(t, &tasks)
	for _, task := range tasks {
		if task.Title == "csrf" {
			t.Errorf("a cross-site POST filed a task")
		}
	}
	var state struct{ State string }
	srv.Do(t, http.MethodGet, "/api/tasks/"+victim, nil).WantOK(t).JSON(t, &state)
	if state.State == "closed" {
		t.Errorf("a cross-site POST closed the victim")
	}

	srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{"title": "Filed with no Origin", "repo": srv.Seed.Repo}).
		WantStatus(t, http.StatusCreated)
	// And the CLI, closing the victim the attacker could not.
	demotest.CLI(t, srv, "close", victim)
	srv.Do(t, http.MethodGet, "/api/tasks/"+victim, nil).WantOK(t).JSON(t, &state)
	if state.State != "closed" {
		t.Errorf("after grain close, the victim is %q", state.State)
	}
}

// ported from ui/e2e/security-perimeter-walkthrough.spec.js:286.
func TestClassic_SecurityPerimeter_OversizedBodiesAre413AndTheLargestAttachmentIsAccepted(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	nine := strings.Repeat("x", 9<<20)
	srv.Do(t, http.MethodPut, "/api/settings", map[string]any{"description": nine}).
		WantStatus(t, http.StatusRequestEntityTooLarge)
	srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{"title": "t", "repo": srv.Seed.Repo, "description": nine}).
		WantStatus(t, http.StatusRequestEntityTooLarge)

	max := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 10<<20))
	srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title": "max attachment",
		"repo":  srv.Seed.Repo,
		"attachments": []map[string]any{
			{"filename": "max.bin", "contentType": "application/octet-stream", "content": max},
		},
	}).WantStatus(t, http.StatusCreated)
}
