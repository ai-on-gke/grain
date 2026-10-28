package ui

// repoSummaries' own tests -- what `grain repo list` is a rendering of.
// They live in package ui rather than ui_test because the function is
// unexported: the three sources a repo can come from (the allowlist, a
// task's target, stored configuration of its own) are exactly what is
// worth testing
// directly, and going through HTTPClient.ListRepos to reach them would
// only add a server to the setup of every case.

import (
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRepoSummariesUnionsAllThreeSources(t *testing.T) {
	t.Parallel()
	got := repoSummaries(
		[]string{"acme/widgets", "acme/allowed-only"},
		[]Task{
			{Repo: "acme/widgets", State: model.StateQueued},
			{Repo: "acme/widgets", State: model.StateQueued, Blocked: true},
			{Repo: "acme/widgets", State: model.StateCompleted},
			// Off the allowlist and carrying no configuration of its
			// own: nothing can be filed against it any more, so it gets
			// no row and its task counts towards none. See the
			// unrestricted case below for the other half of this.
			{Repo: "acme/task-only", State: model.StateRunning},
			// A proposal nobody has pointed at a repo yet is omitted
			// rather than grouped under a blank name.
			{Repo: "", State: model.StateProposed},
		},
		map[string][]string{
			"acme/widgets": {"gcp-key"},
			// Configured before it was ever allow-listed or targeted --
			// SetRepoDefaultCapabilities permits exactly this, so a list
			// that dropped the row would hide a set somebody saved.
			"acme/configured-only": {"self-debug"},
		},
		// The same third source's other half: a repo whose only
		// configuration is standing instructions of its own
		// (grain/task-114), which must put up a row for the same reason
		// a stored default set does.
		[]string{"acme/widgets", "acme/prompt-only"},
		// And its third: a repo whose only configuration is the setup
		// command grain runs in every checkout it makes there
		// (grain/task-154), which is as invisible from everywhere else as
		// the two above.
		[]string{"acme/widgets", "acme/setup-only"},
		// And its fourth: a repo this deployment declares grain CI
		// workflows for (grain/task-245). The likeliest repo to be here
		// and nowhere else, since declaring workflows on grain's side is
		// what you do for a repository grain does not control.
		[]string{"acme/widgets", "acme/workflows-only"},
		[]string{"acme/ci-only"},
		[]string{"acme/release-only"},
	)

	want := []RepoSummary{
		{Repo: "acme/allowed-only", Configured: true},
		{Repo: "acme/ci-only", CICapabilities: true},
		{Repo: "acme/configured-only", DefaultCapabilities: []string{"self-debug"}},
		{Repo: "acme/prompt-only", PromptExtension: true},
		{Repo: "acme/release-only", ReleaseSettings: true},
		{Repo: "acme/setup-only", SetupCommand: true},
		{
			Repo:                "acme/widgets",
			Configured:          true,
			Tasks:               3,
			Blocked:             1,
			States:              map[model.State]int{model.StateQueued: 2, model.StateCompleted: 1},
			DefaultCapabilities: []string{"gcp-key"},
			PromptExtension:     true,
			SetupCommand:        true,
			Workflows:           true,
		},
		{Repo: "acme/workflows-only", Workflows: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repoSummaries() =\n%+v\nwant\n%+v", got, want)
	}
}

// Reads name a repo a task may read but never push to, which is not what
// a task "belongs to" -- the same rule state.js's repoRows follows, and
// the reason a read-only repo does not get a row of its own here.
func TestRepoSummariesIgnoresReadOnlyRepos(t *testing.T) {
	t.Parallel()
	got := repoSummaries(nil, []Task{
		{Repo: "acme/widgets", State: model.StateQueued, Reads: []string{"acme/docs"}},
	}, nil, nil, nil, nil, nil, nil)
	if len(got) != 1 || got[0].Repo != "acme/widgets" {
		t.Fatalf("repoSummaries() = %+v, want just the write target", got)
	}
}

// An unrestricted deployment's allowlist is empty by definition, so
// every row it has is unconfigured -- which is what stops `grain repo
// list` claiming a repo was left off a list that does not exist.
func TestRepoSummariesLeavesEveryRowUnconfiguredWhenUnrestricted(t *testing.T) {
	t.Parallel()
	got := repoSummaries(nil, []Task{{Repo: "acme/widgets", State: model.StateQueued}}, nil, nil, nil, nil, nil, nil)
	if len(got) != 1 || got[0].Configured {
		t.Fatalf("repoSummaries() = %+v, want one row that is not configured", got)
	}
}

// RepoStateOrder is what `grain repo list` walks a repo's counts in, and
// a state missing from it would be silently dropped from every
// breakdown (cmd/grain's repoStateCounts reports the remainder as
// "other" for exactly that reason, but the order is what should be
// complete).
func TestRepoStateOrderNamesEveryState(t *testing.T) {
	t.Parallel()
	for _, state := range []model.State{
		model.StateProposed, model.StateQueued, model.StateRunning,
		model.StateAwaitingReply, model.StateFailed, model.StateAwaitingSubmit,
		model.StateCompleted, model.StateDeferred,
		model.StateClosed,
	} {
		found := false
		for _, s := range RepoStateOrder {
			if s == state {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RepoStateOrder does not name %q", state)
		}
	}
}

// The other half of the rule the test above exercises: with no
// allowlist there is nothing saying where work may go, so a task is the
// only evidence a repo exists and raises a row of its own. This is the
// shape most deployments are in -- Config.TargetRepos' own "leave empty
// for a single-repo deployment" -- and a repos list that dropped these
// would be empty on all of them.
func TestRepoSummariesRaisesRowsFromTasksWhenNothingIsRestricted(t *testing.T) {
	t.Parallel()
	got := repoSummaries(nil, []Task{
		{Repo: "acme/widgets", State: model.StateQueued},
		{Repo: "acme/task-only", State: model.StateRunning},
	}, nil, nil, nil, nil, nil, nil)

	want := []RepoSummary{
		{Repo: "acme/task-only", Tasks: 1, States: map[model.State]int{model.StateRunning: 1}},
		{Repo: "acme/widgets", Tasks: 1, States: map[model.State]int{model.StateQueued: 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repoSummaries() =\n%+v\nwant\n%+v", got, want)
	}
}

// One repository however it is spelled (model.RepoRef.Same), which is
// what stopped `Bwsalmon/grain-home` standing beside
// `bwsalmon/grain-home` as a second repo with its own tasks. The row is
// named the way the allowlist names it, and the repo's stored
// configuration is found under the spelling that wrote it.
func TestRepoSummariesFoldsTwoSpellingsOfOneRepoIntoOneRow(t *testing.T) {
	t.Parallel()
	got := repoSummaries(
		[]string{"bwsalmon/grain-home"},
		[]Task{
			{Repo: "Bwsalmon/grain-home", State: model.StateQueued},
			{Repo: "bwsalmon/grain-home", State: model.StateCompleted},
		},
		map[string][]string{"BWSalmon/Grain-Home": {"gcp-key"}},
		nil, nil, nil, nil, nil,
	)

	want := []RepoSummary{{
		Repo:                "bwsalmon/grain-home",
		Configured:          true,
		Tasks:               2,
		States:              map[model.State]int{model.StateQueued: 1, model.StateCompleted: 1},
		DefaultCapabilities: []string{"gcp-key"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repoSummaries() =\n%+v\nwant\n%+v", got, want)
	}
}
