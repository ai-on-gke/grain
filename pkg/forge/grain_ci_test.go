package forge

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

func TestRequiredGrainCIIgnoresGitHubChecks(t *testing.T) {
	mergeable := true
	detail := github.PullRequestDetail{State: "open", Mergeable: &mergeable}
	cases := []struct {
		name        string
		checks      []github.CheckRun
		checksKnown bool
	}{
		{
			name: "failed",
			checks: []github.CheckRun{{
				Name: "GitHub Actions", Status: "completed", Conclusion: stringPtr("failure"),
			}},
			checksKnown: true,
		},
		{
			name:        "running",
			checks:      []github.CheckRun{{Name: "GitHub Actions", Status: "in_progress"}},
			checksKnown: true,
		},
		{name: "unavailable", checksKnown: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HealthOf(detail, tc.checks, "", tc.checksKnown, true, GrainCI{
				Required: true,
				Passed:   true,
			})
			if got != Clean {
				t.Fatalf("HealthOf() = %q, want clean from authoritative grain CI", got)
			}
		})
	}
}

func TestRequiredGrainCIStillPreservesPullRequestState(t *testing.T) {
	mergeable, conflicted := true, false
	for _, tc := range []struct {
		name   string
		detail github.PullRequestDetail
		want   Health
	}{
		{name: "merged", detail: github.PullRequestDetail{State: "closed", Merged: true}, want: Merged},
		{name: "closed", detail: github.PullRequestDetail{State: "closed"}, want: Closed},
		{name: "conflicted", detail: github.PullRequestDetail{State: "open", Mergeable: &conflicted}, want: Conflicted},
		{name: "mergeability pending", detail: github.PullRequestDetail{State: "open"}, want: Unknown},
		{name: "clean", detail: github.PullRequestDetail{State: "open", Mergeable: &mergeable}, want: Clean},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := HealthOf(tc.detail, nil, "", true, true, GrainCI{Required: true, Passed: true})
			if got != tc.want {
				t.Fatalf("HealthOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGrainCIDoesNotOverrideGitHubChecksWhenItIsNotRequired(t *testing.T) {
	mergeable := true
	got := HealthOf(github.PullRequestDetail{State: "open", Mergeable: &mergeable}, []github.CheckRun{{
		Name: "GitHub Actions", Status: "completed", Conclusion: stringPtr("failure"),
	}}, "", true, true, GrainCI{Passed: true})
	if got != Failing {
		t.Fatalf("HealthOf() = %q, want GitHub failure without required grain CI", got)
	}
}

func TestHealthOfWithRequiredGrainCIWhenGitHubFails(t *testing.T) {
	mergeable := true
	got := HealthOf(github.PullRequestDetail{State: "open", Mergeable: &mergeable}, []github.CheckRun{{
		Name: "GitHub Actions", Status: "completed", Conclusion: stringPtr("failure"),
	}}, "", true, true, GrainCI{Required: true, Passed: true})
	if got != Clean {
		t.Fatalf("HealthOf() = %q, want Clean when GrainCI is required and passed", got)
	}
}

func TestHealthOfIgnoresActionsBudgetFailureWhenGrainCIPassed(t *testing.T) {
	mergeable := true
	detail := github.PullRequestDetail{State: "open", Mergeable: &mergeable}

	budgetCheck := github.CheckRun{
		Name:          "GitHub Actions / tests",
		Status:        "completed",
		Conclusion:    stringPtr("failure"),
		FailureReason: "The job was not started because an Actions budget is preventing further use.",
		StepsExecuted: 0,
	}

	// 1. When GrainCI passed, the external budget failure is ignored -> Clean
	got := HealthOf(detail, []github.CheckRun{budgetCheck}, "", true, true, GrainCI{Passed: true})
	if got != Clean {
		t.Fatalf("HealthOf() = %q, want Clean when GrainCI passed and GitHub check is budget failure", got)
	}

	// 2. When GrainCI has not passed, the budget failure is not ignored -> Failing
	got = HealthOf(detail, []github.CheckRun{budgetCheck}, "", true, true, GrainCI{Passed: false})
	if got != Failing {
		t.Fatalf("HealthOf() = %q, want Failing when GrainCI has not passed", got)
	}

	// 3. When GrainCI passed but another real failure exists -> Failing
	realFailure := github.CheckRun{
		Name:          "lint",
		Status:        "completed",
		Conclusion:    stringPtr("failure"),
		StepsExecuted: 1,
	}
	got = HealthOf(detail, []github.CheckRun{budgetCheck, realFailure}, "", true, true, GrainCI{Passed: true})
	if got != Failing {
		t.Fatalf("HealthOf() = %q, want Failing when a real test check also failed", got)
	}
}

func TestIsActionsBudgetFailure(t *testing.T) {
	tests := []struct {
		name  string
		check github.CheckRun
		want  bool
	}{
		{
			name: "budget failure message 0 steps",
			check: github.CheckRun{
				Status:        "completed",
				Conclusion:    stringPtr("failure"),
				FailureReason: "The job was not started because an Actions budget is preventing further use.",
				StepsExecuted: 0,
			},
			want: true,
		},
		{
			name: "spending limit message 0 steps",
			check: github.CheckRun{
				Status:        "completed",
				Conclusion:    stringPtr("startup_failure"),
				FailureReason: "The job was not started because recent account payments have failed or your spending limit needs to be increased",
				StepsExecuted: 0,
			},
			want: true,
		},
		{
			name: "budget failure message but executed steps",
			check: github.CheckRun{
				Status:        "completed",
				Conclusion:    stringPtr("failure"),
				FailureReason: "The job was not started because an Actions budget is preventing further use.",
				StepsExecuted: 2,
			},
			want: false,
		},
		{
			name: "real test failure with 0 steps",
			check: github.CheckRun{
				Status:        "completed",
				Conclusion:    stringPtr("failure"),
				FailureReason: "exit code 1",
				StepsExecuted: 0,
			},
			want: false,
		},
		{
			name: "in progress check",
			check: github.CheckRun{
				Status:        "in_progress",
				FailureReason: "The job was not started because an Actions budget is preventing further use.",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsActionsBudgetFailure(tt.check); got != tt.want {
				t.Fatalf("IsActionsBudgetFailure(%+v) = %v, want %v", tt.check, got, tt.want)
			}
		})
	}
}

func stringPtr(s string) *string { return &s }
