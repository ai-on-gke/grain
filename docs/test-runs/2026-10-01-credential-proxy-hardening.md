# Credential proxy hardening walkthrough: 2026-10-01

Walkthrough of the model proxy, git proxy and package cache hardening that landed on `main`, run with the real agent CLIs. This covers §5 of `docs/test-plans/2026-10-01.md`. That plan was not readable from this sandbox: no `grain/task-1353` branch exists, and `refs/pull/1353` is an unrelated, older pull request. So the checklist followed here is the one in task 1414's own description.

## Environment

| | |
|---|---|
| Base | `main` at `2771cad65` (contains every commit under test: `cdae1634c` `4eec98e86` `2e97cd4c8` `a122ccb6c` `9be4c051c` `c321c7116` `e2d03a389` `ac3a1ab97` `3debffddb` `b587de053` `f3e4cccd5` `6fd1bf2d6` `93702cc71` `c3603fc31`) |
| Go / git | go1.26.6, git 2.39.5 (Debian sandbox image) |
| claude | **2.1.263**, the `Dockerfile` pin (`CLAUDE_CLI_VERSION`), downloaded from `downloads.claude.ai` with its SHA-256 checked |
| codex | **codex-cli 0.153.0**, the `Dockerfile` pin (`CODEX_CLI_VERSION=rust-v0.153.0`). The GitHub release archive could not be fetched (github.com is unreachable from a sandbox), so this is the same version's `x86_64-unknown-linux-musl` binary from npm `@openai/codex@0.153.0` |
| agy (Gemini) | **1.2.2** build 6061403484848128, the `Dockerfile` pin, SHA-256 checked |

### How the proxy was run, and what was stubbed

The daemon fixes each vendor's upstream to the vendor's own API (`modelproxy.Vendor.Upstream`). No flag or setting redirects it, so a stock `grain daemon` cannot be pointed at a stub vendor. The walkthrough therefore used a small harness (not committed) that wires the same pieces the daemon wires:

- for each vendor, a `modelproxy.Endpoint` and a `modelproxy.Proxy`, with `Upstream` set to a local stub (the one thing `startModelProxy` does not set);
- tokens minted and revoked through the real capability provider (`pkg/capability/modelproxy.Provider.Materialize` / `Revoke`), with a run deadline, exactly as a dispatch does;
- the env each sandbox gets (`ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`, `OPENAI_BASE_URL`/`OPENAI_API_KEY`, `GOOGLE_GEMINI_BASE_URL`/`GEMINI_API_KEY`), built from the placements.

**Stubbed: the vendors only.** The stub records method, path, query, Upgrade header and the credential the proxy attached. It answers in each vendor's real shape:

- Anthropic: Messages SSE including a `tool_use` block, `count_tokens`, and the models list.
- OpenAI: Responses SSE, plus a minimal hand-written WebSocket server speaking codex's `response.create` protocol, including a `custom_tool_call` to codex's `exec` tool.
- Gemini: `streamGenerateContent?alt=sse`, including a `functionCall` to agy's `run_command`.

No real API key was used. The deployment credential was the string `sk-deployment-<vendor>`, and the stub log shows it replacing the run's token on every forwarded request.

The custom-backend replay used the daemon's own `startModelProxies` listeners and `customBackendModelProxy`. Git proxy and package cache checks ran against the real `gitproxy.BuildProxy` / `pkgcache.Server` with real `git` clients and `git http-backend` upstreams. Those harnesses are throwaway tests, not committed; the regression tests that were committed are listed under Fixes.

## Endpoints the agent CLIs used

Prompts asked for a tool call: `WALKTHROUGH: run echo`, answered by the stub with a shell tool call, then text. Claude's `/context` exercised token counting.

| CLI | Request through the proxy | Verdict on `main` | After this branch |
|---|---|---|---|
| claude 2.1.263 | `POST /v1/messages?beta=true` (streaming, tool_use → tool_result → text) | forwarded | forwarded |
| claude 2.1.263 | `POST /v1/messages/count_tokens?beta=true` (19 calls for `/context`) | forwarded | forwarded |
| claude 2.1.263 | `HEAD /api/hello`, sent **without** a credential at start-up | 401 (no token); the CLI ignores it | unchanged; see observation O2 |
| codex 0.153.0 | `GET /v1/responses` with `Upgrade: websocket` (Responses over WebSocket, codex's first choice) | **403**, 5 times, then fallback | **forwarded**: 101, `response.create` frames, tool call and `previous_response_id` follow-up all over the socket |
| codex 0.153.0 | `POST /v1/responses` (HTTPS fallback, streaming) | forwarded | forwarded (only used if the socket fails) |
| agy 1.2.2 | `POST /v1beta/models/<model>:streamGenerateContent?alt=sse` (gemini-3.1-pro-preview and gemini-3.1-flash-lite-preview; functionCall → functionResponse → text) | forwarded | forwarded |

No CLI called a models listing. claude reads `/v1/models` only for model pickers, codex ships its own catalogue with API-key auth, and `agy models` printed a built-in list without a request. Every request that did carry a token was either forwarded or, for codex's WebSocket on `main`, refused by the new method allowlist (finding F1).

## Allow/deny matrix (authenticated with a live run token)

Run with curl against the final tree. In every row marked refused, the stub saw nothing.

| Vendor | Request | Result |
|---|---|---|
| OpenAI | `DELETE /v1/models/x` | 403 |
| OpenAI | `GET /v1/files`, `POST /v1/files` | 403, 403 |
| OpenAI | `GET /v1/chat/completions` (list stored completions) | 403 |
| OpenAI | `GET /v1/responses` **without** a WebSocket upgrade | 403 |
| OpenAI | `DELETE /v1/responses/resp_1`, `POST /v1/batches`, `GET /v1/organization/users` | 403 each |
| OpenAI | `POST /v1/responses?foo=1` (unknown query parameter) | 403 |
| OpenAI | `GET /v1/models/../files` (`--path-as-is`), `GET /v1/models%2F..%2Ffiles` | 403, 403 |
| OpenAI | `POST /v1/responses`, body 64 MiB + 1, `Content-Length` | 413, nothing forwarded |
| OpenAI | `POST /v1/responses`, body 64 MiB + 1, chunked | 413. The first 64 MiB had already streamed upstream, which then saw `unexpected EOF`: a broken chunked stream, never a complete request. This is the documented behaviour of `Proxy.MaxBodyBytes` |
| Claude | `DELETE /v1/models/x`, `GET /v1/files`, `POST /v1/messages/batches`, `GET /v1/organizations/me`, `GET /v1/messages` | 403 each |
| Claude | `POST /v1/messages?foo=1` | 403 (`the query parameter "foo" is not one this proxy forwards`) |
| Claude | `POST /v1/messages`, body 64 MiB + 1 | 413 |
| Gemini | `DELETE /v1beta/models/x`, `GET /v1beta/files`, `GET /v1beta/tunedModels`, `GET /v1beta/cachedContents` | 403 each |
| Gemini | `POST …:generateContent?key=…&foo=1` | 403 |
| Gemini | body 64 MiB + 1 | 413 |

### Token replay after the run ends

| Backend | During the run | After the run ends |
|---|---|---|
| Default (capability lease, all three vendors) | `POST` inference → 200 | capability `Revoke` → **401** for claude, openai and gemini (`unknown-token`) |
| Custom backend (`customBackendModelProxy` through `custom.Framework.Run`, daemon's `startModelProxies` listener) | `DELETE /v1/models/x` → 403. The token authenticates; the route is refused, so nothing reaches the real vendor | `POST /v1/messages` → **401**, `DELETE /v1/models/x` → **401** |

## Git proxy

| Check | Result |
|---|---|
| Local TLS upstream (`httptest.NewTLSServer`, `ForwardTLS: true`) answers `302` to another path on the same host | **Not followed.** The upstream saw exactly one request, the original `info/refs`, carrying the deployment credential. The redirect target was never requested. The sandbox got a 404 and `git clone` failed cleanly. |
| `BuildProxy` / `RealForwarder.Forward` with `github.com@127.0.0.1`, `github.com@127.0.0.1:<port>`, `github.com:443@127.0.0.1:<port>`, `127.0.0.1:<port>/github.com`, `https://127.0.0.1:<port>`, over TLS and plain | All refused: `forward host … is not a bare host[:port]`. Upstream never reached. |
| `BuildProxy` with `example.com` over plain HTTP | Refused: plain HTTP is loopback-only |
| Replace the operator's secrets key (`secrets.Store.ImportKey`, what `POST /api/state-repo/secrets-key` ends in) after a token has been minted | The operator key file changed. A **new dispatch** (`SandboxTokenStore.EnsureToken` for a new sandbox) minted a token and **cloned**. The pre-import token still cloned, and a fresh `BuildProxy` (daemon restart) loaded the token file. |
| Push to the state repo (`LocalRepository` behind the full `GitProxy`, task targeting `grain/grain-config`): tree with a `.git` entry | Refused (`hasDotgit`); objects and refs untouched |
| … tree with a zero-padded mode `0100644` | Refused (`zeroPaddedFilemode`) |
| … tree with mode `100666` or `123456` | **Accepted on `main`**: branch created, objects written (finding F2). Refused on this branch (`badFilemode`), with the state repo's refs and object count unchanged. |
| … push to the checked-out `main` | Refused (`branch is currently checked out`) |
| … ordinary new branch | Accepted |

## Package cache

| Check | Result |
|---|---|
| Registry token request with `scope=repository:acmecorp/widgets:pull,push`, through `/v2/ghcr/token` and through the transparent `Host: ghcr.io` `/token` route | Upstream saw `repository:acmecorp/widgets:pull` with the daemon credential. **Never a push scope.** |
| … `push,pull`, `:*`, `pull,push,delete`, space-separated `pull` + `push` scopes, upper-case owner, a second `scope=` for another repo with `push` | Each narrowed to a single `…:pull` scope for the credential's repository. No push, `*` or delete scope ever went out with the daemon credential. |
| `POST api.github.com/repos/acme/widgets/issues`, `POST api.github.com/graphql`, `POST github.com/…/git-upload-pack/../../../repos/…/issues` through the transparent route | 405, upstream untouched |
| `POST api.github.com/repos/acme/widgets/git-upload-pack`, `POST api.github.com/acme/widgets.git/git-receive-pack`, `uploads.github.com`, `raw.githubusercontent.com` | **Forwarded out on `main`** (finding F3). 405 on this branch. |
| `GET …/info/refs?service=git-upload-pack` / `git-receive-pack`, `POST …/git-upload-pack`, `POST …/git-receive-pack` on `github.com` | Forwarded |
| Real `git clone` then `git push` through the cache's `/github/` route to a `git http-backend` upstream | Both succeed. The pushed commit landed upstream. |

## Findings

### F1: codex's Responses WebSocket refused by the OpenAI method allowlist (fixed)

`4eec98e86` binds `/v1/responses` to `POST`. codex 0.153.0 first opens `ws://<base>/responses`, a `GET` with `Upgrade: websocket`. The proxy answered 403 five times (about 6 s with backoff), each one showing up as an `error` event in `codex exec --json`, before codex fell back to HTTPS. The turn still completed, but every codex session through the proxy started with refused handshakes. The WebSocket transport, codex's preferred one, was unusable.

Reproduction (on `main`): mint an OpenAI proxy token, then:

```sh
CODEX_HOME=$tmp; printf 'openai_base_url = "%s"\n' "$OPENAI_BASE_URL" > $tmp/config.toml
echo "$OPENAI_API_KEY" | codex login --with-api-key
codex exec --json --skip-git-repo-check "hi"
```

The output has five `"type":"error"` events with `403 Forbidden … GET /v1/responses is not forwarded; this proxy forwards only POST there`, then `Falling back from WebSockets to HTTPS transport`.

At the unit level: `TestCodexsResponsesWebSocketHandshakeIsForwarded` fails on `main` with `handshake status 403, want 101`.

### F2: state-repo pushes with bad file modes accepted (fixed)

`f3e4cccd5`'s comment says `receive.fsckObjects` refuses "malformed modes". It does not: git ranks `badFilemode` as *info*, which receive-pack's strict check lets through with only a warning. Reproduction: through the git proxy to the local state repo, write a raw tree `100666 file\0<blob sha>` with `git hash-object -t tree -w --literally`, then `git commit-tree` and `git push origin <commit>:refs/heads/forged`. The push succeeds with `remote: warning: … badFilemode`, and the branch and objects appear in the state checkout. `TestLocalRepositoryRefusesTreesWithBadModes` fails on `main` for `100666` and `123456`.

### F3: package cache forwards POSTs to GitHub's API hosts when the path ends in a git endpoint (fixed)

`93702cc71`'s gate admits `POST` by path suffix (`/git-upload-pack`, `/git-receive-pack`) without looking at the host. The transparent route intercepts `.github.com`, which includes `api.github.com` and `uploads.github.com`. A NetworkNone sandbox could therefore still send a POST body, with whatever `Authorization` it holds, to GitHub's API hosts, as long as the path ends in a git endpoint name.

GitHub's API has no endpoint with that suffix, so this is egress rather than API control. Still, it is exactly the egress the commit set out to close.

Reproduction: `POST /repos/acme/widgets/git-upload-pack` with `Host: api.github.com` against a `pkgcache.Server`. On `main` it leaves the cache: the test got 404 back from the real host, and `uploads.github.com` / `raw.githubusercontent.com` gave 502. `TestGitHubRefusesAnythingButGitAndDownloads` fails on `main` with the new cases.

### O1: codex ignores the variables the OpenAI capability places (fixed later: grain/task-1515)

The capability's prompt tells a run to export `OPENAI_BASE_URL` and `OPENAI_API_KEY`, and names "the codex CLI" among the clients that "work unchanged". With codex 0.153.0 neither variable is enough:

- `OPENAI_BASE_URL` is ignored, as `pkg/agent/codex/codex.go` already notes. codex went straight to `wss://api.openai.com/v1/responses` and `https://api.openai.com/v1/responses`. No credential was sent, so nothing leaked, but the proxy was never used.
- With `openai_base_url` set in `config.toml`, codex still sends no `Authorization` until `codex login --with-api-key` has stored the token. The proxy answered `no-token` 401s.

A run that follows the prompt cannot use codex through the proxy. The working recipe is `openai_base_url` in `$CODEX_HOME/config.toml` plus `codex login --with-api-key`. This is prompt and capability text, not a proxy bug, so it is filed as a follow-up rather than changed here.

Later fix (grain/task-1515): `OpenAI.Clients` no longer names codex. The prompt now gives this recipe, with a separate `CODEX_HOME` (`modelproxy.Vendor.Setup`). It was checked with codex 0.153.0 against a stub vendor, run exactly as the prompt prints it.

### O2: claude's unauthenticated `HEAD /api/hello` start-up probe is logged as a refusal (not fixed)

Each claude CLI start sends `HEAD /api/hello` with no credential. The proxy answers 401 (`no-token`), which predates the allowlist and is correct. The daemon then logs `refused HEAD /api/hello for run "": no-token` for every claude start in a sandbox using the proxy. That is harmless journal noise, recorded here so nobody mistakes it for an attack.

## Fixes on this branch

| Commit | Change | Regression test |
|---|---|---|
| `fix(modelproxy): forward codex's Responses WebSocket handshake` | `Route.Upgrade`: OpenAI forwards `GET /v1/responses` for a `websocket` upgrade only; `Vendor.permits` takes the request | `TestTheAgentCLIsOwnRequestsAreForwarded` lists every request the pinned claude and codex made. `TestCodexsResponsesWebSocketHandshakeIsForwarded` does a 101 end to end and checks the credential swap. A plain `GET /v1/responses` is added to the refused-methods test. |
| `fix(gitproxy): refuse state-repo pushes whose trees carry bad file modes` | `receive.fsck.badFilemode=error` in the receive hardening | `TestLocalRepositoryRefusesTreesWithBadModes` |
| `fix(pkgcache): forward git's POSTs only to the hosts that serve git` | Git POSTs only to `github.com`, `gist.github.com` and the configured GitHub URL's host | New cases in `TestGitHubRefusesAnythingButGitAndDownloads` |

Each regression test was confirmed to fail with its fix reverted. `go test ./pkg/modelproxy/ ./pkg/gitproxy/ ./pkg/pkgcache/ ./pkg/capability/modelproxy/` and `make security` pass.

## Skipped or substituted, and why

- **The daemon binary as the model proxy host.** `startModelProxy` cannot be pointed at a stub vendor, so the real `Proxy`/`Endpoint`/capability provider ran in a harness with `Upstream` overridden (see Environment). The handler code, allowlists, body cap and token registry are the ones the daemon serves.
- **A real dispatch** for "after importing a new secrets key, a new dispatch still mints a git token and clones". This was exercised as the dispatch's own steps: `SandboxTokenStore.EnsureToken` for a new sandbox, a real `git clone` through `BuildProxy`, and a `BuildProxy` restart. A full `grain daemon` dispatch needs a sandbox backend and GitHub.
- **The real `POST /api/state-repo/secrets-key` handler.** The walkthrough called `secrets.Store.ImportKey`, which is where that handler ends (`cmd/grain/statemanager.go`).
- **Gemini via `gemini-cli`.** The image ships agy, not gemini-cli, so agy was used. It honours `GOOGLE_GEMINI_BASE_URL` once its settings carry `"modelProvider": "gemini"`, which grain's agy framework writes.
- **codex from the GitHub release archive**: github.com is unreachable from the sandbox, so the same version's npm build was used.
- **Token expiry without a Revoke** (the `MintUntil` deadline backstop) was not replayed live, since it needs the clock to pass a run deadline. It is covered by `pkg/modelproxy/tokens_test.go`.
