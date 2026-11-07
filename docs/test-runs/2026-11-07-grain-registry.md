# 2026-11-07 walkthrough — Grain Registry service, SPA, CLI, tunnel & delegated identity

Plan: [`docs/test-plans/2026-11-07.md`](../test-plans/2026-11-07.md) §1 (PRs #736, #741, #755, #757, #762,
#792, #793, #798, #803, #804, #805, #810, #827). Run as task 3485, branch
`grain/098073e4/task-3485-walkthrough-grain-registry`, base `main` at `ada3fc6f` (merged
with `5e1058cf` during the run).

**Outcome:** every §1 item was walked with real processes. Six defects found, five fixed on
this branch with regression tests, one documented. Three feature gaps and two design
questions go to follow-up tasks.

| # | Finding | Severity | Status |
|---|---|---|---|
| F1 | Two `grain-registry` replicas starting together on an empty database race on migrations; one exits (`pg_type_typname_nsp_index` duplicate key) | High (first rollout of any multi-replica deployment) | **Fixed**: advisory-locked `store.Migrate` + `TestConcurrentReplicasMigrateAFreshDatabaseOnce` |
| F2 | Heartbeat responses hard-code `next_interval_seconds: 15` and ignore `catalog.heartbeat_interval`, so registries configured below 15s show live entries as offline between heartbeats | Medium | **Fixed**: `RouterOptions.HeartbeatInterval` + `TestHeartbeatResponseCarriesConfiguredCatalogInterval` |
| F3 | With F2 fixed, the default per-entry heartbeat limiter (12/min) rejects the faster cadence the registry now asks for (429 `rate_limited`) | Medium | **Fixed**: limiter allows 3 heartbeats per configured interval; same test |
| F4 | The `grain registry register -standalone` / `rotate-credential` "outbound tunnel config snippet" fails when pasted: the daemon refuses `-registry-connect` without the three `-ui-registry-*` flags, and the snippet also leaves out the token file | Medium (copy-paste onboarding is broken) | **Fixed** + `TestPrintConnectedConfigSnippetCarriesVerifierFlags` |
| F5 | `docs/registry.md` (and the design doc) tell operators to set `-ui-registry-issuer=https://grain.example.com`, but the registry signs `iss: grain-registry`. A daemon set up from the docs returns 401 `token has invalid issuer` for every request. The reference config also names `groups.provider: gcp`, but no build registers that provider. | Medium (docs) | **Fixed** (docs) |
| F6 | When `grain_config.registry` is already stored, a changed `-registry-url` is ignored and the log says "use 'grain settings' to change it", but `grain settings` has no registry flags | Low | **Fixed**: the message now names `grain registry register` and Settings → Registry |

## Environment

- Sandbox: Debian 12, Go 1.26.10, Node 20.19. `postgresql-15` installed with `apt-get` (not in the image).
- Built from the branch: `ui` + `ui-registry` (`npm ci && npm run build`), `/home/debian/bin/grain`,
  `/home/debian/bin/grain-registry`.
- [`env-up.sh`](2026-11-07-grain-registry/env-up.sh) sets up:
  - **PostgreSQL** cluster in `/tmp/grwalk/pg` on `127.0.0.1:4240` (trust auth), with database `registry`.
  - **IAP stub** ([`iapstub.go`](2026-11-07-grain-registry/iapstub.go), `go:build ignore`). This is the only
    stubbed external dependency. It publishes an ES256 JWKS (`:4250/jwks`, wired in as `auth.iap_certs_url`)
    and runs a front proxy for each replica (`:4251` → A, `:4261` → B). Each proxy drops any caller-supplied
    `X-Goog-IAP-JWT-Assertion` and signs a fresh one for the identity in `X-Test-Email`, or in the `email`
    claim of a `Bearer` token. The proxy keeps the `Host` header, so subdomain routing still works, and passes
    WebSocket upgrades through. Its `-force-host`/`-as` mode lets a plain `grain` CLI or browser reach
    `<entry>.<instance_domain>`: `:4255` → conn-alice through B as alice; `:4256` → the registry SPA as admin
    `root@`; `:4258` → missing entry `nope`.
  - **Two `grain-registry serve` replicas**, A (`:4252` main, `:4253` public, `:4254` peer) and B
    (`:4262/4263/4264`). Both use the same database and the same `local` signing key, with
    `instance_domain: grain-user.test`, `reconnect_grace: 5s`, `heartbeat_interval: 2s`,
    `degraded_after: 6s`, `offline_after: 12s`, static groups (`alpha-eng@alpha.test` = alice and carol,
    `beta-eng@beta.test` = bob), admin `root@alpha.test`, and `compute.provider: none`.
  - **Daemon d1** (`:4270`, owner alice), started by
    [`start-tunnelled-daemon.sh`](2026-11-07-grain-registry/start-tunnelled-daemon.sh) with
    `-registry-connect -ui-require-verifier=registry -ui-registry-issuer=grain-registry -ui-registry-audience=https://conn-alice.grain-user.test -ui-registry-keys-url=http://127.0.0.1:4253/.well-known/jwks.json`.
  - **Daemon d2** (`:4271`, owner bob). It started with no registry flags and self-registered through
    `grain registry register` (schema 116 `grain_config.registry`).

## Item 1 — live tunnel, subdomain proxy, verifier, `reconnect_grace`

| Probe (Host `conn-alice.grain-user.test`) | via A (local tunnel) | via B (peer-forwarded) |
|---|---|---|
| alice `GET /`, `/api/whoami`, `/api/tasks` | 200 | 200 |
| carol (alice's group) before the entry had `owner_group` | 403 (registry) | 403 |
| carol after `grain registry update -owner-group group:alpha-eng@alpha.test` | 403 from the **daemon**: its group allowlist needs Cloud Identity, which is not stubbed | same |
| bob (other tenant) | 403 `caller "bob@beta.test" does not have access to "conn-alice"` | same |
| no identity | 401 | 401 |
| SSE `GET /api/events?stream=1` | 200 `text/event-stream`, `: ready cursor=3` | same |
| `Upgrade: websocket` to `/api/console` | forwarded to the daemon. The daemon has no WebSocket route and answers 200 (JSON). WS framing through the tunnel is already covered by `TestRegistryEndToEndStep15`. | same |

Direct requests to the daemon (`127.0.0.1:4270`) all return **401**: without an assertion, with a forged
`X-Grain-Registry-Assertion: a.b.c`, and with a spoofed `Host: conn-alice.grain-user.test`.

`whoami` through the router returns
`{"actor":{"kind":"human","id":"alice@alpha.test","source":"registry"},"verified":true}`.

The `grain` CLI also works through the router (`GRAIN_SERVER=http://127.0.0.1:4255`, which goes to B and is
peer-forwarded to A). `grain whoami` printed `principal: human:alice@alpha.test`, and `grain inbox`,
`grain registry status` and the grain SPA itself (`/`, `/settings/registry`, desktop and phone, light and
dark, no page errors) all worked.

`reconnect_grace` (5s):

- d1 was killed with `kill -9` and restarted about 1s later. Requests sent during the gap were held and then
  succeeded: A returned 200 in 1.16s and B returned 200 in 1.19s.
- d1 was killed and left down. A held the request for **5.005s** and then returned
  `503 {"code":"entry_offline","disconnect_reason":"disconnected",...}`. Requests after the grace period
  return 503 at once. With `Accept: text/html` the reply is the server-rendered page "conn-alice is offline"
  (owner, owner group, last seen and disconnect reason). Bob gets 403, not the state page, so the state page
  leaks nothing across tenants.
- Missing entry: 404 page "nope not found".

## Item 2 — multi-replica peer forwarding & failover

- **F1 came first.** The very first `env-up.sh` started A and B at the same moment on an empty database.
  Replica A died with
  `applying migration 1 (initial_registry_schema): ERROR: duplicate key value violates unique constraint "pg_type_typname_nsp_index"`.
  The regression test reproduces it every time: six concurrent `Migrate` calls on a fresh database.
- The tunnel was moved to B with `grain registry register -registry-url http://127.0.0.1:4261 -entry conn-alice`
  against the running d1. The `tunnel_sessions` row then named B's replica (`127.0.0.1:4264`, epoch 5).
  Requests from A were peer-forwarded: HTTP 200 in 7ms and SSE stream OK.
- B was then killed with `kill -9` while it held the tunnel. A's next request returned
  `503 entry_offline ("peer tunnel gone")` after 2.56s, so it did not hang. B was restarted and d1
  reconnected. A served 200 again 6s after the restart (the daemon's own reconnect backoff), and the session
  epoch went from 5 to 6 on the new replica id.
- This revealed F6. Restarting d1 with a new `-registry-url` does nothing once the stored config has a URL.
  That behaviour is intended, but the log pointed at `grain settings`, which cannot change it.

## Item 3 — catalog isolation, `owner_group`, `/api/directory`, `/api/route`

Transcripts: [`catalog-matrix-before-fixes.txt`](2026-11-07-grain-registry/transcripts/catalog-matrix-before-fixes.txt),
[`catalog-matrix-after-fixes.txt`](2026-11-07-grain-registry/transcripts/catalog-matrix-after-fixes.txt),
[`manage-walk.txt`](2026-11-07-grain-registry/transcripts/manage-walk.txt). Principals: alice and carol
(`alpha.test`, group alpha-eng), bob (`beta.test`), admin root, and mallory (`evil.test`, not in
`allowed_domains`). Every check was run against both replicas.

- **Before F2/F3**, every entry showed `offline` in `list`, `directory` and `route`, even with a live tunnel.
  The daemon was heartbeating every 15s against `offline_after: 12s`.
- `list` shows only the caller's own entries. `list -all` returns 403 to everyone except root. mallory gets
  403 `domain "evil.test" is not in allowed_domains`.
- A **listed** entry appears in everyone's `directory`/`route`, and `show` works for every admitted user.
  This is by design.
- After `update -unlisted bob-ws`, alice, carol and **root** no longer see the entry in `directory` or
  `route`, and alice and carol get 403 from `show`. Only bob and root can still `show` it.
- `scopes`, `rotate-credential`, `transfer`, `delete` and `restore` on another tenant's entry are all 403
  (`not authorized to manage entry`). `accept-transfer` by anyone except the named recipient is 403.
  bob → carol transfer then carol's accept worked, after which bob lost access.
- `delete` followed by `list -include-deleted` showed the entry as `deleted` with listed=false, and
  `restore` brought it back to `running`.
- `unregister -standalone -oidc` was given four bad URLs (`http://` to a non-loopback host, `ftp://`,
  `https://user:pw@…`, and one with `?query#frag`). All four were refused before the fake `gcloud` on `PATH`
  was called (`gcloud invoked: never`).
- `create` with `compute.provider: none` returns
  `400 lookup shape "standard": sql: no rows in result set`. It refuses correctly, but the message is raw.
  See the follow-ups.
- **F4** showed up here. The snippet printed by `register -standalone` failed with
  `grain daemon: -registry-connect requires -ui-registry-issuer, -ui-registry-audience, and -ui-registry-keys-url`.

## Item 4 — lifecycle reconciler, stop/start & fleet upgrades; UI

- The real `grain-registry` binary only provides `compute.provider: none` or `gce`, so instance lifecycles
  ran over the in-process fakes. Two tests cover provisioning → running → stopped → running → upgrading,
  maintenance windows, the concurrency cap and stopped-instance skipping: `TestStep17StopStartAutoUpgradeAndMaintenanceWindowEndToEnd`
  (PASS) and `pkg/registry/reconcile` (PASS). The full `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/registry/... ./cmd/grain-registry ./tests/security`
  run passes against real PostgreSQL, including `TestRegistryEndToEndStep15`, which drives real `grain`
  subprocesses.
- **Gaps (follow-ups):** the API has `POST /api/entries/{name}/stop|start` (#810), but the
  `grain registry` CLI has no `stop`/`start` subcommands. `ui-registry` has no Stop/Start controls, and
  `grain-registry demo` has no stopped entry, so the SPA cannot show that state for review.
- **UI renders:** about 170 renders, all at 1440×900 and 390×844 in light and dark. All 0 flagged by
  [`layout-audit.mjs`](2026-11-07-grain-registry/layout-audit.mjs): no page errors, document never wider
  than the viewport, no element past the right edge outside a scrolling container. See
  [`layout-audit.txt`](2026-11-07-grain-registry/transcripts/layout-audit.txt).
  - `ui-registry` over `grain-registry demo`: `/`, `/directory`, `/new`, `/entries/{payments-staging,alice-workstation}`, `/admin`, `/status`.
  - `ui-registry` over the live registry: `/`, `/directory`, `/entries/{conn-alice,bob-ws}`, `/status`, `/admin`.
  - The offline and not-found state pages.
  - The grain SPA through the tunnel: `/` and `/settings/registry`.
  - `ui/e2e/screenshot.mjs` over `grain demo` for Settings → Registry.
  - `e2e/registry.spec.js` and `ui/e2e/registry-settings-layout.spec.js` pass.
  - The screenshots could not be looked at directly in this sandbox (no image viewer, and `LOOK=1` needs a
    Gemini key). The contact sheets are attached to the PR closing note.
- CI runs `ui-registry` vitest only. Nothing in `.github/workflows` runs `ui-registry/e2e/registry.spec.js`,
  although the plan's "What CI proves" says it does.

## Item 5 — delegated (`on_behalf_of`) identity

Setup: the registry runs `delegation_policy: consent`. Delegating app `walk-router`
(`serviceAccount:router@walk-proj.iam.gserviceaccount.com`) has a write grant from alice for
`conn-alice`. Both rows were inserted with `psql`, because no API or CLI creates delegating apps or grants.

| Request (service caller unless noted) | Result |
|---|---|
| SA to entry host, no `X-Grain-On-Behalf-Of` | 403 `service principals cannot access instance subdomains` |
| SA OBO alice, before the app row existed | 403 `delegation_refused` / `app_not_registered` |
| SA OBO bob or carol (no grant) | 403 `no_grant` |
| human (carol) sets the OBO header | 400 `delegation_requires_service_caller` |
| OBO with an `Origin` header | 403 `delegation_from_browser` |
| OBO on the registry host | 400 `delegation_not_supported` |
| OBO with a WebSocket upgrade | 403 `not_delegable` |
| SA OBO alice, grant ok, **no `delegate:` entry on d1** | 403 from the daemon: `delegate_not_allowed` |
| after `grain access allow --label walk-router --reason … delegate:router@…` | 200. `whoami` shows actor service `router@…`, `onBehalfOf` alice, `via: walk-router`, label "alice@alpha.test (via walk-router)" |
| OBO `POST`/`DELETE`/`PATCH /api/access/principals` | 403 `requires_person` |
| OBO `POST /api/tasks` with `admission: priority` or `durable` | 403 `cannot set admission: … requires an unlimited role` |
| OBO `POST /api/tasks` with standard admission | 201. Task 2 has `author` = the SA, `owner`/`creator` = alice, and `authorLabel` "(via walk-router)" |

`allowed_principal.label` is `walk-router`. `lastUsedAt` was set on first use and is throttled to one write
per minute (`touchDelegateLastUsed`). On the registry side, `delegation_grants.last_used_at` advanced and one
row was written to `delegation_usage`. `--label` must match `[a-z][a-z0-9-]{0,31}`. "Walk Router" was
refused, and `grain access allow --help` does not mention that rule.

## Fixes on this branch

1. `fix(registry): serialize schema migrations across replicas` (F1). Touches
   `pkg/registry/store/migrations.go` and adds `migrate_concurrency_test.go`.
2. `fix(registry): tell daemons the configured heartbeat interval` (F2). Touches `pkg/registry/server/router.go`
   and `pkg/registry/server.go`, and adds `heartbeat_interval_test.go`.
3. `fix(cli): print a registry tunnel snippet that actually starts` (F4, F6). Touches
   `pkg/cli/registry{,_query,_manage}.go`, `pkg/daemon/config.go` and `registry_security_test.go`. No flags
   or help text changed, so the CLI spec did not need regenerating.
4. `fix(registry): scale the per-entry heartbeat limit with the interval` (F3).
5. `docs(registry): document the issuer and groups provider that ship` (F5). Touches `docs/registry.md` and
   `design/design/registry.design.md`.

Each regression test was confirmed to fail without its fix (stash, run, restore) and to pass with it.
`go test ./tests/hygiene/...` passes.

## Follow-ups (not fixed here)

- `grain registry stop|start` CLI subcommands, plus Stop/Start controls and a stopped-entry fixture in
  `ui-registry` / `grain-registry demo` (#810 shipped the API only).
- An admin API/CLI for delegating apps and consent grants. Today it is a hand-written SQL `INSERT`, and the
  `principal` column silently needs the `serviceAccount:` prefix: a bare email made the app "not registered".
- Design question: any admitted user can declare a **primary** scope on any repository, for example bob
  claiming `alpha/app` on his entry. For listed entries this can take over `route` for another tenant's
  repository on paths that nobody with a longer prefix claims.
- Smaller items:
  - `create` with no matching shape leaks `sql: no rows in result set`.
  - The daemon's group-allowlist refusal returns the full ADC error text to the caller.
  - `register` against a local daemon without `-display-name` clears the display name set earlier by
    `-standalone`.
  - The heartbeat overrides a CLI-declared `-host-kind` with the daemon's inferred kind.
  - CI does not run `ui-registry/e2e/registry.spec.js`.

## Reproducing

```bash
sudo apt-get update && sudo apt-get install -y postgresql postgresql-client
(cd ui && npm ci && npm run build); (cd ui-registry && npm ci && npm run build)
go build -o /home/debian/bin/grain ./cmd/grain && go build -o /home/debian/bin/grain-registry ./cmd/grain-registry
docs/test-runs/2026-11-07-grain-registry/env-up.sh          # pg + IAP stub + replicas A/B
# register conn-alice (prints the snippet), then start the tunnelled daemon:
grain registry register -standalone -registry-url http://127.0.0.1:4251 -registry-token "$(fakejwt alice@alpha.test)" \
  -mint-token -token-file /tmp/grwalk/conn-alice.token conn-alice
docs/test-runs/2026-11-07-grain-registry/start-tunnelled-daemon.sh http://127.0.0.1:4251
docs/test-runs/2026-11-07-grain-registry/catalog-matrix.sh
docs/test-runs/2026-11-07-grain-registry/manage-walk.sh
(cd ui && node ../docs/test-runs/2026-11-07-grain-registry/layout-audit.mjs /tmp/out http://127.0.0.1:4256/ …)
GRAIN_REQUIRE_POSTGRES=1 PATH=/usr/lib/postgresql/15/bin:$PATH go test ./pkg/registry/... ./cmd/grain-registry ./tests/security
```

`fakejwt` builds an unsigned three-part token whose `email` claim the IAP stub signs for. It is defined at
the top of `catalog-matrix.sh`.
