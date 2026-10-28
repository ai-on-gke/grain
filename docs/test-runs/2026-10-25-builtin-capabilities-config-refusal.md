# Built-in capabilities and external config-repo refusal walkthrough, 26 October 2026

grain/task-928, executing `docs/test-plans/2026-10-25.md` §2 (built-in in-tree
capability servers, alias adoption, and the external `grain/config` refusal
with rehearsed exact edits) against a live `grain demo`, in a real browser and
over the API.

## Summary

- **Built-in capability servers render, but not where the plan says.** The six
  built-ins (`gcp`, `git`, `host-script`, `model-proxy`, `self-debug`,
  `skill`) appear under Settings → Credentials → Capability servers, each
  with a `built-in` badge, its summary and canonical id, its
  `grain capability <name>` command, a grant count and edit, test, disable
  and bind-a-secret actions. `/settings/capabilities` opened **General**
  instead (bug 1, fixed here).
- **Granting capabilities through the New task form persists.** Task 13 was
  filed with `dispatch-control` and `db-read` ticked beside the defaults.
  Task 14 was filed with the `cloud-key` server method granted with
  arguments (`account: staging`, `project: demo-project-1`). The schema's
  defaults were filled in, and both tasks show their grants on
  `/tasks/<id>` and in `GET /api/tasks/<id>`.
- **External mode refuses every mutating route with 409 and changes
  nothing.** 14 refused requests covered settings, repos, repo config,
  merge-on-green, repo capabilities, schedules, console sessions (create and
  delete) and capability servers (built-in and new). Afterwards, nine
  `GET` snapshots were byte-identical to before. The Director session's task
  (12) stayed `queued`: deleting its session did not cancel it.
- **The rehearsed edits are real, applicable patches.** Each `change` entry
  is a one-hunk unified diff headed `diff --git a/<path> b/<path>`, naming a
  file that is also listed in `path`, with a `fragment` for every non-delete
  change. `git apply --check` accepts the merge-on-green patch's new empty
  `setup.sh`.
- **In the browser, external mode shows a banner on every Settings section
  and disables the write controls.** A write the UI still offers (adding a
  schedule) opens the ConfigRefusal dialog with both rehearsed patches. Until
  this run, `grain demo` could not be put in external mode at all (bug 2,
  fixed here).
- **`docs/external-config-repo.md` contradicted the code** about requests
  that would fail anyway (bug 3, doc fixed here).

## Environment

- Branch `grain/049f1e09/task-928`, cut from `candidate` at `6d8f57485` and
  rebased onto `candidate` again before pushing (last at `abd299815`). Every §2 commit the plan names on
  `candidate` (`4b3852dda`, `203b2c207`, `9cbc4f151`) is in that history.
- Debian sandbox (kernel 6.1), Go 1.26.10, Node 20.19.0, Playwright Chromium
  1234 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- `npm --prefix ui ci && npm --prefix ui run build`, then
  `go build -o ~/grain ./cmd/grain`.
- Demos, each signed in with `-access-code-file` (a bearer token for curl,
  `POST /api/auth/access-code` for the browser):
  - `grain demo -addr 127.0.0.1:4391 -open=false -secrets-store` (internal
    mode).
  - `grain demo -addr 127.0.0.1:4392 -open=false -secrets-store
    -external-config-repo https://github.com/acme/grain-config.git`
    (external mode; the flag is new in this branch).
  - `grain demo -addr 127.0.0.1:4393` built with the fixed UI, to confirm the
    redirect.
- The browser walkthroughs were throwaway Playwright scripts (Chromium,
  1280×900–1000) that logged DOM text, network responses, dialogs and page
  errors. No page error was logged on any page visited.

## The plan against the tree

Parts of §2 describe things that are named differently, or do not exist, on
`candidate`. Each step below was tested against what is actually in the tree.

| Plan says | Tree has |
| --- | --- |
| `pkg/capabilities/builtin/builtin.go` | `pkg/capability/intree/builtins.go` (`Builtins`, `EnsureBuiltins`) |
| built-ins `gcp, github, slack, linear, sentry, datadog, pagerduty, postgres, redis, playwright, self-debug, dispatch-control` | built-in **servers** `gcp, git, host-script, model-proxy, self-debug, skill`. `dispatch-control`, `self-debug`, `db-read` and the rest are in-process capabilities in `GET /api/settings` `capabilities`. There is no github/slack/linear/sentry/datadog/pagerduty/postgres/redis/playwright built-in. |
| `GET /api/capabilities` | no such route (`no such API endpoint`). Built-ins are merged into `GET /api/capability-servers`. |
| `/settings/capabilities`, `CapabilitiesSection.jsx` | Capability servers and policy cards on `/settings/credentials` (`CredentialsSection.jsx`, `CapabilityServersPanel.jsx`). Bug 1. |
| `ConfigRepoModeExternal`, `ConfigRepoReadOnlyError`, `rehearsingStore`, `pkg/ui/store_rehearsal.go` | `externalConfigRepoError` and `configWriteRehearsal` in `pkg/ui/config_refusal.go`, using `staterepo.Rehearse`. The write runs in a transaction that is rolled back; there is no store clone. |
| refusal body `file` / `before` / `after`, files `config.yaml` / `repos.yaml` | body `code: config_managed_externally`, `repo`, `branch`, `path[]`, `change[]{path, op, patch, fragment}`, `description`, `docs`, `url`, `error`. Files are the export's own: `tables/grain_config.json`, `tables/repo_config.json`, `settings/schedules.yml`, `tables/*_sequence.json`, `scripts/repos/<o>/<n>/setup.sh`. |
| `SettingsRedirect.jsx`, `SettingsModal.jsx` | `SettingsPage.jsx` (banner, `external-config-repo-banner`), `ConfigRefusal.jsx` (dialog), `api.js` (`err.configRefusal`). |
| `POST /api/repos` then `PUT /api/repos/...` | both exist. Per-repo config is also `PUT /api/deployment/repos/{o}/{n}/config`. |

## 1. Built-in capabilities in the UI

`GET /api/capability-servers` (internal mode) lists the six built-ins first,
each with `config: {command: grain, args: [capability, <name>], builtin}` and
a `builtin` block (`name`, canonical `id`, `summary`, plus `needsConfig` and
`host` for `gcp` and `host-script`). The demo's two remote servers follow
(`…/cloud`, `…/gcp`), with live `status.methodDetails`.

On `/settings/credentials`, under "capability servers", the built-in servers
group renders one row per built-in: `<name> server`, the summary with the
canonical id, `grain capability <name>`, a `built-in` badge, `granted by 0`,
the state `unknown`, and edit, test, disable and bind a secret. Below them,
"your servers" lists the demo's servers. Each shows its methods' argument
tables (for example `cloud-key`'s `account` enum, `project` pattern,
`scopes` bounds and `ttl_minutes` range) and stateful/stateless labels.

- **Bug 1 (fixed):** `/settings/capabilities` landed on General.
  `settingsSectionFor` knew no such section. Several places pointed there:
  `docs/architecture.md` (twice), the picker's not-ready hint ("…until that
  is set (Settings > Capabilities)"), the empty-picker hint, and the
  New task form's "this deployment's (Settings > Capabilities)". The fix maps
  `capabilities` and `capability-servers` to `credentials` and corrects the
  text. Default capabilities live on Settings → Tasks, so the New task hint
  now names that. After the fix, `/settings/capabilities` is rewritten to
  `/settings/credentials` with the built-in servers card on screen.
- The built-ins show state `unknown` in the demo and advertise no methods,
  because the demo's capability registry holds only its own fake servers.
  As a result:
  - the New task form's "Grant a server method…" menu offers only
    `cloud-key`, `cloud-viewer` and `gcp-key` from the demo's remote servers;
  - `POST /api/tasks` granting `https://grain.dev/capabilities/gcp#gcp-key`
    is refused: `capability https://grain.dev/capabilities/gcp is not
    configured in mcpServers`.

  A real daemon applies `intree.EnsureBuiltins` to its registry
  (`pkg/daemon/capability_servers.go`), so this only affects the demo. It is
  not fixed here: starting `grain capability <name>` children from the demo
  is a larger change.

## 2. Attaching capabilities to a task through the UI

- **Without arguments.** On the New task form, "+ new task", repo
  `acme/widgets`: `gcp key`, `gemini key` and `self debug` were pre-ticked
  as defaults (the first two marked "not ready -- needs gcp-key-minter").
  The script ticked `dispatch control` and `grain db read`, then clicked
  Create task. `POST /api/tasks` returned 201 for task 13 with
  `capabilities: [db-read, dispatch-control, gcp-key, gemini-key,
  self-debug]`. `/tasks/13` lists "grain db read, dispatch control, gcp key,
  gemini key, self debug".
- **With arguments.** In "Grant a server method…", choosing `cloud-key`
  opened its argument form. It shows the plain-text warning, marks `account`
  and `project` required, and validates `scopes` (at least 3 characters,
  1–4 entries, no repeats) and `ttl_minutes` (5–720). The script set
  `account: staging` and `project: demo-project-1` and clicked Grant. The
  chip read `cloud-key · account: staging, project: demo-project-1`. Create
  task returned 201 for task 14 with
  `https://capabilities.demo.grain.example/cloud#cloud-key={"account":"staging","project":"demo-project-1","read_only":true,"scopes":["cloud-platform"],"ttl_minutes":60}`,
  with the schema defaults filled in. `/tasks/14` and `GET /api/tasks/14`
  show the same grant.
- The Create task button stays disabled until a repo (or No repo) is chosen.
  That is expected.

## 3. External `grain/config` refusal over the API

The demo had no way into external mode. It wires neither `StateRepo` nor
`DataDir`, so `Client.externalConfigRepo` always returned empty, and writing
`state-repo.json` into its throwaway directory changed nothing.

**Bug 2 (fixed):** `grain demo -external-config-repo <remote>
[-external-config-branch <b>]` now wires a stub `StateRepo`
(`pkg/demo/demo_external_config.go`). It reports the remote through
`ExternalConfigRepo()` and `Status()` and refuses sync, tree and remote
changes. It is wired after the seeds, because the seeds write configuration
through the same guarded client: wired first, it refused the demo's own
board seed.

With that demo running, the script snapshotted nine reads:
`/api/settings`, `/api/schedules`, `/api/console-sessions`,
`/api/capability-servers` (minus live `status`), `/api/capability-policy`,
task 12's state/outcome/closedAt/capabilities,
`/api/deployment/repos/acme/widgets/config`,
`/api/repos/acme/widgets/capabilities` and `/api/state-repo`. It then sent:

| Request | Status | `path` | Rehearsed `change` |
| --- | --- | --- | --- |
| `PUT /api/settings {maxWorkers: 5}` | 409 | `tables/grain_config.json` | `"max_workers": 5` |
| `PUT /api/settings {environmentName: walkthrough}` | 409 | `tables/grain_config.json` | `"environment_name": "walkthrough"` |
| `POST /api/repos {repo: acme/gadgets}` | 409 | `tables/grain_config.json` | `"target_repos": "acme/widgets,acme/gadgets"` |
| `PUT /api/deployment/repos/acme/widgets/config {promptExtension}` | 409 | `scripts/repos/acme/widgets/setup.sh`, `tables/repo_config.json` | new `acme/widgets` row with the prompt extension (and its empty `setup.sh`) |
| `PUT /api/repos/acme/widgets/merge-on-green {mergeOnGreen: true}` | 409 | same two files | new `acme/widgets` row |
| `PUT /api/repos/acme/widgets/capabilities {defaultCapabilities: [self-debug, db-read]}` | 409 | same two files | `"default_capabilities": "self-debug,db-read"` |
| `POST /api/schedules {title, repo, daily 09:00}` | 409 | `tables/schedule_sequence.json`, `settings/schedules.yml` | sequence number 3, plus a `sched-3` entry with `daily: at: "09:00"` |
| `POST /api/console-sessions {id: walkthrough}` | 409 | `tables/grain_config.json` | `console_sessions` with the new entry |
| `DELETE /api/console-sessions/director` | 409 | `tables/grain_config.json` | none (unrehearsed by design), with a description |
| `PUT /api/capability-servers` (disable built-in `self-debug`) | 409 | `tables/grain_config.json` | `mcp_servers` with the change |
| `PUT /api/capability-servers` (new `https://example.com/caps/new`) | 409 | `tables/grain_config.json` | `mcp_servers` with the entry |
| `DELETE /api/console-sessions/no-such-session` | 404 | -- | -- (looked up before the guard) |
| `PUT /api/repos/nobody/nothing/merge-on-green` | 409 | `scripts/repos/nobody/nothing/setup.sh`, `tables/repo_config.json` | a new `nobody/nothing` row |

Afterwards, all nine snapshots were byte-identical to before. Task 12, the
Director session's task, was still `queued` with no outcome and no
`closedAt`, so the unrehearsed delete did not cancel it.

Every `change` entry passed the script's shape checks: header
`diff --git a/<path> b/<path>`, a `@@` hunk, a `fragment` for non-deletes,
and its path listed in `path`. The one exception is expected: an empty
`setup.sh` is created by a header-only `new file mode 100644` patch, which
`git apply --check` accepts.

A second batch sent requests that change nothing or would fail validation:

| Request | Status | Body |
| --- | --- | --- |
| `PUT /api/settings {maxWorkers: -5}` | 409 | `path: [tables/grain_config.json]`, empty `change`, description |
| `PUT /api/settings {environmentName: <40 chars>}` (max 32) | 409 | same |
| `POST /api/schedules {title: ""}` | 409 | `settings/schedules.yml`, empty `change` |
| `POST /api/schedules {repo: nobody/nothing}` | 409 | same |
| `PUT /api/repos/acme/widgets/capabilities {capabilities: [...]}` (wrong field, a no-op) | 409 | `tables/repo_config.json`, empty `change` |
| `PUT /api/capability-policy` (body fails to decode) | 400 | the JSON error (decoded before the guard) |

- **Bug 3 (doc fixed):** `docs/external-config-repo.md` said a request that
  would fail anyway "is answered with that failure instead, as it would be in
  internal mode". `1f69479df` ("keep refusing every external-mode config
  write with 409") deliberately made `configRefusalWriter` swallow the
  route's own 404 or 400 and send the refusal, without changing the doc. The
  doc now describes what the code does: 409, the same path and description,
  an empty `change`, and 404 only where a route looks the target up before
  its guard. It also lists console-session deletion among the routes refused
  without a patch, and how to try the mode with `grain demo`.
- Observation, not changed: merge-on-green, and the other per-repo
  settings, on a repo with no `repo_config` row (including one that is not a
  target repo, `nobody/nothing`) rehearse as creating the row. That follows
  from what the route does in internal mode. Whether a non-target repo
  should be refused instead was not settled here.

## 4. External mode in the browser

On each of `/settings/general`, `/settings/tasks`, `/settings/repositories`
and `/settings/credentials`, the `external-config-repo-banner` reads:
"Configuration is read-only and managed in
https://github.com/acme/grain-config.git (main). Switch to the internal
config repository in Config Repo settings to edit settings in the UI. Edit
`<file>` in acme/grain-config (main), then open a pull request there; grain
applies it on its next sync." The file is `tables/grain_config.json` on
General, Tasks and Credentials, and `tables/repo_config.json` on
Repositories. Write controls are disabled, for example the environment name
field, the auto-merge-by-default switch, and every capability server's
disable button. No write was sent from these pages.

Adding a schedule is still offered: Processes → "+ new" → schedule, title
"walkthrough schedule", "add schedule". It returned `POST /api/schedules`
409 and opened the ConfigRefusal dialog: "configuration is managed in
acme/grain-config. Edit tables/schedule_sequence.json, settings/schedules.yml
in acme/grain-config (main), then open a pull request there…". Below that
were the two full patches: the sequence entry, and the 23-line
`sched-3` YAML block with `every: hours: 24`, `target: acme/widgets` and its
timestamps. The dialog has "how this works" and "close" buttons.
`GET /api/schedules` still listed three schedules afterwards.

Screenshots (`make ui-screenshots` / `ui/e2e/screenshot.mjs`, desktop and
phone, light and dark):
- `/settings/capabilities` before the fix (General) and after (Credentials).
- `/settings/general` and `/settings/repositories` under
  `-external-config-repo`.

The sandbox's file reader shows PNGs as raw bytes, so the pages were checked
by DOM text, URL and control state rather than by eye. The contact sheets are
attached to the pull request.

## Fixes and regression coverage in this branch

- `ui/src/settingsSections.js`: `capabilities` and `capability-servers`
  resolve to `credentials`. `ui/src/state.js` and `NewTaskOverlay.jsx`: the
  hints name Settings > Credentials and Settings > Tasks.
  `ui/src/paths.test.js` pins both paths.
- `pkg/demo/demo_external_config.go`, `pkg/demo/demo.go`, `pkg/cli/demo.go`:
  `-external-config-repo` and `-external-config-branch`.
  `pkg/demo/demo_external_config_test.go` covers the stub and
  `pkg/cli/demo_flags_test.go` covers the flags. A branch flag without a
  repo is refused.
- `docs/external-config-repo.md` and `docs/architecture.md`: as above.

Ran locally: `npx vitest run src/paths.test.js src/state.test.js
src/components/NewTaskOverlay*` (414 passed); `go test ./pkg/demo ./pkg/cli
-run 'TestExternalConfigRepo|TestParseDemoFlags'`; `go vet ./pkg/demo
./pkg/cli`. The full Grain CI suite ran on the pushed branch
(`wait_for_checks`).

## Not covered

- Built-in server method grants (for example `gcp server › gcp-key` with
  arguments) end to end, for the demo-registry reason in §1.
- Capability alias adoption (`EnsureBuiltins` moving an operator's
  `grain capability <name>` entry to the canonical id) was not exercised
  live, because the demo has no operator entries of that kind. Unit tests in
  `pkg/capability/intree/builtins_test.go` cover it.
- A real external config repository with a sync. The existing
  `ui/e2e/config-repo-iac-modes.spec.js` covers switching modes against a
  real daemon and a bare repository.
