# Connections and Infrastructure settings walkthrough

- **Task:** [Task #1905](/tasks/1905), branch `grain/049f1e09/task-1905` on `ai-on-gke/grain`
- **Date:** 2026-10-30, 12:26–13:20 UTC
- **Base tested:** `main` at `e14cb1ff`. Each fix was re-checked in the browser on a rebuilt server, ending at `cd108d81`.
- **Scope:** Settings → Connections (Git, Capabilities, Legacy capabilities, Executors) and
  Settings → Infrastructure (Engine, CI, Sandboxes, Caches). The settings layout, filter and save model,
  and the live sandbox, cache and CI status in System and on the CI page, belong to other walkthroughs.
- **Previous run:** [2026-10-29](2026-10-29-connections-infrastructure-settings.md). This run re-checked its fixes, and
  went past it on input validation and on what a save leaves on screen.

## Environment

- Debian sandbox (`kubernetes-granule`), Go 1.26, Node 20.19.0, Playwright Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Viewport 1400×1000, plus 390×844 for the screenshots.
- `grain` was built with `go build -tags grain_e2e ./cmd/grain`, after `npm ci && npm run build` in `ui/`.
- Server: `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file <file> -secrets-store -github-api <stub> -host-executor hostd`.
  Each start used a fresh throwaway SQLite store (`TMPDIR` pointed at a scratch directory). The no-git-connection notice was
  checked on a separate start with `-unconfigured`.
- The browser signed in with the demo's access code (`ui/e2e/accesscode.js` `signIn`), and the cookie went into
  the Playwright context. Everything after that went through visible controls: clicks, typing, Enter to commit,
  selecting options, keyboard focus plus Enter, and accepting or dismissing confirmations. `GET`s against the API
  were used only to check what a save had persisted. The ad-hoc Playwright scripts lived outside the tree, and
  they and their screenshots are not committed.
- There is no `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed tooling was used.

## Stubs

Only boundaries the demo has no real counterpart for were stubbed:

| Boundary | Stub | Routes it answered |
| --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js`, run standalone on loopback and passed as `-github-api` (plain HTTP) | `GET /rate_limit`, `/user`, `/user/repos`, `/repos/{owner}/{repo}`. Token `ghp_walkthrough_good` answers as `walk-bot` (scopes repo, workflow), with push on `acme/widgets` and read on `acme/lib`. Any other token gets a 401 |
| Git host behind a connection's proxy | a 20-line Node HTTP proxy on `127.0.0.1:4391` that records each `CONNECT` or absolute-form request and answers 502 | `CONNECT git.walk.test:443` |
| Secrets store | the demo's own `-secrets-store` (a real store in the throwaway directory) | not a stub, only enabled |
| Capability servers | the demo's seeded remote servers (`capabilities.demo.grain.example/{cloud,gcp}`) and grain's real built-ins (`grain capability config-map`, `host-script`, …), started beside the daemon | none intercepted |
| grain hostd | the demo's `-host-executor hostd` (a real hostd on a throwaway allowlist) | none intercepted |
| SSH host, Kubernetes API, GCP | not stubbed: `ssh.walk.test` does not resolve, the Kubernetes card only saves settings, and GCP calls were not made | none |

No browser route interception (`page.route`) was used anywhere.

## Steps and outcomes

### Git (`/settings/git`)

1. The table showed the seeded `github.com` PAT connection.
2. **Add connection** offers kind *GitHub* or *Plain git*, and auth *PAT*, *SSH key*, *GitHub App* or *Ambient*. Through the form I saved:
   - plain git `git.walk.test`: PAT, scope `team`, proxy `http://127.0.0.1:4391`;
   - plain git `ssh.walk.test`: SSH key (a throwaway ed25519 key) plus a known_hosts line, scope `team/app`;
   - GitHub `github.com`: PAT `ghp_walkthrough_good`, scope `acme`;
   - GitHub App: app 424242, installation 777, a throwaway RSA key, scope `acme/widgets`.

   Each row showed its kind, auth type, scope and "via proxy …", and read back the same after a reload and from `GET /api/deployment/git-connections`.
3. Refused saves:
   - a plain git host with *GitHub App*: "a GitHub App needs a GitHub host: a plain git host has no App API";
   - host `bad host!`: "is not a host name";
   - proxy `socks5://user:pw@…`: "must be http(s)://host:port".

   The form offers *GitHub App* for a plain git host even though the save refuses it (noted).
4. **Test**:
   - GitHub PAT: "Token accepted as @walk-bot by github.com · scopes: repo, workflow · ✓ acme/widgets read, push".
   - Seeded PAT: "GitHub rejected the token (bad credentials)".
   - GitHub App: "no installation token could be minted…", as expected with a fake key.
   - SSH key: "dial tcp: lookup ssh.walk.test … no such host ✗ team/app" (yesterday's bug 1 stays fixed).
   - Plain-git PAT scoped to the owner `team`: "nothing was tested … give the connection an owner/repo scope".
   - Plain-git PAT scoped to `team/app`: the proxy stub recorded `CONNECT git.walk.test:443` and the check reported "could not reach git.walk.test … Bad Gateway". Its `info/refs` probe really goes through the connection's proxy.
5. **Remove** asks first: "Remove the git.walk.test connection …? A credential stored for it is deleted with it and cannot be read back…".
   Dismissing it kept the row. Focusing Remove and pressing Enter raised it again, and accepting removed the row, which stayed gone after a reload.
6. No-git-connection notice (`-unconfigured`): Repos and the New task dialog both show "This deployment has no git connection yet…".
   The dialog's **Settings → Git** link opens `/settings/git`.
7. Commit author for the stub-accepted `acme` PAT still reads "asking github.com who credential … is: HTTP 401". The identity lookup
   goes through the demo's own GitHub transport, not `-github-api` (a demo wiring gap, noted).

### Capabilities (`/settings/capabilities`)

1. `/settings/capability-servers` redirects to `/settings/capabilities`. It lists 8 servers, built-ins first (config-map, gchat, gcp, git,
   host-script, model-proxy), then the two custom ones. Built-ins show *needs configuring* or *not attached*.
   Each server has its own page at `/settings/capabilities/<encoded ID>`, and a deep link reaches it directly.
2. **config-map**, through *Edit server settings*:
   - Added config map `Walk Settings` (file `walk.toml`, mode `999`, path override `relative/path`) and secret map
     `walk-secrets` (key `token`, a throwaway value).
   - **Save stored all of it**, and the server then reported needs configuring: `name "Walk Settings" does not match …`.
     This is **bug A**, now fixed: the save is refused with that message and nothing is stored.
   - Corrected to `walk-settings`, with mode and path blank: *Test server settings* answered 1 method whose `keys`
     allow `walk-settings/walk.toml` and `walk-secrets/token`. Saved, the state became *attached*.
   - After a reload the secret's chip read **set** and its box was empty. The value appeared in neither
     `GET /api/capability-servers` nor the page HTML. Replacing it saved, still *attached*.
3. **host-script**:
   - Two scripts both named `walk-echo` **saved**, leaving the server needing configuration ("configured twice"). This is the same bug A, now fixed: the save is refused.
   - `Walk Sleep!` was refused ("does not match ^[a-z0-9]…"), and so was timeout 100000 ("must be at most 86400").
   - With valid values it saved. The row reads "walk-echo · timeout 10s · ≤ 2 args".
   - **Test** with `world; rm -rf /` gave exit 3, stdout `hello world; rm -rf /` and stderr `oops`: the argument is passed exactly, not read by a shell.
   - Three arguments to a `max_args` 2 script gave "Not run: 3 arguments exceed walk-echo's limit of 2".
   - A `sleep 5` script with a 1s timeout gave exit 124 "timed out … its process group was killed".
   - Renamed in place to `walk-echo2` with a new body, it read back after a reload and ran `v2 0`.
   - Leaving an edited form raises the browser's unsaved-changes prompt.
4. **Custom server** `…/gcp`:
   - **Test** listed `gcp-key`.
   - After filing Task #20 with `…/gcp#gcp-key`, *Granted by* lists it. The entry is still not a link (noted yesterday).
   - Changing the Capability ID to `…/gcp-walk` opens "Rename this capability server? … These still grant its capabilities, and will be refused them until they are changed: Task #20 …".
   - Confirming moved the URL and the heading, the old deep link says "No capability server here is called …/gcp", and the new one's *Granted by* is empty.
5. **grain hostd**'s version, adopted runs and allowlist suggestions live on Settings → Models. Yesterday's run covered them, and
   `ui/e2e/cli-run-locations-hostd.spec.js` asserts them; I did not repeat that here.

### Capability policy

- **Add rule** → Deny `https://capabilities.demo.grain.example/cloud#cloud-viewer` for Agent tasks → **Save policy**.
  `GET /api/capability-policy` returns the rule, and it reads back after a reload with all 8 servers still listed (fe45fb04 holds).
- New task → **Add grant…** lists `cloud-viewer` with "the capability policy denies … (rule 1: deny …)".
  Processes → **+ new → template** → **Add grant…** lists it "denied by the capability policy for agent tasks". Yesterday's gap 6 is fixed (971dd9a9).
- **Remove rule** → **Save policy** left `{"policy":{}}`.

### Legacy capabilities (`/settings/credentials`)

- "23 of 28 configured. Still to configure: GCP key, Gemini key, GitHub sandbox, Registry push, Image signing", with 28 entries listed.
- Gemini key names the secret it uses: "Missing secrets: gcp-key-minter … stored as gcp-key-minter/value -- write-only, never shown or read back".
  It also says every new task is filed holding it. Its Test calls Google, so I did not press it.
- GCP project `walk-project-1905` and service account count 3 saved on Enter and read back after a reload, and I then put them back.
  Count 0 was refused by name: "GCP service account count must be at least 1; 0 was not saved."
- *Other secrets*:
  - Set `walk-throwaway`/`token`. The value box clears, and the value appears in the HTML neither before nor after a reload.
  - Setting it again replaced it, and it is still listed once.
  - **Delete secret** asks first: "Delete the secret walk-throwaway and every key in it? …". Dismissing kept it, and accepting deleted it: after a reload the card reads "No other secrets set."

### Executors (`/settings/executors`)

- **Issue token**: *Generate token* stays disabled for an empty name and for one of spaces.
- `walk-throwaway-executor` showed a 36-character `gxt_…` token once, with
  `grain model-server --token <token> --server <server-addr:port>`, and **Copy** put exactly the token on the clipboard.
  After a reload only `Prefix: gxt_22df…` remains, in neither the page text nor the HTML.
- **Revoke** opens "Are you sure you want to revoke token walk-throwaway-executor (Prefix: gxt_22df…)? … disconnected immediately".
  Cancel keeps it. *Revoke token* marks it "revoked · Revoked: 10/30/2026, 1:07:17 PM" and takes its button away, which persists across a reload.
  That time is in the browser's locale, not the app's time-zone preference (noted).
- "2 connected · **see System → Sandboxes**", reached by keyboard (focus plus Enter), goes to `/system/sandboxes`, whose model servers card lists them.

### Engine (`/settings/engine`)

- Poll interval:
  - `abc` was refused ("time: invalid duration") and `0s` was refused ("must be positive");
  - `15s` saved.
- Max worker agents −1 was refused by name, and 7 saved.
- Max merges per repo branch 0 was refused, and 2 saved.
- Merge batch size:
  - 0 and 21 were refused ("cannot be more than 20"), and 4 saved.
  - Blank saves and reads back blank with placeholder 3 (`mergeBatchSizeChosen 0`, `mergeBatchSize 3`).
- Merge gather window:
  - `soon` was refused, and `-1s` and `45s` saved.
  - Blank reads back blank with placeholder `5m0s`.
- After a refused value, saving a valid one into the same box saved it, and the status read "All changes saved",
  but **the refusal's banner stayed up** ("Poll interval must be positive"). This is **bug B**, now fixed.
- The batching and gathering notes need queue holds, which this demo does not seed. `MergeGatheringNote`'s unit tests cover them.

### CI (`/settings/ci`)

- All 14 fields start blank with grain's defaults as placeholders (2m0s, 1m0s, 5000, 64, 14, 256, 256, 4096, 30, 2048, 14, 128, 32),
  except CI jobs per run, where blank means no limit.
- `fast`, −2 and −5 were refused by name.
- New values on every card (3, 2m0s, 10m0s, 500, 2048, 14, 4096, 10240, 50000, 30, 4096, 7, 256, 64) saved on Enter and read back exactly
  after a reload. Clearing all 14 put them back to blank.
- Bug B showed here too: "CI jobs per run must be at least 0; -2 was not saved" stayed up after 3 saved.

### Sandboxes (`/settings/sandboxes`)

- Backend is a native select:
  - Granule VMs ("needs SSH user/workspace/git proxy host for granule"), Host directories ("needs -host-sandboxes at daemon startup")
    and Disabled ("needs -disable-native-sandboxes …") are `disabled` options, with the reason in the label and tooltip, and Playwright cannot select them.
  - Kubernetes saved and read back.
- Network `none` saved and read back. vCPUs 4 saved, memory 100 MiB was refused ("must be 0 (unset) or at least 128") and disk −1 was refused.
- Budget, with overcommit auto:
  - CPU minimum 0.1 was refused ("must be at least 0.25") and CPU pressure 150 was refused ("cannot be more than 100").
  - CPU minimum 8 over a maximum of 6 was refused: "Auto CPU minimum: … cpuMin 8.00 is above cpuMax 6.00" (yesterday's bug 4 stays fixed).
  - CPU maximum 1.0 under a minimum of 1.5, and memory maximum 0.5 under 1, were refused the same way.
  - Fixed mode: CPU overcommit −1 was refused, while 2.5, 1.25 and 3 saved. Everything read back after a reload.
- Disk floor:
  - share 1.5 was refused, and 0.15 saved;
  - a minimum of 4096 MiB saved;
  - host memory reserve −1 was refused.
- Kubernetes card:
  - It comes prefilled: namespace `grain`, images `ghcr.io/bwsalmon/grain/{k8s-sandbox,guest}:latest`, workspace `/home/agent`,
    Deployment and container `grain`, kubectl `kubectl`, and priority classes `grain-interactive`, `grain-agent` and `grain-ci`.
  - `Walk NS`, `Bad_Class` and node selector `novalue` were refused by name (yesterday's bug 7 stays fixed), and `pool=walk`, `walk-ci-low` and an empty agent class saved.
  - ServiceAccount `Bad SA` and git & model proxy host `not a host!` **saved without complaint**. This is **bug C**, now fixed, and the Service,
    Deployment and container names are checked the same way.
- Custom sandbox image: `not a reference!!` was refused, and `registry.walk.test/team/sandbox:v1` saved and read back.

### Caches (`/settings/caches`)

- Both toggles are on.
- Proxy cache max −3 was refused by name, while 40 GiB and an Actions cache of 0 and then 25 GiB saved, reading back 40 and 25 after a reload.
  Both placeholders read 20.

### Console and network

Across every page there were no `pageerror`s. The only console errors were the browser's own "Failed to load resource: 400" for each refused
save above, where the refusal is the expected answer. The only failed requests were `GET /api/tasks` polls aborted (`ERR_ABORTED`) by navigation.

## Bugs

| # | Bug | Reproduction | Outcome |
| --- | --- | --- | --- |
| A | A built-in capability server's settings were saved after only a JSON-schema check, so values its own configure refuses were stored and an attached server dropped to needs configuring | config-map: save a config map named `Walk Settings`, or mode `999`, or path `relative/path`. host-script: two scripts both named `walk-echo` | **Fixed** (`d0ec2cb8`): `intree.CheckBuiltinConfig` runs the built-in's parser (placeholders for secret values and `$env:` references), and `SetCapabilityServer` refuses with its message. Tests: `TestABuiltinsOwnConfigRulesRefuseTheSave` (`pkg/ui`), `TestCheckBuiltinConfigAppliesConfigureRulesWithoutSecrets` (`pkg/capability/intree`) |
| B | After a refused value, a valid save of the same setting left the refusal's banner up beside "All changes saved" | Engine → Poll interval `0s` (Enter), then `12s` (Enter) | **Fixed** (`ac4494d1`): `useSettingsSection`'s save dismisses the outstanding refusal of the setting it saved. Test: "takes a refusal's banner down once that setting saves" (`useSettingsSection.test.jsx`) |
| C | Settings → Sandboxes saved a Kubernetes ServiceAccount, Service, Deployment, container or proxy host that Kubernetes cannot use | Type `Bad SA` into Kubernetes sandbox ServiceAccount, or `not a host!` into git & model proxy host | **Fixed** (`cd108d81`): DNS-1123 checks and an address check in `UpdateSettings`, extending `TestUpdateSettingsRoundTripsKubernetesSettingsAndValidatesNodeSelector` |

Minor, noted and not filed:
- Add connection offers *GitHub App* for a plain git host, which the save then refuses.
- The ambient option reads "this host's GitHub login" for a plain git host too.
- The demo's commit-author lookup ignores `-github-api`.
- *Granted by* entries are not links.
- The executor command shows a literal `<server-addr:port>`.
- The executor revoked time uses the browser locale.
- The no-git-connection notice mentions only an ambient login for non-GitHub hosts, though PAT and SSH key work there too.
- The demo's seeded `ops/triage` scope set names `bwsalmon/grain`.
