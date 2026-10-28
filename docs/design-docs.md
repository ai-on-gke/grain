# Design docs that plan themselves

When a change to a design doc merges, an agent starts (in bwsalmon/grain
this is turned off for now: see "Turned off in bwsalmon/grain for now"
below). It works out which
requirements are new, changed or removed, and files implementation and test
tasks for them. git is the record of what is built: every implementing
commit carries trailers naming the requirement and the doc revision it
implemented. The merged design pull request is the origin of all of that
work, so following its progress needs nothing but GitHub and git
(docs/work-trees.md):

- open the design pull request and read grain's progress comment on it,
  which lists every task the planner filed, and every task those filed,
  with its state and pull request, and an "n of m done" line;
- or run `git log --grep 'Origin: bwsalmon/grain#<n>'` for what has landed,
  and `git log --grep 'Design-Item: <doc>#<ID>'` for one requirement.

None of this is a grain feature of its own. It is two files on top of path
triggers (`.grain/triggers`, `docs/declarations.md`):

- `.grain/triggers/design-docs.yml` fires on every merge to the default
  branch that touches `docs/design/**`. It files one task per doc folder (or
  single-file doc).
- `.grain/templates/design-planner.yml` is the planner's prompt, filled in
  with the trigger's variables: `{{group}}` (the doc folder or file),
  `{{paths}}`, `{{from_sha}}`, `{{to_sha}}` and `{{branch}}`.

## Turned off in bwsalmon/grain for now

This repository does not currently declare the trigger, so a merge that
touches `docs/design/**` here files no planner task. Trigger files have no
`enabled:` flag (`docs/declarations.md`), so it was turned off by removing
`.grain/triggers/design-docs.yml`. Its body is kept in
`pkg/designdoc/testdata/design-docs.yml`, which the tests fire through the
path-trigger machinery, and the `design-planner` template is still declared.

- **To plan a doc by hand**, file a task with the `design-planner`
  template's title and body (`repo:template:bwsalmon:grain:design-planner`
  on the Templates page, or `.grain/templates/design-planner.yml`), with its
  variables written in: `{{group}}` (the doc folder, e.g.
  `docs/design/sample`, or a single-file doc), `{{paths}}` (the changed
  files under it), `{{from_sha}}` and `{{to_sha}}` (the default branch
  before and after the merge) and `{{branch}}` (the default branch). The
  template declares no `fields:` for them, because a declared field is
  filled in (with its default) before a trigger's variables are, so
  filing it unchanged leaves them as written. Give the task the merged
  design pull request as its origin so its progress comment lands there.
- **To turn the trigger back on**, restore `.grain/triggers/design-docs.yml`:
  revert the commit that removed it, or copy
  `pkg/designdoc/testdata/design-docs.yml` back without its first
  paragraph. The trigger starts watching from the first head it sees after
  the restore, so merges made while it was off are not planned
  retroactively.

`pkg/designdoc` reads a doc's requirement IDs and classifies them from a
branch's commit trailers (`designdoc.History`, `designdoc.Classify`), the
same way the planner's prompt tells it to. Use it in tests and in anything
that wants to show a plan.

## Adopting it in a repository

1. Copy `.grain/templates/design-planner.yml` from bwsalmon/grain into the
   same place in your repository, and
   `pkg/designdoc/testdata/design-docs.yml` (the trigger, turned off here
   for now) to `.grain/triggers/design-docs.yml`. Change the trigger's `paths:` if your docs
   live somewhere other than `docs/design/`.
2. Make sure grain reads the repository's declarations (an operator turns
   them on in the repository's settings or with `grain repo`). Run
   `grain declarations check .` to see the trigger the way grain will.
3. Write docs following the conventions below.

The trigger starts watching a branch at the first head it sees after it is
synced. Merging the trigger therefore plans nothing, and the first merge
that edits a doc after that does.

`auto-approve: false` means each planner task waits for a human to approve
it. The tasks the planner proposes are filed the way any proposal is: they
need approval unless the planner's own task could approve them.

## Doc conventions

A design doc is either a folder under `docs/design/` whose doc is its
`README.md` (other files in the folder are supporting material), or a
single file such as `docs/design/foo.md`.

Each requirement heading carries a stable ID at the end of its text:

```md
### Retag latest on promotion {#D-7}
```

An ID is a capitalised prefix, a dash and a number (`D-7`, `API-12`). It
is never reused, not even after the requirement is dropped. A heading that
is not a requirement (background, non-goals, open questions) carries none.
If a doc has requirement headings without IDs, the planner gives each the
next free ID and writes them into the doc in its own pull request.

Keep one requirement per heading. The planner notices a change to a
requirement by comparing that heading's section, from the heading to the
next one of the same or a higher level, between revisions.

## What records progress

There is no ledger file. Three things record a doc's progress, and grain
writes or checks all of them:

- **Commit trailers.** The commit that implements a requirement carries

  ```
  Design-Item: docs/design/foo/README.md#D-7
  Design-Rev: <sha of the doc revision it implemented>
  Origin: bwsalmon/grain#2710
  Filed-by: task 1234
  ```

  `Design-Item:` and `Design-Rev:` are what the planner reads to tell done
  from changed. A commit that takes a removed requirement back out carries
  `Design-Removed: <doc>#<ID>` instead of `Design-Item:`. `Origin:` and
  `Filed-by:` are on every task's work, not only design work: grain adds
  them to the merge commit when it merges a task's pull request, and the
  agent is asked to put them on its own commits too (docs/work-trees.md).
- **The progress comment** grain keeps on the design pull request: one
  comment, edited in place, listing the whole tree of tasks.
- **Each task's pull request description**, whose grain-written section
  names the task that filed it, the origin, and the follow-up tasks it
  filed.

Task execution state (queued, running, retries) is never written to git.

When the planner assigns IDs, its pull request edits the doc itself, so
that merge starts the planner once more. That run finds every requirement
open or unchanged (the `{#ID}` marker is ignored when comparing) and ends
with `nothing_needed`, without opening a pull request.

## What the planner does

The planner reads the doc at `to_sha`, the diff since `from_sha`, the
`Design-Item:` / `Design-Rev:` / `Design-Removed:` trailers on the branch,
and the open tasks that reference the doc (each task body carries the
`Design-Rev:` it was filed against). It then sorts every ID into one class
and acts on it. "Changed" compares the item's section at the revision the
work was done or filed against with the section at `to_sha`.

| Class                                  | Action                                                                  |
|----------------------------------------|-------------------------------------------------------------------------|
| new (in the doc, nothing built or open)| an implementation task, and a test task that depends on it              |
| changed, still open                    | replacement tasks that `supersede` the open ones                        |
| changed, done                          | a delta task covering only what changed                                 |
| removed, open                          | nothing filed: it asks a human to close the open tasks                  |
| removed, done                          | a removal task, whose commit carries `Design-Removed:`                  |
| open or done, unchanged                | nothing                                                                 |

If it filed anything, it finally files a "verify doc against
implementation" task that depends on everything filed in that run. Every
task body names the doc, the item ID, the `Design-Rev:` it was planned
from, and the trailers to put on the implementing commit. The template is
the full procedure.

## Scope

The planner covers the repository the docs live in, and nothing else. It
does not write design docs.

For the end-to-end architecture overview of how Grain itself holds together
(as opposed to how design-doc triggers plan work), see
[`architecture.md`](architecture.md).
