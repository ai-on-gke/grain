# Connections & Infrastructure Settings Walkthrough (Task 3616)

- **Date:** 2026-11-08
- **Commits tested:**
  - Git and Hosts: `21d92af1` (`Merge pull request #929 … task-2876-capabilities-remove-the-legacy`), walked in this task's first attempt. Nothing under `ui/src` or `pkg/ui` that Git or Hosts draws changed between it and `46d27f7e` (`git diff --stat 21d92af1 46d27f7e -- ui/src pkg/ui` only touches sessions, processes, task chat and System health). The committed Git and Hosts specs were run again on the fixed build (see Tests run below).
  - Capabilities, capability policy, API scope sets, Deployment credentials (the old Legacy capabilities), Engine, CI, Sandboxes and Caches: `46d27f7e` (`Merge pull request #951 …`), then this branch with the two fixes below.
- **Branch:** `grain/098073e4/task-3616-walk-through-settings-git`
- **File name:** `docs/test-runs/2026-11-08-connections-infrastructure-settings.md` (task 3163) and the `-task-3359.md` and `-task-3463.md` reports already exist for this date, so this one follows them as `-task-3616.md`.
- **Environment:**
  - Debian x86_64 sandbox VM, Go and Node from the image, Playwright Chromium from `/var/cache/playwright`. `/home/debian/.gemini-api-key` is not present, so no Gemini-backed tooling (`LOOK=1`) was used.
  - The real `grain demo` server (`go build ./cmd/grain`, which embeds the production bundle from `cd ui && npm ci && npm run build`) over a throwaway embedded store, started by `ui/e2e/fixtures.js` (`-secrets-store` for the capability and secrets walks, so typed secrets really reach the encrypted store).
  - Every step was driven through visible controls in headless Chromium (clicks, typing, `Tab`, `Enter`, `Space`, `Escape`, `ArrowDown`) by throwaway Playwright scripts (`ui/e2e/zz-walk-*.spec.js`), which are not committed. A listener on each page recorded console errors, uncaught page errors and every 5xx response. Intentional 4xx refusals were tallied apart.

## Stubs (intercepted routes)

Every write went to the real server and store. `page.route` was used only where the demo has no such system, and only on reads:

| Area | Route | What was changed, and why |
| --- | --- | --- |
| Git, read-only note | `GET /api/settings`, `GET /api/config` | `configRepoOnly: true` added to the real responses. A standalone demo is never config-repo-locked, and that lock is what draws Settings → Git read-only with its secret note. |
| Git, no-connection notice (attempt 1) | `GET /api/config` | Git connections emptied, because the demo seeds an ambient `github.com` connection. |
| Engine, sandbox commitment note | `GET /api/settings` | Added a full `sandboxCommitment` (4 workers + 1 merger + 2 CI jobs × 2 vCPU on an 8-CPU, 32 GiB host, level `warning`). The demo commits no sandboxes. |
| Sandboxes, None backend and mirror hint | `GET /api/settings` | `sandboxBackend: "none"`, `sandboxBackendOptions` with `none` available and `kubernetes` unavailable ("no in-cluster credentials (walk stub)"), plus `k8sGranuleImageMirror`, `k8sGranuleImageEffective` and `k8sGranuleImage`. The demo runs `container` and has no granule mirror. |
| Host process status | `GET /api/settings/cli-run` | `hostExecutor` set to a reachable `hostd` on a socket: build `v3.2.0-walk1` with `v3.2.0-walk2` installed, offers, an allowlist suggestion and one adopted process. The demo's real executor is `{"kind":"local"}`. |

The demo's own HTTP capability server (`https://capabilities.demo.grain.example/cloud`) and `grain capability host-script` (spawned from the built binary over a temp `-data-dir`) were used as real capability servers. Nothing was stubbed at the GitHub, git-host, secret-validation or Kubernetes boundaries: the demo validates keys, PEMs, proxy URLs, durations, Kubernetes names and image references itself.

## Bugs found and fixed

### 1. The retired Legacy capabilities links opened Capabilities six screens above their card

**Repro (on `21d92af1` and `46d27f7e`):** at 1440×900, open `/settings/credentials`, `/settings/legacy-capabilities` or `/settings/secrets`. You land on Settings → Capabilities scrolled to the top, and `#credentials` (Deployment credentials) is at `top: 5865px`, 6.5 viewports below the fold. With Settings locked to the config repo, Settings → Git's note "set write-only under **Settings → Capabilities**" (`href="/settings/capabilities#credentials"`) ran `onSection("capabilities")` on click. That cleared the anchor, so the URL became `/settings/capabilities` and the card sat at `top: 5962px`. `docs/capability-servers-ui.md` says the old address "opens it".

**Fix:**
- `settingsSections.js` gains `settingsAliasAnchor`/`settingsAliasAnchorAt`. `credentials` and `legacy-capabilities` map to `#credentials`, `secrets` to `#other-secrets`, and only when the address has no `#hash` of its own.
- `App.jsx` seeds `settingsAnchor` from the alias and writes it into the corrected address. `openSettings` applies the same rule. `onSection` takes an optional anchor, and choosing a capability server's page clears an anchor the section was opened at.
- `SettingsPage`'s `go(next, anchor)` passes the anchor on.
- `GitSection` asks for `("capabilities", "credentials")`.
- `design/spec/settings/settings.spec.md` now says the aliases open the card.

**Coverage:**
- `ui/src/settingsSections.test.js` (new), `GitSection.test.jsx` (the note's link).
- `ui/e2e/deployment-credentials-links.spec.js` (new). It covers the three aliases at desktop and phone, card in viewport and URL `/settings/capabilities#credentials` or `#other-secrets`; a `#hash` of its own still winning; and the Git note's click landing in view, with Back returning to Git. All three tests failed on the unfixed build (`viewport ratio 0`, and URL without `#credentials`) and pass with the fix.
- `sessions-worktree-pinned-nav-walkthrough.spec.js` now expects `/settings/capabilities#credentials` for the alias.

**Screens:** `make ui-screenshots`-style shots (`node e2e/screenshot.mjs --pages /settings/credentials,/settings/secrets`) at desktop and phone, light and dark, before and after. Before: the top of Capabilities (deprecation notices). After: the Deployment credentials heading at the top of the viewport (`top` 88px desktop, 73px phone), Other secrets in view for `/settings/secrets`, and 0px horizontal overflow at every width and theme.

### 2. The Deployment credentials secrets list went stale when capability servers changed

**Repro (on `46d27f7e`):** on `/settings/capabilities`, add an HTTP capability server, then configure its write-only `apiToken` with **Save server settings**. Scroll to Deployment credentials → Other secrets: "Used by capability servers" does not list the new `capability-server-…` secret until you reload. Then rename the server and **Remove** it. `GET /api/secrets` no longer has the secret, yet the list still shows `capability-server-3fabb7d7 · apiToken · Used by https://walk3616.example/caps/cloud-b`, linking to a server that is gone, until a reload.

**Fix:** `CapabilityServersPanel` calls a new `onChanged` after every successful add, configure, rename, bind or remove. `CapabilitiesSection` bumps a counter that reaches `SecretsPanel` (`refreshKey`) through `CredentialsSection`, and `SecretsPanel` reads `/api/secrets` again when it changes.

**Coverage:**
- `SecretsPanel.test.jsx`: refetches when `refreshKey` changes.
- `deployment-credentials-links.spec.js`: configure shows the secret, and remove drops it, without a reload; then reload. It failed on the unfixed build at the "configured" step and passes with the fix. Nothing changes visually.

## Coverage and outcomes

### Git (`/settings/git`), attempt 1
- Seeded `github.com` row and commit-author summary drew with no console errors.
- **Plain host `git.walk3616.example`:** GitHub App offered disabled (`aria-disabled="true"`, title "A GitHub App needs a GitHub host: a plain git host has no App API"). Saved a PAT and ran **Test**.
- **SSH key on `ssh.walk3616.example`:** an unparseable key was refused 400 with a banner. A generated Ed25519 key plus `known_hosts` saved and **Test** ran.
- **GitHub PAT, scope `walk3616-org`:** a proxy URL carrying credentials was refused, with the password shown as `xxxxx`. A bare proxy saved and tested.
- **GitHub App:** App ID, Installation ID, a 2048-bit RSA PEM, scope `walk3616-app/widgets` and a proxy. Saved and tested.
- All four rows survived a reload.
- **Remove:** activated with `Enter`. Dismissing the confirm kept the row; accepting removed it, and the removal persisted across a reload.
- Two nameless connections stayed distinguishable in Settings → Models → **Reuse authentication from** (`walk3616-nameless-1` and `-2`).
- The no-git-connection notice on `/repos` and in **+ New task** names tokens and SSH keys and links to `/settings/git`.
- 0px overflow at 390×844.

### Hosts (`/settings/hosts`, `/settings/hostds`), attempt 1
- Both paths open Hosts. **Invite hosts** shows the shared `--join` snippet with the `default` and `workstations` chips.
- **New code** `walk3616-pool`: a bad **Expires in** was refused; `48h`, max uses 5, default capacity 3, labels `arch=arm64,pool=gpu`, and principals `user:alice@example.com, group:infra@example.com` saved and survived a reload. **Rotate code** and **Revoke code** both worked.
- **Pending host requests:** `devbox-carol.example.com` shown as a verified enrollee with its `SHA256:` fingerprint and no pairing code. Name, labels and capacity were edited, then **Accept** made a key-bound row with no Rotate or Install. On a fresh store, **Reject** removed the request across a reload.
- **Legacy per-host token** registration (`walk3616-legacy`).
- Row actions:
  - **Edit** owner, labels and capacity.
  - **Rename**: `local` is refused, and the banner cleared on Cancel and on Save.
  - **Revoke**.
  - **Delete** sits behind a confirm: dismiss keeps the row, accept deletes it.
- **Delete all revoked** deleted the unreferenced hosts and listed `Skipped walk3616-referenced: host walk3616-referenced is still named by model connection "walk3616-pinned-backend" (hostd)…`.
- 0px phone overflow.

### Capabilities (`/settings/capabilities`)
- `/settings/capability-servers` opens Capabilities.
- Built-in servers are listed first. `figma`, `gchat` and `host-script` show *needs configuring*.
- Built-ins have **Disable** and no **Remove**. Disable flips the state chip to `disabled` without a reload.
- `(i)` popovers open on click, on `Enter` and on `Space`, and close on `Escape`.
- **Throwaway server `https://walk3616.example/caps/cloud-a`** (the demo server under a new ID):
  - Add form: **Test** answered "2 methods".
  - Schema form: `project` "Bad Project" was refused inline ("does not match"). `walk-3616` plus a write-only `apiToken` saved.
  - Its secret `capability-server-3fabb7d7` (key `apiToken`) is listed under **Used by capability servers**, with no Delete, never under Other secrets. `GET /api/secrets` never returns the value. The row's link opens the server's page.
  - **Test** in place and **Edit connection** (an extra header) worked.
  - Granted to a task, the server reads "granted by 1".
  - **Rename** to `…/cloud-b` through Edit connection opened "Rename this capability server?", listing the task's `…cloud-a#cloud-viewer` grant. After the rename the URL followed the new ID, the secret moved with the server (`capabilityServers: [cloud-b]`, apiToken still `set`), and the state went `attached`.
  - The holder link `data-holder="task:20"` opened `/tasks/20`.
  - **Remove** asked first: Cancel kept the server, Remove deleted it, and `GET /api/secrets` dropped the server's secret. The page's own list kept it until reload: **bug 2**, now fixed.
  - The state chip read `pending` right after the add and after configuring, and `attached` after the next re-read. That matches the panel's re-poll.
- **config-map server:**
  - **Add config map** `walk3616-cfg`: description, file `hello.txt`, content saved.
  - **Add secret map** `walk3616-sec`, key `token`, value typed into a `type="password"` box. The config holds `secrets: [{name: "walk3616-sec", keys: {token: {}}}]` and a `secret_values` `$secret:` reference, never the value.
  - After a reload the list reads "1 config map, 1 secret map … token: set".
  - Removing both maps and saving cleared them across a reload. The server keeps its `secret_values` secret, which it still references.
  - A new map arrives with one empty file row, and a second blank file blocks Save until it is filled or removed (Key is required).
- **Host scripts** (a throwaway server running `grain capability host-script`):
  - `timeout` is required and `0` is refused ("must be at least 1").
  - Saved `walk-hello` and edited its description in place; it survived a reload.
  - **test**, then two arguments `alpha beta` and `$(not-a-subshell)`, then **run walk-hello** printed `walk3616:alpha beta` and `walk3616:$(not-a-subshell)`: literal argv, no shell expansion.
  - **Remove** asked, then deleted the server.
- **Host process status** (stub above), on Settings → Models → Where each agent CLI runs:
  - "The host agent is up at /run/grain-hostd/hostd.sock (build v3.2.0-walk1, host agent protocol 4), offering cli claude, capability host-git".
  - "Installed: build v3.2.0-walk2 … runs version sha256:11111 until it next drains or restarts".
  - "Suggested allowlist entry: capability walk-custom-tool …".
  - "claude for run run-99 of task 42: pid 4242, running, detached, adopted by this daemon".
  - 0px overflow at 390×844.

### Capability policy and API scope sets
- **Rules added through the editor:**
  - **Deny** `…/cloud#cloud-viewer` for **Agent tasks**.
  - Allow `…/cloud#cloud-key` on `acme/gadgets`.
  - Saved, read back after a reload, and `#capability-policy` stays in view once the server list draws.
- **Pickers** (`data-policy-verdict`, each verdict linking to `/settings/capabilities#capability-policy`):
  - **+ New task** on `acme/widgets`: both `denied` and disabled, with the rule named.
  - **+ New task** with **No repo**: cloud-viewer `denied` ("denied by the capability policy for agent tasks"), cloud-key `may_be_refused` ("may be refused by the capability policy for agent tasks").
  - **Template** (`/processes/templates/template-2`) and **schedule** (`/processes/schedules/sched-2`): both `denied`.
  - **Task on `acme/gadgets`:** cloud-viewer `denied`, cloud-key admitted (no verdict).
  - **Repository defaults** (`/repos/acme/widgets/settings/agents`): both `denied`. On `acme/gadgets`, cloud-viewer is `denied` and cloud-key admitted.
  - **Repository CI grants:** `acme/widgets` cloud-key `denied`. `acme/gadgets` cloud-key `may_be_refused` "for ci jobs", by design: a CI job's trigger event is unknown, so it keeps the kind's verdict (`GrantsPicker.jsx`).
  - The New task verdict link (Tab-reachable from the open menu) opened `/settings/capabilities#capability-policy` with the card in view.
  - The policy was restored afterwards.
- **API scope sets:**
  - The name `Bad Name!` was refused: "must be lower-case letters, digits, '.', '-' and '_', in '/'-separated parts".
  - `walk3616/scopes` (`tasks:read, ci:read` @ `acme/widgets`) was created, then edited (description, scopes narrowed to `tasks:read`), and survived a reload.
  - With a task holding `api:walk3616/scopes@acme/widgets`, **Remove** said "1 grant holds api:walk3616/scopes …" and Cancel kept the set.
  - Once the grant was dropped, Remove said "Nothing grants api:walk3616/scopes." and deleted the set across a reload.

### Deployment credentials (the old Legacy capabilities, `/settings/credentials`)
- `/settings/credentials` now lands with the card in view (bug 1).
- **GitHub host:**
  - `https://ghe.walk3616.example` was refused: "a host name, not a URL … (plain http is the separate githubInsecureHttp switch)".
  - `ghe.walk3616.example:8443` saved.
- **GCP project** `walk-3616-proj` saved.
- **GCP service account email:** "not an email" was refused by name; a valid address saved.
- **GCP service account count:** `0` was refused client-side (min 1); `3` saved and read back after a reload; blank reset it to `1`.
- The values were restored afterwards.
- The deprecation notices (gcp-key, model proxies, gemini-key, release, grain-ci, bootstrap-playbooks, github-sandbox) render above the servers.
- **Other secrets:**
  - `walk3616-throwaway/token` was added through a `type="password"` Value box.
  - A second key `extra` was added, then `token` replaced in place. The listing shows only names and keys, never a value.
  - Deleting key `extra` asked "Delete walk3616-throwaway/extra? The stored value cannot be read back…": dismiss kept it, accept deleted it.
  - **Delete secret** asked "Delete the secret walk3616-throwaway and every key in it?…": dismiss kept it; accept, by keyboard `Enter`, deleted it across a reload.
- 0px phone overflow.

### Engine (`/settings/engine`)
- **Poll interval** shows `10s` with placeholder `10s`. `bad-dur` was refused ("invalid duration"), `0s` refused ("must be positive"), and `25s` saved.
- **Max worker agents:** `0` and `-1` were refused by name ("Max worker agents must be at least 1; 0 was not saved").
- **Max merges per repo branch:** `0` was refused the same way.
- `6` and `3` saved and read back after a reload. Blank reset all three to `10s`, `10` and `5`, after a reload too.
- `Enter` in a box saves.
- The commitment note (stub) renders under Max merges per repo branch: "4 workers + 1 merger + 2 CI jobs × 2 vCPU = 14 vCPU committed against the 8 this host has (1.8×). 7 × 4096 MiB = 28.0 GiB of memory, of the 32.0 GiB it has (88%) …".
- 0px phone overflow.

### CI (`/settings/ci`)
- All four cards render with defaults as placeholders (`1m0s`, `2m0s`, `5000`, `256`, `14`, `64`, …).
- Negative values were refused by name in CI queue, CI job cache, CI artifacts and CI output per run ("CI job cache size (MiB) must be at least 0; -5 was not saved"). Bad durations were refused ("ciAgeBoost: time: invalid duration").
- `CI runs kept`, `CI step output kept (MiB)` and `CI step output age (days)` accept any negative number. That is by design: `pkg/ui/settings.go` documents a negative as "no bound", and the help names `-1`.
- Non-default values for all 14 fields saved and read back after a reload in both the UI and `GET /api/settings`, including `-1` runs kept and `45s`, `2m0s` durations. Blank reset every field to `0` or `""`, the default.
- `/settings/ci#ci-artifacts` deep-links the card into view.
- 0px phone overflow.

### Sandboxes (`/settings/sandboxes`)
- **Backend** (`container`): backends that cannot run here are listed disabled with the reason, e.g. "Granule virtual machines — unavailable (needs SSH user/workspace/git proxy host for granule)" and "None (control plane only) — unavailable (needs -disable-native-sandboxes at daemon startup)".
- With the stub selecting `none`, the help reads "This daemon runs grain's control plane only … creates no sandboxes …", and Kubernetes is listed "unavailable (no in-cluster credentials (walk stub))".
- **Size:** vCPUs `3`, memory `6144` (`-1` refused by name) and disk `25` saved.
- **Network access** offers `open` (egress) and `none`.
- **Session sandbox idle TTL:** `nope` was refused, `45m` was saved as `45m0s`, and blank reset it.
- **Host memory reserve:** `-1` was refused; `1024` saved.
- **Sandbox budget, fixed ratios:** CPU `-1` was refused by name; CPU `3`, memory `1.5` and disk `2` saved.
- **Sandbox budget, auto bounds:**
  - CPU min `0` and `0.1` were refused ("Auto CPU minimum must be at least 0.25").
  - CPU min `8` was refused naming both bounds ("cpuMin 8.00 is above cpuMax 6.00").
  - Memory max `0.5` was refused likewise.
  - CPU pressure target `150` was refused ("cannot be more than 100").
  - Valid bounds (1.5–5×, 40%, 1.2–2.5×, 8%) saved and read back after a reload in mode `auto`.
- **Disk floor:**
  - Share `1.5` was refused ("cannot be more than 1") and `-0.1` refused; `0.2` saved.
  - Minimum `0` saved (no minimum), as did `8192`. Blank reset it to `5120`.
- **Kubernetes card:**
  - Prefilled defaults: namespace `grain`, pod images at the build's `v1.2.0`, workspace `/home/agent`, priority classes `grain-interactive`, `grain-agent` and `grain-ci`, deployment and container `grain`, `kubectl`. It carries 16 *needs restart* chips.
  - Refused by name:
    - `Bad_NS` ("not a Kubernetes namespace name")
    - node selector `pool` ("must be key=value")
    - `Bad Class` ("not a Kubernetes PriorityClass name")
    - `not an image ref` ("must be one OCI image reference")
  - Valid values saved and read back after a reload. Blank restored the defaults or "none".
- **Custom sandbox image:**
  - The card shows the `FROM docker.io/grainai/sandbox-k8s-base:v1.2.0` example and the contract text.
  - `::bad::` was refused.
  - `example.com/acme/sandbox:v1` saved, with the warning "Could not read the labels … contract is unverified. This grain drives images at dev.grain.sandbox.contract v2. Check it with `grain sandbox-image check …`". It survived a reload, and blank cleared it.
  - The mirror hint (stub) on the Kubernetes card: "Treated as mirror repository registry.walk3616.example/mirror/granule, not booted; sandboxes boot ghcr.io/ai-on-gke/granule-guest:v9 …".
- 0px phone overflow.

### Caches (`/settings/caches`)
- **Proxy cache max size (GiB):** `-3` was refused by name ("must be at least 0"); `15` saved.
- **Actions cache max size (GiB):** `-3` was refused; `12` saved; `1.5` refused ("takes a whole number").
- Both sizes read back after a reload.
- Both enable toggles flipped and persisted across a reload, then were restored.
- Blank reset both sizes to the default (`0`, placeholder shown).
- 0px phone overflow.

### Console, network, keyboard
- No console errors, uncaught exceptions or 5xx responses in any walk. The only failed requests were the intentional 400 refusals listed above.
- Every destructive action checked asked first: Git Remove, host Delete and Delete all revoked, capability server Remove and Rename, scope set Remove, and secret key and secret Delete.
- Keyboard:
  - `(i)` popovers open on `Enter`/`Space`.
  - Git Remove, Delete secret, and the Engine boxes respond to `Enter`.
  - In a grant menu, `ArrowDown` skips the disabled (denied) items, as MUI does, but `Tab` reaches their "Settings → Capabilities" links.
- Settled observations, not changed:
  - A refused field's banner stays up while other fields save, until that field is saved or left unchanged.
  - Typing `0` in a cache size whose stored value is `0` (default) writes nothing.

## Tests run
- **Vitest:**
  - `src/settingsSections.test.js`
  - `src/components/settings/GitSection.test.jsx`
  - `src/paths.test.js`
  - `src/App.overlays.test.jsx`
  - `src/components/SettingsPage.test.jsx`
  - `src/components/SecretsPanel.test.jsx`
  - `src/components/settings/CapabilitiesSection.test.jsx`
  - `src/components/CapabilityServersPanel.test.jsx`

  All pass.
- **Playwright on the fixed build:**
  - `deployment-credentials-links.spec.js` (new)
  - `settings-panel-walkthrough.spec.js`
  - `sessions-worktree-pinned-nav-walkthrough.spec.js`
  - `deployment-operations-pending-restart-field.spec.js`
  - `capability-policy-deep-link.spec.js`
  - `deployment-credentials-gcp-help.spec.js`
  - `capability-server-settings.spec.js`
  - `connection-credentials.spec.js`
  - `credential-reach-walkthrough.spec.js`
  - `git-connection-refusal-banner`, `git-connections-multihost-walkthrough`, `hostds-capacity-and-banner`, `hostds-delete`, `hostds-settings-layout`, `cli-run-locations-hostd`, `api-scope-set-edit`, `capability-policy-pickers`, `capability-info-popovers`, `capability-in-process-servers`, `host-script-settings`, `settings-config-modes-walkthrough` and `config-repo-only-settings`: 42 passed.

## Merge-queue repair (2026-11-08, later the same day)

Merging `main` into this branch conflicted in `ui/src/components/SettingsPage.jsx`.
`main` (cd41dd73, task 3615) had also taught `go()` to take an anchor, but
only scrolled to it; this branch passes it to `onSection` so the address
carries it as its `#hash`. The resolution keeps both: `go(next, anchor)`
asks before a dirty leave, hands the anchor to `onSection`, and also sets the
scroll target. `SettingsPage.test.jsx` now expects CLI's Sign in link to call
`onSection("agents", "external-agents")`. Vitest for `SettingsPage`,
`GitSection`, `settingsSections` and `SecretsPanel`, and Playwright's
`user-system-settings-walkthrough`, `deployment-credentials-links` and
`sessions-worktree-pinned-nav-walkthrough` (23 tests), pass on the merge.

CI's `go unit` then failed in `pkg/registry`, which nothing on this branch touches:
`TestWSMuxSessionRoundTripClosesStreams` reported "RoundTrip 4 failed: tunnel
mux: stream reset by peer". The cause is a race in the test. The peer handler
closes its stream only after writing the response. So the requester can read
that response and open the next stream before the peer frees a slot under its
`MaxStreams: 2` cap, and the peer resets the new stream. A 50ms sleep before
the handler's close reproduces it every time. The test now waits for each peer
handler to finish, and asserts `serverMux.ActiveStreams() == 0` after every
request. That checks what the test name says: RoundTrip's side closes its
stream on `Body.Close()` and on error. It passes 500 times under `-race` and
with the 50ms sleep. It fails if `streamClosingBody` stops closing the stream.

CI's `ui e2e` then failed one test of 1051:
`settings-config-modes-walkthrough.spec.js` ("a locked Deployment
credentials card dims config fields…"). `main` added it in c7a3e032, after
this branch was cut. It expected `/settings/credentials` to land on a bare
`/settings/capabilities`. On this branch that address deliberately keeps
`#credentials`, which is Bug 1's fix, so the test now expects
`/settings/capabilities#credentials`. That spec, `settings-panel-walkthrough`,
`deployment-credentials-links`, `deployment-operations-pending-restart-field`
and `sessions-worktree-pinned-nav-walkthrough` (37 tests) pass locally.
