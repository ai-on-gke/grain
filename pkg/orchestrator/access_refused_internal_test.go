package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// refusingSandbox is a sandbox whose clone the git proxy refused with
// output, the way a team credential that cannot see the repository is
// refused: git exits 128 and prints the proxy's explanation as remote:
// lines.
func refusingSandbox(output string) []mcp.Tool {
	return []mcp.Tool{{Name: "run_command", Handler: func(context.Context, map[string]any) mcp.Result {
		return mcp.Result{Text: "exit=128\n" + output, IsError: true}
	}}}
}

// A team whose GitHub credential cannot reach the task's repository fails
// the same way on every attempt, before an agent exists. Task runs clone
// through the git proxy as the team, with no deployment fallback
// (model.Store.TaskGitCredential, grain/task-1592), so this is the path
// they take -- and the first refusal is the last: the task is parked on
// the proxy's own explanation rather than retried until
// MaxConsecutiveFailures retires it, a sandbox spent on each attempt.
func TestAccessRefusedCloneParksAtTheFirstFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "private"}
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	task := model.Task{
		ID: "t1", Title: "work on a repo the team cannot see", Target: &repo,
		Origin:   model.Origin{Attribution: human, Reason: model.ReasonDirect},
		Approval: &human, Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	refusal := "remote: acme/private: GitHub answered 404 to a request sent with the \"bouncer\" credential. " +
		"Either no such repository exists, or that credential cannot see it\n" +
		"fatal: repository 'http://172.17.0.1:9000/acme/private.git/' not found"
	_, err := prepareCheckout(ctx, refusingSandbox(refusal), "http://proxy", task, "", setupNotes{})
	var refused *accessRefusedError
	if !errors.As(err, &refused) || refused.Repo != repo {
		t.Fatalf("prepareCheckout error = %#v; want an *accessRefusedError naming %s", err, repo)
	}
	if !strings.Contains(err.Error(), "cloning acme/private into work") {
		t.Errorf("error = %v; want the clone failure's own wording kept", err)
	}

	if err := stopOnAccessRefused(ctx, store, task, refused, now); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want awaiting_reply -- not queued for another attempt", st, err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments = %+v (%v), want one", comments, err)
	}
	for _, want := range []string{"the deployment's GitHub credential", "acme/private", "GitHub answered 404", "Settings → Git", "reply"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("comment %q does not mention %q", comments[0].Body, want)
		}
	}
}

// A clone that failed for any other reason -- the proxy unreachable, a
// 5xx, a network blip -- is not an access refusal, and stays retryable.
func TestOtherCloneFailuresAreNotAccessRefusals(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{ID: "t1", Target: &repo}
	_, err := prepareCheckout(context.Background(),
		refusingSandbox("fatal: unable to access 'http://172.17.0.1:9000/acme/widgets.git/': The requested URL returned error: 502"),
		"http://proxy", task, "", setupNotes{})
	if err == nil {
		t.Fatal("prepareCheckout succeeded on a failed clone")
	}
	var refused *accessRefusedError
	if errors.As(err, &refused) {
		t.Errorf("error = %v; a 502 was read as an access refusal", err)
	}
}
