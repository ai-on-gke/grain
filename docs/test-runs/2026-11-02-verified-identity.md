# Test run — 2026-11-02 — §1 Verified identities (IAP principals, allowlist, CSRF, badge, logout)

Walkthrough of [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §1 (PR #395), task 2443, against a real `grain daemon` behind a local stand-in for IAP. Branch `grain/049f1e09/task-2443-walkthrough-verified-iap`, PR #461.

**Result:** three bugs found and fixed here, each with a regression test (F1–F3). One UI gap (F4) and one design gap (O1) are filed as follow-ups. Everything else in "not proved" 1–6 behaved as the plan says, with one exception: item 6's wording disagrees with the code and the tests that pin it (D1).

## Environment

| | |
|---|---|
| Base | `origin/main` `72b38298` (PR #443). Fixes are on top of it: `1faff9e1`, `5bf5b88b`, `cf23440c`, `03e03cc1` |
| Binary | `cd ui && npm ci && npm run build && cd .. && go build -o ~/bin/grain ./cmd/grain` (embedded UI). The daemon was rebuilt after each fix and the affected rows were re-run |
| OS | Debian GNU/Linux 12 (bookworm), kernel 6.1.0-54-amd64; Go 1.26.10; Node 20.19.0 |
| Browser | Chromium 151.0.7922.34 (Playwright 1.62.1, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless |
| Server | A real `grain daemon` with a data dir. **No** `grain demo` and no consolerestserver (shared env step 3: a daemon with a data dir). It ran with `-host-sandboxes`, because a daemon refuses to start without a backend; nothing was dispatched |
| IAP stand-in | [`2026-11-02-verified-identity/iapstub.go`](2026-11-02-verified-identity/iapstub.go) (`//go:build ignore`). `keygen` writes two P-256 keys: `k1` is published, `k2` never is. `serve` publishes `k1` as a JWK set on `127.0.0.1:18080/jwks`. `sign` mints ES256 `X-Goog-IAP-JWT-Assertion`s (`-kid`, `-key`, `-exp`, `-iat`, `-badsig`) |
| Cloud Identity | Not reachable, which is part of the test. It was tried two ways: with no ADC at all, and with ADC whose `token_uri` is a socket that accepts and never answers (`blackhole.py`, shown in F2) |
| Tools installed | `sqlite3` and `tesseract-ocr`, via apt (the latter to read screenshots, see "Looking at the screenshots") |

Daemon flags ([`run-daemon.sh`](2026-11-02-verified-identity/run-daemon.sh)):

```
grain daemon -data-dir DIR/data -sandbox-port-base=0 -host-sandboxes -sandbox-dir DIR/sb \
  -ui-addr 127.0.0.1:4300 -as owner@example.com \
  -ui-iap-audience /projects/123/global/backendServices/456 \
  -ui-iap-issuer https://iap.test.local \
  -ui-iap-keys-url http://127.0.0.1:18080/jwks  [-ui-allowed-principals ...]
```

## 1. The real flag path: curl matrix

Script: [`matrix.sh`](2026-11-02-verified-identity/matrix.sh). Every row is `GET /api/auth/user` from loopback.

| Request | Status | Body (start) | `logRefusedAssertion` line before F1 fix | after F1 fix |
|---|---|---|---|---|
| valid assertion, no access code | **200** | `{"authenticated":true,"user":"owner@example.com",...,"authMethod":"iap"}` | — | — |
| expired (`exp` −10m) | **401** | `invalid IAP JWT assertion: token has invalid claims: token is expired` | yes | yes |
| wrong audience | **401** | `IAP audience mismatch: expected "/projects/123/...", got [/projects/999/...]` | yes | yes |
| wrong issuer | **401** | `token has invalid claims: token has invalid issuer` | yes | yes |
| unknown kid (`k2`, unpublished) | **401** | `token is unverifiable: ... unknown IAP key ID "k2"` | **no** | yes |
| kid `k1`, signed by `k2` | **401** | `token signature is invalid: crypto/ecdsa: verification error` | **no** | yes (once per kind; the byte-flipped row below is the same kind) |
| bad signature (byte flipped) | **401** | `token signature is invalid: crypto/ecdsa: verification error` | **no** | deduplicated with the row above, by design |
| not a JWT | **401** | `token is malformed: token contains an invalid number of segments` | yes | yes |
| `iat` 10m in the future | **401** | `token has invalid claims: token used before issued` | **no** | yes |
| no assertion, no code | **401** | `this daemon is single-tenant and every caller shows its access code...` | — | — |
| no assertion, access code | **200** | `{"authenticated":false,...,"authMethod":"trusted"}` | — | — |
| plaintext `X-Goog-Authenticated-User-Email` only | **401** | access code required | `ignored the unsigned X-Goog-Authenticated-User-Email header naming "owner@example.com"` | same |
| plaintext email + `X-Goog-Authenticated-User-Id` | **401** | access code required | (logged once per header) | same |
| invalid assertion + valid access code | **401** | `token signature is invalid` | — | — |
| assertion sent to a daemon with **no** `-ui-iap-audience` | **401** | access code required | `no IAP audience is configured ... so no assertion can be verified` | same |

All status codes match the plan. The log lines did not; see F1.

## 2. The allowlist, end to end

| Setup | Caller (token email) | Status | Body / note |
|---|---|---|---|
| default (no flag), `-as owner@example.com` | `owner@example.com` | 200 | |
| default | `OWNER@Example.COM`, `accounts.google.com:Owner@example.com` | 200 | both normalised to `owner@example.com` |
| default | `other@example.com` | 403 | `principal "other@example.com" is not allowed by -ui-allowed-principals`. A browser navigation gets the "Not allowed on this grain" page, which names the `user:` entry to add |
| default, after a verified settings save `gcpProject=my-proj-456` | `grain-daemon@my-proj-456.iam.gserviceaccount.com` | 200 | the default admits grain's daemon SA for the configured project |
| default, same | `grain-daemon@my-proj-123.iam.gserviceaccount.com` (old project) | 403 | |
| `user:Alice@Example.COM, domain:Corp.Example ,serviceAccount:Bot@My-Proj.iam.gserviceaccount.com,group:Ops@Example.com,user:svc@my-proj.iam.gserviceaccount.com` | `alice@example.com`, `ALICE@example.com` | 200 | mixed case in the flag and in the token |
| same | `dev@corp.example`, `Dev@CORP.example` | 200 | `domain:` |
| same | `dev@sub.corp.example` | 403 | `domain:` is exact, with no subdomains |
| same | `bot@…`, `BOT@MY-PROJ.IAM.GSERVICEACCOUNT.COM` | 200 | `serviceAccount:` |
| same | `svc@my-proj.iam.gserviceaccount.com` | 403 | a `user:` entry never admits a service account (kind mismatch). This is consistent with the code, but nothing warns about such an entry |
| same (group entry, no ADC) | `owner@example.com`, `mallory@example.com` | 403 in ~1 ms | `could not be authorized against group allowlist: cloud identity credentials: google: could not find default credentials` |
| `user:Alice@Example.COM,group:Ops@Example.com`, ADC with a token endpoint that never answers | `mallory@example.com` | **no answer after 150 s** before the fix; **403 after 10.0 s** after it | F2 |
| same | `alice@example.com` | 200 in 1 ms | direct entries never wait on the group lookup |
| IAP verifier, `mallory` (not allowed) + **valid** access code | | 403 | under IAP the verified identity wins over the code |
| IAP verifier, `owner` + **wrong** code | | 200 | the code is not consulted |

Malformed flag values: each makes the daemon refuse to start, exiting 2 with a message that names the entry.

| `-ui-allowed-principals` | message |
|---|---|
| `user:alice` | `invalid allowed principal "user:alice": user entry must be an email address` |
| `User:alice@example.com` | `invalid allowed principal "User:alice@example.com": unknown prefix "User:" (want user:, serviceAccount:, group:, or domain:)` |
| `domain:@example.com` | `domain entry must be a bare domain such as example.com` |
| `bogus` | `must start with user:, serviceAccount:, group:, or domain:` |
| `group:` | `empty group value` |
| `serviceAccount:bot` | `serviceAccount entry must be an email address` |
| `user:a@b.c,bogus` | names `bogus` (exit 2) |
| `$GRAIN_ALLOWED_PRINCIPALS='domain:x/y'` | `invalid allowed principal "domain:x/y": domain entry must be a bare domain` |

## 3. What a verified caller becomes

The daemon ran with `-as owner@example.com` and an allowlist of `user:Alice@...` and `serviceAccount:Bot@...`, so that attribution to the caller can be told apart from attribution to the deployment's actor. All requests were made with an assertion and no access code.

| Action | Recorded as | Pass? |
|---|---|---|
| alice files a task (`POST /api/tasks`, `noRepo`) | `author: alice@example.com`, `authorKind: human` | yes |
| alice comments | `task_comment`: `human / alice@example.com` | yes |
| alice closes | `task_event`: `closed`, `human / alice@example.com` | yes |
| bot files a task | `author: bot@my-proj.iam.gserviceaccount.com`, `authorKind: service` | yes |
| bot closes | `task_event`: `closed`, `service / bot@…` | yes |
| bot comments | `task_comment`: **`human`** / bot@… before the fix, **`service`** after it | F3 |
| alice saves settings (`PUT /api/settings {"gcpProject":...}`) | applied (200). **No actor recorded anywhere**: the log line, the state-repo commit (`grain <grain@...> Update grain state`) and `hub-audit.log` all leave it out | O1 (filed) |
| a request with the access code only | `authMethod: trusted`; acts as `-as` | yes |

## 4. CSRF in a real browser

Script: [`browser.mjs`](2026-11-02-verified-identity/browser.mjs). The "IAP" context sends the assertion on **every** request through `extraHTTPHeaders`, including requests a foreign page makes, which is how a real IAP cookie behaves. The attacker page is `http://localhost:18090`, a different *site* from `http://127.0.0.1:4300`.

| Step | IAP assertion context | Access-code context (signed in on the page) |
|---|---|---|
| same-origin `fetch` POST `/api/tasks/6/comments` from the UI origin | 200 | 200 |
| comment through the task page's composer (`Reply...` → **Comment**) | POST 200, stored | POST 200, stored |
| cross-site `<form enctype=text/plain>` POST (body is valid JSON) | **403**, not stored | **403**, not stored |
| cross-site `fetch(..., {mode:"no-cors"})` POST | **403**, not stored | **403**, not stored |
| cross-site link (GET `/`) | navigates, page loads | navigates (to the login page: the `SameSite=Strict` cookie is held back, as designed) |

The same checks with curl and a valid assertion:

| Headers | Status |
|---|---|
| no `Origin`, no cookie (programmatic) | 200 |
| `Origin: http://127.0.0.1:4300` | 200 |
| `Origin: http://localhost:18090` | 403 `cross-origin ... Origin does not match Host` |
| `Origin: null` | 403 |
| `Cookie: GCP_IAP_UID=x`, no `Origin`/`Sec-Fetch-Site` | 403 `state-changing cookie-authenticated request must carry Sec-Fetch-Site: same-origin or a matching Origin` |
| `Sec-Fetch-Site: same-site` | 403 |
| `Sec-Fetch-Site: cross-site` + matching `Origin` | 403 |
| foreign `Origin` + `X-Forwarded-Host` naming it | 403. The global `http.CrossOriginProtection` runs first and does not trust XFH |
| GET with a foreign `Origin` + `Sec-Fetch-Site: cross-site` | 200 |

## 5. Badge, menu and logout

The browser assertions come from `browser.mjs`. I took the screenshots with `make ui-screenshots PAGES=/,/settings/access URL=http://127.0.0.1:4300 CODE=... HEADER="X-Goog-IAP-JWT-Assertion: <alice>"`, using the `--header`/`HEADER=` option added in `03e03cc1`, and with `screenshot.mjs --header` for the long address. They show `/` and `/settings/access` at desktop 1440 and phone 390, light and dark, for `alice@example.com` and for a 94-character address (`alexandra.konstantinopoulou-vanderberg.platform-oncall@infrastructure.example-corporation.com`).

| Width / theme | Badge geometry (bounding boxes) | Menu | Settings → Access | Log out |
|---|---|---|---|---|
| 1440 light/dark, short | badge x 1286..1412, y 24..52; inside `page-header-globals` (1190..1412 × 24..52); text `alice@example.com`; page overflow 0 px | x 1311..1412, items `Access settings`, `Log out`, within the viewport | "Signed in through IAP as alice@example.com." shown; overflow 0 | lands on `/_gcp_iap/clear_login_cookie` |
| 1440 light/dark, long | badge x 1212..1412 (200 px max-width) inside globals (1116..1412); ellipsized, with the full address in `aria-label`/tooltip; page overflow 0 px | same | full address shown | same |
| 390 light/dark, short and long | **no badge**: `top-bar-user` is not rendered at phone width, and `PhoneNav` has no identity or Log out | — | reached by URL: "Signed in through IAP as …" shown, the long address wraps, overflow 0 | the Access page's Log out lands on `/_gcp_iap/clear_login_cookie` |
| no verifier (daemon without `-ui-iap-*`), 1440 | badge count 0 | — | — | the access-code page is shown, login reaches the UI, and a same-origin POST with the access session gives 200 |

**Looking at the screenshots.** The sandbox's file reader shows PNGs as bytes, and there is no Gemini key, so `LOOK=1` was not available. To look at them anyway, I OCR'd 3× crops of the header with `tesseract`:

- 1440 light and dark: `… contact running [bell] [system] [gear] alexandra.konstantinopoulou-v...`. The badge sits right of the gear and is ellipsized.
- 1440 with the menu open: adds `Access settings` / `Log out` under it.
- 390 Access page (dark): `Signed in through IAP as alexandra.konstantinopoulou-vanderberg.platform-oncall@infrastructure.example-corporation.com.`, wrapped over three lines.
- 390 home: the top bar reads only `grain` and the task count.

The mean luminance of the 1440 shots is 0.96 in light and 0.08 in dark, so the dark theme did apply. Taken with the bounding-box rows above, these are the checks the shots were looked at for.

## 6. Break-glass precedence

You can only register a verifier with access-code precedence from Go (`RegisterIdentityVerifier`), so a daemon flag cannot exercise it. I ran the pinned tests, which pass: `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes` and `TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders`. They show:

- With an `AccessCodeWins` verifier, a *valid* assertion for a caller the allowlist refuses (group lookup down) plus a valid code (bearer or cookie) is admitted as the trusted operator.
- A wrong code is refused with 401.
- The precedence applies only to that verifier: the default/IAP verifier lets the identity win (the daemon rows in §2: `mallory` + valid code → 403).

**D1 (the plan disagrees with the code).** The plan says "a request carrying both a valid access code and an *invalid* assertion is admitted by the code". The code (`singleTenantGate` checks `cv.err` before `accessCodeWins`) and the test refuse that request with 401, even for an `AccessCodeWins` verifier. The test's step 4 says so in as many words: "Failed proxy signature verification STILL fails closed with 401 and no fallback". `AccessCodePrecedence`'s doc limits break-glass to "the allowlist or group lookup is broken", not a broken signature. I left the code alone, since failing closed is the safer reading. One consequence is worth knowing: if a proxy's signing keys or keys URL break, the code cannot be used through that proxy, only by going around it.

## Findings

### F1: refused assertions after the first per audience were silently unlogged (fixed, `1faff9e1`)

`logRefusedAssertion` keyed its once-only log on the unverified (issuer, audience) alone. To reproduce on `72b38298`, start the daemon as above and run `matrix.sh`. It logs `token is expired` for the deployment's own audience, and then no line at all for unknown kid, signed-by-another-key, byte-flipped signature or future `iat`, which are the forged-token cases. Restarting the daemon and sending the bad signature first logs that one and then hides the expired one. In both orders every request still answers 401: only the logging was wrong.

**Fix:** the key also carries `refusalKind(err)`, one of nine fixed kinds mapped from golang-jwt's sentinel errors, so the key space grows by a constant and a repeated forgery is still logged once. Regression test: `pkg/ui/refused_assertion_log_test.go` `TestRefusedAssertionLoggedOncePerKindNotOncePerAudience`, which fails on the base (no line for "forged" or "unknown kid").

### F2: a caller depending on a `group:` entry hung forever when Cloud Identity's token endpoint did not answer (fixed, `5bf5b88b`)

To reproduce on `72b38298`:

```
openssl genrsa 2048 > sa.key   # wrapped into sa.json: type service_account, token_uri http://127.0.0.1:18081/token
python3 blackhole.py &          # accept() on 18081 and never reply
GOOGLE_APPLICATION_CREDENTIALS=sa.json run-daemon.sh -ui-allowed-principals 'user:Alice@Example.COM,group:Ops@Example.com'
curl -m 150 -H "X-Goog-IAP-JWT-Assertion: $(iapstub sign ... -email mallory@example.com)" 127.0.0.1:4300/api/auth/user
# → curl gives up: 000 after 150.000s
```

`google.DefaultClient(requestCtx, …)` is `http.DefaultClient` underneath, with no timeout, and the token source keeps that first request's context for later fetches. So one silent token endpoint blocks this caller, and every later group-dependent caller queues behind the token source's lock.

**Fix:** the token source is built on a background context carrying a client whose transport has `ResponseHeaderTimeout = cloudIdentityTimeout` (10 s), and `IsMember` bounds its Cloud Identity calls with a 10 s context deadline. The bound is deliberately not `http.Client.Timeout`: oauth2 copies that onto its outer client, which then logs `Transport.CancelRequest no longer does anything` on every timeout. That was seen in the first attempt. After the fix the same curl gets **403 in 10.0 s**: `could not be authorized against group allowlist: … oauth2: cannot fetch token: … timeout awaiting response headers`. Regression test: `pkg/ui/group_lookup_timeout_test.go` `TestGroupAllowlistRefusesWhenTokenEndpointNeverAnswers`. It fails on the base after its 10 s guard and passes in 0.3 s.

### F3: a verified service account's comment was stored as a human's (fixed, `cf23440c`)

To reproduce on `72b38298`, as `bot@my-proj.iam.gserviceaccount.com` (allowed), file a task, comment on it, then close it:

```
sqlite3 data/store/grain.db "select author_kind,author_id from task_comment where task_id=3"
# → human|bot@my-proj.iam.gserviceaccount.com   (task author and task_event say "service")
```

`commentWithAuthor` rebuilt the author string from `handleAddComment` as `PrincipalHuman`. **Fix:** an author string naming the request's own principal keeps that principal, kind and all. Any other author string is treated as before. Regression test: `pkg/ui/verified_comment_kind_test.go` `TestVerifiedServiceAccountCommentIsAServicePrincipal`, which fails on the base with `{Kind:human ...}`.

### F4: the phone UI has no signed-in identity or Log out (filed)

At 390 px `top-bar-user` is not rendered, and `PhoneNav` neither shows the verified email nor offers Log out. Settings → Access, reached by URL, works. This needs a design choice (where in the drawer it goes) and a design-doc update, so I filed it as a follow-up ("Phone UI: show the verified signed-in identity and Log out in PhoneNav"), which depends on this task.

### O1: settings saves record no actor (filed)

See §3. The plan expected an audit log for settings, and core has none. Filed as "Record who saved a settings change", which depends on this task.

### Smaller observations (not filed)

- A `user:` entry naming a `*.gserviceaccount.com` address parses but can never match, because the kind differs. A startup warning might help; it is harmless as is.
- The 401 body tells the caller the precise refusal reason (for example `unknown IAP key ID "k2"`). Only a caller already past IAP, or one going around it, sees this, so I left it.
- Group lookup failures are not cached (by design), so each request by a group-dependent caller during an outage pays up to 10 s now, instead of hanging.

## Also changed

- `ui/e2e/screenshot.mjs`: `--header 'Name: value'` (`HEADER=` for `make ui-screenshots`), passed as `extraHTTPHeaders`, so the IAP-only UI can be shot against a real daemon. Documented in `docs/ui-screenshots.md`.

## CI run locally

- `go test ./pkg/ui/... ./tests/security/...`: ok (pkg/ui 65–69 s, tests/security 22.7 s), after each fix.
- The new tests pass with the fixes and fail without them (shown above).
- No Playwright spec under `ui/e2e/` was changed. `browser.mjs` needs the walkthrough daemon and is not a CI spec.

## Not run, and why

- **Real Google IAP and real Cloud Identity**: not reachable from the sandbox. `iapstub` stands in for IAP's signing, and the group path was tested refusing, both with no ADC and with a silent token endpoint. Group *admission* through Cloud Identity is covered only by the existing `httptest` unit tests.
- **Break-glass through a daemon**: an `AccessCodeWins` verifier can only be registered from Go, so it was checked through the pinned Go tests (§6).
- **A dispatched run's attribution**: no agent backend is configured, so nothing was dispatched. Only API attribution was checked.
