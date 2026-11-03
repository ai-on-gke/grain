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

package orchestrator

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The dispatch-standing invariant (design/design/durable-tasks.design.md#DT-3): a
// task re-queued after a suspension has exactly the dispatch standing it
// had when the suspended run started, and is started ahead of the queue
// on the next cycle it is eligible for.

// suspensionKindSource is the one file that declares model.SuspensionKind.
const suspensionKindSource = "../model/suspension.go"

// declaredSuspensionKinds reads every constant of type SuspensionKind out
// of suspensionKindSource, so a kind added there with no row here fails.
func declaredSuspensionKinds(t *testing.T) []model.SuspensionKind {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), suspensionKindSource, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", suspensionKindSource, err)
	}
	var kinds []model.SuspensionKind
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if typ, ok := value.Type.(*ast.Ident); !ok || typ.Name != "SuspensionKind" {
				continue
			}
			for i, name := range value.Names {
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not declared as a string literal: this test needs updating", name.Name)
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				kinds = append(kinds, model.SuspensionKind(unquoted))
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("%s declares no SuspensionKind constants", suspensionKindSource)
	}
	return kinds
}

// standingCase is one standing of DT-3's table: how a task comes to have
// it, and how many workers the cycle has. Every case runs with one other
// task's run live, so the pool is full once one more run starts -- or,
// for the standings admitted outside the worker count, full already.
type standingCase struct {
	name    string
	workers int
	give    func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task)
}

var standingCases = []standingCase{
	{name: "run-now", workers: 2, give: func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task) {
		at := standingNow.Add(-time.Minute)
		if err := store.ObserveField(ctx, tk.ID, at, func(o *model.Observation) { o.RunNowAt = &at }); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "priority-dispatch", workers: 1, give: func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task) {
		tk.Grants = append(tk.Grants, model.Grant{Capability: model.PriorityDispatchCapability, Via: model.GrantByLabel})
	}},
	{name: "durable", workers: 1, give: func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task) {
		tk.Grants = append(tk.Grants, model.Grant{Capability: model.DurableCapability, Via: model.GrantByLabel})
	}},
	{name: "front-of-backlog", workers: 2, give: func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task) {
		tk.OrderKey = -1
	}},
	{name: "fixes-base-branch", workers: 2, give: func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task) {
		// A repair is filed at the front of the backlog, which is what
		// orders it; the mark is what must survive alongside.
		tk.Target = &model.RepoRef{Owner: "acme", Name: "widgets"}
		if !model.SetFixesBaseBranch(tk, true) {
			t.Fatal("could not mark the task as a base-branch repair")
		}
		tk.OrderKey = -1
	}},
	{name: "session", workers: 1, give: func(t *testing.T, ctx context.Context, store *model.Store, tk *model.Task) {
		tk.Origin.Reason = model.ReasonConsoleSession
		tk.Tags = append(tk.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
		tk.Interactive = true
		tk.Grants = append(tk.Grants, model.Grant{Capability: model.DurableCapability, Via: model.GrantByGrain})
		if err := store.SetTaskGoal(ctx, tk.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, model.PrincipalString(model.ConsoleSessionPrincipal), standingNow); err != nil {
			t.Fatal(err)
		}
	}},
}

var standingNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// ending is one way to end the run: a suspension kind, ended through the
// real finish path, recordRunEnd -- read off the outcome for the kinds
// that are outcomes of their own (parked is the agent's own exit), named
// by runEnding.suspend for the rest, as Store.Suspend records them -- or
// one of task 837's outcomes that is not a suspension but still
// keeps a Run now request (setup-failed on an ordinary task), which
// counts towards the streak.
//
// The idle and setup rows (DT-S2) are ended by the paths that raise those
// kinds on a durable task, which the subject is made for them: the idle
// watchdog's ending through recordRunEnd, and a setup failure through
// finishSetupFailed.
type ending struct {
	name    string
	kind    model.SuspensionKind
	outcome string
	counts  bool
	// raisedBy is "idle" or "setup" for the rows ended the way grain
	// raises those kinds on a durable task.
	raisedBy string
}

func endings(t *testing.T) []ending {
	var out []ending
	for _, kind := range declaredSuspensionKinds(t) {
		out = append(out, ending{name: string(kind), kind: kind, outcome: kind.Outcome()})
	}
	return append(out,
		ending{name: "setup-failed", outcome: model.SetupFailedOutcome, counts: true},
		ending{name: "idle-watchdog", kind: model.SuspendIdle, outcome: model.SuspendedOutcome, raisedBy: "idle"},
		ending{name: "durable-setup-failed", kind: model.SuspendSetup, outcome: model.SuspendedOutcome, raisedBy: "setup"})
}

func stdTask(id string) model.Task {
	human := model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
	return model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
}

func TestEverySuspensionKindKeepsDispatchStanding(t *testing.T) {
	kinds := declaredSuspensionKinds(t)
	if got, want := len(kinds), len(model.SuspensionKinds()); got != want {
		t.Fatalf("%s declares %d kinds and model.SuspensionKinds lists %d", suspensionKindSource, got, want)
	}
	for _, sc := range standingCases {
		for _, end := range endings(t) {
			t.Run(sc.name+"/"+end.name, func(t *testing.T) {
				t.Parallel()
				keepsStanding(t, sc, end)
			})
		}
	}
}

func keepsStanding(t *testing.T, sc standingCase, end ending) {
	store, ctx := openTestStore(t)
	// The run that fills the pool, and the older backlog.
	busy := stdTask("busy")
	if err := store.PutTask(ctx, busy); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "busy-1", TaskID: "busy", Attempt: 1,
		StartedAt: standingNow.Add(-time.Hour)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t0", "t1"} {
		if err := store.PutTask(ctx, stdTask(id)); err != nil {
			t.Fatal(err)
		}
	}
	subject := stdTask("subject")
	subject.Grants = []model.Grant{{Capability: "memory", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, subject); err != nil {
		t.Fatal(err)
	}
	sc.give(t, ctx, store, &subject)
	if end.raisedBy != "" && !subject.IsDurable() {
		subject.Grants = append(subject.Grants, model.Grant{Capability: model.DurableCapability, Via: model.GrantByLabel})
	}
	if err := store.PutTask(ctx, subject); err != nil {
		t.Fatal(err)
	}
	before, err := store.DispatchStandingJSON(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	limits := model.Limits{Workers: sc.workers}

	// 1. Started through dispatch.Cycle, ahead of the older backlog.
	got, err := dispatch.Cycle(ctx, store, limits, standingNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].TaskID != "subject" {
		t.Fatalf("first cycle started %+v, want subject first", got)
	}
	runID := got[0].RunID
	for _, d := range got[1:] {
		// Anything else the cycle started is not under test; finish it
		// so the next cycle's pool is what it was.
		if err := store.FinishRun(ctx, d.RunID, standingNow, "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}
	streakBefore := failureCount(t, ctx, store, "subject")

	// 2. Ended with the kind through the real finish path.
	at := standingNow.Add(time.Minute)
	task, err := store.GetTask(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case end.raisedBy == "idle":
		cfg := Config{Now: func() time.Time { return at }}
		obs, err := store.GetObservation(ctx, "subject")
		if err != nil {
			t.Fatal(err)
		}
		cause := errRunIdle
		end := classifyRunEnd(ctx, store, cfg, *task, runID, nil, cause, cause, 0, PauseScope{}, standingNow)
		if end.suspend != model.SuspendIdle {
			t.Fatalf("an idle run classified as %+v, want an idle suspension", end)
		}
		if err := recordRunEnd(ctx, ctx, store, cfg, *task, runID, obs, nil, end, nil, true, "", ""); err != nil {
			t.Fatal(err)
		}
	case end.raisedBy == "setup":
		if err := finishSetupFailed(ctx, store, "subject", runID, at, "ended by the test"); err != nil {
			t.Fatal(err)
		}
	default:
		obs, err := store.GetObservation(ctx, "subject")
		if err != nil {
			t.Fatal(err)
		}
		cfg := Config{Now: func() time.Time { return at }}
		ending := runEnding{outcome: end.outcome, detail: "ended by the test"}
		if end.kind.Outcome() == model.SuspendedOutcome {
			// A kind with no outcome of its own names itself, as
			// classifyRunEnd's deadline arm does.
			ending.suspend = end.kind
		}
		if err := recordRunEnd(ctx, ctx, store, cfg, *task, runID, obs, nil, ending, nil, false, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := store.Runs(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	if last := runs[len(runs)-1]; last.Outcome != end.outcome || last.SuspendKind != end.kind {
		t.Fatalf("run recorded as (%q, suspend_kind %q), want (%q, %q)",
			last.Outcome, last.SuspendKind, end.outcome, end.kind)
	}
	after, err := store.DispatchStandingJSON(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("standing after the run ended:\n  %s\nwant it byte for byte as it was:\n  %s", after, before)
	}
	if end.kind != "" {
		recorded, err := store.SuspensionStandingJSON(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if recorded != before {
			t.Fatalf("suspension recorded standing\n  %s\nwant the one the run started with\n  %s", recorded, before)
		}
	}
	streakAfter := failureCount(t, ctx, store, "subject")
	wantStreak := streakBefore
	if end.counts {
		wantStreak++
	}
	if streakAfter != wantStreak {
		t.Fatalf("failure streak went %d -> %d, want %d", streakBefore, streakAfter, wantStreak)
	}

	// 3. The next cycle it is eligible for starts it first. Late enough
	// to be past any retry backoff the ending earns.
	got, err = dispatch.Cycle(ctx, store, limits, standingNow.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].TaskID != "subject" {
		t.Fatalf("cycle after the run ended started %+v, want subject first", got)
	}
	if end.kind != "" {
		suspensions, err := store.Suspensions(ctx, "subject")
		if err != nil {
			t.Fatal(err)
		}
		if len(suspensions) != 1 || suspensions[0].Kind != end.kind || suspensions[0].ResumedRunID != got[0].RunID {
			t.Fatalf("suspensions = %+v, want one of kind %q resumed by %s", suspensions, end.kind, got[0].RunID)
		}
	}
}

func failureCount(t *testing.T, ctx context.Context, store *model.Store, taskID string) int {
	t.Helper()
	streak, err := store.FailureStreak(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if streak != nil {
		n = streak.Count
	}
	if obs != nil && obs.StreakCount != n {
		t.Fatalf("task_observation.streak_count = %d and FailureStreak counts %d", obs.StreakCount, n)
	}
	return n
}

// TestSuspensionKindsAreClassified fails when a run can be finished with
// an outcome that is neither a suspension, a counted failure nor a
// success (model.ClassifyOutcome): every outcome this repository writes
// -- a literal or a model constant handed to Store.FinishRun or
// Store.SetRunOutcome, or assigned to an outcome on its way there -- has
// to declare which it is. recoverRun's details are all one outcome,
// InterruptedOutcome, which this reads off its FinishRun call like any
// other.
func TestSuspensionKindsAreClassified(t *testing.T) {
	consts := modelStringConsts(t)
	found := map[string][]string{}
	for _, dir := range []string{".", "../ui", "../dispatch", "../model", "../demo"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			for _, outcome := range writtenOutcomes(t, path, consts) {
				found[outcome] = append(found[outcome], path)
			}
		}
	}
	if len(found) == 0 {
		t.Fatal("found no outcomes written anywhere: this test needs updating")
	}
	for _, kind := range model.SuspensionKinds() {
		found[kind.Outcome()] = append(found[kind.Outcome()], suspensionKindSource)
	}
	var outcomes []string
	for o := range found {
		outcomes = append(outcomes, o)
	}
	sort.Strings(outcomes)
	for _, outcome := range outcomes {
		if model.ClassifyOutcome(outcome) == model.OutcomeUnclassified {
			t.Errorf("outcome %q (written in %v) is neither a suspension kind, a counted failure "+
				"nor a success: declare which in model.ClassifyOutcome", outcome, found[outcome])
		}
	}
	for _, kind := range model.SuspensionKinds() {
		if model.ClassifyOutcome(kind.Outcome()) != model.OutcomeSuspended {
			t.Errorf("kind %q is recorded as %q, which does not classify as a suspension", kind, kind.Outcome())
		}
	}
}

// modelStringConsts is every string constant pkg/model declares, by name.
func modelStringConsts(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("../model/*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file := parseGo(t, path)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							out[name.Name] = s
						}
					}
				}
			}
		}
	}
	return out
}

func parseGo(t *testing.T, path string) *ast.File {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return file
}

// writtenOutcomes is every outcome path writes that can be resolved to a
// string: the outcome argument of FinishRun and SetRunOutcome, and any
// assignment to a variable or field named outcome. A variable passed on
// is not resolved here; what was assigned to it is. An assignment is only
// taken for a word (outcomeWord): other code names a variable outcome
// too, for a sentence about a CI verdict, and a run's outcome is never a
// sentence.
func writtenOutcomes(t *testing.T, path string, consts map[string]string) []string {
	file := parseGo(t, path)
	inModel := file.Name.Name == "model"
	resolve := func(e ast.Expr) (string, bool) {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				s, err := strconv.Unquote(v.Value)
				return s, err == nil
			}
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "model" {
				s, ok := consts[v.Sel.Name]
				return s, ok
			}
		case *ast.Ident:
			if inModel {
				s, ok := consts[v.Name]
				return s, ok
			}
		}
		return "", false
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			idx := map[string]int{"FinishRun": 3, "SetRunOutcome": 2}[sel.Sel.Name]
			if idx == 0 || len(v.Args) <= idx {
				return true
			}
			if s, ok := resolve(v.Args[idx]); ok {
				out = append(out, s)
			}
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "outcome" && i < len(v.Rhs) {
					if s, ok := resolve(v.Rhs[i]); ok && outcomeWord(s) {
						out = append(out, s)
					}
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && id.Name == "outcome" {
				if s, ok := resolve(v.Value); ok && outcomeWord(s) {
					out = append(out, s)
				}
			}
		}
		return true
	})
	return out
}

// outcomeWord reports whether s has the shape of a run outcome: one
// lower-case word, hyphens and underscores allowed.
func outcomeWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// A run after a suspension is told so in its first line, in both the
// full prompt and the delta a resumed session is sent; a run after
// anything else is not.
func TestPromptOpensWithTheSuspension(t *testing.T) {
	finished := standingNow
	tk := stdTask("subject")
	suspended := History{Attempt: 2, Attempts: []model.Run{{Attempt: 1, Outcome: model.CapacityOutcome,
		SuspendKind: model.SuspendCapacity, Detail: "overloaded\nsecond line", FinishedAt: &finished}}}
	want := "Your previous run was suspended (capacity, overloaded) at 2026-10-26T12:00:00Z " +
		"and this is a fresh run. Anything you did not push is gone.\n\n"
	for name, prompt := range map[string]string{
		"full":    BuildPrompt(tk, "", "", false, time.Hour, suspended, nil),
		"resumed": BuildSuspendedPrompt(tk, "", "", suspended, nil, nil),
	} {
		if !strings.HasPrefix(prompt, want) {
			t.Errorf("%s prompt opens %q, want %q", name, prompt[:min(len(prompt), len(want))], want)
		}
	}
	failed := History{Attempt: 2, Attempts: []model.Run{{Attempt: 1, Outcome: "failed", FinishedAt: &finished}}}
	if prompt := BuildPrompt(tk, "", "", false, time.Hour, failed, nil); strings.Contains(prompt, "was suspended") {
		t.Errorf("a prompt after a failed run names a suspension:\n%s", prompt)
	}
}

// A resumed session can predate the control surface, so its own earlier
// turns name MCP tools that are gone (task 1715's resume called
// wait_for_checks, open_pull_request and run_ci_job, got "No such tool
// available" for each, and parked without running CI). The delta prompt
// carries the grain CLI reference so the resumed run knows what replaced
// them.
func TestSuspendedPromptCarriesTheGrainCLI(t *testing.T) {
	tk := stdTask("subject")
	tk.Target = &model.RepoRef{Owner: "acme", Name: "widgets"}
	prompt := BuildSuspendedPrompt(tk, "", "", History{}, nil, nil)
	for _, want := range []string{
		"### Grain CLI (`grain`)", "`grain ci wait", "`wait_for_checks` is `grain ci wait`",
		"`open_pull_request` is `grain pr open`", "`run_ci_job` is `grain ci run-job`",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("suspended prompt does not mention %q:\n%s", want, prompt)
		}
	}
}
