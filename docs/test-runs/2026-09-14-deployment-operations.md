# Deployment settings and operations walkthrough, 2026-09-14

## Result

Pass after four scoped UI corrections. The clean-store headed walkthrough completed 28/28 scenarios, and a focused headed follow-up completed both task-to-CI and table-to-CI navigation scenarios. There were no unexpected browser console errors, page errors, or failed requests in either final pass. No real credential was stored and no host, pipeline, CI run, upgrade, shell, reboot, restart, or shutdown operation reached a system boundary.

The headed UI walkthrough tested commit `ef8fad49c0bfa67c8037be29619fb0707b45e96d` (`Harden deployment operations controls`), based on `4bb9453c8d3e0b35a19361fed9d652b5e0b01287`. The branch was then integrated with `origin/main` at `018c9734c`, rebuilt, and rerun as noted below. A final base sync to `dee311a0c` changed daemon/deployment tests and setup only, not the frontend; the resulting tree is covered by the final full Grain CI run.

## Environment and method

- Debian GNU/Linux 12, Linux 6.1.0-50-amd64 x86_64.
- Go 1.26.6 and Node 20.19.0.
- Playwright 1.62.1 with Chrome for Testing 151.0.7922.34.
- The repository's production frontend was built with Vite, the real Grain binary was built, and `./bin/grain demo -addr 127.0.0.1:18142 -open=false` served it from a throwaway embedded store.
- Chromium ran headed under Xvfb. The main pass used desktop, narrow, and 820x1180 tablet viewports. All actions below were visible browser gestures against the built frontend and real server.
- The supplied Gemini credential was used only to have Gemini 2.5 Flash challenge the exploratory checklist for missed operator edge cases. It was read from its protected file at request time and was never printed, written into Grain, logged, or added to the repository.
- Browser route interception was limited to unavailable demo host/GitHub/upgrade/CI/secret-validation boundaries and synthetic failure or empty states. The complete ledger is below.
- No screenshots, traces, videos, Playwright output, embedded stores, credentials, or browser artifacts are committed.

The bare `grain demo` store has no deployment settings row, whereas the daemon creates that row from its startup flags. The walkthrough first used General's visible preview with an invalid duration and confirmed that the server refused the incomplete first save and wrote nothing. It then made one browser-origin, non-stub `PUT /api/settings` with the harmless daemon baseline needed to exercise per-tab saves: `pollInterval=30s`, `maxWorkers=2`, `maxMergers=1`, `maxMergesPerRepo=1`, `maxCIJobs=1`, the standard Gemini and Claude model IDs, and `githubHost=github.com`. No credential material was included.

## Coverage and outcomes

| Area | Browser exercise | Outcome |
| --- | --- | --- |
| Settings navigation | Opened General, Models, Memory, GitHub, Sandbox, Capabilities, Config repo, and Upgrade. At 700px the tab strip measured 355px client / 749px scroll width; selecting Upgrade scrolled it into view. Arrow, Enter, and End navigation also reached and activated tabs. | Pass |
| Browser-local controls | Selected Dark appearance and Oldest-first timeline order, checked local storage and the applied theme, then hard-reloaded Settings. | Both choices persisted locally. |
| General | Exercised invalid preview, a valid preview, dirty state, Reset, Save, return navigation, and hard reload. Final harmless demo values included environment `task-42-ops-2`, a 45s poll interval, and UTC. | Validation refused without writing; reset cleared dirty state; save persisted. |
| Models | Added/selected a Gemini backend named `Task 42 Gemini`, chose `gemini-2.5-pro`, effort `high`, and 20 turns, then saved and reloaded. | Persisted. Gemini effort choices remain available when demo model discovery reports an empty unavailable catalog. |
| GitHub | Changed the host to `github.example.invalid` in the throwaway store, previewed/saved it, observed the restart-only explanation, and reloaded. | Persisted; no GitHub call or credential use. |
| Sandbox settings | Set 3 CPUs, 4096 MiB memory, 40 GiB disk, network `none`, and a harmless demo-only bootstrap script, then saved/reloaded. | Persisted; restart implications remained visible and no sandbox was built. |
| Capabilities, secrets, state, memory | Inspected capability status and unavailable secret/config-repository states. Opened memory documents, edited one, and used Revert so the throwaway content was not left changed. Submitted only a fake secret to a rejecting validation stub. | States and validation rendered correctly; no credential stored. |
| Upgrade | Inspected the real unavailable state and immediate/hold planning choices. With the unavailable upgrader stubbed, one POST showed a running state and disabled repeat submission. | Pass; no image, service, or host changed. |
| Setting notices | Injected pending-restart, unrunnable-model, and host resource-commitment metadata while keeping the real settings UI. | All warnings were visible and distinguishable. |
| Sandboxes | Observed initial loading, host CPU/memory/swap pressure, nested virtualization, disk use/error, pinned image drift, pool status and errors, CPU/memory/disk trends, refresh change from 88% to 92%, and 24 long rows. Scrolled the panel both axes; it measured 653px client / 1745px scroll width. | Pass. Separate unavailable, empty, and HTTP 500 states also rendered. |
| CI listing | Inspected active sandboxes, cache totals/hits, repo filter changes, and pending, running, success, failure, errored, cancelled, skipped, and cached vocabulary. | Pass. |
| CI detail and links | Opened runs, jobs, steps, collapsed log output, long wrapping output, errors, skipped/cache-hit steps, and pending activity. Opened output from the CI table and from task 2's `Show the run` link. | Task-origin overlay retained `/tasks/2`; table navigation created `/ci/task-link-run`; the latter survived hard reload. |
| CI controls | Dismissed Cancel run and confirmed no DELETE, then accepted it and observed exactly one intercepted DELETE. | Confirmation boundary behaved correctly; no real run mutated. |
| Logs | Used real demo log sources and tail, confirmed initial near-bottom position, searched case-insensitively, exercised the no-match message, cleared search, changed source, and refreshed. | Pass. Search filters only the current returned tail and does not issue a new request. |
| Sandbox health / Top / Root shell | Revisited sandbox health through the System navigation, refreshed synthetic Top data, disabled auto-refresh and refreshed manually, then inspected Root shell availability and its “no confirmation and no undo” warning. | Pass. No shell command was submitted; a defensive POST guard was registered but never hit. |
| Pipeline | Dismissed Pause and Resume confirmations and proved neither boundary was called. Accepted each in turn and observed paused/running UI and exactly one POST/DELETE. | Pass; both mutations were intercepted. |
| Power | Dismissed Restart and proved no POST. Accepted Restart (synthetic success), Reboot (synthetic connection drop), and Shutdown (synthetic HTTP failure). | Cancel, success/disconnection, and visible failure behavior passed; every POST was intercepted before the host. |
| Metrics | Inspected Throughput, Capacity, Latency, Backlog, task/rate/concurrency values, trend graphics, long values, task navigation, and Last 7/30/90 days. | Pass. Delayed empty data and HTTP 500 states also rendered. |
| Shell/keyboard pressure | At 820x1180 with a standing host banner, checked Settings remained inside the viewport and operated it by keyboard. | Pass after the sidebar correction. |

## Intercepted routes

Every request actually intercepted is listed here. All other settings, memory, logs, metrics, page assets, and browser navigation used the real demo server.

| Route | Why and response |
| --- | --- |
| `GET /api/config` | Twice: exposed demo-disabled System controls for safe UI exercise, and injected the standing host banner for the tablet check. |
| `GET /api/settings` | Once: overlaid restart-required, unrunnable-model, and host-commitment notices on the real settings response. |
| `GET /api/secrets` | Reported demo secret-store availability without exposing any value. |
| `PUT /api/secrets/demo/value` | Rejected the literal fake value to exercise validation/error UI; nothing was stored. |
| `GET /api/upgrade` | Supplied enabled idle/running states because demo has no upgrader. |
| `POST /api/upgrade` | Counted one request and returned running status; no upgrade implementation ran. |
| `GET /api/sandboxes` | Supplied the rich host/pool fixture because demo has no sandbox host; separate interceptions supplied unavailable, empty, and HTTP 500 responses. |
| `GET /api/ci/cache` | Supplied cache summary because demo has no CI executor. |
| `GET /api/ci/runs` | Supplied filterable pending/running/success/failure/errored/cancelled runs because demo has no CI executor. |
| `GET /api/ci/runs/:id` | Supplied jobs, steps, activity, cache, skipped states, errors, and log output for detail/deep-link exercise. |
| `DELETE /api/ci/runs/:id` | Counted the confirmed cancel and returned success; no real run changed. |
| `GET /api/tasks` and `GET /api/tasks/2` | Focused follow-up only: added a synthetic CI verdict to real demo task 2 so its visible `Show the run` link could be exercised. |
| `GET /api/host/top` | Supplied stable process output because demo has no host process reader. |
| `POST /api/pipeline-pause` | Counted the confirmed pause and returned paused state; demo pipeline was not mutated. |
| `DELETE /api/pipeline-pause` | Counted the confirmed resume and returned running state; demo pipeline was not mutated. |
| `POST /api/host/restart` | Returned synthetic success after confirmation; never reached the host. |
| `POST /api/host/reboot` | Aborted with a synthetic connection reset after confirmation; never reached the host. |
| `POST /api/host/shutdown` | Returned HTTP 500 with `intercepted demo shutdown failure`; never reached the host. |
| `GET /api/metrics` | Only the separate state checks: one delayed empty response and one HTTP 500. The main metrics traversal was real. |

A `POST /api/host/shell` safety guard was registered, but because the walkthrough intentionally did not submit a root command, no such request was made and it is not an intercepted route.

## Bugs found and corrected

1. **Fixed operator destinations could fall below a bannered tablet viewport.** Deterministic reproduction: use the built UI at 820x1180, inject `hostSandboxes: true`, and measure the Settings button; before the fix its bottom was at 1286px. The sidebar itself scrolled as one long rail, hiding Settings, System, and Metrics with the optional banner and growing primary list. The primary list now owns vertical overflow while the operator destinations remain fixed. `e2e/shell.spec.js` covers the bannered shell and the headed walkthrough explicitly checks/focuses Settings.

2. **Logs had tail and refresh but no search control.** Deterministic reproduction: open System -> Logs; there was no search field and no way to narrow the returned tail. The new case-insensitive client-side filter keeps source/tail behavior unchanged and distinguishes an empty source from no matching lines. `LogsPage.test.jsx` covers match, case, clear, no-match, and request count; the real demo log tail was exercised headed.

3. **Pipeline pause/resume crossed their mutation boundary without confirmation.** Deterministic reproduction: open System -> Pipeline and click Pause; the POST occurred immediately. Both actions now explain their effect in a confirmation dialog. `PipelineControl.test.jsx` proves dismissal issues no API call and the existing success/error paths still work; the headed pass counted dismissed and accepted POST/DELETE boundaries.

4. **An unavailable model catalog hid built-in Gemini effort choices.** Deterministic reproduction: have `/api/models/gemini` return `{available:false, models:[], efforts:[]}`; the empty array was truthy and shadowed the Settings vocabulary. Empty unavailable discovery now falls back to built-in efforts, while a nonempty catalog still wins. `BackendModelFields.test.jsx` covers the response and the headed pass saved/reloaded Gemini effort `high`.

The existing narrowed-path browser expectation was also stale after interactive tasks became a default exclusion. Its expected URL now includes `f=kind:none:interactive`; the behavior itself was correct.

## Regression runs

- Focused Vitest: 22/22 passed across `BackendModelFields.test.jsx`, `LogsPage.test.jsx`, and `PipelineControl.test.jsx`.
- Production frontend build: passed.
- Chromium regressions: 18/18 passed across `memory.spec.js`, `paths.spec.js`, `scroll.spec.js`, `shell.spec.js`, and `upgrade.spec.js`.
- Clean-store exploratory headed pass on the integrated tree: 28/28 passed, zero unexpected browser errors.
- Focused headed task/CI-link pass on the integrated tree: 2/2 passed, zero browser errors.

The first browser attempt could not launch because the stock Debian sandbox lacked Chromium's shared libraries and X authentication helper. This run installed Playwright's Chromium dependency set plus `xauth`; a sandbox image intended for repeated headed walkthroughs should bake those packages in.
