# Walkthrough: `grain-agentd update` self-update and Settings → Hosts offer (2026-11-16 §4)

- **Date:** 2026-11-17, 00:45–01:30 UTC, one run.
- **Scope:** §4 of [`docs/test-plans/2026-11-16.md`](../test-plans/2026-11-16.md) (PR #1533: `362ccdde3`, `c6b23bdb5`, `72049c499`, `b02694b26`, `5fc43d276`), following `docs/grain-agentd.md` ("Updating it") verbatim.
- **Task:** [Task #6477](/tasks/6477) (filed by [Task #6330](/tasks/6330)), branch `grain/53549e42/task-6477-walkthrough-grain-agentd`, base `main`.
- **Commit walked:** `a771a9973` ("Merge pull request #1705"). Both fixes below were re-verified on live rebuilds of this commit.
- **Outcome:** All four "What is NOT proved" items of §4 were walked end-to-end against a real HEAD `grain daemon -sandbox-port-base=0` serving signed releases and real unprivileged `systemd --user` units (including a real 5-minute wall-clock rollback watchdog and an `ExecStartPre` `guard.sh` crash-on-start rollback). Two bugs were found and fixed with regression tests (**F1**: failed smoke tests left `versions/<v>/grain-agentd` half-staged on disk; **F2**: `/next/settings/hosts` update offer had no Copy button, crushed into 6 lines on 1440px desktop, and scrolled 932px sideways on 390px phone). One design/behavior observation (**F3**) is recorded below.

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian GNU/Linux 12 (bookworm), `linux/amd64`, Go `go1.26.10`, `systemd 252 (252.39-1~deb12u2)`, Node `v20.19.0`. |
| Controller | Real `grain daemon -host-sandboxes -sandbox-port-base=0 -ui-addr 127.0.0.1:4294 -as alice@example.com -poll-interval 1s -max-workers 3 -max-mergers 0` over SQLite, with `GRAIN_AGENTD_DIST_DIR`, `GRAIN_AGENTD_RELEASE_PUBLIC_KEY` (throwaway Ed25519 keypair, key ID `f89aaa32453a302b`), and `GRAIN_AGENTD_RELEASE_URL=off`. |
| Host units | Real `systemd --user` managers for two unprivileged users: `walkhost` (UID 1001, host `ws-v380`, owner `alice@example.com`, server `http://127.0.0.1:4294`) and `walkself` (UID 1002, host `ws-self`, owner `bob@example.com`, server `http://127.0.0.1:4295` via controllable TCP proxy to `4294`). Note: `systemd-logind.service` is masked in the sandbox image, so `loginctl enable-linger` emits a soft note and `sudo systemctl start user@<uid>.service` starts the real per-user systemd instance with `/run/user/<uid>` and `journalctl _UID=<uid>`. |
| Host binaries | Real `grain-agentd` built from git tag `v3.8.0` (detached worktree) and from HEAD (`v3.11.0`, `v3.12.0`, `v3.12.1`), all embedding the test release public key via `-ldflags -X github.com/ai-on-gke/grain/pkg/agentd/release.ReleasePublicKey=...` and signed with `grain-agentd-sign`. |
| Frontend & Browser | Real `make frontend` (`ui-ng` embedded in `pkg/ui/static-next`), Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900` and `390×844` (plus `2048×900`, `1360×900`, `1280×900` checks) in light and dark modes, with `@axe-core/playwright`. |

## Stubs and boundaries

Only two test harnesses were stubbed:

1. **Agent CLI (`agy`):** a gate-controlled shell script installed at `~/.local/bin/agy` for `walkhost` and `walkself` that blocks while `/var/tmp/walk-agentd-gate` exists and writes deterministic output on release, allowing controlled in-flight tasks during cutover.
2. **Controllable TCP proxy (`127.0.0.1:4295 -> 127.0.0.1:4294`):** forwards traffic for `ws-self` normally and resets connections whenever `/home/debian/walkthrough/block-proxy` exists, used to simulate unread final spools across cutover (Item 1B) and controller-unreachable rollback (Item 2A).

In the new Playwright regression spec (`ui-ng/e2e/next-hosts-update.spec.ts`), `GET /api/hostds` is fulfilled from `ui-ng/e2e/hostds-update.fixture.json` and toggled to post-update on reload; the real end-to-end offer and post-update disappearance were also verified live against the running daemon in Item 4.

---

## Steps and outcomes

### Item 1 — Idle cutover and spool survival under real `systemd --user`

#### 1A. `v3.8.0` → `v3.12.0` (`walkhost` / `ws-v380`)

| Step | Outcome |
| --- | --- |
| Installed `v3.8.0` (built from tag `v3.8.0`) as `walkhost` via `grain-agentd-v3.8.0 install`. | ✅ Wrote `~/.config/grain/{agentd.yaml,agentd-token,agentd-release.pub}` (key `f89aaa32453a302b`), `~/.local/bin/grain-agentd`, and `~/.config/systemd/user/grain-agentd.service` (no `ExecStartPre`). Connected as `ws-v380`, `v3.8.0`. |
| Checked `GET /api/hostds` before update. | ✅ `ws-v380` (`v3.8.0`, no `self_update` feature), `local`, and `ws-offline` all had `update: null` as designed; `ws-self` (`v3.11.0`, `self_update`) received `update: {build: "v3.11.0", available: "v3.12.0", command: "grain-agentd update -version v3.12.0"}`. |
| Dispatched live task #2 on `ws-v380` with gate closed (`agy` PID 12104, spool `p-fcbf1ec05b05d743`), then ran HEAD `grain-agentd update -version v3.12.0` as `walkhost` with no `-server`/`-allowlist`/`-socket` flags. | ✅ Read unit defaults, verified signature + SHA-256, smoke-tested `-build` and `-check`, preserved `versions/v3.8.0/grain-agentd`, flipped `current -> versions/v3.12.0` and `~/.local/bin/grain-agentd -> ~/.local/lib/grain-agentd/current/grain-agentd`, wrote `guard.sh` and `pending` (`v3.8.0\nv3.12.0`), added `ExecStartPre=-/bin/sh .../guard.sh` + `daemon-reload`, and requested cutover. |
| Held gate closed while `v3.8.0` was draining. | ✅ `v3.8.0` (MainPID 11459) stayed running while Task #2 remained `running`; did not interrupt the in-flight CLI. |
| Released gate. | ✅ `v3.8.0` exited 0 at `00:58:48 UTC`; `systemd --user` waited `RestartSec=10`, ran `guard.sh`, started `v3.12.0` (MainPID 12886) at `00:58:59 UTC`; `v3.12.0` connected, logged `grain-agentd: update to v3.12.0 confirmed: the controller has said hello`, and removed `pending` + `pending.started`. `GET /api/hostds` reported `ws-v380` at `v3.12.0` with `self_update` and `update: null`. |

#### 1B. `v3.11.0` → `v3.12.0` with verbatim UI command and unread spool adoption (`walkself` / `ws-self`)

| Step | Outcome |
| --- | --- |
| Dispatched live task #3 on `ws-self` (spool `p-7d413e59c520f104`), ran verbatim UI command `grain-agentd update -version v3.12.0` as `walkself`. | ✅ Staged `v3.12.0`, flipped `current`, wrote `pending` (`v3.11.0\nv3.12.0`), and kept `v3.11.0` (MainPID 11513) alive while gate was closed. |
| Blocked proxy `4295`, then released gate so `exit.json` landed unread while disconnected from the daemon. | ✅ `v3.11.0` drained and exited 0 (`01:00:12`); `systemd --user` started `v3.12.0` (MainPID 13650, `01:00:22`), `guard.sh` touched `pending.started`, and `v3.12.0` logged `hostd: kept the final spool of cli "agy" (run 3-1, id p-7d413e59c520f104) for a daemon to read`. |
| Unblocked proxy (`01:00:23`). | ✅ `v3.12.0` connected, confirmed update, daemon re-attached at cursor 53 on `v3.12.0`, finished run 3-1 cleanly, and `GET /api/hostds` cleared `update` to `null`. |

#### 1C. `tests/agentdcompat` harness

Ran `GRAIN_AGENTD_COMPAT_BINARIES="v3.8.0=...,head=..." go test -v ./tests/agentdcompat -run TestReleasedAgentdsUpdateToHead`: **PASS** (`0.72s`) both before and after Fix F1.

---

### Item 2 — Rollback when the new build cannot say hello

#### 2A. Real 5-minute wall-clock watchdog rollback (`walkself` / `ws-self`, `v3.12.0` → `v3.12.1`)

Swapped `GRAIN_AGENTD_DIST_DIR` live (without restarting the daemon) to signed `v3.12.1`; `GET /api/hostds` immediately began offering `v3.12.0 → v3.12.1` to `ws-self`.

1. At `01:04:07 UTC`, ran `grain-agentd update -version v3.12.1` as `walkself`, then immediately touched `/home/debian/walkthrough/block-proxy` (`01:04:08 UTC`).
2. At `01:04:18 UTC`, `systemd --user` ran `guard.sh` (creating `pending.started` at `01:04:18.335Z`) and started `v3.12.1` (MainPID 14489). `v3.12.1` repeatedly failed to connect (`connection reset by peer`, backing off to 5s).
3. During the 5-minute window, `GET /api/hostds` reported `ws-self` as `state: "offline"` (last known `connection.build: "v3.12.0"`, `update: null`).
4. At **`01:09:18 UTC`** (exact 5m0s confirm window), the in-process watchdog rolled back and cut over:
   ```text
   Nov 17 01:09:18 debuerreotype grain-agentd[14489]: 2026/11/17 01:09:18 grain-agentd: rolled back from v3.12.1 to v3.12.0: v3.12.1 did not reach the controller within 5m0s; cutting over to it
   Nov 17 01:09:18 debuerreotype grain-agentd[14489]: 2026/11/17 01:09:18 hostd: cutting over to a new build: still taking new starts, and exiting the moment no run is running; 0 process(es) running now
   Nov 17 01:09:18 debuerreotype grain-agentd[14489]: 2026/11/17 01:09:18 hostd: drained; exiting so the new build takes over
   Nov 17 01:09:28 debuerreotype systemd[11343]: Started grain-agentd.service - grain-agentd: agent CLIs for the grain daemon at http://127.0.0.1:4295.
   Nov 17 01:09:28 debuerreotype grain-agentd[17910]: 2026/11/17 01:09:28 hostd: listening on /run/user/1002/grain-agentd/agentd.sock (hostd protocol 4, build v3.12.0)
   ```
5. Filesystem state immediately after rollback:
   ```text
   ~/.local/lib/grain-agentd/
     current -> versions/v3.12.0
     guard.sh (0755)
     update-failed   # contents: "v3.12.0\nv3.12.1\n"
     versions/{v3.11.0,v3.12.0,v3.12.1}/grain-agentd
   ```
   (`pending` was renamed to `update-failed`, and `pending.started` was removed.)
6. Removed `block-proxy`; at `01:09:50 UTC`, `v3.12.0` reconnected (`hostd: connected to the controller (hostd protocol 4, build v3.12.0)`).
7. **What the controller and UI show after rollback:** `Hello` does not carry local rollback history (`update-failed` is local to the host per `design/design/agentd-stable-contract.design.md`), so `GET /api/hostds` and `/next/settings/hosts` show `ws-self` as `connected` at `v3.12.0` and re-offer `v3.12.0 → v3.12.1`. Running `grain-agentd update -version v3.12.1` again once connectivity was restored succeeded cleanly (`v3.12.1` confirmed, `update` cleared to `null`).

#### 2B. `ExecStartPre` (`guard.sh`) rollback for a crash-on-start build (`walkhost` / `ws-v380`)

Signed a crash-on-start `v3.12.2` script that passed `-build` (`v3.12.2`) and `-check` (exit 0) but exited 1 on service start, and updated `walkhost` with `-from`.

- `v3.12.2` started (`01:06:19`), `guard.sh` created `pending.started`, and `v3.12.2` exited 1 (`Failed with result 'exit-code'`).
- Backdated `pending.started` by 6 minutes (`touch -d '6 minutes ago'`); on the next `RestartSec=10` restart (`01:06:29`), `guard.sh` rolled back before `ExecStart`:
  ```text
  Nov 17 01:06:29 debuerreotype sh[14648]: grain-agentd: rolled back from v3.12.2 to v3.12.0: v3.12.2 did not reach the controller within 300 seconds
  Nov 17 01:06:29 debuerreotype grain-agentd[14657]: 2026/11/17 01:06:29 hostd: connected to the controller (hostd protocol 4, build v3.12.0)
  ```
- Verified `current -> versions/v3.12.0`, `update-failed` (`v3.12.0\nv3.12.2`), `pending.started` removed, and `ws-v380` back online at `v3.12.0`.

---

### Item 3 — Operator-seat refusals and filesystem cleanliness

Tested all refusals on `walkhost` (`ws-v380`) while comparing `find ~/.local/lib/grain-agentd -maxdepth 3` before and after each refusal:

| Case | Exit | Error / behaviour | FS state |
| --- | --- | --- | --- |
| 1. Tampered binary (1 byte flipped) | 1 | `refusing the release: ... sha256 ... does not match manifest ...` | ✅ Unchanged |
| 2. Wrong signing key (`74e557acc71d68a6` vs pinned `f89aaa32453a302b`) | 1 | `refusing the release: manifest is signed by unknown key 74e557acc71d68a6 ...` | ✅ Unchanged |
| 3. Unsigned manifest | 1 | `refusing the release: manifest has no signature` | ✅ Unchanged |
| 4. Wrong platform (`linux/arm64` vs `linux/amd64`) | 1 | `refusing the release: manifest is for linux/arm64, not linux/amd64` | ✅ Unchanged |
| 5. Downgrade without pin (plain, `-version v3.11.0`, glob `update.pin: v3.11.*`) | 1 | `v3.11.0 is older than this build's v3.12.0: refusing a downgrade (set update.pin: v3.11.0 ...)` | ✅ Unchanged |
| 5b. Intentional downgrade with exact `update.pin: v3.11.0` (`-no-cutover`) | 0 | Staged and flipped `current -> versions/v3.11.0`; restored cleanly to `v3.12.0`. | ✅ Expected |
| 6. Version outside pin (`v3.12.1` vs `update.pin: v3.11.*`) | 1 | `v3.12.1 is outside the allowlist's update.pin "v3.11.*"` | ✅ Unchanged |
| 7. `update.mode: off` | 1 | Refused with `update.mode: off` message. | ✅ Unchanged |
| 8a. Smoke-test `-build` mismatch (`v3.12.9` reports `v3.12.1`) | 1 | `... -build says "v3.12.1", but its manifest says "v3.12.9"` | ❌ **Bug F1** (left `versions/v3.12.9/grain-agentd` on disk) → ✅ Fixed & re-verified (`FS UNCHANGED`) |
| 8b. Smoke-test `-check` failure (`v3.12.8` exits 2 on `-check`) | 1 | `... -check -allowlist ...: exit status 2` | ❌ **Bug F1** (left `versions/v3.12.8/grain-agentd` on disk) → ✅ Fixed & re-verified (`FS UNCHANGED`) |
| 9. Running `grain-agentd update` as root | 2 | `run this as yourself, not root` | ✅ Unchanged |
| 10. Already up to date (`v3.12.0` → `v3.12.0`) | 0 | `==> already on v3.12.0` (also works with `env -u XDG_RUNTIME_DIR`) | ✅ Unchanged |
| 11. `v3.8.0 -check` on allowlist with `update:` key | 2 | `field update not found in type hostd.Allowlist` (matches `docs/grain-agentd.md`) | ✅ Expected |
| 12. Double `grain-agentd update` while `pending` exists | 0 | Second call prints `==> already on v3.12.1` and preserves `pending`. | ✅ Expected |
| 13. Unauthenticated `GET /api/hostd/agentd/linux/amd64{/manifest.json}` | 200 | Served without token despite `local-api-token`, with `Cache-Control: no-store` and `X-Grain-Agentd-{Version,Sha256}`. | ✅ Expected |

---

### Item 4 — `/next/settings/hosts` in Playwright (1440px and 390px, light and dark)

Walked `/next/settings/hosts` against the live daemon before update (`ws-self` offered `v3.11.0 → v3.12.0`, and later `v3.12.0 → v3.12.1`) and after update (`ws-self` on `v3.12.0` / `v3.12.1`, update line gone).

| Check | Before fix | After fix (F2) |
| --- | --- | --- |
| Update line text (`Update available: <old> → <new>. On the host, run <cmd>`) | ✅ Rendered for `ws-self`; absent on `local`, `ws-offline`, `ws-v380`. | ✅ Rendered for `ws-self`; absent on up-to-date/offline/local hosts. |
| Copyable command | ❌ No Copy button; plain inline `<span class="s-meta-mono">`. | ✅ Recessed `<code>` well (`user-select: all`) paired beside a 48×28px `Copy` button (`aria-label="Copy update command for <name>"`) writing exact command + showing `"Copied update command"` snackbar. |
| Desktop `1440×900` (light & dark) | ❌ Status cell squeezed to `159px`; update block wrapped into **6 lines** (`131×108`, row height `193px`). | ✅ Status cell `264px`; update block `244×82` (row height `157px`); Copy paired on same row; `0` overflow; `0` axe violations. |
| Phone `390×844` (light & dark) | ❌ `.s-table-wrap` scrolled **932px inside 324px** (2.9× sideways scroll, missing `tabindex="0"`); Status squeezed to `120px`, update wrapped to **9 lines** (`92×162`) off-screen. | ✅ Table stacks into labelled cards (`.s-table.s-table-stack`); **zero sideways scroll** (`wrapScrollW: 322` inside `324px`); update block full-width (`294×82`) left-aligned with Copy paired beside `<code>`; `0` axe violations. |
| Line disappears after update | ✅ Gone on `ws-self` once updated to `v3.12.0`; `hostd-connection-ws-self` shows `v3.12.0`. | ✅ Gone on `ws-self` once updated to `v3.12.1`; `hostd-connection-ws-self` shows `v3.12.1`. |

---

## Findings and fixes

### F1 (Fixed) — Failed `-build` / `-check` smoke tests left `versions/<v>/grain-agentd` half-staged

- **Where:** `pkg/agentd/update.go` (`(*updater).stage`).
- **Reproduction:** Sign a release binary whose `-build` disagrees with the manifest version (or whose `-check -allowlist ...` exits non-zero) with the pinned release key, then run `grain-agentd update -from <dir>`. The command exits 1 with a clear error and does not move `current`, **but** `~/.local/lib/grain-agentd/versions/<v>/grain-agentd` (mode `0755`) remained on disk.
- **Root cause:** `stage` wrote the binary directly to `u.l.binary(m.Version)` before invoking `-build` and `-check`, with no cleanup on failure.
- **Fix:** Write and smoke-test the binary in a temporary file (`os.CreateTemp(u.l.Versions, ".grain-agentd-stage-*")`) on the same filesystem, cleaning up the temp file (and any newly created empty `versions/<v>`, `versions`, and `lib` directories) via `defer` unless both `-build` and `-check` pass and the atomic `os.Rename` into `versions/<v>/grain-agentd` succeeds.
- **Regression test:** Extended `TestUpdateRefusesABuildThatFailsItsSmokeTest` in `pkg/agentd/update_test.go` to assert that neither `f.l.Lib` (on a fresh host) nor extra entries in `f.l.Versions` (on a host with prior versions) remain after `-build` or `-check` refusal. Re-verified live on `walkhost` Cases 8a & 8b (`FS UNCHANGED`).

### F2 (Fixed) — `/next/settings/hosts` update offer lacked a Copy button, crushed on desktop, and scrolled sideways on phone

- **Where:** `ui-ng/src/app/settings/settings-compute.component.ts` (Registered hosts table), `ui-ng/src/app/settings/settings-layout.css`.
- **Reproduction:** Load `/next/settings/hosts` with a connected host offering an update (e.g. `ui-ng/e2e/hostds-update.fixture.json`) at `1440×900` and `390×844`:
  - No Copy button exists for the update command.
  - At `1440px`, Status is 159px wide, wrapping the update notice into 6 lines (`131×108`).
  - At `390px`, the unstacked table overflows `.s-table-wrap` to `932px` inside `324px` (violating `design/design/ui-design-book.design.md`'s Settings `.s-table-stack` rule and clipping the Status/update column off-screen).
- **Fix:**
  - Marked the Registered hosts table `.s-table.s-table-stack` inside `.s-table-wrap.s-table-stack-wrap` with `data-label` on `Owner`, `Status`, `Labels & capacity`, and `Actions` (matching Settings → Repositories).
  - Wrapped Status and Labels cell contents in `.s-cell-stack`, added `.hd-status-cell` responsive rules (`width: 34%` at container `>= 880px`; left-aligned vertical stack below `880px`), and rendered `hostd-update-<name>` with a recessed Material 3 `<code>` chip (`user-select: all`) paired beside a 28px-tall `Copy` button (`copyInvite(h.update.command, 'Copied update command')`).
- **Regression tests:**
  - Updated unit test in `ui-ng/src/app/settings/settings-page.component.spec.ts` to assert the Copy button copies the exact command.
  - Added Playwright e2e spec `ui-ng/e2e/next-hosts-update.spec.ts` (`@phone @dark`) covering the update notice, Copy button + clipboard + snackbar, zero sideways scroll and bounding-box separation at 1440px and 390px, zero serious/critical axe violations, and the offer disappearing after update.

### F3 (Observation — not a bug) — Live allowlist `update:` edits take effect on the host immediately, while `GET /api/hostds` updates on reconnect

Editing `~/.config/grain/agentd.yaml` to set `update.mode: off` or `update.pin: v3.12.0` immediately governs `grain-agentd update` (which re-reads the allowlist on invocation). The controller's `GET /api/hostds` continues displaying the `updateMode` / `updatePin` reported in the active session's `Hello` (and therefore continues offering updates) until the host reconnects/restarts (`systemctl --user restart grain-agentd.service`), at which point `GET /api/hostds` reflects the new policy immediately. This is consistent with how `Hello.Logins` works in `pkg/hostd/remote.go` and does not weaken host-side enforcement.

---

## Verification

- `go test ./pkg/agentd/... ./pkg/hostd/... ./tests/hygiene/... ./pkg/ports/...` — **PASS**
- `go test -v ./pkg/ui -run 'HostUpdate|HostdAgentd'` — **PASS**
- `GRAIN_AGENTD_COMPAT_BINARIES="v3.8.0=...,head=..." go test -v ./tests/agentdcompat -run TestReleasedAgentdsUpdateToHead` — **PASS**
- `make fmt-frontend` — **PASS**
- `cd ui-ng && npm run lint && npm test -- --run && npm run build` — **PASS** (70 files / 689 unit tests passed; 0 eslint errors; 0 component stylesheet budget warnings)
- `npx playwright test e2e/next-hosts-update.spec.ts e2e/next-hosts-invite.spec.js e2e/next-hosts-confirm.spec.js e2e/next-hosts-git-parity.spec.js e2e/next-full-system.spec.js` — **19 passed** (desktop light/dark, phone light/dark, colour contrast, axe)
