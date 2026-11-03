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

package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndpointOfNamesACallSiteRatherThanAPullRequest(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/repos/acme/widgets/pulls/1183", "/repos/{owner}/{repo}/pulls/{n}"},
		{"/repos/acme/widgets/commits/9f2a1c4e8b7d6a5f3e2d1c0b9a8f7e6d5c4b3a29/check-runs",
			"/repos/{owner}/{repo}/commits/{sha}/check-runs"},
		{"/repos/acme/widgets/actions/runs?head_sha=deadbeefdeadbeef&per_page=100",
			"/repos/{owner}/{repo}/actions/runs"},
		{"/repos/acme/widgets/branches/main", "/repos/{owner}/{repo}/branches/main"},
		{"/repos/acme/widgets", "/repos/{owner}/{repo}"},
		{"/rate_limit", "/rate_limit"},
	}
	for _, c := range cases {
		if got := endpointOf(c.path); got != c.want {
			t.Errorf("endpointOf(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// The whole point of reducing a path: two pull requests on one
// repository are one row, not two, or the counts would grow with traffic
// and answer nothing.
func TestTwoPullRequestsAreOneEndpoint(t *testing.T) {
	a := endpointOf("/repos/acme/widgets/pulls/1")
	b := endpointOf("/repos/acme/widgets/pulls/9999")
	if a != b {
		t.Errorf("%q and %q are different rows", a, b)
	}
}

// A repository whose name is all hex and long enough to look like a sha
// still has to be {repo}: the positional rule comes first, or a
// deployment working on a repository called "deadbeef" would file every
// one of its calls under a different row.
func TestARepositoryThatLooksLikeASHAIsStillTheRepository(t *testing.T) {
	if got := endpointOf("/repos/acme/deadbeef/pulls/1"); got != "/repos/{owner}/{repo}/pulls/{n}" {
		t.Errorf("endpointOf = %q", got)
	}
}

// A 304 is sent and free, and telling it apart from a 200 is the whole
// evidence that revalidation is doing anything.
// A caller that sends its own validator -- CompletedWorkflowRuns keeps an
// ETag per repository for the github_workflow_run poll -- is answered 304
// just as freely, and Health's GitHub budget must say so: until this was
// counted the poll's every unchanged read showed as spent
// (docs/test-runs/2026-11-01-github-actions-ci-workflow-run.md).
func TestA304ToACallersOwnValidatorIsRevalidatedAndFree(t *testing.T) {
	etag := `W/"runs"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer server.Close()

	transport := &RealTransport{
		Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false, Conditional: true,
		ledger: &callerLedger{logf: func(string, ...any) {}},
	}
	client := NewClient(transport, nil)
	for i := 0; i < 3; i++ {
		if _, err := WithCaller(client, "github-ci-tails").(*RESTClient).CompletedWorkflowRuns("acme", "widgets", 50); err != nil {
			t.Fatal(err)
		}
	}
	got := transport.Stats()
	if got.Sent != 3 || got.Revalidated != 2 {
		t.Errorf("transport Sent = %d Revalidated = %d, want 3 and 2", got.Sent, got.Revalidated)
	}
	c := transport.ledger.callerStats()["github-ci-tails"]
	if c.Sent != 3 || c.Revalidated != 2 {
		t.Errorf("ledger github-ci-tails = %+v, want Sent 3 Revalidated 2", c)
	}
}

func TestA304IsCountedAsRevalidatedAndFree(t *testing.T) {
	etag := `W/"abc"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4987")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"state":"open"}`))
	}))
	defer server.Close()

	transport := &RealTransport{
		Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false, Conditional: true,
	}
	path := "/repos/acme/widgets/pulls/7"
	for i := 0; i < 3; i++ {
		if _, err := transport.Request("GET", path, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	got := transport.Stats()
	if got.Sent != 3 {
		t.Errorf("Sent = %d, want 3", got.Sent)
	}
	if got.Revalidated != 2 {
		t.Errorf("Revalidated = %d, want 2 -- the first is a miss and the other two are 304s",
			got.Revalidated)
	}
	if got.Failed != 0 || got.Declined != 0 {
		t.Errorf("Failed = %d Declined = %d, want none", got.Failed, got.Declined)
	}
	e := got.Endpoints["/repos/{owner}/{repo}/pulls/{n}"]
	if e.Sent != 3 || e.Revalidated != 2 {
		t.Errorf("endpoint row = %+v", e)
	}
	if got.Limit.Limit != 5000 || got.Limit.Remaining != 4987 {
		t.Errorf("Limit = %+v, want the headers GitHub sent", got.Limit)
	}
	if got.Limit.Seen.IsZero() {
		t.Error("Limit.Seen is zero, so a reader cannot tell a real budget from no budget seen")
	}
}

// The first refusal is one GitHub made and everything behind it is one
// grain made, and the two are counted apart -- that is what lets a
// reader tell "we were throttled once" from "we are throttled now".
func TestARefusalIsCountedApartFromTheCallsItThenDeclines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(403)
		w.Write([]byte(secondaryLimitBody))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	for i := 0; i < 4; i++ {
		transport.Request("GET", fmt.Sprintf("/repos/acme/widgets/pulls/%d", i), nil, nil)
	}

	got := transport.Stats()
	if got.Sent != 1 {
		t.Errorf("Sent = %d, want 1 -- only the first call should have left the host", got.Sent)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1", got.Failed)
	}
	if got.Declined != 3 {
		t.Errorf("Declined = %d, want 3 -- the calls grain refused itself", got.Declined)
	}
	if got.Limit.Remaining != 0 || got.Limit.Limit != 5000 {
		t.Errorf("Limit = %+v, want the exhausted budget", got.Limit)
	}
}

// A response with no rate-limit headers must not overwrite the last one
// that had them with zeroes -- a reader would see an exhausted budget
// that nobody reported.
func TestAResponseWithoutHeadersLeavesTheLastBudgetStanding(t *testing.T) {
	var withHeaders bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if withHeaders {
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "4000")
		}
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	withHeaders = true
	transport.Request("GET", "/repos/acme/widgets", nil, nil)
	withHeaders = false
	transport.Request("GET", "/repos/acme/widgets", nil, nil)

	if got := transport.Stats().Limit; got.Remaining != 4000 || got.Limit != 5000 {
		t.Errorf("Limit = %+v, want the last headers that actually carried a budget", got)
	}
}

// A caller holding a snapshot must not be able to write into the
// transport's own map, and must not see the counts move underneath it.
func TestStatsIsASnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	transport.Request("GET", "/repos/acme/widgets", nil, nil)

	first := transport.Stats()
	first.Endpoints["/repos/{owner}/{repo}"] = EndpointStats{Sent: 99}
	transport.Request("GET", "/repos/acme/widgets", nil, nil)

	second := transport.Stats()
	if second.Endpoints["/repos/{owner}/{repo}"].Sent != 2 {
		t.Errorf("the transport's own counts were written through: %+v", second.Endpoints)
	}
	if first.Sent != 1 {
		t.Errorf("an earlier snapshot moved: Sent = %d", first.Sent)
	}
}

// A transport nothing has used answers zero rather than panicking on the
// state it builds lazily.
func TestStatsOfAnUnusedTransport(t *testing.T) {
	got := (&RealTransport{Host: "api.github.com"}).Stats()
	if got.Sent != 0 || got.Endpoints != nil || !got.Limit.Seen.IsZero() {
		t.Errorf("an unused transport reported %+v", got)
	}
}

func TestRateLimitSnapshotCarriesTheReset(t *testing.T) {
	reset := time.Now().Add(37 * time.Minute).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "12")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transport := &RealTransport{Host: strings.TrimPrefix(server.URL, "http://"), UseTLS: false}
	transport.Request("GET", "/repos/acme/widgets", nil, nil)
	if got := transport.Stats().Limit.Resets; !got.Equal(reset) {
		t.Errorf("Resets = %v, want %v", got, reset)
	}
}
