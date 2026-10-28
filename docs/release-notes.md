# Release notes

grain's own per-version notes are in [docs/releases/](releases/README.md),
one file per release. The newest is **[v2.0.0](releases/v2.0.0.md)**, the
first single-tenant release:

- the access code is the only sign-in;
- the Google build is the default and the core build is `./cmd/grain`;
- the Silo hub is removed;
- corp-research is on by default in fresh Google-build installs;
- Cloudtop installs start with no git connection;
- the Grainfile Format docs are added.

There is no in-place upgrade from 1.x: install fresh, then bring the old
connections across with `grain connections import`. Every release after
2.0.0 upgrades a 2.0 deployment in place. There was no v1.14.0.

The rest of this document is about something else: the weekly release
notes a deployment writes about itself.

grain/task-377: give a deployment a weekly, human-facing account of
itself — what it shipped, across every repository it works in — written
by a standing job grain ships rather than by whoever remembers to look.

This describes what is implemented (`pkg/model/release_note.go`,
`pkg/model/cluster_stats.go`, `pkg/mcp/release_notes_tool.go`,
`pkg/capability/releasenotes`, `pkg/ui/releasenotes.go`,
`ui/src/components/ReleaseNotesPanel.jsx`, and the seeding in
`pkg/orchestrator/systemschedule.go`), not a proposal.

## What it is

- **A schedule every deployment gets.** `system:release-notes` is seeded
  the way `system:memory-agent` already was — a well-known id, written
  once, and the deployment's from then on (how often it runs, whether
  firings overlap, whether it runs at all). Its cadence is weekly, on a
  Monday at the same quiet hour the memory agent uses, because it writes
  about a week that has finished.
- **A row per ISO week.** `model.ReleaseNote` is keyed by `2026-W38`:
  the primary key, the file name in the state repository, and the word
  an agent types back into a tool call. Writing the same week again
  replaces it, so a correction is a correction rather than a second
  account.
- **A file in the state repository.** The export writes each row to
  `release-notes/<year>-W<week>.md`, the way memory bodies already land
  under `memory/` — one markdown file per week, in a place a clone can
  read and a diff can show.
- **A pane.** The week's counts, the repositories behind them and the
  prose are at the top of the Metrics pane, with a picker for earlier
  weeks.

## Why a row and not a pull request

The obvious alternative was to point the weekly job at the state
repository as a write target and let it open a pull request with the
file in it. `model.MemoryDocument`'s own doc comment argues the same
question for memory and settles it the same way, and the reasoning
transfers unchanged: a run writing here is in a sandbox that holds no
credential for this deployment's settings and should not be given one,
the state repository's contract runs the other way (files there are
written *from* the database on a timer), and a file a merged pull
request can replace is a file a branch cut last week can silently
delete. So the row is where the truth is, and the file is the export's
business.

`release_note` is therefore off `staterepo.SettingsTables`, with
`memory` and `memory_document`, and is exported by `ExportConfig`
alongside them.

## The numbers are not the agent's

`Store.ClusterStats` counts a window from grain's own rows: pull
requests merged (`task_observation.pr_merged_at`), tasks completed and
filed, runs and how many succeeded, how long those runs spent running,
how many lines those merges added and removed, and the same counts per
repository, most merged first. The window comes from the period
(`ReleaseNotePeriodRange`), so the figures and the prose are
demonstrably about the same seven days.

The run gets them through `read_cluster_stats` rather than counting for
itself. An agent paging through a week of tasks with `list_grain_tasks`
would produce arithmetic nobody can check, and would quietly miss a page
at a limit. Keeping the two tools separate — one that counts, one that
publishes — is also what lets a reader of a week's notes know the
figures in them were not written by the thing that wrote the sentences.

**The size of a change, and why it is sometimes partial.** GitHub
returns `additions`/`deletions`/`changed_files` on the single-pull-
request GET the merge queue already makes for every tracked pull request
on every sync cycle, and only on that response — a list of pull requests
does not carry them. So the figure costs no extra call and no extra rate
limit (`docs/github-api-rate-limits.md`):
`orchestrator.recordPullRequestEvents` copies the three onto the task's
observation (`task_observation.pr_additions`/`pr_deletions`/
`pr_changed_files`) on the one cycle that stamps `pr_merged_at`, which
is the last cycle that task is ever synced — so each is written exactly
once and never revised, which matters because a merged branch's head is
about to be deleted and the number would not be recoverable afterwards.

The columns are nullable, and the NULL is load-bearing. A merge grain
observed before these columns existed has no size on file, and that is
*not* the same statement as a pull request that changed nothing. So
`ClusterStats` reports `PullRequestsMeasured` beside `LinesAdded`/
`LinesDeleted`: how many of the window's merges the sums are actually
over. Where it is short of `PullRequestsMerged`, `read_cluster_stats`
says the figure is partial and what fraction of the week it covers, the
prompt says never to quote a partial figure as the week's total, and the
pane's chip shows "n of m merges". A window with nothing measured at all
reports no lines figure rather than a confident zero. A pull request
whose detail carried all three as zero is recorded as unmeasured for the
same reason: GitHub does not merge an empty diff, so all-zero means the
response did not say.

## What the job is told

`model.releaseNotesBody` is the prompt, and it spends its length on the
two ways this job goes wrong: inventing figures (so the numbers come
from a tool, and the prompt names what grain does not measure), and
padding a quiet week (so it asks for the week that happened, says a
short note is a good note, and asks for what is still failing beside
what landed).

The job holds `release-notes` and `self-debug`, reads the state
repository so it can see the weeks already written, and targets no
repository — it publishes a row, not a branch. It is deliberately not
given `memory`: a week's report must not be filable as a standing
instruction to every run this deployment dispatches.

## Left open

- The lines-changed figure only covers merges observed since the
  columns existed, so a window older than that is reported as partial
  (above). Nothing backfills the older rows, and nothing could without
  re-reading every merged pull request from GitHub.
- Nothing prunes old weeks. A year is 52 small markdown files; if that
  ever matters, it is a retention setting rather than a rewrite.
- The pane reads; it does not write. Correcting a week by hand goes
  through `PUT /api/release-notes`, which records the author as a
  person, and the UI has no form for it yet.

## Unreleased

Notes for the 2.0 release notes (task 4873) to pick up.

- **Install layout (task 4903).** grain 2.0 keeps everything under one
  root, `/var/lib/grain`, which is `root:root 0755` and holds nothing but
  two sub-folders: `data/` (`GRAIN_DATA_DIR`, `grain:grain 0750`: the
  store, secrets, `secrets.key`, `local-api-token`, `bin/`) and `scratch/`
  (`GRAIN_SANDBOX_DIR`, `grain:grain 0755`: host-sandbox runs and the
  package, Actions and CI caches). Back up `data/`; never back up
  `scratch/`. Either can be a mount of its own, and `scratch/` is best on
  a disk of its own; terraform/gcp mounts its data disk at `data/` and
  binds its sandbox disk onto `scratch/`. This is a breaking change from
  1.x's `/var/lib/grain` and `/var/lib/grain-sandbox`, made now because
  2.0 has no in-place upgrade from 1.x. The variables and the daemon's
  `-data-dir`/`-sandbox-dir` flags keep their names, and an explicit
  setting of either keeps exactly what it says. A re-run of
  `scripts/setup.sh` keeps a data directory `/etc/grain/cli.env` already
  records and says so; a fresh install refuses to lay `data/` out beside
  a 1.x flat data directory left in `/var/lib/grain`, and says how to move
  it. `--uninstall` keeps `data/` and removes `scratch/`; `--purge`
  removes both and then `/var/lib/grain` if it is empty.
- **Rendezvous relay removed.** The Cloud Run rendezvous relay
  (`grain-relay`, `pkg/relay`, `grain relay`, `/api/relay-settings`, and
  the Terraform relay module) has been removed; see
  [relay-removed.md](relay-removed.md). A daemon started with any
  `-relay-*` flag or `GRAIN_RELAY_*` variable refuses to start and says
  so; a stored `<data-dir>/relay-settings.json` or `relay/token` secret
  is ignored with one log line, and imports skip relay settings with a
  note. Remote executors no longer connect through `/relay/executor`;
  they connect directly to the daemon over `-executor-addr` or HTTP
  upgrade (`/_grain/executor`, `/api/executors/connect`,
  `/api/executors/stream`). On GCP (`deploy/terraform/gcp`), reach the
  daemon over `gcloud compute start-iap-tunnel`
  (`terraform output tunnel_command`) or the VM's HTTPS load balancer +
  IAP (`expose_ui_publicly = true`).
