# 2026-10-07: pkg/daemon and pkg/demo move, demo -upgrade-image, image stamping, per-section metrics

This report runs §7 of `docs/test-plans/2026-10-06.md` (task 2385, filed by task 2231).

## Environment and tested commit

- Base: `candidate` at `dafd697783f1626701cadd60e38f5821e62e5461`. Branch `grain/task-2385` was cut from it.
- `git branch -r --contains` lists `origin/candidate` for every commit in scope:
  - `b6d9367ce`, `0d4d2f76a` and `c5321c572` (daemon wiring moved to `pkg/daemon`);
  - `f57b5b21a` (`pkg/demo`);
  - `41b9cd9d6` (`pkg/ci/cli`);
  - `a3d4ae927` (`cmd/grain` split);
  - `47541e8af` (image stamping);
  - `2c3c1d6ac` (`demo -upgrade-image`);
  - `586f28bf4` (default allow-list);
  - `87b4b7eb3` (`?section=`).
- The pre-move reference `98f2a031d` is on both `main` and `candidate`. It was checked out in a worktree at `/home/debian/old`.
- Tools: Go 1.26.6, Node 20.19.0, `@playwright/test` ^1.62.1 with `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` (Chromium), and Docker 29.8.2.
- UI: the production bundle built by `make build` (`npm ci --omit=dev && npm run build` into `pkg/ui/static`), embedded in `bin/grain`. `npm ci` in `ui/` was run as well, to install Playwright.

## Stubs

- **Registry:** a real `registry:2` container on `127.0.0.1:5000`, so the registry stub from the 10-05 §6 run was not needed.
- **Pushed image:** one image, tagged `main`, `candidate`, `latest`, `v1.7.0` and `grain-task-1`. It is `FROM scratch` holding the stamped `bin/grain` as its entrypoint, so the upgrader's health check (`docker run --rm <image> schema-version`) really runs grain and prints `17`.
- **Nothing else was stubbed.**
  - The walkthrough drove the real `grain demo` server, its real `/api/upgrade*` handlers, the real image-mode `upgrade.Upgrader` and real `docker pull` / `docker run`.
  - The browser's only setup step was `localStorage["grain.username"]="debian"` (loopback's trusted actor). It skips the first-visit username dialog.
  - No `page.route` was used.

## 1. Flag wiring audit

Commands:

```
(cd /home/debian/old && go run ./cmd/grain daemon -help; go run ./cmd/grain demo -help)   # 98f2a031d
go run ./cmd/grain daemon -help; go run ./cmd/grain demo -help                            # candidate
diff …
```

**Help diff.** No flag disappeared from either command.

- `daemon` gained `-iap-resource`, `-relay-session-lifetime`, `-ui-trust-loopback` and `-ui-trust-relay`.
- `-tunnel-gateway-url` and `-tunnel-token` are now "removed: … refuses to start".
- `-ui-trust-tunnel` is now "former name of -ui-trust-relay".
- The `-ci` and `-upgrade-allowed-branches` texts changed. The second now defaults to `main,candidate,latest,v[0-9]*.[0-9]*.[0-9]*` (`586f28bf4`).
- `demo` gained `-github-api`, `-merge-rules`, `-upgrade-image`, `-upgrade-allowed-branches` and `-upgrade-verify-cmd`.

**Consumers.** The audit worked at two levels:

1. Every flag variable declared in `parseDaemonArgs` (`x := fs.T(…)` and the `fs.Var` lists) is referenced after its declaration.
   - Flags with no variable are deprecated and deliberately ignored (`-max-ci-jobs`) or refused at start (`-tunnel-gateway-url`, `-tunnel-token`).
2. Every `daemon.Config` field was checked for a set in `cmd/grain/daemon.go` and a read anywhere in non-test `pkg/daemon`.

Fields set in `cmd/grain` but never read in `pkg/daemon`:

| Field | Flag | Where the flag's value really goes | Verdict |
|---|---|---|---|
| `PostgresURL` | `-postgres-url` | folded into `-db-dsn` / `-db-driver` in `parseDaemonArgs` | dead field, removed |
| `RelayAudience` | `-relay-audience` | `RelayOverrides.IDTokenAudience` → `RelayManager.Reload` → `relay.ClientConfig.IAPAudience` | dead field, removed |
| `RelayIAPJWTAudience` | `-relay-iap-jwt-audience` | `RelayOverrides.IAPJWTAudience` → `relay.ClientConfig.IAPJWTAudience` | dead field, removed |
| `GranuleNet`, `GranuleBaseIP`, `GranuleBasePort` | `-granule-net`, `-granule-base-ip`, `-granule-base-port` | deprecated and ignored; `parseDaemonArgs` logs a warning naming each | left as is: `daemon_kontur_flag_alias_test.go` reads them to check the `-kontur-*` alias mapping |

The fields with no `cmd/grain` set are store-backed or test seams (`AgentBackends`, `HostCPUs`, `IapWebClient`, …). All of them are read.

Every `demo.Options` field is read in `pkg/demo`. `Options.UpgradeAllowedBranches` and `Options.UpgradeVerifyCmd` reach `configureUpgradeDemo` → `upgrade.Config.AllowedBranches` / `ImageConfig.VerifyCmd`. A live check showed both:

- `grain demo -upgrade-image=127.0.0.1:5000/grain -upgrade-verify-cmd=/bin/false -upgrade-allowed-branches=main`
  - `/api/upgrade/targets` marked only `main` allowed.
  - `POST {"branch":"v1.7.0"}` → 400 "matches none of main".
  - `POST {"branch":"main"}` → phase `failed` at step 2/7 signature: "/bin/false 127.0.0.1:5000/grain@sha256:8d05… exit status 1".

**Finding F1: no flag lost its consumer, but a test was checking a dead field.**

- The move kept every flag reaching its consumer.
- `TestParseDaemonArgsRelayIAP` (`cmd/grain/daemon_relay_test.go`), however, asserted on `cfg.RelayIAPJWTAudience`, a field nothing in `pkg/daemon` reads. The relay client takes its audiences from `cfg.RelayOverrides`.
- So the test would have passed with the flag's real wiring cut.
- Reproduction: delete `FlagOrEnvSource("relay-iap-jwt-audience", …)` from `relayOverrides` in `parseDaemonArgs`. The old test still passes.
- Fix:
  - The test now resolves `cfg.RelayOverrides` the way `RelayManager.Reload` does (`relaysettings.Resolve`). It asserts the value and source of both audiences: env `GRAIN_RELAY_IAP_JWT_AUDIENCE` / `GRAIN_RELAY_AUDIENCE`, then the flags over the env and over a stored setting.
  - The three dead fields (`PostgresURL`, `RelayAudience`, `RelayIAPJWTAudience`) and the dead env fallbacks that fed the latter two were removed.
  - Re-running the mutation now fails the test: `IAPJWTAudience = {Value: Source:}, want GRAIN_RELAY_IAP_JWT_AUDIENCE's value`.
- The plan's "existing coverage" names `cmd/grain/demo_test.go` for `-upgrade-image`. That test moved with the code and is now `pkg/demo/demo_test.go` (`TestUpgradeDemoListsTheRegistrysVersions`).

## 2. Image stamping

There is no `grain version` subcommand. The image defaults are printed by `grain sandbox-image`, `grain container-sandbox-image`, `grain k8s-sandbox-image` and `grain image`.

```
D=sha256:aaaa…(64)
make build SANDBOX_IMAGE=ghcr.io/bwsalmon/grain/guest:sha-dafd697@$D \
           K8S_SANDBOX_IMAGE=ghcr.io/bwsalmon/grain/k8s-sandbox:sha-dafd697@$D \
           GRAIN_IMAGE_REF=ghcr.io/bwsalmon/grain:sha-dafd697@$D
```

These are the same three variables `scripts/release-images.sh` passes to `make image`, which the Dockerfile turns into `make build`. The link line carried:

- `-X github.com/ai-on-gke/grain/pkg/daemon.DefaultSandboxImage=…`
- `-X main.defaultK8sSandboxImage=…` (still in `cmd/grain/sandboximage.go`; it did not move)
- `-X github.com/ai-on-gke/grain/pkg/daemon.DefaultGrainImage=…`

| Command | `bin/grain` (stamped) | `go run ./cmd/grain` (unstamped) |
|---|---|---|
| `sandbox-image` | `ghcr.io/bwsalmon/grain/guest:sha-dafd697@sha256:aaaa…` | `ghcr.io/bwsalmon/grain/guest:latest` |
| `container-sandbox-image` | `ghcr.io/bwsalmon/grain/k8s-sandbox:sha-dafd697@sha256:aaaa…` | `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` |
| `k8s-sandbox-image` | same as above | `ghcr.io/bwsalmon/grain/k8s-sandbox:latest` |
| `image` | `ghcr.io/bwsalmon/grain:sha-dafd697@sha256:aaaa…` | `ghcr.io/bwsalmon/grain/grain:latest` |

Pass: every stamp lands, and an unstamped build reports the fallbacks.

## 3. `grain demo -upgrade-image` in the browser

```
docker run -d --name reg -p 127.0.0.1:5000:5000 registry:2
# image FROM scratch with bin/grain as entrypoint, tagged and pushed as
#   main candidate latest v1.7.0 grain-task-1   (local copies then removed)
./bin/grain demo -addr 127.0.0.1:8431 -open=false -upgrade-image=127.0.0.1:5000/grain
cp docs/test-runs/2026-10-07-daemon-demo-move/upgrade-walkthrough.mjs ui/ && cd ui && \
  PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright DEMO_URL=http://127.0.0.1:8431 \
  STATUS_FILE=/tmp/grain-demo-*/upgrade-status.json node upgrade-walkthrough.mjs
```

The registry's tag list was `["v1.7.0","main","candidate","grain-task-1","latest"]`.

Outcomes, through Admin → Upgrade in Chromium:

- **Picker.** It offers `latest:enabled candidate:enabled main:enabled v1.7.0:enabled`.
  - The helper text reads "… this deployment only upgrades to main, candidate, latest, v[0-9]*.[0-9]*.[0-9]* (-upgrade-allowed-branches)".
- **`grain-task-1` is not listed at all, rather than listed greyed.** The plan expected it greyed. This is by design (`pkg/upgrade/targets.go`):
  - The selector lists only the standard channels, released `vX.Y.Z` versions and literal allow-list entries.
  - It greys an entry only when it is one of those and not allowed. The other `-upgrade-allowed-branches=main` demo above shows that: `latest`, `candidate` and `v1.7.0` come back `allowed:false`.
  - A task tag is neither, so it is reachable only by typing it, and the gate refuses it.
  - Typing `grain-task-1` and clicking Upgrade → Confirm sent `{"branch":"grain-task-1","afterTasksFinish":false}`. The answer was 400 `branch is not one this deployment may upgrade to: "grain-task-1" matches none of main, candidate, latest, v[0-9]*.[0-9]*.[0-9]* (set with -upgrade-allowed-branches)`, which the pane shows as an alert.
- **`v1.7.0` upgrade.** The POST returned 202. The status progression was read from `GET /api/upgrade` (polled every 100 ms) and from the demo's `upgrade-status.json`; the two agree.
  1. `running | step 1 of 7: pull image`
  2. `running | step 3 of 7: health check` (step 2, signature: skipped, "no signature verification configured (-upgrade-verify-cmd)")
  3. `running | step 7 of 7: record image` (unit flags, sandbox image and schema: skipped, as `pkg/demo/demo_upgrade.go` documents)
  4. `ok | pulled 127.0.0.1:5000/grain:v1.7.0@sha256:8d05941d396a4fd972f1ac1698b95b56a2c8c6053621065ad1f69201e264a970`

  The UI's status list ended on `ok` with the same detail.
- **No restart:**
  - The demo's PID was the same before and after (11435).
  - The page loaded once.
  - The only "restart" in the demo log is its startup line "… and restart nothing".
  - The ref file `grain-image.env` now holds `GRAIN_IMAGE=127.0.0.1:5000/grain:v1.7.0@sha256:8d05…`.
  - The log ends "the upgrade to v1.7.0 is over -- the pipeline is picking work up again" (the hold was lifted).

Observations, not filed as bugs:

- **No ref file before the first upgrade.**
  - Until an upgrade has run, the demo has no `grain-image.env`.
  - Each Upgrade-tab visit logs `ui: reporting what this deployment is running: … open …/grain-image.env: no such file or directory`, and the pane shows no "Now running" line.
  - A deployment always has that file (scripts/setup.sh writes it), so this only affects the demo.
- **Restart wording in the confirm dialog.**
  - The confirm dialog ("2 runs are in flight. Upgrading restarts this deployment, which discards them …") appears before any branch is checked, including the refused `grain-task-1`.
  - In the demo the restart it describes never happens.
  - This is copy fidelity in the demo only.

## 4. Metrics: one failing input, every other section answering

New test: `TestDaemonMetricsEverySectionSurvivesAnotherSectionsFailingInput` (`cmd/grain/metrics_test.go`).

- **Setup.** It runs against a real `pkg/ui` server over SQLite and breaks one store read at a time, for real (`ALTER TABLE … RENAME`):
  - `task_run.prompt` → `prompts`
  - `task_run_check_wait` → `checks`
  - `ci_job` → `ci_queueing`
  - `task_run.transcript` → `merges`
- **Per section.** For every `mcp.MetricsSections` entry it asserts:
  - `GET /api/metrics?section=` answers 200 with nothing in `Failed`, through plain HTTP.
  - `read_grain_metrics` (`daemon.DaemonMetrics` over `ui.NewHTTPClient`) answers without error.
  - The broken section is a 500 / tool error.
- **Whole report.** It marks exactly one section `unavailable -- internal server error (ref …)`.
- **Mutation check.** Adding `metricsPrompts` to `throughput`'s inputs fails it ("section=throughput: status = 500 …").
- **Result.** It passes on this base. The existing tests (`TestMetricsSurvivesAFailingPromptRead`, `TestDaemonMetricsSurvivesAFailingPromptRead`) checked only `throughput` against a broken prompt read.

Against the demo:

- `./bin/grain -server http://127.0.0.1:8431 metrics -window 30d` exits 0.
- It renders throughput, capacity, latency, settings changes ("no settings history to read …") and backlog. No section reads `unavailable`.
- The renderer leaves out sections that have nothing to report. The demo has no daemon cycles, CI jobs or sandboxes, for example.
- `curl '…/api/metrics?window=30d&section=<s>'` returned 200 for all 14 sections:
  - throughput, capacity, cycles, latency, tools, prompts, checks
  - ci_queueing, host, changes, pull_requests, merges, sandboxes, backlog

## Fixes in this change

1. `pkg/daemon/config.go`, `cmd/grain/daemon.go`: drop the dead `PostgresURL`, `RelayAudience` and `RelayIAPJWTAudience` fields and the dead env fallbacks feeding the latter two.
2. `cmd/grain/daemon_relay_test.go`: `TestParseDaemonArgsRelayIAP` asserts on what the relay client is given (`RelayOverrides`), for both audiences, from env and flag.
3. `cmd/grain/metrics_test.go`: the per-section failing-input test above.
4. `docs/test-runs/2026-10-07-daemon-demo-move/upgrade-walkthrough.mjs`: the Playwright walkthrough, kept to reproduce §3. It is not part of the e2e suite, because it needs docker and a local registry.

## CI verdict

Grain CI's full suite (`wait_for_checks`, no job filter) is the gate. The verdict is reported in the pull request's conversation, because this report is committed before that run finishes.
