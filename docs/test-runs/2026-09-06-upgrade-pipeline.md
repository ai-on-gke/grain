# Hand run: the upgrade pipeline, 2026-09-06

The pass `docs/test-plans/2026-09-05.md` section 2 asks for, with the
sandbox-image leg section 10 folds into it (`grain/task-121`). Run by
hand on 2026-09-06 against a throwaway deployment installed by
`scripts/setup.sh`, one commit behind the build it was upgraded onto.

Steps 5 and 6 of that section -- the schema-change legs -- were **not
run**, on the plan's own instruction: they were only a hand run until the
end-to-end container test landed, and it has.
`tests/container/schema_upgrade_test.go` (`f5496a00`, `ecf53c23`) drives
the migration, the refusal from the floor and the refusal from the
import against real images, which is the better home for them.

## The deployment

Not a GCE VM. This run had no GCP credential and no reachable GHCR, so
the deployment was installed on a throwaway host of the same shape as far
as everything in section 2 can tell: real systemd as PID 1, real docker,
`/dev/kvm` present, `scripts/setup.sh` run as root with nothing patched
out, and the daemon running as `grain-daemon.service` out of a container
the way the unit runs it. What differs from `terraform/gcp` is named at
the end, under "What this run could not reach".

    sudo GRAIN_IMAGE=127.0.0.1:5000/grain \
         GRAIN_IMAGE_TAG=main GRAIN_REF=main \
         bash scripts/setup.sh

Two images were built from this checkout with `make image` and pushed to
a registry on the host, standing in for what
`.github/workflows/build-artifacts.yml` publishes per branch:

| Tag | Built from | `sandbox-image` stamp |
| --- | --- | --- |
| `127.0.0.1:5000/grain:main` | `a4b0fbbb` (main) | `127.0.0.1:5000/grain-sandbox:old` |
| `127.0.0.1:5000/grain:grain-task-121` | `b45afd56` (this branch) | `127.0.0.1:5000/grain-sandbox:new` |

Both report `schema-version` 16, which is what step 3 asks for: a branch
whose image exists and shares this schema. The two sandbox references are
distinct images in the same registry, which is what makes the section 10
leg observable at all.

`setup.sh` finished with `daemon: active`, `sandboxing: kontur VMs`, and
the unit's `ExecStart` carrying **no** `-kontur-image` flag -- the
unpinned shape `ensure_kontur_oci_image` describes and the one the
sandbox-image claim depends on.

Every step was driven through the API first, to see exactly what the
deployment reports, and steps 1, 3 and 4 were then driven again through
the pane in a real browser (headless Chromium against
`http://127.0.0.1:80`, nothing stubbed -- unlike `ui/e2e/upgrade.spec.js`,
which has to stub `/api/upgrade` because `grain demo` configures no
upgrader). Step 2's refusal was API-only. Three upgrades were run in all:
onto the branch, back onto main, and onto the branch again.

## 1. The pane names what it is upgrading from, and the steps

Pass, with one thing to know about the steps.

`GET /api/upgrade` on the freshly installed deployment:

    {"enabled":true,"mode":"image",
     "running":{"image":"127.0.0.1:5000/grain:main"},
     "branch":"","phase":"","detail":""}

`running.image` is read back out of `/var/lib/grain/image.env` rather
than inferred from an attempt on record, so the answer is right on a
deployment that has never upgraded -- which is the case `grain/task-50`
was about and the case a first-time operator is always in.
`UpgradePanel.jsx` renders it through `RunningNow`, above the branch
field.

The steps: what the pane shows *before* an upgrade is
`ModeDescription`'s prose -- what the pipeline does, that it is one
upgrade at a time, and what a schema-changing release does to this
deployment's rows. The named step list (`pull image`, `health check`,
`sandbox image`, `schema`, `record image`, `restart`) arrives with the
first poll *after* the button is pressed, because `status.steps` is part
of an attempt's status and there is no attempt yet. The refusal below
carried the whole six-step plan from its first poll, and so did every
upgrade that ran, so nobody watching
an upgrade is left guessing what is still to come; it is only the idle
pane that describes the pipeline in prose rather than as a list.

In the browser, Settings -> Upgrade on this deployment read:

    Pulls the image CI published for the branch below, runs it once to
    prove it works, and then points this deployment at it and restarts.
    Nothing is built here, so a branch CI has published no image for
    cannot be upgraded onto; and a pulled image that fails that check
    changes nothing, leaving this deployment on the image it is already
    running. One upgrade at a time; each step below reports itself as it
    goes. A release that changes the database schema is carried across
    rather than refused: [...]

    Now running 127.0.0.1:5000/grain:grain-task-121.

    Branch [        ]  [Upgrade]

-- the mode-specific description first, then the build it is on. Below
the form, the "After an upgrade" settings (`upgradeCheckMode`, "Hold the
queue until the check is done") stand where an operator reading about the
pipeline will meet them, which is the right place for them.

## 2. A branch with no published image

Pass, exactly as written.

    POST /api/upgrade {"branch":"grain/no-such-branch"}

* `phase: failed` within 120ms, `detail` naming the pull and the
  registry's own words: `127.0.0.1:5000/grain:grain-no-such-branch: not
  found`.
* `/var/lib/grain/image.env` untouched -- same content *and* same mtime
  (`18:41:31`, written by `setup.sh`) after the refusal as before it.
* the deployment stayed up on the old build throughout; `/api/config`
  answered 200 across the refusal.
* the plan was on screen: `pull image` failed, the five steps behind it
  reported `pending`.

One observation rather than a failure: the steps after the one that
failed stay `pending` forever, and the pane's own vocabulary has a
`skipped` state with a reason (`UpgradePanel.test.jsx`, "says why steps
were skipped") that the image path does not use here. A reader of a
failed upgrade sees five steps that look like they are still to come.

## 3. A real upgrade

Pass, in full, including the two things the plan says are only proved
against fixtures.

`POST /api/upgrade {"branch":"grain/task-121"}` at 18:43:39, finished at
18:43:47. Per-step progress was visible in `/api/upgrade` as it went --
polled at four seconds in, it read `step 3 of 6: sandbox image`, with
`pull image` and `health check` already `done` and their details filled
in. The finished run:

    pull image     done  pulled 127.0.0.1:5000/grain:grain-task-121
    health check   done  the pulled image runs
    sandbox image  done  pulled 127.0.0.1:5000/grain-sandbox:new
    schema         done  unchanged: both builds are at schema 16
    record image   done  /var/lib/grain/image.env now names 127.0.0.1:5000/grain:grain-task-121
    restart        done

The restart is the real chain and not a stand-in: the daemon `touch`ed
`/var/lib/grain/control/restart`, `grain-restart.path` fired,
`grain-daemon.service` came back at 18:43:48 on the new image (`docker
ps` showed `grain-daemon` running `127.0.0.1:5000/grain:grain-task-121`),
and the control file was consumed. The UI was answering again by
18:43:53, so the gap was under ten seconds; a poll every five seconds
never caught it down.

**The state repo commit naming the build.** `grain state log` does not
exist -- the plan names a subcommand that is not there (`grain state`
offers `status`, `local`, `adopt`, `sync`, `reformat`, `check`, `format`,
`ci`, `key`). The reading is `git -C /var/lib/grain/state-repo log`, and
what it holds is right:

    Update grain state, on the first export by an upgraded grain

    Written by grain from its own database at 2026-09-06T18:44:19Z.

    This deployment has been upgraded since it last wrote here:

        from 127.0.0.1:5000/grain:main (built from a4b0fbbbf35d)
        to   127.0.0.1:5000/grain:grain-task-121 (built from b45afd56d357)

Both tags *and* both commits, on the first export after the restart and
not on the commit before it. The commits are the ones the two images were
actually built from.

**From the pane, in a browser.** The same upgrade was run twice more
from the button itself -- back onto `main`, then onto the branch again --
with the page left open across the restart. The step list filled in live
(`health check running 1s`, the four behind it `pending`, then `sandbox
image running`, then `schema running 2s`), and at the poll after the
restart, with no reload and no error on screen, the pane flipped
together:

    t+14s  Now running 127.0.0.1:5000/grain:grain-task-121. | Status: running
    t+16s  Now running 127.0.0.1:5000/grain:main.           | Status: ok

That is the whole of "the daemon restarts and the UI comes back on the
new build", seen from the browser that asked for it. The daemon it was
polling went away and came back underneath it, and the pane neither
errored nor got stuck on the old answer.

**The post-upgrade check.** Filed by itself, one cycle after the restart:
task 1, `Check the upgrade to grain/task-121`, author `upgrade`,
authorKind `automation`, capability `self-debug`, queued at the head of
the backlog with no write target. Its body carried the branch, the
result, the upgrade's own start and finish timestamps, the exact detail
string the pipeline reported, and `The build now running:
b45afd56d357...` -- the things a sandbox could not find out for itself.
`upgradeCheckMode` was `auto` and `upgradeCheckPausesPipeline` false, so
it landed approved and held nothing, which is the documented default.

The check task could not run: this host has no agent credential and no
real sandbox guest (below), so it failed at setup and retried each cycle.
That the task is *filed*, correct, and dispatched is what step 3 asks
for; whether an agent can answer it is section 4's business, not this
one's.

## 4. The pipeline pause

Pass, both directions.

`POST /api/pipeline-pause {"reason":"hand run: section 2 step 4"}` at
18:45:05 returned `pausedNow: true` with `byKind: human`, `byId:
operator`, and the same reading appeared on `GET /api/config` as
`pipelinePause` -- which is what the banner stands on, so a pause taken
through the API shows up on every page and not only in the pane that took
it.

With the pause on, a task was filed (`POST /api/tasks`, then
`/approve` -- it lands `proposed` and needs approving, worth knowing if
you run this yourself) and both tasks were watched for two and a half
minutes, five reconcile cycles at 30s:

    18:45:14  task 2 filed and approved, queued
    18:47:28  task 1 attempts=2 (last 18:44:20, before the pause)
              task 2 attempts=0

Nothing was dispatched. `DELETE /api/pipeline-pause` at 18:47:42 answered
`resumed: true`, and the queue drained on the next cycles:

    18:47:50  task 1 attempt 3 starts (8s after the pause was lifted)
    18:48:20  task 2 attempt 1 starts

One at a time, which is `max workers: 1` doing its job rather than
anything to do with the pause.

**The banner.** Taken again at 18:57:17, with a browser open this time,
it stood on every page:

    No new tasks are being picked up since 11:57:17 AM: this deployment
    is paused. Runs already in flight are left to finish.
    -- hand run: what the banner says            [Resume now]

The reason given at the API is quoted back, and "Resume now" on the
banner lifted the pause: the banner disappeared without a reload and
`GET /api/pipeline-pause` read `paused: false` immediately afterwards.
The timestamp is rendered in the *browser's* zone (the pause was taken at
18:57:17 UTC, and this browser was on US Pacific), which is worth knowing
for anybody comparing a banner against a log but is the same convention
the rest of the UI uses.

Observation: while paused the daemon logs nothing at all. `docker logs
grain-daemon` over the whole pause is silent -- no line saying the
deployment is declining to pick work up, and no line when the pause is
lifted. An operator looking at the UI sees the banner; an operator
looking at the logs of a deployment that is dispatching nothing sees a
daemon that appears to be idle for no reason.

## Section 10: the sandbox the upgrade moved

This is the claim `docs/test-plans/2026-09-05.md` section 10 says nothing
proves -- that a task dispatched after an upgrade lands in a sandbox
built from the *new* image, not merely that `cmd/grain/sandboximage.go`
resolves the reference per daemon start.

On this host, after the upgrade and with no re-run of `setup.sh`:

* the new daemon said so at start:
  `grain daemon: sandbox image 127.0.0.1:5000/grain-sandbox:new (stamped
  into this build; nothing pinned one)`
* every sandbox created afterwards came from that reference. `docker ps
  -a` on the host, after the two tasks above were dispatched:

      kontur-vm-g-1-1-netns  127.0.0.1:5000/grain-sandbox:new
      kontur-vm-g-1-2-netns  127.0.0.1:5000/grain-sandbox:new
      kontur-vm-g-1-3-netns  127.0.0.1:5000/grain-sandbox:new
      kontur-vm-g-2-1-netns  127.0.0.1:5000/grain-sandbox:new

  and konturctl's own argv, quoted back in each attempt's failure detail,
  names it: `docker run -d --name kontur-vm-g-1-1-netns ... --entrypoint
  /usr/local/bin/kontur 127.0.0.1:5000/grain-sandbox:new sleep`.

The deployment was *installed* against `:old` and had `:old` on disk; a
restart on the new build was all it took for the next sandbox to be
reached for from `:new`, with nothing on the host re-run or re-configured.

It moves back, too. The upgrade run in the other direction (back onto
`main`, from the pane) put the daemon on a build stamped with `:old`
again, and the host's own record of every sandbox this deployment ever
created is the clearest single reading of the whole claim -- the cut is
at 18:55:57, the second the daemon restarted:

    kontur-vm-g-1-1-netns  ...grain-sandbox:new   18:43:50
    kontur-vm-g-1-2-netns  ...grain-sandbox:new   18:44:20
    kontur-vm-g-1-3-netns  ...grain-sandbox:new   18:47:50
    kontur-vm-g-2-1-netns  ...grain-sandbox:new   18:48:20
    ...
    kontur-vm-g-1-5-netns  ...grain-sandbox:new   18:54:50
    kontur-vm-g-3-1-netns  ...grain-sandbox:old   18:55:57
    kontur-vm-g-3-2-netns  ...grain-sandbox:old   18:56:27

which is `sandboximage.go`'s "a rollback to an older tag asks for its
*own* older sandbox" happening on a host rather than in a unit test.

The honest limit: the guest could not boot. The sandbox images in this
run are stand-ins -- a real one is
`scripts/kontur/build-guest.sh`'s output, which boots a published kontur
base image to provision it and takes 45 minutes of a KVM runner, and the
published base was not reachable from this host. So each container was
*created* from the right image and then failed on `exec:
"/usr/local/bin/kontur": no such file or directory`. What is proved is
that the deployment reaches for the new sandbox image after an upgrade
and that docker resolves it; what is still unproved is a task actually
running inside a guest of the new image and doing work there.

## What this run could not reach

* **A GCE VM from `terraform/gcp`.** No GCP credential was available. The
  host here has the shape section 2 depends on (systemd, docker, KVM,
  `setup.sh` run as root) but not the provisioning path around it, so
  nothing in this run says anything about the terraform module, the
  persistent disk at `/var/lib/grain`, or nested virtualisation on a real
  `machine_type`.
* **GHCR.** Both images came from a registry on the host. The pull
  step was exercised for real, but not against a private package, a
  `docker login`, or the network conditions a real pull has.
* **A booting sandbox guest**, as above -- which also means no run ever
  reached an agent, so nothing here says anything about what a task does
  after an upgrade, only about what is dispatched and where it is
  dispatched to.
* **A phone or a second browser.** The browser leg was headless Chromium
  only.

## What should follow

Nothing here is a bug worth filing on its own. Two small things a reader
of this file may want to act on:

* the plan's step 3 says `grain state log`, and there is no such
  subcommand. Fixed in the plan by this change.
* a failed upgrade leaves its remaining steps `pending` rather than
  `skipped`, and a paused pipeline says nothing in the log. Both are
  cosmetic, both are one line each, and neither is worth a task unless
  somebody is in that code anyway.
