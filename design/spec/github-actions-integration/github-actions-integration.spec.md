# GitHub Actions integration

**Status:** draft for review. Normative.

**Specifies:** [`github-actions-integration.design.md`](../../design/github-actions-integration.design.md)

This specification makes [`github-actions-integration.design.md`](../../design/github-actions-integration.design.md) normative across authentication, ingress, attribution, task creation, waiting, the `grain-task` reusable action, CLI equivalents, pull request reviews, security ceilings, configuration, failure modes, and tests. It stays consistent with [`contributions.playbook.md`](../../playbook/contributions.playbook.md), [`contributions.spec.md`](../contributions/contributions.spec.md), [`authentication.design.md`](../../design/authentication.design.md), [`operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md), [`attribution.design.md`](../../design/attribution.design.md), [`external-cli.design.md`](../../design/external-cli.design.md), and `rbac.design.md` ([Task #2764](/tasks/2764)). Where this specification and its design disagree, the design wins on intent and this document is updated to match; where code on `main` disagrees with an approved version of this specification, the code is the bug.

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT", and "MAY" are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

## Contents

1. Authentication and ingress
2. Attribution and observability
3. Creating and waiting for tasks from workflows
4. Pull request reviews (`grain/review`)
5. Security boundaries and configuration
6. Failure modes
7. Verification and requirement-to-test mapping
- Open questions
- Implementation plan

---

## 1. Authentication and ingress

### 1.1 GitHub Actions OIDC verifier {#GA-1}

1.1.1. **Transport headers.** Grain MUST accept a GitHub Actions OpenID Connect (OIDC) JSON Web Token (JWT) from either:
- `X-Grain-Actions-OIDC: <jwt>`, registered as an `IdentityVerifier` header (`pkg/ui/verifier.go`) under verifier name `"github-oidc"`; or
- `Authorization: Bearer <jwt>`, when `<jwt>` consists of three dot-separated base64url segments whose decoded payload carries an `"iss"` string matching one of the deployment's configured GitHub OIDC issuers (§5.2.1). Non-JWT bearer tokens (`gma_…` external-agent tokens and single-tenant access codes) MUST NOT be claimed by `"github-oidc"`.

1.1.2. **Fail-closed verification.** When `X-Grain-Actions-OIDC` is present, or when `Authorization: Bearer` carries a JWT naming a configured GitHub OIDC issuer, verification failure MUST terminate the request with `401 Unauthorized` (invalid signature, expired token, bad issuer or audience, malformed claims) or `403 Forbidden` (verified token matching no `workflow_principal` rule). Grain MUST NOT fall through to `gma_` tokens, access codes, cookies, or anonymous handling.

1.1.3. **JWKS and signature checks.** Signing keys MUST be fetched from `<issuer>/.well-known/jwks` via `pkg/jwks.Fetch`:
- HTTPS is required; redirects off `https://` MUST be refused (`jwks.Fetch` downgrade check).
- Keys MUST be cached per issuer URL with a 1-hour TTL and a 10-second minimum interval between refetches triggered by an unknown `kid`. If a refetch fails while a cached key for `kid` exists, grain MAY serve the cached key for at most 1 additional hour.
- Only `RS256` (RSA modulus >= 2048 bits) and `ES256` (ECDSA P-256) keys are accepted. The expected `alg` header MUST be derived from the public key's type, never from the token header alone.

1.1.4. **Standard and GitHub workflow claims.** Every verified token MUST satisfy:
- `iss`: exact string match against an enabled issuer in configuration (default `https://token.actions.githubusercontent.com`; GitHub Enterprise Server deployments configure `https://<ghe-host>/_services/token`).
- `aud`: exact match against one of the deployment's configured OIDC audiences (`Config.OIDCAudiences`, defaulting to the deployment's canonical external URL when OIDC is enabled; empty audience configuration verifies nothing).
- `exp` (required), `iat` (required), and `nbf` (when present): valid within `30s` clock skew, with `exp - iat <= 1h` so abnormally long-lived tokens are rejected.
- Required non-empty string claims: `repository` (`<owner>/<name>`), `repository_id`, `repository_owner`, `repository_owner_id`, `ref`, `job_workflow_ref`, `event_name`, `actor`, `actor_id`, `run_id`, `run_attempt`, `sha`. Optional string claims: `ref_type`, `workflow`, `environment`, `runner_environment`.

```go
type GitHubOIDCClaims struct {
	jwt.RegisteredClaims
	Repository        string `json:"repository"`
	RepositoryID      string `json:"repository_id"`
	RepositoryOwner   string `json:"repository_owner"`
	RepositoryOwnerID string `json:"repository_owner_id"`
	Ref               string `json:"ref"`
	RefType           string `json:"ref_type,omitempty"`
	Workflow          string `json:"workflow,omitempty"`
	JobWorkflowRef    string `json:"job_workflow_ref"`
	Environment       string `json:"environment,omitempty"`
	EventName         string `json:"event_name"`
	Actor             string `json:"actor"`
	ActorID           string `json:"actor_id"`
	RunID             string `json:"run_id"`
	RunAttempt        string `json:"run_attempt"`
	SHA               string `json:"sha"`
}
```

### 1.2 Workflow principal resolution and authorization {#GA-2}

1.2.1. **Deny by default.** A cryptographically valid GitHub OIDC token grants zero access until it matches an enabled `workflow_principal` rule (§5.2.1). If no rule matches, grain MUST respond `403 Forbidden` with a structured JSON error naming `repository`, `job_workflow_ref`, `ref`, `environment`, and `event_name` (and containing no secret material).

1.2.2. **Claim matching.** A `workflow_principal` rule matches a verified `GitHubOIDCClaims` set if and only if all of the following hold:
1. `rule.Issuer` (default `https://token.actions.githubusercontent.com`) equals `claims.Issuer`.
2. `rule.Repo` matches `claims.Repository` (case-insensitively, canonicalized via `model.ParseRepo`).
3. If `rule.RepositoryID != ""`, it MUST equal `claims.RepositoryID`; if `rule.RepositoryOwnerID != ""`, it MUST equal `claims.RepositoryOwnerID`. This prevents repository-rename or retired-org takeover spoofing.
4. `claims.JobWorkflowRef` — after stripping `@<git-ref>` to isolate `<owner>/<repo>/.github/workflows/<file>` — matches one of `rule.Workflows` (path or filename glob, e.g. `.github/workflows/triage.yml` or `release-*.yml`), AND the `<owner>/<repo>` prefix of `claims.JobWorkflowRef` matches `rule.Repo` (or an explicitly listed reusable-workflow repo in `rule.TrustedReusableWorkflowRepos`). Matching on the mutable display claim `claims.Workflow` alone MUST NOT be used.
5. `claims.Ref` matches at least one pattern in `rule.Refs` (default `["refs/heads/main"]`). `refs/pull/*` MUST NOT match unless `rule.AllowPullRequestRefs` is `true` and a `refs/pull/*` pattern is listed.
6. If `rule.Environments` is non-empty, `claims.Environment` MUST equal one of its entries.
7. `claims.EventName` is in `rule.AllowedEvents` (default `["push", "schedule", "workflow_dispatch", "release", "merge_group"]`). `pull_request_target` MUST NOT match unless `rule.AllowPullRequestTarget` is `true` (§5.1.3).

1.2.3. **Deterministic precedence.** When multiple rules match, the rule with an explicit `environment` match wins over one without, an exact workflow filename wins over a wildcard, and remaining ties break by rule declaration order in `settings/workflow_principals.yml`. Only the single winning rule's permissions apply; permissions across multiple rules MUST NOT union implicitly.

1.2.4. **RBAC role and scope binding.** The matched `workflow_principal` resolves to:
- RBAC role (`rbac.design.md`, [Task #2764](/tasks/2764)): `automation` (or bounded `writer` limited strictly to the rule's declared scopes and repositories; `admin` is forbidden on workflow principals).
- Effective agent API grants: the rule's `scopes` / `scope_set` narrowed to `rule.AllowedRepos` (default `[rule.Repo]`), `rule.AllowedTemplates`, and `rule.AllowedGrants`, enforced through `agentAPIGate` (`pkg/ui/agent_api_gate.go`).

### 1.3 External-agent token fallback (`gma_`) {#GA-3}

1.3.1. Workflows running where GitHub OIDC is unavailable MAY authenticate with a minted `gma_…` external-agent token (`Authorization: Bearer gma_…`, `pkg/ui/external_agents.go`) when `externalAgentApi` is enabled.

1.3.2. If a request carries both `X-Grain-Actions-OIDC` and `Authorization: Bearer gma_…`, `X-Grain-Actions-OIDC` wins (and fails closed on error per §1.1.2).

1.3.3. A `gma_` token used from GitHub Actions remains subject to all existing `external_agents.go` checks (revocation, expiry, per-token RPM bucket, `max_concurrent`, scope set, repository limit, and journaled audit). Optional workflow metadata sent in `X-Grain-GitHub-Run` (`<repo>/<run_id>/<attempt>`) on a `gma_` call is recorded as unverified operational metadata (`LinkGitHubWorkflowRun`), never as identity.

### 1.4 Ingress behind IAP {#GA-4}

1.4.1. **Narrow non-IAP route (`/api/actions/v1/*`).** Grain MUST serve a restricted Actions API mux under `/api/actions/v1/` containing **only** these five routes:

| Method & path | Underlying handler | Required scope |
| --- | --- | --- |
| `POST /api/actions/v1/tasks` | Task creation (§3.1) | `tasks:from-template` (with `template`) or `tasks:write` |
| `POST /api/actions/v1/reviews` | PR review request (§4.1) | `reviews:request` |
| `GET /api/actions/v1/tasks/{id}` | Task status read (`handleGetTask` subset) | `tasks:read` |
| `GET /api/actions/v1/tasks/{id}/wait` | SSE / long-poll wait (§3.2) | `tasks:read` |
| `POST /api/actions/v1/tasks/{id}/stop` | Task stop (§3.3) | `tasks:write` or matching `(principal, run_id)` self-stop |

1.4.2. **Hardening of `/api/actions/v1/*`.** Every request to `/api/actions/v1/*` MUST obey:
- **No UI, no cookies, no access code:** Browser session cookies (`grain_access`, IAP cookies) and single-tenant access codes MUST be rejected with `401 Unauthorized` on `/api/actions/v1/*`. Only verified `github-oidc` tokens (and `gma_` tokens when `actionsAllowExternalTokens: true` is explicitly configured; default `false` on the non-IAP prefix) are accepted.
- **Disabled when unconfigured:** When zero `workflow_principal` rows are enabled (and `actionsAllowExternalTokens` is `false`), `/api/actions/v1/*` MUST answer `404 Not Found` before parsing bodies.
- **Body and pre-auth rate limits:** Request bodies on `/api/actions/v1/*` MUST be capped at `64 KiB` (`http.MaxBytesReader`). A pre-authentication per-client-IP token bucket (default `120 req/min`, burst `30`) MUST run ahead of JWT parsing, answering `429 Too Many Requests` with `Retry-After` on exhaustion.
- **Edge relay compatibility:** `cmd/github-webhook` (`deploy/kubernetes/github-webhook.yaml`) SHOULD gain an optional `/api/actions/v1/*` reverse-proxy forwarder (verifying JWT structure and rate-limiting at the unprivileged edge pod before forwarding to `Service/grain`) so `Service/grain` itself never needs an `iap.enabled: false` `BackendConfig`.

1.4.3. **Dual-token IAP path (`Proxy-Authorization`).** When a deployment routes all traffic through Google Cloud IAP with no non-IAP `/api/actions/v1/*` route, callers (`grain-task` via `iap-id-token`, and `grain` CLI via `--iap-token` / ADC) MUST send the Google Cloud service-account ID token in `Proxy-Authorization: Bearer <gcp-id-token>` and the GitHub OIDC JWT in `X-Grain-Actions-OIDC: <jwt>` (or `gma_…` in `Authorization: Bearer`). When IAP verifies the outer service account and `X-Grain-Actions-OIDC` is also present, grain MUST verify `X-Grain-Actions-OIDC`, authorize the request strictly under the narrower `workflow_principal` (never granting the GCP service account's human/operator privileges), and log both identities in the audit record.

---

## 2. Attribution and observability

### 2.1 Workflow principal attribution and owner inheritance {#GA-5}

2.1.1. **`AuthSource` and `Principal`.** `pkg/model` (`operator-attribution.spec.md` §1.1.3) adds `AuthSourceGitHubOIDC = "github-oidc"`. A verified workflow call sets the request context's `Attribution` to:
- `Actor`: `model.Principal{Kind: model.PrincipalAutomation, ID: "gha:" + repo + ":" + workflowFile, Source: "github-oidc"}` (for example `gha:acme/widgets:.github/workflows/triage.yml`).
- `OnBehalfOf`: derived solely from verified OIDC `actor` and `actor_id` claims:
  - When `claims.Actor` does not end in `[bot]`: `&model.Principal{Kind: model.PrincipalHuman, ID: "@" + strings.ToLower(claims.Actor), Source: "github-oidc"}`.
  - When `claims.Actor` ends in `[bot]`: `&model.Principal{Kind: model.PrincipalAutomation, ID: strings.ToLower(claims.Actor), Source: "github-oidc"}`.

2.1.2. **No caller-supplied human attribution.** Any `author`, `creator`, `owner`, or `onBehalfOf` field in a request body or unsigned header from a workflow principal MUST be ignored or rejected with `400 Bad Request` (`operator-attribution.spec.md` §2.3).

2.1.3. **Task `Creator`, `Owner`, and inheritance.** On a root task filed by a workflow principal:
- `Task.Creator` MUST be set to `Actor.ID` (`"gha:<repo>:<workflow>"`) with `CreatorSource = "github-oidc"`.
- `Task.Owner` (`attribution.design.md`) MUST be fixed at creation to `rule.DefaultOwner` when configured on the `workflow_principal` rule (resolving `oncall` at filing time per `attribution.design.md`), and otherwise to `Actor.ID` paired with the triggering human's Git identity (`<actor_id>+<actor>@users.noreply.github.com`) when `OnBehalfOf.Kind == PrincipalHuman`.
- Every child task spawned from that root task (via `propose_task`, decomposition, follow-up, repair, or review) MUST inherit `Task.Owner` unchanged (`attribution.design.md` §*Inheritance down task trees*).
- On internal PRs opened by grain from a workflow-filed task, `Origin:` / `Requested-by:` trailers record the workflow principal and `OnBehalfOf`; `Co-authored-by:` is written only when `Task.Owner` resolves to a human identity (`attribution.design.md` §*Identity*).

### 2.2 Bidirectional run and task links {#GA-6}

2.2.1. **Task → Workflow run (`LinkGitHubWorkflowRun`).** `pkg/model` adds `LinkGitHubWorkflowRun = "github-workflow-run"`. Every task filed or review requested with `github-oidc` MUST record a link whose target is `<server_url>/<repository>/actions/runs/<run_id>/attempts/<run_attempt>` (where `<server_url>` is derived from `claims.Issuer`: `https://github.com` for `https://token.actions.githubusercontent.com`, and `https://<ghe-host>` for `https://<ghe-host>/_services/token`). The Task Detail UI (`/tasks/<id>`) and `grain get <id>` MUST display this link.

2.2.2. **Workflow run → Task (`$GITHUB_STEP_SUMMARY` and annotations).** The `grain-task` action MUST:
1. Immediately after receiving `task-id` from grain, append a Markdown summary block to `$GITHUB_STEP_SUMMARY` containing a clickable link `[Task #<id>](<grain-url>/tasks/<id>)`, the mode (`task` or `review`), repository/ref, and workflow principal, and emit `::notice title=Grain task #<id>::<grain-url>/tasks/<id>`.
2. When `wait: true` finishes, append the final `state`, `outcome`, elapsed wait duration, `pr-url` (when non-empty), and `closing-note` (or a `::warning::` annotation on `timeout`, `paused`, or `infra-error`).

2.2.3. **Check Run → Task.** Every `grain/review` Check Run created or updated by grain MUST set `details_url` to `<grain-url>/tasks/<review-task-id>` (when an external base URL is configured on the deployment) and `external_id` to `task:<review-task-id>`.

---

## 3. Creating and waiting for tasks from workflows

### 3.1 Task creation API and idempotency {#GA-7}

3.1.1. **Request schema (`POST /api/actions/v1/tasks`).** Accepts JSON (max `64 KiB`) and maps into `POST /api/tasks` / `POST /api/tasks/from-template`:

```json
{
  "repo": "acme/widgets",
  "base": "main",
  "ref": "refs/heads/main",
  "sha": "e4a1b2c3...",
  "template": "triage-flake",
  "fields": {"test_name": "TestQuotaOffByOne"},
  "title": "",
  "body": "",
  "grants": ["ci-read"]
}
```

- Exactly one of `template` or (`title` + `body`) MUST be non-empty (`400 Bad Request` otherwise).
- When `template` is set, the caller MUST hold `tasks:from-template` (covering that template in `AllowedTemplates`) or `tasks:write` on `repo`.
- When `title` / `body` is set without `template`, the caller MUST hold `tasks:write` on `repo`.
- `grants`, if non-empty, MUST be a subset of the workflow principal's `AllowedGrants` (and never a person-only or dangerous grant, §5.1); otherwise `403 Forbidden` naming the disallowed grant.
- Whether the task lands `queued` (approved) or `proposed` (held for human approval) is determined by `rule.LandsQueued` on the `workflow_principal` (default `true` for template-bound rules, `false` for free-form `tasks:write` rules unless explicitly set `true`). Callers cannot set `approved: true` or `autoMerge: true` to escalate past the rule.

3.1.2. **Idempotency (`Idempotency-Key`).** `POST /api/actions/v1/tasks`, `POST /api/actions/v1/reviews`, `POST /api/tasks`, and `POST /api/tasks/from-template` MUST honour an optional `Idempotency-Key` HTTP header:
- Validated as `1..255` visible ASCII characters (`0x21..0x7E`); invalid values return `400 Bad Request`.
- Stored in table `task_idempotency(principal_scope TEXT, idempotency_key TEXT, request_sha256 TEXT, task_id TEXT, created_at TEXT, PRIMARY KEY (principal_scope, idempotency_key))`, where `principal_scope` is `<principal.Kind>:<principal.Source>:<principal.ID>`. Rows older than 30 days are pruned by store maintenance.
- `request_sha256` is the hex SHA-256 of canonical JSON `{endpoint, repo, base, ref, sha, template, fields, title, body, grants}`.
- On a first call, the task insert and `task_idempotency` insert execute in **one database transaction**, returning `201 Created` with the `TaskResponse`.
- On a repeat call with the same `(principal_scope, idempotency_key)`:
  - If `request_sha256` matches: grain MUST NOT create a second task; it reads the current `TaskResponse` for `task_id` and returns `200 OK` with header `Idempotency-Replayed: true`.
  - If `request_sha256` differs: grain MUST return `409 Conflict` with `{"code": "idempotency_key_conflict"}`.

3.1.3. **Default workflow idempotency key.** When `idempotency-key` input is omitted in `mode: task`, `grain-task` MUST compute `gha:<repository_id>:<run_id>:<run_attempt>:<job>:<action_id>`. Retrying an HTTP call or re-entering within the same attempt replays the same key; clicking "Re-run failed jobs" increments `run_attempt` and files a fresh task unless the workflow explicitly overrides `idempotency-key` without `run_attempt`.

### 3.2 Waiting: SSE and bounded long-polling {#GA-8}

3.2.1. **Endpoint.** `GET /api/tasks/{id}/wait` and `GET /api/actions/v1/tasks/{id}/wait` require scope `tasks:read` on task `{id}`'s target repository. Query parameters:
- `until`: comma-separated subset of `closed`, `merged`, `pr-opened` (default `closed`; `external-cli.design.md` callers on `/api/tasks/{id}/wait` MAY also pass `awaiting-reply` or `ci-done`).
- `timeout`: server-side hold cap for a single HTTP request, clamped to `1s..55s` (default `50s`, staying safely inside 60s load-balancer / IAP idle timeouts).
- `after_seq`: last observed task event sequence number (`uint64`, default `0`).

3.2.2. **Wire modes.**
- **Long-poll JSON (default / `Accept: application/json`):** If `until` is already satisfied (or the task is already in a terminal non-matching state such as `closed` unmerged while waiting for `merged`, or `paused` on a usage limit with `resetAt`), responds immediately `200 OK` with `WaitTaskResponse`. Otherwise blocks on grain's in-process store event notifier until a matching transition occurs or `timeout` elapses, then responds `200 OK` with `"met": true|false` and `"seq": <latest_seq>`.
- **Server-Sent Events (`Accept: text/event-stream`):** Streams `event: state` JSON frames whenever `{id}` changes state, opens a PR, pauses, or closes, plus `: heartbeat` comments every `15s`, closing cleanly after sending `event: done` once `until` is met or `55s` elapses (resumable via `Last-Event-ID: <seq>`).

```json
{
  "taskId": "2801",
  "taskUrl": "https://grain.example.com/tasks/2801",
  "state": "closed",
  "outcome": "merged",
  "met": true,
  "seq": 42,
  "prUrl": "https://github.com/acme/widgets/pull/914",
  "closingNote": "Fixed off-by-one in quota bucket reset.",
  "verdict": "",
  "pause": null
}
```

3.2.3. **`until` satisfaction rules.**
- `pr-opened`: `met: true` as soon as the task has an open or merged `LinkFixes` pull request (`prUrl != ""`); terminates early with `met: false` and terminal `outcome` (`closed`, `nothing-needed`, `failed`, `cancelled`) if the task finishes without opening a PR.
- `closed`: `met: true` when `state == "closed"` (with `outcome` set to `merged`, `nothing-needed`, `closed`, `cancelled`, or in review mode `approve` / `request_changes` / `comment`) or `state == "failed"`.
- `merged`: `met: true` when `PrMergedAt != nil` (`outcome: "merged"`); terminates early with `met: false` if the task closes unmerged or its PR closes without merging.

3.2.4. **Rate-limit budget and OIDC token refresh.**
- `GET .../wait` MUST observe store transitions in memory/SQLite/Postgres and MUST NOT issue any outbound GitHub API requests.
- Active wait requests MUST be accounted against a dedicated per-principal `max_concurrent_waits` semaphore (default `20`) rather than consuming the principal's mutating-request RPM token bucket on long-poll reconnects; reconnects faster than `1 req / 5s` per task are throttled (`429` with `Retry-After: 5`).
- Before opening each reconnect, `grain-task` and `grain` CLI MUST check the cached OIDC JWT's `exp` claim and mint a fresh OIDC token whenever `now + 60s >= exp`, so multi-hour waits never fail at the 5-minute OIDC token expiry boundary.

### 3.3 Timeouts and workflow cancellation {#GA-9}

3.3.1. **Wait timeout.** When the caller's configured `timeout` elapses without `met: true`, the client stops reconnecting, records the last observed `state` and `outcome: "timeout"`, writes `$GITHUB_STEP_SUMMARY`, and leaves the grain task running unless `cancel-on-abort: true` and the step is aborting.

3.3.2. **Default cancellation (`cancel-on-abort: false`).** Cancelling or timing out the GitHub Actions workflow MUST NOT stop or close the grain task.

3.3.3. **Opt-in cancellation (`cancel-on-abort: true`).** When `cancel-on-abort: true` and the workflow job receives `SIGINT`/`SIGTERM` or executes its `post:` step with `cancelled()`, `grain-task` MUST send `POST /api/actions/v1/tasks/{id}/stop` with `{"reason": "Cancelled: GitHub Actions run <run_url> was aborted"}` (saving `task-id` in Actions step state `core.saveState("taskId", id)` during the main step so `post:` has it). Grain MUST permit `POST .../tasks/{id}/stop` when either the principal holds `tasks:write` on `{id}`'s repository OR `{id}` carries `Origin.Attribution.Actor == principal` and `LinkGitHubWorkflowRun` matching the caller's `<repository>/actions/runs/<run_id>/`.

### 3.4 The `grain-task` reusable action {#GA-10}

3.4.1. **Distribution.** Source lives at `actions/grain-task/` in `ai-on-gke/grain` (TypeScript compiled into a self-contained Node 20 entry bundle with `main: dist/index.js` and `post: dist/post.js`, `post-if: cancelled()`) and is published on release to `ai-on-gke/grain-task`.

3.4.2. **No shell execution.** `grain-task` MUST make HTTPS calls and write `$GITHUB_OUTPUT` / `$GITHUB_STEP_SUMMARY` directly via Node APIs (`@actions/core` / `fetch`). It MUST NOT invoke `/bin/sh` with workflow inputs, PR titles, PR bodies, or closing notes.

3.4.3. **Inputs and outputs.** `action.yml` MUST declare:
- Inputs: `grain-url` (required), `mode` (`task` | `review`, default `task`), `template`, `fields`, `title`, `body`, `repo` (default `${{ github.repository }}`), `base`, `ref` (default `${{ github.sha }}`), `pr-number`, `head-sha`, `grants`, `wait` (default `false` in `task`, `true` in `review`), `until` (`closed` | `merged` | `pr-opened`, default `closed`), `timeout` (default `30m`), `poll-interval` (default `15s`, clamped `>= 5s`), `cancel-on-abort` (default `false`), `idempotency-key`, `on-infra-failure` (`neutral` | `fail`, default `neutral`), `token`, `oidc-audience`, `iap-id-token`.
- Outputs: `task-id`, `task-url`, `state`, `outcome`, `pr-url`, `closing-note`, `verdict`.
- Exit code in `mode: task`: `0` when `wait: false` and creation succeeded, when `until` was met (except `state == "failed"` or `outcome == "request_changes"`, which exit `1`), or when an infra/pause/timeout occurred under `on-infra-failure: neutral`; `1` on auth/validation/permission errors (`400`/`401`/`403`/`409`), task failure (`failed`), `request_changes` verdict in blocking `review` mode, or infra/pause/timeout under `on-infra-failure: fail`.

### 3.5 Equivalent `grain` CLI surface {#GA-11}

3.5.1. `pkg/cli` MUST support `--auth github-oidc` (and auto-select it when `$GRAIN_SERVER` is HTTPS, no `--token` is given, and `$ACTIONS_ID_TOKEN_REQUEST_URL` + `$ACTIONS_ID_TOKEN_REQUEST_TOKEN` are set in the environment). It fetches `GET ${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=${url.QueryEscape(audience)}` with `Authorization: bearer ${ACTIONS_ID_TOKEN_REQUEST_TOKEN}` and sends the returned `value` in `X-Grain-Actions-OIDC`.

3.5.2. `grain create` (`pkg/cli/taskcmd.go`) MUST accept `--idempotency-key <key>`, `--ref <ref>`, `--grant <cap>` (repeatable), `--wait`, `--until <closed|merged|pr-opened>`, and `--timeout <dur>`. `grain pr review <owner/repo#number> --head-sha <sha> [--wait] [--timeout <dur>] [--json]` invokes `POST /api/actions/v1/reviews` (or `/api/pull-requests/review`).

---

## 4. Pull request reviews (`grain/review`)

### 4.1 Requesting a review: GitHub App events and `mode: review` {#GA-12}

4.1.1. **GitHub App webhook triggers.** When `RepoConfig.PRReviewMode` (`§5.2.2`) is `advisory` or `blocking` and the repository's GitHub credential is a GitHub App:
- `handlePullRequestEvent` (`pkg/orchestrator/githubevents.go`) MUST evaluate `pull_request` webhook actions `opened`, `synchronize`, `reopened`, `ready_for_review`, `labeled`, and `review_requested`, plus `@<slug> review` comments (`issue_comment.created`, `pull_request_review.submitted`, `pull_request_review_comment.created`).
- Pull requests opened by grain itself (`openGrainTaskForPR != nil`, where grain's internal `SyncReviews` already owns the second-agent review) MUST NOT have a duplicate external review filed; instead, `SyncReviews` MUST create and complete the `grain/review` Check Run on every head of a grain-owned PR so `contributions.spec.md` §2.6.4 holds uniformly across internal and external PRs.

4.1.2. **Workflow `mode: review` (`POST /api/actions/v1/reviews` and `POST /api/pull-requests/review`).**
- Requires scope `reviews:request` on `repo`.
- Request body: `{"repo": "acme/widgets", "prNumber": 42, "headSha": "a1b2c3d4..."}`.
- Grain MUST call `GetPullRequest(owner, name, prNumber)` on GitHub and verify that the PR is open and its current `HeadSHA` equals `req.HeadSHA` (full 40-hex commit SHA). If the PR has moved to another SHA or is closed, grain MUST return `409 Conflict` (`stale_head_sha`) without filing a task or posting a check on an outdated SHA.
- Deduplication key is `pr-review:<repo>#<prNumber>@<headSha>`: if an active (`queued`, `running`, `paused`) or completed clean/blocking review task already exists for that exact `(repo, prNumber, headSha)`, the endpoint returns `200 OK` with that task rather than filing a duplicate.

### 4.2 Author trust and maintainer label gate {#GA-13}

4.2.1. **Automatic-review trust check.** On automatic PR events (`opened`, `synchronize`, `reopened`, `ready_for_review`), the PR's author (`pull_request.user.login`, which MUST have `pull_request.user.type == "User"` unless listed in `RepoConfig.PRReviewAllowedBots`) is trusted for automatic review when either:
1. `CollaboratorPermission(owner, name, author)` is `admin`, `maintain`, or `write` (`github.IsTrustedCollaboratorPermission`); OR
2. `RepoConfig.PRReviewTrust` is `org_and_collaborators` (the default) and `IsOrgMember(owner, author)` (`GET /orgs/{org}/members/{username}` returning `204 No Content`, cached per `(org, username)` for 5 minutes; returns `false` on user-owned repos or `404`/`302`).

4.2.2. **Untrusted authors.** When the PR author is not trusted under §4.2.1 and no valid per-SHA maintainer approval exists for `head_sha`:
- Grain MUST NOT create or queue a sandbox review task.
- When `PRReviewMode == "blocking"`, grain MUST create (or update) the `grain/review` Check Run on `head_sha` with `status: "completed"`, `conclusion: "action_required"`, title `"Awaiting maintainer approval for grain/review"`, and summary instructing a maintainer to inspect the diff at `head_sha` and apply label `grain:review` (or comment `@<slug> review`). (`conclusion: "neutral"` MUST NOT be used here because GitHub treats `neutral` as passing required status checks.)

4.2.3. **Who may set the `grain:review` label (per-SHA binding).**
- When `pull_request.labeled` arrives with label matching `RepoConfig.PRReviewLabel` (default `"grain:review"`, case-insensitive), or when `@<slug> review` is commented on the PR, grain checks the **event sender's** permission (`ev.SenderLogin`, never `pull_request.user.login`).
- **Authorized senders:** a human GitHub account (`ev.SenderType == "User"`) who holds `admin`, `maintain`, or `write` permission on the repository (`IsTrustedCollaboratorPermission`). Users with `triage`, `read`, or `none` permission MUST NOT trigger the review; grain logs the refusal and updates the check summary noting that `write`, `maintain`, or `admin` permission is required.
- **Single-SHA scope:** Approval applies **only** to `ev.HeadSHA` present on the `labeled` / comment event. Grain SHOULD remove the one-shot `grain:review` label via `DELETE /repos/{o}/{r}/issues/{n}/labels/{label}` once the task for `ev.HeadSHA` is queued so a subsequent `synchronize` push (`new_sha`) by the untrusted author returns cleanly to `action_required` until a maintainer inspects `new_sha` and re-applies `grain:review`.
- A `POST /api/actions/v1/reviews` call from a workflow principal for an untrusted author's PR MUST likewise stay in `action_required` (refusing auto-dispatch with `403 untrusted_pr_author`) unless a maintainer has approved `head_sha`.

### 4.3 Review task composition, conformance, and companion security review {#GA-14}

4.3.1. **Base-branch-only template and settings.** When composing a review task for `(repo, pr_number, head_sha)` against `base_ref`:
- Grain MUST read `.grain/templates/review.yml` (`contributions.spec.md` §2.8), `.grain/settings.yml` (`prompt-extension`, `setup-command`), and `.grain/triggers/*.yml` exclusively from the **trusted `base_ref` tree** (via git proxy / forge read of `refs/heads/<base_ref>`), **never** from `head_sha`.
- When `.grain/templates/review.yml` is absent on `base_ref`, grain uses built-in template `system:pr-review`.

4.3.2. **Review prompt requirements.** Both `system:pr-review` and `.grain/templates/review.yml` MUST instruct the reviewer to:
1. Inspect `git diff <merge_base>..<head_sha>` and classify every changed file under `contributions.spec.md` §2.1–2.2.
2. Locate governing designs (`design/design/*.design.md`, `design/playbook/*.playbook.md`) and specs (`design/spec/**/*.spec.md`), check conformance item by item, cite `{#ID}` requirement IDs on inline findings, and request changes when code intentionally diverges from a design or spec without updating it (`contributions.spec.md` §2.8.2).
3. Read CI status and any failing job logs on `head_sha` (`grain ci list`, `grain ci log`).
4. Check prior `<slug>[bot]` review threads on the PR and resolve threads fixed in `head_sha` (§4.6).
5. Submit inline comments via `add_review_comment` / `grain pr review-comment` and conclude with `submit_review_verdict`.

4.3.3. **Companion security review.** When any file changed between `merge_base` and `head_sha` matches `pkg/trustboundary` (or the changed-file list cannot be read, e.g. >3000 files), OR when the PR author was outside §4.2.1 (label-triggered external contributor PR), grain MUST also file a companion `system:security-review` task bound to `(repo, pr_number, head_sha)`. The single `grain/review` Check Run on `head_sha` aggregates all required review tasks for that head:
- Remains `in_progress` until every non-cancelled review task for `head_sha` has finished (or fails immediately once one finishes with `request_changes` if configured, though letting both finish surfaces all findings in one pass—grain SHOULD wait for both to finish before posting the combined check summary).
- Concludes `success` only if **all** required review tasks for `head_sha` concluded `approve`; concludes `failure` (in `blocking` mode) if **any** concluded `request_changes`.

### 4.4 Review tools, inline comments, and verdict submission {#GA-15}

4.4.1. **`add_review_comment` (`grain pr review-comment`).** Accepts:
- `path` (string, repo-relative file path, required for inline comments),
- `line` (positive integer on the `RIGHT` side of `head_sha`, or `LEFT` when `side: "LEFT"`),
- `start_line` (optional positive integer `< line` for multi-line diff comments),
- `side` (`"RIGHT"` default | `"LEFT"`),
- `severity` (`"blocking"` default | `"nit"`),
- `body` (non-empty Markdown string).

Before submitting to GitHub, `relayReviewFeedback` (`pkg/orchestrator/reviewfeedback.go`) MUST validate each comment's `(path, side, start_line, line)` against the actual diff hunks of `head_sha`. Any comment targeting a file or line outside `head_sha`'s diff hunks MUST NOT be sent in GitHub's `comments[]` array (which would cause GitHub to reject the entire review with `422 Unprocessable Entity`); instead, grain appends it under an **"Additional findings (outside diff hunks)"** section of the review summary body with `**path:line (severity):**` formatting.

4.4.2. **`submit_review_verdict` (`grain pr verdict`).** Review runs (`IntentReview`) gain a turn-ending tool `submit_review_verdict`:
- Parameters: `verdict` (`"approve"` | `"request_changes"` | `"comment"`, required), `summary` (non-empty Markdown string, required).
- **In-turn consistency rail:**
  - If `verdict == "approve"` and the run has staged one or more `add_review_comment` calls with `severity == "blocking"`, the tool handler MUST return `isError: true` explaining that `"approve"` cannot accompany blocking findings (the agent must either re-stage non-blocking remarks as `severity: "nit"` or submit `"request_changes"`).
  - When `RepoConfig.PRReviewMode == "blocking"`, `verdict == "comment"` MUST be refused with `isError: true` instructing the reviewer to choose `"approve"` or `"request_changes"` so a blocking gate always receives a decisive verdict.
- **Fallback if omitted:** If a review run terminates normally having called `comment_on_issue` or `nothing_needed` without calling `submit_review_verdict`, grain derives `verdict = "request_changes"` when >= 1 `blocking` finding was staged, and `verdict = "approve"` when 0 `blocking` findings were staged.

### 4.5 Posting the verdict: `grain/review` Check Run and GitHub PR Review {#GA-16}

4.5.1. **Dual publication.** When all review tasks for `(repo, pr_number, head_sha)` settle with a verdict (as opposed to an infra error, §6.1), grain MUST publish **both** from the GitHub App installation token (`<slug>[bot]`):

1. **GitHub Pull Request Review** (`POST /repos/{owner}/{name}/pulls/{number}/reviews`, setting `"commit_id": head_sha`):
   - `verdict == "approve"` → `event: "APPROVE"`, body = formatted summary + task link + any out-of-hunk findings, `comments` = in-hunk inline findings (`severity: "nit"` prefixed with `*(nit)* `).
   - `verdict == "request_changes"` → `event: "REQUEST_CHANGES"` when `PRReviewMode == "blocking"` (`event: "COMMENT"` when `PRReviewMode == "advisory"`), with summary and inline comments.
   - `verdict == "comment"` (`advisory` mode only) → `event: "COMMENT"`, with summary and inline comments.
   - If GitHub rejects `APPROVE` or `REQUEST_CHANGES` with `422` because the same GitHub App identity opened the pull request (`isOwnPullRequestReviewError` in `pkg/github/bot.go`), grain MUST retry once with `event: "COMMENT"` while still setting the authoritative Check Run conclusion below.

2. **GitHub Check Run (`grain/review`)** (`PATCH /repos/{owner}/{name}/check-runs/{check_id}`):
   - `verdict == "approve"` → `status: "completed"`, `conclusion: "success"`.
   - `verdict == "request_changes"` → `status: "completed"`, `conclusion: "failure"` when `PRReviewMode == "blocking"` (`conclusion: "neutral"` when `PRReviewMode == "advisory"`).
   - `verdict == "comment"` → `status: "completed"`, `conclusion: "neutral"`.
   - `output.title` names the verdict and finding counts (e.g. `"Approved (1 nit) — task 2804"` or `"Changes requested (3 blocking findings) — task 2804"`), `output.summary` holds the Markdown review summary (truncated to GitHub's `65535`-byte Check Run field limit with a link to `/tasks/<id>`), and `details_url` points to `<grain-url>/tasks/<id>`.

4.5.2. **Human-approval non-equivalence (normative invariant).** Grain's GitHub App `APPROVE` review does not satisfy GitHub ruleset `required_approving_review_count` or `CODEOWNERS` human review rules. Implementations MUST NOT use a personal access token (PAT) of a human user to post automated PR reviews in order to bypass human review counts (`contributions.spec.md` §1, §2.5.4).

### 4.6 Re-review on push, superseding, and thread resolution {#GA-17}

4.6.1. **Superseding on `synchronize`.** When a pull request's head moves from `old_sha` to `new_sha`:
1. Every open (`proposed`, `queued`, `running`, `paused`) external review task for `(repo, pr_number)` bound to a SHA `!= new_sha` MUST be stopped and closed (`reason: "superseded by " + new_sha[:12]`), tearing down its sandbox immediately.
2. Every non-completed `grain/review` Check Run recorded on `(repo, pr_number)` for `old_sha` MUST be updated to `status: "completed"`, `conclusion: "cancelled"`, `output.title: "Superseded by " + new_sha[:12]`.
3. A fresh review evaluation (§4.2, §4.8) runs for `new_sha`.

4.6.2. **Thread resolution across pushes (`github.com` and GitHub Enterprise Server).**
- When composing the review prompt for `new_sha`, grain lists unresolved review threads on `(repo, pr_number)` whose root comment author is `<slug>[bot]` (`ListReviewThreads` in `pkg/github`), passing each thread's `thread_id`, `path`, `line`, and body into the review prompt.
- The review run MAY call `resolve_review_thread(thread_id, reply?)` (`grain pr resolve <thread_id>`) **only** for threads in that `<slug>[bot]`-authored set on `(repo, pr_number)`. Any attempt to resolve a thread opened by a human or another bot, or on another PR, MUST be refused (`403 Forbidden`) by the sandbox/MCP proxy.
- `ResolveReviewThread` executes GraphQL `resolveReviewThread` against `/graphql` on `api.github.com` and `/api/graphql` on GitHub Enterprise Server (`RealTransport.graphqlPath`, plus full GHE GraphQL thread listing/resolution parity once [Task #2634](/tasks/2634) lands). If GraphQL thread listing or resolution fails with `404`/`501` on an older GHE appliance before [Task #2634](/tasks/2634) is available, grain MUST degrade gracefully by posting a REST review-comment reply (`"Addressed in <new_sha[:12]>"`) without failing the review task.

4.6.3. **Clearing prior `REQUEST_CHANGES`.** When `new_sha` finishes with `verdict == "approve"`, submitting `APPROVE` as `<slug>[bot]` (§4.5.1) replaces `<slug>[bot]`'s prior `REQUEST_CHANGES` review state on GitHub automatically.

### 4.7 Trust boundary and prompt-injection hardening {#GA-18}

4.7.1. **Hard grant and isolation ceiling on review tasks.** Regardless of repository defaults (`Config.DefaultCapabilities`, `RepoConfig.DefaultCapabilities`) or template contents, the daemon MUST enforce all of the following on every external PR review task:
- **Zero `writes:` grants:** `Task.Target` is read-only; the sandbox git proxy (`pkg/gitproxy`) MUST refuse all `git-receive-pack` pushes and ref updates with `403 Forbidden`.
- **Checkout isolation:** The sandbox checks out `base_ref` and fetches `refs/pull/<number>/head` pinned to exact `head_sha` (`git rev-parse FETCH_HEAD == head_sha`, failing the run if the fetched tip moved or mismatched). Base-branch `setup-command` (if any) runs on the clean `base_ref` tree *before* `head_sha` is fetched or checked out, never on `head_sha`'s tree.
- **No secrets or external network:** `Task.NetworkAccess` MUST be `none` (sandbox egress restricted to grain's internal proxy only). All `secrets:*`, `gcp-key`, `request_secret`, host-script, deploy, and secret-bearing capability grants MUST be stripped at task creation and refused at dispatch.
- **Strict tool allowlist:** Only sandbox file/command tools (`read_file`, `run_command` inside the secretless `network: none` VM), `ci-read` commands strictly scoped to `(repo, head_sha)`, `grain pr status` strictly scoped to `(repo, pr_number)`, `add_review_comment`, `resolve_review_thread` (bot's own threads on `pr_number` only), and `submit_review_verdict` (plus `comment_on_issue` fallback) are exposed. `propose_task`, `ask_question`, `request_secret`, `grain pr open`, and all mutating `api_*` tools MUST NOT be registered on external PR review runs.
- **Cross-repo `reads:`:** For label-triggered reviews of untrusted authors (§4.2.3), any cross-repository `reads:` grants from defaults MUST be stripped unless explicitly configured in `RepoConfig.PRReviewUntrustedReads`.

4.7.2. **Untrusted-content quoting.** Every piece of PR-supplied text interpolated into the review prompt—`prTitle`, `prBody`, commit messages, branch names, review thread comments, `@<slug> review` extra notes, and CI log excerpts—MUST be enclosed in a per-run random 128-bit hex nonce boundary:

```text
<untrusted-pr-content source="pull_request.body" nonce="9f3a...c102">
...escaped content (any literal closing tag with that nonce stripped)...
</untrusted-pr-content>
```

The system/preamble prompt outside the tags MUST state that text inside `<untrusted-pr-content>` is untrusted input under inspection and MUST NOT be followed as instructions, tool directives, or verdict overrides.

### 4.8 Cost controls: drafts, rate limits, concurrency, and debounce {#GA-19}

4.8.1. **Draft PRs.** When `RepoConfig.PRReviewSkipDrafts` is `true` (default) and `pull_request.draft == true`:
- `opened`, `synchronize`, and `reopened` MUST NOT file a review task.
- Grain SHOULD post `grain/review` on `head_sha` with `status: "completed"`, `conclusion: "skipped"`, title `"Skipped on draft pull request"`.
- When `pull_request.ready_for_review` arrives (with `draft == false`), grain MUST immediately evaluate `head_sha` for review. (An explicit `@<slug> review` comment by a trusted collaborator MAY still review a draft on demand.)

4.8.2. **Synchronize debounce.** On `pull_request.synchronize`, grain creates the `queued` `grain/review` Check Run immediately and sets `Task.NotBefore = now + PRReviewDebounceSeconds` (default `15s`, `0` disables), so rapid consecutive pushes cancel the not-yet-started task during the debounce window without booting a sandbox.

4.8.3. **Rate limits and caps.** Enforced per repository before queueing an automatic review:
- **Per-repository hourly cap:** `RepoConfig.PRReviewRateLimitPerHour` (default `60`, burst `15`).
- **Per-author hourly cap:** `RepoConfig.PRReviewAuthorLimitPerHour` (default `15` per GitHub login per repository).
- **Per-PR automatic head cap:** `12` distinct `head_sha` reviews per PR (`LinkGitHubEvent` count); past `12`, automatic `synchronize` events stop filing tasks, raise an inbox notification, and complete `grain/review` as `action_required` (`"Automatic review cap (12 pushes) reached; a maintainer may run @<slug> review"`).
- **Concurrent review cap:** `RepoConfig.PRReviewMaxConcurrent` (default `4` running external PR reviews per repository, and at most `50%` of deployment sandbox capacity unless idle). Excess queued reviews wait in dispatch order without failing.
- When the hourly repo or author rate limit is exceeded, grain defers the task (`NotBefore` set to the next token-bucket replenishment slot, up to `15m`; if backlog exceeds `2x` hourly limit, completes `grain/review` per §6.1's pause/rate rule).

### 4.9 Human overrides of `request_changes` verdicts {#GA-20}

4.9.1. **Route and RBAC gate.** `POST /api/pull-requests/{owner}/{name}/{number}/reviews/{headSha}/override` (scope `reviews:override`, person-only):
- Caller MUST be a verified human principal (`RequestActor(ctx).Actor.Verified() == true`, `Kind == PrincipalHuman`; agents, `gma_` tokens, workflow principals, and unverified single-tenant `operator` when `-ui-require-iap` or RBAC is active—Wait: on standalone access-code deployments with no IdP, `operator` holds `admin` in single-tenant mode per `rbac.design.md`; whenever RBAC roles apply, caller MUST hold RBAC **`writer` or `admin`** on `{owner}/{name}`). `viewer` principals MUST be refused with `403 Forbidden`.
- Request body: `{"reason": "<non-empty explanation>"}` (`1..2000` trimmed chars; empty `reason` returns `400 Bad Request`).

4.9.2. **Effects.** On a valid override for `headSha`:
1. Grain verifies `headSha` has an existing `grain/review` Check Run (or failed/action-required review record) on `{owner}/{name}#{number}`.
2. Updates that `grain/review` Check Run on `headSha` to `status: "completed"`, `conclusion: "success"`, `output.title: "Overridden by " + actorLabel`, and `output.summary` prepending `> **Review override** by <actorLabel> at <RFC3339>: <reason>\n\n` above the original review summary.
3. If `<slug>[bot]` submitted a `REQUEST_CHANGES` Pull Request Review on `{owner}/{name}#{number}`, calls `PUT /repos/{owner}/{name}/pulls/{number}/reviews/{review_id}/dismissals` with `message: "Overridden in grain by <actorLabel>: <reason>"`.
4. Appends an immutable `TaskEventReviewOverridden` event and task comment on the review task(s) for `headSha`, and writes a security audit record.
5. **Head-scoped:** The override applies strictly to `headSha`. Any push of a new commit (`new_sha`) starts fresh with no override.

---

## 5. Security boundaries and configuration

### 5.1 Workflow principal permission ceiling and fork/`pull_request_target` rules {#GA-21}

5.1.1. **Allowed scope ceiling (`model.WorkflowPrincipalAllowedScopes`).** A `workflow_principal` row MAY grant only scopes within:
- `tasks:from-template` (requires non-empty `allowed_templates`),
- `tasks:write` (always repo-limited; never grants `autoMerge: true` or `approved: true` past `lands_queued`),
- `tasks:read`,
- `ci:read`,
- `reviews:request`.

5.1.2. **Forbidden grants (hard invariant).** Neither `workflow_principal` configuration nor any runtime request from a workflow principal MAY hold, request, or attach:
- any dangerous scope (`capabilities:admin`, `secrets:write`, `settings:write`, `connections:write`, `merge:force`, `tasks:admin`, `deployment:upgrade`, `deployment:restart`, `deployment:reboot`),
- `secrets:read`, `reviews:override`, `merge:write` (including `submit`), `schedules:write`, `templates:write`, `releases:write`,
- any `model.IsPersonOnlyGrant` capability (`priority-dispatch`, etc.) or ad-hoc `gcp-key` / secret-bound capability in the action's `grants` input. (A pre-existing config-repo template authored and approved by a human in `grain/config` keeps its own template grants only if `workflow_principal.allow_privileged_templates` is `false` → refused when the template carries person-only/secret grants; templates triggered by workflows MUST NOT carry dangerous API scopes.)

5.1.3. **Forks and `pull_request_target`.**
- By default (`allow_pull_request_refs: false`, `allow_pull_request_target: false`), tokens with `ref` matching `refs/pull/*` or `event_name == "pull_request_target"` MUST be rejected (`403 Forbidden`).
- `grain state check` (`pkg/staterepo/statecheck`) and store validation MUST reject any `workflow_principal` rule that sets `allow_pull_request_target: true` if its resolved scopes contain anything outside `{"reviews:request", "tasks:read"}`. A `pull_request_target` workflow can therefore never file arbitrary tasks or templates, even if misconfigured by an operator.

### 5.2 Config-repository schemas (`workflow_principal` and `repo_config`) {#GA-22}

5.2.1. **`settings/workflow_principals.yml` and `tables/workflow_principal.json`.** Managed in the deployment's config repository (`pkg/declschema`, `pkg/staterepo`) and Settings → Access, never in `.grain/settings.yml`:

```yaml
workflow_principals:
  - id: widgets-main-triage
    description: Allow acme/widgets main triage workflow to file flake tasks
    enabled: true
    issuer: https://token.actions.githubusercontent.com
    repo: acme/widgets
    repository_id: "123456789"          # optional; recommended immutable pin
    repository_owner_id: "987654"       # optional; recommended immutable pin
    workflows:
      - .github/workflows/triage.yml
    refs:
      - refs/heads/main
    environments: []                    # optional GitHub Environment pin
    allowed_events: [schedule, workflow_dispatch, push]
    allow_pull_request_refs: false
    allow_pull_request_target: false
    scopes: [tasks:from-template, tasks:read]
    allowed_repos: [acme/widgets]
    allowed_templates: [triage-flake, release-notes]
    allowed_grants: [ci-read]
    default_owner: oncall               # optional; person, @handle, or oncall
    lands_queued: true
    rate_limit_rpm: 30
    max_concurrent: 5
    max_concurrent_waits: 20
```

5.2.2. **Per-repository PR review fields on `model.RepoConfig` (`tables/repo_config.json`).** All fields are rejected in `.grain/settings.yml` by `declschema.RepoSettingsSchema` (Authority / Verdict reasons):

| `RepoConfig` field / JSON key | Type & default | Allowed values |
| --- | --- | --- |
| `pr_review_mode` | `string`, `"off"` | `"off"`, `"advisory"`, `"blocking"` |
| `pr_review_trust` | `string`, `"org_and_collaborators"` | `"org_and_collaborators"`, `"collaborators"` |
| `pr_review_label` | `string`, `"grain:review"` | non-empty GitHub label name (`<= 50` chars) |
| `pr_review_allowed_bots` | `[]string`, `[]` | explicit bot logins allowed auto-review (e.g. `["renovate[bot]"]`) |
| `pr_review_skip_drafts` | `bool`, `true` | `true`, `false` |
| `pr_review_debounce_seconds` | `int`, `15` | `0..300` |
| `pr_review_rate_limit_per_hour` | `int`, `60` | `1..10000` |
| `pr_review_author_limit_per_hour` | `int`, `15` | `1..1000` |
| `pr_review_max_concurrent` | `int`, `4` | `1..100` |
| `pr_review_infra_conclusion` | `string`, `"neutral"` | `"neutral"`, `"failure"` |

---

## 6. Failure modes

### 6.1 Unreachable daemon, usage-limit pauses, and timeouts {#GA-23}

6.1.1. **Default neutral posture for infrastructure faults.** Unless explicitly configured strict (`on-infra-failure: fail` on `grain-task`, or `pr_review_infra_conclusion: "failure"` on `RepoConfig`), infrastructure faults MUST report **neutral** rather than failing workflow steps or blocking human PR merges:

| Fault | `grain-task` action (`on-infra-failure: neutral` / `fail`) | `grain/review` Check Run (`pr_review_infra_conclusion: neutral` / `failure`) |
| --- | --- | --- |
| **Grain unreachable / 5xx / network timeout before task creation** | Retries with exponential backoff + jitter (`1s..15s`, up to `60s`). On exhaustion: sets `state=""`, `outcome="infra-error"`, writes `::warning::` + step summary; exits **`0`** (`neutral`) or **`1`** (`fail`). | Inbound webhooks persist in `github-webhook`'s JSONL spool (`deploy/kubernetes/github-webhook.yaml`) and are processed in order once `grain` recovers; no check fails. |
| **Grain unreachable during `wait: true` (after task created)** | Reconnects with backoff (`poll-interval`..`60s`) until `timeout`. If still unreachable at `timeout`: outputs `task-id`, `task-url`, `outcome="infra-error"`; exits **`0`** (`neutral`) or **`1`** (`fail`). Task continues in grain unless `cancel-on-abort: true` on abort. | Check Run remains `in_progress` while daemon recovers; sandbox/run-host adoption (`pkg/runhost`) resumes or retries the run. |
| **Sandbox / infrastructure crash (`maxInfraRetries` exhausted)** | Wait receives terminal run state: if task ended as infra-aborted, sets `outcome="infra-error"`; exits **`0`** (`neutral`) or **`1`** (`fail`). | Completes Check Run with `conclusion: "neutral"` (`"failure"` when configured), title `"Review unavailable (infrastructure fault)"`, linking `/tasks/<id>`. Posts **no** `REQUEST_CHANGES` PR review. |
| **Model usage-limit / quota pause (`EndingUsageLimit`) or pipeline pause** | Daemon fails over across unpaused models in `default_model` first. If all candidate models are paused (`StoredUsageLimitPause`) with `resetAt` past caller's remaining `timeout` (or `timeout` expires while paused): outputs `state="paused"`, `outcome="paused"`; exits **`0`** (`neutral`) or **`1`** (`fail`). Task stays queued in grain. | Keeps Check Run `queued`/`in_progress` during failover and for up to `15m` grace (`prReviewPauseGrace`). If all models remain paused past `15m` (or `resetAt > now + 15m`), completes Check Run as `conclusion: "neutral"` (`"failure"` when configured) with title `"Review deferred (model quota limit until <HH:MM UTC>)"`. When the pause expires and the PR is still open at `head_sha`, grain automatically files/resumes a fresh check run. |
| **Wait timeout (action) / Run wall-clock timeout (review)** | Action `timeout` reached while task still `queued`/`running`: disconnects without stopping task (unless `cancel-on-abort: true` on abort), outputs `outcome="timeout"`, links `/tasks/<id>`; exits **`0`** (`neutral`) or **`1`** (`fail`). | Review run exceeds sandbox wall-clock budget (after at most 1 automatic retry): completes Check Run as `conclusion: "neutral"` (`"failure"` when configured), title `"Review timed out — task <id>"`, posting **no** `REQUEST_CHANGES` review. |
| **Authentication / authorization / validation error (`400`, `401`, `403`, `409`)** | **Not** an infra failure: always fails fast with `exit 1` and `::error::` naming the refusal. | N/A for valid webhooks; untrusted PR authors get `conclusion: "action_required"` (§4.2.2). |

---

## 7. Verification and requirement-to-test mapping

### 7.1 Normative test suite {#GA-24}

Every requirement ID in this specification MUST be covered by automated tests in the packages below before its implementation task closes:

| Req ID | Requirement summary | Automated tests to add / update |
| --- | --- | --- |
| `#GA-1` | GitHub OIDC JWT verification (`X-Grain-Actions-OIDC` & JWT `Authorization: Bearer`), JWKS caching, HTTPS-downgrade refusal, `alg`/key-type pinning, `iss`/`aud`/`exp`/`iat`/`nbf`, required GitHub claims, fail-closed 401 without fallback to `gma_` or access code | `pkg/ui/github_oidc_test.go`: `TestGitHubOIDCValidatesClaimsAndKeyTypes`, `TestGitHubOIDCRejectsAlgConfusionExpiredWrongAudWrongIss`, `TestGitHubOIDCFailsClosedEvenWithValidAccessCodeOrGmaToken`, `TestGitHubOIDCNonJWTBearerStillRoutesToGmaAndAccessCode`, `TestGitHubOIDCJWKSDowngradeAndUnknownKidRefetchFloor` |
| `#GA-2` | `workflow_principal` matching (`repository_id`, `repository_owner_id`, `job_workflow_ref` vs mutable `workflow`, `ref`, `environment`, `event_name`), specificity precedence, deny-by-default 403, role/scope narrowing | `pkg/ui/workflow_principal_test.go`: `TestWorkflowPrincipalMatchesImmutableRepoAndOwnerIDs`, `TestWorkflowPrincipalRejectsRenamedRepoIDMismatch`, `TestWorkflowPrincipalMatchesJobWorkflowRefNotDisplayName`, `TestWorkflowPrincipalPrecedenceDoesNotUnionRules` |
| `#GA-3` | `gma_` token fallback and `X-Grain-Actions-OIDC` precedence | `pkg/ui/github_oidc_test.go`: `TestActionsFallbackGmaTokenHonoursScopesAndRateLimits`, `TestOIDCHeaderBeatsGmaTokenAndFailsClosed` |
| `#GA-4` | `/api/actions/v1/*` 5-route allowlist, 404 when unconfigured, refusal of cookies/access-code, 64 KiB cap, pre-auth IP rate limit; dual-token `Proxy-Authorization` IAP + `X-Grain-Actions-OIDC` narrowing | `tests/security/actions_ingress_test.go`: `TestActionsIngressExposesOnlyFiveRoutesAndRejectsCookiesAndAccessCode`, `TestActionsIngressBodyCapAndPreAuthRateLimit`, `TestDualTokenIAPAndGitHubOIDCNarrowsToWorkflowPrincipal`; `pkg/ui/agent_api_internal_test.go` (`TestRouteScopesAreTotal`) |
| `#GA-5` | `AuthSourceGitHubOIDC`, `Actor` (`gha:<repo>:<workflow>`), `OnBehalfOf` (`@<actor>` vs bot), `Creator`, `Owner` + child inheritance, refusal of body human spoofing | `pkg/ui/workflow_attribution_test.go`: `TestWorkflowTaskAttributionSetsPrincipalAndActorOnBehalfOf`, `TestWorkflowChildTasksInheritOwner`, `TestWorkflowRequestCannotSpoofHumanAuthor` |
| `#GA-6` | `LinkGitHubWorkflowRun` on tasks, Check Run `details_url`/`external_id`, `$GITHUB_STEP_SUMMARY` & annotations | `pkg/ui/workflow_attribution_test.go`: `TestWorkflowTaskLinksBackToActionsRunAttempt`; `actions/grain-task/test/summary.test.ts` |
| `#GA-7` | `POST /api/actions/v1/tasks`, `Idempotency-Key` transactional deduplication (`201` → `200` replay, `409` payload mismatch, concurrent race safety, cross-principal isolation) | `pkg/ui/task_idempotency_test.go`: `TestIdempotencyKeyReplaysSameTaskAcrossRetries`, `TestIdempotencyKeyRejectsMutatedPayloadWith409`, `TestIdempotencyKeyIsolatedAcrossPrincipals`, `TestConcurrentDuplicateIdempotencyKeyCreatesOneTask` |
| `#GA-8` | `GET .../tasks/{id}/wait` SSE & 55s long-poll (`closed`, `merged`, `pr-opened`), zero GitHub API calls during wait, `max_concurrent_waits`, OIDC token refresh across >5m waits | `pkg/ui/wait_stream_test.go`: `TestTaskWaitLongPollAndSSEOutcomes`, `TestTaskWaitMakesZeroGitHubAPICalls`; `actions/grain-task/test/wait_oidc_refresh.test.ts` |
| `#GA-9` | Wait timeout leaves task running; `cancel-on-abort: false` vs `true` (`SIGINT` + `post:` self-stop bound to `(principal, run_id)`) | `pkg/ui/wait_stream_test.go`: `TestWorkflowSelfStopAllowedOnlyForMatchingPrincipalAndRunID`; `actions/grain-task/test/cancel.test.ts` |
| `#GA-10` | `grain-task` Node 20 action inputs/outputs, shell-free execution, `mode: task` & `mode: review` | `actions/grain-task/test/action.test.ts` (unit + mock grain HTTP/SSE server) |
| `#GA-11` | `grain` CLI `--auth github-oidc`, `--idempotency-key`, `grain create --wait`, `grain pr review` | `pkg/cli/github_oidc_cli_test.go`: `TestCLIGitHubOIDCMintsRefreshesAndWaits` |
| `#GA-12` | App webhook triggers (`opened`, `synchronize`, `reopened`, `ready_for_review`), `POST /api/actions/v1/reviews` live `head_sha` check (`409` on stale SHA), `grain/review` on grain-owned PRs | `pkg/orchestrator/pr_review_events_test.go`, `tests/e2e/github_pr_review_walkthrough_test.go` |
| `#GA-13` | Org-member (`GET /orgs/{org}/members/{user}`) + collaborator auto-trust; untrusted author `action_required` check; `grain:review` label accepted only from `write`/`maintain`/`admin` sender and bound to single `head_sha` | `pkg/orchestrator/pr_review_trust_test.go`: `TestOrgMemberAndCollaboratorAutoReviewed`, `TestExternalAuthorGetsActionRequiredNotNeutral`, `TestMaintainerLabelApprovesSingleHeadSHAOnlyAndRejectsTriageSender` |
| `#GA-14` | Base-branch-only `.grain/templates/review.yml` & `setup-command` read, conformance prompt, companion `system:security-review` on `pkg/trustboundary` & external authors, aggregated gate | `pkg/orchestrator/pr_review_compose_test.go`: `TestReviewReadsTemplateAndSetupFromBaseBranchNeverPRHead`, `TestTrustBoundaryPRFilesCompanionSecurityReviewAndAggregatesGate` |
| `#GA-15` | `add_review_comment` diff-hunk validation & summary fallback, `submit_review_verdict` (`approve`, `request_changes`, `comment`), blocking-finding consistency rail | `pkg/orchestrator/reviewfeedback_test.go`: `TestOutOfHunkInlineCommentsFallBackToSummaryWithout422`, `TestSubmitReviewVerdictRejectsApproveWithBlockingFindings` |
| `#GA-16` | Dual posting: `grain/review` Check Run (`success`/`failure`/`neutral`) + App PR Review (`APPROVE`/`REQUEST_CHANGES`/`COMMENT`), self-PR 422 fallback, bot-identity invariant | `pkg/orchestrator/pr_review_verdict_test.go`, `tests/e2e/github_pr_review_walkthrough_test.go` |
| `#GA-17` | `synchronize` cancels old-SHA run, supersedes old Check Run (`cancelled`), passes prior threads, resolves fixed `<slug>[bot]` threads on `github.com` (`/graphql`) & GHE (`/api/graphql`), refuses resolving human threads, clears prior `REQUEST_CHANGES` | `pkg/orchestrator/pr_review_supersede_test.go`, `pkg/github/graphql_endpoint_test.go`, `tests/e2e/github_pr_review_walkthrough_test.go` |
| `#GA-18` | Zero `writes:` (403 on git push), `network_access: none`, secret/grant stripping, base-before-fetch setup, tool allowlist (`propose_task`/`ask_question` absent), randomized-nonce `<untrusted-pr-content>` quoting | `tests/security/pr_review_isolation_test.go`: `TestExternalPRReviewSandboxCannotPushReadSecretsOrProposeTasks`, `TestAllExternalPRFieldsWrappedInNonceDelimiters` |
| `#GA-19` | Skipping drafts (`skipped` check, queued on `ready_for_review`), `synchronize` debounce, per-repo & per-author rate limits, 12-head cap, `max_concurrent` review cap | `pkg/orchestrator/pr_review_limits_test.go` |
| `#GA-20` | Human override endpoint: RBAC `writer`/`admin` required (`403` for `viewer`/agent/workflow), non-empty `reason` (`400`), `grain/review` → `success`, App `REQUEST_CHANGES` dismissed, audit event, invalidated on next push | `pkg/ui/pr_review_override_test.go`, `tests/e2e/github_pr_review_walkthrough_test.go` |
| `#GA-21` | Workflow-principal permission ceiling, person-only/dangerous grant refusal, fork ref & `pull_request_target` default refusal, `statecheck` restriction on `allow_pull_request_target` | `tests/security/workflow_principal_ceiling_test.go`, `pkg/staterepo/statecheck/workflow_principals_test.go` |
| `#GA-22` | `settings/workflow_principals.yml` + `tables/workflow_principal.json` + `repo_config` round-trip (`staterepo`), `declschema` refusal in `.grain/settings.yml` | `pkg/staterepo/workflow_principals_test.go`, `pkg/declschema/declschema_test.go` |
| `#GA-23` | Failure modes: unreachable daemon, usage-limit pause (`15m` grace + auto re-queue on lift), and timeout report `neutral` by default and `failure`/`exit 1` when configured | `pkg/orchestrator/pr_review_failures_test.go`, `actions/grain-task/test/failures.test.ts` |

---

## Open questions

Each open question mirrors [`github-actions-integration.design.md`](../../design/github-actions-integration.design.md#open-questions) with its recommended normative default already written into §1–§6 above so reviewers can ratify or flip each choice cleanly:

1. **Action distribution (`#GA-10`):** Keep source in `actions/grain-task/` inside `ai-on-gke/grain` and sync compiled Node 20 releases to companion repo `ai-on-gke/grain-task` (*recommended*, §3.4.1), or publish only from `.github/actions/grain-task` in `ai-on-gke/grain`?
2. **Ingress behind IAP (`#GA-4`):** Serve the 5-route `/api/actions/v1/*` non-IAP surface through an unprivileged `iap: false` relay alongside `/webhooks/github` while also supporting `Proxy-Authorization` GCP SA ID tokens (*recommended*, §1.4), or require GCP Workload Identity Federation + IAP on every deployment?
3. **Check conclusion for unreviewed external contributors (`#GA-13`):** Mark `grain/review` `action_required` (*recommended*, §4.2.2, blocking merge with an explicit badge) or leave it `queued` until a maintainer applies `grain:review`?
4. **Override surface in v1 (`#GA-20`):** Restrict overrides to Grain UI/CLI/API (`POST /api/pull-requests/.../override`) authenticated via grain's verified identity + RBAC `writer`/`admin` (*recommended*, §4.9.1), or also accept `@<slug> override-review <reason>` on GitHub from repository `admin`/`maintain` collaborators?
5. **Default check conclusion on grain outages and long model-quota pauses (`#GA-23`):** Default `pr_review_infra_conclusion` to `"neutral"` with per-repo `"failure"` opt-in (*recommended*, §6.1.1), or default `"failure"` on repositories with `pr_review_mode: "blocking"`?
6. **Grain-opened PRs (`#GA-12`):** Have `SyncReviews` post `grain/review` on PRs grain opened itself in the same rollout (*recommended*, §4.1.1, closing `contributions.spec.md` §2.6.4), or sequence that as a separate task?

---

## Implementation plan

Ordered follow-up tasks (not filed until this design and spec PR is reviewed and approved):

1. **Task A — `workflow_principal` schema, config-repo round-trip, and `GitHubActionsVerifier` (`#GA-1`–`#GA-3`, `#GA-5`, `#GA-21`, `#GA-22`):**
   - Store migration adding `workflow_principal` table and `AuthSourceGitHubOIDC`; `declschema`, `staterepo` (`settings/workflow_principals.yml` + `tables/workflow_principal.json`), and `statecheck` rules (including `allow_pull_request_target` scope cap); `GitHubActionsVerifier` in `pkg/ui` over `pkg/jwks`; workflow attribution (`Actor` + `OnBehalfOf` + `Owner` inheritance); security ceiling tests.
   - *Depends on:* this PR, [Task #2764](/tasks/2764) (RBAC roles).
2. **Task B — Idempotent task creation, SSE/long-poll wait, self-stop, and `/api/actions/v1/*` ingress (`#GA-4`, `#GA-6`–`#GA-9`, `#GA-23`):**
   - Store migration adding `task_idempotency` and `LinkGitHubWorkflowRun`; `Idempotency-Key` handling on `POST /api/tasks`, `from-template`, and `/api/actions/v1/*`; `GET /api/tasks/{id}/wait` (SSE + 55s long-poll over store pubsub with `max_concurrent_waits`); `(principal, run_id)` self-stop; `/api/actions/v1/*` mux + optional `cmd/github-webhook` edge relay + dual-token `Proxy-Authorization` test.
   - *Depends on:* Task A.
3. **Task C — CLI `--auth github-oidc` and the `grain-task` Node 20 action (`#GA-10`, `#GA-11`, `#GA-23`):**
   - `pkg/cli` GitHub OIDC credential provider + refresh, `--idempotency-key`, `grain create --wait`, `grain pr review`, and `grain pr override-review`; `actions/grain-task/` TypeScript implementation, unit/mock-server test suite, `$GITHUB_STEP_SUMMARY` renderer, `post:` cancellation hook, and release-sync workflow to `ai-on-gke/grain-task`.
   - *Depends on:* Task B.
4. **Task D — PR review core: `submit_review_verdict`, dual Check Run + App review, base-branch isolation, and prompt-injection hardening (`#GA-14`–`#GA-16`, `#GA-18`):**
   - Add `submit_review_verdict` (`grain pr verdict`) and blocking-finding consistency check; diff-hunk validation and out-of-hunk summary fallback in `reviewfeedback.go`; dual `grain/review` Check Run (`success`/`failure`/`neutral`) + `SubmitReview` (`APPROVE`/`REQUEST_CHANGES`/`COMMENT`); base-branch-only `.grain/templates/review.yml` and `setup-command` loading; randomized-nonce `<untrusted-pr-content>` quoting; hard sandbox grant/network/tool stripping; companion `system:security-review` gate aggregation; `grain/review` Check Run reporting in `SyncReviews` for grain-opened PRs.
   - *Depends on:* this PR.
5. **Task E — Automatic PR triggers, org-member trust, superseding, thread resolution, cost controls, and RBAC overrides (`#GA-12`, `#GA-13`, `#GA-17`, `#GA-19`, `#GA-20`, `#GA-23`, `#GA-24`):**
   - `RepoConfig` PR review columns + UI/staterepo plumbing; `opened`/`synchronize`/`reopened`/`ready_for_review` + `POST /api/actions/v1/reviews` live-SHA check; `IsOrgMember` with 5m cache + per-SHA `grain:review` maintainer label gate (`action_required`); old-SHA task stop + Check Run superseding + `<slug>[bot]` thread resolution (integrating [Task #2634](/tasks/2634) for GHE); draft skip, debounce, per-repo/per-author/12-head/concurrency limiters, and infra-neutral fallback; `POST /api/pull-requests/.../override` (`reviews:override`, RBAC `writer`/`admin`) + UI override dialog + end-to-end `-github-sim` walkthroughs.
   - *Depends on:* Task B, Task D, [Task #2634](/tasks/2634) (for GHE thread parity), [Task #2764](/tasks/2764) (RBAC).
