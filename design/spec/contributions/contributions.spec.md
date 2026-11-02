# Contributions Specification

**Status:** draft for review. Normative.

**Specifies:** [`contributions.playbook.md`](../../playbook/contributions.playbook.md)

This specification makes the contributions playbook precise. It is a table of paths: every class of material in a repository lives under one glob, every review rule for that class is split into the parts GitHub or grain enforces by path, and every part names the file that enforces it. A reviewer, or `contributions-lint`, reads this document and the files it names side by side and checks that they still agree.

- Ownership and credit (`Owner:`, `Contributors:`, `Co-authored-by:`) are defined in [`attribution.design.md`](../../design/attribution.design.md). This document only specifies the syntax the lint checks (§3.6).
- The grainfile format is defined in [`grainfiles.spec.md`](../grainfiles/grainfiles.spec.md). This document does not restate it (§3.7).
- When a pull request merges is defined in [`merging.design.md`](../../design/merging.design.md). This document defines the reviews that design treats as gates.
- Where the playbook design and this document disagree, the playbook design wins and this document is the bug. Where an enforcing file and this document disagree, this document wins and the enforcing file is the bug; `contributions-lint` reports it (§3.8).

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT", and "MAY" are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

## Contents

1. Terms
2. The path table
3. Format requirements (`contributions-lint`)
4. Applying the ruleset
5. Sources
- Open questions

## 1. Terms

- **Class:** one of the seven kinds of material in §2.1. Every file in the repository is in exactly one class (§2.2).
- **Human:** a person with a GitHub account, acting as themselves. A GitHub App, a bot account, and a token minted for an agent are not humans.
- **Grain agent:** an agent run that grain dispatches. Its review of a change is reported on GitHub as the `grain/review` check (§2.6).
- **Entity:** a human or an agent. The author of a change is an entity; so is each reviewer.
- **Owner (of a design or playbook design):** each human or team named on its `**Owner:**` line (§3.3).
- **Approval:** a GitHub pull request review in the `APPROVED` state on the pull request's current head commit.
- **Enforcing file:** a file in the repository that GitHub or grain reads to enforce a rule part. §5 lists every one.

## 2. The path table

### 2.1 Classes and globs {#CS-1}

2.1.1. The repository has these classes, each at the glob given. Globs are matched against repository-relative paths; `**` matches any number of path segments, including none.

| Class | Glob | Review rule (§2.3) | Enforced by (§2.4) |
|---|---|---|---|
| Design | `design/design/**/*.design.md` | An owner approves, and at least one other human approves. | `.github/CODEOWNERS`, `.github/rulesets/main.json`, `.grain/triggers/design.yml`; second human **unenforced** |
| Spec | `design/spec/<name>/**` | At least one human approves, and at least one other entity (human or agent) reviews. | `.github/CODEOWNERS`, `.github/rulesets/main.json`, `.grain/triggers/spec.yml`, `.grain/templates/review.yml` |
| Playbook design | `design/playbook/*.playbook.md` | An owner approves, and at least one other human approves. | `.github/CODEOWNERS`, `.github/rulesets/main.json`, `.grain/triggers/playbook-design.yml`; second human **unenforced** |
| Tasks | `**/*.task.grain.md` | At least one human approves, and at least one other entity reviews (provisional; see Open questions). | `.github/CODEOWNERS`, `.github/rulesets/main.json`, `.grain/triggers/tasks.yml`, `.grain/templates/review.yml` |
| Playbooks | `**/*.process.grain.md`, `.grain/**` | At least one human approves, and at least one other entity (human or agent) reviews. | `.github/CODEOWNERS`, `.github/rulesets/main.json`, `.grain/triggers/playbooks.yml`, `.grain/templates/review.yml` |
| GitHub enforcement | `.github/**` | At least one human approves, and at least one other entity reviews (provisional; see Open questions). | `.github/CODEOWNERS`, `.github/rulesets/main.json`, `.grain/triggers/github.yml`, `.grain/templates/review.yml` |
| Code | everything else | At least two entities, at least one of which is a grain agent reviewing: an agent author and a grain agent reviewer, or a human author and a grain agent reviewer. | `.github/rulesets/main.json`, `.grain/templates/review.yml`, grain's built-in second-agent review |

2.1.2. In the Spec glob, `<name>` is one path segment matching `^[a-z0-9][a-z0-9-]*$`. A spec folder holds one or more `*.spec.md` files (§3.2) and any assets that belong to them, such as schemas, quick references, and mockups.

2.1.3. Non-Markdown files under `design/design/` or `design/playbook/` (images, diagrams) are assets of the designs beside them. They are in the Spec class for review purposes: their CODEOWNERS entry is the folder-level entry of §2.5.4. A Markdown file there without its class suffix is a lint error (CL002).

2.1.4. `contributions-lint` MUST read the class list, globs, and enforcing-file names from the table in §2.1.1 of this document (by its column headings) rather than from a copy, so that changing this table changes what the lint expects (§3.8).

### 2.2 Classification and overlaps {#CS-2}

2.2.1. A file that matches more than one glob in §2.1.1 is in the class that comes first in this precedence order:

1. GitHub enforcement (`.github/**`)
2. Playbooks (`**/*.process.grain.md`, `.grain/**`)
3. Tasks (`**/*.task.grain.md`)
4. Design (`design/design/**/*.design.md`)
5. Playbook design (`design/playbook/*.playbook.md`)
6. Spec (`design/spec/<name>/**`)
7. Code

So a `.task.grain.md` file under `design/spec/<name>/` is a task, and `.grain/workflows/tests.yml` is a playbook, not code.

2.2.2. Precedence is needed because GitHub applies only the last matching `CODEOWNERS` pattern to a file. `.github/CODEOWNERS` MUST order its entries so the precedence of §2.2.1 holds: later classes in the list above come earlier in the file (§2.5.2).

2.2.3. **The Code rule is a floor for every class.** `grain/review` is a required check on every pull request (§2.6), so every change, whatever its class, also gets a grain agent's review. A `.grain/` file that is executable (a workflow, a template) therefore gets both the Playbooks rule and the Code rule's grain agent review, without a second classification.

2.2.4. A pull request that touches files in several classes MUST satisfy the rule of every class it touches. GitHub does this by requiring a code-owner approval for each touched file (§2.6.2). Grain does it by holding the change under every gating trigger that matches, under the strictest `human` among them (`docs/declarations.md`, "`.grain/triggers/<name>.yml`").

2.2.5. The playbook design's sixth class, **agent designs**, has no path yet. Until this document gives it a glob, agent-design files are in whichever class their path puts them in, normally Code (see Open questions).

### 2.3 Review rules {#CS-3}

Each rule is taken from the *Reviews* section of the playbook design. Counts are minimums. The author of a change never counts as one of its approvers, and the run that authored a change never counts as its agent reviewer.

2.3.1. **Design.** A change to a file in the Design class MUST be approved by at least one owner of that design (one human named, directly or through a team, on its `**Owner:**` line, §3.3) and by at least one other human. Two approvals by distinct humans, at least one an owner.

2.3.2. **Spec.** A change to a file in the Spec class MUST be approved by at least one human, and reviewed by at least one other entity, human or agent. One human approval plus one further review.

2.3.3. **Playbook design.** As §2.3.1, for the Playbook design class: two approvals by distinct humans, at least one an owner of the playbook design.

2.3.4. **Tasks.** A change to a file in the Tasks class MUST be approved by at least one human and reviewed by at least one other entity. The playbook design says only that tasks PRs are "audited and merged by humans" and go "through the same *Reviews* rules"; the count here is provisional (see Open questions).

2.3.5. **Playbooks.** A change to a file in the Playbooks class MUST be approved by at least one human, and reviewed by at least one other entity, human or agent.

2.3.6. **GitHub enforcement.** A change to a file in the GitHub enforcement class MUST be approved by at least one human and reviewed by at least one other entity. The playbook design names the class but states no rule for it; this rule follows from its principle that review processes are designs humans curate and approve, and is provisional (see Open questions).

2.3.7. **Code.** A change to a file in the Code class MUST be authored or reviewed by at least two entities, and at least one of the reviewers MUST be a grain agent other than the run that authored it. No human approval is required.

2.3.8. **Conformance.** Every review grain performs MUST check the change against the designs and specs that govern its paths, as the playbook design's *Design and specification conformance* paragraph requires.

### 2.4 Enforcement map {#CS-4}

2.4.1. Each rule of §2.3 is split into parts. Each part is enforced by exactly the file named here, or is marked **unenforced** with what would enforce it.

| Rule part | Classes | System | Enforcing file | Status |
|---|---|---|---|---|
| One approval by a human listed for the path | Spec, Tasks, Playbooks, GitHub enforcement | GitHub | `.github/CODEOWNERS` (who), `.github/rulesets/main.json` (`require_code_owner_review`) | enforced |
| One approval by an owner of the design | Design, Playbook design | GitHub | `.github/CODEOWNERS` (per-file entry naming the `**Owner:**` line), `.github/rulesets/main.json` | enforced |
| The `**Owner:**` line and the CODEOWNERS entry name the same owners | Design, Playbook design | GitHub | `.github/workflows/contributions-lint.yml` (CL005) | enforced |
| A second, distinct human approval | Design, Playbook design | — | — | **unenforced** (§2.9) |
| A grain agent review that is not the author | all | GitHub | `.github/rulesets/main.json` (required check `grain/review`) | enforced on external PRs; on grain-opened PRs once §2.6.4 holds |
| A grain agent review that is not the author, on a change grain drives | all | grain | built into grain (the second-agent review gate, `merging.design.md` *Gates*) | enforced; no repository file |
| The review checks conformance with governing designs and specs | all | grain | `.grain/templates/review.yml` | enforced as instructions to the reviewer; not mechanically checkable |
| A person releases a change grain drives before it merges | Design, Spec, Playbook design, Tasks, Playbooks, GitHub enforcement | grain | `.grain/triggers/<class>.yml` (§2.7) | enforced for changes grain drives |
| Format and drift | all | GitHub | `.github/workflows/contributions-lint.yml`, required by `.github/rulesets/main.json` | enforced |

2.4.2. GitHub's parts apply to every pull request against `main`, whoever opened it. Grain's trigger parts apply only to changes grain drives to a merge (`docs/declarations.md`: a trigger judges "a change grain dispatched"); an external pull request merged by its owner through GitHub is held by the GitHub parts alone.

2.4.3. GitHub's required approval count (`required_approving_review_count`) applies to the whole branch, not to a path. It MUST be `0` in `.github/rulesets/main.json`, because the Code rule needs no human approval; the human approvals of §2.3 come from code-owner review, which is per path.

### 2.5 `.github/CODEOWNERS` {#CS-5}

2.5.1. `.github/CODEOWNERS` MUST exist and MUST have an entry covering every file in the Design, Spec, Playbook design, Tasks, Playbooks, and GitHub enforcement classes. It MUST NOT have an entry that covers a file in the Code class only.

2.5.2. Entries MUST appear in this order, so that the last matching pattern gives the precedence of §2.2.1:

1. folder-level entries for `/design/design/` and `/design/playbook/` (assets, §2.1.3);
2. Spec: `/design/spec/` or one entry per `/design/spec/<name>/`;
3. Playbook design: one entry per `design/playbook/*.playbook.md` file;
4. Design: one entry per `design/design/**/*.design.md` file;
5. Tasks: `*.task.grain.md`;
6. Playbooks: `*.process.grain.md` and `/.grain/`;
7. GitHub enforcement: `/.github/`.

2.5.3. The per-file entry for each design and playbook design MUST name exactly the owners on that file's `**Owner:**` line (§3.3), in any order (CL005).

2.5.4. Every other entry MUST name at least one human or team, and MUST NOT name only bots or apps. The owners of the design a spec specifies SHOULD be named on that spec folder's entry.

2.5.5. `/.github/CODEOWNERS` itself is covered by the `/.github/` entry, so changing who owns a path needs a GitHub-enforcement approval.

### 2.6 `.github/rulesets/main.json` and the required checks {#CS-6}

2.6.1. `.github/rulesets/main.json` MUST be a GitHub repository ruleset in the shape the REST API's "Create a repository ruleset" endpoint accepts, with `"name": "main"`, `"target": "branch"`, `"enforcement": "active"`, and `conditions.ref_name.include` exactly `["refs/heads/main"]`.

2.6.2. It MUST contain a `pull_request` rule with these parameters:

| Parameter | Value | Why |
|---|---|---|
| `required_approving_review_count` | `0` | §2.4.3 |
| `require_code_owner_review` | `true` | §2.4.1, the human parts |
| `dismiss_stale_reviews_on_push` | `true` | an approval holds for the head it was given at, as grain's Submit does |
| `require_last_push_approval` | `true` | the human who pushed last cannot be the one approving |

2.6.3. It MUST contain a `required_status_checks` rule whose `required_status_checks` include contexts `grain/review` and `contributions-lint`, with `strict_required_status_checks_policy` `false` (`merging.design.md`: what landed on the base since a pull request was tested does not matter). It MAY require other checks. The `grain/review` entry SHOULD set `integration_id` to the grain GitHub App installed on the repository, so no other app can post that context.

2.6.4. `grain/review` MUST be posted on every head of every pull request against `main`, including pull requests grain opens itself, and MUST succeed only once grain's second-agent review of that head has passed. (Today grain posts it only on pull requests it did not open, `pkg/orchestrator/githubevents.go`; until that changes, requiring it blocks grain's own merges, so the change that posts it MUST land before the ruleset does.)

2.6.5. `bypass_actors` MUST be empty, unless an Open question is decided otherwise.

### 2.7 `.grain/triggers/<class>.yml` {#CS-7}

2.7.1. For each class other than Code there is one trigger file, named:

| Class | Trigger file |
|---|---|
| Design | `.grain/triggers/design.yml` |
| Spec | `.grain/triggers/spec.yml` |
| Playbook design | `.grain/triggers/playbook-design.yml` |
| Tasks | `.grain/triggers/tasks.yml` |
| Playbooks | `.grain/triggers/playbooks.yml` |
| GitHub enforcement | `.grain/triggers/github.yml` |

2.7.2. Each MUST be modelled on `.grain/triggers/grain-declarations.yml` and MUST contain these keys, with no `per`, `ignore`, `branches`, or `auto-approve`:

```yaml
on: pull_request
paths: [<the class's globs from §2.1.1>]
gate: true
sensitive: true
human: required
template: none
approvers: [<optional>]
```

- `paths` MUST equal the class's globs (with `<name>` written `*`). For Code, which has no glob, there is no file.
- `template: none` because grain's second-agent review already reviews every change with `.grain/templates/review.yml` (§2.8); a trigger that filed another review would review the change twice.
- `approvers` MAY name the grain teams or people the repository means to release the class, in grain's approver syntax. It records intent only: a single-tenant grain lets any person's Submit release every trigger (`docs/declarations.md`, *Sensitive paths*). The lint does not check it.

2.7.3. `.grain/triggers/playbooks.yml` supersedes `.grain/triggers/grain-declarations.yml`, which covers a subset of its paths with the same settings; the change that adds `playbooks.yml` MUST remove `grain-declarations.yml`.

2.7.4. Other trigger files (for example `.grain/triggers/release-promotion.yml`) MAY exist. They only add holds and are outside §2.1.

2.7.5. A trigger's `human: required` is one person's Submit. It cannot require a count of distinct people (§2.9).

### 2.8 `.grain/templates/review.yml` {#CS-8}

2.8.1. The repository MUST declare its default review template at `.grain/templates/review.yml` (`docs/declarations.md`, "`.grain/review.yml`"). Grain attaches it to every review of a change in the repository.

2.8.2. Its prompt MUST instruct the reviewer to:

- classify each changed path by §2.1 and §2.2;
- find the designs (`design/design/`, `design/playbook/`) and specs (`design/spec/`) that govern each path, from the change's links, its paths, and its subject;
- flag each divergence, missing requirement, or untested requirement as an inline comment citing requirement IDs where they exist;
- block a change that intentionally departs from a design or spec until the design or spec is updated in the same change, or a companion `design/` change has merged first;
- flag a change that no design governs and that is big enough to need one.

2.8.3. Its prompt MUST cite this document by path, so `contributions-lint` can check that it exists and points here (CL020).

2.8.4. The pull request's diff, files, commits, title, description, and comments are untrusted input to the reviewer; the template MUST NOT ask for capabilities that let the review write to the pull request's branch, read secrets, deploy, or file tasks (playbook design, *External pull requests*).

### 2.9 Unenforced parts {#CS-9}

2.9.1. **The second, distinct human on designs and playbook designs** is unenforced. GitHub's approval count is per branch (§2.4.3), and a grain trigger's `human: required` is one Submit by whoever holds the deployment. Either of these would enforce it:

- a review-count check in `contributions-lint`, run on `pull_request_review` events too, that counts distinct human approvals on the current head of a pull request touching a Design or Playbook design file and requires two, one of them an owner; or
- an `approvals: <n>` field on grain path triggers counting distinct verified people, which needs per-person identity in grain (`authentication.design.md`).

2.9.2. **That the reviewer actually checked conformance** (§2.3.8) cannot be checked mechanically. What is enforced is that the review template tells it to (§2.8).

## 3. Format requirements (`contributions-lint`)

### 3.1 The workflow {#CS-10}

3.1.1. `.github/workflows/contributions-lint.yml` MUST define a job whose check context is `contributions-lint`, run on `pull_request` (types `opened`, `edited`, `synchronize`, `reopened`) against `main`, and on `push` to `main`.

3.1.2. It MUST run with `permissions: contents: read` and no secrets, so it runs the same on a fork's pull request. It MUST check out the full history (`fetch-depth: 0`) for §3.5.4. It MUST NOT interpolate `${{ github.event.pull_request.* }}` expressions into a `run:` script; it reads the pull request description from `$GITHUB_EVENT_PATH`.

3.1.3. Each finding has a code from §3.9, a severity, a path, and a message. On a pull request, a finding of severity *error* on a path the pull request changes, and every drift (§3.8) and identity-uniqueness (§3.5.3) finding, fails the check; an error on a path the pull request does not change is reported as a warning. On `push` to `main` every finding is reported.

3.1.4. The linter SHOULD be runnable locally on a working tree, as `grain declarations check` is.

### 3.2 File naming and location {#CS-11}

3.2.1. Class suffixes and the folders they belong in:

| Suffix | Allowed location |
|---|---|
| `*.design.md` | `design/design/**` |
| `*.playbook.md` | `design/playbook/` (directly) |
| `*.spec.md` | `design/spec/<name>/**` |
| `*.task.grain.md` | anywhere (`grainfiles.spec.md` §4.2) |
| `*.process.grain.md` | anywhere (`grainfiles.spec.md` §4.2) |

3.2.2. A file with the suffix `.design.md`, `.playbook.md`, or `.spec.md` outside its allowed location is an error (CL001).

3.2.3. A Markdown file (`*.md`) under `design/design/` without the `.design.md` suffix, or directly under `design/playbook/` without the `.playbook.md` suffix, is an error (CL002). A file directly under `design/` is an error (CL002).

3.2.4. Every `design/spec/<name>/` folder MUST contain at least one `*.spec.md` file, and `<name>` MUST match §2.1.2 (CL003). Other files in the folder are assets of its specs.

3.2.5. Grainfile names follow `grainfiles.spec.md` §4.1 (§3.7).

### 3.3 Design and playbook design headers {#CS-12}

3.3.1. Every `*.design.md` and `*.playbook.md` file MUST have, after its title and before its first `## ` heading:

- an owner line: `**Owner:** ` followed by one or more owners, separated by `, `, each a GitHub `@handle` or `@org/team` (CL004);
- a core-rule paragraph beginning `**The core rule:**` (CL006).

```abnf
owner-line = "**Owner:**" 1*WSP owner *( "," 1*WSP owner )
owner      = "@" login [ "/" team ]
login      = ( ALPHA / DIGIT ) *38( ALPHA / DIGIT / "-" )
team       = 1*( ALPHA / DIGIT / "-" / "_" )
```

3.3.2. The owners on the line MUST equal the owners on the file's `.github/CODEOWNERS` entry (CL005, §2.5.3).

### 3.4 Spec headers {#CS-13}

3.4.1. Every `*.spec.md` file MUST have, before its first `## ` heading, a line beginning `**Specifies:**` followed by one or more Markdown links, each to a `*.design.md` or `*.playbook.md` file that exists in the repository or a link to a design in another repository (CL007).

3.4.2. Every `*.spec.md` file MUST carry at least one requirement ID (§3.5) on a heading (CL008).

### 3.5 Requirement IDs {#CS-14}

3.5.1. A requirement ID is written at the end of a Markdown heading's text:

```abnf
requirement-id = "{#" prefix "-" number "}"
prefix         = UPPER *7( UPPER / DIGIT )
number         = %x31-39 *DIGIT          ; 1 or more, no leading zero
UPPER          = %x41-5A
```

For example `### 2.1 Classes and globs {#CS-1}`. A `{#...}` token on a heading in a design or spec that does not match the grammar is an error (CL009).

3.5.2. A prefix SHOULD be used by one document only, so that uniqueness can be kept by reading one file.

3.5.3. An ID MUST be unique across every Markdown file under `design/` (CL010).

3.5.4. An ID is never reused. An ID added by a change (present at the head, absent at the base) MUST NOT appear in any earlier revision of a file under `design/` on the base branch (`git log -S'{#<ID>}' -- design/`), nor in any `Design-Item: <doc>#<ID>` or `Design-Removed: <doc>#<ID>` commit trailer on the base branch (CL011). The trailer convention is the one `.grain/templates/design-planner.yml` and `docs/design-docs.md` define.

3.5.5. Designs MAY carry requirement IDs; when they do, §3.5.1 to §3.5.4 apply to them too.

### 3.6 PR description lines {#CS-15}

The lint reads the pull request description; lines inside fenced code blocks are ignored. Identities are those of [`attribution.design.md`](../../design/attribution.design.md) *Identity*: a user identity as in `grainfiles.spec.md` §6.2.1, or `@` followed by a GitHub `login` (§3.3.1).

```abnf
owner-pr-line     = "Owner:" 1*WSP identity
contributors      = contributors-head LF 1*( contributor-item LF )
contributors-head = "Contributors:" / "### Contributors"
contributor-item  = ( "- " / "* " ) identity
identity          = user-identity / "@" login
```

3.6.1. **`Owner:`** A description MAY have at most one standalone `Owner:` line (CL012). When present it MUST match `owner-pr-line` (CL012). When absent, the owner is the pull request's author (`attribution.design.md`); that is not a finding.

3.6.2. **`Contributors:`** On a pull request that adds or changes a `*.task.grain.md` file, a `Contributors:` block, when present, MUST match `contributors`: the heading is followed immediately by item lines, one identity each, and ends at the next blank line or heading (CL013). An identity MUST NOT be repeated (CL013).

3.6.3. A `Contributors:` block on a pull request that changes no `*.task.grain.md` file is a warning (CL014): nothing reads it there.

3.6.4. The lint checks syntax only. Resolving `@handle` entries and freezing the list at merge are grain's (`attribution.design.md`).

### 3.7 Grainfiles {#CS-16}

3.7.1. Every `*.task.grain.md` and `*.process.grain.md` file, and every grainfile template, MUST pass the lint rules of `grainfiles.spec.md` §10 and validate against `design/spec/grainfiles/schemas/*.json`. Each `GF` finding is reported as CL015 carrying its `GF` code, with `grainfiles.spec.md` §10.2's severity.

3.7.2. Files under `.grain/` other than grainfiles MUST pass `grain declarations check` (CL021).

### 3.8 Drift {#CS-17}

3.8.1. The lint MUST check that the enforcing files agree with §2, reading §2.1.1 as §2.1.4 requires:

- `.github/CODEOWNERS` has the entries, order, and owners of §2.5 (CL016);
- `.github/rulesets/main.json` has the rules and parameters of §2.6 (CL017);
- each `.grain/triggers/<class>.yml` of §2.7.1 exists with the contents of §2.7.2, and `paths` equal to the class's globs (CL018);
- `.grain/templates/review.yml` exists and cites this document (CL020).

3.8.2. Because CODEOWNERS, the ruleset file, the trigger files, and this document are each in a class that needs a human approval, a change to any of them is seen by a person, and a change to one without the others fails `contributions-lint`.

### 3.9 Lint codes {#CS-18}

| Code | Severity | Condition |
|---|---|---|
| CL001 | error | A class suffix outside its allowed location (§3.2.2). |
| CL002 | error | A Markdown file in a design folder without its class suffix, or a file directly under `design/` (§3.2.3). |
| CL003 | error | A spec folder with no `*.spec.md`, or a folder name outside §2.1.2 (§3.2.4). |
| CL004 | error | A design or playbook design without a valid `**Owner:**` line (§3.3.1). |
| CL005 | error | `**Owner:**` and the CODEOWNERS entry name different owners (§3.3.2). |
| CL006 | error | A design or playbook design without a `**The core rule:**` paragraph before its first section (§3.3.1). |
| CL007 | error | A spec without a `**Specifies:**` line, or one whose link does not resolve (§3.4.1). |
| CL008 | error | A spec with no requirement ID (§3.4.2). |
| CL009 | error | A malformed requirement ID (§3.5.1). |
| CL010 | error | A duplicate requirement ID (§3.5.3). |
| CL011 | error | A reused requirement ID (§3.5.4). |
| CL012 | error | More than one `Owner:` line, or a malformed one (§3.6.1). |
| CL013 | error | A malformed `Contributors:` block on a tasks PR (§3.6.2). |
| CL014 | warning | A `Contributors:` block on a PR that is not a tasks PR (§3.6.3). |
| CL015 | per `GF` code | A grainfile lint finding (§3.7.1). |
| CL016 | error | `.github/CODEOWNERS` disagrees with §2.5. |
| CL017 | error | `.github/rulesets/main.json` disagrees with §2.6. |
| CL018 | error | A `.grain/triggers/<class>.yml` file is missing or disagrees with §2.7. |
| CL019 | error | The live ruleset differs from `.github/rulesets/main.json` (§4.3). |
| CL020 | error | `.grain/templates/review.yml` is missing or does not cite this document (§2.8.3). |
| CL021 | error | `grain declarations check` refuses a `.grain/` file (§3.7.2). |

## 4. Applying the ruleset

### 4.1 Source of truth {#CS-19}

4.1.1. GitHub reads rulesets from its settings and API, not from the repository. `.github/rulesets/main.json` is the source of truth for the `main` ruleset; the live ruleset is a copy of it. A change to branch protection on `main` is made by changing the file, and is reviewed under the GitHub enforcement rule (§2.3.6).

4.1.2. The repository MUST NOT also have branch protection rules on `main` outside the ruleset; they would be enforcement the file does not show.

### 4.2 The apply workflow {#CS-20}

4.2.1. `.github/workflows/apply-ruleset.yml` MUST apply the file on `push` to `main` that changes `.github/rulesets/main.json`, and on `workflow_dispatch`. It finds the live ruleset named `main` (`GET /repos/{owner}/{repo}/rulesets`) and replaces it (`PUT /repos/{owner}/{repo}/rulesets/{id}`), or creates it (`POST /repos/{owner}/{repo}/rulesets`) if there is none.

4.2.2. It MUST NOT run on `pull_request` or `pull_request_target`: only a file that has passed review is applied.

4.2.3. **Credential.** Writing a ruleset needs the repository permission *Administration: write*, which a workflow's `GITHUB_TOKEN` cannot be given. The workflow MUST use a GitHub App installation token, or a fine-grained personal access token, scoped to this one repository with *Administration: write* and nothing else, held as an Actions secret in a deployment environment that only the `main` branch may use.

4.2.4. Alternatively, grain MAY apply the file instead of a workflow, through a call a person grants for that one application (a held action a person approves), using a GitHub App installation that holds *Administration: write*. Which of the two a repository uses is an Open question; exactly one MUST be in use.

### 4.3 Live drift {#CS-21}

4.3.1. `contributions-lint` MUST read the live rules on `main` (`GET /repos/{owner}/{repo}/rules/branches/main`, which its read-only token can call) and report CL019 when they differ from the **base** revision of `.github/rulesets/main.json`. It compares against the base, not the head, because a pull request that changes the file is not yet applied.

4.3.2. `bypass_actors` is visible only to a caller with *Administration: read*, so the lint cannot see it; the apply workflow (§4.2) MUST compare it as well, and fail when the live ruleset has bypass actors the file does not.

## 5. Sources

Every enforcing file this document names. "To create" and "to change" are follow-up work once this document is approved.

| File | Enforces | State |
|---|---|---|
| `.github/CODEOWNERS` | §2.5 | to create |
| `.github/rulesets/main.json` | §2.6 | to create |
| `.github/workflows/contributions-lint.yml` | §3 | to create |
| `.github/workflows/apply-ruleset.yml` | §4.2 | to create (or §4.2.4) |
| `.grain/triggers/design.yml`, `spec.yml`, `playbook-design.yml`, `tasks.yml`, `playbooks.yml`, `github.yml` | §2.7 | to create |
| `.grain/triggers/grain-declarations.yml` | §2.7.3 | to remove, superseded by `playbooks.yml` |
| `.grain/templates/review.yml` | §2.8 | to create |
| `pkg/orchestrator/githubevents.go` | §2.6.4 (`grain/review` on grain-opened PRs) | to change |
| `pkg/repodecl/triggers.go` | the trigger format §2.7 relies on | exists |

## Open questions

1. **Second human.** Which of §2.9.1's mechanisms enforces the second distinct human on designs and playbook designs: a review count in `contributions-lint`, or an `approvals:` count on grain triggers?
2. **An owner as author.** When an owner of a design opens the pull request themselves, does authoring count as the owner's approval, so that one other human suffices? GitHub does not let an author approve their own pull request, so as written a second owner, or a team, must approve.
3. **Tasks rule.** The playbook design gives tasks PRs no count. Is §2.3.4's "one human plus one other entity" right, or do tasks need two humans?
4. **GitHub enforcement rule.** The playbook design names `.github/**` as a class but gives it no rule. Is §2.3.6's "one human plus one other entity" right, given that `.github/` holds the files that enforce every other rule?
5. **Agent designs.** What glob holds the playbook design's sixth class, agent designs, and what enforces its "at least two agents" rule?
6. **Who is listed in CODEOWNERS** for specs, tasks, playbooks, `.github/`, and design-folder assets: a maintainers team, the owners of the governing design (§2.5.4's SHOULD), or something else?
7. **Grain's Submit and GitHub's approval.** For a change grain drives in a class with a human rule, a person both approves on GitHub (the code-owner review) and presses Submit in grain (the trigger). Should a code-owner approval of the current head release the grain hold instead?
8. **Applying the ruleset.** Workflow with an *Administration: write* token (§4.2.3), or grain through a person-granted call (§4.2.4)? Should the live ruleset also be re-applied, or only checked, on a schedule between merges?
9. **Existing files.** Most designs on `main` today have no `**Owner:**` line or core rule, the playbook design has no core rule, and `grainfiles.spec.md` carries no `{#...}` IDs. Who assigns owners to the existing designs, and must the tree be clean before `contributions-lint` becomes a required check, or does §3.1.3's changed-paths rule carry it until then?
10. **Separate design repository.** The playbook design allows all design material to live in its own repository. How do §2.5 to §2.7 apply there: does the design repository carry its own ruleset and triggers, and does code review in the code repository resolve governing designs across repositories?
