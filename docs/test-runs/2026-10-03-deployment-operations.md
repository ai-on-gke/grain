# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-03
**Branch:** `grain/task-1629`
**Commit tested:** `f8add8a92` (`Merge pull request #2699 from bwsalmon/grain/task-1580`, the tip of `candidate`). The fixes below (`5425177f1`, `88d6870ff`) were then re-checked against a rebuilt server on a fresh store.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1, Chromium 151 (headless shell, `chromium-1234`)
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm ci && npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR` (a scratch directory). After each round of fixes it was rebuilt and restarted with a fresh store.
**Model and tooling:** there was no `/home/debian/.gemini-api-key` in this sandbox, so nothing here called Gemini. The walkthrough uses no model.

## How it was driven

The walkthrough was a set of Node scripts using Playwright's `chromium` API, kept out of the tree. They drove the real built frontend through visible controls: tabs, MUI selects and native selects, text and number fields with blur-to-save, radio groups, buttons, and keyboard gestures (Tab, ArrowRight, Home, End, Enter, Escape, Backspace). Each native `confirm()` was answered on purpose, dismissed first and then accepted.

Each script recorded console errors and warnings, page errors, failed requests (other than the deliberate `ERR_ABORTED` on 304 polls) and every 4xx/5xx response. Each mutation was confirmed through the matching `GET` (`/api/settings`, `/api/teams/default`, `/api/ci/runs/<id>`, `/api/secrets`), then by reloading or deep-linking the screen.

The sign-in was real. The demo enables password auth, so the account `operator` (one of the demo's seeded system admins) was created through the "new user" tab of the sign-in dialog. The resulting browser state was reused by later scripts.

This run started from the 2026-10-02 report. It spent most of its time on what that run had not probed: the boundary values of every numeric sandbox and engine setting, the capability secrets' destructive controls, CI step output, Logs search with regex metacharacters, Top's auto-refresh, team model and effort selection, and the stubbed upgrader's failure path.

Setup notes, unchanged from earlier runs:

- `npx playwright install chromium` needs `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the package-cache proxy.
- Admin tab names are lowercased by CSS, so Playwright's accessible names are mixed case ("Sandbox & Infrastructure"). Use case-insensitive matchers.

## Bugs found and fixed

| # | Bug | Reproduction (before the fix) | Fix | Regression coverage |
|---|-----|------------------------------|-----|---------------------|
| 1 | **Host memory reserve had its meaning inverted.** The help text said "Blank uses grain's default (an eighth of host RAM); -1 disables the reserve". In the model, though, -1 *is* grain's default (`model.ReserveMemoryDefault`) and 0 is a reserve of nothing. | Fresh demo → `/admin/sandbox`. The field shows `-1`, under text calling that "disabled". `GET /api/settings` → `sandboxMemoryReserveMb: -1`. Select the field, delete its contents, and press Tab: `PUT {"sandboxMemoryReserveMb":0}`, and the store now holds 0. Following the help text to get the default removed the host's reserve entirely. Also: the field's `placeholder` read `sandboxMemoryReserveMbDefault`, which the server never sent. | A negative (grain's own) reserve now shows as blank, and clearing the field stores -1. The help reads "Blank uses grain's default (an eighth of host RAM, never less than 2 GiB); 0 keeps nothing back", and the field has `min: 0`. The settings response now carries `sandboxMemoryReserveMbDefault`: the reserve resolved against the host this process can read, omitted where there is none (as in the demo). Re-driven: blank stored -1; typing `0` stored 0 and showed `0` after a reload; clearing it stored -1 and showed blank after a reload. | `AdminOverlay.test.jsx` "keeps grain's own memory reserve blank and stores blank as it" (fails with the fix stashed); Go `TestSettingsReportsTheDefaultMemoryReserveForThisHost`. |
| 2 | **Clearing or deleting a stored secret needed no confirmation.** A capability's "Clear", the "Other secrets" key chips and "Delete secret" all deleted on the first press. Values in the secrets store are write-only and cannot be read back. The capability then goes not ready, and every task holding it fails to dispatch. MUI's chip delete also fires on Backspace when a chip is focused. The rest of the UI confirms before anything it cannot undo (CI cache, memory, power, pipeline). | `/admin/integrations` → GitHub sandbox → type `fake-app-id-1629` into `github-app/app-id` → Set ("set"). Click Clear: `DELETE /api/secrets/github-app/app-id` goes out at once, no dialog, and the row reads "not set". | `SecretField` and `SecretsPanel` now `confirm()` first, naming the secret and saying the value cannot be read back. A dismissed confirm sends nothing. Re-driven: a dismissed Clear left `set: true` with no DELETE, and an accepted Clear deleted it. Backspace on a focused chip with the confirm dismissed kept the key. "Delete secret", accepted, removed the row. | `SecretField.test.jsx` and `SecretsPanel.test.jsx` (each delete test now dismisses, asserts nothing was sent, then accepts); e2e `deployment_operations_walkthrough` test 5 dismisses then accepts "Delete secret". |
| 3 | **The demo CI step printed a literal `\n`.** (Demo only.) | Start a run on `/ci` (acme/widgets, any branch) → `POST /api/e2e/ci/finish` → open `/ci/<id>` → job "observe" → "Show output". It reads `observed through the sandbox\n`, and `GET /api/ci/runs/<id>` has `"output": "observed through the sandbox\\n"`. The workflow is a Go raw string containing `printf '…\\n'`, so the shell's printf saw an escaped backslash. | A single `\n` in `cmd/grain/demo_observability.go`. | e2e `deployment_operations_walkthrough` test 7 now asserts the exact line (`{ exact: true }`). |

## Coverage and outcomes

### Settings

- **Tab traversal:** there are 16 Admin tabs. Each rendered with no console error and wrote its own `/admin/<tab>` (General keeps `/admin`). Integrations & Auth has 23 sub-tabs (deployment-wide plus one per capability); its summary reads "15 of 22 configured. Still to configure: GCP key, Gemini key, GitHub sandbox, Registry push, Claude proxy, OpenAI proxy, Gemini proxy". Team settings has 8 tabs, and its Models tab deep-links (`/teams/default/settings/models`).
- **Keyboard:** from a focused Admin tab, ArrowRight then Enter opened the next tab (`/admin/top` → `/admin/pipeline`), Home then Enter opened `/admin`, and End then Enter opened `/admin/upgrade`. Horizontal scrolling of the strip at 700 px was covered on 2026-10-02 and by e2e test 1, which passed again.
- **Appearance and timeline order (browser-local, `/settings`):**
  - Dark + Oldest first wrote `grain.themeMode=dark` and `grain.timelineOrder=oldest`, the body turned `rgb(15, 16, 19)`, and both radios were still checked after a reload.
  - Auto + Newest first removed both keys, and the body returned to `rgb(247, 246, 243)`.
- **Validation (Engine and Sandbox tabs, typed through the keyboard and committed with Tab):**
  - **Refused with the server's message in the banner, store unchanged:**
    - sandbox vCPUs `-3` ("must be 0 (unset) or at least 1");
    - CPU overcommit `-1`;
    - disk floor `1.5` and `-0.2`;
    - merge batch size `0` and `-2`;
    - merge gather window `xx`;
    - CI queue age boost `-3m` ("cannot be negative…") and `zz`.
  - **Accepted:**
    - vCPUs `4`, and `0` (shown blank as "unset");
    - disk floor `0.5` and `0.2`;
    - merge gather `-1s` and blank;
    - age boost `3m` (stored `3m0s`);
    - CI runs kept `-1` and `100`.
  - **Accepted, and left as notes:**
    - `1.5` vCPUs is truncated to 1 by `parseInt`;
    - CI runs kept `-5` is stored as -5 (any negative keeps every run);
    - non-numeric text in a number field reaches the blur handler as "" and is treated as cleared, so CPU overcommit `abc` stored 0 ("unbounded"). The browser itself reduces the text to "".
- **Agent and model selection (Gemini):** Team settings → Models lists `gemini-2.5-pro` and `gemini-2.5-flash`, each served by the "gemini 2.5 pro" Antigravity connection.
  - Picking default model `gemini-2.5-flash` sent `PUT /api/teams/default {"defaultModel":"gemini-2.5-flash"}`.
  - Picking effort `high` for `gemini-2.5-pro` sent `{"modelSettings":[{"model":"gemini-2.5-pro","effort":"high"}]}`.
  - Both survived a reload and were reverted through the same controls.
  - The listbox opens from the keyboard (ArrowDown), and Escape closes only the listbox, not the pane.
- **GitHub configuration and the restart-required notice:** setting GitHub host to `ghe.example.test` saved on blur, with its "needs restart / Takes effect when the daemon restarts" notice beside it. It was restored to blank.
- **Capabilities and secrets:** see bug 2. Gemini key, as a default capability that is not ready, shows the unrunnable-setting notice: "Every new task is filed holding this, and it is not ready: each of those tasks will fail to dispatch until the gap below is closed…". The secret's box is cleared after Set and the value is never shown again. Only fake values were stored, and all were deleted again.
- **Config repo and memory:** Config repo says "This UI is not running inside a daemon that owns a config repository". The Memory capability reads ready.
- **Upgrade planning (stubbed upgrader, see stubs):**
  - The idle pane says "Now running ghcr.io/bwsalmon/grain/grain:main".
  - A failed `POST /api/upgrade` (500) showed `upgrader unavailable (stub)` and changed nothing.
  - Upgrade-now sends straight away when nothing is in flight. `UpgradePanel` confirms only when runs are in flight (`inFlightWarning`), which is by design.
  - Without the stub the pane says it has neither `-upgrade-image` nor `-upgrade-src-dir`.

### Sandboxes

- **Real demo reading:** host load, utilization, memory and swap, CPU and memory trend charts, and "No disk figures available".
  - The Storage breakdown covers in-flight sandboxes, proxy cache 5.3 GiB, CI history and the CI job cache. The job cache read "1 KiB … 2 of 4096 job results … answered 0 of 3 lookups" after the CI section below.
  - While a CI run was held, "active sandboxes (1)" listed it on `/ci`.
- **Stubbed states:**
  - A 500 on refresh shows `host stats unavailable (stub)` while the last reading stays up.
  - `{enabled:false}` shows "Not available: this deployment has no sandbox pool or host stats configured".
  - A 40-row sandbox list rendered every row with no page overflow at 1024 px.

### CI

- **Start a run from the visible form:** repo `acme/widgets`, branch `walk-1629-a` → Load workflows ("Observability — .grain/workflows/observability.yml") → Start run. It opened `/ci/<id>`, showing running, `workflow_dispatch`, "cancel run", job `observe`, sandbox `c2820f3-0`, and step "Wait for the browser" with "(waiting for output...)".
- **Cancel:** dismissing the confirm left the run running. Accepting it marked the run cancelled after 3 s, with the job and step cancelled too. There is no re-run or retry control. Escape returned to `/ci`.
- **List and filtering:** after a second run finished (success) and a third was held running, the list showed running, success and cancelled, with "1 jobs: 1 running", "all green" and "1 cancelled".
  - Searching `walk-1629-b` narrowed the list to one run.
  - `zzz` showed "No CI run matches. 3 runs are here…".
  - Clearing the search restored all three.
- **Rows and drill-down:** each row's repo link is a real button, and Enter on it opened the run. On a finished run, clicking the job expands its steps, and "Show output" shows the step's output (bug 3).
- **Direct link and reload:** both keep `/ci/<id>`. An unknown run shows `no CI run "doesnotexist1629" -- a run is remembered for a while and then forgotten` (one intended 404).
- **Covered elsewhere and passed again:** errored, skipped and cache-hit states, and opening a run from a task link, are covered by `observability.spec.js` and `deployment_operations_walkthrough` test 7.

### System

- **Logs:**
  - The tail loads 300 lines from the single `daemon` source.
  - Search is a case-insensitive substring: `merge-queue` and `MERGE-QUEUE` each matched 50 lines.
  - `.*`, `\` and `zzzz-nothing` each showed "(no matching log lines)" with no crash.
  - Refresh fetched `GET /api/logs/daemon?lines=500`, and the tab kept polling every 5 s on its own.
- **Top:** a process table. Auto-refresh is on by default and fetches `/api/host/top`; unticking it stopped every request over the next 6 s.
- **Root shell:** there is still no Root shell entry in this build's Admin tabs or rail, so there was no availability message to check.
- **Pipeline (real demo state):**
  - Pause with a 330-character reason containing `<b>` markup: dismissing the confirm left the pipeline running. Accepting it paused it.
  - After a reload, the tab read "The pipeline is paused since …" with the reason shown as literal text (not markup), and the global banner said paused. The long reason wrapped with no page overflow.
  - Resume: dismissing left it paused, and accepting resumed it.
- **Power:** the demo now advertises all three switches on (`restartEnabled`, `rebootEnabled` and `shutdownEnabled` are all true in `/api/config`). All three POSTs were intercepted (see stubs).
  - Dismissing each confirm sent nothing; accepting sent exactly one POST each.
  - Restart (200) → "Restart requested…".
  - Reboot (500) → `reboot refused (stub)`, and the restart acknowledgement was cleared.
  - Shutdown (connection reset) → "Shutdown requested…".

### Metrics

- **Sections:** Release notes (week 39, "Nothing has been written up for this week yet"), Throughput (11 filed, 3 completed), Latency (with the "fewer samples" footnote), Capacity ("0.01 of 6") and Backlog ("Oldest queued: task 2, waiting 3h 10m").
- **Windows:** last 24 hours, 30 days and 90 days each re-span ("1d 0h", "30d 0h", "90d 0h"). The window is now in the URL (`/metrics?window=90d`), which closes the 2026-10-02 follow-up.
- **Loading and failure:** a slow 503 stub left the page's loading state up, then the failure, with the release-notes section still rendered.

### Console and network

Over the whole walkthrough, the only console errors were intended ones:

- the validation 400s from `PUT /api/settings`;
- the stubbed 500s (`/api/host/reboot`, `/api/sandboxes`, `/api/upgrade`) and the stubbed 503 (`/api/metrics`);
- the shutdown stub's connection reset;
- the 404 for the unknown CI run;
- the demo-only `GET /api/repos/acme/widgets/github-branches` 404 (the demo has no GitHub).

No page errors.

## Stubs (every intercepted route)

All stubs were Playwright `page.route` interceptions inside the browser. The server, frontend, store and gestures stayed real.

| Route | Why | What it returned |
|---|---|---|
| `POST /api/host/restart`, `/reboot`, `/shutdown` | These must never touch the host, and the demo advertises them as enabled. | 200 `{ok:true}`; 500 `{error:"reboot refused (stub)"}`; a connection reset. |
| `GET /api/sandboxes` | The demo host cannot fail, switch off, or carry 40 sandboxes. | Real data first; then a 500; then `{enabled:false}`; then the real response plus 40 synthetic rows. |
| `GET /api/metrics` | The demo store cannot fail. | A 503 after 1.5 s, then the real response. |
| `GET`/`POST /api/upgrade`, `DELETE /api/upgrade/scheduled` | The demo has no upgrader, and a real one would restart grain. | The idle image-mode status from `e2e/upgrade.spec.js`, and a 500 for the one POST made. |

CI execution was not stubbed. The runs were the demo's real CI engine, finished by its own `POST /api/e2e/ci/finish`. The secret store was not stubbed either: only fake values (`fake-app-id-1629`, `dummy-not-real`) went into the demo's real secrets directory, and all were deleted again. GitHub and secret validation needed no stub this time.

## Tests run

- **Vitest:** the full suite (`npx vitest run`): 166 files, 2835 tests, all passed. The new and changed tests are in `AdminOverlay.test.jsx`, `SecretField.test.jsx` and `SecretsPanel.test.jsx`; the memory-reserve test fails with its fix stashed.
- **Go:** `go test ./pkg/ui` passed, including the new `TestSettingsReportsTheDefaultMemoryReserveForThisHost`. `go vet ./pkg/ui ./cmd/grain` is clean.
- **Playwright (Chromium):** `deployment_operations_walkthrough`, `deployment-operations-escape-power`, `observability` and `system-overlay-tabs`: 14/14 passed.

## Left as notes, not changed

- **Errors stay up after the next save works.** An error from a failed save stays in the app-wide banner after a later save succeeds, whether in the same field or another (for example, "mergeBatchSize must be at least 1" is still shown after `4` saves). After a refused reboot, the error also stays beside the later "Shutdown requested". This is the banner's app-wide sticky behaviour rather than this pane's, so changing it belongs to the banner, not here.
- **Number fields read garbage as cleared.** Non-numeric text in a number field reaches the blur handler as "", so it saves the field's "cleared" value. That is harmless where blank means "default", but CPU and memory overcommit store 0 ("unbounded") when cleared. Their help says what 0 means, so this is noted rather than changed.
- **Some values are quietly coerced.** CI runs kept and the host memory reserve accept any negative, not just the documented -1, and vCPUs truncates `1.5` to 1.
- **The upgrade "branch" field accepts blank input.** With only whitespace typed, the Upgrade button stays enabled.
