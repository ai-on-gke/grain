# Authentication Specification

**Specifies:** [`authentication.design.md`](../../design/authentication.design.md)

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHALL**, **SHALL NOT**, **SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **MAY**, and **OPTIONAL** in this document are to be interpreted as described in RFC 2119.

Every normative requirement below is derived from the implementation on `main` and names the automated test(s) that pin it, or explicitly marks unpinned clauses **untested**.

---

## Contents

- [1. Verifier Registration and Evaluation](#1-verifier-registration-and-evaluation)
- [2. IAP Assertion Verification and Principal Normalization](#2-iap-assertion-verification-and-principal-normalization)
- [3. Database Principal Allowlist (`allowed_principal`)](#3-database-principal-allowlist-allowed_principal)
- [4. Access-Code Precedence and Credential Interaction](#4-access-code-precedence-and-credential-interaction)
- [5. `-ui-require-verifier` (`-ui-require-iap`) and Loopback Break-Glass](#5--ui-require-verifier--ui-require-iap-and-loopback-break-glass)
- [6. Cross-Origin and CSRF Protection](#6-cross-origin-and-csrf-protection)
- [7. Refusal Responses and Browser Pages](#7-refusal-responses-and-browser-pages)
- [8. `grain host` Connection Identity and Owner Matching](#8-grain-host-connection-identity-and-owner-matching)
- [9. Diagnostic Logging of Refused Assertions and Principals](#9-diagnostic-logging-of-refused-assertions-and-principals)
- [10. Plaintext Identity Headers and Sandbox Isolation](#10-plaintext-identity-headers-and-sandbox-isolation)
- [11. Delegated (On-Behalf-Of) Identity via the Registry](#11-delegated-on-behalf-of-identity-via-the-registry)
- [12. Requirement Traceability Summary](#12-requirement-traceability-summary)
- [13. Sources](#13-sources)

---

## 1. Verifier Registration and Evaluation

### 1.1 `IdentityVerifier` seam and active ordering {#AU-1}

1. `pkg/ui` MUST expose the `IdentityVerifier` interface (`Name() string`, `Headers() []string`, `Verify(*http.Request) (VerifiedIdentity, bool, error)`), the optional `AccessCodePrecedence` interface (`AccessCodeWins() bool`), the `VerifiedIdentity` struct (`Email string`, `Kind model.PrincipalKind`), and `RegisterIdentityVerifier(v IdentityVerifier) (unregister func())` (`pkg/ui/verifier.go`).
2. `Server.activeVerifiers()` MUST construct the active verifier list in deterministic order:
   - First, `NewIAPVerifier(s.Config.IAP)` (`Name() == "iap"`, `Headers() == ["X-Goog-IAP-JWT-Assertion"]`) if and only if `s.Config.IAP.Enabled()` is `true`;
   - Next, all verifiers currently registered via `RegisterIdentityVerifier`, in the exact order they were registered (`pkg/ui/verifier.go`).
3. Every header name declared by any active verifier's `Headers()` MUST also be treated as a proxy-forwarding indicator by `Server.relayedByProxy(r)` (`pkg/ui/accesscode.go`).

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders`, `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes`

### 1.2 First-present-header evaluation and fail-closed refusal {#AU-2}

1. `Server.verifyCallerNow(r)` MUST evaluate active verifiers at most once per HTTP request—before external-agent token authentication (`authenticateExternalAgent`) and before `singleTenantGate`—and store the resulting `callerVerification` in the request context (`pkg/ui/server.go`, `pkg/ui/verifier.go`).
2. `Server.verifyCaller(r)` MUST skip any active verifier none of whose `Headers()` has a non-empty value on `r`. The first active verifier with at least one non-empty declared header MUST decide the request; subsequent verifiers MUST NOT be consulted even if the deciding verifier returns `ok == false` or a non-nil `err`.
3. If the deciding verifier returns a non-nil `err` (`cv.present && cv.err != nil`), the server MUST refuse the request with HTTP `401 Unauthorized` (`writeAssertionRefused`) and MUST NOT fall back to the deployment access code (`local-api-token`), a `grain_access` cookie, an external-agent `gma_…` token, an existing session token, or an open route (including `GET /api/hostd/connect`).

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders`, `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes`
- `pkg/ui/assertion_refused_page_test.go`: `TestUnverifiedAssertionGetsAPageForABrowserAndJSONForTheAPI`
- `pkg/ui/hostd_owner_test.go`: `TestRemoteHostdMustConnectAsItsOwner`
- `tests/security/identity_test.go`: `TestForgedIAPAssertionDoesNotFallBackToHeaders`

---

## 2. IAP Assertion Verification and Principal Normalization

### 2.1 IAP audience, issuer, and public-key resolution {#AU-3}

1. `IAPConfig.Enabled()` MUST return `true` when at least one non-empty expected audience is configured in `IAPConfig.Audience` (`-ui-iap-audience`) or, in fallback order when unset, `$GRAIN_IAP_AUDIENCE`, `$GRAIN_AUDIENCE`, or `$IAP_AUDIENCE` (comma-separated values trimmed of whitespace) (`pkg/ui/principal.go`, `pkg/cli/daemon.go`).
2. The expected issuer MUST default to `https://cloud.google.com/iap` and MAY be overridden via `IAPConfig.Issuer` (`-ui-iap-issuer` / `$GRAIN_IAP_ISSUER`).
3. Unless `IAPConfig.Validator` overrides verification in tests, public keys MUST be fetched from `IAPConfig.KeysURL` (`-ui-iap-keys-url` / `$GRAIN_IAP_KEYS_URL`, defaulting to `https://www.gstatic.com/iap/verify/public_key`) with a `5s` HTTP client timeout, supporting both IAP's `{kid: pem}` JSON map and RFC 7517 JWK Sets (`{"keys": […]}`) for ECDSA P-256 and RSA public keys.
4. Resolved keys MUST be cached in memory for `1h` (`iapKeysTTL`). On a JWT `kid` cache miss, the verifier MUST refetch keys only if the cached set is older than `10s` (`iapKeysRefetchFloor`), and if a refresh fails while previously fetched keys exist, the verifier MUST retain the stale key map rather than wiping the cache.

**Pinned by:**
- `pkg/ui/principal_test.go`: `TestIAPAssertionVerification`, `TestIAPAssertionKeysAreFetchedFromTheEndpoint`
- `pkg/ui/verifier_allowlist_test.go`: `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`
- **Untested:** RFC 7517 JWKS (`{"keys": […]}`) parsing branch, RSA (`RS256`) public-key parsing branch, `10s` unknown-`kid` refetch floor, and stale-key retention when a key-endpoint refresh errors.

### 2.2 Signature, algorithm, `iss`, `aud`, and `exp`/`iat` clock-skew checks {#AU-4}

1. `verifyIAPAssertion` MUST require a non-empty string `kid` JWT header matching a loaded public key (`pkg/ui/principal.go`).
2. The verifier MUST pin the allowed JWT signing algorithm to the matched key's concrete type—`ES256` for `*ecdsa.PublicKey` and `RS256` for `*rsa.PublicKey`—and MUST reject `alg: none`, HMAC algorithms, and algorithm/key-type mismatches.
3. The verifier MUST enforce exact `iss` equality against the configured issuer, require at least one JWT `aud` entry to match a configured audience string, require `exp` to be present (`jwt.WithExpirationRequired()`), and validate `exp`, `iat` (`jwt.WithIssuedAt()`), and `nbf` with a `30s` clock-skew leeway (`iapClockSkew = 30 * time.Second`).

**Pinned by:**
- `pkg/ui/principal_test.go`: `TestIAPAssertionVerification`
- `pkg/ui/verifier_allowlist_test.go`: `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`
- `pkg/ui/refused_assertion_log_test.go`: `TestRefusedAssertionLoggedOncePerKindNotOncePerAudience`

### 2.3 Email normalization, principal kind, and `RequestActor` attribution {#AU-5}

1. `verifyIAPAssertion` MUST extract the `email` claim, falling back to `sub` only when `email` is empty, and normalize it via `normalizeIdentityEmail`: trimming whitespace, stripping a leading `https://accounts.google.com:` or `accounts.google.com:` prefix, trimming again, and converting to lower-case. If the normalized email is empty, verification MUST fail with `401`.
2. `verifiedIdentityForEmail` MUST classify any normalized email ending in `.gserviceaccount.com` as `model.PrincipalService` (`"service"`) and all other normalized emails as `model.PrincipalHuman` (`"human"`).
3. When admitted via a verifier, the request's `model.Principal` MUST set `ID` to the normalized email and `Source` to `model.AuthSource(verifier.Name())` (`"iap"` for IAP), for which `Principal.Verified()` MUST return `true` (`pkg/model/task.go`).
4. `ui.RequestActor(ctx)` MUST return that verified `model.Principal` on mutating routes, while `GET /api/config` and `GET /api/auth/user` MUST report `authMethod` equal to `verifier.Name()` and `user`/`actor` prefixed with `user:` (for `PrincipalHuman`) or `service:` (for `PrincipalService`).

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`, `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes`
- `pkg/ui/principal_label_test.go`: `TestPrincipalLabel`, `TestPrincipalVerified`
- `pkg/ui/auth_method_test.go`: `TestAuthUserReportsHowTheCallerAuthenticated`
- `pkg/ui/mutating_routes_test.go`: `TestEveryMutatingRouteIsAttributed`
- `pkg/ui/request_actor_source_test.go`: `TestNothingOutsideRequestActorReadsTheActor`

### 2.4 `registry` `IdentityVerifier` (`X-Grain-Hub-Assertion` / `X-Grain-Registry-Assertion`) {#AU-5B}

1. The built-in `"registry"` `IdentityVerifier` (`pkg/webauth/hub.go`, `pkg/ui/verifier.go`) MUST be active when `-ui-hub-issuer` (`$GRAIN_HUB_ISSUER`), `-ui-hub-audience` (`$GRAIN_HUB_AUDIENCE`), and `-ui-hub-keys-url` (`$GRAIN_HUB_KEYS_URL`) (or their deprecated `-ui-registry-*` / `$GRAIN_REGISTRY_*` aliases) are all configured (`daemon.CheckUIRegistryVerifier` refuses partial configuration or comma-separated issuer/audience values at startup). It inspects `X-Grain-Hub-Assertion` (`ui.HubAssertionHeader`) and `X-Grain-Registry-Assertion` (`ui.RegistryAssertionHeader`) (`Headers() == webauth.HubAssertionHeaders`), refuses requests carrying conflicting values across the two headers or multiple distinct values on either header, reports `Name() == "registry"` (`ui.AuthMethodRegistry`, `model.SourceRegistry`), and sets `AccessCodeWins() == false`.
2. Signature verification MUST fetch RFC 7517 JWKS keys via `pkg/jwks` (cached for `1h`, refetched on unknown `kid` at most once per `1m`, retaining stale cached keys on refresh failure, and returning a `VerifierUnavailableError` (`503` with `Retry-After: 5`) when the initial JWKS fetch fails before any keys are cached) and MUST pin the algorithm to `ES256` on an ECDSA P-256 key (`RS256`, `HS256`, and `none` rejected).
3. The verifier MUST enforce exact `iss` equality against `-ui-hub-issuer`, single-audience equality (`len(aud) == 1 && aud[0] == -ui-hub-audience`), require `exp` and `iat` with `30s` clock-skew tolerance on `exp`, `nbf`, and `iat` and `0 < exp - iat <= 5m`, reject any token carrying the reserved `act` claim (`401`) until delegated-identity support is enabled, and normalize/classify `sub`/`email`/`kind` into `model.Principal{Kind: kind, ID: email, Source: "registry"}`.

**Pinned by:**
- `pkg/webauth/webauth_test.go`: `TestRegistryVerifier`
- `pkg/webauth/hub_rename_test.go`: `TestRegistryVerifierRefusesConflictingHubAndRegistryAssertions`
- `pkg/ui/hub_verifier_test.go`: `TestRegistryIdentityVerifierEndToEnd`
- `pkg/cli/daemon_require_iap_test.go`: `TestCheckConfigRegistryVerifierFlagsAndEnv`

---

## 3. Database Principal Allowlist (`allowed_principal`)

### 3.1 Allowlist grammar, API/CLI management, and one-time flag migration {#AU-6}

1. Verified-identity sign-in admission MUST be backed by the `allowed_principal` database table (`entry`, `added_by`, `added_at`; `pkg/model/allowed_principals.go`, `pkg/ui/access_principals.go`), managed live without daemon restart via `GET`/`POST`/`DELETE /api/access/principals`, `Settings → Access`, and `grain access list|allow|deny` (`pkg/cli/access.go`), with every mutation attributed in `config_audit` (`surface = "access"`). `allowed_principal` and `allowed_principal_migration` MUST be excluded from `staterepo.SettingsTables` while preserved in cold backups.
2. Each stored entry MUST normalize via `model.NormalizeAllowedPrincipalEntry` to `<prefix>:<email>` with case-sensitive prefix `user` or `group` and a lower-cased valid email address that MUST NOT end in `.gserviceaccount.com`.
3. When `-ui-allowed-principals` or `$GRAIN_ALLOWED_PRINCIPALS` is non-empty on startup, `Client.MigrateAllowedPrincipals` MUST import supported `user:` and `group:` entries (excluding `*.gserviceaccount.com` and `gcp-key` pool emails) at most once when `allowed_principal` is empty and `allowed_principal_migration` has no record, recording dropped entries in `allowed_principal_migration.dropped` (`droppedFlagEntries`) and ignoring the deprecated flag with a warning on subsequent starts (`daemon.CheckUIAllowedPrincipals` still validates syntax and rejects `gcp-key` pool accounts at startup).
4. For agent callers, `GET /api/access/principals` MUST require `api:access:read`, and `POST`, `PATCH` and `DELETE /api/access/principals` MUST be refused to every agent, console session and external agent token whatever it holds (docs/design/access-rbac.md §3).
5. Every entry MUST carry a `role` naming an API scope set, defaulting to `grain/operator` (every scope; schema 110 gives it to every earlier entry). A verified session MUST be held, on every route, to the union of its matching entries' roles less any scope one of their sets denies (`pkg/ui/access_roles.go`). A change involving a role that holds a dangerous scope or denies scopes MUST carry a reason. A verified session holding `access:write` MUST change an entry only when its own role holds every scope of the entry's current and new role (and every scope a deny role vetoes); such a change involving a dangerous or deny role MUST come from a person. It MUST NOT widen, or add or lift a deny on, an entry that matches itself, MUST confirm (`confirmSelf`) narrowing one, and MUST NOT remove or narrow the last `user:`/`group:` entry with a full role (`docs/design/access-rbac.md` §3).

**Pinned by:**
- `pkg/ui/access_roles_test.go`: `TestViewerRoleReadsButIsRefusedWrites`, `TestRepoLimitedRoleIsRefusedOtherRepositories`, `TestRolesOfEveryMatchingEntryAreUnioned`, `TestADenyInAnyMatchingRoleWithholdsTheScope`, `TestEntriesFromBeforeRolesKeepFullAccess`, `TestRoleAssignmentRules`
- `pkg/ui/access_roles_internal_test.go`: `TestRoleGateCoversEveryRoute`, `TestRoleGateFailsClosed`
- `pkg/ui/access_principals_test.go`: `TestAllowedPrincipalsFlagMigrationHappensExactlyOnce`, `TestAllowedPrincipalsLiveEnforcementEqualPrivilegesAndIdempotency`, `TestAllowedPrincipalsAgentScopeAndSandboxIsolation`, `TestMigrateAllowedPrincipalsRetriesAfterTransientFailureAndDropsServiceAccounts`
- `pkg/cli/access_test.go`: `TestAccessAllowListDenyCLI`

### 3.2 Direct entry matching semantics {#AU-7}

1. All principal email comparisons MUST be case-insensitive after `normalizeIdentityEmail` (`pkg/ui/allowlist.go`).
2. `user:<email>` MUST match only `VerifiedIdentity` values with `Kind == model.PrincipalHuman` and exact normalized `Email == <email>`.
3. In-memory `serviceAccount:<email>` entries (synthesized exclusively for daemon-provisioned non-pool service accounts) MUST match only `VerifiedIdentity` values with `Kind == model.PrincipalService` and exact normalized `Email == <email>`.

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestAllowedPrincipalsDefaultsAndCloudIdentityGroups`, `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`
- `pkg/ui/access_principals_test.go`: `TestAllowedPrincipalsLiveEnforcementEqualPrivilegesAndIdempotency`

### 3.3 Provisioned daemon service accounts and `gcp-key` sandbox pool exclusion {#AU-8}

1. Alongside `allowed_principal` rows from the store, the effective allowlist MUST admit only daemon-owned service accounts in `Config.ProvisionedServiceAccounts` and `grain-daemon@<gcp-project>.iam.gserviceaccount.com` (from startup flags and stored `model.Config.GCPProject`), excluding any address in `GCPKeyPoolEmails` (`pkg/ui/allowlist.go`, `pkg/daemon/ui_server.go`). Human principals are admitted only when matched by `allowed_principal` (never implicitly from `Config.Actor`).
2. `GCPKeyPoolEmails`—comprising the `gcp-key` capability's base service account and every numbered pool account `<prefix>-0@<domain>` / `<prefix>-1@<domain>` through `<prefix>-(N-1)@<domain>` whose keys enter task sandboxes—MUST:
   - be rejected at startup by `ValidateAllowedPrincipals` and `daemon.CheckUIAllowedPrincipals` if explicitly listed in `-ui-allowed-principals` or `$GRAIN_ALLOWED_PRINCIPALS`, and dropped by `MigrateAllowedPrincipals`;
   - be excluded from provisioned daemon service accounts and `isRegisteredHostdServiceOwner` even when `gcp-key` shares the base `grain-daemon@…` email; and
   - be unconditionally refused (`403`) at request time by `checkAllowedPrincipal`, before consulting direct or `group:` entries.

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestAllowedPrincipalsDefaultsAndCloudIdentityGroups`, `TestGCPKeyPoolAccountsAreNotAdmittedByDefault`, `TestRefusedPrincipalsAreLoggedOnce`
- `pkg/daemon/allowed_principals_test.go`: `TestCheckUIAllowedPrincipalsRefusesGCPKeyPoolAccounts`
- `tests/security/gcpkey_iap_allowlist_test.go`: `TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken`

### 3.4 Optional Cloud Identity `group:` lookup, caching, and timeouts {#AU-9}

1. Grain MUST NOT initialize or call the Cloud Identity Groups API unless at least one `group:` entry is present in `allowed_principal` AND the caller has `Kind == model.PrincipalHuman` (`pkg/ui/allowlist.go`); `model.PrincipalService` callers MUST NOT resolve through `group:` entries.
2. Direct entries (`user:`, `serviceAccount:`) MUST be evaluated before `group:` entries; if a direct entry matches, the caller MUST be admitted without performing any Cloud Identity lookup.
3. When evaluating `group:` entries for a `model.PrincipalHuman` caller, `CloudIdentityGroupChecker` (`pkg/ui/allowlist.go`) MUST resolve the group resource via `GET /v1/groups:lookup?groupKey.id=<group>` and check transitive membership via `GET /v1/<groupName>/memberships:checkTransitiveMembership?query=member_key_id == '<member>'` using OAuth scope `https://www.googleapis.com/auth/cloud-identity.groups.readonly`.
4. Positive and negative `(group, member)` membership results MUST be cached in memory for `5m` (`DefaultGroupMembershipCacheTTL`); lookup or token errors MUST NOT be cached and MUST fail closed (`allowed = false`, HTTP `403`).
5. Each Cloud Identity call context, HTTP client timeout, and OAuth token-endpoint `ResponseHeaderTimeout` MUST be bounded to `10s` (`cloudIdentityTimeout`).

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestAllowedPrincipalsDefaultsAndCloudIdentityGroups`
- `pkg/ui/group_lookup_timeout_test.go`: `TestGroupAllowlistRefusesWhenTokenEndpointNeverAnswers`
- `tests/security/gcpkey_iap_allowlist_test.go`: `TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken`

---

## 4. Access-Code Precedence and Credential Interaction

### 4.1 Per-verifier access-code precedence {#AU-10}

1. A verifier that does not implement `AccessCodePrecedence`, or whose `AccessCodeWins()` returns `false` (including `IAPVerifier`), MUST take precedence over the deployment access code (`pkg/ui/verifier.go`, `pkg/ui/accesscode.go`):
   - When its assertion is present and valid on a request that also carries a bearer access code or `grain_access` cookie, `singleTenantGate` MUST evaluate `checkAllowedPrincipal` and `checkVerifiedIdentityCSRF` against the verified identity, and the request's principal MUST be the verified identity rather than `DefaultActor`.
   - When such a request also carries a valid external-agent `gma_…` token, `singleTenantGate` MUST still require the proxy's verified identity to pass `checkAllowedPrincipal` (`403` if disallowed), while `authenticateExternalAgent` sets the acting principal to `{agent, external:<name>, external-token}`.
2. A verifier that implements `AccessCodePrecedence` with `AccessCodeWins() == true` MUST yield to a valid access code or valid `gma_…` token:
   - If the request carries a valid `gma_…` token or a valid access code (where `accessCodeAccepted` permits it), `singleTenantGate` MUST admit the request under that credential without requiring the proxy identity to match `checkAllowedPrincipal`.
   - If the request carries neither a valid access code nor a valid `gma_…` token, `singleTenantGate` MUST evaluate the verified identity through `checkAllowedPrincipal` and `checkVerifiedIdentityCSRF`.
3. Regardless of `AccessCodeWins()`, any present assertion that fails `Verify` MUST fail closed with `401` (`{#AU-2}`).

**Pinned by:**
- `pkg/ui/verifier_allowlist_test.go`: `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes`, `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`
- `pkg/ui/require_iap_test.go`: `TestRequireIAPKeepsTheCodeForAVerifierWhoseCodeWins`
- **Untested:** Interaction of a `gma_…` external-agent token carried alongside a verified assertion under both `AccessCodeWins() == false` and `AccessCodeWins() == true`.

### 4.2 Dangerous external-agent scope minting guard {#AU-11}

1. Creating (`POST /api/external-agents/tokens`) or rotating (`POST /api/external-agents/tokens/{id}/rotate`) an external-agent credential that grants any dangerous scope (`model.IsDangerousAPIScope`) or references a scope set with `requires_person: true` MUST be permitted only when `RequestActor(ctx).Actor.Kind == model.PrincipalHuman` (`canIssuePersonExternalAgentToken`, `pkg/ui/external_agents.go`).
2. Any `model.PrincipalService` caller or in-grain task agent (`model.PrincipalAgent`) MUST be refused with `403 Forbidden`.

**Pinned by:**
- `pkg/ui/external_agents_test.go`: `TestExternalAgentDangerousScopeRequiresPerson`
- `tests/security/gcpkey_iap_allowlist_test.go`: `TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken`

---

## 5. `-ui-require-verifier` (`-ui-require-iap`) and Loopback Break-Glass

### 5.1 Startup check requiring an active verifier {#AU-12}

1. `-ui-require-verifier=<name>` (or `$GRAIN_REQUIRE_VERIFIER`) MUST set `ui.Config.RequireVerifier = <name>`, and `-ui-require-iap` (or `$GRAIN_REQUIRE_IAP` set to `1`, `true`, `yes`, or `on`) MUST remain supported as an alias for requiring `"iap"` (`ui.Config.RequireIAP = true`, `pkg/cli/daemon.go`). Setting both `-ui-require-iap` and a non-`"iap"` `-ui-require-verifier` MUST be refused at startup.
2. `daemon.CheckUIRequireVerifier` (aliased as `daemon.CheckUIRequireIAP`) MUST refuse to start the daemon when a verifier is required (`RequireVerifier != ""` or `RequireIAP == true`) and `ui.HasActiveVerifier(ctx, required)` is `false` (for `"iap"`, when neither an IAP audience nor a registered `"iap"` verifier is configured).

**Pinned by:**
- `pkg/cli/daemon_require_iap_test.go`: `TestCheckConfigRefusesRequireIAPWithoutAnAudience`, `TestRequireIAPFromTheEnvironment`, `TestCheckConfigRequireVerifier`, `TestRequireVerifierFromTheEnvironment`

### 5.2 Network access-code disablement and unproxied loopback break-glass {#AU-13}

1. When a verifier is required (`Config.RequireVerifier != ""` or `Config.RequireIAP == true`), `accessCodeAccepted(r, cfg)` MUST return `false`—disabling bearer access codes, `X-Grain-Access-Code`, `grain_access` session cookies, and `POST /api/auth/access-code`—unless either:
   - an active verifier implementing `AccessCodeWins() == true` verified its header on `r`, OR
   - `isLoopbackRequest(r)` is `true` (`127.0.0.0/8`, `::1`, or non-TCP Unix socket `RemoteAddr`) AND `relayedByProxy(r)` is `false` (`pkg/ui/accesscode.go`).
2. `relayedByProxy(r)` MUST return `true` whenever `r` carries any non-empty standard forwarding header (`Forwarded`, `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Real-IP`, `X-Client-IP`, `True-Client-IP`, `CF-Connecting-IP`, `X-Appengine-User-IP`, `Via`) or any header listed in `Headers()` of an active `IdentityVerifier`.
3. `GET /api/auth/access-code` MUST include `"requireVerifier": "<name>"` (plus `"requireIAP": true` when `<name>` is `"iap"`) and `"accessCodeAccepted"` when a verifier is required, and `grain access-code` MUST print a note to `stderr` explaining that the server accepts the access code only over loopback (`pkg/ui/accesscode.go`, `pkg/cli/accesscode.go`).

**Pinned by:**
- `pkg/ui/require_iap_test.go`: `TestRequireIAPRefusesTheAccessCodeOverTheNetwork`, `TestRequireIAPKeepsTheCodeForAVerifierWhoseCodeWins`, `TestWithoutRequireIAPTheCodeStillWorksOverTheNetwork`, `TestRequireVerifierRefusesAccessCodeOverNetworkAndKeepsLoopbackBreakGlass`, `TestRequireVerifierIAPMatchesRequireIAPAlias`, `TestRequireVerifierKeepsTheCodeForAVerifierWhoseCodeWins`
- `pkg/cli/daemon_require_iap_test.go`: `TestAccessCodeNotesAServerThatRequiresIAP`, `TestAccessCodeNotesAServerThatRequiresVerifier`
- `tests/security/loopback_test.go`: `TestTheHostCLIReachesAnUntrustedLoopbackWithTheLocalToken`

---

## 6. Cross-Origin and CSRF Protection

### 6.1 Cross-origin checks and verified-identity logout {#AU-14}

1. `Server.ServeHTTP` MUST enforce Go's `http.CrossOriginProtection` (`checkCrossOrigin`) before dispatching any route (`pkg/ui/csrf.go`, `pkg/ui/server.go`).
2. In addition, every request admitted via a verified identity whose HTTP method is neither `GET` nor `HEAD` MUST pass `checkVerifiedIdentityCSRF` (`pkg/ui/csrf.go`, `pkg/ui/accesscode.go`):
   - If `Sec-Fetch-Site` is present, the request MUST be allowed when its lower-cased value is `same-origin` or `none`, and refused with `403 Forbidden` (`{"error": "cross-origin request refused"}`) for any other value (`cross-site`, `same-site`, etc.).
   - If `Sec-Fetch-Site` is absent and `Origin` is present, `Origin` MUST parse with a non-empty `Host` matching `r.Host` or a comma-separated entry in `X-Forwarded-Host` case-insensitively, or be refused with `403 Forbidden`.
   - If both `Sec-Fetch-Site` and `Origin` are absent, the request MUST be refused with `403 Forbidden` when the `Cookie` header is non-empty, and allowed when `Cookie` is empty (cookie-less programmatic callers).
3. `POST /api/auth/logout` MUST clear `grain_access` (`MaxAge: -1`, `HttpOnly`, `SameSite=Strict`) and, when `identifyCaller` reports `AuthMethodIAP`, return `"redirect": "/_gcp_iap/clear_login_cookie"` in its JSON response (`pkg/ui/accesscode.go`).

**Pinned by:**
- `pkg/ui/csrf_test.go`: `TestCrossOriginStateChangeRefused`
- `pkg/ui/verifier_allowlist_test.go`: `TestVerifiedIdentityCSRFLogoutAndUnchangedNoIAPPath`
- `ui/src/components/PageHeader.test.jsx`: `"shows the signed-in email in GlobalButtons for IAP/verifier auth and opens Access/Logout menu"`
- `ui/src/components/settings/AccessSection.test.jsx`: `"names the server-reported IAP actor, without its prefix, and logs out"`
- `ui/e2e/iap-header-logout.spec.js`
- `ui/e2e/phone-signed-in-logout.spec.js`

---

## 7. Refusal Responses and Browser Pages

### 7.1 Disallowed principal refusal (`403`) and `PrincipalAdvice` {#AU-15}

1. When a caller's assertion verifies (`cv.err == nil`) but `checkAllowedPrincipal` denies admission, the server MUST respond with HTTP `403 Forbidden` and `Cache-Control: no-store` (`pkg/ui/principalrefusal.go`, `pkg/ui/accesscode.go`).
2. On API requests (`/api/` or `/api/*`) and non-`GET`/`HEAD` methods, the response body MUST be JSON naming the verified email and how to admit it (`Settings → Access` or `grain access allow`), plus any underlying lookup error.
3. On browser navigations (`GET` or `HEAD` outside `/api/`), `writePrincipalRefusedPage` MUST return `text/html; charset=utf-8` titled `"Not allowed on this grain"` containing:
   - the HTML-escaped verified email and verifier name (and any underlying lookup error);
   - remediation prose from the callback registered via `RegisterPrincipalAdvice`, or by default `Settings → Access` and `grain access allow 'user:<email>'` for human callers (or `grain host register --owner '<email>'` for service accounts);
   - an `"Owner: sign in with the access code"` section that renders `<form method="post" action="/api/auth/access-code">` if and only if the deciding verifier has `AccessCodeWins() == true`, and otherwise explains that the verifier's identity takes precedence over the access code.

**Pinned by:**
- `pkg/ui/principal_refusal_test.go`: `TestRefusedPrincipalGetsAPageNamingThemWithTheAccessCodeForm`, `TestRefusedPrincipalPageSaysTheAssertionTakesPrecedence`, `TestRefusedPrincipalPageUsesRegisteredAdvice`
- `pkg/ui/verifier_allowlist_test.go`: `TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders`

### 7.2 Unverified assertion (`401`) and verifier-required (`401`) browser pages {#AU-16}

1. When an assertion header is present and fails verification (`cv.present && cv.err != nil`), `writeAssertionRefused` MUST return HTTP `401 Unauthorized` with `Cache-Control: no-store`:
   - HTML `"Sign-in could not be verified"` naming the failure reason and omitting any access-code form on browser navigations (`GET`/`HEAD` outside `/api/`);
   - JSON `{"error": "authentication assertion failed verification"}` on `/api/*` and non-`GET`/`HEAD` requests (`pkg/ui/principalrefusal.go`).
2. When no assertion is present and a required verifier (`Config.RequireVerifier` or `Config.RequireIAP`) refuses the access code (`!accessCodeAccepted(r, cfg)`), unauthenticated browser navigations MUST receive HTTP `401 Unauthorized` HTML `"Sign in through IAP"` (for `"iap"`) or `"Sign in through <verifier>"` (`writeVerifierRequiredPage`) with `Cache-Control: no-store` and no access-code form, while API requests receive HTTP `401 Unauthorized` JSON stating that sign-in through the required verifier is needed and the access code works only on loopback (`pkg/ui/accesscode.go`).

**Pinned by:**
- `pkg/ui/assertion_refused_page_test.go`: `TestUnverifiedAssertionGetsAPageForABrowserAndJSONForTheAPI`
- `pkg/ui/require_iap_test.go`: `TestRequireIAPRefusesTheAccessCodeOverTheNetwork`, `TestRequireVerifierRefusesAccessCodeOverNetworkAndKeepsLoopbackBreakGlass`, `TestRequireVerifierIAPMatchesRequireIAPAlias`

---

## 8. `grain host` Connection Identity and Owner Matching

### 8.1 Local vs remote `hostd` classification and allowlist gate {#AU-17}

1. `GET /api/hostd/connect` MUST remain in `singleTenantOpenRoute` (requiring no deployment access code), while still running behind `verifyCallerNow` so any invalid assertion fails closed with `401` (`pkg/ui/accesscode.go`, `pkg/ui/hostd_connect.go`).
2. When a verifier header is present and valid on `GET /api/hostd/connect`, `hostdConnectIdentity` MUST admit the caller only if `isRegisteredHostdServiceOwner` is true (active registered non-pool `hostd` service-account owner) or `checkAllowedPrincipal` succeeds (`403 Forbidden` otherwise), and pass `hostd.ConnectIdentity{Principal: cv.id.Email, Via: string(cv.method), Remote: true}` to the `hostd` handler.
3. When no verifier header is present on `GET /api/hostd/connect`, `hostdConnectIdentity` MUST classify the connection as local (`Remote: false`) if and only if either no verifiers are active (`len(activeVerifiers()) == 0`) OR `isLoopbackRequest(r) && !relayedByProxy(r)`; otherwise it MUST classify the connection as `Remote: true` with `Principal: ""`.

**Pinned by:**
- `pkg/ui/hostd_owner_test.go`: `TestRemoteHostdMustConnectAsItsOwner`
- `pkg/ui/access_principals_test.go`: `TestRegisteredHostdServiceOwnerDialsCleanlyWithoutOperatorAccessOrRefusalLog`
- `pkg/ui/hostd_connect_test.go`: `TestHostdConnectEndpoint`
- `tests/security/single_tenant_test.go`: `TestASingleTenantDaemonAcceptsARemoteHostd`

### 8.2 Hostd token authentication and remote owner matching {#AU-18}

1. `grain host` dialling `GET /api/hostd/connect` MUST present its hostd credential (`ghd_…` registry token from `/api/hostds` or the deprecated shared `-hostd-token-file` secret) in `X-Grain-Hostd-Token` (falling back to `Authorization: Bearer` on the server only when `X-Grain-Hostd-Token` is empty), leaving `Authorization: Bearer <id-token>` available for OIDC ID tokens minted by `hostd.IAPTokenProvider` when `-iap-audience` / `$GRAIN_IAP_AUDIENCE` is set (`pkg/hostd/dialout.go`, `pkg/hostd/remote.go`, `pkg/hostd/iap_auth.go`).
2. `hostd.Remote.ServeHTTP` MUST validate the hostd token first (`401` on missing/unknown token, `403` if `X-Grain-Hostd-Name` disagrees with the token's registered name).
3. Whenever `ConnectIdentity.Remote` is `true`, `ownerRefusal` (`pkg/hostd/connect_identity.go`, `pkg/hostd/remote.go`) MUST:
   - refuse with `401 Unauthorized` when `ConnectIdentity.Principal` is empty;
   - refuse with `403 Forbidden` when the matched hostd has no configured `owner` (empty `Hostd.Owner` on a registry entry, or unset `-hostd-token-owner` / `$GRAIN_HOSTD_TOKEN_OWNER` on the deprecated shared token);
   - refuse with `403 Forbidden` when `!strings.EqualFold(ConnectIdentity.Principal, owner)`.
4. Connections with `ConnectIdentity.Remote == false` (and connections over the local Unix socket `/run/grain-hostd/hostd.sock`) MUST NOT require an `owner` match.

**Pinned by:**
- `pkg/hostd/connect_identity_test.go`: `TestOwnerRefusal`, `TestIDTokenEmail`
- `pkg/ui/hostd_owner_test.go`: `TestRemoteHostdMustConnectAsItsOwner`, `TestRemoteSharedTokenHostdMustConnectAsItsOwner`
- `pkg/ui/hostds_test.go`: `TestHostdRegistryRoutes`, `TestRevokeHostdDisconnectsSessionAndClearsNotification`, `TestCreateHostdRefusesTheSharedTokensName`
- `pkg/hostd/registry_linux_test.go`: `TestManyHostdsAreConnectedAtOnceNamedByTheirTokens`, `TestAHostdWhoseNameDisagreesWithItsTokenIsRefused`
- `pkg/cli/hostds_test.go`: `TestHostdsRegisterAndSetOwner`
- `pkg/hostd/iap_auth_test.go`: `TestIAPTokenProviderFromMetadataServer`, `TestIAPTokenProviderRefreshesNearExpiry`, `TestIAPTokenProviderFallbackToADC`, `TestIAPTokenProviderEmptyAudience`, `TestParseTokenExpiry`

---

## 9. Diagnostic Logging of Refused Assertions and Principals

### 9.1 Bounded deduplicated logging of refused assertions and principals {#AU-19}

1. When `X-Goog-IAP-JWT-Assertion` fails verification, `logRefusedAssertion` (`pkg/ui/assertion_diagnostics.go`) MUST log the unverified `iss` claim, unverified `aud` claim, configured audience(s), and verification error, deduplicating log entries by `(iss, aud, refusalKind(err))` across the nine refusal kinds (`malformed`, `signature`, `unverifiable`, `expired`, `not-yet-valid`, `issued-in-future`, `issuer`, `claim-missing`, `other`) and capping the deduplication table at `64` entries (`maxLoggedRefusedAssertions`). When the unverified `aud` already matches one of the configured audiences and `refusalKind(err) != "issuer"`, the log line MUST note that the audience is this deployment's own rather than instructing the operator to change `-ui-iap-audience`.
2. Raw JWT strings and cryptographic signatures MUST NOT be written to logs.
3. If `X-Goog-IAP-JWT-Assertion` is present on a request when `IAPConfig.Enabled()` is `false`, the server MUST log a one-time warning naming `-ui-iap-audience`.
4. When a non-IAP `IdentityVerifier` fails verification, `logRefusedVerifierHeader` MUST log the failure at most once per `(verifier.Name(), header)` pair, capped at `64` entries.
5. When `checkAllowedPrincipal` refuses a verified caller (`403`), `logRefusedPrincipal` (`pkg/ui/allowlist.go`) MUST log the refusal at most once per `(normalizeIdentityEmail(id.Email), isGCPKeyPool)` up to `64` entries, emitting a dedicated warning naming `iap_members` and key rotation when `id` is a `gcp-key` sandbox pool account.

**Pinned by:**
- `pkg/ui/refused_assertion_log_test.go`: `TestRefusedAssertionLoggedOncePerKindNotOncePerAudience`, `TestRefusedAssertionForOwnAudienceDoesNotSayToSetTheAudience`
- `pkg/ui/verifier_allowlist_test.go`: `TestRefusedPrincipalsAreLoggedOnce`
- **Untested:** Saturation behaviour past `maxLoggedRefusedAssertions` (`64`), the one-time warning when `X-Goog-IAP-JWT-Assertion` arrives with no audience configured, and `logRefusedVerifierHeader` deduplication for non-IAP verifiers.

---

## 10. Plaintext Identity Headers and Sandbox Isolation

### 10.1 Never trusting plaintext identity headers {#AU-20}

1. Plaintext identity headers—`X-Goog-Authenticated-User-Email`, `X-User-LDAP`, `X-Forwarded-User`, `X-Goog-Authenticated-User-Id`, `X-Grain-User`, and `Grain-User` (`PlaintextIdentityHeaders`, `pkg/ui/principal.go`)—MUST NEVER authenticate a request, bypass `singleTenantGate`, or populate `Principal` / `RequestActor`, regardless of configuration or whether the request arrives from loopback or the network.
2. When any header in `PlaintextIdentityHeaders` other than grain's own client headers (`X-Grain-User`, `Grain-User`, `X-Forwarded-User`) is present on an unverified request, `logIgnoredIdentityHeader` MUST log a one-time notice per header name stating that the header was ignored.

**Pinned by:**
- `pkg/ui/principal_test.go`: `TestExtractPrincipalNamesNobodyFromAnIdentityHeader`, `TestExtractPrincipal_IAPMode_ForgedPlaintextHeaders`
- `tests/security/identity_test.go`: `TestPlaintextIdentityHeadersIgnoredFromUntrustedSource`, `TestPlaintextIdentityHeadersIgnoredFromLoopbackByDefault`, `TestIAPModeIgnoresPlaintextIdentityHeaders`, `TestForgedIAPAssertionDoesNotFallBackToHeaders`
- `tests/security/single_tenant_test.go`: `TestASingleTenantDaemonRefusesWhoeverAProxyNames`
- **Untested:** Once-per-header deduplication and client-header suppression in `logIgnoredIdentityHeader`.

### 10.2 In-sandbox endpoints isolated from proxy assertions {#AU-21}

1. In-sandbox agent surfaces—the git and sandbox HTTP proxy (`127.0.0.1:9728`) and `grain mcpserver` (`X-Grain-Acting-Task`)—MUST authenticate exclusively with per-attempt scoped sandbox credentials and MUST NOT admit or attribute actions to a human or service principal from proxy assertion headers.

**Pinned by:**
- `tests/security/gitproxy_test.go`: `TestGitProxyScopesEachSandboxToItsOwnRepositories`
- `pkg/daemon/sandbox_namespace_test.go`: `TestSandboxGrainLogReadIsAuditedAsItsTask`
- `pkg/cli/daemon_token_ordering_test.go`: `TestSandboxTokenMintedAfterGitProxyStartsAuthenticates`

---

## 11. Delegated (On-Behalf-Of) Identity via the Registry

Design: [`authentication.design.md`](../../design/authentication.design.md) §6 and [`hub.design.md`](../../design/hub.design.md) *Delegated identity*.

### 11.1 `act` claim parsing and `VerifiedIdentity.Via` {#AU-22}

1. `ui.VerifiedIdentity` MUST gain `Via *DelegatingApp` (`Email`, `Name`, `Grant`, `Access`). Only a verifier that carries delegation (core: `registry`) MAY set it. `NewIAPVerifier` MUST leave it nil.
2. The `registry` verifier MUST accept an `act` claim only when:
   - it is a JSON object with a non-empty `sub` that `verifiedIdentityForEmail` classifies as `model.PrincipalService`;
   - `act.kind == "service"`;
   - `act.access` is `read` or `write`;
   - `act` contains no nested `act`; and
   - the top-level `kind == "human"`, with a top-level `sub`/`email` that classifies as `model.PrincipalHuman`.
3. Any other present `act` MUST fail verification with `401` (`writeAssertionRefused`), with no fallback to the access code, a `gma_…` token, a session cookie or an open route (`AU-2`).
4. All other `registry` verifier checks MUST be unchanged for delegated assertions: `iss`, a single `aud` equal to `-ui-hub-audience`, `ES256`, `exp - iat <= 5 min` and 30 s skew.

**Pinned by:** `pkg/ui/hub_verifier_test.go`: `TestRegistryVerifierExposesPersonAndDelegatingApp`, `TestRegistryVerifierRefusesMalformedOrNestedAct`.

### 11.2 `delegate:` allowlist entries {#AU-23}

1. `allowed_principal` MUST accept a third prefix, `delegate:<email>`, extending `AU-6`'s grammar.
   - `<email>` MUST be a lower-cased service-account email (`.gserviceaccount.com`, or the wrapper verifier's service classification).
   - The entry MAY carry a `label` matching `[a-z][a-z0-9-]{0,31}`.
   - It MUST carry a non-empty `reason`, stored in `allowed_principal.reason` and `config_audit`.
2. A `delegate:` entry MUST match only `VerifiedIdentity.Via.Email`. It MUST NOT admit a direct caller, and a `serviceAccount:`, `user:`, `group:` or `domain:` entry MUST NOT satisfy the delegate check.
3. Adding or changing a `delegate:` entry MUST be refused (`403`) to every agent, console session and `gma_…` token (`AU-6.4`) and to every delegated request (`AU-25`). Only a verified human caller holding `access:write`, or an access-code admin, may do it.
4. A `delegate:` entry's role MUST NOT resolve to any scope for which `model.IsDangerousAPIScope` is true, to `access:write`, or to an admin-only permission (`422` on `POST`/`PATCH /api/access/principals`).
5. `MigrateAllowedPrincipals` MUST never import `delegate:` entries from `-ui-allowed-principals`.
6. A `delegate:` entry naming a `gcp-key` pool account MUST be refused (`422`) and never match (`AU-8`).

**Pinned by:** `pkg/ui/access_principals_test.go`: `TestDelegateEntriesNeedAReasonAndAPerson`, `TestDelegateEntryRoleRefusesDangerousScopes`, `TestDelegateEntryNeverAdmitsADirectCaller`; `pkg/cli/access_test.go`: `TestAccessAllowDelegateCLI`.

### 11.3 Dual admission and refusals {#AU-24}

1. For a request whose `VerifiedIdentity.Via != nil`, `singleTenantGate` MUST admit only when both checks pass:
   - `checkAllowedPrincipal` admits `{Email, Kind}` (the person) under `AU-6`–`AU-9`; and
   - a `delegate:` entry matches `Via.Email`.
2. When the person is refused, the response MUST be the existing `AU-15` refusal naming the person.
3. When the person passes but no `delegate:` entry matches, the response MUST be `403`. A browser navigation gets a page stating that `<label>` is not allowed to act for people on this grain. JSON callers get `{"code": "delegate_not_allowed", "delegatingApp": "<email>"}`.
4. A refused delegated request MUST be logged once per `(person, app)` under the `AU-19` bound.

**Pinned by:** `pkg/ui/delegation_test.go`: `TestDelegatedRequestNeedsPersonAndDelegateEntry`, `TestDelegateNotAllowedRefusalNamesTheApp`.

### 11.4 Role intersection and person-only operations {#AU-25}

1. A delegated request's effective scopes MUST be, on every route and on every request, the intersection of:
   - the person's current role (`AU-6.5`; `rbac.spec.md` `RB-4`); and
   - the matching `delegate:` entry's role.
   This uses the `RB-13` path with `OnBehalfOf` set to the person. A denial MUST include `delegatedFrom: <person>` and `delegatingApp: <app email>`.
2. When `Via.Access == "read"`, every method other than `GET` and `HEAD` MUST be refused with `403` (`code: "read_only_delegation"`).
3. The following MUST be refused with `403` (`code: "requires_person"`) for every delegated request, whatever both roles allow:
   - `POST`, `PATCH` and `DELETE /api/access/principals`;
   - minting or rotating a `gma_…` token that grants a dangerous or `requires_person` scope (`AU-11`);
   - approving a task or proposal that holds an `IsAdminOnlyGrant` grant;
   - `POST /api/hostds`.
4. `hostdConnectIdentity` MUST refuse with `403` any connection whose verified identity has `Via != nil` (`AU-17`, `AU-18`).

**Pinned by:** `pkg/ui/delegation_test.go`: `TestDelegatedRoleIsPersonIntersectDelegate`, `TestDemotingThePersonStopsDelegatedWrites`, `TestReadOnlyDelegationRefusesWrites`, `TestPersonOnlyOperationsRefuseDelegation`; `pkg/hostd/connect_identity_test.go`: `TestDelegatedIdentityNeverOwnsAHostd`.

### 11.5 Attribution, labels, and trailers {#AU-26}

1. For a delegated request, `ui.RequestActor(ctx)` MUST return `model.Attribution` with:
   - `Actor = {Kind: PrincipalService, ID: Via.Email, Source: "registry"}`;
   - `OnBehalfOf = &{Kind: PrincipalHuman, ID: Email, Source: "registry"}`.
   Every mutating route MUST store both (`TestEveryMutatingRouteIsAttributed` extended).
2. `ui.PrincipalLabel` MUST render that pair as `<person email> (via <label>)`. `<label>` is the matching `delegate:` entry's `label`, or else the local part of `Via.Email`.
3. `identityTrailers` MUST write the person's email in `Requested-by:` for a delegated task filing and in `Approved-by:` for a delegated approval. It MUST NOT write a service principal as a person.
4. `GET /api/auth/user` (and `GET /api/whoami` when it ships) MUST report `actor: "service:<app email>"`, `onBehalfOf: "user:<person>"` and `via: "<label>"`.
5. The grain's request log MUST record `registry_rid`, `registry_jti` and `delegation_grant` (`act.grant`) for each delegated request.

**Pinned by:** `pkg/ui/principal_label_test.go`: `TestPrincipalLabelRendersPersonViaApp`; `pkg/ui/delegation_test.go`: `TestDelegatedWriteRecordsAppAndPerson`; `pkg/orchestrator/worktree_test.go`: `TestDelegatedTaskTrailersNameThePerson`.

### 11.6 Unchanged CSRF, access-code, break-glass, and plain-service rules {#AU-27}

1. `checkCrossOrigin` and `checkVerifiedIdentityCSRF` (`AU-14`) MUST apply to delegated requests unchanged.
2. The `registry` verifier MUST keep `AccessCodeWins() == false` (`AU-10`). A request carrying a delegated assertion MUST NOT be admitted by an access code or a `gma_…` token instead of the assertion.
3. Unproxied-loopback break-glass (`AU-13`) and the mandatory tunnel-ingress gate on `connected` grains MUST be unchanged. The latter requires a valid assertion on every tunnelled request and never accepts access codes over the tunnel.
4. A `registry` assertion with `kind: "service"` and no `act` MUST be admitted only by an explicit `serviceAccount:` entry (`AU-6`, `AU-7`), and MUST be attributed to the service principal alone with no `OnBehalfOf`.

**Pinned by:** `pkg/ui/delegation_test.go`: `TestDelegationLeavesCSRFAndBreakGlassUnchanged`, `TestPlainRegistryServicePrincipalNeedsServiceAccountEntry`.

---

## 12. Requirement Traceability Summary

| ID | Requirement | Status on `main` | Primary tests / coverage notes |
| --- | --- | --- | --- |
| `AU-1` | `IdentityVerifier` seam and active ordering | Implemented | `TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders`, `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes` |
| `AU-2` | First-present-header evaluation and fail-closed `401` | Implemented | `TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders`, `TestUnverifiedAssertionGetsAPageForABrowserAndJSONForTheAPI`, `TestForgedIAPAssertionDoesNotFallBackToHeaders` |
| `AU-3` | IAP audience, issuer, and key fetching/caching | Implemented | `TestIAPAssertionVerification`, `TestIAPAssertionKeysAreFetchedFromTheEndpoint` (*JWKS/RSA/refetch-floor/stale-fallback branches untested*) |
| `AU-4` | Signature, pinned `alg`, `iss`, `aud`, and `30s` skew | Implemented | `TestIAPAssertionVerification`, `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`, `TestRefusedAssertionLoggedOncePerKindNotOncePerAudience` |
| `AU-5` | Email normalization, `PrincipalService`, `Source`, `RequestActor` | Implemented | `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts`, `TestPrincipalLabel`, `TestPrincipalVerified`, `TestEveryMutatingRouteIsAttributed`, `TestAuthUserReportsHowTheCallerAuthenticated` |
| `AU-6` | Database `allowed_principal` grammar, live API/CLI, and one-time flag migration | Implemented | `TestAllowedPrincipalsFlagMigrationHappensExactlyOnce`, `TestAllowedPrincipalsLiveEnforcementEqualPrivilegesAndIdempotency`, `TestAllowedPrincipalsAgentScopeAndSandboxIsolation`, `TestAccessAllowListDenyCLI` |
| `AU-7` | Direct entry matching (`user:`, provisioned `serviceAccount:`) | Implemented | `TestAllowedPrincipalsDefaultsAndCloudIdentityGroups`, `TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts` |
| `AU-8` | Provisioned daemon SAs and `gcp-key` sandbox pool exclusion | Implemented | `TestAllowedPrincipalsDefaultsAndCloudIdentityGroups`, `TestGCPKeyPoolAccountsAreNotAdmittedByDefault`, `TestCheckUIAllowedPrincipalsRefusesGCPKeyPoolAccounts`, `TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken` |
| `AU-9` | Human-only optional Cloud Identity `group:` lookup, `5m` cache, `10s` timeout | Implemented | `TestAllowedPrincipalsDefaultsAndCloudIdentityGroups`, `TestGroupAllowlistRefusesWhenTokenEndpointNeverAnswers`, `TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken` |
| `AU-10` | Per-verifier `AccessCodePrecedence` | Implemented | `TestFakeVerifierContractPinsBothAccessCodePrecedenceModes`, `TestRequireIAPKeepsTheCodeForAVerifierWhoseCodeWins`, `TestRequireVerifierKeepsTheCodeForAVerifierWhoseCodeWins` (*`gma_` + assertion combination untested*) |
| `AU-11` | Dangerous `gma_` scope minting restricted to `PrincipalHuman` | Implemented | `TestExternalAgentDangerousScopeRequiresPerson`, `TestUnlistedServicePrincipalCannotMintDangerousExternalAgentToken` |
| `AU-12` | `-ui-require-verifier` (`-ui-require-iap`) startup active-verifier validation | Implemented | `TestCheckConfigRefusesRequireIAPWithoutAnAudience`, `TestRequireIAPFromTheEnvironment`, `TestCheckConfigRequireVerifier`, `TestRequireVerifierFromTheEnvironment` |
| `AU-13` | `-ui-require-verifier` (`-ui-require-iap`) network refusal and loopback break-glass | Implemented | `TestRequireIAPRefusesTheAccessCodeOverTheNetwork`, `TestWithoutRequireIAPTheCodeStillWorksOverTheNetwork`, `TestRequireVerifierRefusesAccessCodeOverNetworkAndKeepsLoopbackBreakGlass`, `TestRequireVerifierIAPMatchesRequireIAPAlias`, `TestAccessCodeNotesAServerThatRequiresIAP`, `TestAccessCodeNotesAServerThatRequiresVerifier` |
| `AU-14` | Global and verified-identity CSRF checks + IAP logout | Implemented | `TestCrossOriginStateChangeRefused`, `TestVerifiedIdentityCSRFLogoutAndUnchangedNoIAPPath`, `PageHeader.test.jsx`, `AccessSection.test.jsx`, `iap-header-logout.spec.js`, `phone-signed-in-logout.spec.js` |
| `AU-15` | `403` `"Not allowed on this grain"` page and `PrincipalAdvice` | Implemented | `TestRefusedPrincipalGetsAPageNamingThemWithTheAccessCodeForm`, `TestRefusedPrincipalPageSaysTheAssertionTakesPrecedence`, `TestRefusedPrincipalPageUsesRegisteredAdvice` |
| `AU-16` | `401` `"Sign-in could not be verified"` and `"Sign in through <verifier>"` pages | Implemented | `TestUnverifiedAssertionGetsAPageForABrowserAndJSONForTheAPI`, `TestRequireIAPRefusesTheAccessCodeOverTheNetwork`, `TestRequireVerifierRefusesAccessCodeOverNetworkAndKeepsLoopbackBreakGlass` |
| `AU-17` | Local vs remote `hostd` classification and allowlist gate | Implemented | `TestRemoteHostdMustConnectAsItsOwner`, `TestHostdConnectEndpoint`, `TestASingleTenantDaemonAcceptsARemoteHostd` |
| `AU-18` | `X-Grain-Hostd-Token`, `IAPTokenProvider`, and remote owner match | Implemented | `TestOwnerRefusal`, `TestRemoteHostdMustConnectAsItsOwner`, `TestRemoteSharedTokenHostdMustConnectAsItsOwner`, `TestHostdRegistryRoutes`, `TestHostdsRegisterAndSetOwner`, `TestIAPTokenProviderFromMetadataServer` |
| `AU-19` | Bounded deduplicated logging of refused assertions and principals | Implemented | `TestRefusedAssertionLoggedOncePerKindNotOncePerAudience`, `TestRefusedAssertionForOwnAudienceDoesNotSayToSetTheAudience`, `TestRefusedPrincipalsAreLoggedOnce` (*64-cap saturation, unconfigured-audience warning, and `logRefusedVerifierHeader` untested*) |
| `AU-20` | Plaintext identity headers never trusted | Implemented | `TestExtractPrincipalNamesNobodyFromAnIdentityHeader`, `TestPlaintextIdentityHeadersIgnoredFromUntrustedSource`, `TestIAPModeIgnoresPlaintextIdentityHeaders`, `TestASingleTenantDaemonRefusesWhoeverAProxyNames` (*`logIgnoredIdentityHeader` dedup untested*) |
| `AU-21` | In-sandbox listeners isolated from proxy assertions | Implemented | `TestGitProxyScopesEachSandboxToItsOwnRepositories`, `TestSandboxGrainLogReadIsAuditedAsItsTask`, `TestSandboxTokenMintedAfterGitProxyStartsAuthenticates` |
| `AU-22` | Registry `act` claim parsing and `VerifiedIdentity.Via` | Implemented | `TestRegistryVerifierExposesPersonAndDelegatingApp`, `TestRegistryVerifierRefusesMalformedOrNestedAct` |
| `AU-23` | `delegate:` allowlist entries (reason, person-only, no dangerous scopes) | Implemented | `TestDelegateEntriesNeedAReasonAndAPerson`, `TestDelegateEntryRoleRefusesDangerousScopes`, `TestDelegateEntryNeverAdmitsADirectCaller`, `TestAccessAllowDelegateCLI` |
| `AU-24` | Dual admission (person and `delegate:` entry) and refusals | Implemented | `TestDelegatedRequestNeedsPersonAndDelegateEntry`, `TestDelegateNotAllowedRefusalNamesTheApp` |
| `AU-25` | Person ∩ delegate role intersection, read-only grants, person-only operations, `hostd` refusal | Implemented | `TestDelegatedRoleIsPersonIntersectDelegate`, `TestDemotingThePersonStopsDelegatedWrites`, `TestReadOnlyDelegationRefusesWrites`, `TestPersonOnlyOperationsRefuseDelegation`, `TestDelegatedIdentityNeverOwnsAHostd` |
| `AU-26` | `Actor` = app, `OnBehalfOf` = person, `(via <app>)` label, trailers, request log | Implemented | `TestPrincipalLabelRendersPersonViaApp`, `TestDelegatedWriteRecordsAppAndPerson`, `TestDelegatedTaskTrailersNameThePerson` |
| `AU-27` | CSRF, access-code, break-glass, and plain-service rules unchanged under delegation | Implemented | `TestDelegationLeavesCSRFAndBreakGlassUnchanged`, `TestPlainRegistryServicePrincipalNeedsServiceAccountEntry` |

---

## 13. Sources

- Design document: [`design/design/authentication.design.md`](../../design/authentication.design.md) (§6 for `AU-22`–`AU-27`, with [`design/design/hub.design.md`](../../design/hub.design.md) *Delegated identity*)
- Companion designs & specs: [`design/design/operator-attribution.design.md`](../../design/operator-attribution.design.md), [`design/spec/operator-attribution/operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md), [`design/design/hub.design.md`](../../design/hub.design.md), [`design/design/simple-config.design.md`](../../design/simple-config.design.md), [`docs/access-code.md`](../../../docs/access-code.md)
- Walkthrough evidence: [`docs/test-runs/2026-11-02-verified-identity.md`](../../../docs/test-runs/2026-11-02-verified-identity.md), [`docs/test-runs/2026-11-02-iap-end-to-end.md`](../../../docs/test-runs/2026-11-02-iap-end-to-end.md), [`docs/test-runs/2026-11-03-iap-deployment.md`](../../../docs/test-runs/2026-11-03-iap-deployment.md)
- Implementation packages: `pkg/ui` (`verifier.go`, `principal.go`, `assertion_diagnostics.go`, `allowlist.go`, `accesscode.go`, `csrf.go`, `principalrefusal.go`, `auth.go`, `server.go`, `request_actor.go`, `principal_label.go`, `external_agents.go`, `httpclient.go`, `hostd_connect.go`, `hostds.go`), `pkg/model/task.go`, `pkg/hostd` (`connect_identity.go`, `remote.go`, `dialout.go`, `iap_auth.go`), `pkg/daemon/ui_server.go`, `pkg/cli` (`daemon.go`, `cliconn.go`, `accesscode.go`, `hostd.go`, `hostds.go`)
