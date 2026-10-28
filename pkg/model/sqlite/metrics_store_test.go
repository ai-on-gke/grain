package sqlite_test

// The measurement reads, against a real database: every moment
// pkg/metrics computes a throughput or latency number from has to survive
// a round trip through SQLite, and a moment that never happened has to
// come back absent rather than zero.

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestTimingsReadEveryMomentAReportMeasuresBetween(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	filed, approved := now, now.Add(30*time.Minute)
	tk := task("a1b2", true)
	tk.CreatedAt = &filed
	tk.ApprovedAt = &approved
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	// One attempt that reached its agent and finished, then a second
	// still in flight -- the two shapes RunTimings has to tell apart.
	started, agentStarted, finished := now.Add(time.Hour), now.Add(time.Hour+5*time.Minute), now.Add(90*time.Minute)
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: started,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunAgentStarted(ctx, "r1", agentStarted); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", finished, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r2", TaskID: "a1b2", Sandbox: "s2", Attempt: 2, StartedAt: now.Add(2 * time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	completed := now.Add(3 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", CompletedAt: &completed}); err != nil {
		t.Fatal(err)
	}

	timings, err := store.TaskTimings(ctx)
	if err != nil {
		t.Fatalf("TaskTimings: %v", err)
	}
	if len(timings) != 1 {
		t.Fatalf("read %d task timings, want 1", len(timings))
	}
	got := timings[0]
	if got.TaskID != "a1b2" {
		t.Errorf("timing = %+v, want task a1b2", got)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(filed) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, filed)
	}
	if got.ApprovedAt == nil || !got.ApprovedAt.Equal(approved) {
		t.Errorf("ApprovedAt = %v, want %v", got.ApprovedAt, approved)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Errorf("CompletedAt = %v, want %v", got.CompletedAt, completed)
	}
	if got.ClosedAt != nil {
		t.Errorf("ClosedAt = %v, want nil -- the task was never closed", got.ClosedAt)
	}

	runs, err := store.RunTimings(ctx)
	if err != nil {
		t.Fatalf("RunTimings: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("read %d run timings, want 2", len(runs))
	}
	first, second := runs[0], runs[1]
	if first.Attempt != 1 || second.Attempt != 2 {
		t.Fatalf("attempts came back %d then %d, want oldest first", first.Attempt, second.Attempt)
	}
	if !first.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", first.StartedAt, started)
	}
	if first.AgentStartedAt == nil || !first.AgentStartedAt.Equal(agentStarted) {
		t.Errorf("AgentStartedAt = %v, want %v", first.AgentStartedAt, agentStarted)
	}
	if first.FinishedAt == nil || !first.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %v", first.FinishedAt, finished)
	}
	if first.Outcome != "succeeded" {
		t.Errorf("Outcome = %q, want succeeded", first.Outcome)
	}

	// The live attempt: started, no agent yet, no ending. All three have
	// to read as absent, since pkg/metrics reports a missing moment by
	// leaving the sample out rather than by measuring against zero.
	if second.AgentStartedAt != nil || second.FinishedAt != nil || second.Outcome != "" {
		t.Errorf("live attempt = %+v, want no agent start, no finish and no outcome", second)
	}
}

// The landing moments: when a task's own pull request opened and when it
// merged (Observation.PrOpenedAt/PrMergedAt), which is what
// metrics.Merges measures filed-to-landed between. A pull request that
// closed without merging has to come back with no merge at all -- the
// task is over either way, and only one of the two endings landed
// anything.
func TestTaskTimingsReadWhenTheWorkLanded(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	for _, id := range []string{"a1b2", "b2c3", "c3d4"} {
		if err := store.PutTask(ctx, task(id, true)); err != nil {
			t.Fatal(err)
		}
	}
	opened, entered, merged := now.Add(time.Hour), now.Add(2*time.Hour), now.Add(3*time.Hour)
	closed := now.Add(4 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", PrOpenedAt: &opened, PrMergedAt: &merged, MergeQueueEnteredAt: &entered,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: "b2c3", PrOpenedAt: &opened, PrClosedAt: &closed,
	}); err != nil {
		t.Fatal(err)
	}

	timings, err := store.TaskTimings(ctx)
	if err != nil {
		t.Fatalf("TaskTimings: %v", err)
	}
	byID := map[string]model.TaskTiming{}
	for _, tm := range timings {
		byID[tm.TaskID] = tm
	}
	if got := byID["a1b2"]; got.PrOpenedAt == nil || !got.PrOpenedAt.Equal(opened) ||
		got.PrMergedAt == nil || !got.PrMergedAt.Equal(merged) {
		t.Errorf("merged task = %+v, want opened %v and merged %v", got, opened, merged)
	}
	// And when the merge queue took it on, which is what its time in the
	// queue is measured from rather than the opening.
	if got := byID["a1b2"]; got.QueueEnteredAt == nil || !got.QueueEnteredAt.Equal(entered) {
		t.Errorf("QueueEnteredAt = %v, want %v", got.QueueEnteredAt, entered)
	}
	if got := byID["b2c3"]; got.QueueEnteredAt != nil {
		t.Errorf("QueueEnteredAt = %v on a task the queue never took on, want nil", got.QueueEnteredAt)
	}
	if got := byID["b2c3"]; got.PrMergedAt != nil {
		t.Errorf("PrMergedAt = %v on a pull request closed unmerged, want nil -- nothing landed",
			got.PrMergedAt)
	}
	if got := byID["c3d4"]; got.PrOpenedAt != nil || got.PrMergedAt != nil {
		t.Errorf("task with no pull request = %+v, want neither moment", got)
	}
}

// TaskTiming carries two facts that are not moments -- whether the task
// had a repo to push to, and whether the merge queue ever had to file a
// fix task for it -- because pkg/metrics cannot measure the pull request
// loop without them: the first is who was ever offered that loop, and
// the second is the recorded form of a red build outliving its run.
func TestTaskTimingsReadTargetAndFixTaskLink(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// One task with a target and a fix task filed against it, one with a
	// target and only an unrelated link, and one with no target at all.
	red := task("a1b2", true)
	red.Links = []model.Link{{Kind: model.LinkFixTask, Target: "f1x0"}}
	green := task("b2c3", true)
	green.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "a1b2"}}
	bare := task("c3d4", true)
	bare.Target = nil
	for _, tk := range []model.Task{red, green, bare} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("putting %s: %v", tk.ID, err)
		}
	}

	timings, err := store.TaskTimings(ctx)
	if err != nil {
		t.Fatalf("TaskTimings: %v", err)
	}
	byID := map[string]model.TaskTiming{}
	for _, tm := range timings {
		byID[tm.TaskID] = tm
	}
	if got := byID["a1b2"]; !got.Targeted || !got.FixTaskFiled {
		t.Errorf("a1b2 = %+v, want targeted with a fix task filed", got)
	}
	if got := byID["b2c3"]; !got.Targeted || got.FixTaskFiled {
		t.Errorf("b2c3 = %+v, want targeted with no fix task -- its only link is a dependency", got)
	}
	if got := byID["c3d4"]; got.Targeted || got.FixTaskFiled {
		t.Errorf("c3d4 = %+v, want neither -- it has no repo to push to", got)
	}
}

// TaskTimings says which tasks are ports and which of those grain made
// itself: a port-of link, and the mechanical-port tag beside it.
func TestTaskTimingsReadHowAPortWasMade(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	mechanical := task("a1b2", true)
	mechanical.Links = []model.Link{{Kind: model.LinkPortOf, Target: "f1x0"}}
	mechanical.Tags = []string{model.TagMechanicalPort}
	byAgent := task("b2c3", true)
	byAgent.Links = []model.Link{{Kind: model.LinkPortOf, Target: "f1x0"}}
	plain := task("c3d4", true)
	for _, tk := range []model.Task{mechanical, byAgent, plain} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("putting %s: %v", tk.ID, err)
		}
	}
	timings, err := store.TaskTimings(ctx)
	if err != nil {
		t.Fatalf("TaskTimings: %v", err)
	}
	want := map[string]string{"a1b2": model.PortMechanical, "b2c3": model.PortByAgent, "c3d4": ""}
	for _, tm := range timings {
		if w, ok := want[tm.TaskID]; ok && tm.Port != w {
			t.Errorf("%s: Port = %q, want %q", tm.TaskID, tm.Port, w)
		}
	}
}

// RunPrompts is the deployment-wide read behind metrics.Prompts: a size
// per run measured in SQLite, and whether grain had to cut the task's
// conversation to reach it. What it must not do is report a run that was
// never handed a prompt as one handed nothing.
func TestRunPromptsMeasureEveryRecordedPromptAndWhichWereCut(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"r1", "r2", "r3"} {
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s" + id, Attempt: i + 1,
			StartedAt: now.Add(time.Duration(i) * time.Hour),
		}, model.Limits{}); err != nil {
			t.Fatalf("starting %s: %v", id, err)
		}
		// Finished before the next one starts: at most one run per task
		// is ever open (schema.go's task_run_open_task index).
		if err := store.FinishRun(ctx, id, now.Add(time.Duration(i)*time.Hour+time.Minute),
			"succeeded", ""); err != nil {
			t.Fatalf("finishing %s: %v", id, err)
		}
	}

	// A prompt that fit whole, one whose thread grain cut, and a run that
	// never reached its agent and so recorded nothing at all.
	whole := "you are running as task a1b2\n- ada: get on with it\n"
	cut := "you are running as task a1b2\n- ada: get on with it\n" +
		model.PromptThreadCutNotice + "4 message(s) from the middle of it, 12 KB of text, elided\n" +
		"- ada: and finish it\n"
	if err := store.SetRunPrompt(ctx, "r1", whole); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunPrompt(ctx, "r2", cut); err != nil {
		t.Fatal(err)
	}

	prompts, err := store.RunPrompts(ctx)
	if err != nil {
		t.Fatalf("RunPrompts: %v", err)
	}
	byRun := map[string]model.RunPrompt{}
	for _, p := range prompts {
		byRun[p.RunID] = p
	}
	if len(prompts) != 2 {
		t.Fatalf("read %d prompts (%+v), want the two that were recorded", len(prompts), prompts)
	}
	if got := byRun["r1"]; got.Bytes != len(whole) || got.ThreadCut {
		t.Errorf("r1 = %+v, want %d bytes and no cut", got, len(whole))
	}
	if got := byRun["r2"]; got.Bytes != len(cut) || !got.ThreadCut {
		t.Errorf("r2 = %+v, want %d bytes and a cut thread", got, len(cut))
	}
	if _, ok := byRun["r3"]; ok {
		t.Errorf("r3 = %+v, want no row: a run that was never handed a prompt is not a prompt of nought bytes",
			byRun["r3"])
	}
}

// LastPrompt is the same measurement narrowed to one task's most recent
// attempt -- "is this conversation costing its runs *now*", which is
// what a control offering a fresh conversation may turn on
// (ui.Client.ListConsoleSessions). The last attempt wins over an earlier
// one however big that one was, and a task nothing has been dispatched
// for measures as nothing rather than as a prompt of nought bytes.
func TestLastPromptIsTheMostRecentAttemptsOwnMeasurement(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task("c3d4", true)); err != nil {
		t.Fatal(err)
	}
	if size, cut, err := store.LastPrompt(ctx, "c3d4"); err != nil || !size.Zero() || cut {
		t.Fatalf("LastPrompt of a task nothing ran = (%+v, %v, %v), want no measurement at all",
			size, cut, err)
	}

	for i, id := range []string{"r1", "r2"} {
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s" + id, Attempt: i + 1,
			StartedAt: now.Add(time.Duration(i) * time.Hour),
		}, model.Limits{}); err != nil {
			t.Fatalf("starting %s: %v", id, err)
		}
		if err := store.FinishRun(ctx, id, now.Add(time.Duration(i)*time.Hour+time.Minute),
			"succeeded", ""); err != nil {
			t.Fatalf("finishing %s: %v", id, err)
		}
	}
	first := strings.Repeat("x", 4096) + "\n"
	latest := "you are running as task a1b2\n" +
		model.PromptThreadCutNotice + "4 message(s) from the middle of it, 12 KB of text, elided\n"
	if err := store.SetRunPrompt(ctx, "r1", first); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunPrompt(ctx, "r2", latest); err != nil {
		t.Fatal(err)
	}

	size, cut, err := store.LastPrompt(ctx, "a1b2")
	if err != nil {
		t.Fatalf("LastPrompt: %v", err)
	}
	if size.Bytes != len(latest) {
		t.Errorf("LastPrompt = %+v, want the second attempt's %d bytes rather than the first's %d",
			size, len(latest), len(first))
	}
	if !cut {
		t.Error("LastPrompt reports no cut thread, want the notice grain wrote into the last prompt")
	}
}

// A prompt that merely quotes a cut notice mid-line -- somebody pasting
// an earlier prompt into a comment, which comes back in the next one --
// is not a prompt that was cut. The notice counts only where grain wrote
// it, at the start of a line of its own (model.PromptThreadCutNotice).
func TestRunPromptsDoNotCountAQuotedCutNotice(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunPrompt(ctx,
		"r1", "- ada: I read \""+model.PromptThreadCutNotice+"\" in yesterday's prompt\n"); err != nil {
		t.Fatal(err)
	}

	prompts, err := store.RunPrompts(ctx)
	if err != nil {
		t.Fatalf("RunPrompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0].ThreadCut {
		t.Errorf("RunPrompts = %+v, want one prompt that was not cut", prompts)
	}
}

// The measurement is in bytes, not characters: a context window is spent
// in bytes, and SQLite's own length() counts characters on TEXT -- which
// is why the read casts to BLOB.
func TestRunPromptsMeasureBytesRatherThanCharacters(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	prompt := "a task called “widgets” — em dashes and all\n"
	if err := store.SetRunPrompt(ctx, "r1", prompt); err != nil {
		t.Fatal(err)
	}

	prompts, err := store.RunPrompts(ctx)
	if err != nil {
		t.Fatalf("RunPrompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Bytes != len(prompt) {
		t.Errorf("RunPrompts = %+v, want one prompt of %d bytes (not %d characters)",
			prompts, len(prompt), len([]rune(prompt)))
	}
}

func TestRunTranscriptsMeasureBytes(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	transcript := "agent transcript with utf8 “symbols” and text\n"
	if err := store.SetRunTranscript(ctx, "r1", transcript); err != nil {
		t.Fatal(err)
	}

	transcripts, err := store.RunTranscripts(ctx)
	if err != nil {
		t.Fatalf("RunTranscripts: %v", err)
	}
	if len(transcripts) != 1 || transcripts[0].Bytes != len(transcript) {
		t.Errorf("RunTranscripts = %+v, want one transcript of %d bytes",
			transcripts, len(transcript))
	}
}

// The CI half of the measurement reads: a job's wait, the cause behind
// it, and the repository it belonged to, which only the run beside it
// knows (Store.CIJobTimings, grain/task-345).
func TestCIJobTimingsReadEachJobsWaitAndWhoseItWas(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	run := model.CIRun{
		ID: "r1", Key: "k1", Repo: "acme/widgets", Ref: "grain/task-9",
		Commit: "9f8e7d", Event: "push", StartedAt: now,
		Jobs: []model.CIJob{
			{
				JobID: "image", Name: "build the image", Sandbox: "grain-ci-1",
				StartedAt: now.Add(40 * time.Minute), FinishedAt: now.Add(time.Hour),
				Conclusion: "success",
				Queued:     40 * time.Minute, QueuedCause: "hole",
				QueuedReason: "capacity is being held for a larger job",
			},
			// A job that never got a machine: no start, an ending, and a
			// wait that is the whole of what can be said about it.
			{
				JobID: "publish", Name: "publish", FinishedAt: now.Add(36 * time.Minute),
				Conclusion: "cancelled", Queued: 36 * time.Minute, QueuedCause: "slot",
				QueuedReason: "for one of the 3 sandboxes",
			},
			// And one served from the cache, which asked for no machine.
			{JobID: "lint", Name: "lint", Cached: true,
				StartedAt: now, FinishedAt: now.Add(time.Minute), Conclusion: "success"},
		},
	}
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("putting a CI run: %v", err)
	}

	jobs, err := store.CIJobTimings(ctx)
	if err != nil {
		t.Fatalf("reading CI job timings: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("read %d jobs, want every job of the run: %+v", len(jobs), jobs)
	}
	// Plan order, and the repository read off the run rather than the
	// job: a job row says nothing about whose branch it was, and "which
	// repositories wear this deployment's queueing" is half the question.
	if jobs[0].Repo != "acme/widgets" || jobs[0].Ref != "grain/task-9" {
		t.Errorf("job came back as %+v, want its run's repository and branch", jobs[0])
	}
	if jobs[0].Name != "build the image" || jobs[0].Queued != 40*time.Minute ||
		jobs[0].QueuedCause != "hole" {
		t.Errorf("the waiting job came back as %+v", jobs[0])
	}
	if jobs[0].StartedAt == nil || jobs[0].FinishedAt == nil {
		t.Errorf("a job that ran came back with no timestamps: %+v", jobs[0])
	}
	// The job that never ran: a missing start stays missing, rather than
	// coming back as the year one, which a report would read as a job
	// that worked for two thousand years.
	if jobs[1].StartedAt != nil {
		t.Errorf("a job that never got a machine came back with a start: %v", *jobs[1].StartedAt)
	}
	if jobs[1].FinishedAt == nil || jobs[1].Queued != 36*time.Minute {
		t.Errorf("the job that never ran came back as %+v, want its whole wait", jobs[1])
	}
	if !jobs[2].Cached {
		t.Errorf("the cache hit came back as %+v, want it marked as one so a report can leave it out", jobs[2])
	}
}
