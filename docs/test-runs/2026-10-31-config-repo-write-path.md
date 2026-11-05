# Test run — grain/config as the only API write path and held config PRs (2026-10-31)

Walks §2 of [`docs/test-plans/2026-10-30.md`](../test-plans/2026-10-30.md): internal-mode refusals, `POST /api/state-repo/pull-requests` and its validated, held config pull requests, the `directConfigWrites` opt-out, and the widened config ceiling (PR #145 `d887dea9`, `00acf020`, `fef0ec3d`, `8a658fea`; PR #166 `dd0cd0a5`). Task 2011.

## Environment

- **Tree:** `main` at `3c02c570`, plus this branch's fix (below) for the "after" half.
- **Build:** `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` (Go 1.26.10, Node 20.19.0), so the UI is embedded.
- **Daemon:** a real `grain daemon`, not `grain demo`:
  - set up by `grain init workstation -non-interactive -sandbox-backend host -model-backend claude -ui-addr 127.0.0.1:4300 -start-service=false` under a temp `HOME`;
  - started from the `daemon.sh` that init wrote, with `-sandbox-port-base=0`;
  - `GET /api/state-repo` reports `mode: internal`, `repository: grain/config`;
  - no `state-repo.json`, so no `directConfigWrites`.
- **Callers:**
  - a person: the daemon's access code as a bearer token, and a signed-in browser session;
  - an agent: an external-agent token (`POST /api/external-agents/tokens`, scope `settings:write`), sent with `X-Grain-Acting-Reason`.
- **Browser:** Chromium from `@playwright/test` 1.62.1 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, at 1440×900 and 390×844, in light and dark.
- **Stubbed:** nothing in grain. A model connection (`custom`, `/bin/true`) was added only so the "every connection disabled" check had a connection to disable. No run was dispatched.

## The plan predates `settings_locked`

`17fcb581` (task 1933) landed after the plan was written. It changed the precondition the plan assumes:

- **Internal mode no longer refuses by default.** `configRepoFor` sends an API write to grain/config only when `grain_config.settings_locked` is on. Settings → General's "Lock settings in UI (require config repo PRs)" turns it on.
- **Without the lock,** every write in this walkthrough went through directly (200), with or without `directConfigWrites`.
- **With the lock,** Settings and repository pages render their non-bootstrap fields read-only, with "Change them with a config pull request" pointing at the Config Repo Browser. So a person never gets a refusal from those pages.
- **What still reaches the refusal dialog in the browser** is any surface that is not read-only, such as Processes → New → Template.

Everything below ran with `PUT /api/settings {"settingsLocked":true}`. That write is bootstrap, so it was accepted.

## Results by gap

### 1. A person's journey from a refusal to a held PR — works; one bug fixed

- **Settings → General → Environment name** is disabled while locked: no refusal, by design.
- **Processes → + New → Template → Add template** opens the refusal dialog at all four sizes. The dialog shows:
  - "This setting lives in grain/config (main) and is changed with a config pull request there";
  - links into the Config Browser for `tables/template_sequence.json` and `settings/templates.yml`;
  - the patch;
  - **Create Config PR**.
- **The plan's premise is out of date.** It said `ui/src` had no caller of `/api/state-repo/pull-requests`. `ConfigRefusal.jsx` now posts the refusal's `change` from that button (`3d26e211`).
- **Create Config PR → "Opened config pull request #3 as task 3. It waits for a person to submit it."** The template was not created.
- **Bug, fixed here: the 422 was unreadable and offered a retry that could only fail.** Repro:
  1. Open the refusal dialog for a new template.
  2. Let another config PR that also adds a template merge first (`POST /api/templates` → `POST /api/state-repo/pull-requests` → `POST /api/tasks/<id>/submit`).
  3. Click Create Config PR.

  - **Before:** 422, shown as the raw server line in a red alert placed *after* the patch box (up to 50vh tall): "the config change does not pass validation: the change does not apply to main: staterepo: applying the change to tables/template_sequence.json: the patch does not match the file at line 5". Create Config PR stayed on offer, and clicking it again failed the same way every time.
  - **After:**
    - the 422 carries `code: config_change_stale`;
    - the dialog says "grain/config has changed since this was refused — This patch no longer applies to main, so no pull request was opened. Close this and make the change again for a patch that does.", with the server's line as a caption;
    - the retry button is gone;
    - a dispatch-locking 422 lists its `validation.liveness` findings as bullets under "This change does not pass validation";
    - both the success and error alerts now sit above the patch.
- **Finding, filed as a follow-up: a misleading discard prompt after opening the PR.** The template editor behind the dialog still holds the edit the PR now carries. Clicking the dialog's "task N" link fires App's `onPopState` unsaved-edit guard ("discard unsaved edits?"). Cancelling, which Playwright does by default, pushes `/processes` back and leaves the person where they were.
- **Minor, not fixed:** a template's refusal has no `description`, so its PR is titled "Change tables/template_sequence.json, settings/templates.yml".

### 2. The held PR's life — works

- **Over HTTP.** I posted `{"title":"Raise merges per repo","changes":<refusal.change>}` from a refused `PUT /api/settings {"maxMergesPerRepo":2}`. It answered 201 with task 2 on `grain/task-2`, which is `awaiting_submit` and not auto-merge.
- **The plan's own curl body fails.** It sends `{"change": ...}`, which answers 400 "changes is required". The request field is `changes`, while the refusal's is `change`. Readable, but worth knowing.
- **`/inbox`:** "asked: submit for merge" lists "Raise merges per repo · grain/config · not auto merge · submit — Asked you to submit grain/config#2", and task 3 the same way.
- **`/tasks/3`:** shows "PR opened grain/config#3", grain CI success on the config check, and a **submit** action.
- **Submit, clicked in the browser on `/tasks/2`:** the task closed and, after the next sync, `GET /api/settings` read `maxMergesPerRepo: 2`. Submitting task 3 the same way created the template "walk 1440 light".
- **422 for `max_workers: 0`** (a hand-made `tables/grain_config.json` patch): "the config change does not pass validation: staterepo: the change would lock dispatch: max_parallel is 0, which lets no run start", with `validation.liveness`.
- **422 for disabling the only connection:** "…every model connection is disabled, so no run has anything to run on".
- **Finding, filed as a follow-up: the liveness check only covers config PRs.** `max_parallel` (`MaxWorkers`), the connections and `default_model` are all bootstrap fields. A person can set them directly: `PUT /api/settings` disabling every connection answered 200. So the liveness suite only ever judges config PRs, and Settings → Models/Sandboxes can still lock dispatch. Neither 422 is reachable from a UI write; the stale patch above is the 422 a person actually meets.

### 3. Bootstrap writes pass and a mixed write is refused whole — works

All with settings locked:

| Write | Result |
| --- | --- |
| `PUT /api/settings {"sandboxCpus":5}` | 200 |
| `PUT /api/settings {"agentBackends":[…]}` (Settings → Models) | 200 |
| `PUT /api/settings {"maxWorkers":3}` | 200 (Sandboxes field) |
| `PUT /api/settings/cli-run {"settings":{"claude":{"location":"host"}}}` | 200 |
| `POST /api/gcp-keys` / `DELETE /api/gcp-keys/walk` | 200 / 200 |
| `PUT /api/settings {"maxMergesPerRepo":2}` | 409 `config_managed_externally`, `internal: true`, patch to `tables/grain_config.json` |
| `PUT /api/settings {"sandboxCpus":4,"maxMergesPerRepo":2}` | 409, and neither field changed (`sandboxCpus` stayed 4 from earlier, `maxMergesPerRepo` stayed 1) |

`PUT /api/console-sessions/director {"model":…}` went through while locked. That is by design (`45e1fb98`): a parameter edit on an open session's task does not touch grain_config.

### 4. Opt-out, live — works

These changes to `<data-dir>/state-repo.json` were made on the running daemon, with no restart:

| File contents | `PUT /api/settings {"maxMergesPerRepo":N}` |
| --- | --- |
| `{"directConfigWrites": true}` | 200, value written |
| `{"directConfigWrites": false}` | 409 |
| file removed | 409 |
| `{"remote":"/tmp/ext.git","branch":"main","directConfigWrites":true}` (external mode) | 409 naming `/tmp/ext.git`; `POST /api/state-repo/pull-requests` 400 "open the pull request there" |

### 5. Ceiling over a real policy — works

I opened config PRs creating `settings/config-policy.yml` with a single `auto_merge_paths` entry:

| Entry | Result |
| --- | --- |
| `tables/template_*.json` | 422 "matches NeverAutoMergePaths entry tables/template_read.json" |
| `tables/gcp_key.json` | 422 |
| `tables/standing_sandbox*.json` | 422 |
| `tables/persistent_sandbox.json` | 422 |
| `tables/schedule_*.json` | 422 |
| `tables/board*.json` (benign) | 201 |

**Not walked live:** "an agent auto-merge config PR touching a template is held for a person". It needs an agent run pushing to grain/config, and this daemon has no model. It is covered by `TestStateRepoNeverAutoMergePathsCeiling` and `TestExternalStateRepoTemplateChangeIsHeldForAPerson` (`pkg/orchestrator/securityhold_state_test.go`).

### 6. Agent token on the PR route — refused

- **External agent token, scope `settings:write`, with a reason:** 403 "only a person may open a config pull request here; an agent opens one from a task on grain/config".
- **Without a reason:** 403 "is destructive … give a reason".
- **The same token with `PUT /api/settings {"settingsLocked":false}`:** refused with a config refusal (`access.canWrite: false`).
- **A bootstrap write with that token:** 200.

## Changes on this branch

- **`fix(ui): say why a held config pull request was not opened`**
  - Server: `ui.ConfigPullRequestInvalid.Stale`; the daemon sets it when `ApplyChanges` fails; the handler adds `code: config_change_stale` to the 422.
  - UI: `api.js` keeps `validation` on the error; `ConfigRefusal.jsx` gets `ConfigPullRequestError`, moves the alerts above the patch, and drops the retry for a stale patch.
  - Tests:
    - `TestOpenConfigPullRequestCommitsAValidatedHeldChange` now moves main under a rehearsed change and expects `Stale`;
    - `TestOpenConfigPullRequestAnswersAnInvalidChangeWithItsReport` (pkg/ui);
    - two `ConfigRefusal.test.jsx` cases;
    - an `api.test.js` case.
  - No `pkg/staterepo` or `pkg/trustboundary` code changed.
- **`test(e2e): walk a refusal to a held config PR on a real daemon`**
  - `ui/e2e/config-refusal-pull-request.spec.js` runs on a real daemon that opts *in* to the config write path (`startDaemon(..., { directConfigWrites: false })`, a new option on `realdaemon.js`) with `-max-mergers 1`, settings locked.
  - At 1440, it creates a template, gets the refusal, clicks Create Config PR, follows the task link, finds it in `/inbox`, Submits, and waits for the template to appear.
  - At 390, it opens a refusal, lets another config PR land, clicks Create Config PR, and asserts the stale message, that the retry is gone and that no template was created.
  - Both alerts are asserted by bounding box to start inside the viewport.

## Screenshots

Taken with a Playwright script over the daemon above. `make ui-screenshots` drives `grain demo`, which has no grain/config and cannot produce a refusal.

- **Before:** the stale-422 dialog at 1440/390 × light/dark, on `3c02c570`.
- **After:** the same four, plus the refusal dialog at all four sizes and the opened-PR alert at 1440 light.

## Follow-ups filed

- Close or clean the originating editor once a refusal's config PR is opened, so the "task N" link does not ask to discard an edit the PR now carries.
- Decide whether direct bootstrap writes (Settings → Models/Sandboxes) should run `CheckDispatchLiveness`, as config PRs do.
