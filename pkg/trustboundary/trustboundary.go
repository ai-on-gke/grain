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

// Package trustboundary classifies a change by the paths it touches, against
// a fixed list of the files that make up grain's own trust boundary: who may
// call what, what a sandbox may run, where credentials live, how grain is
// deployed, and what grain's own CI and merge gate read.
//
// The list is code, and deliberately so. It decides whether a change may
// merge itself (orchestrator's security hold, #2425 Layer 2a), and a list a
// repository could declare for itself -- a `.grain/` file, say -- is one a
// change could edit in the same pull request it is trying to get past it.
// So the operator controls it the way they control the rest of grain's
// behaviour: by reviewing a pull request against this file and upgrading.
// Version moves whenever the list does, so a hold recorded under one list
// says which list it was.
package trustboundary

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version names this build's list. Bump it on any change to Categories, so
// a hold written under an older list is recognisable as one.
const Version = "trust-boundary/v28"

// Category is one kind of trust-boundary path.
type Category struct {
	// ID is short and stable: what a hold records and what a reviewer is
	// told the change touched.
	ID string
	// Description is what the category guards, for the reviewer.
	Description string
	// RequiresHuman is set on the paths a clean automated review is not
	// enough for: what grain's own gate reads to decide that a red build
	// is not this change's fault. A change that edits those can never
	// merge itself, reviewed or not -- a human presses Submit.
	RequiresHuman bool
	// OwnerApproval narrows RequiresHuman to who: only an admin of the
	// repository's owning team, or a system admin, may press that Submit
	// (a Submit by anybody else is refused). Set on what decides who may
	// release everything else -- a repository's own trigger declarations
	// (grain/task-1714) -- since a change that could be let through by
	// whoever it is trying to get past would guard nothing.
	OwnerApproval bool
	// Review is what the security review must check on these paths
	// beyond its usual brief, each item one it reports as a finding when
	// the change violates it. A finding, or a review that cannot decide,
	// leaves the change for a person, so a category that a clean review
	// releases can still keep the part of its paths that matter for one.
	Review []string

	// prefixes are directory prefixes ("pkg/gitproxy/"), files are exact
	// repository paths, and bases are file-name patterns matched against
	// the last path element in any directory (path.Match syntax).
	prefixes []string
	files    []string
	bases    []string
}

// Categories is the list, in the order a reviewer is told about them. The
// first category a path matches is the one it is reported under, so the
// narrower entries come first: securityhold.go before pkg/orchestrator/ as
// a whole, and the ".grain/" suppressions before ".grain/".
//
// v28 adds the six sibling surfaces the post-merge security review of v26
// found unclassified across scripts/ and pkg/ (task 3926, finding 3.1 of
// task 3905): scripts/granule/ (build-granule-image.sh and guest-inputs.sh,
// which scripts/release-images.sh runs to compute the guest cache key and
// build the runtime baked into every Granule VM guest image) and
// scripts/release-patch.sh (which cuts release/vX.Y.Z branches and tags
// patch releases) to release-machinery; scripts/grain-instance-init.sh and
// scripts/bake-gce-release-image.sh (which bake and boot as root on every
// hosted Grain Hub GCE VM instance) to setup-scripts; pkg/hostinit/ (which
// runs host initialization steps including build-granule-image.sh) to
// exec-sandbox; and pkg/installtrack/ (which defines track image
// repositories and MovesFrom migration targets read by pkg/upgrade) to
// upgrade-images.
//
// v27 adds pkg/hubenv/ to auth-routes and keeps cmd/grain-registry/ and
// pkg/registry/ alongside pkg/hub/ and cmd/grain-hub/ in credential-proxy
// (task 3762, found in the security review of task 3710): pkg/hubenv/ reads
// GRAIN_HUB_{ISSUER,AUDIENCE,KEYS_URL,TOKEN,URL,ENTRY,CONNECT,CONFIG} and
// their pre-rename GRAIN_REGISTRY_* spellings for pkg/webauth/, pkg/daemon/
// and pkg/hub/, and cmd/grain-registry/ stays a live shim binary for one
// release.
//
// v26 adds what the security review of task 3597 (task 3624) found beside
// scripts/build-cli.sh: the workstation CLI installer (pkg/ui/cli_install.sh,
// served unauthenticated at GET /cli/install.sh to be piped into sh, and
// pkg/ui/cli_install.go, whose cliInstallRoute exempts it from the
// single-tenant access code) to setup-scripts, beside its hostd
// counterpart; and the helpers the release builds and workflows run or
// source (scripts/buildvcs.sh, scripts/configure-dockerhub-auth.sh, which
// promote.yml and publish-release.yml run with DOCKER_PAT, and
// scripts/release-image-repos.sh, which names the published image
// repositories) to release-machinery.
//
// v25 adds what the 2026-11-08 second security review of main (task 3584,
// finding 1) found unclassified (capabilities-secrets):
// pkg/model/canonical_grant.go, new in #927 (unified grants slice 2), which
// maps …/api#<scope> references to and from api:<scope>[@repo] for
// ParseAPIGrant (so APIGrantsOf and every agent-API authorization),
// IsDangerousGrant, GrantsSubsetOf, EscalatedCapabilities,
// CapabilityPolicy.Permits and the catalogue, and whose SplitRequestGrants
// turns a request's grants: list into a task's Repo/Base, Reads and Writes
// -- a one-line change there could make a dangerous scope read as safe to
// all of them at once; and pkg/model/auto_submit.go (SelfApproved,
// EscalatedCapabilities), the same shape and unclassified since before.
//
// v24 adds scripts/build-cli.sh to release-machinery (task 3597, filed by
// task 3580): promote.yml, publish-release.yml and release-train-assets.yml
// build the grain-<os>-<arch>.tar.gz archives and checksums.txt with it in a
// contents: read job and publish them as assets on every vX.Y.Z and
// vX.Y.Z-rc.N release, where GET /cli/install.sh installs them on
// workstations.
//
// v23 adds what the 2026-11-08 security review of main (task 3329,
// finding 1) found #885's v22 left out (auth-routes): the human-review
// hold (pkg/model/human_review_hold.go, pkg/ui/human_review.go), whose
// ReleaseHumanReviewHold refuses agent and automation principals -- the
// rule that an agent may raise a human-review hold but never release it,
// which the security hold's raiseHumanOnlyReview and raiseFindingsReview
// rely on; the director's Tier 3 floor (pkg/model/director_floor.go,
// WithinDirectorFloor and DirectorFloorRefusal), which the gate, the MCP
// endpoint and staterepo/validate_pr.go apply; and pkg/ui/on_behalf_of.go
// (agentOnBehalfOf), attribution only today, classified so a change that
// makes it authorize is reviewed.
//
// v22 adds grain-registry's front door (task 3479, found in the security
// review of task 3284): pkg/webauth/, which verifies Cloud IAP JWTs and
// X-Grain-Registry-Assertion ES256 JWTs, admits domains, groups and
// service principals, and runs the proxy-auth Fetch-Metadata/Origin CSRF
// checks (auth-routes); and pkg/hub/ with cmd/grain-hub/, which
// verify front-door IAP assertions, decide entry visibility and
// X-Grain-On-Behalf-Of delegation, sign X-Grain-Registry-Assertion JWTs,
// strip edge credentials and IAP cookies before forwarding over tunnels,
// and authenticate grc_ entry credentials (credential-proxy, beside the
// rendezvous relay, the other remote-access edge).
//
// v21 adds what release assets brought onto the boundary (task 2990,
// reviewed by task 3039): scripts/release-assets.sh, which publishes,
// re-points and prunes every release's images with a contents: write
// token, and .github/workflows/release-train-assets.yml, which runs it on
// the release train's tag pushes (release-machinery); and
// scripts/install-grain.sh, which loads a release's image and installs
// the binary out of it (setup-scripts).
//
// v20 adds the external-agent owner-trust policy and the gma_ bearer
// token model and lifecycle handlers (pkg/model/owner_trust.go,
// pkg/model/external_agent_token.go, and pkg/ui/external_agents.go) to
// auth-routes (task 3026): whose external agents skip a person's
// approval on an unapproved filing, and gma_ token scope resolution,
// ownership, authentication, issuance and rotation.
//
// v19 adds the embedded bootstrap playbooks moved out of pkg/capability/
// (pkg/playbooks/, task 3148) and the capability reference/policy,
// catalogue and template-grant admission files
// (pkg/model/capability_ref.go, pkg/model/capability_catalogue.go, and
// pkg/model/template_grants.go) to capabilities-secrets (task 3388).
//
// v18 adds host enrollment codes, pending pairing-code handshakes and
// deployment-owner request checks (pkg/model/host_enroll.go,
// pkg/ui/request_actor.go, and pkg/ui/host_enroll.go) to auth-routes.
//
// v17 added the authorization-bearing files the 2026-11-05 nightly
// security review found unclassified (finding 2, task 3168) to
// auth-routes: the role gate for verified-identity sessions
// (access_roles.go), the built-in scope sets every role and api:<set>
// grant expands through (api_scope_set.go), the inbox reach of a gma_
// token (external_cli.go), the approval-callback bearer tokens and the
// recording of a person's host-script approval
// (local_script_approvals.go), and the daemon's UI listeners and their
// guards (ui_server.go).
//
// v16 added connection_hostd.go to auth-routes and capability server
// hostd placement (capability_servers.go, capability_servers_hostd.go,
// pkg/ui/capability_servers.go) to capabilities-secrets (grain/task-2165):
// where run: hostd capability servers start and receive their resolved
// secrets.
//
// v15 added the authorization-bearing files the 2026-11-04 nightly
// security review found unclassified (finding 2, task 2977): the
// controller MCP endpoint that authorizes every grain_invoke, the
// database-held sign-in allowlist's entry refusal and its API, and the
// person-only check on applying a run's requested grants (auth-routes);
// the code-declared CI grant rule (capabilities-secrets); the store-level
// auto-merge guard and its restore after a hold (hold-policy); and the
// hostd install script served unauthenticated and run as root, with the
// route that renders it (setup-scripts).
//
// v14 added the rc qualification housekeeping workflow and its Actions
// helper to release-machinery (grain/task-2584).
//
// v13 added the GitHub Actions Inbox approval (grain/task-2666,
// design/playbook/releases.playbook.md §0.2) to release-promotion: the
// route that signs a person's answer and dispatches promote-release.yml,
// the signed record, and the script the workflow verifies it with.
//
// v12 narrowed release-promotion to the approval path (grain/task-2664,
// design/playbook/releases.playbook.md §0.3): the approval workflow, in
// both its .grain and .github copies, the route that starts it and the
// release train's template stay person-only; the release jobs and the
// scripts they run moved to release-machinery, which a clean security
// review releases once it has checked they cannot reach the approval.
//
// v11 added the hostd registry, the host-exec confirmation and CI
// repository access (grain/task-2723, 2026-11-03 security review finding 2):
// hostd admission, token lookup, routing and the host executor, and the
// human-only message filter a host command's approval is read through
// (auth-routes); the host-exec confirmation, CI's repository-access grants,
// event gate and pull-request refusal, and the daemon's capability wiring
// (capabilities-secrets). The repo-access gate moved out of
// pkg/model/task.go into pkg/model/repo_access.go so it could be held
// without holding every change to tasks.
//
// v10 added the single-tenant access gate, verified-identity seam and
// principal allowlist (grain/task-2434, 2026-11-02 security review finding 1):
// pkg/ui/accesscode.go, single_tenant.go, verifier.go and allowlist.go.
//
// v8 added release-promotion (grain/task-1236): the person-only step
// that makes a pending release official -- .grain/workflows/promote-release.yml,
// the scripts it runs, and the route that starts it. A change that let an
// agent or a schedule push vX.Y.0 and `latest` must not merge itself.
//
// v7 added state-privilege (grain/task-1025, finding 1 of the
// sandbox/host boundary audit): a pull request against the deployment's
// own state repository is classified by ClassifyState as well, because
// what merges there is applied to the running daemon on the next tick.
//
// v6 added what the 2.4 security audit found on the trust boundary
// outside v5's paths (grain/task-947, filed by task 907): the agent API's
// scope table, gate and its Dangerous scopes, the external-config-repo
// refusal, the persistent-sandbox staging proxy, the workstation policy
// enforcement point and issuer key fetching (auth-routes); credential
// redaction (capabilities-secrets); the sandbox pod spec, the host process
// server and host-script executor, the per-run host, the derived guest
// image, the vendored granule guest agent, exec wire and port-forward mux,
// and the daemon wiring that decides what is forwarded into guests
// (exec-sandbox); and the state repository sync (state-repo).
//
// v5 dropped cmd/grain-tunnel-gateway/ from credential-proxy
// (grain/task-2099): the tunnel gateway is gone, and the rendezvous relay
// is the one remote-access edge.
//
// v4 added merge-rules (grain/task-1659), a repository's own
// `.grain/merge-rules.yml` (and its old name `.grain/sensitive.yml`),
// which decides what CI gates a merge and whether it may merge itself.
//
// v3 added path-triggers (grain/task-1714), a repository's own
// `.grain/triggers`, which only its owners may release.
//
// v2 widened v1 to what the 2026-10-01 repo-wide review found on the
// trust boundary outside v1's paths: auth middleware and pkg/access,
// capability and secret handling, the whole MCP tool surface, the relay
// binaries, the code that decides green (pkg/ci, checkpost,
// repository declarations), the security tests themselves, image upgrades,
// and this list.
var Categories = []Category{
	{
		ID:            "hold-policy",
		Description:   "this list and the security hold that enforces it",
		RequiresHuman: true,
		// The list is operator-controlled only for as long as a change to
		// it cannot merge itself: one that drops a path from it would be
		// classified by the very list it shortens.
		prefixes: []string{"pkg/trustboundary/"},
		files: []string{
			"pkg/orchestrator/securityhold.go",
			// The repository-declared half of the list, and who may
			// release what it holds (grain/task-1714).
			"pkg/orchestrator/repoguard.go", "pkg/model/sensitive_path.go", "pkg/ui/approvals.go",
			// The store-level guard that keeps auto-merge off a held task,
			// and the one place it is handed back once the hold clears.
			"pkg/model/task_auto_merge.go",
		},
	},
	{
		ID:          "auth-routes",
		Description: "authentication, principals, request guards and the UI/API route table",
		prefixes:    []string{"pkg/access/", "pkg/iapreconciler/", "pkg/policy/", "pkg/jwks/", "pkg/webauth/", "pkg/hubenv/"},
		files: []string{
			"pkg/ui/server.go", "pkg/ui/principal.go", "pkg/ui/request_actor.go", "pkg/ui/access.go",
			"pkg/ui/accesscode.go", "pkg/ui/single_tenant.go",
			"pkg/ui/allowlist.go", "pkg/ui/verifier.go",
			"pkg/ui/auth.go", "pkg/ui/agent_keys.go", "pkg/ui/github_tokens.go",
			"pkg/ui/csrf.go", "pkg/ui/hostguard.go", "pkg/ui/security_headers.go",
			"pkg/ui/team_scope.go",
			// The agent API: which scope each route needs, which scopes
			// are Dangerous, and the escalation and self-protection rails.
			"pkg/ui/agent_api_gate.go", "pkg/ui/route_scopes.go", "pkg/ui/agent_api.go",
			"pkg/ui/agent_api_tools.go", "pkg/model/agent_api.go",
			"pkg/ui/config_refusal.go", "pkg/ui/persistent_sandboxes.go",
			// The hostd registry: whether a dialing hostd is local or must
			// present its owner's identity, token -> hostd/owner, revocation,
			// and which hostd a run is routed to.
			"pkg/ui/hostd_connect.go", "pkg/ui/hostds.go", "pkg/ui/host_enroll.go", "pkg/ui/connection_hostd.go",
			"pkg/daemon/hostd_registry.go", "pkg/daemon/hostd_route.go",
			"pkg/daemon/hostexec.go", "pkg/model/hostd.go", "pkg/model/host_enroll.go",
			// messagesSince's human-only filter is what makes the answer a
			// host-exec confirmation reads a person's.
			"pkg/ui/messages.go",
			// The controller MCP endpoint, which agentAPIGate exempts by
			// name: whose access a call runs with (token scopes, the acting
			// task's grants, or full access) and the re-issue of each
			// grain_invoke through the route table and its gate.
			"pkg/ui/mcp_endpoint.go",
			// The sign-in allowlist held in the database: which entries it
			// refuses (service accounts, domains) and its API.
			"pkg/model/allowed_principals.go", "pkg/ui/access_principals.go",
			// That only a person applies a run's requested grants.
			"pkg/ui/task_access_request.go",
			// The role gate for verified-identity sessions: whether a
			// session is gated at all (resolveAccessRoles' Full), which
			// routes a limited role reaches (humanRouteScopes,
			// operatorOnlyRoutes) and the per-call check (roleGate,
			// authorizeRoleCall).
			"pkg/ui/access_roles.go",
			// The built-in roles (grain/operator, viewer, contributor) and
			// the deny/includes resolution every role and every agent's
			// api:<set> grant expands through.
			"pkg/model/api_scope_set.go",
			// Which external-agent owners skip a person's approval on an
			// unapproved filing (OwnerTrusted, OwnerTrustedForTask), and
			// gma_ bearer token scope resolution, ownership, authentication,
			// issuance and rotation.
			"pkg/model/owner_trust.go", "pkg/model/external_agent_token.go",
			"pkg/ui/external_agents.go",
			// Which repositories' parked questions, secret requests,
			// proposals and notifications a gma_ token reads through
			// GET /api/inbox (inboxReadable), and its replies (CheckReply).
			"pkg/ui/external_cli.go",
			// The ghsa_ approval-callback bearer tokens: mint, expiry and
			// lookup, their scoping to their task's callback routes, and
			// where a person's approval of a host-script call is recorded.
			"pkg/ui/local_script_approvals.go",
			// Which listeners the daemon opens for the UI/API -- the
			// -ui-addr one and the loopback approval-callback one -- and
			// the HostGuard in front of each.
			"pkg/daemon/ui_server.go",
			// The human-review hold: an agent or automation principal may
			// raise one but never release it (ReleaseHumanReviewHold,
			// RequestHumanReview), which the security hold relies on.
			"pkg/model/human_review_hold.go", "pkg/ui/human_review.go",
			// The director's Tier 3 floor (WithinDirectorFloor,
			// DirectorFloorRefusal) the gate, /api/mcp and validate_pr apply.
			"pkg/model/director_floor.go",
			// Which person an agent's call is attributed to
			// (agentOnBehalfOf): attribution only, kept that way by review.
			"pkg/ui/on_behalf_of.go",
		},
	},
	{
		ID:          "capabilities-secrets",
		Description: "capability grants, secret storage and the cloud identities credentials are minted from",
		prefixes:    []string{"pkg/capability/", "pkg/playbooks/", "pkg/secrets/", "pkg/secretsrepo/", "pkg/gcpsetup/", "pkg/redact/"},
		files: []string{
			// Running a command on grain's own host once a person approves.
			"pkg/daemon/mcpserver_hostexec.go",
			// Which grain hostd starts a run: hostd capability server and
			// receives its resolved secrets, and the Settings routes that
			// write and probe mcpServers entries.
			"pkg/daemon/capability_servers.go", "pkg/daemon/capability_servers_hostd.go",
			"pkg/ui/capability_servers.go",
			// What a CI sandbox is handed on other repositories, on which
			// events, and the refusal of untrusted pull-request runs.
			"pkg/model/repo_access.go", "pkg/daemon/ci.go", "pkg/daemon/ci_policy.go",
			"pkg/daemon/ci_capability_pass.go", "pkg/daemon/capabilities.go",
			// Which CI events may be handed signing and publishing
			// capabilities, read by all three of their enforcers.
			"pkg/model/ci_grant_rule.go",
			// Capability reference parsing and policy admission
			// (CapabilityPolicy.Permits, UncoveredCapabilityRefs), catalogue
			// grant validation (CapabilityCatalogue.ValidateGrants), and
			// dangerous-grant and template escalation checks
			// (IsDangerousGrant, TemplateWithinTask, ReviewGrants).
			"pkg/model/capability_ref.go", "pkg/model/capability_catalogue.go",
			"pkg/model/template_grants.go",
			// The …/api#<scope> reference <-> api:<scope>[@repo] mapping
			// every grant comparison reads through (CanonicalGrant,
			// LegacyGrantString, StoredGrantSpelling, CanonicalGrantKey)
			// -- ParseAPIGrant, IsDangerousGrant, GrantsSubsetOf,
			// CapabilityPolicy.Permits and the catalogue all ask it first
			// -- and SplitRequestGrants, which turns a request's grants:
			// list into a task's Repo/Base, Reads and Writes.
			"pkg/model/canonical_grant.go",
			// Which capabilities a proposal asked for beyond its
			// proposer's (EscalatedCapabilities), the check that holds it
			// for a person, and the self-approval bounds (SelfApproved,
			// AutoSubmitLimit) on how far a proposal tree auto-submits.
			"pkg/model/auto_submit.go",
		},
	},
	{
		ID:          "exec-sandbox",
		Description: "what a sandbox may run, the tools an agent may call, and what the orchestrator does with a run's output",
		prefixes: []string{
			"pkg/orchestrator/", "pkg/mcp/", "pkg/granule/", "pkg/safepath/",
			"pkg/k8s/", "pkg/hostd/", "pkg/localexec/", "pkg/runhost/", "pkg/sandboximage/",
			"pkg/hostinit/",
			"third_party/granule/",
		},
		// What every backend is wired with, what is forwarded into
		// guests and which git-proxy host sandboxes are told.
		files: []string{"pkg/daemon/run.go", "pkg/daemon/sandboxnet.go"},
	},
	{
		ID:          "credential-proxy",
		Description: "proxies and relays that hold or forward credentials",
		prefixes: []string{
			"pkg/gitproxy/", "pkg/modelproxy/", "pkg/pkgcache/", "pkg/relay/", "pkg/tunnel/",
			"cmd/grain-relay/", "cmd/grain-connect/",
			// grain-hub (and its pre-rename grain-registry shim and package path):
			// the IAP front door, entry visibility and on-behalf-of delegation,
			// the assertion signer, the edge credential stripping before a tunnel,
			// and grc_ credentials.
			"pkg/hub/", "cmd/grain-hub/", "pkg/registry/", "cmd/grain-registry/",
		},
	},
	{
		ID:          "merge-gate",
		Description: "what decides a build is green and which checks a change owes: CI planning and execution, check reports, repository declarations",
		prefixes:    []string{"pkg/ci/", "pkg/workflows/", "pkg/grit/", "cmd/grit/", "pkg/checkpost/", "cmd/checkpost/", "pkg/repodecl/"},
	},
	{
		ID:          "security-tests",
		Description: "the security regression suite, which every PR's gate runs",
		prefixes:    []string{"tests/security/"},
	},
	{
		ID:          "deploy",
		Description: "deployment and infrastructure definitions (terraform, kubernetes)",
		prefixes:    []string{"deploy/"},
		bases:       []string{"*.tf", "*.tfvars"},
	},
	{
		ID: "release-promotion",
		Description: "what makes a release official: the person-only approval workflow that pushes " +
			"vX.Y.0 and latest and the route that starts it; the release train that promotes a qualified " +
			"rc on its own -- its script, its operator settings (release.conf), its template, whose push: " +
			"list decides which tags it may move, and its schedule; and the workflow that publishes what it " +
			"promotes",
		RequiresHuman: true,
		// Before github-workflows and grain-config, which would otherwise
		// take the workflow's two copies and the template.
		files: []string{
			".grain/workflows/promote-release.yml", ".github/workflows/promote-release.yml",
			"pkg/ui/release_approval.go", ".grain/templates/release-train.yml",
			// The Inbox approval of a release on GitHub Actions (gap 4):
			// the signed record, the route that signs and dispatches it,
			// and the check the workflow verifies it with.
			"pkg/ui/release_approval_github.go", "pkg/releaseapproval/releaseapproval.go",
			"scripts/verify-release-approval.sh",
			// The continuous train's own promotion (playbook section 1):
			// the script that tags vX.Y.0 and latest on a qualified rc,
			// the settings that turn it on and off, its schedule, and the
			// workflow that publishes the release from the tag. The tick
			// wrapper too: it writes the CI verdicts and the trusted
			// qualification app the train promotes on.
			"scripts/release-from-trunk.sh", "scripts/release.conf", "scripts/release-train-task.sh",
			".grain/schedules/release-train.yml", ".github/workflows/publish-release.yml",
		},
	},
	{
		ID: "release-machinery",
		Description: "the release jobs and the scripts they run, which cut, build, publish and tag " +
			"releases but cannot make one official: that is the approval's (release-promotion)",
		// Released by a clean review, which checks they still cannot
		// reach the approval (design/playbook/releases.playbook.md §0.3).
		Review: []string{
			"the change cannot move a `vX.Y.Z` or `latest` git tag or image except as a result of a person's " +
				"approval in promote-release.yml or the release train's promotion of a qualified rc " +
				"(scripts/release-from-trunk.sh, publish-release.yml: release-promotion)",
			"it does not widen any workflow's `permissions:`, the secrets it reads, or who may dispatch it",
			"it does not weaken a check that refuses anybody but a person (`GRAIN_CI_REQUESTED_BY_KIND`)",
			"in scripts/release.conf, it does not raise `RELEASE_MAJOR`, which only a person may do",
		},
		// Guest-image cache-key and runtime build helpers invoked by
		// scripts/release-images.sh.
		prefixes: []string{"scripts/granule/"},
		files: []string{
			"scripts/release-images.sh",
			"scripts/release-notes.sh", "scripts/candidate-gate.sh",
			// What the release jobs run besides: the patch-release branch
			// and tag script, the patch-tag guard that refuses a vX.Y.Z not
			// cut from its release branch, the version every published
			// image is tagged with, and the host setup the publishing job
			// sources.
			"scripts/release-patch.sh", "scripts/patch-tag-guard.sh",
			"scripts/build-version.sh", "scripts/grain-ci-host.sh",
			// The VCS-stamp flag every release binary and image is built
			// with, the Docker Hub login the publishing jobs run with
			// DOCKER_PAT, and the registry and repository names every
			// image is published under.
			"scripts/buildvcs.sh", "scripts/configure-dockerhub-auth.sh", "scripts/release-image-repos.sh",
			// The rc's Actions housekeeping: re-running qualification jobs
			// and cancelling a dropped rc's runs.
			"scripts/release-train-actions.sh", ".github/workflows/rc-qualification.yml",
			// Release assets (docs/release-assets.md): the publisher every
			// assets job runs with contents: write, the CLI archive builder
			// whose output it publishes on every release, and the workflow
			// that runs them on the release train's tag pushes.
			"scripts/release-assets.sh", "scripts/build-cli.sh", ".github/workflows/release-train-assets.yml",
			".grain/workflows/release.yml", ".github/workflows/release.yml",
			".grain/workflows/promote.yml", ".github/workflows/promote.yml",
			".grain/workflows/release-train.yml", ".github/workflows/release-train.yml",
		},
	},
	{
		ID:          "github-workflows",
		Description: "GitHub Actions workflows",
		prefixes:    []string{".github/workflows/"},
	},
	{
		ID: "ci-suppressions",
		Description: "CI baselines and quarantined tests, which tell grain's gate which failures to ignore, " +
			"and the GitHub CI path filter, which decides which of the Tests workflow's jobs run at all",
		RequiresHuman: true,
		prefixes:      []string{".grain/baselines/"},
		// A job the path filter skips is not a failing one, so the
		// filter suppresses failures the same way a baseline does.
		files: []string{".grain/quarantine.yml", ".grain/quarantine.yaml", "scripts/github-ci-changes.sh"},
	},
	{
		ID: "path-triggers",
		Description: "the repository's own path triggers, which decide what its pull requests are held for " +
			"and who may release them",
		RequiresHuman: true,
		OwnerApproval: true,
		// Its rules are read from the base, so a change cannot loosen the
		// rule judging it -- but once merged it judges everything after
		// it, so it is released only by the people who own the repository.
		// The templates those triggers file are guarded the same way, by
		// orchestrator's repoGuard: which ones they are is the
		// repository's to say, so they cannot be listed here.
		prefixes: []string{".grain/triggers/"},
	},
	{
		ID: "merge-rules",
		Description: "the repository's own merge rules, which decide per path which CI gates a merge, " +
			"whether it may merge itself and how long the queue waits on CI",
		RequiresHuman: true,
		OwnerApproval: true,
		// Read from the base like the triggers, so a change cannot loosen
		// the rules judging it; once merged it judges everything after it.
		files: []string{".grain/merge-rules.yml", ".grain/merge-rules.yaml", ".grain/sensitive.yml"},
	},
	{
		ID:          "grain-config",
		Description: "the repository's own grain declarations: CI workflows, schedules, suites, templates, settings",
		prefixes:    []string{".grain/"},
	},
	{
		ID:          "state-repo",
		Description: "the state repository sync, which writes this deployment's settings, templates, schedules and suites",
		prefixes:    []string{"pkg/staterepo/"},
	},
	{
		ID:          "sandbox-images",
		Description: "Granulefiles and Dockerfiles: what every sandbox and deployment image is built from",
		bases:       []string{"Granulefile", "Granulefile.*", "Dockerfile", "Dockerfile.*", "*.Dockerfile"},
	},
	{
		ID:          "upgrade-images",
		Description: "how a deployment picks, assembles and runs the images it upgrades to",
		prefixes:    []string{"pkg/upgrade/", "pkg/lifecycle/", "pkg/installtrack/"},
	},
	{
		ID:          "setup-scripts",
		Description: "setup scripts run with the deployment's own privileges",
		files: []string{
			"scripts/setup.sh",
			// Loads a release's image and installs the binary out of it.
			"scripts/install-grain.sh",
			// Bake the per-release Grain Hub GCE VM image and run at boot as
			// root on every hosted Grain Hub GCE VM instance.
			"scripts/grain-instance-init.sh", "scripts/bake-gce-release-image.sh",
			// Served unauthenticated and run as root on every hostd machine,
			// and the route that renders it and decides who sees what.
			"pkg/ui/hostd_install.sh", "pkg/ui/hostd_install.go",
			// Served unauthenticated and piped into sh on operator and
			// developer workstations, and the route that renders it and
			// exempts it from the single-tenant access code.
			"pkg/ui/cli_install.sh", "pkg/ui/cli_install.go",
		},
	},
}

// Match is one category a change touched and the paths that put it there.
type Match struct {
	Category Category
	Paths    []string
}

// Classify returns the categories paths touch, in Categories' order, each
// with the paths (sorted, deduplicated) that matched it. Nil means the
// change touches nothing on the list.
//
// Paths are repository-relative with forward slashes, as GitHub reports
// them. A leading "./" or "/" is ignored rather than trusted to mean
// something else.
func Classify(paths []string) []Match {
	byCategory := map[string]map[string]bool{}
	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+p), "/"), "./")
		if p == "" || p == "." {
			continue
		}
		if c, ok := categoryOf(p); ok {
			if byCategory[c] == nil {
				byCategory[c] = map[string]bool{}
			}
			byCategory[c][p] = true
		}
	}
	var out []Match
	for _, c := range Categories {
		set := byCategory[c.ID]
		if len(set) == 0 {
			continue
		}
		m := Match{Category: c}
		for p := range set {
			m.Paths = append(m.Paths, p)
		}
		sort.Strings(m.Paths)
		out = append(out, m)
	}
	return out
}

// OwnerApproval reports whether any of matches is a category only the
// repository's owners may release.
func OwnerApproval(matches []Match) bool {
	for _, m := range matches {
		if m.Category.OwnerApproval {
			return true
		}
	}
	return false
}

// RequiresHuman reports whether any of matches is a category a clean
// automated review cannot release.
func RequiresHuman(matches []Match) bool {
	for _, m := range matches {
		if m.Category.RequiresHuman {
			return true
		}
	}
	return false
}

func categoryOf(p string) (string, bool) {
	// File names are compared case-insensitively: "dockerfile" builds an
	// image as surely as "Dockerfile" does.
	base := strings.ToLower(path.Base(p))
	for _, c := range Categories {
		for _, f := range c.files {
			if p == f {
				return c.ID, true
			}
		}
		for _, pre := range c.prefixes {
			if strings.HasPrefix(p, pre) {
				return c.ID, true
			}
		}
		for _, pat := range c.bases {
			if ok, _ := path.Match(strings.ToLower(pat), base); ok {
				return c.ID, true
			}
		}
	}
	return "", false
}

// StatePrivilege is the category ClassifyState reports: a change to the
// deployment's own state repository that grants reach, binds a secret or
// loosens isolation. What merges there is applied to the running daemon
// on its next tick (staterepo.Apply), so a change that could merge itself
// would be a grant nobody made. No review releases it: a person presses
// Submit.
var StatePrivilege = Category{
	ID: "state-privilege",
	Description: "this deployment's own settings that grant capabilities, repositories or secrets, " +
		"or set what a sandbox runs and how it is isolated -- applied to the running daemon once merged",
	RequiresHuman: true,
}

// TemplatePush is the category orchestrator's security hold reports for
// a change to any repository-declared template's `push:` list
// (design/playbook/releases.playbook.md §0.3, §0.4). A push set is a
// grant: it lets every task filed from that template update refs beyond
// its own branch -- the release train's is how it tags release
// candidates -- so it is made by a person, once, when it merges, and
// widened only by a person. Which files carry one is the repository's to
// say, so it cannot be a path on Categories: the hold compares each
// changed template's push set at the base with the head's
// (repodecl.TemplatePush), and holds every changed template as if its
// set changed when it cannot read both.
var TemplatePush = Category{
	ID: "template-push",
	Description: "a template's `push:` list: the refs beyond their own branch that the tasks filed from it " +
		"may push, which only a person grants or widens",
	RequiresHuman: true,
}

// ConfigPolicyPath is the repository-relative path of the config repository's
// auto-merge exception policy. It is evaluated from the config repository's
// base branch (main), never from an unmerged pull request head, and is itself
// listed in NeverAutoMergePaths so only a human Submit can modify it.
const ConfigPolicyPath = "settings/config-policy.yml"

// NeverAutoMergePaths is the hardcoded ceiling of config-repository path globs
// that always require a human Submit in grain/config, even if a pattern in
// settings/config-policy.yml (auto_merge_paths) would otherwise match them.
var NeverAutoMergePaths = []string{
	// The exception policy itself: only a human may change what auto-merges.
	ConfigPolicyPath,
	// Declarative settings documents that define capabilities, task creation
	// defaults, templates, schedules, and suites.
	"settings/capabilities.yml",
	"settings/new-task-form.yml",
	"settings/templates.yml",
	"settings/schedules.yml",
	"settings/suites.yml",
	// Core configuration, scope sets, credentials, backends, console sessions,
	// and workflow tables.
	"tables/grain_config.json",
	"tables/repo_config.json",
	"tables/api_scope_set.json",
	"tables/git_credential.json",
	"tables/repo_git_credential.json",
	"tables/agent_backend.json",
	"tables/console_session.json",
	"tables/repo_workflow.json",
	// Templates and schedules, for a dump that still carries them as
	// tables rather than settings/{templates,schedules}.yml: the repos
	// they read and land work in, the capabilities they grant and what
	// their firings run.
	"tables/template.json",
	"tables/template_read.json",
	"tables/template_write.json",
	"tables/template_grant.json",
	"tables/template_field.json",
	"tables/template_custom_field.json",
	"tables/template_sequence.json",
	"tables/schedule.json",
	"tables/schedule_read.json",
	"tables/schedule_write.json",
	"tables/schedule_grant.json",
	"tables/schedule_custom_field.json",
	"tables/schedule_sequence.json",
	// Standing and persistent sandboxes -- the image a sandbox runs and
	// the secret-store keys bound into it as env vars -- and the
	// deployment's GCP keys.
	"tables/standing_sandbox.json",
	"tables/standing_sandbox_secret.json",
	"tables/standing_sandbox_env.json",
	"tables/persistent_sandbox.json",
	"tables/gcp_key.json",
	// Suites, for a dump that still carries them as tables rather than
	// settings/suites.yml: whether the tasks a suite run files need a
	// person's approval, auto-merge, or auto-approve their proposals.
	"tables/suite.json",
	"tables/suite_item.json",
	"tables/suite_sequence.json",
	// Qualification plans: whether a run's tasks may dispatch without a
	// person's approval, and whether its candidate is promoted on its own.
	"tables/qualification_config.json",
	"tables/qualification_item.json",
	"tables/qualification_item_depends_on.json",
	// Token budgets and the usage mode (model.UsageSettings): how many
	// runs dispatch at once under a provider's budget, and whether the
	// automatic cap holds them at all -- spend, applied on the next tick.
	"tables/usage_setting.json",
	// Extracted scripts, CI workflows, and schema version.
	"scripts/**",
	"workflows/**",
	".github/**",
	".grain/**",
	"schema-version",
}

// stateScopePaths is what a config-repository change to each path confers
// once staterepo.Apply imports it, said as the API scopes a person must
// hold to make the same change directly (ui's route scopes): a change
// that adds a host script or widens a scope set is a capabilities:admin
// grant whichever route it took, and one that binds a credential is a
// secrets:write one. Every other path needs settings:write alone, which
// approving any config change already asks for (task 3965).
var stateScopePaths = []struct {
	glob   string
	scopes []string
}{
	// Capability servers (mcp_servers: host scripts, gcp admins and role
	// ceilings), capability policy and default capabilities live in the
	// deployment's config row; repo_config carries each repository's
	// capabilities; api_scope_set is what every role and api:* grant
	// resolves to.
	{"tables/grain_config.json", []string{"capabilities:admin"}},
	{"tables/repo_config.json", []string{"capabilities:admin"}},
	{"tables/api_scope_set.json", []string{"capabilities:admin"}},
	{"settings/capabilities.yml", []string{"capabilities:admin"}},
	{ConfigPolicyPath, []string{"capabilities:admin"}},
	// What templates, schedules, suites, sessions and qualification
	// plans hand the tasks they file: grants, reads, writes, approval.
	{"settings/templates.yml", []string{"capabilities:admin"}},
	{"settings/schedules.yml", []string{"capabilities:admin"}},
	{"settings/suites.yml", []string{"capabilities:admin"}},
	{"settings/new-task-form.yml", []string{"capabilities:admin"}},
	{"tables/template*.json", []string{"capabilities:admin"}},
	{"tables/schedule*.json", []string{"capabilities:admin"}},
	{"tables/suite*.json", []string{"capabilities:admin"}},
	{"tables/qualification*.json", []string{"capabilities:admin"}},
	{"tables/console_session.json", []string{"capabilities:admin"}},
	{"tables/repo_workflow.json", []string{"capabilities:admin"}},
	// What a sandbox runs and the secrets bound into it.
	{"tables/standing_sandbox.json", []string{"capabilities:admin"}},
	{"tables/persistent_sandbox.json", []string{"capabilities:admin"}},
	{"tables/standing_sandbox_secret.json", []string{"capabilities:admin", "secrets:write"}},
	{"tables/standing_sandbox_env.json", []string{"capabilities:admin", "secrets:write"}},
	// Credentials and keys.
	{"tables/gcp_key.json", []string{"capabilities:admin", "secrets:write"}},
	{"tables/git_credential.json", []string{"secrets:write"}},
	{"tables/repo_git_credential.json", []string{"secrets:write"}},
	{"tables/agent_backend.json", []string{"connections:write"}},
	// Code the daemon runs.
	{"scripts/**", []string{"capabilities:admin"}},
	{"workflows/**", []string{"capabilities:admin"}},
	{".github/**", []string{"capabilities:admin"}},
	{".grain/**", []string{"capabilities:admin"}},
	{"schema-version", []string{"capabilities:admin"}},
}

// GrainConfigPath is the deployment's config row in the config
// repository: one file holding plain settings beside the columns that
// grant (mcp_servers, capability_policy, default_capabilities, ...).
const GrainConfigPath = "tables/grain_config.json"

// grainConfigSettingsColumns are the tables/grain_config.json columns a
// change to which confers nothing beyond settings:write: worker and merge
// limits, sandbox sizing, CI retention, display defaults. It is an allow
// list on purpose. Every other column -- capability servers and policy,
// default capabilities, approval and auto-merge defaults, console
// sessions, the sandbox image and setup script, network access, the
// forge host, sign-in, Kubernetes placement, and any column a later
// schema adds -- is judged by grainConfigColumnScopes, and confers
// capabilities:admin there unless it names something narrower.
var grainConfigSettingsColumns = map[string]bool{
	"poll_interval_ms": true, "max_workers": true, "max_mergers": true,
	"max_merges_per_repo": true, "merge_batch_size": true, "merge_gather_window_ms": true,
	"max_ci_jobs": true, "max_agent_turns": true, "newest_first": true,
	"sandbox_cpus": true, "sandbox_memory_mb": true, "sandbox_disk_gb": true,
	"sandbox_cpu_overcommit": true, "sandbox_memory_overcommit": true, "sandbox_disk_overcommit": true,
	"sandbox_overcommit": true, "sandbox_memory_reserve_mb": true,
	"sandbox_disk_floor_share": true, "sandbox_disk_floor_min_mb": true,
	"show_closed_by_default": true, "default_filters": true, "default_model": true,
	"judgement_model": true, "environment_name": true, "time_zone": true,
	"upgrade_check_mode": true, "upgrade_check_pauses": true,
	"package_cache_enabled": true, "package_cache_size_gb": true,
	"actions_cache_enabled": true, "actions_cache_size_gb": true,
	"ci_run_history": true, "ci_step_output_mb": true, "ci_step_output_days": true,
	"ci_run_output_kb": true, "ci_cache_mb": true, "ci_cache_entries": true, "ci_cache_days": true,
	"ci_artifact_mb": true, "ci_artifact_run_mb": true, "ci_artifact_file_mb": true,
	"ci_artifact_days": true, "ci_jobs_per_run": true, "ci_age_boost_ms": true,
	"ci_reserve_after_ms": true, "base_ci_red_streak": true,
	"max_auto_submit_depth": true, "max_auto_submit_fan_out": true,
}

// grainConfigColumnScopes is what a change to one tables/grain_config.json
// column confers, beyond settings:write.
func grainConfigColumnScopes(column string) []string {
	switch {
	case grainConfigSettingsColumns[column]:
		return nil
	case column == "git_credential" || column == "git_connections":
		return []string{"secrets:write"}
	case column == "agent_backends" || column == "agent_backend_id" || column == "model_settings":
		return []string{"connections:write"}
	}
	return []string{"capabilities:admin"}
}

// ChangedColumns is the columns that differ between two versions of a
// config-repository table file -- a JSON array of row objects, as
// staterepo exports it -- sorted, each once, and whether it could say.
// A row added or removed changes every column it carries; a file that is
// missing on one side (nil) is all rows added or removed. ok is false
// when either side is not that shape, and a caller must then judge the
// change by its path alone.
func ChangedColumns(before, after []byte) (columns []string, ok bool) {
	rows := func(data []byte) (map[string]map[string]json.RawMessage, bool) {
		out := map[string]map[string]json.RawMessage{}
		if data == nil {
			return out, true
		}
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, false
		}
		for i, row := range list {
			key := fmt.Sprintf("#%d", i)
			if id, ok := row["id"]; ok {
				key = string(id)
			}
			out[key] = row
		}
		return out, true
	}
	a, okA := rows(before)
	b, okB := rows(after)
	if !okA || !okB {
		return nil, false
	}
	set := map[string]bool{}
	compare := func(x, y map[string]map[string]json.RawMessage) {
		for key, row := range x {
			other := y[key]
			for col, v := range row {
				w, present := other[col]
				if !present || !jsonEqual(v, w) {
					set[col] = true
				}
			}
		}
	}
	compare(a, b)
	compare(b, a)
	columns = make([]string, 0, len(set))
	for col := range set {
		columns = append(columns, col)
	}
	sort.Strings(columns)
	return columns, true
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

// StateScopes is every API scope, beyond the settings:write any config
// change needs, that a person must hold to let a change to these
// grain/config paths merge: what the change confers once it is applied
// (stateScopePaths), sorted, each once. A path outside settings/ and
// tables/ that stateScopePaths does not name (prose aside) confers
// capabilities:admin, since what it would do once applied is not
// something this build can say.
//
// columns, where it has an entry for a path, is the columns the change
// alters in that file (ChangedColumns), and the path is judged by them
// instead: so far only tables/grain_config.json, where a plain settings
// edit (max_workers, time_zone) needs settings:write alone and one to
// mcp_servers or default_capabilities still needs capabilities:admin. A
// path with no entry -- a change recorded before columns were, or a file
// ChangedColumns could not read -- is judged by its path, which for
// grain_config.json is capabilities:admin.
func StateScopes(paths []string, columns map[string][]string) []string {
	set := map[string]bool{}
	for _, raw := range paths {
		p := cleanStatePath(raw)
		if p == "" {
			continue
		}
		if cols, ok := columns[p]; ok && p == GrainConfigPath {
			for _, col := range cols {
				for _, sc := range grainConfigColumnScopes(col) {
					set[sc] = true
				}
			}
			continue
		}
		matched := false
		for _, e := range stateScopePaths {
			if MatchStatePath(e.glob, p) {
				matched = true
				for _, sc := range e.scopes {
					set[sc] = true
				}
			}
		}
		if !matched && !strings.HasPrefix(p, "settings/") && !strings.HasPrefix(p, "tables/") &&
			!strings.HasSuffix(p, ".md") {
			set["capabilities:admin"] = true
		}
	}
	out := make([]string, 0, len(set))
	for sc := range set {
		out = append(out, sc)
	}
	sort.Strings(out)
	return out
}

// ConfigPolicy is the parsed representation of settings/config-policy.yml.
type ConfigPolicy struct {
	AutoMergePaths []string `yaml:"auto_merge_paths"`
}

// ParseConfigPolicy parses settings/config-policy.yml syntax and validates
// that every entry in auto_merge_paths is a well-formed relative path glob.
// An empty or comment-only document parses as a policy with no exceptions.
func ParseConfigPolicy(data []byte) (ConfigPolicy, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return ConfigPolicy{}, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var policy ConfigPolicy
	if err := dec.Decode(&policy); err != nil {
		if errors.Is(err, io.EOF) {
			return ConfigPolicy{}, nil
		}
		return ConfigPolicy{}, fmt.Errorf("parse %s: %w", ConfigPolicyPath, err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return ConfigPolicy{}, fmt.Errorf("parse %s: multiple YAML documents are not allowed", ConfigPolicyPath)
	} else if !errors.Is(err, io.EOF) {
		return ConfigPolicy{}, fmt.Errorf("parse %s: %w", ConfigPolicyPath, err)
	}
	for i, raw := range policy.AutoMergePaths {
		if err := validateStateGlob(raw); err != nil {
			return ConfigPolicy{}, fmt.Errorf("%s: auto_merge_paths[%d] %q: %w", ConfigPolicyPath, i, raw, err)
		}
		policy.AutoMergePaths[i] = strings.TrimSpace(raw)
	}
	return policy, nil
}

// ValidateConfigPolicy validates a parsed ConfigPolicy, rejecting invalid
// globs and any auto_merge_paths entry that matches or overlaps a path in
// NeverAutoMergePaths.
func ValidateConfigPolicy(policy ConfigPolicy) error {
	for i, raw := range policy.AutoMergePaths {
		if err := validateStateGlob(raw); err != nil {
			return fmt.Errorf("%s: auto_merge_paths[%d] %q: %w", ConfigPolicyPath, i, raw, err)
		}
		pat := strings.TrimSpace(raw)
		for _, never := range NeverAutoMergePaths {
			if stateGlobsOverlap(pat, never) {
				return fmt.Errorf("%s: auto_merge_paths[%d] %q matches NeverAutoMergePaths entry %q (never allowed to auto-merge)", ConfigPolicyPath, i, pat, never)
			}
		}
	}
	return nil
}

// CheckConfigPolicy parses settings/config-policy.yml and validates it against
// NeverAutoMergePaths.
func CheckConfigPolicy(data []byte) (ConfigPolicy, error) {
	policy, err := ParseConfigPolicy(data)
	if err != nil {
		return ConfigPolicy{}, err
	}
	if err := ValidateConfigPolicy(policy); err != nil {
		return ConfigPolicy{}, err
	}
	return policy, nil
}

// IsNeverAutoMergePath reports whether filePath matches any entry in
// NeverAutoMergePaths.
func IsNeverAutoMergePath(filePath string) bool {
	p := cleanStatePath(filePath)
	if p == "" {
		return false
	}
	for _, never := range NeverAutoMergePaths {
		if MatchStatePath(never, p) {
			return true
		}
	}
	return false
}

// ClassifyState classifies paths in a pull request against this deployment's
// own state repository (grain/config), which the caller -- not the repository
// -- decides it is.
//
// Every changed path in grain/config requires a human Submit (StatePrivilege,
// RequiresHuman: true) by default. A path is exempt only when it matches one
// of the opt-in globs in autoMergePaths (loaded from settings/config-policy.yml
// on the config repository's base branch) AND does not match any entry in
// NeverAutoMergePaths, which is evaluated first as a hardcoded ceiling.
func ClassifyState(paths []string, autoMergePaths ...string) []Match {
	set := map[string]bool{}
	for _, raw := range paths {
		p := cleanStatePath(raw)
		if p == "" {
			continue
		}
		if IsNeverAutoMergePath(p) {
			set[p] = true
			continue
		}
		if matchesAnyStateGlob(p, autoMergePaths) {
			continue
		}
		set[p] = true
	}
	if len(set) == 0 {
		return nil
	}
	m := Match{Category: StatePrivilege}
	for p := range set {
		m.Paths = append(m.Paths, p)
	}
	sort.Strings(m.Paths)
	return []Match{m}
}

func cleanStatePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+p), "/"), "./")
	if p == "" || p == "." {
		return ""
	}
	return p
}

func matchesAnyStateGlob(filePath string, patterns []string) bool {
	for _, raw := range patterns {
		pat := strings.TrimSpace(raw)
		if pat == "" || validateStateGlob(pat) != nil {
			continue
		}
		if MatchStatePath(pat, filePath) {
			return true
		}
	}
	return false
}

func validateStateGlob(pattern string) error {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return errors.New("empty path glob")
	}
	if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return errors.New("path glob must be relative and must not start or end with '/'")
	}
	if strings.Contains(p, "\\") {
		return errors.New("path glob must use '/' separators")
	}
	segs := strings.Split(p, "/")
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path segment %q", seg)
		}
		if seg == "**" {
			continue
		}
		if strings.Contains(seg, "**") {
			return fmt.Errorf("'**' must be a full path segment in %q", seg)
		}
		if _, err := path.Match(seg, "x"); err != nil {
			return fmt.Errorf("invalid glob syntax in segment %q: %w", seg, err)
		}
	}
	return nil
}

// MatchStatePath reports whether a slash-separated path matches pattern.
// Single-segment wildcards (*, ?, [...]) follow path.Match; a "**" segment
// matches zero or more path segments.
func MatchStatePath(pattern, filePath string) bool {
	pat := strings.Trim(strings.TrimSpace(pattern), "/")
	p := cleanStatePath(filePath)
	if pat == "" || p == "" {
		return false
	}
	return matchStateSegments(strings.Split(pat, "/"), strings.Split(p, "/"))
}

func matchStateSegments(patSegs, pathSegs []string) bool {
	if len(patSegs) == 0 {
		return len(pathSegs) == 0
	}
	if patSegs[0] == "**" {
		if matchStateSegments(patSegs[1:], pathSegs) {
			return true
		}
		return len(pathSegs) > 0 && matchStateSegments(patSegs, pathSegs[1:])
	}
	if len(pathSegs) == 0 {
		return false
	}
	ok, err := path.Match(patSegs[0], pathSegs[0])
	if err != nil || !ok {
		return false
	}
	return matchStateSegments(patSegs[1:], pathSegs[1:])
}

// stateGlobsOverlap reports whether there exists at least one concrete
// non-empty relative path matched by both glob a and glob b.
func stateGlobsOverlap(a, b string) bool {
	a = strings.Trim(strings.TrimSpace(a), "/")
	b = strings.Trim(strings.TrimSpace(b), "/")
	if a == "" || b == "" {
		return false
	}
	return segmentsOverlap(strings.Split(a, "/"), strings.Split(b, "/"))
}

func segmentsOverlap(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) > 0 && a[0] == "**" {
		if segmentsOverlap(a[1:], b) {
			return true
		}
		if len(b) > 0 {
			return segmentsOverlap(a, b[1:])
		}
		return false
	}
	if len(b) > 0 && b[0] == "**" {
		if segmentsOverlap(a, b[1:]) {
			return true
		}
		if len(a) > 0 {
			return segmentsOverlap(a[1:], b)
		}
		return false
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if !singleSegmentOverlaps(a[0], b[0]) {
		return false
	}
	return segmentsOverlap(a[1:], b[1:])
}

func singleSegmentOverlaps(a, b string) bool {
	if a == b || a == "*" || b == "*" {
		return true
	}
	if ok, err := path.Match(a, b); err == nil && ok {
		return true
	}
	if ok, err := path.Match(b, a); err == nil && ok {
		return true
	}
	return false
}
