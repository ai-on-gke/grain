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

package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEnvironmentNameForEntryPrefersTheDisplayNameAndSanitizes(t *testing.T) {
	for _, tc := range []struct {
		display, entry, want string
	}{
		{"", "alpha", "alpha"},
		{"Alpha team", "alpha", "Alpha team"},
		{"   ", "alpha", "alpha"},
		{"  Alpha\n\tteam\r\n", "alpha", "Alpha team"},
		{"Alpha\x00\x7fteam", "alpha", "Alpha team"},
		{strings.Repeat("x", 31) + " yz", "alpha", strings.Repeat("x", 31)},
		{strings.Repeat("é", 80), "alpha", strings.Repeat("é", MaxEnvironmentNameRunes)},
		{"", "", ""},
	} {
		got := EnvironmentNameForEntry(tc.display, tc.entry)
		if got != tc.want {
			t.Errorf("EnvironmentNameForEntry(%q, %q) = %q, want %q", tc.display, tc.entry, got, tc.want)
		}
		if n := utf8.RuneCountInString(got); n > MaxEnvironmentNameRunes {
			t.Errorf("EnvironmentNameForEntry(%q, %q) is %d runes, over %d", tc.display, tc.entry, n, MaxEnvironmentNameRunes)
		}
		if strings.ContainsAny(got, "\n\r\t") {
			t.Errorf("EnvironmentNameForEntry(%q, %q) = %q keeps a line break or tab", tc.display, tc.entry, got)
		}
	}
}
