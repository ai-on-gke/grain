# Recurring and composed work UI walkthrough -- 2026-11-07

Task 3161. A walkthrough of the Processes page and everything it lists --
schedules, workflow schedules, templates, suites, suite runs and
triggers -- in a real Chromium against a real `grain demo` server, with
the fixes for what it found.

## What was tested

- **Commit:** `5dd24214` (`origin/main` when the run started). The fixes
  below were then verified live on the same commit plus this change.
- **Server:** `go build -tags grain_e2e ./cmd/grain` (Go 1.26.10), run as
  `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file … -schedules
  -github-sim -fake-ci -scheduled-workflows -reconcile 1s` over a throwaway
  embedded store under a private `TMPDIR`.
- **Frontend:** `npm ci && npm run build` in `ui/` (Node 20.19.0), served by
  that binary.
- **Browser:** Playwright Chromium (`/var/cache/playwright`, chromium-1234),
  headless, 1400×900 / 1440×900 desktop and 390×844 phone (touch), light
  and dark. The scripts were scratch files outside the checkout and are not
  committed.
- **Model:** no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed run or `LOOK=1` review; layout was judged from DOM
  bounding boxes and `scrollWidth`, not by looking at the images.
- The demo's deployment advertises `gemini-2.5-pro` (default) and
  `gemini-2.5-flash`.

### Stubs and seeding

Every create, edit, run, pause, resume and delete went through visible UI
controls. The only things not done through the UI:

| Boundary | How it was stood in for |
| --- | --- |
| GitHub | `-github-sim`: a bare repo in the demo store. Repository declarations were `git push`ed to its `main`, as a merge would. |
| Grain CI | `-fake-ci`: the workflow run a workflow schedule's Run now started was finished with `POST /api/e2e/fake-ci/runs/<id>/finish` (`failure`, job `test`). |
| Agents | The demo runs none. `ui/e2e/releasehelper complete-suite-pass` marked a suite run's current pass's tasks completed, as an agent's run ending in a comment would; the daemon's own 1s reconciler then advanced the run. |
| A proposed follow-up | `POST /api/tasks` with `proposedBy: <pass-1 task>`, the API an agent's `propose_task` uses. |
| Approval of a `require-approval` suite's tasks | `POST /api/tasks/<id>/approve`. |
| A base-less suite run | Neither the API (`POST /api/suite-runs` with `base: ""` is a 400) nor a declaration (a suite schedule without `base:` is refused, see below) can make one any more, so one finished run's `suite_run.base` was set to `''` with a ten-line `pkg/model/sqlite` helper to see how a legacy row renders. |
| Capability policy | Two rules set with `PUT /api/capability-policy`: `cloud#cloud-viewer` denied for tasks on `acme/widgets`, `cloud#cloud-key` admitted on `acme/gadgets`. |
| A grain/viewer session | The page's `GET /api/config` `access` replaced with the viewer preset (as `screenshot.mjs --access viewer` does); the server still answered as the access code. |

### Declarations used

Pushed to `acme/widgets` `main` and read from its Advanced tab:

- `.grain/templates/nightly.yml` -- `Nightly dependency bump`, priority 7,
  `admission: bypass`, a `scope` input (`go`/`npm`), two grants.
- `.grain/templates/pairing.yml` -- `interactive: true`, `keep-running: true`.
- `.grain/templates/opus.yml` -- `model: claude-opus-5-5` (no connection
  serves it).
- `.grain/suites/sweep.yml` -- `until-clean`, `max-passes: 3`,
  `require-approval: true`.
- `.grain/schedules/nightly.yml` -- `template: nightly`, daily 02:30,
  `admission: durable`.
- `.grain/schedules/sweep.yml` -- `suite: sweep`, `base: main`, weekly.
- `.grain/schedules/wf.yml` and `.grain/workflows/nightly.yml` -- a workflow
  schedule.
- `.grain/triggers/design.yml` -- `on: merge`, `paths: [design/**]`,
  `template: opus`.

## Steps and outcomes

### Processes list

| Step | Outcome |
| --- | --- |
| Open Processes from the rail | Lands on `/processes`. The rail reads `Processes 14` on a cold load and `Processes 15` once the page has fetched the per-repo triggers it loads lazily -- expected, noted. |
| Kind chips | `All 15`, `Schedules 4`, `Workflow schedules 2`, `Templates 6`, `Suites 2`, `Triggers 1`. Two chips together give `?kind=template,suite`. |
| Search | `?q=` narrows; no match shows "No processes match your search." |
| Toolbar Source filter | `?f=source:any:acme/widgets` narrows to the declared rows. |
| Reload, Back, Forward | `kind`, `q` and `f` survive all three. |
| Sort | Next run, Last run, Name, Kind and Custom order each reorder the rows. |
| Custom order | Reordered by pointer drag, by keyboard (Enter on the handle, "Move here", Escape cancels) and by touch tap at 390×844 with no horizontal overflow. |
| Derived runs | A template or suite that no schedule fires reads "on demand"; once a schedule fires it, the row shows that schedule's next run, and after Run now its last run, without a reload. |
| Old links | `/schedules`, `/templates`, `/suites`, `/suites/suite-1`, `/schedules/sched-2` and `/templates/template-2` land on the matching Processes view or pane. |
| New menu | `+ New` offers Schedule, Template and Suite, plus the hint that workflow schedules and triggers are declared in a repository (a link to the declarations doc). |
| Narrowed New | With only Schedules, Templates or Suites chosen the button reads `+ New schedule` / `+ New template` / `+ New suite` and opens that form directly; with Workflow schedules it stays the menu. |

### Schedules

| Step | Outcome |
| --- | --- |
| Raw task schedule | Created with title, description, repo, base, reads, writes, grants, Keep running, network and custom fields; the preview followed every edit. |
| Every N minutes | 5 is refused by the box ("Value must be greater than or equal to 10."); 15 saves. |
| Daily / Weekly / Monthly | Saved with a time zone (`America/Los_Angeles`); the time label names the zone. |
| Overlap | Skip and Allow each show their own helper text and persist. |
| Model picker | Offers only `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`. The declared `Opus review` reads "claude-opus-5-5 (no connection advertises it)". |
| Grants | `Add grant…` shows the policy verdicts: with no repo "may be refused by the capability policy…", on `acme/widgets` `cloud-viewer` disabled as denied by rule 1, on `acme/gadgets` `cloud-key` admitted; `repo#read` and `release#*` say they are CI only. The Grain API group lists `api:` scopes with their repos. |
| Run now | With unsaved edits it asks first; a second Run now while the first task is open is refused (409, overlap). |
| Pause / Resume | Pause shows "Held -- fires … if resumed by then"; Resume clears it. |
| Delete | Dismissing the confirm keeps the schedule; accepting removes it, and it stays gone after reload. |
| `git_ref` off-target | The row shows the Suppressed chip and the pane says why. |
| Template-backed schedule | Firing `WT3161 full chat` with a model override; the template's row followed the schedule's next and last runs without a reload. |
| Suite-running schedule | `Fires: A suite` → `WT3161 suite`, base `main`. The suite row moved from "on demand" to the schedule's next run without a reload; Run now started a suite run whose Scheduled chip opens `/processes/schedules/<id>`. |

### Templates

| Step | Outcome |
| --- | --- |
| Fully populated template | Priority 8, interactive, Keep running, auto-merge, network none, nothing-needed ack, model `gemini-2.5-flash`, reviewers, reads, writes on `acme/gadgets@release/2.0`, three grants, custom fields. Edited and reopened across a reload. |
| Priority validation | 0, -1 and 1.5 are each refused with "a whole number, 1 or more; default 5, higher numbers run first". |
| New task from this template | On a template with no inputs it opens New task on that template with priority 8 carried; on `Nightly dependency bump` (an input) it opens Use… instead, whose preview follows `Scope`. |
| Use… on an interactive template | Shows the live-chat note in place of the Approved box; the task filed (#21) carried priority 8, interactive, Keep running and the model. At 390×844 every long grant chip stays inside the dialog. |
| From a suite | The suite's "What each pass files" lists both templates with Task priority 8 and 6; at 390×844 the widest chip ends at 374px in a 390px pane. |
| Dependency-aware delete | Deleting `WT3161 full chat` while a schedule and a suite use it is refused in the pane: "template is used by 1 schedule(s) ("WT3161 live triage (edited)") and 1 suite(s) ("WT3161 suite"); repoint or delete the schedules and remove it from the suites first". Deleting the suite while a schedule runs it: "suite is used by 1 schedule(s) ("WT3161 suite"); repoint or delete those first". |

### Suites

| Step | Outcome |
| --- | --- |
| Create | `WT3161 suite` from `WT3161 full chat` plus `WT3161 inline tmpl`, made with `+ New template` inside the suite form and preselected on save. |
| Pass limits | Max passes 101 refused ("Value must be less than or equal to 100."), 99 saved, edited to 3 and read back after reload ("run until clean (max 3)"). The preview says "as edited above, not as saved" while editing. |
| Fixed count | A throwaway suite: count 0 refused, 2 saved ("run 2×"), auto-merge off, approval on. Delete: dismiss keeps it, accept removes it, gone after reload. |

### Suite runs

| Step | Outcome |
| --- | --- |
| From the suite row | `Run…` on `WT3161 suite`, `acme/widgets` `main`: run 31 went active on pass 1. A follow-up proposed by its first task made pass 1 "changed", so the reconciler fired pass 2; a clean pass 2 ended it Succeeded. |
| From `+ Run` | `Declared sweep` (require-approval) on `main`: its tasks waited for approval; approved and completed, the run Succeeded. |
| Run pane | `/processes/suites/runs/31` groups Pass 1 and Pass 2, nests the proposed follow-up under the task that proposed it, and a task row opens `/tasks/<id>`; Back returns to the run. |
| Failure | Another run of `WT3161 suite`; after pass 1 its schedule, the suite and `WT3161 inline tmpl` were deleted through the UI. Completing pass 1 ended the run Failed with "pass 2 could not be filed: template template-22 no longer exists" on its row and in its pane. |
| Default branch | `POST /api/suite-runs` with `base: ""` is a 400, and a declared suite schedule without `base:` is refused on the Advanced tab ("a schedule that runs a suite needs `base:` -- a suite run has no default branch to fall back to"). A legacy base-less row (seeded, above) reads "default branch" on its row and in its pane. |
| Direct URLs | `/suites/runs/33` lands on `/processes/suites/runs/33` and survives reload; `/processes/suites/runs/9999` leaves the page standing with no error. |

### Repository declarations

| Step | Outcome |
| --- | --- |
| A bad declaration | A template asking for `repo-read:acme/off-target`, and a trigger with an unknown `name:` key, were each refused with the reason on the Advanced tab. |
| Declared rows | Each says "Declared by acme/widgets"; each pane names `.grain/...` in its notice, disables every field, Save, Delete and Pause, and keeps Run now and New task from this template. `DELETE` on the API for each is a 400. |
| Workflow schedule Run now | The confirm says "It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged." The row showed the run as running, then after fake CI failed it, "failure …, reviewed in task 124". |
| Trigger | The `design.yml` row opens `/repos/acme/widgets/settings/advanced` showing `.grain/triggers/design.yml`. |

### Keyboard, console and network

- A row link focused and Enter opens its pane; Escape closes it. Escape
  inside an open `Add grant…` popover closes the whole pane behind the
  discard prompt rather than just the popover -- noted, not changed.
- No page errors in any run. The only console errors and 4xx responses
  were the refusals the steps asked for (the dependency-aware deletes, the
  busy Run now, the invalid declarations, the declared-row API deletes).

## Bugs found and fixed

### 1. Admission was missing from template and schedule forms, and misreported in previews

`admission` (Standard / Bypass queue / Durable) replaced the
priority-dispatch and durable legacy capabilities and is a column on
templates and schedules as well as tasks, but:

1. Neither the template nor the schedule pane had an Admission field, so a
   template's or schedule's admission could not be set from the UI, and a
   declared one (`admission: bypass`) was not visible.
2. The composition preview printed the field by its wire name and value:
   `admission bypass from the template`.
3. Opening the declared `Nightly bump schedule` (template `bypass`,
   schedule `durable`) previewed `bypass, from the template`, while
   `GET /api/schedules/<id>/composition` -- and the firing -- said
   `durable, from the schedule`. The pane previews through the draft
   endpoint, which copied the template's content and dropped the
   schedule's own admission.

Reproduction (base build, the browser reading the pane):

```json
{
  "declaredScheduleAdmissionRow": "admissionbypassfrom the template",
  "declaredScheduleApi": { "name": "admission", "values": ["durable"], "source": "schedule" },
  "declaredTemplateAdmission": null,
  "rawScheduleHasAdmissionPicker": false
}
```

Fix: an `AdmissionField` select (the task pane's wording and help) on the
template pane and on a raw schedule's pane, sent only when it changes;
`CompositionPanel` names the row "Admission" and the value "bypass queue";
the schedule pane always sends the admission in force with its preview,
and `DraftScheduleComposition` raises a template-backed draft's admission
by it (preview only: `CreateSchedule` still refuses an admission sent
beside a template). After:

```json
{
  "declaredScheduleAdmissionRow": "Admissiondurablefrom the schedule",
  "declaredTemplateAdmission": "Bypass queue",
  "rawScheduleHasAdmissionPicker": true
}
```

Live on the patched build: a new template saved with Durable
(`admission: "durable"` posted and stored), reopened after reload reading
Durable, edited to Bypass queue and stored as `bypass`; a schedule firing
it shows no picker and previews "Admission bypass queue from the
template"; a raw schedule's preview gained "Admission bypass queue from
the schedule" as soon as Bypass queue was picked, it was posted and
stored as `bypass`, reopened reading Bypass queue, and lowered to
Standard by a PATCH of `standard`; the declared template shows Bypass
queue, disabled. At 390×844 in dark the field spans 17–374px of a 390px
pane with no horizontal scroll.

Regression coverage: `TestDraftScheduleCompositionKeepsAScheduleAdmissionOverItsTemplates`
(`pkg/ui/composition_test.go`); `CompositionPanel.test.jsx` (the row in
words); `TemplateOverlay.test.jsx` (not sent when standard, read back,
sent on change, not resent unchanged); `App.schedules.test.jsx` (a raw
schedule's admission read and saved; a template-backed schedule's
preview carries its own admission and offers no picker).

### 2. A suite's Delete, Save and "+ New template" were not gated by role

When the Processes page's writes were drawn for the signed-in person's
role (`AccessGate`), the suite pane was missed: a grain/viewer opening a
suite was offered Delete, Save and "+ New template", and refused only on
the click.

Reproduction (base build, viewer access):
`{"Delete": "ENABLED", "Save": "ENABLED", "+ New template": "ENABLED", "gatedWrappers": []}`.
After: all three disabled, wrapped as `suites:write`, `suites:write` and
`templates:write` with the role's reason; Cancel still works.

Regression coverage: `SuiteOverlay.test.jsx` ("draws a suite's writes
disabled for a role without suites:write").

## Tests run

- `go test ./pkg/ui/ ./tests/hygiene/...`, `go vet ./pkg/ui/`, `gofmt -l`.
- `npx vitest run --maxWorkers=4` (whole suite) and the four touched files
  on their own; `prettier --check` on the touched files.
- `ui/e2e/screenshot.mjs --url … --full-page` before and after: `/processes`,
  the declared template, `sched-2`, the declared template-backed schedule,
  and `/processes/suites/suite-1` with `--access viewer`, desktop and phone,
  light and dark.

The first CI run of this change failed two checks that it does not
touch: `go unit` on `TestRunUpgradesAContainerDeploymentWhenTheButtonIsClicked`
(`pkg/cli`, a sandbox-image digest check in the upgrade path; it passes
locally with `-count=3`), and `ui e2e` on
`waiting-tasks-and-questions-walkthrough.spec.js` ("sandbox 2-1 did not
reach agent-ready"), with the other 973 specs passing.
