# Attribution: crediting the people behind a change

[`contributions.design.md`](contributions.design.md) defines how work flows through a repository: human-authored designs, specifications and task grainfiles started by a person and reviewed by humans, and implementation PRs either written autonomously by grain agents or opened externally on GitHub and gated by a blocking grain review check. This design defines who owns each task and pull request and who receives authorship credit on PR descriptions and merged commits, alongside operational actor tracking in [`operator-attribution.design.md`](operator-attribution.design.md).

**The core rule:** every grain task has a single **owner**, fixed when the task is filed and inherited by every child task it spawns. How a pull request is credited depends on whether grain opened it or a contributor opened it externally:

- **Internally generated PRs** (opened by grain agents: code PRs and agent-opened spec, tasks, clarification, and playbook PRs) take their owner from the task's owner. On a grain-implemented code PR, grain credits the task's owner plus—when the task came from a `.task.grain.md` grainfile—any additional people listed in an explicit `Contributors:` section on the **tasks PR** that merged that grainfile. Grain shows them in the PR description and writes them as `Co-authored-by:` trailers on the merge commit, alongside `Requested-by:` and `Approved-by:` (`operator-attribution.design.md`). Nothing else is derived: not design or spec PR reviewers, and not the implementation PR's own agent commits.
- **External PRs** (opened outside grain, including external code PRs and human-opened design, spec, and tasks PRs) take their credit from the PR's declared `Owner:` line (defaulting to the GitHub PR author), plus its human commit authors and `Co-authored-by:` trailers. Grain takes that attribution as stated and neither adopts external PRs nor rewrites their merge commits.

## Identity

An owner or contributor identity is a Git identity, `Name <email>`, matching `Co-authored-by:` trailers and the grainfile `requestor` field (`design/spec/grainfiles/grainfiles.spec.md` §6.2). GitHub noreply addresses (`<id>+<login>@users.noreply.github.com`) work for people who keep their email private, and grain resolves `@handle` to that form. A bare `<email>` from verified sign-in (`operator-attribution.design.md`) formats with the user's configured display name when known, or `<local-part> <email>`.

Agents are never listed as owners or contributors; grain records agent provenance in the bot commit author and `Origin:` / `Filed-by:` trailers. The unverified `operator` fallback (`operator-attribution.design.md`) is never a person and never receives a `Co-authored-by:` trailer.

## How this connects to `operator-attribution.design.md`

[`operator-attribution.design.md`](operator-attribution.design.md) records *which runtime caller acted in grain* (`ui.RequestActor(ctx)` and `OnBehalfOf`), whereas this document defines *task ownership and Git authorship credit*. They meet at filing and approval:

- **Filing (`Requested-by:` and root `owner`):** the verified person who files a task directly—or on whose behalf a session or `gma_` token files it—becomes `Task.Creator` (`Requested-by: <email>`) and the root task's `owner`.
- **Approval (`Approved-by:`):** the verified person who approves a task or submits a held change becomes its `Approved-by: <email>` trailer without changing `owner` or `Co-authored-by:`.

| PR kind | `Owner:` | `Co-authored-by:` on merged commit | Operational trailers |
| --- | --- | --- | --- |
| **Grain code PR (from `.task.grain.md`)** | Tasks PR owner (inherited by child tasks) | Task owner + tasks PR `Contributors:` list (read when tasks PR merged) | `Requested-by:`, `Approved-by:`, `Origin:`, `Filed-by:` |
| **Grain code PR (UI/API/CLI, session, or schedule)** | Task owner (root or inherited) | Task owner (when a verified person, omitted if `operator`) | `Requested-by:`, `Approved-by:`, `Origin:`, `Filed-by:` |
| **Grain design-folder PR (spec, tasks, clarification, playbook)** | Task owner (editable in PR description by humans before merge) | Task owner + humans credited on commits answering their review comments | `Requested-by:`, `Approved-by:`, `Origin:`, `Filed-by:` |
| **External PR (code, or human design/spec/tasks)** | `Owner:` in PR description, defaulting to PR author (or `gma_` token owner) | Human commit authors and `Co-authored-by:` trailers as merged on GitHub | Whatever trailers the author includes |

## Task ownership, inheritance, and schedules

### Root task owners

When a root task (one with no parent task) is filed, its `owner` is set once from its source:

1. **Imported from a `.task.grain.md` grainfile:** the owner of the tasks PR that merged that grainfile (`Owner:` in the tasks PR description, defaulting to the PR's GitHub author; if an agent opened the tasks PR without an explicit `Owner:`, the owner of the grain task or `gma_` token that opened it; if pushed directly without a PR, the commit author).
2. **Filed by a person in the UI, API, or CLI:** that verified person (`RequestActor(ctx)`).
3. **Filed by a session (director or console) or external token (`gma_`):** the verified person the session or token acts on behalf of (`OnBehalfOf`—the verified sender of the message the session turn answers, or the verified person who minted the token).
4. **Filed by a schedule (or repository trigger):** the schedule's (or trigger's) `owner`, resolved when it fires.
5. **Unverified fallback (`operator`):** used only when nobody verified is known—access-code, loopback, or in-process calls without verified identity, session turns without a verified sender, or an `oncall` schedule when `grain_config.oncall` is unset.

External PRs are not tracked as tasks; their owner is their declared `Owner:` line in the PR description, defaulting to the GitHub PR author.

### Inheritance down task trees

Every task filed by another task—through `propose_task`, decomposition, follow-ups, repairs, review or security-review tasks, or ports—**inherits its parent task's `owner`**. A grandchild inherits from its parent, and so on down the chain. Sub-tasks decomposed from a grainfile task also inherit its link to the tasks PR and that PR's `Contributors:` list.

The owner is fixed when the task is filed and is never re-derived later. A verified person with task-edit access can reassign a task's owner in the UI, API, or CLI (recording an edit event naming the actor); future child tasks inherit the updated owner, while already-filed child tasks keep theirs unless edited too. Agents cannot set or edit task ownership.

### Schedule owners and `oncall`

Schedules (`settings/schedules.yml`, the `schedule` table, and `.grain/schedules/*.yml`) carry an `owner` field. Templates, suites, and triggers may declare an optional default `owner`; when a schedule fires a template or suite, the schedule's `owner` wins (falling back to the suite or template `owner` if unset, then `oncall`). Its value is either a specific person (`Name <email>`, `<email>`, or `@handle`) or the keyword **`oncall`** (the default on schedules).

**The `oncall` setting.** `grain_config.oncall` (`tables/grain_config.json`, Settings → General) holds a single person (`Name <email>`, `<email>`, or `@handle`) naming the deployment's current on-call. It is set directly by a person (or via a `grain/config` PR); rotation schedules and external paging integrations are out of scope. When a schedule with `owner: oncall` fires, grain resolves `grain_config.oncall` at filing time and stores that person as the task's `owner`, so changing `oncall` later does not re-attribute tasks already filed. With no `oncall` configured, `oncall` falls back to `operator`.

## PR conventions: `Owner:` and `Contributors:`

Design docs, specs, and `.task.grain.md` files carry **no contributor lists** in their text (the optional `requestor` field on `.task.grain.md` remains independent for portable grainfile metadata). Attribution lives in PR descriptions and on task records:

- **`Owner:` line (all PRs).** A standalone line in the PR description outside fenced code blocks: `Owner: Jane Doe <jane@example.com>` or `Owner: @janedoe` (at most one). On external PRs and human-opened design-folder PRs, `Owner:` is optional, defaults to the GitHub PR author (`@<author>`, or the `gma_` token's verified owner), and is taken as stated. On grain-opened PRs, grain writes `Owner: <task owner>`; humans with write access may edit `Owner:` in the description of an agent-opened spec or tasks PR before merge, while on a grain code PR it mirrors the task's owner.
- **`Contributors:` block (tasks PRs only).** Additional `Co-authored-by:` contributors for grainfile implementation PRs come solely from an explicit block in the **tasks PR description** (outside fenced code blocks):
  ```markdown
  Owner: @alice
  Contributors:
  - Bob Smith <bob@example.com>
  - @carol
  ```
  `Contributors:` (or `### Contributors`) is followed immediately by `- ` or `* ` lines, one `Name <email>` or `@handle` per line, ending at the next blank line or heading. The tasks PR author and any human with repository write access may edit it before merge; agents never add themselves and edit the list only when carrying out an explicit human comment on that PR. Grain reads `Owner:` and `Contributors:` **at merge time** when importing `.task.grain.md` files, resolves `@handle` entries, links each imported task to the tasks PR, and freezes the owner and contributor list on the task. Every implementation PR from those tasks (or their children) shows the list in its description and writes the owner and contributors (deduplicated by email, owner first) as `Co-authored-by:` trailers on the merge commit.
- **Design-folder review comments.** When an agent listening on an open design, spec, tasks, clarification, or playbook PR pushes a commit carrying out a human's review comment, that commit carries `Co-authored-by:` for the commenter, crediting them on that document PR itself without propagating downstream.

## Examples

- **Grainfile feature.** Alice drafts a design with Bob and opens a tasks PR with `Owner: @alice` and `Contributors:` listing `Bob Smith <bob@example.com>`. At merge, grain imports `auth.task.grain.md` with owner Alice, contributor Bob, and the tasks PR link. A sub-task split out via `propose_task` inherits both; both code PRs merge with `Co-authored-by:` trailers for Alice and Bob.
- **Session and schedule tasks.** Dave asks the director session to fix a race; the session files a task on behalf of `dave@example.com`, setting Dave as `owner` and `Requested-by:`, and its merge commit credits Dave via `Co-authored-by:`. Separately, a nightly schedule with `owner: oncall` fires while `grain_config.oncall` is `eve@example.com`; that task and any repair task it proposes keep owner Eve even after `oncall` changes.
- **External PR.** Frank opens a PR with a `Co-authored-by: Grace Hopper <grace@example.com>` commit trailer and no `Owner:` line. Grain runs the blocking `grain/review` check (`Owner:` defaults to `@frank`) without adopting the PR, and GitHub merges it with Frank and Grace's attribution intact.

## Quality by (optional)

A repository or deployment can enable an optional **Quality by** credit for backlog stewardship: the verified person who intervened on a task (unblocked, retried, or answered its escalation), or otherwise `grain_config.oncall` while it ran. Grain shows a `Quality by` line in the PR description and writes `Quality-by: Name <email>` on the merge commit rather than `Co-authored-by:`.

## Merge commits and checks

For PRs landed through grain's merge queue, grain composes the final commit message itself:

- **Merge-queue check.** Before landing an internal PR, grain checks and repairs the PR description and commit message so they carry `Owner:`, one `Co-authored-by:` trailer per person in the task's attribution set (verified task owner plus any tasks-PR `Contributors:`), `Requested-by:` / `Approved-by:`, and `Quality-by:` when enabled.
- **Tasks PR lint.** Grain's review check on PRs touching `.task.grain.md` validates the syntax of `Owner:` and `Contributors:` lines in the PR description before merge.
- **Post-merge audit and durable record.** Missing required `Co-authored-by:` trailers on a grain-landed commit raise an inbox alert, and grainfile completion updates link the task URL and implementation PRs in Git.

## Follow-up work

1. Add `owner` to `task` (fixed at creation, inherited across parent→child links, person-editable with audit events) and store imported tasks' tasks-PR link and merged `Contributors:` list.
2. Parse and validate `Owner:` on PR descriptions and `Contributors:` on tasks PR descriptions, resolving `@handle` identities.
3. Add `owner` (`<person>` or `oncall`) to schedules (and optional defaults on templates, suites, and triggers) and `oncall` to `grain_config` / Settings → General.
4. Compose grain merge messages and PR descriptions from task `owner` and tasks-PR `Contributors:`, alongside `Requested-by:` and `Approved-by:`.
5. Add merge-queue checks, tasks-PR identity lint, and post-merge inbox audits.
