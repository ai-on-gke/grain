# Role-shaped UI sweep — 2026-10-14

Walkthrough of `docs/test-plans/2026-10-13.md` §11 (task 3557, filed by task 3492) against `candidate`
at `d2f5bb8aa`. The commits under test, `829b5a6b7`, `31f151707`, `2a95f0efb`, `6912d2b82`,
`480db93fb`, `f019e6f9f` and `1d97e51a8`, are all on `origin/candidate` and `origin/main`
(`git branch -r --contains`).

## Environment

- `grain demo -addr 127.0.0.1:0 -open=false -default-target-repo acme/widgets -qualification
  -secrets-store`, started by `ui/e2e/fixtures.js` (embedded SQLite, `-tags grain_e2e`). A second manual
  demo on `127.0.0.1:18080` with the same flags and roles was used for the exploration and the 390x844
  pass.
- The frontend was built from this tree (`cd ui && npm ci && npm run build`) and driven by Playwright
  Chromium. Browser install needed `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
- Two teams and two repos: `alpha` targets `acme/widgets`, `beta` targets `acme/gadgets`. The users
  registered and signed in for real:

  | Role | User | Holds |
  | --- | --- | --- |
  | system admin, Admin mode on | `sysadmin` | `adminAccounts`, `X-Grain-Admin-Mode: on` |
  | system admin, Admin mode off | `sysadmin` | the same, without the header |
  | multi-team admin | `multi` | admin of alpha and beta |
  | single-team admin | `single` | admin of alpha |
  | member | `amember` | member of alpha |
  | viewer | `aviewer` | viewer of alpha |
  | proposer-only | `aprop` | proposer of alpha |
  | no team | `nobody` | their personal team deleted (`DELETE /api/teams/nobody`) |

  Every user except `nobody` also administers the personal team signing up gives them. That is why a
  single-team admin's and a member's pickers list a second team. The spec runs under
  `test.use({ extraHTTPHeaders: {} })`, and only the Admin-mode-on context adds the header.

## Stubs

None of grain's own APIs or React were mocked. The external boundaries stand in as the demo provides
them:
- the demo's seeded GitHub-shaped tasks and pull requests (no forge is called);
- the `-qualification` seed of release 1.0 of `acme/widgets`;
- a real `pkg/secrets` store in the demo's temp directory (`-secrets-store`).

No live session was started: the session dialog was opened and read, not submitted.

## Method

`ui/e2e/role-shaped-ui-sweep.spec.js` (kept as a reusable spec) visits each surface as each role and
records the control's state. It then asks the server the request the control stands for, and holds the
two to each other in both directions:
- **shown and refused**: the control is offered, and the server answers 403;
- **hidden but allowed**: the control is hidden or disabled, and the server lets the request through.

Every offered control was pressed. For the creation pickers, a real create was posted for alpha, beta
and the team the page landed in.

## Role × surface

### Switcher (step 2), desktop

| Role | lands on | teams offered | switch from a repo page, then Back |
| --- | --- | --- | --- |
| sysadmin (on) | sysadmin | every team (9) | `/t/sysadmin/repos` → `/t/alpha` (home); Back → `/t/sysadmin/repos` |
| sysadmin (off) | sysadmin | sysadmin | — (one team) |
| multi-team admin | alpha | Alpha, Beta, multi | `/t/alpha/repos` → `/t/beta` (home); Back → `/t/alpha/repos` |
| single-team admin | alpha | Alpha, single | → `/t/single` (home); Back returns |
| member | alpha | Alpha, amember | → `/t/amember` (home); Back returns |
| viewer | alpha | Alpha, aviewer | → `/t/aviewer` (home); Back returns |
| proposer-only | alpha | Alpha, aprop | → `/t/aprop` (home); Back returns |
| no team | default | admin (the default team) | → `/t/default`: **"This team is not one you can see."** (finding 2) |

### `/t/default/tasks`

| Role | `GET /api/teams/default` | ends on | notice | Back |
| --- | --- | --- | --- | --- |
| sysadmin (on) | 200 | `/t/default/tasks` | no | previous page |
| sysadmin (off) | 403 | `/t/sysadmin/tasks` | yes | previous page |
| multi / single / member / viewer / proposer | 403 | `/t/alpha/tasks` | yes | previous page (not `/t/default`) |
| no team | 403 | **`/t/default/tasks`** | no | `/t/default/...` (finding 2) |

### 390x844 (step 4)

At phone width the switcher sits in the navigation drawer ("Open navigation"). For `multi` and `amember`,
picking a team from a repo page opened that team's home, with no horizontal overflow (scrollWidth 390).
Back returned to the repo page. `/t/default/tasks` redirected to `/t/alpha/tasks` with the notice, and
Back did not return to `/t/default`. `nobody` behaves as on desktop (finding 2).

### Creation dialogs opened under `/t/alpha` (Target team picker; real POSTs)

| Dialog | sysadmin (on) | sysadmin (off) | multi-team admin | single-team admin | member | viewer | proposer-only | no team |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| task | Alpha; all 9 teams; alpha 201, beta 201 | in sysadmin; no picker | Alpha; Alpha, Beta, multi; 201/201 | Alpha; Alpha, single; 201/403 | Alpha; Alpha, amember; 201/403 | Alpha; Alpha, aviewer; alpha **403** (finding 1, fixed) | Alpha; Alpha, aprop; 201 (proposal)/403 | in default; no picker; default **403** |
| session | Alpha; all 9 | in sysadmin; none | Alpha, Beta, multi | Alpha, single | Alpha, amember | Alpha, aviewer | Alpha, aprop | in default; none |
| schedule | Alpha; all 9; 201/201 | in sysadmin | Alpha, Beta, multi; 201/201 | Alpha, single; 201/403 | in amember; no picker; 403/403 | in aviewer; 403/403 | in aprop; 403/403 | in default; **403** |
| template | as schedule | in sysadmin | Alpha, Beta, multi | Alpha, single; 201/403 | in amember | in aviewer | in aprop | in default; **403** |
| suite | as schedule | in sysadmin | Alpha, Beta, multi | Alpha, single; 201/403 | in amember | in aviewer | in aprop | in default; **403** |

A template or suite posted for beta answered 400 for the admins only because the probe named
`acme/widgets`, which beta does not target. That is an allowed answer, not a refusal. Schedules,
templates and suites list only admin teams, and members start in their personal team, as `1d97e51a8`
intends. A picker appears only when there is more than one team to offer.

### Repository page (`/t/<team>/repos/<repo>`: CI Mode, Add branch, declarations, CI capabilities, setup command)

| Repo | sysadmin (on) | sysadmin (off) | multi | single | member | viewer | proposer | no team |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| acme/widgets (alpha) | offered; PUT ci-mode 200 | redirected; 403 | offered; 200 | offered; 200 | **offered; 403** (finding 3) | disabled + read-only sentence; 403 | disabled + sentence; 403 | in default; 403 |
| acme/gadgets (beta) | offered; 200 | redirected; 403 | offered; 200 | redirected to alpha; 403 | redirected; 403 | redirected; 403 | redirected; 403 | in default; 403 |

The admins pressed CI Mode ("Use No CI", then back to "Use GitHub CI"), and the PUTs answered 200.

### Team Settings › Audit / Security

| Team | sysadmin (on) | sysadmin (off) | multi | single | member | viewer | proposer | no team |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| alpha | log (empty); policy offered; 200 | no tab; 403 | log; offered; 200 | log; offered; 200 | admins-only note; policy disabled; 403 | note; disabled; 403 | note; disabled; 403 | no tab; 403 |
| beta | log; offered; 200 | no tab; 403 | log; offered; 200 | no tab; 403 | no tab; 403 | no tab; 403 | no tab; 403 | no tab; 403 |

The read-only panel made no `audit-logs` request: the page saw no 403 for it. No refusal was drawn as an
empty log. The empty logs are for admins of teams with no events yet.

### Admin-role offers (state; the server's answer)

| Control | sysadmin (on) | sysadmin (off) | multi | single | member | viewer | proposer | no team |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Releases: Approve all | offered; 404* | hidden; 403 | offered; 404* | offered; 404* | hidden; 403 | hidden; 403 | hidden; 403 | hidden; 403 |
| Release secrets: Add custom secret | offered; 200 | hidden; 403 | offered; 200 | offered; 200 | hidden; 403 | hidden; 403 | hidden; 403 | hidden; 403 |
| Release secrets: Replace | offered; 200 | hidden; 403 | offered; 200 | offered; 200 | hidden; 403 | hidden; 403 | hidden; 403 | hidden; 403 |
| Force merge | offered; 400† | hidden; 404‡ | offered; 400† | offered; 400† | hidden; 403 | hidden; 403 | hidden; 403 | hidden; 404‡ |
| Add repo (alpha) | offered; 200 | in sysadmin | offered; 200 | offered; 200 | hidden; 403 | hidden; 403 | hidden; 403 | hidden; 403 |
| Add repo (beta) | offered; 200 | in sysadmin | offered; 200 | in alpha; 403 | in alpha | in alpha | in alpha | in default |

\* Candidate 999999 does not exist, so the probe answers 404: allowed past authorization.
† An empty reason gets the 400 asking for one, so the sweep never actually merges.
‡ A task of a team the caller cannot read is "no such task", which is the refusal for an outsider.

### Accessibility (step 3)

- **Read-only repository page.** The reason is in the accessibility tree as one `alert` in the page
  header: `you can read team "alpha"'s repositories but not change them: …`. The disabled controls
  themselves carry no accessible description. The viewer's CI Mode radio has no `aria-describedby`, and
  disabled buttons are not in the tab order, so a keyboard user meets the reason only by reading the
  header. This is recorded in the RepoPage follow-up below.
- **Audit panel.** The reason is reachable. "Only this team's admins can change this policy." sits
  beside the disabled switch, and the log is replaced by an `alert` ("Only this team's admins can see
  its credential audit log.").
- **Release pane for a viewer.** "Jobs dispatched after every merge" stays an editable textbox while
  its Save is disabled. It writes nothing, and is noted only.
- **New task for a viewer** (after the fix): Create task is disabled and the server's sentence is its
  accessible description (vitest `toHaveAccessibleDescription`).

## Findings

1. **Fixed: New task offered a viewer a Create task the server refuses.** A viewer of alpha opening
   `+ New task` under `/t/alpha` saw "In team Alpha" and an enabled Create task. Submitting answered
   `403 you cannot file tasks into team "alpha": that needs the proposer, member or admin role on it`.
   Reproduction: as `aviewer`, `POST /api/tasks {"team":"alpha","title":"x","repo":"acme/widgets"}` →
   403. Fix in `NewTaskOverlay.jsx` and `taskWrites.js` (`taskFileRefusal`):
   - The dialog shows that sentence when the chosen team is on `readOnlyTeams` but not on
     `proposerTeams`.
   - Create task is disabled, with the sentence as its accessible description.
   - Picking one of the viewer's own teams lifts the hold.
2. **Filed: a reader on no team is left in `default`.** Reproduction: register, `DELETE` the personal
   team, sign in.
   - `/` lands on `/t/default/tasks`. The `2a95f0efb` redirect has nowhere to send them, so it shows no
     notice.
   - The switcher offers "admin" (default), which opens "This team is not one you can see."
   - Every creation dialog files into default and gets a 403.

   The server is right, so this is a UI design call. It is proposed as a follow-up task.
3. **Filed: the repository page offers a member writes that need repo admin.** As `amember` on
   `/t/alpha/repos/acme/widgets`, CI Mode, CI capabilities, Add branch, the setup command and
   declarations are all enabled. Pressing "Use No CI" sends `PUT /api/repos/acme/widgets/ci-mode`, which
   answers 403, and the radio reverts. `6912d2b82` disables the page only for viewers and proposers.
   Members are bound by `adminRepos`. Only CI Mode was pressed. The other controls are flagged by the
   same rule, so the follow-up must check each write's route level before gating it. It is proposed as a
   follow-up task, together with per-control accessible descriptions.

   Update: the member gate itself landed in `417cbd8e7` (task 3681), where every gated write was
   checked against `tests/security/routes_test.go`. All of them are `teamAdmin`, apart from
   `base-branch-runs`, which stays offered. Task 3745 makes the page's sentence each disabled
   control's `aria-describedby`, keeping a field's own helper text in it. It also drops the
   `member … acme/widgets` entries from the spec's `known` filter.

No hidden-but-allowed mismatch was found. Every control hidden or disabled from a role was refused by
the server for that role.

## Fixes and tests

- `ui/src/components/NewTaskOverlay.jsx`, `ui/src/taskWrites.js`: finding 1.
- `ui/src/components/NewTaskOverlay.test.jsx`: the viewer is shown the sentence, Create task is disabled
  and described by it, and nothing is posted.
- `ui/src/taskWrites.test.js`: `taskFileRefusal` refuses a viewer, and refuses neither a proposer nor an
  unknown team.
- `ui/e2e/role-shaped-ui-sweep.spec.js`: the whole sweep, which prints the tables above. It fails on any
  mismatch except findings 2 and 3, which it tolerates by name until their follow-ups land. Finding 1
  would fail it.
- No server change, no route expectation touched, nothing under `.grain/` edited.

## CI verdict

grain CI's full suite was run with `wait_for_checks` on the pushed branch. The verdict is in the task's
closing note.
