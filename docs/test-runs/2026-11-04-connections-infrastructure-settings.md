# Test run — Settings → Git, Capabilities, Legacy capabilities, Engine, CI, Sandboxes and Caches (2026-11-04)

Task 2964 is a walkthrough of the Settings sections that connect grain to
outside systems and that tune the machines it runs on. It used a real
`grain demo` server, its built frontend and a real Playwright Chromium,
driven through visible controls.

**Result:** no new bug in these sections needed a code fix. One stale
help text was found under Legacy capabilities, and a follow-up was
proposed for it (see [Findings](#findings)). Yesterday's follow-up is
fixed on `main`: a deployment-declared API scope set is now offered to
pickers, and Remove names the grants that hold it. `main` itself did not
compile at the commit tested (see [Inherited breakage](#inherited-breakage)).

## Environment

- **Commits tested.** The walk ran in two runs on the same day, because
  the first was paused by a usage limit:
  - **Run 1, `main` at `a3bcafa9`** ("Merge pull request #693"): Git,
    Engine, CI, Caches, the capability list, and config-map's schema form
    with Disable/Enable.
  - **Run 2, `main` at `0c8855db`** ("Merge pull request #701"):
    everything else. It also re-ran the throwaway capability server's
    whole lifecycle.
- **Build:** `cd ui && npm ci && npm run build`, then
  `go build ./cmd/grain`. At `0c8855db` this needed the one-line local
  patch described under [Inherited breakage](#inherited-breakage). The
  patch was not committed.
- **Servers.** Each was a `grain demo` over its own throwaway embedded
  store, with `TMPDIR` set to a scratch directory, and each was given an
  access code with `-access-code-file`:
  - `:4390`: `-secrets-store -github-api 127.0.0.1:<stub> -host-executor
    hostd`;
  - `:4391`: `-unconfigured`. This one was used for the no-git-connection
    notices.
- **Browser:** Playwright Chromium from `/var/cache/playwright`, headless,
  1440×1000. It signed in by typing the access code on the sign-in page.
- **Recording:** each script logged `console` errors and warnings,
  `pageerror`, `requestfailed`, every HTTP response with status 400 or
  more, and every `window.confirm`/`beforeunload`.
- **Toolchain:** Debian, Go 1.26, Node v20.19.0.
- **Gemini:** `/home/debian/.gemini-api-key` is absent from this sandbox,
  so no model call was made.
- **Scripts:** ad hoc scripts in `/var/tmp/walk`. They were not
  committed, and nor were any screenshots.

## Stubs

| Boundary | How | Why |
| --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js`, given to the demo as `-github-api`. The run-2 copy listens on a fixed port. `ghp_walkgood` is `@walk-bot`, with scopes `["repo"]` and push on `acme/widgets`; every other token gets 401. There are no `/app/...` routes and no `/branches` route. | The sandbox has no GitHub access. |
| `GET /api/settings/cli-run` (Settings → Models, hostd status only) | `page.route` fetched the real answer and added these fields to `hostExecutor.hostd`: `running`, `current`, `currentBuild`, `updating: true`, `suggest: ["claude"]`, and two `processes`, one of them `detached` + `adopted`. | The demo's throwaway hostd never has an upgrade waiting, an adopted run or an allowlist suggestion. |

Not stubbed:

- the plain git host and SSH (real DNS failures came back);
- the capability servers (the real built-ins, and a real
  `grain capability config-map` subprocess);
- host scripts (the real `grain capability host-script`);
- the secrets store (the demo's real `-secrets-store`);
- Kubernetes (only settings were saved; nothing was applied to a
  cluster).

The demo's own registry-push "test credential" answers that it holds
nothing to test, so no registry was called.

## Git (`/settings/git`) — run 1

| Check | Outcome |
| --- | --- |
| Starting state | One row: `github.com · github · pat · all repos`, with Test and Remove. Below it are the Repositories binding and the commit-author block. |
| GitHub App offered only for a GitHub host | With Kind = Plain git, the "GitHub App" option is `aria-disabled="true"` with the title "A GitHub App needs a GitHub host: a plain git host has no App API". With Kind = GitHub it is enabled. |
| Plain git, PAT, scope `walk-org`, proxy `http://proxy.walk.example:3128` | Saved. The row reads `git · pat · via proxy … · walk-org`. Test: "nothing was tested: … no repository on git.walk.example is in this check's reach -- give the connection an owner/repo scope …". |
| Plain git, SSH key (throwaway ed25519) with a known_hosts line, scope `walk/repo` | Saved. Test: the real `lookup ssh.walk.example … no such host`, with `✗ walk/repo`. |
| SSH key `not a key` | 400. The form stays open, with "the private key will not parse: ssh: no key found". |
| A second connection on the same host and scope | 400: "this deployment already has a git connection on git.walk.example for scope "walk-org" (grain-git-c-…): remove it or edit its scope first". |
| GitHub PAT `ghp_walkgood`, scope `acme` | Test: "Token accepted as @walk-bot by github.com · scopes: repo · expires 2026-12-31 …", with "✓ acme/widgets read, push". |
| GitHub App, scope `acme/gadgets`, proxy, App ID 12345 / installation 67890 / throwaway RSA key | Saved with its proxy. Test: "no installation token could be minted from this GitHub App credential …". This is expected, because the stub has no App routes. |
| Reload | All four throwaway rows come back with their kind, auth type, scope and proxy. |
| Remove asks first | Each Remove opens `window.confirm`: "Remove the ssh.walk.example connection grain-git-c-…? A credential stored for it is deleted with it and cannot be read back …". Dismissing it kept the row (still 5 rows). Accepting removed it. The other three were removed by focusing Remove and pressing Enter, and each confirmed first. After a reload only the demo's row is left. |
| Keyboard | Add connection is reached by Tab, 45 presses from the top of the page. |
| No-git-connection notice (`:4391`) | Repos and New task both say: "This deployment has no git connection yet … Add a git connection first, in Settings → Git: a GitHub token or GitHub App for github.com, or a token, SSH key or ambient login for another git host." The links "Git connection in Settings" (Repos) and "Settings → Git" (New task) both open `/settings/git`. |
| Nameless connections in "Reuse authentication from" | Not reachable in this demo through the UI: its only model connection is Antigravity, so a new Claude connection offers no reuse source. Covered instead by `e2e/provider-connection-lifecycle-walkthrough.spec.js` ([Specs run](#specs-run)), which saves nameless Claude connections. `ModelBackendPage.jsx` labels each source `Reuse authentication from ${name \|\| id}`. |

During run 1 the GitHub stub crashed once. The cause was the walkthrough
script, which passed `scopes` as a string where the stub takes an array.
It was restarted on the same port. This is not a grain bug.

## Capabilities (`/settings/capabilities`)

| Check | Outcome |
| --- | --- |
| Old link | `/settings/capability-servers` lands on `/settings/capabilities`. |
| List order | "10 servers". The built-ins come first (config-map, gchat, gcp, git, host-script, grain, model-proxy, release, repo), then the demo's custom `capabilities.demo.grain.example/cloud`. config-map, gchat and host-script read `needs configuring`. |
| `(i)` popovers (`About grain`, `About repo`) | A click opens one, and it stays open when the pointer leaves. A second click closes it. Focus opens one, and Escape closes it. Enter toggles it open and then shut, and Space does the same. |
| config-map schema form (run 1) | Edit server settings → Add config map `walk-config` (directory `/home/agent/.walk`, description, key `settings.toml`, mode 0644, content) and Add secret map `walk-secrets` (key `token`, a throwaway value). Test server settings answered: "The server answered with 1 method", with the enums `walk-config`, `walk-config/settings.toml`, `walk-secrets/token` and `walk-secrets`. After Save and a reload the chip reads `attached` and `token: set`. The value is nowhere in the page source. |
| Disable / Enable (run 1) | Disable flipped the chip to `disabled` 2.1 s later without a reload, and the button became Enable. Enable returned it to `attached` 2.1 s later. |
| Throwaway server `https://walk.example/caps/throwaway` (stdio `/var/tmp/grain capability config-map`, env `WALK=1`) | Test in the add form answered "0 methods" and listed "Server settings it asks for". Add server opened its page. A secret map `walk-own/token` was saved through its schema form, and reads `token: set` after a reload. The value is not in the DOM. |
| Own secret placement | Legacy capabilities → "Used by capability servers" lists `capability-server-67efdff0 secret_values — Used by https://walk.example/caps/throwaway`. Other secrets reads "No other secrets set." |
| Rename and edit in place | Edit connection → id `…/renamed`, env `WALK=2`. A dialog asked first: "rename this capability server? … becomes https://walk.example/caps/renamed. Nothing grants this server's capabilities." After Rename, the URL became `…%2Frenamed`, and the secret's "Used by" followed it. Test on the saved entry: "grain is connected to this server", with enums `walk-own/token`. |
| Holder link | Task #1 → Add grant… offered "config-map walk.example/caps/renamed", which was added. The server's page then listed "Task #1 “Add a dark mode toggle” on acme/widgets", and its link opened `/tasks/1`. |
| Remove | The dialog lists the holder: "These still grant its capabilities, and will be refused them until they are changed: Task #1 … https://walk.example/caps/renamed#config-map={}". Cancel kept the server. Remove deleted it, and the list is back to 10 servers. The "Used by capability servers" block is gone, so the secret went with the server. Task #1 now shows the grant as "server not attached". |
| Host scripts | Added `walk-echo` (description, usage, script `printf "<%s>\n" "$@"; exit 2`, timeout 5) through host-script's schema form. The chip went from `needs configuring` to `attached`. Test → Run walk-echo with argument `a b; rm -rf /tmp/nothing`: `exit 2`, 2 ms, stdout `<a b; rm -rf /tmp/nothing>`. The argument was passed as one literal word. Timeout 5 → 7 saved, and reads `timeout 7s` after a reload. Timeout 0 was refused (400, `config.scripts[0].timeout: must be at least 1`), shown inline under the field with `aria-invalid`. It stayed at 7s after a reload. |
| hostd version, adopted runs, suggestions (Models, stubbed answer) | "grain hostd is up … but it is updating: new host runs wait until it is back …". The Installed line reads "Installed: build v0.0.0-walk-new … grain hostd runs version … and restarts on the installed one once the runs it holds have ended". Suggestions: "Suggested allowlist entry: claude … grain never edits your allowlist." Processes: "grain hostd is holding 2 processes … claude for run 7 of task 1: pid 4242, running, detached, adopted by this daemon; capability host-git: pid 4343, running". |

## Capability policy and API scope sets

| Check | Outcome |
| --- | --- |
| Deep link `#capability-policy` | The card is in view after load: its top was at y=486 on the first load and y=254 later. Yesterday's fix holds. |
| Edit and save | Two rules were added: **Deny** `…/cloud#cloud-viewer` for Agent tasks, and **Allow only** `…/cloud#cloud-key` on `acme/gadgets`. After Save policy and a reload both rules are back. |
| Task #1 (acme/widgets) picker | `cloud-viewer` is disabled: "the capability policy denies … to this task on acme/widgets (rule 1: deny …)". `cloud-key` is disabled: "the capability policy does not admit … for this task on acme/widgets". Both link to `/settings/capabilities#capability-policy`. |
| Repository defaults (`/repos/acme/widgets#ci`) | `cloud-viewer` is offered, since the deny covers Agent tasks only. `cloud-key` is disabled for "this ci job on acme/widgets", and links to the policy. |
| New template (`/processes` → + New → Template) | No repository is known, so neither entry is disabled. `cloud-viewer` reads "denied by the capability policy for agent tasks", and `cloud-key` reads "may be refused by the capability policy for agent tasks". Both link to the policy. |
| New schedule | The same verdicts as task #1, for acme/widgets. |
| New task overlay | The same verdicts as task #1. |
| Following a verdict link | A real mouse click on "Settings → Capabilities" inside a disabled entry opens `/settings/capabilities#capability-policy` with the card in view (y=254). Tab from the open menu also reaches the link. Playwright's `locator.click()` refuses it as "not enabled", because it sits under `aria-disabled`. That is a test-tool quirk: the row sets `pointer-events: auto`. |
| API scope set create | New scope set `walk/throwaway` (scopes `tasks:read, ci:read`, repositories `acme/widgets`) reads back as "config repo · tables/api_scope_set.json", with `ci:read @ acme/widgets` and `tasks:read @ acme/widgets`. |
| Edit | Description "walkthrough readers" and scopes `tasks:read` read back after a reload. |
| Bad scope | `tasks:fly` gets a 400: "scope set "walk/bad": "tasks:fly" is not a scope of docs/agent-api.md (nor <area>:*)". |
| Offered to pickers | Task #1 → Add grant… offers `api:walk/throwaway`. It was granted, and reads back after a reload. **This was yesterday's follow-up, and it is fixed on `main`.** |
| Remove when a grant holds it | The dialog says: "remove scope set walk/throwaway? 1 grant holds api:walk/throwaway, and will grant nothing once it is removed: Task #1 … api:walk/throwaway". Cancel kept it. Remove deleted it, and a reload confirms it is gone. Task #1 now shows "api:walk/throwaway no longer offered". |

## Legacy capabilities (`/settings/credentials`)

| Check | Outcome |
| --- | --- |
| Overview | "7 of 10 configured. Still to configure: GitHub sandbox, Registry push, Image signing". It lists ten cards (github sandbox … durable), with ready / not ready / ci only / default chips. |
| GitHub host | Reads `127.0.0.1:<stub>` and carries a `needs restart` chip. `https://github.com/` was refused by name: "GitHub host is a host name, not a URL: give "github.com" rather than …". |
| GCP project, service account email and count | `walk-proj-123`, `walk@walk-proj-123.iam.gserviceaccount.com` and `3` were saved and read back after a reload, then cleared. Refused by name: count `0` ("must be at least 1"), project `Bad Project!` ("is not a GCP project ID: 6 to 30 lowercase letters …") and email `not-an-email` ("is not a service account email …"). |
| Other secrets: add | Secret `walk-throwaway` / key `token` / value (a `type=password` box) was set. After a reload it is listed by name and key only, and the value is not in the DOM. |
| Replace | There is no per-row Replace button. Setting the same secret and key again replaced it, and neither value is in the DOM. |
| Remove | "delete secret" opens `window.confirm`: "Delete the secret walk-throwaway and every key in it? …". Dismissing it kept the secret; accepting deleted it ("No other secrets set."). |
| Empty value | Set secret with an empty value stores nothing. No snackbar appeared (observation below). |
| A legacy capability's own credential (Registry push) | The page lists "Missing secrets: registry-push/credential". A throwaway Docker-config JSON was set, and the value is not in the DOM. Test credential: "grain demo holds no registry-push credential to test: nothing here is real". Clear asked first: "Clear registry-push/credential? …". |

## Engine — run 1

| Check | Outcome |
| --- | --- |
| Starting values | Poll interval `10s` (placeholder `10s`), Max worker agents `4` (no placeholder), Max merges per repo branch `5` (placeholder `5`). |
| Save and reload | `25s`, `6` and `2` read back. |
| Refused by name | Poll `0s` and `-5s`: "Poll interval must be positive" (400). Poll `soon`: "Poll interval: time: invalid duration "soon"". Workers `0`/`-2`: "Max worker agents must be at least 1; 0 was not saved." Workers `1.5`: "… takes a whole number; 1.5 was not saved." Merges `0`: "… must be at least 1; 0 was not saved." After a reload, 25s/6/2 are unchanged. |
| Blank resets | Poll interval → `10s`, Max worker agents → `10` and Max merges → `5`. The API reads back `pollInterval 10s`, `maxWorkers 10` and `maxMergesPerRepo 5`. |
| Deep link `#merge-queue` | The card is in view (top y=433). |
| Sandbox commitment note | Not shown. It renders only when `settings.sandboxCommitment` is set, and the demo reports none. |

## CI — run 1

- **Saved and read back after a reload:**
  - jobs per run 3;
  - age boost `90s`, which reads back as `1m30s`;
  - reserved after `-1s` (reservation off);
  - runs kept `-1` (keep every run);
  - step output 32 MiB for 7 days, and 128 KiB per run;
  - job cache 300 MiB, 1000 entries, 9 days;
  - artifacts 1024 MiB for 5 days, 64 MiB per run, 16 MiB per file.
- **Refused:**
  - jobs `-1`: "must be at least 0; -1 was not saved.";
  - boost `soon`: "time: invalid duration";
  - boost `-5s`: "CI queue age boost cannot be negative: it is the floor that stops a low-ranked job waiting for ever …";
  - cache `2.5`: "takes a whole number".
- **Placeholders:** every box starts blank, with grain's default as its
  placeholder (`2m0s`, `1m0s`, 5000, 64, 14, 256, 256, 4096, 30, 2048,
  14, 128, 32).
- **Cleared:** clearing all 14 left them blank again after a reload.

## Caches — run 1

- **Placeholders:** both boxes show `20`.
- **Saved and read back:** proxy cache 25 GiB and Actions cache 12 GiB,
  after a reload.
- **Refused:**
  - `-3`: "Proxy cache max size (GiB) must be at least 0; -3 was not saved.";
  - `2.5`: "Actions cache max size (GiB) takes a whole number".
- **`0`:** saves as blank, which means the default.
- **Actions cache switch:** unticking it hides the size box, and it stays
  off after a reload. Re-ticking brings the box back.
- **Cleared:** clearing both left them blank after a reload.

## Sandboxes — run 2

| Check | Outcome |
| --- | --- |
| Backend select | It is a native `<select>`. `granule`, `host` and `none` are `disabled`, each with its reason in the option text and its `title`: "needs SSH user/workspace/git proxy host for granule", "needs -host-sandboxes at daemon startup", and "None (control plane only) — unavailable (needs -disable-native-sandboxes at daemon startup)". The card carries a `needs restart` chip and the description "Each run executes in an unprivileged local Docker container …". Picking `kubernetes-granule` was saved and read back; it was restored to `container` afterwards. |
| Network | `open` → `none` → `open`. Each was saved and read back after a reload. |
| Session sandbox idle TTL | `45m` saved. `soon` refused: "Session sandbox idle TTL: time: invalid duration "soon"". `-1s` (disable warm retention) saved, and reads `-1s` after a reload. |
| Fixed overcommit ratios | CPU 3, memory 1.25 and disk 4 read back after a reload. CPU `-1` refused: "CPU overcommit must be at least 0; -1 was not saved." Disk `1000` was accepted (observation). |
| Auto overcommit bounds | Mode → auto, read back. CPU min 1.5, CPU max 8, memory min 1.1, memory max 3, pressure targets 30 and 10, and memory reserve 2048 all read back. |
| Auto bounds refused by name | CPU min `0`: "Auto CPU minimum must be at least 0.25; 0 was not saved." Memory max `0.5`: "Auto memory maximum: sandbox overcommit memoryMin 1.10 is above memoryMax 0.50". CPU target `150`: "cannot be more than 100". Memory target `-5`: "must be at least 0.5". Reserve `-1`: "must be at least 0". CPU max `1000` was accepted, and then so was CPU min `9` (observation). The mode was restored to fixed. |
| Disk floor | Share 0.2 and minimum 8192 MiB read back. Share `1.5`: "cannot be more than 1". Share `-0.1` and minimum `-5`: "must be at least 0". |
| Size | Memory 8192 and disk 40 read back. vCPUs `0` saves as blank, which means the default (2). Memory `-1` refused. |
| Kubernetes card prefilled defaults | Namespace `grain`, pod image `ghcr.io/bwsalmon/grain/k8s-sandbox:latest`, VM image `ghcr.io/bwsalmon/grain/guest:latest`, workspace `/home/agent`, Deployment and container `grain`, `kubectl`, node selector placeholder `pool=sandboxes`. The priority classes are `grain-interactive`, `grain-agent` and `grain-ci`, each with placeholder `none`. |
| Kubernetes validation | Each was refused by name: namespace `Bad_NS!`, CI priority class `Bad_Class!`, node selector `garbage` ("must be key=value"), workspace `relative/path` ("is not an absolute path"), pod image `not an image ref` ("must be one OCI image reference") and ServiceAccount `Bad SA`. |
| Kubernetes save | Namespace `walk-ns`, CI class `walk-ci` and selector `pool=walk` read back after a reload. Clearing the agent class saved `""` (no priority class, as the help says). All were restored afterwards. |
| Custom sandbox image card | It explains building `FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:latest`, the contract the base already satisfies, and `dev.grain.sandbox.contract = 2`. `registry.walk.example/team/sandbox:1` saved and read back. `Not A Ref!` was refused by name. For a set image the card says its contract is unverified and suggests `grain sandbox-image check <ref>`. On the Kubernetes Granule backend, with the same ref in `k8s_granule_image`, the pin hint appears ("… holds the same reference … Clearing only the custom image leaves that pin in place …"). "Clear both" cleared both. The mirror wording (`k8sGranuleImageMirror`) was not reached: the demo sets no mirror. |

## Console and network

- **Console:** no `pageerror` in either run.
- **Expected `console.error`s:**
  - the 401 from the sign-in page before the code is typed;
  - the 400s for the refusals provoked above.
- **`404 GET /api/repos/acme/widgets/github-branches`:** on task pages,
  because the GitHub stub has no branches route.
- **`requestfailed` on `GET /api/tasks`:** many `net::ERR_ABORTED`. These
  are the task-list poll being cancelled by navigation, and they were
  filtered out after checking that the page loaded. Only 3 `/api/tasks`
  requests were made in 5 s on a settled page.
- **`beforeunload`:** fired when leaving a page with an unsaved draft
  (Models new connection; host-script form after a refused save).

## Findings

### 1. Legacy capabilities' GCP help points at a card that no longer exists — follow-up proposed

- **What the page says.** Settings → Legacy capabilities, under GCP, the
  service-account-email help reads: "The minter's own key is set on the
  GCP key capability's page, in the list above."
- **What the list holds.** There is no GCP key card in that list. Its ten
  cards are GitHub sandbox, self debug, bootstrap playbooks, registry
  push, package publish, image signing, interactive console, read issues,
  priority dispatch and durable. The capabilities page says `gcp-key` and
  `gemini-key` were retired onto `https://grain.dev/capabilities/gcp`, and
  that server's page says nothing about the `gcp-key-minter` secret.
- **The same stale reference elsewhere.** The project help ("Either
  capability is not ready until it is set") and the "deployment-wide
  settings" lead ("the GCP project and service accounts gcp-key and
  gemini-key mint from") still describe those two as legacy cards.
- **Source:** `ui/src/components/settings/CredentialsSection.jsx`, lines
  130 and 144, and `CapabilitiesPanel.jsx`, line 565.
- **Why it was not fixed here.** The right new pointer depends on where
  an operator is now meant to set `gcp-key-minter`: the gcp server's
  page, Other secrets, or somewhere else. That is a product decision, so a
  focused follow-up was proposed.

### Observations (not filed)

- **Max worker agents has no placeholder.** Its blank box is refilled
  with `10`, so this matters less than it looks. It was also noted on
  2026-11-03.
- **No upper bound on the auto CPU maximum or the disk overcommit.**
  `1000` is accepted for both. `OvercommitSettings.Validate` refuses only
  negatives, a minimum above its maximum, and targets over 100, so this
  is by design.
- **CI runs kept takes any negative number.** `-7` is accepted and reads
  back as `-7`. Every negative means "keep every run"
  (`model.Config.CIRunHistory`), but the help names only `-1`.
- **An empty secret value is silently not stored.** Set secret with an
  empty value gives no inline message and no snackbar.
- **The config-map secret-map default directory is
  `/home/debian/.config-map/<name>`.** That is the sandbox user's home
  (`intree.ConfigMapDir`), not the daemon's.
- **The default images still name `ghcr.io/bwsalmon/grain/...`.** Also
  noted on 2026-11-03.

## Inherited breakage

At `0c8855db`, `main` does not compile:

```
pkg/cli/main.go:684:20: duplicate case "login" (constant of type string) in expression switch
	pkg/cli/main.go:610:28: previous case
```

The merge of #512 (`feat/git-login-task-attribution`) added `"login"` as
an alias of `git-login`, but `"login"` is already the sign-in command.
CI on `main` is red (go, go unit, go model, go e2e, ui e2e, run host,
installer, release images). For this walk only, the binary and the e2e
specs were built with that case reduced to `case "git-login":`. The patch
was not committed: the base-branch repair belongs to grain's
`base-branch-ci` reconciler.

## Specs run

```sh
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test \
  api-scope-set-edit api-scope-sets capability-info-popovers \
  capability-policy-deep-link capability-policy-pickers capability-server-settings \
  host-script-settings git-connections-multihost-walkthrough \
  k8s-sandbox-settings-walkthrough sandbox-disk-overcommit \
  custom-sandbox-image-layout provider-connection-lifecycle-walkthrough
```

These were run on the locally patched build described above, with 2
workers. All 29 tests passed in 1.7 minutes.
