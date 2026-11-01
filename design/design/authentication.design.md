# Authentication: IAP as a first-class sign-in

A grain controller on GKE sits behind Google Cloud Identity-Aware Proxy (`design/design/grain-on-gke.design.md`, *UI and API ingress*). IAP already knows who every caller is. Grain does not use that. This doc proposes that a verified IAP assertion authenticates the caller on its own, as a named principal, for the browser, the `grain` CLI, external agents and `grain hostd`. The access code stays for deployments without IAP.

## What exists today

- **The access code is the only credential.** `pkg/ui/accesscode.go`: the single-tenant daemon's one secret is `local-api-token`. A browser trades it for the `grain_access` session cookie on the "Enter access code" page. The CLI (`pkg/cli/accesscode.go`, `pkg/cli/cliconn.go`) sends it as `Authorization: Bearer`. `singleTenantGate` lets nobody else in, loopback included (`docs/access-code.md`). Callers that have it run as the deployment's own actor (`AuthMethodTrusted`), and nobody is named.
- **IAP assertions are verified, then thrown away.** With `-ui-iap-audience` / `GRAIN_IAP_AUDIENCE` set (plus `-ui-iap-issuer` and `-ui-iap-keys-url`), `identifyCaller` in `pkg/ui/principal.go` checks `X-Goog-IAP-JWT-Assertion` against IAP's keys, issuer, audience and expiry. `extractPrincipal` then drops the person it names. `singleTenantRefusesIdentity` uses the verified assertion only to refuse an API call that has no access code, with a better error (`singleTenantProxiedRefusal`).
- **Programmatic IAP clients already exist.** `pkg/modelexec/iap_auth.go` (`IAPTokenProvider`) mints ID tokens for the IAP audience, so executors and hostd can get through the Gateway. They then present an executor or hostd token in the hello frame.
- **Seams for wrapper builds.** A wrapper distribution (such as Google's internal build) imports this module and extends it only through registries (`pkg/change.Register`, `ui.RegisterHostFamily`) and through `IAPConfig.Validator`. Nothing lets it plug in a different signed-header proxy.

Behind IAP, this means every teammate has to share a second password on top of their Google login. Every action is attributed to one anonymous holder of that code. Setting up the CLI, hostd and external agents needs both an IAP token and the access code.

## Proposal

### 1. A verified assertion authenticates the caller

When IAP authentication is enabled (`-ui-iap-audience` set), a request whose `X-Goog-IAP-JWT-Assertion` verifies is let through by `singleTenantGate` without the access code. The checks are signature against the configured keys, `iss`, `aud` (one of the configured audiences, which pins the backend service) and `exp`/`iat` with a small skew.

The rules:

- **A bad assertion is final.** If the assertion is present but fails to verify, the request is refused (401, logged by `logRefusedAssertion`). It does not fall back to the access code or to an anonymous route. A request that bypasses the Gateway and reaches the pod directly cannot forge the assertion, so the network path no longer matters.
- **Plaintext identity headers never authenticate.** This includes `X-Goog-Authenticated-User-Email`. They stay logged and ignored, as today.
- **The assertion wins over the access code.** If a request carries both, the caller is the IAP principal. The access code does not make a named person anonymous again.

### 2. Who is admitted: an allowlist on top of IAP

IAP's IAM binding (managed by `pkg/iapreconciler`) is the perimeter. Grain adds an optional check of its own, so a mistake in the project's IAM does not hand the deployment to everyone in the organisation:

```
-ui-allowed-principals=user:alice@example.com,group:grain-admins@example.com,serviceAccount:hostd@proj.iam.gserviceaccount.com,domain:example.com
```

- **Default.** The deployment owner (the email `grain init` records for the operator) plus the service accounts grain itself provisions. A new deployment admits only its owner until somebody widens the list.
- **`group:`.** IAP assertions carry no group claims. A `group:` entry is resolved through the Cloud Identity groups API, with the result cached for a few minutes. If the lookup fails, the request is refused. A deployment that does not want that lookup leaves groups to IAP's own binding and lists only `user:`, `serviceAccount:` and `domain:` entries.
- **Same powers for everyone.** Grain is single-tenant (`docs/access-code.md`): every admitted principal gets the access the access code gives today. Roles, such as read-only viewers, are out of scope here (*Open questions*).

A verified caller that is not on the list gets a 403. The response names the email, so the operator knows what to add.

### 3. The verified email is the principal

The `email` claim becomes the request's principal, with `AuthMethodIAP`:

- `{human, alice@example.com}` for a person;
- `{service, hostd@proj.iam.gserviceaccount.com}` for an address ending in `.gserviceaccount.com`. This needs a new `model.PrincipalService` kind. Today there are only `automation`, `agent` and `human`.

`extractPrincipal` stops dropping human principals that came from a verifier. Task creation, approvals, comments, merge-queue submissions and audit logs then record who acted, rather than the deployment's actor. `GET /api/auth/user` (`pkg/ui/auth.go`) already returns `authMethod` and `user`, so the UI header shows the signed-in email. This is the identity `attribution.design.md` needs to credit reviewers who act through grain.

## How each client signs in

### Browser UI

Opening the grain URL goes through IAP's Google sign-in, as it does now. Each request then carries the assertion, so the "Enter access code" page is never shown and grain sets no `grain_access` cookie. Logging out sends the browser to IAP's `/_gcp_iap/clear_login_cookie`.

Every request is now authenticated by a cookie the browser sends on its own: IAP's cookie, which grain does not control and which is not `SameSite=Strict` the way `grain_access` is. So a state-changing request (any method other than GET or HEAD, or anything under `/api/` that writes) that was authenticated by IAP must also pass a CSRF check. It must carry `Sec-Fetch-Site: same-origin`, or a matching `Origin`. The UI's fetches already meet this.

### `grain` CLI and external agents

The CLI gets an IAP mode, configured in `/etc/grain/cli.env` or by environment (`GRAIN_SERVER=https://grain.example.com`, `GRAIN_IAP_AUDIENCE=<iap-client-id>`). It gets an ID token from the first of these sources that works:

1. `gcloud auth print-identity-token --audiences=<iap-client-id>`, for a service account or an impersonated one;
2. `gcloud auth print-identity-token` with the user's own login. This works only if gcloud's OAuth client is on the IAP resource's programmatic-access allowlist. Otherwise the CLI's own OAuth desktop/device flow is used, with the token cached under `~/.config/grain/`;
3. the metadata server or Workload Identity, through `modelexec.IAPTokenProvider`, which already caches and refreshes these tokens.

The CLI sends the token as `Proxy-Authorization: Bearer <id-token>`. IAP accepts it there and leaves `Authorization` to the application. Grain authenticates the caller from the assertion IAP adds, never from the bearer token. This leaves `Authorization` free for a grain credential when one is wanted.

An external agent can do the same with its own service account. It can also present a minted grain API token (`gma_…`, `pkg/ui/external_agents.go`) in `Authorization`, as today. That token still decides the principal (`{agent, external:<name>}`) and its scopes, and the IAP identity that carried it is recorded alongside in the audit log. `grain access-code` reports that the code is not needed when the CLI is in IAP mode.

### `grain hostd`

hostd dials `/api/hostd/connect` through the Gateway with an ID token from its service account (Workload Identity or a key on the workstation) or from the user's login. It uses the token sources above, through `IAPTokenProvider`, as it does now. The hello frame still carries the hostd token. That token names which hostd this is, and it is what GKE-5 makes per hostd.

What changes is that the verified IAP principal is recorded as the connection's principal and checked against the allowlist. Optionally, it can also be checked against the hostd row's `owner`, so a stolen hostd token is useless without its owner's Google identity. The executor bootstrap routes (`executorBootstrapRoute`) stay open, as `docs/access-code.md` describes.

## Fallback and the `IdentityVerifier` seam

### Without IAP

- **No IAP configured.** Local and standalone deployments are unchanged. The access code, the login page and `grain_access` work as they do now.
- **`-ui-require-iap`.** This replaces the unused `Config.IAPMode`. With it set, the network listener no longer accepts the access code at all, neither the bearer token nor the cookie, and the login page is gone. The code still works over loopback when the request carries no forwarding headers. That keeps `kubectl exec deploy/grain -- grain …` as a break-glass path that does not depend on IAP.
- **IAP configured but `-ui-require-iap` not set.** Both are accepted. This is the migration state.

### `IdentityVerifier`

IAP becomes one implementation of a seam in `pkg/ui`, so that a wrapper build can add a different signed-header proxy. A wrapper build fronted by its own corporate proxy, with a companion design doc in that wrapper's repository, plugs in through this seam rather than by patching the gate.

```go
// An IdentityVerifier turns a signed header a trusted proxy adds into a verified identity.
type IdentityVerifier interface {
	Name() string     // "iap", or a wrapper's proxy; becomes the AuthMethod
	Headers() []string // the headers it reads; also treated as proxy headers (relayedByProxy)
	// Verify returns ok=false when none of its headers is present, and an
	// error when one is present but does not verify.
	Verify(r *http.Request) (id VerifiedIdentity, ok bool, err error)
}

type VerifiedIdentity struct {
	Email string              // the principal's ID
	Kind  model.PrincipalKind // human or service
}

func RegisterIdentityVerifier(v IdentityVerifier) (unregister func())
```

`identifyCaller` asks each registered verifier in turn, with IAP registered by default when its audience is set. The first verifier whose header is present decides the request. Its error refuses the request; it never falls through to the next verifier. Whatever identity comes out goes through the same allowlist, CSRF check, principal attribution and audit logging, so a wrapper only supplies the cryptography. `IAPConfig.Validator` becomes the IAP verifier's own option. A test in this tree, using a fake verifier, pins the contract the wrapper relies on.

### In-sandbox calls do not change

Agent calls from inside a sandbox never pass IAP and never carry a person's identity. That covers the git and API proxy at `127.0.0.1:9728` and `grain mcpserver` with `X-Grain-Acting-Task`. They keep the per-attempt scoped token model they have now. The sandbox acceptor is not on the Gateway, and assertion headers arriving on it are ignored.

## Rollout

1. `IdentityVerifier` and the registry, with IAP as the first implementation. No behaviour change.
2. Verified IAP callers pass `singleTenantGate` and become principals: `PrincipalService`, the allowlist, the CSRF check, and the UI header and logout.
3. IAP mode for the CLI, and recording hostd's IAP principal.
4. `-ui-require-iap`. Then update `docs/access-code.md`, which says grain does not check the assertion.

## Open questions

- Should `group:` entries be resolved by grain (which needs Cloud Identity read access for `grain-daemon@…`), or left entirely to IAP's IAM binding?
- Are roles needed now? For example, a viewer who can read but not approve or submit. Or is "everyone admitted is an operator" acceptable for v1?
- Should a hostd connection have to match its row's `owner`, or should the hostd token alone keep naming the hostd?
- Should a principal admitted through IAP be able to mint `gma_` tokens and hostd tokens, or only the deployment owner?
