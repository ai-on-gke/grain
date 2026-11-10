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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

// CheckCIJobPolicy is the local policy (-policy, pkg/policy) asked of one
// CI job before anything is built or materialized for it: nil when the
// job's claims fit what this machine's owner will run, and otherwise the
// error naming the first one the policy refuses (GSA1-07, silo INV-12).
//
// A job is judged the way a task is (policy.Policy.CheckClaims), as the
// task it would be: the repository under test as its target, every
// capability it carries as a grant -- including the `repo-read:` and
// `repo-write:` a workflow on a pushed branch declares for itself, whose
// repositories answer to repos: as well as to capabilities: -- and the
// network every CI sandbox is given, which is open (CiSandboxes.Acquire).
// Approval is not asked: a CI run is grain's own work, started by a push,
// a pull request, a schedule or a person here, never a request to accept.
//
// Every capability the job carries is judged, including one its event
// will not hold (a `repo-write:` on a pull request): what is refused is
// the workflow's declaration, whichever event happens to run it. The
// grain#ci grant ResolveCICapabilities adds to every job on its own
// is not the job's claim and is not judged, whether or not a repository's
// list names it too.
//
// A nil policy -- a daemon with no -policy -- allows every job, as it
// allows every task.
func CheckCIJobPolicy(p *policy.Policy, req ci.SandboxRequest) error {
	if p == nil {
		return nil
	}
	repo, err := model.ParseRepo(req.Repo)
	if err != nil {
		return fmt.Errorf("%q is not a repository local policy can judge: %w", req.Repo, err)
	}
	task := model.Task{Target: &repo, NetworkAccess: model.NetworkOpen}
	for _, id := range req.Capabilities {
		if grainci.IsGrant(id) {
			continue
		}
		task.Grants = append(task.Grants, model.Grant{Capability: id, Via: model.GrantByDefault})
	}
	return p.CheckClaims(task, model.NetworkOpen)
}

// ciPolicyRefusal is CheckCIJobPolicy for the two places a job is built
// (CiGrantReservations.Acquire, CiSandboxes.Acquire): the refusal said
// once in the daemon log, where an operator whose CI jobs all stop on
// their policy can see why, and returned as the job's own error.
func ciPolicyRefusal(p *policy.Policy, req ci.SandboxRequest, logf func(string, ...any)) error {
	err := CheckCIJobPolicy(p, req)
	if err == nil {
		return nil
	}
	if logf != nil {
		logf("grain ci: %s: %s job %s refused by local policy (-policy): %v", req.Repo, req.Event, req.Name, err)
	}
	return fmt.Errorf("local policy refuses this CI job: %w", err)
}

// CheckCIJobCatalogue judges one CI job's capabilities against the
// deployment's capability catalogue (settings/capabilities.yml,
// model.CapabilityCatalogue) as a CI job of the repository under test,
// before any of them is materialized: a capability the catalogue has
// since revoked, or keeps off this repository, is refused rather than
// minted. The grain#ci grant every job carries is grain's own and is
// not judged. A nil catalogue admits every job.
func CheckCIJobCatalogue(cat *model.CapabilityCatalogue, req ci.SandboxRequest) error {
	if cat == nil {
		return nil
	}
	gc := model.GrantContext{Kind: model.CatalogueKindCI}
	if repo, err := model.ParseRepo(req.Repo); err == nil {
		gc.Repo = &repo
	}
	for _, id := range req.Capabilities {
		if !grainci.IsGrant(id) {
			gc.Grants = append(gc.Grants, id)
		}
	}
	_, err := cat.ValidateGrants(gc)
	return err
}

// CheckCIJobRepoAccessPolicy judges a CI job's repository access grants
// (repo#read, repo#write {repo}, and their bare repo-read:/repo-write:
// spellings) and agent API grants (api:<scope>, https://grain.dev/capabilities/api#<scope>,
// release-gate-read, on runs whose event admits ciapi) against the deployment's
// CapabilityPolicy. Other capability references are judged in mintCIPass, which
// skips repository access and ciapi because neither mints a protocol pass.
func CheckCIJobRepoAccessPolicy(capPolicy model.CapabilityPolicy, servers map[string]protocol.ServerConfig, req ci.SandboxRequest) error {
	admitsCIAPI := !req.WithholdEventGated &&
		(req.Event == ci.EventRelease || req.Event == ci.EventManual || (req.Event == ci.EventSchedule && req.TrustedSchedule))
	var canonical []string
	for _, id := range req.Capabilities {
		if target, ok := model.RepoReadTarget(id); ok {
			canonical = append(canonical, model.RepoReadGrant(target))
		} else if target, ok := model.RepoWriteTarget(id); ok {
			canonical = append(canonical, model.RepoWriteGrant(target))
		} else if admitsCIAPI {
			if id == model.ReleaseGateReadCapability {
				canonical = append(canonical, model.APIRef(model.ReleaseGateReadCapability))
			} else if _, ok, _ := model.ParseAPIGrant(id); ok || model.CapabilityGrantRef(id) == model.APICapabilityServerID {
				canonical = append(canonical, model.CanonicalGrantKey(id))
			}
		}
	}
	if len(canonical) == 0 {
		return nil
	}
	var repoPtr *model.RepoRef
	if repo, err := model.ParseRepo(req.Repo); err == nil {
		repoPtr = &repo
	}
	configured, _ := intree.EnsureBuiltins(servers)
	subject := model.CapabilitySubject{
		Repo:      repoPtr,
		Kind:      model.CapabilityRunCI,
		Event:     string(req.Event),
		Untrusted: req.Untrusted,
	}
	for _, grant := range canonical {
		if err := capPolicy.Permits(grant, subject, configured); err != nil {
			return err
		}
	}
	return nil
}

// ciCatalogueRefusal is CheckCIJobCatalogue and CheckCIJobRepoAccessPolicy for
// CiGrantReservations.Acquire, reading the catalogue and capability policy
// through config and saying a refusal once in the daemon log.
func ciCatalogueRefusal(ctx context.Context, config func(context.Context) (*model.Config, error), req ci.SandboxRequest, logf func(string, ...any)) error {
	if config == nil {
		return CheckCIJobRepoAccessPolicy(model.CapabilityPolicy{}, nil, req)
	}
	cfg, err := config(ctx)
	if err != nil {
		return fmt.Errorf("reading the capability catalogue: %w", err)
	}
	if cfg == nil {
		return CheckCIJobRepoAccessPolicy(model.CapabilityPolicy{}, nil, req)
	}
	if err := CheckCIJobRepoAccessPolicy(cfg.CapabilityPolicy, cfg.MCPServers, req); err != nil {
		if logf != nil {
			logf("grain ci: %s: %s job %s refused by capability policy: %v", req.Repo, req.Event, req.Name, err)
		}
		return err
	}
	if err := CheckCIJobCatalogue(cfg.CapabilityPolicy.Catalogue, req); err != nil {
		if logf != nil {
			logf("grain ci: %s: %s job %s refused by settings/capabilities.yml: %v", req.Repo, req.Event, req.Name, err)
		}
		return fmt.Errorf("the capability catalogue refuses this CI job: %w", err)
	}
	return nil
}

// ciDeclarationRefusal refuses a CI job whose workflow declares a
// capability only a person may accept (model.DeclaredCIJobHold: a GCP
// key, a host script, a GitHub credential, a dangerous api scope) that no
// person has accepted for it (model.DeclarationAcceptance, task 3971). A
// workflow file is the repository's to write, by whoever can merge or
// push a branch there, so declaring such a capability is a request, not
// a grant. The request is recorded either way, so Settings ->
// Declarations shows the job awaiting acceptance; a job widening its
// capabilities past the accepted set is refused again until a person
// accepts the new one. grain's own builtin jobs, and a daemon with no
// store (declarations nil), are not asked.
func ciDeclarationRefusal(ctx context.Context, declarations func(context.Context, model.RepoRef, string, string, []string, time.Time) (bool, []string, error),
	repo model.RepoRef, req ci.SandboxRequest, now time.Time, logf func(string, ...any)) error {

	if declarations == nil || req.Builtin != "" {
		return nil
	}
	var declared []string
	for _, id := range req.Capabilities {
		if !grainci.IsGrant(id) {
			declared = append(declared, id)
		}
	}
	accepted, items, err := declarations(ctx, repo, req.Name, req.Commit, declared, now)
	if err != nil {
		return fmt.Errorf("checking whether a person accepted this CI job's capabilities: %w", err)
	}
	if accepted {
		return nil
	}
	if logf != nil {
		logf("grain ci: %s: %s job %s held: its declared %s await a person's acceptance", req.Repo, req.Event, req.Name, strings.Join(items, ", "))
	}
	return fmt.Errorf("this job declares %s, which only a person may grant, and no person has accepted that for it: "+
		"a person holding the scope each needs can accept it on Settings -> Declarations (%s)",
		strings.Join(items, ", "), model.CIJobDeclarationID(repo, req.Name))
}
