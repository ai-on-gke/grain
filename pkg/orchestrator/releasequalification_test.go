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

package orchestrator_test

// SyncReleaseQualification's own tests: Stage 2 of a release candidate,
// against a forge faked at the reconciler's own interface, with each
// qualification task finished directly in the store the way a run would
// have left it.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

const (
	rqTag = "v1.4.0-rc.2"
	rqSHA = "89abcdef0123456789abcdef0123456789abcdef"
)

var rqItems = []string{"qualification-mergequeue", "qualification-ci-diagnostics",
	"qualification-base-triage", "qualification-system-smoke"}

type fakeQualificationForge struct {
	app      string // the App grain posts as; "" when it cannot tell
	tags     []github.Tag
	nightly  string // "" for no run yet, else its conclusion
	anySHA   bool   // nightly is on every commit, not only rqSHA
	heads    map[int64]string
	checks   []github.CheckRun
	branches map[string]bool
	nextID   int64
	messages map[string]string // annotated tag -> its message
}

func (f *fakeQualificationForge) ListTags(owner, repo string) ([]github.Tag, error) {
	return f.tags, nil
}

func (f *fakeQualificationForge) ListMatchingTags(owner, repo, prefix string) ([]github.TagRef, error) {
	var out []github.TagRef
	for _, t := range f.tags {
		if !strings.HasPrefix(t.Name, prefix) {
			continue
		}
		if _, ok := f.messages[t.Name]; ok {
			out = append(out, github.TagRef{Name: t.Name, ObjectSHA: "tag-object:" + t.Name, ObjectType: "tag"})
		} else {
			out = append(out, github.TagRef{Name: t.Name, ObjectSHA: t.SHA, ObjectType: "commit"})
		}
	}
	return out, nil
}

func (f *fakeQualificationForge) GetAnnotatedTag(owner, repo, sha string) (*github.AnnotatedTag, error) {
	name, ok := strings.CutPrefix(sha, "tag-object:")
	msg, annotated := f.messages[name]
	if !ok || !annotated {
		return nil, nil
	}
	for _, t := range f.tags {
		if t.Name == name {
			return &github.AnnotatedTag{Commit: t.SHA, Message: msg}, nil
		}
	}
	return nil, nil
}

func (f *fakeQualificationForge) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	var out []github.CheckRun
	for _, c := range f.checks {
		if head := f.heads[c.ID]; head == "" || head == ref {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeQualificationForge) CheckRunApp(owner, repo string) (string, error) {
	return f.app, nil
}

func (f *fakeQualificationForge) ListWorkflowRuns(owner, repo, sha string) ([]github.CheckRun, error) {
	if f.nightly == "" || (sha != rqSHA && !f.anySHA) {
		return nil, nil
	}
	conclusion := f.nightly
	return []github.CheckRun{
		{Name: "Tests", Status: "completed", Conclusion: &conclusion},
		{Name: "nightly", Status: "completed", Conclusion: &conclusion},
	}, nil
}

func (f *fakeQualificationForge) BranchExists(owner, repo, branch string) (bool, error) {
	return f.branches[branch], nil
}

func (f *fakeQualificationForge) DefaultBranch(owner, repo string) (string, error) {
	return "main", nil
}

func fakeCheckOutput(out *github.CheckRunOutput) string {
	if out == nil {
		return ""
	}
	return strings.TrimSpace(out.Title + "\n\n" + out.Summary)
}

func (f *fakeQualificationForge) CreateCheckRun(owner, repo string, run github.CheckRunWrite) (int64, error) {
	f.nextID++
	c := github.CheckRun{
		ID:         f.nextID,
		Name:       run.Name,
		Status:     run.Status,
		App:        f.app,
		ExternalID: run.ExternalID,
		Output:     fakeCheckOutput(run.Output),
	}
	if run.Output != nil {
		c.FailureReason = run.Output.Summary
	}
	f.checks = append(f.checks, c)
	if f.heads == nil {
		f.heads = map[int64]string{}
	}
	f.heads[f.nextID] = run.HeadSHA
	return f.nextID, nil
}

func (f *fakeQualificationForge) UpdateCheckRun(owner, repo string, id int64, run github.CheckRunWrite) error {
	for i := range f.checks {
		if f.checks[i].ID == id {
			f.checks[i].Status = run.Status
			if run.ExternalID != "" {
				f.checks[i].ExternalID = run.ExternalID
			}
			if run.Conclusion != "" {
				c := run.Conclusion
				f.checks[i].Conclusion = &c
			}
			if run.Output != nil {
				f.checks[i].FailureReason = run.Output.Summary
				f.checks[i].Output = fakeCheckOutput(run.Output)
			}
		}
	}
	return nil
}

func (f *fakeQualificationForge) check(t *testing.T) github.CheckRun {
	t.Helper()
	var matches []github.CheckRun
	for _, c := range f.checks {
		if c.Name != model.ReleaseHoldCheckName {
			matches = append(matches, c)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("got %d qualification check runs in %+v, want exactly one", len(matches), f.checks)
	}
	return matches[0]
}

func newQualificationRepo(t *testing.T) (*model.Store, context.Context, model.RepoRef, *fakeQualificationForge) {
	t.Helper()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	var items []model.SuiteItem
	for _, id := range rqItems {
		if err := store.PutTemplate(ctx, model.Template{
			ID: id, Name: id, Title: id + " for {{rc_tag}}", Body: "Qualify {{rc_tag}} at {{rc_sha}}.",
			Fields:    []model.TemplateField{{Name: "rc_tag"}, {Name: "rc_sha"}},
			CreatedAt: baseTime,
		}); err != nil {
			t.Fatalf("put template %s: %v", id, err)
		}
		items = append(items, model.SuiteItem{TemplateID: id})
	}
	if err := store.PutSuite(ctx, model.Suite{
		ID:   model.RepoDeclaredID(model.DeclaredSuite, repo, model.ReleaseQualificationSuiteStem),
		Name: "Release qualification", Items: items,
		Mode: model.SuiteUntilClean, MaxPasses: 1, CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("put suite: %v", err)
	}
	forge := &fakeQualificationForge{
		tags: []github.Tag{
			{Name: "v1.4.0-rc.1", SHA: strings.Repeat("1", 40)},
			{Name: "v1.4.0-rc.1-discarded", SHA: strings.Repeat("1", 40)},
			{Name: rqTag, SHA: rqSHA},
			{Name: "v1.3.0", SHA: strings.Repeat("3", 40)},
			{Name: "v1.3.0-rc.1", SHA: strings.Repeat("3", 40)},
		},
		app:      "grain-test",
		branches: map[string]bool{},
	}
	return store, ctx, repo, forge
}

func syncQualification(t *testing.T, ctx context.Context, store *model.Store, forge *fakeQualificationForge, at time.Time) {
	t.Helper()
	if err := orchestrator.SyncReleaseQualification(ctx, store, forge, at); err != nil {
		t.Fatalf("SyncReleaseQualification: %v", err)
	}
}

// qualificationTasks is every task filed for the candidate, by template,
// oldest attempt first.
func qualificationTasks(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) map[string][]string {
	t.Helper()
	runs, err := store.SuiteRunsTagged(ctx, model.ReleaseQualificationTag(repo, rqTag))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, run := range runs {
		for _, task := range run.Tasks {
			out[task.TemplateID] = append(out[task.TemplateID], task.TaskID)
		}
	}
	return out
}

// finishQualificationTask completes taskID with a closing note carrying
// verdict's footer.
func finishQualificationTask(t *testing.T, ctx context.Context, store *model.Store, taskID, verdict string, at time.Time) {
	t.Helper()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: ownRunAuthor(taskID),
		Body: "Report.\n\nQualification-Verdict: " + verdict + "\nQualified-Tag: " + rqTag +
			"\nQualified-Commit: " + rqSHA + "\n",
		CreatedAt: at,
	}); err != nil {
		t.Fatalf("closing note on %s: %v", taskID, err)
	}
	completeTask(t, ctx, store, taskID, at)
}

// ownRunAuthor is how grain stores a comment taskID's own run wrote
// (finish.go's relayComment).
func ownRunAuthor(taskID string) model.Attribution {
	return model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
		OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: taskID},
	}
}

// A PASS footer somebody else wrote into the thread after the run's own
// FAIL -- a child's `grain task message`, a person quoting the template --
// is not the run's verdict.
func TestClosingNoteReadsOnlyTheTasksOwnRun(t *testing.T) {
	t.Parallel()
	footer := func(v string) string {
		return "Qualification-Verdict: " + v + "\nQualified-Tag: " + rqTag + "\nQualified-Commit: " + rqSHA
	}
	own := model.Comment{Author: ownRunAuthor("7"), Body: footer("FAIL")}
	child := model.Comment{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "8"}}, Body: footer("PASS")}
	human := model.Comment{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}, Body: footer("PASS")}
	otherRun := model.Comment{Author: ownRunAuthor("9"), Body: footer("PASS")}
	if got := orchestrator.ClosingNote([]model.Comment{own, child, human, otherRun}, "7"); got != own.Body {
		t.Errorf("closingNote = %q, want the task's own FAIL note", got)
	}
	if got := orchestrator.ClosingNote([]model.Comment{child, human, otherRun}, "7"); got != "" {
		t.Errorf("closingNote with no note of the task's own = %q, want none", got)
	}
	plain := model.Comment{Author: ownRunAuthor("7"), Body: "no footer"}
	if got := orchestrator.ClosingNote([]model.Comment{plain, child}, "7"); got != plain.Body {
		t.Errorf("closingNote = %q, want the task's own newest note", got)
	}
}

func TestReleaseQualificationWaitsForAGreenStage1(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	syncQualification(t, ctx, store, forge, baseTime)
	forge.nightly = "failure"
	syncQualification(t, ctx, store, forge, baseTime.Add(time.Minute))
	if got := qualificationTasks(t, ctx, store, repo); len(got) != 0 {
		t.Fatalf("filed %v before Stage 1 was green, want nothing", got)
	}
	if len(forge.checks) != 0 {
		t.Fatalf("posted %+v before Stage 2 started, want nothing", forge.checks)
	}
}

func TestReleaseQualificationPassesACleanCandidate(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	syncQualification(t, ctx, store, forge, baseTime)

	tasks := qualificationTasks(t, ctx, store, repo)
	if len(tasks) != 3 || len(tasks["qualification-system-smoke"]) != 0 {
		t.Fatalf("got %v, want the three gating items and not system-smoke", tasks)
	}
	if forge.check(t).Status != "in_progress" {
		t.Fatalf("check run is %+v, want in_progress", forge.check(t))
	}
	first, err := store.GetTask(ctx, tasks["qualification-mergequeue"][0])
	if err != nil || first == nil {
		t.Fatalf("task: %v", err)
	}
	if first.Priority != model.PriorityReleaseGate || !strings.Contains(first.Body, rqSHA) ||
		!strings.Contains(first.Title, rqTag) || first.AutoMerge {
		t.Fatalf("task is %+v, want priority %d, rc_tag and rc_sha filled in, no auto-merge",
			first, model.PriorityReleaseGate)
	}

	at := baseTime.Add(time.Minute)
	for _, item := range rqItems[:3] {
		finishQualificationTask(t, ctx, store, tasks[item][0], "PASS", at)
	}
	syncQualification(t, ctx, store, forge, at)
	tasks = qualificationTasks(t, ctx, store, repo)
	if len(tasks["qualification-system-smoke"]) != 1 {
		t.Fatalf("got %v, want system-smoke started once the others passed", tasks)
	}
	if c := forge.check(t); c.Status != "in_progress" {
		t.Fatalf("check run is %+v before system-smoke finished, want in_progress", c)
	}

	finishQualificationTask(t, ctx, store, tasks["qualification-system-smoke"][0], "PASS", at.Add(time.Minute))
	syncQualification(t, ctx, store, forge, at.Add(time.Minute))
	c := forge.check(t)
	if c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != model.ConclusionSuccess {
		t.Fatalf("check run is %+v, want completed success", c)
	}

	// And a candidate with its verdict posted is left alone.
	syncQualification(t, ctx, store, forge, at.Add(2*time.Minute))
	if got := qualificationTasks(t, ctx, store, repo); len(got["qualification-mergequeue"]) != 1 {
		t.Fatalf("got %v after the verdict, want nothing filed again", got)
	}
}

// A grain/release-qualification check run from any app but grain's own
// is not its verdict: it neither stops qualification nor is adopted and
// updated, however it concluded.
func TestReleaseQualificationIgnoresACheckRunFromAnotherApp(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	success := model.ConclusionSuccess
	forge.checks = []github.CheckRun{
		{ID: 90, Name: model.ReleaseQualificationCheckName, Status: "completed", Conclusion: &success, App: "github-actions"},
		{ID: 91, Name: model.ReleaseQualificationCheckName, Status: "completed", Conclusion: &success, App: "intruder"},
		{ID: 92, Name: model.ReleaseQualificationCheckName, Status: "completed", Conclusion: &success},
	}
	forge.nextID = 100
	syncQualification(t, ctx, store, forge, baseTime)

	if tasks := qualificationTasks(t, ctx, store, repo); len(tasks) != 3 {
		t.Fatalf("got %v, want Stage 2 started despite the foreign check runs", tasks)
	}
	if len(forge.checks) != 4 {
		t.Fatalf("got check runs %+v, want grain's own posted beside the three foreign ones", forge.checks)
	}
	for _, c := range forge.checks[:3] {
		if c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != model.ConclusionSuccess || c.FailureReason != "" {
			t.Fatalf("foreign check run %d is %+v, want it untouched", c.ID, c)
		}
	}
	if own := forge.checks[3]; own.ID != 101 || own.App != "grain-test" || own.Status != "in_progress" ||
		own.ExternalID != model.ReleaseQualificationTag(repo, rqTag) {
		t.Fatalf("grain's check run is %+v, want a new in_progress one from grain-test for %s", own, rqTag)
	}
}

// Even a check run from grain's own App only counts when its ExternalID
// matches this candidate's ReleaseQualificationTag: a discarded rc on the
// same commit, or a same-app run with no ExternalID, neither blocks
// qualification nor is overwritten.
func TestReleaseQualificationIgnoresSameAppCheckRunWithDifferentExternalID(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	failure := model.ConclusionFailure
	success := model.ConclusionSuccess
	forge.checks = []github.CheckRun{
		{
			ID:         80,
			Name:       model.ReleaseQualificationCheckName,
			Status:     "completed",
			Conclusion: &failure,
			App:        "grain-test",
			ExternalID: model.ReleaseQualificationTag(repo, "v1.4.0-rc.1"),
		},
		{
			ID:         81,
			Name:       model.ReleaseQualificationCheckName,
			Status:     "completed",
			Conclusion: &success,
			App:        "grain-test",
		},
	}
	forge.nextID = 100
	syncQualification(t, ctx, store, forge, baseTime)

	if tasks := qualificationTasks(t, ctx, store, repo); len(tasks) != 3 {
		t.Fatalf("got %v, want Stage 2 started despite earlier/untagged check runs on the same SHA", tasks)
	}
	if len(forge.checks) != 3 {
		t.Fatalf("got check runs %+v, want a new check run posted beside the two ignored ones", forge.checks)
	}
	if own := forge.checks[2]; own.ID != 101 || own.Status != "in_progress" ||
		own.ExternalID != model.ReleaseQualificationTag(repo, rqTag) {
		t.Fatalf("grain's check run is %+v, want a new in_progress one tagged for %s", own, rqTag)
	}
}

// A forge connection that cannot say which App it posts as cannot tell
// its own check run from anyone else's, so nothing is started or posted.
func TestReleaseQualificationRefusesWithoutItsOwnApp(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	forge.app = ""
	if err := orchestrator.SyncReleaseQualification(ctx, store, forge, baseTime); err == nil ||
		!strings.Contains(err.Error(), "not a GitHub App") {
		t.Fatalf("SyncReleaseQualification = %v, want it to refuse without its own app", err)
	}
	if tasks := qualificationTasks(t, ctx, store, repo); len(tasks) != 0 || len(forge.checks) != 0 {
		t.Fatalf("filed %v and posted %+v, want nothing", tasks, forge.checks)
	}
}

func TestReleaseQualificationRetriesAFlakeOnceAndFailsAConfirmedRed(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	syncQualification(t, ctx, store, forge, baseTime)
	tasks := qualificationTasks(t, ctx, store, repo)

	at := baseTime.Add(time.Minute)
	finishQualificationTask(t, ctx, store, tasks["qualification-mergequeue"][0], "PASS", at)
	finishQualificationTask(t, ctx, store, tasks["qualification-ci-diagnostics"][0], "PASS", at)
	// A pull request is a change, and a change is a failure.
	triage := tasks["qualification-base-triage"][0]
	openPullRequest(t, ctx, store, triage)
	finishQualificationTask(t, ctx, store, triage, "PASS", at)
	syncQualification(t, ctx, store, forge, at)

	tasks = qualificationTasks(t, ctx, store, repo)
	if len(tasks["qualification-base-triage"]) != 2 || len(tasks["qualification-system-smoke"]) != 0 {
		t.Fatalf("got %v, want base-triage retried once and system-smoke held", tasks)
	}

	// The retry leaves its scratch branch behind: confirmed red.
	retry := tasks["qualification-base-triage"][1]
	forge.branches[model.QualificationScratchBranch(retry)] = true
	finishQualificationTask(t, ctx, store, retry, "PASS", at.Add(time.Minute))
	syncQualification(t, ctx, store, forge, at.Add(time.Minute))
	c := forge.check(t)
	if c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != model.ConclusionFailure {
		t.Fatalf("check run is %+v, want completed failure", c)
	}
	if !strings.Contains(c.FailureReason, "qualification/"+retry) {
		t.Fatalf("summary %q does not name the leaked branch", c.FailureReason)
	}
}

func TestReleaseQualificationRerunsAnInfraVerdict(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	syncQualification(t, ctx, store, forge, baseTime)
	tasks := qualificationTasks(t, ctx, store, repo)

	at := baseTime.Add(time.Minute)
	finishQualificationTask(t, ctx, store, tasks["qualification-ci-diagnostics"][0], "INFRA", at)
	syncQualification(t, ctx, store, forge, at)
	tasks = qualificationTasks(t, ctx, store, repo)
	if len(tasks["qualification-ci-diagnostics"]) != 2 {
		t.Fatalf("got %v, want ci-diagnostics re-run after INFRA", tasks)
	}
	if c := forge.check(t); c.Status != "in_progress" {
		t.Fatalf("check run is %+v, want an INFRA verdict to leave it qualifying", c)
	}
}

// proposeChild files a task proposed by parent on base, as
// relayProposedTasks would have.
func proposeChild(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, parent, base string) string {
	t.Helper()
	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: id, Title: "fixture", Body: "fixture", Target: &repo, Base: base,
		Links:    []model.Link{{Kind: model.LinkProposedBy, Target: parent}},
		Approval: &model.Attribution{Actor: model.SuitePrincipal}, ApprovedAt: &baseTime,
		CreatedAt: &baseTime,
	}); err != nil {
		t.Fatalf("proposing from %s: %v", parent, err)
	}
	return id
}

func TestReleaseQualificationTellsScratchFixturesFromDefects(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	syncQualification(t, ctx, store, forge, baseTime)
	tasks := qualificationTasks(t, ctx, store, repo)
	at := baseTime.Add(time.Minute)

	// mergequeue's fixtures merged into its own scratch branch and are
	// done: the experiment, not a finding.
	mq := tasks["qualification-mergequeue"][0]
	fixture := proposeChild(t, ctx, store, repo, mq, model.QualificationScratchBranch(mq))
	completeTask(t, ctx, store, fixture, at)
	finishQualificationTask(t, ctx, store, mq, "PASS", at)
	// base-triage left one of its fixtures open: leaked scratch state.
	bt := tasks["qualification-base-triage"][0]
	proposeChild(t, ctx, store, repo, bt, model.QualificationScratchBranch(bt))
	finishQualificationTask(t, ctx, store, bt, "PASS", at)
	// ci-diagnostics filed a defect against main.
	ci := tasks["qualification-ci-diagnostics"][0]
	proposeChild(t, ctx, store, repo, ci, "main")
	finishQualificationTask(t, ctx, store, ci, "PASS", at)

	syncQualification(t, ctx, store, forge, at)
	tasks = qualificationTasks(t, ctx, store, repo)
	if len(tasks["qualification-mergequeue"]) != 1 || len(tasks["qualification-base-triage"]) != 2 ||
		len(tasks["qualification-ci-diagnostics"]) != 2 {
		t.Fatalf("got %v, want mergequeue passed and the other two retried", tasks)
	}
}
