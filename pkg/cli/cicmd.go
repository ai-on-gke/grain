// cicmd.go is "grain ci": grain's own CI (pkg/ci, served as
// /api/ci/runs) read and driven from a terminal -- start a run against a
// repo and a branch, list what has run, open one run's jobs and steps,
// and cancel one that is still going.
//
// ci.go beside this file is the other half of the same feature and is
// deliberately not this: that is the daemon's own adapter, where the
// engine meets the machines a job runs on. This is a task verb like
// `grain metrics` or `grain pause` -- it holds no store and no engine,
// asks a running daemon over REST (pkg/ui's HTTPClient), and takes
// -server and -json from main.go's own global flags. So `grain ci list
// -json | jq` is how a run gets into anything else, and `grain -server
// https://... ci show <id>` is how a run on another deployment is read.
//
// Until this existed the API from grain/task-185 had no reader at all:
// watching a run meant curling the daemon and paging JSON. The two
// things a person actually wants out of one are what the shape here is
// built around -- which jobs failed, and what the step that failed
// printed. Hence: jobs failures-first (ci.SortJobRuns, so the pane and
// this agree on the order), and a step's output shown for the steps that
// failed, all of them under -output all, none of them under -output
// none.
//
// A run's output is already bounded where it is produced (ci.
// MaxStepOutput truncates one step's output in the middle past 256KB
// with a line saying so), so nothing here re-truncates it: a terminal
// has a scrollback and a pipe has a `less`, and a CLI that quietly
// dropped the tail of a failing test's output would be hiding the one
// thing it was opened for.
//
// The third thing a person wants is a run that has not finished yet, and
// that is `grain ci show -follow` / `grain ci watch <id>`: the same poll
// `grain ci run -watch` does, printing what each poll holds that the last
// did not (followCIRun and pkg/ci/cli's Follower), rather than one snapshot the
// reader has to keep asking for.
//
// The rendering of runs, listings and the cache is cicmd_print.go, and the
// polling and following behind -watch and -follow is pkg/ci/cli.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	cicli "github.com/ai-on-gke/grain/pkg/ci/cli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdCI(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	// A bare `grain ci` lists, the way a bare `grain pause` reads: the
	// first question anybody has here is "what has run", and making them
	// type a subcommand to ask it buys nothing. A leading flag still
	// belongs to the listing (`grain ci -repo acme/widgets`), so only a
	// non-flag first argument is taken as a subcommand.
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		return cmdCIList(ctx, c, out, args)
	case "show":
		return cmdCIShow(ctx, c, out, "show", false, args)
	case "watch":
		// The same command with -follow already on, because that is what
		// somebody with a run in flight wants and "watch" is the word
		// they reach for. `grain ci run -watch` spells it the same way.
		return cmdCIShow(ctx, c, out, "watch", true, args)
	case "run":
		return cmdCIRun(ctx, c, out, args)
	case "cancel":
		return cmdCICancel(ctx, c, out, args)
	case "cache":
		return cmdCICache(ctx, c, out, args)
	case "artifact", "artifacts":
		return cmdCIArtifact(ctx, c, out, args)
	case "faults", "fault":
		return cmdCIFaults(ctx, c, out, args)
	default:
		return fmt.Errorf("unknown \"grain ci\" subcommand %q: one of run, list, show, watch, cancel, cache, artifact, faults", sub)
	}
}

// cmdCIFaults lists the shared CI faults across branches right now, from
// both grain CI and GitHub CI (GET /api/ci/faults).
func cmdCIFaults(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci faults", flag.ContinueOnError)
	repo := fs.String("repo", "", "only this repository's shared faults, as owner/name")
	source := fs.String("source", "", "only faults from this CI source: grain, github, or all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	srcFilter := strings.ToLower(strings.TrimSpace(*source))
	switch srcFilter {
	case "", "all":
		srcFilter = ""
	case ci.FaultSourceGrain, ci.FaultSourceGitHub:
	default:
		return fmt.Errorf("unknown -source %q: use grain, github, or all", *source)
	}
	repoFilter := strings.TrimSpace(*repo)

	faults, err := c.CISharedFaults(ctx)
	if err != nil {
		return err
	}
	if repoFilter != "" || srcFilter != "" {
		filtered := make([]ui.CIFault, 0, len(faults))
		for _, f := range faults {
			fSrc := f.Source
			if fSrc == "" {
				fSrc = ci.FaultSourceGrain
			}
			if repoFilter != "" && !strings.EqualFold(f.Repo, repoFilter) {
				continue
			}
			if srcFilter != "" && fSrc != srcFilter {
				continue
			}
			filtered = append(filtered, f)
		}
		faults = filtered
	}
	out.ciFaults(faults)
	return nil
}

// cmdCICache is the job cache from a terminal: `grain ci cache` says
// what it holds and `grain ci cache clear` empties it.
//
// A bare `grain ci cache` reads rather than clears, which is the way
// round every other verb here works and the only safe default for a
// subcommand whose other half is destructive-looking. Clearing is in
// fact safe -- an entry is a promise that identical work was already
// done, never a fact only the cache holds -- but it is not free: the
// next run of every job that was cached does itself over.
func cmdCICache(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	action := "show"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("grain ci cache "+action, flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	switch action {
	case "show":
		view, err := c.CICache(ctx)
		if err != nil {
			return err
		}
		out.ciCache(view, false)
		return nil
	case "clear":
		view, err := c.ClearCICache(ctx)
		if err != nil {
			return err
		}
		out.ciCache(view, true)
		return nil
	default:
		return fmt.Errorf("unknown \"grain ci cache\" action %q: one of show, clear", action)
	}
}

// cmdCIArtifact is the files a failing job left behind, from a terminal:
// `grain ci artifact <run>` lists what a run kept, and `grain ci
// artifact <run> <job> <name>` downloads one.
//
// The download is a file rather than something printed, which is why -o
// defaults to the name the server gives it (ci.ArtifactRef.Filename)
// rather than to stdout: every artifact is a gzipped tar, and a terminal
// full of binary is nobody's intention. `-o -` is there for the pipeline
// somebody will want (`grain ci artifact r j trace -o - | tar tz`).
func cmdCIArtifact(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci artifact", flag.ContinueOnError)
	to := fs.String("o", "",
		"where to write it: a path, or - for standard output (default: the artifact's own file name, in the working directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 3); err != nil {
		return err
	}
	run := strings.TrimSpace(fs.Arg(0))
	if run == "" {
		return fmt.Errorf("a run id is required: %q, or %q to download one (\"grain ci list\" prints run ids)",
			"grain ci artifact <run>", "grain ci artifact <run> <job> <name>")
	}
	job, name := strings.TrimSpace(fs.Arg(1)), strings.TrimSpace(fs.Arg(2))
	if job == "" || name == "" {
		if job != "" || name != "" {
			return fmt.Errorf("a download needs both a job and an artifact name: %q", "grain ci artifact <run> <job> <name>")
		}
		artifacts, available, err := c.CIRunArtifacts(ctx, run)
		if err != nil {
			return err
		}
		out.ciArtifacts(run, artifacts, available)
		return nil
	}

	ref := ci.ArtifactRef{Run: run, Job: job, Name: name}
	if *to == "-" {
		_, err := c.DownloadCIArtifact(ctx, ref, os.Stdout)
		return err
	}
	path := strings.TrimSpace(*to)
	if path == "" {
		path = ref.Filename()
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	written, err := c.DownloadCIArtifact(ctx, ref, file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// The half-written file goes: a truncated .tar.gz that looks like
		// a download is worse than no download, and the command has
		// already said what went wrong.
		os.Remove(path)
		return err
	}
	out.ciArtifactSaved(ref, path, written)
	return nil
}

func cmdCIList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci list", flag.ContinueOnError)
	filterFlags := ciListFilterFlags(fs)
	limit := fs.Int("limit", 20,
		"how many of the newest runs to show; 0 for as many as the daemon answers one listing with")
	compact := fs.Bool("compact", false,
		"each run as its id, ref, commit and conclusion and a line per job that did not succeed "+
			"(the route's view=compact; with -json, its body)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	filter, err := filterFlags.filter(time.Now())
	if err != nil {
		return err
	}
	// The filter and the limit go to the daemon rather than being applied
	// to what it answered with: it asks its run store, so -repo reaches
	// every run this deployment keeps rather than the newest page of
	// them (ci.RunStore.Find, grain/task-289), and -limit is how much of
	// the answer crosses the wire rather than how much of it is printed.
	if *compact {
		page, err := c.FindCIRunsCompact(ctx, filter, *limit)
		if err != nil {
			return err
		}
		out.ciCompactRuns(page, filter)
		return nil
	}
	page, err := c.FindCIRuns(ctx, filter, *limit)
	if err != nil {
		return err
	}
	out.ciRuns(page, filter)
	return nil
}

// ciListFlagValues are `grain ci list`'s narrowing flags, held between
// being registered on the flag set and being read into one ci.RunFilter.
type ciListFlagValues struct {
	repo, ref, job, since, until, sort, order *string
	conclusions                               stringList
}

// ciListFilterFlags puts the whole run-filter vocabulary on `grain ci
// list` -- the same words the other three readers of ci.RunFilter narrow
// by: list_ci_runs' arguments (mcp.ciRunFilter), GET /api/ci/runs' query
// (ui.ciRunFilterFrom) and the CI pane's own row of controls
// (ui/src/ciFilters.js).
//
// One vocabulary, spelled as flags. A terminal is the fourth place the
// same question gets asked, and "failed runs on main" has to mean one
// set of runs wherever it is asked or none of the four answers can be
// checked against the others (grain/task-231). So -conclusion takes
// ci.RunStates' words rather than a word of its own, and -since takes
// both spellings ci.ParseSince knows.
func ciListFilterFlags(fs *flag.FlagSet) *ciListFlagValues {
	f := &ciListFlagValues{}
	f.repo = fs.String("repo", "", "only this repository's runs, as owner/name")
	f.ref = fs.String("ref", "",
		"only branches whose name contains this (\"task-231\" finds \"grain/task-231\")")
	fs.Var(&f.conclusions, "conclusion", "only runs that stand this way: one of "+
		strings.Join(ci.RunStates, ", ")+" (repeatable, or comma-separated, for several)")
	f.job = fs.String("job", "",
		"only runs that have this job, by name or id -- whatever the job did")
	f.since = fs.String("since", "",
		"only runs started since then: a span back from now (24h, 7d, 90m) or a date (2026-09-17, or an RFC 3339 timestamp)")
	f.until = fs.String("until", "",
		"only runs started before then, spelled as -since is; the two together are a window")
	f.sort = fs.String("sort", "", fmt.Sprintf("order by %s (default %s)", ciRunSortWords(), ci.SortRunsByStarted))
	f.order = fs.String("order", "desc", "which way that order points: desc (newest, or longest, first) or asc")
	return f
}

// filter reads those flags into the filter the daemon answers.
//
// Every word it does not recognise is refused here, by the command the
// operator ran, rather than sent on to be answered with everything. A
// filter that quietly dropped "-conclusion failed" would list every run
// in the deployment, and a list of green runs reads as a fact about CI
// rather than as a typo -- so ci.Parse* does the reading, and says which
// words it does know.
func (f *ciListFlagValues) filter(now time.Time) (ci.RunFilter, error) {
	filter := ci.RunFilter{
		Repo:       strings.TrimSpace(*f.repo),
		Ref:        strings.TrimSpace(*f.ref),
		Job:        strings.TrimSpace(*f.job),
		Descending: true,
	}
	for _, value := range f.conclusions {
		// Comma-separated as well as repeatable, because both are how a
		// person writes two of something at a shell -- and because the
		// route this travels over takes both (ui.ciRunFilterFrom).
		for _, word := range strings.Split(value, ",") {
			if strings.TrimSpace(word) == "" {
				continue
			}
			state, err := ci.ParseRunState(strings.TrimSpace(word))
			if err != nil {
				return ci.RunFilter{}, err
			}
			filter.States = append(filter.States, state)
		}
	}
	if since := strings.TrimSpace(*f.since); since != "" {
		// Resolved here rather than sent on as "24h": the wire carries an
		// instant (ui.HTTPClient.FindCIRuns), so the window is the one
		// this command's own clock measured.
		at, err := ci.ParseSince(since, now)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Since = at
	}
	if until := strings.TrimSpace(*f.until); until != "" {
		at, err := ci.ParseSince(until, now)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Until = at
	}
	if word := strings.TrimSpace(*f.sort); word != "" {
		key, err := ci.ParseRunSortKey(word)
		if err != nil {
			return ci.RunFilter{}, err
		}
		filter.Sort = key
	}
	switch strings.ToLower(strings.TrimSpace(*f.order)) {
	case "", "desc":
	case "asc":
		filter.Descending = false
	default:
		return ci.RunFilter{}, fmt.Errorf("%q is not an order: use asc or desc", *f.order)
	}
	return filter, nil
}

// ciRunSortWords is the sort vocabulary for a usage line.
func ciRunSortWords() string {
	words := make([]string, 0, len(ci.RunSortKeys))
	for _, key := range ci.RunSortKeys {
		words = append(words, string(key))
	}
	return strings.Join(words, ", ")
}

// cmdCIShow is both `grain ci show <id>` and `grain ci watch <id>`: one
// snapshot of a run, or the same run followed until it is over. name is
// which of the two the operator typed (so a usage error names their own
// command) and defFollow is what -follow defaults to under it.
func cmdCIShow(ctx context.Context, c *ui.HTTPClient, out *printer, name string, defFollow bool, args []string) error {
	fs := flag.NewFlagSet("grain ci "+name, flag.ContinueOnError)
	output := fs.String("output", "failed",
		"whose output to print: failed (the steps that failed, and the one running), all, or none")
	follow := fs.Bool("follow", defFollow,
		"keep printing the run's output as it arrives, until it is over -- then print the summary "+
			"and exit non-zero if it did not succeed (with -json: wait, then print the finished run once)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, err := cicli.ParseOutputMode(*output)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		return fmt.Errorf("a run id is required: %q (\"grain ci list\" prints them)",
			"grain ci "+name+" <id>")
	}
	if !*follow {
		run, err := c.CIRun(ctx, id)
		if err != nil {
			return err
		}
		out.ciRun(run, mode)
		return nil
	}
	run, err := followCIRun(ctx, c, out, id, mode)
	if err != nil {
		return err
	}
	out.ciFollowSummary(run, mode)
	return cicli.RunFailure(run)
}

func cmdCIRun(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci run", flag.ContinueOnError)
	repo := fs.String("repo", "", "the repository to test, as owner/name (required)")
	branch := fs.String("branch", "", "the branch to test (required)")
	commit := fs.String("commit", "",
		"the SHA that branch is at, if it is known: what makes the result mean something afterwards, and what a cache is keyed by")
	event := fs.String("event", "",
		"which event to run as: push, pull_request or workflow_dispatch (default: a manual dispatch without release secrets; name workflow_dispatch to get them)")
	var recheck bool
	fs.BoolVar(&recheck, "recheck", false,
		"bypass the CI result cache and re-run all matching jobs")
	fs.BoolVar(&recheck, "no-cache", false,
		"bypass the CI result cache and re-run all matching jobs (alias for -recheck)")
	var jobs stringList
	fs.Var(&jobs, "job", "run one job id (repeatable; use workflow-path:job-id when ambiguous)")
	watch := fs.Bool("watch", false,
		"wait for the run to finish, then print it -- and exit non-zero if it did not succeed")
	output := fs.String("output", "failed",
		"with -watch, whose output to print when it is over: failed, all, or none")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, err := cicli.ParseOutputMode(*output)
	if err != nil {
		return err
	}
	// The repository and the branch are flags here, so a bare word is
	// one of them written without its flag.
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	if strings.TrimSpace(*repo) == "" {
		return errors.New("-repo is required: which repository to run CI over, as owner/name")
	}
	if strings.TrimSpace(*branch) == "" {
		return errors.New("-branch is required: which branch of that repository to test")
	}
	run, err := c.StartCIRun(ctx, ui.CIRunRequest{
		Repo:    strings.TrimSpace(*repo),
		Ref:     strings.TrimSpace(*branch),
		Commit:  strings.TrimSpace(*commit),
		Event:   strings.TrimSpace(*event),
		Jobs:    []string(jobs),
		Recheck: recheck,
	})
	if err != nil {
		return err
	}
	if !*watch {
		// Not the whole run view: nothing has happened yet, and the two
		// things worth having are the id to ask about it with and the
		// jobs it is about to run.
		out.ciStarted(run)
		return nil
	}
	run, err = cicli.Watch(ctx, c, run.ID)
	if err != nil {
		return err
	}
	out.ciRun(run, mode)
	return cicli.RunFailure(run)
}

func cmdCICancel(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci cancel", flag.ContinueOnError)
	// Worth typing: the run is stopped either way, and the note is the
	// only thing that tells whoever meets the cancelled run afterwards --
	// a person, or the agent whose branch it was judging -- why it was
	// stopped rather than leaving them to read a bare "cancelled".
	reason := fs.String("reason", "", "an optional note on why this run is being stopped")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		return errors.New("a run id is required: \"grain ci cancel <id>\"")
	}
	run, err := c.CancelCIRun(ctx, id, *reason)
	if err != nil {
		return err
	}
	out.ciCancelled(run)
	return nil
}

// followCIRun polls one run at cicli.WatchInterval and prints whatever each
// poll holds that the one before it did not, until the run is over.
//
// grain CI publishes a step's output while the step is still printing it
// (ci.LiveOutputInterval), so every poll of a live run carries a longer
// version of the log than the last -- but `grain ci show` only ever
// printed one of them. A reader watching a twenty-minute job ran it
// again, and again, and read the same head of the same log each time.
// This is the other half: the run is polled at the cadence it is
// published at, and only the bytes nobody has been shown yet are written
// out (cicli.Follower).
//
// Under -json nothing is streamed. A follow that emitted a document per
// poll would be a stream of half-written runs rather than a run, and
// `grain ci watch <id> -json | jq` would choke on the second one; so
// -json waits and then prints the finished run once, which is exactly
// what a plain `grain ci show -json` of the same run prints afterwards.
func followCIRun(ctx context.Context, c *ui.HTTPClient, out *printer, id string, mode cicli.OutputMode) (ci.Run, error) {
	if out.json {
		return cicli.Watch(ctx, c, id)
	}
	f := cicli.NewFollower(os.Stdout, mode)
	started := false
	return cicli.Poll(ctx, c, id, func(run ci.Run) {
		if !started {
			started = true
			out.ciFollowing(run)
		}
		f.Follow(run)
	})
}
