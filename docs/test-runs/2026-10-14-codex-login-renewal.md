# Walkthrough: Codex Shared-Login Renewal Against a Stub Token Authority (2026-10-14)

Plan: `docs/test-plans/2026-10-13.md` §6 (task 3552, filed by 3492).
Base: `candidate` at `e1bbddcc7`. Both feature commits are on it:
`git branch -r --contains 237c93a12` and `… d5273f758` both list `origin/candidate` (and `origin/main`).

## Result

All six plan steps pass. The walkthrough found one bug and fixed it in this branch: a UI upload through a
symlinked login replaced the symlink instead of the login it points at. The fix has a regression test.
It also recorded one observation, about a refresh that times out at the client. It needs no change.

| Step | What was checked | Outcome |
|------|------------------|---------|
| 1–2 | Refresh timing, 0600, unknown keys, symlinks, on a real daemon | pass |
| 3 | Daemon + `grain executor` on one file, raced under a held refresh | pass (one refresh) |
| 4 | Permanent refusal in Settings, survives restart, cleared by UI upload, file kept | pass |
| 5 | Upload during a held-open refresh | pass (upload wins) |
| 6 | Real codex CLI after renewal makes no refresh of its own | pass (codex 0.153.0, probe `+refresher` cases) |
| — | Upload through a symlinked login | **bug, fixed** (`9c951e3a9`) |

## Environment

- Sandbox: Debian, kernel 6.1, 4 vCPU, Go 1.26.6, Node 20.19.0.
- `grain` built from this branch's base: `v1.11.0-rc.1.0.20261014001911-e1bbddcc771b`. UI built with
  `cd ui && npm ci && npm run build`, embedded in the binary.
- **Real daemon:** `grain daemon -data-dir /tmp/wt/data -ui-addr 127.0.0.1:18420
  -disable-native-sandboxes -codex-auth-file /tmp/wt/operator/auth.json`, with
  `CODEX_REFRESH_TOKEN_URL_OVERRIDE=http://127.0.0.1:18555/oauth/token`. It used the embedded SQLite
  store and the in-process UI/API. It was started three times; the second and third starts are the
  restarts.
- **Backends:** two codex connections, `codex-a` and `codex-b`, set through `PUT /api/settings` with
  `authMode: subscription` in team `walker`. Their logins are `secrets/codex-auth-<sha256(id)>.json`.
  The other logins are the shared `secrets/codex-auth.json` and the operator `-codex-auth-file`. The
  operator file is itself a symlink, `/tmp/wt/operator/auth.json -> /tmp/wt/operator-real/auth.json`.
- **Run-directory symlinks:** `run1/.codex/auth.json -> secrets/codex-auth-<a>.json` and
  `run2/.codex/auth.json -> secrets/codex-auth.json`. This is the arrangement `WithAuthFile` makes.
- **Every login was written with:**
  - an unknown top-level key, `x_walkthrough_unknown`;
  - an unknown key inside `tokens`, `x_unknown_token_key`;
  - `account_id`;
  - mode 0600.
- **Browser:** Playwright (chromium headless shell 151) against the daemon's built UI. A user `walker`
  was registered through `/api/auth/register` and signed in through `/api/auth/login`. The script opens
  Team settings → Models & connections → the connection's **edit** → the subscription login status.
  Downloading Playwright's browser needed `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`,
  because the package-cache proxy re-signs TLS.
- **codex CLI:** `@openai/codex@0.153.0`, installed with `npm install -g` and the same CA variable.

### Stubs (the only external boundary)

- **OpenAI token authority** (`/tmp/stub/authority.py`, Python `ThreadingHTTPServer`, appendix):
  - `POST /oauth/token` implements codex's refresh grant. It records the user agent, client id and
    refresh token of every call. It issues JWT access tokens (`iat` now, `exp` +1h) and a fresh
    refresh token `rt-from-stub-<n>`.
  - `POST /control?mode=ok|reused|hold` switches its answer:
    - `reused` answers `401 {"error":{"code":"refresh_token_reused",…}}`;
    - `hold` holds each refresh until `POST /release`.
  - `GET /stats` returns the call log.
- **For step 6, the `TestLiveCodexSharedLoginSurvivesConcurrentRefresh` probe's own stubs:**
  - a rotating authority;
  - a model that only answers bearers that authority issued.

Nothing of grain's own was stubbed or mocked.

## Steps

### 1–2. Refresh timing, mode, unknown keys, symlinks

The logins were written just before the daemon started at 00:34:07.
`mklogin.py OUT RT IAT_OFFSET EXP_OFFSET` sets the access token's `iat`/`exp` and `last_refresh`.

| Login | Lifetime | Due by rule | Expected first refresh | Observed (authority) |
|-------|----------|-------------|------------------------|----------------------|
| backend B | iat −4m, exp +4m | already inside the 10 min floor | at start | 00:34:07 (+0.3s) |
| shared `codex-auth.json` | iat −19m, exp +21m (40 min) | half-life: exp −20m = +42s | first tick after +42s | 00:35:07 (+60s) |
| backend A | iat −5m, exp +11m (16 min) | half-life would be exp −8m = +162s; **floor** exp −10m = +42s | +60s, not +180s | 00:35:07 (+60s) |
| operator file (via symlink) | iat −28m, exp +32m (60 min) | half-life: exp −30m = +102s | +120s | 00:36:07 (+120s) |

The refresher ticks once a minute. Every refresh came on the first tick after the login was due,
including backend A, whose 10-minute floor came before its half-life.

After the refreshes:
- All four logins are still 0600 regular files.
- `run1/.codex/auth.json`, `run2/.codex/auth.json` and `operator/auth.json` are still symlinks to the
  same targets. Reading through them gives the new `rt-from-stub-<n>` and a new `last_refresh`.
- `x_walkthrough_unknown`, `tokens.x_unknown_token_key` and `tokens.account_id` were all kept.
- Each login's lock file, `.<name>.refresh.lock` (0600, empty), sits beside the *target*, not the link.

The renewed logins were issued for an hour, so they are due again only after 30 minutes. In the next
~20 minutes the authority saw no second refresh of any of them, across two daemon restarts. That is one
refresh per login per half-life.

### 3. Daemon and `grain executor` on one file: exactly one refresh

- `grain executor -server 127.0.0.1:18999 -token … -binary codex=/bin/true
  -codex-auth-file /tmp/wt/operator-real/auth.json` ran with the same override. It names the *resolved*
  file, while the daemon reaches it through the symlink. Nothing listens on the server address; the
  executor keeps reconnecting, and its refresher runs regardless.
- **Timed race:**
  - The daemon ticks at :07 and the executor at :21.
  - At :02 the stub was put in `hold`, and a due login (`rt-race-1`) was written.
  - At :07 the daemon's refresher sent the refresh and the stub held it.
  - At :21 the executor's refresher found the login due on its unlocked look, so it waited on the flock.
  - The refresh was released at :30, inside the refresher's 30s client timeout.
- **Outcome:**
  - The authority saw **one** request for `rt-race-1` (call 8, from the daemon).
  - The file ended with `rt-from-stub-8`, unknown keys kept.
  - The executor, still running, logged nothing and sent nothing: under the lock it re-read the renewed
    login and found it not due.

**Observation (no change made):** the first attempt held the refresh longer than the refresher's 30s
client timeout. The executor's request timed out at 00:37:51 and its lock was released with the login
still due. The daemon's next tick, and then the executor's, each sent `rt-shared-exec` again (calls 5,
6, 7).
- Against the stub this is harmless.
- Against an authority that *had* rotated the token before the answer was lost, the retry presents a
  spent token. It is then refused `refresh_token_reused`, recorded as permanent, and needs a re-upload.
- That is inherent to a lost refresh answer: the new token is gone, and codex's own refresh has the same
  exposure. It needs a 30s+ stall at auth.openai.com, which this deployment has not seen. The recorded
  refusal says what to do. I did not file a task.

### 4. Permanent refusal: Settings, restart, re-upload, file never deleted

1. The stub was switched to `reused`, and backend A's login was replaced with a due login holding
   `rt-a-doomed` (inode 521814).
2. At 00:42:07 the daemon refreshed. It got `401 refresh_token_reused` (call 9) and logged
   `the login's refresh token was refused (refresh_token_reused)`. It wrote
   `.codex-auth-<a>.json.refresh-failure.json` (0600) with `code`, `message`, `at` and
   `refreshTokenSha256`.
3. The login file was untouched: same inode, same content.
4. The next ticks sent nothing (`RefreshBlocked`).
5. `GET /api/agent-backends/codex-a/codex-auth` returned `set: true` and `refreshError: "the subscription
   login can no longer be refreshed (refresh_token_reused: …); upload a new auth.json from `codex login`"`.
   `codex-b` returned no error.
6. **Settings (Playwright):**
   - Codex A shows "Subscription login configured for this backend." and an error alert with that
     message.
   - Codex B shows no alert.
7. **Restart:** the daemon was killed and started again (00:47).
   - Settings still shows the alert for Codex A.
   - The restarted daemon sent no request for the refused token: the authority's call count stayed at 9.
8. **Re-upload in the UI:** a new auth.json (`rt-a-uploaded`, +1h) was pasted into "Codex auth.json" and
   saved with **Save login**.
   - The alert is gone, and the API returns no `refreshError`.
   - The failure record is removed.
   - The login file is replaced in place: a regular 0600 file at the same path.
   - `run1/.codex/auth.json` still links to it and now reads `rt-a-uploaded`.

### 5. An upload during a held-open refresh wins

Both runs made backend B due just before the daemon's tick, with the stub in `hold`.

- **Through the UI:** the Playwright script opened Codex B and saved `rt-b-uploaded` while the refresh of
  `rt-b-race` (call 10) was held.
  - At release (00:50:04) the daemon logged the renewal. The upload, which had been waiting on the
    login's lock, then landed.
  - The file ended with `rt-b-uploaded`, 0600.
- **Timed through the API:** `PUT /api/agent-backends/codex-b/codex-auth` was sent 2s into the hold of
  `rt-b-race-2` (call 11), and the stub was released 10s later.
  - The PUT took **10.01s**: it returned 200 9ms after the release, at the moment the refresh wrote.
  - The file ended with `rt-b-uploaded-2`. The refresh's newer `rt-from-stub-11` was overwritten by the
    upload, not the other way round.

### 6. Real codex CLI after renewal: no refresh of its own

codex 0.153.0, the version the 2988 probe and `docs/codex-authentication.md` pin, was put on `PATH`.
The probe's `+refresher` cases were then run:

```
$ PATH=~/.npm-global/bin:$PATH go test -count=1 -v \
    -run 'TestLiveCodexSharedLoginSurvivesConcurrentRefresh/.*refresher' ./pkg/agent/codex
expired+refresher:  authority: 0 refresh requests from codex, 0 presented a spent refresh token, 1 from grain's refresher; model: 2 answered, 0 refused
rejected+refresher: authority: 0 refresh requests from codex, 0 presented a spent refresh token, 1 from grain's refresher; model: 2 answered, 0 refused
stale+refresher:    authority: 0 refresh requests from codex, 0 presented a spent refresh token, 1 from grain's refresher; model: 2 answered, 0 refused
--- PASS (13.71s)
```

Each case runs two real codex processes through `Framework.Run` on one shared login. Grain's refresher
renewed the login once, codex never refreshed, and both runs finished. The walkthrough's own stub
authority has no model endpoint, so a codex run against it alone could not get past its first request.
The probe's paired stubs are what make a real run possible here.

## Finding and fix

### `WriteAuthFile` renamed an upload over a symlinked login instead of its target

**Reproduction (deterministic, on the live daemon):**
```
mv secrets/codex-auth.json /tmp/wt/shared-real/auth.json
ln -s /tmp/wt/shared-real/auth.json secrets/codex-auth.json
curl -X PUT -H 'X-Grain-Admin-Mode: on' localhost:18420/api/codex-auth -d '{"value":"…rt-shared-uploaded…"}'
ls -l secrets/codex-auth.json          # -rw------- regular file: the link is gone
jq .tokens.refresh_token /tmp/wt/shared-real/auth.json   # rt-from-stub-2: the shared login kept the old tokens
```

**Cause:**
- `codex.WriteAuthFile` takes the refresh lock on the file the path resolves to, then renames the new
  login over the *path* itself.
- The refresher writes over the resolved file "so runs' symlinks keep working". The upload did not, and
  it cleared the refusal recorded beside the new regular file, not the target's.
- Anything else sharing the target is left on the old tokens:
  - another daemon generation;
  - a run whose link points at the target directly.
- If the old tokens had been refused, the target also keeps its refusal record.

**Fix** (`9c951e3a9`, `pkg/agent/codex/auth.go`):
- `WriteAuthFile` resolves the path once.
- It creates the temporary file in the target's directory and renames it over the target, as
  `RefreshAuthFile` does.
- It clears the refusal recorded for that target.
- `RemoveAuthFile` is unchanged: clearing a link removes the link, not a file somebody else owns.

**Regression test:** `TestWriteAuthFileReplacesTheSymlinkTarget` (`pkg/agent/codex/refresh_test.go`).
- It records a refusal through a link, uploads through the link, then asserts:
  - the link is still a link;
  - the target holds the upload at 0600;
  - the target's refusal record is gone.
- It fails on the old code ("the upload replaced the link") and passes now.

**Re-run on the live daemon** after rebuilding: the same `PUT /api/codex-auth` leaves `secrets/codex-auth.json` a
symlink, and `/tmp/wt/shared-real/auth.json` holds `rt-shared-uploaded` at 0600.

**Tests run:**
- `go test -race -count=1 ./pkg/agent/codex`: ok.
- `go test -race -count=1 -run 'Codex|codex' ./pkg/ui ./pkg/daemon`: ok. These are the callers'
  login tests.
- `go test -count=1 -run Codex ./cmd/grain`: ok.

## CI verdict

grain CI (`wait_for_checks`, full suite) at `4532b4f` passed: run `b3b281f8652bc1e9`. Every job
succeeded: frontend, go, installer, lint, package cache, postgres, relay image, run host, terraform and
ui e2e. The run's only difference from this commit is this paragraph.

## Appendix: stub authority

```python
# /tmp/stub/authority.py (abridged to the handler)
def do_POST(self):
    ...
    if self.path.startswith("/control"):   # ?mode=ok|reused|hold
        state["mode"] = mode; release.clear()
    if self.path.startswith("/release"):
        release.set()
    # /oauth/token
    entry = {"n": n, "at": now, "ua": UA, "grant": req["grant_type"], "client": req["client_id"],
             "refresh_token": req["refresh_token"], "mode": state["mode"]}
    state["calls"].append(entry)
    if mode == "reused":
        return self.reply(401, {"error": {"code": "refresh_token_reused",
                                          "message": "Your refresh token has already been used"}})
    if mode == "hold":
        release.wait(120)
    iat = int(time.time())
    self.reply(200, {"access_token": jwt({"exp": iat + LIFETIME, "iat": iat, "n": n}),
                     "id_token": jwt({...}), "refresh_token": "rt-from-stub-%d" % n})
```

`mklogin.py OUT RT IAT_OFF EXP_OFF` writes a codex ChatGPT `auth.json`. Its `tokens` object holds
`id_token`, `access_token` (a JWT with `iat`/`exp` at now+offsets), `refresh_token`, `account_id` and the
unknown `x_unknown_token_key`. The file also carries `last_refresh` = `iat` and the unknown top-level
`x_walkthrough_unknown`.
