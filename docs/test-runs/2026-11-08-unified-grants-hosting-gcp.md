# Walkthrough Report: Unified Grants Slice 2, `hosting` Task Setting, `github#sandbox-repo` Retirement & Keyless GCP Token Mode (`docs/test-plans/2026-11-08.md` §3)

- **Date**: 2026-11-08 / 2026-11-09
- **Task**: [Task #3638](/tasks/3638) (spawned from [Task #3495](/tasks/3495))
- **Test Plan**: `docs/test-plans/2026-11-08.md` §3
- **Feature PRs Exercised**: `#640`, `#818`, `#850`, `#851`, `#857`, `#881`, `#886`, `#891`, `#896`, `#897`, `#898`, `#907`, `#913`, `#925`, `#927`
- **Harnesses**: `docs/test-runs/2026-11-08-unified-grants-hosting-gcp/`

---

## Executive Summary

Executed all five requirement groups of `docs/test-plans/2026-11-08.md` §3 end-to-end against real `grain daemon` instances (SQLite 3.40.1 and PostgreSQL 15.19), a local GitHub App + repository simulator (`walksim.go`), `gcpfake` + `portforward.Relay` + real Google Cloud SDK (`/usr/bin/gcloud`), and headless Playwright Chromium across desktop (`1440×900`) and phone (`390×844`) viewports in light and dark themes.

Four real bugs (**F1–F4**) were uncovered during live execution, deterministically reproduced, fixed in this branch with unit and SQLite/PostgreSQL regression tests, and re-verified end-to-end against rebuilt `HEAD` binaries:

| ID | Area | Severity | Summary | Status |
|---|---|---|---|---|
| **F1** | `pkg/model/legacy_grant_migration.go`, `migrations.go`, `schema.go` | Medium | `rewriteLegacyGrantsMulti` (migrations 67–124, including 124's `github-sandbox` retirement) rewrote `task_grant`, `template_grant`, and `schedule_grant` without rewriting `task_grant_wait.capability`, leaving queued grant-waiting tasks displaying `"waiting for ... (github-sandbox grant)"` after migrating to canonical `github#sandbox-repo={}`. | **Fixed** (`rewriteLegacyGrantsMulti` + schema migration `129` `migrateRetiredGrantWaits` + SQLite/PostgreSQL tests) |
| **F2** | `pkg/mcp/mock_tools.go`, `pkg/orchestrator/finish.go`, `pkg/ui/client.go`, `pkg/model/canonical_grant.go` | High | (a) `propose_task` lacked a `grants` property and did not decode `args["grants"]`, silently dropping `grants: [...]` on proposals and auto-approving widened child proposals with `capabilities: []`! (b) Passing canonical `api#<scope>` `{capability, with}` objects in `propose_task` `capabilities:` bypassed `StoredGrantSpelling` in both `ui.Client.creationGrants` (`ProposedBy != ""`) and `orchestrator.relayProposedTasks`, writing raw `https://grain.dev/capabilities/api#...` into `task_grant`. | **Fixed** (`DecodeRequestGrantsJSON` / `DecodeRawRequestGrants`, `propose_task` `grants` support, `NormalizeCapabilityGrants` on `ProposedBy`, `relayProposedTasks` normalization + unit tests) |
| **F3** | `pkg/ui/client.go` | Medium | (a) `decodeRequestGrants` set `out.Capabilities = []string{}` on `"grants": []` without checking `legacy.Capabilities`, allowing `{"capabilities": ["api:ci:read"], "grants": []}` on `POST /api/tasks` and `PATCH /api/tasks/{id}` to silently wipe `capabilities` instead of returning HTTP 400. (b) `CreateTaskRequest.UnmarshalJSON` checked `len(r.Reads) > 0`, `len(r.Writes) > 0`, and `r.Repo != ""` instead of field presence, accepting `"reads": []` + `repo#read`, `"writes": []` + `repo#write`, `"base": "develop"` + `repo#target` (silently discarding `"base"`), and `"noRepo": true` + `repo#target`. | **Fixed** (`CreateTaskRequest.UnmarshalJSON` + `DecodeRequestGrantsJSON` + regression tests in `request_grants_internal_test.go`) |
| **F4** | `pkg/capability/intree/gcp_token.go` | High | Keyless GCP token mode (`#925`) switched `activate.sh` to `gcloud config set auth/credential_file_override`, but `gcpExternalAccount` omitted `"token_info_url"`. Real `gcloud` (`store._LoadFromFileOverride` → `introspect.GetExternalAccountId`) introspects non-impersonated `external_account` tokens on every command against `token_info_url` (defaulting to `https://sts.googleapis.com/v1/introspect`), failing every `gcloud` invocation in closed-network sandboxes (`ProxyError`) and open-network sandboxes (`InactiveCredentialsError: {"active": false}`). | **Fixed** (`token_info_url` pointing to local `/runs/<attempt_id>/introspect` RFC 7662 handler tracking SHA-256 digests of active minted tokens + real-`gcloud` unit & walkthrough verification) |

---

## Environment & Build Verification (Requirement 1)

- Built frontend (`cd ui && npm ci && npm run build`) and Go CLI/daemon (`go build -o /home/debian/bin/grain ./cmd/grain`) cleanly from `HEAD`.
- Built genuine schema-122 reference binary (`/home/debian/bin/grain122`) from historical schema-122 commit to seed authentic schema-122 SQLite and PostgreSQL databases.
- Verified local backing stores and tools: SQLite `3.40.1`, PostgreSQL `15.19` (`127.0.0.1:5432`), Google Cloud SDK (`/usr/bin/gcloud`), and Playwright Chromium (`/var/cache/playwright`).

---

## Requirement 2: Schema 122 → 129 Migration, `prepare-upgrade`, Export, `state check`, Re-Import & `#851`

Harness: `walk-migration.sh`, `seed122.sh`, `dump-state122.sh`, `probe-migration-api.sh`.

1. **Seeded Realistic Schema-122 SQLite & PostgreSQL Databases**:
   - Tasks, templates, schedules, standing `director` console session, `grain_config.default_capabilities`, `repo_config.default_capabilities`, `repo_config.ci_capabilities`, `task_grant_wait` (`task_id = 3`, `capability = 'github-sandbox'`, `resource = 'GitHub App rate limit'`), `capability_policy` rules + catalogue defaults referencing `console`, `github-sandbox`, and `grain-ci`, plus `gcp#gcp-key={"account":"unconfigured-set"}` across task, template, schedule, repo default, and catalogue repo default.
2. **`grain state prepare-upgrade` + In-Place Boot + Bare Git Export + `grain state check` + Fresh PostgreSQL Re-Import**:
   - `grain state prepare-upgrade` rehearsed `129 rows across 27 tables migrate from schema 122 into 129` on both SQLite and PostgreSQL with expected one-release deprecation warnings for `console` and `gcp#gcp-key` `permission_set` rewrite notices.
   - Booted current `grain daemon -sandbox-port-base=0`, exported state repo to bare git remote, ran `grain state check` (`schema: 129 ... loaded: 40 rows across 9 tables ... ok: grain can load this`), and imported into a fresh PostgreSQL daemon with **0 diff lines** between `prepare-upgrade` and direct in-place migration on both engines:
     - `console` grants moved cleanly to `hosting = 'console'` / `default_hosting = 'console'` / `ConsoleSession.Hosting = 'console'`.
     - `github-sandbox` grants, `repo_config.ci_capabilities`, `capability_policy` rules/catalogue defaults, and **`task_grant_wait.capability`** (post-**F1** fix) migrated to `https://grain.dev/capabilities/github#sandbox-repo={}` (`GET /api/tasks/3` reports `"waiting for GitHub App rate limit (https://grain.dev/capabilities/github#sandbox-repo={} grant)"`).
     - `grain-ci` policy rules/defaults migrated to `https://grain.dev/capabilities/grain#ci={}`.
     - `gcp#gcp-key={"account":"unconfigured-set"}` rewrote to `{"permission_set":"unconfigured-set"}` with explanatory notes so `grain state check` passed cleanly.
   - Submitted legacy `console` and `github-sandbox` over REST (`POST /api/tasks`, `/api/templates`, `/api/schedules`) and `.grain/` declarations (`grain declarations check`): confirmed HTTP 201 canonicalization (`hosting: "console"`, `github#sandbox-repo={}`) and one-release deprecation warnings for `console`.
3. **`#851` Merged-Catalogue Validation (Both Directions)**:
   - Disabling `https://grain.dev/capabilities/github` or `in_process.github-sandbox` via `PUT /api/capability-policy` while referenced in task/CI defaults → **HTTP 400** naming `defaults.task`, `defaults.ci`, and `defaults.repos[acme/widgets]`.
   - Disabling unused `release` server in `PUT /api/capability-policy` (`HTTP 200`), then attempting `PUT /api/repos/acme/widgets/ci-capabilities` adding `release#image-signing={}` → **HTTP 400** (`server https://grain.dev/capabilities/release is disabled in settings/capabilities.yml`).

---

## Requirement 3: Unified Grants Slice 2 Live API, Escalation & Proposal Equivalence Matrix

Harness: `walk-slice2-and-runtime.py`, `walk-slice2-and-runtime.sh`, `walksim.go`, `fake-agy.sh`.

1. **Task Creation & Partial Editing (`POST /api/tasks`, `PATCH /api/tasks/{id}`)**:
   - Created task via `grants: [{capability, with}]` combining `api#tasks:read` (`{"repo":"acme/app"}`), `api#ci:read` (`{}`), `api#task-admin`, `repo#target` (`{"repo":"acme/app","base":"release"}`), `repo#read` (`{"repo":"acme/docs"}`), and `repo#write` (`{"repo":"acme/site","base":"release"}`) → **HTTP 201**, stored cleanly as `capabilities: ["api:ci:read", "api:task-admin", "api:tasks:read@acme/app"]`, `repo: "acme/app"`, `base: "release"`, `reads: ["acme/docs"]`, `writes: [{"repo":"acme/site","base":"release"}]`.
   - Partial `PATCH /api/tasks/2` with only `grants: [repo#read {acme/widgets}]` updated `reads` while preserving all 3 non-repo `capabilities` and `writes` intact; subsequent `PATCH` with only `grants: [api#ci:write {acme/app}]` updated `capabilities` while preserving `reads` and `writes` intact.
   - Verified all **11 mixed-spelling edge cases** (post-**F3** fix) return **HTTP 400**, including non-empty & empty `capabilities`/`reads`/`writes`, `capabilities` + `"grants": []` on `POST` & `PATCH`, `repo`/`base`/`noRepo` + `repo#target`, and `repo#target` on `PATCH`.
2. **Bidirectional Parent/Child Proposal Escalation Matrix (Post-F2 Fix)**:
   - **Direction A (Legacy Parent → Canonical `grants:` Child)**: Parent holding `api:tasks:read@acme/app` + `writes: [{repo: "acme/app", base: "main"}]` (`autoMerge: true`):
     - `A0` (mixed `capabilities` + `grants`): refused immediately by `propose_task` (`isError: true`).
     - `A1` (`api#tasks:read {acme/app}` + `repo#target {acme/app, main}` in `grants:`): **auto-approved** (`approved: true`, `approvedBy: "auto-submit"`, `autoMerge: true`), zero false escalation.
     - `A2` (widened scope `api#tasks:write {acme/app}`): **held unapproved** (`approved: false`, `state: "proposed"`, `autoMerge: false`).
     - `A3`/`B3` (widened unscoped `api#tasks:read` `{}`): **held unapproved**.
     - `A4` (widened write repo `repo#write {acme/site, main}`): **held unapproved**.
     - `A5` (widened base `repo#target {acme/app, release}`): **held unapproved**.
     - `A6` (canonical `{capability: "https://grain.dev/capabilities/api#tasks:read", with: {repo: "acme/app"}}` in `capabilities:`): **auto-approved** and stored in normalized `api:tasks:read@acme/app` form.
   - **Direction B (Canonical `grants:` Parent → Legacy Child)**:
     - `B1` (`api:tasks:read@acme/app` + `repo: "acme/app", branch: "main"`): **auto-approved** (`approved: true`, `autoMerge: true`).
     - `B2` (widened scope), `B3` (unscoped), `B4` (widened base `"release"`): all **held unapproved** (`approved: false`, `autoMerge: false`).
3. **`GET /api/capability-methods` & `POST /api/capability-grants/check`**:
   - `GET /api/capability-methods` returned `api` (65 colon-containing scope & preset methods), `repo` (`read`, `write`, `target`, `git-credential`), `github` (`read-issues`, `sandbox-repo`), `grain`, `config-map`, `model-proxy`, and `release`.
   - `POST /api/capability-grants/check` validated `api#tasks:read` (scoped & unscoped), `api#task-admin`, `repo#target`, `repo#write` (`{repo, base}` task-level vs `{repo}` CI-only vs `{refs, force}` dangerous), and rejected unknown argument keys (`extra: 1`) with field-level refusals.

---

## Requirement 4: Live `github#sandbox-repo`, `hosting` Inheritance & Keyless GCP Token Mode

1. **Live `github#sandbox-repo` `MintPass`, Release & Startup Reaper (`walk-slice2-and-runtime.sh` Sections 5 & 7)**:
   - Configured GitHub App secrets (`github-app/app-id`, `github-app/private-key`) against `walksim.go` and dispatched Task `#15` holding `https://grain.dev/capabilities/github#sandbox-repo={}`.
   - Verified `MintPass` minted an installation token, created attempt-named private repo `sandbox-org/grain-sandbox-grain-15-1-0`, injected scoped `GH_TOKEN`/`GITHUB_TOKEN` + `GRAIN_SANDBOX_REPO` into the run, and executed `DELETE /repos/sandbox-org/grain-sandbox-grain-15-1-0` on `ReleasePass` (`live: []`).
   - Seeded leaked `grain-sandbox-*` repos plus `unrelated-repo` on `walksim` and restarted `grain daemon`: startup reaper deleted only the 2 `grain-sandbox-*` repos and left `unrelated-repo` untouched.
2. **Live `hosting` Inheritance Chain (`walk-slice2-and-runtime.sh` Section 6)**:
   - Verified `grain_config.default_hosting` → `repo_config.default_hosting` → `template.hosting` → `schedule.hosting` → `task.hosting` across 7 live daemon steps, confirming additive elevation (`MaxHosting`) across defaults/templates/schedules and explicit leaf overrides (`task.hosting = "unattended"` overriding `template.hosting = "console"` on creation and `PATCH /api/tasks/{id}`).
3. **Keyless GCP Token Mode (`walk-gcp-token.go`, Post-F4 Fix)**:
   - Wired `intree.NewConfigurableGCP` (`gcpfake`, `credential: "token"`, `deployer@walk-proj.iam.gserviceaccount.com`) over MCP + `portforward.Relay` + `orchestrator.RunCycle` + real `/usr/bin/gcloud`.
   - Verified `activate.sh` invokes `gcloud config set auth/credential_file_override <path> --quiet` and `gcloud config set project 'walk-proj' --quiet` (`CreateKey=0`, `LiveKeys=0`).
   - Verified `.gcp-service-account.json` sets both `token_url` and `token_info_url` on the forwarded local listener; real `gcloud auth print-access-token` succeeded twice with distinct impersonated tokens and **zero** external/STS proxy requests (`count=0`), while post-release exchange and introspection both returned `HTTP 401`.
   - Verified non-zero `gcloud` exit during `activate.sh` aborts before `Framework.Run`, tears down the sandbox/pass, and preserves `gcloud`'s verbatim stderr in the task setup-failure comment.

---

## Requirement 5: Playwright Chromium UI Walkthrough

Harness: `walk-playwright.mjs` (18 screenshots + `/tmp/walk-playwright-shots/contact-sheet.png` across `1440×900` desktop and `390×844` phone in `light` and `dark`; **0 `pageerror`**, **0 horizontal overflow** across all 12 viewport/theme/route cells):

- **`/tasks/<id>`**: Verified `HostingField` directly beneath `AdmissionField` (`y=622.56` vs `y=523.78`), helper copy explaining `hosting` controls interactive console hosting rather than capabilities, live toggle `unattended` → `console` (`PATCH` persisted), and `hosting_changed` transition event.
- **`/processes`**: Verified `HostingField` in `TemplateOverlay` and standalone `ScheduleOverlay`, plus template-backed schedule `#composition-preview` rendering `"Hosting / interactive console"` without exposing a duplicate standalone selector.
- **`/sessions/director`**: Confirmed zero `HostingField` controls on standing sessions (inherently console-hosted) and verified `PUT /api/console-sessions/director` adding `https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}` in `grain demo` succeeded (`HTTP 200`) and rendered in `session-director-grants` (`#913`).
- **`/settings/capabilities` & `grain demo -legacy-capability`**: Verified `github-sandbox-deprecation-notice` banner (`#886`) and legacy capability picker section (`"Legacy capabilities"` → `"Example legacy capability"`).
