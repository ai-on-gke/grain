package metrics

// Whether runs actually reach for their own pull request, and whether the
// ones that do still leave a red build behind.
//
// README.md's "Telling the run it has it" ends on exactly these two
// questions and calls them the thing worth measuring rather than
// assuming, and until now neither had an answer anywhere. Both were
// asked once before (grain/task-48) and could not be taken: the tool was
// not in main yet, and a *succeeded* run recorded nothing at all about
// which tools it called, so "did this run call open_pull_request" was
// only ever recoverable by reading prose. The census (model/telemetry.go)
// fixed the second half; this is the first half read off it.
//
// The two questions are different shapes and are reported as such.
//
// Adoption is a share of runs: of the runs that were offered the loop at
// all, how many took it. The denominator is the whole of what makes that
// number mean anything, which is why it is reported beside the rate --
// see PullRequests.Runs.
//
// Follow-through is a comparison, not a share. "A run called
// open_pull_request and its task still needed a fix task" is only
// interesting against the same rate for the runs that did not call it:
// on its own it is a number with no scale, since some fraction of tasks
// go red for reasons no amount of watching would have caught. So both
// cohorts are reported, each with its own denominator.
//
// Everything here is derived, like the rest of this package: two columns
// this build now reads off rows the store already had (TaskTiming.Targeted,
// TaskTiming.FixTaskFiled) joined in memory to the census rows a run
// writes about itself.

import "github.com/bwsalmon/grain/pkg/model"

// FixTaskRate is one cohort's follow-through: how many of its runs
// belonged to a task the merge queue later had to file a fix task for
// (model.LinkFixTask), which is the recorded form of "this branch was
// left red and somebody else had to deal with it".
//
// Runs is reported beside Rate for the reason every other denominator in
// this package is: a rate over four runs is not a rate.
type FixTaskRate struct {
	Runs     int
	FixTasks int
	Rate     float64
}

// PullRequests is the mid-run pull request loop over the window.
type PullRequests struct {
	// Runs is the denominator for everything below: runs that finished
	// inside the window, recorded a census at all, and belonged to a task
	// with a target repo.
	//
	// All three conditions matter. A run with no census either predates
	// the census or made no tool calls whatsoever, and neither is a run
	// that decided anything. And a task with no target got no
	// push/check/repair paragraph in its prompt and no open_pull_request
	// sentence either (orchestrator.BuildPrompt puts both inside
	// `task.Target != nil`), so counting it would be counting runs that
	// were never asked.
	Runs int
	// Opened is how many of Runs called open_pull_request at least once,
	// and Calls is how many calls they made between them -- a run that
	// opens its pull request and then calls again for each round of
	// checks is the shape the tool was written for, and Calls over
	// Opened is whether that is what runs actually do.
	Opened int
	Calls  int
	// AdoptionRate is Opened/Runs.
	//
	// What it measures is the tool's uptake overall, and not -- as the
	// question was originally posed -- prompt wording against a tool
	// description on its own. There is no period in this deployment's
	// history where the tool existed and BuildPrompt did not name it:
	// the change that named it (grain/task-37) was stacked on the change
	// that added it (grain/task-26) and the two merged half an hour
	// apart. That contrast is not available from production data and this
	// number does not claim to be it.
	AdoptionRate float64

	// WithTool and WithoutTool are the follow-through comparison, over
	// the same Runs split in two by whether the run called the tool.
	//
	// Read them against each other and read both coarsely. The fix-task
	// link is on the *task*, not the attempt, so every finished attempt
	// of a task that ever went red counts as having gone red; and a fix
	// task can only exist for a task that got far enough to have a pull
	// request at all, which flatters any cohort holding runs that pushed
	// nothing. What the pair is good for is a difference between two
	// populations measured the same wrong way, and what it is not good
	// for is either number read alone as "the rate at which runs leave
	// red builds".
	//
	// The fine-grained version of this question -- did a push follow the
	// last failing report -- is only answerable by reading one run's
	// transcript by eye, and deliberately is not counted here: a
	// transcript renders a call as a `> name(args)` line that a tool's
	// own output can contain verbatim (agent/claude/transcript.go), so
	// counting calls out of it in bulk measures forgeries as well as
	// calls.
	WithTool    FixTaskRate
	WithoutTool FixTaskRate
}

// openPullRequestToolName is pkg/mcp's own name for the tool, spelled out
// rather than imported for the reason waitVerdictPassed is: this package
// reads rows, and a report has no business depending on the package that
// serves an agent's tools. The census column is the contract between
// them, and a rename on the far side leaves this reading zero -- which is
// what pullRequestsOf's test pins.
const openPullRequestToolName = "open_pull_request"

// pullRequestsOf builds the section from the whole store's rows, windowed
// against each run's own ending like every other census-derived number
// here (finishedInWindow).
func pullRequestsOf(w Window, tasks []model.TaskTiming, runs []model.RunTiming, uses []model.RunToolUse) PullRequests {
	finished := finishedInWindow(w, runs)

	// Which runs recorded a census at all, and which of them called the
	// tool. Both are read from the same rows, so a run that called it is
	// never counted as one that chose not to.
	censused := map[string]bool{}
	calls := map[string]int{}
	for _, use := range uses {
		if !finished[use.RunID] {
			continue
		}
		censused[use.RunID] = true
		if use.Tool == openPullRequestToolName {
			calls[use.RunID] += use.Calls
		}
	}

	byTask := make(map[string]model.TaskTiming, len(tasks))
	for _, t := range tasks {
		byTask[t.TaskID] = t
	}

	var out PullRequests
	for _, r := range runs {
		if !finished[r.RunID] || !censused[r.RunID] {
			continue
		}
		task, ok := byTask[r.TaskID]
		if !ok || !task.Targeted {
			continue
		}
		out.Runs++

		cohort := &out.WithoutTool
		if n := calls[r.RunID]; n > 0 {
			out.Opened++
			out.Calls += n
			cohort = &out.WithTool
		}
		cohort.Runs++
		if task.FixTaskFiled {
			cohort.FixTasks++
		}
	}

	if out.Runs > 0 {
		out.AdoptionRate = float64(out.Opened) / float64(out.Runs)
	}
	out.WithTool.rate()
	out.WithoutTool.rate()
	return out
}

func (f *FixTaskRate) rate() {
	if f.Runs > 0 {
		f.Rate = float64(f.FixTasks) / float64(f.Runs)
	}
}
