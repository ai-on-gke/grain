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

package gateobserve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

type readOnlySource struct {
	tagReads, moveTagOnRead int
	head                    string
	err                     error
	calls                   int
	changeAt                int
	tags                    []github.Tag
	checks                  []github.CheckRun
	pending                 string
	conf                    []byte
	comparison              github.Comparison
}

func (s *readOnlySource) GetBranchHead(_, _, branch string) (*github.BranchHead, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.changeAt > 0 && s.calls >= s.changeAt {
		return &github.BranchHead{SHA: strings.Repeat("b", 40)}, nil
	}
	return &github.BranchHead{SHA: s.head}, nil
}
func (s *readOnlySource) ListTags(_, _ string) ([]github.Tag, error) {
	s.tagReads++
	if s.moveTagOnRead > 0 && s.tagReads >= s.moveTagOnRead && len(s.tags) > 0 {
		s.tags[0].SHA = strings.Repeat("b", 40)
	}
	return s.tags, s.err
}

var _ releaseObservationChecks = (*readOnlySource)(nil)
var _ github.ReleaseTagReader = (*readOnlySource)(nil)

func (s *readOnlySource) CheckRunApp(_, _ string) (string, error) { return "grain", s.err }
func (s *readOnlySource) ListCheckRuns(_, _, _ string) ([]github.CheckRun, error) {
	return s.checks, s.err
}

func (s *readOnlySource) ListMatchingTags(_, _, prefix string) ([]github.TagRef, error) {
	if s.pending == "" {
		return nil, s.err
	}
	return []github.TagRef{{Name: strings.TrimSuffix(prefix, "-pending") + "-pending", ObjectSHA: "tag-object", ObjectType: "tag"}}, s.err
}
func (s *readOnlySource) GetAnnotatedTag(_, _, _ string) (*github.AnnotatedTag, error) {
	return &github.AnnotatedTag{Commit: s.head, Message: s.pending}, s.err
}
func (s *readOnlySource) ReadFile(_, _, _, path string) ([]byte, bool, error) {
	return s.conf, len(s.conf) > 0, s.err
}
func (s *readOnlySource) CompareRefs(_, _, _, _ string) (github.Comparison, error) {
	return s.comparison, s.err
}

// No mutation, CI, dispatch, approval, agent or promotion methods exist on this source.

func testStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := model.New(db)
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func qaSourceTask(t *testing.T, s *model.Store, id, arm string) (model.Task, *resolve.Resolution) {
	t.Helper()
	ctx := context.Background()
	cfg, err := s.GetConfig(ctx)
	require(t, err)
	next := model.Config{}
	if cfg != nil {
		next = *cfg
	}
	next.QA = model.QAConfig{Enabled: true, FlowID: model.QAFlowGoldenV3, RolloutPct: 100, ShadowOnControl: model.BoolPtr(true), Epoch: 1, RegimeID: 1}
	require(t, s.PutConfig(ctx, next))
	repo := model.RepoRef{Owner: "acme", Name: "grain"}
	task := model.Task{ID: id, Target: &repo, Intent: model.IntentImplement, Title: "Observe QA", AutoMerge: true}
	require(t, s.PutTask(ctx, task))
	_, err = s.EnsureQAAssignment(ctx, model.QAAssignment{RootOrigin: model.ResolveQARootOrigin(task.WorkOrigin, id), Epoch: 1, RegimeID: 1, Arm: arm, FlowID: model.QAFlowGoldenV3, RolloutPctAtAssign: 100})
	require(t, err)
	res, _, err := resolve.ActiveQAResolutionForTask(ctx, s, id)
	require(t, err)
	if res == nil || res.Spec == nil || res.FlowHash == "" {
		t.Fatalf("unpinned fixture %+v", res)
	}
	return task, res
}
func recordS3(t *testing.T, s *model.Store, task model.Task, res *resolve.Resolution, head string) {
	t.Helper()
	for _, rule := range res.Spec.Rules {
		if !rule.Enabled || !(strings.EqualFold(rule.Stage, "S3") || strings.EqualFold(rule.Stage, "S3.5")) {
			continue
		}
		_, err := s.RecordQACheckRun(context.Background(), model.QACheckRun{TaskID: task.ID, CommitSHA: head, Stage: rule.Stage, CheckID: rule.ID, Arm: res.ArmID, ArmID: res.ArmID, Shadow: res.Shadow, FlowHash: res.FlowHash, ExperimentID: res.ExperimentID, QAEngineRev: res.QAEngineRev, Verdict: "pass", BlastTier: "T1"}, nil)
		require(t, err)
	}
	for _, check := range res.Spec.CustomChecks {
		_, err := s.RecordQACheckRun(context.Background(), model.QACheckRun{TaskID: task.ID, CommitSHA: head, Stage: "S3", CheckID: check.ID, Arm: res.ArmID, ArmID: res.ArmID, Shadow: res.Shadow, FlowHash: res.FlowHash, ExperimentID: res.ExperimentID, QAEngineRev: res.QAEngineRev, Verdict: "pass", BlastTier: "T1"}, nil)
		require(t, err)
	}
	for _, stage := range res.Spec.CustomStages {
		_, err := s.RecordQACheckRun(context.Background(), model.QACheckRun{TaskID: task.ID, CommitSHA: head, Stage: stage.ID, CheckID: stage.ID, Arm: res.ArmID, ArmID: res.ArmID, Shadow: res.Shadow, FlowHash: res.FlowHash, ExperimentID: res.ExperimentID, QAEngineRev: res.QAEngineRev, Verdict: "pass", BlastTier: "T1"}, nil)
		require(t, err)
	}

}
func TestQualificationObservationQASourceEventsAndNoninterference(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	task, res := qaSourceTask(t, s, "qa-observe", "treatment")
	src := &readOnlySource{head: strings.Repeat("a", 40)}
	recordS3(t, s, task, res, src.head)
	read := func(want string) gates.QualificationCollection {
		t.Helper()
		before, err := s.GetTask(ctx, task.ID)
		require(t, err)
		changed := s.Changed()
		c, err := CollectQualificationObservations(ctx, s, src, task.ID, now)
		require(t, err)
		if c.QA == nil {
			t.Fatal("missing QA projection")
		}
		b := c.Bindings(task.ID)[2]
		r := c.Evaluate(b, now, 1)
		if r.Status != want {
			t.Fatalf("got %s want %s: %s %+v source=%+v", r.Status, want, r.Summary, r.Blockers, c.QA)
		}
		after, err := s.GetTask(ctx, task.ID)
		require(t, err)
		if gates.Digest(before) != gates.Digest(after) {
			t.Fatal("observer mutated task")
		}
		select {
		case <-changed:
			t.Fatal("observer woke legacy scheduling")
		default:
		}
		return c
	}
	first := read("met")
	// Same evidence, a new read clock: no new source event.
	now = now.Add(time.Second)
	again := read("met")
	if first.SourceVersion() != again.SourceVersion() {
		t.Fatal("clock heartbeat invalidates source")
	}
	src.head = strings.Repeat("b", 40)
	read("unknown")
	src.head = strings.Repeat("a", 40)
	// A current-head failure and all its findings remain visible, even when
	// another check passed and a later same-head attempt succeeds.
	check := res.Spec.Rules[0]
	_, err := s.RecordQACheckRun(ctx, model.QACheckRun{TaskID: task.ID, CommitSHA: src.head, Stage: check.Stage, CheckID: check.ID, Arm: res.ArmID, ArmID: res.ArmID, Shadow: res.Shadow, FlowHash: res.FlowHash, QAEngineRev: res.QAEngineRev, Verdict: "fail", BlastTier: "T1"}, []model.QAFinding{{TaskID: task.ID, Stage: check.Stage, RuleID: check.ID, Severity: "blocking", Summary: "authentic source finding"}})
	require(t, err)
	read("unmet")
	recordS3(t, s, task, res, src.head)
	read("unmet")
	src.err = errors.New("denied with sensitive provider text")
	c := read("unknown")
	data, _ := json.Marshal(c)
	if strings.Contains(string(data), "sensitive") {
		t.Fatal("provider error leaked")
	}
	src.err = nil
	src.calls = 0
	src.changeAt = 2
	read("unknown")
	src.changeAt = 0
	cfg, err := s.GetConfig(ctx)
	require(t, err)
	cfg.QA.Epoch = 2
	require(t, s.PutConfig(ctx, *cfg))
	read("unknown")
}
func TestQualificationObservationControlCannotBecomeTreatment(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	task, res := qaSourceTask(t, s, "qa-control", "control")
	src := &readOnlySource{head: strings.Repeat("a", 40)}
	recordS3(t, s, task, res, src.head)
	c, err := CollectQualificationObservations(ctx, s, src, task.ID, now)
	require(t, err)
	b := c.Bindings(task.ID)[2]
	r := c.Evaluate(b, now, 1)
	if r.Status != "met" || !strings.Contains(r.Summary, "Control/shadow") {
		t.Fatalf("shadow observation %+v", r)
	}
	b.Required = true
	b.Purpose = "guard"
	if r := c.Evaluate(b, now, 1); r.Status != "unknown" {
		t.Fatal("control gained authority")
	}
}
func TestQualificationObservationReleaseUsesExistingAuthenticatedVerdict(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	repo := model.RepoRef{Owner: "acme", Name: "grain"}
	template := model.Template{ID: "qualify", Name: "Qualify", Title: "Qualify", Body: "Check release"}
	require(t, s.PutTemplate(ctx, template))
	suite := model.Suite{ID: model.RepoDeclaredID(model.DeclaredSuite, repo, model.ReleaseQualificationSuiteStem), Name: "Qualification", Mode: model.SuiteUntilClean, MaxPasses: 1, Items: []model.SuiteItem{{TemplateID: template.ID}}}
	require(t, s.PutSuite(ctx, suite))
	candidate := "v3.1.0-rc.1"
	head := strings.Repeat("a", 40)
	tag := model.ReleaseQualificationTag(repo, candidate)
	run, err := s.CreateSuiteRunWith(ctx, suite, repo, "main", model.SuiteRunOptions{Tag: tag}, now)
	require(t, err)
	if len(run.Tasks) != 1 {
		t.Fatal("missing source suite task")
	}
	id := run.Tasks[0].TaskID
	success := "success"
	src := &readOnlySource{head: head, tags: []github.Tag{{Name: candidate, SHA: head}}, checks: []github.CheckRun{
		{ID: 99, Name: model.ReleaseQualificationCheckName, HeadSHA: head, App: "foreign", ExternalID: tag + "#9", Status: "completed", Conclusion: &success},
		{ID: 2, Name: model.ReleaseQualificationCheckName, HeadSHA: head, App: "grain", ExternalID: tag + "#2", Status: "completed", Conclusion: &success},
	}, pending: "Qualification: PASS", conf: []byte("RELEASE_MAJOR=3\n"), comparison: github.Comparison{Status: "identical", MergeBase: head}}
	read := func() gates.QualificationCollection {
		t.Helper()
		c, err := CollectQualificationObservations(ctx, s, src, id, now)
		require(t, err)
		if c.Release == nil {
			t.Fatal("missing release projection")
		}
		return c
	}
	c := read()
	if path := os.Getenv("GRAIN_RELEASE_OBSERVATION_FIXTURE"); path != "" {
		view, _, err := s.ObserveTaskGatesWithQualifications(ctx, id, now, c)
		require(t, err)
		data, err := json.MarshalIndent(view, "", "  ")
		require(t, err)
		require(t, os.WriteFile(path, data, 0600))
	}
	for _, b := range c.Bindings(id)[2:] {
		r := c.Evaluate(b, now, 1)
		if b.ID == "release-suite" && (r.Status != "met" || c.Release.Suite.ID != "2") {
			t.Fatalf("did not reuse latest authentic attempt %+v", r)
		}
		if b.ID == "release-soak" && r.Status != "met" {
			t.Fatal("qualified candidate wrongly requires soak")
		}
		if b.ID == "release-signing_version" && r.Status != "unknown" {
			t.Fatal("tag text became signature")
		}
	}
	require(t, s.PutReleaseApprovalDecision(ctx, model.ReleaseApprovalDecision{Repo: repo, Version: "v3.1.0", Commit: head, Decision: "approve", Workflow: "promote-release.yml", DecidedBy: "alice", DecidedAt: now}))
	c = read()
	for _, b := range c.Bindings(id)[2:] {
		if b.ID == "release-person" && c.Evaluate(b, now, 1).Status != "unknown" {
			t.Fatal("dispatch row granted approval")
		}
	}
	src.tags[0].SHA = strings.Repeat("b", 40)
	c = read()
	for _, b := range c.Bindings(id)[2:] {
		if b.ID == "release-suite" && c.Evaluate(b, now, 1).Status != "unknown" {
			t.Fatal("old RC verdict survived head change")
		}
	}
	src.tags[0].SHA = head
	failure := "failure"
	src.checks[1].Conclusion = &failure
	c = read()
	for _, b := range c.Bindings(id)[2:] {
		if b.ID == "release-suite" && c.Evaluate(b, now, 1).Status != "unmet" {
			t.Fatal("failed verdict became success")
		}
	}
	src.tags[0].SHA = head
	src.checks[1].Conclusion = &success
	src.tagReads = 0
	src.moveTagOnRead = 2
	c = read()
	if c.Release.Complete {
		t.Fatal("moving RC collection was accepted")
	}
	src.moveTagOnRead = 0
	src.tags[0].SHA = head
	neutral := "neutral"
	src.checks[1].Conclusion = &neutral
	c = read()
	for _, b := range c.Bindings(id)[2:] {
		if b.ID == "release-suite" && c.Evaluate(b, now, 1).Status != "pending" {
			t.Fatal("infra verdict became success")
		}
	}
}
func TestQAGateEvidenceCompletenessAndBounds(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	for i := 0; i < 513; i++ {
		_, err := s.RecordQACheckRun(ctx, model.QACheckRun{TaskID: "bounded", CommitSHA: head, Stage: "S3", CheckID: "check", Verdict: "pass"}, nil)
		require(t, err)
	}
	runs, _, complete, err := s.QAGateEvidence(ctx, "bounded", head)
	require(t, err)
	if complete || len(runs) != 512 {
		t.Fatal("truncated collection claimed complete")
	}
	runs, _, complete, err = s.QAGateEvidence(ctx, "bounded", strings.Repeat("b", 40))
	require(t, err)
	if !complete || len(runs) != 0 {
		t.Fatal("exact-head complete empty scope lost")
	}
}

func TestQualificationObservationConditionalCustomEvidence(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	task, res := qaSourceTask(t, s, "custom-conditional", "treatment")
	spec := *res.Spec
	spec.CustomStages = []qa.CustomStageDecl{{ID: "custom:conditional", Kind: "deterministic_batch", InsertAfter: "s3", WhenPaths: []string{"only/**"}, CheckIDs: []string{"custom:child"}}}
	spec.CustomChecks = []qa.CustomCheckDecl{{ID: "custom:child", Kind: "require_companion_change", Severity: "blocking"}}
	raw, err := json.Marshal(spec)
	require(t, err)
	hash := gates.Digest(spec)
	require(t, s.InsertQAFlowVersion(ctx, model.QAFlowVersion{Hash: hash, Key: "observation-custom", Version: 1, Label: "Observation custom", SpecJSON: string(raw), QAEngineRev: res.QAEngineRev, CreatedBy: "operator", CreatedAt: now}))
	require(t, s.UpdateTask(ctx, task.ID, func(t *model.Task) error { t.QAFlowRef = hash; return nil }))
	res, _, err = resolve.ActiveQAResolutionForTask(ctx, s, task.ID)
	require(t, err)
	src := &readOnlySource{head: strings.Repeat("a", 40)}
	record := func(stage, id, detail string) {
		t.Helper()
		_, err := s.RecordQACheckRun(ctx, model.QACheckRun{TaskID: task.ID, CommitSHA: src.head, Stage: stage, CheckID: id, Arm: res.ArmID, ArmID: res.ArmID, Shadow: res.Shadow, FlowHash: res.FlowHash, QAEngineRev: res.QAEngineRev, Verdict: "pass", BlastTier: "T1", DetailJSON: detail}, nil)
		require(t, err)
	}
	basics := func(detail string) {
		for _, rule := range res.Spec.Rules {
			if rule.Enabled && (strings.EqualFold(rule.Stage, "S3") || strings.EqualFold(rule.Stage, "S3.5")) {
				record(rule.Stage, rule.ID, detail)
			}
		}
	}
	read := func(want string) {
		t.Helper()
		c, err := CollectQualificationObservations(ctx, s, src, task.ID, now)
		require(t, err)
		r := c.Evaluate(c.Bindings(task.ID)[2], now, 1)
		if r.Status != want {
			t.Fatalf("custom evidence %s want %s: %+v", r.Status, want, r.Blockers)
		}
	}
	// Authenticated complete-empty changed paths prove the conditional stage is
	// excluded; its grouped check is not unconditionally required a second time.
	basics("{\"blastTier\":\"T1\",\"changedFiles\":[]}")
	read("met")
	// Missing applicability stays unknown even with all ordinary S3 checks.
	src.head = strings.Repeat("b", 40)
	basics("{}")
	read("unknown")
	// An aggregate pass cannot conceal a missing required child check row.
	record("custom:conditional", "custom:conditional", "{}")
	read("unknown")
	record("S3", "custom:child", "{}")
	read("met")
}
