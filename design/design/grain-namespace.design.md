# Grain Namespace: REST File Hierarchy & Thin MCP Wrappers

## Why

A large share of what agents read from Grain is structured or streaming text: CI job and step logs, per-test output, task records, conversations, prompts, transcripts, Grain's own source tree, the deployment state repository, memory documents, and deployment health/metrics. Historically each resource had its own custom MCP read tool (`get_ci_log`, `list_ci_runs`, `read_grain_task`, `read_grain_task_prompt`, `read_grain_task_transcript`, `read_grain_source`, `list_grain_source`, `read_deployment_state`, `read_grain_metrics`, `read_deployment_health`, ...), each with bespoke pagination parameters, and all of them hit the MCP tool-result byte cap (`64 KiB`).

Evidence from production (`read_grain_metrics` tools section, 3 days to 2026-10-25):

- `list_ci_runs` answers were truncated **53%** of the time; `api_list_ci_runs` **83%**.
- There was no way to ask for a single test's output: `get_ci_log` only read raw byte ranges of a job or step log.
- Full `go test` output lived in `/tmp/gotest.log` inside the ephemeral CI sandbox while the step log kept only condensed highlights, and multiple `run-go-tests.sh` steps in the same job overwrote `/tmp/gotest.log`.
- **When a job was retried, only the retry's log survived.** On 2026-10-25, task 796's CI run `883a3337aa7850f7` had its `go` job fail with exit 1 in a `Test` step on attempt 1. The automatic retry (attempt 2) was cancelled past the job timeout bound, and the agent could not inspect which test had failed on attempt 1 (`"Only the retry's log survives"`), forcing it to re-run the e2e packages locally to rediscover the failure.
- Across base-branch repair passes (392 runs across 76 passes, median 2h50m to settle; `run_ci_job` used 7 times in 5 runs), agents routinely fell back to re-running whole CI suites instead of pinpointing the failing test from existing logs.

Serving a unified, grant-scoped, redacted file namespace over REST on the sandbox proxy lets agents use standard Unix tools (`curl`, `rg`, `jq`, `head`, `tail`, `diff`) inside the sandbox without pouring megabytes of raw logs through the model's context window, while thin MCP tools (`grain_ls`, `grain_read`, `grain_grep`, `grain_copy`) expose the exact same hierarchy for discoverability and for sessions without a sandbox.

---

## 1. Namespace Server in the Daemon

The daemon hosts a single `namespace.Server` (`pkg/namespace`) that resolves `<scheme>://<path>` URIs (and `/fs/<scheme>/<path>` HTTP paths) to directory listings, file streams, glob matches, and server-side regex searches. Grant scoping and secret redaction happen in one place in front of every backend resolver.

### 1.1 Roots

| Scheme | Addressable Hierarchy | Grant Requirement | Backing Store |
| :--- | :--- | :--- | :--- |
| `ci://` | `ci://<owner>/<repo>/<run>/<job>/[attempts/<n>/]<step>.log`<br>`ci://<owner>/<repo>/<run>/<job>/[attempts/<n>/]gotest.log`<br>`ci://<owner>/<repo>/<run>/<job>/[attempts/<n>/]tests/<pkg>/<Test>.log`<br>`ci://<owner>/<repo>/<run>/artifacts/<job>/<name>` | `api:ci-read` grant (every repository) or `api:ci-read@<owner>/<repo>` (that one), `self-debug` (every repository), or `grain-ci` for the task's own target / `reads:` / `writes:` repositories | `ci.RunStore` (`MemoryRunStore` / `StoredRunStore`) + `ci.ArtifactStore` (`DirArtifacts`) |
| `src://` | `src://<path>` (e.g. `src://pkg/ci/engine.go`) | `self-debug` capability (or explicit source read grant) | Running build's source directory (`GrainSourceDir`) bounded via `os.Root` |
| `task://` | `task://<id>/record.md`<br>`task://<id>/conversation.md`<br>`task://<id>/prompt.md`<br>`task://<id>/attempts/<n>/transcript.md`<br>`task://<id>/handover.md` | `self-debug` capability, or an `api:tasks:read` grant (`@owner/repo` limits it to tasks in that repository); a task always reads its own `task://<own-id>/` | `model.Store` (`tasks`, `task_comments`, `task_runs`, `run_transcripts`) |
| `config://` | `config://<path>` | `self-debug` capability or `reads:` grant on the state repository | Deployment state repository (`staterepo`) |
| `memory://` | `memory://<path>` | `memory` capability (scoped by deployment/repo) | Memory store (`pkg/memory`) |
| `deploy://` | `deploy://health.json`<br>`deploy://merge-queue.md`<br>`deploy://metrics/<section>.txt` | `self-debug` capability | Daemon health snapshot, merge queue state, and metrics collector |

*(First slice implements `ci://` and `src://`; `task://`, `config://`, `memory://`, and `deploy://` follow in the second slice.)*

### 1.2 Preserving Every CI Attempt

When a CI job has automatic retries (`retry.max_attempts > 1`) or step retries, every attempt is recorded as an immutable `ci.JobAttempt` (1-indexed: `attempts/1`, `attempts/2`, ...):

1. **Before releasing an attempt's sandbox**, the CI engine collects `/tmp/gotest.log` (accumulated across all `scripts/run-go-tests.sh` invocations in that attempt) alongside any declared job artifacts, attaches them to that attempt's `JobAttempt` record (and `ArtifactStore` under an attempt-qualified ref), and appends the `JobAttempt` to `JobRun.Attempts`.
2. **When a retry starts**, the engine snapshots the completed attempt into `JobRun.Attempts` before resetting the live `JobRun.Steps` slice for the next attempt. Earlier attempts' step logs, failure summaries, raw `gotest.log`, and per-test outputs are never overwritten or dropped.
3. **Addressing**:
   - `ci://<owner>/<repo>/<run>/<job>/attempts/<n>/<step>.log` addresses attempt `<n>` (1-indexed).
   - `ci://<owner>/<repo>/<run>/<job>/attempts/<n>/gotest.log` addresses attempt `<n>`'s full `go test` output.
   - `ci://<owner>/<repo>/<run>/<job>/attempts/<n>/tests/<pkg>/<Test>.log` addresses the extracted output for `<Test>` in package `<pkg>` from attempt `<n>`.
   - Unnumbered paths (`ci://<owner>/<repo>/<run>/<job>/<step>.log`, `gotest.log`, `tests/<pkg>/<Test>.log`) resolve to the latest attempt, with one deliberate fallback: if the latest attempt has no output for a requested test or raw log because it was cancelled before running tests while an earlier attempt failed, `FailingAttempt()` is surfaced in failure diagnostics and directly addressable at `attempts/<n>/...`.
4. **Retention**:
   - Attempt logs are persisted in `ci_job.attempts` (JSON column added in schema migration 38) and in `DirArtifacts` on disk.
   - `CIStepOutputUsage` and `PruneCIStepOutput` account for `LENGTH(CAST(j.attempts AS BLOB))` under the existing `ciHistory` and `ciArtifacts` storage budgets (`MaxStepOutputBytes`).
   - Within a run's per-run step-output ceiling (`maxStoredRunOutputBytes`), failed attempts (especially attempt 1 when a retry follows) are budgeted ahead of passing logs so that a failed first attempt's diagnostic output is never the first thing evicted.
5. **Failure reports in `wait_for_checks` and `get_ci_log`**:
   - When a job fails after retries—or when attempt 1 fails with a test failure and attempt 2 is cancelled or times out without producing a more specific failure summary—`wait_for_checks` and `get_ci_log` name the failing attempt (`attempt 1 of 2`) and read/point to `.../attempts/1/...` rather than only the retry's log.

### 1.3 Per-Test Log Extraction (`tests/<pkg>/<Test>.log`)

For Go test runs, `scripts/run-go-tests.sh` opens `/tmp/gotest.log` in append mode (`"a"`) so jobs with multiple `go test` steps (such as unit tests followed by `./tests/e2e`) preserve all raw test output in a single stream.

The namespace server parses `gotest.log` (falling back to step outputs when `gotest.log` is absent) by tracking `=== RUN   <Test>`, `=== PAUSE <Test>`, `=== CONT  <Test>`, `--- PASS: <Test>`, `--- FAIL: <Test>`, `--- SKIP: <Test>`, and package summary lines (`FAIL\t<pkg>`, `ok  \t<pkg>`). Each top-level test (including its subtests' interleaved output and any panic/race trace preceding the package `FAIL` line) is addressable at:

```
ci://<owner>/<repo>/<run>/<job>/[attempts/<n>/]tests/<pkg>/<Test>.log
```

Listing `.../tests/` returns the package paths tested in that attempt; listing `.../tests/<pkg>/` returns `<Test>.log` entries (or `?glob=**/tests/**` enumerates all tests).

---

## 2. REST Surface on the Sandbox Proxy

Every sandbox already reaches the daemon's per-sandbox HTTP git proxy on loopback (`http://127.0.0.1:9728`, or `http://192.168.9.1:9728` inside rootless granule VMs, with `127.0.0.1:9728` also forwarded). The namespace server mounts at `/fs/` on that same `gitproxy.Server`.

### 2.1 Endpoints & Query Parameters

- **`GET /fs/<scheme>/<path>`**
  Streams the resolved file content (`text/plain; charset=utf-8` or appropriate MIME type).
  - `Range: bytes=<start>-<end>` (also `bytes=<start>-` and `bytes=-<suffix>`): returns `206 Partial Content` with `Content-Range: bytes <start>-<end>/<total>` and `Accept-Ranges: bytes`.
  - `?tail=<N>`: returns the last `<N>` lines (or `?tail=<N>b` for last `<N>` bytes) of the file. Works on both completed and growing logs.
  - `?follow=1`: streams current content (or combines with `?tail=<N>` / `Range`) and stays open while the file is still growing (`X-Grain-Growing: true`), flushing newly appended bytes as the CI step produces them, sending periodic keep-alive heartbeats, and closing cleanly once the step/job completes or the follow timeout expires.
- **`HEAD /fs/<scheme>/<path>`**
  Returns metadata headers without a body:
  - `Content-Length: <bytes>`
  - `Accept-Ranges: bytes`
  - `X-Grain-Growing: true` if the underlying CI run/job/step is still active, `false` otherwise.
- **`GET /fs/<scheme>/<path>?list`** (or `GET /fs/<scheme>/` on a directory path)
  Returns a directory listing:
  - Default (`text/plain`): one entry per line (subdirectories suffixed with `/`).
  - `Accept: application/json` (or `?format=json`): JSON object `{"path": "...", "entries": [{"name": "...", "type": "file"|"dir", "size": 123, "growing": false}]}`.
- **`GET /fs/<scheme>/<path>?glob=<pattern>`**
  Matches paths beneath `<scheme>://<path>` against `<pattern>` (supporting `*`, `?`, and `**`), returning one matching path per line (or JSON with `Accept: application/json`).
  A pattern with no `/` and no `**` matches only the directory's direct children, without walking or capping anything. A pattern with either walks the subtree, up to 2000 entries. A trailing `/` matches directories only.
- **Truncation.** A listing, glob or grep that was cut short says so: text output ends with a `[truncated: <why>]` line, the response carries `X-Grain-Truncated: true`, and JSON gets `"truncated": true` plus a `truncated_reason`. Two caps do this: a walk hitting its 2000-entry bound, and a `ci://<owner>/<repo>` listing, which shows only the newest 200 runs.
- **`GET /fs/<scheme>/<path>?grep=<re>[&i=1][&n=1][&context=N][&max=N]`**
  Server-side RE2 regular expression search across a single file or recursively across all files under a directory prefix:
  - `i` (`?i` or `?i=1`): case-insensitive matching.
  - `n` (`?n` or `?n=1`): include 1-based line numbers (`path:line:match`).
  - `context=N`: include `N` lines of leading and trailing context around each match (bounded to `N <= 20`).
  - `max=N`: maximum number of matches to return (defaults to `100`, caller can raise up to `5000`). When the match limit is reached, a trailing summary line states that output stopped at `max=N` and shows how to raise `max` or narrow the path.

### 2.2 Authentication & Grant Scoping

- **Sandbox Proxy Credential**:
  Each sandbox is provisioned with a unique cryptographic token registered in `gitproxy.SandboxTokens` along with its `TaskID` and granted scopes. During sandbox credential setup (`ConfigureGitCredentials` / `ConfigureGitCredentialsOverSSH`), Grain writes `~/.git-credentials.netrc` (`0600`, `machine <proxy host> login sandbox password <token>`, plus the same line for `127.0.0.1`) and `~/.curlrc` (`netrc-optional` + `netrc-file`), so plain `curl -s http://127.0.0.1:9728/fs/...` inside the sandbox authenticates automatically without the agent having to look up or pass tokens on the command line. Explicit `Authorization: Basic ...` or `Authorization: Bearer <token>` headers are also accepted.
- **Grant Enforcement**:
  Every request resolves the caller's token to a `namespace.AccessScope` attached to the token entry:
  - `ci://`: requires an `api:ci-read` grant (`api:ci-read@<owner>/<repo>` for one repository), `self-debug`, or `grain-ci` for the task's own target/read repositories; a grant name the agent API does not parse (a bare `ci-read`, `api:ci-read:<repo>`) widens nothing, checked against `CIRepoScope`. Requests for repositories outside the task's scope—or from tasks without CI read permission—receive `403 Forbidden`.
  - `src://`: requires the `self-debug` grant (`403 Forbidden` otherwise).
  - `task://`: a run reads its own task; any other task requires `self-debug` or an `api:tasks:read` grant naming that task's repository (`403 Forbidden` otherwise, naming both). Working in, reading or writing a repository does not by itself open the other tasks in it.
  - `deploy://`: requires `self-debug` (`403 Forbidden` otherwise).
  - `config://`: requires `self-debug` or a `reads:` grant covering the state repository (`403 Forbidden` otherwise).
- **Redaction**:
  All text streams and grep/list outputs pass through `pkg/redact.Redactor` before leaving the server, scrubbing runtime secrets and credential patterns (`ghp_...`, `ghs_...`, bearer tokens, registered capability secrets) identically to MCP tool outputs.

### 2.3 Why NFS Is Rejected

Mounting the namespace into guests over NFS was evaluated and rejected for four reasons:

1. **Daemon restarts and tunnel drops cause process hangs or mid-read I/O errors:** The Grain daemon restarts on every self-upgrade, and on `kubernetes-granule` the `granule forward` tunnel can drop and reconnect under load. A hard NFS mount blocks every guest process touching the mount in uninterruptible sleep (`D` state) across daemon restarts, while a soft mount returns `EIO` mid-read to tools like `rg` or `python` that do not retry filesystem reads.
2. **Attribute caching makes growing CI logs look stale:** NFS client attribute/page caching (`acregmin`/`acdirmin`) causes `tail` or repeated reads of an in-progress CI step log to see cached EOF until the attribute timer expires, defeating live log inspection.
3. **Guest kernel and privilege requirements across backends:** Mounting NFS inside the guest requires `nfs` kernel modules and `CAP_SYS_ADMIN` / `mount(2)` privileges inside every sandbox across `granule`, `kubernetes-granule`, rootless `docker`, and `host` backends—capabilities that unprivileged container sandboxes deliberately drop.
4. **`AUTH_SYS` provides no cryptographic authentication:** Standard NFS `AUTH_SYS` trusts client-asserted UID/GID values, meaning multi-sandbox isolation would rest entirely on loopback/tunnel plumbing with no per-request bearer token verification.

If a filesystem mount is ever wanted in the future, an **HTTP-backed FUSE adapter** (`rclone mount` / `httpfs` style) layered on top of this same `/fs/` REST route is the option to evaluate.

### 2.4 Limits & Server-Side Protections

The `64 KiB` MCP tool-result cap exists strictly to protect the model's context window. Bytes fetched over REST (`curl -s http://127.0.0.1:9728/fs/... | rg FAIL` or `curl -o /tmp/gotest.log ...`) land in the sandbox filesystem or pipe, not in the model's context; only what the agent chooses to print to stdout in `run_command` reaches the model, where `run_command`'s own output cap applies. Therefore **REST responses carry no tool-result cap**.

To protect the daemon process from resource exhaustion, the `/fs/` server enforces:

- **Streaming without whole-file buffering:** File and artifact reads stream a line at a time through the redactor directly to `http.ResponseWriter` without allocating the whole artifact in memory.
- **Windows are cut after redaction, not before:** `Range`, `?tail=`, the response ceiling, `grain_copy`'s read limit and each `?follow=1` poll widen to the whole lines they touch, redact them, and only then cut. A line redaction left unchanged is cut to the exact byte; a line it changed is served whole (redacted) by every window that touches it, so a window starting or ending inside a secret cannot read the secret a few bytes at a time. While a log is still growing, `?follow=1` holds back a final line that has no newline yet. Redaction is per line (`Redactor.Line`), so a PEM private key's body on the lines after its header is not cut here.
- **Per-response byte ceiling:** Each single HTTP response is capped at a generous ceiling (`64 MiB`, matching `ci.MaxArtifactFileBytes` / `ciArtifacts` limits) via `io.LimitReader`.
- **Per-sandbox concurrency and rate limits:** Each sandbox token is limited to a bounded number of concurrent in-flight `/fs/` requests (default `8`, plus at most `2` concurrent `?follow=1` streams) and a token-bucket request rate limit (`30 req/s` burst `60`), returning `429 Too Many Requests` with `Retry-After: 1` when exceeded.
- **Timeouts and keep-alive for live tails:** `?follow=1` requests enforce a hard server-side lifetime ceiling (default `10m`), poll the in-memory live run state via wakeup ticker (`250ms`) without hitting SQLite when a run is in-memory in `Engine`, and emit keep-alive flushes every `15s`.
- **Bounded store work per request:** Listing and metadata lookups read bounded summaries (`ListSummaries` / cached run lookup with a short in-memory TTL for completed SQLite runs) so polling `/fs/ci/...` never triggers full-table `ci_run` + `ci_job` deserialization storms.
- **Server-side `?grep=` match limit:** Defaults to `max=100` matches (configurable up to `5000`) and bounds the total bytes scanned per request.

---

## 3. MCP Wrapper Tools

For discoverability and for sessions running without a sandbox (such as standing chat or triage sessions), four thin MCP tools wrap the same `namespace.Server`:

1. **`grain_ls(path, glob?)`**
   Lists entries at `path` (e.g. `ci://bwsalmon/grain/883a3337aa7850f7/go/attempts/1/tests` or `src://pkg/ci`), optionally filtered by `glob`.
2. **`grain_read(path, offset?, limit?, tail?)`**
   Reads content from `path` (with optional byte `offset`/`limit` or line `tail`).
3. **`grain_grep(path, pattern, ignore_case?, line_numbers?, context?, max_matches?)`**
   Runs a server-side regex search over `path` (single file or directory subtree).
4. **`grain_copy(path, dest)`**
   Copies a file or directory subtree from the namespace (`path`) directly into `dest` inside the task's sandbox workspace (working across both local directory sandboxes and SSH-backed `granule` / `docker` / `kubernetes-granule` sandboxes).

All four tools are registered in the shared MCP server and added to the tool roster for all agent frameworks (`claude`, `codex`, `antigravity`).

**Truncation behavior:** `grain_ls`, `grain_read`, and `grain_grep` enforce the normal MCP tool-result cap (`64 KiB`). Whenever an MCP tool response is cut at the cap (or hits the default grep match limit), the trailing notice explicitly prints the equivalent REST `curl` URL (`http://127.0.0.1:9728/fs/<scheme>/<path>`) and suggests `grain_copy(path, dest)` so the agent can inspect the full content inside its sandbox without truncation.

---

## 4. Tool Retirement Plan

Action tools that mutate state or coordinate execution stay as dedicated MCP tools:
- **Keep permanently:** `wait_for_checks`, `retrigger_ci`, `run_ci_job`, `propose_task`, `wait_for_task`, `open_pull_request`, `reply_to_pull_request_comment`, `resolve_review_thread`, `add_review_comment`, `comment_on_issue`, `ask_question`, `nothing_needed`, `write_handover`, `revert_task`, `revert_merge`.

Read-only tools that duplicate the namespace will be retired in three phases once all namespace roots (`task://`, `config://`, `memory://`, `deploy://`) are live:

| Phase | Action | Tools Covered |
| :--- | :--- | :--- |
| **Phase 1 (This task)** | Ship `ci://` and `src://` over REST + `grain_ls`, `grain_read`, `grain_grep`, `grain_copy`. Update agent run prompt to prefer `curl http://127.0.0.1:9728/fs/ci/...` and `grain_grep`/`grain_copy` when diagnosing CI failures. Update `wait_for_checks` and `get_ci_log` to surface failing attempt numbers and `/fs/ci/...` paths. | `get_ci_log`, `list_ci_runs`, `get_ci_artifact`, `read_grain_source`, `list_grain_source`, `search_grain_source` |
| **Phase 2 (Follow-up slice)** | Ship `task://`, `config://`, `memory://`, and `deploy://` roots. Add deprecation pointers in the legacy read tools' outputs (`read_grain_task`, `read_grain_task_prompt`, `read_grain_task_transcript`, `read_deployment_state`, `read_deployment_health`, `read_grain_metrics`). | `read_grain_task*`, `read_deployment_*`, `read_grain_metrics`, `query_merge_queue` |
| **Phase 3 (Retirement)** | Remove deprecated read-only tools from the default agent tool roster (keeping server-side aliases for one release window so in-flight sessions do not error on unknown tool names, then deleting the handlers). | All superseded read-only MCP tools |

---

## 5. Sandbox Backends & Tunnel Reconnection Behavior

The `/fs/` route is served on `gitproxy.Server`, which is the exact listener already forwarded into every sandbox backend for `git clone` / `git fetch` / `git push`:

| Backend | How `/fs/` Reaches the Proxy | Address Inside Sandbox |
| :--- | :--- | :--- |
| **`granule`** (local Firecracker VM) | Slirp / host tap interface (`192.168.9.1:9728`) + SSH `-R 127.0.0.1:9728` reverse port forward established during sandbox setup | `http://127.0.0.1:9728/fs/...` (and `http://192.168.9.1:9728/fs/...`) |
| **`kubernetes-granule`** (remote k8s pod running granule) | `granule forward` tunnel + SSH reverse forward from guest loopback `127.0.0.1:9728` back to daemon's `GitProxyAddr` | `http://127.0.0.1:9728/fs/...` |
| **`docker`** (container sandbox) | Container shares host loopback or reaches `127.0.0.1:9728` via container network / SSH reverse forward | `http://127.0.0.1:9728/fs/...` |
| **`host`** (local directory checkout) | Direct loopback connection to daemon's `127.0.0.1:9728` | `http://127.0.0.1:9728/fs/...` |

### Behavior While `granule forward` Is Reconnecting

On `kubernetes-granule` (and SSH-forwarded sandboxes), the `granule forward` tunnel is supervised with exponential backoff and readiness probing (introduced in task 796):

- **Short tunnel drop during a request:** A `GET /fs/...` request interrupted mid-stream receives an immediate TCP EOF. Because `/fs/` supports standard `Range: bytes=<offset>-` headers, `curl --retry 3 --retry-connrefused -C -` (or a subsequent range read) resumes from the byte offset where the stream stopped. Offsets are raw bytes, so a resumed read is exact except around a redacted line, which the resumed window serves again whole.
- **`?follow=1` live tails across reconnects:** If a live tail disconnects when the forward restarts, the client reconnects with `Range: bytes=<last_offset>-&follow=1` and continues tailing without gaps (a line carrying a redacted secret may repeat, as above).
- **MCP `grain_copy` resilience:** `grain_copy` runs inside the daemon and writes into the sandbox over the supervised `remoteRunner` SSH helper, which already waits on the tunnel readiness check before executing remote file writes.
