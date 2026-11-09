# Audit: what a caller with no identity can do, and what the access code does without one

- **Date:** 2026-11-09 (UTC)
- **Auditor:** grain task 3873 (agent)
- **Base audited:** `origin/main` at `d79c6aff` (merge of #1049)
- **Question (operator, 2026-11-09):** "can you do operations without an
  identity today?"
- **Method:** every route `pkg/ui`'s `Server` registers was read off
  `Server.Routes()` -- the same list `tests/security`'s
  `TestRouteAuthorization` holds to `routeAuthorization` -- and probed
  twice with a throwaway test over that test's own fixture
  (`newRouteFixtureFor(t, true)`, a single-tenant daemon with one task and
  one repo): once from an address outside every trusted network carrying
  **no identity and no access code** (and claiming to be the system
  admin in `?user=`), and once with **only the access code's session**.
  The status each answered is in the appendix. The open routes, the gate
  (`pkg/ui/accesscode.go` `singleTenantGate`, `singleTenantOpenRoute`),
  `serveHTTP`'s admission (`pkg/ui/server.go`), the hub tunnel's front
  door, and the listeners grain runs beside the UI were then read.

## Summary

1. **With no identity and no access code, nothing reads grain's data or
   changes its state.** Of the 377 registered patterns, 365 answer such a
   caller 401 before any handler runs. The 12 that answer are the
   sign-in pages, signing out, the install scripts (trimmed for a
   stranger), and a host dialing in or enrolling, which authenticate in
   their own protocol (table below). No finding to file on this side.
   `.grain/baselines/route-authorization.txt` is empty, so
   `TestRouteAuthorization` already fails any new route that answers
   such a caller, and is the generated table task 3873 asked for.
2. **With the access code and no identity, a caller can do everything.**
   All 365 gated routes accept the code alone -- as a bearer token, in
   `X-Grain-Access-Code`, or as the `grain_access` session cookie -- and
   run it as the deployment's own actor (`RequestActor`, source
   `access-code`) with every scope: no role is applied
   (`accessRoleFromContext` is nil), and `access_principals.go` treats
   it as the only caller that may assign a dangerous role. Nothing
   recorded *who* held the code: tasks show "Filed by: operator (access
   code)", and the config audit shows the same actor for every holder.
   This is the gap task 3873 fixes (see "What changed").

## Who authenticates what

| Surface | Authenticated by | Identity-less, no code |
|---|---|---|
| Every `/api/...` route in the route table (365 patterns) | A verified identity (IAP JWT, a registered identity verifier such as a trusted identity proxy's headers, the hub's assertion) admitted by the allowlist and held to its role (`roleGate`); a run or session token (`agentAPIGate`, held to its scopes); a named external agent credential (`gma_...`, `authenticateExternalAgent`); or the access code | 401 from `singleTenantGate` |
| `/` (frontend, SPA routes, static assets) | Same gate | 401 and the "Enter access code" page (or the verifier-required page behind `-ui-require-verifier`) |
| `/mcp`, `/api/mcp` | External agent credential, or the gate | 401 |
| `/staging/{name}/{path...}` (persistent sandbox proxy) | The gate (`requiresAccess`) | 401 |
| Websockets and streams (console, terminal, event streams) | In the route table, same gate | 401 |
| `GET/POST /api/auth/access-code`, `.../login.js` | Open by design: the sign-in itself, rate-limited per source and globally (`accessLimiter`) | Login page script; `{"authenticated":false}`; a wrong code is 401 and counted |
| `GET/POST /api/auth/logout` | Open | Clears a cookie the caller does not have |
| `GET /api/auth/whoami` | Open | `acceptedAuth` (which sign-ins this daemon takes) and whether the external agent API is on -- what a CLI needs to choose how to sign in; no data |
| `GET /api/hostd/install.sh`, `GET /hostd/install`, `GET /cli/install.sh` | Open | The install scripts; the daemon's release and image are left out for a stranger (`hostdInstallCallerHasAccess`) |
| `GET /api/hostd/connect` | The host's own signed protocol (hostd token) | 503/401 without a host credential |
| `POST /api/hostd/join`, `.../join/{id}/poll`, `.../renew` | An enrollment code and the host's ed25519 key; pending joins are bounded per code and identity | 400 without a valid signature; a join waits for a person's approval |
| Hub tunnel requests (`isHubTunnelRequest`) | The hub assertion only; the access code, its cookie, bearer codes and approval-callback tokens are refused outright | 401 |
| Approval callback (`presentsApprovalCallbackToken`) | A per-task callback token, runs as that task | 401 without it |
| In-process requests (no `RemoteAddr`) | Trusted as the daemon itself | Not reachable from the network: no listener in grain serves the UI on a unix socket or anything else that yields an empty `RemoteAddr` |
| `testSourceNets` | Test-only (`export_test.go`) | Empty in production |
| Loopback | Not trusted (grain/task-4343) | Same as any address |

Served elsewhere, not by `pkg/ui`:

| Listener | Authenticated by |
|---|---|
| Git proxy (`pkg/gitproxy`) | Per-sandbox credential, scoped to the task's `reads`/`writes` |
| GitHub webhook relay (`pkg/ghwebhook`) | `POST /webhooks/github` by GitHub's HMAC; `GET /v1/events` by the relay HMAC; `GET /healthz` open, answers `ok` |
| Actions cache (`pkg/actionscache`) | A per-run token in the path |
| Hub / registry (`pkg/hub`) | Its own server and binary; `GET /healthz` and `GET /.well-known/jwks.json` open by design (public keys) |

## The access code with no identity

Before this change, `singleTenantTrusted` returned true for any request
carrying the code (bearer, header or session cookie) and `authMethod`
reported `trusted` with an empty user. Who relied on it:

- **The browser sign-in** (`POST /api/auth/access-code`): the cookie was
  `v1.<issued>.<mac>` and named nobody.
- **The host CLI** (`grain` on the daemon's machine, and every run's
  `grain mcpserver`) reads the code from `local-api-token` and sends it
  as a bearer token.
- **Dangerous roles:** only an access-code caller (no role in context)
  may assign or change a role holding a dangerous scope
  (`checkAccessRoleAssignment`, `checkAccessEntryChange`); task #3864
  extends that to verified admins.

What it could do there: every route (appendix, third column: no gated
route refuses the code; the two 403s are the task database routes, which
refuse the fixture's task, not the caller).

## What changed with this task

- **Named sessions.** The sign-in page takes a name -- an email on (or
  matching) the allowlist when the allowlist has entries -- and the
  session cookie carries it under the MAC (`v2.<issued>.<name>.<mac>`).
  A caller sending the code as a bearer token or header may name itself
  in `X-Grain-Access-Name`; the host CLI sends `$GRAIN_OWNER`, else the
  local account (`<user>@<host>`). Every request the code authenticates
  then records `access-code:<name>` (a human principal, source
  `access-code`) as its actor, so the audit, task authorship and comments
  say who it was. An unnamed legacy cookie keeps working as "operator
  (access code)".
- **Access-code mode** (`grain access-code mode on|recovery|off`, a file
  beside the code, read again within a second like the code itself):
  `recovery` limits the code to managing access and settings once a
  human admin with a verified identity is on the allowlist, and raises an
  Inbox notification for each use; `off` refuses it everywhere. Default
  `on`, today's behaviour. The mode is kept on the host, not in Settings,
  so a code holder over the network cannot switch the code back on.
- **Audit trail.** Every mutating request the code authenticates is
  recorded in the access history (config audit, surface `access-code`)
  with who (if named), source address, route and time, beside #3864's
  role-change records.

## Follow-ups filed

- The host CLI's actor is self-asserted (`X-Grain-Access-Name`), which
  is no weaker than today (the code holder is already the owner) but
  is not verified. Serving the API on a unix socket and mapping the
  peer's `SO_PEERCRED` uid to the configured owner, and `grain login`
  holding a personal token through IAP or a trusted identity proxy, are
  filed as their own task.
- A Settings -> Access card showing the access-code mode, recommending
  `recovery`, and showing the command that changes it, is filed as its
  own task (a UI change with screenshots).

No confirmed finding in the identity-less, code-less surface, so no
separate fix task is filed for it.

## Appendix: every route, probed

Status codes from the throwaway probe. "No identity, no code" is a
request from `203.0.113.9` with no credential at all; "Access code only"
is the same request carrying the access code's session. A 400/404 in
the third column is the handler rejecting the probe's `{}` body or
placeholder id -- it was reached.

| Route | No identity, no code | Access code only |
|---|---|---|
| `/` | 401 | 200 |
| `/staging/{name}/{path...}` | 401 | 404 |
| `DELETE /api/access/principals/{entry}` | 401 | 400 |
| `DELETE /api/agent-backends/{id}/codex-auth` | 401 | 404 |
| `DELETE /api/agent-backends/{id}/credential` | 401 | 400 |
| `DELETE /api/agent-keys/{framework}` | 401 | 404 |
| `DELETE /api/boards/{id}` | 401 | 404 |
| `DELETE /api/ci/cache` | 401 | 404 |
| `DELETE /api/ci/runs/{id}` | 401 | 404 |
| `DELETE /api/codex-auth` | 401 | 404 |
| `DELETE /api/console-sessions/{id}` | 401 | 404 |
| `DELETE /api/console/{id}` | 401 | 404 |
| `DELETE /api/deployment/agent-backends/{backendId}` | 401 | 404 |
| `DELETE /api/deployment/git-connections/{cred}` | 401 | 400 |
| `DELETE /api/external-agents/tokens/{id}` | 401 | 404 |
| `DELETE /api/gcp-keys/{name}` | 401 | 200 |
| `DELETE /api/github-tokens/{name}` | 401 | 404 |
| `DELETE /api/hostds/{name}` | 401 | 404 |
| `DELETE /api/pause` | 401 | 404 |
| `DELETE /api/pipeline-pause` | 401 | 200 |
| `DELETE /api/repos/{owner}/{name}` | 401 | 200 |
| `DELETE /api/repos/{owner}/{name}/release-secrets/{secret}` | 401 | 400 |
| `DELETE /api/repos/{owner}/{name}/releases/{release}` | 401 | 400 |
| `DELETE /api/schedules/{id}` | 401 | 204 |
| `DELETE /api/secrets/{secret}` | 401 | 404 |
| `DELETE /api/secrets/{secret}/{key}` | 401 | 404 |
| `DELETE /api/suites/{id}` | 401 | 204 |
| `DELETE /api/tasks/{id}/goal` | 401 | 200 |
| `DELETE /api/templates/{id}` | 401 | 204 |
| `DELETE /api/upgrade/scheduled` | 401 | 404 |
| `GET /api/access/principals` | 401 | 200 |
| `GET /api/actions-cache` | 401 | 200 |
| `GET /api/agent-backends/{id}/codex-auth` | 401 | 404 |
| `GET /api/agent-keys` | 401 | 200 |
| `GET /api/agent-models` | 401 | 200 |
| `GET /api/api-scope-sets` | 401 | 200 |
| `GET /api/api-scope-sets/usage` | 401 | 200 |
| `GET /api/auth/access-code` | 200 | 200 |
| `GET /api/auth/access-code/login.js` | 200 | 200 |
| `GET /api/auth/logout` | 302 | 302 |
| `GET /api/auth/status` | 401 | 200 |
| `GET /api/auth/user` | 401 | 200 |
| `GET /api/auth/whoami` | 200 | 200 |
| `GET /api/base-branch-gates` | 401 | 200 |
| `GET /api/boards` | 401 | 200 |
| `GET /api/capability-methods` | 401 | 200 |
| `GET /api/capability-policy` | 401 | 200 |
| `GET /api/capability-servers` | 401 | 200 |
| `GET /api/capability-servers/gchat-usage` | 401 | 400 |
| `GET /api/capability-servers/host-entries` | 401 | 200 |
| `GET /api/capability-servers/usage` | 401 | 200 |
| `GET /api/ci/cache` | 401 | 200 |
| `GET /api/ci/faults` | 401 | 200 |
| `GET /api/ci/runs` | 401 | 200 |
| `GET /api/ci/runs/{id}` | 401 | 404 |
| `GET /api/ci/runs/{id}/artifacts` | 401 | 200 |
| `GET /api/ci/runs/{id}/artifacts/{job}/{name}` | 401 | 404 |
| `GET /api/ci/runs/{id}/log` | 401 | 400 |
| `GET /api/ci/workflows` | 401 | 404 |
| `GET /api/config` | 401 | 200 |
| `GET /api/console` | 401 | 200 |
| `GET /api/console-sessions` | 401 | 200 |
| `GET /api/console/{id}/output` | 401 | 404 |
| `GET /api/database` | 401 | 200 |
| `GET /api/deployment` | 401 | 200 |
| `GET /api/deployment/audit-logs` | 401 | 200 |
| `GET /api/deployment/config-audit` | 401 | 200 |
| `GET /api/deployment/git-connections` | 401 | 200 |
| `GET /api/deployment/reachable-repos` | 401 | 200 |
| `GET /api/deployment/repos/{owner}/{name}/config` | 401 | 200 |
| `GET /api/deployment/secret-audit` | 401 | 200 |
| `GET /api/events` | 401 | 200 |
| `GET /api/external-agents/tokens` | 401 | 200 |
| `GET /api/fs/ci/{path...}` | 401 | 503 |
| `GET /api/fs/config/{path...}` | 401 | 503 |
| `GET /api/fs/deploy/{path...}` | 401 | 503 |
| `GET /api/fs/memory/{path...}` | 401 | 503 |
| `GET /api/fs/src/{path...}` | 401 | 503 |
| `GET /api/fs/task/{path...}` | 401 | 503 |
| `GET /api/gcp-keys` | 401 | 200 |
| `GET /api/gcp-keys/{name}` | 401 | 400 |
| `GET /api/git-ambient-sources` | 401 | 200 |
| `GET /api/github-tokens` | 401 | 200 |
| `GET /api/host/top` | 401 | 200 |
| `GET /api/hostd/connect` | 503 | 503 |
| `GET /api/hostd/install.sh` | 200 | 200 |
| `GET /api/hostds` | 401 | 200 |
| `GET /api/hostds/enrollment-codes` | 401 | 200 |
| `GET /api/hostds/pending` | 401 | 200 |
| `GET /api/hostds/{name}/install` | 401 | 404 |
| `GET /api/inbox` | 401 | 200 |
| `GET /api/local-script-approvals` | 401 | 200 |
| `GET /api/logs` | 401 | 200 |
| `GET /api/logs/{source}` | 401 | 404 |
| `GET /api/memory` | 401 | 200 |
| `GET /api/memory/documents` | 401 | 200 |
| `GET /api/memory/documents/{name}` | 401 | 404 |
| `GET /api/merge-batches` | 401 | 200 |
| `GET /api/metrics` | 401 | 200 |
| `GET /api/notifications` | 401 | 200 |
| `GET /api/package-cache` | 401 | 200 |
| `GET /api/pause` | 401 | 200 |
| `GET /api/persistent-sandboxes` | 401 | 200 |
| `GET /api/persistent-sandboxes/{name}/logs` | 401 | 503 |
| `GET /api/pipeline-pause` | 401 | 200 |
| `GET /api/pipeline/changes` | 401 | 200 |
| `GET /api/pull-requests/status` | 401 | 404 |
| `GET /api/refs/{ref}/summary` | 401 | 200 |
| `GET /api/registry` | 401 | 200 |
| `GET /api/registry/status` | 401 | 200 |
| `GET /api/release-approval-key` | 401 | 200 |
| `GET /api/release-notes` | 401 | 200 |
| `GET /api/release-notes/stats` | 401 | 200 |
| `GET /api/release-notes/versions` | 401 | 200 |
| `GET /api/release-notes/versions/{version}` | 401 | 404 |
| `GET /api/release-notes/{period}` | 401 | 400 |
| `GET /api/repos/{owner}/{name}/base-repair-bypasses-human-gates` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/branches` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/candidates/{id}/qualification` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/capabilities` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/ci-capabilities` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/ci-mode` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/compare` | 401 | 404 |
| `GET /api/repos/{owner}/{name}/declarations` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/github-branches` | 401 | 404 |
| `GET /api/repos/{owner}/{name}/ignore-github-ci` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/lifecycle` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/merge-on-green` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/prompt-extension` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/qualification-plan` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/release` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/release-creation` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/releases` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/releases/{release}` | 401 | 404 |
| `GET /api/repos/{owner}/{name}/releases/{release}/candidates` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/require-branch-up-to-date` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/settings` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/setup-command` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/unfixable-checks` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/watch-default-branch` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/work-ownership` | 401 | 200 |
| `GET /api/repos/{owner}/{name}/workflows` | 401 | 200 |
| `GET /api/restart-watch` | 401 | 200 |
| `GET /api/sandboxes` | 401 | 200 |
| `GET /api/schedules` | 401 | 200 |
| `GET /api/schedules/{id}/composition` | 401 | 200 |
| `GET /api/secrets` | 401 | 200 |
| `GET /api/settings` | 401 | 200 |
| `GET /api/settings/cli-run` | 401 | 200 |
| `GET /api/state-repo` | 401 | 200 |
| `GET /api/state-repo/file` | 401 | 501 |
| `GET /api/state-repo/files` | 401 | 501 |
| `GET /api/states` | 401 | 200 |
| `GET /api/suite-runs` | 401 | 200 |
| `GET /api/suite-runs/{id}` | 401 | 404 |
| `GET /api/suites` | 401 | 200 |
| `GET /api/suites/{id}/composition` | 401 | 200 |
| `GET /api/tasks` | 401 | 200 |
| `GET /api/tasks/states` | 401 | 200 |
| `GET /api/tasks/wait` | 401 | 400 |
| `GET /api/tasks/{id}` | 401 | 200 |
| `GET /api/tasks/{id}/attachments/{attachmentId}` | 401 | 400 |
| `GET /api/tasks/{id}/attempts/{number}/capability-audit` | 401 | 404 |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | 401 | 404 |
| `GET /api/tasks/{id}/messages` | 401 | 200 |
| `GET /api/tasks/{id}/prompt` | 401 | 200 |
| `GET /api/tasks/{id}/tree` | 401 | 200 |
| `GET /api/templates` | 401 | 200 |
| `GET /api/templates/{id}/composition` | 401 | 200 |
| `GET /api/unmatched-failures` | 401 | 200 |
| `GET /api/upgrade` | 401 | 200 |
| `GET /api/upgrade/release-notes` | 401 | 404 |
| `GET /api/upgrade/targets` | 401 | 404 |
| `GET /api/usage` | 401 | 200 |
| `GET /api/whoami` | 401 | 200 |
| `GET /cli/install.sh` | 200 | 200 |
| `GET /hostd/install` | 200 | 200 |
| `PATCH /api/access/principals/{entry}` | 401 | 400 |
| `PATCH /api/boards/{id}` | 401 | 404 |
| `PATCH /api/hostds/{name}` | 401 | 404 |
| `PATCH /api/schedules/{id}` | 401 | 200 |
| `PATCH /api/suites/{id}` | 401 | 400 |
| `PATCH /api/tasks/{id}` | 401 | 200 |
| `PATCH /api/templates/{id}` | 401 | 200 |
| `POST /api/access/principals` | 401 | 400 |
| `POST /api/actions-cache/flush` | 401 | 501 |
| `POST /api/agent-keys/{framework}/check` | 401 | 404 |
| `POST /api/api-scope-sets/delete` | 401 | 200 |
| `POST /api/auth/access-code` | 401 | 401 |
| `POST /api/auth/logout` | 200 | 200 |
| `POST /api/boards` | 401 | 400 |
| `POST /api/capability-grants/check` | 401 | 200 |
| `POST /api/capability-policy/preview` | 401 | 200 |
| `POST /api/capability-servers/bind-secret` | 401 | 400 |
| `POST /api/capability-servers/disable` | 401 | 400 |
| `POST /api/capability-servers/dismiss-notice` | 401 | 400 |
| `POST /api/capability-servers/enable` | 401 | 400 |
| `POST /api/capability-servers/figma-oauth-authorize` | 401 | 400 |
| `POST /api/capability-servers/figma-oauth-exchange` | 401 | 400 |
| `POST /api/capability-servers/figma-test` | 401 | 400 |
| `POST /api/capability-servers/gchat-test` | 401 | 400 |
| `POST /api/capability-servers/remove` | 401 | 400 |
| `POST /api/capability-servers/rotate-managed` | 401 | 400 |
| `POST /api/capability-servers/test` | 401 | 400 |
| `POST /api/capability-servers/test-script` | 401 | 400 |
| `POST /api/ci/runs` | 401 | 404 |
| `POST /api/console-sessions` | 401 | 400 |
| `POST /api/console-sessions/{id}/cancel` | 401 | 404 |
| `POST /api/console-sessions/{id}/restart` | 401 | 404 |
| `POST /api/console/{id}/input` | 401 | 404 |
| `POST /api/console/{id}/resize` | 401 | 404 |
| `POST /api/deployment/broadcast` | 401 | 400 |
| `POST /api/deployment/git-connections` | 401 | 400 |
| `POST /api/deployment/git-connections/check` | 401 | 400 |
| `POST /api/external-agents/tokens` | 401 | 400 |
| `POST /api/external-agents/tokens/revoke-all` | 401 | 200 |
| `POST /api/external-agents/tokens/{id}/revoke` | 401 | 404 |
| `POST /api/external-agents/tokens/{id}/rotate` | 401 | 404 |
| `POST /api/gcp-keys` | 401 | 400 |
| `POST /api/host/reboot` | 401 | 404 |
| `POST /api/host/restart` | 401 | 404 |
| `POST /api/host/shutdown` | 401 | 404 |
| `POST /api/hostd/join` | 400 | 400 |
| `POST /api/hostd/join/{id}/poll` | 400 | 400 |
| `POST /api/hostd/renew` | 400 | 400 |
| `POST /api/hostds` | 401 | 400 |
| `POST /api/hostds/delete-revoked` | 401 | 200 |
| `POST /api/hostds/enrollment-codes` | 401 | 400 |
| `POST /api/hostds/enrollment-codes/{name}/revoke` | 401 | 404 |
| `POST /api/hostds/enrollment-codes/{name}/rotate` | 401 | 404 |
| `POST /api/hostds/pending/{id}/accept` | 401 | 404 |
| `POST /api/hostds/pending/{id}/reject` | 401 | 404 |
| `POST /api/hostds/{name}/rename` | 401 | 400 |
| `POST /api/hostds/{name}/revoke` | 401 | 404 |
| `POST /api/hostds/{name}/rotate` | 401 | 404 |
| `POST /api/mcp` | 401 | 400 |
| `POST /api/notifications` | 401 | 400 |
| `POST /api/notifications/{id}/ack` | 401 | 404 |
| `POST /api/notifications/{id}/clear` | 401 | 404 |
| `POST /api/notifications/{id}/investigate` | 401 | 404 |
| `POST /api/package-cache/flush` | 401 | 501 |
| `POST /api/persistent-sandboxes/{name}/{action}` | 401 | 503 |
| `POST /api/pipeline-pause` | 401 | 200 |
| `POST /api/pull-requests/adopt` | 401 | 404 |
| `POST /api/pull-requests/review` | 401 | 404 |
| `POST /api/registry/heartbeat` | 401 | 200 |
| `POST /api/releases/{version}/rollback` | 401 | 400 |
| `POST /api/repos` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/base-branch-runs` | 401 | 404 |
| `POST /api/repos/{owner}/{name}/branches` | 401 | 201 |
| `POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve` | 401 | 404 |
| `POST /api/repos/{owner}/{name}/import-issues` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/lifecycle/refresh` | 401 | 200 |
| `POST /api/repos/{owner}/{name}/pending-releases/{version}/approve` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/pending-releases/{version}/reject` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/release-runs` | 401 | 404 |
| `POST /api/repos/{owner}/{name}/releases` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/releases/{release}/candidates` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote` | 401 | 400 |
| `POST /api/repos/{owner}/{name}/releases/{release}/merge` | 401 | 400 |
| `POST /api/sandbox-probe` | 401 | 404 |
| `POST /api/schedules` | 401 | 201 |
| `POST /api/schedules/composition` | 401 | 200 |
| `POST /api/schedules/{id}/run` | 401 | 201 |
| `POST /api/settings` | 401 | 200 |
| `POST /api/settings/plan` | 401 | 200 |
| `POST /api/state-repo/import-key` | 401 | 501 |
| `POST /api/state-repo/pull-requests` | 401 | 400 |
| `POST /api/state-repo/remote` | 401 | 501 |
| `POST /api/state-repo/reset-notice` | 401 | 501 |
| `POST /api/state-repo/secrets-key` | 401 | 501 |
| `POST /api/state-repo/secrets-repo` | 401 | 501 |
| `POST /api/state-repo/sync` | 401 | 501 |
| `POST /api/suite-runs` | 401 | 400 |
| `POST /api/suites` | 401 | 400 |
| `POST /api/suites/composition` | 401 | 200 |
| `POST /api/tasks` | 401 | 400 |
| `POST /api/tasks/from-template` | 401 | 400 |
| `POST /api/tasks/reorder` | 401 | 400 |
| `POST /api/tasks/{id}/access-request/check` | 401 | 400 |
| `POST /api/tasks/{id}/access-request/deny` | 401 | 400 |
| `POST /api/tasks/{id}/access-request/grant` | 401 | 400 |
| `POST /api/tasks/{id}/activity` | 401 | 400 |
| `POST /api/tasks/{id}/agent-messages` | 401 | 400 |
| `POST /api/tasks/{id}/approve` | 401 | 200 |
| `POST /api/tasks/{id}/capabilities` | 401 | 400 |
| `POST /api/tasks/{id}/change-checks` | 401 | 400 |
| `POST /api/tasks/{id}/change-mail` | 401 | 400 |
| `POST /api/tasks/{id}/change-publish` | 401 | 400 |
| `POST /api/tasks/{id}/change-review` | 401 | 400 |
| `POST /api/tasks/{id}/change-sync` | 401 | 500 |
| `POST /api/tasks/{id}/close` | 401 | 200 |
| `POST /api/tasks/{id}/comments` | 401 | 400 |
| `POST /api/tasks/{id}/config-change` | 401 | 400 |
| `POST /api/tasks/{id}/config-change/approve` | 401 | 400 |
| `POST /api/tasks/{id}/config-change/reject` | 401 | 400 |
| `POST /api/tasks/{id}/db/execute` | 401 | 403 |
| `POST /api/tasks/{id}/db/query` | 401 | 403 |
| `POST /api/tasks/{id}/defer` | 401 | 200 |
| `POST /api/tasks/{id}/delivered` | 401 | 400 |
| `POST /api/tasks/{id}/depends-on` | 401 | 400 |
| `POST /api/tasks/{id}/force-merge` | 401 | 400 |
| `POST /api/tasks/{id}/human-review` | 401 | 400 |
| `POST /api/tasks/{id}/human-review/release` | 401 | 400 |
| `POST /api/tasks/{id}/local-script-approvals` | 401 | 400 |
| `POST /api/tasks/{id}/local-script-approvals/{approval}/answer` | 401 | 404 |
| `POST /api/tasks/{id}/local-script-approvals/{approval}/expire` | 401 | 404 |
| `POST /api/tasks/{id}/local-script-approvals/{approval}/wait` | 401 | 404 |
| `POST /api/tasks/{id}/messages` | 401 | 400 |
| `POST /api/tasks/{id}/notes` | 401 | 400 |
| `POST /api/tasks/{id}/notify` | 401 | 400 |
| `POST /api/tasks/{id}/park-until` | 401 | 400 |
| `POST /api/tasks/{id}/pause` | 401 | 200 |
| `POST /api/tasks/{id}/pull-request` | 401 | 404 |
| `POST /api/tasks/{id}/reopen` | 401 | 200 |
| `POST /api/tasks/{id}/reply` | 401 | 400 |
| `POST /api/tasks/{id}/resume` | 401 | 200 |
| `POST /api/tasks/{id}/resume-later` | 401 | 200 |
| `POST /api/tasks/{id}/retry` | 401 | 200 |
| `POST /api/tasks/{id}/retry-merge` | 401 | 400 |
| `POST /api/tasks/{id}/run-now` | 401 | 200 |
| `POST /api/tasks/{id}/sandbox/recreate` | 401 | 404 |
| `POST /api/tasks/{id}/staged-attachments` | 401 | 400 |
| `POST /api/tasks/{id}/stop` | 401 | 200 |
| `POST /api/tasks/{id}/submit` | 401 | 200 |
| `POST /api/tasks/{id}/undefer` | 401 | 200 |
| `POST /api/tasks/{id}/wait-credit` | 401 | 404 |
| `POST /api/tasks/{id}/wait-on` | 401 | 400 |
| `POST /api/tasks/{id}/withdraw-approval` | 401 | 200 |
| `POST /api/templates` | 401 | 201 |
| `POST /api/tests/quarantine` | 401 | 400 |
| `POST /api/upgrade` | 401 | 404 |
| `POST /mcp` | 401 | 400 |
| `PUT /api/agent-backends/{id}/codex-auth` | 401 | 404 |
| `PUT /api/agent-backends/{id}/credential` | 401 | 400 |
| `PUT /api/agent-keys/{framework}` | 401 | 404 |
| `PUT /api/api-scope-sets` | 401 | 400 |
| `PUT /api/capability-policy` | 401 | 200 |
| `PUT /api/capability-servers` | 401 | 400 |
| `PUT /api/codex-auth` | 401 | 400 |
| `PUT /api/console-sessions/{id}` | 401 | 404 |
| `PUT /api/deployment` | 401 | 200 |
| `PUT /api/deployment/agent-backends/{backendId}` | 401 | 400 |
| `PUT /api/deployment/repos/{owner}/{name}/config` | 401 | 200 |
| `PUT /api/deployment/repos/{owner}/{name}/git-connection` | 401 | 200 |
| `PUT /api/gcp-keys/{name}` | 401 | 400 |
| `PUT /api/github-tokens/{name}` | 401 | 400 |
| `PUT /api/memory` | 401 | 200 |
| `PUT /api/memory/documents` | 401 | 400 |
| `PUT /api/release-notes` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/capabilities` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/ci-capabilities` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/ci-mode` | 401 | 400 |
| `PUT /api/repos/{owner}/{name}/declarations` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/ignore-github-ci` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/merge-on-green` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/prompt-extension` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/qualification-plan` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/release` | 401 | 400 |
| `PUT /api/repos/{owner}/{name}/release-secrets/{secret}` | 401 | 400 |
| `PUT /api/repos/{owner}/{name}/require-branch-up-to-date` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/settings` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/setup-command` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/unfixable-checks` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/watch-default-branch` | 401 | 200 |
| `PUT /api/repos/{owner}/{name}/work-ownership` | 401 | 400 |
| `PUT /api/repos/{owner}/{name}/workflows` | 401 | 200 |
| `PUT /api/secrets/{secret}/{key}` | 401 | 400 |
| `PUT /api/settings` | 401 | 200 |
| `PUT /api/settings/cli-run` | 401 | 200 |
| `PUT /api/tasks/{id}/goal` | 401 | 400 |
| `PUT /api/tasks/{id}/hostd` | 401 | 400 |
| `PUT /api/tasks/{id}/secret` | 401 | 400 |
| `PUT /api/tasks/{id}/session-title` | 401 | 400 |
| `PUT /api/upgrade/source` | 401 | 404 |
| `PUT /api/usage/settings` | 401 | 200 |
