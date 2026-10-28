# Repository, release, and memory UI walkthrough — 2026-09-14

## Scope and environment

- Repository: `bwsalmon/grain`
- Walkthrough base: `a970d15040410ca7190acfc6b7f9975ba1afecc8`
- Fix exercised in the final production build: `5000e96ace23c34f33b0ffa550192b2fe4a94d2a`
- Branch: `grain/task-39`
- Host: Debian bookworm, Linux amd64
- Go `1.26.6`; Node `20.19.0`; npm `10.8.2`
- Playwright `1.62.1`, headless Chromium `151.0.7922.34`
- Server: the repository's built `grain demo -addr 127.0.0.1:8420 -open=false`, with a fresh embedded SQLite store under `/tmp`
- Gemini 2.5 Flash was used as an exploratory-test copilot and to inspect the narrow repository-list capture. The key was read from `/home/debian/.gemini-api-key` only inside the request process and was not printed, logged, written to the checkout, or committed.
- Browser captures, temporary drivers, binaries, and stores stayed under `/tmp`; none are committed.

The production frontend was built with `make frontend`, embedded in a freshly built `grain` binary, and opened by Playwright. All application reads and mutations below used visible React controls backed by the real demo server and its real API.

## Walkthrough

### Repositories and repo-scoped tasks

1. Opened Repos from the rail and directly at `/repos`. The rail count, heading count, one seeded row, per-state counts, and 10-task total agreed.
2. Exercised the first-target-repo warning twice: declined it and confirmed no mutation, then accepted it and observed the real demo's expected first-settings validation (`pollInterval is required the first time settings are saved`). The demo intentionally has no saved deployment-settings row.
3. Added two disposable known repos by opening their direct URLs and saving temporary repo prompt extensions: `acme/gadgets` and `example-corporation/exceptionally-long-repository-name-for-responsive-layout`.
4. Verified case-insensitive search, the no-match path, Custom order, Name (A–Z), Active first, and Most tasks. Non-custom sorts correctly removed reorder handles.
5. Reordered all three repos using HTML5 pointer drag, focused keyboard Enter activation, and touch `tap()` pickup/drop. Each custom order survived reload in the same browser profile.
6. At 390 px, the 73-character repo name stayed visible through ellipsis, rows and document had no horizontal overflow, and controls remained reachable.
7. Opened the widgets row, returned with the page's Repos button, opened the direct repo URL, followed its task count to the repo-filtered task list, and returned with browser Back.
8. Filed a disposable proposal from the repo page. `acme/widgets` was preselected. The form accepted and stored base `release/task-39`, writable `acme/gadgets` at `schema/task-39`, the long repo as read-only, and demo custom-field values. Returning to the repo and reloading increased its count; opening the task's direct URL and reloading preserved the base and both repository relationships.

### Repository configuration

1. Added `task-39/demo-branch`; it appeared as pending (the correct demo state without the GitHub branch reconciler) and survived reload.
2. Selected Gemini proxy for CI jobs and Memory as a task default, saved each independently, reloaded, and verified both chips.
3. Saved a temporary standing prompt extension and setup command, then reloaded and verified exact values.
4. Added `.grain/workflows/task-39.yml`. An intentionally incomplete file was rejected with the real parser message explaining that it had no usable `on:`; a valid no-op workflow saved and read back exactly.
5. Restored both capability sets, prompt extension, setup command, and workflow list to their original empty values and reloaded to confirm restoration.
6. The Base/Head comparison form is limited to the deployment's config repository. `grain demo` does not set `configRepo`, so that specific form was unavailable; task/write bases and release source/latest/previous branches were exercised instead. No Grain API was replaced to expose it.

### Memory

1. Opened Settings → Memory and inspected deployment and `acme/widgets` memory, author chips, fresh/stale ages, summaries, and sizes.
2. Opened the full `release-checklist` and `flaky-e2e` documents and verified text that is absent from the index response.
3. Appended a reversible correction to repository memory and saved it. A full page reload and next tab open read the correction from the store and showed the new human/today attribution.
4. Restored the exact seeded text, reloaded again, and verified the restored value. No console errors, failed requests, or HTTP failures occurred in this pass.

### Releases and release trains

1. Opened `/repos/acme/widgets/releases` directly and observed the real empty state.
2. Verified native required-field validation, real server rejection of `bad name`, and successful creation of `task-39`. Its persisted status was Provisioning, candidate `task-39.rc.1` was cutting, History matched Current candidate, and Cut/Promote/Merge were disabled while provisioning.
3. Toggled both qualification-plan controls, saved, reloaded, verified, and restored them.
4. Created a release-qualification template through Templates, added it to the repo plan, required approval, and created `task-39-q`.
5. Exercised the unavailable GitHub boundary as described below: failure messages appeared beside both release and candidate; success advanced the release to Active and candidate to active, then Grain's own qualification reconciler created one pending-approval task.
6. Observed the release's status, current candidate, history, pending-approval banner, and generated task. Before the fix the task row had zero links/buttons; after the fix it had one `/tasks/{id}` link, opened the task detail, and one browser Back returned to the release URL.
7. Observed the real release-train loading state with a DOM mutation observer, followed by the real empty result for a nonexistent direct URL.
8. From the repo page, verified the empty train list and New release train dialog. Native required validation and server rejection of `bad prefix` worked. Created `task39rel.N`, landed on its direct URL, and verified next-run/empty-history text.
9. Paused and resumed the train, edited its latest branch to `task39rel.current`, reloaded, and verified persistence.
10. Declined the Delete confirmation, confirmed the train remained, then accepted it. The repo returned to the empty train state and the old direct URL rendered “No such release train.”

## External-boundary fixture

No Grain API endpoint or React component was stubbed.

The demo has no GitHub release reconciler, so release/candidate rows otherwise remain Provisioning/Cutting forever. For `task-39-q` only, a temporary Go helper opened the throwaway store and invoked the same model transitions the GitHub reconciler invokes:

- failure: `MarkReleaseError` and `MarkCandidateError`, with realistic branch-creation errors;
- success: `MarkReleaseProvisioned` and `MarkCandidateCut`;
- qualification creation after success: the real `orchestrator.SyncQualifications`.

Loading and empty states came from the real server. This fixture changed only the unavailable external boundary's recorded outcome; release APIs, qualification APIs, store reads, polling, rendering, links, and browser gestures remained real.

## Finding and fix

### Qualification tasks were not links

Deterministic reproduction:

1. Open a release with an active candidate and a qualification run.
2. Locate `.qualification-task-list`.
3. The generated task name is visible, but the row contains zero anchors and zero buttons, so the task cannot be inspected from the release workflow.

Expected: the task name opens its `/tasks/{id}` detail, and browser Back returns to the release.

Fixed in `5000e96ace23c34f33b0ffa550192b2fe4a94d2a`: each name is now a normal link with modifier/new-tab semantics; ordinary clicks use App's task navigation callback, preserving one-step browser history. The component test asserts both href and callback. The final built-frontend Playwright pass verified link → task → one Back → release.

## Diagnostics and tests

Unexpected browser console errors: none. Expected generic Chromium “Failed to load resource” entries occurred only for deliberately submitted 400 validations. A few task polls, and the successful train DELETE during immediate in-app navigation, emitted Playwright `net::ERR_ABORTED`; the related screens and persisted store outcomes were correct on the next read.

Passed locally:

- `npm test -- --run src/components/RepoReleases.test.jsx` — 15 tests
- `npm test -- --run src/App.tasks.test.jsx src/components/RepoReleases.test.jsx` — 36 tests
- `npx playwright test --project=chromium e2e/repo-pane.spec.js e2e/memory.spec.js` — 5 real-browser tests
- final ad hoc built-frontend regression: qualification task link opens its task and one Back restores `/repos/acme/widgets/releases`
