# GitHub Actions integration: workflow-driven tasks and pull request reviews

**Owner:** @bwsalmon

**The core rule:** GitHub Actions workflows authenticate to grain with short-lived GitHub OIDC tokens mapped to least-privilege workflow principals in grain's config repository, never holding model credentials or person-only grants; external pull requests are reviewed in grain's isolated, read-only sandboxes against governing designs, specs, and CI, reporting both a required `grain/review` check run and a GitHub App pull request review.

[`contributions.playbook.md`](../playbook/contributions.playbook.md) sets two expectations that connect GitHub and grain: workflows and contributors outside grain hand bounded work to grain without carrying model credentials inside GitHub Actions, and every pull request gets a required, blocking `grain/review` check that verifies code quality, security, and conformance to `design/`. Today grain only reacts passively after a workflow finishes (`github_workflow_run` schedules) or when someone manually asks `@<slug> review`, and its external PR check completes `neutral` even when it finds bugs. This design specifies:

1. **Workflow-driven tasks (`grain-task` and CLI):** creating—and optionally waiting on—a grain task directly from a GitHub Actions step, authenticated via GitHub OIDC with idempotent retries, streaming waits, and bidirectional links.
2. **Pull request reviews (`grain/review`):** automatic and action-triggered PR review tasks bound to a PR head SHA that check diffs, governing designs/specs, and CI in a secretless, read-only sandbox, resolve fixed threads on re-push, and post both a blocking `grain/review` check run and a GitHub App `APPROVE` / `REQUEST_CHANGES` review.

Normative requirements (`{#GA-1}`–`{#GA-24}`), wire schemas, and the requirement-to-test matrix live in [`github-actions-integration.spec.md`](../spec/github-actions-integration/github-actions-integration.spec.md).

## What exists today

| Piece | Current state in `ai-on-gke/grain` | Gap for this design |
| --- | --- | --- |
| **Agent API & scopes** (`docs/agent-api.md`, `pkg/ui/route_scopes.go`, `pkg/ui/agent_api_gate.go`) | Total route-to-scope table (`tasks:write`, `tasks:from-template`, `tasks:read`, `ci:read`, …), `api_scope_set` rows in `tables/api_scope_set.json`, escalation rails (`IsPersonOnlyGrant`, no `autoMerge: true`, no `submit`), `POST /api/tasks`, and `POST /api/tasks/from-template`. | No scope for requesting an external PR review (`reviews:request`) or overriding a review verdict; no `Idempotency-Key` support on task creation; no external wait route (`GET /api/tasks/{id}/wait`). |
| **External credentials** (`design/design/external-cli.design.md`, `pkg/ui/external_agents.go`) | Long-lived `gma_…` bearer tokens with scope sets, repo limits, expiry, RPM token buckets, and concurrency caps. | Requires storing a static secret in GitHub; no OIDC federation from GitHub Actions (`token.actions.githubusercontent.com` or GitHub Enterprise Server). |
| **Identity verification & JWKS** (`design/design/authentication.design.md`, `pkg/ui/verifier.go`, `pkg/jwks/jwks.go`) | `IdentityVerifier` seam for signed proxy headers (`X-Goog-IAP-JWT-Assertion`) and `jwks.Fetch` with HTTPS-downgrade refusal, key caching, and EC P-256 / RSA-2048+ checks. | `IdentityVerifier` inspects proxy headers and returns `Email`; it has no workflow-claim model (`repository_id`, `ref`, `job_workflow_ref`, `environment`, `event_name`) or principal-mapping table. |
| **IAP ingress** (`deploy/kubernetes/github-webhook.yaml`, `design/design/grain-on-gke.design.md`) | `/*` sits behind Google Cloud IAP; `/webhooks/github` routes to an unprivileged non-IAP backend verifying GitHub HMACs. | Workflows on GitHub-hosted runners cannot pass IAP without GCP Workload Identity Federation setup per repo (the blocker hit by `release-train.yml`, [Task #2382](/tasks/2382)). |
| **Workflow triggers** (`docs/schedules.md`) | `trigger.github_workflow_run` fires a schedule *after* a workflow run concludes (`workflow_run` webhook + poll). | One-way and post-hoc: a running job cannot file a task, pass step outputs, or block on the task's outcome. |
| **Attribution & RBAC** (`attribution.design.md`, `operator-attribution.design.md`, `rbac.design.md` / [Task #2764](/tasks/2764)) | `Principal{Kind, ID, Source}`, `Attribution{Actor, OnBehalfOf}`, task `owner` inheritance, external PR `Owner:` lines, and RBAC roles (`viewer`, `writer`, `admin`). | No `github-oidc` `AuthSource`, no workflow-principal `Actor` + `github.actor` `OnBehalfOf` binding, and no RBAC-gated review override. |
| **GitHub App & PR reviews** (`docs/github-bot.md`, `pkg/orchestrator/githubevents.go`, `review.go`, `reviewfeedback.go`, `reviewthreads.go`, `pkg/github/bot.go`) | `@<slug> review` and `review_requested` file an `IntentReview` task; `RESTClient.SubmitReview` supports `APPROVE`, `REQUEST_CHANGES`, and `COMMENT`; `ResolveReviewThread` calls `/graphql` and `/api/graphql` (full GHE thread parity in [Task #2634](/tasks/2634)). | Does not trigger automatically on `opened`/`synchronize`/`ready_for_review`; trusts repo `write`+ collaborators only (ignoring org membership); submits findings as `COMMENT` instead of `APPROVE`/`REQUEST_CHANGES`; completes `grain/review` as `neutral` when findings > 0; does not supersede prior head checks or resolve fixed threads on re-review; quotes `prBody` only on shepherd reviews. |

---

## Part 1: Creating and waiting for a task from a workflow

### 1.1 Interface: the `grain-task` reusable action and CLI equivalent

#### Packaging recommendation: TypeScript/JavaScript action in a companion repo (`ai-on-gke/grain-task`)

There are two ways to ship `grain-task`: a composite action under `.github/actions/grain-task` in `ai-on-gke/grain` that downloads and invokes the `grain` CLI, or a **zero-runtime-dependency Node 20 JavaScript action (compiled from TypeScript) published from a small companion repository, `ai-on-gke/grain-task`** (whose source lives in `actions/grain-task/` here and syncs on release).

**Recommendation: Node 20 JavaScript action in `ai-on-gke/grain-task`.**
- **Fast, tiny checkout:** Referencing `ai-on-gke/grain/.github/actions/grain-task@v3` forces GitHub Actions to download the entire `ai-on-gke/grain` tarball on every job. A companion repo (`uses: ai-on-gke/grain-task@v1`, SHA-pinned in high-trust repos) is under 100 KB and versions its `@v1` contract independently of daemon releases.
- **Native OIDC and `post:` cleanup:** Actions' `runs.using: node20` provides `core.getIDToken(audience)` directly—avoiding shell interpolation of tokens or untrusted PR text—and provides a native `post:` entrypoint (`post-if: cancelled()`) so `cancel-on-abort: true` reliably stops the grain task when a runner aborts a job. Composite actions have no `post:` hook without third-party wrappers, and work across Linux, macOS, and Windows runners without fetching platform binaries.

#### Action usage, inputs, and outputs

```yaml
permissions:
  contents: read
  id-token: write # mints a short-lived GitHub OIDC JWT for grain; no secret needed

jobs:
  triage:
    runs-on: ubuntu-latest
    steps:
      - name: File and wait for flake triage
        id: grain
        uses: ai-on-gke/grain-task@v1
        with:
          grain-url: https://grain.example.com
          template: triage-flake
          fields: |
            test_name: TestQuotaOffByOne
            workflow_url: ${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}
          repo: ${{ github.repository }}
          base: main
          ref: ${{ github.sha }}
          grants: ci-read
          wait: true
          until: pr-opened        # closed | merged | pr-opened
          timeout: 30m
          poll-interval: 15s
          cancel-on-abort: false
```

| Input | Default | Purpose |
| --- | --- | --- |
| `grain-url` | *(required)* | Base HTTPS URL of the grain deployment (also default OIDC `aud`). |
| `mode` | `task` | `task` (create/wait on a task) or `review` (request/wait on a PR review; Part 2). |
| `template` | `""` | Repository template stem (`.grain/templates/<stem>.yml`) or config-repo template ID. Mutually exclusive with `title`/`body`. |
| `fields` | `""` | YAML or JSON key-value map passed to `template` (`fields`). |
| `title` / `body` | `""` | Inline task title and Markdown brief when not using `template` (requires `tasks:write`). |
| `repo` | `${{ github.repository }}` | Target repository (`owner/name` or `host/owner/name`). |
| `base` | repo/template default | Target base branch (e.g. `main`). |
| `ref` | `${{ github.sha }}` | Git ref or commit SHA the workflow ran against (recorded in task context; in `mode: review`, defaults to PR head SHA). |
| `pr-number` / `head-sha` | from `pull_request` event | Target PR number and head commit SHA when `mode: review`. |
| `grants` | `""` | Comma- or newline-separated capability/API grants, bounded by the workflow principal's `allowed_grants`. |
| `wait` | `false` (`true` in `review`) | Block until `until` is reached or `timeout` elapses. |
| `until` | `closed` | Wait target in `mode: task`: `closed`, `merged`, or `pr-opened`. |
| `timeout` | `30m` | Maximum wait duration (`30s`–`6h`). |
| `poll-interval` | `15s` | Reconnect / long-poll floor (`>= 5s`); SSE/long-poll is primary, never tight polling. |
| `cancel-on-abort` | `false` | If `true`, cancelling the Actions job stops the grain task via `post:` / `SIGINT`. |
| `idempotency-key` | auto from run + attempt | Deduplication key (see §1.5). |
| `on-infra-failure` | `neutral` | `neutral` (exit `0` with `::warning::` and `outcome: infra-error\|paused\|timeout`) or `fail` (exit `1`). |
| `token` | `""` | Fallback `gma_…` secret token when GitHub OIDC is unavailable. |
| `oidc-audience` | `grain-url` | Override audience passed to `core.getIDToken()`. |
| `iap-id-token` | `""` | Optional GCP service-account ID token (`Proxy-Authorization: Bearer`) when calling through IAP directly. |

**Outputs:**
- `task-id`: Grain task ID (e.g. `2801`).
- `task-url`: `<grain-url>/tasks/<task-id>`.
- `state`: Final observed task state (`queued`, `running`, `completed`, `awaiting_submit`, `awaiting_reply`, `paused`, `closed`, `failed`).
- `outcome`: `closed`, `merged`, `pr-opened`, `nothing-needed`, `failed`, `cancelled`, `approve`, `request_changes`, `comment`, `timeout`, `paused`, or `infra-error`.
- `pr-url`: Opened or reviewed pull request URL, or `""`.
- `closing-note`: The task's closing comment or review summary.

#### Equivalent `grain` CLI one-liner

Workflows that already install `grain` (`external-cli.design.md`) can run a single command. When `$ACTIONS_ID_TOKEN_REQUEST_URL` and `$ACTIONS_ID_TOKEN_REQUEST_TOKEN` are set in the environment, `--auth github-oidc` mints and refreshes the GitHub OIDC token automatically:

```sh
grain create --server "$GRAIN_URL" --auth github-oidc \
  --repo "$GITHUB_REPOSITORY" --base main --ref "$GITHUB_SHA" \
  --template triage-flake --field test_name=TestQuotaOffByOne \
  --grant ci-read \
  --idempotency-key "gha:${GITHUB_REPOSITORY_ID}:${GITHUB_RUN_ID}:${GITHUB_RUN_ATTEMPT}:${GITHUB_JOB}" \
  --wait --until=pr-opened --timeout=30m --json
```

With the `gma_` fallback secret, replace `--auth github-oidc` with `--token "$GRAIN_TOKEN"`.

### 1.2 Authentication: GitHub OIDC first, `gma_` fallback, and reaching grain behind IAP

#### Primary: GitHub Actions OIDC verifier (`github-oidc`)

A workflow job with `permissions: id-token: write` requests a short-lived RS256/ES256 JWT from GitHub's OIDC provider (`https://token.actions.githubusercontent.com`, or `https://<ghe-host>/_services/token` on GitHub Enterprise Server) with `aud` set to the grain deployment URL. No secret is stored in GitHub or rotated by hand.

Grain verifies the token in `pkg/ui` alongside the `IdentityVerifier` seam (`authentication.design.md`, `pkg/ui/verifier.go`):
- **Header transport without colliding with `gma_` or access codes:** The action and CLI send `X-Grain-Actions-OIDC: <jwt>` (and grain also accepts `Authorization: Bearer <jwt>` when the bearer value is a 3-segment JWT whose unverified `iss` matches a configured GitHub OIDC issuer, leaving `gma_…` tokens and single-tenant access codes untouched). If an OIDC token is present and fails verification, grain **fails closed with `401 Unauthorized`** and never falls back to another credential.
- **Cryptographic and standard claim checks:** Signing keys are fetched from `<issuer>/.well-known/jwks` through `pkg/jwks.Fetch` (1-hour cache TTL, 10-second refetch floor on unknown `kid`, HTTPS-downgrade refusal, RSA >= 2048 / EC P-256, `alg` pinned by public-key type). `iss`, `aud` (exact match to the deployment's configured OIDC audience), `exp` (required), `iat`, and `nbf` (30s skew) are verified.
- **Workflow claim checks:** Grain parses and validates `repository`, `repository_id`, `repository_owner`, `repository_owner_id`, `ref`, `ref_type`, `job_workflow_ref` (which pins `<owner>/<repo>/.github/workflows/<file>@<ref>`, not the mutable display name `workflow`), `environment`, `event_name`, `actor`, `actor_id`, `run_id`, `run_attempt`, and `sha`.
- **Workflow principal mapping:** Verified claims are matched against `workflow_principal` rules in the deployment's config repository (§Both parts, *Configuration*). When `repository_id` or `repository_owner_id` is configured on a rule, grain enforces numeric ID equality so repository renames or account transfers after deletion can never impersonate a principal. Unmatched tokens are refused (`403 Forbidden`) naming the repository, workflow ref, git ref, event, and environment that failed to match. Each rule binds the caller to an RBAC role and narrow scopes—for example, workflows in `acme/widgets` on `refs/heads/main` running `.github/workflows/triage.yml` may only call `tasks:from-template` for templates `triage-flake` and `release-notes` plus `tasks:read`.

#### Fallback: `gma_` external-agent token

For GitHub Enterprise hosts without OIDC enabled or ad-hoc external workflows, `grain-task` accepts `token: ${{ secrets.GRAIN_TOKEN }}` (`gma_…`, `pkg/ui/external_agents.go`). Calls run under `authenticateExternalAgent` with the token's configured scope set, repository allowlist, expiry, rate limit, and concurrency cap.

#### Reaching grain behind Google Cloud IAP

When grain sits behind IAP (`design/design/grain-on-gke.design.md`), an unauthenticated HTTPS call from a GitHub-hosted runner is stopped at the load balancer before grain's OIDC verifier ever sees it. There are two ways through:

| Option | How it works | Security & operational trade-offs |
| --- | --- | --- |
| **Option A: Narrow non-IAP ingress route (`/api/actions/v1/*`) — *Recommended*** | Mirroring `/webhooks/github` (`deploy/kubernetes/github-webhook.yaml`), the Gateway routes `/api/actions/v1/*` to a backend with `iap.enabled: false` (either an unprivileged relay mode in `github-webhook` or a dedicated listener on `grain` disabled when no OIDC principals exist). Only five narrow endpoints exist on that prefix (`POST .../tasks`, `POST .../reviews`, `GET .../tasks/{id}`, `GET .../tasks/{id}/wait`, `POST .../tasks/{id}/stop`). | **Pros:** Zero GCP IAM / Workload Identity Federation setup per repository; onboarding a repo is one config-repo PR in grain; preserves full raw GitHub OIDC claims. **Cons / mitigations:** Grain (or its edge relay) is the internet perimeter for those five paths. Hardened by: serving zero UI/HTML, refusing cookies and single-tenant access codes on `/api/actions/v1/*`, capping bodies at 64 KiB, enforcing a pre-auth IP token-bucket limiter, and verifying the JWT against cached in-memory JWKS before touching SQLite/Postgres. |
| **Option B: GCP service-account ID token through IAP (`Proxy-Authorization`)** | The workflow exchanges its GitHub OIDC token via Google Cloud Workload Identity Federation (`google-github-actions/auth`) for a GCP SA ID token (`aud = <iap-client-id>`) sent in `Proxy-Authorization: Bearer <sa-id-token>`, while `X-Grain-Actions-OIDC` (or `Authorization: Bearer`) carries the GitHub OIDC JWT (`authentication.design.md` §*`grain` CLI and external agents*). | **Pros:** Every byte passes Cloud Armor and Google's edge IAP check before reaching the cluster; zero non-IAP backend routes. **Cons:** Every repo needs GCP WIF pool bindings, a service account in `-ui-allowed-principals`, and an extra workflow auth step; GCP WIF alone strips workflow-level claims (`environment`, `job_workflow_ref`, `actor`), so grain still has to verify the inner GitHub OIDC token anyway. |

**Recommendation:** **Default to Option A (`/api/actions/v1/*` narrow non-IAP route, enabled only when GitHub OIDC verification is configured), and also support Option B (`iap-id-token` / `--iap-token` sent as `Proxy-Authorization`)** for deployments whose security policy forbids any non-IAP ingress path beyond `/webhooks/github`.

### 1.3 Attribution

Following `operator-attribution.design.md` and `attribution.design.md`:
- **Actor and `Task.Creator`:** The verified workflow principal is recorded as `{Kind: automation, ID: "gha:<owner>/<repo>:<workflow>", Source: "github-oidc"}` (for example `gha:acme/widgets:.github/workflows/triage.yml`), and `Task.Creator` stores that principal ID. Request bodies and headers can never override it.
- **`OnBehalfOf`:** Grain populates `Origin.Attribution.OnBehalfOf` from the verified OIDC `actor` and `actor_id` claims: `{Kind: human, ID: "@<actor>", Source: "github-oidc"}` for a human GitHub account (resolving to `<actor_id>+<actor>@users.noreply.github.com` for Git trailers), or `{Kind: automation, ID: "<actor>", Source: "github-oidc"}` when triggered by a bot (`dependabot[bot]`, `github-actions[bot]`).
- **`Task.Owner` and inheritance:** The root task's `owner` is set at creation from the workflow principal's configured `default_owner` (if set, e.g. `oncall` or a team owner), otherwise the workflow principal paired with its human `OnBehalfOf` identity (`attribution.design.md` §*Root task owners*). Every child task spawned via `propose_task`, decomposition, repair, or review inherits `Task.Owner` unchanged. Agents remain excluded from `Co-authored-by:` trailers; internal PRs grain opens from a workflow-filed task record workflow provenance in `Origin:` / `Requested-by:` and write `Co-authored-by:` only when the resolved owner is a verified human.

### 1.4 Waiting, rate limits, timeouts, and cancellation

- **Streaming / long-poll wait (`GET /api/tasks/{id}/wait` and `/api/actions/v1/tasks/{id}/wait`):**
  - Supports **SSE (`Accept: text/event-stream`)** with `Last-Event-ID` resumption and **bounded HTTP long-polling (`?until=closed|merged|pr-opened&timeout=55s&after_seq=<n>`)**.
  - Keeping each hold under 55 seconds cleanly traverses GKE Gateways, IAP (`timeoutSec`), and NAT idle timeouts without silent TCP drops.
  - **OIDC token refresh across long waits:** GitHub OIDC tokens expire after ~5 minutes while a task wait may run for `30m`–`2h`. Before each reconnect when the cached JWT is within 60 seconds of `exp`, `grain-task` calls `core.getIDToken()` (and the CLI calls `$ACTIONS_ID_TOKEN_REQUEST_URL`) to mint a fresh token.
  - **Zero GitHub API cost:** Wait connections subscribe to grain's in-process store event bus and never trigger outbound GitHub API calls, preserving the deployment's GitHub rate-limit budget (`docs/github-api-rate-limits.md`). Reconnects obey `Retry-After` and jittered backoff floored at `poll-interval` (default `15s`, minimum `5s`), governed by a separate per-principal `max_concurrent_waits` cap (default 20) so waiting jobs do not exhaust mutation RPM buckets.
- **Cancellation semantics:**
  - **Default (`cancel-on-abort: false`):** If the workflow times out or a user cancels the GitHub Actions run, `grain-task` disconnects cleanly and leaves the grain task running. Work already partway through a sandbox build or merge gate is not thrown away by an unrelated CI cancel.
  - **Opt-in (`cancel-on-abort: true`):** On `SIGINT`/`SIGTERM` or in the action's `post:` hook (`cancelled()`), `grain-task` calls `POST /api/actions/v1/tasks/{id}/stop` with reason `"Cancelled because GitHub Actions run <run_id> was aborted"`. Any workflow principal may stop a task whose `Origin` matches that same workflow principal and `run_id`, even without broad `tasks:write`.

### 1.5 Idempotency

Network blips, 502 retries, or runner restarts within an attempt must never file duplicate tasks:
- `POST /api/tasks`, `POST /api/tasks/from-template`, `POST /api/actions/v1/tasks`, and `POST /api/actions/v1/reviews` accept an `Idempotency-Key` header (max 255 ASCII chars).
- By default, `grain-task` sets `gha:<repository_id>:<run_id>:<run_attempt>:<job>:<action_id>`.
- Grain stores `(principal_scope, idempotency_key) → (task_id, request_sha256, created_at)` in a unique store index (retained for 30 days).
  - **Same key + matching payload hash:** returns `200 OK` with `Idempotency-Replayed: true` and the existing `TaskResponse`, so a retried call seamlessly attaches and waits on the already-filed task.
  - **Same key + different payload hash:** returns `409 Conflict` (`idempotency_key_conflict`).
- Because `run_attempt` is in the default key, clicking **"Re-run failed jobs"** in GitHub (`run_attempt` increments from 1 to 2) intentionally files a fresh task if attempt 1's task failed, whereas workflows wanting deduplication across re-run attempts can pass `idempotency-key: gha:${{ github.repository_id }}:${{ github.run_id }}:${{ github.job }}`. In `mode: review` (Part 2), deduplication is keyed on `(repo, pr_number, head_sha)` so re-running a workflow on the same commit always reuses an active or passing review of that commit.

---

## Part 2: Grain reviews a PR and approves or denies it

### 2.1 How a review is requested

Both paths file (or attach to) the same `IntentReview` task bound to `(repo, pr_number, head_sha)`:

1. **GitHub App webhooks (primary, zero-workflow path):** For repositories with `pr_review_mode` enabled (`advisory` or `blocking`), `reconcileGitHubEvents` (`pkg/orchestrator/githubevents.go`) triggers on `pull_request` events `opened`, `synchronize`, `reopened`, and `ready_for_review` (when `draft == false`), plus `labeled` (`grain:review`), `review_requested` (for `<slug>[bot]`), and `@<slug> review` comments.
2. **The `grain-task` action in `mode: review` (or `grain pr review`):** A workflow step calls `POST /api/actions/v1/reviews` with `repo`, `pr_number`, and `head_sha`. Grain verifies against GitHub's API that `head_sha` is the current head of `repo#pr_number` (refusing stale or fabricated SHAs with `409 Conflict`), creates or attaches to the review task for that head, and—when `wait: true`—waits for the verdict.

When a PR touches a trust-boundary path (`pkg/trustboundary`: `.github/workflows/**`, `.grain/**`, `pkg/capability/**`, `pkg/runhost/**`, `pkg/staterepo/**`, `scripts/setup.sh`, …) or its author is outside the automatic-trust set (§2.4), grain **also** files the companion `system:security-review` task for `head_sha`. One `grain/review` check run aggregates every required review task on `head_sha`.

### 2.2 The review itself and how the verdict is posted

- **What the reviewer inspects:**
  1. **The diff and changed paths:** Grain checks out the target base branch (`main`) and fetches `refs/pull/<n>/head` verified at exact `head_sha`. Changed paths are classified per `contributions.spec.md` §2.1–2.2.
  2. **Governing designs and specs (*Conformance*):** Loaded from `.grain/templates/review.yml` on the **base branch** (`contributions.spec.md` §2.8, falling back to `system:pr-review` when absent), the prompt instructs the reviewer to locate governing `design/design/*.design.md`, `design/playbook/*.playbook.md`, and `design/spec/**/*.spec.md` files, verify every touched requirement, cite `{#ID}`s inline, and block intentional departures that lack a design/spec update.
  3. **CI status and prior threads:** Through read-only `ci-read` scoped to `(repo, head_sha)`, the reviewer inspects failing check runs/logs (`grain ci list/show/log`) and existing review threads (`grain pr status`).
- **Inline comments and structured verdict:**
  - The reviewer stages inline findings with `add_review_comment(path, line, side, start_line, severity, body)` (`grain pr review-comment`), where `severity` is `blocking` or `nit`, and ends its turn with a dedicated tool, **`submit_review_verdict(verdict, summary)`**, where `verdict` is `approve`, `request_changes`, or `comment`.
  - **Consistency rail:** Calling `submit_review_verdict(verdict: "approve")` after staging any `severity: "blocking"` finding is rejected in-turn so the agent either downgrades non-blocking nits or switches to `request_changes`. Diff coordinates outside `head_sha`'s diff hunks are converted into file:line items in the review summary rather than failing GitHub's batch review submission with `422`.

#### Verdict delivery: post BOTH a `grain/review` Check Run and a GitHub PR Review

**Recommendation: Post both, from the GitHub App's identity (`<slug>[bot]`).**

| Verdict | `grain/review` Check Run (`checks: write`) | GitHub PR Review (`pull_requests: write`) | Effect on merge |
| --- | --- | --- | --- |
| **`approve`** | `status: completed`, `conclusion: success` | `APPROVE` (with summary + any `nit` inline comments) | **Passes** the required `grain/review` gate; clears any prior `<slug>[bot]` `REQUEST_CHANGES` review. |
| **`request_changes`** | `status: completed`, `conclusion: failure` (or `neutral` when `pr_review_mode: advisory`) | `REQUEST_CHANGES` (with summary + inline comments; `COMMENT` when `advisory`) | **Blocks** merge via the required `grain/review` check (and via bot review state). |
| **`comment`** | `status: completed`, `conclusion: neutral` | `COMMENT` (with summary + inline comments) | Non-blocking remark (allowed only in `advisory` mode or supplementary notes; blocking mode requires `approve` or `request_changes`). |

Why both are needed:
1. **The `grain/review` check run is the authoritative, unforgeable gate.** Repository rulesets (`.github/rulesets/main.json`, `contributions.spec.md` §2.6.3) require `grain/review` pinned to grain's GitHub App `integration_id`. Check runs represent `queued` and `in_progress` states cleanly with a `Details` link straight to `/tasks/<id>`, and work identically on PRs authored by the App itself (where GitHub rejects self-`APPROVE` with `422`, causing `RESTClient.SubmitReview` in `pkg/github/bot.go` to fall back cleanly to `COMMENT`).
2. **The GitHub PR Review is for people.** Inline diff threads, file-by-file conversation folding, email notifications, and the reviewer badge in GitHub's PR sidebar all come from Pull Request Reviews.
3. **App approvals and human approvals stay cleanly separated (by design).** GitHub does not count a GitHub App's `APPROVE` toward `required_approving_review_count` or `CODEOWNERS` approvals. **That is intended (`contributions.playbook.md`):** human design/spec/`CODEOWNERS` approvals can never be satisfied by a bot, while `grain/review` enforces the agent review rule on every PR. And because the same App submits `APPROVE` when a later push fixes the issues (or dismisses its own review on a human override, §2.7), `<slug>[bot]`'s `REQUEST_CHANGES` never leaves an orphaned block.

### 2.3 Re-review on new pushes and thread resolution

When `pull_request.synchronize` moves a PR from `old_sha` to `new_sha`:
1. **Invalidate and cancel obsolete work:** Any queued or running review task for `(repo, pr_number, old_sha)` is stopped (`superseded by <new_sha>`). Any unfinished `grain/review` check run on `old_sha` is completed as `cancelled` (`"Superseded by <short_new_sha>"`), and a new `grain/review` check run is created on `new_sha` (`queued`). Because GitHub evaluates required checks per head commit, `old_sha`'s verdict no longer applies.
2. **Carry context and resolve fixed threads:** The new review task receives the prior review's findings and open `<slug>[bot]` threads. For each `<slug>[bot]` thread whose defect is fixed in `new_sha`, the reviewer resolves the thread through grain's thread tools (`grain pr resolve` / `resolve_review_thread`, calling GraphQL `resolveReviewThread` at `/graphql` on `github.com` and `/api/graphql` on GitHub Enterprise Server once [Task #2634](/tasks/2634) lands; if GraphQL thread listing is unavailable on a GHE host before [Task #2634](/tasks/2634), grain posts a short `"Resolved in <short_sha>"` REST reply without failing the run). Grain never resolves threads opened by humans.

### 2.4 Who gets reviewed, and the maintainer label gate

- **Automatic review:** Triggered automatically on non-draft PRs authored by:
  1. a **member of the repository's GitHub organization** (`GET /orgs/{org}/members/{login}` returning `204`, using `members: read` on the GitHub App and cached for 5 minutes—closing `contributions.playbook.md` gap #4); or
  2. a **repository collaborator** with `write`, `maintain`, or `admin` permission (`CollaboratorPermission`).
- **External contributors (maintainer label gate):** For any other author (such as a first-time fork contributor), grain does **not** dispatch a sandbox automatically. Instead, in `blocking` mode it creates `grain/review` on `head_sha` with `status: completed, conclusion: action_required` (which blocks merge under GitHub required checks, unlike `neutral`, while clearly signalling why) and instructions to apply the **`grain:review`** label.
- **Who may set `grain:review`:** Only a GitHub user with **`write`, `maintain`, or `admin`** permission on the repository (or an org member when `pr_review_trust` includes org members), verified from the `sender` of the `pull_request.labeled` webhook (or an `@<slug> review` comment). Label events from `triage`/`read`/`none` users are ignored for auto-dispatch and logged on the check summary. Crucially, **`grain:review` authorizes the exact `head_sha` present when the label was applied** (and grain removes the one-shot label once queued): if the external author pushes another commit later, `new_sha` returns to `action_required` until a maintainer inspects and re-labels it.

### 2.5 Trust and prompt-injection hardening

Every byte from the PR—diff, head files, commit messages, branch names, PR title, PR body, comments, and CI logs—is **untrusted input**:
- **Base-branch-only configuration and setup:** Grain loads `.grain/templates/review.yml`, `.grain/settings.yml` (`setup-command`, `prompt-extension`), and any path-trigger definitions strictly from the **target base branch (`main`)**, never from the PR's head commit. A PR cannot alter its own review prompt or execute arbitrary setup commands by editing `.grain/` in its diff.
- **Zero write, credential, or network authority:** An external PR review task is hard-capped in the daemon (`pkg/orchestrator` + `pkg/gitproxy` + `agentAPIGate`):
  - **No `writes:` grants** (the sandbox git proxy refuses every push and branch creation with `403`);
  - **No secrets or cloud/deploy capabilities** (`gcp-key`, `secrets:*`, `request_secret`, host scripts, and capability servers holding credentials are stripped/refused);
  - **No task-filing or escalation tools** (`propose_task`, `ask_question`, `grain pr open`, and mutating `api_*` tools are disabled);
  - **`network_access: none`** by default: the reviewer inspects the pre-fetched git tree and base-branch test tools offline, never running the PR's code with credentials or external network egress.
- **Prompt-injection containment:** All untrusted text (PR title, body, commit messages, review comments, CI log tails) is enclosed in randomized-nonce delimiters (`<untrusted-pr-content nonce="<hex>">…</untrusted-pr-content>`)—fixing `fileExternalPRReview` where standalone external PR descriptions are currently interpolated bare—paired with system instructions that content inside those tags is data under audit and cannot alter instructions, suppress findings, or choose `approve`. Even under total prompt capture, the sandbox's tool surface can at worst post a noisy comment or approve a single commit (which `CODEOWNERS`, CI tests, and human review on protected paths still gate; for trust-boundary paths, an independent `system:security-review` run with a dedicated adversarial prompt must also pass).

### 2.6 Cost and limits

- **Drafts skipped:** PRs with `draft: true` do not queue review tasks on `opened` or `synchronize`. Grain posts `grain/review` as `skipped` (`"Draft pull request; grain/review runs when marked ready for review"`) and queues the review immediately on `ready_for_review`.
- **Debounce on rapid pushes:** `synchronize` events wait a short configurable quiet window (`pr_review_debounce_seconds`, default `15s`) before dispatching a sandbox, coalescing back-to-back pushes while cancelling any superseded run immediately.
- **Rate limits and caps (per `repo_config`):**
  - **Per-repo rate limit:** default `60` review tasks/hour (burst `15`).
  - **Per-author rate limit:** default `15` review tasks/hour per GitHub author across the repository, plus a **per-PR automatic head cap** of `12` heads (matching `takeover.go`'s runaway loop guard; further heads wait for an explicit maintainer `@<slug> review` or `grain:review`).
  - **Concurrent review cap:** `pr_review_max_concurrent` (default `4` per repo, plus a deployment-wide review share) keeps PR bursts from starving implementation and base-branch repair tasks; excess reviews stay `queued` with `grain/review` showing queue state.

### 2.7 Human overrides

When a reviewer flags a false positive, a human with the right role can dismiss a `request_changes` verdict without weakening branch protection:
- **Who may override:** Only a verified human principal (`Principal.Verified() == true`, `operator-attribution.design.md`) holding RBAC role **`writer` or `admin`** on the repository (`rbac.design.md`, [Task #2764](/tasks/2764)). Viewers, agents, and workflow principals are refused (`403`).
- **How:** In the Grain UI ("Override review" on `/tasks/<id>`), CLI (`grain pr override-review <owner/repo#n> --sha <head_sha> --reason "<why>"`), or REST API (`POST /api/pull-requests/{owner}/{name}/{number}/reviews/{head_sha}/override`) with a **mandatory non-empty `reason`**.
- **Effect:** Grain updates `grain/review` on `head_sha` to `conclusion: success` (`"Overridden by <email>: <reason>"`), dismisses `<slug>[bot]`'s `REQUEST_CHANGES` review via GitHub's dismissal API with the same attribution, and writes an immutable `review_overridden` event in `task_event` and the audit journal. Any push of a new commit (`new_sha`) invalidates the override and runs a fresh review.

---

## Both parts: security, configuration, observability, and failure modes

### Security boundaries

1. **Exact workflow-principal permission ceiling:**
   - A workflow principal MAY hold only a subset of: `tasks:from-template` (pinned to explicit `allowed_templates`), `tasks:write` (repo-limited, held to `agentAPIGate` proposal rails: never `autoMerge: true`, never `submit`), `tasks:read`, `ci:read`, `reviews:request`, and implicit self-stop on tasks created by that same `(principal, run_id)`.
   - A workflow principal MUST NEVER hold or grant person-only or dangerous scopes (`capabilities:admin`, `secrets:read`, `secrets:write`, `settings:write`, `connections:write`, `merge:force`, `tasks:admin`, `deployment:*` other than `read`, `priority-dispatch`, `gcp-key` via ad-hoc `grants`, `submit`, or `override-review`).
2. **Fork pull requests:**
   - GitHub withholds secrets and write OIDC tokens on `pull_request` runs from forks. In addition, grain's OIDC verifier checks `ref` and `job_workflow_ref`: workflow-principal rules default to requiring protected branch refs (e.g. `refs/heads/main`) unless `allow_pull_request_refs: true` is explicitly set, so fork workflows cannot obtain workflow-principal authority even on misconfigured repositories.
3. **`pull_request_target` hazards:**
   - Workflows triggered on `pull_request_target` run from the base branch *with `id-token: write` and secrets enabled* even when opened from an external fork. Checking out or running untrusted PR code inside `pull_request_target` is a critical privilege-escalation vector.
   - **Rule:** Prefer the **GitHub App webhook path** for PR reviews so no `pull_request_target` workflow exists at all. Furthermore, grain's OIDC verifier **rejects tokens with `event_name == "pull_request_target"` by default** unless `allow_pull_request_target: true` is set on the workflow principal, and `statecheck` refuses any `allow_pull_request_target: true` rule that grants anything beyond `reviews:request` and `tasks:read`.

### Configuration (in the config repository)

Per `pkg/declschema`'s authority rule (*a repository never decides its own authority, who judges it, or what this deployment spends*), neither workflow principals nor automatic PR review enablement may be set in a target repository's `.grain/settings.yml`. Both live in the deployment's **config repository** (`grain/config` or external config repo, `config-repo-settings.design.md`):
- **Workflow principals:** `settings/workflow_principals.yml` (exported/imported alongside `tables/workflow_principal.json` and validated by `grain state check`), mapping `(issuer, repo, repository_id, workflow_refs, refs, environments, events)` to `(id, role, scope_set/scopes, allowed_templates, allowed_grants, default_owner, lands_queued, rate_limit_rpm, max_concurrent)`.
- **Per-repository PR review settings:** columns on `repo_config` (`tables/repo_config.json`, Settings → Repositories): `pr_review_mode` (`off` | `advisory` | `blocking`), `pr_review_trust` (`org_and_collaborators` | `collaborators`), `pr_review_label` (`grain:review`), `pr_review_skip_drafts` (`true`), `pr_review_debounce_seconds` (`15`), `pr_review_rate_limit_per_hour` (`60`), `pr_review_author_limit_per_hour` (`15`), `pr_review_max_concurrent` (`4`), and `pr_review_infra_conclusion` (`neutral` | `failure`).
- **In-repo review rubric:** `.grain/templates/review.yml` in the target repo (read from the base branch only, `contributions.spec.md` §2.8) customizes the review prompt without granting capabilities.

### Observability

- **Task → Workflow run:** Grain records a `LinkGitHubWorkflowRun` link (`<server_url>/<repo>/actions/runs/<run_id>/attempts/<attempt>`) from verified OIDC claims on every workflow-filed task, rendered as a clickable badge on `/tasks/<id>`, in `grain get`, and in audit events.
- **Workflow run → Task:** `grain-task` writes a Markdown table to `$GITHUB_STEP_SUMMARY` as soon as the task is filed (linking `<grain-url>/tasks/<id>`) and updates it on completion with state, outcome, PR link, duration, and closing summary, plus `::notice::` / `::warning::` step annotations.
- **Check run → Task:** Every `grain/review` check run sets `details_url: <grain-url>/tasks/<id>` and `external_id: task:<id>`.

### Failure modes

Infrastructure faults default to **neutral** rather than failing developer workflows or bricking merges during grain outages or model quota windows, unless configured strict (`on-infra-failure: fail` on the action; `pr_review_infra_conclusion: failure` on `repo_config`):

| Failure mode | `grain-task` action behaviour | `grain/review` Check Run behaviour |
| --- | --- | --- |
| **Grain unreachable / 5xx** | Retries initial request with jittered backoff (up to 60s; reconnects until `timeout` once filed). If still unreachable: sets `outcome: infra-error`, emits `::warning::`, writes step summary, and **exits `0` (neutral)** unless `on-infra-failure: fail` (`exit 1`). | Webhook events spool durably in `github-webhook` (`events.jsonl`) and drain when `grain` recovers. If an active review task exhausts infra retries (`maxInfraRetries`), completes `grain/review` as **`neutral`** (`failure` if `pr_review_infra_conclusion: failure`) with a re-run summary. |
| **Usage-limit / quota pause** | Grain fails over across `default_model` pools first. If all pools pause (`StoredUsageLimitPause`) past remaining `timeout`: sets `state: paused`, `outcome: paused`, leaves task queued for reset, and **exits `0` (neutral)** unless `on-infra-failure: fail`. | Stays `queued`/`in_progress` during short pauses (`<= 15m` grace) or active failover. If paused beyond grace with no healthy model pool, completes `grain/review` as **`neutral`** (`failure` if configured) noting the reset timestamp, and automatically re-queues a fresh check when the pause lifts if the PR is still open at that head. |
| **Timeout** | Wait clock (`timeout`) expires: disconnects without stopping the task (unless `cancel-on-abort: true`), sets `outcome: timeout`, links `/tasks/<id>`, and **exits `0` (neutral)** unless `on-infra-failure: fail`. | Review sandbox hits wall-clock timeout without submitting a verdict (after 1 retry): completes `grain/review` as **`neutral`** (`failure` if configured) naming the timed-out task link. |

---

## Open questions

1. **Companion repo vs in-tree composite action for `grain-task`.**
   *Recommendation:* Publish the compiled Node 20 action to a tiny companion repo (`ai-on-gke/grain-task`) from source kept under `actions/grain-task/` in `ai-on-gke/grain`, giving fast tarball downloads, `core.getIDToken()`, and `post:` cancellation hooks while keeping source review in one repo.
2. **Non-IAP `/api/actions/v1/*` route vs mandatory GCP WIF + IAP.**
   *Recommendation:* Provide `/api/actions/v1/*` (narrow 5-route surface, disabled when no OIDC principals exist, routed through an unprivileged `iap: false` backend like `github-webhook`) as the default, while supporting `iap-id-token` (`Proxy-Authorization`) for deployments that forbid non-IAP ingress.
3. **Check conclusion while an external contributor's PR awaits a maintainer's `grain:review` label.**
   *Recommendation:* Complete `grain/review` with `conclusion: action_required` rather than leaving it `queued` indefinitely or marking it `neutral`. `neutral` would *pass* GitHub's required-check rule; `action_required` blocks merge and displays a clear badge instead of a perpetual spinner.
4. **GitHub-side override comments (`@<slug> override-review <reason>`) vs Grain UI/CLI only.**
   *Recommendation:* Require overrides through Grain's UI/CLI/API in v1 (`POST /api/pull-requests/.../override`), where the caller's verified identity (`iap` / signed proxy) is checked directly against Grain RBAC (`writer` / `admin`) without needing a GitHub-handle-to-email link table; add GitHub comment/button overrides later once GitHub identity linking lands.
5. **Default check conclusion on grain outages or prolonged model-quota pauses.**
   *Recommendation:* Default `pr_review_infra_conclusion` to `neutral` so grain downtime or upstream provider quota exhaustion does not block human-reviewed merges, with per-repo `failure` opt-in for high-assurance repos and automatic re-review when a quota pause clears.
6. **Posting `grain/review` on grain's own internal PRs (`contributions.spec.md` §2.6.4).**
   *Recommendation:* Unify `SyncReviews` (second-agent review on grain-opened PRs) with the `grain/review` check-run writer in the same rollout so `.github/rulesets/main.json` can require `grain/review` across internal and external PRs alike (`SubmitReview` already degrades self-`APPROVE` to `COMMENT` when the same App authored the PR).

---

## Implementation plan

Follow-up tasks to file once this design and spec are reviewed and merged (not filed yet):

1. **Config-repo schema & OIDC verifier (`#GA-1`–`#GA-3`, `#GA-5`, `#GA-21`, `#GA-22`):** Add `workflow_principal` store migration, `settings/workflow_principals.yml` (`declschema` + `staterepo` + `statecheck`), `AuthSource` `"github-oidc"`, `GitHubActionsVerifier` over `pkg/jwks`, workflow-principal attribution (`Actor` + `OnBehalfOf`), and permission-ceiling tests.
2. **Idempotent task creation, streaming wait, cancellation, and `/api/actions/v1/*` ingress (`#GA-4`, `#GA-6`–`#GA-9`, `#GA-23`):** Add `task_idempotency` table and `Idempotency-Key` middleware, `GET /api/tasks/{id}/wait` (SSE + 55s long-poll over store pubsub), `LinkGitHubWorkflowRun`, self-stop for matching `(principal, run_id)`, and the narrow `/api/actions/v1/*` mux + Kubernetes `BackendConfig` / relay wiring.
3. **CLI OIDC auth & `grain-task` action (`#GA-10`, `#GA-11`):** Add `--auth github-oidc`, `--idempotency-key`, `grain create --wait`, and `grain pr review` to `pkg/cli`; build `actions/grain-task/` (TypeScript → Node 20 bundle with `post:` cleanup, OIDC refresh, `$GITHUB_STEP_SUMMARY`, and release sync workflow to `ai-on-gke/grain-task`).
4. **PR review engine: structured verdicts, dual check+review posting, and hardening (`#GA-14`–`#GA-16`, `#GA-18`):** Add `submit_review_verdict` tool, base-branch-only `.grain/templates/review.yml` loading, randomized-nonce `<untrusted-pr-content>` quoting across all external PR inputs, hard sandbox grant stripping (`network_access: none`, zero `writes:`/secrets/proposals), dual `grain/review` Check Run + `APPROVE`/`REQUEST_CHANGES` submission, companion trust-boundary security review aggregation, and `grain/review` check reporting on grain-opened PRs.
5. **Automatic PR triggers, org-member trust, superseding, thread resolution, limits, and RBAC overrides (`#GA-12`, `#GA-13`, `#GA-17`, `#GA-19`, `#GA-20`, `#GA-23`, `#GA-24`):** Add `repo_config` PR review columns, `opened`/`synchronize`/`ready_for_review` handling, org-membership caching (`GET /orgs/{org}/members/{user}`), per-SHA `grain:review` maintainer label gate (`action_required`), old-SHA cancellation + check superseding + `<slug>[bot]` thread resolution (with [Task #2634](/tasks/2634) GHE GraphQL support), rate/concurrency/debounce limiters, infra-neutral fallback, RBAC `writer`/`admin` override endpoint + UI button, and full `-github-sim` e2e walkthroughs.
