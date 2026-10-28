# One format, two contexts: the shared schema, and identity

grain/task-339. Templates, suites and schedules are written down in two
places today, in two formats, maintained separately:

- **The settings export.** `tables/template.json`, `tables/suite.json`
  and `tables/schedule.json` in the state repository: a database dump.
  Flat rows, id-keyed (`template-1`, `system:memory-agent`), timestamps
  and firing times as columns, and every relation in its own join table.
  A merged change to one of them is live on the next tick
  (`staterepo.SettingsTables`, `ImportTables`/`Apply`), so the config
  repository is already a first-class authoring surface for exactly these
  objects -- it just does not read like one. (Since grain/task-350 it
  does: those three files and their ten companions are now
  `settings/templates.yml`, `settings/suites.yml` and
  `settings/schedules.yml`. The last section of this document is what
  that took.)
- **The repo declaration.** `.grain/templates/*.yml`,
  `.grain/suites/*.yml`, `.grain/schedules/*.yml`: hand-written YAML,
  nested, name-keyed, hyphenated (`auto-merge`, `max-passes`), read by
  `pkg/repodecl` (docs/declarations.md).

Two formats maintained by hand is how a field lands in one and not the
other. The inputs a template asks for (`model.TemplateField`,
grain/task-58) have been a column, a table and a row of the export since
they existed, and until this change `fields:` in a declaration was
"field not found".

This document is the two decisions that had to be made before any of that
could be fixed: **which direction the unification goes**, and **which
identity a unified format uses**. What landed alongside it is the shared
definition, `pkg/declschema`.

## Which direction: the authoring format wins

These are two jobs, not two formats. The JSON is a *dump*; the YAML is an
*authoring format*. So "unify" means **make the authoring format the one
people write in both places**, not make a repository's files look like
row exports.

Getting that backwards would make the state repository harder to edit,
which is the opposite of the point: a settings repository an operator
reviews in a pull request wants `daily: {at: "09:00"}`, not
`recurrence_kind`, `every_n_hours`, `time_of_day_minutes`, `weekday` and
`day_of_month` with four of the five NULL. Nothing here asks `.grain` to
grow an `id:` or a `created_at:`.

What the two contexts do *not* share is size, and that stays:

| | settings | a repository's `.grain` |
|---|---|---|
| carries | every field | the subset a repository may say about itself |
| identity | the row's id | the file's own name |
| `grants`, `reads`, `writes`, `target` | yes | **refused, loudly** |
| a template's `capabilities: [repo-read:..., repo-write:...]` | as `reads`/`writes` | accepted, and only those two (task 1780) |
| a schedule's own `title`/`body`/`auto-merge` | yes | **refused, loudly** |

## The thing that must not be got wrong

The fields a declaration lacks are not missing fields, they are refusals.
`RepoConfig.Declarations`' own doc gives the principle: *the answer to
"what may this task touch" is never the repository's, switch or no
switch.* A design where the repo layer merely ignored what it does not
honour would turn a visible error into a silent drop, and the fields in
question are capabilities and target repositories.

So the unified format is **one definition with two validation contexts**,
and the repo context refuses by *saying no*, never by dropping. In
`pkg/declschema` that is `Key.In` -- the contexts that accept a key --
and `Key.Why`, the reason the repo context gives when it does not. Four
of them are about the *work* a repository declares:

- `Reach` -- the key would let a repository's own file say what the work
  it declares may touch. `grants`, `reads`, `writes`, `repo`, `target`.
  Templates accept `capabilities:`, which can declare any capability the team
  is authorized to hold, plus repository access (`repo-read:` / `repo-write:`,
  grain/task-1780).
- `Inline` -- the work belongs to the template the schedule fires:
  `title`, `body`, `auto-merge`, `network`, `custom-fields` on a schedule.
  Schedules accept `capabilities:` for schedule-specific capabilities bounded
  by team authorization.
- `Identity` -- derived from the file, so no file says it: `id`.
- `Kept` -- grain's own record of something already running:
  `created-at`, `next-run-at`, `last-run-at`.

Three more arrived with grain/task-340, which applied the same treatment
to a repository's own *settings* (`RepoSettingsSchema`, a fourth schema
over `model.RepoConfig`), and a fourth with grain/task-486. The rule
there is authority rather than behaviour -- a repository already
controls the code that runs, so what is withheld from it is the
authority that code runs with -- and the reasons are the ways a key
would cross that line:

- `Authority` -- the key would let a repository decide what its own work
  is allowed to do: `default-capabilities`, `capabilities`,
  `ci-capabilities`, `release-secrets`, `clone-url`, `repo`.
- `Verdict` -- the key would let a repository decide whether its own
  checks are believed, or what the deployment spends finding out:
  `ignore-github-ci`, `unfixable-checks`, `base-branch-ci-jobs`,
  `require-branch-up-to-date`, `watch-default-branch`.
- `Bootstrap` -- `declarations`, the switch that says this repository's
  word is read at all, which can never be something the repository says.
- `Ownership` -- the key asks which *deployment* runs this repository's
  standing work: `work-ownership`, and `release-workflows-disabled`,
  which it replaced. Added by grain/task-486, and the only reason here
  that is not about trusting the repository -- the repository is refused
  it because it cannot know the answer, not because it must not be
  allowed one. Several deployments may be looking at one repository and
  only one of them should be cutting its releases.

That schema carries the other half of the rule too, since a settings key
a repository *may* write has a deployment-wide counterpart to compose
with: `Field.Compose` is `Append` or `Union` and there is deliberately no
third direction, because a repo layer able to subtract could re-enable
something the deployment switched off or discard what an operator typed.
docs/declarations.md has the classification as a table.

"The config version just has extra fields" is not quite the right
framing, and this is why: one format with two dialects is still two
things to keep in step. The anti-drift property comes from the single
definition, not from the two looking alike.

## What landed: one definition

`pkg/declschema` is a list of fields. For each one: the keys it is
written under and which contexts accept them, the columns of the kind's
own table that carry it in the export, or the companion table it lives in
instead. It is not a decoder and nothing unmarshals into it --
`pkg/repodecl`'s typed structs stay exactly where they were, for the
reason its own preamble gives ("the file is shaped for whoever writes it,
hyphens and all, and the type is shaped for its readers").

Three tests are the deliverable; without them this would be decoration.

- **Against the model.** Every exported field of `model.Template`,
  `model.Suite` and `model.Schedule` is named by exactly one entry, and
  every entry names a real one. A field added to the model does not get
  to land until it has said which contexts accept it and what a
  repository that writes it anyway is told.
- **Against the export.** Every column of the `template`, `suite` and
  `schedule` tables -- which is exactly what `pkg/staterepo` dumps, since
  the dump is `PRAGMA table_info` in file form -- is named by exactly one
  entry, and no entry names a column that does not exist. A column added
  to `schedule` is a key in the export, and now has to be a decision
  here.
- **Against `pkg/repodecl`.** The keys the repo context accepts are
  exactly the yaml tags on that package's raw structs. The keys it
  refuses are not checked against anything, because there is no second
  copy of them to check: `refuse` reads the refused set out of the schema
  on every parse.

The first thing the list caught is in the same change: `fields:` is now a
key a declared template may write, validated as it is read the way the
same declaration typed into the UI is (`model.ValidateTemplateFields`).
It is carried in both contexts because it says nothing about what the
work may touch, and because an unattended firing fills every field with
its default (`Template.FieldValues`), so a declared template with fields
is still one a schedule fires on its cadence.

`.grain/lifecycle.yml` deliberately keeps its own `reserved` fields. It
has no second context -- there is no lifecycle row, no lifecycle table
and nothing of it in the export -- so there is nothing for it to drift
from, and its refusals reach two levels down (`serve.sandbox.network`)
where this schema describes top-level keys only.

## The decision on identity

**A unified format is name-keyed where it is authored by hand in a
repository, and stays id-keyed in grain's settings. Identity is never a
value somebody types: in a repository it is derived from the file, and in
settings it is allocated by the store. The `id` key exists in the
settings context only, and a declaration that writes one is refused with
`Why: Identity`.**

That is not a compromise between the two, it is the same rule read in two
places: *identity comes from where the thing is written*. A repository
writes one file per thing, so the file is the identity
(`model.RepoDeclaredID(kind, repo, stem)` -- `repo:template:acme:widgets:nightly`).
Grain's settings write rows that other rows point at, so the row's id is
the identity, and the export carries it because the row has one.

### Why settings identity does not become name-keyed

Name-keying diffs and authors better, and that is a real cost to decline.
It is declined because a settings id is not only a key, it is a
*reference*, and the references are not all settings:

- `schedule.template_id`, `schedule.suite_id`, `suite_item.template_id`,
  `qualification_item.template_id` and its `depends_on_template_id` --
  other settings, which could be renamed in the same merge.
- `task.review_template_id`, and the suite-run tables' own
  `template_id` -- **history**, which could not. A rename that re-keyed
  the row would silently orphan every past task that recorded which
  template it came from.
- `system:memory-agent` -- the id of a row grain seeds for itself, below.

Re-keying by name would therefore mean rewriting five tables on every
rename, and losing the tie between a finished task and the template it
was filed from. The name-keyed advantage is bought where it is cheap, and
refused where it is expensive.

### What happens on a rename

Four different things are called a rename; the format answers each one
differently, and each answer is already true today.

- **Renaming `name:` in a declaration.** Free. It is prose for a listing;
  the id is the file's. This is why `name:` exists separately at all.
- **Renaming a declaration's file.** A delete and a create: the old row
  is removed by the next sync and a new one written under a new id. What
  makes that safe rather than silent is that the references are by stem
  and are checked at parse time -- a suite or schedule still naming the
  old stem fails the whole read with `no template "nightly"`, so a rename
  that forgets one is a loud error in the pull request that does it, not
  a job that stops firing. Tasks already filed keep the old id, which
  matches what they record: the template they were filed from no longer
  exists.
- **Renaming a settings object's `name`.** Free, and one changed field in
  one row of the diff, which is the diff you wanted. Nothing points at
  the name.
- **Renaming a settings object's `id`.** Not a rename: a delete and a
  create, with every reference to fix by hand. It is not something the UI
  offers and the format does not invite it.

### What protects the `system:` rows

Two different things wear the `system:` prefix, and only one of them is a
row.

- **System templates are not rows at all.** `system:memory-agent` names a
  template compiled into the running build (`model.SystemTemplate`), for
  the reason that file gives: what a system template says is grain's, not
  the deployment's, and a run holding the memory grant must not be able
  to widen its own standing instructions by editing the template that
  writes them. There is nothing in `tables/template.json` to rename.
  `Store.resolveTemplate` asks this build first and the store second.
- **The schedule that fires one is a row**, seeded idempotently under
  that same well-known id by `orchestrator.reconcileSystemSchedules`, and
  it is exported. It is protected on four sides: the id is the identity,
  so no rename of anything else moves it; the seeder re-writes the row if
  it is missing; the resync writes this build's template back over its
  cached content, and `pkg/staterepo/systemschedules.go` posts a notice on
  the pull request whose merge tried to change that content; and
  `ui.refuseSystemScheduleContentEdit` allows a deployment exactly the
  columns it owns -- cadence, overlap, enabled.
- **A retired job's row is deleted.** When a build stops shipping a
  standing job, its id goes in `model.RetiredSystemScheduleIDs` and the
  same reconciler deletes the row every cycle it finds it -- including
  after an import from a state repository that still carries it -- so
  the export after that removes it from `settings/schedules.yml` too. The
  deployment-wide `system:memory-agent` was retired this way when every
  team got its own (`system:memory-agent:<team>`); its template still
  resolves, so a row that fires before the reconciler reaches it does not
  fail.

A name-keyed export would put a hole in that. Renaming the key of the
memory agent's schedule would not rename the row, it would create a
second one and leave the seeder to re-create the first: one rename,
two nightly memory agents, and no error anywhere. Keeping settings
identity on the id is what makes "you may not rename grain's own job"
structural rather than a check somebody has to remember to write.

### So: supplemented, not replaced

The export cannot be replaced by declarations, and should not be. It
keeps what a declaration may never hold -- templates that hold
capabilities, reach repositories by anything other than the filing
team's own credential, or belong to the deployment rather than
to any repository -- and it keeps id identity because its rows are
referenced from history. What converges is the *shape* and the *field
names*: the export learns to write the authoring format, and a person
moving between `tables/template.json` and `.grain/templates/nightly.yml`
reads the same keys in the same arrangement, with the settings file
carrying more of them.

## Converging the file shapes: what grain/task-350 did

The export now writes the three of them in the authoring format
(`pkg/staterepo/decl.go`). `settings/templates.yml`,
`settings/suites.yml` and `settings/schedules.yml` replace thirteen files
under `tables/`: one document per kind, one entry per object, keys
hyphenated the way `.grain` hyphenates them, relations nested inside the
object they belong to, and a cadence written `daily: {at: "09:00"}`
rather than as `recurrence_kind`, `every_n_hours`,
`time_of_day_minutes`, `weekday` and `day_of_month` with four of the five
NULL. Everything about the direction and the identity above held: the
entries are id-keyed, `id` leads each one, and the file carries the keys
the settings context has and a repository's own file does not.

Three things the work turned up that the design above did not price:

- **Every key is written, every time, even at its default.** A settings
  file that does not mention a column is a row that lands on the schema's
  default when it is imported -- that is the cost `SettingsTables`'
  own comment prices, `resets.go` reports and `grain state check` warns
  about -- so a format that left empty values out would turn every one of
  those warnings on for every row. `network: ""` and `base: null` are in
  the file for that reason and not for tidiness.
- **`orphaned-rows`.** `Store.DeleteTemplate` and `Store.DeleteSchedule`
  delete the parent row and leave `template_read`, `template_grant` and
  the rest behind, so a deployment that has ever deleted a template holds
  companion rows no template claims. The table dump carried them because
  it dumped tables; a nested document has nowhere to nest them, and a
  dump that drops rows is not a restore. They go out verbatim under a
  top-level `orphaned-rows:` key, which is normally absent. Making the
  store delete them with their parent would remove the need for it.
- **The four `*_sequence` tables are not part of anything.** They are id
  allocators -- a row is a number and a timestamp -- so they stay in
  `tables/` with the rest of the dump, despite being named alongside a
  template's real companions in the task that asked for this.

Two values the documents re-derive rather than carry, because the
authoring format has no place for them and neither is anything a person
wrote: `suite_item.order_key`, which `putSuite` writes as the item's own
position and the document writes as list order, and `suite_item.id`,
which is an `AUTOINCREMENT` rowid nothing in the schema refers to and
which `putSuite` throws away and reissues on every edit of a suite. Both
are reconstructed on import from the document's order, so
`TestARestoredDatabaseIsTheSameDatabase` still compares every column of
every table with `typeof()` beside it and still passes.

A repository written before this holds `tables/template.json` and its
twelve neighbours, and is still read: a table whose document is not there
falls back to its own JSON file, so `Load`'s restore of an older clone
works unchanged and the first export after the upgrade writes the
documents and removes the old files in one commit.

One thing that is **not** a blocker and was not treated as one:
`schedule.next_run_at` and `last_run_at` are runtime state on a settings
row. `SettingsTables`' own comment has already priced this -- an import
can lose a firing time, it is the same answer a restart already gives,
and it costs at most one early or late firing. Unification does not
create that cost.

## Which of this deployment's own rows a declaration could express

Worth writing down, because it is what a unified format has to answer
for. The generic answer is now exhaustive rather than sampled: the fields
a declaration cannot express are exactly the entries in `pkg/declschema`
that the repo context refuses, and they are all `Reach` or `Inline`.

The one global template whose content lives in this repository is the
memory agent, and it is inexpressible by design, in three separate ways:
it grants `memory` and `self-debug`, it reads the state repository, and
it has no target at all. That is the format working. A declaration that
could express it would be a repository able to hand itself grain's own
capabilities.

`declarations` is `0` on every repository this deployment knows, so none
of the `.grain` path is switched on -- `bwsalmon/grain` has a
`.grain/schedules/nightly.yml` and a `.grain/templates/nightly.yml`
sitting unread. Switching it on for one repository is a `repo_config`
change in the config repository (or `PUT
/api/repos/{owner}/{name}/declarations`), which is an operator's decision
rather than a change this repository can make, and is worth making: with
`KnownFields` and the refusals above, anything inexpressible comes back
as an error naming the key, which makes it a self-checking migration test
that costs a toggle.

## Where the code is

- `pkg/declschema` -- the one definition, the seven reasons a repository
  is told no, and (`RepoSettingsSchema`) which of its own settings a
  repository may declare.
- `pkg/staterepo/decl.go` -- the settings export in that format, and its
  inverse; `pkg/staterepo/decl_test.go` for what the file looks like and
  what a hand edit of one does.
- `pkg/declschema/declschema_test.go` -- the drift tests: against the
  model, and against the export's own columns.
- `pkg/repodecl/parse.go` -- the YAML surface, whose refusals are read
  from the schema at parse time.
- `pkg/repodecl/schema_internal_test.go` -- the accepted keys pinned to
  the schema in both directions.
- `docs/declarations.md` -- what a repository may declare, and how it is
  turned on.
- `docs/agent-api.md` ("Scope sets") -- the agent API's scope sets, a
  settings table (`tables/api_scope_set.json`) rather than a declaration:
  their schema, composition and how the state check reports them.
