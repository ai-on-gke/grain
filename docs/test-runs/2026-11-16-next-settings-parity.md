# Walkthrough: `/next/settings` parity with classic — Capabilities, Hub/Sandboxes/Engine/CI, Git, Hosts, Codex login

- **Test plan:** `docs/test-plans/2026-11-16.md` §9 (filed by [Task #6330](/tasks/6330), executed by [Task #6482](/tasks/6482))
- **Base commit walked:** `a771a9973` (`main`)
- **Date:** 2026-11-17
- **Branch:** `grain/53549e42/task-6482-walkthrough-next-settings`

## Environment

- **Binaries:** built from HEAD (`a771a9973`), then rebuilt after fixes (`make frontend`, `CGO_ENABLED=0 go build ./cmd/grain`, `CGO_ENABLED=0 go build ./cmd/grain-agentd`).
- **Primary database:** PostgreSQL 15.20 (`postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable`).
- **Secondary database (spot check):** SQLite 3.40.1 (Item 1 parity suite).
- **Daemon flags:** real `grain daemon -sandbox-port-base=0` with real `pkg/testutil/githubsim` (`127.0.0.1:4284`), local authenticated HTTPS + SSH git forges (`docs/test-runs/2026-11-16-next-settings-parity/gitforges.go` on `127.0.0.1:4294` HTTPS and `127.0.0.1:4295` SSH), real `grain-agentd` binary for host enrollment, and fake agent CLI (`fake-codex.py` reporting `codex-cli 0.169.0` + `codex debug models` + reporting live workspace/auth observations back through MCP `run_command` and `comment_on_issue`).
- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900`, `2048×1152`, and `390×844` in light and dark themes.
- **Harness scripts:** [`docs/test-runs/2026-11-16-next-settings-parity/`](2026-11-16-next-settings-parity/) (`env.sh`, `gitforges.go`, `fake-codex.py`, `walk_item1_parity.mjs`, `walk_item2_git.mjs`, `walk_item3_hosts.mjs`, `walk_item4_codex.mjs`, `walk_item5_6_links_mobile.mjs`).

## Summary of Outcomes

| §9 Item | Scope | Result (PG) | Result (SQLite) | Notes |
|---|---|---|---|---|
| **Item 1** | Bidirectional save parity across `/next/settings/` (Engine, CI, Sandboxes, Hub, Capabilities, Models/Connections, PR #1587 nav/session, PR #1602 confirm clear) vs classic `/settings/*`, `grain` CLI, and `grain state sync` | 24/27 initial → **Pass after Fixes #2 & #3** | 24/27 identical to PG | Found Bug #2 (`POST /api/sandbox-probe` HTTP 500) and Bug #3 (missing `sandboxDiskOvercommit` & `sandboxMemoryReserveMb` on `/next/settings/sandboxes`). Zero secrets echoed anywhere. |
| **Item 2** | Git connections: HTTPS PAT + SSH `known_hosts` real git-proxy clones, in-use removal, typo'd credential HTTP 400 | HTTPS & typo 400 Pass; SSH failed initially → **Pass after Fix #1** | — | Found Bug #1 (`pkg/gitproxy/sshkey.go` `sshAddr` dropped `:port` from `host`). After Fix #1, dispatched task #26 cloned `127.0.0.1:4295/acme/ssh-repo` over authenticated SSH through the git proxy (`gitforges-audit.jsonl` recorded `proto: "ssh", authOk: true`, `MARKER.txt = walkthrough-ssh-repo-marker`). |
| **Item 3** | Hosts: Invite hosts snippet + custom code + `/hostd/install`, real `grain-agentd -join` + pending accept + live connect, Delete host report (single + bulk `Delete revoked` with usage-limit pauses & in-use skip) | **5/5 Pass** | — | Single Delete refused (`400`) while referenced by `walk-host-ref-conn`; after clearing reference, Delete (`200`) dropped DB pause `1 -> 0` and reported `walk-conn@hostd/walk-host-2 (claude-sonnet-4-6)`. Bulk `Delete revoked` deleted `walk-host-prune` (reporting cleared pause `prune-conn@hostd/walk-host-prune (gemini-2.5-pro)`) and skipped `walk-host-skip`. Zero `hostd` UI prose. |
| **Item 4** | Per-connection Codex `auth.json` upload (invalid JSON rejection, `0600` store, never echoed), dispatched tasks proving per-connection isolation (`codex-a` gets symlinked `auth.json`, `codex-b` gets none), confirm-before-clear | **Pass** | — | Task #22 on `codex-a` reported `auth_exists: true`, `auth_is_symlink: true`, `auth_account: "acct-walk-codex-a"`, `openai_api_key_set: false`. Task #23 on `codex-b` and post-clear task #24 on `codex-a` both refused with `this connection has no Codex credential of its own`. |
| **Item 5** | Retirement (`Framework API keys` card removed, zero `/api/agent-keys` UI fetches, `git-push-any-ref` retired helper), `/t/<team>/` redirects, every `/settings/` link in `docs/*.md` | Found 3 broken docs deep links → **Pass after Fix #4** | — | Found Bug #4: `/next/settings/capabilities/<id>` fell through to `/next/inbox`; `/next/settings/agents#external-agents` stayed on Agents instead of Access; `/next/settings/access#allowed-principals` lacked target `#allowed-principals`. |
| **Item 6** | All 18 `/next/settings/*` pages × light & dark (`36` views) at `390×844` with long values | **36/36 Pass** | — | Zero horizontal overflow (`scrollWidth <= clientWidth`), zero uncaught page errors, zero console errors. |

## Detailed Steps & Observations

### Item 1 — Bidirectional Parity (PostgreSQL & SQLite)

Executed `walk_item1_parity.mjs` against both PostgreSQL (`grain_walk`) and SQLite:

- **Engine (`maxWorkers`, `pollInterval`, `maxMergesPerRepo`):** Saved in `/next/settings/engine` → matched `GET /api/settings`, classic `/settings/engine`, `grain settings`, and `grain state sync` (`tables/grain_config.json`). Reverse update via CLI reflected cleanly in `/next/`. Blank cleared `maxWorkers` and saved grain's default (`3`).
- **CI (`ciJobsPerRun`, `ciRunHistory`, `ciCacheMb`, `ciAgeBoost`):** Bidirectional parity across `/next/`, classic, CLI, and `state-repo`; blank reset `ciCacheMb` to `0` (`grain`'s default).
- **Sandboxes (`sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`, `sandboxCpuOvercommit`, `sandboxDiskFloorMinMb`, plus `sandboxDiskOvercommit` & `sandboxMemoryReserveMb` after Fix #3):** Clearing `sandboxCpuOvercommit` and blurring saved grain's default (`2`); typing `0` stayed `0` with the unbounded warning. `GET /api/settings` reported `sandboxProbe: false` on `-sandbox-port-base=0`, hiding the Probe button in `/next/settings/sandboxes`; direct `POST /api/sandbox-probe` initially returned HTTP 500 (Bug #2) and returned HTTP 501 after Fix #2.
- **Hub (`hub.url`, `hub.entryName`, `hub.purpose`, `hub.hostKind`, `hub.listed`, scope rules):** Bidirectional parity verified across `/next/settings/registry`, classic `/settings/hub`, CLI, and `state-repo`.
- **Capabilities (`secrets`, `credentials`, custom API scope set `walk/reader`):** Stored secret `walk-sec/token` via `/next/settings/capabilities` — never echoed in `GET /api/secrets`, `grain secrets list`, `state-repo`, or DOM. Created custom API scope set `walk/reader` (`tasks:read`) and verified a dispatched task holding it could read tasks while write scopes were denied.
- **Models & Connections (PR #1602, PR #1587):**
  - Confirm dialog shown before clearing a stored connection secret in both `/next/settings/models` and classic `/settings/models` (`Cancel` preserved `set: true`; `Confirm` cleared it).
  - PR #1587: Customize navigation persisted across two tabs via `localStorage` `storage` events; idle console session `PUT /api/console-sessions/{id}` fired zero `POST /api/settings` requests.
- **Non-blocking parity observation:** Creating a Claude/Codex/Gemini connection in `/next/settings/models` with `"Provider default"` saves `authMode: ""`; `/next/` still lets operators set a secret on it, whereas classic `ModelBackendPage.jsx` hides the credential editor when `authMode === ""`.

### Item 2 — Git Connections (HTTPS & SSH Proxy Clones, In-Use Removal, Typo 400)

Executed `walk_item2_git.mjs` against real `gitforges.go` (authenticated HTTPS smart-HTTP on `127.0.0.1:4294`, authenticated SSH git server on `127.0.0.1:4295`):

1. **HTTPS PAT connection:** Added via `/next/settings/git` (`host: 127.0.0.1:4294`, `scope: acme/https-repo`). Verified in `/api/deployment`, classic `/settings/git`, and `state-repo`; token never echoed. Dispatched task #12 on `127.0.0.1:4294/acme/https-repo` cloned through the git proxy (`origin http://127.0.0.1:<proxy>/127.0.0.1:4294/acme/https-repo.git`), read `MARKER.txt = walkthrough-https-repo-marker`, and `gitforges-audit.jsonl` recorded `{proto: "https", authOk: true}` for both `info/refs` and `git-upload-pack`.
2. **SSH connection with `known_hosts`:** Initially failed with HTTP 400 `known_hosts pins no host key for 127.0.0.1` (Bug #1). After Fix #1 and rebuilding `grain`, adding the SSH connection (`127.0.0.1:4295`, `[127.0.0.1]:4295 ssh-ed25519 ...`) succeeded (`HTTP 200`, private key never echoed), and dispatched task #26 cloned `127.0.0.1:4295/acme/ssh-repo` through the git proxy (`origin http://127.0.0.1:<proxy>/127.0.0.1:4295/acme/ssh-repo.git`), reading `MARKER.txt = walkthrough-ssh-repo-marker` with `gitforges-audit.jsonl` recording `{proto: "ssh", authOk: true, command: "git-upload-pack 'acme/ssh-repo.git'"}`.
3. **Missing/typo'd credential name (PR #1575):** Saving a connection naming `typo-missing-cred-xyz` returned HTTP `400`, displayed `no stored credential named "typo-missing-cred-xyz": add it under Settings → Git or pick an existing one`, and left connection count unchanged (`3 -> 3`) with nothing written to API or `state-repo`.
4. **Removal confirmation:** Clicking Remove opened `Remove Git connection?` confirmation (`Cancel` kept the connection; `Remove` deleted it cleanly with `HTTP 200`).

### Item 3 — Hosts (Invite, Real `grain-agentd` Join, Delete Report)

Executed `walk_item3_hosts.mjs` on PostgreSQL:

1. **Invite hosts (PR #1596):** Verified default enrollment command snippet, Show/Hide code toggle, creating a custom enrollment code (`201 Created`), and fetching `/hostd/install` (`200 OK`, `#!/bin/sh`). Zero visible `hostd` wording in UI prose.
2. **Real second host join:** Ran `grain-agentd -join <code> -join-only` (via a local loopback proxy injecting `Authorization: Bearer <access-code>` because `githubsim` does not implement GitHub Device OAuth `/login/device/code`), enrolling an Ed25519 key-bound host request. Accepted the pending request as `walk-host-2` in `/next/settings/hostds`; `grain-agentd` connected live (`state: "connected"`, labels `{os: "linux", role: "walkthrough"}`, `capacity: 2`), matching classic `/settings/hostds`. Delete button remained disabled while host was not revoked.
3. **Delete host report (PR #1636):**
   - Seeded a `usage_limit_pause` row (`walk-conn@hostd/walk-host-2`, `claude-sonnet-4-6`) in PostgreSQL and referenced `walk-host-2` from model connection `walk-host-ref-conn`.
   - Revoked `walk-host-2`; Delete returned `400` while referenced. After clearing the reference, Delete returned `200`, DB pause count dropped `1 -> 0`, and `hostd-delete-notice` reported cleared pause `walk-conn@hostd/walk-host-2 (claude-sonnet-4-6)`.
   - Bulk **Delete revoked** (`POST /api/hostds/delete-revoked`) reported both deleted host `walk-host-prune` (with cleared pause `prune-conn@hostd/walk-host-prune (gemini-2.5-pro)`) and skipped host `walk-host-skip` (still referenced by `walk-host-ref-conn`).

### Item 4 — Per-Connection Codex ChatGPT `auth.json` Upload & Clear

Executed `walk_item4_codex.mjs` on PostgreSQL with two Codex connections (`codex-a`, `codex-b`) running `fake-codex.py`:

1. **Validation:** Invalid JSON was rejected both client-side (`codex-auth-error-codex-a`) and by direct `PUT /api/agent-backends/codex-a/codex-auth` (`HTTP 400`).
2. **Per-connection storage & non-echo:** Uploading a valid fake `auth.json` (with future-exp JWT + fresh `last_refresh`) on `codex-a` created `0600` `data/secrets/codex-auth-<hash>.json`, marked `codex-a` `set: true` and `codex-b` `set: false`, and never echoed tokens/account IDs in API, CLI, `state-repo`, or DOM.
3. **Dispatched task isolation:**
   - Task #22 (`codex-a`) ran `fake-codex.py` and reported `auth_exists: true`, `auth_is_symlink: true`, target `/tmp/walk-pg/data/secrets/codex-auth-3bcf6776….json`, `auth_account: "acct-walk-codex-a"`, `openai_api_key_set: false`.
   - Task #23 (`codex-b`) did not receive `codex-a`'s credential — daemon refused with `this connection has no Codex credential of its own: upload auth.json or set an OpenAI API key on it in Settings -> Models (/settings/models)`.
4. **Confirm before Clear:** Clicking Clear opened confirmation dialog (`Cancel` kept `set: true`; `Confirm` returned `200`, `set: false`). Subsequent task #24 on `codex-a` failed with the same `no Codex credential of its own` refusal.

### Item 5 & Item 6 — Retirement, Docs Links, `/t/<team>/`, and 390 px Mobile

- **Retired surfaces:** `Framework API keys` card is absent from `/next/settings/models`, and opening `/next/settings/models` fires zero `/api/agent-keys` requests while `GET /api/agent-keys` remains `200` for headless scripts. `git-push-any-ref` retired capability guidance is wired in `ui-ng/src/app/settings/capability-retired.ts`.
- **Legacy `/t/<team>/` URLs:** Redirect cleanly to team-less `/next/...` paths preserving fragments.
- **Docs `/settings/` links audit:** Audited all operator-doc `/settings/` URLs extracted from `docs/*.md`, uncovering the 3 broken deep links fixed in Fix #4.
- **390×844 light & dark with long values:** Seeded long environment names, hostnames, scope strings, and descriptions; visited all 18 `/next/settings/*` pages in both light and dark themes (36 views). All 36 passed with zero horizontal overflow, zero uncaught page errors, and zero console errors.

## Bugs Found, Reproductions & Fixes

### Bug #1 — `pkg/gitproxy/sshkey.go`: `sshAddr` drops non-22 port embedded in `host`
- **Reproduction:** In `/next/settings/git` (or classic `/settings/git`), add an SSH connection with `Host = 127.0.0.1:4295` and `known_hosts = [127.0.0.1]:4295 ssh-ed25519 ...` (neither UI exposes a separate SSH port input, so `SSHKeyFile.Port == 0`). `POST /api/deployment/git-connections` failed with `HTTP 400`: `known_hosts pins no host key for 127.0.0.1` because `sshAddr` called `hostName(host)` and unconditionally defaulted `port == 0` to `22`, contradicting its own doc comment. Line 200 also checked `f.Port == 0` instead of resolved port 22 before auto-filling port-22 `githubKnownHosts`.
- **Fix:** Updated `sshAddr` in `pkg/gitproxy/sshkey.go` to preserve any port carried in `host` via `net.SplitHostPort` when `port == 0`, validated out-of-range embedded ports in `parseSSHKeyFile`, and checked `knownhosts.Normalize(addr) == model.DefaultGitHost` before filling `githubKnownHosts`.
- **Regression test:** `TestSSHKeyPreservesPortCarriedInHost` in `pkg/gitproxy/sshkey_test.go` + live SSH git-proxy clone verification in Item 2.

### Bug #2 — `POST /api/sandbox-probe` returned HTTP 500 on non-granule backends and malformed payloads
- **Reproduction:** Against a running `grain daemon -sandbox-port-base=0`, send `POST /api/sandbox-probe` with `{"id":"walk-check","argv":["true"]}`. `orchestrator.ProbeSandbox` returned plain `ErrSandboxProbeUnsupported`, which `writeClientError` fell through to HTTP `500 Internal Server Error` instead of `501 Not Implemented` (matching `actionscache.go`, `packagecache.go`, `staterepo.go`), and missing `id`/`argv` or unknown `access` also fell through to `500` instead of `400`.
- **Fix:** Added `ui.NotImplementedError` / `ui.NewNotImplementedError` (`pkg/ui/client.go`, mapped to HTTP `501` in `writeClientError` in `pkg/ui/tasks.go`), mapped `orchestrator.ErrSandboxProbeUnsupported` to `ui.NewNotImplementedError` in `pkg/daemon/sandbox_probe.go`, and validated empty `id`, empty `argv`, and unknown `access` as HTTP `400` in `pkg/ui/sandbox_probe.go`.
- **Regression tests:** `TestSandboxProbeUnsupportedReturns501Not500` and `TestSandboxProbeRejectsMalformedRequestWith400` in `pkg/ui/sandbox_probe_test.go`, `TestSandboxProbeAdapterMapsUnsupportedBackendToNotImplemented` in `pkg/daemon/sandbox_probe_test.go`, and live PostgreSQL daemon check (`HTTP 501`).

### Bug #3 — `/next/settings/sandboxes` omitted `sandboxDiskOvercommit` and `sandboxMemoryReserveMb`
- **Reproduction:** Open `/next/settings/sandboxes` vs classic `/settings/sandboxes`. Both fields are served by `GET /api/settings`, editable in classic `SandboxesSection.jsx` and `grain settings`, and already labeled in `settings-sections.ts`, yet absent from `/next/settings/sandboxes`.
- **Fix:** Added **Disk overcommit (x)** (`#sb-disk-oc`, `data-testid="input-disk-overcommit"`, `0` unbounded warning `disk-overcommit-unbounded`, blank on blur saves `sandboxDiskOvercommitDefault`) and **Host memory reserve (MiB)** (`#sb-mem-reserve`, `data-testid="input-memory-reserve"`, negative sentinel `-1` displayed blank with reported default placeholder, blank saves `-1`, explicit `0` saves `0`) to `ui-ng/src/app/settings/settings-compute.component.ts`, plus `"reserve"` keyword in `ui-ng/src/app/settings/settings-sections.ts`.
- **Regression tests:** Unit test in `ui-ng/src/app/settings/settings-page.component.spec.ts` and Playwright e2e coverage in `ui-ng/e2e/next-hub-sandboxes-parity.spec.js`.

### Bug #4 — Three operator-doc `/settings/` deep links broke on `/next/`
- **Reproduction:**
  1. `/next/settings/capabilities/config-map` (`docs/capability-servers-ui.md:44`) fell through `**` to `/next/inbox`.
  2. `/next/settings/agents#external-agents` (`docs/agent-api.md:1198`) stayed on Settings → Agents where `card-external-agent-tokens` does not exist.
  3. `/next/settings/access#allowed-principals` (`docs/access-code.md:255`) landed on Access with no `#allowed-principals` target.
- **Fix:** Added a multi-segment matcher redirecting `/settings/capabilities/<id>` to `/settings/capabilities#capability-servers` in `ui-ng/src/app/app.routes.ts`. That matcher was later removed when merging `origin/main`: main (c428c5a69 and the capability server page) now serves `/settings/capabilities/<encoded id>` as the server's own page, which fixes the same link properly. Also redirected `/settings/agents#external-agents` to `/settings/access#external-agents` and added `allowed-principals` in `CARD_ANCHORS` (`ui-ng/src/app/settings/settings-page.component.ts`); added `id="allowed-principals"` and `id="external-agents"` on the corresponding cards in `ui-ng/src/app/settings/settings-security.component.ts`.
- **Regression tests:** Unit test in `ui-ng/src/app/settings/settings-page.component.spec.ts` and Playwright e2e cases in `ui-ng/e2e/next-settings-walkthrough.spec.js`.

## UI Design Book Verification & Three-Review Summary

Screenshots were captured before and after the `ui-ng/` edits at `1440×900`, `2048×1152`, and `390×844` in light and dark themes via `ui/e2e/screenshot.mjs` over `grain demo`, and combined into `contact-sheet.png` (attached to the task report).

- **Design review:** New Disk overcommit and Host memory reserve controls use the existing `.s-grid` / `.s-field` / `.s-input s-num` / `.s-hint` Material 3 tokens inside `#sandbox-budget`, align cleanly across 390 px, 1440 px, and 2048 px in both light and dark themes (verified distinct light/dark hashes and before/after diffs), show `.s-hint-warn` when disk overcommit is `0`, use "host" copy (zero `hostd`), and pass `next-full-system.spec.js` (axe + contrast + zero sideways scroll) and the `@phone @dark` card-overflow spec.
- **Functional review:** Verified end-to-end in real browser against `grain demo` (`next-hub-sandboxes-parity.spec.js`, `next-settings-walkthrough.spec.js`, `next-full-system.spec.js`: **30/30 passed**) and against the live PostgreSQL daemon: `sandboxDiskOvercommit` saves custom values, `0` with unbounded warning, and blank → default; `sandboxMemoryReserveMb` displays `-1` as blank with reported default placeholder, saves `0` as `0`, and saves blank as `-1`; all three deep links (`/next/settings/capabilities/config-map`, `/next/t/acme/settings/capabilities/config-map`, `/next/settings/agents#external-agents`, `/next/settings/access#allowed-principals`) navigate and scroll their target cards into view.
- **Adversarial review:** Checked negative/blank/zero boundary transitions for `sandboxMemoryReserveMb` (`-1` vs `0` vs `Math.trunc` on non-finite input), `diskOvercommitUnbounded()` staying active in `auto` governor mode (since auto mode governs CPU/memory only), `settings/capabilities/<id>` handling multi-segment and `/t/<team>/` prefixed paths, `sshAddr` handling IPv4 `host:port`, IPv6 `[::1]:2222`, explicit `Port` override, out-of-range embedded ports, and non-22 `github.com:2222` (never pinning port-22 GitHub keys), and `handleSandboxProbe` returning 501 vs 400 without invoking the prober on malformed input. No P0/P1 findings remained.

## Test Verification

- `go test -race ./pkg/gitproxy/... ./pkg/ui/... ./pkg/daemon/... ./pkg/orchestrator/...` (targeted suites): **Pass**
- `go test ./tests/hygiene/... ./pkg/ports/...`: **Pass**
- `make fmt-frontend`: **Pass**
- `cd ui-ng && npm run lint && npm run build`: **Pass**
- `cd ui-ng && npx vitest run src/app/settings/settings-page.component.spec.ts`: **29/29 Pass**
- `cd ui-ng && npx playwright test e2e/next-hub-sandboxes-parity.spec.js e2e/next-settings-walkthrough.spec.js e2e/next-full-system.spec.js`: **30/30 Pass**
