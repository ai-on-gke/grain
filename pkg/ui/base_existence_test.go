package ui_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ui"
)

// existenceServer is a server whose GitHub branch listing for
// acme/widgets is lister, behind a credential for that repo, so the
// write-time existence check (grain/task-1620) has something to ask.
func existenceServer(t *testing.T, lister *branchLister) (*ui.Server, *ui.Client) {
	t.Helper()
	client, _, _ := testClient(t)
	client.Config.GitHubBranches = lister
	client.Config.Credentials = credentialSet(t)
	return ui.NewServerWithClient(client), client
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func wantMissing(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "does not exist on acme/widgets") {
		t.Fatalf("body = %s, want it to say the base does not exist", rec.Body)
	}
}

// A one-shot task naming a well-formed base GitHub does not have is
// refused when it is filed, and when an edit moves it there, rather than
// queued to fail at dispatch.
func TestTaskOnABaseGitHubDoesNotHaveIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := existenceServer(t, &branchLister{names: []string{"main", "release/1.2"}})

	t.Run("create", func(t *testing.T) {
		wantMissing(t, do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/widgets","base":"no-such-branch"}`))
	})

	t.Run("create with a missing write-target base", func(t *testing.T) {
		wantMissing(t, do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/gadgets","writes":[{"repo":"acme/widgets","base":"no-such-branch"}]}`))
	})

	t.Run("existing branches and the default are accepted", func(t *testing.T) {
		for _, base := range []string{"", "main", "release/1.2"} {
			wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
				`{"title":"t","description":"d","repo":"acme/widgets","base":"`+base+`"}`), http.StatusCreated)
		}
	})

	t.Run("patch", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/widgets","base":"main"}`)
		wantStatus(t, rec, http.StatusCreated)
		task := decode[ui.Task](t, rec)
		wantMissing(t, do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"base":"no-such-branch"}`))
		wantStatus(t, do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"base":"release/1.2"}`), http.StatusOK)
	})
}

// A task that waits on another may be waiting on exactly the work that
// cuts its branch, so it is not refused; nor is a base grain has itself
// been asked to create and has not cut yet.
func TestTaskOnAMissingBaseIsAcceptedWhenSomethingWillCreateIt(t *testing.T) {
	t.Parallel()
	srv, _ := existenceServer(t, &branchLister{names: []string{"main"}})

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"cut it","description":"d","repo":"acme/widgets"}`)
	wantStatus(t, rec, http.StatusCreated)
	blocker := decode[ui.Task](t, rec)
	wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":"feature/later","dependsOn":["`+blocker.ID+`"]}`),
		http.StatusCreated)

	wantStatus(t, do(t, srv, http.MethodPost, "/api/repos/acme/widgets/branches",
		`{"name":"feature/requested"}`), http.StatusCreated)
	wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":"feature/requested"}`), http.StatusCreated)
}

// Grain being unable to tell is not evidence the branch is absent: with
// GitHub failing, no credential for the repo, or no lister at all, a
// well-formed base is accepted and dispatch stays the backstop.
func TestTaskBaseIsAcceptedWhenGitHubCannotSay(t *testing.T) {
	t.Parallel()
	body := `{"title":"t","description":"d","repo":"acme/widgets","base":"no-such-branch"}`

	t.Run("GitHub failing", func(t *testing.T) {
		srv, _ := existenceServer(t, &branchLister{err: errors.New("502 from GitHub")})
		wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks", body), http.StatusCreated)
	})
	t.Run("no lister", func(t *testing.T) {
		srv, _ := testServer(t)
		wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks", body), http.StatusCreated)
	})
	t.Run("no credential for the repo", func(t *testing.T) {
		srv, _ := existenceServer(t, &branchLister{names: []string{"main"}})
		wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/gadgets","base":"no-such-branch"}`), http.StatusCreated)
	})
}

// Schedules and templates are written ahead of the branch they name --
// a release branch the release-schedules reconciler cuts later -- so a
// missing base is accepted there; the forms warn instead.
func TestScheduleAndTemplateMayNameABaseNotCutYet(t *testing.T) {
	t.Parallel()
	lister := &branchLister{names: []string{"main"}}
	srv, _ := existenceServer(t, lister)

	wantStatus(t, do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"nightly","repo":"acme/widgets","base":"release/9.9","recurrence":{"kind":"everyNHours","everyNHours":24}}`),
		http.StatusCreated)
	wantStatus(t, do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"n","title":"t","repo":"acme/widgets","base":"release/9.9"}`), http.StatusCreated)
	if lister.calls != 0 {
		t.Fatalf("GitHub calls = %d, want none: schedules and templates are not checked", lister.calls)
	}
}

// A branch pushed after the listing was cached is not refused for the
// rest of the cache's five minutes: a miss on a listing older than a few
// seconds asks GitHub once more before refusing.
func TestTaskBaseMissFromAStaleListingAsksGitHubAgain(t *testing.T) {
	t.Parallel()
	lister := &branchLister{names: []string{"main"}}
	srv, client := existenceServer(t, lister)
	now := time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)
	client.Now = func() time.Time { return now }

	wantMissing(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":"just-pushed"}`))
	if lister.calls != 1 {
		t.Fatalf("GitHub calls = %d, want 1: a fresh listing is not asked again", lister.calls)
	}

	lister.names = []string{"main", "just-pushed"}
	now = now.Add(time.Minute)
	wantStatus(t, do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"t","description":"d","repo":"acme/widgets","base":"just-pushed"}`), http.StatusCreated)
	if lister.calls != 2 {
		t.Fatalf("GitHub calls = %d, want 2", lister.calls)
	}
}
