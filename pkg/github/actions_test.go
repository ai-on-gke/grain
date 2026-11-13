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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// TestAttemptReadersAskForEveryAttempt: the release train's gate counts a
// job's or a check run's failed attempt before its retry, so both readers
// ask GitHub for filter=all rather than its default of the latest alone,
// and carry what GitHub repeats of the run on each job.
func TestAttemptReadersAskForEveryAttempt(t *testing.T) {
	t.Parallel()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/repos/acme/widgets/actions/runs/42/jobs":
			if q.Get("filter") != "all" {
				t.Errorf("jobs asked with filter %q, want all", q.Get("filter"))
			}
			w.Write([]byte(`{"jobs":[{"id":7,"run_id":42,"run_attempt":1,"workflow_name":"nightly","head_sha":"abc",
				"name":"go-long","status":"completed","conclusion":"failure","created_at":"2026-11-01T10:00:00Z"},
				{"id":8,"run_id":42,"run_attempt":2,"workflow_name":"nightly","head_sha":"abc",
				"name":"go-long","status":"completed","conclusion":"success","created_at":"2026-11-01T11:00:00Z"}]}`))
		case "/repos/acme/widgets/commits/abc/check-runs":
			if q.Get("filter") != "all" || q.Get("check_name") != "grain/release-qualification" {
				t.Errorf("check runs asked with %v, want filter=all and check_name", q)
			}
			w.Write([]byte(`{"total_count":1,"check_runs":[{"id":5,"head_sha":"abc","name":"grain/release-qualification",
				"status":"completed","conclusion":"success","app":{"slug":"grain-forge"}}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(forge.Close)
	transport := &RealTransport{Host: strings.TrimPrefix(forge.URL, "http://"), ledger: &callerLedger{logf: func(string, ...any) {}}}
	client := NewClient(transport, StaticToken{strPtr("t")})

	jobs, err := client.ListActionsJobAttempts("acme", "widgets", 42)
	if err != nil || len(jobs) != 2 || jobs[0].RunAttempt != 1 || jobs[0].WorkflowName != "nightly" ||
		jobs[0].HeadSHA != "abc" || jobs[0].CreatedAt == nil {
		t.Fatalf("ListActionsJobAttempts = %+v, %v", jobs, err)
	}
	checks, err := client.ListCheckRunAttempts("acme", "widgets", "abc", "grain/release-qualification")
	if err != nil || len(checks) != 1 || checks[0].App != "grain-forge" || checks[0].HeadSHA != "abc" {
		t.Fatalf("ListCheckRunAttempts = %+v, %v", checks, err)
	}
}

// ghesLogForge is an Enterprise Server shaped for job logs: its REST API
// under /api/v3 on one host, and the logs endpoint answering 302 to a
// signed URL on a blob host of its own (blob), which wants no
// credential and must not be sent one.
func ghesLogForge(t *testing.T, blob http.HandlerFunc) (api *httptest.Server, blobAuth *[]string) {
	t.Helper()
	var auths []string
	blobSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		blob(w, r)
	}))
	t.Cleanup(blobSrv.Close)
	// The same server under another name: "localhost" rather than
	// 127.0.0.1, so the redirect crosses hosts as a real one does.
	blobURL := strings.Replace(blobSrv.URL, "127.0.0.1", "localhost", 1)
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widgets/actions/jobs/19190336/logs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "token t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, blobURL+"/logs/19190336.txt?sig=secret", http.StatusFound)
	}))
	t.Cleanup(api.Close)
	return api, &auths
}

func TestActionsJobLogFollowsAnEnterpriseServersRedirect(t *testing.T) {
	const log = "2026-11-04T03:10:00.0000000Z ##[group]Run ./verify.sh\n2026-11-04T03:10:02.0000000Z ERROR: image is not Google-linked\n"
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("transportFollows=%v", !stop), func(t *testing.T) {
			api, auths := ghesLogForge(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, log) })
			hc := &http.Client{}
			if stop {
				// A transport that hands the redirect back un-followed.
				hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			}
			transport := &RealTransport{Host: strings.TrimPrefix(api.URL, "http://"), PathPrefix: "/api/v3", Client: hc,
				ledger: &callerLedger{logf: func(string, ...any) {}}}
			client := NewClient(transport, StaticToken{strPtr("t")})
			got, err := client.ActionsJobLog("acme", "widgets", 19190336)
			if err != nil || got != log {
				t.Fatalf("ActionsJobLog = %q, %v; want the blob's log", got, err)
			}
			if len(*auths) != 1 || (*auths)[0] != "" {
				t.Errorf("the blob host saw Authorization %q; want one request carrying none", *auths)
			}
		})
	}
}

func TestActionsJobLogRefusedByTheBlobHostNamesStatusAndHost(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("transportFollows=%v", !stop), func(t *testing.T) {
			api, _ := ghesLogForge(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, "<Error>AuthenticationFailed</Error>")
			})
			hc := &http.Client{}
			if stop {
				// Followed here rather than by the transport: the error
				// must still name the blob host and not its signed URL.
				hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			}
			transport := &RealTransport{Host: strings.TrimPrefix(api.URL, "http://"), PathPrefix: "/api/v3", Client: hc,
				ledger: &callerLedger{logf: func(string, ...any) {}}}
			_, err := NewClient(transport, StaticToken{strPtr("t")}).ActionsJobLog("acme", "widgets", 19190336)
			var gh *Error
			if !errors.As(err, &gh) || gh.Status != http.StatusForbidden {
				t.Fatalf("ActionsJobLog error = %v; want a 403 *Error", err)
			}
			if !strings.Contains(err.Error(), "localhost") || !strings.Contains(err.Error(), api.URL) {
				t.Errorf("error %q should name both the API host and the blob host it was redirected to", err)
			}
			if strings.Contains(err.Error(), "sig=secret") || strings.Contains(err.Error(), "/logs/19190336.txt") {
				t.Errorf("error %q leaks the signed URL", err)
			}
		})
	}
}

func TestActionsJobLogUnreachableBlobHostKeepsTheSignedQueryOut(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("transportFollows=%v", !stop), func(t *testing.T) {
			api, _ := ghesLogForge(t, func(w http.ResponseWriter, r *http.Request) {
				// Drop the connection: the transport fails rather than answers.
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
			})
			hc := &http.Client{}
			if stop {
				hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			}
			transport := &RealTransport{Host: strings.TrimPrefix(api.URL, "http://"), PathPrefix: "/api/v3", Client: hc,
				ledger: &callerLedger{logf: func(string, ...any) {}}}
			_, err := NewClient(transport, StaticToken{strPtr("t")}).ActionsJobLog("acme", "widgets", 19190336)
			if err == nil {
				t.Fatal("ActionsJobLog succeeded against a blob host that hung up")
			}
			if strings.Contains(err.Error(), "sig=secret") || strings.Contains(err.Error(), "/logs/19190336.txt") {
				t.Errorf("error %q leaks the signed blob URL", err)
			}
			if !strings.Contains(err.Error(), "localhost") {
				t.Errorf("error %q should still name the blob host", err)
			}
		})
	}
}

// A job log the blob store answers 200 with no body is ErrEmptyJobLog,
// naming the status, both hosts and who the headers say answered, so the
// hop that lost the log can be told -- not a log that is just empty.
func TestActionsJobLogAnsweredEmptyNamesStatusHostsAndServer(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("transportFollows=%v", !stop), func(t *testing.T) {
			api, _ := ghesLogForge(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Server", "blobstore")
				w.WriteHeader(http.StatusOK)
			})
			hc := &http.Client{}
			if stop {
				hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			}
			transport := &RealTransport{Host: strings.TrimPrefix(api.URL, "http://"), PathPrefix: "/api/v3", Client: hc,
				ledger: &callerLedger{logf: func(string, ...any) {}}}
			got, err := NewClient(transport, StaticToken{strPtr("t")}).ActionsJobLog("acme", "widgets", 19190336)
			if !errors.Is(err, ErrEmptyJobLog) {
				t.Fatalf("ActionsJobLog = %q, %v; want ErrEmptyJobLog", got, err)
			}
			var gh *Error
			if !errors.As(err, &gh) || gh.Status != http.StatusOK {
				t.Fatalf("error %v should carry the 200 *Error", err)
			}
			for _, want := range []string{"localhost", "content-type text/plain", "server blobstore"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should name %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "sig=secret") {
				t.Errorf("error %q leaks the signed URL's query", err)
			}
		})
	}
}

// A window's ends are GitHub's own ?created= qualifier, so a listing of an
// older window is GitHub's runs of that window rather than the newest
// MaxActionsRunsLimit narrowed to it (grain/task-5274).
func TestListActionsRunsAsksForTheCreatedWindow(t *testing.T) {
	t.Parallel()
	var asked []string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("created"))
		w.Write([]byte(`{"total_count":0,"workflow_runs":[]}`))
	}))
	t.Cleanup(forge.Close)
	transport := &RealTransport{Host: strings.TrimPrefix(forge.URL, "http://"), ledger: &callerLedger{logf: func(string, ...any) {}}}
	client := NewClient(transport, StaticToken{strPtr("t")})

	since := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	for _, q := range []ActionsRunsQuery{{Since: since}, {Until: until}, {Since: since, Until: until}, {}} {
		if _, err := client.ListActionsRunsPage("acme", "widgets", q); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{">=2026-10-16T00:00:00Z", "<2026-11-06T00:00:00Z", "2026-10-16T00:00:00Z..2026-11-06T00:00:00Z", ""}
	if strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Fatalf("created = %q, want %q", asked, want)
	}
}
