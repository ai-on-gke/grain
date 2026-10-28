# Test Run: Transparent Package Cache Playwright Browser CDN and Caching Walkthrough

## Overview
Comprehensive test run and end-to-end verification for Transparent Package Cache Playwright Browser CDN interception, dynamic TLS certificate issuance, immutable browser archive caching, HTTP 206 Partial Content range request pass-through, local cache purging, offline cache hit verification, and headless browser execution as defined in `docs/test-plans/2026-09-25.md §4`.

## Scope and Environment

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/task-667`
- **Host**: Debian GNU/Linux (Trixie), Linux x86_64
- **Runtime Toolchains**: Go `1.26.6`, Node `20.19.0`, npm `10.8.2`, Playwright `1.62.1`
- **Package Cache Architecture**:
  - `pkg/pkgcache.DNSServer`: Embedded DNS resolver intercepting package registry and browser CDN domains to controller IP (`172.17.0.1`).
  - `pkg/pkgcache.CA`: Dynamic TLS certificate authority issuing domain certificates on HTTPS (port 443).
  - `pkg/pkgcache.Server`: Transparent HTTP (port 80) and HTTPS (port 443) proxy engine routing requests to ecosystem drivers (`KindPlaywright`, `KindAPT`, `KindPyPI`, `KindDocker`, `KindNPM`, `KindGo`, `KindGitHub`).
  - `pkg/pkgcache.Cache`: Content-addressed filesystem cache with SHA-256 verification and per-kind metrics.
- **Frontend & Web UI**: React 18, Vite 6, MUI 6 (`ui/src/components/PackageCachePanel.jsx`, `ui/src/components/CachesPage.jsx`).

---

## Walkthrough Coverage & Scenarios Exercised

### 1. Transparent DNS Interception & Host Routing
- **Playwright CDN Domain Coverage**:
  - Validated that `DefaultInterceptedDomains` in `pkg/pkgcache/dns.go` and `DefaultPlaywrightHosts` in `pkg/pkgcache/playwright.go` include:
    - `cdn.playwright.dev` (Primary Playwright CDN)
    - `playwright.azureedge.net` (Legacy Azure CDN endpoint)
    - `playwright.download.prss.microsoft.com` (Microsoft Azure Front Door fallback CDN)
- **Resolver Behavior**:
  - Under `pkgcache.DNSServer` with standard configuration, queries for Playwright domains resolve to `172.17.0.1`.
  - Non-intercepted domains (e.g. `example.com`) are forwarded upstream in normal egress mode.
  - In `ZeroEgress` mode, non-intercepted domains return `NXDOMAIN` (RCode 3), preventing outbound egress leaks.

### 2. TLS Interception & Certificate Authority Trust
- **Dynamic Certificate Minting**:
  - Verified `pkgcache.CA` dynamically mints valid TLS certificates for `cdn.playwright.dev`, `playwright.azureedge.net`, and `playwright.download.prss.microsoft.com` upon TLS handshake on port 443.
  - Tested client TLS verification with CA Root PEM pool (`pkgcache.CA.RootCertPEM()`) matching `NODE_EXTRA_CA_CERTS` / sandbox trust store injection (`orchestrator.ConfigurePackageCacheTransparent`).

### 3. Playwright Browser Download & Immutable Caching
- **Archive Pattern Identification**:
  - `playwrightCacheable(path)` correctly classifies immutable browser distribution archives under `/builds/...` matching `.zip`, `.tar.gz`, and `.tgz`:
    - `https://cdn.playwright.dev/builds/cft/151.0.7922.34/linux64/chrome-linux64.zip` (Chromium)
    - `https://cdn.playwright.dev/builds/cft/151.0.7922.34/linux64/chrome-headless-shell-linux64.zip` (Chrome Headless Shell)
    - `https://cdn.playwright.dev/dbazure/download/playwright/builds/firefox/1538/firefox-debian-12.zip` (Firefox)
    - `https://cdn.playwright.dev/dbazure/download/playwright/builds/webkit/2336/webkit-debian-12.zip` (WebKit)
    - `https://cdn.playwright.dev/dbazure/download/playwright/builds/ffmpeg/1011/ffmpeg-linux.zip` (FFmpeg)
- **Initial Download (Cache Miss)**:
  - On the first request, the proxy connects upstream, streams the response to the client with `X-Grain-Package-Cache: miss`, and atomically stores the body into disk cache under `KindPlaywright` (`playwright/` subdirectory).
  - Verified storage stats via `cache.Stats()`, confirming entries count and total bytes stored.

### 4. Local Sandbox Cache Invalidation & Instant Cache Hit
- **Local Cache Purge**:
  - Purged local sandbox Playwright cache directory (`rm -rf ~/.cache/ms-playwright`).
- **Second Download (Cache Hit)**:
  - Re-requested browser downloads.
  - Verified that all browser binary archives were served directly from the local package cache with `X-Grain-Package-Cache: hit` and matching `Content-Length` / `Content-Type`.
  - Confirmed 0 upstream HTTP requests were made on the second download pass.

### 5. HTTP Range Requests (206 Partial Content) Pass-Through
- **Byte-Range Request Handling**:
  - Executed GET requests containing `Range: bytes=0-9` and `Range: bytes=10-20` headers.
  - Verified response returns HTTP `206 Partial Content` with `X-Grain-Package-Cache: pass`.
  - Confirmed range requests pass through upstream untouched and never write partial chunks to disk, preventing cache corruption or incomplete archive poisoning.

### 6. Non-Cacheable Assets & Path Traversal Refusals
- **Metadata and Dynamic Requests**:
  - Non-archive paths (e.g. `/builds/manifest.json`, `/index.html`) return HTTP 200 with `X-Grain-Package-Cache: pass` and are not stored in the cache.
  - HTTP `HEAD` requests return headers with body length 0 and do not populate cache entries.
- **Security & Path Traversal Guards**:
  - Paths containing `..` (e.g. `/builds/../secret` or `/playwright/../secret`) are rejected with HTTP 400 Bad Request (`grain's package cache will not follow a path containing ".."`) before any upstream dispatch.

### 7. End-to-End Headless Browser Execution
- **Binary Integrity & Execution**:
  - Validated that extracted Chromium binaries execute in headless mode:
    - Launched Chromium headless browser instance via Playwright API.
    - Rendered DOM tree, manipulated elements, and evaluated script expressions.
    - Verified clean termination and process lifecycle management.

### 8. Web UI & Proxy Cache Management
- **UI Presentation**:
  - Updated `ui/src/components/PackageCachePanel.jsx` `KIND_LABELS` to include `playwright: "Playwright browsers"` and formatted plural nouns as `X browsers`.
  - Verified `GET /api/package-cache` stats display on System > Caches page.
  - Verified single-kind flush (`POST /api/package-cache/flush?kind=playwright`) and global cache flush (`POST /api/package-cache/flush`) clear stored Playwright archives and reset disk usage counters.

---

## Bugs Found & Improvements Implemented

1. **Missing Microsoft Fallback CDN in Default Intercepted Hosts (`pkg/pkgcache/dns.go`, `pkg/pkgcache/playwright.go`)**:
   - **Issue**: Modern Playwright versions (`@playwright/test` >= 1.50) configure fallback mirrors pointing to `https://playwright.download.prss.microsoft.com/dbazure/download/playwright`. If `cdn.playwright.dev` experienced transient latency or DNS issues in restricted sandboxes, fallback downloads failed because `playwright.download.prss.microsoft.com` was missing from `DefaultInterceptedDomains` and `DefaultPlaywrightHosts`.
   - **Fix**: Added `playwright.download.prss.microsoft.com` to `DefaultInterceptedDomains` in `pkg/pkgcache/dns.go` and `DefaultPlaywrightHosts` in `pkg/pkgcache/playwright.go`.

2. **Web UI Ecosystem Label for Playwright (`ui/src/components/PackageCachePanel.jsx`)**:
   - **Issue**: When `KindPlaywright` entries existed in the package cache, the UI fell back to raw kind name "playwright" with generic noun "package" ("playwright: 3 packages").
   - **Fix**: Added `playwright: "Playwright browsers"` to `KIND_LABELS` and noun `"browser"` in `cacheSummary()`.

3. **Expanded Test Suite Coverage (`pkg/pkgcache/playwright_test.go`, `ui/src/components/PackageCachePanel.test.jsx`)**:
   - Added test coverage for `playwright.download.prss.microsoft.com` transparent proxying and caching.
   - Added `TestPlaywrightDNSInterception` verifying DNS resolution across all three CDN domains and NXDOMAIN enforcement under `ZeroEgress`.
   - Added Vitest assertions for `Playwright browsers` rendering in `PackageCachePanel.test.jsx`.

---

## Test Execution Summary

| Suite / Test Target | Command | Result |
| :--- | :--- | :--- |
| **Package Cache Playwright Tests** | `go test -v ./pkg/pkgcache/... -run TestPlaywright` | **PASS** (9/9 tests) |
| **Full Package Cache Suite** | `go test ./pkg/pkgcache/...` | **PASS** |
| **Daemon & CLI Integration** | `go test ./cmd/grain/...` | **PASS** |
| **UI Package Cache Component Tests**| `npm --prefix ui test src/components/PackageCachePanel.test.jsx` | **PASS** (5/5 tests) |
| **Full UI Vitest Suite** | `npm --prefix ui test` | **PASS** (143 files, 2,542 tests) |
| **Playwright Headless Browser Launch**| `node -e 'const { chromium } = require("playwright-core"); ...'` | **PASS** |

### Detailed Test Log: `go test -v ./pkg/pkgcache/... -run TestPlaywright`
```
=== RUN   TestPlaywrightBinaryCached
--- PASS: TestPlaywrightBinaryCached (0.00s)
=== RUN   TestPlaywrightNonCacheablePassThrough
--- PASS: TestPlaywrightNonCacheablePassThrough (0.00s)
=== RUN   TestPlaywrightRangeRequest
--- PASS: TestPlaywrightRangeRequest (0.00s)
=== RUN   TestPlaywrightHEADRequest
--- PASS: TestPlaywrightHEADRequest (0.00s)
=== RUN   TestPlaywrightPathTraversalRefusal
--- PASS: TestPlaywrightPathTraversalRefusal (0.00s)
=== RUN   TestPlaywrightLegacyPrefix
--- PASS: TestPlaywrightLegacyPrefix (0.00s)
=== RUN   TestPlaywrightFlush
--- PASS: TestPlaywrightFlush (0.00s)
=== RUN   TestPlaywrightTLSInterception
--- PASS: TestPlaywrightTLSInterception (0.00s)
=== RUN   TestPlaywrightDNSInterception
--- PASS: TestPlaywrightDNSInterception (0.00s)
PASS
ok  	github.com/ai-on-gke/grain/pkg/pkgcache	0.018s
```

---

## Conclusion
The Transparent Package Cache Playwright Browser CDN interception, TLS dynamic certificate generation, immutable binary archive caching, range request pass-through, local cache invalidation, and headless browser execution are verified, robust, and conform to the 2026-09-25 release test plan.
