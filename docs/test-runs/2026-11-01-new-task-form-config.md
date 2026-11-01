# Test run — configurable New Task form (`settings/new-task-form.yml`), 2026-11-01

Plan: `docs/test-plans/2026-11-01.md` §2. Scope: PR #304 (`71ca6b37`
server, `4b75702c` UI, `a7cb221b` agent-filing fix, `e12bb80b`), PR #224
(catalogue-refused capability defaults) and PR #233 (`/new?` link values).
Filed by task 2084; walked by task 2217 (attempt 2 — attempt 1 was paused
on a model usage limit before it pushed anything).

## Environment

- Tree: `main` at `41d9179f`, then this branch's fix on top.
  `cd ui && npm ci && npm run build`, `go build -o ~/bin/grain ./cmd/grain`.
- **A real `grain daemon`** with its own data dir and an internal
  grain/config:

  ```
  grain daemon -data-dir /var/tmp/gd/data -ui-addr 127.0.0.1:4310 \
    -sandbox-port-base=0 -host-sandboxes -sandbox-dir /var/tmp/gd/sb \
    -github-host 127.0.0.1:1 -github-insecure-http \
    -repo acme/widgets -model-backend claude
  ```

  The grain/config working tree is `<data-dir>/state-repo`, with no remote.
  The daemon's state sync (`staterepo.Apply`, every 30 s) imports a commit
  whose HEAD differs from the one it last loaded. Committing there as
  `operator` is the end state a merged config PR reaches.
- Bootstrap through the API, with `"directConfigWrites": true` in
  `<data-dir>/state-repo.json` only while it ran, then removed: the
  template `template-1` ("bugfix", bound to `acme/widgets`), the board
  `board-1` ("Ops"), target repo `acme/gadgets`, a dummy GitHub token
  (`PUT /api/github-tokens/default`; it is never used, it only lets a
  person's filing pass the reach check), `externalAgentApi: true`, and an
  external agent credential `walk` (`grain/task-admin` preset on
  `acme/widgets`, `acme/gadgets`).
- **Stubbed boundaries:** the model backend (none configured, so nothing
  dispatches; filed tasks wait) and GitHub (`-github-host 127.0.0.1:1`,
  never reached). Nothing else was stubbed.
- Browser: Playwright Chromium (`/var/cache/playwright/chromium-1234`),
  headless, 1440×900 and 390×844, light and dark, against the daemon with
  the access code as a bearer header. `grain demo` (via the e2e fixture
  and `make ui-screenshots`) for the Playwright specs.

### The committed form (`settings/new-task-form.yml`, four layers)

```yaml
default:
  fields:
    networkAccess: {visibility: shown, order: 15, label: "Network (default layer)", default: open}
    model: {visibility: shown, label: "Model (default layer)", default: "default-model"}
    autoMerge: {visibility: hidden, default: true}
    approved: {visibility: hidden, default: true}
    atFront: {visibility: hidden, default: true}
    dependsOn: {visibility: more}
  capabilities:
    defaults: [read-issues]
boards:
  board-1:
    fields:
      approved: {visibility: hidden, default: false}   # added in step 6
      networkAccess: {label: "Network (board layer)", default: none}
repos:
  acme/gadgets:
    fields:
      networkAccess: {visibility: more, label: "Network (gadgets repo layer)", default: ""}
      model: {label: "Model (gadgets repo layer)", default: "gadgets-model"}
    capabilities:
      defaults: [read-issues, self-debug]              # self-debug: catalogue denies it here
templates:
  template-1:
    fields:
      networkAccess: {visibility: shown, label: "Network (template layer)", default: none}
      model: {visibility: hidden, default: "template-model"}
```

With it, `settings/capabilities.yml` held a two-entry catalogue:
`read-issues` for every repo, and `self-debug` restricted to `acme/widgets`.

## Steps and observations

### 1. Config round trip — PASS

The commit `operator: new task form` was applied on the next 30 s tick.
`GET /api/config` then carried `newTaskForm` with all four layers, exactly
as committed. The daemon's next export (`Update grain state`) rewrote
`settings/new-task-form.yml` in its canonical form: keys sorted, the
file's header comment added, and the same values (diffed). The form in the
browser changed with no restart.

### 2. Refused files — PASS for the three shape refusals; by design for the catalogue one

Each variant was committed on top of the good form and given one tick.
Every refusal is reported in the same two places: in the daemon log as
`staterepo: refusing to apply the settings that just arrived …` and
`grain: state sync failed: …`, and in `GET /api/state-repo` `.error` (the
State pane). In each case `/api/config` kept the previous form.

| Variant | Text | Live form kept |
|---|---|---|
| unknown key (`title: {visible: shown}`) | `parsing settings/new-task-form.yml: yaml: unmarshal errors: line 3: field visible not found in type model.NewTaskFormField` | yes |
| hidden title | `settings/new-task-form.yml: default.fields.title: the title is required and cannot be hidden` | yes |
| hidden repo, no default | `settings/new-task-form.yml: default.fields.repo: a hidden repo needs a default` | yes |
| `repos.acme/gadgets.capabilities.defaults: [self-debug]` (catalogue grants it only to `acme/widgets`) | **applied** (no refusal) | no, replaced |

The catalogue judgement (`checkNewTaskForm`) runs in `staterepo.Check`,
which is config-PR validation (`validate_pr.go`). It does not run in
`Apply`. Design §6.4 puts it there on purpose: `Check` judges every config
PR, and at runtime the picker filters out what the catalogue denies. The
runtime half held. On `acme/gadgets` the picker showed the `Self debug`
chip, but the filing sent `capabilities: ["read-issues"]` (#224 dropped
the refused default). A direct `POST /api/tasks` naming `self-debug` on
`acme/gadgets` answered 400: `capability self-debug is not granted on
acme/gadgets by settings/capabilities.yml (repos: acme/widgets)`. So a
commit that skips PR validation can put a denied default *in the form*,
but it cannot get it *onto a task*. Not filed: the design intends this.

### 3. Layer precedence in the browser — FAIL before this branch (board, template); PASS after (board)

- Default layer: at both widths and in both themes, `/new` showed
  "Network (default layer)" at order 15, between the title and the
  description. Depends on sat under Advanced options. Auto-merge, Queue
  immediately and Add to front were not drawn.
- Repo layer: switching to `acme/gadgets` moved the field under Advanced
  options as "Network (gadgets repo layer)", and pre-added `Read issues`
  and `Self debug`, the gadgets layer's capability defaults.
- **Bug 1: the board layer never reached the browser.** New Task opened
  with "+ New task" on `/boards/board-1` still read "Network (default
  layer)", and the filing carried no `boardId`. `App.jsx` never passed
  `boardId` to `NewTaskOverlay`. The prop existed and defaulted to `""`,
  and nothing set it. So no `boards:` layer applied in the UI, and the
  server's board-layer hidden defaults (`applyHiddenFormDefaults`, which
  reads `req.BoardID`) never fired for a browser filing. **Fixed here**;
  see "Bugs".
  After the fix, on the same daemon at 1440 and 390, light and dark, the
  board form reads "Network (board layer)". Filing it sent
  `boardId: "board-1"` and `approved: false`, the board layer's hidden
  default, while the default layer's `autoMerge: true` still applied.
  A `/new` filing on `acme/gadgets` in the same session sent
  `approved: true`, the default layer's.
- **Template layer: not reachable in the browser.** `NewTaskOverlay`
  takes a `templateId` prop that nothing sets, and no UI path files a
  task from a template: there is no `templateId` on any UI filing. So
  "switch template, with and without touching the field" cannot be done
  in a browser at all. The layer does work through the API: a person's
  `POST /api/tasks` with `templateId: template-1` and `model: "mine"`
  filed `model: "template-model"`, the template layer's hidden default.
  Filed as a follow-up: it needs a decision on where a template's New
  Task opens, which is more than a scoped fix.
- Touch-then-switch: covered over `grain demo` by the new spec. With
  `promptExtension` shown, switching None → `acme/widgets` → None moves
  the value between the default layer's and the widgets layer's defaults.
  A typed value stays put through a switch, and the layer's label still
  changes. PASS.

### 4. Hidden defaults: a person's filing vs an agent's — PASS (no security bug)

Each body sent `autoMerge: false, approved: false, atFront: false` to
`acme/widgets`.

| Caller | Result |
|---|---|
| person (access code) | 201, task 4: `autoMerge: true`, `approved: true`, the hidden defaults over the body |
| agent (`gma_` external token, `grain/task-admin`) | 201, task 2: `autoMerge: false`, no `approved`, as its body said |
| person, `templateId: template-1`, `model: "mine"` | 201, task 5: `model: "template-model"`, `autoMerge`/`approved` true |
| agent, same body | 201, tasks 3 and 6: `model: "mine"`, `autoMerge: false` |
| agent, `{"title":…}{"approved":true}` (trailing JSON) | 403, refused before the handler |

The agent filing carried none of the hidden defaults, so the
`a7cb221b` fix holds on a live daemon. The only call site of
`applyHiddenFormDefaults` is `handleCreateTask`, behind the
`PrincipalAgent` check. MCP `propose_task` and from-template filings never
reach it.

`priority: 8` and `keepRunning` as hidden fields: **N/A by design.**
They, and Reviewers, are not form-config fields (`NewTaskFormFieldIDs`;
NewTaskOverlay: "Reviewers, Keep running and Task priority are not
configurable and keep their own places"). A file naming them is refused
as an unknown field. Noted, not filed.

### 5. `/new?` links naming hidden or "more" fields — FAIL before this branch; PASS after

`/new?` links carry custom-field values only (`field.<name>`), so the
form field they meet is `customFields`.

- **Bug 2:** the "This link asked for values this form has left out"
  notice lived inside the `customFields` slot. With `customFields: more`
  it sat in the collapsed Advanced options, unseen until somebody opened
  it. With `customFields: hidden` it was never drawn, and the link's
  values were still filed into fields nobody could see. **Fixed here.**
  When the custom fields are not shown, the notice is drawn at the top of
  the form. A hidden `customFields` files the declared defaults and lists
  the link's values as left out ("Priority: “high” is in a field this form
  hides").

### 6. Phone width — PASS

At 390 px, light and dark, over the demo (new spec):

- Thirteen fields moved to `more`: Advanced options opens, and Base
  branch, Auto-merge, Network access, Prompt extension and Queue
  immediately each lie inside the dialog's horizontal bounds.
- Every optional field hidden: Advanced options is left out, and Title
  and Repos are drawn within 390 px.

On the live daemon at 390 px, the board form's network field spans
x 24–366 inside the 390 px dialog.

## Pass/fail summary

| Plan item ("What is NOT proved") | Result |
|---|---|
| 1. Config round trip, refusals, previous form kept | PASS (shape refusals); catalogue denial applied on a direct commit, by design (§6.4), with the runtime backstop verified |
| 2. Layer precedence, repo/template switch ± touch | Board layer: **FAIL → fixed**. Repo: PASS. Template: not reachable in the UI, **filed** |
| 3. Hidden defaults, person vs agent | PASS: the agent filing carries none; priority/keepRunning are not configurable by design |
| 4. `/new?` links vs hidden/more fields | **FAIL → fixed** |
| 5. 390 px, Advanced options full and empty | PASS |

**Security:** no agent filing picked up a hidden default.

## Bugs fixed on this branch

1. `ui/src/App.jsx`: record the board whose page "+ New task" was
   pressed on (`newTaskBoard`) and pass it as `boardId`; every other
   opener clears it.
2. `ui/src/components/NewTaskOverlay.jsx`, `CustomFields.jsx`: the link
   notice moves to the top of the form when the custom fields are not
   shown. `hiddenLinkValues` names a hidden field's link values, and a
   hidden `customFields` files declared defaults.

Regression tests, in `ui/e2e/new-task-form-config.spec.js`:

- the board layer and its `boardId`
- a repo switch, with and without touching the field
- the link notice for `more` and `hidden` custom fields
- 390 px, light and dark, with Advanced options full and empty

The board and link specs fail on the old build and pass on this one.
`CustomFields.test.jsx` gains a `hiddenLinkValues` case.

## Filed

- The New Task form has no template context, so `templates:` layers of
  `settings/new-task-form.yml` never apply in a browser.

## Screenshots

`make ui-screenshots PAGES=/new,/boards/board-1 OUT=screenshots/after`
over `grain demo` (1440 and 390, light and dark). Live-daemon shots of
the board form were taken at the same widths and themes. The sandbox's
file reader cannot render PNGs, so layout was asserted with bounding
boxes in the spec rather than by eye.
