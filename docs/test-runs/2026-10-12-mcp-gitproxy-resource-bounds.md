# Resource Bounds Walkthrough: MCP `read_file`/exec Output, Git Proxy Pack Total

Plan: `docs/test-plans/2026-10-12.md` §3. Task 3288 (origin task 3209).

## Summary

| Step | Result |
| --- | --- |
| 1. `read_file` on special and oversized files | **Pass.** Every non-regular path is refused in under 0.1 s with `not a regular file`. The 40 MiB file is refused as too large. `mcpserver` RSS stays bounded. `/proc/self/environ` is read rather than refused (see notes). |
| 2. `run_command` printing 200 MB | **Pass, with Finding 2 (fixed).** The answer carries the head, a marker and the tail, and RSS is the same at 200 MB and 1 GB. The marker reported only 64 MB. The run pane/transcript was **not** exercised (no daemon run, see Environment). |
| 3. `edit_file` at 31 / 33 MiB | **Pass.** 31 MiB is edited. 33 MiB is refused and its sha256 is unchanged. |
| 4. >64 MiB request line, then a normal call | **Pass, with Finding 3 (fixed).** `-32600` comes back, and the next call on the same connection is answered. The error answer had no `id`. |
| 5. Delta-chain pack through the git proxy | **Pass, with Finding 1 (fixed).** The push is refused and the ordinary push afterwards lands. Peak heap was 519 MiB against a 256 MiB cap; it is 272 MiB after the fix. Run against the real proxy handler, not `grain demo` (see Environment). |

## Environment

- **Commit:** `candidate` at `c8a3f5e0c` (this branch's base). Both feature commits are in it:
  `git branch -r --contains b3c50afb4` and `... 8988fb6dd` both list `origin/candidate`. Fixes
  were re-measured on this branch's commits.
- **Host:** Debian sandbox VM, kernel `6.1.0-53-amd64`, 4 vCPU, 7.9 GiB RAM, no swap. go1.26.6,
  git 2.39.5.
- **Docker:** Engine 29.9.0 (API 1.56, containerd v2.4.1), already installed.
- **MCP path (steps 1–4):** the real `grain mcpserver -container sbx` binary (`go build ./cmd/grain`),
  driven over stdio by a small JSON-RPC client. This is the command every agent framework forks for a
  container-backed run (`pkg/agent/claude`, `codex`, `antigravity`, `pkg/daemon/agents.go`:
  `mcpserver -container <name>`), so its tools go through `mcp.ContainerExecRunner` →
  `docker exec`. The sandbox container was started with the daemon's own hardening flags
  (`pkg/orchestrator/container_sandboxes.go` `runArgs`): `--cpus 2 --memory 4096m
  --memory-swap 4096m --cap-drop ALL --security-opt no-new-privileges --pids-limit 512
  --workdir /home/agent debian:bookworm-slim sleep infinity`.
  I did not run a full `grain daemon` with a model-driven run. The daemon only forks this
  `mcpserver`, and a run needs a live model connection. For the same reason there is no run pane or
  transcript to render step 2's answer in. RSS figures are for the `mcpserver` process, which is the
  process that holds tool output under the container backend.
- **Git proxy (step 5):** `grain demo -github-sim` does **not** serve a git proxy and mints no
  run tokens. `pkg/demo` only builds a `gitproxy.Reach` for API reachability checks, and
  `githubsim.GitUpstream` models auth answers rather than a pushable repository. So step 5 ran
  against the real `gitproxy.NewHandler(&GitProxy{...})` behind a real HTTP server, with a real
  sandbox token (`LoadSandboxTokens`), a real `git push` client, and a local repository as upstream.
  This is the fixture `pkg/gitproxy/hardening_live_test.go` already uses, now driven by
  `TestLiveDeltaChainPastThePackCommitTotalIsRefused`.

## Stubs

| Stub | Stands in for | Where |
| --- | --- | --- |
| Python stdio JSON-RPC client | the agent framework's MCP client (claude/codex CLI) | `drive.py` below |
| `debian:bookworm-slim` container | the stock sandbox image | step 1–4 container |
| `stubScopeLookup`, `stubDeclaredPush` (declared push `refs/heads/main`, fast-forward only) | the store's run → repo scope and declared-push lookups | existing `pkg/gitproxy` fixtures |
| `LocalRepository` + `FakeForwarder` | GitHub's smart-HTTP git host | `newHardeningFixture(t, false)` |

grain's own code was not stubbed on either path.

## Step 1: `read_file` through the MCP server

Files were created in `/home/agent`: a FIFO (`mkfifo`), a directory, and a 40 MiB regular file
(`head -c 41943040 /dev/urandom`). RSS is `ps -o rss` / `VmHWM` of the `mcpserver` process, in KiB.

| Path | Time | Answer | RSS before → after (HWM) |
| --- | --- | --- | --- |
| `/dev/zero` | 0.09 s | `Error reading /dev/zero: /dev/zero: not a regular file` | 37 940 → 38 344 (38 344) |
| `/dev/urandom` | 0.08 s | `Error reading /dev/urandom: /dev/urandom: not a regular file` | 38 344 → 38 344 |
| FIFO | 0.04 s | `Error reading /home/agent/fifo: /home/agent/fifo: not a regular file` | 38 344 → 38 344 |
| directory | 0.04 s | `Error reading /home/agent/adir: /home/agent/adir: not a regular file` | 38 344 → 38 344 |
| `/proc/self/environ` | 0.04 s | **read**, not refused: `HOSTNAME=…\0HOME=/root\0PATH=…\0PWD=/home/agent\0` | 38 344 → 38 344 |
| 40 MiB regular file | 0.10 s | `Error reading /home/agent/big40: file is larger than 33554432 bytes, more than read_file and edit_file read; use run_command with head, tail, sed -n or grep to read part of it` | 38 344 → 176 148 (176 148) |

Notes:

- The plan expected `/proc/self/environ` to be refused. procfs reports it as a regular file, so the
  `[ -f ]` check passes. The read is still bounded by `head -c <cap+1>` and the 2-minute timeout,
  and it returns the environment of the `head` process inside the sandbox, which the run can already
  print with `run_command env`. This is not a bound violation; it is noted as a difference from the
  plan.
- The 40 MiB refusal raises RSS to ~172 MB. `head -c` hands back 32 MiB + 1 byte, and that string
  is copied a few times before it is refused. The rise is fixed by the cap: repeated calls do not
  grow it further. See the step 2 RSS for comparison.

## Step 2: `run_command` writing ~200 MB

`run_command "head -c 200000000 /dev/zero | tr '\0' a; echo; echo TAIL_END_MARKER"`:

- 0.35 s, `isError=false`, an 8 647-byte JSON-RPC answer line. It begins `exit=0\nstdout:\naaaa…`,
  has grain's elision notice on a line of its own, and ends `…\nTAIL_END_MARKER\n\nstderr:\n`.
- `mcpserver` RSS went from 35 236 to 294 488 KiB. With **1 GB** of output (`BYTES=1000000000`) it
  peaked at 243 160 KiB, so RSS is bounded by the 64 MiB head+tail buffer, not by output size.
- **Finding 2.** Before the fix the notice read `[grain] 64.0 MB of 64.0 MB elided here by grain`:
  the runner's own `[… bytes of output dropped …]` marker sits in the middle that the answer elides.
  After the fix it reads `[grain] 190.7 MB of 190.7 MB elided here by grain` (200 000 006 bytes).
- Run pane / transcript rendering (Playwright): **not done**. There was no daemon run whose
  transcript would carry this answer (see Environment).

## Step 3: `edit_file` on 31 MiB and 33 MiB files

Both files are base64 text with `EDIT_ME_HERE` as their first line, and the edit replaces it with
`EDITED_BY_WALK`.

| File | Size | Time | Answer | sha256 before | sha256 after |
| --- | --- | --- | --- | --- | --- |
| `f31` | 32 505 856 | 0.27 s | `Wrote /home/agent/f31` | `f84ec925…1d50d1557` | `2d256e5b…2bf1e28` (first line now `EDITED_BY_WALK`) |
| `f33` | 34 603 008 | 0.11 s | `Error reading /home/agent/f33: file is larger than 33554432 bytes, …` | `5e440170…6adb7891` | `5e440170…6adb7891` (**identical**) |

`mcpserver` HWM across both: 366 292 KiB. The 31 MiB edit holds the file read, the replaced copy
and the write's stdin.

## Step 4: Overlong request line, then a normal call

A single 68 157 578-byte `tools/call` (`write_file` with 65 MiB of content), then
`run_command "echo still-serving; ls /home/agent/huge"` on the same stdin:

- First answer, in 0.14 s: `{"jsonrpc":"2.0","error":{"code":-32600,"message":"invalid request: longer than 67108864 bytes"}}`.
  **Finding 3:** there is no `id`. After the fix the answer carries `"id":null`.
- Second answer, in 0.04 s, `id` 5: `still-serving` and `ls: cannot access '/home/agent/huge'`. The
  overlong write was discarded rather than executed.
- RSS stayed at 193–194 MB across the overlong line (`readRequestLine` discards it chunk by chunk).

## Step 5: Delta-chain pack through the git proxy

The client commits `n = 256 MiB / 30 MiB + 3 = 11` commits whose messages are the same 30 MiB of
text, differing only in their last line. A separate check with `git pack-objects` on 12 such commits
confirms git packs every one after the first as a ~1.7 KB `OFS_DELTA` against the 30 MiB base
(`git verify-pack -v`). The client then runs `git push origin HEAD:refs/heads/main` through the
proxy with sandbox token `tok0`. `main` is a declared ref that may only be fast-forwarded, so the
proxy has to walk the pack to prove the fast-forward.

- The push is refused with `refs/heads/main: could not show … is a fast-forward …`. The walk fails
  closed on `pack commits exceed maximum total size`, and `main` does not move.
- **Finding 1.** Peak Go heap in use during the push was **519 MiB** against a 256 MiB cap.
  After the fix it is **272 MiB** (two runs, both 272 MiB).
- After `git reset --hard <main>` and one empty commit, an ordinary `git push … main` lands.
- 7.4 s per run.

## Findings

1. **Git proxy pack walk made ~1.3× the cap in garbage (fixed).** `commitParents` ran
   `strings.Split(string(data), "\n")` over every reconstructed commit just to read the parent lines
   of its header. Each 30 MiB commit therefore cost a 30 MiB string copy plus a ~500k-entry slice on
   top of what `maxPackCommitBytes` counts. Deterministic repro:
   `go test ./pkg/gitproxy -run TestLiveDeltaChainPastThePackCommitTotalIsRefused` on `c8a3f5e0c`
   with the new test applied: `peak heap in use during the push: 519 MiB, over 412 MiB`.
   **Fix:** walk the header in place with `bytes.Cut` and stop at the blank line (`4e9b326c5`).
2. **`run_command` under-reported output the runner had already cut (fixed).** Any command printing
   more than `execOutputLimit` (64 MiB) was reported as "64.0 MB of 64.0 MB elided". Deterministic
   repro: `TestRunCommandElisionCountsWhatTheRunnerDropped` (10 MB through a 1 MiB runner limit)
   fails on the base. **Fix:** `runnerDropped` reads the runner's marker back from the exact
   position `headTailBuffer.String` puts it, and `run_command`'s elision notice counts those bytes
   (`ac1cfd7ba`). Other `elideMiddle` callers are unchanged.
3. **`-32600` / `-32700` answers carried no `id` (fixed).** JSON-RPC 2.0 requires `"id": null` when
   the request's id cannot be read. Deterministic repro: send `{not json` or a line over the cap to
   `Registry.Handle` / `Serve`. **Fix:** both answers carry `nullID` (`8cc2c542c`), checked by
   `TestServeAnswersAnOverlongLineAndCarriesOn` and `TestHandleAnswersAnUnparsableLineWithANullID`.

Not a finding, but noted:

- The plan's §3 step 6 says to push through `grain demo`'s git proxy, which does not exist. A future
  plan should name the live gitproxy fixture, or `grain daemon` with a declared push, instead.

## Reproduction script (steps 1–4)

`drive.py` spawns `/tmp/grain mcpserver -container sbx`, sends `initialize`, then:

- step 1: `read_file` for each path above;
- step 2: `run_command` with `head -c ${BYTES:-200000000} /dev/zero | tr '\0' a; echo; echo TAIL_END_MARKER`;
- step 3: `edit_file` of `EDIT_ME_HERE` → `EDITED_BY_WALK` on `f31` and `f33`;
- step 4: one 65 MiB-content `write_file` request line, then a `run_command`.

After each call it records `ps -o rss=` and `VmHWM` from `/proc/<pid>/status`.
