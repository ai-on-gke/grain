# Test run — 2026-11-07 §3: RBAC roles, per-tool MCP checks, `delegate:` entries

Plan: [`docs/test-plans/2026-11-07.md` §3](../test-plans/2026-11-07.md). Task 3487, run 2026-11-08 in a grain sandbox against `main` at `46ab9012`. Attempt 2 ran items 1–3 and fixed F2. Attempt 3 ran item 4 (browser), fixed F6–F8 and re-ran items 1–3 on the final build (91/91 again).

## Environment

- `grain` was built from this branch (`go build ./cmd/grain`). It runs two real `grain daemon`s (`-host-sandboxes`, `-sandbox-port-base=0`, `-ci=false`, SQLite):
  - **main** on `127.0.0.1:14320`.
  - **lookup-down** on `127.0.0.1:14321`, whose `-ui-cloud-identity-endpoint` points at a closed port so every group lookup fails.
- Both daemons run behind two loopback verifiers:
  - `tests/iap/iapsim.go`: an IAP assertion signer, JWKS, its proxy on `:18490`, and a Cloud Identity stand-in with group `devs@example.com` = alice, dana and erin.
  - `docs/test-runs/2026-11-07-rbac-roles/regsim.go` (new, `//go:build ignore`): an ES256 grain-registry assertion signer with an optional `act` claim, used for `delegate:` sessions.
- Both verifiers serve plain `http://127.0.0.1` (loopback), which `pkg/webauth` accepts. No real IAP, Cloud Run or GitHub is reached.
- Browser (item 4): [`browser-walkthrough.mjs`](2026-11-07-rbac-roles/browser-walkthrough.mjs) drives Playwright's Chromium (`ui/node_modules`, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). It signs in at the IAP stand-in's `/_iapsim/login`, so every request carries a freshly signed IAP assertion for that person, and walks the daemon `walkthrough.sh` leaves up with `HOLD=1`. Two boards were added first through the access code: `Platform Board` (list) and `Release Kanban` (kanban).
- Script: [`walkthrough.sh`](2026-11-07-rbac-roles/walkthrough.sh). Its full table is in [`results.md`](2026-11-07-rbac-roles/results.md): **91/91 PASS** after the fix below. Before the fix, the `gma_` downgrade checks failed (F2).
- Principals:

  | Principal | Role |
  |---|---|
  | `user:alice` | `grain/viewer` |
  | `group:devs` | `grain/contributor` |
  | `user:vic` | `grain/viewer` |
  | `serviceAccount:ci-bot` | `grain/viewer` (with a reason) |
  | `delegate:console` | `grain/delegated`, label `release-console` |
  | `user:olivia` | `grain/operator`, then downgraded to `grain/viewer`, then removed |

## Findings

| # | Finding | Outcome |
|---|---|---|
| **F2** | **An owned `gma_` token outlived its owner's role downgrade.** A token issued with `scopeSet: grain/operator` for `olivia@` kept every scope after `grain access role user:olivia@example.com grain/viewer`. It still listed 314 tools on `/mcp` and still filed tasks through `POST /api/tasks`, `/mcp` `api_create_task` and `/api/mcp` `grain_invoke`. `PUT /api/secrets/...` passed the scope check and stopped only at "give a reason". Token grants are resolved from the token's own fields at every call site (`ResolveExternalAgentGrants`), and nothing intersected them with the owner's current allowlist role. | **Fixed.** `authenticateExternalAgent` now calls `externalTokenOwnerCap` (`pkg/ui/access_roles.go`). When the token's owner matches allowlist entries, the owner's role is resolved per request. The token is refused with a 403 ("… is wider than its owner's current role …; re-issue it within grain/viewer") when it is wider. If a group lookup fails and that group's role could veto scopes, the token is also refused (fail closed). A token re-issued within the owner's role works. Regression test: `TestExternalTokenCannotExceedItsOwnersCurrentRole`. Live checks: 3.11–3.14c. |
| **F6** | **The Inbox's answers were not role-gated (#822 gap).** As `vic@` (`grain/viewer`) and `dana@` (`grain/contributor`, via `group:devs`), `/inbox` offered every proposal's **Approve**, **Defer** and **Decline**, plus **Approve all (5)**, all enabled. The same was true of Reply, Acknowledge and Investigate. Clicking Approve sent `POST /api/tasks/2/approve`, which got a 403 and showed the toast "vic@example.com (role grain/viewer) holds no scope tasks:admin …". The contributor got the same. No task changed state: 5 proposals before and after ([repro](2026-11-07-rbac-roles/inbox-approve-repro.txt)). | **Fixed.** `InboxPage.jsx` wraps every row answer, bulk answer, Reply and notification action in `AccessGate`. The scope comes from the route each one posts to: `inboxActionScope` mirrors `roleScopeOf`. `/approve` needs `tasks:admin`; `/run-now`, `dispatch:write`; `/retry-merge`, `merge:write`; force merge, `merge:force`; everything else, `tasks:write`. After the fix the viewer has no enabled write control on any page. The contributor keeps Defer and Decline but not Approve or Approve all. Tests: `InboxPage.test.jsx` and `ui/e2e/role-gated-actions.spec.js`. |
| **F7** | **New task was offered to a viewer everywhere.** The rail's **+ New task** split button and its create menu, the phone bar's **+** (all 12 pages, both themes) and the form's **Create task** were all enabled for `grain/viewer`. Filing then ended in a 403. | **Fixed.** `Sidebar.jsx`, `PhoneNav.jsx` and `NewTaskOverlay.jsx` gate filing on `tasks:write`, with the tooltip. The e2e spec checks that the rail button stays in its split-button row (same y; left of and wider than "Create options") and that the phone "+" stays on screen at 390px. |
| **F8** | **Regression in attempt 2's F2 fix, caught by CI** (`ui/e2e/external-agent-filer.spec.js`). A `gma_` token issued without an owner acts for the deployment's local account (`debian`). Whenever any `group:` entry could not be looked up, the F2 cap refused it with "whose role cannot be checked". That covers the demo, which has group rows and no Cloud Identity. | **Fixed.** Groups hold email addresses, so `externalTokenOwnerCap` treats a non-email owner as unmatched (unchanged, as O4 describes). An email owner whose only possible match is an unreachable group is still refused. `TestExternalTokenCannotExceedItsOwnersCurrentRole` covers both cases. |
| F1 | **Behind IAP, the access code never lifts a verified person's role.** `POST /api/auth/access-code` answers 200 and sets the `grain_access` cookie. The next request carries the IAP assertion, the role gate holds the person to `grain/viewer`, and filing gets a 403 (1.35–1.37). This happens because the IAP verifier does not implement `AccessCodePrecedence`, so `verifierAccessCodeWins` is false. The bare access code with no assertion is still an unlimited admin (1.37b), and logout drops back cleanly (1.38–1.40). The plan's phrase "only presenting the deployment access code lifts a role's deny" refers to entry administration (`access-rbac.md` §3), not to elevating an IAP browser session. | **Fixed (task 3565).** Precedence is unchanged. `POST /api/auth/access-code` with a verified assertion from a verifier that does not declare `AccessCodePrecedence` now answers `403`: "you are signed in as … through iap, and that sign-in decides your role here …: the access code does not change it". It sets no cookie. The code is not checked, so the refusal is no oracle. A form post shows the message on the page. The code with no assertion still signs in. Test: `TestAccessCodeSignInBehindIAPSaysTheVerifiedRoleApplies`. |
| O3 | The plan and task said "`grain/developer`" and "CLI exit code `4`". grain ships `grain/operator`, `grain/contributor`, `grain/viewer` and `grain/delegated`. `grain/developer` is refused as an unknown set (1.4). `cli.spec.md` #CLI-10 maps 401/403 to **exit 3**; exit 4 means the daemon is unreachable. | The walkthrough used `grain/contributor` as the "developer" role and checked for exit 3 (2.17–2.22). Every refusal exits 3 and names the role and the missing scope. |
| O4 | A token whose owner is **removed** from the allowlist (`grain access deny`) still works within its own scopes. Filings land in `proposed` (held). F2's cap applies only when the owner still matches an entry, so it does not break deployments whose token owners were never on the list (single-user and access-code-only setups). | **Decided and fixed (task 3565): both are refused while a verifier (IAP, registry or a wrapper's) is active.** That list is then what admits people, so an owner with no entry has no role for the token to act within. This holds whether the owner was removed or was never added. `externalTokenOwnerCap` answers `403` "… acts for <owner>, who holds no role on this deployment". With no verifier the list admits nobody, and tokens work as before; that keeps single-user and access-code deployments working. Ownerless tokens act for the local account, which is not an email address, and are never refused for being unlisted. Test: `TestExternalTokenOfAnUnlistedOwnerIsRefusedBehindAVerifier`. |
| O5 | `grain/viewer` holds `deployment:read`, a non-dangerous `*:read`, so viewers can read `GET /api/logs` (2.7, 2.15). | Behaves as designed (`docs/access-code.md` "Roles"). |

## Item results (all PASS; IDs refer to `results.md`)

1. **Resolution and precedence**:
   - alice (`user:` viewer + `group:` contributor) holds the **union** (1.6–1.8), and vic (viewer only) is refused writes (1.9).
   - An unlisted caller can read `whoami` (`authenticated:false`), but every scoped route returns a 403 naming Settings → Access (1.11, 1.11b).
   - `serviceAccount:` and `delegate:` entries require `--reason`, and `--label` is validated (1.12–1.18).
   - The `delegate:` row defaults to `grain/delegated`, starts with `last_used_at` null, and gets it stamped on first use (1.19, 1.22).
   - Delegated tasks are attributed to "dana (via release-console)" (1.21, 1.24).
   - The person and the delegate are **intersected**: viewer vic via a write delegate is refused (1.25), as are `act.access=read` writes (1.26), unlisted apps (1.27) and access-list edits (1.28).
   - **When the group lookup is down, resolution fails closed**: alice keeps only her `user:` viewer role and is refused writes, group-only erin is refused with the generic "lookup unavailable" message, and no task is stored (1.29–1.34).
2. **Limited-role filing and edit holds (#766)**:
   - The viewer is refused PATCH, approve, force-merge, secret writes and Access edits; `task://` reads are allowed (2.1–2.6).
   - The contributor's dangerous `api:config:write` grant and its `admission: durable` filing are both held. The contributor cannot approve their own held task, cannot raise another task's admission, force-merge, rotate secrets or self-promote (2.8–2.15).
   - The CLI exits 3 on every refusal (2.17–2.22b).
   - The target task is unchanged, and only the held filings were stored (2.23, 2.24).
3. **MCP**:
   - Every caller's `tools/list` on `/api/mcp` is the six compact meta-tools.
   - The routes `grain_explore_scope` returns shrink with role: operator 293 > contributor > viewer 130 (3.2). The viewer's routes are all reads, and none touches `capabilities:detach`, config writes or dispatch (3.3, 3.3b).
   - `grain_invoke` on **every** omitted route was refused for the viewer and the contributor, and nothing was mutated (3.4–3.7b).
   - The `gma_` owner-downgrade checks are covered by F2 (3.8–3.15).
4. **Browser walkthrough (#822)**: run in attempt 3. Two people were walked through 12 routes each: `/`, `/boards`, the kanban board, a task's detail, bulk select on `/`, `/new` (the New Task drawer), `/inbox`, `/repos`, `/processes`, `/settings`, `/settings/access` and `/system`. Each route was visited at 1440×900 and 390×844, light and dark: **96 visits per build**, before and after the fix. The full tables are in [`browser-results.md`](2026-11-07-rbac-roles/browser-results.md), and the raw data is in [`browser-results.json`](2026-11-07-rbac-roles/browser-results.json).
   - **Background polling**: every visit sat idle for 4 s with the page's own polling running. Across all 192 visits there were **0 403s**, 0 other 4xx/5xx responses, 0 error toasts and 0 console errors.
   - **Layout**: horizontal overflow was 0 px everywhere. Header, nav, main, page-header and the page's first buttons were measured on arrival and again 4 s later: the largest movement was **0 px**.
   - **Keyboard**: twelve presses of Tab moved focus through 5–12 distinct controls on every page. Focus never landed off-screen.
   - **Role gating**: before the fix, the viewer had enabled Approve controls on `/inbox` (F6) and an enabled New task on every page (F7). The first run's detector matched the phone "+" but not the rail's "+ New task" label. The detector was widened for the after run. After the fix the viewer has **no enabled write control anywhere**. Every gated control (New board, Edit, Approve, Defer, Run now, New task, …) is disabled and its wrapper carries the reason. The contributor keeps New task, Edit, Defer, Decline and New board (New board files a task, so `tasks:write` is the right scope). It is refused Approve, Approve all and Run now, which match its missing `tasks:admin` and `dispatch:write`.
   - **Screenshots**: [`contact-sheet-before.png`](2026-11-07-rbac-roles/contact-sheet-before.png) and [`contact-sheet-after.png`](2026-11-07-rbac-roles/contact-sheet-after.png) show the viewer's `/` and `/inbox` at 1440 light and dark and at 390, and the contributor's `/inbox`. The sandbox's file reader cannot display PNGs, and `LOOK=1` has no Gemini key here. The layout claims therefore rest on the bounding-box measurements above and the assertions in `role-gated-actions.spec.js`, not on a visual read.

## Reproduce

Item 4 needs the daemons that `HOLD=1` leaves up:

```
HOLD=1 GRAIN=$(pwd)/grain docs/test-runs/2026-11-07-rbac-roles/walkthrough.sh /tmp/rbac &
# after "holding daemon": add a list and a kanban board with the access code, then
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node docs/test-runs/2026-11-07-rbac-roles/browser-walkthrough.mjs \
  http://127.0.0.1:18490 /tmp/shots vic@example.com=viewer dana@example.com=contributor
(cd ui && npx playwright test e2e/role-gated-actions.spec.js e2e/external-agent-filer.spec.js)
```

Items 1–3:

```
GRAIN=$(pwd)/grain docs/test-runs/2026-11-07-rbac-roles/walkthrough.sh /tmp/rbac   # HOLD=1 keeps the daemons up
go test ./pkg/ui -run TestExternalTokenCannotExceedItsOwnersCurrentRole
```
