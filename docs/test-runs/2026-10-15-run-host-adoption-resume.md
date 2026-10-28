# Run-host adoption, resume, flush and sweep on one machine (2026-10-15)

Task 3951 (filed by the nightly test-plan task 3749). This is the walkthrough in §8 of
[`test-plans/2026-10-15.md`](../test-plans/2026-10-15.md). It follows
[`2026-10-13-daemon-restart-adoption.md`](2026-10-13-daemon-restart-adoption.md)
and [`2026-10-14-run-hosts-daemon.md`](2026-10-14-run-hosts-daemon.md), and
covers what those two reports list as not run: the UI's task pane across a
restart, a real upgrade, the deadline, the sweep, the forwarded variables,
release triage, and the hidden tab.

## Environment

- The grain task sandbox: Debian, 4 vCPU, 7.8 GiB, **docker 29.9.0**, Go
  1.26. Branch `grain/eu/task-3951`, cut from `candidate` at `591f0f35f`.
  `git branch -r --contains` puts all sixteen commits under test on
  `origin/candidate`. `b42fdd989` and `db192d4c4` are on `candidate` only;
  the other fourteen are also on `main`.
- **The run-host image.** The tree's `Dockerfile` was built with
  `.grain-ca.crt` staged in the repository root, and the file was removed
  afterwards. The build failed at the kubectl step (`curl` exit 28 against
  `dl.k8s.io`), which is the same unreachable download the 10-14 report
  hit. So the run hosts used `grain-runhost:quick`:
  `debian:bookworm-slim` with this checkout's `grain`
  (`CGO_ENABLED=0`) at `/usr/local/bin/grain`. That is the image the CI
  acceptance test builds.
- **The daemon:** the real `grain daemon` binary built from the tree, on
  SQLite in a scratch data directory. It was built with `-tags grain_e2e`,
  the tag the browser e2e suite uses, so that `-sandbox-dir` gives
  host-directory sandboxes:
  - `-run-host-image` cannot be combined with `-container-sandboxes`
    (step 8), and this sandbox has no granule or Kubernetes.
  - `-disable-native-sandboxes` starts, but it can dispatch nothing.

  Flags: `-data-dir … -sandbox-dir … -max-workers 1 -poll-interval 1s
  -agent-framework claude -claude-path <cred>/claude
  -claude-oauth-token-file <cred>/token -github-host <sim>
  -github-insecure-http -ui-addr 127.0.0.1:8420 -ui-trust-loopback
  -run-host-image grain-runhost:quick -run-host-credential-dir <cred>`.
  Step 7 added `-upgrade-src-dir`, `-upgrade-install-path`,
  `-upgrade-restart-cmd` and `-upgrade-allowed-branches '**'`.
  - The daemon's own environment carried `MCP_TOOL_TIMEOUT=123456`,
    `CLAUDE_CODE_MAX_RETRIES=2`, and two dummies:
    `GITHUB_TOKEN=ghp_dummywalkdaemontoken` and
    `ANTHROPIC_API_KEY=sk-ant-api03-dummywalkdaemonkey`.
- **The browser:** Playwright with Chromium 151 (headless shell), against
  the UI built from the tree (`cd ui && npm ci && npm run build`), which is
  embedded in the daemon binary. Installing the browser needed
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`;
  without it the download fails with `SELF_SIGNED_CERT_IN_CHAIN`. The
  `sqlite3` CLI (apt) was used to read the store.

## Stubs

| Boundary | Stub |
|----------|------|
| GitHub | `githubsim` (REST) plus `git http-backend` over a bare repository, served by a throwaway test in `cmd/grain` (not committed). It reuses the acceptance test's own helpers (`githubHostServer`, `syncedSim`, `seedPassingCheck`) |
| Anthropic / the claude CLI | a shell stub in the credential directory, extended from the acceptance test's `restartStubClaude`. It refuses a bad `--mcp-config` or an unwritable `HOME`, as before. It records its argv, stdin and environment per invocation. Its **first** invocation writes one line, then a terminal `API Error: 529 … overloaded_error` result, and exits 1. Every later one writes five lines a second apart, holds until `release-<n>` exists, writes five more lines, makes one `run_command` tool call, and succeeds |
| claude login | a fake `sk-ant-oat01-walkstub` token file |
| The agent's push | the harness pushed each task's branch to the bare repository itself, as the acceptance test does |
| Sandboxes | host directories (`-tags grain_e2e`, `-sandbox-dir`), as the browser e2e suite uses |

Nothing of grain's own was stubbed: the daemon, the run hosts, the
`grain runhost exec` inside them, the UI, the store, the CI service and the
upgrader are the real ones.

## Steps and outcomes

1. **Image and daemon.** Both are described above. The full deployment
   image could not be built here, which is an environment limit.

2. **The task pane across a SIGTERM restart (walk-1).**
   - Attempt 1 was refused with a 529. The claude retry loop waited 32s
     and started attempt 2 in a new run host
     (`…-runhost-run-walk-1-1-<n>`, a second container), which held.
   - Playwright opened `/tasks/walk-1`. The pane showed "attempt 1 line 1,
     about to be refused" and attempt 2's five lines before the hold.
   - SIGTERM at 11:35:43. The daemon logged `leaving run host … (run
     walk-1-1) running at cursor 730` and `left running in its run host
     for the next daemon to adopt`. Its in-flight `GET /api/restart-watch`
     returned 0.3s after the signal, so the shell heard of the restart
     before the process was gone (`bf9107fd9`).
   - **The banner said "grain is restarting"** 2.9s after the signal and
     stayed up through the gap. The two watch requests made while the
     daemon was down failed at once, and the shell retried every 3s.
   - The daemon was started again at 11:35:51. It logged `adopted run host
     … at cursor 730 after a 8s gap`, `run-host adoption: 1 adopted, 0
     lost, 0 unclaimed run host(s) freed`, and `resuming adopted run
     walk-1-1 … 8s of restart credited`. The banner cleared 4s after the
     start. Watch requests then went back to 25s long polls.
   - After the hold was released, the second daemon opened the pull
     request: `acme/widgets#9000` for `grain/task-walk-1`, with the task
     at `awaiting_submit`.
   - **The stored transcript** (`GET /api/tasks/walk-1/attempts/1/
     transcript`) held each of the eleven stub lines exactly once, in
     order, including both attempts' lines. **It was missing the retry
     loop's note**, which is finding 1.
   - That run's outcome read "Failed: the agent made no tool calls at
     all", because this first version of the stub made none. That is the
     stub's doing: the later runs, whose stub makes one tool call, ended
     `succeeded`.
   - **Control (walk-2):** the same stub, with no restart. The transcript
     ended with `* the provider refused this run mid-flight; attempt 2
     resumed the same conversation in the same sandbox`.

3. **The forwarded variables (walk-1, attempt 2, live).**
   - `docker inspect`'s `Config.Env` was exactly
     `["HOME=/tmp/walk/cred","PATH=…"]`.
   - A grep of the whole `docker inspect` document found 0 matches for
     the dummy `GITHUB_TOKEN`, the dummy `ANTHROPIC_API_KEY`, the OAuth
     token, and the names `GITHUB_TOKEN`, `ANTHROPIC_API_KEY` and
     `MCP_TOOL_TIMEOUT`.
   - The mounts were the credential directory and the spool and staging
     roots, never the data directory itself.
   - **The CLI's own environment**, which the stub dumped, had
     `MCP_TOOL_TIMEOUT=123456` and `CLAUDE_CODE_MAX_RETRIES=2` (the
     daemon's values), `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=3720000`,
     `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`, `CLAUDE_CODE_OAUTH_TOKEN` and
     `HOME`. It had no `GITHUB_TOKEN` and no `ANTHROPIC_API_KEY`.
   - **The plan's expected outcome is slightly off.** The operator
     variables do not show in `docker inspect`: they travel in the spool's
     `env` file, which `grain runhost exec` removes once it has read it.
     That is the design (`db192d4c4`: the run's env, never `--env`). So
     the place to read them is the CLI's environment, and they were there.

4. **The deadline of an adopted run (walk-3).**
   - The daemon has no flag or setting for the ordinary run cap
     (`orchestrator.DefaultMaxRunRuntime`, 120m). So a walkthrough-only
     binary was built with that constant set to 3m. It was not committed.
   - The run's `agent_started_at` was 11:44:51.098. Attempt 1 was
     refused, and attempt 2 held from 11:45:22.
   - The run host's `deadline` label was 13:02:51, that is start + 3m +
     45m wait-credit cap + 30m. That is consistent with the label's
     definition.
   - SIGTERM at 11:46:51.7, two minutes after the agent's start, and a
     restart at 11:47:01. The new daemon logged `after a 10s gap … 10s of
     restart credited`.
   - **The adopter ended the run at 11:48:01.68**, that is
     `agent_started_at` + 3m + 10.4s credited. Its outcome was
     `cancelled`, "the run exceeded its 3m10.435737843s wall-clock limit",
     and `task_run_wait_credit` held 10435/10435 ms. So the deadline is
     carried from the agent's start, not reset by the adoption, and not
     measured from the row's start.
   - With finding 1's fix in that binary, the stored transcript ended with
     the retry note.
   - **Leases: not shown live.** Every lease-minting provider
     (`gcp-key`, `gemini-key`, `github-sandbox`, `modelproxy`) needs real
     cloud credentials that a task sandbox does not have. The `lease`
     table stayed empty. Recording and revocation are covered by
     `ba2e09934`'s tests in `pkg/orchestrator/adopt_test.go` and
     `pkg/model/adopted_run_test.go` only.

5. **The sweep after a crash (walk-3-2, walk-4).**
   - Two runs were held live in run hosts, and the daemon was killed with
     **SIGKILL**, so there was no detach.
   - While the daemon was down, a never-started run host's spool was
     crafted: `spool/run-ghost-1-1-123456/` holding `env`
     (`CLAUDE_CODE_OAUTH_TOKEN=…`) and `command.json`, with mtime 11:00. A
     staging file of the same age was added beside it.
   - On restart, both runs were adopted (`after a 30s gap`, `after a 22s
     gap`), and the daemon logged `removing spool …/run-ghost-1-1-123456:
     no run host is left for it` and `removing staging entry
     …/grain-claude-mcp-config-111.json`.
   - **The ghost spool and its `env` file were gone. Both live spools and
     both live runs' staging files stayed.**
   - After the release, both runs ended `succeeded`, each transcript with
     12 lines and no duplicates, and no run host was left.
   - After a SIGKILL, the gap credited runs from the cursor's last write
     (the last output), not from the crash: 22s here for a 5s outage. A
     crash cannot persist, so this is the expected upper bound, not a
     bug.

6. **A release interrupted by a restart.**
   - `acme/widgets` was set to grain CI (`PUT /api/repos/acme/widgets/
     settings`, `ciMode: grain`, `watchDefaultBranch: true`). Its `main`
     was given a native `release` workflow whose job runs `sleep 150`.
   - The first two attempts at a workflow were invalid: a `runs-on:
     grain` line, and then `ubuntu-latest`, which needs a sandbox shape a
     host-directory sandbox cannot give. They failed for real and filed
     "Fix base branch release" triage tasks (2 and 4), which is correct.
     They were closed before the test.
   - Release run `4186edb2` started at 11:53:25 on `c9c0f93`, with the
     failure streak at 2. The daemon got SIGTERM at 11:53:56 and was
     started again at 11:53:59.
   - The new daemon logged: `the release of acme/widgets main at
     c9c0f93e20ee (run 4186edb2aa4aa4ab) never concluded: the run was
     interrupted by the daemon that was running it stopping; forgetting it
     so the next cycle releases the branch again, and counting it as no
     failure`. It started release `6a3fae4d` at the same commit at
     11:54:00.
   - **No triage task was filed.** The task table had no new "Fix base
     branch release" row. The rerun finished `success` at 11:56:30, and
     the streak went to 0.

7. **The upgrade path (source upgrader, walk-5).**
   - `-upgrade-src-dir` was a clone of a local bare repository. That
     repository's branch `walkup` was this tree plus finding 1's fix plus
     one walkthrough-only change: its Makefile's `container-build` target
     ran `go build -tags grain_e2e -o bin/grain ./cmd/grain`, in place of
     the containerised build, so that the new build could use the same
     sandboxes.
   - `-upgrade-restart-cmd` was a script that SIGTERMs the daemon and
     starts the installed binary.
   - With walk-5 held in a run host, `POST /api/upgrade {"branch":
     "walkup"}` went through checkout, build (10s), install, health check
     and schema, which was unchanged at 17. Then:
     - `runhost: upgrade flushing; leaving run host … (run walk-5-1)
       running at cursor 730`
     - `upgrade: flushed the spool cursors of 1 run(s) in run hosts before
       the restart: [walk-5-1]`
     - and then the restart.
   - The new build (`8996980d1`) logged `adopted run host … for run
     walk-5-1 at cursor 730 after a 0s gap`, `1 adopted, 0 lost, 0
     unclaimed run host(s) freed`, and `an upgrade held the queue over
     this restart -- the pipeline is picking work up again`. It then filed
     task 5, "Check the upgrade to walkup".
   - **The check's body reports adoption correctly:** "1 adopted, 0 lost.
     adopted: run walk-5-1 of task walk-5 (its spool went 0s unread)",
     beside 400ms unreachability per listener.
   - After the release, walk-5-1 ended `succeeded`, with 12 transcript
     lines and no duplicates.
   - The check task's own setup logged `grain source checkout
     unavailable: exit=128`, because the scratch git proxy has no
     `bwsalmon/grain`. That is expected in this setup.

8. **The refused flag pair.** `grain daemon -container-sandboxes …
   -run-host-image grain-runhost:quick …` exited 2 before it touched the
   data directory, with: `-run-host-image cannot be used with
   -container-sandboxes: a run host's mcpserver reaches a container
   sandbox with `docker exec`, and a run host has no docker socket, so
   every sandbox tool would fail -- drop -run-host-image … or use
   -granule-sandboxes or -k8s-sandboxes`.

9. **The hidden tab.**
   - At the end of step 2, Playwright overrode `document.visibilityState`
     to `hidden` and dispatched `visibilitychange`.
   - The long poll already in flight finished on its own 13s later, at
     its 25s server timeout. **No `restart-watch` request was opened in
     the 70s the tab stayed hidden**, and none was open at the end.
   - Making the tab visible again opened one at once.

## Findings

1. **Fixed: an adopted run that had retried lost its retry note.**
   - claude's `Run` appends "* the provider refused this run mid-flight;
     attempt N …" to the transcript for each retry. `ParseAttempts`, which
     the adopting daemon reads the run back through, passed no notes to
     `endRun`. So the stored transcript of every adopted run that had
     retried restarted half way through with nothing saying why.
   - Reproduction: the stub above (a 529 on attempt 1, then a hold),
     SIGTERM during attempt 2, restart, release. The walk-1 transcript has
     no note; the walk-2 control without a restart has it.
     Deterministically, at unit level: feed `ParseAttempts` the same
     attempt streams `Run` is given and compare the two transcripts.
   - The fix: `ParseAttempts` rebuilds the notes from the earlier
     attempts' streams (`earlierRetryNotes`). There is one note per
     attempt, split at claude's `system`/`init` event, and it says
     "resumed" when an attempt so far named a session, as `Run` does.
   - Verified live in step 4 and step 7's builds.
2. **Not fixed, low severity: an adopted run's `--mcp-config` file stays
   in the staging root until the next adoption pass.**
   - The claude framework removes the file in a deferred call. That call
     is skipped on detach, because the CLI is still using the file, and
     the adopter never learns the path.
   - Reproduction: after step 7, `walk-5-1`'s
     `staging/grain-claude-mcp-config-3035914782.json` was still there
     once the run had finished. A non-adopted run's file (run 5-1) was
     removed as its run ended.
   - The file holds the mcpserver argv: sandbox root, PR repo and branch,
     credential *name*, GitHub host, run deadline. It holds no secret, and
     the next restart's sweep (`b42fdd989`) removes it. So it is clutter
     rather than a leak.
   - Removing it at the end of the run host's own life would be wrong,
     because claude's attempts share one config file. The adopter would
     have to read the path out of the spool's `command.json` before
     `Attach` removes the spool. Proposed as a follow-up rather than
     fixed here.
3. **Noted: the plan's step 3 expected the operator variables in
   `docker inspect`.** They are passed through the spool's `env` file by
   design, and the CLI's environment had them (step 3).
4. **Noted: the ordinary run cap is not configurable.**
   `DefaultMaxRunRuntime` has no flag or setting, so the plan's "give a
   task a 3-minute timeout" needs a rebuilt binary, as in step 4.

## Fixes and tests

| Change | Test (fails before, passes after) |
|--------|-----------------------------------|
| `claude.Framework.ParseAttempts` writes the retry notes `Run` writes (`earlierRetryNotes`) | `TestParseAttemptsKeepsTheRetryNotesRunWrites` (`pkg/agent/claude`): three scripted runs (one retry with resume, two retries, a retry with no session yet). For each, the transcript `ParseAttempts` gives from the attempt streams must equal the one `Run` gives. It also checks that a run with no earlier attempt gets no note |

Run here: `go test ./pkg/agent/claude/` (passes), and the walkthrough's
steps 4, 5 and 7 against binaries carrying the fix.

## Not run

- **The tree's full deployment image.** `dl.k8s.io` (kubectl) times out
  from a task sandbox.
- **Lease revocation, live.** It needs a lease-minting capability with
  real cloud credentials (step 4).
- **A second browser pass that watched the adopted run's post-restart
  lines arrive live in the pane, and opened the finished attempt from the
  timeline** (`.timeline-attempt-open`). The previous attempt at this task
  was cut off by a provider usage limit while that pass was running, and
  its result was lost with the sandbox. Step 2 shows the pre-restart
  lines live, the banner and the PR. The post-restart lines were checked
  in the stored transcript through the API, not in the pane.
- **The image upgrader** (`-upgrade-image`). It needs a registry and
  published tags. The Kubernetes path, codex, granule sandboxes, and a
  real subscription login were not run either.

## CI

grain CI's full suite on the branch's final commit is recorded in the
task's closing note.
