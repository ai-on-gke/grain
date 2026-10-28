# Walkthrough: security clearance carried to ports; red base waits via depends-on (`main`)

Plan: `docs/test-plans/2026-10-12.md` §13 (read from `origin/candidate`). Task 3296, filed by task 3209.
Run 2026-10-13 against `main` at `f3dfcaa5c`. Both commits under test are on `main`
(`git branch -r --contains d31c38f0e` and `acd860412` both list `origin/main`).

## Environment

- Debian sandbox, Go toolchain from the repo, git 2.x, Node + `npm ci` in `ui/`, Playwright Chromium
  (installed with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`; without it the
  CDN download fails with `SELF_SIGNED_CERT_IN_CHAIN` behind the package-cache proxy).
- Steps 1–3: real `orchestrator.RunCycle` (dispatch, finish-time classification, `SyncSecurityHolds`,
  port filer, merge queue, base-branch CI reconciler), the real SQLite store, a real git server
  (`githubHostServer` over a bare repository) and `githubsim`.
- Step 4: real `grain demo -merge-queue-states` (embedded SQLite) through `ui/e2e/fixtures.js`, against
  the built frontend (`npm run build`).

## Stubs

| Boundary | Stub | Where |
|----------|------|-------|
| GitHub (PRs, comments, diffs, merges) | `githubsim` behind `syncedSim`, serving real git diffs for `CompareDiff`/`PullRequestDiff` | clearance walkthrough |
| GitHub (merge queue tests) | `githubsim` via `newSim` | red-base walkthrough |
| Model vendor | scripted agent (`carryAgent`): reviews answer "no findings"; ports follow the port body's `git cherry` / `cherry-pick -x` recipe, resolving a conflict with `--theirs`; other tasks run a fixed shell script | clearance walkthrough |
| Model vendor | `capturingFramework` (records dispatches; nothing is dispatched in this test) | red-base walkthrough |
| Grain CI executor | `greenCI` (every run green at once) for ports and changes; `fakeCI` (`newFakeCI`, verdicts set by the test) for the merge queue | both |
| Base-branch CI verdict | `PutBaseBranchCI` rows: confirmed red at main's tip with no repair, then green at the fixed tip. The confirmation run itself is not driven | red-base walkthrough |
| A busy worker | an unrelated running run with `MaxWorkers: 1`, so the cycles link and file without dispatching the repair from under the test | red-base walkthrough |
| "Cleared at an earlier head" / "list changed" | after the source merges, its `trust-boundary-cleared` link is rewritten to `main~1`, or to version `trust-boundary/v4`. A real build change of the list is not reproducible in one binary | clearance walkthrough |
| Mechanical porter's remote | `portpick.Porter` with `Remote` pointing straight at the bare repo, not through the git proxy (the proxy path is `port_mechanical_walkthrough_test.go`'s) | clearance walkthrough |
| UI seed | `grain demo -merge-queue-states` writes the `base_broken` entry and its depends-on link through the store, the way the queue writes them | Playwright |

## Steps and results

### 1. Clearance, positive case: `tests/e2e/clearance_carry_walkthrough_test.go`

A change to `pkg/mcp/tools.go` (exec-sandbox) on `main` is held at its finish. One security review is
filed and finishes clean. The hold is released and the change merges, cleared at its final head. The
declared `port-branches: [main, candidate]` files one port onto `candidate`.

| Case | Result |
|------|--------|
| Agent-made port | **Pass.** Merged onto candidate with `AutoMerge` kept, no hold, no review filed for its branch, and `clearance-inherited-from` naming the source. The "is a port of task N" notice appears exactly once on the PR and once on the task, naming `pkg/mcp/tools.go`. The agent ran for the port. |
| Mechanical port (`portpick.Porter`) | **Pass.** The same results, and the agent never ran for the port. |

### 2. Clearance, negative cases: same file

| Case | Result |
|------|--------|
| Conflict resolution changed the patch (candidate has its own version of the file) | **Pass.** The port is held (`security-hold`, auto-merge off, no inherited link) with exactly one review of its own. |
| Docs-only CI-repair commit pushed onto that held port | **Pass.** No second review: it still waits on the same review, which then releases it, and it merges with one review in total. |
| Source cleared at an earlier head | **Pass.** Held for its own review. |
| Source cleared under another trust-boundary list (`trust-boundary/v4`) | **Pass.** Held for its own review. |
| A released, cleared change moved by a docs-only CI repair before it merges | **Pass.** One review in total, and `trust-boundary-cleared` carried to the new head. |

### 3. Red base: `pkg/orchestrator/redbase_links_walkthrough_test.go`

Main is confirmed red at its tip with no repair filed. There are three ready tasks and a pull request
whose only red (`checks/go`) is main's.

- Two dispatch cycles file **exactly one** repair (tagged with the branch repair tag), and `t1`–`t3` each
  depend on it with one open blocker. Nothing is dispatched. **Pass.**
- The merge queue entry reads `base_broken`, with a note "blocked on task N", and depends on N. **Pass.**
- A second repair of main filed while the first is running depends on the first and does not run. **Pass.**
- Main is fixed and its grain CI goes green. One cycle closes **both** repairs, each with a note saying
  "superseded", and clears every link. The entry is re-armed rather than dispatched: main is merged into
  the branch and a fresh grain CI run starts against that merge. The repair isn't spent, the agent never
  runs, and the PR merges once that run passes. **Pass.**
- Manual depends-on loop: **Fail → fixed (F1).**

### 4. UI: `ui/e2e/red-base-blocked-on-walkthrough.spec.js`

- **Task pane: pass.** The `Blocked` chip's title reads `Blocked on: #13 (queued)`. "Depends on" lists
  `13 Fix grain CI on acme/widgets main` as "(blocking this task)", and the badge reads "Waiting: the base
  branch is broken". There is no "Retry merge" button.
- **Inbox: pass.** The task is in neither "Approve on GitHub" nor a merge-blocked group. This is a
  negative check only: neither section exists in this seed, so the assertion is vacuous when they are
  absent.
- **Repository overview (`/repos`): pass.** The `acme/widgets` row shows a `Blocked N` chip.
- Wording issue: see F2.

## Findings

### F1: a manual depends-on loop was accepted (fixed)

`ui.Client.SetDependency` (`POST /api/tasks/{id}/depends-on`) refused only a task depending on itself.
It accepted a link whose target already waits on the task, directly or through a chain. On a red base
this is the obvious mistake to make: grain links task T to repair R, a person then makes R depend on T,
and both stay blocked forever. Grain's own `dependOn` refuses direct loops, but the picker didn't.

Deterministic reproduction: `go test ./pkg/ui -run TestADependencyThatWouldCloseALoopIsRefused` without the
fix fails with `SetDependency(1 -> 3) closed a loop and was accepted`.

Fix: `dependsOnLinks` walks the target's blocking links (depends-on and child-of; closed tasks wait on
nothing, so the walk stops at them) and refuses with a validation error (HTTP 400) that names the chain:
`task 3 already waits on task 1 (3 → 2 → 1), so task 1 depending on it would be a loop neither could
leave`. The regression test also checks that a chain through a closed task is still accepted.

### F2: the `base_broken` note contradicts the chip beside it (not fixed, filed)

The task pane shows a red `Blocked` chip and the note "blocked on task 13, which is repairing `main` …
no repair is spent and **nothing is blocked**, and when task 13 closes …". The phrase "nothing is
blocked" predates the link: it meant the merge-queue `blocked` flag, but beside a "Blocked" chip and
"blocked on task 13" it reads as a contradiction. The text comes from `pkg/orchestrator/inherited.go`'s
note and the demo seed in `pkg/demo/demo_merge_queue.go`. Tests assert on it in `inherited_*_test.go`
and `merge-queue-waiting-states.spec.js`. The change is cosmetic and crosses several test files, so it
is filed as a follow-up rather than changed here.

No defects were found in the clearance carry (`d31c38f0e`): every positive and negative case behaved as
its commit message describes.

## Fixes made

- `pkg/ui/client.go`: depends-on loop refusal (`blockingPath`), with regression test
  `TestADependencyThatWouldCloseALoopIsRefused` in `pkg/ui/client_test.go`.

## Tests added

- `tests/e2e/clearance_carry_walkthrough_test.go`: `TestClearanceCarryWalkthrough`, 6 subtests.
- `pkg/orchestrator/redbase_links_walkthrough_test.go`: `TestRedBaseWalkthroughLinksOneRepairAndReArmsOnGreen`.
- `ui/e2e/red-base-blocked-on-walkthrough.spec.js`: 3 Playwright tests.

## CI

Grain CI verdict for the pushed branch: see the pull request's `wait_for_checks` result.
