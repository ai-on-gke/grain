# Nightly New Features Test Plan: 2026-10-15 (afternoon)

This plan covers what merged after `docs/test-plans/2026-10-15.md`. That plan audited `candidate` at
`9788ecb1e` and `main` at `6d2329595` (2026-10-15 01:10 UTC). This one audits `candidate` at
`591f0f35f` and `main` at `4a3bfc371`. Audit timestamp: 2026-10-15 14:30 UTC.

The audit listed `git log --no-merges 9788ecb1e..origin/candidate` and `git log --no-merges
6d2329595..origin/main`. It deduplicated subjects (most changes landed twice: once on their own branch
and once as a port), dropped test-run reports and e2e-only adjustments, and compared every subject
against the other branch's last 800 commits.

**`main` is ahead of `candidate` again.** No commit is on `candidate` alone. These are on `main` and not
on `candidate`:

| Commit | Subject | § |
|---|---|---|
| `30ee2ba04` | Commit as the host's own identity on an ambient git connection | 6 |
| `c897b09c2` | Derive the agents' commit author from the git credential itself | 6 |
| `189f96c9d` | Remove the commit author fields from the UI | 6 |
| `7c80f66bf` | fix(gcpkey): give each named GCP key its own file in the sandbox | 7 |
| `a36e9ed23` | fix(gcpkey): reserve the "default" team key name and keep key descriptions | 7 |
| `5f0b254b8` | docs: document team-scoped GCP keys (gcp-key:<name>) | 7 |
| `5731bbcc9`, `4647aef34`, `be033daa4` | antigravity model-turn limits per connection; retries told from slow turns | 8 |
| `18116f68d` | feat(schedules): note a held firing on the blocking task when it waits on a person | 9 |
| `5497ea614` | fix(ui): remove the team-role chip from the top bar | 4 |
| `efa14e107` | test(runhost): adopt a run across a Deployment roll on kind, end to end | 11 |

The three §6 commits have no Conventional Commits type, and §8/§9 are `feat:`. Grain's port-types
filter does not port those, so they reach `candidate` only with the next cut. Their follow-ups
(§6, §8, §9) target `main`. Every other follow-up targets `candidate`. Where a `candidate` step depends
on a `main`-only `fix:` (§4, §7), the task checks `git branch -r --contains <sha>` first. If the port
has not landed, the task skips that step and says so in its report. No task may merge one branch into
the other.

Substantive changes:

| Area | Commits (candidate; main original where different) | § |
|------|---------|---|
| Alerts panel replaces standing banners; Admin mode chip is the only Admin indicator | `d98d9c853` (`dea9c1628`), `7f08cefc8` (`1d092a479`) | 1 |
| Team members set a session's "tasks approved"; the session pane is role-aware | `afae4a4f0` (`ad944c8d1`) | 2 |
| Task timeline in chronological order; inferred requeues; connector under the live mark | `86268eaef` (`dfb0782b6`), `91bbb7d2e` (`6ad26c320`) | 3 |
| Reader on no team; one-style lower-case breadcrumb; role chip removed (main) | `b66f6b8a1` (`fcbbb1307`), `ad4a99ed7` (`bb02427e1`), `5497ea614` (main) | 4 |
| Ambient git credential pinned to its login's host; connection check authorized | `d4263d0c9` (`b71a982cf`) | 5 |
| Commit author derived from the git credential (main only) | `30ee2ba04`, `c897b09c2`, `189f96c9d` | 6 |
| Team GCP keys: minter ownership; named key files, reserved "default", descriptions (main) | `25b040406` (`72dafa277`), `7c80f66bf`, `a36e9ed23`, `5f0b254b8` | 7 |
| Antigravity model-turn limits per connection (main only) | `5731bbcc9`, `4647aef34`, `be033daa4` | 8 |
| Held schedule firing noted on a blocker waiting on a person (main only) | `18116f68d` | 9 |
| Release: candidate gate holds on unrecognised/duplicate verdicts; `grain:sha-<short>` kept on promote | `0aec4ab64` (`fb17bfcef`), `05b00cea8` (`f1bc3e987`) | 10 |
| Run hosts: MCP server from the run host's grain; kind roll e2e (main) | `b698e2014` (`c09cf486d`), `efa14e107` | 11 (no task) |
| Fixes found by the 10-15 UI walkthroughs | see §12 | 12 (no task) |
| Status of this morning's follow-ups | — | 13 (no task) |

Not given a section:
- `2eeef99ad` / `e2ea519c6` (vendored granule resync to upstream `4411ce4`; CI's granule jobs prove it).
- Ports of commits this morning's plan already covered: `be7de4dff` (= `da2743e65`, morning §6),
  `84dbe82a1` (= `acd835d11`, morning §10), `f22e6a866` (= `9b0cf6b07`, morning §3). The morning tasks
  check for these ports, and now find them.
- E2e/test-only commits: `d1887961d`/`84b6d1523`, `5ecca1596`/`ebd3a4fd1`, `b2c3829c2`, `60a615000`.
- Test-run reports and `925b8a8ff`/`db49d816d` (the hand-run upgrade report; §11 uses it).

Every follow-up below runs against a real `grain demo` (SQLite), a real daemon, the real script or the
real package under test. It stubs only external boundaries: GitHub and other forges (`-github-sim`, or
an `httptest` stand-in), model providers, the `gh` CLI's token, the GCP IAM credentials API and
container registries other than a local `registry:2`.

---

## 1. Alerts Panel Replaces Standing Banners

**Commits:** `d98d9c853`, `7f08cefc8` (main: `dea9c1628`, `1d092a479`, e2e `84b6d1523`)

### What changed
- Every standing banner except `DisconnectedBanner` is now a row in an alerts panel, opened from a bell
  in the top bar and in the phone bar. `BannerStrip` and the banner components are deleted.
- The bell shows the number of active alerts, coloured by the worst severity, and goes quiet at zero.
  Its `aria-label` carries the count. A polite live region (`aria-live`, no longer `role="status"` after
  `7f08cefc8`) announces new critical alerts.
- Severity: critical for reconciler down and state sync failing; warning for agent/usage-limit,
  deployment and team pauses, and loopback trust; info for setup steps left.
- Each row has a severity icon, title, sentence, scope (deployment, team, or team plus connection),
  since/until, impact where known, and the banner's old actions: Resume now (still confirming before it
  cancels a scheduled upgrade), Resume team, Finish setup, Open connection, Open System.
- Rows sort by severity, then recency, and group by team once there are five or more.
- Each usage-limit pause entry becomes its own alert. A member sees team pauses for their own teams and
  the team they are viewing; a system admin sees every team's, labelled by team.
- `AdminModeBanner` is gone. The "Admin mode" chip by the logo is the only indicator, and is a button
  that turns Admin mode off.
- The team home page shows an in-page `TeamPauseNotice`; the Inbox state-sync row shares the alert's
  title and sentence.

### What CI proves
- `alerts.test.js` (collectAlerts, worstSeverity, sortAlerts, the sentence helpers) and
  `AlertsButton.test.jsx`.
- e2e specs that now read the setup and pause alerts from the panel (`d1887961d`), including
  `responsive-navigation-walkthrough.spec.js` and `team-git-credential-connections-walkthrough.spec.js`.

### What is NOT proved
- Every alert source from a real server in one browser session: `-agent-paused`, `-unconfigured`
  (setup), `POST /api/teams/{id}/pipeline-pause`, a deployment pipeline pause, state sync failing and
  the loopback trust warning. Each must appear exactly once, with the right severity and scope.
- The bell's count and colour as alerts are added and lifted, and the quiet state at zero.
- Grouping by team at five or more alerts, and the order within groups.
- Visibility by role: a member of `alpha` viewing `beta` sees `beta`'s pause; a member of `alpha` only
  never sees `beta`'s; a system admin sees all, labelled.
- Each action really lifts the pause through its endpoint, and a refused lift (member on a deployment
  pause) shows the server's reason rather than silently failing. Resume now on a pause holding a
  scheduled upgrade confirms first.
- The live region announces a newly arriving critical alert once, and does not announce on every poll.
  No page has two `status` roles.
- The phone bar's bell at 390x844, keyboard access (open, Tab through rows, Escape returns focus to the
  bell), and axe on the open panel.
- `DisconnectedBanner` still shows when the daemon stops, and the Admin mode chip turns Admin mode off.

### Walkthrough procedure
1. Build the UI and start `grain demo -addr 127.0.0.1:0 -open=false -agent-paused -reconcile 2s`.
   Create teams `alpha` and `beta` and users: a system admin; a member of `alpha` who views `beta`; a
   member of `alpha` only.
2. As the admin with no alerts but the agent pause: the bell shows 1 in warning colour. Open the panel;
   check title, scope, until, and Resume now. Lift it and see the bell go quiet without a reload.
3. Pause `alpha` and `beta` through `POST /api/teams/{id}/pipeline-pause`, a deployment pipeline pause,
   and (separately) start a second demo with `-unconfigured` for the setup alert. Record severity, scope
   and actions per row. Push the count to five or more and check team grouping and sort order.
4. Sign in as each member (`test.use({ extraHTTPHeaders: {} })`) and record which alerts each sees.
   Try every action they are offered; a refusal must name its reason.
5. Make state sync fail (point the state repo at an unreachable remote) and record the critical row and
   one live-region announcement; leave it failing over three polls and check it is not re-announced.
6. At 390x844 open the bell from the phone bar. Run axe on the open panel at both sizes. Keyboard-walk
   the panel and close it with Escape.
7. Stop the daemon: `DisconnectedBanner` appears. Restart it. Turn Admin mode off from the chip.

---

## 2. Team Members Set a Session's "Tasks Approved"; Role-Aware Session Pane

**Commits:** `afae4a4f0` (main: `ad944c8d1`). Fixes #3797.

### What changed
- `tasksApproved` on a standing session needs only the member or admin role on the session's team. It
  used to be system-admin only. The session's capabilities, network access and repository grants stay
  with system admins.
- A proposal asking for a capability or repository the session lacks is still filed for a person.
- `ConsoleSessionPage` gives `TaskChat` the team's write refusal. A viewer or proposer sees the
  conversation, the server's reason they cannot send, and a link to the task behind it, but not the
  composer, the capabilities editor, "start a fresh chat" or "Delete session".

### What CI proves
- `TestConsoleSessionWritesAreAuthorized`: a member's `tasksApproved` save succeeds; viewers and
  proposers are refused.
- `ConsoleSessionPage.test.jsx` cases for the read-only pane.

### What is NOT proved
- A member flipping the switch in a real browser, read back from `GET /api/console-sessions`, and then a
  proposal made by that session being filed approved (or held, when it asks for more than the session
  holds).
- That a member's `PUT /api/console-sessions/{id}` carrying `tasksApproved` together with
  `capabilities` or `networkAccess` is refused as a whole, not half-applied.
- A member of team A setting `tasksApproved` on team B's session (must be refused).
- The read-only pane for viewer and proposer at desktop and phone sizes, with axe.
- `tests/security/routes_test.go` still matches the handler's real behaviour.

### Walkthrough procedure
1. Start `grain demo` with teams `alpha` and `beta`, a member, a viewer and a proposer of `alpha`, and
   a member of `beta` only. Create a session in `alpha` as the admin.
2. As the `alpha` member, turn on "tasks approved" in the session pane. Reload; read it back from the
   API.
3. Make the session propose two tasks (drive its MCP `propose_task` path through the demo, or call the
   same store path the session's tool uses): one needing nothing new, one asking for a capability the
   session lacks. The first is filed approved; the second is filed proposed. Record both states.
4. As the `alpha` member, send `PUT` with `tasksApproved` plus `capabilities`, and with `networkAccess`.
   Both are refused and nothing changes. As the `beta` member, `PUT` on `alpha`'s session is refused.
5. As viewer and as proposer, open the session: conversation visible, refusal reason shown, task link
   present, no write control in the DOM. Repeat at 390x844; run axe.

---

## 3. Task Timeline in Chronological Order

**Commits:** `86268eaef` (main: `dfb0782b6`), `91bbb7d2e` (main: `6ad26c320`)

### What changed
- "Closed" is no longer pinned last; timestamps decide. A PR merged/closed event trailing a close by up
  to an hour, with nothing in between, is ordered just ahead of it.
- An attempt's node stands at its `finishedAt` (a running attempt sorts as now); its start time stays in
  the meta line.
- Undated events (missing, NaN, Go's zero time) sit beside their nearest dated neighbour of the same kind
  or go last marked "time unknown". `Store.AddComment` stamps a comment with no `CreatedAt`.
- Same-instant ties use a causal rank (proposed < queued < running < comment < … < closed).
- `model.Transitions` places a requeue at its real moment (`RetryRequestedAt`, `UndeferredAt`) or flags
  it `inferred`; the UI shows "~ <time>" with a tooltip.
- The timeline connector is drawn under the live agent mark.

### What CI proves
- Vitests for the timeline ordering helpers and `pkg/model` tests for `Transitions` and `AddComment`.

### What is NOT proved
- A real task's lifecycle in the demo with real stored timestamps: run, comment during the run, fail,
  retry, close, reopen, re-run, PR open and merge from `-github-sim`.
- The inferred requeue's "~" display and tooltip on a real task, and a deferred task's undeferral not
  appearing twice.
- Postgres precision is argued in the commit, not tested; record whether the walkthrough can run the
  same lifecycle against `GRAIN_TEST_POSTGRES` (skip if unavailable and say so).
- The connector under the live mark while an attempt is running, at desktop and phone widths.

### Walkthrough procedure
1. Start `grain demo -github-sim -fake-ci -reconcile 2s`. File a task and let it run.
2. While it runs, add two comments. Let the attempt finish with a failure; retry it (records
   `RetryRequestedAt`); let the second attempt open a PR and merge it through the simulator; close the
   task; reopen it and close it again.
3. Read `GET /api/tasks/{id}` transitions and the rendered timeline. Every node must be in timestamp
   order, with the comments before the attempt that was running when they were posted, and the PR merge
   just ahead of a close it trails.
4. Defer a task, undefer it, and check one undeferral entry. Force an inferred requeue (a requeue with
   neither a retry nor an undeferral record) and check "~" and its tooltip.
5. Screenshot the running attempt's connector at 1280x800 and 390x844.

---

## 4. Reader on No Team; Breadcrumb; Role Chip

**Commits:** `b66f6b8a1` (main: `fcbbb1307`), `ad4a99ed7` (main: `bb02427e1`), `5497ea614` (main only)

### What changed
- A signed-in reader with no role on any team gets `NoTeamPage` on every team-scoped page. It says who
  can add them (a team admin, or the system admins by name) and offers "Start a new team". The address
  names no team; the top bar shows "No team" with no switcher, no team home or settings buttons. The rail
  and phone New task buttons are not drawn, and `/new` is closed. Sandboxes and Caches still open.
- `/api/config` sends `teams: []` rather than omitting it.
- The breadcrumb's three crumbs share one type style; page labels are lower case and match the
  accessible name.
- On `main` only, the role chip beside the switcher is gone (`5497ea614`); the role stays on the team
  home page and in Members.

### What CI proves
- `App.test.jsx` no-team case; `TopBar.test.jsx` computed-style and lower-case tests.

### What is NOT proved
- A real user whose only team was deleted, signing in to the demo.
- "Start a new team" from `NoTeamPage` creating a team and moving the reader into it without a reload.
- Old deep links (`/t/default/tasks`, `/t/<deleted>/task/N`, `/new`) for that reader.
- That no request the no-team UI makes answers 403 (watch the network log).
- Breadcrumb styling in a real browser at desktop and phone width, and the lower-case labels as read by
  the accessibility tree.

### Walkthrough procedure
1. Start `grain demo`. Create user `u` with a personal team, then delete that team (and any other role
   `u` holds). Sign in as `u` (`test.use({ extraHTTPHeaders: {} })`).
2. Visit `/`, `/t/default/tasks`, `/t/<deleted>/task/1`, `/new`, `/sandboxes`, `/caches`. Record each
   page; collect every non-2xx response.
3. Use "Start a new team". The shell moves into it; New task appears; the crumb reads the team.
4. As an admin, check the computed font of the three crumbs at 1280x800 and 390x844, and the
   accessibility tree's names for the page crumb.
5. If `5497ea614` is on `candidate` (`git branch -r --contains 5497ea614`), check the chip is gone and
   the role shows on the team home page; otherwise skip and say so.

---

## 5. Ambient Git Credential Pinned to Its Login's Host; Connection Check Authorized

**Commits:** `d4263d0c9` (main: `b71a982cf`)

### What changed
- Re-adding an existing ambient credential through `POST /api/teams/{id}/git-connections` needs a system
  admin, and a host other than the ambient file's is refused with 400.
- `CredentialSet.SetHost` refuses to move an ambient credential off its file's host. `load` marks an
  ambient credential Unusable when host record and file disagree. `AmbientIdentityOf` builds its
  transport from the file's host.
- `POST /api/teams/{id}/git-connections/check` tests a saved credential only if the team owns or uses
  it (otherwise a system admin must name it), and only on its recorded host.

### What CI proves
- `pkg/gitproxy` and `pkg/ui` regression tests for the re-add (403 for a team admin, 400 for a system
  admin) and the check against another host.

### What is NOT proved
- The exploit as described, end to end against a real daemon: a team admin re-adding the ambient
  connection with `evil.example.com` and an HTTP listener on that host seeing no request carrying the
  token, on save or on reading the Git connections pane.
- A pre-existing store row whose host already disagrees with the file (a deployment hit before the
  fix): the connection reads as Unusable in the UI, and nothing runs `gh`.
- The check endpoint with another team's credential name, the deployment's own, and a host override.
- The UI's handling of the 400/403: an inline error, not a spinner.

### Walkthrough procedure
1. Start a daemon (or `grain demo -git-proxy`) with a fake `gh` on `PATH` that prints a known token, and
   a local HTTP listener standing in for `evil.example.com` (map it in `/etc/hosts` or via the proxy's
   host override) that logs every request's `Authorization` header.
2. As system admin, add an ambient connection to team `alpha`. As `alpha`'s team admin, re-add it naming
   the listener's host (403), and as system admin (400). Read the Git connections pane five times; the
   listener sees nothing.
3. Stop the daemon, edit the store row's host to the listener's host, restart. The pane shows the
   connection Unusable and the fake `gh` log shows no call.
4. As `alpha`'s admin, call the check endpoint naming `beta`'s credential, the deployment's credential,
   and `alpha`'s own with another host. Record each status.
5. Do steps 2 and 4 again in the browser and screenshot the error each shows.

---

## 6. Commit Author Derived from the Git Credential (main only)

**Commits:** `30ee2ba04`, `c897b09c2`, `189f96c9d` (all `main` only)

### What changed
- A run's commit identity is derived from its git credential for every credential type: a PAT via
  `GET /user` (display name, `<id>+<login>@users.noreply.<host>`); a GitHub App via `GET /app` and
  `GET /users/<slug>[bot]` (`<slug>[bot]`); an SSH key from its paired API credential; an ambient
  connection from the host account's `git config` or else its login.
- The derived identity takes precedence over the team's stored `AgentGitName`/`AgentGitEmail`, which
  only fills a half the credential cannot say (plain git hosts, unpaired SSH keys).
- The New team dialog, Teams panel and Team settings > Git identity no longer offer the author fields.
  The pane lists who each connection commits as (`commitName`/`commitEmail`, pending while looked up, and
  grain's default plus a reason when the credential names nobody).

### What CI proves
- `pkg/daemon/git_identity_test.go` and orchestrator tests with stubbed lookups; vitests and e2e
  walkthroughs updated for the removed fields.

### What is NOT proved
- An agent's real commit in a real sandbox carrying the derived author, for each credential type,
  against a GitHub stand-in that answers `/user`, `/app` and `/users/<slug>[bot]`.
- A rebuilt sandbox mid-run committing as the same identity.
- Teams with stored author values from before the change: which half wins, and whether the stored
  value is still visible or editable anywhere (it should not be offered, and must not be lost from the
  grain/config export without a note).
- Lookup failure (`/user` 401 or 500): the run still commits, as grain's default, and the pane gives the
  reason. A slow lookup does not hold up a dispatch indefinitely.
- Caching per credential and token: rotating a PAT to another account changes the author on the next
  run.

### Walkthrough procedure
Target `main`. Check `git branch -r --contains c897b09c2` first.
1. Start `grain demo -github-sim -git-proxy -reconcile 2s` (or a daemon with an `httptest` GitHub
   stand-in) with four teams, each on one credential type: PAT, App, SSH key paired with a PAT, ambient
   (fake `gh` and a host `git config user.email`). Give one team a stored author from before (seed the
   row directly).
2. Dispatch one task per team that commits and pushes. Read the author of each pushed commit from the
   simulator. Each matches the rule above.
3. Rotate the PAT to another account; the next run's author follows. Make `/user` answer 500; the run
   commits as grain's default and the Git identity pane says why.
4. Open Team settings > Git identity in the browser for each team: the pending state, then the derived
   identity. Confirm New team and the Teams panel have no author fields. Run axe on the pane.
5. Export grain/config and record what happens to the stored author fields.

---

## 7. Team GCP Keys: Minter Ownership, Named Key Files, Reserved Name, Descriptions

**Commits:** `25b040406` (main: `72dafa277`); `main` only: `7c80f66bf`, `a36e9ed23`, `5f0b254b8`

### What changed
- Saving a key through `POST/PUT /api/teams/{id}/gcp-keys` needs a system admin unless the key's
  effective minter secret is restricted, lists the key's team, and lists only teams the acting user
  administers. Checked on every save. Copying credentials between teams applies the same check before
  anything is copied.
- A new key naming no minter gets `gcp-key-minter-<team>-<name>`. The team config repo's
  `gcp_keys.yaml` accepts a key only if it names that minter or is unchanged from the database row.
- `main` only: a named key lands at `/home/debian/.gcp-service-account-<name>.json` (unnamed and
  "default" keep the old path); "default" is rejected as a team key name; `SetTeamGCPKeyRequest` keeps
  the description when omitted; `grain team gcp-key set --description`.

### What CI proves
- `pkg/ui/team_gcp_key_test.go`, `TestCopyCredentialsReportsGCPKeyListFailure`, `pkg/staterepo` import
  tests; on `main`, `gcpkey_test.go` and `tests/e2e/team_scoped_capabilities_e2e_test.go` with two keys.

### What is NOT proved
- The escalation itself against a running daemon: a team admin pointing a key at the deployment's
  `gcp-key-minter` (refused), at a minter restricted to their team plus a team they do not administer
  (refused), and at their own team-restricted minter (allowed).
- A team admin repointing an existing system-admin-created key's project or service account (refused).
- Copy credentials from a team holding a shared-minter key to another: refused with nothing copied.
- The `gcp_keys.yaml` import path with a hostile key in a team config repo.
- `main`-only parts on `candidate` once ported: a task granted `gcp-key` and `gcp-key:deploy` sees two
  files and two prompt sections; "default" refused from the CLI; description kept across an API save.

### Walkthrough procedure
1. Start `grain demo -secrets-store` (or a daemon) with teams `alpha`, `beta`; `alpha`'s team admin is
   not an admin of `beta`. Create minter secrets: the deployment's `gcp-key-minter` (unrestricted); one
   restricted to `alpha`; one restricted to `alpha` and `beta`.
2. As `alpha`'s admin, save keys naming each minter. Record status and whether a row was written. Save a
   key with no minter and read back its minter name.
3. As system admin, create a key on `alpha` with the shared minter; as `alpha`'s admin, change its
   project. Refused, row unchanged.
4. Copy credentials from `alpha` (holding the shared-minter key) to `beta` as `alpha`'s admin. Refused;
   `beta` unchanged.
5. Push a `gcp_keys.yaml` naming the deployment minter to `alpha`'s config repo and sync. The key is
   skipped and logged.
6. For each of `7c80f66bf` and `a36e9ed23`: if on `candidate`, dispatch a task granted `gcp-key` and
   `gcp-key:deploy` with the IAM credentials API stubbed, and check two distinct files and prompt
   sections in the sandbox; try `grain team gcp-key set` named "default"; save twice via the API, the
   second without a description. If not ported, skip and say so.

---

## 8. Antigravity Model-Turn Limits per Connection (main only)

**Commits:** `be033daa4`, `5731bbcc9`, `4647aef34` (all `main` only)

### What changed
- The single 100 s antigravity turn deadline is now two tiers. From the retry cutoff (default 100 s)
  grain re-reads agy's redacted `--log-file` tail every few seconds; a turn whose log gained a retry
  record (HTTP 429/5xx, `RESOURCE_EXHAUSTED`, capacity markers) is cancelled quoting it. A turn with
  none runs to the slow-turn ceiling (default 15 m). An unreadable or unrecognised log falls back to the
  single cutoff, logged once per run. Later turns in an exec take a baseline.
- An antigravity connection carries `modelTurnTimeout` and `modelRetryCutoff` (Go durations, 30 s–60 m,
  cutoff ≤ ceiling). The connection editor shows them as advanced fields on antigravity connections only,
  cleared when the agent type changes. They round-trip through the grain/config export.

### What CI proves
- `pkg/agent/antigravity` deadline tests with a live retry stub; `pkg/model`, `pkg/ui`, `pkg/staterepo`
  and `pkg/daemon` tests for validation, export and wiring; `ModelBackendPage.test.jsx`.

### What is NOT proved
- A real `grain` run of the antigravity framework against a fake `agy` binary that writes a real log
  file: a retrying turn cancelled at the cutoff with the record quoted, a slow clean turn running past
  it to completion, and a hung clean turn stopped at the ceiling. Each message names tier, value,
  connection and setting.
- The baseline across turns in one exec, and the fallback with an unreadable log.
- The editor in a browser: fields on antigravity only, cleared on type change, server bounds surfaced
  inline (29 s, 61 m, cutoff > ceiling, garbage).
- Export and re-import through a real state repo.

### Walkthrough procedure
Target `main`. Check `git branch -r --contains be033daa4` first.
1. Write a fake `agy` (shell or Go) that streams turns and appends retry records to its `--log-file`
   on demand. Configure an antigravity connection with `modelRetryCutoff: 30s`, `modelTurnTimeout: 90s`.
2. Run a task through the daemon (or the framework's test harness with the real process launch) for:
   retrying turn; slow clean turn of 60 s; hung clean turn; second turn after a recovered retry; log path
   unreadable. Record each outcome and message.
3. In `grain demo`, edit the connection in the browser: the fields appear only for antigravity, clear on
   switching type, and each bad value shows the server's reason.
4. Export to a local state repo, wipe the row, re-import; values come back.

---

## 9. Held Schedule Firing Noted on a Blocker Waiting on a Person (main only)

**Commit:** `18116f68d` (`main` only)

### What changed
- When a schedule's firing is held by an open task (overlap skip, or a scheduled workflow's open review)
  and that task waits on a Submit, a reply or an acknowledgement, the schedule writes one comment on it
  saying the firing is held there and fires once the task closes.
- The reconciler passes every recorded skip to `Store.NoteHeldScheduleOnBlocker` each cycle, so a
  blocker that moves into a human-wait state later is still noted. `scheduled_firing_skip.noted_task_id`
  keeps it to one comment per occurrence and blocker; the claim is released if the write fails.

### What CI proves
- `pkg/model` tests for the note and its once-only claim; `docs/schedules.md`.

### What is NOT proved
- The real reconciler in `grain demo -reconcile 2s`, over many cycles: one comment, not one per poll.
- Each human-wait state (`awaiting_submit`, `awaiting_reply`, an acknowledgement) and a blocker that
  enters one only after several skips.
- The scheduled-workflow review path (`-scheduled-workflows`).
- The comment as seen in the task pane and the Inbox, and the firing going ahead once the blocker
  closes, with no further comment.
- The migration on an existing SQLite store from before the column.

### Walkthrough procedure
Target `main`. Check `git branch -r --contains 18116f68d` first.
1. Start `grain demo -reconcile 2s -scheduled-workflows` on a store copied from a build before
   `18116f68d` (build the previous commit, create a schedule, stop it, then start the new build).
2. Create an every-minute schedule with overlap skip. Let it file a task; move that task to
   `awaiting_submit`. Let five firings be skipped. Exactly one comment on the blocker.
3. Repeat with `awaiting_reply` and with an acknowledgement, and with a blocker that sits `running` for
   three skips and then moves to `awaiting_reply`.
4. Do the same for a scheduled workflow whose review task is waiting on a person.
5. Close the blocker; the next firing goes ahead; no new comment. Check the task pane and Inbox show the
   comment.

---

## 10. Release: Candidate Gate Verdict Rules; `grain:sha-<short>` Kept on Promote

**Commits:** `0aec4ab64` (main: `fb17bfcef`), `05b00cea8` (main: `f1bc3e987`)

### What changed
- `scripts/candidate-gate.sh`: an unrecognised verdict word holds promotion for every gated workflow,
  advisory suites included. A workflow listed more than once is judged on its first line when every line
  agrees, and holds when they disagree.
- `release-images.sh promote` also points `grain:sha-<short>` at the stamped `vX.Y.Z` on every tick,
  so `cmd/ghcr-prune` keeps it for as long as the release.

### What CI proves
- `tests/deploy/candidate_gate_test.go` and `promote_images_test.go`/`release_images_test.go` with a
  stubbed `docker`.

### What is NOT proved
- `release-images.sh promote` against a real registry (`registry:2` on localhost) with real
  `docker buildx imagetools`: `sha-<short>` and `vX.Y.Z` resolve to the same digest after a tick, a
  second tick is a no-op, and a pruned `sha-<short>` is put back.
- `cmd/ghcr-prune`'s keep rule against that registry layout (dry run against a stub of the GHCR
  versions API built from the local registry's tags).
- The gate script run by the real cut path (`scripts/release-candidate-cut.sh` in its dry-run or test
  mode) with a verdicts file carrying a typo and a disagreeing duplicate: the cut goes ahead, `latest`
  stays.

### Walkthrough procedure
1. Start `docker run -d -p 5000:5000 registry:2`. Stage the grain CA as `.grain-ca.crt` if a build is
   needed. Push two tiny images as `localhost:5000/grain:sha-abc1234` and a candidate tag.
2. Run `release-images.sh promote` with the registry overridden to `localhost:5000` (record how). Compare
   digests with `docker buildx imagetools inspect`. Run it again. Delete `sha-abc1234`; run again.
3. Run `cmd/ghcr-prune` dry-run against a versions-API stub fed from step 2. `sha-abc1234` is kept.
4. Run the cut's gate step with verdict files: `nightly <sha> pass` + `live-frameworks <sha> regresion`;
   `nightly <sha> pass` + `nightly - missing`; two agreeing lines. Record promote/hold for each and that
   `latest` does not move on a hold.

---

## 11. Run Hosts: MCP Server Path; Kubernetes Roll Test (no task)

`b698e2014` (main: `c09cf486d`) names the run host's own `/usr/local/bin/grain` in a claude run's
`--mcp-config` under `-run-host-image`. It was found and verified by the hand run in
`docs/test-runs/2026-10-15-upgrade-adopts-live-claude-run.md`, and `pkg/daemon/runhost_test.go` holds
it. This morning's §8 walkthrough (run-host adoption on one machine) exercises the same path.

`efa14e107` (main) adds `TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest`, gated on
`GRAIN_RUNHOST_KIND`. It does not pass yet, and `docs/test-runs/2026-10-15-k8s-daemon-roll-adoption.md`
records why: a claude run cannot reach a Kubernetes sandbox (no `agent.RunConfig` target, no
`grain mcpserver` pod flag), K8s sandbox git credentials depend on the image's WORKDIR, that error was
misread as a provider refusal, and the 8 GiB default shape does not fit a kind node. These are open
design gaps owned by task 2700, not new behaviour to walk through. No task is filed here; this
morning's §9 (Kubernetes) task should read that report before starting.

---

## 12. Fixes from the 10-15 UI Walkthroughs (no task)

Each has its own regression test (vitest and/or e2e spec) and a report in `docs/test-runs/2026-10-15-*`:

- Deployment operations: `231a629aa` (Caches, CI runs, Top, Config repo, All Teams say a failed read;
  `deployment-operations-failed-reads.spec.js`).
- Responsive navigation and sessions: `2b21b1046` (no "deleted" note before the session list loads),
  `c4f84e21c` (every pane named), `a1060f260` (`aria-current` on the rail).
- Recurring/composed: `5e711ebbc` (search by cadence, branch and mode), `6d7431015` (template-backed
  schedule's capabilities credited to the template).
- Teams: `54943a4a9` (schedule preview composed as its own team's).
- Repository, release and memory: `894216ba0` (focus, hash tabs, long release names).
- Task intake: `e4d3f0c47` (Title focused when New task opens), `e4108c509` (focus returns to the row's
  link).

No separate task is filed. The standing `ui-walkthrough-*` templates pass over these screens again, and
§1 and §4 above revisit the top bar and shell.

---

## 13. Status of This Morning's Follow-Ups (no task)

At this audit none of the twelve follow-ups filed by `docs/test-plans/2026-10-15.md` has a report on
either branch (`docs/test-runs/2026-10-15-*` holds only the standing UI walkthroughs, the upgrade hand
run and the k8s roll record). They may still be queued or running. The next plan should list their
reports, as the morning plan's §14 did for 10-13.

---

## Summary of Follow-Up Tasks Filed

Each task depends on this one (4016). §6, §8 and §9 target `main`; the rest target `candidate`. Each is
meant to merge unattended once its CI passes; whoever approves it decides that.

1. `Walkthrough: alerts panel in place of standing banners (docs/test-plans/2026-10-15-afternoon.md §1)`
2. `Walkthrough: member-set session approvals and the read-only session pane (docs/test-plans/2026-10-15-afternoon.md §2)`
3. `Walkthrough: task timeline chronological order and inferred requeues (docs/test-plans/2026-10-15-afternoon.md §3)`
4. `Walkthrough: reader on no team and the one-style breadcrumb (docs/test-plans/2026-10-15-afternoon.md §4)`
5. `Walkthrough: ambient git credential host pinning and connection check authorization (docs/test-plans/2026-10-15-afternoon.md §5)`
6. `Walkthrough: commit author derived from the git credential, on main (docs/test-plans/2026-10-15-afternoon.md §6)`
7. `Walkthrough: team GCP key minter ownership and named key files (docs/test-plans/2026-10-15-afternoon.md §7)`
8. `Walkthrough: antigravity per-connection model-turn limits, on main (docs/test-plans/2026-10-15-afternoon.md §8)`
9. `Walkthrough: held schedule firing noted on a blocker, on main (docs/test-plans/2026-10-15-afternoon.md §9)`
10. `Walkthrough: candidate gate verdict rules and sha tag kept on promote (docs/test-plans/2026-10-15-afternoon.md §10)`

---

## Standing Environment Notes for Filed Tasks

- **Authoritative gate:** grain CI (`.grain/workflows`) via `wait_for_checks`. GitHub checks are
  informational only.
- **Base check:** run `git branch -r --contains <sha>` before testing. Do not merge `main` into
  `candidate` or the reverse. Steps that need a `main`-only commit are skipped and reported on a
  `candidate` task if the port has not landed.
- **Browser tests:**
  - Run against a real `go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`. Add `-github-sim`,
    `-git-proxy`, `-fake-ci`, `-agent-paused`, `-unconfigured`, `-secrets-store`, `-reconcile <interval>`
    or `-scheduled-workflows` where needed.
  - Drive it with Playwright against the build from `cd ui && npm ci && npm run build`.
  - Don't mock grain's own APIs or React. Stub only external boundaries and list every stub in the
    report.
  - The suite sends `X-Grain-Admin-Mode: on` by default. Non-admin tests override it with
    `test.use({ extraHTTPHeaders: {} })`.
- **Readiness:** poll `/api/sandboxes`. There is no `/api/health`.
- **Docker builds:** stage `/usr/local/share/ca-certificates/grain-ca.crt` as `.grain-ca.crt` in the
  repo root, and remove it afterwards.
- **Edits under `.grain/`** need a human Submit. Avoid them in walkthrough tasks unless the fix needs
  one.
- **Route expectations:** never lower an expectation in `tests/security/routes_test.go` to fit the code.
  Fix the handler.
- **Reports:** write `docs/test-runs/2026-10-15-<feature>.md` (or the date the run happens) with the
  environment, steps, findings, a deterministic reproduction for each bug, the fixes made with their
  regression tests, and the CI verdict.
