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

package workflows

import "testing"

// A cancellation somebody asked for names them (operator-attribution
// spec §1.3.2), and reads as by hand whatever the name; grain's own does
// not.
func TestCancelByPrincipal(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want CancelSource
	}{
		{"alice@example.com", "alice@example.com"},
		{" 900 ", "900"},
		{"", CancelByOperator},
		{"  ", CancelByOperator},
	} {
		got := CancelByPrincipal(tc.id)
		if got != tc.want || !got.ByHand() {
			t.Errorf("CancelByPrincipal(%q) = %q (by hand %v), want %q by hand", tc.id, got, got.ByHand(), tc.want)
		}
	}
	if CancelBySystem.ByHand() {
		t.Error("CancelBySystem reads as by hand")
	}
}
