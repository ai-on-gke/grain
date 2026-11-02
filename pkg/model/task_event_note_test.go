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

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A note of who took an action (operator-attribution spec §3.1) is not a
// step of the timeline: it is left out, and a close beside it is still
// one.
func TestMergeTaskEventsLeavesNotesOut(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	alice := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP}
	var events []model.TaskEvent
	for i, kind := range []model.TaskEventKind{
		model.TaskEventApprovalWithdrawn, model.TaskEventSubmitted, model.TaskEventRetried,
		model.TaskEventDeferred, model.TaskEventUndeferred, model.TaskEventParked,
		model.TaskEventDependencies, model.TaskEventReordered, model.TaskEventSecretSet,
	} {
		events = append(events, model.TaskEvent{TaskID: "7", Kind: kind, At: at.Add(time.Duration(i) * time.Minute), By: alice})
	}
	events = append(events, model.TaskEvent{TaskID: "7", Kind: model.TaskEventClosed, At: at.Add(time.Hour), By: alice})

	got := model.MergeTaskEvents(nil, events)
	if len(got) != 1 || got[0].State != model.StateClosed || got[0].By == nil || *got[0].By != alice {
		t.Fatalf("MergeTaskEvents = %+v, want the close alone", got)
	}
}
