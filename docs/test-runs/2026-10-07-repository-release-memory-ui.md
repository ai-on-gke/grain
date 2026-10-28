# Repository, Release, and Memory UI Operator Walkthrough

- **Date**: 2026-10-07
- **Task**: grain/task-2414
- **Status**: Passed, with five defects found and fixed in this change:
  1. The repo page's CI workflow editor dropped the row **Add workflow**
     had just added. It also lost a **Remove** while another row was
     half written: the row vanished from the form, but the workflow
     stayed stored.
  2. Team settings closed (Escape, Back, another tab), and a memory
     document's **Close** acted, without asking, throwing away an unsaved
     memory correction.
  3. Removing a qualification plan item left its template in the other
     items' **Depends on**. The picker could not untick it, and the
     server refused every save after that.
  4. Closing the CI run pane after a manual release left focus on the
     page body. This was an observation on 10-06.
  5. The release job lists stored a repeated name twice
     (`build-cache, docs, build-cache`).

  No follow-up task was filed. Observations are listed at the end.

## Environment

- **Commit walked through**: `61604b729`, the tip of `candidate` when the
  run started. The footer stamp read
  `v1.8.0-rc.1.0.20261007002923-61604b7295e5 · 61604b7 · 2026-10-07 00:29Z`.
- **Fixes**: checked on this branch against a second demo, rebuilt from
  the fixed tree, over a fresh store.
- **Host**: Debian (grain sandbox), Linux amd64. Go 1.26.6, Node 20.19.0.
- **Browser**: Chromium Headless Shell 151.0.7922.34 (Playwright 1.62.1,
  `chromium-headless-shell` 1234).
  - It was driven by the Playwright library from ad-hoc scripts kept
    outside the repository (not jsdom).
  - The viewport was 1400×950, with a full deep-link pass at 390×844.
  - Every script logged:
    - console errors and warnings;
    - page errors;
    - failed requests (other than `net::ERR_ABORTED` poll cancels);
    - every `/api/` response of 400 or above, with its body;
    - every mutating `/api/` request, with its body;
    - every `confirm()` dialog, which the script accepted or dismissed as
      the step required.
- **Server**: the real `grain demo`.
  - Built by running `npm ci && npm run build` in `ui/` (into
    `pkg/ui/static`), then `go build ./cmd/grain`.
  - Started as `TMPDIR=/tmp/run1 grain demo -addr 127.0.0.1:8420 -open=false`
    over a fresh embedded SQLite store.
  - The fixes were verified on `127.0.0.1:8421`, under `TMPDIR=/tmp/run2`,
    built from this branch.
- **Account**: `operator`, created through the first-run **New user** tab
  of the Create account dialog on each server.
- **Model tooling**: `/home/debian/.gemini-api-key` is not present in this
  sandbox, so no Gemini-backed tooling was used.
- **Setup**: Chromium was installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium`.
  No screenshots or traces are committed.

### Every stub and simulated boundary

| Boundary | Why | Mechanism | Responses exercised |
|---|---|---|---|
| GitHub branch listing, `GET /api/repos/*/*/github-branches` | The demo has no GitHub. Its real answer is 404 "GitHub branch listing is not available on this UI server", seen unstubbed on a qualification task's page. | `page.route` on this path only, in the New task and task-detail steps | `["main","candidate","release/4.0"]` |
| Release-run dispatch, `POST /api/repos/acme/widgets/release-runs` | The demo daemon runs `-ci=false`. Its real answer, 404 "this deployment has no grain CI wired", was exercised first, with no stub. | `page.route` on this path only | 502 `{"error":"GitHub: could not resolve refs/heads/main (502 Bad Gateway)"}`; a 2.5 s delay, then 202 `{id:"run-20261007-1",status:"queued"}` |
| GitHub release reconciler | No reconciler runs in the demo | The committed `ui/e2e/releasehelper`, run as `-dir /tmp/run1/grain-demo-* -release <name> <cmd>`. It calls the `model.Store` transitions on the demo's own store. | `mark-release-provisioned`, `mark-candidate-cut` (×2), `mark-candidate-promoted`, `sync-qualifications`, `mark-release-merged`, `mark-release-error` |

The repository comparison (`GET …/compare`) was exercised only
unstubbed. It returned the demo's real 404.

Everything else was real: Grain's own API, the React UI and every user
gesture. Read-backs used plain `fetch` from the page against the same
server.

## Steps and outcomes

### 1. Repos list

1. **Opening it**:
   - Focusing the rail's **repos** entry (a `role=button`) and pressing
     **Enter** went to `/repos`.
   - Cold loads of `/repos` at both widths drew the same rows.
2. **Add repo**: the `owner/name` box, with Enter to submit.

   | Typed | Result |
   |---|---|
   | `acme/gadgets`, `zeta/alpha-tools`, the 80-char `example-corporation/…-1007` | added |
   | `Acme/Gadgets`, `acme/gadgets/` | 200, but no second row |
   | `git@github.com:acme/sshform.git` | normalised to `acme/sshform` |
   | `acme/`, `acme/wid gets`, `acme/.git` | 400 `repo must be owner/name, got …`; the box keeps the text |
   | `-acme/x` | added (see Observations) |

   The badge ended at `6`.
3. **Long names**: the document `scrollWidth` equals the viewport at
   1400 px and at 390 px.
4. **Search**: `ALPHA` showed only `zeta/alpha-tools`, with a badge of
   `1`. `zzz` showed "No repos match your search."
5. **Sort**:
   - The options are custom order, name (a–z), active first and most
     tasks.
   - Active first and most tasks put `acme/widgets` first.
   - Only Custom order shows `Move …` handles (6). The others show 0.
6. **Keyboard reorder**:
   - Enter on `Move zeta/alpha-tools` announced "Moving 1 repo — choose
     where it goes", and focus moved to "Stop moving zeta/alpha-tools".
   - There were five "Move here, above …" slots. Enter on the first moved
     zeta to the top, and focus returned to its handle.
   - Escape after picking up `acme/gadgets` cancelled it, with focus kept
     on its handle.
7. **Pointer drag**: dragging the long repo's handle onto the first row
   put it first.
8. **Click/tap pickup**: clicking `Move acme/sshform` and then "Move here,
   to the end of the list" moved it to the end.
9. **Reload**: the order survived. It is stored client-side, so no request
   is made.

### 2. Repo page and navigation

1. **Opening a row**: clicking `acme/widgets` opened
   `/repos/acme/widgets`. Back returned to `/repos` and Forward returned
   to the repo page.
2. **The task-count link**: **12 tasks filed here**
   (`/?f=repo:any:acme/widgets`) opened the task list filtered "repo is
   any of acme/widgets". Back returned to the repo page.
3. **A repo with no tasks**: `/repos/acme/gadgets` read "0 tasks filed
   here".

### 3. Filing a task from the repo page

1. **The form**: **New task** on `/repos/acme/gadgets` opened `/new` with
   `select[name=repo]` = `acme/gadgets`.
2. **Base branch** (stubbed listing): typing `rel`, ArrowDown and Enter
   gave `release/4.0`.
3. **Persistence**: with Queue immediately unticked, Create returned to
   `/repos/acme/gadgets`. It now read "1 task filed here".
   - Task 13 was `proposed`, with repo `acme/gadgets` and base
     `release/4.0`.
   - `/tasks/13` showed both values, and so did a reload.

### 4. Read and write repository relationships (task 13's Repos section)

1. **Base branch**: typing `cand` and picking `candidate` sent
   `PATCH /api/tasks/13 {"base":"candidate"}`.
2. **Read-only repos**: picking `acme/widgets` sent
   `{"reads":["acme/widgets"]}`. The chip read "acme/widgets (read)".
3. **Another repo this task may change**: picking `zeta/alpha-tools` sent
   `{"writes":[{"repo":"zeta/alpha-tools","base":""}]}`.
4. **Write-repo base**: "Base branch in zeta/alpha-tools" = `release/4.0`
   persisted, and survived a reload.
5. **Pickers**:
   - Neither picker offers the task's own repo (typing `gadg` matched
     nothing).
   - "Remove zeta/alpha-tools", pressed from the keyboard, sent
     `{"writes":[]}`.

### 5. Repo configuration (global), then reload

All on `/repos/acme/widgets`, through the page's own controls:

| Control | Request | After reload |
|---|---|---|
| Add branch `walkthrough/1007` | `POST …/branches` | listed, "pending" |
| `bad..branch`, `-lead` | 400 "invalid branch name"; the box keeps the text | — |
| `walkthrough/1007` again | 200, "already on this repo's list (pending); nothing new was added." | — |
| Compare `main` → `walkthrough/1007` (Enter in Head branch) | URL `…/compare/main...walkthrough/1007`; real 404; Back returns | — |
| CI capabilities → Gemini key (Enter on the combobox) | `PUT …/ci-capabilities` | kept |
| Default capabilities → Memory | `PUT …/capabilities` | kept |
| Prompt extension | `PUT …/prompt-extension` | kept |
| Setup command | `PUT …/setup-command` | kept |
| Read declarations (off → on → off) | `PUT …/declarations` | kept; "Read at abc123commit: 1 template(s), 1 suite(s), 1 schedule(s), 0 trigger(s)" (the demo's seeded commit) |
| Unfixable `cla/agreement` | `PUT …/unfixable-checks` | kept |
| Unfixable `CLA/AGREEMENT`, `grain CI` | refused in place, with no request | — |
| Jobs dispatched after every merge `build-cache, , docs ,build-cache` | `PUT …/release` | **`build-cache, docs, build-cache`** (defect 5) |

**CI workflows** (`PUT …/workflows`):

| Step | Result |
|---|---|
| `lint.yml` with `this: is: not: a workflow` | 400 naming the file and the YAML error; nothing stored |
| `runs-on: grain` | 400 "runs-on must be \"ubuntu-latest\" in the first GitHub Actions compatibility release" |
| A valid workflow | stored; the path box became `.grain/workflows/lint.yml` |
| Edit the text, then click **Add workflow** | **the new row vanished** (defect 1) |
| Add `docs.yml`, then reload | both kept (server order: docs, lint) |
| Add a row, type only `half.yml`, then **Remove** lint | **the row vanished, nothing was sent, and lint came back on reload** (defect 1) |

### 6. Team-scoped repo configuration

On `/teams/default/repos/acme/widgets`:

1. **The banner**: "Team context: default — viewing repository settings
   inheriting global defaults for this team".
2. **Setup command**: it showed the inherited global text. Editing it sent
   `PUT /api/teams/default/repos/acme/widgets/config
   {"setupCommand":"echo team-default-1007"}`, which survived a reload.
   The global page still showed the global command.
3. **Default capabilities for default**: Release notes. The page then said
   "…starts with: Self debug, GCP key, Gemini key, Memory, Release
   notes".
4. **New task from this page**: it ticked all five capabilities. Task 14
   was filed with `gcp-key, gemini-key, memory, release-notes,
   self-debug`, so the 10-06 fix holds.
5. **Revert to global defaults**: this asks "Revert team overrides for
   acme/widgets back to global repository defaults?".
   - Dismissed: nothing changed.
   - Accepted: `DELETE …/config`. After a reload the inherited global
     setup command was back.

### 7. Memory (Team settings → Models)

1. **The panel**:
   - The boxes are "memory carried by every run" and "memory about
     acme/widgets".
   - The documents are `release-checklist` (deployment) and `flaky-e2e`
     (acme/widgets).
2. **Repo memory**: appending "- 2026-10-07 walkthrough: corrected note."
   and pressing **Save acme/widgets memory** sent `PUT /api/memory`. Save
   went back to disabled.
3. **Opening a document**: **Open flaky-e2e (acme/widgets)**, then
   appending a line and pressing **Save document** sent
   `PUT /api/memory/documents`.
4. **Reading it back**:
   - Close and reopen showed the correction.
   - So did a reload.
   - `GET /api/memory/documents` reported 274 b, `updatedBy: operator`,
     `human`.
5. **Unsaved edit, then Escape**: the whole pane closed and the edit was
   lost, with no question (defect 2).
6. **Delete release-checklist (this deployment)**: this asks "Delete the
   memory document "release-checklist"? Nothing else keeps a copy of it."
   Dismissed, the document stayed.
7. **Restoring**: both the memory and the document were put back to their
   original text, and this was confirmed through the API.

### 8. Releases

1. **Opening the pane**: **Releases** on the repo page opened
   `/repos/acme/widgets/releases`, which read "acme/widgets has no
   releases yet".
2. **Create release**:

   | Name | Result |
   |---|---|
   | `v4.0-1007`, `hotfix-1007` | created; URL moves to `…/releases/<name>` |
   | `v4.0-1007` again | 400 "this repo already has an unmerged release with this name" |
   | `rel/1007`, `x.lock` | 400 "invalid release name" |

3. **Qualification plan**:
   - **Two items in a dependency cycle**: 400 "qualification plan has a
     dependency cycle".
   - **Remove the item the other depends on**: the "security-audit" chip
     stayed on the remaining item. Save got 400 "qualification item
     template-1 depends on a template not in this plan: template-2"
     (defect 3).
   - **Repeat `-3`**: the browser's own `min=1` check held the submit.
   - **One release-qualification item with Require approval**: saved, and
     survived a reload.
4. **Promote and cut** (helper transitions):
   - **Promote**: it confirms "Promote v4.0-1007.rc.1 to v4.0-1007? …".
     Dismissed, nothing changed. Accepted, rc.1 went to "promoting", then
     "promoted".
   - **Cut new RC**: rc.2 went to "cutting", then "active". The history
     lists both.
5. **Qualification task**:
   - After `sync-qualifications` the pane showed "pending approval" and
     "release-qualification · unapproved".
   - The link `/tasks/15` opened the task, and Back returned to
     `…/releases/v4.0-1007`.
   - **Approve all** moved it to running.
6. **Merge back**: this confirms "Merge v4.0-1007 into acme/widgets's
   default branch? This opens a pull request on GitHub from v4.0-1007."
   - Dismissed: no request.
   - Accepted: "Merge requested", and the button was disabled.
   - After `mark-release-merged`: "Merged", with "Merge requested -- pull
     request". Cut and Promote are disabled.
7. **Error state**: after `mark-release-error`, hotfix-1007 reads
   "Provisioning … (Simulated GitHub failure creating the release
   branches)". All three actions are disabled.
8. **Picker**: Enter opens it, listing `hotfix-1007 -- provisioning` and
   `v4.0-1007 -- merged`. ArrowDown and Enter switch release and URL.
9. **Deep links** (each at 1400 and at 390 px; `scrollWidth` equals the
   viewport throughout):

   | URL | Result |
   |---|---|
   | `/repos/acme/widgets/releases/nope` | rewritten to `…/releases`; alert `no release "nope"` |
   | `/repos/zeta/alpha-tools/releases` | "no releases yet" and "No templates this repo can qualify yet" |
   | `/repos/nobody/missing/releases` | "not on this deployment's target repo list", with no create |
   | `/teams/default/repos/acme/widgets/releases/v4.0-1007` | opens v4.0-1007 |
   | `/repos/acme/widgets/releases/v4.0-1007/extra` | normalised to `…/releases/v4.0-1007` |
   | `/repos/acme/widgets/compare/main...walkthrough/1007` | the compare page with the demo's real 404 |

### 9. Release trains (Release workflows on the repo page)

1. **Automatic releases**: **Turn off automatic releases** survived a
   reload as "Turn on automatic releases". It was then turned back on.
2. **Run release by hand**: this reveals "Release branch", and focus stays
   on "Cancel manual release".
   - **Unstubbed**: refused with the real "this deployment has no grain
     CI wired".
   - **Stubbed 502**: the error is shown, and the form stays open.
   - **Stubbed slow 202**: **Run release** is disabled while the request
     is in flight. The CI run pane then opens, gets the demo's real 404
     for `GET /api/ci/runs/run-20261007-1`, and says so.
   - **Escape** closes the pane, but focus lands on `<body>` (defect 4).

### Console and network

There were no page errors anywhere. Every console error was one of these:

- the resource errors for deliberate 400s;
- the demo's real 404s for `github-branches`, `compare`, `release-runs`
  and `GET /api/ci/runs/<id>`;
- the stubbed 502.

## Fixes in this change

### 1. The repo page's workflow editor keeps a row added mid-save, and a removal

- **Reproduction** (fresh demo, `/repos/acme/widgets`):
  1. **Add workflow** gives `lint.yml` with a valid workflow, which saves
     on blur.
  2. Edit its text and click **Add workflow**: the empty row appears and
     is gone once the save returns.
  3. Separately, with lint stored, **Add workflow**, type only `half.yml`
     and press **Remove** on lint. The row vanishes, no request is made,
     and lint is back after a reload.
- **Cause**:
  - `saveWorkflows` replaced every row with the server's answer, so a row
    added while the request was out was lost.
  - `removeWorkflow` went through the same save, which returns early while
    any row has only one half filled in.
- **Fix** (`RepoPage.jsx`):
  - A save settles only the rows it sent (`settleWorkflowEdits`, matched
    by object identity) and keeps every other row.
  - Remove deletes exactly the stored workflow its row stands for, from
    the set the server last returned. It asks first ("Delete the workflow
    … Nothing else keeps a copy of it"). A row never stored is only
    dropped from the form.
  - The button is named `Remove workflow <path>` rather than a second
    bare "Remove" beside the repo header's own.
- **Regression coverage**:
  - Four `RepoPage.test.jsx` cases, which fail without the change.
  - The e2e test "the repo's workflow editor keeps a row added mid-save,
    and a removal".
  - `repository_release_memory_walkthrough.spec.js` was updated for the
    named, confirmed Remove.

### 2. Unsaved memory edits are asked about before they are dropped

- **Reproduction**:
  1. On `/teams/default/settings/models`, open `flaky-e2e` and type into
     it.
  2. Press Escape, or pick another tab. The pane closes or switches, and
     the edit is gone.
  3. Pressing the document's **Close** drops it too.
- **Cause**:
  - Team settings passes no `isDirty` to `Overlay`, because its own fields
    save as they are left.
  - The memory panel's boxes save only on Save.
- **Fix**:
  - `MemoryPanel` tracks which boxes hold an unsaved edit and answers
    through an `unsavedRef`.
  - `TeamSettingsOverlay` passes that to `Overlay`'s `isDirty` (Escape,
    backdrop, back, browser Back). It asks the same question before a tab
    switch.
  - A document's Close asks before dropping an edited document.
- **Verified in the browser** (rebuilt server):
  - Escape and the Members tab both asked "You have unsaved changes here.
    Leave and discard them?". Dismissing kept the edit.
  - After Save, Escape closed with no question.
- **Regression coverage**:
  - Four `MemoryPanel.test.jsx` cases.
  - The e2e test "leaving Team settings asks before dropping an unsaved
    memory edit".

### 3. Removing a qualification item drops it from the others' dependencies

- **Reproduction**:
  1. On `…/releases`, add security-audit and release-qualification, with
     the second depending on the first.
  2. Remove the first.
  3. The chip stays and cannot be unticked, and Save is refused.
- **Fix**: `withoutDanglingDependencies` (`RepoReleases.jsx`) runs on
  every remove and every template change.
- **Regression coverage**:
  - Two `RepoReleases.test.jsx` cases. The component case fails without
    the change.
  - The e2e test "removing a qualification item drops it from the others'
    dependencies".

### 4. Focus returns to "Run release by hand" after a manual run

- **Cause**:
  - **Run release**, which had focus, unmounts when the run starts.
  - The CI run pane restores focus on close to whatever held it when it
    opened.
- **Fix**: focus moves to the **Run release by hand** button before
  `onOpenRun`.
- **Verified in the browser**: after Escape, focus was on `BUTTON: run
  release by hand`.
- **Regression coverage**: a `RepoReleaseWorkflowSettings.test.jsx` case,
  which fails without the change.

### 5. Release job lists drop repeated names

- **Fix**: `cleanJobNames` (`pkg/ui/repo_release_workflows.go`) trims,
  drops blanks and drops repeats for both `postMergeDispatchJobs` and
  `baseBranchJobs`.
- **Verified in the browser**: `build-cache, , docs ,build-cache` read
  back as `build-cache, docs`.
- **Regression coverage**: `TestRepoReleaseJobListsDropRepeatedNames`.

### Tests run

- `go test ./pkg/ui/ -run TestRepoRelease`: pass.
- `vitest run`, the whole frontend suite: 185 files, 3091 tests, all
  pass.
- Playwright Chromium:
  - The new `repo_release_memory_edits_walkthrough.spec.js`: 3 passed.
  - `repository_release_memory_walkthrough`, `memory`, `repos_workflows`,
    `repo-pane` and `release-run-ref-checks-walkthrough`: all passed
    (11 in all, with the new spec).

## Observations (not filed)

- **`-acme/x` is accepted as a repo**:
  - `model.validRepoSegment` deliberately checks only URL shape, not
    GitHub's owner rules (no leading hyphen).
  - No clone or API path is broken by it.
  - It is worth a look if a repo name ever reaches a command line as a
    bare argument.
- **`/repos/nobody/missing`**: this draws a full, editable repo page with
  no note that the repo is not on the target list. The releases page for
  the same repo says so.
- **The duplicate-check error**: "acme/widgets already declares
  CLA/AGREEMENT unfixable" names the spelling typed, not the stored
  `cla/agreement`.
- **The qualification plan's Remove buttons**: these are bare "Remove"
  with no item named.
- **The server's plan errors**: these name template ids (`template-2`), not
  the template names the form shows.
- **Workflow order**: the stored workflows come back sorted by path, so a
  reload can reorder the rows typed.
- **Task-filter chips**: the release picker and the task filter chips are
  unchanged since 10-06. Errors still outlive later successes in the
  global banner, and the compare 404 is still shown twice.
