# Attribution: crediting the people behind a change

`contributions.design.md` defines how work flows through this repository: human-written design docs, agent-written specifications that humans review, agent-written task grainfiles that humans audit, and autonomous implementation PRs, with clarifications raised as PRs against the design folder. It leaves attribution to a second design. This is that design. The goal is that the people who shaped a change are credited on every implementation PR that comes out of it and on the merged commit. That covers PMs, engineers, and reviewers whose comments changed the outcome. The doc also proposes checks that make sure this happens, not just conventions.

**The core rule:** contributors come only from the design-folder PRs: design, spec, tasks and clarification PRs. On those PRs, a contributor is a human author, a reviewer whose comments changed something, a person added with `/contributors`, or a commenter whose request an agent carried out. Contributors never come from the implementation (code) PRs themselves. An implementation PR's Contributors section and its `Co-authored-by:` trailers are exactly the union of the contributor sets of the design, spec and tasks PRs in its chain. The code PR's own commit authors and reviewers are not added.

## Identity

A contributor is a git identity, `Name <email>`. This is the form a `Co-authored-by:` trailer takes and the form the grainfile `requestor` field already uses (`docs/grainfiles-spec.md` §6.2). GitHub noreply addresses (`<id>+<login>@users.noreply.github.com`) are fine for people who don't want their email in the repo. Grain resolves a `@handle` to that noreply form. Agents are never listed as contributors. Grain already records agent identity in the bot commit author and the task URL.

## Attribution lives on PRs, not in documents

Design docs, specs and task grainfiles carry **no contributor lists**. Central design docs are edited many times, and a list in the doc would grow to everyone who ever touched it. The existing optional `requestor` field on `.task.grain.md` stays as it is.

Instead, every design-folder PR (design, spec, tasks, clarification, playbook) has a **contributor set**, which grain derives from the PR itself:

- the authors of its commits, and every `Co-authored-by:` trailer on them;
- reviewers whose comments led to a change, meaning a commit pushed after the comment that addresses its thread. A review that only approves, without changing anything, does not count;
- anything added with the commands below.

Grain shows the set in a **Contributors** section of the PR description and keeps it up to date as commits and comments arrive. An implementation PR shows the derived chain union in the same section, read-only. Where a role is obvious, grain shows it next to the name: `author` for commit authors, `editor` for someone who pushed to another person's PR, and `reviewer` for someone whose comments changed the PR. Roles are never required.

## Adding and removing contributors by comment

On a **design-folder PR**, anyone with write access to the repository can adjust the PR's set with a comment, one command per line:

```
/contributors add Jane Doe <jane@example.com>
/contributors add @janedoe
/contributors remove @janedoe
/contributors add --quality-by @oncall-handle
```

`remove` only removes names that were added by hand. Derived contributors (commit authors, trailers, reviewers whose comments changed the PR) can't be removed this way, so a typo can't undo real credit. Grain replies on the comment with the resulting set and updates the description. Commands from people without write access, and commands from agents, get a reply explaining why they were ignored. On implementation PRs, `/contributors` is refused with a reply that points at the design-folder PRs in the chain. The one exception is `--quality-by`, which is per task (below) and so is accepted there.

## Agents listen on design-folder PRs

As `contributions.design.md` already requires, an agent watches every design-folder PR until it merges or closes. This includes human-authored design PRs once grain is referenced on them. When a human comments, or mentions grain or replies on a review thread, the agent makes the requested change, pushes it to the same PR, and replies on the thread. The same agent handles `/contributors` commands. A human whose comment leads to an agent-made change counts as a contributor to that PR, exactly like a reviewer whose comment changed the doc. The agent's commit for that change carries a `Co-authored-by:` trailer for the commenter. Asking for a change by comment therefore earns credit.

## Propagation runs PR to PR

Each stage's PR records the PR it derives from:

- Grain writes `Derived from: <PR URL>` in the description of every spec and tasks PR it opens, and stores the link in its DB. A spec PR points at the design PR, and a tasks PR points at the spec PR.
- A task imported from a grainfile is linked to the tasks PR that merged that grainfile.
- Sub-tasks an agent decomposes a task into (`propose_task`, recorded as `LinkProposedBy`) inherit their parent's chain automatically, so an agent can't drop it by forgetting to copy it.
- An implementation PR's contributors are the **union** of the sets of the design, spec and tasks PRs in its chain. If several tasks push to one code PR, the union covers each of those tasks' chains; it does not include whoever pushed. Entries are de-duplicated by email and kept in chain order.

A clarification PR is a design-folder PR, so its contributors count. They flow into every code PR whose chain includes it. For bug-driven work, the bug issue or PR plays the design-folder role. Its contributors are whoever filed it, commented on it substantively, or tagged it into the pipeline. The contributors of a design the bug cites are not credited.

## Edits to existing documents

An edit to an approved design is a new design PR. The spec and tasks PRs it triggers derive from **that** PR, so their chain points at the edit's PRs, not the original design's. The people who made the edit get the credit, and a central doc's long history isn't re-credited on every downstream change. A design's earlier PR contributors are **not** credited on edit-driven PRs: a trailer means "contributed to this change". The edit's description still links the earlier design PRs for context.

## Quality by (optional)

A repo or team can enable a separate **Quality by** credit for the person who managed the backlog. That person is the team's grain on-call or backlog manager who was responsible for the task while it ran. Grain prefers whoever actually acted on the task: approved it, unblocked it, retried it, or answered its escalation. If nobody did, grain uses whoever held the on-call or backlog role during the task's lifetime. Grain records this on the task, and there can be more than one person. The PR description shows it as a "Quality by" line in the Contributors section. The merge commit carries a `Quality-by: Name <email>` trailer, in the style of git's `Reviewed-by:` and `Acked-by:`. It is not `Co-authored-by:`, because these people didn't author the change. `/contributors add|remove --quality-by` adjusts the entry.

## The merge commit

Grain composes the squash or merge message itself rather than relying on agents to write trailers. It recomputes the chain union at merge time and writes one `Co-authored-by:` trailer per contributor, so GitHub credits each of them. This extends what `pkg/orchestrator/takeover.go` already does for a taken-over PR's original author.

## Making sure it happens

- **Merge-queue check.** Before merging, grain checks the PR description and the final commit message against the chain union, and repairs them if anything is missing. When Quality by is enabled, the check also covers the `Quality-by:` trailers.
- **Derivation check.** A spec or tasks PR without a `Derived from:` link fails a check. So does a tasks PR whose link doesn't resolve to a merged spec PR.
- **Post-merge audit.** A merged commit that still lacks a trailer for a contributor in its union is raised in the grain inbox.
- **Durable record.** When a task completes, its completion note in the design folder links the implementation PR(s). The contributors are visible there, outside grain's DB.

## Follow-up work

1. Contributor sets for design-folder PRs (and bug issues): derive them from commits, trailers and comment-driven changes; keep the description section updated; store the set in grain's DB.
2. `/contributors add|remove` command handling, with write-access checks and replies.
3. `Derived from:` links on spec and tasks PRs; link imported tasks to their tasks PR; inherit the chain over `LinkProposedBy`.
4. Compose merge messages with `Co-authored-by:` trailers from the chain union, recomputed at merge.
5. Optional Quality by: record acting on-call and backlog managers on tasks, write `Quality-by:` trailers, and add a per-repo or per-team setting.
6. Merge-queue and derivation checks, the post-merge audit raised in the grain inbox, and completion notes that link implementation PRs.
