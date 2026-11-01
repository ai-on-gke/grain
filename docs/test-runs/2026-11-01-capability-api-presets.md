# 2026-11-01: Retired capabilities → agent API presets, and capability policy (test plan §7)

Task 2222 (`grain/049f1e09/task-2222`, filed by the nightly test-plan job, task
2084), walking §7 of [`docs/test-plans/2026-11-01.md`](../test-plans/2026-11-01.md)
("In-process capabilities retired for agent API presets"). The same run also
walks the previous plan's still-unreported §3,
[`docs/test-plans/2026-10-30.md`](../test-plans/2026-10-30.md) §3 (capability
policy at filing time and in grant pickers, scope-set holders). **That §3 is
covered here**, on a real upgraded daemon, alongside task 2012's demo walk
([`2026-11-01-capability-policy-pickers.md`](2026-11-01-capability-policy-pickers.md)).

Scope: PRs #247, #291, #301 (migrations 67–69, 71–72, 75–76), #271
(`f36c5d61`, detach rule), #216 (capability (i) popovers).

## Outcome

| Area | Result |
|---|---|
| Pre-67 store upgraded: grant rows, defaults, export, `GET /api/tasks/<id>`, grant chips | **Pass** |
| Catalogue entries under retired names after upgrade | **Fail → fixed** (bug 1, security) |
| `settings/capabilities.yml` naming an old capability is reported | **Pass with bug 1's fix** (silently passed before it) |
| Each preset from a real Docker sandbox, and refused without it | **Pass**, with bugs 2 and 3 fixed |
| System memory-agent and release-notes templates, one run each | **Pass** (dispatch, grants, prompts; see notes) |
| Previous §3: policy at filing, pickers, held grant denied, defaults, `requires_person`, scope-set holders | **Pass** (two observations) |
| `f36c5d61` detach rule | **Pass** |
| (i) popovers at 1440/390 and keyboard | **Fail → fixed** (bug 4) |

Bugs fixed on this branch, each with a regression test:

1. **Upgrade silently dropped catalogue restrictions on retired capabilities**
   (`pkg/model/capability_catalogue.go`). Scope leak, so treated as security.
2. **`grain memory write <key> <body>` overwrote the whole deployment memory**
   (`pkg/sandboxcli`).
3. **`grain quarantine` reported success when its repair proposal was not filed**
   (`pkg/gitproxy`, `pkg/sandboxcli`).
4. **Enter and Space could not open a capability (i) popover** (`ui/src/components/InfoPopover.jsx`).

## Environment

- Debian task sandbox, `linux/amd64`, 7 vCPU / 15 GiB. Go 1.26.10, Node.js
  v20.19.0, Docker 29.9.2. `sqlite3` and `imagemagick` were installed with apt.
- **Old build:** `git worktree add /var/tmp/old 8bdfa3a5` (schema 65), `go build
  ./cmd/grain` → `/var/tmp/grain-old`.
- **New build:** `main` at `4077424a` (`make frontend`, then `go build
  ./cmd/grain`, schema 82), then the same tree with this branch's fixes
  (`/var/tmp/grain-fix`, plus `cmd/grain-sandbox` built beside it so runs get
  this build's in-sandbox CLI).
- **Daemon:** `grain daemon -data-dir /var/tmp/d1/data -ui-addr 127.0.0.1:18080
  -sandbox-port-base=0 -max-workers=N`. During setup it ran with
  `-disable-native-sandboxes`. For the sandbox steps it ran with
  `-container-sandboxes -container-sandbox-network bridge
  -container-git-proxy-host 10.240.0.1` and image
  `ghcr.io/bwsalmon/grain/k8s-sandbox:latest`. The single-tenant access code
  came from `data/local-api-token`.
- `data/state-repo.json` was `{"directConfigWrites":true}` (the host-local
  opt-out). Without it, templates, schedules, console sessions and settings
  were refused `409 config_managed_externally` with the grain/config patch, as
  previous plan §2 describes.
- **No model credentials.** Runs used a **scripted custom-command connection**
  (`framework: custom`, `command: /var/tmp/scripted-agent.py`). The script
  speaks MCP to the run's own `mcp_server`. It runs each line between
  `CMDS-BEGIN`/`CMDS-END` in the task description through `run_command`, which
  executes inside the run's real Docker sandbox. A line `@tool <name> <json>`
  calls that MCP tool directly. It logs every result to `/var/tmp/runs/<tag>.log`.
  So every `grain …` command below ran in a real sandbox, under the attempt's
  own minted token and scopes, against the real proxy and API.
- The daemon had no GitHub credential, so a task naming a repository could not
  be filed ("the deployment has no GitHub credential, so it cannot reach
  acme/widgets"). Test tasks were repoless (`noRepo: true`). The schedule
  targets `acme/widgets`, which a schedule accepts without reaching GitHub.
- Chromium: Playwright from `ui/package-lock.json`,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`. Throwaway scripts drove the
  live daemon, sending the access code as a bearer header. The committed spec
  runs over `grain demo`.

## 1. Pre-67 store → upgrade

### Built on `8bdfa3a5` (schema 65)

Filed over the API as the person:

- tasks 2–8: one task per old capability (`team-broadcast`,
  `dispatch-control`, `pipeline-control`, `memory`, `release-notes`,
  `self-upgrade`, `test-quarantine`)
- task 9: all seven
- `template-1`: all seven
- `sched-1`: all seven, on `acme/widgets`
- console session `oldcaps` (task 10): all seven
- deployment default capabilities: `[pipeline-control]`
- `acme/widgets` repo default capabilities: `[release-notes, memory]`.
  `ciCapabilities: [test-quarantine]` was refused: "not supported for CI".
- the capability catalogue (`PUT /api/capability-policy`):

  ```yaml
  in_process:
    memory: {enabled: false}
    release-notes: {restrictions: {repos: [acme/*], requires_person: true}}
    test-quarantine: {restrictions: {kinds: [task]}}
  ```

  Policy *rules* must name `https://` references, so the old names can only
  appear in the catalogue's `in_process` entries and the default lists.

The seeded system schedules already held `memory` (`system:memory-agent`) and
`release-notes` (`system:release-notes`). The old daemon was stopped. Then
`sqlite3` rows from `task_grant`, `template_grant`, `schedule_grant`,
`grain_config.{default_capabilities,capability_policy}` and
`repo_config.{default_capabilities,ci_capabilities}` were dumped, and the
state-repo export was copied: `settings/capabilities.yml`, `schedules.yml`,
`templates.yml`, schema 65.

### Upgrade on `main`

Starting `main`'s binary migrated the store to 82. It then **stopped**:
"the state repository holds a dump this build cannot read (… repository is at
schema 65, this build knows 82) … `grain state reformat` (or re-running
scripts/setup.sh) says so". That is the documented path: setup.sh runs
`grain state prepare-upgrade`. `grain state -data-dir … reformat` let the
next start replace the dump. Not a bug, but a manual upgrade without
setup.sh has to know it.

### Observations

- **sqlite diff.** Every old name became its preset, with `via` and `folder`
  kept:
  - tasks 2–10, `template-1` and `sched-1`
  - `system:memory-agent` (`api:memory`) and `system:release-notes`
    (`api:release-notes`)
  - the console session's grants (task 10)
  - `grain_config.default_capabilities` (`api:pipeline-control`)
  - `repo_config.default_capabilities` (`api:release-notes,api:memory`)

  The Director's `deployment-health` and `grain-log` were rewritten too: to
  `api:deployment:read` + `grain#health={}` and to `grain#journal={}`. No row
  still names an old capability. **Pass.**
- **`GET /api/tasks/<id>`** for tasks 2–10 lists only `api:` presets.
  `GET /api/templates` and `GET /api/schedules` show the same. **Pass.**
- **Export** (schema 82): `schedules.yml` lists the `api:` names, including
  the system schedules' `- capability: api:memory` and `api:release-notes`.
  `capabilities.yml` `defaults` reads `task: ['api:pipeline-control']` and
  `acme/widgets: ['api:release-notes', 'api:memory']`. **Pass.**
- **Grant chips** (Chromium, `/tasks/9`, 1440 and 390): "api preset:
  dispatch-control / memory / pipeline-control / release-notes / self-upgrade
  (dangerous) / team-broadcast / test-quarantine". No bare old name appears as
  a chip. **Pass.** Cosmetic: the same pane renders "api preset: …" in one
  chip list and "API preset: …" in another.
- **Catalogue: Fail → bug 1.** `capability_policy` still keyed its
  `in_process` entries by `memory`, `release-notes` and `test-quarantine`. The
  migrations rewrite grants and default lists, not the catalogue. Nothing on
  `main` judged an `api:` grant against those entries any more:
  - On the old build, `POST /api/tasks {"capabilities":["memory"]}` → **400**
    "capability memory is disabled in settings/capabilities.yml".
  - On `main` after upgrade, `["api:memory"]` → **201**, and `["memory"]`
    (normalised to `api:memory`) → **201**.
  - `release-notes`' `repos: [acme/*]` and `requires_person` stopped binding
    the same way.

  With the fix: both are **400** "capability api:memory is disabled in
  settings/capabilities.yml (in_process.memory, the capability it replaced)",
  `api:release-notes` on a repoless task is refused naming `acme/*`, and
  unrelated presets (`api:team-broadcast`) still file (201).
- **`settings/capabilities.yml` still naming an old capability.**
  `grain state check DIR` on the upgraded export plus the retired entries:
  - `main` before the fix: "ok: grain can load this". The file's
    `release-notes` restriction was silently not applied, though
    `template-1` and `system:release-notes` (both repoless) violate it.
  - With the fix: the check reports both holders, naming
    `in_process.release-notes`.
  - The old build reports its first violation the same way
    (`defaults.repos[acme/widgets].task: capability memory is disabled`).
  - A file whose `defaults` still list bare `pipeline-control` /
    `release-notes, memory` is checked under those names. Grants are
    normalised to `api:` when filed.

  **Pass with the fix.**

## 2. Each preset from a real sandbox

### Run with all seven presets (task 18)

The run's MCP tools included `api_pause_pipeline`, `api_resume_pipeline`,
`api_run_now`, `api_deployment_broadcast`, `api_start_upgrade`,
`api_cancel_scheduled_upgrade`, `api_upgrade_database*` and
`api_lift_agent_pause`. `grain api routes` listed the non-dangerous routes:
`memory:*`, `release-notes:*`, `POST /api/tests/quarantine`,
`GET /api/pipeline-pause`, `GET /api/upgrade*`, …

| Step | Command (in the sandbox) | Result |
|---|---|---|
| Pause | `@tool api_pause_pipeline {"body":{"reason":…}}` | `paused:true, byKind:agent, byId:18`; `grain api GET /api/pipeline-pause` agrees |
| Resume | `@tool api_resume_pipeline` | `resumed:true`; GET shows `paused:false` |
| Run now | `@tool api_run_now {"id":"3"}` | task 3 queued with `runNowAt` set |
| Broadcast | `@tool api_deployment_broadcast {"body":{"message":…}}` | `delivered_count: 6` |
| Memory write | `grain memory write "<text>"` | 200, `updatedByKind: agent` |
| Memory read | `grain memory read global/MEMORY.md`, `grain memory list` | the text; `global/ repos/` |
| Release notes | `grain release-notes stats` | week `2026-W43` stats |
| | `grain release-notes publish --version 2026-W44 "<notes>"` | 200, `updatedBy: 19` |
| | `--version v0.0.0-walk` | 400 "not a week grain files release notes under: … ISO weeks" (expected) |
| Quarantine | `grain quarantine ./pkg/foo TestWalkFlaky --reason …` | entry written, but no follow-up and exit 0 → **bug 3** |
| Self-upgrade check | `grain api GET /api/upgrade`, `/api/upgrade/targets` | `{"enabled":false,…}`; 400 "neither -upgrade-image nor -upgrade-src-dir configured". `api_start_upgrade` was not called. |

### `grain memory write walk-key "<body>"` → bug 2

The in-sandbox help documented `write <key> <body>` as "Write an entry under
memory/entries/<key>". In fact it replaced the deployment memory with
"walk-key <body>", and `grain memory read walk-key` then said `not found`.
With the fix the call is refused naming `doc-write`, and nothing is written.

### Quarantine's held repair proposal → bug 3

On a repoless run, `grain task propose` fails with "no repo given, and this
deployment has no default target repo configured", and `/cli/quarantine`
discarded that error. With the fix `grain quarantine` still writes the entry,
then prints "the test is quarantined, but its follow-up repair task was not
filed: bad request: no repo given … -- file one with grain task propose" and
exits 1.

The successful path (a repo run filing the held `quarantine-fix-*` proposal)
needs a reachable repository, so it was not shown here. CI covers it in
`tests/e2e/mcp_diagnostics_and_quarantine_walkthrough_test.go` and
`pkg/sandboxcli`'s `#102` case.

### Memory alone (task 20, `api:memory`)

- `grain memory write` → 200.
- `grain release-notes stats` → 403 "holds no api grant for scope
  release-notes:read".
- `publish` → 403 "release-notes:write".
- `grain api GET /api/release-notes` → 403. **Pass.**

### No grants (task 21)

- `GET /api/pipeline-pause` → 403 `dispatch:read`.
- `POST` pipeline-pause / run-now / broadcast through `grain api` → refused
  "requires scope dispatch:write / teams:broadcast, which is not permitted on
  the sandbox-cli channel". Dangerous routes are MCP tools only.
- None of the `api_*` tools were offered.
- `grain memory write` → 403 `memory:write`.
- `grain release-notes stats` → 403.
- `grain quarantine` → forbidden, naming `api:test-quarantine`.
- `GET /api/upgrade` → 403 `deployment:read`.
- `grain api routes` → "No agent API routes are granted". **Pass.**

### Scope probe

With no grants, `grain ls memory://global/` and
`grain read memory://global/MEMORY.md` return the deployment memory. This is
by design (`namespace.Scope.PermitsMemory`: global memory and the run's own
repository's memory). That text is already in every run's prompt (task 22's
prompt carries it). Other repositories' memory and documents are not
reachable. Not a leak.

## 3. System templates, one run each

`POST /api/schedules/system:memory-agent/run` and `…/system:release-notes/run`
filed tasks 24 ("Write down what grain has learned about working here",
`[api:memory, self-debug]`) and 25 ("Write up what the cluster shipped this
week", `[api:release-notes, self-debug]`). Both were dispatched into Docker
sandboxes on the upgraded store. Their prompts name the new CLI:

- task 24: `grain memory write --file <f>`, `grain memory doc-write <name>
  --summary <s> --file <f>`, `grain memory list/read`
- task 25: `grain release-notes stats` and `grain release-notes publish
  --title <t> --file <f>`

The release-notes template takes no `--version`; the period defaults to last
week. With the scripted backend the runs did no writing of their own. The
commands they would run are the ones exercised in §2. **Pass** for dispatch,
grants and prompts. No model was available to judge the templates' prose.

Every scripted run ended `awaiting_reply` rather than completing. That comes
from the scripted backend's closing call, not from grain's presets.

## 4. Previous plan §3, on the upgraded daemon

Policy saved: `deny grain#journal` everywhere, plus an allow rule for
`grain#health` with `runKinds: [process]` (a conditional rule).

- **Filing.** `POST /api/tasks` with `grain#journal={}` → 400 "the capability
  policy denies … (rule 1: deny …)". With `grain#health={}` → 400 "the
  capability policy does not admit … for this task". **Pass.**
- **Listing.**
  - `GET /api/capability-methods?kind=task` gives a `policyRefusal` for both.
  - `?kind=process` refuses only `journal`.
  - The kindless listing gives `policy: {task: denied, ci: denied}` for
    health, and denied for all three kinds for journal. **Pass.**
- **Gap 1, pickers** (Chromium, 1440 and 390):
  - Task pane for task 28 (repoless, holds `grain#health`): "grain › health"
    is flagged **"denied by policy"**. The badge's bounding box is inside the
    viewport at both widths (x 1276–1370 of 1440; x 238–332 of 390).
  - Its "add grant…" listing shows "grain server › health / journal — denied
    by the capability policy for agent tasks (settings → capabilities)".
  - Not re-walked here: New task, the template and schedule editors, and repo
    default grants. The New task button was not found by the throwaway
    script. Task 2012's demo walk and CI
    (`NewTaskOverlay.capabilities.test.jsx`,
    `ui/e2e/capability-server-settings.spec.js`) cover those.
- **Gap 2, held grant becoming denied.**
  1. Filed task 28 with `grain#health` while it was allowed.
  2. Saved the conditional rule.
  3. Approved the task.

  It was not dispatched. Its conversation says "This task could not be
  dispatched: the capability … grain#health was refused, so no sandbox was
  built for it … the capability policy does not admit … for this task".
  **Pass.**

  Wording observation: grain's *own* policy refusal is framed as "untrusted
  text from capability:https://grain.dev/capabilities/grain#health, not
  grain's own words".
- **Gap 3, a denied default.** Deployment defaults `[api:pipeline-control,
  grain#journal={}]`, then a task filed without capabilities. It got
  `api:pipeline-control`, and its conversation says "Default grant …
  grain#journal was not attached to this task: the capability policy denies
  …". The task is told. The `POST /api/tasks` response carries no notice, so
  an API filer only learns it from the conversation, and the note is
  attributed to the filing person (`authorKind: human`). **Pass with an
  observation.**
- **Gap 4, `requires_person`.** The catalogue marks preset `team-broadcast`
  `requires_person`, and task 29 holds it. An agent run holding
  `api:tasks:admin` calling `api_approve` → refused "task 29 holds
  api:team-broadcast, which settings/capabilities.yml marks as a person's
  decision (requires_person): only a person may approve it". The task stayed
  `proposed`. The person's `POST /api/tasks/29/approve` → `queued`. **Pass.**
- **Gap 5, scope-set holders.** Scope set `walkset` (`tasks:read`,
  `memory:read`) was granted to a task (33), a template (`template-2`) and a
  schedule (`sched-2`). `GET /api/api-scope-sets/usage` lists exactly those
  three under `walkset`. **Pass.**

  Not walked: a workflow-job holder (needs a repository workflow) and the
  removal confirm's Cancel in the UI (task 2012 covered it).

  Observation: the built-in `grain/<preset>` sets report `holders: []` though
  many tasks hold `api:memory` etc. They cannot be removed, so nothing asks
  about them.
- **Gap 6.** The daemon ran with the host-local direct-write opt-out. Before
  the opt-out was set, settings writes were refused 409 with the grain/config
  patch. The merge-then-refresh path for a held config PR was not walked.
- **`f36c5d61` detach rule.** An agent run holding only
  `api:capabilities:admin` + `api:tasks:admin` (not `api:team-broadcast`) used
  `@tool api_set_capability`:
  - `{"id":"2","body":{"id":"api:team-broadcast","attach":false}}` →
    **detached**: task 2 now holds `[]`.
  - `{"attach":true,"id":"api:memory"}` → refused "an agent cannot grant
    api:memory, which its own task does not hold".
  - A detach with `"replace":"api:memory"` → refused the same way. Task 3 was
    unchanged.
  - Through `grain api POST /api/tasks/2/capabilities` it is refused
    `capabilities:admin … not permitted on the sandbox-cli channel`. It is a
    dangerous scope, so a tool only.

  **Pass.**

## 5. (i) popovers

The live daemon's capability servers were not configured, so no method or
field descriptions (and no (i) buttons) rendered there. The popovers were
walked over `grain demo`'s attached demo capability server
(`https://capabilities.demo.grain.example/cloud`) in the new
`ui/e2e/capability-info-popovers.spec.js`, at 1440 and 390 px. For each
visible (i) button:

- focus opens a tooltip whose bounding box lies inside the viewport
- Escape closes it and focus stays on the button
- **Enter did not reopen it → bug 4**

`InfoPopover`'s own `onKeyDown` toggled on Enter/Space. MUI's ButtonBase also
clicks a non-native (`component="span"`) button on Enter's keydown and Space's
keyup, and that click toggled it back. With the fix the spec passes at both
widths: Enter opens, Enter closes. The spec saves a clip of the open popover
per width (`popover-1440.png`, `popover-390.png` in its output). The 390 px
clip shows the description wrapping inside the viewport.

## Bugs fixed on this branch

1. `fix(capabilities)`, `pkg/model/capability_catalogue.go`. `judgeAPI` also
   applies an `in_process` entry kept under a retired name whose replacement
   is the `api:` grant being judged, including `deployment-health`'s
   `api:deployment:read` half, and names the entry in the refusal.
   Regression: `TestCatalogueRetiredInProcessBindsItsPreset` (6 of 8 cases
   fail without the fix).
2. `fix(sandboxcli)`, `pkg/sandboxcli/{task,help}.go`. `memory write` refuses
   a bare key followed by text. The help for `memory`, `memory read` and
   `memory write` now says what they do. Regression: the new cases in
   `TestStage3SandboxCLICommands`.
3. `fix(quarantine)`, `pkg/gitproxy/cli_actions.go`, `pkg/sandboxcli/task.go`.
   `CLIQuarantineResult.FollowUpError`, reported on stderr with a non-zero
   exit. Regression: `TestPerAttemptTokenBindingAndStage3CLIActions` (fails
   without the fix) and `TestQuarantineSaysWhenItsFollowUpWasNotFiled`.
4. `fix(ui)`, `ui/src/components/InfoPopover.jsx`. Only `stopPropagation` on
   Enter/Space, leaving the toggle to `onClick`. Regression:
   `InfoPopover.test.jsx` (fails without the fix) and
   `e2e/capability-info-popovers.spec.js`.

## Not covered / left for follow-up

- The quarantine success path and a repository's held repair proposal were
  not run here (no reachable repository). CI covers them.
- Pickers not re-walked on this daemon: New task, template/schedule editors,
  repo defaults. Task 2012 walked them on demo.
- The `POST /api/tasks` response does not report dropped defaults (gap 3).
  The refusal of grain's own policy is labelled as untrusted capability text
  (gap 2). Both are small UX follow-ups.
- The upgraded catalogue keeps the retired keys in `in_process`. They now
  bind, but Settings → Capabilities does not show them as belonging to the
  presets, and nothing moves them to `api_scopes.presets`.
