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

package modelproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// vertexProxyFor is proxyFor for a connection on Vertex AI: the endpoint
// resolves target for it, the Vertex origin is up, and the Anthropic
// credential it would otherwise forward with is one no request may carry.
func vertexProxyFor(t *testing.T, up *upstream, target *VertexTarget, refusal error) (*httptest.Server, string, *[]*agent.UsageLimitError) {
	t.Helper()
	endpoint := NewEndpoint()
	endpoint.SetCredential(func(context.Context, Connection) (string, error) { return "sk-ant-never-forwarded", nil })
	endpoint.SetVertex(func(_ context.Context, c Connection) (*VertexTarget, error) {
		if c != testConnection {
			t.Errorf("resolved Vertex for %+v, want %+v", c, testConnection)
		}
		return target, refusal
	})
	var limits []*agent.UsageLimitError
	endpoint.SetPause(nil, func(_ Connection, limit *agent.UsageLimitError) { limits = append(limits, limit) })
	proxy := &Proxy{
		Vendor:         Claude,
		Endpoint:       endpoint,
		Upstream:       "http://127.0.0.1:1", // never the destination of a Vertex request
		VertexUpstream: up.server.URL,
		Transport:      up.transport(),
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	_, token, err := endpoint.Mint("run-1", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	return server, token, &limits
}

func vertexTarget() *VertexTarget {
	return &VertexTarget{Project: "proj-1", Region: "us-east5", Token: "ya29.access",
		Models: []VertexModel{{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"}, {ID: "claude-haiku-4-5"}}}
}

func callerHeaders(token string) http.Header {
	header := http.Header{}
	header.Set("x-api-key", token)
	header.Set("anthropic-version", "2023-06-01")
	header.Set("anthropic-beta", "some-beta-2025")
	header.Set("Content-Type", "application/json")
	return header
}

func bodyFields(t *testing.T, body string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("upstream body %q: %v", body, err)
	}
	return fields
}

// A Messages call is translated the way Anthropic's Vertex SDKs do it:
// the model moves into a rawPredict (or, streamed, streamRawPredict)
// path under the connection's project and region, anthropic_version is
// set in the body, and the request carries the connection's Google
// access token -- never the caller's token, and never an Anthropic key.
func TestAMessagesCallIsTranslatedForVertex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stream string
		call   string
	}{
		{"", ":rawPredict"},
		{`,"stream":false`, ":rawPredict"},
		{`,"stream":true`, ":streamRawPredict"},
	} {
		up := newUpstream(t)
		server, token, _ := vertexProxyFor(t, up, vertexTarget(), nil)
		resp := call(t, server, http.MethodPost, "/v1/messages?beta=true", callerHeaders(token),
			`{"model":"claude-sonnet-4-6","max_tokens":5,"messages":[{"role":"user","content":"hi"}]`+tc.stream+`}`)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("stream %q: %d %s", tc.stream, resp.StatusCode, body)
		}
		got := up.got
		if want := "/v1/projects/proj-1/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6" + tc.call; got.URL.Path != want {
			t.Errorf("stream %q: path %q, want %q", tc.stream, got.URL.Path, want)
		}
		if got.URL.RawQuery != "" {
			t.Errorf("stream %q: query %q reached Vertex", tc.stream, got.URL.RawQuery)
		}
		if auth := got.Header.Get("Authorization"); auth != "Bearer ya29.access" {
			t.Errorf("stream %q: Authorization %q, want the connection's access token", tc.stream, auth)
		}
		if key := got.Header.Get("x-api-key"); key != "" {
			t.Errorf("stream %q: x-api-key %q reached Vertex", tc.stream, key)
		}
		if beta := got.Header.Get("anthropic-beta"); beta != "some-beta-2025" {
			t.Errorf("stream %q: anthropic-beta %q, want the caller's", tc.stream, beta)
		}
		fields := bodyFields(t, up.gotBody)
		if _, ok := fields["model"]; ok {
			t.Errorf("stream %q: model left in the body: %s", tc.stream, up.gotBody)
		}
		if fields["anthropic_version"] != VertexAnthropicVersion || fields["max_tokens"] != float64(5) || fields["messages"] == nil {
			t.Errorf("stream %q: body %s, want the caller's with anthropic_version %s", tc.stream, up.gotBody, VertexAnthropicVersion)
		}
		if strings.Contains(up.gotBody, "sk-ant") || strings.Contains(got.Header.Get("Authorization"), token) {
			t.Errorf("stream %q: a key or the caller's token reached Vertex", tc.stream)
		}
	}
}

// A token count goes to Vertex's count-tokens model, with the model left
// in the body where Vertex reads it; an anthropic_version the caller set
// is its own.
func TestATokenCountIsTranslatedForVertex(t *testing.T) {
	t.Parallel()
	up := newUpstream(t)
	server, token, _ := vertexProxyFor(t, up, vertexTarget(), nil)
	resp := call(t, server, http.MethodPost, "/v1/messages/count_tokens", callerHeaders(token),
		`{"model":"claude-haiku-4-5","anthropic_version":"vertex-2099-01-01","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", resp.StatusCode)
	}
	if want := "/v1/projects/proj-1/locations/us-east5/publishers/anthropic/models/count-tokens:rawPredict"; up.got.URL.Path != want {
		t.Errorf("path %q, want %q", up.got.URL.Path, want)
	}
	fields := bodyFields(t, up.gotBody)
	if fields["model"] != "claude-haiku-4-5" || fields["anthropic_version"] != "vertex-2099-01-01" {
		t.Errorf("body %s, want the caller's model and anthropic_version", up.gotBody)
	}
}

// A model naming anything but a model -- a path, another method -- or a
// body that is not JSON is refused before anything reaches Vertex.
func TestAnUntranslatableVertexRequestIsRefused(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`not json`,
		`{"messages":[]}`,
		`{"model":"../../../../locations/x/endpoints/y","messages":[]}`,
		`{"model":"claude-sonnet-4-6:predict","messages":[]}`,
		`{"model":"claude/x","messages":[]}`,
	} {
		up := newUpstream(t)
		server, token, _ := vertexProxyFor(t, up, vertexTarget(), nil)
		resp := call(t, server, http.MethodPost, "/v1/messages", callerHeaders(token), body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, resp.StatusCode)
		}
		if up.got != nil {
			t.Errorf("%s: reached Vertex at %s", body, up.got.URL.Path)
		}
	}
}

// The model listing is answered here, in Anthropic's shape, from the
// target's models -- Vertex has no listing to translate.
func TestTheModelListingIsAnsweredForAVertexConnection(t *testing.T) {
	t.Parallel()
	up := newUpstream(t)
	server, token, _ := vertexProxyFor(t, up, vertexTarget(), nil)
	resp := call(t, server, http.MethodGet, "/v1/models", callerHeaders(token), "")
	var listing struct {
		Data []struct {
			Type, ID, DisplayName string
		} `json:"data"`
		HasMore bool   `json:"has_more"`
		FirstID string `json:"first_id"`
		LastID  string `json:"last_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, err)
	}
	if len(listing.Data) != 2 || listing.Data[0].ID != "claude-sonnet-4-6" || listing.Data[0].Type != "model" ||
		listing.HasMore || listing.FirstID != "claude-sonnet-4-6" || listing.LastID != "claude-haiku-4-5" {
		t.Errorf("listing = %+v", listing)
	}
	if resp := call(t, server, http.MethodGet, "/v1/models/claude-haiku-4-5", callerHeaders(token), ""); resp.StatusCode != http.StatusOK {
		t.Errorf("one model: %d", resp.StatusCode)
	}
	if resp := call(t, server, http.MethodGet, "/v1/models/gpt-5", callerHeaders(token), ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown model: %d, want 404", resp.StatusCode)
	}
	if up.got != nil {
		t.Errorf("the listing reached Vertex at %s", up.got.URL.Path)
	}
}

// Vertex's own quota error -- RESOURCE_EXHAUSTED, bare or in the
// one-element array Vertex sometimes wraps it in -- is a usage limit and
// pauses the connection; a 429 that is neither that nor Anthropic's
// usage limit is throttling, and pauses nothing.
func TestVertexQuotaExhaustionIsAUsageLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body  string
		limit bool
	}{
		{`{"error":{"code":429,"message":"Quota exceeded for aiplatform.googleapis.com/online_prediction_requests_per_base_model","status":"RESOURCE_EXHAUSTED"}}`, true},
		{`[{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED","details":[{"retryDelay":"30s"}]}}]`, true},
		{`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, false},
	} {
		up := newUpstream(t)
		up.status, up.body = http.StatusTooManyRequests, tc.body
		server, token, limits := vertexProxyFor(t, up, vertexTarget(), nil)
		resp := call(t, server, http.MethodPost, "/v1/messages", callerHeaders(token), `{"model":"claude-sonnet-4-6","messages":[]}`)
		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusTooManyRequests || string(got) != tc.body {
			t.Errorf("%s: the caller got %d %s, want Vertex's answer untouched", tc.body, resp.StatusCode, got)
		}
		if (len(*limits) == 1) != tc.limit {
			t.Errorf("%s: limits reported = %v, want a usage limit %v", tc.body, *limits, tc.limit)
		}
		if tc.limit && strings.HasPrefix(tc.body, "[") && (*limits)[0].RetryAfter != 30*time.Second {
			t.Errorf("RetryAfter = %v, want the RetryInfo's 30s", (*limits)[0].RetryAfter)
		}
	}
}

// A Vertex connection the resolver refuses is refused with its sentence,
// and nothing -- certainly not the Anthropic credential -- is forwarded.
func TestARefusedVertexConnectionForwardsNothing(t *testing.T) {
	t.Parallel()
	up := newUpstream(t)
	server, token, _ := vertexProxyFor(t, up, nil, &CredentialRefusal{Reason: "connection conn-a holds no service account key"})
	resp := call(t, server, http.MethodPost, "/v1/messages", callerHeaders(token), `{"model":"claude-sonnet-4-6"}`)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "holds no service account key") {
		t.Errorf("got %d %s, want a 502 with the refusal", resp.StatusCode, body)
	}
	if up.got != nil {
		t.Errorf("reached the upstream at %s", up.got.URL.Path)
	}
}

func TestVertexOrigin(t *testing.T) {
	if got := vertexOrigin("global"); got != "https://aiplatform.googleapis.com" {
		t.Errorf("global: %s", got)
	}
	if got := vertexOrigin("europe-west1"); got != "https://europe-west1-aiplatform.googleapis.com" {
		t.Errorf("europe-west1: %s", got)
	}
}
