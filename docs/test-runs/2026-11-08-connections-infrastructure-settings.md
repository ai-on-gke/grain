# Connections and infrastructure settings walkthrough — 2026-11-08

Task 3163: Settings → Git, Capabilities (with Capability policy and API
scope sets), Legacy capabilities, Engine, CI, Sandboxes and Caches. Every
step used visible controls in a real Chromium against a real `grain demo`.

## Commit and environment

- Commit tested: `d7dde0c2` (main, "Merge pull request #877"). The re-runs
  marked "fixed build" used the same tree plus this branch's fix.
- Sandbox: Debian, Go 1.26.10, Node 20.19.0, Playwright Chromium 1234
  (`/var/cache/playwright`). `npm ci && npm run build` in `ui/`.
  `ui/e2e/global-setup.js` built the `grain_e2e` binary.
- Server: `grain demo -secrets-store` over a throwaway embedded store, one
  per Playwright worker (`ui/e2e/fixtures.js`). No config repository.
- No `/home/debian/.gemini-api-key` in this sandbox, so this run used no
  Gemini-backed agent or screenshot description.
- I wrote throwaway Playwright specs (`ui/e2e/zz-walk-{dump,infra,git,caps,sec}.spec.js`)
  that log one PASS/FAIL or observation line per step. They are not
  committed. I also ran the committed specs for this area (listed under
  "Committed specs re-run"). Viewport: Playwright's default desktop
  1280×720. The committed layout specs cover phone widths.
- Browser console errors, `pageerror`s and every HTTP response ≥ 400 were
  recorded in each walk. The only ones were the expected 400s from saving
  a value the server refuses, listed below.

## Stubs and intercepted routes

| Route | Where | Why |
|---|---|---|
| `GET /api/config` with `deployment.hasGitCredential=false`, `gitCredential=""` and `gitConnections=[]` | Repos and New task | The demo always has a github.com connection, so this is the only way to show the no-git-connection notice |
| `GET /api/settings` with a synthetic `sandboxCommitment` | Engine → Merge queue | The demo reports no sandbox commitment, so the note never renders (see "Not verified") |

Nothing else was stubbed. Every git-connection Test went to the real
server's check. The GitHub App and PAT checks reached real github.com
from this sandbox and were refused, as throwaway credentials should be.
The capability server under test was the demo's own real one
(`pkg/demo/demo_capability_server.go`), added again under a throwaway
capability ID. Host scripts, Kubernetes settings and secret validation
ran against the real demo server. No Kubernetes cluster was contacted,
because these settings take effect only when the daemon restarts.

## Steps and outcomes

### Git (`/settings/git`) — pass

- The table lists `github.com · github · PAT · all repos` with Test and
  Remove. The Repositories card resolves `acme/widgets` to "Uses
  github.com default (PAT), the host's default". The Commit author card
  says the identity comes from the credential.
- Plain git host `git.walk.example`, Kind "Plain git": the Auth type menu
  shows GitHub App with `aria-disabled="true"` and the title "A GitHub
  App needs a GitHub host: a plain git host has no App API". PAT, SSH
  key and Ambient stay enabled.
- PAT on the plain host saved, and its row reads `git.walk.example · git
  · pat · all repos`. Test answers "nothing was tested: a credential on a
  plain git host is checked by reading a repository with it, and no
  repository on git.walk.example is in this check's reach -- give the
  connection an owner/repo scope …".
- Remove asks first: "Remove the git.walk.example connection
  grain-git-c-…? A credential stored for it is deleted with it and cannot
  be read back …". Dismissing keeps the row. Accepting removes it, and it
  stays gone after a reload.
- SSH key on `ssh.walk.example`: an unparseable key is refused inline
  ("the private key will not parse: ssh: no key found", 400). A real
  throwaway ed25519 key (`ssh-keygen`) with a known_hosts line saved as
  `ssh key`, and Test answers with the same "nothing was tested" reach
  message.
- GitHub PAT scoped `walk-org` with proxy: a proxy URL carrying a login
  is refused, with the password masked in the message ("proxy
  "http://user:xxxxx@proxy…" carries a login …"). The bare address saved,
  and the row shows `via proxy http://proxy.walk.example:3128` and
  `walk-org`.
- GitHub App scoped `walk-app-org/widgets` (App ID, installation ID and an
  RSA key) saved as `github app`. Test reports "no installation token could
  be minted from this GitHub App credential …".
- A reload shows all four rows. Removing each one (after the confirmation)
  leaves only the github.com default.
- No-git-connection notice (stubbed): Repos and the New task dialog both
  say "This deployment has no git connection yet … Add a git connection
  first, in Settings → Git: a GitHub token or GitHub App for github.com, or
  a token, SSH key or ambient login for another git host". Both link to
  `/settings/git`.
- "Reuse authentication from" with nameless sources is in the Models
  backend form (`ModelBackendPage.jsx`), not on Git. Its unit test
  (`ModelBackendPage.test.jsx`) covers it. This walk did not drive it.

### Capabilities (`/settings/capabilities`) — pass

- `/settings/capability-servers` lands on `/settings/capabilities`.
- The list has 12 servers: the 11 `https://grain.dev/capabilities/*`
  built-ins first, then the demo's custom `…/cloud`. figma, gchat and
  host-script read "needs configuring".
- Throwaway `https://capabilities.demo.grain.example/cloud-walk` (HTTP, the
  demo server's URL and bearer header) was added. It reads `pending` and
  shows "Saved; grain connects to it on its next pass".
- Disable flipped the card's state chip to `disabled` with no reload.
  Enable put it back to `pending`.
- The first `(i)` info popover opens on Space, on Enter and on click, and
  Escape closes it.
- Remove opens a dialog, "Remove this capability server? … will be
  detached. Nothing grants this server's capabilities." The server is
  gone after a reload. `/api/secrets` holds no `capability-server-*`
  secret afterwards, and Legacy capabilities never listed one.
- Committed specs re-run in the same session (all pass):
  - `capability-server-settings.spec.js`: schema-form configure, a refused
    field inline, a write-only secret stored as `$secret:capability-server-…`
    and absent from Other secrets, edit and rename in place, test, a
    holder link to the task, a policy denial, and Remove listing holders.
    Also built-ins first and disable-only, config-map's config-map and
    secret-map placements, and the managed data-plane token.
  - `capability-in-process-servers.spec.js`.
  - `capability-info-popovers.spec.js` at 1440 and 390 px.
  - `host-script-settings.spec.js`: define, edit, test with argv, grant.
- Host process server's version, adopted runs and allowlist suggestions:
  only the committed host-script spec exercised these. The demo runs no
  host here.

### Capability policy and API scope sets — pass (committed specs)

These ran in the same session: `capability-policy-pickers.spec.js`
(`denied` and `may_be_refused` verdicts in pickers, repo-conditional
rules, repo CI grants, and links back to `#capability-policy`),
`capability-policy-deep-link.spec.js` (desktop and phone),
`api-scope-sets.spec.js`, and `api-scope-set-edit.spec.js` (create, edit,
remove, and "removing a scope set a task holds asks first, and Cancel
keeps it"). In the walk itself, the policy card reads "No rules: every
trusted run may." and the scope-set card lists the built-in `grain/*` sets.

### Legacy capabilities (`/settings/credentials`) — pass

- The GitHub host field, GCP project `grain-sandbox-dev`, service account
  email and count all render. `legacy-capabilities-gcp-help.spec.js`
  passes.
- Throwaway secret `walk-throwaway/token`: the value box is
  `type="password"`, and Set secret sends
  `PUT /api/secrets/walk-throwaway/token`. The row shows the name and key
  only ("write-only -- never shown or read back") and is still there after
  a reload. Setting it again with a new value replaced it without the
  value showing anywhere on the page. Delete secret asks first ("Delete
  the secret walk-throwaway and every key in it? …"), and `/api/secrets`
  is empty afterwards.

### Engine — pass, with one gap noted

- Poll interval: placeholder `10s`, helper "Blank uses grain's default".
  `25s` saves. `banana`, `0s` and `-5s` are refused by name ("Poll
  interval: time: invalid duration "banana"" and "Poll interval must be
  positive"), and the box reverts. Blank saves `""`, which reads back as
  `10s`.
- Max worker agents: `7` saves. `-1` is refused before any PUT ("Max
  worker agents must be at least 1; -1 was not saved."). Blank resets to
  10. `100000` is accepted, since the server has no upper bound.
- Max merges per repo branch: `3` saves. `0` and `-2` are refused by name.
  Blank resets to 5.
- A reload reads every value back.

### CI — pass

All 14 boxes across the CI queue, CI history, CI job cache and CI
artifacts cards saved and read back after a reload. The server
normalises durations (`90s` → `1m30s`, `2m` → `2m0s`). `soon` for CI queue
age boost is refused by name. `-1` for run history is accepted, as its
helper documents ("-1 keeps every run"). Blanking every box restored the
original stored values.

### Sandboxes — one bug found and fixed

- Backend shows "Granule virtual machines — unavailable (needs SSH
  user/workspace/git proxy host for granule)", "Host directories (no
  isolation) — unavailable (needs -host-sandboxes …)" and "None (control
  plane only) — unavailable (needs -disable-native-sandboxes at daemon
  startup)". All three are `disabled` options. The demo cannot start with
  `-disable-native-sandboxes`, so the None description that shows once
  None is selected was not seen.
- Size: vCPUs `4` saves, `-1` is refused by name, `0` and blank clear it.
  Memory `10` is refused ("must be 0 (unset) or at least 128"). Disk saves
  and clears.
- Network `none` survives a reload. The session sandbox idle TTL saves
  `45m` (reads back `45m0s`), refuses `nope`, and blank clears it.
- Budget, fixed mode: the CPU, memory and disk ratios save, and negatives
  are refused by name.
- Budget, auto bounds:
  - A minimum above its maximum is refused naming both ("Auto CPU
    minimum: sandbox overcommit cpuMin 10.00 is above cpuMax 6.00"), and
    so is a maximum below its minimum.
  - A CPU minimum of 0 is refused before any PUT ("at least 0.25").
  - A CPU pressure target of 150 is refused ("cannot be more than 100").
  - In auto mode, cpuMin 9 against max 6 is refused too.
  - Auto mode survives a reload.
- Host memory reserve: `4096` saves, `-1` is refused, and blank saves −1
  (the default).
- Disk floor share: `1.5` and `-0.1` are refused, and `0.2` and `0.1`
  save.
- **Bug: blanking Disk floor minimum (MiB) saved 0.** The box shows
  grain's default `5120` as its placeholder, the same convention as every
  blank-means-default box, but blank sent `{"sandboxDiskFloorMinMb":0}`.
  On the server, 0 means "no minimum" (`pkg/ui/settings.go`, `DiskFloor`
  in `pkg/model/disk.go`), so clearing the box turned the 5 GiB minimum
  off. The box then read `0`.
  - Reproduction: on `/settings/sandboxes`, type `2048` in Disk floor
    minimum (MiB), Tab, clear it, Tab. `/api/settings` then reads
    `sandboxDiskFloorMinMb: 0`.
  - Fix: `SandboxesSection.jsx` now saves the reported default
    (`sandboxDiskFloorMinMbDefault`, falling back to 5120) for a blank
    box. Typing `0` still means no minimum.
  - Regressions: `ui/e2e/sandbox-disk-floor-blank.spec.js` (real demo,
    asserts the PUT body, the stored value and the value after reload) and
    a `SandboxesSection.test.jsx` case. Both pass on the fixed build.
- Kubernetes card:
  - Prefilled defaults: namespace `grain`, images
    `docker.io/grainai/sandbox-k8s:v1.2.0` and `…/sandbox:v1.2.0`,
    workspace `/home/agent`, priority classes `grain-interactive`,
    `grain-agent` and `grain-ci`, deployment and container `grain`,
    kubectl `kubectl`. Each field is marked "needs restart".
  - Validation by name: `Bad_NS` is "not a Kubernetes namespace name",
    the node selector `pool` "must be key=value", `Bad Class` is "not a
    Kubernetes PriorityClass name", and `not an image ref` is "not one
    OCI image reference".
  - Blank restores the default.
  - `k8s-priority-class-settings.spec.js` and
    `k8s-sandbox-settings-walkthrough.spec.js` pass.
- The custom sandbox image card shows the base image `FROM` snippet,
  "dev.grain.sandbox.contract = 2 (this grain drives images at v2)", the
  rebuild-only-when-the-contract-falls-behind hint and the
  `docs/sandbox-image-contract.md` pointer.
  - `example.com/acme/sandbox:v1` saves, with a warning that its labels
    could not be read so the contract is unverified. `::bad::` is
    refused. Blank clears it.
  - `custom-sandbox-image-layout.spec.js` and
    `sandbox-disk-overcommit.spec.js` pass.
  - The mirror hint was not checked separately.

### Caches — pass

The proxy cache max `15` and the Actions cache max `12` saved and read
back after a reload. `-3` is refused by name, and `0` is accepted. The
Actions cache toggle survives a reload. Blank clears both (stored 0,
which is grain's default).

## Not verified, and minor observations

- Merge queue sandbox commitment note: `SandboxCommitmentNote` renders
  only when `/api/settings` carries `sandboxCommitment`, and the demo sends
  none. My `GET /api/settings` stub did not make the note appear on a
  reload. The settings page probably reads them through a URL the
  `**/api/settings` glob does not match. The note's own unit tests cover
  its text.
- A refusal alert stays on screen after the next field saves successfully.
  For example, "Proxy cache max size … -3 was not saved" was still showing
  after the Actions cache max saved. That is cosmetic, so it was not
  filed.

## Committed specs re-run

31 passed (`--workers=3`): api-scope-sets, api-scope-set-edit,
capability-info-popovers, capability-policy-deep-link,
capability-policy-pickers, capability-in-process-servers,
capability-server-settings, connection-credentials,
custom-sandbox-image-layout, host-script-settings,
k8s-priority-class-settings, k8s-sandbox-settings-walkthrough,
legacy-capabilities-gcp-help and sandbox-disk-overcommit. The new sandbox-disk-floor-blank spec also passed.
git-connections-multihost-walkthrough was not re-run.
