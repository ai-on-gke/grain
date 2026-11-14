# Connections & infrastructure settings walkthrough — 2026-11-13 (task 4731)

- **Date:** 2026-11-13, 02:55–03:50 UTC, one run.
- **Task:** task 4731, branch
  `grain/098073e4/task-4731-walk-through-settings-git`, base `main`.
- **Commit tested:** `4ae6438f` ("Merge pull request #1435"), binary
  `v0.0.0-20261113022244-4ae6438fdcd4`. The `/next/` fixes below were
  checked again in the browser on a rebuild of that commit with them applied
  (`/var/tmp/grain-fixed`, demo on a fresh store).
- **Focus:** the 2026-11-08, 11-09 and 11-11 reports walk this area in depth
  on classic Settings. This run walked every section in scope again on
  current `main`, aimed its extra time at what those runs left uncovered
  (role gating, Hub states other than `connected`, host placement with a
  host agent beside the daemon, the connection proxy), and walked the same
  sections in the Angular UI under `/next/settings/…`, which is now the UI
  new work goes into.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 (phone 390×844 where stated).
  - Built with `cd ui && npm ci && npm run build`,
    `cd ui-ng && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Servers, each over a throwaway embedded SQLite store (`TMPDIR` set to a
    fresh directory):
    1. `grain demo -addr 127.0.0.1:8420 -open=false -github-sim -fake-ci -git-proxy -secrets-store -host-executor hostd -access-code-file <f>`, used for most steps.
    2. `grain demo -addr 127.0.0.1:8421 -open=false -unconfigured -secrets-store …`, for the no-git-connection notices.
    3. `grain demo -addr 127.0.0.1:8422 -open=false -github-sim -secrets-store -external-config-repo https://github.com/example/walk-config …`, for locked mode.
    4. The fixed build on `:8425`, for re-checking the fixes.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots. The file reader returns PNG
    bytes, so layout was checked with bounding boxes and `scrollWidth`
    instead (below).
- **How it was driven:** throwaway Playwright scripts outside the checkout.
  Each signs in with the access code (the cookie `POST
/api/auth/access-code` sets, as `ui-shared/e2e/accesscode.js` does), then
  acts only through visible controls (clicks, typing, `Tab`, `Escape`).
  Every page recorded uncaught page errors, console errors and warnings,
  failed requests, `/api/` responses of 400 or more (with bodies), every
  native dialog (text, and whether it was accepted or dismissed), and every
  non-GET `/api/` request with its body. The REST API (bearer access code)
  was used only to read back what a gesture persisted.
- **Console baseline:** no uncaught page error and no 5xx. Every console
  error was the browser's line for an intended 400 refusal named below.
  The only failed requests were `ERR_ABORTED` from navigating away.

## Stubs and intercepted routes

The server, store, built frontends, browser and gestures were real
throughout. Playwright `page.route` was used only where the demo cannot
produce a state:

| Route                                                                                                                                                                   | Why                                                                                                                                                                    |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/registry`, merged with `{enabled:false,state:"disabled"}`, `{state:"connecting"}`, `{state:"degraded",lastError}`, `{state:"error",lastError,scopeConflicts}` | The demo hub only ever reports `connected`. Used for the Not configured, Connecting, Degraded and Heartbeat error chips, the error alert and the scope-conflict alert. |
| `GET /api/config` with `access: {role:"custom", scopes:[…]}`                                                                                                            | The access code always signs in as the owner. Used for `settings:write` only, `secrets:write` only, `capabilities:admin` only, and read-only.                          |

Two local helpers, both outside the checkout:

- **A logging proxy** (`/var/tmp/walk/proxy.py`, `127.0.0.1:3999`). It
  writes each connection's first line to a file and answers `502`. It
  stood in for a git connection's HTTP proxy.
- **A throwaway Ed25519 key** (`ssh-keygen -t ed25519 -N ""`) for the
  SSH-key connection, with a made-up `known_hosts` line.

No request left the sandbox for GitHub, a hub, a cloud or Figma. A
`github.com` connection is probed at the deployment's GitHub host, which in
the demo is its simulated GitHub on loopback. The made-up hosts
(`*.example.test`, `ghe.walk.invalid`) only cost a DNS lookup that fails.

## Steps and outcomes

### 1. Git (`/settings/git`) — pass, one bug filed

| #   | Step                                    | Outcome                                                                                                                                                                                                                                                                                                                       | Verdict      |
| --- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| 1   | Plain git host, token                   | Kind `Plain git` offers PAT, SSH key, GitHub App (disabled, titled "A GitHub App needs a GitHub host: a plain git host has no App API") and ambient. Saved `git.example.test` as `kind:"git", type:"pat"`. Test says "nothing was tested … give the connection an owner/repo scope, or add a repository on git.example.test". | PASS         |
| 2   | Plain git host, SSH key and known hosts | Fields are SSH private key, Known hosts ("Needed for any host but github.com: grain never trusts a host key on first use") and an optional API token. Saved; the API reports its fingerprint and `sshUser: git`. Test explains the same reach limit as step 1.                                                                | PASS         |
| 3   | GitHub token with scope and proxy       | `github.com`, scope `walk-org`, proxy `http://proxy.example.test:3128`. The row reads "pat via proxy http://proxy.example.test:3128 · walk-org". Test answered from the simulated GitHub ("GitHub rejected the token"), not through the proxy: see step 6.                                                                    | PASS         |
| 4   | GitHub App                              | App ID, Installation ID and private key fields. A key with no PEM block is refused on save with `400 gitproxy: no PEM block found in GitHub App private key`, and the form stays open with what was typed.                                                                                                                    | PASS         |
| 5   | Persistence and Remove                  | All rows survive a reload. Remove asks "Remove the git.example.test connection grain-git-c-…? A credential stored for it is deleted with it and cannot be read back…". Dismissing sends nothing; accepting sends `DELETE` and the row is gone after a reload.                                                                 | PASS         |
| 6   | Test through a connection's proxy       | GitHub connection `ghe.walk.invalid` with proxy `http://127.0.0.1:3999` (the logging proxy). Save sent `CONNECT ghe.walk.invalid:443` to the proxy. Test then reported `dial tcp: lookup ghe.walk.invalid … no such host`: it went direct, and the proxy logged nothing from Test. **Bug 3**, filed as a follow-up.           | FAIL → filed |
| 7   | GitHub host pointer                     | In locked mode (server 3), Git shows "A connection's token or key is never part of the config repo: it is set write-only under Settings → Capabilities". The link opens `/settings/capabilities#credentials` with the card at y=89, below the 77px header.                                                                    | PASS         |
| 8   | No git connection                       | Server 2: Git says "No git connections configured". New task and Repos both show "This deployment has no git connection yet … a GitHub token or GitHub App for github.com, or a token, SSH key or ambient login for another git host", linking `/settings/git`.                                                               | PASS         |
| 9   | Refusal banner on save                  | Covered by `git-connection-refusal-banner.spec.js`, re-run green (below).                                                                                                                                                                                                                                                     | PASS         |

"Reuse authentication from" (nameless connections staying distinguishable)
is on the Models connection editor (`ModelBackendPage.jsx`), not Git, so it
belongs to the settings walkthrough.

### 2. Hosts (`/settings/hostds`, and `/next/settings/hostds`) — two bugs fixed

Classic `/settings/hosts` redirects to `/settings/hostds`. The page shows
Invite hosts (the `curl … | sh -s -- --join ghe_… --release …` snippet,
code chips `default` and `workstations`, uses, New code, Rotate code,
Revoke code). Pending host requests shows the verified enrollee ("Enrolled
by carol@example.com (local user carol)"), CLIs, version and key
fingerprint, editable Host name, labels and capacity, and Accept and Reject,
with no pairing code. The Hosts table has `local` (co-located, Edit only),
token rows and a revoked row (Delete only), Delete all revoked, and legacy
token registration. The confirmations added on 11-11 are still in place;
`hostds-delete`, `hostds-revoke-confirm`, `hostds-capacity-and-banner` and
`hostds-settings-layout` re-run green.

The same section under `/next/` had not been walked:

| #   | Step                              | Outcome                                                                                                                                                                                                                                                                                                                  | Verdict      |
| --- | --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------ |
| 1   | Row actions on `local`            | Rotate, Revoke and Delete were all enabled on `local`. Each sent its request on one click, and the server answered `400 "local" is the co-located host and cannot be revoked` / `… it takes no token` / `… cannot be deleted`. **Bug 1.** `local` now offers no row actions.                                             | FAIL → fixed |
| 2   | Revoke, Rotate, Delete and others | With native dialogs set to dismiss, Revoke, Rotate and Delete still sent `POST …/revoke`, `…/rotate` and `DELETE`: there was no confirmation at all. The handlers for Delete revoked, Rotate code, Revoke code and Reject were the same. Delete was enabled on hosts never revoked. **Bug 1.**                           | FAIL → fixed |
| 3   | Pending request                   | The table had a "Pairing code" column showing `ZXQN-H95Y`, though the request is accepted on the verified enrollee and there is no pairing code to compare (`docs/host-placement.md`). The key fingerprint was not shown. **Bug 2.**                                                                                     | FAIL → fixed |
| 4   | After the fix                     | Revoke opens "Revoke host cloudtop-alice?" in the shared confirm dialog, with Cancel focused. Cancel and `Escape` send nothing. Delete on the revoked `old-laptop` asks "Delete host old-laptop?", sends one `DELETE` and reports "Deleted host old-laptop." Pending shows the fingerprint in place of the pairing code. | PASS         |

### 3. Capabilities (`/settings/capabilities`, `/settings/capabilities/<id>`) — pass

| #   | Step                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Verdict           |
| --- | ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------- |
| 1   | Old link and notices     | `/settings/capability-servers` → `/settings/capabilities`. The seven deprecation notices (gcp-key, model proxies, gemini-key, release, grain-ci, bootstrap-playbooks, github-sandbox) head the page.                                                                                                                                                                                                                                                                                                                                  | PASS              |
| 2   | Built-ins first          | 13 servers. Built-ins come first: config-map, figma, gchat, gcp, git and host-script (remote or beside grain), then api, github, grain, model-proxy, release and repo (in-process). The custom `capabilities.demo.grain.example/cloud` comes last. figma, gchat and host-script read `needs configuring`.                                                                                                                                                                                                                             | PASS              |
| 3   | Disable / Enable         | On gchat's page, Disable sends `POST /api/capability-servers/disable`. The chip still reads `needs configuring` at +300 ms and reads `disabled` ("Turned off: grain does not connect to it…") by +3 s, with no reload. The list shows `disabled` too. Enable reads `pending` and then `needs configuring`. Built-ins never offer Remove.                                                                                                                                                                                              | PASS              |
| 4   | host-script server       | Edit server settings → Add scripts. Name `walk-hello`, description, timeout 30, a bash body. Test lists one method `host-script` with `script` allowed `"walk-hello"`. Saving moves the chip from `needs configuring` to `attached`, and the script persists after a reload.                                                                                                                                                                                                                                                          | PASS              |
| 5   | config-map / secret maps | A config map `walk-cm` holding `hello.txt`. Test lists the `config-map` method with `walk-cm` and `walk-cm/hello.txt` beside the built-in `bootstrap` playbooks. Saved, the page reads "Configured maps (1 config map, 0 secret maps) · walk-cm · /home/debian/.config-map/walk-cm". Remove config map then Save leaves `config_maps: []`. Secret maps sit on the same form.                                                                                                                                                          | PASS              |
| 6   | Host placement           | Run location "host (through the host agent)" is disabled until the command is one the host agent's allowlist offers, saying why ("…has no capabilities entry for "agy" (it offers corp-search)"). With `corp-search`, the Host picker lists the default host, build-runner and cloudtop-alice (not connected), local (connected, logins antigravity and claude), and a label selector. Adding one placed on build-runner is refused by the demo process: `400 … this process does not route placed capability servers to their host`. | PASS (demo limit) |

### 4. Capability policy and API scope sets — pass

- **Policy:** Add rule → effect `Deny`, capability
  `https://capabilities.demo.grain.example/cloud#cloud-viewer`, agent tasks
  ticked. Save policy sends a preview and then a `PUT`, and the rule
  persists after a reload. In New task, Add grant… lists `cloud-viewer`
  `aria-disabled` with "the capability policy denies … (rule 1: deny …)"
  and a link to `/settings/capabilities#capability-policy`. Removing the
  rule and saving leaves `rules: []`. `capability-policy-pickers.spec.js`
  covers templates, schedules, tasks and repository defaults, and re-ran
  green.
- **Scope sets:** New scope set `walk/throwaway` with scope `tasks:read`
  saved to `tables/api_scope_set.json`. Edit changed its description, and
  both persist after a reload. Remove asks "remove scope set
  walk/throwaway? Nothing grants https://grain.dev/capabilities/api#walk/throwaway."
  and the set is gone after a reload.

### 5. Deployment credentials and other secrets — pass

- Old links `/settings/credentials`, `/settings/legacy-capabilities`,
  `/admin/integrations` and `/system/integrations` land on
  `/settings/capabilities#credentials` with the card at y=89 (header
  bottom 77). `/settings/secrets` lands on `#other-secrets`, at the foot of
  the page.
- The card has the GitHub host (needs restart), plain HTTP, GCP project,
  legacy service-account email and count, with pointers to the `gcp`
  server.
- Other secrets: set `walk-throwaway/value`. The value box is
  `type=password`, the value never appears in the DOM, the row survives a
  reload, and delete asks "Delete the secret walk-throwaway and every key in
  it? …" before sending `DELETE`.
- **Role gating** (stub 2), each as its own role:

  | Scopes added to read-only | GitHub host / GCP | Secrets | Add server / policy |
  | ------------------------- | ----------------- | ------- | ------------------- |
  | none                      | off               | off     | off                 |
  | `settings:write`          | ON                | off     | off                 |
  | `secrets:write`           | off               | ON      | off                 |
  | `capabilities:admin`      | off               | off     | ON                  |

  Each disabled group names the scope it needs ("Secrets are read-only for
  you. Needs secrets:write, which your role custom does not grant…").

- **Locked mode** (server 3): the credential fields are disabled, and Other
  secrets still sets and deletes.

### 6. Hub (`/settings/registry`) — pass

- **Chips** (stub 1): `not configured`, `connecting` ("Waiting for first
  heartbeat…"), `degraded` (health degraded, plus the error alert) and
  `heartbeat error` (the alert, plus "Scope conflict reported by the hub:
  acme/widgets (svc/) already claimed as primary by other-grain").
- **Heartbeat now** (unstubbed) sends `POST /api/registry/heartbeat` and
  updates Last heartbeat.
- **Scope overrides:** the rule options are primary, secondary and exclude
  repo. Adding `acme/widgets`, `docs/`, secondary persists after a reload
  as `pathPrefix:"docs"`, and removing it persists `scopes: []`. That
  remove `PUT` omits `authMode`, which the server reads as `token`
  (`model.NormalizeHubAuthMode`), so nothing changes.
- **Locked mode** (server 3): every field and select is `:disabled` through
  its fieldset, and the registration token and its upload stay editable.

### 7. Engine, CI, Caches and Sandboxes — pass

| Field                      | Outcome                                                                                                                                                                                                                                                            |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Poll interval              | Placeholder `10s`. `3s` saves and reloads. Blank saves `""` and reloads as `10s`. `banana` → "Couldn't save Poll interval … invalid duration".                                                                                                                     |
| Max worker agents          | `7` saves. Blank saves `10`. `-3` → "must be at least 1; -3 was not saved", with no request. No placeholder, as noted on 11-11.                                                                                                                                    |
| Max merges per repo branch | Placeholder `5`. Blank saves `5`. `0` is refused with no request.                                                                                                                                                                                                  |
| CI runs kept               | `-5` saves: any negative means unbounded (`model.Config.CIRunHistory`).                                                                                                                                                                                            |
| Proxy cache max size (GiB) | `-1` → "must be at least 0; -1 was not saved".                                                                                                                                                                                                                     |
| Auto CPU minimum / maximum | `0.1` → "Auto CPU minimum must be at least 0.25". `500` as the maximum saves: there is no upper bound by design (`OvercommitSettings.Validate`).                                                                                                                   |
| Session sandbox idle TTL   | `5x` → "unknown unit "x"".                                                                                                                                                                                                                                         |
| Disk floor share           | `0.9` saves; the daemon caps the floor at `sandboxDiskFloorMaxShare` 0.25 of each volume.                                                                                                                                                                          |
| Kubernetes                 | Prefilled `grain`, the sandbox-k8s and sandbox images, `/home/agent`, and the grain-interactive / grain-agent / grain-ci priority classes. `Bad_NS`, `nonsense` (node selector) and `Not A Class` are each refused by name.                                        |
| Custom sandbox image       | Shows the contract (v2), the `FROM docker.io/grainai/sandbox-k8s-base:latest` snippet and the base image. Setting `registry.example.test/walk:1` shows Reset to default and the unverified-contract note; clearing it saves `""` and `sandboxImageDefault` stands. |
| Backend                    | Granule, host directories and None (control plane only) are disabled with their reasons ("needs -disable-native-sandboxes at daemon startup").                                                                                                                     |

## Bugs

### Bug 1: `/next/` Hosts acted on one click and offered actions `local` cannot take (fixed)

- **Reproduction:** open `/next/settings/hostds` on any demo, with the
  browser set to dismiss dialogs. Revoke on `cloudtop-alice` sends `POST
/api/hostds/cloudtop-alice/revoke` at once. Rotate, Revoke and Delete on
  `local` are enabled and each draws a 400.
- **Cause:** `SettingsComputeComponent`'s `rotateHostd`, `revokeHostd`,
  `deleteHostd`, `deleteRevokedHostds`, `rotateEnrollmentCode`,
  `revokeEnrollmentCode` and `rejectPendingHostd` called the API directly.
  The row template rendered all three actions for every host, and Delete
  for hosts never revoked.
- **Fix:** each now opens the shared `g-confirm-dialog` (destructive,
  Cancel focused) with the classic page's wording. `local` renders no row
  actions, Delete is enabled only once a host is revoked, and Delete /
  Delete revoked report what they deleted, the usage-limit pauses they
  cleared and the hosts they skipped.
- **Coverage:** `ui-ng/e2e/next-hosts-confirm.spec.js`.

### Bug 2: `/next/` pending host requests showed a pairing code nobody compares (fixed)

- **Reproduction:** the seeded `devbox-carol` request in
  `/next/settings/hostds` showed "Pairing code ZXQN-H95Y" and no
  fingerprint.
- **Fix:** the column is now Key fingerprint, as on the classic page. A
  50-character fingerprint first widened its column to 415px (867px of
  table on a phone), so it wraps in a 16–28ch mono cell (`.s-fingerprint`):
  220px on desktop and 140px on a phone.
- **Coverage:** the same spec asserts the enrollee and `SHA256:` are shown
  and no pairing code is.

### Bug 3: Git Test ignores a connection's proxy (filed)

Step 1.6. `Client.checkGitHubCredentialForHost`
(`pkg/ui/team_git_connections.go`) probes with
`githubtoken.ProbeTransport(probeHost, insecure)`, which never consults
`gitproxy.CredentialHost.Proxy`. The connection itself goes through the
proxy, so Test judges a path the connection never uses. A host reachable
only through its proxy reads as unreachable. The fix belongs in the probe
transport (`pkg/capability`, a classified path), so it was proposed as a
follow-up task with the reproduction rather than changed here.

## Layout checks (`/next/settings/hostds`, fixed build)

`make ui-screenshots` was run as `node ui/e2e/screenshot.mjs` with
`BIN=` set to the base binary (`OUT=…/before`) and the fixed one
(`…/after`, and `…/after-dialog` with `CLICK` on Revoke), at desktop and
phone in light and dark. The PNGs could not be looked at here, so the same
pages were measured instead, at 1400×900 and 390×844 in both colour
schemes:

- `document.scrollWidth == clientWidth` at both widths (no page overflow).
  The pending table scrolls inside its wrapper on a phone (591 of 314px,
  down from 867).
- The confirm dialog is 570×258 at x=415 on desktop and 358×278 at x=16 on
  a phone, both inside the viewport. Focus starts on Cancel, and Cancel and
  Revoke host share a row.
- The `local` row is 43px tall with no buttons; the other rows keep theirs.

## Observations, not changed

- The demo serves its built-in capability servers over loopback HTTP with a
  literal `Authorization: Bearer …` header (`pkg/demo/demo_capability_server.go`),
  and the server page prints it. A real daemon starts built-ins over stdio
  with the token in the environment, so this is demo wiring.
- "ambient (this host's github login)" is the auth label on a plain git
  host as well, where it means the host's git credential helper. Test
  explains that correctly ("configure a credential helper with a login for
  git3.example.test").
- Couldn't save offers Retry on a 400 validation refusal (`banana`, `5x`),
  where retrying cannot succeed.
- The classic Hub card in `not configured` still shows Heartbeat now
  enabled when the stub leaves a URL in place. A deployment that is truly
  disabled has no URL, so this was only stub-deep.
- The task names `secret-map` and `host-process` capability servers and
  host-process "version, adopted runs and allowlist suggestions". Neither
  server exists in this tree. Secret maps are part of `config-map`, and host
  scripts are the `host-script` server, which reports its methods but no
  version or adopted runs.

## Not covered

- The needs-host warning on a placed server whose host is away: the demo
  process refuses to save a placed server (step 3.6).
  `hostd-placement.spec.js` covers it with stubs and re-ran green.
- `gcp` keyless-token vs service-account-key mode and figma's Test and
  Connect Figma: walked on 11-11, and covered by
  `capability-server-settings.spec.js`, which re-ran green.
- Hosts Edit, Rename and Accept with edits on classic: covered by the
  re-run specs below, not driven by hand again.

## Specs run

- `ui-ng`: `e2e/next-hosts-confirm.spec.js` (new) and
  `e2e/next-full-system.spec.js` — 6 passed.
- Classic `ui`: `hostds-delete`, `hostds-revoke-confirm`,
  `hostds-capacity-and-banner`, `hostds-settings-layout`,
  `git-connection-refusal-banner`, `git-connections-multihost-walkthrough`,
  `capability-in-process-servers`, `capability-info-popovers`,
  `capability-policy-pickers`, `capability-server-settings`,
  `hub-settings-layout`, `k8s-sandbox-settings-walkthrough`,
  `sandbox-disk-floor-blank`, `sandbox-disk-overcommit`,
  `custom-sandbox-image-layout`, `host-script-settings` and
  `hostd-placement` — 35 passed.
