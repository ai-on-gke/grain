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
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SandboxProbePrefix is put in front of every probe's id to make the
// sandbox's name. A run's sandbox is named "<task id>-<attempt>"
// (dispatch.RunID), which never starts with a letter, so a probe can
// never be handed -- or, on a failed Acquire, delete -- the pod of a
// live run.
const SandboxProbePrefix = "probe-"

// SandboxProbeRequest is one `grain sandbox probe`: acquire a sandbox
// called SandboxProbePrefix+ID, run Argv in it, release it.
type SandboxProbeRequest struct {
	// ID names the probe. It is part of the sandbox pod's name, which is
	// what scripts/deploy-gke.sh --check searches the API server's audit
	// log for afterwards.
	ID string
	// Access is the guest-access channel the caller expects the probe to
	// use: K8sGranuleAccessKubectl or K8sGranuleAccessDialout. Empty
	// accepts whichever the deployment is configured with. A deployment
	// configured otherwise is refused rather than probed, since a probe
	// that quietly went over kubectl exec would prove nothing about the
	// dial-out channel.
	Access string
	// Argv is run in the sandbox's guest. It must not be empty.
	Argv []string
}

// SandboxProbeResult is what a probe ran and what came back.
type SandboxProbeResult struct {
	Sandbox  string
	Pod      string
	Access   string
	Stdout   string
	Stderr   string
	ExitCode int
}

// sandboxProber is the backend a probe can run on: one whose sandboxes
// are pods with a known name reached over a known access channel --
// K8sGranuleSandboxes.
type sandboxProber interface {
	Sandboxes
	PodNameFor(sandbox string) (string, error)
	AccessMode() string
}

// AccessMode reports the guest-access channel this backend's sandboxes
// are reached over: K8sGranuleAccessDialout when the dial-out acceptor
// is configured and selected, K8sGranuleAccessKubectl otherwise.
func (k *K8sGranuleSandboxes) AccessMode() string {
	if k.isDialout() {
		return K8sGranuleAccessDialout
	}
	return K8sGranuleAccessKubectl
}

// ErrSandboxProbeUnsupported is returned by ProbeSandbox for a backend
// that is not K8sGranuleSandboxes.
var ErrSandboxProbeUnsupported = errors.New(
	"sandbox probes need the Kubernetes granule sandbox backend (-k8s-granule-sandboxes), which this daemon is not running")

// SandboxProbeDeps is what a probe is built and paid for through: the
// same backend, admission ledger and usage meter every other sandbox this
// deployment builds goes through, so a probe can neither overcommit the
// machine nor go missing from its lifetime counts.
type SandboxProbeDeps struct {
	Sandboxes Sandboxes
	Meter     SandboxMeter
	// Ledger and Budget admit the probe as an agent-shaped sandbox
	// (model.PoolAgent, held under the sandbox's own name -- which is
	// what sandboxPoolAndHolder reads a live "probe-..." sandbox back
	// as). A Budget that is not Known, or a nil Ledger, admits it
	// unconditionally, as it does a run.
	Ledger *model.Ledger
	Budget model.Budget
	// Shape is the deployment's agent shape (Config.AgentShape), the
	// commitment charged for the probe; the backend builds it at its own
	// default shape, which is the same thing.
	Shape model.Commitment
	// PackageCache is handed to PrepareSandbox, so a probe's sandbox is
	// set up exactly as a run's is before its command runs. The zero
	// value prepares nothing.
	PackageCache PackageCacheSource
}

// ProbeSandbox admits and acquires one sandbox from deps.Sandboxes at the
// deployment's default shape, runs req.Argv in its guest over the
// backend's own access channel -- the same transport a run's tool calls
// use -- and releases it. It is what `grain sandbox probe` asks the
// daemon for, so that scripts/deploy-gke.sh --check exercises the real
// sandbox path rather than reasoning about it.
//
// A command that ran and exited non-zero is not an error: the result
// carries its exit code. An error means no command ran -- the backend
// cannot probe, the request was malformed, the budget has no room for
// one more sandbox, or the sandbox could not be acquired.
func ProbeSandbox(ctx context.Context, deps SandboxProbeDeps, req SandboxProbeRequest) (SandboxProbeResult, error) {
	prober, ok := deps.Sandboxes.(sandboxProber)
	if !ok {
		return SandboxProbeResult{}, ErrSandboxProbeUnsupported
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return SandboxProbeResult{}, errors.New("a probe id is required")
	}
	if len(req.Argv) == 0 {
		return SandboxProbeResult{}, errors.New("a command to run in the probe sandbox is required")
	}
	mode := prober.AccessMode()
	switch req.Access {
	case "", mode:
	case K8sGranuleAccessKubectl, K8sGranuleAccessDialout:
		return SandboxProbeResult{}, fmt.Errorf("this deployment reaches sandboxes over %q (-k8s-granule-access), not %q", mode, req.Access)
	default:
		return SandboxProbeResult{}, fmt.Errorf("unknown access mode %q: want %q or %q", req.Access, K8sGranuleAccessKubectl, K8sGranuleAccessDialout)
	}
	name := SandboxProbePrefix + id
	pod, err := prober.PodNameFor(name)
	if err != nil {
		return SandboxProbeResult{}, err
	}
	if deps.Budget.Known() && deps.Ledger != nil {
		verdict, release := deps.Ledger.Admit(deps.Budget, model.PoolAgent, name, deps.Shape)
		if !verdict.OK {
			return SandboxProbeResult{}, fmt.Errorf("no room in the sandbox budget for probe sandbox %q: %s", name, verdict.Reason)
		}
		defer release()
	}
	sb, finished, err := deps.Meter.Acquire(ctx, prober, model.SandboxKindAgent, name, Shape{})
	if err != nil {
		return SandboxProbeResult{}, fmt.Errorf("acquiring probe sandbox %q: %w", name, err)
	}
	defer finished(context.WithoutCancel(ctx))
	PrepareSandbox(ctx, deps.PackageCache, sb)
	result := SandboxProbeResult{Sandbox: name, Pod: pod, Access: mode}
	cmd, ok := sb.(SandboxCommander)
	if !ok {
		_ = sb.Release(ctx)
		return SandboxProbeResult{}, fmt.Errorf("probe sandbox %q cannot run a command directly", name)
	}
	result.Stdout, result.Stderr, result.ExitCode = cmd.RunInSandbox(ctx, req.Argv, "")
	if err := sb.Release(ctx); err != nil {
		return result, fmt.Errorf("releasing probe sandbox %q: %w", name, err)
	}
	return result, nil
}
