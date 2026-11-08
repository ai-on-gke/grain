# Test run — 2026-11-07 §3: RBAC roles, per-tool MCP checks, `delegate:` entries

Plan: [`docs/test-plans/2026-11-07.md` §3](../test-plans/2026-11-07.md). Task 3487, attempt 2, run 2026-11-08 in a grain sandbox against `main` at `46ab9012`.

## Environment

- `grain` was built from this branch (`go build ./cmd/grain`). It runs two real `grain daemon`s (`-host-sandboxes`, `-sandbox-port-base=0`, `-ci=false`, SQLite):
  - **main** on `127.0.0.1:14320`.
  - **lookup-down** on `127.0.0.1:14321`, whose `-ui-cloud-identity-endpoint` points at a closed port so every group lookup fails.
- Both daemons run behind two loopback verifiers:
  - `tests/iap/iapsim.go`: an IAP assertion signer, JWKS, its proxy on `:18490`, and a Cloud Identity stand-in with group `devs@example.com` = alice, dana and erin.
  - `docs/test-runs/2026-11-07-rbac-roles/regsim.go` (new, `//go:build ignore`): an ES256 grain-registry assertion signer with an optional `act` claim, used for `delegate:` sessions.
- Both verifiers serve plain `http://127.0.0.1` (loopback), which `pkg/webauth` accepts. No real IAP, Cloud Run or GitHub is reached.
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
| F1 | **Behind IAP, the access code never lifts a verified person's role.** `POST /api/auth/access-code` answers 200 and sets the `grain_access` cookie. The next request carries the IAP assertion, the role gate holds the person to `grain/viewer`, and filing gets a 403 (1.35–1.37). This happens because the IAP verifier does not implement `AccessCodePrecedence`, so `verifierAccessCodeWins` is false. The bare access code with no assertion is still an unlimited admin (1.37b), and logout drops back cleanly (1.38–1.40). The plan's phrase "only presenting the deployment access code lifts a role's deny" refers to entry administration (`access-rbac.md` §3), not to elevating an IAP browser session. | Not changed; it fails safe. The UX is misleading, though: the code is "accepted" and then ignored. Proposed as a follow-up: say so on the access-code page, or refuse with a message. |
| O3 | The plan and task said "`grain/developer`" and "CLI exit code `4`". grain ships `grain/operator`, `grain/contributor`, `grain/viewer` and `grain/delegated`. `grain/developer` is refused as an unknown set (1.4). `cli.spec.md` #CLI-10 maps 401/403 to **exit 3**; exit 4 means the daemon is unreachable. | The walkthrough used `grain/contributor` as the "developer" role and checked for exit 3 (2.17–2.22). Every refusal exits 3 and names the role and the missing scope. |
| O4 | A token whose owner is **removed** from the allowlist (`grain access deny`) still works within its own scopes. Filings land in `proposed` (held). F2's cap applies only when the owner still matches an entry, so it does not break deployments whose token owners were never on the list (single-user and access-code-only setups). | Recorded (3.16); proposed as a follow-up decision. |
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
4. **Browser walkthrough (#822)**: **not run in this attempt.** The run's 2-hour budget went on items 1–3 and on fixing F2. The existing `ui/e2e/role-gated-actions.spec.js` still covers the mocked viewer layout. A follow-up task was proposed for the full Playwright sweep at 1440/390, light and dark. No UI file was changed, so no screenshots are attached.

## Reproduce

```
GRAIN=$(pwd)/grain docs/test-runs/2026-11-07-rbac-roles/walkthrough.sh /tmp/rbac   # HOLD=1 keeps the daemons up
go test ./pkg/ui -run TestExternalTokenCannotExceedItsOwnersCurrentRole
```
