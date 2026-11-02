# Security fixes, live — 2026-11-02 (test plan §10)

Walkthrough of [`docs/test-plans/2026-11-02.md` §10](../test-plans/2026-11-02.md)
(task 2452, filed by task 2399): PR #388 (redacted task mutation responses
for write-only agents), #366 (no widening past an inherited
`networkAccess: none`), #360 (held proposals with `keep_running` or a
priority above the proposer's), #398/#332 (`droppedDefaults` and the filer
notice), #328 (a refused locked secret binding stores nothing) and #331
(`-grit-url` needs `-grit-experimental`), plus the findings
[`docs/security/audits/2026-11-01.md`](../security/audits/2026-11-01.md)
filed and this range fixed.

**Result:** every fix holds live through every channel it applies to. One
bug was found and fixed on this branch (F1: a held `keep_running` /
high-priority proposal told the run, and the person approving it, that it
"could have been queued", without naming the setting). Three smaller gaps
are filed as follow-ups (F2–F4).

## Environment

- Tree: `origin/main` at `cc939397` (PR #438), plus this branch's fix.
  None of §10's files had changed on `main` since the plan
  (`git log origin/main -- pkg/ui/agent_api_gate.go pkg/ui/client.go
  pkg/model/hold_for_approval.go pkg/orchestrator/finish.go
  pkg/ui/capability_servers.go pkg/daemon/ci.go`).
- Shared environment steps 1, 2, 4, 5 and 7: `npm ci && npm run build` in
  `ui/`, then `go build -o /home/debian/bin/grain ./cmd/grain`; `sqlite3`
  installed with apt. Step 6 (`GRAIN_SANDBOX_PROXY` workaround) was not
  needed for the deployment's own CLI. For the walkthrough's own sandbox
  channel I set `GRAIN_SANDBOX_PROXY`/`GRAIN_SANDBOX_TOKEN` explicitly (see
  below) so the CLI reached the demo's proxy, not this sandbox's.
- **Demo A** (main matrix):
  `grain demo -github-sim -git-proxy -fake-ci -secrets-store -reconcile 2s
  -open=false -addr 127.0.0.1:8811 -access-code-file …`. Store
  `/tmp/grain-demo-*/grain.db`. Settings set over the API:
  `networkAccess: none`, `externalAgentApi: true`.
- **Demo B** (#328): `grain demo -secrets-store -external-config-repo
  https://github.com/example/grain-config -addr 127.0.0.1:8812`
  (`configRepoOnly: true`).
- **Demo C** (F1 fix, live): Demo A's flags on the rebuilt binary, port 8813.
- **Daemons** (#331, audit finding 3): `grain daemon -data-dir … -ui-addr …
  -sandbox-port-base=0 -disable-native-sandboxes`. There is no run host:
  nothing here needed an agent to run.
- No real agent backend was used. The "scripted sandbox run" is
  `POST /api/e2e/git-proxy/runs`, which records a run and mints the
  sandbox's proxy token as `runOne` does, plus a real `grain mcpserver`
  process for that task.

### The three channels

| Channel | How it was driven |
|---|---|
| **External agent token** | `POST /api/external-agents/tokens {"name":"live-writeonly","scopes":["tasks:write"],"repos":["acme/widgets"]}` → `gma_…`, then `curl -H "Authorization: Bearer gma_…"` against REST, and against `POST /mcp` (`tools/call api_update_task` / `api_create_task`). |
| **MCP tool call from a sandbox** | `grain mcpserver -sandbox-root /tmp/sb -server http://127.0.0.1:8811 -task <id> [-interactive]`, the process a run's framework forks, driven over stdio JSON-RPC (`initialize`, `tools/call`) by a small Python script. Its `api_*` tools send `X-Grain-Acting-Task: <id>`. Live `propose_task` is offered only with `-interactive`; without it, `propose_task` is relayed at the run's end (`orchestrator.ProcessResult`), which the demo never runs, so that half is covered by `pkg/orchestrator` tests. |
| **Sandbox proxy's `grain api`** | `env -i HOME=/tmp/sbhome GRAIN_SANDBOX_PROXY=http://127.0.0.1:<proxy> GRAIN_SANDBOX_TOKEN=<run token> grain api …` (and `grain task propose …`), i.e. the sandbox CLI through the demo's real git proxy `/cli/` routes and its in-process API. |

The acting tasks: **#20** (`api:tasks:write` on acme/widgets, no
`tasks:read`, `autoMerge`, no `networkAccess` of its own, run `20-run-1`) and
**#30** (the same plus the deployment's three default grants, so that a
proposal of the defaults' shape is within its permission). **#21** is an
approved, queued sibling.

Store diffs: before and after each call, a sorted `.dump` of `task`,
`task_grant`, `task_comment`, `task_event`, `task_link`, `task_write`,
`task_read`, `external_agent_token` and `grain_config`, diffed. The
only change any refusal made was `external_agent_token.last_used_at` for the
external token, which is that token's use being recorded, not the request.

## Matrix

"Δ store" is the number of changed rows in the dump.

### PR #388 — redaction (write-only agent)

| Call | External REST | External `/mcp` | Sandbox MCP (`grain mcpserver`) | Sandbox `grain api` |
|---|---|---|---|---|
| `PATCH /api/tasks/21 {"title":…}` | 200 `{"id":"21","state":"queued","title":…}` | `{"id":"21","state":"queued","title":…}` | `{"id":"21","state":"queued","title":…}` | `{"id":"21","state":"queued","title":…}` |
| `PATCH /api/tasks/21 {}` | 400 `requires at least one field to edit`, Δ store 0 (token `last_used_at` only) | isError, HTTP 400, same | isError, same, Δ 0 | exit 1, same, Δ 0 |
| `POST /api/tasks/21/comments` | 200 `{"id":"21","state":"queued"}` | — | `api_post_task_message` is the own-task message route; refused for #21 with a pointer to `/comments` and `/agent-messages` | exit 0 `{"id":"21","state":"queued"}` |
| `POST /api/tasks/reorder {"ids":["21"]}` | 200 `[{"id":"21","state":"queued"}]` | — | — | — |
| `POST /api/tasks/21/wait-on {"ids":["20"]}` | 200 `{"targets":[{"id":"20","title":"","state":"running",…}]}` (title blanked) | — | — | — |
| Plain filing `POST /api/tasks` | 201, whole record (as `7d863fa5` intends) | whole record | whole record | whole record |

### PR #366 — inherited `networkAccess: none`

Deployment default `none`, no per-task override on #20, #20 holds
`api:tasks:write`; #21 is queued.

| Call | External REST | External `/mcp` | Sandbox MCP | Sandbox `grain api` | In-grain acting header (`curl`) |
|---|---|---|---|---|---|
| `PATCH /api/tasks/21 {"networkAccess":"open"}` | 403 `an external agent cannot widen networkAccess past this deployment's default of "none"`, Δ 0 | 403, same | isError `an agent with networkAccess "none" cannot widen networkAccess`, Δ 0 | exit 1, same, Δ 0 | `{"NetworkAccess":"open"}` (case-folded): 403, Δ 0 |
| `POST /api/tasks {…,"networkAccess":"open"}` | 403, Δ 0 | 403 | isError, Δ 0 | exit 1, Δ 0 | — |
| Control: `PATCH … {"networkAccess":"none"}` | — | — | — | 200 `{"id":"21","networkAccess":"none","state":"queued"}` | — |

### PR #360 — held proposals

Parent #30 (`autoMerge`, grants covering the defaults, priority 5),
filing through the live `propose_task` (`grain mcpserver -interactive`):

| Proposal | State | Stored | Told to the run / note on the body |
|---|---|---|---|
| control (no settings) | **queued**, approval `auto-submit` | p5 | "filed approved -- your own task's permission covers it" |
| `keep_running: true` | **proposed** | keep_running=1 | "…does not cover it: it asks to keep running (keep_running)…"; body `_Filed for a person to approve: it asks to keep running…_` |
| `priority: 1000000` | **proposed** | p1000000 | "…it asks for priority 1000000, above the 5 its proposer may give it…"; same body note |
| `priority: 6` | **proposed** | p6 | same, "priority 6" |
| `priority: 5` | **queued** | p5 | covered |

Other channels:

- **Sandbox `grain task propose`** always sets `hold_for_approval`. `--keep-running` and
  `--priority 1000000` were held (proposed), but see **F1**: before the fix
  they were told "your permission would otherwise have queued it" and tagged
  `held-for-approval`, and the reason was missing.
- **REST filings by an agent** (`POST /api/tasks` with the acting header,
  via `grain api`, MCP `api_create_task`, external token): every
  filing is `proposed` whatever it asks, since `ProposedBy` is only set by
  `propose_task`. `keepRunning` is refused outright (403 / isError `durable
  lets a task jump the queue…: only a person may grant it`), as for `PATCH`.
  Naming `"proposedBy":"30"` in the body is refused for agents on every
  channel: 403 `an agent cannot name somebody else as the proposedBy of
  work it files`. That holds even when the run names its own task, though
  the wording ("somebody else") reads oddly then.
- **Inbox and approval (Playwright, chromium, 1440 and 390 px):** held #50
  (`keep_running`) and #51 (`priority 900`) are listed in the Inbox under
  "unapproved", each with an **approve** button. #51's row has a `p900`
  badge; #50's row shows nothing about keep_running (**F2**). Opening either
  task shows the body note `Filed for a person to approve: it asks to keep
  running (keep_running)…` / `…it asks for priority 900…`. Pressing
  **approve** on each Inbox row gave exactly what was asked (`sqlite3`):
  `50|queued|p5|keep_running=1|auto_merge=1|human:debian|gcp-key,gemini-key,self-debug`
  and `51|queued|p900|keep_running=0|auto_merge=1|human:debian|<same grants>`.
  The same held for #32/#33 approved from the task page (`keep_running=1`;
  `priority=1000000`).

### PR #398 / #332 — `droppedDefaults`

Deployment `defaultCapabilities` += `https://grain.dev/capabilities/grain#journal={}`;
capability policy `rules: [{capability: …#journal, deny: true}]`. (The
policy refuses a bare id: `{"capability":"self-debug","deny":true}` → 400
`is not a capability reference`.)

| Filer | `droppedDefaults` in the answer | Grants stored | Thread note author |
|---|---|---|---|
| Operator REST | `[{"grant":"…#journal","reason":"the capability policy denies …#journal to this task on acme/widgets (rule 1: deny …)"}]` | gcp-key, gemini-key, self-debug | `automation` / `grain` |
| External REST | same | same | `automation` / `grain` |
| External `/mcp` `api_create_task` | same (whole record) | same | `automation` / `grain` |
| Sandbox `grain api` | same | same | `automation` / `grain` |
| Sandbox MCP `api_create_task` | same | same | `automation` / `grain` |
| Sandbox MCP live `propose_task` | n/a: the tool's reply does not mention the drop (**F3**) | same | `automation` / `grain` |
| Browser **+ New task** (Playwright, 1440) | `POST /api/tasks` answered the same list | same | `automation` / `grain` |

The browser's `dropped-defaults-notice` toast was visible: "Task #49 was
filed without a default grant / https://grain.dev/capabilities/grain#journal:
the capability policy denies … (rule 1: deny …)".

### PR #328 — a refused locked binding stores nothing

Demo B (`configRepoOnly: true`):
`POST /api/capability-servers/bind-secret {"id":"https://grain.dev/capabilities/gcp","kind":"env","name":"GCP_MINTER_KEY","secret":"gcp-minter","value":"super-secret-key-material"}`
→ **409** `config_managed_externally`. The `secrets/` directory was unchanged
(md5 of every file identical, no `secrets.enc` created), the
`grain_config.mcp_servers` md5 was unchanged, and no file under the demo's
directory or the response body contains the value. The refusal's `change`
is `[]` (**F4**).

Control on Demo A (`settingsLocked: true` but `configRepoOnly: false`,
since the demo has no state repo to send the write to): the same bind is
200 and writes `secrets.enc` plus the `$secret:` reference. That is
correct, because the write is not refused there.

### PR #331 — `-grit-url`

| Command | Result |
|---|---|
| `grain daemon … -grit-url http://127.0.0.1:1` | **exit 2**, `grain daemon: -grit-url is experimental and refused without -grit-experimental: a remote grit server runs none of a repository's .grain/workflows … Drop -grit-…` |
| `… -grit-url http://127.0.0.1:1 -grit-experimental` | starts (still running at the 20 s timeout); logs `grain CI is wired to remote grit server at http://127.0.0.1:1 (EXPERIMENTAL, -grit-experimental: it runs no repository's workflows yet)` and `serving the UI/API on http://127.0.0.1:8832` |
| control: neither flag | starts |

Without a sandbox backend flag the daemon exits 2 on `no sandbox backend
selected` first, so `-disable-native-sandboxes` is needed to reach the grit
check.

### Audit 2026-11-01: findings fixed in this range

| Finding | Fix | Live confirmation |
|---|---|---|
| 1. `propose_task` queues `keep_running` / huge priority (Medium) | PR #360 (`480f7291`) | The #360 table above: both held live through MCP, the CLI and the end-of-run relay (tests). F1 fixes what they were told. |
| 2. Editing `scripts/github-ci-changes.sh` skips every `Tests` job (Medium) | `36f7944f` | In a worktree of this tree: a head commit adding `return 0` to `is_ignored_by_go_program()` and touching `pkg/ui/client.go`. The edited script alone still says `go_program=false go=false ui-e2e=false lint=true`, but `tests.yml`'s `guard` step, extracted from the workflow and run with `EVENT_NAME=pull_request BASE_SHA HEAD_SHA`, prints "the path filter or the Tests workflow changed; running every job" and writes `run_all=true` plus 22 job outputs `=true`. `Classify(scripts/github-ci-changes.sh)` is `ci-suppressions` (`RequiresHuman`), as `pkg/trustboundary` tests assert. |
| 3. A write removed from a standing session's declaration while the daemon is down stays (Low) | `b7036151` | Real daemon: the Director session was given writes `acme/widgets` and `acme/gadgets` (`PUT /api/console-sessions/director`) and synced (`task_write` holds both; `console_session_sync` row written). Then the daemon was stopped, `acme/gadgets` removed from `grain_config.console_sessions` and `tables/grain_config.json` (committed in the state repo) and the daemon restarted: after its first cycle `task_write` for task 1 is `acme/widgets` only. |

## Findings

### F1 — a held `keep_running` / high-priority proposal claims it could have been queued (fixed here)

**Reproduction** (Demo A, parent #30, which `AutoSubmits` an ordinary proposal):

```sh
GRAIN_SANDBOX_PROXY=http://127.0.0.1:<proxy> GRAIN_SANDBOX_TOKEN=<#30's run token> \
  grain task propose --title forever --body b --keep-running
# Proposed task #37 — held for human approval
# you asked to hold it (hold_for_approval); your permission would otherwise have queued it
sqlite3 grain.db "select keep_running, body from task where id='37'"   # + task_tag
# 1 | b --- grain: task 30, which proposed this, could have queued it on its own permission and asked
#     for it to wait for a person instead ... ; tag held-for-approval
```

The same happens for `--priority 1000000`, for a live `propose_task` with
`hold_for_approval: true`, and in the end-of-run relay
(`orchestrator.ProcessResult`). The Inbox row then says "held for approval
by 30". So the person approving is told the work was within the run's
permission, and nothing in the note or the row says it restarts forever.
That is the one fact #360's hold exists to show them. It is not an
escalation: the proposal is still held.

**Cause:** in `ui.Client.CreateTask` the `hold_for_approval` arm ran first
and cleared `autoSubmit`, so the person-only arm (and the change-target
arm) never added its note, and `proposalApprovalNote` reported "held". The
change-target arm also stored its note in `heldNote`, which tagged the
proposal held and told the run it had asked to hold it when it had not. In
`finish.go` the held case fired whenever `AutoSubmits` was true, and
`AutoSubmits` ignores keep_running and priority.

**Fix** (`pkg/ui/client.go`, `pkg/orchestrator/finish.go`): ask
`hold_for_approval` after grain's own holds, keep the change-target note in
its own variable, and in the relay mark a proposal "held by the run" only
when grain was not holding it anyway (`needsPersonWhy == ""`). No arm queues
anything it did not before: each still only clears
`autoSubmit`/`sessionQueued`/`Approved`. Regression tests:
`pkg/ui` `TestHeldProposalAskingForAPersonOnlySettingSaysWhyItWaits` and
`pkg/orchestrator` `TestProcessResultHeldKeepRunningIsNotMarkedHeldByTheRun`.
Both fail on `cc939397` and pass with the fix.

**Live after the fix** (Demo C): `grain task propose --keep-running` →
"you asked to hold it (hold_for_approval), and your permission would not
have covered it anyway: it asks to keep running (keep_running)…"; stored
body `_Filed for a person to approve: it asks to keep running…_`, no
`held-for-approval` tag. `--priority 1000000` reads the same way with the
priority. A plain `grain task propose` keeps the held note and tag, as it
should.

`finish.go` is a trust-boundary path, so this needs a security review before it merges.

### F2 — the Inbox row of a held `keep_running` proposal does not say so (follow-up)

Held #50 (`keep_running`) is listed as "unapproved" with an **approve**
button and no keep_running marker. A priority has a `p900` badge, but
keep_running has none. The reason is only on the task page. A person
approving from the Inbox gives a never-ending task without seeing that.
UI work (badge plus screenshots), filed rather than done here.

### F3 — live `propose_task` does not tell the run a default was dropped (follow-up)

Every REST filer gets `droppedDefaults`, but `propose_task`'s reply ("Proposed
task #48 is visible now … filed approved") does not mention that the
policy left `…#journal` off. The thread note says it, but the run reads its
tool result, not the thread.

### F4 — a refused bind-secret in external-config-repo mode gives no edit (follow-up)

On Demo B the 409's `change` is `[]` and `patch` is null, so the operator is
told to "edit the grain_config entries this request would change" without
the `$secret:` reference to add. `PUT /api/settings` on the same demo returns
one change, and the internal locked mode (`TestBindingASecretWhileLockedStoresNothing`)
returns the `$secret:` patch. Nothing is stored, so this is not a security
issue.

## Tests

- `go test ./tests/security/... ./pkg/ui/...` with the fix: `ok tests/security 21.9s`, `ok pkg/ui 68.3s`.
- `go test ./pkg/orchestrator/ -run 'TestProcessResult(Held|Holds|Hold|Files)'`: pass.

## Not run

- The non-interactive end-of-run `propose_task` relay was not run live
  (`grain demo` has no orchestrator finish). It is covered by
  `pkg/orchestrator` tests, including the new one.
- No Docker/Granule run host, and no real agent backend: the sandbox
  channels are the real `grain mcpserver` and the real git proxy over a
  recorded run, not an agent in a VM.
- Screenshots: Playwright captures were taken to check the Inbox and toast
  states, but this branch changes no UI, so no contact sheet is attached.
