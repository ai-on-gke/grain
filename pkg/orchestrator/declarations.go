package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// DeclarationSource is where a repository's own `.grain` declarations
// are read from: the tip of its default branch, and what that tree
// declares (pkg/repodecl).
//
// An interface for ci.Source's reason, one layer up. A deployment reads
// them by cloning the repository on the daemon's host with the
// credential it already holds (cmd/grain's repoDeclarationSource); a
// test reads a directory it wrote itself; and nothing here needs to know
// which, or to grow a git dependency to find out.
//
// Head exists separately from Read because the answer is nearly always
// "nothing has changed". A repository is polled every few minutes and
// moves a few times a day at most, so one `git ls-remote` stands between
// this reconciler and a clone per repository per poll.
type DeclarationSource interface {
	// Head is the commit at the tip of repo's default branch.
	Head(ctx context.Context, repo model.RepoRef) (string, error)
	// Read returns what repo declares at the tip of its default branch,
	// and the commit it read them at.
	Read(ctx context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error)
}

// declarationPollInterval is how often one repository is looked at.
//
// Minutes rather than seconds because this is a network call per
// repository against a forge with rate limits, and because the thing
// being waited for is a merge -- a declaration that lands two minutes
// after it merges is a declaration that landed immediately as far as
// anybody watching is concerned. Minutes rather than hours because the
// first thing somebody does after merging one is wonder whether it
// worked.
const declarationPollInterval = 2 * time.Minute

// reconcileDeclarations keeps this deployment's declared templates,
// suites and schedules level with what the repositories that declare
// them currently say (grain/task-60, model's repo_declaration.go).
//
// It is the "kept up to date as the repo changes" half of the feature,
// and it is a poll rather than a webhook for the reason everything else
// in this cycle is: grain's own README, "Input is a model update, not a
// GitHub issue" -- there is no event to subscribe to that a restart, an
// outage or a deployment created after the fact would not miss, and a
// reconciler that re-reads the world is one that heals itself. The cost
// of the poll is what the commit check above bounds.
//
// Three things happen here, in this order, and the order matters only
// for the first:
//
//   - A repository whose switch has been turned off has everything it
//     declared removed, before anything else this cycle can fire one.
//     RepoConfig.Declarations is what an operator reaches for when a
//     repository's standing work has to stop, and a schedule that fires
//     one more time after they turned it off would make a liar of it.
//   - A repository that has not moved since its last read is recorded as
//     looked at and left alone.
//   - A repository that has moved, or that has never been read, is read
//     and its declarations replace whatever it declared before.
//
// A repository that cannot be read -- unreachable, or with a file that
// does not parse -- keeps the declarations it already had, and the
// reason is recorded against it (Store.RecordDeclarationFailure has why
// that is the right way round). One repository failing does not stop the
// others, the same way one schedule failing to fire does not.
func reconcileDeclarations(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Declarations == nil {
		return nil
	}
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing repo configs to read their declarations: %w", err)
	}
	targets, err := deps.Store.TargetRepos(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading target repos for declarations: %w", err)
	}
	enabled := map[model.RepoRef]bool{}
	var order []model.RepoRef
	for _, cfg := range configs {
		if !cfg.Declarations {
			continue
		}
		if model.RepoAllowed(targets, cfg.Repo) {
			enabled[cfg.Repo] = true
			order = append(order, cfg.Repo)
		}
	}

	var errs []error
	known, err := deps.Store.DeclaringRepos(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: listing repos with declarations: %w", err))
	}
	for _, repo := range known {
		if enabled[repo] {
			continue
		}
		log.Printf("orchestrator: %s no longer declares its own templates, suites and schedules: removing them", repo)
		if err := deps.Store.ForgetRepoDeclarations(ctx, repo); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: removing %s's declarations: %w", repo, err))
		}
	}

	for _, repo := range order {
		if err := syncDeclarations(ctx, deps, repo, now, time.UTC); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// syncDeclarations is one repository's turn.
func syncDeclarations(ctx context.Context, deps Deps, repo model.RepoRef,
	now time.Time, loc *time.Location) error {
	last, err := deps.Store.GetRepoDeclarationSync(ctx, repo)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's declaration sync: %w", repo, err)
	}
	if last != nil && now.Sub(last.CheckedAt) < declarationPollInterval {
		return nil
	}

	head, err := deps.Declarations.Head(ctx, repo)
	if err != nil {
		return recordDeclarationFailure(ctx, deps, repo, now,
			fmt.Errorf("reading %s's default branch: %w", repo, err))
	}
	if last != nil && last.LastError == "" && head == last.Commit {
		if err := deps.Store.RecordDeclarationCheck(ctx, repo, now); err != nil {
			return fmt.Errorf("orchestrator: recording %s's declaration check: %w", repo, err)
		}
		return nil
	}
	if head == "" {
		// An empty repository: a default branch with no commit on it.
		// There is nothing to clone, and it declares nothing -- which is
		// a successful read of nothing rather than a failure, so it is
		// recorded as one and polled cheaply from here on.
		changed, err := deps.Store.SyncRepoDeclarations(ctx, repo, "", model.RepoDeclarations{}, now, loc)
		if err != nil {
			return recordDeclarationFailure(ctx, deps, repo, now,
				fmt.Errorf("storing %s's declarations: %w", repo, err))
		}
		logSettingsChanges(repo, "", "an empty repository declares nothing", changed)
		return nil
	}

	declared, commit, err := deps.Declarations.Read(ctx, repo)
	if err != nil {
		return recordDeclarationFailure(ctx, deps, repo, now,
			fmt.Errorf("reading %s's declarations: %w", repo, err))
	}
	if commit == "" {
		commit = head
	}
	if err := errors.Join(checkTriggerTemplates(ctx, deps.Store, declared.Triggers),
		checkTriggerApprovers(ctx, deps.Store, declared.Triggers)); err != nil {
		return recordDeclarationFailure(ctx, deps, repo, now,
			fmt.Errorf("reading %s's declarations: %w", repo, err))
	}
	changed, err := deps.Store.SyncRepoDeclarations(ctx, repo, commit, declared, now, loc)
	if err != nil {
		return recordDeclarationFailure(ctx, deps, repo, now,
			fmt.Errorf("storing %s's declarations: %w", repo, err))
	}
	log.Printf("orchestrator: %s at %s declares %d template(s), %d suite(s), %d schedule(s), %d trigger(s)",
		repo, shortCommit(commit), len(declared.Templates), len(declared.Suites), len(declared.Schedules),
		len(declared.Triggers))
	logScheduledWorkflowCadence(repo, commit, declared.Schedules)
	if declared.MergeRules != nil {
		log.Printf("orchestrator: %s at %s declares merge rules in %s: %s", repo, shortCommit(commit),
			declared.MergeRules.Path, strings.Join(declared.MergeRules.SortedRuleIDs(), ", "))
		for _, w := range declared.MergeRules.Warnings {
			log.Printf("orchestrator: %s at %s: warning: %s", repo, shortCommit(commit), w)
		}
	}
	source := "withdrawing its settings declaration"
	if declared.Settings != nil {
		source = declared.Settings.Path
	}
	logSettingsChanges(repo, commit, source, changed)
	if declared.Settings != nil {
		logPortTypesWarnings(ctx, deps.Store, repo, commit)
	}
	return nil
}

// logPortTypesWarnings says, after a sync that read a settings file,
// which of the repository's `port-types` entries have no effect
// (model.RepoConfig.PortTypesWarnings): a direction naming a branch that
// is not paired, or one the operator's own pair overrides. The sync
// accepts both -- neither grants anything -- and logSettingsChanges
// reports the declared value as written, so without this the direction
// somebody meant to filter goes on porting every merge with nothing
// saying so. The same sentences are on the repository's page
// (ui.RepoDeclarationsStatus.Warnings).
func logPortTypesWarnings(ctx context.Context, store *model.Store, repo model.RepoRef, commit string) {
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		log.Printf("orchestrator: %s at %s: reading its config to check port-types: %v", repo, shortCommit(commit), err)
		return
	}
	if cfg == nil {
		return
	}
	for _, w := range cfg.PortTypesWarnings() {
		if strings.HasPrefix(w, model.DeclaredPortTypesWarningPrefix) {
			log.Printf("orchestrator: %s at %s: warning: %s", repo, shortCommit(commit), w)
		}
	}
}

// checkTriggerTemplates refuses a path trigger naming a deployment
// template this deployment does not have -- the half of "the template
// exists" pkg/repodecl cannot check, since it reads a tree and nothing
// else. A trigger naming one of the repository's own templates was
// checked there. Refusing the sync keeps the last good commit's triggers
// running, the way any other declaration error does.
func checkTriggerTemplates(ctx context.Context, store *model.Store, triggers []model.PathTrigger) error {
	var errs []error
	for _, t := range triggers {
		if strings.HasPrefix(t.Template, model.RepoDeclaredPrefix) || t.Template == model.TemplateNone {
			continue
		}
		tmpl, err := store.ResolveTemplate(ctx, t.Template, model.RepoRef{})
		if err != nil {
			return err
		}
		if tmpl == nil {
			errs = append(errs, fmt.Errorf("%s: no template %q in this repository or this deployment", t.Path, t.Template))
		}
	}
	return errors.Join(errs...)
}

// checkTriggerApprovers refuses a sensitive trigger naming an approver
// this deployment does not know -- a misspelt team would otherwise be a
// hold nobody can release, found out only when somebody needs it
// released. Like checkTriggerTemplates, refusing the sync keeps the last
// good commit's triggers, and holds every self-merging change in the
// repository until it is fixed (model.TriggerDeclarationsBroken).
func checkTriggerApprovers(ctx context.Context, store *model.Store, triggers []model.PathTrigger) error {
	var errs []error
	for _, t := range triggers {
		for _, a := range t.Approvers {
			if err := store.CheckApprover(ctx, a); err != nil {
				errs = append(errs, fmt.Errorf("%s: approvers: %w", t.Path, err))
			}
		}
	}
	return errors.Join(errs...)
}

// logScheduledWorkflowCadence says so when a repository declares a
// scheduled workflow whose cron fires more often than grain is willing
// to wait for one firing's run (scheduledWorkflowDeadline).
//
// That combination breaks nothing on an ordinary night, which is exactly
// why it needs saying. A firing of one of these is a run *and* the
// review of it, and nothing fires again until both are done
// (model.Store.workflowFiringUnfinished, which is why the translation
// hard-codes OverlapSkip). So as long as runs conclude, a tight cadence
// behaves perfectly. The first time a run's engine forgets to conclude
// one, grain waits the deadline out, files the review as a night that
// never ended -- and by then the next firing has already come due and
// been suppressed, and so is the one after it. The suite stops running,
// and the review that exists to report exactly this is the thing that
// has stopped. Nothing else anywhere notices.
//
// TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence asserts
// the same bound over this repository's own `.grain/workflows`, where a
// build can fail over it. It cannot reach anybody else's: those crons
// are translated at sync time out of a tree grain has just read
// (repodecl's scheduledWorkflows), so this is the only moment the
// question can be put to them. A log line and not a refusal, because the
// cadence is not wrong -- a six-hourly suite is a reasonable thing to
// want -- and refusing it here would throw away every other declaration
// in the repository (scheduledWorkflows returns one error for the whole
// tree) over a configuration that works until it doesn't.
//
// Once per commit rather than once per poll: syncDeclarations returns
// early while the declaring repository's head is where it was, so this
// is reached again only when that repository has actually changed.
func logScheduledWorkflowCadence(repo model.RepoRef, commit string, schedules []model.DeclaredScheduleFile) {
	for _, declared := range schedules {
		if declared.Schedule.TemplateID == nil {
			continue
		}
		if _, ok := model.WorkflowReviewStem(*declared.Schedule.TemplateID); !ok {
			continue
		}
		interval, err := shortestInterval(declared.Schedule.Recurrence)
		if err != nil || interval > scheduledWorkflowDeadline {
			// Either a cadence there is room for, or one this cannot
			// measure -- and a warning guessed at is worse than none.
			continue
		}
		log.Printf("orchestrator: %s at %s declares a scheduled workflow in %s that fires every %s, "+
			"and grain waits %s for a firing's run before it gives up and reviews it unfinished -- "+
			"so the first run nothing concludes is still unreviewed when the next firing is due, "+
			"overlap suppresses that firing and every one after it, and this workflow stops running "+
			"with nothing but this line saying so. A cron longer than %s is what keeps the backstop "+
			"a backstop.",
			repo, shortCommit(commit), declared.Path, interval,
			scheduledWorkflowDeadline, scheduledWorkflowDeadline)
	}
}

// logSettingsChanges is the record of what one sync did to a
// repository's settings row: one line, naming every column it wrote and
// both values, in the daemon log beside the merge queue's own lines
// (grain/task-476).
//
// A settings declaration writes two columns of its own and cannot reach
// any field an operator wrote (model.RepoSettings), so there is no loss
// here to announce -- but there is still a change nobody is told about
// at the time. A repository's declared prompt extension is appended to
// what every agent dispatched there is handed, and its declared setup
// command runs in every checkout of it, so editing either one by merging
// a file surfaces later as an agent behaving differently, with nothing
// connecting that to the commit. Saying so when it happens is the only
// thing that makes it diagnosable afterwards.
//
// Nothing is logged for the ordinary case, which is every sync after the
// file's own last edit: by then the columns already say what the file
// says and there is no change to report.
func logSettingsChanges(repo model.RepoRef, commit, source string, changed []model.SettingsChange) {
	if len(changed) == 0 {
		return
	}
	at := ""
	if commit != "" {
		at = " at " + shortCommit(commit)
	}
	log.Printf("orchestrator: %s%s: %s changes %d settings field(s) of this repository: %s",
		repo, at, source, len(changed), model.DescribeSettingsChanges(changed))
}

// recordDeclarationFailure writes the reason onto the repository's sync
// row and hands the error back to the cycle, which logs it: the row is
// what somebody looking at the repository sees, and the cycle's own log
// is what somebody looking at the daemon sees, and a failure this quiet
// deserves both.
func recordDeclarationFailure(ctx context.Context, deps Deps, repo model.RepoRef,
	now time.Time, cause error) error {
	if err := deps.Store.RecordDeclarationFailure(ctx, repo, cause.Error(), now); err != nil {
		return errors.Join(cause, fmt.Errorf("orchestrator: recording %s's declaration failure: %w", repo, err))
	}
	return fmt.Errorf("orchestrator: %w", cause)
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
