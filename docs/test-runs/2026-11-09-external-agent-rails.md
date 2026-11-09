# External-agent credential rails walkthrough — 2026-11-09

Walkthrough of [`docs/test-plans/2026-11-09.md` §7](../test-plans/2026-11-09.md)
("External-agent credential rails"), task 3734. It covers PR #965 (`0fe934e8`,
run-now's implicit approval needs `tasks:admin` on the task's repository), PR #986
(`a511bd43`, `grain login --issue-token --token-stdin` refused), PR #976
(`1d891270`, `grain mcp` exit 0 on stdin close, `rate_limited` 429s), PR #974
(`29f0a828`, no `Requested-by` for an unverified owner) and PR #973 (`e5980afd`,
the merge queue stops re-asking GitHub after an unresolved-thread hand-off).

**Outcome:** the walkthrough found four defects (F1–F4), all fixed on this branch with
Go regressions. Items 1–4 pass on a real daemon after the fixes (117 + 40 + 13 + 5
checks, 0 failures). Item 5 is pinned by a new `pkg/orchestrator` walkthrough test
against the GitHub simulator.

## Environment

- `grain` built from this branch's `HEAD` (`go build -o /home/debian/bin/grain ./cmd/grain`, go1.26.10). The UI bundle wasn't built: no step here renders a page, and the task-page attribution is read from the API fields the page shows (`authorLabel`, `approvedByLabel`).
- [`2026-11-09-external-agent-rails/env.sh`](2026-11-09-external-agent-rails/env.sh) is copied from task 3639's [`2026-11-08-attribution-external-rails/env.sh`](2026-11-08-attribution-external-rails/env.sh) and reuses its `ghsim.go`. It runs a real `grain daemon -sandbox-port-base=0 -disable-native-sandboxes -ci=false` over SQLite on `127.0.0.1:4291`, with:
  - `tests/iap/iapsim.go` as the IAP JWKS issuer (alice@ and bob@ admitted, as `grain/operator`);
  - `pkg/github/githubsim` over HTTP (`acme/widgets` on `trunk`) as GitHub;
  - `-repo acme/other` added for the out-of-scope task in item 1.

  Nothing else is stubbed.
- `sqlite3` and util-linux `script` (for a TTY on stdin) came from `apt-get`.
- In every committed `.out` file, tokens are redacted to `gma_REDACTED` and the access code to `CODE_REDACTED`.

## Item 1 — approve vs run-now, three credential shapes × three tasks × three routes

Harness: [`item1_approval_rails.sh`](2026-11-09-external-agent-rails/item1_approval_rails.sh). Raw output: [`item1-approval-rails.out`](2026-11-09-external-agent-rails/item1-approval-rails.out). Table: [`item1-table.md`](2026-11-09-external-agent-rails/item1-table.md).

The three credentials, all with `tasks:read`:

- **(a)** `dispatch:write` + `tasks:admin`, limited to `acme/widgets`, minted with the access code;
- **(b)** `dispatch:write` + unlimited `tasks:admin`, minted by alice through IAP (owned, `owner_source: iap`);
- **(c)** `dispatch:write` only, minted with the access code.

Every cell uses two fresh proposed tasks filed by the access code (so no lineage rail applies): one approved, one run-now'd. Each pair goes over REST (`POST /api/tasks/{id}/approve|run-now`), `POST /api/mcp` `grain_invoke` (`api_approve`/`api_run_now`) and the compiled `grain approve` / `grain run-now`.

| shape | no-repo task | `acme/widgets` task | `acme/other` task |
|---|---|---|---|
| (a) `tasks:admin@acme/widgets` | refused, both routes | **allowed**, both routes | refused, both routes |
| (b) unlimited `tasks:admin` | allowed | allowed | allowed |
| (c) `dispatch:write` only | refused | refused | refused |

After the fixes, approve and run-now agree in all 27 route × shape × task cells, and every refused run-now and approve leaves its task `proposed`. The refusals name the rule:

- run-now without `tasks:admin`: "run-now would approve it first, which an external agent credential may only do under tasks:admin";
- (a) on a no-repo task: "holds tasks:admin only for acme/widgets, and … does not name a repository it reaches";
- (a) on `acme/other`: "holds tasks:admin, but not on acme/other".

The stdio `grain mcp` server has no approve or run-now tool among its 13 tools, so it has nothing to compare (check `1.mcp.tools`).

**Before the fixes, 8 cells failed**, all on the CLI route. `grain approve` with a gma_ credential was refused in every case with "POST /api/tasks/{id}/approve is destructive (scope tasks:admin): give a reason", even where REST approve succeeded. `grain run-now` on the same unapproved task succeeded and approved it. That is F1 and F2.

## Item 2 — `grain login` refusals

Harness: [`item2_login_refusals.sh`](2026-11-09-external-agent-rails/item2_login_refusals.sh). Raw output: [`item2-login-refusals.out`](2026-11-09-external-agent-rails/item2-login-refusals.out).

A `seeded` context is minted first, so "untouched" is checked against a real config: a `sha256` over every file under `~/.config/grain`, before and after each call.

| case | exit | code |
|---|---|---|
| `--auth access-code --issue-token --token-stdin` (code piped) | 2 | `token_stdin_issue_token`, `fix: grain -token CODE login <url> --issue-token` |
| `--issue-token --token-stdin` (gma_ piped) | 2 | `token_stdin_issue_token` |
| `--token-stdin` with a TTY stdin, nothing typed | 2 | `usage` ("read nothing from stdin") |
| `--issue-token --auth iap` | 2 | `usage` ("no such credential source": this build registers no `iap` source) |
| `--token-stdin` with a non-gma_ secret | 2 | `usage` (pass `--auth access-code`) |
| `--token-stdin`, empty stdin | 2 | `usage` |
| **`--refresh --token-stdin`** | 2 | **`token_stdin_refresh`** (was exit 3 and `--refresh` ignored, F3) |
| **`--scope-set` without `--issue-token`** | 2 | **`needs_issue_token`** (was ignored, F3) |
| **`--repos`/`--expires` without `--issue-token`** (with `-token CODE`) | 2 | **`needs_issue_token`** |
| `--issue-token --expires soon` | 2 | `usage` |
| a URL with no scheme | 2 | `usage` |

The config is byte-identical after every refusal, and identical to the seeded one at the end (check `2.11`). `grain login --help` says `--token-stdin` is "not with --issue-token or --refresh". The documented form `grain -token CODE login <url> --issue-token` signs in as context `doc (external-token)` holding a gma_ credential, and a following `grain --context doc list` exits 0.

`--token-stdin` on a TTY reads one line from the terminal as typed, echo included. This matches `docker login --password-stdin`, so it is noted here, not changed.

## Item 3 — rate limit and `grain mcp` in a client loop

Harness: [`item3_rate_limit_mcp.sh`](2026-11-09-external-agent-rails/item3_rate_limit_mcp.sh). Raw output: [`item3-rate-limit-mcp.out`](2026-11-09-external-agent-rails/item3-rate-limit-mcp.out).

The setup is three `grain login --issue-token` contexts, then:

- **CLI:** 70 `grain --context rl1 list` calls. Each `grain list` makes two requests, so the 31st call is the 61st request, and it fails with exit 1, `"code":"rate_limited"`.
- **MCP:** one stdio `grain --context rl3 mcp` session sends 70 `list_grain_tasks` calls. From call 31 on, they come back as tool errors naming the limit.
- **Per credential:** `rl2` is unaffected after either burst (exit 0).
- **Disconnect:** `grain mcp` exits 0 with nothing on stderr (no `grain: EOF`) when stdin closes after the burst, and also when the client goes away 2 s into a blocking `wait_for_task` call.

**F4:** the daemon answers the 429 with `Retry-After`, but neither the CLI error nor the MCP tool error passed it on, so an agent had no retry hint. Both now end in "; retry in Ns".

## Item 4 — attribution in a commit

Harness: [`item4_attribution.sh`](2026-11-09-external-agent-rails/item4_attribution.sh), with [`trailers/main.go`](2026-11-09-external-agent-rails/trailers/main.go) (`//go:build ignore`). Raw output: [`item4-attribution.out`](2026-11-09-external-agent-rails/item4-attribution.out).

- The access code mints `alice-typed` naming `owner: alice@example.com`, stored with `owner_source: access-code` (unverified). Alice mints `alice-iap` through IAP (`owner_source: iap`). Each files an `acme/widgets` task, and bob approves both through IAP.
- `trailers/main.go` opens the daemon's own store and prints `orchestrator.CommitTrailers`, the trailers a run's commits are written with. A scripted fake agent commits with exactly those trailers to `grain/task-<id>` in the bare repository behind the GitHub simulator, and the script reads them back with `git interpret-trailers --parse`.
  - unverified owner: `Approved-by: bob@example.com`, **no `Requested-by`**;
  - verified owner: `Requested-by: alice@example.com`, `Approved-by: bob@example.com`.
- The task page's attribution matches: `authorLabel` is "external agent alice-typed" for the unverified owner and "external agent alice-iap for alice@example.com" for the verified one.
- The plan expected `Filed-by` on both. `Filed-by` names the filing **task** (`lineageTrailers`), and an external credential has none, so neither commit carries one. That is by design, not a defect. The plan's wording is off.
- **Not done:** the commit is not made by a real agent run. This daemon has no sandbox runtime (`-disable-native-sandboxes`), so the scripted fake agent writes it with the trailers the orchestrator computes from the real stored tasks.

## Item 5 — merge-queue hand-off over many cycles

The queue cycle (sync PRs plus the review-thread relay, as the daemon runs them) needs a task to have been sent back and its run to have finished. That runs over `pkg/github/githubsim` in a Go walkthrough test rather than a live daemon:

[`TestWalkthroughHandOffAsksNothingForTwelveCyclesThenMergesOnceOnResolve`](../../pkg/orchestrator/walkthrough_handoff_cycles_test.go) walks the 2026-11-08 left-open loop for 12 cycles after the hand-off, then the person resolves the last thread:

- **Before the hand-off:** exactly 2 merge calls. One is the refusal that sent the task back, the other is asked once its run finished, and that one is handed to a person. The plan says "exactly the one before hand-off", but #973 asks once more on purpose so the refusal reaches a person.
- **During the 12 cycles:** 0 merge calls, no second send-back, not merged.
- **After the resolve:** the queue picks the task up again by the next refusal poll (`mergeRefusalPollInterval`, 2 m), asks GitHub **exactly once**, and the task merges.
- On the pre-#973 `githubrefusal.go` the test fails with "merge asked for 3 more time(s) over 12 cycles after the hand-off".

## Findings and fixes

| # | Finding | Fix | Regression |
|---|---|---|---|
| F1 | REST / `grain_invoke` run-now on an unapproved task approved it with no `X-Grain-Acting-Reason`. Approve, which is destructive under `tasks:admin`, is refused without one, so the two routes disagreed. | `externalApprovalRail` (`pkg/ui/agent_api_gate.go`) asks run-now's implicit approval for a reason. Run-now of an approved task still needs none. | `pkg/ui`: `TestExternalAgentRunNowApprovalNeedsAReasonAsApproveDoes` |
| F2 | `grain approve` / `grain run-now` couldn't send a reason, so a gma_ credential could never approve from the CLI. | Both take `-reason`, sent as `X-Grain-Acting-Reason` (`ui.HTTPClient.ActingReason`). | `pkg/cli`: `TestApproveAndRunNowSendTheirReason` |
| F3 | `grain login --refresh --token-stdin` ignored `--refresh`, and `--scope-set`/`--repos`/`--expires` without `--issue-token` were ignored. | Usage refusals `token_stdin_refresh` and `needs_issue_token` (exit 2, nothing written). | `pkg/cli`: `TestGrainLoginRefusesFlagsItWouldIgnore` |
| F4 | A 429's `Retry-After` was dropped: no retry hint from the CLI or `grain mcp`. | `ui.HTTPClient` appends "; retry in Ns" to a 429's message. | `pkg/cli`: `TestRateLimitedErrorSaysWhenToRetry` |
| — | Item 5 pinned over 12 cycles plus resolve-and-merge-once. | — | `pkg/orchestrator`: `TestWalkthroughHandOffAsksNothingForTwelveCyclesThenMergesOnceOnResolve` |

`design/spec/cli/host.spec.md` was regenerated, with its prose updated for `-reason` and the new login refusals. `docs/agent-api.md` says run-now's implicit approval needs a reason.

No file under `pkg/access/*` or `pkg/orchestrator/finish.go` changed, so no automated security review is expected from the trigger paths. F1 is still an authorization-rail change in `pkg/ui/agent_api_gate.go`, and a reviewer should read it as one.

## Backport

None of PRs #965/#986/#976/#974/#973 is on `release/v3.2.0` (`cba7d44b`).

- **F1, the security fix, is not present there.** That branch's `Client.RunNow` sends an unapproved task through `agentMayApprove`, which refuses every external credential outright: an `external:<name>` principal "serves no task whose grants could cover approving it". So run-now never approves for an external agent there, with or without a reason. No backport task is proposed.
- **F2–F4 (CLI only, not security) are present there.** `grain approve` has no `-reason`, the login flag handling is the same, and that branch doesn't yet classify 429s as `rate_limited` (#976). Backporting them would mean backporting #976 and #986 first. An operator who wants that can file it.
