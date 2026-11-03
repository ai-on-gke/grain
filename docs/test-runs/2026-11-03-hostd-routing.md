# Test run — 2026-11-03 §2: per-task hostd routing, capacity, waiting, remote staging and carry

Plan: [`docs/test-plans/2026-11-03.md`](../test-plans/2026-11-03.md) §2 (PRs #540, #469, #495, #503, #455).
Task 2708. Base: `origin/main` at `ac1f0e6d`. `git log origin/main -- pkg/hostd pkg/daemon` showed §1's fix `bfdf241b` (select a hostd by its registry labels only) already on it. Scripts and raw output: [`2026-11-03-hostd-routing/`](2026-11-03-hostd-routing/).

## Environment

- A real `grain daemon` (this branch's build) on `127.0.0.1:4300`, with `-host-sandboxes -sandbox-port-base=0 -agy-run host -poll-interval 3s` and its own data dir (`env.sh`).
- Three hostds on one machine, each with its own root-owned allowlist under `/etc/grain/hostd-<n>.yaml`:
  - `local`: co-located, on a unix socket.
  - `ws-a`: dials in. Own `HOME`, `capacity: 1`, registry labels `gpu=yes,zone=a`, owner `alice@example.com`.
  - `ws-b`: dials in. Own `HOME`, `capacity: 2`, registry labels `gpu=yes,zone=b`, owner `bob@example.com`.
  - `ws-a` and `ws-b` were registered with `grain hostds register -owner … -label … -token-file …` and started with `grain hostd -server … -name … -token-file …`.
- **Agent:** every hostd's `agy` entry is `tests/installer/fakeagy`. Each run's CLI is a real process that the routed hostd starts. The fake writes `HOME`, `hostname`, `pid`, `ppid`, cwd and its MCP endpoint into a report file, and calls grain's MCP server.
  - I used fakeagy rather than `interactive_latency_test.go`'s in-process stub, because that stub never starts a CLI through a hostd.
  - A connection `agy-ambient` (antigravity, `authMode: ambient`) makes item 6's pause hostd-scoped.
- **Stubbed:** the agent backend (no credentials in the sandbox), and the separate machines (each hostd is "remote" only by protocol and `HOME`; all share the hostname `debuerreotype`). Tasks are `noRepo`, so fakeagy's `cd work` probe fails by design.
- `sqlite3` and `shellcheck` were installed from apt.

## Findings

| # | Finding | Outcome |
|---|---|---|
| F1 | **A run pinned to a non-default remote hostd never got its MCP endpoint.** `Remote.Carry` always dialed `DefaultName()`. On a registry-only daemon, every `ws-a` run failed with `antigravity: carrying the run's MCP endpoint to the grain hostd's machine: no grain hostd is connected to this daemon`. With a remote default, the listener would have opened on the wrong machine. | **Fixed**: `Remote.CarryHostd`/`CarryHTTPHostd`/`Carrier(name)`, used by `cliHostdHost`. Tests: `TestACarryGoesToTheNamedHostdAndServesKeepAliveRequests` (`pkg/hostd`) and the carrier check in `TestRunsRouteToTheirTasksHostdWithCapacityAndPerHostdPauses`. |
| F2 | **Every request after the first on a kept-alive connection through a carried listener got 502 `context canceled`.** Reproduced with `curl url --next url`: 200, then 502. Cause: `mux.ErrTimeout` was a plain error. `net/http` aborts its background read between requests with a past deadline, and only ignores a timeout `net.Error`. Anything else is read as the client gone, which cancels the connection's context. Any real CLI's HTTP client reuses connections, so every MCP call but the first failed on a remote hostd. | **Fixed**: `ErrTimeout` is a timeout `net.Error` and `os.ErrDeadlineExceeded`. The pkg/hostd test above reproduces the 502 without the fix; `mux_test.go` asserts the error contract. |
| F3 | **An unmatched or unknown selector was queued, not refused.** `POST /api/tasks` with `hostd: "gpu=no"` or `"ws-nope"`, and `PUT /api/tasks/{id}/hostd` `gpu=no`, were accepted. Each run was then `setup-failed` (`no hostd in the registry is labelled gpu=no`) and backed off in the queue indefinitely. | **Fixed**: filing and re-pinning check the registry the way `SelectHostd` does. 400 `no hostd in the registry is labelled gpu=no (Settings -> Hostds)` / `hostd ws-nope is not in the registry`. A registered but disconnected hostd is still accepted. Tests: `pkg/ui/taskhostd_test.go`, `tests/security/hostd_routing_test.go`. |
| F4 | **A hostd setup failure is read as a provider refusal.** F1's three fast, identical failures tripped the "unrecognised refusal burst" heuristic: `usage limit reached; 3 runs failed within 5m0s … read as the provider refusing antigravity`. That paused `agy-ambient@hostd/ws-a` for 15 minutes, the attempts were recorded as `paused`, and the pause survived a restart. | Reported. With F1 fixed this path no longer triggers, but any persistent hostd-side setup error (one that is not `ErrRemoteNotConnected` while waiting) would do the same. Follow-up proposed. |
| F5 | **A hostd-scoped pause's wait reason drops the hostd.** A task waiting on `ws-a`'s pause says `model fake-agy-model on connection "agy-ambient" is paused after a usage limit`. It does not say the pause is `ws-a`'s alone, though `GET /api/pause` lists `agy-ambient@hostd/ws-a`. | Reported (small wording follow-up). |
| F6 | **PR #540's stated limit "runs on a non-default hostd are not re-adopted after a restart" no longer holds.** The run was left running on `ws-a`, adopted 7 s after the restart, and finished (item 4). | Reported: the plan's and PR's wording is out of date. |
| F7 | The plan's `grain task show` does not exist on the host CLI (the sandbox CLI has `grain task read`). The queue reason was read from the API's `activity`, which is what the board and task page show. | Reported. |
| F8 | `grain hostds list` shows `local` as `-` (not connected / unknown) while the co-located hostd is serving its socket and running work. | Reported, for §1 (Settings → Hostds/registry display). |

## Walkthrough, item by item

### 1. A run routed off the default hostd (`item1-ws-a.sh`, `item1.out`)

Before F1 the run failed at the carry. After F1 and F2:

- The fakeagy report's `home` and cwd are `$HR/ws-a/home/.grain/hostd-stage/sessions/antigravity/7/…`, and `ppid` is `ws-a`'s hostd. `ps` shows `fakeagy …` as a child of `grain hostd -server … -name ws-a …`.
- `mcpUrl` is `http://127.0.0.1:<port>/<secret>/mcp`, the carried listener.
- The tool call succeeds (`write_file: Wrote HOSTD_AGY.md`), and the attempt is `succeeded`: `the run made 2 tool call(s) [write_file x2]`.
- The attempt (`GET /api/tasks/7`) shows `{"hostd":"ws-a","instance":"debuerreotype","owner":"alice@example.com"}`.

`task_hostd` = `ws-a`. `task_run_hostd` = `7-1 | ws-a | debuerreotype | alice@example.com`. Both match. The unpinned task 4 ran on `local`, with HOME under the daemon's `state/agent-sessions` and `ppid` = the local hostd. Its attempt carries no `task_run_hostd` row, because `recordRunHostd` writes one only for a routed run.

### 2. Labels (`items2-6.sh`, `items2-3.out`)

- `gpu=yes` matches both `ws-a` and `ws-b`, and went to `ws-a` (run in `ws-a/home/.grain/hostd-stage`).
- After F3, `gpu=no` and `ws-nope` are refused at `POST /api/tasks` with the messages above, and `PUT …/hostd gpu=no` is refused the same way. Before F3 they were queued, then `setup-failed` and backing off.

### 3. Waiting and capacity

- **Waiting:** with `ws-b` stopped, a task pinned to `ws-b` went queued → `waiting for hostd ws-b` within a second. The same text is on the task (`activity`) and on the board's queued list (`GET /api/tasks?state=queued`). Restarting `ws-b` at 06:28:20, it connected at 06:28:21 and the task ran and succeeded at 06:28:21, within one 3 s cycle.
- **Capacity:** with `capacity: 1` on `ws-a`, two pinned tasks. The first ran 06:28:25–06:28:33. The second waited with `waiting: hostd ws-a is at its capacity (1 of 1 runs); the run waits for one to finish`, and started and finished at 06:28:33 once the first had.

### 4. Daemon restart under a run on `ws-a` (`item4-restart.sh`, `item4.out`)

With fakeagy live on `ws-a` (pid under `ws-a`'s hostd), I sent the daemon SIGTERM:

- The daemon logged `leaving run 21-1's CLI running in grain hostd at cursor 0`, then `left running in its run host for the next daemon to adopt`.
- The CLI stayed alive, and `grain hostd -status` on `ws-a` listed it as `detached, no daemon attached`.
- On restart, `ws-a` redialed (backoff 2s→4s), and the daemon logged `adopted grain hostd process … for run 21-1 at cursor 0 after a 7s gap` and `grain hostd ws-a adoption: 1 adopted, 0 lost`.
- The run finished `succeeded`, and `task_run_hostd` kept `21-1 | ws-a`.

The operator-facing outcome: the task stays `running` through the restart and completes. It is not lost and not silently hung (F6).

### 5. Staging and carry on the remote home (`item1.out`)

- Under `ws-a`'s `~/.grain/hostd-stage`, every directory is `drwx------` and every regular file (`settings.json`, `hooks.json`, `mcp_config.json`, the diagnostic log) is `-rw-------`. After the run, the run-scoped `grain-run` tree and files are removed. The empty session skeleton directories stay.
- The carried listener answered:
  - 200 with no headers;
  - 403 with `Origin: http://evil.example`;
  - 403 with `Host: evil.example`;
  - 403 with `Host: localhost:<port>`;
  - 404 on the right host without the secret.
- **FIFO and symlink refusal** were not re-walked on the live home. In this build every run's agy home is created fresh under `hostd-stage/sessions/antigravity/<task>`, so a planted FIFO is reached only through a persistent-home path. `pkg/hostd/stage_linux_test.go` (symlink, FIFO, mode and temp-area confinement) covers it and passes on this branch. Not proved live.

### 6. Per-hostd pause (`item6.out`)

- A `ws-a` run whose fakeagy printed agy's spent-quota text (`mode=limit`: `Individual quota reached … Resets in 2h0m0s.`) was recorded as `paused`.
- `usage_limit_pause` holds exactly one row, `connection|agy-ambient@hostd/ws-a|fake-agy-model`, until +2h. `GET /api/pause` (Settings → Models) lists that connection with `canLift: true`.
- A second `ws-a` task waited (wording: F5). A `ws-b` task and an unpinned `local` task on the same connection both ran and succeeded straight away.

### 7. UI

- Screenshots over `grain demo` (`node e2e/screenshot.mjs --pages /new,/tasks/1,/tasks/9`) at desktop (1440) and phone (390), light and dark. The contact sheet and two of the shots are in [`2026-11-03-hostd-routing/screenshots/`](2026-11-03-hostd-routing/screenshots/).
- I could not look at them: the sandbox's file reader shows PNG bytes, and `LOOK=1` has no Gemini key here. Layout is checked by bounding boxes instead: `ui/e2e/task-hostd-editor.spec.js` (section and editor inside the column at 1440/390; Save/Cancel on one row) and `cli-run-locations-hostd.spec.js`.
- This branch changes no UI code. The spec now pins the demo's registered `build-runner`, since F3 refuses unregistered names.
- `task-hostd-editor.spec.js` and both `cli-run-locations-hostd.spec.js` specs pass on this branch (3 passed).
- Not walked: New Task → Advanced options' Hostd field by hand (pin by name, by labels, and the new 400 message shown there).

## Records (`sqlite3`)

Every routed run had a `task_hostd` row equal to its selector, and a `task_run_hostd` row naming the hostd it really ran on (checked against the CLI's `ppid` and `HOME`), with instance `debuerreotype` and the registry owner:

- 7-1 `ws-a`/alice
- 12-1 `ws-a` (from `gpu=yes`)
- 13-1 `ws-b`/bob
- 14-1, 15-1 `ws-a`
- 16-1 `ws-a` (paused)
- 18-1 `ws-b`
- 21-1 `ws-a`

`task_run_capability_audit` had no rows for these runs: noRepo tasks with no capabilities granted. Its `hostd` column was therefore not exercised. Per PR #540 it would name the default hostd, since capability servers are not per-hostd. That is a stated limit, not redesigned here.
