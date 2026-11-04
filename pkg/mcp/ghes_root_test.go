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

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// fakeGHES is a GitHub Enterprise Server as far as REST goes: the API is
// under /api/v3, acme/widgets has its branch already pushed, and every
// path outside /api/v3 is 404 -- which is what one answers a transport
// built for a host named only as -github-host, with no path prefix.
func fakeGHES(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v3/repos/acme/widgets":
			w.Write([]byte(`{"default_branch":"main"}`))
		case "/api/v3/repos/acme/widgets/branches/feature":
			w.Write([]byte(`{"name":"feature","commit":{"sha":"abcdef0123456789","commit":{"message":"the change"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ghesClient(srv *httptest.Server, prefix string) *github.RESTClient {
	token := "t"
	return github.NewClient(&github.RealTransport{
		Host: strings.TrimPrefix(srv.URL, "http://"), PathPrefix: prefix,
	}, github.StaticToken{Token: &token})
}

// A GHES reached at its root 404s the repository as well as the branch,
// and none of the three tools may read that as "not pushed yet": the
// branch is there, and telling the run to push it is the wrong answer.
func TestBranchToolsDoNotCallAGHESRootAnUnpushedBranch(t *testing.T) {
	srv := fakeGHES(t)
	client := ghesClient(srv, "")
	scope := PullRequestScope{Owner: "acme", Repo: "widgets", Branch: "feature", Base: "feature"}

	check := func(name, text string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s answered a 404 for the whole repository as an unpushed branch:\n%s", name, text)
		}
		msg := err.Error()
		for _, want := range []string{"/repos/acme/widgets", "404 for the repository acme/widgets itself", "/api/v3", "host.json"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s error does not say %q:\n%s", name, want, msg)
			}
		}
		if strings.Contains(msg, "git push") {
			t.Errorf("%s still tells the run to push:\n%s", name, msg)
		}
	}

	text, err := pullRequestStatus(context.Background(), client, nil, scope)
	check("pull_request_status", text, err)

	w := waiterFor(client, &fakeClock{})
	w.scope = scope
	text, err = w.wait(context.Background(), DefaultWaitForChecksTimeout)
	check("wait_for_checks", text, err)

	text, err = baseBranchStatus(context.Background(), client, nil, scope)
	check("base_branch_status", text, err)
}

// The same server under /api/v3 finds the branch, and a branch really
// missing from a repository that answers is still "push it".
func TestBranchToolsReadAGHESUnderItsAPIPrefix(t *testing.T) {
	srv := fakeGHES(t)
	client := ghesClient(srv, "/api/v3")

	head, err := client.GetBranchHead("acme", "widgets", "feature")
	if err != nil || head == nil || head.SHA != "abcdef0123456789" {
		t.Fatalf("GetBranchHead = %+v, %v; want the pushed branch", head, err)
	}

	text, err := pullRequestStatus(context.Background(), client, nil,
		PullRequestScope{Owner: "acme", Repo: "widgets", Branch: "other"})
	if err != nil {
		t.Fatalf("pull_request_status: %v", err)
	}
	if !strings.Contains(text, "does not exist") || !strings.Contains(text, "git push origin other") {
		t.Errorf("a branch missing from a readable repository is not reported as unpushed:\n%s", text)
	}
}
