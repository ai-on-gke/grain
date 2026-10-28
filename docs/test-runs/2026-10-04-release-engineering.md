# 2026-10-04 — Release engineering walkthrough (test plan §5)

Executes §5 of `docs/test-plans/2026-10-04.md`, "Release engineering". Task 1831.

## Environment

- Debian sandbox (Linux 6.1), Go 1.26.6, Node 20.19.0, Playwright 1.62.1 (Chromium from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), git 2.39.5, ShellCheck 0.9.0.
- UI built with `cd ui && npm ci && npm run build`. The production bundle in `pkg/ui/static` is
  embedded into `go build -o /tmp/grain ./cmd/grain`.
- `grain demo -addr 127.0.0.1:0 -open=false` on its own throwaway SQLite store, plus a second
  instance with `-fake-ci`. The e2e specs start their own per-worker demos (`ui/e2e/fixtures.js`).

## Tested commit

`candidate` at `6772d7c8a` (Merge pull request #2855). All six commits under test are on
`origin/candidate`, confirmed with `git branch -r --contains`:

| Commit      | Subject                                                                 |
| ----------- | ----------------------------------------------------------------------- |
| `7a3e0fc27` | feat(release): write release notes into docs/releases for every release |
| `73c63779b` | fix(release): count the candidate cadence in release days, not hours    |
| `2f36cf0d1` | fix(release): make the latest image tag follow the last promoted release |
| `3df0d2fd0` | fix(upgrade): give every branch its own image tag                       |
| `3e4823e71` | feat(settings): add an editable deployment id for task branch names     |
| `56cf8e574` | feat(demo): add -fake-ci so release-run ref checks can be walked end to end |

## Stubs

Only external boundaries were stubbed. Grain's own APIs and React were not.

| Boundary | Stub |
| --- | --- |
| GitHub (git remote) for the cut | A local bare repository (`/tmp/rel/bare.git` and copies), cloned from this checkout, with refs rewound to the state being replayed. `origin` in each scratch clone points at it, so every `git push` lands there. |
| GitHub API in the cut (`gh` checks, draft release) | None needed: `gh` is not installed and `GITHUB_TOKEN` is unset, so the script takes its "not available" branches. |
| Wall clock in the cut | `RELEASE_CUT_NOW` (epoch seconds), plus `GIT_COMMITTER_DATE` so the tags the replay creates carry the replayed time rather than today's. |
| ghcr / GitHub Packages for `ghcr-prune` | `cmd/ghcr-prune`'s existing `fakeAPI` (package versions) and `github.FakeTransport` (the branch listing read by the real `ListBranchTags`). The run is a dry run. No registry is reachable from the sandbox. |
| CI execution under `-fake-ci` | `cmd/grain/demo_fakeci.go`: the demo's own stub `ui.CIRunner`, which records runs and never executes them. Everything in front of it is the real server. |

## Commands and outcomes

### Step 1: release notes

```
git fetch --tags
scripts/release-notes.sh v1.6.0 v1.5.0 v1.6.0 > /tmp/notes.md
diff /tmp/notes.md docs/releases/v1.6.0.md
```

- **Without highlights:** 287 lines generated against 298 committed. The only difference is the
  committed file's hand-written `## Highlights` block (lines 6–16). Features, Fixes and Other
  changes match line for line.
- **With highlights:** the committed highlights bullets (`sed -n 8,15p docs/releases/v1.6.0.md >
  /tmp/hl.md`) passed as the fourth argument reproduce `docs/releases/v1.6.0.md` byte for byte.
- **Empty or missing highlights:** an empty highlights file adds no Highlights section, as
  intended. A path that does not exist was also silently treated as "no highlights" (finding 1,
  fixed).
- **The cut's notes step, file already on main:** the replay below with `main` = `f56fe0d45`, which
  already carries `docs/releases/v1.6.0.md`, printed "Release notes docs/releases/v1.6.0.md are
  already on main; leaving them as they are" and left the file unchanged.
- **The cut's notes step, file absent:** the same replay on a scratch `main` with the file
  removed, and `RELEASE_NOTES_HIGHLIGHTS=/tmp/hl.md`, committed `docs(release): add release notes
  for v1.6.0` to the bare `main`. That file is identical to the committed `docs/releases/v1.6.0.md`.

### Step 2: cadence, with the real timeline replayed

The real tags: `v1.6.0-rc.1` was tagged 2026-10-03 04:02:42Z. The plan's Gaps list calls this the
"10-02 late tag": it is the late cut belonging to the 10-02 release day. `v1.6.0` was tagged
10-03 23:54Z and `v1.7.0-rc.1` 10-04 01:10Z, both by hand.

**Run A: the true 10-03 18:00Z state.**

- Setup: `main` = `3b58a13cf`, the first-parent tip before 18:00Z. `candidate` = `7eb143523`, the
  first-parent tip of `v1.6.0`'s line before 18:00Z. Tags up to `v1.6.0-rc.1`.
- Command:

  ```
  GIT_COMMITTER_DATE=2026-10-03T18:00:00Z RELEASE_CUT_NOW=$(date -ud 2026-10-03T18:00:00Z +%s) \
    bash scripts/release-candidate-cut.sh
  ```

- Cadence: "cut 13 hours (1 release days) ago". It proceeds. Under the old hour count this run cut
  nothing, which is what `73c63779b` fixed.
- **Back-merge count:** "53 commit(s) on origin/candidate not on main, 53 of them with no
  equivalent patch on main (missed ports)".
- The back-merge then conflicts in 27 files, and the script exits 1. `v1.6.0` and `latest` were
  already tagged and pushed to the bare remote, and the notes were discarded (finding 2).

**Run B: the cut path, with the conflict resolved the way it was for real.**

- Setup: `main` = `f56fe0d45` (the hand back-merge that became `v1.7.0-rc.1`), `candidate` =
  `d49c62ef1` (`v1.6.0`), and the same clock.
- Cadence: 1 release day. **Back-merge count: 0 (0 missed ports).** Notes are already on main and
  left alone.
- Result: `v1.7.0-rc.1` on `f56fe0d45`, and `v1.6.0` and `latest` on `d49c62ef1`, all in the bare
  remote. Exit 0.

**Same-day reruns after run B**, each on a fresh copy of run B's bare remote:

| `RELEASE_CUT_NOW` | Output                         | Outcome           |
| ----------------- | ------------------------------ | ----------------- |
| 10-03 19:00Z      | 1 hour, **0 release days**     | "Skipping release cut" — **refused** |
| 10-04 11:59Z      | 17 hours, 0 release days       | refused           |
| 10-04 12:00Z      | 18 hours, 1 release day        | cuts `v1.8.0-rc.1` |

The release day starts at 12:00Z: 18:00 less the 6 h slack, as documented.

### Step 3: deployment id, browser, `grain settings` and `grain sync`

**Browser.** The new spec `ui/e2e/deployment-id-walkthrough.spec.js` drives this on a real demo,
signed in as the demo's admin. It passes in Chromium.

- The field is under Team settings → **Models** tab → Task branches. The plan says "Settings →
  Task branches" (finding 4).
- `Bad_ID` and a 33-character id are each refused with an error naming `deploymentId`. Nothing is
  stored (`GET /api/settings` keeps `deploymentId: ""`), and the box reverts.
- `blue` is saved, then the template `grain/{deployment}/task-{id}` is saved.
- A task filed through **+ New task** (repo `acme/widgets`) gets `grain/blue/task-13` in both
  `GET /api/tasks` and `GET /api/tasks/13`.
- A seeded task keeps `grain/task-1` after a reload.
- A task filed with no repo has no branch at all, as intended. The task pane shows no branch name
  until something has been pushed (finding 3).

**`grain settings`**, with `GRAIN_SERVER` pointed at the manual demo:

- `grain settings` shows `deployment id: 08bedb02 (default)`.
- `-deployment-id Bad_ID` and a 33-character id both exit 1 with the same messages as the UI.
- `-deployment-id green -task-branch-template 'grain/{deployment}/task-{id}'` exits 0.
  `POST /api/tasks` then yields `grain/green/task-13`, and task 1 is still `grain/task-1`.
- `-deployment-id ""` resets the id to the default.

**`grain sync`**, with `GET /api/settings` saved as the export, and a sync file of
`{"settings":{"deploymentId":"purple","taskBranchTemplate":"grain/{deployment}/task-{id}"}}`:

- `-dry-run` plans only `deployment id: "green" -> "purple"` (the template is unchanged).
- `Bad_ID` is refused in both plan and apply, exit 1.
- Apply prints the same change, and a second `-dry-run` says "already up to date".
- The next task is `grain/purple/task-14`. Task 13 is still `grain/green/task-13` and task 1 is
  still `grain/task-1`.
- Feeding the whole `GET /api/settings` document back to `grain sync` fails with "claudeModel
  cannot be empty" (finding 5).

### Step 4: `-fake-ci`

- `grain demo -fake-ci -observability` and `grain demo -observability -fake-ci` both exit 2 with
  "-fake-ci and -observability each wire a CI of their own; pick one".
- `npx playwright test release-run-ref-checks-walkthrough` passes.
- **By hand:** with `PUT /api/repos/acme/widgets/ci-mode {"ciMode":"grain"}` set and signed in as
  `sysadmin`, `POST /api/repos/acme/widgets/release-runs {"branch":"main"}` returns 202
  (`fake-ci-1`).
- `/ci` lists it as **Pending**, with "took" counting up and "no jobs".
- The CI page's own "Start a run" cannot start one, because the stub offers no workflows ("No
  workflows on this branch accept a manual run"), so runs come from the release-run API.
- Opening the run (its repo link) and pressing **Cancel run** (answering the overlay's `prompt()`)
  leaves it cancelled, with the reason stored. The other run stays Pending.
- This is now the second test in `release-run-ref-checks-walkthrough.spec.js` and passes.

### Step 5: `ghcr-prune` and legacy `-` tags

No test covered this. `3df0d2fd0`'s message says a slash branch's old dash tag is orphaned and
"ghcr-prune ages it out", and `BuildPlan` does exactly that: an unknown dash tag is `KindBranch`,
not in the `..`-spelled live set, so the age window alone holds it.

The new test `TestLegacyDashBranchTagsAgeOutAfterTheMigration` (`cmd/ghcr-prune/main_test.go`)
covers it. It reads the branch list through the real `ListBranchTags` over a fake GitHub
transport, then dry-runs `prune` over a migration-shaped registry with a 48 h window. Results:

- The old dash tag of live `grain/task-232`, 5 days old, is the only version it would delete.
- A dash tag 20 h old is kept.
- `grain..task-232`'s head build is kept at any age.
- A branch literally named `grain-task-9` keeps its tag.
- Nothing is deleted.

`plan.go`'s doc comment still said live branch tags use `-`, and is corrected.

## Findings

1. **A missing highlights file was silently ignored. Fixed.**
   - Repro: `scripts/release-notes.sh v1.6.0 v1.5.0 v1.6.0 /tmp/nonexistent.md` exits 0 with no
     Highlights section. In the cut, `RELEASE_NOTES_HIGHLIGHTS=/tmp/typo.md` promotes and commits
     notes without highlights.
   - Since a committed notes file is never rewritten, the mistake could only be fixed by hand.
   - Fix: `release-notes.sh` exits 2 for a named file that does not exist.
     `release-candidate-cut.sh` refuses the variable before it fetches, tags or pushes anything.
   - Regression test: `TestReleaseCandidateCutScript_ReleaseNotesFileRules` in
     `tests/deploy/release_notes_test.go`. It fails on the unfixed scripts ("a cut naming a
     highlights file that does not exist succeeded"). It also pins that notes already on main are
     left byte-for-byte alone.
2. **A conflicting back-merge leaves the promotion pushed and the notes discarded. Filed as a
   follow-up task (proposed by this run, not auto-merged).**
   - Repro: run A above. It is deterministic: the replayed refs are fixed commits and the clock is
     pinned.
   - The script pushes `v1.6.0` and `latest` before Step 2's back-merge. On conflict it exits 1
     before Step 2b, so the generated notes are lost. This matches 10-03 in reality, when the notes
     were added by hand in `2fc679b3a`.
   - From reading the code: a rerun force-moves `v1.6.0` locally, but its non-forced push is
     rejected and the script swallows that.
   - Not fixed here: the right order of promotion and back-merge is a design call.
3. **The task pane shows no branch name until it has been pushed.** `DetailOverlay` only renders
   the branch as a compare link once `useBranchPushed` says it exists. So the plan's "check the
   task pane's branch" cannot be done for a fresh task, and the new `{deployment}` naming is only
   visible through the API or CLI until a run pushes. Recorded only; this is a UI choice, not a
   regression.
4. **The deployment-wide Task branches section lives in a team's Models tab.** The deployment id
   and the template are deployment-wide, but they sit at the bottom of Team settings → Models,
   after the team's model panel. Recorded only.
5. **`GET /api/settings` does not round-trip through `grain sync`.** `{"settings": <GET
   /api/settings>}` fails planning with "claudeModel cannot be empty" on a demo. `grain sync`
   does not claim the settings read is a valid sync file, so this is recorded rather than filed.
   Partial sync files work.
6. **`go vet ./...` was red on `candidate`. Fixed.** Repro: `go vet ./tests/e2e/` on `6772d7c8a`
   fails with "bareDeclarations redeclared in this block". The §2 port walkthrough and the §3
   path-triggers walkthrough each declare `bareDeclarations`. This broke `make security` and the
   `go` CI job on `candidate` and its merge queue. Renamed the port walkthrough's copy to
   `portBareDeclarations`.

## Fixes in this change

- `scripts/release-notes.sh` and `scripts/release-candidate-cut.sh`: refuse a missing highlights
  file (finding 1). Covered by the new tests/deploy case. ShellCheck is clean.
- `cmd/ghcr-prune/plan.go`: doc comment corrected. New test in `main_test.go` (step 5).
- `tests/e2e/port_walkthrough_test.go`: type renamed (finding 6).
- `ui/e2e/deployment-id-walkthrough.spec.js` (new) and a second test in
  `ui/e2e/release-run-ref-checks-walkthrough.spec.js`: steps 3 and 4 as regression coverage.

## Formatting and security

- `gofmt -l` reports nothing. `npm run format` changed nothing further.
- `shellcheck scripts/release-notes.sh scripts/release-candidate-cut.sh` is clean.
- `make security` was red on the base because of finding 6. With the fix it passes locally: `go vet ./...` is clean and `tests/security` is ok in 108 s (see also the CI
  verdict). No `.grain/baselines/` count changed.

## CI verdict

See the closing note on task 1831 for the final full-suite `wait_for_checks` result on the
branch's last commit.
