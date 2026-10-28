package ci

import (
	"strings"
	"testing"
	"time"
)

func makeTestTime(offset time.Duration) *time.Time {
	t := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC).Add(offset)
	return &t
}

func TestAttributionRegressionWhenBaseBranchPassed(t *testing.T) {
	baseRun := &Run{
		ID:         "run-base",
		Repo:       "bwsalmon/grain",
		Ref:        "main",
		Commit:     "abc1234567890",
		Conclusion: ConclusionSuccess,
		FinishedAt: makeTestTime(-10 * time.Minute),
		Jobs: []JobRun{
			{
				JobID:      "test",
				Name:       "test-job",
				Conclusion: ConclusionSuccess,
			},
		},
	}

	branchRun := Run{
		ID:         "run-branch",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-654",
		Commit:     "def9876543210",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(0),
		Jobs: []JobRun{
			{
				JobID:      "test",
				Name:       "test-job",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{
						Name:       "run-tests",
						Conclusion: ConclusionFailure,
						Output:     "--- FAIL: TestSomething (0.01s)\n    main_test.go:42: assertion failed\nFAIL",
					},
				},
			},
		},
	}

	attr, ok := AttributeJobFailure(branchRun, branchRun.Jobs[0], baseRun, nil)
	if !ok {
		t.Fatalf("expected attribution, got ok=false")
	}

	if attr.Kind != AttributionRegression {
		t.Errorf("got kind %q, want %q", attr.Kind, AttributionRegression)
	}
	if !strings.Contains(attr.Reason, "The job passed on the base branch at commit abc1234") {
		t.Errorf("unexpected reason: %q", attr.Reason)
	}
	if attr.Guidance != GuidanceRegression {
		t.Errorf("got guidance %q, want %q", attr.Guidance, GuidanceRegression)
	}

	summary := attr.Summary()
	wantSummary := "[REGRESSION] The job passed on the base branch at commit abc1234 but failed on this commit. This failure was introduced by your changes."
	if summary != wantSummary {
		t.Errorf("Summary = %q, want %q", summary, wantSummary)
	}
}

func TestAttributionSharedBaseFaultWhenBaseBranchFailedWithSameSignature(t *testing.T) {
	errOutput := "FATAL: connection to postgres failed on 127.0.0.1:5432\ndial tcp 127.0.0.1:5432: connect: connection refused"

	baseRun := &Run{
		ID:         "run-base",
		Repo:       "bwsalmon/grain",
		Ref:        "main",
		Commit:     "1111222333444",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(-20 * time.Minute),
		Jobs: []JobRun{
			{
				JobID:      "integration",
				Name:       "integration-tests",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{
						Name:       "migrate",
						Conclusion: ConclusionFailure,
						Output:     errOutput,
					},
				},
			},
		},
	}

	branchRun := Run{
		ID:         "run-branch",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-654",
		Commit:     "5555666777888",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(0),
		Jobs: []JobRun{
			{
				JobID:      "integration",
				Name:       "integration-tests",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{
						Name:       "migrate",
						Conclusion: ConclusionFailure,
						Output:     errOutput,
					},
				},
			},
		},
	}

	attr, ok := AttributeJobFailure(branchRun, branchRun.Jobs[0], baseRun, nil)
	if !ok {
		t.Fatalf("expected attribution, got ok=false")
	}

	if attr.Kind != AttributionSharedBaseFault {
		t.Errorf("got kind %q, want %q", attr.Kind, AttributionSharedBaseFault)
	}
	if !strings.Contains(attr.Reason, "The job failed with the same signature on main at commit 1111222") {
		t.Errorf("unexpected reason: %q", attr.Reason)
	}
	if attr.Guidance != GuidanceSharedBaseFault {
		t.Errorf("got guidance %q, want %q", attr.Guidance, GuidanceSharedBaseFault)
	}

	summary := attr.Summary()
	wantSummary := "[SHARED / BASE FAULT] The job failed with the same signature on main at commit 1111222. This is a shared upstream issue on the base branch, not caused by your changes. Do not attempt to fix this locally."
	if summary != wantSummary {
		t.Errorf("Summary = %q, want %q", summary, wantSummary)
	}
}

func TestAttributionSharedBaseFaultAcrossSiblingBranches(t *testing.T) {
	errOutput := "npm ERR! 404 Not Found - GET https://registry.npmjs.org/@grain/broken-pkg - Not found\nnpm ERR! A complete log of this run can be found in:"

	sibling1 := Run{
		ID:         "run-sib-1",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-100",
		Commit:     "aaaa1111",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(-15 * time.Minute),
		Jobs: []JobRun{
			{
				JobID:      "ui-build",
				Name:       "ui-build",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{
						Name:       "npm-install",
						Conclusion: ConclusionFailure,
						Output:     errOutput,
					},
				},
			},
		},
	}

	sibling2 := Run{
		ID:         "run-sib-2",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-200",
		Commit:     "bbbb2222",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(-10 * time.Minute),
		Jobs: []JobRun{
			{
				JobID:      "ui-build",
				Name:       "ui-build",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{
						Name:       "npm-install",
						Conclusion: ConclusionFailure,
						Output:     errOutput,
					},
				},
			},
		},
	}

	branchRun := Run{
		ID:         "run-branch",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-654",
		Commit:     "cccc3333",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(0),
		Jobs: []JobRun{
			{
				JobID:      "ui-build",
				Name:       "ui-build",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{
						Name:       "npm-install",
						Conclusion: ConclusionFailure,
						Output:     errOutput,
					},
				},
			},
		},
	}

	// Base run is either nil or not matching
	attr, ok := AttributeJobFailure(branchRun, branchRun.Jobs[0], nil, []Run{sibling1, sibling2})
	if !ok {
		t.Fatalf("expected attribution, got ok=false")
	}

	if attr.Kind != AttributionSharedBaseFault {
		t.Errorf("got kind %q, want %q", attr.Kind, AttributionSharedBaseFault)
	}
	if attr.OwnerTaskID != "100" || attr.AssignedToSelf {
		t.Errorf("got OwnerTaskID=%q AssignedToSelf=%v, want 100 / false", attr.OwnerTaskID, attr.AssignedToSelf)
	}
	if !strings.Contains(attr.Reason, "The job failed with the same signature across 2 sibling branches") {
		t.Errorf("unexpected reason: %q", attr.Reason)
	}
	wantGuidance := GuidanceSharedFaultOwnedBy("100")
	if attr.Guidance != wantGuidance {
		t.Errorf("got guidance %q, want %q", attr.Guidance, wantGuidance)
	}

	summary := attr.Summary()
	wantSummary := "[SHARED / BASE FAULT] The job failed with the same signature across 2 sibling branches. " + wantGuidance
	if summary != wantSummary {
		t.Errorf("Summary = %q, want %q", summary, wantSummary)
	}

	// When the current branch belongs to the earliest task in queue order,
	// the shared fault is assigned to it to fix.
	firstRun := branchRun
	firstRun.Ref = "grain/task-50"
	firstAttr, ok := AttributeJobFailure(firstRun, firstRun.Jobs[0], nil, []Run{sibling1, sibling2})
	if !ok {
		t.Fatalf("expected attribution for earliest task, got ok=false")
	}
	if firstAttr.Kind != AttributionSharedBaseFault || !firstAttr.AssignedToSelf || firstAttr.OwnerTaskID != "50" {
		t.Errorf("got kind=%q AssignedToSelf=%v OwnerTaskID=%q, want %q / true / 50",
			firstAttr.Kind, firstAttr.AssignedToSelf, firstAttr.OwnerTaskID, AttributionSharedBaseFault)
	}
	if firstAttr.Guidance != GuidanceSharedFaultAssigned {
		t.Errorf("got guidance %q, want %q", firstAttr.Guidance, GuidanceSharedFaultAssigned)
	}
	if !strings.HasPrefix(firstAttr.Summary(), "["+string(AttributionSharedFaultAssigned)+"]") {
		t.Errorf("owner summary must start with [%s]: %q", AttributionSharedFaultAssigned, firstAttr.Summary())
	}
	if strings.Contains(firstAttr.Summary(), "Do not attempt to fix this locally") {
		t.Errorf("owner summary must not tell the agent not to fix locally: %q", firstAttr.Summary())
	}
}

func TestAttributionRunHelper(t *testing.T) {
	allRuns := []Run{
		{
			ID:         "run-main",
			Repo:       "bwsalmon/grain",
			Ref:        "main",
			Commit:     "main1234567",
			Conclusion: ConclusionSuccess,
			FinishedAt: makeTestTime(-30 * time.Minute),
			Jobs: []JobRun{
				{JobID: "unit-test", Name: "unit-test", Conclusion: ConclusionSuccess},
				{JobID: "e2e-test", Name: "e2e-test", Conclusion: ConclusionSuccess},
			},
		},
		{
			ID:         "run-sib-1",
			Repo:       "bwsalmon/grain",
			Ref:        "grain/task-999",
			Commit:     "sib1234567",
			Conclusion: ConclusionFailure,
			FinishedAt: makeTestTime(-10 * time.Minute),
			Jobs: []JobRun{
				{
					JobID:      "e2e-test",
					Name:       "e2e-test",
					Conclusion: ConclusionFailure,
					Steps: []StepRun{
						{Name: "run", Conclusion: ConclusionFailure, Output: "docker daemon not reachable\nfailed to connect to unix:///var/run/docker.sock"},
					},
				},
			},
		},
		{
			ID:         "run-sib-2",
			Repo:       "bwsalmon/grain",
			Ref:        "grain/task-888",
			Commit:     "sib8888888",
			Conclusion: ConclusionFailure,
			FinishedAt: makeTestTime(-5 * time.Minute),
			Jobs: []JobRun{
				{
					JobID:      "e2e-test",
					Name:       "e2e-test",
					Conclusion: ConclusionFailure,
					Steps: []StepRun{
						{Name: "run", Conclusion: ConclusionFailure, Output: "docker daemon not reachable\nfailed to connect to unix:///var/run/docker.sock"},
					},
				},
			},
		},
	}

	currentRun := Run{
		ID:         "run-current",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-654",
		Commit:     "curr1234567",
		Conclusion: ConclusionFailure,
		FinishedAt: makeTestTime(0),
		Jobs: []JobRun{
			{
				JobID:      "unit-test",
				Name:       "unit-test",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{Name: "test", Conclusion: ConclusionFailure, Output: "FAIL: TestMyFeature\nmy_feature_test.go:12: want true got false"},
				},
			},
			{
				JobID:      "e2e-test",
				Name:       "e2e-test",
				Conclusion: ConclusionFailure,
				Steps: []StepRun{
					{Name: "run", Conclusion: ConclusionFailure, Output: "docker daemon not reachable\nfailed to connect to unix:///var/run/docker.sock"},
				},
			},
		},
	}

	attributions := AttributeRun(currentRun, "main", allRuns)

	unitAttr, ok := attributions["unit-test"]
	if !ok {
		t.Fatalf("expected attribution for unit-test")
	}
	if unitAttr.Kind != AttributionRegression {
		t.Errorf("unit-test kind = %q, want %q", unitAttr.Kind, AttributionRegression)
	}

	e2eAttr, ok := attributions["e2e-test"]
	if !ok {
		t.Fatalf("expected attribution for e2e-test")
	}
	if e2eAttr.Kind != AttributionSharedBaseFault {
		t.Errorf("e2e-test kind = %q, want %q", e2eAttr.Kind, AttributionSharedBaseFault)
	}
}
