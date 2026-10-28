# Git Proxy Walkthrough: Self-Debug Read of grain's Source, Inflate Total, Spooled Push

Plan: `docs/test-plans/2026-10-13.md` §12. Task 3558 (filed by task 3492).

## Summary

| Step | Result |
| --- | --- |
| 1. Self-debug on the default team, a widened team, and a team-scoped team | **Pass, with Finding 1 (fixed).** The default team and the widened team clone `bwsalmon/grain` through the proxy, pinned to the build revision. Every push to grain gets 403 "read-only", `git-push-any-ref` included. The team-scoped holder gets 403 "not in scope" and no `grain-src/`. The team audit logs show each row under the right team. The team-scoped prompt's "unauthorized" section was present but told the run that adding grain to its reads was the fix. |
| 2. Zero-blob pack to a fast-forward-checked ref | **Pass.** 80 × 16 MiB zero blobs (1.25 GiB inflated, 1.3 MB on the wire) are refused with the fast-forward refusal. The proxy's peak RSS is 64 MB, and it decides in about 0.2 s. An ordinary fast-forward lands afterwards. |
| 3. ~300 MiB push at the default spool threshold | **Pass.** A 314 669 753-byte body spooled to disk and landed as a proven fast-forward with a proxy peak RSS of 101 MB. A refused 300 MiB push peaked at 98 MB. No spool file was left in either case. |

## Environment

- **Base:** `origin/candidate` at `d2f5bb8aa3e4de8d81e0058ba701fab3a0a2fb8a` (this branch's base).
  `git branch -r --contains` lists `origin/candidate` (and `origin/main`) for each commit under test:
  `fee51eb3d`, `c7bb60106`, `4ebe6accc`, `70d5b3f98`.
- **Binary:** `go build -o /tmp/grain ./cmd/grain` from a full clone. `grain version -json` gives
  `revision: d2f5bb8aa3e4de8d81e0058ba701fab3a0a2fb8a`, `release: v1.11.0-rc.1.0.20261014004040-d2f5bb8aa3e4`.
- **Host:** grain sandbox VM, 4 vCPU, 7.9 GiB RAM, go1.26.6, git 2.39.5.
- **Server:** `grain demo -addr 127.0.0.1:18080 -open=false -default-target-repo bwsalmon/grain -github-sim -git-proxy`
  (PID 5827). `-git-proxy` (`pkg/demo/demo_gitproxy.go`) serves the real `gitproxy.BuildProxy` over
  the demo's own store, with the team audit sink, the model authorizer and the ref policy. It forwards
  to `-github-sim`'s bare repository through `githubsim.GitUpstream` + `git http-backend`.
  `POST /api/e2e/git-proxy/runs` records a live run and mints its sandbox token, as dispatch does.
  All git traffic is real `git` over HTTP through that proxy.
  - This answers the 10-12 report's note that `grain demo` had no git proxy: it has one now.
- **Peak RSS:** `VmHWM` in `/proc/5827/status`, reset with `echo 5 > /proc/5827/clear_refs` before
  each push. The demo process holds the UI server, the store and the proxy. `git http-backend` runs as
  a separate child process, so it is not counted.
- **Prompt and checkout (step 1):** the demo dispatches no agent, so it builds no prompt. The prompt was
  observed through `tests/e2e`'s `world`: a real `orchestrator.RunDispatch` with a real gitproxy in
  front of bare upstreams, a real SQLite store, and pkg/mcp's exec-backed sandbox tools
  (`TestGrainSourceCheckoutWalkthrough`, extended here). The live checkout pin was observed by running
  `prepareGrainSourceAt`'s script verbatim through the demo's proxy with `build` set to the binary's
  own revision (`/tmp/walk/pin.sh`, below).

## Stubs

| Stub | Stands in for | Where |
| --- | --- | --- |
| `githubsim.GitUpstream` + `git http-backend` over a bare repo | GitHub's smart-HTTP git host for `bwsalmon/grain` | `-github-sim -git-proxy` |
| `git push -f <bare> origin/main:main origin/candidate:candidate` into the sim's bare repo | GitHub having grain's real history | forge seeding, step 1 pin check |
| `.walkseed`: `Store.SyncRepoDeclarations` for `bwsalmon/grain` with template `ff` (team `rel`) and `push: [{ref: refs/heads/release/*}]` | the declaration sync reading `.grain/templates/ff.yml` off grain's default branch, which a demo does not run | steps 2–3 (a fast-forward-checked ref needs a declared push) |
| `POST /api/e2e/git-proxy/runs` | dispatch + the orchestrator minting a sandbox token | every step |
| scripted `antigravity.NewForTest` agent | the model | e2e prompt observation |

No grain code was stubbed: not the proxy, the store, `GitScope`, the ref policy, or the orchestrator.

## Step 1: Self-debug on three teams

Setup through the demo's API (admin mode):

- teams `team-wide` and `team-scoped`, each with target repo `acme/widgets`;
- `PUT /api/settings {"capabilityTeams": {"self-debug": ["team-wide"]}}`, read back as
  `{'self-debug': ['team-wide']}`;
- three tasks with `"repo": "acme/widgets", "capabilities": ["self-debug"]`: 16 (default team),
  17 (`team-wide`), 18 (`team-scoped`). Task 19 is on the default team with `self-debug` and
  `git-push-any-ref`.

The tasks target `acme/widgets`, so `bwsalmon/grain` is reachable only through the grant.

A first attempt filed tasks 13–15 with `"target"` instead of `"repo"`. Those defaulted to
`bwsalmon/grain` as their target, which proves nothing here, so they were discarded.

### Through the real proxy (`/tmp/walk/step1.sh`)

| Task | Clone of `bwsalmon/grain` | Push `probe` / `main` / own branch name |
| --- | --- | --- |
| 16 default | exit 0 | 403 ×3: `remote: bwsalmon/grain is read-only for this sandbox: it may fetch it but not push to it` |
| 17 team-wide | exit 0 | 403 ×3, same text |
| 18 team-scoped | 403 `remote: bwsalmon/grain is not in scope for this sandbox` | 403, same text |
| 19 default + `git-push-any-ref` | exit 0 | 403 ×3, read-only text |

### Pinned checkout (`/tmp/walk/pin.sh`, the orchestrator's script verbatim)

The sim's bare repo was seeded with grain's real `main` (`b10be1f34…`) and `candidate`
(`d2f5bb8aa…`). Fresh tasks 20/21/22 were filed on the default, `team-wide` and `team-scoped` teams.
`build` is `d2f5bb8aa3e4de8d81e0058ba701fab3a0a2fb8a`.

| Task | Result |
| --- | --- |
| 20 default | `grain-src-head: d2f5bb8aa3e4de8d81e0058ba701fab3a0a2fb8a`, which is the build and not `main`. `git push origin` → `'grain-read-only' does not appear to be a git repository`. Pushing to the proxy URL directly → 403 read-only. |
| 21 team-wide | same as 20 |
| 22 team-scoped | `remote: bwsalmon/grain is not in scope for this sandbox`, 403, exit 128 |

### Prompt (real `RunDispatch`, `tests/e2e`)

- Default team (`self-debug alone`) and widened team (new subtest `self-debug on a team Settings
  widened it to`): `grain-src/` checked out, and the prompt says "read-only at `<root>/grain-src`" and
  "pushing is disabled". Both push routes were refused, and no `probe` branch appeared upstream.
- Team-scoped (new subtest `self-debug held team-scoped`): no `grain-src/`. The prompt's unauthorized
  section **before the fix**:

  > There is no `…/grain-src` checkout of grain's own source in this run: grain's scoped Git proxy does
  > not let this sandbox read bwsalmon/grain (bwsalmon/grain is not in scope for this sandbox). The
  > proxy serves only this task's target and the repositories in its reads, so adding bwsalmon/grain to
  > the task's reads is what gives a later run the checkout. Fall back on the deprecated
  > `read_grain_source` and `list_grain_source` tools, …

  **After the fix** (Finding 1):

  > … (bwsalmon/grain is not in scope for this sandbox). This task's self-debug grant is the
  > team-scoped form: the proxy lets self-debug read bwsalmon/grain only for a team the deployment's
  > Settings widen self-debug to, because a clone reaches every branch of it, other teams' task
  > branches included. Fall back on the deprecated `read_grain_source` and `list_grain_source` tools, …

### Team audit logs (`GET /api/teams/<id>/audit-logs`)

- **default:** for each of tasks 16 and 19:
  - `info/refs` and `git-upload-pack`, `forwarded: 200`, credential `grain-team-ZGVmYXVsdA`;
  - three `info/refs` rows `denied: push to read-only repository`, with no credential.
- **team-wide:** task 17 has the same five rows, with credential `default`.
- **team-scoped:** task 18 has two `info/refs` rows `denied: not in scope`.
- Every row carries `taskId`, `runId`, `sandbox` and `resource: bwsalmon/grain`. No team's log shows
  another team's runs.

## Step 2: Zero-filled blobs to a fast-forward-checked ref

Setup:

- Task 23 was filed from template `repo:template:bwsalmon:grain:ff` on team `rel`, and its run was
  started. `refs/heads/release/1.0` is declared without `force`, so any update must be proven a
  fast-forward by walking the pack.
- Client: fetch `release/1.0` (`08cb32c40a25`), then commit 80 files of `"blob N\n"` + 16 MiB of
  zeros on top of it (`7a979a651780`).
- `git -c pack.window=0 push … bomb:refs/heads/release/1.0`. The pack is 1 308 802 bytes, and the
  blobs inflate to 1.25 GiB.

The commit's parent is the ref's old tip, so the push *is* a fast-forward. Only the walk's bound
can refuse it.

Results:

- Refused:
  `remote: refusing to update bwsalmon/grain: refs/heads/release/1.0: could not show 7a979a651780 is a fast-forward of 08cb32c40a25 from the commits this push carries, and its entry does not allow a force-push (`force: true`). This task may push its own branch, refs/heads/grain/task-23, and what `push:` in .grain/templates/ff.yml declares (as read from bwsalmon/grain's default branch at walkseed); a run cannot widen that by editing the file on its own branch -- the change has to merge first`.
  `release/1.0` stayed at `08cb32c40a25`.

  | Run | Wall time | Proxy peak RSS (VmHWM) |
  | --- | --- | --- |
  | 1 | 3 873 ms | 64 076 kB |
  | 2 | 3 856 ms | 63 572 kB |
  | 3 | 3 967 ms | 63 672 kB |

  `git pack-objects` for the same pack alone takes 3 709 ms on the client. The proxy's share of each
  push is therefore about 0.2 s. The baseline RSS before the push was 58 MB.
- Controls on either side of the bound (256 MiB + 8 × pack bytes):
  - 24 × 16 MiB (384 MiB inflated): refused with the same text, 1 278 ms, 63 152 kB.
  - 8 × 16 MiB (128 MiB inflated): **landed** (`f27bf24..085f369`), 1 193 ms, 61 144 kB.
- Ordinary fast-forward (an empty commit on the old tip): `08cb32c..f27bf24 ord -> release/1.0` in
  29 ms. This was run straight after the refused pushes and before the controls.

## Step 3: ~300 MiB push at the default spool threshold (16 MiB)

Ten 30 MiB blobs from `/dev/urandom` (incompressible) were committed on top of `release/1.0`. A
watcher listed `/tmp/grain-gitproxy-push-*` every 0.2 s.

| Push | Body spooled | Result | Wall time | Proxy peak RSS | Spool files after |
| --- | --- | --- | --- | --- | --- |
| fast-forward of `release/1.0` (walked from the spool file) | 314 669 753 B | landed `085f369..5e0fb5e` | 31.9 s | 101 136 kB | 0 |
| `--force` non-fast-forward of `release/1.0`, fresh 300 MiB | 314 669 741 B | refused with the fast-forward refusal; `release/1.0` unchanged | 24.4 s | 98 312 kB | 0 |
| own branch `grain/task-23` (no walk) | ~300 MiB | landed | 30.8 s | 123 900 kB* |  0 |

\* The own-branch push followed a 19 ms push whose `clear_refs` reset left the high-water mark at the
heap retained from the previous push (~101 MB). It is still ~40 % of the body size.

Peak RSS rose by 40–60 MB over a ~60 MB baseline for a 300 MiB body. It does not grow with the push
size: the pack is never held whole (`70d5b3f98`), and the demo's forwarder streams the spool file
(`RealForwarder.ForwardStream`).

## Findings

1. **A team-scoped self-debug run is told that reads are the way to grain's source (fixed).**
   - Since `c7bb60106`, `Store.GitScope` reads `bwsalmon/grain` only for the deployment-wide form of
     the grant. A clone reaches every branch, other teams' task branches included, and the team-scoped
     form is deliberately kept to `read_grain_source`.
   - The prompt's unauthorized section (`grainSourceSection`) still said "the proxy serves only this
     task's target and the repositories in its reads, so adding bwsalmon/grain to the task's reads is
     what gives a later run the checkout". That sentence is wrong about the cause. It also directs the
     run to a way around the boundary `c7bb60106` keeps, by proposing a task with `reads:
     [bwsalmon/grain]`.
   - Nothing was reachable that should not have been: the proxy refused correctly.
   - *Reproduction:* `go test ./pkg/orchestrator -run TestGrainSourceUnauthorizedSelfDebugIsToldItsGrantIsTeamScoped`
     on `d2f5bb8aa` with only the test applied fails with
     `a team-scoped self-debug run was told reads are the way to grain's source`. The e2e
     `TestGrainSourceCheckoutWalkthrough/self-debug_held_team-scoped` fails the same way.
   - *Fix:* for a self-debug run the proxy refused, the section now says that the grant is
     team-scoped, and that only a team Settings widens self-debug to reads grain's source. A run
     without the grant keeps the reads wording. Nothing about what the proxy allows changed.

Not a finding, but noted:

- The plan's step 2 assumes a fast-forward-checked ref exists in a demo. It only exists for a task
  filed from a repository-declared template with a `push:` entry, and `grain demo` seeds none for the
  sim's repository. The seeding used here is listed under Stubs. A future plan could ask for a demo
  flag that declares one.

## Fixes and tests (this change)

- `pkg/orchestrator/run.go` `grainSourceSection`: the team-scoped wording above.
- `pkg/orchestrator/grain_source_internal_test.go`:
  `TestGrainSourceUnauthorizedSelfDebugIsToldItsGrantIsTeamScoped`.
- `tests/e2e/grain_source_checkout_walkthrough_test.go`: two new subtests, each with a real
  `RunDispatch` through a real proxy.
  - `self-debug on a team Settings widened it to`: checkout present, push refused.
  - `self-debug held team-scoped`: no checkout, push refused, the new wording, no "to the task's
    reads".

## Reproduction scripts

- `step1.sh`: for each task id, `POST /api/e2e/git-proxy/runs`, then `git clone` of
  `<proxy>/bwsalmon/grain.git` with `http.extraHeader=Authorization: Basic x:<token>`, then
  `git push origin HEAD:{probe,main,<own branch>}`.
- `pin.sh`: per task, the `prepareGrainSourceAt` script with `build=$(grain version -json | .revision)`
  and the token in the proxy URL.
- `.walkseed/main.go`: `sqlite.Open(sqlite.DefaultConfig(<demo dir>))`, `model.New(db)`,
  `SyncRepoDeclarations(bwsalmon/grain, "walkseed", {Templates: [{Path: ".grain/templates/ff.yml",
  Template: {ID: RepoDeclaredID(template, repo, "ff"), Team: "rel", Target: repo}, Push:
  [{Pattern: "refs/heads/release/*"}]}]})`.
- `newrun.sh`: `POST /api/tasks {"team":"rel","repo":"bwsalmon/grain","templateId":"repo:template:bwsalmon:grain:ff"}`
  and then start its run.

## CI verdict

Grain CI full suite, run `44278e19a80cf3b1` on `e5806a4` (the fix and this report, before this
verdict was written in): **success**. frontend, go (19m26s), installer, lint, package cache,
postgres, relay image, run host, terraform and ui e2e were all green on the first run, with no
rechecks. The GitHub "sandbox image" checks on the PR are red. They are not this repository's gate.
