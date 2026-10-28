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
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// SandboxAccessMinter is the part of sandboxaccess.Acceptor
// K8sGranuleSandboxes uses: mint a sandbox's credentials when its pod is
// built, and revoke them when it is released.
type SandboxAccessMinter interface {
	Mint(sandbox string, ttl time.Duration) (sandboxaccess.Credentials, error)
	Revoke(sandbox string)
}

// K8sGranuleAccess turns on granule's dial-out sandbox access channel
// for every pod K8sGranuleSandboxes builds
// (docs/design/grain-on-gke.md, "Credentials, per sandbox").
type K8sGranuleAccess struct {
	// Acceptor mints and revokes credentials.
	Acceptor SandboxAccessMinter
	// Dial is the address wrappers dial: grain-access.<ns>.svc:7443.
	Dial string
	// ControllerIPs are the acceptor's addresses as a guest would see
	// them -- the grain-access Service's ClusterIP and the grain pod's own
	// IP. granule's netshim drops every guest-originated packet to them
	// in CHV_NET_MODE=inline, so the guest has no route to the acceptor
	// even though it shares the pod's IP; a dial address that is a DNS
	// name cannot tell granule them by itself.
	ControllerIPs []string
	// Life is how long credentials are minted for
	// (sandboxaccess.MaxCredentialLife when zero, and never more).
	Life time.Duration
}

// Live reports whether sandbox is live in this backend's own view --
// acquired, readopted, or being built -- which is what the acceptor
// admits a wrapper on. Building counts: a wrapper dials as soon as its
// container starts, before the guest is ready.
func (k *K8sGranuleSandboxes) Live(sandbox string) bool {
	pod, err := k.PodNameFor(sandbox)
	if err != nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.live[sandbox] != nil || k.building[pod]
}

// mintAccess mints sandbox's credentials and adds the wrapper's dial-out
// env and the read-only Secret mount to opts. It returns the
// credentials to put in the Secret once the pod exists, or nil when
// access is off.
func (k *K8sGranuleSandboxes) mintAccess(sandbox, pod string, opts *k8s.PodOptions) (*sandboxaccess.Credentials, error) {
	access := k.cfg.Access
	if access == nil || access.Acceptor == nil {
		return nil, nil
	}
	creds, err := access.Acceptor.Mint(sandbox, access.Life)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: minting sandbox access credentials for %q: %w", sandbox, err)
	}
	env := map[string]string{}
	for key, value := range opts.Env {
		env[key] = value
	}
	for key, value := range sandboxaccess.WrapperEnv(access.Dial, sandbox, access.ControllerIPs) {
		env[key] = value
	}
	opts.Env = env
	// The pod's one container is the wrapper (granule-shim, which boots
	// the VM); the guest has its own disk and never sees the container's
	// filesystem, so this mount reaches the wrapper only.
	opts.SecretMounts = append(opts.SecretMounts, k8s.SecretMount{
		Secret:    sandboxaccess.SecretName(pod),
		MountPath: sandboxaccess.MountDir,
	})
	return &creds, nil
}

// createAccessSecret puts creds in pod's Secret, owned by the pod so it
// never outlives it.
func (k *K8sGranuleSandboxes) createAccessSecret(ctx context.Context, pod string, creds *sandboxaccess.Credentials) error {
	uid, err := k.client.PodUID(ctx, pod)
	if err != nil {
		return err
	}
	return k.pods().CreateSecret(ctx, sandboxaccess.SecretName(pod), pod, uid, creds.SecretData())
}

// revokeAccess forgets sandbox's credentials, closing its channel.
func (k *K8sGranuleSandboxes) revokeAccess(sandbox string) {
	if k.cfg.Access != nil && k.cfg.Access.Acceptor != nil {
		k.cfg.Access.Acceptor.Revoke(sandbox)
	}
}
