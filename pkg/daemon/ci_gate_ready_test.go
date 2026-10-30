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
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// grain/task-1863: before runDaemon publishes the CI service, CiGate's
// Get answers false, which is "not up yet" rather than "forgotten" --
// and Ready is how the API tells the two apart.
func TestCiGateSaysItIsNotUpBeforeLiveCIIsPublished(t *testing.T) {
	old := LiveCI.Load()
	t.Cleanup(func() { LiveCI.Store(old) })

	LiveCI.Store(nil)
	var gate CiGate
	if ci.IsUp(gate) {
		t.Error("CiGate is up with no CI service published")
	}
	if _, ok := gate.Get("ci-1"); ok {
		t.Error("CiGate.Get found a run with no CI service published")
	}
	if _, err := gate.Start(context.Background(), ci.Request{}); !ci.IsNotUp(err) {
		t.Errorf("Start err = %v, want ci.ErrNotUp", err)
	}

	LiveCI.Store(ci.NopRunner{})
	if !ci.IsUp(gate) {
		t.Error("CiGate is not up with a CI service published")
	}
}
