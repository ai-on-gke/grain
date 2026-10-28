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

package daemon

import (
	"slices"
	"testing"
)

// A task resumed after its model failed tries the rest of its rotation
// first, and keeps the failed models at the back rather than losing them.
func TestAvoidLastResumesOnTheNextModel(t *testing.T) {
	got := avoidLast([]string{"a", "b", "c"}, []string{"a"})
	if !slices.Equal(got, []string{"b", "c", "a"}) {
		t.Fatalf("avoidLast = %v, want [b c a]", got)
	}
	if got := avoidLast([]string{"a", "b"}, []string{"b", "a"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("every model avoided: %v, want the list as it was", got)
	}
	if got := avoidLast([]string{"a"}, nil); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("nothing avoided: %v", got)
	}
}
