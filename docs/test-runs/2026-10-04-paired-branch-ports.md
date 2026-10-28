# Test run: automatic ports between paired branches (2026-10-04)

Executes §2 of `docs/test-plans/2026-10-04.md`, "Automatic ports between paired branches". Task
1828, branch `grain/task-1828`.

## Tested commit

- **Base:** `candidate` at `5ab9aa1ae` (Merge pull request #2850), 2026-10-04 11:41 UTC.
- **The three feature commits are on it.** `git branch -r --contains` lists `origin/candidate`
  and `origin/main` for each:
  - `e51e1b141` feat(model): declare paired port branches and a per-task port opt-out
  - `302cc0fd3` feat(orchestrator): file a port task when a change lands on a paired branch
  - `de5da5685` feat(ports): let tasks opt out of porting, and turn the nightly backport into a report
- **The fixes and tests below are on top**, in `6e38d99a0`, `a767d09d5`, `0d196db36` and
  `b25f1c5d0`.

## Environment

- **Sandbox:** Debian, Go 1.26.6, git 2.39.5, Node 20.19.0, Playwright 1.62.1 (Chromium from
  `/var/cache/playwright`).
- **Go e2e harness (`tests/e2e`):**
  - the real store (embedded SQLite, reopened for every cycle);
  - real `orchestrator.RunCycle`, which covers the declaration sync, dispatch, close-out, merge
    queue and port filing;
  - githubsim as GitHub, over a real git smart-HTTP server.
- **Browser:**
  - the real `grain demo` (built by `ui/e2e/global-setup.js` with `-tags grain_e2e`);
  - the production bundle from `cd ui && npm ci && npm run build`;
  - driven by Playwright.
- **Triage script:** run against scratch two-branch repositories (`tests/deploy`), and against a
  clone of this repository's real `origin/main` and `origin/candidate`.

## Stubs

Only external boundaries were stubbed. Grain's own APIs, store, merge queue and React were all
real.

| Stub | Where | Why |
|---|---|---|
| githubsim for GitHub's REST API, with real git underneath | `tests/e2e/port_walkthrough_test.go` | GitHub |
| Mergeability set clean on open pull requests (`markPullRequestClean`) | same | GitHub computes mergeability asynchronously |
| Scripted agent (`portAgent`, `antigravity.NewForTest`) instead of a live model session. It runs the `git cherry -v …` command taken from the port task's own body. | same | Live model sessions |
| `greenCI`: a `CIRunner` that answers every run `success` at once | same | Every port carries the `grain-ci` grant (`newPortTask`), and the harness has no sandboxes to execute CI jobs. The merge queue's reading of the verdict is real. |
| `bareDeclarations`: an `orchestrator.DeclarationSource` that clones the bare repo and runs the real `repodecl.Load` | same | cmd/grain's `repoDeclarationSource` does the same clone over the network |
| `releasehelper link-port` writes the `port-of`/`ported-by` link pair into the demo's store | `ui/e2e/paired_branch_ports_walkthrough.spec.js` | `grain demo` runs no merge queue, so nothing in it can file a port. This seeds data; it does not mock an API. The API reads and the pane are real. |

## Commands

```sh
# §2 steps 1 and 3: e2e through the real merge queue
cd tests/e2e && go test -run TestPortWalkthroughPairedBranches -count=1 -v ./

# unit regressions
go test ./pkg/orchestrator -run 'Port|Paired|Ported|OptedOut|Unpaired|Backport|Footer' -count=1

# §2 step 4: triage script
cd tests/deploy && go test -run 'TestBackportTriage' -count=1 -v ./
git clone --shared work triage-real && cd triage-real && bash scripts/backport-triage.sh   # real branches

# §2 step 2: browser
cd ui && npm ci && npm run build
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/paired_branch_ports_walkthrough.spec.js --project=chromium

# §2 step 6: audit of the live branches (script in the appendix)
SINCE='36 hours ago' bash audit.sh

make security
```

## Outcomes, step by step

### 1. Merge queue cycle (`TestPortWalkthroughPairedBranches`), passed

**Setup.**
- `acme/widgets` has `.grain/settings.yml` declaring `port-branches: [main, candidate]` on `main`,
  with `candidate` cut from it.
- The real declaration sync logs
  `.grain/settings.yml changes 1 settings field(s) … port-branches: (blank) -> "main, candidate"`,
  and `PortTargets("main")` is `[candidate]`.

**A merge onto `main`** (an auto-merge task in the default team, with grant `release-notes`)
filed exactly one port. Daemon log: `filed 3 to port acme/widgets#9000 (task 2) from main to candidate`.

| Checked | Result |
|---|---|
| `Base` | `candidate` |
| `Team` | same as the source |
| `AutoMerge` | true, same as the source |
| Approved | yes: `Approval` and `ApprovedAt` set |
| Capabilities | the source's `release-notes`, plus `grain-ci` |
| Links | the port has `port-of 2`; the source has `ported-by 3` |
| Title | `Port acme/widgets#9000 to candidate: add a widget` |
| Body | names the merge, `git cherry -v`, `cherry-pick -x` and `Backported-from`, and quotes the original task |

**After that:**
- **Restart.** Every cycle reopens the store. Further cycles, which also dispatched and merged the
  port, left exactly one port of task 2.
- **The port's own merge onto `candidate` was not ported back** (`the task is itself a port`).
  `candidate` holds the file and a `Backported-from:` commit.

**Skip rules, end to end.** Each one filed nothing:

| Case | Daemon log |
|---|---|
| `NoPort` task | `the task is marked not to be ported` |
| `Port: no` footer | `the change carries a `Port: no` footer` |
| Hand port (footer, no link) | `the change is itself a port (Backported-from / cherry picked from)` |
| Head already merged onto `candidate` | `the change is already on, or already being ported to, every paired branch` |

**Back-merged head** (after fix F1). A task that merged `origin/candidate` into its branch before
landing on `main` filed one port. The port, following the body's `git cherry` recipe, put only that
task's own commit on `candidate`.

**A final restart cycle** filed nothing new. The store holds two ports in total.

### 2. Browser, passed (2 tests)

- **New task:** "Don't port to the repo's paired branches" starts unchecked. Ticked and filed,
  `GET /api/tasks/{id}` returns `noPort: true`, and the pane reads "not ported".
- **Seeded port pair:** the port's pane shows "port of <source>". The link opens the source, whose
  pane shows "ported by <port>", and that link opens the port again.

### 3. `propose_task` with `no_port: true` through the relay, passed

A task whose scripted run calls `propose_task{title, body, no_port: true}` gets a child at the
close-out (`relayProposedTasks`) with `NoPort: true`. Approved and landed on `main`, the child filed
no port (`the task is marked not to be ported`). This is in the same e2e test.

### 4. Triage script: reports only, pushes nothing, passed after fix F3

**Scratch pair** (`TestBackportTriageScript_MixedPairListsOnlyTheMissing`):
- **Setup:** a footer port, a patch-id equivalent, a `Port: no` commit, and one commit missing
  from each side.
- **Result:** it lists exactly the two missing commits, and `for-each-ref` on the origin is
  unchanged.
- **After a back-merge** (candidate merged into main), only main's own commit should be listed.
  That failed before F3.

**Real branches:**
- 40 `PORT` lines: 38 main → candidate and 2 candidate → main. 10 skipped. It took about 11s with
  F3.
- `origin/main` and `origin/candidate` were unchanged afterwards.
- The 38 are mostly `main` work since the cut. That includes `949a2b7d4`, which F1 explains.
- One near-duplicate pair is listed on both sides: `4f74bbf6c` on main and `6fcc0c20e` on
  candidate. The candidate copy was adapted by hand, has a different patch-id and no footer, so
  the report is right to list it.

### 5. What the port prompt says on a conflict

**Before** (as it was on `candidate`):

> "Resolve conflicts so the change does on `<target>` what it did on `<source>`, and say in the
> commit message what you had to adapt."

It said nothing for a commit that cannot stand without an earlier change the target lacks. That is
exactly the situation in which task 1869 merged candidate into main.

**After F2**, step 3 adds:

> "If a commit cannot stand on `<target>` without an earlier change `<target>` does not have, do
> not merge `<source>` into your branch or `<target>` to get it -- that carries every unported
> commit across with it. Port just the change, adapted; if it cannot be adapted, stop and
> ask_question, naming the commit it depends on."

`port_test.go` asserts it.

### 6. Audit of the live branches (last 36h)

The audit script:
1. takes each `Backported-from` commit on a branch;
2. resolves the sha it names;
3. checks that the source is on the other branch, that the source is not itself a port, that the
   source was ported only once, and that the port was not ported back;
4. counts duplicate patch-ids on each branch.

**Ports:**
- There were 20 ports onto `candidate`, all correct: each source is on `main`, each was ported
  once, there is no port of a port, and nothing was ported back.
- **No ping-pong and no double ports.**
- The same 20 port commits are also on `main`.

**Why they are on `main`.** PR #2851 (task 1869, merge `57ddd280c`, 11:55 UTC) merged `candidate`
into `main`, carrying 38 commits: candidate's ports and its own commits. Five of the ports name
sources (`d6e8cd985`, `c067555aa`, `bdb724dbc`, `8abd23ef2`, `ff0ab25e8`) that are not on
`candidate` at all. They are candidate's ports of main commits made after the cut, so on
`candidate` only the port exists, as it should. The result:

| Branch | Duplicate patch-ids |
|---|---|
| `main` | 18 |
| `candidate` | 10 |

Each duplicate is an original sitting next to its own port. That is a back-merge, not a port loop:
the commit message of 1869's `949a2b7d4` says it was deliberate. See F1 and P2.

## Findings

### F1. A change whose head merged the other paired branch was never ported (bug, fixed)

**What happened.**
- `decidePort` treated any `Backported-from`/`cherry picked from` footer in the PR description as
  "this change is itself a port".
- A main-based branch that merged `candidate` carries candidate's ports, whose footers name `main`
  commits.
- `describeCommits` leads the description with one of those ports, so the branch's own change was
  skipped.
- This is real: task 1869's `949a2b7d4` is on `main` only, and today's triage lists it.

**The recipe broke too.** The port prompt's recipe also fails on such a head:
- `rev-list <merge>^1..<merge>^2` lists commits the target already has, and `cherry-pick` stops on
  them as empty.
- `git cherry origin/<target> <merge>^2` marks unrelated main-only commits `+`.

**Reproduction:**
- `TestAFooterNamingACommitTheBaseHasDoesNotStopThePort` (`pkg/orchestrator/port_test.go`) fails
  without the fix.
- The `backmerge:` case in `tests/e2e/port_walkthrough_test.go` failed with `ports of the
  back-merged 8 = 0, want 1`, and the daemon log said `the change is itself a port`.
- Recipe, in a scratch repo:
  ```sh
  git init -b main; c base; git branch candidate; c mainonly; git checkout candidate; c candonly
  git checkout -b feature main; git merge --no-ff candidate; c featurefix
  git checkout main; git merge --no-ff feature -m "Merge pull request #7 from o/feature"
  git rev-list --no-merges M^1..M^2      # featurefix, candonly  (candonly: empty pick)
  git cherry -v candidate M^2            # + mainonly, + featurefix  (mainonly is not this PR's)
  git cherry -v candidate M^2 M^1        # + featurefix  (the fix)
  ```

**Fix:** `6e38d99a0`.
- `portedOnto` counts a footer only if the commit it names is not on the branch the change merged
  onto. An unanswered compare still counts, as before.
- Step 2 of `portBody` is now `git cherry -v origin/<target> <merge>^2 <merge>^1`.
- Step 3 gains the conflict and dependency guidance quoted above.

### F2. The port prompt said nothing about a change that depends on something the target lacks (gap, fixed)

See step 5. Fixed in `6e38d99a0` and asserted in `TestAMergeOnAPairedBranchFilesAPortToTheOther`.

### F3. The triage reported patch-id equivalents that a back-merge had made shared (bug, fixed)

**Cause.** `rev-list --cherry-pick` compares only across the symmetric difference. After a
back-merge, a hand port is history both branches share, so the commit it was ported from is
reported as missing.

**Reproduction:** `TestBackportTriageScript_MixedPairListsOnlyTheMissing`. Before the fix, after
the back-merge it listed `PORT … main -> candidate: docs: same patch`.

**Fix:** `a767d09d5`. Each listed commit's patch-id is also checked against the newest 3000 commits
of the shared history (`PATCH_ID_DEPTH`), after the footer checks. On today's real branches the
counts are unchanged, because those commits already carry footers.

### F4. A `Port: no` on any commit but the lead was dropped, and the change was ported (bug, fixed)

**Cause.** `describeCommits` writes only the lead commit's message in full and lists the others by
summary line. The merge queue reads the opt-out from that description.

**Reproduction:**
- `describeCommits` with `feat: x\n\nWhat it does.` followed by `chore: y\n\nPort: no` produces a
  description without `Port: no`, and `portOptOut` does not match.
- `TestPortNoOnALaterCommitStillOptsOut` fails without the fix ("description lost the second
  commit's Port: no").

**Fix:** `0d196db36`. The description carries a `Port: no` line under the lead message when any
commit has one.

### F5. Footers are still read from the description, not the change's commits (design, filed: P1)

F1 and F4 patch symptoms. What remains:
- a candidate-only `Port: no` commit pulled in by a back-merge suppresses the port of a main
  change;
- `capDescription` truncation;
- the description is rewritten at every refresh.

The proper fix reads the PR's commits at merge time (`GET /pulls/{n}/commits`), which needs a new
`github.Client` method and githubsim support. That is larger than this task, so it is proposed
separately.

### F6. Ordinary tasks are not told not to merge one paired branch into the other (gap, filed: P2)

Task 1869's back-merge is the cause of the duplicate patch-ids on `main` and of the missed port in
F1. Only the port prompt now says not to do this.

### Observations (no change)

- **Every port carries `grain-ci`** (`newPortTask`), whatever the repository's own CI gate. This is
  by design ("lands through grain CI"). In a harness with no grain CI, a port's merge waits for a
  verdict forever, which is why the e2e test needed `greenCI`.
- **`949a2b7d4` still needs its port onto `candidate`.** The nightly triage lists it, and its
  review task files ports, so it is not ported by hand here, as the plan's standing notes say.

## Proposed follow-ups

- **P1.** "Decide port opt-outs and port footers from a pull request's own commits, not its
  description". Depends on 1828.
- **P2.** "Tell agents never to merge one paired branch into the other".

## Formatting and security

- `gofmt -l` is clean.
- `npx prettier --write` was run on the new spec.
- `make security` passes: `go vet ./...` and `tests/security` in 105.9s.
- No `.grain/baselines/` file was touched.

## CI verdict

Grain CI run `17b1d74b8f69e6a9` on `b25f1c5` (all fixes and tests, before this report): **success**.
- frontend 5m6s
- go 15m31s
- installer 3m51s
- lint 23s
- package cache 1m57s
- postgres 1m34s
- terraform 41s
- ui e2e 10m39s

The report commit's own full-suite run is in the task's closing note.

## Appendix: audit script

```bash
#!/usr/bin/env bash
# Audits Backported-from commits on main and candidate for double ports and ping-pong.
set -u
SINCE="${SINCE:-36 hours ago}"
for pair in "main candidate" "candidate main"; do
  set -- $pair; X=$1; Y=$2
  echo "=== ports on $X (since $SINCE)"
  git log origin/$X --since="$SINCE" --no-merges --grep 'Backported-from' --format='%H' | while read -r c; do
    subj=$(git log -1 --format=%s "$c")
    srcs=$(git log -1 --format=%B "$c" | grep -oiE 'Backported-from:[ ]*[0-9a-f]{7,40}' | grep -oE '[0-9a-f]{7,40}$')
    for s in $srcs; do
      full=$(git rev-parse -q --verify "$s^{commit}" 2>/dev/null || echo MISSING)
      onY=no; [ "$full" != MISSING ] && git merge-base --is-ancestor "$full" origin/$Y && onY=yes
      srcIsPort=no; [ "$full" != MISSING ] && git log -1 --format=%B "$full" | grep -qiE 'Backported-from:' && srcIsPort=yes
      n=$(git log origin/$X --no-merges --grep "Backported-from: *${s:0:12}" --format=%h | wc -l)
      back=$(git log origin/$Y --no-merges --grep "Backported-from: *${c:0:12}" --format=%h | tr '\n' ' ')
      flag=""
      [ "$onY" = no ] && flag="$flag SOURCE-NOT-ON-$Y"
      [ "$srcIsPort" = yes ] && flag="$flag PORT-OF-A-PORT"
      [ "$n" -gt 1 ] && flag="$flag PORTED-${n}x"
      [ -n "$back" ] && flag="$flag PORTED-BACK($back)"
      printf '%s <- %s%s  %s\n' "${c:0:9}" "${s:0:9}" "${flag:- ok}" "$subj"
    done
  done
done
echo "=== patch-id duplicates within each branch since $SINCE"
for X in main candidate; do
  git log origin/$X --since="$SINCE" --no-merges --format=%H | while read -r c; do git show "$c" | git patch-id --stable; done | sort | awk '{print $1}' | uniq -d | while read -r p; do
    echo "$X: duplicate patch-id $p"; done
done
```
