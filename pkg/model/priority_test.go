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

import "testing"

func TestParseTaskPriority(t *testing.T) {
	for text, want := range map[string]int{
		"8": 8, " 9 ": 9, "1000": 1000, "1e3": 1000, "1000.0": 1000, "1.5e2": 150,
		"1E3": 1000, "-1": -1, "0": 0, "1e10": 10000000000, "9007199254740992": 9007199254740992, "+5": 5,
	} {
		if got, err := ParseTaskPriority(text); err != nil || got != want {
			t.Errorf("ParseTaskPriority(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	for _, text := range []string{"", "1.5", `"1000"`, "0x10", "1e20", "NaN", "Inf", "true", "five"} {
		if got, err := ParseTaskPriority(text); err == nil {
			t.Errorf("ParseTaskPriority(%q) = %d, want it refused", text, got)
		}
	}
}
