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

package model

import (
	"testing"
	"time"
)

func TestHumanSubmittedTargetRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.FixedZone("x", 3600))
	for _, tc := range []struct{ head, target, want string }{
		{"ABCdef0123", "2026-10-25T11:00:00Z@abcdef0123", "abcdef0123"},
		{"  ", "2026-10-25T11:00:00Z", ""},
	} {
		got := HumanSubmittedTarget(at, tc.head)
		if got != tc.target {
			t.Errorf("HumanSubmittedTarget(%q) = %q, want %q", tc.head, got, tc.target)
		}
		if head := HumanSubmittedHead(got); head != tc.want {
			t.Errorf("HumanSubmittedHead(%q) = %q, want %q", got, head, tc.want)
		}
	}
}
