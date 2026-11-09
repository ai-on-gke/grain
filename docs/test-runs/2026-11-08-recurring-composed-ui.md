# Recurring and composed work UI walkthrough -- 2026-11-08

Task 3357. A walkthrough of the Processes page and everything it lists --
schedules, workflow schedules, templates, suites, suite runs and
triggers -- in a real Chromium against a real `grain demo` server, with
the fixes for the two bugs it found.

## What was tested

- **Commit:** `6d15dc3a` (`origin/main` when the run started, "Merge pull
  request #912"; the binary reports `v0.0.0-20261108080820-6d15dc3a27aa`).
  Both fixes below were then checked live on the same commit plus this
  change.
- **Server:** `go build -tags grain_e2e ./cmd/grain` (Go 1.26.10), run as
  `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file …
-schedules -github-sim -fake-ci -scheduled-workflows -sensitive-hold
-reconcile 1s` with `TMPDIR` set to a scratch directory, so the store was
  a throwaway embedded SQLite file.
- **Frontend:** `npm ci && npm run build` in `ui/` (Node 20.19.0), embedded
  in that binary.
- **Browser:** Playwright Chromium (`/var/cache/playwright`, chromium-1234),
  headless, timezone UTC. Desktop 1400×900 (also 1400×1100–2400 to keep long
  panes on screen), 1024, 620, and 390×844 phone with touch.
- **How it was driven:** throwaway Playwright scripts outside the checkout,
  not committed. Each one signs in on the "Access code" page and then acts
  only through visible controls. Every page logged `pageerror`, console
  errors and warnings, `/api/` responses of 400 or more (with their
  bodies), failed requests, every dialog's text and whether it was
  accepted, and every non-GET `/api/` request with its body. The REST API
  was used only to read back what a gesture had persisted, and for the
  stubs below.
- **Model:** there was no `/home/debian/.gemini-api-key` in this sandbox,
  so no Gemini-backed run and no `LOOK=1`. Layout was judged from DOM
  bounding boxes, `scrollWidth` and `scrollWidth > clientWidth` checks,
  not by looking at the images. The demo's deployment advertises
  `gemini-2.5-pro` (default) and `gemini-2.5-flash`.

### Stubs and seeding

| Boundary              | How it was stood in for                                                                                                                                                                                              |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GitHub                | `-github-sim`: a bare repo in the demo store. The declarations below were `git push`ed to its `main`, as a merge would, and read by the demo's own 2-minute declarations poll.                                       |
| Grain CI              | `-fake-ci`. A workflow schedule's Run now started a run that nothing executes. It was left running.                                                                                                                  |
| Agents                | The demo runs none. `ui/e2e/releasehelper complete-suite-pass` marked a suite run's current-pass tasks completed, as an agent's run ending in a comment would. The daemon's own 1s reconciler then advanced the run. |
| A proposed follow-up  | `POST /api/tasks` with `proposedBy: "24"` (a pass-1 task), the API an agent's `propose_task` uses. Recorded as `origin: task:24`.                                                                                    |
| Capability policy     | `PUT /api/capability-policy` restricting the `example-legacy` in-process capability to `acme/gadgets`.                                                                                                               |
| A base-less suite run | Neither `POST /api/suite-runs` nor a declaration can make one any more (see 2026-11-07), so finished run 2's `suite_run.base` was set to `''` with `sqlite3` (installed with apt) to see how a legacy row renders.   |
| Screenshots           | Two suite runs of the demo's `suite-1` filed with `POST /api/suite-runs` on each server so `/processes` had runs to draw.                                                                                            |

### Declarations used

Pushed to `acme/widgets` `main`:

- `.grain/templates/bump.yml` -- `WT Nightly dependency bump`, priority 7,
  `admission: bypass`, a `scope` input (`go`/`npm`).
- `.grain/templates/pairing.yml` -- `interactive: true`, `keep-running: true`,
  `hosting: console`.
- `.grain/templates/opus.yml` -- `model: claude-opus-5-5` (no connection
  serves it).
- `.grain/suites/sweep.yml` -- `until-clean`, `max-passes: 3`,
  `require-approval: true`.
- `.grain/schedules/bump.yml` -- `template: bump`, daily 02:30,
  `admission: durable`, `hosting: console`.
- `.grain/schedules/sweep.yml` -- `suite: sweep`, `base: main`, weekly.
- `.grain/workflows/nightly.yml` -- `cron: "11 2 * * *"`, which becomes a
  workflow schedule.
- `.grain/triggers/design.yml` -- `on: merge`, `paths: [design/**]`,
  `template: opus`.

A first push also had `.grain/schedules/wf.yml` (`workflow: nightly` with
no cadence). It was refused, and the reason was shown on the repository
and in the log: "a schedule needs exactly one cadence … and this one has
0". The last good declarations stayed in force. That file was then
removed. Expected, noted.

## Steps and outcomes

### Processes list

| Step                  | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                        | Verdict |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Rail → Processes      | Lands on `/processes`. The rail read `processes 9` on the cold first load and `14` once the declarations had synced and the page had fetched the lazily loaded triggers. The page's All chip agreed.                                                                                                                                                                                                                                           | PASS    |
| Kind chips            | `All 14`, `Schedules 4`, `Workflow schedules 1`, `Templates 6`, `Suites 2`, `Triggers 1`. Each narrows to its rows (4/1/6/2/1) and writes `?kind=schedule`, `workflow`, `template`, `suite` or `trigger`. Two chips give `?kind=template,suite` with 8 rows.                                                                                                                                                                                   | PASS    |
| Search                | `release` → `?kind=template,suite&q=release`, 2 rows. A string that matches nothing shows "No processes match your search."                                                                                                                                                                                                                                                                                                                    | PASS    |
| Reload, Back, Forward | Reload keeps `kind` and `q`. Narrowing on the same page replaces the history entry by design (`App.jsx`'s `samePage`), so Back leaves Processes for the page before it, and Forward restores `?kind=template,suite&q=audit` with the search box filled.                                                                                                                                                                                        | PASS    |
| Sort                  | The toolbar opens on "Next run soonest first". Last run (`?sort=last:desc`), Name (`name:asc`, case-insensitive), Kind (`kind:asc`, schedules → workflows → templates → suites → triggers) and Next run (`next:desc`) each reorder the rows as named.                                                                                                                                                                                          | PASS    |
| Custom order          | `?sort=custom`. Keyboard: Enter on a row's handle picks it up (`aria-pressed=true`, 13 "Move here, above …" targets) and Escape cancels; Enter, then Enter on "Move here, above design.yml", moves it to the top. Pointer: an HTML5 drag of an on-screen row onto the second row moves it there. Touch at 390×844: tap the handle, then tap Move here. The order survives a reload, and the page has no horizontal scroll (`scrollWidth` 390). | PASS    |
| Derived runs          | Templates and suites no schedule fires read "on demand". The template a new schedule fires changed from "on demand" to that schedule's next run without a reload. After the 09:00 firing, the suite row read last run 09:00 and next run tomorrow 09:00, matching its schedule.                                                                                                                                                                | PASS    |
| Old links             | `/suites/runs/1` lands on `/processes/suites/runs/1`. A deleted schedule's deep link closes onto `/processes` with `no schedule "sched-3"`, and a missing run with `no suite run "9999"`. (`/schedules`, `/templates` and `/suites[/<id>]` are pinned by `processes-walkthrough.spec.js` and were not repeated.)                                                                                                                               | PASS    |
| New menu              | `+ New` offers Schedule, Template and Suite, and "Workflow schedules and triggers are declared in a repository's .grain/ directory -- declarations."                                                                                                                                                                                                                                                                                           | PASS    |
| Narrowed New          | With only Schedules, Templates or Suites chosen, the button reads `+ New schedule`, `+ New template` or `+ New suite`. With Workflow schedules or Triggers it stays `+ New`.                                                                                                                                                                                                                                                                   | PASS    |

### Schedules

| Step                              | Outcome                                                                                                                                                                                                                                                                                                                                   | Verdict      |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| Raw task schedule                 | Title, description, read-only repo `acme/gadgets`, Keep running, network egress, model `gemini-2.5-flash`, Hosting Interactive console, custom field Priority high. The preview followed each edit ("Keep running on from the schedule", "Network egress (outbound internet only)"). Its hosting row read `hosting console` -- see bug 1. | PASS (bug 1) |
| Every N minutes                   | 5 is refused by the box ("Value must be greater than or equal to 10."). 15 saves, and the row reads `every 15m`.                                                                                                                                                                                                                          | PASS         |
| Overlap                           | "Skip this run" (the default: "The cadence waits: the skipped run happens as soon as the last one finishes") and "Start it anyway" are offered.                                                                                                                                                                                           | PASS         |
| Model picker                      | Offers only `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro` and `gemini-2.5-flash`. The declared `WT Opus review` reads "claude-opus-5-5 (no connection advertises it)".                                                                                                                                                          | PASS         |
| Grants                            | `Add grant…` lists the Grain API presets with what each grants (deployment-ops and self-upgrade marked dangerous). Picking ci-read adds `api:ci-read`, and the schedule saved `capabilities: ["api:ci-read"]`. Escape in the popover closes the popover only; the pane stays open (the 2026-11-07 note about it is out of date).          | PASS         |
| Run now with unsaved edits        | The confirm says "… Your unsaved changes here are discarded, not run." It fired (last run 8:40:06). A second Run now was refused with a 409, and the reason was shown in the pane: "its previous run has not finished yet, and this schedule is set to skip a run that would overlap one …".                                              | PASS         |
| Edit                              | Retitled to "WT3357 raw schedule (edited)". `PATCH` sent, and the row followed without a reload.                                                                                                                                                                                                                                          | PASS         |
| Pause / Resume                    | Pause sends `{"enabled":false}`, and the row reads "Held -- fires 11/8/2026, 8:54:45 AM if resumed by then", "paused". Resume sends `{"enabled":true}` and puts back the next run. Both close the pane by design (`ScheduleOverlay.jsx`).                                                                                                 | PASS         |
| Delete                            | "Delete the schedule … Tasks it already filed are not affected." Dismissing keeps it. Accepting removes it, and it stays gone after reload.                                                                                                                                                                                               | PASS         |
| Suite-running schedule            | `+ New` → Schedule → Fires: A suite → `WT3357 suite`, daily 09:00, base `main`. The suite row moved from "on demand" to 9:00 without a reload. At 09:00 the demo's schedule step fired it on its own (run 4), and that run's Scheduled chip opens `/processes/schedules/sched-4`.                                                         | PASS         |
| Template-backed schedule          | Fires `WT3357 full template` on `acme/widgets`. The preview shows its model, Keep running, network none and "Task priority 8 from the template".                                                                                                                                                                                          | PASS         |
| Declared template-backed schedule | `WT Bump schedule`: the preview has "Admission durable from the schedule" but no Hosting row, while `GET …/composition` says hosting console from the schedule -- bug 1.                                                                                                                                                                  | FAIL → fixed |

### Templates

| Step                            | Outcome                                                                                                                                                                                                                                                                                                                                                                                                | Verdict |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| Fully populated template        | `WT3357 full template`: priority 8, interactive, Keep running, model `gemini-2.5-flash`, Hosting Interactive console, network none. Posted and stored as such, and read back after reopening its deep link.                                                                                                                                                                                            | PASS    |
| Task priority validation        | 0, -1 and 1.5 are each refused with "a whole number, 1 or more; default 5, higher numbers run first" (and the browser's own message).                                                                                                                                                                                                                                                                  | PASS    |
| New task from this template     | No inputs: opens `/new` on that template with priority 8 carried. On `WT Nightly dependency bump` (a `scope` input) it opens Use… instead, with the Scope picker and a "Bump go dependencies" preview.                                                                                                                                                                                                 | PASS    |
| Use… on an interactive template | Shows "This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed." in place of the Approved box. "Create task" filed #23 with priority 8, interactive, Keep running, hosting console, model `gemini-2.5-flash` and network none, then opened `/tasks/23`.                                                                           | PASS    |
| From a suite                    | The suite form's "What each pass files" lists both templates; the first shows "Task priority 8 from the template".                                                                                                                                                                                                                                                                                     | PASS    |
| Dependency-aware delete         | Deleting `WT3357 full template` while a schedule and a suite use it is refused in the pane: "template is used by 1 schedule(s) ("WT3357 chat about the widgets") and 1 suite(s) ("WT3357 suite"); repoint or delete the schedules and remove it from the suites first". Deleting the suite while a schedule runs it: "suite is used by 1 schedule(s) ("WT3357 suite"); repoint or delete those first". | PASS    |

### Suites

| Step                              | Outcome                                                                                                                                                                                                                                                                                                                           | Verdict |
| --------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Create                            | `WT3357 suite` from `WT3357 full template` plus `WT3357 inline tmpl`, made with `+ New template` inside the suite form and preselected on save. Run mode "run until no repo changes or new tasks", max passes 101 refused ("Value must be less than or equal to 100."), saved at 3. Row: "run until clean (max 3) · 2 templates". | PASS    |
| Fixed count, approval, auto-merge | `WT3357 throwaway suite`: "run a fixed number of times", Number of times 0 refused, 2 saved, auto-merge off, approval on. Row: "run 2× · requires approval".                                                                                                                                                                      | PASS    |
| Delete                            | "Delete the suite … Runs already started from it are unaffected." Dismissing keeps it. Accepting removes it, and it stays gone after reload; its template then deleted through its own pane.                                                                                                                                      | PASS    |

### Suite runs

| Step               | Outcome                                                                                                                                                                                                                                                       | Verdict      |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| From the suite row | `Run…` on `WT3357 suite`, `acme/widgets`, branch `main`: run 1, active, pass 1 with tasks 24 and 25. A follow-up proposed by 24 made pass 1 changed, so the reconciler filed pass 2 (28, 29). A clean pass 2 ended it Succeeded.                              | PASS         |
| From `+ Run`       | `WT Declared sweep` (require-approval) on `main`. The dialog's preview says "Approval waits for approval from the suite" and "Model claude-opus-5-5". Run 2 succeeded once its task was completed.                                                            | PASS         |
| Run pane           | `/processes/suites/runs/1` groups Pass 1 (3, with follow-up #27 nested under #24) and Pass 2 (2). It survives a reload. A task link opens `/tasks/24` ("work from task 24: 0 of 1 done"), and Back returns to the run.                                        | PASS         |
| Failure            | The throwaway suite's run 3 on `release/1.0`. After pass 1 the suite and its template were deleted through the UI; completing pass 1 ended the run Failed, with "pass 2 could not be filed: template template-7 no longer exists" on its row and in its pane. | PASS         |
| Scheduled          | Run 4 (fired by `sched-4` at 09:00): Active · acme/widgets · main · Scheduled.                                                                                                                                                                                | PASS         |
| Default branch     | The legacy base-less run 2 (stubbed above) reads "default branch" on its row and in its pane.                                                                                                                                                                 | PASS         |
| Row layout         | The failed run's row did not show its branch at 1400px, and at 620px and 390px every run's status chip was cut inside its pill (8–50px of "failed" or "succeeded") -- bug 2.                                                                                  | FAIL → fixed |

### Repository declarations

| Step                      | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Verdict |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Declared rows             | Each says "Declared by acme/widgets"; the chip's title names the file (for example "Declared in acme/widgets, .grain/workflows/nightly.yml").                                                                                                                                                                                                                                                                                                                          | PASS    |
| Declared panes            | The schedule, template and suite panes each name the file in their notice ("This suite is declared by acme/widgets in .grain/suites/sweep.yml, so it cannot be edited or deleted here. …") and disable the fields, Save, Delete and Pause, and on a suite also `+ New template`. Run now and New task from this template stay on. Not one PATCH or DELETE was sent for a `repo:` id. `DELETE /api/templates/repo:template:acme:widgets:opus` is a 400 naming the file. | PASS    |
| Workflow schedule Run now | The confirm says "It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged." The row then read "Last run (none): starting".                                                                                                                                                                                                                                                      | PASS    |
| Trigger                   | The `design.yml` row opens `/repos/acme/widgets/settings/advanced`, which lists `.grain/triggers/design.yml`.                                                                                                                                                                                                                                                                                                                                                          | PASS    |

### Keyboard, console and network

- No `pageerror` in any script. The only console errors and 4xx responses
  were the refusals the steps asked for: the busy Run now (409), the
  dependency-aware deletes (400), and the declared-row API delete (400).
  There was also one 401 from each fresh context's first load before
  sign-in. The aborted `GET /api/tasks` requests on sign-in are the
  task-list poll being cancelled by the navigation.
- Kind chips, sort controls, row links and drag handles are buttons or links
  reachable by Tab. Reordering works from the keyboard alone (above).
- Not exercised: the viewer-role gating (pinned on 2026-11-07 and by
  `SuiteOverlay.test.jsx`), weekly and monthly cadences, and time zones
  other than UTC.

## Bugs found and fixed

### 1. A template-backed schedule's own hosting was missing from its preview

A repository's schedule may say `hosting: console` beside `template:`,
and its firings are composed with it. The schedule pane previews through
`POST /api/schedules/composition`. `DraftScheduleComposition` raised the
draft's admission by the schedule's own (the 2026-11-07 fix) but dropped
its hosting, so the preview had no Hosting row. A raw schedule's preview,
meanwhile, showed the field by its wire name, `hosting console`.

Reproduction (base build, declared `.grain/schedules/bump.yml` above):

```text
GET  /api/schedules/repo:schedule:acme:widgets:bump/composition
     … {"name":"admission","values":["durable"],"source":"schedule"},
       {"name":"hosting","values":["console"],"source":"schedule"} …
POST /api/schedules/composition
     {"templateId":"repo:template:acme:widgets:bump","admission":"durable","hosting":"console",…}
     … {"name":"admission","values":["durable"],"source":"schedule"} …   (no hosting)
Pane: "… Approval | approved when filed | from the schedule | Admission | durable |
       from the schedule | Task priority | 7 | from the template"
```

Fix: `DraftScheduleComposition` raises a template-backed draft's hosting by
`req.Hosting` with `model.MaxHosting`, as it does admission. This affects
only the preview: `CreateSchedule` still refuses content sent beside a
template. `CompositionPanel` labels the row "Hosting" and words `console`
as "interactive console", as `HostingField` does.

Regression coverage:
`TestDraftScheduleCompositionKeepsAScheduleHostingOverItsTemplates`
(`pkg/ui/composition_test.go`, fails without the fix: `no "hosting"
field`), and `CompositionPanel.test.jsx` ("names the hosting row and its
value in words").

### 2. A suite run's status chip was cut inside its pill on a narrow row

On the Suite runs list, a run's `.chips` strip (status, repo, branch)
shrank in proportion with the name and the pass and error hints beside it.
Once the strip was narrower than its first chip, `ChipStrip` had no second
line to drop that chip onto, so the status was cut inside its own pill.

Reproduction (base build, Chromium; each chip's width, `H` = hidden,
`T` = text truncated):

```text
1400px  failed:47 acme/widgets:95 release/1.0:0H   (the branch dropped beside a long error)
1024px  failed:30 acme/widgets:0H release/1.0:0H
 620px  failed:8T  …   succeeded:23 …   succeeded:43 …
 390px  failed:10T …   succeeded:26 …   succeeded:50 …
```

Fix: `.suite-run-row .chips { min-width: min-content; }` in `style.css`.
The strip's min-content is its widest chip still laid out, since
`ChipStrip` takes the wrapped ones out. The status therefore always reads
whole and only the chips after it give way. The name's floor and the
hints' shrinking are unchanged. After the fix, the status is whole at
1400, 1024, 620 and 390px (47px "failed", 79px "succeeded"), the row's
right edge stays inside the window, and `scrollWidth` equals the window
width. The 1400px row still drops a failed run's branch beside a long
error. That is the documented trade-off (chips are dropped whole), and
the run's pane names the branch.

Regression coverage: `e2e/narrow.spec.js` "keeps a suite run's status
whole at 620px" and "… at 390px". Both fail on the base build ("the run's
status chip was cut inside its own pill") and pass with the fix, alongside
the file's other five cases.

## Tests run

- `go test ./pkg/ui/ ./tests/hygiene/...` (pass), `gofmt -l pkg/ui` (clean).
- `npx vitest run src/components/CompositionPanel.test.jsx` (17 pass).
- `npx playwright test e2e/narrow.spec.js` (7 pass; the 2 new cases fail
  before the fix).
- `prettier --check` on the touched UI files.
- `ui/e2e/screenshot.mjs --url …` of `/processes` and the declared
  schedule's pane on the base and patched builds, at desktop and phone
  widths in light and dark (attached to the pull request, not committed).
