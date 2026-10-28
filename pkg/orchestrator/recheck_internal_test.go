package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestRecheckVerdictCountsOnlyAJobThatFailsTwice(t *testing.T) {
	job := func(name string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "tests", JobID: name, Name: name, Conclusion: c}
	}
	first := "failure: tests/go, tests/lint, tests/ui"
	// lint was inherited and not re-run; go flaked; ui failed again.
	recheck := ci.Run{Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{
		job("go", ci.ConclusionSuccess), job("ui", ci.ConclusionFailure),
	}}
	conclusion, detail, flaked := recheckVerdict(first, recheck)
	if conclusion != ci.ConclusionFailure || detail != "failure: tests/lint, tests/ui" {
		t.Fatalf("got %s %q, want the inherited job and the one that failed twice", conclusion, detail)
	}
	if len(flaked) != 1 || flaked[0] != "tests/go" {
		t.Fatalf("flaked = %v, want tests/go", flaked)
	}

	pass := ci.Run{Conclusion: ci.ConclusionSuccess, Jobs: []ci.JobRun{job("go", ci.ConclusionSuccess)}}
	if conclusion, detail, _ := recheckVerdict("failure: tests/go", pass); conclusion != ci.ConclusionSuccess ||
		len(failedJobsOf(detail)) != 0 {
		t.Fatalf("a recheck that passed every job read %s %q, want a success naming no failure", conclusion, detail)
	}

	// No second sample: the first verdict stands.
	errored := ci.Run{Conclusion: ci.ConclusionErrored}
	if conclusion, detail, flaked := recheckVerdict(first, errored); conclusion != ci.ConclusionFailure ||
		detail != first || flaked != nil {
		t.Fatalf("an errored recheck read %s %q %v, want the first failure unchanged", conclusion, detail, flaked)
	}

	// A first verdict that names no job had nothing re-run: a passing
	// recheck of anything does not turn it green.
	for _, first := range []string{
		"cancelled: superseded: grain/t1 moved to abc1234 while this run was judging def5678",
		"grain CI could not run: no workflow matched",
		"failure: ",
	} {
		if conclusion, detail, flaked := recheckVerdict(first, pass); conclusion == ci.ConclusionSuccess ||
			detail != first || flaked != nil {
			t.Errorf("recheckVerdict(%q, pass) = %s %q %v, want the first verdict unchanged", first, conclusion, detail, flaked)
		}
	}
}

func TestASecondRepairNeedsTheBaseToMoveOrTheFailuresToChange(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	once := &model.Observation{MergeQueueRepairs: 1, MergeQueueRepairAt: &at,
		MergeQueueRepairBase: "aaaaaaa1", MergeQueueRepairFailures: "tests/go"}

	if why := secondRepairReason(once, "main", "aaaaaaa1", "tests/go"); why != "" {
		t.Fatalf("same base, same failures allowed a second repair: %q", why)
	}
	if why := secondRepairReason(once, "main", "bbbbbbb2", "tests/go"); !strings.Contains(why, "new commits") {
		t.Fatalf("a moved base did not allow a second repair: %q", why)
	}
	if why := secondRepairReason(once, "main", "aaaaaaa1", "tests/ui"); !strings.Contains(why, "what is failing has changed") {
		t.Fatalf("different failures did not allow a second repair: %q", why)
	}
	// Nothing known to compare against (a base GitHub would not report).
	if why := secondRepairReason(once, "main", "", "tests/go"); why != "" {
		t.Fatalf("an unknown base allowed a second repair: %q", why)
	}

	twice := *once
	twice.MergeQueueRepairs = 2
	if why := secondRepairReason(&twice, "main", "ccccccc3", "tests/e2e"); why != "" {
		t.Fatalf("a third repair was allowed: %q", why)
	}

	// A repair recorded before the count existed counts as one, and has
	// nothing recorded to measure a change against.
	legacy := &model.Observation{MergeQueueRepairAt: &at}
	if legacy.RepairsSpent() != 1 {
		t.Fatalf("a legacy repair stamp reads %d repairs, want 1", legacy.RepairsSpent())
	}
	if why := secondRepairReason(legacy, "main", "bbbbbbb2", "tests/go"); why != "" {
		t.Fatalf("a legacy repair with nothing recorded allowed a second: %q", why)
	}
}

func TestTheRepairNoteSaysWhichAttemptAndWhy(t *testing.T) {
	at := time.Now()
	obs := &model.Observation{MergeQueueRepairs: 2, MergeQueueRepairAt: &at,
		MergeQueueRepairWhy: "`main` has new commits since the first repair"}
	if note := repairAttemptNote(obs, 2); note != "automatic repair 2 of 2, allowed because `main` has new commits since the first repair" {
		t.Fatalf("note = %q", note)
	}
	if s := repairAttemptSentence(1, 2, ""); s != "This is automatic repair 1 of 2." {
		t.Fatalf("sentence = %q", s)
	}
}
