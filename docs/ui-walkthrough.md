# The release UI walkthrough suite

grain/task-19 asked for the UI to be walked through thoroughly -- not
read, not asserted against in jsdom, but brought up in a browser and
driven through its own controls, section by section, by an agent whose
job is to notice what is wrong and either fix it or write it down. That
was done once, by hand, as six tasks filed on 14 September 2026; their
reports are in `docs/test-runs/`.

Doing it once is the part that has no value. A walkthrough is worth
having because it is repeated: the interesting question is not "did the
UI work in September" but "does this release's UI still work", and that
question is asked at every release. So the six chunks are written down
here as declarations -- six templates and one suite in this
repository's own `.grain` directory (`docs/declarations.md`) -- and the
release ritual is to run the suite.

## The chunks

Eight, split by workflow rather than by screen, so that each one is a
coherent sitting for one agent rather than a list of components. Each is
a template in `.grain/templates/`:

| File | What it walks |
| --- | --- |
| `ui-walkthrough-tasks.yml` | Inbox, the New task form, task lists and narrowing, task detail and its controls (pause, force merge, Run now), bulk edit and manual mode |
| `ui-walkthrough-boards.yml` | The Boards index and every visualization: list, kanban, timeline, tree, dashboard, report |
| `ui-walkthrough-repos.yml` | Repos, the repo page's tabs and Settings → Repositories, the merge queue and base-gate warnings, comparing branches, releases and release trains |
| `ui-walkthrough-automation.yml` | Processes: schedules, templates, suites, suite runs and triggers -- including declared rows |
| `ui-walkthrough-settings.yml` | Settings' layout, filter and save model; Preferences, Access, General, Config repo, Models, Agents, Memory and Tasks |
| `ui-walkthrough-infrastructure.yml` | Settings' Connections and Infrastructure groups: Git, Capabilities, Legacy capabilities, Executors, Engine, CI, Sandboxes, Caches |
| `ui-walkthrough-operations.yml` | System and its tabs (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Upgrade, Power), the CI page, the alerts bell and the standing banners |
| `ui-walkthrough-shell.yml` | The shared shell -- page header rail, phone bar and drawer, nav rail, banners -- at desktop/tablet/phone widths, and the live session surfaces |

Every one of them carries the same walkthrough protocol in its body:
build and run this repository's real `grain demo` server over a
throwaway store, drive its built frontend from a real Playwright
browser, stub only an external boundary and say which, file a dated
report under `docs/test-runs/`, and fix what is safe to fix in the task
itself rather than only reporting it.

Outside the walkthrough, every change to `ui/` follows the standing "UI
changes" rule in `.grain/settings.yml`: screenshots at desktop and phone
widths, in light and dark, taken and looked at before the change counts as
done. `make ui-screenshots` takes them; see `docs/ui-screenshots.md`.

## Running it at release time

`.grain/suites/release-ui-walkthrough.yml` runs all eight in one pass:

```yaml
name: Release UI walkthrough
templates: [ui-walkthrough-tasks, ui-walkthrough-boards, ...]
mode: count
count: 1
```

One pass, because the eight chunks are eight different questions rather than
one question worth asking repeatedly -- `until-clean` would re-run the
whole sweep until a pass found nothing, which for a walkthrough means
re-walking seven clean areas for the sake of the eighth. Run it again by
hand if the fixes it lands are worth a second look.

`require-approval: false` and `auto-merge: true` are the suite's own
disposition, and they beat whatever each template says: the tasks queue
themselves and their fixes land when their checks pass. That is what
makes this worth starting on a release branch and coming back to.

To run it: Suites in grain's UI, the **Release UI walkthrough** row,
Run -- and choose the repository and the base branch there. The suite
deliberately names neither. A suite run stamps its own base onto every
task of every pass (`model.SuiteRun.Base`), which is precisely what a
release needs: the same eight walkthroughs, pointed at the release branch
this time rather than at `main`, with each task's pull request based off
that branch and the next task seeing the previous one's merge.

## What an operator has to have set up

Declarations carry no grants, on purpose (`pkg/model/repo_declaration.go`
has the argument): a walkthrough task holds whatever this deployment
already gives tasks in this repository, which is the union of
`Config.DefaultCapabilities` and `RepoConfig.DefaultCapabilities`
(`orchestrator.defaultTaskGrants`). Two things are worth checking before
a release rather than during one:

- **Declaration syncing has to be on for this repository**
  (`RepoConfig.Declarations`), or none of these files are rows at all
  and there is no suite to run. The repository page says whether it is
  on, and when the files were last read. This one really does live
  outside this repository -- it is a deployment-level switch in
  `grain/config`, and `.grain/settings.yml` is refused if it tries to
  set it.

  **It was off for `bwsalmon/grain` when this was written and was
  switched on at 18:52 UTC on 20 Sep 2026.** Check the repository page
  for what it says today; if it is on, the eight templates and the suite
  are rows and the walkthrough has something to click.

  Switching it on used to be more than a one-click errand, because the
  first sync handed the whole `repo_config` row over to
  `.grain/settings.yml` -- and when it was switched on, that is exactly
  what it did (grain/task-460, 464). Since grain/task-340 it is a
  one-click errand: a settings declaration writes two columns of its own
  and cannot reach the row an operator wrote, so a first sync takes
  nothing away whatever the file does or does not say.
- **The walkthrough wants a Gemini-backed model**, and a declared
  template cannot ask for the `gemini-key` capability. So it has to be a
  default capability for tasks in this repository, and that list is the
  live `repo_config` row -- which no task sandbox can read, and which
  turning declarations on no longer changes. It is a cloud credential
  handed to every task in this repository rather than to the eight, so it
  is an operator's call in grain's own settings, not a file anybody
  reconciles here. Each body is written to fall back rather than fail if
  the key is absent, but a walkthrough run without it is not the
  walkthrough that was designed.

Where that list lives is the whole of why this section is shorter than
it was. It used to say that the capabilities were reviewed in a pull
request like everything else here, and that a name could therefore leave
by being deleted from a file nobody connected to this document. Both
were true, and the second is why a repository no longer writes that list
at all.

## Changing a chunk

Edit the template file and open a pull request: that is the whole point
of these being declarations rather than rows. The stem is the identity
-- a suite names its templates by file stem and the row's id is derived
from it -- so rewording `name:` is free and renaming the file makes a
new template that the suite no longer names. `grain declarations check`
reads a working tree and refuses a file that does not parse, which is
worth running before pushing one.

## Keeping the chunks current

The nightly new-features plan (`.grain/templates/nightly-features-test-plan.yml`)
files a one-off walkthrough for each new surface but never edits these
templates, so a surface nobody writes into a chunk is walked once and
then drops out of coverage. Between 24 September, when Teams was added,
and 11 October about fifty components landed that no chunk named.

So a weekly schedule revisits them: `.grain/schedules/ui-walkthrough-review.yml`
runs `.grain/templates/ui-walkthrough-review.yml` every Monday at 05:00
UTC on `main`, where these templates are maintained. It inventories the
UI in `ui/src`, reads the week's `docs/test-plans/` and `docs/test-runs/`,
and compares both against the chunks. It then either opens one pull
request that adds, moves, drops or splits steps, with a dated coverage
review entry in its description, or closes with nothing needed. The
nightly suite runs on `main` and walks staging, which runs the current
release candidate, so the entry names any surface not yet in the newest
`v*-rc.*` tag: a walkthrough that cannot find it on staging yet has not
found a regression. Like any change under `.grain/`, that pull request
waits for a human Submit.
