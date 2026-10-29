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
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// SandboxAccessMinter is the part of sandboxaccess.Acceptor
// K8sGranuleSandboxes uses: mint a sandbox's credentials when its pod is
// built, re-register its stored token hash across a restart, and revoke
// credentials when it is released or reaped.
type SandboxAccessMinter interface {
	Mint(sandbox string, ttl time.Duration) (sandboxaccess.Credentials, error)
	Register(sandbox, tokenHash string, notAfter time.Time) error
	Revoke(sandbox string)
}

// SandboxAccessStore persists each live k8s-granule sandbox's SHA-256 token
// digest and expiry time so a restarting daemon can re-register readoptable
// sandboxes before their wrappers reconnect. The raw token is never stored.
type SandboxAccessStore interface {
	SetSandboxAccess(ctx context.Context, sandbox, tokenHash string, notAfter time.Time) error
	DeleteSandboxAccess(ctx context.Context, sandbox string) error
	GetSandboxAccess(ctx context.Context, sandbox string) (model.SandboxAccessCredential, bool, error)
	SandboxAccessCredentials(ctx context.Context) ([]model.SandboxAccessCredential, error)
}

// K8sGranuleAccess turns on granule's dial-out sandbox access channel
// for every pod K8sGranuleSandboxes builds
// (docs/design/grain-on-gke.md, "Credentials, per sandbox").
type K8sGranuleAccess struct {
	// Acceptor mints, registers, and revokes credentials.
	Acceptor SandboxAccessMinter
	// Store persists each sandbox's token hash and expiry across restarts.
	Store SandboxAccessStore
	// Dial is the address wrappers dial: grain-access.<ns>.svc:4244.
	Dial string
	// ControllerIPs are the acceptor's addresses as a guest would see
	// them -- the grain-access Service's ClusterIP and the grain pod's own
	// IP. granule's netshim drops every guest-originated packet to them
	// in CHV_NET_MODE=inline, so the guest has no route to the acceptor
	// even though it shares the pod's IP; a dial address that is a DNS
	// name cannot tell granule them by itself.
	ControllerIPs []string
	// Life is the fallback credential lifetime when a sandbox's Shape.Life
	// is zero (sandboxaccess.MaxCredentialLife when zero, and never more).
	Life time.Duration
}

func (a *K8sGranuleAccess) credentialLife(shapeLife time.Duration) time.Duration {
	ttl := shapeLife
	if ttl <= 0 && a != nil {
		ttl = a.Life
	}
	if ttl <= 0 || ttl > sandboxaccess.MaxCredentialLife {
		ttl = sandboxaccess.MaxCredentialLife
	}
	return ttl
}

// Live reports whether sandbox is live in this backend's own view --
// acquired, readopted, awaiting readoption after a restart, or being built --
// which is what the acceptor admits a wrapper on. Building and readoptable
// count: a wrapper dials as soon as its container starts (before the guest is
// ready) and reconnects immediately when grain restarts (before the adoption
// pass reaches Readopt).
func (k *K8sGranuleSandboxes) Live(sandbox string) bool {
	pod, err := k.PodNameFor(sandbox)
	if err != nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.live[sandbox] != nil || k.readoptable[sandbox] || k.building[pod]
}

// mintAccess mints sandbox's credentials, persists the token's SHA-256 hash
// and expiry in Access.Store, and adds the wrapper's dial-out env and the
// read-only Secret mount to opts. It returns the credentials to put in the
// Secret once the pod exists, or nil when access is off.
func (k *K8sGranuleSandboxes) mintAccess(ctx context.Context, sandbox, pod string, life time.Duration, opts *k8s.PodOptions) (*sandboxaccess.Credentials, error) {
	access := k.cfg.Access
	if access == nil || access.Acceptor == nil {
		return nil, nil
	}
	creds, err := access.Acceptor.Mint(sandbox, access.credentialLife(life))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: minting sandbox access credentials for %q: %w", sandbox, err)
	}
	if access.Store != nil {
		if err := access.Store.SetSandboxAccess(ctx, sandbox, sandboxaccess.TokenHash(creds.Token), creds.NotAfter); err != nil {
			access.Acceptor.Revoke(sandbox)
			return nil, fmt.Errorf("orchestrator: storing sandbox access credentials for %q: %w", sandbox, err)
		}
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

// RegisterStoredAccess restores every readoptable sandbox's stored token hash
// onto the acceptor and marks those sandboxes readoptable in Live, so a
// wrapper reconnecting immediately after a grain restart is admitted before
// the adoption pass reaches Readopt.
func (k *K8sGranuleSandboxes) RegisterStoredAccess(ctx context.Context) error {
	if k == nil || k.cfg.Access == nil || k.cfg.Access.Store == nil || k.cfg.Access.Acceptor == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	creds, err := k.cfg.Access.Store.SandboxAccessCredentials(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: loading stored sandbox access credentials: %w", err)
	}
	now := time.Now()
	for _, c := range creds {
		if !c.NotAfter.After(now) {
			_ = k.cfg.Access.Store.DeleteSandboxAccess(ctx, c.Sandbox)
			continue
		}
		if err := k.cfg.Access.Acceptor.Register(c.Sandbox, c.TokenHash, c.NotAfter); err != nil {
			return fmt.Errorf("orchestrator: registering stored sandbox access credential for %q: %w", c.Sandbox, err)
		}
		k.mu.Lock()
		if k.readoptable == nil {
			k.readoptable = map[string]bool{}
		}
		if k.live[c.Sandbox] == nil {
			k.readoptable[c.Sandbox] = true
		}
		k.mu.Unlock()
	}
	return nil
}

// registerSandboxAccess restores a single readopted sandbox's stored token
// hash onto the acceptor during Readopt.
func (k *K8sGranuleSandboxes) registerSandboxAccess(ctx context.Context, sandbox string) error {
	if k == nil || k.cfg.Access == nil || k.cfg.Access.Store == nil || k.cfg.Access.Acceptor == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c, ok, err := k.cfg.Access.Store.GetSandboxAccess(ctx, sandbox)
	if err != nil {
		return fmt.Errorf("orchestrator: loading stored sandbox access credential for %q: %w", sandbox, err)
	}
	if !ok {
		return nil
	}
	if !c.NotAfter.After(time.Now()) {
		_ = k.cfg.Access.Store.DeleteSandboxAccess(ctx, sandbox)
		return nil
	}
	if err := k.cfg.Access.Acceptor.Register(c.Sandbox, c.TokenHash, c.NotAfter); err != nil {
		return fmt.Errorf("orchestrator: registering stored sandbox access credential for %q: %w", sandbox, err)
	}
	return nil
}

// revokeAccess forgets sandbox's credentials in the acceptor and deletes its
// stored token hash, closing its channel.
func (k *K8sGranuleSandboxes) revokeAccess(ctx context.Context, sandbox string) {
	if k == nil || sandbox == "" {
		return
	}
	k.mu.Lock()
	delete(k.readoptable, sandbox)
	k.mu.Unlock()
	if k.cfg.Access == nil {
		return
	}
	if k.cfg.Access.Acceptor != nil {
		k.cfg.Access.Acceptor.Revoke(sandbox)
	}
	if k.cfg.Access.Store != nil {
		if ctx == nil {
			ctx = context.Background()
		} else {
			ctx = context.WithoutCancel(ctx)
		}
		_ = k.cfg.Access.Store.DeleteSandboxAccess(ctx, sandbox)
	}
}

// sweepOrphanAccess revokes and deletes stored credentials for any sandbox
// that this backend is neither holding live nor currently building.
func (k *K8sGranuleSandboxes) sweepOrphanAccess(ctx context.Context) {
	if k == nil {
		return
	}
	candidates := map[string]bool{}
	k.mu.Lock()
	for sb := range k.readoptable {
		candidates[sb] = true
	}
	k.mu.Unlock()
	if k.cfg.Access != nil && k.cfg.Access.Store != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		if creds, err := k.cfg.Access.Store.SandboxAccessCredentials(ctx); err == nil {
			for _, c := range creds {
				candidates[c.Sandbox] = true
			}
		}
	}
	for sb := range candidates {
		pod, err := k.PodNameFor(sb)
		k.mu.Lock()
		held := k.live[sb] != nil || (err == nil && k.building[pod])
		k.mu.Unlock()
		if !held {
			k.revokeAccess(ctx, sb)
		}
	}
}
