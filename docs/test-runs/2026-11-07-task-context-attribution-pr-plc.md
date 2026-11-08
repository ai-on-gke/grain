# Walkthrough: task context, attribution, external refs, review holds, PR adoption & `grain-plc` (§7 of `docs/test-plans/2026-11-07.md`)

- **Date:** 2026-11-08, 01:22–02:00 UTC
- **Task:** [Task #3491](/tasks/3491), branch `grain/098073e4/task-3491-walkthrough-committed-context`, base `main`
- **Commit tested:** `af038c95` (`origin/main` when the run started), then rebuilt and re-run with the fixes below. `main` was merged in at `0733b800` partway through; nothing in it touched these areas.
- **Environment:**
  - Debian sandbox (`linux/amd64`, 7 CPUs, 30 GB RAM), Go `go1.26.10`, Node.js `v20.19.0`, git + `git-http-backend`.
  - `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`. That binary embeds the SPA built from `HEAD`.
  - `cd grain-plc && go test -race ./... && go build -o /home/debian/bin/ ./cmd/...`: all 8 packages pass, before and after the fixes.
  - Headless Chromium through Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- **Artifacts** (in `docs/test-runs/2026-11-07-task-context-attribution-pr-plc/`):
  - `walkthrough.go` (`//go:build ignore`) is the driver for items 1–4: `go run ./docs/test-runs/2026-11-07-task-context-attribution-pr-plc/walkthrough.go -mode=all`.
  - `plc-live.sh` covers item 5.
  - Per-item check transcripts: `item1-committed-context.json`, `item2-external-ref.json`, `item3-owner-proposer.json`, `item4-pr-adoption-human-review.json`, `item5-plc-live.log`.
  - `walkthrough-run.log` is the full run, with 62 of 62 checks passing.
  - `contact-sheet-demo-human-review.png` and `screenshots/` hold `grain demo -human-review -github-sim` shots.

## What was real, what was stubbed

These parts were real:
- SQLite store (`pkg/model/sqlite`)
- bare git repositories behind `git-http-backend`
- `githubsim.GitUpstream` (token-scoped upstream)
- the real scoped git proxy (`gitproxy.BuildProxy` + `gitproxy.NewHandler`), with per-sandbox tokens from `gitproxy.SandboxTokenStore`
- the real `orchestrator.RunCycle` dispatching into `orchestrator.NewHostSandboxes`. It ran prepareCheckout, the committed-context clone and verification, prompt building, finish, PR opening, the merge queue and SyncPullRequests.
- the real REST API from `ui.NewServerWithClient`, behind the access code and external-agent tokens
- the freshly built `grain` CLI (`grain show`, `grain update -owner`, `grain list -owner me`, `grain pr review|adopt|status`), run against that server
- `grain demo -github-sim -human-review`, `plc-studio` and `plc-chat-gateway` as separate processes

Two boundaries were stubbed:

1. **The GitHub REST API** was `githubsim.Sim` over the same bare repositories. It answered PR creation, checks, merges and collaborator reads. Committed-context `@main` resolution went through a `ui.Config.RepoRevs` that runs `git rev-parse` on the bare repo. That is the in-process stand-in for the daemon's `repoRevGate`, which asks GitHub's refs API the same question.
2. **The model vendor** was `orchestrator.StaticFramework` with a scripted `agent.Provider`. It inspected the real sandbox filesystem, tried pushes through the proxy, and returned `propose_task` / `comment_on_issue` tool calls for the orchestrator to process.

---

## 1. Committed context end-to-end (`item1-committed-context.json`, 18/18)

**Setup:**
- `acme/app` and `acme/specs` are behind the scoped proxy.
- `acme/specs@main` = `SHA1` holds `architecture.md` (v1, approved) and `plc/042/{spec.md,context.yml}`. The manifest has a title, a description and one file with `kind`, `title` and `source_url`.

**Steps and results:**

| # | Check | Result |
|---|---|---|
| 1a | `POST /api/tasks {designs:["acme/specs:architecture.md@main"]}` with `autoGrantContextReads` empty and no `-read` | **Refused, HTTP 400**: `committed context "acme/specs:architecture.md@main" is in acme/specs (task target: acme/app): grant read access with -read acme/specs, or add acme/specs to autoGrantContextReads` |
| 1b | Same, with `reads:["acme/specs"]` | Filed (201) |
| 1c | `PUT /api/settings {autoGrantContextReads:["acme/specs"]}` | Stored (schema 112 column) |
| 1d–1e | File again without `-read` | `reads=[acme/specs]` auto-granted. Both designs pinned to `acme/specs:…@SHA1` at filing |
| 1f–1h | Push `SHA2` (an "UNAPPROVED DRAFT") to `acme/specs` `main`, **then** dispatch | The sandbox's `repos/acme/specs` is detached at **SHA1** and holds the v1 text. The prompt names SHA1 and the manifest title, and never SHA2 |
| 1i | Tamper: inside the sandbox, commit and `git push origin` from the context clone | Refused: push URL is `grain-read-only` |
| 1j | Tamper: push straight to the proxy URL with the sandbox's own credentials | **Refused by the proxy**: `remote: acme/specs is read-only for this sandbox: it may fetch it but not push to it` |
| 1k | `acme/specs` `main` afterwards | Still `SHA2`, untouched |
| 1l | Manifest discovered in the sandbox | Persisted on the task (`title`, `description`, `files[0]`) |
| 1m–1o | The run's `propose_task` child | Inherits both pinned refs, `-read acme/specs` **and** the manifest |
| 1p | `GET /api/tasks/{id}` (what Task detail renders) | `context[]` carries `repo`, `rev=SHA1`, `placement:"read"` and the manifest |
| 1p′ | `grain show` | **Bug F1, fixed.** It printed the refs but not the manifest (details below). After the fix it prints the manifest under the `context:` line |
| 1q | Pin a full SHA that does not exist in `acme/specs` | Setup fails with `error: Server does not allow request for unadvertised object …`. No other revision is used (see observation O1) |

**Playwright** (`committed-context.spec.js`, `external-ref.spec.js`, `task-owner.spec.js`) at desktop and phone, light and dark: **10/10 passed** on this build.

## 2. `externalRef` inheritance & cross-repo isolation (`item2-external-ref.json`, 13/13)

**Chain:**
- root (`acme/core`, `externalRef=PROJ-42`, filed over REST)
- → child filed in `acme/billing` with `origin: task:<root>`
- → superseding task (`LinkSupersedes`) in `acme/billing`
- → two-hop proposal (`LinkProposedBy` the superseder)

All four carry `PROJ-42`.

| View | `GET /api/refs/PROJ-42/summary` | `GET /api/tasks?externalRef=PROJ-42` |
|---|---|---|
| Operator | `total:4, byState:{proposed:3, queued:1}` | 4 tasks |
| Token scoped to `acme/core` (`tasks:read,tasks:write`) | `total:1, byState:{queued:1}`, and the body never mentions billing | only the root |
| Same token, `?repo=acme/billing` | **403** `holds tasks:read, but not on acme/billing` | **403** |
| Same token, `GET /api/tasks/<billing task>` | **403** | |
| Same token, unknown ref `UNKNOWN-1` | `total:0` (200), so the response gives no hint whether a ref exists | |

**Inheritance guard:** the scoped token filed a task in `acme/core` with `origin: task:<billing superseder>`. It got **no** `externalRef`, so the ref did not leak across repos through inheritance.

## 3. Task `owner` & `proposer` (`item3-owner-proposer.json`, 16/16)

- **Default owner:**
  - A task filed with the operator's access code gets `owner=operator@example.com`.
  - A task filed with Alice's external-agent token (`owner: alice@example.com`) gets `owner=alice@example.com`.
- **Proposal through a real dispatch:** Alice's task ran, and its `propose_task` child got:
  - `owner=alice@example.com`
  - `proposer = {agent 2 (sandbox) on behalf of alice@example.com, TaskID 2, RunID 2-1}`
  - timeline label `task #2 (run #1) on behalf of alice@example.com`, which is what renders as "Proposed by …"
- **`owner:me`:**
  - Before any transfer: as Alice it lists `[2, 4]`, as Bob it lists nothing.
  - After the UI's `PATCH {owner: bob@example.com}`: Alice gets `[4]`, Bob gets `[2]`.
  - `?owner=bob@example.com` returns `[2]`.
- **CLI:**
  - `grain update 2 -owner carol@example.com` transfers the task.
  - `grain list -owner me` as Alice shows only her proposal. As Bob it is empty.
  - `grain show` prints `owner: carol@example.com`.
- **Audit:** two `owner_changed` transitions, `alice → bob` and `bob → carol`, each attributed to `operator (access code)`.

## 4. PR adoption & `request_human_review` (`item4-pr-adoption-human-review.json`, 15/15)

**PR adoption** (external contributor PR `acme/widgets#41`, green, over githubsim):

| # | Step | Result |
|---|---|---|
| 4a | `grain pr review acme/widgets#41` | `task 1: review of acme/widgets#41 (queued)` |
| 4b | `grain pr adopt acme/widgets#41` (defaults to review-only) | `task 2 adopted …`, `mode: review-only` |
| 4c | Three reconcile cycles later | The PR branch head, `main` and the PR's state are all unchanged: no push and no merge |
| 4d | `grain pr status` | `shepherd: task 2, review-only, queued`, `merge queue: held: review`, plus the three pending reviews |
| 4e | Upgrade to `finish` with a `tasks:write` + `merge:write` token (no `tasks:admin`) | **403**: `upgrading approved shepherd 2 … to finish needs an approved request (a person, or tasks:admin on acme/widgets)` |
| 4f | `grain pr adopt -mode finish` as the operator | `task 2 upgraded …`, `mode: finish` |

**Human-review hold:**

1. An `auto_merge: true` task was dispatched for real. Its run pushed through the scoped proxy and grain opened PR `#9001` while CI was in progress.
2. A review task (`Origin.Reason=review`, `proposed-by` the task) called `POST /api/tasks/{review}/human-review`, which is the door the `request_human_review` tool uses, with category `security`. The review then closed.
3. CI turned green.

| # | Check | Result |
|---|---|---|
| 4h | Three cycles with green checks and auto-merge filed | **PR stays open and unmerged** |
| 4i | Task detail payload | `state: awaiting_submit`, `humanReviewHold: [{source: requested, category: security, reason: "Adds an unauthenticated cache endpoint", reviewTask: …}]` |
| 4j | Task list payload | Carries `humanReviewHold` (the list badge and `humanReview:` filter read it) |
| 4k | `grain show` | Timeline shows `human_review_requested … category security; review task …` and `auto_merge_off … (held for a person's review)` |
| 4l | `GET /api/inbox` | `{"kind":"merge-held","task":…,"pr":"acme/widgets#9001","reason":"held for a person's review: Adds an unauthenticated cache endpoint"}` |
| 4m | Release with an external-agent token | **403** `an agent cannot release a human-review hold: only a person's Submit or Release does` |
| 4n–4o | Operator `POST …/human-review/release`, then cycles | Auto-merge comes back on and **PR #9001 merges** |

`grain demo -human-review -github-sim` (task 20, "Default new workspaces to the paid tier") shows the same hold on its task page and in the Inbox. That is `contact-sheet-demo-human-review.png`, at 1440 and 390 px in light and dark. No UI code changed in this task.

## 5. `grain-plc` against a live grain (`item5-plc-live.log`)

`plc-live.sh` starts these, then files and polls through the studio:
- `grain demo -github-sim -human-review` on `127.0.0.1:4292`
- `plc-studio` on `:4287`
- `plc-chat-gateway -watch-sse` on `:4288`

Before the fixes **nothing worked** (F2–F5 below). After them:
- `POST /api/studio/tasks` with `externalRef: PLC-7` and `context: [{path: .grain/context/planner/plc-042}]` files task 22. Its description, `externalRef`, design and `filedVia: plc-studio` are all stored; `grain show` prints `provenance: filed from plc-studio`.
- `GET /api/studio/tasks?externalRef=PLC-7` returns it.
- `GET /api/studio/refs/PLC-7/summary` returns `counts.total=1, active=1`, with grain's own `byState` alongside.
- The gateway's SSE consumer connects with the token and decodes the new `task.state` events (`caches.sse.events_received.hits = 2`) after `grain approve 22`. Without a token it now says why on stderr and retries.

---

## Findings

| # | Finding | Status |
|---|---|---|
| F1 | **`grain show` dropped committed-context manifests.** `taskBlock` printed `context:` refs but never the `context.yml` manifest (title, description, file inventory with kind, title and source URL), though the Task detail page renders it and the prompt is built from it. | **Fixed** in `pkg/cli/taskprint.go`. Each manifest is now printed beneath its ref. Regression test: `TestTaskBlockRendersContextManifests` (`pkg/cli/taskprint_context_test.go`). |
| F2 | **`plc-studio` and `plc-chat-gateway` could not authenticate to any daemon.** `grainclient.Config.BearerToken` existed, but neither command set it, so every upstream call to a single-tenant daemon (including `grain demo`) failed with 401. | **Fixed**: `-grain-token` (`$GRAIN_TOKEN`) on both commands. |
| F3 | **`grainclient`'s wire types did not match grain's API.** `designs` are `[owner/name:]path[@rev]` strings, not objects, so any task with committed context failed to decode: filing reported an error *after* filing, and the `externalRef` list broke. The task text is `description`, not `body`, so studio-filed tasks lost their description. The ref summary is `{total, byState, pullRequests, openQuestions}`, not `{counts, tasks}`, so every rollup read as zero. | **Fixed** in `grain-plc/pkg/grainclient/wire.go`, which reads and writes grain's shapes and still accepts the package's own. Tests in `wire_test.go` pin the live payloads. |
| F4 | **`GET /api/events` events failed to decode, and its filters were ignored.** grain sends string `id`, `at`, `previousState`, `questionSummary`, `pullRequest` and `ciConclusion`. The first live event ended the gateway's stream with `cannot unmarshal string into … IntegrationEvent.id`. grain also has no `externalRef`/`taskId` query filter, so a caller asking for one ref got every task's events. | **Fixed**: `IntegrationEvent.UnmarshalJSON`, plus client-side `externalRef`/`taskId` filtering in `StreamEvents` and `PollEvents`. |
| F5 | **The chat gateway dropped `WatchSSE`'s error and never retried.** A gateway started before its daemon, or with a refused token, silently consumed nothing. | **Fixed**: `watchSSEForever` logs the failure and reconnects every 5s, resuming after the last event id. |
| O1 | A committed-context pin to a **full SHA that does not exist** in the context repo is accepted at filing (only symbolic refs are resolved). It fails only at dispatch, three setup-failed attempts later. | Not fixed here; filed as a follow-up to verify a full-SHA pin through `RepoRevs` at filing. |
| O2 | The plan says `plc-studio :4280` and `plc-chat-gateway :4281`. The real defaults are `:4285` / `:4286`. | Doc note only. |
| O3 | `plc-studio` accepts context paths only under `.grain/context/<source>/<slug>` (stricter than grain) and requires a commit SHA for `rev`. | Intended (`playbook.ValidateContextPath`). Noted. |
| O4 | `grainclient.CreateTaskRequest.HoldForApproval` has no grain counterpart (grain uses `approved`), so it is ignored on the wire. | Left as is; studio-filed tasks are proposed by default anyway. |
| O5 | A release restores auto-merge only when the change's trust-boundary classification was recorded (`restoreAfterHolds` needs `LinkTrustBoundaryCleared`). A PR seeded straight into the store without a run stays off after release. A real dispatch (item 4g–4o) restores it. | By design; recorded so the next harness does not mistake it for a bug. |
| O6 | The `auto_merge_off` timeline entry's detail includes the Go writer frame, e.g. `ui.(*Client).RequestHumanReview (human_review.go:127)`, and `grain show` prints it. | Intentional (`autoMergeWriter`, for debugging auto-merge flips). Noted. |

## Hygiene

- `go test ./tests/hygiene/... ./pkg/ports/... ./pkg/cli/...` passes.
- `cd grain-plc && go test -race ./...` passes. This PR touches `grain-plc/`, so `.github/workflows/plc-tests.yml` runs too.

## CI repair (attempt 2)

After the first push, this PR's `ui e2e` check was red. Three more failures showed up while fixing it:

- **`ui e2e`, `console-resting-notes-walkthrough.spec.js:92`, `count: 3` instead of `2`.** This was a real orchestrator race, not a flake in the spec. The console-session reconciler only consulted `LiveRuns` to decide whether a session was mid-exchange. A run's row is finished before `ProcessResult` posts its answer (the window `InFlight.Busy` covers), so a cycle landing in that window parked the session with `_Waiting for you._` *above* the answer. The next park then could not reuse it. Repro: `npx playwright test e2e/console-resting-notes-walkthrough.spec.js --repeat-each=15 --workers=4` failed 3 of 39 runs. The logged comments showed the resting note written before `Scripted answer 1.`, with the same timestamp. **Fixed** in `pkg/orchestrator/consolesessions.go`, which now also checks `deps.Runs.Busy`. `TestConsoleSessionIsNotParkedWhileItsResultIsStillBeingProcessed` fails without the fix. The spec then passed 60/60.
- **`installer`, `TestTheCLIWrapperTalksToTheDeploymentItInstalled`.** The job ran across 03:00 UTC, when the memory agent's system schedule fires (`systemScheduleTimeOfDay`), so the fresh deployment's task list held a scheduled task. **Fixed**: the check now lets tasks the deployment's own schedules filed through.
- **`run host`, `TestDaemonRestartKeepsAWarmSessionSandboxOnlyUnderItsRecordedKeys`.** Same minute: a system-scheduled task took the test daemon's only worker slot (`MaxWorkers: 1`), and `warm-session` was never dispatched. This is a time-of-day flake. A rerun away from 03:00 passes. Hardening it is a follow-up.
