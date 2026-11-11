# Capabilities & Grants Walkthrough — 2026-11-11

Walkthrough of `docs/test-plans/2026-11-11.md` §6 ("Capabilities and grants": canonical migration, policy over repo grants, accept-once declarations, default `api:ci-read`, `ci_api_repos`, managed GCP token sets, waiting capability servers), run as [Task #4519](/tasks/4519) and filed from [Task #4297](/tasks/4297). PRs in scope: #1051, #1117, #1101, #1194, #1185, #1118, #1063, #1152, #1197.

## Environment

- **Builds:**
  - `HEAD`: branch `grain/098073e4/task-4519-walkthrough-canonical-grant` from `83c3a1c1b`, `SchemaVersion = 141`, built to `/home/debian/bin/grain`.
  - Baseline: `61668e58`, `SchemaVersion = 131`, built in a worktree to `/home/debian/bin/grain-old`.
  - `go1.26.10 linux/amd64`, Node `v20.19.0`. The UI was built with `cd ui && npm ci && npm run build`.
- **Stores:** embedded SQLite, and real PostgreSQL 15.19 (`postgresql` from apt, cluster `15/main` on `127.0.0.1:5432`, role `debian`).
- **GitHub:** `docs/test-runs/2026-11-10-unified-grants/ghsim.go`, which serves `pkg/github/githubsim` and real bare repositories `acme/{widgets,lib,other}` over `git http-backend`. The daemon runs with `-github-host 127.0.0.1:<port> -github-insecure-http`.
- **Dispatch:** a real `grain daemon -host-sandboxes -sandbox-port-base=0` running [`fake-agy.sh`](2026-11-11-capabilities-grants/fake-agy.sh) as the Antigravity CLI.
  - It saves the prompt grain writes on stdin.
  - It runs [`hook_ci.py`](2026-11-11-capabilities-grants/hook_ci.py) against the run's own `grain mcpserver` over stdio.
- **CI:** for item 4, grain's own CI (`-ci`, the default) runs on the same daemon in host sandboxes. The GitHub-CI repository in item 2 has no Actions runs, so `grain ci list` answers with an empty list.
- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) over `grain demo -github-sim -fake-ci`, at 1440×900 and 390×844, light and dark.
- **Google boundary:** nothing was stubbed. Item 5 could not be pointed at a stub without a code change; see below.

### Reproduction

```bash
sudo apt-get update && sudo apt-get install -y postgresql postgresql-client sqlite3 jq
sudo pg_ctlcluster 15 main start && sudo -u postgres psql -c "CREATE ROLE debian LOGIN SUPERUSER PASSWORD 'walk'"
git worktree add /home/debian/old 61668e58 && (cd /home/debian/old && go build -o /home/debian/bin/grain-old ./cmd/grain)
go build -o /home/debian/bin/grain ./cmd/grain
D=docs/test-runs/2026-11-11-capabilities-grants
bash $D/item1_migration.sh sqlite && bash $D/item1_migration.sh postgres   # item 1
bash $D/item2_ci_read.sh                                                    # item 2
bash $D/item3_declarations.sh                                               # item 3
bash $D/item4_ci_api_repos.sh                                               # item 4
(cd ui && npm ci && npm run build); grain demo -github-sim -fake-ci -addr 127.0.0.1:4310 &
cp $D/item6_waiting_ui.mjs ui/e2e/zz-item6.mjs && (cd ui && node e2e/zz-item6.mjs http://127.0.0.1:4310 "$(cat /tmp/grain-demo-*/local-api-token)" ../$D/out/item6); rm ui/e2e/zz-item6.mjs
```

The item scripts share [`common.sh`](2026-11-11-capabilities-grants/common.sh): the simulator, daemon start/stop, API `call` and fake-agent setup. Fixture repository content is in `seed3/` and `seed4/`. It is stored as `dot-grain/` so this repository's own `.grain` readers never see it, and renamed to `.grain` when seeded. Captured output is in [`out/`](2026-11-11-capabilities-grants/out/).

## Findings

| # | Item | Finding | Outcome |
|---|---|---|---|
| **F1** | 3 (declarations) | **An accepted declaration's firings land approved without the accepted grant.** A repo-declared schedule (`grants: [api#secrets:write]`) firing a declared template carried the grant on its first firing only. After a person accepted the set, the next firing landed approved but held only `api#ci-read`. The `schedule_grant` row was gone. Cause: `Schedule.syncTemplateCache`, run after every firing, replaced the schedule's grants with the template's, which were empty. `ComposeScheduleTask` files the schedule's own grants beside the template's, so every later firing lost them, until the declaration sync rewrote the schedule on a new commit. | **Fixed** in `pkg/model/schedule_fire.go`. The refresh now keeps grants whose provenance is a `.grain/schedules/` declaration file. The declaration sync owns those and removes them with the file, so nothing an operator removed from a template can linger. Regression test: `TestADeclaredSchedulesOwnGrantsSurviveItsFirings` (`pkg/model/declared_schedule_own_grants_test.go`). It fires the stored schedule twice without re-putting it, which is what the existing `fireForTask` helper hid. It failed before the fix and passes after. Re-walked on the real daemon: the accepted firing now holds `api#secrets:write`. |
| **F2** | 3 (declarations) | **There is no revoke.** The plan and 65541710's message describe `POST /api/declarations/{accept,revoke}`, but 5bed26b6 dropped the revoke route. A real daemon answers `no such API endpoint: POST /api/declarations/revoke`, and the Repository declarations card has no revoke control. Once a dangerous scope is accepted, nobody can withdraw it. | **Proposed** as a follow-up: restore revoke, or correct the plan and docs. Needs a route, the authorization matrix, generated routes and UI, so it is too large for this task. |
| **F3** | 4 (`ci_api_repos`) | Admission follows the setting live, as designed. But in the job that was admitted, the step saw no API token: `/home/debian/.grain-api/token`, `$HOME/.grain-api/token`, `GRAIN_API_TOKEN` and `GRAIN_API_URL` were all absent or empty in a host-sandbox CI job. | **Proposed** as a follow-up: check whether ciapi placements reach host-sandbox CI steps. |
| **F4** | 5 (managed GCP sets) | **Not walkable on a real daemon.** Nothing on the daemon can point `gcpsets.GoogleConnector` at a stub IAM/CRM/IAM Credentials/STS endpoint, and `gcpfake` is a Connector-level in-memory fake rather than an HTTP stub. | **Proposed** as a follow-up: an opt-in test endpoint seam plus an HTTP stub over `gcpfake`. Coverage remains `pkg/capability/gcpsets/managed_token_test.go`. |
| **F5** | 3 / schedules | Same root cause as F1, outside this fix: `syncTemplateCache` also overwrites a schedule's own `admission:` and `hosting:` raise with the template's. Operator template-backed schedules with extra grants are ambiguous in the cache. | **Proposed** as a follow-up, depending on this task. |
| **N1** | 2 (prompt) | The CI section of the prompt names `grain ci run-job` and `grain ci retrigger` once, only to say they "drive grain CI only and cannot rerun GitHub's checks: push a commit for that". They are not offered as usable commands, which matches `ci_prompt_golden_test.go`. | Note only. |
| **N2** | 6 (alert) | The capability-server-down alert clearing by itself needs a real server that waits and then stops waiting. That was not reproduced here; `pkg/capability/protocol/waiting_test.go` covers it. | Note only. |

## Results by item

### 1. Canonical-grant migration on real SQLite and PostgreSQL stores — **PASS**

Harness: [`item1_migration.sh`](2026-11-11-capabilities-grants/item1_migration.sh).

Setup:
1. A real `61668e58` daemon (schema 131) created a held task, a template, a schedule, the `ops` console session and repository configs.
2. The store was then seeded with every legacy spelling in every table migration 132 rewrites. The spellings are `api:tasks:read@acme/widgets`, its canonical twin `…/api#tasks:read={"repo":"acme/widgets"}`, `api:ci-read`, the non-canonical `…/api#test-quarantine={}`, `repo-read:acme/lib`, `repo-write:acme/other`, and the bare `release-gate-read`. The tables are:
   - `task_grant`, `template_grant`, `schedule_grant` and `task_grant_wait`;
   - `grain_config.default_capabilities`;
   - `repo_config.default_capabilities` and `ci_capabilities`;
   - console session `Capabilities` and `declinedDefaults`;
   - `capability_policy` catalogue defaults (`task`, `ci`, per-repo `task`/`ci`).
3. `HEAD` then opened the store in place.

Results, identical on SQLite and PostgreSQL:
- Schema went from 131 to 141.
- Every grant table holds 6 distinct canonical rows. The 7 seeded rows lose one to the canonical twin, and the bare `release-gate-read` is left alone. No `api:`, `repo-read:`, `repo-write:` or `={}` survived anywhere.
- `task_grant_wait` became `api#ci-read={"repo":"acme/widgets"}`.
- The console session's declined default became canonical too: `declinedDefaults: ["…/api#ci-read={\"repo\":\"acme/widgets\"}"]`.
- The catalogue defaults were rewritten per list.
- `GET /api/tasks/{id}` on the held task reports the same six canonical grants, so no grant was lost.

Outputs: [`out/item1-sqlite-after.txt`](2026-11-11-capabilities-grants/out/item1-sqlite-after.txt) and [`out/item1-postgres-after.txt`](2026-11-11-capabilities-grants/out/item1-postgres-after.txt). With task ids normalized, `diff` between the two is empty.

**Hand-edited `grain/config` import.** After `grain state sync`, `settings/templates.yml` and `settings/schedules.yml` were edited by hand back to `api:tasks:read@acme/widgets` (beside its canonical twin), `repo-read:acme/lib` and `api:test-quarantine`, plus a new `api:releases:read@acme/widgets`. The edit was committed while the daemon ran. The daemon applied it with every row canonical, the twin deduplicated, and the added grant kept as `api#releases:read={"repo":"acme/widgets"}`. Outputs: [`out/item1-sqlite-import.txt`](2026-11-11-capabilities-grants/out/item1-sqlite-import.txt) and [`out/item1-postgres-import.txt`](2026-11-11-capabilities-grants/out/item1-postgres-import.txt).

Not done: re-dispatching the migrated held task, schedule and console session end to end. Effective access was compared through the API's resolved `capabilities` instead.

### 2. Default `api:ci-read` in a real run — **PASS**

Harness: [`item2_ci_read.sh`](2026-11-11-capabilities-grants/item2_ci_read.sh), [`hook_ci.py`](2026-11-11-capabilities-grants/hook_ci.py). `acme/widgets` was set to `ci_mode: github`, and a task was filed approved and dispatched through `fake-agy.sh`.

- The task holds `https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}` by default ([`out/item2-task.json`](2026-11-11-capabilities-grants/out/item2-task.json)).
- Over the run's own MCP server ([`out/item2-hook.json`](2026-11-11-capabilities-grants/out/item2-hook.json)):
  - `grain ci list` answers for acme/widgets: "0 of 0 matching run(s) in acme/widgets".
  - `grain pr status` answers for the task's branch.
  - `grain ci list --repo acme/lib` and `grain pr status --ref main --repo acme/lib` are refused: `forbidden: acme/lib is outside this sandbox's CI read scope`.
- The MCP tool list no longer offers `pull_request_status`, `base_branch_status` or `wait_for_checks`.
- `request_access` for `grain#ci` is refused with the hint: "…grant https://grain.dev/capabilities/api#ci-read with {repo: "acme/widgets"} instead … This task holds … already."
- In the prompt ([`out/item2-prompt-ci-counts.txt`](2026-11-11-capabilities-grants/out/item2-prompt-ci-counts.txt)), the CI line lists `grain ci wait/list/log/test-history` and `grain pr status/base-status/open/reply/resolve/review-comment`. `run-job` and `retrigger` appear only in the "cannot rerun GitHub's checks" sentence (N1).
- **Deny rule:** after `PUT /api/capability-policy` with `{"rules":[{"capability":"https://grain.dev/capabilities/api#ci-read","deny":true}]}`, the next filing holds no default: `capabilities: []` ([`out/item2-task-denied.json`](2026-11-11-capabilities-grants/out/item2-task-denied.json)).
- **Deny on task repo grants:** with a deny rule on `…/repo#write`, a task with `writes: [acme/lib]` (beyond its target) was not dispatched. Its row says "grain refused the capability `https://grain.dev/capabilities/repo#write` … the capability policy denies … (rule 1)". A task writing only its own target ran normally ([`out/item2-repo-write-deny.json`](2026-11-11-capabilities-grants/out/item2-repo-write-deny.json)).

### 3. Declarations: accept, widen, revoke, refusals — **FAIL → fixed (F1); revoke missing (F2)**

Harness: [`item3_declarations.sh`](2026-11-11-capabilities-grants/item3_declarations.sh), fixture `seed3/widgets/dot-grain/{templates,schedules}/walk.yml`. Repository declarations were turned on for acme/widgets, and the schedule was fired with `POST /api/schedules/{id}/run`. The previous firing was closed first, because `overlap: skip` otherwise suppresses the run. Output: [`out/item3-declarations.txt`](2026-11-11-capabilities-grants/out/item3-declarations.txt).

1. **Unaccepted:** the firing is filed `proposed` and unapproved, with `api#secrets:write` and the note "Nobody has accepted that set yet". `GET /api/declarations` lists it `awaiting`, with `needsScopes: ["secrets:write"]`.
2. **Refusal, stale set:** `POST /api/declarations/accept` with `seen: []` is refused: "…now asks for https://grain.dev/capabilities/api#secrets:write: reload and accept what it asks for now".
3. **Accept:** recorded with `acceptedBy: owner@example.com` and `acceptedScopes: ["secrets:write"]`. The next firing lands `approved: true`.
   - **Before the fix it held only `api#ci-read` (F1).**
   - After the fix it holds `api#secrets:write` as well.
4. **Widen:** the declared template gains `network: open`. After the two-minute declaration poll, the next firing is held again with `networkAccess: "open"`. Its note reads "…asks for https://grain.dev/capabilities/api#secrets:write, network_access: open. That is more than a person last accepted for it".
5. **Revoke:** `POST /api/declarations/revoke` → `no such API endpoint` (F2).

Not walked here, because the access-role and agent-token rails belong to §5 (sibling walkthrough):
- acceptance by a role lacking the scope;
- acceptance by an agent;
- the `.grain/workflows` `capabilities:` variant.

They are covered by `pkg/ui/config_change_scopes_internal_test.go` and `pkg/daemon/ci_declaration_test.go`. Item 4 below shows a workflow job refused, then admitted, for an api grant.

### 4. `ci_api_repos` with no restart — **PASS (admission); token placement not observed (F3)**

Harness: [`item4_ci_api_repos.sh`](2026-11-11-capabilities-grants/item4_ci_api_repos.sh), fixture `seed4/widgets/dot-grain/workflows/probe.yml`, a job declaring `https://grain.dev/capabilities/api#ci-read`. Each probe pushes a fresh commit first, because grain CI reuses a job result for a commit it already ran. Runs use `workflow_dispatch`; api grants are withheld from `push` runs ("withheld on push runs (released only to release, workflow_dispatch, and grain's scheduled runs)"). Output: [`out/item4-ci-api-repos.txt`](2026-11-11-capabilities-grants/out/item4-ci-api-repos.txt). The daemon pid was unchanged throughout.

1. **Not listed:** the job is `errored`: "CI capability "api:ci-read" refused: api:ci-read is not offered to acme/widgets's jobs … offered to CI for ai-on-gke/grain only".
2. **A merged `tables/grain_config.json` commit sets `ci_api_repos: "acme/widgets"`:** the store reflects it within seconds, and the next job succeeds.
3. **A second commit clears it:** the next job is `errored` again with the same refusal.

### 5. Managed token-mode GCP sets against a stub IAM/STS — **NOT WALKED (F4)**

No seam points a real daemon's `gcpsets` at stub Google endpoints, and stubbing the Google boundary without one would mean patching `pkg/capability/gcpsets` only for the walkthrough. Follow-up proposed.

### 6. Waiting-server UI — **PASS (layout); alert clearing not reproduced (N2)**

Harness: [`item6_waiting_ui.mjs`](2026-11-11-capabilities-grants/item6_waiting_ui.mjs). `GET /api/capability-servers` answers an array, and `screenshot.mjs`'s `--merge` lays top-level object fields, so it cannot be used here. The helper intercepts the route instead and sets the demo's `gchat` server to `status: {state: "needs-config", waiting: "the host's gcloud credential to be renewed (run the host's login command)"}`. It shoots `/settings/capabilities` and the server's page at 1440×900 and 390×844, light and dark: 8 shots.

All 8 pass ([`out/item6/item6-results.json`](2026-11-11-capabilities-grants/out/item6/item6-results.json)):
- the list's chip reads "waiting for the host's gcloud credential to be renewed (…)";
- the server page's note reads "Waiting for … grain tries it again every minute, and it attaches by itself …";
- the chip's bounding box stays inside its row;
- no page scrolls sideways (`scrollWidth ≤ innerWidth`);
- no page errors.

The screenshots were not committed: this task changes no UI, so there is no before/after pair.

## CI-only coverage relied on

`pkg/model/sqlite/canonical_grant_migration_test.go`, `pkg/model/{ci_read_default,declared_grant_hold}_test.go`, `pkg/ui/ci_read_default_test.go`, `pkg/gitproxy/cli_ci_read_default_test.go`, `pkg/orchestrator/ci_prompt_golden_test.go`, `pkg/capability/gcpsets/managed_token_test.go`, `pkg/capability/protocol/waiting_test.go`, `pkg/daemon/ci_declaration_test.go`, plus the new `pkg/model/declared_schedule_own_grants_test.go`.
