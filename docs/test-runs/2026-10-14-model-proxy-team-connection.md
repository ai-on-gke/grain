# Walkthrough: model proxy forwards with the run's team connection (2026-10-13 plan §5)

- **Plan:** `docs/test-plans/2026-10-13.md` §5, steps 1–7, plus the audit entries in the team audit log/API.
- **Base:** `candidate` at `e1bbddcc7` (2026-10-14 00:30 UTC).
- **Commits under test:** `63e5aea06` (forward with the run's team connection) and `77b09ad9c` (daemon-wired
  tests). `git branch -r --contains` lists both on `origin/candidate` and `origin/main`.
- **Task:** 3551 (filed by 3492).

## Verdict

Every step behaves as §5 describes for the Claude, OpenAI and Gemini proxies. Each run's proxied request
reached the stub vendor carrying its own team connection's key. None carried another team's key or the
deployment-wide legacy secret.

The walkthrough found two bugs, both fixed on this branch with regression tests:

1. **Model proxy refusals and usage limits never reached the team audit log/API.** They went only to the
   daemon's journal, which team admins can't read. This was the one §5 check that failed.
2. **A refusal sent team admins to a tab that doesn't exist.** Three refusals said "Team settings ->
   Connections"; the tab is "Models & connections".

Two behaviours are by design and recorded below, not filed:

- A usage-limit pause doesn't survive a daemon restart.
- On the host sandbox backend, a restart revokes a run's token instead of carrying it over.

## Environment

- **Daemon:** a real `grain daemon`, not `grain demo`. `grain demo` doesn't start the model proxies: its
  endpoints have no URL, so the capabilities refuse; see `pkg/daemon/modelproxy.go`.
  - **Build:** `go build -tags grain_e2e` of this checkout. The tag compiles in the host-directory sandbox
    backend the browser e2e suite uses (`pkg/daemon/local_sandboxes_e2e.go`), so the daemon dispatches real
    runs without a container runtime.
  - **Store:** the daemon's own on-disk SQLite store under `-data-dir`.
  - **Flags:** `-sandbox-dir`, `-as alice`, `-claude-path`/`-agy-path` pointing at the fake CLIs below,
    `-claude-oauth-token-file` (the legacy deployment secret) and `-github-host 127.0.0.1:9` (no forge).
- **Runs:** every request in this report came from inside a run's sandbox, using the token and URL that
  grain's capability placed there (`home/debian/.<vendor>-proxy-{url,token}`). Each token was minted on the
  real dispatch path (`orchestrator.RunDispatch` → capability `Materialize`). Nothing called
  `WireModelProxy` or `Endpoint.Mint` directly.
  - Tasks target each team's own `grain/config-<team>` repository (`grain/config` for the default team),
    which the daemon serves locally.
  - Tasks were created, given the `claude-proxy` / `openai-proxy` / `gemini-proxy` capability and approved
    through the daemon's own HTTP API, as `alice` in Admin mode.
- **Teams and connections** (credentials set with `PUT /api/agent-backends/{id}/credential`):

  | Team | Connections and keys |
  |------|----------------------|
  | default | `default-claude` (migrated from the legacy secret; key `sk-ant-oat01-legacy-deployment-key`) |
  | team-a | `team-a-claude` (`sk-ant-api03-team-a-key`); `team-a-borrow` (Claude, `credentialBackendId: team-a-claude`); `team-a-agy` (antigravity, `AIza-team-a-gemini`) |
  | team-b | `team-b-claude` (`sk-ant-api03-team-b-key`); `team-b-codex` (codex, `api_key`, `sk-proj-team-b-openai`) |
  | team-c | no Claude connection; `team-c-agy` (antigravity, `AIza-team-c-gemini`) |

- **Banner:** the UI was built with `cd ui && npm ci && npm run build` and embedded in the daemon binary.
  It was read in headless Chromium (Playwright's `chromium`) against the daemon's own server.

### Stubs (the only external boundaries)

- **Vendor stub:** one Go TLS server on `:443`, answering for `api.anthropic.com`, `api.openai.com` and
  `generativelanguage.googleapis.com`.
  - **Routing:** `/etc/hosts` maps the three hosts to `127.0.0.1`. The daemon runs with `SSL_CERT_FILE` set
    to the system bundle plus a throwaway CA that signed the stub's certificate. Grain's vendor upstreams are
    untouched: these are the real `Vendor.Upstream` constants.
  - **Recording:** each request is appended to `requests.jsonl` as `{host, method, path, key, limited}`. The
    key is taken from `x-api-key`, then `x-goog-api-key`, then `Authorization: Bearer`, then `?key=`.
  - **Usage limits:** any key listed in a `limit-keys` file is answered with a usage limit in that vendor's
    own shape:
    - **Anthropic:** 429, `anthropic-ratelimit-unified-status: rejected`, `anthropic-ratelimit-unified-reset`
      two hours out, and a `rate_limit_error` body.
    - **OpenAI:** 429 with `insufficient_quota` as both the error's `type` and its `code`.
    - **Google:** 429 with `RESOURCE_EXHAUSTED`.
  - **Other answers:** every other key gets a minimal success body in that vendor's shape.
- **Fake `claude` and `agy` CLIs**, standing in for the model CLIs a run forks. They are the model-provider
  boundary, like `ui/e2e/config.spec.js`'s fake `agy`.
  - **Sandbox root:** the host backend runs the CLI in the daemon's own directory, so `claude` reads its
    sandbox root from the `-sandbox-root` in the `--mcp-config` grain hands it, and `agy` reads it from
    `--add-dir`.
  - **Behaviour:** each logs the agent credential grain gave it (`CLAUDE_CODE_OAUTH_TOKEN`), then waits.
    For each `call-N` / `ocall-N` / `gcall-N` file dropped in its sandbox, it calls the Claude / OpenAI /
    Gemini proxy with that run's placed token, using curl, and saves the answer as `resp-N` / `oresp-N` /
    `gresp-N`. It exits with a success stream once `agent-finish` appears.

## Steps and outcomes

| # | Step | Outcome |
|---|------|---------|
| 1 | Real daemon with stub vendors; teams A, B with Claude connections, C with none | **Pass.** The daemon logged all three proxies serving (`claude` :9744, `openai` :9745, `gemini` :9746). |
| 2 | A's run calls the proxy; then B's | **Pass.** Run 5-1 (team-a) → stub saw `sk-ant-api03-team-a-key`. Run 6-1 (team-b) → `sk-ant-api03-team-b-key`. Both 200. Each run's agent was also handed its own team's key. |
| 3 | Dispatch on C | **Pass.** See "Step 3 in detail" below. |
| 4 | Borrower forwards with its source's key; audit names the source | **Pass**, as a same-team borrower. See "Step 4 in detail" below. |
| 5 | Usage limit pauses only A: banner, A's next proxy call and next dispatch held, B keeps forwarding | **Pass.** See "Step 5 in detail" below. |
| 6 | Restart mid-run; persisted token keeps its connection | **Pass for what the host backend can show.** See "Step 6 in detail" below. |
| 7 | Store holding only the legacy secret; default team forwards after migration | **Pass.** See "Step 7 in detail" below. |
| — | Audit entries in the team audit log/API | **Failed before the fix, passes after.** See "Audit entries" below and finding 1. |

### Step 3 in detail

- **No connection at all:** with no connection, C's task waited with "team "team-c" has no enabled provider
  connection".
- **With an agy connection:** after C was given one, dispatch refused the `claude-proxy` grant in the
  capability's `Resolve`: "this task asks to call the Claude API through grain's own proxy, but team team-c
  has no Claude connection for the proxy to forward with -- it forwards only with the run's own team's
  connections…". The task was parked (`awaiting_reply`) on grain's comment, not retried. Nothing reached
  the stub.
- **Anthropic shape:** a run that never gets a token never reaches the proxy, so the Anthropic-shaped
  refusal for "no connection" was shown on a run that lost its connection mid-run instead. Run 11-1 (team-a)
  was still live when `team-a-borrow` and `team-a-claude` were deleted. Its next call got
  `502 {"type":"error","error":{"type":"api_error","message":"connection team-a-claude is no longer one of
  team team-a's connections, so this grain proxy has no credential of the team's to forward with on it;
  …"}}`. Nothing reached the stub.
- **OpenAI shape:** switching `team-b-codex` to a ChatGPT `subscription` login mid-run was refused in OpenAI's
  shape: `502 {"error":{"type":"api_error","code":null,"param":null,"message":"connection team-b-codex of team
  team-b authenticates with a ChatGPT subscription login, which gives this grain OpenAI proxy no key…"}}`.
  Nothing reached the stub.

### Step 4 in detail

- **Cross-team borrowing is refused when the connection is saved.**
  `PUT /api/teams/team-a/agent-backends/team-a-borrow` with `credentialBackendId: team-b-claude` →
  `credentialBackendId must name another of team "team-a"'s own connections`. So "A borrows B's backend" can
  only be a borrower within one team.
- **Same-team borrower:** run 8-1 ran on `team-a-borrow`. Its agent got A's key, and the stub saw
  `sk-ant-api03-team-a-key`.
- **Audit:** the usage limit met through the borrower (step 5) was logged against the source:
  `claude proxy: POST /v1/messages for run "8-1" met a usage limit on connection "team-a-claude" (team team-a)`.
  The pause was keyed on `team-a-claude` too.

### Step 5 in detail

The stub was told to limit A's key.

- **The pause:** run 8-1's call met the Anthropic usage limit. `GET /api/pause` showed exactly one pause:
  `connection team-a-claude`, `connectionName "Team a Claude"`, `team team-a`, until the stub's reset header
  (2h).
- **Live runs:** the orchestrator cancelled the live runs on that connection (`cancelling 2 live run(s)
  [5-1 8-1]`).
- **A's next dispatch is held.** Tasks 5 and 8 went back to `queued` with "waiting: connection
  "team-a-claude" is paused after a usage limit on its credential".
- **B keeps forwarding.** B's run 6-1 kept running and its next call was forwarded with B's key (200).
- **A's next proxy call is held.** The step was repeated with run 11-1 (team-a, agent on `team-a-agy`,
  `claude-proxy` minted on `team-a-claude`), so a run stayed live across the pause. Its first call met the
  limit and paused `team-a-claude`; the run, whose agent is not on that connection, kept running. Its next
  call got `429 {"type":"error","error":{"type":"rate_limit_error","message":"connection team-a-claude of
  team team-a is paused after reaching its Anthropic usage limit; …"}}`. The stub's request count didn't
  change. Journal: `refused … (team "team-a", connection "team-a-claude"): connection-paused`.
- **Banner:** the real UI, in headless Chromium, with B paused the same way. It read: "Agent usage limit
  reached: nothing is being dispatched onto connection Team b Claude (team team-b) until 2:46:55 AM (about
  1h 59m). — claude: usage limit reached; You've hit your usage limit (stub); …".
- **OpenAI and Gemini:**
  - `insufficient_quota` on `sk-proj-team-b-openai` paused only `team-b-codex`; B's run on `team-b-claude`
    went on.
  - `RESOURCE_EXHAUSTED` on `AIza-team-c-gemini` paused only `team-c-agy`. Its length was the 15-minute
    default, since Google gave no retry delay.
  - Before the limits: C's Gemini run forwarded with `AIza-team-c-gemini`, and B's OpenAI run forwarded with
    `sk-proj-team-b-openai`.

### Step 6 in detail

- **What is persisted:** `secrets/modelproxy-claude-tokens.json` records each token's owner run, `team` and
  `connection`; for example `{"owner":"10-1","team":"team-b","connection":"team-b-claude"}`. Grain persists
  the binding, not a re-resolution.
- **Graceful stop (SIGTERM):** host-sandbox runs are interrupted and none are left for the next start to
  adopt.
- **SIGKILL:** on restart, recovery finished run 10-1 as interrupted and reaped its sandbox. The token was
  then revoked: "claude proxy: revoked 1 token(s) held by run(s) that ended with the previous process". That
  is correct for a run that can't be adopted.
- **Not shown:** a token forwarding after a restart, with its connection, on a run that *is* adopted.
  - **Why:** that needs a backend whose sandboxes outlive the daemon (granule). This environment has no
    granule or container runtime.
  - **Covered by:** `TestTokensPersistRoundTrip` in `pkg/modelproxy`.

### Step 7 in detail

- **The legacy store:** started from a store that held a configuration (`agentFramework: claude`) and only
  the legacy deployment secret (`-claude-oauth-token-file`), with no connections and the migration markers
  absent.
- **Migration:** at startup the daemon logged "ui: gave team default connection default-claude, a copy of the
  deployment's own framework and key".
- **Forwarding:** a default-team run (task 13 on `grain/config`) minted on `default-claude` (persisted
  `{"owner":"13-1","team":"default","connection":"default-claude"}`). Its call reached the stub with
  `sk-ant-oat01-legacy-deployment-key`, as a bearer OAuth token, and got a 200.
- **Non-default team:** team-c, with no Claude connection, was refused readably (step 3).
- **Fresh store:** with no configuration, migration only writes its markers and makes no connection
  (`MigrateTeamAgentBackends`, `cfg == nil`). So this step needs a store that was configured before the
  upgrade, as described here.

### Audit entries

- **Before the fix:** `GET /api/teams/team-a/audit-logs` held only `kind: git` entries from the git proxy.
  None of the model proxy's refusals or usage limits were there. They went only to the daemon's journal.
- **After the fix**, a usage limit on B's run:

  ```json
  {"timestamp":"2026-10-14T00:43:57.67Z","kind":"model-proxy","teamId":"team-b","runId":"14-1",
   "resource":"claude /v1/messages","action":"POST","credential":"team-b-claude","outcome":"usage-limit"}
  ```

- **Gemini:** a Gemini limit was recorded to team-c with `credential: team-c-agy`.
- **Rendering:** the team Audit tab (`TeamAuditPanel.jsx`) renders `kind`, `credential` and `outcome`
  generically, so the new entries show with no UI change.

## Findings

### 1. Model proxy refusals and usage limits were not in the team audit log (fixed)

- **What was wrong:** `startModelProxy`'s `Audit` callback only called `log.Printf`. So refusals, and usage
  limits that paused a team connection, appeared only in the deployment journal. Team admins can't read
  that journal (`/api/logs` is system-admin only). `GET /api/teams/{id}/audit-logs` never showed them,
  though §5 says "audit entries name the team and the connection".
- **Reproduction:**
  1. Run the daemon as above.
  2. Dispatch a team-b task with `claude-proxy`.
  3. List B's key in the stub's `limit-keys`.
  4. Call the proxy from the run.
  5. Read `GET /api/teams/team-b/audit-logs`. Before the fix it held no `model-proxy` (or any non-git)
     entry; after it, it holds the entry quoted above.
- **Fix:** `pkg/daemon/modelproxy.go`.
  - The callback is now `modelProxyAudit(store)`. Besides the journal line, it records each refusal or usage
    limit whose token named a team as that team's credential access, through `store.RecordCredentialAccess`,
    the same table and API the git proxy's team audit uses.
  - The record has kind `model-proxy` (`model.CredentialAccessModelProxy`) and names the run, the vendor and
    path, the method, the connection and the outcome. Nothing in it is credential material.
  - Forwarded requests are still unrecorded, as in the journal. A request no token authenticated is charged
    to no team.
- **Test:** `TestModelProxyRecordsRefusalsInTheRunsTeamAuditTrail` (`pkg/daemon`). It drives a proxy wired
  with `modelProxyAudit`:
  - Alpha's usage limit, then alpha's paused refusal, land in alpha's trail, newest first, naming
    `alpha-claude` and `run-alpha`.
  - Beta's forwarded request and an unknown token land nowhere.

### 2. Refusals named a "Team settings -> Connections" tab that does not exist (fixed)

- **What was wrong:** three refusals sent team admins to "Team settings -> Connections":
  - the `<vendor>-proxy` capability's "team X has no <vendor> connection" refusal, which team C got in
    step 3;
  - two Vertex refusals in `pkg/daemon/modelproxy_vertex.go`.

  The tab is "Models & connections" (`TeamSettingsOverlay.jsx`), which every other proxy refusal already
  names.
- **Reproduction:** step 3's refusal text.
- **Fix:** only the wording changes. Nothing that was refused is now honoured.
- **Test:** `TestResolveRefusesWithoutAProxyOrACredential` (`pkg/capability/modelproxy`) now requires any
  remedy to name "Team settings -> Models & connections". It fails on the old text for all three vendors.

### By design, recorded and not filed

- **The usage-limit pause doesn't survive a restart.** After a restart inside A's paused window, A's held
  tasks dispatched again onto `team-a-claude`. `pkg/orchestrator/pause.go` documents this as intended: the
  pause is in-process state, and "a daemon restarted inside a paused window dispatches one run, meets the
  same limit, and pauses again".
- **A usage limit tears down the caller.** A vendor usage limit met through the proxy cancels every live
  run whose agent spends that connection, including the run that made the call. That caller never sees the
  vendor's 429, because its process is torn down. This is the "proxy traffic and agent runs on one key pause
  together" of §5. A run whose agent is on another connection keeps running and gets the 429, then the
  paused refusal.
- **The task's waiting reason names the borrower.** A borrower's held task says "connection
  "team-a-borrow" is paused" while the pause itself is keyed on its source, `team-a-claude`. This is
  cosmetic only: the task is held correctly.

## Not covered here

- **A token forwarding across a restart on an adopted run.** This needs granule or remote sandboxes (step 6
  above). It is covered in CI by `TestTokensPersistRoundTrip` (`pkg/modelproxy`).
- **The Vertex AI translation path.** It wasn't in §5's steps, and the refusal tests in `pkg/daemon` cover it.

## CI

The grain CI verdict is reported in task 3551's closing note. The full suite was run with `wait_for_checks`
on the branch's final commit.
