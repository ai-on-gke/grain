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

package stoprule

import (
	"testing"
)

func TestCompleteGoalIsClosingMove(t *testing.T) {
	if MadeClosingMove([]Call{{Name: "record_assumption"}}) {
		t.Fatal("expected record_assumption alone not to count as a closing move")
	}
	if MadeClosingMove([]Call{{Name: "complete_goal", IsError: true}}) {
		t.Fatal("expected failed complete_goal not to count as a closing move")
	}
	if !MadeClosingMove([]Call{{Name: "complete_goal"}}) {
		t.Fatal("expected complete_goal to count as a closing move")
	}
	dec := Decide(false, "Finished all deliverables.", []Call{{Name: "complete_goal"}})
	if dec.Block {
		t.Fatalf("expected Decide not to block after complete_goal, got %+v", dec)
	}
	errDec := Decide(false, "Tried to finish.", []Call{{Name: "complete_goal", IsError: true}})
	if !errDec.Block {
		t.Fatalf("expected Decide to block after errored complete_goal, got %+v", errDec)
	}
}
