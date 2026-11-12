# Classic UI e2e migration plan

Task 5078. **This is a plan only.** No test was moved or deleted. The
operator decides on it, and the director files the batches.

The classic Playwright suite (`ui/e2e/`, 348 spec files) runs each
worker against its own `grain demo` (`ui/e2e/fixtures.js`,
`global-setup.js`). It is the flakiest gating job (`ui e2e` failed 12%
of 597 runs between 11-07 and 11-11) and it tests a UI that now only
takes fixes. This plan sorts every test into one of three buckets:

| Bucket | Where it goes | What it holds |
|---|---|---|
| **1** API/CLI | Go tests against a seeded demo server, through the HTTP API and/or the `grain` CLI | Tests whose real assertion is server behaviour: refusals, grants and scopes, lifecycle, access codes, persistence, query results |
| **2** ui-ng Playwright | A new `ui-ng/e2e/` suite at `/next/`, written fresh (classic selectors are not translated) | Interaction on pages ui-ng already has: dialogs and confirms, keyboard, phone widths, main flows, accessibility |
| **3** freeze | Stays in `ui/e2e/`, frozen | Everything else: classic layout and pixel checks, classic-only copy, and views ui-ng does not have yet. No new tests. Flakes are quarantined, not fixed. A page's specs are deleted when its classic page retires |
| **1+2**, **1+3** | Split | Mixed tests. The server half goes to bucket 1, the UI half to 2 (ui-ng has the page) or 3 (it doesn't) |

## Files

- [`classification.csv`](classification.csv): one row per test, 1,258 rows.
  Columns: `file`, `line`, `title` (describe path ` > ` test), `area`,
  `ui_ng` (whether ui-ng has the page), `bucket` (final, with hand
  corrections applied), `script_bucket` (what the script said),
  `reason`, the counts the script decided on (`api_expects`,
  `dom_expects`, `api_calls`), `flags` (keyboard, phone, dialog, a11y,
  layout, mocked-network, cli/real-daemon), the `api_paths` the test
  touches, and `go_candidates` (Go test files that name the same API
  path).
- [`overrides.csv`](overrides.csv): the 80 hand-checked tests, with the
  bucket I gave each one and why. The script applies these last.
- [`classify.py`](classify.py): the script. To regenerate:
  `(cd ui && npx playwright test --list --reporter=json > /tmp/list.json)`,
  then `LIST=/tmp/list.json python3 docs/test-plans/classic-ui-e2e-migration/classify.py`.

### How many tests

`npx playwright test --list` gives **1,258 tests in 348 files**. The
"~1,365" in the task is a grep-style count: there are 1,264 `test(`-like
calls (including `test.skip(cond, …)`, `test.describe`, `test.use`),
and loops expand some calls into several tests. The CSV uses
Playwright's own list, so a row is exactly one test Playwright runs.

## How the script decides, and how often it is wrong

For each test, the script reads the callback body, plus one level of
helpers defined in the same file. It counts:

- **API expects**: `expect(...)` on a response's `.status()`,
  `.json()` or `.ok()`, or on a value computed from a response. Checks
  that only confirm a setup call worked (`expect(res.ok()).toBe(true)`,
  `status()` 2xx) don't count. Neither does a check on the payload the
  form *sent* (`postDataJSON`).
- **DOM expects**: Playwright's web-first matchers (`toBeVisible`,
  `toHaveText`, `toBeFocused`, …) and checks on locators.
- **Interaction and kind**: clicks, fills, the keyboard, phone
  viewports (≤ 699 px), dialogs and confirms, axe/ARIA, bounding-box
  layout, `page.route` stubs, and real-daemon or CLI use.
- **Area**: a filename for a cross-cutting area (access code, releases,
  merge queue, `next-*`) wins. Otherwise it's the first page the test
  opens, then the filename, then the first page the file opens.

The rules, in order:

1. `next-*` specs → 2: they already drive `/next/`.
2. API expects only → 1.
3. API and DOM expects → 1+2 or 1+3.
4. A layout title ("fits", "laid out", "centres", …) with no keyboard
   or dialog → 3.
5. A keyboard or dialog interaction → 2 when ui-ng has the page,
   otherwise 3.
6. A title naming server behaviour (refuses, persists, survives a
   reload, revokes, validates, …) with DOM expects → 1+2 or 1+3.
7. Any other interaction on a ui-ng page → 2. On a page ui-ng lacks → 3.
8. Static renders → 3.

**Error rate.** Hand-checked against the code of each test:

| Pass | Sample | Disagreements | Error rate |
|---|---|---|---|
| First pass | 60 tests, stratified by bucket (seed 5078) | 23 | **38%** |
| Second pass, after fixing the patterns found, on the same 60 | 60 | 14 | 23% (optimistic: tuned on this sample) |
| Second pass on a fresh holdout | 20 tests, uniform (seed 99) | 6 | **30%** |

All 80 hand-checked tests use the hand bucket in the CSV
(`[hand-checked]` in `reason`). The remaining errors fall into three
patterns. Each batch task should re-triage its own specs with these in
mind before porting anything:

- **Setup reads counted as server assertions.** A test reads a task
  back to find its id, then asserts on the page. These are called 1+2
  but are really 2. The script over-calls "mixed".
- **Layout at phone widths.** Classic pixel checks at 390 px are 3, but
  the script calls them 2 when the title doesn't say "fits" or
  "layout".
- **The page decides the area.** Several `deployment-*` and `hostd*`
  specs drive `/settings/*` sections that ui-ng *has*, not the System
  page it doesn't.

So read the per-area counts below as ±30%. The plan's shape doesn't
change with that: the order and the harness are the same either way.

## Per area

"ui-ng has it" comes from `ui-ng/src/app/shell/nav.ts` and
`app.routes.ts`:

- **yes**: Inbox, Workstreams, Tasks (with saved boards and Timeline),
  Agents, Task detail (`/next/task/:id`), Sessions and the Director,
  Processes (schedules, templates, suites), CI, Telemetry, Context,
  Fleet, Settings (30 sections, from `settings-sections.ts`), and the
  New task sheet.
- **partial**: Repos. ui-ng has the repo detail page, with the
  Overview, Agents, Branches, CI, Merge queue and Releases tabs. It has
  no repo list, repo settings, compare or issue import, and no release
  memory editor. Fleet has Host, Sandboxes, Logs and Caches, but not the
  classic System Health, Pipeline, Upgrade or Top. Merge gates appear
  only as the task detail gates bar.
- **n/a**: access code and sign-in. This is server behaviour shared by
  both UIs.

| Area | ui-ng has it | specs | tests | 1 | 1+2 | 1+3 | 2 | 3 | server half (1, 1+2, 1+3) | of which a Go test already names the route | ui-ng half (2, 1+2) |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Settings | yes | 66 | 165 | 1 | 41 | 0 | 76 | 47 | 42 | 8 | 117 |
| Shell, navigation & resilience | yes | 19 | 150 | 0 | 39 | 0 | 85 | 26 | 39 | 1 | 124 |
| Task detail | yes | 59 | 127 | 1 | 55 | 0 | 48 | 23 | 56 | 26 | 103 |
| Tasks list, boards & timeline | yes | 23 | 114 | 0 | 43 | 0 | 53 | 18 | 43 | 2 | 96 |
| Processes (schedules, templates, suites, workflows) | yes | 38 | 103 | 2 | 33 | 0 | 53 | 15 | 35 | 6 | 86 |
| Fleet / System / hosts | partial | 46 | 89 | 0 | 0 | 7 | 0 | 82 | 7 | 0 | 0 |
| Sessions & Director | yes | 24 | 80 | 0 | 28 | 0 | 47 | 5 | 28 | 11 | 75 |
| Repos | partial | 43 | 79 | 1 | 0 | 23 | 0 | 55 | 24 | 9 | 0 |
| Capabilities, grants & scopes | yes | 37 | 78 | 4 | 18 | 0 | 21 | 35 | 22 | 6 | 39 |
| Inbox | yes | 26 | 54 | 1 | 12 | 0 | 27 | 14 | 13 | 9 | 39 |
| Releases & release memory | partial | 24 | 48 | 1 | 1 | 17 | 0 | 29 | 19 | 8 | 1 |
| Merge queue & gates | partial | 16 | 46 | 0 | 1 | 26 | 0 | 19 | 27 | 4 | 1 |
| Access code & sign-in | n/a | 9 | 41 | 3 | 0 | 12 | 0 | 26 | 15 | 3 | 0 |
| New task form | yes | 14 | 38 | 0 | 10 | 0 | 24 | 4 | 10 | 5 | 34 |
| CI | yes | 9 | 21 | 0 | 5 | 0 | 8 | 8 | 5 | 5 | 13 |
| ui-ng (already /next/) | yes | 5 | 15 | 0 | 0 | 0 | 15 | 0 | 0 | 0 | 15 |
| Telemetry & metrics | yes | 4 | 6 | 0 | 1 | 0 | 1 | 4 | 1 | 0 | 2 |
| Context & memory | yes | 1 | 4 | 0 | 0 | 0 | 4 | 0 | 0 | 0 | 4 |
| **Total** | | **348** | **1258** | 14 | 287 | 85 | 462 | 410 | 386 | 103 | 749 |

The last three columns give the work per batch. The **server half** is
what bucket 1 has to cover. **A Go test already names the route**
counts tests where some existing Go test file contains the same
`/api/...` path literal (`go_candidates` in the CSV). Those tests are
likely, but not certainly, covered already. Each batch checks the
candidates before writing anything new. The count is a *lower bound*:
most `pkg/ui` tests build paths as `"/api/tasks/" + id + "/close"`, and
a literal match misses those. The **ui-ng half** is how many classic
tests have an interaction worth rewriting. That is not how many ui-ng
tests to write: one fresh ui-ng spec usually covers several classic
tests.

### Existing Go tests that cover bucket-1 behaviour per area

These are found by file name and by path literal. They are where each
batch looks first. A classic test whose server half one of these
already asserts needs no port. Mark it `covered-by:<test>` in the CSV
instead.

| Area | Go tests already there (`pkg/ui`, `tests/security`, `tests/e2e`, `pkg/cli`, `pkg/demo`) |
|---|---|
| Access code & sign-in | `pkg/ui/access_code_test.go`, `access_code_mode_test.go`, `access_code_return_test.go`, `access_code_client_test.go`, `httpclient_auth_test.go`; `tests/e2e/access_code_test.go`; `tests/iap/` |
| Capabilities, grants & scopes | 36 files: `pkg/ui/capability_grants_test.go`, `capability_policy_test.go`, `api_scope_sets_test.go`, `access_roles_test.go`, `capability_catalogue_test.go`, `capability_managed_test.go`, …; `tests/security/capabilities_test.go`, `horizontal_access_test.go`, `task_grant_escalation_test.go`, `access_admin_test.go`, `agent_api_task_write_scope_test.go`, `TestRouteAuthorizationMatrix`; `tests/e2e/capability_test.go`, `live_api_grant_test.go` |
| Sessions & Director | `pkg/ui/console_sessions_test.go`, `console_session_stale_edit_test.go`, `session_title_test.go`, `session_default_capabilities_test.go`, `director_floor_test.go`, `director_proposal_rails_test.go`, `agent_messages_test.go`, `external_agents_test.go`; `tests/e2e/interactive_message_delivery_walkthrough_test.go`, `pause_chat_walkthrough_test.go` |
| Inbox | `pkg/ui/inbox_test.go`, `events_inbox_test.go`, `task_access_request_test.go`, `agent_approval_rail_test.go`; `tests/e2e/propose_task_approval_test.go`, `ack_reopen_walkthrough_test.go` |
| Task detail and Tasks list | `pkg/ui/agent_api_test.go`, `task_list_cache_test.go`, `task_list_live_test.go`, `events_test.go`, `boards_test.go`, `retry_merge_test.go`, `force_merge_test.go`; `tests/e2e/close_reopen_streak_walkthrough_test.go`, `custom_field_loop_test.go`, `priority_dispatch_order_test.go`, `close_while_live_test.go` |
| Processes | 29 files: `pkg/ui/schedule_*_test.go` (filter, git-ref, overlap group, suppression, own settings, network access), `cross_team_template_test.go`, `interactive_template_test.go`, `repo_workflows_test.go`; `pkg/demo/demo_schedules_test.go`, `demo_scheduled_workflows_test.go`; `tests/e2e/schedule_auto_approve_proposals_test.go` |
| Settings | `pkg/ui/settings_test.go`, `settings_writer_test.go`, `settings_plan_test.go`, `settings_restart_drift_test.go`, `sandbox_budget_settings_test.go`, `disk_floor_settings_test.go`, `github_host_settings_test.go`, `ci_storage_settings_test.go`, `disable_self_upgrade_test.go`; `pkg/hostinit/k8s_test.go` |
| Repos, Releases, Merge queue | `pkg/ui/repo_*_test.go` (24 files), `releases_test.go`, `release_approval_test.go`, `release_branch_authorization_test.go`, `basebranchrelease_test.go`, `merge_on_green_test.go`, `mergebatches_test.go`, `auto_merge_sticky_test.go`; `tests/e2e/merge_rules_walkthrough_test.go`, `mergequeue_*_test.go`, `path_triggers_walkthrough_test.go` |
| Fleet / hosts | `pkg/ui/hostds_test.go`, `hostd_delete_test.go`, `hostd_install_instructions_test.go`, `hostd_owner_test.go`, `persistent_sandboxes_test.go`; `tests/e2e/git_hosts_*_test.go` |
| CI | 26 files: `pkg/ui/ci_*_test.go`, `cifault_test.go`; `pkg/cli/cicmd_test.go`; `pkg/capability/ciapi/ciapi_test.go`; `tests/e2e/ci_tools_walkthrough_test.go` |

## Flakiness

**What I could not read.** `grain ci test-history`, `grain ci list` and
`ci://` all returned `forbidden`: this run does not hold `grain#ci` or
`api:ci-read`. The repo has no quarantine list either: no spec is
tagged, and the only `test.skip` calls are 5 environment conditions.
So the ranking below is a **proxy**, built from:

1. **Deflake commits**: commits since April 2026 that touch ≤ 4 specs
   and whose message says flake, deflake, intermittent, race or timing
   (17 specs).
2. **Known flakes in this deployment's notes**:
   `new-session-first-message-sending.spec.js:88` at phone width
   (deflaked in #4464), and specs that mutate the worker-scoped
   `demoServer` without restoring it.
3. **Static risk signals**: `waitForTimeout`, `test.setTimeout`/`slow`,
   waits of 10 s or more, and daemon restarts or a real daemon or
   GitHub sim.

`risk = 10 × deflake commits + 2 × waitForTimeout + setTimeout/slow +
0.5 × long waits + restart/real-daemon mentions + sim mentions`.

**The first batch should be granted `api:ci-read@ai-on-gke/grain`. It
should re-rank with `grain ci test-history <spec>` before it starts.**
The proxy is enough to order batches, not to name the one flake that
blocks `main` today.

### Riskiest specs

| Spec | tests | deflake commits | waitForTimeout | setTimeout/slow | long waits (≥10s) | daemon restart/real daemon | sim | risk |
|---|---|---|---|---|---|---|---|---|
| `ui/e2e/director.spec.js` | 4 | 1 | 0 | 2 | 4 | 19 | 0 | 33 |
| `ui/e2e/k8s-sandbox-settings-walkthrough.spec.js` | 1 | 0 | 0 | 1 | 0 | 31 | 0 | 32 |
| `ui/e2e/inbox-collapse-walkthrough.spec.js` | 3 | 2 | 0 | 2 | 1 | 0 | 0 | 22 |
| `ui/e2e/disconnection.spec.js` | 10 | 0 | 3 | 3 | 10 | 6 | 0 | 20 |
| `ui/e2e/grant-matrix-ui.spec.js` | 10 | 1 | 0 | 10 | 0 | 0 | 0 | 20 |
| `ui/e2e/deployment_operations_walkthrough.spec.js` | 10 | 0 | 0 | 0 | 4 | 13 | 0 | 15 |
| `ui/e2e/saved-boards-walkthrough.spec.js` | 32 | 1 | 1 | 1 | 4 | 0 | 0 | 15 |
| `ui/e2e/interactive-session-chat.spec.js` | 1 | 1 | 0 | 1 | 7 | 0 | 0 | 14 |
| `ui/e2e/config-repo-only-settings.spec.js` | 13 | 1 | 0 | 1 | 0 | 3 | 0 | 14 |
| `ui/e2e/disconnected-banner-held-reads.spec.js` | 1 | 1 | 1 | 1 | 2 | 0 | 0 | 14 |
| `ui/e2e/console-resting-notes-walkthrough.spec.js` | 1 | 1 | 0 | 1 | 4 | 0 | 0 | 13 |
| `ui/e2e/sessions-worktree-pinned-nav-walkthrough.spec.js` | 11 | 0 | 1 | 1 | 3 | 8 | 0 | 12 |
| `ui/e2e/audit-settings-layout.spec.js` | 2 | 1 | 0 | 0 | 0 | 2 | 0 | 12 |
| `ui/e2e/restart-banner.spec.js` | 1 | 0 | 0 | 1 | 2 | 10 | 0 | 12 |
| `ui/e2e/task-chat-optimistic-sends.spec.js` | 10 | 0 | 2 | 7 | 2 | 0 | 0 | 12 |
| `ui/e2e/console-session-wake-inbox-walkthrough.spec.js` | 1 | 1 | 0 | 1 | 1 | 0 | 0 | 12 |
| `ui/e2e/inbox-notifications-walkthrough.spec.js` | 3 | 0 | 4 | 1 | 5 | 0 | 0 | 12 |
| `ui/e2e/responsive-navigation-walkthrough.spec.js` | 78 | 0 | 4 | 1 | 5 | 0 | 0 | 12 |
| `ui/e2e/capability-policy-pickers.spec.js` | 3 | 1 | 0 | 1 | 0 | 0 | 0 | 11 |
| `ui/e2e/iap-header-logout.spec.js` | 2 | 1 | 0 | 0 | 0 | 0 | 0 | 10 |

### Flake risk by area

Each spec's risk is spread over its tests' areas.

| Area | flake-risk sum |
|---|---|
| Sessions & Director | 106 |
| Settings | 103 |
| Fleet / System / hosts | 81 |
| Task detail | 60 |
| Shell, navigation & resilience | 58 |
| Inbox | 55 |
| Repos | 48 |
| Releases & release memory | 40 |
| Capabilities, grants & scopes | 32 |
| Tasks list, boards & timeline | 30 |
| Processes (schedules, templates, suites, workflows) | 30 |
| Access code & sign-in | 24 |
| New task form | 19 |
| Merge queue & gates | 15 |
| CI | 10 |
| ui-ng (already /next/) | 10 |
| Telemetry & metrics | 6 |
| Context & memory | 1 |

Sessions & Director and Settings carry the most risk: chat
send/stream timing, disconnect banners, and daemon restarts on settings
saves. Fleet/System is third, but 82 of its 89 tests are bucket 3. For
those the answer is to quarantine, not port.

## Harness

### Bucket 1: Go against a seeded demo (missing today)

No Go test boots `grain demo` the way `fixtures.js` does:

- `tests/e2e/harness_test.go` builds its own `world` (store,
  orchestrator, fake agents) with no demo seed.
- The `pkg/ui` tests build `ui.NewServer`/`NewServerWithClient` over
  hand-made stores.
- `pkg/demo`'s `seedDemo` is unexported, and only `pkg/demo`'s own tests
  call it.

The classic specs lean on the seeded rows: task 2, task 9, the parked
question, `acme/widgets`, the boards. They also use the `/api/e2e/*`
test hooks (18 calls) and the GitHub sim.

What bucket 1 needs, as one harness task:

- **`pkg/demo/demotest`** (or `tests/demoapi`): `demotest.Start(t,
  demotest.Options{Args: …})`. It returns a server with `URL`,
  `AccessCode`, an authenticated `*ui.Client` / `http.Client`, and
  `Restart(args…)`.
  - It runs the same `demo.Run` path as `grain demo` (seed, fake CI,
    GitHub sim, `-access-code-file`) in-process on `127.0.0.1:0` over a
    `t.TempDir()` store. That gives one per test or per package, which
    beats the classic per-worker sharing and its `--repeat-each`
    problem.
  - It supports `GRAIN_E2E_DB_DRIVER=postgres` the way `fixtures.js`
    does.
- **A CLI runner**: `demotest.CLI(t, srv, "task", "close", "2")`. It
  runs `grain`, built once in `TestMain` (as `global-setup.js` does), at
  that server. This covers what operators do from the CLI.
- **Fixture accessors**: named seeded rows, so ported tests stop
  hard-coding ids (`srv.Seed.ParkedQuestionTask`). Today's ids are
  documented in `pkg/demo/demo_test.go`'s `TestSeedDemoCoversEveryState`.
- **Where tests live**: `tests/uiapi/<area>_test.go`, one file per
  area. They run in the existing `go e2e` job, which is already gating
  and needs no new CI wiring.
- **Naming**: `TestClassic_<spec>_<what>`, or a `// ported from
  ui/e2e/x.spec.js:NN` comment, so the CSV can be joined back.

### Bucket 2: ui-ng Playwright (missing today)

ui-ng has 74 vitest unit specs and no e2e. The 5 `ui/e2e/next-*.spec.js`
files (15 tests) already drive `/next/`, but they run inside the classic
job. What bucket 2 needs, as one task:

- **`ui-ng/e2e/` and `ui-ng/playwright.config.ts`**:
  - `baseURL` ends in `/next/`.
  - Projects `desktop` (1440×900), `phone` (390×844, touch) and
    `dark`.
  - `retries: 0` in CI, and `fullyParallel: false` within a file, as in
    the classic suite.
- **Fixtures**: reuse `ui/e2e/fixtures.js`'s one-demo-per-worker server
  and `accesscode.js` sign-in. Move them to `ui-shared/e2e/` or import
  them across, rather than forking a copy. Add an `axe` fixture
  (`@axe-core/playwright`, which the classic suite already uses) and a
  `pageErrors` fixture.
- **Selectors**: role and label only, plus the `data-testid`s the
  design book allows (`design/design/ui-design-book.design.md`). Never
  classic class names.
- **Make and CI**:
  - A `make test-e2e-ng` target.
  - A `ui-ng e2e` job in `.github/workflows/tests.yml`, path-filtered
    on `ui-ng/**`, `ui-shared/**`, `pkg/ui/**` and `pkg/demo/**`.
  - It is non-required for its first week. It becomes a required check
    after 7 days without a flake.
  - Editing `tests.yml` is a classified path, so it files a security
    review.
- **Moves**: the 5 `next-*` specs move into it unchanged. The design
  book's "scenario spec" rule then points at `ui-ng/e2e/` instead of
  `ui/e2e/`.

### Bucket 3: freeze mechanics (missing today)

There is no quarantine list. What the freeze needs:

- **`ui/e2e/QUARANTINE`**: one `file:line` per line, read by
  `playwright.config.js` as a `grepInvert`. The same file feeds a
  non-gating `ui e2e (quarantined)` job (`continue-on-error`) so
  regressions stay visible.
- **`ui/e2e/NONGATING`**: specs whose bucket-1 and bucket-2 halves have
  landed. They run in the same non-gating lane.
- **A hygiene test**: `tests/hygiene`. It fails when a new
  `ui/e2e/*.spec.js` appears that is not in a frozen allowlist. This is
  the "no new classic tests" rule, enforced.

## Recommendations

### Order, estimates, and when each batch stops gating

The order is the flakiest and most-used areas first, after the
harnesses. A "run" is one ~2 h agent task. The estimates assume about
30 server-half tests per run, including checking existing Go coverage,
and about 25 classic tests' worth of interaction per ui-ng run.

| # | Batch | Tests (server half / ui-ng half / 3) | Estimate | Depends on |
|---|---|---|---|---|
| H1 | Go demo harness (`demotest`) + first port of 10 tests as proof | – | 1–2 runs | this plan |
| H2 | ui-ng Playwright suite + `ui-ng e2e` job + move `next-*` | 15 / 15 / 0 | 1–2 runs | this plan |
| H3 | Freeze mechanics: `QUARANTINE`, `NONGATING`, non-gating lane, hygiene allowlist | – | 1 run | this plan |
| B1 | Sessions & Director | 28 / 75 / 5 | 1 + 2 runs | H1, H2, H3 |
| B2 | Settings sections (General, Models, Sandboxes, Agents, Upgrade, Memory, Hosts, …) | 42 / 117 / 47 | 2 + 3 runs | H1, H2, H3 |
| B3 | Inbox | 13 / 39 / 14 | 1 + 1 runs | H1, H2, H3 |
| B4 | Task detail | 56 / 103 / 23 | 2 + 2 runs | H1, H2, H3 |
| B5 | Shell, navigation & resilience (phone, keyboard, a11y, disconnect) | 39 / 124 / 26 | 1 + 3 runs | H2, H3 |
| B6 | Tasks list, boards & timeline + New task form | 53 / 130 / 22 | 2 + 3 runs | H1, H2, H3 |
| B7 | Capabilities, grants & scopes + Access code & sign-in | 37 / 39 / 61 | 2 + 1 runs | H1, H2, H3 |
| B8 | Processes | 35 / 86 / 15 | 1 + 2 runs | H1, H2, H3 |
| B9 | Repos, Releases & Merge queue (bucket 1 only; their UI halves are 3 until ui-ng grows those views) | 70 / 2 / 103 | 3 runs | H1, H3 |
| B10 | Fleet/System, CI, Telemetry, Context (bucket 1 + the CI/Telemetry ui-ng half; the rest quarantined) | 13 / 19 / 94 | 1 + 1 runs | H1, H2, H3 |

Total: about 4–5 harness runs plus about 37 batch runs. Batches B1–B10
can run in parallel once H1–H3 land, two or three at a time to keep the
merge queue calm.

**When a classic spec stops gating.** Each batch updates the CSV,
adding `ported_to` (the Go test or ui-ng spec) for every row it
handles. A spec moves to `ui/e2e/NONGATING` when:

- every bucket-1 and 1+x test in it has its server half merged in Go
  (or is marked `covered-by:` an existing Go test), **and**
- every 2 and 1+2 test has a ui-ng spec in the required `ui-ng e2e`
  job.

That covers the 138 specs with no bucket-3 test. The 131 specs that mix
bucket 3 with something else stop gating as soon as their server half
lands, which is the operator's rule. Their remaining bucket-3 tests
still run in the non-gating lane until the page retires. The 79 specs
that are all bucket 3 keep gating, so classic fixes stay protected,
until their first flake. Then they go to `QUARANTINE` without a fix.

### Follow-up tasks to file

There is one per row of the table above. Each one should:

- read this directory, and re-triage its own rows first. The script is
  ±30%, so correct the `bucket` column in the same PR.
- write Go tests under `tests/uiapi/` and ui-ng specs under
  `ui-ng/e2e/`, following the design book.
- update `classification.csv` (`ported_to`, `covered-by:`) and
  `ui/e2e/NONGATING`.
- not delete classic specs. Deletion waits for the classic page to
  retire.
- carry `api:ci-read@ai-on-gke/grain`, so it can rank by `grain ci
  test-history` before choosing what to port first.
