# Agent resilience and standing sandboxes (2026-10-04)

This report walks through `docs/test-plans/2026-10-04.md` §7 ("Agent resilience and standing sandboxes") for task 1833.

## Result

- Every step of the §7 procedure ran against real components.
- Each behaviour the plan names holds:
  - the burst pause;
  - the near-misses that must not pause;
  - the replayed Claude session-limit marker;
  - standing sandboxes on `/api/sandboxes` and in Sandbox health, including recreation after a kill;
  - the served unit runs as `debian`;
  - a removed sandbox network is recreated.
- The walkthrough found two scoped bugs, both fixed here with regression tests. It also found two larger gaps, filed as follow-ups.

## Tested commit

- Base: `candidate` at `9b06116c4` (Merge pull request #2872), which this branch was cut from.
- `git branch -r --contains` lists `origin/candidate` and `origin/main` for each of `d739f4770`, `8d494aa1b`, `8269ef45d`, `1d44648f0`, `44e0600ac`, `0774d8c20` and `080961bd1`.
- The fixes below were re-walked on the same daemon after they were built.

## Environment

- Grain sandbox VM: Debian 12 (bookworm), kernel 6.1.0-53-amd64, 4 vCPU and 7.8 GiB RAM.
- `/dev/kvm` is present, with nested virtualization (`kvm_intel nested=Y`).
- Docker 29.8.2, Go 1.26.6, Node 20.19.0, and Playwright Chromium from `/var/cache/playwright`.
- Granule was built from `third_party/granule`:
  - `go build -mod=vendor ./cmd/granule`;
  - the `granule:vendored` image, built with `scripts/granule/build-granule-image.sh`.
- Guest image `grain-guest:e2e-test`:
  - resolved with `IMAGE=grain-guest:e2e-test ./scripts/granule-guest-resolve.sh`;
  - a cache hit on `ghcr.io/bwsalmon/grain/guest:inputs-c901d645225731cc93f35315010caeaa`.
- UI: `cd ui && npm ci && npm run build`, so the daemon and demo serve the production bundle from `pkg/ui/static`.

## Stubs

- **Model sessions.** Steps 1 and 2 have no live model.
  - Step 1 uses a scripted `agent.Framework` (`refusingFramework` in `tests/e2e/refusal_burst_walkthrough_test.go`). Each run advances the shared clock by 10s and fails with a fixed text. It can optionally report one tool call first.
  - Step 2 runs the real `claude.New` runner. Its `claude` binary is a shell script that `cat`s a captured stream-json transcript and exits 1. The transcript has the `system/init` event, then a `result` with `subtype:"success"`, `is_error:true` and `"You've hit your session limit · resets 5:30pm (UTC)"`.
- **GitHub.** Step 1 runs use `githubsim` in front of the world's bare repo, and the world's gitproxy forwards to a local `git http-backend`.
- **Paused state in the browser.** It is seeded with `grain demo -agent-paused`. That is a real `orchestrator.Pause`, begun from an `agent.UsageLimitError` (antigravity, 6h). It is not a refusal burst: the demo has no flag for one, and the daemon run had no model connection to fail. The UI reads both kinds through the same `GET /api/pause`, so only the reason text and the window differ.
- Nothing else is stubbed. The store is SQLite, and the daemon, supervisor, granule VMs, docker networks and UI are all real.

## What could not be run

- **ContainerSandboxes.** Not needed, since `/dev/kvm` exists. It also could not serve: only `GranuleSandboxes` implements `ServingSandboxes.Serve`. On a container or host backend the supervisor reports `sandboxes backend does not support serving standing sandboxes: ...` as the sandbox's `lastError`.
- **Network removal before a task.** The plan's "file a task, it gets a sandbox" was not exercised. The daemon had no agent connection, so nothing would dispatch.
  - The acquire after `docker network rm` was the standing sandbox's own restart instead.
  - That goes through the same `GranuleSandboxes.acquire` and `ensureNetwork` path a dispatch uses.
- **A burst's own words in the UI.** No browser saw a burst's reason text (see Stubs).

## Commands and outcomes

### 1. Refusal burst, through real cycles (Go e2e harness)

Command:

    go test ./tests/e2e/ -run 'TestWalkthroughRefusalBurst|TestWalkthroughDifferentTexts|TestWalkthroughReplayedClaude' -count=1 -v

This is `tests/e2e/refusal_burst_walkthrough_test.go`, new in this change. It uses `orchestrator.RunCycle` with three workers and four queued tasks. `Config.Now` and the cycle share one clock.

- **Burst.** All three runs failed 10s after start with no tool call and `Refused: policy X`. The framework paused for exactly 15 minutes after the third failure. The log line was:

      level=error condition=unrecognised_refusal_burst framework="antigravity" runs=["rb-3-1" "rb-2-1" "rb-1-1"] paused_until=2026-08-27T12:15:30Z message="Refused: policy X" -- antigravity failed the same way 3 times in a row; this is almost certainly a provider refusal grain has no marker for

  - All three runs were recorded `paused`, and every failure streak was 0.
  - Cycles at +1, +7 and +13 minutes dispatched nothing, and all four tasks stayed `queued`.
  - At +15 minutes `PausedFrameworks` was empty and the next cycle dispatched again.
  - The provider was still "refusing", so the three retries formed a second burst. It paused again until 12:31:00, which is the designed behaviour.
- **Different texts.** Three runs failed with three different texts (`Refused: policy X`, `Refused: policy Y`, `Refused: policy Z`). There was no pause and all three runs were `failed`.
- **Tool call.** Three runs had identical texts but each reported a tool call first. There was no pause, and each run was `failed` with "the run made 1 tool call(s) [run_command x1] first".

Harness note: with one worker the scheduler retries the same failed task straight away. The near-miss subtests therefore count three runs of `nb-1`, not three tasks.

### 2. Claude session-limit replay

The result was an `agent.UsageLimitError` for `claude`:
- `ResetAt` is the next 17:30 UTC after the wall clock: 2026-10-04T17:30:00Z, with the test run at about 16:53Z.
- The orchestrator logged `claude dispatch paused until 2026-10-04T17:30:00Z`.
- The run was recorded `paused`, the failure streak was 0 and the task stayed `queued`.

The parser reads "(UTC)" as UTC. A reset with no zone is also read as UTC (`resetLocation`), not in the deployment's own zone. That is the documented choice ("what the sandboxes the CLI runs in keep"), so it is not filed. A reset more than 6h away is capped by `maxUsageLimitPause`, and the test allows for that.

### 3. The paused state from the operator's side (`grain demo -agent-paused`, Playwright Chromium)

- **Banner, on every page:** "Agent usage limit reached: no antigravity task is being dispatched until 10:58:16 PM (about 5h 59m). — antigravity: usage limit reached; Gemini API quota exhausted ... / resume now". `GET /api/pause` reports `frameworks: ["antigravity"]` and `secondsRemaining`.
- **Sandbox health** (`/admin/sandboxes`): "Not available: this deployment has no sandbox pool or host stats configured". That is expected on the demo, which has no sandbox pool. It says nothing about the pause.
- **Team settings → Connections:** the antigravity connection reads `active`, with no mention of the pause. **Models** says "Served by: gemini 2.5 pro (enabled)".
- **Task pane of a queued task** (`/tasks/2`): the state is just `queued`, with no hold reason, and "run now" is offered.
- This is finding F4.

### 4. Standing sandbox on real granule VMs

The standing sandbox was declared straight into the daemon's store with `store.PutStandingSandbox`: name `walk`, image `grain-guest:e2e-test`, command `sleep infinity`, env `WALK=1`. The daemon command was:

    PATH=/tmp/gbin:$PATH grain daemon -data-dir /tmp/dd -ui-addr 127.0.0.1:18420 -granule-sandboxes \
      -granule-sandbox-cidr 172.30.7.0/24 -granule-sandbox-network grain-sbx -granule-git-proxy-host 172.17.0.1 \
      -granule-state-dir /tmp/gstate -granule-ssh-user debian -granule-workspace /home/debian \
      -granule-create-arg=-kontur-image -granule-create-arg=grain-guest:e2e-test -granule-create-arg=-guest-port \
      -granule-create-arg=22 -granule-create-arg=-guest-user -granule-create-arg=debian -granule-create-arg=-disk-mode=overlay

(The first two runs had no `-granule-sandbox-cidr`/`-granule-sandbox-network`. Those flags were added for step 5.)

- **Healthy.** `starting standing sandbox "walk"` was logged, and within one tick `GET /api/sandboxes` returned `standingSandboxes: [{name: walk, image: grain-guest:e2e-test, restarts: 0, failed: false}]`. The VM was `g-walk`.
- **Served unit as `debian`.** `granule vm exec g-walk -- systemctl status standing-walk; ps -o user -C sleep` showed:
  - a transient system unit `/run/systemd/transient/standing-walk.service`, `active (running)`;
  - `/bin/sh -lc "sleep infinity"`;
  - `USER debian ... sleep infinity`.
  - So the unit is started through `sudo -n systemd-run --uid=debian` (1d44648f0), and this still holds under the single supervisor `8269ef45d` introduced.
- **Unit killed.** `sudo systemctl kill -s KILL standing-walk` was run at 17:02:40. The probe logged `liveness probe failed (1/2) ... not active: exit 3: inactive` and then 2/2. At 17:02:52 it logged `failed (...); attempting restart`. `/api/sandboxes` then showed `restarts: 1`, with a new `startedAt`.
- **Container killed.** `docker kill granule-vm-g-walk` was run at 17:03:07. Two probe misses (`container ... is not running`) were followed by a restart at 17:03:22, and `restarts: 2`.
- **Sandbox health (Playwright, logged in as `debian`).** The "Standing sandboxes" table showed `walk | grain-guest:e2e-test | running | 10/4/2026, 5:06:26 PM | 1`, above the live-sandbox table.
- **Daemon restart.** After the daemon is stopped, the standing VM keeps running. The next daemon replaces it, so the old VM is deleted and recreated rather than adopted. `restarts` starts again at 0, because the supervisor's state is in memory only.

### 5. `docker network rm` on the sandbox network while the daemon runs

1. With `-granule-sandbox-network grain-sbx -granule-sandbox-cidr 172.30.7.0/24`, the first acquire logged `created docker network "grain-sbx" with subnet 172.30.7.0/24`, and `g-walk` sat on `grain-sbx`.
2. `docker network rm grain-sbx` while the VM ran was refused by docker: `network grain-sbx has active endpoints (name:"granule-vm-g-walk" ...)`.
3. `docker kill granule-vm-g-walk` was followed by `docker network rm grain-sbx`, which succeeded at 17:06:09.
4. The supervisor's restart at 17:06:26 acquired again. It logged `created docker network "grain-sbx" with subnet 172.30.7.0/24` at 17:06:27, and the new `granule-vm-g-walk` came up on `grain-sbx`, reporting `restarts: 1, failed: false`.
5. So the next acquire recreates the network (0774d8c20).

## Findings

### F1 (fixed): a failed first launch was retried as a "secret rotation" and its recovery never counted

**Reproduction (real daemon).** Declare a standing sandbox whose launch fails (for example name `walk-standing`, see F3) and start the daemon. Every 10s tick logged:

    orchestrator: standing sandbox "walk-standing" secret values changed; restarting instance
    orchestrator: error starting standing sandbox "walk-standing": ...

This was against a definition with no secrets.

**Cause.** `StandingSandboxSupervisor.start` only stored `envDigest` on success. After a failed first launch the stored digest was `""`, so every later tick took the definition/secret-changed branch with `afterFailure=false`. When a retry finally succeeded, `Restarts` stayed 0, and that is what `/api/sandboxes` and Sandbox health show.

**Deterministic reproduction.** `TestStandingSandboxFailedFirstLaunchIsRetriedAsAFailure` in `pkg/orchestrator/standing_sandboxes_test.go`: the launcher fails once, then succeeds. Before the fix it fails with `after the retry: failed=false restarts=0, want running with 1 restart`.

**Fix.** A failed launch records the digest it was attempted with, when the environment resolved. Re-walked on the daemon, the retries now log `standing sandbox "walk-standing" failed (...); attempting restart`.

### F2 (fixed): a failed standing sandbox's reason was the status chip's label

**Reproduction (Playwright on the daemon).** With `walk-standing` failing, the status cell is a MUI `Chip` whose label is `lastError`. Measured:
- the label has `text-transform: lowercase; white-space: nowrap`, so the error read `granule vm name "g-walk-standing" ... dispatch.runid`, with its identifiers mangled;
- the chip label was 1320px wide;
- the Standing sandboxes table needed `scrollWidth` 1798px in a 704px pane, which pushed "started" and "restarts" out of view.

**Deterministic reproduction.** The vitest case `shows a standing sandbox's failure reason outside its status chip` in `ui/src/components/SandboxHealthPage.test.jsx`. It fails before the fix, because there is no "failed" chip.

**Fix.** The chip says `failed`, and the reason is a wrapping `caption` below it, in its own case. Re-walked, the table is 704px in a 704px pane and the reason reads `granule VM name "g-walk-standing" ... dispatch.RunID`.

### F3 (filed): standing sandbox names over 9 bytes can never boot on granule, and failing launches retry every tick

**Reproduction.** Declare `{Name: "walk-standing", Image: "grain-guest:e2e-test", Command: "sleep infinity", Enabled: true}` on a granule daemon. Every tick fails with:

    granule VM name "g-walk-standing" is 15 bytes, over the 11-byte limit netshim's tap device naming imposes

- `model.ValidateStandingSandboxName` accepts the name, so the error only appears at launch.
- The retry runs every 10s with no backoff.
- Filed as "Standing sandboxes: reject names granule cannot boot, and back off a launch that keeps failing", against `main`. Validating in the importer needs a design decision, so it is not fixed here.

### F4 (filed): a framework pause is shown only in the banner

See step 3. Connections shows the paused framework's connection as `active`, and a queued task's pane gives no reason for the wait. Filed as "Say a framework is paused where an operator looks: Connections, Models and a queued task's pane", against `main`, for human review.

### Observations, not filed

- In Sandbox health's live-sandbox table, the standing VM `g-walk` is listed with type `agent` beside the runs. It is a standing sandbox, not an agent run.
- Supervisor state (`restarts`, `startedAt`) is in memory, so it resets when the daemon restarts. Standing VMs outlive a daemon stop and are recreated rather than adopted by the next daemon.
- `-granule-sandbox-network` does nothing without `-granule-sandbox-cidr`. The flag's help says so ("Only used with -granule-sandbox-cidr").

## Fixes in this change

- `test(e2e)`: `tests/e2e/refusal_burst_walkthrough_test.go`, the step 1 and step 2 walkthroughs, kept as regression tests.
- `fix(orchestrator)`: F1, with `TestStandingSandboxFailedFirstLaunchIsRetriedAsAFailure`.
- `fix(ui)`: F2, with a new `SandboxHealthPage.test.jsx` case.
- `gofmt` and `npm run format` are clean, and `make security` passed locally (`go vet ./...` and `go test -race ./tests/security/...`).

## CI verdict

- Grain CI full suite (`wait_for_checks` without `jobs`), run `056a0c2028a4457a` at `067f243b2`: **success**.
  - `frontend`, `go`, `installer`, `lint`, `package cache`, `postgres`, `terraform` and `ui e2e` all passed.
  - The run took 16m32s.
- Neither known flake (`RepoPage.test.jsx`, `team_declarations_ci_walkthrough.spec.js:229`) recurred.
- The only commit after that run adds this verdict to this report.
- `make security` passed locally (`ok github.com/bwsalmon/grain/tests/security 113.240s`).
