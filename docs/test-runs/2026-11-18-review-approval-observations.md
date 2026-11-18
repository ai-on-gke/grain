# Review and approval gate observations — 2026-11-18 walkthrough

[Task #7053](/tasks/7053) walks §2 of `docs/test-plans/2026-11-18.md`. That section covers PR #1813 (`f7a0152ed`), "Observe review and approval scopes without changing legacy authority". The plan was filed by [Task #7014](/tasks/7014).

**Result:** the observers never changed legacy behaviour, and every current-mode review and approval prediction read `unknown`, which is what `docs/gate-review-observations.md` says the legacy store can support. The bounds held. Scoped and ungranted readers saw what the doc promises. The walk found:

- one UI bug, fixed here: the panel printed CI agent text twice;
- one legacy attribution bug, proposed as a follow-up;
- two cost/limit gaps in the observation reader, proposed as follow-ups.

## Commit and environment

| | |
| --- | --- |
| Commit walked | `b0b27e281` (`origin/main` when the task started), built with `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` |
| Fix commit | `e9cec6b89` `fix(ui-ng): show an observation's agent text once in the task panel` |
| Daemon | real `grain daemon -sandbox-port-base=0 -host-sandboxes` on `127.0.0.1:4280`, fake Antigravity CLI and GitHub simulator from `docs/test-runs/2026-11-14-review-context/` (themselves adapted from `2026-11-13-grain-health-exit-codes/`) |
| Stores | SQLite (embedded `modernc.org/sqlite v1.57.0`; `sqlite3` CLI 3.40.1 for direct edits) and PostgreSQL 15.20 (Debian 15.20-0+deb12u1), a fresh database per run |
| Allowlist | `GRAIN_GATE_OBSERVATION_TASKS=1,…,32` (the 32-task maximum) for "set" runs, unset for "empty" runs |
| Host | Debian 12.15, Go 1.26.10, Node 20.19.0 |
| Browser | Playwright 1.62.1 Chromium (`chromium-1234`), 1440×900 and 390×844, light and dark |

There is no `docs/test-runs/2026-11-17*` report for §4 of the 2026-11-17 plan (delivery/acknowledgment observations, PR #1739) on `main` (`git log origin/main -- docs/test-runs/`). So there was no §4 harness to reuse, and this walk does not judge the delivery, acknowledgment, CI-check or integration observers. They appear in the payloads but are out of scope.

## Harness

Helpers are in [`2026-11-18-review-approval-observations/`](2026-11-18-review-approval-observations/):

- `grain-obs.sh` wraps the grain binary. The 2026-11-14 harness starts the daemon under `env -i`, so the wrapper exports `GRAIN_GATE_OBSERVATION_TASKS` from `/home/debian/obs-allowlist`.
- `run.sh` (`STORE=sqlite|postgres ALLOW=set|empty`) does the following:
  - runs the unchanged 2026-11-14 `walk.sh` against that wrapper. The walk covers a security review and a path-trigger (`api/**`) `pr-review` per head, a person's push mid-review, a force-push, a merge of `main`, and an external fork PR reviewed three times.
  - runs `sample.sh` the whole time. Every 2 s it records each task's legacy state, `mergeGates` and review/approval observations from `GET /api/tasks/{id}`, plus the read's latency, in `samples.jsonl`.
  - runs `extras.sh` afterwards.
  - exports every task row, normalized (timestamps, SHAs and observation sidecars dropped), for the set/empty diff.
  - writes `summary.txt` (`summarize.sh`): each task's distinct (state, gates, observations) sequence.
- `extras.sh` drives these steps on the same daemon:
  - Accept findings;
  - a sensitive `human: required` trigger naming `alice@example.com`, an author task through it, Submit, then a person's push;
  - a repository move of that task, read by unrestricted, new-repository, old-repository and ungranted callers;
  - 600 links, then one 70,000-byte link, on the author task, and 600 links on a linked review;
  - 40-read latency samples of `GET /api/tasks/{id}` at each step.
- `panel-check.mjs` checks `/next/tasks/<id>` in Playwright against the API (see "Observation panel").

Reproduce with `sudo apt-get install -y postgresql sqlite3 jq bc`, then one run per configuration:

```
cd docs/test-runs/2026-11-18-review-approval-observations
STORE=sqlite ALLOW=set ./run.sh
STORE=sqlite ALLOW=empty ./run.sh
STORE=postgres ALLOW=empty ./run.sh
STORE=postgres ALLOW=set ./run.sh
```

PostgreSQL needs a `grain`/`grain` superuser on `127.0.0.1:5432`. Each run takes about 6–8 minutes, up to 2 of them spent waiting for the 2-minute declaration poll to pick up the new trigger. `KEEP_DAEMON=1` leaves the last daemon up for the panel check. Raw output goes to `out/` and is not committed. The readable parts are copied to `results/<store>-<set|empty>/`: `summary.txt` has each task's distinct (state, merge gates, observations) sequence, and `extras.txt` has every extras step's compact snapshot.

Task IDs below are the walk's own, the same on every run: 2 is the author task (PR `acme/widgets#9000`); security reviews are 3, 6, 9 and 11; path-trigger reviews are 4, 5, 7, 8, 10 and 12 (12 deferred); 13–15 review the fork PR; 16 is the approval author task (PR `#9002`); and 17/18 are its `secrets` trigger reviews.

## Item 1 — review lifecycles under observation

On the author task, the observer always showed one current (`review/current_head_v1`) and one imported (`review/legacy_completion_v1`) monitor per assignment. Assignments were `security/task:<n>`, `trigger:api/task:<n>` and, from findings records, `ordinary/task:<n>`. The task also carried `approval/exact_head_v1` for `domain:submit` and, while the `on-findings` hold applied, for `hold:path:on-findings`.

| Step (legacy merge gates) | Current-head monitors | Imported legacy comparison |
| --- | --- | --- |
| Head 1 filed: `security-review=running`, `review=running` | all `unknown` ("current head, policy or authorization version is unavailable") | `security/task:3` and `trigger:api/task:4` `pending` |
| Review questions posted: `security-review=findings`, `review=findings` | `unknown` | `unmet` for the security and path assignments |
| Re-review per head (author runs 2–4, the person's push, the force-push, the merge of `main`) | `unknown`; assignments move to the newest review IDs as legacy links do | `pending` while the review runs, then `unmet` on findings |
| Security review 11 passes: `security-review=passed` | `unknown` | `security/task:11` `met`, agreeing with the legacy gate |
| Accept findings (`POST /api/tasks/2/accept-findings`, gate `review`, review 9) | the accepted `ordinary/task:9` scope leaves the projection; the others stay `unknown` | legacy keeps `review=findings`, now naming review 6 (it had two findings records) |
| `approval/exact_head_v1 domain:submit` throughout | `unknown` | — |

The following matched the doc, and nothing was promoted to `met` in current mode:

- Current mode stayed `unknown` at every step, on both stores. This is the doc's stated limit: "The legacy store does not supply an authenticated current provider head … Consequently current review/approval observations from this adapter remain unknown, even when imported legacy comparisons pass."
- Head changes (fast-forward, force-push, merge of `main`, a person's push) moved assignments to the newly filed reviews.
- `pending` held while a review was active.
- `trigger:api/task:12`, a deferred review, stayed `unmet`.

Two disagreements are worth recording:

- **D1 — a superseded security review's findings land on the legacy Code review gate.** This is a legacy bug that the observer faithfully mirrors.
  - After security review 6 was superseded by 9, and 9 by 11, the legacy `review` ("Code review") gate read `findings` with "review 9 left findings". It offered "Accept findings" (`acceptable: true`) for security review 9. `reviewFindings` held `{gate: review, reviewTask: 6}` and `{gate: review, reviewTask: 9}`. The observer derives category from the legacy gate name, so it labelled them `ordinary/task:6` and `ordinary/task:9`.
  - Cause: `reviewGateOf` (`pkg/orchestrator/mergegates.go:37`) compares the review ID with the task's *current* `security-review-task` link only. Once that link moves to a newer security review, an older one's findings resolve to `GateReview`.
  - Reproduction: run `STORE=sqlite ALLOW=empty ./run.sh` and read `out/sqlite-empty/walk/h4-task*.json` or `GET /api/tasks/2`. The `review` gate names a security review. It is the same with the allowlist empty, so observations do not cause it.
  - Fixing it changes legacy merge-gate authority, which is outside this task's scope. It is proposed as a follow-up.
- **D2 — `legacy_completion_v1` for an ordinary scope reads `met` while the legacy gate it sits beside reads `findings`.**
  - `ordinary/task:6` and `ordinary/task:9` were `met` ("Ordinary review is closed or completed empty-…") the whole time the legacy `review` gate blocked on their findings.
  - The doc says the ordinary comparison "uses the existing closed-or-completed-without-PR predicate", so this is documented behaviour rather than a predicate bug.
  - It is the only category where the imported comparison can say `met` while legacy blocks. Security and path comparisons label the aggregate gate (`unmet` on findings). It falls out of D1 (security findings seen as ordinary) and is folded into that follow-up.

**Not walked: takeover.** A takeover shepherd task is filed only by the GitHub bot's `@<slug> take over` comment webhook (`pkg/orchestrator/takeover.go`), which the GitHub simulator does not send. No `takes-over` or `takeover-review` links were produced, so takeover scopes were not observed on a live daemon. `pkg/model` and `pkg/gates` tests remain the only coverage.

**Not walked: an "ordinary" configured code review.** The seed repository has no `review_template`. The ordinary scopes above come from findings records, not a configured reviewer.

## Item 2 — noninterference

I ran the identical script with the allowlist set and empty on each store and diffed the normalized task rows, which include comments, links and state history. With the allowlist empty, `gate_observations` was absent from every response.

- **SQLite:** all 18 task rows are identical once arrays are compared as sorted sets and numeric comment and event ids are dropped. The one exception is review 9's `promptBytes` (20,937 set, 20,689 empty). PostgreSQL's *empty* run also has 20,937, so this is run timing, not observations. The walk's own task list (`fork-r3-tasks.txt`) is identical.
- **PostgreSQL:** all 18 task rows are identical under the same normalization. The raw diff showed only reordered state-history entries with equal normalized timestamps, and fork review 14 dumped as `queued` against `closed` 3 s after filing. Both rows end `closed`.

No extra review, wait, approval, link or comment appeared in a set run. The 15 walk tasks had the same titles and final states in both runs.

## Item 3 — repository move

Task 16 (open PR `acme/widgets#9002`, allowlisted) was moved with `PATCH /api/tasks/16 {"repo": "acme/gadgets"}`. Readers were external-agent tokens with `scopes: ["tasks:read"]` and `repos: [<repo>]`. They need `externalAgentApi: true` in Settings; with it off every token gets 403 "external agent API is disabled", which is not a gate answer.

| Reader | `GET /api/tasks/16` after the move |
| --- | --- |
| unrestricted (deployment access code) | 200, current view with no review/approval bindings (the PR is in another repository: "Change policy is outside the authorized task repository scope"), plus `gate_observation_diagnostics` with history |
| scoped to `acme/gadgets` (new repo) | 200, current `gate_observations` only, **no** `gate_observation_diagnostics` |
| scoped to `acme/widgets` (old repo) | 403 "holds tasks:read, but not on acme/gadgets" |
| scoped to `acme/other` (no grant) | 403 "holds tasks:read, but not on acme/gadgets" |
| scoped to `acme/widgets`, unmoved task 2 | 200, current predictions only |

Denials are ordinary task-read refusals, never an "unavailable"/outage observation. A scoped reader never received the history sidecar. Legacy also reacted to the move with the allowlist empty: the task's merge gates disappeared and it went to `awaiting_reply`.

**L1 — the history sidecar holds only the newest sample for PR tasks.**

- History reads stop at a 64 KiB budget (`pkg/model/gate_observations.go`, "Keep the newest complete sample even if it alone exceeds the read budget").
- One PR task sample with review/approval scopes is about 70 KiB (author task: 14,907–70,633 bytes; task 16: up to 73,951). So `gate_observation_diagnostics.history` and the panel's "newest 20 samples" show one sample, even though 95 rows were stored for task 2.
- After a move the unrestricted reader therefore sees the post-move sample only. The "bounded history" that should show pre-move samples is never visible in practice.
- Reproduction: run `STORE=sqlite ALLOW=set ./run.sh` with `KEEP_DAEMON=1`. Then `sqlite3 /home/debian/walk-obs/data/store/grain.db "select task_id,count(*),max(length(view_json)) from gate_observation_shadow_history group by task_id"` against `GET /api/tasks/2 | jq '.gate_observation_diagnostics.history|length'` (1).
- This is not wrong per the code, but it defeats the documented purpose. Proposed as a follow-up.

## Item 4 — approval receipts per exact head

The base gained `.grain/triggers/secrets.yml`: `paths: ["secrets/**"]`, `gate: true`, `human: required`, `sensitive: true`, `approvers: [alice@example.com]`. Task 16 then pushed `secrets/key.txt`.

| Step | Legacy | `approval/exact_head_v1 trigger:secrets` |
| --- | --- | --- |
| Head 1 filed | `approval=needed`, `review=running` | `unknown`, legacy detail "Named approval is outstanding … Recorded approver tokens: alice@example.com." |
| Alice Submits head 1 | `approval=passed`, `humanApprovedBy: alice@example.com` | `unknown`, detail "Legacy named approval marker names alice@example.com, without rendered-head, policy and role-version pins." |
| A person pushes head 2 | `approval=needed`, approval cleared | `unknown`, detail back to "outstanding"; the new trigger review (18) replaces 17 |

The current-mode approval never passes, which is what the doc requires: no rendered-head or role-version receipt exists. It also never reads `pending`, because inaccessible receipt evidence is `unknown`, not a missing receipt over a complete read. The legacy-detail text tracks the exact-head Submit and its invalidation on a new push.

Partial eligibility was visible only as separate scopes, with no positive per-scope receipt to compare:

- `trigger:secrets`, `hold:path:required`, `builtin:human-only`, `hold:human-only/<digest>` and `domain:submit`;
- `builtin:human-only` and the `hold:` scope dropped out while alice's Submit held and came back with head 2.

The approval rule also showed up on the *author* task (2), whose PR does not touch `secrets/`, as `trigger:secrets` (`current unknown`, `legacy unmet`) plus an approval scope. This is the doc's "a newly synchronized gating path rule is unknown even if no review has been filed". The imported `unmet` there has no legacy gate to compare with; legacy shows no gate for it.

## Item 5 — bounds and latency

The author task's sample sequence shows, in order:

| Step | Observation |
| --- | --- |
| 600 `context-manifest` links inserted on task 2 | every review/approval monitor collapses to `collection`: `unknown`, "Linked metadata exceeds the 512-link/65536-byte reader bound; exact source evidence is unavailable." Legacy gates unchanged |
| those removed, one 70,000-byte link inserted | same `unknown` with the same reason |
| that removed, 600 links on linked security review 11 | `security/task:11` current `unknown` "Related task metadata exceeds the 512-link/65536-byte reader bound." |
| links removed | back to the pre-bounds projection on the next read |

Neither bound ever let a monitor pass, and neither slowed the read beyond its own noise.

`GET /api/tasks/2` latency, 40 sequential reads per step (p50 / p95, ms):

| Step | SQLite set | SQLite empty | PostgreSQL set | PostgreSQL empty |
| --- | --- | --- | --- | --- |
| after the walk and Accept findings | 70.8 / 100.4 | 0.6 / 0.8 | 77.8 / 94.4 | 0.8 / 5.4 |
| before bounds | 74.5 / 100.3 | 0.7 / 1.9 | 77.9 / 105.5 | 0.8 / 5.9 |
| 600 links on task 2 | 75.7 / 98.9 | 0.7 / 1.4 | 82.8 / 108.7 | 0.7 / 0.8 |
| one 70,000-byte link on task 2 | 72.7 / 100.2 | 0.6 / 0.9 | 72.6 / 97.1 | 0.7 / 1.1 |
| 600 links on linked review 11 | 71.8 / 96.0 | 0.7 / 1.0 | 76.8 / 96.3 | 0.7 / 0.9 |

The 2-second sampler's reads of every task over the whole walk (p50 / p95; each also spawns curl and jq): SQLite 20.4 / 81.5 ms set against 12.2 / 15.2 ms empty, and PostgreSQL 27.1 / 94.9 ms set against 13.8 / 17.1 ms empty.

**L2 — an allowlisted task read costs about 70 ms more than an ordinary one.**

- Over the whole walk, the sampler's reads of allowlisted tasks had p50 about 70–83 ms (p95 95–109 ms) with the allowlist set, against 0.6–0.8 ms with it empty (table above), on both stores.
- The cost is there on every read, including cache-eligible ones. It is within the documented 250 ms collection budget and capped at 32 tasks, but it is not "unnoticeable".
- Reproduction: compare `latency …` lines in `out/*-set/extras/extras.txt` with `out/*-empty/extras/extras.txt`.
- Proposed as a follow-up to profile the cached path.

## Observation panel on `/next/tasks/<id>`

`panel-check.mjs` loads `/next/tasks/<id>` signed in with the daemon's access code at 1440×900 and 390×844, light and dark. It compares the page with `GET /api/tasks/<id>` on the PostgreSQL set run's daemon (tasks 2, 3, 13 and 16). For each page it checks:

- the panel (`[data-testid="gate-observations"]`) is visible exactly when the API carries `gate_observations`;
- it says "Legacy services remain authoritative";
- it contains no button, input, select, textarea or link;
- the number of articles equals the number of results, and each heading names its result's type and status;
- no paragraph repeats inside one article;
- the panel fits the viewport, with no horizontal page overflow.

Results:

- **On `b0b27e281`:** 8 failures, all the repeated agent text below (tasks 2 and 13, the PR tasks, at every width and theme). Tasks 3 and 16 passed. `results/panel-check-b0b27e281.txt`.
- **On the fix (same data, daemon restarted on the fixed build):** all checks pass. `results/panel-check-fixed.txt`.

I also shot `/next/tasks/2` full-page with `ui/e2e/screenshot.mjs --url … --full-page` (desktop 1440 and phone 390, light and dark) before and after the fix. The contact sheets are attached to the task's closing note. This sandbox cannot display PNGs and has no `LOOK=1` key, so the layout claims above rest on the Playwright assertions, not on looking at the images.

**F1 (fixed in `e9cec6b89`) — agent text shown twice.**

- The panel template rendered `entry.result.agent_text` in two `@if` blocks, before and after `human_text`.
- `pkg/gates/checks.go` sets `agent_text` on every CI checks observation, so each allowlisted PR task showed "Prediction only: inspect CI status/logs; run or retry CI under existing authorization." twice per CI article.
- `panel-check.mjs` caught it as a repeated paragraph inside one article.
- Fix: remove the second block. `ui-ng/src/app/task/task-observations.component.spec.ts` now asserts each article shows the agent text once. It fails on the base ("expected [ …(2) ] to have a length of 1 but got 2") and passes with the fix.
- Observations still have no controls and no authority.

## Follow-ups proposed

Each depends on [Task #7053](/tasks/7053):

1. **D1:** attribute a superseded security review's findings to the security-review gate (`reviewGateOf`). This is a legacy merge-gate fix. D2 goes away with it.
2. **L1:** show more than one history sample for PR tasks. The 64 KiB history read budget is smaller than one PR sample.
3. **L2:** cut the ~70 ms observation cost on allowlisted `GET /api/tasks/{id}`, cache-eligible reads included.

No predicate disagreed with legacy where the doc says it should agree, so this change adds no `pkg/gates` or `pkg/model` fix. The only code change is the panel fix F1.

## Notes for the next walk

- `extras.sh` must wait for the declaration poll (2 minutes, `pkg/orchestrator/declarations.go`) before filing against a new base rule. Filed earlier, the approval task finished `completed` with no approval gate (one discarded run).
- Direct `sqlite3` writes against a live daemon need `.timeout`. Otherwise `database is locked` aborts the step.
- On PostgreSQL, `task_link.blocks` is a boolean.
- Scoped readers are external-agent tokens and need `externalAgentApi: true`.
- The memory note that `ui/node_modules` is ready for Playwright is out of date: `make frontend` installs without dev dependencies. Run `npm ci` in `ui/` (and `ui-ng/` for vitest) before `screenshot.mjs`, `panel-check.mjs` or `npx vitest`.
