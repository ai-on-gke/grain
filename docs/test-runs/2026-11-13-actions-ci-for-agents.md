# 2026-11-13 GitHub Actions CI for agents — test run

This run walks [docs/test-plans/2026-11-13.md §7](../test-plans/2026-11-13.md), items 1–5 of
"What is NOT proved". It audits PRs #1302, #1291, #1262, #1319, #1261, #1230, #1348 and #1214
against real `grain daemon -sandbox-port-base=0` processes, over both SQLite and PostgreSQL 15.

[Task #5359](/tasks/5359) did the work in two attempts:
- Attempt 1 built the harness and walked items 1–4. It found and fixed F1.
- Attempt 2 walked item 5, repeated items 1–5 on PostgreSQL, merged `main` and wrote this report.

Everything is under [`2026-11-13-actions-ci-for-agents/`](2026-11-13-actions-ci-for-agents/): the scripts, the SQLite captures in `out/`, and the PostgreSQL captures in `out/postgres/`.

## Results

| § | Item | SQLite | PostgreSQL | Capture |
|---|------|--------|------------|---------|
| 7.1 | In-sandbox `grain ci retrigger`, `--all`, `run-job`, `--jobs` refusals, another task's PR | pass | pass | `items1_2.txt` |
| 7.2 | Host `grain ci retrigger gha:…` prints a `grain ci wait <id>` line that waits | pass | pass | `items1_2.txt` |
| 7.3 | One repair per red `tests.yml` on `main`, no duplicate, green closes it, idle poll costs only 304s | pass | pass | `item3.txt` |
| 7.4 | A runner DNS outage files no PR repair; the failed jobs are re-run and the PR merges | **fixed** (F1), pass | pass | `item4-before-fix.txt`, `item4.txt` |
| 7.5 | Sandboxes cannot write the shared Go build cache; a trusted Grain CI job can, and a later sandbox reads the hit | pass on an image with Go (F2) | pass on an image with Go (F2) | `item5.txt`, `item5-host-sandboxes.txt` |

## Fixes and findings

### F1: in a real daemon the merge queue never re-ran a runner fault (fixed in this PR)

PR #1262 has the merge queue wait out a runner DNS outage: it re-runs the failed jobs instead of sending the task back for a repair (`prinfra.go`). The shared-fault re-runs (`sharedfaultrerun.go`) and release triage (`basebranchrelease.go`) do the same. Each one finds its re-run capability by asserting `github.WorkflowRunRerunner` / `github.ActionsRerunner` on the orchestrator's forge client.

In a real daemon that client is `StateRepoPullRequestClient`, the config-repository wrapper in `pkg/daemon/statepullrequests.go`. The wrapper implemented neither interface, so the assertion always failed. The fallback was the old behaviour: a repair.

The unit tests in `pkg/orchestrator` hand the orchestrator a bare fake forge, so CI never saw this.

**Reproduction** (`item4.sh`, built before `77e0c0d6e`):
1. Two auto-merge tasks open PRs.
2. Task D's `go` and `ui e2e` jobs fail at setup with `Temporary failure resolving 'archive.ubuntu.com'`.
3. Task C's jobs fail on a real test.

Both tasks were sent back with "its checks are failing … the merge queue has sent this task back to an agent". The simulator logged no re-run (`{"rerunFailedJobs":null,…}`, `out/item4-before-fix.txt`).

**Fix** (`77e0c0d6e`): the wrapper now passes `RerunFailedJobs`, `RerunWorkflowRun` and `RerunWorkflowJob` through to the wrapped client. For the native config repository, or a client without re-runs, it refuses with a 404 `github.Error`. It also asserts `github.ActionsRerunner` at compile time.

The regression tests are in `pkg/daemon/statepullrequests_rerun_test.go`. `TestStateRepoPullRequestClientPassesActionsRerunsThrough` covers the wrapper both bare and under `github.WithCaller`. `TestStateRepoPullRequestClientRefusesActionsRerunsOnTheConfigRepo` covers the config-repository refusal, for both a native and an external grain/config. `TestStateRepoPullRequestClientRerunWithoutCapabilityRefuses` covers a wrapped client without re-runs.

After the fix the same walk logs:

```
orchestrator: acme/widgets#9000 at ffc514dd679f failed on the runner rather than the tree (go ("temporary failure resolving"), ui e2e ("temporary failure resolving")): re-ran the failed jobs (automatic re-run 1 of 2), no repair counted
```

- D gets no repair comment. Its re-run goes green and PR #9000 merges.
- C, the control, is still sent back for its real failure (`out/item4.txt`).

`main` has since added a second passthrough of the same kind to this wrapper, for Stage 1 job attempts (`5b936ac39`). This branch merges both.

### F2: the stock sandbox image has no Go, so the shared Go build cache is never written (finding, not fixed)

`docker.io/grainai/sandbox-k8s:latest`, the `-container-sandbox-image` default, has no Go toolchain:

```
$ docker run --rm --entrypoint sh docker.io/grainai/sandbox-k8s:latest -c 'command -v go; id'
uid=1000(debian) gid=1000(debian) groups=1000(debian)
```

`configureTools` only writes GOCACHEPROG where `go` exists, so neither a task sandbox nor a CI job on that image gets one. `ConfigureGoCacheWriter` then prints `skipped`, and `grantGoCacheWrites` revokes the new token without logging anything.

In this walk, a base-branch job on the stock image was never registered as a writer. With Go added to the same image (`sandbox-go.Dockerfile`) it was registered.

This is safe: the isolation from PR #1230 holds either way. But a deployment on the stock image gets no shared Go build cache from PR #1261. A job that installs Go in one of its own steps is too late to become a writer, because the token is granted right after `PrepareSandbox`.

This is a decision about the image, or about when the writer is granted, rather than a scoped bug. It is reported here and not changed.

### F3: host sandboxes cannot walk item 5 (environment limit)

With `-host-sandboxes`, the harness default, two things block the trusted half of item 5:
- A host sandbox is not an `orchestrator.SandboxCommander`, so it gets no GOCACHEPROG at all.
- Grain CI jobs cannot run there: `orchestrator: sandbox "c3eee82-0" asks for 4 vCPU/16384 MiB/100 GiB but a host-directory sandbox has no shape of its own`.

The sandbox half still holds on host sandboxes. Writes from a host sandbox that finds the cache's address on its own are refused with 403 (`out/item5-host-sandboxes.txt`). Item 5 was therefore walked on Docker container sandboxes.

### Smaller observations

- The deployment memory note says `grain ci retrigger`/`run-job` work only with Grain CI. That is out of date: item 1 shows both re-running GitHub Actions jobs from a sandbox, as PR #1302 intended.
- The host CLI has no `grain ci log` (`unknown "grain ci" subcommand "log"`). The in-sandbox CLI does. On the host, `grain ci show -output all <run>` prints a job's output.
- `.grain/workflows/*.yml` with `runs-on: linux` is parsed as an Actions-compatibility file and refused (`runs-on must be "ubuntu-latest"`). Native jobs use `sandbox:` instead. The harness's seed `ci.yml` has this mistake; it does not matter for items 1–4, which run on `ci_mode: github`.
- The repair body for a red `tests.yml` says it closes on goal `nightly-green`, a name it shares with Stage 1 repairs. The behaviour is right: run 7003 going green closed it.

## Environment

- **Build:** `go build -o /home/debian/bin/grain ./cmd/grain` at this branch's `HEAD`:
  - items 1–3 at `6e42153b2`/`43d87e432`;
  - item 4 before and after `77e0c0d6e`;
  - item 5 at `f1a15a3f2`, the merge of `main` at `ee4cf38d7`.
  - `make frontend` was skipped: nothing in §7 renders a UI, and the embedded placeholder pages are enough for the API.
- **Daemon:** `env.sh` starts it with `-sandbox-port-base=0 -as alice@example.com -repo acme/widgets -poll-interval 500ms -max-mergers 0` (item 4: `-max-mergers 1`), over SQLite under `$WORK_DIR/data`.
  - The agent framework is `antigravity`, with `-agy-path` pointing at `fake-agy.py`.
  - The forge is `-github-host 127.0.0.1:4284 -github-insecure-http`.
  - Items 1–4 use `-host-sandboxes`.
  - Item 5 uses `SANDBOX_FLAGS="-container-sandboxes -container-sandbox-image walk/sandbox-go:local -container-git-proxy-host 10.240.0.1 -container-sandbox-network bridge"`.
- **Stubs:** only GitHub. `ghsim.go` serves grain's own GitHub simulator (`pkg/github/githubsim`) over the bare repository and adds the `/_sim/*` control routes below. It can also inject git clone failures.
  - `seed-actions`: workflow runs, jobs and logs;
  - `check-runs`;
  - `mergeable`;
  - `reruns`: a log of every re-run asked;
  - `requests`: status counts per path, for the 304 check.

  Everything else is real: the daemon, the dispatcher, the merge queue, the base-branch watchers, grain CI, the package cache, the git proxy, the sandboxes and the in-sandbox `grain` CLI. `fake-agy.py` stands in for the model only. It replays the MCP tool calls each item scripts (`run_command`, `resume_later`, `comment_on_issue`) through grain's real MCP server.
- **PostgreSQL:** PostgreSQL 15.20 from Debian's `postgresql` package, with one fresh database per script. Passing `-postgres-url postgres://…/walkN` through `SANDBOX_FLAGS` gets the same scripts onto PostgreSQL. The daemon log confirms the store (`acquired PostgreSQL single-writer advisory lock`). The PostgreSQL walks ran at `ae7bd6b0a`.
- **Host:** the Debian sandbox VM with Go at `/usr/local/bin/go` and Docker. Item 5 pulled two images, `docker.io/grainai/sandbox-k8s:latest` and `docker.io/library/golang:1.25`, and built `walk/sandbox-go:local` from them.

## Steps and evidence

### Items 1–2: retrigger from a sandbox and from the host (`items1_2.sh`)

Task A (`acme/widgets#9000`) and task B (`#9001`) each push a commit and open a PR. The simulator seeds Actions run 9101 at A's head, with jobs `go` ✗, `lint` ✗ and `docs` ✓, and run 9201 at B's head. A's next run makes these calls from its sandbox, then parks:

| Call | Result |
|------|--------|
| `grain ci retrigger` | `gha:acme:widgets:9101 (Tests): its failed jobs`, exit 0 |
| `grain ci retrigger --all` | `every job`, exit 0 |
| `grain ci run-job go` | `go`, exit 0 |
| `--jobs go,lint` (the failed set) | accepted |
| `--jobs go,lint,docs` (all) | accepted |
| `--jobs go,docs` | `bad request: cannot re-run go, docs together … one job (grain ci run-job <job>), all failed jobs (grain ci retrigger), or every job (grain ci retrigger --all)` |
| `--jobs nosuch` | `bad request: no job "nosuch" … (their jobs: go, lint, docs)` |
| `--commit <B's head>` | `forbidden: … is not the head of task 2's pull request` |
| `grain api POST /api/ci/runs/gha:acme:widgets:9201/rerun` | `task 2 holds no api grant for scope ci:write` |

The simulator's log shows only A's run was re-run: `{"rerunFailedJobs":[9101,9101],"rerunJobs":[9101000],"rerunRuns":[9101,9101]}`. B was re-run zero times.

A's head was `8952aadcaa08…` in the sandbox before and after, on PR #9000, and in the bare repository. Every message says "No commit was pushed".

From the host, `grain ci retrigger gha:acme:widgets:9101` printed `wait for it with "grain ci wait gha:acme:widgets:9101"`. That command blocked while the re-run was `in_progress` and returned `grain CI passed … (waited 15s): go, lint, docs`, exit 0.

### Item 3: one repair for a red `tests.yml` on `main` (`item3.sh`)

1. **Red run 7001:** run 7001 (`tests.yml`, push, `main`) goes red. Exactly one task is filed: "Fix the red tests.yml on main at a509088e5fec", priority 9, with the failing job, step and log tail in its body.
2. **Red run 7002:** red run 7002 files nothing new. It is appended to the open repair as a comment by `github-base-repair`.
3. **Idle polling:** over the idle window, every `/actions/runs` list read returned 304 (40 of each query, 0 non-304). The workflow-run poll read the default branch (`GET /repos/acme/widgets`) 0 times.
4. **Green run 7003:** green run 7003 closes the repair: `Closed (goal-met): … Tests run 7003 on main (a509088e5fec) is green.`

### Item 4: a runner DNS outage (`item4.sh`)

See F1 for the before and after. After the fix:
- D, the DNS outage, is re-run once (`rerunFailedJobs: [8101]`), with no repair comment and attempts left at 1.
- When the re-run goes green, D's PR merges (`merged=true`).
- C, the real failure, is sent back for its repair as before.

### Item 5: Go build-cache isolation (`item5.sh`)

`acme/widgets` is switched to `ci_mode: grain` with "watch the default branch" on. Only Grain CI jobs are ever registered as writers.

Every writer uses one action ID, `sha256("walk-item5-action")`. Each PUTs its own output and an action entry pointing at it:
- **Task sandbox (no token):** GET returns 404. PUTs return 403 with `grain's Go build cache only takes writes from authenticated CI jobs…`:
  - plain PUTs of the output and the action;
  - a PUT with a forged `job:000…` basic-auth token;
  - a PUT under `/gocache/trusted/action/` with `Grit-Cache-Trust: trusted`.

  A real `go build` in the same sandbox succeeds. The helper's uploads are best effort, and the daemon logs one rate-limited refusal line.
- **Base-branch Grain CI on a push to `main`:** run `a39e2d2e16ddebe8`, event `push`, asked by `base:acme/widgets:main`.
  - The daemon logs `grain ci: ca39e2d-0: acme/widgets's main is a reviewed branch, so this job writes the shared Go build cache`.
  - Inside the job, GOCACHEPROG is `/tmp/grain-gocacheprog http://job:<token>@10.240.0.1:…`.
  - Its PUTs return 200. A forged token is still 403.
  - Its GET serves back `OutputID 544863…`.
- **A later task sandbox:** it reads the CI job's entry (200, `OutputID 544863…`, output "trusted output from the CI job on main"). Its own PUTs are refused again. A GET afterwards still serves the CI job's entry, not the sandbox's `d9d9b6…`.

## Regression runs

All of these ran on this branch after the merge of `main`, and all pass:

| Package | Command | Result |
|---------|---------|--------|
| `pkg/pkgcache` | `go test ./pkg/pkgcache/` | ok (39 s) |
| `pkg/sandboxcli` | `go test ./pkg/sandboxcli/` | ok |
| `pkg/daemon` (F1's test included) | `go test ./pkg/daemon/` | ok (222 s) |
| `pkg/ui` | `go test ./pkg/ui/ -run 'OnGitHubActions\|RunJob'` | ok, 9 tests |
| CLI spec | `go test ./pkg/cli ./pkg/sandboxcli -run CLISpec` | ok, no spec change |
| `pkg/orchestrator` | `go test ./pkg/orchestrator/ -run 'GitHubBase\|BaseRepair\|Infra\|SharedFault\|Stage1Repair\|WorkflowRun\|Rerun'` | ok, 58 tests |
| `tests/security` | `go test ./tests/security -run 'GoCache\|SandboxToHost\|PackageCache'` | ok (one GHCR test skips) |
| `tests/hygiene` | `go test ./tests/hygiene/...` | ok |

No command or flag changed, so the CLI spec needed no regeneration.

## Reproducing

```sh
cd docs/test-runs/2026-11-13-actions-ci-for-agents
go build -o /home/debian/bin/grain ../../../cmd/grain
bash items1_2.sh      # items 1-2
bash item3.sh         # item 3
bash item4.sh         # item 4 (build before 77e0c0d6e for the "before" capture)
docker build -t walk/sandbox-go:local -f sandbox-go.Dockerfile .
SANDBOX_FLAGS="-container-sandboxes -container-sandbox-image walk/sandbox-go:local \
  -container-git-proxy-host <docker bridge gateway> -container-sandbox-network bridge" \
  WORK_DIR=/tmp/walk-item5c bash item5.sh   # item 5
```

To walk on PostgreSQL, add `-postgres-url postgres://<user>:<pw>@127.0.0.1:5432/<fresh db>` to `SANDBOX_FLAGS`. Items 1–4 need `-host-sandboxes` given explicitly there too.

Each script starts its own simulator on 4284 and its own daemon on 4280 under `$WORK_DIR` (default `/tmp/walk-actions`), and stops both when it is done.
