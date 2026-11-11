# Deployment Operations Walkthrough (Task 3617)

This walkthrough covered System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners. I drove each one in a real Chromium browser against real `grain demo`
servers. It follows `2026-11-08-deployment-operations.md` (task 3360) and
`2026-11-08-deployment-operations-task-3464.md` (task 3464).

- **Commit tested:** `46d27f7eb76362330899abf42e77c5de9ddfd3f3` (`main`). The
  fix, its tests and this report are on
  `grain/098073e4/task-3617-walk-through-system-health`.
- **Date:** 2026-11-08, 13:10–13:45 UTC.
- **Result:** I found and fixed one scoped UI bug, and added unit and
  real-browser regression tests for it (§1). Everything else I exercised
  behaved as designed. No pages threw an uncaught `pageerror`, and no request
  returned an unexpected `5xx`. The only console errors were the expected
  `4xx` responses listed in §3. I filed no follow-up tasks. §4 lists one place
  where the brief and the design disagree, and the coverage I did not walk.

## Environment

- Debian 12.15 sandbox (`linux/amd64`), Go 1.26.10, Node 20.19.0, Playwright
  1.62.1 with Chrome for Testing 151.0.7922.34
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- **Build:** I ran `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e -ldflags "-X …/pkg/version.Release=v3.2.0-dev+46d27f7e" -o /tmp/walk-bin/grain ./cmd/grain`.
  The rail's build stamp read `v3.2.0-dev+46d27f7e · 46d27f7 · 2026-11-08 13:06Z`.
  After the fix I rebuilt both the frontend and the binary as `grain-after` and
  checked the fix on a live demo.
- **Gemini:** this sandbox has no `/home/debian/.gemini-api-key`, so `LOOK=1`
  was not available. I checked layout with DOM geometry instead
  (`getBoundingClientRect`, `documentElement.scrollWidth`, the scroller's
  `scrollLeft`). I also took `ui/e2e/screenshot.mjs` sheets before and after
  the fix, at desktop and phone widths in light and dark. The screenshots are
  not committed.
- **Servers:** each `grain demo` ran on its own throwaway SQLite store with
  `-open=false -access-code-file <dir>/access-code`. Every browser context
  signed in through the visible "Enter access code" page.
  - **A** `-observability -staging`: real CI engine and sandbox ledger,
    overcommit, job cache, proxy and Actions caches, logs, top, pipeline,
    metrics, and two persistent staging sandboxes.
  - **B** `-github-sim -fake-ci -base-branch-ci -scheduled-workflows -notifications -reconcile 2s -agent-paused -host-mode-install -host-executor hostd-down`:
    connection usage-limit pause, the host-mode install notice, the
    unreachable host agent, red/green base branch and shared CI faults.
  - **C** `-unconfigured`: setup alerts.
  - **D** `-upgrade-image 127.0.0.1:5055/x/grain -upgrade-allowed-branches 'latest,candidate,rc,main,v[0-9]*.[0-9]*.[0-9]*'`:
    a real image-mode upgrader. A loopback Node OCI stub (§3) served its tag
    listing.
  - **E** `-disable-self-upgrade -management-url https://console.example.test/grain -external-config-repo https://github.com/acme/grain-config.git`.
  - **F** the same as D, on the fixed binary.

## 1. Finding and fix

### 1.1 Settings → Upgrade showed no running version on an enabled upgrader whose daemon reports none

- **Where:** `ui/src/components/UpgradePanel.jsx`, the enabled branch's
  `<RunningNow running={status.running} />`.
- **Reproduction (deterministic):** start demo D. Its image upgrader has no
  ref file, so `GET /api/upgrade` returns no `running`. Open
  `/settings/upgrade`. `p.upgrade-running` was missing at all four
  widths/themes (desktop/phone × light/dark), so the version appeared only in
  the rail's build stamp. Demo E's externally managed pane did show
  `Now running 46d27f7.`. That branch already falls back to
  `GET /api/config`'s `version` (`fallbackRunning`), but the enabled branch
  never got the same fallback.
- **Fix:** the enabled branch now renders `<RunningNow running={fallbackRunning} />`.
  If the daemon reports `running`, that still wins. With neither a `running`
  nor a build version, the line is still omitted. The existing test "says
  nothing when the daemon cannot say what is running" still passes.
- **Verified on demo F:** `Now running a0e51ed.` (the full SHA is in its
  `title`) renders at all four widths/themes. On desktop it sits at
  `x=488` and on the phone at `x=12`, with a 16px gap above and below. Its
  colour is the secondary text colour in both themes (`rgb(82,78,70)` light,
  `rgb(184,181,173)` dark). The document has no horizontal overflow at any
  size.
- **Regression coverage:**
  - Two Vitest cases in `UpgradePanel.test.jsx`: one for the fallback to the
    build, and one showing the daemon's `running` still wins.
  - One Playwright case in `upgrade-image-chooser.spec.js` over a real
    `-upgrade-image` demo: "names the build it runs when the upgrader cannot
    say".
- **Tests run:**
  - `npx vitest run src/components/UpgradePanel.test.jsx`: 80 passed.
  - `npx playwright test e2e/upgrade-image-chooser.spec.js e2e/upgrade.spec.js e2e/disable-self-upgrade.spec.js e2e/settings-upgrade.spec.js`:
    13 passed.

## 2. Walkthrough coverage and outcomes

### 2.1 System shell, entry points, URL state and legacy links (A)

- On a 1440×900 window, the header rail's `System` button opened `/system`
  with `health` selected. Clicking each of the 8 tabs updated the URL to
  `/system`, `/system/logs`, …, `/system/power`.
  - Reloading on `/system/power` kept Power selected.
  - Back went to `/system/metrics` and Forward returned to `/system/power`,
    with the selected tab matching the URL each time.
- The build stamp at the foot of the rail opened `/settings/upgrade`.
  `/system/upgrade` and `/system/upgrade#database` both redirected to
  `/settings/upgrade`, keeping the anchor. An unknown `/system/bogus` fell
  back to `/system`.
- **Legacy links:**
  - `/admin/logs` went to `/system/logs`.
  - `/sandboxes` went to `/system/sandboxes`.
  - `/caches` went to `/system/caches`.
  - `/metrics?window=24h&repo=acme%2Fwidgets&q=auth` went to
    `/system/metrics?q=auth&f=repo:any:acme/widgets&window=24h`, so the query
    was kept and canonicalised.
  - Queries on `/admin/logs?q=…` and `/caches?x=1` are dropped. That is
    because Metrics is the only System tab with URL state (`paths.js`
    `buildPath`); Logs' search is local to the page.
- **Narrow window (640×800):** the tab scroller is `clientWidth 280` against
  `scrollWidth 730`, and the document is `640` wide. From the selected tab,
  7× `ArrowRight` + `Enter` reached `/system/power`. The scroller moved to
  `scrollLeft 450`, putting Power (`x 486–576`) inside the scroller
  (`x 296–576`).
- **Phone (390×844):** opening the drawer showed the `System` row at the foot
  (`y=709`). It opened System's section list (health … power). Each of the
  8 sections opened its `/system/<tab>` path with `scrollWidth 390` and a
  visible bell. `All of System` returned to `/system`.

### 2.2 Health and the alerts bell

- **A:** shows the overcommit status, the GitHub API budget panel and the
  idle-poll backoff panel:
  - `1,650 of 5,000 calls left this hour · resets in 38m 0s`, projected to run
    out in 30m 0s.
  - `2 of 3 tracked PRs backed off`, with the per-caller table.
  - the sign-in counters.
- **B:**
  - The host-mode install notice `Unmigrated host-mode install` names
    `claude` as the CLI that lost its run location and links to the migration
    docs.
  - The host agent readiness note says "The host agent is not reachable, so
    no run: host server can start …", with the
    `systemctl status grain-hostd` hint. The user-facing wording says "host
    agent", not "hostd".
  - The usage-limit alert reads "Agent usage limit reached: nothing is being
    dispatched onto connection Gemini 2.5 Pro until 7:11:40 PM (about 5h 55m)"
    and offers `Resume now` and `Open connection`.
- **C (unconfigured):** "2 setup steps left — Still to do: connect a git host,
  add a model connection." with a `Finish setup` button. The demo seeds a
  repository, so the "no repository" step does not appear.
- **E:** external config repo state, plus the release-pending and stalled-merge
  notifications.
- **Bell:** the bell's label counts what it lists (`Alerts: N active`). Each
  row has its actions (Approve/Reject/Acknowledge/Investigate,
  `Open #1` → `/tasks/1`). `See all on System → Health` closes the popover and
  opens `/system`.
- **Pipeline pause:**
  - Typing a reason and pressing `Pause pipeline` opened a native
    `confirm("Pause the pipeline? …")`. Dismissing it changed nothing.
  - Accepting it raised "Paused — only tasks you Run now will start … —
    walkthrough pause", and the bell went from 2 to 3. The paused state and
    reason survived a reload.
  - `Resume now` in the bell cleared the alert (3 → 2) and Pipeline read
    "The pipeline is running." again.
  - Manual mode is this same paused state ("only tasks you Run now will
    start").
- **Usage limit (B):** `Resume now` cleared the usage-limit alert (3 → 2), and
  it stayed cleared after a reload.
- **Red base branch (B):**
  - `POST /api/e2e/base-branch/red {"branch":"main"}` raised
    `pipeline.base-red:acme/widgets@main` (daemon log: "raised notification …
    acme/widgets main is red").
  - The bell row reads: "acme/widgets main is red — grain CI on acme/widgets
    main is confirmed red: at 3b64e24b8f99 ("failure: test"). grain has filed
    no repair for it (its triage is off here, or a person is to file one)".
  - `/green` cleared it (daemon log: "cleared notification").
  - The bell shows a raise or clear on its next 30s poll (`notifications.js`
    `POLL_MS`), not straight away. That is by design.

### 2.3 Standing banners (B)

- **Install notice:** `install-notice-banner` sits at `y=0`, 68px tall on
  desktop and 128px on the phone. The rail's bell is below it (`y=88` on
  desktop, `y=136` on the phone, beside the navigation button).
- **Disconnected:** I aborted `/api/**` in Playwright. After that,
  `disconnected-banner` appeared at `y=0` and read "This deployment cannot be
  reached: the connection to it failed. Unreachable since … -- retrying
  automatically".
  - It was 48px tall on desktop and 108px on the phone, and pushed the
    install notice down to `y=48` / `y=108`.
  - No button, link or input overlapped it.
  - It cleared within one retry after I removed the route.

### 2.4 Sandboxes (A)

- **Panels:**
  - Host load, CPU and memory, with sparklines.
  - Storage, plus the proxy cache, CI history, CI job cache
    (`Empty the job cache`) and CI artifacts notes.
  - Sandbox budget "within budget cap", cluster free headroom and the
    overcommit controller (`overcommit: auto`, factors, bounds, PSI, OOM
    kills, recent adjustments).
  - Reconciliation "in sync", standing sandboxes guidance, and persistent
    sandboxes: `docs-preview` running on `/staging/docs-preview/`, and
    `grain-staging` restarting with "exited with status 1", restarts 4,
    port 4280. Each has restart/stop/rebuild/logs controls.
- **Live table:**
  - I started a CI run from `/ci` and added an agent sandbox with
    `POST /api/e2e/sandboxes/other`.
  - The agent sandbox `1-identity-check` is listed above the
    `CI sandboxes (1)` summary row, which reads "1 CI sandbox · 2 vCPU /
    2.0 GiB · longest 9s".
  - The summary row starts collapsed (`aria-expanded="false"`). Expanding it
    showed `c5414d8-0`, and the expanded state survived a reload.
- With nothing in flight, the page says "Nothing in flight, so no sandboxes
  exist."

### 2.5 Caches (A)

- **Proxy cache:** 6 ecosystem rows totalling 5.3 GiB. **Actions cache:**
  "20 entries, 3.0 GiB of 20.0 GiB", with rows for `acme/widgets` (2.0 GiB)
  and `acme/gadgets` (1.0 GiB). Each Flush button is named for what it
  flushes, e.g. `Flush Playwright browsers` or
  `Flush the Actions cache of acme/gadgets`.
- **Flushes:** I flushed Playwright browsers, Go modules and the
  `acme/gadgets` Actions entries, all throwaway demo data.
  - Each flush happened as soon as I clicked, with no confirmation (see §4).
  - Flushed ecosystem rows read `0 …, 0 B` and their buttons are disabled.
    The flushed Actions repository's row disappears.
  - All of it was still the same after a reload.
- **Refresh and failure:** `Refresh` re-reads the caches. Stubbing
  `GET /api/package-cache` with a 500 shows "The caches could not be read
  (walkthrough: cache stats failed). This pane tries again every few
  seconds." After I removed the stub, `Refresh` recovered the pane.

### 2.6 CI (A and B)

- **Reaching it:** the CI section is hidden by default. Through the visible
  `Customize` dialog I ticked CI and saved; CI then appeared in the rail and
  opened `/ci`.
- **Starting a run:** `Start a run` → acme/widgets, branch main →
  `Load workflows` → `Start run` opened `/ci/5414d80e5eca5ab0`, showing the
  `observe` job running with the "Wait for the browser" step and its output
  toggle. Reloading the run URL kept it open.
- **Active sandboxes:** `Active sandboxes (1)` is collapsed to
  "1 CI sandbox running · longest 1s (observe, main)". Expanded, it lists
  `c5414d8-0`.
- **Cancel run:** pressing it opened a native prompt ("Stop this run? …
  Optionally, say why"). Dismissing it left the run running; accepting it
  ended the run as `cancelled` (job cancelled, 19s), and the rail's CI count
  went to 0.
- **Retry (A):** `Retry` on the cancelled run started and followed
  `/ci/e1e5693e4c487d0d`. `POST /api/e2e/ci/finish` concluded it as
  `success` ("observed through the sandbox"), and the job cache then read
  "1 entry".
- **Unknown run:** `/ci/deadbeefdeadbeef` showed the alert "no CI run
  "deadbeefdeadbeef" -- a run is remembered for a while and then forgotten",
  with the run list behind it, both on first load and after a reload.
- **Filtering:**
  - `Search runs…` narrows the list by repository or branch (`widgets`,
    `main`, `acme` → 2 runs). Result words (`success`) match nothing.
  - "zzznomatch" shows "No CI run matches. 2 runs are here …".
  - The search text survives a reload, though it is not in the URL.
- **Fake CI (B):**
  - The base-branch confirm left `failure` and `success` runs on main; I
    opened `/ci/fake-ci-5` (failure, job `test`, "No steps ran.").
  - `Retry` and `Retry failed jobs` are both offered there. Both were refused
    with a 403 that the page shows as text: "grain CI is not enabled for
    acme/widgets: set its CI Mode to "Use Grain CI" …".
  - A merge-queue recheck run (`/ci/demo-recheck-1`, running, 34m) offers
    `Cancel run`.
- **Shared fault:** "1 shared failure — acme/widgets · CI / e2e … failing the
  same way on 3 branches" names the branches and common files on both demos.

### 2.7 Logs and Top (A, desktop and phone)

- **Header controls:** `Source` (`daemon`), `Search logs` and `Refresh` sit in
  the page header rail. The log view opens at the tail
  (`scrollHeight − scrollTop − clientHeight = 0`).
- **Search:** "merge-queue" narrows to the matching 50 lines, "zzz" shows
  "(no matching log lines)", and clearing the search puts the view back at
  the tail.
- **Narrow layout:** on the phone the log `pre` scrolls internally (364px
  wide) and the document stays 390px.
- **Top:** shows the process table (systemd, grain-daemon, node).

### 2.8 Metrics (A)

- **Release notes:** Weekly (W44 figures, by repository and by direction, plus
  the "Nothing has been written up" note) and Versions (v3.1.1 … v1.2.0).
- **Report sections:** throughput (with trend lines), latency stages (with
  the `*` low-n footnote), capacity, outcomes, backlog ("Oldest waiting task:
  task 2"), and github ci (pass/fail/cancel rates, time to green, the flaky
  commit, and the per-job table).
- **Windows:** `?window=24h` and `?window=30d` re-scope the range line (1d 0h
  and 30d 0h). Unsupported `?window=1h` and `?window=bogus` fall back to
  7 days and are dropped from the URL.
- **Navigation:** `#10` opens `/tasks/10`. The backlog chips link to the
  filtered task list (`/?f=state:any:queued`, …).

### 2.9 Power (A)

I stubbed only `POST /api/host/{restart,reboot,shutdown}` in Playwright.

- **Cancel:** each button opens a native `confirm` with its own warning text.
  Dismissing it sent nothing.
- **Success:** accepting with the stub returning `202` shows "Restart
  requested. grain is coming back up …", "Reboot requested. This UI is
  unreachable until the host machine comes back up." or "Shutdown requested.
  grain stays down until somebody powers the machine back on."
- **Failure:** with the stub returning `500`, the page shows an error alert and
  no "requested" message.
  - **Stub caveat:** my stub's body was `text/plain`, so the alert read
    "500 Internal Server Error". `api.js` shows the message only from a
    `{"error": …}` JSON body, which is the format grain's own handlers use.
  - **Stub mistake:** my first stub also matched `/api/restart-watch`. I
    re-ran the failure case with only `/api/host/**` stubbed.

### 2.10 Settings → Upgrade (D, E)

- **Demo D:**
  - Mode description, the in-flight warning ("2 runs are in flight … 2 would
    be lost (antigravity 2)") and the image source (release assets, falling
    back to the registry).
  - The branch picker offered the tags the OCI stub listed (latest,
    candidate/rc, main, v3.2.0, v3.1.1, v3.1.0); `feature-x` was filtered
    out by the allowed branches.
  - "Will pull" shows `127.0.0.1:5055/x/grain:candidate`, and
    `127.0.0.1:5055/other/grain:v3.1.1` once I typed that image.
  - Release notes for `candidate` show "the registry has no image …", from
    the stub's manifest 404.
  - Pressing `Upgrade` opens an in-app "confirm upgrade" dialog. `Cancel`
    sent nothing.
  - `Upgrade` sent `POST /api/upgrade {"branch":"candidate","afterTasksFinish":false}`,
    which I intercepted with a `500`. The page showed
    "intercepted by walkthrough: no upgrade performed".
  - The upgrade check mode is shown read-only, with a
    `Settings → General → Updates` button. The database panel shows SQLite
    with the PostgreSQL migration commands.
- **Demo E:** "Now running 46d27f7. In-VM self-upgrade is disabled on this
  deployment; upgrades and instance lifecycle are managed externally." with a
  `Manage instance` link.

## 3. Stubs and intercepted boundaries

The real `grain demo` servers, SQLite stores, built Vite assets and Chromium
user gestures stayed in the loop throughout. I stubbed only boundaries that
are unavailable or unsafe in this sandbox:

| Boundary | Route / mechanism | Why, and what I checked |
| :--- | :--- | :--- |
| Host power | `page.route("**/api/host/**")`, POST only | Never restart, reboot or shut down the VM. I checked cancel (no POST), 202 success messages and 500 failure. |
| Self-upgrade | `page.route("**/api/upgrade**")` for non-GET only, plus a loopback Node OCI stub on `127.0.0.1:5055` answering `/v2/`, `/v2/x/grain/tags/list` and manifest HEADs with 404 | The real `-upgrade-image` upgrader listed the stub's tags. The upgrade POST itself was never allowed through. |
| Daemon unreachable | `page.route("**/api/**", r => r.abort("failed"))` | Raised the disconnected banner, then removed the route to watch it clear. |
| Cache read failure | `page.route("**/api/package-cache")` returning 500 | The error alert and recovery through `Refresh`. |
| Demo helpers (real server, loopback only) | `POST /api/e2e/sandboxes/other`, `POST /api/e2e/ci/finish`, `POST /api/e2e/base-branch/{red,green}` | Live sandbox rows, concluding a CI run, and raising/clearing the red-base alert. |

Expected `4xx` console errors:

- `404 GET /api/repos/acme/widgets/github-branches` on demo A, which has no
  GitHub (documented in `pkg/demo/demo_githubsim.go`).
- `404 GET /api/ci/runs/deadbeefdeadbeef` (the unknown run).
- `422 GET /api/upgrade/release-notes?branch=candidate` (the stub has no
  manifests).
- `403 POST /api/ci/runs` (retry on a repository without grain CI).

## 4. Brief mismatch and coverage not walked

- **Proxy and Actions cache flushes have no confirmation.** The brief asks
  for "each named Flush control … with its confirmation", and task 3464's
  report describes dismissing one. In fact `PackageCachePanel.jsx` says "A
  flush needs no confirmation and gets none: nothing is lost that a
  re-download does not put back". `CachesPage.jsx`'s `flushActions` likewise
  POSTs immediately. Only `Empty the job cache` confirms. I left this as
  designed. The brief and the earlier report are what is out of date.
- **The upgrade image field is not checked as you type.** Typing
  `not a valid ref!!` shows "Will pull not a valid ref!!:candidate". I did
  not submit it; the server is what checks the reference.
- **Not walked:**
  - A dashboard throughput panel's `Open` link into Metrics. The demo seeds
    no board with a throughput panel (board-3 "Report" is a report board),
    and I did not build one. `panels.js` `panelDestination` and its tests
    cover that link.
  - Health's restart-pending and stored-but-not-runnable field links. These
    were walked in task 3464 and are covered by
    `deployment-operations-pending-restart-field.spec.js`.
  - The shared-fault task assignment, which is covered by
    `deployment-operations-see-all-fault-owner.spec.js`.
