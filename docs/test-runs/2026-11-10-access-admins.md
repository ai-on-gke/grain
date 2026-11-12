# Test run: verified admins, access history, and limited-role approval ceiling (2026-11-10 plan §2)

- **Plan:** [`docs/test-plans/2026-11-10.md`](../test-plans/2026-11-10.md) §2 (filed from [Task #3930](/tasks/3930), executed in [Task #3949](/tasks/3949))
- **Scope:** PR #1054 (`f69f2e1f`, `c18be18e`, `8929db5a`, `406d9924`, `7cdd1c8f`) — verified admins managing dangerous/deny roles on others with a required reason, `self_escalation`, `self_access_change`, `confirm_self`, `last_admin`, `grain access role|deny --reason --confirm-self`, `GET /api/access/history`, Settings → Access reason panel and Access history card; PRs #987 (`6875630e`) and #1017 (`48cb44f2`) — limited roles' `approve` and `run-now` held to `tasks:admin` on the repository and the role's ceiling, including the task's review template (`reviewTemplateId`).
- **Harness:** [`docs/test-runs/2026-11-10-access-admins/`](2026-11-10-access-admins/) (`env.sh`, `ghsim.go`, `walkthrough.mjs`, `run_all.sh`, and redacted `item1-matrix.json` / `item4-approvals.json`).
- **Trust-boundary trigger:** `pkg/ui/access_principals.go` is classified under `auth-routes` in `pkg/trustboundary/trustboundary.go` (alongside `pkg/access/*`), so this change triggers the automated security review.

---

## Summary of findings and fixes

All five defects discovered during the live daemon + IAP/JWKS + Playwright walkthrough were in scope, deterministically reproduced against untouched `HEAD` (`c7651098f`), fixed, and pinned with Go, Vitest, and Playwright regression tests. No out-of-scope follow-up tasks were needed.

| # | Area | Defect reproduced against `HEAD` | Fix | Regression test |
|---|---|---|---|---|
| **F1** | `pkg/ui/access_principals.go` (`cleanAccessReason`) | `cleanAccessReason` stripped only `\r` and `\n`, leaving raw ANSI CSI/OSC/ESC sequences (`\x1b[31m…`, `\x1b]52;c;…\x07`), C0/C1 control bytes, and Unicode bidi format characters (`\u202e`) in `config_audit.summary`, `GET /api/access/history`, `allowed_principal.reason` (`grain access list`), and `daemon.log`. Worse, a visually blank reason consisting solely of ANSI escapes (`"\x1b[31m\x1b[0m\r\n"`) passed `cleanAccessReason != ""` and succeeded (`200 OK`) when assigning `grain/operator`. | Strip ANSI OSC (`\x1b\][^\x07\x1b]*(?:\x07\|\x1b\\)`), CSI (`\x1b\[[0-9;?]*[ -/]*[@-~]`), and two-byte ESC (`\x1b[@-_]`) sequences, fold whitespace runs to a single space, and drop `unicode.IsControl` / `unicode.Is(unicode.Cf, …)` / invalid UTF-8 runes. Escape-only reasons collapse to `""` and fail validation (`400 Bad Request`, `give a reason`). | `TestAVerifiedAdminChangesAnotherAdmin` (`pkg/ui/access_roles_test.go`), `TestVerifiedAdminAccessGuardrailsAndReasonSanitization` (`tests/security/access_admin_test.go`) |
| **F2** | `pkg/ui/access_principals.go` (`checkAccessChange`), `ui/src/components/settings/AccessSection.jsx` | Narrowing or removing oneself when one is the **sole full admin** ran `checkSelfAccessChange`'s `!ch.confirmSelf` check *before* `checkLastFullAdmin`. Both `/settings/access` and `grain access role|deny` therefore prompted the operator to confirm narrowing their own access (`403 confirm_self`), only to refuse the confirmed retry with `403 last_admin` — and `AccessSection.jsx` preserved `change.confirmSelf` on the error state, leaving the stale `"Yes, narrow my own access"` prompt and button label stuck alongside the `last_admin` alert. | Split `checkSelfAccessChange` into `checkSelfAccessEscalation` (still checked first so `self_escalation` and `self_access_change` win) and `checkSelfAccessConfirm` (checked *after* `checkLastFullAdmin`), so a sole full admin receives `403 last_admin` immediately with or without `confirmSelf`. Clear `confirmSelf` in `AccessSection.applyChange` when a confirmed request fails. | `TestAVerifiedAdminChangesAnotherAdmin` (`pkg/ui/access_roles_test.go`), `TestVerifiedAdminAccessGuardrailsAndReasonSanitization` (`tests/security/access_admin_test.go`), `AccessSection.test.jsx` |
| **F3** | `ui/src/components/SettingsPage.jsx`, `ui/src/components/settings/AccessSection.jsx` | `SettingsPage.jsx` listed `"access"` in `READS_SETTINGS` even though `AccessSection` never reads `GET /api/settings`. Any role without `settings:read` — including `carol-limited` and access-only administrators (`access:read` + `access:write`, the role `8929db5a` added `GET /api/access/history` for) — hit `403` on `GET /api/settings` and saw `"Deployment settings could not be read"` instead of `[Settings → Access](/settings/access)`. Inside `AccessSection`, `loadPrincipals()` and `loadHistory()` also fired unconditionally without `access:read`, raising 403 banners and displaying `"Your role does not include access:write, so you can see this list but not change it"` when the list could not be read at all. | Remove `"access"` from `READS_SETTINGS` in `SettingsPage.jsx`. Gate `loadPrincipals()` and `loadHistory()` on `canAccess(config, "access:read")` in `AccessSection.jsx`, and show `"Your role does not include access:read, so this list is hidden."` when `access:read` is absent. | `SettingsPage.test.jsx`, `AccessSection.test.jsx` |
| **F4** | `ui/src/App.jsx`, `ui/src/components/SettingsPage.jsx`, `ui/src/components/settings/AccessSection.jsx` | Confirming a self-narrowing in `[Settings → Access](/settings/access)` (e.g. `bob@example.com` narrowing `user:bob@example.com` from `grain/operator` to `grain/viewer` or `carol-limited`) re-read principals and history without calling `onSaved` (`refreshConfig`). Without a full browser reload, "This browser" continued to display `Your role: grain/operator` and left the `Add allowed principal` form, Role selects, and `Remove` buttons enabled (or left stale rows visible and fired 403 GETs when `access:read` was lost). | Pass `onSaved` from `SettingsPage` to `AccessSection`, return the refreshed config from `App.fetchConfig`, await `onSaved()` in `AccessSection.reload()`, and clear stale entries/history without firing forbidden GETs when the refreshed config lacks `access:read`. | `AccessSection.test.jsx` + live Chromium verification (`3a`, `3b`) |
| **F5** | `ui/src/components/AccessGate.jsx`, `ui/src/components/detail/Actions.jsx`, `ui/src/components/BatchActionsBar.jsx` | After PR #987 (`6875630e`), `POST /api/tasks/{id}/run-now` on an unapproved task requires `tasks:admin` on the task's repository in addition to `dispatch:write`, but the task pane's `Run now` button (`Actions.jsx`) and batch bar's `Run selected now` (`BatchActionsBar.jsx`) still gated only on `dispatch:write`. For `carol@example.com` (`tasks:admin@acme/widgets` + global `dispatch:write`) viewing a proposed task on `acme/other`, `Approve` was disabled by `AccessGate` while `Run now` stayed enabled and produced a 403 toast on click. | Support multi-scope arrays in `AccessGate.jsx` and require `["dispatch:write", "tasks:admin"]` on `Run now` (and `tasks:admin` on unapproved repos in `BatchActionsBar.jsx`) when a task is unapproved (`proposed` or unapproved `deferred`), keeping `dispatch:write` alone once approved. | `Actions.test.jsx`, `BatchActionsBar.test.jsx` + live Chromium verification (`other-repo:run-now`) |

---

## Environment

1. Built frontend and host/daemon binary from `HEAD`:
   - `cd ui && npm ci && npm run build`
   - `go build -o /home/debian/bin/grain ./cmd/grain`
2. `docs/test-runs/2026-11-10-access-admins/env.sh` launched:
   - `tests/iap/iapsim.go` on `127.0.0.1:4292` (ES256 JWKS, `/sign`, `/token`, fake Cloud Identity v1 groups endpoint with `ops@example.com=bob@example.com,temp@example.com`) and `127.0.0.1:4293` (reverse proxy stripping spoofed headers, signing `X-Goog-IAP-JWT-Assertion`, supporting `/_iapsim/login` cookies for Chromium and `Bearer iapsim-idtoken.<email>` for the `grain` CLI).
   - `docs/test-runs/2026-11-10-access-admins/ghsim.go` on `127.0.0.1:4294`, serving `githubsim` REST and `git-http-backend` smart HTTP for both `acme/widgets` (with `.grain/templates/review.yml` declaring `repo:template:acme:widgets:review` carrying `repo#read` on `acme/other`) and `acme/other`.
   - Real `grain daemon -sandbox-port-base=0 -disable-native-sandboxes -ci=false -poll-interval 2s` over SQLite on `127.0.0.1:4291` with `-ui-iap-audience`, `-ui-iap-issuer`, `-ui-iap-keys-url`, and (in Phase 1) `-ui-cloud-identity-endpoint` + service-account ADC JSON; in Phase 2 (`nogroup`), started with no `GOOGLE_APPLICATION_CREDENTIALS` and no `-ui-cloud-identity-endpoint`.
3. Custom scope sets installed via `PUT /api/api-scope-sets`:
   - `widgets-admin`: `tasks:read`, `tasks:write`, `tasks:admin` limited to `repos: ["acme/widgets"]`.
   - `carol-limited`: `tasks:read`, `repos:read`, `templates:read`, `dispatch:read`, `dispatch:write` + `includes: ["widgets-admin"]` (giving `carol@example.com` `tasks:admin@acme/widgets`, `tasks:write@acme/widgets`, global `dispatch:write`, and global read visibility to open tasks on `acme/other` in the UI).
   - `viewer-no-secrets`: `includes: ["grain/viewer"]`, `deny: ["secrets:read"]`.
   - `operator-no-secrets`: `includes: ["grain/operator"]`, `deny: ["secrets:read"]`.
4. Seeded principals:
   - `user:alice@example.com` — `grain/operator` (full admin; not in `group:ops@example.com`)
   - `user:bob@example.com` — `grain/operator` (full admin; also a member of `group:ops@example.com`)
   - `group:ops@example.com` — `grain/dispatch-control`
   - `user:carol@example.com` — `carol-limited`

---

## Item 1: Access rules across verified identities, agent tokens, and the access code

Every scenario reset the allowlist to its deterministic pre-state and drove the identical mutation across:
1. **REST** (`PATCH` / `DELETE /api/access/principals/{entry}` with IAP ES256 JWT, acting-task + `gma_` agent tokens, or `Bearer <REDACTED>` access code),
2. **CLI** (`grain access role|deny [--reason …] [--confirm-self]` through the IAP proxy with `iapsim-idtoken.<email>`, `gma_<REDACTED>`, or access code), and
3. **UI** (`[Settings → Access](/settings/access)` in Playwright Chromium signed in through IAP or access code).

All three surfaces agreed on all 75 cells (15 changes × 5 callers), and every accepted change wrote **exactly one** row (`audit Δ = 1`) in `GET /api/access/history` carrying the sanitized reason (`audit Δ = 0` on every refusal). Full machine-readable output is in [`item1-matrix.json`](2026-11-10-access-admins/item1-matrix.json).

| Change | Caller | REST | CLI (`grain access`) | UI (`/settings/access`) | History Δ | Code / Key message |
|---|---|---|---|---|---|---|
| `other:narrow` (`user:dave` `grain/operator → grain/viewer`) | `alice` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/operator -> grain/viewer (narrow dave)` |
| `other:narrow` | `bob` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/operator -> grain/viewer (narrow dave)` |
| `other:narrow` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write, which PATCH /api/access/principals/{entry} needs` |
| `other:narrow` | `agent` (`task:1` & `gma_`) | 403 | exit 3 | n/a (no browser login) | 0 | `agent … may not change who signs in to this deployment: only a person changes the allowed users and groups list` |
| `other:narrow` | `access-code` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/operator -> grain/viewer (narrow dave)` |
| `other:widen` (`user:dave` `grain/viewer → grain/operator`) | `alice` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/viewer -> grain/operator (widen dave)` |
| `other:widen` | `bob` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/viewer -> grain/operator (widen dave)` |
| `other:widen` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `other:widen` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `other:widen` | `access-code` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/viewer -> grain/operator (widen dave)` |
| `other:add-deny` (`user:dave` `grain/viewer → viewer-no-secrets`) | `alice` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/viewer -> viewer-no-secrets (add deny on dave)` |
| `other:add-deny` | `bob` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/viewer -> viewer-no-secrets (add deny on dave)` |
| `other:add-deny` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `other:add-deny` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `other:add-deny` | `access-code` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/viewer -> viewer-no-secrets (add deny on dave)` |
| `other:lift-deny` (`user:dave` `viewer-no-secrets → grain/viewer`) | `alice` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com viewer-no-secrets -> grain/viewer (lift deny on dave)` |
| `other:lift-deny` | `bob` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com viewer-no-secrets -> grain/viewer (lift deny on dave)` |
| `other:lift-deny` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `other:lift-deny` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `other:lift-deny` | `access-code` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com viewer-no-secrets -> grain/viewer (lift deny on dave)` |
| `self-direct:narrow-unconfirmed` (`user:<self>` `→ grain/viewer`, no `confirmSelf`) | `alice` | 403 | exit 3 | refused (panel) | 0 | `confirm_self`: `user:alice@example.com matches you: confirm that you want to change its role …` |
| `self-direct:narrow-unconfirmed` | `bob` | 403 | exit 3 | refused (panel) | 0 | `confirm_self`: `user:bob@example.com matches you: confirm that you want to change its role …` |
| `self-direct:narrow-unconfirmed` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `self-direct:narrow-unconfirmed` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `self-direct:narrow-unconfirmed` (`user:dave`) | `access-code` | 200 | exit 0 | accepted | +1 | `role user:dave@example.com grain/operator -> grain/viewer` |
| `self-direct:narrow-confirmed` (`user:<self>` `→ grain/viewer`, `confirmSelf`) | `alice` | 200 | exit 0 | accepted | +1 | `role user:alice@example.com grain/operator -> grain/viewer (self narrow confirmed)` |
| `self-direct:narrow-confirmed` | `bob` | 200 | exit 0 | accepted | +1 | `role user:bob@example.com grain/operator -> grain/viewer (self narrow confirmed)` |
| `self-direct:narrow-confirmed` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `self-direct:narrow-confirmed` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `self-direct:narrow-confirmed` (`user:bob`) | `access-code` | 200 | exit 0 | accepted | +1 | `role user:bob@example.com grain/operator -> grain/viewer` |
| `self-direct:widen` (`user:<self>` `grain/contributor → grain/operator`) | `alice` | 403 | exit 3 | gated (`no access:write`) | 0 | `holds no scope access:write` |
| `self-direct:widen` (`bob` holds `grain/operator` via `group:ops`, `grain/contributor` on `user:bob`) | `bob` | 403 | exit 3 | refused (panel) | 0 | `self_escalation`: `user:bob@example.com matches you: you may not widen it from grain/contributor to grain/operator` |
| `self-direct:widen` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `self-direct:widen` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `self-direct:widen` (`user:carol`) | `access-code` | 200 | exit 0 | accepted | +1 | `role user:carol@example.com carol-limited -> grain/operator` |
| `self-direct:add-deny` (`user:<self>` `→ operator-no-secrets`) | `alice` | 403 | exit 3 | refused (panel) | 0 | `self_access_change`: `you may not add or lift a deny on an entry that matches yourself` |
| `self-direct:add-deny` | `bob` | 403 | exit 3 | refused (panel) | 0 | `self_access_change`: `you may not add or lift a deny on an entry that matches yourself` |
| `self-direct:add-deny` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `self-direct:add-deny` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `self-direct:add-deny` (`user:bob`) | `access-code` | 200 | exit 0 | accepted | +1 | `role user:bob@example.com grain/operator -> operator-no-secrets` |
| `self-direct:lift-deny` (`user:<self>` `operator-no-secrets → grain/operator`) | `alice` | 403 | exit 3 | refused (panel) | 0 | `self_access_change`: `you may not add or lift a deny on an entry that matches yourself` |
| `self-direct:lift-deny` | `bob` | 403 | exit 3 | refused (panel) | 0 | `self_access_change`: `you may not add or lift a deny on an entry that matches yourself` |
| `self-direct:lift-deny` | `carol` | 403 | exit 3 | gated (`no access:write`) | 0 | `holds no scope access:write` |
| `self-direct:lift-deny` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `self-direct:lift-deny` (`user:bob`) | `access-code` | 200 | exit 0 | accepted | +1 | `role user:bob@example.com operator-no-secrets -> grain/operator` |
| `group:narrow-unconfirmed` (`group:ops` `grain/dispatch-control → grain/pipeline-control`) | `alice` | 200 | exit 0 | accepted | +1 | `alice` is not in `group:ops`: accepted without `confirmSelf` |
| `group:narrow-unconfirmed` (`bob` in `group:ops`) | `bob` | 403 | exit 3 | refused (`confirm_self`) | 0 | `confirm_self`: `group:ops@example.com matches you: confirm that you want to change its role …` |
| `group:narrow-unconfirmed` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `group:narrow-unconfirmed` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `group:narrow-unconfirmed` | `access-code` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> grain/pipeline-control` |
| `group:narrow-confirmed` (`group:ops` `→ grain/pipeline-control`, `confirmSelf`) | `alice` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> grain/pipeline-control` |
| `group:narrow-confirmed` (`bob` in `group:ops`) | `bob` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> grain/pipeline-control` |
| `group:narrow-confirmed` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `group:narrow-confirmed` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `group:narrow-confirmed` | `access-code` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> grain/pipeline-control` |
| `group:widen` (`group:ops` `grain/dispatch-control → grain/operator`) | `alice` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> grain/operator (widen ops group)` |
| `group:widen` (`bob` in `group:ops`) | `bob` | 403 | exit 3 | refused (panel) | 0 | `self_escalation`: `group:ops@example.com matches you: you may not widen it from grain/dispatch-control to grain/operator` |
| `group:widen` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `group:widen` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `group:widen` | `access-code` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> grain/operator` |
| `group:add-deny` (`group:ops` `→ viewer-no-secrets`) | `alice` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> viewer-no-secrets (add deny on ops group)` |
| `group:add-deny` (`bob` in `group:ops`) | `bob` | 403 | exit 3 | refused (panel) | 0 | `self_access_change`: `group:ops@example.com matches you: you may not add or lift a deny` |
| `group:add-deny` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `group:add-deny` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `group:add-deny` | `access-code` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com grain/dispatch-control -> viewer-no-secrets` |
| `group:lift-deny` (`group:ops` `viewer-no-secrets → grain/dispatch-control`) | `alice` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com viewer-no-secrets -> grain/dispatch-control (lift deny on ops group)` |
| `group:lift-deny` (`bob` in `group:ops`) | `bob` | 403 | exit 3 | refused (panel) | 0 | `self_access_change`: `group:ops@example.com matches you: you may not add or lift a deny` |
| `group:lift-deny` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `group:lift-deny` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `group:lift-deny` | `access-code` | 200 | exit 0 | accepted | +1 | `role group:ops@example.com viewer-no-secrets -> grain/dispatch-control` |
| `last-admin:remove` (`DELETE user:alice` when `bob` is `grain/viewer`) | `alice` | 403 | exit 3 | refused (panel) | 0 | `last_admin`: `user:alice@example.com is the last entry holding a full admin role (grain/operator): refusing to remove it … (the access code can still recover the deployment)` |
| `last-admin:remove` | `bob` | 403 | exit 3 | gated (`no access:write`) | 0 | `holds no scope access:write` |
| `last-admin:remove` | `carol` | 403 | exit 3 | gated (`no access:read`) | 0 | `holds no scope access:write` |
| `last-admin:remove` | `agent` | 403 | exit 3 | n/a | 0 | `only a person changes the allowed users and groups list` |
| `last-admin:remove` | `access-code` | 200 | exit 0 | accepted | +1 | `removed user:alice@example.com (was grain/operator) (remove last admin)` |

---

## Item 2: Hardening bypass checks (`7cdd1c8f`)

| Probe | Outcome |
|---|---|
| **Group-to-direct persistence (`POST user:temp@example.com`)**: `temp@example.com` admitted via `group:ops@example.com` (`grain/operator`) attempts `POST /api/access/principals {"entry":"user:temp@example.com","role":"grain/operator","reason":"persist"}` | **Refused `403`** (`code: "self_escalation"`): `user:temp@example.com matches you: you may not add an entry that matches yourself (role grain/operator)` |
| **`DELETE ?confirmSelf=1` then `POST` wider (direct & group)**: (1) `bob` (holding `grain/operator` via `group:ops` and `grain/contributor` on `user:bob`) deletes `user:bob@example.com?confirmSelf=1` (`200`), then POSTs `user:bob@example.com` as `grain/operator`; (2) `bob` deletes `group:ops@example.com?confirmSelf=1` (`200`), then POSTs `group:ops@example.com` wider (`grain/dispatch-control`), and `temp` attempts POST `user:temp@example.com` | **Closed**: `bob`'s re-POSTs fail `403` (`code: "self_escalation"`), and `temp`'s session is immediately cut off (`403` not on allowlist) once `group:ops` is removed. |
| **Agent `POST` of an already-listed entry**: both an in-sandbox acting-task token (`X-Grain-Acting-Task: 1`) and an external `gma_<REDACTED>` credential holding `access:read` + `access:write` send `POST /api/access/principals {"entry":"user:alice@example.com"}` | **Refused `403`** (`agent … may not change who signs in to this deployment: only a person changes the allowed users and groups list`), never `200 OK` idempotent. |
| **Daemon with `group:` entries and no group checker configured** (`run_all.sh` Phase 2, no ADC and no `-ui-cloud-identity-endpoint`) | **Fails closed**: (1) group-only `temp@example.com` gets `403` on `/api/whoami`; (2) `alice@example.com` (not in `group:ops`) attempting to widen or POST a `group:` entry gets `403 self_escalation` (`"may match you (its membership could not be checked)"`), adding/lifting a deny gets `403 self_access_change`, and deleting requires `?confirmSelf=1` (`403 confirm_self` → `200`); (3) once a deny-bearing group (`group:veto@example.com` = `viewer-no-secrets`) exists, even `alice@example.com` fails closed (`403`) because a potential group veto cannot be ruled out. |
| **Reason containing `\r\n`, ANSI CSI/OSC escapes, and bidi controls** (`"rotate\r\nforged-line\x1b[31mRED\x1b[0m\x1b]52;c;dGVzdA==\x07end"`) and pure-escape reason (`"\x1b[31m\x1b[0m\r\n"`) | **Found F1 & fixed**: before the fix, raw `\x1b` and `\x07` landed in `config_audit.summary` and `daemon.log`, and the pure-escape reason passed validation (`200 OK`). After the fix, `config_audit.summary` records `(rotate forged-line RED end)` with zero control/escape bytes in SQLite, `GET /api/access/history`, `grain access list`, or `daemon.log`, and the pure-escape reason is refused `400 Bad Request` (`give a reason`). |

---

## Item 3: Role-holder self-lockout UX and `last_admin` panel

Driven in Playwright Chromium against the real daemon behind `iapsim`:
1. **`bob@example.com` narrowing `user:bob@example.com` (`grain/operator → grain/viewer`)**:
   - Selecting `grain/viewer` opens `data-testid="access-change-panel"` asking for a reason; submitting opens `data-testid="access-confirm-self"` (`"user:bob@example.com matches you: confirm that you want to change its role grain/operator -> grain/viewer, which narrows your own access (confirmSelf, or --confirm-self)"`) with button `"Yes, narrow my own access"`.
   - Clicking `"Yes, narrow my own access"` applies the `PATCH` with `confirmSelf: true` and — **after fixing F4** — immediately refreshes `GET /api/config` without a browser reload: `This browser` updates to `Your role: grain/viewer — actions outside it are hidden or refused.`, all `Remove` buttons, Role dropdowns, and the `Add allowed principal` form disappear, and `data-testid="access-read-only"` renders `"Your role does not include access:write, so you can see this list but not change it."`
2. **`bob@example.com` narrowing `user:bob@example.com` to a role without `access:read` (`carol-limited`)**:
   - **After fixing F3 & F4**, confirming immediately updates `Your role: carol-limited — actions outside it are hidden or refused.`, clears the previously rendered allowlist and history cards, fires **zero** 403 GET requests on `/api/access/principals` or `/api/access/history`, and displays `"Your role does not include access:read, so this list is hidden."`
3. **`last_admin` verbatim on the panel**:
   - With `user:alice@example.com` (`grain/operator`) as the sole full admin (`user:bob@example.com` = `grain/viewer`), `alice` attempting to narrow `user:alice@example.com` to `grain/viewer` — **after fixing F2** — immediately displays the exact server `last_admin` message verbatim inside `data-testid="access-change-error"` on the first click (`"user:alice@example.com is the last entry holding a full admin role (grain/operator): refusing to change its role grain/operator -> grain/viewer, which would leave nobody to sign in as a full admin; give another entry grain/operator first (the access code can still recover the deployment)"`), with no intermediate `confirm_self` prompt.

---

## Item 4: Limited-role approval and run-now ceiling across every surface

Signed in as `carol@example.com` (`carol-limited`: `tasks:admin@acme/widgets`, `tasks:write@acme/widgets`, global `dispatch:write`, global `tasks:read`), each proposed task case was driven over **REST**, **CLI** (`grain approve <id>` and `grain run-now <id>`), **UI task pane** (`/tasks/<id>`), and **UI Inbox** (`/inbox`; note that `/inbox`'s `Approve a proposal` rows offer `Approve`, `Defer`, and `Decline` — there is no `Run now` button on proposed Inbox rows). Full data is in [`item4-approvals.json`](2026-11-10-access-admins/item4-approvals.json).

| Case | Action | Expected | REST | CLI (`grain approve` / `run-now`) | UI Task Pane (`/tasks/<id>`) | UI Inbox (`/inbox`) | Resulting task state |
|---|---|---|---|---|---|---|---|
| `acme/widgets` without `autoMerge` | `approve` | **Accept** | `200` | exit 0 | enabled → `200` | enabled → `200` | `queued` (`approved: true`) |
| `acme/widgets` without `autoMerge` | `run-now` | **Accept** | `200` | exit 0 | enabled → `200` | *(no Run now on Inbox proposal rows)* | `queued`/`running` (`approved: true`) |
| `acme/widgets` with `autoMerge: true` | `approve` | **Refuse** | `403` (`autoMerge`) | exit 3 | enabled → `403` toast | enabled → `403` toast | `proposed` (`approved: false`) |
| `acme/widgets` with `autoMerge: true` | `run-now` | **Refuse** | `403` (`autoMerge`) | exit 3 | enabled → `403` toast | — | `proposed` (`approved: false`, `runNowAt: null`) |
| `acme/widgets` with dangerous grant (`api#settings:write`) | `approve` | **Refuse** | `403` (`capabilities`) | exit 3 | enabled → `403` toast | enabled → `403` toast | `proposed` (`approved: false`) |
| `acme/widgets` with dangerous grant (`api#settings:write`) | `run-now` | **Refuse** | `403` (`capabilities`) | exit 3 | enabled → `403` toast | — | `proposed` (`approved: false`, `runNowAt: null`) |
| `acme/widgets` with `writes` outside (`acme/other`) | `approve` | **Refuse** | `403` (`writes`) | exit 3 | enabled → `403` toast | enabled → `403` toast | `proposed` (`approved: false`) |
| `acme/widgets` with `writes` outside (`acme/other`) | `run-now` | **Refuse** | `403` (`writes`) | exit 3 | enabled → `403` toast | — | `proposed` (`approved: false`, `runNowAt: null`) |
| `acme/widgets` with non-default review template (`heavy-review`: `api#settings:write`) | `approve` | **Refuse** | `403` (`reviewTemplateId`) | exit 3 | enabled → `403` toast | enabled → `403` toast | `proposed` (`approved: false`) |
| `acme/widgets` with non-default review template (`heavy-review`: `api#settings:write`) | `run-now` | **Refuse** | `403` (`reviewTemplateId`) | exit 3 | enabled → `403` toast | — | `proposed` (`approved: false`, `runNowAt: null`) |
| `acme/widgets` with non-default review template (`other-write-review`: `writes` `acme/other`) | `approve` | **Refuse** | `403` (`reviewTemplateId`) | exit 3 | enabled → `403` toast | enabled → `403` toast | `proposed` (`approved: false`) |
| `acme/widgets` with non-default review template (`other-write-review`: `writes` `acme/other`) | `run-now` | **Refuse** | `403` (`reviewTemplateId`) | exit 3 | enabled → `403` toast | — | `proposed` (`approved: false`, `runNowAt: null`) |
| `acme/widgets` with default review template (`repo:template:acme:widgets:review`) | `approve` | **Accept** | `200` | exit 0 | enabled → `200` | enabled → `200` | `queued` (`approved: true`) |
| `acme/widgets` with default review template (`repo:template:acme:widgets:review`) | `run-now` | **Accept** | `200` | exit 0 | enabled → `200` | — | `queued`/`running` (`approved: true`) |
| `acme/other` | `approve` | **Refuse** | `403` (`tasks:admin`) | exit 3 | disabled by `AccessGate` (`tasks:admin`) | disabled by `AccessGate` (`tasks:admin`) | `proposed` (`approved: false`) |
| `acme/other` | `run-now` | **Refuse** | `403` (`tasks:admin`) | exit 3 | disabled by `AccessGate` (`tasks:admin`, **F5 fix**) | — | `proposed` (`approved: false`, `runNowAt: null`) |

---

## Item 5: Visual and layout check (`[Settings → Access](/settings/access)`)

1. Rendered `[Settings → Access](/settings/access)` over `grain demo` before and after fixes with `make ui-screenshots PAGES=/settings/access` (`1440×900` desktop and `390×844` phone, light and dark; `contact-sheet.png` attached to the closing report).
2. Rendered `[Settings → Access](/settings/access)` on the live IAP-backed daemon (`runItem5Visual` in `walkthrough.mjs`) and in `ui/e2e/access-principals-settings.spec.js` with:
   - 100 history rows in the Access history card,
   - a 200+ character reason in both the history `Change` column and the open `access-change-panel`, and
   - the multi-line `last_admin` refusal alert inside `access-change-panel`.
3. Verified at both `1440×900` and `390×844` (light and dark):
   - `document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1` (zero horizontal page overflow),
   - `access-change-panel`, `access-change-error`, and action buttons stay strictly within `#allowed-principals` with `Apply`/`Cancel` stacked cleanly below the refusal alert, and
   - `#access-history` sits below `#allowed-principals` and wraps long reasons cleanly inside the `Change` column (`wordBreak: "break-word"`).
