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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// A runs page is only reused for the shared fault list when it is the
// whole newest-first listing; one scoped to a reader's own visibility
// must not seed the cache every reader shares.
func TestCanShareFaultRunsRefusesAScopedOrNarrowedPage(t *testing.T) {
	whole := ci.RunFilter{Descending: true}
	if !canShareFaultRuns(whole, 0) {
		t.Fatal("the whole newest-first listing should be shareable")
	}
	scoped := whole
	scoped.Visible = func(string) bool { return true }
	if canShareFaultRuns(scoped, 0) {
		t.Fatal("a page bounded by a reader's visibility must not be shared")
	}
	narrowed := whole
	narrowed.Ref = "main"
	if canShareFaultRuns(narrowed, 0) {
		t.Fatal("a narrowed page must not be shared")
	}
	if canShareFaultRuns(ci.RunFilter{}, 0) {
		t.Fatal("an oldest-first page must not be shared")
	}
	if canShareFaultRuns(whole, ciFaultRunLimit-1) {
		t.Fatal("a page shorter than the fault run limit must not be shared")
	}
}
