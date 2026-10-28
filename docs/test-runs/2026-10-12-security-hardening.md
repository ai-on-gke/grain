# Security Hardening Walkthrough: Model Proxy, Git Proxy, Sandboxnet, Registry-Push

- **Date:** 2026-10-12
- **Plan:** `docs/test-plans/2026-10-11.md` §11
- **Task:** 3147 (filed by the nightly test-plan task 2955)
- **Base:** `candidate` at `0547af051`. All five commits under test are on it: `348a146d1` (model
  proxy), `07f95f71e` (push spool), `b706b7a27` (pack bounds), `c3a112ded` (sandboxnet timeouts),
  `de3880a64` (registry-push/image-signing).
- **Environment:** Debian 12 sandbox (kernel 6.1, 4 vCPU, 7 GiB). Go 1.26.6, git 2.39.5,
  Node v20.19.0, `@playwright/test` (Chromium, headless), the frontend built with `make frontend`.

## Verdict

| Step | Result |
|---|---|
| 1. Model proxy routes | **Pass.** Every refused route gets a 403 and nothing reaches the stub. Every allowed route reaches the stub once, with the deployment's key in place of the run token. |
| 2. Git proxy, 40 MiB push | **Pass, with a finding.** The push spools to `$TMPDIR`, lands, and leaves no temp file. The proxy's memory still grows with push size (Finding 2, filed). |
| 3. Git proxy, crafted packs | **Pass.** zlib and delta bombs are refused by the size bounds and the proxy keeps serving. Found and fixed a regression in the same bound (Finding 1). |
| 4. Sandboxnet slowloris | **Pass.** A client dripping headers at 1 byte/s is cut at 10.0s while ordinary requests are answered. An idle keep-alive connection is closed at 60.0s. |
| 5. Capabilities UI | **Pass.** Both capabilities are widenable and admin-only by default. A non-admin team is refused them until widened, and accepted after. |
| CI | Full grain CI suite green (see below). |

## How the live path was reached

The plan asks for the running daemon's code paths. A full `grain daemon` only mints a run's
model-proxy token and sandbox git token when it dispatches a run into a sandbox. Neither can be
minted from outside without faking a dispatch end to end, so steps 1–4 drive the daemon's own wiring
in-process instead:

- **Model proxy.** `startModelProxy` (`pkg/daemon/modelproxy.go`, the per-vendor half of
  `StartModelProxies` as `run.go` calls it) on a `daemon.Direct` fabric. That is the sandboxnet
  listener and `http.Server` a sandbox reaches, with the credential resolved from the
  `-gemini-api-key-file` / `-openai-api-key-file` path. The run token is minted on the real
  `modelproxy.Endpoint`. The one substitution is `Vendor.Upstream`, pointed at a local stub.
- **Git proxy.** `gitproxy.NewHandler(&GitProxy{...})` behind a real HTTP server, with a real
  `git` client and real `git receive-pack`. Two paths are exercised:
  - the controller-checkout path (`LocalRepository`);
  - the GitHub path: a `RealForwarder` to `git http-backend` served over `net/http/cgi`.

  Authorization is `ModelAuthorizer` with stub scope and branch lookups: target `acme/widgets`,
  own branch `grain/task-2112`, and a declared push set `refs/heads/main` (fast-forward only).
- **Sandboxnet.** `daemon.Direct.Serve`, the function `c3a112ded` changed.
- **Capabilities.** A real `grain demo` (`-addr=127.0.0.1:0`) on its embedded SQLite store, driven by
  Playwright. Nothing is mocked.

Steps 1–5 are now durable tests, so CI reaches the live path from here on:

- `pkg/daemon/hardening_live_test.go`
  - `TestDaemonGeminiProxyForwardsOnlySynchronousModelCalls`
  - `TestDaemonOpenAIProxyRefusesStoredInputItems`
  - `TestDaemonSandboxnetCutsOffAClientDrippingHeaders`
- `pkg/gitproxy/hardening_live_test.go`
  - `TestLivePushOverTheSpoolThresholdLandsAndLeavesNoSpoolFile`
  - `TestLiveCraftedPackBombsAreRefusedAndTheProxyStaysUp`
  - `TestLiveFastForwardCarryingAFileOverThePackObjectBoundLands`
- `pkg/gitproxy/pack_test.go`
  - `TestReadPackCommitsReadsPastABlobOverTheObjectBound`
- `ui/e2e/registry-push-widening-walkthrough.spec.js`

## Stubs

| Stub | Stands in for | Where |
|---|---|---|
| `upstreamStub` (httptest server, records method, path and credential) | Gemini (`generativelanguage.googleapis.com`) and OpenAI (`api.openai.com`) | `pkg/daemon/hardening_live_test.go` |
| `git http-backend` over `net/http/cgi`, on a local bare repo | GitHub's smart-HTTP git host | `pkg/gitproxy/hardening_live_test.go` (forwarded variant) |
| `stubScopeLookup`, `stubDeclaredPush` | the store's run → scope and declared-push lookups | existing gitproxy test fixtures |

No grain API, store or React component was stubbed. The live Gemini key in the sandbox was not used.

## Step 1: Model proxy

Each request was sent with a minted run token: Gemini in `x-goog-api-key` (and as `?key=`),
OpenAI as `Authorization: Bearer`.

**Gemini.** Each row was sent under each of `/v1beta`, `/v1` and `/v1alpha`.

| Request | Status | Reached stub |
|---|---|---|
| `GET models`, `GET models?pageSize=5` | 200 | yes, once, with the deployment key |
| `GET models/gemini-2.5-flash` | 200 | yes |
| `POST models/gemini-2.5-flash:generateContent` | 200 | yes |
| `POST models/gemini-2.5-flash:streamGenerateContent?alt=sse` | 200 | yes |
| `POST models/gemini-2.5-flash:countTokens` | 200 | yes |
| `POST models/text-embedding-004:embedContent` | 200 | yes |
| `POST models/text-embedding-004:batchEmbedContents` | 200 | yes |
| `POST models/gemini-2.5-flash:batchGenerateContent` | **403** | no |
| `POST models/text-embedding-004:asyncBatchEmbedContent` | **403** | no |
| `POST models/veo-3.0-generate-preview:predictLongRunning` | **403** | no |
| `GET models/veo-3.0-generate-preview/operations` (and `/operations/op-1`) | **403** | no |
| `GET models/<m>:generateContent`, `POST models/<m>` (no action) | **403** | no |
| `POST models/:generateContent`, `POST models/a:b:generateContent` | **403** | no |
| `POST batches`, `GET operations`, `POST files` | **403** | no |
| `?key=<run token>` on `:generateContent` / `:batchGenerateContent` | 200 / **403** | yes (key swapped) / no |

**OpenAI.**

| Request | Status | Reached stub |
|---|---|---|
| `POST /v1/responses` | 200 | yes, with `Bearer <deployment key>` |
| `GET /v1/responses/resp_123` | 200 | yes |
| `POST /v1/responses/resp_123/cancel` | 200 | yes |
| `POST /v1/responses/compact`, `POST /v1/responses/input_tokens` | 200 | yes |
| `GET /v1/responses/resp_123/input_items` (also `?limit=100`) | **403** | no |
| `DELETE /v1/responses/resp_123`, `POST /v1/responses/resp_123`, `GET …/cancel` | **403** | no |

Each refusal logged one daemon journal line, for example
`grain daemon: gemini proxy: refused POST /v1beta/models/gemini-2.5-flash:batchGenerateContent for run "run-3147": forbidden-endpoint`.

## Step 2: Git proxy, 40 MiB push

- **Setup.** `TMPDIR` was a private directory, and a 40 MiB random file was committed and pushed to
  the run's own branch with `git push`.
- **Spooling.** A spool directory watcher sampling every 2 ms saw exactly one
  `grain-gitproxy-push-*` file, growing to 40 MiB.
- **Result.** The push succeeded on both paths and the branch tip upstream is the pushed commit.
  The spool directory was empty afterwards.
- **Memory.** The push file was generated outside the Go heap with `head -c … /dev/urandom`.

  | Push | Path | Peak `HeapInuse` | VmHWM (baseline ~20 MB) | TotalAlloc during push |
  |---|---|---|---|---|
  | 40 MiB | local repository | 42 MiB | 91 MB | +124 MiB |
  | 40 MiB | forwarded | 72 MiB | 105 MB | +124 MiB |
  | 120 MiB | forwarded | 123 MiB | 164 MB | +205 MiB |

  Memory tracks push size. See Finding 2.

## Step 3: Git proxy, crafted packs

Each crafted pack was posted to the live handler as a hand-built `git-receive-pack` request that
moves declared `refs/heads/main` from its tip to a new id. A declared, non-force update is the one
case where the proxy walks the pack to prove a fast-forward.

| Pack | Bound that trips | Reply | `main` | Next request |
|---|---|---|---|---|
| One commit whose zlib stream inflates to 33 MiB of zeros | `decompressed pack object exceeds maximum size` | refused, "could not show … is a fast-forward" | unchanged | `info/refs` 200 |
| A small commit, then an ofs-delta against it claiming a 1 TiB result | `delta target size exceeds limit` | refused, same sentence | unchanged | `info/refs` 200 |
| (control) an honest fast-forward pushed afterwards | none | accepted | moved | n/a |

The test first asserts the size bound itself trips on each pack (`readPackCommits` / `applyDelta`),
so a refusal for any other reason cannot pass it. Writing that assertion caught a malformed first
draft of the zlib fixture.

Measured as well: a pack of 64 blobs, each ~32 MiB of zeros (31 KiB compressed, 2 MiB pack), takes
1.7 s of one core to walk, about 0.9 s per MiB of pack. See Finding 3.

## Step 4: Sandboxnet slowloris

The client drips `GET / HTTP/1.1\r\nHost: probe\r\nX-Slow: aaaa…` one byte a second onto a
`Direct.Serve` listener.

- **Cut-off.** The server closes the connection **10.0s** after it opened.
- **Other clients.** Three ordinary `GET`s at 2s, 4s and 6s were each answered 200 `ok` while the
  drip ran.
- **Idle timeout (one-off measurement, not kept as a test).** After one complete request on a
  keep-alive connection, the idle connection was closed after **60.0s** (EOF).

## Step 5: Capabilities UI (`grain demo`, Playwright)

1. **Defaults.** `GET /api/settings` reports `registry-push` and `image-signing` with
   `widenable: true`, `authorizedTeams: ["default"]` and `ciOnly: true`. They are CI-only, so
   `GET /api/config`'s task-grantable list does not include them. A team meets them in its repo
   config's CI capabilities.
2. **Before widening.** Teams `delta` and `epsilon` were created. For each capability,
   `PUT /api/teams/delta/repos/acme/widgets/config {"ciCapabilities":[cap]}` returned **400**:
   `capability "registry-push" is restricted to authorized teams [default]; team "delta" is not authorized. A deployment operator can authorize team "delta" for it under Settings > Capabilities ("registry-push", "Also authorize teams")`.
   `image-signing` got the same refusal.
3. **Widening.** Under `/admin/integrations` → Capabilities, both the **Registry push** and the
   **Image signing** tabs read "Admin-only: held by default." with an empty "Also authorize teams"
   field. Typing `delta` and blurring sent `PUT /api/settings` with `capabilityTeams[cap] = ["delta"]`,
   and the hint changed to "…widened to delta".
4. **After widening.** `delta`'s config accepted `ciCapabilities: ["registry-push","image-signing"]`
   (200), while `epsilon` was still refused both (400). After a reload the fields still read `delta`,
   and `/api/settings` reports both capabilities' `authorizedTeams` as containing `default` and
   `delta`, but not `epsilon`.

The spec passed three times in a row (`--repeat-each=3`).

## Findings

### Finding 1 (fixed here): the pack object bound refused large files in a declared fast-forward

`b706b7a27` applied the 32 MiB `maxPackObjectBytes` bound to every object the fast-forward proof
inflated, blobs included, although the walk keeps only commits. A run pushing a file over 32 MiB to
a ref its template declares without `force: true` was therefore refused with "could not show …
is a fast-forward". The same push to its own branch, which is never walked, landed.

**Reproduction (deterministic).** In the git proxy fixture above, commit a 40 MiB random file and
run `git push origin HEAD:refs/heads/main`. On `candidate` at `0547af051` the push fails:

```
! [remote rejected] HEAD -> main (refusing to update acme/widgets: refs/heads/main: could not show 721596d5845c is a fast-forward of b3fbfc37047c from the commits this push carries, …)
```

**Fix.** Commit `fix(gitproxy): let a fast-forward carry a file over the 32 MiB pack object bound`.
`readPackCommits` now reads into memory, and holds to the bound, only a commit or a delta it could
resolve against a kept commit. Every other object is inflated into `io.Discard`, with no allocation.
The commit bounds are unchanged (per object, per delta target, and the total kept). CPU cost is no
worse, since a blob over the bound used to be inflated up to the bound before failing.

**Regression tests.** `TestLiveFastForwardCarryingAFileOverThePackObjectBoundLands` (live, real
git) and `TestReadPackCommitsReadsPastABlobOverTheObjectBound` (unit, asserts < 8 MiB allocated).
Both fail on the base and pass with the fix. `TestReadPackCommitsDecompressionLimit`,
`TestApplyDeltaValidation` and `TestReadPackCommitsBoundsTheCommitsItKeeps` are untouched and pass.

### Finding 2 (follow-up filed): the push spool does not bound memory

Right after spooling, `GitProxy.HandleRequest` (`pkg/gitproxy/core.go`) calls
`data, err = spooled.Bytes()`, which reads the whole spool file back into memory for the ref-update
check. Memory therefore grows with push size, up to the 2 GiB `DefaultMaxPushBodyBytes` (see the
Step 2 table). Fixing it means a lazy body for `refuseRefUpdates` and a pack walk that reads from the
file. That is a moderate change to security-sensitive code, so it was filed as a follow-up rather
than made here.

### Finding 3 (follow-up filed): no total bound on what the pack walk inflates

The per-object bound does not limit the total. Many ~1000:1 zlib objects cost about 0.9 s of CPU per
MiB of pack, synchronously in the request. The walk is reachable only for a declared, non-force
update of an existing ref. Choosing a total budget that still lets legitimate large fast-forwards
through is a design decision, so it was filed as a follow-up that depends on this task.

## Fixes made here

- `pkg/gitproxy/pack.go`: Finding 1.

## CI

grain CI, full suite: **success** for `270d2d3` (run `9a2b8fd95754c4aa`): frontend, go, installer,
lint, package cache, postgres, relay image, terraform and ui e2e. The verdict for the final commit,
which adds only this report, is in the pull request.

## Notes for the next run

- `make playwright-browsers` and `npm ci` in `ui/` worked as is in the stock sandbox image. Nothing
  extra had to be installed.
- `pkill -f '<pattern>'` inside `run_command` matches the wrapping shell's own command line and
  kills it. Use `pgrep` and kill by PID instead.
