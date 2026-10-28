package metrics_test

// What a run was given before its first turn: the distribution over a
// window, the two counts the bound does not censor, and which tasks the
// heavy prompts belong to.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// runOf is one finished attempt of a named task -- prompts carry no task
// and no moment of their own, so both come off the run they belong to.
func runOf(runID, taskID string, attempt int, d time.Duration) model.RunTiming {
	return model.RunTiming{
		RunID: runID, TaskID: taskID, Attempt: attempt,
		StartedAt: since.Add(d - time.Hour), FinishedAt: at(d),
		Outcome: "succeeded",
	}
}

// warnBytes is a prompt just over the line the daemon warns at, in the
// units the store records: bytes.
const warnBytes = model.PromptWarnTokens*model.PromptBytesPerToken + 1

func TestPromptsSummariseWhatTheWindowsRunsWereHanded(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs: []model.RunTiming{
			runOf("r1", "a1b2", 1, 10*time.Hour),
			runOf("r2", "a1b2", 2, 20*time.Hour),
			runOf("r3", "b2c3", 1, 30*time.Hour),
			// Finished after the window: its prompt is in the store and
			// out of the report, the rule every other measurement follows.
			runOf("r4", "c3d4", 1, 9*24*time.Hour),
		},
		Prompts: []model.RunPrompt{
			{RunID: "r1", Bytes: 4000},
			{RunID: "r2", Bytes: warnBytes, ThreadCut: true},
			{RunID: "r3", Bytes: 9000},
			{RunID: "r4", Bytes: 5 << 20, ThreadCut: true},
			// A prompt whose run this report has never heard of: a row
			// for a run outside the input, which contributes nothing
			// rather than a sample with no moment.
			{RunID: "r9", Bytes: 1 << 20},
		},
	})

	p := rep.Prompts
	if p.Runs != 3 {
		t.Errorf("Prompts.Runs = %d, want 3 -- only the runs that finished in the window", p.Runs)
	}
	if p.Bytes.N != 3 {
		t.Errorf("Bytes.N = %d, want 3", p.Bytes.N)
	}
	if got, want := p.Bytes.Max, int64(warnBytes); got != want {
		t.Errorf("Bytes.Max = %d, want %d -- the largest prompt exactly, not a bucket bound", got, want)
	}
	if got, want := p.Bytes.Mean, int64((4000+warnBytes+9000)/3); got != want {
		t.Errorf("Bytes.Mean = %d, want %d", got, want)
	}
	// A bound rather than a value: 9000 bytes falls in the [8192, 16383]
	// octave, and the bound is what a report may claim (Sizes).
	if got := p.Bytes.P50AtMost; got != 16383 {
		t.Errorf("Bytes.P50AtMost = %d, want 16383 -- the bound of the bucket the median fell in", got)
	}
	if p.Large != 1 {
		t.Errorf("Large = %d, want 1 -- the one prompt past the warning line", p.Large)
	}
	if got, want := p.LargeShare, 1.0/3.0; !closeEnough(got, want) {
		t.Errorf("LargeShare = %v, want %v", got, want)
	}
	if p.ThreadCut != 1 {
		t.Errorf("ThreadCut = %d, want 1 -- r4's cut is outside the window", p.ThreadCut)
	}
	if got, want := p.ThreadCutShare, 1.0/3.0; !closeEnough(got, want) {
		t.Errorf("ThreadCutShare = %v, want %v", got, want)
	}
}

// The distribution says prompts are growing; Heaviest is the part that
// says whose, and is measured on a task's largest attempt rather than
// its mean -- a task's prompt grows with its thread, so its last attempt
// is normally its biggest.
func TestPromptsNameTheHeaviestTasksLargestFirst(t *testing.T) {
	var runs []model.RunTiming
	var prompts []model.RunPrompt
	// Seven tasks, each dispatched once, of increasing prompt size.
	for i := 1; i <= 7; i++ {
		id := string(rune('a' + i - 1))
		runs = append(runs, runOf("r"+id, "task-"+id, 1, time.Duration(i)*time.Hour))
		prompts = append(prompts, model.RunPrompt{RunID: "r" + id, Bytes: i * 1000})
	}
	// ...and a second attempt of the smallest one, with much the largest
	// prompt of the lot and a thread cut to reach it.
	runs = append(runs, runOf("ra2", "task-a", 2, 8*time.Hour))
	prompts = append(prompts, model.RunPrompt{RunID: "ra2", Bytes: 90000, ThreadCut: true})

	rep := metrics.Compute(metrics.Input{Window: win, Runs: runs, Prompts: prompts})

	heaviest := rep.Prompts.Heaviest
	if len(heaviest) != 5 {
		t.Fatalf("Heaviest = %+v, want the five heaviest tasks", heaviest)
	}
	top := heaviest[0]
	if top.TaskID != "task-a" || top.MaxBytes != 90000 {
		t.Errorf("heaviest = %+v, want task-a at 90000 bytes -- its second attempt, not its mean", top)
	}
	if top.Runs != 2 || top.Attempt != 2 || top.ThreadCut != 1 {
		t.Errorf("heaviest = %+v, want two attempts, the largest being attempt 2, with one cut", top)
	}
	if got, want := top.MaxTokens, 90000/model.PromptBytesPerToken; got != want {
		t.Errorf("heaviest MaxTokens = %d, want %d", got, want)
	}
	var ids []string
	for _, task := range heaviest {
		ids = append(ids, task.TaskID)
	}
	// Largest first, and the two smallest tasks left off entirely.
	for i := 1; i < len(heaviest); i++ {
		if heaviest[i-1].MaxBytes < heaviest[i].MaxBytes {
			t.Fatalf("Heaviest = %v, want largest first", ids)
		}
	}
	if ids[len(ids)-1] != "task-d" {
		t.Errorf("Heaviest = %v, want the five largest and no more", ids)
	}
}

// A window whose runs recorded no prompt reports nothing rather than a
// deployment that hands its runs empty prompts: the same "what was never
// measured is not a zero" rule the tool census follows.
func TestPromptsAreEmptyWhenNothingRecordedOne(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs:   []model.RunTiming{runOf("r1", "a1b2", 1, 10*time.Hour)},
	})
	if got := rep.Prompts; got.Runs != 0 || got.Bytes.N != 0 || len(got.Heaviest) != 0 {
		t.Errorf("Prompts = %+v, want nothing measured", got)
	}
}

// A window that straddles the moment prompts began being recorded must
// not dilute its own rates with the runs from before it.
//
// This is the shape of mistake a census of this kind is read wrong by.
// Prompts have only been recorded since grain/task-268 landed (18 Sep
// 2026), and the conversation bound arrived with them, so a window
// reaching back past that date holds finished runs that could not have
// carried a prompt and could not have had a thread cut out of one. If
// Runs counted the window's finished runs rather than its recorded
// prompts, every such run would sit in the denominator of LargeShare and
// ThreadCutShare while being unable to reach either numerator -- and the
// rate that came out would say "this deployment hardly ever cuts a
// thread" about a deployment that cuts most of them, purely as an
// artefact of where the window was started.
//
// promptsOf gets this right by counting inside the loop over prompts
// rather than the loop over runs, which is a one-line property with no
// second line anywhere saying it was meant. It is asserted here because
// the obvious future edit -- deriving Runs from the run timings, which
// are right there and already filtered to the window -- would break it
// silently, leaving a report that is wrong only on the windows anybody
// would actually ask for.
func TestPromptsRateThemselvesOverRecordedPromptsNotOverEveryRun(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs: []model.RunTiming{
			// Four runs from before prompts were recorded: in the window,
			// finished inside it, and carrying no prompt row at all.
			runOf("old1", "a1b2", 1, 2*time.Hour),
			runOf("old2", "a1b2", 2, 3*time.Hour),
			runOf("old3", "b2c3", 1, 4*time.Hour),
			runOf("old4", "b2c3", 2, 5*time.Hour),
			// And two from after, one of which had its thread cut.
			runOf("new1", "c3d4", 1, 30*time.Hour),
			runOf("new2", "c3d4", 2, 31*time.Hour),
		},
		Prompts: []model.RunPrompt{
			{RunID: "new1", Bytes: 40000},
			{RunID: "new2", Bytes: warnBytes, ThreadCut: true},
		},
	})

	p := rep.Prompts
	if p.Runs != 2 {
		t.Errorf("Prompts.Runs = %d, want 2 -- the runs that recorded a prompt, not the six that finished in the window", p.Runs)
	}
	// A half rather than a sixth: the four runs that predate the
	// recording are not a deployment declining to cut a thread.
	if got, want := p.ThreadCutShare, 0.5; !closeEnough(got, want) {
		t.Errorf("ThreadCutShare = %v, want %v -- rated over the prompts, not over every finished run", got, want)
	}
	if got, want := p.LargeShare, 0.5; !closeEnough(got, want) {
		t.Errorf("LargeShare = %v, want %v -- rated over the prompts, not over every finished run", got, want)
	}
	// And the tasks that recorded nothing are absent from Heaviest
	// rather than present at nought bytes.
	if len(p.Heaviest) != 1 || p.Heaviest[0].TaskID != "c3d4" || p.Heaviest[0].Runs != 2 {
		t.Errorf("Heaviest = %+v, want only the task whose attempts recorded a prompt", p.Heaviest)
	}
}

// A scoped report measures the prompts of the tasks it is about and no
// others -- the whole reason scoping happens in one place (Input.scoped).
func TestPromptsAreNarrowedByScope(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Scope:  metrics.Scope{Repos: []string{"acme/widgets"}},
		Tasks: []model.TaskTiming{
			{TaskID: "a1b2", Repo: "acme/widgets", CreatedAt: at(time.Hour)},
			{TaskID: "b2c3", Repo: "acme/docs", CreatedAt: at(time.Hour)},
		},
		Runs: []model.RunTiming{
			runOf("r1", "a1b2", 1, 10*time.Hour),
			runOf("r2", "b2c3", 1, 11*time.Hour),
		},
		Prompts: []model.RunPrompt{
			{RunID: "r1", Bytes: 4000},
			{RunID: "r2", Bytes: 400000, ThreadCut: true},
		},
	})

	p := rep.Prompts
	if p.Runs != 1 || p.Bytes.Max != 4000 || p.ThreadCut != 0 {
		t.Errorf("Prompts = %+v, want only acme/widgets' one 4000-byte prompt", p)
	}
	if len(p.Heaviest) != 1 || p.Heaviest[0].TaskID != "a1b2" {
		t.Errorf("Heaviest = %+v, want only the task in scope", p.Heaviest)
	}
}
