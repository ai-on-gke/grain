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

package uiapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/demo/demotest"
)

// ported from ui/e2e/schedule-every-n-minutes.spec.js:102.
func TestClassic_ScheduleEveryNMinutes_TheAPIHoldsEveryNMinutesToItsFloorAndCeiling(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	for _, c := range []struct {
		n  int
		ok bool
	}{{9, false}, {0, false}, {-5, false}, {10, true}, {527040, true}, {527041, false}} {
		res := srv.Do(t, http.MethodPost, "/api/schedules", map[string]any{
			"title":      fmt.Sprintf("floor %d", c.n),
			"repo":       srv.Seed.Repo,
			"recurrence": map[string]any{"kind": "everyNMinutes", "everyNMinutes": c.n},
		})
		if res.OK() != c.ok {
			t.Errorf("every %d minutes: %d %s, want ok=%v", c.n, res.Status, res.Body, c.ok)
		}
		if res.OK() {
			srv.Do(t, http.MethodDelete, "/api/schedules/"+res.ID(t), nil).WantOK(t)
		}
	}
}
