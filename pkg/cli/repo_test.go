package cli

// `grain repo`'s own rendering and argument parsing (grain/task-36).
// The wire calls behind it are pkg/ui's (httpclient_test.go's
// TestHTTPClientRepoFamilyRoundTrip); what is only decidable here is how
// a row reads and, in particular, that `-set ""` clears a repo's own
// defaults while omitting -set reads them -- a distinction that, got
// wrong, turns every `grain repo capabilities acme/widgets` into a
// silent wipe of what it was asked to print.

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/forge"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestParseRepoCapabilities(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		args     []string
		wantRepo string
		wantSet  *[]string
		wantErr  bool
	}{
		{
			name:     "no -set at all is a read",
			args:     []string{"acme/widgets"},
			wantRepo: "acme/widgets",
			wantSet:  nil,
		},
		{
			name:     "-set names the whole set",
			args:     []string{"-set", "gcp-key,self-debug", "acme/widgets"},
			wantRepo: "acme/widgets",
			wantSet:  &[]string{"gcp-key", "self-debug"},
		},
		{
			// The case this function exists for: given empty, -set is
			// still a write, and the only way to turn a repo's own set
			// back off.
			name:     "-set given empty clears rather than reads",
			args:     []string{"-set", "", "acme/widgets"},
			wantRepo: "acme/widgets",
			wantSet:  &[]string{},
		},
		{
			name:    "no repo at all is an error",
			args:    []string{"-set", "gcp-key"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, set, err := parseRepoCapabilities(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRepoCapabilities(%v) = %q, %v, nil; want an error", tc.args, repo, set)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRepoCapabilities(%v): %v", tc.args, err)
			}
			if repo != tc.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
			}
			if (set == nil) != (tc.wantSet == nil) {
				t.Fatalf("set = %v, want %v (nil-ness is what says read from write)", set, tc.wantSet)
			}
			if set != nil && !reflect.DeepEqual(*set, *tc.wantSet) {
				t.Errorf("set = %v, want %v", *set, *tc.wantSet)
			}
		})
	}
}

func TestRepoLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		summary ui.RepoSummary
		want    []string
		wantNot []string
	}{
		{
			name: "a repo with tasks reports them by state",
			summary: ui.RepoSummary{
				Repo: "acme/widgets", Configured: true, Tasks: 3, Blocked: 1,
				States: map[model.State]int{model.StateQueued: 2, model.StateCompleted: 1},
			},
			want: []string{"acme/widgets", "allowlisted", "3 tasks (2 queued, 1 completed)", "1 blocked"},
		},
		{
			// An unrestricted deployment's allowlist is empty, so every
			// row is unconfigured -- saying so on each would read as a
			// finding rather than as the absence of a list.
			name:    "a repo that is not allow-listed says nothing in its place",
			summary: ui.RepoSummary{Repo: "acme/widgets", Tasks: 1, States: map[model.State]int{model.StateQueued: 1}},
			wantNot: []string{"allowlist"},
		},
		{
			name:    "a repo with no tasks yet says so rather than printing an empty breakdown",
			summary: ui.RepoSummary{Repo: "acme/fresh", Configured: true},
			want:    []string{"acme/fresh", "no tasks"},
			wantNot: []string{"blocked"},
		},
		{
			name: "a repo's own defaults are on its row",
			summary: ui.RepoSummary{
				Repo: "acme/widgets", DefaultCapabilities: []string{"gcp-key", "self-debug"},
			},
			want: []string{"defaults: gcp-key, self-debug"},
			// Named only when there is one: a note on every row would
			// carry no information at all.
			wantNot: []string{"prompt extension"},
		},
		{
			// Named rather than quoted -- a repo's standing instructions
			// can be a paragraph, and a line-per-repo listing is no place
			// for one. The row is the pointer at `grain repo
			// prompt-extension`, which prints the text.
			name:    "a repo with standing instructions of its own says so without quoting them",
			summary: ui.RepoSummary{Repo: "acme/widgets", PromptExtension: true},
			want:    []string{"acme/widgets", "prompt extension"},
		},
		{
			// Named rather than printed for the same reason: a setup
			// command can be several lines of shell, and `grain repo
			// setup-command` is what prints it.
			name:    "a repo with a setup command says so without printing it",
			summary: ui.RepoSummary{Repo: "acme/widgets", SetupCommand: true},
			want:    []string{"acme/widgets", "setup command"},
		},
		{
			// And again for the workflows this deployment declares for a
			// repo it does not control: a whole YAML file per workflow,
			// and possibly several, so the row points at `grain repo
			// workflows` rather than trying to carry them.
			name:    "a repo grain declares CI workflows for says so without printing them",
			summary: ui.RepoSummary{Repo: "acme/theirs", Workflows: true},
			want:    []string{"acme/theirs", "CI workflows"},
		},
		{
			// A state RepoStateOrder does not name must not vanish from
			// a breakdown printed next to a total it would then
			// contradict.
			name: "a state the printer does not know is counted as other",
			summary: ui.RepoSummary{
				Repo: "acme/widgets", Tasks: 2,
				States: map[model.State]int{model.StateQueued: 1, model.State("invented"): 1},
			},
			want: []string{"2 tasks (1 queued, 1 other)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := repoLine(tc.summary)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("line %q does not contain %q", got, want)
				}
			}
			for _, unwanted := range tc.wantNot {
				if strings.Contains(got, unwanted) {
					t.Errorf("line %q unexpectedly contains %q", got, unwanted)
				}
			}
		})
	}
}

// parseRepoPromptExtension makes the same read/write distinction
// parseRepoCapabilities does, and getting it wrong costs more here: a
// repo's standing instructions are a paragraph somebody wrote, and a
// `grain repo prompt-extension acme/widgets` that stored the flag's own
// empty default would wipe them in the act of printing them.
func TestParseRepoPromptExtension(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		args     []string
		wantRepo string
		wantSet  *string
		wantErr  bool
	}{
		{
			name:     "no -set at all is a read",
			args:     []string{"acme/widgets"},
			wantRepo: "acme/widgets",
			wantSet:  nil,
		},
		{
			name:     "-set names the whole text",
			args:     []string{"-set", "Read db/README.md first.", "acme/widgets"},
			wantRepo: "acme/widgets",
			wantSet:  ptr("Read db/README.md first."),
		},
		{
			// The case this function exists for: given empty, -set is
			// still a write, and the only way to turn a repo's own
			// standing instructions back off.
			name:     "-set given empty clears rather than reads",
			args:     []string{"-set", "", "acme/widgets"},
			wantRepo: "acme/widgets",
			wantSet:  ptr(""),
		},
		{
			name:    "no repo at all is an error",
			args:    []string{"-set", "something"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, set, err := parseRepoPromptExtension(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRepoPromptExtension(%v) = %q, %v, nil; want an error", tc.args, repo, set)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRepoPromptExtension(%v): %v", tc.args, err)
			}
			if repo != tc.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
			}
			if (set == nil) != (tc.wantSet == nil) {
				t.Fatalf("set = %v, want %v (nil-ness is what says read from write)", set, tc.wantSet)
			}
			if set != nil && *set != *tc.wantSet {
				t.Errorf("set = %q, want %q", *set, *tc.wantSet)
			}
		})
	}
}

// parseRepoSetupCommand makes that same distinction, and the cost of
// getting it wrong is the same shape: `grain repo setup-command
// acme/widgets` that stored the flag's own empty default would clear the
// command grain runs in every checkout of that repo, in the act of
// printing it.
func TestParseRepoSetupCommand(t *testing.T) {
	t.Parallel()
	repo, set, err := parseRepoSetupCommand([]string{"acme/widgets"})
	if err != nil || repo != "acme/widgets" || set != nil {
		t.Fatalf("parseRepoSetupCommand(read) = (%q, %v, %v), want acme/widgets and no set", repo, set, err)
	}
	repo, set, err = parseRepoSetupCommand([]string{"-set", "make deps", "acme/widgets"})
	if err != nil || repo != "acme/widgets" || set == nil || *set != "make deps" {
		t.Fatalf("parseRepoSetupCommand(write) = (%q, %v, %v), want the command it was given", repo, set, err)
	}
	// Given empty, -set is still a write -- the only way to turn a repo's
	// setup command back off.
	if _, set, err = parseRepoSetupCommand([]string{"-set", "", "acme/widgets"}); err != nil ||
		set == nil || *set != "" {
		t.Fatalf("parseRepoSetupCommand(clear) = (%v, %v), want a non-nil empty set", set, err)
	}
	if _, _, err = parseRepoSetupCommand([]string{"-set", "make deps"}); err == nil {
		t.Error("parseRepoSetupCommand with no repo returned no error")
	}
}

// And again for the list of checks nothing pushed to a branch here
// clears. The cost of getting the read/write distinction wrong is the
// same shape once more, and worse than it looks: a `grain repo
// unfixable-checks acme/widgets` that stored the flag's own empty default
// would withdraw every declaration in the act of printing it, and the
// symptom would be grain quietly spending a repair run on a CLA bot again
// weeks later.
func TestParseRepoUnfixableChecks(t *testing.T) {
	t.Parallel()
	repo, set, err := parseRepoUnfixableChecks([]string{"acme/widgets"})
	if err != nil || repo != "acme/widgets" || set != nil {
		t.Fatalf("parseRepoUnfixableChecks(read) = (%q, %v, %v), want acme/widgets and no set", repo, set, err)
	}
	repo, set, err = parseRepoUnfixableChecks([]string{"-set", "cla/google,license/cla", "acme/widgets"})
	if err != nil || repo != "acme/widgets" || set == nil ||
		!reflect.DeepEqual(*set, []string{"cla/google", "license/cla"}) {
		t.Fatalf("parseRepoUnfixableChecks(write) = (%q, %v, %v), want both names it was given", repo, set, err)
	}
	// Given empty, -set is still a write -- the only way to withdraw a
	// repo's last declaration -- and it has to be a non-nil empty slice
	// so that it crosses REST as `[]` rather than as `null`.
	if _, set, err = parseRepoUnfixableChecks([]string{"-set", "", "acme/widgets"}); err != nil ||
		set == nil || len(*set) != 0 {
		t.Fatalf("parseRepoUnfixableChecks(clear) = (%v, %v), want a non-nil empty set", set, err)
	}
	if _, _, err = parseRepoUnfixableChecks([]string{"-set", "cla/google"}); err == nil {
		t.Error("parseRepoUnfixableChecks with no repo returned no error")
	}
}

// And for the setting whose third value is the point of it
// (model.WorkOwnership, grain/task-542). The read/write distinction its
// siblings above make is here too, and one more on top: `unset` is a
// *value* rather than the absence of one, so it has to be a write that
// sends the empty string -- a read that sent it would clear the
// repository's default in the act of printing it, and the symptom would
// be a deployment taking back work another deployment had been handed.
func TestParseRepoWorkOwnership(t *testing.T) {
	t.Parallel()
	repo, item, set, err := parseRepoWorkOwnership([]string{"acme/widgets"})
	if err != nil || repo != "acme/widgets" || item != "" || set != nil {
		t.Fatalf("parseRepoWorkOwnership(read) = (%q, %q, %v, %v), want acme/widgets and no set", repo, item, set, err)
	}
	if _, _, set, err = parseRepoWorkOwnership([]string{"-set", "off", "acme/widgets"}); err != nil ||
		set == nil || *set != "off" {
		t.Fatalf("parseRepoWorkOwnership(off) = (%v, %v), want an explicit off", set, err)
	}
	// The word for the third state, and the empty string it becomes over
	// the wire: the API has three values and no fourth one to spell this
	// with.
	if _, _, set, err = parseRepoWorkOwnership([]string{"-set", "unset", "acme/widgets"}); err != nil ||
		set == nil || *set != "" {
		t.Fatalf("parseRepoWorkOwnership(unset) = (%v, %v), want a non-nil empty set", set, err)
	}
	_, item, set, err = parseRepoWorkOwnership([]string{
		"-set", "on", "-item", " repo:schedule:acme:widgets:nightly ", "acme/widgets"})
	if err != nil || item != "repo:schedule:acme:widgets:nightly" || set == nil || *set != "on" {
		t.Fatalf("parseRepoWorkOwnership(item) = (%q, %v, %v), want the item it was given", item, set, err)
	}
	// A fourth answer is a typo rather than a state, and is refused here
	// rather than sent to be refused there.
	if _, _, _, err = parseRepoWorkOwnership([]string{"-set", "0", "acme/widgets"}); err == nil {
		t.Error("parseRepoWorkOwnership(-set 0) returned no error")
	}
	// -item with nothing to set reads as a request to act on one item,
	// and answering it with the whole repository's list would look like
	// the item was what had been read.
	if _, _, _, err = parseRepoWorkOwnership([]string{"-item", "release", "acme/widgets"}); err == nil {
		t.Error("parseRepoWorkOwnership(-item with no -set) returned no error")
	}
	if _, _, _, err = parseRepoWorkOwnership([]string{"-set", "off"}); err == nil {
		t.Error("parseRepoWorkOwnership with no repo returned no error")
	}
}

// What the list is printed with matters as much as the list: a reader who
// takes "checks no commit here can clear" for "checks that no longer hold
// the merge" has been told the opposite of the truth, and would reach for
// this for a flaky job. So the sentence is printed either way, including
// over an empty list -- whoever is reading an empty one is usually about
// to add to it.
func TestRepoUnfixableChecksPrintsWhatTheListDoesNotSay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		defaults ui.RepoDefaults
		want     []string
	}{
		{
			name: "a declared check, and what declaring it does not do",
			defaults: ui.RepoDefaults{
				Repo:             "acme/widgets",
				UnfixableChecks:  []string{"cla/google", "license/cla"},
				GrainCICheckName: "grain CI",
			},
			want: []string{
				"acme/widgets", "cla/google, license/cla",
				"does not exempt", "still not merged", "repair run",
				"without case", "grain CI",
			},
		},
		{
			name:     "a repo with none says so rather than printing a blank",
			defaults: ui.RepoDefaults{Repo: "acme/fresh", GrainCICheckName: "grain CI"},
			want: []string{
				"acme/fresh", "none", "could turn green",
				"does not exempt", "grain CI",
			},
		},
		{
			// A response with no name in it -- an older daemon, or one
			// that renamed the check -- still gets the sentence, with
			// the phrase in place of a name this end would have had to
			// guess at.
			name:     "a response that does not name grain's own CI falls back to describing it",
			defaults: ui.RepoDefaults{Repo: "acme/fresh"},
			want:     []string{"grain's own CI"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() { (&printer{}).repoUnfixableChecks(tc.defaults) })
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("printed %q, which does not contain %q", got, want)
				}
			}
		})
	}
}

func ptr(s string) *string { return &s }

// "none" rather than a blank: an empty line after a label reads as a
// rendering bug, and this is the common case for two of the three sets
// `grain repo capabilities` prints.
func TestCapabilityList(t *testing.T) {
	t.Parallel()
	if got := capabilityList(nil); got != "none" {
		t.Errorf("capabilityList(nil) = %q, want %q", got, "none")
	}
	if got := capabilityList([]string{"gcp-key", "self-debug"}); got != "gcp-key, self-debug" {
		t.Errorf("capabilityList = %q", got)
	}
}

// repoServer is a real ui.Server over a real embedded SQLite store, with
// settings already saved -- the first save has to name the core fields
// (ui.UpdateSettings), and `grain repo add` is a read-modify-write of
// those same settings, so a store with no row at all would fail for a
// reason that has nothing to do with repos.
func repoServer(t *testing.T) string {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
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
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("tester"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}, store)
	pollInterval, maxWorkers, host := "30s", 1, "github.com"
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		PollInterval: &pollInterval, MaxMergesPerRepo: &maxWorkers,
		GitHubHost: &host,
	}); err != nil {
		t.Fatalf("saving settings: %v", err)
	}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The dispatch itself, against a real server: `grain repo` is the only
// command whose arguments are parsed a second time, and every test above
// would still pass if runCLI's switch never reached cmdRepo at all.
func TestRunCLIDispatchesTheRepoFamily(t *testing.T) {
	t.Parallel()
	server := repoServer(t)
	for _, args := range [][]string{
		{"-server", server, "repo", "list"},
		{"-server", server, "repo", "add", "acme/widgets"},
		{"-server", server, "repo", "capabilities", "-set", "self-debug", "acme/widgets"},
		{"-server", server, "repo", "capabilities", "acme/widgets"},
		{"-server", server, "repo", "prompt-extension", "-set", "Read db/README.md first.", "acme/widgets"},
		{"-server", server, "repo", "prompt-extension", "acme/widgets"},
		{"-server", server, "repo", "setup-command", "-set", "make deps", "acme/widgets"},
		{"-server", server, "repo", "setup-command", "acme/widgets"},
		{"-server", server, "repo", "unfixable-checks", "-set", "cla/google", "acme/widgets"},
		{"-server", server, "repo", "unfixable-checks", "acme/widgets"},
		{"-server", server, "repo", "unfixable-checks", "-set", "", "acme/widgets"},
		{"-server", server, "repo", "work-ownership", "acme/widgets"},
		{"-server", server, "repo", "work-ownership", "-set", "off", "acme/widgets"},
		{"-server", server, "repo", "work-ownership", "-set", "on", "-item", "release", "acme/widgets"},
		{"-server", server, "repo", "work-ownership", "-set", "unset", "-item", "release", "acme/widgets"},
		{"-server", server, "repo", "work-ownership", "-set", "unset", "acme/widgets"},
		{"-server", server, "repo", "remove", "acme/widgets"},
		{"-json", "-server", server, "repo", "list"},
	} {
		if err := runCLIWithAccessCode(args); err != nil {
			t.Fatalf("grain %s: %v", strings.Join(args, " "), err)
		}
	}

	// A subcommand is required, and an unrecognized one is an error
	// rather than a silent listing.
	if err := runCLIWithAccessCode([]string{"-server", server, "repo"}); err == nil {
		t.Error("`grain repo` with no subcommand returned no error")
	}
	if err := runCLIWithAccessCode([]string{"-server", server, "repo", "nonesuch"}); err == nil {
		t.Error("`grain repo nonesuch` returned no error")
	}
	// Each of the ones that take a repo needs one, and none of them
	// invents a default from Config.DefaultTarget: acting on a repo
	// nobody named is exactly the mistake worth failing on.
	for _, args := range [][]string{
		{"-server", server, "repo", "capabilities"},
		{"-server", server, "repo", "prompt-extension"},
		{"-server", server, "repo", "setup-command"},
		{"-server", server, "repo", "unfixable-checks"},
		{"-server", server, "repo", "work-ownership"},
		{"-server", server, "repo", "add"},
		{"-server", server, "repo", "remove"},
	} {
		if err := runCLIWithAccessCode(args); err == nil {
			t.Errorf("grain %s returned no error, want one naming the missing repo", strings.Join(args, " "))
		}
	}

	// The server's refusal of grain CI's own verdict
	// (ui.SetRepoUnfixableChecks) reaches the shell as an error naming
	// it, rather than as a silent success: the CLI does not repeat the
	// validation, so this is what says the two ends are still connected.
	err := runCLIWithAccessCode([]string{"-server", server, "repo", "unfixable-checks",
		"-set", forge.GrainCICheckName, "acme/widgets"})
	if err == nil {
		t.Fatalf("`grain repo unfixable-checks -set %q` was accepted, want it refused", forge.GrainCICheckName)
	}
	if !strings.Contains(err.Error(), forge.GrainCICheckName) {
		t.Errorf("refusal = %v, want it to name %q", err, forge.GrainCICheckName)
	}
}
