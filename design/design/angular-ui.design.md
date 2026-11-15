# A new UI in Angular, built beside the current one

> How the Angular UI looks, behaves, is tested and is reviewed is in
> [`ui-design-book.design.md`](ui-design-book.design.md). This document
> covers why it exists, how it is served, and how pages move over.

> **Proposal, with its first slice built.** This document describes how
> grain's web UI is rewritten in Angular one page at a time, alongside the
> React UI in `ui/`, until the old one can be retired. The foundation
> (`ui-ng/`, served at `/next/`) and the first page, the Inbox, are
> implemented with it. What the reference design asks for that the API
> cannot do yet is listed in *Gaps*, and the later slices in *Order of
> later pieces*.

## Why, and why beside

The current UI is React 18 and Vite in `ui/`, embedded into the daemon
from `pkg/ui/static` and served by `pkg/ui` with a single-page fallback.
It has grown page by page, and a new visual design -- a Material 3 style
system with a coplanar list-and-inspector layout for triage pages -- is
easier to build fresh than to retrofit. A rewrite that replaced the whole
UI at once would be months with nothing shippable in the middle, and
every page of the old UI would keep changing under it.

So the new UI is a separate application that ships *with* the old one,
in the same binary, and takes over one page at a time. Both are always
deployed together; a person can use either for any page that exists in
both; when the last page has moved, the old one is deleted.

## Coexistence

### Where it lives and how it is built

- **Source:** `ui-ng/`, an Angular workspace of its own (its own
  `package.json` and lockfile), next to `ui/`, `ui-hub/` and
  `ui-shared/`.
- **Build:** `make frontend` builds `ui-ng/` after `ui/` with the same
  recipe -- clear the output directory except its three tracked files,
  then `npm ci --omit=dev && npm run build`. The Angular build packages
  (`@angular/cli`, `@angular/build`, the compiler and TypeScript) are in
  `dependencies` rather than `devDependencies` for the reason `ui/` keeps
  Vite there: the image build installs with `--omit=dev`.
- **Output:** `pkg/ui/static-next/`, which `angular.json` writes straight
  into (`outputPath.base`, flat `browser` output, `baseHref: /next/`).
  Like `pkg/ui/static/` it is ignored by git apart from `.gitignore`,
  `.gitkeep` and `placeholder.html`, the last of which exists so
  `//go:embed` compiles on a checkout that has never built the frontend.

### How the daemon serves it

`pkg/ui/server.go` embeds the directory (`//go:embed static-next`) and
the one `"/"` catch-all route that served the React UI now serves both
through `frontendHandler`:

- `GET /next` redirects (307) to `/next/`, keeping the query;
- any path under `/next/` is served from `static-next/` with the same
  `spaHandler` the React UI uses: a real file is served as-is, and
  anything else gets `static-next/index.html`, so a deep link like
  `/next/inbox` or a reload works;
- everything else is the React UI exactly as before.

`/api/...` routes are registered on the mux ahead of the catch-all and
are untouched; `/next/api/...` does not exist -- the new UI calls the
same `/api/...` the old one does.

### No new authentication path

The new UI is the same origin, served by the same handler chain, so
everything that guards the old UI guards it too, with no change:

- `ServeHTTP`'s security headers, including the Content-Security-Policy
  (`script-src 'self'`). The Angular build is configured not to inline
  critical CSS, because that technique uses an inline `onload` handler
  the policy forbids; `index.html` carries no inline script.
- The CSRF check (`pkg/ui/csrf.go`): requests are same-origin `fetch`es,
  so `Sec-Fetch-Site: same-origin` is what admits a write, as for the
  React UI. There is no token to carry.
- The access-code gate and verified-identity admission. Signed out, a
  request for `/next/inbox` gets the same "Enter access code" page with a
  401, and signing in returns to `/next/inbox`
  (`pkg/ui/server_next_test.go`). The session cookie is the one the old
  UI uses, so moving between the two never asks the person to sign in
  again.
- Role checks. Every API call is authorized by the server as before; the
  new UI only decides what to *offer* (see *Role-aware actions*).

The trust-boundary review of this change is therefore about one thing:
that `/next/` is served from embedded, build-time files through the same
chain, and that nothing under it is reachable without it.
`server_next_internal_test.go` pins the routing (including that
`/nextish` is still the React UI and a missing build 404s rather than
serving the placeholder).

## Moving pages over

### Switching per page

- **From the old UI to the new:** each page that has a new version
  carries a small link to it. The classic Inbox shows "Try the new
  Inbox", linking to `/next/inbox`.
- **From the new UI to the old:** each new page carries a "Classic …"
  link to its old counterpart with `?classic=1`, and the new shell's
  footer links to the classic UI's home.
- **Destinations not moved yet:** the new shell's navigation rail lists
  every destination; those that have not moved are plain links into the
  classic UI (`/tasks`, `/settings`, …). Nobody is ever stranded in the
  new UI without a way to the page they want.

### A default

A person who prefers the new version can make it their default:
"Default to new UI" on a new page sets `grain.ui.preferNext` in the
browser's local storage, the same storage both UIs already share for the
theme (`grain.themeMode`). A classic page that has a new version reads
the key and, when it is set, replaces itself with the new page --
unless the address carries `?classic=1`, which is how the new page's
"Classic" link gets back without bouncing. It is per browser, like the
theme, which is the right scope while pages are moving: no server state,
nothing to migrate when the old UI goes.

When enough pages have moved that the new UI should be the default for
everyone, the default flips (the key's absence means "new"), and the
switch becomes "Prefer classic". A per-user server-side setting is not
needed for that and is not proposed.

### Migrating one page at a time

Each later slice is one task and one pull request that:

1. builds the page in `ui-ng/` under `/next/<path>`, lazy-loaded, with
   unit tests and a Playwright spec in `ui/e2e/` (axe included);
2. adds the "Try the new …" link to the classic page and the redirect for
   people who chose the new UI as default;
3. turns that destination in the new shell's rail from a classic link
   into a route.

The old page is left alone otherwise: bug fixes keep landing there until
the switch-over. Once every destination is a route, the classic UI's
links all point to `/next/...`, and a release has gone by, the classic
UI is removed: `ui/` and `pkg/ui/static/` go, `frontendHandler` serves
`static-next/` at `/` (with `/next/...` redirecting to its new path so
bookmarks keep working), and `baseHref` becomes `/`.

## Angular choices

- **Version:** Angular 19 (`~19.2`), the current long-term-support line.
  Angular 20 and later need Node 20.19+; the deployment image builds the
  frontend with Debian 12's packaged Node 18 (`Dockerfile`'s build stage,
  `Dockerfile.build`), which Angular 19 still supports. Moving to a newer
  major is a follow-up that also moves that image to a newer Node.
- **Standalone components only**, no NgModules. Templates are inline in
  the component file, which keeps a page reviewable as one file.
- **Signals for state.** Each page has a store (`InboxStore`) holding
  writable signals for what the server said and what the person chose,
  and `computed` signals for everything shown. Components use
  `ChangeDetectionStrategy.OnPush`. RxJS appears only at the edges
  (`HttpClient`, the event stream).
- **Routing:** `provideRouter` with every page lazy-loaded
  (`loadComponent`), so the shell's initial bundle does not grow with
  each page. Unknown paths go to the Inbox.
- **HTTP:** `provideHttpClient(withFetch())` and one typed client,
  `GrainApi` (below). Requests are same-origin with the session cookie.
- **Live updates:** `GrainEvents` holds one `EventSource` on
  `GET /api/events?since=now&stream=1`, shared by every subscriber, with
  reconnect and backoff. The server sends *named* frames
  (`task.*`, `notification.*` and `local_script.*`), which never reach
  `onmessage`, so each name is listened for explicitly. A page treats an
  event as "read again" rather than as state; a slow poll (60 s on the
  Inbox) backs the stream up for a dropped connection and for a
  host-script call that runs past its deadline unanswered.
- **Design tokens:** `ui-shared/tokens.css` is loaded first, so both UIs
  share base tokens and the `data-theme` switch; the new UI's sheet adds
  Material 3 colour roles (light and dark), shape, type and spacing
  tokens on top, and colours are only ever named in its `:root` blocks.
  The theme follows the shared `grain.themeMode` key, so changing it in
  one UI changes it in both.
- **Unit tests:** Vitest with Analog's Angular plugin and jsdom
  (`vitest.config.ts`) -- the same runner and assertion style `ui/`
  uses, so one way of writing a test across both. Angular's `TestBed`
  drives components; services and the classifier are tested as plain
  code.
- **End-to-end:** Playwright specs for `/next/` live in `ui/e2e/` and
  reuse its fixtures: a `grain demo` per worker, signed in with the
  access code, against the real built frontend. No second Playwright
  setup.
- **Lint and format:** ESLint 9 with `typescript-eslint` and
  `angular-eslint`, including its template accessibility rules applied
  to inline templates; Prettier at its defaults, as for `ui/`.
- **Build in CI and make:** `make frontend` builds it (so `make build`,
  `make test`, `make vet` and the image build all have it embedded);
  `make test` and the `frontend` CI job lint and unit-test it;
  `make fmt-frontend` checks its formatting; the `ui e2e` job's
  `make test-e2e-suite` runs its Playwright specs with the rest of
  `ui/e2e`. Every CI job that runs `make frontend` warms the npm cache
  for `ui-ng/` too, and the path filters treat `ui-ng/**` like `ui/**`.

## Typed API client

The server's route table is the source of truth, so the client is
generated from it rather than written by hand. `pkg/ui/ng_routes_test.go`
reads `Server.Routes()` -- the list built by the same calls that register
each route -- and `RoleScopeOf` for each, and writes
`ui-ng/src/app/api/generated-routes.ts`: every `"METHOD /path"` with the
scope a person's role needs (`"signed-in"`, a scope such as
`"tasks:admin"`, or `null` for operator-only). The test fails when the
file is stale; `GRAIN_UPDATE_NG_ROUTES=1 go test ./pkg/ui -run
TestAngularRouteTableInSync` regenerates it.

`GrainApi` takes those keys, not strings: `api.post("POST
/api/tasks/{id}/approve", { id })` fills and encodes the path
parameters, and a route that does not exist is a compile error. Request
and response bodies are typed by hand in `models.ts` for the fields the
new UI reads; generating those from the Go types is possible later, but
the route table is where drift has bitten.

## Role-aware actions

`AccessService` answers "may this person make this call?" from the role
`GET /api/config` reports (`access`: role, scopes, repository limits)
and the scope the generated table says the route needs -- the same rule
`ui/src/access.js` applies, keyed by route instead of a scope a page has
to know. A control the person cannot use is shown disabled, with the
reason the server would give ("Your role grain/viewer does not hold
tasks:admin…"), rather than hidden, so a viewer can see what exists and
who to ask. The server's own check is unchanged and authoritative.

## Accessibility

Every page's Playwright spec runs axe (`@axe-core/playwright`, as the
current UI's specs do) in light and dark themes and at a phone width,
and fails on any violation. Templates use real buttons and links, a
labelled checkbox per row for multi-select, `aria-pressed` on filter
chips, `aria-current` on the selected row, and a labelled modal dialog
with focus moved to its accept button. ESLint's template accessibility
rules catch the rest at lint time. Keyboard: `j`/`k` step through the
queue, `x` selects, `Esc` closes the inspector sheet.

## Bundle size

`angular.json` sets the production budget: **initial bundle 450 kB
warning, 550 kB error** (raw, before compression), and 8 kB / 16 kB per
component stylesheet. The foundation plus shell is about 365 kB raw
(about 96 kB transferred); the Inbox is a separate lazy chunk of about
25 kB. Pages are lazy so this budget covers the shell, not the app; a
page that would push a lazy chunk past 100 kB should split.

## The Inbox (slice 1)

### What it shows

The Inbox keeps everything the classic Inbox lists, classified the same
way (`inbox-classifier.ts` ports `waitsOnUser` and the classic Inbox's
groups, and `pendingReleaseOf` for releases):

- **Asked:** tasks parked on a question (reply, which resumes the run,
  or acknowledge, which closes it), on an access request (grant the
  ticked items, or deny with a required reason), or on a credential (a
  link to set it on the task -- the value is never shown);
- **Releases and host scripts:** pending release approvals with their
  soak and breaking-change notes, "Allow breaking changes" and "Allow
  short soak" switches, a confirmation that spells out what approving
  accepts, the failed-dispatch banner with its remedy and run link, and
  the release approval key on request; host-script calls with the exact
  script, arguments and stdin, to approve or deny;
- **Merges:** base-branch repairs, review questions, submits, blocked,
  GitHub-approval and stalled merges;
- **Proposals:** approve, defer, or decline (confirmed);
- **Blocked and alerts:** failed and stuck tasks, and open alerts
  (acknowledge, investigate, clear).

### Layout

After the reference design: a navigation rail, a top bar with "Filter
this view" and the theme switch, and a stage whose header says how many
decisions are waiting, with filter chips by kind and an "Unread" chip.
Below it the page splits into a **queue** of grouped rows on the left
and an **inspector** on the right showing the selected item: its status,
title and provenance, a *resolution dock* with the question and the
answers it takes, and a "Why this needs a person" card. Checking rows
brings up a batch bar offering the answers they share ("Approve all",
"Submit all", …), each confirmed. Under 1080 px the inspector becomes a
sheet over the queue; under 600 px the rail becomes a bottom navigation
bar and the sheet is full-screen.

### Read and cleared state

A row is *unread* until it has been opened in the inspector; the set of
read rows is kept per browser (`grain.next.inbox.seen`), pruned to rows
still waiting. An alert that has been acknowledged stays listed, marked
"acknowledged", until it is cleared; clearing removes it until it is
raised again. Answered rows leave the queue when the server stops
listing them, never on a guess.

## Gaps

What the reference design shows that the API does not support yet, and
which is left out of slice 1. Two earlier gaps are closed (task 4284):
`notification.*` and `local_script.*` events now reach the Inbox at
once, and the inspector shows the first hunks of a change's diff
(`GET /api/tasks/{id}/diff-preview`) beside a submit, review or merge
decision, or any task entry whose task has a pull request.

- **Agent state on the resolution dock** ("anticipating", "processing")
  and **undo** after a batch close need run-state and undo support the
  API does not have; the dock is static and closes are confirmed up
  front instead.
- **Server-side read state.** Read state is per browser; sharing it
  across a person's devices would need a per-user store.

## Order of later pieces

1. **Agents** (what is running now) -- small, read-mostly, and exercises
   the event stream.
2. **Tasks list** -- the biggest daily surface; filters, bulk actions.
3. **Task detail** -- conversation, runs, changes; the inspector pattern
   from the Inbox grown into a full page.
4. **Sessions** -- standing conversations.
5. **CI** and **Processes** (schedules, templates, suites).
6. **Settings** -- many small forms, least urgent to restyle.
7. **Retire the classic UI** as described in *Migrating one page at a
   time*.
