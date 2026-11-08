# Connections & Infrastructure Settings Walkthrough (Task 3463)

- **Date:** 2026-11-08
- **Commit tested:** `ad6d8b91` (`Merge pull request #933 from ai-on-gke/grain/098073e4/task-3359-walk-through-settings-git`)
- **Branch:** `grain/098073e4/task-3463-walk-through-settings-git`
- **File naming note:** `docs/test-runs/2026-11-08-connections-infrastructure-settings.md` (task 3163) and `docs/test-runs/2026-11-08-connections-infrastructure-settings-task-3359.md` (task 3359) already exist on `main` for the same date; following the task-suffixed convention from commit `40029d00`, this run's report is recorded under `-task-3463.md` so earlier walkthrough records remain intact.
- **Environment:**
  - Linux (`debian`, x86_64) sandbox VM; `/home/debian/.gemini-api-key` checked (not provisioned in this sandbox).
  - Real `grain demo` server (`./cmd/grain`) running over a throwaway embedded store with its demo HTTP capability server (`echo`), seeded host registry/enrollment codes/pending join request, and seeded git/capability/task fixtures.
  - Real production frontend bundle (`cd ui && npm run build`) opened in headless Chromium via Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), exercised through visible UI controls, native keyboard events (`Tab`, `Enter`, `Space`, `Escape`), native confirmation dialogs, full page reloads (`page.reload()`), and deep links, plus desktop (`1440×900`) and phone (`390×844`) light/dark layout checks.

---

## Boundary Stubs & Intercepted Routes

Every mutation and persistence check ran end-to-end against the real `grain demo` server and embedded SQLite store. Browser route interception (`page.route`) was used **only** for read-only status/capability surfaces that require external infrastructure (`grain hostd` daemon on a remote socket, a Kubernetes cluster, or an unseeded empty deployment) that `grain demo` does not carry:

| Area | Intercepted route | What was stubbed and why |
| --- | --- | --- |
| Git — no-connection notice | `GET /api/config` | Passed the real response through with `deployment.hasGitCredential = false`, `deployment.gitCredential = ""`, and `deployment.gitConnections = []` (because `grain demo` seeds an ambient `github.com` connection) to verify `NoGitConnectionNotice` (`data-testid="no-git-connection"`) on `/repos` and in `+ New task`. |
| Capabilities — host process runtime state | `GET /api/settings/cli-run` | Augmented the real response with `hostExecutor` (`kind: "hostd"`, `hostAvailable: true`, `hostd.build: "walk-build-1"`, `currentBuild: "walk-build-2"`, `processes: [{ pid: 4242, taskId: "301", tool: "host-script#walk-hello" }]`, `suggest: ["/usr/bin/walk-tool"]`) because no live external `grain hostd` binary is attached in demo mode. Host script creation, schema validation, editing, and argv `Test` ran unstubbed against the real server. |
| Engine — merge-queue sandbox commitment note | `GET /api/settings` | Augmented `sandboxCommitment` (`cpusPerSandbox: 2`, `memoryMbPerSandbox: 4096`, `cpusCommitted: 6`, `memoryMbCommitted: 12288`, `cpuRatio: 0.75`, `memoryShare: 0.75`, `runs: 3`) on one read to render `SandboxCommitmentNote` under `Max merges per repo branch`. All Engine saves, validations, and reloads ran unstubbed. |
| Sandboxes — control-plane-only backend & image mirror hint | `GET /api/settings` | On two read-only checks, augmented `sandboxBackend: "none"` + `sandboxBackends` (with `"none"` available) to verify the `None (control plane only)` isolation description, and `k8sGranuleImageMirror: "us-docker.pkg.dev/acme/mirror/granule:latest"` to verify the custom sandbox image mirror hint. All Sandboxes saves, overcommit/disk/Kubernetes/image validations, and reloads ran unstubbed. |

No GitHub, git-host, secret-validation, or Kubernetes write endpoints needed stubbing: `grain demo` natively validates SSH keys, PEM RSA keys, durations, proxy URLs, Kubernetes RFC-1123 names/labels/PriorityClasses, image references, and runs `Test` against plain git hosts, `github.com` (returning real 401 token/JWT rejection), its local HTTP capability server, and local `host-script` execution.

---

## Coverage & Outcomes

### 1. Git (`/settings/git`)
- **Initial table & seeded state:** Verified `/settings/git` renders the connections table with the seeded `github.com` row and commit-identity summary. No console errors or 5xx responses.
- **Plain git host — auth type gating & PAT:** Opened `Add connection`, set host `git.walk3463.example`, switched `Kind` to `Plain git`, opened `Auth type`, and confirmed `GitHub App` is disabled (`aria-disabled="true"`) with tooltip explaining `"A GitHub App needs a GitHub host: a plain git host has no App API"`. Saved a PAT connection, clicked `Test`, and verified the plain-host check notice (`nothing was tested: a credential on a plain git host is checked by reading a repository with it...`).
- **Plain git host — SSH key + `known_hosts` & refusal banner dismissal:** Opened `Add connection` for `ssh.walk3463.example` (`Plain git`, `SSH key`). Submitted an unparseable key (`not-a-valid-key`), confirmed 400 refusal (`the private key will not parse`) in both inline alert and snackbar banner, replaced with a generated Ed25519 private key and matching `known_hosts` entry, saved, and verified the refusal snackbar dismissed immediately (`< 2s`) and `Test` succeeded.
- **GitHub PAT & GitHub App with scope and proxy:**
  - Added a `GitHub` PAT connection scoped to `walk3463-org` with proxy `http://proxyuser:secretpass@127.0.0.1:8888`; verified the server refused embedded proxy credentials with password masked (`http://proxyuser:xxxxx@127.0.0.1:8888`). Corrected proxy to `http://127.0.0.1:8888`, saved, verified scope `walk3463-org` and proxy in the table, and ran `Test` (real `github.com` rejected the throwaway token cleanly without unhandled errors).
  - Added a `GitHub App` connection (`App ID` `123456`, `Installation ID` `654321`, generated 2048-bit RSA PEM key, scope `walk3463-app/widgets`, proxy `http://127.0.0.1:8889`), saved, ran `Test`, reloaded `/settings/git`, and verified all throwaway rows persisted.
- **Nameless connections distinguishable in `Reuse authentication from`:** Added two nameless plain-host connections (`walk-nameless-a.example` and `walk-nameless-b.example`), navigated to `/settings/models`, opened `Reuse authentication from`, and confirmed both nameless connections are distinctly labeled by host/auth kind rather than collapsing into blank entries.
- **Remove confirmation & keyboard reachability:** Focused `Remove` and activated via keyboard `Enter`; dismissed `window.confirm` first (confirmed row stayed), then accepted `window.confirm` on each throwaway connection and verified removal persisted across reload.
- **No-git-connection notice (`New task` and `Repos`):** With `GET /api/config` stubbed to hold zero git connections, visited `/repos` and opened `+ New task`; verified `data-testid="no-git-connection"` names personal access tokens and SSH keys and links directly to `/settings/git`.

### 2. Hosts (`/settings/hosts` and `/settings/hostds`)
- **Routing & deep link:** Verified both `/settings/hosts` and legacy `/settings/hostds` open the Hosts section cleanly.
- **Invite hosts (`#invite-hosts`):**
  - Verified the shared `--join` command (`curl -fsSL .../hostd/install | sh -s -- --join ghe_...`) and `Copy invite command` button, plus seeded `default` and `workstations` (`Uses: 1`, `Default capacity: 4`, `Labels: os=linux,pool=workstation`, `Allow: group:eng@example.com`) enrollment code chips.
  - Opened `New code`, tested invalid `Expires in` (`bad-duration` -> 400 refusal), then created `walk-gpu-pool` with `Expires in: 7d`, `Max uses: 5`, `Default capacity: 3`, `Default labels: arch=arm64,tier=gpu`, and `Allowed principals: user:dana@example.com, org:acme`. Verified chips (`Uses: 0/5`, `Default capacity: 3`, `Labels: arch=arm64,tier=gpu`, `Allow: user:dana@example.com`, `Allow: org:acme`, `Expires: ...`), reloaded `/settings/hosts`, and confirmed persistence.
  - Clicked `Rotate code` (verified `--join ghe_...` secret changed while preserving metadata) and `Revoke code` (verified chip switched to `walk-gpu-pool (revoked)` with warning alert and no active snippet).
- **Pending host requests (`#pending-hosts`):**
  - Inspected seeded `devbox-carol.example.com (linux/amd64)`: verified `code: workstations` chip, verified enrollee `carol@example.com (local user carol)`, CLIs, version, `SHA256:...` Ed25519 key fingerprint, **no** pairing code rendered (`pending-pairing-code-*` absent), and editable `Host name`, `Host labels`, and `Host capacity` fields.
  - Exercised `Accept` (moving the host into `Hosts` as key-bound `SHA256:...` `via workstations` with no `Install`/`Rotate` buttons) and filed a second throwaway join request via `POST /api/hostd/enroll` to exercise `Reject` (confirming removal from Pending without adding a host row).
- **Hosts table (`#hostds`) & legacy token registration (`#add-hostd`):**
  - Verified `local` (co-located, `Edit` only — no `Rename`, `Install`, `Rotate`, `Revoke`, or `Delete` and no missing-owner IAP warning), key-bound rows, legacy token rows (`cloudtop-alice`, `build-runner` showing `None: needed to dial in through IAP`), and revoked row (`old-laptop` offering `Delete`).
  - Registered a throwaway legacy per-host token host (`walk-legacy-1`) via `Add host`, verified one-time `ghd_...` token alert and `--name walk-legacy-1` install snippet (never embedding the token in the command), clicked `Install` (re-displaying instructions without token and suggesting `grain host rekey`), and clicked `Rotate` (issuing a fresh one-time `ghd_...` token).
  - Exercised `Edit` (owner, labels, capacity — finding and fixing Bugs 1, 2, and 3 below), `Rename` (`walk-legacy-1` -> `walk-legacy-renamed`), `Revoke`, single-row `Delete` behind `window.confirm` (decline keeps, accept deletes and shows `Deleted host ...`), and `Delete all revoked` with one unpinned revoked host and one revoked host pinned by a throwaway model connection (`walk-pin-conn`), confirming the unpinned host was deleted while the pinned host was kept and listed under `Skipped ...` in `hostd-delete-notice`.

### 3. Capabilities (`/settings/capabilities` and `/settings/capability-servers`)
- **Legacy URL & server ordering:** Navigated to `/settings/capability-servers` and verified it resolves to Capabilities with built-in capability servers listed first, full-width, badged `built-in`, and disable-only (no `Remove` button). Confirmed `figma` shows `needs configuring` until configured.
- **Live Disable/Enable without reload:** Clicked `Disable` on a built-in server and verified its state chip flipped immediately to `disabled` in place without a page reload; clicked `Enable` and verified it returned to active state.
- **`(i)` info popovers (mouse & keyboard):** Opened capability `(i)` info popovers by mouse click and via keyboard (`Tab` focus + `Enter` and `Space`), confirmed popover content fit cleanly inside both `1440px` and `390px` viewports, and closed with `Escape`.
- **Throwaway HTTP capability server (`walk3463` -> `walk3463-v2`):**
  - Added throwaway server `walk3463` pointing at demo's real local capability server endpoint with schema fields including a `writeOnly` secret (`apiToken`). Verified invalid config refusal first, then saved valid config.
  - Checked `/settings/credentials` and `/api/secrets`: confirmed the server's secret is grouped under `Used by capability servers` rather than unclaimed `Other secrets`.
  - Granted `walk3463` to a throwaway task, returned to `/settings/capabilities`, verified the holder link (`Task #...`), and clicked it to navigate directly to `/tasks/<id>`.
  - Renamed `walk3463` in place to `walk3463-v2`, edited its config, ran `Test` in place (passing against the live server), removed the task grant, clicked `Remove` (confirming prompt), and verified both the server and its `apiToken` secret were cleaned up from `/api/secrets`.
- **File placements (`config-map` and `secret-map`) & `host-script`:**
  - Configured throwaway entries on built-in `config-map` (both inline config map file placement and secret-backed `secret-map` file placement), saved, reloaded, verified read-back, and restored clean state.
  - Configured a throwaway `walk-hello` script on the `host-script` stdio server (`echo "hi:$1"`), saved, ran `Test` with argv `["world"]`, and verified live stdout `hi:world`.
  - With `GET /api/settings/cli-run` augmented for `hostExecutor`, verified the host agent version/pending upgrade (`walk-build-1` -> `walk-build-2`), adopted detached run (`pid 4242`), and binary allowlist suggestion (`/usr/bin/walk-tool`).

### 4. Capability Policy & API Scope Sets (`#capability-policy`, `#api-scope-sets`)
- **Policy verdicts across pickers & deep links:** Saved a capability policy rule marking one capability `denied` and a repo-conditional rule (`may_be_refused`), reloaded `/settings/capabilities#capability-policy` (confirming anchor scroll stability after server list load), and verified `+ New task`, task templates, schedules, task pages, and repository default capability pickers reflect `denied` and `may_be_refused` badges/reasons and link back to `/settings/capabilities#capability-policy`. Restored original policy afterward.
- **API scope sets (`#api-scope-sets`):** Created throwaway scope set `walk3463-scopes` (`tasks:read, ci:read`), edited its scopes and description, reloaded to confirm persistence, granted `api:walk3463-scopes` to a throwaway task, clicked `Remove`, and confirmed the MUI confirmation dialog named the holding task (`Cancel` kept the scope set; removing after clearing or confirming removed it cleanly).

### 5. Legacy Capabilities (`/settings/credentials`)
- **GitHub host & GCP settings:** Exercised `GitHub host` (exact field and sync checkbox), GCP project ID, and GCP service account fields, plus help links to the `gcp` capability server and `Other secrets`; saved, reloaded, verified read-back, and restored defaults.
- **`Other secrets` lifecycle:** Added throwaway secret `walk3463-dummy-secret` (`placeholder-not-real-1`), verified the input uses `type="password"` and the stored value is never echoed in the DOM or `/api/secrets` response, replaced its value in place (`Replace`), and deleted it behind confirmation.

### 6. Engine (`/settings/engine`)
- **Polling & Merge queue:** Verified `Poll interval` shows placeholder `10s` when unset, saved `25s`, reloaded and confirmed `25s`, tested invalid duration (`not-a-duration` -> 400 refusal), and blanked `Poll interval` to confirm reset back to `10s`.
- **Worker & merge bounds:** Verified `Max worker agents` refuses `0` (out-of-range), saves `4`, and resets on blank; verified `Max merges per repo branch` refuses `-1`, saves `3`, resets on blank, and renders the `SandboxCommitmentNote` (`3 runs · 6 CPUs · 12 GiB committed`) when `sandboxCommitment` is present.

### 7. CI (`/settings/ci`)
- **Cards exercised:** Exercised all 14 settings across **CI queue** (max concurrent jobs, queue limit, default job timeout), **CI history** (retention age/max runs), **CI job cache** (enabled/TTL/size), and **CI artifacts** (retention/max size).
- **Validation & persistence:** Verified negative numbers are refused, valid values save and read back identically after `page.reload()` (`"4m"` normalized to `"4m0s"` by Go `time.Duration`), and blanking resets fields cleanly.

### 8. Sandboxes (`/settings/sandboxes`)
- **Backend & sizes:** Verified unavailable backends appear as `<option disabled>` with explicit reasons (e.g. `None (control plane only) — unavailable (needs -disable-native-sandboxes at daemon startup)`), verified the `None (control plane only)` warning copy via stubbed `GET /api/settings`, and verified `Session sandbox idle TTL` saves and normalizes (`"45m"` -> `"45m0s"`).
- **Sandbox budget (fixed & auto overcommit) & disk floor:** Saved fixed CPU/memory/disk overcommit ratios (confirming negative ratio refusal), switched to `auto` overcommit and confirmed an inverted bound (`cpuMin = 12.00 > cpuMax = 6.00`) is refused naming `cpuMin`/`cpuMax`, and verified `Disk floor share` and blanking `Disk floor minimum (MiB)` resets to `5120` rather than `0`.
- **Kubernetes card & custom sandbox image:** Verified prefilled Kubernetes defaults (`grain-sandboxes`, runtime class, service account), field validation on invalid RFC-1123 namespace, node selector labels, and pod `PriorityClass` fields (including clearing), plus custom sandbox image `contract = 2` guidance, invalid image reference refusal, unverified image save/clear, and `k8sGranuleImageMirror` hint rendering.

### 9. Caches (`/settings/caches`)
- **Proxy & Actions caches:** Saved proxy cache (`18` GiB) and Actions cache (`14` GiB) maximum sizes and enable toggles, reloaded `/settings/caches` to verify read-back, confirmed negative size (`-3`) is refused by field name, and restored initial defaults.

---

## Bugs Found & Fixed

All three bugs found during the walkthrough were in `Settings → Hosts` (`ui/src/components/settings/HostdsSection.jsx` and `ui/src/components/SettingsPage.jsx`) and were fixed in this branch with unit and Playwright E2E regression tests:

### Bug 1: Clearing a host's `Capacity` in `Hosts → Edit` or `Pending → Accept` failed to clear it
- **Reproduction:**
  1. Register or edit a host so it has `cap=6` (`PATCH /api/hostds/<name>` with `{"capacity": 6}`), or view a pending join request enrolled under a code with `default_capacity: 4` (`workstations`).
  2. In `Hosts`, click `Edit` on the host, clear `Capacity` (`"6"` -> `""`), and click `Save`; or in `Pending host requests`, clear `Host capacity` (`"4"` -> `""`) and click `Accept`.
- **Observed:** `HostdsSection.jsx` guarded `body.capacity` behind `String(...).trim() !== ""`, omitting `capacity` from the JSON payload when blank. On `PATCH /api/hostds/{name}`, `UpdateHostdDetails` treats `Capacity == nil` as "no change", leaving `cap=6` stuck on the host with no way to return to unlimited (`0`) unless the user knew to type `"0"`. On `POST /api/hostds/pending/{id}/accept`, `AcceptHostJoinRequest` treats `Capacity == nil` as "fall back to enrollment code's `default_capacity`", silently assigning `cap=4` after the owner explicitly cleared the field.
- **Fix:** Parse blank `Capacity` in `save()` and `Host capacity` in `acceptPending()` as `0` (`parseNonNegativeInt(..., 0)`), sending `capacity: 0` so clearing the input sets unlimited capacity as intended.

### Bug 2: Non-integer numeric inputs (`"abc"`, `"1.5"`, `"-2"`) in Hosts forms serialized to `null` (`NaN`) instead of being refused
- **Reproduction:**
  1. In `Settings → Hosts`, click `Edit` on a host, type `"abc"` into `Capacity`, and click `Save` (or open `New code`, type `"five"` into `Max uses`, and click `Create code`).
- **Observed:** `Number("abc")` evaluated to `NaN`, which `JSON.stringify` serialized as `null`. On `PATCH /api/hostds/{name}`, Go decoded `"capacity": null` into `*int` as `nil` (no-op) and returned `200 OK`, closing Edit mode and silently discarding `"abc"`. On `POST /api/hostds/enrollment-codes`, Go decoded `"max_uses": null` into `int` as `0`, silently creating an **unlimited-use** enrollment code when the user typed a non-numeric limit.
- **Fix:** Added `parseNonNegativeInt(text, label, emptyVal)` in `HostdsSection.jsx` rejecting any non-blank value outside `/^\d+$/` (or `!Number.isSafeInteger`) with `<label> "<value>" must be a non-negative whole number` before any network call is sent, wired into `createCode` (`max uses`, `default capacity`), `acceptPending` (`host capacity`), and `save` (`capacity`).

### Bug 3: Refusal error banner on `Settings → Hosts` stayed up after the corrected action saved or was cancelled
- **Reproduction:**
  1. On `/settings/hosts`, open `New code`, enter `Code name: walk-code` and `Expires in: not-a-duration`, and click `Create code` — the shell's 5-second error snackbar (`invalid expires_in ...`) appears.
  2. Fix `Expires in` to `1h` and click `Create code` (or click `Cancel new code`).
- **Observed:** `SettingsPage.jsx` rendered `<HostdsSection {...common} />` without `dismissError`, and `HostdsSection` never dismissed the error it raised once the retry succeeded or the subform was cancelled, leaving the error snackbar floating over the newly created enrollment code chip / saved host row.
- **Fix:** Passed `dismissError={dismissError}` from `SettingsPage.jsx` to `HostdsSection` and added `raisedRef` / `raise` / `settle` tracking (matching `GitConnections.jsx`), dismissing `HostdsSection`'s own banner as soon as `act()` or `showInstall()` succeeds or `Cancel new code` / `Edit` `Cancel` / `Rename` `Cancel` is clicked.

### Regression Coverage Added
- **Vitest (`ui/src/components/settings/HostdsSection.test.jsx`):** Added unit tests covering `parseNonNegativeInt` edge cases (`""`, whitespace, integers, `"abc"`, `"-1"`, `"1.5"`, `"5x"`), client-side refusal on non-integer `Capacity` without issuing `PATCH`, clearing `Capacity` to `0` on both `Hosts → Edit` and `Pending → Accept`, and calling `dismissError` with the prior error message upon subsequent save.
- **Playwright E2E (`ui/e2e/hostds-capacity-and-banner.spec.js`):** Added a real-browser E2E test against `grain demo` verifying both server-refusal (`Expires in: not-a-duration` -> `1h`) and client-refusal (`Capacity: abc` -> `""`) snackbar dismissal within `< 2s` and verifying that blanking `Capacity` removes `cap=6` across `page.reload()`.
