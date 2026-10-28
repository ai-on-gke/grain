# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-01
- **Task**: grain/task-1357
- **Status**: Passed after fixes. Two bugs fixed in this change, one small
  layout improvement, two follow-ups filed.

## Environment

- **Commit walked through**: `aeba486fa7c2` (`candidate` at the start of the
  run, build stamp `v1.5.0-rc.1.0.20261001092713-aeba486fa7c2`). The fixes
  were then verified in the browser on this branch's commits `6fd43ce38` and
  `ef69eb7ef`.
- **Host**: Debian (grain sandbox), Linux amd64. Go 1.26.6, Node 20.19.0.
- **Browser**: Playwright 1.62.1 driving Chromium 151.0.7922.34
  (headless shell), 1400×900/1000 desktop viewport plus a 390×844 phone
  viewport for the long-name layout check. Scripts used the Playwright
  library directly (not jsdom) so each step could be inspected as it ran.
- **Server**: the real `grain demo` server, built with `go build ./cmd/grain`
  after `npm run build` had written the production frontend to
  `pkg/ui/static`, started as
  `TMPDIR=<dir> grain demo -addr 127.0.0.1:8420 -open=false`. Each start had a
  fresh embedded SQLite store under that `TMPDIR`. The server was restarted
  twice, after each fix, on a fresh store.
- **Account**: created through the first-run "create account" dialog
  (`operator`). Every later page load reused that browser session.
- **Model tooling**: there was no `/home/debian/.gemini-api-key` in this
  sandbox, so no model-backed tooling was used.
- **Setup note**: `npx playwright install chromium` failed with
  `SELF_SIGNED_CERT_IN_CHAIN` behind the sandbox's package-cache proxy.
  It succeeded with `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | Demo answers "GitHub branch listing is not available on this UI server" | `page.route` in the browser, for this path only | success (`main`, `candidate`, `walkthrough/topic-1`, `release/2.1`), empty `[]`, 4 s delay, 502 |
| GitHub comparison, `GET /api/repos/*/*/compare?...` | Demo answers "repository comparison is not available on this UI server" | `page.route`, for this path only | success (a one-file unified diff), empty diff, 4 s delay, 502 |
| GitHub releases reconciler (release provisioned, candidate cut, cut error, promoted, merged) | No reconciler runs in the demo; releases stay `provisioning` and candidates stay `cutting` | A throwaway Go helper, not committed, that called the same `model.Store` methods the reconciler calls (`MarkReleaseProvisioned`, `MarkCandidateCut`, `MarkCandidateError`, `MarkCandidatePromoted`, `MarkReleaseMerged`, `orchestrator.SyncQualifications`) against the demo's own store, keyed by release name. The committed `ui/e2e/releasehelper` does the same for the e2e spec. | provisioned, cut/active, cut error with message, promoted, merged with PR URL, qualification run scheduled |
| Grain CI execution for a manual release, `POST /api/repos/acme/widgets/release-runs` | Demo daemon runs with `-ci=false` and answers 404 "this deployment has no grain CI wired" (observed unstubbed first) | `page.route` for the POST only | 2 s-delayed 201 `{id:"rr-0001",...}`, 502 |
| Network latency/offline | To see loading and offline states without stubbing | Chrome DevTools Protocol `Network.emulateNetworkConditions` (2.5 s / 1.5 s latency, offline) | n/a |

Grain's own API, the React UI and every user gesture were real everywhere
else. A few one-off API calls were used only to set up a reproduction or
to read back what had been stored; each is named where it was used.

## Steps and outcomes

### 1. Repos list (`/repos`)

1. On `/`, focused the rail's **Repos** entry (`div[role=button]`,
   `tabindex=0`) and pressed **Enter**. The client router went to `/repos`.
   A direct cold load of `/repos` rendered the same page with no console
   errors.
2. The header count was `1` with one row, `acme/widgets`, showing per-state
   chips and `12 tasks`. The allowlist note named it.
3. **Add repo** form: added `acme/gadgets`, then
   `example-corporation/exceptionally-long-repository-name-for-responsive-layout-checks`.
   No confirmation appeared, which is correct because the allowlist was
   already non-empty. Adding `not a repo` was refused inline
   (`repo must be owner/name, got "not a repo"`, HTTP 400).
4. **Search**: `GADG` filtered to `acme/gadgets` and the count followed it to
   `1`. `zzz-none` showed "No repos match your search."
5. **Sort**: *Name* gave alphabetical order with no drag handles. *Most tasks*
   put `acme/widgets` first with no handles. *Custom order* brought back
   three handles.
6. **Keyboard/tap reorder**: focused `Move <long repo>` and pressed Enter.
   The banner read "Moving 1 repo — choose where it goes" with slots
   "Move here, above acme/gadgets" and "…above acme/widgets". Focusing the
   first slot and pressing Enter moved the long repo to the top.
   **Escape** after picking up `acme/gadgets` closed every slot.
7. **Pointer drag**: dragged the `acme/gadgets` handle above the first row.
   The order became gadgets, long, widgets. It survived a reload, stored in
   `localStorage["grain.list-order.repos"]`.
8. **Long name at 390 px**: no horizontal overflow
   (`scrollWidth == clientWidth == 390`). The name is cut with an ellipsis
   (609 px of text in a 144 px box). **Finding**: the cut name had no
   `title`, so the rest of it could not be read without opening the repo.
   Fixed (see Fixes).

### 2. Repo page (`/repos/acme/widgets`)

1. Clicking the row name opened `/repos/acme/widgets`. **← repos** returned
   to `/repos`, and browser Back reopened the repo.
2. **Task count link** "12 tasks filed here" led to
   `/?f=repo:any:acme/widgets`, showing 12 tasks and the filter chip
   "repo is any of acme/widgets". Back returned to the repo page and
   Forward returned to the filtered list.
3. **File a task from the repo page**: **new task** opened the `/new`
   drawer with `select[name=repo]` preselected to `acme/widgets`. I filled in
   the title and description.
   - **Base branch** (`BranchField`) suggested `walkthrough/topic-1`,
     `main` and `candidate` from the stubbed GitHub listing, and I picked
     `candidate`.
   - **Writes**: in "Another repo this task may change" I typed `acme/gad`;
     the menu offered `acme/gadgets` and `add acme/gad`, and Enter picked
     `acme/gadgets`.
   - **Reads**: picked the long repo from "Search repos, or type owner/name…".
   - The drawer did not overflow (`560 == 560`).
   - **Create task** returned to the repo page.
   - `GET /api/tasks` showed task 13 with `repo acme/widgets`,
     `base candidate`, `writes [{repo: acme/gadgets}]`,
     `reads [<long repo>]` and state `queued`. The repo page then read
     "13 tasks filed here".
4. **Branch rules**:
   - **add branch** with `walkthrough/topic-1` listed it as
     "walkthrough/topic-1 -- pending", and it persisted across reload.
     `bad..branch` was refused with "invalid branch name".
   - **Compare** with base `main` and head `walkthrough/topic-1` went to
     `/repos/acme/widgets/compare/main...walkthrough/topic-1`. With the
     stubbed comparison:
     - success rendered the side-by-side diff of `README.md` (+1 −0);
     - empty said "There are no changes between these branches.";
     - the 4 s delay showed "Loading the comparison…" and then the diff;
     - 502 showed the error. **Cosmetic**: the error text appears twice,
       once inline and once in the global banner.
   - The compare URL also worked as a cold deep link, and Back returned to
     the repo page.
   - The compare base/head boxes are plain text fields by design;
     GitHub branch suggestions appear only in `BranchField`, on the
     new-task form.
5. **Capabilities**:
   - CI capabilities offered gcp key, gemini key, github sandbox,
     registry push, package publish and the three proxies. I picked
     **gcp key**.
   - Default capabilities marked deployment defaults as "already a
     deployment default -- on here either way". I picked **self debug**.
   - Both saved as soon as they were picked, and the summary line read
     "A task filed against acme/widgets starts with: Self debug, GCP key,
     Gemini key".
6. **Prompt extension / setup command**: set
   "Walkthrough prompt extension — 2026-10-01." and
   `make deps && echo walkthrough-setup`. Both autosaved on blur.
7. **Checks no commit here can clear**: added `cla/assistant`.
8. **CI workflows** ("add workflow", autosaves on blur):
   - `name: [broken` was refused with
     `yaml: line 1: did not find expected ',' or ']'`;
   - `runs-on: grain` was refused with
     `runs-on must be "ubuntu-latest" in the first GitHub Actions compatibility release`;
   - a valid `ubuntu-latest` workflow saved as `walkthrough.yml` and
     survived reload;
   - its **remove** deleted it at once, with no confirmation. That matches
     the section's autosave design, but the button's accessible name is a
     bare "remove", the same as the repo's own remove button.
9. **Declarations**: the "Read acme/widgets's own declarations" checkbox
   flips once its PUT answers. Turned off and reloaded, it was still off;
   turned back on and reloaded, it was still on. "Read at abc123commit:
   1 template(s), 1 suite(s), 1 schedule(s)" is shown in both states.
10. **Reload check**: after a hard reload, CI caps (gcp key), default caps
    (self debug), prompt extension, setup command, `cla/assistant` and the
    branch all read back as saved.
11. **Repo removal** confirms: "Remove acme/widgets from target repos? …".
    I dismissed it.
12. **Repo-scoped (team) configuration**:
    - Picking team `admin` in the rail showed "Team context: default /
      inheriting global defaults".
    - **Finding**: editing the prompt extension there wrote the **global**
      value, and `GET /api/teams/operator/repos/acme/widgets/config` then
      reported it as operator's effective value too.
    - Reproduction: I set an override directly with
      `PUT /api/teams/operator/repos/acme/widgets/config`, because no UI
      creates one. With team `operator` selected the banner said
      "team-specific configuration overrides active", but the fields still
      showed the global prompt and setup command.
    - **Revert to global defaults** confirmed with "Revert team overrides
      for acme/widgets back to global repository defaults?" and cleared the
      override.
    - Filed as a follow-up (see below). The global prompt was restored
      through the UI.

### 3. Memory (`/team-settings` → **Models**)

1. The scopes shown were *this deployment* ("written 2 days ago", "written
   by the memory agent", 271/8192 characters) and *acme/widgets* ("written
   9 days ago", "edited here", 130 characters).
   - Documents: `release-checklist` (deployment, 332 b) and `flaky-e2e`
     (acme/widgets, 208 b), each with an age, an author and a summary.
   - An "another repo" picker adds a new scope.
2. **revert** restored an unsaved edit. Saving an **empty** memory asked
   "Forget everything grain has learned about acme/widgets? …"; I dismissed
   it.
3. **Reversible correction**: appended "Corrected during the 2026-10-01
   operator walkthrough." to acme/widgets memory and saved. The chips
   changed to "written today / edited here". After reloading the page and
   reopening the tab, the corrected text was there.
4. **Documents**: **open** loaded both full documents in place, with a
   summary field and the text (332/65536 characters).
   - I edited `flaky-e2e`'s text and clicked **save document**. The chips
     read "written today / edited here, 278 b".
   - After reloading and opening it again, the corrected text was read back.
   - **delete** asked `Delete the memory document "flaky-e2e"? Nothing else
     keeps a copy of it.`; I dismissed it.
5. **Reverted** both the document and the repo memory to their original
   text, and confirmed it after reload and against `GET /api/memory`.
6. In one scripted pass the repo-memory revert issued right after a
   document save did not stick. I re-ran that exact sequence (open a
   document, save it, then edit and save the scope's memory) with PUT
   capture, and it persisted correctly. It did not reproduce, so I recorded
   it as script timing, not a bug.
7. The open/close buttons are reachable by keyboard (`tabIndex 0`).

### 4. Releases (`/repos/acme/widgets/releases`)

1. Opened from the repo page's **releases** button, and also as a cold deep
   link. Before any release existed, the pane showed "acme/widgets has no
   releases yet -- create one above…" and the qualification plan editor.
2. **Validation**:
   - An empty name was blocked by the browser ("Please fill out this
     field.").
   - `two words`, `../x` and `rel;rm` were all accepted by the browser and
     refused by the server with "invalid release name" (400).
   - A duplicate `walk-a` was refused with "this repo already has an
     unmerged release with this name".
   - **Cosmetic**: the "invalid release name" banner was still on screen
     after a later successful create.
3. **Create**: `walk-a` and `walk-b` were each created as Provisioning, with
   `<name>.rc.1 -- cutting` and Cut/Promote disabled. The newest is
   selected.
4. **Bug (fixed)**: I made `walk-a` active with an active rc.1, and gave
   `walk-b`'s rc.1 a cut error ("Simulated: GitHub refused to create
   walk-b.rc.1 (422)"). Then I picked `walk-a` in the release picker.
   - 150 ms later, and still 1.2 s later, the pane showed walk-a's branch
     names and merge button but **walk-b's** current candidate, its error,
     its history and the Cut/Promote state that follows from it.
   - The pane only caught up on the next 3 s poll.
   - It reproduced on every attempt.
5. **Qualification plan**: **add item**, chose template
   `nightly-maintenance` (offered: nightly-maintenance, security-audit,
   release-qualification), then **save qualification plan**. It survived
   reload.
6. **Lifecycle** (driving `walk-a` with the helper standing in for the
   reconciler):
   - **Promote current RC** moved rc.1 to `promoting` (no confirmation),
     then to `promoted` after the helper.
   - **Cut new RC** then became enabled. Clicking it gave
     `walk-a.rc.2 -- cutting`, and the history listed rc.2 and rc.1.
   - After the helper cut it and synced qualification, rc.2 was `active`,
     a qualification block read "running", and a task link
     `nightly-maintenance → /tasks/14` appeared.
   - Focusing that link and pressing Enter opened task 14, and Back
     returned to the releases pane with `walk-a` still selected.
   - **Merge walk-a into the default branch** went straight to
     merge-requested (no confirmation). After the helper marked it merged:
     "Merge requested -- pull request", linking to
     `https://github.com/acme/widgets/pull/4242` with `target=_blank`
     and `rel=noreferrer`. The picker read "walk-a -- Merged".
7. **Loading state**:
   - **Finding (fixed)**: under 2.5 s of real latency the whole pane was
     blank, with no back button and no heading, until both first reads
     landed. A failed first read would have left it blank for good.
   - **Offline**: the poll failed with a "Failed to fetch" banner, which
     cleared when the network came back.
8. **Deep links**: `/repos/acme/widgets/releases/walk-a` redirects to the
   pane, which does not select `walk-a`. That is expected because the router
   has no per-release path. `/repos/nobody/missing/releases` renders a
   create form for a repo outside the allowlist.
   - **Finding (follow-up)**: creating `ghost` there returned **201**.

### 5. Release trains and release workflows

The demo has no release-train page: `/repos/acme/widgets/trains` falls back
to the repo page and `/releasetrains` falls back to the task list, with no
errors. In this code base the train is the base-branch release, managed in
the repo page's **release workflows** section, and `GET
/api/repos/acme/widgets/release` reports `available: false` in the demo.

1. **turn off automatic releases** sent `PUT …/release` with
   `enabled:false`. The button became **turn on automatic releases**, and
   that survived reload. Turned back on: `enabled:true`.
2. **jobs dispatched after every merge**: `build-cache, docs` saved and
   survived reload, then was cleared.
3. **Run release by hand** reveals a "Release branch" form.
   - An empty branch was blocked by the browser.
   - Unstubbed, `main` returned **404 "this deployment has no grain CI
     wired"**, shown as an error banner.
   - With the execution boundary stubbed:
     - the 502 kept the form open with the error;
     - the delayed 201 handed off to the CI run view (whose real
       `GET /api/ci/runs/rr-0001` 404s, as expected for a stubbed id).
   - **Finding (fixed)**: during the 2 s in-flight window the **Run
     release** button stayed enabled, so a second click would dispatch a
     second trusted release.
4. The custom secrets section correctly says the UI is not connected to
   the encrypted secrets store.

### Console and network

On every page I recorded console errors and warnings, page errors, failed
requests and HTTP responses of 400 or above. No page errors occurred. The
only console errors were the expected 4xx/5xx from deliberate invalid input
and stubbed failures, plus `GET /api/tasks net::ERR_ABORTED`, which is the
app poll cut off by a navigation or reload.

## Fixes in this change

1. **Releases pane shows one release's candidate under another**
   (`ui/src/components/RepoReleases.jsx`).
   - The candidates and qualification run are now stored with the release
     they were fetched for.
   - Until the selected release's own data arrives the pane says "Loading
     candidates…" and both Cut and Promote are disabled.
   - Changing the picker re-reads at once, and a poll response for a
     release that is no longer selected is dropped.
   - Verified in the browser: with 1.5 s of latency, 150 ms after switching
     the pane read "Loading candidates…" with `cut=false promote=false`,
     then settled on `walk-a.rc.1 -- active`.
   - Regression coverage:
     - `RepoReleases.test.jsx` "never shows the previous release's
       candidate after another release is picked" fails on the old
       component.
     - Playwright test "switching releases never shows the previous
       release's candidate" in
       `ui/e2e/repository_release_memory_walkthrough.spec.js` fails on the
       old component (`Expected: 0, Received: 1`) and passes with the fix.
2. **Blank releases pane while loading or after a failed first read**
   (same file). The back button and heading now stay up with "Loading
   releases…", or with "Could not load acme/widgets's releases: … Retrying."
   until the poll's next successful read. Verified under 2.5 s of latency.
   Covered by a new `RepoReleases.test.jsx` case.
3. **Manual release can be dispatched twice**
   (`ui/src/components/RepoReleaseWorkflowSettings.jsx`). **Run release** is
   disabled while its POST is in flight, and the submit handler refuses a
   second submit. Verified in the browser (`submit disabled? true`). Covered
   by a `RepoReleaseWorkflowSettings.test.jsx` case that fails without the
   fix.
4. **Truncated repo names have no tooltip**
   (`ui/src/components/RepoList.jsx`). `.repo-list-name` now carries
   `title={repo}`. Covered in `RepoList.test.jsx`.

Tests run: `vitest` for `RepoReleases`, `RepoReleaseWorkflowSettings`,
`RepoList` and `App.repos` all passed. The Playwright spec
`repository_release_memory_walkthrough.spec.js` (Chromium, both tests)
passed. Grain CI ran the full suite on the first fix commit (frontend, go,
ui e2e, lint, installer, package cache, terraform) and passed.

## Follow-ups filed (branch `candidate`)

- **Repo page in team context shows and edits global settings while team
  overrides are active.** Includes the reproduction from §2.12.
- **Release creation accepted for repos outside the target-repo
  allowlist, and Promote / Merge-back act without confirmation.** Includes
  the reproduction from §4.8 and the observations from §4.6.

## Minor observations (not filed)

- The compare page shows a GitHub error twice (inline and in the banner).
- The "invalid release name" banner stays up after a later successful
  create.
- The release-name field has no client-side pattern hint, so names with
  spaces are only refused by the server.
- The repo workflow's **remove** button has the bare accessible name
  "remove", the same as the repo's own remove button.
- The declarations status line ("Read at …") still shows while reading is
  turned off.
- `/repos/acme/widgets/releases/<name>` does not select that release.
