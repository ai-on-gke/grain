# Security boundaries walkthrough — 2026-10-04

Plan: `docs/test-plans/2026-10-04.md` §8 "Security boundaries". Task 1834.

## Environment

- **Tested commit:** `candidate` at `9b06116c4` ("Merge pull request #2872 from
  bwsalmon/grain/task-1914"). `git branch -r --contains` lists `origin/candidate` (and `origin/main`) for
  each commit §8 covers: `5f9312c94`, `1e65e37aa`, `8c9fb4ed5`, `861af1607`, `3962f0e5c`, `4c4557201`,
  `ecb9c058e`, `9e946727a`, `5a4af3751`, `b23c8481a`, `20d9ebf65`, `53372273e` and `57096986c`.
- Sandbox: Debian, Go 1.26.6, Node 20.19.0, git 2.39.5, Playwright Chromium from
  `/var/cache/playwright`. UI built with `cd ui && npm ci && npm run build`, so `pkg/ui/static` held the
  production bundle.
- The servers:
  - `grain daemon -data-dir /tmp/d1 -disable-native-sandboxes -ui-addr 127.0.0.1:18420 -tunnel-gateway-url http://127.0.0.1:18080 -tunnel-token gwsecret`,
    with and without `-ui-trust-tunnel`;
  - `grain-tunnel-gateway -port 18080 -token gwsecret`;
  - `grain demo -addr 0.0.0.0:<port>` driven by Playwright (`ui/e2e/fixtures.js`, one demo per worker),
    plus one hand-run demo on `0.0.0.0:18501` for curl probes.
- `grain daemon` has no `-dev` flag on `candidate`, and without a backend it refuses to start ("no sandbox
  backend selected"), so I used `-disable-native-sandboxes`.

## Stubs

Only external boundaries were stubbed. grain's own APIs and React were not mocked.

| Boundary | Stub |
| --- | --- |
| GitHub git endpoints | `githubsim.GitUpstream`: GitHub's auth layer, with the deployment's bot token allowed to read and write `acme/widgets`. Behind it is a real `git http-backend` over a bare repository. |
| Google OAuth | Not contacted. Turning Google sign-in on was checked by the `302` to `https://accounts.google.com/…` from `/api/auth/google/login`, which was not followed. |
| IAP | Unsigned `X-Goog-Authenticated-User-Email` header with no assertion. No signer was needed, because no step had to accept an assertion. |
| Live model session | The live `propose_task` was sent as the `POST /api/tasks` with `proposedBy` that `cmd/grain/mcpserver.go`'s proposal filer sends. No model ran. |
| Sandbox | A temporary directory holding a clone made through the proxy with the run's own token. |

## Steps, commands and outcomes

### 1. Git proxy push refs (`5f9312c94`, `1e65e37aa`)

These tests are in `tests/security/gitproxy_real_push_test.go` (new). The proxy is built with
`gitproxy.BuildProxy` with `ReadScopes` and `WriteScopes`, which is the chain `cmd/grain`'s `startGitProxy`
serves. It sits in front of githubsim and a real `git http-backend`. The task is `tw` on `acme/widgets`, with
a live run whose sandbox token clones through the proxy. Then real `git push` runs:

```
go test ./tests/security/ -run TestRealGitPush -v -count=1
```

| Push | Without grant | Upstream received |
| --- | --- | --- |
| `git push origin HEAD:refs/heads/main` | refused | no receive-pack; refs unchanged |
| `git push origin +HEAD:refs/heads/release` (a real force: `release` is not an ancestor) | refused | nothing |
| `git push origin :refs/heads/release` | refused | nothing |
| `git push origin refs/tags/v9.9.9:refs/tags/v9.9.9` | refused | nothing |
| `git push origin HEAD:refs/heads/grain/task-tw HEAD:refs/heads/main` (mixed) | both refs refused | nothing |
| `git push origin HEAD:refs/heads/grain/task-tw` | **accepted** | branch created at HEAD |

Git prints this for each refusal (port elided):

```
remote: refusing to update refs/heads/main in acme/widgets: a sandbox may push only its own task's branch, refs/heads/grain/task-tw (push with `git push origin grain/task-tw`)
To http://127.0.0.1:PORT/acme/widgets.git
 ! [remote rejected] HEAD -> main (refusing to update refs/heads/main in acme/widgets: a sandbox may push only its own task's branch, refs/heads/grain/task-tw (push with `git push origin grain/task-tw`))
error: failed to push some refs to 'http://127.0.0.1:PORT/acme/widgets.git'
```

The other refusals have the same text with their own refs: `HEAD -> release`, `release` for the delete, and
`v9.9.9 -> v9.9.9`. A mixed push rejects both refs with the `main` reason, so nothing partial lands.

With `git-push-any-ref` granted (`Via: label`), every case lands on the upstream: `main` and the tag at
HEAD, the task branch, the force-push of `release`, and the delete of `release`.

### 2. Tunnel trust (`8c9fb4ed5`) and the gateway opt-in (`861af1607`)

I connected the daemon to a local `grain-tunnel-gateway` (`reverse tunnel established`) and sent requests
to the gateway's port:

| Request through the tunnel | Default (`-ui-trust-tunnel=false`) | `-ui-trust-tunnel` |
| --- | --- | --- |
| `GET /api/tasks` with `X-Goog-Authenticated-User-Email: accounts.google.com:admin@example.com`, no assertion | `401 {"error":"sign in to use this server's API: this request's identity was not accepted because it names \"admin@example.com\" only in the unsigned X-Goog-Authenticated-User-Email header, which this deployment does not trust (see -ui-trust-identity-headers)"}` | `401`, the same message: the unsigned header is still not believed |
| `GET /api/tasks`, no header | `401 {"error":"sign in to use this server's API"}` | `200`, as the daemon's own actor |
| `GET /api/config` | `200`, `actor:""`, `authenticated:false` (the public bootstrap answer) | `200`, `actor:"debian"` |

With `-ui-trust-tunnel` and no IAP audience, the daemon logs the warning that anybody who can reach the
gateway runs as the daemon's own actor (grain#2370). This is stricter than the plan expected: even with
the tunnel trusted, an unsigned email header does not make anyone an admin. Only a header-free request
gets the daemon's actor.

`grain-tunnel-gateway`:

- `-audience https://gw.example` with no allow-list refuses to start: "a Google ID token audience is set
  without an allowed-emails list; any Google account or service account can mint an ID token for any
  audience, so this would admit anyone (set allowed emails, or opt in to any Google identity explicitly)".
- Adding `-allow-any-google-identity`, or `$GRAIN_TUNNEL_ALLOW_ANY_GOOGLE_IDENTITY=true`, starts it.
- With no authentication at all it refuses with "no tunnel authentication configured…".

### 3. `capability_teams` (`3962f0e5c`)

In the browser, as the system admin, I went to Admin → Integrations & Auth → Capabilities → "Grain DB
read" → "Also authorize teams" and entered `gamma`. The save is
`PUT /api/settings {"capabilityTeams":{"db-read":["gamma"]}}`, and the hint then reads "widened to gamma".

- Before the change, a `gamma` task granting `db-read` is refused.
- After it, the `gamma` task is accepted (`201`).
- `beta` is still refused with `400 {"error":"capability \"db-read\" is restricted to authorized teams [default gamma]; team \"beta\" is not authorized"}`.
- After a reload the field still holds `gamma`.

`grain sync -dry-run -server … -config sync.json` with `{"settings":{"capabilityTeams":{"db-read":["gamma"]}}}`:

- Before the change it printed `capability teams: "none" -> "db-read: gamma"`.
- After it printed `settings: already up to date, nothing changed`.

### 4. Session revocation (`4c4557201`, `518abcc42`)

I used two browser contexts, A and B, both signed in as `alice` over the machine's non-loopback address.
Loopback is trusted as the server's own actor, which would hide a revoked session.

- A changed the password from Settings. A stays signed in, and B's next `GET /api/tasks` is `401`.
- An admin reset from Admin → All Users → "Change password for alice" signs A out too (`401`), and the
  reset password then signs in.
- A password change behind `X-Forwarded-Proto: https` answers
  `Set-Cookie: grain_session=…; HttpOnly; Secure; SameSite=Lax`.

**Finding 1** (fixed): B's next navigation did not show the sign-in dialog. It showed an empty app as
"alice". See the findings section below.

### 5. The Authentication section (`ecb9c058e`)

This was driven in Admin → Access & Accounts, with a fresh context on the non-loopback address:

- **Open registration off:** a stranger's `POST /api/auth/register` gets `403`.
- **Open registration on:** the same stranger can register.
- **Password sign-in off:** a fresh context's `/api/config` says `passwordAuthEnabled:false`, and its dialog
  offers no "New user" tab.
- **Google sign-in:** I set the client ID, `example.com` and the secret sentinel `GOCSPX-walkthrough-sentinel-7f3a`,
  then ticked "Sign in with Google". `/api/auth/google/login` from a fresh context then answers `302` to
  `accounts.google.com` with the client ID. Unticked, it does not redirect.
- **The secret:** it never appears in the `PUT` response, in the page DOM after a reload (the field is empty,
  with "Set. It is never shown again…"), in `GET /api/settings` (`googleOAuthClientSecretSet:true`), in
  `/api/config`, or in the redirect.

**Finding 2** (filed): the sign-in dialog offers no "Sign in with Google" control, and `/api/config` does
not say Google sign-in is on.

### 6. Registration names (`9e946727a`, `90f48eb8e`)

Registration was tried from a stranger on the non-loopback address, with registration open, on a fresh
demo:

| Name | Answer |
| --- | --- |
| `admin` / `admin ` / `Admin` / `user:Admin` | `409 username already exists` |
| `a/b`, `admin team`, `Default Team` | `400 username "…" is not allowed: use a plain name or a bare email address, without spaces or any of :<>,;"\/?#%` |
| `аdmin` (Cyrillic а, U+0430) | **`200`**: an account, with personal team `dmin` |
| `ADMIN@` | **`200`**: an account, with personal team `admin-1` (not `admin`, as `90f48eb8e` intends) |
| `default` | **`200`**: an account, with personal team `default-1` |
| a 200-character name | **`200`**: an account, with a 56-character team id |

In the browser's New user form, `a/b` and `Admin` show the server's sentence in the dialog and nobody is
signed in. No name-derived team took `admin` or `default`.

**Finding 3** (filed): homoglyph names, `ADMIN@`, and names of any length are accepted.

### 7. Viewer (`b23c8481a`, `20d9ebf65`)

`vic` is a viewer of `beta` only, signed in over the non-loopback address. `/api/config` lists `beta` in
`readOnlyTeams`.

- **Task pane:** a `beta` task shows no Approve, Close, Edit, Comment or Run now button, and no reply box.
- **List row:** the row is not draggable, has no "Move task" handle, and the batch bar (if selection is
  offered) has no write action.
- **Writes sent anyway:** `PATCH /api/tasks/{id}` and `POST …/approve` are `403`.

The existing `rbac-writes-walkthrough.spec.js` (viewer of alpha) passed too.

### 8. Live widening proposal (`5a4af3751`)

I made an approved auto-merge parent (`queued`) and filed two live proposals from it:

- A proposal within the parent's permission is `queued` with auto-merge.
- One with `reads: ["acme/secret"]` is `proposed`, and carries "grain: this proposal asks to read
  `acme/secret`, which task N, which proposed it, could not read itself. It was filed with them, so it waits
  for a person…".

In the browser the task pane shows the note, with `acme/secret` as code, and an Approve button.

### 9. `propose_task` schema (`57096986c`)

`tools/list` against a real `grain mcpserver` built from this tree gives `network_access` the enum
`[open none]`. Its description no longer mentions `restricted`. This is
`TestMCPServerProposeTaskOffersNoRestrictedNetwork` in `tests/e2e/mcpserver_propose_repo_test.go`.

The live tool schema offered to this very task still lists `"open"`, `"restricted"` and `"none"`, so the
deployed build is behind `candidate` (expected; it picks this up at the next deploy of `candidate`).

## Findings

1. **A revoked session's browser was shown an empty app instead of the sign-in dialog.** Fixed in this
   change.
   - **Cause:** `App.jsx` opened `UsernameDialog` only when `grain.username` was absent from localStorage.
     A revoked browser keeps it. The server answers `/api/config` with `actor:""` and refuses every list,
     so the person saw "Tasks 0" under their old name with no prompt.
   - **Reproduction:**
     1. Sign in as `alice` from two contexts on a non-loopback address.
     2. `POST /api/auth/change-password` from A.
     3. Reload B.
     4. Before the fix there was no "Log in" heading. After it, the Log in dialog opens with `alice` filled in.
   - **Fix:** the dialog also opens when grain's own config answer names nobody (`authenticated === false`
     and `actor === ""`). Loopback and other trusted callers are named (for example `actor:"debian"`) and
     are not asked. A failed `/api/config` (no grain answer) still shows the access-denied and not-grain
     screens.
   - **Regression tests:** `App.test.jsx` "asks a browser whose session the server no longer accepts to
     sign in…", which fails without the fix, and "does not ask a caller the server trusts without a
     session…". In the browser it is covered by `ui/e2e/security-boundaries-walkthrough.spec.js` test 1.
2. **Google sign-in has no entry point in the sign-in dialog.** Filed as a follow-up.
   - `UsernameDialog.jsx` has no Google control, and `/api/config` carries no `googleOAuthEnabled`.
   - Turning "Sign in with Google" on only makes `/api/auth/google/login` work for someone who already knows
     that URL.
   - **Reproduction:**
     1. Turn Google sign-in on with an ID, a secret and a domain.
     2. Open `/` in a fresh context on a non-loopback address.
     3. The dialog offers "Log in" and "New user" only, and `jq .googleOAuthEnabled` on `/api/config` is
        `null`.
3. **Registration accepts homoglyph look-alikes and unbounded names.** Filed as a follow-up.
   - `9e946727a` checks `user:` prefixes and display-name forms, not confusables or length.
   - **Reproduction:**
     1. On `grain demo -addr 0.0.0.0:0`, `PUT /api/settings {"openRegistration":true}` from loopback.
     2. From the non-loopback address, `POST /api/auth/register` with `{"username":"аdmin"}` (U+0430),
        `"ADMIN@"`, `"default"`, and 200 × `x`.
     3. All four are `200`.
   - Team ids stay safe (`dmin`, `admin-1`, `default-1`). The risk is impersonation in comments, authors
     and member lists.
   - Which policy to adopt (a confusable skeleton against existing and reserved names, a script-mixing
     rule, a length cap shorter than an email address) is a judgment call, so this was not fixed here.
4. **With only `-tunnel-gateway-url`, the daemon also runs a relay client against the gateway, which never
   connects.** Filed as a follow-up.
   - `daemon.go` defaults `-relay-url` to the gateway URL. `grain-tunnel-gateway` serves no relay path, so
     the log fills with `failed to connect to relay http://127.0.0.1:18080: relay rejected upgrade with
     status 404` (40 lines in 17 minutes, backing off to 30s).
   - This is harmless, but it is noise in every tunnel deployment's log.

## Fixes and tests in this change

- `fix(ui)`: `ui/src/App.jsx` plus two `App.test.jsx` cases (finding 1).
- `tests/security/gitproxy_real_push_test.go`: real `git push` through the production proxy chain, for every
  §8 push case, with and without `git-push-any-ref`.
- `ui/e2e/security-boundaries-walkthrough.spec.js`: steps 3–8 in a browser.
- `tests/e2e/mcpserver_propose_repo_test.go`: `TestMCPServerProposeTaskOffersNoRestrictedNetwork`.

Formatting: `gofmt -l` is clean and `npm run format` was run. `make security` (`go vet ./...` plus
`go test -race ./tests/security/...`) passed in 152s. No `.grain/baselines/` file changed.

Also run locally and passing:

- `npx playwright test e2e/security-boundaries-walkthrough.spec.js e2e/rbac-writes-walkthrough.spec.js e2e/authentication-settings.spec.js e2e/user-auth-walkthrough.spec.js --project=chromium`
  (all passed);
- `npx vitest run src/App src/components/UsernameDialog` (153 passed).

## CI verdict

Grain CI full suite on this branch: see the pull request. The final verdict is recorded in the task's
closing note.
