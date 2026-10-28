# 2026-10-20: repo tab paths and the Settings → Repositories index

Task 4909 implements [docs/design/settings-panel.md](../design/settings-panel.md)
§2.3 and the `/repos/<o>/<n>/settings/<tab>` row of §3.5. Task 4893 had found
both missing on `main` at 7ea12f4e9.

- The repo page's tab is part of its path. `paths.js` parses it into `repoTab`
  and `buildPath` writes it back. Overview stays at `/repos/<o>/<n>`.
- Old `#<tab>` links still open their tab, and the first address written moves
  the tab into the path.
- Settings → Repositories shows one row per repository: base branch,
  declarations, CI mode and Git connection. Each value links to the tab that
  changes it. The connection value links to Settings → Git instead.
- The index reads only endpoints the UI already used: `GET
  /api/repos/{o}/{n}/declarations` and `.../release`, which RepoPage reads, and
  `GET /api/deployment/reachable-repos`, which the Add-repo picker reads. No API
  change was made.

## How it was run

- `grain demo` was built from this branch (c4fc910cc plus the spec fix in this
  commit) and started by the Playwright fixtures (`ui/e2e/fixtures.js`) on a
  throwaway store. The frontend was built with `npm run build`.
- Browser: Playwright 1.62.1 Chromium headless, Node 20.19.0, Go 1.26.6.
- No grain API was mocked or stubbed, and nothing external was stubbed either.
  The demo has no GitHub reach configured, so `reachable-repos` answers
  `available: false`, and the Git connection column shows "Not listed".

## What the demo's index showed

| Repository | Base branch | Declarations | CI mode | Git connection |
|---|---|---|---|---|
| acme/widgets | Default branch | Not read | GitHub CI | Not listed |
| grain/config-default | Default branch | Not read | GitHub CI | Not listed |

"Default branch" means grain has no watched base-branch row for the repository
yet, so `baseBranches` is empty.

## Checks

| Spec | Check | Result |
|---|---|---|
| `settings-panel-walkthrough.spec.js` "Repositories lists each repo's settings and links into its tab" | The headers are Repository, Base branch, Declarations, CI mode, Git connection. acme/widgets' row fills every column, and its Declarations value matches `GET .../declarations` | pass |
| same | Following the row's CI mode link opens `/repos/acme/widgets/settings/ci` with the CI tab selected. A reload stays on the tab, and Back returns to `/settings/repositories` | pass |
| same | Following the row's Declarations link opens `/repos/acme/widgets/settings/advanced` | pass |
| `settings-panel-walkthrough.spec.js` (the rest, 8 tests) | unchanged | pass |
| `repo-tabs-walkthrough.spec.js` | Every tab is loaded at its own path. Back and Forward step between them, and a tab picked on the page writes `/settings/merge-queue` | pass |
| same | An old `/repos/acme/widgets#branches` link opens Branches, and the address becomes `/settings/branches` | pass |
| same | Unsaved CI edits survive a tab change. Go to CI Mode lands on `/settings/ci` | pass |
| `processes-walkthrough.spec.js` | A trigger row opens `/repos/<o>/<n>/settings/advanced`, and Back returns to `/processes` | pass |
| `repository-release-memory-1015-walkthrough.spec.js` | Changing only the hash (`#ci`) still switches the tab | pass |
| `merge-rules`, `port-settings`, `repo-page-unsaved-edits`, `standing-work-switch`, `repo_release_memory_edits`, `workflow-schedules-candidate`, `path-triggers-sensitive-hold`, `repo-ci-capabilities`, `release-run-ref-checks` walkthroughs | Each opens a repo page by an old `#<tab>` link, and those links still open their tab | pass (23 tests) |
| `repository_release_memory_walkthrough.spec.js` | Back to the repo page lands on `/settings/agents`, where it used to land on `#agents`. Its URL pattern was widened to match | fail before the pattern change, pass after |
| `repo-pane.spec.js` | Back out of Releases lands on the repo page on the tab it was showing (`/settings/agents`). It used to land on the bare path with the tab reset to Overview. The expectation was updated | fail in grain CI before the update, pass after |
| `base-branch-gate-warnings`, `repository-release-memory-1012`, `repository-release-memory-regressions`, `responsive-navigation`, `repos_workflows`, `keyboard-open-rows`, `paths` | These specs assert a repo page's URL | pass (63 tests, together with `repo-pane`) |

Unit tests:

- `paths.test.js` covers every tab's round trip, Overview, unknown tabs, a repo
  off the default host, and the trigger redirect.
- `RepositoriesSection.test.jsx` covers the columns, the links, and the "…",
  "Unknown", "Not listed" and "Not reached" states.
- `RepoPage.test.jsx` and `App.test.jsx` pass unchanged.
- In `App.repos.test.jsx`, the pull request page's way back to the repo now
  expects `/repos/acme/other/settings/branches`, the tab its compare form is
  on.
- The whole unit suite passes: 192 files, 3017 tests.
