# Team scope in the shell walkthrough — 2026-10-15

Task: grain/eu/task-3944 (bwsalmon/grain, base `candidate`), filed by
nightly test-plan task 3749. Plan: `docs/test-plans/2026-10-15.md` §1,
"Team Scope in the Shell: Creation Forms, Pins, Top Bar, Paths and Deep
Links".

## Environment

- Commit tested: `977cbdf85` (tip of `candidate` when the branch was cut),
  then the two fixes below on top of it. All eleven commits under test
  were confirmed on `origin/candidate` with `git branch -r --contains`:
  `3b15d9f00`, `badd810ec`, `5311ed15f`, `bc629a157`, `e1cb14849`,
  `626f41b96`, `67d81b58d`, `1ac5f4314`, `3995c35f3`, `bfb89b7c5`,
  `eaf91470b`.
- Go 1.26.6, Node 20.19.0, Playwright 1.62.1 (Chromium headless shell 151,
  installed with
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`,
  because the package-cache proxy re-signs the browser CDN's TLS),
  axe-core 4 (scratch install, not committed), Debian 12.15.
- `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e ./cmd/grain`, so the binary embeds the built
  frontend.
- The demo ran as `grain demo -addr 127.0.0.1:0 -open=false -observability
  -reconcile 2s` on its own throwaway embedded SQLite store. The first
  start had no `-observability`. That demo runs with `-ci=false`, so the
  CI page says "Not available" and there is no form to test, so the demo
  was restarted with `-observability`, which wires a CI runner whose runs
  stay live. It was restarted once more after the fixes were rebuilt.
  Readiness was polled on `GET /api/sandboxes`.
- Users and teams were set up through the API. Users came from
  `POST /api/auth/register`. Teams came from `POST /api/teams` plus
  `PUT /api/teams/{id}` (`targetRepos: ["acme/widgets"]`), as the loopback
  system admin with `X-Grain-Admin-Mode: on`:
  - `boss`: admin of `alpha` and `beta`. This is the "admin of both"; they
    are a team admin, not a system admin, so nothing leans on Admin mode.
  - `mixed`: member of `alpha`, viewer of `beta`.
  - `viewer`: viewer of `alpha` only.
  - `betaonly`: member of `beta` only. Added for finding 1.

  Every user also has the personal team grain makes at registration.
- Browser steps used ad hoc Playwright scripts signed in through
  `POST /api/auth/login` with no `X-Grain-Admin-Mode` header. The scripts
  read the API only to confirm what a gesture had persisted. The
  committed regression spec repeats the core of it
  (`ui/e2e/team-scope-shell-walkthrough.spec.js`, which sets
  `test.use({ extraHTTPHeaders: {} })`).

## Stubs

- `localStorage["grain.username"] = <user>` (an init script), as every
  e2e spec does, so the first-visit account dialog does not cover the app.
  The session itself is a real login cookie.
- axe-core was run with the browser context's `bypassCSP: true`, because
  grain's CSP (`script-src 'self'`) blocks the injected script. This only
  affects the test browser.
- Nothing in grain's API or React was mocked. The CI runner is the demo's
  own `-observability` runner: its jobs wait on a browser that never
  comes, so runs stay in flight. No GitHub was involved; the demo's
  simulated repo `acme/widgets` was used.

## Steps and outcomes

1. **Setup.** Teams `alpha` and `beta` and the users above were created as
   listed. **Pass.**
2. **Creation forms under `/t/alpha` and `/t/beta`, as `boss`.** Each form
   was driven through the UI and every row read back from the API:

   | Form | Picker shown | POST body `team` | Read back |
   |---|---|---|---|
   | New task (`/t/<team>/new`) | Target team, set to the route's team | route's team | `GET /api/tasks`: route's team |
   | New session (`/t/<team>/sessions`) | none | route's team | `GET /api/console-sessions`: route's team |
   | New template | none (`isNew`) | route's team | `GET /api/templates`: route's team |
   | New schedule | none (`isNew`) | route's team | `GET /api/schedules`: route's team |
   | New suite | none (`isNew`) | route's team | `GET /api/suites`: route's team |
   | CI "Start a run" (`/t/<team>/ci`) | none | route's team | **beta's run came back as alpha's (finding 1)** |

   **Pass, except the CI run, now fixed.**

   Side observation: under a non-default team, New task's Repos select
   starts on "—" even though the team targets exactly one repo, so Create
   task stays disabled until one is picked. This is not part of §1 and
   was not changed.
3. **Move between teams from the edit pane.** The template, schedule and
   suite made under `/t/alpha` each show Target team when edited. Picking
   Beta Team and saving PATCHes `team: "beta"`, and the API reads each
   back in `beta`. **Pass.**
4. **Viewer hold.**
   - As `mixed` under `/t/beta/new`: the refusal ("you cannot file tasks
     into team "beta": that needs the proposer, member or admin role on
     it") shows, and Create task is disabled with `aria-describedby` set
     to that sentence. The Target team picker, however, offered only
     "Beta Team (beta)" and "mixed (mixed)". Alpha, where `mixed` is a
     member, was missing (finding 2). **Fail, now fixed.** With the fix the
     picker offers Alpha, Beta and mixed. Picking Alpha lifts the hold,
     and the task files into `alpha` (read back from the API).
   - As `viewer` under `/t/alpha/new`: the hold shows, Create task is
     disabled, and the picker offers "Alpha Team (alpha)" and
     "viewer (viewer)". The second is the viewer's personal team, which
     they administer; this was the same before and after the fix. Enter
     in the title box posts nothing. **Pass.** Probing the other creation
     endpoints as this viewer gives 403 for tasks, sessions, templates,
     schedules, suites and boards, but `POST /api/ci/runs` is accepted
     (finding 3, filed separately). The UI also shows this viewer
     enabled "+ New session/schedule/template/suite" and "Start a run"
     buttons on alpha's pages; the server refuses all of them except CI.
5. **Pins.** `boss`'s legacy key was seeded as
   `grain.pinnedNav.boss = [view tasks, session walk-session-alpha (alpha),
   board board-5 (beta)]`, then the page was reloaded. After the reload
   `grain.pinnedNav.boss.alpha = [view tasks, session]`. The view goes
   there because alpha is `boss`'s first team, and so their fallback.
   `grain.pinnedNav.boss.beta = [view inbox (defaults), board-5]` and the
   legacy key is gone. The Customize overlay names the team ("for team
   alpha") and lists only alpha's items. A reorder (Move up, Save) and
   then Reset to default plus Save in alpha changed only `.alpha`, and
   `.beta` was byte-identical afterwards. **Pass.**
6. **Top bar.** On desktop (1280×800) the focus order is Team switcher →
   "Got it" (the first-visit hint) → Team home → Team settings → "+ New
   task". Tab from the path reaches Team home, then Team settings.
   **Pass.** At 390×844 the top bar sits in the phone drawer (open it with
   "Open navigation"). The drawer is 232 px wide. The switcher sits at
   y≈99, and the icon group wraps onto its own line (y≈221), ending at
   x=220, the drawer's padding edge. **Pass.** axe (wcag2a/aa) found
   nothing on the top bar at either size. It did flag two serious issues
   elsewhere on `/t/alpha/tasks`, both outside §1:
   `color-contrast` on `.task-number`, and `nested-interactive` on a task
   row. These were recorded, not fixed.
7. **Mixed case.** Each address landed on the lower-case path with the
   switcher on the right team. `/t/ALPHA/tasks` → `/t/alpha/tasks`,
   `/t/Alpha` → `/t/alpha`, `/teams/ALPHA/tasks` → `/t/alpha/tasks`,
   `/team/Beta/schedules` → `/t/beta/schedules`,
   `/t/ALPHA/settings/models` → `/t/alpha/settings/models` (Team Settings
   open), `/T/alpha/tasks` → `/t/alpha/tasks`. **Pass.**
8. **Session deep link.** `/t/alpha/session/<beta session>` replaced the
   URL with `/t/beta/session/<id>` once, and the switcher read Beta Team.
   Picking Alpha Team with the chat open went to `/t/alpha` (the
   switcher's normal destination). After 12 s of polling it was still on
   alpha: navigations were exactly `/t/alpha/session/…`,
   `/t/beta/session/…`, `/t/alpha`. **Pass.**
9. **Cross-team suite.** Under `/t/alpha`, New suite's Templates menu
   listed alpha's template first, and every row was labelled "In team X"
   ("In team Alpha Team", "In team Beta Team"). A suite naming beta's
   `template-5` saved with `team: "alpha"`. Running it (Run… → acme/widgets
   → main → Run) filed task 15, "Walk template beta", queued in team
   `alpha`, the suite's team, with `-reconcile 2s`. **Pass.**
10. **Director of a new non-admin team.** `boss` (not a system admin)
    created `gamma` with `POST /api/teams`. In `GET /api/console-sessions`,
    `director-4` has team `gamma` and capabilities `["self-debug"]`. It
    has none of the capabilities authorized only for `default` (`gcp-key`,
    `grain-log`, `corp-research`, `deployment-health`, `self-repair`,
    `self-upgrade`, `bootstrap-playbooks`, `db-read`, `db-write`). The
    seeded alpha and beta Directors read the same. **Pass.**

## Findings

### 1. A CI run started for one team came back as another team's live run (fixed)

Reproduction (demo with `-observability`, signed in as `boss`):

```
POST /api/ci/runs {"repo":"acme/widgets","branch":"main",
                   "workflow":".grain/workflows/observability.yml","team":"alpha"}
  -> 202 {"id":"1db22a28f7f13ab5","team":"alpha",...}
POST /api/ci/runs {... same ..., "team":"beta"}
  -> 202 {"id":"1db22a28f7f13ab5","team":"alpha",...}     # alpha's run
GET  /api/ci/runs?team=beta -> {"runs":[],"matched":0,"stored":1}
```

The same happens as `betaonly`, who is not on alpha at all. They are
handed alpha's run id; they can read it here only because beta also
targets `acme/widgets`. Through the UI, Start run under `/t/beta/ci`
navigates to `/t/beta/ci/<alpha's run>`. The run is absent from beta's
list, which filters by `run.team`, and it was spent on alpha's capacity.

Cause: `ci.Service.Start` hands a request whose plan key matches a live
run that run, and `planKey` (`pkg/ci/fingerprint.go`) did not include
the team.

Fix: `fix(ci): keep one team's live CI run from answering another team's
request`. `planKey` folds in the team, lower-cased. The default team
hashes as no team, so every run filed without one keeps its key. Job
keys, and so the shared verdict cache, are unchanged. The one effect of
an upgrade is that a non-default team's run in flight across it is not
deduplicated against an identical new request.

Tests: `TestPlanKeyTellsTeamsApart` (`pkg/ci/plan_test.go`) checks that
teams differ, that case does not matter, and that default equals none.
The e2e "New task, New session and a CI run each file into the team in
the URL" fails without the fix (`Expected: "beta"`, `Received: "alpha"`).

### 2. New task's Target team never offered a viewer the team they can file into (fixed)

`1ac5f4314` holds Create task back from a viewer of the team the dialog
opens in, and says "picking one of the viewer's own teams lifts the
hold". But the picker (`teamsToCreateIn`, from grain/task-3232) offers
only the current team plus the teams the reader *administers*.

Reproduction: as `mixed` (member of alpha, viewer of beta), open
`/t/beta/new`. The options are `["Beta Team (beta)", "mixed (mixed)"]`.
The only way out of the hold was the personal team, and there was no
way to reach alpha. This is the interaction the plan called out between
`badd810ec`/`3b15d9f00` and `1ac5f4314`.

Fix: `fix(ui): let a viewer pick a team they can file into from New
task`. A new helper, `teamsToFileTaskIn` (`ui/src/teamSetup.js`), is
`teamsToCreateIn` unchanged, except when the team the dialog *opened* in
is one the reader may not file into (`taskFileRefusal`). In that case it
also offers every live team where the reader holds a non-viewer role. It
is keyed on the opening team, so beta stays on the list after a pick.
Members still file into the team they are working in; task-3232's rule
is untouched.

Tests: a NewTaskOverlay vitest ("offers a viewer the teams they may file
into, and picking one lifts the hold") fails without the fix. The e2e
"a viewer of beta who is a member of alpha lifts New task's hold by
picking alpha" covers the same in the browser.

### 3. A viewer can start a by-hand CI run attributed to the team they only view (not fixed: proposed separately)

As `viewer` (alpha: viewer only),
`POST /api/ci/runs {"repo":"acme/widgets","branch":"main","workflow":".grain/workflows/observability.yml","team":"alpha","recheck":true}`
returns 202 with the run attributed to `alpha`. The same viewer gets 403
from tasks, sessions, templates, schedules, suites and boards, and
`/t/alpha/ci` shows them an enabled "Start a run".

`ciRunTeam` (`pkg/ui/ci.go`) decides the team by the caller's *read*
scope, and its doc comment says this is on purpose. Tightening it to
member/admin is an authorization-policy change, with a matching UI hold
to add, so it was proposed as a separate task for a human to decide
rather than changed in an unattended merge. `tests/security/routes_test.go`
was not touched: the route is `member` there, and this is a team-role
check inside the handler.

## Regression coverage added

- `pkg/ci/plan_test.go`: `TestPlanKeyTellsTeamsApart`.
- `ui/src/components/NewTaskOverlay.test.jsx`: the viewer picker case.
- `ui/e2e/team-scope-shell-walkthrough.spec.js` (7 tests, about 23 s
  locally, `-observability` demo, non-admin users): creation forms
  including New session and CI read back per team; schedule, template and
  suite moved to beta from their edit panes; both viewer cases; legacy
  pin migration and per-team Reset; upper-case team URLs.

## Not run, and why

- The axe issues outside the top bar (`.task-number` contrast and the
  nested-interactive task row) were recorded only. They are not part of
  §1.
- The New task repo default under a single-repo non-default team (step 2
  side observation) is not part of §1 and was not changed.
- Old *stored* links were exercised as typed URLs (`/teams/ALPHA/...`,
  `/team/Beta/...`) rather than from a bookmark or an export. They take
  the same code path (`parsePath`).
- WebKit: the walkthrough and the new spec ran on Chromium. The suite's
  WebKit project runs only the specs it lists, and this spec is not one
  of them.

## grain CI verdict

Full suite green at `e91165b`, which is the fixes, the spec and this
report merged with `candidate` at `591f0f35f`. Run `a4ea2e35439ce851`
passed every job except `frontend`. There, one vitest unrelated to this
change (`App.test.jsx` "shows a loading screen with the large mark until
config loads") timed out waiting for "Fix bug". It passes locally (46/46)
and passed on an uncached rerun (`bec9de838205fc36`), so it is a flake
under load. The combined verdict (`29e18f78ca97fc3b`) shows frontend,
go (17m55s), ui e2e (22m4s), lint, installer, postgres, package cache,
relay image, run host and terraform all successful. This paragraph is
the only change after that; its own commit's verdict is on the pull
request.
