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

package cli

import (
	"strings"
	"testing"
)

func TestGoogle3TargetFlags(t *testing.T) {
	if ct, err := google3Target(false, "", 0, ""); ct != nil || err != nil {
		t.Errorf("no --google3: %+v, %v; want nothing", ct, err)
	}
	if _, err := google3Target(false, "w", 0, ""); err == nil || !strings.Contains(err.Error(), "need --google3") {
		t.Errorf("a spec flag without --google3: error %v", err)
	}
	ct, err := google3Target(true, "", 0, "")
	if err != nil || ct == nil || ct.Kind != "google3" || ct.SpecString() != "" {
		t.Errorf("--google3 alone: %+v, %v", ct, err)
	}
	ct, err = google3Target(true, " mine ", 42, "head")
	if err != nil || ct.SpecString() != `{"start_cl":42,"sync_to":"head","workspace":"mine"}` {
		t.Errorf("--google3 with spec: %s, %v", ct.SpecString(), err)
	}
}
