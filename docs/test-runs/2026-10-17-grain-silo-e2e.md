# 2026-10-17: Grain node ↔ Silo hub task round trip, end to end

Task 4203. A task goes from a real Silo hub to a single-tenant Grain node,
runs, lands through the Grain's own merge queue, and comes back to the hub.
This is now an automated test in grain CI:
`TestHubTaskRoundTrip` (`tests/e2e/hub_round_trip_test.go`), run by the
`hub e2e` job in `.grain/workflows/tests.yml`. It needs no GCP and takes
about 35 s once silo is built. The `go` job skips it, because
`GRAIN_HUB_E2E` is unset there.

## Setup

| | |
|---|---|
| Hub | `silo hub serve` from bwsalmon/silo `v0.0.0-20261017095756-0399bba412ff` (main at 0399bba), on SQLite. It is built out of the Go module cache (`go mod download` through the module proxy, then `go build ./cmd/silo` in that directory), pinned as `siloHubVersion`. grain's own `go.mod` pin (c6cc978) is older and has no proposal or approve API, so the test does not use it. Humans sign in by `-dev-user-header X-Silo-User`. |
| Team and token | `silo hub team -id widgets -admin user:alice@acme.test -repo acme/widgets -repo acme/outside`; `silo hub token -owner alice@acme.test -node e2e-grain`, written to `-hub-token-file` |
| Grain | Set up by `grain init workstation -sandbox-backend host -owner alice@acme.test`, started from the `daemon.sh` init wrote, plus `-hub-url -hub-token-file -hub-node-id e2e-grain -hub-repos acme/widgets,acme/outside -allow-unisolated-host-sandboxes`. It runs under init's own `grain_policy.yml` (`repos: ["acme/widgets"]`, approvers alice), with one line changed: `allow_external_on_host_sandboxes: true` |
| People | alice (Grain owner, admin of the hub team), bob (outside the hub team; a member of the Grain's `default` team) |
| Stubs | Agent: a custom backend script that appends a marker line to `NOTES.md`, commits and pushes, with no model. Forge: githubsim over a bare repository on local disk, with the test answering GitHub's mergeability field from `git merge-tree`, as in `TestStandaloneTaskImplementsTestsAndLandsAChange`. No hub or Grain API is mocked. |
| Not run | The real GKE/IAP deployment. No hub URL or node token was given to this sandbox (nothing under `/home/debian/`), so that was not done |

## Results

| # | Step | Result |
|---|---|---|
| 1 | Grain registers and shows `online` in `GET /v1/nodes` | **PASS** |
| 2 | A task for `acme/outside` (the hub team's repo, not allowed by the Grain's policy) is refused; nothing is cloned or run | **PASS.** The hub keeps it `queued` with node `e2e-grain` excluded, reason "refused by node e2e-grain: refused by this node's policy". The Grain logs `local policy does not allow repository acme/outside`. The agent never ran |
| 3 | bob's task (non-member, no `approved_by`) is held by the hub as a proposal | **PASS.** It stays `proposed` with no node across several dispatch passes. bob can't approve his own task |
| 4 | alice approves it at the hub, and it flows | **PASS.** Offered to and accepted by `e2e-grain`. The hub task shows `submitter` bob and `approved_by` alice |
| 5 | Claims carried as Grain task metadata | **PASS.** The Grain task's creator is bob, its team is `default`, its `hubTaskId` is the hub ID, and it is approved. The placement note says "submitted by bob@acme.test and approved by alice@acme.test" |
| 6 | Claims as commit trailers | **PASS.** `Requested-by: bob@acme.test` and `Approved-by: alice@acme.test` are on the commits that landed or in the agent's request (the test reports a gap if neither has them) |
| 7 | The hub restarts mid-run (the agent is held after its commit, before its push) | **PASS.** The Grain reconnects and resumes the hub task. Then the agent ran exactly once, there is one attempt, and one Grain task carries the hub task |
| 8 | Events stream to the hub's live feed | **PASS.** `GET /v1/tasks/{id}/events` replays: the placement comment artifact, `status running/attempt 1/started`, a `diff` artifact, `status done/succeeded`, the attempt's `log` artifact, `result exit 0`, then `end` |
| 9 | The view-through serves the diff on demand | **PASS before the merge.** `?ref=diff` returns the change, with the marker line, to alice. It does not return it to bob (outside the team). **After the merge it returns 200 with an empty body** (G4) |
| 10 | The hub's event store holds no file contents | **PASS.** The live feed and every `hub.db*` file are searched for the marker line, and none holds it |
| 11 | The Grain's merge queue lands the change | **PASS after a local Submit** (G2). Grain CI runs the repo's workflow, the merge queue merges, and the marker is on `main` |
| 12 | The hub mirrors the final state | **PASS.** The hub task is `done` |
| 13 | Real GKE/IAP deployment | **NOT RUN**: no URL or token provided |

## Gaps found

Each one that needs work is filed as a follow-up task.

- **G1 (grain).** `grain init workstation -owner X` makes X the Grain's
  owner, but not a member of the `default` team its repository is filed
  under. So the hub's `approved_by: X` is refused as "may not approve
  tasks for team default on this grain", and the hub only sees "refused
  by this node's policy". The test adds alice to the team itself.
- **G2 (grain/silo interface).** A hub task has no auto-merge. It is
  filed without `AutoMerge` and stops at `awaiting_submit`, so it never
  lands unless somebody Submits it on the Grain. Neither the hub
  interface nor grain's local policy has a way to say "hub tasks for
  this repo auto-merge". The test Submits locally.
- **G3 (grain).** A hub offer names no agent framework or model. The
  Grain files it with init's defaults (`claude` / `claude-sonnet-5`).
  With no connection serving that model, it waits with a reason only the
  Grain shows, and the hub just says `running`. The test makes the fake
  backend the deployment's and the team's default.
- **G4 (grain).** After the merge, the view-through of `diff` answers
  200 with an empty body, rather than the change that landed, or a
  status saying the diff is gone.
- **G5 (grain).** A hub task on host sandboxes needs two separate
  opt-ins: `-allow-unisolated-host-sandboxes` on the daemon and
  `allow_external_on_host_sandboxes: true` in the policy. The daemon's
  refusal names the first; nothing names the second until dispatch.
  Already covered by the walkthrough's B1/B5 setup bugs (task 4346); not
  filed again.
- **G6 (silo).** Policy and configuration refusals all reach the hub as
  the same "refused by this node's policy" (seen for G1 and for
  `acme/outside`). This is the walkthrough's B6 and is already filed.
- **Not covered here:** `grain hub login`. The test mints the token with
  `silo hub token`, because the dev-header hub can't sign `grain hub
  login` in (walkthrough B6). silo's `grain-interop` job covers login
  against fakeoidc. Also not covered: a hub team member filing directly
  (no `approved_by`), which the walkthrough found lands `proposed` on
  the Grain (B3).
