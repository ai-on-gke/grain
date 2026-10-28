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

package claude

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// A Claude plan limit is the login's, shared by every model on it; a
// model family's own limit is that family's.
func TestClaudeLimitsSayTheirReach(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text  string
		scope agent.LimitScope
		model string
	}{
		{"Claude AI usage limit reached|1772370000", agent.LimitScopeAccount, ""},
		{"You've hit your weekly limit · resets Mar 3, 9am (UTC)", agent.LimitScopeAccount, ""},
		{"You've hit your Opus limit · resets 3pm (UTC)", agent.LimitScopeModel, "opus"},
	} {
		limit := usageLimitFromResult(tc.text)
		if limit == nil {
			t.Fatalf("%q: no usage limit read", tc.text)
		}
		if limit.Scope != tc.scope || limit.Model != tc.model {
			t.Errorf("%q: scope %q model %q, want %q %q", tc.text, limit.Scope, limit.Model, tc.scope, tc.model)
		}
	}
}
