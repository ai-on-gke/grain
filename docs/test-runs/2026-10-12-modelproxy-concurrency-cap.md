# Model proxy per-run concurrency cap walkthrough: 2026-10-12

Walkthrough of §12 of `docs/test-plans/2026-10-12.md` (read from `origin/candidate`), run against `main`. Feature commit: `e4f399383`. It is contained in `origin/main`, and `git branch -r --contains` also lists `origin/candidate`, though the task said otherwise. The model proxy caps open requests per run token (`Proxy.MaxConcurrent`, daemon flag `-model-proxy-max-concurrent`, default 16 per vendor). Past the cap it answers 429 in the vendor's own shape with `Retry-After`, audited as `too-many-requests`.

**Verdict:** the cap works as specified through a real daemon for all three vendors:

- run A is capped while run B is not;
- the 429 bodies match each vendor's shape;
- the refusals are audited in the daemon log;
- a killed client frees its slot at once;
- `0` and `-1` mean the default, as `--help` says.

Finding F1 is the one that matters in practice: at their **default** retry settings, the official vendor SDKs fail any request the cap refuses instead of waiting for a slot. With retries raised to cover the queue, every call completes. F1 is filed as a follow-up. No code change was needed; this branch adds one regression test for the disconnect path, which CI did not cover before.

## Environment

| | |
|---|---|
| Base | `main` at `f3dfcaa5c` (contains `e4f399383`) |
| Daemon | the real `grain daemon`, built from that tree (`go build ./cmd/grain`), go1.26.6, on the Debian sandbox host |
| Daemon flags | `-data-dir`, `-sandbox-dir`, `-github-host 127.0.0.1:1 -github-insecure-http` (no GitHub reachable or needed), `-ui-addr 127.0.0.1:8420`, `-native-sandboxes=false`, `-claude-oauth-token-file`/`-openai-api-key-file`/`-gemini-api-key-file` (stub keys), `-model-proxy-max-concurrent 2` (later `0`, `-1`) |
| Proxies | claude `127.0.0.1:9744`, openai `127.0.0.1:9745`, gemini `127.0.0.1:9746` (the daemon's reserved block) |
| Clients | curl 7.88.1. Python 3.11.2 with `anthropic` 1.13.0, `openai` 3.29.0, `google-genai` 2.30.0 (httpx 0.28.1). Node 20.19.0 with `@anthropic-ai/sdk` 0.133.0, `openai` 6.49.0, `@google/genai` 2.29.0 |

### How it was wired, and the one stub

The daemon fixes each vendor's upstream to the vendor's real host (`modelproxy.Vendor.Upstream`), and no flag redirects it. So that the daemon could run unmodified, the redirect was made outside it:

- `/etc/hosts` maps `api.anthropic.com`, `api.openai.com` and `generativelanguage.googleapis.com` to `127.0.0.1`.
- A throwaway CA signs a certificate for those three names. The daemon runs with `SSL_CERT_FILE` set to the system bundle plus that CA. No grain code or flag was changed.

**Stub: the vendor upstream only** (`stub.py`, not committed). It is a Python `ThreadingHTTPServer` on `127.0.0.1:443` with TLS. It:

- holds every non-streaming request 5s, then answers in the vendor's real success shape (Anthropic Messages, OpenAI Chat Completions/Responses, Gemini `generateContent`);
- for a streamed request (`"stream": true` or `streamGenerateContent`), sends one SSE event per second for 10s;
- logs start and end time, active count, a client that went away, and the tail of the credential the proxy attached.

Everything else is grain's own:

- **Connections.** The default team's connections were made through the daemon's REST API: `PUT /api/teams/default/agent-backends/{wt-claude,wt-codex}` and `PUT /api/agent-backends/{id}/credential`. The Gemini connection is `default-antigravity`, which the daemon migrated from `-gemini-api-key-file`.
- **Run tokens.** Tokens for owners `run-A` and `run-B` were minted with the real `modelproxy.Tokens.Mint`, into the files the daemon persists its registry to (`<data-dir>/secrets/modelproxy-<vendor>-tokens.json`). The daemon was then restarted, and it adopted them through `Tokens.PersistTo`, as it adopts tokens across any restart. (A dispatch mints through `pkg/capability/modelproxy`, keyed by run ID; that path was not driven here.) The helper was a 25-line `go run` program, not committed.

No real API key was used. The stub log shows the connection's key replacing the run token on every forwarded request (`…rough-claude`, `…rough-openai`, `…rough-gemini`), and never the run token.

## Steps and results

### 1. Start with `-model-proxy-max-concurrent 2` and mint a run token

The daemon started, served the three proxies at the addresses above, and adopted the persisted tokens across the restart. Pass.

### 2. Five concurrent curl requests per vendor; a second token alongside

`burst.sh <vendor> 5` sent 5 requests from run A at once, and one from run B 0.3s later.

| Vendor | Run A: 200 | Run A: 429 | Run B | 429 body | `Retry-After` |
|---|---|---|---|---|---|
| Claude `POST /v1/messages` | 2 (at 5.0s) | 3 (at 0.02s) | 200 at 5.3s, i.e. forwarded at once | `{"error":{"message":"This grain Claude proxy forwards at most 2 requests at once for one run, …","type":"rate_limit_error"},"type":"error"}` | `1` |
| OpenAI `POST /v1/chat/completions` | 2 | 3 | 200 | `{"error":{"code":"rate_limit_exceeded","message":"This grain OpenAI proxy forwards at most 2 …","param":null,"type":"requests"}}` | `1` |
| Gemini `POST /v1beta/models/gemini-3.1-pro:generateContent` | 2 | 3 | 200 | `{"error":{"code":429,"message":"This grain Gemini proxy forwards at most 2 …","status":"RESOURCE_EXHAUSTED"}}` | `1` |

Each body matches the vendor's own 429 shape (Anthropic `rate_limit_error`, OpenAI `rate_limit_exceeded`/`requests`, Google `RESOURCE_EXHAUSTED`).

The daemon logged one line per refusal, naming the run, the team and the connection, with no token or query string:

```
grain daemon: claude proxy: refused POST /v1/messages for run "run-A" (team "default", connection "wt-claude"): too-many-requests
grain daemon: openai proxy: refused POST /v1/chat/completions for run "run-A" (team "default", connection "wt-codex"): too-many-requests
grain daemon: gemini proxy: refused POST /v1beta/models/gemini-3.1-pro:generateContent for run "run-A" (team "default", connection "default-antigravity"): too-many-requests
```

Refused requests never reached the stub. During each burst it saw at most 3 at once: run A's 2 plus run B's 1. Pass.

### 3. Official clients with retries on

`sdk.py` / `sdk.mjs <vendor> 6 <retries>` sent 6 concurrent calls from run A through the vendor's official client. The cap was 2 and the upstream held each call 5s, so the last pair can only start after about 10s.

| Client | Default retries | `max_retries` 5 | 10 | 30 |
|---|---|---|---|---|
| `anthropic` (Python) | **2/6**: 4 × `RateLimitError` at 2.0s | 4/6 | 6/6 (15.1s) | 6/6 (15.1s) |
| `openai` (Python) | **2/6**: 4 × `RateLimitError` at 2.1s | 4/6 | **4/6** (failed at 10.1s) | 6/6 (15.2s) |
| `google-genai` (Python, `HttpRetryOptions(attempts=n+1)`) | **2/6**: 4 × `ClientError 429` at under 0.1s | 6/6 (23.4s) | 6/6 (22.9s) | 6/6 (22.5s) |
| `@anthropic-ai/sdk` (Node) | **2/6**: 4 × 429 at 2.1s | | | 6/6 (15.2s) |
| `openai` (Node) | **2/6**: 4 × 429 at 2.1s | | | 6/6 (15.2s) |
| `@google/genai` (Node, `retryOptions.attempts`) | **2/6**: 4 × `ApiError` at 0.0s | | | 6/6 (28.8s) |

With retries raised enough to cover the queue, all calls complete on every client, so the step passes as written. At their defaults, though, every client fails the requests the cap refuses: finding F1. Anthropic and OpenAI honour `Retry-After: 1`, so their retries are about 1s apart. `google-genai` uses its own exponential backoff, which is why it finishes later.

### 4. Kill the client mid-stream; the next request gets a slot

`stream-kill.sh <vendor>`:

1. Open two streamed replies from run A, which fills both slots.
2. `kill -9` the first `curl` after 2s.
3. 0.5s later, while the second stream is still open, send a request from run A.

| Vendor | Probe 0.5s after the kill | Stub upstream |
|---|---|---|
| Claude (`"stream": true`) | **200** | killed stream's upstream connection closed (`client-gone`) at the kill |
| OpenAI (`"stream": true`) | **200** | same |
| Gemini (`streamGenerateContent?alt=sse`) | **200** | same |

Two more variants, both on Claude:

- A Python `anthropic` `messages.stream()` killed with SIGKILL after two deltas: the next request got 200.
- Two non-streaming requests killed while still waiting for the upstream's headers: the next request got 200.

So the proxy cancels the upstream request and gives the slot back as soon as the caller goes away, not when the vendor finishes. Pass.

An earlier attempt showed 429 here. It was a harness error: `kill` hit the backgrounding subshell, and `curl` kept reading. The stub log showed both streams running to completion. Once `curl` itself was killed (`exec curl`), the result was as above.

### 5. `-model-proxy-max-concurrent 0` and `-1`

| Value | Daemon | 20 concurrent OpenAI requests from run A (+1 from run B) |
|---|---|---|
| `0` | starts, no warning | 16 × 200, 4 × 429 "forwards at most **16** requests"; run B 200 |
| `-1` | starts, no warning | identical |

Both mean the default of 16. It is documented: `grain daemon --help` says "… 0 or less means the default (default 16)", and `pkg/daemon/config.go` / `Proxy.MaxConcurrent` say the same. There is deliberately no "unlimited". The daemon never logs the effective cap at startup (observation O1). Pass.

## Findings

### F1: at default settings, vendor clients fail requests the cap refuses (filed)

The proxy refuses immediately with `Retry-After: 1`. A slot frees only when a model call ends, which takes seconds for a short call and minutes for a stream. The vendors' clients give up long before that at their defaults:

- Anthropic and OpenAI: 2 retries at `Retry-After` intervals, so they give up about 2s after the call.
- Google's `genai` clients: no retries.

Any run that goes past the cap with stock clients therefore sees hard failures, not backpressure. The code comment says clients "already back off and retry on" the 429, and that only holds for a retry budget at least as long as the wait for a slot.

Deterministic reproduction:

1. `MaxConcurrent: 1` (or the daemon flag `2` with 3 concurrent calls) in front of an upstream that holds 5s.
2. The default `anthropic.Anthropic().messages.create(...)` for the call over the cap raises `RateLimitError` at about 2.0s, every time.

This was not fixed here. The remedy is a design choice: a bounded server-side wait for a slot, or a longer or adaptive `Retry-After`, or documenting the retry budgets. The cap's "refuse at once" contract is deliberate. It is filed as a follow-up for `main` with this reproduction and those options.

Resolved by task 3542 with the bounded wait: a request over the cap now waits up to `Proxy.SlotWait` (`-model-proxy-slot-wait`, default 60s) for one of its run's slots before the 429. `TestAStockClientCompletesWhenASlotFreesWithinTheWait` covers a client that retries twice at `Retry-After` intervals and one that never retries.

### Observations (no action filed)

- **O1. The effective cap is never logged.** `-model-proxy-max-concurrent -1` (or `0`) silently runs at 16, and the startup log doesn't say what the cap is. That matches `--help`, so it is not a bug. A line beside "the claude proxy is serving at …" would make a typo visible.
- **O2. Refusals surface only in the daemon log.** No UI pane or metric counts `too-many-requests` refusals. The plan listed "whatever surfaces refusals in the UI" as unproved, and nothing exists to prove.
- **O3. A WebSocket upgrade holds a slot for its whole life.** This is by reading, not measured. It applies to codex's Responses-over-WebSocket, through `ReverseProxy`'s upgrade copy. Under the default cap of 16 that is one slot per codex session, which is fine. Under a deployment cap of 1 or 2 it would starve the run's other calls.

## Fixes and tests on this branch

No product code changed. One regression test was added to `pkg/modelproxy/concurrency_test.go`:

- `TestACallerThatHangsUpMidStreamGivesItsSlotBack` (each vendor): a caller that closes its connection mid-stream, while the upstream still holds the reply open, gets its slot back. Before this, CI only proved that a stream holds its slot until it finishes; step 4 is what showed the release on disconnect.
  - It was checked against a mutation that hides the disconnect from the forwarded request (`context.WithoutCancel` plus a `ResponseWriter` without `CloseNotify`). There it fails with "status 429, want its slot given back" for every vendor, and it passes on `main`'s code.
- `TestAZeroMaxConcurrentMeansTheDefault` now covers `-1` as well as `0`, since step 5 confirmed the daemon passes a negative value through as given.

`go test -race -shuffle=on ./pkg/modelproxy/` passes.

## Stubs

| Stub | Stands in for | Notes |
|---|---|---|
| `stub.py`, TLS on `127.0.0.1:443`, reached through `/etc/hosts` + a throwaway CA in the daemon's `SSL_CERT_FILE` | the Anthropic, OpenAI and Google Gemini APIs | holds each request 5s, or streams one event per second for 10s; real vendor success shapes; logs arrival, concurrency, disconnects and the attached credential |

Nothing else was stubbed. The daemon, its proxies, token registry, team connections, secrets store and audit log were all grain's own. The SDKs and curl were unmodified.
