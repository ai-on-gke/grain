# Connections & infrastructure settings walkthrough — 2026-11-16 (task 6336)

- **Date:** 2026-11-16, 10:15–11:20 UTC, one run.
- **Scope:** Settings → Git, Hosts, Capabilities (with Deployment credentials, capability policy, API scope sets and secrets), Hub (`registry`), Engine, CI, Caches and Sandboxes.
- **Task:** task 6336, branch `grain/53549e42/task-6336-walk-through-settings-git`, base `main`.
- **Commit tested:** `234824180` ("Merge pull request #1675"). The fixes below were checked on a rebuild of that commit with them applied.
- **Outcome:** since the 2026-11-14 walk, the parity work ported most of the classic sections to `/next/`, and they now save, reload, read back and confirm destructive actions. Three bugs were fixed (F1–F3). The remaining `/next/` gaps, which are classic-only, are filed as one follow-up (G1–G3).

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, `linux/amd64`, Go 1.26, Node 20.19.0. |
| Server | This repository's real `grain demo` over a throwaway SQLite store: one per Playwright worker, started by `ui-shared/e2e/demo.js` through `ui-ng/e2e/fixtures.ts`, signed in with that run's access code. |
| Frontend | `make build` (classic `pkg/ui/static`, Angular `pkg/ui/static-next`), embedded in the binary that the suite's global setup builds. |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`): desktop 1440×900, and phone 390×844 where noted. |
| Model | None was used. The sandbox had no `/home/debian/.gemini-api-key`, so `LOOK=1` was not available. |

## Stubs

Only one route was intercepted, in the regression spec:

- `GET /api/config`, rewritten with `deployment.gitConnections: []`, `hasGitCredential: false` and `gitCredential: ""`, to show the Repos page's no-git-connection notice without deleting the shared demo's connection. During the walk itself the notice was also seen for real: the demo's `default` connection was removed through the UI, in a demo of its own.

Every other boundary was one `grain demo` already provides: its seeded hosts, the pending join request and enrollment codes, the capability servers on loopback, Hub status `connected`, and the git connection. The boundaries the demo lacks were left unstubbed and recorded as observed:

- no secret store (the Capabilities secrets form and Hub's Registration token both say so and disable their controls; `ui-ng/e2e/next-capabilities-parity.spec.js` covers the SecretsPanel flows with its own stubs, and it passed in this run);
- the package proxy and Actions caches are not enabled (Flush is disabled);
- no git host or GitHub was reachable, so Verify connections reports "GitHub rejected the token (bad credentials)" for the fixture token.

The throwaway capability server pointed at `http://127.0.0.1:9/mcp`, a closed port, so its Test was meant to fail.

## How it was walked

These were exploratory Playwright scripts, kept out of the tree, run from `ui-ng/` against the suite's fixtures. They drove visible controls with clicks, typing and the keyboard, and recorded every console error, every failed request (≥400, with its request and response body) and every native dialog. They reloaded after each mutation. The regressions they found are kept as `ui-ng/e2e/next-connections-settings-walk.spec.js`.

UI used for each step: **`/next/`** for every section (all eight are in `ui-ng/src/app/settings/settings-sections.ts`). Classic was used only for what `/next/` lacks (marked *classic*). None of the eight sections logged a console error or failed request on load.

## Steps and outcomes

### Git — `/next/settings/git`

| Step | Outcome |
| --- | --- |
| Table: `default · github.com · GitHub · All repos on host · pat`. | ✅ |
| GitHub host pointer. | ✅ It links to `/next/settings/capabilities#credentials`. |
| Plain git host + token on `git.walk.example`. Reload. | ✅ It saves as `grain-git-c-<hash>`, kind `git host`. GitHub App shows disabled, with "Needs a GitHub host: a plain git host has no App API". |
| Plain host + SSH key. | ✅ Add stays disabled until Known hosts is filled. A malformed key is refused by name ("ssh: invalid openssh private key format"). A real ed25519 key with known hosts saves and persists. |
| Link stored credential → Reuse authentication from. | ✅ Nameless connections can be told apart: "git.walk.example · git host · all repos (pat) stored as grain-git-c-…". |
| GitHub App on github.com with scope `walk-org/` and proxy `http://proxy.walk.example:3128`. | ✅ Row: "github.com via proxy http://proxy.walk.example:3128 · GitHub · walk-org · app". |
| GitHub pat scoped to `walk-org/walk-repo`. | ✅ |
| Verify connections. | ✅ "github.com: GitHub rejected the token (bad credentials)…" (no network). |
| Test, then Remove each throwaway. | ✅ Each Remove asks first ("Remove Git connection? … Repositories using it can no longer clone or push…"). After reload only `default` is left. |
| Remove `default`, then open New task and Repos. | ✅ New task shows the notice ("…a GitHub token or GitHub App for github.com, or a token, SSH key or ambient login…") with links to `/next/settings/git`. ❌ **Repos showed no notice**, only a discovery caption (F1). |
| A fixed connection takes down its refusal banner on save. | ⛔ Not reachable: the demo has no reachable git host, so a refusal cannot be fixed. |

### Hosts — `/next/settings/hosts` (and `/next/settings/hostds`)

| Step | Outcome |
| --- | --- |
| Both deep links. | ✅ |
| Hosts table: `build-runner` ("None: needed to dial in through IAP"), `cloudtop-alice`, `local` ("Co-located (socket)", no actions), `old-laptop` (revoked). | ✅ Delete is disabled on live hosts. Rotate and Revoke are disabled on the revoked one. ⚠️ Host address, principal and build appear only for a connected host, and no demo host connects. |
| Invite hosts: snippet `curl … /hostd/install \| sh -s -- --join ghe_… --release v1.2.0`, code chips `default`/`workstations`, Show code, Copy. | ✅ |
| New code `walk-code`: expiry 24h, 3 uses, capacity 2, labels `team=walk`, principals `user:walk@example.com`. | ✅ The snippet switches to it, showing "Uses: 0 / 3 · Default capacity: 2 · Labels: team=walk". After reload the row reads `0 / 3 · 2026-11-17 · capacity 2 · team=walk · allow user:walk@example.com`. |
| Expiry `soon`. | ✅ Refused by name ("expected a positive duration such as 24h or 7d"). |
| Rotate code, then Revoke code. | ✅ Both ask first. After reload the revoked row says `revoked` with "No actions". |
| Pending `devbox-carol.example.com`: verified enrollee `carol@example.com`, SHA256 fingerprint, no pairing code. Set name `walk-carol`, labels `team=walk`, capacity 3, then Accept. | ✅ The host appears as `walk-carol · team=walk · capacity 3` and the pending list empties. Reject and rekey requests were not exercised (the demo seeds one plain request). |
| Edit `cloudtop-alice`: labels, capacity 5. Reload. Then blank the capacity. | ✅ It reads back `capacity 5`, prefilled when edited. Blanking it clears it after reload. |
| Legacy token host `walk-legacy` (owner, labels). | ✅ The token is shown once (`hostd-legacy-copy`). |
| Rename to `walk-legacy2` (inline). Reload. | ✅ |
| Revoke, then Delete. | ✅ Both ask first. Gone after reload. ⚠️ No pauses referenced the host, so the cleared-pauses report was not seen. |
| Delete revoked. | ✅ It asks first ("…One a model connection or capability server still names is skipped"). The result is "Deleted host old-laptop." |

### Capabilities — `/next/settings/capabilities`

| Step | Outcome |
| --- | --- |
| Server list. | ✅ The `grain.dev` servers come before the demo's own `capabilities.demo.grain.example/cloud`. In-process `api`, `github`, `grain`, `model-proxy`, `release` and `repo` have no Disable. `figma` and `gchat` show `needs-configuring`. The deprecation notice names Deployment credentials. |
| Disable `figma`. | ✅ The chip flips to `disabled` without a reload, and persists. Enable puts it back to `needs-configuring`. |
| Add throwaway `https://capabilities.walk.example/throwaway` → `http://127.0.0.1:9/mcp`, then Test, then Remove. | ✅ State `failed`, and Test reports the failure. Remove asks first ("Tasks that hold a grant on it can no longer use it"). Gone after reload. |
| Test `gcp`. | ✅ `enabled`. |
| Capability policy: `gcp#gcp-key` Deny on `acme/widgets`, Preview, Save. | ❌ **Both calls returned 400 "…is not a capability reference" with no message near the card.** The placeholder suggested the bare name `gh-cli`, which the server refuses. The error went only to the page banner, scrolled far above (F2). After the fix, a bare name is flagged and Add rule stays disabled, and a refused Preview or Save is shown in the card. |
| Policy verdicts in pickers. | ⛔ `/next/`'s `g-grants-picker` shows no `denied` / `may_be_refused` verdicts and no link to `#capability-policy` (G1). |
| API scope set `walk/throwaway` = `tasks:read`, then `tasks:read, ci:read`, then Remove. | ✅ Each step persists across reload. Remove asks first ("Nothing grants api:walk/throwaway"). |
| Deployment credentials: GCP project → `walk-project`, Save, reload, restore. | ✅ It reads back. The gcp link goes to `#capability-servers`. ❌ The description read "…the gcp capability server's**gcp-key** and…" (F3). |
| Redirects: `/next/settings/{credentials,legacy-capabilities}`, `/next/admin/integrations`, `/next/system/integrations` → `#credentials`; `/next/settings/secrets` → `#other-secrets`; `/next/settings/capability-servers` shows Capabilities. | ✅ The target card's top sits below the header (y = 136). |
| `/next/settings/capabilities/<id>`. | ⛔ Falls through to the Inbox (G2). *Classic:* `/settings/capabilities/<id>` exists (figma's page: methods, connection, server URL). Schema forms, `gcp` key modes, figma Test/OAuth, host scripts and config-map/secret-map placements were not walked here (G2). |
| `(i)` popovers (*classic*). | ⚠️ There are four info buttons. Enter on the first opened nothing detectable as a tooltip, popover or dialog; not investigated further (G2). |

### Hub — `/next/settings/registry`

| Step | Outcome |
| --- | --- |
| Status card. | ✅ `Connected`. Last heartbeat "every 15s · health ok · 2 active, 2 awaiting reply". Advertised scope `acme/widgets` primary, "from repo config". |
| Heartbeat now. | ✅ The heartbeat time advances. |
| Credential mode → Workload OIDC, OIDC audience, display name, purpose, Listed off. Save, reload. | ✅ All of it reads back. |
| Entry `Bad Name!`. | ✅ Refused by name ("must be 3..32 lowercase ASCII letters…"). |
| Scope overrides: Primary `acme/widgets` + `docs/`, Secondary `acme/widgets`, Exclude `acme/other`. Save, reload. | ✅ They persist. Advertised scopes become "secondary override" and "docs/ primary override". All removed afterwards. |
| Registration token. | ✅ "not set". With no secret store, Set and Clear say so. Locked mode is covered by `next-hub-sandboxes-parity.spec.js`, not re-walked. |

### Engine — `/next/settings/engine`

| Step | Outcome |
| --- | --- |
| Initial `10s` / `4` / `5`. Placeholders `10s` / `10` / `5`. | ✅ |
| `15s` / `3` / `2`. Save, reload. | ✅ It reads back. |
| All blank. Save, reload. | ✅ It resets to `10s` / `10` / `5`. |
| Merges `-3`, poll `soon`. | ✅ Refused by name. Merges `0` is held by the browser's own `min=1` check, so no request is sent. |
| Sandbox commitment note. | ⚠️ The component is on the page, but the demo reports no commitment, so nothing rendered. |

### CI — `/next/settings/ci` and Caches — `/next/settings/caches`

| Step | Outcome |
| --- | --- |
| Unset values show as blanks with their defaults as placeholders (`no limit`, `5000`, `256`, `2048`, `1m0s`, `2m0s`, …). | ✅ (fixes 11-14's G7) |
| CI jobs per run, runs kept, job cache and artifacts: set `6 / 150 / 4096 / 512`, then blank. Save and reload each time. | ✅ The values read back, and blanks go back to the default placeholders. |
| CI job cache `-5`. | ✅ Refused by name. |
| Flush CI job cache. | ✅ It asks first. Cancelled. |
| Caches `25` / `30`, then blank. Save and reload each time. | ✅ They read back. Blank reads back blank, with a `20` placeholder. Flush buttons are disabled (caches not enabled). |

### Sandboxes — `/next/settings/sandboxes`

| Step | Outcome |
| --- | --- |
| Backends. | ✅ Docker, Kubernetes pod and Kubernetes Granule are offered. Granule VMs ("needs SSH user/workspace/git proxy host"), Host directories ("needs -host-sandboxes…") and **None (control plane only)** ("needs -disable-native-sandboxes…") are disabled, each with its reason. |
| Network: Open / None. Session sandbox idle TTL field present. | ✅ |
| Overcommit → auto: bounds prefilled `1–6` (CPU), `1–2` (memory), pressure targets `25` / `5`. | ✅ |
| cpuMin 8 > cpuMax 4. | ✅ Refused by name ("cpuMin 8.00 is above cpuMax 4.00"). cpuMax 999 is accepted: the server sets no ceiling. |
| Disk floor minimum `1024`, reload, then blank. | ✅ Blank becomes `5120` once the box loses focus, and reads back `5120`. Until the box loses focus there is nothing to save, so the save bar appears on blur. |
| Kubernetes: prefilled defaults as placeholders. Priority class `walk-ci`, save, reload, clear. Workspace `relative/path`; node selector `pool`. | ✅ The priority class reads back. Both bad values are refused by name. |
| Custom image `registry.walk.example/sbx:1`, then cleared. | ✅ The contract, FROM-base snippet and `docker.io/grainai/sandbox-k8s-base:v1.2.0` hint are shown. Clearing goes back to "Empty uses the image this build is stamped for". |

## Findings

**Fixed in this task** (regression coverage in `ui-ng/e2e/next-connections-settings-walk.spec.js`):

- **F1 — `/next/` Repos had no no-git-connection notice.** Classic `RepoList.jsx` shows `NoGitConnectionNotice`, and `/next/`'s New task did too, but the `/next/` Repos page did not. It now renders `g-no-git-connection-notice` once the config has loaded and has no usable connection.
- **F2 — The capability policy editor suggested a name the server refuses, and hid the refusal.** The pattern placeholder was the retired bare name `gh-cli`. `model.CapabilityPolicy.Validate` only takes `https://…` references, so Preview and Save failed, and the error went to the page banner, scrolled out of sight. Now:
  - the placeholder is a reference, with a `<capability_id>#<method>` hint;
  - a typed bare name is marked invalid, and Add rule stays disabled;
  - a refused Preview or Save shows in the policy card under its buttons, with Dismiss.

  Classic's editor already has a reference placeholder and helper text, so it was not changed.
- **F3 — "server'sgcp-key" in the Deployment credentials description** (`/next/`). Angular dropped the whitespace-only text between `</a>` and `<code>`. It is kept with `&ngsp;`.

**Filed as a follow-up** (one task, `branch: "main"`):

- **G1** — `/next/`'s `g-grants-picker` (New task, task detail, repo CI capabilities, sessions) shows no capability-policy verdicts (`denied`, `may_be_refused`) and no link back to `#capability-policy`, as classic `GrantsPicker.jsx` does.
- **G2** — There is no `/next/settings/capabilities/<id>` page (it falls through to the Inbox). It needs per-server schema forms (`Host` placement on `run: hostd`, `gcp` keyless vs key mode, figma Test/OAuth), host-script scripts, config-map/secret-map placements, holder links and `(i)` popovers. All of these are classic-only.
- **G3** — Not reachable in the demo, so not verified: the refusal banner clearing on a fixed git connection, the cleared-pauses report on Delete host, rekey requests, the host address/principal/build of a connected host, and the Engine commitment note.

## Tests run

- `npx playwright test` (from `ui-ng/`, desktop/phone/dark projects) on `next-full-system`, `next-capabilities-parity`, `next-repos-list`, `next-settings-connections`, `next-connections-infra-settings` and the new `next-connections-settings-walk`: 44 passed.
- `npm run lint` and `vitest run src/app/settings src/app/repos` (84 tests): passed.

## Not covered

- Visual review: screenshots of `/next/settings/capabilities` and `/next/repos` were rendered at desktop and phone, in light and dark (`ui/e2e/screenshot.mjs`), along with element shots of the changed states. None are committed. The sandbox file reader returns PNG bytes and there was no Gemini key, so nobody looked at them. Layout was asserted instead: a phone-width test checks that the policy hint and error stay inside their card, with no horizontal overflow. No "before" screenshots were taken.
- Locked mode and the `settings:write` / `secrets:write` role split were not re-walked. `next-capabilities-parity.spec.js` covers both, and passed.
