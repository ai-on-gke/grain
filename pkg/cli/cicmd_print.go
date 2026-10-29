// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// cicmd_print.go is how `grain ci` (cicmd.go) prints what the daemon
// answered: a run's jobs and steps, a listing of runs, the job cache and a
// run's artifacts, as text or, under -json, as the route's own body.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	cicli "github.com/ai-on-gke/grain/pkg/ci/cli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ciRuns prints a listing, under a line saying what it left out.
//
// Which runs are *not* here is the question a bounded list raises and is
// the one the old printer could not answer: three different things put a
// run outside it -- this deployment no longer remembers it, it did not
// match the filter, or the limit stopped short of it -- and they send a
// reader three different places (nowhere, a wider filter, a bigger
// -limit). The response carries both counts for exactly that
// (ui.CIRunsResponse's Matched and Stored, from ci.RunPage), and
// list_ci_runs has been saying the same sentence above its own list
// since grain/task-231; this is a terminal's half of it.
func (p *printer) ciRuns(view ui.CIRunsResponse, filter ci.RunFilter) {
	if p.json {
		// The route's own body, so a listing piped into something else
		// and one fetched with curl carry the same fields -- the
		// enabled/runs split included, which is the part a script has to
		// branch on.
		p.encode(view)
		return
	}
	if !view.Enabled {
		fmt.Println("this deployment does not run grain CI: its daemon was started with -ci=false," +
			"\nso there is no engine here to have run anything")
		return
	}
	if len(view.Runs) == 0 {
		// An empty answer to a narrow question is not an empty
		// deployment, and the two used to read identically. Telling a
		// reader that nothing has ever run, when a thousand things have
		// and none of them matched, is the one way this listing can be
		// actively misleading.
		if view.Stored > 0 && filter.Narrowed() {
			fmt.Printf("no CI run matches %s -- this deployment remembers %d %s in all,"+
				"\nso widen the filter rather than looking for the runs elsewhere\n",
				filter.Describe(), view.Stored, plural(view.Stored, "run", "runs"))
			return
		}
		fmt.Println("no CI runs recorded -- start one with \"grain ci run -repo owner/name -branch b\"")
		return
	}
	if line := ciRunsSummary(view, filter); line != "" {
		fmt.Println(line)
	}
	now := time.Now()
	for _, run := range view.Runs {
		fmt.Println(ciRunLine(run, now))
	}
}

// ciCompactRuns prints the compact listing: a line a run -- id,
// conclusion, ref and commit -- and under it a line for each job that did
// not succeed, naming where it failed. With -json it is the route's own
// body, as ciRuns' is.
func (p *printer) ciCompactRuns(view ui.CICompactRunsResponse, filter ci.RunFilter) {
	if p.json {
		p.encode(view)
		return
	}
	if !view.Enabled {
		fmt.Println("this deployment does not run grain CI: its daemon was started with -ci=false," +
			"\nso there is no engine here to have run anything")
		return
	}
	if len(view.Runs) == 0 {
		if view.Stored > 0 && filter.Narrowed() {
			fmt.Printf("no CI run matches %s -- this deployment remembers %d %s in all\n",
				filter.Describe(), view.Stored, plural(view.Stored, "run", "runs"))
			return
		}
		fmt.Println("no CI runs recorded")
		return
	}
	if line := ciRunsSummary(ui.CIRunsResponse{Runs: make([]ci.Run, len(view.Runs)),
		Matched: view.Matched, Stored: view.Stored}, filter); line != "" {
		fmt.Println(line)
	}
	for _, run := range view.Runs {
		commit := run.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if commit == "" {
			commit = "-"
		}
		fmt.Printf("%-16s %-9s %-12s %s\n", run.ID, run.Conclusion, commit, run.Ref)
		if run.Error != "" {
			fmt.Printf("    %s\n", run.Error)
		}
		for _, job := range run.Jobs {
			fmt.Println("    " + ciCompactJobLine(job))
		}
	}
}

// ciCompactJobLine is one job of the compact listing as a line.
func ciCompactJobLine(job ui.CICompactJob) string {
	parts := []string{job.Name + ": " + ciConclusionWord(job.Conclusion)}
	if job.Step != "" {
		parts = append(parts, "step "+job.Step)
	}
	if job.Test != "" {
		parts = append(parts, "test "+job.Test)
	}
	if job.Flake {
		parts = append(parts, "flake")
	}
	if job.Fault != "" {
		parts = append(parts, "shared fault "+job.Fault)
	}
	line := strings.Join(parts, ", ")
	if job.Error != "" {
		line += " -- " + job.Error
	}
	return line
}

// ciRunsSummary is that line: what was asked, how much of the history
// answered it, and how much of the answer is below.
//
// Empty for the listing that has nothing to say -- no filter, and every
// run that matched shown -- because a bare `grain ci list` of eleven
// runs printing "11 of 11 remembered runs" above them is noise in front
// of the thing somebody actually asked for. The counts appear when one
// of them means something.
func ciRunsSummary(view ui.CIRunsResponse, filter ci.RunFilter) string {
	shown, matched, stored := len(view.Runs), view.Matched, view.Stored
	// A daemon too old to send the counts sends zeroes; they are a
	// header, not the list, so the listing goes out without one rather
	// than claiming 0 of 0.
	if matched < shown || stored < matched {
		return ""
	}
	switch {
	case filter.Narrowed() && matched > shown:
		return fmt.Sprintf("%s: %d of %d remembered runs match; showing %d (raise -limit for the rest)",
			filter.Describe(), matched, stored, shown)
	case filter.Narrowed():
		return fmt.Sprintf("%s: %d of %d remembered runs match", filter.Describe(), matched, stored)
	case matched > shown:
		return fmt.Sprintf("%d of %d remembered runs (raise -limit for the rest)", shown, stored)
	default:
		return ""
	}
}

// ciCache prints the job cache: what it holds, how much of it is being
// used, and the bounds it is kept inside. cleared says whether this is
// the answer to a clear rather than to a read, which is the one extra
// line -- how many entries went.
func (p *printer) ciCache(view ui.CICacheResponse, cleared bool) {
	if p.json {
		p.encode(view)
		return
	}
	if !view.Enabled {
		fmt.Println("this deployment does not run grain CI: its daemon was started with -ci=false," +
			"\nso there is no cache here")
		return
	}
	if !view.Available {
		// Not "the cache is empty": there is no cache, which is a
		// different thing to go and look into (the daemon logs why it
		// could not open one, at startup).
		fmt.Println("grain CI runs here with no job cache behind it: every job runs, every time." +
			"\nThe daemon log says why it could not open one.")
		return
	}
	if cleared {
		removed := 0
		if view.Removed != nil {
			removed = *view.Removed
		}
		fmt.Printf("cleared %d entries\n", removed)
	}
	s := view.Cache
	if s == nil {
		return
	}
	fmt.Printf("%d entries, %s, in %s\n", s.Entries, ciCacheBytes(s.Bytes), s.Dir)
	// Where the bounds come from, said once beside them: they are the
	// daemon's -ci-cache-max-* flags and not Settings (cmd/grain/
	// daemon.go's config.ciCacheMaxBytes has why), so an operator who
	// reads these numbers and wants different ones would otherwise go
	// looking in the pane for a field that is not there.
	fmt.Printf("  bounds      %s of %s, %d of %d entries, kept %s (the daemon's -ci-cache-max-* flags)\n",
		ciCacheBytes(s.Bytes), ciCacheBytes(s.MaxBytes), s.Entries, s.MaxEntries,
		ciCacheAge(time.Duration(s.MaxAgeSeconds)*time.Second))
	// Since this daemon started, and said so: a full cache with no hits
	// on it is a daemon that restarted a minute ago far more often than
	// it is a cache that is not working.
	lookups := s.Hits + s.Misses
	if lookups > 0 {
		fmt.Printf("  lookups     %d hits, %d misses (%.0f%% hit rate) since this daemon started\n",
			s.Hits, s.Misses, float64(s.Hits)/float64(lookups)*100)
	} else {
		fmt.Println("  lookups     none since this daemon started")
	}
	if !s.Newest.IsZero() {
		fmt.Printf("  held        oldest %s, newest %s\n",
			s.Oldest.Local().Format(time.RFC3339), s.Newest.Local().Format(time.RFC3339))
	}
	if !s.LastSweep.IsZero() {
		fmt.Printf("  last swept  %s\n", s.LastSweep.Local().Format(time.RFC3339))
	}
}

// ciCacheAge renders MaxAge in the words an operator set it in. Days
// past a couple of them: "kept 720h0m0s" is a true answer to a question
// nobody asked in hours.
func ciCacheAge(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("for %.0f days", d.Hours()/24)
	}
	return "for " + ciDuration(d)
}

// ciCacheBytes renders a size in the units the cache's own log lines use
// -- 1024-based, and coarse: nobody sizing a data disk wants the last
// three digits of a cache directory.
func ciCacheBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func (p *printer) ciStarted(run ci.Run) {
	if p.json {
		p.encode(run)
		return
	}
	fmt.Printf("started run %s: %s at %s\n", run.ID, run.Repo, ciRefDescriptionOf(run))
	for _, job := range run.Jobs {
		if job.Workflow != "" {
			fmt.Printf("  %s (%s)\n", cicli.JobName(job), job.Workflow)
		} else {
			fmt.Printf("  %s\n", cicli.JobName(job))
		}
	}
	fmt.Printf("\nfollow it with \"grain ci watch %s\", read it with \"grain ci show %s\","+
		"\nor cancel it with \"grain ci cancel %s\"\n", run.ID, run.ID, run.ID)
}

func (p *printer) ciCancelled(run ci.Run) {
	if p.json {
		p.encode(run)
		return
	}
	// The route answers 202 for a request that was accepted, not for a
	// run that has already stopped: the jobs in flight are told to give
	// up and the run reaches "cancelled" a moment later. Saying "asked"
	// rather than "cancelled" is the difference.
	fmt.Printf("asked run %s to stop: the jobs in flight are being told to give up\n", run.ID)
	if run.CancelReason != "" {
		// Read back off the run rather than echoed from the flag: this is
		// what was actually recorded, and what everybody who meets this
		// run afterwards will be told.
		fmt.Printf("recorded on it: %s\n", run.CancelReason)
	}
}

// ciFollowing is the line -follow opens with: what is being followed, so
// that a run which prints nothing for its first minute -- a sandbox being
// built -- still says what the terminal is sitting on.
func (p *printer) ciFollowing(run ci.Run) {
	fmt.Printf("following run %s: %s at %s (^C to stop; the run carries on)\n",
		run.ID, run.Repo, ciRefDescriptionOf(run))
}

// ciFollowSummary is what -follow prints once the run is over: the run as
// `show` renders it, minus the step output -- which has already gone
// past, as it was written, above.
func (p *printer) ciFollowSummary(run ci.Run, mode cicli.OutputMode) {
	if p.json {
		// One document per run, not one per poll: see followCIRun.
		p.encode(run)
		return
	}
	fmt.Println()
	p.ciRun(run, cicli.OutputNone)
	if mode != cicli.OutputNone {
		fmt.Println()
		fmt.Printf("the output above is what those steps printed as they printed it; "+
			"\"grain ci show %s\" prints it again\n", run.ID)
	}
}

// ciRun prints one whole run: what it was, how it ended, and then its
// jobs failures-first with their steps under them.
func (p *printer) ciRun(run ci.Run, mode cicli.OutputMode) {
	if p.json {
		p.encode(run)
		return
	}
	now := time.Now()
	fmt.Printf("run %s  %s  %s\n", run.ID, ciConclusionWord(run.Conclusion), ciDuration(run.Duration(now)))
	fmt.Printf("repo:      %s\n", run.Repo)
	fmt.Printf("ref:       %s\n", ciRefDescriptionOf(run))
	if run.Event != "" {
		fmt.Printf("event:     %s\n", run.Event)
	}
	if run.RequestedBy != "" {
		fmt.Printf("asked by:  %s\n", run.RequestedBy)
	}
	if run.StartedAt != nil {
		fmt.Printf("started:   %s\n", run.StartedAt.Format(time.RFC3339))
	}
	if run.Build != nil {
		// Which build of grain produced this run, so that a run behaving
		// unlike the source in front of you can be attributed to a stale
		// deployment without bisecting the run history for the boundary
		// (ci.Build). The full revision rather than Describe's seven
		// characters: this is the detail view, and the full hash is what
		// goes into `git show` or `git merge-base --is-ancestor`.
		fmt.Printf("build:     %s\n", ciBuildDescription(*run.Build))
	}
	// The suite's own wait, which no single job's says: job-time rather
	// than wall clock, and larger than the run's duration whenever this
	// deployment had less room than the run had jobs. It is the figure
	// that answers "is this deployment short of sandboxes" (ci.Run.Queued).
	if queued := run.Queued(); queued > 0 {
		fmt.Printf("queued:    %s of job-time spent waiting for machines, across %d jobs\n",
			ciDuration(queued), len(run.Jobs))
	}
	if run.Error != "" {
		// A run-level error is why there are no jobs at all -- a repo
		// whose workflows could not be read, a `.grain` that does not
		// parse, a branch none of this repository's workflows answer to
		// -- so it is the whole answer rather than a footnote. The last
		// of those runs to several lines (ci's unmatchedReason); they are
		// indented under the label rather than run together, because what
		// it lists is one workflow per line.
		fmt.Printf("error:     %s\n", strings.ReplaceAll(run.Error, "\n", "\n           "))
	}
	if run.CancelReason != "" {
		// Why somebody stopped it, in their own words: the one line of
		// this report a person wrote for whoever reads the run next.
		fmt.Printf("cancelled: %s\n", run.CancelReason)
	}
	if run.Conclusion == ci.ConclusionCancelled {
		// Who stopped it, which the note above cannot say -- it is
		// optional, and most cancellations carry none -- and which is the
		// whole of what tells a timeout from a person's hand. Either way
		// the merge queue starts another run for this commit after a
		// short delay (orchestrator's ciReplacementFor). Worth a line of
		// its own on exactly the run somebody is looking at because their
		// task has been waiting on a verdict.
		who := "grain itself: a job past its timeout, a daemon that stopped under it, " +
			"or a newer run superseding it -- the merge queue starts another for this commit"
		if run.CancelledBy == ci.CancelByOperator {
			who = "a person, through grain's own API -- the merge queue starts another for " +
				"this commit after a short delay"
		}
		fmt.Printf("stopped:   %s\n", who)
	}
	if len(run.Jobs) == 0 {
		if run.Error == "" {
			fmt.Println("\nno jobs: this repository declares no workflows, so there was nothing to run")
		}
		return
	}
	// Failures first, which is what the reader came for, and the same
	// order the UI's pane puts them in -- pkg/ci owns that rule so the
	// two cannot drift.
	for _, job := range ci.SortJobRuns(run.Jobs) {
		fmt.Println()
		fmt.Printf("job %s  %s  %s  (%s)\n",
			cicli.JobName(job), ciJobStateWord(job), ciJobDuration(job, now), job.Workflow)
		if job.Cached {
			fmt.Println("  cached: this result was remembered rather than run")
		}
		// What the wait above was for, from whatever was holding the job:
		// the duration says a job was starved rather than slow, and this
		// says which scarce thing starved it -- the count, the budget, a
		// hole of its own shape, a capability grant, its own siblings.
		if job.QueuedReason != "" {
			// Said the way the daemon's own log says it, dash and all
			// (SharedGate.Acquire's "ci: this job is waiting -- %s"), so
			// that a line here and a line there are recognisably the
			// same sentence about the same wait.
			fmt.Printf("  queued: %s -- %s\n", ciDuration(job.Queued), job.QueuedReason)
		}
		if job.Sandbox != "" {
			fmt.Printf("  sandbox: %s\n", job.Sandbox)
		}
		if job.Error != "" {
			// Not a step's non-zero exit: a sandbox that could not be
			// built, a transport that stopped answering, a job timeout.
			fmt.Printf("  error: %s\n", job.Error)
		}
		for _, a := range job.Artifacts {
			// Above the steps, not below them: the whole reason this
			// exists is that the steps' output was not enough, and a
			// reader who has scrolled past a failing test suite's log has
			// already given up on finding a line about a trace file.
			if !a.Stored {
				fmt.Printf("  artifact %s: %s\n", a.Name, a.Note)
				continue
			}
			fmt.Printf("  artifact %s (%s): grain ci artifact %s %s %s\n",
				a.Name, ciCacheBytes(a.Bytes), a.Run, a.Job, a.Name)
		}
		for _, step := range job.Steps {
			fmt.Printf("  %-10s %s%s\n", ciConclusionWord(step.Conclusion), step.Name, ciExitNote(step))
			if step.Error != "" {
				fmt.Printf("    error: %s\n", step.Error)
			}
			if mode.Shows(step) {
				// Verbatim and unindented: this is a test suite's own
				// output, and somebody is about to copy a line of it into
				// a search or a bug report.
				fmt.Printf("    --- output of %q ---\n", step.Name)
				fmt.Println(strings.TrimRight(step.Output, "\n"))
				fmt.Printf("    --- end of %q ---\n", step.Name)
			}
		}
	}
	if mode == cicli.OutputFailed && ciHasUnshownOutput(run) {
		fmt.Println()
		fmt.Println("only the failing steps' output is shown; \"-output all\" prints every step's")
	}
}

// ciArtifacts prints what one run kept, for `grain ci artifact <run>`.
//
// The download command goes under each line rather than in a note at the
// end, because a reader who has found the artifact they want should not
// then have to assemble its command out of three columns.
func (p *printer) ciArtifacts(run string, artifacts []ci.Artifact, available bool) {
	if p.json {
		p.encode(artifacts)
		return
	}
	if !available {
		fmt.Println("this deployment keeps no CI artifacts (see the daemon log, and -ci-artifact-max-bytes)")
		return
	}
	if len(artifacts) == 0 {
		fmt.Printf("run %s has kept no artifacts: a job keeps files only when it does not succeed, "+
			"only what its `artifacts:` declares, and only until the sweep takes them\n", run)
		return
	}
	for _, a := range artifacts {
		fmt.Printf("%s  %s  %s\n", a.Job, a.Name, ciCacheBytes(a.Bytes))
		fmt.Printf("  grain ci artifact %s %s %s\n", a.Run, a.Job, a.Name)
	}
}

// ciArtifactSaved is the one line a finished download prints, and it
// names the file rather than the artifact: the next thing that happens
// is somebody untarring it.
func (p *printer) ciArtifactSaved(ref ci.ArtifactRef, path string, written int64) {
	if p.json {
		p.encode(struct {
			Run   string `json:"run"`
			Job   string `json:"job"`
			Name  string `json:"name"`
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		}{ref.Run, ref.Job, ref.Name, path, written})
		return
	}
	fmt.Printf("%s (%s) -- \"tar tzf %s\" lists it\n", path, ciCacheBytes(written), path)
}

// ciHasUnshownOutput reports whether a run holds output the default mode
// did not print -- what the note above is worth printing for.
func ciHasUnshownOutput(run ci.Run) bool {
	for _, job := range run.Jobs {
		for _, step := range job.Steps {
			if step.Output != "" && !cicli.OutputFailed.Shows(step) {
				return true
			}
		}
	}
	return false
}

// ciBuildDescription is a run's build stamp as `grain ci show` prints
// it: the whole revision, when that commit was made, and whether the
// tree it was built in was dirty.
//
// Not ci.Build.Describe, which shortens the revision for a line of
// prose. A reader here has one run in front of them and is about to ask
// git whether this build predates something, so they get the value git
// takes.
func ciBuildDescription(b ci.Build) string {
	out := b.Revision
	if b.CommittedAt != nil {
		out += "  (committed " + b.CommittedAt.Format(time.RFC3339) + ")"
	}
	if b.Modified {
		out += "  built from a modified tree"
	}
	return out
}

// ciRunLine is one run as a row of the listing: enough to pick the run
// somebody is looking for out of a screenful, and no more.
func ciRunLine(run ci.Run, now time.Time) string {
	repo := run.Repo
	if repo == "" {
		repo = "-"
	}
	return fmt.Sprintf("%-16s %-9s %-24s %-24s %-8s %8s  %s",
		run.ID, ciConclusionWord(run.Conclusion), repo, run.Ref, run.Event,
		ciDuration(run.Duration(now)), ciJobSummary(run))
}

// ciJobSummary is the one-line answer to "what happened in there": how
// many jobs, and how they ended when that is not simply all of them
// succeeding.
func ciJobSummary(run ci.Run) string {
	if len(run.Jobs) == 0 {
		return "no jobs"
	}
	counts := map[string]int{}
	for _, job := range run.Jobs {
		counts[ciJobStateWord(job)]++
	}
	if counts["success"] == len(run.Jobs) {
		return fmt.Sprintf("%d jobs, all green", len(run.Jobs))
	}
	// Worst first, so what is wrong is the first thing in the cell.
	var parts []string
	for _, word := range []string{"failure", "errored", "cancelled", "skipped", "success", "running", "waiting"} {
		if n := counts[word]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, word))
		}
	}
	return fmt.Sprintf("%d jobs: %s", len(run.Jobs), strings.Join(parts, ", "))
}

// ciConclusionWord is how a conclusion reads in a column. The pending
// zero value is the one that needs a word of its own: "" in a table is a
// gap, and what it means here is that this is still going.
func ciConclusionWord(c ci.Conclusion) string {
	if !c.Done() {
		return "running"
	}
	return string(c)
}

// ciJobStateWord is ciConclusionWord's own answer, except for a job that
// has not been given a sandbox yet -- one still queueing, one that lost
// a prerequisite, one whose `needs` failed -- which reads as "waiting"
// rather than "running". ci.JobRun.StartedAt is nil for exactly that job
// and for no other, which is the fact ciConclusionWord has no way to
// see: a job holding no sandbox is not running, whatever its conclusion
// has not yet become (grain/task-41 -- this is most of why a job stuck
// behind another sandbox for an hour and three quarters read as a hang
// rather than a queue, since `grain ci show` and `grain ci list` both
// called it "running -" the whole time).
func ciJobStateWord(job ci.JobRun) string {
	if !job.Conclusion.Done() && job.StartedAt == nil {
		return "waiting"
	}
	return ciConclusionWord(job.Conclusion)
}

func ciExitNote(step ci.StepRun) string {
	// -1 is "it never produced one" (the transport failed, or the step
	// was never run), and a skipped step never ran at all -- neither is
	// an exit status worth printing beside a name.
	if step.ExitCode <= 0 {
		return ""
	}
	return fmt.Sprintf("  (exit %d)", step.ExitCode)
}

// ciJobDuration is how long a job took, or how long it has been going,
// with the wait in front of it where there was one: "4m30s (+18m12s
// queued)" is a different thing from "22m42s", and only the first of
// them tells a reader to go and look at this deployment's capacity
// rather than at the job.
//
// A job that never started reports the wait alone, which is the whole of
// what happened to it -- a job cancelled after thirty-six minutes of
// queueing used to read as "-", indistinguishable from a job that was
// passed over instantly (ci.JobRun.Queued, grain/task-326).
func ciJobDuration(job ci.JobRun, now time.Time) string {
	queued := ""
	if job.Queued > 0 {
		queued = ciDuration(job.Queued) + " queued"
	}
	if job.StartedAt == nil {
		if queued == "" {
			return "-"
		}
		return queued
	}
	span := now.Sub(*job.StartedAt)
	if job.FinishedAt != nil {
		span = job.FinishedAt.Sub(*job.StartedAt)
	}
	if queued == "" {
		return ciDuration(span)
	}
	return ciDuration(span) + " (+" + queued + ")"
}

func ciDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return d.Round(time.Second).String()
}

// ciRefDescriptionOf is the branch and, where it is known, the commit it
// was at -- the pair that makes a result mean something afterwards.
func ciRefDescriptionOf(run ci.Run) string {
	ref := run.Ref
	if ref == "" {
		ref = "(no branch)"
	}
	if run.Commit == "" {
		return ref
	}
	commit := run.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return ref + " (" + commit + ")"
}

// ciFaults prints the active shared CI faults across branches, labelled by
// source (grain or github), or the route's JSON body under -json.
func (p *printer) ciFaults(faults []ui.CIFault) {
	if p.json {
		if faults == nil {
			faults = []ui.CIFault{}
		}
		p.encode(faults)
		return
	}
	if len(faults) == 0 {
		fmt.Println("no shared CI faults right now")
		return
	}
	for i, f := range faults {
		if i > 0 {
			fmt.Println()
		}
		src := f.Source
		if src == "" {
			src = ci.FaultSourceGrain
		}
		job := f.JobName
		if job == "" {
			job = f.JobID
		}
		step := ""
		if f.Step != "" {
			step = ", step " + f.Step
		}
		fmt.Printf("[%s] %s  job %s%s  (%d %s)\n",
			src, f.Repo, job, step, len(f.Refs), plural(len(f.Refs), "branch", "branches"))
		if len(f.Refs) > 0 {
			fmt.Printf("  branches: %s\n", strings.Join(f.Refs, ", "))
		}
		if len(f.Paths) > 0 {
			fmt.Printf("  files:    %s\n", strings.Join(f.Paths, ", "))
		}
		if f.OwnerTaskID != "" {
			if f.OwnerRef != "" {
				fmt.Printf("  owner:    task #%s (%s)\n", f.OwnerTaskID, f.OwnerRef)
			} else {
				fmt.Printf("  owner:    task #%s\n", f.OwnerTaskID)
			}
		}
		for _, fx := range f.Fixes {
			line := fmt.Sprintf("  fix:      %s", fx.PullRequest)
			if fx.Task != "" {
				line += " (task #" + fx.Task + ")"
			}
			if fx.Title != "" {
				line += " -- " + fx.Title
			}
			if len(fx.Files) > 0 {
				line += " [touches " + strings.Join(fx.Files, ", ") + "]"
			}
			fmt.Println(line)
		}
	}
}
