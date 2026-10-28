package repodecl

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A workflow that declares `on: schedule` declares a schedule, so this
// package reads it as one (grain/task-344).
//
// It is the same translation grain already makes one layer down: a
// repository's `.github/workflows` are not refused, they are read as grain
// jobs and run through act. A cron is a foreign spelling of something grain
// has -- a recurring claim on this deployment's agents and sandboxes -- and
// the answer is to spell it in grain's own vocabulary rather than to grow a
// second scheduler inside pkg/ci that no operator can see, pause or read
// the history of.
//
// What comes out is an ordinary declared schedule: the declared id
// namespace, the `repo_declaration` provenance row pointing at the workflow
// file, refused in the UI by the same Client.refuseDeclaredEdit with the
// same message, removed when the repository's declarations are turned off,
// and removed when the cron is. Everything that fires, lists or renders a
// schedule goes on working with no idea this one came out of a workflow
// file.
//
// # The switch is the point
//
// These schedules ride RepoConfig.Declarations exactly as the files under
// `.grain/schedules` do, and that is not an implementation convenience.
// model's repo_declaration.go: a declaration is "a standing claim on this
// deployment's agent capacity, written in a repository whose files every
// run grain dispatches there can rewrite". A workflow file is that in every
// particular, so without the switch any repository could conjure recurring
// agent work by adding two lines to a CI file. Reading them here, in the
// package the switch already gates, is what makes them gated.
//
// # What it may not say
//
// The same line every other declaration is held to, and for the same
// reason: a workflow file says *when*, and nothing about what the work may
// touch. The task it files targets the declaring repository, reads nothing
// else, and holds the capabilities grain's own template asks for rather
// than any the file names.
//
// A job in that file may now say `capabilities:` (ci.Job.Capabilities,
// grain/task-486), and nothing about the sentence above changes: that key
// is a *requirement* of a CI job -- what its own steps cannot run without,
// resolved against what the deployment has granted this repository, and
// refused by not running the job -- and it is read nowhere near the task a
// cron files. A workflow that requires `gcp-key` for its publish job still
// files an agent task holding exactly what the deployment gives agent tasks
// in this repository.

// workflowSchedulePrefix keeps a workflow-derived schedule's identity out
// of the way of the ones a repository writes by hand.
//
// A declared row's id is derived from its file stem
// (model.RepoDeclaredID), and `.grain/schedules/nightly.yml` and
// `.grain/workflows/nightly.yml` are two different declarations with the
// same stem -- this repository had both for as long as it took the
// hand-written nightly review to be retired in favour of this translation
// (a5d12f849), and any repository may have both at any time. Without a
// prefix they would be one row written twice, with whichever was read last
// winning.
const workflowSchedulePrefix = "workflow."

// scheduledWorkflows turns every cron in a repository's own workflows --
// ci.LoadNativeDir's answer for its tree -- into a declared schedule.
//
// Only `.grain/workflows` is read (ci.LoadNativeDir), never
// `.github/workflows`: GitHub's own scheduler already runs those crons, and
// a deployment that translated them too would run every nightly twice.
// pkg/ci's rawOn.triggers is the other half of that decision -- a cron in a
// borrowed Actions file is not even parsed into a cadence.
//
// A workflow file that does not parse fails here the way a malformed
// declaration file does, which is also how an inexpressible cron is
// refused: pkg/ci's ParseCron is what says no, at parse time, naming the
// key and the cadences a task schedule has. A repository whose workflow is
// broken keeps the declarations it already had (Store.SyncRepoDeclarations
// is never reached) and the reason is recorded against the repository, the
// same as for any other unreadable declaration.
func scheduledWorkflows(workflows []ci.Workflow, repo model.RepoRef) ([]model.DeclaredScheduleFile, error) {
	var out []model.DeclaredScheduleFile
	for _, workflow := range workflows {
		crons := workflow.On.Schedule
		if len(crons) == 0 {
			continue
		}
		stem := model.DeclarationStem(workflow.Path)
		if err := model.ValidateDeclarationStem(stem); err != nil {
			return nil, fmt.Errorf("%s: %w -- and its `on: schedule` cron makes it one", workflow.Path, err)
		}
		for i, cron := range crons {
			sched, err := workflowSchedule(workflow, stem, cron, i, len(crons), repo)
			if err != nil {
				return nil, err
			}
			out = append(out, model.DeclaredScheduleFile{Path: workflow.Path, Schedule: sched})
		}
	}
	return out, nil
}

// workflowSchedule is one cron's schedule.
//
// Base is left empty on purpose: empty means the repository's default
// branch, which is the branch GitHub runs a scheduled workflow on and the
// only branch a cron could sensibly mean -- a workflow file cannot name a
// branch for its own cron, so neither does this.
//
// Overlap is skip, the default every schedule has: a nightly whose previous
// review has not finished should not have a second one filed on top of it.
func workflowSchedule(workflow ci.Workflow, stem string, cron ci.ScheduleTrigger,
	index, of int, repo model.RepoRef) (model.Schedule, error) {

	templateID := model.WorkflowReviewTemplateID(stem)
	template := model.SystemTemplate(templateID, model.RepoRef{})
	if template == nil {
		// Unreachable unless the two packages have been changed apart:
		// worth saying out loud rather than filing a schedule pointing at
		// a template nothing can resolve.
		return model.Schedule{}, fmt.Errorf("%s: grain has no template to review a scheduled workflow with (%s)",
			workflow.Path, templateID)
	}
	return model.Schedule{
		ID:     model.RepoDeclaredID(model.DeclaredSchedule, repo, workflowScheduleStem(stem, index, of)),
		Title:  template.Title,
		Target: repo,
		// TemplateID rather than an inline title and body, for the reason
		// every declared schedule uses one: the work is grain's to say
		// (model.workflowReviewTemplate) and is resolved fresh at every
		// firing, so upgrading grain upgrades what the next firing does.
		TemplateID:           &templateID,
		TimeZone:             cron.TimeZone,
		Recurrence:           cron.Recurrence,
		Overlap:              model.OverlapSkip,
		WorkflowReview:       cron.Review,
		Enabled:              true,
		AutoApproveProposals: true,
	}, nil
}

// workflowScheduleStem is the identity of the schedule one cron of one
// workflow produces.
//
// A workflow with a single cron -- which is nearly all of them -- gets the
// workflow's own stem, so editing the cron updates the row it already had
// rather than leaving the old one behind. A workflow with several gets them
// numbered in file order, which is the one place this identity is weaker
// than it looks: reordering the `schedule:` list relocates the schedules
// between each other. What that costs is a schedule's own firing history
// (NextRunAt/LastRunAt), not a firing -- the same cadences come out of the
// same file either way.
func workflowScheduleStem(stem string, index, of int) string {
	if of <= 1 {
		return workflowSchedulePrefix + stem
	}
	return fmt.Sprintf("%s%s.%d", workflowSchedulePrefix, stem, index+1)
}
