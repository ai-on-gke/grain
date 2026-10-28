package mcp

import (
	"context"
	"fmt"
	"strings"
)

// The bounds write_release_notes holds a call to, in bytes -- the same
// limits the daemon enforces on the write (model.MaxReleaseNoteLength,
// model.MaxReleaseNoteTitleLength), stated here so a call that is too
// long is refused where the agent can still cut it down in the same turn
// rather than after a round trip.
//
// Far looser than update_memory's next door, and the difference is the
// whole distinction between the two: a memory is repeated in every
// prompt this deployment issues afterwards, and a week's notes are read
// once, by a person, when they choose to.
const (
	MaxReleaseNoteLength      = 64 * 1024
	MaxReleaseNoteTitleLength = 200
)

// ClusterStats is how much work a deployment did over one week, as the
// tool reports it: grain's own count, not an estimate, and the figures
// the week's notes are meant to be built around.
//
// Period is the ISO week ("2026-W38") and Since/Until are the instants
// it covers, already formatted by the side of the hop that has the
// deployment's clock (cmd/grain's daemonReleaseNotes) -- this package
// deliberately does not import pkg/model, and a second opinion about
// what a week is would be one more thing to keep saying the same.
//
// AgentHours rather than a duration: it is read out loud in a sentence
// of prose, and "31.5 hours" is what that sentence wants.
//
// LinesAdded/LinesDeleted are how much code the week's merges changed,
// and PullRequestsMeasured is how many of PullRequestsMerged that is
// summed over. The pair travels together on purpose: grain has a diff's
// size on file only for merges it observed after it started recording
// one, so a week whose measured count is short of its merged count has
// a lines total that is partial, and the answer says so rather than
// letting a run quote it as complete (renderClusterStats).
type ClusterStats struct {
	Period               string
	Since                string
	Until                string
	PullRequestsMerged   int
	PullRequestsMeasured int
	LinesAdded           int
	LinesDeleted         int
	TasksCompleted       int
	TasksFiled           int
	Runs                 int
	RunsSucceeded        int
	AgentHours           float64
	Repos                []RepoStats
}

// RepoStats is one repository's share of that week, most merged pull
// requests first -- which is what lets the notes say where the week
// actually went rather than reporting a cluster-wide total nobody can
// place.
type RepoStats struct {
	Repo                 string
	PullRequestsMerged   int
	PullRequestsMeasured int
	LinesAdded           int
	LinesDeleted         int
	TasksCompleted       int
	TasksFiled           int
	Runs                 int
}

// ReleaseNotesStore is this deployment's account of itself, as the run
// holding the "release-notes" capability may touch it: the numbers
// (ClusterStats) and the prose written around them (WriteReleaseNotes).
//
// An interface, like MemoryStore next door and for the same reason: the
// real implementation (pkg/cli/mcpserver.go's daemonReleaseNotes) asks
// the daemon over its REST API, because the counting and the row being
// written both live there and a run holds no credential of its own.
//
// period is an ISO week throughout, and "" is not a missing argument: it
// means the week that just ended, which is what a job firing on a Monday
// morning is there to write about (model.LastCompleteReleaseNotePeriod).
// Both calls resolve it the same way, so the numbers a run is given and
// the week it files are the same week without the run having to work out
// what today's date makes of that.
type ReleaseNotesStore interface {
	ClusterStats(ctx context.Context, period string) (ClusterStats, error)
	WriteReleaseNotes(ctx context.Context, period, title, text string) (string, error)
}

// NewReleaseNotesTools returns the two tools a run holding the
// "release-notes" capability gets: read_cluster_stats and
// write_release_notes.
//
// They are what makes a week's release notes (model.ReleaseNote)
// something an agent writes rather than something an operator would have
// to keep by hand. The weekly job reads this deployment's own record --
// what merged, what took four attempts, what is still red -- and
// publishes the week as markdown, which the UI shows and the next export
// writes into the state repository as `release-notes/<week>.md`.
//
// Two tools and not one, unlike update_memory's single call: the two
// halves are different acts by different authorities. The numbers are
// grain's, computed from its own rows and not open to an opinion; the
// prose is the run's. Folding them together would make the count look
// like something the run had a say in, which is exactly the impression
// this pair exists to avoid -- a reader of a week's notes is entitled to
// know that the figures in it were not written by the thing that wrote
// the sentences.
//
// store nil returns the tools anyway, refusing every call -- what lets
// each agent framework's allowedTools enumerate the names this package
// registers without holding a live store, exactly as NewMemoryTools and
// NewStatusTools do.
func NewReleaseNotesTools(store ReleaseNotesStore) []Tool {
	return []Tool{readClusterStatsTool(store), writeReleaseNotesTool(store)}
}

func readClusterStatsTool(store ReleaseNotesStore) Tool {
	return Tool{
		Name: "read_cluster_stats",
		Description: "How much work this deployment did over one week, counted from grain's own " +
			"record: pull requests merged, tasks completed and filed, agent runs and how many " +
			"of them succeeded, how long those runs spent running, how many lines those " +
			"merges added and removed, and the same counts for each repository the week " +
			"touched. Call it with no arguments for the week that just ended, which is the " +
			"week write_release_notes files under by default. These are counts, not " +
			"estimates -- quote them as they are. The lines-changed figure is GitHub's own " +
			"count of each pull request's diff, recorded when grain saw it merge; where it " +
			"covers only some of the week's merges the answer says so, and it must then be " +
			"quoted as covering that many rather than as the week's total.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"period": map[string]any{
					"type": "string",
					"description": "The ISO week to count, as \"2026-W38\". Omit it for the " +
						"week that just ended.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if store == nil {
				return noReleaseNotesRoute()
			}
			period, _ := argString(args, "period")
			stats, err := store.ClusterStats(ctx, strings.TrimSpace(period))
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: renderClusterStats(stats)}
		},
	}
}

func writeReleaseNotesTool(store ReleaseNotesStore) Tool {
	return Tool{
		Name: "write_release_notes",
		Description: "Publish one week's release notes: this deployment's account of what it " +
			"shipped, shown in grain's UI and written into the state repository as " +
			"release-notes/<week>.md. Omit \"period\" to file the week that just ended -- the " +
			"same week read_cluster_stats answers for. \"title\" is the headline a person " +
			"reads in a list of weeks, so it says what the week was about rather than that it " +
			"is a week. \"text\" is markdown, and writing the same week again replaces it " +
			"whole, so a correction is another call rather than an addition; an empty text " +
			"withdraws that week entirely. Write what a person would want to know -- what " +
			"landed, in which repositories, what took several goes, what is still open or " +
			"still failing -- around the numbers read_cluster_stats gives you. Not every " +
			"task, which the record already has, and nothing you have not checked.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"period": map[string]any{
					"type": "string",
					"description": "The ISO week these notes are for, as \"2026-W38\". Omit it " +
						"for the week that just ended.",
				},
				"title": map[string]any{
					"type": "string",
					"description": "The headline for the week, at most " +
						fmt.Sprint(MaxReleaseNoteTitleLength) + " characters.",
				},
				"text": map[string]any{
					"type": "string",
					"description": "The notes themselves, as markdown -- the whole of that " +
						"week's, replacing whatever was published for it before. Empty " +
						"withdraws the week.",
				},
			},
			"required": []string{"text"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if store == nil {
				return noReleaseNotesRoute()
			}
			period, _ := argString(args, "period")
			title, _ := argString(args, "title")
			text, _ := argString(args, "text")
			period, title, text = strings.TrimSpace(period), strings.TrimSpace(title), strings.TrimSpace(text)
			switch {
			case len(text) > MaxReleaseNoteLength:
				return Result{
					Text: fmt.Sprintf("Those notes are %d characters; keep them under %d. Past "+
						"that a week's notes have stopped being notes and become the task list "+
						"they were written from, which grain already has.",
						len(text), MaxReleaseNoteLength),
					IsError: true,
				}
			case len(title) > MaxReleaseNoteTitleLength:
				return Result{
					Text: fmt.Sprintf("That title is %d characters; keep it under %d. It is the "+
						"one line shown in a list of weeks, so it is a headline rather than a "+
						"summary.", len(title), MaxReleaseNoteTitleLength),
					IsError: true,
				}
			case text != "" && title == "":
				return Result{
					Text: "These notes need a \"title\": the pane lists weeks by their headline, " +
						"and a week nobody can tell the subject of is one nobody opens. Say in " +
						"one line what the week was about.",
					IsError: true,
				}
			}
			written, err := store.WriteReleaseNotes(ctx, period, title, text)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if text == "" {
				return Result{Text: "Withdrew the release notes for " + written +
					"; that week is no longer published."}
			}
			return Result{Text: fmt.Sprintf(
				"Published the release notes for %s (%d characters). They are in grain's UI now, "+
					"and in the state repository as %s/%s.md after the next export.",
				written, len(text), releaseNotesDir, written)}
		},
	}
}

// releaseNotesDir is where the export puts a week's notes in the state
// repository (staterepo.ReleaseNotesDir), repeated here as a word in a
// sentence rather than imported: this package does not import
// pkg/staterepo any more than it imports pkg/model, and what it is
// saying is where to go and look.
const releaseNotesDir = "release-notes"

// renderClusterStats is the answer a model actually reads: a handful of
// totals, then a line per repository.
//
// Prose-shaped rather than JSON, like every other answer this package
// gives, and with the numbers laid out one per line so that quoting one
// correctly is easier than quoting it wrongly. The repository lines
// arrive already sorted, most merged first, and are left in that order:
// it is the order the week is worth describing in.
func renderClusterStats(s ClusterStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s to %s)\n\n", s.Period, s.Since, s.Until)
	fmt.Fprintf(&b, "Pull requests merged: %d\n", s.PullRequestsMerged)
	if line := linesChangedLine(s); line != "" {
		b.WriteString(line)
	}
	fmt.Fprintf(&b, "Tasks completed: %d\n", s.TasksCompleted)
	fmt.Fprintf(&b, "Tasks filed: %d\n", s.TasksFiled)
	fmt.Fprintf(&b, "Agent runs: %d (%d succeeded)\n", s.Runs, s.RunsSucceeded)
	fmt.Fprintf(&b, "Agent time: %.1f hours\n", s.AgentHours)
	if len(s.Repos) == 0 {
		b.WriteString("\nNo repository saw any work in this week.\n")
		return b.String()
	}
	b.WriteString("\nBy repository, most merged first:\n")
	for _, r := range s.Repos {
		fmt.Fprintf(&b, "- %s: %d merged%s, %d tasks completed, %d filed, %d runs\n",
			r.Repo, r.PullRequestsMerged, repoLinesChanged(r),
			r.TasksCompleted, r.TasksFiled, r.Runs)
	}
	return b.String()
}

// linesChangedLine is the week's lines-changed figure, said in whichever
// of three ways is true of this window -- or not at all, for a week that
// merged nothing, where "Pull requests merged: 0" has already said it.
//
// The three are the whole reason this is a function rather than one more
// Fprintf. Grain records a diff's size at the moment it observes a merge
// (model.Observation.PrAdditions) and has done so only since September
// 2026, so a window can contain merges it has no size for -- and a sum
// over the rest, quoted as the week's total, would be wrong in the
// direction nobody checks. So a partial window says how much of itself
// the figure covers, and a window with nothing measured at all says
// there is no figure rather than reporting a confident zero.
func linesChangedLine(s ClusterStats) string {
	if s.PullRequestsMerged == 0 {
		return ""
	}
	if s.PullRequestsMeasured == 0 {
		return fmt.Sprintf("Lines changed: not recorded for any of this week's %d merges, so "+
			"there is no lines-changed figure to report for it\n", s.PullRequestsMerged)
	}
	if s.PullRequestsMeasured < s.PullRequestsMerged {
		return fmt.Sprintf("Lines changed: +%d -%d, over %d of the week's %d merges -- grain "+
			"has no diff size on file for the other %d, so this is a partial figure: quote "+
			"it as covering %d of the %d merges rather than as the week's total\n",
			s.LinesAdded, s.LinesDeleted, s.PullRequestsMeasured, s.PullRequestsMerged,
			s.PullRequestsMerged-s.PullRequestsMeasured,
			s.PullRequestsMeasured, s.PullRequestsMerged)
	}
	return fmt.Sprintf("Lines changed: +%d -%d across all %d merges\n",
		s.LinesAdded, s.LinesDeleted, s.PullRequestsMerged)
}

// repoLinesChanged is linesChangedLine's clause for one repository's
// row: the same three cases, in the space a list item has for them.
func repoLinesChanged(r RepoStats) string {
	if r.PullRequestsMerged == 0 || r.PullRequestsMeasured == 0 {
		return ""
	}
	if r.PullRequestsMeasured < r.PullRequestsMerged {
		return fmt.Sprintf(" (+%d -%d over %d of them)",
			r.LinesAdded, r.LinesDeleted, r.PullRequestsMeasured)
	}
	return fmt.Sprintf(" (+%d -%d)", r.LinesAdded, r.LinesDeleted)
}

// noReleaseNotesRoute is what both tools answer when this process has no
// daemon to ask -- the same shape noMemoryRoute takes, and for the same
// reason: the counting and the write both happen in the daemon's store,
// so a run that cannot reach it has nothing to fall back on and is owed
// the reason rather than a failure it might retry.
func noReleaseNotesRoute() Result {
	return Result{
		Text: "This run has no route back to the grain daemon, so it can neither count this " +
			"deployment's work nor publish notes about it. Say so in your closing note and " +
			"change nothing.",
		IsError: true,
	}
}
