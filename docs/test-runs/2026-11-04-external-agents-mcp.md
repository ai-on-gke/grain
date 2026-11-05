# Walkthrough Run: External Agents, Owner Trust, `/api/mcp` Controller Endpoint & Workstation Director (`docs/test-plans/2026-11-04.md` §4)

**Date:** 2026-11-04  
**Task:** 3070 (`Origin: task 2958`)  
**Repository / Branch:** `ai-on-gke/grain` (`grain/049f1e09/task-3070-walkthrough-external-agent`, base `40e2820f`)  
**Test Plan Section:** `docs/test-plans/2026-11-04.md` §4 ("External agents and the controller MCP" — PRs #629, #511, #689)  
**Artifacts Directory:** `docs/test-runs/2026-11-04-external-agents-mcp/`

---

## Executive Summary

We executed items 1–4 of `docs/test-plans/2026-11-04.md` §4 ("What is NOT proved") end-to-end against a real `grain daemon` built with the embedded React UI (`ui/dist` -> `pkg/ui/static`), backed by a live SQLite store, real local bare git repositories (`acme/app`, `acme/other`), the official MCP Go SDK client (`github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0 `StreamableClientTransport`), and real headless Chromium (`156.0.8078.79`) via Playwright.

All four walkthrough areas succeeded after reproducing and fixing **five scoped attribution/redaction gaps (`F1`–`F5`)** uncovered during Item 2:

| ID | Surface | Defect Reproduced | Fix & Regression Test |
|---|---|---|---|
| **F1** | Task UI (`Declared.jsx`) | `Filed by` checked `isExternalAgent(t.author)` and constructed `external agent <name> (external:<name>)` from `t.author`, dropping `t.authorLabel` (`for <owner>`) on owned `gma_` tokens. | Render `${t.authorLabel} (${t.author})` for external agents (preserving `(external:<name>)` on unowned tokens while surfacing `for <owner>` on owned tokens) and wrap `Filed by` in `data-testid="filed-by"`. Covered by `Declared.test.jsx` and `attribution.test.jsx`. |
| **F2** | Task UI (`Declared.jsx`, `DetailOverlay.jsx`, `Timeline.jsx`, `chat.js`) | `t.provenance` (`filed from <tool> by <owner>`), `t.filedVia`, and comment `c.filedVia` were returned by `/api/tasks/{id}` and `grain get`, but never rendered anywhere in the web UI. | Surfaced `Filed via` (`data-testid="filed-via"`), `t.provenance` under the `Provenance` disclosure (`data-testid="task-provenance"`), and `· from <filedVia>` / `(from <filedVia>)` on comments in `Timeline.jsx` and `chat.js`. Covered by `attribution.test.jsx` and `chat.test.js`. |
| **F3** | `grain task read` (`task://<id>/record.md` & `conversation.md`) | `pkg/mcp/task_tools.go` (`renderTaskRecord`) omitted `Provenance:` on the task header, formatted comment authors with raw `c.Author` instead of `c.Who()` (dropping `for <owner>`), and omitted `(from <c.FiledVia>)` on comments in both `record.md` and `conversation.md`. | Mapped `Provenance` and `Comment.FiledVia` in `pkg/daemon/mcpserver.go` -> `pkg/mcp/task_views.go`, added `Provenance:` to `renderTaskRecord`, switched comment headers to `c.Who()`, and appended `(from <c.FiledVia>)`. Covered by `pkg/mcp/task_tools_test.go`. |
| **F4** | Daemon API journal (`pkg/ui/agent_api_gate.go`) | Refused external-agent requests logged `call.actor` instead of `call.journalActor()` (omitting `on behalf of <owner>`), and neither allowed nor refused journal lines logged `X-Grain-Client` (`client=%q`). | Switched refused journal lines to `call.journalActor()` and appended `client=%q` whenever `X-Grain-Client` is non-empty. Covered by `pkg/ui/external_agents_test.go`. |
| **F5** | Log/text shape redaction (`pkg/redact/redact.go`) | `pkg/redact` redacted `ghp_`, `github_pat_`, `sk-ant-`, etc. by shape, and `X-Grain-Access-Code` / `Authorization` headers by key, but had no shape pattern for `gma_<64 hex>` tokens appearing in free-form error strings or command logs. | Added `\bgma_[0-9a-f]{64}\b` -> `[redacted: external agent token]` to `pkg/redact/redact.go`. Covered by `pkg/redact/access_code_test.go`. |

---

## Environment & Stubbed Boundaries

Per the test plan's shared-environment rules:

- **Real components used:**
  - `grain` binary built from source with embedded UI (`cd ui && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`).
  - Real `grain daemon` serving HTTP/API/UI/MCP over SQLite (`grain.db`), local host sandboxes, and local bare git repositories (`acme/app.git`, `acme/other.git`).
  - Real MCP SDK client (`github.com/modelcontextprotocol/go-sdk v1.8.0`, `mcp.StreamableClientTransport` over `POST /api/mcp`).
  - Real Chromium (`156.0.8078.79`) driven by Playwright at `1440×960` and `390×900` in both `light` and `dark` color schemes.
- **Unavailable external boundaries stubbed (and named):**
  1. **Cloud IAP / OIDC identity proxy (`tests/iap/iapsim.go`):** Signed real ES256 IAP assertions (`iss: https://iap.test.local`, `aud: /projects/123456/global/backendServices/789012`) verified by `grain daemon -iap-audience ... -iap-issuer-url ... -iap-certs-url http://127.0.0.1:18180/jwks.json` so verified human callers (`alice@example.com`, `bob@example.com`, `operator@example.com`) exercised real JWT verification without Google Cloud IAP network reach.
  2. **GitHub REST reachability (`-github-host 127.0.0.1:1 -github-insecure-http`):** Sandbox has no external GitHub API access; pointing the forge host at closed `127.0.0.1:1` kept git operations against local bare repositories (`acme/app`, `acme/other`) funded via `/api/deployment/git-connections`.
  3. **Model provider CLI for Item 4 Director chat:** No live Anthropic API key exists in the sandbox; `item4_workstation.sh` placed a 4-line executable `claude` stream-JSON stub on `PATH` returning a single assistant message (`"Director stub ready: local workstation policy allowed this run cleanly."`) so `grain daemon` ran its full orchestrator, local-policy gate, host sandbox acquisition, and Director console-session turn lifecycle unmodified.
- **Packages installed in sandbox during run:** `sqlite3`, `chromium` (`156.0.8078.79`).

---

## Item 1: Real MCP Client over `POST /api/mcp` (`gma_` Bearer & `X-Grain-Access-Code`)

**Scripts & Artifacts:**
- `docs/test-runs/2026-11-04-external-agents-mcp/item1_mcp.sh`
- `docs/test-runs/2026-11-04-external-agents-mcp/mcp_sdk_walkthrough.go`
- `docs/test-runs/2026-11-04-external-agents-mcp/item1-jsonrpc.json` (all 34 wire HTTP / JSON-RPC exchanges with redacted tokens)

### Procedure & Verbatim Results

1. **Setup:** Enabled `externalAgentApi: true` via `PUT /api/settings` (verified that before enabling, `POST /api/mcp` with a `gma_` token is refused `403 Forbidden`). Minted an owned `gma_` token (`item1-mcp-agent`, `scopeSet: "grain/external-dev"`, `repos: ["acme/app"]`, `owner: "alice@example.com"`), plus a narrow acting task (`task 2` on `acme/app` holding `api:tasks:read` and `api:tasks:write`).
2. **`initialize` & `tools/list` over `POST /api/mcp` (`Authorization: Bearer gma_<redacted>`):**
   - Negotiated MCP `protocolVersion: "2025-03-26"` against `serverInfo: {"name": "grain-controller", "version": "0.1.0"}` (`instructions` length: `1845` bytes).
   - `tools/list` returned **exactly 6 tools**, every one carrying `_meta: {"antigravity/alwaysLoad": true}`:
     1. `grain_explore_scope` (`_meta.antigravity/alwaysLoad: true`)
     2. `grain_invoke` (`_meta.antigravity/alwaysLoad: true`)
     3. `grain_filing_context` (`_meta.antigravity/alwaysLoad: true`)
     4. `grain_submit_task` (`_meta.antigravity/alwaysLoad: true`)
     5. `grain_list_tasks` (`_meta.antigravity/alwaysLoad: true`)
     6. `grain_get_task` (`_meta.antigravity/alwaysLoad: true`)
3. **`grain_explore_scope`:**
   - Overview call (`{}`) returned only the groups/scopes authorized for `grain/external-dev` (`ci`, `deployment`, `merge`, `repos`, `sandboxes`, `tasks`, `templates`; `2557` bytes) — omitting unauthorized `settings`, `secrets`, `access`, and `capabilities` admin routes.
   - Drill-down call (`{"scope": "tasks:write"}`) returned the route schemas (`api_create_task`, `api_update_task`, `api_add_comment`, etc.) with required parameters and body descriptions.
4. **`grain_submit_task` & `grain_get_task` (compact vs full):**
   - `grain_submit_task` (`repo: "acme/app"`, `title: "MCP SDK task via Authorization: Bearer gma_"`, ~2.5 KB body, omitting `approve`) created **Task 3** in `state: "proposed"`, `approved: false`:
     ```json
     {
       "id": "3",
       "repo": "acme/app",
       "state": "proposed",
       "title": "MCP SDK task via Authorization: Bearer gma_",
       "url": "/tasks/3"
     }
     ```
   - `grain_get_task({"id": "3"})` (default `summary` view) returned a **201-byte** compact summary (well under ~1 KB), whereas `grain_get_task({"id": "3", "view": "full"})` returned **3,430 bytes**:
     ```text
     Task 3: MCP SDK task via Authorization: Bearer gma_
     State: proposed
     Priority: 5
     Kind: proposal
     Repo: acme/app (base default branch)
     (view "full" for the whole record, "conversation" to page the thread)
     ```
5. **`grain_invoke` (allowed vs refused):**
   - **Allowed (`api_add_comment` on Task 3):** Succeeded (`isError: false`), posting comment on `acme/app` task 3 with `X-Grain-Client: mcp-go-sdk/1.8.0 session=item1-bearer-gma`.
   - **Refused scope (`api_get_settings`):** Returned `isError: true`:
     ```text
     api_get_settings failed: HTTP 403: external:item1-mcp-agent holds no api grant for scope settings:read, which GET /api/settings needs
     ```
   - **Refused out-of-scope repo (`api_create_task` on `acme/other`):** Returned `isError: true`:
     ```text
     api_create_task failed: HTTP 403: external:item1-mcp-agent holds api grant tasks:write on [acme/app], which does not cover repository acme/other
     ```
6. **Repeat with `X-Grain-Access-Code` & Daemon Log Redaction Check (Design Distinction `D1`):**
   - **Sending a `gma_` token inside `X-Grain-Access-Code` (with no `Authorization` header):** Returned `HTTP 401 Unauthorized` (`{"error":"access code required"}`). By design (`pkg/ui/accesscode.go:60-62`), `X-Grain-Access-Code` carries the deployment's shared access code when an upstream proxy (such as Cloud Run / UberProxy / IAP) occupies `Authorization: Bearer <oidc-jwt>`; `gma_` external-agent tokens authenticate exclusively via `Authorization: Bearer gma_...`.
   - **Sending the access code in `X-Grain-Access-Code` alongside `Authorization: Bearer <upstream-proxy-jwt>` + `X-Grain-Acting-Task: 2`:** Full MCP SDK run succeeded (`initialize`, `tools/list` -> 6 tools with `alwaysLoad`, `grain_explore_scope` narrowed to `tasks:read` + `tasks:write`, `grain_submit_task` -> Task 4 `proposed`, `grain_get_task` -> `205`-byte summary vs `3,367`-byte full record, `grain_invoke` `api_add_comment` allowed, `api_get_settings` refused `403: caller holds no grant for scope settings:read`, `api_create_task` on `acme/other` refused `403: task 2 holds tasks:write, but not on acme/other`).
   - **Redaction verification:** Grepped `daemon.log` for both the raw `gma_` token secret and the raw `ACCESS_CODE` value; **zero occurrences** appeared in `daemon.log`. Added `pkg/redact` shape redaction (`F5`) so `gma_<64 hex>` strings are also scrubbed (`[redacted: external agent token]`) if echoed in any free-form log or error string.

---

## Item 2: Owner Attribution Across Every Surface

**Scripts & Artifacts:**
- `docs/test-runs/2026-11-04-external-agents-mcp/item2_attribution.sh`
- `docs/test-runs/2026-11-04-external-agents-mcp/item2-attribution.json`
- `docs/test-runs/2026-11-04-external-agents-mcp/item2-cli-and-journal.txt`
- `docs/test-runs/2026-11-04-external-agents-mcp/capture_screenshots.mjs`
- `docs/test-runs/2026-11-04-external-agents-mcp/contact-sheet.png` & `screenshots/`

### Token Issuance & Escalation Guards Verified

1. **Token A (`alice-self`, self-minted by `alice@example.com` via IAP `selfService: true`):**
   - Cross-owner attempt (`{"selfService":true,"owner":"bob@example.com"}`) refused `HTTP 403`:
     `{"error":"a self-service credential belongs to alice@example.com, not \"bob@example.com\""}`
   - Self-minted token recorded `createdBy: "human:alice@example.com"`, `owner: "alice@example.com"`.
2. **Token B (`bob-by-op`, operator-minted for `bob@example.com`):**
   - Operator (`operator@example.com` via IAP) issued `{"name":"bob-by-op","scopeSet":"grain/external-dev","owner":"bob@example.com","repos":["acme/app"]}` -> `HTTP 201`, `createdBy: "human:operator@example.com"`, `owner: "bob@example.com"`.
3. **Token C1 (`alice-child-agent`, minted by an owned external agent `alice-issuer` holding `capabilities:admin` + `tasks:read` + `tasks:write`):**
   - Scope-widening attempt (`scopeSet: "grain/external-dev"`) refused `HTTP 403`:
     `{"error":"an agent cannot grant api:grain/external-dev, which its own task does not hold"}`
   - Cross-owner attempt (`owner: "bob@example.com"`) refused `HTTP 403`:
     `{"error":"an agent cannot issue a credential on behalf of \"bob@example.com\""}`
   - Attenuated mint (`scopes: ["tasks:read","tasks:write"]`, omitting `owner`) succeeded (`HTTP 201`) and automatically inherited `owner: "alice@example.com"`, `createdBy: "agent:external:alice-issuer"`.
4. **Token C2 (`task-minted-bot`, minted by in-sandbox acting `task 2` holding `api:capabilities:admin`):**
   - Naming `owner: "alice@example.com"` refused `HTTP 403`:
     `{"error":"an agent cannot issue a credential on behalf of \"alice@example.com\""}`
   - Unowned mint succeeded (`HTTP 201`) with `owner: ""` (`origin_behalf_id: NULL`), `createdBy: "agent:2"`.

### Surface-by-Surface Attribution Matrix (Post-Fix Verification)

Each token filed a task and a comment through the `grain` CLI (`grain create` and `grain comment`) with distinct `GRAIN_CLIENT` and `GRAIN_CLIENT_SESSION` environment variables:

| Task | Token Path | `GRAIN_CLIENT` / `GRAIN_CLIENT_SESSION` | SQLite (`origin_actor_id` / `origin_behalf_id` / `creator`) | UI `/tasks/<id>` (`Filed by` / `Filed via` / `Provenance`) | `grain get <id>` & `grain fs read task://<id>/record.md` |
|---|---|---|---|---|---|
| **3** | `alice-self` (Alice self-service) | `claude-code/1.2.0` / `sess-alice-101` | `external:alice-self` / `alice@example.com` / `alice@example.com` | `external agent alice-self for alice@example.com (external:alice-self)` · `claude-code/1.2.0 session=sess-alice-101` · `filed from claude-code/1.2.0 session=sess-alice-101 by alice@example.com` | `Filed by: external agent alice-self for alice@example.com` · `Provenance: filed from claude-code/1.2.0 session=sess-alice-101 by alice@example.com` |
| **4** | `bob-by-op` (Operator for Bob) | `codex-cli/0.9.2` / `sess-bob-202` | `external:bob-by-op` / `bob@example.com` / `bob@example.com` | `external agent bob-by-op for bob@example.com (external:bob-by-op)` · `codex-cli/0.9.2 session=sess-bob-202` · `filed from codex-cli/0.9.2 session=sess-bob-202 by bob@example.com` | `Filed by: external agent bob-by-op for bob@example.com` · `Provenance: filed from codex-cli/0.9.2 session=sess-bob-202 by bob@example.com` |
| **5** | `alice-child-agent` (Agent-minted child) | `jetski/2.1` / `sess-child-303` | `external:alice-child-agent` / `alice@example.com` / `alice@example.com` | `external agent alice-child-agent for alice@example.com (external:alice-child-agent)` · `jetski/2.1 session=sess-child-303` · `filed from jetski/2.1 session=sess-child-303 by alice@example.com` | `Filed by: external agent alice-child-agent for alice@example.com` · `Provenance: filed from jetski/2.1 session=sess-child-303 by alice@example.com` |
| **6** | `task-minted-bot` (Task-minted unowned) | `subagent-runner/0.4` / `sess-taskbot-404` | `external:task-minted-bot` / `NULL` / `operator@example.com` | `external agent task-minted-bot (external:task-minted-bot)` · `subagent-runner/0.4 session=sess-taskbot-404` · `filed from subagent-runner/0.4 session=sess-taskbot-404 by operator@example.com` | `Filed by: external agent task-minted-bot` · `Provenance: filed from subagent-runner/0.4 session=sess-taskbot-404 by operator@example.com` |

*(Note `D2` on CLI verbs: the workstation CLI uses `grain get <id>` and `grain fs read task://<id>/record.md`, while the in-sandbox CLI exposes `grain task read <id>` backed by the same `task://<id>/record.md` renderer in `pkg/mcp/task_tools.go`. Both were verified.)*

### Verbatim `grain fs read task://4/record.md` & Daemon Journal Lines

```text
Task 4: Task from operator-minted bob token
State: proposed
Priority: 5
Filed by: external agent bob-by-op for bob@example.com
Provenance: filed from codex-cli/0.9.2 session=sess-bob-202 by bob@example.com
Filed at: 2026-11-04T22:43:03Z
Repo: acme/app

Description:
  Filed via CLI walkthrough (codex-cli/0.9.2, sess-bob-202).

Conversation (2):
  [2026-11-04T22:43:03Z] external agent bob-by-op for bob@example.com (agent) (from codex-cli/0.9.2 session=sess-bob-202):
  Follow-up comment from bob-by-op CLI
  [2026-11-04T22:43:03Z] grain on behalf of external:bob-by-op (automation):
  External agent `bob-by-op` (`external:bob-by-op`) on behalf of `bob@example.com` called `POST /api/tasks/{id}/comments` through the agent API (scope `tasks:write`).
```

Daemon journal (`daemon.log`):
```text
2026/11/04 22:43:03 grain api: refused {agent, external:alice-issuer on behalf of alice@example.com} POST /api/external-agents/tokens: an agent cannot grant api:grain/external-dev, which its own task does not hold
2026/11/04 22:43:03 grain api: {agent, external:alice-self on behalf of alice@example.com} POST /api/tasks scope=tasks:write repo="acme/app" reason="" client="claude-code/1.2.0 session=sess-alice-101" -> 201
2026/11/04 22:43:03 grain api: {agent, external:bob-by-op on behalf of bob@example.com} POST /api/tasks scope=tasks:write repo="acme/app" reason="" client="codex-cli/0.9.2 session=sess-bob-202" -> 201
2026/11/04 22:43:03 grain api: {agent, external:alice-child-agent on behalf of alice@example.com} POST /api/tasks scope=tasks:write repo="acme/app" reason="" client="jetski/2.1 session=sess-child-303" -> 201
2026/11/04 22:43:04 grain api: {agent, external:task-minted-bot} POST /api/tasks scope=tasks:write repo="acme/app" reason="" client="subagent-runner/0.4 session=sess-taskbot-404" -> 201
2026/11/04 22:43:04 grain api: refused {agent, external:alice-self on behalf of alice@example.com} POST /api/tasks: an agent cannot name somebody else as the author of work it files
```

### Spoofing Checks (`author`, `X-Forwarded-User`, `filedVia`)

- **Body `author` spoof (`{"title":"...","repo":"acme/app","author":"mallory@example.com"}`):** Refused `HTTP 403`:
  `{"error":"an agent cannot name somebody else as the author of work it files"}`
- **`X-Forwarded-User: mallory@example.com` + body `{"filedVia":"forged"}` + `X-Grain-Client: spoof-check/1.0 session=xfu`:** Created Task 7 with `authorLabel: "external agent alice-self for alice@example.com"`, `creator: "alice@example.com"`, `filedVia: "spoof-check/1.0 session=xfu"`, `provenance: "filed from spoof-check/1.0 session=xfu by alice@example.com"` — confirming both `X-Forwarded-User` and body `filedVia` are completely ignored.
- **Filter query:** `grain list -filter filed_via:has:sess-bob-202` returned exclusively Task 4 (`4 proposed acme/app Task from operator-minted bob token`).

### UI Visual Verification (Before vs After, 1440px & 390px, Light & Dark)

Captured Playwright screenshots (`docs/test-runs/2026-11-04-external-agents-mcp/contact-sheet.png` and `screenshots/`) across `/tasks/3`, `/tasks/4`, and `/settings/agents` at `1440×960` (desktop) and `390×900` (mobile) in both `light` and `dark` color schemes:

- **Before fix (`before-*`):** `/tasks/3` rendered `Filed by: external agent alice-self (external:alice-self)` (omitting `for alice@example.com` on `/tasks/4` as well, where `bob@example.com` was only visible indirectly via `Creator`), and neither task detail view nor comment headers displayed `filedVia` or `provenance`.
- **After fix (`after-*`):**
  - `/tasks/3` renders `Filed by: external agent alice-self for alice@example.com (external:alice-self)`, `Filed via: claude-code/1.2.0 session=sess-alice-101`, expanded `Provenance` row `filed from claude-code/1.2.0 session=sess-alice-101 by alice@example.com`, and comment header `external agent alice-self for alice@example.com · agent · from claude-code/1.2.0 session=sess-alice-101`.
  - `/tasks/4` renders `Filed by: external agent bob-by-op for bob@example.com (external:bob-by-op)`, `Filed via: codex-cli/0.9.2 session=sess-bob-202`, and `filed from codex-cli/0.9.2 session=sess-bob-202 by bob@example.com`.
  - `/settings/agents` renders cleanly in all four viewport/theme combinations (`D3`: `/settings/agents` configures run-level agent defaults such as turn caps, branch templates, and deployment ID; external-agent token CRUD and `ownerTrust` are API/CLI surfaces).
  - **Overflow check:** `horizontalOverflowPx === 0` (`documentElement.scrollWidth <= window.innerWidth`) verified across all 12 `after-*` screenshots (`screenshots/after-ui-checks.json`).

---

## Item 3: Owner Trust, Escalation Holds/Refusals & Token Rotation

**Scripts & Artifacts:**
- `docs/test-runs/2026-11-04-external-agents-mcp/item3_owner_trust.sh`
- `docs/test-runs/2026-11-04-external-agents-mcp/item3-owner-trust.txt`
- `docs/test-runs/2026-11-04-external-agents-mcp/item3-owner-trust.json`

### Summary of Checks & Outcomes

1. **Self-configuration guard:** `alice-dev` (`grain/external-dev`) calling `PUT /api/settings` with `ownerTrust` was refused `HTTP 403` (`holds no api grant for scope settings:write`).
2. **Matching `(owner, repo)` auto-approval vs untrusted boundaries:** With `ownerTrust: [{"owner": "alice@example.com", "repo": "acme/app"}]`:
   - `alice-dev` filing on `acme/app` -> **Task 2**, `HTTP 201`, `state: "queued"`, `approvedBy: "owner-trust"`, `approvedByLabel: "grain for alice@example.com"` (SQLite: `approval_actor_kind="automation"`, `approval_actor_id="owner-trust"`, `approval_behalf_kind="human"`, `approval_behalf_id="alice@example.com"`).
   - `alice-dev` filing on `acme/other` -> `HTTP 201`, `state: "proposed"`, `approvedBy: null`.
   - `alice-dev` filing on `acme/app` with cross-repo `reads: ["acme/other"]` -> `HTTP 201`, `state: "proposed"`, `approvedBy: null`.
   - `bob-dev` (`owner: "bob@example.com"`) filing on `acme/app` while spoofing `X-Forwarded-User: alice@example.com` -> `HTTP 201`, `state: "proposed"`, `authorLabel: "external agent bob-dev for bob@example.com"`.
3. **Priority, `keep_running`, and `requires_person` grants kept for a person:**
   - With standard `grain/external-dev` (`alice-dev`):
     - `priority: 6` -> `HTTP 403` (`{"error":"an external agent cannot set priority above 5 without tasks:admin"}`)
     - `keepRunning: true` -> `HTTP 403` (`{"error":"durable lets a task jump the queue and run with no deadline: only a person may grant it"}`)
     - `capabilities: ["read-issues"]` (where `read-issues` has `restrictions.requiresPerson: true` in `/api/capability-policy`) -> `HTTP 403` (`{"error":"an agent cannot grant read-issues, which its own task does not hold"}`)
   - Even with an elevated external-agent token (`alice-admin-ext`, holding `tasks:admin` + `capabilities:admin`, owned by `alice@example.com`):
     - Clean baseline filing on `acme/app` -> `state: "queued"`, `approvedBy: "owner-trust"`.
     - `priority: 8` -> `HTTP 201`, **held in `state: "proposed"`** (`approvedBy: null`) — `ownerTrustApproves` explicitly withholds auto-approval when `priority > DefaultTaskPriority (5)`.
     - `keepRunning: true` -> `HTTP 403` (`only a person may grant it`).
     - `capabilities: ["read-issues"]` (`requiresPerson: true`) -> `HTTP 403` (external agents have no `proposedBy` task holding the capability).
4. **`PATCH` withdrawal when moving out of trusted repo:**
   - Editing `title` in-place on `acme/app` (`PATCH /api/tasks/2 {"title":"Trusted filing edited in acme/app"}`) kept `state: "queued"`, `approvedBy: "owner-trust"`.
   - Changing `repo` to `acme/other` (`PATCH /api/tasks/2 {"repo":"acme/other"}`) withdrew `owner-trust` approval and returned Task 2 to `state: "proposed"` (`approvedBy: null`).
5. **Token rotation preserves `owner` & blocks cross-owner / agent rotation:**
   - Bob's `capabilities:admin` external agent calling `POST /api/external-agents/tokens/1/rotate` -> `HTTP 403` (`{"error":"an agent cannot rotate a credential owned by \"alice@example.com\""}`).
   - In-sandbox acting task holding `api:capabilities:admin` calling `POST /api/external-agents/tokens/1/rotate` -> `HTTP 403` (`{"error":"an agent cannot rotate a credential owned by \"alice@example.com\""}`).
   - Alice rotating her own token via IAP (`POST /api/external-agents/tokens/1/rotate`) -> `HTTP 200`, new `tokenPrefix: "gma_349d"`, `owner: "alice@example.com"` preserved, `rotatedAt` populated.
   - Old token immediately rejected with `HTTP 401` (`{"error":"invalid external agent bearer token"}`); new rotated token filed on `acme/app` and landed `state: "queued"` via `owner-trust` (`grain for alice@example.com`).

---

## Item 4: `grain init workstation` on a Clean `HOME` & Director Chat

**Scripts & Artifacts:**
- `docs/test-runs/2026-11-04-external-agents-mcp/item4_workstation.sh`
- `docs/test-runs/2026-11-04-external-agents-mcp/item4-grain_policy-open.yml`
- `docs/test-runs/2026-11-04-external-agents-mcp/item4-grain_policy-none.yml`
- `docs/test-runs/2026-11-04-external-agents-mcp/item4-daemon.sh`
- `docs/test-runs/2026-11-04-external-agents-mcp/item4-init-open-stdout.txt`
- `docs/test-runs/2026-11-04-external-agents-mcp/item4-init-none-stdout.txt`
- `docs/test-runs/2026-11-04-external-agents-mcp/item4-workstation.json`

### Procedure & Verbatim Results

1. **Clean `HOME` initialization (`grain init workstation -repo acme/app -requester alice@example.com -listen 127.0.0.1:14320`):**
   - Generated `~/.grain/grain_policy.yml` (`item4-grain_policy-open.yml`) containing all seven default Director session grants plus `grain/config` in `repos:` and `network: ["open"]`:
     ```yaml
     repos: ["acme/app", "grain/config"]
     local_requesters: ["alice@example.com", "debian"]
     approvers: ["alice@example.com", "debian"]
     allow_unapproved_external: false
     capabilities:
       allow:
         - "grain-ci"
         # The Director chat's own grants. Remove one and the Director is held for it.
         - "durable"
         - "self-debug"
         - "https://grain.dev/capabilities/grain#journal"
         - "api:deployment:read"
         - "https://grain.dev/capabilities/grain#health"
         - "bootstrap-playbooks"
         - "api:ci-read"
     network: ["open"]
     allow_external_on_host_sandboxes: false
     ```
2. **Opening & messaging the Director chat (`task 1`) on the generated daemon:**
   - Started the generated `daemon.sh` (`grain daemon -local-policy .../grain_policy.yml -sandbox-backend host -sandbox-root .../sandboxes ...`), registered the stub `claude` connection, and posted `"Hello Director, verify local workstation policy allows you to run."` to `/api/tasks/1/comments`.
   - **Result:** Local policy did **not** hold the Director chat. Attempt `#1` queued and dispatched in `3.7 ms` (`queuedToDispatchedSeconds: 0.0037`), acquired its host sandbox, ran the stub `claude` CLI (`startedToFirstTurnSeconds: 0.1426`), posted comment `#3` (`"Director stub ready: local workstation policy allowed this run cleanly."`), and parked cleanly back in `state: "awaiting_reply"`, `blocked: false`.
3. **`-network none` Warning & Host-Sandbox Guard:**
   - Running `grain init workstation -sandbox-backend docker -network none ...` succeeded (`exit 0`) and printed the explicit Director warning (`item4-init-none-stdout.txt`):
     ```text
     Once the daemon is up, run `grain settings -network-access none`: the policy allows only that network.
     The Director chat asks for open network, so this policy holds it: add "open" to network: in the policy, or set the Director's network to none in its console session settings.
     ```
   - Running `grain init workstation -sandbox-backend host -network none ...` exited `1` refusing to write an unenforceable `none` policy on host sandboxes (which share the workstation's network namespace).

---

## Verification & Regression Suite

All unit, Spec, hygiene, and security tests pass locally:

- `cd ui && npx vitest run src/chat.test.js src/components/detail/Declared.test.jsx src/components/attribution.test.jsx src/components/detail/Timeline.test.jsx` (`93 passed`)
- `cd ui && npm run format:check && npm run build`
- `cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright PLAYWRIGHT_PROJECT=chromium npx playwright test e2e/external-agent-filer.spec.js --project=chromium` (`1 passed`, updated for owned-token `for <owner>` attribution in `Filed by`)
- `go test -race ./pkg/redact/... ./pkg/mcp/... ./pkg/ports/...`
- `go test -race ./pkg/ui -run "TestExternalAgent|TestControllerMCP|TestOwnerTrust|TestXGrainClient"`
- `go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` (no CLI flags changed)
- `go test ./tests/hygiene/... ./tests/security/...`
