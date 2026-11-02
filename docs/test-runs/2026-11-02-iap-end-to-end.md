# Test run — 2026-11-02 — IAP access end to end (v3.1.0 release gate)

Task 2510. This run tests the IAP sign-in work on `main`: the `IdentityVerifier` seam with IAP as its first verifier (task 2309), `-ui-require-iap` (task 2318), the refusal page (task 2383), and whatever operator attribution has merged (tasks 2310, 2311 onward). It runs against a real `grain demo` placed behind a **simulated** IAP, with a real Chromium browser for the UI and `curl` and the `grain` CLI for the API.

**Result: 112 checks PASS and 0 FAIL.** Three checks are **N/A** because the operator-attribution spec has not landed. Two **DIFFER** from the brief because the code deliberately does something else (D1, D2). One bug was found and fixed here (F1). One follow-up is filed: the real GKE + IAP test.

Re-run it with `tests/iap/walkthrough.sh [OUT]`; the script header lists what it needs. Full per-check table: [`2026-11-02-iap-end-to-end/results.md`](2026-11-02-iap-end-to-end/results.md).

## Environment

| | |
|---|---|
| Base | `origin/main` `40353767` (PR #460) |
| Binary | `cd ui && npm ci && npm run build && cd .. && go build ./cmd/grain` (embedded UI), with this branch's two commits |
| OS | Debian GNU/Linux 12 (bookworm), kernel 6.1.0-54-amd64; Go 1.26.10; Node 20.19.0 |
| Browser | Chromium (Playwright 1.62.1, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, build chromium-1234), headless, 1440×900 and 390×844 |
| Server | `grain demo -addr 0.0.0.0:4300 -as owner@example.com -ui-iap-audience /projects/123/global/backendServices/456 -ui-iap-issuer https://iap.test.local -ui-iap-keys-url http://127.0.0.1:18080/jwks [...]`. `grain demo` did not accept the `-ui-iap-*` flags before this branch; see *Also changed* |
| Simulated IAP | [`tests/iap/iapsim.go`](../../tests/iap/iapsim.go). The **issuer** (`:18080`) publishes P-256 key `k1` as a JWK set at `/jwks`, and `/sign` mints ES256 assertions with `iss`, `aud`, `exp`, `iat`, `email`, `sub` and `hd`. Key `k2` is never published. The **proxy** (`:18443`) is IAP's data path. It drops any `X-Goog-*` header the client sent, signs an assertion for the signed-in person, and adds it together with the plaintext `X-Goog-Authenticated-User-Email`. It sets `X-Forwarded-For`/`-Proto` and keeps the client's `Host`. "Signed in" is a cookie set by a stand-in Google sign-in page (`/_iapsim/login`), or a programmatic ID token (`Bearer iapsim-idtoken.<email>`) for the CLI. `/_gcp_iap/clear_login_cookie` signs out |
| Cloud Identity | iapsim also serves a fake `groups:lookup` and `checkTransitiveMembership` (`-group ops@example.com=carol@example.com`) and an OAuth `/token` endpoint. The demo reaches them through real Application Default Credentials: a service-account JSON whose `token_uri` is iapsim. So the real `CloudIdentityGroupChecker` OAuth and HTTP path runs |
| Isolation | Empty `HOME`, no `GOOGLE_APPLICATION_CREDENTIALS` except where stated, `GCE_METADATA_HOST=127.0.0.1:9` (no metadata server) |
| Tools installed | `sqlite3` (to read the approval actor from the store), `tesseract-ocr` (to read the screenshots, see below), via apt |

## Results by case

| # | Case | Result | Evidence (rows in results.md; screenshots) |
|---|---|---|---|
| 1 | **Allowed person: UI, API, CLI with no access code** | **PASS** | Browser: it is sent to the simulated Google sign-in ([01](2026-11-02-iap-end-to-end/01-iap-simulated-google-signin.png)), then the UI loads with no access-code page and no `grain_access` cookie. The user tile reads `owner@example.com` ([02](2026-11-02-iap-end-to-end/02-iap-ui-signed-in-owner.png)). The menu ([03](2026-11-02-iap-end-to-end/03-iap-user-menu.png)) has a Log out item that lands on `/_gcp_iap/clear_login_cookie`. API through the proxy: `GET /api/auth/user` returns `200 {"user":"owner@example.com","authMethod":"iap"}`; `GET /api/tasks` returns 200; `POST /api/tasks` returns 201 with author `owner@example.com`. CLI: `GRAIN_SERVER=<proxy> GRAIN_ID_TOKEN=<id token> grain list` and `grain create` succeed with no access code, and the CLI's task is authored `owner@example.com human`. The same CLI pointed straight at grain is refused |
| 1 | `GET /api/whoami` | **N/A** | 404. The endpoint is not on `main` (operator-attribution spec §5) |
| 2 | **Allowlist** `user:alice@example.com,domain:corp.example,serviceAccount:bot@proj.iam.gserviceaccount.com,group:ops@example.com` | **PASS** | `user:` admits alice (case-insensitively). bob gets a 403 naming him, and in a browser the **refusal page** ([05](2026-11-02-iap-end-to-end/05-refusal-page-bob.png), phone [05b](2026-11-02-iap-end-to-end/05b-refusal-page-bob-phone.png), 0 px overflow). The page says "Signed in as bob@example.com (verified by iap)" and gives the exact `user:bob@example.com` entry and the `grain init -daemon-arg -ui-allowed-principals='…'` re-run. The owner is out once the list replaces the default. `domain:corp.example` admits `dev@corp.example` and not `dev@sub.corp.example`. `serviceAccount:` admits bot, whose task is authored with kind **`service`**. A `user:`-shaped SA is refused |
| 2 | `group:` with a fake lookup | **PASS** | carol (a member) gets 200 and dave gets 403. The fake was asked (`checkTransitiveMembership` ≥ 1). alice, on a direct entry, is decided with **zero** Cloud Identity calls |
| 2 | `group:` with no lookup configured | **PASS** | With no ADC: alice (direct) gets 200, and carol gets a 403, failing closed with `could not find default credentials`. The stand-in saw **no** calls. With a lookup available but no `group:` entries: Cloud Identity and its token endpoint were never called |
| 2 | refusal page "still offers the access-code form" | **DIFFERS (D1)** | Under IAP the page offers no form. It explains that IAP's identity takes precedence over the code, and how to sign in by code without going through IAP. This is the design (task 2383): the code would be refused anyway |
| 3 | **Bad assertions get 401 with no fall-through** | **PASS** | Each of these, sent straight to grain, gets **401 both alone and with the valid access code**: wrong signature (kid `k1`, signed by `k2`), a corrupted signature byte, an unknown kid, a wrong `aud`, a wrong `iss`, an expired token, `iat` in the future, and a garbled token. Refusals are logged (`ui: refused an X-Goog-IAP-JWT-Assertion …`). A browser now gets a 401 *page* ([11](2026-11-02-iap-end-to-end/11-bad-assertion-page.png), phone [11b](2026-11-02-iap-end-to-end/11b-bad-assertion-page-phone.png)); see F1 |
| 4 | **Spoofed plaintext headers** | **PASS** | `X-Goog-Authenticated-User-Email` alone gets 401, and so does `X-Grain-User` alone. With the access code, either one naming mallory leaves the caller `trusted`, not mallory. With owner's assertion, either one naming mallory leaves the caller owner. `X-Goog-Authenticated-User-Email` is logged (`ignored the unsigned X-Goog-Authenticated-User-Email header naming …`) |
| 4 | `X-Grain-User` is logged | **DIFFERS (D2)** | Not logged, on purpose. Grain's own UI sends `X-Grain-User` on every request, so `logIgnoredIdentityHeader` skips `ownClientIdentityHeaders`. It is still ignored |
| 5 | **Assertion plus access code** | **PASS** | Owner's assertion plus the code gives `authMethod: iap`, `user: owner@example.com`, and a task filed that way is authored by the person. Mallory (not allowed) plus a **valid** code gets 403: the code does not rescue a refused person |
| 6 | **No IAP configured** | **PASS** | No code gets 401. A bearer code gets 200 `trusted`, also from the network address. A browser gets the "Enter access code" page ([07](2026-11-02-iap-end-to-end/07-noiap-access-code-page.png)); typing the code sets `grain_access` (`SameSite=Strict`, `HttpOnly`) and the UI loads with no email tile ([08](2026-11-02-iap-end-to-end/08-noiap-ui-after-code.png)). A wrong code is refused. A valid assertion does not authenticate and is logged (`no IAP audience is configured`) |
| 7 | **`-ui-require-iap`** | **PASS** | From `192.168.9.13` (non-loopback): the bearer is refused, the cookie is refused, `POST /api/auth/access-code` gets 403, and a browser gets "Sign in through IAP" with no form ([10](2026-11-02-iap-end-to-end/10-require-iap-network-no-login.png)). From loopback with no forwarding headers: the bearer, the login and the cookie work, and so does `grain list` with the code (the `kubectl exec` break-glass path). From loopback with `X-Forwarded-For` or `Forwarded`: the bearer and the cookie are refused. Through IAP the UI works with no code ([09](2026-11-02-iap-end-to-end/09-require-iap-ui-through-iap.png)). With no audience, `grain demo -ui-require-iap` and `grain daemon -ui-require-iap` both refuse to start (`-ui-require-iap needs an IAP audience …`), and `$GRAIN_IAP_AUDIENCE` counts as an audience |
| 8 | **Attribution: file, comment, approve, close by an IAP person** | **PASS** (kind + email) | alice files, comments, approves and closes. The API shows `author alice@example.com human`, the comment `alice@example.com human` and the close transition `by: human alice@example.com`. The store has `origin_actor` and `approval_actor` = `human\|alice@example.com`, and `task_event closed` = `human\|alice@example.com`. The task page names her as commenter and closer ([06](2026-11-02-iap-end-to-end/06-attribution-task-page-alice.png)) |
| 8 | `{…, iap}` source; "operator (access code)" label | **N/A** | `Principal.Source` (spec OA-1.1) and the `<p>Label` fields (OA-1.2.2) are not on `main`: there is no `*_source` column and no `authorSource`/`authorLabel`. These are the operator-attribution steps from task 2310 onward. **As things stand, access-code actions are recorded as the deployment's actor, `owner@example.com human`, the same as the verified owner's**, so the two cannot be told apart. The approval actor is stored, but the timeline does not show it ("Waiting", with no "by") |
| 9 | **CSRF with a valid assertion** | **PASS** | curl through the proxy, with the assertion: same-origin `POST` gets 201. A foreign `Origin` gets 403, as do `Sec-Fetch-Site: cross-site` alone, `Origin: null`, the IAP cookie with no `Origin`/`Sec-Fetch-Site`, and a cross-origin `DELETE`. Browser: from `http://localhost:18091` (another site), a cross-site `<form enctype=text/plain>` `POST` gets **403** ([04](2026-11-02-iap-end-to-end/04-csrf-cross-site-form-refused.png)), as does a `no-cors` `fetch` `POST`, and neither was stored. A same-origin `fetch` from the UI page gets 201 |

### Looking at the screenshots

The sandbox's file reader does not render PNGs, and there is no Gemini key (`LOOK=1`), so I read each shot with `tesseract`, plus 3× crops of the header:

- **02/03/09:** the header ends `… contact running … owner@example.com`. The tile sits right of the header icons.
- **08** (access code): the same header with no email.
- **05/05b:** the refusal page text, quoted in case 2. It wraps inside the card at 390 px, and `scrollWidth − clientWidth` is 0.
- **11/11b:** "Sign-in could not be verified", the reason `token is expired`, and the owner's checklist. Overflow is 0 at both widths.
- **10:** "Sign in through IAP … does not take an access code over the network".
- **06:** the timeline shows "Closed by human alice@example.com - done" and "alice@example.com - human / a comment from alice", and the details show "Creator alice@example.com".

The page-level geometry the script asserts is the tile's text, no horizontal overflow on the server-rendered pages at 390 px, and no access-code input where none should be.

## Findings

### F1: a browser whose assertion failed got the API's JSON (fixed)

To reproduce on `40353767`: start the demo or a daemon with `-ui-iap-*`, then open the UI with an expired (or rotated-key, or wrong-audience) `X-Goog-IAP-JWT-Assertion`. The response is `401 application/json {"error":"identity assertion was not accepted: … token is expired"}`, shown as raw JSON. IAP signs every request afresh, so a person normally sees this only after a key rotation, a backend or audience change, or clock skew, which is exactly when they need to be told what is going on.

**Fix:** `writeAssertionRefused` (`pkg/ui/principalrefusal.go`), called from both refusal sites (`server.go` and `singleTenantGate`). A browser navigation (`browserNavigation`: a non-`/api/` `GET`/`HEAD`) gets a 401 HTML page with the reason and the owner's checklist. Everything else keeps the JSON. The status is unchanged and nothing falls through to the access code. Regression test: `TestUnverifiedAssertionGetsAPageForABrowserAndJSONForTheAPI`, which fails on the base. The walkthrough checks the page with curl and in Chromium at 1440 and 390 px.

### D1: the refusal page offers no access-code form under IAP (by design)

The brief expects the refusal page to still offer the access-code form. For IAP it shows the "Owner: sign in with the access code" heading, then explains that IAP's identity beats the code on requests that come through IAP, and to use the code on the host or over an SSH tunnel. The form appears only for a verifier that declares `AccessCodeWins` (pinned by `TestRefusedPrincipalGetsAPageNamingThemWithTheAccessCodeForm`). This follows `authentication.design.md` §1 "Precedence over the access code is per verifier": a form that cannot work would mislead. No change.

### D2: `X-Grain-User` is ignored but not logged (by design)

`logIgnoredIdentityHeader` logs only proxy-sent plaintext headers. `X-Grain-User`, `Grain-User` and `X-Forwarded-User` are what grain's own clients send on every request, so logging them would log every signed-out browser. They never authenticate (case 4). No change.

### Attribution that has not landed (N/A, not a defect of this work)

`GET /api/whoami`, `Principal.Source`/`AuthSource`, the `*_source` columns and `<p>Label` ("operator (access code)") are all still spec only (`design/spec/operator-attribution/operator-attribution.spec.md`). Today a verified person is recorded as `{human, email}` everywhere that was checked, but an access-code holder is recorded under the deployment's `-as` identity with kind `human`. When `-as` is the owner's email, as `grain init` sets it, **the access code and the verified owner leave identical records**. That is the gap the spec's `Source` closes. Re-run this walkthrough when it lands: the N/A rows turn into real checks with no edit.

## Also changed

- **`grain demo`** takes `-ui-iap-audience`, `-ui-iap-issuer`, `-ui-iap-keys-url`, `-ui-require-iap` and `-ui-allowed-principals`. They mean what they mean on `grain daemon` and are validated by the same `daemon.CheckUIAllowedPrincipals`/`CheckUIRequireIAP`. It also takes `-ui-cloud-identity-endpoint`, which resolves `group:` entries against a stand-in Cloud Identity. With none of them set the demo behaves exactly as before.
- **`tests/iap/`**: `iapsim.go` (`//go:build ignore`), `walkthrough.sh` and `browser.mjs`. These are not CI specs, because they need the demo, iapsim and ports 4300/18080/18443/18091.

## CI run locally

- `go test ./pkg/ui/... ./pkg/cli/ ./pkg/demo/ ./tests/security/...`: ok (pkg/ui 66 s, pkg/cli 147 s, pkg/demo 3.5 s, tests/security 23 s).
- `tests/iap/walkthrough.sh /tmp/iap-final`: exit 0, 112 PASS, 0 FAIL, 3 N/A, 2 DIFFERS.

## What this simulation could not cover

These need a real GKE + IAP load balancer (filed as a follow-up):

- **Google's real issuer and keys:** `https://cloud.google.com/iap`, the `gstatic.com` JWK set and its rotation, and real `sub`/`hd` values. iapsim signs exactly the claims IAP documents, but key rotation timing and caching against Google's endpoint were not exercised.
- **IAP's own header handling:** whether a real IAP strips a client-supplied `X-Goog-IAP-JWT-Assertion`, and what it does with `Authorization` versus `Proxy-Authorization` ID tokens. iapsim drops `X-Goog-*` and leaves `Authorization` in place, the harsher case for grain.
- **The real browser flow:** Google sign-in, the `GCP_IAP_*` cookies and their `SameSite` attributes, re-authentication on session expiry, and `/_gcp_iap/clear_login_cookie` actually signing the person out. Cross-site requests carrying the IAP cookie were simulated by sending the identity on every request.
- **Real Cloud Identity:** group admission, nested groups, quota and latency, and the `grain-daemon@` IAM grant. The real checker's HTTP and OAuth path ran, but against a fake.
- **The network path:** the GKE Gateway and GFE `X-Forwarded-For` chains, a request that bypasses the Gateway and reaches the pod IP directly (simulated by curling grain's port), and `kubectl exec` break-glass in a pod (simulated by loopback).
- **hostd and external agents through IAP:** the IAP principal recorded on a hostd connection and the owner match (rollout step 3). Not in this run's scope, and the CLI's own IAP mode has not landed (the CLI here carried a ready-made ID token).
- **A dispatched run's attribution:** nothing was dispatched (the demo runs no agents).
