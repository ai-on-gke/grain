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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGateObservationSelectionIsBoundedAndOffByDefault(t *testing.T) {
	for _, raw := range []string{"a,b,a", "../secret", "1,,2", strings.Repeat("1,", 33)} {
		if _, err := ui.ObservationSelection(raw); err == nil {
			t.Errorf("admitted %q", raw)
		}
	}
	ids, err := ui.ObservationSelection(" 1, 2 ")
	if err != nil || len(ids) != 2 {
		t.Fatalf("selection %v %v", ids, err)
	}
	ids, err = ui.ObservationSelection("")
	if err != nil || len(ids) != 0 {
		t.Fatal("not off by default")
	}
}

func TestGateObservationsOrdinaryAPIReportReceiptAndLegacyReply(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Find the cause", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	read := func() ui.TaskDetail {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != 200 {
			t.Fatalf("GET %d %s", code, body)
		}
		var d ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if read().GateObservations != nil {
		t.Fatal("default enabled")
	}
	if err = c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error { t.Intent = model.IntentAnalyze; return nil }); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	if err := c.Store.StartRun(ctx, model.Run{ID: "shadow-api-run", TaskID: task.ID, Attempt: 1, StartedAt: baseTime, Sandbox: "test"}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}}, Body: "Sensitive report body: do not copy into diagnostics", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Store.FinishRun(ctx, "shadow-api-run", now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err = c.Store.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }); err != nil {
		t.Fatal(err)
	}
	d := read()
	if d.GateObservations == nil || d.GateObservations.Results[0].Result.Status != "met" || d.GateObservations.Results[1].Result.Status != "pending" {
		t.Fatalf("report observations %+v", d.GateObservations)
	}
	// Export actual persisted observations for canonical schema and browser checks.
	if path := os.Getenv("GRAIN_TEST_GATE_FIXTURE"); path != "" {
		b, _ := json.MarshalIndent(map[string]any{"gate_observations": d.GateObservations, "gate_observation_diagnostics": d.GateObservationDiagnostics}, "", "  ")
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(d.GateObservationDiagnostics)
	if strings.Contains(string(b), "Sensitive report") {
		t.Fatal("diagnostics leaked body")
	}
	// UI pagination is not the gate's evidence collection. Put the report
	// outside the visible page and poll cursor; it must remain complete.
	for range 3 {
		_, err = c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}}, Body: "Standing handover", Handover: true, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	var source string
	for _, query := range []string{"?comments_limit=1&attempts_limit=1", "?comments_since=" + now.Format(time.RFC3339Nano)} {
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID+query, "")
		if code != http.StatusOK || strings.Contains(body, "Sensitive report") {
			t.Fatalf("paged read: %d %s", code, body)
		}
		var page ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		if page.GateObservations == nil || page.GateObservations.Results[0].Result.Status != "met" || !page.GateObservations.Results[0].Result.Dependencies[0].Complete {
			t.Fatal("pagination truncated gate evidence")
		}
		if source != "" && page.GateObservations.SourceRevision != source {
			t.Fatal("page/cursor changed the gate source fence")
		}
		source = page.GateObservations.SourceRevision
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP})
	now = now.Add(time.Second)
	if err = c.Close(person, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	d = read()
	if d.GateObservations.Results[1].Result.Status != "met" {
		t.Fatalf("receipt %+v", d.GateObservations.Results[1])
	}
	if err = c.Reopen(person, task.ID); err != nil {
		t.Fatal(err)
	}
	// Legacy person reply remains owned by AddComment and requeues work.
	err = c.AddComment(person, task.ID, "Please investigate another case", nil)
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.Store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.CompletedAt != nil {
		t.Fatal("legacy reply did not clear completion")
	}
	d = read()
	if d.GateObservations.Results[1].Result.Status == "met" {
		t.Fatal("reply became receipt")
	}
	c.Config.GateObservationTaskIDs = []string{}
	if read().GateObservations != nil {
		t.Fatal("disable left sidecar visible")
	}
}

func TestGateObservationsRespectTaskAuthorization(t *testing.T) {
	srv, c := testServer(t)
	seedAPITask(t, c, "caller-observe", "acme/widgets", "api:tasks:read@acme/widgets")
	seedAPITask(t, c, "visible-observe", "acme/widgets", "")
	seedAPITask(t, c, "hidden-observe", "acme/private", "")
	c.Config.GateObservationTaskIDs = []string{"visible-observe", "hidden-observe"}
	code, body := asAgent(t, srv, "caller-observe", http.MethodGet, "/api/tasks/visible-observe", "", "")
	if code != 200 || !strings.Contains(body, "gate_observations") {
		t.Fatalf("authorized %d %s", code, body)
	}
	code, body = asAgent(t, srv, "caller-observe", http.MethodGet, "/api/tasks/hidden-observe", "", "")
	if code == 200 || strings.Contains(body, "gate_observations") {
		t.Fatalf("hidden observation leaked %d %s", code, body)
	}
}

// Main's batch cache can have a stable domain token while a diagnostic lease
// expires. Never return that cached body for a selected observation task.
func TestGateObservationSummariesBypassStableDomainCache(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Observe a task", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/summaries?ids=" + task.ID + "&comments_limit=1&attempts_limit=1"
	code, body := serve(t, srv, http.MethodGet, path, "")
	if code != 200 || strings.Contains(body, "gate_observations") {
		t.Fatalf("unselected %d %s", code, body)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	now := baseTime
	c.Now = func() time.Time { return now }
	code, body = serve(t, srv, http.MethodGet, path, "")
	if code != 200 || !strings.Contains(body, "gate_observations") {
		t.Fatalf("selected cached %d %s", code, body)
	}
	var envelope struct {
		Tasks map[string]ui.TaskDetail `json:"tasks"`
	}
	if err = json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	initial := envelope.Tasks[task.ID].GateObservations.ComparedAt
	// A coalesced read then leases the same stable domain state.
	code, _ = serve(t, srv, http.MethodGet, path, "")
	if code != 200 {
		t.Fatal(code)
	}
	now = now.Add(2 * time.Minute)
	code, body = serve(t, srv, http.MethodGet, path, "")
	if code != 200 {
		t.Fatalf("expired %d %s", code, body)
	}
	if err = json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Tasks[task.ID].GateObservations.ComparedAt.After(initial) {
		t.Fatal("batch cache hid expired observation lease")
	}
}

// A diagnostic storage outage must neither fail the ordinary task API nor
// disappear as an apparently passing/complete observation.
func TestGateObservationStorageOutageIsAnInertSidecar(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	c := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), GateObservationTaskIDs: []string{}}, store)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Observe through an outage", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID, "missing-observation-task"}
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	srv := ui.NewServerWithClient(c)
	read := func() ui.TaskDetail {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != http.StatusOK {
			t.Fatalf("ordinary task read failed: %d %s", code, body)
		}
		if strings.Contains(body, "no such table") || strings.Contains(body, "does not exist") {
			t.Fatal("raw infrastructure error escaped")
		}
		var detail ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	initial := read()
	before, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wake := store.Changed()
	if _, err := db.ExecContext(ctx, "DROP TABLE `gate_observation_shadow`"); err != nil {
		t.Fatal(err)
	}
	failed := read()
	v := failed.GateObservations
	if v == nil || !v.Dirty || v.Authority != "legacy" || v.LifecycleEffects || v.ActionEffects || v.DispatchEffects || v.AutomaticCutover || failed.State != initial.State {
		t.Fatalf("outage affected ordinary task state: %+v", failed)
	}
	for _, entry := range v.Results {
		if entry.Disposition != "rejected" || entry.Result.Status != "error" || entry.AcceptedAt != nil || len(entry.Result.Evidence) != 0 || entry.Result.Dependencies[0].Complete || len(entry.Result.Work) != 0 || len(entry.Result.Actions) != 0 || len(entry.Result.Waits) != 0 {
			t.Fatalf("unsafe unavailable projection: %+v", entry)
		}
	}
	if path := os.Getenv("GRAIN_TEST_GATE_ERROR_FIXTURE"); path != "" {
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	after, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("outage changed lifecycle/failure/question state")
	}
	select {
	case <-wake:
		t.Fatal("outage woke dispatch")
	default:
	}
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/missing-observation-task", "")
	if code != http.StatusNotFound || strings.Contains(body, "gate_observations") {
		t.Fatalf("missing task projection: %d %s", code, body)
	}
	// Repairing diagnostic storage resumes collection without touching task
	// lifecycle or turning an outage into an agent failure/retry.
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	recovered := read()
	if recovered.GateObservations == nil || recovered.GateObservations.Dirty || recovered.State != initial.State {
		t.Fatal("diagnostic recovery changed task state")
	}
}

func TestReviewApprovalObservationsPreserveAuthorizationAndSubmit(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seedAPITask(t, c, "review-observer", "acme/widgets", "api:tasks:read@acme/widgets")
	seedAPITask(t, c, "review-owner", "acme/widgets", "")
	seedAPITask(t, c, "review-hidden", "acme/private", "")
	must(c.Store.UpdateTask(ctx, "review-hidden", func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkReviewerHead, Target: "DO-NOT-EXPOSE-PRIVATE-HEAD"})
		return nil
	}))
	must(c.Store.UpdateTask(ctx, "review-owner", func(t *model.Task) error {
		t.AutoMerge = false
		t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"}, model.Link{Kind: model.LinkReviewTask, Target: "review-hidden"}, model.ApprovalRequiredLink("trigger:owners", []string{"alice"}), model.ReviewFindingsRecord{Gate: model.GateReview, ReviewTask: "review-hidden", HeadSHA: "head-1", Summary: "Needs a deliberate disposition", At: baseTime}.Link())
		return nil
	}))
	c.Config.GateObservationTaskIDs = []string{"review-owner", "review-hidden"}
	read := func() ui.TaskDetail {
		t.Helper()
		code, body := asAgent(t, srv, "review-observer", http.MethodGet, "/api/tasks/review-owner?comments_limit=1&attempts_limit=1", "", "")
		if code != http.StatusOK || strings.Contains(body, "DO-NOT-EXPOSE-PRIVATE-HEAD") {
			t.Fatalf("authorized scoped read: %d %s", code, body)
		}
		var detail ui.TaskDetail
		must(json.Unmarshal([]byte(body), &detail))
		if detail.GateObservations == nil || len(detail.GateObservations.Results) < 5 {
			t.Fatal("missing scoped review/approval sidecar")
		}
		for i, b := range detail.GateObservations.Bindings {
			if (b.Type == "approval" || b.Type == "review" && !b.Origin.Imported) && detail.GateObservations.Results[i].Result.Status == "met" {
				t.Fatal("legacy record became current-head proof")
			}
		}
		return detail
	}
	before := read()
	if path := os.Getenv("GRAIN_TEST_REVIEW_GATE_FIXTURE"); path != "" {
		_, wire := serve(t, srv, http.MethodGet, "/api/tasks/review-owner", "")
		var unrestricted ui.TaskDetail
		must(json.Unmarshal([]byte(wire), &unrestricted))
		b, _ := json.MarshalIndent(map[string]any{"gate_observations": unrestricted.GateObservations, "gate_observation_diagnostics": unrestricted.GateObservationDiagnostics}, "", "  ")
		must(os.WriteFile(path, b, 0600))
	}
	code, body := asAgent(t, srv, "review-observer", http.MethodGet, "/api/tasks/review-hidden", "", "")
	if code == http.StatusOK || strings.Contains(body, "gate_observations") {
		t.Fatalf("hidden subject exposed observations: %d %s", code, body)
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP})
	// A shadow unknown neither blocks the existing person-only Submit service nor
	// grants it the authority to accept findings or finish a review.
	must(c.Submit(person, "review-owner"))
	task, err := c.Store.GetTask(ctx, "review-owner")
	must(err)
	if len(model.PendingApprovals(*task)) != 0 {
		t.Fatal("legacy Submit stopped owning named approvals")
	}
	if len(model.ReviewFindings(*task)) != 1 {
		t.Fatal("Submit/observation cleared findings")
	}
	if len(model.ReviewFindingsAcceptances(*task)) != 0 {
		t.Fatal("Submit/observation issued acceptance")
	}
	after := read()
	if after.GateObservations.SourceRevision == before.GateObservations.SourceRevision {
		t.Fatal("Submit did not invalidate the diagnostic source")
	}
}

func TestGateObservationHistoryRequiresRepositoryTaskRead(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	seedAPITask(t, c, "self-only-observe", "acme/widgets", "")
	seedAPITask(t, c, "same-repo-review", "acme/widgets", "")
	if err := c.Store.UpdateTask(ctx, "self-only-observe", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#43"}, model.Link{Kind: model.LinkReviewTask, Target: "same-repo-review"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{"self-only-observe"}
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/self-only-observe", "")
	if code != http.StatusOK || !strings.Contains(body, "gate_observations") {
		t.Fatalf("privileged collection failed: %d %s", code, body)
	}
	checkSelf := func() {
		t.Helper()
		code, body := asAgent(t, srv, "self-only-observe", http.MethodGet, "/api/tasks/self-only-observe", "", "")
		if code != http.StatusForbidden {
			t.Fatalf("caller without tasks:read was not refused: %d %s", code, body)
		}
		if strings.Contains(body, "gate_observations") || strings.Contains(body, "gate_observation_diagnostics") {
			t.Fatal("implicit self-read disclosed repository-wide linked observations/history")
		}
	}
	checkSelf()
	// Dropping today's change/review links cannot make older linked history safe
	// for a caller whose only permission is its implicit self-task read.
	if err := c.Store.UpdateTask(ctx, "self-only-observe", func(tk *model.Task) error { tk.Links = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	checkSelf()
}

func TestScopedGateObservationsDoNotReadFormerRepositoryHistory(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	seedAPITask(t, c, "scope-mover", "acme/old", "")
	seedAPITask(t, c, "old-review", "acme/old", "")
	seedAPITask(t, c, "new-repo-observer", "acme/new", "api:tasks:read@acme/new")
	if err := c.Store.UpdateTask(ctx, "scope-mover", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/old#1"}, model.Link{Kind: model.LinkReviewTask, Target: "old-review"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{"scope-mover"}
	code, body := serve(t, srv, http.MethodGet, "/api/tasks/scope-mover", "")
	if code != http.StatusOK || !strings.Contains(body, "gate_observation_diagnostics") {
		t.Fatalf("initial unrestricted history: %d %s", code, body)
	}
	if err := c.Store.UpdateTask(ctx, "scope-mover", func(tk *model.Task) error {
		tk.Target = &model.RepoRef{Owner: "acme", Name: "new"}
		tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/new#2"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, body = asAgent(t, srv, "new-repo-observer", http.MethodGet, "/api/tasks/scope-mover", "", "")
	if code != http.StatusOK {
		t.Fatalf("new repository read: %d %s", code, body)
	}
	var scoped ui.TaskDetail
	if err := json.Unmarshal([]byte(body), &scoped); err != nil {
		t.Fatal(err)
	}
	if scoped.GateObservations == nil || scoped.GateObservationDiagnostics != nil {
		t.Fatal("scoped reader lost current prediction or received transitive history")
	}
	for _, b := range scoped.GateObservations.Bindings {
		if b.Subject.Kind == "change" && b.Subject.Repository != "acme/new" {
			t.Fatal("former resource scope replayed to a restricted reader")
		}
	}
	// The current-only store path must not depend on history reads even when
	// diagnostic history is unreadable. (Writes/retention retain ordinary budgets.)
	denied := model.WithExternalRefReadable(ctx, func(string) bool { return false })
	if _, err := c.Store.ObserveTaskGatePredictions(denied, "scope-mover", baseTime); err != model.ErrGateObservationScopeDenied {
		t.Fatalf("collection did not enforce repository scope: %v", err)
	}
}

type qualificationReadSpy struct{ heads int }

func (s *qualificationReadSpy) GetPullRequest(_, _ string, _ int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{}, nil
}
func (s *qualificationReadSpy) GetBranchHead(_, _, _ string) (*github.BranchHead, error) {
	s.heads++
	return &github.BranchHead{SHA: strings.Repeat("a", 40)}, nil
}

func TestQualificationObservationsAuthorizeBeforeSourceAndCoalesceBackoff(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	seedAPITask(t, c, "qualification-caller", "acme/widgets", "api:tasks:read@acme/widgets")
	seedAPITask(t, c, "qualification-visible", "acme/widgets", "")
	seedAPITask(t, c, "qualification-hidden", "acme/private", "")
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	cfg.QA = model.QAConfig{Enabled: true, FlowID: model.QAFlowGoldenV3, RolloutPct: 100, Epoch: 1, RegimeID: 1}
	if err = c.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"qualification-visible", "qualification-hidden"} {
		if err = c.Store.UpdateTask(ctx, id, func(t *model.Task) error { t.QAFlowRef = model.QAFlowGoldenV3; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	spy := &qualificationReadSpy{}
	c.Config.GitHubPullRequests = spy
	now := baseTime
	c.Now = func() time.Time { return now }
	code, body := asAgent(t, srv, "qualification-caller", http.MethodGet, "/api/tasks/qualification-visible", "", "")
	if code != 200 || strings.Contains(body, "gate_observations") || spy.heads != 0 {
		t.Fatalf("disabled source collection %d %s", code, body)
	}
	c.Config.GateObservationTaskIDs = []string{"qualification-visible", "qualification-hidden"}
	collection := gates.QualificationCollection{QA: &gates.StageFacts{TaskID: "qualification-visible", FlowRevision: "unavailable"}}
	failure := gates.UnavailableQualifications("qualification-visible", now, collection)
	if err = c.Store.RecordGateQualificationFailure(ctx, "qualification-visible", now, &failure, collection); err != nil {
		t.Fatal(err)
	}
	now = now.Add(100 * time.Millisecond)
	code, body = asAgent(t, srv, "qualification-caller", http.MethodGet, "/api/tasks/qualification-visible", "", "")
	if code != 200 || !strings.Contains(body, "qa_stage") || spy.heads != 0 {
		t.Fatalf("backoff repeated forge collection %d calls=%d %s", code, spy.heads, body)
	}
	code, body = asAgent(t, srv, "qualification-caller", http.MethodGet, "/api/tasks/qualification-hidden", "", "")
	if code == 200 || strings.Contains(body, "gate_observations") || spy.heads != 0 {
		t.Fatal("hidden source or sidecar was read")
	}
	now = now.Add(2 * time.Second)
	code, body = asAgent(t, srv, "qualification-caller", http.MethodGet, "/api/tasks/qualification-visible", "", "")
	if code != 200 || !strings.Contains(body, "qa_stage") || spy.heads == 0 {
		t.Fatalf("expired backoff did not recollect %d %s", code, body)
	}
}
