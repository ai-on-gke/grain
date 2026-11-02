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
	"errors"
	"strings"
	"testing"
)

func TestDisabledSandboxesAcquireFails(t *testing.T) {
	t.Parallel()
	sb := NewDisabledSandboxes()
	_, err := sb.Acquire(context.Background(), "test-run", Shape{})
	if !errors.Is(err, ErrNoSandboxBackend) {
		t.Fatalf("Acquire error = %v, want ErrNoSandboxBackend", err)
	}
	// The reason a run's failure shows: it names the backend, says
	// nothing runs here and how to change that -- not the remote
	// executors this mode was once for, which no longer exist.
	for _, want := range []string{`sandbox backend is "none"`, "no task can run here", "Settings -> Sandboxes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Acquire error = %q, want it to mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "remote") {
		t.Errorf("Acquire error = %q still mentions remote execution", err)
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
