# Connections & Infrastructure Settings Walkthrough — 2026-11-20

- **Date:** 2026-11-20, 12:26–14:00 UTC.
- **Task:** task 7549, branch `grain/53549e42/task-7549-walk-through-settings-git`, base `main`.
- **Commit tested:** `2c44ffa0b8af7d12d90bd37470af53858572e230` ("Merge pull request #1941").
- **Scope:** Settings → Git, Hosts, Capabilities (server pages, capability policy, API scope sets, Deployment credentials, stored secrets, legacy redirects), Hub (`registry`), Engine, CI, Caches and Sandboxes, all on the new UI at `/next/`.
- **Outcome:** **No new user-facing bugs found** in the walked flows. Every mutation exercised round-tripped through reload. Three `/next/` parity gaps already reported by the 2026-11-19 walk (G1–G3) are **still open** on this commit: the `config-map` / `secret-map` editors, the `host-script` list and per-script `Test`, and the host agent status on `/next/settings/cli`. I did not file them again because the 2026-11-19 run (task 7309) already proposed them. Several observations that did not count as bugs are listed under "Observations".

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, `linux/amd64`, Go `1.26.10`, Node `v20.19.0`. |
| Server | This checkout's real `grain demo`, built with `make frontend && go build ./cmd/grain` and started as `grain demo -addr 127.0.0.1:8471 -open=false -access-code-file <tmp> -secrets-store -host-executor=hostd -github-sim`. It ran over the throwaway embedded SQLite store that `grain demo` creates under `TMPDIR`. |
| Frontend | The built assets embedded in that binary: `ui-ng/` at `/next/`, and the classic `ui/` at `/`. |
| Browser | Playwright Chromium 1.62 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, at `1440×900` (desktop) and `390×844` (phone). The browser context sent the run's access code as `Authorization: Bearer …` on every request, the same way the e2e harness signs in. |
| Model | None. `/home/debian/.gemini-api-key` was not present, so no model-backed checks (`LOOK=1`) were run. |

The walk scripts were throwaway Node scripts driving Playwright through visible controls (clicks, typing, `Tab`, `Enter`, `Space`, `Escape`, confirm dialogs). They are not committed. A listener on each page logged console errors, page errors, every non-GET `/api/` response and every failed request.

## Stubs

- **No route was intercepted** in this run. The walk did not use `page.route`, and no answer from the real server was patched.
- **GitHub boundary:** I used the demo's own `-github-sim` flag instead of a loopback stub. Its GitHub App path has no installations, so `Test` on a GitHub App connection reports that no installation token could be minted. That is the expected result with no real App.
- **Throwaway credentials:** the SSH key (`ssh-keygen -t ed25519`) and the GitHub App RSA key (`openssl genrsa`) were generated in the sandbox for this run and deleted with it. Token and secret values were made-up strings.
- **Not walked here:** the states the demo cannot produce without stubs: the Hub status chips other than `Connected`, a `needs-hostd` away host, retired in-process grant notices, the `None (control plane only)` backend once enabled, and the Figma `Test` call. The 2026-11-19 report covers them with documented `page.route` stubs, and this commit does not touch them.

## Steps and outcomes

Every step below ran on `/next/`. Steps that have no `/next/` page yet are listed at the end.

### Crawl

| Step | Outcome |
| --- | --- |
| Load `/next/settings/{git,hosts,hostds,capabilities,capability-servers,registry,engine,ci,caches,sandboxes}` | ✅ No console errors, page errors or failed requests. |
| Old links `/next/settings/credentials`, `/settings/legacy-capabilities`, `/admin/integrations`, `/system/integrations` | ✅ Redirect to `/next/settings/capabilities#credentials`. |
| Old link `/next/settings/secrets` | ✅ Redirects to `#other-secrets`. |
| Card position after the redirects | ✅ The `#credentials` card sits at `y=136`, clear of the header. |
| Phone width (`390×844`) on all eight sections plus the `gcp` server page | ✅ `scrollWidth == clientWidth == 390`, and no element overflows the viewport without a scroll container. |

### Git — `/next/settings/git`

| Step | Outcome |
| --- | --- |
| Credential source and host kind options | ✅ Credential source offers `Create inline credential` / `Link stored credential`. Host kind offers `GitHub (github.com or Enterprise)` / `Plain git host`. |
| Auth types on a GitHub host vs a plain host | ✅ A GitHub host offers pat / ssh / app / ambient / none. A plain git host shows `GitHub App` disabled, with the hint "Needs a GitHub host". |
| Add a nameless token connection on `git.example.com` | ✅ The row appears as `grain-git-c-<hex>` · `git host` · `pat`. |
| Add an SSH key connection on a plain host | ✅ With no known_hosts, Save stays disabled under the hint "grain never trusts a host key on first use". An unparseable key is refused by the server with `the private key will not parse` in the form's banner. |
| `Reuse authentication from` | ✅ Lists `github.com · GitHub · all repos (pat)` (hint `default`) and `git.example.com · git host · all repos (pat)` (hint `stored as grain-git-c-…`). Nameless connections can be told apart. |
| `Test` on the plain-host connection | ✅ Says nothing was tested and explains why: no repository on `git.example.com` is in reach, so give the connection an `owner/repo` scope or add a repository there. |
| Add a GitHub App connection with scope `acme/` and proxy `http://proxy.example.com:3128` | ✅ The row shows `via proxy http://proxy.example.com:3128`, scope `acme` and type `app`, and survives reload. A malformed App key is refused (asn1 parse error). `Test` reports that no installation token could be minted. |
| Invalid proxy `ftp://user:pw@proxy` | ✅ Refused: `must be http(s)://host:port`. |
| A fixed save after a refusal | ✅ The next good save cleared the refusal banner. |
| `Remove` on a connection | ✅ Asks first ("Remove Git connection? … Repositories using it can no longer clone or push …"). Cancel keeps the row; confirming deletes it, and the row stays gone after reload. All throwaway connections were removed. |
| GitHub host pointer | ✅ Opens `/next/settings/capabilities#credentials`. |
| No-git-connection notice | ✅ With every connection removed, `/next/repos` and the New task sheet show it. It names "a GitHub token or GitHub App … a token, SSH key or ambient login for another git host" and links `/next/settings/git`. The `default` connection was restored afterwards. |

### Hosts — `/next/settings/hosts` (and `/next/settings/hostds`)

| Step | Outcome |
| --- | --- |
| Create enrollment code `walk-code` (expires 2h, max uses 3, capacity 2, labels `pool=walk`, principals `user:walker@example.com`) | ✅ The `Invite hosts` snippet switches to `--join ghe_…` and shows uses `0 / 3`, the default capacity, labels, allowed principals and expiry. |
| Bad code name `Bad Name!` | ✅ Refused by name. |
| Pending request `devbox-carol.example.com` | ✅ Shows the `SHA256:` fingerprint, enrolled by `carol@example.com`, and no pairing code. |
| Accept the request with edits (name `carol-walk`, capacity 3) | ✅ After reload the row is `carol-walk` with key-bound auth `SHA256:… · via workstations`, capacity 3, and no `Rotate`. |
| Register a legacy host `legacy-walk` | ✅ Shows the one-time `ghd_…` token and the install command. |
| Edit with an invalid label `bad label` | ✅ The error appears inline and the editor stays open. |
| Fix the label and set capacity 5; then blank the capacity | ✅ The fixed save persists across reload. Blanking capacity clears it (the API has no `capacity`). |
| Rename `legacy-walk` → `legacy-walk2` | ✅ Persists. |
| Revoke, then Delete | ✅ Both confirm first. The notice reads "Deleted host legacy-walk2." |
| Rotate, then Revoke the enrollment code | ✅ Both confirm first. The code ends as `revoked` with `No actions`. |
| `Delete revoked` | ✅ Confirms first. The notice reads "Deleted host old-laptop." |
| Revoked host row | ✅ Shows `Rotate` and `Revoke` disabled and `Delete` enabled. |

### Capabilities — `/next/settings/capabilities` and server pages

| Step | Outcome |
| --- | --- |
| Server list | ✅ Built-in servers (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`, then in-process `api`, `github`, `grain`, `model-proxy`, `release`, `repo`) come before the demo's custom `cloud` server. Only the custom server offers `Remove`. |
| `Disable` on `figma` | ✅ The chip flips `needs-configuring` → `disabled` without a reload. `Enable` restores it. |
| Add the throwaway server `https://walk.example.com/capabilities/walk` → `http://127.0.0.1:9/mcp` | ✅ Its state is `failed`. Its page opens from the list, and `Test` runs. |
| Remove the throwaway server | ✅ Confirms first ("Nothing grants it now. Its own secrets go with it."). `GET /api/secrets` is empty afterwards. |
| `(i)` popover on the `github` server page | ✅ Opens by click, `Enter` and `Space` (`aria-expanded=true`), closes with `Escape`, and keeps focus on the button. |
| Capability policy: deny `https://grain.dev/capabilities/github#read-issues` | ✅ Saved, and still there after reload. |
| New task picker under that policy | ✅ Its policy notes read "the capability policy denies …#read-issues … (rule 1 …)" and link `/next/settings/capabilities#capability-policy`. `repo#read` is not blamed. The rule was removed afterwards. |
| API scope set `walk/readonly` | ✅ Create (with a description), `Edit` scopes to `tasks:read, ci:read`, then `Remove`. Remove confirms first ("Nothing grants api:walk/readonly"). |
| Stored secrets | ✅ The value input is `type=password`. Add, replace (same name) and `Remove secret` (confirms first) all work, and the list is `Stored secrets (0)` after reload. |
| Deployment credentials card | ✅ All three GCP links point to `/next/settings/capabilities/https:%2F%2Fgrain.dev%2Fcapabilities%2Fgcp`. |

### Hub — `/next/settings/registry`

| Step | Outcome |
| --- | --- |
| Status card and `Heartbeat now` | ✅ Status is `Connected`. The heartbeat reports "Heartbeat delivered … the hub reports this entry connected". Health summary and advertised scopes (`acme/widgets` · whole repo · primary · from repo config) are shown. |
| Display name and Purpose | ✅ Edited, saved, and still there after reload. |
| Registration token field | ✅ Shows "not set" and is write-only. |
| Scope override `acme/widgets` + `docs/` (Primary) | ✅ Added, saved and kept across reload; then removed, saved and gone after reload. |
| Bad Hub URL | ✅ Refused: `must use https:// (or http:// on loopback)`. |

### Engine — `/next/settings/engine`

| Step | Outcome |
| --- | --- |
| Defaults | ✅ Poll interval shows placeholder `10s`, max worker agents `10`, max merges per repo branch `5`. |
| Out-of-range values | ✅ `bogus` → `invalid duration`; `0s` → `pollInterval must be positive`; `-1` workers → `maxWorkers must be at least 1`. Max merges `0` is blocked on the client: Save never sends it. |
| Valid values `15s` / `4` / `3` | ✅ Kept across reload. |
| Blanking all three | ✅ Resets to `10s` / `10` / `5` (the API agrees). |

### CI and Caches

| Step | Outcome |
| --- | --- |
| CI: all 14 inputs (queue, history, job cache, artifacts) | ✅ Filled, saved and reloaded, and every value read back (`7m` → `7m0s`). Blanking all of them saves, and every field reloads blank, falling back to the defaults shown as placeholders. |
| CI: `-5` jobs per run | ✅ Refused by name. |
| Caches: proxy cache max size | ✅ `1.5` refused (`must be a whole number`); `-3` refused (`0 (unset) or at least 1`). |
| Caches: valid `12` / `8`, then blank | ✅ `12` / `8` kept across reload. Blanking saves and reloads blank. |

### Sandboxes — `/next/settings/sandboxes`

| Step | Outcome |
| --- | --- |
| Backend options | ✅ `Granule virtual machines`, `Host directories` and `None (control plane only)` are disabled, each with its reason (e.g. "needs -disable-native-sandboxes at daemon startup"). |
| Network options | ✅ Network shows `Open (full network)`. |
| Auto overcommit bound out of range (CPU max `0.1`) | ✅ Refused by name: `cpuMin 1.00 is above cpuMax 0.10`. |
| Disk floor minimum | ✅ `6144` saved. Blank + `Tab` saved `5120` (the default), and the API agrees after reload. |
| Session sandbox idle TTL | ✅ `nonsense` refused; `45m` → `45m0s`; blank resets. |
| Kubernetes card | ✅ Lists the default fields. An invalid node selector is caught inline ("must be key=value") and Save is never sent. Namespace `Bad_Namespace!` is refused by name. |
| Custom sandbox image | ✅ Setting `registry.example.com/team/grain-sandbox:walk` shows the "contract unverified" warning and `Reset to default`. Reset → save → reload returns to an empty value that uses the stamped default image. |

### Steps with no `/next/` page yet

- **G1:** `/next/settings/capabilities/<config-map>` lists the method ("1 config map(s)") but shows "Nothing configured yet" under Server settings. The `ConfigMapList` / `ConfigMapConfigEditor` port is still missing, so file placements live only on the classic UI.
- **G2:** `/next/settings/capabilities/<host-script>` shows no script list and no per-script `Test`.
- **G3:** host agent version, adopted runs and allowlist suggestions are still classic-only (`/settings/cli`).

The 2026-11-19 run proposed G1–G3 as follow-ups. I did not re-walk these classic-only flows in this run.

## Observations (not filed)

- **`/next/tasks/new`** is not a route. It redirects to `/next/task/new`, which polls `GET /api/tasks/new` and gets 404 repeatedly. New task is a sheet opened from the rail, so nothing links to that URL; it only matters for someone typing it by hand.
- **Blank-field display is inconsistent across sections.** After a blank-to-reset save, Engine shows the effective values (`10s`, `10`, `5`), while CI, Caches, the session TTL and the disk floor (until reload) show an empty box with the default as a placeholder. Both read correctly; it is only inconsistent.
- **Accepting a pending host request asks for no confirmation.** That is consistent with Reject also asking for none, and with the request staying editable before acceptance.

## Tests run

- `ui-ng` Playwright, `desktop` project, run as regression evidence over a fresh demo per worker: `next-capability-policy-pickers`, `next-connections-walk-fixes`, `next-connections-infra-settings`, `next-git-settings-port`, `next-hosts-git-parity`, `next-hub-sandboxes-parity`, `next-infra-settings-port`, `next-capability-server-page`, `next-hosts-invite` and `next-capabilities-parity`. The results are in the closing note.
- No source code changed, so no new spec was added.
