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

package upgrade

// What a command that was killed on a deadline says for itself.
//
// The wording is the whole product of runError and askError, and it is
// read by people rather than by code: an operator in the Upgrade pane,
// and -- since tests/container drives real upgrades against real images
// -- a CI log left by a release that published nothing. Both of them ask
// the same two questions of a killed command, "was it a deadline or the
// command itself", and "which deadline", so both answers are asserted
// here rather than left to the tests that happen to run into one.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A command killed on its deadline says so, and says how long it was
// given -- the part that was missing while ten seconds was bounding a
// container's whole life (grain/task-329, grain/task-341). Without the
// duration, "it ran out of time and was killed" is the same line whether
// the bound was ten seconds or the upgrade's own forty-five minutes, and
// telling those apart from a release log meant reading this package.
func TestAKilledCommandSaysWhichDeadlineKilledIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	err := runError(ctx, 2*time.Minute, errors.New("signal: killed"))
	for _, want := range []string{"ran out of time after 2m0s", "signal: killed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("runError = %q, want it to say %q", err, want)
		}
	}
	// askError's half of the same pair already named its bound, and the
	// two are worth reading side by side: whatever a caller is asking
	// about, it learns the same thing from either.
	if got := askError(ctx, 2*time.Minute, errors.New("signal: killed")).Error(); !strings.Contains(got, "2m0s") {
		t.Errorf("askError = %q, want it to name the bound too", got)
	}
}

// And a command that failed on its own merits is reported as itself: a
// deadline that had not expired must not be blamed for an exit status,
// or every "no such image" would read as a host too slow to pull one.
func TestACommandThatFailedOnItsOwnIsNotBlamedOnADeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	want := fmt.Errorf("exit status 125")
	if got := runError(ctx, time.Second, want); got != want {
		t.Errorf("runError = %v, want the command's own error %v unwrapped", got, want)
	}
	if got := askError(ctx, time.Hour, want); got != want {
		t.Errorf("askError = %v, want the command's own error %v unwrapped", got, want)
	}
}
