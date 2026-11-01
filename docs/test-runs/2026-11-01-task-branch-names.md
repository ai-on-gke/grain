# 2026-11-01: Task branch names (slug upgrade, MaxBranchNameLen, refs git cannot push)

Task 2219 (`grain/049f1e09/task-2219`) works through §4 of
`docs/test-plans/2026-11-01.md`. In scope: PR #282 (`9f7044fe` schema 78,
the slug upgrade; `ba2f7674` the length cap; `cc92abf3` the sample ID in
refusals; `cb564cfd` capped PR titles) and #236 (`dad9910e`, git ref rules).

**Result: every §4 check passes, and the walkthrough found one bug, now
fixed on this branch.** `grain state prepare-upgrade` could not upgrade a
store from before schema 74 (see Bugs).

## Commit and environment

- **Commit tested**: `30f47366` (`main`, "Merge pull request #362"), plus
  the fix on this branch. The old build is `8bdfa3a5` (schema 65), built
  from `git worktree add /var/tmp/old 8bdfa3a5`.
- **Build**: `npm ci && npm run build` in `ui/`, then `go build ./cmd/grain`,
  for both the old and the new binary.
- **Daemons**: `grain daemon -data-dir … -ui-addr 127.0.0.1:1808N
  -sandbox-port-base=0 -disable-native-sandboxes`. `sqlite3` (installed
  with apt) reads the store.
- **Pushes and PRs**: `tests/e2e/TestTaskBranchNamesPushAndOpenPullRequests`
  (new) runs a real `grain daemon` from `grain init workstation`, with host
  sandboxes and the real git proxy.
- **Browser**: Playwright driving Chromium (`/var/cache/playwright`)
  against the worker's `grain demo` (`ui/e2e/fixtures.ts`), at 1440×900
  and 390×844.

### Stubbed boundaries

- **The agent.** A custom-backend shell script commits one change to
  `NOTES.md` and runs `git push origin HEAD` through grain's git proxy. No
  model key is available in a task sandbox.
- **The forge.** GitHub is `githubsim` over a bare repository on disk,
  served on loopback. The sandbox has no GitHub access. `git ls-remote`
  runs against that bare repository.
- **Not `grain demo` for pushes.** `grain demo` has no orchestrator, so
  none of its runs push a branch or open a pull request; with
  `-git-proxy` it only mints a token. The plan's alternative, a daemon
  with a stub framework, is the e2e test above.
- **No-repo tasks for the upgrade store.** The old daemon had no GitHub
  credential, so the old and new tasks in step 1 are `-no-repo` tasks.
  Their branches are frozen at filing the same way.

## Steps and observations

### 1. Pre-78 store upgraded on a real data dir: PASS (after the fix)

1. `grain-old daemon` (8bdfa3a5) ran once on `/var/tmp/d1` and was
   stopped. The store is at schema 65. Its seeded template was already
   `grain/{deployment}/task-{id}-{slug}`, so it was set to
   `grain/{deployment}/task-{id}` with `sqlite3`. The old daemon was then
   started again, and it exported that value to
   `state-repo/tables/grain_config.json` (state repo at schema 65).
2. `grain-old create -no-repo -title "Old task before the slug upgrade"`
   froze task 2 on `grain/d0e20a96/task-2`.
3. A copy of this data dir (`d1-pre78-backup`) was kept for the import
   path.

| | template (store) | template (state repo) | task 2 (old) | task 3 (new, filed after) |
|---|---|---|---|---|
| before, schema 65 | `grain/{deployment}/task-{id}` | `grain/{deployment}/task-{id}` | `grain/d0e20a96/task-2` | — |
| after, in-place migration (d1) | `grain/{deployment}/task-{id}-{slug}` | `grain/{deployment}/task-{id}-{slug}` | `grain/d0e20a96/task-2` | `grain/d0e20a96/task-3-new-task-after-the-slug` |
| after, state-repo import (d2) | `grain/{deployment}/task-{id}-{slug}` | `grain/{deployment}/task-{id}-{slug}` | `grain/d0e20a96/task-2` | `grain/d0e20a96/task-3-new-task-after-the-slug` |

- **Binary swap alone.** Starting `main`'s binary straight on d1
  migrated the store to 82 (migration 78 rewrote the template). The
  daemon then refused to start: the state-repo dump is at 65 and nothing
  says the schema changed under it. This is the designed guard
  (`pkg/daemon/schemaupgrade.go`; `scripts/setup.sh` handles it), not a
  bug. `grain state -data-dir /var/tmp/d1 reformat` followed by a start
  brought d1 up. Its state repo was rewritten at 82 with the slugged
  template.
- **Importing the pre-78 `tables/grain_config.json` through the state
  repo.** This is `grain state -data-dir /var/tmp/d2 prepare-upgrade` on
  a fresh copy of the schema-65 data dir. It exports the store and
  rehearses importing it at 82 (`staterepo.ImportMigrated`, so dump
  version 65 < 78). The next start imports it. The dump's
  `grain_config.json` still says `grain/{deployment}/task-{id}`, and the
  imported store and the rewritten repo say `…-{slug}`.
- **Bug found here.** On `main`, `prepare-upgrade` failed with `this
  build could not take the rows exported from the store at schema 65 …
  inserting schedule row 0: NOT NULL constraint failed:
  schedule.keep_running`. This is fixed on this branch; after the fix it
  reports `rehearsed: 44 rows across 15 tables migrate from schema 65
  into 82`.
- `TestADumpBefore78UpgradesUnsluggedTaskBranchTemplate` (pkg/staterepo)
  and the pkg/model/sqlite migration tests pass.

### 2. Real pushes: PASS

`TestTaskBranchNamesPushAndOpenPullRequests` ran on a deployment with
the longest ID allowed (32 × `d`). For each task, the stub agent pushed
through the proxy and grain opened a pull request on githubsim. The agent
commits with a 300-character summary line, which is what names the PR.
`git ls-remote <bare> refs/heads/<branch>` lists every branch.

| title | branch | bytes | PR title |
|---|---|---|---|
| 300 characters ("Make the very long task title survive branch naming …") | `grain/ddd…ddd/task-2-make-the-very-long-task-title` | 75 | 200 × `x` + `...` |
| `!!! ??? ... --- ***` | `grain/ddd…ddd/task-3` (no slug, no trailing hyphen) | 45 | capped, as above |
| `修复 ünïcödé — 日本語のタイトル` | `grain/ddd…ddd/task-4-n-c-d` | 51 | capped, as above |

- Every branch passes `model.ValidBranchName`, so it is at most 244
  bytes, and `git push` accepted it. Every PR title is ≤ 203 bytes and
  ends in `...`.
- **Not exercised end to end:** the fallback to the task's own title
  (`capTitle(commitSummary(task.Title))`). It only applies when the
  branch has no non-merge commit, and then there is nothing to push. It
  is covered by `cb564cfd`'s orchestrator test.

### 3–4. Near-limit custom template and a long deployment ID: PASS

- **Longest accepted template.** `team/` + 181 × `p` +
  `/{deployment}/task-{id}-{slug}` (186-byte prefix) is the longest that
  UpdateSettings accepts. It is validated with a 19-digit task ID and a
  32-character deployment ID, giving exactly 244 bytes with no slug. One
  more `p` is refused: `branch template produces 245-character branch
  name, over the 244-character limit`.
- **Trimmed slug.** Two tasks (5 and 6) with the same 300-character title
  got `team/ppp…/ddd…ddd/task-5-make-the-very` and `…/task-6-make-the-very`,
  240 bytes each. `{slug}` was trimmed at a word boundary to the 17 bytes
  left. The branches differ only by `{id}`, so they cannot collide. Both
  pushed and opened a PR.
- **When `{slug}` is omitted.** It is dropped entirely only once the real
  task ID is within a byte of the 19 digits the validation reserves. That
  does not happen on a real deployment.
  `TestFormatTaskBranchNameCapsTotalBranchLength` covers it in pkg/model.

### 5. Settings → Agents → Task branches (Chromium, 1440 and 390 px): PASS

The new spec test is "refuses each unpushable or over-length template at
{1440,390}px". At each width, each of these templates was typed and saved
with Enter:

| template | refusal (`role=alert`) |
|---|---|
| `grain/task-{id}.lock` | `… produces invalid branch name "grain/task-123.lock"` |
| `grain/.hidden/task-{id}` | `… produces invalid branch name "grain/.hidden/task-123"` |
| `grain/task-{id}.` | `… produces invalid branch name "grain/task-123."` |
| 187-byte-prefix template | `… produces 245-character branch name, over the 244-character limit` |

- For every refusal, the status reads "Couldn't save Branch name
  template", the field reverts to the stored value, and the alert's
  bounding box lies inside the viewport.
- Each refusal names the sample task 123 (`cc92abf3`), not the 19-digit
  worst case.
- Saving the near-limit template shows "All changes saved", the refusal
  is gone, and it survives a reload.
- **Wording.** The alert prefixes the field label, so it reads "Branch
  name template branch template produces …". It is readable but says
  "template" twice; cosmetic, not changed here.
- The 390 px screenshot was looked at (attached to the closing note,
  downscaled). The alert sits under the header, nothing overflows
  horizontally, and the long template scrolls inside the input.

## Bugs

### Fixed: `grain state prepare-upgrade` cannot upgrade a store from before schema 74

**Deterministic repro (on `main` at 30f47366):**

```sh
git worktree add /var/tmp/old 8bdfa3a5 && (cd /var/tmp/old && go build -o /var/tmp/grain-old ./cmd/grain)
/var/tmp/grain-old daemon -data-dir /var/tmp/d -ui-addr 127.0.0.1:18081 -sandbox-port-base=0 -disable-native-sandboxes &  # wait for "seeded schedule", then stop it
go build -o /var/tmp/grain ./cmd/grain
/var/tmp/grain state -data-dir /var/tmp/d prepare-upgrade
# grain state: this build could not take the rows exported from the store at schema 65, so nothing is armed and the store is untouched: staterepo: inserting schedule row 0: constraint failed: NOT NULL constraint failed: schedule.keep_running (1299)
```

**Cause.** `prepare-upgrade` has the new build export the old store. The
settings documents (`settings/schedules.yml`, templates, suites) are
written field by field from the new build's schema. `declEntry` wrote a
field whose column the old store does not have as `keep-running: null`.
Importing that put NULL into the `NOT NULL DEFAULT 0` column added at
schema 74. Any deployment upgraded with `scripts/setup.sh` from before
74 hits this, because every deployment has seeded schedules.

**Fix** (`pkg/staterepo/decl.go`). A field is left out of the document
when the row has none of its columns, so the import takes the column's
default. That is what already happens for a dump an older build wrote.

**Regression test.** `TestAStoreWithoutAColumnExportsWithoutTheKey`
(`pkg/staterepo/addedcolumns_test.go`) drops `schedule.keep_running`,
exports, checks the key is absent and imports. It fails without the fix.

### Filed

None.

## Regression tests added

- `pkg/staterepo/addedcolumns_test.go`:
  `TestAStoreWithoutAColumnExportsWithoutTheKey`.
- `tests/e2e/task_branch_names_walkthrough_test.go`:
  `TestTaskBranchNamesPushAndOpenPullRequests` (about 17 s).
- `ui/e2e/settings-agents-branch-template.spec.js`: the per-viewport
  refusal test.
