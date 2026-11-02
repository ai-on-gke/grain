# Rendezvous relay as the only remote route — walkthrough (2026-10-07)

Executes §1 of `docs/test-plans/2026-10-06.md` against `candidate`. Task 2379, filed by task 2231.

## Environment and tested commit

- **Tested commit:** `candidate` at `dafd69778` (Merge pull request #3156). The branch was cut
  from it, and `git rev-parse HEAD origin/candidate` matched when the run started.
- `git branch -r --contains` lists `origin/candidate` for all seven commits under test:
  `96e40b25d`, `d55f5a274`, `cc3d93137`, `04175f2f2`, `ac6b5c067`, `f52ad0f38` and `7f03fa1ee`.
- **Sandbox:** Debian, Go 1.26.6, Node 20.19.0, Terraform 1.16.5 (already installed),
  Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Builds:** `cd ui && npm ci && npm run build`, then `go build -o /tmp/bin/grain ./cmd/grain`,
  which embeds the production bundle. After each fix landed on the branch, the binary was rebuilt
  and the affected steps were walked again on it.
- **No `gcp-key` capability.** `/home/debian/.gcp-service-account.json` is absent, so step 8 was
  skipped. No live IAP policy or relay setting was read or written.

Processes (all real, all on 127.0.0.1):

| Process | Command |
|---|---|
| relay A | `grain relay -port 18080 -daemon-token relay-daemon-tok -executor-token relay-exec-tok` |
| relay B | `grain relay -port 18081 -daemon-token relay-daemon-tok -executor-token relay-exec-tok` |
| relay C (step 6) | `grain relay -port 18082 -token shared-tok` |
| front door | `frontdoor -addr 127.0.0.1:18090 -backend-file /tmp/w/backend` (see Stubs) |
| WebSocket-only front door | `frontdoor -addr 127.0.0.1:18091 -backend-file /tmp/w/backendC -websocket-only` |
| daemon | `grain daemon -data-dir /tmp/w/data -ui-addr 127.0.0.1:18420 -sandbox-dir /tmp/w/sbx -disable-native-sandboxes -relay-url http://127.0.0.1:18090 -relay-auth-token-file /tmp/w/reltok -relay-session-lifetime 20s -ui-trust-tunnel` (flags varied per step, as noted) |
| executor (step 6) | `grain executor -relay-url http://127.0.0.1:18091 -token-file /tmp/w/shtok -instance-id ws-exec-1 -binary agy=/tmp/w/stubagy` |

`grain demo` has no relay flags, so the walkthrough used a `grain daemon` with a scratch
`-data-dir`, as the plan allows. A daemon now needs a sandbox backend to start
(`no sandbox backend selected`), so the walkthrough passed `-disable-native-sandboxes`.

## Stubs

- **Cloud Run's front end:** `frontdoor`, a ~40-line Go `httputil.ReverseProxy` (not committed).
  It sends every request, upgrades included, to the backend named in a file, which it reads again
  for each request. That matches how an identity-aware front end sends each new request to the serving
  revision. With `-websocket-only` it answers 400 to any upgrade other than `websocket`, as the
  front end before a `*.run.app` service does.
  - The first attempt used a TCP-level proxy that chose the backend per connection. It pinned the
    browser's keep-alive connection to the old relay, which a per-request front end never does, so
    its failures were thrown away as a stub artifact (step 1c below).
- **IAP signing keys:** step 5 is a Go test that uses `pkg/relay`'s local ES256 signer, serving
  IAP's PEM-map format (`newIAPSigner`). The `grain relay` binary has no flag for its IAP keys URL,
  so a real relay process cannot be pointed at a local signer. Identity admission was therefore
  tested in-process only.
- **Agent CLI (step 6):** `/tmp/w/stubagy`, a shell script that prints `stub`. The executor only
  has to register; nothing is dispatched to it.
- **GCP APIs:** none were called. With no metadata server, an IAP JWT audience makes the daemon's
  client fail with "finding the service account to sign the IAP JWT as". That is real behaviour
  and appears under step 2/3.
- Grain's own APIs and React were never mocked. Browser traffic is the built bundle talking to the
  real daemon, either through the real relay or directly.

## Steps and outcomes

### 1. Local relay and daemon (no cloud) — pass

- **1a, connect.** The daemon logged `established reverse tunnel to relay at http://127.0.0.1:18090`.
  `GET /` and `GET /api/tasks` through the relay's address answered 200, and Chromium rendered the
  task list through it. `relay-status.json` said `connected: true`, with the URL's source shown as
  `flag -relay-url`.
- **1b, rotation under traffic.** This ran with `-relay-session-lifetime=20s`, driven by
  `/tmp/w/relay-traffic.mjs`. The script kept the task list open in Chromium for 180 s. Every
  1.5 s it filed a task (`POST /api/tasks`, `noRepo`) and edited it (`PATCH /api/tasks/{id}`)
  from the page. Every fifth iteration it reloaded the list and waited for the edited row.
  - Result: `DONE tasks=103 okResponses=668 bad=0`, across **9 rotations** in the daemon log.
  - Each rotation logged `rotated reverse tunnel ... (session lifetime reached); draining the
    previous tunnel`. The relay logged `replacing the client's previous session, which finishes
    the streams it carries`, and the old session disconnected about 1 s later.
  - No browser request returned non-2xx or failed.
  - The UI has no EventSource or long-poll stream, so "a long-poll across rotations" has nothing
    to exercise in the browser. An in-flight stream across a rotation is covered by
    `TestRelayClientRotationLosesNoRequests`, which already existed. No new Go test was needed for
    rotation under traffic.
- **1c, new revision.** The front door was switched from relay A to relay B while the traffic
  script ran.
  - With the 20 s lifetime, the switch fell 2 s before a lifetime rotation, which moved the daemon
    to B. Result: 0 bad responses (`traffic3`).
  - To exercise the status probe on its own, the daemon was restarted with
    `-relay-session-lifetime 0` and the switch run again (B→A).
    - The daemon logged `relay serving instance has no session for this daemon; reconnecting
      (serving instance 8421a8d32ebd203e, tunnel on instance 682b4a1533cf82a7)` **14 s** after the
      switch, and the UI recovered: every later request was 2xx.
    - In that gap the relay holding no session answered **19 requests with 503**
      (`GET /api/tasks`, `/api/config`, …) between 2.6 s and 10 s after the switch. Each was held
      for its 2.5 s session wait first.
    - This is the designed behaviour, and within the plan's 30 s. A real redeploy can show up to
      one status interval (20 s) of 503s through the new revision. It is recorded as an
      observation, not a defect.

### 2. Stored settings in Admin → Access → Relay (Playwright) — one bug found and fixed

The walkthrough driver was `/tmp/w/panel.mjs`. Chromium signed in as the daemon's admin account
(`debian`, in `adminAccounts`) on the daemon's own address and opened `/admin/access`.

- **The token is never returned.** Every `GET`/`PUT /api/relay-settings` body was captured. None
  contained the token, only `"tokenSet": true`. The panel showed Token `set`, `saved here` and
  `last changed …`.
- **Saving reconnects at once.**
  - Replacing the token in the panel with a wrong one flipped `relay-status.json` to
    `connected: false` **76 ms** after the click. The panel then showed `Not connected` and
    `Last error … relay rejected upgrade with status 401`.
  - Saving the right token reconnected in **76 ms**.
  - `grain relay set` on the host, with no call to the daemon, reconnected in **3.6 s** (step 3).
- **Bug: a save in the panel cut the daemon off a relay stored without IAP.** See Finding 2.
  Fixed, and walked again on the fixed build: saving the user IAP audience kept `IAP JWT audience
  (unset)` and the panel stayed `Connected`.
- **The flag wins.** The daemon was restarted with `-relay-url http://127.0.0.1:18080` over a
  stored `http://127.0.0.1:18090`.
  - The panel showed `Relay URL | http://127.0.0.1:18080 | flag -relay-url`.
  - `grain relay show` showed `url http://127.0.0.1:18080 [flag -relay-url]`.
  - The other values still read `[stored]` / `saved here`.
- **Test with the tunnel up.** Test answered `Relay accepted this daemon: … a daemon is already
  connected as "default" (normally this one)`. The API body was `{"ok":true,"stage":"accepted",
  "status":409,…}`.
  - The relay logged `daemon tunnel from … refused: id "default" is held by a live session`.
  - The live tunnel was untouched: `connectedAt` was unchanged and no new session was established.
- **A team admin is refused.** `alice` was registered and made an admin of a new team `walk`; she
  is not a system admin.
  - `/admin/access` rendered no Relay fieldset.
  - `GET /api/relay-settings`, `PUT /api/relay-settings` and `POST /api/relay-settings/test` each
    answered 403 (`user "alice" does not have administrator privileges`).
  - The stored URL and the last audit line were unchanged afterwards.
- **Observation, not filed:** `-ui-trust-relay` with no user IAP audience logs two different
  warnings at startup that say the same thing (`grain daemon: -ui-trust-relay is on …` and
  `grain daemon: WARNING: -ui-trust-relay is on …`). This is cosmetic.

### 3. CLI with the daemon down — one bug found and fixed

- With the daemon stopped:
  - `grain relay set -data-dir … --url http://127.0.0.1:18090 --token-file …` → `changed
    iapJwtAudience, token, url`. As documented, `jwt-audience` defaulted to
    `http://127.0.0.1:18090/*`.
  - `grain relay show` printed the stored values and `token set, last changed …`.
  - `grain relay clear --token` → `changed token`, and `show` said `token not set`.
- Starting the daemon with no `-relay-url` gave `starting outbound reverse tunnel relay client to
  http://127.0.0.1:18090 (url from stored)`.
  - It then failed with `IAP credential: … finding the service account to sign the IAP JWT as`,
    because the default JWT audience asks for an IAP credential and there is no metadata server.
    That is correct for a relay behind IAP. A relay without IAP needs `--jwt-audience ""`, which
    `grain relay set -h` documents.
  - `grain relay show` reported it: `status not connected (retrying)` and `last error …`.
- `grain relay set --jwt-audience "" --token-file …` on the running daemon reconnected in 3.6 s
  (`url from stored`, `token set [stored]`).
- **Bug: `grain relay show` said "connected" for a stopped daemon.** See Finding 1. Fixed, and
  walked again: after SIGTERM to the fixed daemon, `show` printed `status not running`.

### 4. Removed flags — pass

`grain daemon -data-dir … -ui-addr 127.0.0.1:0 -sandbox-dir …` was started four times, each with
one removed setting:
- `-tunnel-gateway-url https://gw.example`
- `-tunnel-token x`
- `GRAIN_TUNNEL_GATEWAY_URL=https://gw.example`
- `GRAIN_TUNNEL_TOKEN=x`

Each start **exited 2** with `grain daemon: <name>: the tunnel gateway has been removed; reach this
daemon through the rendezvous relay instead -- -relay-url (or $GRAIN_RELAY_URL) for the gateway
URL, -relay-token or -relay-auth-token-file (or $GRAIN_RELAY_TOKEN) for its token, and
-ui-trust-relay in place of -ui-trust-tunnel … Remove <name> to start`.

A daemon started with `-ui-trust-tunnel` ran with `trustTunnel: true` in `relay-status.json`, and
logged the `-ui-trust-relay is on` warning. So the old name is accepted as `-ui-trust-relay`.

### 5. Identity admission — pass, coverage added

New test: `TestDaemonAdmissionMatrix` in `pkg/relay/iap_identity_test.go`. It checks three relay
configurations against eight requests, 24 cells in all.

Configurations:
- token only;
- allow-list + audience;
- allow-list + audience + token.

Requests:
- token alone;
- wrong token;
- nothing;
- allowed assertion alone;
- allowed assertion + token;
- disallowed assertion + token;
- `X-Goog-Authenticated-User-Email` alone;
- that header + token.

The plan's four claims all hold:
- A token-only relay still admits a token-only daemon (the upgrade path).
- An allow-list + audience relay with no token admits only a valid assertion naming an allowed
  email.
- With a token as well, it refuses when either the token or the assertion is missing.
- The plain email header opens nothing in any configuration. With a token, only a token-only
  relay admits that request, on the token alone.

The cases already in `iap_identity_test.go` covered the forged, unsigned, expired, wrong-audience
and wrong-issuer assertions.

### 6. WebSocket handshake — pass

- Relay C sat behind the `-websocket-only` front door:
  - The daemon connected through it (`established reverse tunnel to relay at
    http://127.0.0.1:18091`).
  - The executor registered: `/api/executors` listed `ws-exec-1` with
    `server_url http://127.0.0.1:18091`, and the relay logged `executor stream bridged … to
    daemon`.
  - The front door refused nothing.
- An old-style client sent a raw `Upgrade: grain-relay` request:
  - Directly to the relay, it got `HTTP/1.1 101 Switching Protocols` with `Upgrade: grain-relay`
    echoed, and the relay logged `daemon reverse tunnel established … (id: old-style-probe)`.
  - Through the WebSocket-only front door, it got 400 (`refused upgrade "grain-relay"`), as Cloud
    Run's front end would.

### 7. Terraform — partial

- `terraform -chdir=deploy/terraform/relay init -backend=false && terraform test` →
  **9 passed, 0 failed** (`iap_mode.tftest.hcl`). `terraform validate` passed.
- **Not done:** `terraform plan` for both `relay_iap_mode` values against a load-balancer state.
  Showing `moved` rather than replace needs a real prior state from a load-balancer deployment. A
  hand-made state with placeholder attributes would show diffs this run invented. The six `moved`
  blocks are in `main.tf:490-518`:
  - network endpoint group;
  - backend service;
  - global address;
  - managed certificate;
  - URL map;
  - HTTPS proxy.

  Each moves the resource from its un-indexed address to `[0]`.

### 8. Live IAP policy — skipped

The sandbox has no `gcp-key` capability.

## Findings

### Finding 1: `grain relay show` reported a stopped daemon as connected (fixed)

`RelayManager.Run` stopped the client on shutdown but never wrote `relay-status.json` again. The
daemon's UI-server shutdown did not wait for `Run` either. So the last report a cleanly stopped
daemon left said `running: true, connected: true`. `show` printed `status connected since …` until
the report passed its 3-minute staleness warning.

Reproduction, on `dafd69778`:

```
grain daemon -data-dir D … -relay-url http://127.0.0.1:18090 -relay-auth-token-file T &   # connects
kill -TERM <daemon pid>; sleep 4
grain relay show -data-dir D        # → "status  connected since …"  (expected: not running)
```

- **Fix (`3628849f3`):** `Run` writes the status after stopping the client, and the shutdown
  function waits for `Run` to return, bounded by its context.
- **Regression test:** `TestRelayManagerReportsStoppedOnShutdown` in
  `cmd/grain/daemon_relay_settings_test.go`. Without the fix it fails with `running=true
  connected=true`.
- Walked again on the rebuilt binary: `status not running`.

### Finding 2: any save in the Relay panel put an IAP JWT audience back on a relay stored without one (fixed)

The panel sends `url` with every save. `PUT /api/relay-settings` set `DefaultJWTAudience` whenever
`url` was present and neither audience was in the request. `relaysettings.Apply` then filled the
audience in as URL + `/*` whenever none was stored.

So on a relay stored with `grain relay set --jwt-audience ""`, the documented setting for a relay
with no IAP, any save in the panel did two things:
- stored the JWT audience — even a save that changed only the user IAP audience;
- made the daemon drop a working tunnel to mint an IAP JWT it cannot get.

Reproduction, on `dafd69778`:

```
grain relay set -data-dir D --url http://127.0.0.1:18090 --jwt-audience "" --token-file T
grain daemon -data-dir D … &                      # connected, jwt-audience (unset)
# Admin → Access → Relay: type a User IAP audience, press "Save relay settings"
# → PUT body back: "iapJwtAudience":"http://127.0.0.1:18090/*"; panel: "not connected",
#   Last error "… finding the service account to sign the IAP JWT as …"
```

The same reproduction as an API call:

```
curl -X PUT …/api/relay-settings \
  -d '{"url":"http://127.0.0.1:18090","userIapAudience":"/projects/1/global/backendServices/2"}'
```

- **Fix (`5b43663b2`):** `relaysettings.Apply` applies the default only when the URL actually
  changes. A first `grain relay set --url`, or a new URL in the panel, still gets URL + `/*`.
- **Regression tests:**
  - `TestRelaySaveKeepsAClearedJWTAudience` (`pkg/ui/relay_settings_test.go`) fails without the
    fix.
  - The new e2e spec below also fails without it (`Expected: "" Received:
    "http://127.0.0.1:37687/*"`).
- **Left as is:** pointing the panel at a *new* URL for a relay with no IAP still defaults the
  audience, and the panel has no way to clear it. The CLI (`--jwt-audience ""`) is the way to do
  that, as the panel's own text says. Production relays all sit behind IAP, where the default is
  right.

### Observations (no change made)

- A front-door switch with no rotation gives up to one status interval (20 s) of 503s from the new
  instance before the probe heals it (step 1c). This is within the plan's bound.
- The duplicate `-ui-trust-relay` startup warnings (step 2).
- `/api/relay-settings` returns `"writtenAt":"0001-01-01T00:00:00Z"` inside `status`; the field is
  only meaningful in the status file. This is cosmetic.

## Coverage added

- `pkg/relay/iap_identity_test.go`: `TestDaemonAdmissionMatrix` (step 5).
- `cmd/grain/daemon_relay_settings_test.go`: `TestRelayManagerReportsStoppedOnShutdown`
  (Finding 1).
- `pkg/ui/relay_settings_test.go`: `TestRelaySaveKeepsAClearedJWTAudience` (Finding 2).
- `ui/e2e/relay-settings-walkthrough.spec.js`: the first browser test of Admin → Access → Relay.
  - It runs against a real `grain relay` and a real `grain daemon` whose client runs only from
    settings `grain relay set` stored with it down.
  - It asserts:
    - the UI answers through the relay's address;
    - the panel shows `Connected` with values `saved here` and `last changed`;
    - saving only the user IAP audience keeps the tunnel up and the JWT audience unset;
    - Test answers 409-after-auth and leaves `connectedAt` alone;
    - no `/api/relay-settings` body carries the token.
  - It passes locally in 2.8 s.

Rotation under traffic was not given a new Go test. `TestRelayClientRotationLosesNoRequests`
already proves it deterministically in-process, and step 1b proved it across real processes.

## Commands to rerun

```
cd ui && npm ci && npm run build && cd .. && go build -o /tmp/bin/grain ./cmd/grain
go test ./pkg/relay -run 'TestDaemonAdmissionMatrix|TestIAPIdentity' -count=1
go test ./cmd/grain -run 'TestRelayManager|TestRelayFlags' -count=1
go test ./pkg/ui -run 'TestRelay' -count=1
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/relay-settings-walkthrough.spec.js
terraform -chdir=deploy/terraform/relay init -backend=false && terraform -chdir=deploy/terraform/relay test
make security
```

## Security suite and CI

- `make security` passed locally (`ok github.com/ai-on-gke/grain/tests/security 147.667s`, exit 0).
  The `go` job runs it too, and no `.grain/baselines/` count was changed.
- The fixes touch `pkg/relaysettings`, `pkg/daemon` and tests under `pkg/relay`/`pkg/ui`. The
  trust-boundary hold for security review is expected.

**Final grain CI verdict:** full suite **success** on `e77397ace` (run `6bc4260a0a6e047b`): all
eight jobs passed — `frontend`, `go`, `installer`, `lint`, `package cache`, `postgres`, `terraform`
and `ui e2e`. None of the known flakes recurred.
