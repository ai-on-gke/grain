# Model source, paused-framework UI, chat delivery and standing sandbox names (2026-10-05)

**Plan**: `docs/test-plans/2026-10-05.md` §9
**Task**: grain/task-2000
**Tested commit**: `0c790498f` (`origin/candidate` when this task started; `grain/task-2000` was cut from it). The fix below was re-walked at `60eaecd58`, the branch tip.
**§9 commits**: `f275315c5`, `885f60f0d`, `a4968aef0`, `1fe1adf43`, `c25b751c7`, `5cdd48880`, `2a8bea686`, `9e70aa4bb`, `ab58767e7`, `83f6ce5b9` and `a9162b936`. `git branch -r --contains <sha>` lists `origin/candidate` (and `origin/main`) for every one of them.

## Environment

| | |
|---|---|
| OS | Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, x86_64, 4 vCPU, 7.8 GiB RAM, `/dev/kvm` present |
| Go | go1.26.6 linux/amd64 |
| Node | v20.19.0 |
| Docker | 29.8.2 |
| Playwright | 1.62.1, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, Chromium 1234 headless |
| UI bundle | `cd ui && npm ci && npm run build` into `pkg/ui/static`, then `go build -o /tmp/grain ./cmd/grain` |
| Granule | `go build -mod=vendor ./cmd/granule` in `third_party/granule`; image `granule:vendored` from `scripts/granule/build-granule-image.sh` |
| Guest image | `GRAIN_CI_REPO=bwsalmon/grain IMAGE=grain-guest:e2e-test ./scripts/granule-guest-resolve.sh`, a cache hit on `ghcr.io/bwsalmon/grain/guest:inputs-c901d645225731cc93f35315010caeaa`. The script fails with `GRAIN_CI_REPO: unbound variable` outside CI unless that variable is set. |

## Servers and harnesses

- **`grain demo -agent-paused`**, one per Playwright worker through `ui/e2e/fixtures.js` (and one by hand on port 18555 for the API probes in step 1). `-agent-paused` begins a real `orchestrator.Pause` from an `agent.UsageLimitError` on antigravity, the framework of demo's one connection (`gemini-pro`), for 6h.
- **`tests/e2e`** (the Go world harness): real SQLite store, real `RunCycle`/`dispatch.Cycle`/`RunDispatch`, a real `ui.Client` and `ui.Server` over `httptest`, githubsim in front of bare repos.
- **`grain daemon` on granule**, one data dir (`/tmp/dd`), restarted once onto the fixed binary:

      PATH=/tmp/gbin:$PATH grain daemon -data-dir /tmp/dd -ui-addr 127.0.0.1:18420 -granule-sandboxes \
        -granule-git-proxy-host 172.17.0.1 -granule-state-dir /tmp/gstate -granule-ssh-user debian \
        -granule-workspace /home/debian -granule-create-arg=-kontur-image -granule-create-arg=grain-guest:e2e-test \
        -granule-create-arg=-guest-port -granule-create-arg=22 -granule-create-arg=-guest-user \
        -granule-create-arg=debian -granule-create-arg=-disk-mode=overlay

- **Container backend**: a real `orchestrator.NewContainerSandboxes` on docker, driven by a throwaway `main` (not committed), for the package-cache check.

## Stubs

- **Model sessions.** No live model anywhere.
  - Step 3 uses `chatFramework` (`tests/e2e/pause_chat_walkthrough_test.go`): each run records its prompt and answers in plain text, and the first run posts a person's message through `ui.Client.AddComment` before returning.
  - Step 2's expiry uses `limitedFramework` in the same file: its first run returns an `agent.UsageLimitError` with a reset 90s away on the shared clock, and later runs answer in text.
- **Usage-limit pause in the browser.** `grain demo -agent-paused` (above). There is no admin API that *sets* a pause (`GET`/`DELETE /api/pause` only), so the browser walk used demo's, and the expiry was driven by a real run in Go.
- **GitHub.** githubsim in the Go harness. The daemon had no GitHub credential and needed none.
- Nothing else. grain's API, React, the store, the supervisor, granule VMs and docker are all real.

## Steps and outcomes

### 1. Model source (browser, `grain demo`)

`ui/e2e/model-source-pause-walkthrough.spec.js` test 1 **passes** (`npx playwright test e2e/model-source-pause-walkthrough.spec.js --project=chromium`). Templates, schedules and tasks are filed through grain's own API; the task pane's Model line is read in Chromium.

| How the task was filed | API `model` / `modelSource` | Pane's Model line |
|---|---|---|
| From a template with `model: gemini-2.5-flash` | `gemini-2.5-flash` / `template` | `gemini-2.5-flash (from the template)` |
| Run now on a schedule with `model: gemini-2.5-pro` | `gemini-2.5-pro` / `schedule` | `gemini-2.5-pro (from the schedule)` |
| Run now on a schedule over that template, with its own `gemini-2.5-pro` | `gemini-2.5-pro` / `schedule` | `gemini-2.5-pro (from the schedule)` |
| Run now on a schedule over that template, no model of its own (curl only) | `gemini-2.5-flash` / `template` | — |
| From the template, model overridden to `gemini-2.5-pro` | `gemini-2.5-pro` / none | `gemini-2.5-pro`, no "from the" |
| From the template, model restated as the template's own `gemini-2.5-flash` (curl only) | `gemini-2.5-flash` / none | — |
| Template task, then `PATCH {title}` | unchanged / `template` | still `(from the template)` |
| Template task, then `PATCH {model: gemini-2.5-flash}` (same model) (curl only) | unchanged / `template` | — |
| Schedule task, then `PATCH {model: gemini-2.5-flash}` | `gemini-2.5-flash` / none | `gemini-2.5-flash`, no "from the" |

All as `f275315c5` intends. Restating the template's own model when filing gives no credit, because `modelSourceOfRequest` only credits a model `fromTemplate` filled in. That is defensible: the person chose it.

### 2. A paused framework (browser on `grain demo -agent-paused`, then Go for expiry)

Test 2 of the same spec **passes**:

- `GET /api/config` → `agentPause: {paused: true, frameworks: ["antigravity"], reason: "antigravity: usage limit reached; Gemini API quota exhausted for project example-sandbox (demo); resets at …"}`.
- **Connections** (Team settings → Connections): the connection's chip reads `Paused` (no `Active` chip anywhere), with the line `paused until 11:31:58 AM (about 5h 59m) — antigravity: usage limit reached; Gemini API quota exhausted …` under it.
- **Models**: "Served by" chips `gemini-pro (Paused)`, and its `title` tooltip is `… : paused until … — … Gemini API quota exhausted …`.
- **Queued task's pane**: `grain waiting: Antigravity is paused until 11:31:58 AM (about 5h 59m) — antigravity: usage limit reached; …`.
- **Run now**: the hint `Antigravity is paused until … A run asked for now waits for that, unless the pause is lifted with Resume now on the banner.` is shown before the click. After the click the pane says `Asked for a run…`, and the API reads `state: queued` with `runNowAt` set. Demo has no dispatch loop, so "does not start" is proven on the daemon-shaped Go walk below.
- **Lifted from the banner** (Resume now): the pane's waiting line and hint are gone, `/api/config` reads not paused, Connections reads `Active` with no paused line, and Models reads `gemini-pro (Enabled)` with no `(Paused)` chip.

**Expiry and dispatch**: `TestWalkthroughAShortPauseExpiresAndTheQueuedTaskDispatches` (`tests/e2e/pause_chat_walkthrough_test.go`) **passes**. A real run meets the usage limit (`dispatch paused until 12:01:35`). `GET /api/pause` and `GET /api/config` both read paused with the provider's reason. `POST /api/tasks/sp-1/run-now` → 2xx, and a cycle 30s into the pause runs nothing (task `queued`). After the reset, both reads are clear, the log says `the agent usage-limit pause … has expired; dispatch resumes`, and the next cycle dispatches the task. The UI reads `agentPause` from `/api/config` only, so the panes clear with it (as the Resume-now walk shows).

The waiting-reason fixes (`a4968aef0`, `1fe1adf43`, `c25b751c7`) were walked on a daemon in `2026-10-04-connections-and-models.md` (F1-F3) and their tests pass in this branch's CI. They were not re-walked here.

### 3. Chat delivery (Go e2e, fake agent)

`TestWalkthroughAMessagePostedWhileTheRunAnswersIsDispatchedFor` **passes**:

1. The first cycle files the director session (`Interactive`) and parks it `awaiting reply`.
2. Alice posts "what is this deployment doing?" through `ui.Client.AddComment`. The next cycle dispatches run `1-1`. While it writes its final answer, Alice posts "and while you are at it, what is queued?".
3. When the run ends: the log says `task 1 run 1-1: a message was posted that the run was never shown; leaving the task ready to be dispatched for it`. The task is `queued`, not parked; no "Waiting for you." follows the delivered mark; `Observation.DeliveredCommentID` is before the second message. The first prompt did not contain it.
4. The next cycle dispatches run 2, whose prompt contains the second message. The cycle after that parks the session with no third run.
5. `POST /api/tasks/1/delivered {"through": <comment id on task chat-other>}` → **400**. `{"through": <the second message's id>}` → 200.

### 4. Standing sandboxes (granule VMs on docker; container backend for the package cache)

**Container backend cannot serve standing sandboxes.** Only `GranuleSandboxes` implements `ServingSandboxes.Serve`. Any other backend reports `sandboxes backend does not support serving standing sandboxes: …` (`pkg/orchestrator/standing_sandboxes.go:475`). `2026-10-04-agent-resilience-standing-sandboxes.md` already records this, and it is a design limit, not a regression. So the launch and backoff steps ran on granule, whose VMs are docker containers (`granule-vm-g-…`). The package-cache step ran on the real container backend, which is what `a9162b936` changed.

Each standing sandbox was declared by inserting a `standing_sandbox` row into the daemon's SQLite store (`sqlite3 /tmp/dd/store/grain.db "INSERT INTO standing_sandbox (name,image,command,enabled) VALUES (…)"`). That is the same row `store.PutStandingSandbox` writes, since there is no API for it.

- **Long name.** `walk-standing-long-name`, `grain-guest:e2e-test`, `sleep infinity`, inserted at 05:37:33. At 05:37:42: `starting standing sandbox "walk-standing-long-name"`, and VM `granule-vm-g-s7b7f727b` was up. `GET /api/sandboxes` → `standingSandboxes: [{name: walk-standing-long-name, sandbox: s7b7f727b, restarts: 0, failed: false}]`. It ran without a restart until the daemon was stopped 26 minutes later, then launched again on the next daemon.
  - **Sandbox health (Chromium):** the Standing sandboxes table reads `walk-standing-long-name / s7b7f727b | grain-guest:e2e-test | running | … | 0`. The live-sandbox table read `s7b7f727b | agent | granule | g-s7b7f727b | ready …`, with no declared name, typed as an agent run (**F1**, fixed).
- **Missing image.** `walk-missing-image`, `grain-guest:does-not-exist`, inserted at 05:39:12.
  - **Reason beside the chip (Chromium):** the chip says `failed`, and the full `lastError` (`creating granule VM "g-s0a0e0593": … No such image: grain-guest:does-not-exist … pull access denied …`) wraps below it. Under that: `3 failed launches in a row; next attempt 10/5/2026, 5:42:52 AM`. The panel is 1112px in a 1112px pane, with no overflow.
  - **Backoff, counted from the log** (`grep 'next attempt' daemon.log`):

    | Attempt | Logged at | Next attempt at | Wait |
    |---|---|---|---|
    | 1 | 05:39:23 | 05:39:52 | 30s |
    | 2 | 05:39:53 | 05:40:52 | 1m |
    | 3 | 05:40:53 | 05:42:52 | 2m |
    | 4 | 05:43:03 | 05:47:02 | 4m |
    | 5 | 05:47:13 | 05:55:12 | 8m |
    | 6 | 05:55:13 | 06:10:12 | 15m (cap) |

    That is 6 launches in 24 minutes, where the 10s tick would have made about 145. Each attempt lands on the first tick after its due time. After the daemon restart the count starts again at 1 (06:03:35), because supervisor state is in memory (already noted on 10-04).
- **Package-cache config written as root (container backend).** A real `NewContainerSandboxes{Docker: docker, Image: walk-nosudo:1}` was used. `walk-nosudo:1` is `debian:bookworm-slim` plus `useradd -m -u 1000 debian; USER debian`, so it has no sudo, like `Dockerfile.k8s-sandbox`. `Acquire("walkpc")` was followed by `ConfigurePackageCache(ctx, sb, "http://172.17.0.1:9999")`, which returned `configured: true, err: nil`. Inside the container, as `uid=1000(debian)` with no sudo:

      root:root 644 /etc/apt/apt.conf.d/01-grain-package-cache   readable by debian
      root:root 644 /etc/pip.conf                                 readable by debian
      root:root 644 /etc/docker/certs.d/ghcr.io/hosts.toml        readable by debian
      root:root 644 /etc/docker/certs.d/gcr.io/hosts.toml         readable by debian
      root:root 644 /etc/grain/package-cache.env                  readable by debian
      touch: cannot touch '/etc/pip.conf': Permission denied

## Findings

### F1 (fixed): Sandbox health's live table names a standing sandbox only by its hash, as an agent run

**Reproduction.** On a granule daemon, declare a standing sandbox `walk-standing-long-name` (above) and open `/sandboxes`. The live-sandbox table's row is `s7b7f727b | agent | granule | g-s7b7f727b`. The declared name appears nowhere in that row, and the type says it is an agent run. On 10-04 the same row was `walk | agent` (an observation in that report, not filed). Since `9e70aa4bb` named the VM `s<hash>`, the name is gone too. The live table is where a person looks to see what machines exist.

**Deterministic reproduction.** The Vitest case `names a standing sandbox's live row by its declared name` in `ui/src/components/SandboxHealthPage.test.jsx`. Without the fix it fails (checked by stashing the component change).

**Fix.** `SandboxHealthPage.jsx` maps `standingSandboxes[].sandbox` to the declared entry. A live row whose sandbox matches one shows the declared name, with the `s<hash>` under it in monospace, and type `standing`. The fix is frontend only, because `GET /api/sandboxes` already returns both lists. Re-walked on the fixed daemon in Chromium, the row reads `walk-standing-long-name s7b7f727b | standing | granule | g-s7b7f727b | ready`.

### Observations, not filed

- **O1.** The Run now hint and the queued task's waiting line both say the pause can be lifted "with Resume now on the banner". But the task pane is a modal dialog, so the banner's button is behind it: aria-hidden, and not reachable by role or by click until the pane is closed. The spec closes the pane (Escape) before pressing Resume now. This is minor, and the wording is still true once the pane is closed.
- **O2.** `scripts/granule-guest-resolve.sh` needs `GRAIN_CI_REPO` set when run by hand (`line 197: GRAIN_CI_REPO: unbound variable`).
- **O3.** The plan asks for step 4 "on the docker container backend". Standing sandboxes cannot be served there (see step 4). Future plans should say granule.
- Overlap check: `docs/test-runs/` has no report of F1 as a finding, only the 10-04 observation. O1-O3 are not filed.

## Fixes in this change

- `test(e2e)`: `ui/e2e/model-source-pause-walkthrough.spec.js` (steps 1 and 2 in the browser).
- `test(e2e)`: `tests/e2e/pause_chat_walkthrough_test.go` (step 3, and step 2's expiry and dispatch).
- `fix(ui)`: F1, with a new `SandboxHealthPage.test.jsx` case.
- `gofmt -l` and `npm run format` are clean. `make security` passed locally (`go vet ./...`; `go test -race -count=1 ./tests/security/...` ok in 156s).

## Commands

    cd ui && npm ci && npm run build && cd .. && go build -o /tmp/grain ./cmd/grain
    cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/model-source-pause-walkthrough.spec.js --project=chromium
    go test ./tests/e2e/ -run 'TestWalkthroughAMessagePostedWhileTheRunAnswersIsDispatchedFor|TestWalkthroughAShortPauseExpiresAndTheQueuedTaskDispatches' -count=1 -v
    cd ui && npx vitest run src/components/SandboxHealthPage.test.jsx
    # step 4: the granule daemon above, sqlite3 inserts, grep of the daemon log, and a throwaway
    # Playwright script reading /sandboxes with X-Grain-User: debian
    make security

## CI verdict

Grain CI run `67687ad680de9f17` on `60eaecd58` (the walkthroughs and the F1 fix) was **success**: frontend, go, installer, lint, package cache, postgres, terraform and ui e2e. No known flake recurred.

Grain CI run `dc711274c23bddc4` on `bdb54e671` (this report, merged with `origin/candidate` at `ab557fa98`) was also **success** on every job. Its `go` job passed on grain's automatic retry: "first attempt failed: step Test: FAIL". The first attempt's output is not kept, so the failing test is unknown. This branch adds two Go tests, both on a scripted clock. They passed `go test -race -count=5` locally (53s), so the red is recorded here as an unidentified flake, not as one of them. The verdict on the commit that adds this paragraph is in the pull request.
