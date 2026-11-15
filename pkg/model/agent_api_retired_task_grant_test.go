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
	"strings"
	"testing"
)

func TestRetiredTaskGrantRefusalNamesWhatTheNameBecame(t *testing.T) {
	if msg, ok := RetiredTaskGrantRefusal("self-debug"); !ok || !strings.Contains(msg, APIRef("self-debug")) || !strings.HasSuffix(msg, "name that instead") {
		t.Errorf("self-debug: %q, %v", msg, ok)
	}
	if msg, ok := RetiredTaskGrantRefusal("grain-ci"); !ok || !strings.Contains(msg, GrainCIRef) || !strings.Contains(msg, "CI Mode") {
		t.Errorf("grain-ci: %q, %v", msg, ok)
	}
	for _, name := range []string{APIRef("self-debug"), "local-scripts:deploy", ""} {
		if msg, ok := RetiredTaskGrantRefusal(name); ok {
			t.Errorf("%q refused as retired: %q", name, msg)
		}
	}
	if err := RefuseRetiredTaskGrants([]string{APIRef("ci-read"), "read-issues"}); err == nil || !strings.Contains(err.Error(), "read-issues is a retired") {
		t.Errorf("RefuseRetiredTaskGrants = %v, want read-issues refused", err)
	}
}
