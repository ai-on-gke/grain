# Grain contribution processes

Attribution and task ownership are defined in [`attribution.design.md`](attribution.design.md).

## The design folder

Each repository has a `design/` folder with five classes of material:

1. **Design documents (`design/design/*.design.md`)** — High-level "1-pager" designs for a specific topic, written in interactive sessions between PMs, engineers, and coding agents, and reviewed by humans. Designs may reference documents in other repositories.
2. **Specifications (`design/spec/...`)** — Detailed normative specifications of design documents, generally one file or folder per design. Specifications are started by a person (with a personal agent or a grain task) and reviewed and edited by humans. They cover APIs, schemas, requirement IDs, and UI mockups.
3. **Tasks (`**/*.task.grain.md`)** — High-level task definitions with initial capability and repository grants (per [`grainfiles.design.md`](grainfiles.design.md)). They are drafted when a person starts task generation, audited and merged by humans, and named `.task.grain.md` so grain imports them automatically once merged.
4. **Playbook designs** — High-level designs for background processes such as releases, qualification, and nightly testing.
5. **Playbooks (`**/*.process.grain.md`, `.grain/`)** — Concrete definitions of standing tasks, triggers, and schedules in grain's declarative formats.

Alternatively, all design material can live in a separate repository rather than in each code repository; both layouts follow the same rules.

## Reviews

- **Design:** Any change to a design is approved by an owner and at least one other human.
- **Specs:** Any change to a specification is reviewed by at least one human and a second entity (human or agent).
- **Playbook designs:** Any change to a playbook design is reviewed by an owner and at least one other human.
- **Playbooks:** Any change to a playbook is reviewed by at least one human and a second entity (human or agent).
- **Code:** Any change to other code is reviewed by at least two entities (two agents, one human and one agent, or two humans).

**Design and specification conformance.** Every review grain performs checks the change explicitly against the governing designs (`design/design/`) and specifications (`design/spec/`), so that code, specs, and designs stay in sync. This covers code PRs (grain-authored and external), spec PRs, task and grainfile PRs, playbook PRs, and security reviews. Specifically, the reviewer must:

- find the designs and specs the change touches, from its links, its modified paths, and the subject area;
- flag any divergence, missing requirement, or untested requirement in inline comments, citing requirement IDs where they exist;
- when a change intentionally departs from a design or spec, block until the design or spec is updated, or a companion `design/` PR is raised and merged first.

A change that no design governs is reviewed against the existing designs it touches, and is flagged if it is big enough to need its own design.

## Process

Wherever an agent opens a PR against `design/` (specifications, tasks, clarifications, or playbooks), it listens to that PR and responds to review comments by pushing updates and replying on each thread. Every agent-opened change is reviewed by a separate entity (never the run that authored it) before merge. Grain also reviews external pull requests opened outside grain, as a required, blocking GitHub check (see [External pull requests](#external-pull-requests)).

Designs, specifications, and tasks can be written any way: with grain (by filing a grain task), with an external agent, by hand, or any mix of these. No path is preferred or required, and every one of them goes through the same *Reviews* rules. Grain never starts specification or task generation automatically; a person starts each step.

### Design

PMs and engineers write or update a design document on a branch—by hand, with an external agent, or with a grain task—and merge the PR once an owner and at least one other human approve it.

### Specification

Merging a design does **not** automatically file a task in grain. A person starts specification generation explicitly, in any of the ways above: with an **external agent** working against the repository (for example through grain's external agent API with a `gma_` token), by hand, or by filing a **grain task** to write `design/spec/...` on a new branch.

The spec PR goes through the *Reviews* rules above (including checking against the design), and the agent that opened it listens to and answers review comments until humans approve and merge it. Whoever starts the step owns the resulting spec PR (for a grain task, that task's owner; see [`attribution.design.md`](attribution.design.md)).

### Tasks

Merging a specification likewise does **not** automatically start task generation. A person starts the step explicitly—with an external agent, by hand, or by filing a grain task—to open a PR adding `.task.grain.md` grainfiles with task descriptions, dependencies, and initial permissions.

The tasks PR goes through the same *Reviews* rules, with the opening agent listening to and answering review comments until humans audit, approve, and merge it. Whoever starts the tasks step owns the tasks PR (for a grain task, that task's owner). Per [`attribution.design.md`](attribution.design.md), the tasks' author (the owner of the tasks PR) owns every task imported from its grainfiles and is the author on every implementation PR grain opens from them, together with any `Contributors:` listed on the tasks PR.

### Implementation

Implementation code reaches the repository by one of two paths:

1. **Grain implementation (from tasks).** People commit the tasks by merging the `.task.grain.md` grainfiles, and grain does the rest: it imports them, dispatches agents to write the code PRs, reviews each PR with a separate agent (including design and spec conformance), runs CI, and merges each PR once it is clean and green.
2. **External pull requests (blocking grain review).** A contributor opens a pull request directly on GitHub, by hand or with an external agent. Grain reviews every head of the PR as a required, blocking GitHub check. Once the review and the other required checks pass, the PR's owner merges it by hand, if they choose, through the repository's normal GitHub flow.

#### Autonomous agent implementation

Grain automatically imports merged task grainfiles into its backlog, watched by the grain on-call. Agents decompose tasks into child tasks as needed while staying within their initial grants (`grainfiles.design.md`) and inheriting their parent's owner (`attribution.design.md`). No human is in the loop during routine implementation and merging: a second agent reviews each PR, and grain merges it once it is clean and green, unless a trust-boundary rule or escalation requires a human. Design ambiguities are raised as clarification PRs against `design/` for humans to review and merge; execution or test blockers go to the grain inbox. Once merged, the grainfile task is marked complete with the task URL as the index into grain's database.

#### External pull requests

Developers iterating interactively and contributors working outside grain open standard GitHub pull requests directly against the repository.

- **Required, blocking GitHub check.** The grain review (`grain/review`) is a required, blocking GitHub check that runs on every new head SHA pushed to the PR.
- **The owner merges it.** Once the grain review and the other required checks pass, the PR's owner merges it by hand, if they choose, through the repository's normal GitHub flow. Together, the grain review and the owner's merge meet the *Reviews* rule for code (at least two entities: the grain agent and a human).
- **Who triggers it.** It runs automatically on PRs from **organization members and repository collaborators**. A PR from anyone else is not reviewed automatically, protecting the model budget. *(Open question: whether a maintainer can trigger it manually on an outside contributor's PR, for example with a label. Recommendation: yes, scoped to the labeled head SHA so new commits require re-triggering.)*
- **How it runs (trust model).**
  - The model never runs inside GitHub Actions. Fork PRs get no repository secrets, and placing a model credential in a workflow job that reads untrusted PR content breaks the trust boundary.
  - Instead, the PR event—through grain's GitHub App, or a thin, secretless Actions workflow calling grain's API—asks grain to review that head SHA. Grain runs the reviewer in its own isolated sandbox, with **no write access** to the PR's branch or repository, and posts the verdict via the GitHub App as a check run plus inline review comments.
  - **Untrusted input.** The PR's diff, files, commits, title, description, and comments are untrusted input to the reviewer, and the reviewer sandbox holds no write, secret, deploy, or task-filing grants that could act on it.
- **What the review checks.** Code quality, tests, security (including the trust-boundary security review for sensitive paths), and divergence from any governing design or spec (*Reviews*).
- **Owner.** An external PR declares its owner on a standalone line in the PR description: `Owner: Name <email>` or `Owner: @handle`, defaulting to the PR author (`@<author>`) when omitted. Grain takes that declaration as fact (`attribution.design.md`).

### Checking

Beyond per-PR reviews checking every change against governing designs and specs (*Reviews*), grain playbooks run regularly to verify landed code end-to-end against `design/design/` and `design/spec/`. Playbooks include human checkpoints where needed, such as reviewing release summaries.

### Loops

Teams track uncaught bugs, review findings, and clarification PRs to refine specs, review prompts, and playbooks for faster landing with fewer errors.

### Bugs

Bugs and feature requests arrive as GitHub issues, external PRs, or tasks filed in grain. A person files or triages an issue to turn it into a grain task or an external PR.

### Playbooks

Playbook designs and playbooks follow the same review rules in `design/` and `.grain/`, with humans authoring overall principles and reviewing standing schedules and triggers.
