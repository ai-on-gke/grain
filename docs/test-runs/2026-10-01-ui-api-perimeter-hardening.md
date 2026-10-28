# UI/API perimeter hardening: real-browser walkthrough (2026-10-01)

Task 1412 ran §3 of the 2026-10-01 test plan: the Web UI/API perimeter
hardening that is on `main` only. That covers the Host guard
(`e1288b880`), CSRF (`06afc2771`), CSP and security headers
(`0e3fb0426`), loopback-only unauthenticated access and
`-ui-trusted-cidrs` (`e4b4f755c`), attachment serving (`e7c55b578`),
body limits (`6a73e1620`), sanitized 5xx (`98d50c673`), panic recovery
(`1f580349e`), admin-only database routes (`5b165faa6`) and the removal
of `?user=` identity (`39bfc6d6f`).

**Plan source.** The task said to read
`docs/test-plans/2026-10-01.md` from branch `grain/task-1353`. That
branch does not exist on the remote, and `refs/pull/1353` is an
unrelated PR that has no such file. I ran the steps the task itself
spells out for §3, which list every check below.

## Environment

| | |
|---|---|
| Base | `main` at `9e265140f` (all ten commits above confirmed with `git branch -r --contains`) |
| OS | Debian GNU/Linux 12 (bookworm), kernel 6.1.0-53-amd64, x86_64 sandbox |
| Go / Node | go1.26.6, Node v20.19.0 |
| Browser | Playwright 1.62.1, Chromium 151.0.7922.34 (headless), `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| UI build | `cd ui && npm ci && npm run build` (into `pkg/ui/static`), then `go build -o /tmp/grain ./cmd/grain` |

Servers used:

- `grain demo -addr 127.0.0.1:18420 -open=false` for the CSP sweep,
  attachments, CSRF, limits and the database route.
- `grain demo -addr 127.0.0.1:8420` with `npx vite --port 5173` (the
  `npm run dev` server) in front of it.
- `grain daemon -data-dir … -disable-native-sandboxes -ui-addr 127.0.0.1:18430 -ui-allowed-hosts '*.example'`
  for the Host allow-list.
- `grain daemon … -ui-addr 0.0.0.0:18431 -as operator`, run first
  without and then with `-ui-trusted-cidrs 10.250.0.0/24`, reached from a
  network namespace `evilns` (veth `10.250.0.2` → host `10.250.0.1`).
- `grain daemon … -ui-addr 127.0.0.1:18432` over a deliberately damaged
  copy of that store, to force a 5xx.

Stubs: none of grain's APIs or React UI were mocked. The demo has no
GitHub, so `GET /api/repos/acme/widgets/github-branches` answers 404 on
every task pane. That is the external boundary, not the perimeter work,
and it is the one console error the spec tolerates (matched by URL).

## Status-code matrix

| Request | Expected | Observed |
|---|---|---|
| `GET /api/config`, `Host: evil.example` (demo, 127.0.0.1) | 421 | 421 |
| `GET /api/config`, `Host: localhost` / `127.0.0.1:port` | 200 | 200 / 200 |
| daemon `-ui-allowed-hosts '*.example'`: `a.example`, `b.a.example` | 200 | 200, 200 |
| … `evil.example.com`, `notexample` | 421 | 421, 421 |
| … `localhost` | 200 | 200 |
| cross-site `no-cors` text/plain `fetch` POST `/api/teams` | 403, no team | 403, no team |
| cross-site `no-cors` POST `/api/tasks/3/close` | 403, task open | 403, still `running` |
| cross-site `<form enctype=text/plain>` POST `/api/tasks/2/close` | 403, task open | 403, still `queued` |
| curl (no Origin) POST `/api/teams`, `/api/tasks/3/close` | 201 / 200 | 201 / 200 |
| `grain -server … close 4` (CLI) | closes | closed |
| curl `Sec-Fetch-Site: cross-site` POST `/api/teams` | 403 | 403 |
| UI Close button on task (same origin) | closes | closed |
| netns → `0.0.0.0` daemon: `GET /api/tasks` | 401 | 401 |
| netns: `GET /api/config` | 200 | 200 |
| netns: `GET /` (HTML shell) | 200 | 200 |
| netns: `/api/tasks` with `X-Grain-User` / `X-Forwarded-User` / `?user=` | 401 | 401 / 401 / 401 |
| netns: POST `/api/teams` | 401 | 401 |
| host via its own non-loopback IP `10.250.0.1` | 401 | 401 |
| loopback `GET /api/tasks` | 200 | 200 |
| netns, with `-ui-trusted-cidrs 10.250.0.0/24`: `GET /api/tasks` | 200 | 200 |
| … `X-Grain-User: bob` on `/api/config` | believed | `actor: bob` |
| … POST `/api/teams` | 201 | 201 |
| loopback `/api/config?user=alice` | ignored | actor stays `debian` |
| POST `/api/database/test`, header user `carol` (has access, not admin) | 403 | 403 "does not have administrator privileges" |
| … `bob` (no access at all) | 403 | 403 "does not have access" |
| … admin, DSN at `127.0.0.1:1` | generic error | 400 "could not connect to PostgreSQL with the supplied settings; see the daemon log", with the password masked in the log |
| POST `/api/database/upgrade`, non-admin | 403 | 403 |
| PUT `/api/settings`, 9 MiB JSON | 413 | 413 |
| **POST `/api/tasks`, 9 MiB description, no attachment** | 413 | **201 on `main`** (finding 1); 413 after the fix |
| POST `/api/tasks`, 10 × 10 MiB attachments (139.8 MB body) | 201 | 201 in 1.3 s |
| POST `/api/tasks`, one 10 MiB + 1 byte attachment | 400 | 400 "over the 10485760 byte limit" |
| `GET /api/boards` with the `board` tables dropped | 500 `{error, code, ref}` | 500 `{"error":"internal server error (ref 3b0a1a4e3dc7)","code":"internal","ref":"3b0a1a4e3dc7"}`; daemon log: `api: 500 internal [ref 3b0a1a4e3dc7]: listing boards: SQL logic error: no such table: board` |
| attachment `text/html` | download | `application/octet-stream`, `attachment`, sandbox CSP |
| attachment `image/svg+xml` | download | `application/octet-stream`, `attachment`, sandbox CSP |
| attachment `image/png` | inline | `image/png`, `inline` |

## Steps and observations

### CSP sweep and headers

In Chromium I visited `/`, `/boards`, the task pane of a task whose body
has Markdown images over `https:` and `http:`, plus external, internal
and `javascript:` links and a raw `<img onerror>`. I also visited
`/tasks/1`, `/ci`, `/repos`, `/repos/acme/widgets`, `/teams`,
`/teams/default`, `/settings`, `/system`, `/merge-queue` and an unknown
path. A `securitypolicyviolation` listener was installed by init script
alongside console-error, `pageerror` and `dialog` listeners.

- Only one view had a CSP violation: the task pane, on the `http:`
  Markdown image (finding 2).
- `index.html` has no inline script. The built bundle is one module
  script plus a stylesheet, and MUI/emotion's runtime `<style>` is
  covered by `style-src 'unsafe-inline'`.
- No `alert` dialog ever fired. The `javascript:` link renders with an
  empty `href`, and raw HTML in Markdown is not rendered. Links get
  `target=_blank rel="noopener noreferrer"`. Data-URI Markdown images
  render with an empty `src` because react-markdown's URL transform
  drops them, so they never reach the CSP.
- HTML, API JSON and 404 responses all carry the full CSP plus
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: same-origin`, `Cross-Origin-Opener-Policy: same-origin`
  and `Permissions-Policy`. Attachment responses keep those and replace
  the CSP with `default-src 'none'; style-src 'unsafe-inline'; sandbox`.

### `npm run dev`

`npx vite --port 5173` in front of a demo on `127.0.0.1:8420` worked.
The Vite proxy forwards `Host: localhost:5173`, which the guard admits.
The browser's requests are same-origin to Vite, so CSRF admits them too.
I loaded `/ci`, `/settings`, `/system` and `/`, then closed a task with
the UI's Close button, and the demo recorded it as `closed`. Vite's dev
HTML carries no CSP, so the dev server does not exercise the policy;
only the built bundle does.

### Host guard

Matrix above. On a `0.0.0.0` listener with no `-ui-allowed-hosts`, Host
is not checked (`Host: evil.example` → 200). That is the documented
behaviour, and the daemon logs at startup: "-ui-addr 0.0.0.0:18431
listens on every interface and -ui-allowed-hosts is unset, so Host
headers are not checked …".

### CSRF

The attacker page was served by `python3 -m http.server` on
`localhost:18999`, a different site from the daemon's `127.0.0.1`. It
issued two `no-cors` text/plain `fetch` POSTs (create team, close task)
and submitted a `text/plain` form (close task) into an iframe. The
daemon answered 403 to all three. No team was created and both tasks
kept their state. The same actions from curl, the CLI and the real UI
all went through.

### Trusted sources

Matrix above. From an untrusted source, the unauthenticated
`/api/config` answers 200 with `authenticated:false` and `allowed:true`.
It names the daemon's own actor (`"actor":"operator"`) and lists the
capability catalogue with each one's readiness and missing
prerequisites. The plan expects that endpoint to stay open, so I am only
noting what it discloses (see "Other observations").

### Attachments

Opening the `text/html` and `image/svg+xml` attachment URLs in a tab
started a download (`evil.html`, `evil.svg`). Neither rendered, and
neither script ran. The png opened in the task pane's image viewer and
drew (`naturalWidth` 1). The svg chip also opened the viewer, which
showed a broken image (`naturalWidth` 0) (finding 3).

### Panic recovery

There is no route that panics on demand in a real server, so this was
not exercised in the browser. `go test ./pkg/ui -run TestServeRecovered`
passes.

## Findings

### 1. Attachment routes accepted any 140 MiB body, attachments or not (fixed)

Repro on `main`: `python3 -c 'import json;print(json.dumps({"title":"x","description":"a"*(9<<20)}))' > big9.json; curl -XPOST --data-binary @big9.json http://127.0.0.1:18420/api/tasks`
returns 201. `PUT /api/settings` with the same body returns 413.

Cause: `POST /api/tasks`, `POST /api/tasks/{id}/comments` and
`POST /api/tasks/{id}/messages` read their body with
`readJSONLimit(…, maxAttachmentJSONBody)`, about 141 MiB. That
allowance exists for the base64 attachments, but nothing held the rest
of the body to `maxJSONBody`. A description or comment of over 100 MiB
was stored and then served in every task listing.

Fix: `readAttachmentJSON` in `pkg/ui/tasks.go` counts the bytes it
decodes, subtracts the uploads' `Content`, and answers 413 when more
than `maxJSONBody` (8 MiB) is left. The maximum-attachment request
(10 × 10 MiB) still returns 201.

Tests: `TestAttachmentRoutesHoldNonAttachmentBytesToMaxJSONBody` in
`pkg/ui/perimeter_walkthrough_test.go`, and the last test in the e2e
spec.

### 2. CSP blocked Markdown images linked over plain http (fixed)

Repro: create a task whose description contains
`![x](http://example.com/y.png)` and open its pane. Chromium logs
`Loading the image 'http://example.com/y.png' violates … "img-src 'self' data: blob: https:"`
and fires `securitypolicyviolation`.

The policy's own comment says Markdown images may point "wherever their
author linked them". Plain-http internal dashboards and LAN hosts are
common. `https:` already admits every origin, and an image cannot run
script. So `pkg/ui/security_headers.go` now has
`img-src 'self' data: blob: http: https:`.

Tests: `TestContentSecurityPolicyAdmitsMarkdownImagesOverHTTP`, and the
e2e CSP sweep. With the old policy restored, the sweep fails with
`CSP img-src http://example.invalid/b.png`.

### 3. SVG attachments opened a broken image viewer (fixed)

Repro: attach an `image/svg+xml` file to a task, open the task pane and
click the chip. A full-screen viewer opens on a broken image.

`AttachmentLinks.jsx` treated every `image/*` as viewable, but since
`e7c55b578` the server sends SVG (and any image type besides
png/jpeg/gif/webp/bmp) as an `application/octet-stream` download. The
component now uses the server's inline list, so SVG gets the same
plain link as any other file. That link downloads it.

Tests: a vitest case in `AttachmentLinks.test.jsx`, and the e2e
attachment test, which asserts the svg is a link and the png preview
draws.

## Other observations (not fixed)

- **409 conflicts carry raw store errors.** With the SQLite write lock
  held by another process, `POST /api/teams` answered `409 {"error":"conflict:
  the store kept changing under this operation after 5 attempts (put team
  locked): database is locked (5) (SQLITE_BUSY)"}`. The 5xx sanitizing
  does not cover 4xx, so this one passes the driver's text through. It
  is low-sensitivity, but it is the kind of detail `98d50c673` keeps out
  of 5xx responses.
- **Unauthenticated `/api/config`** names the daemon actor and lists
  the capability catalogue with readiness and missing prerequisites. The
  plan expects this endpoint to stay open. Whether it should say less to
  a caller who is not signed in is a design call for a human.
- **A store-write lock did not stop `POST /api/tasks`** (201 while
  `/api/teams` got 409), so a write lock was not a usable way to force a
  5xx. I forced it by dropping the `board` tables in the throwaway store
  instead. `-db-dsn "file:…?mode=ro"` cannot be used for this, because
  the sqlite driver appends `/grain.db` to the DSN.

## Skipped

- **Docker as the non-loopback source.** I used a network namespace
  (`ip netns`, veth pair) instead. It gives the same thing: a real
  non-loopback source address.
- **Panic recovery in a live server.** No route panics on demand; it is
  covered by `TestServeRecoveredSanitizesPanic`.
- **The plan document itself.** It is unavailable, as described above.

## Regression coverage added

- `ui/e2e/security-perimeter-walkthrough.spec.js` has six tests: headers,
  CSP sweep, Host guard, CSRF from a second port with the UI and a
  no-Origin client still writing, attachments, and body limits.
- `pkg/ui/perimeter_walkthrough_test.go` has the body limit and `img-src`
  tests.
- `ui/src/components/AttachmentLinks.test.jsx` has the SVG-is-a-link
  case.

Commands run: `npm run format`, `make security` (go vet plus
`tests/security`, which passed), `go test ./pkg/ui` (passed),
`npx vitest run src/components/AttachmentLinks.test.jsx` (passed), and
`npx playwright test e2e/security-perimeter-walkthrough.spec.js --project chromium`
(6 passed).
