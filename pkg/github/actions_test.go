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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// actionsForge is a forge serving one workflow run, its jobs, one job's
// log behind a redirect to a blob host (as GitHub serves it), and its
// artifacts, under prefix ("" for github.com, "/api/v3" for GHE).
func actionsForge(t *testing.T, prefix string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the blob host was sent the forge credential")
		}
		w.Write([]byte("line 1\nline 2\nFAIL: TestThing\n"))
	}))
	t.Cleanup(blob.Close)
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		p := strings.TrimPrefix(r.URL.Path, prefix)
		switch p {
		case "/repos/acme/widgets/actions/runs":
			if r.URL.Query().Get("branch") != "main" {
				t.Errorf("runs asked with branch %q, want main", r.URL.Query().Get("branch"))
			}
			w.Write([]byte(`{"workflow_runs":[{"id":42,"name":"Release","path":".github/workflows/release.yml",
				"head_branch":"main","head_sha":"abc","event":"push","status":"completed","conclusion":"failure",
				"html_url":"https://github.test/acme/widgets/actions/runs/42","run_attempt":2,
				"run_started_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-01T10:05:00Z"}]}`))
		case "/repos/acme/widgets/actions/runs/42":
			w.Write([]byte(`{"id":42,"name":"Release","path":".github/workflows/release.yml","status":"completed","conclusion":"failure"}`))
		case "/repos/acme/widgets/actions/runs/42/jobs":
			w.Write([]byte(`{"jobs":[{"id":7,"run_id":42,"name":"publish","status":"completed","conclusion":"failure",
				"steps":[{"number":1,"name":"checkout","status":"completed","conclusion":"success"},
				{"number":2,"name":"push","status":"completed","conclusion":"failure"}]}]}`))
		case "/repos/acme/widgets/actions/jobs/7/logs":
			// Another host name for the same listener: net/http keeps a
			// credential across a redirect to the same host whatever the
			// port, and GitHub's blob host is never the API's.
			http.Redirect(w, r, strings.Replace(blob.URL, "127.0.0.1", "localhost", 1)+"/log.txt?sig=x", http.StatusFound)
		case "/repos/acme/widgets/actions/runs/42/artifacts":
			w.Write([]byte(`{"artifacts":[{"id":9,"name":"dist","size_in_bytes":1234}]}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(forge.Close)
	return forge, &seen
}

func TestActionsReaderOnGitHubAndGHE(t *testing.T) {
	for _, prefix := range []string{"", "/api/v3"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			forge, seen := actionsForge(t, prefix)
			transport := &RealTransport{Host: strings.TrimPrefix(forge.URL, "http://"), PathPrefix: prefix,
				ledger: &callerLedger{logf: func(string, ...any) {}}}
			client := WithCaller(NewClient(transport, StaticToken{strPtr("t")}), "ci-read").(*RESTClient)

			runs, err := client.ListActionsRuns("acme", "widgets", ActionsRunsQuery{Branch: "main", Limit: 5})
			if err != nil || len(runs) != 1 {
				t.Fatalf("ListActionsRuns = %+v, %v", runs, err)
			}
			if r := runs[0]; r.ID != 42 || r.Path != ".github/workflows/release.yml" || r.RunAttempt != 2 || r.StartedAt == nil {
				t.Errorf("run = %+v", r)
			}
			run, err := client.GetActionsRun("acme", "widgets", 42)
			if err != nil || run == nil || run.Name != "Release" {
				t.Fatalf("GetActionsRun = %+v, %v", run, err)
			}
			if missing, err := client.GetActionsRun("acme", "widgets", 43); err != nil || missing != nil {
				t.Errorf("GetActionsRun(43) = %+v, %v; want nil, nil", missing, err)
			}
			jobs, err := client.ListActionsJobs("acme", "widgets", 42)
			if err != nil || len(jobs) != 1 || len(jobs[0].Steps) != 2 || jobs[0].Steps[1].Conclusion != "failure" {
				t.Fatalf("ListActionsJobs = %+v, %v", jobs, err)
			}
			log, err := client.ActionsJobLog("acme", "widgets", 7)
			if err != nil || !strings.Contains(log, "FAIL: TestThing") {
				t.Fatalf("ActionsJobLog = %q, %v", log, err)
			}
			if _, err := client.ActionsJobLog("acme", "widgets", 8); err == nil {
				t.Errorf("a log the forge will not serve should be an error")
			}
			arts, err := client.ListActionsArtifacts("acme", "widgets", 42)
			if err != nil || len(arts) != 1 || arts[0].Bytes != 1234 {
				t.Fatalf("ListActionsArtifacts = %+v, %v", arts, err)
			}
			for _, uri := range *seen {
				if !strings.HasPrefix(uri, prefix+"/repos/") {
					t.Errorf("request %q is not under %q", uri, prefix+"/repos/")
				}
			}
			stats := transport.ledger.callerStats()["ci-read"]
			if stats.Sent != int64(len(*seen)) {
				t.Errorf("ledger charged ci-read %d requests, the forge saw %d", stats.Sent, len(*seen))
			}
			if stats.Kinds["job logs"].Sent != 2 || stats.Kinds["artifacts"].Sent != 1 {
				t.Errorf("ledger kinds = %+v, want 2 job logs and 1 artifacts", stats.Kinds)
			}
		})
	}
}

func TestListActionsRunsPaginatesAndFiltersByWorkflow(t *testing.T) {
	t.Parallel()
	var forgeURL string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/actions/workflows/nightly.yml/runs":
			w.Write([]byte(`{"total_count":1,"workflow_runs":[{"id":999,"name":"nightly","path":".github/workflows/nightly.yml",
				"head_branch":"main","head_sha":"744a1db","event":"schedule","status":"completed","conclusion":"failure",
				"run_started_at":"2026-11-02T02:21:46Z","updated_at":"2026-11-02T02:59:54Z"}]}`))
		case "/repos/acme/widgets/actions/runs":
			if r.URL.Query().Get("page") == "2" {
				w.Write([]byte(`{"total_count":3410,"workflow_runs":[{"id":999,"name":"nightly","path":".github/workflows/nightly.yml",
					"head_branch":"main","head_sha":"744a1db","event":"schedule","status":"completed","conclusion":"failure",
					"run_started_at":"2026-11-02T02:21:46Z","updated_at":"2026-11-02T02:59:54Z"}]}`))
				return
			}
			w.Header().Set("Link", `<`+forgeURL+`/repos/acme/widgets/actions/runs?page=2&per_page=100>; rel="next"`)
			var buf strings.Builder
			buf.WriteString(`{"total_count":3410,"workflow_runs":[`)
			for i := 0; i < 100; i++ {
				if i > 0 {
					buf.WriteByte(',')
				}
				buf.WriteString(`{"id":`)
				buf.WriteString(intString(1000 + i))
				buf.WriteString(`,"name":"Tests","path":".github/workflows/tests.yml","head_branch":"main","status":"completed","conclusion":"success"}`)
			}
			buf.WriteString(`]}`)
			w.Write([]byte(buf.String()))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	forgeURL = forge.URL
	t.Cleanup(forge.Close)

	transport := &RealTransport{Host: strings.TrimPrefix(forge.URL, "http://"), ledger: &callerLedger{logf: func(string, ...any) {}}}
	client := NewClient(transport, StaticToken{strPtr("t")})

	for _, wf := range []string{"nightly", "nightly.yml", ".github/workflows/nightly.yml"} {
		page, err := client.ListActionsRunsPage("acme", "widgets", ActionsRunsQuery{Workflow: wf, Limit: 20})
		if err != nil {
			t.Fatalf("ListActionsRunsPage(%q): %v", wf, err)
		}
		if !page.WorkflowFiltered || page.TotalCount != 1 || len(page.Runs) != 1 || page.Runs[0].ID != 999 {
			t.Fatalf("ListActionsRunsPage(%q) = %+v, want 1 filtered nightly run 999", wf, page)
		}
	}

	fallback, err := client.ListActionsRunsPage("acme", "widgets", ActionsRunsQuery{Workflow: "Nightly Suite", Limit: 50})
	if err != nil {
		t.Fatalf("fallback ListActionsRunsPage: %v", err)
	}
	if fallback.WorkflowFiltered {
		t.Errorf("display-name fallback unexpectedly marked WorkflowFiltered")
	}

	paged, err := client.ListActionsRunsPage("acme", "widgets", ActionsRunsQuery{Branch: "main", Limit: 200})
	if err != nil {
		t.Fatalf("paged ListActionsRunsPage: %v", err)
	}
	if len(paged.Runs) != 101 || paged.Runs[100].ID != 999 || paged.TotalCount != 3410 {
		t.Fatalf("paged = %d runs, last=%+v, total=%d; want 101 runs ending in 999 with total 3410",
			len(paged.Runs), paged.Runs[len(paged.Runs)-1], paged.TotalCount)
	}
}

func intString(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
