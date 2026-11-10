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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A repository's own schedule is written by whoever can merge to the
// repository (insider profile B, task 3965), so a firing carrying a grant
// only a person may accept -- a GCP key, a host script, a dangerous api
// scope -- is filed for a person's approval instead of landing approved.
// Its ordinary grants, and the same grant on a schedule an operator
// wrote, still land approved.
func TestARepoDeclaredScheduleGrantingAGCPKeyFilesUnapproved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	declared := func(stem string) string { return model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, stem) }
	for _, tc := range []struct {
		name, id, grant string
		held            bool
	}{
		{"declared gcp key", declared("publish"), `gcp#gcp-key={"permission_set":"ar-writer"}`, true},
		{"declared host script", declared("deploy"), `host-script#run={"script":"deploy"}`, true},
		{"declared dangerous api scope", declared("admin"), "api:capabilities:admin", true},
		{"declared ci read", declared("nightly"), "api:ci-read@acme/widgets", false},
		{"operator's gcp key", "sched-operator-gcp", `gcp#gcp-key={"permission_set":"ar-writer"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := fireForTask(t, store, ctx, model.Schedule{
				ID:         tc.id,
				Title:      "Publish",
				Target:     nightlyRepo,
				Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
				Enabled:    true,
				NextRunAt:  firstNight,
				CreatedAt:  firstNight.Add(-time.Hour),
				Grants:     []model.Grant{{Capability: tc.grant}},
			})
			if held := task.Approval == nil; held != tc.held {
				t.Fatalf("%s: filed unapproved = %v, want %v (approval %+v)", tc.grant, held, tc.held, task.Approval)
			}
			if tc.held && !strings.Contains(task.Body, "filed for a person's approval") {
				t.Errorf("a held firing does not say why: %q", task.Body)
			}
		})
	}
}

func TestDeclaredGrantNeedsPerson(t *testing.T) {
	for grant, want := range map[string]bool{
		`gcp#gcp-key={"permission_set":"ar-writer"}`: true,
		"host-script#run":          true,
		"github-credential#token":  true,
		"api:secrets:write":        true,
		"api:ci-read@acme/widgets": false,
		"grain-ci":                 false,
		"":                         false,
	} {
		if got := model.DeclaredGrantNeedsPerson(grant); got != want {
			t.Errorf("DeclaredGrantNeedsPerson(%q) = %v, want %v", grant, got, want)
		}
	}
}
