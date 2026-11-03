# The sandbox CLI and API edges: `grain grep` over `src://`, `grain log -n`, missing arguments, priority `1e3`

- **Plan:** `docs/test-plans/2026-11-03.md` §10 (PRs #507, #457, #448, #502), "What is NOT proved" 1–3
- **Date:** 2026-11-03, 04:55–05:35 UTC
- **Task:** [Task #2716](/tasks/2716), branch `grain/049f1e09/task-2716-walkthrough-sandbox-cli-and` on `ai-on-gke/grain`, base `main` at `3d811a7d` (origin: [Task #2697](/tasks/2697))
- **Harness and transcripts:** [`2026-11-03-sandbox-cli-edges/`](2026-11-03-sandbox-cli-edges/)

## Environment

- Debian 12 task sandbox: 7 vCPU, 30 GiB, Go 1.26, Docker 29 (`debian:bookworm-slim` pulled from Docker Hub). Node and the UI build (`npm ci && npm run build`) were used for the board check only.
- **Daemon:** a real `daemon.Run` with an embedded git host (`gitHTTPBackend`) and the stub `claude` framework. This is the `tests/e2e` pattern (shared step 3, second bullet). The harness is `TestSandboxCLIEdgesWalkthrough` ([`tests/e2e/sandbox_cli_edges_walkthrough_test.go`](../../tests/e2e/sandbox_cli_edges_walkthrough_test.go)). It is gated on `GRAIN_WALK_SANDBOX_CLI_EDGES=1`, so CI compiles it but does not run it.
- **Run host:** `-container-sandboxes` (Docker, `--network host`, `-container-git-proxy-host 127.0.0.1`), so every run gets its own sandbox container. Its image is `debian:bookworm-slim` with git, an unprivileged `agent` user, this branch's `grain` at `/usr/local/bin/grain`, and `dev.grain.sandbox.contract=2`. The daemon flag help says `-run-host-image` cannot reach container sandboxes, so the agent CLI ran as the daemon's child. The stub `claude` then `docker exec`s [`sweep.sh`](2026-11-03-sandbox-cli-edges/sweep.sh) into the run's container (`grain-sandbox-2-1`) as `agent`. So **every `grain` line below is the sandbox's own CLI, inside a real sandbox container**, finding the proxy through the run's own `~/.git-credentials`.
- **`src://`'s tree:** a daemon serves `src://` from `/usr/local/share/grain/src`, where the image carries it (`Dockerfile:390`, `daemon.sourceDir`). It was staged there with `scripts/export-source.sh` from this checkout: 6614 files, 3317 under `pkg/`. This is core's own daemon, so `src://` is core and `src://core` is `not found`, as documented (`CoreSource` returns nothing for core itself; see the 11-02 report §3).
- **Task:** `self-debug` and `grain#journal`, repo `acme/widgets`.
- **Second data point:** my own task sandbox's `grain` against this deployment's daemon. This task does not hold `self-debug`, so every `src://` read there is refused: `grain: forbidden: src://pkg: that tool belongs to the "self-debug" capability, and this run does not hold it` ([`task-sandbox-forbidden.txt`](2026-11-03-sandbox-cli-edges/task-sandbox-forbidden.txt), [`task-sandbox-grepwalk.txt`](2026-11-03-sandbox-cli-edges/task-sandbox-grepwalk.txt): all 54 greps exit 1). That is a correct refusal, and the only data point it gives. Every timing below is from the walk's daemon, not this deployment's.
- No real agent backend was used.

## Outcome

| Item | Result |
|---|---|
| 1. `grain grep` over `src://pkg` and `src://`, one symbol per package `pkg/orchestrator`..`pkg/ui`; tiny `--max` | **Pass after F1 and F3.** All 27 packages are found under both roots (54 greps, exit 0, one hit each, no truncation). Before F1, each grep took **~16 s** over `src://pkg` and **~50 s** over `src://`. After F1 they take **0.26–0.31 s** and **0.49–1.09 s**. `--max 1` prints one match and `[truncated at match limit (1 matches); pass --max 2 (grain grep) to raise the limit]`, with exit 0 (see "Plan expectation" below). `--max 0` reached the proxy and exited 1 (F3). It is now a usage error. |
| 2. `grain log -n 5`, bare `grain log -n`, every missing argument | **Pass after F2.** `grain log -n 5` and `grain log daemon -n 2` are accepted and answered (exit 0). Bare `grain log -n` exits 2: `grain log: flag -n needs a value; run 'grain log --help' for usage`. The pointer to `--help` is new. 57 command lines were run with no argument, tabled below. Five of them exited 1 for a missing required argument (F2). They now exit 2. |
| 3. Priority `1e3`, `1000.0`, `1.5e2`, `-1`, `1e10`, `"1000"` | **Pass for the API; F4 for the host CLI.** POST and PATCH accept the four whole numbers and refuse `-1` and the JSON string `"1000"`. Before F4, `grain create -priority 1e3` and `grain update -priority 1e3` failed with `invalid value "1e3" for flag -priority: parse error`. They now store what the API stores. The board's list shows each one as stored, and `P10000000000` fits its row at 1440 and 390 px. |

## 1. `grain grep` over `src://`

`sweep.sh` greps each package's symbol ([`symbols.txt`](2026-11-03-sandbox-cli-edges/symbols.txt): a top-level `func`/`type` whose definition exists only in that package) as `^(func|type) (\([^)]*\) )?<sym>[ (\[]`. It runs once under `src://pkg` and once under `src://`. `exit/hits/truncated` is the CLI's exit status, the number of lines in `pkg/<package>/`, and the number of `truncated` notes. These are the after-fix numbers ([`transcript-sweep.txt`](2026-11-03-sandbox-cli-edges/transcript-sweep.txt)):

| Package | Symbol | `src://pkg` exit/hits/trunc | time | `src://` exit/hits/trunc | time |
|---|---|---|---|---|---|
| `pkg/orchestrator` | `ReadoptSandbox` | 0/1/0 | 0.30s | 0/1/0 | 0.54s |
| `pkg/pkgcache` | `SaveCA` | 0/1/0 | 0.26s | 0/1/0 | 0.55s |
| `pkg/policy` | `OpenAuditLog` | 0/1/0 | 0.28s | 0/1/0 | 0.56s |
| `pkg/portpick` | `Pick` | 0/1/0 | 0.27s | 0/1/0 | 0.56s |
| `pkg/ports` | `InConvention` | 0/1/0 | 0.28s | 0/1/0 | 0.53s |
| `pkg/procgroup` | `KillPID` | 0/1/0 | 0.27s | 0/1/0 | 0.54s |
| `pkg/redact` | `pattern` | 0/1/0 | 0.27s | 0/1/0 | 0.55s |
| `pkg/registryauth` | `IsGoogleRegistry` | 0/1/0 | 0.26s | 0/1/0 | 0.55s |
| `pkg/releasenotes` | `Between` | 0/1/0 | 0.28s | 0/1/0 | 0.56s |
| `pkg/repodecl` | `LoadLifecycle` | 0/1/0 | 0.27s | 0/1/0 | 0.53s |
| `pkg/runhost` | `RunFrom` | 0/1/0 | 0.26s | 0/1/0 | 0.54s |
| `pkg/safepath` | `Within` | 0/1/0 | 0.27s | 0/1/0 | 0.54s |
| `pkg/sandboxaccess` | `SecretName` | 0/1/0 | 0.27s | 0/1/0 | 0.55s |
| `pkg/sandboxaddr` | `NewPool` | 0/1/0 | 0.27s | 0/1/0 | 0.54s |
| `pkg/sandboxcli` | `IsCommand` | 0/1/0 | 0.27s | 0/1/0 | 0.54s |
| `pkg/sandboximage` | `CheckContract` | 0/1/0 | 0.27s | 0/1/0 | 0.53s |
| `pkg/schedulefilter` | `Attributes` | 0/1/0 | 0.27s | 0/1/0 | 0.54s |
| `pkg/secrets` | `GenerateKey` | 0/1/0 | 0.26s | 0/1/0 | 0.54s |
| `pkg/secretsrepo` | `sanitize` | 0/1/0 | 0.27s | 0/1/0 | 0.53s |
| `pkg/statemachine` | `WithReduceContext` | 0/1/0 | 0.26s | 0/1/0 | 0.52s |
| `pkg/staterepo` | `LoadExternal` | 0/1/0 | 0.27s | 0/1/0 | 0.52s |
| `pkg/sysstat` | `NewHistory` | 0/1/0 | 0.26s | 0/1/0 | 0.53s |
| `pkg/systemlog` | `Journalctl` | 0/1/0 | 0.26s | 0/1/0 | 0.53s |
| `pkg/treeid` | `Archive` | 0/1/0 | 0.27s | 0/1/0 | 0.54s |
| `pkg/trustboundary` | `OwnerApproval` | 0/1/0 | 0.31s | 0/1/0 | 0.54s |
| `pkg/tunnel` | `TestPingFloodFromNonReadingPeerIsBounded` | 0/1/0 | 0.26s | 0/1/0 | 0.49s |
| `pkg/ui` | `RotateLocalAPIToken` | 0/1/0 | 0.26s | 0/1/0 | 0.52s |

Also from inside the container:

- `grain grep 'func RotateLocalAPIToken' src://pkg` gives `ui/accesscode.go:153:…`, and under `src://` gives `pkg/ui/accesscode.go:153:…`.
- `--glob 'ui/*.go'` takes 0.07 s.
- `src://core/pkg` is `not found` (exit 1), because this is core's own daemon.
- `grain grep NoSuchSymbolAnywhere_2716 src://` is empty with exit 0. Nothing was cut short, so an empty answer is a real "no".
- `--max 100000` is accepted. The namespace clamps it to `MaxGrepMaxMatches`.
- `--max -1`, `--max x` and `--max 1e3` exit 2 from the CLI's count check, with no request made.

**No walk was cut short** on core with the 20000-entry `src://` cap from PR #507, so the walk-truncation exit 1 cannot be reached on core. `TestGrepOfSrcReachesPastTheDefaultWalkCap` and `TestWalkCapIsHigherForSrcOnly` in `pkg/namespace` still pin it.

**Plan expectation for a tiny `--max`.** The plan says "non-zero exit, clear message". `--max` is the *match* limit, not the walk cap. A grep that hits it has found matches, so it exits 0, as grep does. It ends stdout with the truncation line, which says how to raise the limit. The only non-zero exit PR #507 added is for a walk cut short with nothing found, where an empty answer would read as "not there". I left this as it is. Making a found-something answer exit non-zero would break `grain grep … && …`. The case that was wrong is `--max 0`, which is F3.

## 2. `grain log -n` and missing arguments

`grain log -n 5` and `grain log -n 5 --tail 100` are accepted (exit 0): F1 of 11-02 holds. The in-process daemon kept no log of its own for `read_grain_log` to read, so both answered `No entries matched. This is an answer, not a failure …`. Without `grain#journal` (the first run), the same line got `grain: forbidden: deploy://grain.log: that tool belongs to the "…grain#journal" capability` (exit 1). Bare `-n`, `-n 0` and `--lines` exit 2 with no request made.

Every command and subcommand in `sandboxCommands` was run with no argument (two-word entries too, plus `grep x` and `ci artifact go`). The table compares the 11-02 report's F2 column, the exit status on `main` before this branch, and the exit status after this branch:

| Command line | 11-02 (`2026-11-02-sandbox-cli-strict-flags.md`) | before | after | first line of the answer |
|---|---|---|---|---|
| `grain ls` | 1 → 2 (F2) | 2 | **2** | `grain ls: missing <uri>; run 'grain ls --help' for usage` |
| `grain read` | 1 → 2 (F2) | 2 | **2** | `grain read: missing <uri>; run 'grain read --help' for usage` |
| `grain grep` | — | 2 | **2** | `grain grep: missing <pattern>; run 'grain grep --help' for usage` |
| `grain grep x` | 1 → 2 (F2) | 2 | **2** | `grain grep: missing <uri>; run 'grain grep --help' for usage` |
| `grain merge-queue` | — | 0 | **0** | `# Merge Queue Status` |
| `grain status` | 1 → 2 (F2) | 2 | **2** | `grain status: missing <one-line-note> (or --file); run 'grain status --help' for usage` |
| `grain notify` | — | 1 | **2** | `grain notify: missing --subject <key>: a short key for what this notification is about; run 'grain n` |
| `grain api` | — | 2 | **2** | `grain api: missing <routes \| METHOD>; run 'grain api --help' for usage` |
| `grain quarantine` | 1 → 2 (F2) | 2 | **2** | `grain quarantine: missing <test-name>; run 'grain quarantine --help' for usage` |
| `grain metrics` | — | 0 | **0** | `window: 2026-10-27T05:29:10Z -> 2026-11-03T05:29:10Z (168h0m0s)` |
| `grain health` | — | 0 | **0** | `{` |
| `grain log` | — | 0 | **0** | `daemon log, most recent entries.` |
| `grain playbooks` | — | 0 | **0** | `cloudsql-postgres` |
| `grain cap` | — | 0 | **0** | `grain cap list [--json]` |
| `grain ci list` | — | 0 | **0** | `0 of 0 matching run(s) in acme/widgets, newest first:` |
| `grain ci show` | 1 → 2 (F2) | 2 | **2** | `grain ci show: missing <run-id>; run 'grain ci show --help' for usage` |
| `grain ci log` | — | 1 | **1** | `grain: this task's branch has no grain CI run yet` |
| `grain ci test-history` | — | 2 | **2** | `grain ci test-history: missing <test-name>; run 'grain ci test-history --help' for usage` |
| `grain ci artifact` | — | 2 | **2** | `grain ci artifact: missing <job>; run 'grain ci artifact --help' for usage` |
| `grain ci artifact go` | — | 2 | **2** | `grain ci artifact: missing <name>; run 'grain ci artifact --help' for usage` |
| `grain ci wait` | — | 1 | **1** | `grain: this deployment has no grain CI wired` |
| `grain ci run-job` | — | 2 | **2** | `grain ci run-job: missing <job>; run 'grain ci run-job --help' for usage` |
| `grain ci retrigger` | — | 1 | **1** | `grain: this deployment has no grain CI wired` |
| `grain pr status` | — | 0 | **0** | `grain/19995530/task-2-sandbox-cli-edges (this task's branch) in acme/widgets has no grain CI run yet` |
| `grain pr base-status` | — | 0 | **0** | `main (the base branch) in acme/widgets has no grain CI run yet.` |
| `grain pr open` | — | 1 | **1** | `grain: orchestrator: checking 2's branch: orchestrator: cannot read acme/widgets, so "grain/19995530` |
| `grain pr reply` | — | 2 | **2** | `grain pr reply: missing <body> (or --file); run 'grain pr reply --help' for usage` |
| `grain pr resolve` | — | 1 | **2** | `grain pr resolve: missing --thread-id <id> or --comment-id <id>: name the thread; run 'grain pr reso` |
| `grain pr review-comment` | — | 2 | **2** | `grain pr review-comment: missing <body> (or --file); run 'grain pr review-comment --help' for usage` |
| `grain git revert` | — | 1 | **2** | `grain git revert: missing --reason <why>: say why this is being reverted; run 'grain git revert --he` |
| `grain handover write` | — | 2 | **2** | `grain handover write: missing <handover> (or --file); run 'grain handover write --help' for usage` |
| `grain task list` | — | 0 | **0** | `1  awaiting_reply  -  Director` |
| `grain task read` | — | 2 | **2** | `grain task read: missing <id>; run 'grain task read --help' for usage` |
| `grain task prompt` | 1 → 2 (F2) | 2 | **2** | `grain task prompt: missing <id>; run 'grain task prompt --help' for usage` |
| `grain task transcript` | — | 2 | **2** | `grain task transcript: missing <id>; run 'grain task transcript --help' for usage` |
| `grain task wait` | 1 → 2 (F2) | 2 | **2** | `grain task wait: missing <id>...: name at least one task to wait on; run 'grain task wait --help' fo` |
| `grain task message` | — | 2 | **2** | `grain task message: missing <id>; run 'grain task message --help' for usage` |
| `grain task propose` | — | 1 | **2** | `grain task propose: missing --title <title>; run 'grain task propose --help' for usage` |
| `grain memory list` | — | 0 | **0** | `global/` |
| `grain memory read` | — | 2 | **2** | `grain memory read: missing <key>; run 'grain memory read --help' for usage` |
| `grain memory write` | — | 2 | **2** | `grain memory write: missing <text> (or --file); run 'grain memory write --help' for usage` |
| `grain memory doc-write` | — | 2 | **2** | `grain memory doc-write: missing <name>; run 'grain memory doc-write --help' for usage` |
| `grain memory doc` | 2 (F2) | 2 | **2** | `grain memory doc: the command is 'grain memory doc write' (or doc-write)` |
| `grain release-notes stats` | — | 1 | **1** | `grain: {"error":"task 2 holds no api grant for scope release-notes:read, which GET /api/release-note` |
| `grain release-notes publish` | — | 2 | **2** | `grain release-notes publish: missing <markdown> (or --file); run 'grain release-notes publish --help` |
| `grain issue list` | — | 1 | **1** | `grain: forbidden: grain issue: that tool belongs to the "read-issues" capability, and this run does` |
| `grain issue read` | — | 2 | **2** | `grain issue read: missing <number>; run 'grain issue read --help' for usage` |
| `grain db query` | — | 1 | **2** | `grain db query: missing <sql>; run 'grain db query --help' for usage` |
| `grain ci` | — | 0 | **0** | `grain ci (list \| show \| log \| test-history \| artifact \| wait \| run-job \| retrigger) ...` |
| `grain pr` | — | 0 | **0** | `grain pr (status \| base-status \| open \| reply \| resolve \| review-comment)` |
| `grain git` | — | 0 | **0** | `grain git revert (--task <id> \| --commit <sha>) --reason <why>` |
| `grain handover` | — | 0 | **0** | `grain handover write (<text> \| --file <f>)` |
| `grain task` | — | 0 | **0** | `grain task (list \| read \| prompt \| transcript \| wait \| message \| propose) ...` |
| `grain memory` | — | 0 | **0** | `grain memory (list \| read <path> \| write [--repo o/n] <text\|--file f> \| doc-write <name> <body\|--fil` |
| `grain release-notes` | — | 0 | **0** | `grain release-notes (stats \| publish --version <v> <body\|--file f>)` |
| `grain issue` | — | 0 | **0** | `grain issue (list \| read) ...` |
| `grain db` | — | 0 | **0** | `grain db query <sql>` |

**Exit 0** rows are commands that need no argument (`ci list`, `merge-queue`, `health`, `metrics` …) or a bare parent that prints its own help (`grain ci`, `grain git` …). **Exit 1** rows reached the daemon and got an answer about this deployment: no grain CI wired, no CI run yet, an ungranted capability or API scope, or a branch the forge cannot read. Those are real answers, not usage errors. Every other row is exit 2 with `grain <cmd>: missing …; run 'grain <cmd> --help' for usage`.

## 3. Priority

[`priority.txt`](2026-11-03-sandbox-cli-edges/priority.txt), after F4. POST files a new task and PATCH edits one task. The host CLI ran as `grain -json create -title … -repo acme/widgets -priority <v>` and `grain -json update <id> -priority <v>`:

| Spelling | POST `/api/tasks` | stored | PATCH `/api/tasks/<id>` | stored | `grain create -priority` | stored | `grain update -priority` | stored |
|---|---|---|---|---|---|---|---|---|
| `1e3` | 201 | 1000 | 200 | 1000 | exit 0 (was exit 2, `parse error`) | 1000 | exit 0 (was exit 2) | 1000 |
| `1000.0` | 201 | 1000 | 200 | 1000 | exit 0 (was exit 2) | 1000 | exit 0 (was exit 2) | 1000 |
| `1.5e2` | 201 | 150 | 200 | 150 | exit 0 (was exit 2) | 150 | exit 0 (was exit 2) | 150 |
| `-1` | 400 `priority must be a positive integer (1 or more; leave it out for the default, 5)` | – | 400, same | – | exit 1, the API's words | – | exit 1, the API's words | – |
| `1e10` | 201 | 10000000000 | 200 | 10000000000 | exit 0 (was exit 2) | 10000000000 | exit 0 (was exit 2) | 10000000000 |
| `"1000"` (JSON string) | 400, same words | – | 400, same words | – | exit 0 (the shell strips the quotes, so the flag sees `1000`) | 1000 | exit 0 | 1000 |

`GET /api/tasks`, the list the board draws, returned exactly the stored values. The board itself was rendered from `grain demo` (this branch's binary, real UI embedded) with three tasks POSTed at `1e10`, `1e3` and `1.5e2`, shot at desktop 1440 px and phone 390 px in light and dark ([`board-priorities-contact-sheet.png`](2026-11-03-sandbox-cli-edges/board-priorities-contact-sheet.png)). My file reader shows PNGs as bytes and `LOOK=1` has no key in a sandbox, so the layout check is a Playwright bounding-box measurement ([`chip.mjs.txt`](2026-11-03-sandbox-cli-edges/chip.mjs.txt), [`board-chip-measure.txt`](2026-11-03-sandbox-cli-edges/board-chip-measure.txt)). The list's chips read `P10000000000` (101 px wide), `P1000` and `P150`. Each sits inside its `<li>` row, and the document's scroll width equals the viewport at both widths, so nothing overflows sideways.

There is no upper bound: `1e10` is accepted and sorts above everything. Nothing breaks, and the bound PR #502 chose is 2^53. Whether a maximum is wanted is a product question, not a bug, so I did not add one.

## Findings

| # | Finding | Outcome |
|---|---|---|
| F1 | **`grain grep` over `src://` took 16 s (`src://pkg`) and ~50 s (`src://`) per request** on core's 6614-file tree, from a real sandbox ([`transcript-before-fix.txt`](2026-11-03-sandbox-cli-edges/transcript-before-fix.txt): the proxy's `grain_grep` lines are 16 s and 50 s apart). A one-symbol-per-package sweep would have taken half an hour. A CPU profile put 96% in `redact.Line`. `Server.Grep` ran every line of every file through the credential-shape regexps *before* trying the pattern. On the daemon it also rebuilt the redactor from the secrets directory for every file (3317 `NewRedactor` calls for one `src://pkg` grep). It reproduces without a daemon: `Server{SrcDir: <core>}.Grep(…"pkg"…)` took 15.4 s with a nil redactor. | **Fixed** (`7cacce12`). One redactor per request. Only printed lines (matches and context) are redacted. A raw match counts only if its redacted text still matches, so a pattern spelling a secret still finds nothing. The cost: a pattern matching only a `[redacted: …]` placeholder no longer finds a line this pass would have redacted. Now 0.30 s and 0.60 s. Pinned by `TestGrepRedactsOnlyWhatItPrintsAndStillHidesSecrets` (`pkg/namespace`): secret probes find nothing, context lines are redacted, and the redactor is built once over 41 files. |
| F2 | **Five commands exited 1, unnamed, for a missing required argument:** `grain notify` (`--subject is required: …`), `grain task propose` (`--title is required: …`), `grain pr resolve` (`name the thread: …`), `grain git revert` (`--reason is required: …`), `grain db query` (`db query: a SQL statement is required`). The 11-02 F2 fix made every positional argument a usage error, but these check flags or the joined positionals later in their own functions. Also, `flag -n needs a value` did not point at `--help` as every other usage error does. | **Fixed** (`17db7829`). All go through `sandboxArgs.missing`: exit 2, `grain <cmd>: missing …; run 'grain <cmd> --help' for usage`. The `needs a value` error names `--help` too. Pinned by new rows in `TestMissingArgumentIsAUsageError`. Two tests that pinned exit 1 (`pr resolve`, `git revert`) now expect 2. |
| F3 | **`grain grep … --max 0` reached the proxy** and came back `grain: invalid max parameter "0"` (exit 1). This is the same shape as 11-02's F3/F4 (`log -n 0`, `read --page 0`): the CLI's non-negative count check lets 0 through to a server that wants 1 or more. | **Fixed** (`17db7829`). `grain grep: --max "0" is not a match limit: it takes a positive integer`, exit 2, no request made. |
| F4 | **`grain create -priority 1e3` and `grain update -priority 1e3` were refused** (`invalid value "1e3" for flag -priority: parse error`, exit 2), though PR #502 made POST/PATCH accept `1e3`, `1000.0` and `1.5e2`. The sandbox's `grain task propose --priority 1e3` (strconv.Atoi) refused them too. That is one entry with two answers, the shape #502 set out to remove. | **Fixed** (`f20f99e6`). `model.ParseTaskPriority` is the one reading, and the API's `decodeTaskPriority`, a `-priority` flag type in `pkg/cli`, and `task propose` all use it. Pinned by `TestParseTaskPriority`, `TestPriorityFlagReadsTheAPIsSpellings` and the propose rows in `TestStage3SandboxCLICommands`. |

## The deployment memory note on `grain grep`

The note "`grain grep <re> src://pkg` caps at 2000 walked files (`[truncated: walked 2000 entries]`), silently skipping … `pkg/orchestrator`..`pkg/ui`. Narrow to a subdirectory … or pass `--max 10000`" is **stale**:

- Since PR #507, `src://` walks stop at 20000 entries. Every package from `pkg/orchestrator` to `pkg/ui` is found under `src://pkg` and `src://` (table above).
- A walk that *is* cut short and finds nothing exits 1, with a note on stderr, instead of exiting 0.
- `--max` never raised the walk cap. It is the match limit (`[truncated at match limit …]`), so "pass `--max 10000`" was never the remedy.
- The note's list of valid flags (`-C`, `--glob`, `-i`, `--max`) is still right.
- After F1 lands and deploys, a whole-tree `src://` grep takes about half a second rather than tens of seconds.

The memory note lives in grain/config and is not editable from here. It needs a change there, or the next memory pass, to drop the 2000-entry/`--max` advice.

## Reproduce

```
cd ui && npm ci && npm run build && cd ..
sudo mkdir -p /usr/local/share/grain && sudo chown "$USER" /usr/local/share/grain
GRAIN_SOURCE_SECRET_SCAN=skip scripts/export-source.sh "$PWD" /usr/local/share/grain/src
GRAIN_WALK_SANDBOX_CLI_EDGES=1 GRAIN_WALK_OUT=/tmp/walk go test ./tests/e2e/ -run TestSandboxCLIEdgesWalkthrough -v -count=1
```

It takes about 50 s on this branch, including the image build. The CPU profile for F1 came from `go test ./pkg/namespace -run <a Grep over that tree> -cpuprofile`. Remove `/usr/local/share/grain/src` afterwards: a daemon on the same host serves it as `src://`.

## Commits

- `7cacce12` fix(namespace): make grain grep over src:// fast by redacting only the lines it prints (F1)
- `f20f99e6` fix(cli): read grain create -priority 1e3 as 1000, as POST /api/tasks does (F4)
- `17db7829` fix(sandboxcli): exit 2 for every missing required argument, and refuse grep --max 0 locally (F2, F3)
- this report, its harness and the gated walkthrough test
