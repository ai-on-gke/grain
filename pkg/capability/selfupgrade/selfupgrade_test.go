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

package selfupgrade

// What the three tools a self-upgrade grant adds actually do, against a
// fake deployment: which mode they ask for, what they tell the agent
// about the upgrade it just scheduled, what they log for an operator
// reading the journal afterwards, and how they answer a deployment that
// cannot upgrade at all.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// fakeDeployment records what it was asked for and answers with whatever
// the test set up -- the daemon's three routes, without a daemon.
type fakeDeployment struct {
	status Status
	// startedBranch and startedAfterTasksFinish are what StartUpgrade was
	// last called with; calls counts how many times, since "scheduling
	// starts nothing" is a claim about the call that was *not* made.
	startedBranch           string
	startedAfterTasksFinish bool
	starts                  int
	cancelled               bool
	cancels                 int
	err                     error
}

func (f *fakeDeployment) UpgradeStatus(context.Context) (Status, error) {
	return f.status, f.err
}

func (f *fakeDeployment) StartUpgrade(_ context.Context, branch string, afterTasksFinish bool) (Status, error) {
	f.starts++
	f.startedBranch, f.startedAfterTasksFinish = branch, afterTasksFinish
	if f.err != nil {
		return Status{}, f.err
	}
	if afterTasksFinish {
		f.status.Scheduled = &Scheduled{Branch: branch, RequestedAt: time.Now().UTC(), ByID: "grain"}
	} else {
		f.status.Branch, f.status.Phase = branch, "running"
	}
	return f.status, nil
}

func (f *fakeDeployment) CancelScheduledUpgrade(context.Context) (Status, bool, error) {
	f.cancels++
	if f.err != nil {
		return Status{}, false, f.err
	}
	cancelled := f.status.Scheduled != nil
	f.status.Scheduled = nil
	f.cancelled = cancelled
	return f.status, cancelled, nil
}

// recorder collects what the tools wrote to the journal, which is the
// whole of this capability's accountability: there is no per-call human
// in the loop, so the log line is what answers "why did this deployment
// restart".
type recorder struct{ lines []string }

func (r *recorder) log(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recorder) joined() string { return strings.Join(r.lines, "\n") }

// tools builds the roster for a task, with a journal to read back and an auto-approving confirmation.
func tools(t *testing.T, deployment Deployment) (map[string]mcp.Tool, *recorder) {
	t.Helper()
	rec := &recorder{}
	byName := map[string]mcp.Tool{}
	for _, tool := range Tools(Config{
		TaskID:     "335",
		Deployment: deployment,
		Confirm: func(ctx context.Context, question string) (bool, string, error) {
			return true, "approved by test", nil
		},
		Log: rec.log,
	}) {
		byName[tool.Name] = tool
	}
	for _, want := range []string{"deployment_upgrade_status", "upgrade_deployment", "cancel_deployment_upgrade"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("Tools gave %v, want %s among them", byName, want)
		}
	}
	return byName, rec
}

func enabledStatus() Status {
	return Status{Enabled: true, Mode: "image", RunningImage: "ghcr.io/bwsalmon/grain/grain:main"}
}

func openStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}

func putTask(t *testing.T, store *model.Store, ctx context.Context) string {
	t.Helper()
	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("NewTaskID: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{ID: id, Interactive: true}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	return id
}

func humanReply(t *testing.T, store *model.Store, ctx context.Context, taskID, body string) {
	t.Helper()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      body,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
}

func awaitConfirmWaiting(t *testing.T, store *model.Store, ctx context.Context, taskID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		obs, err := store.GetObservation(ctx, taskID)
		if err != nil {
			t.Fatalf("GetObservation: %v", err)
		}
		if obs != nil && obs.PendingQuestionCommentID != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Confirm never marked the task awaiting a reply -- it did not reach its polling loop")
		}
		time.Sleep(time.Millisecond)
	}
}

// The reading an agent decides on. The live CI count is the point of it:
// a release build an upgrade would kill is the difference between
// "upgrading in a minute" and "upgrading in ninety", and a scheduled
// upgrade waits for it.
func TestTheStatusToolReportsWhatAScheduledUpgradeIsWaitingFor(t *testing.T) {
	t.Parallel()
	status := enabledStatus()
	status.InFlightRuns = 4
	status.Scheduled = &Scheduled{
		Branch: "main", RequestedAt: time.Now().UTC(), ByID: "334", BusyRuns: 2, BusyCIRuns: 1,
	}
	roster, rec := tools(t, &fakeDeployment{status: status})

	got := roster["deployment_upgrade_status"].Handler(context.Background(), nil)
	if got.IsError {
		t.Fatalf("deployment_upgrade_status = %+v, want a reading", got)
	}
	for _, want := range []string{"image", "ghcr.io/bwsalmon/grain/grain:main", "4 runs", "main", "2 runs", "1 CI run"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("the reading does not mention %q:\n%s", want, got.Text)
		}
	}
	if !strings.Contains(rec.joined(), "task=\"335\"") {
		t.Errorf("the journal does not name the task that read it:\n%s", rec.joined())
	}
}

// Who asked, in terms the reader can follow up: an agent's principal id
// is a task id, and an agent deciding whether to leave a scheduled
// upgrade alone wants to know which task wanted it (grain/task-353). A
// human or the deployment's own automation is named as it stands.
func TestTheStatusToolNamesWhoAskedForAScheduledUpgrade(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, kind, id, want string }{
		{"an agent is named by its task", "agent", "335", "asked for by task 335"},
		{"a person is named as they are", "human", "alice", "asked for by alice"},
		{"an unrecorded requester says so", "", "", "somebody this deployment did not record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status := enabledStatus()
			status.Scheduled = &Scheduled{
				Branch: "main", RequestedAt: time.Now().UTC(), ByKind: tc.kind, ByID: tc.id,
			}
			roster, _ := tools(t, &fakeDeployment{status: status})
			got := roster["deployment_upgrade_status"].Handler(context.Background(), nil)
			if !strings.Contains(got.Text, tc.want) {
				t.Errorf("the reading does not say %q:\n%s", tc.want, got.Text)
			}
		})
	}
}

// A deployment with nothing scheduled and nothing running says so
// plainly rather than leaving an agent to infer it from missing lines.
func TestTheStatusToolSaysWhenNothingIsScheduled(t *testing.T) {
	t.Parallel()
	roster, _ := tools(t, &fakeDeployment{status: enabledStatus()})
	got := roster["deployment_upgrade_status"].Handler(context.Background(), nil)
	if !strings.Contains(got.Text, "No upgrade is scheduled") {
		t.Errorf("the reading does not say that nothing is scheduled:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "No upgrade has been attempted") {
		t.Errorf("the reading does not say that nothing has been attempted:\n%s", got.Text)
	}
}

// The default, and the whole point of the tool's shape: an agent that
// says nothing about when gets the mode that costs nothing.
func TestUpgradingDefaultsToWaitingForThisDeploymentToFinish(t *testing.T) {
	t.Parallel()
	fake := &fakeDeployment{status: enabledStatus()}
	roster, rec := tools(t, fake)

	got := roster["upgrade_deployment"].Handler(context.Background(), map[string]any{"branch": "main"})
	if got.IsError {
		t.Fatalf("upgrade_deployment = %+v, want it accepted", got)
	}
	if fake.starts != 1 || fake.startedBranch != "main" {
		t.Fatalf("StartUpgrade called %d times with %q", fake.starts, fake.startedBranch)
	}
	if !fake.startedAfterTasksFinish {
		t.Error("an upgrade asked for with no mode started immediately; the default must be to wait")
	}
	// The one thing an agent will otherwise get wrong: it is itself one
	// of the runs the cut-over waits for, so polling for the upgrade to
	// land holds up the upgrade forever.
	if !strings.Contains(got.Text, "own run") {
		t.Errorf("the answer does not tell the agent its own run is one of the runs waited for:\n%s", got.Text)
	}
	if !strings.Contains(rec.joined(), "main") || !strings.Contains(rec.joined(), "scheduled") {
		t.Errorf("the journal does not record the branch and the mode:\n%s", rec.joined())
	}
}

// Asking for an immediate upgrade is possible, warns in the words it
// answers with, and is logged before the call rather than after -- the
// restart it triggers may well destroy the process that would have
// written the line.
func TestAnImmediateUpgradeIsAskedForExplicitlyAndLoggedFirst(t *testing.T) {
	t.Parallel()
	fake := &fakeDeployment{status: enabledStatus()}
	roster, rec := tools(t, fake)

	got := roster["upgrade_deployment"].Handler(context.Background(),
		map[string]any{"branch": "main", "after_tasks_finish": false})
	if got.IsError {
		t.Fatalf("upgrade_deployment = %+v, want it accepted", got)
	}
	if fake.startedAfterTasksFinish {
		t.Error("after_tasks_finish false was not passed on")
	}
	if !strings.Contains(got.Text, "discarded") {
		t.Errorf("the answer does not say what an immediate upgrade costs:\n%s", got.Text)
	}
	if len(rec.lines) == 0 || !strings.Contains(rec.lines[0], "immediately") {
		t.Errorf("the journal's first line does not record the immediate upgrade before it began:\n%s",
			rec.joined())
	}
}

// A branch is required where the deployment follows no channel to
// default to, and a non-boolean mode is a bad argument rather
// than a silent reading of it as one or the other -- "after_tasks_finish"
// is the difference between a free upgrade and a destructive one.
func TestUpgradingRefusesArgumentsItCannotRead(t *testing.T) {
	t.Parallel()
	fake := &fakeDeployment{status: enabledStatus()}
	roster, _ := tools(t, fake)

	if got := roster["upgrade_deployment"].Handler(context.Background(), map[string]any{}); !got.IsError {
		t.Errorf("upgrade_deployment with no branch = %+v, want a refusal", got)
	}
	got := roster["upgrade_deployment"].Handler(context.Background(),
		map[string]any{"branch": "main", "after_tasks_finish": "no"})
	if !got.IsError {
		t.Errorf("after_tasks_finish \"no\" = %+v, want a refusal", got)
	}
	if fake.starts != 0 {
		t.Errorf("StartUpgrade was called %d times for arguments that were refused", fake.starts)
	}
}

// With no branch named, the upgrade goes to the channel the deployment
// already follows, and the question a human approves says which
// (grain/task-3689): a deployment on latest is moved to the next
// release, not to main's head.
func TestUpgradingWithNoBranchFollowsTheChannel(t *testing.T) {
	t.Parallel()
	status := enabledStatus()
	status.Channel = "latest"
	fake := &fakeDeployment{status: status}
	var asked string
	roster := Tools(Config{
		TaskID:     "335",
		Deployment: fake,
		Log:        func(string, ...any) {},
		Confirm: func(_ context.Context, question string) (bool, string, error) {
			asked = question
			return true, "", nil
		},
	})
	byName := map[string]mcp.Tool{}
	for _, tool := range roster {
		byName[tool.Name] = tool
	}
	got := byName["upgrade_deployment"].Handler(context.Background(), map[string]any{})
	if got.IsError {
		t.Fatalf("upgrade_deployment with no branch on a latest deployment = %+v, want it scheduled", got)
	}
	if fake.startedBranch != "latest" || !fake.startedAfterTasksFinish {
		t.Errorf("StartUpgrade(%q, %v), want the deployment's channel, latest, after tasks finish",
			fake.startedBranch, fake.startedAfterTasksFinish)
	}
	if !strings.Contains(asked, "`latest`") {
		t.Errorf("the confirmation does not name the channel it resolved to:\n%s", asked)
	}
	if text := renderStatus(fake.status); !strings.Contains(text, "follows the latest channel") {
		t.Errorf("the status does not name the channel:\n%s", text)
	}
}

func TestCancellingSaysWhetherThereWasAnythingToCancel(t *testing.T) {
	t.Parallel()
	status := enabledStatus()
	status.Scheduled = &Scheduled{Branch: "main", RequestedAt: time.Now().UTC(), ByID: "335"}
	fake := &fakeDeployment{status: status}
	roster, _ := tools(t, fake)

	got := roster["cancel_deployment_upgrade"].Handler(context.Background(), nil)
	if got.IsError || !strings.Contains(got.Text, "called off") {
		t.Fatalf("cancel_deployment_upgrade = %+v, want it to report the cancellation", got)
	}
	if !strings.Contains(got.Text, "No upgrade is scheduled") {
		t.Errorf("the answer does not report the state it left behind:\n%s", got.Text)
	}

	again := roster["cancel_deployment_upgrade"].Handler(context.Background(), nil)
	if again.IsError {
		t.Errorf("cancelling nothing = %+v, want it to be no error", again)
	}
	if !strings.Contains(again.Text, "no upgrade scheduled") {
		t.Errorf("cancelling nothing does not say so:\n%s", again.Text)
	}
}

// A deployment with no upgrader at all refuses in words, and
// deliberately not as an error: it is a fact about how the deployment
// was installed, and an agent told "error" retries.
func TestBothWritesRefuseInWordsWithNoUpgraderConfigured(t *testing.T) {
	t.Parallel()
	// GET's own answer for it is enabled false, not an error.
	roster, _ := tools(t, &fakeDeployment{status: Status{Enabled: false}})
	got := roster["deployment_upgrade_status"].Handler(context.Background(), nil)
	if got.IsError {
		t.Errorf("deployment_upgrade_status on a deployment that cannot upgrade = error: %s", got.Text)
	}
	if !strings.Contains(got.Text, "-upgrade-image") {
		t.Errorf("the refusal does not say what is missing:\n%s", got.Text)
	}

	// POST and DELETE answer 404, which reaches these tools as the
	// sentinel.
	refusing, _ := tools(t, &fakeDeployment{err: fmt.Errorf("%w: 404", ErrNotConfigured)})
	for _, name := range []string{"deployment_upgrade_status", "upgrade_deployment", "cancel_deployment_upgrade"} {
		got := refusing[name].Handler(context.Background(), map[string]any{"branch": "main"})
		if got.IsError {
			t.Errorf("%s = error on a deployment with no upgrader: %s", name, got.Text)
		}
		if !strings.Contains(got.Text, "cannot upgrade itself") {
			t.Errorf("%s does not explain itself:\n%s", name, got.Text)
		}
	}
}

// Anything else that goes wrong is an error, because a retry is the
// right response to it.
func TestARealFailureIsAnError(t *testing.T) {
	t.Parallel()
	roster, rec := tools(t, &fakeDeployment{err: errors.New("connection refused")})
	got := roster["deployment_upgrade_status"].Handler(context.Background(), nil)
	if !got.IsError {
		t.Errorf("a failed read = %+v, want an error", got)
	}
	if !strings.Contains(rec.joined(), "connection refused") {
		t.Errorf("the failure was not logged:\n%s", rec.joined())
	}
}

// A run with no route back to its daemon still has all three tools, each
// refusing with a sentence -- a roster is a property of the grants a
// task holds, not of how the process serving it was started.
func TestTheToolsAreOnTheRosterWithNoDeploymentToAsk(t *testing.T) {
	t.Parallel()
	roster, _ := tools(t, nil)
	for name, tool := range roster {
		got := tool.Handler(context.Background(), map[string]any{"branch": "main"})
		if !got.IsError || !strings.Contains(got.Text, "cannot reach the grain daemon") {
			t.Errorf("%s with no daemon = %+v, want a refusal naming the missing route", name, got)
		}
	}
}

// The grant an operator ticks, and the sentence they read before ticking
// it: one description, in the provider's spec and in the picker's row
// both (ui.OfferedCapabilities reads this constant).
func TestTheSpecNamesTheCapabilityAndSaysWhatItCosts(t *testing.T) {
	t.Parallel()
	spec := New().Spec()
	if spec.Name != "self-upgrade" {
		t.Errorf("Spec().Name = %q", spec.Name)
	}
	if spec.Description != Description {
		t.Errorf("Spec().Description = %q, want the shared constant", spec.Description)
	}
	if !strings.Contains(Description, "restarts grain") {
		t.Errorf("the description does not say that an upgrade restarts grain: %q", Description)
	}
}

func TestConfirmApprovedRunsAfterAHumanApproves(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	done := make(chan struct{})
	var approved bool
	var reply string
	go func() {
		approved, reply, _ = Confirm(ctx, store, taskID, "upgrade to main?", 20*time.Millisecond, 30*time.Second)
		close(done)
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "approve, go ahead")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Confirm did not return in time")
	}
	if !approved {
		t.Fatalf("approved = false, reply = %q, want true", reply)
	}
}

func TestConfirmDeniedAfterAHumanDenies(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	done := make(chan struct{})
	var approved bool
	var reply string
	go func() {
		approved, reply, _ = Confirm(ctx, store, taskID, "upgrade to main?", 20*time.Millisecond, 30*time.Second)
		close(done)
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "deny, that branch is untested")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Confirm did not return in time")
	}
	if approved {
		t.Fatal("approved = true, want false")
	}
	if !strings.Contains(reply, "untested") {
		t.Fatalf("reply = %q, want it to carry the human's own words", reply)
	}
}

func TestConfirmDeniesOnTimeoutWithNoReply(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	approved, reply, err := Confirm(ctx, store, taskID, "upgrade to main?", 10*time.Millisecond, 60*time.Millisecond)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if approved {
		t.Fatal("approved = true, want false on timeout")
	}
	if !strings.Contains(reply, "timeout") {
		t.Fatalf("reply = %q, want it to mention the timeout", reply)
	}

	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Fatal("PendingQuestionCommentID left set after a timeout, want it cleared")
	}
}

func TestConfirmIgnoresNonHumanCommentsAndItsOwnQuestion(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	done := make(chan struct{})
	var approved bool
	go func() {
		approved, _, _ = Confirm(ctx, store, taskID, "upgrade to main?", 10*time.Millisecond, 30*time.Second)
		close(done)
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: taskID}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: agentPrincipal},
		Body:      "approve",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	// Confirm should still be waiting
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Confirm stopped waiting after an agent comment, want it to wait for a human")
	default:
	}

	humanReply(t, store, ctx, taskID, "approve")
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Confirm did not finish after human approved")
	}
	if !approved {
		t.Fatal("approved = false, want true")
	}
}

func TestUpgradingRequiresInteractiveHumanConfirmation(t *testing.T) {
	t.Parallel()

	// 1. Non-interactive task with no confirm handler and no store
	t.Run("non-interactive task is refused", func(t *testing.T) {
		fake := &fakeDeployment{status: enabledStatus()}
		rec := &recorder{}
		roster := map[string]mcp.Tool{}
		for _, tool := range Tools(Config{TaskID: "100", Deployment: fake, Log: rec.log}) {
			roster[tool.Name] = tool
		}
		got := roster["upgrade_deployment"].Handler(context.Background(), map[string]any{"branch": "main"})
		if !got.IsError {
			t.Fatalf("non-interactive upgrade = %+v, want error", got)
		}
		if !strings.Contains(got.Text, "requires interactive human confirmation") {
			t.Errorf("error text does not explain missing confirmation:\n%s", got.Text)
		}
		if fake.starts != 0 {
			t.Fatalf("StartUpgrade was called %d times, want 0", fake.starts)
		}
	})

	// 2. Interactive task where human denies
	t.Run("denied upgrade does not start", func(t *testing.T) {
		fake := &fakeDeployment{status: enabledStatus()}
		rec := &recorder{}
		roster := map[string]mcp.Tool{}
		for _, tool := range Tools(Config{
			TaskID:     "100",
			Deployment: fake,
			Confirm: func(ctx context.Context, question string) (bool, string, error) {
				return false, "denied: bad timing", nil
			},
			Log: rec.log,
		}) {
			roster[tool.Name] = tool
		}
		got := roster["upgrade_deployment"].Handler(context.Background(), map[string]any{"branch": "main"})
		if !got.IsError {
			t.Fatalf("denied upgrade = %+v, want error", got)
		}
		if !strings.Contains(got.Text, "denied") {
			t.Errorf("error text does not mention denial:\n%s", got.Text)
		}
		if fake.starts != 0 {
			t.Fatalf("StartUpgrade was called %d times on denied upgrade, want 0", fake.starts)
		}
	})

	// 3. Interactive task where confirmation errors
	t.Run("confirmation error does not start upgrade", func(t *testing.T) {
		fake := &fakeDeployment{status: enabledStatus()}
		rec := &recorder{}
		roster := map[string]mcp.Tool{}
		for _, tool := range Tools(Config{
			TaskID:     "100",
			Deployment: fake,
			Confirm: func(ctx context.Context, question string) (bool, string, error) {
				return false, "", errors.New("network error")
			},
			Log: rec.log,
		}) {
			roster[tool.Name] = tool
		}
		got := roster["upgrade_deployment"].Handler(context.Background(), map[string]any{"branch": "main"})
		if !got.IsError {
			t.Fatalf("errored confirmation upgrade = %+v, want error", got)
		}
		if !strings.Contains(got.Text, "could not get confirmation") {
			t.Errorf("error text does not mention confirmation failure:\n%s", got.Text)
		}
		if fake.starts != 0 {
			t.Fatalf("StartUpgrade was called %d times on confirmation error, want 0", fake.starts)
		}
	})

	// 4. Interactive task where human approves
	t.Run("approved upgrade starts", func(t *testing.T) {
		fake := &fakeDeployment{status: enabledStatus()}
		rec := &recorder{}
		roster := map[string]mcp.Tool{}
		for _, tool := range Tools(Config{
			TaskID:     "100",
			Deployment: fake,
			Confirm: func(ctx context.Context, question string) (bool, string, error) {
				if !strings.Contains(question, "main") {
					t.Errorf("question does not mention branch main: %s", question)
				}
				return true, "approved", nil
			},
			Log: rec.log,
		}) {
			roster[tool.Name] = tool
		}
		got := roster["upgrade_deployment"].Handler(context.Background(), map[string]any{"branch": "main"})
		if got.IsError {
			t.Fatalf("approved upgrade = %+v, want success", got)
		}
		if fake.starts != 1 || fake.startedBranch != "main" {
			t.Fatalf("StartUpgrade called %d times with branch %q, want 1 with main", fake.starts, fake.startedBranch)
		}
	})
}

func TestDecide(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reply    string
		approved bool
	}{
		{"approve", true},
		{"Approve, looks good", true},
		{"YES", true},
		{"yes please", true},
		{"deny", false},
		{"Denied by admin", false},
		{"refuse", false},
		{"what does this do?", false},
		{"", false},
	} {
		if got := Decide(tc.reply); got != tc.approved {
			t.Errorf("Decide(%q) = %v, want %v", tc.reply, got, tc.approved)
		}
	}
}
