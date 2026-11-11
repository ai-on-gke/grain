# In-sandbox `grain` CLI specification

**Status:** draft for review. Normative.
**Part of:** [`cli.spec.md`](cli.spec.md). Read that first for dispatch, exit codes ([#CLI-10](cli.spec.md#CLI-10)), flag syntax and help ([#CLI-13](cli.spec.md#CLI-13)), output size ([#CLI-9](cli.spec.md#CLI-9)) and the coverage guard ([#CLI-14](cli.spec.md#CLI-14)).
**Code:** `pkg/sandboxcli` (`sandboxcli.go`, `actions.go`, `task.go`, `cap.go`, `help.go`). Served by `cmd/grain` and `cmd/grain-sandbox`. The server side is `pkg/gitproxy/cli.go` (`/cli/…`) and the namespace (`/fs/…`).

The tables between `BEGIN/END GENERATED FLAGS` markers are generated from `sandboxCommandFlags`, the list the CLI enforces. Every other flag is refused with status 2. Types:
- `bool` takes no value (`sandboxBoolFlags`);
- `count` must be a non-negative integer, checked before the proxy is asked (`sandboxCountFlags`), else `grain <cmd>: <flag> "<v>" is not a count: it takes a non-negative integer`;
- `string` takes one value.

Scope is enforced by the proxy, not by the CLI. A run without the grant a command needs gets the proxy's refusal (`pkg/mcp`'s `unknownToolMessage` wording), printed as `grain: <refusal>` with status 1. Below, **Needs** names the grant the proxy checks, where one is needed beyond the run's own credential.

---

## 1. Commands and proxy discovery

### 1.1 Proxy discovery {#CLI-80}

A process is "in a sandbox", and the commands below are answered, when `detectSandboxProxy` finds a proxy. The rules are applied in order:

1. When `GRAIN_SANDBOX_CLI` is `0` or `false` (any case), sandbox mode **MUST** be off.
2. When `GRAIN_SERVER` is non-empty, sandbox mode **MUST** be off: a CLI pointed at a daemon is the host CLI wherever it runs.
3. The base URL is `$GRAIN_SANDBOX_PROXY` with trailing `/` trimmed. The token is `$GRAIN_SANDBOX_TOKEN`.
4. The git credentials file is `$HOME/.git-credentials` when it exists, else `/home/debian/.git-credentials`. It is scanned line by line. `%3a` in the host part is read as `:`. Lines without user info are skipped.
   - With no base yet, the first line that is a *proxy credential* sets the base to `scheme://host:port`. A proxy credential is an `http`/`https` URL with user info and an explicit port, where either the port is `9728`, or the user is `sandbox` and the host is a sandbox-proxy host: `localhost`, a loopback, private, link-local or CGNAT (`100.64.0.0/10`) IP, a dotless name, or a name ending `.internal`, `.local` or `.svc`.
   - With a base already, only a line whose host matches the base counts.
   - A matching line's password becomes the token when `$GRAIN_SANDBOX_TOKEN` is empty.
5. With no base, sandbox mode is off and the host CLI runs.

Every request **MUST** carry the token as HTTP basic auth, user `x-access-token`. Help and error text **MUST NOT** print the token: the proxy URL is shown with user info removed.

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestDetectSandboxProxy`, `TestSandboxCLIOutsideASandboxIsNotHandled`; `pkg/sandboxcli/actions_test.go`: `TestSandboxActionsUseGitCredentialsWithoutProxyEnv`; `pkg/sandboxcli/flags_test.go`: `TestSandboxHelpLeavesOutTheProxyToken`.

### 1.2 Command set {#CLI-81}

The commands sandbox mode answers **MUST** be exactly the keys of `sandboxCommands`. The subcommands are:

| Command | Subcommands |
|---|---|
| `ci` | `list`, `show`, `log`, `test-history`, `artifact`, `wait`, `run-job`, `retrigger` |
| `pr` | `status`, `base-status`, `open`, `reply`, `resolve`, `review-comment` |
| `git` | `revert` |
| `handover` | `write` |
| `task` | `list`, `read`, `prompt`, `transcript`, `wait`, `message`, `propose` |
| `memory` | `list`, `read`, `write`, `doc-write` (also spelled `doc write`) |
| `release-notes` | `stats`, `publish` |
| `issue` | `list`, `read` |
| `db` | `query` |
| no subcommands | `help`, `ls`, `read`, `grep`, `merge-queue`, `status`, `notify`, `api`, `quarantine`, `metrics`, `health`, `log`, `playbooks`, `cap` |

Any other command, including `mcpserver`, `capability` and the agent hooks, runs as the host CLI does, even in a sandbox. A command with subcommands given none, or `-h`, prints its help. An unknown subcommand prints `grain <cmd>: unknown command "<sub>"` and the command's help, with status 2. `grain memory doc` not followed by `write` prints `grain memory doc: the command is 'grain memory doc write' (or doc-write)`.

**Pinned by:** `pkg/sandboxcli/flags_test.go`: `TestEverySandboxCommandNamesItsFlags`, `TestUnknownSubcommandGetsTheSandboxHelp`, `TestEverySubcommandIsInItsCommandsHelp`; `pkg/sandboxcli/clispec_test.go`: `TestSandboxCLISpecCoversEveryCommandAndFlag`.

### 1.3 Namespaces {#CLI-82}

`ls`, `read` and `grep` take a namespace URI `<scheme>://<path>`. The CLI **MUST** map it to `GET /fs/<scheme>/<path>`, each path segment URL-escaped. It **MUST** refuse a string with no scheme: `"<x>" is not a namespace URI (ci://, src://, task://, config://, memory://, deploy://)`, status 1. The schemes the namespace serves:

| Scheme | Holds | Needs |
|---|---|---|
| `ci://<owner>/<repo>/<run>/<job>/[attempts/<k>/]<step>.log`, `…/artifacts/<job>/<name>` | grain CI runs and GitHub Actions runs (`gha:` ids; one `job.log` per job) | `https://grain.dev/capabilities/grain#ci` (formerly `grain-ci`, retired in schema migration 96) for the task's own repo, `ci-read` for others |
| `src://…` | the daemon's own source; `src://core/…` for core under a layered build; `src://playbooks/…` | `…/grain#source` (formerly `self-debug`), or `…/config-map#config-map={"config_maps":["bootstrap"]}` (formerly `bootstrap-playbooks`, retired in schema migration 118) for playbooks |
| `task://<id>/record.md`, `prompt.md`, `transcript.md`, `attempts/<n>/…` | task records | the run's own task, or `tasks:read` (in `api:self-debug`) for others |
| `config://…` | the deployment's state repository (grain/config) | as the namespace grants |
| `memory://<key>` | memory documents | `memory` |
| `deploy://grain.log[/<source>]`, `health`, `metrics[/<section>]`, `merge-queue`, `pipeline-changes` | deployment state | `…/grain#journal` (formerly `grain-log`, retired in schema migration 80), `…/grain#health` (with `api:deployment:read`, formerly `deployment-health`, retired in schema migration 81), `api:deployment:read` (in `api:self-debug`) for metrics and pipeline changes |

The schemes and their grants are the namespace's to define ([`grain-namespace.design.md`](../../design/grain-namespace.design.md)). This CLI only forwards.

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIAgainstTheProxy`, `TestSandboxCLIReadsOtherTasksCI`; grants *untested here* (pinned in `pkg/gitproxy`).

---

## 2. Namespace and files

### 2.1 `grain ls` {#CLI-83}

`grain ls <uri> [--glob <pat>]` sends `GET /fs/…?list[&glob=]` and streams the listing. A glob containing `/` or `**` walks the subtree, and the namespace ends a cut-short walk with a `[truncated: …]` line.

<!-- BEGIN GENERATED FLAGS: grain ls -->
Usage: `grain ls <uri> [--glob <pat>]`

| Flag | Type |
|---|---|
| `--glob` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIAgainstTheProxy`.

### 2.2 `grain read` {#CLI-84}

`grain read <uri>` sends `GET /fs/…` with `tail`, `offset`, `limit`, `cursor`, `since` and `page` as the query, and streams the body.
- `--lines N` / `-n N` prints only the last N lines.
- `--cursor`/`--since`/`--page` are valid only for `deploy://pipeline-changes[.md]`. On any other URI: `grain read: <flag> pages deploy://pipeline-changes, not <uri>` and the help, status 2.
- `--page 0`: `grain read: --page "0" is not a page size: it takes a positive integer`, status 2.

<!-- BEGIN GENERATED FLAGS: grain read -->
Usage: `grain read <uri> [--tail N] [--offset N --limit N] [--lines N]`

| Flag | Type |
|---|---|
| `--tail` | count (non-negative integer) |
| `--offset` | count (non-negative integer) |
| `--limit` | count (non-negative integer) |
| `--lines` | count (non-negative integer) |
| `-n` | count (non-negative integer) |
| `--cursor` | string |
| `--since` | string |
| `--page` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/flags_test.go`: `TestReadTakesBytesAndLines`, `TestReadPagesPipelineChanges`.

### 2.3 `grain grep` {#CLI-85}

`grain grep <pattern> <uri>` sends `GET /fs/…?grep=<re>&n=1[&i=1][&context=N][&glob=][&max=]`. The pattern is RE2.
- `-C` must be a non-negative integer: `grain grep: -C "<v>" is not a count: it takes a non-negative integer`.
- `--max 0`: `grain grep: --max "0" is not a match limit: it takes a positive integer`. Both are status 2.
- When the answer has `X-Grain-Walk-Truncated: true` and `X-Grain-Matches: 0`, the CLI **MUST** print `grain grep: no match in the part of <uri> searched before the walk cap, and the rest was not searched: narrow the path or pass --glob` and exit 1.

<!-- BEGIN GENERATED FLAGS: grain grep -->
Usage: `grain grep <pattern> <uri> [-i] [-C N] [--glob <pat>] [--max N]`

| Flag | Type |
|---|---|
| `-i` | bool |
| `-C` | string |
| `--glob` | string |
| `--max` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestGrepExitsNonZeroWhenATruncatedWalkFoundNothing`.

---

## 3. CI and pull request

These are scoped by the proxy to the run's task repository and branch. **Needs** `…/grain#ci` (own repository) or `ci-read` (others). `ci retrigger` and `ci run-job` are grain-CI-only.

### 3.1 `grain ci list` {#CLI-86}

`GET /cli/ci/runs` with every given flag copied into the query. Output is the proxy's text table: a header line, then one run per line, run id first. `--provider` is `grain` or `github-actions`. `--since` takes a span or an RFC 3339 time.

`--format github` answers JSON instead, in GitHub's own shape for `GET /repos/{owner}/{repo}/actions/runs` (`{"total_count", "workflow_runs": [...]}`, `--limit` up to 500), and `--format github-jobs` every attempt of every job of each listed run (`GET .../actions/runs/{id}/jobs?filter=all`, `{"total_count", "jobs": [...]}`, at most 50 runs). Both imply `--provider github-actions` and refuse `--provider grain`. A run grain holds only from its record of the run's jobs (no workflow-run status and conclusion of the forge's) is left out, never made up, and a `grain_note` field says how many; a run whose jobs the forge will not give fails the whole answer. This is what `scripts/candidate-gate.sh` reads as `--runs-json` from a task with no GitHub token (`.grain/templates/release-train.yml`).

<!-- BEGIN GENERATED FLAGS: grain ci list -->
Usage: `grain ci list [--task <id> | --ref <branch>] [--conclusion failure,errored] [--job <j>] [--workflow <file>] [--since 24h] [--limit N] [--repo owner/name] [--provider grain|github-actions] [--format github|github-jobs]`

| Flag | Type |
|---|---|
| `--ref` | string |
| `--conclusion` | string |
| `--job` | string |
| `--workflow` | string |
| `--since` | string |
| `--limit` | count (non-negative integer) |
| `--repo` | string |
| `--task` | string |
| `--provider` | string |
| `--format` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_ci_test.go`: `TestSandboxCLIReadsGitHubActionsRuns`; `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIReadsOtherTasksCI`.

### 3.2 `grain ci show` {#CLI-87}

`grain ci show <run-id>` sends `GET /cli/ci/show?id=<run-id>`.

<!-- BEGIN GENERATED FLAGS: grain ci show -->
Usage: `grain ci show <run-id>`

_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_ci_test.go`: `TestSandboxCLIReadsGitHubActionsRuns`.

### 3.2a `grain ci checks`

`grain ci checks <sha>` sends `GET /cli/ci/checks?sha=<sha>` with `--repo`, `--name` and `--format` copied into the query: every check run on the commit (every attempt of each, GitHub's `filter=all`), or every attempt of the check run `--name` names, read through grain's own forge connection. Text is one check run per line: its conclusion (or status), name, the App that posted it (`(grain)` beside grain's own) and its page. `--format github` answers GitHub's `GET /repos/{owner}/{repo}/commits/{sha}/check-runs` shape (`{"total_count", "check_runs": [...]}`), plus `grain_app`, the slug of the App grain's forge connection posts check runs as ("" when it cannot tell): `scripts/candidate-gate.sh check-qualification` reads the first as `--checks-json` and trusts only check runs whose `app.slug` is the second (its `CHECK_QUALIFICATION_APP`). Needs the same CI read scope as `grain ci list`.

<!-- BEGIN GENERATED FLAGS: grain ci checks -->
Usage: `grain ci checks <sha> [--repo owner/name] [--name <check>] [--format github]`

| Flag | Type |
|---|---|
| `--repo` | string |
| `--name` | string |
| `--format` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/gitproxy/cli_github_shape_test.go`: `TestCLICIChecksAnswersGitHubsShape`, `TestCLICIListFormatGitHub`.

### 3.3 `grain ci log` {#CLI-88}

`grain ci log [<run-id>]` finds the run first:
1. the given id, else the newest run on `--task`'s branch (`GET /cli/ci/runs?task=<id>&limit=1`), else the newest on this task's branch (`ref=@branch`);
2. the repository from `--repo`, else `GET /cli/ci/run?id=`, else the run's context (`GET /cli/context`).

It then lists the run's jobs, or with `--job` the job's steps, or with `--step` reads `ci://<repo>/<run>/<job>/[attempts/<k>/]<step>.log` (`.log` is added when missing). For `gha:` runs it reads `<job>/job.log`.

Errors:
- no run on the branch: `<this task's branch|task N's branch> has no grain CI run yet`;
- a task with no repository and no run id: `this task has no repository or branch, so it has no latest grain CI run: name a run id, with --repo <owner/repo> if you like (`grain ci list --repo <owner/repo>` lists them), or pass --task <id>`.

<!-- BEGIN GENERATED FLAGS: grain ci log -->
Usage: `grain ci log [<run-id> | --task <id>] [--repo owner/name] [--job <j>] [--step <s>] [--attempt K] [--tail N | --lines N]`

| Flag | Type |
|---|---|
| `--task` | string |
| `--repo` | string |
| `--job` | string |
| `--step` | string |
| `--attempt` | count (non-negative integer) |
| `--tail` | count (non-negative integer) |
| `--offset` | count (non-negative integer) |
| `--limit` | count (non-negative integer) |
| `--lines` | count (non-negative integer) |
| `-n` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIReadsOtherTasksCI`, `TestSandboxCLIOnANoRepoTask`.

### 3.4 `grain ci test-history` {#CLI-89}

The external `grain ci test-history` is [host #CLI-62](host.spec.md#CLI-62), rendering the same report.

`grain ci test-history <test-name>` sends `GET /cli/ci/test-history?test=<name>[&limit][&repo][&provider]`.

The runs are grain CI's and, for a repository whose CI is GitHub Actions (`ci_mode: github`), its Actions runs (`ci.ExternalRuns`), with the log tails grain recorded of their failed jobs. With no `--provider`, such a repository's history is its Actions runs alone: its grain CI runs are a retired mode's, and the report says how many were left out (`--provider grain` reads them). Grain keeps no log of an Actions job that passed, so an Actions run counts the test's failures and never a pass (`ci.TestHistoryReport.PassesUncounted`); every report names each provider's runs and the window they span (`Sources`).

<!-- BEGIN GENERATED FLAGS: grain ci test-history -->
Usage: `grain ci test-history <test-name> [--limit N] [--repo owner/name] [--provider grain|github-actions]`

| Flag | Type |
|---|---|
| `--limit` | count (non-negative integer) |
| `--repo` | string |
| `--provider` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIAgainstTheProxy`.

### 3.5 `grain ci artifact` {#CLI-90}

`grain ci artifact <job> <name>` downloads `ci://<repo>/<run>/artifacts/<job>/<name>`. The run and repo are found as for `ci log`; `--run` names the run. With `-o <path>` the file is written there (and removed on failure); without `-o`, or with `-o -`, it goes to stdout.

<!-- BEGIN GENERATED FLAGS: grain ci artifact -->
Usage: `grain ci artifact <job> <name> [-o <dest>] [--run <run-id> | --task <id>] [--repo owner/name]`

| Flag | Type |
|---|---|
| `-o` | string |
| `--run` | string |
| `--task` | string |
| `--repo` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestSandboxAPIInputModesAndCIArtifactDownload`.

### 3.6 `grain ci wait` and `grain ci run-job` {#CLI-91}

The external `grain ci wait` is [host #CLI-62](host.spec.md#CLI-62): it names its run, waits up to 30m with no wait credit, and answers the same JSON shape.

`grain ci wait` sends `POST /cli/ci/wait` with `{"timeout_seconds", "jobs"?, "resume_run_id"?}`. `grain ci run-job <job>` sends `POST /cli/ci/run-job` with `{"job", "timeout_seconds", "resume_run_id"?}`.

`--timeout` is a Go duration or a number of seconds. It is capped at `540s`, and at `$GRAIN_COMMAND_TIMEOUT_MS` − 30s when that is set. Errors: `--timeout "<v>" is not a duration (9m, 300s) or a number of seconds` and `--timeout must be positive`.

The answer is printed as one of:
- `running`: `CI run <id> is still running after <d> (jobs running: …; passed: …[; wait credit used: <u> / <cap>]).`, then the `grain ci wait --resume <id>` and `resume_later` lines. Exit 0.
- `passed`: `grain CI passed on <commit12> (run <id>, waited <d>…): <jobs>`. A selective run adds a line saying it never satisfies the full-suite gate. Exit 0.
- anything else: `grain CI failed on <commit12> (run <id>, <conclusion>, waited <d>…).` followed by the error, the cancel reason, a superseded-by note, and the `Failing:` and `Passed:` lists. Exit **1**.
- If the proxy sends `report`, that text is printed instead. The exit is 1 when `status` is `failed`.

Time blocked is credited back to the run's deadline by the proxy.

<!-- BEGIN GENERATED FLAGS: grain ci wait -->
Usage: `grain ci wait [--jobs j1,j2] [--timeout 9m] [--resume <run-id>]`

| Flag | Type |
|---|---|
| `--jobs` | string |
| `--timeout` | string |
| `--resume` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain ci run-job -->
Usage: `grain ci run-job <job> [--timeout 9m] [--resume <run-id>]`

| Flag | Type |
|---|---|
| `--timeout` | string |
| `--resume` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestSandboxCIWaitReportsEachVerdict`, `TestParseWaitTimeout`.

### 3.7 `grain ci retrigger` {#CLI-92}

The external `grain ci retrigger <run-id>` is [host #CLI-62](host.spec.md#CLI-62).

`POST /cli/ci/retrigger` with `{"jobs"?, "all"?, "commit"?, "workflow"?}`. Reruns failed or named jobs fresh and uncached.

<!-- BEGIN GENERATED FLAGS: grain ci retrigger -->
Usage: `grain ci retrigger [--jobs j1,j2 | --all] [--commit <sha>] [--workflow <file>]`

| Flag | Type |
|---|---|
| `--jobs` | string |
| `--all` | bool |
| `--commit` | string |
| `--workflow` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestStage3SandboxCLICommands`.

### 3.8 `grain pr …` {#CLI-94}

| Command | Request |
|---|---|
| `pr status` | `GET /cli/pr/status[?task&ref&repo]` |
| `pr base-status` | `GET /cli/pr/base-status` |
| `pr open` | `POST /cli/pr/open {}`: opens or updates the pull request from the branch's commit messages |
| `pr reply <body \| --file f>` | `POST /cli/pr/reply {"body", "comment_id"?}` |
| `pr resolve` | `POST /cli/pr/resolve {"thread_id"?, "comment_id"?}`. Missing both: `grain pr resolve: missing --thread-id <id> or --comment-id <id>: name the thread`, status 2 |
| `pr threads [--unresolved]` | `GET /cli/pr/threads[?unresolved=1]`: every review thread on the task's own pull request -- thread ID, first comment ID, author, path:line, resolved/outdated, body and replies -- or only the unresolved ones (outdated included). Needs no CI grant |
| `pr review-comment <body \| --file f>` | `POST /cli/pr/review-comment {"body", "path"?, "line"?}`. Given one of `--path`/`--line` without the other: `give both --path and --line to attach a comment to a line, or neither for a general remark` |

`--file -` reads stdin. `--comment-id` and `--line` must be positive: `--<flag> must be a positive integer`. A missing body gets `missing <body> (or --file)`, status 2.

<!-- BEGIN GENERATED FLAGS: grain pr status -->
Usage: `grain pr status [--task <id> | --ref <branch> [--repo owner/name]]`

| Flag | Type |
|---|---|
| `--task` | string |
| `--ref` | string |
| `--repo` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain pr base-status -->
Usage: `grain pr base-status`

_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain pr open -->
Usage: `grain pr open`

_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain pr reply -->
Usage: `grain pr reply <body|--file f> [--comment-id <id>]`

| Flag | Type |
|---|---|
| `--file` | string |
| `--comment-id` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain pr resolve -->
Usage: `grain pr resolve (--thread-id <id> | --comment-id <id>)`

| Flag | Type |
|---|---|
| `--thread-id` | string |
| `--comment-id` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain pr threads -->
Usage: `grain pr threads [--unresolved]`

| Flag | Type |
|---|---|
| `--unresolved` | bool |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain pr review-comment -->
Usage: `grain pr review-comment <body|--file f> [--path <file> --line <N>]`

| Flag | Type |
|---|---|
| `--file` | string |
| `--path` | string |
| `--line` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestStage3SandboxCLICommands`; `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIReadsOtherTasksCI`.

### 3.9 `grain merge-queue` {#CLI-95}

The external `grain merge-queue` is [host #CLI-107](host.spec.md#CLI-107), reading the same file.

Reads `deploy://merge-queue`.

<!-- BEGIN GENERATED FLAGS: grain merge-queue -->
Usage: `grain merge-queue`

_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested*.

---

## 4. Git, task and collaboration

### 4.1 `grain git revert` {#CLI-96}

`grain git revert (--task <id> | --commit <sha>) --reason <why> [-C dir]` reverts in the local working tree (default `.`) with `mcp.GitTaskReverter`, the same code the `revert_task` tool runs. Nothing is pushed.
- With `--task`, it asks `GET /cli/tasks/<id>/revert-info` and looks for the newest commit naming the task's branch (`git log --grep`).
- On success it prints `Reverted <summary> (<sha>) as <new sha>.` and `Nothing was pushed: check the tree, then push the branch as usual.`
- Errors: `missing --reason <why>: say why this is being reverted` (status 2); `name exactly one of --task <id> or --commit <sha>`.

<!-- BEGIN GENERATED FLAGS: grain git revert -->
Usage: `grain git revert (--task <id> | --commit <sha>) --reason <why> [-C <dir>]`

| Flag | Type |
|---|---|
| `--task` | string |
| `--commit` | string |
| `--reason` | string |
| `-C` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestSandboxGitRevertRevertsATaskMergeLocally`.

### 4.2 `grain status` and `grain handover write` {#CLI-97}

- `grain status <note…> | --file f` sends `POST /cli/status {"status"}`, which sets the run's live activity line.
- `grain handover write (<text> | --file f | -)` sends `POST /cli/handover {"handover"}`, which replaces the task's standing handover.

<!-- BEGIN GENERATED FLAGS: grain status -->
Usage: `grain status <one-line-note>`

| Flag | Type |
|---|---|
| `--file` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain handover write -->
Usage: `grain handover write (<text> | --file <f>)`

| Flag | Type |
|---|---|
| `--file` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestStage3SandboxCLICommands`.

### 4.3 `grain notify` {#CLI-98}

`grain notify --subject <key> --title <t> [--body b | --file f | <body…>]` sends `POST /cli/notify` with `{subject, title, body, severity, task, clears_when}`.
- It prints `<Raised|Updated> <severity> notification "<subject>" (#<id>, count <n>).`, or the raw JSON with `--json`.
- Missing `--subject` or `--title` is a status-2 usage error naming the flag.
- A bad `--clears-when` gets `--clears-when must be valid JSON: …`.

<!-- BEGIN GENERATED FLAGS: grain notify -->
Usage: `grain notify --subject <key> --title <one-line> [--body <md> | --file <f>] [--severity info|warning|critical] [--task <id>] [--clears-when <json>]`

| Flag | Type |
|---|---|
| `--subject` | string |
| `--title` | string |
| `--body` | string |
| `--file` | string |
| `--severity` | string |
| `--task` | string |
| `--clears-when` | string |
| `--json` | bool |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestStage3SandboxCLICommands`.

### 4.4 `grain task …` {#CLI-93}

| Command | Behaviour |
|---|---|
| `task list` | `GET /cli/tasks` with the filters. Takes no positionals (`task list: unexpected argument "<x>": …`). `--limit` must be positive (`task list: --limit "<v>" is not a positive number`). **Needs** `tasks:read` (or the `api:self-debug` preset) for tasks other than the run's own |
| `task read <id>` | reads `task://<id>/record.md` |
| `task prompt <id>` / `task transcript <id>` | reads `task://<id>/[attempts/<n>/]prompt.md` / `transcript.md`. `transcript` reports the file's size on stderr |
| `task wait <id>…` | `POST /cli/tasks/wait {"ids", "until", "timeout_seconds"}`. Prints `Task wait (<status>, waited <d>…):` and the summary. When not settled, it adds how to keep waiting and how to park. `--park` or `--resume-at` is refused: `parking a task ends the run and is a model-only control operation: call the wait_for_task (park: true) or resume_later MCP tool instead` |
| `task message <id> <body…> \| --body \| --file` | `POST /cli/tasks/<id>/notes {"body"}` |
| `task message <id> <body…> --relay` | `POST /api/tasks/<id>/comments {"body", "relay": true}`: a console session relays the answer of the person it acts for, written as "<person> via <session>" and counted as their reply |
| `task propose` | `POST /cli/proposals`. Always files **unapproved** with `auto_merge=false`. Refuses `--supersedes`, `--auto-merge` and `--hold-for-approval=false` with messages pointing at the `propose_task` tool. Requires `--title` (status 2) and `--body`/`--file` (`--body (or --file) is required: run 'grain help propose' for usage`). `--custom-field` must be `key=value`; `--priority` must be a positive integer; `--not-before` must be `YYYY-MM-DD` or RFC 3339 |

<!-- BEGIN GENERATED FLAGS: grain task list -->
Usage: `grain task list [--state <s>[,<s>...]] [--repo <owner/name>] [--search <title text>]`

| Flag | Type |
|---|---|
| `--state` | string |
| `--repo` | string |
| `--limit` | count (non-negative integer) |
| `--search` | string |
| `--changed-since` | string |
| `--stalled` | bool |
| `--kind` | string |
| `--json` | bool |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain task read -->
Usage: `grain task read <id>`

_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain task prompt -->
Usage: `grain task prompt <id> [--attempt N] [--tail N] [--offset N --limit N] [--lines N]`

| Flag | Type |
|---|---|
| `--attempt` | count (non-negative integer) |
| `--tail` | count (non-negative integer) |
| `--offset` | count (non-negative integer) |
| `--limit` | count (non-negative integer) |
| `--lines` | count (non-negative integer) |
| `-n` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain task transcript -->
Usage: `grain task transcript <id> [--attempt N] [--tail N] [--offset N --limit N] [--lines N]`

| Flag | Type |
|---|---|
| `--attempt` | count (non-negative integer) |
| `--tail` | count (non-negative integer) |
| `--offset` | count (non-negative integer) |
| `--limit` | count (non-negative integer) |
| `--lines` | count (non-negative integer) |
| `-n` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain task wait -->
Usage: `grain task wait <id>... [--until closed|merged] [--timeout 9m]`

| Flag | Type |
|---|---|
| `--until` | string |
| `--timeout` | string |
| `--json` | bool |
| `--park` | bool |
| `--resume-at` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain task message -->
Usage: `grain task message <id> (<body> | --body <b> | --file <f>) [--relay]`

| Flag | Type |
|---|---|
| `--body` | string |
| `--file` | string |
| `--relay` | bool |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain task propose -->
Usage: `grain task propose --title <t> (--body <b> | --file <f>) [flags]`

| Flag | Type |
|---|---|
| `--title` | string |
| `--body` | string |
| `--file` | string |
| `--id` | string |
| `--repo` | string |
| `--branch` | string |
| `--no-repo` | bool |
| `--depends-on` | string |
| `--capability` | string |
| `--fixes-base-branch` | bool |
| `--no-port` | bool |
| `--keep-running` | bool |
| `--origin` | string |
| `--read` | string |
| `--design` | string |
| `--custom-field` | string |
| `--not-before` | string |
| `--priority` | string |
| `--json` | bool |
| `--supersedes` | string |
| `--auto-merge` | bool |
| `--hold-for-approval` | bool |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/task_list_test.go`: `TestSandboxTaskListFiltersAndBounds`; `pkg/sandboxcli/actions_test.go`: `TestStage3SandboxCLICommands`; `pkg/sandboxcli/flags_test.go`: `TestTaskTranscriptSaysHowBigItIs`.

---

## 5. Deployment API, memory, release notes, quarantine

### 5.1 `grain api` {#CLI-99}

- `grain api routes [--scope s] [--json]` sends `GET /cli/api-routes` and prints one `METHOD PATH (scope)[ [--reason required]]` line per granted route, or `No agent API routes are granted to this attempt.`
- `grain api <METHOD> <path>` sends that method to the proxy at `<path>`, which **MUST** start with `/api/` (`agent API path must start with /api/ (got "<p>")`). The body comes from the first of `--body`, `--file` (`-` for stdin), or repeated `--field key=value` (a value that starts with `{` or `[`, or is `true`, `false` or `null`, is parsed as JSON; every other value, numbers included, is sent as a string; `--field "<kv>" must be key=value`).
- `--reason` is sent as `X-Grain-Acting-Reason` and `X-Grain-Reason`.

**Needs** an `api:<scope>` grant covering the route.

<!-- BEGIN GENERATED FLAGS: grain api -->
Usage: `grain api routes [--scope <s>]`

| Flag | Type |
|---|---|
| `--scope` | string |
| `--json` | bool |
| `--body` | string |
| `--file` | string |
| `--field` | string |
| `--reason` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestSandboxAPIInputModesAndCIArtifactDownload`.

### 5.2 `grain memory …` {#CLI-100}

- `memory list` lists `memory://`.
- `memory read <key>` reads `memory://<key>`.
- `memory write [<owner/name>] <text> | --file f [--repo r] [--reason]` sends `PUT /api/memory {"repo", "text"}`, which replaces the whole memory text. A leading `<key>` is refused: `grain memory write takes no <key>: …`.
- `memory doc-write <name> …` sends `PUT /api/memory/documents`.

**Needs** `api:memory` (formerly the `memory` capability, retired in schema migration 71).

<!-- BEGIN GENERATED FLAGS: grain memory list -->
Usage: `grain memory list`

_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain memory read -->
Usage: `grain memory read <path>`

_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain memory write -->
Usage: `grain memory write [--repo owner/name] (<text> | --file <f>) [--reason <why>]`

| Flag | Type |
|---|---|
| `--repo` | string |
| `--file` | string |
| `--reason` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain memory doc-write -->
Usage: `grain memory doc-write <path> (<content> | --file <f>) [--summary <s>] [--repo <owner/name>] [--reason <why>]`

| Flag | Type |
|---|---|
| `--repo` | string |
| `--file` | string |
| `--summary` | string |
| `--reason` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestStage3SandboxCLICommands`.

### 5.3 `grain release-notes …` {#CLI-101}

- `release-notes stats [<period> | --period p]` sends `GET /api/release-notes/stats`.
- `release-notes publish --version v | --period p [--title] (--file f | <text>) [--reason]` sends `PUT /api/release-notes`.

<!-- BEGIN GENERATED FLAGS: grain release-notes stats -->
Usage: `grain release-notes stats [<period> | --period <p>]`

| Flag | Type |
|---|---|
| `--period` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain release-notes publish -->
Usage: `grain release-notes publish --version <v> (<markdown> | --file <f>) [--period <p>] [--title <t>] [--reason <why>]`

| Flag | Type |
|---|---|
| `--version` | string |
| `--period` | string |
| `--title` | string |
| `--file` | string |
| `--reason` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** *untested*.

### 5.4 `grain quarantine` {#CLI-102}

`grain quarantine <test-name> --reason <why> [--ttl d] [--no-follow-up] [-C dir]` sends `POST /cli/quarantine`. It reports whether its follow-up task was filed. Errors: `missing <test-name>` (status 2); `--reason is required: explain why the test is being quarantined`.

<!-- BEGIN GENERATED FLAGS: grain quarantine -->
Usage: `grain quarantine <package> <test-name> --reason <why> [--ttl 30d] [--no-follow-up] [-C <dir>]`

| Flag | Type |
|---|---|
| `--reason` | string |
| `--ttl` | string |
| `--no-follow-up` | bool |
| `-C` | string |
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/actions_test.go`: `TestQuarantineSaysWhenItsFollowUpWasNotFiled`.

---

## 6. Deployment observation

### 6.1 `grain metrics`, `grain health`, `grain log`, `grain playbooks` {#CLI-103}

- `grain metrics [<section>]` reads `deploy://metrics[/<section>]` with `window`, `since` and `until`. `--repo` and `--author` may be repeated. **Needs** `api:deployment:read` (or the `api:self-debug` preset).
- `grain health` reads `deploy://health`. **Needs** `…/grain#health` (formerly `deployment-health`).
- `grain log [<source> | --source s]` reads `deploy://grain.log[/<source>]`. `--since`/`--until` spans are resolved to RFC 3339 instants relative to now. `--priority`/`--min-priority` become `min_priority`, and `--max-lines`/`--lines`/`-n` become `max_lines`. A zero count is refused: `grain log: <flag> "0" is not a count of entries: it takes a positive integer`. **Needs** `…/grain#journal` (formerly `grain-log`).
- `grain playbooks` prints one playbook name per line (`src://playbooks`, with `.md` dropped). `grain playbooks <name>` reads it. **Needs** `…/config-map#config-map={"config_maps":["bootstrap"]}` (formerly `bootstrap-playbooks`).

<!-- BEGIN GENERATED FLAGS: grain metrics -->
Usage: `grain metrics [<section>] [--window 7d | --since <date> [--until <date>]] [--repo owner/name]... [--author <a>]...`

| Flag | Type |
|---|---|
| `--window` | string |
| `--since` | string |
| `--until` | string |
| `--repo` | string |
| `--author` | string |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain health -->
Usage: `grain health`

_No flags._
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain log -->
Usage: `grain log [<source> | --source daemon|dmesg] [--unit <unit>] [--since <rfc3339>] [--until <rfc3339>] [--priority err|warning|...] [--contains <text>] [--lines N] [--tail N]`

| Flag | Type |
|---|---|
| `--source` | string |
| `--unit` | string |
| `--since` | string |
| `--until` | string |
| `--priority` | string |
| `--min-priority` | string |
| `--contains` | string |
| `--max-lines` | count (non-negative integer) |
| `--lines` | count (non-negative integer) |
| `-n` | count (non-negative integer) |
| `--tail` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain playbooks -->
Usage: `grain playbooks [<name>]`

_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxLogAndMetricsFlagsBecomeTheirQuery`, `TestSandboxLogAndMetricsRefuseUnknownFlags`.

### 6.2 `grain issue …` and `grain db query` {#CLI-104}

- `issue list` sends `GET /cli/issues[?repo&label&limit]`.
- `issue read <number>` sends `GET /cli/issues/<n>[?repo&comments]`.
- `db query <sql…>` sends `GET /cli/db/query?sql=`. Missing SQL gets `missing <sql>`, status 2.

<!-- BEGIN GENERATED FLAGS: grain issue list -->
Usage: `grain issue list [--repo owner/name] [--label <l>] [--limit N]`

| Flag | Type |
|---|---|
| `--repo` | string |
| `--label` | string |
| `--limit` | count (non-negative integer) |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain issue read -->
Usage: `grain issue read <number> [--repo owner/name] [--comments]`

| Flag | Type |
|---|---|
| `--repo` | string |
| `--comments` | bool |
<!-- END GENERATED FLAGS -->

<!-- BEGIN GENERATED FLAGS: grain db query -->
Usage: `grain db query <sql>`

_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/sandboxcli_test.go`: `TestSandboxCLIIssuesAndDBAgainstTheProxy`.

---

## 7. External capabilities and help

### 7.1 `grain cap` {#CLI-105}

- `grain cap list [--json]` lists the sandbox-callable tools of the external capability servers granted to the run.
- `grain cap <server> [--help]` describes one server.
- `grain cap <server> <tool> --help` prints flags generated from the tool's input schema.
- `grain cap <server> <tool> [--<prop> v … | --json '<args>']` calls the tool, converting each flag by its schema type.

Model-only tools are refused by the proxy. The per-tool flags are not in `sandboxCommandFlags` and not covered by the guard ([#CLI-14](cli.spec.md#CLI-14)).

<!-- BEGIN GENERATED FLAGS: grain cap -->
Usage: `grain cap list [--json]`

_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/cap_test.go`: `TestGrainCapListHelpAndCall`, `TestCapArgumentsConvertsBySchema`.

### 7.2 `grain help` {#CLI-106}

`grain help`, `-h` and `--help` print `sandboxHelpText` to stdout with the redacted proxy URL. `grain help <cmd> [<sub>]` prints that command's help, or `no help for "<cmd>": run 'grain help'` (status 1).

<!-- BEGIN GENERATED FLAGS: grain help -->
_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/flags_test.go`: `TestSandboxHelpSaysBytesAndHasLiveExamples`, `TestSandboxHelpLeavesOutTheProxyToken`.

### 7.3 `grain screenshot` {#CLI-109}

`grain screenshot --url <app-url> [flags]` screenshots a web app the run is working on -- a dev server it started in the sandbox, a preview deployment -- with Playwright's Chromium, at each of `--widths` (desktop 1440×900, tablet 820×1180, phone 390×844) and `--themes` (light, dark, via `prefers-color-scheme` and optionally `localStorage[--theme-key]`), into `--out` (default `screenshots/`) as `<page>-<width>-<theme>.png`, then writes `contact-sheet.png` with every shot (and any `--reference` design images first). It talks to no grain service: the sandbox CLI writes the embedded `pkg/sandboxcli/screenshot.mjs` to a temporary directory and runs it with `node`, so its flags are the script's own and are not in `sandboxCommandFlags`.

- Playwright is resolved from the working directory's `node_modules`, then `~/.cache/grain/screenshot/node_modules`, then `npm root -g`. With none, the script exits 3 and the CLI installs `playwright-core@$GRAIN_PLAYWRIGHT_VERSION` (default the images' `PLAYWRIGHT_VERSION`) into the cache with `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`, and runs it once more.
- An uncaught page error, or an HTTP status of 400 or more for a page, fails the run (status 1) unless `--allow-errors`.
- The contact sheet is what a run attaches to its closing note by `path`; grain previews image attachments inline in the task's conversation and publishes them into the pull request's description (`docs/ui-screenshots.md`).

<!-- BEGIN GENERATED FLAGS: grain screenshot -->
Usage: `grain screenshot --url <app-url> [--pages /,/settings] [flags]`

_No flags._
<!-- END GENERATED FLAGS -->

**Pinned by:** `pkg/sandboxcli/screenshot_test.go`: `TestGrainScreenshotInstallsPlaywrightOnceAndPassesFlagsThrough`, `TestGrainScreenshotHelp`.
