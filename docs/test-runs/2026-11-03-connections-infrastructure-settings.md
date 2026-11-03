# Test run — Settings → Git, Capabilities, Legacy capabilities, Engine, CI, Sandboxes and Caches (2026-11-03)

Task 2703 is a walkthrough of the Settings sections that connect grain to
outside systems and that tune the machines it runs on. It used a real
`grain demo` server, its built frontend and a real Playwright Chromium,
driven through visible controls.

**Result:** two UI bugs were found and fixed on this branch, and one
server-side gap was proposed as a follow-up (see [Bugs](#bugs)). Two
parts were not walked this time, Legacy capabilities and part of
Sandboxes; [Not covered](#not-covered) says what was left out.

## Environment

- **Commit tested:** `main` at `0fdeff3b` ("Merge pull request #540").
  The fixes were checked on this branch at `5bfe748a`, then `8408520c`.
- **Build:** `cd ui && npm ci && npm run build`, then
  `go build ./cmd/grain`, so the UI is embedded. The binary was rebuilt
  after each fix.
- **Servers.** Each was a `grain demo` over its own throwaway embedded
  store, with `TMPDIR` set to a scratch directory:
  - `:4390`: `-secrets-store -github-api 127.0.0.1:39083 -host-executor
    hostd -access-code-file …`. This was restarted twice, once on each
    fixed build. The restarts reset its store.
  - `:4391`: `-unconfigured`. This was used for the no-git-connection
    notices.
- **Browser:** Playwright Chromium from `/var/cache/playwright`, headless,
  1440×1000. It signed in by typing the access code on the sign-in page.
- **Recording:** each script logged `console` errors and warnings,
  `pageerror`, `requestfailed`, HTTP responses with status 400 or more,
  and every `window.confirm`.
- **Toolchain:** Debian, Go 1.26, Node v20.19.0.
- **Gemini:** `/home/debian/.gemini-api-key` is absent from this sandbox,
  so no model call was made.
- **Scripts:** ad hoc scripts in `/var/tmp/walk`, not committed.

## Stubs

| Boundary | How | Why |
| --- | --- | --- |
| GitHub REST API | `ui/e2e/githubstub.js` on `127.0.0.1:39083`, given to the demo as `-github-api`. `ghp_walkgood` is `@walk-bot` with push on `acme/widgets`; every other token gets 401. There are no `/app/...` routes and no `/branches` route. | The sandbox has no GitHub access. |
| `GET /api/settings/cli-run` (Models → hostd status only) | `page.route` returned the real answer, with `hostExecutor.hostd` given `running`, `current`, `currentBuild`, `updating`, `suggest: ["claude"]` and two `processes`, one of them `adopted`. | The demo's throwaway hostd never has an upgrade waiting, an adopted run or an allowlist suggestion. |

Not stubbed:

- the plain git host and SSH (real DNS failures came back);
- the capability servers (the real built-ins and a real `grain capability
  config-map` subprocess);
- host scripts (the real `grain capability host-script`);
- Kubernetes (only settings were saved; nothing was applied to a
  cluster).

## Git (`/settings/git`)

| Check | Outcome |
| --- | --- |
| Starting state | The table lists the demo's `github.com` row (`github`, `pat`, all repos, Test and Remove), then the Repositories binding and the commit author block. |
| GitHub App offered only for a GitHub host | With Kind = Plain git, the Auth type option "GitHub App" is `aria-disabled` with the title "A GitHub App needs a GitHub host: a plain git host has no App API". |
| Plain git, token, scope `walk-org`, proxy `http://proxy.walk.example:3128` | Saved. The row reads `git · pat · via proxy … · walk-org`. Test: "nothing was tested: … give the connection an owner/repo scope …". |
| Plain git, SSH key (throwaway ed25519) with known hosts, scope `walk/repo` | Saved. Test: the real `lookup ssh.walk.example … no such host`, with `✗ walk/repo`. |
| SSH key `not a key` | Refused (400): "the private key will not parse: ssh: no key found". The message appears both inline and in the snackbar. |
| GitHub token `ghp_walkgood`, scope `acme` | Test: "Token accepted as @walk-bot by github.com · scopes: repo · expires …", with "✓ acme/widgets read, push". |
| GitHub App, scope `acme/gadgets`, proxy, App ID 12345 / installation 67890 / throwaway RSA key | Saved with its proxy. Test: "no installation token could be minted …". This is expected, because the stub has no App routes. |
| Reload | All four throwaway rows come back with their kind, auth type, scope and proxy. |
| Remove asks first | `window.confirm` asks: "Remove the git.walk.example connection grain-git-c-…? A credential stored for it is deleted …". Dismissing it keeps the row; accepting removes it. The other rows were removed by focusing Remove and pressing Enter, and each confirmed first. After a reload only the demo's row is left. |
| Keyboard | Add connection is reached by Tab, 43 presses from the top. |
| No-git-connection notice (`:4391`) | Both Repos and New task show "This deployment has no git connection yet … a GitHub token or GitHub App for github.com, or a token, SSH key or ambient login for another git host." The links "Git connection in Settings" and "Settings → Git" open `/settings/git`. |
| Nameless connections in "Reuse authentication from" (Settings → Models → New connection → Claude → Continue) | Nameless Claude connections are listed by id ("Reuse authentication from backend-e82c…", "…backend-3c7e…"), so they can be told apart. |

## Capabilities (`/settings/capabilities`)

| Check | Outcome |
| --- | --- |
| Old link | `/settings/capability-servers` lands on `/settings/capabilities`. |
| List order | 10 servers. The built-ins come first (config-map, gchat, gcp, git, host-script, model-proxy, grain, repo), then the demo's two custom servers. config-map, gchat, gcp and host-script read `needs configuring`. |
| `(i)` popovers | With the keyboard: focus opens one, Escape closes it, and Enter and Space each toggle it. **A mouse click did not open it** (bug 1, fixed): after the fix a click opens it and a second click closes it. |
| config-map through its schema form | Config map `walk-config` (directory, description, key `settings.toml`, mode 0644, content) and secret map `walk-secrets` (key `token`, a throwaway value). Test server settings answered with 1 method, listing enums for the new maps. After Save and a reload the server reads `attached` and `token: set`, and the value is nowhere in the DOM. |
| Disable / Enable | Disable flips the chip to `disabled` 2 s later without a reload. Enable returns it to `attached` 3 s later. |
| Own secrets stay on the server | Under Legacy capabilities, `capability-server-1f6d92c4/secret_values` is under "Used by capability servers" → config-map, not among the unclaimed secrets ("No other secrets set."). |
| Throwaway custom server `https://walk.example/caps/throwaway` (stdio `grain capability config-map`, `WALK=1`) | Test in the add form answered with 0 methods and listed the settings it asks for. Add server opened its page. A secret map was saved through its schema form, and its secret was listed as used by this server. |
| Rename and edit in place | Edit connection → id `…/renamed`, `WALK=2`. A dialog asked "rename this capability server? … becomes …" first. The deep link moved to `…%2Frenamed`, and its secret followed it. Test then ran the saved entry: 1 method, with `walk-own/token`. |
| Holder link | Task #1 → Add grant… → `…/renamed#config-map`. The server's page lists "Task #1 “Add a dark mode toggle” on acme/widgets", and the link opens `/tasks/1`. |
| Remove | The dialog lists the holding task. Cancel keeps the server. Remove deletes it, and its secret goes with it: only config-map's is left under "Used by capability servers". Task #1 then shows the grant as "server not attached". |
| Host scripts | Added `walk-echo` (description, usage, script, timeout 5). It saved and attached. Run with the argument `a b; rm -rf /tmp/nothing`: `exit 2`, 2 ms, stdout `<a b; rm -rf /tmp/nothing>`, so the argument was passed literally. Timeout 5 → 7 was saved and reads `timeout 7s` after a reload. |
| Host process server (Models → where each CLI runs) | Real answer: "grain hostd is up at …/hostd.sock (build v0.0.0-…-0fdeff3b8161+dirty, hostd protocol 4), offering capability corp-search, cli agy". Under the stub, three things render: the processes ("agy for run 17-1 of task 42: pid 4242, running, detached, adopted by this daemon"), the installed build against the running one, and the allowlist suggestion ("Copy it from /etc/grain/hostd.yaml.default; grain never edits your allowlist."). |

## Capability policy and API scope sets

| Check | Outcome |
| --- | --- |
| Edit, save, reload | Rule 1 denies `…/cloud#cloud-viewer` to agent tasks. Rule 2 allows only `…/cloud#cloud-key` on `acme/gadgets`. Save policy is disabled after saving, and after a reload both rules read back (inputs and `GET /api/capability-policy`). |
| New task, Task #1, schedule (acme/widgets), repo `#ci`, repo Agents settings | `cloud-viewer` is `aria-disabled` with "the capability policy denies … to this task on acme/widgets (rule 1: deny …)". `cloud-key` is disabled with "does not admit … for this task" (or "for this ci job"). In repo `#ci`, `cloud-viewer` is offered, because rule 1 covers agent tasks only. |
| New template, deployment default grants (Settings → Tasks) | No repository is known, so neither is disabled. `cloud-viewer` is marked `denied` ("denied by the capability policy for agent tasks") and `cloud-key` is marked `may_be_refused` ("may be refused …"). |
| Link back to `#capability-policy` | Every verdict links to `/settings/capabilities#capability-policy`. **The link landed on the server list, with the policy card ~1600 px below the fold** (bug 2, fixed). After the fix the card is at y≈209 from a picker and y≈441 from a fresh load. |
| API scope set create / edit / remove | New scope set `walk/throwaway` (description, `tasks:read`) was saved and read back as "config repo · tables/api_scope_set.json". Editing the description and scopes (to `ci:read`) read back. Remove asked "remove scope set walk/throwaway? Nothing grants api:walk/throwaway." Cancel kept it; Remove deleted it, and a reload confirmed it was gone. |
| Remove when grants hold the set | **Could not be reached.** No grant picker offers a deployment-declared set: neither the demo's own `ops/triage` nor one made here. So nothing could hold one (follow-up below). |

## Engine

| Check | Outcome |
| --- | --- |
| Starting values | Poll interval `10s` (placeholder `10s`), Max worker agents `4`, Max merges per repo branch `1`. |
| Save and reload | `25s`, `6` and `2` read back. |
| Refused by name | Poll `0s`: "Poll interval must be positive". Workers `0`: "Max worker agents must be at least 1; 0 was not saved." Merges `0`: "… must be at least 1; 0 was not saved." |
| Blank resets | Poll interval → `10s` and merges → `1`. Workers → `10`, grain's default, but that box has no placeholder saying so (observation). |
| Sandbox commitment note | Not shown. `SandboxCommitmentNote` renders only when `settings.sandboxCommitment` is set, and the demo reports none. |

## CI

- **Cards:** CI queue, CI history, CI job cache and CI artifacts, 14
  boxes in all. Each box is blank with grain's default as its
  placeholder.
- **Saved and read back after a reload:**
  - jobs per run 3;
  - age boost `90s`, which reads back normalised as `1m30s`;
  - reserved after `45s`;
  - runs kept 777;
  - step output 32 MiB for 7 days, and 128 KiB per run;
  - job cache 300 MiB, 1000 entries, 9 days;
  - artifacts 1024 MiB for 5 days, 64 MiB per run, 16 MiB per file.
- **Refused:**
  - jobs `-1`: "must be at least 0; -1 was not saved.";
  - boost `soon`: "invalid duration" (400).
- **Cleared:** clearing all 14 left them blank again after a reload.

## Caches

- **Placeholders:** both boxes show `20`.
- **Saved and read back:** proxy cache 25 GiB and Actions cache 12 GiB,
  after a reload.
- **Refused:**
  - `-3`: "must be at least 0; -3 was not saved.";
  - `2.5`: "takes a whole number; 2.5 was not saved.".
- **Cleared:** clearing both left them blank after a reload.

## Sandboxes

- **Backend select:** these options are disabled, each with its reason:
  - `granule`: "needs SSH user/workspace/git proxy host for granule";
  - `host`: "needs -host-sandboxes at daemon startup";
  - `none`: "None (control plane only) — unavailable (needs
    -disable-native-sandboxes at daemon startup)".

  The select carries a `needs restart` chip.
- **Session sandbox idle TTL:** `45m` was saved and reads `45m0s` after a
  reload. `soon` was refused by name.
- **Auto CPU minimum:** `0` was refused: "must be at least 0.25; 0 was not
  saved."
- **Kubernetes card:** prefilled with these defaults:
  - namespace `grain`;
  - pod and VM images `ghcr.io/bwsalmon/grain/...`;
  - workspace `/home/agent`;
  - Deployment and container `grain`;
  - `kubectl`;
  - priority classes `grain-interactive`, `grain-agent` and `grain-ci`,
    each with the placeholder `none`.

  Each of these was refused by name:
  - namespace `Bad_NS!`: "is not a Kubernetes namespace name …";
  - CI priority class `Bad_Class!`: "is not a Kubernetes PriorityClass
    name …";
  - node selector `garbage`: "must be key=value".
- **Custom sandbox image card:** explains building `FROM` grain's minimal
  base image, `k8s-sandbox-base`, and the contract it already satisfies.

## Console and network

- **Console:** no `pageerror`s.
- **Expected `console.error`s:**
  - the 401 from the sign-in page before the code is typed;
  - the 400s for the refusals provoked above.
- **`404 GET /api/repos/acme/widgets/github-branches`:** on task pages,
  because the GitHub stub has no branches route.
- **`requestfailed` on Remove:** each Remove in Settings → Models logged a
  `DELETE /api/deployment/agent-backends/<id> net::ERR_ABORTED`, yet the
  connection was removed, as a reload confirmed.

## Bugs

### 1. A mouse click on an `(i)` info popover did not open it — fixed

- **Reproduction:**
  1. Open `/settings/capabilities/https%3A%2F%2Fcapabilities.demo.grain.example%2Fcloud`.
  2. Click an `(i)` beside a method.
  3. No tooltip appears; only a second click shows it.

  The press hovers and focuses the button, both of which open the
  popover, and then the click toggled it closed.
- **Fix** (`ui/src/components/InfoPopover.jsx`): a pointer click
  (`detail > 0`) pins the popover open, so leaving it with the pointer
  does not close it. A second click, blur or Escape closes it. Enter and
  Space still toggle it.
- **Regression tests:**
  - `InfoPopover.test.jsx` › "stays open on a mouse click, and a second
    click closes it" fails without the fix;
  - `e2e/capability-info-popovers.spec.js` gained a click case, which
    passes at 1440 and 390 px.

### 2. `#capability-policy` deep link landed on the server list — fixed

- **Reproduction:** open `/settings/capabilities#capability-policy`, or
  follow any picker's "Settings → Capabilities" verdict link. After load,
  `#capability-policy` is at top ≈ 1618 px in a 1000 px viewport.
  `SettingsPage` scrolled to the card as soon as it drew, and the server
  list then loaded above it.
- **Fix:** `ui/src/keepInView.js`, called from `SettingsPage`'s hash
  reveal. It holds the revealed card in place while the section draws.
  It lets go after 5 s, or at once when the reader scrolls, presses a key
  or clicks.
- **Regression tests:**
  - `keepInView.test.js`;
  - `e2e/capability-policy-deep-link.spec.js` (desktop and phone), which
    failed without the fix and passes with it.

### 3. Deployment-declared API scope sets are never offered to a picker — follow-up proposed

`pkg/daemon/ui_server.go` and `pkg/demo/demo.go` build
`ui.Config.Capabilities`, including `ui.APICapabilities()`, once at
startup. In the demo that happens even before `ops/triage` is declared.
A set made or changed later through Settings, or by an import, is
therefore never in `/api/config` or in any "Add grant…" menu, although
the card says sets are "granted as api:<name> through any capability
picker".

A unit check confirms `apiCapability("api:ops/triage")` is ok once the
set is installed, so only the snapshot is stale. The fix touches daemon
wiring and grant validation, so it was proposed as a separate task.

## Not covered

- **Legacy capabilities** (`/settings/credentials`): the GitHub host, the
  GCP project and service accounts, and the add, mask, replace and remove
  of a throwaway secret were not walked this run. They were walked on
  2026-11-02. Only the "Used by capability servers" grouping was checked
  (above).
- **Sandboxes:**
  - the network change;
  - the fixed overcommit ratios save;
  - the auto bounds above their maximums;
  - the disk floor;
  - saving a valid Kubernetes value and a custom image reference.

  The script for these stopped after the refusals above. No screenshots
  were taken of the Sandboxes page.

## Observations

- **Max worker agents has no placeholder.** A blank box saves grain's
  default, 10, but nothing in the box says so, unlike Poll interval.
- **A literal header is shown in plain text.** The demo's custom server
  shows its literal `Authorization: Bearer …` header on its page. This is
  by design: it is plain config, and Bind a secret is offered for real
  credentials.
- **Stale page name in the grants pane.** It says credentials "belong in
  Settings → Capability servers", but the page is now called
  Capabilities.
- **An abandoned new connection survives a closed tab.** Closing the
  browser on a new model connection after Continue leaves a disabled,
  nameless draft connection behind. A `beforeunload` prompt warns first,
  and in-app navigation discards the draft.
- **The default images still name `ghcr.io/bwsalmon/grain/...`.**

## Commands

```sh
cd ui && npx vitest run src/components/InfoPopover.test.jsx src/keepInView.test.js src/components/SettingsPage.test.jsx
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/capability-info-popovers.spec.js e2e/capability-policy-deep-link.spec.js
```

All passed: 3 + 3 + 36 unit tests and 4 e2e tests.
