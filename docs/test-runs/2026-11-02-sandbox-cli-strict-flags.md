# The sandbox `grain` CLI: strict flags, byte/line counts, `deploy://pipeline-changes` paging, `src://core`, `ci wait` supersession, sibling listing, `resume_later`

- **Plan:** `docs/test-plans/2026-11-02.md` §7 (PR #330, #318, #382, #252, #335, #371, #248), "What is NOT proved" 1–6
- **Date:** 2026-11-02, 05:54–06:40 UTC
- **Task:** [Task #2449](/tasks/2449), branch `grain/049f1e09/task-2449-walkthrough-strict-sandbox` on `ai-on-gke/grain`, base `main` at `8726f7f7` (origin: [Task #2399](/tasks/2399))
- **Transcripts and harness:** [`2026-11-02-sandbox-cli-strict-flags/`](2026-11-02-sandbox-cli-strict-flags/)

## Environment

- Debian 12 task sandbox, 7 vCPU / 15 GiB, Go 1.26 (the checkout's `go 1.26.6`), git, no Docker work needed.
- Binaries from this branch: `go build -o ~/bin/grain ./cmd/grain` and `go build -o ~/bin/grain-sandbox ./cmd/grain-sandbox`. Both were rebuilt after each fix. The UI was not built, because nothing here renders a page.
- **Daemon side:** `grain demo -github-sim -fake-ci -git-proxy -reconcile 2s -addr 127.0.0.1:7811`. That is the real `gitproxy.BuildProxy` and namespace server over the demo's store. A run and its sandbox token were minted with `POST /api/e2e/git-proxy/runs {"taskId": …}`. Two more demos served the `src://core` cases: one from the layer module's directory (`:7812`), one from a layer whose core source did not ship (`:7813`).
- **Proxy log:** `fwd` ([`fwd.go.txt`](2026-11-02-sandbox-cli-strict-flags/fwd.go.txt)), a 20-line `httputil.ReverseProxy`, sat in front of each demo's proxy (`127.0.0.1:9800`–`9802`) and logged every request line. `sweep.sh` ran each command and recorded the requests `fwd` saw during it. That is the "no proxy request was made" column below.
- **The sandbox side:** the in-sandbox binary, `grain-sandbox`, was run under `env -i HOME=<scratch home> PATH=/usr/bin:/bin` ([`sb.sh`](2026-11-02-sandbox-cli-strict-flags/sb.sh)). Each scratch home held only a `~/.git-credentials` line, `http://sandbox:<token>@127.0.0.1%3a9800`, the `%3a`-encoded form a real sandbox has. **`GRAIN_SANDBOX_PROXY` was not needed.** The CLI found the proxy from the credential file on its own every time.
- The harness of `docs/test-runs/2026-10-31-sandbox-grain-cli/` (Docker images, `agent.py` MCP driver) was not reused as such. A sandbox container adds nothing to a test of argument parsing and proxy requests. The binary, the credential discovery and the proxy are the same, and `fwd` gives a sharper "did it reach the proxy" signal than the proxy's own audit log, which records git operations only.
- Gaps 4 and 6 need a dispatched run, which `grain demo` does not do. They ran as `tests/e2e` walkthroughs instead (committed, so CI re-runs them):
  - **Gap 4:** a real `grain daemon` (`daemon.Run`, embedded grain CI, host sandboxes) and the `ci_wait_shutdown_walkthrough_test.go` pattern. A stub `claude` runs the real in-sandbox `grain`.
  - **Gap 6:** `deliveryWorld` from `interactive_message_delivery_walkthrough_test.go`: the real store, UI server and orchestrator, plus a stub framework that forks the real `grain mcpserver` and drives it over MCP.

  No real agent backend was used.

## Outcome

| Gap | Result |
|---|---|
| 1. Strict flags, bad counts, unknown subcommands, `--lines`/`--tail`, no proxy request on a usage error | **Pass after F1–F3.** The before-fix sweep ran 87 command lines: an unknown flag on all 45 entries of `sandboxCommandFlags`, 3 flags missing their value, 24 bad counts, 11 unknown subcommands or commands, and 4 pipeline flags on another URI. 86 exited 2 with no proxy request. `grain ls` with no URI exited 1 (F2). After the fixes all 87 exit 2 with `proxy=[none]` ([`transcript-usage-after.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-usage-after.txt)). The sweep also found that `grain log -n 1` was refused (F1). `--lines`/`-n`/`--tail` on `read`, `task prompt`, `task transcript` and `log` behave as documented. |
| 2. Paging the real feed | **Pass, after F4.** 130 tasks filed on top of the demo's 28 changes, so 158 changes in the feed. The default page is 100 + 58, in 2 pages. `--page 7` gives 23 pages (22×7 + 4). The two walks are **identical, in order, with no duplicates**. Each `next:` line carries the `--page` it was read with. The last cursor answers `Nothing has changed since that cursor.` `--cursor`/`--since`/`--page` on `task://…` or `deploy://health` are refused with exit 2 and no request. |
| 3. `src://core` in both builds | **Pass.** Details below. |
| 4. `grain ci wait` across two quick pushes | **Pass for the wait after the second push; F5 for the wait between them.** A wait started after both pushes reports commit 2's pass. A wait started between them answered `grain CI failed on <commit 1> (run …, cancelled …)` with no reason. Fixed so that it says grain cancelled it for the newer commit's run, and which commit to wait on. |
| 5. `grain task list` under `api:tasks:read@acme/widgets` | **Pass.** Task 150 holds only that grant. It lists exactly the 144 `acme/widgets` tasks. `acme/gadgets`' task 151 and the six repo-less tasks (14–19) are absent from the list, from `--repo acme/gadgets` and from `--search`. `grain read task://151/record.md` is refused, naming both ways to read it. |
| 6. `resume_later` with the deadline notice appended | **Pass (live, tests/e2e).** `resume_later`'s real answer was `resume_on: {"at":"…07:16:52Z"}` followed by `[grain] 4m left before grain cancels this run …`. The task settled `deferred`, `DeferredUntil` about an hour out, after 1 run. |

## Findings

| # | Finding | Outcome |
|---|---|---|
| F1 | **`grain log -n 5` was refused: `grain log: unknown flag -n (flags: --contains, --lines, …)`.** `-n` is in `log`'s `sandboxCommandFlags` entry and in its help (`--lines N, -n N`). The `log` and `metrics` cases in `run` still had their own `p.only(...)` lists from the 10-31 F3 fix, from before PR #330's table. `log`'s list lacked `n`. (`log -n x` was refused correctly, by the count check, which runs before the case.) Repro: `grain log -n 1` in any sandbox, exit 2. | **Fixed** (`dcb0e587`). Both per-case lists are gone; the table is the one list. `TestSandboxLogAndMetricsRefuseUnknownFlags` now also runs `log -n 5`. |
| F2 | **A missing positional argument exited 1, with no command named.** `grain ls`, `grain read`, `grain grep x`, `grain ci show`, `grain task prompt`, `grain task wait`, `grain quarantine` and `grain status` printed `grain: missing <uri>: run with --help for usage` (exit 1). Every other usage error exits 2 as `grain <cmd>: …`. None reached the proxy. Also: `grain memory doc` (without `write`) said `unknown command` for a subcommand the list has, and `grain ci frob`'s help left out `ci show`. | **Fixed** (`dcb0e587`). The new `sandboxArgs.missing` gives `grain ls: missing <uri>; run 'grain ls --help' for usage`, exit 2. `memory doc` says `the command is 'grain memory doc write' (or doc-write)`. The `ci` help names `show`. Pinned by `TestMissingArgumentIsAUsageError` and by `TestEverySubcommandIsInItsCommandsHelp`, which checks every subcommand in `sandboxCommands` against its parent's help. |
| F3 | **`grain log --max-lines 0` (and `--lines 0`, `-n 0`) reached the proxy and exited 1.** The CLI's count check allows 0, and `read_grain_log` then refuses it: `bad request: max_lines "0" is not a positive whole number of entries`. | **Fixed** (`024a7e4a`). Usage error, exit 2, no request. |
| F4 | **`grain read deploy://pipeline-changes --page 0` reached the proxy and exited 1** (`bad request: page "0" is not a positive whole number of changes`). This was the only line of the usage sweep's shape that got past the CLI. | **Fixed** (`c4611a8e`). `grain read: --page "0" is not a page size: it takes a positive integer`, exit 2. |
| F5 | **A `grain ci wait` whose run grain cancelled for a newer push said only "failed … cancelled".** The run pushes commit 1, starts `grain ci wait`, pushes commit 2 and starts a second `grain ci wait`. The second wait's `StartTaskCI` cancels commit 1's run, with reason `superseded by run <2>, started for the same task`. `followSuperseded` rightly does not follow onto another commit, as its comment says. But `CLIWaitResponse` carried no cancel reason at all, so the first wait printed `grain CI failed on 9b8983dd8e58 (run …, cancelled …). Failing: a (cancelled)`. An agent cannot tell that from a real failure, though `wait_for_checks` prints the reason (`Grain cancelled this run itself: …`). Repro: `TestCIWaitAcrossTwoQuickPushes`. | **Fixed** (`5039debd`). The answer now carries `cancel_reason`, `cancelled_by_hand`, and, for a successor on another commit of the task's branch, `superseded_by` and `superseded_by_commit`. The CLI prints `Grain cancelled this run itself: superseded by run 76c9…, started for the same task` and `That run is on e0450936ab15, a newer commit on this branch, and its verdict is the branch's: run 'grain ci wait' again for it.` `status` and the recorded verdict are unchanged, so an older CLI on a newer proxy reads the answer as before. Pinned in `TestCLIWaitFollowsTheRunThatSupersededIt` (proxy), `TestSandboxCIWaitReportsEachVerdict` (CLI wording) and the e2e walk. |
| F6 | **`grain grep <re> src://pkg` on core cannot find anything past the 2000th file.** `pkg/` of this checkout has 3271 files, and `namespace.maxWalkFiles` is 2000. `grain grep 'func CoreSource' src://pkg`, `src://core/pkg` and `src://` each answered only `[truncated: walked 2000 entries; narrow the path]`, exit 0, though the function exists. `grep LAYERMARK src://pkg` found the layer's `pkg/layeronly/x.go` but not its `pkg/namespace/coresrc.go`, which comes later in the walk. `src://pkg` is the path every example and grain's own notes name, and it reads core on every layer build. | **Filed** as a follow-up (below). Narrowing works and is what the truncation line says to do: `src://pkg/namespace`, or `src://pkg --glob 'namespace/*.go'`. |

## Steps and observations

### 1. The sweep (gap 1)

[`cmds-usage.txt`](2026-11-02-sandbox-cli-strict-flags/cmds-usage.txt) is every command in `sandboxCommandFlags` with `--bogus x`, plus flags with no value, bad counts (`abc`, `-1`, `1.5`, `x`, `--offset=-3`, `ten`, `zero`) on every count flag, unknown subcommands of every command that has subcommands, an unknown top-level command, and the pipeline flags on another URI. Before the fixes ([`transcript-usage.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-usage.txt)):

```
grain ls task:// --bogus x            exit=2 proxy=[none]  grain ls: unknown flag --bogus (flags: --glob); run 'grain ls --help' for usage
grain ci show r1 --bogus x            exit=2 proxy=[none]  grain ci show: unknown flag --bogus (it takes none); …
grain memory doc write x --bogus x    exit=2 proxy=[none]  grain memory doc-write: unknown flag --bogus (flags: --file, --reason, --repo, --summary); …
grain read task://2/record.md --tail  exit=2 proxy=[none]  grain read: flag --tail needs a value
grain read … --offset=-3              exit=2 proxy=[none]  grain read: --offset "-3" is not a count: it takes a non-negative integer
grain grep foo task://2 -C x          exit=2 proxy=[none]  grain grep: -C "x" is not a count: …
grain ci frob                         exit=2 proxy=[none]  grain ci: unknown command "frob" + grain ci's help (without show: F2)
grain frob                            exit=2 proxy=[none]  grain: unknown command "frob" + the sandbox help
grain read task://2/record.md --page 5  exit=2 proxy=[none]  grain read: --page pages deploy://pipeline-changes, not task://2/record.md
grain ls                              exit=1 proxy=[none]  grain: missing <uri>: run with --help for usage     (F2)
```

After the fixes ([`transcript-after.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-after.txt), [`transcript-usage-after.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-usage-after.txt)) all 87 lines read `exit=2 proxy=[none]`, and so do the new missing-argument, `memory doc` and zero-count lines.

`--lines`/`--tail` on real files ([`transcript-counts.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-counts.txt), [`transcript-log.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-log.txt)):

- **`read`:** `grain read task://5/record.md --tail 20` sends `?tail=20` and prints the last 20 bytes (`\n\nConversation (0):\n`). `--lines 2` and `-n 1` send no byte range and print the last 2 lines and 1 line. `-n=3` works. `--lines 0` prints nothing. `--tail 40 --lines 1` is the last line of the last 40 bytes. `--offset 0 --limit 12` prints `Task 5: Rota`.
- **`task transcript`:** says on stderr `grain: task://5/transcript.md is 0 bytes; --tail, --offset and --limit take bytes, --lines N the last N lines`. `--attempt 1` on a task with no attempt is `not found` (exit 1, a real read).
- **`grain log`**, as task 152 holding `grain-log`:
  - `--tail 10` prints `aemon log` (10 bytes), as the help's "the last N BYTES" says. `-n 1` and `--lines 1` send `max_lines=1`. `--since 10m` is sent as an RFC 3339 instant.
  - **Audit (PR #318):** every one of the 8 reads that answered appears in the demo log as `grain mcpserver: read_grain_log: task 152 read daemon (…)`. The refused `--max-lines 0` shows only as the proxy's `status 400` line (and is now refused in the CLI, F3).

### 2. Paging (gap 2)

[`follow.sh`](2026-11-02-sandbox-cli-strict-flags/follow.sh) reads the feed, then runs each page's `next:` line until a page has no `(more changes follow …)` marker.

```
page 1: grain read deploy://pipeline-changes -> exit 0, 100 changes, 1 more-marker; next: grain read deploy://pipeline-changes --cursor 2026-11-02T06:02:05.141071244Z~0
page 2: grain read deploy://pipeline-changes --cursor 2026-11-02T06:02:05.141071244Z~0 -> exit 0, 58 changes, 0 more-marker; next: … --cursor 2026-11-02T06:02:05.796755809Z~0
```

`--page 7` ([`transcript-paging-7.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-paging-7.txt)) takes 23 pages. A cursor with a tie-break sequence appears (`2026-11-02T05:15:29.705586094Z~1`, two changes at one instant), and the walk does not drop or repeat either change. The concatenated 158 lines are byte-identical to the two default pages' 158.

Edge cases:

- `--page 1` gives one change and `next: … --page 1`.
- `--since 2026-11-02T06:02:05Z --page 3` starts at that time.
- `--since yesterday` gives `"yesterday" is not a pipeline cursor: want <RFC 3339 time>~<seq>, as the last read's next` (exit 1, the namespace's words: a value check, not a usage error).
- `--page 100000` returns all 158.
- `--page 3 --tail 50` and `--lines 1` apply to the page's text, so the `next:` line survives `--lines 1`.

### 3. `src://core` (gap 3)

- **Core's own demo** (`grain demo` from the checkout): `src://` is the demo's scratch tree (`playbooks/`). The checkout has no `playbooks/`, so `demoSrcDir` does not serve it. `src://core` and `src://pkg` are `not found`. That is as documented for core itself: `CoreSource` returns nothing for a tree that is core, or has no `go.mod`, and only a daemon "whose own module builds on grain core" serves `src://core/` (`grain ls --help`).
- **A layer build:** a scratch module `example.com/layer` ([`layer-go.mod.txt`](2026-11-02-sandbox-cli-strict-flags/layer-go.mod.txt)) with:
  - `require github.com/ai-on-gke/grain v0.0.0-…` and `replace github.com/ai-on-gke/grain => /home/debian/work`;
  - a `cmd/layergrain` that runs `cli.Main()`, built with `go build`;
  - `playbooks/`, a layer-only `pkg/layeronly/x.go`, and a `pkg/namespace/coresrc.go` that shadows core's, each marked `LAYERMARK`.

  It needed core's own `replace github.com/bwsalmon/granule => …/third_party/granule` as well: a module that requires core does not inherit core's replace, and without it `go mod tidy` tries to fetch granule from GitHub. A real layer has the same requirement.

  Its demo, run from the module's directory ([`transcript-src-layer.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-src-layer.txt), [`transcript-src-layer2.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-src-layer2.txt)):
  - `grain ls src://` is the union, with `core/` added. `grain ls src://pkg` is the union too (`layeronly/` beside core's packages).
  - `grain ls src://core` is core's root alone (no `core/`, no `playbooks/`).
  - `src://pkg/namespace/coresrc.go` is the layer's (`LAYERMARK the layer shadows core here.`): the layer wins. `src://core/pkg/namespace/coresrc.go --lines 3` is core's last three lines.
  - `src://core/pkg/layeronly/x.go` is `not found`.
  - `grain grep 'maxWalkFiles = ' src://pkg --glob 'namespace/*.go'` finds core's `namespace.go:67`, a file the layer lacks, so grep reads through the union. `grep 'func CoreSource' src://core/pkg/namespace` finds core's line 41. `grep LAYERMARK src://pkg --glob 'namespace/*.go'` finds the layer's copy, and the same under `src://core/pkg` finds nothing.
  - Unnarrowed greps truncate (F6).
  - `src://playbooks` is the bootstrap-playbooks mapping (`cloudsql-postgres.md` …), not the layer's own `playbooks/` directory. That is by design, so `grep LAYERMARK src://playbooks` is empty.
- **A layer whose core source did not ship** (`go.mod` requires `github.com/ai-on-gke/grain v0.0.0-20990101000000-abcdefabcdef`, no replace, not in the module cache; [`transcript-src-miss.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-src-miss.txt)): `grain ls src://core`, `grain read src://core/pkg/namespace/coresrc.go` and `grain grep x src://pkg/nosuch` all answer:

  ```
  grain: not found: src://core -- src:// serves this daemon's own module, which builds on github.com/ai-on-gke/grain@v0.0.0-20990101000000-abcdefabcdef, and that source did not ship with this daemon. To read core: `go mod download -json github.com/ai-on-gke/grain@v0.0.0-20990101000000-abcdefabcdef` in grain-src/ (or any checkout of this module) and read the Dir it prints, or clone ai-on-gke/grain if your task reads it
  ```

  The layer's own `src://pkg/own` still lists. `grep CoreSource src://pkg` there finds nothing, with no hint: the layer has a `pkg/` of its own, so nothing is missing as such.
- The first candidate in the lookup order, `/usr/local/share/grain/core-src`, was not exercised: nothing was installed there. The replace and the "not shipped" cases cover the other two branches, and the module-cache branch is `TestCoreSource`'s.

### 4. `grain ci wait` across two pushes (gap 4)

`TestCIWaitAcrossTwoQuickPushes` (`tests/e2e/ci_wait_two_pushes_walkthrough_test.go`) uses a real daemon with embedded CI and a one-job workflow (`sleep 10`). The stub agent, in its sandbox checkout, does the following with the real `grain` binary found through its `.git-credentials`:

1. commits and pushes;
2. starts `grain ci wait` in the background;
3. three seconds later, commits and pushes again;
4. starts a second `grain ci wait`.

Live output ([`transcript-ci-two-pushes.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-ci-two-pushes.txt)), after F5:

```
--- wait.1
grain CI failed on 9b8983dd8e58 (run ad874071cca18e1c, cancelled, waited 5s; wait credit used: 5s / 45m0s).
Grain cancelled this run itself: superseded by run 76c92248b90e0c48, started for the same task
That run is on e0450936ab15, a newer commit on this branch, and its verdict is the branch's: run 'grain ci wait' again for it.
Failing: a (cancelled)
exit=1
--- wait.2
grain CI passed on e0450936ab15 (run 76c92248b90e0c48, waited 15s; wait credit used: 19s / 45m0s): a
exit=0
```

Before F5, wait.1 was the first line and `Failing: a (cancelled)` only. Neither push started a run by itself: each run was started by a `grain ci wait` (`StartTaskCI`). The second run's start is what cancelled the first.

Whether a wait on an older commit's run should *follow* onto the newer commit, as the plan's wording ("reports the second run's verdict") suggests, is a design question I did not change. `followSuperseded` says "A run on another branch or commit is a different question and is never followed", and `wait_for_checks` (`pkg/mcp`'s `followSupersede`) does the same. Following would have one wait report a verdict for a commit other than the one it printed. F5 makes the answer say what happened and what to run instead.

### 5. Siblings (gap 5)

Task 150 (`capabilities: ["api:tasks:read@acme/widgets"]`, no self-debug) on the core demo, with task 151 filed in `acme/gadgets` ([`transcript-tasklist.txt`](2026-11-02-sandbox-cli-strict-flags/transcript-tasklist.txt)):

- `grain task list --limit 500` gives `144 task(s) shown of 144 matching`. Every one is in `acme/widgets`: tasks 1–13 and 20–150. None is 151 or 14–19.
- `--repo acme/gadgets` gives `No tasks in acme/gadgets.` `--search OTHER-REPO` gives `No tasks with "OTHER-REPO" in the title.`
- `ls task:// --glob '15*'` gives `150/` only (not 15, which is repo-less, nor 151).
- `grain task read 2` reads a sibling. `grain task read 151` is `forbidden: … needs the "self-debug" capability or an "api:tasks:read@<owner/repo>" grant naming its repository`.

### 6. `resume_later` near the deadline (gap 6)

`TestResumeLaterNearTheDeadlineDefersTheTask` (`tests/e2e/resume_later_deadline_walkthrough_test.go`) forks the run's `grain mcpserver` with `-run-deadline` five minutes out. That is inside `mcp.RunDeadlineNoticeWindow` (20m), so every result carries the deadline notice. The script calls `run_command` (notice present), then `resume_later {"reason": …, "when": {"at": "1h"}}`:

```
resume_on: {"at":"2026-11-02T07:16:52.292273542Z"}
[grain] 4m left before grain cancels this run and destroys its sandbox. There is no time for another edit-and-test cycle: …
after the run: state=deferred … runs=1
```

`mcp.ResumeLaterTaken` reads the condition back from that text. The task is `deferred` with `DeferredUntil` about 1h ahead, not finished. The tool-budget notice and mid-run addenda were not exercised live here:

- the budget applies only to durable judgement tasks (`-tool-budget`);
- addenda are skipped on turn-ending tools.

PR #248's unit test covers the budget notice, and its orchestrator test covers a director session with notices.

## Fixes (on this branch)

- `dcb0e587` fix(sandboxcli): accept `grain log -n`, and make a missing argument exit 2 (F1, F2)
- `c4611a8e` fix(sandboxcli): refuse `grain read --page 0` before asking the proxy (F4)
- `5039debd` fix(gitproxy): say why `grain ci wait` found its run cancelled, and for which commit (F5), with `TestCIWaitAcrossTwoQuickPushes`
- `73195638` test(e2e): walk resume_later from a run inside its deadline window (gap 6)
- `024a7e4a` fix(sandboxcli): refuse `grain log --max-lines 0` before asking the proxy (F3)

Each fix's unit test was run with the fix stashed and fails there (F1/F2's three tests checked together; F5's proxy assertion fails on the missing fields). Locally:

- `go test ./pkg/sandboxcli/ ./pkg/gitproxy/ ./pkg/cli/ ./cmd/...`: all `ok`.
- `go test ./tests/e2e/ -run 'TestCIWaitAcrossTwoQuickPushes|TestACIWaitAcrossADaemonRestartResumesToTheVerdict|TestResumeLaterNearTheDeadlineDefersTheTask'`: `ok`.

## Follow-ups

- **F6:** `grain grep` over `src://pkg` (or `src://core/pkg`, `src://`) walks at most 2000 files, fewer than core's `pkg/` holds, and so misses matches with exit 0. Filed for `ai-on-gke/grain`. Options: raise the cap for `src://`, which is source and cheap to scan; walk grep's candidates by `--glob` first; or skip `_test.go` and `ui/node_modules`-like trees by default.

## Not run

- The sandbox CLI inside a real sandbox container or pod (the 10-31 Docker images). Credential discovery, `PATH` and the proxy are the same; the container adds nothing to a test of argument parsing.
- `src://core` from the `/usr/local/share/grain/core-src` candidate, and on the deployed Google-layer image (`experimental/grain#45`). This sandbox has no `/usr/local/share/grain/`, so the self-debug note that core is not yet shipped there still holds for this run's own image.
- The tool-budget and addenda notices on a live `resume_later` (see §6).
- `grain ci wait` on GitHub-checks (`ci_mode: github`) repos. The fake-CI demo starts no runs, and the two-push walk used grain CI.
