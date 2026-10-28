package ui

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/forge"
	"github.com/bwsalmon/grain/pkg/model"
)

// AddRepoRequest is POST /api/repos' body.
type AddRepoRequest struct {
	Repo     string `json:"repo"`
	CloneURL string `json:"cloneUrl,omitempty"`
	Team     string `json:"team,omitempty"`
}

// AddTargetRepo appends repo to the deployment's TargetRepos allowlist --
// the repos pane's own "Add" (bwsalmon/agents#473), replacing the text
// field Settings used to bury this behind. A no-op, not an error, when
// repo is already present.
//
// This is a read-modify-write against whatever UpdateSettings already
// has stored, delegated to it rather than duplicated here, so a
// deployment that has never saved settings at all still gets
// UpdateSettings' own "poll interval etc. are required the first time"
// refusal instead of silently seeding a Config that says nothing about
// how the deployment is meant to run (a zero PollInterval is not an
// interval a reconcile loop can be paced by, and the daemon reading one
// back runs at its own -poll-interval flag instead of at anything stored
// -- see UpdateSettings' own doc comment, and cmd/grain's
// runnableStoredConfig for what a row like that costs).
//
// "Already present" is asked of the repo, not of the text
// (model.RepoRef.Same): adding `BWSalmon/Grain` to an allowlist that
// says `bwsalmon/grain` is the same no-op as adding it verbatim, rather
// than a second entry for one repository that would then have two
// spellings for parseRepo to choose between.
func (c *Client) AddTargetRepo(ctx context.Context, repo string) (Settings, error) {
	return c.AddTargetRepoWithCloneURL(ctx, repo, "")
}

// AddTargetRepoWithCloneURL adds a repository and, when cloneURL is set,
// records the non-GitHub remote that sandboxes clone with ambient credentials.
func (c *Client) AddTargetRepoWithCloneURL(ctx context.Context, repo, cloneURL string) (Settings, error) {
	return c.AddTargetRepoForTeam(ctx, model.DefaultTeamID, repo, cloneURL)
}

// AddTargetRepoForTeam appends repo to a specific team's TargetRepos allowlist.
func (c *Client) AddTargetRepoForTeam(ctx context.Context, teamID, repo, cloneURL string) (Settings, error) {
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return Settings{}, err
	}
	if teamID == "" || teamID == "all" {
		teamID = model.DefaultTeamID
	}
	parsed, err := model.ParseRepo(repo)
	if err != nil {
		return Settings{}, validationErrorf("repo: %v", err)
	}
	cloneURL = strings.TrimSpace(cloneURL)
	authoritative := false
	if cloneURL == "" {
		parsed, authoritative = c.githubSpelling(parsed)
	}
	repo = parsed.String()
	if strings.ContainsAny(cloneURL, "\r\n\x00") {
		return Settings{}, validationErrorf("clone URL contains a control character")
	}

	if c.Store != nil {
		if teamID != "" && teamID != model.DefaultTeamID {
			return Settings{}, validationErrorf("team %s not found", teamID)
		}
		if err := c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
			updated := make([]string, 0, len(cfg.TargetRepos)+1)
			found := false
			for _, r := range cfg.TargetRepos {
				if existing, err := model.ParseRepo(r); err == nil && existing.Same(parsed) {
					found = true
					if authoritative {
						updated = append(updated, repo)
						continue
					}
					updated = append(updated, r)
					continue
				}
				updated = append(updated, r)
			}
			if !found {
				updated = append(updated, repo)
			}
			cfg.TargetRepos = updated
			return nil
		}); err != nil {
			return Settings{}, err
		}
	}

	if cloneURL != "" {
		if _, err := c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.CloneURL = cloneURL }); err != nil {
			return Settings{}, err
		}
	}

	return c.GetSettings(ctx)
}

// RepoNames resolves a repository's own name -- what GitHub calls it, as
// against what somebody typed. github.(*RESTClient).RepoFullName is the
// implementation; nil on Config means this deployment cannot ask, which
// is the ordinary state of a UI with no GitHub client beside it
// (`grain demo`, and any UI not colocated with a daemon) and the same
// nil-means-unavailable contract Config.PullRequests already gives.
type RepoNames interface {
	RepoFullName(owner, name string) (string, error)
}

// githubSpelling is repo as GitHub itself spells it, and whether that is
// in fact where the answer came from -- false meaning repo is returned
// exactly as given, because this deployment cannot ask or GitHub would
// not answer.
//
// Falling back rather than refusing is the whole of the error handling
// here, and it is deliberate. The reasons this call fails are a network
// that is down, a credential that cannot see a private repo, and a
// deployment with no GitHub client at all -- none of which is a reason
// to refuse to add a repo to a list. The cost of falling back is that
// the entry keeps the operator's capitalisation, which is what every
// entry had before this existed.
//
// The bool is what keeps that fallback honest: its caller respells an
// existing entry only on an authoritative answer, so a deployment that
// cannot reach GitHub goes on treating a re-add as the no-op it always
// was rather than letting each operator overwrite the last one's
// capitalisation.
//
// A rename is adopted along with the capitalisation, because this
// endpoint follows GitHub's redirect and so answers under the name the
// repository has now. That is the same repository under the name it
// actually responds to, and storing the name it has moved away from
// would leave the allowlist naming something that only works by
// redirect.
func (c *Client) githubSpelling(repo model.RepoRef) (model.RepoRef, bool) {
	if c.Config.RepoNames == nil || model.IsConfigRepo(repo) {
		// The config repository's name is the one grain's git proxy
		// gives it; GitHub has no spelling of it to offer.
		return repo, false
	}
	full, err := c.Config.RepoNames.RepoFullName(repo.Owner, repo.Name)
	if err != nil {
		return repo, false
	}
	canonical, err := model.ParseRepo(full)
	if err != nil {
		return repo, false
	}
	return canonical, true
}

// RemoveTargetRepo removes repo from the deployment's TargetRepos
// allowlist -- the repos pane's own "Remove" (bwsalmon/agents#473). A
// no-op, not an error, when repo isn't present -- including every repo
// on an unrestricted deployment (empty TargetRepos), which are only ever
// known through the tasks that already target them rather than through
// this allowlist.
//
// Removing repo here narrows what a *new* task may target from now on,
// forgets any stored repo configuration, defaults, workflows, and
// declarations for repo, and takes repo off the repos pane once its
// tasks are settled.
//
// A task that already targets repo keeps its target, keeps its own page,
// and keeps every run it ever had. What it does not keep is its next
// dispatch: the pass that would have started it parks it instead, on
// grain's own comment saying the list narrowed under it
// (orchestrator.reconcileTargetRepos, model.OffAllowlistAtDispatchComment).
//
// This used to refuse outright while repo had a task still in play, and
// now reports instead -- Settings.TargetReposStranded, the same answer
// PUT /api/settings gives for the same act (Store.StrandedByTargetRepos).
// Two reasons, and the first is that the refusal's premise expired:
// it was written when nothing on the dispatch path read TargetRepos, so
// removal really did hide live work rather than stop it, and since
// grain/task-404 that work parks and says so in its own conversation.
// The second is that the refusal was only ever on this one gesture. The
// same narrowing through PUT /api/settings -- the Settings pane, and
// `grain sync` applying a config repo somebody has already approved --
// went through untouched, so "is this allowed" had two answers depending
// on which button an operator pressed. Making the save refuse was the
// other way to end that, and it would have let a queue of tasks nobody
// is looking at block a reviewed config change from landing.
//
// So both gestures allow it and both say what it costs. What an operator
// loses by removing a repo with work in it is now a sentence they are
// shown at the moment they do it, rather than an error telling them to
// close, complete or defer real work in order to make a configuration
// change.
//
// Which entry to drop is decided by repo, not by text, the way
// AddTargetRepo decides whether one is already there: `BWSalmon/Grain`
// removes the entry that says `bwsalmon/grain`, since that is the one
// repository the person clicking Remove is looking at. An unparseable
// repo, or an unparseable entry, can still only match itself verbatim --
// neither names a repository to compare.
func (c *Client) RemoveTargetRepo(ctx context.Context, repo string) (Settings, error) {
	return c.RemoveTargetRepoForTeam(ctx, model.DefaultTeamID, repo)
}

// RemoveTargetRepoForTeam removes repo from a specific team's TargetRepos allowlist.
func (c *Client) RemoveTargetRepoForTeam(ctx context.Context, teamID, repo string) (Settings, error) {
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return Settings{}, err
	}
	if teamID == "" || teamID == "all" {
		teamID = model.DefaultTeamID
	}
	if removing, err := model.ParseRepo(repo); err == nil && c.Config.StateRepoRef != nil {
		if configRepo := c.Config.StateRepoRef(); configRepo != (model.RepoRef{}) && configRepo.Same(removing) {
			return Settings{}, validationErrorf("the config repository %s cannot be removed", configRepo)
		}
	}
	if c.Store == nil {
		return c.GetSettings(ctx)
	}
	if teamID != model.DefaultTeamID {
		return Settings{}, validationErrorf("team %s not found", teamID)
	}
	current, err := c.Store.TargetRepos(ctx)
	if err != nil {
		return Settings{}, err
	}
	removing, removingErr := model.ParseRepo(repo)
	updated := make([]string, 0, len(current))
	for _, r := range current {
		match := r == repo
		if !match && removingErr == nil {
			if existing, err := model.ParseRepo(r); err == nil && existing.Same(removing) {
				match = true
			}
		}
		if !match {
			updated = append(updated, r)
		}
	}
	if len(updated) == len(current) {
		return c.GetSettings(ctx)
	}
	if removingErr == nil {
		if err := c.Store.ForgetRepo(ctx, removing); err != nil {
			return Settings{}, err
		}
	}
	if err := c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.TargetRepos = updated
		return nil
	}); err != nil {
		return Settings{}, err
	}
	return c.GetSettings(ctx)
}

// RepoDefaults is one repo's own task defaults -- what GET
// /api/repos/{owner}/{name}/capabilities and GET
// /api/repos/{owner}/{name}/prompt-extension both answer with, and what
// each of their PUTs returns -- the per-repo half of what the Settings
// pane chooses deployment-wide (grain/task-24, grain/task-114).
//
// One document, two routes onto it: a repo's defaults are one row
// (model.RepoConfig) and reporting one field of it without the rest
// would leave a caller unable to say what it is about to overwrite, so
// both routes report all of it and each PUT writes only the field its
// path names.
//
// It reports all three sets rather than only the one that is editable
// here, because the editable one is meaningless on its own: a repo row
// that lists gcp-key looks identical whether that is the only thing
// putting gcp-key on tasks here or a restatement of something every task
// in the deployment already gets, and the pane doing the editing has to
// be able to say which.
type RepoDefaults struct {
	Repo string `json:"repo"`
	// CICapabilities is the operator's explicit set for CI, independent of task defaults.
	CICapabilities []string `json:"ciCapabilities"`
	// AvailableCICapabilities names the providers that can deliver material to CI shells.
	AvailableCICapabilities []string `json:"availableCICapabilities"`
	// DefaultCapabilities is this repo's own set --
	// model.RepoConfig.DefaultCapabilities, exactly as stored, and the
	// only one of the three a PUT here writes.
	DefaultCapabilities []string `json:"defaultCapabilities"`
	// DeploymentDefaultCapabilities is model.Config.DefaultCapabilities,
	// the deployment-wide layer, reported read-only: it is chosen on
	// Settings' Capabilities tab, and a repo can add to it but never
	// subtract from it (model.RepoConfig.DefaultCapabilities has why).
	// The repo pane annotates each of these in its own picker, so
	// "unticking this here would not turn it off" is visible rather than
	// discovered by trying.
	DeploymentDefaultCapabilities []string `json:"deploymentDefaultCapabilities"`
	// EffectiveDefaultCapabilities is what a task filed against this repo
	// would actually start out holding: the union of the two above,
	// deployment-wide first, filtered to what this build still offers --
	// (*Client).defaultCapabilities' own answer, for this repo, rather
	// than a second computation of it that could drift.
	EffectiveDefaultCapabilities []string `json:"effectiveDefaultCapabilities"`
	// PromptExtension is this repo's own standing instructions for a run
	// working in it -- model.RepoConfig.PromptExtension, exactly as
	// stored, and the only one of the three fields below that a PUT to
	// this repo's prompt-extension route writes.
	PromptExtension string `json:"promptExtension"`
	// DeploymentPromptExtension is model.Config.PromptExtension, the
	// deployment-wide layer, reported read-only for the same reason
	// DeploymentDefaultCapabilities is: it is chosen in Settings, a repo
	// adds to it and can never replace it, and a pane editing this repo's
	// own text has to be able to show what it is being appended to.
	DeploymentPromptExtension string `json:"deploymentPromptExtension"`
	// EffectivePromptExtension is what a run against this repo would
	// actually be told, absent a task that overrides it: the two above
	// composed by model.PromptExtensionFor, rather than a second
	// composition here that could drift from the one dispatch uses.
	//
	// A task with a PromptExtension of its own replaces all of it (that
	// field's own doc comment), which is why this is "absent a task that
	// overrides it" rather than the last word.
	EffectivePromptExtension string `json:"effectivePromptExtension"`
	// SetupCommand is the shell grain runs in this repo's fresh checkout
	// before a run's first turn -- model.RepoConfig.SetupCommand, exactly
	// as stored, and the only thing a PUT to this repo's setup-command
	// route writes.
	//
	// It has no deployment-wide layer to report beside it, unlike the two
	// pairs above, and deliberately: "how this repo is set up" is a fact
	// about one repo's toolchain, and a deployment-wide command run in
	// every checkout would be a command most repos have no answer to.
	SetupCommand string `json:"setupCommand"`
	// DeclaredPromptExtension and DeclaredSetupCommand are what the
	// *repository* declared for itself in `.grain/settings.yml`
	// (model.RepoConfig.DeclaredPromptExtension, grain/task-340).
	//
	// Read-only here, and absent for the repositories that declare
	// nothing, which is very nearly all of them. They are reported
	// because the pane above them is where somebody edits the operator's
	// half of these two settings, and editing a box whose effect is being
	// appended to by a file elsewhere without being able to see the file
	// is how a setting comes to look as though it is not working. Nothing
	// a PUT to this repo's routes writes touches either of them: the
	// declaration sync owns these two columns, as those routes own the
	// two above.
	DeclaredPromptExtension string `json:"declaredPromptExtension,omitempty"`
	DeclaredSetupCommand    string `json:"declaredSetupCommand,omitempty"`
	// RepairTemplateID is this repo's configured default repair template.
	RepairTemplateID string `json:"repairTemplateID,omitempty"`
	// PortBranches, DeclaredPortBranches and PairedBranches are which of
	// this repository's branches are ported to one another: the
	// operator's list (model.RepoConfig.PortBranches, written only
	// through the state repository), the repository's own
	// `port-branches` in `.grain/settings.yml`, and the union of the two
	// that the merge queue actually ports between. Read-only, and absent
	// for a repository that pairs nothing.
	PortBranches         []string `json:"portBranches,omitempty"`
	DeclaredPortBranches []string `json:"declaredPortBranches,omitempty"`
	PairedBranches       []string `json:"pairedBranches,omitempty"`
	// PortDirections is, per direction either layer names or the pairing
	// implies, which merges are ported that way: both layers' lists and
	// what model.RepoConfig.PortTypeFilter composes them into. Read-only,
	// for the reason the three fields above are -- without it, which
	// filter is in force was a question only the database could answer.
	PortDirections []RepoPortDirection `json:"portDirections,omitempty"`
	// PortWarnings is every port-types entry, either layer's, that has
	// no effect (model.RepoConfig.PortTypesWarnings): a direction naming
	// a branch that is not paired, or a declared list the operator's
	// own pair overrides. The declared ones are also among
	// DeclarationsStatus.Warnings, where the pane already shows them.
	PortWarnings []string `json:"portWarnings,omitempty"`
	// Declarations is whether grain reads this repository's own `.grain`
	// directory for the templates, suites and schedules it declares
	// (model.RepoConfig.Declarations, grain/task-60) -- off unless
	// somebody here turned it on, and the only thing a PUT to this
	// repo's declarations route writes.
	Declarations bool `json:"declarations"`
	// DeclarationsStatus is what has come of reading them: the commit
	// grain last read, when it last looked, what it found and what went
	// wrong if anything did. Read-only, and absent for a repository
	// grain has never read.
	//
	// It is beside the switch because the switch alone cannot be
	// believed: turning it on is a request to poll a repository, and
	// everything that can go wrong afterwards -- an unreachable
	// repository, a file that does not parse, a repository that simply
	// declares nothing -- looks identical from a form that only shows a
	// tick.
	DeclarationsStatus *RepoDeclarationsStatus `json:"declarationsStatus,omitempty"`
	// RequireBranchUpToDate is whether the merge queue insists a branch
	// contain its base before it merges (model.RepoConfig.
	// RequireBranchUpToDate, grain/task-198) -- off unless somebody here
	// turned it on, and the only thing a PUT to this repo's
	// require-branch-up-to-date route writes.
	//
	// There is no deployment-wide layer beside it and no status under
	// it: unlike the switch above, which asks grain to go and read a
	// repository and can fail at it, this one only changes what the
	// queue does with a branch it is already looking at. What it costs
	// is the thing worth saying beside the tick, and that is a sentence
	// on the pane rather than a field here.
	RequireBranchUpToDate bool `json:"requireBranchUpToDate"`
	// IgnoreGitHubCI is whether the merge queue ignores external GitHub Actions
	// checks and relies solely on Grain internal CI for this repository.
	IgnoreGitHubCI bool `json:"ignoreGitHubCI"`
	// CIMode is the single-select CI mode configured for this repository: "grain", "github", or "none".
	CIMode string `json:"ciMode"`
	// WatchDefaultBranch is whether grain keeps a verdict about this
	// repository's default branch current without having merged into it
	// (model.RepoConfig.WatchDefaultBranch, grain/task-331) -- off
	// unless somebody here turned it on, and the only thing a PUT to
	// this repo's watch-default-branch route writes.
	//
	// Like RequireBranchUpToDate above it, what is worth saying beside
	// the tick is what it costs: turning it on starts one full suite on
	// that branch, and holds this repository's merge queue if that suite
	// is red. That is a sentence on the pane rather than a field here.
	WatchDefaultBranch bool `json:"watchDefaultBranch"`
	// MergeOnGreen is whether the merge queue merges a pull request here
	// as soon as it is green and merges cleanly, without waiting behind
	// entries whose CI is still running (model.RepoConfig.MergeOnGreen,
	// grain/task-545) -- off unless somebody here turned it on, and the
	// only thing a PUT to this repo's merge-on-green route writes.
	MergeOnGreen bool `json:"mergeOnGreen"`
	// BaseRepairBypassesHumanGates is whether a base-branch repair pull
	// request here may merge without a human on green CI while its target
	// base branch is red, even when it touches sensitive path triggers or
	// trust-boundary paths (model.RepoConfig.BaseRepairBypassesHumanGates,
	// grain/task-401).
	BaseRepairBypassesHumanGates bool `json:"baseRepairBypassesHumanGates"`
	// MergeRecheck is the merge queue's overlap re-check mode here, the
	// default filled in: "off", "observe" or "enforce"
	// (model.MergeRecheckModeOf, docs/merge-queue-behind.md).
	MergeRecheck string `json:"mergeRecheck"`
	// UnfixableChecks names this repository's checks that will never pass
	// for a pull request grain's agents author, whatever is pushed to the
	// branch -- `cla/google` and its kind (model.RepoConfig.
	// UnfixableChecks, grain/task-291), exactly as stored, and the only
	// thing a PUT to this repo's unfixable-checks route writes.
	//
	// Empty for almost every repository, and empty is not a state the
	// pane should read as "loading": a repository whose checks all work
	// says nothing here and is meant to.
	//
	// No deployment-wide layer beside it, unlike the two pairs above, and
	// deliberately: which check never passes is a fact about one
	// repository's bots, not about the deployment. The other half of the
	// same conclusion -- grain's own reading of a repository where no
	// check ever starts (docs/repository-checks-never-started.md) -- is
	// observed per cycle rather than configured, so there is nothing to
	// report beside this either; what the queue made of the two together
	// is on the task's row and in its escalation comment.
	UnfixableChecks []string `json:"unfixableChecks"`
	// GrainCICheckName is what grain's own CI is called in the sentences
	// the queue writes (forge.GrainCICheckName), reported read-only so a
	// pane refusing to put that name in the list above can name it in the
	// refusal rather than hard-coding a string that this build may have
	// renamed. It is never covered by a declaration, whatever is stored
	// (forge.Unfixable.Covers), and SetRepoUnfixableChecks refuses it.
	GrainCICheckName string `json:"grainCICheckName"`
}

// RepoDeclarationsStatus is what grain has made of one repository's
// declarations (model.RepoDeclarationSync, plus what it declares now).
type RepoDeclarationsStatus struct {
	Commit    string     `json:"commit,omitempty"`
	CheckedAt time.Time  `json:"checkedAt"`
	SyncedAt  *time.Time `json:"syncedAt,omitempty"`
	// Error is the last read that could not be used, empty when the last
	// one was fine. The declarations named below stay live while it is
	// set -- they are the last good commit's (model.Store.
	// RecordDeclarationFailure) -- which is exactly why the error has to
	// be shown rather than inferred from them being there.
	Error string `json:"error,omitempty"`
	// Templates, Suites, Schedules and Triggers are what this repository
	// declares right now, each as the file it is declared in.
	Templates []string `json:"templates,omitempty"`
	Suites    []string `json:"suites,omitempty"`
	Schedules []string `json:"schedules,omitempty"`
	Triggers  []string `json:"triggers,omitempty"`
	// Pushes is every declared template here with a `push:` list
	// (grain/task-2536): what the runs of tasks filed from it may push in
	// this repository beyond their own branch.
	Pushes []DeclaredPush `json:"pushes,omitempty"`
	// MergeRules is the file this repository's merge rules were read
	// from, empty where it declares none.
	MergeRules string `json:"mergeRules,omitempty"`
	// Warnings are what the last sync noticed about those files and did
	// not refuse them over (model.MergeRules.Warnings) -- a `ci.grain`
	// name no workflow job declares, or .grain/sensitive.yml ignored
	// beside .grain/merge-rules.yml. Without them here the only sign that
	// part of a file is ignored is a line in the daemon log.
	Warnings []string `json:"warnings,omitempty"`
}

// RepoPortDirection is one direction of RepoDefaults.PortDirections.
type RepoPortDirection struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Paired is whether both ends are paired branches; a direction that
	// is not is one nothing is ever ported along, whatever its lists say.
	Paired bool `json:"paired"`
	// OperatorTypes and DeclaredTypes are the operator's and the
	// repository's lists for this direction, as stored.
	OperatorTypes []string `json:"operatorTypes,omitempty"`
	DeclaredTypes []string `json:"declaredTypes,omitempty"`
	// Types is what the two compose into (model.RepoConfig.
	// PortTypeFilter): the conventional-commit types a merge has to carry
	// to be ported this way, empty when every merge is.
	Types []string `json:"types,omitempty"`
}

// SetRepoDeclarationsRequest is PUT /api/repos/{owner}/{name}/
// declarations' body: whether grain reads this repository's own
// declarations, a plain bool for the reason SetRepoSetupCommandRequest's
// string is plain.
type SetRepoDeclarationsRequest struct {
	Declarations bool `json:"declarations"`
}

// SetRepoRequireBranchUpToDateRequest is PUT /api/repos/{owner}/{name}/
// require-branch-up-to-date's body: whether the merge queue insists a
// branch contain its base before it merges, a plain bool for the reason
// SetRepoDeclarationsRequest's is plain.
type SetRepoRequireBranchUpToDateRequest struct {
	RequireBranchUpToDate bool `json:"requireBranchUpToDate"`
}

// SetRepoIgnoreGitHubCIRequest is PUT /api/repos/{owner}/{name}/
// ignore-github-ci's body.
type SetRepoIgnoreGitHubCIRequest struct {
	IgnoreGitHubCI bool `json:"ignoreGitHubCI"`
}

// SetRepoCIModeRequest is PUT /api/repos/{owner}/{name}/ci-mode's body.
type SetRepoCIModeRequest struct {
	CIMode string `json:"ciMode"`
}

// SetRepoSettingsRequest is PUT /api/repos/{owner}/{name}/settings' body.
// An older client may still send `admins`, the per-repository admin list
// schema 27 dropped; the decode is not strict, so it is ignored.
type SetRepoSettingsRequest struct {
	CIMode                       *string `json:"ciMode,omitempty"`
	IgnoreGitHubCI               *bool   `json:"ignoreGitHubCI,omitempty"`
	RequireBranchUpToDate        *bool   `json:"requireBranchUpToDate,omitempty"`
	WatchDefaultBranch           *bool   `json:"watchDefaultBranch,omitempty"`
	MergeOnGreen                 *bool   `json:"mergeOnGreen,omitempty"`
	BaseRepairBypassesHumanGates *bool   `json:"baseRepairBypassesHumanGates,omitempty"`
	// MergeRecheck is the overlap re-check's mode: "off", "observe" or
	// "enforce" (model.RepoConfig.MergeRecheck); "" puts it back to the
	// default, observe.
	MergeRecheck        *string   `json:"mergeRecheck,omitempty"`
	DefaultCapabilities *[]string `json:"defaultCapabilities,omitempty"`
	CICapabilities      *[]string `json:"ciCapabilities,omitempty"`
	PromptExtension     *string   `json:"promptExtension,omitempty"`
	RepairTemplateID    *string   `json:"repairTemplateID,omitempty"`
	SetupCommand        *string   `json:"setupCommand,omitempty"`
	UnfixableChecks     *[]string `json:"unfixableChecks,omitempty"`
}

// SetRepoWatchDefaultBranchRequest is PUT /api/repos/{owner}/{name}/
// watch-default-branch's body: whether grain keeps its own CI's verdict
// about this repository's default branch current whether or not it has
// merged into it, a plain bool for the reason
// SetRepoDeclarationsRequest's is plain.
type SetRepoWatchDefaultBranchRequest struct {
	WatchDefaultBranch bool `json:"watchDefaultBranch"`
}

// SetRepoMergeOnGreenRequest is PUT /api/repos/{owner}/{name}/
// merge-on-green's body: whether the merge queue merges a green, clean
// pull request without waiting behind entries whose CI is still running.
type SetRepoMergeOnGreenRequest struct {
	MergeOnGreen bool `json:"mergeOnGreen"`
}

// SetRepoBaseRepairBypassesHumanGatesRequest is PUT /api/repos/{owner}/{name}/
// base-repair-bypasses-human-gates' body: whether base-branch repair pull
// requests may merge without a human when their CI is green while the base
// branch is red.
type SetRepoBaseRepairBypassesHumanGatesRequest struct {
	BaseRepairBypassesHumanGates bool `json:"baseRepairBypassesHumanGates"`
}

// SetRepoUnfixableChecksRequest is PUT /api/repos/{owner}/{name}/
// unfixable-checks' body: the whole of this repository's list of checks
// nothing pushed to a branch clears, replaced rather than added to, a
// plain []string for the reason SetRepoCapabilitiesRequest's is plain --
// an omitted list and an empty one both say this repository has no such
// check, which is what almost every repository says.
type SetRepoUnfixableChecksRequest struct {
	UnfixableChecks []string `json:"unfixableChecks"`
}

// SetRepoCapabilitiesRequest is PUT /api/repos/{owner}/{name}/
// capabilities' body: the whole of this repo's own default set,
// replaced rather than added to, the same way UpdateSettingsRequest.
// DefaultCapabilities replaces the deployment-wide one.
//
// A plain []string rather than the *[]string that field uses: there is
// one field here and replacing it is the entire request, so an omitted
// list and an empty one mean the same thing -- this repo adds nothing --
// and there is no "leave this one alone" for a request whose only
// purpose is to say what the set now is.
type SetRepoCapabilitiesRequest struct {
	DefaultCapabilities []string `json:"defaultCapabilities"`
}

// SetRepoPromptExtensionRequest is PUT /api/repos/{owner}/{name}/
// prompt-extension's body: the whole of this repo's own standing
// instructions, replaced rather than added to.
//
// A plain string rather than a pointer, for the reason
// SetRepoCapabilitiesRequest's list is plain: there is one field here
// and replacing it is the entire request, so an omitted value and an
// empty one mean the same thing -- this repo adds nothing of its own --
// and there is no "leave this alone" for a request that exists only to
// say what the text now is.
type SetRepoPromptExtensionRequest struct {
	PromptExtension string `json:"promptExtension"`
}

// SetRepoSetupCommandRequest is PUT /api/repos/{owner}/{name}/
// setup-command's body: the whole of this repo's setup command, replaced
// rather than added to, a plain string for the reason
// SetRepoPromptExtensionRequest's is plain.
type SetRepoSetupCommandRequest struct {
	SetupCommand string `json:"setupCommand"`
}

// RepoDefaults reads repo's own defaults, alongside the deployment-wide
// set they compose with and the effective set that composition produces.
// A repo with nothing stored is not an error: it reports an empty own
// set, which is exactly what it contributes.
func (c *Client) RepoDefaults(ctx context.Context, repo string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.repoDefaults(ctx, parsed)
}

func (c *Client) repoDefaults(ctx context.Context, repo model.RepoRef) (RepoDefaults, error) {
	out := RepoDefaults{
		Repo:                    repo.String(),
		AvailableCICapabilities: c.ciCapabilityIDs(),
		GrainCICheckName:        forge.GrainCICheckName,
	}
	stored, err := c.Store.GetRepoConfig(ctx, repo)
	if err != nil {
		return RepoDefaults{}, err
	}
	out.MergeRecheck = string(model.MergeRecheckModeOf(stored))
	if stored != nil {
		out.DefaultCapabilities = stored.DefaultCapabilities
		out.CICapabilities = stored.CICapabilities
		out.PromptExtension = stored.PromptExtension
		out.RepairTemplateID = stored.RepairTemplateID
		out.SetupCommand = stored.SetupCommand
		out.DeclaredPromptExtension = stored.DeclaredPromptExtension
		out.DeclaredSetupCommand = stored.DeclaredSetupCommand
		out.Declarations = stored.Declarations
		out.RequireBranchUpToDate = stored.RequireBranchUpToDate
		out.IgnoreGitHubCI = stored.IgnoreGitHubCI
		out.CIMode = string(stored.EffectiveCIMode())
		out.WatchDefaultBranch = stored.WatchDefaultBranch
		out.MergeOnGreen = stored.MergeOnGreen
		out.BaseRepairBypassesHumanGates = stored.BaseRepairBypassesHumanGates
		out.UnfixableChecks = stored.UnfixableChecks
		out.PortBranches = stored.PortBranches
		out.DeclaredPortBranches = stored.DeclaredPortBranches
		out.PairedBranches = stored.PairedBranches()
		out.PortDirections = repoPortDirections(*stored)
		out.PortWarnings = stored.PortTypesWarnings()
	} else {
		out.CIMode = string(model.CIModeGitHub)
	}
	status, err := c.repoDeclarationsStatus(ctx, repo)
	if err != nil {
		return RepoDefaults{}, err
	}
	out.DeclarationsStatus = status
	if status != nil && stored != nil {
		// Only the repository's own entries: the operator's are no
		// finding about the files this status describes.
		for _, w := range stored.PortTypesWarnings() {
			if strings.HasPrefix(w, model.DeclaredPortTypesWarningPrefix) {
				status.Warnings = append(status.Warnings, w)
			}
		}
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return RepoDefaults{}, err
	}
	if cfg != nil {
		out.DeploymentDefaultCapabilities = cfg.DefaultCapabilities
		out.DeploymentPromptExtension = cfg.PromptExtension
	}
	// The same composition dispatch itself applies, with no task layer:
	// what a run here is told when nothing overrides it
	// (model.PromptExtensionFor).
	// The repo layer is the operator's text and the repository's own
	// declared text, in that order, exactly as dispatch composes them
	// (model.RepoConfig.RepoPromptExtension) -- a second composition here
	// would be one that could drift from the one a run is actually given.
	repoLayer := out.PromptExtension
	if stored != nil {
		repoLayer = stored.RepoPromptExtension()
	}
	out.EffectivePromptExtension = model.PromptExtensionFor(
		out.DeploymentPromptExtension, repoLayer, "")
	effective, err := c.defaultCapabilities(ctx, "", &repo)
	if err != nil {
		return RepoDefaults{}, err
	}
	out.EffectiveDefaultCapabilities = effective
	return out, nil
}

// SetRepoDefaultCapabilities replaces repo's own default capability set
// -- the repos pane's per-repo counterpart to the Settings pane's
// deployment-wide picker (bwsalmon/grain task-24).
//
// Every id must have a row in OfferedCapabilities, rejected here rather
// than skipped for the same reason UpdateSettings rejects one: this is
// somebody choosing a set, and a choice that could never take effect
// should be refused while whoever made it is still looking at it.
// Duplicates are dropped rather than refused -- a picker can produce one,
// and a set is what this is.
//
// An id the deployment already defaults is accepted and stored as given,
// not folded away: the two layers are chosen by different people at
// different times, and a repo that names one explicitly keeps it if the
// deployment-wide entry is later dropped (model.RepoConfig.
// DefaultCapabilities). What a task is filed with is the union either
// way, so storing it changes nothing today and preserves an intent that
// would otherwise be silently discarded.
//
// This does not check repo against Config.TargetRepos. A repo can be
// configured before it is allow-listed, and a repo removed from the
// allowlist keeps whatever it said here -- the same independence
// RemoveTargetRepo already leaves between "targeted" and "configured".
// Nothing is granted by this row alone: a task still has to target the
// repo, and targeting one off the allowlist parks the task before it
// ever dispatches (parkOffAllowlist).
func (c *Client) SetRepoDefaultCapabilities(ctx context.Context, repo string, ids []string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	clean, err := c.cleanRepoDefaultCapabilities(ids)
	if err != nil {
		return RepoDefaults{}, err
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.DefaultCapabilities = clean })
}

func (c *Client) cleanRepoDefaultCapabilities(ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id, err := c.normalizeCapabilityID(id)
		if err != nil {
			return nil, err
		}
		if _, ok := c.capabilityByID(id); !ok {
			// The same refusal UpdateSettings gives the deployment-wide
			// set, for the same reason: this one seeds every task filed
			// against this repo, so a CI-only id here would break the
			// whole repository's tasks rather than one of them. The CI
			// grant an operator reaching for it actually wants is the
			// next field along (SetRepoCICapabilities).
			return nil, validationErrorf("defaultCapabilities: %s", capabilityRefusal(id))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return clean, nil
}

// SetRepoPromptExtension replaces repo's own standing instructions --
// the repos pane's per-repo counterpart to the Settings pane's
// deployment-wide box, and the middle of model/prompt_extension.go's
// three layers (grain/task-114).
//
// Trimmed the same way ui.UpdateSettings trims the deployment-wide one,
// and empty is how a repo goes back to adding nothing of its own -- at
// which point it may stop having a row here at all, since a repo config
// that says nothing is deleted rather than stored (model.RepoConfig.Empty).
//
// It does not check repo against Config.TargetRepos, for the reason
// SetRepoDefaultCapabilities does not: a repo may be configured before
// it is allow-listed, nothing here is granted by the row alone, and a
// task targeting a repo off the allowlist parks before it can ever be
// dispatched with any of this.
func (c *Client) SetRepoPromptExtension(ctx context.Context, repo, text string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	trimmed := strings.TrimSpace(text)
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.PromptExtension = trimmed })
}

// SetRepoSetupCommand replaces the shell grain runs in this repo's fresh
// checkout before a run's first turn -- `make deps`, an `npm ci`, a
// virtualenv (model.RepoConfig.SetupCommand, grain/task-154).
//
// Trimmed like the two setters above, and empty is how a repo goes back
// to needing no setup at all -- at which point it may stop having a row
// here, since a repo config that says nothing is deleted rather than
// stored (model.RepoConfig.Empty).
//
// Nothing here validates the shell, and there is nothing useful it could
// validate: it is an arbitrary command for an arbitrary toolchain, and
// the only thing that can tell whether it works is running it in a
// checkout. What a bad one costs is bounded and visible -- every run
// against this repo is told its exit status and the tail of its output
// (orchestrator.setupSection), and one that never finishes is cut off at
// orchestrator's own bound and fails the dispatch rather than the run.
//
// It does not check repo against Config.TargetRepos, for the reason the
// two setters above do not.
func (c *Client) SetRepoSetupCommand(ctx context.Context, repo, command string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	trimmed := strings.TrimSpace(command)
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.SetupCommand = trimmed })
}

// SetRepoDeclarations turns reading this repository's own `.grain`
// declarations on or off (model.RepoConfig.Declarations, grain/task-60).
//
// Nothing happens here beyond the switch. Turning it on does not read
// the repository -- the next cycle does (orchestrator.
// reconcileDeclarations), within a couple of minutes, and what it found
// shows up in RepoDefaults.DeclarationsStatus -- and turning it off does
// not delete what the repository declared here either: that same
// reconciler does it, on the same tick it notices, and before anything
// can fire one of them again.
//
// Doing both from the reconciler rather than from here is what makes the
// switch mean the same thing however it was flipped. It is a
// `repo_config` row like any other, so it can equally arrive as a merged
// change to the state repository, where there is no request in flight to
// hang the work off at all.
func (c *Client) SetRepoDeclarations(ctx context.Context, repo string, on bool) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.Declarations = on })
}

// SetRepoRequireBranchUpToDate turns the merge queue's strict rule for
// this repository on or off -- GitHub's own "Require branches to be up
// to date before merging", done by grain (model.RepoConfig.
// RequireBranchUpToDate, grain/task-198).
//
// Nothing happens here beyond the switch, the way nothing happens beyond
// the switch above: no branch is brought up to date by this request and
// no suite is started by it. The next merge-queue cycle reads the row
// and acts on it (orchestrator.requireBranchUpToDate,
// bringHeadUpToDate), which is also what makes the switch mean the same
// thing when it arrives as a merged change to the state repository
// instead of as a request here.
//
// Turning it on is the expensive answer and deliberately not the
// default: every open branch is brought up to date as the base moves,
// and each write to a head throws that head's verdict away, so N open
// branches and N merges cost on the order of N² full suites
// (docs/merge-queue-behind.md). It does not check repo against
// Config.TargetRepos, for the reason its neighbours do not.
func (c *Client) SetRepoRequireBranchUpToDate(ctx context.Context, repo string, on bool) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.RequireBranchUpToDate = on })
}

// SetRepoWatchDefaultBranch turns watching this repository's default
// branch on or off (model.RepoConfig.WatchDefaultBranch,
// grain/task-331): whether grain keeps its own CI's verdict about the
// branch the queue merges onto current whether or not grain has ever
// merged into it.
//
// Nothing runs here, the way nothing runs in its neighbours: the next
// base-branch cycle resolves the default branch and starts the first
// suite (orchestrator.seedWatchedBaseBranches), which is also what makes
// the switch mean the same thing when it arrives as a merged change to
// the state repository instead of as a request here.
//
// Turning it on has a price and it is worth knowing before ticking it:
// one full suite on that branch, and -- if the branch turns out to be
// red -- this repository's merge queue held and one repair task filed.
// That is the control working as designed on a branch nothing had ever
// judged, rather than a fault, but it arrives as CI spend and as a task
// in somebody's queue.
//
// Turning it off again stops grain *seeding*, and does not stop it
// watching a branch it has already seeded: once a row exists the row
// itself is the opt-in, exactly as it is for a branch a merge created
// one for (orchestrator.reconcileOneBaseBranchCI's own note on that).
// What stops a repository's base branches being judged at all is moving
// its CI mode off "Use Grain CI", and only that.
// RepoConfig.ReleaseWorkflowsDisabled does not: publishing and judging
// are two switches (grain/task-330), and since grain/task-461 they have
// nothing at all to do with each other -- every base branch is judged
// under `push` with everything a push would run unless
// RepoConfig.BaseBranchCIJobs narrows it, and what that switch decides is
// only whether a merge also starts the release run beside it.
//
// A branch that has stopped being judged is reported rather than merely
// logged, on the Release pane and on the deployment health reading
// (basebranchgate.go, grain/task-453). That is worth keeping now that
// the switch-shaped way into the state has been closed: what is left is
// a branch nobody has taken the capability off and nobody has switched
// anything on, which simply stops having runs started for it -- and this
// deployment went 33 hours without noticing the version of that it had.
func (c *Client) SetRepoWatchDefaultBranch(ctx context.Context, repo string, on bool) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.WatchDefaultBranch = on })
}

// SetRepoMergeOnGreen turns merging green pull requests ahead of
// pending ones on or off for this repository
// (model.RepoConfig.MergeOnGreen). Like its neighbours nothing runs
// here: the next cycle's merge walk reads the row.
func (c *Client) SetRepoMergeOnGreen(ctx context.Context, repo string, on bool) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.MergeOnGreen = on })
}

// SetRepoBaseRepairBypassesHumanGates turns allowing base-branch repair pull
// requests to bypass human gates on or off for this repository
// (model.RepoConfig.BaseRepairBypassesHumanGates).
func (c *Client) SetRepoBaseRepairBypassesHumanGates(ctx context.Context, repo string, on bool) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.BaseRepairBypassesHumanGates = on })
}

// SetRepoIgnoreGitHubCI turns the ignore-github-ci setting for this repository on or off.
func (c *Client) SetRepoIgnoreGitHubCI(ctx context.Context, repo string, on bool) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) {
		rc.IgnoreGitHubCI = on
		if on {
			rc.CIMode = model.CIModeGrain
		} else {
			rc.CIMode = model.CIModeGitHub
		}
	})
}

// SetRepoCIMode updates the CI mode for this repository ("grain", "github", "none").
func (c *Client) SetRepoCIMode(ctx context.Context, repo string, mode string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	if err := c.checkRepoPerson(ctx, parsed); err != nil {
		return RepoDefaults{}, err
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch model.CIMode(mode) {
	case model.CIModeGrain, model.CIModeGitHub, model.CIModeNone:
	default:
		return RepoDefaults{}, validationErrorf("ciMode: must be %q, %q, or %q", model.CIModeGrain, model.CIModeGitHub, model.CIModeNone)
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) {
		rc.CIMode = model.CIMode(mode)
		rc.IgnoreGitHubCI = (rc.CIMode == model.CIModeGrain)
	})
}

// SetRepoSettings updates multiple repository settings in one request.
func (c *Client) SetRepoSettings(ctx context.Context, repo string, req SetRepoSettingsRequest) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	var recheck model.MergeRecheckMode
	if req.MergeRecheck != nil {
		if recheck, err = model.ParseMergeRecheckMode(*req.MergeRecheck); err != nil {
			return RepoDefaults{}, validationErrorf("mergeRecheck: %v", err)
		}
	}
	var defaultCaps []string
	if req.DefaultCapabilities != nil {
		if defaultCaps, err = c.cleanRepoDefaultCapabilities(*req.DefaultCapabilities); err != nil {
			return RepoDefaults{}, err
		}
	}
	var ciCaps []string
	if req.CICapabilities != nil {
		if ciCaps, err = c.cleanRepoCICapabilities(*req.CICapabilities); err != nil {
			return RepoDefaults{}, err
		}
	}
	var unfixable []string
	if req.UnfixableChecks != nil {
		if unfixable, err = cleanRepoUnfixableChecks(*req.UnfixableChecks); err != nil {
			return RepoDefaults{}, err
		}
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) {
		if req.MergeRecheck != nil {
			rc.MergeRecheck = recheck
		}
		if req.DefaultCapabilities != nil {
			rc.DefaultCapabilities = defaultCaps
		}
		if req.CICapabilities != nil {
			rc.CICapabilities = ciCaps
		}
		if req.UnfixableChecks != nil {
			rc.UnfixableChecks = unfixable
		}
		if req.CIMode != nil {
			mode := strings.ToLower(strings.TrimSpace(*req.CIMode))
			if mode == string(model.CIModeGrain) || mode == string(model.CIModeGitHub) || mode == string(model.CIModeNone) {
				rc.CIMode = model.CIMode(mode)
				rc.IgnoreGitHubCI = (rc.CIMode == model.CIModeGrain)
			}
		}
		if req.IgnoreGitHubCI != nil {
			rc.IgnoreGitHubCI = *req.IgnoreGitHubCI
			if rc.IgnoreGitHubCI {
				rc.CIMode = model.CIModeGrain
			} else if rc.CIMode == model.CIModeGrain {
				rc.CIMode = model.CIModeGitHub
			}
		}
		if req.RequireBranchUpToDate != nil {
			rc.RequireBranchUpToDate = *req.RequireBranchUpToDate
		}
		if req.WatchDefaultBranch != nil {
			rc.WatchDefaultBranch = *req.WatchDefaultBranch
		}
		if req.MergeOnGreen != nil {
			rc.MergeOnGreen = *req.MergeOnGreen
		}
		if req.BaseRepairBypassesHumanGates != nil {
			rc.BaseRepairBypassesHumanGates = *req.BaseRepairBypassesHumanGates
		}
		if req.PromptExtension != nil {
			rc.PromptExtension = strings.TrimSpace(*req.PromptExtension)
		}
		if req.RepairTemplateID != nil {
			rc.RepairTemplateID = strings.TrimSpace(*req.RepairTemplateID)
		}
		if req.SetupCommand != nil {
			rc.SetupCommand = strings.TrimSpace(*req.SetupCommand)
		}
	})
}

// SetRepoUnfixableChecks replaces the list of checks that will never pass
// for a pull request grain's agents author in repo -- a CLA bot and its
// kind (model.RepoConfig.UnfixableChecks, grain/task-291). The whole list
// is replaced, the way SetRepoDefaultCapabilities replaces the whole set,
// and empty is how a repository goes back to having no such check -- at
// which point it may stop having a row here at all, since a repo config
// that says nothing is deleted rather than stored (model.RepoConfig.Empty).
//
// Names are trimmed, and a name that is nothing but space is dropped
// rather than refused: a form with an empty box in it is somebody who has
// not typed a check name yet, not somebody declaring one. Duplicates go
// the same way, compared the way the matching itself is done -- without
// case (forge.Unfixable.Covers) -- so `CLA/Google` beside `cla/google` is
// one declaration and not two spellings of it that would both have to be
// removed. The spelling kept is the first, because it is what the
// operator wrote and the list is read by people as well as by Covers.
//
// Two things are refused rather than cleaned up, because accepting either
// would store a declaration that does nothing:
//
//   - grain CI's own verdict (forge.GrainCICheckName). Covers declines it
//     whatever is stored -- a grain CI run judges one exact commit on the
//     branch, so it is always the branch's own answer -- and a deployment
//     able to declare its own runner unfixable would have built a way to
//     merge with no verdict at all. Stored, it would sit on the pane
//     looking like a setting.
//   - a name with a comma in it. The column is a comma-joined list
//     (model.joinCSV), so such a name reads back as two names, neither of
//     which any check is called.
//
// Nothing here checks the name against the checks the repository actually
// reports, and nothing could honestly: the point of the declaration is a
// check that is red now, and a repository whose provider is dead reports
// no names at all to check against. A name nothing reports simply never
// matches -- it costs a line on this pane and no behaviour.
//
// It does not check repo against Config.TargetRepos, for the reason its
// neighbours do not.
func (c *Client) SetRepoUnfixableChecks(ctx context.Context, repo string, checks []string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	clean, err := cleanRepoUnfixableChecks(checks)
	if err != nil {
		return RepoDefaults{}, err
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.UnfixableChecks = clean })
}

func cleanRepoUnfixableChecks(checks []string) ([]string, error) {
	seen := make(map[string]bool, len(checks))
	clean := make([]string, 0, len(checks))
	for _, check := range checks {
		name := strings.TrimSpace(check)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, forge.GrainCICheckName) {
			return nil, validationErrorf(
				"unfixableChecks: %s cannot be declared unfixable -- it judges this exact commit on the branch, so it is always the branch's own answer",
				forge.GrainCICheckName)
		}
		if strings.Contains(name, ",") {
			return nil, validationErrorf(
				"unfixableChecks: %q has a comma in it, and these are stored as a comma-separated list -- name one check per entry", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, name)
	}
	return clean, nil
}

// repoDeclarationsStatus reports what grain has made of repo's
// declarations, or nil for a repository it has never read.
func (c *Client) repoDeclarationsStatus(ctx context.Context, repo model.RepoRef) (*RepoDeclarationsStatus, error) {
	sync, err := c.Store.GetRepoDeclarationSync(ctx, repo)
	if err != nil {
		return nil, err
	}
	if sync == nil {
		return nil, nil
	}
	status := &RepoDeclarationsStatus{
		Commit: sync.Commit, CheckedAt: sync.CheckedAt, Error: sync.LastError,
	}
	if !sync.SyncedAt.IsZero() {
		at := sync.SyncedAt
		status.SyncedAt = &at
	}
	declared, err := c.Store.RepoDeclarationsOf(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, row := range declared {
		switch row.Kind {
		case model.DeclaredTemplate:
			status.Templates = append(status.Templates, row.Path)
			if push := declaredPushOf(&row); push != nil {
				status.Pushes = append(status.Pushes, *push)
			}
		case model.DeclaredSuite:
			status.Suites = append(status.Suites, row.Path)
		case model.DeclaredSchedule:
			status.Schedules = append(status.Schedules, row.Path)
		}
	}
	// Path triggers are kept in a table of their own (model's
	// path_trigger_store.go), and a repository whose only declaration is
	// a trigger must not read as one that declares nothing.
	triggers, err := c.Store.PathTriggersOf(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, tr := range triggers {
		status.Triggers = append(status.Triggers, tr.Trigger.Path)
	}
	rules, err := c.Store.MergeRulesOf(ctx, repo)
	if err != nil {
		return nil, err
	}
	if rules != nil {
		status.MergeRules = rules.Path
		status.Warnings = rules.Warnings
	}
	return status, nil
}

// repoPortDirections is RepoDefaults.PortDirections for rc.
func repoPortDirections(rc model.RepoConfig) []RepoPortDirection {
	paired := rc.PairedBranches()
	var out []RepoPortDirection
	for _, d := range rc.PortTypeDirections() {
		from, to := d[0], d[1]
		dir := model.PortDirection(from, to)
		out = append(out, RepoPortDirection{
			From: from, To: to,
			Paired:        slices.Contains(paired, from) && slices.Contains(paired, to),
			OperatorTypes: rc.PortTypes[dir],
			DeclaredTypes: rc.DeclaredPortTypes[dir],
			Types:         rc.PortTypeFilter(from, to),
		})
	}
	return out
}

// checkRepoPerson refuses a change to repo's settings unless the actor
// is a person, or an agent whose call agentAPIGate already authorized for
// its scope on repo. Every person is the deployment's owner -- whoever holds
// the access code is (docs/access-code.md) -- so what this keeps out is
// an un-gated agent: a run's MCP token, a sandbox or an executor with no
// api:<scope> grant for the route. A deployment with
// no settings row yet refuses nobody. A refusal is a ForbiddenError, so
// it reaches the API as a 403 rather than as a malformed request.
func (c *Client) checkRepoPerson(ctx context.Context, repo model.RepoRef) error {
	actor := c.actor(ctx)
	if actor.ID == "" || c.Store == nil {
		return forbiddenErrorf("user %q does not have permission to modify repository %s", actor.ID, repo)
	}
	if actor.Kind == model.PrincipalHuman {
		return nil
	}
	if call, ok := agentCallFromContext(ctx); ok && call.taskID == actor.ID &&
		(call.repo == "" || strings.EqualFold(call.repo, repo.String())) {
		return nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	return forbiddenErrorf("%q is not a person, so it may not modify repository %s", actor.ID, repo)
}

// requireRepoPerson is checkRepoPerson for an act rather than a settings
// write: the same rule, but a refusal is a 403 rather than the 400 a
// settings form has always shown inline.
func (c *Client) requireRepoPerson(ctx context.Context, repo model.RepoRef) error {
	err := c.checkRepoPerson(ctx, repo)
	var ve *ValidationError
	if errors.As(err, &ve) {
		return &ForbiddenError{err: ve.err}
	}
	return err
}

// putRepoConfig applies one edit to repo's stored configuration and
// writes the whole row back, read-modify-write.
//
// Read first, rather than each setter writing a model.RepoConfig built
// from its own field alone: Store.PutRepoConfig replaces the row
// wholesale (one row per repo, replaced rather than diffed), so a setter
// that named only its own field would silently clear every other one --
// saving a repo's capabilities would wipe the prompt extension somebody
// wrote on the pane beside it. A repo with no row yet starts from an
// empty config, which is exactly what it means.
func (c *Client) putRepoConfig(ctx context.Context, repo model.RepoRef, edit func(*model.RepoConfig)) (RepoDefaults, error) {
	if err := c.checkSettingsWritable(ctx, "repo_config"); err != nil {
		return RepoDefaults{}, err
	}
	if err := c.checkRepoPerson(ctx, repo); err != nil {
		return RepoDefaults{}, err
	}
	stored, err := c.Store.GetRepoConfig(ctx, repo)
	if err != nil {
		return RepoDefaults{}, err
	}
	cfg := model.RepoConfig{Repo: repo}
	if stored != nil {
		cfg = *stored
	}
	edit(&cfg)
	if err := c.Store.PutRepoConfig(ctx, cfg); err != nil {
		return RepoDefaults{}, err
	}
	return c.repoDefaults(ctx, repo)
}

// repoKey folds a repo name to the one thing two spellings of one
// repository have in common, so that a map over repo names groups them
// together. It is model.RepoRef.Same expressed as a key rather than as a
// comparison -- which that method's own doc comment says is what
// everything storing a repo lacks, and what let `Bwsalmon/grain-home`
// and `bwsalmon/grain-home` be two repos on one repos page.
//
// Text in, text out: the callers below hold repo names as written
// (targetRepos entries, Task.Repo, a RepoConfig's key) rather than
// parsed refs, and a name that does not parse still has to land
// somewhere rather than collapse into "".
func repoKey(repo string) string { return strings.ToLower(repo) }

// RepoSummary is one repo as `grain repo list` reports it (grain/
// task-36): whether the deployment's allowlist names it, how its tasks
// are spread across the states, and what it defaults on its own -- the
// repos page's own row, for a shell with no page to open.
//
// Composed on the client side, out of GET /api/config and GET /api/tasks,
// rather than served by a GET /api/repos of its own. A repo is not a
// stored row here: docs/data-model.md's folder tree is still unbuilt, so
// "a repo grain knows about" is *derived* -- whatever TargetRepos names,
// union whatever a task targets, union whatever carries defaults of its
// own -- and the frontend already derives exactly that from these same
// two responses (state.js's repoRows). A third derivation, on the
// server, would be a second definition of the same thing to keep in
// step with the first.
type RepoSummary struct {
	// Repo is "owner/name".
	Repo string `json:"repo"`
	// Configured is whether Config.TargetRepos names this repo, as
	// against it being known only because something else here mentions
	// it -- repoRows' own `configured`, and the same distinction that
	// decides whether a repo's own page offers Remove. Always
	// false on an unrestricted deployment, whose allowlist is empty by
	// definition.
	Configured bool `json:"configured"`
	// Tasks is how many tasks target this repo (Task.Repo, never Reads
	// -- a read-only repo grants nothing and is not what a task "belongs
	// to"), and States is that same count broken down by state.
	Tasks  int                 `json:"tasks"`
	States map[model.State]int `json:"states,omitempty"`
	// Blocked is how many of those tasks are waiting on a link that is
	// still open (model.IsBlocked) -- the "something is stuck here"
	// signal the repos page exists to surface at a glance.
	Blocked int `json:"blocked"`
	// DefaultCapabilities is this repo's *own* default set
	// (model.RepoConfig.DefaultCapabilities), not the union a task filed
	// here would actually hold: `grain repo capabilities` reports all
	// three sets, and a list of every repo is the wrong place to repeat
	// the deployment-wide layer once per row.
	DefaultCapabilities []string `json:"defaultCapabilities,omitempty"`
	// PromptExtension is whether this repo has standing instructions of
	// its own (model.RepoConfig.PromptExtension) -- whether, not what,
	// since a list of every repo is no place for a paragraph per row and
	// GET /api/config carries only the names for the same reason
	// (configResponse.ReposWithPromptExtension). `grain repo
	// prompt-extension <owner/name>` is what prints the text.
	PromptExtension bool `json:"promptExtension,omitempty"`
	// SetupCommand is whether this repo has a setup command of its own
	// (model.RepoConfig.SetupCommand) -- whether, not what, for the
	// reason PromptExtension above is a bool. `grain repo setup-command
	// <owner/name>` is what prints it.
	SetupCommand bool `json:"setupCommand,omitempty"`
	// Workflows is whether this deployment declares grain CI workflows
	// for this repo (model.RepoWorkflow) -- whether, not which, for the
	// reason the two above are bools. A whole workflow file per row would
	// be a listing nobody could read.
	Workflows       bool `json:"workflows,omitempty"`
	CICapabilities  bool `json:"ciCapabilities,omitempty"`
	ReleaseSettings bool `json:"releaseSettings,omitempty"`
}

// RepoStateOrder is the order a repo's per-state counts are meant to be
// read in -- ui/src/state.js's own STATE_ORDER, mirrored here so `grain
// repo list` walks a repo's tasks the way the repos page does rather
// than landing on Go's map iteration order.
var RepoStateOrder = []model.State{
	model.StateProposed,
	model.StateQueued,
	model.StateRunning,
	model.StateAwaitingReply,
	model.StateFailed,
	model.StateAwaitingSubmit,
	model.StateCompleted,
	model.StateDeferred,
	model.StateClosed,
}

// repoSummaries composes one RepoSummary per known repo, sorted by name,
// from the three things that can make grain know a repo at all: the
// allowlist, the tasks that target it, and the defaults it carries.
//
// Tasks raise a row of their own only where there is no allowlist. A
// row here is a claim that this deployment can work in that repo now,
// and a repo that has fallen off Config.TargetRepos -- removed, or
// renamed away underneath the tasks that named it -- is somewhere
// nothing can be filed against any more (CreateTask parks it,
// parkOffAllowlist). Its tasks stay exactly where they are and stay
// readable; what they stop doing is holding up a row that RemoveTargetRepo
// then cannot remove, since Remove is only offered for a Configured one.
// state.js's repoRows applies the same rule for the page, and has to:
// the two derive one list and a shell that disagrees with the page is
// worse than either answer alone.
//
// Two spellings of one repository are one row, for model.RepoRef.Same's
// reason -- GitHub resolves owner/name case-insensitively. The row is
// named by whichever source is read first, which puts an allowlist
// entry's spelling ahead of a task's: that list is the deployment's own
// canon, and AddTargetRepo resolves an entry against GitHub's own
// spelling before storing it.
//
// That third source is where this deviates from state.js's repoRows,
// deliberately. None of SetRepoDefaultCapabilities,
// SetRepoPromptExtension or SetRepoSetupCommand requires a repo to be
// allow-listed (their own doc comments: a repo can be configured before
// it is allowed, and stays configured after it is removed), so a repo
// can hold any of those kinds of configuration while matching neither of
// the other two sources.
// Dropping it would mean `grain repo capabilities`, `grain repo
// prompt-extension` or `grain repo setup-command` could write something
// that `grain repo list` then never admits exists -- and a list whose
// whole job includes reporting per-repo configuration must not be the
// one place it is invisible.
func repoSummaries(targetRepos []string, tasks []Task, repoDefaults map[string][]string,
	reposWithPromptExtension, reposWithSetupCommand, reposWithWorkflows, reposWithCICapabilities, reposWithReleaseSettings []string) []RepoSummary {

	// Every map here is keyed by repoKey rather than by the string as
	// written, for model.RepoRef.Same's reason: GitHub resolves
	// owner/name case-insensitively, so two spellings that reach this
	// from two sources are one repository and belong on one row.
	caps := make(map[string][]string, len(repoDefaults))
	for repo, ids := range repoDefaults {
		caps[repoKey(repo)] = ids
	}
	withPrompt := make(map[string]bool, len(reposWithPromptExtension))
	for _, repo := range reposWithPromptExtension {
		withPrompt[repoKey(repo)] = true
	}
	withSetup := make(map[string]bool, len(reposWithSetupCommand))
	for _, repo := range reposWithSetupCommand {
		withSetup[repoKey(repo)] = true
	}
	withReleaseSettings := make(map[string]bool, len(reposWithReleaseSettings))
	for _, repo := range reposWithReleaseSettings {
		withReleaseSettings[repoKey(repo)] = true
	}
	withWorkflows := make(map[string]bool, len(reposWithWorkflows))
	for _, repo := range reposWithWorkflows {
		withWorkflows[repoKey(repo)] = true
	}
	byRepo := make(map[string]*RepoSummary)
	row := func(repo string) *RepoSummary {
		key := repoKey(repo)
		if r, ok := byRepo[key]; ok {
			return r
		}
		r := &RepoSummary{
			Repo:                repo,
			DefaultCapabilities: caps[key],
			PromptExtension:     withPrompt[key],
			SetupCommand:        withSetup[key],
			Workflows:           withWorkflows[key],
			ReleaseSettings:     withReleaseSettings[key],
		}
		byRepo[key] = r
		return r
	}
	for _, repo := range targetRepos {
		row(repo).Configured = true
	}
	for repo := range repoDefaults {
		row(repo)
	}
	for _, repo := range reposWithPromptExtension {
		row(repo)
	}
	for _, repo := range reposWithSetupCommand {
		row(repo)
	}
	for _, repo := range reposWithWorkflows {
		row(repo)
	}
	for _, repo := range reposWithReleaseSettings {
		row(repo)
	}
	for _, repo := range reposWithCICapabilities {
		row(repo).CICapabilities = true
	}
	// An allowlist is the deployment saying which repos may be worked in
	// at all, so where there is one a task counts towards a row the two
	// sources above already raised and never raises one of its own. See
	// this function's own doc comment.
	restricted := len(targetRepos) > 0
	for _, t := range tasks {
		// A task nobody has pointed at a repo yet is omitted rather than
		// grouped under a blank name, the same as repoRows.
		if t.Repo == "" {
			continue
		}
		r, ok := byRepo[repoKey(t.Repo)]
		if !ok {
			if restricted {
				continue
			}
			r = row(t.Repo)
		}
		r.Tasks++
		if r.States == nil {
			r.States = make(map[model.State]int)
		}
		r.States[t.State]++
		if t.Blocked {
			r.Blocked++
		}
	}
	out := make([]RepoSummary, 0, len(byRepo))
	for _, r := range byRepo {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out
}

// ciCapabilityIDs lists capabilities that have a CI shell implementation.
//
// The per-task picker's own listing (c.Config.Capabilities) decides this
// for every capability a task can also hold: a deployment that does not
// offer one to tasks is not offering it to that repo's CI either. A
// CI-only capability (model.CapabilitySpec.CIOnly) has no row there by
// construction and is listed here on the strength of the catalog alone
// -- otherwise taking registry-push and package-publish out of the task
// picker would have taken them out of the only picker that *should*
// have them, and silently unconfigured the repositories already
// publishing with one.
func (c *Client) ciCapabilityIDs() []string {
	ids := []string{}
	for _, spec := range capabilityCatalog() {
		if !spec.SupportsCI() {
			continue
		}
		if _, offered := c.capabilityByID(spec.Name); offered || spec.CIOnly {
			ids = append(ids, spec.Name)
		}
	}
	return ids
}

// SetRepoCICapabilities replaces only the repo's explicit CI grants.
func (c *Client) SetRepoCICapabilities(ctx context.Context, repo string, ids []string) (RepoDefaults, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return RepoDefaults{}, validationErrorf("repo: %v", err)
	}
	clean, err := c.cleanRepoCICapabilities(ids)
	if err != nil {
		return RepoDefaults{}, err
	}
	return c.putRepoConfig(ctx, parsed, func(rc *model.RepoConfig) { rc.CICapabilities = clean })
}

func (c *Client) cleanRepoCICapabilities(ids []string) ([]string, error) {
	allowed := c.ciCapabilityIDs()
	clean := []string{}
	for _, id := range ids {
		canonicalID := id
		if model.IsCapabilityRef(id) {
			// A capability server method, with its with: arguments: a
			// CI job's grant admission mints it through its server
			// (daemon.CiGrantReservations), so the repo may grant it
			// like any in-process CI capability. Normalized the way a
			// task's own grant is, so equal grants are kept once.
			normalized, err := c.normalizeCapabilityID(id)
			if err != nil {
				return nil, err
			}
			if _, ok := c.capabilityByID(normalized); !ok {
				return nil, validationErrorf("ciCapabilities: %s", capabilityRefusal(normalized))
			}
			canonicalID = normalized
		} else if target, ok := model.RepoReadTarget(id); ok {
			canonicalID = model.RepoReadCapability(target)
		} else if target, ok := model.RepoWriteTarget(id); ok {
			canonicalID = model.RepoWriteCapability(target)
		} else if !slices.Contains(allowed, id) {
			return nil, validationErrorf("ciCapabilities: capability %q is not supported for CI", id)
		}
		if !slices.Contains(clean, canonicalID) {
			clean = append(clean, canonicalID)
		}
	}
	return clean, nil
}

// setRepoCICapabilitiesRequest is PUT
// /api/repos/{owner}/{name}/ci-capabilities's body.
type setRepoCICapabilitiesRequest struct {
	CICapabilities []string `json:"ciCapabilities"`
}

func (s *Server) handleSetRepoCICapabilities(w http.ResponseWriter, r *http.Request) {
	var req setRepoCICapabilitiesRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoCICapabilities(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"), req.CICapabilities)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleGetRepoCapabilities(w http.ResponseWriter, r *http.Request) {
	defaults, err := s.tasks.RepoDefaults(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleSetRepoCapabilities(w http.ResponseWriter, r *http.Request) {
	var req SetRepoCapabilitiesRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoDefaultCapabilities(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.DefaultCapabilities)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

// handleGetRepoPromptExtension answers with the same whole-defaults
// document handleGetRepoCapabilities does (RepoDefaults' own doc comment
// on why there is one document behind both routes) -- so a pane that
// only edits the prompt extension still reads one response and gets the
// deployment-wide text it is appended to along with it.
func (s *Server) handleGetRepoPromptExtension(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoPromptExtension(w http.ResponseWriter, r *http.Request) {
	var req SetRepoPromptExtensionRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoPromptExtension(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.PromptExtension)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

// handleGetRepoSetupCommand answers with the same whole-defaults
// document its two siblings do -- one document behind all three routes,
// RepoDefaults' own doc comment has why.
func (s *Server) handleGetRepoSetupCommand(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoSetupCommand(w http.ResponseWriter, r *http.Request) {
	var req SetRepoSetupCommandRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoSetupCommand(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.SetupCommand)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

// handleGetRepoDeclarations answers with this repo's whole RepoDefaults,
// the same as its three neighbours: one shape for a page that reads
// several of these routes and shows one repository.
func (s *Server) handleGetRepoDeclarations(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoDeclarations(w http.ResponseWriter, r *http.Request) {
	var req SetRepoDeclarationsRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoDeclarations(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.Declarations)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

// handleGetRepoRequireBranchUpToDate answers with this repo's whole
// RepoDefaults, the same as its neighbours: one shape for a page that
// reads several of these routes and shows one repository.
func (s *Server) handleGetRepoRequireBranchUpToDate(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoRequireBranchUpToDate(w http.ResponseWriter, r *http.Request) {
	var req SetRepoRequireBranchUpToDateRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoRequireBranchUpToDate(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.RequireBranchUpToDate)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleGetRepoIgnoreGitHubCI(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

// handleGetRepoWatchDefaultBranch answers with this repo's whole
// RepoDefaults, the same as its neighbours.
func (s *Server) handleGetRepoWatchDefaultBranch(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoWatchDefaultBranch(w http.ResponseWriter, r *http.Request) {
	var req SetRepoWatchDefaultBranchRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoWatchDefaultBranch(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.WatchDefaultBranch)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleSetRepoIgnoreGitHubCI(w http.ResponseWriter, r *http.Request) {
	var req SetRepoIgnoreGitHubCIRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoIgnoreGitHubCI(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.IgnoreGitHubCI)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleGetRepoCIMode(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoCIMode(w http.ResponseWriter, r *http.Request) {
	var req SetRepoCIModeRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoCIMode(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.CIMode)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleGetRepoSettings(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoSettings(w http.ResponseWriter, r *http.Request) {
	var req SetRepoSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoSettings(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

// handleGetRepoUnfixableChecks answers with this repo's whole
// RepoDefaults, the same as its neighbours: one shape for a page that
// reads several of these routes and shows one repository.
func (s *Server) handleGetRepoUnfixableChecks(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoUnfixableChecks(w http.ResponseWriter, r *http.Request) {
	var req SetRepoUnfixableChecksRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoUnfixableChecks(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.UnfixableChecks)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleAddTargetRepo(w http.ResponseWriter, r *http.Request) {
	teamID := r.URL.Query().Get("team")
	var req AddRepoRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Team != "" {
		teamID = req.Team
	}
	if teamID != "" && s.tasks.Store != nil {
		settings, err := s.tasks.AddTargetRepoForTeam(r.Context(), teamID, req.Repo, req.CloneURL)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
		return
	}
	settings, err := s.tasks.AddTargetRepoWithCloneURL(r.Context(), req.Repo, req.CloneURL)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleRemoveTargetRepo answers DELETE /api/repos/{owner}/{name}: take a
// repo off a team's targets (?team=, or the default team without one).
func (s *Server) handleRemoveTargetRepo(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("owner") + "/" + r.PathValue("name")
	teamID := r.URL.Query().Get("team")
	if teamID != "" && s.tasks.Store != nil {
		settings, err := s.tasks.RemoveTargetRepoForTeam(r.Context(), teamID, repo)
		if err != nil {
			writeClientError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
		return
	}
	settings, err := s.tasks.RemoveTargetRepo(r.Context(), repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleGetRepoMergeOnGreen answers with this repo's whole RepoDefaults,
// the same as its neighbours.
func (s *Server) handleGetRepoMergeOnGreen(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoMergeOnGreen(w http.ResponseWriter, r *http.Request) {
	var req SetRepoMergeOnGreenRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoMergeOnGreen(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.MergeOnGreen)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *Server) handleGetRepoBaseRepairBypassesHumanGates(w http.ResponseWriter, r *http.Request) {
	s.handleGetRepoCapabilities(w, r)
}

func (s *Server) handleSetRepoBaseRepairBypassesHumanGates(w http.ResponseWriter, r *http.Request) {
	var req SetRepoBaseRepairBypassesHumanGatesRequest
	if !readJSON(w, r, &req) {
		return
	}
	defaults, err := s.tasks.SetRepoBaseRepairBypassesHumanGates(r.Context(),
		r.PathValue("owner")+"/"+r.PathValue("name"), req.BaseRepairBypassesHumanGates)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}
