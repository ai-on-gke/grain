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

package mcp

import "testing"

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"", "''"},
		{"abc-123_XYZ.456/:@=,", "abc-123_XYZ.456/:@=,"},
		{"hello world", "'hello world'"},
		{"don't", `'don'\''t'`},
		{"'quoted'", `''\''quoted'\'''`},
		{"$PATH", `'$PATH'`},
		{"`rm -rf /`", "'`rm -rf /`'"},
		{"line1\nline2", "'line1\nline2'"},
		{"foo; bar", "'foo; bar'"},
		{"a\"b", "'a\"b'"},
	} {
		if got := shellQuote(tc.input); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
