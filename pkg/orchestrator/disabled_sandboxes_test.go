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

package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestDisabledSandboxesAcquireFails(t *testing.T) {
	t.Parallel()
	sb := NewDisabledSandboxes()
	_, err := sb.Acquire(context.Background(), "test-run", Shape{})
	if err == nil {
		t.Fatal("Acquire = nil, want error when native sandboxes are disabled")
	}
	if !strings.Contains(err.Error(), "native sandboxes are disabled") {
		t.Fatalf("Acquire error = %q, want it to mention native sandboxes are disabled", err)
	}
}

func TestDisabledSandboxesNetworkAndOrphans(t *testing.T) {
	t.Parallel()
	sb := NewDisabledSandboxes()
	if !sb.IsolatesNetwork() {
		t.Error("IsolatesNetwork = false, want true")
	}
	reaped, err := sb.ReapOrphans(context.Background())
	if err != nil || reaped != 0 {
		t.Fatalf("ReapOrphans = (%d, %v), want (0, nil)", reaped, err)
	}
}
