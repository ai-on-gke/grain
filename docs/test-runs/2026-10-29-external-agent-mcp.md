# External agents over MCP/HTTP (`POST /mcp`, `gma_` credentials) walkthrough — 2026-10-29

- **Plan:** `docs/test-plans/2026-10-29.md` §5 (PR #104: `external_agent_api`,
  `POST /mcp`, `/api/external-agents/tokens`, scope and repository holds, limits,
  attribution).
- **Date:** 2026-10-29, 14:47–15:30 UTC
- **Task:** 1799 (`grain/049f1e09/task-1799`), filed by task 1753, base `main`
- **Commit tested:** `2f53e63a` (`main` at the start of the run, "Merge pull
  request #130"). The fixes below were then re-verified live against a demo
  rebuilt from this branch.
- **Environment:**
  - Debian sandbox (`kubernetes-granule` backend), Go (`/usr/local/bin/go`),
    Node.js `v20.19.0`.
  - Frontend `cd ui && npm ci && npm run build`; server
    `go build -o /home/debian/bin/grain ./cmd/grain`.
  - `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:18080
    -open=false -access-code-file /var/tmp/ext/code` on its own throwaway store.
    The plan's `-sandbox-port-base=0` is **not a `grain demo` flag** (the demo
    exits with `flag provided but not defined: -sandbox-port-base`); it belongs
    to `grain daemon`, and was dropped.
  - MCP client: `npx -y @modelcontextprotocol/inspector@0.17.5 --cli
    http://127.0.0.1:18080/mcp --transport http --header "Authorization: Bearer
    gma_..." --method tools/list|tools/call ...`. The current
    `@modelcontextprotocol/inspector@latest` (2.13.0) fails to install under
    npm 10 (`Cannot read properties of null (reading 'edgesOut')`), and the
    standalone `@modelcontextprotocol/inspector-cli@1.0.3` is missing its
    client bundle (`Cannot find module .../client/bin/start.js`). 0.17.5 warns
    that it wants Node ≥ 22.7.5 but works on Node 20. Every inspector call does a
    real `initialize` → `notifications/initialized` → method exchange.
  - The doc's own example (`docs/agent-api.md`, "Worked example") was run with
    `curl` exactly as written, with `GRAIN_URL=http://127.0.0.1:18080`.
  - Browser: Playwright (`ui/node_modules`) with Chromium from
    `/var/cache/playwright`, 1280×900, signed in through the "Enter access code"
    page. Throwaway script, not committed.
  - "Journal" here is the daemon's log (`grain api: {agent, external:<name>} …`
    and `grain mcp: …` lines); the demo has no other journal route
    (`GET /api/system/journal` is 404).
- **Credentials issued** (as the access-code holder, `human:debian`):
  `ops-laptop` (doc example: `grain/task-admin` on `bwsalmon/grain`),
  `ops-widgets` (the same on `acme/widgets`, the demo's repository), `reader`
  (`tasks:read`, all repositories), `widgets-writer`
  (`tasks:write@acme/widgets`), `slow` (`tasks:read`, `rateLimit` 3,
  `maxConcurrent` 1), `capadmin` (`capabilities:admin`).

## Outcome

| Area | Result |
|---|---|
| Real MCP session: `initialize`, `tools/list` filtered by scope, `tools/call` | Pass |
| Filing a task with an explicit repo; refusals without one / on another repo / with `writes:`/`reads:` elsewhere / `autoMerge` / `autoApproveProposals` / an ungranted capability | Pass |
| Attribution in the journal and in task conversations (API and browser) | Pass for calls on a task; **gap** for a task the agent filed (follow-up 1) |
| Rotate, revoke, delete, revoke-all, case-insensitive names, setting off | Pass; 404 named "no task <id>" (**fixed**) |
| Rate limit, concurrency cap, 4 MiB body | Pass; `Retry-After: 1` regardless of rate (**fixed**) |
| Repo-scoped credential reading other repositories | **Fail: `GET /api/tasks` leaked every repository (fixed)** |
| `gma_` on internal routes; credential minting/rotation escalation | Pass |
| Doc's ops-laptop example as written | Works, with gaps in the doc (**fixed**) |

## Steps and observations

### 1. Enabling the endpoint

- Before enabling, `POST /mcp` with any `gma_` bearer: `403 {"error":"external
  agent API is disabled in Settings (externalAgentApi: false)"}`.
- `PUT /api/settings {"externalAgentApi": true}` with the access code (doc step
  1a, verbatim): 200.

### 2. MCP session and scope filtering

- `ops-laptop` (doc step 1b, verbatim): 201, token returned once,
  `resolvedScopes` `ci:read ci:write merge:write notifications:write tasks:read
  tasks:write`. `tools/list` (doc step 3a, verbatim curl, no `initialize`): 54
  tools.
- Inspector `tools/list` as `reader` (`tasks:read`): 12 read tools only
  (`api_get_task`, `api_list_tasks`, `api_get_task_tree`, …), no write tool.
- As `widgets-writer` (`tasks:write@acme/widgets`): 26 write tools and **no**
  read tools — each level is its own scope. The excluded task-context tools
  (`api_post_task_message`, `api_set_task_session_title`, console tools) are
  absent from every list.
- Every request is logged `grain mcp: {agent, external:<name>} initialize` /
  `tools/list` / `tools/call name="…"`.

### 3. Filing work (`api_create_task`) as `widgets-writer`

| Body | Answer |
|---|---|
| no `repo` | 403 "holds tasks:write only for , and POST /api/tasks does not name a repository it reaches" — the empty list is Fix 4; with this branch it reads "only for acme/widgets" |
| `repo: acme/widgets` | 201, task 19, `author: external:widgets-writer`, `authorKind: agent`, `state: proposed` (needs a person's Approve) |
| `repo: acme/gadgets` | 403 "holds tasks:write, but not on acme/gadgets" |
| `writes: [{repo: acme/gadgets}]` | 403 "cannot grant push access to acme/gadgets, which its credential does not cover" |
| `reads: [acme/gadgets]` | 403 "cannot grant read access to acme/gadgets …" |
| `noRepo: true` | 403 (no repository the scope reaches) |
| `capabilities: [package-publish]` | 403 "an agent cannot grant package-publish, which its own task does not hold" |
| `autoMerge: true` | 403 "an agent cannot turn on auto-merge …" |
| `state: queued` | 201, but filed `proposed` |
| `autoApproveProposals: true` | 403 "an external agent cannot turn on autoApproveProposals" |

The filed task carries the repository's default capabilities (`gcp-key`,
`gemini-key`, `self-debug`), identical to a task the person filed with the same
body (task 20). Each refusal is in the journal as `grain api: refused {agent,
external:widgets-writer} POST /api/tasks: <reason>`; the success as `… POST
/api/tasks scope=tasks:write repo="acme/widgets" reason="" -> 201`.

### 4. The doc's worked example

- Step 3's reorder and CI calls, verbatim (tasks 42 and 10 on
  `bwsalmon/grain`): both answered `isError` "holds … only for bwsalmon/grain,
  and … does not name a repository it reaches" — the demo has no such tasks, and
  that is how a missing task id is refused.
- Re-run with `ops-widgets` on the demo's tasks:
  - `api_reorder {"ids":["12"],"beforeId":"2"}`: refused, "task 12 is the
    standing session "director", which external:ops-widgets does not own".
  - `api_reorder {"ids":["11"],"beforeId":"2"}`: 200. Task 11's conversation has
    "External agent `ops-widgets` (`external:ops-widgets`) called `POST
    /api/tasks/reorder` through the agent API (scope `tasks:write`). Reason:
    prioritize release blocker", shown in the browser on `/tasks/11` as "grain on
    behalf of external:ops-widgets · automation".
  - `api_start_ci_run {"taskId":"6"}`: refused, `task 6 does not hold the
    "grain-ci" capability`. No demo task holds it, and it cannot be attached by
    hand ("grain grants it to every task on a repository whose CI Mode is "Use
    Grain CI""). After `PUT /api/repos/acme/widgets/ci-mode {"ciMode":"grain"}`,
    a newly filed task 22 held it, and the call on task 22 returned run
    `fake-ci-1` (202); `/tasks/22` shows the same "on behalf of
    external:ops-widgets" audit comment.
  - The doc did not say either precondition (grain-CI repository, no standing
    sessions) and still issued the example against the retired
    `bwsalmon/grain` (Fix 5).

### 5. Lifecycle

- Rotate `reader`: old token → 401 "invalid external agent bearer token", new
  token → 200.
- Names: `READER`, `Reader`, `ops-LAPTOP` each 400 "already exists"; after
  revoke, `Reader` is still refused; after delete, `Reader` is accepted (new id).
- Revoke: 204, then the token → 401 "external agent bearer token has been
  revoked"; rotating a revoked credential: 400.
- Delete: 204, then 401.
- Rotate/revoke/delete of id 999: 404 `{"error":"no task 999"}` (Fix 3).
- Setting off: every `gma_` request (`/mcp` and `/api/...`) → 403 "disabled in
  Settings"; back on → 200 again without re-issuing.
- `revoke-all`: `{"revoked":6}`, every token → 401, every listed credential
  `active: false`; a second call `{"revoked":0}`.

### 6. Limits

- `slow` (`rateLimit` 3): 200 200 200 429 429, body "exceeded rate limit (3
  requests/minute)", `Retry-After: 1` on `main` (Fix 2).
- Concurrency (`maxConcurrent` 1): a raw socket sent headers and 5 bytes of body
  and held; a second request got `429 "exceeded max concurrent requests (1)"`;
  the first then completed 200. The existing unit test for the cap ran one
  request in a goroutine and asserted nothing; it is now a real test.
- Body: 4,194,381 bytes → 413 "exceeds maximum size of 4194304 bytes", also with
  `Transfer-Encoding: chunked` (no Content-Length); 4,194,290 bytes → 200.

### 7. `gma_` on internal routes and escalation

- `ops-widgets` on `GET/PUT /api/settings`, `GET/POST
  /api/external-agents/tokens`, `revoke-all`: 403 "holds no api grant for scope
  …". `GET /api/auth/user`: 403 "not part of the agent API".
  `POST /api/tasks/6/messages`, `GET /api/console-sessions`: 403 with the
  no-task-context reason.
- `capadmin` (`capabilities:admin`, issued by the person): without a reason,
  every credential write is 403 "is destructive … give a reason". With
  `X-Grain-Acting-Reason`: minting `tasks:write,ci:write` or `capabilities:read`
  → 403 "an agent cannot grant …, which its own task does not hold"; minting
  `capabilities:admin` → 403 "dangerous api grant: only a person may grant it";
  rotating `ops-widgets` → 403 "cannot grant api:grain/task-admin@acme/widgets".
  Revoke and revoke-all are allowed (a kill switch any holder may pull).

### 8. Repo reach of reads — the leak

`ops-laptop`, restricted to `bwsalmon/grain`, called `GET /api/tasks` (and
`api_list_tasks` over MCP) and was answered **all 22 tasks**, every one on
`acme/widgets` or no repository. `?repo=bwsalmon/grain` gave the same 22. A
`?repo=acme/widgets` was correctly refused, so the gate was doing its part: it
defaults `?repo=` to the credential's one repository and admits the call on that
basis, but `handleListTasks` never read `?repo=` (Fix 1).

## Fixes (this branch)

1. **`GET /api/tasks` honours `?repo=`** (`pkg/ui/task_list_cache.go`,
   `pkg/ui/tasks.go`). The list is narrowed to that repository,
   case-insensitively as grants compare, cached under its own key with its own
   ETag. This closes the cross-repository read for single-repo external
   credentials, and for in-grain agents with a repository-scoped `tasks:read`,
   whose gate also admits `GET /api/tasks` on the `?repo=` it names
   (`agentCallRepo`). Re-verified live: `ops-laptop` now gets `0 []`,
   `ops-widgets` `12 ['acme/widgets']`, and inspector `api_list_tasks` as
   `ops-laptop` returns `[]`. Tests: `TestExternalAgentSingleRepoCredentialListsOnlyItsRepo`,
   `TestTaskListNarrowsToTheAskedRepo`. `routeQueries` now describes the
   parameter for the `api_list_tasks` tool.
2. **`Retry-After` covers the refill** (`pkg/ui/external_agents.go`): the
   limiter returns how long until the bucket holds a request — 20 s at 3/min,
   30 s at 2/min — and 1 s for a full concurrency cap. Live: `Retry-After: 20`.
   Test: `TestExternalAgentRateLimitRetryAfterCoversTheRefill`.
3. **Credential 404s name the credential**: "no external agent credential 99"
   rather than `NotFoundError`'s default "no task 99". Test:
   `TestExternalAgentCredentialNotFoundNamesTheCredential`.
4. **The gate's "holds X only for …" refusal names per-scope repositories**
   (`model.APIGrantSet.Repos`): a `tasks:write@acme/widgets` credential was told
   it "holds tasks:write only for , and …". It now reads "only for
   acme/widgets".
5. **`docs/agent-api.md`**: the example targets `ai-on-gke/grain`; it says that
   both tasks must be in the credential's repository, that standing sessions
   cannot be moved, and that `api_start_ci_run` with `taskId` needs a task
   holding `grain-ci` (a "Use Grain CI" repository). It also shows what the
   journal line looks like, that plain `curl` works without `initialize`, and
   the inspector command. It now documents `<area>:<level>@owner/name` scopes,
   that each level is its own scope, that single-repo reads hold nothing
   outside the repository, and the `Retry-After` values.
6. **Concurrency-cap test** (`TestExternalAgentConcurrencyCapRefusesASecondRequest`)
   holds a request open on an `io.Pipe` body and checks the second is refused
   and a third, after release, succeeds.

## Follow-ups (proposed, not fixed here)

1. **A task filed by an external agent shows no sign of it in the browser.**
   `/tasks/19` (filed by `external:widgets-writer`) shows "Creator: debian" and
   nothing else about who filed it. The API's `author: external:widgets-writer`
   is not rendered, and `creator` is the deployment's person, so the page reads
   as though they filed it. Deciding what `creator` should be for an
   external-agent filing touches team visibility and the "User" filter
   (`ui/src/taskAttributes.js`), so it is a follow-up rather than a fix here.
2. **`resolvedScopes` drops the per-scope repository**: `widgets-writer`
   (`tasks:write@acme/widgets`) is listed with `resolvedScopes:
   ["tasks:write"]` and no `repos`, which reads as all-repository write.

## Observations (not changed)

- `lastUsedAt` is touched by requests the gate then refuses (`capadmin`'s
  refused calls updated it). It means "last presented", not "last allowed".
- `GET /api/tasks?state=queued` ignores `state` (no such filter exists); listed
  here only because the plan's walkthrough would naturally reach for it.
- Not exercised: `expiresAt`, `409 config_managed_externally` through `/mcp`
  (covered by `TestExternalAgentExternalConfigRepoGuidance`), and whether
  `GET /api/ci/runs`'s shared-fault summaries can name other repositories to a
  single-repo credential (the run list itself is filtered by `repo`).
