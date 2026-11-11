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

// A person's ID is often an e-mail address: its '@' is not the head's, so
// a Submit naming no head reads as one naming none, by whom it was, and
// when -- not as a Submit of the head "example.com|...".
func TestHumanSubmittedByTargetWithAnEmailActor(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ by, head, wantHead string }{
		{"alice@example.com", "", ""},
		{"alice@example.com", "ABCdef0123", "abcdef0123"},
		{"user:bob@example.com", "0123abcd", "0123abcd"},
	} {
		target := HumanSubmittedByTarget(tc.by, at, tc.head)
		if got := HumanSubmittedHead(target); got != tc.wantHead {
			t.Errorf("HumanSubmittedHead(%q) = %q, want %q", target, got, tc.wantHead)
		}
		if got := HumanSubmittedBy(target); got != tc.by {
			t.Errorf("HumanSubmittedBy(%q) = %q, want %q", target, got, tc.by)
		}
		if got, ok := HumanSubmittedAt(target); !ok || !got.Equal(at) {
			t.Errorf("HumanSubmittedAt(%q) = %v, %v, want %v", target, got, ok, at)
		}
	}
}
