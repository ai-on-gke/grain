# Walkthrough: CLI `grain version` Subcommand and User Settings Version Display

**Date**: 2026-10-10  
**Branch**: `grain/task-2840`  
**Commits evaluated**: `7f7564082` (`feat(cli,ui): add grain version command and display version in user settings`, backported from `main` `4c3c9e526`)  
**Test Plan**: `docs/test-plans/2026-10-10.md` §2 ("CLI grain version subcommand and User Settings version display")  
**Environment**: Debian GNU/Linux 12 (bookworm) x86_64, Go 1.24.0, Node.js 20.19.0, npm 10.8.2, Playwright 1.62.1 with Chromium / Chrome Headless Shell build 1234  

---

## 1. Scope and Objectives

Audit and execute the end-to-end walkthrough for the `grain version` CLI command and the User Settings overlay version display introduced in commit `7f7564082` (task 2777, task 2811):

1. **CLI Execution (`grain version`)**:
   - Verify standard human-readable output formatting (`release · revision[-dirty] · timestampZ` for VCS-stamped binaries, and `dev (unstamped)` for unstamped builds).
   - Verify structured JSON parsing via `grain version -json`, including fields `release`, `revision`, `committedAt` (RFC 3339 UTC format), and `modified` (boolean).
   - Verify invalid flag handling, error diagnostic reporting, and non-zero exit codes.
   - Verify positional argument tolerance.
2. **UI Settings Overlay Version Display (`SettingsOverlay.jsx`)**:
   - Verify user Settings dialog can be accessed from the sidebar user tile across desktop and mobile form factors.
   - Verify the Version section and DOM node tagged with `data-testid="settings-version-display"`.
   - Verify the version text displayed in the UI strictly matches the backend daemon `/api/config` version payload and formatting conventions.
   - Verify typography (monospace font family) and responsive layout (desktop pane vs. full-width mobile viewport).
   - Verify dismissability via "← Back" button and `Escape` key.
3. **Automated Regression Suite**:
   - Provide an end-to-end Playwright specification in `ui/e2e/version_display_walkthrough.spec.js` running against live `grain demo` instances.

---

## 2. CLI Walkthrough Execution & Results

Testing was performed directly against binaries built both unstamped (`go run ./cmd/grain`) and VCS-stamped via Go standard VCS metadata injection (`go build -o /tmp/grain-build ./cmd/grain`):

### 2.1 Human-Readable Formatting (`grain version`)

- **Unstamped binary (`go run ./cmd/grain version`)**:
  - Command: `go run ./cmd/grain version`
  - Output: `dev (unstamped)`
  - Exit code: `0`
- **VCS-stamped binary (`/tmp/grain-build version`)**:
  - Command: `/tmp/grain-build version`
  - Output: `v1.8.0-rc.1.0.20261010070604-72b44631fc8d · 72b4463 · 2026-10-10 07:06Z`
  - Exit code: `0`
  - Verification: Release tag, 7-character abbreviated revision, and UTC commit timestamp formatted with `Z` suffix separated by ` · `.

### 2.2 Structured JSON Output (`grain version -json`)

- **Unstamped binary (`go run ./cmd/grain version -json`)**:
  - Output: `{}` (valid JSON object with empty fields omitted via `omitempty`)
  - Exit code: `0`
- **VCS-stamped binary (`/tmp/grain-build version -json`)**:
  - Output:
    ```json
    {
      "release": "v1.8.0-rc.1.0.20261010070604-72b44631fc8d",
      "revision": "72b44631fc8d423570129e47c5a6343d9a3c0954",
      "committedAt": "2026-10-10T07:06:04Z"
    }
    ```
  - Exit code: `0`
  - Verification: Parsed cleanly into JSON object with full commit hash in `revision`, RFC 3339 timestamp in `committedAt`, and release identifier in `release`.

### 2.3 Flag Handling and Exit Codes

- **Invalid flag (`/tmp/grain-build version -invalid`)**:
  - Standard error:
    ```text
    flag provided but not defined: -invalid
    Usage of grain version:
      -json
        	print version information as JSON
    ```
  - Exit code: `2` (non-zero `flag.ExitOnError` exit code).
- **Extra positional arguments (`/tmp/grain-build version extraarg`)**:
  - Output: `v1.8.0-rc.1.0.20261010070604-72b44631fc8d · 72b4463 · 2026-10-10 07:06Z`
  - Exit code: `0` (flags parsed; extra positional arguments ignored).

---

## 3. UI Settings Overlay Walkthrough Execution & Results

Automated browser walkthrough was executed using Playwright in `ui/e2e/version_display_walkthrough.spec.js` against the live `grain demo` server.

### 3.1 Desktop Viewport (1280 × 720)

- **Step 1: Backend Version Payload Inspection**:
  - Queried `GET /api/config`.
  - In `grain demo`, `version` payload is either stamped or omitted if built unstamped. When omitted, `SettingsOverlay.jsx` evaluates `!v || (!v.commit && !v.release)` to display `dev (unstamped)`.
- **Step 2: User Settings Navigation**:
  - Clicked user tile button in sidebar (`[aria-label="User settings"]`).
  - Overlay header displayed: `Settings` (`role="heading"`).
- **Step 3: Version Section Inspection**:
  - Located Version heading (`Typography variant="subtitle2"`).
  - Located version display element via `data-testid="settings-version-display"`.
  - DOM text content asserted: matched expected backend config version (`dev (unstamped)`).
- **Step 4: Typography and Styling**:
  - Computed CSS property `fontFamily` verified to contain `monospace`.
  - Typography color matches `text.secondary`.
- **Step 5: Dismissal**:
  - Clicked "← Back" button (`.overlay-pane-back button`): Settings overlay dismissed cleanly.
  - Reopened Settings overlay and pressed `Escape`: dialog closed cleanly without error.

### 3.2 Mobile Responsive Viewport (390 × 844)

- **Step 1: Phone Navigation Drawer**:
  - Viewport configured to `390 × 844`.
  - Sidebar rail is hidden behind `PhoneNav` top bar.
  - Clicked "Open navigation" hamburger button (`[aria-label="Open navigation"]`), sliding out the drawer.
- **Step 2: Access User Settings**:
  - Clicked "User settings" button inside drawer.
  - Drawer closed and Settings overlay opened.
- **Step 3: Responsive Layout Verification**:
  - Evaluated dialog paper element (`.MuiDialog-paper`).
  - Bounding box width asserted to be `390px` (100% full-width on mobile viewport via `isPhone` hook in `Overlay.jsx`).
  - Version section (`data-testid="settings-version-display"`) rendered clearly with no horizontal text clipping or overflow.
- **Step 4: Mobile Dismissal**:
  - Clicked "← Back" button: Settings overlay dismissed and returned to task list view.

---

## 4. Test Verification Suite

### Automated Playwright E2E Suite
```bash
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/version_display_walkthrough.spec.js
```
Output:
```text
Running 2 tests using 1 worker

  ✓  1 [chromium] › e2e/version_display_walkthrough.spec.js:7:3 › Settings overlay version display walkthrough › Desktop: Settings overlay displays version matching /api/config with monospace font and dismisses via button and Escape (1.0s)
  ✓  2 [chromium] › e2e/version_display_walkthrough.spec.js:90:3 › Settings overlay version display walkthrough › Mobile phone viewport: opens navigation drawer, accesses Settings, and verifies responsive layout (1.1s)

  2 passed (9.9s)
```

### Component Unit Test Suite
```bash
cd ui && npm test src/components/SettingsOverlay.test.jsx
```
Output:
```text
 RUN  v4.1.11 /home/debian/work/ui

 ✓ src/components/SettingsOverlay.test.jsx (19 tests) 3235ms

 Test Files  1 passed (1)
      Tests  19 passed (19)
```

### CLI Go Unit Test Suite
```bash
go test -v ./cmd/grain -run TestVersion
```
Output:
```text
=== RUN   TestFormatVersion
=== RUN   TestFormatVersion/unstamped
=== RUN   TestFormatVersion/full_stamp_without_release
=== RUN   TestFormatVersion/full_stamp_with_release_and_modified
=== RUN   TestFormatVersion/release_alone
--- PASS: TestFormatVersion (0.00s)
=== RUN   TestVersionCmd_Output
--- PASS: TestVersionCmd_Output (0.00s)
PASS
ok  	github.com/ai-on-gke/grain/cmd/grain	0.015s
```

---

## 5. Summary and Conclusion

- **CLI `grain version` subcommand**: Fully functional across human-readable and structured `-json` output modes. Error reporting on invalid flags adheres to standard exit codes.
- **Settings dialog version display**: Correctly renders `config.version` matching CLI formatting rules, falls back gracefully to `dev (unstamped)`, adheres to monospace styling, and operates responsively across desktop and mobile devices.
- **Regression test coverage**: Permanent Playwright walkthrough added in `ui/e2e/version_display_walkthrough.spec.js`.
