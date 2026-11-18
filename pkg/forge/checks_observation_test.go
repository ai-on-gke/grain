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

package forge

import (
	"testing"
	"time"
)

func TestInspectEmptyChecksDoesNotOwnLegacyClock(t *testing.T) {
	restore := SetCheckRegistrationWindow(time.Minute)
	defer restore()
	ref := Ref{Owner: "observer", Repo: "test", Number: 66}
	now := time.Now()
	if known, _, _ := InspectEmptyChecks(ref, "H1", now.Add(time.Hour)); known {
		t.Fatal("inspection created a sighting")
	}
	if EmptyChecksSettled(ref, "H1", now) {
		t.Fatal("legacy clock unexpectedly settled")
	}
	known, settled, deadline := InspectEmptyChecks(ref, "H1", now.Add(time.Minute))
	if !known || !settled || !deadline.Equal(now.Add(time.Minute)) {
		t.Fatal(known, settled, deadline)
	}
	if known, _, _ := InspectEmptyChecks(ref, "H2", now.Add(time.Hour)); known {
		t.Fatal("old head clock carried forward")
	}
	if known, _, _ := InspectEmptyChecks(ref, "H1", now); !known {
		t.Fatal("inspecting new head cleared old clock")
	}
}
