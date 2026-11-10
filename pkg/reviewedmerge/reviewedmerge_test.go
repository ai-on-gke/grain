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
	"testing"
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
		reviews: map[int][]Review{2: {{Login: "bob", State: "APPROVED", CommitID: "h2"}, {Login: "bob", State: "COMMENTED", CommitID: "h2"}}},
	}
	m, err := policy.Check(context.Background(), f, "m2")
	if err != nil {
		t.Fatal(err)
	}
	if m.Via != "review" || m.By != "bob" {
		t.Fatalf("merge = %+v", m)
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
			want:  "no approval from anybody but its author",
		},
		"approved only by its author": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "Mallory", State: "APPROVED", CommitID: "h"}},
			want:    "no approval from anybody but its author",
		},
		"approval of an earlier head": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h2")},
			reviews: []Review{{Login: "bob", State: "APPROVED", CommitID: "h1"}},
			want:    "earlier head",
		},
		"approval withdrawn by a dismissal": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "bob", State: "APPROVED", CommitID: "h"}, {Login: "bob", State: "DISMISSED", CommitID: "h"}},
			want:    "no approval",
		},
		"changes requested": {
			pulls:   []Pull{merged(3, "x", "main", "mallory", "mallory", "h")},
			reviews: []Review{{Login: "bob", State: "APPROVED", CommitID: "h"}, {Login: "carol", State: "CHANGES_REQUESTED", CommitID: "h"}},
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
			fmt.Fprint(w, `[{"user":{"login":"bob"},"state":"APPROVED","commit_id":"h7"}]`)
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
