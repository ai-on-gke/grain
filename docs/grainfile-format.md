# Grain and the Grainfile Format

Status: intent. This page records how grain plans to read and write the
Grainfile Format. None of it is implemented yet. Until it is, a
repository's standing work is what it declares under `.grain/`
([declarations.md](declarations.md)), and nothing on this page changes
how that is read.

The Grainfile Format is specified in bwsalmon/silo
`docs/grainfiles-spec.md`. bwsalmon/silo#174 (task 4658) narrowed it to an
authoring language for work. A repository writes the inputs: definitions,
templates, triggers, capabilities, designs, gates and lifecycle
definitions. A deployment writes one output after each execution, an
immutable execution record (`<name>.record/`, holding `record.md` and a
`comments/` process log). Everything else is left to the engine:
backlogs, lanes, live state, placement and runs. Every executor setting
goes in one opaque `settings:` mapping whose keys the engine defines. The
spec's Appendix C says grain's docs define grain's keys, and this page is
where they are defined.

This page covers four things:

1. what grain keeps to itself;
2. where and when grain writes execution records, and what goes in them;
3. the `settings:` keys grain reads;
4. how grain maps capability references onto its own grants.

## 1. What grain keeps to itself

Grain's backlog, queue and live task state stay in grain's own database
(`pkg/model`, SQLite or Postgres). None of it is written into a
repository in the Grainfile Format:

- **The backlog.** Proposed, queued, deferred and in-flight tasks, with
  their approvals, dependencies (`depends_on`), supersessions and
  proposal inheritance.
- **The queue and placement.** Which task dispatches next, each team's
  worker and CI limits, and the merge queue with its batches. These are
  what the spec calls lanes and placement.
- **Live state.** The task states in `model.State` (`proposed`, `queued`,
  `running`, `awaiting_reply`, `awaiting_submit`, `awaiting_ack`,
  `failed`, `completed`, `deferred`, `closed`), runs and attempts,
  sandboxes, transcripts, the activity line, CI verdicts and fingerprint
  caches, and a schedule's next and last firing times.
- **Settings rows.** These include the deployment's own templates,
  schedules and suites, which an operator writes and the state
  repository (`grain/config`) exports ([declaration-schema.md](declaration-schema.md)).
  The state repository is grain's database written out as text. It is
  not a Grainfile, and this page does not change it.

So a definition, template or trigger file is something grain *reads* and
turns into the same rows a `.grain/` declaration turns into today, under
the same rules. A file takes effect only from the default branch, an
unknown key is an error that names the key, and a refused key is refused
with its reason (`pkg/declschema`). An execution record is the only thing
grain *writes*. Grain reads records back only to avoid writing the same
one twice. It never reads one to rebuild state.

## 2. Execution records

### When

Grain writes a record once, when a task that ran **settles**. "Ran" means
at least one run of the task was dispatched. "Settles" means it reaches a
state it does not leave on its own:

| How the task ended | Grain's state at that point | Record `outcome` |
| --- | --- | --- |
| Its pull request merged | `completed`, then `closed` when the merge is seen | `succeeded` |
| A run answered in a comment and somebody acknowledged it | `awaiting_ack`, then `closed` | `answered` |
| A run ended on `nothing_needed` | `closed` (or `awaiting_ack`, then `closed`, under `nothing-needed: ack`) | `nothing-needed` |
| Closed in favour of another task | `closed` with a `superseded-by` link | `superseded` |
| Closed by a person while its pull request was unmerged, or while it was still running | `awaiting_submit`, `running` or `queued`, then `closed` | `cancelled` |
| Closed after repeated failed runs | `failed`, then `closed` | `failed` |

A task's state only says what is going on now, so several situations get
**no** record:

- A task that never dispatched a run gets none. That includes a proposal
  that was rejected and a deferred task that was closed. Nothing was
  executed, so there is nothing to record.
- `failed` gets none on its own. It waits on a human retry, and a retried
  task's later runs belong in the same record.
- `awaiting_reply`, `awaiting_submit` and `awaiting_ack` get none. Each
  is still waiting on someone.
- Standing console sessions (`model.NeverCompletes`) get none. They never
  settle.

A task that settles and is later reopened (for example, a closed task
that someone retries) gets a second record, numbered after the first. A
record is never rewritten.

### Where

Grain writes records into the repository the definition was read from,
on a branch of their own: `grain/records`. Each record's path mirrors the
definition's path, plus the task id:

```text
grain/records:
  .grain/definitions/nightly-bump/nightly-bump.task-4688.record/
    record.md
    comments/
      0001-filed.md
      0002-run-1.md
      0003-question.md
      0004-reply.md
      0005-closing-note.md
```

A record for a task that came from no file at all (filed by hand, or from
a deployment's own settings template) goes under `records/<template or
"adhoc">/` on the same branch of the task's target repository.

Records go on their own branch, not the default branch, because writing
to the default branch would put every finished task's paperwork through
the merge queue, CI and the `.grain/**` human-submit trigger, and those
exist to review code. The branch can only ever be appended to, and two
rules enforce that. Grain only ever pushes a fast-forward to it. And
grain refuses to write a path that already exists there, so a record
cannot be overwritten. Anyone who wants records next to the code can
merge or read the branch. Grain does neither.

Grain pushes records through its git proxy, using the same credential the
task's own work was pushed with (`Store.TaskGitCredential`). A record
therefore needs no access the task did not already have.

### Who writes it

The commit's author and committer are the deployment's forge account, the
account behind that credential. Grain writes the record on the
deployment's behalf, so the commit is never attributed to an agent run or
to the people in the thread. The commit carries trailers so it can be
traced from git alone:

```text
Record task 4688: Nightly dependency bump (succeeded)

Grain-Task: 4688
Grain-Deployment: <deployment name>
Grain-Definition: .grain/definitions/nightly-bump.definition.md@<sha>
```

### `record.md`

The front matter holds facts. The body is prose a human can read.

```markdown
---
definition: .grain/definitions/nightly-bump.definition.md
definition-commit: 3f2a9c1        # the default-branch commit the definition was read at
template: nightly-bump            # stem, or a deployment template id
trigger: nightly                  # the trigger that filed it, if any
executor: grain
deployment: <deployment name>
task: 4688
team: infra
repo: acme/app
base: main
filed-at: 2026-10-17T02:00:00Z
started-at: 2026-10-17T02:03:11Z  # first run dispatched
settled-at: 2026-10-17T03:40:02Z
outcome: succeeded                # see the table above
runs: 2                           # attempts dispatched
models: [claude-opus-5-5]
capabilities: [gcp-key, repo-read:acme/upstream]   # what the task held, after the team bound
settings:                         # the effective settings, after precedence
  model: claude-opus-5-5
  network: none
  auto-merge: true
pull-requests:
  - url: https://github.com/acme/app/pull/812
    merged: 9b1e7d0
proposed: [4690, 4691]            # tasks this one filed with propose_task
superseded-by: null
---

# Nightly dependency bump

<the title and body as filed, after template fields were filled in>

## Closing note

<the run's closing comment or nothing_needed reason, verbatim>
```

The record leaves out run transcripts, sandbox names, the activity line,
CI fingerprints, cached verdicts and every value from the secret store.
Transcripts and CI logs stay in grain, where `grain get` and the task
page show them. Secret values never reach a thread in the first place
(`request_secret`), so they cannot reach a record either.

### `comments/`: the process log

The process log has one file per entry in the task's conversation, in
order. Each file is numbered and named after its kind:

- `filed`: who filed the task and how (by hand, a trigger, a schedule, a
  suite, or a proposal from task N), with approval if it needed one.
- `run-N`: one per attempt. It gives the model, the start and end times,
  and the ending (`model.EndingOf`) with its detail line, and links the
  commits that run pushed. It holds no transcript.
- `question`, `reply`, `comment`, `closing-note`: what the agent and
  people wrote, verbatim.
- `review`: findings from an automated or human review.
- `grain`: grain's own notes in the thread. These include refusal notes
  (`TeamUnauthorizedRefusal`), dependency notes, merge-queue repairs,
  CI verdicts and the merge.

Each file's front matter gives `author` (a grain user, an agent run as
`task 4688 run 2`, or `grain`), `at`, and `kind`. People are named the
way grain names them in the thread. The forge account is only the
committer.

## 3. The `settings:` keys grain reads

Grain reads `settings:` on four kinds of file: `grain.config.md`,
definitions, templates and triggers. Every key below comes from a
`.grain/` key that exists today, and keeps its spelling and meaning. Only
where the key is written changes.

The rule that decides which keys are allowed carries over from
[declarations.md](declarations.md) unchanged:

> A setting is declarable when it changes what a repository's own work
> does, and never when it changes what that work is permitted to do, who
> judges it, or whether the repository is believed.

When the same key is set at more than one level, the most specific wins:
**trigger > definition > template > `grain.config.md` > the team's and
the deployment's own defaults.** This generalises the precedence grain
already has, where a schedule's `model` beats its template's
(`model.Schedule.Model`).

### On definitions and templates

| Key | Values | Today's `.grain/` key | What grain does with it |
| --- | --- | --- | --- |
| `model` | a model id | template `model` | The model the work runs on, served only by the team's own connections ([connections-and-models.md](connections-and-models.md)). |
| `network` | `open`, `none` | template `network` | The sandbox's outbound access. A declaration can only narrow the deployment default, never widen it. |
| `nothing-needed` | `close`, `ack` | template `nothing-needed` | Whether a `nothing_needed` ending closes the task or waits for someone to acknowledge it. |
| `auto-merge` | bool, default `false` | template `auto-merge` | Whether the task's pull request goes straight onto the merge queue. |
| `base` | a branch | template `base` | The branch the work starts from and lands on. Defaults to the repository's default branch. |
| `team` | `default`, or left out | none: schema 25 dropped the column | Read and kept nowhere. A deployment has no team besides the default one, so any other value refuses the whole file (`repodecl.checkTeam`). |
| `sandbox.cpus`, `sandbox.memory-mb`, `sandbox.disk-gb` | positive integers | none on templates today. Same vocabulary as a workflow job's `sandbox:` and a lifecycle's `serve.sandbox` | The VM shape for the task's runs (`Task.SandboxCPUs`, `SandboxMemoryMB`, `SandboxDiskGB`). Zero or unset means the deployment's default. A value above the deployment's ceiling is refused when the file is read, not clamped later. |
| `push` | a list of ref patterns, as today | template `push` | Refs beyond the task branch that the task's runs may push in this repository. The rules are unchanged: explicit refs only, `force`/`delete` only where an entry says so, read only from the default branch. Templates only. |

### On a definition that runs several templates (today's suite)

A suite becomes a definition that names more than one template. Its keys
sit under `settings.suite`:

| Key | Values | Today's `.grain/suites` key |
| --- | --- | --- |
| `suite.mode` | `count` (default), `until-clean` | `mode` |
| `suite.count` | integer, default 1 | `count` |
| `suite.max-passes` | integer, default 5 | `max-passes` |
| `suite.require-approval` | bool, default `false` | `require-approval` |
| `suite.auto-approve-proposals` | bool, default `false` | `auto-approve-proposals` |

A suite's `auto-merge` is the plain `auto-merge` key above. Its default
stays `true` for a suite, as it is today.

### On triggers

A trigger can set any definition key above. Whatever it sets applies to
the work it files. A scheduled trigger also reads these keys:

| Key | Today's `.grain/schedules` key | What grain does with it |
| --- | --- | --- |
| `base` | `base` | The branch a firing runs against. For a trigger that runs a workflow, this is the branch the workflow runs on. |
| `auto-approve-proposals` | `auto-approve-proposals` | Whether proposals from the tasks it files are auto-approved, held to the rules in `model.AutoSubmits`. |
| `overlap` | `overlap` | `skip` (default) or `allow` ([declarations.md](declarations.md), "Skipped firings"). |
| `review` | `review` | `on-failure` (default) or `always`, for triggers that run a workflow. |

The cadence, the paths a trigger watches and the template it fires are
parts of the trigger in the format itself. They are not executor
settings, so they do not go in `settings:`.

### On `grain.config.md`

`grain.config.md` is the repository's own engine configuration. It
replaces `.grain/settings.yml`, and its `settings:` holds the same four
keys: `prompt-extension`, `setup-command`, `port-branches` and
`port-types`. It may also set any definition key above. A key set here is
the repository-wide default that templates, definitions and triggers
override.

### Keys grain refuses

The authority keys stay refused, wherever they are written and with the
same reasons `pkg/declschema` gives today. Writing them under `settings:`
does not make them readable:

- `reads`, `writes`, `grants`, `target`, `repo`: **reach**. Repository
  access is requested through capability references (section 4), never
  through settings.
- `default-capabilities`, `ci-capabilities`, `release-secrets`,
  `clone-url`, `admins`, `repair-template-id`, `post-merge-dispatch-jobs`,
  `git-credential`: **authority**.
- `ignore-github-ci`, `unfixable-checks`, `base-branch-ci-jobs`,
  `require-branch-up-to-date`, `watch-default-branch`, `ci-mode`:
  **verdict**.
- `declarations`: **bootstrap**.
- `work-ownership`, `release-workflows-disabled`: **ownership**.

Any other key grain does not know is an error that names the key, not
something grain silently skips. If several engines read the same file,
each one's keys go under its own name (`settings: {grain: {...}}`), and
grain reads only what is under `grain`. Grain uses unprefixed keys only
when the file names no other engine.

## 4. Capability references

A definition or template lists the capabilities its work needs as
references to capability files. A reference can point into the same
repository or into a shared commons repository, for example
`commons/gcp-key.capability.md`. Grain treats a reference as a **request**.
The rule from [declarations.md](declarations.md), "A requirement is not a
grant", still holds: the declaration says what the work asks for, and the
filing team's grants decide what it gets.

How grain resolves a reference:

1. **By name.** The capability's name is the file's stem before
   `.capability.md`, so `gcp-key.capability.md` is `gcp-key`. Grain looks
   that name up in its own capability registry (`pkg/capability`). The
   referenced file's body is documentation for people. Grain never runs,
   installs or trusts anything in it. Every credential still comes from
   grain's own provider for that id.
2. **Repository access.** `repo-read:<owner>/<repo>` and
   `repo-write:<owner>/<repo>` become the task's `reads` and `writes`,
   exactly as a `.grain/templates` `capabilities:` entry does today.
   Those repositories still have to be among the team's target repos, and
   `grain/config` repositories are still refused.
3. **Scoped variants.** A reference to one of the deployment's named GCP
   keys names it in full (`gcp-key:deploy`) and resolves to that key
   ([team-gcp-keys.md](team-gcp-keys.md)). A bare `gcp-key` means the
   deployment's pooled key, which stays admin-only unless an operator has
   widened it (`capability_teams`). Grain never swaps one for the other.
4. **Bounded by the team.** A capability the filing team may not hold is
   dropped from the task, and a `TeamUnauthorizedRefusal` note in the
   thread names it. This is how `.grain/` declarations behave today. A
   proposal filed by such a task still inherits only what its parent held
   (`model.UngrantedReads`, `model.UngrantedWrites`).
5. **Unknown names.** A reference to a capability grain does not have
   refuses the whole file, with the reference named, in the same way a
   misspelt key does. The last good commit's declarations keep running.
   An unknown name is not quietly dropped, because work written to need a
   capability should not run without it.

Grain keeps an alias table in `pkg/capability` for commons names that
differ from grain's ids. It is empty today, because the commons names are
expected to match grain's ids. An alias only renames a reference. It
cannot grant anything.

Capability references are covered by the same review as today's
`capabilities:` entries. A change that adds or widens one touches a
trust-boundary path, so it is held for human review, as `.grain/**`
changes are now.

## What is still open

- Several shapes on this page are grain's proposals, not the spec's:
  where a suite's keys sit, how a record is named
  (`<definition>.task-<id>.record`), the `grain/records` branch, and the
  `settings: {grain: ...}` namespace for files several engines read. If
  bwsalmon/silo `docs/grainfiles-spec.md` settles any of these
  differently, the spec wins and this page should be updated to match.
- `sandbox.*` on a definition or template is the one key above with no
  `.grain/` declaration behind it today. Today the shape comes only from
  `propose_task`, the task form and the deployment default.
