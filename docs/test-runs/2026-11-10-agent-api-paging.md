# Agent API list paging: `limit`, `cursor` and `q` (2026-11-10 plan, §6)

**Task:** 3953 (filed by task 3930). **Plan:** [`docs/test-plans/2026-11-10.md` §6](../test-plans/2026-11-10.md#6-agent-api-list-paging-limit-cursor-and-q).
**Scope under test:** PR #709 (`a93b8acc`, `f77d856d`): `pkg/ui/list_page.go` paging on every growing GET collection route exposed as an `api_*` tool, agent-caller default pages, `reachable-repos` filters, role-aware `q` on access principals, and `docs/agent-api.md`'s paging table.
**Branch:** `grain/098073e4/task-3953-walkthrough-agent-api-list`, with `origin/main` (64 newer commits) merged in before this run's fixes.
**Harness:** [`2026-11-10-agent-api-paging/`](2026-11-10-agent-api-paging/). Results are in [`results/`](2026-11-10-agent-api-paging/results/).

Attempt 1 (commit `2f9b53e2`) fixed the first round of defects and committed the seed and walk harness. This run (attempt 2) re-ran the whole walkthrough from a fresh sandbox against the merged branch. It fixed five more defects the re-run found, made the walker data-driven so its own probes no longer produced false anomalies, and added items 2–4.

## Environment

- `grain` built from this branch's `HEAD` (`cd ui && npm ci && npm run build`, `go build ./cmd/grain`).
- [`env.sh`](2026-11-10-agent-api-paging/env.sh) `start_paging_env sqlite|postgres` does the following:
  - Builds [`ghsim.go`](2026-11-10-agent-api-paging/ghsim.go): a GitHub simulator serving `acme/widgets` (265 branches, 265 `.grain/workflows/*.yml`, smart-HTTP git through `git http-backend`) and 305 reachable repositories.
  - Starts a real `grain hostd` with 265 allowlist entries.
  - Runs [`seed.go`](2026-11-10-agent-api-paging/seed.go) (`-count 260`): ≥260 rows per store collection (tasks, CI runs and artifacts, templates, schedules, suites and suite runs, boards, memories and documents, release notes, secrets and secret audit, config and access audit, notifications, inbox questions, external-agent tokens, GCP keys, GitHub tokens, hosts, persistent sandboxes, console sessions, scope sets, allowed principals), plus 265 agent models (fake `agy`) and 265 upgrade targets.
  - Starts `grain daemon -sandbox-port-base=0 -ui-addr 127.0.0.1:4281` over SQLite, or `:4282` over PostgreSQL 15 (`-db-driver postgres`).
  - Turns on `externalAgentApi` through `PUT /api/settings`.
- Three external credentials are minted into `/var/tmp/.../tokens.json` (never committed): a full reader, a `repos: [acme/widgets]` credential, and a per-scope `@acme/widgets` qualifier credential. The two task identities are 9000 (unrestricted) and 9001 (grants `@acme/widgets` only).
- Tokens and the access code never appear in committed output. `walk_routes.py` records request sizes with `Authorization` redacted.

## Defects found and fixed in this run

Each fix has a deterministic reproduction and a Go regression test in `pkg/ui/list_page_walkthrough_test.go`. Each test was confirmed to fail with the fix reverted and pass with it.

| # | Defect (live reproduction) | Fix | Regression test |
|---|---|---|---|
| D1 | **A repo-limited agent's `GET /api/events` page never returned and kept a CPU spinning.** Task 9001 (`@acme/widgets`) walked `?since=…` with the default limit. The page after `since=744` was still running after 120 s. `readAuthorizedEvents` stopped advancing its scan position once the page was full. When a full batch (100) of other repositories' events followed, it re-read the same batch forever. Long-poll and SSE share the function. | `pkg/ui/events.go`: a separate `queryAfter` that always moves past every row read. The client's cursor still stops at the page's last event. | `TestEventsPageAfterLastReadableEventReturnsForRepoLimitedAgent`: 3 readable then 150 unreadable events, `limit=1`, 10 s guard per page. |
| D2 | **Task-list cursors never advanced past tasks with no filing or closing time.** `time.Time{}.UnixNano()` is outside int64, so it answers `-6795364578871345152`, which reads back as 1754. The next page began at the same task, and agents and `HTTPClient.ListTasks` looped forever. Attempt 1's scope test hung CI's `pkg/ui` for 10 min (`TestRepoLimitedCredentialsNeverLeakReachableReposOrTasksCounts/task-token`). | `pkg/ui/task_list_filter.go`: a zero time encodes as `z.<id>` and decodes back to zero. Old numeric cursors still parse. | `TestTaskListCursorAdvancesPastTasksWithNoTime`. The scope test now has a page guard, and its expected totals were corrected (the caller's own task is on the allowed repository). |
| D3 | **`GET /api/deployment/git-connections`: an agent's default page was 28,049 B.** Each row's `status.defaultKnownHosts` repeats GitHub's ~450 B host keys, and 50 rows of ~280 B are 14 KB even without them. | Paged calls blank `defaultKnownHosts`, and the default page is 20 (`listPageLarge`). A person's unpaged read is unchanged. Doc row updated. | `TestGitConnectionsAndCapabilityServersAgentPagesFitToolResultCap`. |
| D4 | **`GET /api/capability-servers`: default page 12,033 B.** The `api` capability lists one method per scope set (279 here), so `status.methods` was 6.8 KB on one row. | Paged rows cap `status.methods` at 40, with `methodCount` and `methodsTruncated`. Fields were added on `ui.CapabilityServer`, so `pkg/capability/*` is untouched. Doc row updated. | Same test as D3. |
| D5 | **`GET /api/external-agents/tokens`: default page 12–20 KB in `TestAllHeavyPagedRoutesStayUnderToolResultCap`** (red on the merged branch). `resolvedScopes` is scopes × repositories (27 entries for `grain/external-dev` on 3 repos). | Paged rows leave out `resolvedScopes` and report `resolvedScopeCount` and `resolvedScopesTruncated`. `scopeSet`, `scopes` and `repos` stay complete. `whoami` and the UI are unaffected. Doc row updated. | `TestAllHeavyPagedRoutesStayUnderToolResultCap` (now green). |
| D6 | **`GET /api/tasks`: a later default agent page was 14,467 B.** The director's row carried `reads` for 262 repositories (7.8 KB). | Paged list rows (`fields` unset or `list`) cap `reads` at 10, with `readCount` and `readsTruncated` on `ui.Task`. `fields=summary`/`full` and `GET /api/tasks/{id}` are unchanged. Doc row updated. | `TestTaskListRowCapsReadsForAgents`. |

Docs-only drift fixed in `docs/agent-api.md` (and the `ref` description in `agent_api_tools.go`):

- `GET /api/ci/workflows` needs `ref`: without one it answers 400 ("a run names the branch to run against"). The table listed it as an optional filter.
- `limit` above 1000 is answered as 1000, with `truncated` and `nextCursor`, not refused. This is now spelled out (see "Plan drift").
- `fields=full` task rows carry whole descriptions, so a default page of five can pass the cap. A seeded page was 35,541 B with 6 KB descriptions. The doc now says to use a smaller `limit` or `GET /api/tasks/{id}`. `fields=full` is an explicit opt-in to whole records, so this is documented, not changed.

Attempt 1's fixes (`2f9b53e2`) are re-verified by this run's walk:

- tasks paging in all `fields` modes, branch/externalRef/id in `q`, and 400 on malformed or foreign cursors;
- holder caps on the usage routes, schedule prompts omitted, console-session banners omitted, CI fault caps;
- memoized git credential status;
- `limit` honoured on human `access/history`;
- repo-limited `reachable-repos` narrowed with counts redacted, and per-scope `@repo` qualifiers.

## Item 1: every documented route, for real

[`walk_routes.py`](2026-11-10-agent-api-paging/walk_routes.py) does the following for each route in the doc's table:

1. Reads the person's unpaged answer.
2. Reads the agent's (task 9000) unparameterised page and checks the documented default, the 10,240 B cap, `truncated` and `nextCursor`.
3. Follows `nextCursor` to the end and compares the union with the person's answer (no duplicates, same rows) and checks every page against the cap.
4. Takes the middle row's key field (a documented `q` field), swaps its case, and checks `q` returns that row.
5. Sends `limit=201`, `limit=-1`, `limit=abc`, a forged cursor and another route's cursor.

The insert test inserts between pages on keyset (tasks, events) and offset (templates, secrets) routes.

**Result (SQLite, final run): no route over the cap on any page, no duplicates, the union equal to the person's answer on every route with a stable unpaged answer, and case-insensitive `q` matching on every route that has `q`.** Every walked route answered 400 to a forged cursor, another route's cursor, `limit=-1` and `limit=abc`. The only remaining walker anomalies are seed coverage gaps (below).

Union `None` means the person's unpaged answer is itself capped (`audit-logs`, `config-audit`, `access/history` at 100, `ci/runs` at 100) or is the live view (`tasks` without `fields`, which hides closed tasks), so no equality applies. The walk still found no duplicates and the documented counts. Request size is the HTTP request line plus headers, with credentials counted as `REDACTED`.

| Route | Doc default | Page 1 rows | Req B | Page 1 B | Max page B | Person unpaged B | total | Pages | Dups | Union = person | `q` (case swapped) → rows | `limit=201` | forged / other cursor | PostgreSQL page 1 B · union |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `/api/deployment/reachable-repos` | 100 | 100 | 134 | 5,212 | 5,878 | 16,560 | 305 | 4 | 0 | yes | `GKE-LABS/SERVICE-145-COMPONENT` → 1 | 200 | 400 / 400 | 5212 · yes |
| `/api/deployment/git-connections` | 20 | 20 | 134 | 5,929 | 5,944 | 146,148 | 261 | 14 | 0 | yes | `BOT` → 261 | 200 | 400 / 400 | 5929 · yes |
| `/api/deployment/secret-audit` | 50 | 50 | 131 | 8,153 | 8,164 | 16,192 | 262 | 6 | 0 | yes | `SERVICE-SECRET-211` → 1 | 200 | 400 / 400 | 8146 · yes |
| `/api/deployment/audit-logs` | 20 | 20 | 129 | 3,613 | 3,674 | 17,792 | 322 | 17 | 0 | n/a | `SERVICE-SECRET-211/API-KEY-001` → 1 | 200 | 400 / 400 | 3606 · n/a |
| `/api/deployment/config-audit` | 20 | 20 | 131 | 4,234 | 4,234 | 20,253 | 282 | 15 | 0 | n/a | `aUDIT CHANGE 231 ON ACCESS` → 1 | 200 | 400 / 400 | 4228 · n/a |
| `/api/repos/acme/widgets/branches` | 50 | 50 | 135 | 5,463 | 5,464 | 28,082 | 260 | 6 | 0 | yes | `GRAIN/CUT-BRANCH-129` → 1 | 200 | 400 / 400 | 5463 · yes |
| `/api/repos/acme/widgets/github-branches` | 50 | 50 | 142 | 963 | 964 | 4,780 | 266 | 6 | 0 | yes | `FEAT-BRANCH-133` → 1 | 200 | 400 / 400 | 963 · yes |
| `/api/repos/acme/widgets/workflows` | 5 | 5 | 136 | 1,002 | 1,005 | 46,343 | 260 | 52 | 0 | yes | `.GRAIN/WORKFLOWS/DECLARED-130.YML` → 1 | 200 | 400 / 400 | 1002 · yes |
| `/api/repos/acme/widgets/releases` | 20 | 20 | 135 | 3,043 | 3,044 | 38,742 | 260 | 13 | 0 | yes | `REL-129` → 1 | 200 | 400 / 400 | 3043 · yes |
| `/api/repos/acme/widgets/releases/rel-000/candidates` | 20 | 20 | 154 | 3,723 | 3,724 | 47,547 | 261 | 14 | 0 | yes | `REL-000.RC.131` → 1 | 200 | 400 / 400 | 3723 · yes |
| `/api/tasks` | 10 | 10 | 113 | 7,126 | 8,576 | 357,221 | 523 | 53 | 0 | n/a | `269` → 1 | 200 | 400 / 400 | 7096 · n/a |
| `/api/tasks?fields=summary` | 50 | 50 | 128 | 6,156 | 6,156 | 61,670 | 523 | 11 | 0 | yes | `260` → 1 | 200 | 400 / 400 | 6006 · yes |
| `/api/tasks/states` | 250 | 250 | 120 | 4,194 | 5,256 | 9,638 | 523 | 3 | 0 | yes | — | 200 | 400 / 400 | 4184 · yes |
| `/api/states` | 250 | 250 | 114 | 4,194 | 5,256 | 9,638 | 523 | 3 | 0 | yes | — | 200 | 400 / 400 | 4184 · yes |
| `/api/tasks/1/messages` | 10 | 10 | 124 | 1,564 | 1,584 | 39,414 | 260 | 26 | 0 | yes | `REVIEWER-10@EXAMPLE.COM` → 13 | 200 | 400 / 400 | 1564 · yes |
| `/api/inbox` | 20 | 20 | 113 | 2,544 | 4,139 | 70,244 | 446 | 24 | 0 | yes | `sTANDING tRIAGE sESSION 244` → 1 | 200 | 400 / 400 | 2544 · no |
| `/api/notifications` | 20 | 20 | 121 | 6,773 | 6,774 | 67,063 | 200 | 10 | 0 | yes | `CI:SUBJECT-159` → 1 | 200 | 400 / 400 | 6773 · yes |
| `/api/ci/runs` | 10 | 10 | 115 | 6,282 | 6,283 | 160,993 | 260 | 26 | 0 | n/a | `CI-RUN-129` → 1 | 200 | 400 / 400 | 6282 · n/a |
| `/api/ci/runs?view=compact` | 20 | 20 | 128 | 6,712 | 6,713 | 86,113 | 260 | 13 | 0 | n/a | `CI-RUN-129` → 1 | 200 | 400 / 400 | 6712 · n/a |
| `/api/ci/workflows?repo=acme/widgets&ref=trunk` | 20 | 20 | 148 | 1,287 | 1,288 | 16,193 | 265 | 14 | 0 | yes | `.GRAIN/WORKFLOWS/WF-132.YML` → 1 | 200 | 400 / 400 | 1287 · yes |
| `/api/ci/runs/ci-run-000/artifacts` | 50 | 50 | 136 | 6,499 | 6,500 | 33,340 | 260 | 6 | 0 | yes | `BUNDLE-005` → 1 | 200 | 400 / 400 | 6492 · yes |
| `/api/ci/faults` | 20 | 0 | 117 | 27 | 27 | 27 | 0 | 1 | 0 | yes | — | 200 | 400 / 400 | 27 · yes |
| `/api/merge-batches` | 20 | 0 | 121 | 36 | 36 | 36 | 0 | 1 | 0 | yes | — | 200 | 400 / 400 | 36 · yes |
| `/api/base-branch-gates` | 20 | 0 | 125 | 13 | 13 | 13 | 0 | 1 | 0 | yes | — | 200 | 400 / 400 | 13 · yes |
| `/api/memory` | 5 | 5 | 114 | 4,146 | 4,164 | 213,996 | 261 | 53 | 0 | yes | `ACME/MEM-REPO-131` → 1 | 200 | 400 / 400 | 4146 · yes |
| `/api/memory/documents` | 20 | 20 | 124 | 4,121 | 4,144 | 53,005 | 260 | 13 | 0 | yes | `ARCHITECTURE-NOTE-130` → 1 | 200 | 400 / 400 | 4121 · yes |
| `/api/templates` | 5 | 5 | 117 | 4,511 | 4,624 | 239,328 | 261 | 53 | 0 | yes | `TMPL-129` → 1 | 200 | 400 / 400 | 4511 · yes |
| `/api/templates?fields=summary` | 20 | 20 | 132 | 4,218 | 4,344 | 56,368 | 261 | 14 | 0 | yes | `TMPL-129` → 1 | 200 | 400 / 400 | 4218 · yes |
| `/api/schedules` | 10 | 10 | 117 | 6,782 | 6,782 | 203,805 | 265 | 27 | 0 | yes | `SCHED-132` → 1 | 200 | 400 / 400 | 6749 · yes |
| `/api/suites` | 20 | 20 | 114 | 5,743 | 5,744 | 73,842 | 260 | 13 | 0 | yes | `SUITE-129` → 1 | 200 | 400 / 400 | 5743 · yes |
| `/api/suite-runs` | 20 | 20 | 118 | 9,378 | 9,389 | 118,195 | 260 | 13 | 0 | yes | `qUALIFICATION sUITE 129` → 1 | 200 | 400 / 400 | 9363 · yes |
| `/api/boards` | 20 | 20 | 114 | 2,603 | 2,604 | 32,892 | 260 | 13 | 0 | yes | `BOARD-174` → 1 | 200 | 400 / 400 | 2603 · yes |
| `/api/release-notes` | 20 | 20 | 121 | 4,263 | 4,264 | 54,602 | 260 | 13 | 0 | yes | `2020-w26` → 1 | 200 | 400 / 400 | 4263 · yes |
| `/api/release-notes/versions` | 50 | 17 | 130 | 1,301 | 1,301 | 1,301 | 17 | 1 | 0 | yes | `V1.13.0` → 1 | 200 | 400 / 400 | 1301 · yes |
| `/api/state-repo/files` | 100 | 100 | 124 | 8,146 | 8,417 | 105,922 | 1614 | 17 | 0 | yes | `SCRIPTS/REPOS/ACME/WATCHED-REPO-004/SETUP.SH` → 1 | 200 | 400 / 400 | 8146 · yes |
| `/api/capability-methods` | 10 | 10 | 126 | 4,860 | 9,534 | 186,910 | 361 | 37 | 0 | yes | `HTTPS://GRAIN.DEV/CAPABILITIES/API#OPS/SCOPE-SET-130` → 1 | 200 | 400 / 400 | 4860 · yes |
| `/api/capability-servers` | 10 | 10 | 126 | 6,146 | 6,146 | 185,821 | 12 | 2 | 0 | yes | `HTTPS://GRAIN.DEV/CAPABILITIES/API` → 1 | 200 | 400 / 400 | 6146 · yes |
| `/api/capability-servers/usage` | 20 | 8 | 132 | 1,459 | 1,459 | 61,056 | 8 | 1 | 0 | yes | `HTTPS://GRAIN.DEV/CAPABILITIES/GCP` → 1 | 200 | 400 / 400 | 1459 · yes |
| `/api/capability-servers/gchat-usage?id=https://grain.dev/capabilities/gchat` | 50 | HTTP 400 (gchat not configured) | | | | | | | | | | | | |
| `/api/capability-servers/host-entries` | 50 | 50 | 139 | 1,970 | 1,971 | 5,144 | 265 | 6 | 0 | yes | `HOST-CAP-132` → 1 | 200 | 400 / 400 | 1970 · yes |
| `/api/api-scope-sets` | 10 | 10 | 122 | 5,874 | 5,874 | 92,807 | 279 | 28 | 0 | yes | `OPS/SCOPE-SET-120` → 1 | 200 | 400 / 400 | 5874 · yes |
| `/api/api-scope-sets/usage` | 20 | 20 | 128 | 918 | 918 | 11,735 | 279 | 14 | 0 | yes | `OPS/SCOPE-SET-120` → 1 | 200 | 400 / 400 | 918 · yes |
| `/api/external-agents/tokens` | 20 | 20 | 130 | 7,866 | 7,866 | 106,455 | 263 | 14 | 0 | yes | `EAT-131` → 1 | 200 | 400 / 400 | 7866 · yes |
| `/api/access/principals` | 50 | 50 | 125 | 8,090 | 8,091 | 84,758 | 260 | 6 | 0 | yes | `USER:MEMBER-130@EXAMPLE.COM` → 1 | 200 | 400 / 400 | 8090 · yes |
| `/api/access/history` | 20 | 20 | 122 | 4,083 | 4,084 | 20,102 | 265 | 14 | 0 | n/a | `aUDIT CHANGE 214 ON ACCESS` → 1 | 200 | 400 / 400 | 4083 · n/a |
| `/api/hostds` | 50 | 50 | 114 | 9,059 | 9,110 | 47,160 | 261 | 6 | 0 | yes | `RUNNER-HOST-129` → 1 | 200 | 400 / 400 | 8911 · yes |
| `/api/gcp-keys` | 20 | 20 | 116 | 5,960 | 5,964 | 76,680 | 260 | 13 | 0 | yes | `GCP-CRED-130` → 1 | 200 | 400 / 400 | 5902 · yes |
| `/api/github-tokens` | 20 | 20 | 121 | 4,445 | 4,445 | 59,103 | 261 | 14 | 0 | yes | `GH-CRED-129` → 1 | 200 | 400 / 400 | 4447 · yes |
| `/api/secrets` | 50 | 50 | 115 | 2,880 | 2,880 | 14,065 | 262 | 6 | 0 | yes | `SERVICE-SECRET-129` → 1 | 200 | 400 / 400 | 2873 · yes |
| `/api/agent-models` | 50 | 50 | 120 | 5,619 | 5,620 | 29,213 | 265 | 6 | 0 | yes | `GEMINI-4.132-PRO-HIGH` → 1 | 200 | 400 / 400 | 5619 · yes |
| `/api/persistent-sandboxes` | 50 | 50 | 128 | 7,804 | 7,806 | 40,258 | 260 | 6 | 0 | yes | `DEVBOX-130` → 1 | 200 | 400 / 400 | 7804 · yes |
| `/api/console` | 20 | 0 | 115 | 31 | 31 | 31 | 0 | 1 | 0 | yes | — | 200 | 400 / 400 | 31 · yes |
| `/api/console-sessions` | 5 | 5 | 124 | 8,565 | 8,565 | 172,330 | 261 | 53 | 0 | yes | `SESSION-129` → 1 | 200 | 400 / 400 | 8565 · yes |
| `/api/upgrade/targets` | 50 | 50 | 123 | 8,416 | 8,436 | 21,602 | 267 | 6 | 0 | yes | `RELEASE-TRACK-131` → 1 | 200 | 400 / 400 | 8416 · yes |
| `/api/events` | 20 | 20 | 114 | 5,210 | 5,674 | 26,430 | 2689 | 135 | 0 | n/a | — | 200 | 400 / 400 | 5197 · n/a |

**PostgreSQL (ordering).** 50 of the 55 walked routes returned the identical row order on both drivers. The 5 that differ are all live data, not ordering bugs:

- `audit-logs` (4 new entries) and `events` (live state changes);
- `secrets` (the previous step's `zzz-mid-walk-secret`);
- `github-branches` (the two environments shared one ghsim, so the PostgreSQL seed's cut branches added 260);
- `inbox` (see the next paragraph).

On the PostgreSQL daemon, a full walk of `GET /api/inbox` read 463 rows with 27 duplicates (436 unique), because the inbox grew from 436 to 464 items during the walk. The orchestrator turned the seeded queued tasks' failed runs into inbox items. A second PostgreSQL run reproduced this identically. Three back-to-back unpaged reads afterwards were byte-identical, so the order is deterministic, and every item is unique (464/464). The duplicates come from the offset cursor under inserts (next paragraph). On SQLite the inbox stopped growing before the walk reached it.

**Inserts between pages.**

- Keyset cursors hold: `tasks` walked 550 rows with 0 duplicates while a task was inserted at the head, and `events` walked 2,780 with 0 duplicates while comments were added, the new tail events included.
- Offset cursors do not: `GET /api/templates?limit=5`, then insert a template, then page 2 repeats `tmpl-255` (both drivers).
- Ascending `secrets` picked up a tail insert correctly.

This is inherent to `list_page.go`'s `o:<n>` cursor and is filed as a follow-up (keyset cursors for newest-first offset routes). It is not fixed here because it changes the cursor format of ~45 routes.

**Doc ↔ route drift (both ways).**

- The 47 handlers that call `parseListQuery`/`parsePageBounds` are all in the doc's table.
- The four table rows paged by their own code (`/api/tasks`, `/api/tasks/states`, `/api/states`, `/api/ci/runs`) were walked and are paged as documented.
- No paged route is missing from the doc, and no documented route is unpaged.
- Page keys and default sizes in the table match the routes. Attempt 1 fixed the earlier mismatches (`tokens`, `sessions`, `entries`, `sets`, and the 10/20/50 defaults). This run added D3–D6's notes.

**Seed coverage gaps (not exercised with >250 rows live; covered by Go tests).**

- `ci/faults`, `merge-batches` and `base-branch-gates` are derived from live daemon state and answered 0 rows.
- `capability-servers` (12 rows), `capability-servers/usage` (8) and `gchat-usage` (400: gchat not configured): the seed's `cfg.MCPServers` and `cfg.ExternalAgentAPI` are not in effect once the daemon boots.
- `release-notes/versions` has only the 17 built-in versions.
- `console` lists live PTYs only.

Their paging and caps are covered by `list_page_test.go` and `TestAllHeavyPagedRoutesStayUnderToolResultCap`. A follow-up extends the harness.

## Item 2: the UI is unaffected (Chromium)

[`ui_requests.mjs`](2026-11-10-agent-api-paging/ui_requests.mjs) signs in to the seeded SQLite daemon with the access code as `owner@example.com` (`grain/operator`). It loads 32 pages (`/`, tasks, inbox, CI, templates, schedules, suites, boards, memory, release notes, notifications, console and all 20 Settings pages) in headless Chromium, and records each of the browser's 620 `GET /api/*` calls ([`results/ui-requests.json`](2026-11-10-agent-api-paging/results/ui-requests.json)).

- **No `X-Grain-Agent-List` header on any call.** No such header exists in the code base either (see "Plan drift").
- **No list is cut to an agent page size.** Every list the UI reads arrived whole and untruncated. Examples: access principals 261, scope sets 279, capability methods 361, host entries 265, console sessions 261, git connections 261, hosts 261, memories 261, memory documents 260, persistent sandboxes 260, schedules 265, secrets 263, suites and suite runs 260, templates 262, boards 260, tasks 483.
- **The browser sends `limit` in exactly two places, and neither is new or an agent size:**
  - `GET /api/tasks?state=closed&limit=200&cursor=`: the closed-task loader's own human paging (`ui/src/closedTasks.js`). It is untruncated here (68 closed).
  - `GET /api/ci/runs?limit=200`: the CI pane's bounded poll (`CIPage.jsx`, grain/task-289). It came back `truncated: true` with 200 of 260 runs, by that pane's design. The audit sections' `limit=100` (`AuditSection.jsx`) are human caps too.

## Item 3: real MCP clients

[`mcp_calls.py`](2026-11-10-agent-api-paging/mcp_calls.py) calls `api_deployment_reachable_repos`, `api_list_tasks` (the plan's "`api_tasks`") and `api_events`, with and without parameters, through each MCP surface ([`results/mcp-calls.json`](2026-11-10-agent-api-paging/results/mcp-calls.json)):

| Surface | reachable_repos `{}` | reachable_repos `push,owner,limit=5` | reachable_repos `q` | list_tasks `{}` | list_tasks `fields=summary` | list_tasks `limit=3,q` | events `{}` | events `since=0,limit=5` |
|---|---|---|---|---|---|---|---|---|
| `POST /mcp`, gma_ full reader | 5,212 B | 438 | 708 | 7,257 | 6,128 | 2,266 | 5,209 | 1,233 |
| `POST /mcp`, gma_ `acme/widgets` | 134 | 134 | 99 | 7,057 | 5,925 | 65 | 5,209 | 1,233 |
| `POST /api/mcp` `grain_invoke` as task 9000 | 5,212 | 438 | 708 | 7,257 | 6,128 | 2,266 | 5,209 | 1,233 |
| `POST /api/mcp` `grain_invoke` as task 9001 (`@acme/widgets`) | 134 | 134 | 99 | 7,057 | 5,925 | 65 | 5,209 | 1,233 |
| `POST /api/mcp` `grain_invoke` as operator | 5,212 | 438 | 708 | 7,257 | 6,128 | 2,266 | 5,209 | 1,233 |

All of these are under the ~10 KB budget.

`grain_explore_scope` as task 9000 and 9001 describes the parameters:

- `scope=repos, tool=reachable` names `limit`, `cursor`, `q`, `push`, `owner` and `host` (1,838 B);
- `scope=tasks, tool=list` names `limit`, `cursor`, `q` and `owner` (9,277 B);
- `scope=tasks, tool=events` names `limit` and `since`. Events uses `since` as its cursor, as documented.

A stdio `grain mcp` session exposes the 13-tool workstation roster: `propose_task`, `read_grain_task`, `list_grain_tasks`, `wait_for_task`, `reply_to_task`, `comment_on_task`, `grain_inbox`, `ci_runs`, `ci_log`, `merge_queue`, `fs_read`, `fs_grep` and `adopt_pull_request`. It has no `api_*` tools and no `grain_explore_scope`. **Its list tools do not page:**

- `list_grain_tasks {}`: 20,855 B
- `list_grain_tasks {repo}`: 20,884 B
- `grain_inbox {}`: 20,846 B
- `ci_runs {repo}`: 20,326 B

Each was cut in the middle by pkg/mcp's backstop. This is outside PR #709 (`pkg/cli/mcp_external.go`) and is filed as a follow-up.

A dispatched task with a fake agent was not run. `POST /mcp` refuses a task identity ("not part of the agent API"), and the sandbox's MCP reaches the daemon's controller surface (`POST /api/mcp`) with `X-Grain-Acting-Task`. That surface is what the table above exercised as tasks 9000 and 9001. The follow-up harness task covers a real dispatched run.

## Item 4: scope

The repo-limited callers were task 9001 (`@acme/widgets` grants), the gma_ `repos: [acme/widgets]` credential, and the gma_ per-scope `@acme/widgets` qualifier credential. Each walked every page of `reachable-repos`, `tasks` (all three `fields` modes), `events` and `templates`:

- **No page carried another repository's rows** (`other/thing`, `service-001`, other-repo titles).
- `reachable-repos` answered `total: 1` (only `acme/widgets`) with no connection counts. The unrestricted answer is 305.
- `tasks` answered `total: 262`, the `acme/widgets` count. The deployment has 523. Every caller walked exactly 262 rows, so `total` matches what the credential can read and leaks no count.
- The qualifier credential lacks `templates:read` and got a 403, as it should.
- In the Go tests, `TestRepoLimitedCredentialsNeverLeakReachableReposOrTasksCounts` (now terminating, D2) and the new D1 test pin this down for the cursor walk.

## Plan drift (the plan or the PR text vs the code and docs)

- **`limit` bounds.** The plan and PR #709's message say `limit` is 1..200 and that `limit=201` is a 400. The code (`listPageMax = 1000`) and `docs/agent-api.md` clamp at 1000: `limit=201` is a 200 answer and `limit=1001` is answered as 1000 with `truncated`/`nextCursor`. Negative and non-numeric limits are 400. The doc now says the clamp out loud. Changing to a 400 would break the UI's and CLI's own `limit=200`/`500`/`1000` reads, so it was left as is.
- **`X-Grain-Agent-List` does not exist.** An agent caller is detected by `agentCallFromContext` (task identity or gma_ credential) or `controllerMCPRequestFromContext` (`POST /api/mcp`'s `grain_invoke`). A person sending that header is answered unpaged.
- **Tool names.** The plan's `api_tasks` is `api_list_tasks`. `grain_explore_scope` groups `reachable-repos` under `repos`, not `deployment`.
- **stdio `grain mcp`** has no `api_*` or `grain_explore_scope` tools (see item 3).

## Follow-ups proposed

1. `fix(ui)`: keyset cursors for newest-first offset-paged list routes, so inserts between pages neither duplicate nor skip rows.
2. `fix(cli)`: page `grain mcp`'s `list_grain_tasks`, `grain_inbox` and `ci_runs` to fit the ~10 KB tool budget.
3. `test`: extend the paging harness to the derived collections (CI faults, merge batches, gates, capability servers/usage/gchat) and to a dispatched fake-agent MCP run, and find out why seeded config fields are not in effect at boot.

## Tests run

- `go test ./pkg/ui/ ./pkg/cli/ ./tests/hygiene/... ./pkg/ports/... ./tests/deploy/... -count=1`: green.
- `go vet` on the `//go:build ignore` helpers.
- No edits under `pkg/capability/*`, `pkg/access/*`, `pkg/ci/*` or the other security-review paths. No `ui/` changes, so no screenshots.

## How to re-run

```bash
cd /home/debian/work
(cd ui && npm ci && npm run build) && go build -o /home/debian/bin/grain ./cmd/grain
source docs/test-runs/2026-11-10-agent-api-paging/env.sh
start_paging_env sqlite /var/tmp/grain-paging-sqlite          # or: PAGING_PG_DSN=... start_paging_env postgres ...
W=/var/tmp/grain-paging-sqlite; D=docs/test-runs/2026-11-10-agent-api-paging
python3 $D/walk_routes.py --url http://127.0.0.1:4281 --code "$(cat $W/data/local-api-token)" \
  --tokens-file $W/tokens.json --out /tmp/walk.json --insert-test
python3 $D/mcp_calls.py --url http://127.0.0.1:4281 --code-file $W/data/local-api-token \
  --tokens-file $W/tokens.json --out /tmp/mcp.json
# Item 2 needs the operator on the allowlist first (POST /api/access/principals user:owner@example.com grain/operator).
(cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node ../$D/ui_requests.mjs \
  http://127.0.0.1:4281 "$(cat $W/data/local-api-token)" /tmp/ui.json)
stop_paging_env /var/tmp/grain-paging-sqlite
```
