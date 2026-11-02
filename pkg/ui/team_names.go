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

package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// teamOrDefault is team, or the default team for the empty one -- the
// spelling an object stored before it had a team carries.
func teamOrDefault(team string) string {
	if team = strings.TrimSpace(team); team == "" {
		return model.DefaultTeamID
	}
	return team
}

// retiredTeamField is the `team` a schedule, suite, suite-run or
// template request used to carry. Schema 25 dropped
// schedule.team and suite.team, and the requests dropped the field with
// them; a client still sending it is told so rather than having it
// ignored.
const retiredTeamField = "team"

// readJSONRefusing is readJSON for a request type that has retired the
// top-level fields named by retired: a body carrying any of them, with
// any value, is answered 400 naming the field instead of decoding with
// that field silently dropped.
func readJSONRefusing(w http.ResponseWriter, r *http.Request, v any, retired ...string) bool {
	var raw json.RawMessage
	if !readJSON(w, r, &raw) {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		for _, name := range retired {
			if _, ok := fields[name]; ok {
				writeError(w, http.StatusBadRequest, fmt.Errorf("the %q field is no longer accepted: grain has no teams, and this belongs to the deployment", name))
				return false
			}
		}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		writeBodyError(w, err)
		return false
	}
	return true
}
