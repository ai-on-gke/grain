# Walkthrough: Identity-Verifier Sessions, Exempt Bundles and `/next/` Retries (2026-11-16 §1)

- **Plan section:** `docs/test-plans/2026-11-16.md` §1 (filed by [Task #6330](/tasks/6330), executed in [Task #6474](/tasks/6474))
- **Base commit walked:** `1d52982bb` (`origin/main`)
- **Fix commit:** `f46ec0e72` (`fix(ui,ui-ng): strip staging-proxy assertions and offer Reload on stale chunks`)
- **Features under test:**
  - Signed short verifier sessions (`__Host-grain_verified`, `SessionBinder` / `LocalVerifier`, `SessionGrace`) — PR #1574 (`5759e47eb`, `32a3e5e8f`)
  - Content-hashed frontend bundles served before identity verification — PR #1626 (`4e355594b`)
  - Static asset `Cache-Control`, strong/weak `ETag` (`304`) and `404 no-store` for missing assets — PR #1612 (`50c7b4d84`)
  - `/next/` `GrainApi` retrying `GET`/`HEAD` on `503 verifier_unavailable` — PR #1608 (`65d2d3f6d`)
  - `/next/` idle route chunk preloading (`IdleRoutePreload`) and `g-nav-progress` bar — PR #1539 (`227d6f058`)

---

## 1. Environment

| Component | Details |
|---|---|
| OS / Host | Debian GNU/Linux 12 (bookworm), `x86_64`, Go `1.26`, Node.js `v20.19.0` |
| Daemon mode | Real `grain daemon -sandbox-port-base=0 -host-sandboxes -ui-require-verifier=slow-proxy -deployment-owner=alice@example.com` (`docs/test-runs/2026-11-16-identity-sessions/grain_slowverifier.go` + `env.sh`) |
| Identity proxy | Local TLS 1.2+ reverse proxy on `https://127.0.0.1:4292` (secondary host `https://127.0.0.1:4295`) signing per-request `X-Slow-Proxy-Assertion` headers and forwarding to `http://127.0.0.1:4291`; CA installed into Chromium NSS (`$HOME/.pki/nssdb`) so Chromium's HTTP disk cache operates as in production HTTPS |
| Databases walked | SQLite `3.40.1` (`2022-12-28`) and PostgreSQL `15.20` (`Debian 15.20-0+deb12u1`, `GRAIN_REQUIRE_POSTGRES=1`) |
| Browser & widths | Playwright `1.62.1`, headless Chromium `151.0.7922.34` at `1440×900` (desktop, light & dark) and `390×844` (phone, touch, light & dark) |
| Committed helpers | `docs/test-runs/2026-11-16-identity-sessions/{grain_slowverifier.go,probe_hub_tunnel.go,env.sh,probe_curl.sh,walk_playwright.mjs,walk_upgrade.mjs}` |

---

## 2. Exact Walkthrough Steps

1. Built frontend bundles and CLI from `HEAD`:
   ```sh
   make frontend && go build -o /home/debian/bin/grain ./cmd/grain
   ```
2. Installed `postgresql`, `postgresql-client`, `sqlite3`, and `libnss3-tools`; started PostgreSQL 15.20 and ran `pkg/ui` and `tests/security` suites with `GRAIN_REQUIRE_POSTGRES=1`.
3. Built `grain_slowverifier.go` (`//go:build ignore`), registering `slow-proxy` via `ui.RegisterIdentityVerifier` implementing `IdentityVerifier`, `SessionBinder`, and `LocalVerifier`, alongside a local control server (`127.0.0.1:4293`) recording `fullVerifies`, `localVerifies`, `graceAdmits`, `unavailableErrors`, per-request `verifyEvents`, and proxy `Set-Cookie` / `Cache-Control` / `ETag` headers.
4. Started `ghsim` (`127.0.0.1:4294`), `grain-slowverifier daemon` (`127.0.0.1:4291` behind `https://127.0.0.1:4292`), seeded proposed tasks and a live persistent staging sandbox (`preview` at `/staging/preview` running `staging_echo.py` on port `4281`), and trusted the proxy CA in `$HOME/.pki/nssdb`.
5. Executed `probe_curl.sh` (Item 2 `< 5m` grace matrix + Item 3 26-probe bundle-exemption & `HubTunnelHandler` table), real wall-clock `> 5m` (`314s`) grace expiry probes, `walk_playwright.mjs` (Items 1, 5, 6), and `walk_upgrade.mjs` (Item 4 cold/warm HARs + live daemon upgrade with open stale tabs), over SQLite and PostgreSQL.

---

## 3. Per-Item Outcomes

### Item 1 — Real browser behind a real slow verifier (`delayMs = 1500ms`)
- **Cold `/next/inbox` load:** **PASS.** Across 85 total proxied browser requests (HTML entry document, CSS, 68 content-hashed JS chunks including `IdleRoutePreload`, and 15 `/api/*` reads), `slow-proxy` executed **1** full `Verify` (`GET /next/inbox`, `1500 ms`), setting:
  `Set-Cookie: __Host-grain_verified=v1.<payload>.<sig>; Path=/; Max-Age=360; HttpOnly; Secure; SameSite=Strict`.
  All 69 content-hashed bundles bypassed verification via `serveHashedFrontendBundle` (`0 ms`), and all 15 `/api/*` reads took the `VerifyLocal` session fast path (`localVerifies = 15`, `fullVerifies = 0`, `0 ms`). Cold render completed in `1,952 ms` (vs. `> 22 s` without sessions and bundle exemption).
- **Client-side navigation (`/next/tasks`, `/next/task/2`):** **PASS.** Navigating to `/next/tasks` (`83 ms`) and `/next/task/2` (`203 ms`) performed `0` full `Verify` calls (`localVerifies = 13`).
- **Cookie re-issuance, cross-port binding, and logout:** **PASS.**
  - After a `1.2 s` `SessionTTL` elapsed, `/next/tasks` performed `1` full `Verify` and re-issued a fresh `__Host-grain_verified` value.
  - Navigating in the same browser to `https://127.0.0.1:4295/api/auth/user` sent `127.0.0.1`'s host-only cookie (RFC 6265 cookies are not port-scoped), and `requestVerifierSession` rejected it on the `Host` mismatch (`"127.0.0.1:4292" != "127.0.0.1:4295"`), forcing a full `Verify` (`altPortFullVerifies = 1`).
  - Clicking **Account → Sign out** (`[data-testid="account-sign-out"]`) posted `POST /api/auth/logout`, cleared `__Host-grain_verified` (`Max-Age=0`), and landed on the *"Sign in through slow-proxy"* page when unproxied.
- **Staging proxy (`/staging/preview/*`):** **BUG FOUND & FIXED (Bug #1 — Security).**
  - Inbound `__Host-grain_verified` cookies were stripped (`stagingSawVerifierCookie: false`), and outbound `Set-Cookie: __Host-grain_verified=evil` / `__host-grain_verified=evil2` / `grain_access=evil3` / `Clear-Site-Data` headers from the staging app were dropped while `staging_app=ok` was scoped to `/staging/preview`.
  - **Defect found on `1d52982bb`:** `handleStagingProxy` (`pkg/ui/persistent_sandboxes.go`) did **not** strip signed proxy identity assertion headers (`X-Slow-Proxy-Assertion`, `X-Goog-IAP-JWT-Assertion`, `X-Grain-Hub-Assertion`, `X-Grain-Registry-Assertion`), `Proxy-Authorization`, or `X-Grain-Tunnel-Token` before forwarding to the untrusted staging sandbox (`stagingSawAssertionHeader: true`). Fixed in `stripStagingCredentials` and verified live (`stagingSawAssertionHeader: false` on both SQLite and PostgreSQL).

### Item 2 — `SessionGrace` in practice
- **Backend down `< 5m` past expiry (same principal):** **PASS.** With `TTL = 2s`, at `t = +2.5s` with `mode = "down"`, `GET /api/auth/user` as `alice@example.com` returned `200 OK` (`graceAdmits = 1`).
- **Grace never extends itself across repeated requests:** **PASS.** Three consecutive `GET /api/auth/user` calls during grace all returned `200 OK` with `Set-Cookie` count `0` (`graceAdmits = 1, 2, 3`).
- **Different header principal or different `Host` during outage:** **PASS.** Presenting `bob@example.com` in `X-Slow-Proxy-Assertion` alongside Alice's cookie returned `503 Service Unavailable` (`code: "verifier_unavailable"`, `Retry-After: 5`, `Cache-Control: no-store`, `graceAdmits = 0`). Presenting `127.0.0.1:4292`'s cookie against `https://127.0.0.1:4295` likewise returned `503`.
- **Backend down `> 5m` past expiry (real wall-clock `314s` wait):** **PASS.** Cookie minted at `10:49:49 UTC` (`TTL = 2s`, grace deadline `10:54:51 UTC`) and replayed explicitly at `10:56:03 UTC` received `hasSession = false`:
  - In `mode = "down"`: refused with `503 verifier_unavailable` (`Cache-Control: no-store`, `graceAdmits = 0`).
  - In `mode = "lie-grace"` (verifier claiming `SessionGrace: true` anyway): `verifyWithVerifier` refused closed with `401 Unauthorized` (`"verifier \"slow-proxy\" admitted alice@example.com on a session the request does not carry"`).

### Item 3 — Bundle exemption from outside (`curl --path-as-is`, verifier `mode = "refuse"`)

All probes below were sent from an external forwarded IP (`X-Forwarded-For: 198.51.100.99`) with no session cookie and with `slow-proxy` refusing every assertion (`401`):

| Path / Probe | Method | Status | `Cache-Control` | `ETag` | `Content-Encoding` | Body Bytes | `Verify` Called |
|---|---|---|---|---|---|---|---|
| `/next/main-6EQG52GH.js` (`Accept-Encoding: gzip`) | `GET` | `200` | `private, max-age=31536000, immutable` | `W/"f4083bfd508a28dec3cf206bed9bad5b"` | `gzip` | `17160` | `0` |
| `/next/main-6EQG52GH.js` (identity) | `GET` | `200` | `private, max-age=31536000, immutable` | `"f4083bfd508a28dec3cf206bed9bad5b"` | `-` | `57997` | `0` |
| `/next/main-6EQG52GH.js` | `HEAD` | `200` | `private, max-age=31536000, immutable` | `"f4083bfd508a28dec3cf206bed9bad5b"` | `-` | `0` | `0` |
| `/next/main-6EQG52GH.js` (`If-None-Match` strong) | `GET` | `304` | `private, max-age=31536000, immutable` | `"f4083bfd508a28dec3cf206bed9bad5b"` | `-` | `0` | `0` |
| `/next/main-6EQG52GH.js` (`Range: bytes=0-15`) | `GET` | `200` | `private, max-age=31536000, immutable` | `"f4083bfd508a28dec3cf206bed9bad5b"` | `-` | `57997` | `0` |
| `/next/styles-HRI7DVJQ.css` (`gzip`) | `GET` | `200` | `private, max-age=31536000, immutable` | `W/"2de8c9a6adb805332cf5a4cb894e91d0"` | `gzip` | `7645` | `0` |
| `/assets/index-CL8eXieL.css` (`gzip`, classic) | `GET` | `200` | `private, max-age=31536000, immutable` | `W/"cc0fff5cbe40285a7799d4b2fbfefa5d"` | `gzip` | `19223` | `0` |
| `/next/main-6EQG52GH.js` | `POST` | `401` | `no-store` | `-` | `-` | `78` | `1` |
| `/next/main-6EQG52GH.js?v=1` | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/main-6EQG52GH.js?` (bare `?`) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next//chunk-2GC3TG4U.js` | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/%2e/chunk-2GC3TG4U.js` | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/%6dain-6EQG52GH.js` (`%6d` = `m`) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/./chunk-2GC3TG4U.js` | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/main-6EQG52GH.js/` (trailing `/`) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/main-6EQG52GH.js.map` | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/chunk-ZZZZZZZZ.js` (missing chunk) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/index.html` | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/index.html` | `HEAD` | `401` | `no-store` | `-` | `-` | `0` | `1` |
| `/next/` (SPA root) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/inbox` (SPA route) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/next/grain-favicon-light.svg` (unhashed) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/` (classic root) | `GET` | `401` | `no-store` | `-` | `-` | `1266` | `1` |
| `/api/tasks` | `GET` | `401` | `no-store` | `-` | `-` | `78` | `1` |
| `HubTunnelHandler` `/next/main-6EQG52GH.js` (refused) | `GET` | `401` | `no-store` | `-` | `-` | `0` | `1` |
| `HubTunnelHandler` `/next/main-6EQG52GH.js` (admitted) | `GET` | `403` | `no-store` | `-` | `-` | `1683` | `1` |

When authenticated, missing asset paths (`/next/chunk-ZZZZZZZZ.js`, `/next/main-6EQG52GH.js.map`, `/assets/missing-12345678.js`) all returned **`404` `Cache-Control: no-store`** (`text/plain; charset=utf-8`), never falling back to `index.html`.

### Item 4 — Browser caching end to end & simulated upgrade with open tab
- **Cold vs. warm HAR (`cold-trusted.har` vs. `warm-trusted.har`):** **PASS.**
  - Cold load: `85` requests reached the proxy (`69` hashed JS/CSS bundles with `private, max-age=31536000, immutable`), `fullVerifies = 1`.
  - Warm load: `GET /next/inbox` sent `If-None-Match` and received **`304 Not Modified`** (`Cache-Control: no-cache`, `ETag: "0981c7bd619db0a35a7b704720002b9a"`); **`0` of `69`** hashed bundles hit the server (all served from Chromium's disk cache); `/api/tasks` and `/api/tasks/2` also revalidated with `304`; `fullVerifies = 0`.
- **Does an upgrade reach the browser on the next load without a hard refresh?** **YES — PASS.** Because `/next/*` SPA HTML responses carry `Cache-Control: no-cache` + a startup-computed strong `ETag`, an ordinary `page.reload()` (no hard refresh) after rebuilding `ui-ng` (`main-6EQG52GH.js` → `main-FQXRCSFG.js`) received `200 OK` on `/next/inbox` and immediately fetched `/next/main-FQXRCSFG.js`.
- **Stale open tab after upgrade (404 chunk):** **BUG FOUND & FIXED (Bug #2 — UX).**
  - **Defect found on `1d52982bb`:** When an open tab had not yet preloaded a lazy route chunk (under `Save-Data`, within `1.5 s` of boot, or when `IdleRoutePreload` failed during a daemon restart), clicking **Tasks** in the rail fetched `/next/chunk-MJYP2YLY.js` → `404 no-store`, emitted `NavigationError`, threw an unhandled promise rejection, and showed **nothing on screen** (`snackbarShown: false`, URL stuck at `/next/inbox`).
  - **Post-fix:** `staleChunkErrorHandler` (`withNavigationErrorHandler` in `ui-ng/src/app/shell/route-preload.ts`) catches the failed route load without an unhandled rejection and displays `"Page could not load — Grain may have updated. Reload to open it."` with a **Reload** action button (`snackbarShown: true`, `snackbarHasActionBtn: true`), which navigates cleanly to `/next/tasks` (`urlAfterReloadAction: "/next/tasks"`). `@defer` chunk failures (`onDeferError`) now also carry the **Reload** button.

### Item 5 — Retry storm behaviour (`503 verifier_unavailable`)
- **Transient blip WITH `Retry-After: 5` (`7.5 s` outage, then recovery):** **PASS.** `0` error banners shown during recovery; concurrent reads shared a single backoff timer (`9` total `503`s across initial + shared retry waves); recovered cleanly on its own.
- **POST during blip:** **PASS.** Submitting `New task` (`POST /api/tasks`) during the `503` blip made exactly **`1`** `POST` request (zero retries) and surfaced the refusal in **`74 ms`**: *"The task was not filed: sign-in verification could not check your identity right now; retrying shortly (ref …)"*.
- **Transient blip WITHOUT `Retry-After` (`4.5 s` outage, header stripped at proxy):** **PASS.** Retried quietly on the `2 s ± 30%` default timer with `0` error banners (`2` `503`s total).
- **Sustained `503 verifier_unavailable` outage:** **PASS.** Raised exactly **`1`** banner at `27.9–29.1 s` (*"The task list could not be read: sign-in verification could not check your identity right now; retrying shortly (ref …). This page tries again on its own."*), stayed at `1` banner across subsequent failed polls, and cleared automatically when `slow-proxy` recovered.

### Item 6 — Navigation progress at phone width (`390×844`) & `Save-Data`
- **`g-nav-progress` at `390×844`:** **PASS.** Holding `/next/chunk-*.js` for `900 ms` and tapping **Tasks** in the phone compact bottom bar showed `[data-testid="nav-progress"]` in `186–199 ms` spanning `{ x: 0, y: 0, width: 390, height: 3 }` (`position: fixed`, `pointer-events: none`, `z-index: 1000`), clearing to `0` elements once navigation landed.
- **Preloading under `Save-Data`:** **BUG FOUND & FIXED (Bug #3 — `Save-Data`).**
  - **Defect found on `1d52982bb`:** `IdleRoutePreload` skipped all lazy route chunks under `navigator.connection.saveData = true`, but `AppComponent`'s `@defer (when createTask.isOpen(); prefetch on idle)` unconditionally prefetched `NewTaskSheetComponent` (`3` extra chunks: `44 → 47` after `3.2 s`).
  - **Post-fix:** Gating `@defer (...; prefetch when idlePrefetch())` on `!saveDataEnabled()` reduced post-boot idle chunk requests under `Save-Data` to **`0`** (`41 → 41`).

---

## 4. Bugs Found, Deterministic Reproductions & Fixes

1. **Bug #1 (Security): `/staging/{name}/...` forwarded signed identity assertions and proxy tokens to staging sandboxes**
   - **Reproduce on `1d52982bb`:** Start `grain-slowverifier` with persistent sandbox `preview` (`staging_echo.py`) and run `curl -sk https://127.0.0.1:4292/staging/preview/echo`: response JSON includes `"x-slow-proxy-assertion": "v1|alice@example.com|..."` (and likewise `X-Goog-IAP-JWT-Assertion` / `X-Grain-Hub-Assertion` / `Proxy-Authorization`).
   - **Fix:** `stripStagingCredentials` in `pkg/ui/persistent_sandboxes.go` strips `Authorization`, `Proxy-Authorization`, `X-Grain-Tunnel-Token`, `X-Grain-Access-Code`, `X-Grain-Access-Name`, `X-Grain-Acting-Task`, `proxyIdentityHeaders`, `PlaintextIdentityHeaders`, every registered `IdentityVerifier`'s `Headers()`, and `grain_*` cookies.
   - **Regression tests:** `TestStagingProxyStripsVerifierAssertionsAndProxyCredentials` (`pkg/ui/persistent_sandboxes_test.go`) and `TestStagingProxyNeverForwardsSignedIdentityAssertionsOrGrainCookies` (`tests/security/staging_proxy_credentials_test.go`).

2. **Bug #2 (UX): Stale open `/next/` tab after daemon upgrade silently ignored clicks on unpreloaded lazy routes**
   - **Reproduce on `1d52982bb`:** Run `walk_upgrade.mjs` (opens `/next/inbox` under `Save-Data`, rebuilds `ui-ng` with new chunk hashes, restarts daemon, clicks **Tasks**): `/next/chunk-MJYP2YLY.js` answers `404 no-store` and `snackbarShown` is `false` while URL stays `/next/inbox`.
   - **Fix:** `staleChunkErrorHandler` (`withNavigationErrorHandler` in `ui-ng/src/app/shell/route-preload.ts` + `app.config.ts`) and `AppComponent.onDeferError` (`ui-ng/src/app/app.component.ts`) surface a snackbar with a **Reload** action button (`resolveReloadHref` guarding against `//` and `/\` open redirects).
   - **Regression tests:** `ui-ng/src/app/app.component.spec.ts` and `ui-ng/e2e/next-stale-chunk-upgrade.spec.ts` (`@phone @dark`).

3. **Bug #3 (`Save-Data`): Shell `@defer` prefetched `NewTaskSheetComponent` chunks on idle under `Save-Data`**
   - **Reproduce on `1d52982bb`:** Open `/next/inbox` with `navigator.connection = { saveData: true }` and wait `3.2 s`: `/next/chunk-*.js` count rises by `+3` (`44 → 47`).
   - **Fix:** Gate `AppComponent.idlePrefetch` on `!saveDataEnabled()` (`ui-ng/src/app/app.component.ts`, `ui-ng/src/app/shell/route-preload.ts`).
   - **Regression tests:** `ui-ng/src/app/app.component.spec.ts` and `ui-ng/e2e/next-stale-chunk-upgrade.spec.ts`.
