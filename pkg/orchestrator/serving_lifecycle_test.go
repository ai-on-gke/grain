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

package orchestrator_test

// The seam between pkg/orchestrator and pkg/lifecycle, pinned from a test
// so that neither package imports the other in production.
//
// It is Go's structural typing doing the work: orchestrator.ServingSandbox
// is what a serving sandbox can do, lifecycle.Serving is the pair a
// registry needs of one, and as long as the first is a superset of the
// second, wiring them together in cmd/grain is a ServeSpec built from a
// ServeRequest and nothing more. This test is what notices if a method on
// either side is renamed.

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/lifecycle"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A serving sandbox is something a registry can serve a version from.
var _ lifecycle.Serving = orchestrator.ServingSandbox(nil)

// And it can say what the server wrote, which is the difference between a
// readiness timeout an operator can act on and one they cannot.
var _ lifecycle.ServingOutput = orchestrator.ServingSandbox(nil)

// The granule backend is a backend that can hold one.
var _ orchestrator.ServingSandboxes = (*orchestrator.GranuleSandboxes)(nil)

// TestTheSeamNeedsNoAdapter is the assertions above, as something that
// shows up in a test run rather than only in a build.
func TestTheSeamNeedsNoAdapter(t *testing.T) {
	if _, ok := any(orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})).(orchestrator.ServingSandboxes); !ok {
		t.Fatal("the granule backend no longer serves")
	}
}
