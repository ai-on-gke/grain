# Walkthrough: remote executor MCP tunnel, installer and relay hardening

Test plan: `docs/test-plans/2026-10-01.md` §7 (task 1416).
Date: 2026-10-01, 21:11–21:23 UTC.

## Environment

- Base: `main` at `49fc95eb0`. It contains every commit under test:
  `5f0137b69` `c2da232d0` `27f7a1866` `a9edc7c2e` `d2a9d9cf4` `0c07e84e3`
  `93c4c3943` `140bf9271` `18cb8f40a`.
- The plan document was not readable. Branch `grain/task-1353` does not exist
  on the remote, and `refs/pull/1353` is an unrelated, older pull request
  without the file. The walkthrough follows the steps as the task listed
  them.
- Host: grain sandbox VM (Debian, linux/amd64, 4 vCPU, 7.8 GiB), Go 1.26.6.
  `strace` was installed with apt so the token file's creation mode could be
  observed.
- Binaries were built from `main`: `go build -o /tmp/grain ./cmd/grain`
  (sha256 `37d47533…19e8bd2`) and `go build ./cmd/grain-tunnel-gateway`.

Processes (all real, all on 127.0.0.1):

| Process | Command |
|---|---|
| relay | `grain relay -port 18080 -daemon-token relay-daemon-tok -executor-token relay-exec-tok` |
| daemon | `grain daemon -data-dir /tmp/w/data -ui-addr 127.0.0.1:18420 -sandbox-dir /tmp/w/sbx -executor-addr 127.0.0.1:18422 -executor-token-file /tmp/w/exectok -relay-url http://127.0.0.1:18080 -relay-token relay-daemon-tok` |
| tunnel gateway | `grain-tunnel-gateway -port 18090 -token gw-tok` |
| executor | started by the daemon's served `install.sh` (see §1) |

The daemon connected to the relay at once (`established reverse tunnel to
relay ... (id: default)`).

### Stubs and substitutions

- **Agent CLI.** A shell-script `agy` in the install prefix. It records its
  environment, pipes one JSON-RPC request through the grain binary grain
  handed it (issued argv), tries a forged argv the same way, and checks the
  environment of a shell command it spawns.
- **Dispatch.** The stub task was not dispatched through the daemon's task
  queue. A real dispatch needs three things this sandbox does not have: a
  fake GitHub (the e2e `githubsim`), an `executor-*` agent backend selected
  in Settings, and sandbox budget. The daemon reported "0.0 GiB of sandbox
  memory (7.8 GiB host less 8.0 GiB reserved)", so it admits no agent run.
  Instead a small harness took the controller's side of the dispatch
  (`modelexec.Server`, `RemoteHost.AuthorizeMCPArgs`, `RemoteHost.Run`), the
  same calls a Framework makes. Its `ServeMCP` answers with the argv it was
  asked to serve. The executor was the real installed
  `grain executor`, launched by `install.sh` a second time (instance
  `walk-exec-h`).
- **Forged-argv client.** The forged MCP streams were sent through the live
  executor's own tunnel socket (`/tmp/grain-executor-*/mcp.sock`). That
  socket forwards an arbitrary argv over the authenticated executor
  connection to the real daemon, whose `ServeMCP` is the real
  `grain mcpserver`. The impersonation case (finding 1) needs a hand-written
  hello, so it is a Go test with a raw executor client.

## 1. Installer from `/api/executors/install.sh`

```
curl -s http://127.0.0.1:18420/api/executors/install.sh -o install.sh          # 200
curl -s http://127.0.0.1:18420/api/executors/bin/grain-linux-amd64.sha256       # 200, 37d47533…  grain-linux-amd64
curl -s http://127.0.0.1:18420/install/linux/amd64.sha256                       # 200, same digest
```

**Wrong `--sha256`** (empty prefix, no grain on `PATH`):

```
HOME=/tmp/w/pfx-bad PATH=/usr/bin:/bin bash install.sh --server 127.0.0.1:18422 \
  --grain-url http://127.0.0.1:18420 --token … --name wrongsha --sha256 000…000
```

The installer printed `Checksum mismatch … expected 000… (from --sha256), got
37d47533…` and `refusing to install a grain binary that failed checksum
verification`, and exited 1. The prefix afterwards held only an empty
`.local/bin`. There was no grain binary, no `.grain` directory, no token
file and no executor process. **Pass.**

**Correct `--sha256`**, under `strace -f -e openat,chmod,fchmodat,rename,renameat2`:

```
HOME=/tmp/w/pfx PATH=/usr/bin:/bin bash install.sh --server 127.0.0.1:18422 \
  --grain-url http://127.0.0.1:18420 --token … --name walk-exec --instance-id walk-exec-1 \
  --sha256 37d4753331eb58cf95b62a3a2933a9d36b109628ed81ae7fd168aff1219e8bd2
```

- `Verified SHA-256 37d47533… (from --sha256)` was printed, then
  `Installed grain binary to /tmp/w/pfx/.local/bin/grain`.
- `grain executor` started and connected: `connected to 127.0.0.1:18422 as
  walk-exec`. The daemon's `/api/executors` listed `walk-exec-1`.
- Token file: `pfx/.grain` is `drwx------` and
  `executor-walk-exec-1.token` is `-rw-------`. strace shows the file was
  0600 from its first syscall:
  ```
  openat(".../.grain/.executor-token.BPgZnj", O_RDWR|O_CREAT|O_EXCL, 0600)
  openat(".../.grain/.executor-token.BPgZnj", O_WRONLY|O_CREAT|O_TRUNC, 0666)   # existing file, umask 077
  fchmodat(".../.grain/.executor-token.BPgZnj", 0600)
  renameat2(... -> ".../.grain/executor-walk-exec-1.token")
  ```
  **Pass.**

## 2. Stub agent on the executor

- The executor advertises `grain` as a shim,
  `/tmp/grain-executor-*/grain` (mode `-rwx------`, in a `drwx------`
  directory next to `mcp.sock`).
- Stub agent output (harness on :18522, executor `walk-exec-h`):

```
agent env has GRAIN_EXECUTOR_SOCK: 0
--- issued argv through the shim:
{"jsonrpc":"2.0","id":1,"result":{"served":"[-task 1416 -sandbox-root /tmp/w/run-1416]"}}   # after fix 2; empty on main
--- forged -sandbox-root / through the shim:
rc=0                                          # refused: harness logs "refused an MCP tunnel ..."
--- model-chosen shell command env:
0
```

`/tmp/w/agent-env.txt` has no `GRAIN_EXECUTOR_SOCK`. Neither does a shell
the agent spawns. **Pass.** On `main` the "issued argv" request was served,
but its answer never reached the agent. That is finding 2.

## 3. Forged MCP tunnel argv

Sent through the live executor's tunnel socket to the real daemon. Each
stream carried one forged argv plus an `initialize` request:

| argv | result | daemon log |
|---|---|---|
| `mcpserver -sandbox-root /` | stream closed, 0 bytes | `walk-exec refused an MCP tunnel whose arguments grain did not issue to it` |
| `mcpserver -github-host evil` | stream closed, 0 bytes | same |
| `mcpserver -github-insecure-http` | stream closed, 0 bytes | same |
| `mcpserver -data-dir /tmp/w/data` (the daemon's own data dir) | stream closed, 0 bytes | same |

**Pass**, for argvs grain never issued. A replay of an argv issued for
*another* executor's run did **not** hold on `main`; see finding 1.

## 4. Relay

| Check | Request | Status / body |
|---|---|---|
| second registration of live `default` | `GET /relay/daemon?daemon_id=default` + Upgrade + Bearer | **409** `Conflict: daemon id already connected` |
| same, while the daemon was frozen with SIGSTOP | same | **409** |
| reconnect after the first session dies (curl peer killed) | `…?daemon_id=rc`, second attempt | 409 while the first was alive, **101** after it died |
| real daemon, frozen 33 s then resumed | — | relay dropped it after the ping timeout ("disconnected", 21:18:55); daemon logged `relay session ended: ping timeout`, re-established 21:19:03 |
| unknown id, proxied request | `GET /api/executors?daemon_id=nope` | **503** `Grain reverse tunnel not connected` (no fallback to `default`) |
| unknown id, executor stream | `/relay/executor?daemon_id=nope` + Bearer | **503** |
| known id, for contrast | `GET /api/executors?daemon_id=default` | 200 |
| Origin on daemon upgrade | `Origin: https://evil.example` | **403** `Forbidden: origin not allowed` |
| Origin on proxied WebSocket | `/api/events`, `Origin: https://evil.example` | **403** |
| same-origin upgrade | `Origin: http://127.0.0.1:18080` | 101 |
| `?token=` daemon | `/relay/daemon?daemon_id=x&token=<valid>` | **401** `Unauthorized` |
| `?token=` executor | `/relay/executor?token=<valid>` | **401** |
| `?token=` tunnel gateway | `/_grain/tunnel?token=<valid>` | **401** |
| gateway wrong bearer | `Authorization: Bearer nope` | 401 |
| gateway evil Origin | valid bearer, `Origin: https://evil.example` | **403** |
| gateway, no tunnel | `GET /api/x` | 503 `Grain reverse tunnel not connected` |
| relay error body | peer registered as `ff`, then killed mid-request | **502** `Error reading response from Grain: an upstream service returned an error` |
| gateway error body | same against the gateway | **502**, same text |

In both 502 cases the raw error (`reading frame header: EOF`) appears only
in the relay or gateway log, never in the response body. **All pass.**

## 5. Flow control

Payload: a 500 MiB random file (sha256 `76da9854…c7ec77`) the daemon serves
from `~/.grain/bin/grain-linux-arm64` at `/api/executors/bin/grain-linux-arm64`.
The client requested it through the relay
(`?daemon_id=default`) with `SO_RCVBUF=64 KiB`, read nothing for 30 s,
then drained it. RSS came from `/proc/<pid>/status` (VmRSS):

| t | relay RSS | daemon RSS |
|---|---|---|
| before | 41 MiB | 50 MiB |
| +3 s … +30 s (10 samples, stalled) | 41 MiB every sample | 49–50 MiB |
| after the drain | 38 MiB | 50 MiB |

Growth during the stall: about 0 MiB for both, well under the 64 MiB bound.
The body arrived complete: `HTTP/1.1 200 OK`, 524 288 000 bytes, sha256
matching, 30.8 s in all. **Pass.** No pre-`140bf9271` build was measured for
comparison (see Skipped).

## Findings

### 1. MCP grants followed an executor-chosen instance id to another token (fixed)

`mcpGrants` was a server-wide table keyed by `hostConn.id`, and that id is
`hello.InstanceID`: the executor's own claim. It is also shown to anyone who
can list executors. Any client with *any* valid executor token, including
another team's, could connect claiming a live run's instance id. `register`
replaced the victim's connection, and replaying the run's argv was served.
That argv carries the other team's `-task` and `-data-dir`, and with them
that run's credentials.

Reproduction: `pkg/modelexec/mcpgrants_impersonation_test.go`,
`TestMCPGrantsDoNotPassToAnotherConnectionClaimingTheSameID`. A team-alpha
raw executor is granted `-task 41 -data-dir /var/lib/grain`. A team-beta
token then connects as `alpha-node-1` and opens a stream with that argv. On
`main` it fails with:

```
a team-beta token claiming instance "alpha-node-1" was served team-alpha's run argv ["-task" "41" "-data-dir" "/var/lib/grain"]
```

Fix (commit 1 on this branch): grants live on the `hostConn` they were
issued through, and `handleStream` checks only the stream's own connection.
A `RemoteHost` is bound to a single `hostConn`, so a run's commands and its
mcpserver tunnel never use another connection. The grants are now freed
with the connection, which also ends a slow leak: before, every connection
with an auto-generated instance id left an entry behind.

### 2. `grain mcpserver` over the tunnel dropped the answer once stdin hit EOF (fixed)

`tunnelMCPServer` returned when either copy finished, so with stdin at EOF
(`echo req | grain mcpserver …`) it exited before grain answered. The stub
agent's issued-argv request was served (harness log `ServeMCP served argv
…`), but stdout stayed empty. The request had been acted on and its result
was thrown away.

Reproduction: `cmd/grain/mcpserver_tunnel_test.go`,
`TestTunnelMCPServerWaitsForTheAnswerAfterStdinEnds`. On the old code it
fails with `stdout = "", want "{\"answer\":{\"id\":1}}\n"`. It was also
confirmed live (§2) before and after the fix.

Fix (commit 2): half-close the tunnel when stdin ends, and return when
grain's side finishes.

### 3. A different token can evict a live executor by claiming its instance id (not fixed; follow-up proposed)

`modelexec.Server.register` closes and replaces any live connection with the
same instance id, whatever token or team the newcomer holds. After fix 1
this no longer leaks grants. But a holder of any executor token can still
disconnect another team's executor, and the runs on it, and then take its
slot. Refusing or namespacing the claim changes reconnect semantics, for
example token rotation without revocation. That needs a design decision, so
it is filed as a follow-up rather than fixed here. Source: code reading
(`pkg/modelexec/server.go`, `register`) and the impersonation test above,
whose attacker connection replaced the victim.

### 4. A refused MCP tunnel looks like an empty, successful session (observation)

When the controller refuses a stream, the executor side just closes it.
`grain mcpserver` exits 0 with no output (stub output `rc=0`). The refusal
is logged on the controller only, so an agent CLI sees an MCP server that
died silently. Error reporting only; it is not a security problem, and it
is left as is.

## Commands run for verification

- `go test ./pkg/relay/... ./pkg/tunnel/... ./pkg/modelexec/... ./cmd/grain-relay/... -count=1`: all ok.
- `go test ./tests/e2e -run RemoteExecutor -count=1 -v`: 7/7 pass.
- `go test ./cmd/grain -run 'TestTunnelMCPServer|MCP|Mcp' -count=1`: ok.
- `make security` (`go vet ./...` plus `tests/security` under `-race`): ok, 116 s.
- Full grain CI through `wait_for_checks`: see the pull request.

## Skipped and why

- **Dispatch through the daemon's own task queue.** It needs a fake GitHub,
  an `executor-*` backend in Settings, and sandbox budget (0 GiB admissible
  on this host). The controller side of the dispatch was driven by a harness
  through the same `RemoteHost` calls a Framework makes, against the real
  installed executor. The daemon side of the MCP tunnel (real
  `grain mcpserver` pins and refusal) was exercised against the live daemon
  in §3.
- **Before/after RSS against a build without `140bf9271`.** Only the fixed
  build was measured. It showed no growth at all, so the bound holds. The
  size of the improvement was not measured.
- **TLS, IAP and Google ID-token authentication paths.** Shared-secret
  tokens only. No IAP audience is available in a sandbox.
