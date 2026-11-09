# Shell liveness walkthrough — 2026-11-09 (§5)

Walkthrough of [`docs/test-plans/2026-11-09.md` §5](../test-plans/2026-11-09.md#5-shell-liveness-the-disconnected-banner-on-a-silent-daemon-and-phone-shell-attribute-sync): the disconnected banner on a daemon that accepts but never answers (PR #983, `31534b31`), and the phone shell's `data-phone`/`--sidebar-width`/`data-theme` sync before paint (`48034607`, PR #979). Task 3732.

**Outcome:** items 1–5 behave as specified. One defect was found next to them and fixed here (finding 1): when held connections filled the browser's six HTTP/1.1 sockets to the daemon, the banner could never clear without a reload. Two smaller observations are recorded, not fixed (findings 2 and 3).

## Environment

- `main` at `a59d1e4c` (Merge PR #991). Built from HEAD: `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain`.
- Debian sandbox. Node 20.19.0. Playwright Chromium and WebKit from `/var/cache/playwright`. `socat` 1.7 and `psmisc` from apt.
- One real `grain demo -github-sim -fake-ci -addr 127.0.0.1:4290` process. `walk.mjs` SIGSTOPs and SIGCONTs it. Proxies in front of it:
  - `4291`: `proxy.mjs` (switchable: `forward`, `stall`, `headers`, `delay`, `recover`)
  - `4292`: `socat TCP-LISTEN:4292,fork TCP:127.0.0.1:4290`, which the harness SIGSTOPs
  - `4293`: `proxy.mjs -prefix /grain` (strict)
  - `4294`: `proxy.mjs -prefix /grain -passthrough`
- Harness: [`2026-11-09-shell-liveness/`](2026-11-09-shell-liveness/).
  - `run.sh [OUT] [MODE...]` starts the demo and the proxies, then runs `walk.mjs` modes: `stop proxies recover slow mutations phone shots`.
  - Results per mode are under `before/` (HEAD as found) and `after/` (with the fix).

## Results

| Item | Check | Result |
|---|---|---|
| 1 | `kill -STOP` the real demo on `/`, `/tasks`, `/tasks/1` and `/session/director` (Chromium, 1440×900) | **Pass** on all four. The banner rose at 23.4 s every time ("accepted the connection but sent no answer within 10 seconds"). It reads 23–25 s, not the plan's "~10 s + one poll", by design: `CONFIG_POLL_TIMEOUT_MS`'s comment asks for two timeouts in a row. At most one `GET /api/config` was in flight at a time (3 sent in 44 s, each aborted at 10 s). After `kill -CONT` the banner cleared in 91–282 ms with no reload (`window` marker kept). Nothing was still in flight 3 s later. |
| 2a | socat in front, SIGSTOPped (the kernel completes the handshake into the backlog, nothing reads) | **Pass**: banner at 23.4 s, cleared in 113 ms. |
| 2b | proxy accepts the request and never answers | **Pass**: banner at 23.4 s, cleared in 168 ms. |
| 2c | proxy sends `200` + JSON headers + `{`, then stalls the body | **Pass**: banner at 23.4 s, cleared in 175 ms. The timeout covers the body, as `api()` promises. |
| 2d | sub-path proxy (`/grain/…` → `/…`) | **Pass** when root paths also pass through (4294): banner at 23.4 s, then cleared. A strict prefix proxy (4293) cannot serve the UI at all: `index.html` loads `/assets/index-*.js` and `/assets/index-*.css` by absolute path, so the shell never mounts. This was already true before §5 (finding 3). |
| 2e | slow but answering: every `/api/` answer delayed 8 s, then 9.5 s, watched 120 s each | **Pass**: the banner never rose, no flapping. At most one config read in flight (12 and 10 sent). |
| 2f | proxy stalls, then answers new requests while its held ones stay open (half-open backend connections) | **Fail on HEAD, pass with the fix** (finding 1). Before: the proxy held exactly 6 requests while the browser had 18 in flight. 90 s after recovery the banner was still up, with 39 requests queued. After: the banner cleared 29 s after recovery. |
| 3 | demo stopped: submit New task, send a session message, save Settings → General → Environment name (Enter) | **Pass, nothing applied twice.** While stopped: Create task stays disabled with the dialog open, the composer clears, the settings field shows no error, no toast appears, and the banner explains all of it. Sent once each: `POST /api/tasks`, `POST /api/tasks/13/comments`, `PUT /api/settings`. Within 20 s of CONT: one task (`#20`) with that title, and the dialog closed. The message is on task 13 exactly once. `environmentName` is saved. No mutation failed visibly, because none failed. |
| 4 | Chromium and WebKit, `/tasks`: 20 reloads per theme (1440×900 ↔ 390×844 ×5, then 844×390 ↔ 390×844 ×5) and 20 live resizes/rotations without reload. Every animation frame with a nav in it is sampled. | **Pass** in both engines. On every frame, `<html data-phone>` matches `matchMedia(PHONE_QUERY)`, PhoneNav's AppBar is present exactly when `data-phone="true"`, and the desktop rail is present exactly when it is `"false"`. `data-theme` matched the stored mode on the first frame, and the first frame with a task row had the same gutter as the settled page (232 px desktop, 0 px phone and landscape). Bounding boxes: AppBar at `x=0,y=0` full width at 390×844 and 844×390, no rail. Rail at `x=0, w=232` at 1440×900, no AppBar. |
| 4 | `tasks-load-failed.spec.js --repeat-each=20` (Chromium) | **Pass**: 20/20 in 1.7 min. |
| 5 | banner over `/`, `/tasks`, `/tasks/1` and `/session/director` at 1440×900 and 390×844, light and dark, demo stopped (16 shots) | **Pass**: the banner's box overlaps neither PhoneNav's AppBar nor the session composer, and nothing overflows horizontally. Desktop: banner at `y 0–48`, full width. Phone: banner at `y 0–128`, with the AppBar pushed below it at `y=128`. |

Screenshots were checked by bounding-box assertion only. This sandbox's file reader shows PNGs as bytes, and `LOOK=1` needs a Gemini key it does not have. The banner shots and both contact sheets are attached to the task's closing note for a human eye.

## Findings

### 1. Held reads could fill the browser's connections and keep the banner up forever (fixed)

**Symptom.** A proxy stalls, then starts answering new requests again, but its old backend connections stay half-open: a VPN or NAT drop, or a load balancer whose upstream connection hung. The disconnected banner stayed up, and the page stopped updating until a reload, even though new requests were being answered.

**Cause.** Only the main poll's `GET /api/config` had a timeout (`CONFIG_POLL_TIMEOUT_MS`).

- When it fired, `Promise.all` rejected, but the tick's other reads (`/api/tasks`, `/api/ci/runs…`, `/api/console-sessions`) stayed open, and every attempted tick added another set.
- The background polls had neither a bound nor an in-flight guard: Staging's `/api/persistent-sandboxes` (every 15 s), the bell's `/api/notifications` (every 30 s) and the base-branch gates (every 60 s).
- Over HTTP/1.1 Chromium opens six connections per host. Once held reads occupied all six, every later config read queued inside the browser, timed out without ever being sent, and counted as another failure.
- The plain `kill -STOP`/`CONT` case hid this: CONT answers every held read at once.

**Reproduction.** `run.sh OUT recover` (`walk.mjs recover`) shows it. Before the fix (`before/recover-results.json`): `held: 6` at the proxy, `browserInFlight: 18`, and 90 s after recovery the banner had not cleared, with `browserInFlight: 39`.

**Fix** (`ui/src/App.jsx`, `ui/src/notifications.js`):

- Each poll tick creates an `AbortController`. `refreshList`, `refreshCIRuns` and `refreshSessions` take its signal, and the tick aborts it when it fails as unreachable.
- An aborted list read does not set a tasks error of its own, so the tasks pane still says what the banner says.
- The three background polls carry the same 10 s bound the config read does. They were already quiet on failure, so they still raise nothing.
- The mount load and mutations are unchanged: still no timeout.

**After** (`after/`):

- `recover`: the proxy still held 5, the browser had 5 in flight, and the banner cleared 29 s after recovery.
- `stop`: at most 7–8 `/api/` requests in flight during a 44 s stop, and 2–3 at CONT (before: 14–15 and 14–15). The banner timing is unchanged (23.4 s) and it still clears in about 0.1 s.

**Regressions.**

- `App.resilience.test.jsx` › "gives up on the rest of a tick's reads when grain stops answering": real `api()` over a fetch that never answers, for 90 s. At most one open read per polled path. Fails without the fix (8 `/api/tasks` reads open).
- `ui/e2e/disconnected-banner-held-reads.spec.js`: Playwright holds every background GET unanswered for the length of the banner plus 35 s and asserts at most one in flight per path, then answers new requests and expects the banner to clear. Fails without the fix (`/api/tasks`: 5 in flight).

### 2. Banner latency is 23–25 s, not "~10 s plus one poll interval" (by design, not changed)

The plan's item 1 expects the banner within about 10 s plus one poll. The code asks for two consecutive failed ticks (`noteUnreachable`'s streak of 2, which guards against one slow answer flapping the banner), so it rises after two timeouts plus the tick between them. `CONFIG_POLL_TIMEOUT_MS`'s comment and `disconnected-banner-silent-daemon.spec.js` (`> 15 s`) both say this. The slow-daemon runs (2e) show the trade-off working as intended.

### 3. The UI cannot be served under a strict sub-path (pre-existing, out of §5's scope)

Under a proxy that forwards only `/grain/*`, the shell never mounts: the built `index.html` and `api()` use root-absolute paths (`/assets/…`, `/api/…`). Behind a prefix proxy that also routes root paths, the banner behaves as everywhere else (2d). Supporting a real sub-path mount is a feature (a Vite `base`, plus a path prefix in `api()` and in the router), not a liveness fix, so it is only recorded here.

## Not covered

- PostgreSQL vs SQLite makes no difference here: the store sits behind the HTTP surface under test. The runs used the demo's own throwaway store.
- `--repeat-each=20` was run on `tasks-load-failed.spec.js` only. The WebKit phone layout specs were covered instead by item 4's 40 reloads and 20 live resizes per engine, which sample every frame.

## Artifacts

[`2026-11-09-shell-liveness/screenshots/`](2026-11-09-shell-liveness/screenshots/):

- `contact-sheet-before.png` and `contact-sheet-after.png`: `ui/e2e/screenshot.mjs` over `/`, `/tasks`, `/tasks/1` and `/session/director`, at desktop and phone, light and dark, using the pre-fix and fixed binaries. The fix changes no layout, and the two sheets show the same pages.
- `banner-*.png`: the banner with the demo stopped (item 5).
- `recover-before-stuck.png` and `recover-after-cleared.png`: finding 1's page, 90 s after the proxy recovered.
