# Install walkthroughs, weekly

Two walkthroughs install Grain from scratch the way a new user or an
operator would, and a sweep cleans up after them. They run every week, so
that an installation regression is caught without anybody asking for it.
Each first ran once by hand in October 2026; those reports are the baseline
the first weekly run compares against.

| Walkthrough | Template | Schedule (UTC) | First run | Report |
| --- | --- | --- | --- | --- |
| Fresh single-tenant install, end to end: `scripts/setup.sh`, access code, remote sign-in, model and git connections, first task, rotation, re-run, uninstall | `.grain/templates/install-walkthrough-single-tenant.yml` | Mondays 05:00 | task 4344 | `docs/test-runs/YYYY-MM-DD-single-tenant-install-walkthrough.md` |
| Install lifecycle (the template keeps its old "shared host" name; this build is single-tenant only): `setup.sh` in container and host mode, `grain init`, re-run, upgrade, channel switch, rollback, uninstall | `.grain/templates/install-walkthrough-shared-host.yml` | Tuesdays 05:00 | task 4345 | `docs/test-runs/YYYY-MM-DD-install-upgrade-walkthrough.md` |
| Leftover VM sweep | `.grain/templates/install-walkthrough-vm-sweep.yml` | Thursdays 05:00 | -- | closing comment only |

The first single-tenant run (task 4344) used the Workstation installer. That
walkthrough is Google-internal now, and lives in
the wrapper distribution's own docs.

The schedules are in `.grain/schedules/install-walkthrough-*.yml`. They are a
day apart so the walkthroughs don't compete for sandboxes or for the
`grain-sandbox-dev` quota. They start at 05:00, after the release cut
(02:00), the nightly workflow (02:11), the UI walkthrough suite (03:00) and
the staging cleanup (04:00). Times are read on the deployment's clock
(`Config.TimeZone`), which is UTC here. `overlap: skip` holds a week's
firing while the previous week's task is still open, and fires it once that
task closes.

## What every walkthrough does

- **Starts from last week.** It reads the newest report of its own kind
  and checks which of the bugs that report filed have been fixed on `main`.
- **Installs from `GRAIN_CHANNEL=main`**, with the installer from
  `origin/main` as an operator gets it from a clone. It records the image
  digest and its commit, and says at the top of the report when the
  published `main` image is behind `origin/main`.
- **Uses a throwaway VM** in `grain-sandbox-dev`, labelled
  `purpose=grain-walkthrough` and `grain-task=<task id>`, named
  `grain-wt-<task id>…`, with tcp:4242 open only to the sandbox's egress IP.
  **It deletes the VM and the firewall rule at the end, even on failure**,
  and it deletes any `purpose=grain-walkthrough` VM older than 6 hours that
  an earlier run left behind.
- **Files a bug task for each failure**, after checking that it isn't
  already filed. A small, safe fix may land
  in the walkthrough's own pull request with a regression test.
- **Calls out regressions first.** The report and the closing comment both
  open with a "Since last week" section: what newly fails, what was fixed,
  what still fails.
- **Closes with a pass/fail table**, one row per step with last week's
  result beside it and the bug task for each failure, then confirms the VMs
  are gone.

The templates target `main` with `auto-merge: true`, the same as the first
runs: what lands is the report and any small scoped fix. The schedules
don't auto-approve proposals, so the bug tasks a walkthrough files wait for
a person to approve them.

## The sweep

A walkthrough that is cancelled, or that loses its sandbox, cannot delete
its own VM. The Thursday sweep lists every instance labelled
`purpose=grain-walkthrough` in `grain-sandbox-dev`, deletes the ones
created more than 6 hours ago along with their firewall rules, and leaves
everything else alone. It pushes nothing, and it ends `nothing_needed` when
there was nothing to delete. Something to delete means a walkthrough's
cleanup failed, and the closing comment names that task.

## What the deployment has to provide

All three templates declare `capabilities: [gcp-key]`. Since
grain/task-1780 and e8041e94c (both on `candidate`), a repository-declared
template may carry it (docs/declarations.md, "Capabilities and repository
access"). The team bounds what a declaration can ask for, and none of the
three names a team,
so they file under the default team:

- **`gcp-key`** is limited to `model.DefaultAdminTeams`, which is the default
  team, unless the deployment's gcp-key config names `AuthorizedTeams`. The
  deployment hands each run one key from its pool of service accounts
  (`grain-sb-agent-N@grain-sandbox-dev`, `gcp_service_account_count` in
  grain/config). Every account in the pool has to be able to create and
  delete instances and firewall rules in the project, not just the one a
  given run gets (task 4345's could). If the
  team isn't authorized, the grant is stripped when the task is filed, and
  the task thread says so (`TeamUnauthorizedRefusal`).
