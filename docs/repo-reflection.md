# The weekly repository reflection

A standing job that reads what this deployment's tasks kept tripping over and
proposes changes to the repositories themselves, such as a lint rule, a helper
script or an agent-instructions entry, so the next agent working there does not
trip over the same thing. It proposes and never applies.

This describes what is implemented (`model.RepoReflectionTemplateID` and
`repoReflectionTemplate` in `pkg/model/repo_reflection_template.go`, seeded by
`pkg/orchestrator/systemschedule.go`).

## What it is

- **A schedule every deployment gets.** `system:repo-reflection` is seeded the
  way `system:memory-agent` and `system:infra-review` are: a well-known id,
  written once, and the deployment's from then on. Turning it off is the
  schedule's **Enabled** switch in Schedules, like any other system job.
- **Weekly**, Mondays at the deployment's quiet hour, reading the last 14 days.
  A friction has to recur across distinct tasks before it is worth a proposal,
  and a single night rarely holds enough tasks to tell.
- **No target, and no capability that writes a repository or a setting.** It
  holds `api#self-debug` (task records, transcripts, `grain metrics`),
  `grain#source` (to check claims about grain against its source) and
  `api#memory`, which it uses for exactly one document (below). It reads the
  state repository.
- **Its output is a report and, sometimes, proposals.** The report is the run's
  closing note. Each change it wants is a `grain task propose --repo
  owner/name`. The sandbox CLI files every proposal held for approval, so
  nothing lands until a person approves it in the Inbox. A firing proposes at
  most three changes.

## How it decides

1. **History first.** It reads its own earlier reports and the proposals they
   filed. It does not re-file one that is still waiting, does not re-file a
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

"Nothing proposed" is the ordinary outcome of a week.

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
deployment's operator may not have access to grain's own repository. The
reflection therefore collects these in one memory document,
`grain-improvement-proposals`, in the deployment scope, where a person triages
them from the **Context** page and files the ones worth filing upstream. Each
entry has a status (`proposed`, `triaged`, `filed <link>`, `rejected`), the task
ids behind it and a suggested change, and is written so it can be pasted into a
public issue as it stands. The document is capped at 20 open entries.

That document is the only thing the reflection writes to memory. It never
touches the short memory or any other document.

## Left open

- Whether the ledger should become a first-class feedback queue rather than a
  memory document.
- Proposed changes are not benchmarked before they are filed. A reviewer who
  wants evidence that a new instruction changes agent behaviour can ask for it
  on the proposal.
