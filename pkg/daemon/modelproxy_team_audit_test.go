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

package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A request the proxy refuses, or one that meets a usage limit, is in the
// stored audit trail -- the default team's, which is the deployment's
// since a token belongs to no team (single-tenant reset, stage 4c) --
// naming the run and the connection it was (or would have been)
// forwarded with (docs/test-runs/2026-10-14-model-proxy-team-connection.md).
// A forwarded request is recorded nowhere, as in the journal, and a
// request no token authenticated is not recorded either.
func TestModelProxyRecordsRefusalsInTheAuditTrail(t *testing.T) {
	t.Parallel()
	cfg, secretStore := twoTeams(t)
	store := teamConfigTestStore(t)
	pause := &orchestrator.Pause{}
	reset := time.Now().Add(2 * time.Hour).Unix()
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-api-alpha" {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		w.Header().Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"limit"}}`)
	}))
	t.Cleanup(vendor.Close)
	endpoint := modelproxy.NewEndpoint()
	WireModelProxy(modelproxy.Claude, endpoint, cfg, nil, secretStore, pause)
	proxy := httptest.NewServer(&modelproxy.Proxy{
		Vendor: modelproxy.Claude, Endpoint: endpoint, Upstream: vendor.URL, Audit: modelProxyAudit(store),
	})
	t.Cleanup(proxy.Close)
	alpha, beta := mintFor(t, endpoint, "alpha", ""), mintFor(t, endpoint, "beta", "beta-claude")

	for _, call := range []struct {
		token string
		want  int
	}{
		{alpha, http.StatusTooManyRequests},      // the vendor's usage limit, which pauses alpha-claude
		{alpha, http.StatusTooManyRequests},      // refused: alpha-claude is paused
		{beta, http.StatusOK},                    // forwarded, and so not audited
		{"not-a-token", http.StatusUnauthorized}, // no run to name
	} {
		if resp, body := callClaude(t, proxy, call.token); resp.StatusCode != call.want {
			t.Fatalf("got %d %s, want %d", resp.StatusCode, body, call.want)
		}
	}

	events, err := store.ListTeamAuditEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("the audit trail = %+v, want run-alpha's usage limit and its refusal", events)
	}
	// Newest first.
	for i, outcome := range []string{modelproxy.OutcomePaused, modelproxy.OutcomeUsageLimit} {
		e := events[i]
		if e.Kind != model.CredentialAccessModelProxy || e.RunID != "run-alpha" ||
			e.Credential != "alpha-claude" || e.Outcome != outcome || e.Action != http.MethodPost ||
			e.Resource != "claude /v1/messages" {
			t.Errorf("audit event %d = %+v, want a %s on alpha-claude for run-alpha", i, e, outcome)
		}
	}
}
