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

	"github.com/ai-on-gke/grain/pkg/model"
)

// The static half of grain/task-314 and grain/task-358: neither lane has
// any dead states.
//
// queuestatus_internal_test.go proves *totality* -- every input renders as
// some named state with a sentence. These prove *liveness*: every state
// that can be written is classified as driven, awaiting a person or
// awaiting an external answer, a driven one names the component that leaves
// it, and no state is unclassified. The last clause is the one that keeps
// this true rather than making it true once: the states are read out of
// the file that declares them -- queuestatus.go for the merge lane,
// pkg/model's task.go for the task lane -- so a state added there and not
// classified here fails the build.
//
// What these cannot prove is that the named driver fires -- that needs a
// cycle, a store and a simulated GitHub, and lives in
// liveness_test.go (orchestrator_test).

// livenessLanes is every lane these guards run over. They are written over
// a lane rather than over a table so that classifying a second set of
// states is a second table and its coverage cases, and not a second copy
// of the three tests below.
var livenessLanes = []laneLiveness{mergeLane, taskLane}

// declaredStates is every constant in a lane's source file that carries
// its prefix, by the name it is declared under.
//
// Read off the parsed syntax rather than a second hand-written list,
// because a second hand-written list is exactly what went stale: the
// vocabulary check in queuestatus_internal_test.go has its own
// allMergeStates(), and it is four states behind the file it is about
// (base_broken, checks_unfixable, batching and updating_branch are all
// missing from it). pkg/mcp's merge_queue_phrases_test.go reads the same
// file the same way and for the same reason.
func declaredStates(t *testing.T, lane laneLiveness) map[string]string {
	t.Helper()
	return declaredConsts(t, lane.source, lane.prefix)
}

// declaredConsts is every string constant in source whose name carries
// prefix, by name: the states of a lane, or (livenessreasons.go) its
// sub-cases.
func declaredConsts(t *testing.T, source, prefix string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", source, err)
	}
	states := map[string]string{}
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
			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not declared as a string literal: this test needs updating", name.Name)
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s's value %s: %v", name.Name, lit.Value, err)
				}
				states[name.Name] = unquoted
			}
		}
	}
	if len(states) == 0 {
		t.Fatalf("found no %s* constants in %s: this test needs updating", prefix, source)
	}
	return states
}

// packageFunctions is every function this package declares, by name, read
// off the non-test sources. It is what makes the driver a claim rather
// than a comment: a driver renamed or deleted without this table being
// updated is the change that orphaned waiting_ci, and it fails here.
func packageFunctions(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				funcs[fn.Name.Name] = true
			}
		}
	}
	if len(funcs) == 0 {
		t.Fatal("read no functions at all out of this package: this test needs updating")
	}
	return funcs
}

// TestEveryStateIsClassified is the guard. A state the daemon can write
// and no table says anything about is a state nobody has asked the
// liveness question of -- which is how grain/task-149, grain/task-264 and
// grain/task-312 each happened -- and the answer is not allowed to be
// silence.
func TestEveryStateIsClassified(t *testing.T) {
	for _, lane := range livenessLanes {
		t.Run(lane.name, func(t *testing.T) {
			declared := declaredStates(t, lane)
			for _, name := range sortedNames(declared) {
				state := declared[name]
				if _, ok := lane.of(state); !ok {
					t.Errorf("%s (%q) has no entry in the %s lane: say what leaves that state -- a "+
						"component of grain (livenessDriven, naming it), a person "+
						"(livenessAwaitingPerson, naming the control), or GitHub "+
						"(livenessAwaitingExternal)", name, state, lane.name)
				}
			}
			// And nothing classified that the daemon no longer writes: a
			// classification outliving its state is how a table stops
			// describing the thing it is about.
			real := map[string]bool{}
			for _, state := range declared {
				real[state] = true
			}
			for _, state := range sortedStates(lane) {
				if !real[state] {
					t.Errorf("the %s lane classifies %q and %s declares no such state",
						lane.name, state, lane.source)
				}
			}
		})
	}
}

// TestEveryClassificationSaysWhatItHasTo is the shape of an entry, per
// class. Each class is a different promise and each promise has a field
// that carries it, so an entry that names neither a driver nor a control
// is one that has been filled in without the question being answered.
func TestEveryClassificationSaysWhatItHasTo(t *testing.T) {
	for _, lane := range livenessLanes {
		t.Run(lane.name, func(t *testing.T) { classificationsSayWhatTheyHaveTo(t, lane) })
	}
}

func classificationsSayWhatTheyHaveTo(t *testing.T, lane laneLiveness) {
	for _, state := range sortedStates(lane) {
		liveness, _ := lane.of(state)
		if liveness.patience == nil {
			t.Errorf("%q has no patience function at all: say noPatience for a state that is "+
				"terminal by design, and how long it may stand for every other", state)
			continue
		}
		patience := liveness.patience()
		switch liveness.class {
		case livenessDriven:
			// "Something will leave this state" is the promise, so the
			// something has to be named, and a runtime clock has to be on
			// it: a driven state with no patience is a state nothing would
			// ever notice had stopped being driven, which is the whole of
			// what grain/task-264 was.
			if liveness.driver == "" {
				t.Errorf("%q is driven and names no driver", state)
			}
			if liveness.control != "" {
				t.Errorf("%q is driven and names a control (%q): a state a person has to press "+
					"something to leave is livenessAwaitingPerson", state, liveness.control)
			}
			if patience <= 0 {
				t.Errorf("%q is driven with no patience: nothing would ever say its driver had stopped", state)
			}
			if liveness.why == "" {
				t.Errorf("%q is driven with no `why`: the row would say it is stuck and not what to "+
					"do about it", state)
			}
		case livenessAwaitingPerson:
			// Terminal by design. The one thing it owes a reader is the way
			// out, and it must not be flagged for standing still -- which
			// is the state doing exactly what it is for.
			if liveness.control == "" || liveness.controlBy == "" {
				t.Errorf("%q waits for a person and names no control that restarts the cycle "+
					"(control=%q by %q)", state, liveness.control, liveness.controlBy)
			}
			if liveness.driver != "" {
				t.Errorf("%q waits for a person and names driver %q: a state something leaves by "+
					"itself is livenessDriven", state, liveness.driver)
			}
			if patience != 0 {
				t.Errorf("%q waits for a person and has a patience of %s: a state that is terminal "+
					"by design standing still is not a stall", state, patience)
			}
		case livenessAwaitingExternal:
			// grain drives nothing, so there is no driver to name -- but
			// an external answer that never comes is the one kind of dead
			// end nothing else here would ever report, so the clock is
			// required.
			if liveness.driver != "" {
				t.Errorf("%q awaits an external answer and names driver %q", state, liveness.driver)
			}
			if patience <= 0 {
				t.Errorf("%q awaits an external answer with no patience: an answer that never comes "+
					"would never be reported", state)
			}
			if liveness.why == "" {
				t.Errorf("%q awaits an external answer with no `why`", state)
			}
		default:
			t.Errorf("%q has class %q, which is not one of the three", state, liveness.class)
		}
	}
}

// TestEveryNamedDriverIsARealComponent is what stops the driver being
// prose. Each is an identifier this package declares, checked against the
// package's own syntax, so the divergence this whole file exists to close
// -- the driving side changing under the display side -- cannot be done
// silently.
func TestEveryNamedDriverIsARealComponent(t *testing.T) {
	funcs := packageFunctions(t)
	for _, lane := range livenessLanes {
		for _, state := range sortedStates(lane) {
			liveness, _ := lane.of(state)
			for what, name := range map[string]string{"driver": liveness.driver, "backstop": liveness.backstop} {
				if name == "" {
					continue
				}
				if !funcs[name] {
					t.Errorf("the %s lane's %q names %s %q and this package declares no such "+
						"function: either it has been renamed (say so here) or it is gone (and "+
						"that state is driven by nothing)", lane.name, state, what, name)
				}
			}
		}
	}
}

// TestAStateThatOutlivesItsDriverSaysSoOnTheRow is the runtime half, as a
// unit: the age grain already records, measured against the expectation
// this file adds, and the sentence a person gets.
func TestAStateThatOutlivesItsDriverSaysSoOnTheRow(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	entered := baseInstant
	patience := checkStallPatience()

	// Inside the patience the row says exactly what it said: a wait that is
	// going normally must not be dressed up as a fault, or the flag means
	// nothing.
	for _, at := range []time.Time{entered, entered.Add(patience - time.Second)} {
		if _, ok := mergeLane.overdue(mergeStateWaitingCI, &entered, at); ok {
			t.Errorf("waiting_ci flagged as stalled %s in", at.Sub(entered))
		}
		if got := mergeLane.noteWithStall(mergeStateWaitingCI, "waiting on `go`", &entered, at); got != "waiting on `go`" {
			t.Errorf("note at %s = %q, want it untouched", at.Sub(entered), got)
		}
		if got := mergeLane.stalledAt(mergeStateWaitingCI, &entered, at); got != nil {
			t.Errorf("stalledAt %s in = %v, want nothing: this row still gets its live mark",
				at.Sub(entered), got)
		}
	}

	// On it, and the row says which component should have left this state
	// and has not. That is the difference between the age grain/task-264's
	// row already carried -- "since 00:52:40 (7m ago)", true and useless --
	// and a stall detector: nobody reading an age knows whether it is a
	// normal one for the state beside it.
	at := entered.Add(patience)
	if got, ok := mergeLane.overdue(mergeStateWaitingCI, &entered, at); !ok || got != patience {
		t.Fatalf("overdue at the deadline = (%s, %t), want (%s, true)", got, ok, patience)
	}
	note := mergeLane.noteWithStall(mergeStateWaitingCI, "waiting on `grain CI`", &entered, at)
	for _, want := range []string{"waiting on `grain CI`", "stood here for more than 2h15m",
		"reconcileTaskCI", "escalateStalledChecks", "stuck rather than as waiting"} {
		if !strings.Contains(note, want) {
			t.Errorf("the stalled row does not mention %q:\n%s", want, note)
		}
	}

	// And it says the same thing an hour later. The row is written when the
	// note changes (setMergeQueueState), so a note that counted the age up
	// would be a store write per stalled entry per cycle about a fact that
	// has not changed -- and the age is already on the row beside it.
	if later := mergeLane.noteWithStall(mergeStateWaitingCI, "waiting on `grain CI`",
		&entered, at.Add(time.Hour)); later != note {
		t.Errorf("the stalled row changed on the next cycle:\n%s\n%s", note, later)
	}

	// The same finding as a fact rather than a sentence, which is what
	// everything that is not a person reads: the UI's live mark and
	// pkg/mcp's own answer both come off this
	// (Observation.MergeQueueStalledAt). It is the deadline the entry
	// crossed and not the moment a cycle noticed, so -- like the sentence
	// -- it stands still while the entry does.
	stalled := mergeLane.stalledAt(mergeStateWaitingCI, &entered, at)
	if stalled == nil || !stalled.Equal(entered.Add(patience)) {
		t.Fatalf("stalledAt on the deadline = %v, want %v", stalled, entered.Add(patience))
	}
	if later := mergeLane.stalledAt(mergeStateWaitingCI, &entered, at.Add(time.Hour)); later == nil ||
		!later.Equal(*stalled) {
		t.Errorf("stalledAt moved on the next cycle: %v then %v", stalled, later)
	}
}

// The three ways this must not fire, each of which would make the flag
// worth less than nothing.
func TestTheStallFlagHoldsItsTongue(t *testing.T) {
	entered := baseInstant
	long := entered.Add(30 * 24 * time.Hour)

	// A state that is terminal by design. merge_failed standing still for a
	// month is merge_failed working: the queue has stopped driving and said
	// so, and a stall warning on it would be grain reporting its own
	// correct behaviour as a fault.
	if _, ok := mergeLane.overdue(mergeStateFailed, &entered, long); ok {
		t.Error("merge_failed flagged as stalled: it is terminal by design")
	}
	// Which means no stamp on the row either: a "Stalled" chip beside a
	// row whose whole message is that a person now owns it would be two
	// alarms for one fact.
	if got := mergeLane.stalledAt(mergeStateFailed, &entered, long); got != nil {
		t.Errorf("merge_failed stamped as stalled at %v: it is terminal by design", got)
	}
	// What it does say is how to leave it, which is the awaiting-a-person
	// clause's other half -- and only once, however many cycles rewrite the
	// row.
	note := mergeLane.noteWithWayOut(mergeStateFailed, "the repair ran and did not take")
	if !strings.Contains(note, `"Retry merge" starts the round again`) {
		t.Errorf("a terminal row does not say what to press:\n%s", note)
	}
	if again := mergeLane.noteWithWayOut(mergeStateFailed, note); again != note {
		t.Errorf("the way out was appended twice:\n%s", again)
	}
	// And it is only said about a state that is actually a person's.
	if got := mergeLane.noteWithWayOut(mergeStateWaitingCI, "waiting on `go`"); got != "waiting on `go`" {
		t.Errorf("a driven state was told to press something: %q", got)
	}

	// A state with no stamp behind it -- an observation written before the
	// stamp existed, or one the closing cycle cleared. There is nothing to
	// measure, and guessing would flag every old row in the database.
	if _, ok := mergeLane.overdue(mergeStateWaitingCI, nil, long); ok {
		t.Error("waiting_ci with no state stamp flagged as stalled")
	}

	// A state this build has no classification for: a daemon mid-upgrade
	// writing a word an older binary does not know degrades to saying
	// nothing, the way the phrase maps already do. The guard above is what
	// keeps that from lasting.
	if _, ok := mergeLane.overdue("waiting_thing", &entered, long); ok {
		t.Error("an unclassified state was flagged as stalled")
	}

	// And a deployment that has asked to wait forever is not told it is
	// stuck. The patience for waiting_ci is a grace period on forge's own
	// stall deadline, so switching giving up off switches this off with it
	// rather than leaving the two disagreeing.
	defer SetCheckStallDeadline(0)()
	if got := checkStallPatience(); got != 0 {
		t.Fatalf("patience with giving up switched off = %s, want none", got)
	}
	if _, ok := mergeLane.overdue(mergeStateWaitingCI, &entered, long); ok {
		t.Error("waiting_ci flagged as stalled on a deployment that never gives up on CI")
	}
}

// TestTaskStallNoteFlagsTheStatesGrainDrivesAndNoOthers is the task
// lane's half of the two above, through the one door a reader outside
// this package comes in by (TaskStallNote, which pkg/ui asks on every
// task read).
func TestTaskStallNoteFlagsTheStatesGrainDrivesAndNoOthers(t *testing.T) {
	entered := baseInstant

	// The three states grain itself is working through, each measured
	// against its own patience: inside it the row says nothing, on it the
	// row names what should have ended the wait.
	for state, patience := range map[model.State]time.Duration{
		model.StateQueued:    dispatchPatience(),
		model.StateRunning:   runPatience(),
		model.StateCompleted: mergeHandoffPatience(),
	} {
		if got := TaskStallNote(state, entered, entered.Add(patience-time.Second)); got != "" {
			t.Errorf("%s flagged a second inside its patience: %q", state, got)
		}
		note := TaskStallNote(state, entered, entered.Add(patience))
		if note == "" {
			t.Errorf("%s said nothing at %s, its whole patience", state, patience)
			continue
		}
		liveness, _ := taskLane.of(string(state))
		if !strings.Contains(note, liveness.driver) {
			t.Errorf("%s's stalled row does not name %s:\n%s", state, liveness.driver, note)
		}
		// And it says the same thing a day later: the sentence names the
		// patience, not the age, so a row changes once when it is crossed
		// and then stands still (noteWithStall's own doc comment). Here
		// that matters for a different reason than it does on the merge
		// lane -- nothing is written, but a note that moved would make
		// every poll of the task list a fresh answer.
		if later := TaskStallNote(state, entered, entered.Add(patience+24*time.Hour)); later != note {
			t.Errorf("%s's stalled row changed a day later:\n%s\n%s", state, note, later)
		}
	}

	// And the ones a person owns say nothing, however long they stand. A
	// proposal nobody has approved in a month is a proposal. (A deferral
	// is the exception with a clock on it, and has a test of its own.)
	month := entered.Add(30 * 24 * time.Hour)
	for _, state := range []model.State{model.StateProposed, model.StateAwaitingReply,
		model.StateAwaitingSubmit, model.StateFailed, model.StateClosed} {
		if got := TaskStallNote(state, entered, month); got != "" {
			t.Errorf("%s, which is waiting on a person by design, was flagged: %q", state, got)
		}
	}

	// A state this build has no classification for -- a daemon mid-upgrade
	// reading a word an older binary does not know -- degrades to silence,
	// the same way the merge lane's does.
	if got := TaskStallNote(model.State("thinking"), entered, month); got != "" {
		t.Errorf("an unclassified task state was flagged: %q", got)
	}
}

// TestADeferralWhoseWakeTimeHasPassedStalls is the driven half of
// 'deferred' (deferredWake): a wake time reconcileWake has not acted on
// within a cycle of it reads as stuck, measured from the wake time and
// not from when the task was put aside, and its row offers Undefer. A
// deferral with no wake time has no since (model.StateTimes.Since), so
// nothing ever measures it.
func TestADeferralWhoseWakeTimeHasPassedStalls(t *testing.T) {
	deferredAt := baseInstant
	until := deferredAt.Add(30 * 24 * time.Hour)
	task := model.Task{ID: "9", DeferredAt: &deferredAt, DeferredUntil: &until}
	times := model.StateTimesFrom(task, nil, nil)
	since := times.Since(model.StateDeferred)
	if since == nil || !since.Equal(until) {
		t.Fatalf("a deferral with a wake time is measured from %v, want the wake time %v", since, until)
	}
	patience := deferredWake.patience()
	if patience <= 0 {
		t.Fatal("deferredWake has no patience: a wake that never happens would never be reported")
	}

	// A month on the shelf before the wake time is not a stall, and nor is
	// the cycle after it: reconcileWake gets one cycle's grace.
	for _, at := range []time.Time{deferredAt.Add(29 * 24 * time.Hour), until, until.Add(patience - time.Second)} {
		if got := TaskStallNote(model.StateDeferred, *since, at); got != "" {
			t.Errorf("flagged at %v, inside the wake time's patience: %q", at, got)
		}
	}
	note := TaskStallNote(model.StateDeferred, *since, until.Add(patience))
	if note == "" {
		t.Fatalf("said nothing %s after the wake time", patience)
	}
	for _, want := range []string{"reconcileWake", `"Undefer"`} {
		if !strings.Contains(note, want) {
			t.Errorf("the stalled row does not name %s:\n%s", want, note)
		}
	}
	if later := TaskStallNote(model.StateDeferred, *since, until.Add(patience+24*time.Hour)); later != note {
		t.Errorf("the stalled row changed a day later:\n%s\n%s", note, later)
	}

	// The sub-case agrees: driven by the same component, escalated to a
	// person, with the control the row is meant to offer.
	reason := TaskReason(task, model.StateDeferred, nil, nil, nil)
	if reason.Reason != taskReasonDeferredWake || reason.Driver != deferredWake.driver || reason.Control != "Undefer" {
		t.Errorf("TaskReason = %+v, want %s driven by %s with Undefer", reason, taskReasonDeferredWake, deferredWake.driver)
	}
	if r := taskReasons[taskReasonDeferredWake]; r.escalation != stallToPerson {
		t.Errorf("%s escalates to %q, want %q", taskReasonDeferredWake, r.escalation, stallToPerson)
	}
	if !packageFunctions(t)[deferredWake.driver] {
		t.Errorf("deferredWake's driver %q is not a function in this package", deferredWake.driver)
	}

	// No wake time: a person's decision, and nothing to measure.
	task.DeferredUntil = nil
	if got := model.StateTimesFrom(task, nil, nil).Since(model.StateDeferred); got != nil {
		t.Errorf("a deferral with no wake time is measured from %v, want nothing", got)
	}
}

// A clock that has jumped backwards reads as "not overdue yet" -- the
// direction every other wait in this package errs in (forge's sightings,
// EmptyChecksSettled).
func TestTheStallFlagErrsTowardWaiting(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	entered := baseInstant
	if _, ok := mergeLane.overdue(mergeStateWaitingCI, &entered, entered.Add(-time.Hour)); ok {
		t.Error("a clock that jumped backwards flagged a fresh state as stalled")
	}
}

// sortedStates and sortedNames keep these failures in a stable order, so a
// table that has broken in several places reads the same way twice --
// pkg/mcp's sortedKeys, over the two maps here.
func sortedStates(lane laneLiveness) []string {
	states := make([]string, 0, len(lane.states))
	for state := range lane.states {
		states = append(states, state)
	}
	sort.Strings(states)
	return states
}

func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
