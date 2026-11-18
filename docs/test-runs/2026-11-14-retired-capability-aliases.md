# Walkthrough: retired capability aliases no longer grant (2026-11-14 §1)

Walkthrough of `docs/test-plans/2026-11-14.md` §1 (PR #1455: retired bare in-process capability names no longer grant). Run as [Task #5726](/tasks/5726), filed from [Task #5629](/tasks/5629).

## Environment

- **Commit walked:** `main` at `3cc8bc207` (SchemaVersion 146), then this branch's fix (`fix(capability): name the canonical grant when refusing a retired bare name`).
- **Old build:** `v3.5.0` (SchemaVersion 136), built from a worktree as `grain-v350`.
- **Daemons:** real `grain daemon -sandbox-port-base=0 -host-sandboxes`, dispatching [`fake-agy.sh`](2026-11-14-retired-capability-aliases/fake-agy.sh). Inside each run, [`hook.py`](2026-11-14-retired-capability-aliases/hook.py) drives the run's own `grain-sandbox` MCP server.
- **Stores:** SQLite 3.40.1, and PostgreSQL 15.20 (Debian 12 packages).
- **GitHub:** [`ghsim.go`](2026-11-14-retired-capability-aliases/ghsim.go). This is the 2026-11-10 simulator plus one log line naming the credential each git request carried. Two tokens were configured: `default` (`ghp_walkthrough_stub`) and `bot` (`ghp_walkthrough_bot`).
- **Browser:** Playwright Chromium (`/var/cache/playwright`), 1440×900 and 390×844, light and dark.
- **Scripts:**
  - [`common.sh`](2026-11-14-retired-capability-aliases/common.sh): world setup, daemon start and stop, `call`, `q`.
  - [`seed.sh`](2026-11-14-retired-capability-aliases/seed.sh): seeds through v3.5.0's API.
  - [`head.sh`](2026-11-14-retired-capability-aliases/head.sh): injects bare rows, opens the store with HEAD, and dispatches.
  - [`refusals.sh`](2026-11-14-retired-capability-aliases/refusals.sh): every save path.
- **Outputs:** [`out/`](2026-11-14-retired-capability-aliases/out/). The `*-before-fix.txt` files are from `3cc8bc207`; the others are from the fixed build.

Steps (each store; `STORE=postgres PORT=4393 GHSIM_ADDR=127.0.0.1:4394` for PostgreSQL):

```sh
D=docs/test-runs/2026-11-14-retired-capability-aliases
STORE=sqlite BIN=/home/debian/bin/grain-v350 $D/seed.sh > $D/out/seed-sqlite.txt
STORE=sqlite BIN=/home/debian/bin/grain-head KEEP=1 DISPATCH="4 7 8 9" $D/head.sh > $D/out/head-sqlite.txt
STORE=sqlite $D/refusals.sh > $D/out/refusals-sqlite.txt
```

## Findings

| # | Item | Finding | Outcome |
|---|---|---|---|
| **F1** | 3 (filing and saving paths) | **Every refusal of a retired bare name said only "unknown capability grain-ci".** This was true on `POST /api/tasks` (`capabilities` and `grants`), `POST /api/tasks/{id}/capabilities`, template save, schedule save, the deployment and repo default sets and `request_access`. The repo CI picker said `capability "grain-ci" is not supported for CI`. A task row still holding a bare name was parked on `no provider is registered for capability "bootstrap-playbooks"`. Each answer was a visible 400 or park, never a 500 and never a silent drop, but none named the grant to use instead. | **Fixed.** `model.RetiredGrantRefusal` names what the name became (`model.RetiredGrantsBecame`). `capabilityRefusal` (`pkg/ui/capability_status.go`) uses it, and adds the CI Mode sentence for `grain-ci` and the CI-only sentence for `image-signing`/`registry-push`. The repo CI picker (`pkg/ui/repos.go`) and `ResolveGrants`' dispatch refusal (`pkg/model/capability.go`) append it too. Re-walked: 104 of 112 save-path answers on each store now name the canonical grant. The other 8 are `local-scripts:deploy`, which has no single canonical grant (see F3). Before the fix the count was 0 (`out/refusals-*-before-fix.txt`). Regression tests: `TestRetiredBareNamesAreRefusedNamingWhatTheyBecame` (`pkg/ui`), `TestRetiredGrantRefusalNamesWhatTheNameBecame` and `TestResolveGrantsRefusesARetiredBareRowNamingWhatItBecame` (`pkg/model`). |
| **F2** | 1–2 (store upgrade) | **No row silently loses a grant: v3.5.0 never stored the bare names.** On both stores, v3.5.0 rewrote every accepted bare name to its canonical grant on every API save path, before anything was written. That covers task, template and schedule grants, deployment and repo `default_capabilities`, and repo `ci_capabilities` (`out/seed-*.txt`). It refused the rest outright: `grain-ci`, `gcp-key*`/`gemini-key` (no gcp permission set configured), and `image-signing`/`registry-push` on a task. So HEAD opens a store written this way unchanged and every grant still works. **No schema migration is needed.** | No change. |
| **F3** | 1–2 | `local-scripts:<name>` is the one bare name v3.5.0 stored as written. It is a runtime retirement: `migrateLocalScripts` rewrites it onto `host-script` when the declaration exists. Without a declaration it granted nothing on v3.5.0 either. On HEAD the task is parked with a visible "no provider" refusal (task 8). | Unchanged behaviour. Its refusal stays "unknown capability", which is correct: `RetiredGrantsBecame` has no canonical grant for it. |
| **F4** | 5 (UI) | A bare name written straight into `repo_config.ci_capabilities` (`grain-ci,self-debug`) appears raw in `GET /api/repos/acme/widgets/settings`. `/next/repos/acme/widgets/ci` does not show it at all. Likewise `GET /api/config` drops bare `grain-ci` and `local-scripts:deploy` from `defaultCapabilities`, though the stored row keeps them. Neither grants anything, and nothing on v3.5.0's API writes such rows (F2), so the hiding is harmless. But such a row can only be seen or removed with SQL or the API. | Reported, not fixed (cosmetic; reachable only by hand-written rows). |
| **F5** | 3 (`propose_task`) | `propose_task` with `capabilities: ["self-debug","grain-ci"]` answered "Recorded" inside the run. The filed proposal had not appeared by the time the run's task sat in `awaiting_submit`, and the daemon log has no line about it. I could not tell, in the time available, whether it is filed later or refused at relay. | **Resolved in [the follow-up walk](#follow-up-walk-task-5896)** (F5 there): filed, holding the bare rows, and held for a person. Fixed. |

## Results by item

### 1. Seed v3.5.0 (SQLite and PostgreSQL) — done

`seed.sh` filed one task per bare name, then a template, a schedule, deployment defaults, repo defaults and repo CI capabilities naming every accepted name. The raw rows are in `out/seed-sqlite.txt` and `out/seed-postgres.txt`. Apart from timestamps, the two runs are identical from `== tasks` on. Every stored grant is canonical, except `local-scripts:deploy` (F2, F3).

### 2. Open with HEAD — every row *upgraded already*, or *refused visibly*

`head.sh` also wrote every bare name straight into task 9's `task_grant`, appended `grain-ci` to `grain_config.default_capabilities`, and set acme/widgets' `ci_capabilities` to `grain-ci,self-debug,…`. These are the rows the plan worried about, made by hand because nothing on the API writes them. HEAD then opened the store in place. The schema stayed 146 and no row changed (`out/head-*.txt`). For each row:

- **Canonical rows (tasks 2–7, template-1, sched-1, defaults): upgraded already.** `GET /api/tasks/{id}` reports the canonical grants. In a dispatched run:
  - Task 7 (`api#self-debug` + `grain#source`) lists the self-debug API routes under `grain api routes`. `grain ls src://` answers `src:// is not configured on this daemon`; the walkthrough daemon has no source tree, and the grant itself is honoured.
  - Task 4 (`repo#credential {bot}`) pushed its branch. The simulator logged `ghp_walkthrough_bot` on the `git-receive-pack` requests (the bot credential).
  - Both runs' MCP tool lists are the 13 standard tools; `grain-log` → `grain#journal` adds no MCP tool on this build.
  - `grain ci log` refused without `api:ci-read`, naming `api#ci-read` and saying never to ask for `grain#ci`.
- **Task 9 (bare rows): refused visibly.** Dispatch parked it in `awaiting_reply` with grain's comment. After the fix the comment names what the name became: `no provider is registered for capability "bootstrap-playbooks" (bootstrap-playbooks is a retired capability name and grants nothing any longer: it became https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]})`.
- **Task 8 (`local-scripts:deploy`): refused visibly** (F3).
- **`default_capabilities` holding bare `grain-ci`:** `GET /api/config` omits it (F4). acme/widgets stays on `ciMode: github`, so bare `grain-ci` does not select grain CI.
- **Repo CI row `grain-ci,self-debug`:** shown raw by the API, hidden on `/next/` (F4). Neither is granted to CI.

Nothing that used to grant was silently dropped. The two stores behave the same: diffing `out/head-sqlite.txt` and `out/head-postgres.txt` shows only row order.

### 3. Bare names on every filing and saving path — refused visibly; now naming the canonical grant (F1)

| Path | Before (`3cc8bc207`) | After |
|---|---|---|
| `POST /api/tasks` (`capabilities`, `grants`) | 400 `unknown capability self-debug` | 400 `self-debug is a retired capability name and grants nothing any longer: it became https://grain.dev/capabilities/api#self-debug and https://grain.dev/capabilities/grain#source={}; grant that instead` |
| `POST /api/tasks/{id}/capabilities` | 400 `unknown capability …` | same as above |
| `POST /api/templates`, `POST /api/schedules` | 400 `unknown capability …` | same |
| `PUT /api/config` / repo `defaultCapabilities` | 400 `defaultCapabilities: unknown capability …` | `defaultCapabilities: <same sentence>` |
| repo `ciCapabilities` | 400 `capability "grain-log" is not supported for CI` | `ciCapabilities: grain-log is a retired capability name …: it became …grain#journal={}; list that instead` |
| `grain-ci` anywhere on a task | `unknown capability grain-ci` | `…it became …grain#ci={}. …cannot be attached by hand … Set the CI Mode instead` |
| `image-signing` on a task | `unknown capability image-signing` | `…it became …release#image-signing={}. … is a CI capability, not a task grant …` |
| `request_access` (in a run) | `requests[0] (grant self-debug): unknown capability self-debug` | goes through the same `capabilityRefusal`, so it names the canonical grant |
| `.grain/` CI job declaration | `this job requires the capability "self-debug" and this deployment does not provide it for acme/widgets` (`pkg/workflows`) | names what it became; walked live in [the follow-up](#follow-up-walk-task-5896) (F7) |

No path answered 500 and none dropped the name silently (`out/refusals-*.txt`; the SQLite and PostgreSQL files are byte-identical).

### 4. Local policy, `settings/capabilities.yml` and `grain/config` import — walked in [the follow-up](#follow-up-walk-task-5896)

I ran out of time before item 4. Coverage remains CI's: `pkg/policy/policy_test.go` (`deny: [self-debug]`, alias globs), the catalogue binding in `pkg/ui/capability_status.go`, and the `pkg/staterepo` import tests that upgrade through `RetiredGrantsBecame`. F2 makes the import path matter less, because v3.5.0 exports already hold canonical names.

### 5. `/next/settings/capabilities` and `/next/repos/acme/widgets/ci` — renders, no overflow

[`screenshots/`](2026-11-14-retired-capability-aliases/screenshots/) holds both pages at 1440×900 and 390×844, light and dark, plus `contact-sheet.png`. They were shot by `ui/e2e/screenshot.mjs --url` against the SQLite daemon with the seeded and injected rows. This sandbox's file reader cannot display PNGs, so I checked the pages through the DOM, signed in, at every width and theme:

- `scrollWidth − clientWidth = 0` (no horizontal overflow).
- No deprecation notice.
- No bare `grain-ci` or `local-scripts:deploy` text.
- Retired rows appear only by the names they became (the CI page hides the bare CI row entirely: F4).

## Not walked

The first three items below were walked by [Task #5896](/tasks/5896): see [the follow-up walk](#follow-up-walk-task-5896).


- Item 4 live (policy deny, `settings/capabilities.yml` entry for `grain-ci`, v3.5.x export import and re-export).
- A `.grain/` CI job declaring a bare name on a grain-CI repository.
- Where the run's `propose_task` with bare names ends up (F5).
- `gcp-key*`/`gemini-key` seeded on v3.5.0: it needs a configured gcp permission set, and v3.5.0 refused these at filing without one.

## Tests run

- `go test ./pkg/model ./pkg/ui` with `GRAIN_REQUIRE_POSTGRES=1` (PostgreSQL 15 installed).
- The new regression tests listed under F1.
- The existing `TestCIOnlyCapabilities*` and grain-CI refusal tests, which still pass.

## Follow-up walk (task 5896)

[Task #5896](/tasks/5896) walked what is listed under "Not walked" above, live, on SQLite 3.40.1 and PostgreSQL 15.20. It used the same harness: v3.5.0 seeded by `seed.sh`, then a HEAD daemon dispatching `fake-agy.sh` + `hook.py` against `ghsim`. Builds:

- **HEAD:** this branch, `grain-head`.
- **Before the fix:** `main` at `c7987fe4b`, `grain-main`.
- **The seed:** `v3.5.0`.

New scripts:

- [`item4.sh`](2026-11-14-retired-capability-aliases/item4.sh) walks items 1–3 below, continuing seed.sh's data dir in place.
- [`f5.sh`](2026-11-14-retired-capability-aliases/f5.sh) walks item 4, on a fresh world.

`fake-agy.sh` now also replays `hook.py`'s `propose_task` call in its transcript. Without that, nothing a run proposes is relayed (item 4).

```sh
D=docs/test-runs/2026-11-14-retired-capability-aliases
STORE=sqlite BIN=/home/debian/bin/grain-v350 $D/seed.sh
STORE=sqlite BIN=/home/debian/bin/grain-head $D/item4.sh > $D/out/item4-sqlite.txt
STORE=sqlite WORK=/var/tmp/retired-aliases/f5 PORT=4395 GHSIM_ADDR=127.0.0.1:4396 BIN=/home/debian/bin/grain-main $D/f5.sh > $D/out/f5-sqlite-before-fix.txt
# and STORE=postgres PORT=4393 GHSIM_ADDR=127.0.0.1:4394 for item4; BIN=grain-head for f5-*.txt
```

### Findings

| # | Item | Finding | Outcome |
|---|---|---|---|
| **F5** | 4 | **A run's `propose_task` naming bare names was filed holding them, behind a misleading note.** Here is where it went. The orchestrator relays proposals from the agent's transcript, not from the MCP server. The first walk's `fake-agy.sh` emitted no tool steps, so nothing it proposed was ever relayed: that is why no proposal turned up. Once the call is in the transcript, `main` behaves as follows on both stores (`out/f5-*-before-fix.txt`). The call answers "Recorded". At the run's end, task 3 is filed `proposed`, holding the bare rows `grain-ci` and `self-debug`, with the note "approving it grants those capabilities as well as the work". When a person approves it, dispatch parks it: `no provider is registered for capability "grain-ci" (grain-ci is a retired capability name … it became …grain#ci={})`. So the proposal was not dropped silently. But it asked a person to approve grants that grant nothing, and a dispatch failure was the first anyone heard of it. | **Fixed** (`fix(capability): refuse a retired bare name in propose_task …`). The live call now refuses in the same turn, through `model.RetiredTaskGrantRefusal` (built on `RetiredGrantRefusal`): `propose_task: self-debug is a retired capability name and grants nothing any longer: it became …api#self-debug and …grain#source={}; name that instead`. For `grain-ci` it says the grant comes from the repository's CI Mode. Nothing is filed (`out/f5-sqlite.txt`, `out/f5-postgres.txt`). A call recorded before this refusal is filed without the name, and the proposal's body and the proposer's conversation each name what it became. Tests: `TestProposeTaskRefusesARetiredBareNameNamingWhatItBecame`, `TestRelayedProposalLeavesOffARetiredBareNameNamingWhatItBecame`, `TestRetiredTaskGrantRefusalNamesWhatTheNameBecame`. `TestRelayProposedTasksUnifiedGrantsEquivalenceAndNormalization` pinned the old behaviour ("kept as written, which dispatch refuses") and now pins the new one. |
| **F6** | 2 | **Importing a v3.5.0 export upgraded bare names in templates and schedules, but not in the capability columns.** Hand-added `self-debug`, `read-issues`, `github-sandbox`, `grain-log` and `github-credential:bot` stayed bare in `grain_config.default_capabilities` and in `repo_config.default_capabilities`/`ci_capabilities`. They granted nothing and were hidden by the API (F4). Every export after wrote them back out, both into `tables/` and into the `defaults:` of the new `settings/capabilities.yml`. | **Fixed** (`fix(staterepo): upgrade retired names in capability columns on import`). Importing a dump stamped with an older schema now reads those columns through `RetiredGrantsBecame`, as `shapeGrants` already reads grant lists. A dump at this build's own schema imports byte for byte as before, so the restore-fidelity tests stand. Re-walked: no bare name is left in the re-export on either store. Test: `TestAnImportUpgradesRetiredNamesInCapabilityColumns`. |
| **F7** | 3 | **The CI job refusal did not name the canonical grant.** The first walk read `pkg/daemon/ci.go`'s "is refused: it was retired" as covering `.grain/` declarations. Walked live, a job declaring `capabilities: [self-debug]` (or `[grain-ci]`) on `acme/widgets` (`ci_mode: grain`) erred with only `this job requires the capability "self-debug" and this deployment does not provide it for acme/widgets`. `workflows.UnmetRequirements` answers before that path is reached. | **Fixed** (`fix(ci): name the canonical grant …`). `workflows.CapabilitySyntax.Retired`, wired to `RetiredGrantRefusal` in `pkg/ci`, now ends the error with `…: self-debug is a retired capability name and grants nothing any longer: it became …api#self-debug and …grain#source={}; declare that instead`. The `grain-ci` job's error likewise ends `it became …grain#ci={}; declare that instead`. The prefix is unchanged, so the orchestrator's retry-once-granted matcher still reads it. `TestAJobDoesNotRunWithoutTheCapabilityItRequires` now asserts the suffix. |
| **F8** | 1 | `settings/capabilities.yml` `in_process.grain-ci: {enabled: false}` was imported, kept and re-exported under its bare key, as `in_process` keys are by design (`retiredInProcessNames`). But a run on `acme/widgets` (`ci_mode: grain`) still answered `grain ci log` with "this task's branch has no grain CI run yet", not forbidden. I did not determine whether the catalogue entry is meant to withdraw the grain#ci a repository's CI Mode grants. | Reported, not changed (proposed as a follow-up). |

### 1. Local policy and the catalogue at dispatch — refuse or bind the canonical capability

- **`deny: [self-debug, "gcp-key*"]`** (`-policy`). The task holding `…api#self-debug` + `…grain#source={}` stays queued. Each dispatch cycle logs `candidate 12 skipped: held by local policy: local policy forbids capability "https://grain.dev/capabilities/api#self-debug"`. The task holding `…gcp#gcp-key={"permission_set":"default","primary":true}` (written by SQL: this world has no gcp permission set to file it through) is held the same way by `gcp-key*`. Neither starts a run. Both stores give the same result.
- **`settings/capabilities.yml` `in_process.self-debug: {restrictions: {repos: [acme/lib]}}`**, imported from grain/config:
  - Filing `api#self-debug` on `acme/widgets` is refused: `capability api:self-debug is not granted on acme/widgets by settings/capabilities.yml (repos: acme/lib) (in_process.self-debug, the capability it replaced)`.
  - On `acme/lib` the task files and dispatches. Its run has the standard 13 MCP tools, and `grain api routes` lists the self-debug routes (`/api/fs/src/…`, `/api/tasks`, …). `grain ls src://` says `src:// is not configured on this daemon`, because the walk daemon has no source tree. `grain ci log` says `no CI run` (GitHub mode).
  - The `acme/widgets` run without the grant gets `No agent API routes are granted`, and `grain ls src://` is forbidden, naming `grain#source`.
- **`in_process.grain-ci: {enabled: false}`:** see F8.
- Inside both runs, `request_access` for `self-debug` is refused naming what it became (F1's fix, live).

### 2. Import a v3.5.0 `grain/config` export with bare names hand-added — upgraded; re-export canonical (after F6)

Setup:
- seed.sh's v3.5.0 data dir, with its `state-repo` at schema 136.
- Two hand commits on top:
  - Templates: `self-debug`, `grain-ci`, `read-issues` and `github-credential:bot`.
  - Schedules: `bootstrap-playbooks` and `grain-log`.
  - `grain_config.default_capabilities`: `,self-debug,read-issues`.
  - `acme/widgets`: `ci_mode: grain`, default `github-sandbox`, CI `grain-log,self-debug,github-credential:bot`.
  - A new `settings/capabilities.yml`.
- HEAD opened the data dir: `migrated the store in place from schema 136 to 147`, then `applied 2 previously unapplied config commit(s) … before rewriting … at schema 147`.

Result:
- Every hand-added name is stored as what it became, on both stores. For example, template `grain-ci` became `…grain#ci={}`. `acme/widgets` holds default `…github#sandbox-repo={}` and CI `…grain#journal={}, …api#self-debug, …grain#source={}, …repo#credential={"credential":"bot"}`.
- The rewritten dump names none of them. The only bare names left are `local-scripts:deploy` (F3) and the `in_process` keys of `settings/capabilities.yml` (by design, F8).
- Before the F6 fix, the default and CI columns kept the bare names, and the dump wrote them back.

### 3. A `.grain/workflows` job declaring a bare name on a `ci_mode: grain` repository — refused, now naming the canonical grant (F7)

`walk.yml` on branch `walk-ci` has jobs `bare` (`capabilities: [self-debug]`) and `bareci` (`[grain-ci]`). `POST /api/ci/runs` started the run, which concluded `errored`. Both jobs erred, each naming what its name became (F7). Neither spent a sandbox.

### 4. `propose_task` with `["self-debug","grain-ci"]` — see F5

### Tests run (task 5896)

- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/model ./pkg/mcp ./pkg/orchestrator ./pkg/staterepo/... ./pkg/ci ./pkg/workflows ./pkg/daemon` passed on the final tree, with PostgreSQL 15 installed so the Postgres tests ran rather than skipping.
- `./pkg/ui` passed after the F5 fix. It was not re-run locally after F6 and F7; GitHub Actions covers it.
