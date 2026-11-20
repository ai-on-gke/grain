# 2026-11-19 Walkthrough: `/next/processes` parity, template priority/interactive, and daemon woken-cycle scheduling

- **Date:** 2026-11-19 – 2026-11-20 (UTC), 23:21–00:20 UTC.
- **Task:** [Task #7320](/tasks/7320) (depends on [Task #7303](/tasks/7303)), branch `grain/53549e42/task-7320-walkthrough-next-processes`, base `main`.
- **Plan section:** `docs/test-plans/2026-11-19.md` §4 (PRs #1870, #1837; fetched from PR #1889 commit `73ccc0a47`).
- **Commit walked:** `1a449c7d5` (`origin/main`), then re-verified against the branch build after fixing `AccessService.deniedReason`.
- **Overlapping reports check:** `git log origin/main -- docs/test-runs/` confirmed no prior `2026-11-19` report covering `/next/processes` parity or `daemon.ReconcileWith` / `InFlight.SubscribeDone`.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Databases: SQLite `3.40.1` and PostgreSQL `15.20` (`postgresql`, `postgresql-client`, and `sqlite3` installed via `apt-get`).
  - Browser: headless Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), viewports `1440×900` (desktop) and `390×844` (phone, touch/mobile), light and dark themes.
  - Real daemon binary built from `HEAD` (`make frontend && go build -o /home/debian/bin/grain ./cmd/grain`), run as `grain daemon -host-sandboxes -sandbox-port-base=0` against a multi-repo local `githubsim` + `git-http-backend` + ES256 IAP key/JWT server (`docs/test-runs/2026-11-19-next-processes-and-reconcile/sim.go`) and scriptable fake Antigravity CLI (`fake-agy.py`).
  - Helpers: `docs/test-runs/2026-11-19-next-processes-and-reconcile/{sim.go,fake-agy.py,env.sh,walk_ui.mjs,run_walkthrough.sh}`.
- **Console and network:** zero `pageerror`s, zero `console.error`s, zero `5xx` responses across all steps on both SQLite and PostgreSQL.

## Real-daemon setup & non-mocked RBAC

Every CRUD action, composition preview, task filing, schedule firing, scoped RBAC refusal, and woken-cycle interactive turn ran against a live `grain daemon -sandbox-port-base=0` over both SQLite and PostgreSQL 15:

1. **Repositories:** three real bare git repositories served via `git-http-backend` and `githubsim`: `acme/widgets` (branches `main` and `release/v1`), `acme/docs` (`main`), and `acme/gadgets` (`main`), configured on `targetRepos`.
2. **Declared custom fields:** seeded into `custom_field` on both SQLite and PostgreSQL (`priority` enum `low`/`high`, `requested-by` text, `urgent` bool, `tags` multi-text).
3. **Real verified RBAC users (zero `/api/config` route mocking):** `sim.go` serves an ES256 public key map at `/_sim/iap-keys` (`-ui-iap-audience walk-iap-aud -ui-iap-issuer https://walk-iap.example -ui-iap-keys-url ...`) and mints signed `X-Goog-IAP-JWT-Assertion` headers for three principals backed by real DB scope sets (`PUT /api/api-scope-sets`, `POST /api/access/principals`):
   - `alice@example.com` → `grain/operator`
   - `bob@example.com` → `walk/bob-suites-a-only` (includes `walk/suites-widgets-only`: `suites:write` and `templates:write` limited to `acme/widgets` only; reads on all repos)
   - `carol@example.com` → `walk/carol-no-tmpl-write` (`suites:write` everywhere, no `templates:write`)

## Steps and outcomes (SQLite & PostgreSQL 15)

| Area | Check | SQLite | PostgreSQL 15 | Details |
| --- | --- | --- | --- | --- |
| Rail badge & narrowing | Initial rail Processes count badge off-page (`/next/inbox`) | PASS | PASS | Badge read `6`, matching `GET /api/schedules` + `/api/templates` + `/api/suites` |
| Rail badge & narrowing | Templates tab shows direct `+ New template` (no `New ▾` menu) | PASS | PASS | `proc-new-menu-btn` absent, `proc-new-template-btn` visible |
| Templates & priority | Create interactive template (`walk-int-fields`) with priority `1e3` and parameter `area` | PASS | PASS | Request sent `priority: 1000`; `GET /api/templates` returned `priority: 1000, interactive: true, fields: [area]`; Configuration pane rendered `Priority 1000` and `Interactive on` |
| Templates & firing | "New task from this template" from inspector AND edit dialog on parameterized interactive template | PASS | PASS | Both opened Launch modal (`launch-template-dialog`) with live-chat note (`This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed.`); submitted task carried `priority: 1000, interactive: true`, title `chat: triage billing` |
| Templates & priority | Edit template, clear Task priority | PASS | PASS | `PATCH /api/templates/template-1` sent `priority: 0`; stored priority cleared; subsequent Launch filed task with default `priority: 5, interactive: true` |
| Templates & firing | "New task from this template" from inspector AND edit dialog on interactive template without fields (`walk-int-nofields`, priority `1e3`) | PASS | PASS | Both opened `new-task-sheet` with `nt-priority` prefilled to `1000`; submitted task carried `priority: 1000, interactive: true` |
| Schedules & writes | Create inline schedule targeting `acme/gadgets@main` with Model `gemini-3.1-pro`, Admission `bypass`, Hosting `console`, Keep running `on`, Network `none`, Reads `acme/docs`, **two Writes targeting different branches of the same repo** (`acme/widgets@main` and `acme/widgets@release/v1`), and all 4 declared Custom fields | PASS | PASS | Both write chips persisted (`acme/widgets @ main`, `acme/widgets @ release/v1`); Configuration pane showed `Model gemini-3.1-pro`, `Admission Bypass queue`, `Hosting Interactive console`, `Keep running on`, `Network None (this deployment only)`, `Reads acme/docs`, `Writes acme/widgets @ main, acme/widgets @ release/v1` |
| Schedules & firing | Draft composition preview (`POST /api/schedules/composition`) vs actual fired task (`POST /api/schedules/{id}/run`) | PASS | PASS | 100% field-by-field parity: `repo: acme/gadgets`, `base: main`, `reads: ["acme/docs"]`, `writes: [{repo:"acme/widgets",base:"main"},{repo:"acme/widgets",base:"release/v1"}]`, `networkAccess: "none"`, `admission: "bypass"`, `hosting: "console"`, `keepRunning: true`, `customFields: {priority:"high", "requested-by":"PlatOps", tags:["nightly","ops"], urgent:"true"}` |
| Schedules edit | Single-field edit (`networkAccess` → `open`) sends change-only `PATCH` | PASS | PASS | `PATCH` body contained `networkAccess: "open"` and omitted unchanged `admission`, `hosting`, `keepRunning`, `reads`, `writes`, `customFields`, `capabilities`, `model` |
| Schedules dirty guard | Unsaved-edits guard prompts once on Escape, Cancel, scrim click, and rail navigation (`canDeactivate`) | PASS | PASS | Each gesture showed 1 `Discard unsaved changes?` dialog; cancelling preserved the dirty dialog; accepting on rail navigation navigated cleanly to `/next/inbox` |
| Suites & stacked dialog | Searchable multi-select Templates combobox (`walk-bound-widgets` 1st, `walk-bound-gadgets` 2nd), Escape closes listbox only, inline `+ New template` stacks over `inert` suite dialog, Escape closes top dialog only & restores focus (`suite-new-template-btn`), saving new template selects it immediately | PASS | PASS | Saved `suite-1` with templates `[walk-bound-widgets, walk-bound-gadgets, walk-inline-created]` in order |
| Scoped RBAC (Bob) | `bob@example.com` (`suites:write` on `acme/widgets` only) edits `suite-1` whose **2nd** template targets `acme/gadgets` | FAIL → **FIXED & PASS** | FAIL → **FIXED & PASS** | On `1a449c7d5`, Save was disabled and tooltip appeared, but `AccessService.deniedReason` said generic `on this repository` instead of naming `acme/gadgets` (see Bug #1). After fix: tooltip reads `Your role walk/bob-suites-a-only does not hold suites:write on acme/gadgets, which this needs. Ask an admin for a role that includes it.`; direct `PATCH /api/suites/suite-1` returns HTTP 403 |
| Scoped RBAC (Carol) | `carol@example.com` (`suites:write`, no `templates:write`) opens Edit suite | PASS | PASS | `+ New template` disabled in place (`dx=0px, dy=0px, saveDy=0px` vs Alice), `Save suite` enabled, tooltip explains missing `templates:write` |
| Rail badge & `/next/agents` scroll | Rail Processes badge updates on creates/deletes (`6 → 13 → 6`) without janking `/next/agents` Details scroll while tasks transition states | PASS | PASS | Anchor running task scrolled to `scrollTop = 300` (`scrollHeight = 1867, clientHeight = 558`); stayed locked at `[300, 300, 300]` across 3 background task create/approve/close transitions |
| Responsive & theme sweep | `/next/processes/schedules`, `/next/processes/templates`, `/next/processes/suites`, stacked template-over-suite dialog, and `/next/agents` at `1440×900` & `390×844`, light & dark (20 checks per store) | PASS | PASS | 20/20 zero horizontal overflow (`overflowPx <= 0`), 0 severe axe violations |
| Daemon woken-cycle 3A | Slow-unwinding interactive run (`PollInterval = 30s`, `MaxWorkers = 1`); human reply posted mid-turn while run holds `InFlight` > `minWakeInterval` (50 ms) | PASS | PASS | SQLite: Turn 2 started **0.087 s** after Turn 1 exit (`0.135 s` after release). PostgreSQL: Turn 2 started **0.406 s** after Turn 1 exit (`0.419 s` after release). Both delivered the unseen `"follow-up during unwind"` reply without waiting for the 30 s poll interval |
| Daemon woken-cycle 3A (repo-backed unwind) | Repo-backed interactive task (`repo: acme/widgets`, no commits pushed) experiencing real ~8 s `ProcessResult` (`branchExistsSettled`) unwind while reply arrives (`PollInterval = 30s`) | PASS | — | Woken cycle at `+5s` logged `dispatch: candidate 11 skipped: busy: this process is still finishing its last run`; the instant `11-1` finished unwinding at `23:52:01`, `InFlight.SubscribeDone` immediately dispatched `11-2` at `23:52:02` (25 s ahead of the 30 s poll tick) |
| Daemon woken-cycle 3B | Rapid burst of 6 interactive dispatches (1 every 1.1 s) while a woken full `RunCycle` is pending (`PollInterval = 30s`) | PASS | PASS | SQLite: `cycles_at_1_5s = 2` (deferred by `dispatchedWakeHeadStart = 5s`), woken full cycle #3 ran at **+6.938 s**. PostgreSQL: `cycles_at_1_5s = 2`, woken full cycle ran at **+7.289 s**. Neither was starved toward the 30 s poll interval |

## Bugs found & findings

### Fixed in this branch

| # | Bug | Deterministic reproduction | Fix | Coverage |
| --- | --- | --- | --- | --- |
| 1 | `AccessService.deniedReason(route, repo)` said vague `"on this repository"` even when passed a repository or multi-repository list (e.g. a suite spanning `acme/widgets` and `acme/gadgets` edited by a user whose `suites:write` is limited to `acme/widgets`), so `suiteSaveDeniedReason` did not name which template's repository caused the refusal | Sign in as `bob@example.com` (`suites:write` limited to `acme/widgets`) and open `/next/processes/suites/<id>?edit=1` on a suite whose 1st template targets `acme/widgets` and 2nd template targets `acme/gadgets`; hover disabled `Save suite` → tooltip read `"does not hold suites:write on this repository"` without naming `acme/gadgets` | Updated `AccessService.deniedReason` (`ui-ng/src/app/services/access.service.ts`) to compute the case-insensitively deduplicated subset of requested repositories not covered by `access.repoLimits[scope]` and render `` ` on ${denied.join(", ")}` `` (e.g. `does not hold suites:write on acme/gadgets`) | `ui-ng/src/app/services/access.service.spec.ts`, `ui-ng/src/app/processes/processes-page.component.spec.ts`, `ui-ng/e2e/next-processes-parity.spec.ts` (desktop, phone, dark) |

### Recorded unchanged (real-daemon observation)

- **Server-vs-UI policy gap on repo-limited `suites:write` for allowed-only suites:** In `pkg/ui/agent_api_gate.go` (`agentCallRepo`), `POST /api/suites` and `PATCH /api/suites/{id}` return `repo = ""` because suites are deployment-level objects without a top-level `repo` field. Consequently, `pkg/ui/access_roles.go:809-812` refuses **every** `POST /api/suites` and `PATCH /api/suites/{id}` call from a role whose `suites:write` carries any `repoLimits` entry (returning HTTP 403 `"holds suites:write only on acme/widgets, and PATCH /api/suites/{id} does not name one of them"`), even when every template in the suite targets `acme/widgets`. Meanwhile, the UI's `canSaveSuite()` enables `Save suite` whenever all bound template repos are inside `repoLimits["suites:write"]` (or all templates are unbound). Left unchanged here because changing server RBAC semantics on `pkg/ui/access_roles.go` is a security-policy decision outside this walkthrough's scope; fail-closed server enforcement already prevents unauthorized writes.

## Verification & tests run

- **Real-daemon walkthrough (`run_walkthrough.sh`):**
  - `STORE=sqlite`: 16/16 UI & RBAC checks passed, 20/20 responsive/theme/axe sweep checks passed, Phase 3 woken-cycle tests 3A & 3B passed (`pageErrors=0, http5xx=0`).
  - `STORE=postgres` (PostgreSQL 15.20): 16/16 UI & RBAC checks passed, 20/20 responsive/theme/axe sweep checks passed, Phase 3 woken-cycle tests 3A & 3B passed (`pageErrors=0, http5xx=0`).
- **Angular unit tests (`ui-ng`):** `npx vitest run` — **76 test files, 828 tests passed**.
- **Angular Playwright e2e (`ui-ng`, over `grain demo`):**
  - `e2e/next-processes-parity.spec.ts`: **11/11 passed** across `desktop`, `phone`, and `dark`.
  - `e2e/next-template-priority-interactive.spec.ts`, `e2e/next-processes-unsaved-and-runs.spec.ts`, `e2e/next-processes-narrowing.spec.ts`, `e2e/next-full-system.spec.js`: **23/23 passed** across `desktop`, `phone`, and `dark`.
- **Go tests:**
  - `GRAIN_REQUIRE_POSTGRES=1 go test -race -count=1 ./pkg/daemon -run TestReconcileWith` — passed.
  - `go test -race -count=1 ./pkg/orchestrator -run TestInFlight` — passed.
  - `go test ./tests/hygiene/...` — passed.
- **Screenshots:** `ui/e2e/screenshot.mjs` before and after across `/next/processes/schedules`, `/next/processes/templates`, `/next/processes/suites`, `/next/processes/suites/suite-1?edit=1`, and `/next/agents` at `1440×900` and `390×844` in light and dark (`contact-sheet.png` before & after attached to the task closing note).
- **No classic (`ui/`) files changed.**
