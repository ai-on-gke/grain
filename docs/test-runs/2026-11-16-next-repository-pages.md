# `/next/` repository pages: list, Advanced tab, classic ports and the base-gate warning (2026-11-16)

Walkthrough of [`docs/test-plans/2026-11-16.md`](../test-plans/2026-11-16.md) §8, filed by
[Task #6330](/tasks/6330) and run as [Task #6481](/tasks/6481). It covers:

- the `/next/repos` list and the repository page's Advanced tab (PR #1599);
- the classic ports (PR #1595): Work ownership, the Qualification plan editor, CI capabilities and
  the issue-import "Every (minutes)" box;
- the base-gate warning (PR #1592);
- Settings → Repositories (PR #1589).

## Summary

| Item | Outcome |
|---|---|
| 1. Round trips against a real daemon | **Pass.** These saves from `/next/` read back the same through the API, `grain repo`, classic `/repos/acme/widgets` (plan) and the `grain/config` export: work ownership (one item off, one on, then the default off), a two-item qualification plan with a dependency and a repeat, a CI capability, and a 15-minute issue-import interval. `pkg/release` validation refuses a dependency cycle with 400. |
| 2. Config-repo-managed repositories | **Two bugs, both fixed.** With an external `grain/config`, the server refuses every write with 409 `config_managed_externally`. CI capabilities, the plan and the issue-import box were already read-only and said where to edit. But **Work ownership kept its switch and its On/Off/Say nothing buttons live**, so every click got a 409. And **`/next/repos` kept offering Add repo**, which also gets a 409. Both now disable their controls and name the file to edit. |
| 3. Roles | **Pass.** `grain/viewer` and `grain/contributor` (neither holds `repos:write`) see disabled cards, each naming the scope it needs. The server refuses their writes with 403s that say why. A declared `repo-writer` role (viewer plus `repos:write` on acme/widgets) can save ownership. It is refused CI capabilities (`capabilities:admin`) and another repository. |
| 4. Add repository | **Bad name: pass** (refused beside the box, `aria-invalid`). **Duplicate: silent** (200, no message, nothing changes). **Missing git connection: bug, fixed.** `PUT /api/deployment/repos/{owner}/{name}/config`, which the repo page's "Save config" sends, stored a `gitCredential` the deployment does not hold. Pinning the same credential through `…/git-connection` answers 400. |
| 5. Base-gate warning truthfulness | **No CI: true. Grain CI with a run that judged nothing: true. GitHub: not true** (reported, not changed; see below). |
| 6. Layout at 1440/390 px, light/dark | **Pass.** No element ran past the viewport on any tab (Overview, Branches, CI, Releases, Advanced) or on the list, at either width or in either scheme. The page had the saved two-item plan in it. |

## Environment

- Commit walked: `origin/main` `46fdc1104`. Fixes are on top of it in this branch.
- `make frontend && go build -o /home/debian/bin/grain ./cmd/grain`, rebuilt after the fixes.
- A real `grain daemon -sandbox-port-base=0 -host-sandboxes` over SQLite (modernc; sqlite3 CLI
  3.40.1 for the store edits).
- The harness is the 2026-11-13 next-ui-complete one (`docs/test-runs/2026-11-13-next-ui-complete/env.sh`):
  - `ghsim` serves five `acme/*` repositories over git-http-backend;
  - `iapsim` signs IAP assertions so the roles can be walked as people.
- `env.sh` here sources that harness and adds `seed_repos`. It configures **acme/widgets** and
  **acme/api** and adds `user:viewer@example.com` (grain/viewer) and
  `user:contributor@example.com` (grain/contributor).
- `writer@example.com` holds a declared `repo-writer` scope set: includes `grain/viewer`, scopes
  `repos:write`, repos `acme/widgets`.
- Config-repo-managed phase:
  1. `grain state sync`;
  2. push `data/state-repo` to a local bare `/tmp/walk-repos/config.git`;
  3. restart the daemon with `state-repo.json`
     `{"remote":"/tmp/walk-repos/config.git","branch":"main","mode":"external"}`.

  `GET /api/config` then answers `configRepoOnly: true`, `externalConfigRepo: "/tmp/walk-repos/config.git"`.
- Browser: Chromium 1234 from `/var/cache/playwright` through `playwright-core` (ui-ng's),
  Node v20.19.0. Widths 1440×900 and 390×844, `colorScheme` light and dark.
- PostgreSQL was not walked: nothing in §8 is store-specific, and the plan asks for SQLite.
- Helpers in `docs/test-runs/2026-11-16-next-repository-pages/`:
  - `env.sh`;
  - `walk.mjs <roundtrip|roles|add|gate|layout|managed>`, whose phases log `STEP <name>: <outcome>`
    lines;
  - `contact.mjs` (the contact sheets);
  - `captures/*.log` (everything quoted below).

## Item 1: round trips

`walk.mjs roundtrip` (`captures/roundtrip.log`), as alice through the IAP proxy:

- **Work ownership** (Releases). Triage "Off", release "On", then the default switch off.
  - `GET …/work-ownership`: `{"default":"off","items":[["release","on",true],["base-ci","",false],["post-merge","",false],["ports","",false],["triage","off",false]]}`.
  - `grain repo work-ownership acme/widgets` (`captures/cli.log`) prints the same: "repository default: off", "release on here", "triage off here", and the rest "follows the default". The overrides resolve as the server says.
  - The card re-reads as "Off, set here" / "On, set here" / "Nobody has said".
- **Qualification plan.** Add `qualify` (×1) and `security-audit` (×2, depends on `qualify`), then Save.
  - API: `items:[{template-1, repeat 1},{template-2, repeat 2, dependsOn:[template-1]}]`.
  - Classic Releases shows both rows, the second depending on `qualify`.
  - The export holds `tables/qualification_item.json` (both rows, `order_key` 0/1) and `qualification_item_depends_on.json` (`template-2 → template-1`).
  - A PUT making the two depend on each other answers `400 {"error":"qualification plan has a dependency cycle"}` (`captures/plan-cycle.log`). No rc was cut here, so "used by the next rc qualification" rests on that validation.
- **CI capabilities** (CI & Workflows). "Package publish" on.
  - API and `grain repo ci-capabilities`: `https://grain.dev/capabilities/release#package-publish={}`.
  - Export: `repo_config.ci_capabilities` is the same.
  - The card only offers capabilities that are ready. Registry push, image signing and GitHub sandbox lack their secrets on this daemon and are not listed, so the plan's spec walks "Package publish" instead.
- **Issue import "Every (minutes)".** 15 then Save: `intervalSeconds: 900`, and `repo_config.issue_import_interval_seconds: 900` in the export.
- **Advanced tab.** Renders the `.grain/` declarations card ("Grain has never read acme/widgets's declarations"), trigger files, extra push refs and ports.
- **`grain state sync`.** Reported "already up to date, nothing to commit": each save had already been committed as "Update grain state after a write by alice@example.com".

## Item 2: config-repo-managed

`walk.mjs managed` before the fix (`captures/managed.log`):

- The page banner says "Repository configuration is managed in the config repository; operational actions … remain available".
- Every write answers **409** `config_managed_externally` with the patch it would have needed: ownership, CI capabilities, plan, settings, and `POST /api/repos`.
- Already right: CI capabilities says "acme/widgets's CI capabilities are managed in the config repository (tables/repo_config.json); edit them there", with the picker disabled. The plan editor says the same for `tables/qualification_*.json`. The interval box and its Save are disabled.
- **Bug A.** The Releases tab's live controls included the Work ownership switch and 18 On/Off/Say nothing buttons (6 rows × 3). Reproduction:
  1. Run any external config repository, or route `GET /api/config` to `configRepoOnly: true` over `grain demo -github-sim`.
  2. Open `/next/repos/acme/widgets/releases`.
  3. Every button in `card-repo-work-ownership` is enabled, and a click is refused with 409.

  The cause: `<g-repo-qualification-plan>` took `[configRepoOnly]` from the page, and `<g-repo-work-ownership>` had no such input.
- **Bug B.** `/next/repos` showed the Add repo form, whose POST is a 409. `canAdd` asked the role only.

After the fix (`captures/managed-after.log`, same daemon, rebuilt binary):

- Releases' live controls are the tabs and "Run release workflow" (operational) only.
- The ownership card says "acme/widgets's work ownership is managed in the config repository (tables/repo_config.json: work_ownership and work_overrides); edit it there."
- `/next/repos` has no Add form and says "The repos this deployment works in are managed in the config repository (tables/grain_config.json, target_repos); add one there."

## Item 3: roles

`walk.mjs roles` (`captures/roles.log`, `captures/roles-writer.log`). Writes go through the proxy with the browser's CSRF cookie.

| Role | Page | Server |
|---|---|---|
| grain/viewer | Ownership, plan, CI capabilities and repo config disabled. Ownership says "Your role grain/viewer does not hold repos:write, which this needs". No Add repo on the list. | 403 on ownership, CI capabilities (`capabilities:admin`), deployment repo config and `POST /api/repos`, each naming the scope |
| grain/contributor | Same as viewer (holds `tasks:write`, `ci:write` only) | Same 403s |
| repo-writer (acme/widgets) | Ownership, plan, the interval and repo config enabled. CI capabilities disabled with "Your role repo-writer does not hold capabilities:admin". Add repo shown. | 200 on ownership and on `PUT /api/deployment/repos/acme/widgets/config`. 403 on CI capabilities. 403 "holds repos:write, but not on acme/docs" on adding another repository. |

The writer's Add repo form is shown although every name it may add is already there. That is
honest: the 403 names the reason beside the box.

## Item 4: Add repository

`walk.mjs add` (`captures/add.log`) and `curl`:

- **`not a repo`:** refused beside the box, `aria-invalid="true"`: "Couldn't add not a repo: repo: repo must be owner/name, got "not a repo" (one on another git host is host/owner/name)". **Pass.**
- **`acme/widgets` (duplicate):** `POST /api/repos` answers 200 with the unchanged config. The page shows nothing and the list is unchanged. Idempotent rather than wrong, but a person gets no "already added" line. Noted, not changed.
- **`ghe.example.com/acme/legacy`, with no connection for that host:** accepted (200). The list grows to 5. The page shows the `g-no-git-connection-notice` hint only when there is no connection at all. Allowing a repository a task may target is not the same as being able to clone it, so this was left alone.
- **The missing connection (§9's 400):**
  - `PUT /api/deployment/repos/acme/api/git-connection {"credential":"nope2"}` answers **400** "this deployment has no git connection "nope2" on github.com: add it under Settings -> Git connections first".
  - **Bug C.** `PUT /api/deployment/repos/acme/api/config {"gitCredential":"nope"}`, which the repo page's "Save config" sends, answered **200** and stored `gitCredential: "nope"`. Nothing said so until the next clone failed.
  - Fixed in `pkg/ui/teams.go`. The setter now refuses a newly named credential the deployment does not hold, with 400, as the deployment-wide `gitCredential` already did. The one already stored is let through so the rest of the form stays saveable, the rule `ciCapabilities` follows.
  - Regression: `TestDeploymentRepoConfigRefusesUnknownGitCredential` (`pkg/ui/deployment_repo_config_test.go`). It fails on `46fdc1104` and passes with the fix.

## Item 5: the base-gate warning

`walk.mjs gate` (`captures/gate.log`). Each case sets `ciMode` and `watchDefaultBranch` through `PUT …/settings` and reads Overview.

| Case | Warning | `GET /api/base-branch-gates` | True? |
|---|---|---|---|
| `none`, default branch not watched | none | `[]` | yes, nothing is enrolled to report on |
| `none`, watched | "Nothing is testing acme/widgets's default branch … grain CI is not enabled … Go to CI Mode" | `grain-ci-off` | **yes** |
| `github`, watched | same sentence | `grain-ci-off` | **no**, see below |
| `grain`, watched, not yet enrolled | none | `[]` | yes, the seeder enrols it next cycle |
| `grain`, with a run that judged nothing (no jobs answer `push`), 3 h old | "The base-branch run for acme/widgets main finished without judging the tree … grain recorded it as: grain CI ran no job at all", on Overview and Releases | `judged-nothing` | **yes** |

- The last row is simulated: a `base_branch_ci` row with `conclusion = 'errored'` and `detail = 'grain CI ran no job at all'`. That is what `orchestrator.collectBaseBranchCI` writes for a run of no jobs. A real one needs the reconciler and 2 h (`baseBranchRunOverdue`), which this run did not have.
- Before ownership was reset, every case showed no warning (`captures/gate-ownership-off.log`). Item 1 had left the work-ownership default "off", so `base-ci` does not run. `baseCheckSwitchedOff` treats that as unjudged on purpose. That is correct, but worth knowing when reading the gate.
- **GitHub with a required check.** On a `ci_mode: github` repository the sentence says "Nothing is testing … the merge queue is merging onto it anyway" and points at grain CI. But GitHub Actions does judge that branch:
  - grain itself watches its gating workflows' default-branch runs, files a repair when one goes red, and raises `pipeline.base-no-verdict` (`pkg/orchestrator/githubbaserepair.go`);
  - the required check gates each merge.

  So the warning is true only where nothing on GitHub gates the branch.
- This was not changed here. `next-repo-base-gate-warning.spec.js` asserts the warning for `github` on purpose (PR #1592, classic parity), and the reading also feeds deployment health. Which of "suppress it for `ci_mode: github`" and "reword it" is right is a product decision, so it is a follow-up task rather than this change.

## Item 6: layout

`walk.mjs layout` (`captures/layout.log`):

- Covers five tabs and the list, at 1440 and 390 px, light and dark.
- Every page reported `docW == vw`, and no element in `.main-stage` ran past the viewport outside a scroller.
- The sandbox cannot show images to the agent (no Gemini key for `LOOK=1`), so the layout was checked by these geometric assertions. The contact sheets were not looked at by eye. The new spec also checks the managed note against the phone viewport and runs axe.

## Fixes in this change

1. `fix(ui)`: refuse a repository `gitCredential` the deployment does not hold (`pkg/ui/teams.go`, plus a Go regression test).
2. `fix(ui-ng)`: Work ownership and the `/next/repos` Add form respect an external config repository:
   - `repo-work-ownership.component.ts` gains a `configRepoOnly` input, and `repo-detail-page.component.ts` passes it;
   - `repos-page.component.ts` hides Add repo and names the file;
   - `ui-ng/e2e/next-repo-config-managed.spec.js` is the regression spec;
   - design book §"Role-aware" gains the rule.

Checks run locally:

- `go test ./pkg/ui -run 'Config|GitCredential|GitConnection|Repo'`;
- `vitest run src/app/repos` (38) and `eslint src/app/repos`;
- `make fmt-frontend`;
- Playwright: `next-repo-config-managed`, `next-repo-ownership-plan-caps`, `next-repos-list`, `next-repo-advanced*`, `next-repo-base-gate-warning` and `next-settings-repos` (desktop/phone/dark, 41 tests), and `next-full-system.spec.js` (5/5).

Not done: the design book's three independent reviews (Design, Functional, Adversarial) were
not run by separate reviewers from inside this task. The PR's gating code review and a person
reviewing the screenshots stand in for them, and the closing note says so.
