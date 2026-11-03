# Test run — 2026-11-01 — Who owns a setting: `settings_locked`, the Settings/config-repo split, declared settings

Plan: `docs/test-plans/2026-11-01.md` §3 (task 2084; walked by task 2218).
Normative reference: `design/spec/settings/settings-mutability.spec.md` (§1.3, §2, §2.10, §3, §4 D-1..D-13).
Scope: PR #275 (`17fcb581`, schema 70), #234 (`3d26e211`), #319, #310, #237, #324, #230, #290.

**Result: PASS, with one UI bug fixed here and two discrepancies filed.**

| | |
| --- | --- |
| Matrix (6 rows × 5 modes, plus D-12) | Every row matches the spec's tables (§2.10, §3.9, §3.13, §3.16, §3.18) |
| Default-unlocked round trip | PASS: export commit, survives reconcile and restart |
| Lock toggle, person vs agent | PASS |
| Read-only rendering, "View in config repo" links | PASS, except the `settings/capabilities.yml` links (filed, below) |
| Navigation (`/settings`, `/admin`, old deep links) | PASS |
| Health → Settings card links | **FAIL → fixed**: a tall card scrolled its heading and the focused field off screen |
| Declared settings beside grain/config | PASS |
| D-12 | **Reproduces** (filed) |

## Environment

- Branch `grain/049f1e09/task-2218` from `main` at `41d9179f`. UI built with `npm ci && npm run build`, then `go build -o ~/bin/grain ./cmd/grain`, so every server embeds the real UI.
- Chromium from `/var/cache/playwright` (Playwright 1234 build), run headless at 1440×900 and 390×844, light and dark.
- Servers:
  - **Mode 1, no config repo:** `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:8701`.
  - **Modes 2, 3 and 3b, internal grain/config:** `grain daemon -data-dir /var/tmp/d2/data -ui-addr 127.0.0.1:8702 -sandbox-port-base=0 -disable-native-sandboxes -github-host 127.0.0.1:1 -github-insecure-http -poll-interval 5s`.
    - A fresh data dir creates `<data-dir>/state-repo` (internal grain/config) by itself.
    - `settings_locked` was toggled through `PUT /api/settings`.
    - Mode 3b adds `{"directConfigWrites": true}` in `<data-dir>/state-repo.json`, re-read without a restart.
  - **Mode 4, external:** the same daemon flags on `/var/tmp/d4/data`, with `state-repo.json` set to `{"remote":"file:///var/tmp/ext.git","branch":"main"}`.
    - `grain daemon` has no `-external-config-repo` flag. External mode is the `remote` in `state-repo.json`, or `grain state repo <url>`. The task text's flag name is out of date.
    - `ext.git` is a bare clone of the internal export.
- Test repo `acme/widgets`, added with `POST /api/repos`.
  - For §2.8, it is served over smart HTTP: a `git http-backend` WSGI wrapper on `127.0.0.1:8790`, with `githubHost` pointed at it.
  - Dumb HTTP is not enough: the declaration sync's shallow clone fails with `dumb http transport does not support shallow capabilities`.
- Auth: the deployment's access code, used as the bearer token (`<data-dir>/local-api-token`). For agent rows I used an external agent token: `POST /api/external-agents/tokens` with `scopes: ["settings:write","settings:read"]`, after a person enabled `externalAgentApi`.

## Per-mode API matrix

The rows are:

- Engine: `PUT /api/settings {"pollInterval":"Ns"}`
- Sandboxes: `{"sessionSandboxIdleTtl":"Nm"}`
- Bootstrap: `{"maxWorkers":N}`
- Repo setting: `PUT /api/repos/acme/widgets/prompt-extension`
- Capability default: `PUT /api/repos/acme/widgets/capabilities {"defaultCapabilities":[…]}`
- Memory: `PUT /api/memory {"text":…}`

Each request changes the value, so none is a no-op. "flags" is `configRepoOnly`/`settingsLocked`, read from `GET /api/config` and `GET /api/settings`; the two always agreed. `/api/config` omits both fields when false (`omitempty`).

| Row | 1. demo | 1 + `settings_locked` (D-12) | 2. internal, unlocked | 3. internal, locked | 3b. locked + `directConfigWrites` | 4. external |
| --- | --- | --- | --- | --- | --- | --- |
| flags | false / false | **true / true** | false / false | true / true | **false** / true | true / true (imported) |
| Engine `pollInterval` | 200 | 409 `config_managed_externally`, `internal`, `repo: grain/config`, `tables/grain_config.json`, patch | 200, synced | 409, `internal`, `grain/config`, `tables/grain_config.json`, patch | 200, synced | 409, `repo: file:///var/tmp/ext.git`, no `internal`, patch |
| Sandboxes `sessionSandboxIdleTtl` | 200 | 409 (as above) | 200, synced | 409 (as above) | 200, synced | 409 |
| Bootstrap `maxWorkers` | 200 | 200 | 200, synced | **200** | 200 | **409** (guard B) |
| Repo prompt extension | 200 | 409, `tables/repo_config.json` | 200, synced | 409, `tables/repo_config.json`, patch | 200, synced | 409 |
| Capability default (repo) | 200 | 409, `tables/repo_config.json` | 200, synced | 409, `tables/repo_config.json`, patch | 200, synced | 409, patch |
| Memory | 200 | 200 | 200, not synced at once (§2.2.3) | **200** (D-3) | 200 | **200** (D-3) |
| Person `settingsLocked:false` | — | 200 | — | 200 | 200 | **409** |
| Agent `settingsLocked:false` | — | — | — | **409**, `access: {canWrite:false, need:"tasks:write on grain/config…"}` | **200** (§2.5.3) | — |
| Agent `maxWorkers` (bootstrap) | — | — | — | 200 | — | — |

Notes on the matrix:

- "Synced" means each guarded write made one `Update grain state` commit in `<data-dir>/state-repo` at once (`SyncAfterWrite`, §2.2.1). After the first full pass that was 10 commits for 5 guarded writes × 2 passes. Memory added none until the background export.
- Agent `{"settingsLocked":false,"maxWorkers":8}` (mixed) was also refused 409 as a whole (§2.3.5). An agent's `pollInterval` got 409 with the same `access` block.
- Under the lock, `externalAgentApi` itself is non-bootstrap. A person must unlock to enable it, then re-lock. That is consistent with §2.4, but it is worth knowing when testing agent rows.
- In external mode the capability-default refusal sometimes carried no `change`. Each time, the value sent equalled the imported one, so the rehearsal had nothing to diff (§2.3.3, refused anyway). A value that differed got a patch.
- §3.13 lists `settings/capabilities.yml` for repo default capabilities. A deployment that hasn't adopted the capability catalogue has no such file, and the rehearsed patch rightly edits `tables/repo_config.json`'s `default_capabilities`. This is the spec's "(over …)" wording, not a bug.

Every cell matches the spec's §2.10.1 table and §3 rows.

## Default-unlocked write path (not-proved 2)

1. Mode 2: `PUT /api/settings {"pollInterval":"17s","sessionSandboxIdleTtl":"47m"}` returned 200.
2. `git -C <data-dir>/state-repo log -1` showed `2d4344a Update grain state` at 09:20:03. Its diff in `tables/grain_config.json` was `poll_interval_ms 11000 → 17000` and `session_sandbox_idle_ttl_ms 660000 → 2820000`.
3. After 20 s (four reconcile ticks at 5 s), `GET /api/settings` still read `17s` / `47m0s`.
4. I stopped and restarted the daemon on the same data dir: `17s` / `47m0s`. No config PR was needed. **PASS.**

## Lock toggle and agent gate (not-proved 3)

- **Person:** lock and unlock through `PUT /api/settings` and through the General → "Lock settings in UI (require config repo PRs)" switch both work (`config-repo-only-settings.spec.js` "toggling…" passes).
- **Agent:** the agent token cannot unlock (409), and in 3b it can (§2.5.3).
- **External mode:** a person's unlock is refused too (guard B), which is correct.
- **D-12:** on `grain demo` (no state repository) with `settings_locked: true`, a non-bootstrap write is refused with:
  - `repo: "grain/config"`, `url: "/repos/grain/config"`, `internal: true`, `branch: "main"`;
  - a message telling the caller to "Open a config pull request with it (POST /api/state-repo/pull-requests…)".

  The demo has no grain/config and no such route. Settings → General draws read-only, and the only way out is the unlock switch. **Reproduces.** Filed (see Bugs); no code change here.

## Settings in Chromium

Per section, I counted visible controls and enabled ones (outside a disabled fieldset), checked for the config-managed notice, and watched for horizontal overflow. There was no overflow in any mode at 1440 or 390 px, and no page errors.

| Section | 1. demo | 3. locked internal | 3b. + `directConfigWrites` | 4. external |
| --- | --- | --- | --- | --- |
| Models, Sandboxes, Executors | editable | editable (Sandboxes: one control disabled, `sessionSandboxIdleTtl`, D-1 fixed) | editable | read-only under the external banner; Executors' join tokens editable (D-9 fixed) |
| Legacy capabilities | editable | 22/27 enabled: deployment-wide fieldset disabled, with notice; secret entry editable (D-2 fixed) | editable | 22/27 (`allowInExternalMode`) |
| Engine / Caches | editable | read-only with notice except `maxWorkers` (D-1 fixed) | editable | read-only |
| General, Git, Repositories, Tasks, Agents, Capabilities, CI, Memory | editable | read-only with notice and "View in config repo" links; Settings lock stays enabled in General | editable, no notice (D-10 fixed) | read-only |
| Repo page prompt extension | enabled | disabled | enabled | disabled |

### "View in config repo" links

Each distinct link was followed from its section at 1440 px. Each opens `/settings/config-repo/<path>` with the file shown. The files linked are:

- `tables/grain_config.json`
- `tables/repo_config.json`
- `tables/custom_field.json`
- `tables/api_scope_set.json`
- `memory/deployment.md`

**Exception:** Tasks and Capabilities also link `settings/capabilities.yml`. This deployment's export (which has not adopted the capability catalogue) has no such file, so the browser shows `settings/capabilities.yml: file does not exist`. Tasks draws that link twice: four links for three paths.

The ordering is deliberate (`settingsSections.js` puts the always-present files first), but it is a remaining case of D-11's "notices link files that do not exist". Filed.

### Navigation

| Path | 1440 | 390 |
| --- | --- | --- |
| `/settings` | `/settings/models` | `/settings` (the phone section index, by design: `SettingsPage.jsx` `current = section \|\| (isPhone ? "" : DEFAULT)`) |
| `/admin` | `/settings/general` | `/settings/general` |
| `/admin/git` | `/settings/git` | `/settings/git` |
| `/admin/workflow` | `/settings/tasks` | `/settings/tasks` |
| `/admin/state` | `/settings/config-repo` | `/settings/config-repo` |
| `/admin/integrations` | `/settings/credentials` | `/settings/credentials` |
| `/admin/caches` | `/system/caches` | `/system/caches` |
| `/settings/secrets` and `/settings/legacy-capabilities` | `/settings/credentials` | `/settings/credentials` |
| `/settings/capability-servers` | `/settings/capabilities` | `/settings/capabilities` |

### Health → Settings links (PR #290)

Under the lock, `k8sNamespace` is a bootstrap field and still writable. Changing it left `pendingRestart: ["k8sNamespace"]`. System → Health's pending-restart notice links "Kubernetes namespace" to `/settings/sandboxes#kubernetes`.

The link opened the right section and focused the "Kubernetes namespace" input. **But the card was scrolled past its own heading:**

| Viewport | Card height | Card top | Focused input top |
| --- | --- | --- | --- |
| 1440×900 | ~1950 px | −477 | **−416** |
| 390×844 | ~2450 px | −766 | **−709** |

Both light and dark were the same. The field the person came for was off screen.

Cause: `SettingsPage.jsx`'s reveal calls `scrollIntoView({block:"center"})` on the card and then focuses its first field with `preventScroll`. **Fixed here** (below). After the fix:

| Viewport | Card top | Input top |
| --- | --- | --- |
| 1440×900 | 93 | 154 |
| 390×844 | 69 | 126 |

## Declared settings beside grain/config (not-proved 5)

1. On the internal daemon (mode 2), `acme/widgets`'s default branch got `.grain/settings.yml` with `prompt-extension:` and `setup-command: echo declared-setup`. I sent `PUT /api/repos/acme/widgets/declarations {"declarations":true}`.
2. The sync at `352e507` logged `.grain/settings.yml changes 2 settings field(s)…`. `GET …/prompt-extension` answers `declaredPromptExtension` / `declaredSetupCommand` and `effectivePromptExtension = operator text + declared text` (composed, D-13).
3. The next export (`6b02948`) has no `declared_*` key in `tables/repo_config.json`. Neither declared value appears anywhere in grain/config. **PASS.**
4. The repo page (`/repos/acme/widgets/settings/agents`) shows the declared text read-only: "Declared by acme/widgets itself in .grain/settings.yml, added after your text rather than replacing it, and not editable here…". **PASS.**
5. Next I committed straight to grain/config's `tables/repo_config.json`, setting `declared_prompt_extension: "INJECTED via grain/config"` together with an operator `prompt_extension` edit.
   - The next import applied the operator field (`operator edit via grain/config`) and kept the database's declared value.
   - The re-export (`fe51ba8`) dropped the injected key.
   - `grain state check` on that tree warns: `row 0 carries "declared_prompt_extension", which the repository declares in its own .grain/settings.yml and which is never imported…`.
   - **PASS** (§2.8.2).

## Discrepancies D-1..D-13

| | Status on this run |
| --- | --- |
| D-1 | Fixed, confirmed: Engine/Caches/`sessionSandboxIdleTtl` are read-only in the UI under the lock, and the server refuses them. |
| D-2 | Fixed, confirmed: the Legacy capabilities deployment-wide fieldset is disabled under the lock. |
| D-3 | **Reproduces**: memory writes 200 in locked internal and external mode, while the UI draws Memory read-only. |
| D-4, D-5, D-6, D-7, D-8 | Not exercised (outside this section's six-row matrix). |
| D-9 | Fixed, confirmed: in external mode Executors' join tokens and Legacy capabilities' secret entry stay editable. |
| D-10 | Fixed, confirmed: with `directConfigWrites` the Settings and repo pages draw no lock. |
| D-11 | Fixed for Git/Agents. **Remainder:** Tasks/Capabilities link `settings/capabilities.yml`, which does not exist until the catalogue is adopted (filed). |
| D-12 | **Reproduces** (filed). |
| D-13 | Consistent: the declared prompt extension is appended after the operator's. |

No undocumented server/spec mismatch was found. The one new bug (the anchor scroll) is UI-only and not a mutability rule.

## Bugs fixed here

**Settings anchor lands past a tall card's heading and focused field.** `ui/src/components/SettingsPage.jsx`: a target taller than the viewport is now aligned to its top; fields and short cards are still centred.

Regression test: `ui/e2e/config-repo-only-settings.spec.js`, "a Health link to a card taller than the screen lands on its heading and named field", at desktop and phone. It runs under the config-repo-only intercept with `pendingRestart: ["k8sNamespace"]`, follows the Health link, and asserts with bounding boxes that:

- the card is taller than the viewport;
- its top edge is at y ≥ 0 and is not covered (`elementFromPoint`);
- the "Kubernetes namespace" field is focused, enabled and in the viewport.

Without the fix both cases fail (`toBeInViewport`: viewport ratio 0); with it, the whole spec passes (8/8). `SettingsPage.test.jsx` (34), `deployment-operations-alert-power-flush-names.spec.js`, `settings-panel-walkthrough.spec.js` and `cli-run-locations.spec.js` also pass.

Screenshots (`ui/e2e/screenshot.mjs` against the locked internal daemon, 1440 and 390 px, light and dark) cover `/settings/sandboxes#kubernetes`, `/system/health` and `/settings/general`.

## Bugs filed

- **D-12:** `settings_locked` on a deployment with no state repository refuses non-bootstrap writes and points at a grain/config that does not exist. Not fixed here:
  - §2.1's normative text describes the current behaviour.
  - The fix is a choice between treating "no state repository" as unlocked (server) and not offering the lock there (UI).
  - `config-repo-only-settings.spec.js`'s toggle test currently depends on locking the demo.
- **D-11 remainder:** the Tasks and Capabilities notices link `settings/capabilities.yml` on a deployment whose export has no such file, and Tasks draws it twice.

## Not covered

- D-4..D-8 surfaces (boards, processes, console sessions, release secrets, capability-server secret binding, release notes).
- §3's validation items (PR #237 Kubernetes values).
- The refusal follow-through flows of #319/#310/#237 beyond what `config-refusal-pull-request.spec.js` already proves.
- PR #230 (a config PR's task branch). Opening a config PR needs a GitHub-backed grain/config, which this sandbox does not have.
