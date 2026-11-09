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

import "testing"

// Core offers no bare in-process capability any more: every legacy
// provider was retired onto a capability server method, an api preset or
// a task setting, and the seam an edition contributed one through
// (capability.RegisterProvider) is gone. A row here would be a grant no
// provider answers, which prepareCapabilities refuses at dispatch.
func TestCoreOffersNoLegacyCapabilities(t *testing.T) {
	if got := OfferedCapabilities(); len(got) != 0 {
		t.Errorf("OfferedCapabilities() = %+v, want no legacy rows", got)
	}
}
