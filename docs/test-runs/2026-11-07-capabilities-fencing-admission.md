# Capabilities, container fencing & admission — 2026-11-07 §6 walkthrough

- **Plan:** [`docs/test-plans/2026-11-07.md`](../test-plans/2026-11-07.md) §6 (PRs #814, #662/#720/#790, #767/#816, #770/#808, #812, #769, #692/#721, #691, #756), plus what was left of [`2026-11-04.md`](../test-plans/2026-11-04.md) §8.
- **Builds on:** [`2026-11-04-capabilities.md`](2026-11-04-capabilities.md) (task 3074, PR #818, merged as `b1c3151a`). That walk upgraded a schema-93 store. This one starts at schema 108, the last schema before §6's migrations, and reuses its `dump-grants.sh` approach. The 2026-11-04 F2 class of problem (a catalogue that its own export refuses) shows up again below as F1.
- **Date:** 2026-11-08, 01:45–02:30 UTC.
- **Task:** 3490, branch `grain/098073e4/task-3490-walkthrough-capabilities`, base `main` at `3a463269` (`SchemaVersion = 125`).
- **Environment:**
  - Debian 12.15 sandbox, `linux/amd64`, 7 vCPU / 30 GiB, kernel `6.1.0-54-amd64`, Docker on the host. Go `go1.26.10`, Node `v20.19.0`.
  - From apt: `sqlite3` 3.40.1, `postgresql` 15.19 (local cluster on `127.0.0.1:5432`, started with `pg_ctlcluster`) and `nftables` 1.0.6.
  - `grain-head`: `cd ui && npm ci && npm run build`, then `go build ./cmd/grain` at `HEAD`. `grain108`: `go build ./cmd/grain` in a worktree at `cc66a6bc` (PR #689, `SchemaVersion = 108`). `grain-sandbox`: `go build ./cmd/grain-sandbox` (the in-sandbox CLI that holds `grain screenshot`).
  - Every daemon below runs with `-disable-native-sandboxes -sandbox-port-base=0` ([`start108.sh`](2026-11-07-capabilities-fencing-admission/start108.sh)) and starts no runs.
  - Scripts and outputs are in [`2026-11-07-capabilities-fencing-admission/`](2026-11-07-capabilities-fencing-admission/).

## Outcome

| §6 "not proved" item | Result |
|---|---|
| 1. Cumulative 108 → 125 upgrade, `grain state` round trip, `.grain` declarations | **Pass, with one finding filed (F1) and one fix (F2).** On SQLite and PostgreSQL, the in-place migration rewrites every retired grant on every surface: grant tables, deployment and repository defaults, repository CI capabilities, console sessions, and policy rules, catalogue defaults and catalogue server args. `priority-dispatch` and `durable` become `admission`. A gcp-key grant that names an unconfigured set is kept and noted on the task, template and schedule. Deny rules written under retired names still deny, including through the implicit `skill` → `config-map` alias. The two store kinds give identical dumps, and so do the in-place and `prepare-upgrade` routes. `GET /api/notifications` is empty after the upgrade, so there are no false unserved-capability alarms. **F1:** a grant that migration 109 keeps (an `account` naming an unconfigured gcp set) on a template, schedule, deployment default or repository CI capability makes the upgraded deployment's own state repository fail `grain state check`, and makes `grain state prepare-upgrade` refuse the 108 store outright. **F2 (fixed):** `grain declarations check` never printed `admission`/`hosting`, so a template that declared the retired `durable` showed no sign it would be durable. |
| 2. `admission` dispatch and approval-rail holds | **Pass on API and unit level. A live dispatch cycle was not walked.** Operator filings through the migrated daemon's API take `admission` from the repository default (`durable`), and a bare `priority-dispatch` grant from an operator is taken as `admission: bypass`. Holds for agents, limited roles and delegated callers that set `bypass`/`durable` pass their tests. `pkg/dispatch` (slot and queue semantics of `bypass`/`durable`) passes in full. A real orchestrator dispatch needs an agent backend and a sandbox image, which this walk did not stand up (see "Not walked"). |
| 3. `host-script` per-call approval and `stdin` | **Pass (unit tests and Playwright walkthrough).** `TestHostScriptPerCallApproval`, `TestHostScriptStdinForwardingAndLimit`, `TestHostScriptApprovalCallbackThroughHostd` and `TestConvertToHostScriptsPreservesApproval` pass. So do all 7 of `local-scripts-approval-walkthrough.spec.js` / `host-script-settings.spec.js` / `bootstrap-playbooks-deprecation-notice.spec.js` / `legacy-capabilities-gcp-help.spec.js`: the Inbox prompt, approve from the task pane, deny from the Inbox, and the rail count. `TestAdmissionWithholdsWhatALocalScriptsDeclarationBecameFromAScheduledRun` covers migrated `local-scripts` on scheduled runs. A host-script call from a live run was not walked. |
| 4. Container `nftables` fence | **Pass (kernel probe and unit tests). Observation O4.** In this VM, `sudo nft` installs and removes an `inet` table with an output-hook drop rule for `169.254.169.254` (the kernel has nf_tables, and passwordless sudo gives CAP_NET_ADMIN). `TestAdmissionRefusesGrantsOnUnfencedContainerSandboxes/{unfenced,fenced}` and `TestMaterializePassOnACommandlessSandboxPlacesOnlyTheBootstrapPlaybooks` (7 subtests) pass. A daemon started with `-container-sandboxes` under `-container-sandbox-fence auto` (nft reachable, or not) and under `require` logs nothing about the fence at startup ([`fence-modes.txt`](2026-11-07-capabilities-fencing-admission/fence-modes.txt)). The probe is lazy and runs at the first sandbox or capability pass. |
| 5. `figma`, `grain screenshot`, thumbnail viewer | **Pass, with a new Playwright regression spec.** Over `grain demo -secrets-store`, the figma server is configured with a `pat` account through the Settings PUT, and its Test returns `Demo Designer <designer@demo.grain.example>`. An unknown account answers `Figma account "nobody" is not configured`. The server Test lists the `mcp` method with the account line. `grain screenshot` against a `python3 -m http.server` inside the sandbox writes 8 shots (desktop and phone, light and dark, two pages) and a contact sheet. The new [`attachment-thumbnail-paging.spec.js`](../../ui/e2e/attachment-thumbnail-paging.spec.js) attaches three PNGs to one comment and pages the viewer at 1440×900 and 390×844 in light and dark: 4/4 pass. Figma's MCP design-context and image tools could not be called over the demo, because its fake Figma API serves only `/v1/me` (O5). They pass in `TestFigmaCapabilityServerMCPAndTokenGeneration` / `TestFigmaExportImageBlocksSSRFHosts`. |

## Item 1: upgrade from schema 108

### The store

[`seed108.sh`](2026-11-07-capabilities-fencing-admission/seed108.sh) builds the store in two steps. First the schema-108 daemon's API creates the rows: two GitHub tokens (`default`, `bot`), repositories `acme/widgets` and `acme/denied`, three tasks, a template, a schedule and a console session. Then, with the daemon stopped, it writes the grants as SQL, so SQLite and PostgreSQL get identical rows:

| Surface | Retired shapes |
|---|---|
| task 2, `template-1`, `sched-1`, console session `retired-session` | `gcp#gcp-key={"account":"default","activate":true}`, `gcp#gcp-key={"account":"ghost-set","activate":false}` (a set that does not exist), `self-debug`, `bootstrap-playbooks`, `github-credential:bot`, `read-issues`, `priority-dispatch`, `durable` |
| task 3 / task 4 | `priority-dispatch` / `durable` + `gcp#gcp-key={"account":"deploy"}` |
| director session (task 5, as 108 seeds it) | `bootstrap-playbooks`, `self-debug`, `durable` (108's own defaults) |
| `grain_config.default_capabilities` | default-set gcp-key, `self-debug`, `priority-dispatch`, `read-issues` |
| `repo_config` acme/widgets | default: `account: deploy`, `bootstrap-playbooks`, `github-credential:bot`, `durable`. CI: ghost-set gcp-key, `self-debug`, `read-issues`, `bootstrap-playbooks`, `github-credential:bot`, `priority-dispatch` |
| gcp server (`mcp_servers`) | permission sets `sandbox-agent`, `deploy` |
| capability policy rules | deny `bootstrap-playbooks` / `read-issues` / `github-credential:bot` / `priority-dispatch` on `acme/denied`; allow `self-debug` and `durable` for `runKinds: [task]`; deny `skill#config-map` on `acme/skill-denied` |
| capability policy catalogue | defaults task/CI/per-repo naming the retired names and gcp-key `account` grants; gcp `gcp-key` args `account: [default, deploy, ghost-set]`, `activate: [true, false]`; `inProcess` entries for `self-debug`, `read-issues` (**disabled**), `bootstrap-playbooks`, `priority-dispatch`, `durable` |

`CLEAN=1` leaves out the two shapes that the HEAD export refuses (see F1): the ghost-set grant is kept only on task 2, and `inProcess.read-issues` stays enabled. [`walk-item1.sh`](2026-11-07-capabilities-fencing-admission/walk-item1.sh) runs everything for one store kind: seed, then in-place upgrade, dump, API policy probe, `state sync` + `state check`, and the `prepare-upgrade` route. [`dump-grants.sh`](2026-11-07-capabilities-fencing-admission/dump-grants.sh) is the 2026-11-04 dumper extended with `admission` columns, task notes and template/schedule notes.

| Run | Store | In place | `prepare-upgrade` | `state check` on the upgraded repo |
|---|---|---|---|---|
| `sqlite-ghost` | SQLite | `migrated the store in place from schema 108 to 125` | **refused** (F1) | **refused** (F1) |
| `pg-ghost` | PostgreSQL | same; dump identical to `sqlite-ghost` | **refused** (F1) | **refused** (F1) |
| `sqlite-clean` | SQLite | same | `rehearsed: 101 rows across 22 tables migrate from schema 108 into 125`, `prepared 108 125 …` | `ok: grain can load this` |
| `pg-clean` | PostgreSQL | same; dump identical to `sqlite-clean` | same (with `GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN=…`) | `ok` |

In both clean runs the dump route equals the in-place route line for line ([`*-inplace-vs-dump-route.diff`](2026-11-07-capabilities-fencing-admission/item1/) are empty; tasks travel in a `prepare-upgrade` export). The only `prepare-upgrade` warning is `tables/repo_config.json row 0 has a column "default_hostd" that repo_config does not; it would be ignored` (migration 111 dropped it). That is expected.

### What the migrations did (`item1/*-108-vs-head-inplace.diff`)

| Schema 108 | Schema 125 |
|---|---|
| `gcp#gcp-key={"account":"default","activate":true}` | `gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}` (first configured set) |
| `gcp#gcp-key={"account":"deploy"}` | `gcp#gcp-key={"permission_set":"deploy","primary":true}` |
| `gcp#gcp-key={"account":"ghost-set",…}` | kept as written. Log line per surface, and a `grain:` note on task 2 (`task_comment`), `template-1` and `sched-1` (appended to the body after `---`) |
| catalogue `gcp-key` args `account`/`activate` | `permission_set: [sandbox-agent, deploy, ghost-set]` (`default` mapped to the first set; `activate` dropped) |
| `self-debug` | `api:self-debug` + `grain#source={}` |
| `bootstrap-playbooks` | `config-map#config-map={"config_maps":["bootstrap"]}` |
| `github-credential:bot` | `repo#credential={"credential":"bot"}` |
| `read-issues` | `github#read-issues={}` |
| `priority-dispatch` / `durable` on a task | removed. `task.admission` = `bypass` / `durable` (task 2, holding both, is `durable`). The console-session task is `durable` by construction |
| on the template / schedule | `admission: durable` |
| in `default_capabilities` | `grain_config.default_admission = bypass`. On the repository: `repo_config.default_admission = durable`. In the CI list: dropped, since admission does not apply to CI |
| on a console session | stripped (sessions are durable) |
| policy rules | `deny config-map#config-map`, `deny github#read-issues`, `deny repo#credential` on `acme/denied`; `grain#source` allowed for `task` runs; the `priority-dispatch` deny and `durable` allow rules dropped; the `skill#config-map` deny kept as written |
| `inProcess` | `self-debug`, `read-issues` (`enabled: false`), `bootstrap-playbooks` kept; `priority-dispatch`/`durable` ignored with `in_process.<name> is deprecated and ignored; admission is now a task setting` |

### Policy enforcement after the upgrade ([`policy-probe.sh`](2026-11-07-capabilities-fencing-admission/policy-probe.sh), [`item1/sqlite-ghost-policy-probe.txt`](2026-11-07-capabilities-fencing-admission/item1/sqlite-ghost-policy-probe.txt))

```
acme/denied       config-map#config-map={"config_maps":["bootstrap"]}  400 … denies config-map#config-map … (rule 1: deny config-map#config-map)
acme/denied       skill#config-map={"config_maps":["bootstrap"]}       400 … denies config-map#config-map … (rule 1: …)     <- implicit alias
acme/denied       repo#credential={"credential":"bot"}                 400 … (rule 3: deny repo#credential)
acme/skill-denied config-map#config-map={"config_maps":["bootstrap"]}  400 … (rule 5: deny skill#config-map)                <- alias, other way
acme/widgets      github#read-issues={}  (and bare read-issues)        400 … is disabled … (in_process.read-issues, the capability it replaced)
acme/widgets      config-map bootstrap / repo#credential bot / grain#source   201, admission=durable (repository default)
acme/widgets      priority-dispatch (operator)                         201, admission=bypass, no grant kept
```

### `.grain` declarations ([`decl/`](2026-11-07-capabilities-fencing-admission/decl/), [`declarations-check.txt`](2026-11-07-capabilities-fencing-admission/declarations-check.txt))

- `admission: durable` on a template and `admission: bypass` on a schedule are well-formed.
- `admission: priority` (the plan's own word) is refused with `unknown admission "priority": want standard, bypass or durable`. The values are `standard`, `bypass`, `durable`: the plan's "priority" is `bypass`.
- Retired `priority-dispatch`/`durable` under `capabilities:` log `capability "durable" is deprecated; use admission: durable instead`. The other retired names (`self-debug`, `bootstrap-playbooks`, `read-issues`, `github-credential:bot`, gcp-key `account`) are accepted offline without guidance. The check has no daemon or catalogue to judge them against (O3).
- **F2 (fixed here):** neither form printed an `admission:` line in the text summary, even though `--json` carried it, so the only trace of what a declared template's tasks would be admitted as was a stderr log line. `pkg/cli/declcmd.go` now prints `admission:` and `hosting:` for templates and schedules whenever they are set. Regression: `TestDeclarationsCheckPrintsAdmissionAndHosting`.

## Item 2: admission

- Live, on the upgraded daemon: see the probe above. Admission follows `repo_config.default_admission` (`durable`), and an operator's `priority-dispatch` grant spelling becomes `admission: bypass`.
- Tests run: `go test ./pkg/dispatch/...` (ok); `go test ./pkg/ui -run 'Admission|Bypass|Durable|Priority|ApprovalRail|Delegat'` (14 pass, among them `TestAnAgentCannotGrantDurableEvenHoldingIt`, `TestProposalWithKeepRunningOrHigherPriorityWaitsForAPerson`, `TestRunNowByAnAgentDoesNotApprovePriorityDispatchEvenHoldingIt`); `TestPersonOnlyOperationsRefuseDelegation` (delegated `bypass`/`durable`); `TestStandingWorkAndFromTemplateHoldLimitedRolesToTheirScopes` (limited roles); `TestPriorityDispatchIsAPersonsToGrant` (`model.PersonOnlyProposalSettings`); `go test ./pkg/model/... -run 'Admission|HoldForApproval|Migration1(09|15|18|2[0-5])'` (ok).

## Item 3: host-script

The tests and specs are listed in the Outcome table, summarised in [`tests-run.txt`](2026-11-07-capabilities-fencing-admission/tests-run.txt) (Playwright: 7 passed, 32 s).

## Item 4: nftables

```
$ sudo nft add table inet grainwalk; … add chain … { type filter hook output priority 0; }; … ip daddr 169.254.169.254 drop
table inet grainwalk { chain out { type filter hook output priority filter; policy accept; ip daddr 169.254.169.254 drop } }
```

`go test ./pkg/orchestrator -run 'Admission|Bypass|Durable|Fence|Unfenced|Bootstrap'`: 56 pass, including the unfenced/fenced refusal and the bootstrap-only commandless placement. [`fence-modes.sh`](2026-11-07-capabilities-fencing-admission/fence-modes.sh) starts a container-sandbox daemon under `auto` (with nft, and with a `sudo` that refuses and no nft) and under `require`. None of the three logs anything about the fence at startup (O4).

## Item 5: figma, `grain screenshot`, thumbnails

- [`figma-demo.sh`](2026-11-07-capabilities-fencing-admission/figma-demo.sh) → [`figma-demo.txt`](2026-11-07-capabilities-fencing-admission/figma-demo.txt). Without `-secrets-store` the PUT is refused: `secrets are not available: this UI has no local secrets directory to write to`. The demo's own comment says its figma server is there "so Settings can add accounts and Test one". That holds only with `-secrets-store`, which the plan's shared `grain demo` command line does not pass (O5).
- `grain screenshot --url http://127.0.0.1:4231 --pages /,/settings.html --wait-for '#card'`: the stock image has no Playwright module, so the first call installs `playwright-core@1.62.1` into `~/.cache/grain/screenshot` (network needed). It then writes `home-{desktop,phone}-{light,dark}.png`, `settings_html-…` and `contact-sheet.png` ([`grain-screenshot-files.txt`](2026-11-07-capabilities-fencing-admission/grain-screenshot-files.txt), [`grain-screenshot-contact-sheet.png`](2026-11-07-capabilities-fencing-admission/grain-screenshot-contact-sheet.png)). The agent's file reader cannot render PNGs, so the sheet was checked by its file list and size only.
- [`ui/e2e/attachment-thumbnail-paging.spec.js`](../../ui/e2e/attachment-thumbnail-paging.spec.js) (new) checks four things. Three thumbnails sit inside the window. The viewer opens on the one clicked. Its arrows are on screen at the sides, within 2 px of the window's vertical middle, and below the close button. Next, ArrowRight (wrapping), ArrowLeft and Previous step through `n / 3 · <file>`, and Escape closes only the viewer. It runs at 1440×900 and 390×844, in light and dark: 4 passed. On the first run, `getByRole("dialog")` also matched the task's own overlay, so the spec names the viewer by its "Close image" button. The UI source did not change, so there are no before/after screenshots.

## Findings

- **F1 (filed, not fixed):** migration 109 keeps a `gcp#gcp-key` grant whose `account` names an unconfigured set, by design (log + note). But `CheckGCPKeyGrantArgs` refuses `account` wherever the state repository is validated: templates, schedules, `defaults.task` (deployment defaults) and `defaults.repos[…].ci`. On a deployment that held such a grant, every `grain state check` of its own repository therefore fails after the upgrade. That includes the check agents run on proposed `grain/config` changes. `grain state prepare-upgrade` (the Upgrade-button route) refuses the 108 store: `this build could not take the rows exported from the store at schema 108, so nothing is armed`. The in-place route succeeds. Repro: `OUT=/tmp/o ./walk-item1.sh sqlite-ghost sqlite`, then read `sqlite-ghost-prepare-upgrade.txt` / `sqlite-ghost-state-check.txt`. The fix needs a decision between three options: rewrite the kept grant to `permission_set: <missing>` (in a new migration, since 109 has already run on upgraded stores), drop it with a note, or let the import accept a migration-kept `account`. Filed as a follow-up rather than decided here.
  - **Fixed in task 3563:** migration 109 now rewrites such a grant to `permission_set: <missing>` (with the note), and new migration 127 does the same for stores that already ran 109. Re-run with `inProcess.read-issues` left enabled (so O1 is out of the way), `sqlite-ghost` gives `prepared 108 <SchemaVersion> …` (126 when it was walked) and `state check: ok`, and the in-place and dump routes give identical dumps. The `sqlite-ghost` seed as committed still stops at O1 (`defaults.task: capability … read-issues … is disabled`).
- **F2 (fixed):** `grain declarations check` text output omitted `admission`/`hosting`.
- **O1:** `read-issues` left in a default list while `inProcess.read-issues` is disabled is refused on export (`defaults.task: capability … is disabled`). Whether the 108 build refused it too was not checked. This is the 2026-11-04 F2 class (validate a policy save against what it will export) and is still open there.
  - **Fixed in task 3608:** API policy and default saves were already validated against the merged catalogue in task 3412 (PR #851). For existing pre-upgrade stores, migration 121 and new migration 128 (`MigrateCatalogueDisabledGrants`) drop grants that the store's own capability catalogue disables from deployment/repository defaults, catalogue defaults, templates and schedules (noting affected templates and schedules while leaving task grants, console sessions and deny rules untouched), and `staterepo.CheckMigration` checks catalogue admission against the migrated configuration. Re-run on the committed `sqlite-ghost` and `pg-ghost` seeds without modification, both give `prepared 108 128 …` and `state check: ok`, with identical dumps across SQLite, PostgreSQL, in-place and `prepare-upgrade` routes.
- **O2:** `self-debug` allow rules become `grain#source` rules only. `api:self-debug` is not a capability reference, so a rule cannot name it, and the REST half of the retired grant is no longer policy-scoped by `runKinds`.
- **O3:** offline `grain declarations check` accepts retired names other than `priority-dispatch`/`durable` (and gcp-key `account`) without guidance. They are canonicalized when the declarations sync.
- **O4:** the container fence decision is lazy, so a daemon never says at startup whether its container sandboxes are fenced. An operator learns it from the first refused capability pass.
- **O5:** the demo's figma Test needs `grain demo -secrets-store`, and its fake Figma API serves only `/v1/me`, so the MCP tools (`get_design_context`, `export_images`, …) cannot be exercised over `grain demo`.

## Not walked

- A live orchestrator dispatch of `standard`/`bypass`/`durable` tasks, a live host-script call (Deny/Approve with `stdin`) from a sandboxed run, and a live capability pass on an unfenced container. Each needs a daemon with an agent backend and a container sandbox image (the 2026-11-04 walk's `fake-agy` + `Dockerfile.walk-sandbox` set-up), which did not fit into this run's time. Unit and e2e coverage is listed per item above.

## Tests run locally

`go test ./pkg/cli -run 'TestDeclarations|TestDeclared'`, `go test ./pkg/repodecl/... ./pkg/staterepo/...`, `go test ./pkg/model/... -run …`, `go test ./pkg/orchestrator -run …`, `go test ./pkg/capability/localscripts/... ./pkg/capability/intree/... -run 'HostScript|Figma|Approval|Stdin|LocalScript'`, `go test ./pkg/dispatch/...`, `go test ./pkg/ui -run …`, `go test ./tests/hygiene/...`, and Playwright: `attachment-thumbnail-paging.spec.js` (4), `local-scripts-approval-walkthrough.spec.js` + `host-script-settings.spec.js` + `bootstrap-playbooks-deprecation-notice.spec.js` + `legacy-capabilities-gcp-help.spec.js` (7). All pass.
