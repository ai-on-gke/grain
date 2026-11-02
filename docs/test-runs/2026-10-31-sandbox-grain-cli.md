# The sandbox `grain` CLI everywhere: placement, `grain-sandbox`, and `grain log`/`grain metrics` filters

- **Plan:** `docs/test-plans/2026-10-30.md` §5 (PR #196: `0130d6f5`, `f7dae2c4`, `e11997b7`, `7ee70a27`)
- **Date:** 2026-10-31, 22:15–23:00 UTC
- **Task:** `grain/049f1e09/task-2014` on `ai-on-gke/grain`, base `main` at `70036e3f` (origin: [Task #1899](/tasks/1899)), `main` at `01fc11c` merged in before the final push
- **Environment:**
  - Debian 12 sandbox (`granule`), 7 vCPU / 15 GiB, Go `go1.26.10`, Docker, Python 3.11
  - `go build -o /home/debian/bin/grain ./cmd/grain` and `go build -o /home/debian/bin/grain-sandbox ./cmd/grain-sandbox` from this branch, so `orchestrator.SandboxGrainBinaryBeside` finds `grain-sandbox` beside the daemon. Both were rebuilt after each fix.
  - A **stale** `grain` built from `0130d6f5^` (`go build ./cmd/grain-sandbox` there: the host-script-only CLI, which answers `grain ls` with `"ls" is not a command this sandbox's grain has`).
  - Three sandbox images, each built on `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` (which carries no `grain` at all):
    - `grain-sandbox-stale:test`: the stale binary at `/usr/local/bin/grain`.
    - `grain-sandbox-current:test`: this branch's `grain-sandbox` at `/usr/local/bin/grain`, so the bytes are identical to the daemon's.
    - `grain-sandbox-robin:test`: the stale image, plus `/home/debian/.grain/bin` as a root-owned `0555` directory (a read-only home for the placement).
  - The only external boundary stubbed is GitHub: a walkthrough-only Go program (not committed) serving `githubsim` REST and `git http-backend` for a bare `acme/widgets` on `127.0.0.1:4250`. Its workflow has one job, `test` (shape 1 vCPU / 1 GiB), whose step echoes `WALKTHROUGH-CI-MARKER line one` and `second line`.
  - The daemon is real, on the **container** sandbox backend: one Docker container per run, the same `PlaceFile`/`RunInSandbox` path a pod or VM takes. The granule backend could not be used: this sandbox has no `granule` host binary, only the guest agent. Later runs used the same flags under `sudo systemd-run --unit grain-daemon`, so `deploy://grain.log` has a real `grain-daemon.service` journal to read:
    ```sh
    grain daemon -data-dir d1 -sandbox-dir sbx-d1 -ui-addr 127.0.0.1:7811 -sandbox-port-base=0 \
      -sandbox-backend container -container-sandbox-image <image> -container-git-proxy-host 10.241.0.1 \
      -github-host 127.0.0.1:4250 -github-insecure-http
    ```
    Then, over the API: `POST /api/repos`, `PUT .../ci-mode {"ciMode":"grain"}`, a sim GitHub token, `PUT /api/settings` with a custom agent backend and `sandboxMemoryOvercommit: 2`. The default 8 GiB agent sandbox fills this host's budget, and the CI job would otherwise never start beside it.
  - **The agent** is a custom backend (`agent.py`, a ~50-line Python MCP client). It starts the run's own `mcp_server` (`grain mcpserver -container grain-sandbox-<run> …`) and sends every command of a list through grain's **`run_command`** tool, so each one ran with the PATH a real agent's commands get (`/home/debian/.grain/bin:…`). Before the commands, it `docker cp`'d a full `cmd/grain` into the container as `/home/debian/grain-full`, plus `cmp.sh`, which runs each `grain …` once with the placed `grain-sandbox` and once with the full `grain` and says whether the output and exit status are identical.

## Outcome

| Gap (§5 "What is NOT proved") | Result |
|---|---|
| 1a. Stale image receives the daemon's build; PATH picks it first | **Pass (live).** On `grain-sandbox-stale:test`, run 2-1 found `/home/debian/.grain/bin/grain` (12031917 bytes, sha256 `138aec2c…` = the daemon's `grain-sandbox`). `command -v grain` → `/home/debian/.grain/bin/grain`, and `/usr/local/bin/grain` was still the stale `a5d460a4…`. `grain ls ci://acme/widgets` worked. |
| 1b. Identical bytes already in the image → nothing placed | **Pass (live).** On `grain-sandbox-current:test` (run 5-1), `/home/debian/.grain/bin/` did not exist and `command -v grain` → `/usr/local/bin/grain` (`138aec2c…`). Setup took no measurable extra time: the agent started at 22:30:08, the same second as `orchestrator: task 5: its sandbox fetches …`, and its first command answered at 22:30:09. |
| 2. Every subcommand from inside a real sandbox, using only `grain-sandbox`, compared with a full `grain` | **Pass, after F1 (help) and F3.** 47 commands in run 4-1; table below. Every output and exit status was identical between the two binaries, except where the answer itself changed between the two calls: `deploy://health`'s `ageMS`, and the journal growing by the first call's own audit line. |
| 3. Filter edge cases: `--lines`, invalid `--since`, `--min-priority` values, `--repo` outside the grant | **Pass, after F3**; F1 and F2 found here. Details below. |
| 4. Placement failure is logged and the run still starts | **Pass (live).** On `grain-sandbox-robin:test` (run 6-1), the journal shows `orchestrator: task 6: the sandbox keeps its image's grain: placing /home/debian/.grain/bin/grain: creating remote /home/debian/.grain/bin/grain with mode 0755: install: cannot create regular file '/home/debian/.grain/bin/grain': Permission denied`. The agent then ran: `command -v grain` → `/usr/local/bin/grain` (the stale one). "No space" was not reproduced separately: it fails the same `install` call and takes the same logged branch. |
| 5. A capability placement named `grain` is refused | **Pass (Go integration test, not live).** Configuring a third-party capability server into the live daemon was more than this run could fit. The new `TestHTTPCapabilityServerCannotPlaceTheSandboxGrain` (`pkg/capability/protocol/e2e_test.go`) runs a real HTTP capability server through `Attach`/`Normalize`/`MintPass`. A mint placing `/home/debian/.grain/bin/grain` fails with `mint response: placement path "/home/debian/.grain/bin/grain" is reserved`, while `/home/debian/.grain/bin/mytool` is placed. |

## Findings

| # | Finding | Outcome |
|---|---|---|
| F1 | **`--tail` counts bytes, but `grain log --help` said "the last N lines".** The namespace serves every `tail` as bytes (`pkg/namespace/namespace.go`). From the sandbox, `grain log --tail 2` printed `.` (the last two bytes, `.\n`). `grain ci log --job test --step say --tail 1` printed an empty line (the trailing `\n`) instead of `second line`. The `grain read` and `grain ci log` help did not say which. | **Fixed** (`c364ee45`): all three help texts say bytes. `TestTailHelpSaysBytes` pins it. Whether `--tail` *should* count lines is a design question for the namespace, not changed here. |
| F2 | **`grain log` reads were audited as `task (no task)`.** Every read made through `deploy://grain.log`, the sandbox CLI's path, logged `grain mcpserver: read_grain_log: task (no task) read daemon …`. `sandboxDeploy.ReadGrainLog` built the tool without `TaskID`, so the audit trail of which run read the journal named nobody. | **Fixed** (`f07c5ddd`): `namespace.GrainLogQuery.TaskID` is set from the caller's `Scope`, never from the query string, and passed on. Live after the fix: `read_grain_log: task 6 read daemon …`, `task 8 read daemon …`. `TestDeployReadsForwardTheirQuery` and `TestSandboxGrainLogReadIsAuditedAsItsTask` pin it. |
| F3 | **`grain log` and `grain metrics` silently ignored unknown flags.** `grain log --repo other/repo --max-lines 1` returned the log, unscoped. A misspelt `--min-prority err` would have answered with every priority, and `metrics --repos x` with every repository. `sandboxArgs.only` exists for exactly this ("a command whose every flag narrows its answer"), but only `task list` used it. | **Fixed** (`c9b99043`): both commands call `only` with their documented flags and aliases. Live after the fix: `grain: log: unknown flag --repo: run 'grain log --help' for usage` (exit 1), and `grain: metrics: unknown flag --repos: …`. `TestSandboxLogAndMetricsRefuseUnknownFlags` also checks that no read is made. |
| O1 | `grain log --since 10m` (the plan's own example) is refused: `since "10m" is not an RFC3339 timestamp …; this tool takes no other kind of time`. That matches the help (`--since <rfc3339>`), and `grain metrics --since` takes a date or a timestamp, also as documented. | Not a bug. The plan's example was wrong. |
| O2 | `grain ci log --repo other/repo` with no run id answers `this task's branch has no grain CI run yet`: `--repo` only places a named run. It is not a scope check. | Behaves as its help says. Noted only. |
| O3 | The placement is skipped for a reused (durable or resumed) sandbox (`!cfg.sandboxReused`), so a durable sandbox resumed after a daemon upgrade keeps the previous daemon's `~/.grain/bin/grain`. The skip is by design, and no image bakes `~/.grain/bin`, so a stale copy there cannot shadow a current `/usr/local/bin/grain` in a fresh sandbox. | Noted, not changed. |
| O4 | This run's own (production, granule) sandbox had no `~/.grain/bin/grain`. Its `/usr/local/bin/grain` knows `ls`, but cannot find the proxy because of the `%3a` in `~/.git-credentials`, which grain's notes already describe. That deployment's daemon is the Google-layer build, outside this repo. | Not core; matches a known note. |

## Steps and observations

### 1. The stale image (gap 1a)

Run 2-1, first `run_command`:

```
PATH=/home/debian/.grain/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
/home/debian/.grain/bin/grain
-rwxr-xr-x 1 debian debian 12031917 Oct 31 22:21 grain
138aec2c090dea223ff6632b7fd60003ab7e5242cf7157f20877635ac0a08c11  /home/debian/.grain/bin/grain
a5d460a42ff4302998f26b6388e1431a422f0cce589479fdaf165223d7864a4d  /usr/local/bin/grain
```

### 2. The full sweep (gap 2), run 4-1 on the stale image

Each line: the command; what the placed `grain-sandbox` printed; whether the full `grain` matched.

| Command | Result | Full `grain` |
|---|---|---|
| `git push` then `grain pr open` | `Pull request #9001 in acme/widgets … No checks have reported against it yet.` | — |
| `grain ci wait` | `grain CI passed on 6be265298311 (run 53ff74103351d607, waited 1m55s; wait credit used: 1m55s / 45m0s): test` | — |
| `grain ci list` | 2 runs, newest first, both `success` | identical |
| `grain ci log` | `test/` | identical |
| `grain ci log --job test --step say --tail 1` | an empty line (F1) | identical |
| `grain pr status` / `grain pr base-status` | latest run `success`, `test: success` / `main … has no grain CI run yet` | identical |
| `grain ls ci://acme/widgets`, `…/<run>`, `…/<run>/test` | `53ff…/ b737…/`; `test/`; `attempts/ say.log` | identical |
| `grain read ci://acme/widgets/<run>/test/say.log` | `WALKTHROUGH-CI-MARKER line one` / `second line` | identical |
| `grain grep MARKER ci://acme/widgets` | `53ff…/test/say.log:1:WALKTHROUGH-CI-MARKER line one` (and the other run) | identical |
| `grain ls task://`, `task://2`; `grain grep walkthrough task://2` | `1/ 2/ 3/ 4/`; `attempts/ conversation.md handover.md prompt.md record.md summary.md`; 5 hits | identical |
| `grain read task://2/description.md` | `grain: not found` (exit 1): no such file, the listing above is what exists | identical |
| `grain ls deploy://`; `grain read deploy://health` | `grain.log health merge-queue metrics/ pipeline-changes state/`; health JSON | identical / only `ageMS` differed |
| `grain task message 1 "…"` | `forbidden: task 4 may only message its parent or its children … needs api:task-admin` (exit 1) | identical |
| `grain metrics --window 24h --repo acme/widgets` | `scope: tasks in acme/widgets`, `tasks filed 3` … | identical |
| `grain metrics --window 7d`, `--since 2026-10-30 --until 2026-11-01`, `--author debian`, `--repo acme/widgets,other/repo` | windows `168h`, `48h`; scopes `tasks by debian`, `tasks in acme/widgets or other/repo` | identical |
| `grain host-script --list`; `grain host-script cluster-autoscaler --grain-help` | `no host scripts are granted to this sandbox`; `no host script "cluster-autoscaler" is granted …` (exit 125) | identical |
| `grain help` | the full sandbox CLI help, naming the proxy `http://10.241.0.1:45601` | identical |

The `grain log` lines of that sweep all answered "No entries matched": that daemon ran outside systemd, with no `grain-daemon.service` journal to read. They were re-run under `systemd-run` (section 3).

### 3. Filters and edge cases (gap 3), runs 4-1, 5-1, 7-1 and 8-1

`grain log` against a real journal (run 5-1, current image):

- `--max-lines 3` → `!! TRUNCATED: 41 entries matched and the 3 most recent are below …`, 3 entries. `--lines 2` → 2 entries (the alias works).
- `--since <10 min ago> --contains orchestrator --max-lines 5` → `daemon log, since 2026-10-31T22:20:09Z, containing "orchestrator".` and the one matching entry.
- `--until <an hour ago>` → `No entries matched` (the daemon started minutes earlier). `--since X --until Y` → `daemon log, X to Y.`
- `--min-priority err` → no entries, since nothing logged at err. `--min-priority info --max-lines 2` → 2 entries. `--priority warning` is accepted as the same flag.
- `--min-priority bogus` → `min_priority "bogus" is not a journald priority; the ones there are: emerg, alert, crit, err, warning, notice, info, debug` (exit 1).
- `--since not-a-time` and `--since 10m` → `since "…" is not an RFC3339 timestamp …` (exit 1).
- `--max-lines 0` and `-1` → `bad request: max_lines "0" is not a positive whole number of entries` (exit 1). `--max-lines 5000` and `1001` → served, clamped: the audit line says `max_lines 1000`.
- `--max-lines 5 --tail 120` → the last 120 **bytes**, starting mid-line (`uerreotype grain[16543]: …`) (F1).
- `grain log dmesg --max-lines 2` → `dmesg log, … 560 entries matched`.

Out-of-grant reads (run 7-1, a task holding only `grain-ci` and `grain-log`):

- `grain ls ci://other/repo` → `grain: forbidden: ci://other/repo is outside this sandbox's CI read scope` (exit 1), from both binaries. With `self-debug` (run 4-1), the same listing is empty with exit 0, because self-debug reads every repository's CI.
- `grain metrics --repo other/repo` → `forbidden: deploy://metrics: that tool belongs to the "self-debug" capability …`. Metrics are all-or-nothing under self-debug, which already reads every repository, so `--repo` filters the report rather than widening a grant.
- `grain metrics --window bogus` → `window "bogus": expected a duration like 36h, or a count of days or weeks like 7d`. `--since not-a-date` → `moment "not-a-date": expected a date like 2026-01-31, or an RFC 3339 timestamp`. `--window 24h --since 2026-10-30` → `since and window both say where the report starts: give one or the other`.
- `grain log --repo other/repo` → accepted, whole log (F3). After the fix (run 8-1): `grain: log: unknown flag --repo …` (exit 1). Likewise `--min-prority` and `metrics --repos`.

### 4. Placement failure (gap 4), run 6-1

See the Outcome table: the setup logged `the sandbox keeps its image's grain: … Permission denied`, the agent ran all three commands, and `grain` resolved to the image's `/usr/local/bin/grain`.

### 5. Capability placement named `grain` (gap 5)

Go integration test only (see the Outcome table). `protocol.ReservedPath` refuses `SandboxBinDir/grain` and allows any other `[a-z0-9][a-z0-9_-]*` name there. `applyPlacements` itself does not re-check reserved paths: it relies on `MintResponse` validation for external servers, and in-tree capabilities are trusted (their own placements, such as `skill`, call `ReservedPath`).

## Tests and CI

- New or updated: `TestTailHelpSaysBytes`, `TestSandboxLogAndMetricsRefuseUnknownFlags` (`pkg/sandboxcli`); `TestSandboxGrainLogReadIsAuditedAsItsTask` (`pkg/daemon`); `TestDeployReadsForwardTheirQuery` (`pkg/namespace`, now expects the scope's task); `TestHTTPCapabilityServerCannotPlaceTheSandboxGrain` (`pkg/capability/protocol`). Each fix's test was run with the fix stashed and fails there.
- `go test ./pkg/sandboxcli/... ./pkg/namespace/... ./pkg/daemon/... ./pkg/cli/... ./pkg/capability/protocol/... ./pkg/gitproxy/...`: all `ok` locally.
- Trust boundary: `pkg/capability/protocol` gained a test only, no code change. Sandbox setup (`pkg/orchestrator`) is not touched.
- `release images` / `promote release images` fail on `main` itself (`GHCR_TOKEN secret is required to publish packages under ghcr.io/bwsalmon/grain`): already failing, not from this branch.

## Reproducing

`docs/test-runs/2026-10-31-sandbox-grain-cli/` holds the harness:
- `simhost.go.txt`: the GitHub stand-in. Copy it to a `main.go` inside the module to build it.
- `agent.py` and `cmp.sh`: the agent and the two-binary comparison.
- `start-sd.sh`: the daemon under `systemd-run`.
- the three `Dockerfile.*` images.
- `cmds-sweep.txt` and `cmds-after-fixes.txt`: the command lists.
- `sweep-transcript.txt`: run 4-1's full transcript, before the fixes.
