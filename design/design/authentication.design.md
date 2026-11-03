# Authentication behind IAP

**Owner:** @bwsalmon

**Specified by:** [`authentication.spec.md`](../spec/authentication/authentication.spec.md)

**The core rule:** A cryptographic assertion verified by a registered `IdentityVerifier` authenticates the caller directly as a named human or service principal; a present assertion that fails verification refuses the request with `401` and never falls back to the access code, plaintext identity headers name nobody, and deployments with no active verifier keep the single-tenant access code unchanged.

A grain controller on GKE sits behind Google Cloud Identity-Aware Proxy ([`grain-on-gke.design.md`](grain-on-gke.design.md), *UI and API ingress*). When IAP is configured (`-ui-iap-audience` / `$GRAIN_IAP_AUDIENCE`), `singleTenantGate` (`pkg/ui/accesscode.go`) admits verified, allowlisted callers directly from `X-Goog-IAP-JWT-Assertion` as named principals—for the browser UI, the `grain` CLI, external agents, and remote `grain hostd` instances—without a shared access code. Wrapper distributions fronted by a different signed-header proxy plug into the same gate through `ui.RegisterIdentityVerifier` (`pkg/ui/verifier.go`). Companion designs cover how verified principals are recorded on mutations ([`operator-attribution.design.md`](operator-attribution.design.md)), how multi-instance registry proxies sign assertions for replaceable VMs ([`registry.design.md`](registry.design.md)), and how fine-grained authorization roles layer on top (`rbac.design.md`, task 2764).

## 1. Verified assertions and the `IdentityVerifier` seam

### `IdentityVerifier` (`pkg/ui/verifier.go`)

IAP is the built-in implementation of `ui.IdentityVerifier`, the extension seam through which core and wrapper distributions verify proxy-signed identity headers:

```go
// An IdentityVerifier turns a signed header a trusted proxy adds into a verified identity.
type IdentityVerifier interface {
	Name() string      // "iap", or a wrapper's proxy; becomes AuthMethod and Principal.Source
	Headers() []string // headers read by Verify; also treated as proxy headers in relayedByProxy
	Verify(r *http.Request) (id VerifiedIdentity, ok bool, err error)
}

// AccessCodePrecedence may be implemented by an IdentityVerifier whose proxy
// attaches a signed header to every proxied request, so a valid access code
// still works as break-glass recovery. Verifiers that do not implement it
// default to false (the verified identity takes precedence over the code).
type AccessCodePrecedence interface {
	AccessCodeWins() bool
}

type VerifiedIdentity struct {
	Email string              // normalized lower-case email; becomes Principal.ID
	Kind  model.PrincipalKind // model.PrincipalHuman or model.PrincipalService
}

func RegisterIdentityVerifier(v IdentityVerifier) (unregister func())
```

`Server.activeVerifiers` (`pkg/ui/verifier.go`) places `NewIAPVerifier` (`Name() == "iap"`, `Headers() == ["X-Goog-IAP-JWT-Assertion"]`) first whenever `IAPConfig.Enabled()` is true, followed by verifiers registered via `RegisterIdentityVerifier` in registration order.

`Server.verifyCallerNow` (`pkg/ui/server.go`, `pkg/ui/verifier.go`) runs once per HTTP request before external-agent token authentication and `singleTenantGate`, caching its `callerVerification` in the request context:

- **First present header decides.** `verifyCaller` inspects active verifiers in order. Verifiers with none of their `Headers()` set on the request are skipped. The first verifier with a non-empty header decides the request; later verifiers are never consulted, even on error.
- **A bad assertion fails closed (`401`).** When a verifier's header is present and `Verify` returns an error, the request is refused with `401` (`writeAssertionRefused` in `pkg/ui/principalrefusal.go`). It never falls back to the access code, to a `gma_…` external-agent token, to a session cookie, or to an unauthenticated open route (including `/api/hostd/connect`). A caller that reaches the pod directly past the Gateway cannot forge the signature, so network topology is not trusted on its own.
- **Bounded diagnostic logging (`pkg/ui/assertion_diagnostics.go`).** Refused IAP assertions are logged by `logRefusedAssertion` with the unverified `iss`, unverified `aud`, configured audience(s), and failure reason, deduplicated by `(iss, aud, refusalKind)` across nine failure categories (`malformed`, `signature`, `unverifiable`, `expired`, `not-yet-valid`, `issued-in-future`, `issuer`, `claim-missing`, `other`) up to 64 distinct keys (`maxLoggedRefusedAssertions`). Raw tokens and signatures are never logged. If `X-Goog-IAP-JWT-Assertion` arrives when no IAP audience is configured, grain logs a one-time configuration hint naming `-ui-iap-audience`. Non-IAP verifier failures log once per `(verifier.Name(), header)` via `logRefusedVerifierHeader` (also capped at 64 entries).
- **Plaintext identity headers never authenticate.** Unsigned headers (`PlaintextIdentityHeaders` in `pkg/ui/principal.go`: `X-Goog-Authenticated-User-Email`, `X-User-LDAP`, `X-Forwarded-User`, `X-Goog-Authenticated-User-Id`, `X-Grain-User`, `Grain-User`) never establish a principal or satisfy `singleTenantGate`, whether from the network or loopback. Proxy-sent headers outside grain's own client headers (`X-Grain-User`, `Grain-User`, `X-Forwarded-User`) are logged once per header name (`logIgnoredIdentityHeader`) and ignored.

### IAP JWT checks (`pkg/ui/principal.go`)

`IAPConfig` configures `NewIAPVerifier` from daemon flags (`pkg/cli/daemon.go`) and environment fallbacks:

- **Audience:** `-ui-iap-audience` (`$GRAIN_IAP_AUDIENCE`, falling back to `$GRAIN_AUDIENCE` and `$IAP_AUDIENCE`), accepting one or more comma-separated expected audiences (for example `/projects/<number>/global/backendServices/<id>`).
- **Issuer:** `-ui-iap-issuer` (`$GRAIN_IAP_ISSUER`), defaulting to `https://cloud.google.com/iap`.
- **Public keys:** `-ui-iap-keys-url` (`$GRAIN_IAP_KEYS_URL`), defaulting to `https://www.gstatic.com/iap/verify/public_key` (or overridden in tests via `IAPConfig.Validator`). `fetchGoogleIAPKeys` accepts both IAP's `{kid: pem}` JSON map and RFC 7517 JWKS (`{"keys": […]}`), supporting ECDSA P-256 (`ES256`) and RSA (`RS256`) keys with a `5s` HTTP timeout, a `1h` cache TTL (`iapKeysTTL`), a `10s` minimum refetch interval (`iapKeysRefetchFloor`) when an unknown `kid` arrives, and stale-cache fallback if a background refresh fails.
- **Claims and skew:** `verifyIAPAssertion` pins the JWT signing algorithm to the matched key's type (`ES256` for ECDSA, `RS256` for RSA—never `none` or HMAC), checks exact `iss` equality, requires `aud` to intersect the configured audiences, requires `exp`, and validates `exp`, `iat`, and `nbf` with `30s` clock-skew leeway (`iapClockSkew`).

## 2. Principals, attribution, and UI session state

`verifyIAPAssertion` extracts the `email` claim (falling back to `sub` when `email` is empty) and normalizes it with `normalizeIdentityEmail` (`pkg/ui/principal.go`): stripping `https://accounts.google.com:` or `accounts.google.com:`, trimming whitespace, and lower-casing. An assertion with no resulting email is rejected (`401`).

`verifiedIdentityForEmail` (`pkg/ui/verifier.go`) classifies the verified address into `model.Principal` (`pkg/model/task.go`):

- `{Kind: model.PrincipalService, ID: "<sa>@<proj>.iam.gserviceaccount.com", Source: "<verifier>"}` when the address ends in `.gserviceaccount.com` (`isServiceAccountEmail`);
- `{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: "<verifier>"}` for every other address.

`Principal.Source` (`model.AuthSource`) records `"iap"` for IAP or `verifier.Name()` for a registered verifier, distinguishing cryptographic sign-ins (`Principal.Verified() == true`) from `"access-code"`, `"token"`, `"loopback"`, `"in-process"`, `"session"`, `"sandbox"`, `"external-token"`, and `"internal"`.

`extractPrincipal` (`pkg/ui/principal.go`) and `Server.serveHTTP` (`pkg/ui/server.go`) attach this `model.Principal` to the request context. Every mutating UI and API handler reads the actor through `ui.RequestActor(ctx)` (`pkg/ui/request_actor.go`, enforced across routes by `TestEveryMutatingRouteIsAttributed`), recording `(kind, id, source)` on task filings, approvals, comments, state transitions, merge-queue actions, settings writes, and audit logs ([`operator-attribution.design.md`](operator-attribution.design.md)). Unverified access-code and loopback callers continue to record `{human, operator, access-code}` (or `loopback`), rendered by `ui.PrincipalLabel` (`pkg/ui/principal_label.go`) as `operator (access code)`.

In the UI:

- `GET /api/config` (`pkg/ui/server.go`) and `GET /api/auth/user` (`pkg/ui/auth.go`, `AuthUserResponse`) return `authMethod` (`"iap"` or verifier name) and `user`/`actor` (`user:<email>` for humans, `service:<email>` for service accounts).
- At desktop widths, `PageHeader.jsx` (`ui/src/components/PageHeader.jsx`) shows the signed-in email badge (`top-bar-user`) with a menu linking to `Settings → Access` (`ui/src/components/settings/AccessSection.jsx`) and `Log out`. At phone widths (`390 px`), `Sidebar.jsx` (`ui/src/components/Sidebar.jsx`) renders `"Signed in as <email>"` and `Log out` in the pinned drawer footer.
- `POST /api/auth/logout` (`pkg/ui/accesscode.go`) clears any `grain_access` cookie and, when `identifyCaller` reports `AuthMethodIAP`, returns `{"loggedOut": true, "redirect": "/_gcp_iap/clear_login_cookie"}` so the browser clears IAP's login cookie.

## 3. Admission: the allowlist, groups, and roles

IAP's IAM binding (`pkg/iapreconciler`) is the perimeter. Grain enforces an application-level allowlist (`pkg/ui/allowlist.go`, `pkg/daemon/ui_server.go`) on every verified principal so an overly broad IAM binding on the GCP project does not grant deployment access to an entire organisation:

```
-ui-allowed-principals=user:alice@example.com,group:grain-admins@example.com,serviceAccount:hostd@proj.iam.gserviceaccount.com,domain:example.com
```

The list is configured via `-ui-allowed-principals` or `$GRAIN_ALLOWED_PRINCIPALS` and validated at daemon startup by `ui.ValidateAllowedPrincipals` and `daemon.CheckUIAllowedPrincipals`:

- **Entry kinds (`pkg/ui/allowlist.go`):**
  - `user:<email>` — matches `model.PrincipalHuman` only (case-insensitive exact email; `.gserviceaccount.com` addresses are rejected at startup).
  - `serviceAccount:<email>` — matches `model.PrincipalService` only (must end in `.gserviceaccount.com`).
  - `domain:<domain>` — matches `model.PrincipalHuman` whose exact domain after `@` equals `<domain>` (never matches subdomains or `model.PrincipalService`).
  - `group:<group-email>` — matches human or service principals that are transitive members of `<group-email>` in Cloud Identity.
- **Default allowlist.** When `-ui-allowed-principals` is unset or empty, grain admits only the deployment owner (`Config.Actor` / `-as`, when it contains `@`) plus the daemon's own service accounts (`Config.ProvisionedServiceAccounts` and `grain-daemon@<gcp-project>.iam.gserviceaccount.com`).
- **Sandbox `gcp-key` pool exclusion.** Service accounts belonging to the `gcp-key` sandbox credential pool (`GCPKeyPoolEmails`: the configured base account and numbered `<prefix>-1@…` through `<prefix>-N@…` pool accounts whose keys are placed inside task sandboxes) are never included in the default allowlist, cause daemon startup to fail if listed in `-ui-allowed-principals`, and are unconditionally refused (`403`) at request time even if matched through a `group:` entry.
- **Decision — Group resolution (`CloudIdentityGroupChecker`).** IAP assertions carry no group claims. Grain calls the Cloud Identity Groups API (`groups:lookup` followed by `memberships:checkTransitiveMembership`, OAuth scope `https://www.googleapis.com/auth/cloud-identity.groups.readonly`) **only** when at least one `group:` entry is configured; deployments without `group:` entries never contact Cloud Identity and need no Cloud Identity IAM permission on `grain-daemon@…`. Direct entries (`user:`, `serviceAccount:`, `domain:`) are checked first without network I/O, so a Cloud Identity outage affects only callers relying on `group:` membership. Positive and negative membership answers are cached for `5m` (`defaultGroupCacheTTL`); lookup errors are never cached and fail closed (`403`), with `10s` bounds on both OAuth token headers and API calls (`cloudIdentityCallTimeout`).
- **Decision — Roles and token minting (until RBAC lands).** Grain is single-tenant ([`docs/access-code.md`](../../docs/access-code.md)): every admitted principal has full operator powers, with one guard on external-agent credential minting (`pkg/ui/external_agents.go`). Minting or rotating a `gma_…` credential (`POST /api/external-agents/tokens`, `POST /api/external-agents/tokens/{id}/rotate`) that grants a dangerous scope (`model.IsDangerousScope`) or a `requires_person` scope set requires either a `human` principal or a `service` principal **explicitly** listed as `serviceAccount:<email>` in `-ui-allowed-principals` (`isExplicitlyAllowedServiceAccount`); a service account admitted only through the default allowlist—or an in-grain task agent—is refused with `403`. Admitted principals may register `hostd` rows (`POST /api/hostds`) and mint non-dangerous `gma_…` tokens. Finer-grained roles (such as read-only viewers or separating admin from operator permissions) are deferred to `rbac.design.md` (task 2764).

## 4. Access-code modes, precedence, CSRF, and refusal pages

### Three access modes (`pkg/ui/accesscode.go`, [`docs/access-code.md`](../../docs/access-code.md))

1. **No verifier active (standalone / local default).** `local-api-token` (`$GRAIN_ACCESS_CODE`) is the sole credential: browsers exchange it at `POST /api/auth/access-code` for the `grain_access` cookie (`HttpOnly`, `SameSite=Strict`), and the CLI sends it as `Authorization: Bearer`.
2. **Migration mode (`-ui-iap-audience` set, `-ui-require-iap` unset).** Verified, allowlisted IAP callers pass `singleTenantGate` without the access code; requests carrying no assertion may still authenticate with the access code.
3. **IAP required (`-ui-require-iap` / `$GRAIN_REQUIRE_IAP`).** `daemon.CheckUIRequireIAP` (`pkg/cli/daemon.go`) refuses to start if `-ui-require-iap` is set without an IAP audience. Over the network—and over loopback whenever any proxy or verifier header is present (`relayedByProxy` in `pkg/ui/accesscode.go`)—neither the bearer access code, nor `grain_access`, nor `POST /api/auth/access-code` is accepted (`accessCodeAccepted` returns `false`). Over **unproxied loopback** (`127.0.0.0/8`, `::1`, or Unix socket with no forwarding headers), the access code remains accepted so `kubectl exec deploy/grain -- grain …` always works as break-glass recovery.

### Per-verifier access-code precedence

- **Verified identity wins by default (`AccessCodeWins() == false`, including IAP).** When a request carries both an IAP assertion and a bearer access code or `grain_access` cookie, the assertion governs: the caller must pass `-ui-allowed-principals` and `checkVerifiedIdentityCSRF`, and acts as their named email rather than anonymous `operator`.
- **Access code wins for always-attached wrapper proxies (`AccessCodeWins() == true`).** A wrapper distribution whose corporate proxy attaches a signed identity header to every proxied request may return `AccessCodeWins() == true`. When such a request also carries a valid access code (or a valid `gma_…` token), `singleTenantGate` and `accessCodeAccepted` honour that credential—even under `-ui-require-iap`—so an operator can still break glass through the corporate proxy when the allowlist or group directory is broken. A present header that fails cryptographic verification still returns `401` with no fallback.

### CSRF protection (`pkg/ui/csrf.go`)

IAP authenticates browser requests with IAP's own cookie rather than grain's `SameSite=Strict` `grain_access` cookie. Grain enforces two layers of cross-origin protection:

1. `ServeHTTP` runs Go's `http.CrossOriginProtection` (`checkCrossOrigin`) across all routes (`GET`, `HEAD`, and `OPTIONS` allowed; browser cross-site requests rejected via `Sec-Fetch-Site` and `Origin`).
2. `singleTenantGate` runs `checkVerifiedIdentityCSRF` on every request admitted through a verified identity whose HTTP method is neither `GET` nor `HEAD`:
   - `Sec-Fetch-Site: same-origin` or `Sec-Fetch-Site: none` is allowed; any other non-empty `Sec-Fetch-Site` is refused with `403`.
   - When `Sec-Fetch-Site` is absent, `Origin` is parsed and its `host[:port]` is compared case-insensitively against `r.Host` and comma-separated `X-Forwarded-Host` values (`403` on mismatch or malformed `Origin`).
   - When both `Sec-Fetch-Site` and `Origin` are absent, requests carrying a non-empty `Cookie` header are refused with `403`, while cookie-less programmatic requests (`Authorization: Bearer`, `X-Grain-Hostd-Token`) pass.

### Refusal pages (`pkg/ui/principalrefusal.go`, `pkg/ui/accesscode.go`)

Browser navigations (`GET` or `HEAD` outside `/api/`) receive self-contained HTML pages (`Cache-Control: no-store`), while `/api/*` and non-`GET`/`HEAD` requests receive JSON errors:

- **`403` `"Not allowed on this grain"` (`writePrincipalRefusedPage`).** Rendered when a caller's assertion verifies (`cv.err == nil`) but `checkAllowedPrincipal` refuses admission. The page displays the verified email and verifier name, any underlying lookup error (for example a Cloud Identity timeout), remediation guidance from `ui.RegisterPrincipalAdvice` (defaulting to the exact `user:<email>` or `serviceAccount:<email>` entry and a `grain init -daemon-arg -ui-allowed-principals='…'` command that preserves existing entries when safe), and an `"Owner: sign in with the access code"` section that renders `<form method="post" action="/api/auth/access-code">` **only** when `AccessCodeWins()` is true—otherwise explaining that the verifier's identity takes precedence over the access code and instructing the owner to connect directly or over an SSH tunnel.
- **`401` `"Sign-in could not be verified"` (`writeAssertionRefused`).** Rendered when a verifier header is present on a browser navigation but fails verification, naming the failure reason and configuration/clock checklist with no access-code fallback.
- **`401` `"Sign in through IAP"` (`writeIAPRequiredPage`).** Rendered when `-ui-require-iap` refuses an unauthenticated network browser navigation, in place of the `"Enter access code"` login form.

## 5. How each client signs in

### Browser UI

Opening the deployment URL signs the user in through IAP's Google login flow. Every proxied request carries `X-Goog-IAP-JWT-Assertion`, bypassing the access-code page without setting `grain_access`. Same-origin browser `fetch` calls carry `Sec-Fetch-Site: same-origin` and pass `checkVerifiedIdentityCSRF` unchanged. Clicking `Log out` posts `/api/auth/logout` and follows the returned redirect to `/_gcp_iap/clear_login_cookie`.

### `grain` CLI (`pkg/cli/cliconn.go`, `pkg/cli/accesscode.go`, `pkg/ui/httpclient.go`)

The CLI loads `GRAIN_SERVER` and `GRAIN_DATA_DIR` from `/etc/grain/cli.env` when unset in the environment. `ui.HTTPClient` selects a bearer token from the first available source:

1. Explicit `-token` (`HTTPClient.Token`) or `HTTPClient.TokenProvider`;
2. Environment variables `GRAIN_TOKEN`, `GRAIN_AUTH_TOKEN`, `GRAIN_ID_TOKEN`, or `GRAIN_TUNNEL_TOKEN`;
3. Local `<data-dir>/local-api-token` when `BaseURL` targets a loopback host (`127.0.0.1`, `::1`, `localhost`);
4. Google OIDC ID token resolution (`getGoogleIDToken`, cached in memory until `1m` before `exp`) when `BaseURL` uses `https://`, ends in `.run.app`, or an explicit `-audience` (`$GRAIN_AUDIENCE` / `$GRAIN_TUNNEL_AUDIENCE`) is set:
   - Application Default Credentials (`idtoken.NewTokenSource(ctx, audience)`, where `audience` defaults to `BaseURL`);
   - `gcloud auth print-identity-token --audiences=<audience>` (for service accounts or impersonated service accounts);
   - bare `gcloud auth print-identity-token` **only** when the target host ends in `.run.app`.

`HTTPClient` sends the token in `Authorization: Bearer <token>` (and `X-Grain-Tunnel-Token`). Behind IAP, IAP validates the Google ID token at the edge and attaches `X-Goog-IAP-JWT-Assertion`; grain authenticates the caller from `X-Goog-IAP-JWT-Assertion` without needing `local-api-token`. Running `grain access-code` prints the local token and checks `GET /api/auth/access-code`, warning on stderr when the server reports `requireIAP: true`.

### External agents (`pkg/ui/external_agents.go`)

An external agent behind IAP can authenticate in either of two ways:

- **Directly as a Google service account:** the agent sends an IAP-audience ID token for its service account, IAP attaches `X-Goog-IAP-JWT-Assertion`, and grain admits it as `{service, <sa>@<proj>.iam.gserviceaccount.com, iap}` when allowed by `-ui-allowed-principals`.
- **With a scoped `gma_…` credential:** the agent presents `Authorization: Bearer gma_…` (and, when traversing IAP, sends its Google OIDC ID token in `Proxy-Authorization: Bearer <id-token>` so `Authorization` reaches grain intact). `verifyCallerNow` still validates any present proxy assertion (`401` on invalid signature) and, for verifiers where `AccessCodeWins() == false` (including IAP), `singleTenantGate` still checks `-ui-allowed-principals` on the proxy identity (`403` on disallow). `authenticateExternalAgent` sets the request's principal to `{agent, external:<name>, external-token}`, enforcing the credential's scopes, repository list, rate limit, and concurrency cap.

### Decision — `grain hostd` and owner matching (`pkg/hostd/`, `pkg/ui/hostd_connect.go`, `pkg/ui/hostds.go`)

Operators register per-hostd credentials in the hostd registry (`POST /api/hostds`, `grain hostds register --owner <email>`, `grain hostds set-owner`, `pkg/ui/hostds.go`, `pkg/cli/hostds.go`), receiving a `ghd_…` token bound to that hostd row and its `owner` email (human or service account). A deprecated single-file fallback (`-hostd-token-file` paired with `-hostd-token-owner` / `$GRAIN_HOSTD_TOKEN_OWNER`) remains supported for legacy setups. Remote model executors (`pkg/modelexec`, `executorBootstrapRoute`) were removed (`docs/executors-removed.md`).

`grain hostd` dials `GET /api/hostd/connect` (`pkg/hostd/dialout.go`) sending its hostd token in `X-Grain-Hostd-Token` (plus optional `X-Grain-Hostd-Name`). When `-iap-audience` (`$GRAIN_IAP_AUDIENCE`) is set on `grain hostd`, `hostd.NewIAPTokenProvider` (`pkg/hostd/iap_auth.go`) mints and caches an OIDC ID token from the GCE metadata server (`…/instance/service-accounts/default/identity?audience=<aud>&format=full`) with fallback to Application Default Credentials (`idtoken.NewTokenSource`), sending `Authorization: Bearer <id-token>` for IAP while `X-Grain-Hostd-Token` carries the hostd credential.

Although `/api/hostd/connect` is in `singleTenantOpenRoute` (requiring no deployment access code), `serveHTTP` runs `verifyCallerNow` first (`401` on bad assertion) and `hostdConnectIdentity` (`pkg/ui/hostd_connect.go`) enforces `checkAllowedPrincipal` (`403` on disallow) whenever a verifier header is present:

- **Local `hostd` (`Remote: false`):** Only connections with no verifier header arriving either when no verifiers are active or over unproxied loopback (`isLoopbackRequest && !relayedByProxy`)—plus the local Unix socket `/run/grain-hostd/hostd.sock`—are treated as local and authenticate by hostd token alone.
- **Remote `hostd` (`Remote: true`):** Every connection carrying a verifier header, arriving from the network when any verifier is active, or carrying proxy forwarding headers is remote. `hostd.Remote.ServeHTTP` (`pkg/hostd/remote.go`, `pkg/hostd/connect_identity.go`) refuses with `401` if `Principal == ""` (preventing direct-to-pod bypass when IAP is configured) and refuses with `403` unless the hostd's configured `owner` is non-empty and matches `Principal` case-insensitively (`strings.EqualFold`). A stolen `ghd_…` token therefore cannot connect without its owner's verified identity.

### In-sandbox calls

Agent calls from inside a task sandbox never traverse IAP and never carry a person's identity. Both the sandbox git/API proxy (`127.0.0.1:9728`) and `grain mcpserver` (`X-Grain-Acting-Task`) run off the Gateway using per-attempt scoped sandbox tokens, and proxy assertion headers never authenticate them.

## 6. Rollout status

### Done

- **`IdentityVerifier` seam and `NewIAPVerifier`** (`pkg/ui/verifier.go`, `pkg/ui/principal.go`, `pkg/ui/assertion_diagnostics.go`): verifier registry, per-verifier `AccessCodePrecedence`, ECDSA/RSA key fetching, `30s` clock skew, fail-closed `401`, and bounded deduplicated assertion logging (tasks 2309, 2443).
- **Admission, CSRF, UI identity, and refusal pages** (`pkg/ui/allowlist.go`, `pkg/ui/csrf.go`, `pkg/ui/principalrefusal.go`, `pkg/ui/accesscode.go`, `pkg/daemon/ui_server.go`): `PrincipalHuman` and `PrincipalService`, `-ui-allowed-principals` with optional Cloud Identity `group:` lookup and `gcp-key` sandbox pool exclusion, `checkVerifiedIdentityCSRF`, HTML refusal pages (`403` principal refused with `RegisterPrincipalAdvice`, `401` assertion refused, `401` IAP required), desktop and phone signed-in identity UI, and `/_gcp_iap/clear_login_cookie` logout (tasks 2309, 2383, 2443, 2510).
- **`-ui-require-iap` and loopback break-glass** (`pkg/ui/accesscode.go`, `pkg/cli/daemon.go`, `pkg/cli/accesscode.go`, [`docs/access-code.md`](../../docs/access-code.md)): network access-code disablement, startup audience check, and unproxied loopback break-glass (task 2318).
- **Per-hostd registry and remote `hostd` owner verification** (`pkg/ui/hostds.go`, `pkg/ui/hostd_connect.go`, `pkg/hostd/connect_identity.go`, `pkg/hostd/remote.go`, `pkg/hostd/iap_auth.go`, `pkg/cli/hostds.go`): `/api/hostds`, `grain hostds`, `hostd.ConnectIdentity`, and `-hostd-token-owner` (tasks 2164, 2316).
- **Operator attribution foundation** (`pkg/model/task.go`, `pkg/ui/request_actor.go`, `pkg/ui/principal_label.go`): `Principal.Source`, `Principal.Verified()`, `ui.RequestActor(ctx)`, and `*_source` database columns across mutating routes (tasks 2310, 2311).
- **Simulated IAP end-to-end verification:** 112 checks passed, 0 failed across browser UI, CLI, external agents, and remote `hostd` (`docs/test-runs/2026-11-02-iap-end-to-end.md`, task 2510).

### Remaining

- **Dedicated CLI IAP ergonomics:** teaching `ui.HTTPClient` and the `grain` CLI to send the IAP OIDC token in `Proxy-Authorization: Bearer` (freeing `Authorization` when pairing IAP with a `gma_…` token), reading `GRAIN_IAP_AUDIENCE` from `/etc/grain/cli.env`, and adding a user OAuth desktop/device flow when `gcloud` cannot mint an audience-bound ID token without service-account impersonation.
- **Live GKE Gateway + Cloud IAP rollout check:** verifying a live cluster deployment behind Google's managed IAP edge alongside the simulated proxy suite.
- **Remaining operator-attribution items** ([`operator-attribution.design.md`](operator-attribution.design.md)): `GET /api/whoami` (`OA-5`) and `OnBehalfOf` dual-identity recording when an external or in-grain agent acts on a verified person's behalf (`OA-13`).
- **Registry verifier and generalised verifier requirement** ([`registry.design.md`](registry.design.md)): core `registry` `IdentityVerifier` (`X-Grain-Registry-Assertion`) and generalising `-ui-require-iap` to `-ui-require-verifier`.
- **Role-based access control** (`rbac.design.md`, task 2764): roles and scoped permissions beyond single-tenant operator equivalence.
