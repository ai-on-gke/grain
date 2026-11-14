# External CLI specification

**Status:** draft for review. Normative.
**Specifies:** [`external-cli.design.md`](../../design/external-cli.design.md)
**Related:** `design/spec/cli/` (the full host and in-sandbox `grain` CLI command/flag specification), [`sandbox-first-agent-surface.design.md`](../../design/sandbox-first-agent-surface.design.md), [`grain-namespace.design.md`](../../design/grain-namespace.design.md), [`operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md), [`task-priorities.spec.md`](../task-priorities/task-priorities.spec.md), [`docs/agent-api.md`](../../../docs/agent-api.md), [`docs/access-code.md`](../../../docs/access-code.md)

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHOULD**, **SHOULD NOT**, and **MAY** in this document are to be interpreted as described in RFC 2119.

This specification defines the **external-agent workflow** over the `grain` CLI (`pkg/cli`, `cmd/grain`), the server endpoints and authorization rules that back it (`pkg/ui`, `pkg/model`, `pkg/namespace`), and its workstation onboarding surfaces (`grain skill`, `grain mcp`, Settings → CLI, and `/cli/install.sh`). Baseline command and flag syntax already shipped across the host and in-sandbox `grain` CLI belongs to `design/spec/cli/` as the single command-reference source of truth; sections 5–7 below specify only the workflow deltas, API contracts, and external-caller semantics required beyond baseline CLI behaviour.

---

## 1. Configuration, contexts, and server resolution

### 1.1 Named contexts file {#XC-1}

The CLI **MUST** store named deployment contexts in `$XDG_CONFIG_HOME/grain/config.json` when `$XDG_CONFIG_HOME` is non-empty, and in `~/.config/grain/config.json` otherwise.

1. When writing `config.json`, the CLI **MUST** create parent directories with mode `0700` and write `config.json` atomically with mode `0600`.
2. The top-level JSON object **MUST** have the shape:
   ```json
   {
     "current": "team-prod",
     "contexts": {
       "team-prod": {
         "server": "https://grain.team.example.com",
         "auth": "external-token",
         "credential": "keyring:grain/team-prod",
         "tokenId": "eat_..."
       },
       "personal": {
         "server": "https://grain-abc123.run.app",
         "auth": "google-id-token",
         "audience": "https://grain-abc123.run.app"
       },
       "workstation": {
         "server": "http://127.0.0.1:4242",
         "auth": "local"
       }
     }
   }
   ```
3. Supported `auth` values are `"external-token"`, `"access-code"`, `"google-id-token"`, `"local"`, `"none"`, and any provider name registered via `cli.RegisterCredentialSource` ([#XC-10](#XC-10)).
4. `config.json` **MUST NOT** contain raw bearer tokens, access codes, or secret material; `credential` holds only a storage locator (`"keyring:grain/<ctx>"` or `"file:<ctx>"`).
5. When `config.json` does not exist, the CLI **MUST** proceed without error and fall through to `/etc/grain/cli.env` and the built-in default ([#XC-3](#XC-3)).

### 1.2 Context selection and `grain context` commands {#XC-2}

1. The CLI **MUST** provide `grain context list`, `grain context use <name>`, `grain context show [<name>]`, and `grain context delete <name>` (all supporting `--json`).
2. For a single invocation, the active context **MUST** resolve in order:
   1. `--context <name>` (or `-context <name>`), accepted before or after the verb;
   2. `$GRAIN_CONTEXT`, when non-empty;
   3. `"current"` in `config.json`.
3. Naming a context via `--context`, `$GRAIN_CONTEXT`, `grain context use`, `grain context show`, or `grain context delete` that is not present in `config.json` **MUST** fail with exit code `2` and name the missing context.
4. `grain context delete <name>` **MUST** remove the context entry from `config.json`, clear `"current"` if it named `<name>`, and remove any local keyring or fallback credential stored for `<name>` (without revoking server tokens; server revocation is `grain logout`, [#XC-9](#XC-9)).

### 1.3 Server URL resolution and error attribution {#XC-3}

1. The CLI **MUST** resolve the target daemon URL (`serverSource` in `pkg/cli/cliconn.go`) in strict order:
   1. `-server` / `--server` flag;
   2. `$GRAIN_SERVER` environment variable (trimmed, non-empty);
   3. `server` of the active context ([#XC-2](#XC-2));
   4. `GRAIN_SERVER` key in `/etc/grain/cli.env` (or the path in `$GRAIN_CLI_ENV` when set; setting `GRAIN_CLI_ENV=""` disables reading `/etc/grain/cli.env`);
   5. built-in default `http://127.0.0.1:4242`.
2. `/etc/grain/cli.env` (and any `$GRAIN_CLI_ENV` file) **MUST** be parsed strictly as `KEY=VALUE` lines with `#` comments and optional matching single or double quotes, and **MUST NOT** execute shell expansions or subshells.
3. Connection-refused and timeout errors (`explainDaemonError`) **MUST** name the target URL and the winning source (`-server`, `$GRAIN_SERVER`, `context <name>`, `/etc/grain/cli.env`, or `the default`), without rewriting structured HTTP error responses returned by the daemon itself.
4. In the unified `grain` binary (`cmd/grain`), `sandboxcli.MaybeRun` **MUST** yield to `pkg/cli` whenever `GRAIN_SANDBOX_CLI=0`, `$GRAIN_SERVER` is non-empty, `$GRAIN_CONTEXT` is non-empty, or `-server` / `--server` / `-context` / `--context` is passed on the command line.

---

## 2. Authentication, credentials, and `whoami`

### 2.1 Credential resolution order and bearer transport {#XC-4}

`ui.HTTPClient` and the CLI **MUST** resolve bearer credentials for outbound requests in the following order:

1. Explicit `-token` / `--token` flag (`HTTPClient.Token`, stripping any redundant leading `"Bearer "` prefix);
2. Programmatic `HTTPClient.TokenProvider` (including registered `CredentialSource` providers when selected by context);
3. Environment variables in order: `$GRAIN_TOKEN`, `$GRAIN_AUTH_TOKEN`, `$GRAIN_ID_TOKEN`, `$GRAIN_TUNNEL_TOKEN`;
4. Active context's configured `auth` source ([#XC-1](#XC-1), [#XC-7](#XC-7), [#XC-10](#XC-10)) when the request targets that context's server (skipped if `-server` or `$GRAIN_SERVER` points at a different origin);
5. Loopback local API token (`$GRAIN_LOCAL_API_TOKEN_FILE`, else `$GRAIN_DATA_DIR/local-api-token` or `/var/lib/grain/data/local-api-token`) when the server host is loopback (`localhost`, `127.0.0.1`, `::1`); if that file exists on disk but cannot be read because of permissions and the daemon answers `401 Unauthorized`, the CLI **MUST** name the unreadable token file in the error;
6. Google OIDC ID token when the target URL uses `https://`, ends in `.run.app`, or has an explicit audience (`-audience` / `--audience`, `$GRAIN_AUDIENCE`, `$GRAIN_TUNNEL_AUDIENCE`): minted via Google Application Default Credentials (`idtoken.NewTokenSource`) first, then `gcloud auth print-identity-token --audiences=<audience>`, and falling back to bare `gcloud auth print-identity-token` **only** for `*.run.app` hosts; cached in memory until 30 seconds before the JWT `exp` claim (or 55 minutes when `exp` is absent).

Resolved credentials **MUST** be sent as `Authorization: Bearer <token>` on JSON API calls, multipart attachment uploads, and binary/stream downloads alike.

### 2.2 Server-side IAP, proxy identity, and access-code authentication {#XC-5}

1. When a deployment runs behind Identity-Aware Proxy or a verified identity proxy (`ui.RegisterIdentityVerifier`, `pkg/ui/principal.go`), the server **MUST** cryptographically verify the assertion JWT before binding `Principal{Kind: PrincipalHuman, ID: <verified-email-or-sub>}`.
2. The `-as` flag and `X-Forwarded-User` header **MUST NOT** authenticate a caller or override a verified human principal, access-code principal, or external-agent token principal.
3. On a single-tenant deployment with an installation access code (`pkg/ui/accesscode.go`, `docs/access-code.md`), the server **MUST** accept `Authorization: Bearer <access-code>` from any network address and manage the installation secret via host-local `grain access-code [show|rotate]` (`pkg/cli/accesscode.go`).

### 2.3 External-agent `gma_` bearer tokens, limits, and lifecycle {#XC-6}

1. External-agent credentials use the prefix `gma_` followed by 64 lowercase hex characters (`32` random bytes). Only the SHA-256 hex digest of the raw token **MUST** be persisted (`external_agent_token` table); the raw token is returned once on creation or rotation and never returned again.
2. When `externalAgentApi` is `false` in deployment settings (the default), every request presenting a `gma_` bearer token — on either `POST /mcp` or `/api/...` — **MUST** be rejected with `403 Forbidden`.
3. When `externalAgentApi` is `true`, `gma_` bearer tokens **MUST** authenticate requests on **both** `POST /mcp` and HTTP `/api/...` routes through `agentAPIGate`, rejecting unknown, revoked, or expired tokens with `401 Unauthorized`.
4. Each credential enforces:
   - `scopeSet` and/or explicit `scopes` (supporting `<area>:<level>@<owner>/<name>` per-repo scope narrowing);
   - optional `repos` allowlist (defaulting `?repo=` automatically on single-repo collection reads when the token is restricted to exactly one repository, and requiring `?repo=` when restricted to multiple repositories);
   - per-token request rate limit (`rateLimit` req/min, default `60`) and in-flight concurrency cap (`maxConcurrent`, default `8`), returning `429 Too Many Requests` with `Retry-After` when exceeded;
   - request body cap (`MaxExternalAgentBodyBytes`).
5. Lifecycle routes on `/api/external-agents/tokens` (`GET`, `POST`, `POST /{id}/rotate`, `POST /{id}/revoke`, `DELETE /{id}`, and emergency kill switch `POST /revoke-all`) **MUST** require a `PrincipalHuman` caller whenever the requested scopes resolve to any dangerous scope or a declarative scope set with `requires_person: true`.
6. Routes assuming an in-grain sandbox or console task (`POST /api/tasks/{id}/messages`, `PUT /api/tasks/{id}/session-title`, `/api/console/*`, `/api/console-sessions*`) **MUST** be refused for external-agent principals.

### 2.4 Credential storage in OS keyring and `0600` fallback file {#XC-7}

1. When `grain login` stores a secret (`gma_` token or access code) for `<context>`, it **MUST** write to the OS keyring under service/account `grain/<context>` when a native keyring backend (freedesktop Secret Service, macOS Keychain, or Windows Credential Manager) is reachable, recording `"credential": "keyring:grain/<context>"` in `config.json`.
2. On a headless machine with no reachable keyring, `grain login` **MUST** store the secret in `$XDG_CONFIG_HOME/grain/credentials/<context>` (or `~/.config/grain/credentials/<context>`) with directory mode `0700` and file mode `0600`, recording `"credential": "file:<context>"`.
3. Before reading a file-backed credential, the CLI **MUST** `stat` the file and **MUST** refuse to read it (exit code `3`) if any group or other permission bit is set (`mode.Perm() & 0o077 != 0`), printing an actionable fix (`chmod 0600 <path>`).

### 2.5 `GET /api/auth/whoami` and `grain whoami` {#XC-8}

1. The daemon **MUST** serve `GET /api/auth/whoami`:
   - When called **unauthenticated**, it **MUST** return `200 OK` with the server's accepted authentication methods (`acceptedAuth`: subset of `["access-code", "external-token", "iap", "proxy"]`), `externalAgentApi` (`bool`), and daemon `version`, without exposing secrets, internal hostnames, or user data.
   - When called **authenticated**, it **MUST** additionally return `authenticated: true`, `principal` (`"human:<id>"`, `"agent:external:<name>"`, or `"operator"`), and — for an external-agent token — `tokenId`, `owner`, `scopeSet`, `resolvedScopes`, `repos`, and `expiresAt`.
   - When called with an **expired** `gma_` token whose hash still exists in `external_agent_token`, `GET /api/auth/whoami` (and `authorizeExternalAgentCall`) **MUST** return `401 Unauthorized` with structured code `"token_expired"` and `expiredAt` timestamp so the CLI reports `token for context <ctx> expired on <date>: run grain login --refresh`.
2. `grain whoami [--json]` **MUST** query `GET /api/auth/whoami` using the active context/server and print the context name, server URL, authenticated principal, owner (if any), scopes/scope set, repository limits, and token expiry. Over the daemon's API socket it **MUST** say so (`server: <url>, over the API socket <path>`, and `socket` in `--json`), and a principal the daemon knew by the connecting uid (`hostAccount: true` in the response) **MUST** be printed as that host account's. If the caller is unauthenticated or the credential is invalid/expired, `grain whoami` **MUST** exit `3`.

### 2.6 `grain login` and `grain logout` {#XC-9}

1. `grain login <url> [--name <ctx>] [--issue-token] [--scope-set <name>] [--repos <r1,r2>] [--expires <dur>] [--token-stdin] [--refresh] [--json]` **MUST** probe `GET /api/auth/whoami` on `<url>` and configure `<ctx>` (defaulting `<ctx>` to a slug derived from `<url>`'s hostname, and setting `"current"` to `<ctx>`):
   - **IAP / Cloud Run (`google-id-token`):** when `<url>` accepts IAP or is `*.run.app` and `--issue-token` / `--token-stdin` is not requested, `grain login` **MUST** verify that ADC or `gcloud` mints a token accepted by `GET /api/auth/whoami` (if missing, fail fast with exit `3` and `fix: "gcloud auth login"`), then record `"auth": "google-id-token"` and `"audience"` in `config.json` with no stored secret.
   - **Minting a scoped `gma_` token (`--issue-token`):** when `externalAgentApi` is `true`, `grain login --issue-token` authenticates using the caller's human session (IAP, registered proxy credential source, or stdin access code) and calls `POST /api/external-agents/tokens` (default `--scope-set grain/external-dev`, default `--expires 30d`). Any authenticated human principal (`PrincipalHuman`) **MUST** be permitted to mint a token for a non-dangerous scope set (`requires_person: false` and no dangerous scopes) restricted to repositories that human can already access, even without holding `capabilities:admin`; dangerous scopes **MUST** still be refused unless the caller is an authorized operator. `grain login` stores the returned `gma_` secret per [#XC-7](#XC-7) and `tokenId` in `config.json`. `--refresh` re-mints/rotates an expired or expiring context token using the same parameters.
   - **`$GRAIN_ID_TOKEN` is an identity, not an explicit token:** when it is the only token given and `GET /api/auth/whoami` reports a `human:` principal for a non-loopback `<url>`, `grain login` **MUST** treat it as the IAP sign-in above (a personal `gma_` token by default, `google-id-token` with `--issue-token=false` or with `externalAgentApi` off) and **MUST NOT** store it or label it as an access code. A `gma_` context minted through IAP (`"auth": "external-token"` with an `"audience"`) **MUST** send a Google ID token for that audience (`$GRAIN_ID_TOKEN`, else ADC/`gcloud`) in `Proxy-Authorization` beside the `gma_` token in `Authorization`, since IAP admits no request on a `gma_` token alone.
   - **Pre-minted token (`--token-stdin`):** reads a `gma_` token (or access code when explicitly selected) from stdin, verifies it against `GET /api/auth/whoami`, and stores it per [#XC-7](#XC-7).
   - **Single-tenant access code:** `grain login` **MUST NOT** silently write an installation access code to keyring when `gma_` tokens are enabled; if the user logs in with an access code on a deployment where `externalAgentApi` is off or access code is chosen explicitly, `grain login` **MUST** warn on stderr that the access code grants full operator authority and recommend enabling `externalAgentApi` and minting a `gma_` token.
2. `grain logout [<ctx>]` **MUST** delete the stored credential for `<ctx>` (defaulting to the active context) and, when the context holds a `tokenId` minted by `grain login`, call `POST /api/external-agents/tokens/{id}/revoke` (treating an already-revoked or unreachable server after `--local` cleanup according to flags, warning if remote revocation could not be confirmed).

### 2.7 Credential-source registry and fail-fast login redirects {#XC-10}

1. `pkg/cli` **MUST** expose `RegisterCredentialSource(name string, src CredentialSource)` allowing wrapper distributions to register custom `"auth": "<name>"` providers (such as a corporate identity-aware proxy) without placing organization-specific identifiers in core (`tests/hygiene/boundary_test.go`).
2. When a registered `CredentialSource` finds an expired or missing local sign-in session, it **MUST** fail fast with exit code `3`, naming the renewal command in both plain stderr and `--json` `error.fix`.
3. `ui.HTTPClient` **MUST** disable automatic redirect-following to sign-in pages (`CheckRedirect` consulting `authfix.ClassifyRedirect`) and **MUST** inspect responses where `application/json` was expected:
   - a `301`, `302`, `303`, `307`, or `308` redirect to a classified login host (or cross-origin sign-in redirect), or
   - a `200`/`401`/`403` response with `Content-Type: text/html` where a JSON API payload was expected,
   **MUST** fail immediately with exit code `3` and message `not signed in to <provider>: run <fix>` (or `unexpected HTML sign-in response from <url>`) rather than following the redirect or reporting a JSON parse error.

---

## 3. CLI flag parsing, output format, errors, and exit codes

### 3.1 Flag syntax and post-verb global flags {#XC-11}

1. Every external CLI command and subcommand **MUST** accept both single-dash (`-flag`) and double-dash (`--flag`) spellings for all flags.
2. Global flags `--json` (`-json`), `--context` (`-context`), `--server` (`-server`), `--token` (`-token`), `--audience` (`-audience`), `--as` (`-as`), `--connect-timeout`, and `--timeout` **MUST** be accepted **both** before the verb (`grain --json list`) and after the verb/subcommand (`grain list --json`, `grain ci show ci-42 --json`).
3. Commands that take no positional arguments or a fixed number of positional arguments **MUST** validate `fs.Args()` (`parseArgs` / `noArgs`, `pkg/cli/args.go`) so stray words or flags placed after positional arguments are either parsed cleanly (`parseArgs`) or rejected with exit code `2` and a clear usage message.

### 3.2 `--json` stdout contract and compact text default {#XC-12}

1. Every external CLI verb **MUST** support `--json`.
2. When `--json` is set, stdout **MUST** contain a single valid JSON value (object or array) using the `/api` wire field names (`camelCase` / existing API schema). New fields **MAY** be added; existing fields **MUST NOT** be renamed or change type without a versioned migration. Human hints (such as omitted closed-task counts or stale-skill notices) **MUST** go to stderr, never stdout.
3. When `--json` is not set, output **MUST** be compact plain text: one line per item on list commands with IDs first and no box-drawing borders.

### 3.3 Structured error JSON and exit codes `0..5` {#XC-13}

1. When `--json` is active and a command exits non-zero because of an error, stderr **MUST** receive a single JSON object:
   ```json
   {"error": {"code": "auth_expired", "message": "token for context team expired on 2026-11-28", "fix": "grain login --refresh"}}
   ```
   (`"fix"` is omitted or empty when no concrete command remediation applies), and stdout **MUST** remain empty (unless the exit code is `1` on a completed `wait` / `ci show` / `ci wait` / `health` query whose JSON payload describes the failed run or task, in which case the result document is written to stdout and no redundant error wrapper is needed).
2. All external CLI commands **MUST** use the following exit codes:
   | Exit code | Meaning |
   |---:|---|
   | `0` | Success (and for `wait` / `ci wait`, the target met the requested condition without failure). |
   | `1` | The target entity asked about failed (for example, `ci wait` concluded `failure` or `cancelled`, or `wait --until=merged` saw the task close unmerged/failed, or `health` printed a `FAILING` section), an unclassified daemon/internal error (`5xx`), or the token's rate limit (`429 Too Many Requests`, code `rate_limited`: the same command succeeds once it refills). |
   | `2` | CLI usage error, unknown command/flag, stray positional argument, missing required flag, invalid state precondition (`400 Bad Request`, `409 Conflict`, `422 Unprocessable Entity`), or unknown context. |
   | `3` | Authentication or authorization failure (`401 Unauthorized`, `403 Forbidden`, expired `gma_` token, insecure credential file permissions, missing local sign-in session, or login redirect). |
   | `4` | Daemon unreachable (DNS failure, TCP connection refused/reset, TLS handshake error, or HTTP connection/response timeout before receiving a daemon status). |
   | `5` | Blocking wait (`grain wait`, `grain ci wait`) timed out (`--timeout` elapsed) before its wake condition was satisfied. |

---

## 4. Attribution, scoped tokens, and proposal approval rules

### 4.1 Token `owner` and `on behalf of` attribution {#XC-14}

1. `model.ExternalAgentToken` **MUST** carry an `Owner` field storing the canonical human principal ID (`PrincipalHuman.ID`, e.g. `jane@example.com` or GitHub handle) to whom the credential belongs:
   - When minted via self-service `grain login --issue-token` ([#XC-9](#XC-9)), `Owner` **MUST** be set automatically from the authenticated human principal (and **MUST NOT** be overridable to a different person). A self-service mint authenticated only by the access code (no verified human principal) **MUST** leave `Owner` empty and **MUST** refuse a body naming one.
   - When minted by an operator with `capabilities:admin` (`POST /api/external-agents/tokens`), `owner` **MAY** be specified in the request body and defaults to the issuing human principal when omitted -- the *verified* one only: an issuer no verifier established (the access code, break-glass loopback, the in-process CLI) **MUST NOT** default `Owner` to the deployment's own actor, so its token acts on behalf of nobody (`operator-attribution.spec.md` §6.3).
   - `model.ExternalAgentToken.OwnerSource` (`external_agent_token.owner_source`) **MUST** record how the issuer who set `Owner` was established: the verifier's source for a verified minter, or the issuer's unverified source (`access-code`, `loopback`, `in-process`) when an unverified issuer named the owner. An agent minting a credential passes on its own credential's `Owner` and `OwnerSource`. A row with an empty `OwnerSource` (issued before it was recorded) keeps its previous attribution.
2. Calls authenticated with an external-agent token continue to execute with `Actor: Principal{Kind: PrincipalAgent, ID: "external:<name>"}`, and **MUST** populate `OnBehalfOf: &Principal{Kind: PrincipalHuman, ID: token.Owner}` whenever `token.Owner` is non-empty. Its `Source` is `token` (a verified on-behalf-of) when a verified person minted it or `OwnerSource` is empty, and the unverified `OwnerSource` itself otherwise, so an owner named under the access code never reads as `Verified()`.
3. Daemon journal records (`agent_api_call` / system log) and task conversation audit comments written by `agentAPIGate` **MUST** include `on behalf of <owner>` when `Owner` is set.
4. When an owned external-agent token creates a task via `POST /api/tasks`, the task's `Creator` (`Author`) and `Origin.OnBehalfOf` **MUST** be populated from `token.Owner` (so `grain list -author <owner>` and design-folder contributor attribution credit the human owner), while any `author` field or `-as` / `X-Forwarded-User` header sent by the client remains ignored/rejected as an identity claim.

### 4.2 Client tool and session label (`X-Grain-Client` and `filed_via`) {#XC-15}

1. `ui.HTTPClient` **MUST** send an `X-Grain-Client` HTTP header when `$GRAIN_CLIENT` or `$GRAIN_CLIENT_SESSION` is set in the environment:
   - If `$GRAIN_CLIENT` is `<tool>/<ver>` and `$GRAIN_CLIENT_SESSION` is `<id>` (and `$GRAIN_CLIENT` does not already contain `session=`), the header value **MUST** be `<tool>/<ver> session=<id>`.
   - Control characters and newlines **MUST** be stripped and total header length capped (at most `256` bytes).
2. On `POST /api/tasks` and `POST /api/tasks/{id}/comments` (and `reply`), the daemon **MUST** persist a sanitized `X-Grain-Client` value as `filed_via` (`filedVia` in JSON) on the created task and comment records, and display it alongside `Owner` in task provenance (e.g. `filed from jetski/1.4 by jane@example.com`).
3. `X-Grain-Client` is strictly an observational label: it **MUST NOT** grant or restrict permissions, scopes, repository access, or proposal auto-approval.

### 4.3 Built-in scope sets `grain/external-dev` and `grain/external-read` {#XC-16}

`model.BuiltinAPIScopeSets()` (`pkg/model/agent_api.go`) **MUST** include two non-dangerous presets (`RequiresPerson: false`):

1. **`grain/external-dev`** (the default scope set for `grain login --issue-token`):
   - Grants: `tasks:read`, `tasks:write`, `ci:read`, `ci:write`, `merge:write`, `repos:read`, `templates:read`, `deployment:read`.
   - **MUST NOT** grant: `tasks:admin`, `merge:force`, `settings:read`, `settings:write`, `secrets:read`, `secrets:write`, `connections:write`, `capabilities:admin`, or any dangerous `deployment:*` scope (`deployment:write`, `deployment:upgrade`, `deployment:src`).
2. **`grain/external-read`** (read-only observer preset):
   - Grants: `tasks:read`, `ci:read`, `repos:read`, `templates:read`, `deployment:read`.
   - **MUST NOT** grant any `:write`, `:admin`, or `:force` scope, nor `deployment:src`.

### 4.4 External filing escalation rails and proposal approval rules {#XC-17}

1. Every `POST /api/tasks` call made by an external-agent principal **MUST** pass through `externalAgentFilingEscalation` (`pkg/ui/agent_api_gate.go`), which **MUST** refuse (`403 Forbidden`) any request that:
   - targets a repository (`repo`, `reads`, `writes`) outside the token's repository allowlist;
   - attaches a capability not held by the token;
   - sets `autoMerge: true`, `autoApproveProposals: true`, or `keepRunning: true`;
   - sets `priority` above `model.DefaultTaskPriority` (`5`), unless the token holds `tasks:admin`;
   - sets `approved: true`, unless the token holds `tasks:admin`;
   - attempts to set `author` / `creator` directly in the request body (attribution comes solely from `token.Owner`, [#XC-14](#XC-14)).
2. Consequently, tasks filed via `grain create` (which defaults `-approve=false`) or via an external token without `tasks:admin` **MUST** land in state **`proposed`** (`Approval == nil`) by default.
3. **Owner-trust proposal auto-approval policy:** Deployment settings (`PUT /api/settings`, scope `settings:write` — never writable under `grain/external-dev`) **MUST** support an optional owner-trust list (matching `(owner, repo)` or `(owner, "*")`). When an external-agent token with non-empty `Owner` files an unapproved task (`approved: false`) whose `(token.Owner, targetRepo)` matches the deployment's owner-trust policy, and no catalogue/person-only hold (`catalogueHold`, `requires_person`) applies to the task, `CreateTask` **MUST** auto-approve and queue the task (`state: "queued"`), recording `Approval` as automated owner-trust approval on behalf of `token.Owner`.

---

## 5. External task, inbox, waiting, and log workflow deltas

Baseline flag syntax and output behaviour for `grain list`, `grain get`, `grain create`, and `grain comment` are specified in `design/spec/cli/`. For the external-agent workflow, the following extensions and contracts apply:

### 5.1 `grain list` provenance filters and `grain get --comments-since` {#XC-18}

1. In addition to its baseline filters (`design/spec/cli/`), `grain list` **MUST** support two `-filter` predicates for external orchestration:
   - `derived_from:eq:<owner/name#pr>` (or PR URL): matches tasks whose provenance/links record `Derived from:` pointing at that PR;
   - `filed_via:has:<substring>`: case-insensitively matches tasks whose `filed_via` ([#XC-15](#XC-15)) contains `<substring>` (for example `session=abc123` or `jetski/`).
2. `grain get <id>` (`GET /api/tasks/{id}`, scope `tasks:read`) **MUST** accept `--comments-since <RFC3339>` (`?comments_since=<RFC3339>`) so external polling loops receive only conversation items created strictly after that timestamp.

### 5.2 `grain create` external authoring flags (`--body-file` and `--design`) {#XC-19}

Alongside its baseline task-creation flags (`-repo`/`-no-repo`, `-base`, `-title`, `-body`, `-depends-on`, `-position`, `-priority`, `-field`, `-capability`, `-read`, `-write`, `-attach`, `-approve`; see `design/spec/cli/`), `grain create` **MUST** support:
- `--body-file <path|->` (`-` reads UTF-8 markdown from stdin; passing both `--body` and `--body-file` **MUST** fail with exit `2`);
- `--design <path>`: records `<path>` (a `design/...` design or spec file) on the task's provenance links so downstream reviews and commits inherit the design/spec chain.

### 5.3 `grain comment` and `grain reply` {#XC-20}

1. `grain comment <id> [<body...> | --body-file <path|->] [--attach <file>] [--json]` posts a general note to `POST /api/tasks/{id}/comments` (`tasks:write`).
2. `grain reply <id> [<body...> | --body-file <path|->] [--attach <file>] [--json]` answers a parked question on task `<id>` (`POST /api/tasks/{id}/reply`, scope `tasks:write`):
   - If task `<id>` is **not** currently parked in `awaiting-reply` on an `ask_question` prompt, the server and CLI **MUST** refuse the call (`409 Conflict` / exit code `2`) without adding a comment to the conversation.
   - If task `<id>` is parked on a **secret request** (`request_secret`), `grain reply` **MUST** refuse (`409 Conflict` / exit code `2`) without posting anything and direct the operator to the encrypted secret input on `/tasks/<id>`.
   - When valid, `grain reply` records the answer in the task conversation (carrying `Owner` and `filed_via`) and re-queues the task.

### 5.4 `grain inbox` {#XC-21}

`grain inbox [--repo <owner/name>] [--kind <question|secret|notification|merge-held|proposal>] [--json]` (backed by `GET /api/inbox`, scope `tasks:read`) **MUST** return the actionable items currently surfaced in the UI Inbox across repositories readable by the caller:

1. Tasks parked on a question (`kind: "question"`) or secret request (`kind: "secret"`), with summary and `/tasks/<id>` path;
2. Active `warning` and `critical` operator notifications (`kind: "notification"`), with `severity`, `subject`, `title`, and target UI route;
3. Tasks whose pull request is held at a merge gate requiring attention (`kind: "merge-held"`), with `task`, `pr`, and `reason`;
4. Unapproved task proposals (`kind: "proposal"`), with `task`, `title`, `proposer`, and escalation/hold reason.

### 5.5 `GET /api/tasks/wait` and `grain wait` {#XC-22}

1. The server **MUST** expose `GET /api/tasks/wait?ids=<id1,id2,...>&until=<cond1,cond2,...>&timeout=<seconds>` under scope `tasks:read`:
   - Verifies `tasks:read` repository authorization on **every** task ID in `ids` before waiting (`403` or `404` otherwise).
   - Caps each single HTTP hold to at most `60` seconds (`timeout` clamped to `1..60`, default `60`).
   - Subscribes to the daemon's in-process task/CI change broadcaster (reusing the UI live-update feed; **MUST NOT** run per-caller database polling loops).
   - Evaluates `until` conditions immediately on entry (so already-satisfied conditions return immediately without race) and on every relevant state/CI transition:
     - `closed`: satisfied when **all** `ids` are in a closed terminal state;
     - `merged`: satisfied when **all** `ids` closed merged, **or** wakes early with failure if any task in `ids` closes unmerged/failed/cancelled so the caller never hangs waiting for an impossible merge;
     - `awaiting-reply`: wakes as soon as **any** task in `ids` is parked on a question or secret request -- its derived state is `awaiting_reply` **and** it has no attempt still live. A question comment the run posted while it is still finishing (`PendingQuestionCommentID` set mid-run) **MUST NOT** wake the wait on its own, so a caller that replies straight after waking is never refused as "not parked";
     - `ci-done`: wakes as soon as **any** task in `ids` has a concluded latest CI run;
     - `any`: wakes on the first state or run change on **any** task in `ids` (compared against optional `since=<revision|timestamp>`).
   - External callers **MUST NOT** accrue `RunWaitCredits` (`pkg/orchestrator/waitcredit.go` applies exclusively to in-grain task runs).
2. `grain wait <id...> --until=<cond[,cond...]> [--timeout=30m] [--json]` **MUST** re-issue `GET /api/tasks/wait` slices until the condition is satisfied or `--timeout` (default `30m`) expires:
   - When satisfied, prints each task's `id`, `state`, `outcome`, `prUrl`, `mergeCommit` (when merged), `question` (when `awaiting-reply`), `ci` summary (when `ci-done`), and closing note; exits `0` when met cleanly, or exits `1` when `--until=merged` (without `closed`) encounters a task that closed unmerged/failed or `--until=closed` encounters a failed task.
   - When `--timeout` expires first, prints current states plus a resumable `grain wait ...` command line (to stderr in `--json` mode, or in the structured timeout error) and exits `5`.

### 5.6 `grain logs` {#XC-23}

`grain logs <id> [--attempt <N>] [--tail <N>] [--follow] [--json]` (`tasks:read`) **MUST** print the live activity status and transcript/log lines of task `<id>`'s latest (or `<N>`th) run. With `--follow`, it streams new entries until the attempt finishes (exiting `0` when the attempt succeeded or completed normally, `1` if the attempt errored, or `4` on broken connection).

---

## 6. Namespace over the main API (`grain fs`)

### 6.1 `GET /api/fs/<scheme>/<path>` and per-scheme scopes {#XC-24}

1. `ui.Server` **MUST** mount `pkg/namespace.Server` at `GET /api/fs/<scheme>/<path...>` behind `agentAPIGate`, supporting identical query parameters (`?list`, `?glob`, `?grep`, `?tail`, `?follow`) and HTTP `Range` requests, secret redaction, and response size bounds as the in-sandbox `/fs/` proxy (`pkg/gitproxy/namespace.go`).
2. `agentAPIGate` **MUST** authorize `/api/fs/<scheme>/...` per scheme:
   | Scheme | Required API scope | Additional rules |
   |---|---|---|
   | `ci://` | `ci:read` | Enforces repository grant on `<owner>/<name>` path prefix. |
   | `task://` | `tasks:read` | Enforces repository grant on the target task's repository. |
   | `deploy://` | `deployment:read` | `deploy://merge-queue` (and `merge-queue.md`) is also permitted with `ci:read`, filtered to repositories the caller may read. |
   | `config://` | `settings:read` | Same secret-redacted config projection as Stage 2 `pkg/namespace`. |
   | `memory://` | `memory:read` | Enforces repository grant on repository-scoped memory documents. |
   | `src://` | `deployment:read` | **Refused (`403`) for external-agent (`gma_`) tokens** unless the token's scope set or explicit scopes explicitly grant `deployment:src`. |

### 6.2 `grain fs` CLI subcommands {#XC-25}

1. `pkg/cli` **MUST** provide:
   - `grain fs ls <uri> [--glob <pattern>] [--json]`
   - `grain fs cat <uri> [--tail <bytes-or-lines>] [--follow]`
   - `grain fs read <uri> [--tail <bytes-or-lines>] [--follow]` (exact alias for `grain fs cat` for parity with in-sandbox `grain read`)
   - `grain fs grep <regex> <uri> [-n] [--glob <pattern>] [--json]`
2. `grain fs` and `pkg/sandboxcli`'s `ls|read|grep` **MUST** share URI parsing (`<scheme>://<path>` including inline query suffixes like `?grep=FAIL`), query construction, and output formatting over a common client interface backed by either `http://127.0.0.1:9728/fs/` (sandbox) or `<server>/api/fs/` (external).

---

## 7. External CI, merge-queue, and pull request workflow

Baseline `grain ci` subcommands (`list`, `show`, `watch`, `run`, `cancel`, `cache`, `artifact`, `faults` in `pkg/cli/cicmd.go`) and the in-sandbox `grain ci` / `grain pr` / `grain merge-queue` subcommands (`pkg/sandboxcli`) are specified in `design/spec/cli/`. Outside a sandbox, the external-agent workflow adds the following commands and API contracts:

### 7.1 External `grain ci wait`, `retrigger`, and `test-history` {#XC-26}

`pkg/cli/cicmd.go` **MUST** expose three workflow verbs outside the sandbox (sharing formatting/types with the in-sandbox implementations in `design/spec/cli/`):

1. `grain ci wait (<run-id> | --pr <url|owner/name#n> | --ref <branch> --repo <owner/name>) [--jobs <j1,j2>] [--timeout 30m] [--json]` (`ci:read`): attaches to the specified or latest in-flight CI run on the PR/branch, blocks via long-poll slices (with **no** `RunWaitCredits`) up to `--timeout` (default `30m`), prints the verdict with failing-job details and shared-fault annotations (`GrainCISharedFaults`), and exits `0` on `success`, `1` on `failure`/`cancelled`/`timed_out`, or `5` when `--timeout` elapses while the run is still in progress.
2. `grain ci retrigger <run-id> [--jobs <j1,j2>] [--json]` (`ci:write`): reruns failed jobs of `<run-id>` once with cache disabled, matching in-sandbox `grain ci retrigger`.
3. `grain ci test-history <test-name> [--repo <owner/name>] [--limit <N>] [--json]` (`ci:read`): queries recent pass/fail/flake history for `<test-name>` across runs in the repository, matching in-sandbox `grain ci test-history`.

### 7.2 `grain merge-queue` {#XC-27}

`grain merge-queue [--repo <owner/name>] [--json]` (`ci:read` or `deployment:read`) **MUST** display queued pull requests, active CI and review checks, and held merge gates (reading `GET /api/pipeline/changes` / `deploy://merge-queue`), filtered to `--repo` when specified and to the caller's allowed repositories. `grain force-merge` remains gated on dangerous `merge:force` and **MUST NOT** be reachable with `grain/external-dev`.

### 7.3 `POST /api/pull-requests/adopt` and `grain pr adopt` {#XC-28}

1. The server **MUST** expose `POST /api/pull-requests/adopt` requiring **both** `merge:write` and `tasks:write` on the target PR's repository (`owner/name`).
2. Request parameters: `pr` (`https://github.com/<owner>/<name>/pull/<n>` or `<owner>/<name>#<n>`), `mode` (`"review-only"` or `"finish"`, defaulting to `"review-only"`), optional `spec` (`design/spec/...`), and optional `design` (`design/design/...`).
3. Behaviour (`pkg/orchestrator/takeover.go`):
   - Creates (or updates in place, if an active shepherd task already exists for that PR) the takeover shepherd task. Updating an existing `"review-only"` shepherd with `--mode finish` **MUST** upgrade that shepherd in place rather than creating a second task.
   - In **`review-only`** mode, the shepherd runs Grain CI, files code review (and security review when trust-boundary paths or non-collaborator authorship require it), holds the `takeover-cleared` gate until reviews and CI pass for the current head, and submits to the merge queue once cleared, **without** pushing fix commits to the PR branch.
   - In **`finish`** mode, the shepherd additionally addresses review findings and red CI by pushing new commits onto the PR branch (never force-pushing).
   - When `--spec` or `--design` is passed (or `Implements:` / `Design:` appears in the PR body), the shepherd links those paths in its provenance chain and instructs code review tasks to verify conformance against the named document(s) and their required tests.
   - Proposal approval follows [#XC-17](#XC-17) (starts `proposed` when filed by an external agent unless `tasks:admin` or the owner-trust gate auto-approves it); `autoMerge` follows takeover's existing trusted-author rule.
4. `grain pr adopt <pr-url|owner/name#n> [--mode review-only|finish] [--spec <path>] [--design <path>] [--json]` calls `POST /api/pull-requests/adopt` and prints the shepherd task ID, state, and mode.

### 7.4 `grain pr review` and `grain pr status` {#XC-29}

1. `grain pr review <pr-url|owner/name#n> [--design] [--spec <path>] [--json]` (`tasks:write` + `repos:read`, or `POST /api/pull-requests/review`) files a review-only Grain review task on the PR (equivalent to `@<slug> review` on GitHub). When `--design` is set, the reviewer prompt checks design-folder conventions (`contributions.spec.md`) and overlapping designs in `design/`.
2. `grain pr status <pr-url|owner/name#n> [--threads] [--json]` (`GET /api/pull-requests/status`, requiring `ci:read` and `tasks:read` on `owner/name`) returns:
   - current PR `head` SHA and shepherd task ID/mode (if adopted);
   - `ci`: latest Grain CI run ID, status/conclusion, and failing job names;
   - `reviews`: each Grain review (`code`, `security`, `design`) for the head with state (`pending`, `passed`, `findings`) and unresolved thread count, plus full thread file/line/body items when `--threads` is passed;
   - `mergeQueue`: current gate state (`queued`, `held: review`, `held: ci`, `merged`, `not-queued`).

---

## 8. Agent onboarding, local MCP bridge, and distribution

### 8.1 `grain skill install` and `grain skill status` {#XC-30}

1. `grain skill install [--jetski | --claude | --codex | --gemini | --dir <path>] [--project] [--mcp] [--json]` **MUST** render a `SKILL.md` file for the active context:
   - **Target path resolution:**
     | Flag | User-level (default) | With `--project` (relative to repo root / cwd) |
     |---|---|---|
     | `--jetski` | `~/.gemini/jetski/skills/grain/SKILL.md` | `.agents/skills/grain/SKILL.md` |
     | `--claude` | `~/.claude/skills/grain/SKILL.md` | `.claude/skills/grain/SKILL.md` |
     | `--codex` | `~/.codex/skills/grain/SKILL.md` | `.agents/skills/grain/SKILL.md` |
     | `--gemini` | `~/.gemini/skills/grain/SKILL.md` | `.gemini/skills/grain/SKILL.md` |
     | `--dir <p>` | `<p>/SKILL.md` | `<p>/SKILL.md` |
   - **Content:** YAML frontmatter (`name: grain`, concise 1–2 line `description` so idle prompt overhead stays minimal) followed by:
     1. Static CLI guide embedded in the binary (`pkg/cli/skill/SKILL.md.tmpl`): core verbs, `--json` and exit codes `0..5`, `grain wait` patterns, exporting `GRAIN_CLIENT` / `GRAIN_CLIENT_SESSION`, using `grain reply` (never `grain comment`) to answer parked questions, never calling `force-merge`, and relaying secret requests as root-relative `/tasks/<id>` links.
     2. Deployment section populated live from `GET /api/config` and `GET /api/auth/whoami`: context name, server URL, reachable repositories, available templates and declared custom fields, whether `design/` workflow is present, and held scopes/scope set.
   - **Security invariant:** `SKILL.md` **MUST NOT** contain any bearer token, access code, or secret.
   - **Staleness metadata:** embeds a machine-readable comment recording the `grain` CLI version and the SHA-256 digest of the rendered deployment metadata. `grain skill install` **MUST** be idempotent when re-run.
2. `grain skill status [--json]` checks installed skill paths for the active context and reports `installed`, `stale` (binary version mismatch or deployment digest mismatch), or `missing`.
3. When any ordinary external `grain` command runs against a context whose recorded skill file on disk has a mismatched CLI version or cached deployment digest, the CLI **SHOULD** emit at most one concise single-line notice on stderr (`note: grain skill for context <ctx> is stale; run 'grain skill install'`), never writing to stdout.

### 8.2 `grain mcp` local stdio MCP server {#XC-31}

1. `grain mcp [--context <name>]` **MUST** run a local Model Context Protocol server over stdio (`stdin`/`stdout` JSON-RPC 2.0) intended for workstation coding agents (distinct from `grain mcpserver`, which is the internal sandbox harness MCP server).
2. `grain mcp` **MUST** expose the compact 13-tool external roster: `propose_task`, `read_grain_task`, `list_grain_tasks`, `wait_for_task`, `reply_to_task`, `comment_on_task`, `grain_inbox`, `ci_runs`, `ci_log`, `merge_queue`, `fs_read`, `fs_grep`, and `adopt_pull_request`.
3. Every tool **MUST** execute the same Go client/validation code path and active context credentials as its corresponding `grain` CLI verb; `grain mcp` **MUST NOT** act as a raw pass-through proxy to remote `POST /mcp` (which exposes the ~270-tool generated `api_*` schema).
4. `wait_for_task` in `grain mcp` **MUST** bound each tool-call wait slice below standard MCP client tool timeouts (default slice `90s`, configurable via `timeout_seconds`) and return `{status: "waiting", tasks: [...], resume: true}` when the condition is not yet met so the client never suffers a transport timeout.
5. `list_grain_tasks`, `grain_inbox`, and `ci_runs` **MUST** page their answers so a call with no arguments fits an MCP client's ~10 KB tool-result budget rather than being cut mid-row by `pkg/mcp`'s backstop (task 3953): each answers `total`, `truncated`, and `nextCursor`, and takes `limit`, `cursor`, and `q`. Their default pages are the agent API's (`pkg/ui/list_page.go`): `list_grain_tasks` answers 50 `fields=summary` rows (`id`, `title`, `state`, `repo`, `externalRef`, `createdAt`, `blockedBy`, `stall`) or 5 whole tasks with `fields: "full"`, paging the list it filtered on the client with the route's own offset cursors; `grain_inbox` answers 20 items; `ci_runs` answers 20 runs in the compact view by default, or with `compact: false` up to 10 whole runs, dropping runs off the end of a page the caller set no `limit` on until it fits, and listing only the shared faults on that page with at most 5 of each one's refs and runs.
6. `grain skill install --mcp` **MUST** register `{"command": "<grain-path>", "args": ["mcp", "--context", "<ctx>"]}` in the target agent's MCP server configuration file alongside writing `SKILL.md`.

### 8.3 Settings → CLI UI page and `GET /cli/install.sh` {#XC-32}

1. The daemon **MUST** serve `GET /cli/install.sh` (unauthenticated-safe, `Content-Type: text/x-shellscript; charset=utf-8`):
   - Detects `uname -s` (`Linux`, `Darwin`) and `uname -m` (`x86_64`/`amd64`, `arm64`/`aarch64`), downloads the release archive matching the running daemon's version (or a configured release mirror URL), verifies its SHA-256 digest before extracting, and installs `grain` into `${GRAIN_INSTALL_DIR:-$HOME/.local/bin}/grain` with mode `0755`.
2. The web UI (`ui/src/components/settings/`) **MUST** provide a **Settings → CLI** section displaying:
   - the deployment's `curl -fsSL <origin>/cli/install.sh | sh` install line;
   - a pre-filled `grain login <origin> --name <slug> --issue-token --scope-set grain/external-dev ...` command (noting when `externalAgentApi` needs to be enabled first, with a direct control or link to Settings → Agents);
   - per-agent `grain skill install` commands (`--jetski`, `--claude`, `--codex`, `--gemini`).

---

## 9. Boundary with `design/spec/cli/` and the in-sandbox `grain` CLI

`design/spec/cli/` is the normative reference for the whole `grain` CLI's command and flag surface (both host `pkg/cli` and in-sandbox `pkg/sandboxcli`), while `design/design/sandbox-first-agent-surface.design.md` describes the in-sandbox proxy architecture (`http://127.0.0.1:9728`). Once new verbs specified here (`context`, `login`, `logout`, `whoami`, `reply`, `inbox`, `wait`, `logs`, `fs`, `merge-queue`, `pr`, `skill`, `mcp`) land in code, their canonical flag tables merge into `design/spec/cli/` while their external-agent auth, attribution, scope, and long-poll rules remain here.

The host/external CLI and in-sandbox CLI coexist in one `grain` binary without contradiction:

1. **Dispatch boundary (`cmd/grain`, [#XC-3](#XC-3)):** `pkg/sandboxcli.MaybeRun` handles invocations inside a task sandbox (detected via `/etc/grain/sandbox.env` or `$GRAIN_SANDBOX_CLI`) and yields to `pkg/cli` whenever `GRAIN_SANDBOX_CLI=0` or an explicit external target (`-server`, `$GRAIN_SERVER`, `--context`, `$GRAIN_CONTEXT`) is given.
2. **Namespace (`grain ls|read|grep` vs `grain fs ls|cat|read|grep`, [#XC-24](#XC-24)–[#XC-25](#XC-25)):** Inside a sandbox, `ls`, `read`, and `grep` are top-level verbs; on a workstation, `pkg/cli` already owns host/operator top-level verbs, so namespace commands live under `grain fs` and accept both `cat` and `read` over the same `pkg/namespace` client logic.
3. **Task verbs:** Inside a sandbox, `grain status`, `grain handover`, and `grain notify` act on *the calling run itself*, while cross-task queries sit under `grain task list|read|wait|message|propose`. Externally there is no calling run, so `grain list`, `grain get`, `grain create`, `grain comment`, `grain reply`, `grain wait`, `grain inbox`, and `grain logs` remain top-level.
4. **Blocking waits and wait credit ([#XC-22](#XC-22), [#XC-26](#XC-26)):** Sandbox `grain ci wait` and `grain task wait` slice at `9m` (`540s`), exit `0` on slice expiry with a `--resume` hint (to stay under `run_command`'s `600s` ceiling), and accrue `RunWaitCredits` against the attempt deadline. External `grain wait` and `grain ci wait` have no `run_command` ceiling and no attempt budget: they re-issue `60s` long-polls up to `--timeout` (`30m` default), accrue **zero** wait credit, and exit `5` on timeout (`grain mcp`'s `wait_for_task` uses short slices with a `resume` field because MCP tool calls are bounded like `run_command`).
5. **Pull requests ([#XC-28](#XC-28)–[#XC-29](#XC-29)):** Sandbox `grain pr open|reply|resolve|review-comment|status|base-status` mutate or inspect *the task's own branch* through the proxy's GitHub App token because sandboxes hold no GitHub credentials. Externally, the human/agent pushes and comments on GitHub with the engineer's own `git`/`gh` login (preserving human authorship), while `grain pr adopt|review|status <pr>` coordinates Grain's CI, conformance reviews, and merge queue on that PR.

---

## 10. Implementation status on `main`

| ID | Requirement | Status on `main` | Implementing code | Existing tests |
|---|---|---|---|---|
| [#XC-1](#XC-1) | `~/.config/grain/config.json` named contexts (`0600`, `$XDG_CONFIG_HOME`, no secrets) | **implemented** | `pkg/cli/context.go` | `pkg/cli/context_test.go` (`TestContextConfigXDGMode0600AndNoRawSecrets`) |
| [#XC-2](#XC-2) | `grain context list\|use\|show\|delete`, `--context`, `$GRAIN_CONTEXT` | **implemented** | `pkg/cli/context.go`, `pkg/cli/main.go`, `pkg/cli/args.go` | `pkg/cli/cliconn_test.go` (`TestContextCommandsAndServerPrecedence`) |
| [#XC-3](#XC-3) | Server URL resolution (`-server` > `$GRAIN_SERVER` > context > `cli.env` > default) & sandbox disambiguation | **implemented** | `pkg/cli/cliconn.go`, `pkg/cli/main.go`, `pkg/sandboxcli/sandboxcli.go`, `pkg/cli/context.go` | `pkg/cli/cliconn_test.go` (`TestTheCLIFindsItsDaemonInCLIEnv`, `TestCLIEnvIsReadAsData`, `TestARefusedConnectionFailsFastAndSaysWhereTheDaemonIs`, `TestAnUnansweredCallTimesOut`, `TestATimeoutNamesTheHostDaemonAfterTheServerThatTimedOut`, `TestADaemonErrorIsNotRewritten`); `pkg/cli/cli_flags_test.go` (`TestRunCLI_GlobalFlags`); `TestContextCommandsAndServerPrecedence`; `pkg/sandboxcli/sandboxcli_test.go` (`TestSandboxCLIDisambiguatesExplicitServerAndContext`) |
| [#XC-4](#XC-4) | Credential resolution order (`-token`, env, context, local token, ADC/`gcloud` ID token) | **implemented** | `pkg/ui/httpclient.go` (`ContextToken`), `pkg/cli/credsource.go`, `pkg/cli/cliconn.go` | `pkg/ui/httpclient_auth_test.go` (`TestHTTPClient_StaticTokenHeader`, `TestHTTPClient_TokenWithBearerPrefix`, `TestHTTPClient_TokenProvider`, `TestHTTPClient_EnvVarToken`, `TestHTTPClient_AttachmentAndDownloadCarryAuth`, `TestHTTPClient_ShouldAttemptCloudRunAuth`, `TestHTTPClient_TokenCacheExpiry`, `TestHTTPClient_NoAudienceTokenOnlyForCloudRun`, `TestHTTPClient_IDTokenCachedUntilItsOwnExpiry`); `pkg/cli/cliconn_test.go` (`TestAnUnauthorisedCallNamesTheUnreadableToken`, `TestContextCommandsAndServerPrecedence`) |
| [#XC-5](#XC-5) | Server-side IAP/proxy JWT verification, non-authoritative `-as`, and single-tenant access code | **implemented and tested** | `pkg/ui/principal.go`, `pkg/ui/verifier.go`, `pkg/ui/accesscode.go`, `pkg/cli/accesscode.go` | `pkg/ui/principal_test.go`; `pkg/ui/accesscode_test.go`; `pkg/cli/accesscode_test.go` (`TestAccessCodeShowsAndRotatesTheCode`, `TestAccessCodeRotateNeedsAnExistingInstallation`); `pkg/ui/httpclient_auth_test.go` (`TestHTTPClient_UserIdentityHeader`) |
| [#XC-6](#XC-6) | `gma_` external-agent tokens on `/api/*` & `/mcp`, SHA-256 storage, rate/concurrency limits, lifecycle & kill switch | **implemented and tested** | `pkg/ui/external_agents.go`, `pkg/ui/server.go`, `pkg/model/external_agent_token.go` | `pkg/ui/external_agents_test.go` (`TestExternalAgentMCPDoneWhen`, `TestExternalAgentAuthAndSettingsGate`, `TestExternalAgentScopesAndReposEnforced`, `TestExternalAgentDangerousScopeRequiresPerson`, `TestExternalAgentRevokeRotateExpiryAndKillSwitch`, `TestExternalAgentNoTaskContextRails`, `TestExternalAgentRateLimitConcurrencyAndSizeLimit`) |
| [#XC-7](#XC-7) | OS keyring (`grain/<ctx>`) and `0600` `~/.config/grain/credentials/<ctx>` fallback with permission refusal | **implemented** | `pkg/cli/credentials.go` (keyring through `secret-tool` / macOS `security`; no Windows Credential Manager backend yet, so Windows uses the file) | `pkg/cli/credentials_test.go` (`TestCredentialStoreKeyringFallbackAndRefusesWorldReadableFile`) |
| [#XC-8](#XC-8) | `GET /api/auth/whoami` (unauth discovery, auth identity/scopes, expired-token detail) and `grain whoami` | **implemented** | `pkg/ui/auth_whoami.go`, `pkg/ui/external_agents.go`, `pkg/cli/login.go` | `pkg/ui/whoami_test.go` (`TestAuthWhoamiUnauthenticatedAuthenticatedAndExpiredToken`); `pkg/cli/login_test.go` (`TestGrainLoginIssueTokenSelfServiceNonAdminAndLogoutRevokes`) |
| [#XC-9](#XC-9) | `grain login` (`--issue-token`, non-admin self-service non-dangerous minting, `--token-stdin`, `--refresh`) & `grain logout` | **implemented** | `pkg/cli/login.go`, `pkg/ui/external_agents.go` (`selfServiceLimits`), `pkg/ui/agent_api_gate.go` (`ownCredentialRoute`) | `pkg/cli/login_test.go` (`TestGrainLoginIssueTokenSelfServiceNonAdminAndLogoutRevokes`) |
| [#XC-10](#XC-10) | `cli.RegisterCredentialSource` hook and `HTTPClient` fail-fast login-redirect / HTML refusal | **implemented** | `pkg/cli/credsource.go`, `pkg/ui/httpclient_login.go`, `pkg/authfix/authfix.go` | `pkg/cli/credsource_test.go` and `pkg/ui/httpclient_auth_test.go` (`TestRegisterCredentialSourceAndHTTPClientRefusesLoginRedirectAndHTML`) |
| [#XC-11](#XC-11) | `-flag`/`--flag` parity, post-verb `--json`/global flags, and stray-argument rejection | **partly implemented** | `pkg/cli/args.go`, `pkg/cli/main.go` (post-verb `--json` not yet supported) | `pkg/cli/args_test.go` (`TestCommandsRefuseArgumentsTheyHaveNoUseFor`, `TestHostLocalCommandsRefuseArgumentsTheyHaveNoUseFor`, `TestArgumentCheckWording`); `pkg/cli/cli_flags_test.go` (`TestRunCLI_GlobalFlags`) |
| [#XC-12](#XC-12) | `--json` single-document stdout on every verb and compact one-line text default | **partly implemented** | `pkg/cli/main.go`, `pkg/cli/list.go`, `pkg/cli/cicmd.go` | `pkg/cli/cicmd_test.go` (`TestPrintCIRunsJSON`, `TestCIFollowJSONIsOneDocument`); `pkg/cli/list_hidden_test.go` (`TestListSaysWhatItLeftOutWithoutSpoilingTheListing`); `pkg/cli/releasenotes_test.go` (`TestReleaseNotesCmdJSONFromAVersion`) |
| [#XC-13](#XC-13) | Structured stderr error JSON (`{code, message, fix}`) under `--json` and exit codes `0..5` | **not implemented** | `pkg/cli/main.go` currently exits `1` with plain text on all errors | — |
| [#XC-14](#XC-14) | `ExternalAgentToken.Owner`, `on behalf of <owner>` journal/audit attribution, and task `Author` from owner | **implemented** | `pkg/model/external_agent_token.go`, `pkg/ui/external_agents.go`, `pkg/ui/request_actor.go`, `pkg/ui/agent_api_gate.go`, `pkg/ui/tasks.go` | `pkg/ui/external_agents_test.go` (`TestExternalAgentTokenOwnerAttributionOnJournalAuditAndTaskAuthor`, `TestExternalAgentMCPDoneWhen`); `pkg/ui/external_agent_owner_source_test.go` (`TestExternalAgentTokenOwnerDefaultsOnlyToAVerifiedMinter`); `pkg/ui/external_agents_internal_test.go` (`TestExternalAgentTokenOwnerSources`) |
| [#XC-15](#XC-15) | `X-Grain-Client` (`$GRAIN_CLIENT`, `$GRAIN_CLIENT_SESSION`) and `filed_via` on tasks/comments | **implemented** | `pkg/model/grain_client.go`, `pkg/ui/httpclient.go`, `pkg/ui/grain_client.go`, `pkg/ui/tasks.go` | `pkg/ui/httpclient_auth_test.go` (`TestHTTPClient_XGrainClientFromEnvAndSession`); `pkg/ui/external_agents_test.go` (`TestXGrainClientHeaderStoredAsFiledViaWithoutGrantingAuthority`) |
| [#XC-16](#XC-16) | Built-in scope sets `grain/external-dev` and `grain/external-read` | **implemented** | `pkg/model/agent_api.go` | `pkg/ui/api_scope_sets_test.go` (`TestExternalDevAndExternalReadScopeSetsHoldNoDangerousScopes`) |
| [#XC-17](#XC-17) | `externalAgentFilingEscalation` rails, default `proposed` filings, and owner-trust auto-approval policy | **implemented** | `pkg/ui/agent_api_gate.go`, `pkg/ui/client.go` (`ownerTrustApproves`), `pkg/model/owner_trust.go`, `pkg/ui/settings.go` (`ownerTrust`) | `pkg/ui/agent_api_test.go`; `pkg/ui/external_agents_test.go` (`TestOwnerTrustProposalPolicyAutoApprovesMatchingOwnerAndRepoOnly`); `pkg/cli/create_unapproved_test.go` (`TestUnapprovedNote`); `ui/e2e/external-agent-filer.spec.js` |
| [#XC-18](#XC-18) | `grain list` (`derived_from`, `filed_via` filters) and `grain get` (`--comments-since`) | **implemented** | `pkg/cli/list.go` (`provenanceFilter`), `pkg/cli/taskcmd.go`, `pkg/ui/task_list_filter.go` (`?derived_from=`, `?filed_via=`, `TaskDerivedFrom`), `pkg/ui/tasks.go` (`?comments_since=`), `pkg/model/task.go` (`LinkDerivedFrom`) | `pkg/cli/list_test.go` & `pkg/ui/tasks_api_test.go` (`TestListDerivedFromAndFiledViaFiltersAndGetCommentsSince`) |
| [#XC-19](#XC-19) | `grain create` (`--body-file`, `--design`, `-depends-on`, `-position`, `-priority`, `-field`) | **implemented** | `pkg/cli/taskcmd.go`, `pkg/cli/externalcmd.go` (`bodyFromFlags`), `pkg/ui/provenance.go` (`LinkDesign`) | `pkg/cli/taskcmd_test.go` (`TestCreateBodyFileStdinConflictAndDesignLink`); `pkg/cli/create_position_test.go` (`TestBacklogPosition`); `pkg/cli/create_author_test.go` (`TestCreateAuthorFilesTheTaskAsItsRequester`); `pkg/cli/create_model_test.go`; `pkg/ui/task_priority_test.go` |
| [#XC-20](#XC-20) | `grain comment` vs `grain reply` (refuses when no question is parked or on secret requests) | **implemented** | `pkg/ui/external_cli.go` (`POST /api/tasks/{id}/reply`, `Client.CheckReply`), `pkg/cli/externalcmd.go` (`cmdReply`) | `pkg/ui/reply_test.go` & `pkg/cli/reply_test.go` (`TestGrainReplyAnswersQuestionRefusesIdleAndRefusesSecretRequest`); `pkg/cli/args_test.go` (`TestCommentStillTakesItsBodyAsWords`) |
| [#XC-21](#XC-21) | `grain inbox` and `GET /api/inbox` | **implemented** | `pkg/ui/external_cli.go` (`handleInbox`, `inboxReadable`), `pkg/ui/agent_api_gate.go`, `pkg/cli/externalcmd.go` (`cmdInbox`) | `pkg/ui/inbox_test.go` & `pkg/cli/inbox_test.go` (`TestGrainInboxMatchesActionableCategoriesWithinRepoGrants`) |
| [#XC-22](#XC-22) | `GET /api/tasks/wait` (`closed\|merged\|awaiting-reply\|ci-done\|any`, no wait credit) and `grain wait` | **implemented** | `pkg/ui/external_cli.go` (`handleTasksWait`), `pkg/model/store.go` (`Store.Changed`), `pkg/ui/agent_api_gate.go` (`tasksWaitCallRepo`), `pkg/cli/externalcmd.go` (`cmdWait`) | `pkg/ui/wait_tasks_external_test.go` & `pkg/cli/wait_test.go` (`TestTasksWaitEndpointWakesOnConditionsEarnsNoWaitCreditAndExits5OnTimeout`) |
| [#XC-23](#XC-23) | `grain logs <id> [--follow]` | **implemented** | `pkg/cli/externalcmd.go` (`cmdLogs`, over `GET /api/tasks/{id}` and `GET /api/tasks/{id}/attempts/{number}/transcript`) | `pkg/cli/logs_test.go` (`TestGrainLogsStreamsAndExitsOnAttemptCompletion`) |
| [#XC-24](#XC-24) | Mount `pkg/namespace` at `GET /api/fs/<scheme>/<path>` with per-scheme scopes and `src://` opt-in | **implemented** | `pkg/ui/api_fs.go` (one route per scheme in `route_scopes.go`; repo grants and the `deploy://merge-queue` / `src://` rules in `agent_api_gate.go`); `deployment:src` scope in `pkg/model/agent_api.go`; wired in `pkg/daemon/ui_server.go` | `pkg/ui/api_fs_test.go` (`TestAPIFsPerSchemeScopesRepoLimitsAndExternalSrcRefusal`, `TestAPIFsSecretRedactionRangeAndQueryParityWithSandboxProxy`); `pkg/namespace/namespace_test.go`; `pkg/namespace/stage2_test.go` |
| [#XC-25](#XC-25) | `grain fs ls\|cat\|read\|grep <uri>` sharing code with `pkg/sandboxcli` | **implemented** | `pkg/nsclient/` (shared URI parsing, queries, transport, output); `pkg/cli/fscmd.go`; `pkg/sandboxcli/sandboxcli.go` | `pkg/cli/fscmd_test.go` (`TestGrainFsLsCatReadGrepSharesClientWithSandboxCLI`); `pkg/sandboxcli/` tests |
| [#XC-26](#XC-26) | External `grain ci wait`, `ci retrigger`, and `ci test-history` alongside `list\|show\|watch\|run\|faults\|cache` | **implemented** | `pkg/cli/cicmd.go` (`cmdCIWait`, `cmdCIRetrigger`, `cmdCITestHistory`: flags and printing), `pkg/ci/cli/wait.go` (run resolution, verdict, shared-fault annotation, failed-job selection, test history; wait answers `gitproxy.CLIWaitResponse`, the in-sandbox wait's own shape, and test-history renders `ci.TestHistoryReport` as `pkg/sandboxcli` does); documented in [host #CLI-62](../cli/host.spec.md#CLI-62) | `pkg/cli/cicmd_test.go` (`TestCICommandDispatch`, `TestCIListSendsTheWholeFilterVocabularyToTheDaemon`, `TestCIWatchStreamsThenReportsTheVerdict`, `TestCIFaultsCommandListsAndFiltersBySourceAndRepo`, `TestExternalCIWaitVerdictsSharedFaultsZeroWaitCreditAndExitCodes`, `TestExternalCIRetriggerRunsUncachedFailedJobs`, `TestExternalCITestHistoryFormattingAndJSON`) |
| [#XC-27](#XC-27) | External `grain merge-queue` | **implemented** | `pkg/cli/mergequeue.go` (reads `deploy://merge-queue` over `GET /api/fs/deploy/merge-queue` through `pkg/nsclient`, as `pkg/sandboxcli` does); `?repo=` / `?format=json` in `pkg/namespace/stage2.go` (`MergeQueueQueryReader`) answered by `pkg/daemon/sandbox_namespace.go`; documented in [host #CLI-107](../cli/host.spec.md#CLI-107) | `pkg/cli/mergequeue_test.go` (`TestExternalMergeQueueFiltersByRepoAndJSON`); `pkg/namespace/stage2_test.go` (`TestMergeQueueQueryCarriesRepoFormatAndCIRepoLimit`); `pkg/ui/api_fs_test.go` |
| [#XC-28](#XC-28) | `POST /api/pull-requests/adopt`, `grain pr adopt`, `review-only` / `finish` modes, and `--spec`/`--design` conformance linking | **implemented** | `pkg/orchestrator/takeover_adopt.go`, `pkg/orchestrator/takeover.go`, `pkg/ui/pull_requests_api.go`, `pkg/daemon/pull_requests.go`, `pkg/cli/prcmd.go`, `pkg/model/pull_request_adopt.go` | `pkg/orchestrator/takeover_adopt_test.go` (`TestAdoptPullRequestReviewOnlyVsFinishUpgradeInPlace`, `TestAdoptPullRequestLinksSpecAndDesignIntoConformanceReviewAndProposalGate`); `pkg/ui/mutating_routes_test.go` (`TestEveryMutatingRouteIsAttributed`) |
| [#XC-29](#XC-29) | External `grain pr review [--design]` and `grain pr status [--threads]` | **implemented** | `pkg/orchestrator/takeover_adopt.go` (`ReviewPullRequest`, `PullRequestStatusOf`), `pkg/ui/pull_requests_api.go`, `pkg/cli/prcmd.go` | `pkg/ui/pull_requests_api_test.go`, `pkg/cli/prcmd_test.go` (`TestGrainPRReviewDesignAndPRStatusThreadsJSON`); `pkg/orchestrator/takeover_adopt_test.go` (`TestPullRequestReviewDesignAndStatusThreads`) |
| [#XC-30](#XC-30) | `grain skill install\|status` (`--jetski`, `--claude`, `--codex`, `--gemini`, `--project`, staleness check) | **implemented** | `pkg/cli/skill.go`, `pkg/cli/skill/SKILL.md.tmpl`, `pkg/cli/skill/template.go`, `pkg/cli/skill/install.go`, `pkg/cli/main.go` (`warnIfCommandSkillStale`), `pkg/ui/agent_api_gate.go` & `pkg/ui/templates.go` (`deployment:read` on `GET /api/config` and repo-filtered `GET /api/templates`), `pkg/ui/httpclient.go`; documented in [host #CLI-110](../cli/host.spec.md#CLI-110) | `pkg/cli/skill_test.go` (`TestGrainSkillInstallTargetPathsIdempotencyAndContainsNoSecrets`, `TestGrainSkillStatusDetectsVersionAndDeploymentDigestStaleness`, `TestStaleSkillStderrNoticeNeverCorruptsJSONStdout`) |
| [#XC-31](#XC-31) | `grain mcp` local stdio 13-tool MCP server and `grain skill install --mcp` | **implemented** | `pkg/cli/mcp_external.go`, `pkg/cli/mcp_external_ops.go`, `pkg/cli/skill/install.go` (`RegisterMCPServer`); documented in [host #CLI-110](../cli/host.spec.md#CLI-110) | `pkg/cli/mcp_external_test.go` (`TestGrainMCPStdioToolsCallSharedClientAndSliceWaitForTask`); `pkg/cli/mcp_external_paging_test.go` (`TestGrainMCPListToolsPageUnderTheToolBudget`); `pkg/cli/skill_test.go` (`TestGrainSkillInstallTargetPathsIdempotencyAndContainsNoSecrets`) |
| [#XC-32](#XC-32) | Settings → CLI UI page and `GET /cli/install.sh` | **implemented** | `pkg/ui/cli_install.go`, `pkg/ui/cli_install.sh` (release mirror: the daemon's `GRAIN_CLI_RELEASE_URL`), `ui/src/components/settings/CLISection.jsx`, `ui/src/components/settings/AgentsSection.jsx` (External agents switch for `externalAgentApi`) | `pkg/ui/cli_install_test.go` (`TestCLIInstallScriptEndpointPinsDaemonVersionAndVerifiesSHA256`); `ui/e2e/settings-cli.spec.js` (`TestSettingsCLIPageRendersInstallLoginAndSkillCommands`) |
