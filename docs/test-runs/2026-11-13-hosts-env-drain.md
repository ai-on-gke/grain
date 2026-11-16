# Hosts: env_deny_v2, drain and Exit delivery, the git built-in on 4254 — 2026-11-13 (task 5363)

- **Date:** 2026-11-13, 03:56–04:20 UTC, one run (attempt 2; attempt 1 was paused on a usage limit before it pushed anything).
- **Plan:** `docs/test-plans/2026-11-13.md` §11 ("Hosts: `env_deny_v2`, drain and the git built-in's port"), items 1–4 under "What is NOT proved". PRs #1326, #1325, #1323, #1287, #1264, #1173, #1391, #1390, #1205, #1433.
- **Task:** task 5363 (filed by task 4725), branch `grain/098073e4/task-5363-walkthrough-host-env-deny-v2`, base `main`.
- **Commit tested:** `51844bb2c` ("Merge pull request #1451"), built as `v0.0.0-20261113032736-51844bb2c12b`. The fixes below were checked again on rebuilds of this branch (`…-4ac221f6d3e6+dirty`).
- **Outcome:** items 1–4 hold. One bug was found and fixed: **grain-agentd had no drain of its own** (F1). Two follow-ups were filed: F2 and F3. Two smaller observations are recorded as F4 and F5.

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, kernel 6.1, `linux/amd64`, Go 1.26.10. systemd 252 is PID 1. |
| Daemon (real, SQLite) | `grain daemon -data-dir /var/tmp/hed/data -sandbox-dir /var/tmp/hed/sb -host-sandboxes -sandbox-port-base=0 -ui-addr 127.0.0.1:4292 -as walk-owner -poll-interval 1s -max-workers 3 -max-mergers 0 …` ([env.sh](2026-11-13-hosts-env-drain/env.sh)). Runs as the sandbox user and is restarted with `env.sh daemon-stop` / `daemon-start`. |
| Host | A second Linux user, `walkhost`, runs **`grain-agentd` as the real systemd `--user` unit that `grain-agentd install` writes** (`Restart=always`, `RestartSec=10`). It dials out to the daemon as host `walk` with a per-host token from `grain host register walk`. |
| systemd `--user` | The sandbox's logind does not load (`loginctl enable-linger` fails with `dbus-org.freedesktop.login1.service failed to load`). `sudo systemctl start user@1001.service` starts walkhost's user manager directly, which is what lingering does. The unit is driven with `systemctl --user` as walkhost and read with `journalctl _UID=1001`. |
| Agent CLIs | Stub `agy`/`claude`/`codex` shell scripts in `~walkhost/.local/bin` (env.sh). Each records its argv and environment, then sleeps for the seconds in `/var/tmp/hed/stub-sleep`, so drains and restarts land mid-run. |
| Runs | Real dispatch: `noRepo` tasks over `POST /api/tasks` + `/approve`, on an ambient model connection `walk-agy` (antigravity) placed on `hostd: walk`. Every daemon run starts **detached** on the host. |
| Older contract | grain-agentd **v3.6.0** (`c327523c7`), built from its tag in a detached worktree. Its features lack `env_deny_v2`. |
| Probes (`//go:build ignore`) | [envprobe.go](2026-11-13-hosts-env-drain/envprobe.go) and [drainprobe.go](2026-11-13-hosts-env-drain/drainprobe.go) talk to the host agent's local socket as walkhost. |

**Stubbed boundaries** (only what the sandbox lacks):

- the model provider (stub CLIs);
- host enrollment by `-join` (no identity proxy or device flow; a legacy per-host token was used instead);
- logind (the user manager was started directly);
- the git upstream (`http://127.0.0.1:9` plus a fake token secret);
- a container sandbox for item 4. `-host-sandboxes` refuses every attempt that holds a capability grant ("cannot keep a capability server's control plane out of a sandbox's reach"), and the container sandbox image (`grainai/sandbox-k8s`) was not pulled. Item 4's "a task reaches it on 4254" was therefore checked at the data plane itself (below), not from inside a run's sandbox.

## Item 1 — `env_deny_v2` on a real host

1. **HEAD host, a `clis:` entry with `env: ["*"]`** (`claude`, as written by `grain-agentd install`). `envprobe -name claude` started the entry once per variable with only that variable set ([item 1 transcript excerpt below](#item-1-transcript)).
   - Hello: protocol 4, features `… env_open logins_map hook_policy handover env_deny_v2`.
   - **36 refused, 1 admitted, 0 unexpected.** These were refused, each with `environment variable X is not one this entry accepts`:
     - every listed `env_deny_v2` name: `JAVA_TOOL_OPTIONS=-javaagent:/tmp/x.jar`, `JDK_JAVA_OPTIONS`, `_JAVA_OPTIONS`, `GIT_CONFIG_PARAMETERS`, `GIT_CONFIG_COUNT`, `GIT_SSH_COMMAND`, `GIT_ASKPASS`, `GIT_EXEC_PATH`, `PROMPT_COMMAND`, `SHELLOPTS`, `BASHOPTS`, `PS4`, `ZDOTDIR`, `PYTHONWARNINGS`, `PERL5DB`, `LUA_INIT`, `LUA_INIT_5_4`, `LUA_PATH`, `LUA_CPATH_5_3`, `DYLD_INSERT_LIBRARIES`;
     - the path-valued ones, with a value **inside `paths_under`** (the stage directory), which v1 admitted: `NODE_PATH`, `PYTHONHOME`, `PYTHONUSERBASE`, `PERL5LIB`, `PERLLIB`, `RUBYLIB`, `GCONV_PATH`, `CLASSPATH`, `XDG_CONFIG_HOME`, `OPENSSL_CONF`, `OPENSSL_ENGINES`, `OPENSSL_MODULES`;
     - the frozen v1 set, for comparison: `LD_PRELOAD`, `BASH_ENV`, `NODE_OPTIONS`, `PATH`.
   - **The child never saw them.** For every refused variable no child ran: the stub writes a file on every start, and no new file appeared.
   - **An ordinary variable passes.** `GRAIN_WALK_ORDINARY=hello` was admitted, and the child's recorded environment had it.
   - **The refusal is reported** both ways: to the requester as a Refusal, and in the host's journal (`hostd: refused: cli "claude": environment variable JAVA_TOOL_OPTIONS is not one this entry accepts`, 36 lines).
2. **The host agent's own environment is stripped.** `systemctl --user set-environment JAVA_TOOL_OPTIONS=… GIT_CONFIG_PARAMETERS=… NODE_PATH=/tmp OPENSSL_CONF=…`, then a unit restart: `/proc/<agentd>/environ` had all four. The admitted child's environment had none of them, only `HOME LANG LOGNAME PATH PWD USER` plus the two variables requested. `identityEnv` and `pass_env` are the only names copied from the host.
3. **A `capabilities:` entry.** grain-agentd refuses any `capabilities:` section, so this used a real `grain host` as walkhost, with a root-owned allowlist holding one `capabilities: stubcap: {command: …claude, env: ["*"], paths_under: […]}`. The same probe with `-kind capability` gave **the same 36 refused / 1 admitted** (`capability "stubcap": environment variable … is not one this entry accepts`).
4. **Older-contract host.** With grain-agentd **v3.6.0** swapped into the unit:
   - `GET /api/hostds` listed walk's features without `env_deny_v2`, and the probe saw `env_deny_v2=false`.
   - The same probe gave **5 refused / 32 admitted**. The v1 set is still refused, and v1 admits every v2 name, as `Hello.Features` says. `OPENSSL_CONF` was refused only because its value, a file under the stage, did not exist.
   - A real daemon run on v3.6.0 started and ended normally. The daemon sends a host only `HOME` and `AGY_CLI_DISABLE_AUTO_UPDATE`, as its log line `(env: HOME=… AGY_CLI_DISABLE_AUTO_UPDATE=true)` shows. It needs no `env_deny_v2` check, as `features.go` says.

**Result: holds.**

<a id="item-1-transcript"></a>

```
host build v0.0.0-20261113032736-51844bb2c12b protocol 4 env_open=true env_deny_v2=true
GRAIN_WALK_ORDINARY      admitted, child saw it
JAVA_TOOL_OPTIONS        refused, no child: the host agent refused: cli "claude": environment variable JAVA_TOOL_OPTIONS is not one this entry accepts
GIT_CONFIG_PARAMETERS    refused, no child: … GIT_CONFIG_PARAMETERS is not one this entry accepts
NODE_PATH                refused, no child: … NODE_PATH is not one this entry accepts
OPENSSL_CONF             refused, no child: … OPENSSL_CONF is not one this entry accepts
…
summary: 36 refused, 1 admitted, 0 unexpected
```

## Item 2 — drain under load, Exit delivery, restart, re-attach

### `grain-agentd -drain` as the plan says it: F1

`grain-agentd -drain` printed `flag provided but not defined: -drain` and exit 2. `systemctl --user kill -s USR1 --kill-whom=main grain-agentd` was silently ignored: same pid, no drain line in the journal, and the run finished as if nothing had happened. `grain host -drain` documents SIGUSR1 as its drain, but grain-agentd registered no handler, and Go ignores an unhandled SIGUSR1. A grain-agentd host could be drained only by the daemon, or by a `grain` binary the host does not carry. **Fixed here (F1).** The rest of item 2 used both the plain drain (`drainprobe`, the same OpDrain that `grain-agentd -drain` sends a host of another protocol) and, after the fix, the real `grain-agentd -drain` and SIGUSR1.

### Plain drain with three runs in flight and a daemon restart in the middle ([item2-drain.sh](2026-11-13-hosts-env-drain/item2-drain.sh))

The allowlist had `drain_cap: 25s` (re-read on every request). Three runs were in flight:

- **A**: a daemon run (4-1), detached, 8 s;
- **B**: a daemon run (5-1), detached, 120 s;
- **C**: drainprobe's own start, **attached** over the socket, 10 s.

```
[+  1.0s] in flight: cli agy pid 11344 run "4-1" detached=true
[+  1.0s] in flight: cli agy pid 11387 run "5-1" detached=true
[+  1.0s] in flight: cli claude pid 11410 run "" detached=false
[+  1.0s] drain answered
[+  1.0s] start right after the drain: draining=true err=the host agent refused: grain hostd is draining for an upgrade; …
[+  1.0s] hello while draining: draining=true
[+ 10.0s] attached run's Exit: code=0 signal="" draining=false error="" err=<nil>
[+ 28.1s] grain-agentd no longer answers on its socket
```

- **No new starts.** The probe's start right after the drain was refused as draining, and Hello said `draining=true`. The journal said `draining for an upgrade: refusing new starts; 3 process(es) still running`.
- **The attached run's Exit frame arrived** (C, +10 s) before the host exited (+28 s). PR #1325 holds.
- **A daemon restart during the drain re-attaches to the unread Exit.** The daemon was stopped 2 s into the drain (04:03:32) and started again at 04:03:45. A ended at 04:03:35, while no daemon was attached. When walk reconnected (04:03:49), the daemon logged `adopted grain hostd process … for run 4-1 at cursor 0 after a 17s gap` and `resuming adopted run 4-1`, read A's exit, and finished 4-1 at 04:03:49. PR #1391 holds. 4-1's outcome is `failed: the agent made no tool calls at all`; that is the stub's doing, not the drain's.
- **The drain-cap kill is marked interrupted.** At the cap the host logged `drain cap 25s reached: cancelling cli "agy" (pid 11387, run 5-1)` and `ended: code -1 signal "KILL" … draining true`. The re-started daemon was attached by then, and recorded 5-1 as **`interrupted`**: `antigravity: running agy: the host agent is updating (draining for its own upgrade); the run goes back to the queue: stopped because the host agent drained for an upgrade`. Task 5 went back to the queue with no strike and ran 5-2 on the restarted host. 5-2's prompt quotes attempt 1 as `ended "interrupted": …drained for an upgrade`. PRs #1323 and #1173 hold.
- **The service restarts.** `drained; exiting so the new build takes over` came at 04:03:55. At 04:04:05 the journal showed `Scheduled restart job, restart counter is at 1` and the unit started again. PR #1264 (`Restart=always`) holds on a real systemd `--user` unit.

### The real `grain-agentd -drain` after the fix ([item2-handover.sh](2026-11-13-hosts-env-drain/item2-handover.sh))

There were three detached daemon runs (10-1, 11-1, 12-1), 20 s each. `grain-agentd -drain` (exit 0) asked for a **cutover**: the host speaks this build's protocol, and grain-agentd has no in-place re-exec, so a handover would fall back to the same cutover.

- The journal said `cutting over to a new build: still taking new starts, and exiting the moment no run is running; 3 process(es) running now`.
- All three runs ended at 04:07:34, followed by `drained; exiting` in the same second. The daemon recorded **all three runs finished at 04:07:34**, with no `lost host … while streaming` line, so every Exit frame was delivered before the exit. That is PR #1325 on the real CLI path.
- systemd restarted the unit 10 s later.

**SIGUSR1 after the fix**, with one run (18-1, 12 s) in flight, logged `draining for an upgrade: refusing new starts; 1 process(es) still running`. The run then ended (04:15:46), followed by `drained; exiting`. The daemon recorded 18-1 finished at 04:15:46, and the unit restarted. `sudo -u walkhost env -u XDG_RUNTIME_DIR grain-agentd -drain` now finds the unit's socket (F1's second commit).

**Result: holds**, with F1 fixed.

## Item 3 — host away while the daemon starts

### Killing the host under its unit ([item3-away.sh](2026-11-13-hosts-env-drain/item3-away.sh))

Two runs were in flight, 13-1 and 14-1 (90 s each). The script stopped the daemon, then ran `systemctl --user kill -s KILL --kill-whom=main grain-agentd` and started the daemon at once.

- **The agent CLIs died with grain-agentd.** `ps` one second after the kill showed none. The unit uses systemd's default `KillMode=control-group`, so systemd ends every process left in the cgroup once the main process dies, and a detached run is in that cgroup.
- The restarted host kept the final spools (`kept the final spool of cli "agy" (run 13-1 …) for a daemon to read`). The daemon adopted both runs and read their `Draining` exits. It recorded both as `interrupted` (`…the host agent stopped while it ran`), re-queued them, and ran 13-2 and 14-2.
- Nothing was left running, and the reason names the host. But the same thing happens on any restart of the unit (a crash, `systemctl --user restart`, re-running `grain-agentd install`), and the reason starts "the host agent is updating (draining for its own upgrade)" for what was a kill. **Filed as F2.**
- walk was back after 10 s (`RestartSec`), inside the daemon's adoption wait, so this variant never took the "away at startup" path.

### A frozen host: live orphans and a host away ([item3-frozen.sh](2026-11-13-hosts-env-drain/item3-frozen.sh))

To leave live processes on a host that is away while the daemon starts, grain-agentd was frozen instead (`kill -s STOP --kill-whom=main`). Its runs (15-1, 16-1, 120 s each) kept running, but it neither answered nor reconnected.

- The daemon started at 04:09:51 with walk away. At 04:10:21 it logged `grain hostd walk has not dialed in again after 30s; deciding its runs without it`. At 04:10:51 it logged `adoption: 0 adopted …` and `recovering the host agent walk's host CLIs: … did not answer hello`.
- It recorded both runs **`interrupted`** (`the daemon driving this run was stopped before it finished …; recovered at the next daemon startup and re-queued without a strike`).
- The host was thawed at 04:10:51. In the same second the daemon logged `cancelling grain hostd process p-15bbbcf26536daf8 (task 15, run 15-1): the host agent did not answer at startup, so its run was finished as interrupted` (the same for 16-1) and `grain hostd walk is back; cancelled 2 process(es) of runs it did not answer for at startup`.
- The host logged `releasing cli "agy" (pid 15936, run 15-1): cancelled by the daemon` and `ended: code -1 signal "KILL"`. `ps` 15 s later showed none of the old processes. Only 15-2 and 16-2 (new attempts) ran.
- PR #1390 holds: the orphans were cancelled with a clear reason and not left to run for 120 s.

**Result: holds.** F2 is filed, and F4 is an observation.

## Item 4 — the git built-in on 4254 ([item4-git.sh](2026-11-13-hosts-env-drain/item4-git.sh))

1. `PUT /api/capability-servers` named `https://grain.dev/capabilities/git` with `builtin: git` and the config `{upstream: http://127.0.0.1:9, repos: [acme/widgets], token: $secret:walk-git/token}`. It **attached** (`attached, 1 method(s)`).
2. It listened on **`127.0.0.1:4254`** (`ss -ltnp`: `grain` pid …), off the git proxy's 9728. Before the PR the built-in always collided with 9728 in a daemon.
3. **The data plane on 4254** answered `401 Unauthorized`, `WWW-Authenticate: Basic realm="grain git-repo"`, `this attempt's git-repo credential is missing, wrong, or already released`. That is the git built-in itself serving on 4254.
4. Settings' Test (`POST /api/capability-servers/test`) returned `ok: true` with the `git-repo` method.
5. **`grain health` was clean** (exit 0).
6. **4254 occupied** (`socat TCP-LISTEN:4254`) and the daemon restarted: **`grain health` exited 1** with:

   ```
   FAILING: capability servers out of use -- no grant is minted from one until it attaches again:
     https://grain.dev/capabilities/git (refused its saved config): … git: listen tcp 127.0.0.1:4254: bind: address already in use
   grain: health: failing: 1 capability server(s) out of use
   ```

7. With 4254 freed and the daemon restarted, the built-in was attached again.

- **Not exercised:** a run's sandbox reaching 4254 through its forwarded loopback port. This deployment's `-host-sandboxes` refuses every attempt holding a capability grant (task 17 was refused at admission: "this deployment's sandboxes are directories on grain's own host … so it runs no attempt holding a capability grant"), and no container sandbox image was available. `pkg/cli/sandboxnet_test.go` (PR #1433) covers the forwarding.
- **Finding (filed, F3):** since PR #1433 the listener is bound at configure time, so a configured built-in whose port is taken fails *in configure*. The registry then files it as **`needs-config`** and the daemon logs `not configured`. `grain health` still reports it (a configured built-in in needs-config counts), but labels it "(refused its saved config)", which sends an operator to the config rather than the port.

**Result: holds** (health reports the failure). The in-sandbox reach was not exercised, and F3 is filed.

## Findings

| # | What | Outcome |
| --- | --- | --- |
| F1 | `grain-agentd -drain` was an unknown flag, and SIGUSR1 (which `grain host` documents as its drain) was ignored, so a grain-agentd host could not be drained by hand. From a `sudo -u`/`su` shell, the default socket (`/tmp/grain-agentd-<uid>/…`) was not the one the unit listens on (`/run/user/<uid>/…`). | **Fixed** in this PR: commits `4ac221f6d` (`-drain`, SIGUSR1) and `1767f5c04` (socket default). Regressions are in `pkg/agentd/drain_linux_test.go`, and `docs/grain-agentd.md` is updated. |
| F2 | Under `grain-agentd.service` (default `KillMode=control-group`), any end of the main process (crash, SIGKILL, `systemctl --user restart`, re-running `grain-agentd install`) kills every detached agent CLI. The runs are recorded as interrupted with "the host agent is updating (draining for its own upgrade)", a misleading reason for a kill. | Filed: decide `KillMode` for the unit and word the reason for a host that stopped, not drained. |
| F3 | A configured built-in whose data-plane port is taken is filed as `needs-config` and labelled "refused its saved config" in `grain health`, though the config is fine. | Filed |
| F4 | The startup adoption pass waits about 30 s for an away dial-in host, then about 30 s more for hello. During that time the daemon's first cycle (every other task) waits too. The runs it finishes say "the daemon driving this run was stopped", not that the host was away. The cancel log line itself is clear. | Observation, not filed |
| F5 | `grain-agentd -h` gives the default `-socket` as the `/tmp` path when run without `XDG_RUNTIME_DIR`. After F1's second commit it is the `/run/user/<uid>` one whenever that directory exists. | Covered by F1 |

### F1 reproduction (before the fix)

```
$ sudo -u walkhost XDG_RUNTIME_DIR=/run/user/1001 ~/.local/bin/grain-agentd -drain
flag provided but not defined: -drain                      # exit 2
$ systemctl --user kill -s USR1 --kill-whom=main grain-agentd
# journal: "Sent signal SIGUSR1 to main process 11596" and nothing else; same MainPID, run 9-1 unaffected
```

After the fix, `go test ./pkg/agentd` passes. `TestDrainFlagEndsAHostAgent` drives a real `hostd.Server` through `-drain` until `Serve` returns. `TestDrainFlagSaysWhenNothingAnswers`, `TestSIGUSR1Drains` and `TestDefaultSocketFallsBackToTheUsersRunDir` cover the rest. On the walkthrough host, `-drain` (with and without `XDG_RUNTIME_DIR`) and SIGUSR1 both drain and restart the unit (item 2).

No host-agent wire field, op, allowlist key or login name changed, so the contract block in `design/design/agentd-stable-contract.design.md` is unchanged. The change is in `pkg/agentd` only, not under `pkg/runhost/*` or `pkg/capability/*`.

## Helpers

All are under [`2026-11-13-hosts-env-drain/`](2026-11-13-hosts-env-drain/):

- `env.sh`: environment setup (daemon, walkhost, the systemd `--user` unit, connection, tasks).
- `envprobe.go`: item 1.
- `item2-drain.sh` + `drainprobe.go`: plain drain, attached and detached runs, daemon restart.
- `item2-handover.sh`: the real `grain-agentd -drain`.
- `item3-away.sh` (SIGKILL) and `item3-frozen.sh` (SIGSTOP): host away at daemon start.
- `item4-git.sh`: the git built-in on 4254 and `grain health`.
