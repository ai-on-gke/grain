# Daily Exploratory UI Testing (evening run)

**Date:** 2026-10-06
**Branch:** `grain/task-2353`
**Commit tested:** `fc7ba6f99` (`Merge pull request #3111 from bwsalmon/grain/task-2238`), the tip of `candidate` when the run started. That is 39 non-merge commits past the morning run's `bc6581f44` (`2026-10-06-daily-exploratory-ui.md`).
**Environment:** Debian GNU/Linux 12, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 (from `ui/node_modules`), Chromium headless shell and WebKit 26.5 (`webkit-2336`).
**Server under test:** this tree's own `grain demo -observability -merge-queue-states -sensitive-hold -addr 127.0.0.1:8420 -open=false`. It was built with `go build ./cmd/grain` after `npm ci && npm run build`, over the throwaway embedded SQLite store.
**Model and tooling:** no model was called. Every step was a Playwright script or a curl call. The scripts were kept out of the tree.

## Focus

The morning run walked the deployment as `operator`, a deployment admin. This run walked it mostly as the people a deployment admin is not. It also covered the features that landed on `candidate` since then:

- the new **proposer** team role (ea6453fd6);
- **desktop notifications** for new inbox items (017527a85);
- the team picker, agent connections, the release-name check and the suite count fixes that rode along.

### Personas

- `operator`: deployment admin.
- `bob`: member of `default`.
- `pam`: **proposer** on `default`. Added through Team settings › Members, whose role picker offers admin, member, proposer and viewer, each with its one-line hint.
- `mia`: **viewer** on `default`. Added the same way.
- `zed`: registered, on no team.
- `carol`: admin of `payments`, not a deployment admin.

### How it was driven

- **Sign-in:** the same as the morning run: `POST /api/auth/register` once, then `POST /api/auth/login` per context, and `grain.username` in `localStorage`.
- **What every page recorded:**
  - console errors and warnings;
  - page errors;
  - every 4xx/5xx response;
  - failed requests other than `ERR_ABORTED`;
  - every mutating request with its body;
  - confirm dialogs, which were accepted.
- **Layout checks:** document overflow, with the offending elements that are not inside a scroll container; a near-empty body; and error-boundary text.

### Stubs

- Grain's own APIs and React were not mocked.
- The notification checks replaced the **browser's** `window.Notification` with a recorder (`permission = "granted"`) through an init script. They also overrode `document.visibilityState` and `document.hasFocus`.
- No other stubs were used.

## Coverage

### Route crawl as non-admins

- **Routes:** 61 in all:
  - every rail view;
  - seeded tasks 1, 12, 13 and 16 and a proposer's own task;
  - a schedule, a template and a suite;
  - all four boards;
  - `/teams/default`, `/teams/payments` and all 8 Team-settings tabs;
  - the 16 non-power Admin tabs;
  - the repo page and a release page;
  - `/tasks/99999`, `/ci/999`, `?state=%%` and `?f=state:zz:qq`.
- **Engine, viewport and user:**
  - Chromium as `pam` at 1440x900 and 375x812;
  - WebKit as `mia` at 375x812;
  - Chromium as `zed` at 768x1024;
  - Chromium as `bob` at 1920x1080.
- **Results:**
  - No page errors, document overflow, blank screens or error-boundary text.
  - `zed` gets 404 for other teams' tasks. The Team-settings tabs say "You are not a member of team default, so its settings are not yours to see."
  - **Every** load by a non-admin requests `GET /api/state-repo` → 403, and logs a console error and a warning. This is **Bug 5**.
  - Team settings › Executors raises a 403 toast for every non-deployment-admin, and shows a false empty state. This is **Bug 4**.

### Proposer role (new)

- **New task as `pam`:**
  - The form ends with "You are a proposer on this team: what you file is a proposal, and waits for a member or admin to approve it before it runs."
  - Its "approved" box is absent.
  - Filing creates task 17 in `proposed`.
- **API, as `pam` compared with `mia` and `bob`:**
  - `POST /api/tasks` with `approved: true` → 201 and `proposed`.
  - From a template → `proposed`.
  - `interactive: true` → 403 "cannot start an interactive task".
  - Into `payments` → 403.
- **Own unapproved task:**
  - PATCH `title` / `description` → 200.
  - PATCH `base`, `team` or `autoMerge` → 403 "may edit only the title and body of your own proposals".
  - Fields the request type does not have (`capabilities`, `priority`) are ignored, and the task is unchanged. The check is a `reflect.DeepEqual` against the zero request with title and body cleared, so new fields are refused by default.
- **Refused actions:**
  - Approve, run-now, close, defer, move, messages, secret and reorder → 403 with the proposer sentence.
  - Another person's task, PATCH or comment → 403.
- **Standing work:** schedules (run and edit), templates, boards and console sessions → 403 "can read team … but cannot …". `mia` gets the same answers. `bob` gets 2xx.
- **Task pane in the UI:**
  - Own proposal: Edit and Comment only.
  - The Edit form saves `{"title","description"}` only.
  - Own task once approved or closed: Prompt only.
  - Another person's task: no write buttons.
  - Select-all shows the read-only sentence instead of bulk actions. Its wording says "need the member or admin role", which slightly undersells a proposer. Not filed.
- **Inbox:** `pam`'s and `mia`'s inboxes leave out proposals they cannot approve. `operator` approved a proposal from the inbox pane (`POST /api/tasks/31/approve`).

### Repo and CI writes by non-writers

These were found while checking that the proposer role adds nothing it should not:

- **Releases and branches:**
  - `POST /api/repos/acme/widgets/releases` and `POST /api/repos/acme/widgets/branches` → **201 for `mia` (viewer), `pam` (proposer) and `zed` (no team)**.
  - `CutCandidate`, `PromoteCandidate` and `RequestReleaseMerge` have no caller check either.
  - The sibling `release-runs` refuses them all with 403 "does not have permission to modify repository". This is **Bug 1**.
- **Task CI runs:**
  - `POST /api/ci/runs {"taskId":"1"}` as `zed` → **202**. That starts CI on another team's task, and the response carries its branch and team. This is **Bug 2**.
  - The repo-only form of the same call is open on purpose, with release secrets withheld by `withholdReleaseSecrets`.
- **Settings writes:** `PUT /api/teams/default` and `POST /api/executors/tokens` are 403 for all three.

### Inbox desktop notifications (new)

- **Button:** the inbox header carries the "turn on/off notifications" button. With permission granted it starts on, and clicking it turns notifications off.
- **Tab visible but not focused**, as `operator`:
  - One new proposal raised `42 was proposed and needs approval` (tag `task:42:proposed`).
  - A burst of five raised one `5 new items in your inbox` summary listing the first three.
  - `pam` and `mia` got nothing, which is right because those items are not in their inbox.
- **Tab hidden** (`visibilityState = "hidden"`): nothing was raised for either arrival. The shell poll in `App.jsx` returns early while the document is hidden, so the inbox never sees the new items. This is **Bug 3**.

### Other journeys

- **Task pane history:** `/?state=proposed` → open task 32 → Back returns to `/?state=proposed` with the same rows. Forward goes to `/tasks/32`, and a reload keeps the pane.
- **Team settings › Members:** adding a proposer and a viewer each sends one `PUT /api/teams/default` with the full member list. Admins see team-scoped Executors correctly: payments shows `box-payments`, default shows `box-default`, and Issue token preselects the open team.
- **New task base branch:** the base was prefilled as `grain/task-15` for `acme/widgets`. That comes from the demo's seeded task 16, which has no `authorKind`, so `suggestsBase` counts it as human. This is documented behaviour and was not filed.

## Bugs filed

All five were proposed with `propose_task` against `candidate`. Each has the reproduction, the expected and actual behaviour, and code pointers. None has a fix on `main` (`b8982e98d`), and none appears in an earlier `docs/test-runs` report.

1. **Releases and branches:** any signed-in user, even one on no team, can create, cut, promote and merge releases, and request branches. These routes have no `requireRepoAdmin`.
2. **CI:** `POST /api/ci/runs {taskId}` starts a run on a task the caller cannot read. There is no team scope check in `StartTaskCIRunOpt`.
3. **UI:** inbox desktop notifications never fire from a hidden background tab, because the app's poll stops while hidden.
4. **UI:** Team settings › Executors tells non-deployment-admins, team admins included, "No executor tokens found", offers Issue token, and raises a 403 toast.
5. **UI:** every page load by a non-admin requests `GET /api/state-repo` and logs a 403 console error.

## Setup notes

- **Native selects:** the New task repo picker is a native `<select>`. Use `selectOption`, because clicking the `<option>` times out as not visible.
- **Visible text boxes:** the task pane has hidden autosize `<textarea aria-hidden>` elements. Target `textarea:not([aria-hidden=true]):visible` for the comment box.
- **Notification stub:** a stub with `permission = "granted"` starts the inbox toggle on. Clicking it in a script turns notifications **off**.
