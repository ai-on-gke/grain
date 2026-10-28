# Verification and Operation of Inputs in Teams, Members, and Git Connections Settings

- **Date:** 2026-10-11
- **Tested Environment:**
  - OS: Debian GNU/Linux 12 (bookworm) x86_64
  - Node.js: `v20.19.0`
  - npm: `10.8.2`
  - Playwright: `@playwright/test` `v1.62.1`
  - Vitest: `v4.1.11`
  - Test Server: Live `grain demo` HTTP/WebSocket backend with SQLite database
  - Browser: Headless Chromium (Playwright)

---

## Executive Summary

This walkthrough verified and operated input controls across all team configuration, member management, git connection, model backend, and remote executor panels in the Grain UI. Every form input, validation boundary, and sanitization rule was tested both via dedicated Vitest component test suites and via a full end-to-end browser walkthrough using Playwright against a live backend server.

Key areas covered and verified:
1. **New Team Dialog (`NewTeamDialog.jsx`):**
   - Team Name (required validation, error display on empty save).
   - Team ID / Slug (`TextField`, optional auto-generation vs explicit slug input).
   - Description multiline input.
   - Concurrency limits: Max Workers, Max Mergers, Max CI Jobs (integer validation, rejection of fractional values like `3.5` and negative integers).
   - Default Timezone and Default Model selection dropdowns.
   - Members and Admins comma-separated text fields (`users:role` parsing and operator admin fallback).
   - Agent Git identity (`agentGitName`, `agentGitEmail`) and Git credential token (PUT to `/api/teams/:id/github-credential`).
2. **Team Settings Overlay - Profile Panel (`TeamProfilePanel.jsx` & `TeamBroadcastDialog.jsx`):**
   - In-place text inputs for team name and description with auto-save on blur.
   - Team broadcast dialog triggering, message textarea validation (enabling submit only when non-empty), author input, and dialog cancellation.
3. **Team Settings Overlay - Capacity Panel (`TeamCapacityPanel.jsx`):**
   - Numeric inputs for Max worker agents, Max merge agents, Max CI jobs, and Merge batch size.
   - Non-negative integer validation with `rangeError(Infinity)` feedback ("Not saved: use a whole number, 0 or more.") on negative inputs.
   - On-blur and Enter key submission behaviors.
4. **Team Settings Overlay - Git Identity & Connections (`TeamGitPanel.jsx`):**
   - Git author identity inputs (Commit author name, Commit author email) with blur-save.
   - Git connections list and Add Connection form: Host, Scope (optional), Token (PAT) input.
   - GitHub App authentication option: App ID, Installation ID, and App private key multiline input.
5. **Team Settings Overlay - Members Panel (`TeamMembersPanel.jsx` & `TeamMembersBulkAdd.jsx`):**
   - Single member username input, role selection (Admin, Member, Proposer, Viewer), and dynamic add button enable/disable state.
   - Bulk members expansion drawer, multi-format textarea input (commas, semicolons, whitespace, email format), duplicate detection chips, and bulk submission.
6. **Team Settings Overlay - Models & Executors (`TeamModelsPanel.jsx` & `ExecutorsPanel.jsx`):**
   - Default model selector.
   - Remote worker daemon executor token issuance dialog, token name input, team scope, and cancellation.

---

## Automated Test Results

### 1. Vitest Component Unit Suites
- `ui/src/components/NewTeamDialog.test.jsx`: 6 tests passed (validation of required fields, boundary limits, slug handling, git credentials).
- `ui/src/components/TeamGitPanel.connections.test.jsx`: 2 tests passed (PAT and GitHub App connection form inputs).
- `ui/src/components/TeamCapacityPanel.test.jsx`: 6 tests passed (integer blur validation, range error display).
- `ui/src/components/TeamMembersPanel.test.jsx`: 8 tests passed (member role edits, removal confirmation, addition).
- `ui/src/components/ExecutorsPanel.test.jsx`: 7 tests passed (token creation, team scoping, revocation).
- `ui/src/components/TeamBroadcastDialog.test.jsx`: 8 tests passed (message validation, task count rendering).

Total Vitest Suite Execution: **All tests passed cleanly.**

### 2. End-to-End Walkthrough
- `ui/e2e/team-settings-inputs-verification-walkthrough.spec.js`:
  - Exercised full workflow end-to-end in Chromium against the live test server.
  - Test run status: **1 passed (5.4s)**.
  - Zero console errors and zero unhandled exceptions.

---

## Verification Outcomes & Edge Behaviors Verified

- **Empty Required Fields:** Saving a team with an empty name is blocked on the client side with the user-facing alert `"Team name is required"`.
- **Fractional & Negative Concurrency Limits:** Non-integer values (e.g. `3.5`) and negative values (e.g. `-1`, `-5`) are caught immediately by `teamLimitsError` and `rangeError` on both creation and inline settings edits, preventing malformed payloads from reaching the backend.
- **Auto-slug vs Manual Slug:** Supplying a custom slug sets `id` directly; omitting it sends `undefined` allowing the backend to auto-slugify the team name.
- **Secret Redaction:** Git credentials and private keys are transmitted securely via dedicated endpoints and masked in the UI.
- **Bulk Member Parsing:** Duplicate usernames in bulk text inputs are deduplicated and categorized into preview chips before committing additions.
