# Sandbox namespace and the in-sandbox grain CLI

- **Plan:** `docs/test-plans/2026-10-28.md` §4 (retired `bwsalmon/grain`), restated in [Task #1717](/tasks/1717)
- **Date:** 2026-10-29, 03:30–06:10 UTC
- **Task:** `grain/049f1e09/task-1717` on `ai-on-gke/grain`, base `main` at `605a27d` (origin: [Task #1549](/tasks/1549))
- **Environment:**
  - Debian 12 sandbox (`kubernetes-granule`), kernel `6.1.0-54-amd64`, `linux/amd64`
  - Go `go1.26.10`
  - `grain` built from this branch (`go build -o /tmp/grain ./cmd/grain`)
  - `grain demo -github-sim -fake-ci -git-proxy -secrets-store -merge-queue-states -open=false -addr 127.0.0.1:7809 -access-code-file /tmp/wt-demo/code`
  - Dispatched sandbox runs created via `POST /api/e2e/git-proxy/runs` with isolated `$HOME/.git-credentials` (`git credential-store` format, including percent-encoded `%3a` port delimiter) and a shell script agent invoking every in-sandbox `grain` subcommand over the scoped proxy

## Outcome

| Step | Result |
|---|---|
| 1. `grain demo -github-sim -fake-ci` with sandbox proxy; shell script agent running every in-sandbox `grain` subcommand and saving output | **Pass after fixes (F1–F6).** All subcommands (`ls\|read\|grep`, `ci list\|log\|artifact\|test-history\|wait\|run-job\|retrigger`, `pr status\|base-status\|open\|reply\|resolve\|review-comment`, `git revert`, `merge-queue`, `task list\|read\|prompt\|transcript`, `metrics\|health\|log\|playbooks`, `issue list\|read`, `db query`) execute cleanly over the sandbox proxy and are narrated and counted in `task_run_tool`. |
| 2. Grant matrix: each scheme with and without its gating grant → 403 `not in scope` / capability refusal where ungranted; bare `ci-read` opens only the task's own repo | **Pass after fix (F2).** Every scheme and `/cli/` route refuses ungranted callers with HTTP 403 naming the missing capability (`self-debug`, `bootstrap-playbooks`, `memory`, `deployment-health`, `grain-log`, `read-issues`, `db-read`). A task with repo-scoped CI read (`grain-ci` / `api:ci-read@acme/widgets`) lists `ci://acme/widgets` (200) and is refused on `ci://other/repo` (403 `outside this sandbox's CI read scope`); a bare `ci-read` grant without `api:` prefix opens no repo. |
| 3. Seed a CI log with a fake token: redacted by `read`, `grep`, `grain_copy`; never split mid-line | **Pass after fix (F1).** Both built-in token patterns (`ghp_...` → `[redacted: github token]`) and runtime-added secrets (`PUT /api/secrets/custom-api-token/token` → `[redacted: secret custom-api-token/token]`) are redacted across `grain read` / `grain ci log`, `grain grep`, `grain_copy` (`ReadRedacted`), and `deploy://grain.log`, and windowed reads (`--offset` / `--limit` / `--tail`) redact whole lines before slicing so secrets are never split mid-line. |
| 4. `grain ci wait` on a running fake-CI run → deadline credited; `grain git revert <sha>` → attributed revert commit; `grain pr reply` / `resolve` against githubsim | **Pass after fixes (F4, F5).** `grain ci wait` blocks until `POST /api/e2e/fake-ci/runs/{id}/finish` settles the run and reports `wait credit used: 5s / 45m0s` (`grain ci run-job` accumulates `10s / 45m0s`); `grain git revert --commit <sha> --reason ...` creates a local revert commit with `Reverts-Commit: <sha>`; `grain pr reply --comment-id 501` and `grain pr resolve --comment-id 501` succeed against `githubsim` on `acme/widgets#9000`. |
| 5. `grain db query 'delete from task'` and a non-exposed table → refused | **Pass.** `grain db query "delete from task"` exits non-zero (`Query rejected: only read-only queries (SELECT, WITH, EXPLAIN, PRAGMA) are permitted with db-read capability`), and querying a non-exposed/non-existent secret table (`SELECT * FROM secret_store` or `deploy://state/secrets`) is refused. |

## Steps and observations

### 1. In-sandbox `grain` CLI subcommand sweep against `grain demo -github-sim -fake-ci -git-proxy`

1. Started `grain demo -github-sim -fake-ci -git-proxy -secrets-store -merge-queue-states` on `127.0.0.1:7809`, set `ciMode: "grain"` on `acme/widgets`, and filed task 19 with full read grants (`self-debug`, `memory`, `deployment-health`, `grain-log`, `read-issues`, `db-read`, and `api:*:read`).
2. Started run `19-run-1` via `POST /api/e2e/git-proxy/runs`, which minted sandbox token `demo-sandbox-19-1` and returned the loopback proxy remote `http://127.0.0.1:<port>/acme/widgets.git`.
3. Configured an isolated sandbox `$HOME` with `.git-credentials` containing `http://sandbox:<token>@127.0.0.1%3a<port>` (matching the percent-encoded host written by `git credential-store`) and no `GRAIN_SANDBOX_PROXY` / `GRAIN_SERVER` environment variables.
4. Cloned `acme/widgets.git` through the git proxy, committed `walkthrough.txt` on `grain/task-19`, pushed to `origin`, and executed every subcommand from the shell script:
   - `grain pr open` → `Pull request #9000 in acme/widgets: https://github.example/acme/widgets/pull/9000`
   - `grain pr status` / `grain pr base-status` → reported branch `grain/task-19` and base branch `main` CI states
   - `grain pr review-comment --path walkthrough.txt --line 1 "Looks good on line 1"` → recorded draft review finding for relay on run completion
   - `grain pr reply --comment-id 501 "Addressed review comment 501"` → `Replied to comment 501 on pull request #9000 (reply 1001).`
   - `grain pr resolve --comment-id 501` → `Resolved thread thread-501 on pull request #9000.`
   - `grain git revert --commit <sha> --reason "Revert test commit during walkthrough"` → created `revert: commit <short>` with trailer `Reverts-Commit: <sha>` in the sandbox working tree without pushing
   - `grain merge-queue` → rendered `deploy://merge-queue` showing in-flight batch `grain-merge-queue/.../main` and 3 queued pull requests
   - `grain ls` / `grain read` / `grain grep` across `src://playbooks`, `config://`, `config://settings/settings.yaml`, `memory://`, `deploy://`, and `task://`
   - `grain task list`, `grain task read 19`, `grain task prompt 19`, `grain task transcript 19`
   - `grain metrics`, `grain health` (`{"health":{"enabled":false},"sandboxCount":0}`), `grain log --tail 256` (`2026-10-29T00:00:00Z grain demo: started daemon log`), `grain playbooks` (`incident-triage.md`)
   - `grain issue list --repo acme/widgets --label enhancement --limit 5` and `grain issue read 1 --repo acme/widgets --comments` → returned issue `#1` (`Support sandbox CLI subcommands in demo mode`) and comment `101` by `@operator`
   - `grain db query "SELECT id, title FROM task LIMIT 3"` → returned 3 rows from `task`
5. Every proxy call logged an activity line (`grain: sandbox proxy call run 19-run-1 -> <tool> (status 200, N bytes)`) and incremented the run's tool call counter.

### 2. Grant matrix and `ci-read` repo scoping

With an ungranted task (`capabilities: []` on `acme/widgets`, which holds only the automatic repo-scoped `grain-ci` grant):

| Command / URI | HTTP Status | Proxy / CLI Response |
|---|---|---|
| `grain ls src://` | 403 | `grain: forbidden: src://: that tool belongs to the "self-debug" capability, and this run does not hold it` |
| `grain playbooks` | 403 | `grain: forbidden: src://playbooks: that tool belongs to the "bootstrap-playbooks" capability, and this run does not hold it` |
| `grain ls task://1` | 403 | `grain: forbidden: task://1: that tool belongs to the "self-debug" capability, and this run does not hold it` |
| `grain read config://settings/settings.yaml` | 403 | `grain: forbidden: config://settings/settings.yaml: that tool belongs to the "self-debug" capability, and this run does not hold it` |
| `grain ls config://repos/other/repo` | 403 | `grain: forbidden: config://repos/other/repo: that tool belongs to the "self-debug" capability, and this run does not hold it` |
| `grain ls memory://repos/other/repo` | 403 | `grain: forbidden: memory://repos/other/repo: that tool belongs to the "memory" capability, and this run does not hold it` |
| `grain issue list` | 403 | `grain: forbidden: grain issue: that tool belongs to the "read-issues" capability, and this run does not hold it` |
| `grain metrics` | 403 | `grain: forbidden: deploy://metrics: that tool belongs to the "self-debug" capability, and this run does not hold it` |
| `grain health` | 403 | `grain: forbidden: deploy://health: that tool belongs to the "deployment-health" capability, and this run does not hold it` |
| `grain log` | 403 | `grain: forbidden: deploy://grain.log: that tool belongs to the "grain-log" capability, and this run does not hold it` |
| `grain db query "SELECT id FROM task"` | 403 | `grain: forbidden: grain db query: that tool belongs to the "db-read" capability, and this run does not hold it` |
| `grain ls ci://other/repo` | 403 | `grain: forbidden: ci://other/repo is outside this sandbox's CI read scope` |
| `grain ls ci://acme/widgets` | 200 | `fake-ci-3/`, `fake-ci-2/`, `fake-ci-1/` (own repo allowed) |

### 3. Per-request whole-line redaction

1. Stored a custom secret `custom-api-token/token = custom-secret-val-9988776655` via `PUT /api/secrets/custom-api-token/token` after the server had already started, and finished `fake-ci-1` with step output containing both a GitHub PAT (`ghp_1234567890abcdefghijklmnopqrstuvwx`) and the custom secret (`custom-secret-val-9988776655`).
2. `grain ci log fake-ci-1 --job test --step test` and `grain read ci://acme/widgets/fake-ci-1/test/test.log` printed:
   ```
   PASS
   Using token [redacted: github token] and [redacted: secret custom-api-token/token] on this line
   Done
   ```
3. `grain grep "Using token" ci://acme/widgets/fake-ci-1/test` printed:
   ```
   test.log:2:Using token [redacted: github token] and [redacted: secret custom-api-token/token] on this line
   ```
4. `ReadRedacted` (`grain_copy`) and windowed reads (`redactWindow` with byte `offset`/`limit`) apply `s.redactor()` across whole lines before slicing bytes, so partial-line windows never leak a prefix or suffix of a token.

### 4. `grain ci wait` credit, `grain git revert`, and `grain pr reply` / `resolve`

1. `grain ci wait --timeout 30` started `fake-ci-1` on `grain/task-19`, waited for `POST /api/e2e/fake-ci/runs/fake-ci-1/finish`, and reported:
   `grain CI passed on (run fake-ci-1, waited 5s; wait credit used: 5s / 45m0s): test`
2. `grain ci run-job test --timeout 15` started `fake-ci-2`, waited for completion, and reported:
   `grain CI passed on (run fake-ci-2, waited 5s; wait credit used: 10s / 45m0s): test`
   followed by the selective-run notice.
3. `grain ci retrigger --jobs test` started `fake-ci-3` (`Retriggered grain CI on grain/task-19: fake-ci-3 grain/task-19 running ... Wait for it with: grain ci wait --resume fake-ci-3`).
4. `grain git revert --commit <sha> --reason "Revert test commit during walkthrough"` created revert commit `revert: commit <short>` with `Reverts-Commit: <sha>` in the sandbox checkout.
5. `grain pr reply --comment-id 501` and `grain pr resolve --comment-id 501` updated the simulated PR `#9000` on `githubsim`.

### 5. `grain db query` refusals

1. `grain db query "delete from task"` was refused with exit code 1:
   `grain: Query rejected: only read-only queries (SELECT, WITH, EXPLAIN, PRAGMA) are permitted with db-read capability`
2. `grain db query "SELECT * FROM secret_store"` was refused with exit code 1:
   `grain: Query failed: query execution failed: SQL logic error: no such table: secret_store (1)`

## Findings and fixes

- **F1 (fixed): `pkg/namespace.Server` ignored `NewRedactor` in `ReadRedacted` (`grain_copy`), `Grep`, `serveGlob`, `serveList`, `redactWindow`, and `RedactGrainLogText`.**
  - Cause: Only `serveRead` called `s.NewRedactor()` when `s.Redactor == nil`; the other methods passed `s.Redactor` (`nil` when configured with per-request `NewRedactor`) directly to `redactLine`. In addition, `pkg/daemon/mcpserver.go` passed a static `Redactor` instead of `NewRedactor` to `mcp.NewNamespaceTools`.
  - Fix: Added `(*Server).redactor()` in `pkg/namespace/namespace.go` and used it across `ReadRedacted`, `Grep`, `serveGlob`, `serveList`, `redactWindow`, and `RedactGrainLogText` (`pkg/namespace/stage2.go`); added `NewRedactor` to `mcp.NamespaceToolsConfig` (`pkg/mcp/namespace_tools.go`) and wired it in `pkg/daemon/mcpserver.go`.
  - Regression test: `TestNewRedactorAppliesToReadRedactedGrepAndGrainLog` in `pkg/namespace/namespace_test.go`.
- **F2 (fixed): `pkg/daemon/mcpserver.go` omitted Stage 2 `namespace.Scope` fields when registering MCP `grain_ls|read|grep|copy` tools.**
  - Cause: `ServeMCPServerPinned` populated only `TaskRepo`, `MayReadCI`, `MayReadAllCI`, `AllowedRepos` (from `api:ci:read`), and `MayReadSrc`, leaving `TaskIDStr`, `MayReadPlaybooks`, `MayReadAllTasks`, `MayReadFullConfig`, `MayReadAllMemory`, `MayReadMetrics`, `MayReadPipeline`, `MayReadHealth`, `MayReadGrainLog`, and `task.Reads`/`Writes` unset on the MCP server's `namespace.Scope`.
  - Fix: Populated all Stage 2 scope fields and `taskDetail.Task.Reads`/`Writes` in `pkg/daemon/mcpserver.go`.
- **F3 (fixed): `detectSandboxProxy` in `pkg/cli/sandboxcli.go` failed when `.git-credentials` lived in `$HOME`, had percent-encoded colons (`%3a`), or pointed at an ephemeral loopback port; several CLI flags were not forwarded.**
  - Cause: `osSandboxEnv` hardcoded `/home/debian/.git-credentials` without checking `$HOME/.git-credentials`; `git credential-store` percent-encodes `:` as `%3a` in the host (`http://sandbox:<tok>@127.0.0.1%3a9728`), which `url.Parse` rejects; `detectSandboxProxy` rejected `http://sandbox:<tok>@127.0.0.1:<port>` when `<port>` was not `9728` (`-sandbox-port-base=0` and `grain demo -git-proxy`); `--comments` was not in `sandboxBoolFlags`; and `grep --max`, `issue list --label/--limit`, `issue read --comments`, and `ci log --offset/--limit` were not forwarded in query parameters.
  - Fix: Updated `osSandboxEnv`, `parseGitCredentialURL`, `detectSandboxProxy`, `sandboxBoolFlags`, and query forwarding in `pkg/cli/sandboxcli.go`.
  - Regression test: `TestDetectSandboxProxy` in `pkg/cli/sandboxcli_test.go`.
- **F4 (fixed): `PullRequestOpener.reviewTool` in `pkg/daemon/sandbox_cli.go` passed empty `task.Branch` instead of `task.BranchName()`.**
  - Cause: Tasks that use the default branch name (`grain/task-<id>`) leave `task.Branch` empty in the store, causing `reply_to_pull_request_comment` and `resolve_review_thread` (`grain pr reply` / `grain pr resolve`) to fail with `this task has no branch` unless a PR link was already cached on the task row.
  - Fix: Changed `Branch: task.Branch` to `Branch: task.BranchName()` in `PullRequestOpener.reviewTool` (`pkg/daemon/sandbox_cli.go`).
- **F5 (fixed): `sandboxDeploy.ReadGrainLog` in `pkg/daemon/sandbox_namespace.go` failed when `grain log` (or `deploy://grain.log`) was called with no `<source>` argument.**
  - Cause: `resolveDeploy` passes `source = ""` for `deploy://grain.log`, and `sandboxDeploy.ReadGrainLog` omitted `args["source"]`, causing `grainlog` to fail with `source "" is not a log this run can read`.
  - Fix: Defaulted empty `source` to `"daemon"` in `sandboxDeploy.ReadGrainLog` (`pkg/daemon/sandbox_namespace.go`).
- **F6 (fixed): `grain demo -github-sim -fake-ci -git-proxy` did not wire `Namespace`, `CLI`, `CLIActions`, `WaitCredit`, `API`, `SourceDB`, `demoFakeCI` `RunStore`, or `secrets/secrets.key` on the demo git proxy.**
  - Cause: `configureGitProxyDemo` (`pkg/demo/demo_gitproxy.go`) originally built `gitproxy.BuildProxy` only for git push auditing (`demo.go`), without wiring the namespace server, `/cli/` backend, action handlers, wait creditor, or `demoFakeCI` `ci.RunStore` methods (`Put`, `Update`, `Live`).
  - Fix: Wired `Namespace`, `CLI`, `CLIActions`, `WaitCredit`, `API`, `SourceDB`, `demoFakeCI` `ci.RunStore`, `state-repo/settings/settings.yaml`, `secrets/secrets.key`, and seeded `githubsim` issue/comment/review thread in `pkg/demo/demo.go`, `pkg/demo/demo_fakeci.go`, `pkg/demo/demo_githubsim.go`, `pkg/demo/demo_gitproxy.go`, and `pkg/demo/demo_qualification.go`.
  - Regression test: `TestDemoGitProxyServesNamespaceAndSandboxCLI` in `pkg/demo/demo_gitproxy_test.go`.

## Re-running this

```sh
go test -race ./pkg/namespace/... ./pkg/cli/... ./pkg/demo/... ./pkg/gitproxy/...
```
