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

package reviewedmerge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeForge struct {
	pulls   map[string][]Pull
	reviews map[int][]Review
	parents map[string]string
}

func (f fakeForge) PullsForCommit(_ context.Context, _, sha string) ([]Pull, error) {
	return f.pulls[sha], nil
}

func (f fakeForge) Reviews(_ context.Context, _ string, n int) ([]Review, error) {
	return f.reviews[n], nil
}

func (f fakeForge) FirstParent(_ context.Context, _, sha string) (string, error) {
	return f.parents[sha], nil
}

var policy = Policy{Repo: "example/grain", Branches: []string{"main", "release/v*"}, Mergers: []string{"grain-bot[bot]"}}

func merged(n int, sha, base, author, by, head string) Pull {
	return Pull{Number: n, State: "closed", Merged: true, MergeCommitSHA: sha, Base: base, Author: author, MergedBy: by, HeadSHA: head}
}

func TestAMergeByGrainIsReviewed(t *testing.T) {
	f := fakeForge{pulls: map[string][]Pull{"m1": {merged(1, "m1", "main", "grain-bot[bot]", "grain-bot[bot]", "h1")}}}
	m, err := policy.Check(context.Background(), f, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Via != "grain" || m.By != "grain-bot[bot]" || m.Pull != 1 {
		t.Fatalf("merge = %+v", m)
	}
}

func TestAForgeApprovalOfTheMergedHeadIsReviewed(t *testing.T) {
	f := fakeForge{
		pulls:   map[string][]Pull{"m2": {merged(2, "m2", "release/v3.2.0", "alice", "alice", "h2")}},
		reviews: map[int][]Review{2: {{Login: "bob", State: "APPROVED", CommitID: "h2", Association: "MEMBER"}, {Login: "bob", State: "COMMENTED", CommitID: "h2", Association: "MEMBER"}}},
	}
	m, err := policy.Check(context.Background(), f, "m2")
	if err != nil {
		t.Fatal(err)
	}
	if m.Via != "review" || m.By != "bob" {
		t.Fatalf("merge = %+v", m)
	}
}

// Nor can an account with no standing hold a release up by requesting
// changes, any more than it can approve one.
func TestAChangeRequestByAnOutsiderDoesNotWithdrawAnApproval(t *testing.T) {
	f := fakeForge{
		pulls: map[string][]Pull{"m2": {merged(2, "m2", "main", "alice", "alice", "h2")}},
		reviews: map[int][]Review{2: {
			{Login: "bob", State: "APPROVED", CommitID: "h2", Association: "COLLABORATOR"},
			{Login: "zed", State: "CHANGES_REQUESTED", CommitID: "h2", Association: "NONE"},
		}},
	}
	if m, err := policy.Check(context.Background(), f, "m2"); err != nil || m.By != "bob" {
		t.Fatalf("merge = %+v, err = %v", m, err)
	}
}

func TestUnreviewedMergesAreRefused(t *testing.T) {
	cases := map[string]struct {
		pulls   []Pull
		reviews []Review
		want    string
	}{
		"pushed directly": {
			want: "pushed onto the branch",
		},
		"merged into a branch that is not released": {
			pulls: []Pull{merged(3, "x", "feature", "alice", "grain-bot[bot]", "h")},
			want:  "pushed onto the branch",
		},
		"only an open pull request names it": {
			pulls: []Pull{{Number: 3, State: "open", Base: "main", HeadSHA: "x"}},
			want:  "pushed onto the branch",
		},
		"self-merged with no review": {
			pulls: []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			want:  "no approval from an owner, member or collaborator other than its author",
		},
		"approved only by its author": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "Mallory", State: "APPROVED", CommitID: "h", Association: "MEMBER"}},
			want:    "no approval from an owner, member or collaborator other than its author",
		},
		"approval of an earlier head": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h2")},
			reviews: []Review{{Login: "bob", State: "APPROVED", CommitID: "h1", Association: "MEMBER"}},
			want:    "earlier head",
		},
		"approval withdrawn by a dismissal": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "bob", State: "APPROVED", CommitID: "h", Association: "MEMBER"}, {Login: "bob", State: "DISMISSED", CommitID: "h", Association: "MEMBER"}},
			want:    "no approval",
		},
		"approved only by an account with no standing in the repository": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "mallorys-sock", State: "APPROVED", CommitID: "h", Association: "NONE"}, {Login: "eve", State: "APPROVED", CommitID: "h", Association: "CONTRIBUTOR"}, {Login: "trent", State: "APPROVED", CommitID: "h"}},
			want:    "no approval from an owner, member or collaborator",
		},
		"changes requested": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "bob", State: "APPROVED", CommitID: "h", Association: "MEMBER"}, {Login: "carol", State: "CHANGES_REQUESTED", CommitID: "h", Association: "MEMBER"}},
			want:    "carol's request for changes",
		},
		"a grain branch merged on the forge by a writer": {
			pulls: []Pull{merged(3, "x", "main", "grain-bot[bot]", "mallory", "h")},
			want:  "merged by mallory",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := fakeForge{pulls: map[string][]Pull{"x": c.pulls}, reviews: map[int][]Review{3: c.reviews}}
			_, err := policy.Check(context.Background(), f, "x")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one saying %q", err, c.want)
			}
		})
	}
}

// The attack a single-commit check misses: a self-merged change with a
// grain merge on top of it. The release of the grain merge carries it.
func TestTheChainCarriesAnUnreviewedMergeBelowAReviewedOne(t *testing.T) {
	f := fakeForge{
		pulls: map[string][]Pull{
			"top":  {merged(5, "top", "main", "grain-bot[bot]", "grain-bot[bot]", "h5")},
			"evil": {merged(4, "evil", "main", "mallory", "mallory", "h4")},
			"prev": {merged(3, "prev", "main", "grain-bot[bot]", "grain-bot[bot]", "h3")},
		},
		parents: map[string]string{"top": "evil", "evil": "prev", "prev": "root"},
	}
	_, err := policy.CheckChain(context.Background(), f, "top", "prev", 100)
	if err == nil || !strings.Contains(err.Error(), "carries evil") {
		t.Fatalf("err = %v, want the chain refused at evil", err)
	}

	f.pulls["evil"] = []Pull{merged(4, "evil", "main", "grain-bot[bot]", "grain-bot[bot]", "h4")}
	rec, err := policy.CheckChain(context.Background(), f, "top", "prev", 100)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Type != RecordType || rec.Since != "prev" || len(rec.Merges) != 2 || rec.Merges[1].Commit != "evil" {
		t.Fatalf("record = %+v", rec)
	}
	if _, err := policy.CheckChain(context.Background(), f, "top", "prev", 1); err == nil || !strings.Contains(err.Error(), "more than 1") {
		t.Fatalf("a chain past max: err = %v", err)
	}
	f.pulls["root"] = []Pull{merged(2, "root", "main", "grain-bot[bot]", "grain-bot[bot]", "h2")}
	if _, err := policy.CheckChain(context.Background(), f, "top", "elsewhere", 100); err == nil || !strings.Contains(err.Error(), "never reaches") {
		t.Fatalf("a chain that misses since: err = %v", err)
	}
}

func TestRESTReadsGitHubsShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			http.Error(w, "no token: "+got, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/example/grain/commits/abc/pulls":
			fmt.Fprint(w, `[{"number":7,"state":"closed","merged_at":"2026-11-10T00:00:00Z","merge_commit_sha":"abc","base":{"ref":"main"},"head":{"sha":"h7"},"user":{"login":"alice"}},
			               {"number":8,"state":"open","merged_at":null,"base":{"ref":"main"},"head":{"sha":"abc"},"user":{"login":"bob"}}]`)
		case "/repos/example/grain/pulls/7":
			fmt.Fprint(w, `{"merged_by":{"login":"alice"}}`)
		case "/repos/example/grain/pulls/7/reviews":
			fmt.Fprint(w, `[{"user":{"login":"zed"},"state":"APPROVED","commit_id":"h7","author_association":"NONE"},{"user":{"login":"bob"},"state":"APPROVED","commit_id":"h7","author_association":"COLLABORATOR"}]`)
		case "/repos/example/grain/git/commits/abc":
			fmt.Fprint(w, `{"parents":[{"sha":"p1"},{"sha":"h7"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := REST{BaseURL: srv.URL, Token: "tok"}
	rec, err := Policy{Repo: "example/grain", Branches: []string{"main"}}.CheckChain(context.Background(), f, "abc", "p1", 10)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(rec)
	want := `{"type":"` + RecordType + `","repo":"example/grain","commit":"abc","since":"p1","merges":[{"commit":"abc","pull":7,"base":"main","via":"review","by":"bob"}]}`
	if string(got) != want {
		t.Fatalf("record = %s\nwant      %s", got, want)
	}
	if _, err := (REST{BaseURL: srv.URL}).PullsForCommit(context.Background(), "example/grain", "abc"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("no token: err = %v", err)
	}
}

// TestRESTRefusesAReviewListTooLongToRead: a change request on a page
// past the last one read would go unseen, so the list is refused rather
// than read in part.
func TestRESTRefusesAReviewListTooLongToRead(t *testing.T) {
	page := "[" + strings.TrimSuffix(strings.Repeat(`{"user":{"login":"bob"},"state":"COMMENTED","author_association":"MEMBER"},`, 100), ",") + "]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page)
	}))
	defer srv.Close()
	if _, err := (REST{BaseURL: srv.URL}).Reviews(context.Background(), "example/grain", 7); err == nil || !strings.Contains(err.Error(), "more than 1000 reviews") {
		t.Fatalf("err = %v, want the list refused", err)
	}
}

// shortRESTBackoff makes get's retries immediate for a test.
func shortRESTBackoff(t *testing.T) {
	t.Helper()
	old := restBackoff
	restBackoff = time.Millisecond
	t.Cleanup(func() { restBackoff = old })
}

// TestRESTRidesOutATransientOutage: a 503 from the API refused release
// run 43048355272 outright; a passing outage is now asked through.
func TestRESTRidesOutATransientOutage(t *testing.T) {
	shortRESTBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		case 2:
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		default:
			fmt.Fprint(w, `{"parents":[{"sha":"p1"}]}`)
		}
	}))
	defer srv.Close()
	got, err := (REST{BaseURL: srv.URL}).FirstParent(context.Background(), "example/grain", "abc")
	if err != nil || got != "p1" {
		t.Fatalf("FirstParent = %q, %v; want p1 after the outage", got, err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("calls = %d, want 3", n)
	}
}

// TestRESTGivesUpOnALastingOutage: an outage that outlasts every attempt
// still refuses, with the API's own answer.
func TestRESTGivesUpOnALastingOutage(t *testing.T) {
	shortRESTBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := (REST{BaseURL: srv.URL}).FirstParent(context.Background(), "example/grain", "abc"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want the 503", err)
	}
	if n := calls.Load(); int(n) != restAttempts {
		t.Fatalf("calls = %d, want %d", n, restAttempts)
	}
}

// TestRESTDoesNotRetryAnAnswer: a 404 or 401 says the same thing every
// time, so it is not asked again.
func TestRESTDoesNotRetryAnAnswer(t *testing.T) {
	shortRESTBackoff(t)
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, http.StatusText(status), status)
		}))
		if _, err := (REST{BaseURL: srv.URL}).FirstParent(context.Background(), "example/grain", "abc"); err == nil {
			t.Fatalf("%d: err = nil", status)
		}
		srv.Close()
		if n := calls.Load(); n != 1 {
			t.Fatalf("%d: calls = %d, want 1", status, n)
		}
	}
}

func TestSplitRepoTakesOwnerNameAndHostOwnerName(t *testing.T) {
	for _, tc := range []struct{ repo, host, owner, name string }{
		{"example/grain", "github.com", "example", "grain"},
		{"git.example.test/experimental/grain", "git.example.test", "experimental", "grain"},
		{"GIT.Example.test/experimental/grain", "git.example.test", "experimental", "grain"},
	} {
		host, owner, name, err := SplitRepo(tc.repo)
		if err != nil || host != tc.host || owner != tc.owner || name != tc.name {
			t.Errorf("SplitRepo(%q) = %q, %q, %q, %v", tc.repo, host, owner, name, err)
		}
	}
	for _, bad := range []string{"", "grain", "/grain", "example/", "a/b/c/d", "/example/grain"} {
		if _, _, _, err := SplitRepo(bad); err == nil {
			t.Errorf("SplitRepo(%q) took it", bad)
		}
	}
	if got := APIBaseURL("github.com"); got != "https://api.github.com" {
		t.Errorf("APIBaseURL(github.com) = %q", got)
	}
	if got := APIBaseURL("git.example.test"); got != "https://git.example.test/api/v3" {
		t.Errorf("APIBaseURL(git.example.test) = %q", got)
	}
}

// roundTripFunc serves a request in process, as a host tool's
// http.Client would, so a test sees the URL REST built.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func inProcessClient(urls *[]string, h http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*urls = append(*urls, r.URL.String())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
}

// TestRESTAsksAnEnterpriseRepositorysOwnAPI: a host/owner/name
// repository is read from https://<host>/api/v3 and recorded whole.
func TestRESTAsksAnEnterpriseRepositorysOwnAPI(t *testing.T) {
	const repo = "git.example.test/experimental/grain"
	var urls []string
	client := inProcessClient(&urls, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/experimental/grain/commits/abc/pulls":
			fmt.Fprint(w, `[{"number":7,"state":"closed","merged_at":"2026-11-10T00:00:00Z","merge_commit_sha":"abc","base":{"ref":"main"},"head":{"sha":"h7"},"user":{"login":"alice"}}]`)
		case "/api/v3/repos/experimental/grain/pulls/7":
			fmt.Fprint(w, `{"merged_by":{"login":"grain-bot"}}`)
		case "/api/v3/repos/experimental/grain/git/commits/abc":
			fmt.Fprint(w, `{"parents":[{"sha":"p1"},{"sha":"h7"}]}`)
		case "/api/v3/repos/experimental/grain/compare/p1...abc":
			fmt.Fprint(w, `{"status":"ahead","merge_base_commit":{"sha":"p1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	f := REST{Client: client}
	p := Policy{Repo: repo, Branches: []string{"main"}, Mergers: []string{"grain-bot"}}
	rec, err := p.CheckChain(context.Background(), f, "abc", "p1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Repo != repo {
		t.Fatalf("record repo = %q, want %q", rec.Repo, repo)
	}
	cmp, err := f.Compare(context.Background(), repo, "p1", "abc")
	if err != nil || cmp.Status != "ahead" || cmp.MergeBase != "p1" {
		t.Fatalf("compare = %+v, %v", cmp, err)
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "https://git.example.test/api/v3/repos/experimental/grain/") {
			t.Errorf("asked %s, not the repository's own host's API", u)
		}
	}
	if len(urls) != 4 {
		t.Errorf("urls = %q", urls)
	}

	// owner/name stays on api.github.com.
	urls = nil
	if _, err := (REST{Client: client}).FirstParent(context.Background(), "example/grain", "abc"); err == nil {
		t.Fatal("api.github.com path was served by the Enterprise handler")
	}
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://api.github.com/repos/example/grain/") {
		t.Fatalf("owner/name asked %q", urls)
	}
}

type errForge struct {
	fakeForge
	failCommit string
}

func (f errForge) PullsForCommit(ctx context.Context, repo, sha string) ([]Pull, error) {
	if sha == f.failCommit {
		return nil, fmt.Errorf("503 Service Unavailable")
	}
	return f.fakeForge.PullsForCommit(ctx, repo, sha)
}

func TestCheckChainAllWalksEveryUnreviewedMerge(t *testing.T) {
	f := fakeForge{
		pulls: map[string][]Pull{
			"c5": {merged(1630, "c5", "main", "grain-bot[bot]", "grain-bot[bot]", "h1630")},
			"c4": {merged(1626, "c4", "main", "alice", "alice", "h1626abcdef0123456789")},
			"c3": {merged(1620, "c3", "main", "dave", "dave", "h1620")},
			"c2": {merged(1612, "c2", "main", "carol", "carol", "h1612abcdef0123456789")},
			// c1 has no PR (direct push)
		},
		reviews: map[int][]Review{
			1620: {{Login: "bob", State: "APPROVED", CommitID: "h1620", Association: "MEMBER"}},
			1612: {{Login: "bob", State: "APPROVED", CommitID: "earlier-head", Association: "MEMBER"}},
		},
		parents: map[string]string{
			"c5":   "c4",
			"c4":   "c3",
			"c3":   "c2",
			"c2":   "c1",
			"c1":   "prev",
			"prev": "",
		},
	}

	rec, refusals, err := policy.CheckChainAll(context.Background(), f, "c5", "prev", 100)
	if err == nil {
		t.Fatal("expected refusal error, got nil")
	}
	if IsForgeError(err) {
		t.Fatalf("unexpected ForgeError: %v", err)
	}
	if rec.Commit != "" || len(rec.Merges) != 0 {
		t.Fatalf("refused CheckChainAll returned non-zero record: %+v", rec)
	}
	if len(refusals) != 3 {
		t.Fatalf("got %d refusals, want 3: %+v", len(refusals), refusals)
	}
	if r := refusals[0]; r.Pull != 1626 || r.MergedBy != "alice" || r.HeadSHA != "h1626abcdef0123456789" || r.Commit != "c4" {
		t.Errorf("refusal[0] = %+v", r)
	}
	if r := refusals[1]; r.Pull != 1612 || r.MergedBy != "carol" || r.HeadSHA != "h1612abcdef0123456789" || r.Commit != "c2" || !strings.Contains(r.Reason, "earlier head") {
		t.Errorf("refusal[1] = %+v", r)
	}
	if r := refusals[2]; r.Pull != 0 || r.Commit != "c1" || !strings.Contains(r.Reason, "pushed onto the branch") {
		t.Errorf("refusal[2] = %+v", r)
	}

	// max caps total commits walked, not only passing merges, and still
	// returns refusals collected before hitting the limit.
	_, partial, err := policy.CheckChainAll(context.Background(), f, "c5", "prev", 2)
	if err == nil || !strings.Contains(err.Error(), "more than 2") {
		t.Fatalf("max=2 err = %v, want more than 2", err)
	}
	if len(partial) != 1 || partial[0].Pull != 1626 {
		t.Fatalf("max=2 partial refusals = %+v, want [#1626]", partial)
	}

	// Missing since reports the chain gap alongside refusals already seen.
	_, partial, err = policy.CheckChainAll(context.Background(), f, "c5", "elsewhere", 100)
	if err == nil || !strings.Contains(err.Error(), "never reaches") {
		t.Fatalf("missing since err = %v", err)
	}
	if len(partial) != 4 { // c4, c2, c1, and prev (no PR)
		t.Fatalf("missing since partial refusals = %+v", partial)
	}

	// Empty since checks only sha.
	_, single, err := policy.CheckChainAll(context.Background(), f, "c4", "", 100)
	if err == nil || len(single) != 1 || single[0].Pull != 1626 {
		t.Fatalf("empty since: refusals=%+v err=%v", single, err)
	}

	// A forge error midway through the walk aborts immediately as a ForgeError.
	_, forgeRefusals, err := policy.CheckChainAll(context.Background(), errForge{fakeForge: f, failCommit: "c2"}, "c5", "prev", 100)
	if !IsForgeError(err) {
		t.Fatalf("expected ForgeError, got %v", err)
	}
	if len(forgeRefusals) != 0 {
		t.Fatalf("forge error returned refusals %+v, want none", forgeRefusals)
	}

	// Once every commit is reviewed, CheckChainAll returns the same Record as CheckChain.
	f.reviews[1626] = []Review{{Login: "bob", State: "APPROVED", CommitID: "h1626abcdef0123456789", Association: "OWNER"}}
	f.reviews[1612] = []Review{{Login: "bob", State: "APPROVED", CommitID: "h1612abcdef0123456789", Association: "COLLABORATOR"}}
	f.pulls["c1"] = []Pull{merged(1608, "c1", "main", "grain-bot[bot]", "grain-bot[bot]", "h1608")}
	rec, refusals, err = policy.CheckChainAll(context.Background(), f, "c5", "prev", 100)
	if err != nil || len(refusals) != 0 {
		t.Fatalf("all reviewed: refusals=%+v err=%v", refusals, err)
	}
	wantRec, err := policy.CheckChain(context.Background(), f, "c5", "prev", 100)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(rec)
	wantJSON, _ := json.Marshal(wantRec)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("record mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestFormatAndParseRefusalLog(t *testing.T) {
	in := []Refusal{
		{
			Commit:   "111111111111abcdef",
			Pull:     1626,
			MergedBy: "alice",
			HeadSHA:  "deadbeef00001111222233334444555566667777",
			Reason:   "with no approval from an owner, member or collaborator other than its author, and not by grain",
		},
		{
			Commit:   "222222222222abcdef",
			Pull:     1612,
			MergedBy: "",
			HeadSHA:  "",
			Reason:   "and bob's approval was of an earlier head than the 999999999999 that was merged",
		},
		{
			Commit: "333333333333abcdef",
			Reason: "not the merge commit of any pull request merged into main or release/v*: pushed onto the branch, not merged through a pull request",
		},
	}

	var b strings.Builder
	b.WriteString("2026-11-16T10:50:12.1234567Z + grep -q 'reviewed-merge: refused:' /tmp/err\r\n")
	b.WriteString("2026-11-16T10:50:12.2345678Z reviewed-merge: refused: 3 unreviewed commit(s) between 111111111111 and 000000000000\r\n")
	b.WriteString("2026-11-16T10:50:12.3456789Z " + FormatRefusalLine(in[0]) + "\r\n")
	b.WriteString("2026-11-16T10:50:12.4567890Z ##[error]" + FormatRefusalLine(in[1]) + "\r\n")
	b.WriteString("2026-11-16T10:50:12.5678901Z " + FormatRefusalLine(in[2]) + "\r\n")
	// Duplicate line should be deduplicated.
	b.WriteString("2026-11-16T10:50:12.6789012Z " + FormatRefusalLine(in[0]) + "\r\n")

	got, raw, matched := ParseRefusalLog(b.String())
	if !matched {
		t.Fatal("expected matched=true")
	}
	if len(raw) != 0 {
		t.Fatalf("expected no rawLines when structured refusals exist, got %q", raw)
	}
	if len(got) != 3 {
		t.Fatalf("got %d refusals, want 3: %+v", len(got), got)
	}
	if got[0].Pull != 1626 || got[0].MergedBy != "alice" || got[0].HeadSHA != in[0].HeadSHA || got[0].Commit != "111111111111" || got[0].Reason != in[0].Reason {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Pull != 1612 || got[1].MergedBy != "" || got[1].HeadSHA != "" || got[1].Commit != "222222222222" || got[1].Reason != in[1].Reason {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].Pull != 0 || got[2].Commit != "333333333333" || got[2].Reason != in[2].Reason {
		t.Errorf("got[2] = %+v", got[2])
	}

	// Legacy single-refusal format (with carrier prefix).
	legacy := "2026-11-16T10:50:12.0Z reviewed-merge: refused: aaaaaaaaaaaa carries bbbbbbbbbbbb, which it was built on: bbbbbbbbbbbb did not reach main through a reviewed merge: pull request #1608 was merged by alice, with no approval from an owner, member or collaborator other than its author, and not by grain\n"
	gotLegacy, rawLegacy, matchedLegacy := ParseRefusalLog(legacy)
	if !matchedLegacy || len(rawLegacy) != 0 || len(gotLegacy) != 1 {
		t.Fatalf("legacy parse: matched=%v refusals=%+v raw=%q", matchedLegacy, gotLegacy, rawLegacy)
	}
	if r := gotLegacy[0]; r.Pull != 1608 || r.MergedBy != "alice" || r.Commit != "bbbbbbbbbbbb" || r.Base != "main" {
		t.Errorf("legacy refusal = %+v", r)
	}

	// Forge error does NOT match as a refusal.
	forgeLog := "2026-11-16T10:50:12.0Z reviewed-merge: forge error, not a policy refusal: could not read the pull requests for abc: 503 Service Unavailable\n"
	if r, raw, ok := ParseRefusalLog(forgeLog); ok || len(r) != 0 || len(raw) != 0 {
		t.Fatalf("forge error matched as refusal: ok=%v r=%+v raw=%q", ok, r, raw)
	}

	// Mid-line shell command referencing the prefix does NOT match.
	shellLog := "2026-11-16T10:50:12.0Z + grep -q 'reviewed-merge: refused:' /tmp/err\n"
	if _, _, ok := ParseRefusalLog(shellLog); ok {
		t.Fatal("shell command trace falsely matched as refusal")
	}

	// Chain-cap error with no PR lines still matches and preserves raw line.
	capLog := "reviewed-merge: refused: aaaaaaaaaaaa is more than 1000 first-parent commits past bbbbbbbbbbbb, so the chain between them was not checked\n"
	if r, raw, ok := ParseRefusalLog(capLog); !ok || len(r) != 0 || len(raw) != 1 || !strings.Contains(raw[0], "more than 1000") {
		t.Fatalf("cap parse: ok=%v r=%+v raw=%q", ok, r, raw)
	}
}
