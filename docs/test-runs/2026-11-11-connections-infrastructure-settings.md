# Connections & infrastructure settings walkthrough — 2026-11-11 (task 4303)

- **Date:** 2026-11-11, 08:37–09:45 UTC, one run.
- **Task:** task 4303, branch
  `grain/098073e4/task-4303-walk-through-settings-git`, base `main`.
- **Commit tested:** `8097b80d` ("Merge pull request #1179"), binary
  `v0.0.0-20261111083700-8097b80d9eaf`. Each fix below was checked again
  in the browser on a rebuild of that commit with the fix applied.
- **Focus:** the 2026-11-08 and 2026-11-09 reports cover this area in
  depth. This run walked every section in scope again on current `main`
  and spent its extra time on what those runs could not reach: a real
  host enrollment and rekey (not only the seeded pending request), the
  destructive actions on Hosts, the Hub settings after their move to the
  `hub` key (`0a31a108`), the figma account form, config-map and secret-map
  placements, and locked (external config repo) mode.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0.
  - Playwright from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900.
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -git-proxy -secrets-store -access-code-file <file>`, with
    `TMPDIR` set to a fresh directory on every start, so each store was a
    throwaway embedded SQLite file. It was restarted on a fresh store after
    the Git section (which removes the demo's only git connection), after
    each rebuild, and once with `-external-config-repo
https://github.com/example/walk-config` for locked mode.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through the Access code page, then acts only
  through visible controls (clicks, typing, `Tab`, `Enter`, `Space`,
  `Escape`). Every page recorded uncaught page errors, console errors and
  warnings, failed requests, `/api/` responses of 400 or more (with
  bodies), every native dialog (its text, and whether it was accepted),
  and every non-GET `/api/` request with its body. The REST API (bearer
  access code) was used only to read back what a gesture persisted.
- **Console baseline:** each sign-in logs one `401` for the
  unauthenticated first load, which is expected and left out below. No
  uncaught page error and no 5xx response was recorded. Every console
  error was the browser's own line for an intended 400 refusal listed in
  the steps below.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout.

1. **Host enrollment identity.** `grain hostd -join` asks the enrolling
   person to prove who they are with GitHub's device flow against real
   github.com, which this run must not complete. Instead, a scratch Go
   program outside the checkout (`/var/tmp/walk/joiner`) called the real
   `hostd.Enroll` with `IDToken` returning the demo's access code. The
   daemon accepts that as the enrollee (`verifyHostJoinEnrollee`'s
   access-code branch). It filed one real join request with the `default`
   code and one real rekey request for a legacy-token host, then waited
   for acceptance like the real CLI.
2. **figma.com.** A Playwright route answered every `*.figma.com`
   request with a stub page, so Connect Figma's OAuth popup never left
   the sandbox.

Nothing else was stubbed. The git hosts, GitHub App and proxy used in
Git are made-up names that do not resolve, so each Test reports what it
could not reach rather than hitting a stub.

## Steps and outcomes

### 1. Routes and old links

| #   | Step                 | Outcome                                                                                                                                                                                                                                                                                                    | Verdict |
| --- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Every route in scope | `/settings/git`, `/settings/hosts`, `/settings/hostds`, `/settings/capabilities`, `/settings/registry`, `/settings/engine`, `/settings/ci`, `/settings/sandboxes` and `/settings/caches` each render their cards. `/settings/hosts` lands on `/settings/hostds`, the section's id (`settingsSections.js`). | PASS    |
| 2   | Old links            | `/settings/capability-servers` → `/settings/capabilities`. `/settings/credentials`, `/settings/legacy-capabilities`, `/admin/integrations` and `/system/integrations` → `#credentials`. `/settings/secrets` → `#other-secrets`. The `#credentials` card lands at y≈88, clear of the header.                | PASS    |

### 2. Git

| #   | Step                              | Outcome                                                                                                                                                                                                                                                                                                   | Verdict |
| --- | --------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | GitHub App offered only on GitHub | With Kind = Plain git, the Auth type menu's GitHub App is `aria-disabled` with the title "A GitHub App needs a GitHub host: a plain git host has no App API". With Kind = GitHub, it is enabled.                                                                                                          | PASS    |
| 2   | Plain git, token                  | `git.walk4303.example` + PAT saved as a `git` / `pat` row. Test says nothing was tested and why (no repository on that host is in reach), and how to give it one.                                                                                                                                         | PASS    |
| 3   | Plain git, SSH key                | `not-a-key` is refused with "the private key will not parse: ssh: no key found", once in the card and once in the shell banner (deliberate, `GitConnections.jsx:361`). A throwaway Ed25519 key with a `known_hosts` line saves as `ssh key`. Its Test explains the same reach limit.                      | PASS    |
| 4   | GitHub token, scope and proxy     | A proxy URL with a login is refused with the password masked (`http://u:xxxxx@…`). With the bare proxy address it saves, showing "via proxy …" and scope `walk4303-org`. Test: "could not reach GitHub … no such host".                                                                                   | PASS    |
| 5   | GitHub App, scope and proxy       | App ID, installation and a throwaway RSA key save as `github app` with scope and proxy, and persist across a reload. Test: "no installation token could be minted from this GitHub App credential".                                                                                                       | PASS    |
| 6   | Remove asks first                 | Remove asks "Remove the git.walk4303.example connection …? A credential stored for it is deleted with it …". Dismissing keeps the row. Accepting removes it, which a reload confirms.                                                                                                                     | PASS    |
| 7   | No git connection                 | After removing the demo's only connection, Repos and New task both say "This deployment has no git connection yet … a GitHub token or GitHub App for github.com, or a token, SSH key or ambient login for another git host", linking to Settings → Git. Clicking the New task link opens `/settings/git`. | PASS    |
| 8   | GitHub host pointer               | Shown only in locked mode (`git-secret-note`, step 9.1). Its link opens `/settings/capabilities#credentials` with the card in view.                                                                                                                                                                       | PASS    |

### 3. Hosts

| #   | Step                          | Outcome                                                                                                                                                                                                                                                                                                                                                    | Verdict      |
| --- | ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| 1   | New code                      | Allowed principals `carol@example.com` is refused: "must start with user:, group:, github: or org:". With `user:carol@example.com, github:dave`, expiry `48h`, max uses 3, capacity 2 and labels, the code is created. It shows chips for uses 0/3, capacity, labels, both principals and the expiry date. `Enter` on a code chip switches the snippet.    | PASS         |
| 2   | Real join and rekey (stub 1)  | Both real requests appear in Pending: fingerprint, "Enrolled by", version, and `code: default` or `rekey: walk-legacy`. The API's `pairing_code` is never shown. Accept with an edited name, labels and capacity makes `walkbox` key-bound (`SHA256:…`, "via default", `cap=2`). The rekey turns `walk-legacy` key-bound. Both enrollers print "Accepted". | PASS         |
| 3   | Legacy token host             | Add host shows the token once, with the install command. The row shows `ghd_…`.                                                                                                                                                                                                                                                                            | PASS         |
| 4   | Edit, Rename                  | Owner, labels and capacity save. Capacity `zero` is refused by name. Blank capacity saves `capacity: 0` (chip gone) and takes the banner down. A reload confirms. Rename to `Bad Name` is refused with the DNS-label rule, and `build-runner-2` is accepted.                                                                                               | PASS         |
| 5   | Revoke, Rotate, codes, Reject | Revoke sent `POST …/revoke` on one click, with no dialog. The same holds for Rotate (token), Rotate code, Revoke code and Reject. **Bug 1.**                                                                                                                                                                                                               | FAIL → fixed |
| 6   | Delete, Delete all revoked    | Delete asks first. Dismissing keeps the host, and accepting reports "Deleted host old-laptop.". Delete all revoked asks first and reports "Deleted host build-runner-2.".                                                                                                                                                                                  | PASS         |
| 7   | Connected key-bound row       | Not reached. The demo daemon refuses host agents dialing in ("start it with -hostd-token-file"), so `walkbox` stays "Never connected". `HostdsSection.test.jsx` covers the "Connected from … as … (build)" text.                                                                                                                                           | NOT COVERED  |

### 4. Capabilities

| #   | Step                             | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Verdict            |
| --- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------ |
| 1   | Deprecation notices              | All seven are present: `gcp-key`, `model-proxy`, `gemini-key`, `release`, `grain-ci`, `bootstrap-playbooks` and `github-sandbox`.                                                                                                                                                                                                                                                                                                                                                                                                                        | PASS               |
| 2   | Server list                      | 13 servers: the built-in remote and beside-grain ones (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`), then the in-process ones (`api`, `github`, `grain`, `model-proxy`, `release`, `repo`), then the demo's custom one. The task brief's `secret-map` is now part of `config-map` ("Secret maps"), and its "host process server" is `host-script`.                                                                                                                                                                                       | PASS               |
| 3   | `(i)` popovers                   | Clicking opens one, and it stays open when the pointer leaves. `Escape` closes it. With focus, `Enter` toggles it open and shut, and `Space` opens it.                                                                                                                                                                                                                                                                                                                                                                                                   | PASS               |
| 4   | Disable and Enable               | Disable on the custom server flips its chip `attached` → `disabled` with no reload, and the list shows `disabled`. Enable → `pending`. Built-in in-process servers (`github`) offer no Disable or Remove.                                                                                                                                                                                                                                                                                                                                                | PASS               |
| 5   | needs-configuring and needs-host | `host-script` and `figma` read "Needs configuring: it offers no methods until it is configured …". Run location "host" is disabled with the reason: "this daemon has no host agent beside it …". So no host-placed server, and no needs-host warning, could be made in the demo.                                                                                                                                                                                                                                                                         | PASS / NOT COVERED |
| 6   | Host scripts                     | Timeout `0` is refused (`scripts[0].timeout: must be at least 1`). `walk-echo` saves, survives a reload and is edited in place. Its Test runs with `["alpha beta", "$(echo not-expanded)"]` and echoes both literally (exit 0), with no shell expansion. The argument's Remove button works from `Enter`.                                                                                                                                                                                                                                                | PASS               |
| 7   | Throwaway server                 | Test before saving reports what the server offers. Add server opens its page. Bind a secret (password input) stores `walk4303-echo-token` with `$secret:` in the env. Rename asks in a dialog ("… becomes …echo2. Nothing grants …") and moves the page. Test works after the rename. Remove asks, `Escape` cancels it, and confirming removes the server.                                                                                                                                                                                               | PASS               |
| 8   | Server secrets                   | The server's secret is listed apart, under "Used by capability servers" with a link to the server, while Other secrets says "No other secrets set". After Remove, a secret bound by name stays in the store, listed as an orphan with Delete secret. That is by design: `RemoveCapabilityServer` deletes only grain-made config secrets, because a bound name may be shared or may already have existed.                                                                                                                                                 | PASS               |
| 9   | figma                            | The account's "Allow short-lived token placement (figma#token)" switch was missing, replaced by "allow_sandbox_token looks like a secret and is not offered here: with: arguments must never carry secrets". **Bug 3.** After the fix it saves `allow_sandbox_token: true`, and the account reads "oauth · figma#token allowed". Test shows "Figma refused: … no OAuth client secret …". Connect Figma opens the Figma OAuth URL in a popup (stub 2), and the page names the callback URL to register. Leaving with unsaved edits raises `beforeunload`. | FAIL → fixed       |
| 10  | gcp                              | Each permission set's credential offers "token (recommended, no keys)" and "key (service-account key pool)".                                                                                                                                                                                                                                                                                                                                                                                                                                             | PASS               |
| 11  | config-map and secret maps       | A throwaway config map (`walk4303/hello.txt`) and secret map (`walk4303-sec/token.txt`, value masked) save. The file content is in the plain config, while the secret value is not: it goes to the managed secret `capability-server-…/secret_values`. The page lists both maps with their directories. Removing both through the form clears them from the config.                                                                                                                                                                                      | PASS               |
| 12  | Holder link                      | A "Task #1" holder link on `github` opens `/tasks/1`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | PASS               |

### 5. Capability policy and API scope sets

| #   | Step       | Outcome                                                                                                                                                                                                                                                                                                                | Verdict |
| --- | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Deny rule  | Deny `…/cloud#cloud-viewer` sends a preview, then saves, and persists across a reload. In New task → Add grant…, `cloud-viewer` is `denied` and cites "rule 1: deny …". The three `release` methods say "CI only …", not policy. Following "settings → capabilities" opens `#capability-policy` with the card in view. | PASS    |
| 2   | Scope sets | `grain/walk` is refused: "the grain/ prefix is reserved". `walk4303/scopes` saves, and New task's picker offers it without a reload. Remove asks ("Nothing grants api:walk4303/scopes.") and takes it off, which a reload confirms. The held-by-a-grant case was not repeated: the 11-09 run covered it.               | PASS    |

### 6. Deployment credentials and secrets

| #   | Step             | Outcome                                                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| 1   | Throwaway secret | The value input is `type=password`. `walk4303-throwaway/token` is set, `extra` is added, and `token` is replaced in place. No value ever reached the DOM or `GET /api/secrets`. Delete secret asks first, from the keyboard (`Enter`), and removes the secret. The two orphaned secrets from 4.8 were deleted the same way. The panel has no per-key delete. | PASS    |
| 2   | Links            | The credentials card links to the `gcp` server page three times, and to Other secrets and Capability servers.                                                                                                                                                                                                                                                | PASS    |
| 3   | Locked mode      | See 9.1.                                                                                                                                                                                                                                                                                                                                                     | PASS    |

### 7. Hub

| #   | Step                    | Outcome                                                                                                                                                                                                                                                                                                                | Verdict      |
| --- | ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| 1   | Status card             | The chip reads `connected`, with the endpoint, last heartbeat, health and "2 active, 2 awaiting reply". Advertised scopes carry origin chips ("from repo config", and "added in settings" once a rule is added). Heartbeat now sends `POST /api/registry/heartbeat`.                                                   | PASS         |
| 2   | Fields                  | `not a url` is refused by name. URL, entry, OIDC source, audience, display name, purpose and host kind save under the `hub` key and persist across a reload. OIDC audience is disabled while the source is the token.                                                                                                  | PASS         |
| 3   | Listed in hub directory | Unticking sends `"listed": false`, but after a reload the box is ticked again and the setting has no `listed`. **Bug 2.** After the fix it persists unticked, and ticking persists ticked.                                                                                                                             | FAIL → fixed |
| 4   | Host kind Auto-detect   | With a kind chosen, Auto-detect read "Auto-detect (kubernetes)": the chosen kind, not what Auto-detect would pick (part of Bug 2). It now reads "Auto-detect" while a kind is chosen, and "Auto-detect (workstation)" once none is.                                                                                    | FAIL → fixed |
| 5   | Scope overrides         | The rule menu offers primary, secondary and exclude repo. Primary `acme/walk-primary`, secondary `acme/walk-secondary` at `svc/api` and exclude `acme/walk-excluded` persist across a reload. A duplicate is refused ("duplicate rule for acme/walk-primary"). Each "Remove scope rule for …" button removes its rule. | PASS         |
| 6   | Locked mode             | See 9.1.                                                                                                                                                                                                                                                                                                               | PASS         |

### 8. Engine, CI, Caches and Sandboxes

| #   | Step         | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | Verdict |
| --- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Engine       | Placeholders: Poll interval `10s`, Max merges `5`. `soon`, `0` and `-1` are refused by field name. `25s` / 6 / 3 persist across a reload, and blanking all three gives `10s` / 10 / 5. There was no sandbox commitment note: the demo reports no `sandboxCommitment`, so the note renders nothing, by design (`SandboxCommitmentNote.jsx:41`).                                                                                                                                                                                                                                                                                                                                          | PASS    |
| 2   | CI           | The four cards show default placeholders. `-2`, `nope`, `-5` and `1.5` are refused by name. Fourteen non-default values read back identically after a reload in both the inputs and `GET /api/settings`, and blanking resets them.                                                                                                                                                                                                                                                                                                                                                                                                                                                      | PASS    |
| 3   | Caches       | `-3` and `1.5` are refused. 15 and 12 GiB persist, and blanks reset to 0 (placeholder 20).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | PASS    |
| 4   | Sandboxes    | Granule, Host directories and "None (control plane only)" are disabled with the startup flag each needs. The backend has a description and a "needs restart" badge. Network offers egress and none. `-1`, `10`, `nope`, `1.5` and `-1` are refused by name. Auto bounds: `0.1` → "Auto CPU minimum must be at least 0.25", memory max `0.5` → "memoryMin 1.00 is above memoryMax 0.50", pressure `150` → "cannot be more than 100". Auto mode values persist. A blank disk-floor minimum resets to 5120, while `0` saves `0`. Fixed ratios persist. Auto CPU max `100` and a fixed CPU ratio of `0.5` are accepted: the model has no upper or ≥1 bound (`OvercommitSettings.Validate`). | PASS    |
| 5   | Kubernetes   | Prefilled with `grain`, the image, `/home/agent`, and the priority classes `grain-interactive`, `grain-agent` and `grain-ci`. `Bad_NS`, `pool`, `Bad Class` and `not an image ref` are refused by name. Overrides persist, and blanks reset.                                                                                                                                                                                                                                                                                                                                                                                                                                            | PASS    |
| 6   | Custom image | `::bad::` is refused. A valid reference shows the "contract unverified" notice, the `grain sandbox-image check` hint, the base-image Dockerfile and the contract label. Clearing it saves `""` and the notice goes, so it reverts cleanly to the default image. No mirror hint showed, because the demo configures no image mirror.                                                                                                                                                                                                                                                                                                                                                     | PASS    |

### 9. Locked mode (`-external-config-repo`)

| #   | Step             | Outcome                                                                                                                                                                                                                                                                                                                                        | Verdict |
| --- | ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Dimmed vs usable | Git hides Add connection and shows the write-only note with its pointer. Credentials fields are `disabled`. Other secrets still sets and deletes. In Hub, every field and select is inside a disabled fieldset (typing changes nothing), the scope inputs are disabled, and the Registration token stays editable. Engine fields are disabled. | PASS    |

## Bugs

### Bug 1: Hosts revoked, rotated and rejected on one click (fixed)

- **Reproduction (on `8097b80d`):** on Settings → Hosts, click Revoke on
  `build-runner`. `POST /api/hostds/build-runner/revoke` is sent at once,
  with no dialog, and the row reads "Revoked". There is no un-revoke: the
  only way back is enrolling the machine again. Rotate (host token),
  Rotate code, Revoke code and Reject do the same, while Delete asks
  first.
- **Fix:** `HostdsSection.jsx` now confirms each of the five, saying what
  it breaks. Declining sends nothing.
- **Coverage:** a `HostdsSection.test.jsx` case for all five (declining
  sends nothing, confirming sends the request) and
  `ui/e2e/hostds-revoke-confirm.spec.js` (dismiss keeps the host across a
  reload, accept revokes it). `hostds-capacity-and-banner.spec.js` now
  accepts the Revoke code dialog in its cleanup.

### Bug 2: Hub "Listed in hub directory" could not be turned off (fixed)

- **Reproduction:** on Settings → Hub, untick Listed in hub directory.
  `PUT /api/settings` sends `"listed": false`. Reload: the box is ticked
  again, and `GET /api/settings` has no `listed`. `model.HubConfig.Listed`
  is `bool,omitempty`, so false is never stored, and the box drew a
  missing value as ticked (`reg.listed !== false`). The daemon, however,
  registers an entry listed only when the value is true, and the hub
  design says `listed` defaults to false. So a fresh grain showed as
  listed while it was not. Separately, Host kind's "Auto-detect (X)"
  named the chosen kind whenever one was chosen.
- **Fix:** the box ticks only for an explicit `true`, and Auto-detect
  names a kind only while none is chosen.
- **Coverage:** a `HubSection.test.jsx` case.

### Bug 3: figma's `allow_sandbox_token` could not be set from Settings (fixed)

- **Reproduction:** open the figma server, then Edit server settings and
  Add accounts. The account has no "Allow short-lived token placement"
  switch; it shows "allow_sandbox_token looks like a secret and is not
  offered here: with: arguments must never carry secrets" instead.
  `isSecretLooking` hid any argument whose name matched `token` and
  similar words. A server's settings switch that rule off only at the top
  level, not inside an array item such as an account.
- **Fix:** a boolean, integer or number is not secret-looking unless the
  schema marks it `writeOnly` or a password. None can hold a secret.
- **Coverage:** a `capabilityArgs.test.js` case. The switch was then
  saved through the browser (step 4.9).

## Observations, not changed

- The demo's only git connection is on `127.0.0.1:<sim port>`, while
  Settings → Git's repository card says "No connection reaches this
  repository" for `acme/widgets`. Its Test fails with "server gave HTTP
  response to HTTPS client", because `githubInsecureHttp` is off in the
  demo. That is demo seeding, not a product fault.
- The seven deprecation notices sit above every Capabilities page,
  including each server's detail page, and push the servers well down.
- Max worker agents has no placeholder, while its siblings show their
  defaults.

## Not covered

- A connected key-bound host row (address, principal, build), a host
  placement and the needs-host warning, and the host process server's
  version, adopted runs and allowlist suggestions: the demo takes no
  dial-in host and has no host agent beside it.
- `may_be_refused` verdicts in repo-less pickers (templates, schedules),
  and Remove on a scope set that a grant holds: the 2026-11-09 run
  covered both.
- Role gating by `settings:write` and `secrets:write`: the demo signs in
  only as the owner.
- Hub status states other than `connected`: the demo hub reports only
  that one.
- The screenshots (`make ui-screenshots
PAGES=/settings/registry,/settings/hostds`) were taken but could not be
  looked at: the file reader returns PNG bytes, and there is no Gemini
  key for `LOOK=1`. The fixes change no layout, and
  `hub-settings-layout.spec.js` and `hostds-settings-layout.spec.js`
  (bounding-box checks at desktop and phone) pass.
