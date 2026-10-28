# 2026-10-08 Teams UI Walkthrough: Git connections list

**Date**: 2026-10-08
**Commit tested**: branch `grain/task-2372`
**Environment**: Debian sandbox, Linux x86_64, Go 1.26.6, Node 20.19.0, Playwright 1.62.1 with Chromium.
**Server**: `grain demo` over embedded SQLite store serving the production frontend built into `pkg/ui/static`.
**Browser**: Playwright library script driving visible controls at 1400×900.

## Scope Walked
1. Team Settings > Git Identity:
   - Added `GitConnectionsList` table displaying configured connections: Host, Kind, Auth Type, Scope, and Actions (Test, Remove).
   - Display of legacy `Team.GitCredential` entry with "legacy default" badge on initial load.
2. Adding Connections:
   - Add connection form supporting Host, Kind (github / git), Scope (optional), and Auth type (PAT / GitHub App).
   - Validated adding a PAT connection (`ghe.internal.acme`, scope `acme-org`).
   - Validated adding a GitHub App connection (`ghe.corp.example`, App ID, Installation ID, and RSA private key).
3. Testing Connections:
   - Host-aware test endpoint (`POST /api/teams/:id/git-connections/check`) executed directly via the "Test" button on connection row.
   - Verified that check result displays correctly in `CredentialCheckResult` component.
4. Removing Connections:
   - Verified connection removal via `DELETE /api/teams/:id/git-connections/:cred`.
   - Verified table updates dynamically after removal.
5. RepoPicker multi-host support:
   - Verified `looksLikeRepo` accepts multi-host format (`host/owner/name`).
   - Verified `repoUrl` generates URLs pointing to external hosts when host is specified.

## Outcomes
- All Vitest and Go unit/security tests pass:
  - Vitest: 3181 tests passed across 192 test files.
  - Go: `pkg/ui`, `pkg/model`, and `tests/security` route authorization tests pass.
- Demo walkthrough completed with zero regressions.
