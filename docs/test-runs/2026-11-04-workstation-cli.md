# Workstation CLI Walkthrough (`docs/test-plans/2026-11-04.md` §5)

- **Date:** 2026-11-04, 21:28–23:00 UTC
- **Task:** `#3071` (`grain/049f1e09/task-3071-walkthrough-workstation-cli`, origin task `#2958`, base `main`)
- **Base commit tested:** `40e2820f5325` (`origin/main` at start of run)
- **Environment:**
  - Debian sandbox VM (`linux/amd64`), Go `1.26.x`, Node.js `v20.x`, `git` (`git http-backend` CGI), `bubblewrap` (`bwrap`)
  - Embedded UI built per Shared Step 1: `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`
  - Source tree staged at `/usr/local/share/grain/src` via `scripts/export-source.sh` (7,090 files; 3,510 under `pkg/`) so `src://` served real repository files
  - Live servers exercised by [`docs/test-runs/2026-11-04-workstation-cli/walkthrough.go`](2026-11-04-workstation-cli/walkthrough.go) (full structured run output in [`docs/test-runs/2026-11-04-workstation-cli/results.json`](2026-11-04-workstation-cli/results.json)):
    1. **Real `grain daemon`** (`grain init workstation -non-interactive -sandbox-backend host` + generated `daemon.sh -github-host <loopback> -github-insecure-http -sandbox-port-base=0 -poll-interval 1s`) running the real SQLite WAL store, `state-repo` sync, `gitproxy` sandbox proxy, `bwrap` host sandboxes, in-sandbox `grain` CLI, and grain CI runner over `.grain/workflows/ci.yml`.
    2. **Multi-repo `githubsim` + `git http-backend` CGI forge** serving real bare repositories `acme/widgets` (`ci_mode: grain`) and `acme/other` (`ci_mode: github` with seeded Actions runs `101..104`).
    3. **`grain demo -github-sim -fake-ci -scheduled-workflows -merge-queue-states -git-proxy`** for `fake-ci` runs, demo merge-queue states, and `grain health` layer counters.
    4. **Simulated IAP (`tests/iap/iapsim.go`)** in front of `grain demo -ui-iap-audience ... -ui-iap-issuer ... -ui-iap-keys-url ...`, exercising both `401 IAP: sign-in required` on `/api/...` and `302 -> /_iapsim/login` HTML sign-in page responses.
    5. **TLS mismatch endpoints:** loopback `httptest.NewTLSServer` (self-signed certificate) in front of the live daemon, tested in all three mismatch directions (`https://` client to plain `http://` daemon, `https://` client to untrusted TLS server, and `http://` client to `https://` server).

## Stubs & Boundaries

Every server, SQLite store, HTTP route, `agentAPIGate` check, `pkg/namespace` mount, `gitproxy` `/fs/` handler, and CLI binary was real. Only unavailable external boundaries were simulated:

1. **GitHub REST/GraphQL & Git hosting** (sandbox has no external GitHub credentials): `pkg/github/githubsim` paired with `githubsim.GitUpstream` + `git http-backend` over real bare git repos (`acme/widgets.git`, `acme/other.git`).
2. **Cloud IAP** (no Cloud Run / GCP credentials in sandbox): `tests/iap/iapsim.go` from Shared Step 5 (`/_iapsim/login` HTML sign-in page + ES256 JWKS issuer).
3. **Agent LLM provider** (for capturing in-sandbox `grain` CLI output over the real `127.0.0.1:<sandbox-port>` proxy in Item 3): a scripted `agy` binary emitting valid stream-JSON events while executing real `grain ls`, `grain read`, and `grain grep` commands inside the `bwrap` host sandbox.
4. **Deterministic `503 Service Unavailable` probe** (for verifying 5xx → exit `1` + structured JSON error once `grain demo`'s own accidental 503 was fixed): a 4-line loopback `httptest.Server` returning HTTP 503 `{"error":"the service is temporarily unavailable"}`.

---

## Walkthrough Results (`docs/test-plans/2026-11-04.md` §5 Items 1–6)

### 1. Workstation CLI Exit Codes `0..5` and `-json` Error Contract

All commands below were executed against the live `grain daemon` (and IAP / TLS / 503 harnesses where noted). Under `-json`, every error case was validated to parse as `{"error":{"code":...,"message":...}}` on `stderr` with empty `stdout`, except `target_failed` (`exit 1` on settled failed `ci wait` or truncated 0-match `fs grep`) where `stdout` carries the valid JSON result document and `stderr` is empty (`#XC-13`). Note: on `exit 5` (`ci wait -timeout 5s -json`), `stdout` carries the `"status": "running"` result JSON and `stderr` carries `{"error":{"code":"wait_timeout",...}}`, both valid JSON objects.

| Exit | Scenario | Command | Expected | Observed (Before → After) | Output Excerpt (`stdout` / `stderr`) |
|---:|---|---|---:|---:|---|
| `0` | Task listing (text) | `grain list` | `0` | `0 → 0` | `stdout`: `105    completed       acme/other ...` / `stderr`: `1 closed not shown -- pass -closed to include them` |
| `0` | Task listing (`-json`) | `grain list -json` | `0` | `0 → 0` | `stdout`: `[{"id":"105","title":"Track other#9003 in merge queue",...}]` (`stderr` empty) |
| `0` | Green CI run | `grain ci wait <green-id>` | `0` | `0 → 0` | `stdout`: `grain CI passed: run ca3a781c8a8a65c6 on acme/widgets feat/green at ... (waited 0s): lint, unit` |
| `1` | Red CI run (text) | `grain ci wait <red-id>` | `1` | `1 → 1` | `stdout`: `grain CI failed: run 2b5715c6d63f3bf1 ... ✗ unit tests (failure, step Run tests)` / `stderr`: `grain: run 2b5715c6d63f3bf1: failure` |
| `1` | Red CI run (`-json`) | `grain ci wait <red-id> -json` | `1` | `1 → 1` | `stdout`: `{"status":"failed","run_id":"2b5715c6d63f3bf1","conclusion":"failure",...}` (`stderr` empty) |
| `1` | Cancelled CI run | `grain ci wait <cancelled-id>` | `1` | `1 → 1` | `stdout`: `grain CI failed: ... cancelled (waited 0s)` / `stderr`: `grain: run 9dafee98db796087: cancelled` |
| `1` | Truncated walk grep (0 matches, text) | `grain fs grep __never_occurs_walk_cap__ src://walkcap_fixture` | `1` | `1 → 1` | `stdout`: `[truncated: walked 20000 entries and stopped at ...]` / `stderr`: `grain: no match in the part of src://walkcap_fixture searched before the walk cap...` |
| `1` | Truncated walk grep (0 matches, `-json`) | `grain fs grep __never_occurs_walk_cap__ src://walkcap_fixture -json` | `1` | `1 → 1` | `stdout`: `{"uri":"src://walkcap_fixture","output":"[truncated: walked 20000 entries...]"}` (`stderr` empty) |
| `1` | Daemon `5xx` (`-json`) | `grain -server <503-stub> list -json` | `1` | `1 → 1` | `stderr`: `{"error":{"code":"unavailable","message":"the service is temporarily unavailable"}}` (`stdout` empty) |
| `2` | Unknown flag (`-json`) | `grain list -no-such-flag -json` | `2` | `2 → 2` | `stderr`: `{"error":{"code":"invalid_flag","message":"flag provided but not defined: -no-such-flag"}}` (`stdout` empty) |
| `2` | Stray positional argument | `grain list strayword` | `2` | `2 → 2` | `stderr`: `grain: grain list takes no arguments, so "strayword" would be ignored...` |
| `2` | Unknown command (`-json`) | `grain nosuchverb -json` | `2` | `2 → 2` | `stderr`: `{"error":{"code":"unknown_command","message":"unknown command \"nosuchverb\"","fix":"grain -h"}}` |
| `2` | `404` task ID (`-json`) | `grain get 999999 -json` | `2` | `2 → 2` | `stderr`: `{"error":{"code":"not_found","message":"no task 999999"}}` (`stdout` empty) |
| `2` | `404` CI run ID (`-json`) | `grain ci show no-such-run -json` | `2` | `2 → 2` | `stderr`: `{"error":{"code":"not_found","message":"no CI run \"no-such-run\"..."}}` (`stdout` empty) |
| `2` | Missing `fs` subcommand (`-json`) | `grain fs -json` | `2` | **`2` (dirty stdout) → `2`** (F2) | `stderr`: `{"error":{"code":"usage","message":"grain fs needs a command: ls, cat, read or grep"}}` (`stdout` empty after fix) |
| `3` | Invalid bearer token (`-json`) | `grain -token gma_invalid list -json` | `3` | `3 → 3` | `stderr`: `{"error":{"code":"unauthenticated","message":"the grain daemon ... refused this CLI's credentials..."}}` |
| `3` | Expired `gma_` token (`-json`) | `grain -token <expired-gma> list -json` | `3` | `3 → 3` | `stderr`: `{"error":{"code":"unauthenticated","message":"the grain daemon ... refused this CLI's credentials..."}}` |
| `3` | Forbidden scope (`403`, `-json`) | `grain -token <ci:read> create -title x -json` | `3` | `3 → 3` | `stderr`: `{"error":{"code":"forbidden","message":"external:ci-widgets holds no api grant for scope tasks:write..."}}` |
| `3` | IAP `/api/` `401` (`iapsim`, `-json`) | `grain -server <iapsim-proxy> list -json` | `3` | `3 → 3` | `stderr`: `{"error":{"code":"unauthenticated","message":"... (grain server: 401 Unauthorized (401 Unauthorized))"}}` |
| `3` | IAP HTML sign-in page (`/_iapsim/login`, `-json`) | `grain -server <iap-login-redirect> list -json` | `3` | `3 → 3` | `stderr`: `{"error":{"code":"login_required","message":"... unexpected HTML sign-in response from .../_iapsim/login..."}}` |
| `4` | Wrong / closed port (`-json`) | `grain -server <unused-port> -connect-timeout 600ms list -json` | `4` | `4 → 4` | `stderr`: `{"error":{"code":"daemon_unreachable","message":"no grain daemon answered at ... (connect: connection refused)"}}` |
| `4` | TLS mismatch (`https://` → `http://`, `-json`) | `grain -server https://<http-daemon> list -json` | `4` | **`1` → `4`** (F3) | `stderr`: `{"error":{"code":"daemon_unreachable","message":"... http: server gave HTTP response to HTTPS client"}}` |
| `4` | TLS mismatch (untrusted self-signed cert, `-json`) | `grain -server https://<self-signed> list -json` | `4` | `4 → 4` | `stderr`: `{"error":{"code":"daemon_unreachable","message":"... tls: failed to verify certificate: x509: certificate signed by unknown authority"}}` |
| `4` | TLS mismatch (`http://` → `https://`, `-json`) | `grain -server http://<https-server> list -json` | `4` | **`2` → `4`** (F3) | `stderr`: `{"error":{"code":"daemon_unreachable","message":"tls: Client sent an HTTP request to an HTTPS server."}}` |
| `5` | `ci wait -timeout 5s` on slow run (text) | `grain ci wait <slow-id> -timeout 5s` | `5` | `5 → 5` | `stdout`: `grain CI run ... is still running after 5s (jobs running: slow; passed: none)` / `stderr`: `grain: run ... is still running after 5s` |
| `5` | `ci wait -timeout 5s -json` on slow run | `grain ci wait <slow-id> -timeout 5s -json` | `5` | `5 → 5` | `stdout`: `{"status":"running","run_id":"...","resume_run_id":"...",...}` / `stderr`: `{"error":{"code":"wait_timeout","message":"run ... is still running after 5s"}}` |

---

### 2. Global Flags After the Verb & `-as` Inside `grain comment`

Verified against the live daemon:

1. `grain list --json` → exit `0`, valid JSON task array on `stdout` (`liftGlobalFlags` accepts two-dash `--json` after the verb).
2. `grain ci show <green-id> -json` → exit `0`, valid `ci.Run` JSON object on `stdout` (`liftGlobalFlags` lifts `-json` after nested `ci show <id>`).
3. `grain comment <id> "use -as carefully"` and unquoted `grain comment <id> use -as carefully` → both exited `0` without lifting `-as` as the global impersonation flag. Reading back `grain get <id> -json` confirmed two comments stored with exact body `"use -as carefully"`.

---

### 3. `grain fs` Scope Matrix and Byte-for-Byte Sandbox CLI Parity

Scoped `gma_` tokens were minted via `POST /api/external-agents/tokens` (backing [Settings → Agents](/settings/agents)):

| Token (`gma_`) | Command | Expected | Observed (Before → After) | Result |
|---|---|---:|---:|---|
| `ci:read@acme/widgets` | `grain fs cat ci://acme/widgets/<red>/unit/Test.log` | `0` | `0 → 0` | Prints `=== RUN   TestWidgetParse ...` |
| `ci:read@acme/widgets` | `grain fs ls ci://acme/other` | `3` | `3 → 3` | `grain: external:ci-widgets holds ci:read, but not on acme/other` |
| `ci:read@acme/widgets` | `grain fs cat ci://acme/other/gha:acme:other:102/build/job.log -json` | `3` | `3 → 3` | `{"error":{"code":"forbidden","message":"external:ci-widgets holds ci:read, but not on acme/other"}}` |
| `ci:read@acme/widgets` | `grain fs ls ci://` | `0` | `0 → 0` | Narrowed to `acme/widgets/` (`acme/other/` omitted) |
| `memory:read@acme/widgets` | `grain fs cat memory://repos/acme/widgets.md` | `0` | `0 → 0` | Prints `# Widgets Memory` |
| `memory:read@acme/widgets` | `grain fs cat memory://repos/acme/widgets/arch.md` | `0` | `0 → 0` | Prints `# Widgets Architecture` |
| `memory:read@acme/widgets` | `grain fs cat memory://repos/acme/other.md` | `3` | `3 → 3` | `grain: external:mem-widgets holds memory:read, but not on acme/other` |
| `memory:read@acme/widgets` | `grain fs cat memory://repos/acme/other/notes.md` | `3` | `3 → 3` | `grain: external:mem-widgets holds memory:read, but not on acme/other` |
| `memory:read@acme/widgets` | `grain fs ls memory://repos/acme` | `0` | **`3` → `0`** (F4) | Lists `widgets/` only (`other/` omitted) |
| `memory:read@acme/widgets` | `grain fs grep . memory://` | `0` | **partial → `0`** (F4) | Greps `global` + `repos/acme/widgets/*` without leaking `acme/other` |
| `memory:read@{acme/widgets,acme/other}` | `grain fs ls memory://repos/acme` | `0` | **`3` → `0`** (F4) | Lists `other/` and `widgets/` |
| `grain/external-dev` (no `deployment:src`) | `grain fs ls src://` | `3` | `3 → 3` | `external:ext-dev holds no api grant for scope deployment:read...` |
| `deployment:read` (no `deployment:src`) | `grain fs cat src://README.md -json` | `3` | `3 → 3` | `{"error":{"code":"forbidden","message":"external:deploy-nosrc may not read src://: an external token reads grain's source only when it is also granted deployment:src"}}` |
| `deployment:read` + `deployment:src` | `grain fs cat src://README.md` | `0` | `0 → 0` | Prints staged `/usr/local/share/grain/src/README.md` |

#### Sandbox CLI vs Workstation `grain fs` Byte-for-Byte Parity

A real task (`acme/widgets`) was dispatched on the `grain daemon` into a `bwrap` host sandbox and ran the in-sandbox `grain` CLI (`pkg/sandboxcli` over `http://127.0.0.1:<proxy>/fs/`) while workstation `grain fs` ran externally against `GET /api/fs/` for the exact same URIs (including a live growing CI step log for `--follow` / `?follow=1` and raw HTTP `Range: bytes=8-39` `206 Partial Content`):

| Check | In-Sandbox Command (`/fs/`) | Workstation Command (`/api/fs/`) | Bytes | Byte-Identical |
|---|---|---|---:|:---:|
| Directory listing (`ls`) | `grain ls ci://acme/widgets/<red>` | `grain fs ls ci://acme/widgets/<red>` | `12` | Yes |
| Inline `?tail=45` | `grain read 'ci://.../unit/Test.log?tail=45'` | `grain fs cat 'ci://.../unit/Test.log?tail=45'` | `45` | Yes |
| Flag `--tail 45` (`read` alias) | `grain read ci://.../unit/Test.log --tail 45` | `grain fs read ci://.../unit/Test.log --tail 45` | `45` | Yes |
| Byte slice (`--offset 8 --limit 32`) | `grain read ci://.../unit/Test.log --offset 8 --limit 32` | `grain fs cat ci://.../unit/Test.log --offset 8 --limit 32` | `32` | Yes |
| Subtree `grep` | `grain grep FAIL ci://acme/widgets/<red>` | `grain fs grep FAIL ci://acme/widgets/<red>` | `215` | Yes |
| Inline `?grep=FAIL` | `grain read 'ci://acme/widgets/<red>?grep=FAIL'` | `grain fs cat 'ci://acme/widgets/<red>?grep=FAIL'` | `215` | Yes |
| Live growing log (`?follow=1` / `--follow`) | `grain read 'ci://.../unit/Test.log?follow=1'` | `grain fs cat --follow ci://.../unit/Test.log` | `138` | Yes |
| HTTP `Range: bytes=8-39` (`206`) | `GET /fs/ci/.../unit/Test.log` (`Range: bytes=8-39`) | `GET /api/fs/ci/.../unit/Test.log` (`Range: bytes=8-39`) | `32` | Yes |

---

### 4. External `grain ci wait`, `ci retrigger`, `ci test-history`, and `pr status`

Tested across all three CI backends (`githubsim` Actions on `acme/other`, `fake-ci` on `grain demo`, and real `.grain/workflows/ci.yml` grain CI on `acme/widgets`):

1. **`githubsim` Actions runs (`gha:acme:other:101..104`)** (after fixing F5, F6, F7):
   - `grain ci wait gha:acme:other:101` (and `-json`) → exit `0` (`passed: build`).
   - `grain ci wait gha:acme:other:102` (and `-json`) → exit `1` (`✗ build (failure, step Run tests)`, `failing tests: TestOtherAction`).
   - `grain ci wait gha:acme:other:103` → exit `1` (`cancelled`).
   - `grain ci wait gha:acme:other:101 -jobs nonexistent` and 0-job `grain ci wait gha:acme:other:104 -jobs nonexistent` → both exit `2` (`run ... has no job "nonexistent"` — never green).
   - `grain ci wait -pr acme/other#9001` → exit `0`; `grain ci wait -pr https://github.com/acme/other/pull/9002` → exit `1`.
   - `grain ci wait -ref feat/green -repo acme/other` → exit `0`; `grain ci wait -ref feat/red -repo acme/other` → exit `1`.
   - `grain ci retrigger gha:acme:other:102` → exit `2` with explicit message explaining `grain ci retrigger` applies to grain CI runs.
2. **`fake-ci` runs on `grain demo -github-sim -fake-ci -scheduled-workflows`** (after fixing F7):
   - Green (`main`), red (`release/1.0`), and cancelled runs exited `0`, `1`, and `1`.
   - `grain ci wait <fake-ci-green> -jobs nonexistent` → exit `2` (`has no job "nonexistent"`).
   - `grain ci wait -ref release/1.0 -repo acme/widgets` → exit `1`; `grain ci wait -pr acme/widgets#104` → exit `0`.
3. **Real grain CI runs on `grain daemon` (`acme/widgets`)**:
   - `grain ci wait -pr acme/widgets#501` → exit `0`; `grain ci wait -ref feat/red -repo acme/widgets` → exit `1`.
   - Selective `grain ci wait <red-run> -jobs lint` (where `lint` passed and `unit` failed) → exit `0` with selective-verdict notice.
   - `grain ci wait <green-run> -jobs nonexistent` → exit `2`.
   - **`grain ci retrigger <red-run> -json`**: exited `0`, started a new run with `recheck: true` whose settled `Jobs` slice contained **only `["unit"]`** (skipping passing `lint`), once. `grain ci retrigger <green-run>` exited `2` (no failed jobs).
   - **`grain ci test-history TestWidgetParse -repo acme/widgets`** (text and `-json`): exited `0`, reporting `TestWidgetParse` pass/fail counts and recent run verdicts across the grain CI runs.
   - **`grain pr status acme/widgets#501`** on the live `grain daemon` (after fixing F8): exited `0`, reporting head SHA, open status, and check state.

---

### 5. `grain merge-queue` (Operator vs One-Repo `ci:read`, Daemon & Demo)

1. **Operator (`grain daemon`)**:
   - `grain merge-queue` (text) and `grain merge-queue -json` exited `0`, showing `acme/other` Task `105` (`waiting_ci`, waiting on `build`; `acme/widgets#502` had already been merged automatically by the live merge queue).
2. **One-repo `ci:read@acme/widgets` token (`grain daemon`, after fixing F1)**:
   - `grain merge-queue` (text) exited `0` (`The merge queue is currently idle and clear.`, omitting `acme/other` Task `105`).
   - `grain merge-queue -json` exited `0` (`{}`).
   - `grain merge-queue -repo acme/other -json` exited `0` (`{"repo":"acme/other"}` with zero batches, queued PRs, or gates — `-repo` cannot widen a repo-limited `ci:read` token).
   - Positive inclusion + exclusion (`acme/widgets` included while `acme/secret` omitted) is pinned in `TestInProcessAPIDetachesExternalTokenContextOnMergeQueue` (`pkg/daemon/sandbox_namespace_test.go`).
3. **`grain demo -merge-queue-states` (after fixing F9)**:
   - `grain merge-queue` exited `0`, rendering in-flight batch `grain-merge-queue/d40bd848/recheck/5/main` and queued PRs `#101`, `#102`, `#103`.

---

### 6. `grain health` on `grain demo`

Executed against `grain demo` (which registers `sign-in verification (demo)` via `ui.RegisterHealthCounters`):

- **Text mode (`grain health`)**: exited `0` with counter keys in strict lexicographical order:
  ```text
  counters since the daemon started:

  sign-in verification (demo)
    proxy:admitted              49
    proxy:admitted_cached       212
    proxy:host_mismatch         1
    proxy:outside_window        3
    proxy:verifier_refused      2
    proxy:verifier_unreachable  1
  ```
- **JSON mode (`grain health -json`)**: exited `0` with valid JSON `{"enabled":false,"counters":[{"name":"sign-in verification (demo)","counters":{...}}]}` and empty `stderr`.

---

## Findings, Root Causes, and Fixes

### Fixed in This Branch (`F1`–`F10`)

1. **`F1` — `grain merge-queue` with a `ci:read` external token failed with HTTP `500` (exit `1`) on `grain daemon`**
   - **Reproduction:** Mint a `gma_` token with `ci:read@acme/widgets` and run `grain merge-queue` against a real `grain daemon`. Before fix, exited `1` with `grain server: Failed to query merge queue: reading pipeline pause: external:ci-widgets holds no api grant for scope dispatch:read, which GET /api/pipeline-pause needs (500 Internal Server Error)`.
   - **Root cause:** `inProcessAPI.RoundTrip` and `inProcessAPI.ServeHTTP` (`pkg/daemon/sandbox_namespace.go`) stripped `Authorization` and `RemoteAddr` so internal namespace subrequests (`GET /api/pipeline-pause`, `GET /api/tasks`, `GET /api/merge-batches`) would run as the deployment's own actor, but `req.Clone(req.Context())` preserved the outer HTTP request's `externalToken` and `agentCall` context values when invoked in-process from `GET /api/fs/deploy/merge-queue`.
   - **Fix & test:** Wrapped the cloned context in `cancelOnlyContext` (`pkg/daemon/sandbox_namespace.go`) so deadlines/cancellation propagate while outer auth context values do not cross the in-process HTTP boundary (`RepoFilteredMergeQueue` continues to filter the report to `q.Permits`). Pinned by `TestInProcessAPIDetachesExternalTokenContextOnMergeQueue` in `pkg/daemon/sandbox_namespace_test.go`.

2. **`F2` — `grain fs -json` wrote plain-text usage to `stdout` on error**
   - **Reproduction:** Run `grain fs -json` with no subcommand. Before fix, exited `2` with JSON error on `stderr` **and** 12 lines of plain-text `usage: grain fs ...` on `stdout`, violating `#XC-13` ("stdout MUST remain empty" on error).
   - **Fix & test:** In `runFS` (`pkg/cli/fscmd.go`), wrote `fsUsage` to `stderr` (captured/suppressed by `holdStderr` when `-json` is active) when `len(args) == 0`, reserving `stdout` for `-h` / `--help` / `help`. Pinned by `TestGrainFsMissingSubcommandJSONKeepsStdoutEmpty` in `pkg/cli/fscmd_test.go`.

3. **`F3` — TLS protocol mismatches (`https://` → `http://` and `http://` → `https://`) exited `1` and `2` instead of `4`**
   - **Reproduction:** Point `grain -server https://<http-daemon> list` (exited `1` with `"http: server gave HTTP response to HTTPS client"`) or `grain -server http://<https-server> list` (exited `2` with `"grain: 400 Bad Request"` from Go `crypto/tls`'s `"Client sent an HTTP request to an HTTPS server."`).
   - **Fix & test:** Added `"server gave HTTP response to HTTPS client"` to `transportError` in `pkg/cli/errors.go`, and classified HTTP 400 plain-HTTP-to-HTTPS responses as `tls: ...` transport errors in `pkg/ui/httpclient.go`. All three TLS mismatch directions now exit `4` (`daemon_unreachable`). Pinned in `pkg/cli/errors_test.go`.

4. **`F4` — `grain fs ls memory://repos/<owner>` and `grain fs grep <re> memory://` failed or skipped allowed repos for repo-limited `memory:read` tokens**
   - **Reproduction:** Mint `gma_` with `memory:read@acme/widgets` (or `@acme/widgets,@acme/other`) and run `grain fs ls memory://repos/acme`. Before fix, exited `3` (`forbidden: memory://repos/acme`) because `fsScope` (`pkg/ui/api_fs.go`) only set `scope.TaskRepo = call.repo`, which is empty whenever the URI has fewer than 3 path segments (`memory://`, `memory://repos`, `memory://repos/acme`).
   - **Fix & test:** Added `MemoryReadRepos map[string]bool` to `namespace.Scope` (`pkg/namespace/namespace.go`), populated it from `set.Repos("memory:read")` in `pkg/ui/api_fs.go`, and checked `MemoryReadRepos` alongside `TaskRepo` in `Scope.PermitsMemory` (`pkg/namespace/stage2.go`). Pinned in `TestAPIFsPerSchemeScopesRepoLimitsAndExternalSrcRefusal` (`pkg/ui/api_fs_test.go`).

5. **`F5` — `pkg/github/githubsim` panicked on `GET /repos/{owner}/{name}/actions/runs/{id}`**
   - **Reproduction:** Run `grain ci wait gha:acme:other:101` against `githubsim`. Before fix, `Sim.Request` panicked (`unhandled request GET /repos/acme/other/actions/runs/101`), returning HTTP `500`.
   - **Fix & test:** Added `actionsRunRe` (`GET /repos/{owner}/{name}/actions/runs/{id}`) to `pkg/github/githubsim/githubsim.go`, returning the matching run from `repoRunsJSON` or `404`. Pinned in `pkg/github/githubsim/githubsim_test.go`.

6. **`F6` — Operator `grain ci wait -pr` and `-ref` missed GitHub Actions runs (`exit 2: no grain CI run ...`)**
   - **Reproduction:** As an operator (`PrincipalHuman`), run `grain ci wait -pr acme/other#9001` or `grain ci wait -ref feat/green -repo acme/other` on a repository with `ci_mode: github`. Before fix, exited `2` because `GET /api/ci/runs` without `provider=github-actions` omits Actions runs for human actors (`wantsActions` in `pkg/ui/ci_actions.go` keeps the browser CI pane on grain runs).
   - **Fix & test:** Updated `resolveCIWaitRun` (`pkg/cli/cicmd.go`) to fall back to `Provider: ci.ProviderGitHubActions` when no grain CI run matches the branch. Pinned by `TestExternalCIWaitActionsFallbackAndZeroJobMissingJobName` in `pkg/cli/cicmd_test.go`.

7. **`F7` — `grain ci wait <id> -jobs <missing>` exited `1` instead of `2` on settled 0-job runs (`fake-ci`, 0-job Actions runs)**
   - **Reproduction:** Run `grain ci wait <fake-ci-green> -jobs nonexistent`. Before fix, `len(run.Jobs) > 0` skipped the `usageErrorf` check on 0-job runs and fell through to `WaitVerdict`, exiting `1` (`failure`) instead of `2` (`usage`).
   - **Fix & test:** Checked `(len(run.Jobs) > 0 || run.Conclusion.Done())` before `WaitVerdict` in `pkg/cli/cicmd.go`, preserving exit `5` if a wait times out before jobs populate while returning exit `2` on settled runs lacking the named job. Pinned in `pkg/cli/cicmd_test.go`.

8. **`F8` — `PullRequestGate` on `grain daemon` did not forward `ui.PullRequestAdopter` (`grain pr status|adopt|review`)**
   - **Reproduction:** Run `grain pr status acme/widgets#501` against a live `grain daemon`. Before fix, exited `2` (`adopting pull requests is not available: this deployment's UI is not wired to a GitHub client`) because `startUIServer` passes `PullRequestGate{}`, whereas only `*PullRequestOpener` implemented `ui.PullRequestAdopter`.
   - **Fix & test:** Implemented `AdoptPullRequest`, `ReviewPullRequest`, and `PullRequestStatus` on `PullRequestGate` (`pkg/daemon/pull_requests.go`). Pinned in `pkg/daemon/sandbox_namespace_test.go` and verified live in `walkthrough.go`.

9. **`F9` — `grain demo` never wired `SetNamespace` on `ui.Server`, causing HTTP `503` on `grain merge-queue` and `grain fs`**
   - **Reproduction:** Start `grain demo -merge-queue-states` (with or without `-git-proxy`) and run `grain merge-queue` or `grain fs ls ci://`. Before fix, `ui.Server` returned `503 the service is temporarily unavailable`.
   - **Fix & test:** Wired `uiSrv.SetNamespace(...)` in `pkg/demo/demo.go` and `pkg/demo/demo_gitproxy.go` both with and without `-git-proxy` (including `ExternalCIRuns` on `ci://`). Pinned by `TestDemoUIServerServesAPIFsAndMergeQueueWithoutGitProxy` in `pkg/demo/demo_gitproxy_test.go`.

10. **`F10` — Stale prose in `design/spec/cli/cli.spec.md` (`#CLI-10` and `#CLI-11`)**
    - **Fix:** Updated `#CLI-10` (which still stated task CLI exits `1` on all errors including unknown flags and `-h`) and `#CLI-11` item 4 (which stated global flags must precede the command) to reflect exit codes `0..5` (`#XC-13`), `-h` exiting `0`, and `liftGlobalFlags` (`#XC-11`). No CLI flag definitions changed, and `go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` passes cleanly.

### Filed as Follow-Up Proposal (`F11`)

- **`F11` — Quadratic $O(N^2)$ `ReadMemoryLayer` fetches during `memory://` subtree `grep` / `glob` walks:** During walk-cap testing with 2,050 documents in a single `memory://` repository layer, `grain fs grep <re> memory://repos/<owner>/<repo>` timed out (>45s) because `resolveMemory` (`pkg/namespace/stage2.go`) calls `s.Memory.ReadMemoryLayer(ctx, repo)` (which fetches and decodes all $N$ documents over `GET /api/memory/...`) once for the directory listing and then **once per document** across all $N$ documents ($4.2 \times 10^6$ document marshals/unmarshals at $N = 2050$). Filed as a follow-up task via `propose_task` to resolve single documents without re-fetching the entire layer on every file open.

### Merge-Queue CI Repair (Pre-existing `installer` Race on `main`)

- **`TestGoogleTrackRunsAgyThroughHostdAndMigratesFromHostMode/a_stuck_host_run_is_cancelled_at_the_drain_cap` (`zz_hostd_test.go:789`):** On PR `#725`, all 24 other checks passed while `installer` failed waiting 30s for `hostd` to start cutting over after `grain-hostd-upgrade.service` started and exited `0` (`Deactivated successfully`) 1s later with zero drain log lines.
  - **Root cause:** In `scripts/setup.sh`, `install_hostd_version` ran `install -d -m0755 "$dir"` *before* taking `flock 9` on `$dir.lock`. Even when `$dir` (`/usr/local/lib/grain/hostd`) already existed, `install -d -m0755` fired an `IN_ATTRIB` event on `$dir`, waking `grain-hostd-upgrade.path` before the lock was held. `hostd-upgrade.sh` read the old `current` symlink (`running == current`), released `flock 9`, and spent ~1s running `systemctl start grain-hostd-allowlist.service`; meanwhile `install_hostd_version` moved `current` to `next` and ran `systemctl start --no-block grain-hostd-upgrade.service`, which systemd coalesced into the already-activating `oneshot` job. `hostd-upgrade.sh` then resumed with its stale `current` value and exited `0` without draining `hostd`.
  - **Fix & test:** Acquired `flock 9` on `$dir.lock` before `install -d -m0755 "$dir"` in `install_hostd_version`, and updated `write_hostd_upgrade_script` (`scripts/setup.sh`) to re-read `current` in a loop until `current` settles across `grain-hostd-allowlist.service` and `hostd -drain`. Pinned in `TestHostdUpgradeScriptDrainsOnlyAHostdOnAnOldBinary` and `TestTheExtractorMovesCurrentUnderTheVersionsLock` (`tests/deploy/hostd_upgrade_test.go`).
