# Test run — 2026-11-04 — Access and identity (`docs/test-plans/2026-11-04.md` §3)

Task 3069 (proposed from task 2958). Walks §3 ("Access and identity — the database allowlist, service accounts with a reason, `whoami` and principal labels, Settings → Audit") of [`docs/test-plans/2026-11-04.md`](../test-plans/2026-11-04.md), executing items 1–7 of "What is NOT proved" plus the 1440/390 light/dark screenshot suite against a real `grain daemon` built with the embedded UI and `grain demo` for Playwright e2e regressions.

**Result: 62 shell/API/SQLite/CLI/MCP checks PASS, 31 live-daemon Playwright browser/layout checks PASS, and 11 `grain demo` Playwright e2e checks PASS (0 FAIL).** Five code/UI bugs (F1–F5) and one documentation drift (F6) were uncovered by live reproduction, fixed on this branch, and pinned with Go and Playwright regression tests.

Re-run the full end-to-end harness with:

```bash
docs/test-runs/2026-11-04-access-identity/walkthrough.sh [/tmp/out]
```

Full per-check tables and captured artifacts live in [`2026-11-04-access-identity/`](2026-11-04-access-identity/):
- [`results.md`](2026-11-04-access-identity/results.md) — all 62 shell/API/SQLite/CLI/MCP assertions
- [`browser-results.json`](2026-11-04-access-identity/browser-results.json) & [`browser-verifier-down.json`](2026-11-04-access-identity/browser-verifier-down.json) — all 31 live-daemon Playwright assertions
- [`contact-sheet.png`](2026-11-04-access-identity/contact-sheet.png) & [`screenshots/`](2026-11-04-access-identity/screenshots/) — 18 responsive light/dark PNG captures

---

## Environment and stubbed boundaries

| Component | Details |
|---|---|
| Base commit | `d9c2bb0a` (`origin/main`) |
| Binary | `cd ui && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain` (embedded React/Vite UI in `pkg/ui/static`) |
| OS & toolchain | Debian GNU/Linux, Go 1.26, Node.js v20.19.0, SQLite 3, ImageMagick `montage` |
| Browser | Headless Chromium via `@playwright/test` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900` (desktop) and `390×844` (phone), `light` and `dark` |
| Live daemon | `grain daemon -data-dir $WORK/data -host-sandboxes -sandbox-dir $WORK/sandboxes -ui-addr 127.0.0.1:14310 -as owner@example.com -ci=false -repo acme/app -default-target-repo acme/app -gcp-project my-proj -ui-iap-audience /projects/123456/global/backendServices/789012 -ui-iap-issuer https://iap.test.local -ui-iap-keys-url http://127.0.0.1:18280/jwks -ui-cloud-identity-endpoint http://127.0.0.1:18280 -ui-allowed-principals "user:seed@example.com,serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com,domain:example.com"` |
| Demo server | `grain demo` launched by `ui/playwright.config.js` (`GRAIN_BIN=/home/debian/bin/grain`) for `e2e/whoami-user-tile.spec.js`, `e2e/audit-settings-layout.spec.js`, `e2e/access-principals-settings.spec.js`, and `e2e/interactive-session-chat.spec.js` |
| Isolation | Clean `HOME=$WORK/home` (no `~/.git-credentials` or saved contexts), `GRAIN_SANDBOX_CLI=0` for workstation CLI invocations, `GCE_METADATA_HOST=127.0.0.1:9` |

### Explicitly stubbed external boundaries

Real Cloud Run / Google IAP and production Google Cloud Identity are unreachable from the isolated sandbox. **Only those two external Google boundaries were stubbed**, using the shared [`tests/iap/iapsim.go`](../../tests/iap/iapsim.go) harness from `docs/test-runs/2026-11-02-iap-end-to-end.md` and `docs/test-runs/2026-11-03-iap-deployment/`:

1. **IAP JWT issuer & JWKS endpoint (`127.0.0.1:18280`):** Serves a real P-256 public JWK set at `/jwks` and signs real ES256 `X-Goog-IAP-JWT-Assertion` tokens at `/sign` with `iss=https://iap.test.local`, `aud=/projects/123456/global/backendServices/789012`, `email`, and `sub`. `grain daemon` runs its full production `webauth.IAPVerifier` + `jwks.Fetch` signature, issuer, audience, and expiry verification path.
2. **Cloud Identity Groups API + OAuth token endpoint (`127.0.0.1:18280`):** Serves `/token` (consumed by a real `GOOGLE_APPLICATION_CREDENTIALS` service-account JSON file), `/v1/groups:lookup`, and `/v1/groups/{id}/memberships:checkTransitiveMembership` configured with `-group ops@example.com=carol@example.com`. `grain daemon` runs its real `webauth.NewCloudIdentityGroupChecker` OAuth JWT exchange and REST membership client against `-ui-cloud-identity-endpoint` (added to `grain daemon` in **F3** below).

Everything inside grain — `grain daemon`, SQLite (`grain.db`), one-time flag migration, RBAC/scope evaluation, `config_audit`, `grain` workstation CLI, `grain` sandbox CLI, Controller MCP (`POST /api/mcp`), and the embedded React frontend in Chromium — ran unstubbed.

---

## Walkthrough results by item

### 1. Live allowlist edits (`grain access allow|deny`, UI, API) and Cloud Identity group membership

- **Initial refusal (`1.1`–`1.2`):** With `user:alice@example.com` absent from `allowed_principal`, `GET /api/whoami` carrying a valid IAP assertion for `alice@example.com` returned **`403 Forbidden`**:
  ```json
  {"error":"principal \"alice@example.com\" is not on the allowed users and groups list (Settings -> Access)"}
  ```
- **Live CLI allow & deny (`1.3`–`1.7`):**
  - `grain -server http://127.0.0.1:14310 -token "$ACCESS_CODE" access allow --reason "admit alice as primary operator" user:alice@example.com` printed `allowed user:alice@example.com as grain/operator`.
  - On the **very next request with no daemon restart**, `GET /api/whoami` as `alice@example.com` returned **`200 OK`**:
    ```json
    {"actor":{"kind":"human","id":"alice@example.com","source":"iap","label":"alice@example.com"},"onBehalfOf":null,"source":"iap","verified":true,"allowed":true}
    ```
  - `grain ... access deny user:alice@example.com` took effect immediately on the next request (`403`, pointing to `Settings -> Access`), and re-allowing restored `200 OK` without a restart.
- **Live API add, role update & delete (`1.8`–`1.15`):**
  - Verified IAP operator `alice@example.com` called `POST /api/access/principals` (`{"entry":"user:bob@example.com","role":"grain/viewer","reason":"bob viewer via API"}`) → **`201 Created`**.
  - `bob@example.com` was admitted immediately (`GET /api/whoami` → `200`), refused `POST /api/tasks` under `grain/viewer` (`403`: `bob@example.com (role grain/viewer) holds no scope tasks:write`), promoted live via `PATCH /api/access/principals/user%3Abob%40example.com` to `grain/contributor` (`200`), immediately able to file a task (`POST /api/tasks` → `201`), and immediately refused (`403`) after `DELETE /api/access/principals/user%3Abob%40example.com` (`200`).
- **Live UI allowlist add & remove (`UI-1a`–`UI-1e`):**
  - Opening `/settings/access` in Chromium as `alice@example.com` showed `Your role: grain/operator` and `Signed in through IAP as alice@example.com.`.
  - Before adding `user:frank@example.com`, `/api/whoami` as `frank` returned `403` pointing to `Settings -> Access`. Filling `user:frank@example.com` + role `grain/contributor` + reason `UI live allowlist check` and clicking **Add allowed principal** rendered `principal-row-user:frank@example.com` and immediately admitted `frank@example.com` (`200 OK`, `allowed: true`, `verified: true`). Clicking **Remove user:frank@example.com** detached the row and immediately returned `frank@example.com` to `403`.
- **Group membership via Cloud Identity resolver stub (`1.16`–`1.20`):**
  - `carol@example.com` was refused (`403`) before `group:ops@example.com` was added.
  - After `grain access allow --role grain/contributor --reason "ops on-call group" group:ops@example.com`, `carol@example.com` was admitted immediately (`GET /api/whoami` → `200 OK`) and inherited the group's `grain/contributor` role (`POST /api/tasks` → `201`), while non-member `dave@example.com` remained refused (`403`).

### 2. Service accounts: required reason, `kind: "service"` attribution, planted `gcp-key` pool SA refusal, and `-ui-allowed-principals` migration

- **Refused without a reason across CLI, API, and UI (`2.1`–`2.4`, `UI-2b`):**
  - **CLI:** `grain access allow --role grain/contributor serviceAccount:release-bot@my-proj.iam.gserviceaccount.com` exited **`2`** with:
    `grain: allowed principal "serviceAccount:release-bot@my-proj.iam.gserviceaccount.com": give a reason to admit a service account (with role grain/contributor)`
  - **API:** `POST /api/access/principals` without `reason` returned **`400 Bad Request`** with the same message.
  - **UI:** Submitting `serviceAccount:no-reason-ui@my-proj.iam.gserviceaccount.com` with an empty **Reason** field on `/settings/access` blocked submission client-side with helper prompt `Give a reason` and created zero rows.
- **Admitted with a reason as `kind: "service"` and attributed (`2.5`–`2.10`, `UI-2c`):**
  - `POST /api/access/principals` with `{"entry":"serviceAccount:release-bot@my-proj.iam.gserviceaccount.com","role":"grain/contributor","reason":"release pipeline files tasks through IAP"}` returned **`201 Created`**.
  - `GET /api/whoami` as `release-bot@my-proj.iam.gserviceaccount.com` returned **`200 OK`**:
    ```json
    {"actor":{"kind":"service","id":"release-bot@my-proj.iam.gserviceaccount.com","source":"iap","label":"release-bot@my-proj.iam.gserviceaccount.com"},"onBehalfOf":null,"source":"iap","verified":true,"allowed":true}
    ```
  - `POST /api/tasks` by `release-bot` returned `201 Created` with `"author":"release-bot@my-proj.iam.gserviceaccount.com"`, `"authorKind":"service"`, `"authorLabel":"release-bot@my-proj.iam.gserviceaccount.com"`, and SQLite `task` stored `origin_actor_kind='service'`, `origin_actor_id='release-bot@my-proj.iam.gserviceaccount.com'`, `origin_actor_source='iap'`.
  - Settings → Access rendered `release-bot` in its dedicated **Service accounts** table (`data-testid="allowed-service-accounts"`) separate from human/group principals, showing its role, who added it, when, and why.
- **`gcp-key` pool SA refused via API and even when planted directly in SQLite (`2.11`–`2.14`):**
  - Registered a `gcp_key` pool (`POST /api/gcp-keys` with `serviceAccountEmail: "grain-sb-agent@my-proj.iam.gserviceaccount.com"`, `serviceAccountCount: 2` → pool accounts `grain-sb-agent-{0,1}@my-proj.iam.gserviceaccount.com`).
  - `POST /api/access/principals` for `serviceAccount:grain-sb-agent-0@my-proj.iam.gserviceaccount.com` returned **`400 Bad Request`**.
  - Planted the row directly into SQLite (`INSERT INTO allowed_principal ... VALUES ('serviceAccount:grain-sb-agent-0@my-proj.iam.gserviceaccount.com', ...)`), confirmed `COUNT(*) = 1`, and sent a valid IAP JWT for `grain-sb-agent-0@my-proj.iam.gserviceaccount.com`: sign-in **still refused it with `403 Forbidden`**:
    ```json
    {"error":"principal \"grain-sb-agent-0@my-proj.iam.gserviceaccount.com\" is a gcp-key agent service account whose key is materialized into task sandboxes and cannot be admitted"}
    ```
    and logged:
    `ui: refused verified principal "grain-sb-agent-0@my-proj.iam.gserviceaccount.com": it is a gcp-key agent service account, whose key is handed to task sandboxes -- a sandbox's key was presented through the identity proxy; take it off the proxy's access list (iap_members) and rotate the pool's keys`
- **Deprecated `-ui-allowed-principals` one-time import drops `serviceAccount:` and `domain:` (`2.15`–`2.18`, `UI-2a`):**
  - First boot with `-ui-allowed-principals "user:seed@example.com,serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com,domain:example.com"` imported `user:seed@example.com` once, dropped `serviceAccount:dropped-ci@...` and `domain:example.com`, logged explicit warnings:
    ```text
    grain access: WARNING: dropped "serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com" from -ui-allowed-principals during migration -- only user:<email> and group:<email> entries are imported; add a service account deliberately with 'grain access allow serviceAccount:<email> --reason <why>'
    ```
    returned `"droppedFlagEntries":["serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com","domain:example.com"]` on `GET /api/access/principals`, and displayed the warning banner (`data-testid="dropped-flag-entries-warning"`) in Settings → Access.
  - Restarting `grain daemon` on the same database with `-ui-allowed-principals "user:second-boot-ignored@example.com"` ignored the flag (`second-boot-ignored@example.com` → `403`) while keeping `alice@example.com` admitted (`200`).

### 3. `api:access:write` held by an agent and `config_audit` reasons

- **Person-only escalation guard (`3.1`–`3.3`):** An agent task (`PLAIN_TASK`) holding `api:tasks:write` but not `api:access:write` attempted to file a task requesting `api:access:write` (`X-Grain-Acting-Task: 5`) and was refused with **`403 Forbidden`**:
  `{"error":"api:access:write is a dangerous api grant: only a person may grant it"}`. A person creating task `6` (`PRIV_TASK`) with `["api:access:write","api:settings:write","api:tasks:read"]` succeeded (`201 Created`).
- **Refused on `sandbox-cli` channel (`3.4`–`3.5`):** Calling `POST /api/access/principals` with `X-Grain-Acting-Task: 6` and `X-Grain-Channel: sandbox-cli` returned **`403 Forbidden`**:
  `{"error":"POST /api/access/principals requires scope access:write, which is not permitted on the sandbox-cli channel"}`.
- **Refused without `X-Grain-Acting-Reason` (`3.6`–`3.7`):** Calling `POST /api/access/principals` with `X-Grain-Acting-Task: 6` on the MCP/API channel with no `X-Grain-Acting-Reason` returned **`403 Forbidden`**:
  `{"error":"POST /api/access/principals is destructive (scope access:write): give a reason"}`.
- **Post-RBAC `AU-6.4` person-only allowlist guard (`3.8`–`3.9`, Design note D1):** Even when task `6` holds person-granted `api:access:write` **and** supplies `X-Grain-Acting-Reason`, `POST /api/access/principals` returns **`403 Forbidden`**:
  `{"error":"agent 6 may not change who signs in to this deployment: only a person changes the allowed users and groups list"}` (intentional per `AU-6.4` / commit `f8629b38`; documented in `docs/agent-api.md` via **F6**).
- **Agent dangerous config write & reason in `config_audit` + Settings → Audit (`3.10`–`3.13`, `UI-3-audit`):**
  - Task `6` calling `PUT /api/settings` (`{"pollInterval":"25s"}`) without `X-Grain-Acting-Reason` was refused (`403`); with `X-Grain-Acting-Reason: enable telemetry check from task run` it succeeded (`200 OK`).
  - After fixing **F4** (`pkg/ui/write_actor.go`), both human allowlist reasons and agent `X-Grain-Acting-Reason` values appear in SQLite `config_audit`, [`config-audit.json`](2026-11-04-access-identity/config-audit.json) (`GET /api/deployment/config-audit`), and Settings → Audit (`config-audit-table`):
    ```json
    {"id":13,"actor":"6","actorKind":"agent","actorSource":"sandbox","actorLabel":"task 6","surface":"settings","summary":"PUT /api/settings (enable telemetry check from task run)"}
    ```

### 4. Consistent `PrincipalLabel` across every surface (`4.1`–`4.11`, `UI-4a`, `UI-4b`)

Task `7` ("Cross-surface attribution walkthrough") was touched by all four actor classes:
1. **Verified IAP person (`alice@example.com`):** filed the task unapproved, approved it (`proposed -> queued`), posted a comment, and resumed it once.
2. **Access code (`operator (access code)`):** posted a comment and paused the task (leaving it paused so the pause banner is active).
3. **Owned external agent (`external agent triage-bot for alice@example.com`):** minted by `alice@example.com` (`POST /api/external-agents/tokens`) and posted a comment carrying `X-Grain-Client: triage-runner/1.2`.
4. **Grain automation (`grain`):** authored the pause/resume system comments (`Paused by operator (access code).`, `Resumed by alice@example.com...`).

An interactive chat task (`8`) was also touched by `alice@example.com`, `operator (access code)`, and `external agent triage-bot for alice@example.com`.

| Surface | Verified person | Access code | Owned external agent | Automation | Raw `{human, operator}`? |
|---|---|---|---|---|---|
| Task page (`Filed by`, comments, timeline, pause banner) | `alice@example.com` | `operator (access code)` / `Paused by operator (access code)` | `external agent triage-bot for alice@example.com` | `grain` | **None** (`UI-4a`) |
| Interactive `TaskChat` bubbles (after **F5** CSS fix) | `alice@example.com` | `operator (access code)` | `external agent triage-bot for alice@example.com` | `grain` | **None** (`UI-4b`) |
| Workstation CLI [`grain get 7`](2026-11-04-access-identity/grain-get.txt) | `filed by: alice@example.com` | `paused: by operator (access code)` / `comment #4 by operator (access code)` | `comment #8 by external agent triage-bot for alice@example.com` | `comment #5..#7 by grain` | **None** (`4.4`–`4.6`) |
| Sandbox CLI [`grain task read 7`](2026-11-04-access-identity/grain-task-read.txt) | `Filed by: alice@example.com` | `operator (access code) (human)` | `external agent triage-bot for alice@example.com (agent)` | `grain (automation)` | **None** (`4.7`–`4.8`) |
| Controller MCP [`grain_get_task` (`summary`, `conversation`, `full`)](2026-11-04-access-identity/mcp-conversation.json) | `alice@example.com (human)` | `operator (access code) (human)` | `external agent triage-bot for alice@example.com (agent) [from triage-runner/1.2]` | `grain (automation)` | **None** (`4.9`–`4.11`) |
| Sidebar user tile (`1440` & `390`) | `alice@example.com` | `operator (access code)` | n/a | n/a | **None** |

### 5. Settings → Audit with partial scopes (`settings:read` only) (`5.1`–`5.3`, `UI-5-*`)

Tested both a verified IAP human (`auditor@example.com` assigned custom scope set `acme/audit-config-only` = `["settings:read"]`) and an external agent credential (`audit-reader` holding `["settings:read"]`):
- `GET /api/deployment/config-audit` → **`200 OK`**
- `GET /api/deployment/audit-logs` → **`403 Forbidden`** (requires `secrets:read`)
- `GET /api/deployment/secret-audit` → **`403 Forbidden`** (requires `secrets:read`)
- In Chromium at `/settings/audit` ([`audit-partial-1440-light.png`](2026-11-04-access-identity/screenshots/audit-partial-1440-light.png)), the **Configuration changes** card (`config-audit-table`) rendered normally with full rows, while **Credential access** (`team-audit-table-error`) and **Secret resolutions** (`secret-audit-table-error`) each showed clean warning alerts (`"The credential accesses could not be read..."`, `"The secret resolutions could not be read..."`) with zero uncaught page errors and zero horizontal overflow.

### 6. Identity verifier down (`6.1`–`6.4`, `UI-6a`)

Started a second `grain daemon` pointing `-ui-iap-keys-url` at an closed local port (`http://127.0.0.1:18299/jwks`). After fixing **F1** and **F2**:
- **API (`GET /api/tasks` with `X-Goog-IAP-JWT-Assertion`):** Returned **`503 Service Unavailable`** with `Retry-After: 5` and sanitized correlation JSON:
  ```json
  {"error":"an upstream service could not be reached (ref 3d06596616f8)","code":"upstream_unavailable","ref":"3d06596616f8"}
  ```
- **Access code does not bypass (`6.4`):** Presenting `Authorization: Bearer <access-code>` alongside `X-Goog-IAP-JWT-Assertion` still returned **`503 Service Unavailable`**.
- **Browser (`GET /` in Chromium, [`verifier-down-1440-light.png`](2026-11-04-access-identity/screenshots/verifier-down-1440-light.png)):** Returned **`503 Service Unavailable`**, `Retry-After: 5`, and a styled HTML page titled **`Sign-in verification is starting up`** instructing the user to `"Reload the page in a few seconds"`, rather than a 401 bad-assertion page.

### 7. CLI credential edges (`7.1`–`7.6`)

- **`GRAIN_USER` → `-as` (`7.1`–`7.2b`):**
  - `GRAIN_USER=dana@example.com grain -server <echo> list` sent `X-Forwarded-User: dana@example.com` automatically.
  - Passing `-as explicit@example.com` before the verb or `-as post-verb@example.com` after the verb overrode `GRAIN_USER`.
- **Rejected credentials exit `3` naming the exact source (`7.3`–`7.6`):**
  - `GRAIN_TOKEN="bad-secret-value" grain -server http://127.0.0.1:14310 list` exited **`3`** and named `$GRAIN_TOKEN`:
    `grain: the grain daemon at http://127.0.0.1:14310 refused this CLI's credentials (...): check $GRAIN_TOKEN or run 'grain login http://127.0.0.1:14310'`
  - `GRAIN_AUTH_TOKEN="bad-auth-value"` exited **`3`** naming `$GRAIN_AUTH_TOKEN`, and `-token "bad-flag-value"` exited **`3`** naming `-token`.

---

## Findings and fixes

### F1: Built-in IAP verifier returned `401 Unauthorized` instead of `503 Service Unavailable` when the JWKS endpoint was unreachable (fixed)

- **Reproduction before fix:** Start `grain daemon` with `-ui-iap-audience ... -ui-iap-issuer ... -ui-iap-keys-url http://127.0.0.1:18299/jwks` (closed port) and request `/api/tasks` or `/` in a browser with an `X-Goog-IAP-JWT-Assertion`. Before this branch:
  - API returned `HTTP/1.1 401 Unauthorized` `{"error":"identity assertion was not accepted: invalid IAP JWT assertion: ... connect: connection refused"}`.
  - Browser received a `401` `"Sign-in could not be verified"` page blaming clock skew or key rotation instead of `503` `"Sign-in verification is starting up"`.
- **Root cause:** `principalrefusal.go`'s `verifierUnavailable(err)` checks for `Unavailable() bool`, which `CloudIdentityGroupChecker` implemented via `GroupLookupUnavailableError`, whereas `pkg/jwks.Fetch` (used by `webauth.IAPVerifier`) returned plain `fmt.Errorf(...)` on transport errors and HTTP `5xx`/`429` responses.
- **Fix & regression test:** Wrapped transport errors (excluding `context.Canceled` and HTTPS→HTTP downgrade redirects) and upstream HTTP `500..599` / `429` responses in `jwks.Fetch` with `unavailableError` (`Unavailable() bool { return true }`) in [`pkg/jwks/jwks.go`](../../pkg/jwks/jwks.go). Added `TestFetchMarksTransportAnd5xxAnd429AsUnavailableAndKeeps4xxPermanent` in [`pkg/jwks/jwks_test.go`](../../pkg/jwks/jwks_test.go).

### F2: `assertion_diagnostics.go` misclassified unavailable verifiers as `"unverifiable"` and suppressed subsequent unknown-`kid` 401 logs (fixed)

- **Reproduction & root cause:** `refusalKind(err)` in [`pkg/ui/assertion_diagnostics.go`](../../pkg/ui/assertion_diagnostics.go) matched `strings.Contains(msg, "token is unverifiable")` before checking `verifierUnavailable(err)`. Because `jwt/v5` wraps keyfunc errors in `"token is unverifiable: error while executing keyfunc: ..."`, a JWKS outage logged misleading `401` bad-assertion guidance and tripped the per-minute `"unverifiable"` rate limiter, silencing a genuine unknown-`kid` `401` after JWKS recovered.
- **Fix & regression test:** Added an explicit `if verifierUnavailable(err) { return "unavailable" }` branch first in `refusalKind` and a dedicated `"unavailable"` log message (`"ui: could not verify ... yet ... callers receive 503 until it answers"`). Added `TestBuiltInIAPVerifierReturns503WhenJWKSDownAndLogsUnknownKidAfterRecovery` in [`pkg/ui/assertion_refused_page_test.go`](../../pkg/ui/assertion_refused_page_test.go).

### F3: `grain daemon` lacked `-ui-cloud-identity-endpoint` (fixed)

- **Reproduction & root cause:** `grain demo` accepted `-ui-cloud-identity-endpoint`, but `grain daemon` logged `WARNING: starting anyway, ignoring 1 flag(s) this build does not define: -ui-cloud-identity-endpoint` and hardcoded `https://cloudidentity.googleapis.com` in `pkg/daemon/ui_server.go`.
- **Fix & regression test:** Added `-ui-cloud-identity-endpoint` (with `$GRAIN_CLOUD_IDENTITY_ENDPOINT` fallback) to [`pkg/cli/daemon.go`](../../pkg/cli/daemon.go), [`pkg/daemon/config.go`](../../pkg/daemon/config.go), [`pkg/daemon/ui_server.go`](../../pkg/daemon/ui_server.go), and [`pkg/webauth/groups.go`](../../pkg/webauth/groups.go), and updated [`pkg/cli/testdata/daemon_help.golden`](../../pkg/cli/testdata/daemon_help.golden) and [`design/spec/cli/host.spec.md`](../../design/spec/cli/host.spec.md) (`TestHostCLISpecCoversEveryCommandAndFlag`).

### F4: `recordConfigWrite` dropped `X-Grain-Acting-Reason` from `config_audit` and Settings → Audit (fixed)

- **Reproduction before fix:** Have an agent task holding `api:settings:write` call `PUT /api/settings` with `X-Grain-Acting-Reason: enable telemetry check from task run`. `agentAPIGate` enforced the header's presence, but `GET /api/deployment/config-audit` recorded `"summary": "PUT /api/settings"` without the reason, so Settings → Audit never showed why the agent changed settings.
- **Root cause:** `recordConfigWrite` in [`pkg/ui/write_actor.go`](../../pkg/ui/write_actor.go) set `summary := r.Method + " " + r.URL.Path` and ignored `r.Header.Get(ActingReasonHeader)`.
- **Fix & regression test:** Appended `" (" + reason + ")"` (with CR/LF normalized to spaces, matching `access_principals.go`'s summary convention) when `ActingReasonHeader` is non-empty. Added `TestAgentConfigWriteRecordsActingReasonInConfigAudit` in [`pkg/ui/settings_writer_test.go`](../../pkg/ui/settings_writer_test.go).

### F5: `TaskChat` hid `PrincipalLabel` headers on outgoing human/operator bubbles in real browsers (fixed)

- **Reproduction before fix:** Open an interactive chat task (`/tasks/8`) touched by `alice@example.com`, `operator (access code)`, and `external agent triage-bot for alice@example.com` in Chromium. Incoming agent bubbles showed `external agent triage-bot for alice@example.com`, but outgoing human/access-code bubbles hid `alice@example.com` and `operator (access code)` (`display: none`).
- **Root cause:** `TaskChat.jsx` sets `position="single"` and `<Message.Header sender={e.sender} />` so every bubble carries its speaker's `PrincipalLabel`, but `@chatscope/chat-ui-kit-styles` sets `.cs-message--outgoing .cs-message__sender-name { display: none; }` and only unhides `.cs-message__sent-time` on `.cs-message--outgoing.cs-message--single` (jsdom unit tests did not catch this because jsdom does not apply vendor stylesheet `display: none` to `textContent`).
- **Fix & regression test:** Added `.task-chat-window .cs-message-list .cs-message.cs-message--outgoing.cs-message--single .cs-message__sender-name { display: block; }` in [`ui/src/style.css`](../../ui/src/style.css) and added Playwright regression test `TaskChat shows PrincipalLabel headers visibly on outgoing human bubbles` in [`ui/e2e/whoami-user-tile.spec.js`](../../ui/e2e/whoami-user-tile.spec.js).

### F6: `docs/agent-api.md` `access:write` description predated `AU-6.4` (fixed)

- Updated [`docs/agent-api.md`](../../docs/agent-api.md) to note that allowlist mutations are person-only (`AU-6.4`), so an agent holding `access:write` is still refused edits to `/api/access/principals`.

### Design notes (verified intentional behavior)

- **D1 (`AU-6.4` person-only allowlist guard):** Commit `f8629b38` intentionally tightened `/api/access/principals` mutations so even a task granted `api:access:write` by a person with a valid `X-Grain-Acting-Reason` is refused after passing the sandbox-cli and missing-reason gates (`"only a person changes the allowed users and groups list"`). All three layers (person-only grant, sandbox-cli channel denial, missing-reason denial) plus `AU-6.4` were verified in checks `3.1`–`3.9`.
- **D2 (Sanitized `503` API error body):** On `/api/*` routes, `writeAssertionRefused` returns `{"error":"an upstream service could not be reached (ref <hex>)","code":"upstream_unavailable","ref":"<hex>"}` with `Retry-After: 5` and logs the full upstream dial error + `ref` to the daemon log, avoiding leaking internal JWKS URLs to API callers.

---

## Screenshot inventory (`1440×900` and `390×844`, `light` and `dark`)

All 18 screenshots passed `scrollWidth <= clientWidth + 1` and zero uncaught `pageerror` checks, and are tiled into [`contact-sheet.png`](2026-11-04-access-identity/contact-sheet.png):

| Surface | `1440×900` light | `1440×900` dark | `390×844` light | `390×844` dark |
|---|---|---|---|---|
| Settings → Access | [`access-1440-light.png`](2026-11-04-access-identity/screenshots/access-1440-light.png) | [`access-1440-dark.png`](2026-11-04-access-identity/screenshots/access-1440-dark.png) | [`access-390-light.png`](2026-11-04-access-identity/screenshots/access-390-light.png) | [`access-390-dark.png`](2026-11-04-access-identity/screenshots/access-390-dark.png) |
| Settings → Audit (full) | [`audit-1440-light.png`](2026-11-04-access-identity/screenshots/audit-1440-light.png) | [`audit-1440-dark.png`](2026-11-04-access-identity/screenshots/audit-1440-dark.png) | [`audit-390-light.png`](2026-11-04-access-identity/screenshots/audit-390-light.png) | [`audit-390-dark.png`](2026-11-04-access-identity/screenshots/audit-390-dark.png) |
| Task page (4 actors + pause banner) | [`task-1440-light.png`](2026-11-04-access-identity/screenshots/task-1440-light.png) | [`task-1440-dark.png`](2026-11-04-access-identity/screenshots/task-1440-dark.png) | [`task-390-light.png`](2026-11-04-access-identity/screenshots/task-390-light.png) | [`task-390-dark.png`](2026-11-04-access-identity/screenshots/task-390-dark.png) |
| Sidebar (rail / open phone drawer) | [`sidebar-1440-light.png`](2026-11-04-access-identity/screenshots/sidebar-1440-light.png) | [`sidebar-1440-dark.png`](2026-11-04-access-identity/screenshots/sidebar-1440-dark.png) | [`sidebar-390-light.png`](2026-11-04-access-identity/screenshots/sidebar-390-light.png) | [`sidebar-390-dark.png`](2026-11-04-access-identity/screenshots/sidebar-390-dark.png) |
| Partial-scope Audit (`settings:read` only) | [`audit-partial-1440-light.png`](2026-11-04-access-identity/screenshots/audit-partial-1440-light.png) | — | — | — |
| Verifier down (`503` browser page) | [`verifier-down-1440-light.png`](2026-11-04-access-identity/screenshots/verifier-down-1440-light.png) | — | — | — |
