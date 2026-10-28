package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestBuildPromptIncludesTimezone(t *testing.T) {
	tests := []struct {
		name         string
		task         model.Task
		expectedPart string
	}{
		{
			name: "Direct interactive task with user timezone",
			task: model.Task{
				Interactive: true,
				TimeZone:    "America/Los_Angeles",
			},
			expectedPart: "User timezone: America/Los_Angeles",
		},
		{
			name: "Console session task with user timezone",
			task: model.Task{
				Origin: model.Origin{
					Reason: model.ReasonConsoleSession,
				},
				TimeZone: "Europe/London",
			},
			expectedPart: "User timezone: Europe/London",
		},
		{
			name: "Background task with team timezone",
			task: model.Task{
				Origin: model.Origin{
					Reason: model.ReasonSchedule,
				},
				TimeZone: "Asia/Tokyo",
			},
			expectedPart: "Team timezone: Asia/Tokyo",
		},
		{
			name: "Task without timezone defaults to UTC",
			task: model.Task{
				Origin: model.Origin{
					Reason: model.ReasonSchedule,
				},
				TimeZone: "",
			},
			expectedPart: "Team timezone: UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := orchestrator.BuildPrompt(tt.task, "/work", "/home/debian", true, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil)
			if !strings.Contains(prompt, tt.expectedPart) {
				t.Fatalf("expected prompt to contain %q, but got:\n%s", tt.expectedPart, prompt)
			}
		})
	}
}
