package ui_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type repoComparer struct {
	gotRepo          model.RepoRef
	gotBase, gotHead string
	result           ui.BranchComparison
	err              error
}

func (c *repoComparer) Compare(_ context.Context, repo model.RepoRef, base, head string) (ui.BranchComparison, error) {
	c.gotRepo, c.gotBase, c.gotHead = repo, base, head
	return c.result, c.err
}

func TestCompareBranchesReturnsAForgeNeutralDiff(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	comparer := &repoComparer{result: ui.BranchComparison{
		Base: "main", Head: "topic/change", Diff: "diff --git a/a.txt b/a.txt\n",
	}}
	client.Config.RepoComparer = comparer
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet,
		"/api/repos/acme/widgets/compare?base=main&head=topic%2Fchange", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if comparer.gotRepo.String() != "acme/widgets" || comparer.gotBase != "main" || comparer.gotHead != "topic/change" {
		t.Fatalf("Compare got %s %q...%q", comparer.gotRepo, comparer.gotBase, comparer.gotHead)
	}
	if !strings.Contains(rec.Body.String(), "\"diff\":\"diff --git") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCompareBranchesRequiresAHead(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.RepoComparer = &repoComparer{}
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet,
		"/api/repos/acme/widgets/compare?base=main", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestCompareBranchesLeavesAnOmittedBaseForTheComparerToResolve(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	comparer := &repoComparer{result: ui.BranchComparison{Base: "trunk", Head: "grain/task-1"}}
	client.Config.RepoComparer = comparer
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet,
		"/api/repos/acme/widgets/compare?head=grain%2Ftask-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if comparer.gotBase != "" || comparer.gotHead != "grain/task-1" {
		t.Fatalf("Compare got %q...%q", comparer.gotBase, comparer.gotHead)
	}
}

func TestCompareBranchesClassifiesRepositoryFailures(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.RepoComparer = &repoComparer{err: errors.New("branch does not exist")}
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet,
		"/api/repos/acme/widgets/compare?base=main&head=missing", "")
	// The upstream's own text stays in the daemon's log (bwsalmon/grain#2424);
	// the caller gets its classification and the reference to find it.
	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway || strings.Contains(body, "branch does not exist") ||
		!strings.Contains(body, `"code":"upstream_unavailable"`) || !strings.Contains(body, `"ref":`) {
		t.Fatalf("status = %d, body = %q", rec.Code, body)
	}
}

// A branch GitHub does not have is a name somebody typed or followed a
// stale link to, not an outage: the page says which comparison it could
// not find, in grain's words, rather than "an upstream service returned
// an error" -- and still none of GitHub's own text.
func TestCompareBranchesAnswersAnUnknownBranchAsNotFound(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.RepoComparer = &repoComparer{err: fmt.Errorf("comparing main and missing in acme/widgets: %w",
		&github.Error{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found","secret":"upstream"}`)})}
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet,
		"/api/repos/acme/widgets/compare?base=main&head=missing", "")
	body := rec.Body.String()
	if rec.Code != http.StatusNotFound ||
		!strings.Contains(body, "main...missing in acme/widgets") ||
		strings.Contains(body, "upstream") {
		t.Fatalf("status = %d, body = %q", rec.Code, body)
	}

	rec = do(t, ui.NewServerWithClient(client), http.MethodGet,
		"/api/repos/acme/widgets/compare?head=missing", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "the default branch...missing") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}
