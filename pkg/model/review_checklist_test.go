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

package model_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestReviewChecklistCarriesAllCorrectnessCategories(t *testing.T) {
	t.Parallel()
	got := model.ReviewChecklist()
	for _, want := range []string{
		"**Beyond the diff.**",
		"on an incremental re-review, in the new commits only",
		"git grep -n",
		"**External contracts.**",
		"every JSON-schema `array` has `items`",
		"Gemini rejects a whole tool list",
		"**Multi-host and edition paths.**",
		"kubernetes-granule",
		"capability.Register",
		"**Optional dependencies and test shape.**",
		"-count=2 -shuffle=on",
		"t.Cleanup",
		"**Task lifecycle.**",
		"reopened from closed",
		"resume_later",
		"**Migrations and stored data.**",
		"idempotent on both SQLite and Postgres",
		"pkg/staterepo/dump.go",
		"**Docs and messages that point somewhere.**",
		"actually grants or changes what the message says",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ReviewChecklist() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("ReviewChecklist() contains unexpanded template placeholder:\n%s", got)
	}
	// Budget: roughly 0.75K tokens (~2.5-4.5 KB of prose).
	if len(got) < 2000 || len(got) > 5000 {
		t.Errorf("len(ReviewChecklist()) = %d bytes, want 2000..5000 (~0.75K tokens)", len(got))
	}
}
