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

// `skip-when:` (model.Schedule.SkipWhen, grain/task-4393).

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestAScheduleDeclaresSkipWhenPublishingHeld(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/train.yml": "title: Train\n",
		".grain/schedules/train.yml": "template: train\nevery:\n  minutes: 20\nskip-when: publishing-held\n",
	})
	if got := declared.Schedules[0].Schedule.SkipWhen; got != model.SkipWhenPublishingHeld {
		t.Errorf("skip-when = %q, want %q", got, model.SkipWhenPublishingHeld)
	}
	declared = load(t, map[string]string{
		".grain/templates/train.yml": "title: Train\n",
		".grain/schedules/train.yml": "template: train\nevery:\n  minutes: 20\n",
	})
	if got := declared.Schedules[0].Schedule.SkipWhen; got != model.SkipWhenNever {
		t.Errorf("skip-when left out = %q, want nothing", got)
	}
}

func TestAnUnknownSkipWhenIsRefused(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/train.yml": "title: Train\n",
		".grain/schedules/train.yml": "template: train\nevery:\n  minutes: 20\nskip-when: ci-red\n",
	})
	if !strings.Contains(msg, "skip-when") || !strings.Contains(msg, "publishing-held") {
		t.Errorf("error = %q, want it to name the key and the condition there is", msg)
	}
}
