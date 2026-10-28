# 2026-10-07: the mechanical porter end to end, and the read-only push refusal

Plan: `docs/test-plans/2026-10-06.md` §3. Filed by task 2231, run as task 2381.

## Environment and tested commit

- Base `candidate` at `dafd69778` (Merge pull request #3156). The branch `grain/task-2381` is cut from it.
- `git branch -r --contains` shows each commit in scope on `origin/candidate`:
  - `8c6fd129d`: `pkg/portpick`;
  - `47e6ad845`: a clean port filed as a finished PR;
  - `e698eee10`: `daemon.NewPorter` and the merges split;
  - `b56625fc1`: `--end-of-options`;
  - `e2e1d4aff`: the read-only push refusal.
- Tools: Go 1.26.6, git 2.39.5, Node 20.19.0, Playwright 1.62.1 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, chromium).
- The UI was built with `cd ui && npm ci && npm run build`, so `pkg/ui/static` held the production bundle.

## Commands

```
go test ./tests/e2e/ -run 'TestPortMechanicalWalkthrough$|TestReadOnlyPushIsRefusedWithAReasonGitPrints$|TestPortWalkthroughPairedBranches$' -race -count=3
go test ./pkg/demo/ -count=1
go build -o /tmp/grain ./cmd/grain && /tmp/grain demo -addr 127.0.0.1:0 -open=false -mechanical-port
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node walk.mjs http://127.0.0.1:<port>   # see "UI" below
git log origin/main --since='1 day ago' --no-merges --committer='grain@users.noreply.github.com'
git log origin/main --since='1 day ago' --no-merges --grep='Backported-from:' --format='%ce'
```

## Step 1–2: `tests/e2e/port_mechanical_walkthrough_test.go`

### `TestPortMechanicalWalkthrough`

The test runs real `orchestrator.RunCycle` cycles with a real embedded SQLite store and the real declaration sync, which reads
`port-branches: [main, candidate]` from the repository itself. The porter comes from `daemon.NewPorter` and reaches the
stand-in GitHub only through a real `gitproxy.BuildProxy`. The proxy is wired as `daemon.StartGitProxy` wires it, with
`CiReadScopes`, `CiWriteScopes` and `CiSandboxTeams`, and it uses the default team's credential. The porter is wrapped only to
record each attempt's token and whether its `Release` ran; every call is passed through unchanged.

| Case | What is asserted | Outcome |
|---|---|---|
| Clean one-commit change merged on `main` | The port is tagged `port-mechanical`, approved, `completed`, has **no runs**, and owns PR → `candidate` from its own branch. The ported commit carries `Backported-from: <sha>` and `(cherry picked from commit <sha>)` and is committed as `grain <grain@users.noreply.github.com>`. Grain CI ran on the port branch. The port merges onto `candidate`, and its merge files **no port back** to `main`. | pass |
| Three-commit change: `ALPHA` already on `candidate` by patch id, `BETA` named by a hand port's `Backported-from:` footer | The port is mechanical and `candidate..port` is exactly `feat: gamma`. The body says `cherry-picked 1 commit(s)` and `Left out` for both alpha and beta. After the merge, `candidate:BETA.md` is still the hand port's adapted text. | pass |
| Conflict on `NOTES.md` | The port is not tagged, is `queued` for an agent, and its body names the conflicting commit's full sha, `` `NOTES.md` ``, "stopped on a conflict" and "Nothing was pushed". The port branch does not exist on the remote. The scripted agent then resolves the conflict and the port lands with a run. | pass |
| Port branch already on the remote (stray branches for the next ids) | The port is not tagged. Its body says "could not finish it … already exists … nothing was pushed". The stray branch's sha is **unchanged**, so it was not force-pushed. After the stray is deleted, the agent lands the port. | pass |
| After **every** attempt | `Release` ran. The audit log names one `port-<12 hex>` identity per attempt, reaching only `acme/gadgets`, and every request was forwarded with the team's credential. It shows `git-upload-pack`, and `git-receive-pack` only when a branch was pushed. `CiWriteScopes` and `CiSandboxTeams` no longer hold the identity. A `git ls-remote` with the same token **fails**, and that request is not forwarded. Four attempts used four distinct tokens. | pass (see F1) |
| Merges metrics: `ui.Client.Metrics(section: "merges")`, the call behind `GET /api/metrics?section=merges` | `ports = {mechanical: 2, agent: 2, repaired: 0, mechanicalRate: 0.5}` | pass |

The repaired count is asserted as 0 here. A red mechanical port going through repair is already covered by
`pkg/orchestrator/port_mechanical_test.go` (`TestACleanPortIsMadeWithoutAnAgent`), and `pkg/metrics/merges_test.go` covers
the count. A merge commit *inside* the merged PR is not walked here.

### `TestReadOnlyPushIsRefusedWithAReasonGitPrints` (step 5)

This is a real `git push` through the real proxy. The sandbox is credentialed by `mcp.ConfigureGitCredentials`, as a run's
is, and holds a CI read scope (`daemon.CiReadScopes`). The clone works. The push prints:

```
remote: acme/readonly is read-only for this sandbox: it may fetch it but not push to it
fatal: unable to access 'http://127.0.0.1:38089/acme/readonly.git/': The requested URL returned error: 403
```

The test asserts that the reason (`gitproxy.ReadOnlyPushReason`) appears and that `RPC failed` does not. It also asserts:

- no ref was created and `main` did not move;
- the last audit entry is `denied: push to read-only repository` for that sandbox;
- a fetch afterwards still works, because a 403 does not make git erase the stored credential.

Before this run, only `pkg/gitproxy/readonly_push_test.go` covered the refusal, by calling the handler directly.

## Step 3: UI (Playwright on `grain demo -mechanical-port`)

The demo had no way to show a mechanical port, so this change adds an opt-in `grain demo -mechanical-port` seed
(`pkg/demo/demo_mechanical_port.go`, tested by `demo_mechanical_port_test.go`). It writes the rows `fileMechanicalPort`
leaves behind:

- a change merged on `main` (task 13, PR #140), with the porter's comment;
- its port (task 14), approved by `merge-queue`, tagged `port-mechanical` plus its port tag, linked `port-of`, owning
  PR #141 → `candidate`, `completed`, with no run.

The seed is opt-in, so no other demo spec has to count its tasks. Playwright opened `/tasks/14`, `/tasks/13`, `/inbox` and
`/repos/acme/widgets` and dumped the visible text:

- **Port task pane:**
  - The header reads "acme/widgets#141 · port of 13".
  - The state is "queued for merge", with target `candidate` and auto-merge on.
  - The timeline shows Proposed → Queued → Queued for merge → PR opened.
  - The body says grain made the port itself.
  - There is no Attempts or run section at all, so nothing says "no attempts yet". "Run now" is offered, and its tooltip
    describes what it does: it puts an agent on the PR's branch. That is deliberate (`Actions.jsx` comment).
- **Source task pane:** "acme/widgets#140 · ported by 14", "merged". The porter's comment says 14 ported the change "with no
  agent run". (The first seed stamped the source's `createdAt` after its merge, so the timeline listed "Proposed" last. The
  seed was fixed, and this is not a product bug.)
- **Inbox:** the port is absent, which is correct: it needs nothing from a person while it waits in the merge queue.
- **Repo page:** shows no port or pairing information at all.

Nothing the UI said was false, so no UI fix was made. One gap is recorded as U1 below.

## Step 4: live deployment, read only

This sandbox has no `read_grain_metrics` tool and cannot reach the deployment's API, so the merges section could not be
read directly. The same question was answered from git, read only: a mechanical port's commits are committed by
`grain <grain@users.noreply.github.com>` (`daemon.NewPorter`'s committer), and an agent's are not.

- In the last 24h (to 2026-10-07 ~19:00 UTC), `origin/main` has **0** commits by the grain committer and **67** agent-committed
  commits with `Backported-from:`. `origin/candidate` has **0** and **2**.
- **No mechanical port PR exists to inspect.** The agent-made ports carry `Backported-from:` footers as `portBody` asks.

The porter commits reached `candidate` in the 2026-10-06 cut, and `pkg/daemon/run.go` wires `NewPorter` with the
daemon's proxy URL and token store. So either the deployed build predates that cut, or every live attempt falls back. The
porter logs each case as "(by agent, after a failed cherry-pick)", "(by agent, after a conflicting cherry-pick)" or
"(by agent)". This needs the deployment's own log and metrics, so it is filed as a follow-up (F2).

## Findings

- **F1: the git proxy still authenticates a revoked sandbox token from its cache.** After `Release`,
  `SandboxTokenStore.Revoke` removes the `port-…` token from `sandbox-tokens.json`, but `gitproxy.SandboxTokens.Authenticate`
  answers a cached token without looking at the file. The type's doc comment says this is deliberate: the hot path does no
  I/O, and only a miss re-reads. As a result, a fetch with the released token is logged as
  `{"sandbox":"port-…","outcome":"denied: not in scope"}`, a 403, rather than a 401 for an unknown token. It is refused only
  because the identity's write scope and team were dropped as well.
  - Reproduction: in `TestPortMechanicalWalkthrough`, log the audit lines after `checkAttempt`'s `withToken` fetch.
  - Impact today: none found. Every path that revokes a token also drops what it authorized (port scopes, or the run
    ending). The token stays cached until the next miss re-reads the file or the TTL expires.
  - Not fixed here. The fix trades against a documented hot-path decision in a trust-boundary package, so it is a
    follow-up. The e2e test asserts what matters now: the request fails and is never forwarded.
- **F2: no mechanical ports landed in the last 24h on this deployment** (69 agent ports). This needs live log and metrics
  access to explain. Follow-up proposed.
- **U1 (gap, not false):** the API does not serialize task tags, so the UI cannot tell a mechanical port from an agent
  port. Only the body text and the source's comment say "no agent". Recorded, with no follow-up filed: the pane's facts
  (completed, PR, no run) are all true.

## Stubs (external boundaries only)

- **GitHub:** `githubsim` serves the REST API over a bare repository, and `git http-backend` serves git, in both e2e tests.
  `-mechanical-port` seeds the demo's rows directly, with PR numbers that GitHub never saw.
- **The live model session:** a scripted antigravity agent (`scriptAgent`) runs shell in the agent's checkout. Its port
  recipe is `portBody`'s own `git cherry` command; on a conflict it resolves to the merged change's side.
- **The grain CI executor:** `greenCI`, wrapped to record which refs it was started for, answers every run green. The merge
  queue's reading of the verdict is real.
- **Agent sandboxes** push straight to the stand-in GitHub (`credentialed`), as in `port_walkthrough_test.go`. **The
  porter goes through the real git proxy.**

Nothing of grain's own was mocked: orchestrator, portpick, gitproxy, daemon porter, store, metrics, UI server and React
are all real.

## Fixes and changes in this branch

- `tests/e2e/port_mechanical_walkthrough_test.go`: the two tests above.
- `pkg/demo/demo_mechanical_port.go` and `_test.go`, plus `cmd/grain/demo.go` / `pkg/demo/demo.go`: the opt-in
  `-mechanical-port` seed.
- No product code changed.

## CI verdict

See the closing note on task 2381 for the full-suite `wait_for_checks` result on this branch's final commit.
