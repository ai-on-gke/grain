# Walkthrough: model proxy Vertex AI forwarding, slot wait, team audit, revoked recovered tokens (2026-10-15 plan §7)

- **Plan:** `docs/test-plans/2026-10-15.md` §7, steps 1–5.
- **Task:** 3950 (filed by the nightly test-plan task 3749).
- **Base:** `candidate` at `591f0f35f` (2026-10-15 11:26 UTC).
- **Commits under test:** `eba055e5d` (Vertex AI forwarding), `1126d996d` (slot wait), `d6d7925e0` (team audit trail),
  `5e6884a54` (refusals name Team settings -> Models & connections) and `75f321358` (revoke recovered runs' tokens).
  `git branch -r --contains` lists every one of them on `origin/candidate` and `origin/main`, and all five are
  ancestors of the base.

## Verdict

Steps 1–5 all pass on this branch.

Step 2 failed on the base: **a run of a team whose Claude connection is on Vertex AI was never given a proxy
token** (finding 1). The `claude-proxy` capability refused it at dispatch for holding no Anthropic key, so the
Vertex forwarding of `eba055e5d` was unreachable from any dispatched run. It is fixed on this branch, with
regression tests at the capability and at the daemon wiring.

Once that was fixed:

- The proxy translated messages, count_tokens and streaming calls from the stock `anthropic` SDK exactly as §7
  describes, for both `us-east5` and `global`.
- Three stock-SDK calls through a cap of 1 completed one after another in 15.2s.
- With the slot wait turned off, the 429s came back. The flag needs a unit, `-1s`, not `-1` (finding 2: help
  text fixed).
- The refusal and the usage limit were in team A's Audit / Security tab, naming the connection, for team A's
  admin. Team B's admin saw neither.
- After a SIGKILL and restart, the old run token got a 401.

## Environment

- **Daemon:** a real `grain daemon`, built with `go build -tags grain_e2e ./cmd/grain` from this checkout, with
  go1.26.6. The tag compiles in the host-directory sandbox backend that the browser e2e suite's real-daemon specs
  use (`pkg/daemon/local_sandboxes_e2e.go`), so runs are dispatched for real with no container runtime.
  - After finding 1, the daemon was rebuilt from the fixed tree. Every outcome below is from that build.
  - **Store:** its own SQLite store under a scratch `-data-dir` (`~/wt/data`).
  - **Flags:**
    - `-sandbox-dir ~/wt/sandboxes` (absolute; see the harness note below);
    - `-ui-addr 127.0.0.1:8420 -as alice -poll-interval 200ms -max-workers 4 -max-mergers 0`;
    - `-claude-path`/`-agy-path` pointing at the fake CLIs below;
    - `-claude-oauth-token-file`, a throwaway legacy key;
    - `-github-host 127.0.0.1:9 -github-insecure-http`, so there is no forge;
    - per step: `-model-proxy-max-concurrent 1` and `-model-proxy-slot-wait=-1s`.
  - **Environment variables:** `SSL_CERT_FILE` (the system bundle plus the walkthrough CA). For the `global` step
    only, `CLOUD_ML_REGION=global`.
  - **Proxies:** the daemon logged the claude proxy serving at `127.0.0.1:9744`.
- **Teams and connections:** all made through the daemon's own REST API as `alice` in Admin mode.
  - **team-a** (admin `ta-admin`):
    - `team-a-vertex`: framework `claude`, `authMode: vertex`. Its credential, set with
      `PUT /api/agent-backends/team-a-vertex/credential`, is a throwaway service-account key: a fresh RSA key,
      `project_id: wt-vertex-project`, `token_uri: https://oauth2.googleapis.com/token`.
    - `team-a-agy`: antigravity, key `AIza-team-a-gemini`. Added for step 4 so that a run's agent could spend
      another connection than the one the proxy pauses.
  - **team-b** (admin `tb-admin`): `team-b-claude`, with an API key.
  - `ta-admin` and `tb-admin` were registered and signed in with `/api/auth/register` and `/api/auth/login`.
- **Run tokens:** every token used here was minted on the real dispatch path:
  1. Create a task on `grain/config-team-a` (`POST /api/tasks`).
  2. Grant `claude-proxy` (`POST /api/tasks/{id}/capabilities`).
  3. Approve it.
  4. `orchestrator.RunDispatch` → the capability's `Resolve`/`Materialize` → `home/debian/.claude-proxy-{url,token}`
     in the run's sandbox.

  The persisted registry (`data/secrets/modelproxy-claude-tokens.json`) recorded each token with
  `"team":"team-a","connection":"team-a-vertex"`.
- **Clients:**
  - **SDK calls:** Python 3.11.2 with `anthropic` 1.14.0 (over `httpx2` 2.13.1), installed into a venv from the
    package cache with `pip install anthropic`. Every proxy call in steps 2–5 is the stock SDK
    (`anthropic.Anthropic(base_url=<placed url>, api_key=<placed token>)`), at its default retries and timeout
    unless a step says otherwise. curl was used once, for a liveness probe before the kill in step 5.
  - **Browser:** Playwright 1.62.1 (headless Chromium 151, installed with `NODE_EXTRA_CA_CERTS` set to the
    package cache's CA) against the UI built by `cd ui && npm ci && npm run build` and embedded in the daemon.

### Stubs (the only external boundaries)

- **Vertex AI and Google's token endpoint:** one Python `ThreadingHTTPServer` with TLS on `127.0.0.1:443`.
  - **Routing:** `/etc/hosts` maps `us-east5-aiplatform.googleapis.com`, `aiplatform.googleapis.com` and
    `oauth2.googleapis.com` to `127.0.0.1`. The certificate is signed by a throwaway CA, and the daemon trusts it
    through `SSL_CERT_FILE`.
  - **No production override was used or added.** The proxy's only base-URL override, `Proxy.VertexUpstream`, is a
    test-only field that no daemon flag sets (`pkg/modelproxy/proxy.go`). So the real origins (`vertexOrigin`) were
    redirected outside the daemon, the same way the 2026-10-12 and 2026-10-14 walkthroughs redirected
    `api.anthropic.com`.
  - **`POST /token`:** answers `ya29.stub-access-N`. The daemon's request carried an RS256 JWT `assertion`, signed
    by the service-account key (`golang.org/x/oauth2/google`'s ordinary service-account flow).
  - **`…:rawPredict`:** answers an Anthropic Messages body, after `delay` seconds when that control file is set.
  - **`…/count-tokens:rawPredict`:** answers `{"input_tokens":42}`.
  - **`…:streamRawPredict`:** streams Anthropic SSE: `message_start`, a content block, five `text_delta`s,
    `message_delta` and `message_stop`, 200 ms apart.
  - **`limit` control file:** while it is present, every model call gets Google's 429 `RESOURCE_EXHAUSTED`.
  - **Recording:** each request goes to `requests.jsonl` with its host, path, `Authorization`, `x-api-key`, the
    parsed body, the number of requests open when it started, and its start and end times.
- **Fake `claude` and `agy` CLIs:** these stand in for the model CLIs a run forks. Each logs its arguments and
  waits for an `agent-finish` file, which was never written, so the runs stayed live for the proxy calls.

The stub and harness scripts are not committed. Neither is the Playwright spec, which reads entries only this
environment's runs wrote.

## Steps and outcomes

| # | Step | Outcome |
|---|------|---------|
| 1 | Stub Vertex endpoint (HTTPS with a walkthrough CA), recording, SSE for `:streamRawPredict` | **Pass.** |
| 2 | Real daemon, Vertex connection, dispatched run's token, stock SDK: paths, `model` out/in, streaming, `global` | **Failed on the base (finding 1); passes with the fix.** See "Step 2 in detail". |
| 3 | Cap 1, stub 5s, three concurrent SDK calls; then `-model-proxy-slot-wait` negative | **Pass**, with `-1s` rather than `-1` (finding 2). See "Step 3 in detail". |
| 4 | Paused-connection refusal and usage limit in Team settings -> Audit / Security; absent for another team's admin | **Pass.** See "Step 4 in detail". |
| 5 | SIGKILL mid-run, restart, replay the old token | **Pass.** See "Step 5 in detail". |

### Step 2 in detail

- **On the base:** task 4 on `grain/config-team-a` with `claude-proxy` was parked (`awaiting_reply`) at dispatch,
  on grain's comment: "it holds the capability `claude-proxy`, and grain refuses it. > this task asks to call the
  Claude API through grain's own proxy, but connection team-a-vertex of team team-a authenticates through Vertex
  AI, which gives this grain Claude proxy no key to forward with…". No token was minted, so nothing reached the
  proxy. See finding 1.
- **With the fix, region `us-east5`** (the default; run 6-4): `sdk_step2.py` ran `messages.create`,
  `messages.count_tokens`, `messages.stream` and `models.list`.

  | SDK call | Stub saw | Body as forwarded |
  |---|---|---|
  | `messages.create(model="claude-sonnet-4-6")` | `us-east5-aiplatform.googleapis.com` `/v1/projects/wt-vertex-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:rawPredict` | `{"anthropic_version":"vertex-2023-10-16","max_tokens":32,"messages":[…]}`, with **no `model`** |
  | `messages.count_tokens(...)` | `…/publishers/anthropic/models/count-tokens:rawPredict` | `{"anthropic_version":…,"messages":[…],"model":"claude-sonnet-4-6"}`, with **`model` kept** |
  | `messages.stream(...)` | `…/models/claude-sonnet-4-6:streamRawPredict` | no `model`, and `"stream":true` |

  - **Answers:** the SDK got `msg_stub` with text `Hello from stub Vertex.` and `end_turn`, and `input_tokens` 42.
  - **Streaming:** the stream arrived intact as five deltas, `['Hello', ' from', ' stub', ' Vertex', ' stream.']`,
    then a final message with `end_turn` and `output_tokens` 5.
  - **Model listing:** answered by the proxy itself (`claude-sonnet-5`, `claude-opus-4-8`, …), never forwarded.
  - **Credentials:** every forwarded request carried `Authorization: Bearer ya29.stub-access-N`, minted from the
    connection's key, and no `x-api-key`. The run's own token never reached the stub.
- **Region `global`** (`CLOUD_ML_REGION=global`, run 7-1): the same three calls went to `aiplatform.googleapis.com`,
  with no region prefix, at `/v1/projects/wt-vertex-project/locations/global/publishers/anthropic/models/…`. The
  answers were the same.

### Step 3 in detail

- **Default slot wait (60s), `-model-proxy-max-concurrent 1`, stub `delay` 5:** `sdk_step3.py` fired three
  `messages.create` calls at once from one stock client (`max_retries` 2, the default).

  | Call | Result | At |
  |---|---|---|
  | 0 | ok | 5.1s |
  | 2 | ok | 10.1s |
  | 1 | ok | 15.2s |

  The stub's record shows them strictly one at a time. They started at 0.0s, 5.0s and 10.1s, and each had 1 open
  request at its start. No refusal was logged.
- **`-model-proxy-slot-wait=-1`:** as the plan spells it, the daemon would not start: `grain daemon: invalid value
  "-1" for flag -model-proxy-slot-wait: parse error`. See finding 2.
- **`-model-proxy-slot-wait=-1s`** (run 8-1): one call succeeded at 5.0s. The other two raised `RateLimitError`
  (429) at 2.0s, after the SDK's two retries: "This grain Claude proxy forwards at most 1 requests at once for one
  run…". The stub saw exactly 1 request. The journal had six `refused POST /v1/messages for run "8-1" (team
  "team-a", connection "team-a-vertex"): too-many-requests` lines: two calls, each tried three times.

### Step 4 in detail

- **Setup:** a usage limit on `team-a-vertex` cancels every live run whose agent spends that connection, including
  a caller on it (a by-design behaviour recorded in the 2026-10-14 report). So the refusal was shown from run 9-1.
  Its agent is on `team-a-agy` (task `agentBackendId: team-a-agy`, fake `agy`), and its proxy token was minted on
  team A's Claude connection, `team-a-vertex`.
- **Usage limit:** with the stub's `limit` file present, `sdk_step4.py` (`max_retries=0`) got 429 with Google's
  `RESOURCE_EXHAUSTED` body. The journal said: "met a usage limit on connection "team-a-vertex" (team team-a)",
  then "dispatch onto connection team-a-vertex (team team-a) paused until 11:53:46Z; cancelling 4 live run(s)".
- **Paused refusal:** the next call got 429 with `Retry-After: 60` and "connection team-a-vertex of team team-a is
  paused after reaching its Anthropic usage limit…". The stub saw only the first call.
- **Re-dispatch:** the held team-a tasks were re-dispatched at once onto `team-a-agy`, steering around the paused
  connection, as designed.
- **API:** `GET /api/teams/team-a/audit-logs` held both entries, with `kind: model-proxy`, `runId: 9-1`,
  `credential: team-a-vertex`, and outcomes `usage-limit` and `connection-paused`. It also held step 3's six
  `too-many-requests` refusals from run 8-1. Team B's log was empty.
- **Browser:** Playwright, with `extraHTTPHeaders: {}` and no Admin mode, signed in with `/api/auth/login`.
  - `ta-admin`, at `/teams/team-a/settings` → **Audit / Security**: the table's `model-proxy` rows include
    - `claude /v1/messages · POST · team-a-vertex · connection-paused · 9-1`
    - `claude /v1/projects/wt-vertex-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:rawPredict · POST · team-a-vertex · usage-limit · 9-1`

    plus the six `too-many-requests` rows. Passed.
  - `tb-admin`:
    - `GET /api/teams/team-a/audit-logs` answered **403**.
    - `/teams/team-a/settings` shows neither `team-a-vertex` nor `model-proxy`.
    - Team B's own Audit / Security tab reads "No credential audit events recorded yet for this team.".

    Passed.
  - Both tests passed in 2.9s.

### Step 5 in detail

- **Before the kill:** run 9-1 was live and its token was in the persisted registry. A probe with it got the
  paused 429, so the token was recognised.
- **Kill and restart:** `kill -9` on the daemon, then a restart over the same data directory.
  - **Recovery:** it recovered orphaned runs 7-4, 8-3 and 9-1 as interrupted, then logged "claude proxy: revoked 4
    token(s) held by run(s) that ended with the previous process".
  - **Replay:** the stock SDK with 9-1's old token got **401** `AuthenticationError`: "This grain Claude proxy does
    not recognise that token: it was never minted here, or the run that minted it has finished and its token has
    been revoked."
  - **Registry:** the persisted registry no longer lists owner `9-1`.

## Findings

### 1. `claude-proxy` was refused for every team whose Claude connection is on Vertex AI (fixed)

- **What was wrong:** `pkg/capability/modelproxy`'s `Resolve` asked the endpoint only for the connection's key
  (`ResolveCredential`). The daemon refuses a key for a Vertex AI connection on purpose (`modelProxyCredential`
  in `pkg/daemon/modelproxy.go`), because the proxy reaches Vertex by translating instead (`ResolveVertex` →
  `serveVertex`).
  - `Proxy.ServeHTTP` asks `ResolveVertex` first; the capability never did.
  - So a Vertex team's run was never given a token, and its task was parked at dispatch. `eba055e5d`'s forwarding
    was reachable only from a token minted by hand, which is how its tests mint.
- **Reproduction:**
  1. Run the daemon as above. Make a team whose only Claude connection is `authMode: vertex`, with a
     service-account key.
  2. Create a task on the team's config repo, grant `claude-proxy`, and approve it.
  3. On the base, the task goes to `awaiting_reply` with the refusal quoted in step 2, every time. No sandbox gets
     `.claude-proxy-token`.
- **Fix** (`06a095e7b`): `Resolve` asks `ResolveVertex` first, in the proxy's own order.
  - A target honours the capability.
  - A Vertex refusal refuses it with that refusal's own reason (no key, no project, or a token that can't be
    minted). Any other error refuses it with a sentence that doesn't repeat Google's error, which can quote the
    key.
  - Only a non-Vertex connection falls through to the key check as before. Nothing that was refused before is
    honoured now, except a Vertex connection the proxy really forwards on.
- **Side effect:** resolving a Vertex connection at dispatch reads its key and mints (or reuses) a Google access
  token. The key read shows in the team's audit log as a `secret` / `resolve` entry for
  `agent-backend-<connection>/value`, the same entry an agent run on that connection writes.
- **Tests:**
  - `TestResolveHonoursAConnectionOnVertexAI` and `TestResolveRefusesAVertexAIConnectionItCannotMintFor`, in
    `pkg/capability/modelproxy`.
  - `TestModelProxyForwardsAVertexConnectionToVertexAI` (`pkg/daemon`, `d193b9ca6`) now also resolves the
    capability over the daemon-wired endpoint. Alpha, which has a key, is honoured; beta, which has none, is
    refused with "no service account key".
  - All three fail on the base and pass with the fix.

### 2. `-model-proxy-slot-wait=-1` stops the daemon from starting (help text fixed)

- **What was wrong:** the flag is a Go duration, so a negative value needs a unit. The plan, and the sibling
  `-model-proxy-max-concurrent -1` (an int), suggest `-1`. That fails at startup with `parse error`. The help said
  only "less refuses at once".
- **Reproduction:** `grain daemon … -model-proxy-slot-wait=-1` exits with `invalid value "-1" for flag
  -model-proxy-slot-wait: parse error`, every time.
- **Fix** (`d5be4c917`): the help now says "a negative duration, such as -1s, refuses at once", and
  `cmd/grain/testdata/daemon_help.golden` follows (`TestDaemonHelpUnchanged` passes). Behaviour is unchanged: a bad value
  still refuses to start, which is safer than guessing a unit.

### Observations (not filed)

- **The usage-limit audit entry names the translated path.** On a Vertex connection, the usage-limit entry's
  resource is the outbound Vertex path (`claude /v1/projects/…/claude-sonnet-4-6:rawPredict`). The refusal entries
  for the same run name the caller's `/v1/messages`. This happens because `Proxy.entry` reads `resp.Request`, which
  is the rewritten request. Both name the call, and the Vertex one also names the project and model, so it was
  left as is.
- **The paused refusal says "Anthropic usage limit" for a Vertex connection.** The limit was Google's
  `RESOURCE_EXHAUSTED` quota. This is cosmetic.
- **The usage limit's 429 is passed through in Google's shape.** The caller got Vertex's `RESOURCE_EXHAUSTED`
  body, not an Anthropic-shaped error. The stock SDK still raised `RateLimitError` on the status, so nothing
  broke.
- **The too-many 429's wording with a negative slot wait.** It says the run "had that many open for as long as the
  proxy waits for one to finish", which is vague when the wait is zero, but not wrong.
- **Recovery logs "config repository … is not available yet".** It logs this once per orphaned run while
  recovering. The runs were still finished as interrupted and their tokens revoked.
- **Harness note: a relative `-sandbox-dir` breaks every clone on the host-directory backend.** `HOME` is set to
  the relative sandbox root while the command runs from inside it, so git finds no `.git-credentials` ("could not
  read Username"). Only the `grain_e2e` and demo backends use `NewHostSandboxes`, so this was worked around with
  an absolute path and not filed.

## Not run

- **A Vertex token forwarding across a restart for an adopted run.** It needs a backend whose sandboxes outlive
  the daemon (granule), which this environment does not have. As in the 2026-10-14 report, host-backend runs are
  finished as interrupted on restart and their tokens revoked (step 5).
- **The Node `@anthropic-ai/sdk`.** Only the Python SDK was used; the plan allows either.
- **A real Google token endpoint and a real Vertex AI project.** Both were stubbed, as the task asked.

## CI

The final commit's full grain CI suite is reported in task 3950's closing note (`wait_for_checks` without
`jobs`).
