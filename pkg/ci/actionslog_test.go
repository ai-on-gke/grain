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

package ci

import (
	"strings"
	"testing"
	"time"
)

func TestActionsStepLogSlicesByStepTimes(t *testing.T) {
	at := func(sec int) *time.Time {
		v := time.Date(2026, 11, 4, 3, 10, sec, 0, time.UTC)
		return &v
	}
	steps := []StepRun{
		{Name: "Set up job", StartedAt: at(0), FinishedAt: at(0)},
		{Name: "Test", StartedAt: at(1), FinishedAt: at(3)},
		{Name: "Skipped", Conclusion: ConclusionSkipped},
		{Name: "Last", StartedAt: at(5)},
	}
	log := "\ufeff2026-11-04T03:10:00.5000000Z Runner\n" +
		"2026-11-04T03:10:01.0000000Z ##[group]Run go test\n" +
		"2026-11-04T03:10:03.9000000Z FAIL\n" +
		"no stamp\n" +
		"2026-11-04T03:10:04.5000000Z between steps\n" +
		"2026-11-04T03:10:05.0000000Z last\n"
	for i, want := range map[int]string{
		0: "\ufeff2026-11-04T03:10:00.5000000Z Runner\n",
		1: "2026-11-04T03:10:01.0000000Z ##[group]Run go test\n2026-11-04T03:10:03.9000000Z FAIL\nno stamp\n",
		3: "2026-11-04T03:10:05.0000000Z last\n",
	} {
		if got, err := ActionsStepLog(log, steps, i); err != nil || got != want {
			t.Errorf("step %d = %q, %v; want %q", i, got, err, want)
		}
	}
	if _, err := ActionsStepLog(log, steps, 2); err == nil || !strings.Contains(err.Error(), "never started") {
		t.Errorf("a skipped step = %v; want it to say it never started", err)
	}
	if _, err := ActionsStepLog("plain\nlines\n", steps, 1); err == nil || !strings.Contains(err.Error(), "no timestamps") {
		t.Errorf("an unstamped log = %v; want it to say so", err)
	}
	if _, err := ActionsStepLog("2026-11-04T04:00:00Z later\n", steps, 1); err == nil || !strings.Contains(err.Error(), "no lines") {
		t.Errorf("an empty window = %v; want it to say so", err)
	}
}
