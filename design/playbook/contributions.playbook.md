# Grain contribution processes

**Owner:** @bwsalmon

**The core rule:** humans own the designs, and every change to a design, spec, tasks file or playbook needs a human's approval. Agents write and review the code. People commit the tasks, and grain does the rest. Every review checks the change against the designs and specs that govern it.

The precise path table, enforcing files and format rules are in the spec, [`contributions.spec.md`](../spec/contributions/contributions.spec.md). Ownership and credit are defined in [`attribution.design.md`](../design/attribution.design.md). When a pull request merges is defined in [`merging.design.md`](../design/merging.design.md).

## Principles

**Humans own designs.** Designs include design documents, specs, UI mockups and playbook designs. Agents may do most of the writing and revising, but a human approves every change. Designs are how humans control what is built. An agent may propose a design change while it writes code; a human must approve it.

**Agents own code.** Agents change code without human input. Humans may direct how code is written, or review it where they choose. Code must comply with the designs and specs, and every code change gets at least one agent review besides its author, which checks it against the repository's goals, processes and standards.

**Processes matter more than code.** Review processes, qualification suites and testing procedures are designs that humans curate and approve. Enforcing them is how quality is kept on top of stochastic programming.

## Terms

- **Owner (of a design or playbook design):** each person or team on its `**Owner:**` line.
- **Owner (of a task or PR):** the one person grain credits and holds responsible for it (`attribution.design.md`). Child tasks inherit their parent's owner. A schedule's tasks take the schedule's owner, or the current on-call when the owner is `oncall`.
- **Author:** a person credited as an author of a PR on its merge commit. On a PR grain opens from a tasks file, the authors are the tasks PR's owner and its `Contributors:`.
- **Contributor:** a person listed in a tasks PR's `Contributors:` block, credited as a co-author on every PR grain opens from those tasks.
- **Grainfile:** a `.task.grain.md` or `.process.grain.md` file (`grainfiles.design.md`).
- **Tasks PR:** a PR that adds or changes `.task.grain.md` grainfiles.
- **Review:** an agent's or a human's examination of a change. A grain agent's review of a PR is reported as the `grain/review` check.
- **Check:** a status on a PR's head commit that GitHub can require before merge.
- **Submit:** a person's approval, in grain, of a change that requires human sign-off. Submit satisfies the human approval gate only and never skips or cancels any required security or code review (`merging.design.md`).

## The design folder

Each repository has a `design/` folder. Every file is in exactly one class:

| Class | Path | What it holds |
| --- | --- | --- |
| Designs | `design/design/**/*.design.md` | High-level 1-pager designs for a topic, written by PMs, engineers and agents in interactive sessions. |
| Specs | `design/spec/<name>/**` | Detailed, normative specs of a design: APIs, schemas, requirement IDs and UI mockups. Usually one file or folder per design. |
| Playbook designs | `design/playbook/*.playbook.md` | High-level 1-pager designs for a background process, such as releases, qualification or nightly testing. |
| Tasks | `**/*.task.grain.md` | Task definitions that grain imports once merged. |
| Playbooks | `**/*.process.grain.md` and `.grain/**` | Standing tasks, triggers and schedules in grain's declarative formats. Usually one file or folder per playbook design. |
| GitHub enforcement | `.github/**` | The GitHub files that enforce this playbook. |
| Code | everything else | |

Any design may link to documents in other repositories. `.grain/` and `.github/` stay at the repository root, because that is where grain and GitHub read them. They hold the files that enforce this playbook; the spec maps each rule to its enforcing file.

**Agent designs** are a seventh class: designs, rules and principles that agents decide on. A change needs at least two agents to review it. Agent designs must conform to the human designs, which win any conflict. Otherwise agents may expand them as they see fit.

> **Reviewer note (Q3):** agent designs have no path yet, and "two agents review" differs from the Code rule's one grain agent review. See the PR description.

All design material may instead live in a separate repository. Both layouts follow the same rules.

## Reviews

- **Designs:** an owner and at least one other human approve.
- **Specs:** at least one human approves, and a second entity (human or agent) reviews.
- **Playbook designs:** an owner and at least one other human approve.
- **Playbooks:** at least one human approves, and a second entity (human or agent) reviews.
- **Code:** at least two entities: its author (an agent or a human) and a grain agent that reviews it.

> **Reviewer note (Q1, Q2):** this list gives no rule for Tasks or for GitHub enforcement. The spec uses "one human approves, one other entity reviews" for both, marked provisional. See the PR description.

**Conformance.** Every review grain performs checks the change against the designs (`design/design/`, `design/playbook/`) and specs (`design/spec/`) that govern it, so that code, specs and designs stay in sync. This covers code, spec, tasks and playbook PRs, PRs grain opens and external PRs, and security reviews. The reviewer:

- finds the designs and specs the change touches, from its links, its paths and its subject;
- flags each divergence, missing requirement or untested requirement as an inline comment, citing requirement IDs where they exist;
- blocks a change that intentionally departs from a design or spec until the design or spec is updated, or a companion `design/` PR has merged first;
- flags a change that no design governs if it is big enough to need its own design.

## Process

Designs, specs and tasks can be written any way: by filing a grain task, with an external agent, by hand, or any mix. No path is preferred, and all follow the *Reviews* rules. Grain never starts spec or task generation automatically; a person starts each step. Whoever starts a step owns the resulting PR (for a grain task, the task's owner).

When an agent opens a PR, it listens to the PR and answers review comments by pushing updates and replying on each thread. A separate entity, never the run that authored the change, reviews it before merge.

### Design

PMs and engineers write or update a design on a branch, with whatever tools suit them. It merges once an owner and at least one other human approve.

### Specification

A person starts the spec. The spec PR follows the *Reviews* rules, and the agent that opened it answers review comments until humans approve and merge it.

> **Reviewer note (Q6):** "humans approve and merge" here and under *Tasks* differs from `merging.design.md`, where grain merges a PR it opened once its gates, including a Submit, pass. See the PR description.

### Tasks

A person starts the tasks step. The tasks PR adds `.task.grain.md` grainfiles with task descriptions, dependencies and requested capabilities (`capabilities:`, `grainfiles.design.md`). It follows the *Reviews* rules, and the agent that opened it answers review comments until humans audit, approve and merge it.

The tasks PR's owner owns every task imported from it, and is the author of every PR grain opens from those tasks, together with the tasks PR's `Contributors:` (`attribution.design.md`).

### Implementation

Code reaches the repository by one of two paths.

#### From tasks (native)

People commit the tasks by merging the tasks PR, and grain does the rest:

- Grain imports the merged grainfiles into its backlog, which the on-call watches.
- Agents split tasks into child tasks as needed. Child tasks stay within the parent's capabilities and inherit its owner.
- An agent writes each code PR, a separate agent reviews it, and CI runs. Grain merges the PR once it is clean, green and through its gates (`merging.design.md`). No human is in the loop unless a gate needs one.
- Design ambiguities go to humans as clarification PRs against `design/`. Execution and test blockers go to the grain inbox.
- When a task's PR merges, grain marks the grainfile task complete, with the task's URL as its index into grain's database.

> **Reviewer note (Q5):** `merging.design.md` holds a task without auto-merge for a person's Submit, and the grainfile format has no auto-merge field. See the PR description.

#### External pull requests

A contributor opens a pull request directly on GitHub, by hand or with an interactive coding agent.

- **The check.** The grain review, `grain/review`, is a required, blocking GitHub check that runs on every new head of the PR.
- **Who gets it.** It runs automatically on PRs from organization members and repository collaborators. A PR from anyone else is not reviewed automatically, to protect the model budget. Such a PR needs a review by an organization member before the grain review can run.
- **What it checks.** Code quality, tests, security (including the trust-boundary security review for sensitive paths), and *Conformance*.
- **The owner merges it.** Once `grain/review` and the other required checks pass, the PR's owner may merge it by hand through GitHub.
- **Owner.** The PR declares its owner on a standalone `Owner: Name <email>` or `Owner: @handle` line in its description. Without one, the owner is the PR's GitHub author. Grain takes the declaration as stated (`attribution.design.md`).
- **Trust model.**
  - The model never runs inside GitHub Actions. Fork PRs get no repository secrets, and a model credential in a job that reads untrusted PR content breaks the trust boundary.
  - Instead, the PR event asks grain to review that head, through grain's GitHub App or a thin, secretless Actions workflow calling grain's API. Grain runs the reviewer in its own isolated sandbox, with no write access to the PR's branch or repository, and posts the verdict through the GitHub App as a check run plus inline review comments.
  - The PR's diff, files, commits, title, description and comments are untrusted input. The reviewer's sandbox holds no write, secret, deploy or task-filing grants that could act on them.

> **Reviewer note (Q4):** what "a review by an organization member" means here is open. See the PR description.

### Checking

Besides per-PR reviews, grain playbooks run regularly to check landed code end to end against the designs and specs. Playbooks include human checkpoints where needed, such as reviewing release summaries.

### Loops

Teams track uncaught bugs, review findings and clarification PRs, and use them to refine specs, review prompts and playbooks, so that changes land faster with fewer errors.

### Bugs

Bugs and feature requests arrive as GitHub issues, external PRs, or tasks filed in grain. A person triages each issue into a grain task or an external PR.

### Playbooks

Playbook designs and playbooks follow the *Reviews* rules. Humans write the overall principles and review standing schedules and triggers.

## Implementation gaps

Where the code differs from this design today:

1. **Grainfile import.** Grain does not yet import `.task.grain.md` files, so the native path, its completion record and the tasks-PR attribution do not run.
2. **Owners.** Tasks have no `owner`, so nothing inherits one, and grain does not read `Owner:` or `Contributors:` on PRs. Schedules have no `owner` and `oncall` does not exist (`attribution.design.md` *Follow-up work*).
3. **When `grain/review` runs.** Grain reviews an external PR only when someone requests the grain bot as a reviewer, mentions it, or hands the PR to grain. It does not run on every new head.
4. **Who is trusted.** Grain trusts a sender with write, maintain or admin permission on the repository; organization membership alone does not count. An untrusted sender's review waits for approval in grain, and posts no check.
5. **Blocking.** A review with findings completes `grain/review` as `neutral`, not as a failure. The repository has no ruleset or `CODEOWNERS` that makes it required (spec §5).
6. **Security review.** An external PR gets the trust-boundary security review only when it is handed to grain, not from the plain review.
7. **Conformance.** No review prompt yet tells the reviewer to check designs and specs (`.grain/templates/review.yml`, spec §2.8).
8. **Enforcing files.** The per-class triggers, `CODEOWNERS`, the ruleset and `contributions-lint` do not exist yet (spec §5). Of the classes, only `.grain/**` has a Submit trigger today (`.grain/triggers/grain-declarations.yml`).
9. **Merging.** See `merging.design.md` *Implementation gaps*.
