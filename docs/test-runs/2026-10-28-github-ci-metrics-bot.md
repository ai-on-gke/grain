# GitHub CI metrics, shared-fault correlation, API budget and the GitHub App "take over" bot

- **Plan:** `docs/test-plans/2026-10-28.md` §7 (retired `bwsalmon/grain`), restated in task 1720
- **Date:** 2026-10-29, 00:29–01:20 UTC
- **Task:** `grain/049f1e09/task-1720` on `ai-on-gke/grain`, base `main` at `0dc554f` (origin: task 1549)
- **Environment:**
  - Debian 12 sandbox (`kubernetes-granule`), kernel `6.1.0-54-amd64`, `linux/amd64`
  - Go `go1.26.10`, Node `v20.19.0`, Playwright Chromium from `/var/cache/playwright`
  - `grain` and `github-webhook` built from this branch (`go build ./cmd/grain ./cmd/github-webhook`), with `ui/` built first (`npm ci && npm run build`) so the demo embeds the real UI
  - `grain demo -github-sim -fake-ci -reconcile 2s -access-code-file …` on `127.0.0.1:7801`; githubsim (`pkg/github/githubsim`) is the only stub
  - `github-webhook serve -addr 127.0.0.1:7811 -store …/events.jsonl` with `GITHUB_WEBHOOK_SECRET=test-secret`, `GITHUB_WEBHOOK_RELAY_SECRET=relay-secret`

## Outcome

| Step | Result |
|---|---|
| 1. Signed check-run / workflow-job webhooks; `/api/metrics`, `grain metrics`, Metrics pane agree | **Pass after a fix.** All three agree on every number. One rendering bug fixed (F1). The demo has no webhook receiver, so raw webhooks were driven through the real gateway into the store by a new end-to-end test (F2). |
| 2. GitHub failure beside grain CI; shared-fault box labelled by source; fold, reload, still folded (desktop + phone) | **Pass.** The source chip reads "GitHub CI". The box stays folded across a reload at 1280 and 390 px. The phone case is now in the spec. |
| 3. `cmd/github-webhook` with a test secret; "@<slug> take over" files a shepherd, review first; unsigned / wrong-secret refused | **Pass.** The real binary returns 401 for unsigned, wrong-secret and SHA-1-only deliveries and for an unsigned relay read. Through gateway → relay → reconciler, a take-over comment files a shepherd that waits on its review, and the merge stays held. |
| 4. Idle PRs over several sync cycles; Health shows per-caller spend and poll intervals lengthen | **Partly observable.** Health shows per-caller spend, and the numbers add up. It shows **no poll intervals**, and the demo's GitHub hour is fixed (`demoGitHubCalls`). The backoff itself passes its orchestrator tests over githubsim (F3, follow-up proposed). |

## Steps and observations

### 1. GitHub CI metrics

1. `GET /api/metrics` → `githubCI`: `source: github`, 19 jobs, 9 passed / 7 failed / 1 cancelled / 1 skipped / 1 neutral. Pass rate 9/17 = 53%, fail rate 41%, cancel rate 6%. Seven `byJob` rows; CI / test flaky on 1 commit; time to green p50 1h with 4 still red.
2. `GRAIN_SERVER=… GRAIN_TOKEN=<access code> grain metrics` prints the same figures: `53% / 41% / 6% (9 / 7 / 1)`, `1 skipped, 1 neutral`, the same seven rows in the same order, and the same flake and base-branch lines.
3. The Metrics pane, in Chromium at 1280 and 390 px, shows the same headline stats and the same table. Neither width scrolls the page sideways.
4. **F1:** in both the pane and the CLI, "Deploy / release" (1 skipped) and "codecov/patch" (1 neutral) showed `0% 0% 0%` for pass / fail / cancel, as if they had decided runs. The API is right (`passRate: 0` with zero decided). The renderers ignored that nothing was decided. Fixed; see below.
5. Webhooks into a live process:
   - `grain demo` has no webhook ingest path (`-github-webhook-url` / `-github-webhook-addr` are daemon-only), so a webhook posted at a demo goes nowhere.
   - I ran the real `cmd/github-webhook` binary and POSTed raw GitHub-shaped `workflow_job` (with failing step `go test`), `check_run` (codecov, with output) and `issue_comment` payloads, signed with HMAC-SHA256. Each got `202 {"accepted":true,…,"seq":N}`.
   - To get from there to the store and metrics, I added `TestGitHubWebhookGatewayRelayToMetricsAndTakeover` (F2). It POSTs raw payloads to the same `ghwebhook.Server` the binary serves, reads them back over the signed relay with `ghwebhook.Client` (the daemon's `-github-webhook-url` path), reconciles them over githubsim, and checks the recorded jobs and `metrics.Compute`. Results:
     - Three jobs are recorded. The Actions job's `check_run` twin is the same job, not a second one.
     - A redelivery is deduplicated at the gateway, so the cursor ends at 5.
     - Workflow, failing step, ref, commit and `via: webhook` survive from the raw payload.
     - The third-party check keeps its output as its failure tail.
     - Metrics read 3 jobs, 1 passed and 2 failed, with source `github`.

### 2. Shared-fault box

- `GET /api/ci/faults` serves the seeded GitHub fault: `source: github`, CI / e2e, step `Run go test ./...`, on 3 branches, common file `pkg/auth/session.go`.
- Correlation is strictly within one source (`pkg/ci/sharedfault.go` `FaultSource*`). A grain-CI failure with the same text as a GitHub failure stays a separate card under its own label. `TestGitHubSharedFaultDoesNotJoinDifferentFailuresOrGrainCI` covers this over githubsim. `-fake-ci` runs nothing, so it cannot produce a real grain-CI failure in the demo.
- In the browser:
  - `/ci` shows the card with its "GitHub CI" chip.
  - Clicking the header sets `aria-expanded="false"`, and after `page.reload()` it still reads `false`, at both 1280 and 390 px.
  - With the box collapsed and nothing seen, the header counts the fault as "1 new".
- `ui/e2e/ci-faults-github.spec.js` checked fold-and-reload only on a desktop. It now runs at both widths, and all 8 tests pass.

### 3. GitHub App bot

The real `github-webhook` binary, with `GITHUB_WEBHOOK_SECRET=test-secret`:

| Delivery | Status |
|---|---|
| `workflow_job`, signed | 202 |
| `workflow_job`, unsigned | 401 (`missing signature`) |
| `workflow_job`, signed with another secret | 401 (`signature does not match`) |
| `workflow_job`, `X-Hub-Signature` sha1 only | 401 |
| `check_run`, signed | 202 |
| `issue_comment` "@grain-bot take over", signed | 202 |
| `issue_comment`, wrong secret | 401 |
| `GET /v1/events` without a relay signature | 401 |

The end-to-end test then carries the signed take-over comment through the relay into `reconcileGitHubEvents`, with `grain-bot[bot]` as the App identity and alice holding write on githubsim:

- One shepherd task with a `takes-over` link to the pull request.
- Its reviews filed, and the shepherd `depends_on` every one of them, so review comes first.
- `MergeQueueReviewPending` holds the merge.
- The refused unsigned and wrong-secret copies filed nothing.
- A reader signing with the wrong relay secret is refused.

The deeper takeover behaviour (security review for non-collaborators, head moves, `release`, forks, label trigger) is already covered by `pkg/orchestrator/takeover_test.go`.

### 4. API budget and idle backoff

- System → Health's GitHub budget panel (`github-budget-panel.spec.js` passes at both widths) shows "1,650 of 5,000 calls left this hour", a "runs out in" projection, and per-caller rows.
- In `/api/metrics` → `github`, the per-caller `sent` values sum to 5,210 and `spent` to 1,680, matching the totals. `spendPerHour` 3,300 against 1,650 remaining gives `exhaustedAt` 30 minutes out, as shown.
- **F3:** two things stop the plan's "poll intervals lengthen" check:
  - The demo's GitHub figures are a fixed hour (`cfg.GitHubCalls = demoGitHubCalls{}`), so leaving the demo's PRs idle changes nothing on Health.
  - Nothing exports `github.PollSchedule`'s intervals (`PollBase` 30s doubling to `PollCap` 8m), so Health has nothing to draw.

  The backoff itself passes `TestAnIdlePullRequestBacksOffAndResetsOnAHeadChange`, `TestAPullRequestWithCIInFlightIsReadEveryCycle` and `TestTheQueueHeadIsReadEveryCycleUnderALowBudget` (and `pkg/github`'s poll and budget tests). I proposed a follow-up rather than building it here.

## Findings and fixes

- **F1 (fixed): a job with nothing decided showed 0% rates.**
  - Fix: `pkg/daemon/metrics.go` (`decidedPercent`, used by `grain metrics` and `read_grain_metrics`) and `ui/src/components/MetricsSections.jsx` (`decidedPercent`) print a dash when passed + failed + cancelled is 0. `/api/metrics` is unchanged.
  - Regression tests: `TestRenderMetricsGitHubCIJobWithNothingDecidedHasNoRates` (`pkg/cli`) and "draws no rates for a GitHub CI job with nothing decided" (`ReportBoard.sections.test.jsx`). Both fail without the fix.
  - Screenshots: `/metrics` at desktop and phone, light and dark, before and after. The two rows now read `— — —`, the other rows are unchanged, and there is no overflow.
- **F2 (test added): no test drove the bot from raw signed webhooks across the gateway and relay.** `pkg/orchestrator/ghwebhook_relay_test.go`. No bug was found on that path.
- **F3 (follow-up): idle-poll backoff is not observable.** It is invisible on System → Health, and `grain demo` can neither receive webhooks nor make budgeted GitHub calls. Proposed as its own task.

## Re-running this

```sh
cd ui && npm ci && npm run build && cd ..
go build -o bin/grain ./cmd/grain && go build -o bin/github-webhook ./cmd/github-webhook
bin/grain demo -github-sim -fake-ci -open=false -addr 127.0.0.1:7801 -access-code-file /tmp/code -reconcile 2s &
GITHUB_WEBHOOK_SECRET=test-secret GITHUB_WEBHOOK_RELAY_SECRET=relay-secret \
  bin/github-webhook serve -addr 127.0.0.1:7811 -store /tmp/events.jsonl &
go test -run 'TestGitHubWebhookGatewayRelayToMetricsAndTakeover|Takeover|GitHubCI|IdlePullRequest' ./pkg/orchestrator
go test -run 'TestRenderMetrics.*GitHubCI' ./pkg/cli
(cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test --project=chromium \
  e2e/ci-faults-github.spec.js e2e/metrics-github-ci.spec.js e2e/github-budget-panel.spec.js)
```

Without `npm run build` the binary embeds no UI, and every Playwright spec fails on a missing test id.
