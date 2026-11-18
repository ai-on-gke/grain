# Merging and reviews walkthrough — 2026-11-13 §6 (tasks 4725, 6633)

`docs/test-plans/2026-11-13.md` §6: merge credentials, the every-PR gating
review, review priority, review carry across a base merge, and the merge
queue's give-up.

- **Walked:** task 4725 walked §6 on 2026-11-13 against a real daemon and
  fixed the three bugs below (F1–F3) on
  `grain/098073e4/task-5358-walkthrough-merge-credentials` (PR #1465). It
  committed the helper scripts, but not this report. Task 6633 (review 5414's
  send-back) re-ran every item script on 2026-11-17 07:27–07:45 UTC against
  the PR's head with all three fixes in, and wrote this report from that
  re-run. Every output quoted below is from the re-run.
- **Commit tested:** `7c580a382` (merge of `main` into the PR branch; footer
  `v0.0.0-20261113043242-7c580a382aad`), which includes the fixes
  `c7c46d8ed` (F1), `db9436f5d` (F2) and `2b89658d3` (F3).
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`, 7 CPUs, 30 GB), Go `1.26.10`,
    Node.js `v20.19.0`, `sqlite3` from apt (not preinstalled).
  - `go build -o /home/debian/bin/grain ./cmd/grain` and
    `go build -o /home/debian/bin/reviewed-merge ./cmd/reviewed-merge`.
    For the Inbox probe, `cd ui && npm ci && npm run build`,
    `cd ui-ng && npm ci && npm run build`, then `grain` rebuilt so both UIs
    are embedded.
  - Playwright headless Chromium from `ui/node_modules`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).

## Setup (`docs/test-runs/2026-11-13-merge-and-reviews/`)

| File | What it does |
| --- | --- |
| `env.sh` | Builds and starts `ghsim.go` on `127.0.0.1:4284`. Starts a real `grain daemon -host-sandboxes -sandbox-port-base=0` over SQLite on `127.0.0.1:4280` with `-max-mergers 0`, `-agent-framework antigravity` and `-agy-path fake-agy.py`. Seeds a bare `acme/widgets` with this repository's own `.grain/triggers/every-pr.yml` and `.grain/templates/pr-review.yml`, plus a grain CI workflow that always passes. Imports two git connections with `grain connections import`: `bot` (token `ghp_push`) pushes and names `merge_credential: mergebot` (token `ghp_merge`, scoped to an unrelated repo so it is never picked to push). Turns on `acme/widgets`'s declarations. |
| `ghsim.go` | `pkg/github/githubsim` over HTTP, with each merge attributed to the token that made it (`-merge-logins ghp_push=alice-person,ghp_merge=widgets-merger[bot]`). It records merges at `GET /_sim/merges` and seeds check runs on a ref with `POST /_sim/check-runs`. |
| `fake-agy.py` | A scriptable stand-in for the agent CLI. `$WALK_DIR/action-<task>.json`, `action-review.json` or `action-default.json` give the MCP calls it makes. A `hold` keeps a run alive until its `live-<task>.hold` file is removed. |
| `common.sh` | `boot`, `file_task` (approved, auto-merge on), `wait_for`, `tasks`, and the default actions: a one-file push and a clean review. |
| `item1_2_merge_and_review.sh` | Items 1 and 2 (clean review, merge identity). |
| `item1b_credentials.sh` | Item 1, second half: `reviewed-merge` accept and refuse, import round-trip, `grain health`. |
| `item2b_3_findings_and_priority.sh` | Item 2 (findings) and item 3 (priority). |
| `item4_carry.sh` | Item 4 (carry, incremental re-review). |
| `item5_give_up.sh` | Item 5 (give-up Inbox item, Retry merge). `KEEP=1` leaves the daemon up for `ui_inbox.mjs`. |
| `ui_inbox.mjs` | Playwright: `/next/inbox` and `/inbox` at 1440×900 and 390×844, light and dark. |

Outputs go to `out/` (git-ignored). The scripts run in the order above, each
on a fresh `/tmp/walk-merge`.

The re-run changed two scripts. In `item1_2_merge_and_review.sh`, the merge
wait looked for a `merged` state that a merged change never reports (it
closes), so it always waited out its 120 s. In `item5_give_up.sh`, the
`/api/inbox` read used a jq filter that fails on the route's `{items: […]}`
shape, so the script exited 5. Its Retry-merge step also now lifts the red
before it checks that the item clears (see item 5).

## Results

| Item | Result |
| --- | --- |
| 1 | Pass (F1 fixed). The command-backed refusal was not walked end to end. |
| 1b | Pass |
| 2 | Pass |
| 2b | Pass |
| 3 | Pass |
| 4 | Pass (F2 fixed) |
| 5 | Pass (F3 fixed), with observations O1 and O2 |

### 1. Merge credential over the simulator — pass

`item1_2_merge_and_review.sh`: one task (`Add a note`, priority 5, auto-merge
on) pushed a one-file change, and grain opened `acme/widgets#9000`.

- ghsim recorded one merge, made with the merge credential's token:

  ```
  [{"path":"/repos/acme/widgets/pulls/9000/merge","token":"ghp_merge","login":"widgets-merger[bot]",…}]
  {"merged":true,"merged_by":{"login":"widgets-merger[bot]"}}
  ```

- The task's events were `auto-merge-off` (the every-pr hold),
  `auto-merge-on` (the review passed), `merged-as acme/widgets#9000 merged by
  widgets-merger[bot]` and `closed`.

### 1b. Reviewed-merge check, import round-trip, health — pass

`item1b_credentials.sh`:

- **A. Accepts the merge credential's merge.** `reviewed-merge -repo
  acme/widgets -commit <#9000's merge commit> -mergers 'widgets-merger[bot]'`
  exited 0 with `{"merges":[{"pull":9000,"base":"main","via":"grain","by":"widgets-merger[bot]"}]}`.
- **B. Import round-trips `merge_credential`.**
  - `grain_config.git_connections` read
    `[{"credential":"mergebot","scope":"acme/merge-credential-holder"},{"credential":"bot","merge_credential":"mergebot"}]`
    both before and after a second `grain connections import` of the same
    file (`cmp`: "second import changed nothing").
  - `GET /api/deployment/git-connections` now carries the field:
    `{"credential":"bot","scope":null,"mergeCredential":"mergebot"}`.
    Before F1's fix it dropped it.
- **C. Refuses a merge made with the push credential.**
  - The environment was booted again with `MERGE_CREDENTIAL=""` (no merge
    credential) and `-upgrade-reviewed-merge-repo acme/widgets
    -upgrade-reviewed-merge-logins alice-person`.
  - The merge went out as `ghp_push` / `alice-person`
    (`merged-as acme/widgets#9000 merged by alice-person`).
  - `reviewed-merge -mergers 'widgets-merger[bot]'` exited 1:
    `refused: c97f41dfdf7f did not reach main through a reviewed merge: pull
    request #9000 was merged by alice-person, with no approval from an owner,
    member or collaborator other than its author, and not by grain`.
- **D. `grain health`** warned that `alice-person` is a user account.
  `GET /api/sandboxes`'s `mergeLoginWarnings` carried the same sentence:
  `-upgrade-reviewed-merge-logins trusts alice-person's merges as grain's,
  and alice-person is a user account: every merge its owner makes by hand
  passes the reviewed-merge gate unreviewed. Give the repository's git
  connection a merge_credential (a GitHub App), then name the App's
  <slug>[bot] here instead of alice-person (docs/github-actions-ci.md#a-merge-identity-of-grains-own)`.
- **Not walked:** a merge credential behind a *command-backed* push
  credential (PR #1256). This needs a push credential whose forge calls go
  through a host tool, which ghsim over HTTP cannot stand in for. It is
  covered by `TestCredentialTokenSourceRefusesAMergeCredentialBehindAForgeCommand`
  (`pkg/daemon`, run under "Tests run" below). That test checks that the
  refusal (`ErrNoMergeToken.Reason`) names the push credential. Whether the
  reason reaches `grain health` and the UI was not observed.

### 2. Every-PR review, clean — pass

From the same run as item 1:

```
{"id":"2","title":"Add a note","state":"closed","priority":5}
{"id":"3","title":"Review acme/widgets#9000","state":"closed","priority":7}
```

- Exactly one review task was filed for `#9000`, at priority 7
  (`max(5, ReviewPriorityFloor 7)`).
- The change's gates read `CI passed` and `Code review passed (task #3)`.
- The clean review auto-approved, auto-merge came back on, and the PR merged.
  The touched file (`notes/2.txt`) is unclassified, so no security review was
  filed. The classified-path half of item 2 (a security review as well) is
  covered by the declaration-trigger tests and was not re-walked.

### 2b. Every-PR review with findings — pass

`item2b_3_findings_and_priority.sh`, part 2b: the review agent made one
`add_review_comment` on `README.md:1`.

```
{"state":"awaiting_submit","autoMerge":false,"mergeGates":[{"id":"ci","state":"running",…},{"id":"review","label":"Code review","state":"findings","detail":"This note has no trailing context; say why it exists.","reviewTask":"3","overridable":true}],…}
{"state":"open","merged":false}
```

- Auto-merge stayed off: the only event was `auto-merge-off`, with no
  `auto-merge-on`.
- The PR stayed open and unmerged. The change waited in `awaiting_submit`
  with the overridable review gate showing the finding (`human: on-findings`).

### 3. Review priority against a full queue — pass

Same script, part 3, with `MAX_WORKERS=1`. Task 4 (`Urgent change`,
priority 9) held the only slot while three priority-5 fillers (5–7) were
queued behind it. The hold was then released. Run start times:

```
4|07:31:40.200   Urgent change (p9)
5|07:31:41.199   Filler 1 (p5)
8|07:31:44.855   Review acme/widgets#9001 (p9)
6|07:31:45.355   Filler 2 (p5)
7|07:31:49.301   Filler 3 (p5)
```

- Review 8 was filed at priority 9 (the gated change's own, above the floor).
- It dispatched ahead of Fillers 2 and 3, which had been queued since
  07:31:40.9.
- Filler 1 took the slot in the moment task 4's run ended. The daemon logged
  `path trigger filed 8` in that same second, once task 4's PR was opened.
  So the review was not yet queued when the slot first freed. It went next
  as soon as Filler 1 finished, which is the ordering the plan asks for.

### 4. Carry across a base merge, incremental re-review — pass (F2 fixed)

`item4_carry.sh`. The fake agent seeds an in-progress `unit` check, so the
approved change waits on CI rather than merging while its head is moved by
hand.

- **Approved:** review 3 passed. The change read `waiting_ci`, with the
  review gate `passed (task #3)`.
- **4a. Merge `main` into the PR without changing its diff.** An unrelated
  commit landed on `main`, then the branch merged it:
  head `2fe28d97` → `4ef3afe0`. After 45 s there was still only one review
  task, and the gate still read `passed (task #3)`: the review was carried
  and no re-review was filed.
- **4b. A push that changes the diff.** `docs: extend the note` filed review
  4 at priority 7, and the gate moved to `running (task #4)`. The re-review's
  body says:

  > **This is an incremental review.** An earlier review of this kind read
  > the change at 4ef3afe0d3da. Review only what changed since: `git diff
  > 4ef3afe0d3da4682e3655d773969dbb36fa059d1 f0366eefeee7f0980392c55746c556521c26e068` …

  It compares from the carried head `4ef3afe`, not from `2fe28d97`, the head
  review 3 originally read. Before F2's fix it fell back to a full review
  here.

### 5. Merge queue give-up and Inbox — pass (F3 fixed)

`item5_give_up.sh`:

- **Setup.** `acme/widgets` declares `cla/google` unfixable
  (`PUT /api/repos/acme/widgets/unfixable-checks`). The fake agent seeds a
  failing `cla/google` and a passing `unit` on its branch before pushing.
- **The queue gives up at once and spends no repair.** The store held only
  tasks 1 (Director), 2 (the change) and 3 (its clean review). No repair
  task was filed:

  ```
  {"state":"completed","mergeQueueState":"merge_failed","mergeQueueNote":"nothing pushed to this branch turns `cla/google` green, so grain handed it to a person rather than spend a repair that could not have worked -- \"Retry merge\" starts the round again"}
  ```

  Before F3's fix, the every-pr hold had withdrawn auto-merge, so this change
  was first handed to a repair agent. It only gave up afterwards, with "the
  repair didn't take".
- **Notification.** `pipeline.merge-needs-person:2`, titled `The merge queue
  gave up on acme/widgets#9000 (task 2): it needs a person`, with actions
  `open-task` and `investigate`. Its body names the PR and the reason, and
  lists what unblocks it (a fix, Retry merge, a stale gate).
- **Inbox.**
  - `GET /api/inbox` has a `merge-held` item:
    `{"task":"2","pr":"acme/widgets#9000","reason":"nothing pushed to this branch turns `cla/google` green, …"}`.
  - `grain inbox` prints
    `merge-held   #2 acme/widgets#9000 -- nothing pushed to this branch turns `cla/google` green, …  /tasks/2`.
- **Retry merge.**
  - While `cla/google` is still red, `POST /api/tasks/2/retry-merge` gives
    up again at once, and the notification stays open (count `1`). That is
    right: nothing changed.
  - Once ghsim reports `cla/google` green on the branch, Retry merge merges
    the PR (`merged_by widgets-merger[bot]`), the task closes, and the
    notification clears (count `0`).
- **Browser (`KEEP=1`, then `node ui_inbox.mjs http://127.0.0.1:4280 <code> 2 acme/widgets#9000`).**
  There were no page errors and no horizontal overflow (`overflowPx: 0`) on
  any of the 8 shots.
  - **`/next/inbox`, at both widths and in both themes:** the Merges lane
    has an "Unblock a merge" row for `#2 Add a note the queue gives up on`
    (`Merge blocked`, `The merge queue stopped driving this pull request.`,
    `✕ CI`). The Alerts lane has the notification, whose body names
    `acme/widgets#9000` and the `cla/google` reason. Retry merge is offered.
  - **Classic `/inbox`, at both widths and in both themes:** an "unblock a
    merge" group ("The merge queue gave up on landing these automatically --
    its own comment on each says why.") lists `Add a note the queue gives up
    on` with `merge failed`, `given up`, `retry merge` and `open pull
    request`. See O2.
  - The plan's shared-fault variant (a red Actions job grain flagged as a
    shared fault, and a stale red from an older head) needs Actions run logs
    ghsim does not serve. It is left to
    `TestASharedFaultOnlyRedIsReRunOnceTheBaseGoesGreen` and the merge-queue
    escalation tests.

## Findings

### F1 (fixed in `c7c46d8ed`): `GET /api/deployment/git-connections` omitted `mergeCredential`

- **Where:** `pkg/ui/team_git_connections.go`, the connection list view.
- **Observed:** `grain connections import` stored `merge_credential: mergebot`
  on the `bot` connection, and every merge went out under it. But the list
  route's JSON had no such field, so neither UI nor the API could say which
  credential a connection merges with.
- **Fix:** the view carries `mergeCredential`. `grain connections --help`'s
  seed-file example also documents `merge_credential` (`pkg/cli/connections.go`).
- **Regression test:** `TestGitConnectionsListSaysWhichCredentialMerges`
  (`pkg/ui/git_connection_merge_credential_test.go`).
- **Verified:** item 1b B above.

### F2 (fixed in `db9436f5d`): re-reviews after a carried base merge were full reviews

- **Where:** `pkg/orchestrator/samechange.go` (`carryPathTriggerGate`, new
  `carryLastReviewed`) and `pkg/orchestrator/pathtriggerreview.go`
  (`incrementalBase`).
- **Observed:** when a merge of the base left the PR's diff unchanged, the
  gating reviews carried (PR #1301), but the last-reviewed head stayed at the
  head the review had read. The next real push compared from that older head.
  The base's merge commit inside that range brought in files that
  `every-pr.yml`'s `"**"` watches, so the re-review fell back to a full review
  every time.
- **Fix:** the carry moves the last-reviewed head along with the gates, and
  `incrementalBase` reads from it, so the re-review covers only the new
  commits.
- **Regression test:** `TestACarriedBaseMergeKeepsACatchAllReReviewIncremental`
  (`pkg/orchestrator/samechange_test.go`).
- **Verified:** item 4b above.

### F3 (fixed in `2b89658d3`): `splitReds` skipped entries with auto-merge off

- **Where:** `pkg/orchestrator/sync.go`, `syncEntry`.
- **Observed:** a failing entry's reds were split into the branch's own and
  the repository's declared-unfixable checks only when the task had
  auto-merge on. But the failing arm repairs a red change whether or not it
  is merging itself yet. `.grain/triggers/every-pr.yml` withdraws auto-merge
  from every grain-opened PR until its review passes. So a change red only
  on an unfixable check (`cla/google`) was handed to a repair agent no
  commit could help, and gave up only afterwards, with "the repair didn't
  take".
- **Fix:** the split runs for every unstacked, unblocked, unsettled entry,
  so such a change is escalated as unfixable without spending its repair.
- **Regression test:** `TestAnUnfixableRedIsEscalatedWhileAGatingReviewHoldsAutoMerge`
  (`pkg/orchestrator/mergeneedsperson_test.go`, which fails without the
  change).
- **Verified:** item 5 above (no repair task filed).

## Observations, not changed

- **O1. `/next/` Alerts rows show a notification's subject as its title.**
  `classifyNotification` (`ui-ng/src/app/inbox/inbox-classifier.ts`) sets both
  `ref` and `title` to `n.subject`. So the give-up alert reads
  `pipeline.merge-needs-person:2` twice, at every width, rather than its
  title `The merge queue gave up on acme/widgets#9000 (task 2): it needs a
  person`. The body underneath still names the PR and the reason. Filed as a
  follow-up from task 6633.
- **O2. Classic `/inbox`'s "unblock a merge" row names neither the PR number
  nor the reason inline.** It shows the task title, `given up` and an `open
  pull request` link, and its group header points to the queue's own
  comment. `/next/` is the experience the plan points new work at, and it
  does name both. Classic gets bug fixes only, so this was left as is.

## Tests run

On `7c580a382` (re-run, 2026-11-17):

- `go test ./pkg/orchestrator/... ./pkg/mergelogins ./pkg/reviewedmerge/...`:
  ok (`pkg/orchestrator` 165.6 s).
- `go test ./pkg/daemon -run 'MergeToken|MergeCredential|MergeRefus' -v`: ok
  (`TestCredentialTokenSourceHandsOutTheMergeCredentialForMerges`,
  `TestCredentialTokenSourceRefusesAMergeCredentialBehindAForgeCommand`).
- `go test ./pkg/ui -run 'MergeCredential|CredentialMerges' -v`: ok
  (`TestReplacingAGitConnectionKeepsItsMergeCredential`,
  `TestGitConnectionsListSaysWhichCredentialMerges`).
- `go test ./tests/hygiene/...`: ok.
