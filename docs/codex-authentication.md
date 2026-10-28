# Codex authentication

In **Settings → Models**, add a Codex backend and choose its authentication:

- **ChatGPT subscription (auth.json):** upload `auth.json` from your Codex
  login, then press **Save login**. Each backend stores its own writable login
  file so refreshed tokens are retained. API keys are ignored in this mode.
- **OpenAI API key:** set the backend's key. A missing key is an error; Grain
  does not silently switch to a subscription.
- **Automatic (API key, then subscription):** preserves the behavior of
  existing backend entries that do not specify an authentication mode.

An existing deployment-wide upload or `-codex-auth-file` remains the fallback
when a backend has no login of its own. The UI reports when it is using a
shared login. Uploading, replacing, or clearing one backend's login never
modifies another backend's login or the shared file. Clearing a backend login
makes the shared fallback eligible again.

Backend configuration stores only `authMode` (`subscription`, `api_key`, or
omitted for automatic). Credentials remain outside configuration in the
controller's secrets directory. The API reports presence, never file contents:
`GET`, `PUT`, and `DELETE /api/agent-backends/{id}/codex-auth`. PUT accepts
`{"value":"<auth.json contents>"}`. The legacy `/api/codex-auth` upload endpoint
continues to manage the deployment-wide login.

## Several processes, one login

Every codex run on a backend shares one login. `WithAuthFile`
(`pkg/agent/codex/codex.go`) symlinks the backend's `auth.json` into each
run's private `CODEX_HOME`. Under the non-disruptive-upgrade design
(`non-disruptive-upgrade.md`), runs from different daemon generations
share it too. A ChatGPT login rotates its refresh token on every refresh,
so this section records what the pinned CLI (`ReviewedCLIVersion`,
0.153.0) does when several processes need a refresh together.

**Verdict: not safe to leave to the CLI.** Two processes can both spend
the same refresh token. On one of codex's two refresh paths, that fails a
run with "Your access token could not be refreshed because your refresh
token was already used. Please log out and sign in again." In the stub
below the shared file survives, so later runs still work. Whether the
real authority (`auth.openai.com`) also revokes the *rest* of the token
family when it sees reuse, which would log every process out, is
something a stub cannot show. Nobody has measured it against the real
service.

### What the source does

From `codex-rs/login/src/auth/manager.rs` and `auth/storage.rs` at
`rust-v0.153.0` (commit `41e22fee981a`):

- **Proactive refresh** (`AuthManager::auth` → `refresh_token`): this
  runs when the access token's JWT `exp` is under 5 minutes away, or,
  when `exp` cannot be read, when `last_refresh` is over 8 days old. It
  first does a *guarded reload*: it re-reads `auth.json` from disk, and
  if the file differs from what the process has cached, it adopts the
  file and skips the refresh. If the file is unchanged, it posts the
  refresh token to the authority.
- **401 recovery** (`UnauthorizedRecovery`): step 1 reloads `auth.json`
  (only if the file holds the same account id) and retries. Step 2
  refreshes from the authority with whatever step 1 loaded. Step 2 does
  **not** re-check the file after taking the lock, and a refused refresh
  is the end of recovery: the error goes to the run.
- **Locking:** a `tokio` semaphore (`refresh_lock`) serializes refreshes
  *inside one process*. Nothing locks across processes: no `flock`, no
  lock file.
- **Writing:** `persist_tokens` re-reads `auth.json`, sets the new
  tokens and `last_refresh`, and saves. `FileAuthStorage::save` opens the
  file with `O_TRUNC` (mode 0600) and writes it **in place**. There is
  no temporary file, no rename and no fsync. Writing in place is what
  keeps grain's symlink working, because a rename would replace the
  link inside the private home rather than the shared target. The cost
  is a short window in which another process can read a truncated or
  half-written file. That read fails to parse, and codex treats it as an
  account mismatch for that attempt.
- **A refused refresh** (`refresh_token_reused`, `_expired`,
  `_invalidated`, or any 401) counts as permanent for that cached token.
  It is recorded in memory only. codex does not delete or rewrite
  `auth.json` on failure.

### What the probe measured

`TestLiveCodexSharedLoginSurvivesConcurrentRefresh`
(`pkg/agent/codex/auth_refresh_live_test.go`) runs two real codex
processes through `Framework.Run` with one `WithAuthFile`. It uses a stub
token authority (`CODEX_REFRESH_TOKEN_URL_OVERRIDE`) that rotates refresh
tokens and refuses a spent one with `refresh_token_reused`. The stub
model accepts only access tokens that authority issued. The authority
holds the first refresh request until a second arrives, so a race is
forced rather than left to timing. No credential is involved. It runs
nightly in the "Live codex probes (no credential)" step of
`.grain/workflows/live-frameworks.yml`.

| Case | How the refresh starts | Result with 0.153.0 |
| --- | --- | --- |
| `expired` | stored access token already expired; both processes refresh proactively | both send `rt-0`, one is refused. The loser's next `auth()` reloads the file the winner wrote, and **both runs finish** |
| `rejected` | token not expired, but the model returns 401 to both | both reload (no change), both send `rt-0`, one is refused, and **that run fails** with the "already used" error |
| `stale` | as `rejected`, but the second process gets its 401 only after the first has rotated | the second reloads the rotated file and retries. **No second refresh, both finish** |

In every case the shared `auth.json` ends up holding the refresh token
the authority will honour next. With a strict-rotation authority, the
login itself is never lost. A single run can be. The test fails if the
login is lost, or if any run fails other than the known `rejected` loss.

### What this means for grain

- The common path (access token reaches its `exp`) survives the race,
  but only because the loser's spent token is forgiven. Every concurrent
  expiry presents a reused refresh token to OpenAI. If OpenAI treats reuse
  as theft and revokes the token family, which OAuth 2.0 security
  guidance recommends, every process sharing the login is logged out and
  someone has to upload a new `auth.json`.
- The 401 path loses a run outright whenever two processes hit it
  together.
- So the safe arrangement is the fallback named in
  `non-disruptive-upgrade.md`: grain renews the shared login on a
  schedule, ahead of the 5-minute window, under one lock, and writes it
  atomically. The CLIs then always find a fresh access token and never
  refresh. grain now does this; see the next section.
- A `CODEX_HOME` on a filesystem without coherent in-place writes (for
  example gcsfuse) is unsafe for a different reason than the upgrade
  design gave: codex 0.153.0 does not rename at all.

### grain renews the login itself

The daemon runs a login refresher (`codex.Refresher`,
`pkg/agent/codex/refresh.go`) over every subscription login its codex runs
can share: each backend's `codex-auth-*.json`, the UI-managed
`codex-auth.json`, and an operator's `-codex-auth-file`. `grain executor
-codex-auth-file` runs one over its own login. Once a minute it reads each
file and:

- **Decides from the access token's JWT `exp`.** A login is due halfway
  through the token's life (from `iat`, else from `last_refresh`). It is
  never later than 10 minutes before `exp`, which is twice codex's own
  5-minute window. When the lifetime cannot be read, it is due an hour
  before `exp`. When `exp` cannot be read either, it is due once
  `last_refresh` is 4 days old.
- **Uses codex's own grant.** It sends `POST https://auth.openai.com/oauth/token`
  with `{client_id: "app_EMoamEEZ73f0CkXaXp7hrann", grant_type:
  "refresh_token", refresh_token}`. It honours
  `CODEX_REFRESH_TOKEN_URL_OVERRIDE`, as codex does, and sends
  `User-Agent: grain-codex-login-refresher`.
- **Locks each login.** An in-process mutex covers one daemon. An
  `flock` on `.<file>.refresh.lock`, beside the login in the credential
  directory, covers different daemon generations. The file is read again
  under the lock, so a refresh another generation has just done is kept
  rather than repeated. UI upload and clear take the same lock, so a
  refresh in flight cannot write an old login back.
- **Merges and writes atomically.** The existing JSON is kept, unknown
  keys included. It sets `tokens.{id_token,access_token,refresh_token}`
  (whichever the authority returned) and `last_refresh`, writes a 0600
  temporary file, fsyncs it, and renames it over the file the path
  *resolves to*. Runs' `auth.json` symlinks therefore keep pointing at the
  renewed login. codex re-reads the file by path, so it picks up the new
  inode.
- **Reports a permanent refusal without deleting anything.**
  `refresh_token_reused`, `_expired`, `_invalidated`, `invalid_grant`, or
  any other 401, is recorded in `.<file>.refresh-failure.json` with a
  fingerprint of the refused refresh token. Nothing more is sent for that
  token. The backend's login status in Settings → Models shows the error
  (`refreshError` / `sharedRefreshError` from
  `GET /api/agent-backends/{id}/codex-auth`, and `codexAuthRefreshError`
  from `/api/agent-keys`). Uploading a new `auth.json` clears it. A
  transient failure (network, 5xx) is only logged and retried on the next
  pass.

The same probe proves it. Its `expired+refresher`, `rejected+refresher`
and `stale+refresher` cases run both codex processes while the refresher
runs against the shared file. Against 0.153.0, the authority sees one
refresh, from grain, and **zero refresh requests from codex**. Both runs
finish in every case, including the 401 case that otherwise loses one.

## Claude: a long-lived token instead of a refreshed login

The pinned claude CLI (`CLAUDE_CLI_VERSION` 2.1.286 in the Dockerfile)
supports `claude setup-token`, which prints a long-lived subscription
token (`sk-ant-oat01-…`). Run against a stub Messages API with
`CLAUDE_CODE_OAUTH_TOKEN` set and an empty `HOME`, 2.1.286 did the
following:

- It sent the token as `Authorization: Bearer <token>`, with the
  `oauth-2025-04-20` beta, and no `x-api-key`.
- It wrote nothing containing the token under `$HOME`, and made no
  request to any token endpoint.
- When the API answered 401, it made no refresh attempt. The run ended
  with `Failed to authenticate. API Error: 401 …`.

There is no refresh token to rotate, so any number of processes and
daemon generations can hold the token at once. When it expires (a year
after issue) or is revoked, someone runs `claude setup-token` again and
pastes the new value.

grain passes it already: the daemon reads secret `claude-oauth-token`
(`secrets.ClaudeOAuthTokenSecret`, set in Settings) or
`-claude-oauth-token-file`. It then hands the value to `claude.WithOAuthTokenFunc`
(`pkg/daemon/agents.go`), which puts it into the subprocess's environment
as `CLAUDE_CODE_OAUTH_TOKEN`, never on the command line
(`pkg/agent/claude/claude.go`). It is re-read on every run, so a replaced
token takes effect on the next run. Two exceptions: runs on a remote
host (`remoteHost != nil`) and the ambient auth mode do not get it from
grain.
