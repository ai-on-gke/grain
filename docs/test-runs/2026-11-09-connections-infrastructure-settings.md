# Connections & Infrastructure Settings Walkthrough — 2026-11-09

## Scope & Environment

- **Base commit tested:** `8ea343b0` (`Merge pull request #1000 from ai-on-gke/grain/098073e4/task-3714-walk-through-task-intake-inbox`) on branch `grain/098073e4/task-3719-walk-through-settings-git`
- **Server & store:** Real compiled `grain demo -secrets-store` binary serving the production Vite bundle (`pkg/ui/static`) over throwaway per-run embedded SQLite stores, git-backed `grain/config` repositories, and colocated encrypted secrets stores
- **Browser & viewports:** Real Playwright Chromium (`1234`) at desktop (`1440×900`) and phone (`390×844`) viewports; Go `go1.26.10 linux/amd64`, Node `v20.19.0` (`/home/debian/.gemini-api-key` not present in sandbox, so `LOOK=1` visual narration was skipped)
- **Sections covered:**
  1. [Settings → Git](/settings/git)
  2. [Settings → Hosts](/settings/hosts) (and legacy alias `/settings/hostds`)
  3. [Settings → Capabilities](/settings/capabilities) (and legacy alias `/settings/capability-servers`)
  4. [Capability policy](/settings/capabilities#capability-policy) and [API scope sets](/settings/capabilities#api-scope-sets)
  5. [Deployment credentials](/settings/capabilities#credentials) (retired Legacy capabilities aliases `/settings/credentials`, `/settings/legacy-capabilities`, and `/settings/secrets`)
  6. [Settings → Engine](/settings/engine)
  7. [Settings → CI](/settings/ci)
  8. [Settings → Sandboxes](/settings/sandboxes) and [Settings → Caches](/settings/caches)

---

## Summary & Bug Found / Fixed

All eight connection and infrastructure settings areas were exercised end to end through visible browser controls, keyboard interactions, destructive-action confirmations, deep links, reloads, and responsive width checks (`0px` horizontal overflow at `390×844`, zero uncaught page errors, zero 5xx responses, and zero calls to the retired `/api/capabilities/{id}/check` endpoint).

### Bug 1 (Fixed in this branch): Creating, editing, or removing an API scope set left `GrantsPicker` menus stale until a full browser reload

- **Severity:** Medium (functional state staleness across task/template/schedule/repository grant pickers)
- **Deterministic reproduction (on `8ea343b0`):**
  1. Open [Settings → Capabilities → API scope sets](/settings/capabilities#api-scope-sets).
  2. Click **New scope set**, enter **Name** `walk3719/scopes`, **Scopes** `tasks:read, ci:read`, **Repositories** `acme/widgets`, and click **Save**.
  3. Without reloading the browser, click **+ New task** in the sidebar and click **Add grant…** in `new-task-grants`.
     - **Observed before fix:** `walk3719/scopes` was absent (`0` matches in `Grants to add`), even though `PUT /api/api-scope-sets` puts the declared set into effect immediately on the server.
  4. Reload the browser so `GET /api/config` loads `api:walk3719/scopes`, then click **Remove** on `walk3719/scopes` in `#api-scope-sets` and confirm **Remove**.
  5. Without reloading the browser, click **+ New task → Add grant…**.
     - **Observed before fix:** The deleted `walk3719/scopes` row was **still offered** (`1` match in `Grants to add`) despite granting nothing on the server.
- **Root cause:** `GrantsPicker` reads custom API scope sets and preset metadata from `config.capabilities` (`GET /api/config`), which `App.jsx` refreshes via `s.onSaved` (`refreshConfig`). In `ui/src/components/settings/CapabilitiesSection.jsx`, `CredentialsSection` called `await onSaved?.()` when secrets changed, but `ApiScopeSetsPanel` and `CapabilityPolicyEditor` were never passed `onSaved={s?.onSaved}`, `CapabilityServersPanel`'s `onChanged` callback only bumped `secretsSeq`, and `ApiScopeSetsPanel`'s `save()` and `remove()` never invoked `onSaved`.
- **Fix:**
  - Passed `onSaved={s?.onSaved}` from `CapabilitiesSection` to `ApiScopeSetsPanel` and `CapabilityPolicyEditor`, and invoked `onSaved?.()` from `serversChanged`.
  - Updated `ApiScopeSetsPanel.save()`, `ApiScopeSetsPanel.remove()`, and `CapabilityPolicyEditor.save()` to `await onSaved?.()` after persisting changes.
- **Regression coverage added:**
  - Unit tests in `ui/src/components/ApiScopeSetsPanel.test.jsx` (`onSaved` called on `PUT /api/api-scope-sets` and `POST /api/api-scope-sets/delete`, not called when **Cancel** dismisses the removal dialog) and `ui/src/components/settings/CapabilitiesSection.test.jsx` (`s.onSaved` forwarded to `ApiScopeSetsPanel`, `CapabilityPolicyEditor`, and `CapabilityServersPanel`).
  - Browser end-to-end assertions in `ui/e2e/api-scope-set-edit.spec.js` confirming that `+ New task → Add grant…` immediately offers a newly created scope set and immediately stops offering a removed scope set without reloading the page.

---

## Detailed Walkthrough Steps & Outcomes

### 1. Git (`/settings/git`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Table & pre-seeded row | Opened [Settings → Git](/settings/git); verified `github.com` token connection row and `Add connection` button. | Pass |
| Auth options per host | Clicked **Add connection**, entered plain host `git.walk3719.example`, opened **Auth** (`new-connection-auth`), and inspected options vs `github.com` / `ghe-app.walk3719.example`. | Pass — `GitHub App` is disabled on plain git hosts and enabled only when the host is `github.com` or GitHub Enterprise (`github*` / `ghe*` / configured `githubHost`). |
| Plain git host (Token) | Added `git.walk3719.example` with Personal access token (`glpat-throwaway-walk3719`) and clicked **Save connection**; clicked **Test** on the saved row. | Pass — saved cleanly; `Test` explained `nothing was tested: a credential on a plain git host is checked by reading a repository with it, and no repository on git.walk3719.example is in this check's reach`. |
| Plain git host (SSH key + `known_hosts`) | Tested invalid SSH key input first (`not-a-valid-ssh-private-key` refused with key parse error), then generated a throwaway Ed25519 keypair + `known_hosts` entry for `ssh.walk3719.example`, saved, and clicked **Test**. | Pass — invalid key refused; valid Ed25519 key + `known_hosts` saved and rendered `SSH key` badge; `Test` reported reachability/scope guidance cleanly without leaking key material. |
| GitHub connection (Token + Scope + Proxy) | Tested proxy URL carrying embedded credentials (`http://proxyuser:proxypass@proxy.walk3719.example:3128`) on `ghe.walk3719.example`, then saved with bare proxy `http://proxy.walk3719.example:3128` and scope `walk3719-org`. Also tested against a local mock GitHub HTTP server (`127.0.0.1:<port>`) with `githubInsecureHttp: true`. | Pass — proxy with login refused (`proxy "http://proxyuser:xxxxx@proxy.walk3719.example:3128" carries a login, and a host record is not a secret`, password masked); bare proxy + scope saved and persisted across reload; local mock GitHub PAT probe succeeded with `Token accepted as @walk3719-bot ... scopes: repo, workflow`. |
| GitHub connection (GitHub App + Scope + Proxy) | Added `ghe-app.walk3719.example` with `GitHub App` (`App ID` `371901`, `Installation ID` `99003719`, throwaway 2048-bit RSA PEM, scope `walk3719-app/widgets`, proxy `http://proxy.walk3719.example:3128`), saved, reloaded, and clicked **Test**. | Pass — row displayed `GitHub App`, scope, and proxy across reload; `Test` reported `no installation token could be minted from this GitHub App credential`. |
| Nameless reuse disambiguation | Created two nameless Claude backends (`walk3719-nameless-1`, `walk3719-nameless-2`) with `name: ""`, opened [Settings → Models](/settings/models) → **New connection** → `Claude` → **Continue** → **Authentication method**. | Pass — dropdown listed `Reuse authentication from walk3719-nameless-1` and `Reuse authentication from walk3719-nameless-2` distinctly by ID instead of colliding on framework name. |
| `Remove` confirmation & empty state notices | Focused **Remove** on `git.walk3719.example` and pressed `Enter`; dismissed `window.confirm` (`Remove the git.walk3719.example connection?`) and verified row remained; then accepted removal of all rows to reach `No git connections configured.` across reload. Visited [Repos](/repos) and opened **+ New task**. | Pass — both [Repos](/repos) and **+ New task** displayed the no-git-connection notice naming both **tokens** and **SSH keys** with a `Git connection in Settings` link navigating to [Settings → Git](/settings/git). |
| Config-repo-locked read-only link | Under config-repo lock, verified [Settings → Git](/settings/git)'s read-only secret note links to [Settings → Capabilities → Deployment credentials](/settings/capabilities#credentials) and scrolls the card into view. | Pass |

### 2. Hosts (`/settings/hosts` and `/settings/hostds`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Route alias & Invite hosts `--join` snippet | Navigated to `/settings/hostds` and `/settings/hosts`; verified `Hosts` heading, `default` and `workstations` enrollment code chips, and `--join ghe_...` install command in `invite-hosts-snippet`. | Pass |
| `New code` validations & creation | Clicked **New code**; verified invalid label (`Bad Label`) and invalid `Max uses` (`0`) disabled/refused save; created `walk3719-pool` (`Expires in` `48h`, `Max uses` `5`, `Default capacity` `3`, `Default labels` `gpu=l4, os=linux`, `Allowed principals` `ops@example.com, group:sre@example.com`) and reloaded. | Pass — chip `walk3719-pool` persisted across reload, updated `--join` snippet when selected, and rendered expiry, `0/5 uses`, capacity, labels, and allowed principals. |
| `Rotate code` & `Revoke code` | Clicked **Rotate code** on `walk3719-pool` (confirming `--join ghe_...` secret changed while preserving metadata), then clicked **Revoke code** and reloaded. | Pass — revoked code disappeared immediately and stayed absent after reload. |
| Pending host requests (`Accept` & `Reject`) | Verified demo's pending `devbox-carol.example.com` request showed verified enrollee `carol@example.com`, `SHA256:...` Ed25519 key fingerprint, and **no pairing code**; edited `Host name` (`carol-gpu-box`), `Labels` (`os=linux, tier=canary`), and `Capacity` (`6`) before clicking **Accept**; in a clean run clicked **Reject** and reloaded. | Pass — `Accept` moved `carol-gpu-box` into the Hosts table with `key` auth badge (no legacy `Rotate`/`Install` token buttons) and persisted across reload; `Reject` removed the pending request across reload. |
| Legacy per-host token registration & non-clobbering banners | Registered `walk3719-legacy` via legacy per-host form (`Owner` `alice@example.com`, `Labels` `arch=amd64`, `Capacity` `2`); clicked **Rotate** on another legacy host while **Install** was also opened (`hostd-issued-token` + `hostd-install-again`). | Pass — one-time issued token banner (`hostd-issued-token`) and generic install reminder (`hostd-install-again`) rendered side by side without clobbering the unclosed token. |
| `local` row, `Edit`, `Rename`, `Revoke`, `Delete` & `Delete all revoked` | Verified `local` row; edited owner/labels/capacity on `walk3719-legacy`; tested `Rename` to reserved name `local` (refused inline, `Cancel` cleared error) then renamed to `walk3719-legacy-renamed`; clicked **Revoke**; tested **Delete** confirm dismiss (kept row) and accept (deleted row); pinned a model backend to revoked `walk3719-ref` and clicked **Delete all revoked**. | Pass — `Delete all revoked` removed unreferenced revoked hosts and reported `Skipped walk3719-ref: ... walk3719-pinned-backend` so referenced host records were preserved. |

### 3. Capabilities (`/settings/capabilities` and `/settings/capability-servers`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Alias, deprecation notices & retired check route | Opened `/settings/capability-servers`; verified `Capabilities` heading, all 7 in-process retirement notices (`gcp-key`, `model-proxy`, `gemini-key`, `release`, `grain-ci`, `bootstrap-playbooks`, `github-sandbox`), zero requests to `/api/capabilities/*/check`, and `404`/`405` on `POST /api/capabilities/foo/check`. | Pass |
| Built-in servers first, `needs configuring`, `Disable` without reload | Verified built-in rows (`config-map`, `figma`, `gchat`, `gcp`, `git`, `host-script`) appear ahead of custom servers, unconfigured built-ins show `needs configuring`, built-in detail cards omit **Remove**, and clicking **Disable** flips `capability-server-state` to `disabled` immediately **without reload** (and **Enable** restores it). | Pass |
| `(i)` info popovers (mouse + keyboard) | On `About cloud-key`, tested mouse click (confirmed tooltip stayed pinned open when moving pointer to `(10, 10)`), `Escape` to close, keyboard focus + `Enter` to toggle open, `Escape` to close, and `Space` to toggle open. | Pass |
| Throwaway HTTP server lifecycle, secret isolation & holder link | Added `https://walk3719.example/caps/cloud-a` (using demo capability server's URL + bearer token), ran **Test** before saving (`2 methods`), saved, tested schema validation (`"Invalid Project!" does not match`), saved `walk3719-proj` + write-only `apiToken`, tested in place, edited headers in place (`X-Walk-3719=yes`), granted `#cloud-viewer` to a task, renamed in place to `https://walk3719.example/caps/cloud-b` (dialog listed holder task), clicked holder link to `/tasks/<id>`, then clicked **Remove** (tested **Cancel**, then confirmed **Remove**). | Pass — server's `$secret:capability-server-...` entry stayed under **Used by capability servers** (never **Other secrets**, value never returned by `GET /api/secrets`), followed the rename to `cloud-b`, navigated cleanly to the holder task, and was automatically deleted from both `GET /api/secrets` and the UI on **Remove**. |
| `config-map` & `secret-map` file placements | Opened built-in `config-map`, added throwaway config map `walk3719-cm` (`settings.ini`) and secret map `walk3719-sec` (`api.key`, `type="password"`), saved, reloaded, verified `/home/debian/.config-map/walk3719-cm` and `api.key: set` summary, then removed throwaway entries. | Pass |
| `host-script` server & host process status | Added throwaway stdio server `grain capability host-script`, verified `timeout: 0` validation refusal, saved `walk-echo` (`timeout: 10`), edited description in place, reloaded, and ran **Test** with `alpha beta` and `$(echo not-expanded)` before removing the throwaway server. Verified host process status card (`#cli-run` on [Settings → Models](/settings/models#cli-run)) displays live version vs installed version (`v1.1.0` draining onto `v1.2.0`), allowlist suggestion (`Suggested allowlist entry: cli claude`), and adopted detached runs (`adopted by this daemon`). | Pass — argv arguments passed literally (`[alpha beta]`, `[$(echo not-expanded)]`) with zero shell expansion. |

### 4. Capability Policy (`#capability-policy`) & API Scope Sets (`#api-scope-sets`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Capability policy save, preview & deep link | Deep-linked to [Settings → Capabilities → Capability policy](/settings/capabilities#capability-policy); added `Deny` rule on `cloud-viewer` (`Agent tasks`, `Processes`) and repo-conditional `Allow only` rule on `cloud-key` (`acme/gadgets`); clicked **Save policy**, inspected `policy-preview` (`refuse 1 existing grant`), clicked **Save anyway**, and reloaded. | Pass |
| Verdicts & `#capability-policy` links across all 5 pickers | Checked **+ New task**, **Templates** ([Processes](/processes) → `+ New` → `Template`), **Schedules** ([Processes](/processes) → `+ New` → `Schedule`), **Task pane** (`/tasks/<id>`), and **Repository defaults & CI grants** (`/repos/acme/widgets` **CI** and **Agents** tabs). Clicked `Settings → Capabilities` verdict link on a disabled `denied` row in `repo-default-grants`. | Pass — repo-bound pickers disabled `denied` rows (`aria-disabled="true"`) with rule citations; repo-less template/schedule pickers badged `denied` and `may_be_refused` (`data-policy-verdict`); clicking `Settings → Capabilities` navigated back to `/settings/capabilities#capability-policy`. |
| API scope sets create, edit, live picker sync & holder-aware `Remove` | Tested reserved `grain/` name refusal (`grain/reserved-test`), created `walk3719/scopes` (`tasks:read, ci:read` @ `acme/widgets`), confirmed immediate visibility in **+ New task → Add grant…** without reload (after Bug 1 fix), edited scopes/description, attached `api:walk3719/scopes@acme/widgets` to a task, verified **Remove** warned `1 grant holds api:walk3719/scopes`, clicked **Cancel**, detached the task grant, confirmed `Nothing grants api:walk3719/scopes.`, removed, and verified immediate removal from **+ New task → Add grant…** without reload and across reload. | Pass (after Bug 1 fix) |

### 5. Legacy Capabilities / Deployment Credentials (`/settings/credentials`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Legacy route aliases & helper links | Visited `/settings/credentials` and `/settings/legacy-capabilities` (redirected to `/settings/capabilities#credentials`) and `/settings/secrets` (redirected to `/settings/capabilities#other-secrets`, clear of sticky header). Clicked `Other secrets` and `gcp capability server` links inside the card. | Pass |
| GitHub host & GCP settings | Tested `githubHost` URL refusal (`https://ghe.example.com`) and valid `ghe.walk3719.example:8443` save; saved `gcpProject` (`walk3719-gcp-proj`); tested `gcpServiceAccountEmail` invalid refusal (`not-an-email`) and valid save; tested `gcpServiceAccountCount` `0` refusal (`at least 1`), saved `3`, reloaded, and verified blank-to-reset back to `1`. | Pass |
| Throwaway secret lifecycle | Under **Other secrets**, verified `Value` uses `type="password"`, added `walk3719-secret/token`, added second key `extra`, replaced `token` in place, verified `GET /api/secrets` never returns secret values, dismissed then accepted `window.confirm` when deleting `walk3719-secret/extra`, and focused **Delete secret** + pressed `Enter` + accepted confirm to remove the whole secret across reload. | Pass |

### 6. Engine (`/settings/engine`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Polling & Merge queue validations, save & blank-to-reset | Opened [Settings → Engine](/settings/engine); confirmed `Poll interval` placeholder `10s`; verified invalid duration (`not-a-duration`), zero duration (`0s`), `<1` `Max worker agents` (`0`), and `<1` `Max merges per repo branch` (`0`) each refused by field name; saved `25s` (via `Enter`), `6`, and `3`; reloaded; cleared all three fields to blank and verified reset to `10s`, `10`, and `5`. Verified `SandboxCommitmentNote` warning alert under Merge queue (`14 vCPU`). | Pass |

### 7. CI (`/settings/ci`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Deep link, 4 cards validation, save, reload & reset | Deep-linked to [Settings → CI → CI artifacts](/settings/ci#ci-artifacts) (scrolled into viewport); verified negative/invalid refusals on `ciJobsPerRun` (`-2`), `ciAgeBoost` (`not-dur`), `ciCacheMb` (`-5`), and `ciArtifactMb` (`-3`); saved non-default values across **CI queue** (`4`, `45s`, `3m0s`), **CI history** (`-1`, `512`, `21`), **CI job cache** (`2048`, `10`), and **CI artifacts** (`1024`, `7`, `128`, `32`); reloaded and verified both UI inputs and `GET /api/settings`; cleared to blank to verify reset to defaults. | Pass |

### 8. Sandboxes (`/settings/sandboxes`) & Caches (`/settings/caches`)

| Area / Gesture | Steps & Verification | Outcome |
| --- | --- | --- |
| Backend options, size, network & session idle TTL | Opened [Settings → Sandboxes](/settings/sandboxes); verified unavailable backends (`granule`, `host`, `none`) are shown disabled with explicit startup/host reasons (`needs SSH user/workspace/git proxy host for granule`, `needs -host-sandboxes at daemon startup`, `needs -disable-native-sandboxes at daemon startup`) and verified `None (control plane only)` description; tested negative CPU (`-1`), too-small memory (`10` < `128`), and invalid TTL (`nope`) refusals; saved `4` vCPU, `6144` MiB, `30` GiB, `none` network, and `45m0s` session sandbox idle TTL across reload. | Pass |
| Sandbox budget (fixed & auto overcommit) & disk floor | Tested out-of-range auto bounds refused by field name: `Auto CPU minimum` `0.1` (`< 0.25`), `Auto CPU minimum` `8` (`cpuMin 8.00 is above cpuMax 6.00`), `Auto memory maximum` `0.5`, `CPU pressure target (%)` `150` (`> 100`), `Host memory reserve` `-1`, and `Disk floor` `1.5` (`cannot be more than 1`). Switched mode to `auto`, saved valid bounds/targets (`1.5`..`5`, `1.2`..`2.5`, `40%`, `8%`), `2048` MiB reserve, `0.2` disk floor share, and `8192` MiB minimum across reload; verified clearing `Disk floor minimum` to blank resets to `5120` MiB while explicit `0` saves `0`. | Pass |
| Kubernetes card & Custom sandbox image card | Verified `needs restart` badges and prefilled defaults (`grain` namespace, `grain-interactive`, `grain-agent`, `grain-ci` pod priority classes); verified validation refusals on `Bad_NS` namespace, `pool` nodeSelector (missing `=`), `Bad Class` PriorityClass, and invalid OCI image refs (`not an image ref`, `::bad::`); saved valid overrides across reload; verified `CustomSandboxImageCard` renders Dockerfile snippet (`FROM docker.io/grainai/sandbox-k8s-base`), contract label (`dev.grain.sandbox.contract`), unverified-contract notice, and mirror repository alert (`k8s-granule-image-mirror`). | Pass |
| Caches (`/settings/caches`) | Opened [Settings → Caches](/settings/caches); verified negative `packageCacheSizeGb` (`-3`) and fractional `actionsCacheSizeGb` (`1.5`) refusals; saved `15` GB proxy cache and `12` GB Actions cache, reloaded, verified persistence; toggled `Enable GitHub Actions cache` off and back on across reload (confirming `actionsCacheSizeGb` hides when disabled and restores `12` when re-enabled); cleared both to blank to reset to `0` (unlimited). | Pass |

---

## Stubbed / Intercepted External Boundaries

Every mutation and persistence check ran against the real `grain demo -secrets-store` server, real `grain capability host-script` subprocess, real demo capability server, and real Playwright Chromium browser. Only external systems or startup-only hardware shapes absent in `grain demo` were stubbed, and each route was immediately unrouted after its check:

| Boundary / Route | Why Stubbed | What Was Stubbed |
| --- | --- | --- |
| `http://127.0.0.1:<ephemeral>` (`GET /rate_limit`, `GET /user`, `GET /user/repos`) | Sandbox has no outbound credentials for `api.github.com` | Local Node HTTP server returning `200` JSON (`{"login":"walk3719-bot"}` with `X-OAuth-Scopes: repo, workflow`) paired with `githubInsecureHttp: true` to exercise a positive GitHub PAT `Test` check end to end through `githubtoken.Probe`. |
| `GET /api/settings/cli-run` (browser route enrichment on `/settings/models#cli-run`) | Sandbox runs without a live upgrading systemd `grain hostd` daemon holding detached runs | Fetched the real response and enriched `hostExecutor` (`kind: "hostd"`, `build: "v1.1.0"`, `currentBuild: "v1.2.0"`, `suggest: ["cli claude"]`, and an `adopted: true` process) to verify version drain, allowlist suggestion, and adopted-run rendering. |
| `GET /api/settings` (temporary browser route enrichment on `/settings/engine`, `/settings/sandboxes`, and `/settings/git`) | `grain demo` commits 0 active sandboxes, starts with native sandboxes enabled rather than `-disable-native-sandboxes`, and has `configRepoOnly: false` by default | Fetched the real `/api/settings` payload and temporarily overlaid (a) a non-zero `sandboxCommitment` warning (`14 vCPU`), (b) `sandboxBackend: "none"` + `k8sGranuleImageMirror`, and (c) `configRepoOnly: true` to verify `SandboxCommitmentNote`, the control-plane-only backend copy, the mirror hint banner, and the read-only Git link to `#credentials`. |
