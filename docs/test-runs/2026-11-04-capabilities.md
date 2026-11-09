# Capabilities: retirements, gcp sets over existing accounts, server-drop reporting, the custom connection removal

- **Plan:** `docs/test-plans/2026-11-04.md` §8 (PRs #493, #613, #625, #614, #471, #631, #647, #594, #645, #643). Starts from [`2026-11-03-capabilities-ci-repo.md`](2026-11-03-capabilities-ci-repo.md).
- **Date:** 2026-11-07, from 03:10 UTC.
- **Task:** 3074, branch `grain/049f1e09/task-3074-walkthrough-capability` on `ai-on-gke/grain`, base `main` at `c6004bc9` (schema 119 when the walk started; 120 with the fix below). **Renumbered after the walk:** `main` then took schema 120 for the `github-credential:<name>` rewrite, and migrations 121-124 for later retirements, so F1's migration is now **125** (`TestMigration125RewritesGrainCIInThePolicy`). Where this record says "migration 120" or "schema 120" for F1 below, read 125. The dumps were taken at the time and keep their file names.
- **Environment:**
  - Debian 12 sandbox, `linux/amd64`, 7 vCPU / 30 GiB, Docker on the host. Go `go1.26.10`, Node 20.19.
  - `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` (rebuilt after the fix).
  - `grain93`: `go build ./cmd/grain` in a worktree at `0e33f85f` (`SchemaVersion = 93`).
  - `sqlite3` and `postgresql-client` from apt. PostgreSQL 16 from `docker run postgres:16` on `127.0.0.1:55432`.
  - Item 1's daemons ran with `-disable-native-sandboxes -sandbox-port-base=0` and started no runs.
  - **Items 2 and 4 used a real `grain daemon`** (`127.0.0.1:7830`) with `-container-sandboxes -container-sandbox-image grain-sbx:walk -container-git-proxy-host 10.240.0.1 -github-host 127.0.0.1:4250 -github-insecure-http`, configured by [`setup-daemon.sh`](2026-11-04-capabilities/setup-daemon.sh). With `-host-sandboxes` the daemon refuses every capability grant (`this sandbox backend (*orchestrator.hostSandbox) cannot run commands in its sandbox, so it cannot materialize capability grants`), so its runs used Docker. Every run and CI job ran in its own container.
    - **The stub framework.** `grain daemon` has no stub agent and no `-fake-ci`. Instead, two antigravity connections (`ag-a`, `ag-b`, each with its own fake Gemini key) run [`fake-agy.sh`](2026-11-04-capabilities/fake-agy.sh). That is `tests/e2e/fake_agy_test.go`'s `writeFakeAgy` written as a script: the agent CLI runs on grain's host, and its probes look into the run's container with `docker exec` or call the run's own grain MCP server ([`mcp-call.py`](2026-11-04-capabilities/mcp-call.py), the server command from the run's `mcp_config.json` over stdio).
    - **Grain CI** ran in place of fake CI, on the daemon's own CI with Docker jobs ([`ci-run.sh`](2026-11-04-capabilities/ci-run.sh), [`acme-widgets-ci.yml`](2026-11-04-capabilities/acme-widgets-ci.yml)).
    - **Stubbed GitHub:** [`simhost.go`](2026-11-04-capabilities/simhost.go). Git smart-HTTP goes to `git http-backend`, as 2026-11-03's `githost` did, and the REST API goes to `pkg/github/githubsim` over the same bare `acme/widgets`. A task run needs the REST API, which `githost` did not serve.
    - **Stubbed sandbox image:** [`Dockerfile.walk-sandbox`](2026-11-04-capabilities/Dockerfile.walk-sandbox) (`debian:bookworm-slim`, the contract-2 packages, user `debian`, `cmd/grain-sandbox` as `/usr/local/bin/grain`).
  - `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:7900` (shared step 2) for its gcp `org-deploy` set.
  - **No GCP.** GCP IAM/STS is not reachable from the sandbox, and nothing below called Google Cloud. Item 3 used the demo's `gcpfake`-backed gcp server and the CI fakes (see item 3). Google's Generative Language API *was* reachable, and item 2 used it to see which key the model proxy forwarded with.

## Outcome

| §8 "not proved" item | Result |
|---|---|
| 1. Upgrade from a schema-93 store holding every retired grant shape | **Pass after one fix (F1).** Every grant on tasks, templates, schedules, deployment and repository defaults, repository CI capabilities and console sessions is rewritten to its server grant. The gcp sets are seeded from the pool and the `gcp_key` row, and the custom connection is disabled and kept. SQLite and PostgreSQL give identical results, and so do the in-place migration and the state-repo dump route. One surface was missed: a bare `grain-ci` in the capability policy's catalogue defaults stayed bare (migration 96 never touched the policy). Fixed with migration **120** and regression tests. F2: a schema-93 deployment can save a catalogue that its own export then refuses. That refusal survives the upgrade (filed). |
| 2. Mints in a real sandbox | **Pass after three fixes.** **model-proxy:** a `model-proxy {vendor: gemini}` grant on a run of connection `ag-b` placed `~/.gemini-proxy-{url,token}` (0600) in the sandbox. A request through them reached Google with `ag-b`'s own key: Google's `DebugInfo` echoes the invalid key back, `walk-key-B-not-real`, not `ag-a`'s. A wrong token gets the proxy's 401. **release#package-publish:** on a real daemon, every CI job holding it was refused, first with `capability https://grain.dev/capabilities/release is not configured in mcpServers` (F4), then with `mode "00755" is not four octal digits` (F5). After both fixes the `workflow_dispatch` job got the same seven files as 93's `package-publish`. The `push` job was withheld with no note at all (F6). After that fix it carries `grain ci: capability …release#package-publish={} is withheld on push runs (released only to release, workflow_dispatch, and grain's scheduled runs)` in its log and in `JobRun.Warning`. |
| 3. gcp token mode | **Not proved live: no GCP, and the real daemon has no gcp sets over existing accounts.** The demo's `org-deploy` set is shown with `credential token`, `x-grain-person-granted: [org-deploy]` and "grain creates, binds and deletes nothing" against its `gcpfake` cloud. Exchange, refresh, revoke-at-release and the person-granted propose hold were re-run as unit tests on fakes (`TestGCPTokenModeGrantPlacesAnExternalAccountAndNoKey`, `TestGCPExistingAccountSetsArePersonGranted`, `TestPersonGrantedIsNeverCovered`, `TestRelayedProposalHoldsARequiresPersonGrant`, all pass). The live sandbox walk is filed as a follow-up. |
| 4. Server drop on a live run | **Pass after one fix (F7), with two findings filed (F8, O3).** A task's run held a grant from `https://walk.example/caps/cm` (grain's config-map server under its own id, [`capsrv.sh`](2026-11-04-capabilities/capsrv.sh)), which was killed and held down mid-run. Within a second the daemon raised `capability.server-down:https://walk.example/caps/cm`. The run's next `run_command` result carried the notice, and the result after it did not. Once the server could start again it re-attached by itself in under 30 s, and the alert was cleared by `automation:capability-servers`. `grain health` printed "no counters: nothing on this daemon registers any" with the server down (F7, fixed). It now leads with the server. A config the server rejects is refused with its reason, on the set's field. `PUT /api/capability-servers` with two gcp sets both named `org` gets 400 `capability server https://grain.dev/capabilities/gcp refused this configuration, so it was not saved: configure: server reported an error: permission set "org" is configured twice`, with the same reason under `fields.permission_sets[0]` ([`refused-config.txt`](2026-11-04-capabilities/refused-config.txt)). This was checked through the API, not through the form. |
| 5. Settings → Capabilities and Settings → Models at 1440/390 | **Pass after one fix (F3).** Settings → Models shows the custom connection as removed, with its reason. Settings → Capabilities had notices for gcp-key, model-proxy, gemini-key and bootstrap-playbooks, but none for the release server or `grain#ci` (F3, fixed). The [contact sheet](2026-11-04-capabilities/screenshots/contact-sheet.png) is from the upgraded schema-120 store, light and dark at 1440 and 390 px. [`page-text.txt`](2026-11-04-capabilities/page-text.txt) is the rendered text of both pages ([`page-text.mjs`](2026-11-04-capabilities/page-text.mjs)). |

## Findings

| # | Finding | Status |
|---|---|---|
| F1 | **Migration 96 left `grain-ci` bare in `grain_config.capability_policy`.** 96 rewrote `grain-ci` in the grant tables, default lists, console sessions and `repo_config.ci_capabilities`, but not in the capability policy. Every other retirement in the range rewrites the policy's rules and catalogue defaults too: 97 (`rewriteCapabilityPolicyGCPKey`), 100, 101 and 103 (`rewritePolicyRenames`), and later 115 and 118. So a catalogue whose `defaults.ci` (or task, or per-repository) list named `grain-ci` kept the bare alias through every upgrade. That happened in place on SQLite and on PostgreSQL alike, and on the dump route (`staterepo.ImportMigrated` replays the same `Up`s through `model.ApplyDataMigrations`). The alias still resolves to `grain#ci` today. It is only "accepted for one release", though, and Settings → Capabilities keeps flagging it as deprecated. Repro: [`seed93.sh`](2026-11-04-capabilities/seed93.sh) on a `grain93` daemon, then start the current binary on that data dir. [`grants-119-inplace.txt`](2026-11-04-capabilities/grants-119-inplace.txt) line 70 shows `"grain-ci"` under `catalogue.defaults.ci`. | **Fixed.** Migration **120** (`pkg/model/grain_ci_policy_migration.go`) runs `rewritePolicyRenames({grain-ci: grain#ci}, grain#ci)`, the same helper migration 100 uses. It changes no table. Regressions: `TestMigration120RewritesGrainCIInThePolicy` (SQLite and PostgreSQL) and `TestStateRepoImportRewritesGrainCIInThePolicy` (SQLite, dump route). Both SQLite tests fail without the migration (`defaults.ci = [… grain-ci]`). The PostgreSQL test was run against the Docker PostgreSQL (`GRAIN_POSTGRES_TEST_URL`). After the fix, [`grants-120-inplace.txt`](2026-11-04-capabilities/grants-120-inplace.txt) and [`grants-120-dump-import.txt`](2026-11-04-capabilities/grants-120-dump-import.txt) both carry `grain#ci={}`. |
| F2 | **The API saves a catalogue that its own export refuses.** On 93, `PUT /api/capability-policy` accepted `inProcess: {image-signing: {enabled: false}}`. `PUT /api/repos/acme/widgets/ci-capabilities` had already stored `image-signing`, and neither request checks the other. The export writes both into `settings/capabilities.yml`, and `grain93 state check` on that export fails: `defaults.ci: capability image-signing is disabled in settings/capabilities.yml`. After the upgrade (either binary), the in-place store's own schema-119/120 dump fails `grain state check` the same way (`… release#image-signing={} is disabled … (in_process.image-signing, the capability it replaced)`). `grain state prepare-upgrade` refuses to arm (`this build could not take the rows exported from the store at schema 93`). A deployment in this state cannot change `settings/capabilities.yml` in a config-repo PR until somebody fixes the contradiction by hand. | **Filed** (see "Follow-ups"). The fix belongs in `Client.SetCapabilityPolicy` and the repo-capability writes (validate the merged catalogue the export will write), not in a migration. |
| F3 | **Settings → Capabilities had no deprecation notice for the release server or `grain#ci`.** Both retirements keep their bare names as one-release aliases, like the four that have a notice. | **Fixed** (`ui/src/components/settings/CapabilitiesSection.jsx`). Two notices were added (`release-deprecation-notice`, `grain-ci-deprecation-notice`). `CapabilitiesSection.test.jsx` checks all six and fails on the two without the fix. |
| F4 | **On a real daemon, CI grant admission could not mint any in-process capability server's grant.** `mintCIPass` resolved a job's references against the attached registry (`capabilityServerLookup`), which tracks `mcp_servers` only. The release server is in-process (`capability.Register`), so every job holding `release#image-signing`, `#registry-push` or `#package-publish` was refused after three tries. Migration 101 rewrote every repository's `ci_capabilities` to those references, so every publish job failed: `capability https://grain.dev/capabilities/release is not configured in mcpServers` ([`ci-workflow_dispatch-before-fix.txt`](2026-11-04-capabilities/ci-workflow_dispatch-before-fix.txt)). The release server's own tests stood `grainserver.Lookup(nil)` in for the registry, so none of them could see it. The agent task's half (`orchestrator.mintCapabilityPass`) already wraps its registry in `grainserver.Lookup`. | **Fixed** (`pkg/daemon/ci_capability_pass.go`). `mintCIPass` now wraps the same way, and the no-servers refusal is kept for external references. `TestCIJobMintsAnInProcessReleaseGrantWithOnlyTheAttachedRegistry` hands admission a real, empty `servers.Registry` and fails without the fix. |
| F5 | **`release#package-publish` mint responses failed protocol validation.** The release server (and the in-process model-proxy) put a `"0"` in front of each retired provider's placement mode, which assumes three digits. package-publish's docker credential helper is placed `"0755"`, so it went out as `"00755"` and the whole mint was refused: `placement /home/debian/.grain/bin/docker-credential-grain: mode "00755" is not four octal digits` ([`ci-workflow_dispatch-before-mode-fix.txt`](2026-11-04-capabilities/ci-workflow_dispatch-before-mode-fix.txt)). This hits only a daemon that configures the refresh route (every real one), so the fidelity test, which configures none, never placed the helper. | **Fixed.** `model.Placement.ProtocolMode` parses the mode and formats it as four octal digits, and the release server and model-proxy use it. `TestPlacementProtocolMode`, plus the F4 test with the refresh route configured, which fails on `00755` without the fix. |
| F6 | **A job declaring an event-gated release reference got no `grain ci:` note and no `JobRun.Warning`.** `ciWithheldCapabilityNotes` skipped every capability reference. #594's notes still fired for `repo#write` and the retired providers, but not for the release methods that replaced the providers. The push job ran with no files and no word why ([`ci-push-before-fix.txt`](2026-11-04-capabilities/ci-push-before-fix.txt)). | **Fixed** (`pkg/daemon/ci.go`). A reference with a `model.CIGrantRule` is noted when the rule withholds it, in the providers' words. `TestEventGatedReleaseReferencesAreNotedWhenWithheld` fails without the fix. Live: [`ci-push.txt`](2026-11-04-capabilities/ci-push.txt). |
| F7 | **`grain health` never listed capability servers.** #647 put `capabilityServers` (failed servers and refused configs) on the health response, but `pkg/cli/health.go`'s printer never read it. With a server down and its alert raised, the command said "no counters: nothing on this daemon registers any". | **Fixed.** The printer leads with a `FAILING: capability servers out of use` section. `TestGrainHealthListsCapabilityServersOutOfUse` fails without the fix. Live output: [`drop-walk.txt`](2026-11-04-capabilities/drop-walk.txt). |
| F8 | **The drop notice reaches the run as a person's words, and its advice is for a config change, not a drop.** `noteCapabilityServerDropped` posts as `automation:capability-change`. `ui.Client.MessagesSince` relays that as author `grain`, but `pkg/mcp/addenda.go` frames every addendum as `[grain] A person added this to the task's conversation … The quoted text is a person's own words, not grain's:` and then quotes `[grain]: grain: capability server …`. The body says `is now failed since.` and then that the grant "works as it did then, but a change made to that configuration since … has not reached it". For a server that is down, nothing it minted can be refreshed or released, which is the opposite advice. | **Filed** (see "Follow-ups"). The fix spans `pkg/mcp`'s notice wording (an addendum needs to know its author) and the orchestrator's text for a drop versus a refused config. |
| O3 | A stdio (`command:`) server that dies is restarted at once (`its process ended, attaching it again`) and raises nothing. That is right for a crash that recovers. Grants it minted in the dead process are not re-minted, though, and the run is not told. The walk had to hold the server down ([`capsrv.sh`](2026-11-04-capabilities/capsrv.sh)) to see a drop. `kill` (SIGTERM) did not stop `grain capability config-map`. Only SIGKILL did. | Noted. |
| O4 | `GET /api/capability-servers` on the demo returns the demo gcp server's `Authorization: Bearer <64 hex>` header in plain text. A real deployment would hold it as `$secret:`. | Noted (demo only). |
| O5 | The model proxy relays upstream error bodies verbatim. For an *invalid* Gemini key, Google's `DebugInfo` contains the key, so a sandbox can read the connection's key if it is wrong. A valid key is never echoed. | Noted. |
| O6 | Settings → Capabilities' in-process list shows `image-signing — disabled (in_process.image-signing)` without the `retired … → move to servers` hint the gcp-key, claude-proxy and gemini-key rows get, and lists no `grain-ci` row. | Noted. |
| O1 | A data dir holding `schema-upgrade/` but no store (deleted by hand) starts a fresh, empty store. The dump is not imported ("the store is already at schema 120 and … schema-upgrade is still here"), and the empty database is then exported over the state repository. The prior dump is the preceding commit, so nothing is lost for good. It is not a supported flow. | Noted. |
| O2 | The 93 export writes `settings/capabilities.yml`'s `defaults` from `grain_config.default_capabilities` and `repo_config`, not from the policy's own catalogue defaults. So the catalogue defaults a dump-route import arrives with are those lists, not what `capability_policy.catalogue.defaults` held (the only difference in [`grants-120-inplace-vs-dump-import.diff`](2026-11-04-capabilities/grants-120-inplace-vs-dump-import.diff) besides the task rows, which a dump never carries). That is 93's own export, unchanged by this range. | Noted. |

## Item 1: upgrade from schema 93

### Building the store

[`seed93.sh`](2026-11-04-capabilities/seed93.sh) fills a fresh `grain93 daemon` through its own API wherever the schema-93 API accepts the grant, and with sqlite3 (or psql) where it refuses:

| Surface | Retired shapes | How |
|---|---|---|
| `gcp_key` + pool | `deploy` set (`walk-deploy`, 1 account, 2 h); pool `grain-sa@walk-proj` ×2 | `POST /api/gcp-keys`, `PUT /api/config` |
| task 2 | `gcp-key:deploy`, `gcp-key`, `claude-/openai-/gemini-proxy`, `gemini-key` (`label`); `grain-ci` (`default`, from CI mode `grain`) | `POST /api/tasks` |
| task 2 | `image-signing`, `registry-push`, `package-publish` | sqlite3. 93 refuses CI-only grants on a task |
| `template-1`, `sched-1` | the task's six, plus `grain-ci` and the three release names | API, then sqlite3. 93 refuses `grain-ci` by hand (`grain-ci cannot be attached by hand`) |
| `repo_config` acme/widgets | default `gcp-key:deploy, claude-proxy, gemini-key`; CI `gcp-key, claude-proxy, image-signing, registry-push, package-publish, gemini-key` | `PUT …/capabilities`, `…/ci-capabilities`. 93 refuses `gcp-key:deploy` for CI |
| deployment defaults | `gcp-key, openai-proxy` | `PUT /api/config` |
| console session `retired-session` (task 3) | the task's six, plus `grain-ci`, `image-signing` (sqlite3) | `POST /api/console-sessions` |
| capability policy | catalogue defaults (task/CI/per-repo) naming all five families; `inProcess` entries for `gcp-key`, `claude-proxy`, `image-signing` (disabled), `gemini-key`, `grain-ci` | `PUT /api/capability-policy`. Rules take capability references only, so 93 refuses a rule on a bare name |
| custom connection | `{"id":"my-custom","framework":"custom","command":"/usr/local/bin/my-agent --prompt-file {prompt}"}` beside a `claude` one | `PUT /api/config` |

[`dump-grants.sh`](2026-11-04-capabilities/dump-grants.sh) writes one line per grant from either store kind, and redacts secrets. Before: [`grants-93.txt`](2026-11-04-capabilities/grants-93.txt). The PostgreSQL store seeded by the same script, [`grants-93-postgres.txt`](2026-11-04-capabilities/grants-93-postgres.txt), is identical apart from the session task.

### Upgrading

| Route | Command | Result |
|---|---|---|
| In place, SQLite | `grain daemon -data-dir <copy>` | `migrated the store in place from schema 93 to 119` (120 after the fix). [`grants-93-vs-119-inplace.diff`](2026-11-04-capabilities/grants-93-vs-119-inplace.diff) before the fix, [`grants-93-vs-120-inplace.diff`](2026-11-04-capabilities/grants-93-vs-120-inplace.diff) after |
| In place, PostgreSQL | `grain daemon -postgres-url …` on the 93 database | Same log line. [`grants-119-postgres.txt`](2026-11-04-capabilities/grants-119-postgres.txt) is identical to the SQLite result apart from the session task's row order |
| `grain state prepare-upgrade` | after `grain93 state sync` | Refused on the seeded store (F2). With the contradiction removed (`image-signing` enabled), it rehearsed and armed (`114 rows across 23 tables migrate from schema 93 into 119`), and the start that followed matched the in-place result line for line |
| Dump route | [`import-dump.go`](2026-11-04-capabilities/import-dump.go): `staterepo.MigrateDump` + `staterepo.ImportMigrated` into a fresh store, then one daemon start | [`grants-120-dump-import.txt`](2026-11-04-capabilities/grants-120-dump-import.txt). Templates, schedules, defaults, CI capabilities, sessions, sets, `mcp_servers` and the custom connection match in place. Task rows are not in a dump. The catalogue defaults differ (O2) |
| Fresh 120 daemon over the 93 state repo | `state-repo/` copied into an empty data dir | Refused by design: `repository is at schema 93, this build knows 120 … run the build that wrote it` |

What the diff shows, grant by grant (each holds on every surface listed above):

| Schema 93 | Schema 120 |
|---|---|
| `gcp-key` | `gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}` |
| `gcp-key:deploy` | `gcp#gcp-key={"permission_set":"deploy","primary":true}`. The seeded `deploy` set has `key_path: /home/debian/.gcp-service-account-deploy.json`, so both primaries coexist. Only the default path activates gcloud, as on 93 |
| `claude-proxy` / `openai-proxy` / `gemini-proxy` | `model-proxy#model-proxy={"vendor":"claude"\|"openai"\|"gemini"}` |
| `image-signing` / `registry-push` / `package-publish` | `release#image-signing={}` / `#registry-push={}` / `#package-publish={}` |
| `gemini-key` | `gcp#gemini-key={}`, with `gemini_key: {admin: minter, project: walk-proj}` seeded on the gcp entry |
| `grain-ci` | `grain#ci={}`, and in the policy's catalogue only after migration 120 (F1) |
| gcp entry | `permission_sets` `sandbox-agent` (`grain-sa-0/1@walk-proj`, 24 h, admin `minter`) and `deploy` (2 h, admin `minter-deploy`), `adopt_legacy: true`, `env` naming `$secret:gcp-key-minter[-deploy]`, plus the deprecation `notice`. The attach is then refused, because neither secret exists (`capability server references secrets that are missing or empty: gcp-key-minter, gcp-key-minter-deploy`), exactly as the 93 `gcp-key` would have failed to mint |
| `model-proxy` external entry in `mcp_servers` | removed (in-process now) |
| `my-custom` connection | kept, with `"disabled": true` and `"disabledReason": "custom command connections were removed in v3.2.0; replace it with an Antigravity, Claude or Codex connection"` |
| `inProcess.image-signing: {enabled: false}` | kept. `judgeRetiredInProcess` binds `release#image-signing` to it (`… disabled … (in_process.image-signing, the capability it replaced)`), so the operator's switch still holds |

The session's task (3) is re-synced from the session on the first start. It picks up `grain#ci` and `release#image-signing` from the two names sqlite3 added to the session, plus `api:ci-read` (default). `release#image-signing` is CI-only, and minting refuses it for a task. It is only there because the seed wrote what 93's API refuses.

## Item 2: mints in a real sandbox

### model-proxy {vendor}

1. `setup-daemon.sh`. Task 4: `agentBackendId: ag-b`, grant `model-proxy#model-proxy={"vendor":"gemini"}`, probe [`step-model-proxy.sh`](2026-11-04-capabilities/step-model-proxy.sh).
2. The run's container `grain-sandbox-4-1` ([`run-item2-model-proxy.log`](2026-11-04-capabilities/run-item2-model-proxy.log); tokens cut):

   ```
   -rw------- 1 debian debian 82 /home/debian/.gemini-proxy-token
   -rw------- 1 debian debian 24 /home/debian/.gemini-proxy-url      -> http://10.240.0.1:33441
   GET <url>/v1beta/models (placed token): HTTP 400 "API key not valid" … "detail": "Invalid API key: walk-key-B-not-real"
   GET <url>/v1beta/models (wrong token):  HTTP 401
   ```

   The proxy forwarded with the run's own connection's key (`ag-b`), not `ag-a`'s, which is listed first. The attempt's capability audit is empty: the model-proxy method declares no `Audit`, so no records are expected.

### release#package-publish on grain CI

`acme/widgets` has CI mode `grain` and `ci_capabilities: [release#package-publish={}]`. Its job `publish` declares the same grant and lists what it was given.

| Run | Before | After |
|---|---|---|
| `workflow_dispatch` | errored: `…release is not configured in mcpServers` (F4), then `mode "00755" is not four octal digits` (F5) | success. `~/.package-publish-{token,username,registry,refresh-url,refresh-token}` (0600), `~/.docker/config.json` (0600), `~/.grain/bin/docker-credential-grain` (0755): the 93 provider's files ([`ci-workflow_dispatch.txt`](2026-11-04-capabilities/ci-workflow_dispatch.txt)) |
| `push` | success, no files, no warning (F6) | success, no files. First log line `grain ci: capability https://grain.dev/capabilities/release#package-publish={} is withheld on push runs (released only to release, workflow_dispatch, and grain's scheduled runs)`, and the same text in the job's `warning` ([`ci-push.txt`](2026-11-04-capabilities/ci-push.txt)) |

## Item 3: gcp token mode

- The demo's gcp server, `GET /api/capability-servers` on `127.0.0.1:7900`, reports: `set org-deploy: existing service account deployer@demo-project.iam.gserviceaccount.com (credential token; grain creates, binds and deletes nothing); admin default can impersonate it; short-lived tokens, no key cap`. Its `permission_set` argument carries `x-grain-person-granted: ["org-deploy"]`.
- The walk daemon has no gcp sets: its gcp builtin needs a real admin credential to configure one over an existing account. Pointing the daemon at the demo's server would test the demo's wiring, not a deployment's. So the exchange at the forwarded listener inside a real container, refresh to the deadline, and revoke at release were **not walked live**. CI's fakes were re-run instead (`pkg/capability/intree`, `pkg/capability/protocol`, `pkg/orchestrator`, all `ok`; names in the Outcome table). A follow-up is filed to walk it on a daemon with a `gcpfake` connector. **Walked 2026-11-08** in [`2026-11-08-gcp-token-mode.md`](2026-11-08-gcp-token-mode.md): all four parts pass after three fixes (a stdio server's stderr discarded, the person-granted proposal note, and default-blind grant coverage).

## Item 4: server drop on a live run

[`drop-walk.sh`](2026-11-04-capabilities/drop-walk.sh), output in [`drop-walk.txt`](2026-11-04-capabilities/drop-walk.txt):

```
task 7 filed and approved at 04:20:00
run holding the grant at 04:20:09            (~/.config-map/bootstrap/*.md placed in the container)
server pid 93484 killed and held down at 04:20:09
== alert
capability.server-down:https://walk.example/caps/cm | Capability server https://walk.example/caps/cm is failed | … initialize: mcp: capability server closed its output: EOF
== grain health                               (after F7)
FAILING: capability servers out of use -- no grant is minted from one until it attaches again:
  https://walk.example/caps/cm (failed): capability server https://walk.example/caps/cm: initialize: mcp: capability server closed its output: EOF
== the run's tool calls
run_command 'echo first call after the drop' -> … [grain] A person added this to the task's conversation … > [grain]: grain: capability server https://walk.example/caps/cm, which minted this run's grant https://walk.example/caps/cm#config-map, is now failed since. …
run_command 'echo second call'              -> plain result, no notice
```

Re-attach: with the hold removed, the registry re-attached the server at 04:19:03 on the earlier probe, and `notification.cleared_by` is `automation:capability-servers`. A run is limited to 1m40s of silence (the connection's model retry cutoff), so the whole drop has to fit inside one fake turn. The first attempt, which waited by hand, was cut off before its tool call.

## Item 5: screenshots

`node e2e/screenshot.mjs --pages /settings/capabilities,/settings/models --url http://127.0.0.1:7812` against the in-place-upgraded schema-120 store (its custom connection disabled by migration 104), light and dark at 1440 and 390 px: [`screenshots/`](2026-11-04-capabilities/screenshots/), [contact sheet](2026-11-04-capabilities/screenshots/contact-sheet.png). The sandbox cannot show a PNG to the agent, so what the pages say was checked from their rendered text ([`page-text.txt`](2026-11-04-capabilities/page-text.txt)): six deprecation notices after F3, the in-process rows (O6), and on Settings → Models `custom command` / `removed` / `custom command connections were removed in v3.2.0; replace it with an Antigravity, Claude or Codex connection`.

## Tests run locally

`go test ./pkg/model/... ./pkg/staterepo/...` (PostgreSQL migration tests also against the Docker PostgreSQL with `GRAIN_POSTGRES_TEST_URL`), `go test ./pkg/daemon` (full package), `go test ./pkg/cli` with `GRAIN_UPDATE_CLI_SPEC=1 … -run CLISpec` (no spec change), `go test ./pkg/capability/...`, and `npx vitest run src/components/settings/CapabilitiesSection.test.jsx`. This run has no access to the repository's GitHub checks: `pull_request_status` says no repository is configured for it, and `grain ci` needs `grain#ci`, which this task does not hold. `wait_for_checks` likewise reports no CI for this run, so the pull request (#818) had no check verdict visible from inside the run. Before the final push, the branch (level with `main` at `c6004bc9`) was re-checked with `go build ./...`, `go vet` on the changed packages, `go test ./tests/hygiene ./pkg/model/... ./pkg/cli ./pkg/capability/... ./pkg/staterepo/...`, the four new `pkg/daemon` regression tests by name, and the `CapabilitiesSection` vitest file. All passed.

## Follow-ups

- F2: validate the catalogue a save will export (policy plus repository and deployment defaults) on `PUT /api/capability-policy` and the repository capability writes, so the API cannot store a catalogue `grain state check` refuses.
- F8: word the server-drop notice as grain's, not a person's, and give a drop advice that differs from a refused config.
- Item 3 live: walk gcp token mode in a container on a daemon whose gcp server uses a `gcpfake` connector. Cover the exchange at the forwarded listener, refresh to the deadline, revoke at release, and `propose_task` with the person-granted set. Walked in [2026-11-08-gcp-token-mode.md](2026-11-08-gcp-token-mode.md) (task 3414) and, on container sandboxes with a walk clock, in [2026-11-08-gcp-token-mode-containers.md](2026-11-08-gcp-token-mode-containers.md) (task 3411).
