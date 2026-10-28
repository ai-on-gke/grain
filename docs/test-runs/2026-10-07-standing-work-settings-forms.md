# Standing work and settings forms: walkthrough, 2026-10-07

Plan: `docs/test-plans/2026-10-07.md` §5. Task 2424 (filed by 2411), branch
`grain/task-2424`, base `candidate`.

## Environment

| | |
| --- | --- |
| Tested commit | `6634d5c81` (candidate tip, "Merge pull request #3147"), then this branch's fixes on top |
| Features confirmed in base | `9108513bd`, `bd6989e69`, `ade66e4fd`, `51cde04b1`, `1cd29da15`, `78501f5d0`, `54ef4c961`, `d738ae75b`: `git branch -r --contains` lists `origin/candidate` for each |
| Go / Node | go1.26.6 linux/amd64, Node v20.19.0 |
| Browser | `@playwright/test` (ui/package-lock.json), Chromium headless shell 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Server | `grain demo -addr 127.0.0.1:18080 -open=false` on its throwaway SQLite store, built from this tree after `npm ci && npm run build` (candidate code, before the fixes). The fixes were checked by the e2e suite's own per-worker `grain demo` on the rebuilt bundle. |
| Identity | `localStorage["grain.username"]` from an init script (`X-Grain-User`): `sysadmin` (system admin), and the team admins below |
| Model / agents | None. The demo runs no agents. |

The exploration used throwaway Playwright scripts in `ui/.tmp-walk/`, which were
excluded through `.git/info/exclude` and not committed. They logged page errors,
every HTTP status of 400 or more, and every `confirm()`.

## Stubs

Nothing mocked grain's API or React. The only intercepted route:

- `page.route("**/api/relay-settings")` answering **500** `relay store unreachable (forced)`.
  The plan asks for this failure. The real answer is the demo's 501.

The 501 was the real server's answer. GitHub was not reached: the demo's
`/api/repos/acme/widgets/github-branches` answers 404, which was logged and left alone.

## Steps, tables and outcomes

### 1. Preview against outcome

Two teams were made through `POST /api/teams`:

- `nomodel*`, with no default model;
- `withmodel*`, with `defaultModel: "team-default-model"`.

The deployment's own default model is `gemini-2.5-pro`. There were six template cases:

| Case | Team | Template sets |
| --- | --- | --- |
| C1 | no default model | nothing |
| C2 | with default model | nothing |
| C3 | no default model | model `claude-opus-5-5` |
| C4 | with default model | repo `acme/widgets` |
| C5 | no default model | network `none` |
| C6 | with default model | nothing-needed `ack`, model, repo, network `none` |

For each case the script did four things:

- read the "What this files" rows (`dt`/`dd`) on the Use… form, filed the task, and
  read it back through `/api/tasks/<id>`;
- opened a schedule on the template, read its rows, pressed **Run now**, and read
  back the new task;
- built a suite in the UI and read its rows;
- opened the suite row's **Run…** and read its rows.

| Case | Form | Model row | Repo row | Network | Nothing needed | Stored (model / repo / network / nn / team) | Match |
| --- | --- | --- | --- | --- | --- | --- | --- |
| C1 | Use… | the team's default model | the deployment's default repo | no override | close | "" / acme/widgets / "" / "" / nomodel | yes, but see F4 |
| C1 | schedule (Run now) | the team's default model | acme/widgets | no override | close | "" / acme/widgets / "" / "" / nomodel | yes, but see F4 |
| C1 | suite | the team's default model | the repo this run targets | no override | close | (suite saved, team nomodel) | yes |
| C1 | run-suite | the team's default model | acme/widgets | no override | close | no run recorded (see "Not completed") | n/a |
| C2 | Use… | the team's default model | the deployment's default repo | no override | close | "" / acme/widgets / "" / "" / withmodel | yes |
| C2 | schedule (Run now) | the team's default model | acme/widgets | no override | close | "" / acme/widgets / "" / "" / withmodel | yes |
| C2 | suite | the team's default model | the repo this run targets | no override | close | (suite saved, team withmodel) | yes |
| C3 | Use… | n/a | | | | `POST /api/tasks` 400: `agentBackendId "claude-opus-5-5" is not a configured model or connection` | see F5 |

Unset fields were stored empty and resolved later, and every row named them that way.
The demo stores the resolved repo, and it matched. Network and nothing-needed were
worded the way their pickers word them.

Two outcomes need more than a "yes":

- **The Model row is ambiguous (F4).** It reads "the team's default model" for C1, whose
  team has none, and for C2, whose team has one. The stored task carries no model in
  either case.
- **C3 stopped the run (F5).** A template saved with a model that this deployment does
  not configure was accepted on save. Filing from it was then refused.

### 2. Guard sweep

Each editor was edited, then left by one exit. **asks** means `confirm()` fired.
**keeps** means the edit was still shown or stored afterwards. **LOSES** means it was
gone without a word.

Team switch and sign-out are not reachable from inside a modal pane. The team picker
is behind the pane's backdrop, so a click there is a backdrop click and takes the same
path as Escape. Log out lives in the Settings overlay, which cannot open over another
pane. Those cells read "unreachable".

| Editor | Escape | Back link | Browser Back | Team switch | Sign-out |
| --- | --- | --- | --- | --- | --- |
| Board → Columns | **LOSES → asks (fixed, F3)** | n/a (centred dialog, no back link) | **LOSES → asks (fixed)** | unreachable (backdrop = Escape) | unreachable |
| Task pane → Edit (title, …) | **LOSES** (F6) | **LOSES** | **LOSES** | unreachable | unreachable |
| Task pane → Edit fields (custom fields) | **LOSES** (F6) | **LOSES** | **LOSES** | unreachable | unreachable |
| Repo page: prompt extension (repo config, `/repos/…` and `/teams/<t>/repos/…`) | loses (see note) | keeps | keeps | keeps | unreachable |
| Team settings → Workflow & Tasks (follow-up limits) | keeps (saves on blur) | keeps | keeps | unreachable | unreachable |
| Admin → General (Environment name) | keeps (saves on blur) | keeps | keeps | unreachable | unreachable |
| Team settings → Connections → Edit | asks (`1cd29da15`; e2e re-run green) | asks | asks | unreachable | unreachable |
| Schedules / templates / suites | asks (existing `useUnsavedEdits`; e2e green) | asks | asks | unreachable | unreachable |

Note on the repo prompt extension: Escape inside that text box puts it back to its
stored value. That is the field's own "cancel this edit" key, not the page being left,
so it is recorded here and not filed.

Not driven in this sweep, for time:

- memory documents (`MemoryPanel` is already guarded through `TeamSettingsOverlay`'s
  `memoryUnsaved`);
- merge rules;
- the Integrations capability fields.

### 3. Cross-team delete refusal

The setup, made through the API:

- team A (`ta*`), admins `anna` and `both`;
- team B (`tb*`), admins `bert` and `both`;
- no member of either team is a system admin.

`anna` made a template and an A suite on it. `both` can read both teams, and built a
B suite on A's template (201). `bert` was refused the same suite: "unknown template".

Then, as `anna`, in the UI: `/templates/<id>` → Delete → accept the confirm. The banner
read:

> template is used by 2 suite(s) ("A-visible-suite-…", 1 in teams you cannot see); remove it from those first

No B title appeared anywhere on the page.

**25 named dependents at 375 px (F1).** There were 25 A suites with long names on
another template. The refusal (1,551 characters) was 797 px tall in an 800 px window.
Its top was at −11 px, so the line saying what was refused was cut off. It covered
every control on the page until the banner timed out. This is fixed, and after the fix
the alert stays on screen and scrolls.

### 4. Tab strips

At 700 px and 375 px, the Team settings strip (8 tabs) and the Integrations capability
strip (26 tabs) were each walked with:

- ArrowRight to the end, then ArrowLeft back;
- End, Home, End, ArrowLeft, Home, ArrowRight.

That is 20 and 56 presses. After every press the focused tab was wholly inside
`.MuiTabs-scroller`, with no misses. These walks are now e2e tests.

### 5. Suite mode

The new suite started at Max passes 7. The mode changes and edits were:

1. → count, which showed 1; set it to 3.
2. → until clean, which showed 7; set it to 9.
3. → count, which showed 3; set it to 4.
4. → until clean, which showed 9; set it to 6.

Save sent `{"mode":"until_clean","maxPasses":6}`, and the store held `until_clean`, 6.
After a reload the form showed "until clean", 6.

Editing the saved suite went count (5) → until (8) → count, which showed 5. Save stored
`count`, 5, and the reload showed count 5 and "5 passes of these tasks".

The stored and shown values agreed every time. No finding.

### 6. Access tab

With the real 501, `/admin/access` showed no `role=alert`, and General & Deployment
after it showed none either. With a forced 500, the banner read "relay store unreachable
(forced)" on Access and stayed up on the next tab until it timed out, as the banner does
for any error. Both behaviours are correct.

### 7. Team picker

This was done keyboard only. Tab reached the picker on the 5th press. Then:

- Enter opened the list, Tab moved to New Team, and Enter opened "create new team".
- The URL stayed `/teams/default`, and `grain.selectedTeam` in `localStorage` was
  unchanged.
- **Escape closed the dialog and left focus on `<body>` (F2).** This is fixed: focus
  now returns to the picker.

## Findings

| # | Finding | Deterministic reproduction | Status |
| --- | --- | --- | --- |
| F1 | Error banner taller than the screen | Make a template and 25 suites on it (long names). At 375×800, open `/templates/<id>`, press Delete and accept. The alert's top is at −11 px and the "template is used by 25 suite(s)" line is off screen. Every control is covered. | **Fixed**: `ErrorBanner` caps the message at `40vh` and lets it scroll. |
| F2 | Escape from New Team loses focus | On `/teams/default`: Tab to the Team picker, press Enter, Tab to New Team, press Enter, then Escape. `document.activeElement` is `<body>`. | **Fixed**: the New Team button focuses the picker before opening the dialog, so the dialog restores focus there. |
| F3 | Board columns drop an edit | `/boards/board-1` → Columns. Change "Column 1 title", then press Escape (or browser Back). It closes with no prompt and the edit is gone. | **Fixed**: `BoardColumnsOverlay` passes `Overlay` an `isDirty` (draft vs. what it opened on). |
| F4 | The Model row does not say which default applies | Team with no `defaultModel` (deployment default `gemini-2.5-pro`). Template with no model. The Use…, schedule and suite previews all say "the team's default model", the same as for a team that has one. The stored task has `model: ""`. | **Proposed as a follow-up.** The panel needs to know whether the team has a default, which comes from the composition API or the team config, so it is not a wording-only change. |
| F5 | Unknown model accepted on templates | `POST /api/templates {"model":"claude-opus-5-5", …}` gives 201. Use… → Create task then gives 400 `agentBackendId "claude-opus-5-5" is not a configured model or connection`. | **Noted, not filed.** It is server validation outside §5's preview scope, and the refusal is explicit when filing. |
| F6 | Task pane edits drop silently | `/tasks/2` → Edit, change Title, press Escape (or "← Back", or browser Back). The pane closes, nothing is asked, and the task is unchanged. The same happens with `/tasks/1` → Edit fields → Area. | **Proposed as a follow-up.** `EditTaskForm` and `CustomFieldsSection` must lift their draft state to `DetailOverlay` for an `isDirty`, which is more than this task's scope. |

Overlap check:

- None of F1–F6 is in `docs/test-runs/2026-10-06-recurring-composed-ui.md` or
  `2026-10-06-deployment-operations.md`.
- The sandbox has no task-list tool, so open tasks could not be searched. The
  follow-ups say so.

## Not completed

- **Run-suite preview against outcome.** **Run…** → Run on a suite gave no suite run
  in `/api/suite-runs`, so there was nothing to compare. The demo runs no orchestrator,
  and the 2026-10-06 report drove suite runs through a store helper. Its preview rows
  matched the suite form's, with the repo as the run's repo.
- **Cases C3–C6 on the schedule and suite forms.** C3's refusal (F5) stopped the script,
  so these cases were only shown on Use… through C3.
- **Guard sweep: memory documents, merge rules and capability fields.**

## Fixes

| Fix | Unit test (fails without the fix) | e2e (`deployment-operations-navigation.spec.js`) |
| --- | --- | --- |
| F1 `ErrorBanner.jsx` | `ErrorBanner.test.jsx` "scrolls a long message inside a box shorter than the window" | "keeps a refusal naming 25 dependents on a phone's screen" |
| F2 `Sidebar.jsx` | `Sidebar.test.jsx` "hands the New Team dialog the picker to return focus to" | "returns focus to the team picker when New Team is dismissed" |
| F3 `BoardColumnsOverlay.jsx` | `BoardColumnsOverlay.test.jsx` "asks before Escape throws an edit away, and stays when told to" (and "leaves an untouched editor…") | "asks before leaving an edited board layout" |
| (coverage) | | Team settings and Integrations strips at 700 and 375 px, with Arrow keys, Home and End; "still reports a relay settings read that fails" |

How the "fails without the fix" claims were checked:

- **Unit tests:** run with the component change stashed. Each named case failed.
- **e2e:** each failing behaviour was first reproduced against the unfixed candidate
  build on `:18080` by the scratch scripts. The results were:
  - focus on `BODY` after Escape;
  - no `confirm()` on Columns Escape or Back;
  - the alert's top at −11 px.

  The new tests pass on the rebuilt bundle, 17 of 17 twice in a row.

`npm run format` was run, and no Go was changed.

## CI verdict

Grain CI full suite (`wait_for_checks` without `jobs`), run `3015eec614885ab4` on
`3d18197f0`: **success**. Every job passed: frontend, go, installer, lint, package cache,
postgres, terraform and ui e2e. No known flake recurred.
