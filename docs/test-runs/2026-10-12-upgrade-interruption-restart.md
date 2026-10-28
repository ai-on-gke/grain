# Walkthrough: interrupted runs, restart gap, persisted proxy tokens, restarting banner

- **Date:** 2026-10-13 (09:00–09:31 UTC)
- **Plan:** `docs/test-plans/2026-10-12.md` §11 (non-disruptive upgrade stages 0–1)
- **Base under test:** `main` at `f3dfcaa5c`. The features (`6e0b3c0e9`, `c045a170f`, `9dbc6a06d`, `b2a883e27`) were
  confirmed in `origin/main` with `git branch -r --contains`. By the time of this run they were also on `candidate`.
- **Task:** 3294 (filed by task 3209)

## Environment

- Debian 12 sandbox, Go 1.26.6, Node v20.19.0, Playwright 1.62.1 with Chromium headless shell 151.0.7922.34. The
  browser was installed with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`, because the
  package-cache proxy re-signs the Playwright CDN.
- A real `grain daemon` built with `go build -tags grain_e2e ./cmd/grain`, with the frontend embedded
  (`cd ui && npm ci && npm run build`). The `grain_e2e` tag adds the local-directory sandbox backend the browser
  suite uses. This sandbox has no granule or container runtime.
- Command line (`/tmp/wt/start.sh`), always over the same persistent `-data-dir`:
  `grain daemon -data-dir /tmp/wt/data -sandbox-dir /tmp/wt/sbx -ui-addr 127.0.0.1:8420 -agent-framework claude
  -claude-path /tmp/wt/claude-stub -claude-oauth-token-file /tmp/wt/claude-token -poll-interval 2s -max-workers 1`.
- Readiness: `start.sh` polled `GET /api/sandboxes` every 100 ms. It was ready 300 ms after each start (1.5 s on the
  first, which generated the secrets key).
- Stop: `kill -TERM <pid>` and `kill -9 <pid>`. Restart: the same command line again.
- Browser: `/tmp/wt/watch.cjs`, a Playwright script that opens `/tasks/2` with `X-Grain-Admin-Mode: on`. Every 250 ms
  it logs any change to `[data-testid="disconnected-banner"]`, to any `[role=alert]` or snackbar, and to the
  Timeline's attempt rows. It also logs every `/api/config` answer's `restarting`.
- Store: read with `sqlite3` on `data/store/grain.db` (`task_run`, `task_streak`).
- CLI: the same binary (`grain list`, `grain get`, `grain create`), timed with a wrapper that records wall-clock
  milliseconds and the exit code.

### Stubs

| Boundary | Stub | Why |
|---|---|---|
| Agent / model (the claude CLI) | `/tmp/wt/claude-stub`, a bash script. It prints claude's stream-json `system/init` with a `session_id`, or keeps the `--resume` id when it is given one. Then it prints an assistant line and one `run_command` tool call, sleeps 900 s, and prints a `result`. It also copies the run's `.claude-proxy-token` from the sandbox into a log | Holds a run open, names a resumable session the way claude does, and exposes the minted proxy token |
| Anthropic API behind the claude model proxy | **Not stubbed.** The daemon has no upstream override flag (`modelproxy.Proxy.Upstream` is set only in tests). A token grain *accepted* was forwarded to `api.anthropic.com` with the dummy credential `tok`, and Anthropic answered `401 invalid x-api-key` (its own body, with a `request_id`). A token grain *refused* got grain's own 401 body ("This grain Claude proxy does not recognise that token") and a `refused … unknown-token` journal line. Three requests in all reached Anthropic, each carrying only the dummy credential | The two bodies tell "grain accepted it" apart from "grain refused it" without a model call |
| Connection resets (step 5) | `/tmp/wt/rst.py`, a TCP listener on 127.0.0.1:8499. It reads the request line, then closes with `SO_LINGER 0` (an RST), logging each connection. It is a network fault, not a fake grain: it never answers HTTP | No other way to make a reset on demand |
| Forges | None needed. Every task was `-no-repo` | |

Nothing in grain's own API, store or UI was mocked.

## Step 1: SIGTERM with a live run and the page open

1. `grain create -approve -no-repo -capability claude-proxy -title "long run for restart walkthrough"` filed task 2.
   Run `2-1` was dispatched at 09:03:17, and the stub named session `sess-1791882197825890639`.
2. Playwright had `/t/default/tasks/2` open, showing "Running". At 09:04:34.652 the daemon got SIGTERM. It exited
   **54 ms later** (09:04:34.706). Journal:
   ```
   09:04:34 grain daemon: shutting down; waiting up to 4m30s for 1 run(s) to release their sandboxes: [2-1]
   09:04:34 orchestrator: run 2-1 (task 2): orchestrator: run 2-1: orchestrator: the daemon driving this run was stopped
   09:04:34 grain daemon: shutdown interrupted 1 run(s): 1 finished as interrupted, 0 left for the next start to recover; none counts against its task
   ```
3. Browser log (excerpt):
   ```
   09:04:32.900 config 200 restarting=undefined
   09:04:35.889 config request failed: net::ERR_CONNECTION_REFUSED
   09:04:36.137 banner: "This deployment cannot be reached: the connection to it failed. Unreachable since 9:04:35 AM -- retrying automatically; …"
   09:04:56.926 config 200 restarting=undefined   (banner gone, no reload)
   ```
   The banner said **"cannot be reached", not "grain is restarting"**. This is **finding 1**. No error toast appeared:
   the only alert on the page throughout was the standing "Team admin: 3 setup steps left" checklist notice.
4. The daemon was restarted at 09:04:49.7 and was ready 300 ms later. The page recovered on its own at the next
   backed-off poll (09:04:56.9). Run `2-2` was dispatched at 09:04:49, the same second.
5. After the restart:
   - `task_run 2-1`: outcome `interrupted`, detail `interrupted by daemon shutdown or upgrade; re-queued for next
     dispatch; the run made 1 tool call(s) [run_command x1] …`.
   - Task note (author `grain`): "Run 2-1 was interrupted: the grain daemon running it was stopped for an upgrade or
     a restart before it finished. Nothing about this task caused it, so it does not count against the task's
     failure streak, and the task has been queued again. …"
   - The streak was unchanged: no `task_streak` row for task 2, and `GET /api/tasks/2` had `failedAttempts: null`.
   - **It resumed with its conversation.** `suspendedSession.token` was `sess-1791882197825890639` (the session 2-1
     named), and the stub for 2-2 was started with `--resume sess-1791882197825890639`.
   - The Timeline showed "Attempt #2 · Running" over "Interrupted (daemon restarted)".

**Result:** pass, except the banner wording (finding 1).

## Step 2: `kill -9` with a live run

1. With run `2-2` live and the page open, the daemon got `kill -9` at 09:06:21.84. The browser showed "cannot be
   reached" (expected, since a killed process cannot announce anything) and recovered by itself at 09:06:32.6.
2. Restarted at about 09:06:31. Journal:
   `orchestrator: recovering 1 run(s) the previous daemon was stopped in the middle of; each is finished as
   interrupted and its task queued again without a strike`, followed by `reaped 2 sandbox(es) left behind by a
   previous process`.
3. `task_run 2-2`: outcome **`interrupted`** (not `orphaned`), detail `the daemon driving this run was stopped before
   it finished (an upgrade or a restart); recovered at the next daemon startup and re-queued without a strike`.
   Still no streak row. Run `2-3` was dispatched in the same second.
4. The model-proxy token minted for 2-2 was still in `modelproxy-claude-tokens.json` after the restart, and the
   proxy **still forwarded requests on it** (Anthropic's 401, no grain refusal), even though 2-2 was already
   finished as `interrupted`. This is **finding 2**, fixed below.
5. Not a grain finding, but worth knowing: with the `grain_e2e` local-directory backend, the killed daemon's agent
   process (the stub, pid 6972) kept running after the restart. Real backends delete the sandbox at startup
   (`ReapOrphans`).

**Result:** pass for the run's outcome. Finding 2 for its token.

## Step 3: `state/restart-gap.json` and the gap log lines

After the step 1 SIGTERM restart, `restart-gap.json` read `{"last":{"mode":"host binary","startedAt":…,"services":[…]}}`.
Each of the five services had `downAt` (when the old process closed it) and `upAt` (when the new one bound it). The
journal said:

```
09:04:49 grain daemon: model proxy (claude) 15.2s (unavailable across the restart, host binary)
09:04:49 grain daemon: model proxy (openai) 15.2s (unavailable across the restart, host binary)
09:04:49 grain daemon: model proxy (gemini) 15.2s (unavailable across the restart, host binary)
09:04:49 grain daemon: API 15.3s (unavailable across the restart, host binary)
09:04:49 grain daemon: git proxy 15.3s (unavailable across the restart, host binary)
```

15.3 s is the 15 s the walkthrough waited before restarting, plus 300 ms of startup. Two quick back-to-back
restarts logged `API 300ms` each (09:15:00, 09:15:11).

- **SIGQUIT** (the process was told nothing first and died mid-drain): the new process logged `API at most 3m43.1s
  (the previous process was killed before it closed this listener; measured from when it was told to stop)`. That is
  measured from the earlier SIGTERM, which it had recorded as `down.shutdown`.
- **`kill -9` with no stop first:** the old process wrote no `down` times, so the 09:06:32 start logged **no gap line
  at all**. Its `services` entries carry `upAt` only. This follows from the design, but the doc did not say it. It now
  does (`docs/non-disruptive-upgrade.md`, "The gap is measured").
- While the daemon was down, the file's `down` map held `shutdown` and each listener's close time, all within
  4 ms of each other.

**Result:** pass.

## Step 4: model-proxy tokens across a restart

| Check | Result |
|---|---|
| File after the first mint | `secrets/modelproxy-claude-tokens.json`: `stat -c %a` = **600**, owner `debian`. `jq .` parses it as `[{"id":"756be8ba55d3bf96","secret_sha256":"85993f19…","owner":"2-1","team":"default","connection":"default-claude","expires":"…12:48:17Z","until":"…12:48:17Z"}]`. `secret_sha256` equals `sha256sum` of the token's secret half. `grep` for the raw secret: **0 matches** |
| Token of a run that ended through SIGTERM (2-1) | Revoked at the run's own teardown and gone from the file. Presented after the restart: grain's 401 "does not recognise that token" |
| Token minted before a restart, run still live (2-2, before `kill -9`) | Accepted: forwarded, with Anthropic's 401 for the dummy key |
| Same token after `kill -9` and restart (2-2 now `interrupted`) | **Still accepted** with the `main` build (finding 2). With the fix: `claude proxy: revoked 2 token(s) held by run(s) that ended with the previous process`, and the token is refused |
| Token with no run owner, persisted with a future expiry, after a restart | Accepted (forwarded) |
| Same file, entry with a past expiry, after a restart | Refused with grain's 401, and the entry was dropped from the file on load |
| File corrupted (`[{"id": "truncated`), then restart | Logged: `claude proxy: modelproxy: reading …/modelproxy-claude-tokens.json: unexpected end of JSON input; runs that held a token before this restart will be refused until they are given another`. The file was **kept byte-for-byte** (`sha256sum -c` OK) and stayed mode 600 |

The two hand-made entries (persisted and expired) were written in the daemon's own format with `jq`, because a run
token is always revoked or deadline-bound by its run.

One observation, not filed: once the file fails to load, the registry never calls `PersistTo` successfully, so tokens
minted for later runs in that process (2-6 here) live in memory only. The log line covers runs from *before* the
restart, not these.

**Result:** pass after the fix for finding 2.

## Step 5: the `grain` CLI while the daemon is down

| Call | Situation | Wall time | Outcome |
|---|---|---|---|
| `grain create -no-repo …` (POST) | daemon stopped, refused dials | **116.7 s** | exit 1, 23 attempts: `grain: grain's daemon at http://127.0.0.1:8420 did not answer for 1m57s (23 attempts): it is most likely restarting, for an upgrade or after a crash; nothing was lost by this call failing, so try it again once grain is back (last error: Post "http://127.0.0.1:8420/api/tasks": dial tcp 127.0.0.1:8420: connect: connection refused)` |
| `grain list` (GETs), `main` build | same | **347.3 s** | exit 1, same sentence for `GET /api/tasks`, but after three budgets: **finding 3** |
| `grain list`, fixed build | same | **114.6 s** | exit 1, the same sentence for `GET /api/config` |
| `grain create` (POST) against `rst.py` | every connection reset | **19 ms** | exit 1, one connection, no retry: `reaching grain server at http://127.0.0.1:8499: Post ".../api/tasks": read tcp …: read: connection reset by peer` |
| `grain get 2` (GET) against `rst.py` | same | **117.1 s** | exit 1, retried. 23 attempts reached the listener about 7 s apart once backoff had hit its 5 s cap plus jitter. `…did not answer for 1m57s (23 attempts)… (last error: Get ".../api/tasks/2": read tcp …: connection reset by peer)` |
| `grain get 2`, `grain create`, `grain list` | started with the daemon down, daemon started 20 s later | 24.8 s, 24.5 s, 20.7 s | all exit 0. The POST filed task 3 exactly once |

**Result:** pass after the fix for finding 3. The method-aware policy did what `c045a170f` says: refused dials are
retried for any method, a reset is retried for GET and not for POST, and the final error is the
`DaemonUnavailableError` sentence.

## Findings

### 1. The "grain is restarting" banner does not show for a plain SIGTERM restart (filed, not fixed)

The UI shows the restarting wording only if its *last* `/api/config` answer said `restarting: true`. It polls every
3 s. The daemon answers `restarting: true` only between SIGTERM and closing the API listener, which took 54 ms here.
So any SIGTERM or `systemctl restart` whose drain beats the next poll shows "cannot be reached", the wording this
feature was meant to replace. Only an upgrade (`upgrader.Status().Phase == PhaseRunning`, which lasts through
checkout and build) reliably shows the restarting banner.

**Deterministic reproduction:** open any page against a daemon, and `kill -TERM` it. Every `/api/config` answer the
page receives has no `restarting` field, and the banner reads "This deployment cannot be reached". Browser log above.

Fixing it needs a design choice. Holding the API open for a poll interval lengthens every restart's gap, so the
alternatives are pushing the state over a live connection, or a response header. It was filed with `propose_task`
for `main`, depending on this task.

### 2. A recovered run's persisted model-proxy token stays valid (fixed)

Tokens persist across restarts (`9dbc6a06d`), but only the run's own teardown revokes one. After `kill -9`, or a
drain cut off by the stop timeout, `RecoverOrphanedRuns` finishes the run as `interrupted`, yet the new process loads
its token from the file and forwards on it with the team's credential until the run's deadline (3 h 42 m away here).

**Deterministic reproduction:** start a run holding `claude-proxy`, `kill -9` the daemon, restart it, and present the
run's token to the claude proxy. It is forwarded upstream rather than refused.

**Fix (`b8196b184`):** right after `RecoverOrphanedRuns`, the daemon revokes every persisted token whose owner is a run
the store says has finished. This adds `Tokens.Owners` and `Store.RunFinished`. Live runs, which a later stage may
adopt, and owners that are not runs (a custom backend's task-owned token) are left to their own expiry.
**Regression test:** `pkg/daemon/modelproxy_recovery_test.go` (`TestRevokeFinishedRunTokensDropsWhatRecoveredRunsHeld`).
It fails without the revoke. Verified live: `claude proxy: revoked 2 token(s) held by run(s) that ended with the
previous process`, and 2-2's token was then refused.

### 3. A dead agent CLI whose child holds its stdout hangs the run and the shutdown drain (fixed)

The walkthrough's own cleanup hit this. A `pkill -f claude-stub` also matched the daemon's command line (it carries
`-claude-path /tmp/wt/claude-stub`), so the daemon got SIGTERM at the same moment as the stub's bash was killed. The
stub's `sleep` child still held stdout, and the daemon sat in `shutting down; waiting up to 4m30s for 1 run(s)` on
run 2-3 for more than 40 s, with nothing alive but that `sleep`. A SIGQUIT dump showed the run's goroutine in
`os/exec.(*Cmd).awaitGoroutines` under `agent.LocalHost.Run`. `procgroup`'s group kill only fires when ctx is
cancelled while the CLI is still alive, and `LocalHost.Run` set no `WaitDelay`. So a run whose agent CLI dies on its
own while something it forked (the `mcpserver`, a tool's background process) keeps the pipe open hangs for as long as
that process lives. Shutdown then waits on it until the drain budget runs out.

**Deterministic reproduction:** `LocalHost{}.Run` of `sh -c 'echo started; sleep 60 & kill -9 $$'` returns after
60 s, not at once.

**Fix (`ae40b640c`):** `LocalHost.Run` sets a 5 s `WaitDelay`, the bound `pkg/upgrade` already uses. A CLI that exits
0 while a child still holds the pipe is still a success, and that is logged. **Regression test:**
`pkg/agent/host_waitdelay_test.go`. It takes 60 s per case without the fix and 5 s with it. Verified live: with the
stub's bash killed (`kill -9 <pid>`) and its `sleep` child alive, run 2-7 finished `failed` 5.0 s later
(`claude: running claude: signal: killed`). That is the right outcome for a killed agent, and it is the one failure
on task 2's streak.

### 4. `grain list` takes about 6 minutes to fail against a stopped daemon (fixed)

`cmdList` asks for `/api/config` (task-list configuration), then `/api/settings`, then `/api/tasks`. It swallows the
first two failures and falls back to defaults, and each call spends the client's full 2-minute `RetryPolicy`.

**Deterministic reproduction:** stop the daemon and run `grain list`: 347 s, against 117 s for `grain create`.

**Fix (`21f6e8e68`):** a `DaemonUnavailableError` from the first call is returned at once. **Regression test:**
`cmd/grain/list_unavailable_test.go` uses a gateway-503 server and a 200 ms policy. Without the fix it asked all 3
endpoints, 5 attempts each. Verified live: 114.6 s.

### 5. The design doc still said a leftover run ends `orphaned` with a strike (fixed, docs)

`docs/non-disruptive-upgrade.md`'s "What a restart costs today" table predates `6e0b3c0e9`. It now says
`RecoverOrphanedRuns` finishes such a run as `interrupted` with no strike, which is what step 2 observed. The gap
paragraph now says that a process killed without a stop leaves nothing to measure from.

## Fixes and follow-ups

| Commit | What |
|---|---|
| `b8196b184` | `fix(modelproxy)`: revoke recovered runs' persisted proxy tokens at startup (finding 2) |
| `ae40b640c` | `fix(agent)`: `LocalHost.Run` stops waiting on a dead CLI's output after 5 s (finding 3) |
| `21f6e8e68` | `fix(cli)`: `grain list` gives up after one retry budget (finding 4) |
| this report's commit | the report, and the doc corrections (finding 5) |
| proposed task | the restarting banner for plain SIGTERM restarts (finding 1), for `main`, depending on task 3294 |

All four fixes are `fix:` commits on `main` and candidates for porting to `candidate`, alongside `6e0b3c0e9` and
`9dbc6a06d`.

## Notes for whoever runs this next

- Don't use `pkill -f claude-stub`. The daemon's own command line contains the stub's path, so it matches. Kill the
  stub by pid (`pgrep -f '^/bin/bash /tmp/wt/claude-stub'`).
- `/usr/bin/time` and `bc` are not in the sandbox image, and `sqlite3` needs `apt-get install`. Playwright's browser
  download needs `NODE_EXTRA_CA_CERTS` pointing at Grain's CA.
