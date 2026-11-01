# Nightly New Features Test Plan — 2026-10-23 (Afternoon Batch)

This plan covers everything merged to `candidate` and `main` since the previous batch, [`2026-10-23-morning.md`](2026-10-23-morning.md). That plan audited `candidate` at `fb5b8074e` (2026-10-23 03:25 UTC) and `main` at `fae76632e` (2026-10-23 04:08 UTC). This one audits `origin/candidate` at `5c39c2173` (2026-10-23 13:06 UTC) and `origin/main` at `162e4a3d8` (2026-10-23 13:20 UTC). Audit timestamp: 2026-10-23 13:21 UTC.

- **Range audited:** `fb5b8074e..origin/candidate` (23 non-merge commits across 18 pull requests) and `fae76632e..origin/main` (39 non-merge commits across 16 pull requests; `95f0d3825`, `d9fb2a870`, `bbb783d92`, `8da13f388`, `a12e35875`, `f7bee01d4`, `5fa5f1f48`, `ac8918fbf`, `cdf773d8f`, `374341e32`, `bd2d2fe1a`, `d44a2141f`, `d4cc808be`, `445130a7d`, `e9338449e`, `4a78ddcf4` were authored earlier but merged into `main` after `fae76632e`).
- **Releases in the window:** none. `v2.2.0-rc.1` (`aa5879403`, merge-base `747f82455`) remains the current candidate tag and `v2.1.1` is `latest`.
- **File naming:** named `2026-10-23-afternoon.md` because `2026-10-23.md` (01:00 UTC) and `2026-10-23-morning.md` (04:15 UTC) already exist for earlier batches on the same date, following `2026-10-15-afternoon.md`.

## Branch situation and how follow-up tasks handle it

1. **What landed on `candidate` (`fb5b8074e..5c39c2173`):**
   - Walkthrough fixes, security-review follow-ups, and E2E suites from the 2026-10-22 and 2026-10-23 batches:
     - External config-repo (read-only IaC) boundary hardening, `StateRepoPullRequestClient` complete method guard, and strict git branch validation (`20482a321`, `ad2b57700`, `2e25d02d5`, `f6d41ae41` — §6);
     - Console session restart refresh (`refreshSessionSaved`), live `WorkTree` updates during `awaiting_reply`, `/director` route canonicalization, `/processes` phone layout, and `grain demo` multi-scenario declaration merging (`61707e025`, `f82f5ee81`, `cd2b49e44` — §5);
     - Kubernetes sandbox settings CLI & UI fixes (`fe47c169c` — §7);
     - `grain get <id>` waiting activity output (`4b58f9867` — §7);
     - Shared CI fault ownership cache fingerprint invalidation and `PriorityMergeHead` promotion (`8153efad6` — §7);
     - Guest step timeout ceiling (`math.Ceil`) and CI suite speedups (`70c0de4e6` — §7);
     - Wildcard + randomized starting block port probing in `freeAlignedBlock` (`943ba96f9` — §7).
   - Shared fixes landed on both `main` and `candidate`:
     - CI `capabilities: []` opt-out across workflows (`22fffcf57` on `main`, `01d598d5f` on `candidate` — §7);
     - Scarce CI capability opt-in (`ExplicitOnlyCapability`) and GCP IAM 429 / 10-key cap 30–60s backoff (`3636faf07`, `a28d532e4`, `f301b8768` on `main`; `966e6d7db`, `ebb78c544`, `a6d4666c9` on `candidate` — §7);
     - Merge-queue `MergeQueueRepairBase` tracking across in-flight re-armed runs and port closure via `Backported-from` footers (`ac8918fbf` on `main`, `99ac96f96` on `candidate` — §7);
     - Go 1.26.10 toolchain bump (`4a78ddcf4` on `main`, `47533c170` on `candidate`) and load-test drain reopen gate (`e9338449e` on `main`, `02050c93d` on `candidate`).

2. **What landed on `main` only (`fae76632e..162e4a3d8`):**
   - Standing console sessions (`director` and custom sessions) choosing which model they run on (`41e37b8ef`) and starting a new session directly from its first message at `/session/new` (`NewSessionPage.jsx`, `ConsoleSessionTitleFromMessage`, `05bdba0f9`, `cdc9ffa35`, `26b4a2d19`, `39579fa8b` — §1);
   - Capability server schema-declared configuration (`configure`), secret config fields, in-place server edit/rename/test, usage holder tracking, and the `capability_policy` rule editor with deny rules and impact preview (`8da13f388`, `a12e35875`, `f7bee01d4`, `5fa5f1f48`, `42cf5cb6f`, `49ea003b3`, `4d21ca84e`, `df27e7c78`, `cc71469ed` — §2);
   - Daemon-side detached host CLI execution through `grain hostd` protocol 2, adoption across daemon upgrades (`runhost.Hostd`, `RecoverHostd`, `antigravity.ParseResult`), and removal of host mode (`GRAIN_INSTALL_MODE=host`), the per-run Docker CLI launcher (`agent.ContainerHost`), and the `auto` run location (`374341e32`, `bd2d2fe1a`, `d44a2141f`, `d4cc808be`, `445130a7d`, `0b985ac31`, `92ea4ada2`, `1b06f6607`, `580d8c2d7`, `186e75fd0`, `b966975b4`, `f87bcfedd` — §3);
   - Cross-framework `default_model` fallback when a provider's models are usage-limit paused, plus the interactive framework-switch conversation notice (`cdf773d8f` — §4);
   - Pinned sidebar navigation sub-target matching (`System → Health`, `Settings → General`, specific sub-targets) and `ConsoleSessionPage` task-prop guarding (`95f0d3825` — §5).

3. **How follow-up tasks handle the branch split:**
   - Every follow-up task targets `candidate` (`branch: "candidate"`, `auto_merge: true`) and depends on Task 270.
   - **§6 and §7** test commits that are already on `origin/candidate` today and can execute immediately once Task 270 merges.
   - **§5** tests `61707e025`, `f82f5ee81`, and `cd2b49e44` (already on `origin/candidate`) and also checks whether `95f0d3825` (`main`) has been ported or cut onto `origin/candidate`.
   - **§1, §2, §3, and §4** test `main`-only features. Each task starts by running `git fetch origin && git branch -r --contains <sha>` on its feature's key commits:
     - **Commits are on `origin/candidate`:** the task proceeds.
     - **Commits are not yet on `origin/candidate`:** the task stops with `ask_question`, naming the missing commits so the human can reply once the next candidate cut lands. It **never merges `main` into `candidate`**.

4. **Relationship to `2026-10-23-morning.md`:**
   - `2026-10-23-morning.md` §1 covered the `pkg/hostd` server-side protocol 2 (`a1ea68161`, `b82d33cb6`, `dacee7e00`). §3 of this plan covers the **daemon and antigravity half** (`bd2d2fe1a`, `d44a2141f`, `d4cc808be`) and the **removal of host mode, `agent.ContainerHost`, and `auto`** (`374341e32`, `1b06f6607`, `580d8c2d7`, `186e75fd0`), which superseded `2026-10-23-morning.md` §3 (`3697762f8`).
   - `2026-10-23-morning.md` §2 covered per-grant method arguments (`CapabilityArgsForm` on tasks/templates/schedules). §2 of this plan covers **server-level `configure` schemas, secret config fields, in-place server edit/rename/test, usage holders, and the `capability_policy` editor** (`8da13f388`..`df27e7c78`).
   - Potential merge conflicts flagged for the next cut:
     - `95f0d3825` (`main`) and `61707e025` (`candidate`) both edited `<ConsoleSessionPage task={...}>` in `ui/src/App.jsx` (`openSessionTaskId && String(sessionTask?.id) === String(openSessionTaskId)` vs `sessionTask && String(sessionTask.id) === String(openSessionTaskId)`). §5 explicitly verifies their combined behavior.
     - `cdc9ffa35` (`main`, PR #4521) deleted `NewSessionDialog.jsx` in favor of `/session/new` (`NewSessionPage.jsx`), while `61707e025` (`candidate`, PR #4515) added a test in `ui/e2e/director.spec.js` that still opens `NewSessionDialog` (`page.getByRole("dialog")` -> `getByLabel(/Session name/)`). §1 updates `ui/e2e/director.spec.js` and tests both `/session/new` and `ConsoleSessionPage` model selection once PR #4521 is on `candidate`.

## Summary of Audited Feature Areas

| § | Feature Area | Key commits (branch) | Report Target |
|---|---|---|---|
| 1 | Standing console sessions model selection (`model.ConsoleSession.Model`) and starting a session from its first message (`/session/new`, `NewSessionPage`, `ConsoleSessionTitleFromMessage`, `ModelPicker`) | `41e37b8ef`, `05bdba0f9`, `cdc9ffa35`, `26b4a2d19`, `39579fa8b` (main, #4514, #4521) | `docs/test-runs/2026-10-23-standing-session-model.md` |
| 2 | Capability server schema-declared `configure`, secret config fields (`configSecrets`), in-place edit/rename (`renameFrom`), live probe (`POST /api/capability-servers/test`), `run: host` allowlist check, usage holders, and `capability_policy` editor with deny rules & preview | `8da13f388`, `a12e35875`, `f7bee01d4`, `5fa5f1f48`, `42cf5cb6f`, `49ea003b3`, `4d21ca84e`, `df27e7c78`, `cc71469ed` (main, #4466, #4476) | `docs/test-runs/2026-10-23-capability-configure-and-policy.md` |
| 3 | Daemon detached host CLI runs via `grain hostd` protocol 2, upgrade adoption (`RecoverHostd`, `antigravity.ParseResult`), `HostdStatus.updating`/`.mismatch`, and removal of host mode, `agent.ContainerHost`, and `auto` run location | `374341e32`, `bd2d2fe1a`, `d44a2141f`, `d4cc808be`, `445130a7d`, `0b985ac31`, `92ea4ada2`, `1b06f6607`, `580d8c2d7`, `186e75fd0`, `b966975b4`, `f87bcfedd` (main, #4471, #4472, #4478, #4483, #4482) | `docs/test-runs/2026-10-23-hostd-adoption-and-two-run-locations.md` |
| 4 | Cross-framework `default_model` fallback (`defaultModelFramework`), pinned-task wait reasons, and interactive framework-switch conversation notices (`FallbackFramework`) | `cdf773d8f` (main, #4470) | `docs/test-runs/2026-10-23-cross-framework-model-fallback.md` |
| 5 | Pinned sidebar navigation sub-target matching, console session restart refresh (`refreshSessionSaved`), live `WorkTree` updates during `awaiting_reply`, `/director` route canonicalization, `/processes` phone layout, and `grain demo` multi-scenario declaration merging | `61707e025`, `f82f5ee81`, `cd2b49e44` (candidate, #4515, #4469, #4522); `95f0d3825` (main, #4424) | `docs/test-runs/2026-10-23-sessions-worktree-and-pinned-nav.md` |
| 6 | External config-repo (read-only IaC) boundary hardening, `StateRepoPullRequestClient` complete method guard, `PUT /api/github-tokens` bootstrap guard, and strict `git check-ref-format --branch` validation | `20482a321`, `ad2b57700`, `2e25d02d5`, `f6d41ae41` (candidate, #4494, #4502, #4510, #4519) | `docs/test-runs/2026-10-23-external-config-repo-hardening.md` |
| 7 | CI `capabilities: []` opt-out, scarce capability opt-in & IAM 429/key-cap backoff, fractional guest step timeout ceiling (`math.Ceil`), shared fault ownership cache invalidation & `MergeQueueRepairBase`, and CLI `grain get` / `grain settings` output | `966e6d7db`, `ebb78c544`, `a6d4666c9`, `01d598d5f`, `70c0de4e6`, `8153efad6`, `99ac96f96`, `4b58f9867`, `fe47c169c`, `943ba96f9` (candidate & main) | `docs/test-runs/2026-10-23-ci-capabilities-timeouts-and-queue.md` |

---

## 1. Standing Console Sessions Model Selection and Starting a Session from Its First Message

**Commits (main only):** `41e37b8ef` (PR #4514), `05bdba0f9`, `cdc9ffa35`, `26b4a2d19`, `39579fa8b` (PR #4521)

### What changed
- **Standing session model selection (`41e37b8ef`):**
  - `model.ConsoleSession` gains `Model string` (`json:"model,omitempty"`), stored inside the `console_sessions` JSON column of `grain_config`. An empty string `""` (the default) means the deployment's default model.
  - `model.FileConsoleSessionTask` sets `Task.Model: session.Model` when filing the backing task for a new or restarted console session.
  - `orchestrator.syncConsoleSessionTask` compares `task.Model` with `session.Model` and updates the live backing task's `Model` before its next exchange, alongside `Capabilities`, `Reads`, `Writes`, `NetworkAccess`, and `GoogleChatSpace`.
  - `CreateConsoleSessionRequest` (`POST /api/console-sessions`) and `UpdateConsoleSessionRequest` (`PUT /api/console-sessions/{id}`) accept `model`, validate it through `c.declaredModelChoice(ctx, model)` (must be a model ID advertised by a configured connection or known catalog, never a connection ID; whitespace is trimmed; `""` resets to the deployment default), and return `model` in `ConsoleSessionSettings` (`GET /api/console-sessions`).
  - `ConsoleSessionPage.jsx` renders `ModelPicker` (`name="sessionModel"`) under Network access, calling `save({ model: next }, "model")` on change, and updates the "Start a fresh chat" description to state that the session's model is preserved on restart.
- **Starting a session from its first message (`05bdba0f9`, `cdc9ffa35`, `26b4a2d19`, `39579fa8b`):**
  - `POST /api/console-sessions` no longer requires an upfront `title`. It accepts `message` (and `attachments`), derives the session title from the first sentence/words of `message` (`model.ConsoleSessionTitleFromMessage`), reserves the session ID `"new"` for the UI's unsaved chat, files the backing task on the chosen `model`, posts `message` into the task as the caller's first comment, queues the first exchange immediately, and returns `created` in the response. A request with neither `title` nor `message` is refused (`400 Bad Request`).
  - `NewSessionDialog.jsx` is replaced by `/session/new` (`NewSessionPage.jsx`): clicking `+ New session` opens an empty chat with the composer focused, `Send` disabled when empty, and `ModelPicker` in the composer's button row. Opening `/session/new` and navigating away without sending a message creates nothing in `/api/console-sessions`.
  - `Sidebar.jsx` highlights the `Sessions` rail entry on `/session/new`.

### What CI proves
- `pkg/orchestrator/consolesessions_test.go` (`TestChangingASessionsModelReachesItsTask`), `pkg/ui/console_sessions_test.go` (`TestASessionsModelIsStoredAndCarriedOntoItsTask`, plus first-message creation tests), `pkg/model/console_session_title_test.go`.
- Vitest unit tests in `ui/src/components/ConsoleSessionPage.test.jsx`, `ui/src/components/NewSessionPage.test.jsx`, and `ui/src/App.test.jsx`.
- Playwright layout and smoke checks in `ui/e2e/responsive-navigation-walkthrough.spec.js`, `ui/e2e/settings-and-operations-inputs-walkthrough.spec.js`, and `ui/e2e/ui-navigation-split-button-upgrade.spec.js`.

### What is NOT proved
- **End-to-end Playwright browser flow in `ui/e2e/director.spec.js` (and reconciliation with `candidate`'s `61707e025`):**
  - Note: `61707e025` on `candidate` added a step in `ui/e2e/director.spec.js` that still expects `NewSessionDialog` (`page.getByRole("dialog")` -> `getByLabel(/Session name/)`). Once `05bdba0f9`/`cdc9ffa35` is on `candidate`, `ui/e2e/director.spec.js` must be updated to exercise `/session/new` (`NewSessionPage`) and the full live agent exchange!
  - Opening `/session/new`, selecting a non-default model in `NewSessionPage`'s `ModelPicker`, attaching a file or typing a multi-sentence first message, clicking `Send`, and verifying:
    1. `POST /api/console-sessions` returns `created` with the derived slug ID, title (`ConsoleSessionTitleFromMessage`), and `model`;
    2. The browser transitions from `/session/new` to `/session/<created.id>` without leaving a broken `/session/new` history entry;
    3. The backing task has `model` set and the first exchange runs on that model and replies in `TaskChat`;
    4. Changing `ModelPicker` on `ConsoleSessionPage` while `awaiting_reply`, sending a second message, and asserting the next turn dispatches on the newly selected model;
    5. Resetting `ModelPicker` back to the deployment default (`""`) and asserting `session.model` and `task.model` return to `""`.
- **Reserved `"new"` ID and edge-case first messages:**
  - Sending a first message whose first words are `"new"`, `"New!"`, markdown headings/code blocks (`"```go\nfunc main()..."`), or punctuation/emoji only, and verifying `ConsoleSessionTitleFromMessage` and slug generation never collide with the reserved `"new"` ID or produce an empty slug, and that creating two sessions with the same first message deduplicates their IDs (`-2`, `-3`).
- **Session restart preservation (`POST /api/console-sessions/{id}/restart`):**
  - Restarting a session with a non-default `model` both with `carryHandover: true` and `carryHandover: false`, and verifying `FileConsoleSessionTask` sets `Task.Model` on the newly created replacement task and `ConsoleSessionPage` keeps the selected model without a page reload.
- **Cross-framework model change on a live session:**
  - Starting a session on a model of Framework A (e.g. `claude-sonnet-4-6`), completing one turn (which records a framework-keyed resume token), changing the session's model to a model of Framework B (e.g. `gemini-3.1-pro`), and sending a second turn: verifying the second turn dispatches on Framework B without passing Framework A's resume token and carries the conversation history in the prompt.
- **External (read-only IaC) config-repo refusal:**
  - In external config mode, verifying `PUT /api/console-sessions/director` with `{"model": "..."}` and `POST /api/console-sessions` with `message` and `model` are refused with `409 Conflict` before filing any backing task or mutating `grain_config`.
- **Visual & responsive layout:**
  - Screenshots of `/session/new` (`NewSessionPage` with `ModelPicker` in the composer row) and `/session/director` (`ConsoleSessionPage` showing `ModelPicker` alongside capabilities and network access) at desktop (`1440x900`) and phone (`390x844`) widths in light and dark themes.

### Walkthrough procedure
1. Run `git fetch origin && git branch -r --contains 41e37b8ef 05bdba0f9 cdc9ffa35`. If `origin/candidate` is not listed, stop and call `ask_question` naming the missing commits. Never merge `main` into `candidate`.
2. Build the frontend (`cd ui && npm ci && npm run build`) and start `grain demo -addr 127.0.0.1:0 -open=false` (and `ui/e2e/directorserver` via Playwright).
3. Exercise the API with `curl` / Playwright `request`:
   - `POST /api/console-sessions` with `message: "Investigate redirect loops in auth middleware. Check cookies."` and `model: "  claude-opus-4-6  "` -> assert `200 OK`, `created.model === "claude-opus-4-6"`, derived `created.title`, and backing task `model === "claude-opus-4-6"` with the first comment posted.
   - `POST /api/console-sessions` with `message: "new"` or `title: "new"` -> assert the generated ID does not collide with reserved `"new"` (or is handled cleanly) and duplicate first messages suffix cleanly.
   - `POST /api/console-sessions` with neither `title` nor `message` -> assert `400 Bad Request`.
   - `PUT /api/console-sessions/{id}` with a connection ID instead of a model ID -> assert `400 Bad Request` (`declaredModelChoice`).
   - `PUT /api/console-sessions/{id}` with `model: ""` -> assert `model` is cleared on both the session and the backing task.
   - In external config-repo mode, assert `POST /api/console-sessions` (with `message`) and `PUT /api/console-sessions/{id}` (with `model`) return `409 Conflict` without creating an orphan task.
4. In Playwright (`ui/e2e/director.spec.js`):
   - Update `director.spec.js` if needed for `/session/new` (`NewSessionPage`) after merging with `61707e025`.
   - Open `/sessions`, click `+ New session` -> `/session/new`, select a specific model in `ModelPicker`, send the first message, and assert redirect to `/session/<id>`, `ConsoleSessionPage` `ModelPicker` value, and agent response.
   - Change the model on `ConsoleSessionPage`, send a second message, and assert the turn executes on the new model.
   - Restart the session (`Start a fresh chat`, both with handover and fresh) and assert the replacement task inherits `session.model`.
   - Test switching a session's model from one framework to another across turns.
5. Take screenshots with `make ui-screenshots PAGES=/session/new,/sessions,/session/director OUT=screenshots/standing-session-model`, inspect them at 1440px and 390px in light and dark themes, fix any scoped bugs found, add durable Playwright/Go regression tests, and write `docs/test-runs/2026-10-23-standing-session-model.md`.

---

## 2. Capability Server Schema-Declared `configure`, In-Place Edit/Rename/Test, Usage Holders, and `capability_policy` Editor

**Commits (main only):** `8da13f388`, `a12e35875`, `f7bee01d4`, `5fa5f1f48`, `42cf5cb6f`, `49ea003b3`, `4d21ca84e`, `df27e7c78`, `cc71469ed` (PRs #4466, #4476)

### What changed
- **Protocol & in-tree `gcp` server (`8da13f388`, `f7bee01d4`):**
  - Capability servers may advertise a `configure` tool whose `inputSchema.properties.config` declares their configuration schema in the enforced JSON Schema keyword subset (§4.5).
  - `pkg/capability/protocol` discovers `configure` from `tools/list` (rejecting unsupported schema keywords by name), validates `mcpServers[id].config` against that schema, calls `configure({config})` after every `initialize` and before any `mint`, and re-lists tools afterwards because advertised methods may depend on configuration.
  - Properties marked `writeOnly: true` or `format: "password"` are secret fields: `CheckStoredConfig` refuses cleartext values in `mcpServers[id].config` and requires `$secret:<name>` references, which are resolved in memory only before calling `configure`, with all resolved values scrubbed from attach/probe errors.
  - `grain capability gcp` (`pkg/capability/intree/gcp.go`) declares `configure` (`project`, `accounts`, `maxKeyAge`, and `minterKey` secret) while preserving CLI flags and `GCP_MINTER_KEY` env fallback.
- **Settings API for config, secrets, in-place edit/rename, probe test, and `run: host` (`a12e35875`, `49ea003b3`):**
  - `PUT /api/capability-servers` (`SetCapabilityServer`) validates `config` against the server's discovered `ConfigSchema` (returning field-keyed `ValidationError`s for inline UI display), writes `configSecrets` into the secrets store under `capability-server-<sha256[:4]>/<field>` (or the existing `$secret:` reference) and stores only `$secret:<name>`, and supports `renameFrom` to move an entry to a new `capability_id` in one write while checking against the old ID's schema and refusing collisions.
  - `POST /api/capability-servers/test` (`TestCapabilityServer` / `servers.Registry.Probe`) runs `initialize`, `tools/list`, and `configure` in an isolated session, resolving unsaved `configSecrets` from an ephemeral in-memory map (`grain-capability-test/<field>`) without persisting them.
  - `GET /api/capability-servers/host-entries` (`HostCapabilityEntries`) reports whether `grain hostd` is reachable and which `capabilities` entries its allowlist offers; `checkRunLocation` refuses `run: host` on save and test unless the server's `command` is in that list.
  - `grain demo`'s built-in capability server (`pkg/demo/demo_capability_server.go`) declares `configure` (`project` pattern `^[a-z][a-z0-9-]{2,29}$`, `region` enum `["us", "eu", "ap"]`, `apiToken` `writeOnly: true`) and resolves `$secret:` from `-secrets-store`.
- **Capability policy & usage holders API (`42cf5cb6f`):**
  - `model.CapabilityPolicyRule` gains `Deny bool` (`json:"deny,omitempty"`): a deny rule refuses matching references ahead of any allow rule and restricts nothing outside its scope (§4.2.2).
  - `GET /api/capability-policy` and `PUT /api/capability-policy` read and update `grain_config.capability_policy`.
  - `POST /api/capability-policy/preview` evaluates every existing capability-reference grant across open tasks, templates, inline schedules, and Settings workflow jobs and returns those that the proposed policy would newly refuse compared to the stored policy.
  - `GET /api/capability-servers/usage` returns all holders (`task`, `template`, `schedule`, `workflow`) per configured capability server.
- **Frontend & E2E (`4d21ca84e`, `df27e7c78`, `cc71469ed`):**
  - `CapabilityServersPanel.jsx` adds in-place editing (`Edit`), renaming (`renameFrom`), `Configure` (`CapabilityArgsForm` in secrets mode showing `"set"` / `"missing"` / `"not set"`), `Test` (unsaved or saved), `run: host` option gating, usage counts (`granted by N`), and holder confirmation dialogs before remove or rename; polls while any saved server is still `pending`.
  - `CapabilityPolicyEditor.jsx` edits `capability_policy` rules, previews newly denied grants via `/api/capability-policy/preview`, and gates saving behind `Save anyway` when existing grants would be refused.

### What CI proves
- Go unit tests: `pkg/capability/protocol/configure_test.go`, `pkg/capability/servers/configure_test.go`, `pkg/capability/intree/configure_test.go`, `pkg/ui/capability_server_config_test.go`, `pkg/ui/capability_policy_test.go`, `pkg/model/capability_ref_test.go`, `tests/security/routes_test.go`.
- Vitest unit tests: `CapabilityServersPanel.test.jsx`, `CapabilityPolicyEditor.test.jsx`.
- Playwright `ui/e2e/capability-server-settings.spec.js`: adds a second HTTP server entry on `grain demo -secrets-store`, tests it unsaved, configures it (invalid `project` pattern then valid + `apiToken` secret), edits headers in place, grants `#cloud-viewer` to a task, adds a deny rule with preview confirmation, and removes the server after confirming the holder dialog.

### What is NOT proved
- **Worker-state leak in `ui/e2e/capability-server-settings.spec.js`:**
  - The first test in `ui/e2e/capability-server-settings.spec.js` saves a `deny: true` rule to `/api/capability-policy` and creates an open task, and never cleans them up in a `finally` block on the shared worker-scoped `demoServer`.
- **Secret write-before-validation bug in `SetCapabilityServer` and `BindCapabilityServerSecret` (`pkg/ui/capability_servers.go`):**
  - In `SetCapabilityServer` (lines 269–299), `writes` (`req.ConfigSecrets`) are written to `c.Config.Secrets` BEFORE `updateCapabilityServers` validates `renameFrom` (whether `from` exists or `id` already exists) or `protocol.ParseServers(data)`! If `renameFrom` targets an existing `id` or nonexistent `from` (or `cfg` fails `ParseServers`), the secret is still written to the encrypted secrets store even though the `PUT` fails with `400 Bad Request`.
  - Similarly, `BindCapabilityServerSecret` (lines 456–480) writes `req.Value` to `c.Config.Secrets` before `updateCapabilityServers` checks whether `req.ID` exists in `m[req.ID]`.
- **In-place rename (`renameFrom`) end-to-end in the browser and across all 4 holder types:**
  - Creating holders of all four kinds (`task`, `template`, `schedule`, and a repo `workflow` in Settings) that reference a server, clicking `Edit` on the server, changing its Capability ID, verifying the warning dialog lists all four holders before saving, confirming the rename, and verifying `config` and its `$secret:` reference move to the new ID while the old ID is removed.
  - Attempting to rename a server onto an already-configured Capability ID in the UI and verifying the inline error and that neither server nor secrets are corrupted.
- **Secret state `"missing"` and unsaved `Test` secret isolation:**
  - Deleting the secret backing `config.apiToken` (`DELETE /api/secrets/...`) while the capability server still references `$secret:capability-server-<hash>/apiToken`: verifying `GET /api/capability-servers` reports `present: false`, `Configure` shows badge `"missing"`, and re-attaching/probing reports a missing-secret error naming only the secret reference.
  - Typing a new `apiToken` value in the `Configure` form and clicking `Test` (or testing in `Edit`) without clicking `Save configuration`, then checking `/api/secrets` to prove the unsaved test secret was not persisted to the secrets store.
- **Real in-tree `grain capability gcp` stdio server `configure` lifecycle:**
  - Adding a stdio capability server running `grain capability gcp` (with no flags), verifying it attaches and advertises its `configure` schema (`project`, `accounts`, `maxKeyAge`, `minterKey`), configuring it via the API/UI with a fake service-account JSON key stored as a secret, and verifying `configure` validates `maxKeyAge` duration strings and re-lists methods.
- **Policy allow-only vs deny precedence and dimensions (`untrustedCI`, `events`, `repos`, `runKinds`):**
  - Testing a policy that combines an allow rule (restricting a capability to `repo: "acme/widgets"` and `runKinds: ["task", "ci"]`) with a narrower deny rule (`untrustedCI: true` or `events: ["pull_request"]`), and verifying `/api/capability-policy/preview` accurately diffs newly denied holders against the previously stored policy (and does NOT list holders that were already denied by the stored policy).
- **External (read-only IaC) config-repo mode:**
  - Verifying `PUT /api/capability-policy`, `PUT /api/capability-servers` (with `configSecrets` and `renameFrom`), `POST /api/capability-servers/bind-secret` (with `value`), `POST /api/capability-servers/disable`, and `DELETE /api/capability-servers` all return `409 Conflict` in external mode without writing secrets to `-secrets-store`.
- **Visual & responsive layout:**
  - Screenshots of `/settings/credentials#capability-servers` and `#capability-policy` at desktop (`1440x900`) and phone (`390x844`) widths in light and dark themes with the Configure form, Test result panel, Policy preview warning, and Rename/Remove holder dialog open.

### Walkthrough procedure
1. Run `git fetch origin && git branch -r --contains 8da13f388 a12e35875 42cf5cb6f 49ea003b3 4d21ca84e`. If `origin/candidate` is not listed, stop and call `ask_question` naming the missing commits. Never merge `main` into `candidate`.
2. Build `grain` and the frontend (`go build -o /var/tmp/grain ./cmd/grain && cd ui && npm ci && npm run build`).
3. Audit and fix the secret-write ordering in `SetCapabilityServer` and `BindCapabilityServerSecret` (`pkg/ui/capability_servers.go`) if confirmed that `configSecrets` / `req.Value` can be written to `c.Config.Secrets` when `renameFrom`, server lookup, or `protocol.ParseServers` fails, and add a Go unit test in `pkg/ui/capability_server_config_test.go`.
4. Add `finally` state cleanup to `ui/e2e/capability-server-settings.spec.js` and expand the E2E/integration coverage to exercise:
   - In-place `renameFrom` with holders across `task`, `template`, `schedule`, and `workflow`, plus collision refusal;
   - Secret state transitions (`not set` -> `set` -> delete secret -> `missing` -> re-save -> `set`) and verifying `POST /api/capability-servers/test` with `configSecrets` leaves no entry in `/api/secrets`;
   - Attaching and configuring the in-tree `/var/tmp/grain capability gcp` stdio server through `configure`;
   - `POST /api/capability-policy/preview` with combined allow + deny rules across `runKinds`, `untrustedCI`, `events`, and `repos`;
   - External config-repo mode (`409 Conflict`) across all capability-server and capability-policy write routes.
5. Take desktop and phone screenshots in light and dark themes (`make ui-screenshots PAGES=/settings/credentials OUT=screenshots/capability-configure-policy DEMO_ARGS=-secrets-store`), inspect every image, and write `docs/test-runs/2026-10-23-capability-configure-and-policy.md`.

---

## 3. Daemon Detached Host CLI Runs via `grain hostd` Protocol 2, Upgrade Adoption, and Removal of Host Mode / `auto` Run Location

**Commits (main only):** `374341e32`, `bd2d2fe1a`, `d44a2141f`, `d4cc808be`, `445130a7d`, `0b985ac31`, `92ea4ada2`, `1b06f6607`, `580d8c2d7`, `186e75fd0`, `b966975b4`, `f87bcfedd` (PRs #4471, #4472, #4478, #4483, #4482)

### What changed
- **Daemon detached host CLI execution & upgrade adoption (`bd2d2fe1a`, `d44a2141f`, `d4cc808be`, `445130a7d`):**
  - `runhost.Hostd` (`pkg/runhost/hostdhost.go`) implements the daemon client for `grain hostd` protocol 2:
    - Performs `OpHello` before starting a process: refuses protocol mismatches (`ErrProtocolMismatch`) and falls back to attached execution only for protocol-1 `hostd`.
    - Starts host CLIs detached (`Detached: true`, `ID: runID`, `TaskID`, `Deadline: budget + DefaultAllowance`), streams stdout while persisting the byte cursor in `task_run_spool_cursor` (`Store.SetHostdCursor`, prefixed `hostd:<id>`) every `DefaultCursorEvery` and on detach.
    - When the daemon is shutting down (`orchestrator.StoppedByDaemon`) or flushing for an upgrade (`runHostUpgrade`), it detaches without killing the host process and returns `agent.ErrDetached`.
  - `pkg/agent/antigravity` (`d44a2141f`) handles `agent.ErrDetached` (returning immediately without deleting the run's temporary `HOME` directory or counting a retry) and implements `agent.ResultParser` (`ParseResult`) so `ResumeAdoptedRun` can parse the final NDJSON stream from an adopted `agy` run's spool.
  - On daemon startup, `orchestrator.RecoverHostd` (`pkg/orchestrator/hostd.go`) calls `OpHello` and `OpProcesses`, runs `runhost.DecideHostd` against live `task_runs` rows with a `hostd:` cursor or matching `hostd` process, and:
    - Adopts still-running or newly finished processes via `ResumeAdoptedRun` (`AdoptHost`), reading the spool from byte 0 for `ParseResult` while teeing only bytes past the saved cursor to the live log, and sparing adopted runs in `RecoverOrphanedRunsExcept`;
    - Releases orphaned processes (`OpCancel`) and runs `recoverRun` for vanished rows.
  - A draining `hostd` refusal (`agent.ErrHostdDraining`) is recorded as `OutcomeInterrupted` (no failure strike, task re-queued).
  - `GET /api/cli-run` (`HostdStatus`) includes `protocol`, `build`, `updating` (draining), and `mismatch`, and `CLIRunLocations.jsx` shows a warning alert in Settings → Models when `hostd` is updating or mismatched.
- **Removal of host mode, `agent.ContainerHost`, and `auto` run location (`374341e32`, `1b06f6607`, `580d8c2d7`, `186e75fd0`, `b966975b4`, `f87bcfedd`):**
  - Supersedes `2026-10-23-morning.md` §3 (`3697762f8`). `GRAIN_INSTALL_MODE=host` is removed from `scripts/setup.sh` and a wrapper distribution's own installer (refused upfront with migration instructions). Every machine install is the release container plus `grain hostd` by default (`GRAIN_HOSTD=0` omits or removes `grain-hostd.service`).
  - `grain-hostd.service` carries `ExecCondition=/usr/local/bin/grain hostd -h` (`1b06f6607`) so rolling `/usr/local/bin/grain` back to a pre-hostd release (`v2.1.1`) cleanly skips `grain-hostd.service` instead of crash-looping.
  - Legacy host-mode installs upgraded via the UI Upgrade button onto this build start up once and refuse subsequent UI upgrades (`upgrade.Config.Refusal`) with the installer command that migrates them.
  - `agent.ContainerHost` (`pkg/agent/container.go`), `-cli-image` / `-cli-image-file` / `-cli-container-engine` (kept as no-op flags for one release), and the `auto` run location are removed (`580d8c2d7`, `186e75fd0`).
  - Valid run locations are now `container | host` (default `container` in container installs). On a bare-machine development daemon (`grain demo`, local `grain daemon`, `GRAIN_IN_CONTAINER=0`), only `host` is offered; `PUT /api/cli-run` refuses `container`, a stored `container` runs on the host with a warning in Settings and the log, and stored `auto` or `image` fields in `grain_config.cli_run` are stripped on read.

### What CI proves
- `pkg/runhost/hostdhost_linux_test.go`, `pkg/orchestrator/hostd_test.go`, `pkg/agent/antigravity/antigravity_test.go`, `pkg/daemon/clirun_test.go`, `pkg/daemon/clirun_hostd_test.go`, `pkg/daemon/upgrade_hostmode_test.go`, `pkg/whererun/whererun_test.go`, `tests/deploy/daemon_flags_test.go`, `tests/security/cli_run_test.go`.
- `tests/installer/zz_hostd_test.go` (live `fakeagy` `mode=steady` adoption across `systemctl restart grain-daemon.service`, `ExecCondition` pre-hostd binary check, `GRAIN_HOSTD=0` removal) and `tests/installer/hostmode/installer_test.go` (installing from `4cb28b7de` in host mode, UI-style binary upgrade, refusal of `GRAIN_INSTALL_MODE=host`, and in-place migration).
- Vitest `CLIRunLocations.test.jsx` and Playwright `ui/e2e/cli-run-locations.spec.js`.

### What is NOT proved
- **Adoption when a detached `antigravity` (`agy`) or `claude` process finishes WHILE the daemon is stopped:**
  - `zz_hostd_test.go` tests a process that is still running when the restarted daemon comes back up. It does NOT test the window where the detached host process finishes (exits 0 and writes its final result line to `hostd`'s spool while the daemon is down) before `RecoverHostd` runs on startup: verifying `DecideHostd` adopts the exited-with-spool process, `ResumeAdoptedRun` reads the full spool from byte 0, `antigravity.ParseResult` / `claude.ParseResult` extracts the final result, the task transitions to `completed` (without re-dispatching a second attempt!), and the spool directory and `task_run_spool_cursor` row are cleaned up.
- **Live draining `hostd` (`updating: true`) and protocol mismatch (`mismatch`) in a real browser and daemon dispatch loop:**
  - `d4cc808be` noted that `HostdStatus.updating` was only tested in Vitest because `grain demo`'s built-in hostd was not put into the draining state. Starting a real `grain hostd` on a unix socket, holding its drain open (`SIGUSR1` while a detached process runs, as in `445130a7d`), and attaching a daemon/UI server to that socket proves:
    1. `GET /api/cli-run` returns `hostd.updating: true` and Settings → Models (`CLIRunLocations.jsx`) renders the live updating warning in Playwright;
    2. Dispatching a new host-CLI task against the draining `hostd` hits `agent.ErrHostdDraining`, records the attempt as `interrupted` with 0 failure strikes, keeps the task queued, and dispatches cleanly once a fresh `hostd` replaces the socket.
  - Serving a stub `OpHello` with protocol `99` on the socket and verifying `GET /api/cli-run` (`hostd.mismatch`) and the Settings → Models warning in Playwright.
- **Legacy `grain_config.cli_run` migration (`auto` and `image` stripping) and bare-machine `container` warning end-to-end:**
  - Writing raw `{"claude":{"run":"auto","image":"ghcr.io/example/cli:v1"},"codex":{"run":"container"}}` into `grain_config.cli_run` in SQLite, booting the daemon in bare-machine mode (`GRAIN_IN_CONTAINER=0`) vs container mode (`GRAIN_IN_CONTAINER=1` with `-hostd-socket`), and verifying:
    - `auto` and `image` are dropped on read and removed from `grain_config` on the next save;
    - On the bare machine, `codex: container` shows the disabled `Container` option plus the daemon's warning in Settings → Models while running on the host, and `PUT /api/cli-run` refuses setting `container`.
- **Legacy host-mode upgrade refusal (`upgrade.Config.Refusal`) in the UI:**
  - Booting a daemon configured with `upgrade.Config.Refusal` (as a legacy host-mode install upgraded once onto this binary does) and verifying the System / upgrade endpoint and UI surface the exact migration command (`curl -fsSL .../setup.sh | sudo sh`) instead of running a second in-place binary upgrade.

### Walkthrough procedure
1. Run `git fetch origin && git branch -r --contains 374341e32 bd2d2fe1a d44a2141f 1b06f6607 580d8c2d7 186e75fd0`. If `origin/candidate` is not listed, stop and call `ask_question` naming the missing commits. Never merge `main` into `candidate`.
2. Build `grain` (`go build -o /var/tmp/grain ./cmd/grain`) and the frontend (`cd ui && npm ci && npm run build`).
3. Test detached completion while the daemon is down:
   - Start a real `grain hostd` on a temporary socket and a daemon (or orchestrator harness with SQLite store and `runhost.NewHostd`) running a fake `agy` (and `claude`) CLI.
   - Stop the daemon while the detached `agy` process is mid-run; let the `agy` process finish and write its final NDJSON result into `hostd`'s spool while the daemon is down; start the new daemon with `RecoverHostd` and assert the run finishes in attempt 1 with its transcript and result intact, `task_run_spool_cursor` is cleared, and `hostd`'s spool directory is removed.
4. Test draining `hostd` and protocol mismatch against a live UI + daemon:
   - Hold `grain hostd` in drain mode (`SIGUSR1` while a child process is running). Point `grain demo` / `grain daemon` at `-hostd-socket`.
   - Verify `GET /api/cli-run` (`hostd.updating: true`), open `/settings/models#cli-run` in Playwright to assert and screenshot the live updating warning, and verify a task dispatched during drain records `interrupted` (0 strikes) and succeeds after a fresh `hostd` takes over the socket.
   - Verify the `hostd.mismatch` warning when `OpHello` reports an unsupported protocol version.
5. Test legacy `cli_run` (`auto`, `image`, and bare-machine `container`) and `upgrade.Config.Refusal` over the API and Playwright.
6. Take desktop and phone screenshots (`make ui-screenshots PAGES=/settings/models OUT=screenshots/hostd-cli-run`), add durable Go/Playwright tests for any gaps or bugs found, and write `docs/test-runs/2026-10-23-hostd-adoption-and-two-run-locations.md`.

---

## 4. Cross-Framework Default Model Fallback and Interactive Framework-Switch Notice

**Commits (main only):** `cdf773d8f` (PR #4470)

### What changed
- **Per-model framework resolution in `default_model` (`pkg/daemon/agents.go`):**
  - Previously, an unpinned task (no explicit `model`, `connection`, or `agent_framework`) inherited the deployment's top-level `agent_framework` before `AgentPool.resolve` ran, which filtered `default_model` down to models on that single framework and caused tasks to wait whenever that framework's models were usage-limit paused—even if `default_model` listed models on another framework with available quota.
  - Each model in `default_model` now resolves its own framework (`defaultModelFramework`, from the connections serving it, falling back to `model.ModelFramework`). `agent_framework` only disambiguates a model served by multiple frameworks or drives rotation when `default_model` is empty.
  - When the earlier models in `default_model` (e.g. Gemini on `antigravity`) are usage-limit paused (`Pause.ModelPausedUntil`), unpinned tasks (including interactive tasks and standing console sessions) fall over to the next available model in `default_model` on another framework (e.g. `claude-opus-4-6` on `claude`), and rotation keys follow the chosen model's framework.
- **Pinned task wait reason (`pkg/daemon/agents.go`, `pkg/orchestrator/pause.go`):**
  - Tasks pinned to a specific `model`, `connection`, or `agent_framework` still wait when their pinned target is paused, and their wait activity message now ends with `"the task is pinned to <X>; it does not fall back"`.
- **Interactive framework-switch notice (`pkg/orchestrator/frameworkswitch.go`, `pkg/orchestrator/run.go`):**
  - When an interactive or console-session task runs a turn on a different framework than its previous turn because the primary model is paused (`BackendFramework.Fallback` / `orchestrator.FallbackFramework`), `ResumeTokenFor(framework)` returns empty so the new framework starts fresh from Grain's prompt + conversation thread, and `RunDispatch` posts a note in the conversation: `"This turn ran on <model> (<framework>) because <paused-model> is paused until HH:MM UTC"`.

### What CI proves
- `pkg/daemon/model_fallback_test.go` and `pkg/orchestrator/suspend_test.go`: Go unit tests covering `AgentPool` cross-framework selection, rotation keys, pinned task wait reasons, and `FallbackFramework` note formatting.

### What is NOT proved
- **End-to-end multi-turn interactive / console-session fallback across frameworks in a live daemon + UI (`TaskChat` / `ConsoleSessionPage`):**
  - Configuring `default_model: "gemini-3.1-pro,claude-sonnet-4-6"` with fake `antigravity` and `claude` CLI binaries:
    1. Turn 1 of an interactive task (or Director session) runs on `gemini-3.1-pro` (`antigravity`), saves an `antigravity` session resume token, and parks in `awaiting_reply`.
    2. Turn 2 triggers a usage-limit pause on `gemini-3.1-pro` (or `gemini-3.1-pro` is paused before the user replies). The daemon automatically dispatches Turn 2 on `claude-sonnet-4-6` (`claude`), does NOT pass the `antigravity` resume token to `claude`, passes the prior conversation transcript in the prompt, and posts `"This turn ran on claude-sonnet-4-6 (claude) because gemini-3.1-pro is paused until ... UTC"` in the thread.
    3. Opening the task / session in Playwright and asserting how the framework-switch notice and turn transcript render in `TaskChat` / `DetailOverlay` / `ConsoleSessionPage`.
    4. Turn 3 after `gemini-3.1-pro`'s pause expires: verifying whether Turn 3 returns to `gemini-3.1-pro` (`antigravity`) and how `ResumeTokenFor("antigravity")` behaves when an intervening turn ran on `claude`! (Wait: if Turn 1 saved an `antigravity` resume token, Turn 2 ran on `claude`, and Turn 3 runs back on `antigravity`, does `ResumeTokenFor("antigravity")` still hold Turn 1's stale resume token that misses Turn 2's exchange, or is it cleared/re-prompted? This is a critical multi-turn invariant to test!)
- **Interaction between §1 (standing session model pin) and §4 (pinned wait reason + `grain get` activity from `4b58f9867`):**
  - When a standing console session has `session.Model` pinned to `gemini-3.1-pro` (§1) and `gemini-3.1-pro` is paused, verifying the session's task does NOT fall back to `claude-sonnet-4-6`, and that both the UI (`DetailOverlay` / `ConsoleSessionPage`) and `grain get <id>` (`4b58f9867`) display `"waiting: ... the task is pinned to gemini-3.1-pro; it does not fall back"`.

### Walkthrough procedure
1. Run `git fetch origin && git branch -r --contains cdf773d8f`. If `origin/candidate` is not listed, stop and call `ask_question` naming `cdf773d8f`. Never merge `main` into `candidate`.
2. Build `grain` and the UI, and start a local test daemon (`-sandbox-port-base=0`) with fake `antigravity` and `claude` binaries and `default_model` set to `"gemini-3.1-pro,claude-sonnet-4-6"`.
3. Run a 3-turn interactive / console-session walkthrough:
   - Turn 1 on `gemini-3.1-pro` (`antigravity`, saving a resume token `agy-session-1`);
   - Pause `gemini-3.1-pro` (via a quota-exhaustion output on Turn 2's first attempt or `ModelPausedUntil`) and verify Turn 2 falls over to `claude-sonnet-4-6` (`claude`), receives the full conversation prompt without `agy-session-1`, and posts the `"This turn ran on claude-sonnet-4-6 (claude) because gemini-3.1-pro is paused until ... UTC"` note;
   - Expire the pause on `gemini-3.1-pro` and send Turn 3: inspect what `antigravity` receives on Turn 3. Verify that Turn 2's exchange on `claude` is not lost if `antigravity` resumes! If `ResumeTokenFor("antigravity")` would cause `antigravity` to resume from Turn 1's state and miss Turn 2, fix it so cross-framework turns clear or supersede stale resume tokens from earlier turns, and add a regression test in `pkg/orchestrator/suspend_test.go`.
4. Pin a task (and a standing console session) to `gemini-3.1-pro` while `gemini-3.1-pro` is paused; verify it waits rather than falling back and check the wait message in both Playwright and `grain get <id>`.
5. Take screenshots of the framework-switch notice in the task/session thread, and write `docs/test-runs/2026-10-23-cross-framework-model-fallback.md`.

---

## 5. Pinned Sidebar Navigation Sub-Targets, Console Session Restart Refresh, Live `WorkTree` Updates, `/director` Route, and `/processes` Demo Scenarios

**Commits:** `61707e025` (candidate, PR #4515), `f82f5ee81` (candidate, PR #4469), `cd2b49e44` (candidate, PR #4522), `95f0d3825` (main, PR #4424)

### What changed
- **Console session restart refresh, live `WorkTree` updates, and `/director` route (`61707e025` on `candidate`):**
  - `App.jsx` (`refreshSessionSaved`): awaits `refreshSessions()` before calling `loadSessionTask(nextTaskId)` and checks `openSessionTaskIdRef.current` after the fetch so restarting a session (`POST /api/console-sessions/{id}/restart`) immediately loads and renders the replacement task rather than racing with a stale fetch of the old closed task ID.
  - `WorkTree.jsx`: derives a dependency key from the tasks in the tree (`id`, `state`, `status`, `updatedAt`, `parentID`, `dependsOn`, `spawnedBy`) so the `WorkTree` component on `ConsoleSessionPage` re-fetches `/api/tasks/{id}/tree` whenever follow-up tasks are proposed or change state while the session stays in `awaiting_reply`, without flashing `null`.
  - `paths.js` and `App.jsx`: routes `/director` to `{ view: "session", sessionId: "director" }` and canonicalizes the browser address bar to `/session/director` via `history.replaceState` (without pushing an extra history entry).
- **Pinned sidebar navigation sub-target matching & session switch guard (`95f0d3825` on `main`):**
  - `CustomizePinnedOverlay.jsx`: resolves candidate pins with `pinFromTarget(addId)` and compares with `samePin(it, candidatePin)` so pinning a specific System tab (e.g. `System → Sandboxes`) or Settings section (e.g. `Settings → Credentials`) disables only that sub-target in the Add dropdown, leaving top-level `System` / `Settings` and other tabs/sections selectable; includes `item.tab` / `item.section` in React list keys.
  - `Sidebar.jsx` and `App.jsx`: normalizes the default System tab (`'health'`) and default Settings section (`'general'`) when matching active pins and navigating on desktop, so a pinned `System → Health` highlights on `/system` and navigates to `/system`, while bare `System` and `Settings` pins highlight across unpinned tabs/sections.
  - `App.jsx`: guards `<ConsoleSessionPage task={...}>` so switching between console sessions does not flash the previous session's task while the new session's task fetch is in flight.
- **`/processes` phone layout (`f82f5ee81` on `candidate`) and `grain demo` multi-scenario declaration merging (`cd2b49e44` on `candidate`):**
  - Under `:root[data-phone="true"]`, `.process-row` places the kind chip (`template` / `schedule`) and `Use…`/`Run…` button on a top handle line, giving the process name, title, and repository columns the full ~326px width at 390px viewport width instead of a cramped 110px column.
  - `syncDemoDeclarations` (`pkg/demo/demo.go`) merges incoming scenario declarations (`-merge-rules`, `-sensitive-hold`, `-mechanical-port`) with the templates, suites, schedules, settings, triggers, merge rules, and declaration read failure already stored for `acme/widgets` instead of replacing them on each `SyncRepoDeclarations` call.

### What CI proves
- On `candidate`: `ui/e2e/director.spec.js`, `ui/e2e/processes-phone-layout.spec.js`, `pkg/demo/demo_test.go`, `ui/src/App.test.jsx`, `ui/src/components/detail/WorkTree.test.jsx`, `ui/src/paths.test.js`.
- On `main`: `ui/e2e/sidebar-navigation.spec.js`, `ui/src/App.test.jsx`, `ui/src/components/CustomizePinnedOverlay.test.jsx`, `ui/src/components/Sidebar.test.jsx`.

### What is NOT proved
- **Combined behavior and merge cleanliness of `61707e025` (`candidate`) and `95f0d3825` (`main`) in `ui/src/App.jsx`:**
  - Both commits edited the `<ConsoleSessionPage task={...}>` prop in `ui/src/App.jsx` independently on `candidate` and `main`. When `95f0d3825` is ported or cut onto `candidate`, or when switching rapidly between two standing sessions (`/session/director` <-> `/session/triage`) immediately after restarting one of them, both the `openSessionTaskIdRef` guard (`61707e025`) and the session-switch guard (`95f0d3825`) must hold together without flashing stale messages or dropping the new session's task.
- **Pinning standing sessions (`session:director`), `/director` navigation, and System/Settings sub-targets together:**
  - Pinning both bare `System` and `System → Sandboxes` (and bare `Settings` and `Settings → Credentials`) in `CustomizePinnedOverlay`, then navigating across `/system`, `/system/sandboxes`, `/system/executors`, `/settings`, `/settings/general`, `/settings/credentials`, and `/director` (at both desktop 1440px and phone 390px viewports), asserting:
    1. Only the more specific pin highlights when on `/system/sandboxes` or `/settings/credentials`, and the bare `System`/`Settings` pin highlights on `/system` (`/system/health`) and `/system/executors`;
    2. Browser Back/Forward across `/director` -> `/session/director` -> `/processes` does not get trapped by `replaceState`;
    3. `localStorage` persistence of sub-target pins survives reload and handles malformed/duplicate sub-target entries cleanly.
- **Live `WorkTree` multi-hop updates on `ConsoleSessionPage`:**
  - When a standing session's child task spawns a grandchild task (`depends_on` chain) or transitions from `in_progress` -> `blocked` -> `completed` while the user is viewing `/session/director`, verifying the `WorkTree` disclosure updates live and clicking a child node in `WorkTree` opens `DetailOverlay` over the session page without losing the session's scroll position or draft reply.
- **`/processes` phone layout and `/repos/acme/widgets` with combined `-merge-rules -sensitive-hold -mechanical-port` demo scenarios (`f82f5ee81`, `cd2b49e44`):**
  - Running `grain demo -merge-rules -sensitive-hold -mechanical-port` and verifying in Playwright that `/processes` and `/repos/acme/widgets` display the merged union of all seeded templates, suites, schedules, merge rules, triggers, and the `.grain/ declarations error` banner simultaneously, and that all schedule and template rows on `/processes` render without horizontal overflow or button overlap at `390x844` in light and dark themes.

### Walkthrough procedure
1. Check `git fetch origin && git branch -r --contains 61707e025 f82f5ee81 cd2b49e44`. Also check `git branch -r --contains 95f0d3825`: if `95f0d3825` has not yet been ported or cut onto `origin/candidate`, cherry-pick `95f0d3825` (`Backported-from: 95f0d382548b88375ff67db41860890e03a87bfd`) onto the task branch and resolve the `<ConsoleSessionPage task={...}>` conflict in `ui/src/App.jsx` cleanly so `candidate` has both fixes!
2. Build the frontend (`cd ui && npm ci && npm run build`) and run `grain demo -addr 127.0.0.1:0 -open=false -merge-rules -sensitive-hold -mechanical-port`.
3. In Playwright:
   - Open `CustomizePinnedOverlay`, pin `System`, `System → Health`, `System → Sandboxes`, `Settings`, `Settings → General`, and `Settings → Credentials`. Assert the Add dropdown disables only the already-pinned sub-targets, and assert active-pin highlighting and URL canonicalization across desktop (`1440x900`) and phone (`390x844`).
   - Navigate to `/director`, assert `location.pathname === "/session/director"` and `history.length` does not grow, switch rapidly between `/session/director` and a second session while restarting one of them, and assert no stale task flash occurs.
   - Trigger live child and grandchild task state transitions while on `/session/director` and assert `WorkTree` updates in place and opens `DetailOverlay` cleanly when a child task is clicked.
   - Inspect `/processes` and `/repos/acme/widgets` with `-merge-rules -sensitive-hold -mechanical-port` enabled, asserting all merged templates, schedules, suites, merge rules, and declaration errors are visible in the UI, and verify `/processes` at `390x844` across both template rows (`Use…`) and schedule rows (`Run now`, `Pause`/`Resume`, long titles) with bounding-box assertions for horizontal overflow and button overlap.
4. Run `make ui-screenshots PAGES=/director,/sessions,/processes,/system,/settings/credentials OUT=screenshots/sessions-and-pinned-nav`, inspect all desktop and phone screenshots, and write `docs/test-runs/2026-10-23-sessions-worktree-and-pinned-nav.md`.

---

## 6. External Configuration Repository (Read-Only IaC) Boundary Hardening, `StateRepoPullRequestClient` Complete Method Guard, and Strict Git Branch Validation

**Commits (candidate):** `20482a321` (PR #4494), `ad2b57700` (PR #4502), `2e25d02d5` (PR #4510), `f6d41ae41` (PR #4519)

### What changed
- **Read-only IaC mutator guards (`20482a321`, `ad2b57700`, `f6d41ae41`):**
  - `checkSettingsWritable(ctx)` now guards every settings-backed mutator before any side effect (store write, SQLite sequence increment, credential file write, or stopping a live console session):
    - Added in `20482a321`: `DeleteSchedule`, `PutTeamAgentBackend`, `DeleteTeamAgentBackend`, `AddTeamGitConnection`, `DeleteTeamGitConnection`, `SetTeamRepoGitConnection`, `SetRepoReleaseSecret`, `DeleteRepoReleaseSecret`, and moved to the top of `CreateTemplate`, `UpdateTemplate`, `CreateSuite`, `UpdateSuite`, `DeleteSuite`, `CreateBoard`, `UpdateBoard`, `DeleteBoard`, `UpdateConsoleSession`, `DeleteConsoleSession`, and standing-work pre-auth helpers.
    - Added in `ad2b57700`: `SetGCPKeyWithUser` (`POST /api/gcp-keys`, `PUT /api/gcp-keys/{name}`), `DeleteGCPKeyWithUser` (`DELETE /api/gcp-keys/{name}`), and moved to the entry of `PutQualificationPlan`, `SetRepoWorkflows`, and `SetDeploymentRepoConfigWithUser`.
    - Added in `f6d41ae41`: guards the default `GitConnections` auto-bootstrap inside `PUT /api/github-tokens/{name}` with `checkSettingsWritable(ctx) == nil` so storing a host-local GitHub token in external IaC mode succeeds for the host secret file without mutating the repo-managed `grain_config` row.
- **`StateManager.noteOutcome` mutex deadlock fix (`ad2b57700`):**
  - `StateManager.noteOutcome` (invoked while `StateManager.cycle` already holds `m.mu`) now calls `settingsRepoLocked` / `externalConfigRepo(false)` instead of re-acquiring `m.mu`, preventing a self-deadlock when `dataDir` is empty or `state-repo.json` fails to parse.
- **`StateRepoPullRequestClient` and local `grain/config` isolation (`20482a321`, `ad2b57700`, `2e25d02d5`, `f6d41ae41`):**
  - `StateRepoPullRequestClient` (`pkg/daemon/statepullrequests.go`) no longer embeds `github.Client`. `Client` is an explicit struct field with compile-time interface assertions (`github.Client`, `github.PullRequestQueuer`, `github.MergeRulesReader`), and all 11 previously unoverridden methods (`DeleteBranch`, `SetDefaultBranch`, `ListPullRequestCommits`, `ListIssues`, `GetIssue`, `AddLabel`, `RemoveLabel`, `CloseIssue`, `ReopenIssue`, `UpdateIssue`, `CreateIssue`) plus `EnqueuePullRequest`, `MergeQueueEntryOf`, and `MergeRequirements` are explicitly implemented or refused with `404` (`errExternalConfigRepoNoNativePR`) when targeting `grain/config` in external mode.
  - `GitProxy.HandleRequest`, `CiWorkflowSource.clone`, and `StateCheckSandboxes.localRepo` check `localConfigRepoEnabled(ConfigRepoDir)` so local `grain/config` clones, workflow reads, and state-check sandboxes are refused in external mode, and local `grain/config` clones never receive forge credentials or `ServerGitRemoteFor`.
- **Strict branch name validation (`2e25d02d5`, `f6d41ae41`):**
  - `staterepo.ValidateBranch`, `localRef`, `StateManager.Compare`, `secretsrepo.ValidateBranch`, `secretsrepo.Open`, `grain state repo`, and `grain secrets repo` verify that `git check-ref-format --branch <name>` exits 0 AND echoes the exact input branch name (rejecting `@{-1}` reflog shorthands, leading `-` flags, `..`, control chars, and `.lock` suffixes) before writing `state-repo.json` or `secrets-repo.json`.

### What CI proves
- `pkg/daemon/state_settings_repo_test.go`, `pkg/ui/staterepo_test.go`, `pkg/gitproxy/local_test.go`, and Playwright `ui/e2e/config-repo-iac-modes.spec.js`.

### What is NOT proved
- **Playwright & live-daemon E2E coverage for the three post-walkthrough review commits (`ad2b57700`, `2e25d02d5`, `f6d41ae41`):**
  - `ui/e2e/config-repo-iac-modes.spec.js` was authored in `20482a321` before `ad2b57700`, `2e25d02d5`, and `f6d41ae41` landed. None of the three review-fix commits updated `ui/e2e/config-repo-iac-modes.spec.js`!
  - Specifically unproved in live E2E:
    1. **`PUT /api/github-tokens/{name}` in external mode (`f6d41ae41`):** storing a host-local GitHub token while in external config-repo mode must succeed (`200 OK`) and persist the token on disk while leaving `grain_config.git_connections` unmutated.
    2. **Newly guarded endpoints in external mode (`ad2b57700`):** `POST /api/gcp-keys`, `PUT /api/gcp-keys/{name}`, `DELETE /api/gcp-keys/{name}`, `PUT /api/qualifications`, `PUT /api/repos/{owner}/{name}/workflows`, and `PUT /api/repos/{owner}/{name}/config` all returning `409 Conflict` with no store/disk mutation.
    3. **Reflog shorthand (`@{-1}`) and invalid branch rejection across HTTP & CLI (`2e25d02d5`, `f6d41ae41`):**
       - Calling `PUT /api/system/state-repo`, `GET /api/system/state-repo/compare?base=@{-1}&head=main`, `PUT /api/system/secrets-repo`, `grain state repo -remote ... -branch '@{-1}'`, and `grain secrets repo -remote ... -branch '@{-1}'` (as well as `--help`, `HEAD`, `a..b`, `branch.lock`) and asserting every one is rejected with `400` / non-zero exit without modifying `state-repo.json` or `secrets-repo.json`.
       - Placing a hand-crafted `secrets-repo.json` or `state-repo.json` with `"branch": "@{-1}"` on disk and verifying `secretsrepo.Open` / `StateManager` refuses it cleanly without executing git against a resolved reflog entry.
    4. **`StateManager.noteOutcome` under sync failure with corrupted `state-repo.json` (`ad2b57700`):**
       - Corrupting `state-repo.json` on disk (or pointing the external remote at a non-existent path) while the daemon sync loop runs and verifying `StateManager.cycle` records the outcome error in `/api/system/state-repo` without hanging or deadlocking `m.mu`.
    5. **`CiWorkflowSource` and `StateCheckSandboxes` in external mode (`f6d41ae41`):**
       - Triggering a CI workflow read or state-check for `grain/config` while external mode is active and verifying it is refused locally rather than cloning the internal `grain/config` repo or forwarding forge credentials.

### Walkthrough procedure
1. Confirm `5b0651b93` (`20482a321`, `ad2b57700`, `2e25d02d5`, `f6d41ae41`) is on `origin/candidate` (it is already merged).
2. Build `grain` (`go build -o /var/tmp/grain ./cmd/grain`) and the frontend (`cd ui && npm ci && npm run build`).
3. against a live daemon / `ui/e2e/config-repo-iac-modes.spec.js`:
   - In external config-repo mode, test `PUT /api/github-tokens/e2e-token` and assert `200 OK`, token stored in host secret store, and `grain_config.git_connections` unchanged.
   - Test `POST /api/gcp-keys`, `PUT /api/gcp-keys/default`, `DELETE /api/gcp-keys/default`, `PUT /api/qualifications`, `PUT /api/repos/acme/widgets/workflows`, and `PUT /api/repos/acme/widgets/config` in external mode and assert `409 Conflict` with zero side effects.
   - Test `@{-1}`, `--upload-pack=sh`, `a..b`, and `foo.lock` branch names against `PUT /api/system/state-repo`, `GET /api/system/state-repo/compare`, `PUT /api/system/secrets-repo`, `/var/tmp/grain state repo`, and `/var/tmp/grain secrets repo`.
   - Corrupt `state-repo.json` during a live sync tick and verify `/api/system/state-repo` responds promptly (no `m.mu` deadlock).
   - Exercise `CiWorkflowSource` and `StateCheckSandboxes` against `grain/config` in external mode and verify no upstream GitHub leak or local internal clone occurs.
4. Expand `ui/e2e/config-repo-iac-modes.spec.js` (and Go tests where appropriate) to lock in these checks, and record the results in `docs/test-runs/2026-10-23-external-config-repo-hardening.md`.

---

## 7. CI `capabilities: []` Opt-Out, Scarce Grant Admission & IAM 429 Backoff, Guest Timeout Ceiling, Shared Fault Ownership, and CLI Inspection

**Commits (`candidate` & `main`):** `966e6d7db`/`3636faf07`, `ebb78c544`/`a28d532e4`, `a6d4666c9`/`f301b8768`, `01d598d5f`/`22fffcf57`, `70c0de4e6`, `8153efad6`, `99ac96f96`/`ac8918fbf`, `4b58f9867`, `fe47c169c`, `943ba96f9`

### What changed
- **CI `capabilities: []` opt-out and scarce capability opt-in (`966e6d7db`/`3636faf07`, `ebb78c544`/`a28d532e4`, `a6d4666c9`/`f301b8768`, `01d598d5f`/`22fffcf57`):**
  - `pkg/ci/yaml.go` distinguishes `capabilities: []` (`Job.NoCapabilities = true`) from an omitted `capabilities:` key. A job with `capabilities: []` receives none of the repository's default CI capabilities (only granted `optional-capabilities`).
  - When `capabilities:` is omitted, `jobCapabilities` (`pkg/ci/requirements.go`) excludes capabilities reported by `ExplicitOnlyCapability` (`model.IsScarceResourceProvider`: `gcp-key`, `gcp-key:<name>`, `gemini-key`), which a job only receives by explicitly naming them.
  - Every job in `.grain/workflows/tests.yml`, `nightly.yml`, `post-reset-e2e.yml`, `kvm-route-stress.yml`, `backport-triage.yml`, `image.yml`, and `*-surface.yml` now declares `capabilities: []`; `gcp-smoke.yml` declares `capabilities: [gcp-key]`.
  - `pkg/capability/gcpkey`: IAM `429` (`RATE_LIMIT_EXCEEDED`) and 10-key cap exhaustion on `CreateKey` return `model.ResourceExhaustedError` with a jittered 30–60s `RetryAfter`, queuing the job instead of erroring the CI run or retrying every 5s across every account; `Revoke` retries rate-limited deletes 4 times over ~30s and stops on persistent 429s so `Reap` cleans up later.
- **Guest step timeout ceiling (`70c0de4e6`):**
  - `granuleSandbox.RunShell` (`pkg/orchestrator/shell.go`) rounds `req.Timeout` up to whole seconds with `math.Ceil(req.Timeout.Seconds())` instead of truncating with `int(req.Timeout.Seconds())`, and `pkg/ci/engine.go` checks `stepCtx.Err() == context.DeadlineExceeded` directly, preventing guest `timeout(1)` from killing a step up to `0.999s` before `stepCtx` expires and caching a job timeout as `ConclusionFailure` instead of `ConclusionCancelled`.
- **Shared fault ownership cache invalidation & merge-queue base-move tracking (`8153efad6`, `99ac96f96`/`ac8918fbf`):**
  - `pkg/ui/cifault.go` keys the 30s `CISharedFaults` cache by the recent failed runs fingerprint and re-evaluates `cifault.ResolveOwner` on cache hits so blocked-owner promotions appear immediately in `wait_for_checks` and `get_ci_log`.
  - `pkg/orchestrator/mergequeueheads.go` skips failed or merge-blocked fault owners so promoted queue heads and fault owners immediately receive `PriorityMergeHead`.
  - `rearmBlockedOnBaseMove` (`pkg/orchestrator/inherited.go`) records `MergeQueueRepairBase` and `blockMergeQueueAgainst` preserves base moves that occur while a re-armed CI run is in flight (`re-arm 2 of 2`); `closeIfLanded` (`pkg/orchestrator/landed.go`) closes port PRs (`LinkPortOf`) whose non-merge commits all carry `Backported-from` / `cherry picked from` footers on the base branch.
- **CLI & sandbox backend fixes (`4b58f9867`, `fe47c169c`, `943ba96f9`):**
  - `grain get <id>` (`pkg/cli/taskprint.go`) prints `waiting: <Task.Activity>` in human-readable output when a queued task is waiting on quota or capability exhaustion.
  - `grain settings` (`pkg/cli/settingsprint.go`) prints all 13 Kubernetes sandbox settings in human-readable output; `availableSandboxBackends()` preserves startup `none` and `host` availability when switching to `kubernetes` / `kubernetes-granule`; `SandboxesSection.jsx` renders a disabled empty `<option value="">` when `sandboxBackend` is unset.
  - `freeAlignedBlock` (`pkg/cli/portalloc.go`) randomizes the starting block and probes wildcard (`:port`) as well as `127.0.0.1:port`.

### What CI proves
- `pkg/ci/no_capabilities_test.go`, `pkg/ci/requirements_test.go`, `pkg/capability/gcpkey/ratelimit_test.go`, `pkg/orchestrator/shell_internal_test.go`, `pkg/orchestrator/inherited_failures_test.go`, `pkg/orchestrator/landed_test.go`, `pkg/ui/cifault_test.go`, `pkg/orchestrator/mergequeueheads_test.go`, `tests/e2e/shared_ci_fault_ownership_walkthrough_test.go`, `pkg/cli/ci_capacity_test.go`, `pkg/cli/settings_sandbox_shape_test.go`, `ui/e2e/k8s-sandbox-settings-walkthrough.spec.js`, `ui/e2e/waiting-tasks-and-questions-walkthrough.spec.js`.

### What is NOT proved
- **End-to-end `ci.Engine` execution combining `capabilities: []`, omitted `capabilities:`, explicit `[gcp-key]`, and IAM 429 `ResourceExhaustedError` retry:**
  - The unit tests in `pkg/ci/no_capabilities_test.go` and `pkg/ci/requirements_test.go` test `jobCapabilities` in isolation, and `ratelimit_test.go` tests `gcpkey` in isolation. What is not yet proved end-to-end through `ci.Engine` + `daemon` admission is a multi-job workflow where:
    - Job 1 declares `capabilities: []` -> runs immediately without minting any repo capability;
    - Job 2 omits `capabilities:` -> receives the repo's non-scarce capability (`package-publish`) and runs immediately without touching `gcp-key`;
    - Job 3 declares `capabilities: [gcp-key]` -> hits a simulated IAM 429 (`ResourceExhaustedError`), stays pending/queued while Jobs 1 and 2 finish green, and then succeeds on retry without tainting the commit's cached verdict as `errored`.
- **End-to-end fractional guest step timeout cancellation (`70c0de4e6`) through `ci.Engine` + verdict cache:**
  - Running a real CI job whose `timeout-minutes` expires with a fractional second remaining (e.g. `1.1s` left when a step starts) where the guest step traps `SIGTERM` and exits `143` (or `1`) within `5ms` of guest `timeout(1)` firing: verifying `ci.Engine` records the step and job as `ConclusionCancelled` and does NOT write a cached `ConclusionFailure` for the job's content fingerprint.
- **Combined interaction of `99ac96f96` (base move during re-armed CI run) and `8153efad6` (shared fault ownership promotion):**
  - Two PRs in the merge queue sharing a broken test when `candidate` moves during PR #1's first re-armed CI run: verifying PR #1 gets its second re-arm (`MergeQueueRepairBase`), and if PR #1 then exhausts its re-arms (`merge_failed`), PR #2 immediately promotes to shared-fault owner in `CISharedFaults` and receives `PriorityMergeHead`.
- **CLI human-readable output round-trip (`grain get` waiting activity + `grain settings` K8s fields):**
  - Running the compiled `grain` CLI binary against a live daemon to verify `grain get <id>` prints `waiting: ...` when a task waits on a scarce capability or model pause, and `grain settings` prints all 13 Kubernetes settings after toggling backends between `host`, `kubernetes`, and `kubernetes-granule`.

### Walkthrough procedure
1. Confirm `70c0de4e6`, `8153efad6`, `4b58f9867`, `fe47c169c`, `01d598d5f`, `966e6d7db`, `a6d4666c9`, `99ac96f96` are on `origin/candidate` (they are already merged).
2. Build `grain` (`go build -o /var/tmp/grain ./cmd/grain`).
3. Write and run an integration/E2E walkthrough exercising:
   - A 3-job workflow (`capabilities: []`, omitted `capabilities:`, and `capabilities: [gcp-key]`) through `ci.Engine` with a scarce `gcp-key` provider that returns a 429 `ResourceExhaustedError` on first mint and succeeds on retry, asserting Job 1 and Job 2 never call `gcp-key.Mint` and Job 3 queues and completes `ConclusionSuccess`.
   - A `ci.Engine` job with a fractional remaining deadline (`~1.1s`) running a shell step that exits promptly on `SIGTERM`, asserting `ConclusionCancelled` and asserting the verdict cache holds no `ConclusionFailure`.
   - The combined `MergeQueueRepairBase` (`99ac96f96`) + `CISharedFaults` promotion (`8153efad6`) sequence across two queued PRs.
   - Live `/var/tmp/grain get <id>` and `/var/tmp/grain settings` CLI output assertions against a running daemon.
4. Add durable Go integration tests for any uncovered path or bug found, and record the full run in `docs/test-runs/2026-10-23-ci-capabilities-timeouts-and-queue.md`.

---

## Changes Not Requiring a Separate Task

- **`47533c170` (`candidate`) / `4a78ddcf4` (`main`):** bumps the Go toolchain pin from `go1.26.9` to `go1.26.10` across `go.mod`, `Dockerfile`, `Dockerfile.build`, and `Granulefile` for standard-library advisory `GO-2026-6809` (`crypto/x509`). Verified by `pkg/hostinit/goversion_test.go`, `tests/deploy/go_build_targets_test.go`, `tests/deploy/promote_images_test.go`, and `make vuln`.
- **`9f8972d18` (`candidate`):** removes the orphaned `multiteam-stress` job from `.grain/workflows/nightly.yml` and `Makefile`.
- **`02050c93d` (`candidate`) / `e9338449e` (`main`):** gates `client.Reopen` on `creating.Load()` in `TestLoadSustainedConcurrency` (`tests/e2e/loadtest_helpers_test.go`) so load-test writers stop reopening closed tasks during the drain phase.
- **`ed24aa657` (`candidate`):** rebalances `security-audit` and `ui-e2e-long` timeouts in `.grain/workflows/nightly.yml` on `candidate`, verified by `TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence`.
- **`bbb783d92`, `d9fb2a870`, `5fa5f1f48`, `a612b0b12`, `ee406783b` (`main`):** test fixture and formatting updates (`{slug}` branch template expectation in `installer_test.go`, Prettier on frontend test files, `gofmt` on `capability_server_config_test.go`, and `"v2-refactored-with-fix"` in `landed_test.go` for `tests/deploy/v2_test.go`).
- **`703ce9c32`, `6ecdf7289` (`candidate`) and `92ea4ada2`, `f87bcfedd` (`main`):** the `2026-10-23-morning.md` test plan and test-run report/screenshot commits.
