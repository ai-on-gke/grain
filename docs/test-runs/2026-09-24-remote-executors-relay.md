# 2026-09-24 Remote Executor Pools, Rendezvous Relay Routing, and Agent Concurrency Test Run

## Overview

Comprehensive verification and live end-to-end walkthrough for Remote Executor Pools, Rendezvous Relay Routing, and Agent Concurrency per `docs/test-plans/2026-09-24.md` §1.

## Scope and Environment

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/task-541`
- **Host**: Debian bookworm, Linux x86_64
- **Go**: `1.26.6`
- **Node**: `20.19.0`
- **Vitest**: `4.1.11`
- **Test Artifacts**:
  - `cmd/grain/relay.go`: `grain relay` subcommand implementation.
  - `scripts/grain-remote-executor.sh`: symlink to `scripts/run-remote-executor.sh`.
  - `scripts/run-remote-executor.sh` & `pkg/ui/executor_install.sh`: concurrency (`-c`, `--concurrency`, `GRAIN_CONCURRENCY`) and instance ID (`-i`, `--instance-id`, `GRAIN_INSTANCE_ID`) flag support with instance-scoped token storage.
  - `tests/deploy/run_remote_executor_test.go`: flag, environment variable, and symlink unit tests.
  - `tests/e2e/remote_executors_relay_e2e_test.go`: full Go end-to-end multi-executor pool scheduling, deduplication, workspace isolation, token revocation, and half-close test.
  - `scripts/test-remote-executors-relay-walkthrough.mjs`: live subprocess orchestration script for end-to-end validation.

## Feature Implementations & Changes

1. **Subcommand Wiring (`cmd/grain/relay.go`, `cmd/grain/main.go`)**:
   - Added `grain relay` top-level CLI subcommand invoking `relay.Server` with flags (`-port`, `-daemon-token`, `-daemon-token-file`, `-log-level`).
   - Added `grain relay` dispatch to main CLI entry point.
2. **Symlink and Script Parity (`scripts/grain-remote-executor.sh`, `pkg/ui/executor_install.sh`)**:
   - Created `scripts/grain-remote-executor.sh -> run-remote-executor.sh` symlink.
   - Added `-c`, `--concurrency`, `-i`, `--instance-id` flags and environment variable handling (`GRAIN_CONCURRENCY`, `GRAIN_INSTANCE_ID`).
   - Scoped default token file paths to `$HOME/.grain/executor-${INSTANCE_ID:-${NAME:-}}.token` to avoid token collisions when multiple executor processes run concurrently on the same machine.
   - Maintained byte-level parity between `scripts/run-remote-executor.sh` and `pkg/ui/executor_install.sh`.
3. **End-to-End Test Suite (`tests/e2e/remote_executors_relay_e2e_test.go`)**:
   - Validated rendezvous relay reverse tunneling (`pkg/relay`), multi-executor registration with distinct concurrency bounds (Worker Alpha with concurrency 2, Worker Beta with concurrency 4).
   - Validated least-loaded scheduling across multi-executor pool distributing tasks in proportion to capacity (2 tasks on Alpha, 4 tasks on Beta).
   - Validated instance ID deduplication when duplicate instance IDs reconnect.
   - Validated per-executor token revocation via `store.RevokeExecutorToken` / `srv.RevokeToken`, verifying immediate eviction and rejection of reconnects.
   - Validated multiplexed stream half-close and EOF recovery.

## Walkthrough Scenarios & Results

### Walkthrough Phases

1. **Phase 1: Launch Rendezvous Relay Service (`grain relay`)**:
   - Spawned relay server on ephemeral TCP port with healthcheck endpoint at `/relay/healthz`.
2. **Phase 2: Launch Grain Daemon Connected via Relay**:
   - Started isolated `grain daemon` configured with `-relay-url` and `-relay-token`.
   - Reverse tunnel established from daemon to relay server.
3. **Phase 3: Issue Scoped Executor Tokens via API**:
   - Created per-executor tokens for `worker-alpha` and `worker-beta` via `POST /api/executors/tokens`.
   - Verified created token hashes and metadata records in store.
4. **Phase 4: Connect Remote Executors via Rendezvous Relay**:
   - Started `scripts/grain-remote-executor.sh` for Worker Alpha (`--concurrency 2`, `--instance-id inst-alpha-001`) and Worker Beta (`--concurrency 4`, `--instance-id inst-beta-002`) over relay reverse tunnel.
   - Verified both executors registered with correct capacities, instance IDs, and team tags in `GET /api/executors`.
5. **Phase 5: Revoke Executor Token & Verify Instant Node Disconnection**:
   - Revoked Worker Alpha token via `POST /api/executors/tokens/:id/revoke`.
   - Verified instant disconnection of Worker Alpha while Worker Beta remained connected and healthy.
   - Verified that Worker Alpha's reconnect attempts were actively rejected with `authentication failure: token revoked`.

### Execution Summary

```
========================================================
PHASE 1: Launch Rendezvous Relay Service (grain relay)
========================================================
[grain-relay] listening on port 8090
Rendezvous relay verified healthy at http://127.0.0.1:8090/relay/healthz

========================================================
PHASE 2: Launch Grain Daemon Server Connected via Relay
========================================================
Grain daemon running with UI/API at http://127.0.0.1:39999
[grain-relay] daemon reverse tunnel established

========================================================
PHASE 3: Issue Scoped Executor Tokens via API
========================================================
Token Alpha creation status: 201 (ID=exec-tok-1, Team=ai-team)
Token Beta creation status: 201 (ID=exec-tok-2, Team=ai-team)
GET /api/executors/tokens status: 200, count: 2

========================================================
PHASE 4: Connect Remote Executors via Rendezvous Relay
========================================================
Starting Worker Alpha via Relay (--relay-url http://127.0.0.1:8090, -c 2, --instance-id inst-alpha-001)
Starting Worker Beta via Relay (--relay-url http://127.0.0.1:8090, -c 4, --instance-id inst-beta-002)
[grain-relay] executor stream bridged to daemon (Worker Alpha)
[grain-relay] executor stream bridged to daemon (Worker Beta)
Connected executors count: 2
- inst-alpha-001 (host: worker-alpha, concurrency: 2, team: ai-team)
- inst-beta-002 (host: worker-beta, concurrency: 4, team: ai-team)

========================================================
PHASE 5: Revoke Executor Token & Verify Instant Node Disconnection
========================================================
Revoking Token Alpha (exec-tok-1)...
Revocation status: 200
[grain-relay] executor stream disconnected (Worker Alpha)
modelexec: authentication failure from stream (token revoked)
Remaining connected executors: 1 (Worker Beta: inst-beta-002, concurrency: 4)

========================================================
WALKTHROUGH COMPLETED SUCCESSFULLY!
========================================================
```

## Test Suite Results

- **Go Test Suite (`go test ./...`)**:
  - All packages passed (`cmd/...`, `pkg/...`, `tests/...`), including `tests/e2e/remote_executors_relay_e2e_test.go` and `tests/deploy/run_remote_executor_test.go`.
- **UI Test Suite (`ui/ npm test`)**:
  - All 141 test files and 2,514 tests passed with zero failures.
