# Walkthrough: Per-model reasoning effort selectors and no-effort model hiding (`2026-11-19` §6)

- **Date:** 2026-11-19, 23:13–23:55 UTC.
- **Test plan:** `docs/test-plans/2026-11-19.md` §6 (from [Task #7303](/tasks/7303), PR #1889; audited PRs #1554 and #1551).
- **Task:** [Task #7322](/tasks/7322), branch `grain/53549e42/task-7322-walkthrough-per-model`, base `main`.
- **Base commit walked:** `1a449c7d513ee831d3723425c05c4f33f5142a5b` (`Merge pull request #1913`).
- **Overlap check:** `git log origin/main -- docs/test-runs/` confirmed no prior `2026-11-19` report covered §6 (`2026-11-19-connections-infrastructure-settings.md` covered Settings → Git/Hosts/Capabilities/Hub/Engine/CI/Caches/Sandboxes only).
- **Outcome:**
  - All §6 walkthrough flows (save/reload & `deployDraft` dirty state, "Other models" adder reconciliation, stale stored efforts on no-effort models on both `/next/settings/models` and classic `/settings/models`, real runner argv and Codex `config.toml` verification across all 5 pinned models, and responsive/accessibility checks) were executed end-to-end against a real `grain daemon -host-sandboxes -sandbox-port-base=0` on **both SQLite and PostgreSQL**.
  - **2 defects found and fixed in `ui-ng/`** (F1, F2), with Angular unit tests and Playwright e2e regression coverage added and re-verified on both SQLite and PostgreSQL (37/37 UI checks and 5/5 runner invariants green on both databases):
    - **F1:** On `/next/settings/models`, a stale stored effort on a no-effort **default or fallback** model (`opus-5.5-high`, `auto`, `claude-opus-5-5`) displayed `<effort> (not offered)` instead of `<effort> (not applied)` and hid the explanatory reason (`es.reason`), unlike both `/next/`'s "Other models" table and classic `/settings/models` (`ModelDefaults.jsx`).
    - **F2:** `<g-pill-select>`'s `autoOpenUp` check hard-coded a `220px` height threshold instead of estimating from the option list; at `1024×860`, opening the 9-option Codex reasoning effort menu (`320px` tall) overflowed `33px` below the viewport bottom (`y=573`, `bottom=893 > 860`).
  - **2 non-blocking observations recorded** (O1: PR #1551 expanded `antigravity.Efforts()` to `["low","medium","high","xhigh","max"]`, so `"xhigh"` is valid on Antigravity while Codex-only `"minimal"`/`"none"`/`"ultra"` reconcile to `"high"`; O2: classic `useDeployment.js` optimistic `PUT /api/deployment` concurrency when rapid consecutive dropdown saves race without waiting for the prior response).

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12 (`bookworm`), `linux/amd64`, Go `1.26.10`, Node `v20.19.0`, npm `10.8.2`, `jq`. |
| Packages installed | `postgresql` (`15.20`), `postgresql-client`, `sqlite3` (`3.40.1`). |
| Binary | Built from `HEAD` (`make frontend && go build -o /home/debian/bin/grain ./cmd/grain`), embedding real classic `ui/` (`pkg/ui/static`) and Angular `ui-ng/` (`pkg/ui/static-next`). Rebuilt after fixes. |
| Daemon (SQLite) | Real `grain daemon -host-sandboxes -sandbox-port-base=0 -ui-addr 127.0.0.1:4282 -ci=false -poll-interval 1s -max-workers 3 -max-mergers 0 -as alice@example.com` with `-agy-path`, `-codex-path`, and `-claude-path` pointing at argv-logging wrapper scripts. |
| Daemon (PostgreSQL) | Identical real `grain daemon` on `127.0.0.1:4283` with `-postgres-url postgres://walk:walk@127.0.0.1:5432/me_walk?sslmode=disable`. |
| Browser | Playwright Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900`, `390×844`, `1024×860`, and `2048×1200`, in `light` and `dark` colour schemes, with axe-core WCAG 2.1 AA audits (`serious`/`critical` impact floor = 0). |
| Model / `LOOK=1` | No `/home/debian/.gemini-api-key` in sandbox; screenshots verified via Playwright bounding boxes, light/dark luminance checks, byte-diff comparisons, and attached before/after contact sheets. |

## Helper scripts

All helper scripts live in `docs/test-runs/2026-11-19-settings-model-efforts/` (Apache-2.0 licensed):

- `walk.sh` — starts a real `grain daemon` on `sqlite` or `postgres`, runs `walk_ui.mjs`, inspects `grain_config` directly via `sqlite3`/`psql`, seeds 5 models with efforts (including stale ones on no-effort models), files and dispatches 5 standalone (`noRepo: true`) tasks, and asserts exact runner argv + Codex `config.toml`.
- `walk_ui.mjs` — drives Playwright Chromium across all UI scenarios on `/next/settings/models` and classic `/settings/models`, responsive viewports (`1440×900`, `390×844`, `1024×860`, `2048×1200`), light/dark themes, bounding-box alignment, menu viewport containment, and axe-core checks.
- `fake-agy.sh`, `fake-codex.sh`, `fake-claude.sh` — CLI wrappers that record exact JSON `argv` (and for `codex`, `$CODEX_HOME/config.toml` and its `model_reasoning_effort` line) into `$WALK_LOG_DIR/invocations.jsonl`, answer `codex --version` (`codex-cli 0.169.0`) and `codex debug models`, and emit valid stream-json transcripts so each dispatched run finishes cleanly.

## Seeded connections & vocabularies

Three ambient-auth connections were configured through `PUT /api/deployment/agent-backends/{id}`:

- `agy-main` (`framework: "antigravity"`, `authMode: "ambient"`): `gemini-3.1-pro`, `opus-5.5-high`, `opus-5.5-xhigh`, `gemini-4-carbon-xhigh`, `auto`.
- `codex-main` (`framework: "codex"`, `authMode: "ambient"`): `gpt-5.1-codex`, `gpt-5.1-codex-max`.
- `claude-main` (`framework: "claude"`, `authMode: "ambient"`): `claude-opus-5-5`.

`GET /api/settings` on the live daemon reported:

- `geminiEfforts`: `["low","medium","high","xhigh","max"]`
- `codexEfforts`: `["none","minimal","low","medium","high","xhigh","max","ultra"]`

## Walkthrough results

| Check | SQLite (before) | PostgreSQL (before) | SQLite (after fixes) | PostgreSQL (after fixes) | Notes |
| --- | --- | --- | --- | --- | --- |
| `GET /api/deployment` omits empty `modelSettings` | PASS | PASS | PASS | PASS | `modelSettings` absent when empty (`omitempty`). |
| No-effort fallback models hide picker (`claude-opus-5-5`, `opus-5.5-high`) | PASS | PASS | PASS | PASS | Shows `"Claude takes no reasoning effort"` and `"Effort is part of the model name: high"`. |
| Unset effort -> set `high` -> revert to `"Model default"` clears save bar | PASS | PASS | PASS | PASS | `"1 unsaved change"` appears on set and disappears cleanly on revert (no stuck `deployDraft`). |
| Set effort on default (`gemini-3.1-pro`=`high`) + Codex fallback (`gpt-5.1-codex`=`xhigh`), save & reload | PASS | PASS | PASS | PASS | Persisted across reload, API, and `grain_config.model_settings`. |
| Modify stored effort in place (`high` -> `medium`, same row count = 2), save & reload | PASS | PASS | PASS | PASS | `formatSettingValue` detected the object-slice edit (`1 unsaved change`), persisted `medium` on reload. |
| "Other models" adder initially disabled; omits empty `"Model default"` | PASS | PASS | PASS | PASS | Both effort select and `Set model effort` stay disabled until an effort-taking model is picked. |
| Adder switch Codex `"xhigh"` -> bare Antigravity (`gemini-3.1-pro`) | PASS | PASS | PASS | PASS | Stays valid (`"xhigh"` is in `geminiEfforts`, see O1). |
| Adder switch Codex-only `"minimal"` -> bare Antigravity (`gemini-3.1-pro`) | PASS | PASS | PASS | PASS | Reconciled immediately to `"high"`, never leaking `"minimal"`. |
| Adder no-effort disabling (`claude-opus-5-5`, `auto`, `opus-5.5-high`, `gemini-4-carbon-xhigh`) | PASS | PASS | PASS | PASS | Effort select and `Set model effort` disable with exact per-category hints. |
| Stale effort in `/next/` "Other models" table (`opus-5.5-high`, `auto`, `claude-opus-5-5`) | PASS | PASS | PASS | PASS | All 3 marked `(not applied: <reason>.)`; `Remove` + Save cleared cleanly. |
| Stale effort on `/next/` default & fallback models (`opus-5.5-high`, `auto`, `claude-opus-5-5`) | **FAIL (F1)** | **FAIL (F1)** | **PASS** | **PASS** | Before fix: showed `(not offered)` with no reason. After fix: shows `(not applied)` + `Not applied: <reason>. Choose "Model default" to clear it.`, clears cleanly to no-effort hint. |
| Stale effort on classic `/settings/models` (`ModelDefaults.jsx`) | PASS | PASS* | PASS | PASS | All 3 show `(not applied)` and `Not applied: <reason> Choose "Model default" to clear it.`; selecting `"Model default"` clears cleanly (*see O2 for rapid concurrent click note). |
| Direct DB row inspection (`grain_config.default_model`, `grain_config.model_settings`) | PASS | PASS | PASS | PASS | Exact JSON array persisted in both SQLite (`store/grain.db`) and PostgreSQL (`me_walk`). |
| Responsive `/next/settings/models` at `1440×900`, `390×844`, `2048×1200` (light & dark) | PASS | PASS | PASS | PASS | Fixed `210px` effort column aligned within `0.1px` (`x=976.6` @ 1440, `x=1132.6` @ 2048); phone wraps cleanly with `0` horizontal overflow; 0 axe violations; 0 page errors. |
| Responsive `/next/settings/models` at `1024×860` (light & dark) | **FAIL (F2)** | **FAIL (F2)** | **PASS** | **PASS** | Before fix: 9-option Codex menu clipped below viewport (`y=573, h=320 -> bottom=893 > 860`). After fix: flips upward cleanly (`y=209, h=320 -> bottom=529 <= 860`). |
| Responsive classic `/settings/models` at `1440×900` & `390×844` (light & dark) | PASS | PASS | PASS | PASS | Zero horizontal overflow, 0 serious/critical axe violations, 0 page errors. |

## Runner argv & Codex `config.toml` verification (SQLite & PostgreSQL)

With `deployment.modelSettings` pre-seeded to:

```json
[
  {"model": "gemini-3.1-pro", "effort": "high"},
  {"model": "opus-5.5-xhigh", "effort": "low"},
  {"model": "auto", "effort": "high"},
  {"model": "gpt-5.1-codex-max", "effort": "xhigh"},
  {"model": "claude-opus-5-5", "effort": "high"}
]
```

Five real tasks (`noRepo: true`, `approved: true`) were dispatched on both SQLite and PostgreSQL daemons. Exact captured wrapper invocations (identical on both databases):

| Pinned model | Wrapper | Captured model & effort evidence | Verdict |
| --- | --- | --- | --- |
| `gemini-3.1-pro` | `agy` | `["--input-format","stream-json","--output-format","stream-json","--dangerously-skip-permissions","--disable-slash-commands","--print-timeout","24h0m0s","--model","gemini-3.1-pro","--effort","high","--add-dir",...]` | PASS (`--effort high` passed) |
| `opus-5.5-xhigh` | `agy` | `["--input-format","stream-json",...,"--model","opus-5.5-xhigh","--add-dir",...]` (no `--effort`) | PASS (stale `low` dropped) |
| `auto` | `agy` | `["--input-format","stream-json",...,"--model","auto","--add-dir",...]` (no `--effort`) | PASS (stale `high` dropped) |
| `gpt-5.1-codex-max` | `codex` | argv: `["exec","--json","--skip-git-repo-check","--model","gpt-5.1-codex-max","-"]`; `$CODEX_HOME/config.toml`: `model_reasoning_effort = "xhigh"` | PASS (`xhigh` configured in `config.toml`) |
| `claude-opus-5-5` | `claude` | `["-p","--tools","","--mcp-config",...,"--model","claude-opus-5-5","--settings",...]` (no `--effort`) | PASS (stale `high` ignored) |

Note that Codex receives reasoning effort via `$CODEX_HOME/config.toml` (`model_reasoning_effort = "xhigh"`, written by `pkg/agent/codex/codex.go`), not a CLI flag.

## Defects found and fixed

### F1: Stale stored effort on no-effort default/fallback models on `/next/settings/models` showed `(not offered)` and omitted the reason

- **Reproduction:**
  1. Seed `deployment.defaultModels = ["opus-5.5-high", "auto", "claude-opus-5-5", "gpt-5.1-codex"]` and `deployment.modelSettings = [{"model":"opus-5.5-high","effort":"low"},{"model":"auto","effort":"high"},{"model":"claude-opus-5-5","effort":"high"}]`.
  2. Open `/next/settings/models`.
  3. **Before fix:** the default model pill displayed `low (not offered)` with no explanation, and the fallback pills displayed `Effort high (not offered)` with no explanation — diverging from both `/next/`'s "Other models" table (`(not applied: …)`) and classic `/settings/models` (`ModelDefaults.jsx`, which shows `(not applied)` and `Not applied: <reason> Choose "Model default" to clear it.`).
- **Root cause:** `SettingsComputeComponent.effortOptionsFor()` (`ui-ng/src/app/settings/settings-compute.component.ts`) unconditionally appended `(not offered)` for any stored effort not in `choices`, and the default/fallback templates only rendered `es.reason` in the `@else` branch (when `!modelEffort(m)`).
- **Fix:**
  - Updated `effortOptionsFor()` to label out-of-choices values `${cur} (not applied)` when `!es.separate` and keep `${cur} (not offered)` when `es.separate` (a valid-effort model holding an unknown effort string).
  - Rendered `Not applied: {{ es.reason }}. Choose "Model default" to clear it.` beneath the default model effort pill (`data-testid="effort-default-model-unused"`) and on an indented full-width wrap line (`.mp-chain-effort-hint`, `data-testid="effort-fallback-unused-<idx>"`) on fallback rows when `!es.separate && modelEffort(m)`, while bounding pill width inside `.mp-chain-effort` (`max-width: 100%`) so the fixed `210px` desktop effort column stays aligned and never overlaps row actions.
- **Regression tests:**
  - `ui-ng/src/app/settings/settings-models.component.spec.ts` (`marks stale stored effort on no-effort default and fallback models as not applied and lets them be cleared`).
  - `ui-ng/e2e/next-settings-model-efforts.spec.ts` (`marks stale effort on no-effort default/fallback models as not applied and keeps tall effort menus in viewport`, `@phone @dark`).

### F2: `<g-pill-select>` clipped tall reasoning-effort menus past the bottom of the `1024×860` viewport

- **Reproduction:**
  1. Open `/next/settings/models` at `1024×860` (light or dark) with a Codex fallback model (`gpt-5.1-codex` or `gpt-5.1-codex-max`, 9 options in the effort menu: `Model default` + 8 Codex efforts).
  2. Click `[data-testid="effort-fallback-0"]`.
  3. **Before fix:** the menu opened downward at `y=573`, `height=320` (`bottom=893 > 860`), clipping `33px` past the bottom edge of the viewport even though `533px` of space was available above.
- **Root cause:** `PillSelectComponent.openMenu()` (`ui-ng/src/app/shared/pill-select.component.ts`) hard-coded `rect.bottom + 220 > vh - 12 && rect.top > 220` instead of accounting for the option list's height (`max-height: min(356px, calc(100dvh - 64px))`).
- **Fix:** Estimated menu height from `this.options()` (capped by `356px` and `vh - 64`) and flipped `autoOpenUp` when `spaceBelow < estimatedHeight && spaceAbove > spaceBelow`. At `1024×860`, the menu now opens upward at `y=209`, `bottom=529 <= 860`.
- **Regression tests:**
  - `ui-ng/src/app/shared/pill-select.component.spec.ts` (3 unit tests covering tall menus flipping upward, short menus staying downward, and top-of-viewport menus staying downward).
  - `ui-ng/e2e/next-settings-model-efforts.spec.ts` (`1024×860` viewport containment assertion on the 9-option Codex effort menu).

## Observations (non-blocking)

- **O1 (`xhigh` in Antigravity vocabulary after PR #1551):** PR #1551 (`cf024c5e4`) updated `antigravity.Efforts()` from `["low","medium","high"]` to `["low","medium","high","xhigh","max"]` so suffix parsing covers `-xhigh` and `-max`. As a result, switching the "Other models" adder from Codex `"xhigh"` to bare Antigravity `gemini-3.1-pro` keeps `"xhigh"` because it is now valid in `geminiEfforts`; switching from Codex-only `"minimal"` (or `"none"` / `"ultra"`) reconciles cleanly to `"high"`.
- **O2 (classic `useDeployment.js` optimistic `PUT /api/deployment` concurrency):** Classic `ui/src/useDeployment.js` optimistically mutates local `deployment` state before `PUT /api/deployment` returns and does not serialize back-to-back saves. When clearing three stale entries back-to-back on classic `/settings/models` without waiting for each `PUT` response, concurrent `PUT /api/deployment` requests can race on `staterepo` export/sync if the browser tab navigates or closes mid-request. Waiting for each `PUT /api/deployment` response clears all three cleanly on both SQLite and PostgreSQL. `/next/settings/models` is unaffected because it stages edits in `deployDraft` and sends a single `PUT /api/deployment` on Save.

## Verification & UI design-book review

- **Unit tests:**
  - `cd ui-ng && npm test` — 77/77 files, 832/832 tests passed (including new `settings-models.component.spec.ts` and `pill-select.component.spec.ts` specs).
  - `cd ui-ng && npm run lint && npm run build` — lint clean, production build clean, `model-picker.css` `6,218` bytes (under 8 KB budget).
  - `cd ui && npx vitest run src/components/settings/ModelDefaults.test.jsx src/state.test.js` — 261/261 passed.
  - `go test ./tests/hygiene/...` — passed.
  - `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/agent/antigravity ./pkg/agent/codex ./pkg/agent/claude ./pkg/model -count=1` — passed.
- **Playwright e2e (`ui-ng/e2e/`):**
  - `npx playwright test e2e/next-settings-model-efforts.spec.ts e2e/next-settings-models.spec.js e2e/next-full-system.spec.js` — 23/23 passed across `desktop`, `phone`, and `dark` projects (including WCAG AA axe scans and colour-contrast checks).
- **Screenshots & three-angle review (`design/design/ui-design-book.design.md` §10–11):**
  - Captured `before` and `after` contact sheets via `ui/e2e/screenshot.mjs` (`/next/settings/models`, `/settings/models`, desktop `1440×900` and phone `390×844`, light and dark) plus a walkthrough before/after comparison sheet (`walk-contact-sheet.png`). Verified non-blank rendering, clear light/dark luminance separation (`avgLum` `241.7–247.0` light vs `33.4–35.8` dark), and visible before/after stale-chain diff (`151,269` vs `159,649` bytes).
  - **Design:** `.mp-chain-effort-hint` uses `.mp-chain-note` (`--md-sys-color-on-surface-variant`, 12/16 body font), keeps desktop `.mp-chain-effort` fixed-width aligned (`x=976.6` @ 1440, `x=595.7` @ 1024, `x=1132.6` @ 2048), caps pill width inside the `210px` column (`max-width: 100%`), and stacks cleanly on `390×844` phone with zero overflow.
  - **Functional:** Clearing stale efforts on default/fallback rows immediately reverts to the no-effort hint and persists empty `modelSettings` on Save; unknown efforts on effort-taking models still display `(not offered)` without an unused hint; tall `g-pill-select` menus stay inside the viewport across all tested breakpoints.
  - **Adversarial:** Checked read-only / disabled states, empty option arrays (`Math.max(0, vh - 64)` guard), case-insensitive model matching, zero console/page errors, and zero axe violations. No P0/P1 findings remain.
