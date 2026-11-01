# Security fixes live confirmation: the 2026-10-30/31 reviews, and the 2026-10-28/29 ones

- **Date:** 2026-11-01
- **Plan:** `docs/test-plans/2026-11-01.md` §11. This run also covers the previous plan's still-unreported §9 (`docs/test-plans/2026-10-30.md` §9).
- **Task:** 2226, filed by the nightly test-plan task 2084.
- **Base:** `main` at `da97fdb0`. Every commit under test is on it:
  - `c2e4b7d5`, `50a3c160`, `a7cb221b`, `42d25d37`, `06c8e26e`, `015061c8`, `4eaa3b7d`, `6b80075c`;
  - from §9: `62ce8160`, `0953a905`, `af8a6180`.
- **Review docs:** `docs/security/audits/2026-10-30.md` (PR #256) and `2026-10-31.md` (PR #277). Their findings were each attacked live:
  - 10-30 F1 → A1;
  - 10-30 F2 → A5;
  - 10-31 F1 → A2.

## Verdict

**One security bug found and fixed on this branch.** It needs a security review: see [Finding F1](#finding-f1-agents-could-widen-networkaccess-past-an-inherited-none-fixed-here). Every fix under test held against the attacks below. One low-severity observation was filed, held for approval ([O2](#observations)).

| # | Item | Result |
|---|---|---|
| A1 | `task://` siblings (`c2e4b7d5`) | **Held.** |
| A2 | Fork / `pull_request` workflow runs (`50a3c160`) | **Held.** |
| A3 | Form hidden defaults on agent filings (`a7cb221b`) | **Held.** |
| A4 | Trailing JSON, array and empty PATCH bodies (`42d25d37`) | **Held.** |
| A5 | Suite / qualification tables under the ceiling (`06c8e26e`) | **Held.** |
| A6 | Task and session grant edits (`015061c8`, `4eaa3b7d`) | **Held**, except network access: **F1, fixed here.** |
| A7 | Narrowed `writes`/`reads`, then the warm sandbox's old token (`6b80075c`, `4eaa3b7d`) | **Held.** |
| §9.1 | Git signing only for a sandbox that may push (`0953a905`) | **Refusal held.** The positive signature could not be produced here (no ambient credential with an SSH key). |
| §9.2 | Event-gated CI capabilities withheld from agents (`62ce8160`) | **Held**, including across a daemon restart. |
| §9.3 | Repo-scoped `gma_` on `GET /api/tasks` (`af8a6180`) | **Held**, ETags included. |

## Environment

- **Machine:** Debian sandbox, 7 vCPU, 15 GiB. Go 1.26.10, git, Docker (bridge `docker0` at 10.240.0.1). `sqlite3` was installed with apt for reading stores.
- **grain:** built from this branch (`go build ./cmd/grain`). It was rebuilt after the F1 fix to confirm the fix live.
- **`grain demo`** with `-github-sim -git-proxy -fake-ci -secrets-store -access-code-file=…`:
  - a real UI/API server on its real SQLite store;
  - the real git proxy (`gitproxy.BuildProxy`, the real `SandboxTokenStore` and `ModelAuthorizer`) in front of githubsim's `GitUpstream` and `git http-backend`.
  
  Its `POST /api/e2e/git-proxy/runs` records a run with `StartRun` and mints that sandbox's real proxy token, as `runOne` does. That token is what an agent in a sandbox holds. With it, every request below went through the proxy's `/fs/`, `/cli/` and `/api/` routes and through real `git`. Used for A1, A3, A4, A6, A7 (narrowing), §9.1 and §9.3.
- **Two `grain daemon` instances** (`-container-sandboxes -container-git-proxy-host=10.240.0.1 -sandbox-port-base=0 -github-host=<sim> -github-insecure-http`), each with the default internal grain/config state repo:
  - **d1** added `-github-webhook-addr=127.0.0.1:4244` and `GITHUB_WEBHOOK_SECRET`. Used for A2.
  - **d2** had a PAT git connection to the demo's simulated forge and Grain CI on for `acme/widgets`. Used for §9.2, restarted mid-test.
- **Not available: a real agent in a real sandbox.** The sandbox has no model credential. The daemon logged "no configured agent backend is free", so it dispatched nothing into a Docker container. Where an item needs the orchestrator's own cycle (A5's finish path, A7's warm-sandbox reclaim), it was driven in-process:
  - real store, real githubsim with real git pushes;
  - real `orchestrator.ProcessResult`, `SyncSecurityHolds` and `RunCycle`;
  - real `gitproxy.SandboxTokenStore`.
  
  Only the agent's turn was scripted. These checks are kept as tests (listed under each item).

Tokens used:
- **Sandbox tokens** from the demo's run-minting route: task 20, "attacker", with no capability, then single `api:` grants as noted; task 28, a keep-running session; task 27, `noRepo`.
- **External agent credentials** (`gma_`) from `POST /api/external-agents/tokens`, with `externalAgentApi` on:
  - `reader-widgets`: `tasks:read@acme/widgets`;
  - `reader-gadgets`: `tasks:read@acme/gadgets`;
  - `admin-only`: `tasks:admin@acme/widgets`;
  - `writer-widgets`: `tasks:write@acme/widgets`;
  - `ci-writer2`: `ci:write,ci:read@acme/widgets`.
- **The access code**, for the person-side controls.

## A1. A capability-less sandbox reading a sibling's `task://`

Task 20 has no capability and no grant. Task 21 is a sibling in the same repository, with description `SECRET-SIBLING-PROMPT-7731`.

| Request (task 20's token) | Response | Effect |
|---|---|---|
| `GET /fs/task/20/record.md` (own) | 200 | own record |
| `GET /fs/task/21/{record,prompt,conversation,transcript,summary}.md`, `/fs/task/21/attempts?list`, `/fs/task/1/record.md` | 403 "another task's record needs the "self-debug" capability or an "api:tasks:read@<owner/repo>" grant…" | nothing leaked |
| `GET /fs/task?list` | 200 `20/` | only its own task listed |
| `/fs/task/20/../21/record.md`, `20%2F..%2F21` | 400 path traversal | — |
| `/fs/task/%32%31/…`, `/fs/task/%2321/…`, `/fs/task/21`, `/fs/task/21/` | 403 | — |
| `grain read task://21/record.md`, `grain task read 21` (sandbox CLI) | forbidden | — |
| `grain grep SECRET-SIBLING task://`, `grain ls task://`, `grain task list` | no match / `20/` / only task 20 | — |
| `GET /api/tasks/21`, `/prompt`, `/attempts/1/transcript`, `/api/tasks?repo=acme/widgets` (via proxy `/api/`) | 403 "holds no api grant for scope tasks:read" | — |
| Control: grant `api:tasks:read@acme/gadgets` | sibling still 403 ("holds tasks:read, but not on acme/widgets") | — |
| Control: grant `api:tasks:read@acme/widgets` | `/fs/task/21/record.md` 200, `/api/tasks/21` 200 | intended |

## A2. A fork-run `workflow_run` webhook with head branch `main`

Each webhook was a signed `workflow_run` `completed` delivery (`X-Hub-Signature-256` over `whsec`) to d1's listener for `acme/widgets`:
- workflow "release images", `.github/workflows/release.yml`;
- `head_branch: main`, conclusion `failure`.

All six were accepted (202). The effect was read from the daemon's log and `github_ci_job`.

| run id | `event` | `head_repository` | Effect |
|---|---|---|---|
| 9001 | `pull_request` | `contrib/widgets` | log "ignoring concluded GitHub workflow run 9001 … from head repository contrib/widgets"; **no row** |
| 9002 | `pull_request` | `acme/widgets` | no row |
| 9003 | `push` | `contrib/widgets` | no row |
| 9004 | `pull_request_target` | omitted | no row |
| 9005 (control) | `push` | `acme/widgets` | `github_ci_job` row `acme/widgets|9005|main|failure|webhook` |
| 9006 (control) | `schedule` | omitted | row for 9006 |

So a fork's run, or any `pull_request*` run, is never read as the base branch's:
- no job record, tail or base-branch release;
- no `github_workflow_run` schedule firing (those steps all sit behind the same two checks in `handleConcludedGitHubWorkflowRun`).

`base_branch_release` stayed empty even for the controls. `isGitHubBaseBranch` needs a REST client, which the git-only sim cannot answer, so the release-triage half was not seen live. `TestGitHub*WorkflowRun*` in `pkg/orchestrator` covers it.

## A3. An agent filing through a form that hides autoMerge or approved

`grain_config.new_task_form` was set to a default layer hiding:
- `autoMerge: true`, `approved: true`, `atFront: true`;
- `grants: [api:tasks:read@acme/widgets]`.

That is what a merged `settings/new-task-form.yml` would store; in the demo it was written into the store directly. A first version that also hid `writes`/`reads` was refused for the person too, because those repos were not filable here.

| Filing | Result |
|---|---|
| Person, body `{title, repo}` (control) | 201, task 24: `approved: true`, `autoMerge: true`, `capabilities: [api:tasks:read@acme/widgets]`. The hidden defaults apply. |
| Agent (task 20 holding `api:tasks:write@acme/widgets`, through the proxy), same body | 201, task 25: `state: proposed`, unapproved, `autoMerge: false`, none of the hidden grants. |
| Agent asking `approved:true, autoMerge:true` itself | 403 "an agent files work unapproved: approving it needs tasks:admin" |
| Agent with `boardId`/`templateId` to pick another layer | 404 "no template y" |

## A4. Concatenated-JSON, array and empty `PATCH` bodies

The target was `PATCH /api/tasks/21`. After every request the task was re-read: title, priority, autoMerge and capabilities. Nothing changed except where marked.

**With the `tasks:admin`-only external credential** (the priority-only path `42d25d37` hardened):

| Body | Response |
|---|---|
| `{"priority":3}{"title":"pwned","autoMerge":true}` | 403 "requires a single JSON object editing priority" |
| `{"priority":3} {"capabilities":["self-debug"]}` | 403 (same) |
| `[{"priority":3},{"autoMerge":true}]`, `[]`, `null` | 403 (same) |
| empty body; no body | 403 "requires a JSON object editing priority" |
| `{"priority":true}` | 403 "may only edit numeric priority" |
| `{"priority":"7"}` | 400 (type) |
| `{"priority":3,"Title":"pwned"}`, `{"PRIORITY":4,"autoMerge":true}`, `{"priority":3,"priority":"x","autoMerge":true}` | 403 "may only edit priority (got …)" |
| `{"priority":3}`, `{"Priority":2}` (controls) | 200; priority 3, then 2 |
| `{}` | 200, no change (see O2) |
| `{"priority":1}` on the gadgets task | 403 "holds tasks:admin, but not on acme/gadgets" |

**On the sandbox channel:**
- `tasks:admin` is stripped there, so every body above was refused 403 "holds no api grant for scope tasks:write".
- With `api:tasks:write`:
  - `{"priority":4}{"capabilities":["self-debug","api:tasks:admin"]}` → 400 "unexpected trailing JSON data after request body";
  - `{"autoMerge":true}` → 403.

**Filings through the sandbox**, with trailing objects or an array (`{…}{"approved":true,"autoMerge":true}`, `[{…}]`):
- refused 403 before the handler;
- `autoMerge`/`AutoMerge`, `writes` to a repo the task cannot push, and a repo outside the grant: each 403.

## A5. A grain/config PR raising a suite above the ceiling

This drove the real finish path (`ProcessResult` → security hold → `SyncSecurityHolds`, with a clean review) against githubsim's grain/config with real git pushes. The base branch's `settings/config-policy.yml` was set, in turn, to four operator mistakes:
1. listing all six tables plus `settings/suites.yml`;
2. `tables/**` and `settings/**`;
3. `tables/suite*.json` and `tables/qualification_*.json`;
4. only `tables/custom_field.json`.

Under each, the PR head changed one of:
- `tables/suite.json` (`require_approval: 0`, `auto_merge: 1`, `auto_approve_proposals: 1`);
- `tables/suite_item.json`, `tables/suite_sequence.json`;
- `tables/qualification_config.json` (`require_approval: 0`, `auto_promote: 1`);
- `tables/qualification_item.json`, `tables/qualification_item_depends_on.json`;
- `settings/suites.yml` (`require_approval: false`, `auto_merge: true`).

**Result:** all 28 changes were held: `autoMerge=false`, human-only hold `trust-boundary/v9`, security review filed. **Control:** `tables/custom_field.json` under policy 4 auto-merged (`autoMerge=true`, no hold), so the hold above is the ceiling at work rather than a policy that does nothing.

`settings/suites.yml` is the only file form of suites (`declFileName`). The qualification tables have no `settings/` form. `repos.yml` is only used to recognise a dump, never applied. So nothing applies these tables by another path.

**Kept as a test:** the six tables joined `TestStateRepoNeverAutoMergePathsCeiling` (`pkg/orchestrator/securityhold_state_test.go`), which did not name them.

## A6. Task and session grant edits by an agent

The target was `PATCH /api/tasks/21`. Each body was sent twice:
- with task 20's sandbox token (`api:tasks:write@acme/widgets`);
- with the `writer-widgets` external credential, with an `X-Grain-Reason`.

| Body | Sandbox | External | Effect |
|---|---|---|---|
| `{"capabilities":["self-debug"]}`, `{"capabilities":[]}`, `{"grants":["api:tasks:admin"]}` | 403 "attaching capabilities … is not permitted from the sandbox CLI" | 403 "needs capabilities:admin" | none |
| `{"keepRunning":true}`, `{"keep_running":true}`, `{"keepRunning":false}` | 403 | 403 | none |
| `{"autoApproveProposals":true}` | 403 | 403 | none |
| `{"reads":["acme/gadgets"]}`, `{"writes":[{"repo":"acme/gadgets"}]}` | 403 | 403 | none |
| `{"title":"benign title edit"}` (control) | 200 | 200 | edited |
| `{"networkAccess":"open"}` | **200** | **200** | **sibling set to `open`. See F1.** |

On the deployment default `open`, `"open"` is no wider than the agent's own network, so those two 200s were correct. The deployment default was then set to `none` (`PUT /api/settings {"networkAccess":"none"}`), with tasks 20 and 21 left at `network_access ''`. The same PATCH from task 20's sandbox still returned 200, and task 21 (approved, queued) read back `'open'`. Filing `{"networkAccess":"open"}` returned 201, task 29 `'open'`. This is F1.

**After the fix** (rebuilt binary, fresh demo, deployment default `none`):

| Request | Response |
|---|---|
| Sandbox `PATCH {"networkAccess":"open"}` | 403 "an agent with networkAccess "none" cannot widen networkAccess"; victim unchanged |
| Sandbox filing with `networkAccess: "open"` | 403 (same) |
| External `PATCH` open | 403 "an external agent cannot widen networkAccess past this deployment's default of "none""; victim unchanged |
| External `PATCH {"networkAccess":"none"}` (control) | 200 |
| Deployment default back to `open`, sandbox `PATCH` open (control) | 200 |

## A7. A session whose `writes` were narrowed, then its warm sandbox's old token reused

**Live, through the demo's proxy.** Task 28 is keep-running, with `reads: [acme/gadgets]` and `writes: [{repo: acme/gadgets}]`, and a minted token.

| | `git ls-remote …/acme/gadgets.git` | `git push … acme/gadgets` | `acme/widgets` (own target) |
|---|---|---|---|
| Before narrowing | forwarded: "repository not found" (the sim has no gadgets repo) | — | `ls-remote` lists refs |
| After a person PATCHes `reads: [], writes: []`, same token, nothing restarted | **403** | **403** "acme/gadgets is not in scope for this sandbox" (identical to a never-granted `acme/never`) | push to `grain/task-28` succeeds |

The proxy reads scope from the live store, so narrowing takes effect on the very next request, before any reconcile.

**Warm-sandbox reclaim.** This used `RunCycle` with the daemon's own `MintSandboxToken: tokens.EnsureToken` and `RevokeSandboxToken: tokens.Revoke` on a real `gitproxy.SandboxTokenStore`, read back with `gitproxy.LoadSandboxTokens(...).Authenticate`, as the proxy does.
- After cycle 1 the sandbox was parked warm (`kr-…-1`) and its token authenticated.
- Narrowing `writes`, and separately `reads`, then cycle 2 gave:
  - the old token **no longer authenticates**;
  - the warm sandbox was replaced (`kr-…-2`).

**Kept as a test:** `TestWarmSessionSandboxTokenStopsAuthenticatingOnceRepoAccessNarrows` (`pkg/orchestrator/warm_sandbox_token_narrowing_test.go`). It passes under `-race -count=2`.

## Previous plan §9 (2026-10-30), covered

### §9.1 Git signing only for a sandbox that may push

The payload was a well-formed commit object, sent with `POST /_grain/git-sign` through the demo's proxy.

| Caller | Response |
|---|---|
| No token | 401 "authentication required" |
| `noRepo` task 27's token | 403 "this sandbox has no repository it may push to, so it may not sign commits" |
| Writing task 20's token | 403 "credential "default" is not an ambient git credential" |

The refusal side holds. Writing task 20 also never falls back to a non-ambient credential, which is the second half of `0953a905`.

**Not done:** the positive check, a signature from a writing sandbox that verifies against the host owner's SSH key. It needs an ambient git credential and a host SSH signing key, and neither exists in a task sandbox. `pkg/gitproxy` `-run Sign` passes and covers it with a fixture key.

### §9.2 Event-gated CI capabilities on `workflow_dispatch`

`.grain/workflows/gated.yml` (`on: workflow_dispatch`, one job with `capabilities: [package-publish, "repo-write:acme/widgets"]`) was pushed to `grain/task-21` of the sim repo through the proxy. It was started on d2 with `POST /api/ci/runs` (`workflow` is the file's path; `ref` the branch).

| Caller | Run | Job verdict |
|---|---|---|
| Person (access code) | `b16c3238…` | errored only on "package-publish … this deployment does not provide it"; `repo-write:acme/widgets` not reported unmet |
| In-grain agent (acting task 2, `api:ci:write@acme/widgets`) | `f91f66b7…` | errored: both capabilities refused, "a workflow_dispatch run … started by a caller that is not a person places no capabilities or release secrets" |
| External `ci-writer2` | `fa8cf1be…` | same refusal |
| In-grain agent, `event` omitted | `a8cae076…` | same refusal |

The person's plan key differs from the agents', which share one (`run:9db242c9…`): the withholding is part of the persisted plan.

After a daemon restart:
- both runs read back unchanged;
- the agent's replayed dispatch reused the persisted plan `run:9db242c9…` and was still refused both capabilities.

No job reached a sandbox (each stopped at its requirements). `TestEventGatedCapabilitiesAreWithheldFromNonHumanDispatches` passes.

### §9.3 Repo-scoped `gma_` credentials on `GET /api/tasks`

Setup: target repos `acme/widgets` and `acme/gadgets`, and a gadgets task with description `GADGETS-ONLY-9921`. The two readers alternated twice.

| Caller | `/api/tasks` | `?repo=acme/widgets` | `?repo=acme/gadgets` | `?repo=ACME/Gadgets` |
|---|---|---|---|---|
| `reader-widgets` | 200, 19 tasks, only widgets, no gadgets marker; ETag `06900d91…` | 200, same ETag | 403 | 403 |
| `reader-gadgets` | 200, 1 task, gadgets; ETag `3a6c26f5…` | 403 | 200 | 200 |

ETag replay and query tricks:
- `reader-gadgets` with `If-None-Match` of `reader-widgets`'s ETag: **200** with its own one task (not 304).
- The reverse, and `reader-widgets` with the person's ETag: 200 with their own lists.
- A caller's own ETag: 304.
- `?repo=widgets&repo=gadgets`: 200, widgets only. Reversed order: 403. `?repo=` empty and `?Repo=acme/gadgets`: own repo only.
- `?repo=a,b` and `?repo=*`: 400.
- `?search=GADGETS-ONLY`: 0 results.
- `/api/tasks/<gadgets id>` and `/prompt`: 403.

## Finding F1: agents could widen networkAccess past an inherited none (fixed here)

**Flag for security review: trust-boundary edit in `pkg/ui/agent_api_gate.go`.**

- **Severity:** Medium. It applies only on a deployment that chose `networkAccess: none` as its default. There, an agent holding `tasks:write` hands work the network the deployment withholds.
- **What:**
  - `4eaa3b7d` added the in-grain check in `agentFilingEscalation`, but it compared only the acting task's own `NetworkAccess` field with `"none"`. A task with no override (`""`, the usual case) takes the deployment default (`orchestrator.deploymentNetworkAccess`). So on a deployment whose default is none, the agent could PATCH an approved, queued sibling to `open`, or file work with `open`. The sibling would dispatch with an open network.
  - `externalAgentFilingEscalation` had no `networkAccess` case at all, although `4eaa3b7d`'s message says it did.
- **Reproduction** (deterministic):
  1. Set the deployment's `networkAccess` to `none`.
  2. Give an acting task `api:tasks:write@owner/repo` and no `networkAccess` of its own.
  3. `PATCH /api/tasks/<approved sibling>` with `{"networkAccess":"open"}`, through the run's API tools or with a `tasks:write` `gma_` credential. It answers 200 and the sibling stores `open`.
- **Fix:** both gates call a new `networkAccessWidens`. It reads the caller's limit and the requested value against the deployment default the way dispatch does, and treats an unreadable config as none. An external agent's limit is the deployment default. Unchanged: narrowing, asking for the default, and `open` on an open deployment.
- **Regression test:** `tests/security/network_access_widening_test.go` (`TestAgentCannotWidenNetworkAccessPastAnInheritedNone`). It runs over a real `ui.Server` and store, through the acting-task header and a minted `gma_` credential, with the controls above. It fails without the fix (5 failures) and passes with it. Confirmed live after the fix (A6).

## Observations

- **O1. Deployment default capabilities on agent filings.** An agent's filing still takes the deployment's default capabilities. In this demo that was `gcp-key, gemini-key, self-debug`, so task 25 was filed with them although the agent asked for none. That is the deployment's own `default_capabilities`, and the task is filed **proposed and unapproved**: a person sees the capabilities before it can run. It is not a form hidden default, and `a7cb221b` did not intend to change it. Not filed.
- **O2. A `tasks:write`-only (or `tasks:admin`-only) credential can read a task through `PATCH`.** `GET /api/tasks/{id}` refuses such a caller, but `PATCH` answers with the full record, prompt included, and a no-op `{}` changes nothing. Seen with `write-only` on task 22: GET 403, PATCH `{}` 200 with `PRIVATE-PROMPT-4410`. Low severity: it only reaches a repository the credential may already write. Filed, held for approval: "Don't return the task record to an agent holding tasks:write but not tasks:read".

## Tests run

- Whole packages: `pkg/ui` and `tests/security` with the fix.
  - `pkg/ui`'s `TestSettingsLockedRefusesExactlyTheFieldsTheUIGreysOut` fails on the base tree as well; it is not this branch's.
- Targeted runs:
  - `pkg/gitproxy -run Sign`;
  - `pkg/daemon -run 'EventGated|SandboxProxyServesTheStage2Roots|TaskReads'`;
  - `pkg/namespace`, `pkg/trustboundary`, `pkg/staterepo`, `pkg/ghwebhook`;
  - `pkg/orchestrator -run 'GitHub.*WorkflowRun|Fork|KeepRunning|SessionSandbox|StateRepo|WarmSession'`.

## CI repairs on this branch

The merge queue sent the branch back for its two red checks. Both failures were unrelated to the security changes, and both are fixed here:

- **`go` (`pkg/ui`), red on `main` too.** `TestSettingsLockedRefusesExactlyTheFieldsTheUIGreysOut` sent `{"gcpProject":"proj"}`, expecting the lock's 409. Since `374a0736`, `"proj"` is not a valid project ID, and the validation error answers first. That ordering is deliberate (`checkGrainConfigWritable` leaves an invalid request to its validation error). The test now sends a valid ID (`my-project-123`), so it still proves the lock refuses a writable change. `main` made the same correction independently in `400fe306`; the merge keeps `main`'s version.
- **`go unit` (`pkg/tunnel/hostport`).** `TestForwarderForwardsAndRepoints` picked a free port and closed its listener before the forwarder bound it. The test's own echo servers, started afterwards on `127.0.0.1:0`, could be handed that very port (CI's "bind: address already in use"). The echo servers now listen before the port is picked, and the first bind retries on a fresh port if another process took it. It passes 20× under `-race`.
- **`go unit` again (`pkg/granule`), after the merge with `main`.** `TestServeForwardRoundTrip` had the same probe-then-bind race. Its guest port was probed free and closed, and the guest bound it later. When another listener got there first, the guest returned `EADDRINUSE`, but the test never read it, so it showed only as "timed out waiting for forward ready" after 5s. The test now watches the guest's error, retries on a fresh port when the bind is refused, and allows 30s for readiness on a loaded `-race` runner. A squatted port is now reported at once as `EADDRINUSE`, and the test passes 30× under `-race`.
