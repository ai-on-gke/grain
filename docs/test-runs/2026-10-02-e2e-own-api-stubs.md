# ui/e2e: stubs of grain's own API, removed and remaining

**Date**: 2026-10-02
**Branch**: `grain/task-1508`, based on `main` at `40ddd261c`
**Environment**: Debian sandbox, Linux x86_64, Go (repo toolchain), Node 20, Playwright / Chromium headless shell 151
**Server**: `grain demo -addr 127.0.0.1:0 -open=false` (with `-observability` where the spec asks for it), one per Playwright worker, over its throwaway embedded SQLite store and the `npm run build` frontend in `pkg/ui/static`

This follows the tests/ui-e2e code review (task 1400). The protocol is that Playwright runs against the real `grain demo` and stubs only external boundaries. The specs below were fulfilling grain's own endpoints from fixtures, so they tested React against shapes that could drift from the server, and no server regression could fail them.

## What changed

### `deployment_operations_walkthrough.spec.js` (`-observability` demo)

| Was stubbed | Now |
|---|---|
| `/api/sandboxes` (rich fixture, a 500, `enabled:false`) | A held CI run puts a real host-directory sandbox on the pane. The test checks the `ci` row, then finishes the run and waits for "Nothing in flight". The 500 was fault injection and is dropped. The "not available" state is now asserted in `paths.spec.js` on the plain demo, which really has no sandbox backend. |
| `/api/ci/cache`, `/api/ci/runs*`, `/api/ci/runs/<id>` | Real runs. One is started from the CI pane's own form and cancelled through the real `DELETE /api/ci/runs/{id}`: dismissing the confirm leaves it running, and accepting it ends `cancelled`. Another is started with `POST /api/ci/runs` and finished with `/api/e2e/ci/finish`, and its job → step → output (`observed through the sandbox`) is inspected on `/ci/<id>`. |
| `/api/tasks/2` rewritten to carry a CI run | `POST /api/ci/runs {taskId: "2"}` records a real verdict on task 2. The demo's workflow answers only `workflow_dispatch`, so that push run errors and says so. "Show the run" opens it and the error text is asserted. |
| `/api/config` rewritten to `restart/reboot/shutdownEnabled` | The demo now wires power hooks that refuse rather than act (`refuseDemoPower`), so the Power tab offers its buttons for real. |
| `/api/host/top*` | The demo now wires `HostTop` to a fixed snapshot (`demoTopLines`), the same way `HostStats` is already fixed. |
| `/api/pipeline-pause` | The real store-backed pause. The test checks it with `GET /api/pipeline-pause` after each dismissed and accepted confirm, and ends resumed. |
| `/api/config` rewritten to carry a pipeline pause (tablet banner) | A real `POST /api/pipeline-pause`, lifted in a `finally`. |
| `/api/secrets`, `/api/secrets/**` | The demo now wires a real `secrets.Store` in its throwaway directory. The test sets a fake secret on Admin → Integrations & Auth, sees it listed by name and key (never by value, and also checked via `GET /api/secrets`), and deletes it. The old stubs never took effect: the test looked for the form on Team Settings, where it isn't, and skipped itself. |
| `/api/models/gemini` | Removed. No such route exists in `pkg/ui` and nothing in `ui/src` calls it. |
| `/api/metrics*` (a 500) | Removed (fault injection). The test now checks that Refresh re-reads the real `GET /api/metrics` and the sections still draw. |

Each held run names a branch of its own (`ops-walkthrough/...`). The job cache keys on the ref, so a finished `main` run would turn the next `main` run into a cache hit: this file's cancel walkthrough, or `observability.spec.js`'s "uncached" run when both land on one worker's demo. `observability.spec.js` now counts sandbox acquires relative to where its test started, for the same shared-worker reason.

### `dependency_automerge_revert_walkthrough.spec.js` (plain demo)

`/api/merge-batches` was a fixture. Merge batches are rows only the merge queue writes, and there is no API to create one. So `seedDemo` (`cmd/grain/demo.go`) now writes one in-flight batch on `acme/widgets` `main` through `model.Store.PutMergeBatch`. Its members are the seeded #101 ("Rotate the Gemini signing key") and #102 ("Cache the capability readiness probe"). The spec asserts the panel's "2 changes on main", the `grain-merge-queue/main` ref, the "halved towards this end" marker, and member order, all read through the real `GET /api/merge-batches`. The spec's old "No batch is in flight" check on that repo no longer holds and is gone. The full chromium suite (238 tests) passed with the seeded batch in place.

### `phone.spec.js` (two tests: phone and tablet branch suggestions)

The stub matched `/api/repos/{owner}/{name}/(branches|refs)`, which is grain's **own** store-backed branch list. `/github-branches`, the GitHub boundary, is a different path the regex never matched. Both tests now record the wide branch through the real `POST /api/repos/{owner}/{name}/branches` before opening the field. The demo has no branches reconciler, so the branch stays recorded. The GitHub half answers 404 "not available" on a demo, and `BranchField` already treats that as no suggestions.

### `responsive-navigation-walkthrough.spec.js`

- The tablet test's `/api/config` pipeline-pause rewrite is now a real pause, lifted at the end.
- The Director/Operations session test stubbed `GET /api/console-sessions`. That listing is store rows, not a live session, so it was an own-API stub. The demo seeds the Director, and the test now creates "Operations" through the real `POST /api/console-sessions`.

## Stubs that remain

See "Every stub that remains" at the end of this report. That table is the full census after the follow-up below.

## Verification

- `npx playwright test --project=chromium` over the whole suite: 238 passed. This was after the merge-batch seed and before the phone and responsive edits, which were then run on their own: 39 passed.
- The six `-observability` specs (`ci_workflows`, `deployment_operations_walkthrough`, `observability`, `system-overlay-tabs`, `team-concurrency-dispatch-walkthrough`, `team_declarations_ci_walkthrough`) were run in one worker, so this file's runs share a demo with `observability.spec.js` ahead of it: 15 passed.
- `go test ./cmd/grain`: passed.

## Follow-up: the rest of ui/e2e (task 1569)

**Branch**: `grain/task-1569`, based on `main` at `728c71d4d`. Same environment and server as above.

This reviews every `page.route` the first pass listed as outside its scope, plus its one leftover ("Stacked banners…").

### What changed

| Spec | Was stubbed | Now |
|---|---|---|
| `responsive-navigation-walkthrough.spec.js` → new `stacked-banners.spec.js` | `/api/config` overlay of `modelsConfigured:false`, `gitCredentialsConfigured:false`, `agentPause`, `pipelinePause` | Two new demo flags. `-unconfigured` clears the stored agent models and the git credential set, as on a fresh install. `-agent-paused` wires a real `orchestrator.Pause`, begun through `Pause.Begin` with a provider `UsageLimitError`. The pipeline pause is a real `POST /api/pipeline-pause`, lifted in a `finally`. `demoArgs` is worker-scoped and cannot be set inside a describe block, so the test moved to its own file. Both setup banners now speak for the selected team (grain/task-1592), so the test selects the default team. It first checks on the real `GET /api/config` that this team has no model connection and no GitHub credential (with no credential set, `MigrateTeamGitCredentials` has nothing to give it), and that the agent pause is in force. It stays on the WebKit project too (`playwright.config.js`'s `testMatch`), as it was before. |
| `shell.spec.js` | `/api/config` overlay of `pipelinePause` | A real `POST /api/pipeline-pause` in each banner test, lifted by an `afterEach`. |
| `deployment-operations-navigation.spec.js` | `GET /api/settings` rewritten to mark `gemini-key` checkable | Every demo now wires `ui.Config.CapabilityChecks` (`demoCapabilityChecks`, which refuses: there is no credential to test), so the real `/api/settings` reports `checkable`. `POST /api/capabilities/gemini-key/check` stays stubbed, with a comment: its verdict is Google's answer about the key. |
| `provider-connection-lifecycle-walkthrough.spec.js` | `GET /api/executors` fixture | Every demo now wires `ui.Config.Executors` to a fixed snapshot of two executors (`demoExecutors`: `runner-infra-worker-01` on team infra, `runner-frontend-worker-01` on team frontend), in place of `modelexec.Server`'s live connections. The old fixture had already drifted from the wire shape: it sent `serverUrl` where `modelexec.Status` sends `server_url`. |
| `recurring-composed-walkthrough.spec.js` | `POST /api/templates` → injected 503 | A real refusal. The template is bound to `acme/widgets` on base `release..broken`, the server answers 400 (`validateBase`), and the test checks that the form shows the server's own sentence, that Cancel works and that no template was filed. |
| `diff-render.spec.js`, `upgrade.spec.js`, `ui-navigation-split-button-upgrade.spec.js` | unchanged | A comment at each stub saying why it stays. |

`cmd/grain/demo_scenarios.go` holds the four stand-ins. `TestDemoScenariosRaiseTheSetupAndPauseBanners` (`cmd/grain/demo_test.go`) asserts the flags on the server's own `/api/config`.

### Fault injection: the rule this change applies

The first pass kept one injected failure (`GET /api/console-sessions` → 503) and flagged it for a human in the review of PR #2643. The review comment itself couldn't be read from the sandbox (no GitHub API), so the rule below is this change's own and is open to review:

- An injected failure stays only when the failure is the subject of the test **and** a healthy `grain demo` cannot be made to produce it. Only the one failing response is fabricated. Everything after it reaches the real handler.
- Where the real server has a real refusal that drives the same UI path, the test uses that instead. Templates is the case converted here.
- Failures that are not the test's subject are dropped, as the first pass did with `/api/sandboxes` and `/api/metrics`.

Under that rule, `error-boundaries.spec.js` keeps both of its stubs. A wrong-shaped `/api/schedules` body and an HTML sign-in page answering for `/api/config` are both, by construction, answers grain itself never gives. The second is literally a proxy in front of grain: an external boundary.

## Every stub that remains

`grep -n "page.route" ui/e2e/*.js` after this change:

| Spec | Route | Kind | Why it stays |
|---|---|---|---|
| `deployment_operations_walkthrough.spec.js` | `POST /api/host/restart`, `/reboot`, `/shutdown` | destructive | The real answer is the deployment going away. The demo's hooks refuse. |
| `deployment_operations_walkthrough.spec.js`, `upgrade.spec.js`, `ui-navigation-split-button-upgrade.spec.js` | `/api/upgrade`, `DELETE /api/upgrade/scheduled` | destructive | A real upgrade rebuilds or re-pulls grain and restarts onto it. The demo wires no `Upgrader`. |
| `deployment-operations-navigation.spec.js` | `POST /api/capabilities/gemini-key/check` | external (Google) | The verdict is the issuer's answer about a key the demo does not have. |
| `provider-connection-lifecycle-walkthrough.spec.js` | `GET /api/agent-models*` | external (agent CLI / vendor) | A daemon's lister runs `agy models` with the connection's credential. The demo has neither binary nor key, and the test needs both a catalog and a refused key. A fixed demo lister could give the first but not the second without keying off a connection's name. |
| `diff-render.spec.js` | `GET /api/repos/{owner}/{name}/compare` | external (GitHub) | GitHub's compare API, and the demo has no repository behind it. |
| `responsive-navigation-walkthrough.spec.js` | first `GET /api/console-sessions` → 503, then `route.fallback()` | fault injection | The subject of "Session manager error state". See the rule above. |
| `error-boundaries.spec.js` | `GET /api/schedules` → wrong shape; `/api/config` → HTML sign-in page | fault injection | The subject of both tests, and no healthy grain answers either way. See the rule above. |

### Verification (task 1569)

- The ten affected specs (`shell`, `stacked-banners`, `deployment-operations-navigation`, `provider-connection-lifecycle-walkthrough`, `recurring-composed-walkthrough`, `responsive-navigation-walkthrough`, `diff-render`, `upgrade`, `ui-navigation-split-button-upgrade`, `error-boundaries`), `--project=chromium`: 40 passed.
- `stacked-banners.spec.js`, `--project=webkit`: 1 passed.
- `go test ./cmd/grain -run 'TestDemo|TestSeedDemo'`: passed.
- Full `--project=chromium` suite: 238 passed, 1 failed. The failure was `team-settings-github-tokens-walkthrough.spec.js` and predates this change. It finds its new team's card by the text "Platform Engineering", with `.first()`. `team-navigation-scoping.spec.js` creates an "Infrastructure" team described as "Cloud & Platform Engineering", so when the two files share a worker's demo, the wrong card comes first. Adding a spec file reshuffles which files share a worker, which is how this surfaced here. It reproduces on the unchanged spec with `--workers=1 team-navigation-scoping.spec.js team-settings-github-tokens-walkthrough.spec.js`. The fix matches the card by its heading instead. That pair now passes (5 passed).
