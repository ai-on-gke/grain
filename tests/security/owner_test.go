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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// formerAdminRoutes are routes whose handlers asked, until task 4880,
// whether the caller was one of the deployment's system admins
// (admin_accounts) or a repository's admins. A single-tenant grain has
// one credential, the access code, and whoever holds it is the owner
// (docs/access-code.md), so every one of them answers that caller. The
// ones marked personOnly still tell a person from an agent: an agent --
// a run's MCP token, a sandbox -- is no owner, and is refused.
var formerAdminRoutes = []struct {
	method, path, body string
	personOnly         bool
}{
	{http.MethodPut, "/api/repos/acme/widgets/ci-mode", `{"ciMode":"grain"}`, true},
	// The repo admin list's own route is gone (schema 27); the settings
	// PUT an older client sent it through still takes `admins`, ignores
	// it, and is still a person's.
	{http.MethodPut, "/api/repos/acme/widgets/settings", `{"admins":["someone"],"ciMode":"grain"}`, true},
	{http.MethodPut, "/api/repos/acme/widgets/prompt-extension", `{"promptExtension":"owner's"}`, true},
	{http.MethodPost, "/api/repos/acme/widgets/branches", `{"name":"owner-branch"}`, true},
	{http.MethodPost, "/api/repos/acme/widgets/releases", `{"name":"owner-release"}`, true},
	{http.MethodPut, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", `{"value":"s3cret"}`, true},
	{http.MethodPut, "/api/memory", `{"body":"the owner's note"}`, false},
	{http.MethodGet, "/api/settings", "", false},
	{http.MethodGet, "/api/config", "", false},
}

// TestTheAccessCodeHolderIsTheOwner holds every former admin-only route
// to the access code: its session gets past the handler's own check (no
// 401 or 403) on a deployment that lists no administrators at all, a
// caller without it is refused at the gate, and an agent is refused by a
// person-only route's handler.
func TestTheAccessCodeHolderIsTheOwner(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	for _, rt := range formerAdminRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			f := newRouteFixtureFor(t, true)
			if rec := f.serve(t, rt.method, rt.path, rt.body, http.Header{}); refused(rec.code) {
				t.Errorf("the access code's session was refused: %d %s", rec.code, rec.excerpt())
			}
			if rec := f.untrusted().serve(t, rt.method, rt.path, rt.body, http.Header{}); rec.code != http.StatusUnauthorized {
				t.Errorf("a caller without the access code got %d %s, want 401", rec.code, rec.excerpt())
			}
			if !rt.personOnly {
				return
			}
			if rec := serveAsAgent(t, f, rt.method, rt.path, rt.body); rec.code != http.StatusForbidden {
				t.Errorf("an agent got %d %s, want 403: this route is a person's", rec.code, rec.excerpt())
			}
		})
	}
}

// serveAsAgent answers one in-process request -- no remote address, as
// the daemon's own MCP server makes them -- whose context names an agent
// principal: a run's token, which reaches past the access gate as the
// daemon itself does and is held to the handler's person check alone.
func serveAsAgent(t *testing.T, f *routeFixture, method, path, body string) *statusRecorder {
	t.Helper()
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}
	ctx := ui.WithPrincipal(context.Background(), agent)
	r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	r.RemoteAddr = ""
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return serveUntilAnswered(t, f.server, r)
}
