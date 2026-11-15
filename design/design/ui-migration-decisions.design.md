# UI migration: operator decisions before retiring classic

**Decision record, not an implementation plan.** Audited 2026-11-15 against
`347a4f5f7fa494354bec9bd1ecc9afb2fc3c6b6a`. Recommendations below do not
authorize restorations, removals, or follow-up task filing. Every operator
decision is intentionally blank. Retain classic access while decisions remain open.

## Executive decisions

1. **Bring forward first:** correct active-upgrade cancellation, preserve rollback
   and target warnings, restore declaration revocation, and make authenticated
   capability-server setup usable (G1–G4). These affect recovery and authority.
2. **Bring forward next:** scope-set inspection, essential sandbox bootstrap
   controls, scheduled-task execution options, and desktop notifications (G5–G8).
3. **Choose the supported board experience:** recommend keeping saved filters and
   the new timeline, leaving the old multi-layout dashboard editor behind (G9).
   Existing non-list boards need an explicit fallback before classic retirement.
4. **Accept selective simplification:** recommend leaving task-level continuous
   runs, host power-off/reboot, and advanced CI retention forms behind; retain
   their supported API/config workflows (G10, G13, G14).
5. **Resolve deployment-specific demand:** confirm whether non-git change targets
   are supported deployments for this migration (G12). This is the one gap whose
   useful disposition depends on unavailable edition/deployment information.
6. **Do not duplicate active work:** Task #26 covers Codex setup and connection
   tests; Task #27 covers Director scrolling and composer keys. See status below.

Recommendations use operator value, recovery risk, overlap with newer workflows,
and maintenance surface. No feature-usage analytics were available: low usage is
**not** asserted. Priority is impact, not an instruction to implement everything.

## Comparison scope and evidence rules

Classic is React in `ui/src/`, served at `/`; new is Angular in
`ui-ng/src/app/`, served at `/next/`. Both remain shipped, sharing
`/api/...` (`pkg/ui/server.go`, `frontendHandler`). Here “missing” means missing
from Angular, not removed from the deployment or backend.

Read `PROMPT.md` (no `AGENTS.md` found), the design-folder conventions in
`design/playbook/contributions.playbook.md`, and
`docs/design-docs.md`. Migration intent comes from
[angular-ui.design.md](angular-ui.design.md) and
[ui-design-book.design.md](ui-design-book.design.md).
Settings intent was checked against
[settings.design.md](settings.design.md),
[settings-panel.design.md](settings-panel.design.md),
[config-repo-settings.design.md](config-repo-settings.design.md), and the relevant
sections of `design/spec/settings/settings.spec.md` and
`design/spec/settings/settings-mutability.spec.md`.

Compared route registration (`ui/src/paths.js`, `App.jsx`,
`ui-ng/src/app/app.routes.ts`, `shell/nav.ts`), rendered controls and handlers,
request payloads, and backing server routes/handlers. Searched Angular callers
excluding generated route declarations, types and tests: a route's presence in
`api/generated-routes.ts` is not working UI wiring. Used git history to check
migration context and recent restorations, including the Codex changes at this
revision. Current classic source is the reproducible old baseline; no historical
old UI source needed for these findings was unavailable. This does not reconstruct
every pre-single-tenant or edition-private UI from history.

**Validation limits:** static implementation audit, not a live browser walkthrough
or execution of disruptive APIs. “Verified” means a concrete classic control and
new handler/payload or absence were traced, not that an interaction was exercised
against a production deployment. Existing tests were read as supporting evidence;
UI tests were not run for this document-only change. Deployment configuration and
private change-target implementations are outside this checkout.

All API registrations cited below are in `pkg/ui/server.go` unless another file
is named. **No documented removal rationale was found for G1–G15** in the migration
and settings material reviewed; do not mistake a recommendation to leave a feature
behind for a previously approved removal. Deliberate config-write restrictions
and older documented removals are separated below.

Effort is comparative: **S** = localized control/caller using an existing API;
**M** = form/state/permissions and workflow tests; **L** = several renderers or
integration-specific flows. These are not delivery estimates.

## Highest-impact verified gaps

### G1 — Active-upgrade cancellation is wired to the wrong action (P1, unfinished wiring)

- **Old:** Settings → Upgrade, `ui/src/components/UpgradePanel.jsx`,
  `cancelCurrent`, sends `DELETE /api/upgrade/current` with a reason.
- **New:** `/next/settings/upgrade`,
  `settings/settings-instance.component.ts`, `cancelUpgrade`, sends
  `DELETE /api/upgrade/scheduled?reason=operator_cancelled` and locally clears
  active state with “Cancelled active upgrade”.
- **API evidence:** `pkg/ui/upgrade.go`, `handleCancelScheduledUpgrade` only
  clears the stored pending schedule (success is also returned if none exists).
  `handleCancelCurrentUpgrade` is the distinct live cancellation path.
- **Impact:** an operator can be told an active upgrade was cancelled while it
  continues toward cut-over. Scheduled cancellation itself is retained.
- **Recommendation: bring forward.** Correct behavior and truthful feedback are
  essential recovery controls. **S**, existing endpoint; test active versus
  scheduled cancellation and refresh from the returned status.
- **Operator decision:** ____________________

### G2 — Upgrade recovery, target selection and policy are reduced (P1, partial migration)

- **Old:** `UpgradePanel.jsx` provides previous-image/commit rollback,
  explicit image targeting, edition-switch losses/confirmation, retracted-release
  and discarded-RC notices, progress, and automatic upgrade policy controls.
  `components/settings/UpgradeSection.jsx` hosts this under `/settings/upgrade`.
- **New:** `settings-instance.component.ts` provides branch selection,
  immediate/drain start, release notes, database summary, externally managed
  deployment notice, and upgrade-check mode. `startUpgrade` posts only
  `branch` and `afterTasksFinish`; there is no rollback or explicit image
  action, nor forms for `autoUpgradeOn/Channel/Time/MinIntervalHours` or
  `upgradeCheckPausesPipeline`. Labels in `settings-sections.ts` are not controls.
- **API evidence:** `POST /api/upgrade` supports `rollback: true` and image
  targets; `GET /api/upgrade` carries previous target, warnings and progress
  (`pkg/ui/upgrade.go`). Policy remains in `/api/settings`
  (`pkg/ui/settings.go`; settings spec §§3.6, 3.10).
- **Impact:** choosing a branch is not equivalent to restoring the exact prior
  build. Operators also lose visibility/control of recurring upgrade policy.
- **Recommendation: bring forward** rollback, backend safety notices and policy
  visibility/editing. Keep arbitrary image/edition changes behind an advanced
  disclosure with the original safeguards, rather than silently dropping them.
  **M**, existing APIs; conditional behavior depends on source/image deployment
  and external management. Do not enable self-upgrade where the backend disables it.
- **Operator decision:** ____________________

### G3 — Capability-server configuration and credential lifecycle are materially reduced (P1)

- **Old:** `/settings/capabilities`, `CapabilityServersPanel.jsx` offers editing
  command/args/env, HTTP headers, host run location, secret binding, server-specific
  config/secret fields, managed-secret rotation and grant-holder usage.
  It tests unsaved configuration as well as saved connections.
- **New:** `/next/settings/capabilities`,
  `settings-work.component.ts` lists ID/endpoint/state and supports saved-server
  Test, enable/disable/remove. `saveCapServer` sends only
  `{id, config: {serverUrl}}` or `{id, config: {command}}`.
  The secret vault exists separately, but there is no binding/config editor.
- **API evidence:** `PUT /api/capability-servers`,
  `POST /api/capability-servers/{test,bind-secret,rotate-managed}`,
  `GET /api/capability-servers/{usage,host-entries}`; classic callers above,
  registrations in `server.go`. Secret storage alone cannot construct a header
  or environment binding.
- **Impact:** common authenticated or hosted MCP connections cannot be fully
  configured/tested in Angular. Removing/editing one loses classic's holder
  context. Reusing an ID in the add form is not an equivalent populated editor.
- **Recommendation: bring forward** the generic connection/secret lifecycle and
  holder context. Avoid a second, incompatible configuration model. **M–L**,
  existing APIs but transport-specific forms, write guards and secret handling.
- **Operator decision:** ____________________

### G4 — Accepted repository privileges cannot be revoked in Angular (P1)

- **Old:** `DeclarationAcceptancesPanel.jsx` lists acceptances and offers
  Revoke for accepted entries in capability settings.
- **New:** `/next/processes/declarations`,
  `processes-page.component.ts` shows requests/scopes/accepted-by and Accept;
  accepted rows become an “Accepted” pill with no Revoke.
- **API evidence:** `GET /api/declarations`,
  `POST /api/declarations/{accept,revoke}`;
  `pkg/ui/declaration_acceptance.go` has separate accept/revoke handlers.
  `processes.store.ts` calls accept only.
- **Impact:** an operator can authorize repeated privileged declared work but
  must return to classic/API to withdraw that acceptance. Disabling declarations
  for an entire repository is a much broader action.
- **Recommendation: bring forward.** Revocation completes the authority lifecycle.
  **S–M**, existing endpoint, same caller-scope checks, confirmation and refreshed state.
- **Operator decision:** ____________________

### G5 — Scope sets lose inspection and safe maintenance (P1)

- **Old:** `ApiScopeSetsPanel.jsx` shows actual scopes, includes, repo restrictions,
  deny rules, requires-person, description, deprecated/version metadata; populated
  edit and deletion include a holder-usage warning.
- **New:** `settings-work.component.ts` displays name plus scope count and a
  name/comma-separated-scopes form; `saveScopeSet` submits only
  `{set: {name, scopes}}`. No populated advanced editor, usage or delete action.
- **API evidence:** `GET/PUT /api/api-scope-sets`,
  `GET /api/api-scope-sets/usage`, `POST /api/api-scope-sets/delete`;
  `pkg/ui/api_scope_sets.go`.
- **Impact:** operators cannot inspect the effective restriction model from this
  UI or safely maintain existing rich sets. Do not assume omitted fields are
  preserved by manually re-entering an existing name; that workaround is not parity.
- **Recommendation: bring forward** full inspection and safe edit/delete, since
  the backend still supports this privilege abstraction. **M**, existing APIs;
  respect config-managed mode and requires-person semantics.
- **Operator decision:** ____________________

### G6 — Sandbox bootstrap and capacity tuning are incomplete (P1)

- **Old:** `components/settings/SandboxesSection.jsx` exposes Kubernetes pod/VM
  images, node selector, service account, privileged mode and workspace/runtime
  fields; disk overcommit/floor, host memory reserve, and auto-overcommit
  bounds/pressure targets, alongside basic resources/backend/network.
- **New:** `/next/settings/sandboxes`,
  `settings-compute.component.ts` retains backend, CPU/RAM/disk, network, custom
  image, overcommit mode and fixed CPU/memory ratios, Kubernetes namespace and
  NetworkPolicy assertion. The listed advanced controls have no rendered forms.
- **API evidence:** `GET/POST /api/settings`, `pkg/ui/settings.go`;
  settings spec and mutability spec identify sandbox infrastructure as bootstrap
  settings. New `overcommitMode` controls do not expose its tuning bounds.
- **Impact:** Kubernetes deployments cannot complete several infrastructure
  changes in Angular; capacity troubleshooting needs classic/config/API.
- **Recommendation: bring forward** Kubernetes bootstrap fields and disk/memory
  safety limits; keep controller pressure tuning in an advanced section.
  **M**, settings API/defaults/restart metadata already exist; validate backend
  applicability and bootstrap mutability rather than treating all as locked config.
- **Operator decision:** ____________________

## Other verified reductions

### G7 — Scheduled-task execution options are reduced (P2)

- **Old:** `ScheduleOverlay.jsx`, `scheduleRequest`, lets an inline task schedule
  choose model, auto-merge, keep-running, admission/hosting, network,
  nothing-needed action, reads/writes/capabilities and custom fields.
- **New:** `/next/processes/schedules`,
  `processes-page.component.ts`, `buildScheduleCreatePayload` /
  `buildScheduleUpdatePayload`, exposes cadence/timezone/overlap and target
  template/suite or inline title/description/repo/base. Inline creation sends
  empty capability/read/write lists. Existing omitted values on PATCH are not
  evidence of deletion, but cannot be inspected/edited through this form.
- **API evidence:** `POST /api/schedules`, `PATCH /api/schedules/{id}`,
  `pkg/ui/schedules.go`; `processes.store.ts` passes the reduced requests.
- **Impact:** recurring work requiring a specific model, permissions or network
  policy needs a template/config/API workaround. Basic schedules are relocated,
  not missing.
- **Recommendation: bring forward** model, execution and permission overrides
  using the shared filing controls; prefer templates for repeated complex setups.
  **M**, existing APIs/composition previews, preserve revision checks and omitted fields.
- **Operator decision:** ____________________

### G8 — Browser desktop Inbox notifications are absent (P2)

- **Old:** `InboxPage.jsx`'s NotificationSwitch and `inboxNotifications.js`
  offer opt-in browser permission, persisted preference and notifications when
  new work starts waiting on a person.
- **New:** Inbox classifies server notifications and supports acknowledge,
  investigate and clear (`inbox-classifier.ts`, `inbox.store.ts`), but no
  browser Notification API/permission/preference implementation exists in Angular.
- **API evidence:** server alerts remain `GET /api/notifications` and
  `POST /api/notifications/{id}/{ack,investigate,clear}`.
  Desktop delivery was browser-local, not another server endpoint.
- **Impact:** an operator working in another tab can miss a newly blocked task
  despite the new Inbox being otherwise functional.
- **Recommendation: bring forward.** Small opt-in attention aid with no new
  backend. **S–M**, deduplication, browser permission and hidden-tab tests; do not
  prompt automatically on load.
- **Operator decision:** ____________________

### G9 — Saved boards survive as filters, not their full layouts (P2)

- **Old:** `/boards` and individual boards, `BoardsPage.jsx`, `BoardPage.jsx`,
  `BoardColumnsOverlay.jsx`, `boards.js`: list, Kanban, Gantt, tree, report and
  panels boards; folders, order and configurable columns/panels.
- **New:** Tasks has saved-board loading/creation/deletion and list/timeline.
  `tasks-page.component.ts::applyBoard` consumes query keys
  `tab/group/q/repo/view` only; it ignores `kind/columns/panels`.
  `tasks.store.ts::createBoard` defaults to `kind: "list"`.
  `app.routes.ts` has no `boards` route (unknown routes fall to Inbox).
- **API evidence:** `GET/POST /api/boards`, `PATCH/DELETE /api/boards/{id}`
  (`pkg/ui/boards.go`); new store has no board PATCH caller.
- **Impact:** saved multi-panel/report/column layouts are not reproduced by
  opening their names in Tasks; old board bookmarks are not migrated.
- **Recommendation: leave behind** the full old dashboard editor: the new
  Tasks timeline, Workstreams and Telemetry overlap much of its purpose, while
  several renderers/editor models are costly (**L**). Preserve saved-filter use
  and explicitly label/link unsupported board kinds to classic until retirement;
  agree a migration/export path before removing that fallback (**S–M**).
- **Operator decision:** ____________________

### G10 — Ad hoc filing loses ownership override and task-level continuous runs (P2)

- **Old:** `NewTaskOverlay.jsx` renders Owner and Keep running and submits
  `owner` and `keepRunning: true` to `POST /api/tasks`.
- **New:** `create/new-task-sheet.component.ts` /
  `new-task-form.ts::buildCreatePayload` have neither field/control.
  Reviewers and most other advanced options are retained. Sessions still has
  keep-running (`sessions-page.component.ts`).
- **API evidence:** `pkg/ui/tasks.go::handleCreateTask` consumes
  `CreateTaskRequest` (`pkg/ui/client.go`); task responses retain owner/keepRunning and the classic
  request explicitly supplies them.
- **Impact:** a person cannot file on another owner's behalf from Angular;
  unattended one-off task restart loops require another supported surface.
- **Recommendation:** **bring forward ownership override** for accountable
  delegation (**S–M**, validate caller authorization); **leave behind task-level
  keep-running in ad hoc filing**, since Sessions is the clearer standing-run
  workflow and accidental restart loops consume capacity. Keep API/template support.
- **Operator decision — ownership:** ____________________
- **Operator decision — continuous runs:** ____________________

### G11 — Integration-specific setup and diagnostics are absent (P2)

- **Old:** capability settings' `FigmaAccountList.jsx` supports account test and
  OAuth authorize/exchange; `GChatSpaceList.jsx` shows usage and sends a test post;
  `HostScriptList.jsx` runs a script test with arguments/stdout/stderr/timeout.
- **New:** `settings-work.component.ts` only provides the generic saved-server
  Test and generic secret vault, not these specialized flows.
- **API evidence:** `POST /api/capability-servers/figma-{test,oauth-authorize,oauth-exchange}`,
  `GET .../gchat-usage`, `POST .../gchat-test`, `POST .../test-script`;
  registered in `server.go`. Generic MCP connectivity is not an OAuth exchange
  or a script invocation.
- **Impact:** operators cannot complete those accounts or diagnose actual
  posting/script behavior in Angular; generic “connected” is insufficient.
- **Recommendation: bring forward** integration-aware diagnostics/setup as
  optional detail views, reusing G3 rather than expanding the main settings table.
  **M–L**, service-specific forms and explicit confirmation of test side effects.
  Deployment usage is unverified; prioritize enabled integrations first.
- **Operator decision:** ____________________

### G12 — Conditional non-git change-target filing is missing (P2)

- **Old:** `NewTaskOverlay.jsx` reads `config.changeTargets`, offers registered
  non-git kinds, and posts `changeTarget: {kind}`; git-only fields are suppressed.
- **New:** filing draft/payload in `new-task-form.ts` only handles repo/no-repo;
  no `changeTarget` selector or request. New Inbox/Changes does recognize
  existing changelist tasks, so this is a filing gap, not total changelist absence.
- **API evidence:** `GET /api/config` populates registered kinds in
  `pkg/ui/tasks.go`; `POST /api/tasks` handles creation; templates also carry
  `ChangeTarget` (`pkg/ui/templates.go`).
- **Impact:** an edition advertising those kinds loses direct filing.
- **Recommendation: needs operator decision.** Confirm which supported deployments
  register non-git kinds and whether template-only filing is acceptable. Bring
  forward a conditional picker if any require ad hoc filing; leave it out if this
  migration deliberately supports git-only deployments. **M**, requires access
  to the edition's actual change-target implementation for end-to-end validation.
- **Operator decision:** ____________________

### G13 — Host reboot and power-off are absent; service restart remains (P2)

- **Old:** `/system/power`, `SystemPage.jsx::POWER_ACTIONS` offers restart,
  reboot and shutdown, gated by configured command availability and confirmation.
- **New:** `/next/fleet?tab=controls`, `fleet-page.component.ts` provides
  `POST /api/host/restart` and restart-watch, but no reboot/shutdown.
  Legacy power links relocate to this controls tab.
- **API evidence:** `POST /api/host/{restart,reboot,shutdown}` remain registered;
  `pkg/ui/server_test.go` covers enabled/unavailable power actions.
- **Impact:** host recovery/power-off needs classic or infrastructure tooling.
- **Recommendation: leave behind** reboot/shutdown in the default new console.
  Restart covers the service workflow; power-off cannot be recovered from this UI,
  and infrastructure management is a better home. No usage evidence establishes
  reliance on these controls. **S** if the operator elects to retain them; preserve
  availability gates/confirmation. This recommendation does not remove APIs.
- **Operator decision:** ____________________

### G14 — CI retention and queue tuning forms are reduced (P2)

- **Old:** `components/settings/CISection.jsx` includes queue age boost,
  output-size/age retention, job-cache entry/age limits and artifact age/per-run/
  per-file caps.
- **New:** `/next/settings/ci`, `settings-code.component.ts` retains jobs/run,
  runs kept, cache/artifact total MiB and capacity reservation, but not
  `ciAgeBoost, ciStepOutputMb/Days, ciRunOutputKb, ciCacheEntries/Days,
  ciArtifactDays/RunMb/FileMb`.
- **API evidence:** `GET/POST /api/settings`, `pkg/ui/settings.go` still
  implements these settings. Their presence in the new settings label map is
  not an editor.
- **Impact:** fine-grained storage/fairness tuning moves to config/API/classic;
  ordinary CI run operations are unaffected.
- **Recommendation: leave behind** the dense advanced form, retain documented
  config keys and display a pointer for tuning. Existing basic limits cover the
  common UI case; a second exhaustive editor adds maintenance cost. **M** to
  restore with validation/defaults if operators report a frequent tuning workflow.
- **Operator decision:** ____________________

### G15 — Some configuration-repository recovery controls and release narratives are missing (P3)

These are separate low-priority choices, grouped here only to keep the executive
list short.

| Feature | Classic behavior and evidence | Angular behavior and evidence | Operator impact, recommendation and effort | Operator decision |
| --- | --- | --- | --- | --- |
| Separate encrypted-secrets repository and reset-notice dismissal | `StateRepoPanel.jsx`: `POST /api/state-repo/secrets-repo` sets/disconnects the encrypted remote; `POST /api/state-repo/reset-notice` acknowledges reset notice. Backing handlers in `pkg/ui/staterepo.go`. | `/next/settings/config-repo`, `settings-code.component.ts`: main remote/sync, files/PRs, key wrapping/import retained; no separate secrets-remote or reset-notice action. | Secret backup destination cannot be maintained here. **Bring forward secrets-remote status/setup** (**M**, preserve key/secret safeguards); **leave behind standalone reset dismissal** (**S** to restore), provided the underlying reset/recovery condition stays visible. Do not equate a dismissed notice with repaired data. | Secrets remote: __________; reset dismissal: __________ |
| Weekly prose and version-history release notes | `/system/metrics`, `SystemPage.jsx` → `ReleaseNotesPanel.jsx` / `ReleaseVersions.jsx`: `GET /api/release-notes`, `/{period}`, `/versions`, `/versions/{version}`; handlers `pkg/ui/releasenotes*.go`. | `/next/telemetry`, `telemetry-page.component.ts` only fetches `GET /api/release-notes/stats`; Upgrade can fetch selected target notes via `GET /api/upgrade/release-notes`. | Counts do not explain what shipped; selected upgrade notes do not replace browsing weekly history. **Bring forward** a compact history/prose view (**M**, APIs already exist), useful for evaluating automation output. | __________ |

### G16 — Merge-batch diagnostics are absent (P2)

- **Old:** repository overview, `ui/src/components/RepoPage.jsx` →
  `MergeBatchPanel.jsx` renders batch members, pinned heads/base, recheck ref
  and outcome from `GET /api/merge-batches?repo=...`.
- **New:** `/next/repos/:owner/:name` and CI show individual work/checks;
  `repos/repo-detail-page.component.ts` and `ci/` have no merge-batch caller.
- **API evidence:** `GET /api/merge-batches` remains registered in
  `pkg/ui/server.go`. A generated route entry is the only Angular route
  declaration, not a diagnostic view.
- **Impact:** operators cannot explain batch membership or a discarded combined
  recheck from the new UI without inspecting classic/API.
- **Recommendation: bring forward** a compact repo/CI diagnostic panel (**M**,
  existing read API and task/run links). Keep ordinary PR actions where they are.
- **Operator decision:** ____________________

## Retained, relocated, or deliberately constrained — not restoration proposals

| Area | Implementation checked | Result / boundary |
| --- | --- | --- |
| Settings/model setup | Classic `settings/ModelsSection.jsx`, `ModelBackendPage.jsx`; new `settings-compute.component.ts`: connection CRUD/credentials, model catalog/defaults, reasoning and usage budgets. Shared deployment and credential routes in `pkg/ui/deployment_routes.go`, `server.go`. | Major setup is present. Per-connection discovery/test differs: see #26 below. |
| Git credentials and testing | New `settings-code.component.ts`: `POST /api/deployment/git-connections/check` (individual and overall), connection CRUD and named-token storage. | Relocated to Settings → Git, not a lost “connection test” category. Ambient-source detail (`GET /api/git-ambient-sources` in classic GitConnections) is a candidate reduction, **not fully audited**. |
| Sessions and Director | `app.routes.ts` maps `director` to `sessions/director`; `sessions-page.component.ts`, `sessions.store.ts`: create/delete, model, grants, network, keep-running, restart/cancel and attachments via console-session/task APIs. `director/` implements the side panel. | Sessions is not absent. No additional verified session gap beyond #27's reported interaction defects; those defects were not browser-reproduced in this audit. |
| Task filing/capabilities | `new-task-form.ts::buildCreatePayload`, `new-task-sheet.component.ts`, `shared/grants-picker.component.ts`: repo/base/new branch, dependencies, reads/writes, grants, model/connection/network/resources, reviewers, custom fields, attachments, approval/auto-merge, review template, prompt override and priority. | Controls under More are relocated, not missing. Specific omissions are G10/G12. |
| Task/PR/merge operations | `task-detail.component.ts`, `task-changes-tab.component.ts`, `task-gates-bar.component.ts`, Inbox classifiers: submit/retry-merge/force-merge, diff-preview, config-change approval, human-review and script-approval actions with shared task APIs. | Core decisions retained; batch diagnostics are the specific reduction in G16. |
| CI | `ci/ci-page.component.ts`, store: runs/jobs/steps, cancellation/rerun, quarantine, cache actions and artifact links via `/api/ci/...`. | Not missing wholesale; G14 is the settings reduction. Native/GitHub-provider live execution was not tested. |
| Schedules/templates/suites | `processes-page.component.ts`, store: CRUD, run-now, pause, recurrence including events, timezone/overlap, composition, suites/runs and declaration inspection. `/api/schedules`, `/api/templates`, `/api/suites`, `/api/suite-runs`. | Relocated to Processes. G7/G4 are specific remaining reductions. |
| Sandbox operations | `fleet-page.component.ts`: health/probe/recreate, persistent-sandbox lifecycle/logs, host join/rotation/rename/revoke/delete, top/logs/pipeline pause and caches; `/api/sandboxes`, `/api/persistent-sandboxes/...`, `/api/hostds/...`, `/api/tasks/{id}/sandbox/recreate`. | Relocated to Fleet; bootstrap tuning gap is G6. |
| Repositories, context and telemetry | New repo detail includes release settings/cut/promote and workflow actions; `context/` handles memory/namespace; Telemetry has metrics. Legacy system/admin routes redirect through explicit maps in `app.routes.ts`. | Different navigation is not loss. Release narrative gap is G15; old board routes are G9. No claim of field-by-field parity in every metric. |
| Config-managed settings | Both UIs use config-repo state; new `settings-state.service.ts`, `settings-sections.ts::CONFIG_REPO_SECTION_PATHS`, repo detail and Processes check locks/refusals. | Deliberate: locked/external standing config uses reviewed config PRs. See settings design and mutability spec; operational actions/secrets/bootstrap have distinct rules. Do **not** restore bypasses. |
| Old administrator/team model | `settings-panel.design.md` inventory explicitly records System Administrators deleted by #4880; route comments document pre-single-tenant `/t/<team>` compatibility. | Documented earlier removal/architecture change, not evidence the Angular migration accidentally lost those editors. Recommend **leave behind** superseded admin/team UI. **Operator decision:** __________ |

## Already in progress: Tasks #26 and #27

The operator identified both as active work; this audit keeps them **in progress**
rather than proposing duplicate tasks.

- **[Task #26](/tasks/26): Codex auth.json setup and model connection tests.**
  This checkout already has per-connection auth.json status/save/clear in
  `settings-compute.component.ts`, calling
  `GET/PUT/DELETE /api/agent-backends/{id}/codex-auth`; the head includes the
  recent per-connection Codex merge (#1587). Do not report auth upload as wholly
  absent. Classic `settings/ModelsSection.jsx`'s Test Connection calls
  `GET /api/agent-models?framework=...&backend=<id>`; Angular
  `loadDiscoveredModels` calls unqualified `GET /api/agent-models`, which is
  not a per-connection test (`pkg/ui/agent_models.go` selects the named backend).
  Recommend **bring forward through #26**, verifying named-backend requests and
  distinguishing authentication success from model discovery.
  **Operator decision:** ____________________
- **[Task #27](/tasks/27): Director side-panel scrolling and Enter/Shift+Enter.**
  Recommend **bring forward through #27**, aligning its composer with Sessions.
  Treat as an interaction fix already owned there, not a new restoration.
  **Operator decision:** ____________________

**Status refresh:** 2026-11-15 23:48 UTC initial check:
`grain pr status --task 26` and `--task 27` each reported no CI run recorded.
`grain_get_task` returned 403 (`tasks:read` grant unavailable); namespace task
reading was likewise denied. This is **not** evidence either task is unstarted,
merged, or complete. Final refresh at **23:53 UTC** used `grain ci list --task` as well: #26 has no
runs; #27 has GitHub Actions tests running at `6bb8cfc85`, with both scanning
workflows successful. The generic `grain pr status` result alone did not expose
these Actions runs. After fetching, #27's branch contains that commit (not on
main): its diff adds shrink/scroll constraints and a Director-specific
Enter-to-send option with Shift+Enter/IME guards and regression tests. This
confirms implementation is in progress, not landed parity. No #26 remote branch
was available in fetched refs. Definitive task state remains unverified without
the grant. No task-record access workaround or duplicate proposal was attempted.

## Validation and open limits

- Confirmed both UI implementations are available and shared backend routes still
  register the cited actions; inspected important request bodies and backend
  semantics, especially active versus scheduled cancellation.
- Checked the new route map and nav against classic paths, and searched actual
  callers before classifying absences. Saved boards, Codex setup, notifications,
  schedules, PR actions and sandbox controls received explicit false-positive checks.
- Reviewed git history for the current baseline/recent model changes; did not infer
  old behavior from screenshots or filenames alone.
- No UI restoration, API mutation, deployment setting change or follow-up filing is
  included. This document is non-normative and has no implementation requirement
  IDs; accepting the audit does not approve all recommendations.
- Unverified: production feature usage; private edition behavior; live browser
  rendering/keyboard/reverse-proxy behavior; definitive #26/#27 task state; exhaustive
  historical parity and every setting/metric. Those limits do not weaken the
  concrete source-level gaps above.
