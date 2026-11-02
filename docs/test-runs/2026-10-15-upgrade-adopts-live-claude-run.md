# A real upgrade adopts a live claude run (2026-10-15)

Task 3606, the hand run of stage 2c of
[`non-disruptive-upgrade.md`](../../design/design/non-disruptive-upgrade.design.md) (item 3 of
task 3282). It follows
[`2026-10-13-daemon-restart-adoption.md`](2026-10-13-daemon-restart-adoption.md),
the in-process acceptance test with a stub agent, and
[`2026-10-14-run-hosts-daemon.md`](2026-10-14-run-hosts-daemon.md), the
real daemon binary with a stub model. This time the agent is the real
claude CLI talking to a real model. It does a real implement task
through its sandbox tools while the daemon is upgraded in place and,
separately, restarted with `systemctl restart`. The run finished in the
new build and opened its pull request.

## Environment

This is not the production host. It is a single machine set up the way
`scripts/setup.sh` sets one up, inside a grain task sandbox, which now
has a route to Anthropic's API through grain's model proxy. What
stands in for what:

| Asked for | What ran |
|-----------|----------|
| A real single-machine deployment | `grain daemon` as the systemd unit `grain-daemon.service` (systemd is PID 1 in the sandbox; Debian, docker 29.9.0), on SQLite in its own data directory, with `KillMode=process`, `-ui-trust-loopback` and `-max-workers 3`. The team's `maxWorkers` was raised to 2 and the sandbox memory overcommit to 3, so that one 8 GiB run fits a 7.8 GiB machine |
| `-run-host-image`, `-run-host-credential-dir` | Both set. The image is `debian:bookworm-slim` plus git, the tree's `grain` at `/usr/local/bin/grain`, the claude CLI 2.1.286 (the Dockerfile's pinned build, sha256-checked) at `/usr/local/bin/claude`, and a docker client (see below). The credential directory is `/home/debian/hr/cred` |
| claude on a subscription login | **Not a subscription login.** The real claude CLI 2.1.286 and the real model (`-claude-model claude-sonnet-5-5`), but authenticated through grain's own model proxy with this run's proxy token, not a `claude setup-token`. `-claude-path` is a wrapper in the credential directory that exports `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN`, unsets `CLAUDE_CODE_OAUTH_TOKEN`, and execs the CLI. The wrapper is needed because the daemon's environment does not reach a run host (finding 5 of the 10-14 walkthrough). `-claude-oauth-token-file` holds a placeholder |
| The sandbox backend | **Local container sandboxes** (`-container-sandboxes`, `ghcr.io/bwsalmon/grain/k8s-sandbox:latest`), which `daemon.CheckRunHostBackend` refuses with run hosts, because a run host has no docker socket. Granule sandboxes, the production pairing, need GCE. For this run only, two changes were made that are **not committed**: an env var (`GRAIN_HANDRUN_ALLOW_CONTAINER_RUNHOST=1`) that skips the refusal, and a `socat` bridge from `127.0.0.1:2375` to the docker socket, with `DOCKER_HOST=tcp://127.0.0.1:2375` in the run-host image. A run host is on the host network, so its `mcpserver` reaches the sandbox with `docker exec` through that bridge. The sandbox-tool path is therefore not the production one. Everything from the CLI back to the daemon is |
| GitHub | `githubsim` (REST) plus `git http-backend` over a bare `acme/widgets`, in a throwaway `main` package, as `cmd/grain`'s live tests pair them. One passing `build` check is seeded on the task's branch, as `seedPassingCheck` does. **The agent's pushes are real:** they go from the sandbox through grain's git proxy to that backend |
| The in-place upgrade | Source mode, as the UI's Upgrade button runs it: `POST /api/upgrade {"branch":"handrun/next"}`, with `-upgrade-src-dir` (a clone whose `origin` is a local bare repository), `-upgrade-install-path` (the unit's binary) and `-upgrade-restart-cmd sudo systemctl restart grain-daemon.service`. `handrun/base` is this branch's base `cd549ba5f` plus the refusal skip and the fix below. `handrun/next` is that plus one comment line, so the upgrade builds and installs a different binary (`8089360` → `1ff83ba`) |
| `read_grain_task_transcript` | `GET /api/tasks/{id}/attempts/{n}/transcript`, the endpoint that tool reads. The post-upgrade check run also called the tool itself on the run (see below) |

The task, as filed through `POST /api/tasks` and approved (`acme/widgets`,
implement):

1. Add `farewell.sh`, commit, push.
2. Wait for an operator: `date; until [ -e /tmp/handrun-continue-1 ]; do sleep 5; done; date`.
3. Let `greet.sh NAME` print `hello NAME`, test it, commit, push.
4. Wait again on `/tmp/handrun-continue-2`.
5. Add a `## Scripts` section to README.md, commit, push, finish.

I held the gates by creating those files in the sandbox container. This
puts each restart at a chosen point, and puts it inside a tool call that
is in flight, which is the case a long `wait_for_checks` creates.

## What happened to run 6-1

| Time (UTC) | Event |
|------------|-------|
| 00:30:14 | Task 6 dispatched. `StartedAt` 00:30:14.231759459. Sandbox `grain-sandbox-6-1` created |
| 00:30:15 | Run host `…-runhost-run-6-1-2579182008` created, container id `1ca56ea17451…`, `StartedAt` 00:30:15.784327561, deadline label `2026-10-15T03:45:15Z`, generation `…-8089360006ce`. `agent_started_at` 00:30:15.68814625 |
| 00:30:18–22 | The agent reads the checkout, commits and pushes `farewell.sh` (`27ac822`), and starts the gate-1 command (`date` prints 00:30:22) |
| 00:30:42 | First upgrade attempt. It **failed at the build**: npm in the builder container hit `SELF_SIGNED_CERT_IN_CHAIN` from this sandbox's package cache. This is environmental. No flush and no restart followed, and the run was untouched |
| 00:33:19 | **`sudo systemctl restart grain-daemon`**, to give the unit `NPM_CONFIG_REGISTRY`/`GOPROXY` pointing at the sandbox's plain-HTTP caches |
| 00:33:33 | Second upgrade to `handrun/next`, started through the API |
| 00:34:21 | Build, install, health check and schema done. **Flush**, then `RestartCmd` |
| 00:34:22 | The new build (`1ff83ba`) adopts run 6-1 and files check task 7 |
| 00:34:45 | Gate 1 released. The gate command returns (`date` prints 00:34:48). The agent commits and pushes `greet.sh` (`4d57020`) under the new build, then starts gate 2 |
| 00:35:08 | **A second `systemctl restart`**, under the new build, mid gate 2 |
| 00:35:17 | Gate 2 released. The agent pushes the README change (`4f2878b`), calls `wait_for_checks` (green), and finishes |
| 00:35:24 | Run 6-1 `succeeded` ("the run made 7 tool call(s) [run_command x6, wait_for_checks x1]"). Pull request **acme/widgets#9001** opened for `grain/3937212e/task-6`. Task 6 `awaiting_submit`. Run host and sandbox destroyed. `task_run_spool_cursor` row gone |

### The journal lines

The plain restart at 00:33:19:

```
runhost: daemon stopping; leaving run host 3937212e…-runhost-run-6-1-2579182008 (run 6-1) running at cursor 9074
orchestrator: run 6-1: left running in its run host for the next daemon to adopt
orchestrator: adopted run host 3937212e…-runhost-run-6-1-2579182008 for run 6-1 at cursor 9074 after a 0s gap
grain daemon: run-host adoption: 1 adopted, 0 lost, 0 unclaimed run host(s) freed
orchestrator: resuming adopted run 6-1 (task 6) from cursor 9074; 0s of restart credited
```

The upgrade at 00:34:21:

```
upgrade: handrun/next: step 5/6 schema: done in 10ms: unchanged: both builds are at schema 17
runhost: upgrade flushing; leaving run host 3937212e…-runhost-run-6-1-2579182008 (run 6-1) running at cursor 9726
grain daemon: upgrade: flushed the spool cursors of 1 run(s) in run hosts before the restart: [6-1]
upgrade: handrun/next: step 6/6 restart: started
upgrade: handrun/next: restart: running: sudo systemctl restart grain-daemon.service
grain daemon: serving the UI/API on http://127.0.0.1:8420 as debian
orchestrator: adopted run host 3937212e…-runhost-run-6-1-2579182008 for run 6-1 at cursor 9726 after a 0s gap
grain daemon: run-host adoption: 1 adopted, 0 lost, 0 unclaimed run host(s) freed
orchestrator: resuming adopted run 6-1 (task 6) from cursor 9726; 0s of restart credited
orchestrator: filed task 7 to check the upgrade to handrun/next (auto)
```

The second plain restart at 00:35:08 logged the same lines at cursor
13469, also with a 0s gap.

### The run host was never restarted

`docker inspect` of the run host before the upgrade, after the first
plain restart, after the upgrade and after the second plain restart
gave the same answer every time:

```
/3937212e…-runhost-run-6-1-2579182008 id=1ca56ea174511e4e9f7d844d6b639a0aaf9f957d5718556799b8028f66497214 started=2026-10-15T00:30:15.784327561Z restarts=0
```

`docker ps` showed it `Up` throughout, created at 00:30:15. The sandbox
container (`1576ef431fe6…`, started 00:30:14.374993266Z) was the same
throughout too. Only the daemon's pid changed.

### The transcript has no hole and no duplicate

The stored transcript of attempt 1 holds the run's seven tool calls
exactly once each, in order: `ls`/`cat`, the `farewell.sh` push, gate 1,
the `greet.sh` push, gate 2, the README push, and `wait_for_checks`. The
final message follows them. The gate-1 call and its output, `Thu Oct 15
00:30:22` then `Thu Oct 15 00:34:48`, span the first plain restart and
the upgrade. The output was written in the run host while the daemon
was down or detached, and it appears once, with its call. Gate 2 likewise
spans the second plain restart.

### StartedAt and the deadline did not move, and the gap was credited

- `StartedAt` read `2026-10-15T00:30:14.231759459Z` before the upgrade
  (API) and after the run finished (API and the `task_run` row).
- `agent_started_at`, which the adopter carries the deadline from, still
  reads `00:30:15.68814625` on the finished row.
- The deadline label `2026-10-15T03:45:15Z` is set at creation, and the
  container was never recreated.
- `task_run_wait_credit` for 6-1: `blocked_ms 284`, `credited_ms 284`.
  That is the three sub-second gaps. Each restart logged "0s gap"
  because the stop persisted the cursor at the stop, and the next process
  was serving within a second.
- An earlier adoption shows a gap that was not near zero being credited.
  Run 3-1, a casualty of the bug below, was adopted after a 13s gap
  while the daemon was swapped by hand. It logged `13s of restart
  credited`, and its row reads `blocked_ms 13151`, `credited_ms 13151`.

### The post-upgrade check task lists the run as adopted

Task 7's body (filed at 00:34:22) includes:

```
What happened to the agent runs in flight across the restart (the adoption pass at 2026-10-15T00:34:22Z): 1 adopted, 0 lost.

- adopted: run 6-1 of task 6 (its spool went 0s unread)

Check that each adopted run went on to finish -- its pull request opened, its transcript continuous across the restart -- rather than only that it was adopted.
```

and measured the restart's unreachability at 300ms for the API, the git
proxy and each model proxy. Task 7 itself ran as a real claude run in a
second run host. It called `read_grain_task_transcript` on task 6 and
found it continuous up to the in-flight gate. It reported, correctly,
that it could not yet confirm the run had finished, because run 6-1 was
still blocked at gate 1 when the check ran.

## Findings

1. **Fixed: under a run host, claude's `--mcp-config` named the daemon's
   own binary.**
   - `BuildClaudeFramework` used `os.Executable()` as the `grain` that
     each run's MCP server is started from. That file is read, and the
     server started, inside the run host, where grain is
     `/usr/local/bin/grain` (`runHostGrain`).
   - On a deployment whose daemon runs from the deployment image, the
     two paths agree by accident. Here the daemon ran from the unit's
     `-upgrade-install-path`, so they did not. Every run then started
     with no tools.
   - The first run (task 2) wrote its tool calls out as text and failed
     "the agent made no tool calls at all". The second (task 3) sat
     after `init` until it was stopped.
   - The config now names `cfg.RunHost.Grain`.
     `TestClaudeUnderARunHostStartsTheRunHostsOwnMCPServer`
     (`pkg/daemon`) fails without the change.
   - Task 4, with the fix, ran the task end to end in a run host with no
     restart: 7 tool calls, 3 pushes, pull request #9000.
2. **Environment, not grain: run_command returned after about 3s.**
   - The first gated run (task 5) found its `until` loop returning
     exit 0 after about 3s with only the first `date`. It stopped and
     asked, rather than take that as the operator's go-ahead.
   - The cause was the `socat` bridge. By default it closes a hijacked
     `docker exec` stream 0.5s after one direction's EOF. `socat -t
     86400` fixed it.
   - Task 4's `sleep 150`s were cut short the same way.
   - Nothing in grain's own path is implicated.
3. **Environment, not grain: the first upgrade failed to build.** `make
   container-build` runs npm in a builder container. This sandbox's
   package cache re-signs registry TLS, and the builder does not trust
   the cache's CA. Setting `NPM_CONFIG_REGISTRY` and `GOPROXY` on the unit
   to the cache's plain-HTTP endpoints (the Makefile passes both
   through) let the second attempt build. The failed attempt
   released its hold and touched no run, as designed.
4. **Noted: misleading log lines at a stop.**
   - At the flush, the *stopping* process logs `grain daemon: adopted run
     6-1 (task 6): orchestrator: adopted run 6-1: agent: the daemon
     stopped; the run continues in its run host for the next daemon to
     adopt`.
   - At a plain stop it logs `orchestrator: run 6-1 (task 6):
     orchestrator: run 6-1: agent: the daemon stopped; …`.
   - Both prefixes repeat themselves, and the first says "adopted" about
     a run that is being left, not adopted.
   - The adopting process's own lines are accurate.
   - Not fixed here.
5. **Noted: the check task cannot see an adopted run finish.** It is
   filed and dispatched the moment the new build is up, so a run
   adopted mid-task is still in flight when the check looks. Its body
   asks it to "check that each adopted run went on to finish". Task 7
   could only say the run was still running. That answer was honest, but
   it was not the one asked for. Not changed here: whether the check
   should wait for adopted runs is a design call.
6. **Not investigated: `read_grain_log` refused in the check run.** It
   said the serving process could not read the credentials it must
   redact ("no secrets dir"). This deployment's secrets live in the data
   directory's `secrets/`, the setup a source-mode install uses. Whether
   the log tool's secret lookup misses that layout, or only this ad hoc
   one, was not established.

## Not run

- **A subscription login.** The CLI authenticated through grain's
  model proxy. The setup-token path, and a token shared by run hosts of
  two generations at once, are what this stand-in does not exercise.
- **The production sandbox pairing.** Run hosts with granule sandboxes
  (`-granule-sandboxes`) need GCE. Here the sandbox tools went through a
  docker socket bridge that grain refuses to set up by itself.
- **The UI's Upgrade button itself.** The upgrade was started through its
  API, `POST /api/upgrade`, which is what the button calls.
- **A scheduled upgrade**, and **Kubernetes**.
- **The deployment image.** Its kubectl and codex downloads are
  unreachable from a task sandbox, as on 10-14. The run-host image was
  built for this run instead.
