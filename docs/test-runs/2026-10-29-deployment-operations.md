# Deployment Operations Walkthrough — 2026-10-29

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real browser against real `grain demo` servers.

- **Commit tested:** `9958172316fbbb293c4c4040fdf2311877284689` (the
  `main` merge of #143), then the fixes below on
  `grain/049f1e09/task-1760`.
- **Date:** 2026-10-29
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), `linux/amd64`; Go 1.26.10; Node 20.19.0.
  - `grain` built with `go build -tags grain_e2e -ldflags "-X …/pkg/version.Release=v1.2.0" ./cmd/grain`,
    over the Vite production bundle (`npm run build`), as
    `ui/e2e/global-setup.js` builds it.
  - Three demos, each over its own throwaway embedded SQLite store
    (`TMPDIR` a scratch directory), signed in with an access code from
    `-access-code-file`:
    - `grain demo -observability` (real `pkg/ci` service, job cache,
      host-directory sandboxes, fake proxy/Actions caches, `top`);
    - `grain demo -github-sim -fake-ci -base-branch-ci -agent-paused -host-mode-install`;
    - `grain demo -unconfigured`.
  - Playwright Chromium (`chromium-1234`, headless) from
    `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900 and 390×844.
  - No Gemini key in this sandbox (`/home/debian/.gemini-api-key` absent),
    so nothing here used a model.

## 1. Bugs found and fixed

### Bug 1: the CI page never said which task a shared fault is assigned to

- **Surface:** [CI](/ci), its shared-faults cards and the line under a red
  run (`ui/src/components/CIPage.jsx`).
- **Reproduction:** `GET /api/ci/runs` carries `faults[].ownerTaskId` and
  `ownerRef` whenever `cifault.ResolveOwner` finds one (an open base-repair
  task, else the first queued task hitting the fault). `grain ci` prints
  it (`owner: task #N`) and agents' `ci_faults` tool says "Assigned to Task
  #N". Give a fault an owner (below the demo's answer gets
  `ownerTaskId: "2"` added) and open `/ci`: the card shows repo, job,
  branches, common files and open fixes, and nothing about the owner.
  `grep -rn ownerTaskId ui/src` came back empty.
- **Fix:** a `FaultOwner` line, "Assigned to task #N (ref)", on both the
  shared-faults card and the `SharedFaultRow` under a run. It links to
  `/tasks/N` through `TaskLink`, so a plain click opens the task in place.
  `CIPage` now takes `onOpenTask`, and `App.jsx` passes `openTask`.
- **Coverage:** `CIPage.test.jsx` › "names and links the task a shared
  fault is assigned to"; `e2e/deployment-operations-see-all-fault-owner.spec.js`
  › "a shared CI fault names and links the task assigned to fix it".

### Bug 2: the alerts bell had no "see all" to System → Health

- **Surface:** the bell in every page's header rail and on a phone's bar
  (`ui/src/components/AlertsButton.jsx`).
- **Reproduction:** open the bell on any page. The popover lists the
  alerts and nothing else. `design/design/settings-panel.design.md` §4.1 says "Its
  'see all' goes to System → Health", and the brief asks to verify it. No
  control in the tree did it (`grep -rni "see all" ui/src` found nothing).
- **Fix:** a "See all on System → Health" button below the list, shown
  whether or not anything is alerting, because Health also carries the
  config-repo state and settings waiting on a restart. It closes the
  popover and calls `openSystem("health")`. That lands on `/system` on a
  desktop and on `/system/health` on a phone, where Health is a section
  of its own.
- **Coverage:** `AlertsButton.test.jsx` › "opens System → Health from See
  all, with or without alerts, and closes"; the e2e spec above, at
  1440×900 (keyboard Enter, Back returns to `/inbox`) and 390×844.

### Bug 3: the red-base repair alert and Inbox note showed literal backticks

- **Surface:** the bell / System → Health row and the Inbox's "Base-branch
  repair waiting" note (`ui/src/alerts.js`, `ui/src/state.js`).
- **Reproduction:** on the `-github-sim -fake-ci -base-branch-ci` demo,
  `POST /api/e2e/base-branch/red {"branch":"main"}`, then give a task
  `redBaseBranch: "main"` and a pull request (intercepted `/api/tasks`,
  since the demo files no repair). The bell's title read
  `` `main` is red — repair #1 `` and its sentence
  `` `main` is red — repair #1 is waiting for approval ``: both surfaces
  render plain text, not markdown.
- **Fix:** the branch is printed bare ("main is red — repair #1").
- **Coverage:** the exact-text assertions in `alerts.test.js`,
  `AlertsButton.test.jsx` and `InboxPage.test.jsx` now expect no backticks.

## 2. Walkthrough and outcomes

### Navigation, URLs, history

- The System button in the rail opens `/system` (Health). On a phone the
  drawer's System row opens the section list (Health, Logs, Sandboxes,
  Caches, Top, Pipeline, Metrics, Power, each with a ›).
- Every tab rides in the URL and survives reload: `/system/logs`,
  `/system/sandboxes`, `/system/caches`, `/system/top`, `/system/pipeline`,
  `/system/metrics`, `/system/power`. `/system/health` canonicalises to
  `/system` on a desktop and stays `/system/health` on a phone.
- Old links: `/admin/logs` → `/system/logs`, `/sandboxes` →
  `/system/sandboxes`, `/caches` → `/system/caches`, `/metrics?window=30d`
  → `/system/metrics?window=30d` (query kept), `/system/upgrade` →
  `/settings/upgrade`.
- The build stamp at the foot of the rail ("v1.2.0 · 9958172 · …") opens
  `/settings/upgrade`.
- At 390×844 no System tab scrolls sideways
  (`scrollWidth == clientWidth == 390` on all eight).

### Health

- Lists the same alerts as the bell (the demo's two operator
  notifications, with Approve/Reject/Acknowledge/Investigate), the
  overcommit controller's status and the GitHub API budget.
- On the host-mode demo the install notice is on Health as well as in
  the banner.

### Sandboxes

- Host load, memory and swap with sparklines; storage (sandboxes, proxy
  cache 5.3 GiB, CI history, CI job cache with "Empty the job cache", CI
  artifacts disabled); the budget (`within budget cap`, committed vs cap,
  cluster free, roomiest node); the auto overcommit status and its
  adjustment log; reconciliation `in sync`; standing and persistent
  sandboxes empty states; two connected model servers.
- A held CI run showed up in the CI sandboxes and cleared once finished.
  The previous run's 60-row and error stubs
  (`docs/test-runs/2026-10-26-deployment-operations.md`) were not
  repeated.

### Caches

- Proxy cache per ecosystem (apt, npm, Python, Go, Docker Hub,
  Playwright) and the Actions cache per repository (acme/widgets 2.0 GiB,
  acme/gadgets 1.0 GiB of 20.0 GiB).
- Flush npm against the demo's throwaway entries sent
  `POST /api/package-cache/flush?kind=npm`. The row went to zero and its
  button disabled; that held through a reload.
- **No confirmation was asked**, contrary to the brief's "with its
  confirmation". This is deliberate: `PackageCachePanel.jsx` documents
  "A flush needs no confirmation and gets none: nothing is lost that a
  re-download does not put back". Left as designed and recorded here.

### CI

- `POST /api/ci/runs {repo: acme/widgets, ref: walk/held-1}` held a live
  sandbox. "Active sandboxes (1)" is a collapsed summary ("1 CI sandbox
  running · longest 2m 9s (observe, held-1)"); expanding it shows the
  table.
- Opening the run from its repo link goes to `/ci/<id>`, which survives
  reload and Back/Forward.
- Cancel run: dismissing the prompt left the run open; accepting
  cancelled it, and only Retry remains (no "Retry failed jobs" on a
  cancelled run, by design).
- Retry followed to the new run's `/ci/<id>`; finishing it
  (`/api/e2e/ci/finish`) showed success with "show output" on the step.
- A forgotten run id (`/ci/0000000000000000`) shows the "remembered for
  a while and then forgotten" notice. Its 404 is the expected answer.
- Failure, errored, skipped and cache-hit states came from one stubbed
  run (`/api/ci/runs/feedfacefeedface`). "Retry failed jobs" posted
  `jobs: ["observe","e2e"]` with `recheck: true`; the daemon refused the
  stub's made-up `e2e` job, and the UI showed its error. With only
  `observe` failing it started a real run of that one job and followed
  to it.
- The shared fault ("failing the same way on 3 branches", common files)
  and its owner: Bug 1.

### Logs, Top

- Logs: search with no match shows "No matching log lines."; matching
  `orchestrator` filters the tail.
- Top: the process table, auto-refresh and Refresh.

### Pipeline

- Pause pipeline with a reason asks for confirmation. After accepting,
  the bell went from 2 to 3 alerts: "Paused — only tasks you Run now will
  start" (manual mode, `runNowPasses`), with the reason, its time and
  Resume now.
- The pause persisted across a reload.
- Resume pipeline asks; after accepting, the alert cleared (bell back to 2).

### Power

`/api/host/{restart,reboot,shutdown}` were intercepted, so the host was
never touched.

- Dismissing each of the three confirmations sent nothing.
- Accepted:
  - Restart → 200: "Restart requested …".
  - Reboot → connection reset: "Reboot requested …".
  - Shut down → 500: its error shown.

### Metrics

- Release notes: Weekly (Week 43, hero numbers, per-repo line, empty
  write-up note) and Versions (`running` on v1.2.0), then Throughput and
  the rest.
- Windows: 24 hours, 7 days, 30 days and 90 days. 30 days writes
  `?window=30d` and survives reload.
- The previous run's failure/partial and task-navigation checks were not
  repeated.

### Settings → Upgrade

- Reached from the build stamp and from `/system/upgrade`.
- With neither `-upgrade-image` nor `-upgrade-src-dir` it says "Not
  available …".
- The "Settings → General → Updates" link is present, and so is the
  database panel (SQLite → PostgreSQL DSN, Test connection, Migrate).
- **Not covered:** the image picker and version display with an upgrader
  wired. That needs `-upgrade-image` against a registry this sandbox does
  not run, and was left rather than stubbed wholesale.

### Alerts bell

- **Agent/connection pause** (`-agent-paused`): "Usage limit reached
  (antigravity)", scoped to connection Gemini 2.5 Pro, "Until 4:50:14 PM
  (about 5h 59m)", with Resume now and Open connection.
- **Setup** (`-unconfigured`): "1 setup step left · Still to do: add a
  model connection" with Finish setup.
  - That demo still stores a git connection (`hasGitCredential: true`)
    while `/api/config` says `gitCredentialsConfigured: false`, so the
    "no git connection" step can't be raised by `-unconfigured` alone.
    The UI follows the deployment row, as designed.
- **Red base branch:** the server's own "acme/widgets main is red"
  notification, and the repair alert (Bug 3). It cleared once the task no
  longer carried `redBaseBranch`.
- **Pipeline pause / manual mode:** see Pipeline.
- **See all:** Bug 2. After the fix it is reached by Tab and opens
  System → Health.
- **Not driven:** reconciler-down, failing config-repo sync and host
  sandboxes. Each needs a daemon state the demo can't produce (a stopped
  reconcile loop, an unreachable state repo, `-host-sandboxes`). They are
  covered by `AlertsButton.test.jsx` / `alerts.test.js` and by the
  2026-10-26 run's `/api/config` injection.

### Standing banners

- **Host-mode install notice** (`-host-mode-install`): at 1440 it is a
  48 px strip at y=0, and the rail starts at y=48. At 390 it is 128 px,
  and the phone bar starts at y=128.
- **Disconnected banner:** aborting `/api/tasks` and `/api/config` raised
  it at y=0 and pushed the install notice below it (y=48 desktop, 108
  phone) and the rail below both. Nothing overlaps.
- The disconnected banner cleared about 10 s after the routes were
  restored (the poll's backoff).

### Console and network

- No page errors or console errors on any System tab, CI, or Settings →
  Upgrade.
- Seen and expected: the 404 for the forgotten run id; `ERR_ABORTED` on
  `/api/tasks` when navigating away mid-poll; on the observability demo,
  a 404 for `/api/repos/acme/widgets/github-branches` on a task page
  (the demo wires no GitHub).

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset and a 500 to check success, accepted-by-disconnect and failure. |
| `/api/ci/runs/feedfacefeedface` | GET | One run carrying failure, errored, skipped and cache-hit jobs, which the demo's single always-green workflow cannot produce. The Retry POSTs it led to were real. |
| `/api/ci/runs` (list) | GET | The real answer with `ownerTaskId`/`ownerRef` added to each fault: no task owns the demo's feature-branch fault. Also in the e2e spec. |
| `/api/tasks` | GET | The real answer with `redBaseBranch`/`pullRequest` on one task: the demo files no base-branch repair. |
| `/api/tasks`, `/api/config` | GET | Aborted to raise the disconnected banner, then restored. |

Everything else was the demos' own state, driven through the UI or the
demos' e2e endpoints (`/api/e2e/ci/finish`, `/api/e2e/base-branch/red`).

## 4. Tests run

- `npx vitest run src/components/CIPage.test.jsx src/components/AlertsButton.test.jsx src/alerts.test.js src/components/InboxPage.test.jsx src/App.overlays.test.jsx src/state.test.js`:
  399 passed.
- `npx playwright test e2e/deployment-operations-see-all-fault-owner.spec.js`:
  3 passed.
- `npx playwright test e2e/alerts-panel-layout.spec.js e2e/ci-faults-github.spec.js e2e/deployment-operations-navigation.spec.js e2e/disconnection.spec.js e2e/deployment_operations_walkthrough.spec.js`:
  41 passed.
