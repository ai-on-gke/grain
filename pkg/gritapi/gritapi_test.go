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

package gritapi

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/treeid"
)

func TestComputeRequestIDIsDeterministicAndOrderIndependentForJobs(t *testing.T) {
	a := StartRun{
		Repo:    "bwsalmon/grain",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
		Tree:    treeid.ID{GitTree: "aaaa", SHA256: "bbbb"},
		Event:   "push",
		Jobs:    []string{"lint", "go"},
		Attempt: 1,
	}
	b := a
	b.Jobs = []string{"go", "lint"}
	if gotA, gotB := ComputeRequestID(a), ComputeRequestID(b); gotA != gotB {
		t.Fatalf("ComputeRequestID differed across job ordering: %q vs %q", gotA, gotB)
	}
	c := a
	c.Attempt = 2
	if ComputeRequestID(a) == ComputeRequestID(c) {
		t.Fatal("ComputeRequestID did not change when Attempt changed")
	}
}

func TestValidScopeAndDecodeAPIError(t *testing.T) {
	for _, s := range AllScopes {
		if !ValidScope(s) {
			t.Errorf("ValidScope(%q) = false, want true", s)
		}
	}
	if ValidScope("unknown") {
		t.Error("ValidScope(\"unknown\") = true, want false")
	}
	err := DecodeAPIError(428, []byte(`{"error":"tree_missing","sha256":"deadbeef"}`))
	if sha, ok := IsTreeMissing(err); !ok || sha != "deadbeef" {
		t.Fatalf("IsTreeMissing = (%q, %v), want (deadbeef, true)", sha, ok)
	}
	err410 := DecodeAPIError(410, []byte(`{"error":"cursor_gone","epoch":"e-123","oldestSeq":50}`))
	if apiErr, ok := IsCursorGone(err410); !ok || apiErr.Epoch != "e-123" || apiErr.OldestSeq != 50 {
		t.Fatalf("IsCursorGone = (%+v, %v), want epoch=e-123 oldestSeq=50", apiErr, ok)
	}
}
