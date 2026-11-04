# Test Run: Agent Access Requests and Console-Session Reach (2026-11-04)

Walkthrough execution of `docs/test-plans/2026-11-04.md` §6 ("Agent access requests and console-session reach") on task 3072, covering "What is NOT proved" items 1–4 and UI verification across desktop (`1440×900`) and phone (`390×844`) viewports in light and dark themes.

## Summary of Findings and Fixes

Four bugs were uncovered during the walkthrough, reproduced deterministically (`docs/test-runs/2026-11-04-access-requests/repro-before.txt` → `repro-after.txt`), fixed in-branch, and covered with Go, Vitest, and Playwright regression tests:

1. **Second `request_access` after a partial grant hid the earlier answer (`pkg/ui/task_access_request.go`, `ui/src/components/detail/PendingAccess.jsx`).**
   - **Root cause:** `CheckAccessRequest` rejects items a task already holds (`"task <id> holds this already"`). After a person granted 2 of 3 items in Request 1, a follow-up `request_access` asking again for the declined 3rd item had a different request key (`1 item` vs `3 items`), so `pendingAccessFor`'s exact `d.Request.Key() == req.Key()` check left `pendingAccess.earlier` empty (`null`). In addition, `PendingAccess.jsx` styled `access-earlier` as `"info"` for partial grants and did not name the declined items.
   - **Fix:** Match any earlier `AccessDecision` sharing at least one `AccessRequestItem.Key()` with the pending request (`asked[it.Key()]`), render `severity="warning"` whenever `d.denied || declined.length > 0`, and list declined item labels (`2 granted, 1 declined (api:tasks:admin).`).
   - **Regression tests:** `TestSecondAccessRequestAfterPartialGrantShowsEarlierAnswer` (`pkg/ui/task_access_request_test.go`), `PendingAccess.test.jsx`, and `ui/e2e/access-request-walkthrough.spec.js`.

2. **Interactive tasks and console sessions parked on `request_access` reported `"Waiting for your message"` and were omitted from `/inbox` (`ui/src/state.js`).**
   - **Root cause:** `askedCase(t)` checked `if (t.interactive || t.consoleSession)` before `if (t.awaitingAccess)`, returning `{ kind: "message", title: "Waiting for your message", action: "reply" }` instead of `{ kind: "access", title: "Asked for access", action: "grant" }`.
   - **Fix:** Moved `if (t.awaitingAccess)` ahead of `if (t.interactive || t.consoleSession)` in `askedCase(t)`.
   - **Regression tests:** `ui/src/state.test.js` and `ui/e2e/access-request-walkthrough.spec.js`.

3. **Capability catalogue `requires_person` checks blocked `PrincipalAgent` only, permitting `PrincipalService` / `PrincipalAutomation` (`pkg/ui/capability_catalogue.go`, `pkg/ui/client.go`).**
   - **Root cause:** `catalogueEditRefusal`, `catalogueApprovalRefusal`, `CreateTask` (`pkg/ui/client.go`), and `agentMayApprove` (called by `RunNow`) tested `actor.Kind == model.PrincipalAgent` rather than `actor.Kind != model.PrincipalHuman`, allowing non-person service accounts or automation to file approved tasks holding `requires_person` capabilities, attach them directly, or approve / `run-now` proposed tasks holding them.
   - **Fix:** Enforced `actor.Kind != model.PrincipalHuman` across `catalogueEditRefusal`, `catalogueApprovalRefusal`, `CreateTask`, and `agentMayApprove` (running `catalogueApprovalRefusal` before `by.Kind != model.PrincipalAgent`).
   - **Regression tests:** `TestCatalogueRequiresPersonHoldsAProposal`, `TestCatalogueRequiresPersonRefusesAnAgentsApproval`, `TestCatalogueRefusesFilingAndEdit` (`pkg/ui/capability_catalogue_test.go`), and `TestOnlyAPersonDecidesAnAccessRequest` (`pkg/ui/task_access_request_test.go`).

4. **`PendingAccess.jsx` omitted `by <actor>` on real server responses (`ui/src/components/detail/PendingAccess.jsx`).**
   - **Root cause:** `model.Principal` has untagged Go fields (`Kind`, `ID`, `Source`), so `/api/tasks/{id}` serializes `earlier[].by` with capital `"ID"`, while `PendingAccess.jsx` only read `d.by?.id`.
   - **Fix:** Updated `PendingAccess.jsx` to read `const who = d.by?.id || d.by?.ID;`.
   - **Regression tests:** `PendingAccess.test.jsx` (using Go's serialized `{ Kind: "human", ID: "alice", Source: "access-code" }`) and `ui/e2e/access-request-walkthrough.spec.js`.

---

## Commit and Environment

- **Base commit tested:** `3e1d55bf` (`origin/main`, fast-forwarded onto `grain/049f1e09/task-3072-walkthrough-request-access`).
- **Build:**
  - `cd ui && npm run build` (Vite production bundle into `pkg/ui/static`).
  - `go build -o /home/debian/bin/grain ./cmd/grain` and Playwright `e2e/global-setup.js` (`-tags grain_e2e`).
- **Browser & Toolchain:** Chromium via Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), Go 1.26, Node v20.19.0 on Debian Linux sandbox VM.
- **Named Stubs / Boundaries:**
  1. **Scripted LLM turn driver (`ui/e2e/consolerestserver/main.go` `scriptedSession`):** Substitutes deterministic tool-call sequences for live LLM inference while spawning the real `grain mcpserver` subprocess over stdio JSON-RPC against the live SQLite-backed `ui.NewServer` + `orchestrator.RunCycle` loop.
  2. **Host-sandbox network-isolation adapter (`isolatingHostSandboxes` in `ui/e2e/consolerestserver/main.go`):** Because the sandbox VM runs host-directory sandboxes without nested KVM/Granule VMs, `isolatingHostSandboxes` reports `IsolatesNetwork() == true` so tasks filed with `networkAccess: "none"` can be queued, dispatched, and upgraded to `"open"` through `request_access`.
  3. **Local `git http-backend` remote (`startGitServer` in `ui/e2e/consolerestserver/main.go`):** Serves local bare git repositories (`acme/widgets`, `acme/docs`, `acme/lib`) in place of GitHub.com.

---

## Walkthrough Execution & Verification

### 1. End-to-End `request_access` Grant, Resume, Partial-Grant History, and Denial

Executed in `ui/e2e/access-request-walkthrough.spec.js` over `consolerestserver` + `grain mcpserver`:

1. **In-turn validation refusal & recovery (Turn 1):**
   - Configured `targetRepos: ["acme/widgets", "acme/docs", "acme/lib"]` and `work-ownership: {"default": "on"}`.
   - Filed task on `acme/widgets` with `networkAccess: "none"`.
   - Turn 1 called `request_access` first with `{ kind: "grant", capability: "nonexistent-cap" }`: `POST /api/tasks/{id}/access-request/check` refused it live (`isError: true`, `"This request cannot be relayed as it stands: requests[0] (nonexistent-cap): nonexistent-cap is not a registered capability... Fix it and call request_access again."`).
   - In the same turn, the run called `request_access` with 3 valid items:
     - `0`: `{ kind: "read", repo: "acme/docs" }`
     - `1`: `{ kind: "network", access: "open" }`
     - `2`: `{ kind: "grant", capability: "api:tasks:admin" }`
   - Check succeeded (`isError: false`); `orchestrator.ProcessResult` skipped the errored call and parked the task in `awaiting_reply` (`awaitingAccess: true`).
2. **Partial grant in Inbox (2 of 3 items):**
   - Opened `/inbox`, verified the row rendered `<PendingAccess>` in place of the plain text reply box at `1440×900` and `390×844`, unticked `api:tasks:admin`, and clicked `Grant 2 of 3`.
3. **Resumed run holds & decision comment (Turn 2):**
   - Verified via `/e2e/runs` that Turn 2 ran holding `reads: ["acme/docs"]`, `networkAccess: "open"`, and `grants: []` (without `api:tasks:admin`).
   - Verified Turn 2's assembled prompt included the decision comment:
     - `**Access request granted:** Grant read on acme/docs and open network to verify docs`
     - Granted items: `read acme/docs`, `open network`
     - `Declined (not granted -- do not ask for these again):` `- api:tasks:admin`
4. **Second request displays earlier partial decision & denial flow (Turn 2 → Turn 3):**
   - Turn 2 called `request_access` for `{ kind: "grant", capability: "api:tasks:admin" }` and parked.
   - Opened `/tasks/{id}`: verified `data-testid="access-earlier"` rendered warning alert `"Answered before by console-rest-e2e-operator: 2 granted, 1 declined (api:tasks:admin)."` and `data-testid="access-holds"` rendered `"Already holds: reads acme/docs · network open"`.
   - Clicked `Deny`, verified `Deny request` remained disabled for empty and whitespace-only `"   "` input, entered `"Inspect task summary from parent note instead."`, clicked `Deny request`, and confirmed Turn 3's prompt carried `Access request denied:` and the operator's reason.

### 2. Refusals

Verified across `ui/e2e/access-request-walkthrough.spec.js`, `pkg/ui/task_access_request_test.go`, and `pkg/ui/capability_catalogue_test.go`:

- **Agent `api:tasks:admin` token refused:**
  - `POST /api/tasks/{id}/access-request/grant` and `/deny` with `X-Grain-Acting-Task: <adminTaskId>` (holding `api:tasks:admin`) returned `403 Forbidden`: `{"error":"POST /api/tasks/{id}/access-request/grant is not part of the agent API (docs/agent-api.md)"}`.
  - Direct `Client.GrantAccessRequest` / `DenyAccessRequest` with `PrincipalAgent` or `PrincipalService` also refuses (`"only a person may approve or deny an access request"`).
- **Access-code session succeeds:**
  - Browser & API sessions signed in via `POST /auth/access-code` (`PrincipalHuman`, `SourceAccessCode`) granted both background and interactive requests with `200 OK`.
- **`requires_person` capability requires `PrincipalHuman`:**
  - Verified in `pkg/ui/capability_catalogue_test.go` and `repro-after.txt` that `PrincipalAgent`, `PrincipalService`, and `PrincipalAutomation` are held as unapproved proposals on `CreateTask(Approved: true)` and refused on `SetCapabilityGrant`, `Approve`, and `RunNow`, while `PrincipalHuman` succeeds.
- **Deny without a reason refused:**
  - `POST /api/tasks/{id}/access-request/deny` with `{"reason": "   "}` returned `400 Bad Request`: `{"error":"reason is required: the next run is told why it was denied"}`, and the UI `Deny request` button remained disabled until non-whitespace text was entered.

### 3. Interactive Chat Card and Persistent Session Turn Loop

- **Interactive chat (`ui/e2e/access-request-walkthrough.spec.js`):**
  - Filed `interactive: true` task on `acme/widgets`; Turn 1 called `request_access` for `read acme/lib` + `grant api:tasks:admin`.
  - Confirmed the parked interactive task surfaced in `/inbox` with `"Asked for access"` (not hidden as `"Waiting for your message"`), rendered `<PendingAccess>` inside `<TaskChat>` with zero horizontal overflow at `1440×900` and `390×844`, and resumed holding `reads: ["acme/lib"]` after `Grant 1 of 2`.
- **Persistent session turn loop (`pkg/orchestrator/access_request_park_test.go`):**
  - Added `TestPersistentSessionTurnLoopStopsOnAccessRequest`:
    1. `in-process continuation loop` (`WaitForSessionReply: true`, `SessionSandboxIdleTTL: 5s`): verified `continueSessionTurns` broke immediately (`< 2s`, 1 agent call) on `request_access` with `StateAwaitingReply` and `PendingAccessRequest` set, rather than waiting out `SessionSandboxIdleTTL` or spinning.
    2. `keep-running session across cycles` (`KeepRunning: true` across 3 `RunCycle` ticks): verified `reconcileKeepRunning` stayed parked on `PendingAccessRequest` (`calls == 1`) until decided.

### 4. Console Session Reach

Verified via Go integration tests (`go test -race ./pkg/ui -run TestConsoleSession` and `go test -race ./pkg/orchestrator -run TestConsoleSession`):

- Console sessions default to `readsAll: true` and `model.ConsoleSessionCIReadGrant` (`api:ci:read`), automatically syncing newly added working repositories (`repo_config` / `ConsoleSessionAutoReads`) onto the Director session's open task on the next cycle so `grain ci list` can read their CI runs, dropping retired repositories (`work_ownership: off`) on the next cycle, preserving `readsAll: false` across cycles when opted out, and refusing `403` when an agent holding `api:sessions:write` (without deployment-wide read access) attempts to set `readsAll: true` (`readsAll`, `reads_all`, or `ReadsAll`).

### 5. UI Screenshots and Layout Checks

Captured in `docs/test-runs/2026-11-04-access-requests/` with Playwright bounding-box assertions (`expectInside`: `box.x >= 0`, `box.x + box.width <= viewportWidth + 1`, `scrollWidth - clientWidth <= 1`, and all buttons and `access-item-chip` badges strictly inside the card):

- `inbox-desktop-light.png`, `inbox-desktop-dark.png`, `inbox-phone-light.png`, `inbox-phone-dark.png`
- `task-pane-desktop-light.png`, `task-pane-desktop-dark.png`, `task-pane-phone-light.png`, `task-pane-phone-dark.png`
- `interactive-chat-desktop-light.png`, `interactive-chat-desktop-dark.png`, `interactive-chat-phone-light.png`, `interactive-chat-phone-dark.png`
- `contact-sheet.png` (`2000×1797` composite grid across all 12 shots)

---

## Commands Run

```bash
cd ui && npm run build
go build -o /home/debian/bin/grain ./cmd/grain
go test ./tests/hygiene/... ./tests/security/... ./pkg/mcp/...
go test -race ./pkg/ui -run 'TestAPendingAccessRequest|TestAnAccessRequestDenial|TestOnlyAPersonDecides|TestAnAgentCannotApprove|TestSecondAccessRequest|TestCatalogue|TestConsoleSession'
go test -race ./pkg/orchestrator -run 'TestProcessResultParksOnAnAccessRequest|TestPersistentSessionTurnLoopStopsOnAccessRequest|TestConsoleSession'
cd ui && npx vitest run src/state.test.js src/components/detail/PendingAccess.test.jsx
cd ui && SHOTS_DIR=/home/debian/work/docs/test-runs/2026-11-04-access-requests PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/access-request-walkthrough.spec.js e2e/access-request-card.spec.js
```
