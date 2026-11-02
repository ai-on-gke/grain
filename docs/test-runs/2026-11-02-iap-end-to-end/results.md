| Case | Check | Expected | Got | Result |
|---|---|---|---|---|
| | **IAP on, default allowlist (the owner)** | | | |
| 1 | API: GET /api/auth/user through the proxy | `^200 .*"user":"owner@example.com".*"authMethod":"iap"` | `200 {"authenticated":true,"user":"owner@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 1 | API: GET /api/tasks through the proxy | `^200` | `200` | PASS |
| 1 | API: POST /api/tasks through the proxy | `^20[01] .*"author":"owner@example.com"` | `201 {"id":"20","title":"filed through IAP by curl","description":"","author":"owner@example.com","authorKind":"human","state":"proposed","repo":"acme/widgets","` | PASS |
| 1 | API: GET /api/whoami (operator-attribution spec §5) | `^200 .*owner@example.com` | `404 {"error":"no such API endpoint: GET /api/whoami"}` | N/A: GET /api/whoami not on main (operator-attribution spec §5) |
| 1 | CLI: grain list through the proxy, no code | `filed through IAP by curl` | `1      proposed        acme/widgets             Add a dark mode toggle 2      queued          acme/widgets             Fix flaky retry test 3      running      ` | PASS |
| 1 | CLI: grain create through the proxy, no code | `^[0-9]+$` | `21` | PASS |
| 1 | CLI: the created task's author | `^owner@example.com human$` | `owner@example.com human` | PASS |
| 1 | CLI without IAP (the ID token straight to grain) is refused | `access code|401|single-tenant` | `grain: the grain daemon at http://127.0.0.1:4300 (from $GRAIN_SERVER) refused this CLI's credentials (grain server: this daemon is single-tenant and every calle` | PASS |
| 1 | browser: UI loads with no access code; user tile | | `owner@example.com` | PASS |
| 1 | browser: no 'Enter access code' page | | `http://127.0.0.1:18443/` | PASS |
| 1 | browser: no grain_access cookie is set | | `iapsim_email` | PASS |
| 1 | browser: same-origin POST from the page | | `201 owner@example.com` | PASS |
| 1 | browser: Log out goes to IAP's clear_login_cookie | | `http://127.0.0.1:18443/_gcp_iap/clear_login_cookie` | PASS |
| 9 | browser: cross-site form POST with a valid assertion | | `403 {"error":"cross-origin POST /api/tasks refused: cross-origin request detected from Sec-Fetch-Site he` | PASS |
| 9 | browser: cross-site no-cors fetch POST with a valid assertion | | `403` | PASS |
| 9 | browser: neither cross-site write was stored | | `22` | PASS |
| 3 | browser: expired assertion, browser at 1440px: 401 page, fits | | `401 Sign-in could not be verified overflow 0px` | PASS |
| 3 | browser: expired assertion, browser at 390px: 401 page, fits | | `401 Sign-in could not be verified overflow 0px` | PASS |
| 3 | wrong signature (signed by an unpublished key, kid k1), no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token signature is invalid: crypto/ecdsa: verification error"}` | PASS |
| 3 | wrong signature (signed by an unpublished key, kid k1), plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token signature is invalid: crypto/ecdsa: verification error"}` | PASS |
| 3 | corrupted signature byte, no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token signature is invalid: crypto/ecdsa: verification error"}` | PASS |
| 3 | corrupted signature byte, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token signature is invalid: crypto/ecdsa: verification error"}` | PASS |
| 3 | unknown kid, no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token is unverifiable: error while executing keyfunc: unknown IAP key ID \"k2\""}` | PASS |
| 3 | unknown kid, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token is unverifiable: error while executing keyfunc: unknown IAP key ID \"k2\""}` | PASS |
| 3 | wrong aud, no code | `^401` | `401 {"error":"identity assertion was not accepted: IAP audience mismatch: expected \"/projects/123/global/backendServices/456\", got [/projects/999/global/backe` | PASS |
| 3 | wrong aud, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: IAP audience mismatch: expected \"/projects/123/global/backendServices/456\", got [/projects/999/global/backe` | PASS |
| 3 | wrong iss, no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token has invalid claims: token has invalid issuer"}` | PASS |
| 3 | wrong iss, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token has invalid claims: token has invalid issuer"}` | PASS |
| 3 | expired, no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token has invalid claims: token is expired"}` | PASS |
| 3 | expired, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token has invalid claims: token is expired"}` | PASS |
| 3 | issued in the future, no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token has invalid claims: token used before issued"}` | PASS |
| 3 | issued in the future, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token has invalid claims: token used before issued"}` | PASS |
| 3 | garbled token, no code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token is malformed: could not JSON decode header: invalid character '\\u009e' look` | PASS |
| 3 | garbled token, plus the valid code | `^401` | `401 {"error":"identity assertion was not accepted: invalid IAP JWT assertion: token is malformed: token contains an invalid number of segments"}` | PASS |
| 3 | garbled token, plus the access cookie (browser) | `^401` | `401 <!doctype html> <html lang="en"> <head> <meta charset="utf-8"> <meta name="viewport" content="width=device-width, initial-scale=1"> <title>grain: sign-in no` | PASS |
| 3 | a browser navigation with an expired assertion gets a 401 page | `^401 .*Sign-in could not be verified` | `401 <!doctype html> <html lang="en"> <head> <meta charset="utf-8"> <meta name="viewport" content="width=device-width, initial-scale=1"> <title>grain: sign-in no` | PASS |
| 3 | refused assertions are logged | `refused.*(signature|expired|issuer|audience)` | `2026/11/02 09:19:48 ui: refused an X-Goog-IAP-JWT-Assertion (issuer "https://iap.test.local", audience "/projects/123/global/backendServices/456"): invalid IAP ` | PASS |
| 4 | X-Goog-Authenticated-User-Email alone | `^401` | `401 {"error":"this daemon is single-tenant and every caller shows its access code: sign in with it on the UI's \"Enter access code\" page, or send it as a beare` | PASS |
| 4 | X-Grain-User alone | `^401` | `401 {"error":"this daemon is single-tenant and every caller shows its access code: sign in with it on the UI's \"Enter access code\" page, or send it as a beare` | PASS |
| 4 | code + X-Goog-Authenticated-User-Email: mallory is not mallory | `^200 .*"authMethod":"trusted"` | `200 {"authenticated":false,"identityNotAccepted":"This request's identity was not accepted because it names \"mallory@example.com\" only in the unsigned X-Goog-` | PASS |
| 4 | code + X-Grain-User: mallory is not mallory | `^200 .*"authMethod":"trusted"` | `200 {"authenticated":false,"tenancy":"single","authMethod":"trusted"}` | PASS |
| 4 | assertion(owner) + X-Goog-Authenticated-User-Email: mallory is owner | `^200 .*"user":"owner@example.com"` | `200 {"authenticated":true,"user":"owner@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 4 | assertion(owner) + X-Grain-User: mallory is owner | `^200 .*"user":"owner@example.com"` | `200 {"authenticated":true,"user":"owner@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 4 | X-Goog-Authenticated-User-Email is logged | `ignored the unsigned X-Goog-Authenticated-User-Email` | `2026/11/02 09:19:45 grain demo: serving fake tasks from a throwaway store at http://[::]:4300 -- nothing here is real 2026/11/02 09:19:48 ui: refused an X-Goog-` | PASS |
| 4 | X-Grain-User is logged | `ignored the unsigned X-Grain-User` | `0` | DIFFERS: not logged by design -- grain's own UI sends X-Grain-User on every request (ownClientIdentityHeaders) |
| 5 | assertion(owner) + code: the caller is the person | `^200 .*"user":"owner@example.com".*"authMethod":"iap"` | `200 {"authenticated":true,"user":"owner@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 5 | assertion(owner) + code: a task filed is the person's | `^owner@example.com human$` | `owner@example.com human` | PASS |
| 5 | assertion(mallory, not allowed) + code: refused, not trusted | `^403` | `403 {"error":"principal \"mallory@example.com\" is not allowed by -ui-allowed-principals"}` | PASS |
| 9 | same-origin POST (Origin = proxy) | `^201` | `201 {"id":"24","title":"csrf probe","description":"","author":"owner@example.com","authorKind":"human","state":"proposed","repo":"acme/widgets","branch":"grain/` | PASS |
| 9 | cross-origin POST (Origin: https://evil.example) | `^403` | `403 {"error":"cross-origin POST /api/tasks refused: cross-origin request detected from Sec-Fetch-Site header"}` | PASS |
| 9 | cross-site POST (Sec-Fetch-Site only) | `^403` | `403 {"error":"cross-origin POST /api/tasks refused: cross-origin request detected from Sec-Fetch-Site header"}` | PASS |
| 9 | Origin: null | `^403` | `403 {"error":"cross-origin POST /api/tasks refused: cross-origin request detected, and/or browser is out of date: Sec-Fetch-Site is missing, and Origin does not` | PASS |
| 9 | IAP cookie, no Origin/Sec-Fetch-Site | `^403` | `403 {"error":"CSRF check refused POST /api/tasks: state-changing cookie-authenticated request must carry Sec-Fetch-Site: same-origin or a matching Origin"}` | PASS |
| 9 | cross-origin DELETE | `^403` | `403 {"error":"cross-origin DELETE /api/tasks/1 refused: cross-origin request detected, and/or browser is out of date: Sec-Fetch-Site is missing, and Origin does` | PASS |
| | **IAP on, -ui-allowed-principals with a fake Cloud Identity** | | | |
| 2 | user:alice admitted | `^200 .*"user":"alice@example.com"` | `200 {"authenticated":true,"user":"alice@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 2 | user: entry is case-insensitive (ALICE@Example.com) | `^200 .*"user":"alice@example.com"` | `200 {"authenticated":true,"user":"alice@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 2 | bob (no entry) refused, API | `^403 .*bob@example.com` | `403 {"error":"principal \"bob@example.com\" is not allowed by -ui-allowed-principals"}` | PASS |
| 2 | the owner is not in a replaced list | `^403` | `403 {"error":"principal \"owner@example.com\" is not allowed by -ui-allowed-principals"}` | PASS |
| 2 | domain:corp.example admits dev@corp.example | `^200 .*"user":"dev@corp.example"` | `200 {"authenticated":true,"user":"dev@corp.example","tenancy":"single","authMethod":"iap"}` | PASS |
| 2 | domain: is exact (dev@sub.corp.example) | `^403` | `403 {"error":"principal \"dev@sub.corp.example\" is not allowed by -ui-allowed-principals"}` | PASS |
| 2 | serviceAccount: admits bot | `^200 .*"user":"bot@proj.iam.gserviceaccount.com"` | `200 {"authenticated":true,"user":"bot@proj.iam.gserviceaccount.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 2 | serviceAccount: principal kind | `^bot@proj.iam.gserviceaccount.com service$` | `bot@proj.iam.gserviceaccount.com service` | PASS |
| 2 | a service account never matches a user: entry (alice's domain SA) | `^403` | `403 {"error":"principal \"other@proj.iam.gserviceaccount.com\" is not allowed by -ui-allowed-principals"}` | PASS |
| 2 | group: member carol admitted (fake lookup) | `^200 .*"user":"carol@example.com"` | `200 {"authenticated":true,"user":"carol@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 2 | group: non-member dave refused | `^403 .*dave@example.com` | `403 {"error":"principal \"dave@example.com\" is not allowed by -ui-allowed-principals"}` | PASS |
| 2 | the fake Cloud Identity was asked | `"checkTransitiveMembership":[1-9]` | `{"checkTransitiveMembership":2,"proxied":2}` | PASS |
| 2 | a direct entry is decided without asking Cloud Identity | `^\{\}$` | `{}` | PASS |
| 8 | file: task author (API) | `^alice@example.com human` | `alice@example.com human` | PASS |
| 8 | comment: author (API) | `^alice@example.com human$` | `alice@example.com human` | PASS |
| 8 | close: transition by (API) | `^human alice@example.com$` | `human alice@example.com` | PASS |
| 8 | file+approve: store's origin and approval actors | `^human\|alice@example.com\|human\|alice@example.com$` | `human\|alice@example.com\|human\|alice@example.com` | PASS |
| 8 | close: store's task_event actor | `^closed\|human\|alice@example.com$` | `closed\|human\|alice@example.com` | PASS |
| 8 | the recorded source is iap ({human, email, iap}; spec OA-1.1) | `iap` | ` (no authorSource in the API)` | N/A: Principal.Source (OA-1.1) not on main |
| 8 | access code: recorded as the deployment's actor | `^owner@example.com human$` | `owner@example.com human` | PASS |
| 8 | access code: shown as 'operator (access code)' (spec OA-1.2.2) | `operator \(access code\)` | `{"author":"owner@example.com","authorKind":"human","authorLabel":null}` | N/A: <p>Label (OA-1.2.2) not on main |
| 2 | browser: refusal page names bob | | `Signed in as bob@example.com (verified by iap), whom this daemon's allowlist does not admit.` | PASS |
| 2 | browser: refusal page says how the owner adds bob | | `To let them in, the owner adds user:bob@example.com to the daemon's -ui-allowed-principals (or GRAIN_ALLOWED_PRINCIPALS in its environment) and restarts it. The` | PASS |
| 2 | browser: refusal page offers the access-code form | | `no form; says: iap's identity takes precedence over the access code on requests that come through it, so signing in by code here would still be refused. Reach t` | DIFFERS: by design for IAP (identity beats the code), the page explains how to use the code instead |
| 2 | browser: refusal page fits a 390px phone | | `overflow 0px` | PASS |
| 2 | browser: alice (user:) gets in; tile | | `alice@example.com` | PASS |
| 8 | browser: task page names alice as filer/commenter/closer | | `4 mentions` | PASS |
| 8 | browser: task page shows 'operator (access code)' nowhere for alice's task | | `-` | PASS |
| | **group: entries with no lookup configured** | | | |
| 2 | no ADC: a direct entry still admits alice | `^200 .*alice@example.com` | `200 {"authenticated":true,"user":"alice@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 2 | no ADC: group-dependent carol refused, fails closed | `^403 .*(credentials|group)` | `403 {"error":"principal \"carol@example.com\" could not be authorized against group allowlist: looking up Cloud Identity group \"ops@example.com\": Get \"https:` | PASS |
| 2 | no ADC: the Cloud Identity stand-in was never called | `^\{\}$` | `{}` | PASS |
| 2 | no group: entries, lookup available: carol refused | `^403` | `403 {"error":"principal \"carol@example.com\" is not allowed by -ui-allowed-principals"}` | PASS |
| 2 | no group: entries: Cloud Identity never called (not even a token) | `^\{\}$` | `{}` | PASS |
| | **No IAP configured** | | | |
| 6 | no code: 401 | `^401` | `401` | PASS |
| 6 | bearer code: 200 trusted | `^200 .*"authMethod":"trusted"` | `200 {"authenticated":false,"tenancy":"single","authMethod":"trusted"}` | PASS |
| 6 | bearer code from the network (192.168.9.13) | `^200` | `200` | PASS |
| 6 | a browser gets the login page | `^401 .*[Aa]ccess code` | `401 <!doctype html> <html lang="en"> <head> <meta charset="utf-8"> <meta name="viewport" content="width=device-width, initial-scale=1"> <title>grain: enter acce` | PASS |
| 6 | POST /api/auth/access-code sets grain_access | `^200` | `200` | PASS |
| 6 | the cookie authenticates | `^200` | `200` | PASS |
| 6 | a wrong code is refused | `^(401|403)` | `401` | PASS |
| 6 | a valid-looking assertion does not authenticate | `^401` | `401` | PASS |
| 6 | ... and is logged | `no IAP audience is configured` | `2026/11/02 09:19:58 grain demo: serving fake tasks from a throwaway store at http://[::]:4300 -- nothing here is real 2026/11/02 09:19:58 ui: refused an X-Goog-` | PASS |
| 6 | browser: login page shown | | `Enter access code` | PASS |
| 6 | browser: grain_access cookie set, SameSite=Strict, HttpOnly | | `{"sameSite":"Strict","httpOnly":true}` | PASS |
| 6 | browser: the UI loads after the code | | `http://127.0.0.1:4300/` | PASS |
| 6 | browser: no signed-in email tile with the access code | | `0` | PASS |
| | **-ui-require-iap** | | | |
| 7 | loopback, no forwarding headers: bearer works | `^200 .*"authMethod":"trusted"` | `200 {"authenticated":false,"tenancy":"single","authMethod":"trusted"}` | PASS |
| 7 | loopback, no forwarding headers: login sets a cookie | `^200` | `200` | PASS |
| 7 | loopback, no forwarding headers: cookie works | `^200` | `200` | PASS |
| 7 | network (192.168.9.13): bearer refused | `^(401|403)` | `401 {"error":"this daemon requires IAP (-ui-require-iap): the access code is not accepted over the network, so sign in through the deployment's IAP URL; the cod` | PASS |
| 7 | network (192.168.9.13): cookie refused | `^(401|403)` | `401` | PASS |
| 7 | network (192.168.9.13): login refused | `^403` | `403 {"error":"this daemon requires IAP (-ui-require-iap): the access code is not accepted over the network, so sign in through the deployment's IAP URL; the cod` | PASS |
| 7 | loopback + X-Forwarded-For: bearer refused | `^(401|403)` | `401 {"error":"this daemon requires IAP (-ui-require-iap): the access code is not accepted over the network, so sign in through the deployment's IAP URL; the cod` | PASS |
| 7 | loopback + X-Forwarded-For: cookie refused | `^(401|403)` | `401` | PASS |
| 7 | loopback + Forwarded: bearer refused | `^(401|403)` | `401` | PASS |
| 7 | through the proxy (the code is not needed): the IAP person | `^200 .*"authMethod":"iap"` | `200 {"authenticated":true,"user":"owner@example.com","tenancy":"single","authMethod":"iap"}` | PASS |
| 7 | network browser: no login page | `^(401|403)` | `401 <!doctype html> <html lang="en"> <head> <meta charset="utf-8"> <meta name="viewport" content="width=device-width, initial-scale=1"> <title>grain: sign in th` | PASS |
| 7 | loopback CLI (kubectl exec break-glass) still works with the code | `proposed|queued` | `1      proposed        acme/widgets             Add a dark mode toggle 2      queued          acme/widgets             Fix flaky retry test 3      running      ` | PASS |
| 7 | browser: through IAP: UI with no code; tile | | `owner@example.com` | PASS |
| 7 | browser: over the network without IAP: no access-code form | | `401 Sign in through IAP  This grain requires Identity-Aware Proxy and does not take an access code over the network. Open it` | PASS |
| 7 | grain demo -ui-require-iap with no audience refuses to start | `needs an IAP audience` | `grain demo: -ui-require-iap needs an IAP audience: set -ui-iap-audience (or $GRAIN_IAP_AUDIENCE) to the IAP backend's /projects/NUMBER/global/backendServices/ID` | PASS |
| 7 | grain daemon -ui-require-iap with no audience refuses to start | `needs an IAP audience` | `grain daemon: -ui-require-iap needs an IAP audience: set -ui-iap-audience (or $GRAIN_IAP_AUDIENCE) to the IAP backend's /projects/NUMBER/global/backendServices/` | PASS |
| 7 | ... and $GRAIN_IAP_AUDIENCE counts as an audience (starts) | `serving fake tasks` | `2026/11/02 09:20:03 grain demo: serving fake tasks from a throwaway store at http://127.0.0.1:4300 -- nothing here is real` | PASS |
