# 2026-10-29: Sandbox-first agent surface (`grain cap`, MCP control roster, proxy hardening, SSH signing)

Task 1798 (`grain/049f1e09/task-1798`) walked §4 of
[`docs/test-plans/2026-10-29.md`](../test-plans/2026-10-29.md). That section
covers PRs #112 (`grain cap`), #120 (the control roster), #101 (sandbox-proxy
hardening) and #85 (SSH commit signing). Every probe ran against the real
`grain` binary and grain's real proxy code, from a sandbox-style shell
(throwaway `$HOME`, `GRAIN_SANDBOX_PROXY`, `GRAIN_SANDBOX_TOKEN`) with `curl`
and `git`. Two small fixes came out of it (below). No bypass of a trust
boundary was found.

## Commit and environment

- **Base tested**: `2f53e63a` (`main`, "Merge pull request #130 from
  ai-on-gke/grain/049f1e09/task-1732"). The fixes were re-run against
  binaries built from this branch.
- **Build**: `cd ui && npm ci && npm run build`, then
  `go build -o /home/debian/bin/grain ./cmd/grain`.
- **Demo**: `grain demo -github-sim -fake-ci -git-proxy -reconcile 2s
  -addr 127.0.0.1:18080 -open=false -access-code-file /var/tmp/walk/code`.
  `-git-proxy` serves grain's real sandbox proxy (`gitproxy.BuildProxy`,
  with `/api/`, `/cap/`, `/fs/` and `/cli/` wired to the demo's in-process
  API). `POST /api/e2e/git-proxy/runs {"taskId": ...}` records a live run
  and mints its sandbox token.
- **Sandbox shell**: `HOME=/var/tmp/walk/home
  GRAIN_SANDBOX_PROXY=http://127.0.0.1:<proxy> GRAIN_SANDBOX_TOKEN=<run token>`,
  with `GRAIN_SERVER` unset. The throwaway `$HOME` matters: this task's own
  sandbox has `~/.git-credentials` pointing at the real deployment's proxy,
  and the sandbox CLI would otherwise discover that proxy instead.
- **Toolchain**: Debian, Go (`/usr/local/bin/go`), Node 20, OpenSSH
  `ssh-keygen`, git. `sqlite3` was installed with apt to read and adjust the
  demo's store.

## Stubs and harnesses

| Boundary | How | Why |
|---|---|---|
| External capability server | **Harness** `/var/tmp/walk/capwalk`, a throwaway `main` package built from this tree. It has a hand-written streamable-HTTP JSON-RPC data plane at `/runs/att-walk-1/mcp`, with tools `ping` (string, integer, boolean and array args) and `rotate` (`annotations.modelOnly: true`). It sits behind grain's real `protocol.NewProxy` and `protocol.ServeEndpoint`, with `protocol.Handshake` and `protocol.ResolveModelOnly(…, DeliverySandbox)` deciding delivery. That is registered in a `gitproxy.CapRegistry` under `CapSandboxKey` and served by `gitproxy.NewHandler`. The data plane logs every request it receives, headers included. | The demo neither runs agents nor dispatches an attempt through `orchestrator.wireEndpoints`, so its proxy has no `CapServers` registry. The CI-job key is the one `/cap/` path that needs no live task scope. Everything from the proxy outwards is production code. |
| Ambient credential with SSH signing | **Harness** `/var/tmp/walk/signwalk`. It runs `gitproxy.LoadCredentialSet` + `SetAmbient("op","github.com","gh")` against a throwaway host `$HOME` (`GRAIN_TEST_AMBIENT_HOME`) holding a fresh ed25519 key and `.gitconfig` with `gpg.format=ssh`. It serves `/_grain/git-sign` with `gitproxy.NewHandler`, and writes the sandbox's files with `mcp.GitCredentialFilesInDir`, `mcp.CurlCredentialFiles` and `mcp.GitSigningFiles`. | The demo has no ambient credential. The signing path itself, the sandbox's `.git-ssh-sign` wrapper, `git` and `ssh-keygen` all ran for real. |
| Demo store | `sqlite3` was used to give two tasks `grain-ci` (`task_grant`), because the API refuses `grain-ci` by hand. It also made one task a changelist (`change_target_kind`), ended one run, and blanked one run's `task_id`. | The demo has no CL tasks and no repo on Grain CI. |

## Outcome

| # | Item (§4 "What is NOT proved") | Result |
|---|---|---|
| 1 | `grain cap` against a real external server: list, `--help`, flag typing, `--json` with overriding flags, model-only refused 403 (also in a batch), census | **Pass**. Wording fix below. Prompt block not exercised live (see Not exercised). |
| 2 | `tools/list` for an ordinary run, a run without `grain-ci`, a CL task and `-mcp-surface=full`; a CLI equivalent for each dropped tool | **Pass**. One CLI fix below. |
| 3 | Proxy hardening: forged `X-Grain-*`, `/api/` and `/cap/` with no task, reserved `--id`, `api:releases:write` | **Pass** |
| 4 | SSH signing: a sandbox commit verifies with `git verify-commit` | **Pass** |

## Steps and observations

### 1. `grain cap` (harness proxy, token `tok-walk`)

| Check | Observed |
|---|---|
| Delivery resolution | Handshake listed `ping` and `rotate`. `ResolveModelOnly` gave `ping` sandbox and `rotate` model-only. |
| `grain cap list` / `grain cap walkcap` | `walkcap (capability:walkcap)`, then `grain cap walkcap ping  Ping a host…`, then `model-only, not callable here: rotate`. Exit 0. |
| `grain cap list --json` | `{"servers":[{"name":"walkcap","grant":…,"tools":[{"name":"ping",…,"modelOnly":false}],"modelOnlyTools":["rotate"]}]}` |
| `grain cap walkcap ping --help` | Flags come from `inputSchema`: `--host <string> (required)`, `--count <integer>`, `--tags <string>...`, `--verbose`, `--json '<object>'`. |
| Flag call | `--host example.com --count 3 --verbose --tags a --tags b`. The server received `{"count":3,"host":"example.com","tags":["a","b"],"verbose":true}`: typed, with the array repeated. |
| Bad flag type | `--count notanumber` gave `--count: "notanumber" is not an integer`, exit 1. Nothing was sent. |
| `--json` plus an override | `--json '{"host":"j.example","count":1}' --count 7` sent `{"count":7,"host":"j.example"}`. |
| Model-only through the CLI | `grain cap walkcap rotate` (and its `--help`) answered "rotate is model-only: it cannot be called from the sandbox", exit 1. |
| Unknown server | `grain cap nosuch list` got 403 "capability server "nosuch" is not granted to this sandbox". |
| Raw `tools/call rotate` | 403 "is model-only". The server never saw it. |
| Raw `tools/call secret_dump` (unlisted) | 403, but the message said "is model-only" (**fixed**, see Findings). |
| Batch `[ping, rotate]`; batch notification `[rotate]` with no id | 403 for both. The server never saw either. |
| `{"params":{"name":"rotate"},"Params":{"name":"ping"}}` | 400 "key "Params" appears twice". |
| `"name":"rotate"` | 403. The escape is decoded before the check. |
| Two concatenated messages | 400 "invalid character '{' after top-level value". |
| `PUT` with a `rotate` body | 405 from the per-grant endpoint proxy. |
| `GET` with a `rotate` body | Reached the server as a `GET` with an **empty** body: `protocol.Proxy` sends a body only on POST. |
| Batch `[ping, ping]` with `X-Grain-Acting-Task`, `X-Grain-Channel`, `Cookie`, `Proxy-Authorization` and the proxy token | 200. The server's log shows only `Accept`, `Content-Type`, `User-Agent`, `Accept-Encoding` and **its own** `Authorization: Bearer cap-server-own-secret`. There was no proxy token, `X-Grain-*`, cookie or proxy auth. |
| No token or a bad token | 401 |
| Census | The proxy logged `cap__walkcap__ping` for the single call, and `cap__walkcap` for batches and refusals (`len(calls) == 1` names the tool). On the demo's agent-run path, `task_run_tool` recorded `cap_list` and `cap__nosuch` rows. |

### 2. Control roster (`grain mcpserver`, launched as the daemon launches it)

Command: `grain mcpserver -server http://127.0.0.1:18080 -task <id> -pr-repo
acme/widgets -pr-branch grain/task-<id> -sandbox-root /var/tmp/walk/sbx
-data-dir <demo dir> [-mcp-surface=full]` with `GRAIN_TOKEN=<access code>`.
The client was a stdio script sending `initialize`, `notifications/initialized`
and `tools/list` (`/var/tmp/walk/toolslist.sh`). With `-server`, the run's
grants are the task's own (`taskDetail.Task.Capabilities`), so a `-grant`
flag has no effect. The cases below set the grants on the tasks.

| Case | Tools |
|---|---|
| (a) Ordinary, `grain-ci` (task 22) | 12: `ask_question comment_on_issue edit_file nothing_needed propose_task read_file recreate_sandbox request_secret resume_later run_command wait_for_task write_file` |
| (b) No `grain-ci` (task 21) | (a) + `base_branch_status`, `pull_request_status`, `wait_for_checks` (15) |
| (c) CL task, `grain-ci` + `change_target_kind=changelist` (task 23) | (a) + `pull_request_status`, `wait_for_checks` (14) |
| (d) `-mcp-surface=full` (task 22) | 67: (a) plus 55 more |

These match `controlRoster` (`pkg/daemon/mcp_surface.go`) and the design
doc's §6.

`grain mcpserver` without `-data-dir` logged "pull_request_status and
wait_for_checks are unavailable this run: -data-dir is required with
-pr-repo". The daemon always passes it.

**CLI equivalents of the dropped tools**, run through the demo proxy on task
22's live run (`/var/tmp/walk/sweep.sh`):

| Dropped tool | `grain` command | Result |
|---|---|---|
| `list_grain_tasks`, `read_grain_task`, `read_grain_task_prompt`, `read_grain_task_transcript` | `task list`, `task read 22`, `task prompt 22`, `task transcript 22` | ok |
| `message_task` | `task message 21 …` | 403 "may only message its parent or its children". That is the right refusal for an unrelated task. Messaging itself is also refused ("just say it"). |
| `grain_ls`, `grain_read`, `grain_grep`, `grain_copy` | `ls task://22`, `read task://22/record.md`, `grep probe task://22` (`read … > file` for copy) | ok |
| `list_ci_runs`, `query_test_history`, `get_ci_log`, `get_ci_artifact`, `retrigger_ci` | `ci list`, `ci test-history`, `ci log`, `ci artifact`, `ci retrigger` | ok. `ci log` and `ci retrigger` say "no grain CI run yet", which is accurate. |
| `wait_for_checks`, `run_ci_job` | `ci wait --timeout 1`, `ci run-job go` | `ci wait` reported the fake run still running and the wait credit. `ci run-job` blocks: `-fake-ci` never finishes a run. |
| `pull_request_status`, `base_branch_status`, `open_pull_request` | `pr status`, `pr base-status`, `pr open` | ok. `pr open` says "commit and push it before asking for a pull request". |
| `add_review_comment`, `reply_to_pull_request_comment` | `pr review-comment <body>`, `pr reply <body>` | ok positionally. **`--body` was ignored** (fixed, see Findings). |
| `resolve_review_thread` | `pr resolve --comment-id N` | ok |
| `revert_task`, `revert_merge` | `git revert --task N --reason …` | ok ("could not find any commit mentioning task", which is accurate) |
| `query_merge_queue`, `read_grain_metrics`, `update_status`, `write_handover`, `notify_operator` | `merge-queue`, `metrics`, `status`, `handover write`, `notify` | ok. `metrics` prints "by surface: N MCP control tool call(s), M grain CLI command(s)". |
| `api_*` (26 tools) | `grain api GET /api/tasks/22`, `grain api routes` | ok |

### 3. Proxy hardening (demo proxy, `curl -u sandbox:<token>`)

| Probe | Observed |
|---|---|
| `PATCH /api/tasks/2` from task 1's sandbox, with `X-Grain-Acting-Task: 2`, `x-grain-acting-task: 2`, `X-Grain-Acting-Run: 2-run-1`, `X-Grain-Channel: ui` | 403 "task **1** holds no api grant for scope tasks:write". The forged headers were overwritten. |
| No token or a bad token on `/api/` | 401 |
| `/cap/` on a run with no capability servers | `{"servers":[]}`. `/cap/nosuch/mcp` gave 403. |
| Token of an **ended** run (`finished_at` set) on `/api/`, `/cap/`, `/cap/release-notes`, `/fs/` | 403 "no live task run on this sandbox" |
| Live run with `task_id = ''` | 500 "failed to resolve sandbox namespace scope" on every route. The store cannot build a scope for it, so nothing is forwarded (fail-closed). `forwardToAPI`'s own "no task to act for" 403 sits behind that and stays covered by `TestPerAttemptTokenBindingAndStage3CLIActions`. |
| `grain task propose --id review:1 …`, twice | Both returned `#19`, so the second was idempotent. `task_tag` holds `proposal:1:749dd616…` only, so the raw `review:1` never became a tag. |
| Task 20 holding `api:releases:read` + `api:releases:write`: `PUT /api/release-notes` and `PUT /cap/release-notes` | 403 "holds no api grant for scope memory:write". `GET /api/release-notes` gave 200. |

### 4. SSH commit signing

The host `$HOME` was `/var/tmp/walk/sign/hosthome`, holding
`ssh-keygen -t ed25519 -N ''` and `.gitconfig` with `gpg.format=ssh`,
`user.signingkey=~/.ssh/id_ed25519.pub` and `commit.gpgsign=true`. The
sandbox `$HOME` was `/var/tmp/walk/sign/sbxhome`, written by grain's own
renderers.

- Discovery: `HostGitIdentity` read "Walk Signer" <walk@example.com>, with
  the public key line as `SigningKey`.
- The sandbox `.gitconfig` had `gpg.format=ssh`, `gpg.ssh.program=~/.git-ssh-sign`,
  `allowedSignersFile=~/.git-allowed-signers`, `commit.gpgsign` and
  `tag.gpgSign`. There was no private key anywhere in the sandbox `$HOME`.
- `git commit -m 'signed from sandbox'` with `HOME=<sandbox>
  GIT_CONFIG_NOSYSTEM=1` produced a commit with a `gpgsig` SSH signature
  block.
- `git verify-commit HEAD` returned `Good "git" signature for walk@example.com
  with ED25519 key SHA256:HaGPxf3z…`, exit 0.
- The same commit with its message rewritten failed: "Signature verification
  failed: incorrect signature".
- `/_grain/git-sign` with no token or a bad token gave 401. `GET` gave 405.
  An empty body gave 400. 1 MiB + 1 byte gave 413 "signing payload exceeds
  1048576 bytes".

## Findings, fixes and follow-ups

1. **Fixed: `grain pr review-comment --body …` and `grain pr reply --body …`
   failed with "missing <body>"** (`pkg/cli/sandboxcli_actions.go`). These
   commands replace `add_review_comment` and `reply_to_pull_request_comment`,
   whose MCP parameter is `body`. `grain task propose` and `grain notify` take
   `--body` too. The parser accepted the flag and then ignored it. `textArg`
   now falls back to `--<what>` when there is neither a positional nor
   `--file`. `TestSandboxCIWaitReportsEachVerdict` (`pkg/cli/sandboxcli_actions_test.go`)
   now asserts `--body` for both.
2. **Fixed: an unlisted tool was refused as "model-only"**
   (`pkg/gitproxy/capserver.go`). The 403 is unchanged, but it now says
   `capability server "walkcap" lists no tool "secret_dump" for this run`.
   `TestCapRefusalSaysWhetherTheToolIsModelOnlyOrUnlisted` pins both
   wordings, inside a batch.
3. **Plan correction**: `grain demo` has no `-sandbox-port-base` flag. The
   test plan's shared-environment command (and the deployment note that
   recommends it) applies to `grain daemon`, and `grain demo` exits with
   "flag provided but not defined". Drop it for the demo.
4. **Observation, not changed**: the census and `task_run_tool` name a
   refused call to an unknown server after the sandbox's own string
   (`cap__nosuch`), so a sandbox can add arbitrary tool names to its run's
   tool rows. The rows stay scoped to the run, and the name is the server
   segment of the path. It is low risk and was left as it is.
5. **Gap in the plan's harness**: no local stand-in dispatches an attempt
   with an external capability grant through `orchestrator.wireEndpoints`.
   The demo runs no agents, and its `-git-proxy` has no `CapServers`. So
   the `<capability-cli>` prompt block and the run-keyed census row
   `cap__<server>__<tool>` in `task_run_tool` were seen only through their
   unit tests (`TestWireEndpointsPartitionsToolsByDelivery`) and the CI-job
   path's log line.

No bypass of `/cap/`, `/api/`, `/_grain/git-sign` or the acting headers was
found.

## Not exercised

- The `<capability-cli>` prompt block in a real agent prompt, and a run-keyed
  `cap__<server>__<tool>` row in `task_run_tool` (finding 5).
- `POST /api/capability-servers/test` reporting each method's `delivery`.
- Antigravity's `enter_plan_mode`/`schedule` denial. No agent ran.
- `/home/debian/.grain/mcp.json` in a real CI job.
