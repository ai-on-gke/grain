# Walkthrough: access forms after the Legacy capabilities picker removal, and `https://grain.dev/capabilities/api` references (2026-11-09 §1)

Plan: [`docs/test-plans/2026-11-09.md` §1](../test-plans/2026-11-09.md). Task 3728, filed by [Task #3713](/tasks/3713). Run 2026-11-09 04:05–04:40 UTC in a grain sandbox against `main` at `4d7d6373`. Feature commits: `25b1d0df` (PR #985), `daa9bd53` (PR #940), `e8fa6a2d`, `6fb3ef59`, `795f2465` (PR #971).

**Outcome: items 1, 2, 3, 5 and 6 were walked against a real `grain daemon`, and item 4's live half was too. One bug was found and fixed (F1), with a Go regression. Four stale doc passages were fixed (F3). One divergence (F2) and one UI gap (O1) are filed as follow-ups, not changed here. 20/20 scripted API/CLI/MCP checks, the browser round trip over all six forms, and 24/24 layout checks pass on the fixed build.**

## Environment

- **Build.** `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain` from `HEAD` (Go 1.26.10, Node 20.19.0). Rebuilt after the F1 fix.
- **Daemon.** [`env.sh`](2026-11-09-access-forms-api-grants/env.sh) starts a real `grain daemon -sandbox-port-base=0 -disable-native-sandboxes -ci=false` on `127.0.0.1:4295`. It is control plane only, with a fresh SQLite store per script. Requests authenticate with the shared access code.
- **GitHub.** GitHub is played by [`ghsim.go`](2026-11-09-access-forms-api-grants/ghsim.go) (`pkg/github/githubsim` over HTTP on `:4296`), which serves `acme/widgets` on `trunk`. A git credential is imported with `grain connections import` so tasks may target the repository.
- **Scripted agent.** `env.sh`'s `propose` speaks MCP to the **real** `grain mcpserver -interactive -server … -task <id>` and calls the real `propose_task` tool. That is the path a live run's proposals take (`ui.Client.CreateTask` with `ProposedBy`).
- **Held bare ids.** Every write path now rewrites a retired alias (`self-debug` becomes `api:self-debug` + `https://grain.dev/capabilities/grain#source={}`). So [`item1_seed.sh`](2026-11-09-access-forms-api-grants/item1_seed.sh) files each object through the API and then stops the daemon. It writes a bare `self-debug` row straight into `task_grant`, `template_grant`, `schedule_grant`, `grain_config.default_capabilities` and `repo_config.default_capabilities`, and `release-gate-read` into `repo_config.ci_capabilities`, the way an older store still holds them. Then it restarts the daemon.
- **Browser.** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). [`ui_walkthrough.mjs`](2026-11-09-access-forms-api-grants/ui_walkthrough.mjs) is the round trip, [`layout_check.mjs`](2026-11-09-access-forms-api-grants/layout_check.mjs) the geometry, and `ui/e2e/screenshot.mjs --url` the screenshots.

Reproduce:

```sh
D=docs/test-runs/2026-11-09-access-forms-api-grants
bash $D/items_api.sh                                  # items 2, 3, 4 (live), 5 -> items-api.out
bash $D/item1_seed.sh && cd ui &&                     # item 1 seed, then the browser half
  node ../$D/ui_walkthrough.mjs http://127.0.0.1:4295 "$(cat /var/tmp/access-forms/data/local-api-token)" ../$D/seeded-ids.json ../$D/ui-results.json
(cd $D && source env.sh && WORK=/var/tmp/access-forms CODE=... BODY=/tmp/b.json snapshot)   # after.json
node ../$D/layout_check.mjs http://127.0.0.1:4295 "$CODE" ../$D/seeded-ids.json ../$D/layout-results.json
```

In this sandbox a daemon started by one `run_command` is stopped when that command ends, so the seed and the browser script must run in one command.

## Findings

| # | Finding | Resolution |
|---|---|---|
| **F1** | **`POST /api/tasks` refused `capabilities: [{capability: read-issues}]` with a Go decoding error.** The body was `{"error":"json: cannot unmarshal object into Go struct field .Alias.capabilities of type string"}`. That blames Go's struct layout rather than saying what to write. The identical `grants:` entry, and both spellings through `propose_task`, say `capability grant "read-issues": a name that is not a capability reference (https://...) is written as a plain string, "read-issues", not as a {capability, with} object` (`795f2465`). `CreateTaskRequest` decoded `capabilities` as `[]string`, while `propose_task` reads it with `model.CapabilityEntries`, which takes strings and `{capability, with}` objects. | **Fixed** in `pkg/ui/client.go`. `CreateTaskRequest.UnmarshalJSON` reads `capabilities:` with `model.CapabilityEntries`, as `propose_task` does. A wrapped bare name now gets 795f2465's message as a 400 validation error (check 5.2). A real `{capability: <ref>, with: {...}}` is read as the `<ref>={json}` string the same field already accepted, so nothing new becomes grantable (check 3.3). `[]` still means no capabilities, and an absent or `null` list still means the defaults. Regression: `TestCreateTaskRequestCapabilitiesReadAsProposeTaskReadsThem` (`pkg/ui/request_grants_internal_test.go`), which fails without the fix. |
| **F2** | **A bare label proposed live and the same proposal relayed at run end are stored differently.** `propose_task {capabilities: ["self-debug"]}` filed live (`ui.Client.CreateTask`) stores `api:self-debug` + `https://grain.dev/capabilities/grain#source={}` (check 4.1). Since `e8fa6a2d`, `orchestrator.relayProposedTasks` applies `model.StoredGrantSpelling` alone and stores `self-debug` (or `self-repair`) unexpanded. `TestRelayProposedTasksUnifiedGrantsEquivalenceAndNormalization` pins that. A proposal the relay files (one the live call missed) therefore holds a different grant set from its live twin. Item 4 asks for both properties, unexpanded **and** the same as live, and they cannot both hold. | **Not changed:** `e8fa6a2d` chose this deliberately, and `pkg/orchestrator/finish.go` is a classified path. Filed as a follow-up to decide which side is right. No real dispatched run was possible here (the sandbox has no agent backend), so the relay half rests on the existing Go test. |
| **F3** | **Stale "still to be removed" copy.** `docs/capabilities.md` (two places) and `design/design/unified-grants.design.md` still said the task forms' empty Legacy capabilities checklist "is still to be removed". `design/design/config-repo-settings.design.md` still listed a `legacyCapabilities` "Legacy capabilities" select on the New task form. | **Fixed.** The docs say the checklist, `POST /api/capabilities/{id}/check` and `grain settings -check-capability` were removed in PR #985. The form-field table row now says the id is read as an alias of `grants` (`model/new_task_form.go`, `newTaskForm.js`). No reference to `-check-capability` or `-legacy-capability` remains outside `docs/test-*` and `docs/releases`. |
| O1 | **A held bare id is invisible, so it cannot be removed in the UI.** On every form, `GrantsPicker` shows only API grants and capability references. A held `self-debug` has no line of its own, by design ("a stored legacy id has no picker of its own and is saved back as it is"). On the template, schedule, Settings → Tasks and repo-defaults forms, the first save sends it back and the server rewrites it to its canonical pair, which then *does* appear as two chips (`after.json`). On the task pane, saves go through `POST /api/tasks/{id}/capabilities` one grant at a time, so the bare id stays stored, unseen and unremovable for the life of the task. | Filed as a follow-up: show a held bare id as a removable read-only line, or migrate it. Not changed here: it is a design decision about one-release aliases. |
| O2 | `release-gate-read` cannot be put on a repository's CI list: `PUT /api/repos/acme/widgets/ci-capabilities {"ciCapabilities":["release-gate-read"]}` answers 400 `capability "release-gate-read" is not supported for CI`, and `availableCICapabilities` does not list it. `pkg/ui.capabilityProviderCatalog` (which `ciCapabilityIDs` walks) has no `ciapi` alias. `docs/ci-capabilities.md` says the alias "is granted the older way, from this repository's list or by declaring the alias". Grain's own `.grain/workflows/release-train.yml` uses the declaration. | Pre-existing: PR #985 did not touch `ciCapabilityIDs` or the catalogue. Recorded only. A held `release-gate-read` planted in the store survives every form untouched (`after.json` `repoCI`). |
| O3 | `grain settings -check-capability x` and `grain demo -legacy-capability` both exit **2** and print `flag provided but not defined: -<flag>` twice, once above the usage text and once (`grain:`/`grain demo:`-prefixed) below it. | As designed for the flag package plus grain's own error line. Recorded only. |
| O4 | The repo CI and defaults routes are separate: `PUT /api/repos/{o}/{n}/capabilities` replaces `defaultCapabilities` only, so a body carrying just `ciCapabilities` clears the defaults. CI goes through `PUT …/ci-capabilities`. | As designed (each route replaces its own list). Noted because the plan names only the first. |

## Item 1: held bare ids round-trip through every form that lost its picker

[`item1_seed.sh`](2026-11-09-access-forms-api-grants/item1_seed.sh) writes [`before.json`](2026-11-09-access-forms-api-grants/before.json). [`ui_walkthrough.mjs`](2026-11-09-access-forms-api-grants/ui_walkthrough.mjs) then opens each form in Chromium at 1440×900, ticks the unrelated `tasks:write` scope with `GrantsPicker`, saves, unticks it, saves and reloads. The results are in [`ui-results.json`](2026-11-09-access-forms-api-grants/ui-results.json) and the stored state afterwards in [`after.json`](2026-11-09-access-forms-api-grants/after.json).

| Form | Sent back (request body) | Stored before → after | Verdict |
|---|---|---|---|
| Task pane (`/tasks/2`, Access fieldset) | `POST /api/tasks/2/capabilities {"id":"api:tasks:write","attach":true/false}` | `api:ci-read@acme/widgets, api:tasks:read, self-debug` → unchanged | kept bare (O1: unseen) |
| Template editor | `PATCH /api/templates/template-1` `capabilities: ["self-debug", …, "api:tasks:write"]` | `…, self-debug` → `…, api:self-debug, grain#source={}` | sent unchanged; server canonicalised once, not duplicated |
| Schedule editor | `PATCH /api/schedules/sched-1` `capabilities: ["self-debug", …]` | same as template | same |
| Settings → Tasks defaults | `PUT /api/settings {"defaultCapabilities":["self-debug","api:tasks:read","api:tasks:write"]}` | `self-debug, api:tasks:read` → `api:self-debug, api:tasks:read, grain#source={}` | same |
| Repo default capabilities (`/repos/acme/widgets#agents`) | `PUT /api/repos/acme/widgets/capabilities {"defaultCapabilities":["self-debug","api:ci-read@acme/widgets","api:tasks:write"]}` | `self-debug, api:ci-read@acme/widgets` → `api:self-debug, api:ci-read@acme/widgets, grain#source={}` | same |
| Repo CI capabilities | not edited | `release-gate-read` → unchanged | kept |

No form dropped the held id, and none stored it twice (bare beside canonical). `tasks:write` came and went cleanly everywhere. No "Legacy capabilities" text renders on any of the six forms (`legacy-heading` count 0).

## Item 2: no dangling UI or CLI for the removed route and flags

[`items_api.sh`](2026-11-09-access-forms-api-grants/items_api.sh), output in [`items-api.out`](2026-11-09-access-forms-api-grants/items-api.out):

- `POST` and `GET /api/capabilities/release-gate-read/check` answer **404** `{"error":"no such API endpoint: POST /api/capabilities/release-gate-read/check"}`, not 500 (2.1, 2.2).
- `grain settings -check-capability x` and `grain demo -legacy-capability` exit **2** with `flag provided but not defined: -check-capability` / `-legacy-capability` (2.3–2.6, O3).
- **Network.** `ui_walkthrough.mjs` watched every request across the six forms, `/settings/capabilities` (capability-server cards and Deployment credentials), `/settings`, `/setup` and `/repos/acme/widgets#ci`: **0** requests to `/api/capabilities/*/check`, and no page errors.
- **Docs.** `grep` over `docs/` and `design/` (excluding `docs/test-*`, `docs/releases`) finds no `-check-capability` or `-legacy-capability`. Four "Legacy capabilities … still to be removed" passages were stale and are fixed (F3). The remaining mentions are history: the retired Settings tab, the security audits and the alias note in `config-repo-settings.design.md` §556.

## Item 3: both API grant spellings, end to end

`items_api.sh` 3.1–3.9, all PASS. In every path the stored grants are exactly `api:ci-read@acme/widgets,api:tasks:read`. Inputs were `https://grain.dev/capabilities/api#tasks:read` and `https://grain.dev/capabilities/api#ci-read` with `{repo: acme/widgets}`, sent through:

- `POST /api/tasks` with `grants:` (string and `{capability, with}`), with `capabilities:` (`ref={json}` strings), and with `capabilities:` objects (after F1);
- `grain create -capability …` (`grain create -h`: `-capability` takes "a capability reference (https://...[#method]), or a reference with its arguments as ref={json}");
- `propose_task` with `grants:` and with `capabilities:`;
- `POST /api/templates`, `POST /api/schedules`;
- `PUT /api/console-sessions/director`.

In the browser (`ui-results.json`), each form shows **one** chip per grant, with `data-grant` values `api:ci-read@acme/widgets` ("api:ci-read on acme/widgets") and `api:tasks:read`. No `grain.dev ›` generic method chip appears beside them in the task pane, the Template and Schedule overlays, Settings → Tasks, the RepoPage defaults, the New task overlay (pre-ticked defaults) or the Console session page (Director). Clicking **Remove api:tasks:read** on the task pane removes the stored grant (`after.json` `task`: `api:ci-read@acme/widgets, self-debug`).

`GET /api/capability-methods` lists 65 `https://grain.dev/capabilities/api#*` rows beside 17 server methods (3.9). The UI keeps them out of the generic group (`api-grant-picker.spec.js` covers that on a real server). The empty state when they are the only rows is now pinned by a new Vitest, `GrantsPicker.test.jsx` "says there is nothing to grant when api references are the only methods": the menu says "Nothing to grant", and with nothing held and nothing to offer the picker is not drawn.

## Item 4: relay of bare labels

- **Live** (`ui.Client.CreateTask` through `propose_task`): `capabilities: ["self-debug"]` → `api:self-debug, https://grain.dev/capabilities/grain#source={}` (check 4.1). `grants: ["https://grain.dev/capabilities/api#tasks:read"]` → `api:tasks:read` (3.5).
- **Run end** (`relayProposedTasks`): not reproducible on a real dispatch here, because the sandbox has no agent backend and `-disable-native-sandboxes` runs no sandbox. The relay is pinned by `TestRelayProposedTasksUnifiedGrantsEquivalenceAndNormalization` to store a bare label unexpanded (`self-repair`) and the API reference as `api:tasks:read@acme/app`. So live and relay agree on API references and **disagree on bare labels** (F2).

## Item 5: refusal copy

`items_api.sh` 5.1–5.4, all PASS on the fixed build, each verbatim:

| Path | Answer |
|---|---|
| `POST /api/tasks` `grants: [{capability: read-issues}]` | 400 `{"error":"grants: capability grant \"read-issues\": a name that is not a capability reference (https://...) is written as a plain string, \"read-issues\", not as a {capability, with} object"}` |
| `POST /api/tasks` `capabilities: [{capability: read-issues}]` | before F1: 400 `json: cannot unmarshal object into Go struct field .Alias.capabilities of type string`; after: 400 `capabilities: capability grant "read-issues": …` (same text) |
| `propose_task` `grants:` / `capabilities:` | the tool's answer is the same message, prefixed `grants:` / `capabilities:` |
| New task form YAML/JSON import | none exists (`NewTaskOverlay.jsx` has no import), so there is nothing to check |

## Item 6: visual check

`ui/e2e/screenshot.mjs --url http://127.0.0.1:4295 --full-page` shot `/tasks/2`, `/processes/templates/template-1`, `/processes/schedules/sched-1`, `/settings/tasks` and `/repos/acme/widgets#agents`, and `/` with the New task overlay open (`--click 'role=button[name=/New task$/]'`). Each page was shot at 1440×900 and 390×844, light and dark: [`contact-sheet.png`](2026-11-09-access-forms-api-grants/contact-sheet.png) and [`contact-sheet-new-task.png`](2026-11-09-access-forms-api-grants/contact-sheet-new-task.png).

This sandbox's file reader shows PNGs as bytes, and `LOOK=1` needs a Gemini key it does not have. So the look was measured instead. [`layout_check.mjs`](2026-11-09-access-forms-api-grants/layout_check.mjs) gave **24/24 PASS** ([`layout-check.out`](2026-11-09-access-forms-api-grants/layout-check.out), [`layout-results.json`](2026-11-09-access-forms-api-grants/layout-results.json)). Per form, width and theme it checks:

- **No empty slot where the picker was.** The next element sits 3–18 px below the grants group: the helper text on Settings → Tasks, RepoPage and New task; "Depends on" on the task pane; the action row on the template; "Repeat" on the schedule.
- **Task pane.** The **Access** fieldset holds the grants group and its 37 px legend and padding, nothing else.
- **No overflow.** There is no horizontal overflow, and the grants stay inside the viewport.
- **No leftovers.** No "Legacy capabilities" text, and no heading left with nothing under it.

The catalogue-refusal note's position under the grants at both widths is asserted on every CI run by `new-task-catalogue-defaults.spec.js`. Nothing in `ui/src` changed in this walkthrough, so there are no before/after pairs: the sheets are the current state.

## Follow-ups filed

- F2: decide whether a relayed proposal's bare label is expanded like its live twin (`pkg/orchestrator/finish.go` vs `ui.Client.CreateTask`).
- O1: a held bare id is invisible and unremovable on the task pane's Access fieldset (and invisible on the other forms until their first save canonicalises it).
