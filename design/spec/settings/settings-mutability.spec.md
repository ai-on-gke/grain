# Settings mutability and `settings_locked` (normative)

**Status:** draft for review. Normative for the write paths it describes; where the code and this document disagree, the disagreement is listed in §4 and is a bug in one of them until a follow-up resolves it.

This specification records, for **every setting and configuration entity in Grain**, three things:

1. whether it is **settable from the UI** (and through which surface and REST route);
2. whether it is **settable from the config repository** (`grain/config` or an external Git state repo), and in which file;
3. whether it is **locked** when `grain_config.settings_locked` (`settingsLocked`) is on, and how it behaves in external config-repo mode, under the `directConfigWrites` opt-out, and beside a repository's own `.grain/settings.yml`.

§2.10 condenses this into one matrix: for each surface (bootstrap and config-managed `grain_config` fields, `repo_config`, composed entities, secrets, `state-repo.json`, browser preferences), who may write it (person via UI, REST API, agent via scoped API, config PR) in each of the four modes.

It is the per-setting companion of the one-page model in [`design/design/settings.design.md`](../../design/settings.design.md), which defines the six state surfaces and the four deployment modes this document relies on. The field-by-field storage reference that design names, [`settings.spec.md`](settings.spec.md), is a separate document and is not restated here.

Ground truth audited: `pkg/ui/config_refusal.go`, `pkg/ui/config_bootstrap.go`, `pkg/ui/settings.go`, every mutating route registered in `pkg/ui/server.go` and `pkg/ui/deployment_routes.go`, `pkg/ui/route_scopes.go`, `pkg/staterepo/bind.go` / `dump.go` / `decl.go` / `capabilities_file.go`, `pkg/trustboundary/trustboundary.go`, `pkg/declschema/declschema.go`, `pkg/repodecl/settings.go`, `ui/src/settingsSections.js`, `ui/src/components/SettingsPage.jsx`, `ui/src/components/RepoPage.jsx` and the section components under `ui/src/components/settings/`, on `main` at the merge of #297 (which includes #275, `settings_locked`).

## 1. Conventions

1.1. The key words "MUST", "MUST NOT", "SHOULD" and "MAY" are to be interpreted as in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

1.2. **Guard classes.** Every mutating handler either calls one of the guards in `pkg/ui/config_refusal.go` or calls none. This document names them by the following short codes, used in every table of §3:

| Code | Guard | Refused in internal mode with `settings_locked: true`? | Refused in external mode? | Refusal carries |
| --- | --- | --- | --- | --- |
| **S** | `checkSettingsWritable` | Yes | Yes | rehearsed `change` patches + `path` (`configWriteRehearsal`) |
| **S·u** | `checkSettingsWritableUnrehearsed` | Yes | Yes | `path` + `description` only; refused *before* any side effect (secret store writes) |
| **B**, **B·u** | `checkBootstrapWritable` / `checkBootstrapWritableUnrehearsed` | No | Yes | as S / S·u |
| **G** | `checkGrainConfigWritable(before, after)` | Only if the request changes a non-bootstrap `grain_config` field (§2.4) | Yes | as S |
| **—** | no guard | No | No | — (written directly in every mode) |

1.3. **"Locked"** below means the server's `configRepoOnly(ctx)` is true for an API caller: `externalConfigRepo != ""` **or** (`Config.StateRepo != nil` **and** `settings_locked` is true **and** `directConfigWrites` is not set in `<data-dir>/state-repo.json`). Grain's own writes (reconcilers, merged config being applied, daemon loops) carry no `WriteRehearsal` in their context and are never refused (`configRepoFor`; `TestInternalModeLetsGrainsOwnWritesThrough`).

1.4. **UI read-only** is what the React app renders, which is decided separately from the server (§2.6). Where the two differ, §4 lists it.

## 2. Mutability and lock rules

### 2.1 Standalone / demo (no config repository) {#SM-1}

With no state repository (`Config.StateRepo == nil` and no external remote, e.g. `grain demo`), `configRepoFor`, `configRepoOnly` and `rowConfigRepoOnly` treat the deployment as unlocked regardless of `settings_locked`: every guard returns nil, every setting a route exists for MUST be writable from the UI and the REST API and is written straight to the store with nothing to sync, and `GET /api/config` and `GET /api/settings` report `configRepoOnly: false`. When `GET /api/config` reports neither `configRepo` nor `externalConfigRepo`, Settings → General disables the *Settings lock* card so the UI does not offer a lock toward a repository that does not exist (D-12, fixed).

### 2.2 Internal `grain/config`, `settings_locked: false` (default) {#SM-2}

2.2.1. Every guard class allows the write. UI and REST writes commit to the store and, when the request's guard ran (`markWroteSettings`) and its status is < 300, `configRefusalWriter` calls `StateManager.SyncAfterWrite`, which exports the database to `grain/config` immediately and advances the loaded-commit marker so the next reconcile tick does not import the older file over it (`TestInternalModeEditsSyncToStateRepoAndPersistAcrossReconcileCycles`).

2.2.2. A merged commit or PR on `grain/config` is imported by `staterepo.Apply` on the next tick, **only** for `SettingsTables` and `memoryTables` (`pkg/staterepo/bind.go`). Runtime tables (`task`, `task_run`, `executor_token`, …) are never imported live.

2.2.3. Writes through unguarded routes (class —; memory, release notes, secrets, tokens) do not call `markWroteSettings` and so do not trigger `SyncAfterWrite`; the ones that live in exported tables (memory, release notes) reach `grain/config` on the next background export.

### 2.3 Internal `grain/config`, `settings_locked: true` {#SM-3}

2.3.1. `GET /api/config` MUST report `configRepoOnly: true` and `settingsLocked: true` (unless `directConfigWrites` is set, §2.5).

2.3.2. A route of class **S** or **S·u**, and a **G** request that changes any non-bootstrap field, MUST answer `409` with `code: "config_managed_externally"`, `repo: "grain/config"`, `internal: true`, `url: "/repos/grain/config"`, the `path`s of the files the write would change and — for S/G — the rehearsed unified-diff `change` patches, which `POST /api/state-repo/pull-requests` accepts as-is (`TestOpenConfigPullRequestTakesTheRefusalsChange`). For an agent caller the body also carries `access` (`canWrite`, `via` / `need`), saying whether the task holds `writes:` on `grain/config`.

2.3.3. A rehearsed route that stops before its write (unknown id, invalid body, no-op) MUST still answer with the refusal, not its own 4xx/200 (`configRefusalWriter` swallows the response; `TestExternalModeRefusesARequestThatWouldFailAnyway`).

2.3.4. The following MUST stay writable:
- **Bootstrap settings** (class B, and the bootstrap fields of class G, §2.4): Models, Sandboxes resources/backend/image/k8s, `maxWorkers`, CLI run locations, model connections, GCP keys, capability-server secret bindings' guard.
- **Secret material** (class —): `PUT/DELETE /api/secrets/…`, agent keys, model-connection credentials, Codex auth, GitHub tokens.
- **Browser preferences** (`localStorage`, never sent to the server).
- **Access** (sign-in; not stored in Grain's database).
- **Runtime actions** (class —): upgrade, power, pipeline pause, cache flushes, sync, connection checks, task operations, schedule *run-now*, persistent-sandbox actions.
- **The lock itself**: `settingsLocked` is a bootstrap field (cleared in `clearBootstrap`), so `PUT /api/settings` / `PUT /api/config` with `{"settingsLocked": false}` (or `settings_locked`) is a bootstrap-only change and is allowed — for a person. An agent principal unlocking (`before.SettingsLocked && !after.SettingsLocked`) is sent through `checkSettingsWritable` instead, and so is refused while locked (§2.8).

2.3.5. **Mixed requests.** A single `PUT /api/settings` (or `PUT /api/deployment`) whose planned row differs from the stored one in any non-bootstrap field MUST be refused as a whole: its bootstrap half is not written either (`{"maxWorkers":5,"environmentName":"prod"}` in `TestInternalModeWritesBootstrapDirectlyAndRefusesTheRest`). A non-bootstrap field sent with its *current* value is not a change (`bootstrapOnlyChange` compares pruned JSON), so a client that echoes the whole form back after editing only bootstrap fields is not refused. A request that fails validation (`plannedConfig` error) is answered by the validation error unless the bootstrap guard refuses it (external mode).

2.3.6. A row that is `ConnectionsOnly()` (no poll interval, worker count, GitHub host or deployment id — an install before the daemon's first start) is treated as bootstrap: any `G` write to it goes through `checkBootstrapWritable`.

### 2.4 Bootstrap fields of `grain_config` {#SM-4}

The bootstrap set is exactly what `clearBootstrap` (`pkg/ui/config_bootstrap.go`) zeroes:

- **Models:** `agentBackends`, `defaultModel`, `agentBackendId`, `modelSettings`, `judgementModel`.
- **Sandboxes:** `maxWorkers`, `sandboxBackend`, `sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`, `sandbox{Cpu,Memory,Disk}Overcommit`, `sandboxMemoryReserveMb`, `sandboxDiskFloorShare`, `sandboxDiskFloorMinMb`, `sandboxOvercommit` (adaptive overcommit), `networkAccess`, `sandboxImage`, `sandboxImageMirror`, every `k8s*` field (including `k8sPriorityClass{Interactive,Agent,Ci}`).
- **CLI run locations:** `cliRun`.
- **The lock:** `settingsLocked`.

Every other `grain_config` field is non-bootstrap. Notably **not** bootstrap: `sessionSandboxIdleTtl` (drawn in Sandboxes), `pollInterval` / `maxMergesPerRepo` / `mergeBatchSize` / `mergeGatherWindow` (Engine), all cache settings (Caches), `githubHost` / `githubInsecureHttp` / `gcp*` / `iap*` (drawn in Legacy capabilities), `gitConnections` / `gitCredential`, `externalAgentApi`, `timeZone`. See §4 for where the UI disagrees.

### 2.5 `directConfigWrites` opt-out {#SM-5}

2.5.1. `"directConfigWrites": true` in `<data-dir>/state-repo.json` (re-read on every guarded write, no restart) MUST make `configRepoOnly` false in **internal** mode regardless of `settings_locked`: every guard allows the write and S/G writes sync via `SyncAfterWrite` as in §2.2 (`TestDirectConfigWritesLetsTheAPIWriteInInternalMode`).

2.5.2. In **external** mode `directConfigWrites` MUST be ignored (`configRepoFor` checks `externalConfigRepo` first), so an external mirror never diverges from its upstream.

2.5.3. With `directConfigWrites`, an agent's unlock of `settings_locked` is allowed too, since the guard it is routed through allows everything.

### 2.6 What the UI renders {#SM-6}

2.6.1. **Settings page** (`SettingsPage.jsx`):
- `external = externalConfigRepo != "" && section ∉ NOT_CONFIG` where `NOT_CONFIG = {config-repo, preferences, upgrade}`. In external mode every other section renders read-only (a disabled fieldset through `SettingsReadOnlyContext`) under the "Configuration is read-only and managed in …" banner.
- `lockedInUI` is `settings.settingsLocked` once `GET /api/settings` has loaded, otherwise `config.configRepoOnly || config.settingsLocked`; `configManaged = (lockedInUI || external) && isConfigRepoSection(section)`. Only the sections in `CONFIG_REPO_SECTION_PATHS` — **General, Git, Repositories, Tasks, Agents, Capabilities, CI, Memory** — render read-only when locked, with a `ConfigManagedNotice` linking `/config?path=…` for each path.
- A `SettingsCard` with `allowInExternalMode` escapes the read-only context: *Legacy capabilities* (its deployment-wide fieldset still honours `readOnly`), *Capability servers* (the panel draws its own read-only form from `readOnly`), *Task fields* (display-only), and *Settings lock* (only while `!external`, so the lock can be turned off from a locked General section; disabled when `GET /api/config` reports neither `configRepo` nor `externalConfigRepo`).
- The settings search marks a hit "Read-only: in the config repo" when its section is a config-repo section and `configRepoOnly`.

2.6.2. **Repo page** (`RepoPage.jsx`): `configRepoOnly = config.configRepoOnly || config.settingsLocked`; every configuration control is disabled with the config-repo notice as its `aria-describedby`. Operational actions (add a branch, import issues, manual release, base-branch run) are gated only by repo-admin rights (`adminReadOnly`).

2.6.3. **Every other page** — Boards, Processes (schedules / templates / suites), console sessions, the Memory panel outside Settings — has no lock-aware rendering. A refused write comes back as the 409 and `api.js` turns it into the `ConfigRefusal` dialog ("Edit in config repo", with the patch).

### 2.7 External config repository {#SM-7}

2.7.1. When `state-repo.json` names a `remote`, every guard class (S, S·u, B, G) MUST refuse with `config_managed_externally` (`internal` absent, `repo` the remote's slug, `url` its GitHub page when it is one), whatever `settings_locked` and `directConfigWrites` say (`TestExternalModeRefusesEachConfigWriteWithTheEditToCommit`, `TestExternalConfigRepoRefusesSettingsMutationsAndExposesMode`).

2.7.2. A route that would store a secret before its config write (`SetCapabilityServer` with secret values, `BindCapabilityServerSecret`, git connections, release secrets, GCP keys) MUST refuse before touching the secret store (`TestExternalModeRefusesACapabilityServerSecretBeforeStoringIt`).

2.7.3. Unguarded routes (class —) are still written in external mode: secrets, tokens, memory, release notes, and every runtime action. Changes to declarative configuration arrive only by commits to the external repository, imported on sync.

2.7.4. Switching mode is itself an action: `POST /api/state-repo/remote` (`settings:write`) names or clears the external remote and is not guarded.

### 2.8 `.grain/settings.yml` and repository declarations {#SM-8}

2.8.1. When `declarations` is on for a repository (`PUT /api/repos/{owner}/{name}/declarations`), the declaration sync reads `.grain/settings.yml` (and `.grain/{workflows,templates,suites,schedules,triggers}/…`, `merge-rules.yml`, `lifecycle.yml`, `review.yml`, `**/*.grain.md`) from the default branch. Its settings keys are exactly `prompt-extension`, `setup-command`, `port-branches` and `port-types` (`pkg/repodecl/settings.go`).

2.8.2. Declared values are stored in their **own** columns — `repo_config.declared_prompt_extension`, `declared_setup_command`, `declared_port_branches`, `declared_port_types` — owned by the declaration sync. They MUST NOT be writable through the UI, the REST API, or a `grain/config` edit that the next sync would not overwrite; the UI shows them read-only ("Declared by {repo} itself in .grain/settings.yml"). Because the sync rewrites them only when the repository's default branch moves, `ExportConfig` leaves them out of `tables/repo_config.json` and applying a settings change keeps the values the database holds (`staterepo.declaredColumns`, beside `withheldColumns`); `grain state check` warns, naming `.grain/settings.yml`, when a file carries one (`TestTheRepositoryDeclaredColumnsAreNeverSetFromTheConfigRepository`).

2.8.3. They **compose with** (`Compose: Append`, `pkg/declschema`) rather than override the operator's columns: the declared prompt extension and setup command run after the operator's, and the declared port branches/types are unioned with `repo_config.port_branches`/`port_types`. The operator's own field stays writable (subject to the lock) and stays meaningful.

2.8.4. Keys that would grant authority — `default-capabilities`, `capabilities`, cross-repo `reads`/`writes`, `target` and their spellings — MUST be rejected at parse time (`declschema` `Authority` keys), so a repository cannot grant itself privilege.

### 2.9 Agent versus person gates {#SM-9}

2.9.1. Every route maps to an `api:<area>:<verb>` scope (`pkg/ui/route_scopes.go`). The **Dangerous** scopes — `settings:write`, `connections:write`, `capabilities:admin`, `secrets:write`, `tasks:admin`, `merge:force`, `deployment:{upgrade,restart,reboot}` (`pkg/model/agent_api.go`) — are grantable only by a person. Agent writes are attributed and audited with the reason the call gave.

2.9.2. An agent MUST NOT unlock `settings_locked` through the API while the lock is effective (§2.3.4). It MAY turn the lock **on** (that is a bootstrap-only change) and it MAY propose `settings_locked: false` in a `grain/config` PR.

2.9.3. Every path in `grain/config` is classified `StatePrivilege` (`RequiresHuman: true`) by `trustboundary.ClassifyState`, so a config PR waits for a person's **Submit** unless `settings/config-policy.yml`'s `auto_merge_paths` exempts it. `NeverAutoMergePaths` bounds that exemption: `settings/config-policy.yml`, `settings/{capabilities,new-task-form,templates,schedules,suites}.yml`, `tables/{grain_config,repo_config,api_scope_set,agent_backend,repo_workflow}.json`, every `template*`/`schedule*`/`suite*`/`qualification*`/`standing_sandbox*`/`persistent_sandbox`/`gcp_key` table, `scripts/**`, `workflows/**`, `.github/**`, `.grain/**` and `schema-version` always need a person. Paths that MAY be exempted include `memory/**`, `release-notes/**`, `tables/board*.json`, `tables/custom_field.json`.

### 2.10 Per-mode write matrix by surface {#SM-10}

2.10.1. The table below is the summary of §2.1–§2.9: for each settings surface, **who** may write it in each of the four modes. §3 is the per-setting detail behind each row, and §4 lists where the code departs from it. Writers:

- **P**: a person, through the UI.
- **R**: the REST API called by a person or operator credential (no `X-Grain-Acting-Task`). The server applies the same guard as for **P**, but without the UI's read-only rendering (§2.6).
- **A**: an agent, through the scoped agent API (`agentAPIGate`, `pkg/ui/agent_api_gate.go`). The acting task MUST hold the route's `api:<scope>` grant for the repository the call names. A **Dangerous** scope (§2.9.1) also needs a reason (`X-Grain-Acting-Reason`), and it is refused on the `sandbox-cli` channel together with `repos:write` (`IsSandboxDeniedAPIScope`). So the agent rows here are reachable only by a run that a person granted the scope to, over the model channel. Where **A** is allowed it is the same write as **R**, behind the same config guard.
- **C**: a config PR, meaning a commit merged into the config repository and imported by `staterepo.Apply` on the next tick (§2.2.2). In modes 2 and 3 that is `grain/config`, opened by a person, through `POST /api/state-repo/pull-requests` (`settings:write`), or by a task with `writes:` on `grain/config`. Every path needs a person's **Submit** unless it is auto-merge-exempt (§2.9.3). In mode 4 it is a pull request on the external remote, merged under that repository's own rules and applied on Grain's next sync.
- **409**: the write is refused with `config_managed_externally`, and the refusal carries the edit to commit instead (§2.10.3).
- **—**: no writer on that path.

| Surface | 1. Standalone / demo (`StateRepo == nil`) | 2. Internal, unlocked | 3. Internal, `settings_locked` | 4. External config repo |
| --- | --- | --- | --- | --- |
| **Bootstrap `grain_config` fields** (`clearBootstrap`, §2.4: models and connections, sandbox resources/backend/image/`k8s*`, `maxWorkers`, CLI run locations, `settingsLocked` itself) | P, R, A (*Settings lock* disabled in the UI without a config repo, §2.1). No C. | P, R, A, each synced to `grain/config` (`SyncAfterWrite`); C | P, R, A; C. Guard **B** / **G** with `bootstrapOnlyChange` allows the write. Exception: an agent turning the lock **off** goes through **S** and gets 409 (§2.9.2). | C only. P / R / A get 409. Guard **B** refuses in external mode. |
| **Config-managed `grain_config` fields** (every other column, §2.4) | P, R, A, even if `settings_locked` is set on the row (D-12, fixed). No C. | P, R, A (synced); C | C only. P / R / A get 409 (`internal: true`, rehearsed patch to `tables/grain_config.json` or `settings/capabilities.yml`). The UI greys out each such field it draws in a bootstrap or operations section (D-1, D-2, fixed 2026-11-01). | C only. P / R / A get 409. |
| **`repo_config`** (repo page, `PUT /api/repos/{o}/{n}/…`, guard **S**) | P, R, A (`repos:*`, `capabilities:admin` for grants, without self-escalation) | P, R, A (synced); C | C only. P / R / A get 409. Release secrets get 409 too (**S·u**, D-6). | C only. P / R / A get 409. |
| **Composed entities**: templates, schedules, suites, qualifications, boards, custom fields, API scope sets, standing / persistent sandboxes, `repo_workflow` (guard **S**) | P, R, A | P, R, A (synced); C | C only. P / R / A get 409. The UI is not lock-aware for boards and processes (D-4). An open console session's live task row stays writable (D-5). | C only. P / R / A get 409. |
| ↳ **Memory** (`memory/**`; no guard) | P, R, A (`memory:write`) | P, R, A; C | P, R, A (the UI renders it read-only, D-3); C | P, R, A, until the next import replaces them (D-3); C |
| ↳ **Repository declarations** (`.grain/settings.yml`, …) | A PR on the target repository, in every mode, by whoever may write that repository. These are composed with the operator's value, never written through Grain's settings API (§2.8, D-13). | ← | ← | ← |
| **Secrets** (`<data-dir>/secrets`: agent keys, connection credentials, tokens, `PUT /api/secrets/…`; no guard) | P, R, A (`secrets:write` / `connections:write`, person-granted). Never C: not exported. | same | same (except D-6, D-7) | P (D-9, fixed: Legacy capabilities, Capability servers, model-connection credentials and executor join tokens stay usable; Git's token controls point to Legacy capabilities), R, A. Capability-server bindings get 409 before the value is stored (§2.7.2). Never C. |
| **`<data-dir>/state-repo.json`** (host-local, not in the database) | — (`POST /api/state-repo/remote` answers 501). Operator: edit the file, or `grain state repo <url>`. | `remote` / `branch` / `tokenFile`: P (Config repo page), R, A (`settings:write`) via `POST /api/state-repo/remote`, which calls no config guard; operator CLI or file. `directConfigWrites`, `noWorkflow`, `postCheck`, `checkImage`, `localOnlyAcknowledged`: operator file edit only. Never C. | same | same: `POST /api/state-repo/remote` with `"none": true` is how a deployment leaves external mode. |
| **Browser preferences** (`localStorage`: theme, time zone, timeline order, sidebar) | P, in that browser only | same | same | same. Never read-only (`NOT_CONFIG`). |

2.10.2. **The `directConfigWrites` escape hatch.** `"directConfigWrites": true` in `state-repo.json` (`staterepo.Settings.DirectConfigWrites`, read through `LoadSettings` on every guarded write, so it needs no restart) MUST make column 3 behave exactly like column 2. `configRepoFor` returns "not managed" before it reads `settings_locked`, `configRepoOnly` reports false, an agent may unlock, and guarded writes sync through `SyncAfterWrite`. It MUST NOT change column 4, because `configRepoFor` checks `externalConfigRepo` first. The flag can only be set by whoever can edit the data directory: no UI, REST route, agent scope or config PR writes it. The Settings and repository pages draw no lock while it is set, because `configRepoOnly` is false then (D-10, fixed).

2.10.3. **The refusal and its rehearsal.** Each **409** cell above is the same answer: `code: "config_managed_externally"`, `repo`, `branch`, `path`, `change` and, for an agent, `access` (§2.3.2, §2.7.1). How the edit is filled in depends on the guard:

- **Rehearsed guards (S, B, G).** `guardConfigWrite` *arms* the request's `configWriteRehearsal` rather than failing. The handler's write then runs inside `staterepo.Rehearse`, between two exports of the configuration on a transaction that is rolled back. The diff becomes `change`, and `path` lists the files in it. Changes that touch only `*_sequence.json` are carried forward to the next rehearsed write instead of being answered. If the rehearsal fails, or the handler never reaches a write, the refusal goes out with only the paths of the guard's tables (§2.3.3).
- **Unrehearsed guards (S·u, B·u).** These refuse at once, with `path` and a `description`, before any side effect.

Either way the database is unchanged and nothing syncs. A person or a task holding `settings:write` can pass `change` to `POST /api/state-repo/pull-requests` as it is. A task whose `access.canWrite` is true can commit it to the config repository directly. Grain's own writes, made without a `WriteRehearsal` in context, are never refused (§1.3).

## 3. Settings mutability and lock matrix

Columns:
- **UI (unlocked)** — `Yes` (editable), `No` (no UI control), `Write-only` (value is never shown back), `Browser-local`, or `Action` (imperative, not standing config); then the UI path and REST route.
- **Config repo** — `Yes` + file, or `No` + reason. `tables/grain_config.json` is the `grain_config` row; `settings/capabilities.yml` projects `grain_config.mcp_servers`, `capability_policy`, `default_capabilities`, `api_scope_set` and `repo_config.{default,ci}_capabilities` and wins where it speaks.
- **Locked (`settings_locked: true`)** — server guard result / UI rendering. `Locked` = refused with 409 / rendered read-only. `Unlocked (…)` names why it stays writable.
- **External** — whether the server refuses it in external config-repo mode.

### 3.1 Models (`/settings/models`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Model connections `agentBackends` (framework, models, CLI path, …) | Yes — Models; `PUT /api/settings`, `PUT`/`DELETE /api/deployment/agent-backends/{id}` | Yes — `tables/grain_config.json` (`agent_backends`) | Unlocked (Bootstrap; G / B) / editable | Yes | `connections:write`. Edits lift a connection's usage-limit pause and re-run model discovery. |
| Default model `defaultModel`, pinned connection `agentBackendId`, per-model `modelSettings` | Yes — Models → defaults; `PUT /api/deployment`, `PUT /api/settings` | Yes — `tables/grain_config.json` | Unlocked (Bootstrap; G) / editable | Yes | |
| `judgementModel` | No | Yes — `tables/grain_config.json` | Unlocked (Bootstrap) / — | — | No route sets it; config repo is the only writer. |
| CLI run locations `cliRun` | Yes — Models → CLI run locations; `PUT /api/settings/cli-run` | Yes — `tables/grain_config.json` | Unlocked (Bootstrap; B) / editable | Yes | |
| Model connection credential / agent key / Codex auth | Write-only — Models; `PUT`/`DELETE /api/agent-backends/{id}/credential`, `/api/agent-keys/{framework}`, `/api/codex-auth`, `/api/agent-backends/{id}/codex-auth` | No — secret material (`<data-dir>/secrets`) | Unlocked (Secret; —) / editable | No | `secrets:write`. Stays editable in external mode, where the connection's own fields are drawn read-only (§4, D-9, fixed). |
| Key / connection checks | Action — `POST /api/agent-keys/{framework}/check`, `GET /api/agent-models` | No — action | Unlocked (Action) | No | |

### 3.2 Sandboxes (`/settings/sandboxes`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `sandboxBackend`, `sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`, overcommit ratios and adaptive `sandboxOvercommit`, `sandboxMemoryReserveMb`, `sandboxDiskFloor*`, `networkAccess` | Yes — Sandboxes; `PUT /api/settings` | Yes — `tables/grain_config.json` | Unlocked (Bootstrap; G) / editable | Yes | `sandboxBackend` is `restartRequired` (reports `pendingRestart`). |
| `sandboxImage`, `sandboxImageMirror` | Yes (image) / No (mirror) — Sandboxes → Custom sandbox image; `PUT /api/settings` | Yes — `tables/grain_config.json` | Unlocked (Bootstrap; G) / editable | Yes | |
| Kubernetes `k8sNamespace`, `k8sKubectl`, `k8sSandboxImage`, `k8sGranuleImage`, `k8sWorkspace`, `k8sGitProxyHost`, `k8sSandboxService`, `k8sServiceAccount`, `k8sNodeSelector`, `k8sPrivileged`, `k8sNetworkPolicy`, `k8sDeployment`, `k8sContainer`, `k8sPriorityClass{Interactive,Agent,Ci}` | Yes — Sandboxes; `PUT /api/settings` | Yes — `tables/grain_config.json` | Unlocked (Bootstrap; G) / editable | Yes | The first nine are `restartRequired`. |
| Session sandbox idle TTL `sessionSandboxIdleTtl` | Yes — Sandboxes; `PUT /api/settings` | Yes — `tables/grain_config.json` | **Locked** (non-bootstrap; G) / **read-only** | Yes | D-1 (fixed). |
| Standing / persistent sandboxes (image, env, bound secret keys) | Action only — System → Sandboxes; `POST /api/persistent-sandboxes/{name}/{action}` (stop/start/restart/rebuild) | Yes — `tables/standing_sandbox.json`, `standing_sandbox_secret.json`, `standing_sandbox_env.json`, `persistent_sandbox.json` | Unlocked (Action) | No | Declarations are config-repo only; never auto-merge. |
| Sandbox probe | Action — `POST /api/sandbox-probe` | No — action | Unlocked (Action) | No | |

### 3.3 Executors (`/settings/executors`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Remote executor tokens | Write-only — Executors; `POST /api/executors/tokens`, `POST …/{id}/revoke`, `DELETE …/{id}` | No — runtime credential (`executor_token`, runtime table) | Unlocked (Secret; —) / editable | No | `secrets:write`; token shown once. Stays editable in external mode (D-9, fixed). |
| External agent tokens | Write-only — `POST /api/external-agents/tokens`, `…/{id}/rotate`, `…/{id}/revoke`, `…/revoke-all`, `DELETE …/{id}` | No — runtime credential | Unlocked (Secret; —) | No | `capabilities:admin`. |
| External agent API on/off `externalAgentApi` | No — API only (`PUT /api/settings`) | Yes — `tables/grain_config.json` | Locked (G) / — | Yes | |

### 3.4 Legacy capabilities / secrets (`/settings/credentials`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Secret values (`<secret>/<key>`) | Write-only — Legacy capabilities; `PUT /api/secrets/{secret}/{key}`, `DELETE /api/secrets/{secret}[/{key}]`, `PUT /api/tasks/{id}/secret` (`request_secret`) | No — secret material; config holds only `$secret:<name>` / `<secret>/<key>` references | Unlocked (Secret; —) / editable | No | `secrets:write`. `statecheck` rejects raw credentials in config PRs. |
| GitHub tokens | Write-only — `PUT`/`DELETE /api/github-tokens/{name}` | No — secret material | Unlocked (Secret; —) | No | Setting the first token also records a default git credential, only when `settingsWritable` (not external). |
| GCP keys (metadata + key material) | Write-only — `POST /api/gcp-keys`, `PUT`/`DELETE /api/gcp-keys/{name}` | Metadata Yes — `tables/gcp_key.json`; material No | Unlocked (Bootstrap; B·u) / editable | Yes | Never auto-merges. |
| GitHub host `githubHost`, `githubInsecureHttp` | Yes — Legacy capabilities → deployment-wide; `PUT /api/settings` | Yes — `tables/grain_config.json` | **Locked** (G) / **read-only** | Yes | D-2 (fixed). |
| GCP project / SA `gcpProject`, `gcpServiceAccountEmail`, `gcpServiceAccountCount` | Yes — Legacy capabilities; `PUT /api/settings` | Yes — `tables/grain_config.json` | **Locked** (G) / **read-only** | Yes | D-2 (fixed). |
| IAP `iapBackendService`, `iapResource` | No — API only (`PUT /api/settings`) | Yes — `tables/grain_config.json` | Locked (G) | Yes | Triggers an IAP sync. |
| Legacy capability catalogue entries (`settings.capabilities`) | Yes — Legacy capabilities panel (secrets bound to them) | Yes — `settings/capabilities.yml` | Secret parts Unlocked; entries Locked (S) | Yes | |

### 3.5 Access (`/settings/access`)

| Setting / entity | UI (unlocked) | Config repo | Locked | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Access code / local API token | No — read-only display; sign-in via `POST /api/auth/access-code`, `POST /api/auth/logout` | No — access lockout safety (`GRAIN_ACCESS_CODE`, `-access-code-file`, `<data-dir>/local-api-token`) | Unlocked (Access) | No | |
| Cloud IAP / identity verifier (`-iap-audience`, …) | No | No — startup flag | Unlocked (startup) | No | |
| Admin users / accounts (`-add-admin`, `-admin-user`) | No | No — startup flag / CLI | Unlocked (startup) | No | |

### 3.6 Preferences (`/settings/preferences`)

| Setting / entity | UI (unlocked) | Config repo | Locked | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Theme, time zone, timeline order, sidebar pins/visibility | Browser-local — `localStorage` (`PreferencesContext.jsx`) | No — browser storage | Unlocked (Browser) | No | Never read-only (`NOT_CONFIG`). |

### 3.7 Upgrade (`/settings/upgrade`) and General → Updates

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Upgrade now / scheduled / Postgres upgrade | Action — `POST /api/upgrade`, `DELETE /api/upgrade/scheduled`, `POST /api/upgrade/postgres` | No — action | Unlocked (Action) | No | `deployment:upgrade`. Never read-only (`NOT_CONFIG`). |
| `upgradeCheckMode`, `upgradeCheckPausesPipeline`, `autoUpgradeChannel`, `autoUpgradeTime` | Yes — General → Updates; `PUT /api/settings` | Yes — `tables/grain_config.json` | Locked (G) / read-only | Yes | |
| Power (restart, reboot, shutdown) | Action — System → Power; `POST /api/host/{restart,reboot,shutdown}` | No — action | Unlocked (Action) | No | |

### 3.8 Caches (`/settings/caches`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `packageCacheEnabled`, `packageCacheSizeGb`, `actionsCacheEnabled`, `actionsCacheSizeGb` | Yes — Caches; `PUT /api/settings` | Yes — `tables/grain_config.json` | **Locked** (G) / **read-only** | Yes | D-1 (fixed). |
| Flush package / actions / CI cache | Action — System → Caches; `POST /api/package-cache/flush`, `POST /api/actions-cache/flush`, `DELETE /api/ci/cache` | No — action | Unlocked (Action) | No | `caches:write`. |

### 3.9 Engine (`/settings/engine`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `maxWorkers` | Yes — Engine; `PUT /api/settings` | Yes — `tables/grain_config.json` | Unlocked (Bootstrap; G) / editable | Yes | |
| `pollInterval`, `maxMergesPerRepo`, `mergeBatchSize`, `mergeGatherWindow` | Yes — Engine; `PUT /api/settings` | Yes — `tables/grain_config.json` | **Locked** (G) / **read-only** | Yes | D-1 (fixed). |
| `maxMergers`, `maxCIJobs`, `baseCIRedStreak`, `agentFramework`, `defaultFilters` | No | Yes — `tables/grain_config.json` | Locked (no route) | — | Retired or config-only columns. |
| Pipeline pause | Action — System → Pipeline; `POST`/`DELETE /api/pipeline-pause`, `DELETE /api/pause` | No — action | Unlocked (Action) | No | `dispatch:write`. |
| Database migrate / test | Action — `POST /api/settings/db/migrate-to-postgres`, `POST /api/database/{test,upgrade}` | No — action | Unlocked (Action) | No | `settings:write`. |

### 3.10 Config repo (`/settings/config-repo`)

| Setting / entity | UI (unlocked) | Config repo | Locked | External | Notes |
| --- | --- | --- | --- | --- | --- |
| External remote / branch | Action — Config repo; `POST /api/state-repo/remote` | No — host-local `<data-dir>/state-repo.json` (also `-external-config-repo`, `-external-config-branch`) | Unlocked (Action) | No | Switches mode (§2.7.4). |
| `directConfigWrites` | No | No — host-local `state-repo.json`, read per request | — | Ignored | §2.5. |
| Secrets key / secrets repo | Write-only — `POST /api/state-repo/secrets-key` (`import-key`), `POST /api/state-repo/secrets-repo` | No — secret material | Unlocked (Secret) | No | |
| Sync now, open config PR, dismiss reset notice | Action — `POST /api/state-repo/sync`, `/pull-requests`, `/reset-notice` | No — action | Unlocked (Action) | No | The PR route is how a refusal's `change` becomes a PR. |

### 3.11 General (`/settings/general`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `environmentName`, `showClosedByDefault` | Yes — General → Deployment; `PUT /api/settings` | Yes — `tables/grain_config.json` | Locked (G) / read-only | Yes | |
| Settings lock `settingsLocked` (`settings_locked`) | Yes (disabled without `configRepo`) — General → Settings lock; `PUT /api/settings`, `PUT /api/config` | Yes — `tables/grain_config.json` (`settings_locked`) | Unlocked (Unlock toggle; bootstrap) / editable (`allowInExternalMode={!external}`) | Yes | Person only for unlock (§2.9.2). Disabled in General when no config repo exists (D-12, fixed). |
| Deployment time zone `timeZone` | No — API only (`PUT /api/settings`); the UI's time zone is the browser preference | Yes — `tables/grain_config.json` | Locked (G) | Yes | |

### 3.12 Git (`/settings/git`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Git connections `gitConnections` (metadata) and default `gitCredential` | Yes — Git; `POST /api/deployment/git-connections`, `DELETE …/{cred}`, `PUT /api/deployment` | Yes — `tables/grain_config.json` (`git_connections`, `git_credential`) | Locked (S·u / G) / read-only | Yes | `connections:write`. Token stored write-only under Credentials. Refused before the token is stored. |
| Per-repo connection binding | Yes — Git → repositories; `PUT /api/deployment/repos/{owner}/{name}/git-connection` | Yes — `tables/repo_config.json` (`git_credential`) | Locked (S·u) / read-only | Yes | |
| Connection check, ambient sources | Action — `POST /api/deployment/git-connections/check`, `GET /api/git-ambient-sources` | No — action | Unlocked (Action) | No | |
| Commit identity `agentGitName`, `agentGitEmail` | No — API only (`PUT /api/deployment`) | Yes — `tables/grain_config.json` | Locked (G) | Yes | Agents → Commit identity shows the defaults. |

### 3.13 Repositories (`/settings/repositories`) and per-repo settings (`/repos/{owner}/{name}`)

Every per-repo setter goes through `putRepoConfig` (class **S** on `repo_config`) plus `checkRepoPerson` (the caller must administer the repo). RepoPage renders all of them read-only when locked (§2.6.2).

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Target repos `targetRepos` (add / remove), clone URL | Yes — Repositories / repo page; `POST /api/repos`, `DELETE /api/repos/{owner}/{name}`, `PUT /api/deployment` | Yes — `tables/grain_config.json` (`target_repos`), `tables/repo_config.json` (`clone_url`) | Locked (S / G) / read-only | Yes | `repos:write`. |
| Default capabilities, CI capabilities | Yes — repo page; `PUT /api/repos/{o}/{n}/capabilities`, `…/ci-capabilities` | Yes — `settings/capabilities.yml` (over `tables/repo_config.json`) | Locked (S) / read-only | Yes | `capabilities:admin`; agents may not self-escalate (`agentGrantEscalation`). |
| Prompt extension | Yes — repo page; `PUT …/prompt-extension` | Yes — `tables/repo_config.json` | Locked (S) / read-only | Yes | Declared layer appends (§2.8). |
| Setup command | Yes — repo page; `PUT …/setup-command` | Yes — `scripts/repos/<owner>/<name>/setup.sh` | Locked (S) / read-only | Yes | Declared layer appends (§2.8). |
| Declarations on/off | Yes — Repositories; `PUT …/declarations` | Yes — `tables/repo_config.json` | Locked (S) / read-only | Yes | |
| `requireBranchUpToDate`, `watchDefaultBranch`, `mergeOnGreen`, `baseRepairBypassesHumanGates`, `ignoreGitHubCI`, `ciMode`, `unfixableChecks`, bulk `PUT …/settings`, `PUT /api/deployment/repos/{o}/{n}/config` | Yes — repo page | Yes — `tables/repo_config.json` | Locked (S) / read-only | Yes | |
| Release workflow (enabled, base-branch CI jobs, post-merge dispatch jobs) | Yes — repo page; `PUT …/release` | Yes — `tables/repo_config.json` | Locked (S) / read-only | Yes | `releases:write`. |
| Release secrets | Write-only — repo page; `PUT`/`DELETE …/release-secrets/{secret}` | Names Yes — `tables/repo_config.json`; values No | **Locked** (S·u) / read-only | Yes | Discrepancy D-6 (secret refused while locked). |
| Work ownership | Yes — repo page; `PUT …/work-ownership` | Yes — `tables/repo_config.json` | Locked (S) / read-only | Yes | |
| Repo CI workflows | Yes — repo page; `PUT …/workflows` | Yes — `tables/repo_workflow.json` and `workflows/<owner>/<name>/…` | Locked (S) / read-only | Yes | |
| Qualification plan | Yes — repo page; `PUT …/qualification-plan` | Yes — `tables/qualification_config.json`, `qualification_item.json`, `qualification_item_depends_on.json` | Locked (S) / read-only | Yes | `releases:write`. |
| Repair template `repairTemplateID` (repo and deployment) | No — API only (`PUT /api/settings` for the deployment) | Yes — `tables/grain_config.json`, `tables/repo_config.json` | Locked (G / S) | Yes | |
| Port branches / types (operator) | No | Yes — `tables/repo_config.json` (`port_branches`, `port_types`) | Locked (no route) | — | Unioned with the declared layer. |
| Declared prompt extension / setup command / port branches / port types | No — read-only display | No — owned by the declaration sync from `.grain/settings.yml` | n/a | n/a | §2.8. |
| Branch, issue import, release run, candidate promote/approve, lifecycle refresh, base-branch run | Action — repo page | No — action | Unlocked (Action) | No | Repo-admin gated only. |

### 3.14 Tasks (`/settings/tasks`) and custom fields

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `approvedByDefault`, `autoMergeByDefault`, `newestFirst`, `maxAutoSubmitDepth`, `maxAutoSubmitFanOut` | Yes — Tasks; `PUT /api/settings` | Yes — `tables/grain_config.json` | Locked (G) / read-only | Yes | |
| Deployment default capabilities `defaultCapabilities` | Yes — Tasks → Task defaults; `PUT /api/settings` | Yes — `settings/capabilities.yml` (over `grain_config.default_capabilities`) | Locked (G) / read-only | Yes | Agent grant-escalation checked. |
| Custom fields | No — Tasks → Task fields is display-only | Yes — `tables/custom_field.json` | Locked (no route) / display | — | Task *values* are task data, not settings. |

### 3.15 Agents (`/settings/agents`) and console sessions

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `maxAgentTurns`, `promptExtension`, `taskBranchTemplate`, `deploymentId` | Yes — Agents; `PUT /api/settings` | Yes — `tables/grain_config.json` | Locked (G) / read-only | Yes | |
| Standing console sessions `consoleSessions` | Yes — sessions pane; `POST /api/console-sessions`, `PUT`/`DELETE /api/console-sessions/{id}` | Yes — `tables/grain_config.json` (`console_sessions`) | Locked (S / S·u) / **not gated** in the sessions pane | Yes | `sessions:write`. Exception: while locked, a `PUT` that edits an *open* session's title or parameters skips the guard and updates only the live task row; the declaration in `grain_config` is not written (D-5). |
| Session restart / cancel, console input / resize / close, session rename | Action — `POST /api/console-sessions/{id}/{restart,cancel}`, `/api/console/{id}/…`, `PUT /api/tasks/{id}/session-title` | No — action | Unlocked (Action) | No | |

### 3.16 Capabilities (`/settings/capabilities`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Capability servers (add/edit, remove, enable, disable, dismiss notice) | Yes — Capabilities → servers; `PUT /api/capability-servers`, `POST /api/capability-servers/{remove,enable,disable,dismiss-notice}` | Yes — `settings/capabilities.yml` (over `grain_config.mcp_servers`) | Locked (S; S·u first when secrets are given) / read-only | Yes | `capabilities:admin`. |
| Capability server secret binding | Write-only — `POST /api/capability-servers/bind-secret` | Reference Yes (`$secret:<ref>` in `settings/capabilities.yml`); value No | **Partly**: value stored (B·u passes), reference refused (S) / read-only | Yes (refused before storing) | Discrepancy D-7. |
| Rotate managed token | Action — `POST /api/capability-servers/rotate-managed` | No — secret material | Unlocked (Secret; —) | No | |
| Capability policy | Yes — Capabilities → policy; `PUT /api/capability-policy` | Yes — `settings/capabilities.yml` (over `grain_config.capability_policy`) | Locked (S) / read-only | Yes | |
| API scope sets | Yes — Capabilities → API scope sets; `PUT /api/api-scope-sets`, `POST /api/api-scope-sets/delete` | Yes — `tables/api_scope_set.json` / `settings/capabilities.yml` | Locked (S) / read-only | Yes | Validated by `statecheck.APIScopeSets`. |
| Tests, previews, grant checks | Action — `POST /api/capability-servers/{test,test-script,gchat-test}`, `/api/capability-policy/preview`, `/api/capability-grants/check`, `/api/capabilities/{id}/check` | No — action | Unlocked (Action) | No | |

### 3.17 CI (`/settings/ci`)

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `ciRunHistory`, `ciStepOutputMb`, `ciStepOutputDays`, `ciRunOutputKb`, `ciCacheMb`, `ciCacheEntries`, `ciCacheDays`, `ciArtifactMb`, `ciArtifactRunMb`, `ciArtifactFileMb`, `ciArtifactDays`, `ciJobsPerRun`, `ciAgeBoost`, `ciReserveAfter` | Yes — CI; `PUT /api/settings` | Yes — `tables/grain_config.json` | Locked (G) / read-only | Yes | |
| CI runs, quarantine | Action — `POST /api/ci/runs`, `DELETE /api/ci/runs/{id}`, `POST /api/tests/quarantine` | No — action / runtime | Unlocked (Action) | No | |

### 3.18 Memory (`/settings/memory`) and release notes

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Deployment and repo memory | Yes — Memory; `PUT /api/memory` | Yes — `memory/deployment.md`, `memory/repos/<owner>/<name>.md` (`memory/teams/…` for legacy team scopes) | **Unlocked** (—) / **read-only** | No | `memory:write`; the memory agent writes it too. Discrepancy D-3. |
| Memory documents | Yes — Memory; `PUT /api/memory/documents` | Yes — `memory/documents/deployment/<doc>.md`, `memory/documents/repos/<owner>/<name>/<doc>.md` | **Unlocked** (—) / **read-only** | No | Discrepancy D-3. |
| Weekly release notes | Yes — release-notes pane; `PUT /api/release-notes` | Exported only — `release-notes/<year>-W<week>.md`; **not imported** by `Apply` | Unlocked (—) | No | Discrepancy D-8. `release-notes:write`. |

### 3.19 Boards

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Boards, columns, panels, sections | Yes — Boards; `POST /api/boards`, `PATCH`/`DELETE /api/boards/{id}` | Yes — `tables/board.json`, `board_column.json`, `board_panel.json`, `board_section.json`, `board_sequence.json` | Locked (S) / **not gated** (409 dialog) | Yes | `boards:write`. May be exempted via `auto_merge_paths`. D-4. |

### 3.20 Processes — schedules, templates, suites

| Setting / entity | UI (unlocked) | Config repo | Locked: server / UI | External | Notes |
| --- | --- | --- | --- | --- | --- |
| Schedules | Yes — Processes; `POST /api/schedules`, `PATCH`/`DELETE /api/schedules/{id}` | Yes — `settings/schedules.yml` (over `tables/schedule*.json`) | Locked (S) / **not gated** (409 dialog) | Yes | `schedules:write`. D-4. |
| Templates | Yes — Processes; `POST /api/templates`, `PATCH`/`DELETE /api/templates/{id}` | Yes — `settings/templates.yml` (over `tables/template*.json`) | Locked (S) / not gated | Yes | `templates:write`. D-4. |
| Suites | Yes — Processes; `POST /api/suites`, `PATCH`/`DELETE /api/suites/{id}` | Yes — `settings/suites.yml` (over `tables/suite*.json`) | Locked (S) / not gated | Yes | `suites:write`. D-4. |
| Standing-work pre-authorisation | (inside the above) `authorizeScheduleEdit` / `…TemplateEdit` / `…SuiteEdit` | — | Locked (S) | Yes | |
| Run now, compositions, suite runs, task from template | Action — `POST /api/schedules/{id}/run`, `/api/{schedules,suites}/composition`, `/api/suite-runs`, `/api/tasks/from-template` | No — action | Unlocked (Action) | No | |

### 3.21 Daemon startup-only flags

| Setting / entity | UI | Config repo | Locked | External | Notes |
| --- | --- | --- | --- | --- | --- |
| `-data-dir`, `-db-driver` / `-db-dsn`, `-addr` and listener/TLS flags, `-executor-addr` / `-executor-cert-file` / `-executor-key-file`, `-sandbox-port-base`, `-granule-*`, `-container-*`, `-iap-audience`, `-access-code-file`, `-external-config-repo` / `-external-config-branch`, backup flags | No | No — startup flag: they open the store, secret store and listeners the config repo is read through | n/a | n/a | Some (e.g. `-github-host`, `-environment-name`, `-deployment-id`, `-ci-*`, `-default-capabilities`) only **seed** a missing or `ConnectionsOnly` `grain_config` row; afterwards the row (and so `grain/config`) wins. |

## 4. Discrepancy callouts

Each item is a mismatch between what the React UI lets somebody edit and what the server enforces, or between the code and a rule of §2. Each is a candidate for a follow-up that either changes the UI gating or the server guard, with a test.

### D-1: Engine, Caches and `sessionSandboxIdleTtl` are editable in the UI but refused while locked

*Fixed in the UI 2026-11-01 (`docs/test-runs/2026-11-01-settings-bootstrap-operations-verification.md`): `caches` is in `CONFIG_REPO_SECTION_PATHS`, so the section is read-only with the config-managed notice while locked; Engine disables `pollInterval` and the three merge-queue fields one by one and keeps `maxWorkers` editable; Sandboxes disables `sessionSandboxIdleTtl` only. All four sections key this on the page's `configRepoOnly` (`s.configLocked`).*

`CONFIG_REPO_SECTION_PATHS` omits `engine`, `caches` and `sandboxes`, so with `settings_locked: true` those sections render editable. The server classifies only `maxWorkers` (Engine) and the Sandboxes resource/backend/k8s fields as bootstrap; `pollInterval`, `maxMergesPerRepo`, `mergeBatchSize`, `mergeGatherWindow`, all four cache fields and `sessionSandboxIdleTtl` are non-bootstrap and `PUT /api/settings` answers 409. The operator sees an editable field whose save fails with the config-repo dialog. Fix either side: mark the non-bootstrap fields read-only per field when locked, or add them to `clearBootstrap`.

### D-2: Legacy capabilities' deployment-wide fields are editable while locked

*Fixed in the UI 2026-11-01: the deployment-wide fieldset is disabled while locked as well as in external mode, with the config-managed notice; the secret entry below it stays writable.*

`githubHost`, `githubInsecureHttp`, `gcpProject`, `gcpServiceAccountEmail` (and `gcpServiceAccountCount`) live in the `credentials` section, which is not a config-repo section, so they are editable when locked; the server refuses them (non-bootstrap, G). In external mode the fieldset is disabled, so only the locked case differs.

### D-3: Memory is read-only in the UI when locked, but the API writes it in every mode

*Reproduced live 2026-11-01 (`docs/test-runs/2026-11-01-settings-ownership-lock.md`): `PUT /api/memory` answers 200 in locked internal mode and in external mode.*

`memory` is in `CONFIG_REPO_SECTION_PATHS`, so the Memory section renders read-only under the lock, but `PUT /api/memory` and `PUT /api/memory/documents` call no guard: they write in locked internal mode **and in external mode** (where the next import of the external repository's `memory/**` can then replace them). Either the server should guard memory like other config-repo content (keeping the memory agent's MCP `update_memory` path working), or the UI should stop gating it and memory should be documented as a runtime surface that is merely exported.

### D-4: Boards, Processes and console sessions are not lock-aware in the UI

Only `SettingsPage` and `RepoPage` read `configRepoOnly`/`settingsLocked`. Boards, schedules, templates, suites and standing console sessions render editable controls when locked (and in external mode) and rely on the server's 409 being turned into the `ConfigRefusal` dialog. The server is consistent (S); the UI is not gated.

### D-5: Editing an open console session while locked changes only the live task

When `configRepoOnly`, `UpdateConsoleSession` skips its guard for an edit to an *open* session's title or parameters (capabilities, network access, reads, writes, model, …) and updates only the live task row (`writable := !c.configRepoOnly(ctx)`). The standing declaration in `grain_config.console_sessions` is not changed, so the next restart of the session goes back to the declared values. This is intentional, but no answer or UI hint tells the person their edit lasts only until then. An edit to a session with no open task is refused (S).

### D-6: Release secrets are refused while locked

The rule of §2.3.4 keeps secret material writable under the lock, but `SetRepoReleaseSecret` / `DeleteRepoReleaseSecret` guard with `checkSettingsWritableUnrehearsed("repo_config")` because they also record the secret's name in `repo_config.release_secrets`. While locked a person cannot set a release secret value at all. The UI agrees (RepoPage is read-only), so this is a code-versus-rule mismatch, not a UI/API one.

### D-7: Binding a capability-server secret while locked stores the value, then refuses the binding

*Fixed 2026-11-01 (`docs/test-runs/2026-11-01-settings-deployment-work-verification.md`): `BindCapabilityServerSecret` stores the value only when the binding's `mcp_servers` write is not sent to grain/config, so a locked binding answers 409 with the `$secret:` reference to commit and stores nothing (`TestBindingASecretWhileLockedStoresNothing`).*

`BindCapabilityServerSecret` passes its bootstrap guard (allowed while locked), writes the value to the secret store, and then `updateCapabilityServers` runs `checkSettingsWritable`, which refuses with 409. The secret is stored without its `$secret:` reference in `mcp_servers`. In external mode it is refused before storing (§2.7.2). It should either be refused up front under the lock (S·u) or treat the binding as bootstrap.

### D-8: Release notes are exported to `grain/config` but never imported

`release_note` is exported as `release-notes/<period>.md` (`dump.go`, `ExportConfig`), but it is in neither `SettingsTables` nor `memoryTables`, so `staterepo.Apply` ignores a merged edit to `release-notes/**`; the next export writes the database's text back over it. Either import it or document the directory as export-only in the config repo's README.

### D-9: External mode blocks secret and executor entry in the UI that the API accepts

*Fixed 2026-11-01 (task 2232; confirmed open in `docs/test-runs/2026-11-01-settings-bootstrap-operations-verification.md`). The Models → Provider connections cards and the Executors → Executor join tokens card opt out of the read-only fieldset with `allowInExternalMode`. In external mode a connection's credential (and Codex login) can be set, cleared and tested from its editor, while New connection, Enable/Disable, Remove, a pause's Resume and the connection's agent type, authentication method and Connection form stay read-only (`ModelBackendPage`'s `configReadOnly`, which draws them in the same `ReadOnlyFieldset` as a read-only card). Model defaults and CLI run locations stay read-only. Git is unchanged: its read-only note points to Legacy capabilities, where tokens can be entered (`SettingsPage.test.jsx` "under an external config repo", `ExecutorsSection.test.jsx`).*

In external mode every Settings section except Config repo, Preferences and Upgrade is read-only, including Models (model-connection credentials), Executors (tokens) and Git (whose note points to Credentials). The corresponding routes are unguarded and still accept writes. Only the Legacy capabilities and Capability servers cards opt out with `allowInExternalMode`. Secrets entry is the one thing external mode is meant to leave in the UI.

### D-10: The UI ignores `directConfigWrites`

*Fixed. `RepoPage` 2026-10-31 (`docs/test-runs/2026-10-31-settings-git-repositories-verification.md`). `SettingsPage` 2026-11-01 (`docs/test-runs/2026-11-01-settings-deployment-work-verification.md`): `GET`/`PUT /api/settings` carry `configRepoOnly` for the row they answer with, and the page keys its lock on that (falling back to `GET /api/config`'s) rather than on `settingsLocked` (`TestSettingsSayWhenTheyAreConfigRepoOnly`).*

With `directConfigWrites: true` and `settings_locked: true`, `GET /api/config` reports `configRepoOnly: false`, and the server accepts every write. But `SettingsPage` prefers `settings.settingsLocked` once settings are loaded, and `RepoPage` uses `config.configRepoOnly || config.settingsLocked`, so both render read-only. The UI should key the lock on `config.configRepoOnly` alone.

### D-11: Config-managed notices link files that do not exist

*Fixed. Remainder 2026-11-01 (task 2281; found in `docs/test-runs/2026-11-01-settings-ownership-lock.md`): `ConfigManagedNotice` and `ReadOnlyGrants` drop paths that `GET /api/state-repo/files` says have not been exported (keeping at least the first path), and `TasksSection`'s `ReadOnlyGrants` omits its per-card `settings/capabilities.yml` link so Settings → Tasks links each file once (`ConfigRepoLink.test.jsx`, `config-repo-only-settings.spec.js`).*

*Fixed in `CONFIG_REPO_SECTION_PATHS` 2026-10-31: Git links `tables/grain_config.json` and `tables/repo_config.json`, Agents `tables/grain_config.json`. The `trustboundary.NeverAutoMergePaths` entries are left (harmless).*

`CONFIG_REPO_SECTION_PATHS` names `tables/git_credential.json`, `tables/repo_git_credential.json` (Git) and `tables/console_session.json` (Agents). Those tables do not exist: git connections live in `grain_config.git_connections` / `git_credential` and `repo_config.git_credential`, and console sessions in `grain_config.console_sessions`, all exported to `tables/grain_config.json` / `tables/repo_config.json`. The same names appear in `trustboundary.NeverAutoMergePaths` (harmless there), and `settings/new-task-form.yml` is listed there but is not written by `pkg/staterepo`.

### D-12: `settings_locked` refuses writes on a deployment with no config repository

*Fixed 2026-11-01 (task 2280; reproduced live on `grain demo` in `docs/test-runs/2026-11-01-settings-ownership-lock.md`): `configRepoFor`, `configRepoOnly` and `rowConfigRepoOnly` treat `Config.StateRepo == nil` (with no external remote) as unlocked, so `configRepoOnly` stays false on `GET`/`PUT /api/settings` and `GET /api/config` and non-bootstrap writes succeed directly (`TestSettingsLockedWithoutAStateRepositoryLeavesSettingsWritable`). In the UI, `GeneralSection` disables the Settings lock card when `GET /api/config` reports neither `configRepo` nor `externalConfigRepo`.*

`configRepoFor` refuses a non-bootstrap API write whenever `settings_locked` is on and `directConfigWrites` is off, without checking that `Config.StateRepo` exists. On a standalone deployment (§2.1) that flag leaves non-bootstrap settings writable by nobody but the unlock toggle. The 409 names `grain/config` and `POST /api/state-repo/pull-requests`, and neither exists there. The guard should treat "no state repository" as unlocked, or the UI should not offer the lock there.

### D-13: `.grain/settings.yml` composes; it does not override

*Design doc wording corrected 2026-10-31.*

`design/design/settings.design.md` §3 says repository declarations "override both the UI and `grain/config`". In the code (§2.8) the four declarable settings are stored in separate `declared_*` columns and **appended to / unioned with** the operator's value. The design doc's wording should be corrected; no code change is implied.
