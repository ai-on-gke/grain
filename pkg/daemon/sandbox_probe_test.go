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
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSandboxProbeAdapterMapsUnsupportedBackendToNotImplemented(t *testing.T) {
	t.Parallel()
	adapter := sandboxProbeAdapter{
		sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
	}
	if adapter.SandboxProbeAvailable() {
		t.Fatal("SandboxProbeAvailable = true for host sandboxes, want false")
	}
	_, err := adapter.ProbeSandbox(context.Background(), ui.SandboxProbeRequest{
		ID:   "walk-check",
		Argv: []string{"true"},
	})
	if err == nil {
		t.Fatal("ProbeSandbox succeeded on host sandboxes, want error")
	}
	if !errors.Is(err, orchestrator.ErrSandboxProbeUnsupported) {
		t.Errorf("err = %v, want wrapping orchestrator.ErrSandboxProbeUnsupported", err)
	}
	var nie *ui.NotImplementedError
	if !errors.As(err, &nie) {
		t.Errorf("err = %T (%v), want *ui.NotImplementedError", err, err)
	}
}
