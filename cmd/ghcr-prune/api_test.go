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

package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

func newAPI(responses ...github.ApiResponse) (*API, *github.FakeTransport) {
	tr := github.NewFakeTransport(responses...)
	return &API{Transport: tr, Token: "t", Owner: "bwsalmon", Repo: "grain"}, tr
}

func ok(body string) github.ApiResponse {
	return github.ApiResponse{Status: 200, Body: []byte(body)}
}

// GHCR names a package for the whole image path under the owner, so
// ghcr.io/bwsalmon/grain/guest is the package "grain/guest" -- one path
// segment with a slash in it, which has to be escaped or GitHub answers
// 404 to every call. And the packages API has one path for an
// organization and another for a user, with nothing in a workflow's
// environment saying which this account is, so the first call asks.
func TestThePackageIsAskedForByItsFullNameUnderEitherKindOfOwner(t *testing.T) {
	api, tr := newAPI(
		github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)},
		ok(`[]`),
		ok(`[]`),
	)
	if _, err := api.ListVersions("guest"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/orgs/bwsalmon/packages/container/grain%2Fguest/versions?per_page=1",
		"/users/bwsalmon/packages/container/grain%2Fguest/versions?per_page=1",
		"/users/bwsalmon/packages/container/grain%2Fguest/versions?per_page=100",
	}
	if len(tr.Calls) != len(want) {
		t.Fatalf("made %d calls, want %d: %+v", len(tr.Calls), len(want), tr.Calls)
	}
	for i, path := range want {
		if tr.Calls[i].Path != path {
			t.Errorf("call %d went to %q, want %q", i, tr.Calls[i].Path, path)
		}
	}
	if got := tr.Calls[0].Headers["Authorization"]; got != "token t" {
		t.Errorf("Authorization = %q, want the token this command was given", got)
	}
}

func TestVersionsAreReadWithTheirTagsAndAgesAcrossPages(t *testing.T) {
	page1 := github.ApiResponse{
		Status:  200,
		Headers: map[string]string{"Link": `<https://api.github.com/users/bwsalmon/packages/container/grain%2Fguest/versions?per_page=100&page=2>; rel="next"`},
		Body: []byte(`[{"id":1,"name":"sha256:aa","created_at":"2026-01-02T03:04:05Z",
			"metadata":{"container":{"tags":["latest","sha-abc1234"]}}}]`),
	}
	page2 := ok(`[{"id":2,"name":"sha256:bb","created_at":"2025-06-07T08:09:10Z","metadata":{"container":{"tags":[]}}}]`)
	api, tr := newAPI(ok(`[]`), page1, page2)

	got, err := api.ListVersions("guest")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d versions, want both pages", len(got))
	}
	if got[0].ID != 1 || got[0].Digest != "sha256:aa" || len(got[0].Tags) != 2 {
		t.Errorf("first version came back as %+v", got[0])
	}
	if got[0].Package != "guest" {
		t.Errorf("version's package is %q, want the name it was asked for -- the plan reasons across packages", got[0].Package)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !got[0].Created.Equal(want) {
		t.Errorf("created_at read as %v, want %v", got[0].Created, want)
	}
	if len(got[1].Tags) != 0 {
		t.Errorf("second version should be untagged, got %v", got[1].Tags)
	}
	if last := tr.Calls[len(tr.Calls)-1].Path; last != "/users/bwsalmon/packages/container/grain%2Fguest/versions?per_page=100&page=2" {
		t.Errorf("second page fetched from %q", last)
	}
}

func TestVersionsAreReadWithLowercaseLinkHeader(t *testing.T) {
	page1 := github.ApiResponse{
		Status:  200,
		Headers: map[string]string{"link": `<https://api.github.com/users/bwsalmon/packages/container/grain%2Fguest/versions?per_page=100&page=2>; rel="next"`},
		Body: []byte(`[{"id":1,"name":"sha256:aa","created_at":"2026-01-02T03:04:05Z",
			"metadata":{"container":{"tags":["latest"]}}}]`),
	}
	page2 := ok(`[{"id":2,"name":"sha256:bb","created_at":"2025-06-07T08:09:10Z","metadata":{"container":{"tags":[]}}}]`)
	api, _ := newAPI(ok(`[]`), page1, page2)

	got, err := api.ListVersions("guest")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d versions with lowercase link header, want both pages (2)", len(got))
	}
}

// The two failures a deletion can meet are not the same failure. A
// public version with more than 5,000 downloads is one GitHub refuses
// for everybody and for ever (400), and failing the schedule on it would
// leave a red run no later one can clear; a 403 says this token cannot
// delete packages at all, which is exactly what should stop a run and be
// seen.
func TestARefusalAndAMissingPermissionAreDifferentErrors(t *testing.T) {
	cases := []struct {
		status  int
		refused bool
		fatal   bool
	}{
		{204, false, false},
		{400, true, false},
		{422, true, false},
		{403, false, true},
		{404, false, true},
	}
	for _, c := range cases {
		api, _ := newAPI(ok(`[]`), github.ApiResponse{Status: c.status, Body: []byte(`{"message":"..."}`)})
		err := api.DeleteVersion("guest", 7)
		var refusal *ErrRefused
		var apiErr *github.Error
		switch {
		case c.refused && !errors.As(err, &refusal):
			t.Errorf("delete answered %d and gave %v, want a refusal to step over", c.status, err)
		case c.fatal && !errors.As(err, &apiErr):
			t.Errorf("delete answered %d and gave %v, want an error that stops the run", c.status, err)
		case !c.refused && !c.fatal && err != nil:
			t.Errorf("delete answered %d and gave %v, want success", c.status, err)
		}
	}
}

// The 403 above is the one an operator actually meets, and what they
// read is a workflow log: main prints whatever run() returns, and the
// reason they are reading it at all is that their images did not get
// pruned. So the failure names itself in GitHub's own words, with this
// file's sentence about which credential fixes it still after them --
// not as a quoted JSON document with both of those inside it.
func TestAMissingPermissionReadsAsWhatGitHubSaid(t *testing.T) {
	api, _ := newAPI(ok(`[]`), github.ApiResponse{Status: 403, Body: []byte(
		"{\r\n  \"message\": \"Resource not accessible by personal access token\",\r\n" +
			"  \"documentation_url\": \"https://docs.github.com/rest\"\r\n}")})
	err := api.DeleteVersion("guest", 7)
	if err == nil {
		t.Fatal("a refused deletion came back as a deletion")
	}
	if !strings.Contains(err.Error(), "GitHub answered 403: Resource not accessible by personal access token") {
		t.Errorf("the log line does not say what GitHub said: %v", err)
	}
	if !strings.Contains(err.Error(), "GHCR_PRUNE_TOKEN") {
		t.Errorf("the remedy this file adds did not survive the rendering: %v", err)
	}
	for _, unwanted := range []string{`\r\n`, "documentation_url"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("the log line carries %s out of the raw body: %v", unwanted, err)
		}
	}
	// Rendered, not replaced: the test above tells a fatal error from a
	// refusal by asking what it is, and so does anything added later.
	var apiErr *github.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Errorf("the GitHub error under the rendering is gone: %v", err)
	}
}

// A run of this command is hundreds of requests in one job -- every
// version of three packages, a hundred to a page, then a burst of
// deletions -- so meeting a rate limit is an expected event rather than
// an exotic one, and it clears by waiting. The wait is recorded rather
// than taken, or this test would cost a minute.
func TestARateLimitIsWaitedOutRatherThanFailingTheRun(t *testing.T) {
	limited := github.ApiResponse{
		Status:  403,
		Headers: map[string]string{"retry-after": "17"},
		Body:    []byte(`{"message":"You have exceeded a secondary rate limit"}`),
	}
	api, tr := newAPI(ok(`[]`), limited, github.ApiResponse{Status: 204})
	var waits []time.Duration
	api.Sleep = func(d time.Duration) { waits = append(waits, d) }

	if err := api.DeleteVersion("guest", 7); err != nil {
		t.Fatalf("a rate limit that cleared on the retry gave %v", err)
	}
	if len(waits) != 1 || waits[0] != 17*time.Second {
		t.Errorf("waited %v, want the single 17s Retry-After asked for", waits)
	}
	if last := tr.Calls[len(tr.Calls)-1]; last.Method != "DELETE" {
		t.Errorf("the retry went to %s %s, want the same DELETE again", last.Method, last.Path)
	}
}

func TestA429RateLimitWithoutHeadersIsWaitedOut(t *testing.T) {
	limited := github.ApiResponse{
		Status: 429,
		Body:   []byte(`Too Many Requests`),
	}
	api, tr := newAPI(ok(`[]`), limited, github.ApiResponse{Status: 204})
	var waits []time.Duration
	api.Sleep = func(d time.Duration) { waits = append(waits, d) }

	if err := api.DeleteVersion("guest", 7); err != nil {
		t.Fatalf("a 429 rate limit that cleared on the retry gave %v", err)
	}
	if len(waits) != 1 || waits[0] != defaultRateLimitPause {
		t.Errorf("waited %v, want default pause %v", waits, defaultRateLimitPause)
	}
	if last := tr.Calls[len(tr.Calls)-1]; last.Method != "DELETE" {
		t.Errorf("the retry went to %s %s, want the same DELETE again", last.Method, last.Path)
	}
}

// The distinction the whole of rateLimitPause exists for: 403 is both
// "this credential may never delete packages", which is fixed by a
// person setting GHCR_PRUNE_TOKEN, and "not so fast", which is fixed by
// waiting. Answering the second with the first sends somebody to make a
// personal access token that changes nothing.
func TestARateLimitIsNotReportedAsAMissingPermission(t *testing.T) {
	reset := time.Date(2026, 9, 7, 12, 30, 0, 0, time.UTC)
	now := reset.Add(-40 * time.Minute)
	cases := []struct {
		name string
		resp github.ApiResponse
		want time.Duration
	}{{
		name: "the primary limit, spent to nothing until it refills",
		resp: github.ApiResponse{
			Status: 403,
			Headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     strconv.FormatInt(reset.Unix(), 10),
			},
			Body: []byte(`{"message":"API rate limit exceeded"}`),
		},
		want: 40 * time.Minute,
	}, {
		name: "a secondary limit asking for longer than the job has",
		resp: github.ApiResponse{
			Status:  429,
			Headers: map[string]string{"Retry-After": "600"},
			Body:    []byte(`{"message":"You have exceeded a secondary rate limit"}`),
		},
		want: 10 * time.Minute,
	}, {
		name: "a secondary limit 429 without rate limit text exceeding max pause",
		resp: github.ApiResponse{
			Status:  429,
			Headers: map[string]string{"Retry-After": "360"},
			Body:    []byte(`Too Many Requests`),
		},
		want: 6 * time.Minute,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api, _ := newAPI(ok(`[]`), c.resp)
			api.Now = func() time.Time { return now }
			api.Sleep = func(time.Duration) { t.Error("waited out a limit longer than the job's own timeout") }

			err := api.DeleteVersion("guest", 7)
			var limited *ErrRateLimited
			if !errors.As(err, &limited) {
				t.Fatalf("delete gave %v, want an error that says it is a rate limit", err)
			}
			if limited.Pause != c.want {
				t.Errorf("reported a wait of %s, want %s", limited.Pause, c.want)
			}
			if strings.Contains(err.Error(), "packages: write") {
				t.Errorf("a rate limit was reported as a credential problem: %v", err)
			}
		})
	}
}

// And the other direction, which is the one that must stay loud: a
// refusal that names no limit anywhere is the credential, and the
// message is what tells a person to go and set GHCR_PRUNE_TOKEN.
func TestAMissingPermissionStillSaysWhatToDoAboutIt(t *testing.T) {
	api, _ := newAPI(ok(`[]`), github.ApiResponse{
		Status: 403,
		Body:   []byte(`{"message":"Resource not accessible by integration"}`),
	})
	api.Sleep = func(time.Duration) { t.Error("retried a permission failure, which no amount of waiting clears") }

	err := api.DeleteVersion("guest", 7)
	var limited *ErrRateLimited
	if errors.As(err, &limited) {
		t.Fatalf("a permission failure was read as a rate limit: %v", err)
	}
	if !strings.Contains(err.Error(), "GHCR_PRUNE_TOKEN") {
		t.Errorf("the 403 message %q does not say what to do about it", err)
	}
}

// The listing is the other half of the request budget -- 100 versions a
// page across three packages -- and it fails long before any deletion
// happens, so it has to name the limit too rather than reporting a bare
// 403 from a call nobody would expect to need a permission.
func TestAListingThatRunsOutOfBudgetSaysSo(t *testing.T) {
	api, _ := newAPI(ok(`[]`), github.ApiResponse{
		Status:  429,
		Headers: map[string]string{"Retry-After": "3600"},
		Body:    []byte(`{"message":"You have exceeded a secondary rate limit"}`),
	})
	api.Sleep = func(time.Duration) { t.Error("waited out an hour inside a twenty-minute job") }

	_, err := api.ListVersions("guest")
	var limited *ErrRateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("listing gave %v, want an error that says it is a rate limit", err)
	}
}

func TestDeletionNamesTheVersionUnderThePackageItBelongsTo(t *testing.T) {
	api, tr := newAPI(ok(`[]`), github.ApiResponse{Status: 204})
	if err := api.DeleteVersion("guest-base", 42); err != nil {
		t.Fatal(err)
	}
	last := tr.Calls[len(tr.Calls)-1]
	if last.Method != "DELETE" {
		t.Errorf("method %q, want DELETE", last.Method)
	}
	if want := "/orgs/bwsalmon/packages/container/grain%2Fguest-base/versions/42"; last.Path != want {
		t.Errorf("deleted %q, want %q", last.Path, want)
	}
}

// A branch is compared against the registry, so it is read in the
// registry's spelling: '/' is not legal in a docker tag, and CI, this
// command and pkg/upgrade's TagForBranch all make the same substitution
// -- one that keeps grain/task-232 and grain-task-232 apart (#2386).
func TestBranchesComeBackInTheSpellingATagUses(t *testing.T) {
	api, _ := newAPI(ok(`[{"name":"main"},{"name":"grain/task-232"},{"name":"grain-task-9"}]`))
	got, err := api.ListBranchTags()
	if err != nil {
		t.Fatal(err)
	}
	if !got["main"] || !got["grain..task-232"] || !got["grain-task-9"] || got["grain-task-232"] || len(got) != 3 {
		t.Errorf("branch tags came back as %v", got)
	}
}

type failingTransport struct {
	errorsLeft int
	err        error
	fallback   github.Transport
}

func (f *failingTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if f.errorsLeft > 0 {
		f.errorsLeft--
		return github.ApiResponse{}, f.err
	}
	return f.fallback.Request(method, path, headers, body)
}

func TestNetworkErrorsAreRetriedWithBackoff(t *testing.T) {
	fakeTr := github.NewFakeTransport(ok(`[{"name":"main"}]`))
	tr := &failingTransport{
		errorsLeft: 2,
		err:        errors.New("dial tcp: lookup api.github.com: i/o timeout"),
		fallback:   fakeTr,
	}
	api := &API{
		Transport: tr,
		Token:     "t",
		Owner:     "bwsalmon",
		Repo:      "grain",
	}

	var slept []time.Duration
	api.Sleep = func(d time.Duration) {
		slept = append(slept, d)
	}

	tags, err := api.ListBranchTags()
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if !tags["main"] {
		t.Errorf("branch tags = %v, want main", tags)
	}
	if len(slept) != 2 {
		t.Fatalf("expected 2 sleeps, got %d: %v", len(slept), slept)
	}
	if slept[0] != 500*time.Millisecond || slept[1] != 1000*time.Millisecond {
		t.Errorf("unexpected sleep backoff sequence: %v", slept)
	}
}

func TestNetworkErrorsExhaustRetries(t *testing.T) {
	tr := &failingTransport{
		errorsLeft: 10,
		err:        errors.New("dial tcp: lookup api.github.com: i/o timeout"),
		fallback:   github.NewFakeTransport(),
	}
	api := &API{
		Transport: tr,
		Token:     "t",
		Owner:     "bwsalmon",
		Repo:      "grain",
	}

	var slept []time.Duration
	api.Sleep = func(d time.Duration) {
		slept = append(slept, d)
	}

	_, err := api.ListBranchTags()
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
	if len(slept) != networkRetries {
		t.Errorf("expected %d retries/sleeps, got %d", networkRetries, len(slept))
	}
}

// A DELETE whose response was lost to a timeout may still have reached
// GitHub, so the retry's 404 is that deletion rather than a failure.
func TestARetriedDeleteThatFindsTheVersionGoneIsNotFatal(t *testing.T) {
	api, fake := newAPI(ok(`[]`), github.ApiResponse{Status: 404, Body: []byte(`{"message":"Package version not found"}`)})
	api.Sleep = func(time.Duration) {}
	if _, err := api.packagePath("guest"); err != nil {
		t.Fatal(err)
	}
	api.Transport = &failingTransport{errorsLeft: 1, err: errors.New("context deadline exceeded"), fallback: fake}
	err := api.DeleteVersion("guest", 42)
	var gone *ErrAlreadyGone
	if !errors.As(err, &gone) {
		t.Fatalf("DeleteVersion = %v, want ErrAlreadyGone", err)
	}
}

// Without a lost response before it, a 404 is not explained by a retry
// and stays an error somebody has to look at.
func TestAFirstTimeDelete404IsStillAnError(t *testing.T) {
	api, _ := newAPI(ok(`[]`), github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)})
	err := api.DeleteVersion("guest", 42)
	var gone *ErrAlreadyGone
	if err == nil || errors.As(err, &gone) {
		t.Fatalf("DeleteVersion = %v, want a plain error", err)
	}
}

type gatedTransport struct {
	refusals int
	until    time.Time
	fallback github.Transport
}

func (g *gatedTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if g.refusals > 0 {
		g.refusals--
		return github.ApiResponse{}, &github.RateLimitError{Until: g.until, Reason: "secondary rate limit"}
	}
	return g.fallback.Request(method, path, headers, body)
}

// RealTransport's own gate refuses calls until a stated instant; that is
// waited out once, to the instant, not spent on network retries.
func TestAGateRefusalIsWaitedOutUntilItOpens(t *testing.T) {
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	tr := &gatedTransport{refusals: 1, until: clock.Add(60 * time.Second), fallback: github.NewFakeTransport(ok(`[{"name":"main"}]`))}
	var slept []time.Duration
	api := &API{Transport: tr, Token: "t", Owner: "bwsalmon", Repo: "grain",
		Now: func() time.Time { return clock }, Sleep: func(d time.Duration) { slept = append(slept, d) }}
	tags, err := api.ListBranchTags()
	if err != nil || !tags["main"] {
		t.Fatalf("ListBranchTags = %v, %v", tags, err)
	}
	if len(slept) != 1 || slept[0] != 60*time.Second {
		t.Errorf("slept %v, want one 60s wait for the gate", slept)
	}
}

// A gate shut for longer than one job can wait is handed back at once.
func TestAGateShutTooLongIsReturned(t *testing.T) {
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	tr := &gatedTransport{refusals: 10, until: clock.Add(time.Hour), fallback: github.NewFakeTransport()}
	var slept []time.Duration
	api := &API{Transport: tr, Token: "t", Owner: "bwsalmon", Repo: "grain",
		Now: func() time.Time { return clock }, Sleep: func(d time.Duration) { slept = append(slept, d) }}
	_, err := api.ListBranchTags()
	if !github.IsRateLimited(err) {
		t.Fatalf("err = %v, want the gate's RateLimitError", err)
	}
	if len(slept) != 0 {
		t.Errorf("slept %v, want no wait for an hour-long gate", slept)
	}
}
