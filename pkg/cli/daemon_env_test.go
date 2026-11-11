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

import "testing"

func TestApplyEnvFallbacksLetsAPassedFlagWin(t *testing.T) {
	t.Setenv("GRAIN_TEST_FALLBACK_A", "  from-env  ")
	t.Setenv("GRAIN_TEST_FALLBACK_B", "from-env")
	t.Setenv("GRAIN_TEST_FALLBACK_C", "   ")
	a, b, c := "default", "from-flag", "default"
	applyEnvFallbacks(map[string]bool{"b": true}, []envFallback{
		{"a", "GRAIN_TEST_FALLBACK_A", &a},
		{"b", "GRAIN_TEST_FALLBACK_B", &b},
		{"c", "GRAIN_TEST_FALLBACK_C", &c},
	})
	if a != "from-env" || b != "from-flag" || c != "default" {
		t.Fatalf("got a=%q b=%q c=%q, want from-env, from-flag, default", a, b, c)
	}
}
