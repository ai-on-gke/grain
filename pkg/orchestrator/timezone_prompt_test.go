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
