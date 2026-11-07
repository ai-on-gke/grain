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

package demo

import "time"

// demoHealthCounters stands in for a layer built on grain counting its
// sign-in verifier's outcomes (ui.RegisterHealthCounters): core
// registers no counters of its own, so without this System → Health on
// the demo would never show the section. Admissions climb with the
// demo's uptime so the panel can be seen re-reading them.
func demoHealthCounters(started time.Time) func() map[string]int64 {
	return func() map[string]int64 {
		return map[string]int64{
			"proxy:admitted":             40 + int64(time.Since(started)/(5*time.Second)),
			"proxy:admitted_cached":      212,
			"proxy:host_mismatch":        1,
			"proxy:outside_window":       3,
			"proxy:verifier_refused":     2,
			"proxy:verifier_unreachable": 1,
		}
	}
}
