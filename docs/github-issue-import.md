# Importing GitHub issues as tasks

Grain can poll a GitHub repository's open issues and file a task for each
one it has not imported before. It is configured per repository, on the
repository page's **Agents** tab under **GitHub issue import**, or through
`PUT /api/repos/{owner}/{name}/settings`:

| Setting | API field | Default | Meaning |
|---|---|---|---|
| Import open GitHub issues as tasks | `issueImportEnabled` | off | Switches the importer on for this repository. |
| Source repository | `issueImportRepo` | this repository | The `owner/name` whose issues are read, such as an upstream project you work on in a fork. Tasks always target the repository the setting is on. |
| Label | `issueImportLabel` | any | Import only open issues carrying this label. |
| Queue imported tasks at once | `issueImportApproved` | off | Off: each imported task waits unapproved until a person approves it. On: imported tasks are approved and queued immediately. |
| Every (minutes) | `issueImportIntervalSeconds` | 10 minutes | How often the source is polled. The API takes seconds, 0 for the default; the minimum is 60. |

The settings are stored in `repo_config` (`issue_import_enabled`,
`issue_import_repo`, `issue_import_label`, `issue_import_approved`,
`issue_import_interval_seconds`) and exported with the rest of the
deployment's state. They are operator settings: a repository cannot declare
them in its own `.grain/settings.yml`.

## What an imported task looks like

- Its title is `[#N] <issue title>`, or `[owner/name#N] <issue title>`
  when the source is another repository.
- Its body carries the issue's URL, author, labels and description, and
  its first five comments.
- Its origin is `<source>#N` and its origin reason is `imported`. When the
  source is the target repository, the task's issue number is set too, so
  its pull request says `Fixes #N`. For an upstream source it is not set,
  because `#N` in the fork's pull request would name a different issue.
- It carries the `imported` kind tag for its whole life. Filter on it with
  `list_grain_tasks(kind: "imported")`, or check it with
  `expect: {kind: "imported"}`. In the task list it has an **imported**
  chip, and the Origin filter offers **Imported issue**.
- While nobody has approved it, its status reads **Imported** rather than
  Proposed. The scheduler does not dispatch it until a person approves it
  with the usual **Approve** action.

## Each issue is imported once

Before filing, the importer looks for any task that already stands for the
issue: one whose origin is `<source>#N`, or, for a same-repository source,
one targeting the repository with issue number N. Tasks in every state
count, closed ones included, so closing an imported task does not bring
the issue back on the next poll.

## Importing now

**Import issues now** (`POST /api/repos/{owner}/{name}/import-issues`) runs
one pass straight away. It needs the importer switched on, and it says how
many open issues it read, how many it imported and how many it skipped.

## Why imported tasks wait by default

Whoever can open an issue on the source repository writes the words an
imported task's agent is given. With **Queue imported tasks at once** off,
a person reads each one before any agent runs on it. Turn it on only for a
source whose issue authors you would let file work here directly.

For the same reason, grain posts no progress comment on an imported issue
until somebody has approved its task.
