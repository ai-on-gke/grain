# Team repo default capabilities on the New task form: 2026-10-07

This run executes §3 of `docs/test-plans/2026-10-07.md` (task 2422, filed by task 2411).

## Summary

- **Server agreement holds.** In all 72 cells of the resolution matrix (deployment default ×
  repo default × team repo default × team × whether `gcp-key` is widened to `payments`), the
  New task form's seeded list (`defaultCapabilitiesFor` over the live `GET /api/config`)
  equals what `POST /api/tasks` with no capability list stores. That held before the fixes
  below and still holds after them.
- **`/api/config` visibility holds.** A single-team member, another team's viewer and a system
  admin each get only their visible teams' `teamRepoDefaultCapabilities`. This run adds a
  per-caller test for it to `pkg/ui/server_test.go`.
- **Found and fixed:**
  - **(1)** A team could save a default it may not hold. The server then dropped it from every
    task without a word.
  - **(2)** New task re-read its stored draft on every render. After a team switch it showed,
    and filed, a repo the user had already moved off.
  - **(3)** The team's repo page promised that team the deployment's admin-team-only defaults.
  - **(4)** The New task note on pre-ticked boxes did not mention the team layer.
- **Proposed as follow-ups:**
  - **(5)** After a hand edit, switching team keeps the old team's own repo defaults ticked.
  - **(6)** Team repo config stores capability ids that nothing answers to.

## Environment

- Sandbox: Debian (kernel 6.1), Go 1.26.6, Node 20.19.0, Playwright 1.62.1 with Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Server: `go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`, run as a built binary,
  using demo's embedded SQLite store. The frontend was built with
  `cd ui && npm ci && npm run build` into `pkg/ui/static`. The Playwright spec starts its
  own demo through `ui/e2e/fixtures.js`.
- **Tested commit:** `candidate` at `6634d5c81` (the branch base). `git branch -r --contains`
  lists `origin/candidate` (and `origin/grain-merge-queue/candidate`) for each of `661aba7fd`,
  `78327446b` and `39ca8f20f`.
- **Setup:**
  - Team `payments` was created, with target repos `acme/widgets` and `acme/gadgets`.
  - Team `default` added Memory on `acme/widgets`, and `payments` added Release notes.
  - Demo's deployment defaults are Self debug, GCP key and Gemini key. GCP key and Self debug
    are authorized for team `default` only.

## Stubs

None beyond what `grain demo` already is. Every request went to the real demo server and its
SQLite store, and the UI was the production bundle. `grain demo` itself runs no GitHub,
providers or live sessions, and nothing here needed them. No `page.route` was used.

## Steps and results

### 1. Seeding, switching team and repo, hand edits, filing (Playwright)

New task was opened from `/teams/default/repos/acme/widgets`. This is
`ui/e2e/team_repo_default_capabilities_walkthrough.spec.js`, "seeds, follows team and repo, and
files what is ticked".

| Step | Ticked |
|---|---|
| Opened, team default | GCP key, Gemini key, Memory, Self debug |
| Team → payments | Gemini key, Release notes. Memory and the two admin-team-only defaults come off. |
| Repo → acme/gadgets (no team layer) | Gemini key |
| Repo → acme/widgets | Gemini key, Release notes |
| Team → default | GCP key, Gemini key, Memory, Self debug. Before fix (2), Repos turned into a text box holding `acme/gadgets` at this step; see finding 2. |
| Hand edit: untick Gemini key, tick Read issues | GCP key, Memory, Read issues, Self debug |
| Repo → grain/config-default → acme/widgets | Unchanged. Hand edits are kept, and the repo change does not re-seed. |
| Team → payments (after the hand edit) | Memory, Read issues. Only what payments may not hold comes off. See finding 5. |
| Create | Stored `memory, read-issues`, equal to the ticked boxes. |

In a second test, an untouched form for `payments` files `gemini-key, release-notes`. A bare
`POST /api/tasks {"repo":"acme/widgets","team":"payments"}` stores the same set.

**Result:** pass, after fix (2).

### 2. Resolution matrix (`curl` and `node` against the demo)

`matrix.mjs` (reproduced at the end of this report) sets each layer through the real endpoints:

- `PUT /api/settings` for `defaultCapabilities` and `capabilityTeams`;
- `PUT /api/repos/acme/widgets/capabilities`;
- `PUT /api/teams/{team}/repos/acme/widgets/config`.

For each cell it then reads `GET /api/config` and computes the form's list with the real
`defaultCapabilitiesFor` from `ui/src/state.js`. It files `POST /api/tasks` with no
`capabilities` and compares the two lists as sets.

- **Before the fixes (`6634d5c81`):** 72 cells, 0 mismatches. Every "team save" was accepted,
  including `gcp-key` for `payments` when it had not been widened. Both sides then dropped it
  silently (finding 1).
- **After the fixes:** 72 cells, 0 mismatches. The 6 cells that save `gcp-key` as a team
  default for an unwidened `payments` are refused at save time. The form and the server still
  agree on what is left.

Rows refused after the fix:

| Widened (`capabilityTeams`) | Deployment default | Repo default | Team repo default | Team | Team save | Form seeds | `POST /api/tasks` stores | Result |
|---|---|---|---|---|---|---|---|---|
| {} | gemini-key | - | memory+gcp-key | payments | refused | gemini-key | gemini-key | match |
| {} | gemini-key | read-issues | memory+gcp-key | payments | refused | gemini-key,read-issues | gemini-key,read-issues | match |
| {} | gemini-key | gcp-key | memory+gcp-key | payments | refused | gemini-key | gemini-key | match |
| {} | gemini-key+gcp-key | - | memory+gcp-key | payments | refused | gemini-key | gemini-key | match |
| {} | gemini-key+gcp-key | read-issues | memory+gcp-key | payments | refused | gemini-key,read-issues | gemini-key,read-issues | match |
| {} | gemini-key+gcp-key | gcp-key | memory+gcp-key | payments | refused | gemini-key | gemini-key | match |

<details><summary>Full matrix before the fixes (72 cells)</summary>

| Widened (`capabilityTeams`) | Deployment default | Repo default | Team repo default | Team | Team save | Form seeds | `POST /api/tasks` stores | Result |
|---|---|---|---|---|---|---|---|---|
| {} | gemini-key | - | - | default | saved | gemini-key | gemini-key | match |
| {} | gemini-key | - | - | payments | saved | gemini-key | gemini-key | match |
| {} | gemini-key | - | memory | default | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key | - | memory | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key | - | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key | - | memory+gcp-key | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key | read-issues | - | default | saved | gemini-key,read-issues | gemini-key,read-issues | match |
| {} | gemini-key | read-issues | - | payments | saved | gemini-key,read-issues | gemini-key,read-issues | match |
| {} | gemini-key | read-issues | memory | default | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {} | gemini-key | read-issues | memory | payments | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {} | gemini-key | read-issues | memory+gcp-key | default | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {} | gemini-key | read-issues | memory+gcp-key | payments | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {} | gemini-key | gcp-key | - | default | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {} | gemini-key | gcp-key | - | payments | saved | gemini-key | gemini-key | match |
| {} | gemini-key | gcp-key | memory | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key | gcp-key | memory | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key | gcp-key | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key | gcp-key | memory+gcp-key | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key+gcp-key | - | - | default | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {} | gemini-key+gcp-key | - | - | payments | saved | gemini-key | gemini-key | match |
| {} | gemini-key+gcp-key | - | memory | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key+gcp-key | - | memory | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key+gcp-key | - | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key+gcp-key | - | memory+gcp-key | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key+gcp-key | read-issues | - | default | saved | gcp-key,gemini-key,read-issues | gcp-key,gemini-key,read-issues | match |
| {} | gemini-key+gcp-key | read-issues | - | payments | saved | gemini-key,read-issues | gemini-key,read-issues | match |
| {} | gemini-key+gcp-key | read-issues | memory | default | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {} | gemini-key+gcp-key | read-issues | memory | payments | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {} | gemini-key+gcp-key | read-issues | memory+gcp-key | default | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {} | gemini-key+gcp-key | read-issues | memory+gcp-key | payments | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {} | gemini-key+gcp-key | gcp-key | - | default | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {} | gemini-key+gcp-key | gcp-key | - | payments | saved | gemini-key | gemini-key | match |
| {} | gemini-key+gcp-key | gcp-key | memory | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key+gcp-key | gcp-key | memory | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {} | gemini-key+gcp-key | gcp-key | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {} | gemini-key+gcp-key | gcp-key | memory+gcp-key | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | - | - | default | saved | gemini-key | gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key | - | - | payments | saved | gemini-key | gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key | - | memory | default | saved | gemini-key,memory | gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | - | memory | payments | saved | gemini-key,memory | gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | - | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | - | memory+gcp-key | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | read-issues | - | default | saved | gemini-key,read-issues | gemini-key,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key | read-issues | - | payments | saved | gemini-key,read-issues | gemini-key,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key | read-issues | memory | default | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key | read-issues | memory | payments | saved | gemini-key,memory,read-issues | gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key | read-issues | memory+gcp-key | default | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key | read-issues | memory+gcp-key | payments | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key | gcp-key | - | default | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key | gcp-key | - | payments | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key | gcp-key | memory | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | gcp-key | memory | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | gcp-key | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key | gcp-key | memory+gcp-key | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | - | - | default | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | - | - | payments | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | - | memory | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | - | memory | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | - | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | - | memory+gcp-key | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | read-issues | - | default | saved | gcp-key,gemini-key,read-issues | gcp-key,gemini-key,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | read-issues | - | payments | saved | gcp-key,gemini-key,read-issues | gcp-key,gemini-key,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | read-issues | memory | default | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | read-issues | memory | payments | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | read-issues | memory+gcp-key | default | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | read-issues | memory+gcp-key | payments | saved | gcp-key,gemini-key,memory,read-issues | gcp-key,gemini-key,memory,read-issues | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | gcp-key | - | default | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | gcp-key | - | payments | saved | gcp-key,gemini-key | gcp-key,gemini-key | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | gcp-key | memory | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | gcp-key | memory | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | gcp-key | memory+gcp-key | default | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |
| {"gcp-key":["payments"]} | gemini-key+gcp-key | gcp-key | memory+gcp-key | payments | saved | gcp-key,gemini-key,memory | gcp-key,gemini-key,memory | match |

</details>

**Result:** pass. There are no mismatches to report.

### 3. Other entry points (Playwright)

- **Use template.** The template's team is `payments` and its capabilities are `gemini-key`.
  Filed against acme/widgets from `/templates` → **Use…**, the task stores `gemini-key` only.
  `payments`' Release notes is not added. That is by design: templates, suites and schedules
  file with the set they were authored with (`ui.Client.defaultCapabilities`, "Only tasks
  filed through CreateTask are seeded"). **Pass.**
- **Suite run.** A suite of that template, run from `/suites` → **Run…** on acme/widgets
  `main`, files `gemini-key` only. **Pass.**
- **Board request.** **New board** on `/boards` opens New task on `grain/config-default`
  through the same `openBoardTask` path a panel's **Keep** uses. With team default adding Read
  issues on `grain/config-default`:
  - it ticks GCP key, Gemini key, Read issues and Self debug, with no `repo-off-allowlist`
    warning on the form;
  - the task stores those four plus `grain-ci`, the config repo's CI floor, which the server
    adds;
  - the task's page shows no "not on this deployment's target repo list" warning
    (`78327446b` holds).

  **Pass.**
- **Proposer.** `pat`, a proposer on team default, opened `/new` and picked acme/widgets. The
  form ticks GCP key, Gemini key, Memory and Self debug. The task is filed `proposed`, with
  exactly those four. **Pass.**

### 4. `GET /api/config` visibility (`curl`, then a Go test)

On the demo, each caller saw the following:

| Caller | `teams` | `teamRepoDefaultCapabilities` |
|---|---|---|
| `pam`, a member of payments only | pam, payments | payments only |
| `vic`, a viewer of payments | payments, vic | payments only. `pam`'s personal-team entry, set to `console`, is not shown. |
| `alice`, a member of default (the admin team), so a system admin | all five | default and payments, plus personal teams that have entries |
| No cookie (demo's trusted local operator) | all | all |

`pkg/ui/server_test.go` only had the anonymous-admin case and a ghost team. This run adds
`TestConfigEndpointTeamRepoDefaultCapabilitiesPerCaller`, which covers a plain member, another
team's viewer and a system admin. It passes on the base too, so it adds coverage rather than
catching a bug. **Pass.**

### 5. A restricted capability as an unauthorized team's default

Before the fix:

- `PUT /api/teams/payments/repos/acme/widgets/config {"defaultCapabilities":["release-notes","gcp-key"]}`
  returned 200 and stored both.
- New task for payments ticked only Gemini key and Release notes. The form agreed with the
  server, and neither said why GCP key was missing.
- The team's repo page said "A task payments files against acme/widgets starts with: Self
  debug, GCP key, Gemini key, Release notes", and offered GCP key as "already a deployment
  default -- on here either way".

After the fixes:

- The same `PUT` is refused with 400:

  > defaultCapabilities: capability "gcp-key" is restricted to authorized teams [default]; team "payments" is not authorized. A deployment operator can authorize team "payments" for it under Settings > Capabilities ("gcp-key", "Also authorize teams"); or team "payments" can configure a GCP key of its own under its Team Settings and grant gcp-key:<name> instead

- Ticking it on the page shows that sentence as an alert and stores nothing.
- The picker row reads "Restricted to teams: default.", and the summary reads "starts with:
  Gemini key, Release notes".
- New task still never ticks it.

The form never ticked something the server would refuse, so the form needed no change. The
gaps were on the save path and on the team's repo page. **Pass, after fixes (1) and (3).**

## Findings

Each finding was checked against `docs/test-runs/` (in particular
`2026-10-06-repository-release-memory-ui.md` and `2026-10-07-task-intake-inbox-lifecycle.md`,
which cover `661aba7fd`'s happy path) for an earlier report of the same thing. None of them
had been reported.

### 1. A team could save a repo default it may not hold (fixed)

**Reproduction** (`grain demo`, with team `payments` created):

```
curl -XPUT $B/api/teams/payments/repos/acme/widgets/config -d '{"defaultCapabilities":["gcp-key"]}'
# 200, stored
curl -XPOST $B/api/tasks -d '{"title":"t","repo":"acme/widgets","team":"payments"}' | jq .capabilities
# no gcp-key: CreateTask drops an inherited default the team may not hold, silently
```

**Fix:** `pkg/ui/teams.go` (`SetTeamRepoConfigWithUser`) refuses a newly added
`defaultCapabilities` id that the team cannot hold. It uses `model.TeamUnauthorizedRefusal`,
the same rule `39ca8f20f` applies to `ciCapabilities`. Ids already stored are let through.

**Regression test:** `TestTeamRepoConfigRefusesDefaultCapabilityTheTeamCannotHold` fails
without the fix ("accepted a gcp-key default…"). `TestTeamRepoConfigCRUDAndEffectiveOverrides`
used to save `package-publish` (admin-team-only) as a default for `team-alpha`. That is now
refused, so the test saves `memory` instead.

`pkg/ui` touches the trust-boundary list (`pkg/ui` auth), so a security-review hold on merge is
expected.

### 2. New task filed a repo the user had moved off, after a team switch (fixed)

**Reproduction** (Playwright, on `grain demo` with `payments` targeting `acme/widgets` and
`acme/gadgets`):

1. Open New task from `/teams/default/repos/acme/widgets` and type nothing.
2. Switch Target team to payments.
3. Pick `acme/gadgets`, then `acme/widgets`.
4. Switch Target team back to default.
5. Repos is now a free-text box holding `acme/gadgets`. Type a title and press Create.
6. The task is stored with `repo: "acme/gadgets"`, while the capability boxes showed
   `acme/widgets`' defaults.

**Cause:** `initialDraft` used `null` both for "not read yet" and for "no draft". A form opened
without a draft re-read `localStorage` on every render. Its own first autosave (taken at
`acme/gadgets`) therefore became its "restored draft", and so became `RepoField`'s
`defaultValue`. When default's option list lacked `acme/gadgets`, `RepoField` switched to text
mode with that value. `submit` reads `data.get("repo")` first.

**Fix:** the ref starts `undefined`, so storage is read once, at mount.

**Regression tests:**

- "files the repo picked last, not the first autosave's, after a team switch" in
  `NewTaskOverlay.repos.test.jsx` fails without the fix: `expected 'INPUT' to be 'SELECT'`.
- The e2e spec asserts the same in a real browser.

The same code is on `main`.

### 3. The team's repo page promised capabilities that team never gets (fixed)

**Reproduction:** open `/teams/payments/repos/acme/widgets` on the demo. It reads "starts with:
Self debug, GCP key, Gemini key, Release notes". A task payments files there gets Gemini key
and Release notes only (step 2's matrix).

**Fix:** `RepoPage.jsx`. In team context, `capsEffective` leaves out what the team may not hold
(`capabilityAllowedForTeam`), and the picker row shows `capabilityTeamHint` in place of
"already a deployment default".

**Regression test:** "leaves out of what a task starts with a default the team may not hold" in
`RepoPage.test.jsx` fails without the fix.

### 4. New task's note on pre-ticked boxes left out the team layer (fixed)

The note said the boxes were the deployment's defaults plus the repo's. It now also names what
the target team adds, less what it may not hold. This is a text change only.

### 5. After a hand edit, a team switch keeps the old team's own repo defaults (follow-up)

**Reproduction** (in the spec): on New task for default on acme/widgets, untick any box. Then
switch to payments. Memory, which is default's own team default, stays ticked, and payments'
Release notes is not added. The filed task gets `memory`.

This follows the documented rule that "once the picker has been touched, the ticks are theirs".
But Memory was never ticked by hand. Fixing it means tracking hand ticks and unticks separately
from the seeded set, which changes documented behaviour. It is proposed as a follow-up rather
than changed here.

### 6. Team repo config stores capability ids nothing answers to (follow-up)

**Reproduction:**

```
curl -XPUT .../api/teams/payments/repos/acme/widgets/config -d '{"defaultCapabilities":["no-such-cap"],"ciCapabilities":["no-such-cap"]}'
```

This returns 200 and stores both. The repo-level `PUT /api/repos/{repo}/capabilities` refuses
an unknown id. The existing CRUD test deliberately stores CI-style ids (`grain-ci`,
`package-publish`) as team defaults, so this changes an established contract and is left as a
follow-up.

## Fixes in this branch

- `fix(teams)`: refuse a team repo default capability the team cannot hold, and add the
  per-caller `/api/config` test.
- `fix(ui)`: read New task's stored draft once, so a team switch keeps the picked repo.
- `fix(ui)`: a team's repo page promises only the defaults that team can hold.
- `fix(ui)`: the New task note names the target team's repo defaults.
- `test(e2e)`: `ui/e2e/team_repo_default_capabilities_walkthrough.spec.js`, 6 tests. It passed
  6/6, and 12/12 with `--repeat-each=2`, locally on Chromium.

## Checks

- `go test ./pkg/ui` passes.
- `npx vitest run` passes for `src/components/NewTaskOverlay*`, `src/components/RepoPage.test.jsx`,
  `src/state.test.js` and `src/App*`.
- `npx prettier --check .` is clean.
- `make security` is green (`go vet ./...`; `tests/security` ok).
- No `.grain/baselines/` file was touched.
- **CI verdict:** the full grain CI suite passed on `def79e2bd`, the branch with every fix,
  test and this report except this line: run `95faf1d9203398c0`. Every job passed: frontend,
  go, installer, lint, package cache, postgres, terraform and ui e2e. `ui e2e` includes the new
  walkthrough spec. It also passed on `2a632414f` (run `5e4eb6797a14ec90`). None of the known
  flakes recurred.

## `matrix.mjs`

Run it from the directory holding the `work` checkout with `node matrix.mjs http://127.0.0.1:<port>`,
after creating team `payments`.

```js
import { defaultCapabilitiesFor } from "./work/ui/src/state.js";

const B = process.argv[2];
const j = async (method, path, body) => {
  const r = await fetch(B + path, {
    method,
    headers: { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status} ${text}`);
  return text ? JSON.parse(text) : null;
};
const D = [["gemini-key"], ["gemini-key", "gcp-key"]];
const R = [[], ["read-issues"], ["gcp-key"]];
const T = [[], ["memory"], ["memory", "gcp-key"]];
const W = [{}, { "gcp-key": ["payments"] }];
for (const w of W) {
  await j("PUT", "/api/settings", { capabilityTeams: w });
  for (const d of D) {
    await j("PUT", "/api/settings", { defaultCapabilities: d });
    for (const r of R) {
      await j("PUT", "/api/repos/acme/widgets/capabilities", { defaultCapabilities: r });
      for (const t of T)
        for (const team of ["default", "payments"]) {
          let saved = "saved";
          try {
            await j("PUT", `/api/teams/${team}/repos/acme/widgets/config`, { defaultCapabilities: t });
          } catch { saved = "refused"; }
          const config = await j("GET", "/api/config");
          const form = [...defaultCapabilitiesFor(config, "acme/widgets", team)].sort();
          const task = await j("POST", "/api/tasks", { title: "m", repo: "acme/widgets", team });
          const stored = [...(task.capabilities || [])].sort();
          console.log(JSON.stringify({ w, d, r, t, team, saved, form, stored,
            ok: JSON.stringify(form) === JSON.stringify(stored) }));
          await j("PUT", `/api/teams/${team}/repos/acme/widgets/config`, { defaultCapabilities: [] });
        }
    }
  }
}
```
