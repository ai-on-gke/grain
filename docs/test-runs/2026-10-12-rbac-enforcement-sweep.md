# RBAC enforcement sweep — 2026-10-12

Walkthrough of `docs/test-plans/2026-10-11.md` §3 (task 3139, filed by task 2955) against
`candidate` at `b2ce41d09`. It covers commits `2e2df4c21`, `4104c201f`, `eee375fab`, `ab4096f9a`,
`962dfe8ef`, `47a78f8d0` and `d0a13ab8a`, which are all on `origin/candidate`.

## Environment

- `grain demo -addr 127.0.0.1:0 -open=false -default-target-repo acme/widgets`, started by
  `ui/e2e/fixtures.js` (one demo per Playwright worker, embedded SQLite, built with `-tags grain_e2e`).
  The sandbox also ran a manual demo on `127.0.0.1:18080` for the curl reproduction below.
- The frontend was built from this tree (`cd ui && npm ci && npm run build`) and driven by Playwright
  Chromium (headless shell 151). The browser download needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt` in the sandbox.
- Users signed up and signed in for real (`/api/auth/register`, `/api/auth/login`), with no claimed users:
  - `sysadmin`: in `adminAccounts`. It is used twice: with `X-Grain-Admin-Mode: on`, and with no header.
  - `alead`: admin of team `alpha`.
  - `amember`: member of `alpha`.
  - `bmember`: member of `beta`.
  - `repoadmin`: member of `alpha`, and in `RepoConfig.Admins` of `acme/widgets`.

  Both teams target `acme/widgets`. The spec runs under `test.use({ extraHTTPHeaders: {} })`, and only
  the "Admin mode on" context adds the header.
- Every user who signs up is also the admin of a personal team named after them. This matters for the
  create routes (see the note under the matrix).

## Stubs

None of grain's own APIs or React were mocked. The only stub is the demo's own capability checker
(`pkg/demo`'s `demoCapabilityChecks`), which stands in for the external credential probe behind
`POST /api/capabilities/{id}/check`. The demo has no secrets store on the release-secret path, so an
authorized release-secret write answers 404 "no secrets directory". That is expected here.

## Role × route matrix

Each cell shows two statuses: the valid body, then the malformed one.

The malformed body depends on the route:
- `POST` schedules, templates, suites and repos: JSON that names `team: "alpha"`, with every other
  field given the wrong type.
- `GET /api/logs/{source}`: `?lines=x`.
- Every other route: the bytes `{not json`.

These statuses are after the fixes below. Before the fixes, the cells marked † were `400` for the
malformed body, and the cells marked ‡ were `404` for every role.

| Route | sysadmin (Admin mode on) | sysadmin (Admin mode off) | team-A admin | team-A member | team-B member | repo admin |
| --- | --- | --- | --- | --- | --- | --- |
| `POST /api/schedules` | 201 / 400 | 403 / 403† | 201 / 400 | 403 / 403† | 403 / 403† | 403 / 403† |
| `PATCH /api/schedules/{id}` | 200 / 400 | 404 / 404† | 200 / 400 | 403 / 403† | 404 / 404† | 403 / 403† |
| `DELETE /api/schedules/{id}` | 204 / 204 | 404 / 404 | 204 / 204 | 403 / 403 | 404 / 404 | 403 / 403 |
| `POST /api/templates` | 201 / 400 | 403 / 403† | 201 / 400 | 403 / 403† | 403 / 403† | 403 / 403† |
| `PATCH /api/templates/{id}` | 200 / 400 | 404 / 404† | 200 / 400 | 403 / 403† | 404 / 404† | 403 / 403† |
| `DELETE /api/templates/{id}` | 204 / 204 | 404 / 404 | 204 / 204 | 403 / 403 | 404 / 404 | 403 / 403 |
| `POST /api/suites` | 201 / 400 | 403 / 403† | 201 / 400 | 403 / 403† | 403 / 403† | 403 / 403† |
| `PATCH /api/suites/{id}` | 200 / 400 | 404 / 404† | 200 / 400 | 403 / 403† | 404 / 404† | 403 / 403† |
| `DELETE /api/suites/{id}` | 204 / 204 | 404 / 404 | 204 / 204 | 403 / 403 | 404 / 404 | 403 / 403 |
| `POST /api/repos` | 200 / 400 | 403 / 403† | 200 / 400 | 403 / 403† | 403 / 403† | 403 / 403† |
| `POST …/candidates/{id}/qualification/approve` | 404 / 404 | 403 / 403 | 404 / 404 | 403 / 403 | 403 / 403 | 404 / 404 |
| `PUT …/release-secrets/{secret}` | 404 / 404 | 403 / 403‡ | 404 / 404 | 403 / 403‡ | 403 / 403‡ | 404 / 404 |
| `DELETE …/release-secrets/{secret}` | 404 / 404 | 403 / 403‡ | 404 / 404 | 403 / 403‡ | 403 / 403‡ | 404 / 404 |
| `POST /api/tasks/{id}/force-merge` | 400 / 400 | 404 / 404 | 400 / 400 | 403 / 403 | 404 / 404 | 400 / 400 (was 403 / 403) |
| `POST /api/capabilities/{id}/check` | 200 / 200 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PUT /api/secrets/{secret}/{key}` | 404 / 404 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PUT /api/secrets/{secret}/teams` | 404 / 404 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `DELETE /api/secrets/{secret}/{key}` | 404 / 404 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `DELETE /api/secrets/{secret}` | 404 / 404 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `GET /api/logs/{source}` | 200 / 400 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |

How to read the matrix:

- **404 for a role outside alpha.** The system admin with Admin mode off and the team-B member cannot
  read alpha's objects, so they get 404. That is the same "no such object" answer a read of the object
  gives, and a refusal that does not reveal whether the object exists. Nothing is written in either case.
- **Allowed roles.** An allowed role's 400/404 comes from its own request: the force-merge probe sends
  an empty reason on purpose so it never actually merges, candidate 999999 does not exist, and the demo
  has no secrets store. The demo's `-default-target-repo` makes `acme/widgets` the default target, so
  `POST /api/repos` answers 200 for the system admin and the team admin.
- **A body that is not JSON at all.** Sent to a create route, it still gets a 400 from every role. That
  is correct: every signed-up user administers their own personal team, so they may create standing work.
  A body that cannot say which team it is for has nothing to authorize against. The matrix therefore uses
  a body that names alpha.
- **Admin mode (step 3).** With Admin mode off, the system admin is treated as a normal user on every
  route: 403, or 404 for alpha's objects. With it on, every route lets them through.

## UI controls

Each cell shows the control's state for that role after the fix.

| Control | sysadmin (on) | sysadmin (off) | team-A admin | team-A member | team-B member | repo admin |
| --- | --- | --- | --- | --- | --- | --- |
| Schedules: + New schedule | offered | offered | offered | offered | offered | offered |
| Templates: + New template | offered | offered | offered | offered | offered | offered |
| Suites: + New suite | offered | offered | offered | offered | offered | offered |
| Repos: Add repo | offered | offered | offered | offered | offered | offered |
| Task pane: Force merge | offered | hidden | offered | hidden (**was offered → 403**) | hidden | hidden (see below) |
| Settings → Capabilities: Test credential | hidden | hidden | hidden | hidden | hidden | hidden |
| Settings → Logs (host logs) | offered | hidden | hidden | hidden | hidden | hidden |

- **New schedule / New template / New suite / Add repo.** These are offered to everyone, and that is not
  an offered-then-403. Every signed-up user is the admin of their own personal team and may create in it.
  The form's team field then decides the outcome, and the server refuses a team the user does not
  administer. A finer UI (limiting the team picker to `adminTeams`) was filed as a follow-up and is done; see the task 3186 section below.
- **Force merge.** This was an offered-then-403 bug for the team-A member, and it is now fixed: the pane
  hides the button unless config says the user administers the task's team. A repository admin who is not
  a team admin cannot be told apart from a member in the pane, so the button is hidden for them too. They
  can still force the merge through the API.
- **Test credential.** The demo configures no capability credentials, so this is hidden for every role,
  including the system admin with Admin mode on. The spec records it but does not require it to be offered.
- **Not driven in the browser:** qualification "Approve all" (the demo seeds no qualification run),
  release secrets (the demo has no secrets store), and Settings → Secrets writes. Their API rows above
  are 403 for every role that is not allowed. Driving their UI needs fixtures the demo does not have.
  This was part of the follow-up; see the task 3186 section below.

## Bugs, reproductions and fixes

### 1. Standing-work create/update and `POST /api/repos` decoded before authorizing

Reproduction, against a demo with team alpha (as in the spec), signed in as `amember`:

    curl -b cj -XPOST $B/api/schedules -H 'Content-Type: application/json' \
      -d '{"team":"alpha","recurrence":"x"}'
    # before: 400 json: cannot unmarshal string into ... CreateScheduleRequest
    # after:  403 user "amember" can read team "alpha"'s work but cannot create a schedule in it

PATCH had the same problem: a member's or an outsider's malformed body got a 400.

**Fix (`pkg/ui/standing_work_preauth.go`):**
- **PATCH** on a schedule, template or suite now looks up the stored object and authorizes against its
  team before it reads the body (`authorize{Schedule,Template,Suite}Edit`).
- **POST** reads the body's `team` ahead of the decode (`bodyTeam`), authorizes that team, and refuses a
  caller who administers no team at all (`authorizeStandingCreate`).
- **`POST /api/repos`** authorizes the body's team the same way when the query names none.

### 2. Release-secret writes answered 404 before checking repo admin

`PUT` and `DELETE /api/repos/acme/widgets/release-secrets/X` told every caller, including
`bmember`, "secrets are not available: this UI has no local secrets directory". **Fix:**
`requireReleaseSecretAdmin` asks for repo admin first. Callers who are not allowed now get 403 (‡ in
the matrix).

### 3. Force-merge refused a repository admin

The route wrapper's `authorizeTaskAdmin` asked only for the team admin role, so `repoadmin` got a 403:
"you cannot force-merge tasks of team "alpha": that needs the admin role on it". `eee375fab`'s
`Client.ForceMerge` / `checkForceMergeAdmin` allows repo admins. **Fix:** the wrapper now falls back to
`checkForceMergeAdmin`. A plain member is still refused, and the route's `teamAdmin` level in
`tests/security/routes_test.go` is unchanged.

### 4. UI offered Force merge to a team member

**Fix:**
- `/api/config` now returns `adminTeams` and `adminScoped`.
- `forceMergeOffered` in `ui/src/taskWrites.js` hides the button unless the caller administers the
  task's team, or nothing restricts them.

## Regression tests

- `ui/e2e/rbac-enforcement-sweep.spec.js`: the whole matrix and the UI control pass as a durable
  Playwright spec. It fails on any of bugs 1–4.
- `ui/src/taskWrites.test.js`: `forceMergeOffered`.
- `tests/security` and `pkg/ui` Go tests pass locally. No expectation in `tests/security/routes_test.go`
  was lowered, and nothing under `.grain/` was edited.

## CI verdict

grain CI's full suite was run with `wait_for_checks` on the pushed branch. The verdict is reported in
the pull request and the task's closing note.


## Follow-up: admin-role gating (task 3186)

Task 3186 closed the follow-ups above. The same spec was re-run against `grain demo` with two new opt-in
flags:
- `-qualification` seeds release `1.0` of `acme/widgets`, whose active candidate has a qualification run
  pending approval (`pkg/demo/demo_qualification.go`).
- `-secrets-store` wires a real `pkg/secrets` store in the demo's temporary directory.

Neither flag is a stub: both write the same rows and files a deployment has. Team pages are opened
under `/t/alpha/...`. A role that cannot read alpha is moved to a team of its own; that is recorded as
"(in <team>)", and those cells are not held to alpha's rules.

### Changes

- **Standing-work team pickers.** The New schedule, New template and New suite pickers now list only the
  teams in `config.adminTeams` when `config.adminScoped` is set (`teamsToAdministerIn`), plus the team an
  item being edited already sits in. A new item starts in a team the reader administers
  (`standingTeam`): the selected team, or else their personal team. A member who opens New template
  under alpha now creates it in their own team, and the server accepts it.
- **Add repo.** Add repo writes into the selected team, so it is shown only when the reader administers
  that team (`administers`).
- **`adminRepos` on `/api/config`.** `/api/config` now lists the repos the caller is a repository admin of
  (`Client.adminReposOf`). It follows `authorizeRepo`'s rule: repo configs that name the caller, plus the
  target and config repos of the non-default teams they administer. The list uses these controls:
  - **Force merge** is offered again to a repo admin of the task's repo (`forceMergeOffered`).
  - **Releases → Approve all** is offered only to repo admins. For anyone else the banner says a repo
    admin approves.
  - **Release secrets: Replace, Remove and Add custom secret** are offered only to repo admins.
- **Settings → Secrets** was already gated: the Capabilities tab is shown only to a system admin with
  Admin mode on. The spec now records that, and it presses Set secret and Delete secret too.
- **Release-secret DELETE no longer answers 500.**
  - *Bug:* a DELETE of a release secret the store did not hold answered 500 (`secrets: no key …`). That
    included a secret the repo still declares, which is the pane's "Missing" row, so Remove could never
    clear it.
  - *Fix:* the declaration is now dropped whether or not the store holds the key. A secret that is
    neither stored nor declared gets a 404 (`TestRepoReleaseSecretDeleteOfAMissingSecret`).

### Role × route matrix (re-run)

The 200s and 404s for allowed roles on the secret routes come from the real store. Each allowed role in
turn deletes the same secret, so the later deletes find it already gone.

| Route | sysadmin (Admin mode on) | sysadmin (Admin mode off) | team-A admin | team-A member | team-B member | repo admin |
| --- | --- | --- | --- | --- | --- | --- |
| `POST /api/schedules` | 201 / 400 | 403 / 403 | 201 / 400 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PATCH /api/schedules/{id}` | 200 / 400 | 404 / 404 | 200 / 400 | 403 / 403 | 404 / 404 | 403 / 403 |
| `DELETE /api/schedules/{id}` | 204 / 204 | 404 / 404 | 204 / 204 | 403 / 403 | 404 / 404 | 403 / 403 |
| `POST /api/templates` | 201 / 400 | 403 / 403 | 201 / 400 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PATCH /api/templates/{id}` | 200 / 400 | 404 / 404 | 200 / 400 | 403 / 403 | 404 / 404 | 403 / 403 |
| `DELETE /api/templates/{id}` | 204 / 204 | 404 / 404 | 204 / 204 | 403 / 403 | 404 / 404 | 403 / 403 |
| `POST /api/suites` | 201 / 400 | 403 / 403 | 201 / 400 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PATCH /api/suites/{id}` | 200 / 400 | 404 / 404 | 200 / 400 | 403 / 403 | 404 / 404 | 403 / 403 |
| `DELETE /api/suites/{id}` | 204 / 204 | 404 / 404 | 204 / 204 | 403 / 403 | 404 / 404 | 403 / 403 |
| `POST /api/repos` | 200 / 400 | 403 / 403 | 200 / 400 | 403 / 403 | 403 / 403 | 403 / 403 |
| `POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve` | 404 / 404 | 403 / 403 | 404 / 404 | 403 / 403 | 403 / 403 | 404 / 404 |
| `PUT /api/repos/{owner}/{name}/release-secrets/{secret}` | 200 / 400 | 403 / 403 | 200 / 400 | 403 / 403 | 403 / 403 | 200 / 400 |
| `DELETE /api/repos/{owner}/{name}/release-secrets/{secret}` | 200 / 404 | 403 / 403 | 404 / 404 | 403 / 403 | 403 / 403 | 404 / 404 |
| `POST /api/tasks/{id}/force-merge` | 400 / 400 | 404 / 404 | 400 / 400 | 403 / 403 | 404 / 404 | 400 / 400 |
| `POST /api/capabilities/{id}/check` | 200 / 200 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PUT /api/secrets/{secret}/{key}` | 200 / 400 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `PUT /api/secrets/{secret}/teams` | 200 / 400 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `DELETE /api/secrets/{secret}/{key}` | 200 / 404 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `DELETE /api/secrets/{secret}` | 404 / 404 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |
| `GET /api/logs/{source}` | 200 / 400 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 | 403 / 403 |

### UI controls (re-run)

| Control | sysadmin (Admin mode on) | sysadmin (Admin mode off) | team-A admin | team-A member | team-B member | repo admin |
| --- | --- | --- | --- | --- | --- | --- |
| New schedule | offered | offered (in sysadmin) | offered | offered | offered (in beta) | offered |
| New template | offered | offered (in sysadmin) | offered | offered | offered (in beta) | offered |
| New suite | offered | offered (in sysadmin) | offered | offered | offered (in beta) | offered |
| Add repo | offered | offered (in sysadmin) | offered | hidden | hidden (in beta) | hidden |
| Force merge | offered | hidden | offered | hidden | hidden | offered |
| Releases: Approve all | offered | hidden (in sysadmin) | offered | hidden | hidden (in beta) | offered |
| Release secrets: Add custom secret | offered | hidden (in sysadmin) | offered | hidden | hidden (in beta) | offered |
| Release secrets: Replace | offered | hidden (in sysadmin) | offered | hidden | hidden (in beta) | offered |
| Settings → Secrets: Set secret | offered | hidden | hidden | hidden | hidden | hidden |
| Settings → Secrets: Delete secret | offered | hidden | hidden | hidden | hidden | hidden |
| Capabilities: Test credential | hidden | hidden | hidden | hidden | hidden | hidden |
| Host logs tab | offered | hidden | hidden | hidden | hidden | hidden |

### Team pickers (options offered in each dialog's Target team)

| Picker | sysadmin (Admin mode on) | sysadmin (Admin mode off) | team-A admin | team-A member | team-B member | repo admin |
| --- | --- | --- | --- | --- | --- | --- |
| schedule | alead (alead), Alpha (alpha), amember (amember), Beta (beta), bmember (bmember), admin (default), repoadmin (repoadmin), sysadmin (sysadmin) | sysadmin (sysadmin) | alead (alead), Alpha (alpha) | amember (amember) | bmember (bmember) | repoadmin (repoadmin) |
| template | alead, Alpha, amember, Beta, bmember, admin (default), repoadmin, sysadmin | sysadmin | alead, Alpha | amember | bmember | repoadmin |
| suite | alead, Alpha, amember, Beta, bmember, admin (default), repoadmin, sysadmin | sysadmin | alead, Alpha | amember | bmember | repoadmin |

### Pressed in the browser

- **The repo admin** (a member of alpha who is in `RepoConfig.Admins`):
  - pressed **Approve all** on release 1.0's qualification run, and the run left "Pending approval";
  - added the release secret `SWEEP_UI` through **Add custom secret**, saw it listed as "Set", and
    removed it with **Remove**.
- **The system admin with Admin mode on** set the secret `sweepui/token` through Settings → Integrations
  → **Set secret**, and deleted it with **Delete secret**.
- **The team-A member** created a template from New template under alpha. It was created in their own
  team.

### Remaining limitation

A team's `targetRepos` entry is matched in the browser the way `model.RepoAllowed` matches it: exact,
`owner/*`, `*`, and path.Match's `*`. A pattern beyond those is not covered, and the server stays the
authority.
