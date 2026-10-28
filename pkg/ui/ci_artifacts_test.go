package ui_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/ui"
)

// artifactRunner is a fakeCIRunner that also keeps artifacts. The fake
// beside it keeps none: its artifact half is ci.NopRunner's, which
// answers as a deployment with no artifact store.
type artifactRunner struct {
	fakeCIRunner
	// available is whether this deployment keeps artifacts at all, which
	// is a different answer from a run that kept none.
	available bool
	held      map[string]string
	// merged records the run WithArtifacts was asked to fill in.
	merged []string
}

func (a *artifactRunner) RunArtifacts(_ context.Context, runID string) ([]ci.Artifact, bool, error) {
	if !a.available {
		return nil, false, nil
	}
	var out []ci.Artifact
	for key, body := range a.held {
		run, rest, _ := strings.Cut(key, "/")
		job, name, _ := strings.Cut(rest, "/")
		if run != runID {
			continue
		}
		out = append(out, ci.Artifact{Run: run, Job: job, Name: name, Stored: true, Bytes: int64(len(body))})
	}
	return out, true, nil
}

func (a *artifactRunner) OpenArtifact(_ context.Context, ref ci.ArtifactRef) (io.ReadCloser, ci.Artifact, error) {
	body, ok := a.held[ref.Run+"/"+ref.Job+"/"+ref.Name]
	if !ok {
		return nil, ci.Artifact{}, fmt.Errorf("no artifact %s: %w", ref, fs.ErrNotExist)
	}
	return io.NopCloser(strings.NewReader(body)), ci.Artifact{
		Run: ref.Run, Job: ref.Job, Name: ref.Name, Stored: true, Bytes: int64(len(body)),
	}, nil
}

func (a *artifactRunner) WithArtifacts(ctx context.Context, run ci.Run) ci.Run {
	a.merged = append(a.merged, run.ID)
	artifacts, _, _ := a.RunArtifacts(ctx, run.ID)
	for i := range run.Jobs {
		run.Jobs[i].Artifacts = artifacts
	}
	return run
}

func TestCIArtifactIsServedAsADownload(t *testing.T) {
	t.Parallel()
	runner := &artifactRunner{
		available: true,
		held:      map[string]string{"run-1/ui-e2e/playwright-test-results": "a gzipped tar, pretend"},
	}
	runner.runs = []ci.Run{{ID: "run-1", Repo: "acme/widgets", Jobs: []ci.JobRun{{JobID: "ui-e2e"}}}}
	srv, _ := ciServer(t, runner)

	rec := do(t, srv, http.MethodGet, "/api/ci/runs/run-1/artifacts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	listed := decode[ui.CIRunArtifactsResponse](t, rec)
	if !listed.Enabled || !listed.Available || len(listed.Artifacts) != 1 {
		t.Fatalf("listing = %+v", listed)
	}

	rec = do(t, srv, http.MethodGet, "/api/ci/runs/run-1/artifacts/ui-e2e/playwright-test-results", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "a gzipped tar, pretend" {
		t.Fatalf("body = %q, want the artifact's own bytes", got)
	}
	// The headers are the whole of what makes this a download rather
	// than a page: a browser following this link saves a file named
	// after the run and the job it came out of.
	if got := rec.Header().Get("Content-Type"); got != ci.ArtifactContentType {
		t.Errorf("Content-Type = %q, want %q", got, ci.ArtifactContentType)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "run-1-ui-e2e-playwright-test-results.tar.gz") {
		t.Errorf("Content-Disposition = %q", got)
	}

	// And a run served over the API says what is on disk now rather than
	// what was recorded when it finished.
	rec = do(t, srv, http.MethodGet, "/api/ci/runs/run-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	run := decode[ci.Run](t, rec)
	if len(run.Jobs[0].Artifacts) != 1 || run.Jobs[0].Artifacts[0].Name != "playwright-test-results" {
		t.Fatalf("the run was served with %+v", run.Jobs[0].Artifacts)
	}
}

func TestMissingCIArtifactIsANotFoundRatherThanAnError(t *testing.T) {
	t.Parallel()
	runner := &artifactRunner{available: true, held: map[string]string{}}
	srv, _ := ciServer(t, runner)

	// Swept, or never kept: both are "there is nothing here", which is
	// the same answer a forgotten run gets.
	rec := do(t, srv, http.MethodGet, "/api/ci/runs/run-1/artifacts/ui-e2e/traces", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	// A ref that could not name an artifact at all is refused before it
	// reaches the store, which is what keeps a URL from becoming a path.
	rec = do(t, srv, http.MethodGet, "/api/ci/runs/run-1/artifacts/ui-e2e/..%2f..%2fetc%2fpasswd", "")
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the ref refused: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "root:") {
		t.Fatal("a path traversal was served")
	}
}

// TestADeploymentThatKeepsNoArtifactsSaysSo: a ci.Runner with no
// artifacts behind it is an ordinary one, and the routes answer rather
// than failing.
func TestADeploymentThatKeepsNoArtifactsSaysSo(t *testing.T) {
	t.Parallel()
	srv, _ := ciServer(t, &fakeCIRunner{runs: []ci.Run{{ID: "run-1"}}})

	rec := do(t, srv, http.MethodGet, "/api/ci/runs/run-1/artifacts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	listed := decode[ui.CIRunArtifactsResponse](t, rec)
	if !listed.Enabled || listed.Available || len(listed.Artifacts) != 0 {
		t.Fatalf("listing = %+v, want enabled with nothing behind it", listed)
	}
	rec = do(t, srv, http.MethodGet, "/api/ci/runs/run-1/artifacts/job/traces", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	// The run itself is still served, artifacts or no artifacts.
	rec = do(t, srv, http.MethodGet, "/api/ci/runs/run-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}
