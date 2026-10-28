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

package antigravity

import "testing"

func TestProviderFor(t *testing.T) {
	tests := []struct {
		key      string
		expected string
	}{
		{"", ""},
		{"ya29.a0AfB_bCG...", "vertex"},
		{" ya29.foo ", "vertex"},
		{"{\"type\": \"service_account\"}", "vertex"},
		{" { \"type\":... }", "vertex"},
		{"AIzaSy...", "gemini"},
		{"some_other_key", "gemini"},
	}

	for _, tt := range tests {
		got := providerFor(tt.key)
		if got != tt.expected {
			t.Errorf("ProviderFor(%q) = %q; want %q", tt.key, got, tt.expected)
		}
	}
}
