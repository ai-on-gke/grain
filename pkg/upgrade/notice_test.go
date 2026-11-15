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

import (
	"reflect"
	"testing"
)

// Notices come back in the order their sources were registered, without
// any that say nothing, and an unregistered source stops contributing.
func TestNoticesFollowRegistrationOrder(t *testing.T) {
	if got := Notices(); len(got) != 0 {
		t.Fatalf("Notices() = %v with nothing registered", got)
	}
	first := RegisterNotice(func() []Notice {
		return []Notice{{Kind: "a", Message: "first"}, {Kind: "blank"}}
	})
	t.Cleanup(first)
	second := RegisterNotice(func() []Notice {
		return []Notice{{Kind: "b", Message: "second", Command: "fix-it"}}
	})
	t.Cleanup(second)

	want := []Notice{{Kind: "a", Message: "first"}, {Kind: "b", Message: "second", Command: "fix-it"}}
	if got := Notices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Notices() = %v, want %v", got, want)
	}
	first()
	if got := Notices(); !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("after unregistering the first, Notices() = %v, want %v", got, want[1:])
	}
}
