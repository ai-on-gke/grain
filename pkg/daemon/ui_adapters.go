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
	"fmt"
	"slices"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/agent/credcheck"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/sysstat"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// sandboxHealthAdapter adapts orchestrator's own SandboxHealth (a core
// dispatch type) onto ui.SandboxSnapshot (a presentation DTO) field by
// field -- the one place both types are ever in scope together, so
// neither package needs to import the other (see ui/sandbox_health.go's
// own doc comment). inner is run()'s own sandboxes value; it may be nil
// (a test or a future backend with no Sandboxes yet) or simply not
// implement Health, either of which Health below reports as "nothing to
// show" rather than a panic.
type SandboxHealthAdapter struct {
	Inner  orchestrator.Sandboxes
	Limits func() model.Limits
}

func sandboxTerminationReason(s orchestrator.SandboxHealth) string {
	if s.Ready {
		return "running"
	}
	if s.Error != "" {
		return s.Error
	}
	return "unknown"
}

// SandboxCount carries the backend's cheap live allocation count through
// without asking Health to connect to every running guest.
func (a SandboxHealthAdapter) SandboxCount() int {
	reporter, ok := a.Inner.(interface{ SandboxCount() int })
	if !ok {
		return 0
	}
	return reporter.SandboxCount()
}

func (a SandboxHealthAdapter) Health(ctx context.Context) []ui.SandboxSnapshot {
	reporter, ok := a.Inner.(interface {
		Health(context.Context) []orchestrator.SandboxHealth
	})
	if !ok {
		return nil
	}
	sandboxes := reporter.Health(ctx)
	out := make([]ui.SandboxSnapshot, len(sandboxes))
	for i, s := range sandboxes {
		out[i] = ui.SandboxSnapshot{
			Sandbox:           s.Sandbox,
			Kind:              s.Kind,
			Backend:           s.Backend,
			Name:              s.Name,
			CPUs:              s.CPUs,
			MemoryMB:          s.MemoryMB,
			DiskGB:            s.DiskGB,
			NetworkAccess:     s.NetworkAccess,
			Ready:             s.Ready,
			Error:             s.Error,
			TerminationReason: sandboxTerminationReason(s),
			LoadAverage:       s.LoadAverage,
			MemoryUsedMB:      s.MemoryUsedMB,
			MemoryTotalMB:     s.MemoryTotalMB,
			SwapUsedMB:        s.SwapUsedMB,
			SwapTotalMB:       s.SwapTotalMB,
			DiskUsedMB:        s.DiskUsedMB,
			DiskTotalMB:       s.DiskTotalMB,
			ProbeMS:           s.ProbeMS,
			ProbeAgeMS:        s.ProbeAgeMS,
			NestedVirt:        s.NestedVirt,
		}
		if !s.StartedAt.IsZero() {
			started := s.StartedAt
			out[i].StartedAt = &started
		}
	}
	return out
}

// BudgetAndReconciliation reports the deployment's sandbox admission budget,
// what the ledger currently has committed across pools, and the latest
// reconciliation against the backend's live sandbox list.
func (a SandboxHealthAdapter) BudgetAndReconciliation() (*ui.SandboxBudgetReport, *ui.SandboxReconciliationReport) {
	if a.Limits == nil {
		return nil, nil
	}
	limits := a.Limits()
	budget := limits.Budget
	ledger := limits.Ledger

	report := &ui.SandboxBudgetReport{
		Known:            budget.Known(),
		HostCPUs:         budget.Host.CPUs,
		HostMemoryMB:     budget.Host.MemoryMB,
		ReserveCPUs:      budget.ReserveCPUs,
		ReserveMemoryMB:  budget.ReserveMemoryMB,
		CPUOvercommit:    budget.CPUOvercommit,
		MemoryOvercommit: budget.MemoryOvercommit,
		WithinCap:        true,
		Summary:          budget.Summary(),
	}
	if cpus, ok := budget.CPUBudget(); ok {
		report.BudgetCPUs = int(cpus)
	}
	if memMB, ok := budget.MemoryBudgetMB(); ok {
		report.BudgetMemoryMB = memMB
	}
	if kept := budget.ReservedFor(); !kept.IsZero() {
		report.RepairReserveCPUs = kept.CPUs
		report.RepairReserveMemoryMB = kept.MemoryMB
	}
	if reader, ok := a.Inner.(clusterFreeReader); ok {
		report.Cluster = clusterFreeReport(reader.ClusterFree(context.Background()))
	}

	var holders []model.LedgerHolder
	if ledger != nil {
		live := ledger.Live()
		holders = ledger.Holders()
		report.CommittedCPUs = live.CPUs
		report.CommittedMemoryMB = live.MemoryMB
		report.CommittedCount = len(holders)
		report.WithinCap = budget.WithinCap(live)
		report.OverCapByException = budget.Known() && !budget.WithinCap(live)

		byPool := make(map[string]*ui.SandboxBudgetPool)
		poolOrder := []string{model.PoolAgent, model.PoolCI, model.PoolPersistent, model.PoolStanding}
		for _, h := range holders {
			p, ok := byPool[h.Pool]
			if !ok {
				p = &ui.SandboxBudgetPool{Pool: h.Pool}
				byPool[h.Pool] = p
				if !slices.Contains(poolOrder, h.Pool) {
					poolOrder = append(poolOrder, h.Pool)
				}
			}
			p.Count++
			p.CPUs += h.Shape.CPUs
			p.MemoryMB += h.Shape.MemoryMB
		}
		for _, pool := range poolOrder {
			if p, ok := byPool[pool]; ok && p.Count > 0 {
				report.Pools = append(report.Pools, *p)
			}
		}
	}

	var recReport *ui.SandboxReconciliationReport
	if rec, ok := orchestrator.LatestSandboxReconciliation(ledger); ok {
		checkedAt := rec.CheckedAt
		recReport = &ui.SandboxReconciliationReport{
			CheckedAt:          &checkedAt,
			AccountedCount:     rec.AccountedCount,
			LiveCount:          rec.LiveCount,
			LiveCPUs:           rec.LiveCommitted.CPUs,
			LiveMemoryMB:       rec.LiveCommitted.MemoryMB,
			InSync:             rec.InSync,
			Discrepancies:      append([]string(nil), rec.Discrepancies...),
			OrphansReaped:      rec.OrphansReaped,
			TotalOrphansReaped: rec.TotalOrphansReaped,
		}
	} else if a.Inner != nil {
		liveCount := a.SandboxCount()
		var liveCPUs, liveMemMB int
		if lister, ok := a.Inner.(orchestrator.LiveSandboxLister); ok {
			liveSandboxes := lister.LiveSandboxes()
			liveCount = len(liveSandboxes)
			for _, sb := range liveSandboxes {
				liveCPUs += sb.Shape.CPUs
				liveMemMB += sb.Shape.MemoryMB
			}
		}
		recReport = &ui.SandboxReconciliationReport{
			AccountedCount: len(holders),
			LiveCount:      liveCount,
			LiveCPUs:       liveCPUs,
			LiveMemoryMB:   liveMemMB,
			InSync:         len(holders) == liveCount,
		}
	}

	return report, recReport
}

// hostLoadAdapter adapts sysstat's own hourly rollups onto
// metrics.HostRollup, field for field, the way cycleTimesAdapter below
// does for the reconcile loop's ticks and for the same reason: neither
// package imports the other, and cmd/grain is the one place both are in
// scope.
type hostLoadAdapter struct {
	inner *sysstat.History
}

func (a hostLoadAdapter) HostLoad() metrics.HostHistory {
	rollups := a.inner.Rollups()
	out := metrics.HostHistory{
		Hours:   a.inner.Hours(),
		Rollups: make([]metrics.HostRollup, 0, len(rollups)),
	}
	for _, r := range rollups {
		out.Rollups = append(out.Rollups, metrics.HostRollup{
			Since:              r.Since,
			Samples:            r.Samples,
			Load1Mean:          r.Load1Mean,
			Load1Max:           r.Load1Max,
			CPUUtilizationMean: r.CPUUtilizationMean,
			CPUUtilizationMax:  r.CPUUtilizationMax,
			MemUsedMeanMB:      r.MemUsedMeanMB,
			MemUsedMaxMB:       r.MemUsedMaxMB,
			SwapUsedMaxMB:      r.SwapUsedMaxMB,
			CPUs:               r.CPUs,
			MemTotalMB:         r.MemTotalMB,
		})
	}
	return out
}

// cycleTimesAdapter adapts orchestrator's own CycleTiming (what a cycle
// measured about itself) onto metrics.CycleSample (what pkg/metrics
// summarises), field for field -- the one place both types are ever in
// scope, so neither package needs to import the other, exactly as
// sandboxHealthAdapter above does for the sandbox pane.
//
// It converts eagerly rather than handing over the ring: a report is a
// read of a bounded slice a few hundred entries long, taken once per
// GET /api/metrics, and copying it is what keeps the reconcile loop's
// own record out of reach of everything downstream of it.
type cycleTimesAdapter struct {
	inner *orchestrator.CycleTimes
}

func (a cycleTimesAdapter) CycleTimes() metrics.CycleHistory {
	recent, observed := a.inner.History()
	out := metrics.CycleHistory{
		Observed: observed,
		Samples:  make([]metrics.CycleSample, 0, len(recent)),
	}
	for _, c := range recent {
		sample := metrics.CycleSample{
			Start:        c.Start,
			Duration:     c.Duration,
			DispatchWait: c.DispatchWait,
			Commits:      c.Commits,
			Reconcilers:  make([]metrics.ReconcilerSample, 0, len(c.Reconcilers)),
		}
		for _, r := range c.Reconcilers {
			sample.Reconcilers = append(sample.Reconcilers, metrics.ReconcilerSample{
				Name:     r.Name,
				Wait:     r.Wait,
				Duration: r.Duration,
				Failed:   r.Failed,
			})
		}
		out.Samples = append(out.Samples, sample)
	}
	return out
}

// capabilityCheckAdapter is ui.Config.CapabilityChecks over this
// deployment's own capability providers: it authenticates as one
// capability's standing credential and makes the one cheap, harmless
// call that capability's provider offers as proof the credential still
// works (model.CredentialChecker).
//
// It rebuilds the providers per call, out of capabilityProviders and the
// live configuration, rather than holding a registry of its own. Two
// reasons, both about answering for what this deployment is doing right
// now: capabilityProviders is the one place a provider's deployment
// configuration is assembled, so a check made through it cannot drift
// from the mint a dispatch would make; and the answer follows a settings
// change immediately, where a registry captured at startup would go on
// checking the project an operator has since replaced. Construction
// touches no network -- every provider here builds its client at call
// time -- so the cost is a handful of structs per button press.
//
// The credential resolver is the daemon's own secrets store, the same
// one orchestrator.Config.Credentials resolves a real Materialize
// through. A named GitHub token is the one capability whose standing
// credential is not in there at all -- it is an operator's file under
// secrets/github -- so its provider is built holding the credential
// ladder instead (capabilityProviders' own githubCredentials argument),
// and this resolver goes unused by it. Nothing here reads a value back
// out to the caller either way: material goes into a provider, and what
// comes back is a sentence about what the far end said.
type CapabilityCheckAdapter struct {
	Live  *LiveConfig
	Creds model.CredentialResolver
}

func (a CapabilityCheckAdapter) CheckCapability(ctx context.Context, id string) (ui.CapabilityCheckResult, error) {
	registry := newCapabilityRegistryWithStore(
		a.Live.store, a.Live.Current().LocalScriptsConfig(),
		CapabilityProviders(a.Live.Current(), a.Live.store, ModelProxies, a.Live.GitHubTokens(), a.Live.gitHubCredentialSource())...)
	provider, ok := registry.Lookup(id)
	if !ok {
		// An answer about this deployment rather than a fault:
		// capabilityProviders registers gcp-key and gemini-key only once
		// a GCP project (and, for gcp-key, an agent service account) is
		// set, so the honest reply is that there is nothing configured
		// here to check -- which is what the pane's own "Needs:" line
		// beside this already says.
		//
		// Which of the two unconfigured deployments this is, though, has
		// to be told apart before saying so. The registry above is built
		// out of what this process is *running* with, and the pane
		// asking the question reports what the store *holds*: a setting
		// saved but not yet adopted registers no provider here while
		// every field for it is filled in over there, and answering
		// "it is not configured" then sends an operator to a pane where
		// their own project is already typed in and the badge beside it
		// says Ready. That deployment exists and is not a race: settings
		// are adopted by the reconcile loop (liveConfig.refresh), and a
		// process whose loop never came up (reconcilerDown) keeps this
		// UI serving a configuration nothing is reading.
		return ui.CapabilityCheckResult{}, a.unregistered(ctx, id)
	}
	checker, ok := provider.(model.CredentialChecker)
	if !ok {
		return ui.CapabilityCheckResult{}, fmt.Errorf("capability %q has no credential to test", id)
	}
	check, err := checker.CheckCredential(ctx, a.Creds)
	// The result is carried out even alongside an error: Credentials is
	// filled in from the provider's own Requires before the call is
	// made, and a refusal's whole remedy is "replace what is in that
	// secret" -- see ui.CapabilityCheck.Credentials.
	return ui.CapabilityCheckResult{Credentials: check.Credentials, Detail: check.Detail}, err
}

// unregistered is the sentence a check gets for a capability this
// process has no provider for. A known configuration-gated provider
// names each blank setting; an unknown capability keeps a generic
// no-provider answer.
//
// A deployment that has been configured but has not adopted its stored
// settings is told that first, because the remedy is the opposite kind
// of thing: nothing to fill in, and a daemon to restart (or a dead
// reconcile loop to fix), which no amount of re-reading Settings would
// have suggested.
func (a CapabilityCheckAdapter) unregistered(ctx context.Context, id string) error {
	if stored, ok := a.Live.storedConfig(ctx); ok {
		registry := newCapabilityRegistryWithStore(
			a.Live.store, stored.LocalScriptsConfig(),
			CapabilityProviders(stored, a.Live.store, ModelProxies, a.Live.GitHubTokens(), a.Live.gitHubCredentialSource())...)
		if _, found := registry.Lookup(id); found {
			return fmt.Errorf(
				"this deployment is configured for %q, but the daemon serving this check has not "+
					"applied that configuration and so registers no provider for it, which is also "+
					"how a task granted it would fail: settings are applied by the reconcile loop, "+
					"once per poll interval, so either it was saved a moment ago or that loop is not "+
					"running (Status) and this deployment needs restarting", id)
		}
	}
	missing := missingCapabilityProviderConfig(a.Live.Current(), id)
	switch len(missing) {
	case 1:
		return fmt.Errorf(
			"this deployment has no active provider for %q, so there is nothing to test: "+
				"the %s setting is blank; set it in Settings -> Capabilities", id, missing[0])
	case 2:
		return fmt.Errorf(
			"this deployment has no active provider for %q, so there is nothing to test: "+
				"the %s and %s settings are blank; set them in Settings -> Capabilities",
			id, missing[0], missing[1])
	}
	return fmt.Errorf(
		"this deployment registers no provider for %q, so there is nothing to test", id)
}

// agentKeyCheckAdapter is ui.Config.AgentKeyChecks over this
// deployment's own agent credentials: it resolves the credential a run
// with that framework would be dispatched with, and asks the vendor that
// issued it whether it still works (pkg/agent/credcheck).
//
// It resolves exactly as a dispatch resolves -- agentCredential, secrets
// database first and then whatever -gemini-api-key-file /
// -claude-oauth-token-file / -openai-api-key-file names -- and reads the
// key files off the live configuration rather than off a copy taken at
// startup, for the same reason capabilityCheckAdapter above rebuilds its
// providers per call: the answer has to be about what the *next*
// dispatch would authenticate with, not about what this process was
// started with. A deployment seeded by file and a deployment configured
// from the UI are therefore both testable, and a key pasted into
// Settings a second ago is what gets tested.
//
// Nothing here reads a credential back out to the caller: material goes
// into credcheck, and what comes back is a sentence about what the
// vendor said.
type AgentKeyCheckAdapter struct {
	Live  *LiveConfig
	Creds *secrets.Store
	// checker is the zero value in a deployment -- the vendors' own
	// hosts. Only tests set it, pointing it at a stub, so that testing
	// this adapter's own job (which file, which secret, which sentence)
	// costs no call to anybody's real API.
	Checker credcheck.Checker
}

func (a AgentKeyCheckAdapter) CheckAgentKey(ctx context.Context, framework string) (ui.AgentKeyCheckResult, error) {
	secret, ok := credcheck.SecretFor(framework)
	if !ok {
		// pkg/ui rejects an unknown framework before reaching here; this
		// is the same answer for any other caller.
		return ui.AgentKeyCheckResult{}, fmt.Errorf("no agent framework named %q", framework)
	}
	result := ui.AgentKeyCheckResult{Secret: secret}
	cfg := a.Live.Current()
	var keyFile string
	switch model.NormalizeAgentFrameworkName(framework) {
	case model.AgentFrameworkAntigravity:
		keyFile = cfg.GeminiAPIKeyFile
	case model.AgentFrameworkClaude:
		keyFile = cfg.ClaudeOAuthTokenFile
	case model.AgentFrameworkCodex:
		keyFile = cfg.OpenAIAPIKeyFile
	}
	credential, err := AgentCredential(ctx, a.Creds, secret, keyFile)
	if err != nil {
		return result, fmt.Errorf("reading the credential for %s: %w", framework, err)
	}
	if credential == "" && model.NormalizeAgentFrameworkName(framework) == model.AgentFrameworkCodex && configuredCodexAuthFile(cfg) != "" {
		if err := codex.ValidateAuthFile(configuredCodexAuthFile(cfg)); err != nil {
			return result, err
		}
		result.Detail = "Codex is configured with a shared ChatGPT login file; its JSON is readable. " +
			"The next Codex run will validate and refresh the login with OpenAI."
		return result, nil
	}
	// An empty credential falls through to credcheck rather than being
	// answered here, so "not set" reads in one sentence written in one
	// place whichever caller asked.
	check, err := a.Checker.Check(ctx, framework, credential)
	if check.Secret != "" {
		result.Secret = check.Secret
	}
	result.Detail = check.Detail
	return result, err
}

// clusterFreeReader is a Kubernetes backend's reading of its nodes' free
// capacity (K8sSandboxes.ClusterFree).
type clusterFreeReader interface {
	ClusterFree(ctx context.Context) k8s.FreeSummary
}

func clusterFreeReport(s k8s.FreeSummary) *ui.SandboxClusterFree {
	out := &ui.SandboxClusterFree{
		Known:               s.Known,
		Nodes:               s.Nodes,
		AllocatableCPUs:     float64(s.Allocatable.MilliCPU) / 1000,
		AllocatableMemoryMB: int(s.Allocatable.MemoryBytes >> 20),
		FreeCPUs:            float64(s.Free.MilliCPU) / 1000,
		FreeMemoryMB:        int(s.Free.MemoryBytes >> 20),
		LargestFreeNode:     s.LargestNode,
		LargestFreeCPUs:     float64(s.LargestFree.MilliCPU) / 1000,
		LargestFreeMemoryMB: int(s.LargestFree.MemoryBytes >> 20),
		PendingPods:         s.Pending,
		Partial:             s.Partial,
		Error:               s.Err,
		Summary:             s.String(),
	}
	if !s.ReadAt.IsZero() {
		at := s.ReadAt
		out.ReadAt = &at
	}
	return out
}
