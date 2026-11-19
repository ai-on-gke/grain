# Walkthrough: targeted-review Accept findings and review feedback fixes (2026-11-18 §3)

Plan: [`docs/test-plans/2026-11-18.md`](../test-plans/2026-11-18.md) §3 (PRs #1760, #1808, #1806).
Filed by [Task #7014](/tasks/7014); run as [Task #7054](/tasks/7054).

**Outcome:** I walked "What is NOT proved" items 1–5 live on a real daemon, over SQLite and then PostgreSQL, with identical results apart from commit SHAs. Most of it works:

- template-less sensitive triggers file a targeted review whose findings hold the **Targeted review** gate;
- Accept findings clears that gate only, from the classic gate card and the CLI;
- a QA hold and a broken-rules hold keep auto-merge off;
- agents are refused;
- the 422 fallback asks the owner, and their reply answers the question;
- the question cap fills in the documented order and stays under 2048 bytes.

The walk found **two bugs, both fixed on this branch**, and **one behaviour left as a follow-up**:

1. **Accept findings on `/next/` was refused whenever the diff preview had loaded, on every review gate** (`546fc0172`). The Changes tab pinned the acceptance to the diff preview's `head`, which is a branch name. The server read it as a head that had moved and refused, so the dialog stayed open. Force merge pinned the same value.
2. **Accepting findings handed auto-merge back to a change whose merge rules say `auto_merge: never`** (`f757079aa`). Nothing merged, because the merge cycle withdrew it again first, but the task read auto-merge on in between.
3. **Follow-up (not fixed):** a targeted review that serves two triggers records all its findings under the first trigger's name. `-gate trigger:<second>` is refused, and `-gate trigger:<first>` clears the findings of both.

`2026-11-16.md` §3 (Accept findings on `review` and `security-review`) still has no report on `main` (`git log origin/main -- docs/test-runs/`). I walked only `targeted-review`, the holds and the two feedback fixes, as §3 item 6 asks.

## Environment

- **Commit walked:** `main` at `b0b27e281`, then this branch's fixes. The runs recorded under [`2026-11-18-review-gates-feedback/out/`](2026-11-18-review-gates-feedback/out/) used `546fc0172`, which has both fixes. The bug 1 reproduction below is from the first SQLite run on `b0b27e281`.
- **Build:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (go1.26.10, Node 20.19.0).
- **Host:** Debian 12, git 2.39.5, Python 3.11.2, sqlite3 3.40.1.
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes -max-workers 3 -as alice@example.com -repo acme/widgets` on `127.0.0.1:4280`, polling every 500ms.
- **Stores:**
  - SQLite (embedded, `data/store/grain.db`).
  - PostgreSQL 15.20 (Debian `postgresql`), `-postgres-url postgres://grain:grain@127.0.0.1:5432/grain_walk`. The daemon log shows the PostgreSQL single-writer advisory lock taken.
  - Each run starts from a fresh data dir, database and bare repository.
- **GitHub:** `pkg/github/githubsim` over plain HTTP ([`ghsim.go`](2026-11-18-review-gates-feedback/ghsim.go), adapted from the 2026-11-14 harness). The new `/_sim/refuse-reviews?n=` sets the sim's own `RefuseReviews` from PR #1808, so its 422 is the code CI tests, not a wrapper.
- **Repository `acme/widgets`:**
  - `.grain/triggers/payments.yml` and `ledger.yml`: `paths: [payments/**]` / `[ledger/**]`, `gate: true`, `sensitive: true`, `human: on-findings`, `auto-approve: true`, and **no `template:`**. Each files a targeted review, titled `Targeted review (payments): …`.
  - `.grain/merge-rules.yml`: rule `frozen` (`frozen/**`) with `auto_merge: never`.
- **Agent:**
  - The 2026-11-14 harness's `fake-agy.py`, reused unchanged, driving the real `grain mcpserver` of each run.
  - Reviews post findings with the in-sandbox `grain pr review-comment … --path --line`. The MCP `add_review_comment` tool the 2026-11-14 script used is no longer offered to runs.
  - What each run does is scripted in [`rules.json`](2026-11-18-review-gates-feedback/rules.json), written by [`gen-rules.py`](2026-11-18-review-gates-feedback/gen-rules.py).
- **Browser:** Playwright 1.62.1 Chromium (headless shell 1234). Widths: 1440×900 and 390×844, light theme.
- **Helpers:** [`2026-11-18-review-gates-feedback/`](2026-11-18-review-gates-feedback/) holds:
  - `env.sh`, `walk.sh` and `ghsim.go`;
  - `ui-accept.mjs`, which drives the browser steps and prints one JSON line each (`out/<store>/ui.jsonl`);
  - `gen-rules.py` and `rules.json`.
- **Reproduce:** `STORE=sqlite ./walk.sh`, then `STORE=postgres ./walk.sh`.
  - Each run takes about 4 minutes. Re-reviews are undeferred with `POST /api/tasks/{id}/undefer`, so the 3-minute `reviewDebounce` is not waited out.
  - It needs PostgreSQL with role `grain`/`grain` for the second store, and ui-ng's dev dependencies (`cd ui-ng && npm ci`). `make frontend` removes them again.
  - `out/<store>/steps.log` has every step's gates and answers.

## Steps and outcomes

Task ids are the same on both stores. A*n* is an author task, each touching `payments/` and filed with auto-merge on.

| Item | What happened | Result |
|---|---|---|
| 1 | A1 (2), A2 (4), A3 (6): each head gets **`Targeted review (payments): WALK-A… `** (3, 5, 7). The review posts one located finding with `grain pr review-comment`. Each author's gates: CI running, **`targeted-review` `findings`, `acceptable: true`, reviewTask = the review**. Findings are recorded under `trigger:payments` and auto-merge is off. | Pass |
| 1, `/next/` 1440 px | `/next/task/2?tab=changes` shows **Accept findings…** beside the Targeted review pill, on screen. The dialog reads "Accept Targeted review findings — Review task 3 left findings on this gate: A1: …" and fits the viewport. With a reason, Confirm clears the gate (`passed (task #3)`) and the button goes. | **Fail on `b0b27e281`** (bug 1); pass with the fix |
| 1, `/next/` 390 px | Same on task 4; button at x = 12, dialog inside 390 px. | Same as above |
| 1, classic `/tasks/6` | The gate card offers **Accept findings…** on the Targeted review row. The dialog (`confirm-accept-findings`) clears it, and auto-merge comes back (`autoMerge: true`) with nothing else holding. | Pass |
| 1, two triggers | A4 (8) touches `payments/` and `ledger/`, so one review (9) serves both triggers and posts one finding on each path. `reviewFindings` holds **one record, gate `trigger:ledger`**, with both comments. `grain accept-findings -gate trigger:payments 8 …` answers "8 has no review findings on gate trigger:payments". `-gate trigger:ledger` clears both comments' record and restores auto-merge; then `-gate targeted-review` has nothing left. | **Finding 3** (follow-up) |
| 2, withdrawn merge policy | A5 (10) also touches `frozen/**` (`auto_merge: never`). The targeted review's hold had already turned auto-merge off, so grain never recorded `merge-policy-withdrawn` (`A5 links:` empty). Accepting the findings answered 200. On `b0b27e281` it **set `autoMerge: true`** and the next merge cycle withdrew it again. With the fix, auto-merge stays off and the task waits for a Submit (`approval: needed`). | **Fail on `b0b27e281`** (bug 2); pass with the fix |
| 2, QA hold | A6 (12): a `qa-hold` link set at the head (written into `task_link`, since this repository has no QA flow), then the findings accepted: `autoMerge: false`, the QA gate is still running, and the hold is still there. | Pass |
| 2, broken-rules hold | A7 (14): a `broken-rules-hold` set the same way, then the findings accepted: `autoMerge: false`. | Pass |
| 3, agent token | Task 18 holds `https://grain.dev/capabilities/api#tasks:write`. Its run calls `grain api POST /api/tasks/16/accept-findings` with its own token and gets `{"error":"only a person may accept a review's findings: an agent that thinks they need no fix asks the owner (ask_question)"}` (exit 1). A8's findings are untouched. | Pass (refused, as `docs/agent-api.md` says) |
| 3, owner via CLI | On A8 (16):<br>• `-gate security-review` and `-gate review`: "16 has no review findings on gate security-review" (resp. `review`).<br>• No reason: "say why: grain accept-findings -gate targeted-review <id> …".<br>• `-head 0000000`: "acme/widgets#9007 has moved since you looked: you read the findings at 0000000 and its head is now 318c5eb84b79 …".<br>• The real acceptance succeeds.<br>• A second acceptance: "16 has no review findings on gate targeted-review". | Pass |
| 4, 422 | `refuse-reviews?n=1`, then A9 (19). Review 20's draft is refused, and the daemon logs "task 20 could not post its 1 review finding(s) as a review on acme/widgets#9008 (GitHub API error 422 … one pending review per pull request); relaying them into the task's conversation instead". The sim holds no review for #9008. Task 19's conversation gets **"Review needs your decision."** with the question and the closing sentence "Posting them as a review on acme/widgets#9008 failed, so they are here and on task 20 only." | Pass |
| 4, `/next/inbox` | At 1440 and 390 px the Inbox lists task 19 under **"Review needs your decision"** ("Review questions — The review left questions only you can settle."). | Pass |
| 4, reply | The owner's comment on 19 ("Q9: yes, log the refused amount…") requeues it with no retry needed: run 2 starts and pushes head 2. Review 21's prompt lists "- Task 20 (`security-review` at …): verdict `findings` … - **[answered]** `payments/pay.go:3`: Q9: …" (`out/<store>/a9-second-review-earlier.md`). | Pass |
| 5, cap | A10 (22) over four reviews:<br>• R1 (23) asks 15 long questions (~300 B each), answered by the owner.<br>• R2 (24) asks 2, answered.<br>• R3 (25) asks 3, left open.<br>• A person then force-pushes, and R4 (26) is filed.<br>R4's prompt lists 23, 24 and 25 in that order. 25's three **[open]** questions and 24's two answered ones are listed in full. 23 shows L01–L05 and L06 cut, then "- ... (9 answered questions omitted; read full threads with `grain pr threads`)". Question lines plus footer come to **1988 B** (cap 2048). | Pass |

### Item 5: the order matches the documented policy

`allocateEarlierReviewQuestions` (`pkg/orchestrator/reviewcontext.go`) spends the budget on open questions first, then answered ones, newer reviews before older within each. Each review's questions go in the order it asked them, and the listing stays oldest review first. R4's section is exactly that:

- every open question;
- R2's answered ones;
- then R1's from L01 until the budget ran out.

R1 gets the only footer. Its count names the status ("answered").

## Bugs found and fixed

### 1. `/next/` Accept findings refused once the diff preview loaded (`546fc0172`)

**Reproduction (first SQLite run on `b0b27e281`):**

1. With A1 (task 2) holding targeted-review findings, open `/next/task/2?tab=changes` and wait for the diff preview.
2. Click **Accept findings…** on the Targeted review pill. The dialog reads "At commit grain/4f86c6": the first 12 characters of the branch `grain/4f86c696/task-2-…`.
3. Fill in a reason and click Confirm. The dialog stays open, and the gate still says `findings`. `ui.jsonl`: `TimeoutError: … waiting for getByTestId('accept-findings-dialog') to be hidden` at both widths.
4. Doing it immediately, before the preview loads, works. The same POST without `head` answers 200. The existing spec never asserted the pinned head, so it could not catch this.

**Cause:**
- `GET /api/tasks/{id}/diff-preview` answers `{"head": "grain/4f86c696/task-16-walk-a8-tweak-payments", …}`, the compared branch.
- `task-changes-tab.component.ts` pinned `head: this.preview()?.head || t.ci?.commit`.
- `acceptFindingsHead` compares a pinned head against the pull request's head SHA, so a name always reads as "has moved since you looked".
- `emitForceMerge` pinned the same value, recorded server-side as the override's head SHA (`ForceMergeHeadSHA`). I did not walk Force merge live.
- The classic pane pins `t.ci?.commit` only (`AcceptFindingsDialog.jsx`).

**Fix:** both pin CI's last commit (`pinnedHead()`), as the classic pane does.

**Regression:** `ui-ng/e2e/next-accept-findings.spec.js`, "Targeted review findings are accepted at CI's commit", at 1440 and 390 px. It serves a diff preview whose `head` is a branch and waits for it to load. Then it asserts:
- the dialog names the Targeted review and "At commit c0ffee123456", never the branch;
- the POST body is exactly `{gate: "targeted-review", reviewTask: "4", reason, head: <CI commit>}`.

It also covers the Targeted review gate, which no ui-ng spec did.

### 2. Accepting findings restored auto-merge under `auto_merge: never` (`f757079aa`)

**Reproduction:** A5 in `walk.sh` (item 2).
- A change matching an `auto_merge: never` rule gets a targeted review with findings before the merge cycle sees its policy.
- `withdrawForMergePolicy` returns early because auto-merge is already off, so `merge-policy-withdrawn` is never written.
- On `b0b27e281`, `POST /api/tasks/10/accept-findings` set `autoMerge: true`. The next cycle withdrew it again, so nothing merged.

**Cause:** two restorers looked only for the withdrawal mark:
- `reviewGatesSettled` (`pkg/ui/accept_findings.go`);
- the store's `restoreAfterHolds` (`pkg/model/task_auto_merge.go`), which turns auto-merge back on when a write drops the last hold link. Review findings are one of those links.

**Fix:** both now also read the effective policy (`model.MergePolicyOf`) and stay off when it says `AutoMergeNever`. A person's Submit still turns auto-merge on as before.

**Regressions:** each fails without the fix.
- `TestDroppingTheLastHoldHandsAutoMergeBack` (`pkg/model`): new cases "auto_merge: never policy", "merge policy withdrawn" and "merge policy allows it".
- `TestAcceptFindingsTargetedReviewGate` (`pkg/ui`): a fourth hold case, the policy link with no withdrawal mark.

## Finding not fixed (follow-up proposed)

### 3. A targeted review serving two triggers records its findings under the first trigger only

**Reproduction:** A4 in `walk.sh` (item 1, two triggers).
- `dropPathTriggerHoldOnFindings` (`pkg/orchestrator/pathtriggerhold.go`) and `reviewGateOf` (`mergegates.go`) both take `triggerNames[0]`. One review serving `ledger` and `payments` therefore records `trigger:ledger` for its finding on `payments/pay.go` as well.
- The aggregate gate behaves: `-gate targeted-review` accepts the review.
- The per-trigger form `-gate trigger:<name>` ("accepts one trigger's own record", `docs/declarations.md`) does not:
  - `trigger:payments` is refused with "no review findings on gate trigger:payments", while the gate shows a payments finding;
  - `trigger:ledger` clears the payments finding too.
- The plan's "accept one: the other's findings and questions remain" cannot hold while one review files one record. Questions are per review task, not per trigger.
- Fixing it means attributing each finding to the trigger(s) whose paths it is on, or saying in the docs and the refusal that a shared review is accepted as a whole. That is a design call, so I left it for review.

### Observed, by design or harness artefacts

- Neither hold is produced naturally here: a QA hold needs a QA flow with S4, and a broken-rules hold needs a broken `merge-rules.yml` under a trust-boundary hold. Both were written into `task_link` at the head, then the findings accepted.
  - The broken-rules link was gone again after the next cycle on SQLite (`still: 0`) and stayed on PostgreSQL (`still: 1`). Grain rewrites it from the security hold, which this change does not have, so how long the injected link lasts is a harness artefact. Auto-merge stayed off in both.
- After the 422, A9 is `awaiting_submit` while it waits on its question. The owner's plain comment still requeued it as an answer, which PR #1808 promised.
- The A4 review's finding detail on the gate reads "A4-pay: … (and 1 more)". The ledger finding is folded into the one record (finding 3).

## UI review (design book mandate)

The UI change is two lines of logic in `task-changes-tab.component.ts`, with no template, style or layout change. What the user sees change is the dialog's "At commit" line, now a SHA, and Confirm working.

- **Design:** dialog, button and gate pill rendered unchanged at 1440 and 390 px over the live daemon (`out/*/shots`). The button sits inside the gates row and the dialog inside the viewport at both widths (asserted in the spec and in `ui.jsonl`). No new tokens or copy.
- **Functional:** Accept findings clears the targeted gate end to end on both stores at both widths. The new spec asserts the exact request. `next-full-system.spec.js` passes (5/5: desktop and phone, light and dark, and contrast).
- **Adversarial:**
  - With no CI commit yet, no `head` is sent and the server pins the PR head it knows. That is what the classic pane does, and what `/next/` did before the preview loaded.
  - A preview from an older head can no longer pin a wrong commit: it pinned a name before, never a commit.
  - Force merge gets the same pin. Its server records the pinned value as the override's head, so a branch name there was wrong too.
  - No P0/P1 left.

Screenshots: `contact-sheet.png` (attached to the task's closing note) lays out the PostgreSQL run's shots:
- `/next/` Changes tab before, dialog and after, at 1440 and 390 px;
- classic `/tasks/6` before, dialog and after;
- `/next/inbox` at 1440 and 390 px.

## Tests run

- `go test ./pkg/model ./pkg/ui ./pkg/orchestrator -count=1`: ok (PostgreSQL installed, so its tests ran).
- `go test ./pkg/model -run TestDroppingTheLastHoldHandsAutoMergeBack` and `go test ./pkg/ui -run TestAcceptFindings`: each fails with fix 2 reverted, and passes with it.
- `cd ui-ng && npx playwright test e2e/next-accept-findings.spec.js`: 4 passed, the two new cases included.
- `cd ui-ng && npx playwright test e2e/next-full-system.spec.js`: 5 passed.
- `STORE=sqlite ./walk.sh` and `STORE=postgres ./walk.sh` at `546fc0172`: every item as in the table above.
