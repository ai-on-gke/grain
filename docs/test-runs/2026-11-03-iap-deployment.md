# Test run — 2026-11-03 — IAP on real deployments (test plan §4)

Task 2710. This run walks every item under "What is NOT proved" in [`docs/test-plans/2026-11-03.md`](../test-plans/2026-11-03.md) §4, which covers PRs #498, #433, #481, #461, #501, #452 and #477: the Terraform audience, the owner seeded into the allowlist, gcp-key service accounts kept off it, forged-assertion logging, the phone drawer, and Settings → Access under an external config repo.

**Result:** all five items were walked. Item 1 (rendering) passes as built. Items 2–4 turned up **three flaws, all fixed here** with regression tests:

- **F1:** refused principals, including gcp-key pool accounts, were never logged.
- **F2:** the log line for an expired or forged assertion told the operator to set an audience that was already right.
- **F3:** the phone drawer cut off even a short verified address.

Item 5 passes. After the fixes, the scripts report: rendering 4/4 Terraform runs plus the hand render, admission 17/17, browser 32/32.

Scripts, raw output and screenshots are in [`2026-11-03-iap-deployment/`](2026-11-03-iap-deployment/).

## Environment

| | |
|---|---|
| Base | `origin/main` `ac1f0e6d` (PR #458) |
| Binary | `cd ui && npm ci && npm run build && cd .. && go build -o ~/bin/grain ./cmd/grain` (embedded UI), rebuilt after each fix |
| OS / tools | Debian 12 sandbox; Go 1.26; Node 20; **Terraform v1.17.1** and **kubectl** already installed (no apt step needed); `tesseract` and ImageMagick for reading the screenshots |
| Terraform | A scratch copy of `deploy/terraform/gcp` in `/var/tmp/tf`, `terraform init -backend=false` (the real `hashicorp/google ~> 7.21` provider was downloaded). `terraform test` used **`mock_provider "google"`** (plus the `sandbox` alias) with `override_during = plan` and `command = plan`. That means **no Google API call, no credentials, no apply**. Project is `made-up-project-1234`, project number `123456789012`, backend service `generated_id` `987654321098765` |
| Server | `grain demo` behind [`tests/iap/iapsim.go`](../../tests/iap/iapsim.go), the simulated IAP from [`2026-11-02-iap-end-to-end.md`](2026-11-02-iap-end-to-end.md): issuer `:18080`, proxy `:18443`, demo `:4300`, `-as owner@example.com -ui-iap-audience /projects/123/global/backendServices/456 -ui-iap-issuer https://iap.test.local -ui-iap-keys-url http://127.0.0.1:18080/jwks`. Item 2 ran with **no** `-ui-allowed-principals` and `GRAIN_ALLOWED_PRINCIPALS` unset. The demo's stored config carries the gcp-key pool `grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com`. Items 4–5 added `-external-config-repo https://git.example.com/made-up/grain-config.git` |
| Browser | Chromium (Playwright, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, 390×844 and 1440×900, `colorScheme` and `grain.themeMode` both set to light or dark |

No orchestrator, daemon or run host was needed: nothing here dispatches a run.

## Results by item

| # | Item | Result | Evidence |
|---|---|---|---|
| 1 | `expose_ui_publicly = true`: audience wired | **PASS** | [`iap_render.tftest.hcl`](2026-11-03-iap-deployment/iap_render.tftest.hcl) `public_ui_on`: `local.grain_config.iap_audience == "/projects/123456789012/global/backendServices/987654321098765"` |
| 1 | `expose_ui_publicly = false`: audience empty | **PASS** | `public_ui_off`: `iap_audience == ""` |
| 1 | Owner reaches the allowlist | **PASS** | Both runs: `allowed_principals == "user:owner@example.com"` from `deployer_member = "user:owner@example.com"` |
| 1 | A CI service-account deployer seeds nothing; an explicit list replaces it | **PASS** | `ci_service_account_deployer`: `""`. `explicit_list`: `"user:alice@example.com,domain:corp.example"` |
| 1 | `deploy.sh` → `setup.sh` → daemon flags | **PASS** (hand render) | [`render-deploy.sh`](2026-11-03-iap-deployment/render-deploy.sh) runs the real `cfg` (from `deploy.sh`) and `resolve_allowed_principals`/`resolve_iap_audience` (from `setup.sh`) on each `grain_config` shape. On: `-ui-iap-audience /projects/123456789012/global/backendServices/987654321098765 -ui-allowed-principals user:owner@example.com`. Off: `-ui-allowed-principals user:owner@example.com`, with no audience flag. `deploy.sh` always passes both variables, even empty, so an explicit empty clears an installed audience (`TestSetupIAPAudienceReRunKeepsInstalledAndExplicitClears`) |
| 1 | `deploy/kubernetes` shows `GRAIN_ALLOWED_PRINCIPALS` | **PASS** | `kubectl create --dry-run=client` cannot run here: even client mode needs API discovery, and there is no cluster. `kubectl kustomize` over `grain.yaml` renders 17 objects, with `GRAIN_ALLOWED_PRINCIPALS: user:CHANGEME@example.com` on the grain container. `-ui-iap-audience` is a commented placeholder (CHANGEME 4 of 4) |
| 2 | Owner admitted under the default allowlist | **PASS** | `GET /api/auth/user` → `200 {"user":"owner@example.com","authMethod":"iap"}` |
| 2 | gcp-key pool email refused, not admitted as operator | **PASS** | 403 `principal "grain-sb-agent-0@…" is a gcp-key agent service account whose key is materialized into task sandboxes…`. The mixed-case spelling also gets 403. The provisioned `grain-daemon@grain-sandbox-dev…` gets 200, and a stranger gets 403 |
| 2 | …and **logged** | **FAIL → fixed (F1)** | On the base, the demo log had **no line at all** for the pool account or the stranger. After the fix: one line each, see below |
| 3 | Expired, then forged, for the same audience: both 401, both logged | **PASS** | Both get 401, and the demo log has both lines (PR #461's fix holds). A second forged request is not logged again (once per kind) |
| 3 | …and the log line points the right way | **FLAW → fixed (F2)** | Both lines ended "if the proxy that sent it is this deployment's own, set -ui-iap-audience … to match", though the audience already matched |
| 4 | 390 px, IAP: the drawer shows the email, Access settings and Log out | **FAIL → fixed (F3)** | The row, Access settings, Log out and geometry all pass, but the email is drawn **"owner@example..."**: `scrollWidth 124 > clientWidth 111`, light and dark ([before](2026-11-03-iap-deployment/before/contact-sheet.png)). After: `owner@example.com` is shown whole ([after](2026-11-03-iap-deployment/after/contact-sheet.png)) |
| 4 | Log out → `/_gcp_iap/clear_login_cookie` | **PASS** | Light and dark: the URL ends `/_gcp_iap/clear_login_cookie` |
| 4 | Access settings → Settings → Access | **PASS** | "Signed in through IAP as owner@example.com." |
| 4 | Access code: no email badge | **PASS** | `top-bar-user` count is 0 at 390 and 1440, and the drawer has no `phone-signed-in-email`, light and dark |
| 4 | Contrast / overflow | **PASS** | Email `rgb(36,34,29)` on `rgb(255,255,255)` (light) and `rgb(233,231,225)` on `rgb(20,22,26)` (dark). Page overflow is 0 |
| 5 | Settings → Access under `-external-config-repo` | **PASS** | It reads the sign-in, has no `[data-settings-readonly]` in the section, and Log out is enabled at 1440 and 390. Access has nothing to save (one "This browser" card), so "saves or refuses" does not apply |
| 5 | Tab skips locked selects | **PASS** | Settings → General shows the read-only notice ("Configuration is read-only and managed in https://git.example.com/made-up/grain-config.git (main)…") with 2 `.MuiSelect-select` inside `[data-settings-readonly]`. 80 Tab presses at 1440 and 390 focused **none** of them |

Raw output: [`admission-results.txt`](2026-11-03-iap-deployment/admission-results.txt), [`admission-log-lines.txt`](2026-11-03-iap-deployment/admission-log-lines.txt), [`drawer-results.txt`](2026-11-03-iap-deployment/drawer-results.txt).

## Findings

### F1: a refused verified principal was never logged (fixed)

**To reproduce on `ac1f0e6d`:** run [`admission.sh`](2026-11-03-iap-deployment/admission.sh) (a demo with the default allowlist and a gcp-key pool). Then `curl -H "X-Goog-IAP-JWT-Assertion: $(curl -s 'http://127.0.0.1:18080/sign?email=grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com&aud=/projects/123/global/backendServices/456&iss=https://iap.test.local')" http://127.0.0.1:4300/api/auth/user` returns 403, and `grep -c grain-sb-agent demo.log` is 0. The same is true for any principal not on the list.

**Why it matters:** a pool account's key is handed to task sandboxes, so a verified assertion for one means that a sandbox's key got through the identity proxy. That deserves a line in the log, not silence.

**Fix:** `checkAllowedPrincipal` (`pkg/ui/allowlist.go`) now logs each refusal through `logRefusedPrincipal`. It logs once per principal and kind (pool or not), and is bounded at 64 like the refused-assertion log, so a retrying browser is logged once. A pool refusal says what it means and what to do:

```
ui: refused verified principal "grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com": it is a gcp-key agent service account, whose key is handed to task sandboxes -- a sandbox's key was presented through the identity proxy; take it off the proxy's access list (iap_members) and rotate the pool's keys
ui: refused verified principal "stranger@example.com" (human): principal "stranger@example.com" is not allowed by -ui-allowed-principals
```

The response text and status are unchanged. Regression test: `TestRefusedPrincipalsAreLoggedOnce` (`pkg/ui/verifier_allowlist_test.go`), which fails on the base.

### F2: an own-audience refusal said to set the audience (fixed)

**To reproduce on `ac1f0e6d`:** with `-ui-iap-audience A`, send an expired assertion and then a forged one, both for audience `A`. Each is logged as `refused an X-Goog-IAP-JWT-Assertion (issuer …, audience "A"): … token signature is invalid … -- if the proxy that sent it is this deployment's own, set -ui-iap-audience (GRAIN_IAP_AUDIENCE) and … -ui-iap-issuer and -ui-iap-keys-url to match`. That hint is right for a new proxy's first assertion. For the deployment's own audience it is wrong, and it is wrong in exactly the case (forgery) where the operator most needs to look somewhere else.

**Fix:** `logRefusedAssertion` (`pkg/ui/assertion_diagnostics.go`) is now given the audience the verifier checked. When the assertion's audience is one of those (a comma list is supported) and the refusal is not about the issuer, the line says `this deployment's own … not a configuration mismatch: an expired one is clock skew or a replayed header, and a bad signature or unknown key ID is one the configured issuer did not sign`. Deduplication is unchanged, so PR #461's forged-after-expired line still appears. Regression test: `TestRefusedAssertionForOwnAudienceDoesNotSayToSetTheAudience`, which fails on the base.

### F3: the phone drawer cut off an ordinary verified address (fixed)

**To reproduce on `ac1f0e6d`:** at 390×844, signed in through iapsim as `owner@example.com`, open the drawer. The row reads "Signed in as / owner@example..." (OCR of [before/light-390-iap-drawer.png](2026-11-03-iap-deployment/before/light-390-iap-drawer.png)), and `phone-signed-in-email` has `scrollWidth 124`, `clientWidth 111`. The drawer is 232 px wide, and Log out (55 px) beside the address left the address about 110 px. PR #501's spec only checked a 70-character service account, where clipping is expected.

**Fix:** `SignedInRow` (`ui/src/components/Sidebar.jsx`) stacks the row: the row's button keeps "Signed in as" over the address at the full row width, and Log out sits under it at the right. A long address is still ellipsized on one line. `settings-panel.design.md` §4.3 is updated to match. `phone-signed-in-logout.spec.js` now checks that Log out is below the address and inside the row. A new case asserts that `alice.smith@example.com` is unclipped at 390 px in light and dark. On the old layout that case fails, clipped by 39 px.

### Not a defect: Sessions "missing" from the IAP drawer

In the IAP drawer screenshot the pinned list ends at Boards, while the access-code drawer shows Sessions too. A DOM probe found the Sessions entry present (count 1, and `/api/console-sessions` returns 200 with the director). The taller signed-in footer just scrolls it below the fold of the pinned list. No change.

## Looking at the screenshots

The sandbox's file reader shows PNGs as bytes and there is no Gemini key (`LOOK=1`), so each shot was read with `tesseract` on 3× crops, with dark crops inverted:

- **Drawer, IAP, after** (light/dark): `… boards 4 / system / signed in as / owner@example.com / log out / v0.0.0-…`. The address is whole, Log out sits under it, and the build stamp is below. Boxes: drawer x 0..232, row x 11..220, Log out right edge ≤ 233.
- **Drawer, IAP, before** (light/dark): `signed in as / log out / owner@example... og`.
- **Drawer, access code** (light/dark): `… boards 4 / sessions 1 / system / v0.0.0-…`, with no signed-in row.
- **1440, access code**: the header's right end reads `… contact running (2) …` with no email.
- **Settings → Access, external** (1440): "This browser / Signed in through IAP as owner@example.com. / Log out", with the header badge `owner@example.com`. The section list marks General and the rest "Managed in config repo".
- **Settings → General, external**: the read-only notice naming `https://git.example.com/made-up/grain-config.git (main)` and the edit to commit.

Contact sheets: [before](2026-11-03-iap-deployment/before/contact-sheet.png) (the two drawers), [after, 390](2026-11-03-iap-deployment/after/contact-sheet.png) (drawers by IAP and by code, Access, Access and General under the external repo), and [after, 1440](2026-11-03-iap-deployment/after/contact-sheet-desktop.png). `make ui-screenshots PAGES=/,/settings/access` was also run on the fixed build (desktop and phone, light and dark). Those pages draw the drawer closed, so the drawer shots above are the ones that show the change.

## Re-running

```
go build -o ~/bin/grain ./cmd/grain          # after cd ui && npm ci && npm run build
cp -r deploy/terraform/gcp /var/tmp/tf && cp docs/test-runs/2026-11-03-iap-deployment/iap_render.tftest.hcl /var/tmp/tf/tests/
(cd /var/tmp/tf && terraform init -backend=false && terraform test)
docs/test-runs/2026-11-03-iap-deployment/render-deploy.sh
docs/test-runs/2026-11-03-iap-deployment/admission.sh /tmp/adm
docs/test-runs/2026-11-03-iap-deployment/drawer.sh /tmp/drw
```

The tftest is kept here rather than under `deploy/terraform/gcp/tests/` because CI does not install Terraform. Its rendering checks are covered in CI by `tests/deploy` (`iap_audience_test.go`, `allowed_principals_test.go`) at the shell level.

## CI run locally

- `go test ./pkg/ui/... -count=1`: ok (87 s).
- `npx vitest run src/components/Sidebar`: 81 passed.
- `npx playwright test e2e/phone-signed-in-logout.spec.js e2e/iap-header-logout.spec.js`: 7 passed.

## What this could not cover

- **Real Google**: a real `terraform plan` against a project (the mock fills in `data.google_project.number` and the backend's `generated_id`), real IAP headers and cookies, and Google's issuer and keys. See [`2026-11-02-iap-end-to-end.md`](2026-11-02-iap-end-to-end.md) "What this simulation could not cover".
- **A real `setup.sh` install**: the flags were rendered from the real functions, but no systemd unit was written.
- **`kubectl apply --dry-run=server`**: there is no cluster.
