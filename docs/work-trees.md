# Work trees on GitHub

Work grows in trees. A design pull request merges and the planner files
implementation tasks; an implementation task files a follow-up; a chat files
three tasks, and one of those files two more. All of it can be followed from
GitHub and git alone, without opening grain.

## Origin and parent

Every task has two provenance fields:

- **parent**: the task whose run filed it with `propose_task` (the existing
  `proposed-by` link, `Task.Parent()`). A task a person or a trigger filed
  has none.
- **origin** (`Task.WorkOrigin`, column `task.origin_ref`): where the whole
  tree began. It is one of:
  - a GitHub pull request or issue, stored as `owner/repo#N`. A task a merge
    trigger files (the design-doc planner, for example) has the pull request
    that merged as its origin. A task filed against an issue (`Task.Issue`)
    has that issue.
  - a grain task, stored as `task:<id>`. A task filed from a chat has the
    chat as its origin.

A task filed with `propose_task` inherits its parent's origin, at any depth.
A parent with no origin of its own passes on its own parent's, and so on up;
only a task nobody filed is the root of its tree (`treeOrigin` in
`pkg/model/work_origin_store.go`). A task origin that is named rather than
inherited is resolved the same way, so naming a task part way down a tree
(a run naming itself in `propose_task`'s `origin`) files the work at that
tree's root. A
chat, or any task whose own origin is not on GitHub, can name a pull request
or issue with `propose_task`'s `origin` argument; a task already in a GitHub
tree keeps that origin (`model.ExplicitOrigin`). The origin is written when
the task is first stored and never changes after.

Tasks that existed before origins were added were backfilled once: a task
filed against an issue got that issue, and a task whose parent chain reaches
one got it too. Mid-tree tasks whose root had no issue were initially left
with no origin stored.

Tasks filed before inheritance walked up to the root could take their parent
as their origin when the parent had none stored (any tree from before
origins whose root had no issue). Opening the store moves each task whose
origin names a task that is not a root to that tree's root, and fills
pre-origin mid-tree tasks (rows with a proposed-by link and no origin)
with their tree's root origin (`Store.repairWorkOrigins`); once the rows
are right it finds nothing to do.

The task view in grain shows the origin beside "generated from" (the parent).

## The tree in grain

A task that belongs to a tree has the whole tree on its own page: the tree's
tasks nested under the task that filed each one, with each task's status and
pull request, and "n of m done". A task belongs to a tree if it has an origin
or has filed work itself. A tree rooted at a grain task (a chat, usually)
has nowhere else to be read. A tree with a GitHub origin is the same tree
the progress comment on that origin shows (see below).

The page reads `GET /api/tasks/{id}/tree`. That returns the tree for the
task's origin, or the tree rooted at the task when it has no origin. The
root task is drawn at the top but is not counted in "n of m": a chat is
where the work came from, not a piece of it. Tasks from teams the reader
cannot see are left out, and the tasks they filed move up to where they
would have been. The tree and its status words come from
`model.Store.WorkTree`, which also builds the progress comment, so the two
cannot disagree.

A saved board of kind `tree` draws the backlog the same way: every task
the board's query matches, nested under the task that filed it.

## What grain writes on GitHub

**The pull request description.** Every task pull request grain opens or
refreshes has a section grain writes itself, between
`<!-- grain:lineage -->` markers, above the footer:

```
Filed by: task 1766 (bwsalmon/grain#2700), Plan the design doc
Origin: bwsalmon/grain#2710

Follow-up tasks filed:
- task 1910: Port the change to candidate (queued)
- task 1911: Test the retagging (PR open bwsalmon/grain#2731)
```

The follow-up list is every task this task filed during its runs, with its
current state, so a reviewer sees what the pull request deliberately left
for later. It is refreshed when one of them changes state. A task with no
parent, no origin and no follow-ups gets no section. As with the rest of
the description, grain only rewrites a description it wrote
(`grainAuthored`).

**A label.** When the origin is a GitHub pull request or issue, the pull
request is labelled `origin/#<n>` (`origin/<owner>/<repo>#<n>` across
repositories). GitHub creates the label on first use; a repository that
refuses costs a log line and nothing else.

**Commit trailers.** The merge commit grain makes for a task's pull request
carries

```
Origin: bwsalmon/grain#2710      (or: Origin: task 1766)
Filed-by: task 1766
```

under the pull request's title, and the agent is asked to end its own
commits with the same trailers. Design work adds `Design-Item:` and
`Design-Rev:` (docs/design-docs.md). So

```
git log --grep 'Origin: bwsalmon/grain#2710'
```

lists everything from that tree that has landed.

**The progress comment.** When the origin is a pull request or an issue,
grain posts one comment on it when the tree's first task is filed, and edits
that same comment from then on -- never a new comment per event:

```
Work from bwsalmon/grain#2710: 2 of 5 done

- [x] task 1800: Plan design doc changes (merged bwsalmon/grain#2715)
  - [ ] task 1801: Implement D-1 (PR open bwsalmon/grain#2720)
    - [ ] task 1805: Follow up on D-1 (planned)
  - [x] task 1802: Implement D-2 (merged bwsalmon/grain#2718)
  - [ ] task 1803: Verify the doc (queued)
```

States are planned, queued, running, PR open (with its link), merged, closed.
A task counts as done when its pull request merged or the task is closed or
completed. The comment is looked at no more than once every two minutes per
origin (once an hour when everything is done), and edited only when what it
says changed (the `work-trees` reconciler, `pkg/orchestrator/worktree.go`).
grain remembers which comment it posted in the `work_origin_comment` table.

When the origin is a grain task rather than a GitHub object, there is no
comment: open the task in grain.
