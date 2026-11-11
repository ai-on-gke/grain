# Connections and infrastructure settings walkthrough (task 3359) — 2026-11-08

Task 3359: Settings → Git, Capabilities (with Capability policy and API
scope sets), Legacy capabilities, Engine, CI, Sandboxes and Caches, in a
real Chromium against a real `grain demo`.

Task 3163 walked the same area earlier today
(`2026-11-08-connections-infrastructure-settings.md`), on a tree only a few
merges older. This run repeats its core flows, but it spends most of its
time on what that report listed as not driven or not verified:

- "Reuse authentication from" with nameless sources
- the Merge queue sandbox commitment note
- the "None (control plane only)" description
- the mirror hint
- the version, adopted runs and allowlist suggestions the host agent
  reports

## Commit and environment

- Commit tested: `10df6284` (main, "Merge pull request #913"). Steps marked
  "fixed build" ran on that tree plus this branch's fix.
- Sandbox: Debian, Go 1.26.10, Node 20.19.0, Playwright Chromium 1234
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). I ran `npm ci && npm run
  build` in `ui/`, and `ui/e2e/global-setup.js` built the `grain_e2e`
  binary.
- Server: `grain demo` over a throwaway embedded store, one per Playwright
  worker (`ui/e2e/fixtures.js`). No config repository.
- This sandbox has no `/home/debian/.gemini-api-key`, so no Gemini-backed
  agent or screenshot description was used.
- Throwaway walk specs (`ui/e2e/zz-walk3359-{dump,gaps,git,infra}.spec.js`,
  not committed) logged each step's outcome. They also logged every
  console error, `pageerror`, failed request and HTTP response ≥ 400.
  Viewport: Playwright's default 1280×720.
- Throwaway credentials only: an `ssh-keygen` ed25519 key, an `openssl
  genrsa` 2048-bit key for the GitHub App, and made-up tokens.

## Stubs and intercepted routes

| Route | Where | What was changed | Why |
|---|---|---|---|
| `GET /api/settings` (real response, `sandboxCommitment` added) | Engine → Merge queue, Sandboxes | A capped commitment: 10 workers, 5 mergers, 4 CI jobs, a 16 vCPU / 64 GiB host and a fixed-mode budget | The demo reads no host capacity, so it reports no commitment |
| `GET /api/settings` (real response, backend fields replaced) | Sandboxes | `sandboxBackend: "none"`, None available, container unavailable ("stubbed away"), and `k8sGranuleImage`/`k8sGranuleImageMirror`/`k8sGranuleImageEffective` set to a mirrored guest ref | The demo cannot start with `-disable-native-sandboxes` and has no mirrored image |
| `GET /api/settings/cli-run` (real response, `hostExecutor` replaced) | Models → CLI run locations | A reachable host agent: a pending upgrade (current ≠ running), one suggested allowlist entry, one adopted detached CLI run and one ended capability process | The demo's executor is `kind: "local"`, with no host agent |

Nothing else was stubbed. The stubs above went through `route.fetch()`, so
every other field came from the real server.

Every git connection Test went to the real server's check. The GitHub PAT
and GitHub App checks reached real github.com and were refused, as
throwaway credentials should be. Agent connections were seeded with
`PUT /api/deployment/agent-backends/<id>` and removed in `finally`.

## Steps and outcomes

### Git (`/settings/git`) — one bug found and fixed

- The page loads with no console errors. The table holds the demo's
  `github.com · github · pat · all repos` row. The add form is inline under
  "New git connection", not a dialog.
- With Kind "Plain git", the Auth type menu offers PAT, SSH key, Ambient,
  and GitHub App with `aria-disabled="true"` and the title "A GitHub App
  needs a GitHub host: a plain git host has no App API". With Kind GitHub,
  GitHub App can be chosen.
- SSH key on `ssh.walk3359.example`:
  - The form has "SSH private key", "Known hosts (optional)" (helper:
    "Needed for any host but github.com: grain never trusts a host key on
    first use") and "API token (optional)".
  - A key reading `not a key` is refused (400): "the private key will not
    parse: ssh: no key found".
  - The real key with a known_hosts line saved as `ssh key · all repos`.
  - Test answers "nothing was tested: an SSH key is checked by reading a
    repository with it, and no repository on ssh.walk3359.example is in
    this check's reach …".
- **Bug:** after that successful save, the shell's error banner still read
  "the private key will not parse" over the new row, until its five-second
  timeout. Details and the fix are under "Bug" below.
- PAT on the plain host `git.walk3359.example` saved, and Test gives the
  plain-git-host "nothing was tested" message.
- GitHub PAT scoped `walk3359-org`:
  - A proxy URL carrying a login is refused, with the password masked:
    "proxy "http://user:xxxxx@proxy.walk3359.example:3128" carries a login,
    and a host record is not a secret …".
  - The bare proxy address saved, and the row reads `pat | via proxy
    http://proxy.walk3359.example:3128 | walk3359-org`.
  - Test: "GitHub rejected the token (bad credentials): it is wrong,
    revoked or expired".
- GitHub App scoped `walk3359-app/widgets` (App ID, Installation ID, App
  private key) saved as `github app`. Test: "no installation token could
  be minted from this GitHub App credential …".
- A reload shows all five rows.
- Remove asks first in every case, through a native `confirm`: "Remove the
  ssh.walk3359.example connection grain-git-c-…? A credential stored for it
  is deleted with it and cannot be read back …".
  - Opened from the keyboard (focus, then Enter) and dismissed, the row
    stays.
  - Accepted, the row goes, and it stays gone after a reload.
  - Afterwards only the github.com default remains, and `/api/secrets`
    lists nothing.
- "Reuse authentication from" with nameless connections. This picker is on
  Settings → Models' connection form (`ModelBackendPage.jsx`), not on Git.
  - Two nameless Claude connections (`walk-nameless-a`, `walk-nameless-b`)
    were seeded.
  - New connection → Agent type Claude → Continue. The Authentication
    method menu lists "Reuse authentication from walk-nameless-a" and
    "Reuse authentication from walk-nameless-b", named by id and
    distinguishable.
  - The Models list also names them by id.
- The no-git-connection notice was not re-walked. 3163's run covered it
  with a `/api/config` stub. This run changed nothing in that path.

### Capabilities (`/settings/capabilities`) — pass

- `/settings/capability-servers` lands on `/settings/capabilities`.
- The first `(i)` button, "About github", opens on focus. Enter and Space
  toggle it, and Escape closes it.
  - So Enter straight after focusing closes the popover that focus opened.
    This matches `InfoPopover.jsx`'s documented toggle, and
    `capability-info-popovers.spec.js` (1440 and 390 px) passes.
- The following passed in `capability-server-settings.spec.js` (real
  demo):
  - built-ins first, and disable-only
  - Disable flipping the state chip without a reload
  - configuring a throwaway server through its schema form
  - edit, rename and test in place
  - a write-only secret stored on the server and kept out of Other secrets
  - Remove listing holders, and cleanup
  - a holder link to a task
  - config-map's config-map and secret-map placements
- Host scripts (edit, test with argv, grant) passed in
  `host-script-settings.spec.js`.
- The host agent's version, adopted runs and allowlist suggestions appear
  under Settings → Models → CLI run locations (stubbed as above). It reads:
  - "The host agent is up at /run/grain/hostd.sock (build walk-build-1,
    host agent protocol 3), offering claude, host-script."
  - "The host agent is holding 2 processes … claude for run 7 of task 42:
    pid 4242, running, detached, adopted by this daemon" and "capability
    host-script: pid 4343, ended, waiting for a daemon to read how".
  - "Installed: build walk-build-2 (version …); the host agent runs version
    … until it next drains or restarts."
  - "Suggested allowlist entry: /usr/bin/walk-tool … grain never edits your
    allowlist."
- Two points about this stub:
  - My first stub omitted `hostAvailable`. The page then threw (`reason` of
    undefined) and the whole Settings view unmounted. The real server
    always sends both fields (`pkg/daemon/hostexec.go`), so this was a stub
    artifact, not filed.
  - My stub's version IDs carried a `sha256:` prefix, which the 12-character
    short form cuts down to `sha256:aaaaa`. The installer already strips
    that prefix and keeps 12 hex characters (`scripts/setup.sh`,
    `install_hostd_version`), so this is not a bug either.

### Capability policy and API scope sets — pass (committed specs)

These passed on the fixed build:

- `capability-policy-pickers.spec.js`: `denied` and `may_be_refused` in
  the pickers, with links back to `#capability-policy`
- `capability-policy-deep-link.spec.js`
- `api-scope-sets.spec.js`
- `api-scope-set-edit.spec.js`: create, edit and remove, including "a set
  a task holds asks first"

### Legacy capabilities (`/settings/credentials`) — pass

- The GitHub host field (with its "needs restart" plain-HTTP toggle), GCP
  project, service account email and count all render.
- This run's demo had no secret store (`/api/secrets` reports
  `enabled:false`), so I did not repeat the add, mask, replace and remove
  of a throwaway secret. 3163's run covered that with `-secrets-store`.
  `connection-credentials.spec.js` and `legacy-capabilities-gcp-help.spec.js`
  pass.

### Engine — pass

- Poll interval has the placeholder `10s`.
  - `20s` saved (200).
  - `never` was refused by the server and named: "Poll interval: time:
    invalid duration "never"". The box reverted to `20s`.
  - Blank saved `""`, which reads back as `10s`.
- Max worker agents:
  - `3` saved.
  - `0` was refused before any PUT: "Max worker agents must be at least 1;
    0 was not saved."
  - Blank saved 10.
- Max merges per repo branch:
  - `2` saved.
  - `-1` was refused before any PUT.
  - Blank saved 5.
- A reload read back `10s`, 10 and 5.
- The sandbox commitment note (stubbed) renders under Merge queue: "Committed
  if every slot is full: 10 workers + 5 mergers + 4 CI jobs × 2 vCPU = 38
  vCPU committed against the 16 this host has (2.4×), held against a 64
  vCPU sandbox budget … 76.0 GiB of memory, of the 64.0 GiB it has (119%),
  held against a 96.0 GiB sandbox budget — 64.0 GiB host, less 4.0 GiB
  kept back for the host itself (grain's own reserve), at 1.50× …".
  - The same note shows on Sandboxes.
  - 3163's run did not see it, which was a matter of how that stub was
    shaped. The page reads `/api/settings` as usual.

### CI — pass (labels checked; saves covered by 3163's run)

All 14 boxes render across CI queue, CI history, CI job cache and CI
artifacts: CI jobs per run, CI queue age boost, CI capacity reserved after,
CI runs kept, CI step output kept (MiB), CI step output age (days), CI
output per run (KiB), CI job cache size (MiB), CI job cache entries, CI job
cache age (days), CI artifacts kept (MiB), CI artifacts age (days), CI
artifacts per run (MiB) and Largest CI artifact (MiB). This run did not
repeat 3163's save and reload of each one. This branch changes nothing
there.

### Sandboxes — pass

- With the demo's real options, the backend select shows Granule, Host
  directories and None as disabled options, each with its reason ("—
  unavailable (needs -disable-native-sandboxes at daemon startup)" for
  None). The reason is also in the option's `title`.
- With None stubbed as the stored, available backend, the select shows
  "None (control plane only)". Its helper reads "This daemon runs grain's
  control plane only (settings, the inbox, the config repository) and
  creates no sandboxes …". The container option is disabled with its
  stubbed reason.
- Mirror hint (stubbed mirrored guest ref): "Treated as mirror repository
  mirror.walk.example/grain/sandbox, not booted; sandboxes boot
  mirror.walk.example/grain/sandbox:v1.2.0. Clearing it keeps that mirror
  as sandbox_image_mirror."
- These passed on the fixed build: `sandbox-disk-overcommit.spec.js`,
  `sandbox-disk-floor-blank.spec.js` (3163's fix),
  `custom-sandbox-image-layout.spec.js`, `k8s-priority-class-settings.spec.js`
  and `k8s-sandbox-settings-walkthrough.spec.js`. Between them they cover
  the overcommit bounds, the disk floor, the custom image contract and the
  Kubernetes prefilled defaults, validation and priority classes.

### Caches — pass (labels checked)

The proxy cache card (with its long helper) and the Actions cache card
render. This run did not repeat 3163's save and reload.

## Bug: a git connection's refusal banner outlives the save that fixes it

- **Reproduction:**
  1. On `/settings/git`, choose add connection.
  2. Enter host `ssh.banner.example`, Kind "Plain git", Auth type "SSH key"
     and private key `nope`.
  3. Choose Save connection. The form's alert and the shell's banner both
     say "the private key will not parse: ssh: no key found".
  4. Paste a real key and a known_hosts line, then choose Save connection.
     The row appears and the form's alert goes. The banner still reads
     "the private key will not parse" until its five-second timeout.
- **Cause:** `GitConnectionsList` cleared its own `actionError` on the next
  attempt but never took down the banner it had raised through
  `showError`. `useSettingsSection` does this for every other section
  through App's `dismissError`. Git never received `dismissError`.
- **Fix:** `SettingsPage` → `GitSection` → `GitConnections` now pass
  `dismissError`. The list remembers the message it raised, and takes it
  down when a save or remove succeeds or the form is cancelled. App's
  `dismissError` only clears a banner that still shows that message, so a
  later, unrelated error stays up.
- **Regression coverage:**
  - `ui/e2e/git-connection-refusal-banner.spec.js` (real demo and browser)
    fails on the unfixed build (banner still visible) and passes on the
    fixed one.
  - A `GitConnections.connections.test.jsx` case asserts that
    `dismissError` is called with the refused message after the next save
    succeeds.

## Minor observations, not filed

- With Kind "Plain git" and an SSH key, the form still offers "API token
  (optional)", and its helper begins "On a GitHub host, …". On a plain host
  the field is harmless but irrelevant.
- After a successful save, the previous connection's Test result alert
  stays on screen until the next Test. It is labelled with what it tested,
  so this was not filed.

## Tests run

- Committed Playwright specs on the fixed build, 35 passed (`--workers=4`):
  - api-scope-sets, api-scope-set-edit
  - capability-info-popovers, capability-policy-deep-link,
    capability-policy-pickers, capability-in-process-servers,
    capability-server-settings
  - connection-credentials, credential-reach-walkthrough,
    git-connections-multihost-walkthrough, git-connection-refusal-banner
    (new)
  - custom-sandbox-image-layout, host-script-settings,
    k8s-priority-class-settings, k8s-sandbox-settings-walkthrough,
    legacy-capabilities-gcp-help
  - sandbox-disk-overcommit, sandbox-disk-floor-blank
- Vitest: `src/components/settings/GitConnections*`, `GitSection*` and
  `SettingsPage*`, 63 passed.
