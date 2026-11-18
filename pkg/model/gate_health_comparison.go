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

package model

import (
	"encoding/json"
	"strings"

	"github.com/ai-on-gke/grain/pkg/gates"
)

func compareHealthPredictions(v gates.ObservationView) string {
	legacy, current := []string{}, []string{}
	for i, b := range v.Bindings {
		status := v.Results[i].Result.Status
		if status == "unknown" || status == "error" {
			return "health_unknown_or_incomplete"
		}
		if strings.Contains(string(b.Config), "legacy_") {
			legacy = append(legacy, status)
		} else {
			current = append(current, status)
		}
	}
	if gates.Digest(legacy) == gates.Digest(current) {
		return "health_profiles_agree"
	}
	return "health_stronger_profile_differs"
}

// Expiring a declared window can change a prediction without changing any
// source row. Retain that transition while coalescing stable lease refreshes.
func healthPredictionsChanged(previous string, current gates.ObservationView) bool {
	var old gates.ObservationView
	if json.Unmarshal([]byte(previous), &old) != nil {
		return true
	}
	signature := func(v gates.ObservationView) string {
		type prediction struct{ Binding, Status, Summary string }
		out := []prediction{}
		for _, r := range v.Results {
			out = append(out, prediction{r.Result.BindingID, r.Result.Status, r.Result.Summary})
		}
		return gates.Digest(out)
	}
	return signature(old) != signature(current)
}
