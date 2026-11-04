# Test run — 2026-11-03 — operator attribution, `config_audit` and sticky `auto_merge: false`

Task 2709, walking §3 of [`docs/test-plans/2026-11-03.md`](../test-plans/2026-11-03.md) (PRs #436, #518, #504, #530, #447, #456) against a live deployment. It runs a real `grain demo` behind the simulated IAP from [`2026-11-02-iap-end-to-end.md`](2026-11-02-iap-end-to-end.md), so writes come from a verified `alice@example.com`. A real browser is used for the timeline.

**Result:** six defects were found and fixed here (F1–F6), each with a regression test. Six behaviours differ from the plan's wording on purpose (D1–D6). One check could not be walked live: the state-repository `git log` (see item 2).

Scripts and dumps are in [`2026-11-03-operator-attribution/`](2026-11-03-operator-attribution/):

- `env.sh`: iapsim and the demo.
- `matrix.sh`: items 1 and 5.
- `sticky.sh`: the live part of item 3.
- `upgrade.go`: item 4.
- `dump-*.json` and `actions.tsv`: every call's status and refusal.
- `shots/`: screenshots.

## Environment

| | |
|---|---|
| Base | `origin/main` `ac1f0e6d` (PR #458), schema 94 |
| Binary | `cd ui && npm ci && npm run build && cd .. && go build ./cmd/grain` (embedded UI). "Before" shots are of the base, "after" of this branch |
| OS / tools | Debian 12, Go 1.26.10, Node 20.19.0, Playwright Chromium (`/var/cache/playwright`); `sqlite3`, `jq` and `tesseract-ocr` installed with apt |
| Server | `grain demo -github-sim -fake-ci -secrets-store -reconcile 2s -as owner@example.com -ui-iap-audience … -ui-iap-issuer https://iap.test.local -ui-iap-keys-url http://127.0.0.1:18080/jwks -ui-allowed-principals user:alice@example.com,user:owner@example.com` on `127.0.0.1:4300` |
| Simulated IAP | `tests/iap/iapsim.go`: an issuer on `:18080` and a proxy on `:18443` that signs an ES256 assertion for the `X-Iapsim-Email` it is sent |
| Schema-85 store | `75331e14`'s `grain demo` (schema 85), killed with `SIGKILL` so its directory stays, then opened by this tree's store |

**Shared step 3 choice.** I used `grain demo`, not `consolerestserver` or a daemon. It is the only server in the tree that takes the `-ui-iap-*` flags together with `-github-sim`. `consolerestserver` has no IAP, and a daemon needs a GitHub-hosted state repository this sandbox cannot reach. `grain demo` runs no dispatch, so the parts of item 3 that need a security-review *run* go through the real orchestrator in process instead (item 3 below).

**Stubbed boundaries:**

- Google IAP (iapsim).
- GitHub (`githubsim`, through `-github-sim`, and in the orchestrator chain).
- CI (`-fake-ci`).
- The agent's sandbox. The agent source (d) is the call a run's API proxy makes: the access code plus `X-Grain-Acting-Task: 20` for a task holding `api:*` grants, which `agentAPIGate` authorizes and records as `{agent, 20, sandbox}`.

No real agent backend was used.

**Sources.** Each was driven by `curl`:

- (a) **iap**: alice through the proxy, with no access code.
- (b) **code**: the access code as a bearer token.
- (c) **loop**: loopback with no code.
- (d) **agent**: as described under stubbed boundaries.
- (e) **ext**: a `gma_` token named `ext1` (Settings → `externalAgentApi: true`).

## Item 1: the matrix, per source

`matrix.sh` does each action once per source, then dumps the store.

The actions are:

- file
- comment
- approve
- withdraw approval
- PATCH `autoMerge` off and on
- defer and undefer
- depends-on on and off
- pause and resume
- memory and memory document
- forged-author memory and release note
- repo settings
- pipeline pause and resume
- close

(c) loopback got **401 on every call** (D1).

Refusals that are policy, not defects:

- An agent may not turn auto-merge on.
- An agent may not act on its own task.
- An agent may not name another author (403).
- `ext1` holds no `repos:write` grant for repository settings.

### Attribution table (store rows, final build)

| Who acted | `task.origin_*` | `task_event` (`actor_*`) | `task_comment.author_*` | `config_audit` | `memory_document.updated_by_*` | `release_note` |
|---|---|---|---|---|---|---|
| (a) alice via IAP (task 21) | human / alice@example.com / **iap** | approval-withdrawn, auto-merge-off/on, deferred, undeferred, dependencies ×2, closed: all human / alice@example.com / iap | human / alice@example.com / iap | `repo:acme/widgets` human / alice@example.com / iap | human / alice@example.com / iap | human / alice@example.com / iap |
| (b) access code (task 22) | human / owner@example.com / **access-code** | the same kinds, all human / owner@example.com / access-code | human / owner@example.com / access-code | `settings` and `repo:acme/widgets`, human / owner@example.com / access-code | human / owner@example.com / access-code | forged author refused (D2) |
| (c) loopback, no code | — | — | — | — | — | — (401, D1) |
| (d) agent via the API gate (task 23) | agent / 20 / **sandbox** | approval-withdrawn, auto-merge-off (F5), deferred, undeferred, dependencies ×2, closed: agent / 20 / sandbox | agent / 20 / sandbox | `repo:acme/widgets` agent / 20 / sandbox | agent / 20 / sandbox | forged author refused (403) |
| (e) `gma_` token `ext1` (task 24) | agent / external:ext1 / **external-token** | the same kinds as (d), agent / external:ext1 / external-token | agent / external:ext1 / external-token | (403: not a person) | agent / external:ext1 / external-token | forged author refused (403) |

Notes:

- The `behalf_*` columns are empty on every row. No relay was involved (spec §2.4 has not landed).
- Approve leaves no `task_event`. It is the `approval_*` columns, which the withdrawal then clears.
- Pause and resume leave grain-authored comments (F1) and a `task_pause` row that is deleted on resume.
- `pipeline_pause` likewise keeps no row after its resume. Who paused and resumed is in the log only.
- `external_agent_token.created_by` is `human:owner@example.com` with no source column (F6).

Every row's kind, id and source matches who acted. The source is right everywhere, but before this branch it reached no person (F3, F4).

## Item 2: where people see it

| Surface | Before (base) | After (this branch) |
|---|---|---|
| **Task timeline** (`/tasks/22`, filed by the access code) | "by human owner@example.com · done"; "owner@example.com · human"; "Paused by owner@example.com."; "…was paused by the operator at…"; "edited · ui.(*Client).mutate (client.go:5029)" | "by operator (access code) · done"; "operator (access code) · human"; "Paused by operator (access code)."; "…was paused by operator (access code) at…"; "edited · ui.(*Client).UpdateTask (client.go:4523)" |
| **Task timeline** (`/tasks/23`, filed by the agent) | "by agent 20"; "Paused by 20."; "paused by the operator" | "by task 20 · done"; "Paused by task 20."; "paused by task 20 at" |
| **Pipeline change feed** (`GET /api/pipeline/changes`) | "by human owner@example.com", "by agent external:ext1: walkthrough" | "by operator (access code)", "by task 20: walkthrough", "by external agent ext1: walkthrough" |
| **Daemon log** | `settings saved by the access code: changed ExternalAgentAPI`; `alice@example.com (iap) changed repo:acme/widgets`; `task 20 (sandbox) changed repo:acme/widgets`; `issued credential id=1` (who issued it is not named) | the same, and `operator (access code) (access-code) issued credential id=1` (F6) |
| **"Activity"** | No page or view called Activity exists on `main` (D5). The task pane's timeline and the change feed are what a person reads | |
| **State-repo `git log`** | Not walked live: `grain demo` has no state repository, and a daemon's needs GitHub (D6). `writeCommitNote` names the same writer as the log line above. `TestSettingsSaveByAVerifiedCallerRecordsWhoSavedIt` checks the commit for an IAP caller | |

**The new `task_event` kinds are not steps.** On task 22 the timeline's steps are:

1. filed
2. auto-merge off
3. auto-merge on
4. Closed

`approval-withdrawn`, `deferred`, `undeferred`, `dependencies` and `secret-set` appear only in the change feed, and nothing renders as a second close.

**No secret value anywhere.** Alice answered a pending secret request (`walkthrough-token`) with `s3cr3t-walkthrough-value`. The `secret-set` event's detail is the name `walkthrough-token`. The value occurs 0 times in each of these:

- the demo log
- a full `sqlite3 .dump` of the store
- `GET /api/tasks/21`
- the change feed

The committed dumps carry neither the access code nor a `gma_` token (checked with `grep`). The token table is dumped without its hash.

**Screenshots.** Pages: `/tasks/22` and `/tasks/23`, full page, at 1440 and 390 px, light and dark, for the base and for this branch. This sandbox's file reader does not render PNGs and there is no Gemini key, so each shot was read back with `tesseract`; the lines quoted above are its output. Files: `shots/before-contact-sheet.png`, `shots/after-contact-sheet.png`, and one desktop-light shot of task 22 and one phone-dark shot of task 23 for each.

## Item 3: sticky `auto_merge: false`

**Live (`sticky.sh`, final build):**

1. Agent 20 files a proposal the way `propose_task`'s `auto_merge: false` does (`declineAutoMerge: true`), as task 25.
2. Its PATCH to `autoMerge: true` gets **403**.
3. An approval with the access code leaves `autoMerge: false`.
4. Alice's Submit through IAP gives `autoMerge: true`, with `auto-merge-on` and then `submitted`, both `human / alice@example.com / iap` (`reason: submitted`, writer `ui.(*Client).submit`).
5. The `auto-merge-declined` link is lifted, and `human-submitted` is pinned.

**Hold → release → carried clearance → restart → Submit.** This needs a security-review run, which `grain demo` never dispatches. `TestDeclinedAutoMergeOutlastsHoldReleaseCarryAndRestartUntilSubmit` (`pkg/orchestrator`) walks the whole chain through the real `ProcessResult` / `SyncSecurityHolds` / `SyncPullRequests`, `githubsim`, an on-disk sqlite store and `ui.Client`:

1. A push to `pkg/ui/server.go` is held, and a review is filed.
2. Alice PATCHes `autoMerge: false`.
3. The clean review releases the hold, and auto-merge stays off.
4. A `docs/notes.md` push carries the clearance: no second review, and no merge.
5. The store is closed and reopened from its file (`Store.Init`, as a restart does), and it still reads off and declined. A further sync does not merge.
6. Alice's Submit records `submitted` and `auto-merge-on` as `alice@example.com` / `iap`, and the next sync merges.

Two findings came out of this:

- **F5:** alice's PATCH in step 2 had recorded nothing.
- **D4:** no `auto-merge-kept-off` appears in this chain.

## Item 4: old rows after an upgrade (85 → 94)

`75331e14`'s demo (schema 85) wrote the following, all with the access code: a task, a comment, an approval, a PATCH to false, a pause, a memory and a close. `upgrade.go` opened that directory with this tree's store, which migrated it to **94**. The plan says 93, but `main` has since added 94.

- **Store** (`dump-upgrade-85-to-94.json`): `origin_actor_*`, `approval_actor_*`, the `task_event` `closed` row, the comment and both memories read `human / owner@example.com` with **an empty source**.
- **API** (`upgrade-85-to-94-task.json`): `authorLabel`, `byLabel` and every comment's `authorLabel` are empty. So the timeline renders these rows exactly as it did before: "by human owner@example.com · done at 85", "owner@example.com · human". There is no "unknown" and no "(unverified)".
- Neither does a pre-91 pause note get re-labelled ("Paused by owner@example.com.", written by the old binary).
- The 85-era PATCH to false left no `auto-merge-*` event, because #447 came after it.

## Item 5: forgery

`"author": "bob@example.com", "authorKind": "human"` was sent on `PUT /api/memory`, `PUT /api/memory/documents` and `PUT /api/release-notes`:

| Source | Result | Stored as |
|---|---|---|
| IAP (alice) | 200 | `human / alice@example.com / iap`: bob ignored |
| Access code | **400** "a memory's author may only name an agent, got human "bob@example.com": a person is recorded as themselves only when a verifier says who they are" | nothing written (D2) |
| Agent (task 20) | 403 "an agent cannot name somebody else as the author of work it files" | nothing written |
| `gma_` (ext1) | 403, the same | nothing written |

bob is recorded nowhere.

## Findings (fixed)

### F1: pause and resume notes named the access-code holder as a person, and every pauser as "the operator"

To reproduce on `ac1f0e6d`, pause and resume any task with the access code (`POST /api/tasks/22/pause`, then `/resume`). The notes read:

> "Paused by owner@example.com."
>
> "Resumed by owner@example.com. This task was paused by the operator at …"

Alice through IAP, task 20 and `ext1` all got "paused by the operator" too. `Task.paused.by` carried the bare id.

**Fix:** the pause and resume notes, their refusals and `TaskPaused.By` use `PrincipalLabel`, and `model.ResumedNote` takes the pauser's label. Regression test: `TestPauseAndResumeNotesNameWhoPausedAsTheirLabel`. `TestPauseAndResumeRestoreEachStateExactly` now expects the in-process caller as `operator (local)`, not `alice`.

### F2: every auto-merge event named `ui.(*Client).mutate` as its writer

To reproduce, `PATCH /api/tasks/22 {"autoMerge":false}`. The `auto-merge-off` event's detail is `ui.(*Client).mutate (client.go:5029)` for every Client edit. That frame is the wrapper every edit passes through, so it says nothing about which edit wrote.

**Fix:** `autoMergeWriter` skips that wrapper (`autoMergeWrappers`), so the detail reads `ui.(*Client).UpdateTask (client.go:4523)` or `ui.(*Client).submit`. Regression test: `TestAutoMergeEventNamesTheEditNotTheMutateWrapper`.

### F3: the timeline showed an access-code write as the deployment's own address (UI)

On `ac1f0e6d`, the task API carried only `author`/`authorKind` and `by: "human owner@example.com"`. A task filed, commented on and closed with the access code therefore read on the timeline as that person, indistinguishable from owner signing in through IAP. The stored source never reached anybody.

**Fix:** `Task.authorLabel`, `Comment.authorLabel` and `Transition.byLabel` carry `PrincipalLabel` ("operator (access code)", "task 20", "external agent ext1", or a verified email), and `Timeline.jsx` shows them. `shownLabel` leaves them empty for a row with no source (pre-91), which keeps reading as before (item 4).

Regression tests:

- `TestTaskViewLabelsEachActorByHowItWasEstablished`
- `Timeline.test.jsx` "names an access-code holder as the operator, not the deployment's address"

Screenshots are attached to the closing note.

### F4: the pipeline change feed said "by human owner@example.com" for the access code

This is the same fault as F3 in `pipelineTaskChanges`. It now reads "by operator (access code)", "by task 20" and "by external agent ext1", and an unsourced event reads as before. Regression test: `TestPipelineChangeFeedNamesAnAccessCodeHolderAsTheOperator`.

### F5: declining auto-merge on a change a hold had already turned off left no event

To reproduce:

1. Hold a trust-boundary push, which sets auto-merge off.
2. As a person, `PATCH {"autoMerge":false}`.

The task gains the `auto-merge-declined` mark: from then on only that person's Submit or PATCH turns auto-merge back on. But no `task_event` was written, so neither the timeline nor the feed said who made the change wait for them.

**Fix:** `guardAutoMerge` (`declinedWhileOff`) records `auto-merge-off`, with the principal, the reason and the writer, when a write first declines a task that is already off. Repeating the PATCH records nothing more.

Regression tests:

- `TestDecliningAnAlreadyOffAutoMergeIsRecordedAsThePersons`
- `TestDeclinedAutoMergeOutlastsHoldReleaseCarryAndRestartUntilSubmit`

### F6: external-agent token writes were logged without who made them

`POST /api/external-agents/tokens` logged `issued credential id=1 name="ext1" …`, and the rotate, revoke, delete and kill-switch lines were the same. A credential has no `config_audit` row (it is not configuration, and routing it through the config guard would refuse it under an external config repository), so this line is its whole audit trail. Each line now leads with `<label> (<source>)`, for example `operator (access code) (access-code) issued credential id=1`. Regression test: `TestExternalAgentTokenWritesLogWhoMadeThem`, which also checks that the token itself is not logged.

## Differences from the plan (not defects)

- **D1: loopback (c) is not a live source.** With the access code required on every caller, loopback included (`accesscode.go`, task-4343), loopback with no code gets 401. `trustedRequestSource` returns `loopback` only for a request admitted some other way, which never happens over HTTP today. A request from loopback *with* the code records `access-code`. The `loopback` source is kept for a deployment that turns the code off. Nothing to fix here, but nothing in this run could write a `loopback` row.
- **D2: a forged human author sent with the access code is refused (400), not recorded as the operator.** The plan expected the write to succeed and name the operator. The code refuses it outright, which is stricter, and bob is recorded nowhere. Through IAP the forged author is ignored and alice is recorded instead.
- **D3: `Creator` stays the deployment's person on an agent's or token's task.** Task 23's details read "Creator owner@example.com". This is the human who vouches for the credential the task runs under (`Declared.jsx`), not the filer, who is `authorLabel` ("task 20"). It is by design.
- **D4: a hold's release over a declined task writes no `auto-merge-kept-off`.** `heldForHuman` (`securityhold.go`) treats a declined task as human-only, so the release never attempts to turn auto-merge on, and there is nothing to keep off. `kept-off` appears only when some writer does try, which `TestExplicitAutoMergeFalseStaysOffUntilAPersonSaysOtherwise` drives. The plan's "with `auto-merge-kept-off` events naming each writer" describes the store's guard, not this path.
- **D5: there is no "Activity" view.** No page, panel or heading of that name exists in `ui/src`. The pipeline change feed is API-only (`GET /api/pipeline/changes`, read by the pipeline steward).
- **D6: the state-repo `git log` was not walked live.** See item 2.

## Re-running

```sh
go build -o /home/debian/bin/grain ./cmd/grain           # after cd ui && npm ci && npm run build
go build -o /home/debian/bin/iapsim tests/iap/iapsim.go
docs/test-runs/2026-11-03-operator-attribution/matrix.sh /tmp/oa   # starts and stops the env
# or: (source env.sh; start_env /tmp/oa); REUSE=1 ./matrix.sh /tmp/oa; ./sticky.sh /tmp/oa
git worktree add /tmp/old85 75331e14 && (cd /tmp/old85 && go build -o /tmp/grain85 ./cmd/grain)
# run /tmp/grain85 demo, write with the access code, kill -9 it, then:
go run docs/test-runs/2026-11-03-operator-attribution/upgrade.go <its grain-demo-* dir> <task id>
```
