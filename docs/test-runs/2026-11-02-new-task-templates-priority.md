# Test run — New Task from a template, the board's form layer, and priority on every surface, 2026-11-02

Plan: `docs/test-plans/2026-11-02.md` §6. Scope: PR #410 ("New task from
this template", the Template picker, `templates:` layers), PR #376 (the
board's layer and `boardId`, the left-out link values notice), and
priority on templates, Bulk edit, Edit, number boxes and the API (PRs
#350, #361, #417, #391, #337, #347). Filed by task 2399; walked by task
2448.

## Environment

- Tree: `main` at `8726f7f7`, then this branch's fixes on top.
  `cd ui && npm ci && npm run build`, `go build -o ~/bin/grain ./cmd/grain`.
  No sibling §6 walkthrough had touched these files
  (`git log origin/main -- ui/src/components/NewTaskOverlay.jsx ...`).
- **A real `grain daemon`** with its own data dir and its internal
  grain/config (`<data-dir>/state-repo`, no remote), the same flags as
  the 11-01 run:

  ```
  grain daemon -data-dir /var/tmp/gd/data -ui-addr 127.0.0.1:4310 \
    -sandbox-port-base=0 -host-sandboxes -sandbox-dir /var/tmp/gd/sb \
    -github-host 127.0.0.1:1 -github-insecure-http \
    -repo acme/widgets -model-backend claude
  ```

  Committing `settings/new-task-form.yml` to the state repo as
  `operator` (the end state a merged config PR reaches) was imported on
  the next state-sync tick; `GET /api/config` then carried all four
  layers. The daemon was restarted twice on the same data dir to pick
  up the fixed binary.
- Bootstrap through the API: `template-1` "bugfix" (bound to
  `acme/widgets`, priority 8, `read-issues`, reads `acme/gadgets`,
  title "Fix from the bugfix template", body "Template body text"),
  `template-2` "loose" (no repo, priority 3), `template-3` "withinputs"
  (a required input field `who`), board `board-1` "Ops", target repos
  `acme/widgets` and `acme/gadgets` (`-repo acme/widgets` alone does not
  put it on the target list), and a dummy GitHub token so a person's
  filing passes the reach check.
- **Stubbed boundaries:** the model backend (none configured, so filed
  tasks wait) and GitHub (`127.0.0.1:1`, never reached). Host sandboxes,
  so the network picker is drawn disabled (`sandboxShapeIgnored`) and
  every filing sends `networkAccess: ""` — by design, not a finding.
- Browser: Playwright Chromium (`/var/cache/playwright`), headless, the
  access code as a bearer header; ad-hoc scripts for the walk, and
  `grain demo` (via `ui/e2e/fixtures.ts`) for the specs. `grain demo
  -github-sim -fake-ci` was not needed separately: the priority matrix
  ran on the daemon.
- The `grain` CLI needed no `GRAIN_SANDBOX_PROXY`.

### The committed form (`settings/new-task-form.yml`)

```yaml
default:
  fields:
    networkAccess: {visibility: shown, order: 15, label: "Network (default layer)", default: open}
    promptExtension: {visibility: shown, order: 16, label: "Ext (default layer)", default: "default-ext"}
    autoMerge: {visibility: hidden, default: true}
    atFront: {visibility: hidden, default: true}
    dependsOn: {visibility: more}
boards:
  board-1:
    fields:
      networkAccess: {label: "Network (board layer)", default: none}
      promptExtension: {label: "Ext (board layer)", default: "board-ext"}
      approved: {visibility: hidden, default: false}
      model: {visibility: hidden, default: "board-model"}
repos:
  acme/widgets:
    fields:
      promptExtension: {label: "Ext (widgets repo layer)", default: "widgets-ext"}
templates:
  template-1:
    fields:
      networkAccess: {label: "Network (template layer)", default: none}
      promptExtension: {label: "Ext (template-1 layer)", default: "template-ext"}
      model: {visibility: hidden, default: "template-model"}
  template-2:
    fields:
      description: {label: "Description (template-2 layer)", default: "t2 layer desc"}
  template-3:
    fields:
      networkAccess: {label: "Network (template-3 layer)"}
```

## 1. Layer precedence — PASS, matches design §6.3

Design §6.3 orders the layers built-in → default → board → repo →
template. That is what the browser showed:

| Opener | Labels drawn | Hidden fields | What wins |
|---|---|---|---|
| `/new` | Network/Ext "(default layer)" | autoMerge, atFront | default; picking `acme/widgets` turns Ext to `widgets-ext` (repo over default) |
| board-1 "+ New task" | Network/Ext "(board layer)" | + approved, model | board; picking `acme/widgets` still turns Ext to `widgets-ext` (repo over board) |
| template-1 page "New task from this template" | Network "(template layer)", Ext "(template-1 layer)" | + model | template over repo (`template-ext`, not `widgets-ext`) |
| board-1, then picker → bugfix | template-1's labels | board's approved **and** template's model | template over board and repo; the board's hidden `approved: false` still applies, since template-1 says nothing about it |
| `/new`, picker → loose | Description "(template-2 layer)" | default's | template-2's description default fills the box |

The picker offered "No template", "loose" and "bugfix" on every opener;
"withinputs" was never offered.

## 2. What is filed — table of opener × filed fields

From `GET /api/tasks/<id>` after a browser filing (title left blank on
the template openers):

| Opener | `templateId` | `boardId` | title | description | priority | model | promptExtension | approved | autoMerge / atFront | reads / capabilities |
|---|---|---|---|---|---|---|---|---|---|---|
| template-1 page | template-1 | — | template's ("Fix from the bugfix template") | template's | 8 | template-model (hidden, template layer) | template-ext | true | true / true (hidden, default layer) | acme/gadgets / read-issues (template's) |
| board-1 | — | board-1 | typed | "" | 5 | board-model (hidden, board layer) | widgets-ext (repo layer) | **false** (hidden, board layer) | true / true | — |
| `/new` | — | — | typed | "" | 5 | team default | widgets-ext | true | true / true | — |
| board-1 + picker bugfix | template-1 | board-1 | typed | template's | 8 | template-model | template-ext | false (board) | true / true | acme/gadgets / read-issues |
| `/new` + picker loose | template-2 | — | typed | "t2 layer desc" (layer default beats the template body: the form stated it) | 3 before the fix, see Bug 1 | team default | widgets-ext | true | true / true | — |

`boardId` is sent in the POST (`boardId: "board-1"`) and acted on (the
board's hidden defaults applied); `GET /api/tasks/<id>` does not echo
`templateId` or `boardId` back, which is how the API has always been.
The template's title fills a blank title, the form's values win where it
stated one, and the template fills reads, capabilities and priority it
left blank — all as §6.3 says, apart from priority (Bug 1).

## 3. Templates with input fields — FAIL before this branch; PASS after

- The picker leaves template-3 out on every opener. PASS.
- Its row's "Use…" on the Templates list asks for Who and files
  `{"templateId":"template-3","fields":{"who":"alice"}}` → task "Input
  alice". PASS.
- **Bug 2:** template-3's own page still drew "New task from this
  template". It opened New Task on the template (the picker reading
  "withinputs", no Who field anywhere), and Create answered 400 `field
  "Who" is required`. **Fixed here:** for a template with input fields
  that button opens Use… instead.

## 4. Priority on every surface

Typed into each box (or sent), with what was sent and what was saved,
**before** and **after** this branch. "native" is the browser's own
min/step bubble ("Value must be greater than or equal to 1.", "Please
enter a valid value. The two nearest valid values are 1 and 2."); "the
wording" is "a whole number, 1 or more; default 5, higher numbers run
first" under the box; the API's is "priority must be a positive integer
(1 or more; leave it out for the default, 5)".

| Typed | New Task | Edit | Bulk edit | Template form | API `POST`/`PATCH` | `propose_task` |
|---|---|---|---|---|---|---|
| `0` | native → **the wording**, Create disabled | the wording | the wording | native → **the wording** | refused, API wording | refused, API wording |
| `-1` | native → **the wording** | the wording | the wording | native → **the wording** | refused, API wording | refused, API wording |
| `1.5` | native → **the wording** | the wording | the wording | native → **the wording** | Go JSON error → **API wording** | **filed priority 1** → refused, API wording |
| `1e3` | saved 1000 | the wording | the wording | the wording | Go JSON error → **API wording** | saved 1000 |
| `99999999999999999999` | **Go JSON error** → the wording | **sent, Go JSON error** → the wording | **passed the check** → the wording | **sent, server refusal** → the wording | Go JSON error → **API wording** | refused (int overflow went negative) → refused, API wording |
| `9007199254740993` | **saved …992** → the wording | **saved …992** → the wording | **passed the check** → the wording | **sent …992** → the wording | (JSON number; int64 holds it) | refused after |
| `2147483648` | saved as typed | — | — | — | saved as typed | — |
| blank | default 5 (or the template's) | "leave it as it is" | Apply disabled (the wording) | no priority / clears | default 5 | default 5 |
| `"8"` (string) | n/a | n/a | n/a | n/a | Go JSON error → **API wording** | **dropped, default 5** → refused, API wording |
| `5` over a template at 8 | **filed 8** → filed 5 | | | | | |

Bold is what this branch changed. Remaining difference, deliberately
left alone: `1e3` is saved as 1000 by New Task (pinned by
`new-task-number-boxes.spec.js`, PR #347) and `propose_task`, and refused
by Edit, Bulk edit and the template form (PR #337/#361/#417 refuse
exponent entries on purpose). Both halves satisfy "saves exactly what was
typed or refuses with the same wording"; making them agree is a product
call, filed below.

Bulk edit over a selection with a task a person may not edit: not
reached. As a person every task is editable — a `PATCH` priority on a
closed task answered 200 — and the agent-side refusal
(`api:tasks:admin` without `api:tasks:write`) is a REST gate the Bulk
edit UI never meets. Covered by `TestTasksAdminCapabilityAllowsEditingPriorityViaREST`.

- **Bug 1:** with a template picked, New Task's priority box read 5 and
  the task was filed at the template's priority (template-1 8,
  template-2 3), because the form left a 5 out and `fromTemplate` fills a
  missing priority. A 5 typed on purpose could never be filed.
- **Bug 3:** New Task, Edit, Bulk edit and the template form let numbers
  past 2^53 through: `9007199254740993` saved as `9007199254740992`, and
  `1e20` came back as `json: cannot unmarshal number … of type int`.
- **Bug 4:** the API answered 1.5, 1e3, `"8"` and 1e20 with Go's decoding
  error rather than the priority wording.
- **Bug 5:** `propose_task` read priority with `int(float64)`: 1.5 filed
  priority 1, and a `"8"` string was dropped for the default 5. (Code
  and unit test; not called live from this run, which would have filed
  real tasks.)
- New Task and the template form left 0, -1 and 1.5 to the browser's
  bubble, in other words than Edit and Bulk edit. Fixed with Bug 3/4.

## 5. Layout — PASS

Bounding boxes on the live daemon, template-1's and board-1's forms, at
1440×900 and 390×844, light and dark, Advanced options closed and open
(16 combinations): the Template picker lies inside the dialog (x 904–1416
in 880–1440; x 24–366 in 0–390) and above the Title in every one; the
priority box spans the same column; no input, label or button runs past
the dialog's edges, and the document never scrolls sideways. The new
spec pins the 390 px picker case. The left-out link values notice was not
re-shot here: `new-task-form-config.spec.js` already pins it at the top
of the form (11-01 run), and nothing on this branch moves it.

Screenshots (`ui/e2e/screenshot.mjs` against the daemon, `--click "New
task from this template"`, and again with `--click "Advanced options"`;
board-1 with "+ New task" at 1440): `screenshots/before*` on the base
build, `screenshots/after*` on this branch, each 1440 and 390, light and
dark. The sandbox's file reader does not render PNGs and there is no
Gemini key for `LOOK=1`, so layout was checked with the bounding boxes
above rather than by eye.

## Pass/fail summary

| Plan item ("What is NOT proved") | Result |
|---|---|
| 1. A real config layer, precedence | PASS, matches §6.3 (template > repo > board > default) |
| 2. What is filed per opener | PASS for templateId, boardId, hidden defaults, blank title, reads/caps; **FAIL → fixed** for priority (Bug 1) |
| 3. Templates with input fields | Picker and Use…: PASS; template page button **FAIL → fixed** (Bug 2) |
| 4. Priority on every surface | **FAIL → fixed** (Bugs 1, 3, 4, 5); `1e3` disagreement left, filed |
| 5. Layout 1440/390, light/dark, Advanced open/closed | PASS |

## Fixes on this branch

1. `ui/src/components/NewTaskOverlay.jsx`: Task priority opens on and
   follows the picked template's priority until edited; a filing from a
   template always states its priority; a value it cannot file as typed
   is refused under the box in the shared wording and disables Create.
2. `ui/src/wholeNumber.js` `taskPriority` (whole, ≥ 1, ≤
   `Number.MAX_SAFE_INTEGER`) and `PRIORITY_REFUSAL`, used by New Task,
   Edit (`EditTaskForm.jsx`), Bulk edit (`BulkEditOverlay.jsx`) and the
   template form (`TemplateOverlay.jsx`); the template form's `invalid`
   event draws the shared refusal instead of the browser bubble.
3. `pkg/ui`: `writeBodyError` and `UpdateTaskRequest.UnmarshalJSON`
   answer a non-whole priority with `errPriorityNotPositive`.
4. `pkg/mcp` `argWholeInt`: `propose_task` refuses a priority that is not
   a whole number (≤ 2^53) in the same words as 0.
5. `ui/src/components/ProcessesList.jsx`: a template with input fields'
   "New task from this template" opens Use….

Regression tests: `new-task-form-config.spec.js` ("Task priority follows
the picked template and files what it shows", with the 390 px picker
bounding box; "a template with input fields goes through Use…"),
`template-priority.spec.js` (0, -1, 1.5 get the shared refusal),
`TestTasksAdminCapabilityAllowsEditingPriorityViaREST` (1.5, 1e3, `"8"`,
1e20 on POST and PATCH), `TestProposeTaskAcceptsAndValidatesPriority`
(1.5, 1e20, 2^53+2, `"8"`, `true` refused; 1e3 filed as 1000). Bugs 1
and 2 were reproduced on the base build on the live daemon (above)
before the fix; the specs were run on the fixed build only.

Run locally: `npx vitest run` (237 files, 3658 tests), the Playwright
specs `new-task-form-config`, `template-priority`,
`bulk-edit-task-priority`, `new-task-number-boxes`, `edit-task-priority`,
`task-priority-walkthrough`, `built-in-template`, `new-task-link-values`,
`new-task-catalogue-defaults` (28 tests), `go test ./pkg/ui/...
./pkg/mcp/...` — all pass.

## Follow-ups filed

- Decide whether a Task priority written with an exponent (`1e3`) is
  accepted everywhere (as New Task and `propose_task` do) or refused
  everywhere (as Edit, Bulk edit and the template form do).

## Not run

- `propose_task` live through MCP (unit-tested instead, see Bug 5).
- Bulk edit over a selection containing a task the caller may not edit
  (no such task exists for a person; see §4).
- A real agent backend: none configured, nothing dispatched.
