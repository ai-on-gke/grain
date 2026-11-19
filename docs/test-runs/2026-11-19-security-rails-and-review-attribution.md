# Walkthrough: Cross-task agent halting rail, in-place review commit attribution, and `trust-boundary/v31` (2026-11-19 plan §7)

**Plan:** `docs/test-plans/2026-11-19.md` §7 (proposed by [Task #7303](/tasks/7303), PR #1889; covers PRs #1398 `224e04b90`, #1809 `d354194c6`, and #1828 `9dd303d71`).
**Task:** [Task #7323](/tasks/7323).
**Commit walked:** `1a449c7d5` (`origin/main` at run start; includes PRs #1398, #1809, #1828, plus subsequent trust-boundary bumps `v32`–`v34` through PR #1909 `b90e2fceb`).
**Helpers:** [`2026-11-19-security-rails-and-review-attribution/`](2026-11-19-security-rails-and-review-attribution/) (`env.sh`, `ghsim.go`, `fake-agy.py`, `walk.sh`, `verify.py`) adapted from [`2026-11-14-review-context/`](2026-11-14-review-context/).
**Transcripts:** [`out/sqlite/summary.txt`](2026-11-19-security-rails-and-review-attribution/out/sqlite/summary.txt), [`out/sqlite/steps.log`](2026-11-19-security-rails-and-review-attribution/out/sqlite/steps.log), [`out/postgres/summary.txt`](2026-11-19-security-rails-and-review-attribution/out/postgres/summary.txt), [`out/postgres/steps.log`](2026-11-19-security-rails-and-review-attribution/out/postgres/steps.log).

---

## Outcome

| # | Area | SQLite | PostgreSQL | Notes |
|---|---|---|---|---|
| 1 | **Cross-task agent halting rail** (PR #1398, all 9 halting routes / 11 request variants across `grain api` sandbox-cli & `grain-controller` MCP) | **Pass** (157/157 checks) | **Pass** (157/157 checks) | All 11 variants return `403` against wider siblings (`api:tasks:admin`, extra write repo `acme/gadgets`) and wider parent (`LinkChildOf`) with zero target mutation. All 11 succeed via `grain-controller` (`grain_invoke`) against equal-grant sibling, proposed child (`LinkProposedBy`), and wider parent → narrower & wider (`LinkChildOf`) children. On `grain api` (`sandbox-cli`), the 3 model-only control actions (`park-until`, `resume-later`, `wait-on {park:true}`) are additionally blocked by `sandboxChannelGuard` across all targets; the other 8 variants succeed on equal-grant/child targets (see Observation 1). |
| 2 | **In-place security review commit attribution** through real `gitproxy` (PR #1809, Cases A, B, C1/C2, D) | **Pass** (16/16 checks) | **Pass** (16/16 checks) | `gitproxy` records `reviewer-push` links only for allowed fast-forwards of borrowed branches; `recordReviewerHead` and `reviewPushedCommits` never credit outside human commits (`H1`, `H2`, `H_after`, `H_fp`), non-fast-forward refusals, remote hook rejections (`R_rej`), or force-pushed-away commits. |
| 3 | **`trust-boundary/v31` live hold triggers** (PR #1828, 6 `v31` paths + 4 `v32`–`v34` paths + 1 negative control) | **Pass** (32/32 checks) | **Pass** (32/32 checks) | Every `v31` path (and every `v32`–`v34` path on `HEAD`) withdrew `autoMerge`, recorded `security-hold: trust-boundary/v34`, and filed a security review citing the expected category (`auth-routes`, `exec-sandbox`, `release-machinery`, `hold-policy`). Negative control (`lib/f2.go`) was not held. |

**Total automated walkthrough assertions:** **205 PASS / 0 FAIL** on SQLite; **205 PASS / 0 FAIL** on PostgreSQL.
**Defects found:** **0** in the walked features. No source-code fixes were needed.

---

## Environment

- **Host / Sandbox:** Debian 12 (linux/amd64), Go `1.26.10`, `jq`, `git`, bubblewrap host-sandbox wrapper.
- **Built binary:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` from `HEAD` (`1a449c7d5`).
- **Databases:**
  - **SQLite:** embedded `modernc.org/sqlite` + `sqlite3` CLI `3.40.1`.
  - **PostgreSQL:** `PostgreSQL 15.20 (Debian 15.20-0+deb12u1)`, fresh database `grain_walk_s7` (`-postgres-url postgres://grain:grain@127.0.0.1:5432/grain_walk_s7?sslmode=disable`).
- **Daemon & GitHub simulator:** real `grain daemon -sandbox-port-base=0 -host-sandboxes -repo acme/widgets -repo acme/gadgets -default-target-repo acme/widgets -poll-interval 300ms -max-workers 4 -max-mergers 1` talking to real `gitproxy` (ephemeral sandbox port) and `ghsim.go` (`pkg/github/githubsim` + `git http-backend` over bare repos `widgets.git` and `gadgets.git`).
- **Agent harness:** `fake-agy.py` speaks both real `grain-sandbox` stdio MCP (`run_command`, `comment_on_issue`, `nothing_needed`) and real `grain-controller` Streamable HTTP MCP (`grain_invoke`, reading `mcpServers["grain-controller"]["serverUrl"]` from `~/.gemini/config/mcp_config.json`), with `_signal_and_wait` file barriers for deterministic mid-run outside pushes.
- **Re-run:**
  ```bash
  STORE=sqlite docs/test-runs/2026-11-19-security-rails-and-review-attribution/walk.sh
  STORE=postgres PG_DSN="postgres://grain:grain@127.0.0.1:5432/grain_walk_s7?sslmode=disable" \
    docs/test-runs/2026-11-19-security-rails-and-review-attribution/walk.sh
  ```

---

## Suite 1: Cross-task agent halting rail (PR #1398)

### Setup
On both SQLite and PostgreSQL, a live sandboxed narrow agent (`ACTING`, task `9`) ran holding only `https://grain.dev/capabilities/api#tasks:write={"repo":"acme/widgets"}`, alongside:
- `WIDER_ADMIN` (task `5`): `api:tasks:write@acme/widgets` + `api:tasks:admin`
- `WIDER_REPO` (task `6`): `api:tasks:write@acme/widgets` + extra write target `acme/gadgets`
- `WIDER_PARENT` (task `4`): `api:tasks:write@acme/widgets` + `api:tasks:admin`, linked as parent of `ACTING` via real `task_link (task_id='9', kind='child-of', target='4', blocks=TRUE)` attached while `ACTING` was held live
- `EQUAL_SIB_CLI` / `EQUAL_SIB_MCP` (tasks `7`, `8`): equal grants (`api:tasks:write@acme/widgets`)
- `CHILD_CLI` / `CHILD_MCP` (tasks `10`, `11`): proposed by `ACTING` (`LinkProposedBy -> 9`)
- `NARROW_CHILD_CLI` / `NARROW_CHILD_MCP` (tasks `12`, `13`): narrower children of `WIDER_PARENT` (`LinkChildOf -> 4`)
- `WIDER_CHILD_MCP` (task `14`): child of `WIDER_PARENT` (`LinkChildOf -> 4`) carrying extra `writes: [acme/gadgets]` that `WIDER_PARENT` lacks (verifying the one-way `childOf` halting exemption)

Every target was exercised across all 9 halting routes (11 request variants, ordered so non-terminal mutations run before `close`):
1. `POST /api/tasks/{id}/depends-on` (`attach: true`) — `api_set_dependency`
2. `POST /api/tasks/{id}/depends-on` (`attach: false`) — `api_set_dependency`
3. `POST /api/tasks/{id}/wait-on` (`park` omitted / false) — `api_wait_on`
4. `POST /api/tasks/{id}/wait-on` (`park: true`) — `api_wait_on`
5. `POST /api/tasks/{id}/park-until` — `api_park_until`
6. `POST /api/tasks/{id}/resume-later` — `api_resume_later`
7. `POST /api/tasks/{id}/defer` — `api_defer`
8. `POST /api/tasks/{id}/withdraw-approval` — `api_withdraw_approval`
9. `POST /api/tasks/{id}/pause` — `api_pause`
10. `POST /api/tasks/{id}/stop` — `api_stop`
11. `POST /api/tasks/{id}/close` — `api_close`

### Results (identical on SQLite and PostgreSQL)

| Caller → Target | `grain api` (`sandbox-cli`) | `grain-controller` (`grain_invoke`) | Target state after calls |
|---|---|---|---|
| `ACTING` → `WIDER_ADMIN` | **11/11 refused (`403`)** (8 by `agentCrossTaskRail` citing `grant https://grain.dev/capabilities/api#tasks:admin`; 3 model-only by `sandboxChannelGuard`) | **11/11 refused (`403`)** (all 11 by `agentCrossTaskRail` citing `grant https://grain.dev/capabilities/api#tasks:admin`) | Untouched (`state=queued`, `approved=true`, `paused=null`, `deferredAt=null`, `depends-on:3` preserved) |
| `ACTING` → `WIDER_REPO` | **11/11 refused (`403`)** (8 citing `writes to acme/gadgets`; 3 model-only) | **11/11 refused (`403`)** (all 11 citing `writes to acme/gadgets`) | Untouched (`state=queued`, `approved=true`, `depends-on:3` preserved) |
| `ACTING` → `WIDER_PARENT` (`LinkChildOf`) | **11/11 refused (`403`)** (8 citing `grant https://grain.dev/capabilities/api#tasks:admin`; 3 model-only) | **11/11 refused (`403`)** (all 11 citing `grant https://grain.dev/capabilities/api#tasks:admin`) | Untouched (`state=queued`, `approved=true`, `depends-on:3` preserved) |
| `ACTING` → `EQUAL_SIB_*` | **8/8 non-model-only `200 OK`**; 3 model-only `403` (`sandboxChannelGuard`) | **11/11 `200 OK`** | Halted & closed as requested |
| `ACTING` → `CHILD_*` (`LinkProposedBy`) | **8/8 non-model-only `200 OK`**; 3 model-only `403` (`sandboxChannelGuard`) | **11/11 `200 OK`** | Halted & closed as requested |
| `WIDER_PARENT` → `NARROW_CHILD_*` (`LinkChildOf`) | **8/8 non-model-only `200 OK`**; 3 model-only `403` (`sandboxChannelGuard`) | **11/11 `200 OK`** | Halted & closed as requested |
| `WIDER_PARENT` → `WIDER_CHILD_MCP` (`LinkChildOf` + extra write repo) | _(MCP channel tested)_ | **11/11 `200 OK`** (one-way `childOf` exemption) | Halted & closed as requested |

---

## Suite 2: In-place security review commit attribution through real `gitproxy` (PR #1809)

Each case started with a live author task pushing `H0` touching `pkg/gitproxy/proxy.go` (`autoMerge: true`), triggering a real security hold and an in-place security review (`LinkPushesReviewedBranch`) whose git pushes traversed the daemon's real `gitproxy`.

### Case A — Outside human commit `H1` while review runs; review pushes nothing
- **Steps:** While `REV_A1` (task `16`) held open at `H0`, an outside human pushed `H1` (touching `pkg/gitproxy/proxy.go`) directly to `widgets.git` and `ghsim` was seeded with a green check-run on `H1`. `REV_A1` then finished clean without pushing.
- **SQLite evidence:** `H0=103cb81401c9`, `H1=ca11fe911096`. `REV_A1` persisted zero `reviewer-push` and zero `reviewer-head` links. PR did **not** merge (`autoMerge=false`, `Merged=false`); daemon filed fresh review `REV_A2` (task `17`) whose `## Earlier reviews of this pull request` read:
  > `- Task 16 (`security-review` at `103cb81401c9`): verdict `clean`; commits pushed: none.`
- **PostgreSQL evidence:** `H0=eda05dddc490`, `H1=9a5a8f2ce7e0`. Identical: zero `reviewer-push`/`reviewer-head`, `autoMerge=false`, `Merged=false`, fresh review `REV_A2` (task `17`) rendered `commits pushed: none` and did not mention `H1` in Earlier reviews.

### Case B — Human pushes `H1` under review's fix `R1`, then `H2` on top of `R1` before review finishes
- **Steps:** While `REV_B1` (task `19`) held open at `H0`, human pushed non-boundary `H1` (`lib/f1.go`). `REV_B1` ran `git fetch origin && git rebase origin/<branch>`, committed non-boundary `R1` (`pkg/widgets/validate.go`, `"review: caseB R1 fix"`), and pushed `R1` through real `gitproxy`. While `REV_B1` remained live, human pushed trust-boundary `H2` (`pkg/gitproxy/proxy.go`) on top of `R1`; `REV_B1` then finished clean.
- **SQLite evidence:** `H0=dc7a21840d1a`, `H1=50def9f0268e`, `R1=7976b4e965e9`, `H2=67fbbfb806ae`.
  - `gitproxy` recorded `reviewer-push:50def9f0268e7b8cb9403808e718ecd92dfd0008..7976b4e965e992ffb7622dc10b0067d710c2e27b`.
  - `recordReviewerHead` claimed `reviewer-head:7976b4e965e992ffb7622dc10b0067d710c2e27b` (`R1`, **not** `H2`).
  - `H2` was re-classified (`autoMerge=false`, fresh review `REV_B2` task `20` filed).
  - `REV_B2`'s `## Earlier reviews of this pull request` listed **only** `R1` (excluding both `H1` and `H2`):
    > `- Task 19 (`security-review` at `dc7a21840d1a`): verdict `clean (pushed fixes)`; commits pushed: `7976b4e965e9` ("review: caseB R1 fix").`
- **PostgreSQL evidence:** `H0=5851a3fd4e02`, `H1=deb9eb107369`, `R1=916d04354f80`, `H2=f5909a406bc7`. Identical: `reviewer-push:deb9eb107369..916d04354f80`, `reviewer-head:916d04354f80`, `H2` re-held, and `REV_B2` listed only `916d04354f80` (`"review: caseB R1 fix"`).

### Case C — Non-fast-forward push refused by `gitproxy` + remote-rejected fast-forward push (`pre-receive` hook)
- **Steps:**
  1. **Non-fast-forward (`C1`):** `REV_C1` (task `22`) checked out `HEAD~1`, committed `R_nonff`, and ran `git push --force origin HEAD:<branch>` through `gitproxy`. `gitproxy` refused (`refusing to update refs/heads/...: it is the branch of the change this task reviews, which this task may only fast-forward, and ... could not be shown to descend from ...`) and recorded **no** `reviewer-push` link.
  2. **Remote hook rejection (`C2`):** An executable `pre-receive` hook (`exit 1`) was installed in `widgets.git`. `REV_C1` committed fast-forward `R_rej` and pushed through `gitproxy`. `gitproxy` validated the fast-forward and recorded `reviewer-push:<H0>..<R_rej>` pre-forward, then `pre-receive` rejected the push so the branch remained at `H0`. After removing the hook, a human pushed `H_after` directly to move the branch off `H0` (forcing `recordReviewerHead` to evaluate the recorded `reviewer-push` link rather than short-circuiting on `head == filedAt`), and `REV_C1` finished clean.
- **SQLite evidence:** `H0=5900dd9e6f20`, `R_nonff=0824d209493b`, `R_rej=cff2899439e8`, `H_after=d6c4185fdd5b`. `REV_C1` retained `reviewer-push:5900dd9e6f20..cff2899439e8`, claimed **no** `reviewer-head`, and `REV_C2` (task `23`) reported `commits pushed: none`.
- **PostgreSQL evidence:** `H0=3af183f8d01d`, `R_nonff=7c9c0c07f728`, `R_rej=5efe85353509`, `H_after=7f183338c6b8`. Identical: non-ff refused with no link, remote-rejected push claimed no `reviewer-head`, `REV_C2` reported `commits pushed: none`.

### Case D — Force-push rewrite removes review's pushed commit `R1`
- **Steps:** `REV_D1` (task `25`) pushed `R1` through `gitproxy` (`reviewer-push:<H0>..<R1>` recorded and landed on branch). While `REV_D1` remained live, an outside human force-pushed a rewritten history `H_fp` cut fresh from `main` (omitting `R1`, touching `pkg/gitproxy/proxy.go`). `REV_D1` then finished clean.
- **SQLite evidence:** `H0=18e68b7838f1`, `R1=ee6970e96b8e`, `H_fp=0b8bc26049d4`. `REV_D1` retained `reviewer-push:18e68b7838f1..ee6970e96b8e`, `recordReviewerHead` claimed **no** `reviewer-head`, `H_fp` filed fresh review `REV_D2` (task `26`), and `REV_D2` rendered `commits pushed: none`.
- **PostgreSQL evidence:** `H0=8c5911c8684a`, `R1=f1f8dc44c423`, `H_fp=40619b78f559`. Identical: no `reviewer-head` claimed, `H_fp` re-held, `REV_D2` rendered `commits pushed: none`.

---

## Suite 3: `trust-boundary/v31` live hold triggers (PR #1828 + `v32`–`v34` on `HEAD`)

Between test-plan generation (`17339dd82`, `trust-boundary/v31`) and the walked `HEAD` (`1a449c7d5`), PRs #1896 (`v32`), #1901 (`v33`), and #1909 (`v34`) advanced `trustboundary.Version` to `"trust-boundary/v34"` while retaining every `v31` entry. Single-file simulated PRs (`autoMerge: true`) were opened one at a time for all six `v31` surfaces, all four `v32`–`v34` surfaces, and one non-boundary negative control:

| Added in | Touched path (only file in PR) | Expected category | `security-hold` link (SQLite & PG) | `human-only-hold` | `autoMerge` after finish | Review task cited category & path |
|---|---|---|---|---|---|---|
| **v31** (#1828) | `pkg/ui/accept_findings.go` | `auth-routes` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `28`) |
| **v31** (#1828) | `pkg/model/review_findings_accept.go` | `auth-routes` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `30`) |
| **v31** (#1828) | `pkg/ui/verifier_session.go` | `auth-routes` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `32`) |
| **v31** (#1828) | `pkg/agentd/update.go` | `exec-sandbox` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `34`) |
| **v31** (#1828) | `cmd/grain-agentd/main.go` | `exec-sandbox` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `36`) |
| **v31** (#1828) | `scripts/build-agentd.sh` | `release-machinery` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `38`) |
| v32 (#1896) | `pkg/model/merge_gates.go` | `hold-policy` | `trust-boundary/v34` | `trust-boundary/v34` | `false` | **Pass** (task `40`) |
| v33 (#1901) | `pkg/ui/agent_cross_task.go` | `auth-routes` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `42`) |
| v33 (#1901) | `pkg/ui/notifications.go` | `auth-routes` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `44`) |
| v34 (#1909) | `pkg/ui/refs.go` | `auth-routes` | `trust-boundary/v34` | _(none)_ | `false` | **Pass** (task `46`) |
| Negative control | `lib/f2.go` | _(none)_ | _(none)_ | _(none)_ | `true` | **Pass** (no hold, no review) |

---

## Observations (non-blocking)

1. **`sandbox-cli` vs `grain-controller` channel split on 3 halting routes:** From inside a sandbox, `grain api` sends `X-Grain-Channel: sandbox-cli`, where `sandboxChannelGuard` (`pkg/ui/agent_api_gate.go:676-688`) unconditionally refuses `POST /api/tasks/{id}/park-until`, `POST /api/tasks/{id}/resume-later`, and `POST /api/tasks/{id}/wait-on` with `park: true` as model-only control actions (`403`) — even against equal-grant siblings and proposed children. To exercise both the negative (`403` from `agentCrossTaskRail`) and positive (`200`) arms of all nine halting routes on a live sandbox run, Suite 1 exercises both `grain api` (`sandbox-cli`) and `grain-controller` MCP (`grain_invoke`, model channel).
2. **`trustboundary.Version` at `HEAD`:** `HEAD` (`1a449c7d5`) is on `trust-boundary/v34` (superseding `v31` via `v32`–`v34`); all six `v31` paths remain classified under their exact expected categories (`auth-routes`, `exec-sandbox`, `release-machinery`).
3. **Limited-role parity note (`pkg/ui/access_roles.go`):** While inspecting the halting rail, we noted that `roleStandingTargetEscalation` (`roleTaskWithin`, `pkg/ui/access_roles.go:1064-1102`) covers steering routes (`PATCH`, `retry`, `reopen`, `undefer`, `resume`, `comments`, `reply`, `qa/bypass-s3`) for limited human roles (`grain/contributor`, etc.) but not the nine halting routes. This is outside the scope of PR #1398 (which specifically covers in-grain agents via `agentCrossTaskRail`) and is recorded here as an informational observation rather than a defect in §7.

---

## Go Test Verification (`GRAIN_REQUIRE_POSTGRES=1`)

All affected Go test suites passed against PostgreSQL 15.20 and SQLite:
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/ui -run 'TestAgentAPI|TestSandboxCLIChannel'` — **PASS**
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/gitproxy -run 'Test.*Borrowed|Test.*Reviewed|Test.*FastForward'` — **PASS**
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/orchestrator -run 'TestSecurityReview|TestSecurityHold|TestSupersededInPlace|TestReview.*Push|TestEarlierReviews'` — **PASS**
- `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/trustboundary/...` — **PASS**
- `GRAIN_REQUIRE_POSTGRES=1 go test ./tests/security -run 'TestAgentAPITaskWrite|TestRouteAuthorization'` — **PASS**
- `GRAIN_REQUIRE_POSTGRES=1 go test ./tests/hygiene/...` — **PASS**
