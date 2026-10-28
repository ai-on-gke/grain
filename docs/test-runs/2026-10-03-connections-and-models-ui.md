# Connections and models UI walkthrough

**Date**: 2026-10-03
**Branch tested**: `grain/task-1607`, built on `candidate` at `11606c9fc`, which carries task 1593's backend split.
**Environment**: Debian sandbox, Linux x86_64, Go from `/usr/local/bin/go`, Node 20, Playwright / Chromium headless shell.
**Server**: `grain demo` over its embedded SQLite store, one per Playwright worker (`ui/e2e/fixtures.js`). It serves the production frontend built by `vite build` into `pkg/ui/static`.
**Browser**: the `ui/e2e` specs below, `--project=chromium`.

## What was walked

`ui/e2e/provider-connection-lifecycle-walkthrough.spec.js`, steps 3 and 4. The steps were rewritten for this change:

1. **A connection's page.** Editing "Gemini Production Cluster" shows **Discovered models** read-only, and the page has no reasoning-effort field. **Refresh** asks the provider again. **Advertise all 3 discovered models** followed by **Save connection** stores the list. The card on the Connections tab then reads "Advertises: gemini-2.5-pro, gemini-2.5-flash, gemini-1.5-pro".
2. **The default team's Models tab.** **Default model** is picked from the advertised models (gemini-1.5-pro). The model's card shows the Default chip and "Gemini Production Cluster (Enabled)". Its **Reasoning effort** is set to `high`. gemini-2.5-pro shows "2 connections", because the seeded connection serves it too. `GET /api/teams/default` then returns `defaultModel: gemini-1.5-pro` and `modelSettings` containing `{gemini-1.5-pro, high}`.
3. **New task.** The form has no **Agent framework** field. **Model** is set to gemini-2.5-flash. Under Advanced options, **Specific connection** doesn't offer infra's Claude connection, and "Gemini Production Cluster · Antigravity" is picked. The `POST /api/tasks` body has `model: gemini-2.5-flash` and no `agentFramework`, and the created task returns the same `model` and the pin.
4. **The infra team's Models tab** only offers claude-sonnet-5. That is the framework default of its only connection, which lists nothing. It isn't offered default-team models, and it says "Claude takes no reasoning effort." qa-platform, which has no connections, says "No connection of team "qa-platform" advertises a model yet".

`ui/e2e/new-user-workflow-walkthrough.spec.js` now checks that a new personal team's Models tab shows its empty state. Before, it typed a default model into what was then a text field. `ui/e2e/non-admin-team-setup-walkthrough.spec.js` only follows the rename to **Save connection**.

All three specs passed.

## Stubs

- `**/api/agent-models*` was stubbed. This was already in the spec. It stands in for the provider's own model listing, which is an external boundary with no live provider in demo. Without the stub, demo answers `available: false`, and the page shows "This deployment cannot ask this provider for its models".
- `**/api/executors` was stubbed for step 5's remote executors. This was already in the spec and wasn't changed.

Grain's own APIs (`/api/settings`, `/api/teams/{id}`, `/api/tasks`) weren't stubbed.
