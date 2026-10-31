# Daily auto-upgrade, `rc` channel, and the Settings → Upgrade restart report (2026-10-28 plan §5)

Task 1718 (filed by task 1549). This is the walkthrough in §5 of
`docs/test-plans/2026-10-28.md` in the retired `bwsalmon/grain`. It was run on
2026-10-30 against `ai-on-gke/grain` `main` at `8bdfa3a5`.

**Outcome:** pass for everything §5 covers. The daily auto-upgrade fired on its
channel. It held the queue, cut over under live claude, codex and Director
runs, and released the hold on the new build. All five runs were adopted, and
the Settings → Upgrade report and the post-upgrade check matched SQLite run
for run. A day with nothing new took no hold. `rc` resolves to `candidate`,
and a discarded RC is labelled whether or not it has a replacement. The CI
adoption and cache regression tests still pass.

There was one finding, outside the feature under test: a config pull request
opened from a Settings refusal was closed unmerged on the next tick. It is
fixed here, with a regression test.

## Environment

- The grain task sandbox: Debian 12.15, kernel `6.1.0-54-amd64`, 7 vCPU,
  15.6 GiB, Go 1.26.10, Node 20.19, Playwright Chromium (headless shell) from
  `/var/cache/playwright`. `sqlite3` came from apt.
- The frontend came from `cd ui && npm ci && npm run build`. It is embedded in
  every binary below.
- Every binary was built with `-tags grain_e2e`, so `-sandbox-dir` gives
  host-directory sandboxes, as the browser e2e suite uses.

### The two stamped binaries (the local channel)

A bare repository, `/home/debian/wk/channel.git`, stood in for the
deployment's upstream. It was cloned from this checkout with two commits on
top. The first changes the `Makefile`'s `container-build` target to
`CGO_ENABLED=0 go build -tags grain_e2e -o bin/grain ./cmd/grain`, since
there is no builder container in a sandbox. The second adds one comment line.
That is the 2026-10-15 and 2026-10-12 recipe; the 2026-10-27 run stamped its
revisions by hand instead.

| Binary | Commit (VCS stamp) | `grain version` |
|---|---|---|
| v1, started first | `578af23288c5243e641f608eba3132bd8401b3b7` (`main`, then `candidate`) | `v0.0.0-20261030105941-578af23288c5 (unversioned) · 578af23` |
| v2, what auto-upgrade built and installed | `f04b077b756d1d982d1f60e09f6625ec1cbbc0b6` (`main` after the advance) | `v0.0.0-20261030105941-f04b077b756d (unversioned) · f04b077` |
| RC demo, for the discarded-RC label | this tree, `-X …/pkg/version.Release=v0.10.0-rc.1` | `v0.10.0-rc.1 · 8bdfa3a-dirty` |
| Fix build, for the live check of the finding | this tree plus the fix | `…8bdfa3a56130+dirty` |

`-upgrade-src-dir` was a clone of `channel.git`, sitting on `main` at v1's
commit. The checkout, the build, the install, the health check, the schema
step and the restart were all grain's own source upgrader.

### The deployment

The daemon command line:

```
GRAIN_IN_CONTAINER=1 TMPDIR=<wk>/data/tmp grain-installed daemon
  -data-dir <wk>/data -sandbox-dir <wk>/sbx -ui-addr 127.0.0.1:8420 -ui-trust-loopback
  -sandbox-port-base=0 -agent-framework claude -claude-oauth-token-file <wk>/claude-token
  -codex-auth-file <wk>/codex-auth.json -hostd-socket <wk>/hostd.sock
  -claude-run detached -codex-run detached -poll-interval 2s -max-workers 4
  -upgrade-src-dir <wk>/src -upgrade-install-path <wk>/grain-installed
  -upgrade-restart-cmd <wk>/restart.sh -upgrade-allowed-branches main,candidate,rc
  -upgrade-drain-window 45s
```

- `GRAIN_IN_CONTAINER=1` together with `-hostd-socket` is how a container
  install finds grain hostd (`DetectHostExecutor`). `TMPDIR` under the data
  directory is what `scripts/setup.sh` gives the container (`TMPDIR=…/tmp`).
  Without it, hostd's `paths_under` refuses claude's `--mcp-config` in
  `/tmp`, exactly as it should.
- `grain hostd -socket <wk>/hostd.sock -allowlist /etc/grain/hostd.yaml` ran
  v1's binary. The allowlist is root-owned, has `-ensure-builtins`' entries,
  and has `claude-detached` and `codex-detached` entries copied from
  `hostd_allowlist_content` in `scripts/setup.sh`. `current/codex` is the
  installer's `grain codex-detached` wrapper.
- `restart.sh` stood in for `systemctl restart`. It SIGTERMs the daemon,
  waits for it to exit, and starts the installed binary with the same flags.
- Settings → Models had two connections: `walk-claude` (claude, ambient) and
  `walk-codex` (codex subscription, with an `auth.json` uploaded through
  `PUT /api/agent-backends/walk-codex/codex-auth`). Settings → CLI run
  locations set claude and codex to `detached`. `GET /api/settings/cli-run`
  resolved both to `hostd`, entry `claude-detached` / `codex-detached`, "so it
  outlives a daemon upgrade".

### Stubs

| Boundary | Stub | Why |
|---|---|---|
| The claude and codex CLIs | Shell scripts at hostd's `current/claude` and `current/codex.bin`, based on `tests/installer/zz_hostd_test.go`'s fakes. Each logs its pid to `reports/*.starts`, then streams one numbered tick a second (claude stream-json, codex `item.completed`) until `release-claude` / `release-codex` exists. | There is no model access in a sandbox. Each run is held open across as many restarts as needed. |
| The upstream channel | The local bare repository described above | Allowed by the plan: "registry → local files" |
| The image registry (discarded RC only) | A 20-line Python `http.server` on `127.0.0.1:5000` that answers `/v2/` and `tags/list` from a file. Its other routes answer 404, and it sends HTTP/1.0, so the plain-HTTP fallback for a loopback registry engages, as in the 2026-10-15 run. | There is no registry in a sandbox |
| `systemctl restart grain-daemon` | `restart.sh` | There is no systemd unit in a sandbox |

Nothing of grain's own was stubbed. That includes the daemon, hostd, the
`codex-detached` wrapper, the upgrader, the store, the UI and the config
repository.

## Steps and observations

### 1. Seeding: a claude run, a codex run, and a console session

`POST /api/tasks` filed no-repo tasks on `fake-claude-model` and
`fake-codex-model`. A comment on task 1 started a turn of the Director
session, which is the console session every deployment has. That turn ran on
codex.

`POST /api/console-sessions` for a second session is a configuration write.
Under internal config mode it is answered `409 config_managed_externally`,
which is by design (`docs/external-config-repo.md`), so the Director stood in
for it.

The first attempts failed, and taught the setup two things:

- Claude refused `/tmp/…mcp-config…json`, which is outside `paths_under`,
  until `TMPDIR` was set.
- Codex had "no Codex credential of its own" until the per-connection
  `auth.json` was uploaded through the API.

Once both were fixed, the live runs were:

- `6-1` and `4-3`: claude
- `3-3` and `5-1`: codex
- `1-1`: the Director session, on codex

All five were detached in hostd. Two plain SIGTERM restarts before the
upgrade logged `grain hostd adoption: 2 adopted, 0 lost` and then
`5 adopted, 0 lost`.

### 2. The daily auto-upgrade fires on `main`, holds the queue, adopts everything, and releases the hold

Auto-upgrade had to be set to channel `main` at `11:00` UTC.
`PUT /api/settings` is refused with a patch in internal config mode, and the
config pull request it leads to closed itself (that is the finding below). So
the two columns were first written into `grain_config` directly, as a merged
sync would. With the fix, the same write was later made through a config pull
request and applied (see "Findings").

The journal (v1, then v2):

```
11:14:06 orchestrator: scheduled the automatic upgrade to main for 2026-10-30 (11:00 UTC)
11:14:06 orchestrator: holding the queue until this deployment is idle, then upgrading to main (4 runs still to finish)
11:14:08 … dispatch: candidate 2 skipped: dispatch is paused         (every tick until the cut-over)
11:17:40 upgrade: main: checkout → main at f04b077b (walkthrough: advance main by one comment line)
         build 6s, install, health check "the installed binary runs", schema "unchanged: both builds are at schema 65"
11:17:46 runhost: upgrade flushing; leaving run 3-3's CLI running in grain hostd at cursor 70742   (and 6-1, 5-1, 4-3, 1-1)
11:17:46 grain daemon: upgrade: flushed the cursors of 5 host CLI run(s) in grain hostd before the restart: [6-1 4-3 3-3 5-1 1-1]
11:17:46 upgrade: main: this daemon is being stopped: the restart is under way, so the queue hold and the flushed runs are left for the next startup
11:17:46 grain daemon: shutdown interrupted 0 run(s)
--- f04b077 starts ---
11:17:47 orchestrator: adopted grain hostd process hostd:p-… for run 1-1 at cursor 64862 after a 0s gap      (×5)
11:17:47 grain daemon: grain hostd adoption: 5 adopted, 0 lost, 0 unclaimed process(es) released
11:17:47 orchestrator: an upgrade held the queue over this restart -- the pipeline is picking work up again
11:17:47 orchestrator: filed task 8 to check the upgrade to main (auto)
```

- The hold counted "4 runs still to finish" against 5 live ones. The Director
  session is the one not waited for, which is what the pane says ("neither is
  an open session").
- With no drain window, the hold waited for the four runs, which my stubs
  never end. Restarting with `-upgrade-drain-window 45s` let the scheduled
  upgrade cut over with the runs live. The `restart.sh` log shows SIGTERM at
  `11:17:46.64` and v2 starting at `.84`.
- The hold was left in place for the next startup (c0f1d313b), and that
  startup released it.

**The restart report against SQLite.**

| Source | Adopted | Lost | By framework | Sessions |
|---|---|---|---|---|
| `GET /api/upgrade` `lastRestart` | 1-1, 3-3, 4-3, 5-1, 6-1 | none | codex ×3, claude ×2 | 1-1 `session: director` |
| Settings → Upgrade (desktop and phone) | "Last restart (10/30/2026, 11:17:47 AM): 5 adopted, 0 lost." | | | |
| Settings → Upgrade, at risk | "5 runs are in flight … 5 are in a run host or grain hostd and would be adopted by the new daemon." `atRisk = {lost: 0, survive: 5}` | | | |
| Task 8, "Check the upgrade to main" | "5 adopted, 0 lost; of those, console and Director sessions: 1 adopted, 0 lost; 0 CI jobs adopted, 0 lost", then one line per run naming its framework and "(the Director session)" | | | |
| `task_run`, SQLite | 1-1, 3-3, 4-3, 5-1, 6-1 all unfinished, and still attempt 1 or 3 as before (no new attempt for tasks 1/3/4/5/6) | 0 rows finished `interrupted` across the upgrade | codex: 1-1, 3-3, 5-1; claude: 4-3, 6-1 | task 1 is the Director |
| hostd and the stubs | `reports/claude.starts`: 2 pids, `codex.starts`: 3 pids, each started once and never again across four restarts | | | |

"CLIs an upgrade interrupts" (`upgradeInterrupts`) named only agy and custom,
which have no detached location. claude and codex, being detached, were
correctly left out.

**The adopted runs to the end.** `release-claude` and `release-codex` were
touched at 11:26. Every adopted run then ended in its own attempt, with no
re-dispatch. Each transcript has every tick exactly once, with no gaps and no
duplicates, across the three restarts and the upgrade:

| Run | Ticks | Range |
|---|---|---|
| 6-1 | 1242 | 0–1241 |
| 4-3 | 1212 | 0–1211 |
| 3-3 | 1264 | 0–1263 |
| 5-1 | 1306 | 0–1305 |

The rows ended `failed: the agent made no tool calls at all`, because the
stubs never call a tool. That is the stubs' outcome, not adoption's. The
Director's turn `1-1` ended `parked`, as a session turn does.

The screenshots of Settings → Upgrade on the upgraded deployment (1280 px
desktop and 390 px phone, light) are attached to the task's closing note.

### 3a. Nothing new: no queue hold

`auto_upgrade_run.day` was moved back a day, with `main` still at the running
commit:

```
11:19:25 orchestrator: the automatic upgrade to main for 2026-10-30 is not scheduled -- main is already running the build it would upgrade to
```

There was no `scheduled_upgrade` row, no `dispatch is paused` line after
11:17:44, and no restart. The day was still recorded (639a22f4e).

### 3b. Channel `rc`

- Setting `autoUpgradeChannel` to `rc` was refused with `409` and a real
  patch (`"auto_upgrade_channel": "rc"`), which means it validated. A bogus
  branch is refused `400` "matches none of main, candidate, rc".
- With `candidate` at the running commit (f04b077):
  `the automatic upgrade to rc for 2026-10-30 is not scheduled -- rc is already running the build it would upgrade to`.
  So `rc` was resolved through `candidate`.
- With `candidate` moved back to 578af23: `scheduled the automatic upgrade to rc …`
  and `holding the queue … (4 runs still to finish)`. Then
  `DELETE /api/upgrade/scheduled` logged `no upgrade is scheduled any more -- the pipeline is picking work up again`,
  with no restart.

### 3c. A discarded RC is labelled

This used `grain demo -upgrade-image 127.0.0.1:5000/x/grain` on the
`v0.10.0-rc.1` binary.

- Tags `latest candidate main v0.9.0 v0.10.0-rc.2`:
  `discardedRc = {running: v0.10.0-rc.1, latest: v0.9.0, replacementRc: v0.10.0-rc.2}`.
  The pane says "Release candidate v0.10.0-rc.1 was discarded and replaced by
  v0.10.0-rc.2 … Upgrade to candidate (v0.10.0-rc.2) or move to latest
  (v0.9.0)", with the "upgrade to candidate (v0.10.0-rc.2)" action.
- Tags `latest candidate main v0.9.0 v0.10.0-rc.1-discarded`:
  `{running: v0.10.0-rc.1, latest: v0.9.0}`. The pane says "…was discarded. It
  failed qualification during soak testing and will not be promoted, and no
  replacement candidate has been cut yet … You can wait for v0.10.0-rc.2, or
  move to latest (v0.9.0)."
- The tag list is cached for `tagListTTL` (10 minutes), so the second case
  needed a fresh demo. That is by design.
- `localhost` does not resolve in this sandbox (its resolver is the cluster's
  DNS), so the registry was named `127.0.0.1:5000`. That is an environment
  limit.

### 4. Regression: CI-job adoption and the job cache

The 2026-10-27 run walked this live. Here it was regression-checked with that
run's own tests on this tree:

```
--- PASS: TestCIJobCacheKeysOnSandboxImageWithoutWipingOnUpgrade
--- PASS: TestCIRunAndInFlightJobSurviveDaemonRestart
--- PASS: TestCIRunWithLostSandboxMarksJobLostAndRecoversAsInterrupted
--- PASS: TestK8sGranuleCISandboxesSurviveDaemonRestartAndSparePodFromReap
--- PASS: TestUpgradeCheckReportsAdoptedAndLostRuns
```

The live deployment above had no CI job in flight, so its report reads
"0 CI jobs adopted, 0 lost".

## Findings

### F1 (fixed): a config pull request opened from a Settings refusal closes itself on the next tick

**Reproduction.** Any deployment in internal config mode with a task branch
template (every new deployment has `grain/{deployment}/task-{id}-{slug}`):

1. `PUT /api/settings {"autoUpgradeTime":"03:00"}` is answered `409` with `change`.
2. `POST /api/state-repo/pull-requests {"changes": <change>}` is answered
   `201`, for example task 9, branch `grain/task-9`.
3. Within one tick the task is `closed`, with `pullRequestEvents: closed` and
   no comment. Task 7 closed 32 ms after it was created, and task 9 0.8 s
   after. `git -C data/state-repo branch` still shows `grain/task-9`, unmerged
   and carrying the change.

**Cause.** `StateManager.OpenConfigPullRequest` commits the change to
`model.BranchName(id)` (`grain/task-<id>`) but stores the task with no
`Branch`, so `PutTask` gives it the template's expansion
(`grain/869d08ea/task-9-change-tables-grain-config`).
`StateRepoPullRequestClient.GetPullRequest` looks up the head by
`task.BranchName()`, finds no such ref, and answers `State: "closed"`.
`recordPullRequestEvents` then closes the task. Every config change a person
tried to make through the refusal's own instructions was thrown away.

**Fix.** The task literal names `Branch: branch`.
`TestOpenConfigPullRequestCommitsAValidatedHeldChange` now runs with
`DefaultTaskBranchTemplate`, asserts that the task names the branch the
commit is on, and asserts that the internal client reads the pull request as
open. Without the fix it fails with: `the task names branch "grain/…/task-1-change-tables-grain-config", but the change was committed to "grain/task-1"`.

**Live check.** The walkthrough deployment was restarted on a build with the
fix. A config pull request for `autoUpgradeTime: 04:30` (task 10) stayed
`awaiting_submit / not_auto_merge`. `POST /api/tasks/10/submit` merged it,
and `grain_config.auto_upgrade_time` read `04:30`.

### Observations, not bugs

- Upgrade-once-idle with no `-upgrade-drain-window` waits for detached runs
  as well, even though they would be adopted. That is the documented
  behaviour: "let the work already under way finish". Pressing Upgrade with
  the toggle off, or setting a drain window, is how an operator cuts over
  under live adoptable runs. It may be worth a follow-up to let a hold
  proceed once every remaining run is adoptable, since the pane already
  knows that ("5 are in a run host or grain hostd and would be adopted"). It
  was not filed, because it is a product choice.
- After the upgrade, the status still reads `phase: ok`, "built and
  installed; restarting". That is the finished upgrade's last record, with
  `running` naming the new build, not a stale state.

## Not run

- A second standing console session, because `POST /api/console-sessions`
  is a config write in internal mode. The Director session covered "console
  and Director sessions" in the report.
- A live in-flight CI job across the upgrade. That was walked on 2026-10-27,
  and here it was covered by that run's tests (step 4).
- A real image registry and an image-mode cut-over. The discarded-RC label
  was checked on `grain demo -upgrade-image`, which never restarts.
