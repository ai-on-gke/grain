# Walkthrough: Deployment credentials card & Legacy capabilities tab retirement (2026-11-08)

This walkthrough covers `docs/test-plans/2026-11-08-afternoon.md` §1, items 1–5, for PR #929 (`f4e9db32`, `632a8ffd`, `6eb2679f`, `2510d211`). Task 3654 ran it against real servers.

## Environment

| What | Value |
|---|---|
| Base commit | `106f4efc` (`origin/main` when the run started). `origin/main` was merged in mid-run (`0a48eedf`). |
| Binaries | `grain` built from HEAD: `cd ui && npm ci && npm run build && go build -o /home/debian/bin/grain ./cmd/grain` (`v0.0.0-20261108214135-106f4efcc374`). After the fix, `/home/debian/bin/grain-fixed` was built the same way from this branch. |
| Demo A | `grain demo -github-sim -fake-ci -secrets-store -legacy-capability -addr 127.0.0.1:4261`, seeded by [`seed.sh`](2026-11-08-deployment-credentials-card/seed.sh): `github-app`, `registry-push`, `cosign-key`, `cosign-password`, `unclaimed-thing`, `cloud-bearer` (bound to the demo's `https://capabilities.demo.grain.example/cloud` server through **Bind a secret**), and 18 filler secrets. That makes 24 secrets and 13 capability servers. |
| Demo B | `grain demo -github-sim -fake-ci -secrets-store -external-config-repo <local bare repo> -addr 127.0.0.1:4262`, with no `-legacy-capability`. |
| Real daemon | [`roles.sh`](2026-11-08-deployment-credentials-card/roles.sh): `grain daemon -sandbox-port-base=0` behind `tests/iap/iapsim.go`. The browser and curl pick an identity with `X-Iapsim-Email`. |
| Roles | `reader@` = `grain/viewer`, `writer@` = `grain/contributor`, `admin@` = `grain/operator`. Two custom roles split the card's three scopes: `capadmin@` (`capabilities:admin` without `settings:write`/`secrets:write`) and `secretsonly@` (`settings:write` + `secrets:write` without `capabilities:admin`). |
| Browser | Playwright Chromium (`/var/cache/playwright/chromium-1234`), Node v20.19.0. Viewports 1440×900 and 390×844, light and dark. |
| Harnesses | [`walkthrough.mjs`](2026-11-08-deployment-credentials-card/walkthrough.mjs) (items 1–3, 5), [`pickers.mjs`](2026-11-08-deployment-credentials-card/pickers.mjs) (item 4), `roles.sh` (item 2 curl matrix), `seed.sh` |
| Raw results | `results-item2-before-fix.jsonl`, `results-item2-after-fix.jsonl`, `results-items-1-3-4-5.jsonl`, `results-item4-pickers.jsonl`, `routes.md` in the same directory |

Nothing was stubbed. Every check ran against a live demo or daemon.

## Findings at a glance

| # | Item | Finding | Severity | Outcome |
|---|---|---|---|---|
| F1 | 2 | A role holding `capabilities:admin` without `settings:write`/`secrets:write` got an **editable** GitHub host and GCP fields, plus **Set secret**, key deletes and **Delete secret**. The live server refused each with 403 (`PUT /api/secrets/capadmin-ui-probe/token 403`, `DELETE /api/secrets/cosign-key 403`). | Medium | **Fixed** |
| F2 | 1 | `/admin/integrations` and `/system/integrations` (no hash) landed at the **top** of Capabilities. The card was 5,865 px below at desktop and 9,104 px below on the phone, while `/settings/credentials` scrolled to it. | Low | **Fixed** |
| F3 | 1 | `/settings/secrets` and every `#other-secrets` link put the Other secrets heading flush against the foot of the sticky header: a 0 px gap at both widths (`top` 77 = header 77 at desktop, 57 = 57 on the phone). SettingsCard anchors keep 16 px. | Low | **Fixed** |
| F4 | 4 | Settings → Tasks **Default legacy capabilities** shows "None offered -- grant access under Grants" even with `-legacy-capability`, where the New task form and the task pane offer **Example legacy capability**. It reads `GET /api/settings`' `capabilities`, which now holds only the retired catalogue rows (all `grantable: false`), instead of `GET /api/config`'s offered list. | Medium | Follow-up (overlaps §2 of the same plan) |
| F5 | 2 | `secretsonly` (holds `settings:write` and `secrets:write`, both 200 from the server) sees the whole card read-only because it lacks `capabilities:admin`. The UI is stricter than the server. | Low (product decision) | Follow-up |
| F6 | 1 | Settings search ranks other entries first for some old words. `legacy` puts "Add a host (legacy token)" above "Deployment credentials", and `secrets` puts "Config repo" above it. Both still list the card, and clicking it lands on `#credentials`. | Cosmetic | Reported only |

## Fixes

- **F1** (`ui/src/components/SettingsPage.jsx`, `settings/CredentialsSection.jsx`, `SecretsPanel.jsx`):
  - SettingsPage puts `settingsWriteRefusal` and `secretsWriteRefusal` (from `accessDeniedReason`) on the section state.
  - CredentialsSection disables the GitHub host and GCP fields when `settings:write` is refused, and passes the new `readOnly`/`readOnlyReason` props to `SecretsPanel` when `secrets:write` is refused.
  - `SecretsPanel` wraps its list and form in `ReadOnlyFieldset`, with the reason as the tooltip, and drops the chips' `onDelete`.
  - The card says why in `data-testid="credentials-read-only"`, but only when the section itself is not already refused. The page-level note covers that case.
  - Roles without `capabilities:admin` behave as before.
- **F2** (`ui/src/settingsSections.js`): `settingsAliasAnchorAt` maps `/(admin|system)/integrations` with no hash to `credentials`.
- **F3** (`SecretsPanel.jsx`): `#other-secrets` gets `scrollMarginTop: 16`, like every SettingsCard.

### Regression tests (each failed before its fix)

| Test | Covers | Before the fix |
|---|---|---|
| `ui/src/components/settings/CredentialsSection.test.jsx` → "CredentialsSection role gating" (3 tests) | F1 | 2 failed |
| `ui/src/components/SecretsPanel.test.jsx` → "draws its controls disabled, saying why, when readOnly" | F1 | failed |
| `ui/src/settingsSections.test.js` → "scrolls the old Integrations tab's addresses to the card too" | F2 | failed |
| `ui/e2e/deployment-credentials-role-gating.spec.js` → capadmin card at 1440 and 390 px | F1 | failed against the pre-fix bundle (`credentials-read-only` missing) |
| same spec → "the old Integrations tab's addresses scroll to the Deployment credentials card" | F2 | failed against the pre-fix bundle (`toBeInViewport`) |
| same spec → "/settings/secrets leaves Other secrets clear of the header" | F3 | reproduced by `walkthrough.mjs` against the pre-fix demo (gap 0 px) |
| same spec → "an operator still gets every control" | guard | passed before and after |

Local runs after the fixes:
- Vitest `src/components/settings/`, `SecretsPanel`, `SettingsPage`, `settingsSections`, `paths`: all green (221 + 129 tests).
- Playwright `deployment-credentials-role-gating`, `deployment-credentials-gcp-help`, `deployment-credentials-links`, `capability-server-settings`, `config-repo-iac-modes`, `settings-panel-walkthrough`, `sessions-worktree-pinned-nav-walkthrough`: all green.
- `make fmt-frontend` and `go test ./tests/hygiene/...`: clean.

## Item 1 — Old entry points, hashes, back/forward, search, hint links

**Entry points.** Demo A, desktop and phone (the phone layout's Settings nav is the drawer/section list).

| Address | Lands on | Target top (desktop / phone) | In view |
|---|---|---|---|
| `/settings/credentials` | `/settings/capabilities#credentials` | 88 / 73 | yes |
| `/settings/credentials#githubHost` | `…#githubHost` | 439 / 371 | yes |
| `/settings/credentials#gcp-settings` | `…#gcp-settings` | 478 / 440 | yes |
| `/settings/secrets`, `/settings/secrets#other-secrets` | `…#other-secrets` | 77 / 57 (flush with the header, **F3**) | yes |
| `/settings/legacy-capabilities`, `…#credentials` | `…#credentials` | 88 / 73 | yes |
| `/admin/integrations`, `/system/integrations` | `/settings/capabilities` | **5865 / 9104 (F2)** | **no**, fixed |
| `/admin/integrations#githubHost` | `…#githubHost` | 439 / 371 | yes |
| `/system/integrations#gcpProject` | `…#gcpProject` | 451 / 372 | yes |

- **Back/forward:** `/settings/general` → `/settings/credentials#githubHost` → Back lands on `/settings/general`, and Forward lands on `/settings/capabilities#githubHost` with the field in view. There is no redirect loop and no extra history entry trapping Back, at either width.
- **Settings search** (both widths):
  - `legacy capabilities` and `credentials` → only "Deployment credentials", which lands on `#credentials`.
  - `GitHub host` → GitHub host, then Speak plain HTTP; lands on `#githubHost`.
  - `GCP project` → GCP project; lands on `#gcpProject`.
  - `legacy` and `secrets` list the card second (**F6**).
  - The keywords the plan expected to be missing are back on `card:capabilities:credentials` (an earlier connections walkthrough restored them).
- **In-app links:** the GCP service-account help's "Other secrets" link lands on `#other-secrets`, and Other secrets' "Capability servers" link lands on `#capability-servers`, at both widths. None of the hints still names the old tab.

## Item 2 — Role and lock gating against a real daemon

**Server enforcement** (`roles.sh`, curl through iapsim; full table in [`routes.md`](2026-11-08-deployment-credentials-card/routes.md)):

| Identity | GET settings / secrets | PUT /api/settings | PUT secret | DELETE secret |
|---|---|---|---|---|
| reader (`grain/viewer`) | 200 / 200 | 403 | 403 | 403 |
| writer (`grain/contributor`) | 200 / 200 | 403 | 403 | 403 |
| admin (`grain/operator`) | 200 / 200 | 200 | 200 | 200 (404 for a missing key) |
| capadmin | 200 / 200 | 403 | 403 | 403 |
| secretsonly | 200 / 200 | 200 | 200 | 200 |

`GET /api/secrets` never returned a value to any identity: the seeded `…VALUE` strings were absent from every body.

**What the browser drew** (`/settings/capabilities#credentials`, desktop):

| Identity | Fields (host/GCP) | Delete secret | Set secret | Note | Before fix |
|---|---|---|---|---|---|
| reader | disabled | 0/5 usable | disabled | page: "Needs capabilities:admin, which your role grain/viewer does not grant." | same |
| writer | disabled | 0/5 | disabled | page: same, for grain/contributor | same |
| admin | editable | 5/5 | enabled | none | same; set, host save and delete all succeeded |
| capadmin | **disabled** | **0/5** | **disabled** | card: "Read-only for you. Needs settings:write… Needs secrets:write…" | **editable, 5/5, enabled; the server answered 403 (F1)** |
| secretsonly | disabled | 0/5 | disabled | page: "Needs capabilities:admin…" | same (**F5**) |

**Config-locked mode** (demo B, `-external-config-repo`):
- GitHub host, GCP project and GCP service-account email are all disabled. `allowInExternalMode` does not unlock the GCP fields.
- The secret form stays usable (`Set secret` 1/1).
- The card itself shows no config-repo notice. The page's `external-config-repo-banner` says why instead.

## Item 3 — Secrets lifecycle with a real secrets store

Run on demo A, desktop, as the operator.

- `cloud-bearer`, the server-bound secret, is listed once, under "Used by capability servers", linking to its server. It is not repeated under Other secrets.
- The retired-capability secrets (`github-app`, `registry-push`, `cosign-key`, `cosign-password`) each appear exactly once under Other secrets, each with **Delete secret**.
- Changes refreshed the list without a reload:
  - Setting `walk-new/token` added the row.
  - Setting `walk-new/second` then deleting it by its chip removed only that key.
  - **Delete secret** on `walk-new` removed the row.
- **Pending restart** chips: two, for `githubHost` and `githubInsecureHttp`. That is the demo's `restartRequired`, since both GitHub fields are restart-bound. No other card field showed one.
- No console errors and no failed requests.
- Neither the DOM nor any `/api/secrets`, `/api/settings` or `/api/capability-servers` response contained a seeded or typed value.

## Item 4 — Picker hint copy

[`pickers.mjs`](2026-11-08-deployment-credentials-card/pickers.mjs) opened each legacy-capability picker at 1440 and 390 px:

| Surface | With `-legacy-capability` (demo A) | Without (demo B) |
|---|---|---|
| New task (`/new`) | Example legacy capability row | hint, inside the viewport; Grants picker on the same form |
| Task pane `CapabilityToggles` (`/tasks/1`) | Example legacy capability row | hint, inside the viewport; Grants on the pane |
| Settings → Tasks Default legacy capabilities | **hint** (**F4**) | picker disabled (config-locked) |
| Repository page, Template, Schedule | the probe did not reach a picker (see below) | same |

Where the hint renders, it reads "None offered -- grant access under Grants", fits inside the viewport at 390 px, and sits on a surface that also shows a Grants picker.

The probe's routes for the Repository page (`/repos/acme/app`), Template and Schedule did not reach a legacy picker in the demo. Those three read the same `config.capabilities` and constant as the New task form (`RepoPage.jsx:811/815`, `TemplateOverlay.jsx:127`, `ScheduleOverlay.jsx:358`), and their copy is covered by Vitest. They were not walked in the browser here.

## Item 5 — Visual layout of a long Capabilities page

Demo A had 13 capability servers and 24 secrets. `/settings/capabilities#credentials` was checked at 1440×900 and 390×844, light and dark:

- `scrollWidth <= clientWidth` in all four.
- No element inside `#credentials` overflows horizontally.
- The `#credentials` anchor lands 11 px (desktop) and 16 px (phone) below the header.

`make ui-screenshots PAGES=/settings/capabilities,/settings/capabilities#credentials` was taken before the fix (`screenshots/before`) and after it (`screenshots/after`, plus `/settings/secrets`), both on a `-secrets-store -legacy-capability` demo. A capadmin pair (`ACCESS=` with `capabilities:admin` only) was also taken before and after.

The sandbox's file reader shows PNGs as bytes and `LOOK=1` has no Gemini key here, so layout was judged from the DOM checks above and the bounding-box assertions in `deployment-credentials-role-gating.spec.js` (note inside the card, no horizontal overflow), not by eye.

## Follow-ups filed

- F4: Settings → Tasks Default legacy capabilities should offer `GET /api/config`'s capabilities, as the other pickers do. It touches the same `configuredCapabilities` / `-legacy-capability` area as §2, so it was left out of this change.
- F5: decide whether the Deployment credentials card should follow its own write scopes (`settings:write` for the fields, `secrets:write` for the secrets) instead of `capabilities:admin`, so that roles like `secretsonly` can use what the server allows.

## Other notes

- The `SecretsPanel` "Not available" alert still cites `bwsalmon/agents#357`, a retired repository. Not changed here.
- `release/v3.2.0` still has the Legacy capabilities tab, not this card. F1–F3 are defects of the card as it exists on `main`, so no backport is proposed.
