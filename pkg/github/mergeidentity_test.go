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
	"errors"
	"strings"
	"testing"
)

// authRecorder answers every request 200 with body and records the
// Authorization each one carried, by method and path.
type authRecorder struct {
	body string
	auth map[string]string
}

func (r *authRecorder) Request(method, path string, headers map[string]string, body []byte) (ApiResponse, error) {
	if r.auth == nil {
		r.auth = map[string]string{}
	}
	r.auth[method+" "+path] = headers["Authorization"]
	return ApiResponse{Status: 200, Body: []byte(r.body)}, nil
}

// mergeSource pushes with push and merges with merge, when configured.
type mergeSource struct {
	push, merge *string
	configured  bool
}

func (m mergeSource) TokenFor(owner, repo string) *string { return m.push }
func (m mergeSource) MergeTokenFor(owner, repo string) (*string, bool) {
	return m.merge, m.configured
}
func (m mergeSource) MergeIdentityFor(owner, repo string) string {
	if !m.configured {
		return ""
	}
	return "merge credential grain-merge (grain-merge[bot])"
}

func strp(s string) *string { return &s }

func TestAMergeGoesOutUnderTheRepositorysMergeCredential(t *testing.T) {
	rec := &authRecorder{body: `{"merged":true}`}
	c := NewClient(rec, mergeSource{push: strp("person"), merge: strp("app"), configured: true})
	if err := c.MergePullRequestWithMessage("acme", "widgets", 7, "abc", "", "Origin: task 1"); err != nil {
		t.Fatal(err)
	}
	if got := rec.auth["PUT /repos/acme/widgets/pulls/7/merge"]; got != "token app" {
		t.Fatalf("merge went out with %q, want the merge credential's token", got)
	}
	// Nothing else moved to the merge credential.
	if _, err := c.GetPullRequest("acme", "widgets", 7); err != nil {
		t.Fatal(err)
	}
	if got := rec.auth["GET /repos/acme/widgets/pulls/7"]; got != "token person" {
		t.Fatalf("a read went out with %q, want the push credential's token", got)
	}
	if got := c.MergeIdentity("acme", "widgets"); !strings.Contains(got, "grain-merge[bot]") {
		t.Fatalf("MergeIdentity = %q", got)
	}
}

func TestAMergeWithNoMergeCredentialStaysOnThePushCredential(t *testing.T) {
	rec := &authRecorder{body: `{"merged":true}`}
	c := NewClient(rec, mergeSource{push: strp("person")})
	if err := c.MergePullRequest("acme", "widgets", 7, "abc"); err != nil {
		t.Fatal(err)
	}
	if got := rec.auth["PUT /repos/acme/widgets/pulls/7/merge"]; got != "token person" {
		t.Fatalf("merge went out with %q, want the push credential's token", got)
	}
	if got := c.MergeIdentity("acme", "widgets"); got != "" {
		t.Fatalf("MergeIdentity = %q, want none", got)
	}
}

// A merge credential that gives no token refuses the merge: making it as
// the person instead is the very thing the setting is there to stop.
func TestAMergeCredentialWithNoTokenRefusesRatherThanMergingAsThePerson(t *testing.T) {
	rec := &authRecorder{body: `{"merged":true}`}
	c := NewClient(rec, mergeSource{push: strp("person"), configured: true})
	err := c.MergePullRequest("acme", "widgets", 7, "abc")
	var none *ErrNoMergeToken
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want ErrNoMergeToken", err)
	}
	if len(rec.auth) != 0 {
		t.Fatalf("requests went out anyway: %v", rec.auth)
	}
}

func TestPullRequestDetailCarriesMergedBy(t *testing.T) {
	rec := &authRecorder{body: `{"number":7,"state":"closed","merged":true,"merged_by":{"login":"grain-merge[bot]"}}`}
	d, err := NewClient(rec, nil).GetPullRequest("acme", "widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.MergedBy != "grain-merge[bot]" {
		t.Fatalf("MergedBy = %q", d.MergedBy)
	}
}

func TestRepoVariableAndAccountType(t *testing.T) {
	rec := &authRecorder{body: `{"name":"GRAIN_MERGE_LOGINS","value":"octocat","type":"User"}`}
	c := NewClient(rec, nil)
	v, ok, err := c.RepoVariable("acme", "widgets", "GRAIN_MERGE_LOGINS")
	if err != nil || !ok || v != "octocat" {
		t.Fatalf("RepoVariable = %q, %v, %v", v, ok, err)
	}
	kind, err := c.AccountType("acme", "widgets", "octocat")
	if err != nil || kind != "User" {
		t.Fatalf("AccountType = %q, %v", kind, err)
	}
}
