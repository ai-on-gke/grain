# Walkthrough: blocker task-ID normalization and Claude Code CLI 2.1.286 (candidate)

Plan: `docs/test-plans/2026-10-01.md` §8. Task: grain/task-1417. Run on 2026-10-01, 21:23–22:10 UTC.

## Environment

| | |
|---|---|
| Branch / SHA under test | `candidate` @ `2d8b11521` (contains `0472c7b1f` and `bef53864f`, checked with `git branch -r --contains`) |
| Go | go1.26.6 linux/amd64 |
| Store backends | SQLite (the `grain demo` embedded store, and `withStore` in `tests/e2e`). PostgreSQL 15 (Debian `postgresql` package, started embedded by `postgrestest` via `postgres.EnsureRunning`) |
| UI | production bundle (`cd ui && npm ci && npm run build` → `pkg/ui/static`) embedded in a fresh `go build ./cmd/grain` |
| Browser | Playwright Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Claude Code CLI | `2.1.286 (Claude Code)`, installed with the Dockerfile's own recipe (`claude.ai/install.sh 2.1.286`; installer sha256 `3a68d340…a944`) into a private `HOME`, and again inside a `debian:bookworm-slim` container (see step 5) |

### Stubs (external boundaries only)

- **GitHub**: `githubsim` behind the e2e harness's smart-HTTP git host (`newProposalRig`), only for the e2e test. The demo walkthrough doesn't touch GitHub.
- **Model vendor**: an in-process stub of Anthropic's `/v1/messages` (`stubMessagesAPI` in `pkg/agent/claude/live_resume_stub_test.go`), reached through `ANTHROPIC_BASE_URL`. A dummy `ANTHROPIC_API_KEY` is set and non-essential traffic is disabled.
- **Scripted agent**: `antigravity.NewForTest` steps in the e2e test. In the demo walkthrough, a Python MCP client speaks JSON-RPC to a real `grain mcpserver` process.
- **Dangling link rows**: inserted straight into the demo's `grain.db` (`INSERT INTO task_link …`), because no filing path will write one any more (finding 3).

Grain's own API, UI, MCP server, orchestrator and dispatcher were not mocked.

## Steps, expected vs observed

### 1–2. Mixed-form `propose_task` → links, supersede, blocked until done, then dispatchable

**Demo walkthrough (live filing path).** I ran `grain demo -addr 127.0.0.1:18420 -open=false`. The demo seeds tasks 1–12, so I created the four targets as 13–16 with `grain create`. Parent task 17 was approved. A scripted MCP client then ran `grain mcpserver -sandbox-root … -server http://127.0.0.1:18420 -task 17 -interactive` (the daemon's live `daemonProposalFiler`) and called:

```json
{"title":"s8 proposal mixed forms","depends_on":["grain/task-13","#14","http://127.0.0.1:18420/tasks/15"],"supersedes":"grain/task-16"}
```

| Check | Expected | Observed |
|---|---|---|
| Tool answer | filed | `Proposed task #18 is visible now … Task 16 is closed as superseded by it.` |
| `dependsOn` of 18 | `[13 14 15]` | `['13', '14', '15']`, `supersedes: '16'` |
| Task 16 | closed, superseded by 18 | `state: closed`, `supersededBy: '18'` |
| 18 after `grain approve 18` | queued, blocked | `queued blocked=True blockedBy=['13','14','15']` |
| After `grain close 13`, `14`, `15`, one at a time | blocked until the last | `['14','15']` → `['15']` → `blocked=False` |
| `task_ready` view | contains 18 | `[('2',), ('12',), ('17',), ('18',)]` |

**E2E test (end-of-run relay path + real dispatcher).** `tests/e2e/blocker_id_normalization_walkthrough_test.go` (new). It dispatches a parent through `dispatch.Cycle`. A scripted antigravity run calls `propose_task` with `grain/task-N`, `#N` and `https://grain.example/tasks/N` plus `supersedes: grain/task-N`, and the test relays it with `orchestrator.ProcessResult`. It asserts the links, the closed superseded task, and that the approved proposal stays out of `task_ready` (and `model.IsBlocked`) until each of the three blockers is closed. It then asserts that `dispatch.Cycle` dispatches the proposal. It runs on **SQLite and Postgres**. Both pass:

```
--- PASS: TestBlockerIDNormalizationWalkthrough/sqlite (4.16s)
--- PASS: TestBlockerIDNormalizationWalkthrough/postgres (7.74s)
```

I closed the blockers to finish them, which is what releases a link. The `task_blocked` view reads only `closed_at`, so it makes no difference whether a task is completed through a merged PR or closed. There is no separate "reconciler" in the demo.

### 3. Dependency on non-existent `99999`

| Path | Expected (plan) | Observed |
|---|---|---|
| Live `propose_task` (`-interactive` mcpserver) with `depends_on: ["99999"]` | stays blocked | **Refused outright**: `Could not file proposal: depends on unknown task 99999` (`isError: true`). No task is filed. |
| End-of-run relay (`ProcessResult`) | stays blocked | Filed **with no link**. `99999` is written into the body for the approver (`proposedDependencies`' existing design). Asserted in the e2e test. |
| `POST /api/tasks` with `dependsOn: ["99999"]` | — | `{"error":"depends on unknown task 99999"}` |
| `POST /api/tasks/19/depends-on {"id":"99999"}` | — | `depends on unknown task 99999` |
| An existing `task_link` row targeting `99999` (inserted into SQLite) | blocked, not failed open | `task_blocked` = 1, not in `task_ready`, API `blocked=True blockedBy=['99999']` |
| Same on Postgres (e2e test, row written with `PutTask`) | blocked | `OpenBlockers = 1`, not in `Ready`. Detaching it as `#99999` unblocks it. |

**Task pane (Playwright, production bundle, second demo on :18421).** Task 14 depends on 13 and on a dangling `99999` row:

```
pane before: {"blocked":true,"deps":["13 s8 dep target","99999 (open)"]}   # after 13 was closed
pane after ×: {"blocked":false,"deps":["13 s8 dep target"]}
API:          queued blocked=False dependsOn=['13']
```

The blocker shows as a warning chip, `99999 (open)`, with `title="Remove dependency on 99999"`. Clicking its × removes it, so a person can clear a dangling blocker from the pane. Before that, I also checked the pane with both blockers open: `13 s8 dep target (open)`, `99999 (open)`.

Note for anyone repeating this: an unauthenticated browser gets the demo's "create account / log in" dialog over the pane. The script registers (or logs in as) `s8tester` first.

### 4. CLI `grain update <id> -depends-on '#2'` and the API forms

- **CLI: not possible.** `grain update` has no `-depends-on` flag on `candidate` (`grain update -h` lists only -auto-merge, -base, -body, -field, -issue, -read, -repo, -review-template, -team, -title, -write). `grain create` has none either, and `main` doesn't have one. No CLI verb attaches a dependency. See finding 2.
- **API (`POST /api/tasks/{id}/depends-on`) on task 19:**

  | body | resulting `dependsOn` |
  |---|---|
  | `{"id":"http://127.0.0.1:18420/tasks/3","attach":true}` | `['2','3']` |
  | `{"id":"task-6","attach":true}` | `['2','3','6']` |
  | `{"id":"#7","attach":true}` (twice) | `['2','3','6','7']` (no duplicate) |
  | `{"id":"grain/task-2","attach":false}` | `['3','6','7']` |
  | `{"id":"99999","attach":true}` | error `depends on unknown task 99999` |
  | `{"id":"task-19","attach":true}` | error `a task cannot depend on itself` |

- **`POST /api/tasks`** with `dependsOn: ["http://…/tasks/2","task-11","grain/task-1","#4"]` gave `['1','11','2','4']`.
- Live `propose_task` with `depends_on: ["task-2"]` filed task 19 depending on 2.
- The e2e test also covers `SetDependency` with URL, `task-N`, `grain/task-N` and `#N`, plus a `task-99999` refusal, on both backends.

### 5. Claude Code CLI 2.1.286

- `claude --version` gives `2.1.286 (Claude Code)`, both from the host install and inside a `debian:bookworm-slim` container built by running the Dockerfile's own `ARG CLAUDE_CLI_VERSION` / install `RUN` step verbatim. That step ends with its own `test "$(claude --version …)" = "$CLAUDE_CLI_VERSION"`. I did not build the full grain image. It takes several minutes and repeats `image.yml`. The step that installs and asserts the CLI is the one that ran.
- **Stubbed-vendor runs against the real 2.1.286 binary** (`GRAIN_LIVE_AGENT_TEST=1`):

  ```
  --- PASS: TestLiveClaudeResumeReplaysTheEarlierConversation (5.17s)
  --- PASS: TestLiveClaudeResumesASessionTheProviderRefused (2.86s)
  --- PASS: TestLiveClaudeRetryResumesTheRunAfterARefusal (2.70s)
  --- PASS: TestLiveClaudeRefusesASessionItDoesNotHave (2.14s)
  --- PASS: TestLiveClaudeCarriesAProposeTaskCallThroughGrainsMCPServer (2.33s)   # new
  --- FAIL: TestLiveClaudeResumesASessionAcrossExecs (0.00s)  # needs CLAUDE_CODE_OAUTH_TOKEN; none here (expected)
  ```

  The new probe drives `Framework.Run` with the production argv (`-p --tools '' --mcp-config … --strict-mcp-config --allowedTools mcp__grain-sandbox__… --output-format stream-json`). The stub model answers the first turn with a `tool_use` of `mcp__grain-sandbox__propose_task` carrying `depends_on: ["grain/task-1","#2","https://grain.example/tasks/3"]` and `supersedes: "grain/task-4"`. The probe checks three things: the call reached `grain mcpserver`, its answer went back to the model as a `tool_result` (the `proposed task(s) so far this run` text), and the transcript records the call as bare `propose_task` with the arguments intact. `ProcessResult` relays proposals from those arguments.
- **Hooks:** the Claude framework installs no hooks. Hooks (`hooks.json`, `grain agy-tool-hook`) belong to antigravity only. Claude's containment is `--tools ''` plus `--strict-mcp-config` plus `--allowedTools`, and the probe above runs under exactly that, so there was no Claude hook to exercise. The plan's "hooks" wording doesn't apply to this framework.

## Findings

1. **Bug (fixed here): a GitHub URL was read as a grain task ID.** `model.NormalizeTaskID` took the last path segment of *any* URL. A `depends_on` or `supersedes` naming a GitHub pull request or issue therefore resolved to whichever grain task shared the number. Supersede actually *closed* that unrelated task.
   - Deterministic reproduction on `2d8b11521`: `NormalizeTaskID("https://github.com/acme/widgets/pull/12") == "12"` (also `/issues/7` → `"7"`, `http://ui/tasks/12/runs` → `"runs"`).
   - End to end: with task 12 queued, a relayed `propose_task {depends_on:["https://github.com/acme/widgets/pull/12"], supersedes:"https://github.com/acme/widgets/issues/12"}` filed a depends-on **and** a supersedes link to 12, and **closed task 12** (`TestProcessResultDoesNotReadAGitHubURLAsATaskID` fails this way without the fix). In the UI, `SetDependency(…, "https://github.com/acme/widgets/pull/1")` attached task 1.
   - Fix (`pkg/model/task.go`): a URL yields an ID only from what follows a `tasks`, `task` or `t` segment of its fragment or path. A sub-page like `/tasks/12/runs` still gives 12. Any other URL is returned whole, so it resolves to no task and is noted (relay) or refused (UI/API) like any other unknown ID. Every case already in `TestNormalizeTaskID` still passes.
   - Regression tests: `pkg/model/task_test.go`, `pkg/orchestrator/finish_test.go`, `pkg/ui/client_test.go`. All three fail on the old code and pass with the fix.
2. **Gap (not a bug in the change): no CLI way to set a dependency.** The plan's `grain update <id> -depends-on '#2'` doesn't exist on `candidate` or `main`. Dependencies can only be set through the API/UI (`POST /api/tasks/{id}/depends-on`, `dependsOn` on create) or `propose_task`. This is a feature, so I'm proposing it as a follow-up rather than adding it here.
3. **Observation: "a proposal depending on 99999 stays blocked" can't happen through any filing path.** Every path refuses an unknown ID before a link exists. The live (interactive) filer fails the whole `propose_task` call. The end-of-run relay files the proposal unlinked and writes the ID into its body. The UI/API returns a validation error. None of these fail open: the agent is told, or the approver reads the note. The `LEFT JOIN` in `task_blocked` only matters for link rows that already exist and point at nothing. I verified it does keep those blocked on both backends, and that the pane shows them and lets a person remove them. The two `propose_task` paths still treat the same argument differently (refuse vs. file-with-note). That predates this change and I'm only noting it here.
4. **Test-harness note:** `tests/e2e`'s `fileIssue` writes explicit IDs that bypass `task_sequence`. A proposal filed afterwards is allocated `"1"` again and replaces the existing task. Production allocates every ID through `NewTaskID`, so this only affects tests. The new walkthrough files everything through `ui.Client.CreateTask` for that reason.

## Changes on this branch

- `fix(model)`: `NormalizeTaskID` URL handling, with regression tests in `pkg/model`, `pkg/orchestrator`, `pkg/ui`.
- `test(e2e)`: `TestBlockerIDNormalizationWalkthrough` (SQLite + Postgres).
- `test(claude)`: the stub messages API can answer with a `tool_use`, and `TestLiveClaudeCarriesAProposeTaskCallThroughGrainsMCPServer` uses it. It is gated like its neighbours on `GRAIN_LIVE_AGENT_TEST` and a `claude` on `PATH`.

## Skipped, and why

- **`grain update -depends-on`**: the flag doesn't exist (finding 2).
- **Full grain image build**: I ran only the Dockerfile's Claude install step in a container. `image.yml` builds the full image in CI.
- **Credentialed Claude run** (`TestLiveClaudeResumesASessionAcrossExecs`): no `CLAUDE_CODE_OAUTH_TOKEN` in this sandbox. The vendor was stubbed as the task asked.
- **Postgres-backed `grain demo` + Playwright**: `grain demo` only runs on SQLite. The Postgres half of the blocked-view check runs at the store/dispatcher level in the e2e test. The UI reads the same `blocked`/`blockedBy` fields off the API regardless of backend.

## Reproducing

```sh
go build -o /tmp/grain ./cmd/grain     # after: cd ui && npm ci && npm run build
/tmp/grain demo -addr 127.0.0.1:18420 -open=false &
export GRAIN_SERVER=http://127.0.0.1:18420
for i in 1 2 3 4; do grain create -title "s8 dep target $i" -repo acme/widgets; done   # → 13..16
grain create -title "s8 parent run" -repo acme/widgets && grain approve 17
# MCP client: spawn `grain mcpserver -sandbox-root /tmp/sbx -server $GRAIN_SERVER -task 17 -interactive`,
# initialize, then tools/call propose_task with the arguments shown in step 1.
curl -XPOST $GRAIN_SERVER/api/tasks/19/depends-on -d '{"id":"task-6","attach":true}'

PATH=/usr/lib/postgresql/15/bin:$PATH go test ./tests/e2e -run TestBlockerIDNormalizationWalkthrough -v
PATH=$HOME/.local/bin:$PATH GRAIN_LIVE_AGENT_TEST=1 go test ./pkg/agent/claude -run 'TestLiveClaude(Carries|Retry|Resume|Refuses)' -v
```
