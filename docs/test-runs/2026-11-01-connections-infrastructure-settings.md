# Connections and Infrastructure settings walkthrough

- **Task:** [Task #2090](/tasks/2090), branch `grain/049f1e09/task-2090` on `ai-on-gke/grain`
- **Date:** 2026-11-01, 03:36–04:35 UTC
- **Base tested:** `main` at `20d79d0b`. Each fix was re-checked in the browser on a rebuilt server, ending at `6ccb8d73`.
- **Scope:** Settings' Connections sections (Git, Capabilities, Legacy capabilities, Executors) and
  Infrastructure sections (Engine, CI, Sandboxes, Caches). The settings layout, filter and save model belong to the
  settings walkthrough, and the live sandbox, cache and CI status to the operations walkthrough. The nav now groups these
  sections differently: Executors and Sandboxes are under *Models & sandboxes*, Legacy capabilities under *Secrets & access*,
  Caches and Engine under *System / Operations*, and Git, Capabilities and CI under *Managed in config repo*.
- **Previous run:** [2026-10-30](2026-10-30-connections-infrastructure-settings.md). Its fixes A–C held. This run went further
  on the Repositories picker, the capability policy and pickers, Other secrets, the merge gathering note and the
  Kubernetes card.

## Environment

- Debian sandbox, Go 1.26.10, Node 20.19.0, Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
  Viewport 1400×1000 for the walk, plus `make ui-screenshots` at desktop and phone widths, light and dark.
- Build: `npm ci && npm run build` in `ui/`, then `go build -tags grain_e2e ./cmd/grain`.
- Server: `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file <file> -secrets-store -github-api <stub> -host-executor hostd`.
  Each start used a fresh throwaway SQLite store (`TMPDIR` set to a scratch directory). The no-git-connection notice was checked on a
  separate `-unconfigured` start.
- The browser signed in with the demo's access code (`ui/e2e/accesscode.js` `signIn`), and the cookie went into the Playwright
  context. Every step after that went through visible controls: typing and Enter to commit, clicking, selecting options,
  focusing a control and pressing Enter, and accepting or dismissing confirmations. API `GET`s served only to check what a save had persisted.
  The ad-hoc Playwright scripts lived outside the tree. They and the screenshots are not committed.
- There is no `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed tooling was used.

## Stubs and seeds

| Boundary | Stub | Routes it answered |
| --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js`, run standalone on loopback and passed as `-github-api` (plain HTTP) | `GET /rate_limit`, `/user`, `/user/repos`, `/repos/{owner}/{repo}`. Token `ghp_walkthrough_good` answers as `walk-bot` (scopes repo, workflow), with push on `acme/widgets` and read on `acme/lib`. Every other token gets a 401 |
| Git host behind a connection's proxy | a small Node HTTP proxy on `127.0.0.1:4391` that logs each request and answers 502 | `CONNECT git.walk.test:443` (logged twice: the Test press, then reach) |
| Merge gather history | three rows put straight into the throwaway store's `merge_gather_history` (one each batched, released and expired), because the demo's queue never holds a change | none: a data seed, so the Engine card's gathering note has something to show |
| Secrets store | the demo's own `-secrets-store` | not a stub, only enabled |
| Capability servers | the demo's seeded remote servers (`capabilities.demo.grain.example/{cloud,gcp}`) and grain's real built-ins (`grain capability config-map`, `host-script`, …) beside the daemon | none intercepted |
| grain hostd | the demo's `-host-executor hostd` (a real hostd on a throwaway allowlist offering `agy`) | none intercepted |
| SSH host, Kubernetes API, GCP | not stubbed: `ssh.walk.test` does not resolve, the Kubernetes card only saves settings, and no GCP call was made | none |

No browser route interception (`page.route`) was used anywhere. My first stub run passed `scopes` as a string, which
crashed the stub on its first GitHub call. Those Test results were discarded and redone on a fresh store with the stub fixed.

## Steps and outcomes

### Git (`/settings/git`)

1. The table showed the seeded `github.com` PAT connection.
2. **Add connection** offers kind *GitHub* or *Plain git*, and auth *PAT*, *SSH key*, *GitHub App* or *Ambient*. I saved four connections through the form:
   - plain git `git.walk.test`: PAT, scope `team/app`, proxy `http://127.0.0.1:4391`;
   - plain git `ssh.walk.test`: SSH key (a throwaway ed25519 key) plus a known_hosts line, scope `team`;
   - GitHub `github.com`: PAT `ghp_walkthrough_good`, scope `acme`;
   - GitHub App: app 424242, installation 777, a throwaway RSA key, scope `acme/widgets`.

   Every row showed its kind, auth type, scope and "via proxy …", and read back the same after a reload. No token or key appeared in the page HTML.
3. Refused saves, each leaving the form open with the message:
   - a plain git host with *GitHub App*: "a GitHub App needs a GitHub host";
   - `bad host!`: "is not a host name";
   - proxy `socks5://user:pw@…`: "must be http(s)://host:port";
   - SSH key `not a key`: "the private key will not parse";
   - a second `github.com` connection for scope `acme`: "already has a git connection … remove it or edit its scope first".
4. **Test** results:
   - Seeded PAT: "GitHub rejected the token (bad credentials)".
   - `acme` PAT: "Token accepted as @walk-bot by github.com · scopes: repo, workflow · ✓ acme/widgets read, push".
   - App: "no installation token could be minted…", as expected with a fake key.
   - SSH key scoped to the owner `team`: "nothing was tested … give the connection an owner/repo scope".
   - `git.walk.test`: "could not reach git.walk.test … Bad Gateway", and the proxy stub logged `CONNECT git.walk.test:443`.
5. **Repositories** card: picking "acme on github.com (PAT)" for `acme/widgets` **silently replaced the GitHub App connection
   scoped to acme/widgets** and orphaned its stored key. This is **bug 3**, fixed: the pick is now refused by name, and the App row and the picker's value stay as they were.
6. **Remove** asks first: "Remove the git.walk.test connection …? A credential stored for it is deleted with it and cannot be read back…".
   Dismissing it kept the row. Focusing Remove and pressing Enter raised it again, and accepting removed the row, which stayed gone after a reload.
7. No-git-connection notice (`-unconfigured`): Repos and New task both say "This deployment has no git connection yet…". The New task
   dialog's **Settings → Git** link, focused and opened with Enter, goes to `/settings/git`. The Repos notice links to Settings → Git too.
8. Commit author still reads "asking github.com who credential … is: HTTP 401" even for the stub-accepted `acme` PAT. The demo's
   identity lookup does not go through `-github-api` (still a wiring gap, noted on 10-30).

### Capabilities (`/settings/capabilities`)

1. `/settings/capability-servers` redirects to `/settings/capabilities`. 8 servers are listed, built-ins first (config-map, gchat, gcp, git,
   host-script, model-proxy), then the two custom ones. Built-ins read *needs configuring* or *not attached*, and every server has its own
   page at `/settings/capabilities/<encoded ID>` (pages, not tabs), which a deep link reaches directly.
   A built-in offers only Disable/Enable: Disable then reload read *disabled*, with Enable and no Remove, and Enable brought it back
   *attached*. A custom server also has Remove.
2. **config-map**, through *Edit server settings* (the schema form):
   - config map `Walk Settings` with file `walk.toml` and secret map `walk-secrets`/`token`: the save was refused, `name "Walk Settings" does not match …`.
   - Renamed to `walk-settings`, mode `999`: *Test server settings* answered "mode "999" must be a four-digit octal mode such as 0644". The message repeats "configure: configure:", which is cosmetic.
   - Mode `0644`: Test answered 1 method allowing `walk-settings/walk.toml` and `walk-secrets/token`. Saved, the server went *attached*, listing placements `/home/debian/.config-map/walk-settings` and `…/walk-secrets` with `token: set`.
   - After a reload the secret box was empty, and the value was in neither the page HTML nor `GET /api/capability-servers`. A replacement saved, and the server stayed *attached*.
3. **host-script**:
   - Two scripts both named `walk-echo` were refused ("configured twice"). `Walk Sleep!` and timeout `100000` were refused inline on the field (`aria-invalid`).
   - The valid pair saved, and the server went *attached* with method `host-script` (`script`: "walk-echo", "walk-sleep").
   - Test `walk-echo` with `world; rm -rf /` gave exit 3, stdout `hello world; rm -rf /` and stderr `oops`: the argument is passed exactly.
   - Three arguments gave "Not run: 3 arguments exceed walk-echo's limit of 2". `sleep 5` with a 1s timeout gave exit 124, "timed out … its process group was killed".
   - Renamed in place to `walk-nap` with a new body: after a reload the method listed `walk-nap`, and Test printed `nap 0`.
4. **Host process server** (Settings → Models, *Where each agent CLI runs*): "grain hostd is up at …/hostd.sock (build
   v0.0.0-20261101042503-6ccb8d73b97c, hostd protocol 3), offering capability corp-search, cli agy". claude and codex read
   "Host is unavailable … its allowlist offers no cli entry", and agy runs on the host through hostd. The demo's hostd has no
   pending upgrade, adopted runs or allowlist suggestions, so those notes did not render. `CLIRunLocations.test.jsx` covers them.
5. **Granted by**: a task filed from New task with `config-map server › config-map` showed on the server's page as "Task #20 “walk
   throwaway grant holder” on acme/widgets · …#config-map={}", and the list row read "granted by 1". The entry is still plain text, not a link (noted).
6. **Capability policy**:
   - Add rule *Deny* `https://grain.dev/capabilities/host-script#host-script` for agent tasks. Save policy, reload: the rule read back, and `GET /api/capability-policy` held it.
   - New task → *add grant…* listed `host-script server › host-script` disabled (`aria-disabled`), naming the rule:
     "the capability policy denies … to this task on acme/widgets (rule 1 …)".
   - Removing the rule and saving read "No rules: every trusted run may." after a reload, and the same item became pickable.
   - A new template's (Processes → + new → template) *add grant…* offers both configured built-ins.

### Legacy capabilities (`/settings/credentials`)

- Saved and read back after a reload: GCP project `walk-project-2090`, service account count 3, and email
  `walk-minter@walk-project-2090.iam.gserviceaccount.com`.
- Refused: count 0 by name, and GitHub host `https://github.com` ("a host name, not a URL").
  `ghe.walk.test:8443` saved (needs restart) and I put the stub's host back.
- Email `not an email` **saved without complaint** (**bug 5**, fixed: refused by name).
- *Other secrets*: Set `walk-throwaway`/`token`. The value box clears, and the value is not in the HTML after a reload.
  Setting it again replaced it, and it is still listed once.
  **Delete secret** asks first, naming the secret, and dismissing kept it.
- The list also showed `capability-server-1f6d92c4`, the config-map server's own secret. Its Delete removed `walk-secrets/token`
  behind a confirmation that never says the secret belongs to a server (**bug 4**, fixed: capability servers' secrets are claimed and left
  out; after the fix the card reads "No other secrets set." while config-map holds its secret).

### Executors (`/settings/executors`)

- **Issue token**: *Generate token* stays disabled for an empty name and for one of spaces.
- `walk-throwaway-executor` showed a 36-character token once, with `grain model-server --token <token> --server <server-addr:port>`.
  **Copy** put exactly the token on the clipboard.
  After a reload only `Prefix: gxt_f851…` remains, and the token is nowhere in the HTML.
- **Revoke** opens "Are you sure you want to revoke token walk-throwaway-executor (Prefix: gxt_f851…)? … disconnected immediately".
  Cancel kept it. Focusing Revoke, pressing Enter and confirming marked it "revoked · Revoked: …" with no Revoke button, which persisted across a reload.
- "2 connected · **see System → Sandboxes**": focused and opened with Enter, it goes to `/system/sandboxes`, whose model servers card lists both.
  It is a `<button>`, not a link, so it cannot be opened in a new tab (noted).

### Engine (`/settings/engine`)

- Refused by name, each with nothing saved:
  - poll interval `abc` and `0s`;
  - max worker agents −1;
  - max merges per repo branch 0;
  - merge batch size 0, 21 ("cannot be more than 20") and 1.5;
  - gather window `soon`.
- Saved: `15s`, 7, 2, batch 4, then a blank batch (it reads back blank with placeholder 3, `mergeBatchSizeChosen 0`), and windows `-1s`, `45s`, blank and `2m`.
  After a reload the window read `2m0s` with placeholder `5m0s`.
- Gathering note, from the seeded history: "3 holds since 10/31/2026, 10:02:00 AM: 1 ended in a batch, 1 let go early …, and 1 ran the
  full window and expired. A wait that paid off took 2m 0s on average, against a window of 5m 0s. The 1 hold that expired waited
  5m 0s each…". The "since" time is the first hold's end, not when it was armed, and "1 hold … each" reads oddly (both noted).
- `0s` was refused for poll interval, max worker agents 6 saved, then poll interval `12s` saved. **"Poll interval must be positive"
  stayed up** beside "All changes saved" until it timed out (**bug 1**, fixed).

### CI (`/settings/ci`)

- All 14 fields start blank, with grain's defaults as placeholders (2m0s, 1m0s, 5000, 64, 14, 256, 256, 4096, 30, 2048, 14, 128, 32).
  The exception is CI jobs per run, where blank means no limit.
- Refused by name: `fast`, −2, 1.5, −1 for largest artifact, and an age boost of `-1s` ("cannot be negative…").
  Runs kept −5 saves, since a negative value means keep every run.
- New values on every card (3, 1m30s, 10m0s, 500, 128, 7, 512, 1024, 9000, 45, 4096, 3, 256, 64) saved on Enter and read back exactly after a
  reload, in the boxes and from `GET /api/settings`. Clearing all 14 put them back to blank with the placeholders.
- The same stale-banner bug showed here: the `-1s` age-boost refusal stayed up after 90s saved, because jobs per run had saved in between (bug 1).

### Sandboxes (`/settings/sandboxes`)

- Backend is a native select:
  - Granule VMs ("needs SSH user/workspace/git proxy host for granule"), Host directories ("needs -host-sandboxes …") and Disabled
    are `disabled` options, with the reason in the label and the tooltip, and Playwright cannot select them.
  - Kubernetes saved (needs restart).
- Network `none` saved. vCPUs 4 and disk 50 saved. Memory 100 was refused ("must be 0 (unset) or at least 128"), and so was disk −1.
  Session idle TTL `1h` saved.
- Fixed overcommit: CPU −1 was refused, while 2.5, 1.25 and 3 saved.
- Auto overcommit bounds, each refused by name:
  - CPU minimum 0.1 ("at least 0.25");
  - CPU pressure 150 ("cannot be more than 100");
  - CPU minimum 8 over a maximum of 6 ("Auto CPU minimum: … cpuMin 8.00 is above cpuMax 6.00");
  - memory maximum 0.5 under 1, and CPU minimum 1.5 over 1.

  CPU max 5 and memory pressure 10 saved, and everything read back after a reload.
- Disk floor: share 1.5 refused and 0.15 saved, minimum 4096 MiB saved, host memory reserve −5 refused and 2048 saved.
- Kubernetes card:
  - It comes prefilled: namespace `grain`, images `ghcr.io/bwsalmon/grain/{k8s-sandbox,guest}:latest`, workspace `/home/agent`,
    Deployment and container `grain`, kubectl `kubectl`, and priority classes `grain-interactive`, `grain-agent` and `grain-ci` (placeholder "none").
  - Refused by name: `Walk NS`, `novalue`, `Bad SA`, `not a host!`, `Bad_Class`, a 300-character class, `Bad Dep` and `Bad_C`.
  - Saved: `walk`, `pool=walk`, `walk-sa`, `grain.walk.svc`, `walk-ci-low`, an empty agent class, `walk-svc` and `walk-grain`.
    A blank kubectl reads back as the `kubectl` default.
  - Pod image `not a ref!!` and workspace `relative/dir` **saved without complaint** (**bug 2**, fixed). The Granule VM pod image is checked the same way.
- Custom sandbox image (the setup script's replacement): `not a reference!!` was refused, and `registry.walk.test/team/sandbox:v1` saved
  and read back, with "Could not read the labels … its sandbox contract is unverified" and the base-image recipe shown.

### Caches (`/settings/caches`)

- Both toggles are on and both placeholders read 20.
- Proxy cache −3 and 2.5 were refused by name. 40 GiB, then an Actions cache of 0 and 25 GiB, saved and read back after a reload.
  Clearing both put them back to blank.

### Console and network

- No `pageerror` on any page.
- The only console errors were the browser's own "Failed to load resource: 400" for each refused save above, where the 400 is the expected answer.
- The only failed requests were `GET /api/tasks` polls aborted (`ERR_ABORTED`) by navigation.

## Bugs

| # | Bug | Reproduction | Outcome |
| --- | --- | --- | --- |
| 1 | A refusal's banner outlived its own setting's later save whenever another setting saved in between: `useSettingsSection` remembered only the latest failure, and any save cleared it | Engine: Poll interval `0s` (Enter), Max worker agents `6` (Enter), Poll interval `12s` (Enter) | **Fixed** (`87dd4404`): the hook tracks every outstanding refusal by setting. Test: "takes a refusal's banner down when that setting saves after another one did" (`useSettingsSection.test.jsx`), which fails on the old hook |
| 2 | The Kubernetes sandbox and Granule VM pod images and the workspace directory saved anything | Sandboxes: pod image `not a ref!!`, workspace `relative/dir` | **Fixed** (`d291db88`): `sandboximage.ValidateReference` and an absolute-path check in `UpdateSettings`, extending `TestUpdateSettingsRoundTripsKubernetesSettingsAndValidatesNodeSelector` |
| 3 | Choosing a connection for a repository dropped a connection added for that repository with a credential of its own (here a GitHub App), with no confirmation, orphaning its stored key | Git: add a GitHub App scoped `acme/widgets` and a PAT scoped `acme`, then pick "acme on github.com (PAT)" for acme/widgets | **Fixed** (`1ac6deac`): `SetTeamRepoGitConnection` refuses to drop a repository-scoped connection whose credential nothing else holds, and names it. Test: `TestSetTeamRepoGitConnectionKeepsAConnectionWithItsOwnCredential` |
| 4 | Legacy capabilities → Other secrets listed a capability server's own secret, and its Delete broke that server | Configure config-map with a secret map, open Legacy capabilities: `capability-server-…` is listed with Delete secret | **Fixed** (`65c7916c`): `CredentialsSection` claims every secret `GET /api/capability-servers` names. Test: "leaves a capability server's own secret out of Other secrets" (`SettingsPage.test.jsx`) |
| 5 | The GCP service account email saved anything | Legacy capabilities: GCP service account email `not an email` (Enter) | **Fixed** (`6ccb8d73`): `UpdateSettings` trims it and refuses one that is not `local@domain`. Test: `TestUpdateSettingsRefusesAGCPServiceAccountEmailThatIsNotOne` |

Minor, noted and not filed:
- Add connection still offers *GitHub App* for a plain git host, which the save refuses.
- The demo's commit-author lookup ignores `-github-api`.
- *Granted by* entries are not links.
- The Executors "see System → Sandboxes" control is a button, not a link.
- The executor command shows a literal `<server-addr:port>`.
- The gathering note's "since" is the first hold's end time, and reads "1 hold … each".
- A capability server's configure refusal repeats "configure: configure:".
- The image-reference refusals say "such as …" twice.
- The Kubernetes and custom image defaults still name `ghcr.io/bwsalmon/grain` (the release-image repository question is #2201's).
- The demo's custom `cloud` server keeps a literal `Authorization` header in its connection rather than a bound secret, which the page shows. That is the demo's own seed, and the page says connections are plain text.

## Tests run

- `npx vitest run src/useSettingsSection.test.jsx src/components/SettingsPage.test.jsx src/components/SecretsPanel.test.jsx src/components/CapabilitiesPanel.test.jsx`: all pass.
  The two new tests fail without their fixes.
- `go test ./pkg/ui ./pkg/cli -count=1`: pass. The three new or extended Go tests fail without their fixes.
- `make ui-screenshots PAGES=/settings/credentials,/settings/engine,/settings/sandboxes,/settings/git`, at desktop and phone widths, in light and dark.

## Second run: Task #2238

- **Task:** [Task #2238](/tasks/2238), branch `grain/049f1e09/task-2238` (attempt 2; attempt 1 was paused on a model usage limit before it pushed anything).
- **Date:** 2026-11-01, 05:01–05:45 UTC.
- **Base tested:** `main` at `2d1e77ee`, which is before [Task #2090](/tasks/2090)'s fixes above merged. The two runs went in parallel, so this run reproduced #2090's bugs 2 (Kubernetes pod images and workspace) and 4 (Other secrets) on its own.
  - This branch was rebuilt on `main` after #2090 merged, so it carries #2090's fixes for both and none of its own.
  - A follow-up it had proposed for Other secrets duplicates #2090's fix 4, and should be closed.
- **Fixed here, beyond #2090:**
  - F1, a refused merge batch size left in its box;
  - F2, *Granted by* entries as links;
  - F3, the notice's wording;
  - F4, *GitHub App* offered for a plain git host;
  - F5, the GCP project ID check;
  - F6, a refused Kubernetes value left in its box.

  F2 and F4 were on #2090's "noted, not filed" list. Each was checked in the browser on a server rebuilt from `2d1e77ee` plus these fixes, before the branch was rebuilt on `main`. After the rebuild the unit and Go tests below were run again, and they pass.

### Environment

- Debian sandbox, Go 1.26.10, Node 20.19.0, Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), viewport 1400×1000.
- Build: `npm ci && npm run build` in `ui/`, then `go build -tags grain_e2e ./cmd/grain`.
- Main server: `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file <file> -secrets-store -github-api 127.0.0.1:40169 -host-executor hostd`.
  It ran over a throwaway embedded store, with `TMPDIR` set to a scratch directory.
- The no-git-connection notice was checked on a second demo started with `-unconfigured` on `127.0.0.1:4292`.
- The browser signed in with the demo's access code (`ui/e2e/accesscode.js` `signIn`), and the session cookie went into the Playwright context.
- Everything after sign-in went through visible controls:
  - clicking, typing, and pressing Enter to commit;
  - choosing MUI and native select options;
  - keyboard focus plus Enter;
  - accepting or dismissing confirmations.

  `fetch`es against the API were used only to read back what a save had persisted, plus two probes noted below. The ad-hoc scripts lived outside the tree (`/home/debian/walk`), and none of them, nor any screenshot, is committed.
- `/home/debian/.gemini-api-key` does not exist in this sandbox, so no Gemini-backed tooling was used.

### Stubs

Only boundaries the demo has no real counterpart for were stubbed. No browser route interception (`page.route`) was used.

| Boundary | Stub | Routes it answered |
| --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js`, fronted on loopback `127.0.0.1:40169` and given to the demo as `-github-api` (plain HTTP) | `GET /rate_limit`, `/user`, `/user/repos`, `/repos/{owner}/{repo}`. Token `ghp_walkthrough_good` answers as `walk-bot` (scopes repo, workflow), with push on `acme/widgets` and read on `acme/lib`. Any other token gets a 401. My first stub table gave `scopes` as a string, which crashed the stub on the first Test ("connection refused"). Corrected and re-run |
| Git host behind a connection's proxy | a 10-line Node HTTP proxy on `127.0.0.1:4391` that logs each request and answers 502 | It logged `CONNECT git.walk.test:443` twice, once for each `team/app` Test |
| Secrets store | the demo's own `-secrets-store` (a real store in the throwaway directory) | not a stub, only enabled |
| Capability servers | the demo's seeded remote servers (`capabilities.demo.grain.example/{cloud,gcp}`) and grain's real built-ins (`grain capability config-map`, `host-script`, …), started beside the daemon | none intercepted |
| grain hostd | the demo's `-host-executor hostd` (a real hostd on a throwaway allowlist) | none intercepted |
| SSH host, Kubernetes API, GCP | not stubbed: `ssh.walk.test` does not resolve, the Kubernetes card only saves settings, and no GCP call was made | none |

### Steps and outcomes

#### Git (`/settings/git`)

1. The table showed the seeded `github.com` PAT connection. **Add connection** offers kind *GitHub* or *Plain git*, and auth *PAT*, *SSH key*, *GitHub App* or *Ambient*.
2. Saved through the form:
   - plain git `git.walk.test`: PAT, scope `team`, proxy `http://127.0.0.1:4391`;
   - the same host again with scope `team/app`;
   - plain git `ssh.walk.test`: SSH key (a throwaway ed25519 key) plus a known_hosts line, scope `team/app`;
   - GitHub `github.com`: PAT `ghp_walkthrough_good`, scope `acme`;
   - GitHub App: app 424242, installation 777, a throwaway RSA key, scope `acme/widgets`.

   Every row read back after a reload with its kind, auth type, scope and "via proxy …".
3. Refused saves, each by name:
   - a plain git host with a GitHub App: "a GitHub App needs a GitHub host";
   - host `bad host!`: "is not a host name";
   - proxy `socks5://user:pw@…`: "must be http(s)://host:port";
   - scope `a/b/c`: "must be an owner or owner/repo".

   An empty token keeps **Save connection** disabled.
4. **Test** on each row:
   - GitHub PAT: "Token accepted as @walk-bot by github.com · scopes: repo, workflow · ✓ acme/widgets read, push".
   - Seeded PAT: "GitHub rejected the token (bad credentials)".
   - App: "no installation token could be minted…", as expected for a fake key.
   - Plain git `team`: "nothing was tested … give the connection an owner/repo scope".
   - Plain git `team/app`: the proxy stub logged the CONNECT, and the result was "could not reach git.walk.test … Bad Gateway".
   - SSH key: "lookup ssh.walk.test … no such host ✗ team/app".
5. **Remove** asks first: "Remove the git.walk.test connection …? A credential stored for it is deleted with it and cannot be read back…".
   Dismissing it kept all 6 rows. Focusing Remove and pressing Enter raised it again, and accepting left 5 rows, which stayed that way after a reload.
6. No-git-connection notice (`-unconfigured`): Repos and the New task dialog both show it. Both **Settings → Git** links point to `/settings/git`, and the dialog's, reached by keyboard, opened it.
   The notice named only an ambient login for a plain git host. **Fixed (F3)**, and it now reads "a token, SSH key or ambient login for another git host".
7. The form offered *GitHub App* for a plain git host and refused it only on save. **Fixed (F4)**: the option is disabled for plain git, with the reason as its title.

#### Capabilities (`/settings/capabilities`)

1. `/settings/capability-servers` redirects to `/settings/capabilities`. It lists 8 servers: the built-ins first (config-map, gchat, gcp, git, host-script, model-proxy), then the two custom ones.
   - The built-ins read *needs configuring* or *not attached*, and the custom ones *attached*.
   - Each server has its own page at `/settings/capabilities/<encoded ID>`; there are no `role=tab` tabs.
   - A focused row opens its page on Enter.
   - Built-ins offer only **Test** and **Disable**; custom servers add **Remove**.
2. **config-map**, through *Edit server settings* → *Add config map* / *Add secret map*. Refused by name, with nothing stored:
   - map name `Walk Settings` ("does not match ^[a-z0-9]…");
   - directory `../escape` ("is not a clean absolute path");
   - mode `999` ("must be a four-digit octal mode").

   With `walk-settings/walk.toml`, and `walk-secrets/token` set to a throwaway value:
   - **Test server settings** answered 1 method, whose `keys` allow `walk-settings/walk.toml` and `walk-secrets/token`.
   - It saved and turned *attached*.
   - After a reload the secret box is empty with "Stored in the encrypted secrets store", and the value appears in neither the page HTML nor `GET /api/capability-servers`.
3. **host-script**:
   - Two scripts both named `walk-echo`: "script "walk-echo" is configured twice".
   - `Walk Sleep!` and timeout 100000 were refused inline by name ("does not match …", "must be at most 86400"). A PUT went out each time, nothing was stored, and the server stayed as it was.
   - Valid `walk-echo` (timeout 10s, ≤ 2 args) and `walk-sleep` (`sleep 5`, timeout 1s) saved.
   - The method's `script` argument then allows exactly those two.
   - Per-script **Test**:
     - `world; rm -rf /` gave exit 3, stdout `hello world; rm -rf /` and stderr `oops`: the argument is passed verbatim, not read by a shell.
     - Three arguments gave "Not run: 3 arguments exceed walk-echo's limit of 2".
     - `walk-sleep` gave exit 124 "timed out after 1s; its process group was killed".
4. **grain hostd** (Settings → Models): "grain hostd is up at …/hostd.sock (build v0.0.0-20261101050029-2d1e77ee8abf, hostd protocol 3), offering capability corp-search, cli agy".
   For claude and codex it reads "Host is unavailable … its allowlist (/etc/grain/hostd.yaml) offers no cli entry".
   The demo seeds no adopted runs; `ui/e2e/cli-run-locations-hostd.spec.js` covers those.
5. **Custom server** `…/gcp`:
   - I filed Task #20 through New task with `…/gcp#gcp-key` (account `walk-acct`). The server's *Granted by* listed it, but **not as a link**. **Fixed (F2)**: it now links to `/tasks/20`, checked in the browser.
   - **Test** listed the method and its service-account report.
   - Changing the Capability ID to `…/gcp-walk` opens "Rename this capability server? … These still grant its capabilities, and will be refused them until they are changed: Task #20 …".
   - Confirming moved the URL, and the old deep link says "No capability server here is called …/gcp".
   - Renaming back with **Cancel** kept `gcp-walk` after a reload. Leaving the still-edited form raised the browser's unsaved-changes prompt. Renamed back to `…/gcp` afterwards.
6. **Disable** on host-script turns it *disabled* ("Turned off: grain does not connect to it and refuses grants of its methods. Its entry and settings are kept") with no confirmation. It is reversible, and **Enable** brought it back *attached*. Noted, not filed.

#### Capability policy

- **Add rule** → Deny `https://capabilities.demo.grain.example/cloud#cloud-viewer` for *agent tasks* → **Save policy**.
  `GET /api/capability-policy` returns the rule, it reads back after a reload, and all 8 servers are still listed.
- New task → **Add grant…** shows `cloud-viewer` disabled with "the capability policy denies … (rule 1: deny …)" and a Settings → Capabilities link. `gcp-key`, config-map and host-script are offered.
  The template picker was covered on 10-30 and not repeated.
- **Remove rule** → **Save policy** left `{"policy":{}}`.

#### Legacy capabilities (`/settings/credentials`)

- "16 of 21 configured. Still to configure: GCP key, Gemini key, GitHub sandbox, Registry push, Image signing".
- GitHub host:
  - `https://github.com` was refused ("a host name, not a URL").
  - The demo's value is the stub, `127.0.0.1:40169`, with *speak plain http* on, and both are marked *needs restart*.
- GCP project:
  - **`Bad Project!` saved** (box, API and reload). **Fixed (F5)**: it is now refused as "GCP project "Bad Project!" is not a GCP project ID…".
  - `walk-project-2238` saved and read back.
- GCP service account:
  - Email `not-an-email` saved on this run's base. #2090's bug 5 now refuses an email without `@`.
  - Count 0 was refused by name, and 3 saved and read back.
  - Values were put back afterwards.
- **Other secrets** lists `capability-server-1f6d92c4 / secret_values`, under "Everything … that nothing above claims", with a **Delete secret** button.
  That secret is the config-map server's own, saved in step 2 of Capabilities. This is #2090's bug 4, fixed on `main`.
- A throwaway `walk-throwaway`/`token` was set, replaced and deleted. Delete was dismissed once and then accepted. The value appeared neither in the HTML nor after a reload.

#### Executors (`/settings/executors`)

- **Issue token** keeps *Generate token* disabled for an empty name and for one of spaces.
- `walk-throwaway-executor` showed a 36-character `gxt_…` token once, with `grain model-server --token <token> --server <server-addr:port>`. **Copy** put exactly the token on the clipboard.
- After a reload only `Prefix: gxt_87c1…` remains, in neither the page text nor the HTML.
- **Revoke** opens "Are you sure you want to revoke token walk-throwaway-executor (Prefix: gxt_87c1…)? … disconnected immediately and blocked from reconnecting":
  - **Cancel** left it active, with its Revoke button still there;
  - **Revoke token** marked it *revoked · Revoked: 11/1/2026, 5:32:23 AM*, which persists across a reload.
- "2 connected · see System → Sandboxes", reached by keyboard (focus plus Enter), opens `/system/sandboxes`, whose model servers card lists `runner-infra-worker-01` and `runner-frontend-worker-01`.

#### Engine (`/settings/engine`)

- Poll interval:
  - `abc` was refused ("time: invalid duration") and `0s` was refused ("must be positive");
  - `15s` saved.
- Max worker agents −1 and max merges per repo branch 0 were refused by name, while 7 and 2 saved.
- Merge batch size:
  - 0 and `1.5` were refused client-side ("…; 0 was not saved", "takes a whole number").
  - **21 was refused by the daemon ("cannot be more than 20") but stayed in the box**, as though it had been saved. **Fixed (F1)**: the box now goes back to the saved value.
  - 4 saved. Blank saves as grain's default and shows placeholder 3.
- Merge gather window: `soon` was refused, while `-1s` and `45s` saved, with placeholder `5m0s`.
- Every value read back after a reload. Tab moves through the five boxes in order.
- The batching and gathering notes need queue holds, which the demo does not seed. `MergeGatheringNote`'s unit tests cover them.
- Yesterday's bug B (a stale refusal banner) stays fixed.

#### CI (`/settings/ci`)

- All 14 boxes start blank, with grain's defaults as placeholders: 2m0s, 1m0s, 5000, 64, 14, 256, 256, 4096, 30, 2048, 14, 128, 32. CI jobs per run has no placeholder, because blank there means no limit.
- Refused by name: `fast`, −2 and `1.5`.
- −5 for CI runs kept saved. That is deliberate: a negative history bound means "keep every run" (`UpdateSettings`, grain/task-269), while the job-cache and artifact bounds refuse negatives.
- New values on every card (3, 2m0s, 10m0s, 500, 2048, 14, 4096, 10240, 50000, 30, 4096, 7, 256, 64) read back exactly after a reload. Clearing all 14 put them back to blank.

#### Sandboxes (`/settings/sandboxes`)

- Backend:
  - Granule VMs ("needs SSH user/workspace/git proxy host for granule"), Host directories ("needs -host-sandboxes …") and Disabled ("needs -disable-native-sandboxes …") are `disabled` native options, with the reason in the label and title.
  - Docker, Kubernetes pods and Kubernetes Granule VMs are selectable.
- Size: vCPUs 4 and memory 4096 saved. Memory 100 MiB ("must be 0 (unset) or at least 128") and disk −1 were refused.
- Budget, auto bounds:
  - CPU minimum 0.1 was refused ("must be at least 0.25").
  - Minimum 8 over a maximum of 6 was refused by name ("Auto CPU minimum: … cpuMin 8.00 is above cpuMax 6.00"), and so were memory maximum 0.5 under 1 and CPU minimum 1.5 over 1.
  - CPU pressure 150 and memory pressure −1 were refused.
- Budget, fixed ratios: CPU overcommit −1 was refused, while 2.5, 1.25 and 3 saved. All of them read back after a reload.
- Disk floor:
  - share 1.5 was refused, and 0.15 saved;
  - a minimum of 4096 MiB saved;
  - host memory reserve −1 and −5 were refused.
- Kubernetes card:
  - It comes prefilled (namespace `grain`, images `ghcr.io/bwsalmon/grain/{k8s-sandbox,guest}:latest`, workspace `/home/agent`, Deployment and container `grain`, `kubectl`), each with a *default* chip.
  - The priority classes are `grain-interactive`, `grain-agent` and `grain-ci`.
  - Refused by name: `Walk NS`, `Bad_Class`, node selector `novalue`, ServiceAccount `Bad SA` and proxy host `not a host!`. Yesterday's bug C stays fixed.
  - Saved and read back: `pool=walk`, `walk-ci-low` and an empty agent class.
  - A pod image of `not a ref!!` and a workspace of `relative/dir` saved on this run's base. #2090's bug 2 fixes that, and both are refused by name on the merged branch.
  - **A refused namespace, pod image or workspace stayed in its box** next to its refusal, though a reload showed the old value. **Fixed (F6)**: the box goes back.
  - `kubectl; rm -rf /` saves as the kubectl path. It is executed directly, not through a shell, so I noted it and did not file it.
- Custom sandbox image: `not a reference!!` was refused, and `registry.walk.test/team/sandbox:v1` saved and read back, with a note that its labels could not be read.

#### Caches (`/settings/caches`)

- Both toggles are on, and both placeholders read 20.
- −3 was refused by name for each. 40 GiB and 25 GiB saved and read back 40 and 25 after a reload.

#### Console and network

- No `pageerror` on any page.
- The only console errors were the browser's own "Failed to load resource: 400" for each refused save above, where the refusal is the expected answer.
- The only failed requests were `GET /api/tasks` polls aborted (`ERR_ABORTED`) by navigation.

### Bugs

| # | Bug | Reproduction | Outcome |
| --- | --- | --- | --- |
| F1 | A merge batch size the daemon refused (21, over its ceiling of 20) stayed in the box, looking saved | Engine: type `21` into Merge batch size and press Enter | **Fixed** (`93e4b59c`): the box is keyed on the saved size as well as the chosen one, so a rollback remounts it. Test: `EngineSection.test.jsx` (fails without the fix) |
| F2 | *Granted by* and the rename/remove confirmation list a task as plain text | File a task granting `…/gcp#gcp-key`, then open that server | **Fixed** (`bf43a17e`): Task #N links to `/tasks/N`. Test: `CapabilityServersPanel.test.jsx` |
| F3 | The no-git-connection notice named only an ambient login for a plain git host | `grain demo -unconfigured`, then Repos or New task | **Fixed** (`ba311dc3`). Test: `NoGitConnectionNotice.test.jsx` |
| F4 | Add connection offered *GitHub App* for a plain git host and refused it only on save | Kind *Plain git*, then Auth type | **Fixed** (`775acd4f`): the option is disabled there, and switching an App draft to plain git resets it to PAT. Test: `GitConnections.connections.test.jsx` |
| F5 | GCP project saved a value that is not a project ID | Legacy capabilities → GCP project `Bad Project!` | **Fixed** (`374a0736`): refused by name, trimmed, with a legacy `domain:` prefix allowed. Test: `TestUpdateSettingsRefusesAGCPProjectThatIsNotAProjectID` |
| F6 | A refused Kubernetes namespace, pod image or workspace stayed in its box (`DefaultableField` keeps its own draft) | Sandboxes → Kubernetes namespace `Walk NS`, then Enter | **Fixed** (`93e4b59c`): the draft is put back when `saveChoice` reports a refusal. Test: `SettingsCard.test.jsx` (fails without the fix) |
| G | *Other secrets* lists a capability server's own config secret as unclaimed, with Delete | Configure config-map with a secret map, then open Legacy capabilities | Fixed by #2090 (bug 4). This run's follow-up proposal duplicates it |

Minor, noted and not filed:
- Disable on a capability server asks nothing, even when something grants it (it is reversible).
- The executor command shows a literal `<server-addr:port>`.
- Executor times use the browser's locale.
- The demo's commit-author lookup ignores `-github-api` ("HTTP 401").
- Removing a capability server leaves the `capability-server-<hash>` secrets grain created for it in the store, where they then show under Other secrets.
- The ambient option reads "this host's GitHub login" for a plain git host too.
- The demo's seeded `ops/triage` scope set names `bwsalmon/grain`, and the stock sandbox images are still `ghcr.io/bwsalmon/grain/...` (the release-image repository is #2201's).

### Tests run

- `npx vitest run src/components/settings src/components/CapabilityServersPanel.test.jsx src/components/NoGitConnectionNotice.test.jsx src/components/RepoList.test.jsx src/components/NewTaskOverlay.repos.test.jsx src/settingsIndex.test.jsx src/useSettingsSection.test.jsx`: all pass.
  `EngineSection.test.jsx` and `SettingsCard.test.jsx` fail without their fixes.
- `go test ./pkg/ui -count=1`: pass. `go test ./pkg/cli ./pkg/staterepo ./pkg/demo -run 'Sync|Settings|GCP|Gcp|Seed|Import|Demo'`: pass.
- No screenshots were taken. The UI changes are an option disabled in a menu, a link inside an existing list, one sentence, and a box's value after a refusal, each asserted in the browser walk above.
- CI on the first push of this series (`305140b0`), as the pull request:
  - `installer` failed at "Assemble the image to install": `export-source.sh` refuses the 129 MB staged source, over its 128 MB ceiling. It fails the same way on `main`, and a follow-up was proposed.
  - `go unit` failed on a data race in `pkg/hostd`'s tests, which this branch does not touch. It passes on `main`, and `go test -race -count=3 ./pkg/hostd` passed locally, so it is a flake. A follow-up was proposed.
  - Every other check passed, `ui e2e` included.
