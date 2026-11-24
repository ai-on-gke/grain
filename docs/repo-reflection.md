# The repository reflection

A standing job, one per repository, that reads what this deployment's tasks
in that repository kept tripping over and proposes changes to the repository
itself, such as a lint rule, a helper script or an agent-instructions entry,
so the next agent working there does not trip over the same thing. It
proposes, and never applies.

This describes what is implemented (`model.RepoReflectionTemplatePrefix` and
`repoReflectionTemplate` in `pkg/model/repo_reflection_template.go`, seeded by
`reconcileRepoReflectionSchedules` in `pkg/orchestrator/systemschedule.go`).

## What it is

- **One schedule per repository, created for you.** For every repository
  this deployment works in (every repository with a settings row, and every
  repository a task has targeted, except the state repository) grain keeps
  a system schedule `system:repo-reflection:<owner>/<name>`. It removes the
  schedule when the repository goes.
- **Your settings, per repository.** Each row's cadence, **Enabled** switch,
  model and **auto-approve proposals** switch are the deployment's to edit in
  Schedules, like any other system job, and grain never resets them. A
  repository grain changes all day can run daily while the rest keep the
  default.
- **Every three days by default.** That is often enough for friction to turn
  into fixes while the tasks that showed it are still fresh. Each firing reads
  from its previous report back as far again (between 3 and 30 days; 14 on its
  first run), so a friction seen once on each side of the last report still
  counts twice.
- **Proposals open pull requests straight away by default.** The row starts
  with **auto-approve proposals** on: each proposal is queued as soon as it is
  filed and opens its own pull request, which is reviewed before it merges.
  Proposals never auto-merge. Turn the switch off on a repository's row to
  hold its proposals in the Inbox for a person to approve first.
- **Targets its repository, and does not commit.** It holds `api#self-debug`
  (task records, transcripts, `grain metrics`), `grain#source` (to check
  claims about grain against its source) and `api#memory`, which it uses for
  exactly one document (below). It reads the state repository. It files at
  most three proposals per firing, through `propose_task`. A firing with
  nothing to propose ends with `nothing_needed` and closes itself.

## How it decides

1. **History first.** It reads its own earlier reports and the proposals they
   filed. It does not re-file one that is still open, does not re-file a
   rejected one without new evidence, and checks whether a merged one actually
   stopped the friction.
2. **Recurrence across distinct tasks.** A finding must cite the task ids that
   show it. One task going wrong is an anecdote.
3. **Whose it is.** A finding belongs to one of three owners:
   - **The repository** gets a proposal.
   - **This deployment's configuration** (a grant, a sandbox size, a repository
     setting) goes in the report, addressed to the operator. A branch cannot
     change these.
   - **Grain itself** goes in the grain-improvement ledger.
4. **The strongest control that fits.** For repository findings it walks a
   ladder and proposes the first rung that would have stopped the friction:

   | Rung | Control |
   | :--- | :--- |
   | 1 | Eliminate: change shared code so the mistake cannot be made |
   | 2 | Automate: a lint rule, test or CI check |
   | 3 | Tool: a repository script for the error-prone procedure |
   | 4 | Route: link or move existing guidance where agents look (`AGENTS.md`, `CLAUDE.md`, skills) |
   | 5 | Document: a new instruction, only with a stated reason rungs 1–4 do not apply and at least two cases outside the incident |
   | 6 | Annotate: a scoped suppression with its reason |
   | 7 | Discard |

"Nothing proposed" is the ordinary outcome of a firing.

## How it differs from the memory agent

The memory agent writes what it learned into this deployment's memory: prose
that every later run of this deployment reads and that nobody reviews. The
reflection proposes changes that land in the repository through a reviewed
pull request, so they help every agent working there, including agents outside
grain. It also prefers checks and tools over prose. A memory note that survives
for weeks is one of its best leads: the friction keeps recurring, and only the
repository can retire it. A proposal that would make a note redundant says so,
so the note can go once the change merges.

## The grain-improvement ledger

Friction caused by grain itself (a tool answer that misleads, a missing CLI
flag, an absent capability) is not something a repository can fix, and a
deployment's operator may not have access to grain's own repository. Each
repository's reflection therefore collects these in a memory document,
`grain-improvement-proposals`, in that repository's scope. A person triages
them from the **Context** page and files the ones worth filing upstream. Each
entry has a status (`proposed`, `triaged`, `filed <link>`, `rejected`), the task
ids behind it and a suggested change, and is written so it can be pasted into a
public issue as it stands. The document is capped at 20 open entries, and one
per repository means two reflections firing at once never rewrite the same
document.

That document is the only thing the reflection writes to memory. It never
touches the short memory or any other document.

## Left open

- Whether the ledger should become a first-class feedback queue rather than a
  memory document.
- Proposed changes are not benchmarked before they are filed. A reviewer who
  wants evidence that a new instruction changes agent behaviour can ask for it
  on the pull request.
- A repository that is no longer active keeps its row until nothing references
  it. Its firings end at once with `nothing_needed`, but each still costs a
  short run; disable the row to stop them.
