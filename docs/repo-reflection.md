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
  claims about grain against its source), `api#memory:read` (read-only access
  to inspect surviving memory-agent notes as leads without being able to
  overwrite prompt memory) and `api#feedback` (to record findings about grain
  itself). It reads the state repository. It files at most three proposals per
  firing, through `propose_task`. A firing with nothing to propose ends with
  `nothing_needed` and closes itself.

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
   - **Grain itself** goes into the `grain_feedback` triage store.
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

## The Grain Feedback triage store

Friction caused by grain itself (a tool answer that misleads, a missing CLI
flag, an absent capability) is not something a repository can fix, and a
deployment's operator may not have access to grain's own repository. Each
repository's reflection therefore records these via `grain feedback record
<slug>` into the deployment-wide `grain_feedback` table (`POST /api/feedback`),
never into prompt memory:

- **Zero prompt cost and no memory-agent interference.** Entries never appear
  in `MemoryIndex` on coding runs and are never audited or rewritten by the
  nightly memory agent.
- **Atomic cross-repository deduplication.** When multiple repositories or
  firings hit the same `<slug>`, `grain_feedback` unions their repositories and
  task ids and increments `occurrence_count` while preserving any human-set
  status (`triaged`, `filed`, `rejected`) and `upstream_url`.
- **Single first-seen alert.** Recording a brand-new slug raises one
  low-severity `info` notification (`feedback.new:<slug>`); repeat sightings
  bump the counter silently, and triaging or dismissing the entry clears the
  notification.
- **Human triage on Context → Grain Feedback.** Operators inspect entries
  ranked by recurrence, copy redacted Markdown formatted for a public GitHub
  issue in one click, link an upstream issue URL, or dismiss entries so future
  reflections do not re-propose them.

## Left open

- Proposed changes are not benchmarked before they are filed. A reviewer who
  wants evidence that a new instruction changes agent behaviour can ask for it
  on the pull request.
- A repository that is no longer active keeps its row until nothing references
  it. Its firings end at once with `nothing_needed`, but each still costs a
  short run; disable the row to stop them.
