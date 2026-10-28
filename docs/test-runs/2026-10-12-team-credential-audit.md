# Test run 2026-10-12: team credential audit log and cross-team approval

Plan: `docs/test-plans/2026-10-12.md` §1. Run by task 3286, filed by task 3209.

## Environment

- Base: `candidate` at `c8a3f5e0c`. The commits under test are all on it
  (`git branch -r --contains` lists `origin/candidate` for `3b66c0c9b`, `2a85c7ad4` and `99dc67d1e`).
- Sandbox: Debian (kernel 6.1.0-53-amd64, x86_64), Go 1.26.6, Node 20.19.0, npm 10.8.2, git 2.39.5,
  Playwright Chromium headless shell 151.0.7922.34. The browser download needs
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.
- Frontend: `cd ui && npm ci && npm run build` into `pkg/ui/static`. The Go server embeds it.
- Browser walkthrough: the real `grain demo -addr 127.0.0.1:0 -open=false -default-target-repo
  acme/credential-audit -github-sim`, started by `ui/e2e/fixtures.js` on its embedded SQLite store.
  Requests carry no `X-Grain-Admin-Mode` unless the test turns Admin mode on (`extraHTTPHeaders: {}`).
- Git proxy walkthrough: `tests/e2e/team_credential_audit_walkthrough_test.go`. A real `git` clones and pushes
  through `gitproxy.BuildProxy` (the daemon's own constructor, with its `TeamAuditLog` wiring) over a
  real embedded SQLite `model.Store`. A real `ui.Server` over the same store answers the API.
  `grain demo` runs no git proxy, so this step cannot be walked on the demo (F2).
- Users: `alice`/`ca-alice` (team A admin), `amy`/`ca-amy` (A member), `avery`/`ca-avery` (A viewer),
  `bob`/`ca-bob` (team B admin), and a system admin (`root-admin` in Go, the demo's own `sysadmin` in the browser).

## Stubs

| Boundary | Stub | Why |
| --- | --- | --- |
| GitHub (git smart-HTTP) | A bare repository per team repo (`acme/alpha`, `acme/beta`) served by `git http-backend` over `httptest`, as `pkg/gitproxy/live_test.go` does. The proxy forwards to it over loopback HTTP (`ForwardTLS: false`, which `ValidateForwardTarget` allows only for loopback). | There is no GitHub here. Everything from the `git` client through the proxy, its authorization, credential selection and audit, to the store and the API is real. |
| GitHub (REST listing) | `grain demo -github-sim` (`githubsim.Sim`) | The demo's own stand-in. |
| Agent run | A task row and a started run (`store.StartRun`, sandbox `sandbox-a`) plus that sandbox's entry in `secrets/sandbox-tokens.json`, then `git` run by the test. | No sandbox or model provider. The proxy attributes the request from the live run on the sandbox, which is exactly what this creates. |
| Audit rows in the browser | Once, by hand, not committed: the Go walkthrough held open its `ui.Server` (embedded built UI) on a loopback port, and a Playwright script opened Settings → Teams → team-a → Audit / Security. | The demo has no proxy, so it can only show the empty state. Registration is refused on that bare server, so every request ran as its local actor (the system admin). It proves rendering and redaction, not the role matrix. |

Nothing else was stubbed. No grain API or React code was mocked.

## Steps and results

The 10-11 §4 walkthrough (`docs/test-runs/2026-10-12-secret-audit-cross-team.md`) already proved the secret
half of the trail and the approval gate. This run did not repeat it. It re-ran that run's tests on this
base, and added only what the 10-12 plan asks beyond them.

| # | Step | Result |
| --- | --- | --- |
| 1 | Teams A and B, each with its own git credential (`alpha-bot`, `beta-bot`) and repo (`acme/alpha`, `acme/beta`). In the browser, team A has an admin, a member and a viewer (`PUT /api/teams/team-a` `members` with roles). | Pass. |
| 2 | Team A task `a1`, run `a1-run-1` on `sandbox-a`: `git clone` then `git push origin HEAD:grain/task-a1` through the proxy. | Pass. `GET /api/teams/team-a/audit-logs` has exactly one push: kind `git`, task `a1`, run `a1-run-1`, sandbox `sandbox-a`, resource `acme/alpha`, action `git-receive-pack`, credential `alpha-bot`, outcome `forwarded: 200`. The clone's `info/refs` and `git-upload-pack` rows are charged to the same task and run. Team B's log is `[]`. The action is git's service name, not `push` as the plan wrote (F3). |
| 3a | A second push with the run's token in the remote URL (`http://x-access-token:<token>@proxy/acme/alpha.git`). | Pass. Recorded like the first. git sends userinfo as Basic auth, so it reaches no recorded field. The token is absent from the `credential_access_audit` rows, the API (as A's admin, and as a system admin in Admin mode) and the page HTML. |
| 3b | A push with a GitHub-shaped token (`ghp_…`) as the URL password. | Refused (unknown sandbox token) and not recorded for any team. Nothing to leak. |
| 3c | A push with that token as the repository name (`/acme/ghp_….git`), authenticated as the run. | Pass. Refused `denied: not in scope`. The team row's resource is `acme/[REDACTED]` in the table and the API, and in the rendered panel. The deployment's own `audit.log` keeps the raw path (F4). |
| 3d | A push to `main`. | Refused by the proxy and recorded as `git-receive-pack`, `denied: ref not in scope`, against `a1`/`a1-run-1`. |
| 3e | Panel over these rows (stub above). | Pass. Nine rows, newest first, with resource, action, credential, outcome, task and run. `acme/[REDACTED]` is shown. Neither token appears in the page HTML or the API body. |
| 4 | API, `GET /api/teams/team-a/audit-logs` | A admin **200**, system admin with Admin mode **200**, system admin without **403**, A member **403**, A viewer **403**, B admin **403**. The same in Go and in the browser against the demo. No refusal body carries a row. |
| 4 | Panel, as each role | A admin and system admin in Admin mode: the log (empty state on the demo). B admin and system admin without Admin mode: "You are not a member of team team-a, so its settings are not yours to see…", with no Audit tab. **A member and A viewer: failed on candidate (F1).** The tab showed the 403 sentence twice, then "No credential audit events recorded yet for this team.". **Pass after fix.** |
| 5 | Cross-team approval | Already proved by 10-11 §4 (held with reason, A's admin/member and B's member refused, A's admin naming team A refused, a non-dependency refused with a 4xx, B's admin approves and `dispatch.Cycle` dispatches). Both of its tests pass on this base. Added the one case the 10-12 plan names and it did not: **A's admin naming team B** → **403**, nothing recorded. Pass. |

## Findings

- **F1. A refused audit log read as an empty one (fixed).** For a team member or viewer (who can open
  team settings), the Audit / Security tab showed the server's `user "ca-amy" does not have permission to
  manipulate team "team-a"` inline and again as a toast. Under it was "No credential audit events
  recorded yet for this team.".
  Repro on unmodified candidate: `cd ui && npm run build && npx playwright test
  e2e/team-credential-audit-walkthrough.spec.js`. The `ca-amy is told the audit log is for admins` test
  fails on `team-audit-error`.
  **Fix:** `TeamAuditPanel.jsx` shows one Alert in place of the table on any load error, and no
  empty state. For a 403 it reads `Only admins of team "team-a" and system admins in Admin mode can read its
  credential audit log.` It no longer raises a toast for the load as well.
  Regression: the spec's member and viewer tests.
- **F2. `grain demo -github-sim` runs no git proxy.** The plan's steps 2–3 ("push through the demo git
  proxy") cannot be done on the demo. `-github-sim` wires only a REST `GitHubBranches` client
  (`pkg/demo/demo_githubsim.go`), and `BuildProxy` is never called. Covered here by the Go walkthrough
  over the daemon's own constructor. A follow-up proposes serving the proxy in the demo, so a
  browser walkthrough can show real rows. *Resolved by task 3393:* `grain demo -github-sim -git-proxy`
  serves the real proxy over the simulated repo, `POST /api/e2e/git-proxy/runs` starts a task's run and
  mints its sandbox token, and `ui/e2e/team-credential-audit-push.spec.js` pushes and reads the rows.
- **F3. Actions are git service names.** Rows read `info/refs`, `git-upload-pack` and `git-receive-pack`. An
  `info/refs` row does not say whether it was a fetch's or a push's advertisement, so a team admin
  cannot tell "cloned" from "pushed" for refused requests at that stage. Not a bug in the feature as
  written (the commit records `entry.Action`). Noted, not filed.
- **F4. The deployment `audit.log` is not redacted.** The token-shaped repository name in step 3c is
  written verbatim to `state/git-proxy/audit.log`, which system admins read through `/api/logs`. The
  team copy is redacted. This is outside what `3b66c0c9b` promises, and the input is pathological. Noted.
- **F5. Redaction is by shape.** `RedactSecretMaterial` catches known token formats, bearer/basic headers,
  URL userinfo and private keys. An opaque token in a repository path (the sandbox token itself, say)
  would be stored as-is. Noted for whoever extends the patterns, not filed.
- **F6. Minor, not filed.** The Audit / Security tab still shows the cross-team policy switch, enabled,
  to members and viewers. A change is refused server-side (10-11 §4). Timestamps render in the browser's
  zone (as 10-11 F8).

## Durable tests added

- `tests/e2e/team_credential_audit_walkthrough_test.go`: steps 1–4 over a real git proxy, store and UI
  server, with the forge stubbed.
- `tests/e2e/secret_audit_cross_team_walkthrough_test.go`: one more refused approval (A's admin naming team B).
- `ui/e2e/team-credential-audit-walkthrough.spec.js`: step 4 in Chromium against the real demo. 9/9
  locally, together with `secret-audit-cross-team-walkthrough.spec.js` (13/13 for both files).
  The member and viewer cases fail without F1's fix.
- `npx vitest run`: 3264/3264.

## CI verdict

Recorded in the task's closing note and on the pull request.
