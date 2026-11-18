# Connections & infrastructure settings walkthrough — 2026-11-18 (task 7020)

- **Date:** 2026-11-18, 04:17–05:10 UTC, one run.
- **Scope:** Settings → Git, Hosts, Capabilities (with the per-server pages, capability policy, API scope sets, Deployment credentials and secrets), Hub (`registry`), Engine, CI, Caches and Sandboxes.
- **Task:** task 7020, branch `grain/53549e42/task-7020-walk-through-settings-git`, base `main`.
- **Commit tested:** `a6fdad20c` ("Merge pull request #1820"). The fixes below were rebuilt and checked on top of it.
- **Outcome:** one gap fixed and one hint added (F1 and F2 below). One gap is filed as a follow-up (G1). Everything else read back as the [2026-11-17 walk](2026-11-17-connections-infrastructure-settings.md) recorded it.
- **New since 2026-11-17:** the `/next/settings/capabilities/<id>` page (G2 of the 2026-11-16 walk) has landed, so this walk covered it on `/next/` for the first time.

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, `linux/amd64`, Go 1.26.10, Node 20.19.0. |
| Server | This repository's real `grain demo` over a throwaway SQLite store. Each Playwright worker started one through `ui-shared/e2e/demo.js` (via `ui-ng/e2e/fixtures.ts`) and signed in with that run's access code. Specs that needed secrets ran it with `-secrets-store`, a real encrypted store. |
| Frontend | `make frontend`, then `npm run build` in `ui-ng/` after each change (`pkg/ui/static-next`), embedded in the binary the suite's global setup builds. |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`): desktop 1440×900, plus the phone (390×844) and dark projects where a spec is tagged for them. |
| Model | None. There was no `/home/debian/.gemini-api-key`, so `LOOK=1` was not available. |

## Stubs

**Nothing was intercepted in this run.** Every request went to the real demo server.

- The host-script Test ran the demo's real `ops` script through `POST /api/capability-servers/test-script`.
- Secrets went to a real `-secrets-store` store.
- The throwaway config map and the long-named host script were written through the real `PUT /api/capability-servers`, then put back.

The Hub `connecting` / `degraded` / `error` states, a reachable git host, GitHub and a connected remote host are still absent from the demo. They are covered by the `next-hub-sandboxes-parity` spec, which stubs `GET /api/registry` (documented there), and by the 2026-11-17 walk.

## How it was walked

Exploratory Playwright scripts (`ui-ng/e2e/zz-walk-*.spec.js`, deleted before commit) drove visible controls with clicks, typing, focus and Enter. They logged every console error, every response of 400 or more (with its body) and every native dialog, and they reloaded after each mutation.

Everything ran against **`/next/`**. Every section in scope is in `ui-ng/src/app/settings/settings-sections.ts`, and the per-server page is now routed (`settings/:section/:item` in `ui-ng/src/app/app.routes.ts`). No step needed the classic UI, and no classic file was changed.

## Steps and outcomes

### Sweep of every section, redirect and server page

| Step | Outcome |
| --- | --- |
| `/next/settings/{git,hosts,hostds,capabilities,registry,engine,ci,caches,sandboxes}` | ✅ Each renders its cards. None logged a console error or a failed request. |
| `/next/settings/capability-servers`, `/credentials`, `/legacy-capabilities`, `/secrets`, `/next/admin/integrations`, `/next/system/integrations` | ✅ They land on Capabilities: `capability-servers` at the page top, `secrets` at `#other-secrets`, and the rest at `#credentials`. |
| `/next/settings/capabilities/<id>` for each of the 13 servers (`api`, `config-map`, `figma`, `gchat`, `gcp`, `git`, `github`, `grain`, `host-script`, `model-proxy`, `release`, `repo` and the demo's `cloud`) | ✅ Every page renders, with no console error and no response of 400 or more. |

### Capabilities — per-server pages (`/next/settings/capabilities/<id>`)

| Step | Outcome |
| --- | --- |
| `(i)` popovers by click, `Enter` and `Space`, closed by Escape; schema form, write-only secrets, gcp keyless vs key mode, figma Test / Connect Figma and its OAuth callback, host placement, rename, holders and the Remove confirm | ✅ `next-capability-server-page` covers all of these. Its 17 tests passed over the desktop, phone and dark projects. |
| host-script: the demo's saved `ops` script | ❌ **F1.** The page offered "Edit server settings" but listed no scripts, and there was no way to test one. Classic's `HostScriptList` does both. |
| host-script after F1: Test with arguments `hello world` and `$(id)` | ✅ stdout reads `hello world $(id)`, with `exit 0`, a duration and stderr `(empty)`. Each argument is passed exactly as typed, and no shell expands `$(id)`. |
| host-script after F1: remove argument 2, run again | ✅ stdout reads `hello world`. |
| host-script after F1: an argument holding a NUL byte | ✅ "Not run: argument 1 holds a NUL byte", with no exit chip. |
| host-script after F1: Enter on the focused Test | ✅ `aria-expanded` goes back to `false` and the run panel closes. |
| config-map: Configure, Add config map, name `walk-map`, data `{"hello.txt":{"content":"hi"}}`, Save, reload | ✅ It is stored as `{"config_maps":[{"data":{"hello.txt":…},"name":"walk-map"}]}`. Remove plus Save cleared it again. |
| config-map: the placement editor itself | ⚠️ **G1.** `/next/` offers only the generic schema form. A map's files are one JSON textarea, and `secret_values` is one JSON string box. Classic's `ConfigMapList` edits each file, its mode and path, and each secret-map key. Filed as a follow-up. |
| The host process server's "version, adopted runs and allowlist suggestions" | — No such server exists in this tree. `host-script` is the host-side script server, and its status reports none of these. |

### Deployment credentials and Other secrets (`#other-secrets`, over `-secrets-store`)

| Step | Outcome |
| --- | --- |
| Store `walk-throwaway` / `token` | ✅ The value box is `type="password"`. The row lists the name and key only. Neither the page HTML nor `GET /api/secrets` holds the value, after the store or after a reload. |
| Replace | ✅ The form fills in `walk-throwaway` / `token`, and focus moves to the value box. |
| Remove | ✅ It asks first: "Remove the secret walk-throwaway? Removes walk-throwaway and every key in it. No stored value can be read back…". After confirming and reloading, the row is gone. |

### Engine — `/next/settings/engine`

| Step | Outcome |
| --- | --- |
| Max merges per repo branch `0` | ✅ Reads back as `5`, the default. |
| Max merges per repo branch `-1` | ✅ Refused ("maxMergesPerRepo must be at least 1"). |
| Max merges per repo branch `2.5` | ✅ Refused ("maxMergesPerRepo must be a whole number, not 2.5"), the wording F1 of 2026-11-17 introduced. |
| Max merges per repo branch `3` | ✅ Saved and read back. |
| Max worker agents `0`, or blank | ✅ Reads back as `10`, the default. |
| Poll interval `5`, `abc` | ✅ Refused ("pollInterval: time: missing unit in duration "5"", "…invalid duration "abc""). The placeholder reads `10s`. |

### Caches and CI — `/next/settings/caches`, `/next/settings/ci`

| Step | Outcome |
| --- | --- |
| Actions cache max size `-1` | ✅ Refused ("actionsCacheSizeGb must be 0 (unset) or at least 1"). |
| Actions cache max size `2`, then blank, then Save | ✅ `2` reads back. The blank then reads back empty, with the placeholder `20`. |
| Proxy cache max size `3` | ✅ Read back. |
| CI queue, history, job cache and artifacts | ✅ The `next-hub-sandboxes-parity` CI retention test and `next-infra-settings-port`'s Flush confirm passed. |

### Sandboxes — `/next/settings/sandboxes`

| Step | Outcome |
| --- | --- |
| Session sandbox idle TTL `-1m` | ⚠️ **F2.** Saved silently as `-1m0s`. The server takes a negative TTL as "release at once" (`pkg/ui/settings.go`), but neither UI said so anywhere. |
| Session sandbox idle TTL after F2 | ✅ The hint reads "…Blank uses grain's default (30m0s); a negative duration such as -1s releases it as each turn ends." The box is tied to it through `aria-describedby`. |
| Backends, auto bounds (refused by name), disk floor blank reset, Kubernetes card, custom image revert | ✅ `next-hub-sandboxes-parity` and `next-infra-settings-port` passed. |

### Git, Hosts, Hub, policy and scope sets

All of these passed in the scripted `/next/` suite below:

- Git add / test / remove for a plain git host (token, and SSH key with known hosts), GitHub token and GitHub App;
- GitHub App offered only for GitHub, and Reuse options labelled `host · kind · scope (type)`;
- the GitHub host pointer to `#credentials`;
- the no-git-connection notice;
- Hosts invites, codes, pending requests, Edit / Rename / Revoke / Delete and Delete all revoked, each behind its confirm;
- Hub status, heartbeat, scopes, the token under a config-repo lock, and Scope overrides;
- capability policy verdicts in pickers, linking back to `#capability-policy`;
- API scope set create / edit / remove.

## Findings

**Fixed in this task:**

- **F1 — The `/next/` host-script server page did not list its scripts and could not test one.**
  - **Reproduction:** open `/next/settings/capabilities/https:%2F%2Fgrain.dev%2Fcapabilities%2Fhost-script` in the demo. The demo saves one script, `ops`, but the page shows only "Edit server settings". Classic's card lists `ops` with a Test.
  - **Fix:** the page now lists each saved script beside Server settings. A row shows the script's name, description, usage and limit chips. Env variables show by name only, never by value.
  - The row's Test (`aria-expanded`) opens a panel with one box per argument, plus Add argument and Remove argument controls. It runs the saved script through `POST /api/capability-servers/test-script` and shows the exit, duration, stdout and stderr, or "Not run: <why>".
  - It is disabled for a caller who cannot call that route.
  - New helpers `isHostScriptServer`, `hostScripts` and `hostScriptLimits` live in `capability-server.ts`. The design book's server-page paragraph records the rule.
  - **Coverage:**
    - vitest: `capability-server.spec.ts` › "host scripts".
    - `next-capability-server-page.spec.js`, three tests:
      - the list, and the exact-argv run of a space and `$(id)`, over desktop, phone and dark, asserting the Test stays inside its row with no sideways scroll;
      - the NUL refusal;
      - a 63-character name wrapping inside its row at phone width, with the config put back.
- **F2 — A negative Session sandbox idle TTL saved silently, and nothing said what it means.**
  - **Fix:** a hint under the box, tied to it with `aria-describedby`, gives the default and says what a negative value does.
  - **Coverage:** `next-infra-settings-port.spec.js` › "Sandboxes says what a blank or negative session sandbox idle TTL means".

**Filed as a follow-up:**

- **G1 — `/next/` has no config-map / secret-map placement editor.**
  - The generic form works: a throwaway map saved, read back and was removed. But it asks for each map's files as raw JSON, and for every secret-map value as one JSON string.
  - Classic's `ConfigMapList` (632 lines) offers per-file rows (key, content, mode, executable, path), per-key secret rows with set / not-set chips, and the default directory hint.
  - That port is materially larger than this walk's fixes, so it was proposed separately.

**Not reachable in the demo, unchanged:** these remain as the 2026-11-16 walk recorded them (its G3):

- a connected host's address, principal and build;
- a git refusal banner clearing on a fixed connection;
- the cleared-pauses report on Delete;
- `needs-hostd` for a host that is away.

## Tests run

- `npx vitest run src/app/settings/` passed: 66 tests. `npm run lint` was clean, and `prettier --check` was clean on every changed file.
- `npx playwright test` from `ui-ng/`:
  - 70 passed (desktop, two workers) across the in-scope specs: `next-git-settings-port`, `next-hosts-git-parity`, `next-hosts-invite`, `next-hosts-confirm`, `next-hosts-update`, `next-connections-infra-settings`, `next-connections-settings-walk`, `next-capabilities-parity`, `next-capability-policy-pickers`, `next-hub-sandboxes-parity`, `next-infra-settings-port`, `next-settings-connection-tests`, `next-settings-connections` and `next-full-system`;
  - 17 passed for `next-capability-server-page` over all projects after the fix, including the three new tests;
  - the new idle TTL test passed.

## Not covered

- **Visual review:** the host-script page was shot at desktop, phone and dark by `SHOTS=<dir>`, not committed. Nobody looked at the shots: the sandbox's file reader returns PNG bytes, and there was no Gemini key. Layout is asserted with bounding boxes and a no-sideways-scroll check instead.
- **Locked mode and the `settings:write` / `secrets:write` split:** not re-walked by hand. `next-capabilities-parity` and `next-hub-sandboxes-parity` cover them, and both passed.
