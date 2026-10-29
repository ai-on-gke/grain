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

package model_test

// What grain's nightly infrastructure review is and is not allowed to
// be. The template is a prompt plus a set of grants, and both halves are
// load-bearing in a way a reader editing either could undo without
// noticing: the whole feature is that this run suggests a parameter
// change and cannot make one, and every assertion here is one of the
// ways that stops being true.
//
// In pkg/model because the text and the grants are this package's own,
// and because the template is grain's rather than a deployment's
// (SystemTemplate's doc comment on why): a deployment cannot put any of
// this back by configuring anything, so nothing but a test in this
// repository defends it.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func infraReview(t *testing.T) *model.Template {
	t.Helper()
	tmpl := model.SystemTemplate(model.InfraReviewTemplateID,
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no infrastructure review template")
	}
	return tmpl
}

// The one property the whole job rests on: it can read everything about
// this deployment and change nothing about it. A grant that writes
// settings, or a target to push to, would each on their own turn a job
// whose value is that a person reads the reasoning first into a job that
// moves the deployment's behaviour overnight.
func TestTheInfraReviewCanSuggestAndCannotApply(t *testing.T) {
	t.Parallel()
	tmpl := infraReview(t)
	if tmpl.Target != nil {
		t.Errorf("target = %s, want none: this job produces a report and proposals, and a "+
			"target would be the very repository whose settings it is reviewing", tmpl.Target)
	}
	granted := map[string]bool{}
	for _, g := range tmpl.Grants {
		granted[g.Capability] = true
	}
	for _, want := range []string{"self-debug", "grain-ci"} {
		if !granted[want] {
			t.Errorf("the infrastructure review does not hold %q; its grants are %+v",
				want, tmpl.Grants)
		}
	}
	// Each of these would let the run act rather than recommend, and
	// each for a different reason -- see infraReviewTemplate's own
	// "deliberately not granted".
	for _, forbidden := range []string{"self-repair", "memory", "release-notes"} {
		if granted[forbidden] {
			t.Errorf("the infrastructure review holds %q: a job that suggests parameter "+
				"changes must not also be able to make one, or to file tonight's hypothesis "+
				"where every later run reads it as instruction", forbidden)
		}
	}
	if !strings.Contains(tmpl.Body, "never apply") {
		t.Error("the prompt does not say in as many words that this run suggests and never " +
			"applies, which is the whole of what the job is")
	}
}

// A suggestion is only worth reading if it carries its own evidence, and
// the three things it must carry are the three ways a tuning
// recommendation is wrong: a number with no history is noise, a number
// with no mechanism is a guess, and a change with no rule for reading
// the result can never be evaluated -- by a person or by the next run of
// this same job.
func TestTheInfraReviewIsToldWhatASuggestionMustCarry(t *testing.T) {
	t.Parallel()
	body := infraReview(t).Body
	for _, want := range []string{
		// The number, with something to compare it against.
		"with its history",
		// Which limit is binding: inside the jobs, or in front of them.
		"mechanism",
		// What would show it worked, and what would show it was the
		// wrong lever, said before the change is made.
		"decision rule",
		// And the bar: an incomplete suggestion is not made at all.
		"is not a suggestion",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the prompt never says %q, so a suggestion missing it can still be "+
				"made:\n\n%s", want, body)
		}
	}
	if !strings.Contains(body, "max_workers") || !strings.Contains(body, "max_ci_jobs") {
		t.Error("the worked example no longer contrasts the two levers that a single " +
			"\"CI is slow\" reading cannot tell apart, which is the point of naming a mechanism")
	}
}

// A tuner with no cooldown can never attribute an improvement to
// anything it did, which is the only thing separating it from guessing.
// The cooldown has to be in the prompt as a rule and in the report as a
// statement -- a run left to decide each night whether this particular
// number is special will decide that it is.
func TestTheInfraReviewIsToldNotToThrash(t *testing.T) {
	t.Parallel()
	body := infraReview(t).Body
	if !strings.Contains(body, "cooldown") {
		t.Fatalf("the prompt has no cooldown, so nothing stops this job moving a parameter "+
			"every night and never being able to say whether any of it helped:\n\n%s", body)
	}
	if !strings.Contains(body, "7 days") {
		t.Error("the cooldown is not stated as a length, so it is a judgement the run makes " +
			"fresh each night rather than a rule")
	}
	if !strings.Contains(body, "State the cooldown in your report") {
		t.Error("the prompt does not ask for the cooldown in the report, so a person reading " +
			"it cannot tell what was noticed and deliberately left alone")
	}
}

// Without its own history this job re-files the same recommendation
// every night until somebody switches the schedule off -- and being
// switched off is indistinguishable, from the outside, from working.
func TestTheInfraReviewReadsItsOwnHistoryFirst(t *testing.T) {
	t.Parallel()
	tmpl := infraReview(t)
	// A nil reader returns the four tools anyway, refusing every call:
	// this wants their names, not a route to the daemon.
	for _, tool := range []struct{ Name string }{{"grain task list"}, {"grain task read"}, {"grain task prompt"}, {"grain task transcript"}} { // the self-debug task tools, as `grain` commands
		if !strings.Contains(tmpl.Body, tool.Name) {
			t.Errorf("the prompt never mentions %s, which is how this run reads what it "+
				"recommended last night and what came of it", tool.Name)
		}
	}
	if !strings.Contains(tmpl.Body, "grain metrics") {
		t.Error("the prompt never mentions read_grain_metrics, which is the whole of this " +
			"deployment's measurement of itself and the only reason the job can be more than " +
			"an opinion")
	}
	for _, want := range []string{"ci_queueing", "host", "changes"} {
		if !strings.Contains(tmpl.Body, want) {
			t.Errorf("the prompt never names the %q section: an agent that has to discover "+
				"the sections by asking for the whole report spends its turns on the tool "+
				"rather than on the question", want)
		}
	}
	namedOnlyAsAbsent(t, "infrastructure review's", tmpl.Body)
}

// Reliability is half the brief and the half with no single figure on a
// dashboard, so the prompt names the signals one at a time. A run asked
// for "reliability" in the abstract reports the failure rate and stops.
func TestTheInfraReviewIsSentAfterReliabilityByName(t *testing.T) {
	t.Parallel()
	body := infraReview(t).Body
	for _, want := range []string{
		"daemon restart",         // a cancellation is not a verdict
		"stuck",                  // tasks nothing will move
		"across unrelated",       // the same job failing everywhere at once
		"merge queue had to",     // repairs somebody else had to file
		"no parameter would fix", // and that most of these have no dial behind them
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the reliability half of the prompt no longer says %q; it is the half "+
				"that gets dropped, because throughput is the reading with the obvious "+
				"numbers behind it", want)
		}
	}
	// The tools that answer the cross-branch question, which is the one
	// no aggregate over job durations contains.
	for _, want := range []string{"grain ci list", "grain ci log"} {
		if !strings.Contains(body, want) {
			t.Errorf("the prompt never mentions %s, so the flake-across-branches signal has "+
				"no route into the report", want)
		}
	}
}

// A nightly job that always finds something is a nightly job nobody
// reads -- and by the time it has something urgent to say, nobody is
// reading it.
func TestTheInfraReviewIsToldThatNothingIsAGoodAnswer(t *testing.T) {
	t.Parallel()
	body := infraReview(t).Body
	if !strings.Contains(body, "nothing recommended") {
		t.Errorf("the prompt does not say that recommending nothing is a complete report, so "+
			"every night's run has a page to fill:\n\n%s", body)
	}
	// And the report itself has somewhere to go. It must be a comment
	// rather than a question: a scheduled firing that stays open skips
	// the next one silently (model.OverlapSkip), so a run that parks on
	// a question stops this schedule indefinitely.
	if !strings.Contains(body, "comment_on_issue") {
		t.Error("the prompt does not say where the report goes; nobody watches this run, so " +
			"the closing note is the only account of it there will be")
	}
	if !strings.Contains(body, "the next night's firing is skipped") {
		t.Error("the prompt does not warn that leaving this task open stops the schedule, " +
			"which is how a standing review job dies without anything saying so")
	}
}

// What the operator ruled out, which is not a matter of taste: a tuner
// that could reach the merge predicate could make its own numbers look
// better by changing what counts as a pass.
func TestTheInfraReviewMayNotReachTheGate(t *testing.T) {
	t.Parallel()
	body := infraReview(t).Body
	if !strings.Contains(body, "what counts as a verdict") {
		t.Errorf("the prompt no longer rules the merge gate out of scope, so a nightly tuner "+
			"may reach a fixed merge predicate by strategy:\n\n%s", body)
	}
	if !strings.Contains(body, "Per-repository policy") {
		t.Error("the prompt no longer rules per-repository policy out of scope; those are " +
			"decisions somebody made about a repository, not dials")
	}
}

// With no state repository there is nothing to read the current value of
// a parameter out of, and nothing to measure the cooldown against. The
// honest answer is to say so, not to recommend a change to a number
// nobody looked at.
func TestTheInfraReviewWithoutAStateRepositorySaysWhatItCannotSee(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.InfraReviewTemplateID, model.RepoRef{})
	if tmpl == nil {
		t.Fatal("this build ships no infrastructure review template")
	}
	if len(tmpl.Reads) != 0 {
		t.Errorf("reads = %+v, want none: there is no state repository to read", tmpl.Reads)
	}
	if !strings.Contains(tmpl.Body, "exports no state repository") {
		t.Errorf("the prompt does not tell the run that it cannot see the settings, so it "+
			"will spend the run looking for them:\n\n%s", tmpl.Body)
	}
	if !strings.Contains(tmpl.Body, "do not propose a change to a number you have not seen") {
		t.Error("the prompt does not stop the run recommending a change to a value it could " +
			"not read")
	}
	namedOnlyAsAbsent(t, "infrastructure review's", tmpl.Body)

	// And with one, that repository is what it reads and where a change
	// it proposes has to land.
	withRepo := infraReview(t)
	stateRepo := model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}
	if len(withRepo.Reads) != 1 || withRepo.Reads[0] != stateRepo {
		t.Errorf("reads = %+v, want the state repository, where every parameter in scope "+
			"actually lives", withRepo.Reads)
	}
	if !strings.Contains(withRepo.Body, "tables/grain_config.json") {
		t.Error("the prompt does not say where the parameters are exported, which is where " +
			"the run reads the value it is about to recommend changing")
	}
	if !strings.Contains(withRepo.Body, "propose_task") {
		t.Error("the prompt does not say how a change is actually filed, which leaves the " +
			"run with a recommendation and nowhere to put it")
	}
}
