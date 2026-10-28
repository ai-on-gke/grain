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

package agent

import "testing"

// What a provider's refusal says about its own reach: one model's quota,
// or the account's, and which model it names.
func TestClassifyLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		scope      LimitScope
		model      string
	}{
		{"gemini per-model quota", `429 RESOURCE_EXHAUSTED: Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_requests, limit: 50, model: gemini-3-pro`, LimitScopeModel, "gemini-3-pro"},
		{"gemini quota dimensions", `{"quotaDimensions": {"location": "global", "model": "gemini-2.5-flash"}}`, LimitScopeModel, "gemini-2.5-flash"},
		{"openai per-model rate limit", `Rate limit reached for gpt-5-codex in organization org-123 on tokens per min`, LimitScopeModel, "gpt-5-codex"},
		{"openai insufficient quota", `insufficient_quota: You exceeded your current quota, please check your plan and billing details.`, LimitScopeAccount, ""},
		{"anthropic credits", `Your credit balance is too low to access the Anthropic API.`, LimitScopeAccount, ""},
		{"all models", `quota exhausted for all models on this project`, LimitScopeAccount, ""},
		{"bare 429", `429 Too Many Requests`, LimitScopeModel, ""},
	} {
		scope, named := ClassifyLimit(tc.text)
		if scope != tc.scope || named != tc.model {
			t.Errorf("%s: ClassifyLimit = (%q, %q), want (%q, %q)", tc.name, scope, named, tc.scope, tc.model)
		}
	}
}

// Classify fills what the text says and leaves what a parser already set.
func TestUsageLimitErrorClassifyKeepsWhatWasSet(t *testing.T) {
	t.Parallel()
	limit := (&UsageLimitError{Model: "auto-chosen"}).Classify("insufficient_quota for model gpt-5")
	if limit.Scope != LimitScopeAccount || limit.Model != "auto-chosen" {
		t.Errorf("Classify = %+v, want account scope and the model already set", limit)
	}
}
