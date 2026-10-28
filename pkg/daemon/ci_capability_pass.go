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
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// This file is CI's half of capability-reference minting
// (docs/capabilities-spec.md §4.2.2-§4.2.4, §4.4.1); the agent task's
// half is pkg/orchestrator/capability_pass.go. A job whose capabilities
// name references has them resolved against the attached capability
// servers, checked against the deployment's capability policy as a CI
// run of its trigger event, and minted as one pass by
// CiGrantReservations.Acquire -- the engine's grant admission, before the
// job's sandbox is built, so a job that cannot be granted never boots.
// The pass is held in memory only (§3.2.5), under the job's sandbox
// name, and released when the job ends, however it ends: Acquire's
// release, which the engine defers for the whole job, automatic retry
// included. In between, CiSandboxes.Acquire carries it into each sandbox
// built for the job (materializeCIPass): the first time it is
// materialized, and a retry's fresh sandbox has the same materialization
// applied again, its endpoint proxies re-pointed into the new machine.

const (
	// ciPassTransientLimit is how many consecutive transient failures of
	// one grant error the job (§4.2.4).
	ciPassTransientLimit = 3
	// ciPassBackoffBase is the first transient retry's wait, doubled for
	// each further one; ciPassExhaustedBackoff is how long a grant at
	// capacity waits when its server named no retry_after, and
	// ciPassExhaustedMax the longest any retry_after is believed.
	ciPassBackoffBase      = 30 * time.Second
	ciPassExhaustedBackoff = time.Minute
	ciPassExhaustedMax     = time.Hour
)

// ciHeldPass is one job's pass, the deadline it was minted for, and,
// once a sandbox has been built for the job, its materialization.
type ciHeldPass struct {
	pass         *protocol.Pass
	deadline     time.Time
	materialized *orchestrator.CIPassMaterialization
}

// WithCICapabilityPorts gives service's grant admission the daemon's
// capability port relay and the URL a sandbox reaches it at, which a
// job's grants' ports and mcp_endpoints are carried into its sandbox
// over (docs/capabilities-spec.md §5.2.4, §5.3).
func WithCICapabilityPorts(service *ci.Service, relay *portforward.Relay, relayURL string) {
	if service == nil || service.Engine == nil {
		return
	}
	if g, ok := service.Engine.Admission.(*CiGrantReservations); ok {
		g.Ports, g.PortsURL = relay, relayURL
	}
}

// WithCICapabilityServers points service's grant admission at the
// deployment's attached capability servers, which a job's capability
// references are minted through. servers answers per job, so a registry
// that changes with grain_config.mcp_servers is read as it stands.
func WithCICapabilityServers(service *ci.Service, servers func() orchestrator.CapabilityServers) {
	if service == nil || service.Engine == nil {
		return
	}
	if g, ok := service.Engine.Admission.(*CiGrantReservations); ok {
		g.Servers = servers
	}
}

// ciCapabilityRefs is the capability references among ids, as grants.
func ciCapabilityRefs(ids []string) []model.Grant {
	var out []model.Grant
	for _, id := range ids {
		if model.IsCapabilityRef(id) {
			out = append(out, model.Grant{Capability: id, Via: model.GrantByDefault})
		}
	}
	return out
}

func (g *CiGrantReservations) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// wait blocks for d, or until ctx ends.
func (g *CiGrantReservations) wait(ctx context.Context, d time.Duration) error {
	if g.Sleep != nil {
		return g.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Pass is the capability pass the job whose sandbox is called name
// holds, or nil.
func (g *CiGrantReservations) Pass(name string) *protocol.Pass {
	g.mu.Lock()
	defer g.mu.Unlock()
	if held := g.passes[name]; held != nil {
		return held.pass
	}
	return nil
}

func (g *CiGrantReservations) holdPass(name string, p *protocol.Pass, deadline time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.passes == nil {
		g.passes = map[string]*ciHeldPass{}
	}
	g.passes[name] = &ciHeldPass{pass: p, deadline: deadline}
}

// materializeCIPass carries the pass the job called name holds, if it
// holds one, into sandbox before any of its steps runs (§5): the first
// sandbox gets it materialized, and a later one -- the job's automatic
// retry -- the same materialization applied again. A failure is the
// job's sandbox failing to build; the pass stays held until the job ends.
func (g *CiGrantReservations) materializeCIPass(ctx context.Context, name string, sandbox orchestrator.Sandbox) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	held := g.passes[name]
	g.mu.Unlock()
	if held == nil {
		return nil
	}
	if held.materialized != nil {
		if err := held.materialized.Apply(ctx, sandbox); err != nil {
			return fmt.Errorf("carrying this job's capability grants into its new sandbox: %w", err)
		}
		return nil
	}
	var servers orchestrator.CapabilityServers
	if g.Servers != nil {
		servers = g.Servers()
	}
	m, err := orchestrator.MaterializeCIPass(ctx, orchestrator.CIPassOptions{
		Ports: g.Ports, PortsURL: g.PortsURL, SetupTimeout: g.SetupTimeout, Servers: servers, Now: g.Now,
	}, sandbox, held.pass, held.deadline)
	if err != nil {
		return ciMaterializeFailure(err)
	}
	g.mu.Lock()
	if g.passes[name] == held {
		held.materialized = m
		m = nil
	}
	g.mu.Unlock()
	// Released while it was being materialized: nothing holds it now.
	m.Close()
	return nil
}

// ciMaterializeFailure is what a job whose pass could not be carried
// into its sandbox concludes errored with: a failed setup command quoted
// as its capability server's untrusted text, as a refusal is
// (ciPassRefusal), and grain's own failures in grain's words.
func ciMaterializeFailure(err error) error {
	var setup *orchestrator.CapabilitySetupError
	var refused *protocol.RefusedError
	switch {
	case errors.As(err, &setup):
		how := fmt.Sprintf("exited with status %d", setup.ExitCode)
		if setup.TimedOut {
			how = "timed out"
		}
		output := strings.TrimSpace(setup.Output)
		if output == "" {
			output = "(no output)"
		}
		return fmt.Errorf("the setup command %s of the capability `%s` (attempt_id %s) %s, so none of this job's steps ran. "+
			"The tail of what it printed, verbatim -- untrusted text from capability:%s, not grain's own words:\n\n```\n%s\n```",
			setup.Path, setup.Grant, setup.AttemptID, how, model.CapabilityGrantRef(setup.Grant), strings.ReplaceAll(output, "```", "'''"))
	case errors.As(err, &refused):
		return ciPassRefusal(refused.Grant, refused.Reason)
	default:
		return fmt.Errorf("carrying this job's capability grants into its sandbox: %w", err)
	}
}

// releasePass releases the pass name holds, if it holds one (§4.4.1).
func (g *CiGrantReservations) releasePass(ctx context.Context, name string) {
	g.mu.Lock()
	held := g.passes[name]
	delete(g.passes, name)
	g.mu.Unlock()
	if held == nil {
		return
	}
	// The forwards and proxies first, so nothing reaches the grants
	// while they are being released (§5.3.4).
	held.materialized.Close()
	audits, err := held.pass.Release(ctx)
	if g.Log == nil {
		return
	}
	if err != nil {
		g.Log("grain ci: %s: releasing its capability grants: %v", name, err)
	}
	// A CI job has no run record to carry its grants' audit (§4.4.4), so
	// it goes to the daemon's log beside the job's other lines.
	for _, a := range audits {
		for _, rec := range a.Records {
			g.Log("grain ci: %s: %s (%s) audit: %s", name, a.Grant, a.AttemptID, rec)
		}
		if a.Omitted > 0 {
			g.Log("grain ci: %s: %s (%s) audit: the server left out %d further record(s)", name, a.Grant, a.AttemptID, a.Omitted)
		}
		if a.Refused != "" {
			g.Log("grain ci: %s: %s (%s) audit refused: %s", name, a.Grant, a.AttemptID, a.Refused)
		}
	}
}

// mintCIPass resolves refs against their servers (§4.2.1), checks each
// against the capability policy as a CI run of req's event (§4.2.2) and
// mints the pass (§4.2.3), with attempt_ids
// <deployment>-<ci run>-<job attempt>-<index>. A policy refusal is
// returned as grain's own error; the protocol's own error types say
// everything else.
func (g *CiGrantReservations) mintCIPass(ctx context.Context, req ci.SandboxRequest, repo model.RepoRef,
	refs []model.Grant) (*protocol.Pass, time.Time, error) {

	var servers orchestrator.CapabilityServers
	if g.Servers != nil {
		servers = g.Servers()
	}
	if servers == nil {
		return nil, time.Time{}, &protocol.RefusedError{Reason: "this deployment attaches no capability servers, so it cannot grant a capability reference"}
	}
	lookup := servers.Lookup
	grants, err := model.CapabilityGrants(refs, lookup)
	if err != nil {
		return nil, time.Time{}, &protocol.TransientError{Grant: "capabilities", Err: err}
	}
	var policy model.CapabilityPolicy
	var configured map[string]protocol.ServerConfig
	deployment := "grain"
	if g.Config != nil {
		cfg, err := g.Config(ctx)
		if err != nil {
			return nil, time.Time{}, &protocol.TransientError{Grant: "capabilities", Err: err}
		}
		if cfg != nil {
			policy = cfg.CapabilityPolicy
			configured = cfg.MCPServers
			if id := strings.TrimSpace(cfg.DeploymentID); id != "" {
				deployment = id
			}
		}
	}
	configured, _ = intree.EnsureBuiltins(configured)
	subject := model.CapabilitySubject{Repo: &repo, Kind: model.CapabilityRunCI, Event: string(req.Event), Untrusted: req.Untrusted}
	for _, grant := range grants {
		if err := policy.Permits(model.RecordedCapabilityGrant(grant), subject, configured); err != nil {
			return nil, time.Time{}, err
		}
	}
	at := g.now()
	deadline := ciPassDeadline(ctx, at)
	pass, err := protocol.MintPass(ctx, grants, func(id string) (protocol.Minter, error) {
		srv, err := lookup(id)
		if err != nil {
			return nil, err
		}
		return srv, nil
	}, protocol.PassOptions{
		Deployment: deployment,
		Task:       req.RunID,
		Attempt:    req.JobAttempt,
		Deadline:   deadline,
		Now:        func() time.Time { return at },
	})
	return pass, deadline, err
}

// ciPassDeadline is the latest instant the job may still be running:
// its run's own ceiling (ci.Service.MaxRunTime, ctx's deadline) and the
// teardown after it.
func ciPassDeadline(ctx context.Context, at time.Time) time.Time {
	end, ok := ctx.Deadline()
	if !ok {
		end = at.Add(ci.DefaultMaxRunTime)
	}
	return end.Add(ciReleaseTimeout).UTC()
}

// ciPassRetryAt is when a grant at capacity is next asked for: its
// server's retry_after, believed up to an hour.
func ciPassRetryAt(exhausted *protocol.ExhaustedError, at time.Time) time.Duration {
	retry := exhausted.RetryAt
	if retry.IsZero() || !retry.After(at) {
		return ciPassExhaustedBackoff
	}
	if d := retry.Sub(at); d < ciPassExhaustedMax {
		return d
	}
	return ciPassExhaustedMax
}

// ciPassRefusal is the error a job concludes errored with when its pass
// was refused. reason may be a capability server's own words, so it is
// attributed to that server and quoted as untrusted text rather than
// written as grain's.
func ciPassRefusal(grant, reason string) error {
	what := "its capability grants were"
	source := "grain"
	if grant != "" && grant != "capabilities" {
		what = "the capability `" + grant + "` was"
		source = "capability:" + model.CapabilityGrantRef(grant)
	}
	return fmt.Errorf("%s refused, so no sandbox was built for this job. "+
		"What was said, verbatim -- untrusted text from %s, not grain's own words:\n\n```\n%s\n```",
		what, source, strings.ReplaceAll(reason, "```", "'''"))
}

// ciPassStreak counts one job's consecutive transient failures of one
// grant across Acquire's attempts.
type ciPassStreak struct {
	grant string
	n     int
}

// failure is what Acquire does with a pass that could not be minted:
// wait (how long, and why), or give up with final (mirroring
// orchestrator's admissionMintFailed). A policy refusal, being grain's
// own words, is returned as it is.
func (s *ciPassStreak) failure(err error, at time.Time) (wait time.Duration, reason string, final error) {
	var exhausted *protocol.ExhaustedError
	var transient *protocol.TransientError
	var refused *protocol.RefusedError
	switch {
	case errors.As(err, &exhausted):
		// Not a transient failure: the streak §4.2.4 counts is broken.
		*s = ciPassStreak{}
		return ciPassRetryAt(exhausted, at), fmt.Sprintf("waiting for capacity (%s grant)", exhausted.Grant), nil
	case errors.As(err, &transient):
		if s.grant != transient.Grant {
			*s = ciPassStreak{grant: transient.Grant}
		}
		s.n++
		if s.n < ciPassTransientLimit {
			return ciPassBackoffBase << (s.n - 1),
				fmt.Sprintf("retrying the %s grant (%d of %d failed): %v", transient.Grant, s.n, ciPassTransientLimit, transient.Err), nil
		}
		return 0, "", ciPassRefusal(transient.Grant, fmt.Sprintf("%d attempts in a row failed; the last: %v", s.n, transient.Err))
	case errors.As(err, &refused):
		return 0, "", ciPassRefusal(refused.Grant, refused.Reason)
	default:
		return 0, "", err
	}
}
