# Test run — 2026-11-03 — the hostd registry, per-hostd tokens and owner-bound admission (§1)

Task 2707. This walks §1 of [`2026-11-03.md`](../test-plans/2026-11-03.md) (PRs #473, #542, #411, #474) against a real `grain daemon` with three hostds connected at once, a simulated IAP in front of it, and a real Chromium.

**Result:** every item under §1 "What is NOT proved" was walked. Six findings were fixed on this branch, each with a regression test that fails on the base: F1–F6. Four are reported and not fixed, because they are design questions or out of scope: R1–R4.

Scripts are in [`2026-11-03-hostd-registry/`](2026-11-03-hostd-registry/). They are `env.sh` (the daemon, the co-located hostd and the remote hostds, each in its own `HOME`, plus `connect`/`api` helpers), `metadata-stub.py`, `upgrade-86.sh`, `ui-walk.mjs` and `shared-row-bbox.mjs`.

## Environment

| | |
|---|---|
| Binary | `origin/main` `314e6e2e` (PR #471), built per shared step 1 (`npm ci && npm run build`, `go build ./cmd/grain`, embedded UI). The fixes were re-walked on this branch's build, rebased on `770171b7` |
| Run host | Debian GNU/Linux 12 (bookworm), kernel 6.1.0-54-amd64, Go 1.26.10, Node 20.19.0, Chromium 1234 (Playwright, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), in a grain sandbox |
| Daemon | `grain daemon -data-dir /var/tmp/hr/daemon/data -ui-addr 127.0.0.1:4310 -sandbox-port-base=0 -disable-native-sandboxes -poll-interval 2s -hostd-socket /var/tmp/hr/local/hostd.sock` (`env.sh start_daemon`). There is no sandbox backend, because §1 dispatches no runs |
| `local` | a real `grain hostd -socket … -allowlist …` (root-owned allowlist, `labels: {role: local}`, `capacity: 4`), `HOME=/var/tmp/hr/local` |
| Remote hostds | `ws-a`, `ws-b`, `ws-c`, `ws-d` (and `ui-…` from the UI walk), each a real `grain hostd -server http://127.0.0.1:4310 -name <n> -token-file <f>` in its own `HOME=/var/tmp/hr/<n>`, with its own root-owned allowlist (`labels: {name: <n>}`, `capacity: 1`). Each was registered first with `grain hostds register <n> -owner <email> -label k=v -token-file <f>` |
| Verifier | the daemon was restarted with `-ui-iap-audience /projects/123/global/backendServices/456 -ui-iap-issuer https://iap.test.local -ui-iap-keys-url http://127.0.0.1:18080/jwks -ui-allowed-principals user:owner-a@…,user:owner-b@…,user:mallory@…,user:ops@…,user:shared@…` |
| **Stubbed** | (1) **IAP**: `tests/iap/iapsim.go` (from [`2026-11-02-iap-end-to-end.md`](2026-11-02-iap-end-to-end.md)) as the proxy on `:18443` and the JWKS issuer on `:18080`. (2) **The GCE metadata server**: `metadata-stub.py` answers `…/service-accounts/default/identity` with iapsim's stand-in ID token `iapsim-idtoken.<email>`. A real `grain hostd -iap-audience` reaches it through `GCE_METADATA_HOST` (`pkg/hostd/iap_auth.go`), so the hostd's own OIDC path runs unchanged. Nothing else is stubbed: no real GCP is reached |
| Tools added | `sqlite3`, `tesseract-ocr` (apt) |

## Results

| # | Item | Result | Evidence |
|---|---|---|---|
| 1 | **Three hostds at once** | **PASS**, plus **F4** and **R1** | `local` (socket), `ws-a` and `ws-b` (dialed in). `grain hostds list` and `GET /api/hostds` show `ws-a`/`ws-b` `connected`, each with its own `since`, `host` and `addr`. `kill -9` of `ws-b`'s hostd raises `hostd-disconnected:ws-b` ("grain hostd ws-b disconnected"), logs `grain daemon: grain hostd ws-b on debuerreotype disconnected`, and leaves `ws-a` connected. Restarting `ws-b` reconnects within 3 s, and the notification's `clearedAt` is set. `local` is shown with no connection state at all: `-` in the CLI, and "Co-located (socket)" in the UI even on a daemon with no socket (R1) |
| 2a | **Rotate while connected** | **PASS** | `POST /api/hostds/ws-a/rotate` gives 200 with a new token (prefix `ghd_4e086c`). The live session **survives** (still `connected`, by design: rotation retires the token for the next dial). A redial with the old token gets **401** `grain hostd: this controller does not take that hostd token`, logged as `grain daemon: refused a grain hostd on an unnamed host connecting from 127.0.0.1:58806: it presented a token that is not a hostd token here (/api/hostds, or -hostd-token-file)`. The real hostd backs off 2 s, 4 s, …, and restarting it with the new token reconnects |
| 2b | **Revoke while connected** | **PASS** | `POST /api/hostds/ws-c/revoke` gives 200 with `revoked_at`. The session drops at once (`grain hostd ws-c on debuerreotype disconnected`, then `grain hostd: revoked hostd ws-c and its tokens`). The redial gets **401**, as above. No `hostd-disconnected:ws-c` is left open. Refusals: revoking again gives **200** (idempotent; it appends a second config-audit entry, R4). Rotating a revoked hostd gives **400** `{"error":"model: hostd is revoked"}`. Revoking `local` gives **400** `"local" is the co-located hostd and cannot be revoked`. Rotating `local` gives **400** `"local" is the co-located hostd, and it takes no token`. Revoking an unknown hostd gives **404** `{"error":"model: hostd not found"}` |
| 2c | **A token under another hostd's `-name`** | **PASS**, plus **F2** | curl with `ws-a`'s token and `X-Grain-Hostd-Name: ws-b` gets **403** `grain hostd: this token belongs to hostd "ws-a", not "ws-b"`. A real `grain hostd -name ws-b -token-file ws-a/token` gets the same 403. The log line is `refused a grain hostd on debuerreotype connecting from 127.0.0.1:45568: it calls itself hostd "ws-b", but its token is hostd "ws-a"'s`. **But** on the base the first such attempt was not logged at all: see F2 |
| 3 | **Owner-bound admission behind the verifier** | **PASS**, plus **F3** | Behind iapsim, a real hostd for `ws-a` whose metadata stub says `owner-a@example.com` is admitted: `connected as owner-a@example.com`. `ws-b` dialing as `mallory@example.com` gets **403** `grain hostd: hostd "ws-b" connected as "mallory@example.com", but its owner is "owner-b@example.com": a remote hostd must connect as its owner (run it as owner-b@example.com, or change the owner in Settings → Hostds, `grain hostds set-owner ws-b <email>`, or PATCH /api/hostds/ws-b {"owner": "<email>"})`, and the daemon logs the same text after `refused a grain hostd on … connecting from 127.0.0.1:48510:`. `ws-d` (no owner) dialing as `ops@example.com` gets **403** `hostd "ws-d" has no owner, and a remote hostd's verified identity must be its owner: it connected as "ops@example.com"; set it in Settings → Hostds, …`. Then `grain hostds set-owner ws-d ops@example.com`, and the hostd's next redial is admitted (`connected as ops@example.com`). A principal outside `-ui-allowed-principals` (eve) gets **403** `principal "eve@example.com" is not allowed by -ui-allowed-principals`. Loopback with no forwarding headers and the token alone gets **101** (admitted). The same request with `X-Forwarded-For: 10.0.0.9` and no assertion gets **401** `hostd "ws-a" dialed in through a proxy with no verified identity: a remote hostd must present its owner's identity (grain hostd -iap-audience, …)` |
| 4 | **Deprecated shared token with `-hostd-token-owner`** | **PASS**, plus **F4** | `-hostd-token-file` with `-hostd-socket` is refused at start: `grain daemon: -hostd-socket=… and -hostd-token-file: host CLIs go through one grain hostd -- the one at the socket, or one that dials in -- not both` (by design). With no socket, the daemon logs `grain daemon: -hostd-token-file is deprecated and goes in the next release: register hostd remote in the hostd registry (POST /api/hostds) and give it the token it answers with`. The shared hostd dialing through IAP as `shared@example.com` connects beside registry hostds `ws-a` (as owner-a) and `ws-b` (loopback). As mallory it gets **403** `… but its owner is "shared@example.com": … or start the daemon with -hostd-token-owner=<email> …`. The shared token under `-name ws-a` gets **403** `this token belongs to hostd "remote", not "ws-a"`. `POST /api/hostds {"name":"remote"}` gets **400** `"remote" is the name -hostd-token-name gives the shared token's hostd; a registry hostd needs another`. **But** `remote` appeared nowhere in Settings → Hostds, `GET /api/hostds` or `grain hostds list`: see F4 |
| 5 | **Settings → Hostds UI** | **PASS** (15/15 checks) | `ui-walk.mjs` against the live daemon. Register shows the token once (`ghd_…`, `hostd-issued-token`), and the row shows only its prefix. After a reload the token is gone, and `GET /api/hostds` never carries it. Edit sets owner and labels. Rotate shows a new, different token. Revoke marks the row Revoked, hides its actions and clears the shown token. There were no page errors, and horizontal overflow was 0 px at 1440 and 390, light and dark. Shots: [`screenshots/ui-walk/`](2026-11-03-hostd-registry/screenshots/ui-walk/). The token in `01-registered-token-shown-1440.png` belongs to a throwaway sandbox daemon and was revoked in the same walk. Rotate and Revoke act on one click (R3) |
| 5 | **Exports** | **PASS** (by design: two kinds of export) | `hostd_token` is in `staterepo.LocalTables`, so it is never written to any dump. The grain/config state repository (`data/state-repo/tables/`) holds **neither** `hostd` nor `hostd_token`: `tables_internal_test.go`'s `notSettings` lists both, because hostds are created in Settings, not declared. `hostd` rows are in the full dump `staterepo.Export` writes for a dump-based schema upgrade. No `ghd_` string or `hostd_token` appears anywhere in the state-repo tree. Consequence: R2 |
| 6 | **Executors removal on upgrade** | **PASS**, plus **F6** | `upgrade-86.sh`: the old binary (built from `49d57a8e^1`, schema 86) minted two executor tokens (`exec-tok-1 laptop gxt_8e62`, `exec-tok-2 ci-box gxt_2fd3`). The current binary started on that data dir: `grain: migrated the store in place from schema 86 to 96` (main is at 96 now). Afterwards there are no `executor*` tables, `hostd` and `hostd_token` exist, `local` is seeded, and `GET /api/executors/tokens` gives **404** `no such API endpoint`. `grain help` and `grain daemon -h` mention executors only as "host executor" (hostd). The two dropped tokens are not mentioned in the log (R4). `/settings/executors` landed on **Settings → Models** (F6) |
| 7 | **`-ensure-builtins` with an operator-edited entry** | **PASS**, plus **F5**, **R1** | On a fresh allowlist it adds `host-git`, `grain-gchat`, `grain-gcp` and `grain-host-script`. After I edited `host-git` (`max_duration: 30m`, `set_env: {GIT_TRACE: "1"}`) and deleted `grain-gcp`, it re-adds `grain-gcp`, keeps `host-git` byte-for-byte, and reports `kept host-git: max_duration 30m0s (default 24h0m0s) as you have it; it differs from grain's default`. The `set_env` edit was **not** reported (F5). **Health does not show the drift**: only `-ensure-builtins`' stdout says it (R1) |

## Findings fixed here

### F1: none

There was no defect in admission itself. Each refusal named both identities and how to fix it, as specified.

### F2: one refused hostd's retries hid another's refusal from the daemon log (fixed)

**Repro (base `314e6e2e`):** start `ws-a`'s hostd with a rotated-away token (it retries every 2–30 s), then within the minute present `ws-a`'s valid token as `-name ws-b`. The 403 is returned, but the daemon logs nothing until the shared one-line-a-minute window ends, and then logs it as "(5 more refused since the last line like this)". `hostd.Remote.logRefused` kept a single `refusedAt` for every refusal, so a misconfigured hostd hid a token presented under another hostd's name, which is the refusal an operator most wants to see.

**Fix:** `pkg/hostd/remote.go` rate-limits each kind of refusal on its own: unknown or absent tokens share one key, and each (token's hostd, claimed name) pair has its own. The map is bounded at 64 keys, with an overflow key past that. **Test:** `TestRemoteLogsANameMismatchBesideARetryingStaleToken` fails on the base and passes with the fix. Live on the fix: the stale-token line (09:10:35) and the name-mismatch line (09:10:38) are both logged.

### F3: changing a connected hostd's owner left it serving as the old owner (fixed)

**Repro (base):** behind IAP, `ws-d` is connected as `ops@example.com`. `grain hostds set-owner ws-d owner-b@example.com` leaves `ws-d owner owner-b@example.com connected as ops@example.com` indefinitely. Owner-bound admission is checked only at dial time, so an owner change (someone leaving, a service account being replaced) did not take effect until the hostd happened to drop.

**Fix:** `Client.UpdateHostd` (`pkg/ui/hostds.go`) closes the live session when the owner was changed and the session was admitted as a verified principal other than the new owner. It logs `grain hostd: dropped hostd ws-d's session: it connected as ops@example.com, and its owner is now "owner-b@example.com"`. A token-only session (loopback, or a daemon with no verifier) was never owner-bound and stays, and so does a labels-only edit. **Test:** `TestChangingAHostdsOwnerDropsASessionConnectedAsTheOldOne`. Live on the fix: the session dropped, the redial got 403 against the new owner, and setting the owner back readmitted it.

### F4: the deprecated shared-token hostd was invisible in Settings → Hostds and `grain hostds list` (fixed; UI)

**Repro (base):** run `grain daemon -hostd-token-file f -hostd-token-owner shared@example.com` with a hostd dialing in under it. It is connected and is the daemon's *default* hostd, but `GET /api/hostds` lists only registry rows, so the page shows `local` ("Co-located (socket)", which this daemon does not have) and never `remote`. The before shots are in [`screenshots/before-shared/`](2026-11-03-hostd-registry/screenshots/before-shared/).

**Fix:** `ListHostds` appends the shared hostd as a view with `"shared": true`, its `-hostd-token-owner` and its connection. The new `Remote.SharedTokenOwner` supplies the owner, and the token is never included. The CLI marks it `connected (deprecated shared -hostd-token-file)`. Settings → Hostds shows it with no Edit, Rotate or Revoke (it has no registry row to change) and "Shared token (deprecated)" in the Token column. **Tests:** `TestListHostdsShowsTheSharedTokensHostd` (Go) and `HostdsSection.test.jsx` "lists the deprecated shared token's hostd with no actions" (vitest). The after shots are in [`screenshots/after-shared/`](2026-11-03-hostd-registry/screenshots/after-shared/) at 1440/390, light/dark. Bounding boxes (`shared-row-bbox.mjs`): the label is one line (167×20 px, line height 20) inside its row, with 0 buttons and 0 px page overflow at all four. Before `nowrap` it wrapped to 2 lines at 1440 and 3 at 390.

### F5: `-ensure-builtins` did not report edits to fields grain's entry leaves unset (fixed)

**Repro:** add `set_env: {GIT_TRACE: "1"}` (or `GIT_SSH_COMMAND`, `cwd_under`, `paths_under` or `path`) to the `host-git` entry and run `grain hostd -ensure-builtins`. The entry is kept, which is correct, but the "kept … differs from grain's default" report omits the edit, because `differences` compared only the fields the built-in sets.

**Fix:** `pkg/hostd/builtins.go`'s `differences` also compares `path`, `set_env` (by variable name, never value), `paths_under` and `cwd_under`. **Test:** `TestPlanEnsureReportsEditsToFieldsTheBuiltinLeavesUnset`, which fails on the base with `differs = []`. It also checks that a `set_env` value never appears in the report.

### F6: `/settings/executors` landed on Settings → Models (fixed; UI)

`/system/executors` and `/admin/executors` already aliased to Hostds, but `/settings/executors`, the retired pane's own path, fell through to the first section. **Fix:** an `executors: "hostds"` entry in `SETTINGS_SECTION_ALIASES` (`ui/src/settingsSections.js`). **Test:** `paths.test.js`. The after shots are `screenshots/after-shared/settings_executors-*`, and OCR reads "settings | hostds".

## Reported, not fixed

- **R1 (gap): Health and the registry say nothing about `local`'s state, or about built-in allowlist drift.** `local` has no `connection` in `GET /api/hostds`. The UI always says "Co-located (socket)", even with no socket or a dead hostd, and only the host-executor status probes it. `-ensure-builtins`' "kept … differs" lines go only to its stdout or journal. Health shows *missing* entries (`Suggest`/`Missing`), not edited ones, so §1 item 7's "Health shows the drift" is not met. Surfacing either needs `local`'s probe in the registry view and drift in hostd's hello, a protocol change.
- **R2 (design): a dump-based schema upgrade drops every remote hostd's token.** `hostd_token` is deliberately local (`LocalTables`: "a store restored from the repository mints them again (Rotate)"). A deployment whose upgrade goes through `staterepo.Export`/import (`grain state prepare-upgrade`, or a SQLite→Postgres move) keeps its `hostd` rows but loses every token, so every remote hostd is refused (401) until an admin rotates each one and redistributes the token. This run's 86→96 upgrade migrated in place, so it was not hit. Nothing tells the operator after such an import. It needs at least a notification listing the hostds to rotate, or a decision to carry the digests.
- **R3 (UX): Rotate and Revoke in Settings → Hostds act on one click.** Revoke drops a live session and is irreversible (the name cannot be registered again). Rotate makes the running hostd's next redial fail. Other destructive settings actions confirm first.
- **R4 (minor):**
  - Revoking an already-revoked hostd answers 200 and appends a second `revoked hostd …` config-audit entry.
  - `model: hostd is revoked` / `hostd not found` do not name the hostd.
  - Owner-mismatch refusals are logged on every retry (2 s, 4 s, … backoff), unlike token refusals, which are limited to one line a minute.
  - The 86→93 migration drops executor tokens without a log line saying how many were dropped.
  - There is no `grain hostds rotate|revoke` in the CLI (only `list|register|set-owner`).

## Re-running

```sh
R=/var/tmp/hr . docs/test-runs/2026-11-03-hostd-registry/env.sh
start_daemon                                   # add the -ui-iap-* flags for item 3
register ws-a owner-a@example.com gpu=yes zone=a; register ws-b owner-b@example.com gpu=yes zone=b
start_remote ws-a; start_remote ws-b; hostds
connect "$(cat $R/ws-a/token)" ws-b            # 403, name mismatch
go build -o $R/iapsim tests/iap/iapsim.go && $R/iapsim -upstream http://127.0.0.1:4310 &
python3 docs/test-runs/2026-11-03-hostd-registry/metadata-stub.py 18101 owner-a@example.com &
GCE_METADATA_HOST=127.0.0.1:18101 HOSTD_SERVER=http://127.0.0.1:18443 start_remote ws-a -iap-audience /projects/123/global/backendServices/456
OLD=$R/grain86 bash docs/test-runs/2026-11-03-hostd-registry/upgrade-86.sh
(cd ui && node ../docs/test-runs/2026-11-03-hostd-registry/ui-walk.mjs http://127.0.0.1:4310 "$(cat $R/daemon/data/local-api-token)" /tmp/shots)
```
