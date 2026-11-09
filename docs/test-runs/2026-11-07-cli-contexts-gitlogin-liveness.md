# §8 Walkthrough — CLI contexts, login/whoami, wait/inbox/reply/logs, git-login & dispatch conditions

- **Plan:** [`docs/test-plans/2026-11-07.md` §8](../test-plans/2026-11-07.md) (PRs #512, #685, #687, #702, #712, #717, #765, #782)
- **Date:** 2026-11-07, 22:30–23:00 UTC
- **Task:** 3492 (origin task 3157), branch `grain/098073e4/task-3492-walkthrough-cli-contexts-login`
- **Base tested:** `f55b8c73` (`origin/main` at start, PR #867)
- **Scripts & raw output:** [`2026-11-07-cli-contexts-gitlogin-liveness/`](2026-11-07-cli-contexts-gitlogin-liveness/)

## Environment

Debian sandbox VM (linux/amd64), Go 1.26.10, Node 20.19. `grain` built from `HEAD` (`go build ./cmd/grain`; `ui` built with
`npm ci && npm run build` before the UI screenshots). The in-sandbox CLI auto-detection was switched off for every
workstation command with `GRAIN_SANDBOX_CLI=0`, and each script runs in its own throwaway `HOME` / `XDG_CONFIG_HOME`.

| Server | What it is | Used for |
|---|---|---|
| A `127.0.0.1:4291` | `grain demo -github-sim -fake-ci -reconcile 2s -access-code-file …` (access code only) | contexts, `whoami`, `git-login`, seeded `awaiting_reply` reasons, `record.md` |
| B `127.0.0.1:4292` | same, with `externalAgentApi` turned on (`PUT /api/config`) | `grain login --issue-token` (`gma_`), revocation on logout |
| C (ephemeral port) | `ui/e2e/consolerestserver` — real `ui.Server` + `orchestrator.RunCycle`, scripted agent making real MCP calls through `grain mcpserver` | the external-agent loop: `create --depends-on`, parks on `ask_question` / `request_secret` / `request_access`, `wait`, `inbox`, `reply`, `logs` |
| D `127.0.0.1:4293` | real `grain daemon -data-dir … -host-sandboxes -sandbox-port-base 24576` (SQLite, real secret store and redactor) | a live dispatch-down condition with a stored secret inside the error |
| stub | [`deviceflow-stub.py`](2026-11-07-cli-contexts-gitlogin-liveness/deviceflow-stub.py) — GitHub OAuth device-flow + `/api/v3/user` | `grain git-login -web` polling states |

Only boundaries the sandbox cannot reach were simulated: the GitHub OAuth device flow (local stub), the keyring
(a fake `secret-tool` that records its argv and keeps secrets in files), and the agent model (consolerestserver's
scripted turns). Access codes and `gma_` tokens visible in the raw `.out` files belong to these throwaway local
servers, which no longer exist.

## Results

### 1. Named contexts, `login` / `logout` / `whoami`, credential security — [`contexts.sh`](2026-11-07-cli-contexts-gitlogin-liveness/contexts.sh) ([before](2026-11-07-cli-contexts-gitlogin-liveness/contexts.before.out) / [after](2026-11-07-cli-contexts-gitlogin-liveness/contexts.out))

| Check | Result |
|---|---|
| `whoami` unauthenticated (text / `-json`) | exit 3, `unauthenticated`, `fix: grain login <url>` ✅ |
| `login A --token-stdin` without `--auth access-code` | refused, exit 2 ✅; with it: stored, operator-authority warning on stderr ✅ |
| `login B` with the access code in `GRAIN_TOKEN` | refused (`access_code_refused`, B accepts `gma_`) ✅; `--issue-token` mints a `grain/external-dev` `gma_` with expiry ✅ |
| `whoami` as access code / `gma_` | `principal: operator` / `agent:external:cli-…`, owner, scope set, scopes, expiry ✅ |
| `config.json` and `credentials/<ctx>` modes; secret in `config.json` | both `0600`, credentials dir `0700`; `config.json` holds only the locator ✅ |
| `--context` beats current; `--context` beats `GRAIN_CONTEXT`; `GRAIN_CONTEXT` beats current | ✅ |
| `GRAIN_SERVER=B` with current=a / `--server A --context b` | the other server's credential is **not** sent (exit 3, not signed in) ✅ |
| `GRAIN_TOKEN` overrides the context credential | ✅ (garbage `gma_` refused, error names `$GRAIN_TOKEN`) |
| `credentials/a` chmod `0644` | refused, exit 3, `fix: chmod 0600 …` ✅ |
| `ps auxww` / `/proc/<pid>/cmdline` during `grain --context b logs 3 -follow` | no secret ✅ |
| `DBUS_SESSION_BUS_ADDRESS` set, no `secret-tool` | falls back to the `0600` file ✅ |
| fake `secret-tool` | `keyring:grain/k`, secret on stdin (argv `store --label=grain/k service grain account k`) ✅ |
| keyring locked (`secret-tool` fails) | falls back to the `0600` file ✅ |
| keyring context read from a session with no D-Bus | exit 3 `credential_unavailable` ✅ |
| **file → keyring re-login** | **F1:** old secret left in `credentials/fk` → fixed |
| **`logout` of a keyring context from a session that cannot reach it** | **F2:** said "signed out", secret stayed in the keyring → fixed |
| `logout b` | token revoked on B (`external agent bearer token has been revoked`), file removed ✅ |
| verified-user (`IAP`) `whoami` | not re-run here; covered live by `2026-11-04-workstation-cli.md` (iapsim), unchanged since |

### 2. External-agent loop — [`lifecycle.sh`](2026-11-07-cli-contexts-gitlogin-liveness/lifecycle.sh) ([output](2026-11-07-cli-contexts-gitlogin-liveness/lifecycle.out))

| Check | Result |
|---|---|
| `create` without `-repo` from a `gma_` token | exit 3 `forbidden` ("requires an explicit repo") — see O1 |
| `create -approve` from a `gma_` token | refused (needs `tasks:admin`); filed `proposed`, operator approves ✅ |
| `create -depends-on T1` | `depends on: 2 (proposed)` in the create echo ✅ |
| `list` / `show` / `show -json` | `T2 … (waits on 2)`, `depends on: 2 (running)`, `dependsOn: ["2"]`, `blockedBy: ["2"]`, `blocked: true` ✅ |
| `task://<id>/record.md` | `Depends on: 8 (running)` and `Reason: queued/blocked_on_task (…)` on server A ✅ (consolerestserver serves no namespace: 503 there, harness limit) |
| `wait -until awaiting-reply` on `ask_question` | wakes, `question: …` ✅ (printed twice before the fix — **F4**) |
| `show` on the park | `reason: awaiting_reply/question [Reply]` (after **F3**; before, only `-json` had it) |
| `inbox` / `inbox -json` | `question #2 T1 external loop -- Which retry budget should T1 use?` ✅ |
| `reply` on a question | answered, requeued ✅ |
| `request_secret` park | `wait` says `secret requested: widgets-deploy-key`; `show` says `waiting on secret …`; `inbox -kind secret` lists it ✅ |
| `reply` to a secret park | 409: "grain reply never posts a credential into a conversation" ✅ |
| `PUT /api/tasks/{id}/secret` with the agent's `gma_` | 403 ✅; operator's 200 requeues it (after the harness fix, below) |
| `request_access` park | `show`: `reason: awaiting_reply/access_request [Grant]` ✅ (#782); `inbox` lists it as `question … Grant read on acme/other` |
| `reply` to an access park | 409 "decide it on /tasks/2" ✅; the agent's token on `access-request/grant` → 403 "not part of the agent API" ✅ |
| grant → closing comment | `reason: awaiting_reply/answered [Reply]` ✅; operator `close` → `wait` returns `closed completed` ✅ |
| T2 after T1 closed | dispatched at once and ran ✅ |
| `inbox -repo acme/other` / `-repo acme/widgets` | the repo-less Director question (#1) stays out of both filters ✅ |
| `wait`/`show`/`logs` on an unknown id | 404 → exit 2 `not_found` ✅ |
| token limited to `acme/other` on an `acme/widgets` task | 403 → exit 3 `forbidden` for `wait`, `show`, `logs`; `inbox` empty ✅ — see O2 |
| `logs <id>` / `-attempt 1 -tail 5` | transcript and `#2 attempt 1 finished: succeeded` ✅ |
| **`wait -until awaiting-reply` race** | **F6:** once woke with `state: queued` while the access request was still being posted ([excerpt](2026-11-07-cli-contexts-gitlogin-liveness/lifecycle.wait-race.out)); follow-up filed |

### 3. `grain git-login` device flow — [`gitlogin.sh`](2026-11-07-cli-contexts-gitlogin-liveness/gitlogin.sh) ([output](2026-11-07-cli-contexts-gitlogin-liveness/gitlogin.out))

| Stub answers | Result |
|---|---|
| `expired_token` | exit 1 "the GitHub device code expired … run `grain git-login` again" ✅ |
| `access_denied` | exit 1 "GitHub authorization was cancelled in the browser" ✅ |
| `authorization_pending → slow_down(interval 3) → authorization_pending → token` | polls 1 s apart, then 3 s after `slow_down` ✅, correct `grant_type` ✅, token stored, never readable back ✅ |
| explicit `-author "Dana Example"` | `Signed by Dana Example (via Grain)` ✅ (format matches `design/design/git-login.design.md`) |
| default author | **F5:** the stub's `/user` says `Octo Dev`, but the signature became `Signed by bsalmon (via Grain)` — the host's git identity (`applyHostGitIdentity`) overrides the credential owner on a host with ambient logins; follow-up filed |

A live PR + PR comment on `githubsim` signed through a delegated login was **not** walked: it needs a dispatched task
whose run opens a PR through a GitHub-backed daemon, which did not fit this run. `SignPullRequestBody` /
`SignComment` are covered by `pkg/github` unit tests; F5 shows the name they would be given.

### 4. Dispatch-down condition & redaction — [`dispatch-down.sh`](2026-11-07-cli-contexts-gitlogin-liveness/dispatch-down.sh) ([output](2026-11-07-cli-contexts-gitlogin-liveness/dispatch-down.out))

Fault: the git proxy's port held by another process, and a stored secret `bind-host/value` whose value is that
address (`127.0.0.1:24576`), so the bind error quotes a secret.

| Check | Result |
|---|---|
| journal | `dispatch blocked [git-proxy]: starting git proxy: listen tcp [redacted: secret bind-host/value]: bind: address already in use` ✅ |
| unauthenticated `GET /api/auth/config`, `/api/config`, `/api/sandboxes`, `/api/health` | access-code wall, nothing of the condition ✅; `/api/auth/whoami` carries no condition ✅ |
| `grain health` | `FAILING: dispatch is blocked …` one line, redacted, with remedy ✅ |
| `/api/sandboxes`, `/api/config` | `dispatchBlocked[0]` redacted, `reconcilerDown: true` ✅ |
| queued task: `show -json`, MCP `read_grain_task` | `reason: queued/dispatch_blocked` with the redacted condition ✅ (the plan's `grain_get_task` is called `read_grain_task` on `grain mcp`) |
| queued task: `grain show` text | before **F3** nothing; after: `reason: queued/dispatch_blocked (dispatch blocked -- git proxy: … [redacted: secret bind-host/value] …)` |
| the secret value on any authenticated surface | absent ✅ |
| UI (`/`, `/system`) with the daemon blocked | page text has the git-proxy condition and `redacted`, never the secret ([`ui-dispatch-blocked.out.json`](2026-11-07-cli-contexts-gitlogin-liveness/ui-dispatch-blocked.out.json)) ✅ |
| free the port, restart | `dispatchBlocked: null`, task back to `queued/ready` ✅ (conditions clear on restart, as `recordDispatchBlock` documents) |

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | Medium (secret hygiene) | `credentialStore.store` into the keyring left an earlier fallback file holding the old secret | **Fixed**: the file is removed once the keyring takes the secret. `TestCredentialStoreKeepsNoStaleSecret` |
| F2 | Medium (secret hygiene) | `grain logout` from a session that cannot reach the keyring dropped the locator and left the secret in the keyring | **Fixed**: logout stops before revoking/forgetting, exit 3 `credential_unavailable`, naming `secret-tool clear service grain account <ctx>`. Same test |
| F3 | Medium (#782 / #712 reach) | `grain show` text never printed `Task.Reason`, so the awaiting_reply sub-case and the dispatch-down condition reached only `-json` | **Fixed**: `reason:` line in `record.md`'s format, with the control. `TestShowSaysTheReasonSubCase` |
| F4 | Low | `grain wait` printed a parked task's question twice (question + identical closing note) | **Fixed**. `TestWaitSaysAParkedQuestionOnce` |
| F5 | Medium (attribution) | `git-login` without `-author` signs with the host's git identity instead of the credential owner's GitHub profile when ambient logins are available | Follow-up proposed (touches `pkg/gitproxy`, a reviewed path) |
| F6 | Low (race) | `wait -until awaiting-reply` is met by a pending question comment alone, so it can wake while the run is still finishing (`state: queued`), before a `reply` would be accepted | Follow-up proposed |
| H1 | Harness | consolerestserver had no secret store (request_secret could never be answered: 404) and no reason classifier | **Fixed** in `ui/e2e/consolerestserver` |
| O1 | Observation | A `gma_` `create` without `-repo` is exit 3 `forbidden` though the fix is a flag | Left as is (gate wording) |
| O2 | Observation | For a repo-scoped token, a forbidden task id says `holds tasks:read, but not on acme/widgets` while an unknown id says `… does not name a repository it reaches`: existence and repo name of other repos' tasks are distinguishable | Left as is; noted for the agent-API owners |

## Verification

- `go test ./pkg/cli -count=1` ✅, `go test ./tests/hygiene/... ./pkg/ports/...` ✅, `go vet ./pkg/cli ./ui/e2e/consolerestserver` ✅
- Every Playwright spec that runs on consolerestserver, after the harness change (`access-request-walkthrough`,
  `console-resting-notes-walkthrough`, `console-session-wake-inbox-walkthrough`, `interactive-session-chat`,
  `stopped-turn-retry`, `task-chat-optimistic-sends`): 17 passed ✅
- Follow-ups proposed for F5 and F6, both depending on this task.
- No command or flag changed, so `design/spec/cli/*.spec.md` needed no regeneration. No `ui/` source changed (screenshots were taken
  of the unchanged UI only to check the condition's text).
