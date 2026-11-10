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

package ui_test

// An overlap group over the API (grain/task-3895): carried on create and
// update, refused beside "allow", and named when it is why "Run now"
// cannot run.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAnOverlapGroupIsCarriedAndNamedWhenItHoldsRunNow(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	create := func(title string) ui.Schedule {
		t.Helper()
		sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
			Title: title, Description: "sync it", Repo: "acme/widgets",
			Recurrence: everyDay, OverlapGroup: "release-sync",
		})
		if err != nil {
			t.Fatalf("creating %s: %v", title, err)
		}
		if sched.OverlapGroup != "release-sync" || sched.Overlap != "skip" {
			t.Errorf("created %s with overlap %q / group %q", title, sched.Overlap, sched.OverlapGroup)
		}
		return sched
	}
	sync, net := create("Sync"), create("Safety net")

	if _, err := c.RunScheduleNow(ctx, sync.ID); err != nil {
		t.Fatalf("running %s now: %v", sync.ID, err)
	}
	_, err := c.RunScheduleNow(ctx, net.ID)
	if err == nil || !strings.Contains(err.Error(), sync.ID) || !strings.Contains(err.Error(), "release-sync") {
		t.Errorf("running %s now = %v, want it refused naming %s and the group", net.ID, err, sync.ID)
	}

	allow := "allow"
	if _, err := c.UpdateSchedule(ctx, net.ID, ui.UpdateScheduleRequest{Overlap: &allow}); err == nil {
		t.Error("setting a grouped schedule to allow overlap was accepted, want it refused")
	}
	none := ""
	updated, err := c.UpdateSchedule(ctx, net.ID, ui.UpdateScheduleRequest{OverlapGroup: &none})
	if err != nil || updated.OverlapGroup != "" {
		t.Fatalf("leaving the group = %+v, %v", updated, err)
	}
	if _, err := c.RunScheduleNow(ctx, net.ID); err != nil {
		t.Errorf("running %s now outside the group: %v", net.ID, err)
	}

	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Loose", Description: "x", Repo: "acme/widgets", Recurrence: everyDay,
		Overlap: "allow", OverlapGroup: "release-sync",
	}); err == nil {
		t.Error("creating an allow schedule in a group was accepted, want it refused")
	}
}
