# Run hosts under a real `grain daemon -run-host-image` (2026-10-14)

Task 3553: the walkthrough in §7 of
[`test-plans/2026-10-13.md`](../test-plans/2026-10-13.md), Stage 2 of
[`non-disruptive-upgrade.md`](../non-disruptive-upgrade.md). It follows
[`2026-10-12-run-hosts.md`](2026-10-12-run-hosts.md) (the package against
real docker) and
[`2026-10-13-daemon-restart-adoption.md`](2026-10-13-daemon-restart-adoption.md)
(the in-process acceptance test). This time the daemon is the real
binary, and one of the runs uses the real claude CLI.

## Environment

- The grain task sandbox: Debian, docker 29, Go 1.26.6. Branch
  `grain/eu/task-3553`, cut from `candidate` at `e1bbddcc7`.
  `git branch -r --contains` puts all five commits under test
  (`3f4fd4308`, `d7f97980a`, `4d0db5235`, `bb68cba96`, `88802f12e`) on
  `origin/candidate`.
- **The daemon:** `/usr/local/bin/grain daemon`, built from the tree with
  `CGO_ENABLED=0`, on SQLite in a scratch data directory. Flags:
  `-container-sandboxes -container-sandbox-network bridge
  -container-git-proxy-host 10.240.0.1 -max-workers 1 -poll-interval 1s
  -agent-framework claude -claude-path … -claude-oauth-token-file …
  -github-host 127.0.0.1:9911 -github-insecure-http -ui-addr
  127.0.0.1:8420 -ui-trust-loopback -run-host-image <img>
  -run-host-credential-dir /tmp/walk/cred`. The binary refuses to start
  with no sandbox backend, so local container sandboxes
  (`ghcr.io/bwsalmon/grain/k8s-sandbox:latest`) are what it ran with.
- **The run-host image.** The tree's own `Dockerfile` was built with
  `.grain-ca.crt` staged in the repository root, which was removed
  afterwards. That build could not finish here: `dl.k8s.io` (kubectl) and
  `github.com` (codex) time out from a task sandbox, and the kubectl step
  failed after 180s. Two images stood in for it:
  - `grain-runhost:quick`: `debian:bookworm-slim` with this tree's
    `grain` at `/usr/local/bin/grain`. This is what the CI test builds.
  - `grain-runhost:claude`: the same, plus the Dockerfile's own pinned
    claude install block copied verbatim (`CLAUDE_CLI_VERSION=2.1.286`,
    sha256-checked, from `downloads.claude.ai`, which is reachable).
- **The agent:**
  - Steps 2–5 used a shell stub claude CLI in the credential directory.
    The repository has no fake agent binary. Its tests use shell stubs
    like this one. The stub writes stream-json lines two seconds apart,
    then holds until a `release` file appears.
  - The **real claude CLI 2.1.286** ran one task end to end in a run host
    (step 2b). A wrapper in the credential directory exports
    `ANTHROPIC_BASE_URL`, pointing at a stub Messages endpoint, and then
    execs `/usr/local/bin/claude` (finding 5 explains why a wrapper was
    needed).
- **The transcript** was read through the daemon's API
  (`GET /api/tasks/{id}/attempts/{n}/transcript`), not the UI.

## Stubs

| Boundary | Stub |
|----------|------|
| GitHub | `githubsim` (REST) plus `git http-backend` over a bare repository, in a throwaway `main` package outside the commit, the same pairing `cmd/grain`'s live tests use |
| Anthropic | a 40-line Python Messages API: SSE text deltas, `count_tokens`, `HEAD /api/hello` |
| The agent's push | the test pushed the task branch to the bare repository by hand, as the acceptance test does |
| claude login | a fake `sk-ant-oat01-…` token in `-claude-oauth-token-file` |
| codex login | a fake `auth.json` (step 6b never got as far as using it) |

## Steps and outcomes

1. **Image and daemon.** Both are described above. The tree's full image
   could not be built here, which is an environment limit and not a
   finding.
2. **A run in a run host.**
   - `docker ps` showed the sibling
     `<deployment>-runhost-run-2-1-<n>` (image `grain-runhost:quick`,
     user `1000:1000`, network `host`). It carried all seven labels:
     `dev.grain.runhost.{deployment,task,run,generation,protocol,deadline,spool}`.
     The deadline label was the run's budget plus 30 minutes. The
     container's mounts were the spool root, the staging root and the
     credential directory, and never the data directory.
   - `task_run_spool_cursor` held `2-1|0` at creation and `2-1|765` once
     the stub's six lines were read. The 765 is the size of the spool's
     `stdout` exactly. At finish the row was gone (run 3-3: `0` at
     creation, no row nine seconds later).
   - The transcript streamed through the API while the run was still
     live (`walk line 1 before … walk line 6 before`).
   - **2b, the real CLI.** On `candidate` as it stands, the real claude
     CLI in a run host exits at once with `Error: Invalid MCP
     configuration: MCP config file not found:
     /tmp/grain-claude-mcp-config-1506892822.json`, and its `HOME` is
     `/` (findings 1 and 2). It is retried with backoff and fails the
     same way every time. With the fix, run 3-3 made one
     `POST /v1/messages?beta=true` (bearer auth) to the stub and
     finished. Its transcript is the stub model's answer, and the task
     went to `awaiting_ack`.
3. **No credential in `docker inspect`.** `Config.Env` was
   `["PATH=…"]`, and after the fix also `HOME=/tmp/walk/cred`. A grep of
   the whole `docker inspect` document for the token found 0 matches. The
   spool held `command.json stderr stdin stdout`, with no `env` file:
   `grain runhost exec` had removed it once read. A recursive grep of the
   run-host directories for the token found nothing either. The daemon's
   own log shows the variable as `CLAUDE_CODE_OAUTH_TOKEN=[redacted]`.
4. **A daemon stopped mid-run.**
   - SIGTERM. The daemon logged `runhost: daemon stopping; leaving run
     host … (run 2-1) running at cursor 765` and `left running in its run
     host for the next daemon to adopt`, then exited in 2s. While it was
     down, the run host and the sandbox container were both still
     running, and the row was still live.
   - On restart: `adopted run host … at cursor 765 after a 1m2s gap`,
     `run-host adoption: 1 adopted, 0 lost, 0 unclaimed run host(s)
     freed`, `resuming adopted run 2-1 … 1m2s of restart credited`.
     After the release, the run succeeded, the second daemon opened the
     pull request (`awaiting_submit`), and no run host was left.
   - **The plan's expected outcome is out of date.** The plan says the
     matched run is "finished with a reason saying that post-processing
     resume is not built yet". `candidate` now carries tasks 3281 and
     3282, so the run is resumed, not finished, and its pull request is
     opened.
   - **The gap was wrong.** The daemon was down for about 7s, and 1m2s
     was credited (finding 3). After the fix, a run quiet for 30s before
     the stop had its cursor row's `updated_at` moved to the stop
     (00:47:03). The restart at 00:47:08 credited `6s`.
5. **Crafted adoption outcomes.** Each was set up while the daemon was
   down, by stopping it mid-run and then replacing or removing the
   container. The reasons below are `Decide`'s, word for word:
   - *A row whose container is gone* (`docker rm -f`): run 5-1 finished
     `interrupted`, "no run host is left for this run: it was never
     started, or it was removed while no daemon was running; finished as
     interrupted and re-queued without a strike". Task 5 was dispatched
     again as 5-2.
   - *A container with no row* (labels for run `999-1`): "freeing run
     host walk-unclaimed (task 999, run 999-1, generation v-old): no live
     run claims it". The container was removed.
   - *A protocol label one ahead* (`protocol=2`): run 6-1 finished
     `interrupted`, "its run host … speaks run-host protocol 2, which
     this build (protocol 1) cannot adopt; the conversation stays in the
     session store for the next dispatch". The container was freed as
     "unsupported run-host protocol 2". The startup line for this pass
     read `0 adopted, 1 lost, 2 unclaimed run host(s) freed`, but only
     one of the two was unclaimed (finding 4).
   - *A deadline in the past* (`deadline=2026-10-14T00:00:00Z`): run 7-1
     finished `interrupted`, "its run host … outlived its own deadline
     (2026-10-14T00:00:00Z) while no daemon was watching it". The
     container was freed as "past its deadline label".
6. **The flag unset, and codex.**
   - *Unset:* no `run-host adoption` line at startup and no run-host
     container. The stub claude ran as a direct child of the daemon
     (`ppid` = the daemon's pid), with `--mcp-config` in `/tmp` as
     before.
   - *codex with the flag set:* **not shown end to end.**
     `-agent-framework codex` is ignored once the store has a framework
     ("stored config already has claude"). `grain settings
     -agent-framework codex` and a task filed with `agentFramework:
     codex` both took. The task was then held at admission with `team
     "default" has no enabled connection`: this deployment has no codex
     model connection, and setting one up was out of scope. By the code,
     `pkg/daemon/agents.go` gives `cfg.RunHost` to claude only
     (`claude.WithHost`). codex's builder only ever takes the remote
     host.
7. **Where the design doc disagrees with the run:**
   - The Kubernetes note's "Still missing" item 1 says that "the stopping
     daemon still destroys its run hosts (`Host.Detach` is unset), and
     `RecoverRunHosts` still finishes a matched run as interrupted". On a
     single machine both are untrue now: the daemon detached and the
     next one adopted (step 4). The Stage 2 note at the top of the
     section is the current one. Whether the Kubernetes backends' share
     is built is not something this run tested.
   - The `Host.Mounts` doc and the Stage 2 note say a run host is given
     "what mcpserver needs to reach its sandbox". That holds for granule
     sandboxes, which get the exec key. It does not hold for local
     container sandboxes (finding 6).
   - Before the fix, "`-run-host-image` … drives **claude** runs in run
     hosts" was true only of a stub CLI. A real claude could not start
     (findings 1 and 2). The acceptance test's stub ignored its argv,
     which is why CI passed.

## Findings

1. **Fixed: the real claude CLI could not start in a run host.** The
   claude framework writes `--mcp-config` with `host.CreateTemp(ctx, "",
   …)`. `runhost.Host` inherited `LocalHost.CreateTemp`, which uses the
   daemon's `os.TempDir`, and no run host mounts that directory. So every
   claude run under `-run-host-image` failed before its first model call.
   `runhost.Host.CreateTemp` now puts the file under `StagingRoot`, as
   `MkdirTemp` already did for per-run homes.
2. **Fixed: a docker run host's CLI had `HOME=/`.** The container runs as
   the daemon's uid, which has no passwd entry in the image. `/` is not
   writable, so `~/.claude` (config, session store, `--resume`) could not
   be written. `runhost.Docker` gained `Env`, set with `--env` and never
   a secret. `NewRunHost` sets `HOME` to `-run-host-credential-dir`, the
   directory the design puts `~/.claude` in, as `newKubeRunHostDriver`
   already did for run pods.
3. **Fixed: the restart gap credited the run's quiet time before the
   stop.**
   - `Attach`'s `persist(true)` returned early whenever the cursor had
     not moved. So at detach, `task_run_spool_cursor.updated_at` stayed
     at the last output, and `Decide`'s `Gap = now - LastSeen` included
     the whole silence before the stop. The result was 1m2s credited
     for a ~7s stop.
   - A run in a long tool call (a 45-minute `wait_for_checks`) would
     have had up to that much extra budget credited
     (`newAdoptedRunClock`).
   - A forced persist (detach, flush or finish) now always reports.
4. **Fixed: the adoption report overcounted freed run hosts.**
   `NewAdoptionReport` set `Freed` to `len(plan.Release)`, which counts
   the run hosts of lost runs too: refused protocol, past deadline,
   killed without an exit record, and duplicates. The startup line and
   the post-upgrade check both say "no live run claimed". `Freed` now
   counts only releases whose run no row claims.
5. **Not fixed, a judgement call: the daemon's environment does not reach
   a run host's CLI.**
   - `LocalHost` runs the CLI with `os.Environ()` plus the run's env, and
     `claude.go` and `retry.go` promise operators that a variable set on
     the controller (`CLAUDE_CODE_MAX_RETRIES`, `ANTHROPIC_BASE_URL`,
     proxy settings) "reach[es] claude untouched".
   - A run host gets only the run's own env, through the spool, on top
     of the image's.
   - Keeping the daemon's secrets out of the run host is arguably the
     point. But the promise in those comments is broken under
     `-run-host-image`, and nothing says so.
   - Filed as a follow-up. **Since decided (task 3731):** run hosts do
     not inherit the daemon's environment, and the design doc's "Run
     hosts" section now says so. The four variables the claude package
     promises operators (`operatorVars`: `MCP_TOOL_TIMEOUT`,
     `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`,
     `CLAUDE_CODE_DISABLE_AUTO_MEMORY`, `CLAUDE_CODE_MAX_RETRIES`) are
     forwarded into the run's env by name. Without that, an operator's
     `MCP_TOOL_TIMEOUT` switched grain's default off and put nothing in
     its place. `ANTHROPIC_BASE_URL` and the proxy variables are not
     forwarded; a deployment sets them in the run-host image.
     `TestRunForwardsAnOperatorsOwnSettings` (`pkg/agent/claude`).
6. **Not fixed, needs a design decision: sandbox tools fail from a run
   host with local container sandboxes.** The run host's mcpserver is
   given `-container grain-sandbox-…` and reaches its sandbox with
   `docker exec`. A run host has neither the docker CLI (in the CI image)
   nor the socket (in any image). By hand, `run_command` through
   `grain mcpserver` inside the run-host image returned `exec: "docker":
   executable file not found in $PATH`. The same call from the host
   reached the container. Under `-container-sandboxes -run-host-image`,
   an agent therefore has no working sandbox tool. Granule sandboxes
   (this deployment) get the exec key mounted and are not affected.
   Filed as a follow-up.
7. **Not fixed: a spool is left behind when its run host is gone.** In
   case 5a the row was finished, but `runhosts/spool/run-5-1-…` stayed,
   because the spool path is only on the container's label. A run host
   that was created but never started would leave the `env` file, which
   holds the run's `CLAUDE_CODE_OAUTH_TOKEN`, in that directory. The
   data directory is private, so this is low severity, but nothing ever
   removes it. Filed as a follow-up.
8. **Noted:** the mcpserver's `-data-dir` (the daemon's data directory)
   is passed to a run host that does not mount it. With granule
   sandboxes, which tools need it was not established here.

## Fixes and tests

| Change | Test (fails before, passes after) |
|--------|-----------------------------------|
| `runhost.Host.CreateTemp` under `StagingRoot` | `TestHostCreateTempIsUnderStaging` |
| `runhost.Docker.Env`; `NewRunHost` sets `HOME` to the credential dir | `TestDockerCreateArgsSetsHome`, `TestNewRunHostGivesDockerRunHostsTheCredentialDirectoryAsHome` (`pkg/daemon`) |
| A forced cursor persist always reports | `TestDetachPersistsTheCursorAtTheStopWhenItHasNotMoved` |
| `AdoptionReport.Freed` counts only unclaimed run hosts | `TestAdoptionReportFreedCountsOnlyUnclaimedRunHosts` (`pkg/orchestrator`) |
| The acceptance test's stub claude exits, as the real one does, on an unreadable `--mcp-config` or an unwritable `HOME` | `TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest`, docker-gated by `GRAIN_RUNHOST_DOCKER` and run by the "run host" job. Against the pre-fix tree it fails with `MCP config file not found: /tmp/grain-claude-mcp-config-…json` |

Run here: `go test -race -count=2 ./pkg/runhost/`; `go test -run
'TestAdoptionReport|UpgradeCheck|RecoverRunHosts|Adopt' ./pkg/orchestrator/`;
`go test -run TestNewRunHost ./pkg/daemon/`; and `GRAIN_RUNHOST_DOCKER=1
go test -run '^TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest$'
./cmd/grain/`. All pass.

## Not run

- The tree's full deployment image. Its kubectl and codex downloads are
  unreachable from a task sandbox.
- A codex run with the flag set, end to end. The deployment has no codex
  connection.
- The UI's task pane. The transcript was read through the API instead.
- A real subscription login, a granule sandbox, and the hand run on a
  real deployment.

## CI

grain CI's full suite on the branch's final commit: see the pull request.
The verdict is recorded in the task's closing note.
