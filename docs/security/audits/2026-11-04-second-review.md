# Security review: merges to `main`, 2026-11-04 (second review, 04:05 → 15:21 UTC)

- **Date:** 2026-11-04 (UTC), second review of the day. The first,
  `2026-11-04.md`, audited up to `b96b128a` (04:05 UTC) and was itself
  merged in this range (#666). This file picks up where it stopped; the
  next review should start from the base below, not from
  `2026-11-04.md`'s.
- **Auditor:** grain task 2977 (agent), the nightly review of the day's
  merges, in the shape of task 907's 2.4 audit
  (`2026-10-25-2.4-audit.md`)
- **Base audited:** `origin/main` at `d197b35a` (merge of #692,
  2026-11-04 15:21 UTC)
- **Range:** `b96b128a..d197b35a` on `main`'s first-parent line: the 47
  pull requests merged since the previous report's base `b96b128a`
  (merge of #541, 04:05 UTC); the oldest in the range is #656 (04:42
  UTC).
- **Method:** every merged pull request's file list was run through
  `trustboundary.Classify` (the repository's own list, v15 at the head)
  and mapped to the surfaces below; the in-scope code was read together
  with what it calls across the boundary; `go test
  ./pkg/trustboundary/... ./tests/security/...` and the suites of the
  touched boundary packages were run; each finding marked *reproduced*
  was confirmed with a throwaway test against the real code, which is
  not committed. Fix tasks are filed one per finding with
  `propose_task` (`repo: ai-on-gke/grain`, `branch: main`).

> **Dedup note.** This task holds neither `self-debug` nor
> `tasks:read`, so `grain task list --search …` (`adopt`, `owner-trust`,
> `auto-merge`, `api/fs`, `src://`, `trust-boundary`, `classif`,
> `external_agents`, `security`) showed only task 2977 itself, as the
> deployment notes warn; the searches cannot rule out an open duplicate.
> The first 11-04 review's two findings are closed in this range: §1
> (`readsAll`) by #672 `1e33e823`, §2 (unclassified MCP endpoint, hostd
> install script, CI grant rule, auto-merge guard) by #673 `b9cac46f`,
> trust-boundary v15. Finding 3 below is the same *shape* as that §2 --
> and as 11-01 §2, 11-02 §1 and 11-03 §2 -- for the files this range
> added.

## Commits reviewed

First-parent merges, newest first. The last column is what
`trustboundary.Classify` reports for the pull request's full file list
(with the number of files per category); "—" means the list touched
nothing on it: `ui/`, `docs/`, `design/`, tests, or code outside every
category. 19 of the 47 are "—". Merge times are UTC.

| Merge | PR / task | Change | Trust-boundary categories (files) |
| --- | --- | --- | --- |
| `d197b35a` | #692 task-2366 | feat(capabilities): convert local-scripts declarations to host-script configuration | auth-routes (1), capabilities-secrets (4), exec-sandbox (3) |
| `4c6fcebb` | #677 task-2973 | refactor(granule): import granule as github.com/gke-labs/granule | exec-sandbox (122), grain-config (1), release-machinery (1), sandbox-images (1), upgrade-images (2) |
| `b9cac46f` | #673 task-3000 | fix(security): classify the MCP endpoint, hostd install script, CI grant rule and auto-merge guard as trust-boundary (v15) | hold-policy (2) |
| `30b70418` | #680 task-3012 | feat(release): release a qualified rc automatically from a continuous train | credential-proxy (1), hold-policy (2), path-triggers (2), release-promotion (6) |
| `3d65e8ab` | #703 task-2961 | fix(ui): take a repo setting's refusal down once that setting saves | — |
| `c8b67a0e` | #700 task-2473 | test(release): run the train's dry run twice and expect the same cut | — |
| `0ea3da67` | #705 task-2965 | fix(ui): show that Settings → Upgrade is reading its status | — |
| `d25ec3da` | #697 task-2476 | fix: name the right tools and runs in PR, CI-log and close-log wording | exec-sandbox (4) |
| `45c94b1a` | #694 task-2960 | fix(ui): keep both asked states together when a list is sorted by state | — |
| `e84be29a` | #706 task-3054 | docs: catch the docs up with the last day's merges to main | — |
| `be8390ba` | #702 task-3090 | fix(cli): drop the duplicate "login" alias that broke the build | — |
| `b725bf46` | #704 task-2964 | docs(test-runs): record the 2026-11-04 connections and infrastructure settings walkthrough | — |
| `0c8855db` | #701 task-2963 | docs(test-runs): record the 2026-11-04 Settings walkthrough | — |
| `4460d761` | #698 task-2475 | fix(mcp): stop calling a branch unpushed when the whole repository 404s | exec-sandbox (5) |
| `249f92a0` | #696 task-2962 | fix(ui): preview a saved schedule with the model it asks for | — |
| `d2cdb59a` | #512 branch feat/git-login-task-attribution (human-authored) | feat(git-login): add delegated GitHub login and user PR/comment signatures | capabilities-secrets (1), credential-proxy (2), exec-sandbox (2) |
| `cb3d34aa` | #687 task-2799 | feat(cli): named contexts, grain login/logout/whoami and credential sources | auth-routes (4), security-tests (2) |
| `59816254` | #679 task-2978 | feat(setup): default fresh installs to a local Postgres, keep existing SQLite | deploy (2), setup-scripts (1) |
| `62557f64` | #695 task-2959 | fix(ui): count closed work the rail has not paged in yet | auth-routes (1) |
| `d665c1ae` | #451 task-2451 | fix(mcp): say why pull_request_status shows no review-thread state | exec-sandbox (2) |
| `0affd663` | #682 task-2802 | feat(orchestrator): adopt pull requests in review-only or finish mode | auth-routes (4), exec-sandbox (4), security-tests (1) |
| `a3bcafa9` | #693 task-2958 | docs(test-plans): add the 2026-11-04 nightly new features test plan | — |
| `eba9007e` | #691 task-2458 | feat(orchestrator): fence container sandboxes with nftables, refuse grants when unfenced | exec-sandbox (6) |
| `5eb6b1c0` | #688 task-2970 | docs(test-runs): record the Cloud Run IAP qualification on the main image | — |
| `cc66a6bc` | #689 task-2759 | fix(cli): let the workstation starter policy run the Director chat | — |
| `09d37611` | #683 task-2801 | feat(cli): add external grain ci wait/retrigger/test-history and grain merge-queue | exec-sandbox (1), merge-gate (2) |
| `b7b6160b` | #686 (other deployment, task-19) | Add daily, weekly and monthly direction rollups to cluster stats | — |
| `72969536` | #681 task-2983 | feat(ui): poll only live tasks, without descriptions, and gzip responses | auth-routes (2) |
| `2bf176c3` | #678 task-2979 | fix(ci): read GitHub Actions job and step logs by bare run id | credential-proxy (1), merge-gate (3) |
| `1e33e823` | #672 task-3001 | fix(security): hold an agent's readsAll edit to the gate's reads rule | auth-routes (1) |
| `f955d19d` | #669 task-2999 | feat(ui): search the Legacy capabilities and suite Templates pickers | — |
| `6797020f` | #652 task-2942 | feat(access): admit service accounts on the database allowlist, with a reason | auth-routes (3) |
| `6f52ca9d` | #674 (other deployment, task-18) | Distinguish answered report tasks from parked questions in UI | — |
| `2f57dd98` | #670 (other deployment, task-16) | Show blocking tasks under Blocked badge and omit self-depends_on on repo-less tasks | exec-sandbox (2) |
| `9671f86f` | #668 (other deployment, task-15) | Skip CI sandbox capacity gates for in-process builtin jobs | merge-gate (3) |
| `dc96d10d` | #671 task-3002 | fix(releases): say when grain/rc-hold cannot be read, and grant the train checks: read | release-machinery (1), release-promotion (1) |
| `f673310a` | #667 task-2996 | test(release): pin rollback record fields and document the rollback approval | release-promotion (2) |
| `1a32ce4a` | #629 task-2797 | feat(external-agents): attribute gma_ tokens to an owner, record X-Grain-Client, add owner-trust approval | auth-routes (2), exec-sandbox (3) |
| `298848da` | #665 task-2824 | feat(ci): let a task read the release train's Stage 2 check run through grain | credential-proxy (2), merge-gate (2), release-promotion (2) |
| `524032e3` | #655 task-2916 | feat(releases): wait on grain/rc-hold before cutting rc.(N+1) | release-promotion (1) |
| `9f769328` | #659 task-2911 | feat(ui): move Settings, model, connection and release pickers onto SearchSelect | — |
| `6962e6a2` | #663 (other deployment, task-13) | Flag under-resourced disk and host budgets against concurrency settings | setup-scripts (1) |
| `31b8bba1` | #664 task-2943 | feat(release): add person-only release rollback route, workflow job, and CLI | auth-routes (3), exec-sandbox (1), merge-gate (5), release-promotion (6), security-tests (1) |
| `c585df9f` | #658 (other deployment, task-8) | Support fork-to-upstream pull request workflow in RepoConfig and EnsurePullRequest | exec-sandbox (16), state-repo (1) |
| `ac9d9586` | #666 task-2977 | docs(security): add the 2026-11-04 nightly security review of main | — |
| `cb87335a` | #660 task-2910 | feat(ui): search the task, schedule and template forms' data-driven pickers | — |
| `2b97a658` | #656 task-2798 | feat(ui): serve the namespace at GET /api/fs/<scheme>/<path> | auth-routes (4), exec-sandbox (1), github-workflows (1), grain-config (1), security-tests (1) |

## Trust-boundary surfaces touched

Of the surfaces the review brief names:

- **Agent API scopes and route authorization** -- touched heavily. New
  routes: `GET /api/fs/{ci,task,deploy,config,memory,src}/{path...}`
  (one scope each; #656) with a new `deployment:src` scope; `GET
  /api/auth/whoami` (internal, answers anybody; #687) and a credential's
  own `whoami`/`revoke` let past the gate whatever its scopes
  (`ownCredentialRoute`); `POST /api/pull-requests/{adopt,review}` and
  `GET /api/pull-requests/status` (`merge:write`, `tasks:write`,
  `ci:read`; #682); `POST /api/releases/{version}/rollback`
  (`releases:write`, person-only in the client; #664). The external
  filing escalation gained `hostd`, `keepRunning`, `priority`,
  `fixesBaseBranch`, `interactive`, `creator` and template-carried
  checks, and `RequestActor` now carries a `gma_` token's owner as
  `OnBehalfOf` (#629). `.grain/baselines/route-authorization.txt` is
  unchanged and empty.
- **Capability grants and placements** -- touched: local-scripts
  declarations converted to host-script configuration at daemon start,
  with the migrated grants refused to untrusted runs and CI jobs the
  way local-scripts refused them (#692); external capability grants
  refused on an unfenced container backend (#691); the git-login
  delegated credential and its author signature (#512).
- **Sandbox backends** -- `pkg/granule`, `third_party/granule` and the
  Dockerfile changed only by the import path rename
  `bwsalmon/granule` → `gke-labs/granule` (#677; the diff of
  `third_party/granule` contains no line that is not that rename, and
  `go.mod` still replaces it with `./third_party/granule`). `pkg/k8s`
  and `k8s_granule_sandboxes.go` were **not touched**. The container
  backend gained a per-container nftables fence (#691).
- **Release and secrets gating** -- touched: the trust-boundary list
  went v14 → v15 (#673 classified the first review's §2 files; #680
  moved `release-from-trunk.sh`, `release.conf`, the new
  `release-train-task.sh`, the train's schedule and
  `publish-release.yml` from `release-machinery` to
  `release-promotion`, since the train now promotes a qualified rc with
  no person's approval); `promote-release.yml` gained the rollback job
  (#664, #667); the train waits on `grain/rc-hold` (#655, #671) and a
  task may read the Stage 2 check run (#665).
- **State-repo sync** -- `pkg/staterepo` changed only in
  `fidelity_test.go` (#658's `upstream_repo` column, schema v105).
  `pkg/orchestrator/sync.go` was not touched.
- **`pkg/trustboundary` and the security hold** -- the list changed
  twice (above); `securityhold.go` was not touched.
- **Path containment (`pkg/safepath`)** -- not touched.

## Tests run

- `go test ./pkg/trustboundary/... ./tests/security/...` -- **pass**
  (`ok pkg/trustboundary 0.4s`, `ok tests/security 21.8s`).
- The touched boundary packages: `go test ./pkg/ui/ ./pkg/model/...
  ./pkg/orchestrator/ ./pkg/capability/... ./pkg/namespace/...
  ./pkg/gitproxy/ ./pkg/ci/... ./pkg/github/... ./pkg/releaseapproval/
  ./pkg/grit/... ./pkg/mcp/ ./pkg/daemon/ ./pkg/cli/` -- **all pass**
  (49 packages `ok`; see "Package suites" at the end). No failure,
  security or environmental.

## Findings

Three findings. Finding 1 is a new route that hands an agent what the
rest of the agent API refuses every agent -- auto-merge. Finding 2 is a
new route that reads grain's source under a scope the sandbox namespace
reserves for `self-debug`. Finding 3 is the recurring shape: the day's
new authorization code, including the file that authenticates every
`gma_` bearer token, is not on the trust-boundary list.

### 1. An agent can turn on auto-merge by adopting a collaborator's pull request, which the agent API refuses it everywhere else -- **Medium**, reproduced

**Who can do what.** An acting task holding `api:merge:write`,
`api:tasks:write` and `api:tasks:admin` on a repository, or an external
`gma_` credential holding `merge:write` and `tasks:write` there whose
owner the deployment's owner-trust policy (#629) trusts for that
repository, can `POST /api/pull-requests/adopt {"pr": "<owner/name#n>",
"mode": "finish"}` on any open pull request whose author is a
collaborator, and get back an **approved, auto-merge** shepherd task
that pushes commits onto that pull request and merges it once grain's
own (agent-run) reviews and CI pass -- with no person's Submit. The
agent API's rule is that no agent turns auto-merge on, with or without
`tasks:admin`: `agentFilingEscalation` and
`externalAgentFilingEscalation` (`pkg/ui/agent_api_gate.go:1045`,
`:1092`, `:1164`, `:1984`, `:2032`, `:2109`) answer `autoMerge: true`,
an auto-merge template and `POST /api/tasks/{id}/submit` with
`errAgentAutoMerge` ("letting a change merge unreviewed is a person's
decision"), and owner-trust's own contract is that a trusted owner's
filings *queue without a person's approval*, not that they merge.

**Where.** `pkg/ui/pull_requests_api.go` `pullRequestFiling` (`:121`)
returns `approved = true` for an agent with `tasks:admin` on the
repository or an owner-trusted credential; `AdoptPullRequest` (`:159`)
passes that as `Approved`; `pkg/orchestrator/takeover_adopt.go`
`fileAdoptedShepherd` sets `AutoMerge: in.Approved && authorTrusted`
(`:305`) and `Approval: &in.Requester` (`:256`), i.e. the agent approves
its own filing. The spec (`external-cli.spec.md#XC-28` item 3) says
"`autoMerge` follows takeover's existing trusted-author rule", but the
existing rule in `pkg/orchestrator/takeover.go:176-288` is a
trusted-**sender** rule: `senderTrusted` is run on the maintainer who
typed `/grain takeover` in a GitHub comment -- a person -- and
`AutoMerge: trusted` is that person's decision. The adopt route runs the
same `senderTrusted` on the pull request's **author** (`:176`) while the
requester is the agent, so the one human in the webhook rule is gone.

**Reproduced.** A throwaway test in `pkg/orchestrator` (not committed)
on the `takeoverFixture` with collaborator author `alice`:
`AdoptPullRequest(ctx, deps, {Approved: true, Mode: finish, Requester:
{Actor: agent external:reviewer-bot (external-token), OnBehalfOf: human
carol}})` filed task 1 `state=queued`, `autoMerge=true`, `approval.actor
= agent external:reviewer-bot`. The API-side preconditions were read
rather than driven: `pullRequestFiling` approves on
`call.grants.Allows("tasks:admin", repo)` or
`model.OwnerTrustedForTask(cfg.OwnerTrust, owner, repo, nil, nil)`.

**Severity.** Medium. It needs either `tasks:admin` (dangerous, a
person grants it, but the API still withholds auto-merge from a task
holding it) or an owner-trust rule for the credential's owner; what it
yields is an unreviewed merge of somebody else's branch, which the
trust-boundary and path-trigger holds narrow only where the files hit
the list.

**Fix task filed:** `fix(security): do not let an agent's pull-request
adoption turn on auto-merge`, filed from task 2977 with `propose_task`
(grain assigns the id when the run ends and links it from task 2977).
The fix sets `AutoMerge` only when the requester is a person
(`in.Requester.Actor.Kind == model.PrincipalHuman`, or the operator),
never from `in.Approved` alone, and has `AdoptPullRequest` record an
owner-trust approval as `model.OwnerTrustPrincipal` on behalf of the
owner (as `CreateTask` does, `pkg/ui/client.go:2845`) rather than as
the agent itself, so `SelfApproved`/`baserepairbypass.go` read it
right. Regression test:
`TestAdoptPullRequestByAnAgentNeverTurnsOnAutoMerge` in
`pkg/orchestrator/takeover_adopt_test.go` (agent requester with
`Approved: true` on a collaborator's PR → `AutoMerge == false`, approval
recorded as owner-trust), and a `pkg/ui/pull_requests_api_test.go` case
driving `POST /api/pull-requests/adopt` as an owner-trusted `gma_`
token and asserting the filed task has `autoMerge: false`.

### 2. A task holding only `deployment-health` reads grain's source through `GET /api/fs/src/`, which the sandbox namespace refuses it -- **Low**, reproduced

**Who can do what.** Any task whose grants include `api:deployment:read`
-- which the `deployment-health` capability expands to
(`pkg/model/agent_api.go:184`, `:515`), and which `api:deployment-health`,
`api:self-upgrade` and the read presets carry -- can read every file
under `src://` by calling `GET /api/fs/src/<path>` with its acting-task
identity, through `grain api`, the controller MCP's `grain_invoke`, or
the proxy's `/api/` hop. `src://` on the sandbox proxy (`/fs/src/`,
`pkg/model/store.go:4955`) is opened by **`self-debug`** and nothing
else; `deployment:read` is "Read host, sandbox, log, metric and upgrade
status" and is not dangerous, so an agent may pass it on to work it
files (`agentGrantRefusal`). `src://` is grain's source as this
deployment builds it -- the open core *and any private layer a
deployment adds*, redacted line by line for secrets but not for
anything else. The same change refuses it to an external `gma_` token
holding `deployment:read` unless it also holds the new `deployment:src`
("deployment:read is too ordinary a scope to carry it by itself",
`errExternalSrc`), which is the right rule; a task's run is held to a
looser one.

**Where.** `pkg/ui/api_fs.go` `fsScope` (`:146-147`): `case "src":
scope.MayReadSrc, scope.MayReadPlaybooks = true, true` for any agent
call the gate admitted, and the gate admits the route on
`deployment:read` alone (`route_scopes.go` `"GET /api/fs/src/{path...}":
"deployment:read"`); `authorizeAgentCall` has no counterpart to
`authorizeExternalAgentCall`'s `deployment:src` check
(`agent_api_gate.go:1712`). The repository's own test pins the
behaviour as intended for a task (`api_fs_test.go:231-239`, "src:// is
its under deployment:read"), so this is a design decision this review
disagrees with rather than an oversight -- it is still a second door
with a weaker lock than the first.

**Reproduced.** A throwaway test in `pkg/ui` (not committed) on
`fsTestServer`: task 31 seeded with the single grant `deployment-health`
and `X-Grain-Acting-Task: 31` → `GET /api/fs/src/README.md` answered
`200 "grain source\n"`.

**Severity.** Low: a read, redacted, of source that for this repository
is public, and the grant is one a person hands out; the failure mode is
a deployment with a private layer whose tasks hold `deployment-health`
for its health counters.

**Fix task filed:** `fix(security): hold a task's GET /api/fs/src/ to
self-debug or deployment:src, as the sandbox namespace does`, filed
from task 2977 with `propose_task` (linked from task 2977 once the run
ends). The fix has `authorizeAgentCall` refuse
`fsRoutes[pattern] == "src"` unless the acting task holds `self-debug`
or `api:deployment:src`, mirroring the external-token branch, and
`fsScope`'s `src` case set `MayReadSrc` from the same test. Regression
test: extend `TestAPIFsPerSchemeScopesRepoLimitsAndExternalSrcRefusal`
in `pkg/ui/api_fs_test.go` so task 9 (`api:deployment:read`) is
**refused** `/api/fs/src/README.md` with a message naming `self-debug`
and `deployment:src`, and a task seeded with `self-debug` reads it.
Because it changes a scope rule, the task is filed `auto_merge: false`,
`hold_for_approval: true`.

### 3. The day's new authorization code is unclassified, so an agent's auto-merge pull request can change it with no security hold -- **Medium**, reproduced

**Who can do what.** Any task with an auto-merge pull request against
`ai-on-gke/grain` -- the default for the deployment's own fix tasks --
can edit the files below and merge with no security review, no hold and
no person's Submit, because `trustboundary.Classify` returns nothing for
them and `securityhold.go` clears a pull request whose file list is
known and matches nothing. v15 (#673) closed the first 11-04 review's
list this morning; the merges since added these:

- **`pkg/ui/external_agents.go`** (#687, #629 this range; the file dates
  from #104) -- `authenticateExternalAgent` (`:542`), which is the
  bearer-token check for every `gma_` credential: hash lookup, expiry,
  rate limit; `CreateExternalAgentToken` (`:233`) and
  `externalAgentTokenOwner` (`:172`), which decide who may mint a
  credential, with which scopes and owned by whom;
  `selfServiceLimits` (`:213`). A one-line edit to the hash comparison
  admits any bearer as any credential. `agent_api_gate.go`, which calls
  it, is `auth-routes`; this file is nothing, and the list has never
  named it since the external agent API landed (10-29, #104).
- **`pkg/model/external_agent_token.go`** (#629) -- the token record,
  `GenerateExternalAgentToken` and `OwnerPrincipal`.
- **`pkg/ui/api_fs.go`** (#656) -- `fsScope` (`:106`), which hands a
  non-agent caller the whole namespace (`MayReadDB` included) and an
  agent the repositories its grants reach; `fsCallRepo` (`:170`), which
  names the repository the gate holds a repository-limited grant to.
  Dropping one line of `fsScope` gives every `gma_` token every
  repository's CI, every task and `config://` whole.
- **`pkg/namespace/namespace.go`, `pkg/namespace/stage2.go`** (#656,
  #678, #683) -- `Scope.PermitsCI`, `PermitsSrcPath`, `PermitsConfig`,
  `PermitsMemory`, `PermitsTask`: the namespace's own authorization,
  which both `/fs/` (credential-proxy) and `/api/fs/` defer to.
- **`pkg/ui/pull_requests_api.go`** (#682) -- `requirePullRequestScopes`
  and `pullRequestFiling` (finding 1's approval decision).
- **`pkg/model/owner_trust.go`** (#629) -- `OwnerTrusted` and
  `OwnerTrustedForTask`, which decide whose filings land approved with
  no person; returning `true` approves every external agent's work.
- **`pkg/ui/request_actor.go`** (#629) -- `RequestActor`, which is who
  every mutating route attributes its write to and now carries an
  external credential's owner as `OnBehalfOf`.
- **`pkg/ui/auth_whoami.go`** (#687) -- answers unauthenticated callers;
  what it may say about a credential is its own decision.

**Reproduced.** `trustboundary.Classify` on each path above returns
nil (the throwaway classifier run for the table; not committed). For
contrast, `pkg/ui/allowlist.go`, `agent_api_gate.go`, `route_scopes.go`
and `pkg/gitproxy/namespace.go` on the same run return `auth-routes` /
`credential-proxy`.

**Severity.** Medium, as the previous three days rated the same shape:
the hold is the one control between an agent's auto-merge and the
authentication path, and `external_agents.go` is the first file on
these lists that *authenticates* rather than authorizes.

**Fix task filed:** `fix(security): classify the external-agent
authentication, namespace scope, owner-trust and adopt files as
trust-boundary (v16)`, filed from task 2977 with `propose_task` (linked
from task 2977 once the run ends). The fix adds
`pkg/ui/external_agents.go`, `pkg/model/external_agent_token.go`,
`pkg/ui/api_fs.go`, `pkg/ui/auth_whoami.go`, `pkg/ui/request_actor.go`,
`pkg/ui/pull_requests_api.go` and `pkg/model/owner_trust.go` to
`auth-routes` and `pkg/namespace/` to `credential-proxy` (beside
`pkg/gitproxy/`), bumps `Version` to `trust-boundary/v16` with the
usual comment, and extends `TestClassify…` in
`pkg/trustboundary/trustboundary_test.go` with one case per path. It
is filed `auto_merge: false`, `hold_for_approval: true`, as every list
change is.

## Examined and found sound

What was read in this range and where it held, so the next review can
tell what was looked at.

- **`GET /api/fs/<scheme>/` scoping (#656), apart from finding 2.** Each
  scheme is its own route with its own scope; `fsCallRepo` reads the
  repository out of `ci://<owner>/<name>`, `memory://repos/<owner>/<name>`
  and `task://<id>` (by the task's repository) so a repository-limited
  grant is held to it by the gate, and a read naming no repository is
  narrowed by `fsScope` instead (`AllowedRepos`, `TaskReadRepos`,
  `MayReadAllCI` only on an unlimited grant). `deploy://merge-queue`
  under `ci:read` alone is cut to the caller's repositories
  (`MergeQueueByCI`). `config://` is whole only on an unlimited
  `settings:read`; otherwise `PermitsConfig` serves the public
  skeleton and the task's own repository. A sandbox principal with no
  agent call is refused outright. Redaction, ranges, `?grep`, `?glob`
  and size caps are the same `namespace.Server` as `/fs/`, pinned by
  `TestAPIFsSecretRedactionRangeAndQueryParityWithSandboxProxy`.
- **Bare GitHub Actions run ids under `ci://` (#678).** The lookup runs
  inside `resolveCI` after `PermitsCI(repo)` and additionally requires
  `strings.EqualFold(ext.Repo, repo)` on the forge's answer, so a run id
  cannot be read under another repository's path. `forgeAnswer` strips
  a log redirect's signed URL to its hosts before it reaches an agent.
- **`grain login`, contexts and `GET /api/auth/whoami` (#687).**
  `ownCredentialRoute` lets a `gma_` token past the gate for exactly
  `GET /api/auth/whoami` and `POST /api/external-agents/tokens/{id}/revoke`
  on its own id. The unauthenticated `whoami` answer is the accepted
  auth methods, the version and `externalAgentApi`; a credential's
  details only for the credential presenting it. Self-service minting
  (`SelfService`) is refused to agents, bound to the signed-in person as
  owner, and refuses dangerous scopes, `requires_person` sets and
  repositories off the allowlist -- but the route is `capabilities:admin`
  for agents and a person is the owner anyway, so these are limits on a
  person's own convenience, not a new boundary.
- **Owner-trust (#629).** Approval is given only on an external token's
  own filing (`proposedBy == nil`), never for `approved`,
  `holdForApproval`, `fixesBaseBranch`, `interactive`, `keepRunning`,
  priority above default, a widened `networkAccess`, or a target,
  `reads` or `writes` repository the owner is not trusted for; an
  agent's later edit that moves the task outside those re-proposes it
  (`client.go:5043`). Approval is recorded as `OwnerTrustPrincipal` on
  behalf of the owner, which `baserepairbypass.go` now treats as
  self-approval. The policy is `model.Config.OwnerTrust`, editable over
  `PUT /api/settings` (`settings:write`, dangerous) and `grain sync`; an
  agent can only ever name its own credential's owner on a token it
  mints. The gaps are finding 1 (the adopt route reads owner-trust as
  auto-merge) and finding 3 (the rule file is unclassified).
- **External filing escalation (#629).** The new keys (`hostd`,
  `defaulthostd` through `agentMayRouteHostd`; `keepRunning` refused;
  `priority`, `fixesBaseBranch`, `interactive` behind `tasks:admin`;
  `creator` refused) and the template-carried equivalents close the
  template path the previous days' reviews walked.
- **Pull-request adopt/review/status (#682), apart from finding 1.**
  `adopt` needs `merge:write` on the route and `tasks:write` on the
  repository (`requirePullRequestScopes`); `review` needs `tasks:write`
  and `repos:read`; `status` `ci:read`. A review-only shepherd works on
  no branch at all (`branch = ""`), so the git proxy cannot be asked to
  push the author's head. An unapproved adoption copies nothing and
  opens no replacement PR until a person approves.
- **Person-only rollback (#664).** `RollbackRelease` refuses any actor
  that is not `PrincipalHuman` with an id (`release_approval.go:239`);
  the route scope is `releases:write`; the record is verified by
  `verify-release-approval.sh` in `promote-release.yml` like an approval.
- **Continuous release train (#680, #655, #671, #665).** The train's
  own promotion is now `release-promotion` (hold-policy, person's
  Submit on edits to the script, `release.conf`, the tick wrapper, the
  schedule and `publish-release.yml`); `release-from-trunk.sh` waits on
  `grain/rc-hold` and reads the Stage 2 check run through grain's own
  `checks: read`. Read for the person checks and tag moves, not fuzzed.
- **Service accounts on the allowlist (#652).** A `serviceAccount:`
  entry needs an explicit reason, is never imported from the legacy
  `allowed_users` sources, is refused for any address in the gcp-key
  pool (whose keys are handed to sandboxes), and is written only over
  `POST /api/access/principals` (`access:write`, dangerous) or the
  access-code holder's CLI. The residual risk is a person admitting a
  service account whose tokens a `gcp#…` permission set also hands to
  sandboxes; the refusal message says in so many words that the entry
  gets full operator access.
- **Local-scripts → host-script (#692).** The conversion runs at daemon
  start, before capability servers; each converted script's body
  re-checks at run time that the file is a regular executable owned by
  grain's user and nowhere group/world-writable, `exec`s it with the
  original argv, unsets the withheld ambient variables, and binds each
  secret to a `GRAIN_LOCAL_SCRIPTS_*` server variable. The migrated
  grants are refused to untrusted runs (`localscripts.Untrusted`) in
  `capability_pass.go` and to every CI job in `ci_capability_pass.go`,
  as local-scripts refused them; host-script scripts configured by hand
  keep host-script's own (looser) rule, as before.
- **Container fence (#691).** `fenceEnabled` is fail-closed under
  `auto`: only "nft cannot run at all" leaves the backend unfenced, any
  other nft failure fails the Acquire; a container whose chain cannot
  be installed is removed, and a readopted one is fenced again. An
  unfenced backend refuses external capability grants in
  `mintCapabilityPass` the way an unisolated one does; in-process grants
  are unaffected, as before.
- **Fork-to-upstream pull requests (#658).** The sandbox's `upstream`
  remote is fetched through the git proxy, which is unchanged and holds
  the task to its `reads`; `set-url --push upstream grain-read-only` is
  a convenience, not the control. The upstream is
  `RepoConfig.UpstreamRepo`, set over `PUT /api/repos/{owner}/{name}/settings`
  (`repos:write`); it redirects where grain *opens* and tracks a task's
  pull request and which repository's branch the merge queue lands,
  while the holds and settings read are the fork's. No concrete failure
  mode was reproduced -- opening and merging there still needs the
  deployment's credential to write to the upstream and the usual gates
  -- so it is recorded here for the next review rather than filed.
- **Delegated git login (#512).** `grain git-login` captures a token
  on the workstation (device flow, `gh auth token`, or stdin) and posts
  it to `POST /api/deployment/git-connections` (`connections:write`); the
  `Author` recorded on the credential host is one line, trimmed, and
  only decorates pull-request and comment bodies ("Signed by … (via
  Grain)"), which `PreservePullRequestSignature` keeps across grain's
  rewrites. No authority rests on the text.
- **Task-list cache and gzip (#681).** The cache is keyed by view and
  `?repo=` only; that is safe because `ListTasks` is not filtered per
  caller (every person is the owner, and an agent with a
  repository-limited `tasks:read` is held to `?repo=` by
  `agentCallRepo`, which the key includes). The handler comment's
  "teams they belong to" describes nothing in the code.
- **Local Postgres by default (#679).** The password is 24 random bytes
  from `/dev/urandom` written `0600` under `secrets/`, mounted read-only
  into the container as `POSTGRES_PASSWORD_FILE`; the server runs
  `--network host` with `listen_addresses=127.0.0.1`, which a bridged
  sandbox cannot reach and the fence (#691) would drop regardless; the
  DSN is written to a `0600` env file.
- **Granule import rename (#677), builtin CI jobs skipping the
  capacity gate (#668), `self-depends_on` on repo-less tasks (#670),
  budget flags in `setup.sh` (#663), the MCP wording fixes (#697, #698,
  #451):** read; no authorization or boundary change.
- **The first review's fixes (#672, #673)** land what their findings
  asked for: `readsAll` is held to `agentMayReadAllRepos`, and the v15
  list carries the eight files §2 named.

## Not reviewed

- `ui/` (components, specs), `docs/`, `design/` and the walkthrough
  reports merged this range (the 19 rows marked "—" above): no
  trust-boundary code.
- `scripts/release-from-trunk.sh`, `scripts/release-train-task.sh` and
  `scripts/verify-release-approval.sh` were read for their person
  checks and tag moves, not fuzzed.
- `third_party/granule` beyond confirming the diff is the import
  rename alone.
- `scripts/iap-e2e/run.sh` (#652's additions): a harness run against a
  deployment, not executed.

## Package suites

Result of the touched-package run named under "Tests run"
(`go test ./pkg/ui/ ./pkg/model/... ./pkg/orchestrator/
./pkg/capability/... ./pkg/namespace/... ./pkg/gitproxy/ ./pkg/ci/...
./pkg/github/... ./pkg/releaseapproval/ ./pkg/grit/... ./pkg/mcp/
./pkg/daemon/ ./pkg/cli/`):

**All pass** -- 49 packages `ok`, none failed (`pkg/daemon` 214s,
`pkg/cli` 162s, `pkg/orchestrator` 133s, `pkg/ui` 95s, `pkg/gitproxy`
66s, `pkg/mcp` 34s, `pkg/model/sqlite` 23s, `pkg/ci` 23s, the rest under
13s). `go test ./tests/hygiene/ -run TestCoreSaysNoGoogleTokens` also
passes with this report in the tree. No failure to report, security or
environmental.
