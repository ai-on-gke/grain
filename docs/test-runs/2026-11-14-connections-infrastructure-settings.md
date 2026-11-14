# Connections & infrastructure settings walkthrough — 2026-11-14 (task 5635)

- **Date:** 2026-11-14, 11:57–12:45 UTC, one run.
- **Scope:** Settings → Git, Hosts, Capabilities (with Deployment credentials, capability policy, API scope sets and secrets), Hub (`registry`), Engine, CI, Caches and Sandboxes.
- **Task:** task 5635, branch `grain/53549e42/task-5635-walk-through-settings-git`, base `main`.
- **Commit tested:** `94eb0aced` ("Merge pull request #1517"). The fixes below are in `fb93f6f2f` and were checked again on a rebuild of that commit.
- **Outcome:** the `/next/` pages do the basics: they save, reload and read back, and the server refuses out-of-range values by name. Five bugs were fixed (F1–F5). One more bug and a set of `/next/` gaps were filed as a follow-up task (F6, G1–G9). Most of the task's scripted coverage is only in the classic UI so far (G-list); those classic pages were checked for redirects and anchors only, not walked end to end (see "Not covered").

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12, `linux/amd64`, Go 1.26.10, Node 20.19.0. |
| Server | This repository's real `grain demo` over a throwaway SQLite store: one per Playwright worker, started by `ui-shared/e2e/demo.js` through `ui-ng/e2e/fixtures.ts` (`grain demo -addr 127.0.0.1:0 -open=false -access-code-file …`), signed in with that run's access code. |
| Frontend | `make frontend` builds (`pkg/ui/static`, `pkg/ui/static-next`), embedded in the binary that the suite's global setup builds. |
| Browser | Playwright 1.62.1 Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), desktop project 1440×900. |
| Model | None was needed. The sandbox had no `/home/debian/.gemini-api-key`, so `LOOK=1` was not used. |

**Stubs:** none were added. Every boundary used was one `grain demo` already provides: its seeded hosts, pending join request, enrollment codes, capability servers on loopback, Hub status (`connected`) and git connection. Two boundaries the demo lacks were left unstubbed and recorded as observed:

- no secret store (`GET /api/secrets` → `{"enabled":false}`; `PUT /api/secrets/…` → 404);
- no sandbox prober (`POST /api/sandbox-probe` → 404 "sandbox probes are not available"). Package-cache flush is 501 for the same kind of reason.

The throwaway capability server pointed at `http://127.0.0.1:9/mcp`, a closed port, so Test was meant to fail. No git host was contacted: Verify connections ran against the demo's own fixture.

## How it was walked

These were exploratory Playwright scripts, kept out of the tree. They used the suite's fixtures and drove visible controls with clicks, typing and the keyboard. They recorded every console error, every failed request (≥400) and every native dialog, and they reloaded after each mutation. Run with `npx playwright test <spec> --project=desktop` from `ui-ng/`. The regressions they found are kept as `ui-ng/e2e/next-settings-connections.spec.js`.

UI used for each step: **`/next/`** wherever the section exists there; all eight do (`ui-ng/src/app/settings/settings-sections.ts`). Classic was used only for what `/next/` lacks (marked *classic*).

## Steps and outcomes

### Git — `/next/settings/git`

| Step | Outcome |
| --- | --- |
| Table loads: `default · github.com · All repos on host · pat`. | ✅ |
| Add a pat connection on `git.walk.example`. Reload. | ✅ It saves as nameless `grain-git-c-<hash>` and persists. ❌ It was sent with no `kind`, so the server recorded a **plain git host as a GitHub** (F2). |
| Auth types for a plain host. | ❌ GitHub App was offered for every host (F2). |
| Verify connections. | ✅ Banner: "Check reported issues" (the demo's fixture is not reachable). |
| Remove the throwaway connection. | ❌ **Deleted on one click, without asking** (F1). After the fix: a dialog, Cancel keeps it, Escape cancels with focus on Cancel, Remove deletes it, and the deletion holds after reload. |
| SSH key + known hosts, GitHub App with scope/proxy, "Reuse authentication from", refusal banner, GitHub host pointer, no-git-connection notice. | ⛔ Not in `/next/` (G1). |

### Hosts — `/next/settings/hosts` (alias of `hostds`)

| Step | Outcome |
| --- | --- |
| Deep link `/next/settings/hosts`. | ✅ Shows Hosts. |
| Hosts table: `local` (connected, no actions), `build-runner`, `cloudtop-alice` (owner + labels), `old-laptop` (revoked). | ✅ Delete is disabled on live hosts. ⛔ No host address, principal, build or capacity columns, and no Edit or Rename (G2). |
| Create enrollment code `walk-code`. | ✅ The row shows `0 / ∞ · never`. ⛔ No expiry, max uses, default capacity, labels or principals, and no `--join` snippet (G2). |
| Revoke the code. | ✅ It asks first ("Its invite command stops working for good…"). After reload the row stays, with `revoked` in the Expires column. Rotate and Revoke stay enabled on the revoked row (minor; G2). |
| Pending request `devbox-carol`: key fingerprint shown, edit labels to `team=walk`, Accept. | ✅ The host appears with `team=walk`, and the pending list empties. ⛔ No host name or capacity fields (G2). |
| Register legacy host `walk-legacy` with `zone=walk`. | ✅ Its token is shown once. |
| Revoke `walk-legacy`, then Delete. | ✅ Both ask first. Delete removes it, and it stays gone after reload. ⛔ The confirmation does not report cleared pauses (G2). |
| Delete revoked. | ✅ It asks first ("…One a model connection or capability server still names is skipped") and removes `old-laptop`. |

### Capabilities — `/next/settings/capabilities`

| Step | Outcome |
| --- | --- |
| Server list. | ✅ In-process `api`, `github`, `grain`, `model-proxy`, `release` and `repo` show Test only. ⛔ Built-ins are not listed first. `gcp`, `config-map`, `figma` and `host-script` are listed as remote MCP rows with Disable. No `needs-configuring`/`needs-hostd` chips, `(i)` popovers or deprecation notices (G3). |
| Disable `config-map`. | ✅ The chip flips to `disabled` without a reload. Enable flips it back. |
| Add throwaway `https://capabilities.walk.example/throwaway` → `http://127.0.0.1:9/mcp`, then Test. | ✅ The row appears. Test: "…: failed". |
| Remove it. | ❌ **Removed on one click** (F3). After the fix it asks first, and Cancel keeps it. |
| Capability policy: add `gcp#gcp-key` Deny, Preview, Save, reload, remove, Save. | ✅ "0 existing grants newly denied (1 rules)". The rule persists across reload and is removed cleanly. ⛔ Not checked in pickers: the New task picker was not reached (`/next/tasks/new` is not a route; G4). |
| API scope set `walk/throwaway` = `tasks:read`. | ✅ It saves and persists. ⛔ There is no Remove (G4). |
| Store a throwaway secret. | ❌ The value box was a plain textarea, so the **secret showed in the clear**. With no secret store the PUT 404'd behind an enabled button (F4). ⛔ No list, mask, replace or remove (G5). |
| `/next/settings/capabilities/<id>`. | ⛔ Falls through to the Inbox (G3). |
| Deployment credentials (`#credentials`, `#other-secrets`). | ⛔ Not in `/next/` (G5). *Classic:* the `#credentials` card shows the GitHub host, the GCP project and the pool. |
| Redirects. | ✅ `/next/settings/{capability-servers,credentials,secrets,legacy-capabilities}` resolve to Capabilities. ❌ `/next/admin/integrations` → Inbox and `/next/system/integrations` → Fleet (F5). *Classic:* `/settings/credentials`, `/settings/legacy-capabilities`, `/admin/integrations` and `/system/integrations` → `/settings/capabilities#credentials`; `/settings/secrets` → `#other-secrets`; `/settings/capability-servers` → `/settings/capabilities`. The target card's top (89 px) sits below the header's bottom (77 px). |

### Hub — `/next/settings/registry`

| Step | Outcome |
| --- | --- |
| Card shows `connected`, Hub URL `https://registry.grain.example.com`, entry `acme-widgets`, and a Credential mode with `Shared bearer token` / `Workload OIDC`. | ✅ ⛔ The title still says "Grain Registry". No last heartbeat, health, scopes, OIDC audience, platform, purpose, listed flag, registration token or `Scope overrides` card (G6). |
| Heartbeat now. | ✅ No error. The status stays `connected`. |
| Display name → `Walk Display`. Save, reload. | ✅ It persists. |

### Engine — `/next/settings/engine`

| Step | Outcome |
| --- | --- |
| Initial: poll `10s`, workers `4`, merges `5`. | ❌ The placeholder was a hardcoded `30s`, but the server's `pollIntervalDefault` is `10s`. The fallbacks were 4 and 1, but the defaults are 10 and 5 (F5). |
| Set `15s` / `3` / `2`. Save, reload. | ✅ It reads back. |
| Blank poll interval. Save, reload. | ✅ Reads back `10s`. |
| Blank Max merges. | ❌ Sent `Number("")` = 0, refused with "maxMergesPerRepo must be at least 1", and left no way back to the default (F5). After the fix: blank → 5 (merges) and 10 (workers), as on the classic pane. *Classic:* blank merges → 5. |
| Workers `0`, poll `soon`. | ✅ Refused by name ("maxWorkers must be at least 1", "pollInterval: time: invalid duration \"soon\""). |
| Sandbox commitment note. | ⛔ Missing in `/next/` (G7). |
| Keyboard. | ✅ Tab goes poll → workers → merges → newest-first switch. |

### CI — `/next/settings/ci` and Caches — `/next/settings/caches`

| Step | Outcome |
| --- | --- |
| CI initial `0,0,0,0,""`. Set `6 / 150 / 4096 / 512 / 7m`. Save, reload. | ✅ Reads back (`7m0s`). ⚠️ An unset value shows as `0`, not as the default it stands for (G7). |
| Blank "CI runs kept". | ⚠️ Stored as 0 silently (the server's "default" value), and shown as `0` (G7). |
| CI job cache `-5`. | ✅ Refused by name. |
| Caches `25` / `30`. Save, reload. | ✅ Reads back. Blank → `0` (default), shown as `0` (G7). |
| Flush proxy cache. | ⚠️ No confirmation. 501 in the demo (no proxy) (G7). |

### Sandboxes — `/next/settings/sandboxes`

| Step | Outcome |
| --- | --- |
| Backends: Granule, Docker, Podman, Kubernetes, Local host directory. | ⛔ No "None (control plane only)", and backends that cannot run here are not disabled with a reason (G8). |
| Network: None / Filtered / Full. Overcommit: Fixed / Pressure-governed auto / Disabled. | ✅ |
| vCPU `3`, memory `6144`, disk `40`. Save, reload. | ✅ Reads back. |
| Fixed CPU overcommit `0`. | ⚠️ Accepted and stored as `0` with no message (G8). |
| Auto mode. | ⛔ No auto bounds, disk floor, idle TTL value, Kubernetes defaults or priority classes, and no custom image contract (G8). |
| Probe sandbox runtime. | ❌ Sends `{}` (no `id` or `argv`); the demo answers 404 "not available" (F6, filed). |

## Findings

**Fixed in this task** (`fb93f6f2f`; regression coverage in `ui-ng/e2e/next-settings-connections.spec.js`):

- **F1 — Git Remove deleted without asking** (`/next/`). It now asks first through `g-confirm-dialog`.
- **F2 — Plain git hosts were registered as GitHubs; GitHub App was offered everywhere** (`/next/`). There is a new Host kind choice (GitHub / Plain git host), sent as `kind`. App is offered only for GitHub, which matches the classic form and `pkg/ui/team_git_credential_types.go`.
- **F3 — Capability server Remove deleted without asking** (`/next/`). It now asks first.
- **F4 — The secret value was typed in the clear, and Store failed with a 404 when there is no store** (`/next/`). The value is masked (`-webkit-text-security: disc`, still a textarea so a PEM keeps its newlines). With `enabled:false` the form says so and disables Store.
- **F5 — Engine defaults and blank-to-reset, plus legacy Integrations links** (`/next/`):
  - The poll placeholder now comes from `pollIntervalDefault`.
  - Blank counts reset to 10 and 5.
  - Each box carries a hint.
  - `/next/admin/integrations` and `/next/system/integrations` → `/next/settings/capabilities`.

- **F7 — Settings → Hosts said "hostd"** (`/next/`): "Registered workstation hostds", "New hostd name", "Register hostd" and "Hostd token". These are now "host", following the terminology rule (hosts, not hostd), and the Fleet page's "New hostd name" label is fixed too.

Classic pages were not changed: none of these bugs exist in classic, which already confirms, has kinds and resets blanks.

**Filed as a follow-up** (one task with `branch: "main"`, carrying these reproductions):

- **F6** — `/next/` "Probe sandbox runtime" posts `{}` to `POST /api/sandbox-probe`. That route needs `id` and `argv`, so even a deployment with a prober gets a useless probe. Needs a real prober to verify.
- **G1–G9** — Gaps where the `/next/` page lacks what its classic page has:
  - G1: Git — SSH/known hosts, App fields, reuse, refusal banner, host pointer.
  - G2: Hosts — `--join` snippet, code options, Edit/Rename/capacity, host address, principal and build, pause report.
  - G3: Capabilities — built-ins first, state chips, popovers, deprecations, schema forms, the `/settings/capabilities/<id>` page.
  - G4: Scope set Remove; policy verdicts in pickers.
  - G5: Deployment credentials card and SecretsPanel.
  - G6: Hub fields and Scope overrides.
  - G7: Engine commitment note; CI/Caches showing defaults as blanks with placeholders; flush confirmation.
  - G8: Sandboxes — None backend, disabled backends, auto bounds, disk floor, Kubernetes card, custom image.

## Not covered

- The classic pages were only checked for anchors and redirects. Their full walkthrough (Invite hosts, schema forms, Hub scope overrides, SecretsPanel, Sandboxes budget) did not fit this run and is part of the follow-up.
- Locked mode (config-repo lock) and role gating (`settings:write` vs `secrets:write`) were not exercised: the demo signs in as an owner and is not locked.
- Visual review: screenshots of `/next/settings/{git,capabilities,engine}` were rendered at desktop and phone, light and dark (`ui/e2e/screenshot.mjs`), and none are committed. Nobody looked at them in this run: the sandbox file reader returns PNG bytes, and with no Gemini key `LOOK=1` cannot describe them. Layout was asserted instead. A phone-width bounding-box test checks that the new Host kind choice and the Engine hints stay inside their cards, with no horizontal overflow. No "before" screenshots were taken.
