# Grain contribution processes

Attribution and task ownership are defined in [`attribution.design.md`](attribution.design.md).

## Principles

**Humans own designs** Humans own designs, including documents, specs, ui mockups, etc. While agents should do most of the writing and revising of these documents, changes to these documents require human review. Designs are the way humans control what is built. Agents may ask to change designs as they write code and update designs, but a human must approve any change.

**Agents own code** Agents own the actual code. Humans may direct agents to write code a specific way, or review code from agents where appropriate, but agents can change code without needing human input. However, they are required to maintain compliance with the designs and specs. All code written by agents is passed through at least one additional agent review to ensure it remains compliant with the repos goals, processes and standards.

**Processes are even more important than code** As with normal software projects, the processes and rules of the repository are even more important than the code itself. Review processes, qualification suites, testing procedures, etc are all designs that humans must curate and approve. The implementation of these processes, as with standard human development, is the way we enforce quality on top of stochastic programming.

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

Wherever an agent opens a PR against `design/` (specifications, tasks, clarifications, or playbooks), it listens to that PR and responds to review comments by pushing updates and replying on each thread. Every agent-opened change is reviewed by a separate entity (never the run that authored it) before merge. External pull requests opened outside grain are gated by a blocking grain review check on GitHub rather than adopted (see [External pull requests](#external-pull-requests)).

### Design

PMs and engineers write or update a design document on a branch with the most appropriate tools, and merge the PR once an owner and at least one other human approve it.

### Specification

A person starts specification generation explicitly, either by hand, with an external agent or through a grain task. The spec PR goes through the *Reviews* rules above (including checking against the design), and the agent that opened it listens to and answers review comments until humans approve and merge it. Whoever starts the step owns the resulting spec PR (for a grain task, that task's owner; see [`attribution.design.md`](attribution.design.md)).

### Implementation

Implementation code reaches the repository by one of two paths:

1. **Autonomous agent implementation (from tasks).** A person creates a set of tasks explicitly—with their personal agent (or by hand) or by filing a grain task—to open a PR adding `.task.grain.md` grainfiles with task descriptions, dependencies, and initial permissions. Grain automatically imports merged `.task.grain.md` files, dispatches agents to write the code PRs, reviews them with a separate agent (including design and spec conformance), and lands them through the grain merge queue once CI is green.

2. **External pull requests (blocking grain review check).** A contributor opens a pull request directly on GitHub, by hand or with an interactive coding agent. A grain agent review is required for all external PRs, and they merge through GitHub's normal flow once required checks pass.

#### Autonomous agent implementation

A person creates a set of tasks explicitly—with their personal agent (or by hand) or by filing a grain task—to open a PR adding `.task.grain.md` grainfiles with task descriptions, dependencies, and initial permissions.
  
The tasks PR goes through the same *Reviews* rules, with the opening agent listening to and answering review comments until humans audit, approve, and merge it. Whoever starts the tasks step owns the tasks PR (for a grain task, that task's owner). Per [`attribution.design.md`](attribution.design.md), the owner of the tasks PR owns every task imported from its grainfiles and every implementation PR those tasks produce.

Grain automatically imports merged task grainfiles into its backlog, watched by the grain on-call. Agents decompose tasks into child tasks as needed while staying within their initial grants (`grainfiles.design.md`) and inheriting their parent's owner (`attribution.design.md`). No human is in the loop during routine implementation and landing: a second agent reviews each PR before the merge queue lands it, unless a trust-boundary rule or escalation requires a human. Design ambiguities are raised as clarification PRs against `design/` for humans to review and merge; execution or test blockers go to the grain inbox. Once landed, the grainfile task is marked complete with the task URL as the index into grain's database.

#### External pull requests

Developers iterating interactively and contributors working outside grain open standard GitHub pull requests directly against the repository.

- **Required, blocking GitHub check.** The grain review (`grain/review`) is a required, blocking GitHub check that runs on every new head SHA pushed to the PR.
- **Who triggers it.** It runs automatically on PRs from **organization members and repository collaborators**. A PR from anyone else is not reviewed automatically, protecting the model budget. External PRs need to be reviewed by an organization member before the grain check can be performed.
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
