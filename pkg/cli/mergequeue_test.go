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

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// mqTestQueue is a merge queue over two repositories: acme/widgets, which
// the caller may read, and acme/secret, which it may not.
type mqTestQueue struct{}

func (mqTestQueue) QueryMergeQueue(_ context.Context, repo, _ string) (mcp.MergeQueueReport, error) {
	all := mcp.MergeQueueReport{
		HeldGates: []mcp.HeldGateReport{
			{Repo: "acme/widgets", Branch: "main", Gate: "base-green", Status: "held", Reason: "main is red"},
			{Repo: "acme/secret", Branch: "main", Gate: "base-green", Status: "held", Reason: "secret is red"},
		},
		Batches: []mcp.MergeBatchReport{
			{Repo: "acme/widgets", Branch: "main", RunID: "run-w", Conclusion: "running",
				Members: []mcp.MergeBatchMemberReport{{TaskID: "7", Number: 7, Title: "Fix widgets"}}},
		},
		QueuedPRs: []mcp.QueuedPRReport{
			{TaskID: "7", Number: 7, Title: "Fix widgets", Repo: "acme/widgets", Branch: "main", State: "testing"},
			{TaskID: "9", Number: 9, Title: "Polish gadgets", Repo: "acme/gadgets", Branch: "main", State: "queued"},
			{TaskID: "8", Number: 8, Title: "Leak secrets", Repo: "acme/secret", Branch: "main", State: "queued"},
		},
	}
	if repo == "" {
		return all, nil
	}
	narrowed := mcp.MergeQueueReport{Repo: repo}
	for _, g := range all.HeldGates {
		if g.Repo == repo {
			narrowed.HeldGates = append(narrowed.HeldGates, g)
		}
	}
	for _, b := range all.Batches {
		if b.Repo == repo {
			narrowed.Batches = append(narrowed.Batches, b)
		}
	}
	for _, p := range all.QueuedPRs {
		if p.Repo == repo {
			narrowed.QueuedPRs = append(narrowed.QueuedPRs, p)
		}
	}
	return narrowed, nil
}

// mqTestDeploy is deploy://, answering the merge queue as the daemon's
// own provider does: through the query_merge_queue tool's rendering, or
// its report as JSON, limited to what Permits allows.
type mqTestDeploy struct{ fsTestDeployBase }

func (mqTestDeploy) ReadMergeQueueQuery(ctx context.Context, q namespace.MergeQueueQuery) (string, error) {
	var reader mcp.MergeQueueReader = mqTestQueue{}
	if q.Permits != nil {
		reader = mcp.RepoFilteredMergeQueue{Reader: reader, Permits: q.Permits}
	}
	if q.JSON {
		report, err := reader.QueryMergeQueue(ctx, q.Repo, "")
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(report)
		return string(raw) + "\n", err
	}
	res := mcp.NewMergeQueueTools(reader)[0].Handler(ctx, map[string]any{"view": "full", "repo": q.Repo})
	return res.Text + "\n", nil
}

func (d mqTestDeploy) ReadMergeQueue(ctx context.Context) (string, error) {
	return d.ReadMergeQueueQuery(ctx, namespace.MergeQueueQuery{})
}

func (d mqTestDeploy) ReadMergeQueueFor(ctx context.Context, permits func(string) bool) (string, error) {
	return d.ReadMergeQueueQuery(ctx, namespace.MergeQueueQuery{Permits: permits})
}

// fsTestDeployBase is the rest of a DeployProvider, which these tests
// never read.
type fsTestDeployBase struct{}

func (fsTestDeployBase) ReadMetrics(context.Context, string, namespace.MetricsQuery) (string, error) {
	return "", namespace.ErrNotFound
}
func (fsTestDeployBase) ReadPipelineChanges(context.Context, string, int) (string, error) {
	return "", namespace.ErrNotFound
}
func (fsTestDeployBase) ReadHealth(context.Context) (string, error) { return "", namespace.ErrNotFound }
func (fsTestDeployBase) ReadState(context.Context, string) (string, error) {
	return "", namespace.ErrNotFound
}
func (fsTestDeployBase) ListStateTables(context.Context) ([]string, error)     { return nil, nil }
func (fsTestDeployBase) ListGrainLogSources(context.Context) ([]string, error) { return nil, nil }
func (fsTestDeployBase) ReadGrainLog(context.Context, string, namespace.GrainLogQuery) (string, error) {
	return "", namespace.ErrNotFound
}

// mergeQueueCLI runs `grain merge-queue args...` against a namespace
// served at /api/fs/ as pkg/ui serves it, to a caller holding ci:read on
// acme/widgets and acme/gadgets only (MergeQueueByCI).
func mergeQueueCLI(t *testing.T, asJSON bool, args ...string) (string, string, error) {
	t.Helper()
	ns := &namespace.Server{Deploy: mqTestDeploy{}}
	scope := namespace.Scope{
		MayReadCI:      true,
		AllowedRepos:   map[string]bool{"acme/widgets": true, "acme/gadgets": true},
		MergeQueueByCI: true,
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		in := r.Clone(r.Context())
		in.URL.Path = "/fs/" + strings.TrimPrefix(r.URL.Path, "/api/fs/")
		ns.ServeHTTP(w, in, scope)
	}))
	t.Cleanup(srv.Close)
	var err error
	out := captureStdout(t, func() {
		err = cmdMergeQueue(context.Background(), ui.NewHTTPClient(srv.URL), &printer{json: asJSON}, args)
	})
	return out, strings.Join(asked, "\n"), err
}

// `grain merge-queue` (#XC-27) reads deploy://merge-queue over GET
// /api/fs/, as a sandbox's `grain merge-queue` reads it over its proxy:
// queued pull requests, the batches testing them and held gates, never
// a repository the caller may not read, narrowed further by -repo, and
// as the report's JSON under -json.
func TestExternalMergeQueueFiltersByRepoAndJSON(t *testing.T) {
	out, asked, err := mergeQueueCLI(t, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asked, "/api/fs/deploy/merge-queue?") {
		t.Errorf("the merge queue should be read from deploy://merge-queue, asked %q", asked)
	}
	for _, want := range []string{"Fix widgets", "Polish gadgets", "main is red", "run-w"} {
		if !strings.Contains(out, want) {
			t.Errorf("the merge queue does not show %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{"acme/secret", "Leak secrets", "secret is red"} {
		if strings.Contains(out, leak) {
			t.Errorf("the merge queue shows %q, of a repository the caller may not read:\n%s", leak, out)
		}
	}

	out, asked, err = mergeQueueCLI(t, false, "-repo", "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "repo=acme%2Fwidgets") {
		t.Errorf("-repo should be asked of the daemon: %q", asked)
	}
	if !strings.Contains(out, "Fix widgets") || strings.Contains(out, "Polish gadgets") {
		t.Errorf("-repo acme/widgets should show that repository's queue alone:\n%s", out)
	}

	out, asked, err = mergeQueueCLI(t, true, "-repo", "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "format=json") {
		t.Errorf("-json should ask for the JSON report: %q", asked)
	}
	var report mcp.MergeQueueReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if report.Repo != "acme/widgets" || len(report.QueuedPRs) != 1 || report.QueuedPRs[0].TaskID != "7" ||
		len(report.Batches) != 1 || len(report.HeldGates) != 1 || report.HeldGates[0].Gate != "base-green" {
		t.Errorf("report = %+v", report)
	}
	if !strings.Contains(out, `"queuedPrs"`) || !strings.Contains(out, `"heldGates"`) {
		t.Errorf("the JSON report should use its own field names:\n%s", out)
	}

	// A repository the caller may not read answers an empty queue, not
	// that repository's.
	out, _, err = mergeQueueCLI(t, true, "-repo", "acme/secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Leak secrets") || strings.Contains(out, "secret is red") {
		t.Errorf("-repo cannot widen what the caller may read:\n%s", out)
	}

	if _, _, err := mergeQueueCLI(t, false, "-repo", "not a repo"); err == nil {
		t.Error("a -repo that is not owner/name should be refused")
	}
	if _, _, err := mergeQueueCLI(t, false, "extra"); err == nil {
		t.Error("grain merge-queue takes no arguments")
	}
}
