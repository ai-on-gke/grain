# Test run 2026-10-12: team secret audit, down-scoped tokens, cross-team approval

Plan: `docs/test-plans/2026-10-11.md` §4. Commit under test: `38ee4bcf5` (cherry-pick of `main`
`4814caab7`), on `candidate` at `b2ce41d09`. Run by task 3140, filed by the nightly test-plan task 2955.

## Environment

- Sandbox: Debian, Go toolchain from `go.mod`, Node + `npm ci` in `ui/`, `npm run build` into
  `pkg/ui/static`, Playwright Chromium (headless shell 151). The browser download needs
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`, because the package-cache proxy
  re-signs TLS. Without it, `npx playwright install` fails with `SELF_SIGNED_CERT_IN_CHAIN`.
- Browser walkthrough: the real `grain demo -addr 127.0.0.1:0 -open=false`, started per worker by
  `ui/e2e/fixtures.js`, on its embedded SQLite store. No grain API or React code was mocked.
- API, dispatch and token walkthrough: a real `ui.Server` over a real embedded SQLite store and a real
  `secrets.Store` (`tests/e2e/secret_audit_cross_team_walkthrough_test.go`). Requests carry signed
  session tokens (`ui.CreateSessionToken`), as a password sign-in does. System-admin requests send
  `X-Grain-Admin-Mode: on`.
- Users: `alice` (team A admin), `amy` (team A member), `bob` (team B admin), `bea` (team B member),
  plus a system admin.

## Stubs

| Boundary | Stub | Why |
| --- | --- | --- |
| Agent run | `model.ResolveGrants` called with the `CapabilityContext` the orchestrator builds for a run (`orchestrator/run.go`), using the real `geminikey` provider. Its `Resolve` reads its standing credential through `cc.Credentials`, as a run does. | `grain demo` has no orchestrator, no sandbox and no secrets store, so nothing in it can resolve a secret. |
| Audit rows in the browser | A one-off (uncommitted) program serving a real `ui.Server` with a real secrets store and the frontend built above, seeded by the stubbed run. Driven by Playwright as `alice`, `bob` and `amy`. | Same reason. The demo can only show the empty state. |

Nothing else was stubbed: no GitHub, no live session.

## Steps and results

| # | Step | Result |
| --- | --- | --- |
| 1 | Two teams. Team B's admin turns on **Require authorization for cross-team dependencies** in the Audit / Security tab. | **Failed on candidate** (F1): the PUT answered 200 and stored nothing, and the switch was off again after a reload. **Pass after fix.** A team-B member gets 403. |
| 2a | Audit tab empty state | Pass: "No secret resolution audit events recorded yet for this team." for team A and team B in the demo. `GET …/secret-audit` returns `[]`. |
| 2b | Team-A secret (`alpha-minter/key.json`, restricted to team-a) resolved by two runs of team-A tasks | **Failed on candidate** (F2): rows appeared with an empty task and run. **Pass after fix:** two rows, `task-two/run-2` then `task-one/run-1` (newest first), each with secret name, key and time. |
| 2c | Secret value in the DOM or the API response | Pass: absent from the page HTML, the API body, every refused response, and the minted token string. |
| 2d | Team B's audit | Pass: `[]`, so none of team A's rows appear there. |
| 3 | `GET /api/teams/team-a/secret-audit` as each user | alice **200**, amy (team-A member) **403**, bob (team-B admin) **403**, bea (team-B member) **403**, system admin with Admin mode **200**. Browser: bob opening team A's settings sees "You are not a member of team team-a…" and no rows. |
| 4a | `daemon.IssueScopedCapabilityToken`, TTL 1s | Pass: accepted by `secrets.Store.ValidateScopedToken` when fresh, and refused ("token expired at …") after 1.1s. |
| 4b | TTL 2h | Pass: clamped to exactly 1h (`ExpiresAt − IssuedAt == MaxTokenTTL`). With no TTL it is 15m. Team B cannot mint from team A's restricted secret. See F6 for what this does not show. |
| 5a | A team-A task depending on a team-B task, with the upstream closed | Pass: `dispatch.Cycle` skips it ("pending cross-team approval from team team-b"). **On candidate the UI showed it as a plain queued task with no reason** (F4). After the fix, the task pane shows "Held: this task depends on N, which belongs to team team-b, …" with an **Approve for team team-b** button. |
| 5b | Unauthorized approve | **Failed on candidate** (F3): alice posting `{"targetTaskId":…, "targetTeam":"team-a"}` got **200 `{"status":"approved"}`** and the hold was released. After the fix, alice (button and API, with or without `targetTeam`), amy and bea all get **403**, and no approval is recorded. Approving a task that is not a dependency gets **400**. |
| 5c | Authorized approve (bob) | Pass: 200. The hold is gone from the task pane, and the next `dispatch.Cycle` dispatches the task. |
| 5d | Fresh task: bob approves, alice withdraws approval | Pass as written: `withdraw-approval` puts the task back to `proposed`, and dispatch does not start it. But see F7: the cross-team approval itself survives. |

## Findings

F1–F5 are fixed on this branch. F6 and F7 are filed as follow-ups. F8 is minor and only noted.

- **F1. The cross-team policy could not be set.** `UpdateTeamRequest` and `ui.Team` had no
  `requireCrossTeamApproval`, so `PUT /api/teams/{id}` dropped it. The tab's switch reverted on reload,
  and no API or UI path could turn the policy on. Repro: `PUT /api/teams/team-b
  {"requireCrossTeamApproval":true}` as team B's admin returns 200, then `GET /api/teams/team-b` has
  no `requireCrossTeamApproval`.
  **Fix:** the field is on both types and applied in `Client.UpdateTeam`, behind the existing team-admin check.
- **F2. Run resolutions were audited with no task or run.** `model.ResolveGrants`,
  `MaterializeScarceGrants`, `CompleteMaterializeGrants` and `PromptSections` tagged the context with the
  team only. Repro: step 2b on candidate gives rows with `taskId:""`, `runId:""`.
  **Fix:** `credentialContext` tags team, task and run. A task-tagged row with no team is recorded
  under `default`, so the default team's tab finds it.
- **F3. Any team's admin could approve on another team's behalf (security).** `approve-cross-team`
  authorized the caller against the `targetTeam` named in the request body. Repro on unmodified
  candidate (team-b policy on, a1@team-a depends on b1@team-b): `POST /api/tasks/a1/approve-cross-team
  {"targetTaskId":"b1","targetTeam":"team-a"}` as team-a's admin returns `200 {"status":"approved"}`,
  and `HasPendingCrossTeamApproval` becomes false.
  **Fix:** the approving team is the dependency task's own team. A `targetTeam` in the body must agree
  with it. The target must be one of the task's `depends_on` links. An empty `targetTaskId` approves the
  dependency currently holding the task.
- **F4. The hold was invisible.** Dispatch skipped the task without changing its state, and nothing on
  the task said why. `StatePendingCrossTeamApproval` is declared but never produced.
  **Fix:** `TaskDetail.crossTeamApproval` (`targetTaskId`, `targetTeam`, `reason`) for open tasks, and a
  task-pane banner with an approve button (`ui/src/components/detail/CrossTeamApproval.jsx`).
- **F5. A real daemon never audited a run's resolutions.** `daemon.Run` opens the secrets store that
  runs resolve through (`orchestrator.Config.Credentials`). The UI server opens a second instance
  (`ui_server.go`), and only that one got the audit logger, from `ui.NewServer`. Found by reading the
  wiring. The walkthrough builds its own server, so it does not exercise `daemon.Run`.
  **Fix:** `daemon.Run` attaches the audit logger to its own store as well. No automated test covers
  that line, because `daemon.Run` starts a whole deployment.
- **F6. Down-scoped tokens have no running consumer, and validation trusts the holder** (follow-up filed).
  - Nothing in the daemon calls `IssueScopedCapabilityToken` or `ValidateScopedToken` outside tests.
    "The daemon's real issuance path" is that exported function, and nothing issues tokens at runtime.
  - `ValidateScopedToken` checks the `ExpiresAt` of an `EphemeralToken` struct the caller holds.
    The token string carries no verifiable expiry or scope, so a consumer handed the string cannot
    check it, and a holder can extend a struct's `ExpiresAt` at will.
  - `MaxAge` can only shorten a token.

  The TTL rules themselves (default 15m, clamp 1h) behave as specified.

  **Resolved (task 3194):** removed rather than made real, as agreed with a human. Nothing depended on
  them. Steps 4a and 4b no longer exist in the walkthrough. Step 4 now checks only that team B cannot
  resolve team A's secret.
- **F7. Cross-team approvals cannot be revoked** (follow-up filed). `withdraw-approval` withdraws the
  task's own approval, but leaves its `cross_team_dependency_approval` row in place. Re-approving the
  task therefore dispatches it without team B being asked again. Team B also has no endpoint to revoke
  an approval it gave.
- **F8. Minor, not filed:**
  - The audit 403 says "does not have permission to manipulate team" for a read.
  - `principal` is always empty for run resolutions (shown as "-").
  - Timestamps render in the browser's time zone rather than the team's.
  - The endpoint returns the newest 100 rows with no paging.

### Overlap with candidate after the walkthrough

While this branch was open, `candidate` independently landed fixes for F2 and F3: `99dc67d1e`
("approve cross-team dependencies only as the target task's team"), and `secretsContext` with
default-team attribution from `3b66c0c9b`/#3484. Merging `candidate` in, this branch took
candidate's versions of those. The walkthrough tests here still pass against them. The one behaviour
change is that `approve-cross-team` now requires `targetTaskId`, and the task pane's button sends it.
F1, F4 and F5 remain this branch's own. Candidate's `3b66c0c9b` also moved the Audit / Security tab onto `GET /api/teams/{id}/audit-logs`
(secret resolutions plus git credential accesses). Its empty state now reads "No credential audit
events recorded yet for this team." The spec follows that, and checks the same 200/403/403 matrix on
both endpoints.

## Durable tests added

- `tests/e2e/secret_audit_cross_team_walkthrough_test.go`: all five steps over HTTP, plus dispatch and
  token issuance. It covers F1–F4 and the 403/200 matrix.
- `tests/security/secret_audit_and_scoping_test.go`: `TestCrossTeamApprovalOwnerComesFromTheDependency`,
  the F3 regression.
- `pkg/model/secret_audit_test.go`: default-team attribution of task resolutions.
- `ui/e2e/secret-audit-cross-team-walkthrough.spec.js`, against the real demo in Chromium: the tab's
  empty state, the policy switch persisting, the audit status matrix, the hold banner and its reason,
  refused and accepted approvals, and withdraw. 5/5 passing locally.
- `pkg/ui/teams_test.go`: `TestTeamSecretAuditAndCrossTeamApprovalClient` approved a link to a task
  that did not exist. Its fixture now creates the dependency. No expectation was lowered.

## CI verdict

Grain CI on `grain/eu/task-3140` (bwsalmon/grain#3500):

1. `d2d8da8`, run `ae8f112a2f060fa1`: `ui e2e` was red in two specs this branch did not touch,
   `system-settings-scoped-access-walkthrough` and `task_detail_workflows`. The new spec shared a
   worker's demo with them. It registered `alice` before them and filed tasks that shifted the task
   ids they expect. Fixed by giving the spec a `demoArgs` of its own and prefixed usernames, and
   rerun locally with both specs on one worker: 11/11.
2. `ab29d3c`, run `9aa93c7d8f29e84b`: `ui e2e` green. `go` red in `TestRouteAuthorization`:
   `approve-cross-team` with no `targetTaskId` answered a plain member 400 before authorizing. Fixed
   by authorizing against the task's own team admin before that 400 (and before the 404 for a missing
   target). The route's expected level and the baseline are unchanged.
3. `333c580`: all nine jobs green.
4. The merge queue then reported conflicts with `candidate` (the overlap above). After merging
   `candidate` in, the final full-suite verdict is on the pull request and in the task's closing note.
