# Unified grants, GrantsPicker and API scope sets enforcement

- **Plan:** `docs/test-plans/2026-10-28.md` §3 (retired `bwsalmon/grain`), restated in [Task #1716](/tasks/1716)
- **Date:** 2026-10-29, 08:20–09:40 UTC
- **Task:** `grain/049f1e09/task-1716` on `ai-on-gke/grain`, base `main` at `425375a` (origin: [Task #1549](/tasks/1549))
- **Environment:**
  - Debian sandbox (`kubernetes-granule`), `linux/amd64`, Go `go1.26.10`, Node 20.19.0
  - `grain` built from this branch with its UI (`make frontend grain`, `bin/grain`, `v3.1.0-dev.216+16e070e`)
  - `grain demo -github-sim -fake-ci -addr 127.0.0.1:4290`; API calls with `curl` and the demo's access code as bearer token
  - An agent is the in-daemon identity `X-Grain-Acting-Task: <id>` (plus `X-Grain-Channel: sandbox-cli` for the sandbox proxy channel, `X-Grain-Acting-Reason` for destructive routes), which is how `pkg/ui/agent_api_gate.go` reads a task's run
  - Playwright 1.x Chromium from `/var/cache/playwright`, against the same demo (ad-hoc driver) and through `npx playwright test` (the suite's own demo per worker)

## Outcome

| Step | Result |
|---|---|
| 1. Declare `walk-readers` (task read + CI read on one repo), grant it, read with that task's identity | **Pass.** Allowed reads succeed; other-repo reads, unscoped lists and writes are refused with reasons naming the scope and repository. The repo limit holds on the sandbox channel too. |
| 2. Agent declares/grants a set with `tasks:admin` / `grain/task-admin` | **Pass after fix (F1).** Every escalation is refused with a clear reason. F1: an agent could not pass on a repo-limited set it holds. |
| 3. Default, repo, CI and session grants via GrantsPicker; inheritance; chips; auto-save | **Pass** (one observation, O2). |
| 4. Settings → Capabilities: server list, Legacy, scope-set create/edit/delete, polling | **Pass.** New regression spec `ui/e2e/api-scope-set-edit.spec.js` covers create → edit → reload → remove, which no spec covered before. Observation O3. |
| 5. Capability server narration; data-plane token survives a restart | **Not run live:** `grain demo` never runs agents, and its store is thrown away on restart. Covered by the unit tests listed below, which pass on this branch. |

## Steps and observations

### 1. `walk-readers` over the API

```
PUT /api/api-scope-sets {"set":{"name":"walk-readers","scopes":["tasks:read","ci:read"],"repos":["acme/widgets"]}}
  → 200; listed as source "config", resolved [ci:read, tasks:read], repoLimits both → acme/widgets
POST /api/tasks/2/capabilities {"id":"api:walk-readers","attach":true}   (person)
```

The plan says `POST /api/api-scope-sets`; the route is `PUT /api/api-scope-sets`, as `docs/agent-api.md` says. A
second acme/gadgets task (19) was filed for the cross-repo probes. As task 2 (`X-Grain-Acting-Task: 2`):

| Call | Result |
|---|---|
| `GET /api/tasks/1` (acme/widgets) | 200 |
| `GET /api/tasks?repo=acme/widgets` | 200 |
| `GET /api/ci/runs?repo=acme/widgets` (also on `sandbox-cli`) | 200 |
| `GET /api/tasks/19` (acme/gadgets) | 403 `task 2 holds tasks:read, but not on acme/gadgets` |
| `GET /api/ci/runs?repo=acme/gadgets` (also on `sandbox-cli`) | 403 `task 2 holds ci:read, but not on acme/gadgets` |
| `GET /api/ci/runs`, `GET /api/tasks` (no repo) | 403 `… holds ci:read only for some repositories, and GET /api/ci/runs does not name one it reaches` |
| `PATCH /api/tasks/1`, `POST /api/tasks` | 403 `task 2 holds no api grant for scope tasks:write …` |
| `GET /api/api-scope-sets` | 403 `… no api grant for scope capabilities:read …` |

### 2. Escalation through scope sets

A person then gave task 2 `api:capabilities:admin` and `api:tasks:write`, so it could reach the write routes. As task 2:

| Call | Result |
|---|---|
| any write, no `X-Grain-Acting-Reason` | 403 `PUT /api/api-scope-sets is destructive (scope capabilities:admin): give a reason` |
| `PUT` set `{scopes:[tasks:admin]}` | 403 `api:tasks:admin is a dangerous api grant: only a person may grant it` |
| `PUT` set `{scopes:[capabilities:admin]}` | 403 `api:capabilities:admin is a dangerous api grant: only a person may grant it` |
| `PUT` set `{includes:[grain/task-admin]}` | 403 `an agent cannot grant api:ci:read, which its own task does not hold` (it holds ci:read only on acme/widgets) |
| `PUT` set `{scopes:[tasks:write]}`, then `POST /api/api-scope-sets/delete` | 200, 200 (it holds tasks:write everywhere) |
| grant `api:task-admin`, `api:grain/task-admin`, `api:ops/triage` to task 1 | 403 `an agent cannot grant api:<name>, which its own task does not hold` |
| grant `api:tasks:admin` to task 1 | 403 `… dangerous api grant: only a person may grant it` |
| grant `api:walk-readers` to task 1 | **403 `an agent cannot grant api:walk-readers, which its own task does not hold`** — F1 |

### 3. Grants via the UI and inheritance

- `PUT /api/settings {"defaultCapabilities":[…,"api:grain/ci-read"]}` and
  `PUT /api/repos/acme/widgets/capabilities {"defaultCapabilities":["api:walk-readers"]}`: a new acme/widgets task got
  `api:grain/ci-read, api:walk-readers, gcp-key, gemini-key, self-debug`; a new acme/gadgets task got everything but
  `api:walk-readers`. The repo page's GET reports the deployment and repo layers and the effective union.
- The GrantsPicker paths (Settings default grant with arguments, New task inheriting it, API grant picker, grant chips
  with hover, the one-stack layout, a console session's pending grant) were run through the suite's own specs against
  this build, desktop and 390×844: `default-grants`, `api-grant-picker`, `grant-chips`, `grants-picker-layout`,
  `session-pending-grants` — 11 passed.
- Rendered `/settings/tasks`, `/repos/acme/widgets`, `/settings/capabilities`, `/settings/capabilities#api-scope-sets`
  and `/tasks/2` at 1280×900 and 390×844 in light and dark: no horizontal overflow on any (`scrollWidth − innerWidth = 0`),
  no page errors; task 2's row shows its grant chip.

### 4. Settings → Capabilities

- The page shows **Capability servers** (`+ Add server`, built-ins `config-map`, `gchat`, `gcp`, `host-script`, … each
  "needs configuring" in the demo), a **Legacy capabilities** control, **Capability policy** and **API scope sets**.
- **Polling.** Request timings over 45 s from load: `/api/capability-servers` at 0.4, 2.4, 4.4, 6.4, 8.4, 10.5 s and
  then never again (the demo's built-ins report no status, so the panel reads again `unreportedReadLimit` = 5 times and
  stops); `/api/capability-servers/host-entries` at 0.4 and 30.4 s (its deliberate 30 s refresh); `/api/api-scope-sets`
  once. The remaining 3 s polls (`/api/tasks`, `/api/config`, `/api/ci/runs`, `/api/console-sessions`,
  `/api/restart-watch`) belong to the app shell on every page.
- **Scope sets from the card.** Create `walk/readers` (tasks:read, ci:read @ acme/widgets) → row shows
  `ci:read @ acme/widgets`, `tasks:read @ acme/widgets`; Edit (description, drop ci:read) → saved, survives a reload;
  Remove → gone, and stays gone after a reload. Now `ui/e2e/api-scope-set-edit.spec.js`.

### 5. Capability servers

Not runnable in `grain demo` (no agent runs; a restart starts a new throwaway store). Covered on this branch by:
`TestTheRegistryManagesDataPlaneTokens` (a generated data-plane token is kept across a restart — a new registry over the
same secrets store — and replaced by rotate), `TestWireEndpointsNarratesEachToolCallUnderItsServersName` and
`TestProxyTellsItsObserverOfEachToolCallBeforeForwardingIt` (narration). All pass.

## Findings

### F1 — an agent could not pass on a repo-limited scope set it holds (fixed)

**Reproduction:** declare `walk-readers` with `repos: [acme/widgets]`; give task 2 `api:walk-readers` and
`api:capabilities:admin`; as task 2, `POST /api/tasks/1/capabilities {"id":"api:walk-readers","attach":true}` with a
reason → 403 `an agent cannot grant api:walk-readers, which its own task does not hold`.

**Cause:** `agentGrantRefusal` (`pkg/ui/agent_api_gate.go`) checked each scope of the grant with
`held.Allows(scope, g.Repo)`. `g.Repo` is the grant's own `@repo`, empty for a set granted by name, so it asked whether
the agent holds the scope on *every* repository — which a repo-limited holder never does — although the set itself
grants it only on acme/widgets.

**Fix:** read each scope's repositories through the set, `APIGrant.ReposOf` (the existing `reposOf`, exported — the
rule `apiGrantsOf` already applies when the grant is used). An agent holding the set, or the same scopes on the set's
repositories, may now pass it on; one holding them only elsewhere still may not. Regression test
`TestAgentMayPassOnARepoLimitedScopeSetItHolds`, which fails without the fix. Verified live after the fix: granting
`api:walk-readers` → 200; `api:grain/ci-read` (unlimited ci:read) → still 403.

## Observations (no change made)

- **O1.** After the fix an agent may also grant `api:walk-readers@acme/gadgets`, a repository outside the set's limit.
  It grants nothing (`ReposOf` is empty, so `apiGrantsOf` adds no scope), so it is not an escalation, but it is
  accepted silently. Refusing an `@repo` outside a set's own limit when the grant is recorded would be clearer.
- **O2.** `PUT /api/repos/{repo}/ci-capabilities` refuses any `api:` grant (`capability "api:…" is not supported for
  CI`). Repo-narrowed `api:` grants for CI belong to a workflow job's own `capabilities:` in `.grain/workflows`
  (30468301c); the repo-wide CI list is for providers that deliver material. Working as designed, noted because the
  plan's "CI grant" could be read either way.
- **O3.** A deployment scope set's **Remove** button deletes it at once, with no confirmation and no word on how many
  tasks hold it; every grant of it then grants nothing. Filed as a follow-up rather than changed here.

## Fixes in this branch

- `fix(api): let an agent pass on a repo-limited scope set it holds` — F1 and its Go regression test.
- `test(ui): cover creating, editing and removing a scope set from the card` — `ui/e2e/api-scope-set-edit.spec.js` and
  this report.
