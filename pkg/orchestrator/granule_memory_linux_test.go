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

package orchestrator

import (
	"testing"

	"github.com/bwsalmon/granule/pkg/vmspec"
)

// GranuleMemoryLimitMB overrides granule's own derived container limit
// (vmspec.VMSpec.ContainerMemoryLimitMB) with grain's larger,
// budget-charged headroom. That is only right while grain's is the
// larger: a granule resync whose rule outgrows it (gke-labs/granule#238
// took it to guest + max(512 MiB, guest/16)) would have grain pass a
// limit below what granule itself thinks the VMM needs. vmspec imports
// granule's Linux-only netshim, hence the build tag.
func TestGranuleMemoryLimitIsNeverBelowGranulesOwn(t *testing.T) {
	t.Parallel()
	for _, guestMB := range []int{512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 262144} {
		got := GranuleMemoryLimitMB(guestMB, 0)
		if granules := (vmspec.VMSpec{MemoryMB: guestMB}).ContainerMemoryLimitMB(); got < granules {
			t.Errorf("GranuleMemoryLimitMB(%d, 0) = %d, below the vendored granule's own %d: raise model.DefaultVMMHeadroom to at least granule's rule", guestMB, got, granules)
		}
	}
}
