# Walkthrough: acting-for attribution, external agent `owner_source` and the G8 approval rails (2026-11-08 §4)

Plan: [`docs/test-plans/2026-11-08.md` §4](../test-plans/2026-11-08.md). Task 3639, filed by [Task #3495](/tasks/3495). Run 2026-11-08 in a grain sandbox against `main` at `ed863dc6`. Feature PRs: #639, #894, #895, #917, #918, #923, #924, #926.

**Outcome: every item was walked against a real `grain daemon`. Two bugs were found and fixed (F1, F2), with regressions. 34 + 66 + 12 shell checks and 27 browser checks pass on the fixed build.**

## Environment

- **Build.** `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain` from `HEAD`. It was rebuilt after each fix.
- **Daemon.** [`env.sh`](2026-11-08-attribution-external-rails/env.sh) starts a real `grain daemon -sandbox-port-base=0 -disable-native-sandboxes` (control plane only, SQLite store) on `127.0.0.1:4291`. It runs behind `tests/iap/iapsim.go`, a local IAP JWKS issuer and proxy (`:4292`/`:4293`). The daemon is started with `-ui-allowed-principals user:alice@example.com,user:bob@example.com`, which imports both as `grain/operator`.
- **GitHub.** GitHub is played by [`ghsim.go`](2026-11-08-attribution-external-rails/ghsim.go), which serves `pkg/github/githubsim` over HTTP on `:4294` through `-github-host`. It serves `acme/widgets`, whose default branch is **`trunk`** and which also has a `release/v1` branch. A git connection is seeded with `grain connections import`, because a task cannot target a repository without one.
- **Scripted director.** No agent backend can run in a sandbox, so the director's turns are scripted the way the orchestrator records them:
  - an open `task_run` row is the live turn (`Store.StartRun`);
  - `task_observation.delivered_comment_id` is what the turn has been shown (`Store.AdvanceDelivered`);
  - the agent's writes are the calls the daemon's sandbox API proxy forwards: the access code plus `X-Grain-Acting-Task`.

  The daemon's own Agent API gate then derives every attribution from those rows. The script first waits out the daemon's own dispatch of the session. With no agent backend, the daemon suspends that dispatch at once. The script then opens its turn, so `task_run_open_task` holds exactly one live run.
- **Scripted task agent.** `env.sh`'s `propose` speaks MCP to the **real** `grain mcpserver -interactive -server … -task <id>`, the server a run's agent CLI is handed, and calls the real `propose_task` tool. That reaches `ui.Client.CreateTask` with `ProposedBy`, the path a live run's proposals take.
- **Browser.** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), signed in through iapsim's proxy cookie as alice (and as bob for the second tab). The script is [`ui_walkthrough.mjs`](2026-11-08-attribution-external-rails/ui_walkthrough.mjs).

Reproduce: `bash docs/test-runs/2026-11-08-attribution-external-rails/item1_on_behalf_of.sh`, `…/item2_external_rails.sh` and `…/item3_align_default_bases.sh`. Each starts and stops its own environment. `REUSE=1` reuses a running one. For the browser halves, `source env.sh && start_env`, run item 1 with `REUSE=1`, then `cd ui && node ../docs/test-runs/2026-11-08-attribution-external-rails/ui_walkthrough.mjs attribution|race "$PROXY" "$DAEMON" "$CODE" OUT [TARGET CLOSED FILED]`.

## Findings

| # | Finding | Resolution |
|---|---|---|
| **F1** | **An unverified owner still got a `Requested-by:` trailer.** T3 is a `gma_` credential minted with the access code that explicitly names `owner: alice@example.com`. It is stored correctly with `owner_source access-code`, and its filings record `on_behalf_of {human, alice@example.com, access-code}`, which is not `Verified()`. But `handleCreateTask` sets the filing's `Creator` to the token's owner, and `orchestrator.requestedBy` returned `Creator` unconditionally for any agent filer. So every commit for T3's filing would end with `Requested-by: alice@example.com`, for a person nobody verified (the walkthrough's INFO line 1.12 shows `creator = alice@example.com`). | **Fixed** in `pkg/orchestrator/worktree.go`. `requestedBy` gives no requester when an agent filed on behalf of that same person with an unverified source. That is the existing `grain create -author` rule applied to agents. A verified owner (`owner_source iap`, read back as source `token`) keeps the trailer. Regression: two new rows in `TestIdentityTrailersNameOnlyVerifiedPeopleOrTheOperator`. The first row fails without the fix. |
| **F2** | **A live proposal held only for its branch got no explanation.** The `[grain]` note #924 added ("task N, which proposed this, may queue … only work landing on `release/v1` … This task lands on the default branch … instead") was written only by `orchestrator.relayProposedTasks`, which replays proposals at the end of a run. A run's `propose_task` call files live through `ui.Client.CreateTask`, and the relay then skips that proposal as already filed (`TaskWithTag(key)`). So on a real daemon the `release/v1` parent's child was held with an empty body: item 3 check 2.3 failed before the fix. | **Fixed.** The note moved into `model.OtherBranchQueueNote`, with `model.LandingBranch`. `orchestrator` keeps thin wrappers. `CreateTask` now appends the note to a live proposal that `model.AutoSubmitRefusedOnLanding` holds, unless the parent's own session permission queued it or the run asked for `hold_for_approval`, whose own note already says why. Regression: `TestLiveProposalHeldOnlyForItsBranchSaysWhy` (`pkg/ui/live_proposal_test.go`), which fails without the fix. `pkg/orchestrator/finish.go` is a classified path, so this change files the automated security review. |
| O1 | The director's own filings stayed `proposed` on this daemon even with `tasksApproved: true` (`autoApproveProposals` on its task). This happened both for filings made through the agent API and for a no-repo filing. I did not dig further. | Item 1 check 4.7 therefore uses a run nobody approved. A run that grain approved (`AutoSubmitPrincipal`) acting for nobody is covered by `TestADispatchedRunActsForItsApprover`, and in item 3 the trunk child approved by `automation:auto-submit` shows grain's own approval. |
| O2 | The plan's `grain/developer` is not a shipped role set (as in 2026-11-07's O3), and `grain access role` refuses it. | Check 2.2 records the refusal. The walk used `grain/contributor` instead. |
| O3 | `grain login --auth access-code --issue-token --token-stdin URL` **stores the access code** rather than minting a token. The `--token-stdin` branch wins over `--issue-token`, with only the generic access-code warning. `grain -token CODE login --issue-token URL` mints correctly: an unowned `gma_`, check 1.8. | Not changed: `pkg/cli/login.go` is §2's file. This is worth a flag-conflict refusal there. |
| O4 | Live role capping refuses a token outright while it is **wider** than its owner's current role. The 403 says "… is wider than its owner's current role: it grants tasks:admin, which alice@example.com (role grain/contributor) does not hold; re-issue it within grain/contributor". It does not silently narrow the token. | This is the documented design (`docs/agent-api.md`, "A credential never does more than its owner's current role allows"; 2026-11-07 F2). Checks 2.4–2.9 walk it with tokens re-issued within each role. |
| O5 | T1's `on_behalf_of` is recorded with source `token` (`ExternalAgentToken.OwnerPrincipal` of an `iap` `owner_source`), not `iap`. `token` is a verified source. | As designed. Check 1.9 asserts `human:alice@example.com:token`. |
| O6 | `run-now` on P1, a task the credential filed that bob had already approved, is allowed. It approves nothing (`externalApprovalRail`'s `onlyIfUnapproved`), and `approve` on the same task is refused. | As designed. The rail is checked on P1u, an unapproved filing by the same credential. |
| O7 | `holdForApproval` on a person's own `POST /api/tasks` is ignored: it is `propose_task`'s flag ("means nothing on a request that names no ProposedBy"). `proposedBy` is daemon-internal, so an agent-API `POST /api/tasks` is a plain filing, not a proposal. | The walk files P2, P2b and P3 with the scripted agent's real `propose_task`. P3 carries `hold-requested` (check 4.0). |

## Item 1: multi-person standing session and dispatched-run attribution

[`item1_on_behalf_of.sh`](2026-11-08-attribution-external-rails/item1_on_behalf_of.sh), output in [`item1-on-behalf-of.out`](2026-11-08-attribution-external-rails/item1-on-behalf-of.out): **34/34 PASS**, plus one INFO line (O1).

- **Turn 1:**
  - Alice messages the director through IAP, and the scripted turn opens, shown Alice's message.
  - Bob then posts while the turn is live. The turn survives two reconcile ticks (1.3).
  - The turn makes four writes: a comment, a filed task, a close and a configuration write (`POST /api/hostds`, which writes a `config_audit` row).
  - Every write is recorded as `{agent, 1, session}` on behalf of `{human, alice@example.com, iap}`: the `task_comment`, `task` origin and `config_audit` rows (1.5, 1.9, 1.14).
  - The API labels read **"Director for alice@example.com"**. The session's title on this deployment is "Director", so that is how the label reads (1.6, 1.8, 1.11, 1.13).
  - Nothing switched to Bob, though his message was newer.
- **Turn 2**, shown Bob's message (`delivered_comment_id` advanced): every write reads "Director for bob@example.com" (2.1–2.5).
- **Shared access code:**
  - A message posted with the shared access code is stored as `{human, owner@example.com, access-code}` (3.2).
  - With no live run, the session then acts for nobody: the label is plain "Director" (3.3, 3.4).
  - A live turn shown that message acts for nobody too (3.5).
- **Dispatched runs:**
  - A task approved by IAP-verified Alice acts for `alice@example.com (iap)`, on its comment and on the follow-up it files (4.2, 4.3).
  - Tasks approved with the access code, or filed pre-approved with it, act for nobody (4.4, 4.5).
- **Spoofing:**
  - An agent naming `author: alice@example.com` is refused with a 403 (5.1).
  - Claiming `onBehalfOf` in the body or in `X-Grain-On-Behalf-Of`/`X-Grain-User` headers is ignored (5.2).
  - Carrying Alice's real IAP assertion alongside `X-Grain-Acting-Task` keeps the call the agent's, for nobody (5.3).
- **Browser** (`attribution-results.json`, 16/16): `/tasks/<id>` for the comment, the close transition and the filed task's author show "Director for alice@example.com". Each was checked at 1440×900 and 390×844, light and dark, inside the viewport, with no horizontal overflow and no page errors.

## Item 2: `owner_source`, live role capping, `grain access deny`, the access-code 403 and G8

[`item2_external_rails.sh`](2026-11-08-attribution-external-rails/item2_external_rails.sh), output in [`item2-external-rails.out`](2026-11-08-attribution-external-rails/item2-external-rails.out): **66/66 PASS**.

- **Owner sources** (`external_agent_token` rows):

  | Token | Minted by | `owner \| owner_source` |
  |---|---|---|
  | T1 | IAP-verified Alice | `alice@example.com \| iap` (1.2) |
  | T2 | The access code | empty `\| ` (1.4) |
  | T3 | The access code, naming Alice | `alice@example.com \| access-code` (1.6) |

  - A self-service mint under the access code that names an owner is refused: "acts for nobody" (1.7).
  - `grain login --issue-token` under the access code mints an unowned token (1.8).
  - T1's filing is on behalf of Alice, verified (1.9). T2's is on behalf of nobody (1.10). T3's carries only the unverified `access-code` source (1.11), and F1 removes its `Requested-by:`.
- **Live demotion** (no restart), probing file, approve and read with tokens each within one role:
  - Alice moves `grain/operator` → `grain/contributor` → `grain/viewer` → `grain access deny`.
  - T1 (`tasks:admin`) is refused on the very next request after the demotion to contributor. The refusal names the scope and the role (2.4, 2.5).
  - A contributor-sized token files but cannot approve (2.6). It is refused once Alice is a viewer (2.8). A viewer-sized token can only read (2.9).
  - After the deny, every token she owns gets a 403 "… holds no role" (2.10, 2.11).
  - A token owned by never-listed `charlie@example.com` is refused, even filing (2.12). The unowned T2 is not (2.13).
  - Re-allowing Alice restores T1 at once (2.14).
- **`POST /api/auth/access-code` carrying Alice's verified assertion:** the right and wrong codes both return 403 with **byte-identical bodies** (same sha256) and no `Set-Cookie` (3.1–3.3). The body is in [`item2-access-code-403.json`](2026-11-08-attribution-external-rails/item2-access-code-403.json).
- **G8 `externalApprovalRail`** with `t4-admin` (`tasks:admin` and `dispatch:write` on `acme/widgets`, owned by verified Alice). Each lineage and person-held task is tried four ways: REST `approve`, REST `run-now`, and `POST /api/mcp` `grain_invoke` with `api_approve` and `api_run_now`. All are refused with a 403 naming `external:t4-admin` and the reason, and the approval is left unchanged:

  | Task | Why it is held | Reason in the 403 |
  |---|---|---|
  | P1u | t4's own filing | "it filed it" |
  | P2 | Proposed by P1's run | "it filed task P1, which is in the chain…" |
  | P2b | Two hops down | "P1 → P2x" |
  | P3 | `hold_for_approval` proposal from Alice's run | — |
  | P4 | Person-filed, `admission: durable` | — |
  | P5 | Person-filed, `autoMerge` | — |

  Unrelated standard tasks inside the repo scope are approved over REST `approve` and `run-now`, and over MCP `api_approve` (U1–U3).

## Item 3: `AlignDefaultBases` and the two-tab `/session/director` grant race

[`item3_align_default_bases.sh`](2026-11-08-attribution-external-rails/item3_align_default_bases.sh), output in [`item3-align-default-bases.out`](2026-11-08-attribution-external-rails/item3-align-default-bases.out): **12/12 PASS** after F2. 2.3 failed before it.

- **Trunk parent:**
  - The parent was filed with `base: trunk` and `autoMerge: true`. ghsim says `trunk` is the default branch.
  - Its run's `propose_task` named no branch, and the child was queued at once (`automation:auto-submit`), with `auto_merge: true` and stored `base ""` (1.1–1.4).
- **`release/v1` parent:**
  - The child is held `proposed`, without auto-merge (2.1, 2.2).
  - The agent is told it is "not work of the shape your task may queue unreviewed: the same branch your task lands on…" (2.4).
  - Since F2, the child's body ends with grain's note: "may queue the work it splits out without review, but only work landing on `release/v1` in `acme/widgets`. This task lands on the default branch of `acme/widgets` instead…" (2.3).
- **Two tabs** (`race-results.json`, 11/11):
  1. tabA (Alice) and tabB (Bob) open `/session/director`. The director first lacks the shipped `grain#source` grant, so tabB shows the **Adopt shipped defaults** banner.
  2. tabA adds `api:tasks:read` through GrantsPicker.
  3. tabB, never reloaded, clicks Adopt. It sends `{"adoptShippedDefaults":true}` with no list. Afterwards `grain#source` is back **and tabA's `api:tasks:read` is kept** (3.3, 3.4).
  4. tabA's next add, made from a copy older than tabB's Adopt, is refused `409 "stale"`, re-reads and retries once with a 200 (3.5, 3.6). tabB's add then lands, and nothing either tab saved is lost (3.7).
  5. **Double conflict:** Playwright routes tabB's `PUT /api/console-sessions/director` so that a REST write lands just before each one. Both of tabB's PUTs get 409, with no third try. tabB shows "this console session was changed by somebody else after you opened it … reload it to see what they saved, then make your change again". Its grant is not saved, and nothing the other writers saved is lost (3.8–3.10).

The contact sheet ([`contact-sheet.png`](2026-11-08-attribution-external-rails/contact-sheet.png)) has the task pages at both widths and both themes, and the three race states.

## Not proved here

- The director's turns are scripted rows plus the agent's real HTTP calls, not a live agent CLI: no agent backend runs in a grain sandbox. In particular, Bob's message being handed to a running turn as an addendum (the orchestrator's `AdvanceDelivered` mid-run) is modelled as a new turn. `TestASessionTurnActsForTheMessageItIsAnswering` covers the mid-run advance.
- `Requested-by:` was checked at `identityTrailers`, not on a pushed commit, because no run pushes in this environment.
- The end-of-run relay path (`relayProposedTasks`) for the same proposals is `pkg/orchestrator/finish_test.go`'s. The live path is the one walked here.
