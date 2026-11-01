# 2026-11-01: The sandbox `grain` CLI and namespace (test plan §8, and the 2026-10-30 plan's §5)

Task 2223 (`grain/049f1e09/task-2223`), walking §8 of
[`docs/test-plans/2026-11-01.md`](../test-plans/2026-11-01.md) ("The sandbox
`grain` CLI and namespace") against the live deployment and a local demo,
and the still-unreported §5 of
[`docs/test-plans/2026-10-30.md`](../test-plans/2026-10-30.md) ("The sandbox
`grain` CLI everywhere"), **which this report covers** (see
[§5 of the previous plan](#previous-plan-5-the-sandbox-grain-cli-everywhere)).
Scope: PRs #209, #284, #262, #204, #292, #305, #255, #254, #296, #201, #257,
#259, #214, #244, #283, #250. Origin: task 2084.

## Environment

- **This task's own sandbox, on the live deployment.** Debian 12 (bookworm),
  `granule` bridge network: the default route is `192.168.9.1`, and the sandbox
  proxy is `192.168.9.1:9728`. The run holds only `corp-research`: no
  `self-debug`, no `api:tasks:read`, no `grain-ci`, no `grain-log`.
  - `~/.git-credentials` was already in git's rewritten form at the start of
    the run: `http://sandbox:<token>@192.168.9.1%3a9728`.
  - The image's `/usr/local/bin/grain` has sha256 `4bba4dc18e57…`. There is no
    `~/.grain/bin/grain`, so the daemon placed nothing over it. `PATH` is
    `/home/debian/.grain/bin:/usr/local/bin:…`.
- **Tree.** `main` at `ccc64703`, plus this branch's two fixes. Go 1.26.10.
  - `go build -o ~/bin/grain-sandbox ./cmd/grain-sandbox` (12 MB) and
    `go build -o ~/bin/grain ./cmd/grain`.
  - Rebuilt after each fix.
- **Demo, the namespace and CLI half.** `grain demo -github-sim -fake-ci
  -git-proxy [-stalled-tasks] -open=false -addr 127.0.0.1:7809`.
  - This is grain's real git proxy (`gitproxy.BuildProxy` over the demo store)
    on loopback.
  - `POST /api/e2e/git-proxy/runs {"taskId":…}` starts a run and mints its
    sandbox token, as dispatch does.
  - Each "sandbox" is a throwaway `HOME` whose `.git-credentials` holds the
    minted token in the `%3a`-escaped form git writes
    (`http://sandbox:<token>@127.0.0.1%3a<port>`). `GRAIN_SANDBOX_PROXY` is
    unset. The CLI is the tree's `grain-sandbox`.
- **Docker 29.9.2, the image and placement half.**
  - Four locally built images: `local/sbx:v1` (contract label 1, an old
    host-script-only `grain`), `local/sbx:v2old` (contract 2, the same old
    `grain`), `local/sbx:v2` (contract 2, the tree's `grain-sandbox`) and
    `local/sbx:v2live` (contract 2, the live image's `grain`, `4bba4dc1`).
  - `grain sandbox-image check -probe` ran against all four.
  - `placeSandboxGrain` ran through a real `containerSandbox`
    (`mcp.ContainerExecRunner`, i.e. `docker exec`) from a throwaway internal
    test. That test is not committed.
- **Stale pin.** `grain demo` built with
  `-ldflags "-X github.com/ai-on-gke/grain/pkg/version.Release=v0.50.0"`, so
  the stamp names a build (`…/guest:v0.50.0`). A dev build is stamped
  `:latest`, which by design names no build and never warns.
- **Not used:** a `grain daemon -sandbox-port-base=0` dispatching agent runs
  into a Docker pool.
  - The demo's `-git-proxy` serves the same proxy handler and the same scope
    resolution (`model.Store.SandboxNamespaceScope`) a daemon does. It mints
    the tokens a dispatch would.
  - So every proxy and namespace answer below comes from the code a daemon
    runs. Only the agent turn itself is absent.

## Outcome

| # | Item | Result |
|---|---|---|
| 1 | Proxy discovery with no `GRAIN_SANDBOX_PROXY`, bridge gateway, `%3a` credential | **Live image: FAIL. Workaround still needed. Tree: PASS.** The live image's `grain` (`4bba4dc1`) refuses every proxied command with `no proxy credential in ~/.git-credentials`. The tree's `grain-sandbox`, run in the same sandbox on the same credential, reaches the proxy for `task`, `ci`, `pr`, `status`, `metrics`, `log` and `ls`. |
| 2 | `--task` / `--ref`: same repo, a `reads:` repo, outside both | **PASS** |
| 3 | `task://` access matrix | **Tree (demo): PASS. Live: FAIL, a security finding.** The live daemon serves every `ai-on-gke/grain` task's record, prompt and conversation to a run holding no task grant. That is the pre-#284 rule. |
| 3a | `grain task list` with an `api:tasks:read@<repo>` grant | **FAIL → fixed here** (F1) |
| 4 | `grain task list` over 1133 tasks | **PASS** |
| 5 | `ls --glob` / `ci://` truncation notices; `src://playbooks`; grep pruning of nested `attempts/` | **PASS / PASS / covered by CI only.** The live daemon predates #296, and the demo's minted attempts are empty. |
| 6 | Host-qualified repo (`git.forge.example/acme/widgets`) | **PASS** for namespace paths, CI/PR, `task list`, `task://` and the agent API. **UI links not exercised.** |
| 7 | Stale-pin warning; v1 compliance refusal | **PASS** |
| 8 | Previous plan §5 | **PASS, plus one fix** (F2) |

### Fixes on this branch

- **F1 — `fix(gitproxy): list repo-granted siblings in grain task list`.**
  - `/cli/tasks` admitted a non-`MayReadAllTasks` scope's tasks by
    `PermitsTask` (its own ID) only.
  - So a run holding `api:tasks:read@acme/gadgets` could `grain read
    task://28/record.md` but `grain task list` showed one line, its own task.
  - It now admits what `task://` serves: `PermitsTask(id) ||
    PermitsTaskRepo(repo)`.
  - Regression test: `TestSandboxTaskListFiltersAndBounds` gains a
    `tok-repo` scope. Without the fix it fails with
    `1 lines (want 41)`.
  - This makes the list *consistent* with `task://`, not wider. The run could
    already read every one of those records.
- **F2 — `fix(sandboxcli): let grain log --since/--until take a span like
  10m`.**
  - `grain log --since 10m` was refused with
    `since "10m" is not an RFC3339 timestamp`, while `grain ci list --since`
    and `grain task list --changed-since` both take spans.
  - The `read_grain_log` tool stays RFC 3339 only, as
    `TestReadGrainLogTakesRFC3339TimesOnly` pins.
  - The CLI resolves what `ci.ParseSince` reads to the instant it names and
    passes anything else through. Regression cases are in
    `TestSandboxLogAndMetricsFlagsBecomeTheirQuery`.

### Findings not fixed here

- **S1 (security, live deployment): `task://` beyond scope.** The live daemon
  serves sibling tasks to a run with no task grant.
  - Reproduction from this task's own sandbox, which holds `corp-research`
    only, using either CLI:

    ```
    $ grain read task://2222/prompt.md | head -1
    Your previous run was suspended (usage-limit, …
    $ grain read task://2210/record.md | head -1
    Task 2210: Security review: Add part of task name to branch names
    $ grain read task://2100/record.md
    grain: forbidden: task://2100/record.md: that tool belongs to the "self-debug" capability, …
    ```

  - A sweep of task IDs 2100–2222 read every `ai-on-gke/grain` task and was
    refused every task in another repository. Security-review prompts were
    among those read.
  - This is #209's "tasks in the caller's repositories" rule before #284
    (`c2e4b7d5`) narrowed it. The refusal wording also predates the current
    `refuseTaskRead` message.
  - The tree is right. The same reads from the same grant set in the demo are
    refused (step 3).
  - The live daemon is the Google-layer build, and it has not picked up core
    at or past `c2e4b7d5`. Filed as a held follow-up against
    `depot.code.corp.goog/experimental/grain` (see the closing note), beside
    upgrade #2188.
- **N1: contract v2 cannot catch a stale CLI.** The live image's `grain`
  (`4bba4dc1`) **passes** the v2 compliance probe
  (`sandbox-contract verify ok: grain=v2`): its usage lists every command, so
  the probe is satisfied.
  - Yet that same binary lacks the bridge-gateway and `%3a` discovery from
    #292 and #305.
  - So the contract cannot tell an operator that this image needs the
    workaround. Only the image upgrade (or the daemon placing its own build at
    `~/.grain/bin/grain`) retires it.
  - Recorded, not filed: a contract bump for every CLI behaviour change would
    be the wrong tool, and #2188 is the fix.
- **N2 (minor, not filed): wording and classification.**
  - `grain log --min-priority 3` is refused. Only journald names are taken,
    not numbers.
  - Invalid `grain log` filters are classified `not found` (the
    `deploy://grain.log` read's class) rather than `bad request`.
  - `grain ci log <run>` on a run with no jobs prints nothing and exits 0.

## Steps and observations

### 1. Proxy discovery on the live bridge-gateway sandbox

```
$ unset GRAIN_SANDBOX_PROXY; sed 's/:[^:@]*@/:REDACTED@/' ~/.git-credentials
http://sandbox:REDACTED@192.168.9.1%3a9728
```

The live image's `grain`, unchanged since the run started:

| Command | Exit | Output |
|---|---|---|
| `grain task list --limit 3` | 2 | `grain: "task" answers over a sandbox's scoped proxy, and this process found none (no proxy credential in ~/.git-credentials, and GRAIN_SANDBOX_PROXY is unset)` |
| `grain ci list` / `pr status` / `pr base-status` / `metrics --window 24h` / `log --max-lines 3` / `status …` / `ls task://2223` / `ls ci://ai-on-gke/grain` | 2 | the same, naming each command |

With the workaround
(`export GRAIN_SANDBOX_PROXY="$(sed 's/%3[aA]/:/' ~/.git-credentials | head -1)"`),
the same binary answers: `grain task list` prints this task's row, and
`grain ci list` returns the server's grant refusal.

The tree's `grain-sandbox`, same sandbox, same credential, nothing exported:

| Command | Exit | Output |
|---|---|---|
| `grain task list --limit 3` | 0 | `2223  running  ai-on-gke/grain  Walkthrough: …` / `1 task(s) shown of 1 matching, oldest first.` |
| `grain status walkthrough-§8-…` | 0 | `Updated status: walkthrough-§8-proxy-discovery-with-tree-CLI` |
| `grain ls task://2223` | 0 | `attempts/ conversation.md handover.md prompt.md record.md summary.md` |
| `grain ci list`, `pr status`, `pr base-status`, `ls ci://ai-on-gke/grain` | 1 | `grain: forbidden: grain ci: that tool belongs to the "grain-ci" capability, and this run does not hold it …` |
| `grain metrics --window 24h` | 1 | `grain: forbidden: deploy://metrics: that tool belongs to the "self-debug" capability …` |
| `grain log --max-lines 3` | 1 | `grain: forbidden: deploy://grain.log: that tool belongs to the "grain-log" capability …` |

Every refusal in the second table is the server's answer for a grant this run
does not hold, so each of those commands found and authenticated to the proxy.
The demo runs every proxied command over a `127.0.0.1%3a<port>` credential with
the variable unset (steps 2–5 and §5 below).

**Verdict:** the `export GRAIN_SANDBOX_PROXY=…` workaround **is still needed**
on the deployment's current sandbox image (`/usr/local/bin/grain` sha256
`4bba4dc18e57…`, which the daemon does not override with a placed
`~/.grain/bin/grain`). It can be retired once #2188 ships an image, or a
daemon, carrying core at or past `605cd798` (#292) and `7a71f535` (#305).

### 2. `--task` / `--ref`

Demo tasks:

- 30: `acme/widgets`, implied `grain-ci`.
- 31: `acme/widgets`, `reads: [acme/gadgets]`.
- 32: `acme/gadgets`.
- 33: `acme/other`.

All three repos have CI Mode "Use Grain CI". `grain-ci` is refused when
attached by hand (`grain-ci cannot be attached by hand: … Set the CI Mode
instead`). Each task started a fake CI run with `grain ci retrigger --all`,
and `fake-ci-3` (task 32) was finished as `failure`.

| From | Command | Result |
|---|---|---|
| 30 | `ci list --task 31` | `1 run(s) of task 31 on grain/task-31 in acme/widgets` / `fake-ci-2 … running` |
| 30 | `pr status --task 31` | `grain/task-31 (task 31's branch) in acme/widgets -- latest grain CI run: fake-ci-2` |
| 30 | `ci list --ref grain/task-31`, `pr status --ref grain/task-31` | the same run |
| 30 | `ci list --task 32`, `pr status --task 32`, `ci list --task 33` | `not found: task 32 is not a task in a repository this sandbox may read CI for` |
| 30 | `pr status --ref grain/task-32 --repo acme/gadgets` | `forbidden: acme/gadgets is outside this sandbox's CI read scope` |
| 30 | `ci log fake-ci-3` | `not found: no CI run fake-ci-3 in a repository this sandbox may read` |
| 31 (reads gadgets) | `ci list --task 32`, `pr status --task 32`, `pr status --ref grain/task-32 --repo acme/gadgets` | `fake-ci-3 grain/task-32  failure` |
| 31 | `ci log fake-ci-3 [--repo acme/gadgets]` | exit 0, no output (fake run with no jobs; N2) |
| 31 | `ci list --task 33`, `pr status --task 33` | `not found: task 33 is not a task in a repository this sandbox may read CI for` |
| 31 | `pr status --ref grain/task-33 --repo acme/other`, `ci log fake-ci-4 --repo acme/other` | `forbidden: … acme/other is outside this sandbox's CI read scope` |
| 31 | `ci list --task 99999` | the same `not found` as for 33, so a missing task and an out-of-scope one cannot be told apart |

`grain ci artifact --task` was not exercised: fake runs carry no artifacts.

### 3. `task://` access matrix (demo, tree build)

Tasks:

- 24: `acme/widgets`, no grant.
- 25: `api:tasks:read`.
- 26: `self-debug`.
- 27: `reads: [acme/gadgets]`.
- 29: `api:tasks:read@acme/gadgets`.
- 28: an `acme/gadgets` task, and 1 a seeded `acme/widgets` task.

| From | `ls task://` | `ls task://<own>` | `read task://1/record.md` | `read task://28/record.md` | `ls task://99999` |
|---|---|---|---|---|---|
| 24 no grant | `24/` | listed | forbidden | forbidden | forbidden |
| 25 `api:tasks:read` | every ID | listed | `Task 1: Add a dark mode toggle` | `Task 28: G-other-repo` | `not found: no task 99999` |
| 26 `self-debug` | every ID | listed | read | read | `not found` |
| 27 `reads: acme/gadgets` | `27/` | listed | forbidden | forbidden | forbidden |
| 29 `api:tasks:read@acme/gadgets` | `29/` | listed | forbidden | `Task 28: G-other-repo` | forbidden |

Every `forbidden` reads `another task's record needs the "self-debug"
capability or an "api:tasks:read@<owner/repo>" grant naming its repository,
and this run holds neither.` A missing task is refused in the same words, so
the refusal leaks nothing.

The bare `api:tasks:read` row reads *every* task, other repositories
included, not just same-repo siblings. That matches `pkg/ui/agent_api_gate.go`
(a bare grant holds `api:tasks:read@` every repo) and the `serveTasks` doc
("a scope that may read every task (self-debug, api:tasks:read)"). The plan's
"(siblings)" undersells it.

`ls task://` at the root lists only a repo-scoped run's own ID (row 29). That
is left as it is: the readable siblings now come from `grain task list` (F1).

**Before F1**, task 29's `grain task list` showed `29  running  acme/widgets
M-tasksread-gadgets` and nothing else, though it reads `task://28`. A
host-qualified pair showed the same: task 1134, holding
`api:tasks:read@git.forge.example/acme/widgets`, read `task://1135` but listed
only itself.

**After F1**, on a rebuilt demo (`/var/tmp/wt2`):

```
=== task 22 (api:tasks:read@acme/gadgets): grain task list
20  awaiting_reply  acme/gadgets  gadgets A
21  awaiting_reply  acme/gadgets  gadgets B
22  running  acme/widgets  widgets grant@gadgets
3 task(s) shown of 3 matching, oldest first.
=== task 23 (no grant): grain task list
23  running  acme/widgets  widgets nogrant
1 task(s) shown of 1 matching, oldest first.
=== task 24 (api:tasks:read@git.forge.example/acme/widgets): grain task list
24  awaiting_reply  git.forge.example/acme/widgets  HQ task
25  awaiting_reply  git.forge.example/acme/widgets  HQ sibling
2 task(s) shown of 2 matching, oldest first.
```

**Live matrix row, no grant:** see S1. The deployment reads every
same-repo task and refuses others with the older `that tool belongs to the
"self-debug" capability` wording. `grain task list` live shows only this task,
so the list and the namespace disagree there too.

### 4. `grain task list` over 1133 tasks

The 1100 tasks were bulk-filed with 16 parallel `POST /api/tasks` (all `201`;
every third in `acme/gadgets`), on top of the seeded tasks. All from task 26
(self-debug):

| Command | Lines | stderr note |
|---|---|---|
| `task list` | 50 (1083 … 1132) | `50 task(s) shown of 1133 matching, oldest first. 1083 older matching tasks are not shown -- raise --limit to see them.` |
| `task list --limit 1100` | 1100 | `1100 task(s) shown of 1133 matching, … 33 older …` |
| `task list --limit 5000` | 1133 | `1133 task(s) shown of 1133 matching, oldest first.` |
| `task list --changed-since 24h --limit 3` | 3 | `3 task(s) shown of 1127 matching` (the seeded tasks carry older timestamps) |
| `task list --changed-since 1s` | 0 | `No tasks changed since 2026-11-01T08:37:32Z.` |
| `task list --stalled` | 4 | rows 22, 20, 21, 5, each `(stalled)` (`-stalled-tasks` seeds) |
| `task list --repo acme/gadgets --limit 2` | 2 | `2 task(s) shown of 368 matching` |
| `task list --search "Bulk task 1099"` | 1 | `1125  proposed  acme/widgets  Bulk task 1099` |
| `task list --json --limit 2` | 1 | `{"matched":1133,"shown":2,"tasks":[{"id":"1131",…},{"id":"1132",…}]}` (note in-band, nothing on stderr) |
| `task list --changed-since nonsense` | exit 1 | `bad request: "nonsense" is neither a span back from now (24h, 7d, 90m) nor a date …` |

Ordering is `sortTasksByFiling`: CreatedAt, then task number.

- With 16-way concurrent filing, creation instants do not follow IDs. So the
  default page ends `…1131, 1132` with 1133 earlier in it, and `--limit 5000`
  opens with the seeded tasks' backdated 10, 9, ….
- That is the documented order, not string order, so it **passes**.
- Read the list as "filing time", not "ID".

### 5. Truncation notices, `src://playbooks`, grep pruning

| Command | Result |
|---|---|
| `ls task:// --glob '113*'` (1133 tasks) | `113/ 1130/ 1131/ 1132/ 1133/` (direct children, no walk) |
| `ls task:// --glob '**/record.md'` | 324 matches, then `[truncated: walked 2000 entries; narrow the path]` |
| `grep 'Bulk task 7$' task://` | `37/summary.md:1:Task 37: Bulk task 7`, then `[truncated: walked 2000 entries; narrow the path]` |
| `ls task:// --glob '113*/summary.md'` | five matches, no truncation (segment pruning) |
| `ls ci://acme/widgets` after 212 fake runs | 200 runs, then `[truncated: listed the newest 200 of 212 runs; use grain ci list to filter the rest]` |
| `ls ci://acme/widgets --glob 'fake-ci-1*'` | 105 runs, then the same notice |
| `ci list --limit 500` | `200 of 212 matching run(s) in acme/widgets, newest first:` |
| `ls src://playbooks` (self-debug) | `cloudsql-postgres.md gcp-capabilities.md github-connections.md …`; `read src://playbooks/cloudsql-postgres.md` → `# Bootstrap: PostgreSQL & Cloud SQL Auth Proxy Setup` |
| `ls src://playbooks` (no grant, demo and live) | `forbidden: src://playbooks: that tool belongs to the "bootstrap-playbooks" capability` |

**Grep pruning of nested `attempts/`:** not proved live.

- The demo's minted runs record empty attempt prompts and transcripts, so
  `grep … task://24` and `grep … task://24 --glob '**'` agree trivially.
- Live, `grep 'Walkthrough: sandbox grain CLI' task://2223` returns
  `prompt.md`, `record.md` and `summary.md` and nothing under
  `attempts/1..5/`. It returns the same with `--glob '**'`, on both CLIs.
- But the live daemon predates #296, so this says nothing about the tree.
- This rests on `pkg/namespace/glob_test.go` (#296).

### 6. Host-qualified repo

The repo `git.forge.example/acme/widgets` was given CI Mode "grain" via
`PUT /api/repos/git.forge.example%2Facme/widgets/settings`. Task 1134 was
filed on it with `api:tasks:read@git.forge.example/acme/widgets`, and the
task picked up `grain-ci` automatically. From its sandbox:

| Command | Result |
|---|---|
| `task list` | `1134  awaiting_reply  git.forge.example/acme/widgets  HQ task` |
| `ci retrigger --all` / `ci list` | `fake-ci-215 grain/task-1134` / `1 of 1 matching run(s) in git.forge.example/acme/widgets` |
| `ls ci://git.forge.example/acme/widgets` | `fake-ci-215/` |
| `pr status` | `grain/task-1134 (this task's branch) in git.forge.example/acme/widgets` |
| `ls config://repos`, `ls memory://repos` | `git.forge.example/` |
| `read task://1135/record.md` (HQ sibling) | `Task 1135: HQ sibling` |
| `api GET /api/tasks/1135` | `{"id":"1135","repo":"git.forge.example/acme/widgets"}` |
| `api GET /api/repos/git.forge.example%2Facme/widgets/settings` | `task 1134 holds no api grant for scope repos:read …` (the right refusal, routed correctly) |
| from task 31: `ci list --task 1134` | `not found: task 1134 is not a task in a repository this sandbox may read CI for` |

The demo's proxy forwards only its one simulated repo, so `git push` to a
second host is not simulated. The UI's PR and issue links on the repo's own
host (`6506674f`) were not rendered: this branch changes no UI. Those links
rest on that commit's tests.

### 7. Stale-pin warning and v1 compliance refusal

On the `v0.50.0`-stamped demo (stamps `ghcr.io/bwsalmon/grain/k8s-sandbox:v0.50.0`
and `…/guest:v0.50.0`), the request was:

```
PUT /api/settings {"sandboxImage":"us-docker.pkg.dev/p/grain/k8s-sandbox:sha-1234567",
                   "k8sGranuleImage":"us-docker.pkg.dev/p/grain/guest:v0.49.0"}
```

- Settings' `sandboxImageCheck.stale` read: `Sandbox image
  us-docker.pkg.dev/p/grain/k8s-sandbox:sha-1234567 is grain's stock sandbox
  image from another build (sha-1234567), but this daemon is stamped for
  ghcr.io/bwsalmon/grain/k8s-sandbox:v0.50.0 (v0.50.0). … Clear the setting
  to follow the stamp, or name the v0.50.0 tag.`
- `GET /api/sandboxes` returned `enabled: false` (no pool wired) and
  `sandboxImageWarnings` held two entries:
  - `sandbox_image: … (sha-1234567) …`
  - `k8s_granule_image: … (v0.49.0) … stamped for ghcr.io/bwsalmon/grain/guest:v0.50.0`
- Re-pinning at the stamped build (`…:v0.50.0`) cleared both:
  `sandboxImageWarnings: null`.
- On a dev build (stamp `…:latest`), the same `sha-1234567` pin warns
  nothing, as `StaleStockPin` documents.

The stamped defaults still name `ghcr.io/bwsalmon/grain` (#2201).

Compliance, via `grain sandbox-image check -backend container [-probe]`:

| Image | Static check | `-probe` |
|---|---|---|
| `local/sbx:v1` | `FAILED … grain sandbox contract: v1 (older than minimum supported v2; rebuild FROM a current base image)` | `FAILED (… /etc/grain/sandbox-contract version 1 is older than minimum supported 2)`, exit 1 |
| `local/sbx:v2old` | `v2 (current)` | `FAILED (… /usr/local/bin/grain does not offer 'grain ls': it is an older grain without the sandbox CLI …)`, exit 1 |
| `local/sbx:v2` | `OK … v2 (current)` | `passed (sandbox-contract verify ok: grain=v2)`, exit 0 |
| `local/sbx:v2live` (the live image's `grain`) | `v2 (current)` | `passed` (N1) |

## Previous plan §5: the sandbox `grain` CLI everywhere

| Gap | Result |
|---|---|
| 1. Stale image gets the daemon's build at `~/.grain/bin/grain`, first on PATH; identical bytes get nothing | **PASS** (real Docker containers) |
| 2. Every subcommand from inside a sandbox using only `grain-sandbox` | **PASS** (demo, plus live where grants allow) |
| 3. Filter edge cases | **PASS after F2**, and N2 noted |
| 4. Placement failure is logged and the run still starts | **PASS** (real Docker, read-only rootfs) |
| 5. A placement named `grain` is refused | **CI only:** `TestSandboxBinDirIsTheOnePlaceableGrainPath` (`pkg/capability/protocol`) passes |

**Gap 1** used `placeSandboxGrain(ctx, <tree grain-sandbox>, "", containerSandbox{docker exec})`:

- On `local/sbx:v2old` before placement: `grain` →
  `usage: grain host-script <name> …`, and there was no `~/.grain/bin`.
- After placement: `/home/debian/.grain/bin/grain` (12063260 bytes, `0755`,
  sha256 `6ebd4a84…`, not the image's `f6d24b76…`). With the run's PATH,
  `command -v grain` → `/home/debian/.grain/bin/grain`, whose usage offers
  `grain ls`.
- A second placement left the file's mtime and ctime unchanged
  (`1793522813.1793522813` both times).
- On `local/sbx:v2`, whose `/usr/local/bin/grain` holds the identical bytes,
  nothing was placed (`ls: cannot access '/home/debian/.grain/bin/grain'`).
- Live: this task's sandbox has no `~/.grain/bin/grain`. That is consistent
  with the deployment's daemon and image coming from one build. The daemon is
  not visible from here to confirm it.

**Gap 4** ran on `--read-only` rootfs. `placeSandboxGrain` returned `placing
/home/debian/.grain/bin/grain: creating remote /home/debian/.grain/bin: mkdir:
cannot create directory '/home/debian/.grain': Read-only file system`, and the
image's `grain` still answered. `pkg/orchestrator/run.go` logs that as
`orchestrator: task <id>: the sandbox keeps its image's grain: …` and carries
on.

**Gap 2** ran from demo task 27 (`self-debug`, `grain-log`; acme/widgets on
Grain CI), with `GRAIN_SANDBOX_PROXY` unset:

| Command | Result |
|---|---|
| `ls ci://acme/widgets` / `ls task://` / `ls deploy://` | OK (`deploy://`: `grain.log health merge-queue metrics/ …`) |
| `read task://1/record.md`, `grep dark task://1` | OK |
| `ci retrigger --all`, `ci list`, `pr status` | OK |
| `ci wait --timeout 3s` | `CI run fake-ci-2 is still running after 3s (… wait credit used: 3s / 45m0s).` plus `--resume` / `resume_later` hints |
| `ci log` | exit 0, empty (no jobs; N2) |
| `task message 1 "…"` | `forbidden: task 27 may only message its parent or its children, and task 1 is neither: message_task to any other task needs api:task-admin` (the right refusal) |
| `metrics --window 24h --repo acme/widgets` | `window: … (24h0m0s)` / `scope:  tasks in acme/widgets` |
| `metrics --window 24h --repo other/repo` | `scope:  tasks in other/repo` (self-debug reads all; without it `grain metrics` is refused outright, step 1) |
| `host-script --list` | `grain host-script: no host scripts are granted to this sandbox` |

The tree's full `grain` (`cmd/grain`) was not compared side by side. Inside a
sandbox it dispatches to the same `pkg/sandboxcli`. The live image's binary
gave the same answers as the tree's once pointed at the proxy (step 1).

**Gap 3, filter edge cases:**

| Command | Before F2 | After F2 |
|---|---|---|
| `log --since 10m --contains proxy --max-lines 5` | exit 1, `since "10m" is not an RFC3339 timestamp …` | exit 0, `daemon log, since 2026-11-01T08:35:39Z, containing "proxy".` / `No entries matched. …` |
| `log --since 2h --until 1m --max-lines 2` | refused | `daemon log, 2026-11-01T06:45:39Z to 2026-11-01T08:44:39Z.` |
| `log --since nonsense` / `--since yesterday` | refused | still refused, in the tool's words |
| `log --lines 2` (alias of `--max-lines`) | OK | OK |
| `log --min-priority warning` | `… priority warning and above.` | same |
| `log --min-priority 3`, `--min-priority bogus` | `min_priority "3" is not a journald priority; the ones there are: emerg, alert, crit, err, warning, notice, info, debug` | same (N2) |
| `metrics --since nonsense` | `moment "nonsense": expected a date like 2026-01-31, or an RFC 3339 timestamp` | same |

## CI-proved items run locally

All of these ran with `-count=1` and passed:

- `go test ./pkg/sandboxcli ./pkg/gitproxy ./pkg/namespace ./tests/security`
- `go test ./pkg/orchestrator -run 'TestPlaceSandboxGrain|TestSandboxGrainBinaryBeside'`
- `go test ./pkg/capability/protocol`
- `go test ./tests/e2e -run TestMCPServerRecreatesARealSandboxOverStdio`
  (PR #250, adopted runs recreating their sandbox)
