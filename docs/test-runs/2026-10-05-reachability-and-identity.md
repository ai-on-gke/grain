# Reaching grain and proving who you are: 2026-10-05

grain/task-1999. This run executes §8 of
[`docs/test-plans/2026-10-05.md`](../test-plans/2026-10-05.md). It covers:
- the disconnected banner across a real daemon kill and restart;
- identity from verified signed assertions only;
- "Sign in with Google" in the sign-in dialog;
- the relay client behind an IAP stand-in;
- the relay client's stop at a tunnel gateway;
- reduced motion.

It uses real `grain demo`, `grain daemon`, `grain-relay` and `grain-tunnel-gateway` processes. Only Google's
side (OAuth, IAP and the metadata server) is stubbed, with local keys.

Summary:
- Four of the five plan steps behaved as described.
- Step 5 did not. With only `-tunnel-gateway-url`, a released daemon's relay client still retries forever,
  because the daemon's UI answers the relay upgrade with a 200 page, not a 404. This is fixed here (finding 1).
- Finding 2 is filed as a follow-up: an operator who relied on the plaintext header learns why they were
  refused only from a 5-second toast, and the daemon logs nothing.

## Environment

| | |
| --- | --- |
| Commit tested | `0c790498f` (the `candidate` tip, "Merge pull request #2920"). It equals `origin/candidate`, then this branch's commits on top |
| Branch | `grain/task-1999`, based on `candidate` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| UI bundle | `cd ui && npm ci && npm run build` into `pkg/ui/static`. Every server below serves that production bundle |
| Servers | `grain demo` through `ui/e2e` (`GRAIN_E2E_DEMO_BIN` from `global-setup.js`). `/tmp/grain daemon -data-dir … -ui-addr 127.0.0.1:<port> -disable-native-sandboxes`, `/tmp/grain-relay` and `/tmp/grain-tunnel-gateway`, all built from this tree with `go build ./cmd/...` |
| Model / agents | None |

All of this section's commits are on `candidate`. `git branch -r --contains <sha>` lists `origin/candidate`
(and `origin/main`) for each of `9460f11eb`, `8864baf1e`, `cab728ab0`, `fac7681cb`, `f94fa1969`,
`67f1f439b`, `ab2c91f9d`, `6b07b4415`, `f0b2d78c2`, `4a37b3cd0` and `18b95ac0c`.

## Stubs

Nothing in grain was mocked, and React was not mocked. Each stub below stands in for Google, or for a gateway
in front of grain:

1. **IAP's signing key and key endpoint.**
   - The Go tests use `tests/security/signed_assertion_test.go`'s `newAssertionSigner`. The manual walk uses a
     throwaway `mint` program on `127.0.0.1:18500`.
   - Each generates an ES256 P-256 key and serves its JWK set.
   - The daemon was pointed at it with `-ui-iap-keys-url`, `-ui-iap-issuer https://iap.walk.example` and
     `-ui-iap-audience /projects/1/global/backendServices/2`.
2. **Google's OAuth authorize page.** `page.route("https://accounts.google.com/**")` in
   `authentication-settings.spec.js` answers with a stub page. The client ID
   (`1234-dialog.apps.googleusercontent.com`) and secret are stub values.
3. **The GCE metadata server.** `GCE_METADATA_HOST=127.0.0.1:18600`, served by a throwaway `iapproxy`, makes
   the relay client's `-relay-audience` ID-token provider (`modelexec.IAPTokenProvider`) fetch an ES256 ID
   token signed by a local key.
4. **IAP in front of the relay.**
   - The same `iapproxy` program listens on `127.0.0.1:18601`.
   - Like IAP, it takes the credential from `Proxy-Authorization`, or from `Authorization` when there is no
     `Proxy-Authorization`. It verifies the token's signature, `aud=walk-relay-aud` and `exp`.
   - It strips `Proxy-Authorization` and reverse-proxies the request, upgrade included, to a real `grain-relay`
     on `:18602`.
5. **A gateway answering 503 or 500.** `page.route` on `/api/**` in `disconnection.spec.js`, in front of a
   live daemon. The plan's standing notes count failing a real response as not mocking it.

The throwaway programs lived in `.walk/` (excluded through `.git/info/exclude`, not committed).

## 1. The disconnected banner across a real kill and restart

**Commands.**

```
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/disconnection.spec.js --project=chromium
```

That ran 7 tests, which all passed (2.6 min).

`disconnection.spec.js` starts its own `grain demo` on a fixed `127.0.0.1:<port>` and `SIGKILL`s it. It then
starts a new one on the same address with no page reload. The new cases, run once with
`emulateMedia({reducedMotion: 'no-preference'})` and once with `'reduce'`, are:

| Case | Outcome |
| --- | --- |
| Task list: kill the daemon | One `data-testid="disconnected-banner"` with `role="status"` within one poll, reading "This deployment cannot be reached: the connection to it failed. Unreachable since … -- retrying automatically". Over 7 s more of failed ticks of both polls it stayed at one banner, with zero `role="alert"`, zero `.MuiSnackbar-root` and no "Failed to fetch" text. |
| Task list: restart the daemon | The banner cleared, and no toast followed. A task filed on the first daemon disappeared from the list because the restarted demo reseeds a fresh store, which shows the list was re-read from the new daemon. `window.__noReload` survived, so there was no reload. |
| Director's session pane (`/session/director`): kill, then restart | Same results: one banner, no toast, and it cleared on restart with the composer still there. |
| `page.route('/api/**')` answering 503 with no body | One banner reading "a gateway in front of it reports it is unavailable (503 Service Unavailable)". It stayed single with no toast, and cleared after `unroute`. |
| `page.route('/api/**')` answering 500 | No banner and no toast. That is grain answering, so the poll stays quiet. |
| `reducedMotion: 'reduce'` | `matchMedia('(prefers-reduced-motion: reduce)')` was true, and every `transition-duration` on the banner was `0s`. The drawer and dialog halves of `18b95ac0c` are already covered by `reduced-motion.spec.js`. |

**Differences from the plan's wording** (observations, not filed):
- **The banner's wording and threshold changed.** The plan describes `9460f11eb`'s banner: "Can't reach grain —
  retrying…", shown after two consecutive failures, with `ui/src/reachability.js`. On `candidate`,
  `cab728ab0` kept task-26's `DisconnectedBanner`, and `App.jsx`'s `noteUnreachable` puts it up on the
  **first** unreachable poll of either source. Neither the string nor `reachability.js` exists any more.
  Everything the plan asks of it (one banner, `role="status"`, clears on the first answer, no toast) holds.
  But a single dropped poll now raises the banner where the original design waited for a second. To
  reproduce: on a live demo, `page.route('**/api/tasks', r => r.fulfill({status: 503}), {times: 1})`, and the
  banner appears on the next tick and clears on the one after. Whether that flicker matters is a design call,
  so it is noted for a human rather than changed.
- **The demo has no fixed data dir.** `grain demo` has no data-dir flag and seeds a fresh
  `os.MkdirTemp("", "grain-demo-")` store on every start, so "restart on the same data dir" is not possible.
  The spec uses the reseed as its proof that the list refreshed.

## 2. Signed assertions

**Go tests.** `tests/security/signed_assertion_test.go` is new. It runs a real `ui.Server` behind
`httptest.NewServer`, on a loopback TCP listener, configured as `docs/beyondcorp.md` says to.

```
go test ./tests/security/ -run 'SignedAssertion|BadSignedAssertions|PlaintextEmailHeaderAlone|NoAudienceConfigured' -count=1 -v
```

All four pass:

| Assertion | `/api/auth/user` | `GET /api/tasks` |
| --- | --- | --- |
| Valid (ES256, right `iss`, `aud` and `exp`, `email: accounts.google.com:alice@example.com`) | `authenticated: true, user: alice@example.com` | 200 |
| Wrong `aud`, wrong `iss` (`https://cloud.google.com/iap`), expired, no `exp`, another key under the same `kid`, `alg: none`, signature stripped. Each also carried the plaintext email header | `authenticated: false` | 401, naming `-ui-iap-audience`, `-ui-iap-issuer` and `-ui-iap-keys-url` |
| No assertion, only `X-Goog-Authenticated-User-Email` from loopback, with and without an audience configured | `authenticated: false` | 401, naming `-ui-trust-identity-headers` |
| Valid assertion, but no audience configured | `authenticated: false` | 401 |

**The same checks against a real `grain daemon`:**

```
/tmp/grain daemon -data-dir /tmp/d2/data -ui-addr 127.0.0.1:18432 -disable-native-sandboxes \
  -ui-iap-audience /projects/1/global/backendServices/2 -ui-iap-issuer https://iap.walk.example \
  -ui-iap-keys-url http://127.0.0.1:18500
curl -H "X-Goog-IAP-JWT-Assertion: $(cat /tmp/assert/<case>)" 127.0.0.1:18432/api/auth/user
```

- `valid` gave `{"authenticated":true,"user":"dharb@google.com"}`, and `/api/tasks` returned 403 `user
  "dharb@google.com" does not have access … an admin adds exactly "dharb@google.com" to a team or the access
  list`. That is correct for a fresh deployment.
- `wrong-aud`, `wrong-iss`, `expired`, `other-key`, `unsigned` and `stripped` each gave
  `authenticated:false` and a 401.
- The daemon logged `ui: refused an X-Goog-IAP-JWT-Assertion (issuer …, audience …): … set
  -ui-iap-audience …` once per distinct issuer and audience pair, three lines in all. The signature failures
  shared the expired token's pair and were not logged again, which is the documented "once per distinct pair".

**What an operator who relied on headers sees after upgrading.** I used Chromium with `extraHTTPHeaders`
standing in for the tunnel, against a real daemon:
- **Plaintext header only, nothing configured.** There is no AccessDenied page. `/api/config` answers
  `authenticated:false, allowed:true`, so the app opens the **log in** dialog. Every data read returns 401,
  and the shell's mount fetch shows that 401's text once, as a 5-second toast: "…it names "dharb@google.com"
  only in the unsigned X-Goog-Authenticated-User-Email header, which this deployment does not trust (see
  -ui-trust-identity-headers)". The daemon logs nothing. See finding 2.
- **A valid assertion, audience configured, user not yet added.** This is the AccessDenied page, and it names
  the identity twice (`482bb58b2`): "Logged in as dharb@google.com" and "an admin adds exactly
  dharb@google.com to a team or to the access list". A toast with the 403's text is also shown.
- **An expired assertion.** This shows the log-in dialog and a toast saying the assertion "did not verify
  against this deployment's IAP audience, issuer and keys (-ui-iap-audience, -ui-iap-issuer,
  -ui-iap-keys-url)".

**The operator notes.** `docs/beyondcorp.md` (`67f1f439b`) tells the operator what to set in both cases:
- **The tunnel sends an assertion.** Grep for `refused an X-Goog-IAP-JWT-Assertion`, then set
  `GRAIN_IAP_AUDIENCE` or `-ui-iap-audience` (with `-ui-iap-issuer` and `-ui-iap-keys-url` for a non-IAP
  issuer). This matches the log line above exactly.
- **The tunnel sends only the header.** Users sign in, or the operator sets `-ui-trust-identity-headers`.

The `ab2c91f9d` operator notes say the same, but they exist only as a commit message.

During the browser walk, the first daemon also showed "This deployment's reconcile loop has stopped". That
was an artefact of running two daemons on one host: the second could not bind the git proxy's `:9728`. It was
not a finding.

## 3. Sign in with Google

```
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/authentication-settings.spec.js --project=chromium
```

This ran 3 tests, which all passed. The existing test (grain/task-1942) already covered most of what the plan
asks:
- With stub client credentials and an allowed domain, a stranger's dialog shows a "Sign in with Google" link
  to `/api/auth/google/login`.
- That link's 302 goes to `https://accounts.google.com/…` with the client ID and without the secret.
- The link is absent when the feature is off, and absent while the secret is still missing.

This run adds one step. The browser loads that 302's Location under the `accounts.google.com` stub. It
asserts the stub page renders with `client_id` set to the stub ID, `redirect_uri` set to
`<origin>/api/auth/google/callback`, `response_type=code`, and a 32-hex-digit `state`.

**A pitfall for anyone extending this.** The first version clicked the link. `page.route` sees only the first
URL of a redirect chain, so the hop to `accounts.google.com` escaped the stub. One request with the fake
client ID reached the real Google, which answered with its 404 page. The step now loads the Location
directly. A `route.fetch({maxRedirects: 0})` passthrough on the login URL was also tried, and it hung the
test.

## 4. The relay client behind an IAP stand-in

The real `grain-relay -port 18602 -token relaysecret-walk` ran behind the `iapproxy` stand-in (stubs 3 and 4).
The daemon was started with:

```
GCE_METADATA_HOST=127.0.0.1:18600 /tmp/grain daemon … -relay-url http://127.0.0.1:18601 [-relay-token relaysecret-walk] [-relay-audience walk-relay-aud]
```

| Credentials | IAP stand-in | grain-relay | Daemon |
| --- | --- | --- | --- |
| Both | `PASSED (credential in Proxy-Authorization)`. Authorization and the `X-Grain-Daemon-Token`, `X-Grain-Tunnel-Token`, `X-Grain-Daemon-Id` and `X-Grain-Relay-Stream-Types` headers were forwarded | `daemon reverse tunnel established … (id: default)` | `established reverse tunnel to relay`. `GET /api/config` through the relay returned 200 |
| Relay token only | `REFUSED … no Proxy-Authorization bearer` | Never reached | `relay rejected upgrade with status 401: IAP: missing credential`, retried with backoff |
| IAP credential only (`-relay-audience`) | `PASSED (credential in Authorization)`. Alone, the ID token goes in `Authorization` | `daemon tunnel auth failed: invalid authentication token` | `status 401: Unauthorized`, retried |

This matches `f0b2d78c2`. One detail: with no relay token, the client also puts the Google ID token in the
`X-Grain-*` headers, which is the documented single-credential behaviour.

`grain-relay` refuses to start with only `-daemon-token`, because executor auth must also be set, so `-token`
was used.

## 5. The relay client at a tunnel gateway

`/tmp/grain-tunnel-gateway -port 18610 -token gwsecret-walk` ran against a daemon started with
`-tunnel-gateway-url http://127.0.0.1:18610 -tunnel-token gwsecret-walk`. Each run was watched for 75 s.

| | Before the fix (`0c790498f`) | After (this branch) |
| --- | --- | --- |
| Only `-tunnel-gateway-url` | **8 retries**: `failed to connect to relay …: relay rejected upgrade with status 200: <!doctype html>…` at 0.5, 1, 2, 4, 8, 16 and 30 s, and onward. See finding 1 | `relay … has no relay endpoint (no relay route: … status 200 …); not retrying` and `grain daemon: -tunnel-gateway-url … serves no relay; the relay client is off (set -relay-url to run one)`. 0 retries. The plan asks for one line: this is one line from the client and one from the daemon |
| Explicit `-relay-url` (same URL) | Retries | One 503 from before the tunnel connected, then retries of the 200 at 0.5 to 30 s backoff, still going. That is correct, because an explicit relay URL is trusted to be a relay |

## Findings

### 1. With a built UI, the relay client never stops at a tunnel gateway (fixed)

`4a37b3cd0` stops a relay client whose URL was defaulted from `-tunnel-gateway-url`, but only when the upgrade
is answered 404. A real `grain-tunnel-gateway` hands the `/relay/daemon` upgrade through its tunnel to the
daemon's own UI. With the bundle built, as it is in every release, the UI's single-page fallback answers
**200 with `index.html`**, so the 404 test never matched.

The daemon retried every 30 s for as long as it ran, and each retry logged the whole `index.html` into one
log line. The 2026-10-04 security-boundaries walk (its finding 4) saw a 404, most likely because its daemon
had no bundle built. `TestDaemonRelayDefaultedFromTunnelGatewayStops` used `http.NotFoundHandler()` for the
UI, so it modelled the same case.

**Reproduction.** Build the UI, then `go build ./cmd/grain ./cmd/grain-tunnel-gateway`. Run
`grain-tunnel-gateway -port 18610 -token s` and `grain daemon -disable-native-sandboxes -data-dir <tmp>
-tunnel-gateway-url http://127.0.0.1:18610 -tunnel-token s`. Count `failed to connect to relay` over 75 s:
the result is 8 and still growing.

**Fix.**
- `pkg/relay/client.go`: a 2xx answer to the upgrade now also counts as "not a relay" (`errUpgradeNotFound`).
  A relay only switches protocols or refuses.
- The refused body kept for the error and the log is capped at 512 bytes.
- An explicit `-relay-url` still retries both.

**Regression test.** `TestDaemonRelayDefaultedFromTunnelGatewayStops` now runs against a 404 UI and against
a UI that serves a page. The page case fails without the fix ("still retrying the tunnel gateway after 10s
… status 200: <!doctype html>") and passes with it.

### 2. An operator who relied on the plaintext header gets a 5-second explanation and no log line (follow-up)

After upgrading, a BeyondCorp-tunnel user whose tunnel sends only `X-Goog-Authenticated-User-Email` sees the
log-in dialog, not the AccessDenied page. The only statement of why is the 401's text, shown once as a toast
that disappears after 5 seconds. The daemon logs nothing, as `docs/beyondcorp.md` itself says ("nothing is
logged").

So neither the person in front of the browser nor the operator reading `journalctl` has a lasting record that
the deployment ignored an identity header and which flag decides it. Compare the signed-assertion path, which
logs once per issuer and audience pair.

**Reproduction.** Start `grain daemon -disable-native-sandboxes -data-dir <tmp> -ui-addr 127.0.0.1:18431`, with
no identity flags. Open it in Chromium with `extraHTTPHeaders: {"X-Goog-Authenticated-User-Email":
"accounts.google.com:dharb@google.com"}`. The toast is gone after 5 s, and `grep -i "unsigned\|identity
header" <daemon log>` is empty.

This was not fixed here. It needs:
- a log policy decision: once per header name, or per identity, and how to rate-limit something any caller
  can trigger;
- a UI decision about where a persistent explanation belongs (the sign-in dialog);
- a docs update.

All of that is in `pkg/ui` auth, which is held for security review. Filed as a follow-up task.

### 3. Observations, not filed

- The banner now appears on the first unreachable poll, not the second (see §1 above).
- `grain demo` has no fixed data dir (see §1 above).

## Fixes and tests in this change

| File | What |
| --- | --- |
| `ui/e2e/disconnection.spec.js` | Kill and restart a real demo under the task list and under the session pane, with and without reduced motion: exactly one `role=status` banner, no toast, no "Failed to fetch", clears with no reload, and the list is re-read. A 503 from a gateway gives the 503 wording, and a 500 gives no banner |
| `ui/e2e/authentication-settings.spec.js` | The Google login redirect lands on a stubbed `accounts.google.com` with the right `client_id`, `redirect_uri`, `response_type` and `state` |
| `tests/security/signed_assertion_test.go` | Real ES256 assertions and a JWKS, a real `ui.Server` over loopback HTTP: one valid case, seven invalid ones, header-only, and no audience |
| `pkg/relay/client.go`, `cmd/grain/daemon_relay_test.go` | Finding 1's fix and its regression test |

Formatting and suites:
- `npm run format` in `ui/` and `gofmt -l` were clean.
- `make security` (`go vet ./...` and `go test -race -count=1 ./tests/security/...`) passed (133 s).
- `go test ./pkg/relay/ ./cmd/grain/ -run TestDaemonRelay` passed.

The fixes in `pkg/relay` and `tests/security` are expected to be held for security review.

## CI verdict

The full grain CI suite (`wait_for_checks` with no job filter) ran as run `67728aedc652cc32` on `4b94a34`,
this branch's last code commit. It passed. All eight jobs succeeded: `frontend` (5m30s), `go` (16m37s),
`installer`, `lint`, `package cache`, `postgres`, `terraform` and `ui e2e` (12m4s). None of the known flakes
recurred. The commit that adds this paragraph changes only this report, and its own full-suite verdict is
given in the task's closing note.
