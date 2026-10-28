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

//go:build linux

package hosttop_test

import "testing"

// One sample, not both, and procps spells the line that opens one
// "top - 10:11:31 up 3 days, ..." (hosttop_linux.go's sampleHeader).
func TestReadKeepsOnlyTheLastProcpsSample(t *testing.T) {
	keptOneSample(t, "top - ")
}
