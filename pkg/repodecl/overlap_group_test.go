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

package repodecl_test

// `overlap:` in both of its forms, and `overlap-group:` (grain/task-3895).

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestAScheduleNamesAnOverlapGroup(t *testing.T) {
	for name, overlap := range map[string]string{
		"mapping":        "overlap: {skip: true, group: sync-oss-release}\n",
		"mapping, block": "overlap:\n  group: sync-oss-release\n",
		"top-level key":  "overlap: skip\noverlap-group: sync-oss-release\n",
		"key alone":      "overlap-group: sync-oss-release\n",
	} {
		t.Run(name, func(t *testing.T) {
			declared := load(t, map[string]string{
				".grain/templates/sync.yml": "title: Sync\n",
				".grain/schedules/sync.yml": "template: sync\ndaily:\n  at: \"02:30\"\n" + overlap,
			})
			sched := declared.Schedules[0].Schedule
			if sched.Overlap != model.OverlapSkip || sched.OverlapGroup != "sync-oss-release" {
				t.Errorf("overlap = %q, group = %q", sched.Overlap, sched.OverlapGroup)
			}
		})
	}
}

func TestThePlainOverlapStringsStillParse(t *testing.T) {
	for _, v := range []model.OverlapPolicy{model.OverlapSkip, model.OverlapAllow} {
		declared := load(t, map[string]string{
			".grain/templates/sync.yml": "title: Sync\n",
			".grain/schedules/sync.yml": "template: sync\ndaily:\n  at: \"02:30\"\noverlap: " + string(v) + "\n",
		})
		if s := declared.Schedules[0].Schedule; s.Overlap != v || s.OverlapGroup != "" {
			t.Errorf("overlap: %s parsed as %q / group %q", v, s.Overlap, s.OverlapGroup)
		}
	}
}

func TestABadOverlapGroupIsRefused(t *testing.T) {
	for overlap, want := range map[string]string{
		"overlap: {skip: false, group: g}\n":      "cannot be combined",
		"overlap: allow\noverlap-group: g\n":      "cannot be combined",
		"overlap: {group: g}\noverlap-group: h\n": "not both",
		"overlap: {group: \"a b\"}\n":             "may only contain",
		"overlap: {group: \"\"}\n":                "empty",
		"overlap: {skip: true, groups: g}\n":      "unknown key",
		"overlap: sometimes\n":                    "\"skip\" or \"allow\"",
	} {
		msg := loadErr(t, map[string]string{
			".grain/templates/sync.yml": "title: Sync\n",
			".grain/schedules/sync.yml": "template: sync\ndaily:\n  at: \"02:30\"\n" + overlap,
		})
		if !strings.Contains(msg, want) {
			t.Errorf("%q: error = %q, want it to mention %q", overlap, msg, want)
		}
	}
}
