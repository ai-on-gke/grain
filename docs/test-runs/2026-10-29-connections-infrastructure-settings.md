# Connections and Infrastructure settings walkthrough

- **Task:** [Task #1759](/tasks/1759), branch `grain/049f1e09/task-1759` on `ai-on-gke/grain`
- **Date:** 2026-10-29, 10:11–11:30 UTC (two runs: the first was parked by an exec-tunnel outage)
- **Base tested:** `main` at `9958172`. Fixes were re-checked in the browser on each new head, ending at `ebbfed47`.
- **Scope:** Settings → Connections (Git, Capabilities, Legacy capabilities, Executors) and
  Settings → Infrastructure (Engine, CI, Sandboxes, Caches). The settings layout, filter and
  save model, and the live status in System and on the CI page, belong to other walkthroughs.

## Environment

- Debian sandbox (`kubernetes-granule`), Go 1.26.10, Node 20.19.0, Playwright Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), viewport 1400×1000.
- `grain` built with `go build -tags grain_e2e ./cmd/grain`, and the frontend with `npm ci && npm run build` in `ui/`.
- Server: `grain demo -addr 127.0.0.1:0 -open=false -access-code-file <file> -secrets-store -github-api <stub>`,
  each run on a fresh throwaway SQLite store (`TMPDIR` pointed at a scratch directory). The no-git-connection notice
  was checked with `-unconfigured` added, and grain hostd with `-host-executor hostd`.
- The browser signed in with the demo's access code (`POST /api/auth/access-code`; the cookie was put into the
  Playwright context). Everything after that went through visible controls: clicks, typing, Enter/blur to commit,
  and selecting options. Ad-hoc Playwright scripts outside the tree drove it, and they and their screenshots
  are not committed.
- There is no `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed tooling was used.

## Stubs

Only boundaries the demo has no real counterpart for:

| Boundary | Stub | Routes it answered |
| --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js`, run standalone on loopback and passed as `-github-api` (plain HTTP) | `GET /rate_limit`, `GET /user`, `GET /user/repos`, `GET /repos/{owner}/{repo}`. Token `ghp_walkthrough_good` answers as `walk-bot` with push on `acme/widgets`, and any other token gets a 401 |
| Secrets store | the demo's own `-secrets-store` (a real store in the throwaway directory) | not a stub, only enabled |
| Capability servers | the demo's seeded remote servers (`capabilities.demo.grain.example/{cloud,gcp}`) and grain's real built-ins (`grain capability config-map`, `host-script`, …) started beside the daemon | none intercepted |
| grain hostd | the demo's `-host-executor hostd` (a real hostd on a throwaway allowlist offering `agy`) | none intercepted |
| Plain git host, SSH host, Kubernetes API | not stubbed: `git.walk.test` and `ssh.walk.test` do not resolve, and the Kubernetes card only saves settings | none |

No browser route interception (`page.route`) was used anywhere.

## Steps and outcomes

### Git (`/settings/git`)

1. The table showed the seeded `github.com` PAT connection (Default). Through **Add connection** I added four, each saved:
   - plain git `git.walk.test`: PAT, scope `team`, proxy `http://127.0.0.1:3128`;
   - plain git `ssh.walk.test`: SSH key plus a known_hosts line;
   - GitHub `github.com`: PAT `ghp_walkthrough_good`, scope `acme`;
   - GitHub App: app 424242, installation 777, a throwaway RSA key, scope `acme/widgets`.
   
   Kind, auth type, scope and "via proxy" all showed in the table and read back the same through `GET /api/deployment/git-connections` and after a reload.
2. **Test** on each:
   - GitHub PAT: "Token accepted as @walk-bot by github.com · scopes: repo, workflow · ✓ acme/widgets read, push" (stub).
   - GitHub App: "no installation token could be minted…", as expected with a fake key.
   - SSH key: said **"Token accepted by ssh.walk.test"** for a host that does not resolve. This is bug 1 (below), and is fixed.
   - Plain-git PAT: "could not reach GitHub: Get http://git.walk.test/rate_limit…". This is bug 5, filed as a follow-up.
3. **Remove** asks first: "Remove the git.walk.test connection …? A credential stored for it is deleted with it and
   cannot be read back…". Dismissing it kept the row, and accepting removed it. Keyboard focus plus Enter on Remove raised
   the same confirmation. After a reload only the seeded connection was left.
4. No-git-connection notice: on `-unconfigured` it showed **neither** on Repos nor in New task. This is bug 2, and is fixed. After the fix both show
   "This deployment has no git connection yet…", and its **Settings → Git** link opens `/settings/git` (clicked from the
   New task dialog).

### Capabilities (`/settings/capabilities`)

1. `/settings/capability-servers` redirects to `/settings/capabilities`. The list shows 8 servers with the built-ins first
   (config-map, gchat, gcp, git, host-script, model-proxy), then the two custom ones. Built-ins show "needs configuring" or "not attached"
   until configured. A built-in's *Check and manage* offers only **Test** and **Disable**, and a custom server also offers **Remove**.
2. Each server has its own page at `/settings/capabilities/<encoded ID>` and deep-links there. A short name
   (`/settings/capabilities/config-map`) answers "No capability server here is called config-map": the ID is the full URL (noted, not filed).
3. **config-map**, through *Edit server settings*:
   - Added a config map `walk-settings` (file `walk.toml`) and a secret map `walk-secrets` (key `token`, a throwaway value). *Test server settings* answered with 1 method whose `keys` allow `walk-settings/walk.toml` and `walk-secrets/token`.
   - Saved: the state went from needs configuring to **attached**.
   - After a reload the secret's chip read **set** and its box was empty. The value appeared in neither `GET /api/capability-servers` nor the page HTML. Replacing it saved.
   - Removing both maps and saving returned the server to needs configuring.
   - The needs-configuring alert told the user to "open Configure", a control the page lacks. This is bug 3, and is fixed.
4. **host-script**:
   - Added `walk-echo` with a script that prints `$1`, writes to stderr and exits 3. Timeout 0 was refused inline: "must be at least 1", and nothing was saved.
   - With timeout 10s it tested and saved, and the server became attached.
   - The per-script **Test** run with the argument `world; rm -rf /` gave `exit 3`, stdout "hello world; rm -rf /" (argv exactly, not shell-read) and stderr "oops".
   - Renamed it in place to `walk-echo2` and edited the body. It read back after a reload and its run printed `v2 0`.
   - The schema form shows raw regexes as placeholders (`^/`, `^[a-z0-9]…`). That is cosmetic and was noted, not filed.
5. **Rename a custom server**: changing the Capability ID of `…/gcp` to `…/gcp-walk` raised "Rename this capability
   server? … Nothing grants this server's capabilities." Confirming it moved the URL and the heading, the old deep link then
   said not found, and renaming it back restored it.
6. **Granted by**: after filing task #19 with `…/cloud#cloud-viewer`, that server's page reads "granted by 1 / Task #19
   'walk policy probe' on acme/widgets". The entry is not a link (noted).
7. **grain hostd** (the host process server, with `-host-executor hostd`): Settings → Models → *Where each agent CLI runs*
   reports "grain hostd is up at …/hostd.sock (build v0.0.0-…, hostd protocol 3), offering capability corp-search,
   cli agy". It names the missing allowlist entries for claude and codex and says which CLIs a daemon upgrade would interrupt
   rather than adopt. The *suggested allowlist entries* line was not reached here: it shows only for a chosen
   host location hostd cannot serve, which `ui/e2e/cli-run-locations-hostd.spec.js` covers.

### Capability policy

- **Add rule** → Deny on `https://capabilities.demo.grain.example/cloud#cloud-viewer` for Agent tasks → **Save policy**.
  `GET /api/capability-policy` returns the rule, and it reads back after a reload. Removing it and saving leaves "No rules".
- New task's **Add grant…** picker **still offered `cloud-viewer`** under the deny rule, with nothing marking it refused,
  and filing a task with that grant succeeded (201). The policy is enforced only at admission. This is gap 6, filed as a follow-up.

### Legacy capabilities (`/settings/credentials`)

- 28 of 28 capabilities are listed with their state. The deployment-wide card shows the GitHub host (the stub's address, with
  "speak plain HTTP" ticked, both marked needs restart) and the GCP fields.
- GCP project `walk-project-1759` and service account count 3 saved on Enter and read back after a reload, and I then put them back.
  Count 0 was refused by name: "GCP service account count must be at least 1; 0 was not saved."
- *Other secrets*:
  - Set `walk-throwaway`/`token`. It lists as a name and key only; the value box clears and the value never appears in the HTML, before or after a reload.
  - Setting the same name and key again replaced it.
  - **Delete secret** asks first: "Delete the secret walk-throwaway and every key in it? …". Dismissing kept it, accepting deleted it, and after a reload the card reads "No other secrets set."
- The unavailable-store notice cites `bwsalmon/agents#357`, a link into the retired tree (noted).

### Executors (`/settings/executors`)

- **Issue Token**: *Generate Token* is disabled while the name is empty. Issuing `walk-throwaway-executor` showed a 36-character
  `gxt_…` token once, with the `grain model-server --token …` line, and **Copy** put exactly that on the clipboard.
  After closing and reloading, only the prefix `gxt_e915…` remains and the token is in neither the page nor its HTML.
- **Revoke** opens "Revoke executor token … will be disconnected immediately". Cancel keeps it, and Revoke marks it Revoked
  with a time and takes the button away, which persists across a reload.
- "2 connected · **see System → Sandboxes**" goes to `/system/sandboxes`, by click and by keyboard (Enter), where the
  model servers card lists them. The card is titled "Executor join tokens" with a subheading "Model servers", left over from the rename (noted).

### Engine (`/settings/engine`)

- Poll interval `abc` was refused: "Poll interval: time: invalid duration "abc"". `15s` and then `10s` saved.
- Max worker agents −1 was refused by name, and 7 saved.
- Merge batch size:
  - 0 was refused, and 100000 refused with "cannot be more than 20". 4 and 2 saved.
  - Blank saves and reads back blank, with placeholder 3: `/api/settings` gives `mergeBatchSizeChosen 0`, `mergeBatchSize 3`.
- Merge gather window:
  - `soon` was refused. `45s` and `-1s` saved.
  - Blank reads back blank with placeholder `5m0s` (`mergeGatherWindowDefault`).
- The batching and gathering notes render only once the queue has holds. This demo has none, so the gathering note's
  wording was not seen live; `MergeGatheringNote`'s unit tests cover it.

### CI (`/settings/ci`)

- All 14 fields across the four cards start blank, with grain's defaults as placeholders: 2m0s, 1m0s, 5000, 64, 14, 256, 256, 4096, 30, 2048, 14, 128 and 32. CI jobs per run has no placeholder, because blank means no limit.
- CI jobs per run −2 was refused by name. Every card then took new values, saved on Enter, and after a reload
  read back exactly: 3, 2m0s, 10m0s, 500, 2048, 14, 4096, 10240, 50000, 30, 4096, 7, 256 and 64. The number boxes reject typed letters natively.

### Sandboxes (`/settings/sandboxes`)

- Backend: a native select. Granule VMs, Host directories and Disabled are `disabled` options whose label and tooltip
  give the reason ("needs -host-sandboxes at daemon startup", …), and selecting one fails. Container and both Kubernetes
  backends are selectable.
- Network: `none` saved and read back after a reload, and was then restored to `open`.
- Budget:
  - Overcommit mode fixed → auto saved. CPU overcommit −1 was refused, while 2.5, memory 1.25 and disk 3 saved.
  - Auto CPU minimum 0.1 was refused ("must be at least 0.25") and CPU pressure 150 was refused ("cannot be more than 100").
  - Auto CPU minimum 8, above the maximum of 6, was refused by the daemon but reported as **"Couldn't save Overcommit mode"**. This is bug 4, and is fixed: it now says "Couldn't save Auto CPU minimum / Auto CPU minimum: sandbox overcommit cpuMin 8.00 is above cpuMax 6.00".
  - Auto maximum 1.0 below a minimum of 1.5 is refused the same way. All the values read back after a reload.
- Disk floor: share 1.5 was refused ("cannot be more than 1"), and 0.15 and a minimum of 4096 MiB saved.
- Kubernetes card:
  - It comes prefilled with defaults marked "default" and needs restart: namespace `grain`, images `ghcr.io/bwsalmon/grain/{k8s-sandbox,guest}:latest` (nothing is published under ai-on-gke yet, per `tests.yml`), workspace `/home/agent`, Deployment and container `grain`, kubectl `kubectl`.
  - The priority classes are prefilled `grain-interactive`, `grain-agent` and `grain-ci`.
  - Node selector `novalue` was refused ("must be key=value").
  - The priority class `Bad_Class` and the namespace `Walk NS` **saved without complaint**. This is bug 7, and is fixed: both are refused by name now. `walk-ci-low` saved, and an empty agent priority class (none) saved and read back empty.
- Custom sandbox image:
  - The placeholder is the stamped default.
  - `not a reference!!` was refused ("must be one OCI image reference…").
  - `registry.walk.test/team/sandbox:v1` saved, with the notice that its contract labels could not be read on this host, and read back after a reload.

### Caches (`/settings/caches`)

- Both toggles are on (default). Proxy cache max −3 was refused by name, while 40 GiB and an Actions cache of 0 and then 25 GiB saved
  and read back 40 and 25 after a reload. Both placeholders read 20.

### Console and network

Across every page there were no `pageerror`s and no console errors other than the browser's own "Failed to load resource: 400" for
each refused save above (the refusal is the expected answer). The only failed requests were `GET /api/tasks` polls
aborted (`ERR_ABORTED`) by navigation.

## Bugs

| # | Bug | Reproduction | Outcome |
| --- | --- | --- | --- |
| 1 | Git → Test on an SSH key connection reports "Token accepted by <host>" when no repository is in reach, having dialled nothing | Add a plain git SSH key for an unresolvable host with no scope, then press Test | **Fixed** (`9e36a1c`): it reports "nothing was tested …" as an error. Regression test `TestSSHKeyCheckWithNothingToReadIsNotAccepted` (`pkg/ui`) |
| 2 | `grain demo -unconfigured` keeps the seeded `default` connection, so `hasGitCredential` stays true and the no-git-connection notice never shows | `grain demo -unconfigured`, then Repos / New task | **Fixed** (`cd0eea2`): the connection is dropped after seeding. `TestDemoScenariosRaiseTheSetupAndPauseBanners` asserts it |
| 3 | The needs-configuring reason says "open Configure", a control that does not exist | Any unconfigured built-in server's page | **Fixed** (`ba28828`): it names *Edit server settings*. Asserted in `TestABuiltinWaitingForConfigurationNeedsConfigAndAliasesResolve` |
| 4 | A daemon-refused auto overcommit bound is reported as "Couldn't save Overcommit mode" | Overcommit auto, Auto CPU minimum 8 (max 6) | **Fixed** (`5d974e5`): `save`/`saveChoice` take an `errorKey`. Tests in `useSettingsSection.test.jsx` and `SandboxesSection.test.jsx` |
| 5 | Git → Test on a plain-git PAT connection probes the host as GitHub (`/rate_limit`) instead of reading a repository | Add a plain git PAT connection, then press Test | **Follow-up proposed** (needs an HTTPS ls-remote probe through the git proxy's forwarder) |
| 6 | The capability picker offers, and filing accepts, a grant the capability policy denies; it is refused only at admission | Deny `…/cloud#cloud-viewer` for agent tasks, then New task → Add grant… | **Follow-up proposed** (the picker has to evaluate policy per repo and run kind) |
| 7 | Settings → Sandboxes saves Kubernetes namespace and PriorityClass names that Kubernetes refuses | Type `Bad_Class` into Priority class for CI jobs | **Fixed** (`ebbfed4`): DNS-1123 checks in `UpdateSettings`, extending `TestUpdateSettingsRoundTripsKubernetesSettingsAndValidatesNodeSelector` |

Minor wording and polish, not filed:
- short-name capability deep links;
- Granted by entries are not links;
- the Executors "Model servers" subheading;
- the `bwsalmon/agents#357` reference;
- raw regex placeholders in schema forms;
- a refused namespace leaves the box showing "custom" until a reload.
