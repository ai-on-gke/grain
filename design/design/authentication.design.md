# Authentication behind IAP

**Owner:** @bwsalmon

**Specified by:** [`authentication.spec.md`](../spec/authentication/authentication.spec.md)

**The core rule:** A cryptographic assertion verified by a registered `IdentityVerifier` authenticates the caller directly as a named human or service principal; a present assertion that fails verification refuses the request with `401` and never falls back to the access code, plaintext identity headers name nobody, and deployments with no active verifier keep the single-tenant access code unchanged.

A grain controller on GKE sits behind Google Cloud Identity-Aware Proxy ([`grain-on-gke.design.md`](grain-on-gke.design.md), *UI and API ingress*). When IAP is configured (`-ui-iap-audience` / `$GRAIN_IAP_AUDIENCE`), `singleTenantGate` (`pkg/ui/accesscode.go`) admits verified, allowlisted callers directly from `X-Goog-IAP-JWT-Assertion` as named principals—for the browser UI, the `grain` CLI, external agents, and remote `grain hostd` instances—without a shared access code. Wrapper distributions fronted by a different signed-header proxy plug into the same gate through `ui.RegisterIdentityVerifier` (`pkg/ui/verifier.go`). Companion designs cover how verified principals are recorded on mutations ([`operator-attribution.design.md`](operator-attribution.design.md)), how multi-instance registry proxies sign assertions for replaceable VMs, including delegated assertions in which an application acts for a signed-in person ([`registry.design.md`](registry.design.md); §6 here), how simplified configuration layers database-backed allowed users on top ([`simple-config.design.md`](simple-config.design.md)), and how fine-grained authorization roles (`admin`, `writer`, `reader`) layer on top (`rbac.design.md`, task 2764).

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
- **An unavailable verifier fails closed too, but as `503`.** When `Verify`'s error (anywhere in its wrapped chain) has `Unavailable() bool` returning true (`VerifierUnavailableError` in `pkg/ui/verifier.go`) -- a backend the verifier consults is not reachable yet, say just after a daemon restart -- the refusal is `503 Service Unavailable` with `Retry-After: 5`: a "sign-in verification is starting up" page for a browser navigation, JSON for everything else, and `503` from `hostdConnectIdentity`. Only the status and the page change; the access code and later verifiers are still not consulted.
- **Bounded diagnostic logging (`pkg/ui/assertion_diagnostics.go`).** Refused IAP assertions are logged by `logRefusedAssertion` with the unverified `iss`, unverified `aud`, configured audience(s), and failure reason, deduplicated by `(iss, aud, refusalKind)` across nine failure categories (`malformed`, `signature`, `unverifiable`, `expired`, `not-yet-valid`, `issued-in-future`, `issuer`, `claim-missing`, `other`) up to 64 distinct keys (`maxLoggedRefusedAssertions`). When the unverified `aud` already matches a configured audience (and the failure is not `issuer`), the log line notes that the audience is this deployment's own—pointing to clock skew, a replayed header, or an untrusted signer rather than telling the operator to change `-ui-iap-audience`. Raw tokens and signatures are never logged. If `X-Goog-IAP-JWT-Assertion` arrives when no IAP audience is configured, grain logs a one-time configuration hint naming `-ui-iap-audience`. Non-IAP verifier failures log once per `(verifier.Name(), header)` via `logRefusedVerifierHeader` (also capped at 64 entries).
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
- At desktop widths, the foot of `Sidebar.jsx` (`ui/src/components/Sidebar.jsx`) shows the user tile (`user-tile`, the verified email, opening `Settings → Access` in `ui/src/components/settings/AccessSection.jsx`) with a compact `Log out` button (`sidebar-logout`) beside it; the page header rail has no signed-in badge (settings-panel design §4.3). At phone widths (`390 px`), `Sidebar.jsx` (`ui/src/components/Sidebar.jsx`) renders `"Signed in as <email>"` and `Log out` in the pinned drawer footer.
- `POST /api/auth/logout` (`pkg/ui/accesscode.go`) clears any `grain_access` cookie and, when `identifyCaller` reports `AuthMethodIAP`, returns `{"loggedOut": true, "redirect": "/_gcp_iap/clear_login_cookie"}` so the browser clears IAP's login cookie.

## 3. Admission: the database allowlist, groups, and roles

IAP's IAM binding (`pkg/iapreconciler`) is the perimeter. Grain enforces an application-level **Allowed users and groups** list in the database (`allowed_principal` table: `entry`, `added_by`, `added_at`; `pkg/model/allowed_principals.go`, `pkg/ui/access_principals.go`, `pkg/ui/allowlist.go`) on every verified principal so an overly broad IAM binding on the GCP project does not grant deployment access to an entire organisation, and changes take effect immediately without restarting the daemon:

```
user:alice@example.com
group:grain-admins@example.com
```

- **Entry kinds and seeding (`pkg/model/allowed_principals.go`, `pkg/ui/allowlist.go`):**
  - `user:<email>` — matches `model.PrincipalHuman` only (case-insensitive exact email; `*.gserviceaccount.com` addresses are rejected).
  - `group:<group-email>` — matches `model.PrincipalHuman` callers who are transitive members of `<group-email>` in Cloud Identity (`*.gserviceaccount.com` addresses are rejected and `model.PrincipalService` callers never resolve through `group:` entries).
  - `serviceAccount:<sa-email>` — matches `model.PrincipalService` only, for `*.gserviceaccount.com` addresses. Added only explicitly (`POST /api/access/principals` / `grain access allow --reason`) with a required reason stored in `allowed_principal.reason` and `config_audit`; never imported from `-ui-allowed-principals`. Accounts in the `gcp-key` pool or the gcp capability's permission sets (`gcpKeyPoolSet`) are refused when added and at admission even if a row for one exists.
  - Daemon-provisioned service accounts (`Config.ProvisionedServiceAccounts` and `grain-daemon@<gcp-project>.iam.gserviceaccount.com`, excluding `gcp-key` sandbox pool accounts) remain admitted in memory (`serviceAccount:<email>`), and dedicated non-pool GCP service accounts registered as active `hostd` owners (`isRegisteredHostdServiceOwner`) are admitted on `GET /api/hostd/connect`.
  - Installers seed the installing operator with `grain access allow user:<email>` (idempotent). Keeping `allowed_principal` and `allowed_principal_migration` outside `staterepo.SettingsTables` preserves them in cold backups while preventing `grain/config` pull requests from locking operators out or granting sign-in access.
- **One-time migration off `-ui-allowed-principals` (`pkg/ui/access_principals.go`, `pkg/model/allowed_principals.go`).** If `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS` is set on start when `allowed_principal` is empty and has not yet migrated (`allowed_principal_migration`), grain imports its `user:` and `group:` entries once (excluding `*.gserviceaccount.com` and `gcp-key` pool accounts) and logs the import. Unsupported entries (`serviceAccount:`, `domain:`, or service-account emails) are dropped, logged individually, and surfaced in `Settings → Access` (`droppedFlagEntries`). Thereafter the flag and environment variable are deprecated: grain logs a warning when set and ignores them, even if all database entries are later removed.
- **Sandbox `gcp-key` pool exclusion and refused-principal logging.** Service accounts belonging to the `gcp-key` sandbox credential pool (`GCPKeyPoolEmails`: the configured base account and numbered `<prefix>-0@…` / `<prefix>-1@…` pool accounts whose keys enter task sandboxes) are never admitted as provisioned daemon accounts or `hostd` owners, are rejected at startup by `daemon.CheckUIAllowedPrincipals` and dropped during flag migration if listed in `-ui-allowed-principals`, and are unconditionally refused (`403`) at request time. Every verified principal turned away by `checkAllowedPrincipal` is logged once per `(email, isGCPKeyPool)` up to 64 entries (`logRefusedPrincipal` in `pkg/ui/allowlist.go`), with a dedicated warning when a `gcp-key` pool account reaches the proxy instructing the operator to remove it from `iap_members` and rotate the pool's keys.
- **Decision — Group resolution (`CloudIdentityGroupChecker`).** IAP assertions carry no group claims. Grain calls the Cloud Identity Groups API (`groups:lookup` followed by `memberships:checkTransitiveMembership`, OAuth scope `https://www.googleapis.com/auth/cloud-identity.groups.readonly`) **only** for `model.PrincipalHuman` callers when at least one `group:` entry is present in `allowed_principal` and no direct `user:` entry matched; deployments without `group:` entries never contact Cloud Identity and need no Cloud Identity IAM permission on `grain-daemon@…`. Positive and negative membership answers are cached for `5m` (`DefaultGroupMembershipCacheTTL`); lookup errors are never cached and fail closed (`403`), with `10s` bounds on both OAuth token headers and API calls (`cloudIdentityTimeout`).
- **Decision — Equal operator powers, agent scopes, and token minting (until RBAC lands).** Grain is single-tenant ([`docs/access-code.md`](../../docs/access-code.md)): every verified user admitted via `allowed_principal` has the same full operator powers as an access-code holder, including viewing, adding, and removing allowlist entries (`Settings → Access`, `GET`/`POST`/`DELETE /api/access/principals`, and `grain access list|allow|deny`), minting `gma_…` external-agent tokens, and registering `hostd` rows (`POST /api/hostds`). Every allowlist change is recorded in `config_audit` (`surface = "access"`). For external or task agents, `api:access:read` reads the list and dangerous `api:access:write` (grantable only by a person, requiring `X-Grain-Acting-Reason` recorded in `config_audit`, and refused on `sandbox-cli`) modifies it. Minting or rotating a `gma_…` credential (`POST /api/external-agents/tokens`, `POST /api/external-agents/tokens/{id}/rotate`) that grants a dangerous scope (`model.IsDangerousAPIScope`) or a `requires_person` scope set requires a `model.PrincipalHuman` caller (`canIssuePersonExternalAgentToken`); `model.PrincipalService` and `model.PrincipalAgent` callers are refused with `403`. Finer-grained roles are deferred to `rbac.design.md` (task 2764).

## 4. Access-code modes, precedence, CSRF, and refusal pages

### Three access modes (`pkg/ui/accesscode.go`, [`docs/access-code.md`](../../docs/access-code.md))

1. **No verifier active (standalone / local default).** `local-api-token` (`$GRAIN_ACCESS_CODE`) is the sole credential: browsers exchange it at `POST /api/auth/access-code` for the `grain_access` cookie (`HttpOnly`, `SameSite=Strict`), and the CLI sends it as `Authorization: Bearer`.
2. **Migration mode (verifier active, `-ui-require-verifier` / `-ui-require-iap` unset).** Verified, allowlisted callers pass `singleTenantGate` without the access code; requests carrying no assertion may still authenticate with the access code.
3. **Verifier required (`-ui-require-verifier=<name>` / `$GRAIN_REQUIRE_VERIFIER`, with `-ui-require-iap` / `$GRAIN_REQUIRE_IAP` kept as an alias for `-ui-require-verifier=iap`).** `daemon.CheckUIRequireVerifier` (`pkg/daemon/ui_server.go`, `pkg/cli/daemon.go`) refuses conflicting flags and refuses to start if the named `IdentityVerifier` is not active (for `"iap"`, when no IAP audience or registered `"iap"` verifier is configured; for `"registry"`, when `-ui-registry-issuer`, `-ui-registry-audience`, and `-ui-registry-keys-url` are not all configured and no `"registry"` verifier is registered). Over the network—and over loopback whenever any proxy or verifier header is present (`relayedByProxy` in `pkg/ui/accesscode.go`)—neither the bearer access code, nor `grain_access`, nor `POST /api/auth/access-code` is accepted (`accessCodeAccepted` returns `false`). Over **unproxied loopback** (`127.0.0.0/8`, `::1`, or Unix socket with no forwarding headers), the access code remains accepted so `kubectl exec deploy/grain -- grain …` and SSH tunnels always work as break-glass recovery.

### Per-verifier access-code precedence

- **Verified identity wins by default (`AccessCodeWins() == false`, including IAP).** When a request carries both an IAP assertion and a bearer access code or `grain_access` cookie, the assertion governs: the caller must pass `checkAllowedPrincipal` and `checkVerifiedIdentityCSRF`, and acts as their named email rather than anonymous `operator`.
- **Access code wins for always-attached wrapper proxies (`AccessCodeWins() == true`).** A wrapper distribution whose corporate proxy attaches a signed identity header to every proxied request may return `AccessCodeWins() == true`. When such a request also carries a valid access code (or a valid `gma_…` token), `singleTenantGate` and `accessCodeAccepted` honour that credential—even under `-ui-require-verifier` / `-ui-require-iap`—so an operator can still break glass through the corporate proxy when the allowlist or group directory is broken. A present header that fails cryptographic verification still returns `401` with no fallback.
- **Signing in by code behind a winning verifier is refused, not ignored.** Under `AccessCodeWins() == false`, `POST /api/auth/access-code` with a verified assertion answers `403`, saying that the verified identity's role applies. It sets no `grain_access` cookie, which that browser's proxied requests would never use. The code is not compared, so the refusal reveals nothing about it.

### CSRF protection (`pkg/ui/csrf.go`)

IAP authenticates browser requests with IAP's own cookie rather than grain's `SameSite=Strict` `grain_access` cookie. Grain enforces two layers of cross-origin protection:

1. `ServeHTTP` runs Go's `http.CrossOriginProtection` (`checkCrossOrigin`) across all routes (`GET`, `HEAD`, and `OPTIONS` allowed; browser cross-site requests rejected via `Sec-Fetch-Site` and `Origin`).
2. `singleTenantGate` runs `checkVerifiedIdentityCSRF` on every request admitted through a verified identity whose HTTP method is neither `GET` nor `HEAD`:
   - `Sec-Fetch-Site: same-origin` or `Sec-Fetch-Site: none` is allowed; any other non-empty `Sec-Fetch-Site` is refused with `403`.
   - When `Sec-Fetch-Site` is absent, `Origin` is parsed and its `host[:port]` is compared case-insensitively against `r.Host` and comma-separated `X-Forwarded-Host` values (`403` on mismatch or malformed `Origin`).
   - When both `Sec-Fetch-Site` and `Origin` are absent, requests carrying a non-empty `Cookie` header are refused with `403`, while cookie-less programmatic requests (`Authorization: Bearer`, `X-Grain-Hostd-Token`) pass.

### Refusal pages (`pkg/ui/principalrefusal.go`, `pkg/ui/accesscode.go`)

Browser navigations (`GET` or `HEAD` outside `/api/`) receive self-contained HTML pages (`Cache-Control: no-store`), while `/api/*` and non-`GET`/`HEAD` requests receive JSON errors:

- **`403` `"Not allowed on this grain"` (`writePrincipalRefusedPage`).** Rendered when a caller's assertion verifies (`cv.err == nil`) but `checkAllowedPrincipal` refuses admission. The page displays the verified email and verifier name, any underlying lookup error (for example a Cloud Identity timeout), remediation guidance from `ui.RegisterPrincipalAdvice` (defaulting to `Settings → Access` and `grain access allow user:<email>` for human callers, or `grain host register --owner <email>` for service accounts), and an `"Owner: sign in with the access code"` section that renders `<form method="post" action="/api/auth/access-code">` **only** when `AccessCodeWins()` is true—otherwise explaining that the verifier's identity takes precedence over the access code and instructing the owner to connect directly or over an SSH tunnel.
- **`401` `"Sign-in could not be verified"` (`writeAssertionRefused`).** Rendered when a verifier header is present on a browser navigation but fails verification, naming the failure reason and configuration/clock checklist with no access-code fallback.
- **`401` `"Sign in through <verifier>"` / `"Sign in through IAP"` (`writeVerifierRequiredPage` / `writeIAPRequiredPage`).** Rendered when `-ui-require-verifier` (or `-ui-require-iap`) refuses an unauthenticated network browser navigation, in place of the `"Enter access code"` login form.

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

External agents behind IAP authenticate with a scoped `gma_…` credential (`Authorization: Bearer gma_…`, sending their Google OIDC ID token in `Proxy-Authorization: Bearer <id-token>` when traversing IAP so `Authorization` reaches grain intact). `verifyCallerNow` still validates any present proxy assertion (`401` on invalid signature) and, for verifiers where `AccessCodeWins() == false` (including IAP), `singleTenantGate` still requires the proxy identity to pass `checkAllowedPrincipal` (`403` on disallow). `authenticateExternalAgent` sets the request's principal to `{agent, external:<name>, external-token}`, enforcing the credential's scopes, repository list, rate limit, and concurrency cap. (General GCP service accounts outside the daemon's own provisioned accounts are not admitted as single-tenant operators.)

### Decision — `grain hostd` and owner matching (`pkg/hostd/`, `pkg/ui/hostd_connect.go`, `pkg/ui/hostds.go`)

Operators register per-hostd credentials in the hostd registry (`POST /api/hostds`, `grain host register --owner <email>`, `grain host set-owner`, `pkg/ui/hostds.go`, `pkg/cli/hostds.go`), receiving a `ghd_…` token bound to that hostd row and its `owner` email (human or service account). Revoking a hostd (`POST /api/hostds/{name}/revoke`) retires its tokens and drops its session; a revoked hostd can then be deleted (`DELETE /api/hostds/{name}`, `grain host delete`), which removes its row and token rows so the name can be registered again with a fresh token (`docs/host-placement.md`). A deprecated single-file fallback (`-hostd-token-file` paired with `-hostd-token-owner` / `$GRAIN_HOSTD_TOKEN_OWNER`) remains supported for legacy setups. Remote model executors (`pkg/modelexec`, `executorBootstrapRoute`) were removed (`docs/executors-removed.md`).

`grain hostd` dials `GET /api/hostd/connect` (`pkg/hostd/dialout.go`) sending its hostd token in `X-Grain-Hostd-Token` (plus optional `X-Grain-Hostd-Name`). When `-iap-audience` (`$GRAIN_IAP_AUDIENCE`) is set on `grain hostd`, `hostd.NewIAPTokenProvider` (`pkg/hostd/iap_auth.go`) mints and caches an OIDC ID token from the GCE metadata server (`…/instance/service-accounts/default/identity?audience=<aud>&format=full`) with fallback to Application Default Credentials (`idtoken.NewTokenSource`), sending `Authorization: Bearer <id-token>` for IAP while `X-Grain-Hostd-Token` carries the hostd credential.

Although `/api/hostd/connect` is in `singleTenantOpenRoute` (requiring no deployment access code), `serveHTTP` runs `verifyCallerNow` first (`401` on bad assertion) and `hostdConnectIdentity` (`pkg/ui/hostd_connect.go`) admits a present verified identity only when it is an active registered non-pool `hostd` service-account owner (`isRegisteredHostdServiceOwner`) or passes `checkAllowedPrincipal` (`403` on disallow):

- **Local `hostd` (`Remote: false`):** Only connections with no verifier header arriving either when no verifiers are active or over unproxied loopback (`isLoopbackRequest && !relayedByProxy`)—plus the local Unix socket `/run/grain-hostd/hostd.sock`—are treated as local and authenticate by hostd token alone.
- **Remote `hostd` (`Remote: true`):** Every connection carrying a verifier header, arriving from the network when any verifier is active, or carrying proxy forwarding headers is remote. `hostd.Remote.ServeHTTP` (`pkg/hostd/remote.go`, `pkg/hostd/connect_identity.go`) refuses with `401` if `Principal == ""` (preventing direct-to-pod bypass when IAP is configured) and refuses with `403` unless the hostd's configured `owner` is non-empty and matches `Principal` case-insensitively (`strings.EqualFold`). A stolen `ghd_…` token therefore cannot connect without its owner's verified identity.

### In-sandbox calls

Agent calls from inside a task sandbox never traverse IAP and never carry a person's identity. Both the sandbox git/API proxy (`127.0.0.1:9728`) and `grain mcpserver` (`X-Grain-Acting-Task`) run off the Gateway using per-attempt scoped sandbox tokens, and proxy assertion headers never authenticate them.

## 6. Delegated identity through the registry (proposed)

**Status: proposed (task 3130), not built.** Specified by [`authentication.spec.md`](../spec/authentication/authentication.spec.md) `{#AU-22}`–`{#AU-27}`. The registry side, covering which apps may delegate, people's consent grants, revocation and registry audit, is in [`registry.design.md`](registry.design.md) *Delegated identity*.

A **delegating app** is a service principal registered with the registry, such as a planning board or a chat app. When a person has granted it consent, the app may send a request to `https://<name>.<instance-domain>/…` that names that person in `X-Grain-On-Behalf-Of`. The registry checks the registration and the grant, strips the header, and forwards the request with a registry JWT whose `sub` is the person (`kind: human`) and whose `act` is the app:

```json
"act": {"sub": "planning-app@example-prod.iam.gserviceaccount.com", "kind": "service", "app": "planning-app", "grant": "dlg_4f9c2a7e1b", "access": "write"}
```

The grain never trusts a header to name the person. It learns about delegation only from the signed `act`.

### The verifier exposes both identities

```go
type VerifiedIdentity struct {
	Email string              // the person (or the direct caller)
	Kind  model.PrincipalKind
	Via   *DelegatingApp      // set only by a verifier that carries delegation
}

// DelegatingApp is the application acting for VerifiedIdentity.Email.
type DelegatingApp struct {
	Email  string // act.sub, a service-account email; Kind is always PrincipalService
	Name   string // act.app, informational (logs); labels come from the delegate: entry
	Grant  string // act.grant, for joining grain and registry audit
	Access string // act.access: "read" or "write"
}
```

Only the `registry` verifier sets `Via`. IAP and wrapper verifiers leave it nil unless they adopt the same claim. A present `act` that is malformed refuses the request with `401` (`writeAssertionRefused`), with no fallback (§1). Malformed means any of:

- a nested `act`;
- `act.kind` other than `service`;
- a top-level `kind` other than `human`;
- an empty `act.sub`;
- an `act.sub` that is not a service-account email.

### Admission: the person, and an app allowed to delegate here

1. **The person** must pass `allowed_principal` exactly as a direct caller would (§3: `user:` and `group:` entries, plus `domain:` once RBAC lands; group lookup; `gcp-key` pool exclusion), on `VerifiedIdentity.Email`.
2. **The app** must match a **`delegate:<service-account-email>`** entry on the same allowlist.
   - This is a new third prefix. It matches only `Via.Email`, never a direct caller.
   - A `serviceAccount:` entry for the same account does not let it delegate, and a `delegate:` entry does not admit it as a plain caller.
   - Like a service-account entry, a `delegate:` entry is added only explicitly, with a required reason (`grain access allow delegate:<email> --role <set> --reason "…" [--label planning-app]`). Only a verified human admin or an access-code admin may add it. Every agent, console session and `gma_…` token is refused, whatever it holds.
   - It is never imported from `-ui-allowed-principals`, and stays outside `staterepo.SettingsTables` like the rest of `allowed_principal`.
3. **Neither check alone is enough.** If the person is not allowed, the existing `403` *"Not allowed on this grain"* page names the person. If the app has no `delegate:` entry, a `403` names the app: *"planning-app is not allowed to act for people on this grain"*. The JSON form is `code: "delegate_not_allowed"`.

### Roles: the person's, capped by the app's

The request holds the **intersection** of two roles:

- the person's current role: the union of their matching entries' scope sets (§3), or `rbac.design.md`'s `admin`/`writer`/`reader`;
- the `delegate:` entry's role.

This is the rule `rbac.design.md` §3 already applies to agents acting `OnBehalfOf` a person (`RB-13`), evaluated on every request, so demoting or removing the person stops their delegated writes at once.

- A `delegate:` entry's role MUST NOT contain a dangerous scope (`model.IsDangerousAPIScope`), `access:write`, or an admin-only permission (`422` on add).
- Core ships a `grain/delegated` scope set covering what integrations do for people: read what a writer reads, file and edit tasks, comment and reply, approve non-admin-only grants, and start or resume work.
- When `Via.Access == "read"`, every method other than `GET` and `HEAD` is refused with `403`, as defence in depth behind the registry's own check.

**Person-only operations.** Some operations require `Actor.Kind == human` and are refused to every delegated request with `403` (`requires_person`), whatever the roles allow:

- `POST`/`PATCH`/`DELETE /api/access/principals`;
- minting or rotating `gma_…` tokens with dangerous or `requires_person` scopes (§3, `AU-11`);
- approving `IsAdminOnlyGrant` grants;
- `POST /api/hostds`.

A delegated identity never satisfies remote `hostd` owner matching (§5), and the registry refuses delegated protocol upgrades before they reach the grain.

### Attribution: "Roman (via planning-app)"

`ui.RequestActor(ctx)` returns:

```go
model.Attribution{
	Actor:      model.Principal{Kind: model.PrincipalService, ID: "planning-app@example-prod.iam.gserviceaccount.com", Source: "registry"},
	OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: "roman@example.com", Source: "registry"},
}
```

This follows [`operator-attribution.design.md`](operator-attribution.design.md)'s rule that whoever made the call is the actor and says who it acts for. It is stored in the existing actor and on-behalf-of columns, with no new schema.

- **Label.** `ui.PrincipalLabel` renders a verified `service` actor with a verified `human` on-behalf-of, both from the `registry` source, as **`roman@example.com (via planning-app)`**. That puts the person first, because the person is who the grain authorized. The app's label is the `delegate:` entry's `label`, or else the local part of its email. Agents acting for a person keep `<agent> for <person>`.
- **Trailers.** `Requested-by:` (task creator) and `Approved-by:` (a delegated approval) name the person's email. A plain service principal never fills them with a person.
- **Who am I.** `GET /api/auth/user` and `GET /api/whoami` report `actor: service:<app-email>`, `onBehalfOf: user:<email>` and `via: <label>`.
- **Request log.** Each delegated request records `registry_rid`, `registry_jti` and `delegation_grant` in the grain's request log, joining the registry's `delegated_write` audit row.
- **Settings → Access** lists `delegate:` entries with `added_by`, reason, role and last use.

### What does not change

- **CSRF (§4).** Delegated calls are server-to-server and cookie-less, so `checkVerifiedIdentityCSRF` passes them as programmatic callers, unchanged. The registry refuses `X-Grain-On-Behalf-Of` arriving with a `Cookie`, `Origin` or `Sec-Fetch-Site` header, so a browser cannot trigger delegation.
- **Access code and break-glass (§4).** `registry` keeps `AccessCodeWins() == false`. The access code and delegation never combine. Unproxied-loopback break-glass under `-ui-require-verifier` / `-ui-require-iap` is untouched. The mandatory tunnel-ingress gate on `connected` grains applies to delegated assertions exactly as to direct ones.
- **Plain service principals.** A `kind: service` assertion with no `act` is admitted only by an explicit `serviceAccount:` entry with a reason (§3) and attributed to the account alone. The recommended role for one is read-only (status, tasks, CI). Writes that represent a person's intent should arrive delegated.
- **In-sandbox calls (§5).** These never carry a person's identity, delegated or not.

## 7. Rollout status

### Done

- **`IdentityVerifier` seam and `NewIAPVerifier`** (`pkg/ui/verifier.go`, `pkg/ui/principal.go`, `pkg/ui/assertion_diagnostics.go`): verifier registry, per-verifier `AccessCodePrecedence`, ECDSA/RSA key fetching, `30s` clock skew, fail-closed `401`, and bounded deduplicated assertion logging (tasks 2309, 2443).
- **Database allowlist, CSRF, UI identity, and refusal pages** (`pkg/model/allowed_principals.go`, `pkg/ui/access_principals.go`, `pkg/ui/allowlist.go`, `pkg/ui/csrf.go`, `pkg/ui/principalrefusal.go`, `pkg/ui/accesscode.go`, `pkg/cli/access.go`, `pkg/daemon/ui_server.go`): `PrincipalHuman` and `PrincipalService`, database-backed `allowed_principal` list (`user:` and `group:`) with one-time `-ui-allowed-principals` startup migration, optional Cloud Identity `group:` lookup, `gcp-key` sandbox pool exclusion, `Settings → Access` UI and `grain access list|allow|deny` CLI, `api:access:read` and dangerous `api:access:write` scopes, `checkVerifiedIdentityCSRF`, HTML refusal pages (`403` principal refused with `RegisterPrincipalAdvice`, `401` assertion refused, `401` IAP required), desktop and phone signed-in identity UI, and `/_gcp_iap/clear_login_cookie` logout (tasks 2309, 2383, 2443, 2510, 2741).
- **`-ui-require-iap` and loopback break-glass** (`pkg/ui/accesscode.go`, `pkg/cli/daemon.go`, `pkg/cli/accesscode.go`, [`docs/access-code.md`](../../docs/access-code.md)): network access-code disablement, startup audience check, and unproxied loopback break-glass (task 2318).
- **Per-hostd registry and remote `hostd` owner verification** (`pkg/ui/hostds.go`, `pkg/ui/hostd_connect.go`, `pkg/hostd/connect_identity.go`, `pkg/hostd/remote.go`, `pkg/hostd/iap_auth.go`, `pkg/cli/hostds.go`): `/api/hostds`, `grain hostds`, `hostd.ConnectIdentity`, and `-hostd-token-owner` (tasks 2164, 2316).
- **Operator attribution foundation** (`pkg/model/task.go`, `pkg/ui/request_actor.go`, `pkg/ui/principal_label.go`): `Principal.Source`, `Principal.Verified()`, `ui.RequestActor(ctx)`, and `*_source` database columns across mutating routes (tasks 2310, 2311).
- **Simulated IAP end-to-end and deployment verification:** 112 checks passed, 0 failed across browser UI, CLI, external agents, and remote `hostd` (`docs/test-runs/2026-11-02-iap-end-to-end.md`, task 2510), plus Terraform/script flag rendering, refused-principal and own-audience logging, phone drawer stacked layout, and external-config-repo walkthroughs (`docs/test-runs/2026-11-03-iap-deployment.md`, task 2710).

### Remaining

- **Dedicated CLI IAP ergonomics:** teaching `ui.HTTPClient` and the `grain` CLI to send the IAP OIDC token in `Proxy-Authorization: Bearer` (freeing `Authorization` when pairing IAP with a `gma_…` token), reading `GRAIN_IAP_AUDIENCE` from `/etc/grain/cli.env`, and adding a user OAuth desktop/device flow when `gcloud` cannot mint an audience-bound ID token without service-account impersonation.
- **Live GKE Gateway + Cloud IAP rollout check:** verifying a live cluster deployment behind Google's managed IAP edge alongside the simulated proxy suite.
- **Remaining operator-attribution items** ([`operator-attribution.design.md`](operator-attribution.design.md)): `GET /api/whoami` (`OA-5`) and `OnBehalfOf` dual-identity recording when an external or in-grain agent acts on a verified person's behalf (`OA-13`).
- **Registry verifier and generalised verifier requirement** ([`registry.design.md`](registry.design.md)): core `registry` `IdentityVerifier` (`X-Grain-Registry-Assertion`) and generalising `-ui-require-iap` to `-ui-require-verifier`.
- **Role-based access control** (`rbac.design.md`, task 2764): roles and scoped permissions beyond single-tenant operator equivalence.
- **Delegated identity through the registry** (§6, `AU-22`–`AU-27`, task 3130; `registry.design.md` implementation plan step 19): `VerifiedIdentity.Via` from the registry JWT's `act` claim, `delegate:` allowlist entries, dual admission, person ∩ delegate role intersection, `requires_person` refusals, and `roman@example.com (via planning-app)` attribution. Depends on the `registry` verifier.
