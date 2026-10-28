# Should the State pane carry a settings reset the pull request never heard about?

**Status: decided and built** (grain/task-197). Approving that task was
the yes to both questions at the end of this document, and what it built
is what "What building it would cost" describes, corrections included:
the marker appends within a cap, and the notice has a marker on the rail
beside it. The document is kept as it was written -- the reasoning is
what the code's own comments point back at, and a proposal rewritten
after the fact stops being a record of the decision. This is the third of
the three surfaces grain/task-156 listed for
the settings-reset notice -- the journal (task-156), the merged pull
request (task-192, `cmd/grain/stateresets.go`), and this pane -- written
from the code as it stands so that the decision is a yes or a no rather
than a design exercise.

**The recommendation, in three lines.** Yes, carry it, and only when the
pull-request comment could not be delivered. But it is a smaller thing
than task-156 imagined it was, because the journal is already in the
browser (`LogsPage.jsx`), so the pane notice is a *pointer to a fact*
rather than the last surviving copy of one -- which is what makes
dismissing it safe. Two details of the clearing rule as proposed are
wrong and are corrected below: the marker must append rather than
overwrite, and the pane on its own reaches nobody without a marker on the
rail beside it.

## What exists already

`pkg/staterepo/resets.go` reads what a merged settings file is about to
overwrite, in the moment before `ImportTables` replaces the row, and says
so twice: `reportResets` writes the journal line, which is this package's
own record and cannot fail, and `announceResets` hands the same
`[]staterepo.Reset` to `Config.AnnounceResets` -- the seam for any
surface the deployment can actually reach.

`cmd/grain/stateresets.go` is the one surface wired to that seam today.
It posts the notice as a comment on the merged pull request the commit
arrived in, with the credential this deployment already pushes its
exports with. Every field a pane would want to show is exported on
`staterepo.Reset`, already cut to one line and one screen's width by
`abbreviate`, so nothing below needs new plumbing in `pkg/staterepo` for
the *values*. The question is only ever about lifecycle.

`stateResetNotice.announce` gives up in six places, and each one is a
deployment where the comment never lands:

- no `state-repo.json` to read (`LoadSettings` failed);
- `githubRepoFromRemote` says no -- a local-only deployment, or a state
  repository that is not on GitHub. This branch is deliberately silent,
  because it would otherwise log the same sentence forever;
- no credential, or a credential that will not resolve;
- `MergedPullRequestForCommit` failed;
- it returned nothing, because the commit was pushed straight at the
  branch rather than merged from a pull request -- a supported way to
  change settings that simply has no conversation attached to it;
- `CreateComment` was refused, for permissions or anything else.

Those six are the whole case for a third surface. Everywhere else the
person who caused the reset is told where they caused it, which is the
right place and the end of it.

## The fact task-156's framing missed

The daemon's journal is already readable in this UI. `cmd/grain/daemon.go`
wires `ui.Config.Logs["daemon"]` to `journalctl -u grain-daemon.service`,
and `LogsPage.jsx` -- its own entry in the sidebar -- tails it. So the
operator "who never logs into the host" can, today, read the exact line
`reportResets` wrote, in a browser, without a shell.

This does not close the case, because that pane is a 500-line tail with a
source picker and no search (`LINES_TO_FETCH`, `REFRESH_MS`). Finding a
reset from three days ago in it means scrolling a busy deployment's
journal past everything else it has said since, and nobody scrolls
looking for an event they were never told happened. The reachability of
the fact is fine; what is missing is that anybody knows to go and look.

But it changes three things about the design, all of them in the
direction of *less* ceremony:

1. **Dismissal is cheap.** task-156 worried that dismissal is "a person
   saying they have read the one copy of a value that is gone". It is
   not the one copy. The journal line survives a dismissal and is
   reachable from the same browser, subject to journald's retention. A
   dismissed notice costs the reader a scroll, not a value.
2. **The notice does not have to be an archive.** It should still carry
   the values -- see the scrolling problem above -- but it does not have
   to be sized, capped and guarded like the only record of them. It can
   say what it knows and point at the journal for the rest, exactly as
   `resetComment` already does past `resetCommentValues`.
3. **"A badge nobody can dismiss is worse than a journal line" stops
   being the risk.** The risk is the opposite one: a notice nobody ever
   sees, because it is on a tab nobody opens.

## Question 1: does the pane carry a dismissable notice at all?

**Yes.** The five branches above are not exotic. A fresh install is
local-only -- that is the state `grain state adopt` moves a deployment
*out of* -- and a local-only deployment is precisely one being run
through this UI by somebody who has never used the CLI, since a state
repository with no remote has no pull request anywhere to carry anything.
Today such a deployment resets a value and says so in a journal line, in
a pane that will not still be showing it by the time anyone looks.

The pane is also the surface this repository has already decided such
things belong on: `StateRepoStatus.WorkflowRefused` is a fact read out of
a marker beside the working tree for exactly this reason, and
`UnbackedStateDot.jsx` is a whole argument about how the State pane
reaches somebody who does not open it.

**But the pane alone reaches nobody.** `UnbackedStateDot.jsx` makes this
point about its own warning in as many words: the pane "is opened by
roughly the same operator who would have run `grain state status` in a
terminal ... what it lacked was a reader". A reset notice on that tab and
nowhere else has the same defect, and worse, because a reset is a thing
that happened once rather than a condition that will still be there next
month. **So the notice needs the marker on the rail as well** -- a second
boolean on `GET /api/config` next to `stateRepoUnbacked`, carrying no
values (that route is polled by every open tab; the values stay on
`GET /api/state-repo`, which the pane fetches once when it opens).

A dot rather than a `BannerStrip` banner, for the reasons that file
already gives: the strip is for a dead reconcile loop, a paused agent and
host sandboxing, and one more permanent thing across every page is how
people learn to read past all of them.

## Question 2: is "only when the comment could not be posted" too clever?

**No, on one condition: the notice says its own reason, in its own
sentence, rather than the rule living only in the code.**

The rule is hard to state in the abstract -- "you are seeing this because
of where you are not seeing it" -- and easy to state concretely, because
`announce` already has a distinct, human sentence written for each of the
give-up branches. Reused, the pane reads:

> Merging `a1b2c3d4` reset 3 settings on this deployment. grain could not
> post this on the pull request it arrived in -- this deployment's state
> repository is local only, so there is no pull request to post it on --
> so this and the deployment's journal are the only places these values
> are written down. [values] [Dismiss]

Nobody has to be taught a rule to read that. It is also the same
discipline `resetComment` already keeps with the journal: one rendering
of one fact, so that two surfaces cannot come to disagree.

What the narrow rule deliberately gives up is worth naming so that the
decision is made with it in view:

- **The transient failure.** A `MergedPullRequestForCommit` that failed
  on a network blip writes a notice that a retry would have made
  unnecessary; grain does not retry the comment. This is the rule
  behaving correctly -- the comment was in fact never posted -- but it
  means the pane can carry a notice for a deployment that looks, to
  anyone else, like one that gets comments.
- **The comment that landed where the pane's reader cannot see it.** A
  private state repository whose operators are not all on GitHub gets a
  comment and no pane notice. That case is left to the journal, as it is
  today. Widening the rule to "always" to catch it would fire on every
  deployment that is already told properly, which is the outcome the
  narrow rule exists to avoid.

## What is wrong with the clearing rule as proposed

task-156 sketched: a marker beside the working tree, holding the rendered
notice and its commit, "replaced by the next one", cleared by an operator
dismissing it. Two corrections.

**It must append, not overwrite.** The whole justification for the notice
is that these values exist nowhere a reader will find them. A second
merge that resets something before anybody has opened the pane would,
under "replaced by the next one", silently discard the first notice --
the one failure mode the feature is for. Keep a bounded list: append,
cap the number of notices retained and the number of values per notice
(`resetCommentValues` is the precedent and the reasoning is identical),
and say "and N more, in this deployment's journal" past the cap.

**Dismissal deletes, and is not reversible from the pane -- which is
right, and is a different thing from `LocalOnlyAcknowledged`.** That flag
had to be reversible because it describes a condition an operator can
change their mind about, and it deliberately does not hide the pane's
warning, only the rail dot. This is an event: dismissing it is a person
saying "I have read this", there is nothing left to un-read, and the
journal keeps the record either way. So dismissal removes the notice and
the dot together, and a later reset that cannot be delivered puts a fresh
one up. Server-side, in the marker, not in the browser -- for the reason
`UnbackedStateDot.jsx` gives about per-browser dismissals landing on the
wrong person.

Two smaller lifecycle answers, for completeness: nothing expires on a
timer (an unread notice that ages out is the badge doing exactly what it
was built not to do), and nothing clears on a subsequent successful
comment (a later merge delivering its own notice says nothing about an
earlier one that did not).

## What building it would cost

Roughly, and in the order the fact travels:

- `pkg/staterepo`: `Config.AnnounceResets` grows an `error` return, and
  `announceResets` records the marker when the callback did not deliver.
  A sentinel for "there is nowhere to say it" (local-only, non-GitHub
  remote) is needed alongside a plain error, because that case is not a
  failure and must not read as one in the journal. The marker itself goes
  in the git directory beside `grain-workflow-refused`
  (`format.go`) -- a fact about this host and its credential, never
  committed, never travelling to a clone -- as JSON, with a
  `Repo.UndeliveredResets`/`ClearUndeliveredResets` pair to match
  `WorkflowRefusedAt`/`clearWorkflowRefused`. Writing it from
  `cmd/grain` instead was the alternative; it is rejected because
  `stateResetNotice` holds only `dataDir`, and because the fact belongs
  to the package that produced it.
- `cmd/grain/stateresets.go`: each give-up branch returns its reason
  instead of only logging it. The journal lines stay exactly as they are.
- `cmd/grain/statemanager.go` and `pkg/ui/staterepo.go`: the notice on
  `StateRepoStatus`, read out of the marker the way `WorkflowRefusedAt`
  already is, plus `POST /api/state-repo/reset-notice` to dismiss,
  shaped like `handleAcknowledgeLocalOnly`.
- `cmd/grain/daemon.go` and `pkg/ui/tasks.go`: the boolean for the rail
  dot, beside `StateRepoUnbacked`.
- `cmd/grain/state.go`: the same marker printed by `grain state status`,
  for the deployment with no browser -- and because that file already
  says the terminal and the pane must not come to say different things
  about one repository.
- `ui/`: an `Alert` with a dismiss button in `StateRepoPanel.jsx`, and
  the dot in `UnbackedStateDot.jsx`'s two places, with the tests beside
  each.

Call it a day's work, most of it wording.

## The one alternative that beats it

If grain would rather spend the same effort making the Logs pane
searchable -- a filter box and a "since" control over the source it
already tails -- every deployment gets a better answer to this and to
every other "grain said something once and I did not see it" at the same
time, and this feature shrinks to nothing. It does not remove the "nobody
knew to look" problem, which is the actual gap, so my recommendation is
still the notice; but if the notice is judged too much machinery for one
event, that is the direction the effort should go rather than nowhere.

## What the human is being asked

1. Does grain want a dismissable notice on the State pane at all, given
   that it needs a rail dot to be seen and that dismissal is a route that
   turns a warning off? My answer: yes.
2. Is "only when the comment could not be posted" too clever to explain?
   My answer: no, provided the notice carries its own reason in its own
   sentence, reusing the ones `announce` already writes.

A follow-up task is proposed for the build, blocked on this one, and left
unapproved: approving it is the yes.

Both were answered yes (grain/task-197). Two things that document did not
settle, and the build did: the commit pushed straight at the branch is
recorded through the same sentinel as local-only, because it is the same
"there was nowhere to say it" and not a fault; and the rail's marker is
the dot that already existed rather than a second one beside it, since a
dot means "something on that pane is worth reading" and a rail that
answered that question twice would have an operator counting dots instead
of opening the pane.

A third thing it did not settle, corrected afterwards (grain/task-229):
this document lists `grain state status` as a surface and says nothing
about clearing from there, and the build took that literally -- the
terminal printed the notice and only the pane could dismiss it. That left
the deployment the notice exists for, the one with no browser, reading a
block it could not take away and watching every later reset append
underneath it. `grain state resets clear` is the dismissal at a terminal,
the same delete on the same marker as the pane's button. It prints the
notice before deleting it, which the pane has no need to do: the pane
shows the values and the button together, and a terminal cannot, so the
last thing that command does for the values is put one more copy in the
scrollback of the person forgetting them. Nothing to clear is not an
error, for the same reason it is not on the pane's route.
