package demo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestDemoBaseBranchHookDrivesARedAndGreenBranch is -base-branch-ci end to
// end against the demo's own store: red has grain's own reconciler
// confirm main red at the simulated forge's tip, file its repair and link
// the ready work on main to wait on it; red again re-judges nothing; and
// green closes the repair, which releases the work.
func TestDemoBaseBranchHookDrivesARedAndGreenBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, err := configureFakeCIDemo(ctx, store, ui.Config{}, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	cfg, bare, err := configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	tip, err := demoGit(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	client, _ := cfg.GitHubBranches.(github.Client)
	hook := configureBaseBranchDemo(store, repo, cfg.CI.(*demoFakeCI), client)

	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(ctx, model.Task{
		ID: "feature", Intent: model.IntentImplement, Title: "feature", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo, Base: "main", Binding: model.BindingDirective,
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(baseBranchDemoHandler(http.NotFoundHandler(), hook))
	t.Cleanup(srv.Close)
	post := func(verdict, body string) (int, demoBaseBranchResult, string) {
		t.Helper()
		res, err := http.Post(srv.URL+"/api/e2e/base-branch/"+verdict, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var out demoBaseBranchResult
		if res.StatusCode == http.StatusOK {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode, out, string(raw)
	}
	blockers := func(id string) int {
		t.Helper()
		n, err := store.OpenBlockers(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if code, _, _ := post("red", `{}`); code != http.StatusBadRequest {
		t.Errorf("red with no branch = %d, want 400", code)
	}
	if code, _, _ := post("blue", `{"branch":"main"}`); code != http.StatusNotFound {
		t.Errorf("an unknown verdict = %d, want the app's 404", code)
	}

	code, red, body := post("red", `{"branch":"main"}`)
	if code != http.StatusOK {
		t.Fatalf("red = %d: %s", code, body)
	}
	if red.Commit != tip || red.Conclusion != "failure" || red.Confirmation != model.BaseCIConfirmed || red.RepairTaskID != "" {
		t.Fatalf("red = %+v, want main confirmed red at %s with no repair filed by the daemon", red, tip)
	}
	// A red base holds nothing back: the work aimed at it is not linked
	// to anything (system:pipeline-steward coordinates a red branch).
	feature, err := store.GetTask(ctx, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(feature.Links) != 0 || blockers("feature") != 0 {
		t.Fatalf("feature has links %v and %d open blocker(s), want none on a red main",
			feature.Links, blockers("feature"))
	}

	// Red again is the same red.
	if code, again, body := post("red", `{"branch":"main"}`); code != http.StatusOK || again.Confirmation != model.BaseCIConfirmed {
		t.Errorf("red again = %d %+v %s, want main still confirmed red", code, again, body)
	}

	code, green, body := post("green", `{"branch":"main"}`)
	if code != http.StatusOK {
		t.Fatalf("green = %d: %s", code, body)
	}
	if green.Commit != tip || green.Conclusion != "success" {
		t.Fatalf("green = %+v, want main green at %s", green, tip)
	}

	// Standing work off: nothing is judged, and the refusal says why
	// rather than `concluded "": ` and nothing more.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo,
		Work: model.WorkOwnership{Default: model.OwnershipOff}}); err != nil {
		t.Fatal(err)
	}
	code, _, body = post("red", `{"branch":"main"}`)
	if code != http.StatusConflict || !strings.Contains(body, "base-branch check") {
		t.Errorf("red with standing work off = %d %q, want a 409 naming the base-branch check", code, body)
	}
}

// TestDemoBaseBranchHookAnswersLoopbackOnly: the hook writes a verdict
// nobody earned, so a caller from anywhere else is refused before it is
// read.
func TestDemoBaseBranchHookAnswersLoopbackOnly(t *testing.T) {
	h := baseBranchDemoHandler(http.NotFoundHandler(), &demoBaseBranch{})
	req := httptest.NewRequest(http.MethodPost, "/api/e2e/base-branch/red", strings.NewReader(`{"branch":"main"}`))
	req.RemoteAddr = "192.0.2.7:4242"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a non-loopback caller got %d, want 403", rec.Code)
	}
}
