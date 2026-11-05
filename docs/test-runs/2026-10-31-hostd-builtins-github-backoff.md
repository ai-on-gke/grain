# hostd built-in entries on upgrade, Health's narrowed missing-entry list, and GitHub idle-poll backoff

- **Plan:** `docs/test-plans/2026-10-30.md` §8 (PRs #207 `c59df948`/`afaa3068`/`c631576a`, #133 `f023f448`, #172 `b0ce9495`)
- **Date:** 2026-10-31, 21:15–21:45 UTC
- **Task:** [Task #2017](/tasks/2017), branch `grain/049f1e09/task-2017` on `ai-on-gke/grain`, base `main` at `7c652762` ("Merge pull request #297"). Origin: [Task #1899](/tasks/1899).
- **Environment:**
  - Debian 12 sandbox (`kubernetes-granule`), `linux/amd64`, running as uid 1000 (`debian`) with passwordless sudo.
  - Go `go1.26.10`, Node `v20.19.0`, Playwright Chromium from `/var/cache/playwright`.
  - `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`. Both were rebuilt from this branch for the after walk.
  - There was no Gemini key, so `LOOK=1` was not used. Pages were checked with Playwright DOM text, bounding-box reads and computed styles. Ad-hoc walk scripts were kept outside the repository, in `/var/tmp/w8`.

## Processes and exact commands

| Process | Command |
|---|---|
| Daemon (real, separate process) | `grain daemon -data-dir /var/tmp/w8/data -ui-addr 127.0.0.1:18090 -sandbox-port-base=0 -as alice -agy-path /bin/true -gemini-api-key-file /var/tmp/w8/gem.key -github-host 127.0.0.1:9 -github-insecure-http -host-sandboxes -sandbox-dir /var/tmp/w8/sb -hostd-token-file /var/tmp/w8/hostd.token` |
| hostd (real, dialing in over TCP) | `/var/tmp/w8/lib/current/grain hostd -server http://127.0.0.1:18090 -token-file hostd.token -socket= -allowlist hostd.yaml -state-dir /var/tmp/w8/hstate` |
| hostd's install layout | `/var/tmp/w8/lib/versions/v1/grain` (`root:root`, `0755`, the build under test), `/var/tmp/w8/lib/current -> versions/v1`, as the installer lays out `/usr/local/lib/grain/hostd`. hostd resolves `current` to `versions/v1/grain` (`hostdSelf`). |
| Upgrade step | `sudo /var/tmp/w8/lib/current/grain hostd -ensure-builtins -allowlist /var/tmp/w8/hostd.yaml -command /var/tmp/w8/lib/current/grain` (what `grain-hostd-allowlist.service` runs) |
| Demo | `GITHUB_WEBHOOK_SECRET=walksecret grain demo -github-sim -reconcile 2s -github-webhook-addr 127.0.0.1:18301 -addr 127.0.0.1:18300 -open=false -access-code-file /var/tmp/d8/code` |
| Webhooks | `POST http://127.0.0.1:18301/webhooks/github`, with `X-GitHub-Event: pull_request`, a fresh `X-GitHub-Delivery` and `X-Hub-Signature-256` (HMAC-SHA256 of the body with `walksecret`, via `openssl dgst`). The body was `{"action":"synchronize","number":N,"pull_request":{"number":N,…},"repository":{"full_name":"acme/widgets",…}}`. |

The "old" allowlist (`/var/tmp/w8/hostd.yaml`, `root:root 0644`) is a pre-#207 install's: `host-git` and `grain-host-script` as the installer wrote them, and `grain-gcp` edited by the operator to `command: /bin/echo`, `args: [operator-edited-gcp]` (no `pass_env`). It has **no `grain-gchat`**. No external boundary was stubbed for the hostd half. The gchat webhook URL is a placeholder `https://chat.googleapis.com/...` that is never posted to. The capability test lists methods and posts nothing.

## Outcome

| # | Item ("What is NOT proved") | Outcome |
|---|---|---|
| 1a | Old allowlist lacking `grain-gchat`, gchat unconfigured: no Health error | **Pass.** With the binary root-owned `0755`, hostd logs `offering grain's own entries grain-gchat, which the allowlist lacks, as /var/tmp/w8/lib/versions/v1/grain`, and offers it. `hostd.missing` is empty. With the binary `0775` (nothing offered), gchat set to `run: host` but unconfigured still leaves `hostd.missing: []`. `/system/health` in Chromium at 1440 and 390 px has no hostd/gchat/allowlist line. |
| 1b | Configure gchat: entry offered and works | **Pass.** With gchat configured (`grain_url`, one webhook space) and the binary `0755`, `POST /api/capability-servers/test` answered `ok: true, methods: [post]`. The server went `failed` → `attached, 1 method(s)` on the daemon's next attach retry, about 60 s after hostd's restart, with no operator action. hostd lists process `grain-gchat` and the host runs `/var/tmp/w8/lib/versions/v1/grain capability gchat`, the resolved version and not the `current` link. |
| 1b′ | Configured gchat while hostd cannot offer it | **Pass (Health narrows correctly).** With the binary `0775`, configuring gchat makes `hostd.missing: ["grain-gchat"]`. Health shows the "grain hostd's allowlist on the host has no capabilities entry grain-gchat … paste this under capabilities:" note at both widths, with 0 px overflow and no page errors. Unconfiguring gchat again clears it. |
| 1c | Operator-edited entry not overwritten | **Pass.** hostd's offers kept the operator's `grain-gcp`, and nothing was offered over it. `-ensure-builtins` printed `added grain-gchat` and `kept grain-gcp: command "/bin/echo" … as you have it; it differs from grain's default`. The file diff is only the added `grain-gchat` block, and the file stays `root:root 0644`. |
| 1d | Group-writable hostd binary disables offering | **Pass, with a fix.** With `versions/v1/grain` at `0775`, the offers are only the allowlist's 3 entries, and the log says why. The log's remedy was "make it 0644 or tighter", which for the binary would make it unexecutable. **Fixed here** (see below). |
| 2a | Backoff over several reconcile cycles | **Pass.** Sampled `/api/metrics` every 1 s from the demo's start: `tracked 3, backedOff 2`. Median and max went 60 s (21:29:21) → 120 s (21:29:25) → 240 s (21:29:32) → 480 s, the cap (21:29:49), and stayed there. `backedOff` is 2 from the first sample on, not rising: the demo has 3 polled PRs and #101 is critical (always at base), so 2 is its ceiling and the first cycle already reaches it. |
| 2b | Webhook resets the PR's interval | **Pass.** A signed `pull_request` webhook for `acme/widgets#102` was answered `202 {"accepted":true}`. Within 1 s the metrics read `backedOff 1, median 30s, max 480s`, then 60 s and 120 s, then back up. A webhook for `#140`, a PR the demo's poller does not track, was accepted and changed nothing, as expected. |
| 2c | UI, `/api/metrics` and `grain metrics` agree | **Pass.** At the cap, `/api/metrics` read `{"tracked":3,"backedOff":2,"baseSeconds":30,"capSeconds":480,"medianSeconds":480,"maxSeconds":480}`. `grain metrics` printed `poll schedule 2 of 3 PR(s) backed off (median 8m0s, max 8m0s; 30s..8m0s)`. Health printed "Idle-poll backoff: 2 of 3 tracked PRs backed off · poll interval median 8m 0s, max 8m 0s (30s base, 8m 0s cap).", and the page's own `fetch('/api/metrics')` in the same step returned the same object. |
| 2d | Watching Health live | **Bug, fixed here.** An open `/system/health` never changed: one `GET /api/metrics?section=github` at load, and the backoff, calls left and projection stayed frozen while the sandbox section below them re-read every 5 s. After the fix the page re-reads every 10 s. A webhook sent at 21:36:36 showed on the open page at 21:36:42 ("median 8m 0s" → "median 1m 0s"). |
| 3 | Backoff line's phone layout, 390 px, light and dark | **Pass.** The line is inside the panel's box (x 62–362 in a 366 px panel). It wraps to 3 lines (60 px, 20 px line height), with `scrollWidth == clientWidth` (300), 0 px page overflow and no page errors. At 1440 px it is one line (686 px). Its text is `rgb(74, 51, 29)` in light and `rgb(245, 222, 198)` in dark, over the Alert's tinted background. Before and after are identical in layout. |

## Steps and observations

### 1. hostd with an old allowlist

1. Started the daemon, then hostd through `lib/current`. `GET /api/sandboxes` → `hostExecutor.hostd`: `reachable: true`, offers `capability grain-gchat, grain-gcp, grain-host-script, host-git`, no `missing`.
2. Set `versions/v1/grain` to `0775` and restarted hostd. Log: `offering only the allowlist's own entries, not grain's built-in ones it may lack: grain hostd's own binary /var/tmp/w8/lib/versions/v1/grain is writable by group or others (mode -rwxrwxr-x); make it 0644 or tighter`. Offers: 3, no `grain-gchat`. `missing: []`.
3. `PUT /api/capability-servers` with `{"id":"https://grain.dev/capabilities/gchat","config":{"command":"grain-gchat","run":"host","builtin":"gchat"}}` → `needs-config`. `missing: []`: Health stays quiet (`OnlyNeeded` skips a `NeedsConfig` built-in with no config).
4. The same entry with `config: {grain_url, spaces:[{name:"walk",transport:"webhook"}]}` and `configSecrets.webhook_urls` → `missing: ["grain-gchat"]`. The server's own error carries `MissingEntryHelp`'s text. Health shows the note (§ table, 1b′).
5. Back to `0755`, restarted hostd → `grain-gchat` offered implicitly, the test probe passes, and the server attaches on its own about 60 s later.
6. Ran `-ensure-builtins` as root: only `grain-gchat` was added, and the three existing entries were "kept".

**Observations, no change made:**
- While gchat is unconfigured and set to `run: host` against a hostd that cannot offer it, Settings → Capabilities' status for it still carries the "allowlist offers no capability entry grain-gchat" error beside `needs-config`. Health says nothing, as #207 intends. The Settings text is accurate, just early.
- The daemon's help names `/etc/grain/hostd.yaml` (`DefaultAllowlistPath`), even for a hostd started with another `-allowlist`. The daemon does not know hostd's path. That is right for every installed hostd and only differs for a hand-run one like this walk's.
- A configured server that failed to attach for want of the entry is not re-attached when hostd reconnects with the entry. It waits for the daemon's next attach retry, about 60 s here.

### 2. GitHub idle-poll backoff in the demo

The demo's simulated poll clock advances `PollBase` (30 s) per 2 s reconcile, so the 480 s cap is reached about 30 s after start. `tracked` counts PRs (subjects), not PR × aspect keys. The median of the sorted intervals `[30, 480, 480]` is 480, which is why median equals max at the cap.

### 3. Screenshots

`/system/health` was shot at desktop (1440) and phone (390), light and dark, before and after the fix:
- Full page: `e2e/screenshot.mjs --url http://127.0.0.1:18300 --pages /system/health`.
- Panel crops: `locator('[data-testid=health-github-budget]').screenshot()`.

The before and after crops differ only in the live figures.

## Fixes in this branch

1. **`fix(ui)`: `ui/src/components/system/GitHubBudgetPanel.jsx`.** The panel re-reads `/api/metrics?section=github` every `BUDGET_REFRESH_MS` (10 s), one read at a time after the last one came back, as `SandboxHealthPage` does. A failed read keeps the last figures. Regression test: `GitHubBudgetPanel.test.jsx` "re-reads the budget while it is open". It fails on the old panel ("expected to be called 2 times, but got 1 times").
2. **`fix(hostd)`: `pkg/hostd/owner_unix.go`.** The group/other-writable refusal now says `remove that with chmod go-w <path>` rather than "make it 0644 or tighter", which is right for both the allowlist and hostd's own binary. The check itself is unchanged. **Trust boundary:** `pkg/hostd` is touched (message only). `TestHostdOffersNoBuiltinsWhenItsBinaryIsWritableByOthers` asserts the new remedy.

## Not exercised

- **A real `setup.sh` upgrade with `grain-hostd-allowlist.service` under systemd.** The `-ensure-builtins` step it runs was run by hand as root against the same layout. `tests/deploy/hostd_builtins_test.go` covers the unit.
- **A gchat post to a real Chat space.** There was no Chat webhook or credential. The capability's test call (initialize + list) ran on the host through hostd, and nothing was posted.
