package ui_test

// Branch creation's own tests (bwsalmon/agents#638), following
// releases_test.go's own discipline: a real embedded SQLite store, no
// GitHub anywhere in sight -- CreateBranch only ever writes a row;
// pkg/orchestrator.SyncBranches (its own tests) is what actually talks to
// GitHub.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

type branchLister struct {
	names []string
	err   error
	calls int
}

func (l *branchLister) ListBranches(owner, repo string) ([]string, error) {
	l.calls++
	return l.names, l.err
}

func credentialSet(t *testing.T) *gitproxy.CredentialSet {
	t.Helper()
	set, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := set.SetToken("bot", "secret"); err != nil {
		t.Fatal(err)
	}
	set.SetConnections(func(owner, repo string) (string, bool) {
		return "bot", owner == "acme" && repo == "widgets"
	})
	return set
}

func TestListGitHubBranchesCachesARepository(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	lister := &branchLister{names: []string{"main", "topic/new"}}
	client.Config.GitHubBranches = lister
	client.Config.Credentials = credentialSet(t)
	for range 2 {
		got, err := client.ListGitHubBranches(ctx, widgets)
		if err != nil || len(got) != 2 || got[1] != "topic/new" {
			t.Fatalf("got %v, %v", got, err)
		}
	}
	if lister.calls != 1 {
		t.Fatalf("GitHub calls = %d, want 1", lister.calls)
	}
}

func TestGitHubBranchesWithoutARepoCredentialIsExplicit(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.GitHubBranches = &branchLister{names: []string{"main"}}
	set, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = set
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet, "/api/repos/acme/widgets/github-branches", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "no GitHub credential is configured for this repository") {
		t.Fatalf("body = %q", body)
	}
}

// localBranches is a ui.RepoBranchSource serving the repos named in
// names, and nothing else.
type localBranches struct {
	names map[string][]string
	err   error
	asked []string
}

func (l *localBranches) RepoBranches(_ context.Context, repo model.RepoRef) ([]string, bool, error) {
	l.asked = append(l.asked, repo.String())
	names, ok := l.names[repo.String()]
	if !ok {
		return nil, false, nil
	}
	return names, true, l.err
}

// mustNotCallGitHub fails the test the moment anything asks GitHub for a
// branch listing.
type mustNotCallGitHub struct{ t *testing.T }

func (m mustNotCallGitHub) ListBranches(owner, repo string) ([]string, error) {
	m.t.Errorf("GitHub asked for %s/%s's branches", owner, repo)
	return nil, errors.New("GitHub API error 404")
}

// grain/task-473: the controller-local config repository's branches come
// from its own checkout. GitHub has no grain/config, and asking it was a
// 404 the branch pickers showed as a 502.
func TestConfigRepoBranchesAreReadLocallyAndNeverFromGitHub(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	client.Config.GitHubBranches = mustNotCallGitHub{t}
	// A credential that would cover grain/config if it were asked: the
	// failure on the host was exactly a credential that matched.
	client.Config.GitHubCredentialAvailable = func(owner, repo string) bool { return true }
	client.Config.RepoBranches = &localBranches{names: map[string][]string{
		"grain/config": {"grain/049f1e09/task-465", "main"},
	}}
	got, err := client.ListGitHubBranches(ctx, model.ConfigRepo())
	if err != nil || strings.Join(got, ",") != "grain/049f1e09/task-465,main" {
		t.Fatalf("got %v, %v", got, err)
	}
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet, "/api/repos/grain/config/github-branches", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "task-465") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

// With no local source wired, the config repository says plainly that
// its branches cannot be listed here -- a 404, never GitHub's 404 relayed
// as a 502.
func TestConfigRepoBranchesWithoutALocalSourceAreNotAskedOfGitHub(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.GitHubBranches = mustNotCallGitHub{t}
	client.Config.GitHubCredentialAvailable = func(owner, repo string) bool { return true }
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet, "/api/repos/grain/config/github-branches", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "internal config repository") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

// A local source failing is this server's failure (500), not an upstream
// GitHub one (502), and GitHub is not asked instead.
func TestConfigRepoBranchFailureIsNotAnUpstreamError(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.GitHubBranches = mustNotCallGitHub{t}
	client.Config.RepoBranches = &localBranches{
		names: map[string][]string{"grain/config": nil}, err: errors.New("git for-each-ref: exit 128"),
	}
	rec := do(t, ui.NewServerWithClient(client), http.MethodGet, "/api/repos/grain/config/github-branches", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

// A repository on a plain git host is listed over git by the same source,
// and GitHub -- which would be asked about some other repository -- is not
// asked at all; a GitHub repository still is.
func TestPlainGitRepoBranchesComeFromTheLocalSource(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	plain := model.RepoRef{Host: "git.example.com", Owner: "walk", Name: "demo"}
	src := &localBranches{names: map[string][]string{plain.String(): {"main", "topic"}}}
	client.Config.RepoBranches = src
	client.Config.GitHubBranches = mustNotCallGitHub{t}
	got, err := client.ListGitHubBranches(ctx, plain)
	if err != nil || strings.Join(got, ",") != "main,topic" {
		t.Fatalf("got %v, %v", got, err)
	}

	lister := &branchLister{names: []string{"main"}}
	client.Config.GitHubBranches = lister
	client.Config.Credentials = credentialSet(t)
	if got, err := client.ListGitHubBranches(ctx, widgets); err != nil || len(got) != 1 {
		t.Fatalf("GitHub repo: got %v, %v", got, err)
	}
	if lister.calls != 1 {
		t.Fatalf("GitHub calls for a GitHub repo = %d, want 1", lister.calls)
	}
}

func TestCreateBranchRecordsAFreshBranch(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	b, err := client.CreateBranch(ctx, widgets, ui.CreateBranchRequest{Name: "myfeat"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Repo != "acme/widgets" || b.Name != "myfeat" || b.Status != "pending" {
		t.Fatalf("got %+v", b)
	}
}

func TestCreateBranchTrimsWhitespaceFromTheName(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	b, err := client.CreateBranch(ctx, widgets, ui.CreateBranchRequest{Name: "  myfeat  "})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "myfeat" {
		t.Fatalf("got name %q, want it trimmed to %q", b.Name, "myfeat")
	}
}

func TestCreateBranchRejectsAnEmptyName(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CreateBranch(ctx, widgets, ui.CreateBranchRequest{}); err == nil {
		t.Fatal("expected a validation error for an empty name")
	}
}

// A refused name is answered with the rule, not only the verdict
// (2026-10-23 repository walkthrough, task 273): "invalid branch name"
// alone left the operator guessing which character git objected to.
func TestCreateBranchRefusalSpellsOutTheRule(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	for _, tc := range []struct{ name, from, bad string }{
		{name: "bad branch..name", bad: "bad branch..name"},
		{name: "feature//x", bad: "feature//x"},
		{name: "ok-name", from: "main..", bad: "main.."},
	} {
		_, err := client.CreateBranch(ctx, widgets, ui.CreateBranchRequest{Name: tc.name, From: tc.from})
		if err == nil {
			t.Fatalf("%q from %q: expected a validation error", tc.name, tc.from)
		}
		var verr *ui.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("%q: error %T %v is not a validation error", tc.name, err, err)
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, fmt.Sprintf("invalid branch name %q: ", tc.bad)) ||
			!strings.Contains(msg, `cannot contain ".." or "//" or end in "/"`) {
			t.Fatalf("%q from %q: message = %q", tc.name, tc.from, msg)
		}
	}
}

func TestCreateBranchTwiceAnswersWithTheExistingRow(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	body := `{"name":"walk/topic-1004"}`
	if rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/branches", body); rec.Code != http.StatusCreated {
		t.Fatalf("first add: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/branches", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("second add: status = %d, body = %q, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"alreadyRequested":true`) || !strings.Contains(got, `"status":"pending"`) {
		t.Fatalf("second add: body = %q, want the pending row marked alreadyRequested", got)
	}
	got, err := client.ListBranches(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "walk/topic-1004" || got[0].AlreadyRequested {
		t.Fatalf("got %+v, want one walk/topic-1004 row", got)
	}
}

func TestListBranchesListsEveryBranchForARepoNewestFirst(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CreateBranch(ctx, widgets, ui.CreateBranchRequest{Name: "myfeat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateBranch(ctx, widgets, ui.CreateBranchRequest{Name: "otherfeat"}); err != nil {
		t.Fatal(err)
	}
	got, err := client.ListBranches(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "otherfeat" || got[1].Name != "myfeat" {
		t.Fatalf("got %+v, want otherfeat then myfeat", got)
	}
}

// The repo page's "add a branch" and "create a release" both end in a
// reconciler that goes to GitHub, which has no grain/config: refused
// plainly, with nothing recorded to sit pending forever.
func TestConfigRepoRefusesBranchAndReleaseCreation(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	if _, err := client.CreateBranch(ctx, model.ConfigRepo(), ui.CreateBranchRequest{Name: "topic"}); err == nil ||
		!strings.Contains(err.Error(), "internal config repository") {
		t.Fatalf("CreateBranch(grain/config) err = %v", err)
	}
	if got, err := client.ListBranches(ctx, model.ConfigRepo()); err != nil || len(got) != 0 {
		t.Fatalf("recorded branches = %v, %v; want none", got, err)
	}
	if got := client.ReleaseCreation(ctx, model.ConfigRepo()); got.Allowed || !strings.Contains(got.Reason, "internal config repository") {
		t.Fatalf("ReleaseCreation(grain/config) = %+v", got)
	}
	if _, err := client.CreateRelease(ctx, model.ConfigRepo(), ui.CreateReleaseRequest{Name: "v1"}); err == nil {
		t.Fatal("CreateRelease(grain/config) succeeded")
	}
}
