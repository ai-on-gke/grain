package orchestrator_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// The prompt extension reaching a real dispatch (grain/task-114), across
// all three layers that can carry one. These drive RunDispatch rather
// than a prompt-building function directly, because the whole point of
// the feature is that a *run* is told: two of the three layers are rows
// the dispatch path has to go and read (grain_config through
// orchestrator.Config, repo_config through the task's own target), and a
// test of the composition alone would pass with either of those reads
// missing -- which is exactly the way this can silently do nothing.

// promptFor dispatches taskID and returns the prompt the framework was
// given.
func promptFor(t *testing.T, ctx context.Context, store *model.Store, cfg orchestrator.Config, taskID, runID string) string {
	t.Helper()
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("reading task %s: %v", taskID, err)
	}
	d := dispatch.Dispatch{TaskID: taskID, RunID: runID, Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	var prompt string
	fw := agentFunc(func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
		prompt = rc.Prompt
		return pushed(), nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	return prompt
}

func TestRunDispatchAppendsTheDeploymentsPromptExtension(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := dispatchTask(t, ctx, store, "t1")

	const text = "Run `make lint` before you push."
	cfg := orchestrator.Config{PromptExtension: text}
	prompt := promptFor(t, ctx, store, cfg, "t1", "r1")

	if !strings.Contains(prompt, text) {
		t.Errorf("prompt does not carry the deployment's own standing instructions: %q", prompt)
	}
	// Last, deliberately (prepareCapabilities' own doc comment): it is
	// the one part of the prompt a human wrote for runs in general, and
	// where it sits is what keeps it from reading as an aside to
	// whatever section happened to precede it.
	if !strings.HasSuffix(prompt, text) {
		t.Errorf("the extension is not the last thing in the prompt: %q", prompt)
	}
	// A deployment that has written none is a prompt with nothing
	// appended at all -- no empty heading, no trailing blank lines.
	if bare := promptFor(t, ctx, store, orchestrator.Config{}, "t1", "r2"); withoutClock(bare) != withoutClock(orchestrator.BuildPrompt(
		task, "", "", false,
		orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)) {
		t.Errorf("with no extension configured, prompt = %q, want exactly BuildPrompt's own", bare)
	}
}

func TestRunDispatchAppendsTheTargetRepoOwnPromptExtension(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := dispatchTask(t, ctx, store, "t1")

	const deployment = "Run `make lint` before you push."
	const repo = "Widgets keeps its migrations in db/, read db/README.md first."
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: *task.Target, PromptExtension: repo}); err != nil {
		t.Fatalf("configuring the repo: %v", err)
	}

	prompt := promptFor(t, ctx, store, orchestrator.Config{PromptExtension: deployment}, "t1", "r1")
	if !strings.Contains(prompt, deployment) || !strings.Contains(prompt, repo) {
		t.Errorf("prompt does not carry both layers: %q", prompt)
	}
	// Appended, in that order: a repo adds to what the deployment says
	// and can never replace it (model.PromptExtensionFor).
	if strings.Index(prompt, deployment) > strings.Index(prompt, repo) {
		t.Errorf("the repo's text comes before the deployment's: %q", prompt)
	}
}

func TestRunDispatchLetsATaskOverrideBothPromptExtensions(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := dispatchTask(t, ctx, store, "t1")

	const deployment = "Run `make lint` before you push."
	const repo = "Widgets keeps its migrations in db/."
	const override = "Ignore the house rules: this task is regenerating them."
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: *task.Target, PromptExtension: repo}); err != nil {
		t.Fatalf("configuring the repo: %v", err)
	}
	task.PromptExtension = override
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("overriding the task's own: %v", err)
	}

	prompt := promptFor(t, ctx, store, orchestrator.Config{PromptExtension: deployment}, "t1", "r1")
	if !strings.Contains(prompt, override) {
		t.Errorf("prompt does not carry the task's own override: %q", prompt)
	}
	// Replaces, rather than adding to: an override that could only
	// append would leave no way to run one task without instructions
	// that are wrong for it.
	if strings.Contains(prompt, deployment) || strings.Contains(prompt, repo) {
		t.Errorf("an overriding task was still told what it overrode: %q", prompt)
	}
}

// A task with no repo at all has no repo_config row to key on, and must
// still be told what the deployment says -- that being the only layer
// that could apply to it.
func TestRunDispatchGivesATaskWithNoRepoTheDeploymentsPromptExtension(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := dispatchTask(t, ctx, store, "t1")
	task.Target = nil
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("clearing the task's target: %v", err)
	}

	const text = "Say what you tried, even when it did not work."
	prompt := promptFor(t, ctx, store, orchestrator.Config{PromptExtension: text}, "t1", "r1")
	if !strings.Contains(prompt, text) {
		t.Errorf("prompt does not carry the deployment's own standing instructions: %q", prompt)
	}
}

// promptClock matches the wall-clock minute BuildPrompt writes into its
// timezone line (timezoneSection reads time.Now()).
var promptClock = regexp.MustCompile(`\(current time: [^)]*\)`)

// withoutClock blanks that minute out, so a test comparing a dispatched
// run's prompt with a second BuildPrompt call compares the prompts
// rather than when each was built: the two are built moments apart, and
// a minute boundary between them used to fail the comparison on a
// prompt that was otherwise identical.
func withoutClock(prompt string) string {
	return promptClock.ReplaceAllString(prompt, "(current time: -)")
}

// The paired-branch paragraph reaching a real dispatch: the branches come
// from both the operator's PortBranches and the repository's own
// declaration (model.RepoConfig.PairedBranches), which the dispatch path
// has to read for itself.
func TestRunDispatchTellsARunInAPairedRepoNotToMergeAcross(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := dispatchTask(t, ctx, store, "t1")

	if bare := promptFor(t, ctx, store, orchestrator.Config{}, "t1", "r1"); strings.Contains(bare, "are paired") {
		t.Errorf("a repository pairing nothing was told about paired branches: %q", bare)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: *task.Target,
		PortBranches: []string{"main"}, DeclaredPortBranches: []string{"candidate"}}); err != nil {
		t.Fatalf("configuring the repo: %v", err)
	}
	if prompt := promptFor(t, ctx, store, orchestrator.Config{}, "t1", "r2"); !strings.Contains(prompt,
		"`main` and `candidate` are paired: do not merge one into the other") {
		t.Errorf("prompt does not forbid merging the paired branches: %q", prompt)
	}
}
