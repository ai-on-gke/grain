# Walkthrough: `/next/agents` per-run CPU, selection and pane tabs (2026-11-14)

Plan: [`docs/test-plans/2026-11-14.md`](../test-plans/2026-11-14.md) §5 ("`/next/agents` per-run CPU, selection and pane tabs", PR #1488), items 1–4 under "What is NOT proved". Task 5730, filed by [Task #5629](/tasks/5629).

## Environment

- **Commit:** `main` at `3cc8bc207` (no change on `main` while the walk ran), plus fix 1 below on branch `grain/53549e42/task-5730-walkthrough-next-agents-per`.
- **Host:** Debian 12, kernel 6.1.0-54-amd64, 7 vCPU, 31 GiB. Go 1.26.10, Node 20.19.0, Playwright 1.62.1 Chromium, stress-ng 0.15.06, sysstat (pidstat) 12.6.1, bubblewrap 0.8.0, strace 6.1, Docker 29.9.3.
- **Build:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain`, rebuilt after the fix for the re-walk.
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes` (SQLite store; `-container-sandboxes` for item 2) over the GitHub simulator `ghsim.go` and a bare `acme/widgets`, `-max-workers 2`/`3`, `-agent-framework antigravity -agy-path fake-agy.py`. The daemon has a data directory, so every `run_command` runs confined under bwrap (`pkg/mcp/host_confine.go`), the way a real host deployment runs.
- **Stubbed:** only the model. `fake-agy.py` reads the real prompt and then calls the run's real `grain-sandbox` MCP server: a `run_command` that holds the CPU load, then `nothing_needed`. So the load runs where an agent's commands run, under the agent's own process tree.
- **Workstreams UI:** `grain demo -github-sim -fake-ci -reconcile 2s` (its seeded stream `director:director:8`, nodes 8 and 11).

Harness: [`2026-11-14-agents-cpu/`](2026-11-14-agents-cpu/):

- `env.sh`, `ghsim.go` and `fake-agy.py` are adapted from the 2026-11-11 durable-tasks walk. `fake-agy.py` now reports a tool step as ACTIVE before a long `run_command` returns.
- `item1_accuracy.sh`, `item1_edges_daemon.sh` and `edges.go` (`//go:build ignore`) cover item 1. `item2_container.sh` covers item 2. `sweepcost.go` and `item3_overhead.sh` cover item 3.
- `agents_probe.mjs` (three tabs, card text and screenshots), `keyboard_probe.mjs` (item 4) and `start_live.sh` are the browser probes and the live daemon they drive.
- Captured output is under `out/`.

## Results

| # | Item | Result |
|---|---|---|
| 1 | Accuracy, two concurrent runs at 1 and 2 cores | **pass**: mean 0.998 / 1.996 cores against pidstat 1.000 / 1.999 |
| 1a | Run directories that are prefixes of each other (`…/5-1`, `…/5-10`) | **pass**: 1.00 / 2.00, nothing crosses |
| 1b | Child that `cd`s to `/tmp` | **pass**: 1.00 (counted by ancestry) |
| 1c | Double fork reparented away | **pass under bwrap** (1.00); **lost when unconfined** (0.00, known limit) |
| 1d | Burst of short reaped children, then the parent exits | **pass**: ≈1.00 → 0.41/0.59 → 0, never negative, no spike |
| 1e | The right numbers on the right `/next/agents` cards | **pass** |
| 2 | Non-host backend shows "not sampled" with a reason; `GOOS=darwin` build | **pass**, wording finding F2 |
| 3 | `/proc` sweep cost at ~500 processes, three tabs, coalescing | **pass**: 12.3 ms per sweep; concurrent callers share one sweep |
| 4 | Enter/Space selection; `?pane=` through reload and back/forward, 1440/390, light/dark | **fail → fixed** (fix 1); 58/58 checks after |

### 1. Accuracy (`item1_accuracy.sh`)

Two runs, task 2 (`stress-ng --cpu 1`) and task 3 (`--cpu 2`), dispatched together into `runs/2-1` and `runs/3-1`. The script sampled `GET /api/sandboxes` every 2 s for 16 samples, with `pidstat -u -C stress-ng 2` alongside. The pidstat rows are mapped to runs by each worker's `/proc/<pid>/cwd`.

| run | `/api/sandboxes` mean (min–max) | pidstat mean |
|---|---|---|
| `2-1` (1 core) | 0.998 (0.971–1.004) | 1.000 |
| `3-1` (2 cores) | 1.996 (1.942–2.008) | 1.999 |

That is within 0.3% of pidstat, against the plan's ±20%. The first reading of a new run is a 252 ms settle window (`cpuSettle`); every reading after it is the 2 s poll gap.

**On the cards (`agents_probe.mjs`, the real daemon):** `#2 Load one core` shows "CPU 1.1 vCPU … 16% of this host's 7 vCPU", and `#3 Load two cores` shows "2.2 vCPU … 31%", the same values `/api/sandboxes` returned at that moment (1.095 and 2.187). The ~10% overshoot of that one reading came from strace, which item 3 had attached to the daemon at the time: it slows each sweep to seconds. See "Measurement note" under item 3. Sampled without strace, the same runs read 0.999–1.002 and 1.997–2.004 over 3.1 s windows.

**Edge cases through the real daemon** (`item1_edges_daemon.sh`, under bwrap, 2 s samples):

```
         cd-tmp   double-fork  burst
02:18:13 1.004    0.999        1.004
02:18:17 1.001    1.001        1.01
02:18:21 0.998    0.998        0.588   <- loop ends half way
02:18:23 0.999    0.999        0
02:18:32 1.001    1.001        0
```

- **`cd /tmp`:** the stress-ng worker's cwd is `/tmp`, and its parent chain (stress-ng → bwrap → mcpserver → agent) reaches the agent's cwd in the run. It is counted in full.
- **Double fork:** `(cd /tmp && setsid stress-ng … &)` inside `run_command`. bwrap's PID namespace makes the orphan's new parent the command's own shell, the namespace's init, inside the tree (`ppid_comm=bash cwd=/tmp`), so nothing is lost. `--die-with-parent` also means it cannot outlive the `run_command`.
- **Burst:** a shell loop of `sh -c` children, each a few milliseconds of arithmetic, reaped into the loop's `cutime`. The readings were 0.99–1.01 while it ran and 0.41 or 0.59 for the window in which it stopped, then 0. There were no negative rates and no spike: when part of a tree is reaped outside it, `total < p.total` restarts that sandbox's window (`hostCPUSampler.sample`).

**Edge cases against `orchestrator.HostSandboxes` directly** (`edges.go`, `out/edges.txt`). These are real processes with no bwrap, under a bash anchored in the run directory the way the agent process is:

- **Prefix directories:** `5-1` and `5-10` were acquired side by side. They read 0.988–1.003 and 1.995–2.015 on every sample, so `safepath.Within` never lets `5-1` claim `5-10`.
- **`cd /tmp`:** 0.990–1.003, counted.
- **Double fork that keeps its cwd:** 0.990–1.003, counted. The orphan is reparented to init, but its cwd is still the run's directory.
- **Double fork that `cd`s to `/tmp`: lost, reading 0.000 while the orphan burns a core.** It has left both the directory and the tree, and only its working directory or an ancestor could have attributed it. This is the plan's "is its CPU lost?": yes, but only on a host sandbox whose commands run unconfined. A daemon with a data directory always confines them under bwrap (`pkg/daemon/run.go` `WithHidden`), and there the orphan stays inside the run's PID namespace (above). It is recorded as a known limit; no fix is proposed.
- **Burst:** 1.004–1.030 while the loop ran, then absent once its parent exited, never negative.

Harness artefact, recorded so nobody repeats it: `stress-ng` refuses to start in a directory it cannot write to ("temp-path '.' must be readable and writeable"). A first `cd /` double-fork variant therefore read 0 because nothing ran, not because anything was lost. The scripts use `cd /tmp`.

### 2. Other backends (`item2_container.sh`)

`-container-sandboxes` with `docker.io/grainai/sandbox-k8s:latest`. One task ran a CPU-bound loop in container `grain-sandbox-2-1`. `GET /api/sandboxes` answered `{"enabled": true, "sandboxes": null}`: `ContainerSandboxes` implements no `Health`, so the run has no row at all. `/next/agents` therefore takes `cpuNote`'s no-row branch. The card says "not sampled", with the tooltip and screen-reader text "Not sampled: this deployment reported no sandbox for this run yet." That is never a dash or 0, as the plan requires; `ui-ng/e2e/next-agents.spec.js` asserts the same text for `grain demo`'s sandbox-less run.

**F2 (P3, wording, not fixed):** "yet" promises a reading that a container-backend deployment will never produce, because the backend never reports a row. Giving `ContainerSandboxes` a `Health` (`docker stats`) or a per-backend note would make it truthful. That is a feature beyond this walk. Granule, Kubernetes and hosts were not available in this sandbox; their `Health` paths report a load average, which the `probe` branch of `cpuOf` already covers in Vitest.

`GOOS=darwin go build ./pkg/sysstat/...` and `GOOS=darwin go vet ./pkg/sysstat/` both succeed (`proctree_darwin.go`).

### 3. Overhead (`sweepcost.go`, `item3_overhead.sh`)

- **Sweep cost:** 571 processes on the host (450 idle `sleep`s added). Over 50 sweeps, one `sysstat.ProcessTreeCPU` sweep of the real `/proc` took **p50 12.3 ms, p90 13.9 ms, max 17.7 ms**. A run whose window is new costs two sweeps and the 250 ms settle wait.
- **Three tabs:** the host was padded to 515 processes, with two loaded runs live and `/next/agents` open in three pages of one browser for 60 s. strace counted the daemon's sweeps by its `readlinkat` of one idle sentinel's `/proc/<pid>/cwd`, which only `ProcessTreeCPU` reads. The tabs made 15 `GET /api/sandboxes` requests (each tab polls every 15 s, `SLOW_POLL_MS`), and the daemon swept 15 times. The closest two sweeps were 2.85 s apart, never inside the 2 s `sandboxHealthTTL`.
- **Concurrent callers:** three simultaneous `GET /api/sandboxes` requests caused **one** sweep (`out/item3/concurrent.txt`).

So the sweeps are coalesced as designed: callers inside one 2 s window share a sweep, and tabs whose polls fall in different windows each get their own. Three staggered tabs cost three 12 ms sweeps every 15 s, about 0.25% of one core.

**Measurement note:** with strace attached (`-f`), each sweep stops on 500+ syscalls and takes seconds. The sampler stamps its reading before the sweep starts, so a slow sweep skews the window it ends against the one it begins. The two readings around the traced period were 0.77 and then 1.10 cores for a 1-core run, together about right. Untraced sweeps take 12 ms against 2–15 s windows, a bias under 1%, so no change is proposed. Stamping the middle of the sweep would remove even that, if hosts with very slow `/proc` ever matter.

Also seen: `fake-agy.py` first emitted nothing while a long `run_command` blocked, so grain's "1m40s retry cutoff" restarted the agent turn (`--conversation conv-2`). That killed the confined command with its parent. During the 30 s gap the run had no process in its directory, and `/api/sandboxes` correctly gave it no `cpuCores`: "none of this run's processes were found". This was a harness artefact, fixed in `fake-agy.py` by reporting the tool step ACTIVE first.

### 4. Keyboard and tabs in a real browser (`keyboard_probe.mjs`)

The probe ran against the real daemon's two live runs (`/next/agents`) and `grain demo` (`/next/workstreams/director:director:8`), at 1440×900 and 390×844, light and dark.

**Bug F1 (P1): Enter on a card's CPU reading opened the previously selected task.** It was reproducible every time at 1440 px, in light and in dark:

1. Open `/next/agents` with two running tasks. The first, task 2, is selected and shown in the pane.
2. Tab to the CPU reading inside the second card (task 3). It has `tabindex="0"` for its tooltip.
3. Press Enter. The page navigated to `/next/task/2?from=agents`, the full page of the task *previously* selected, not the card the focus was in.

Cause: `pickKey` returned early unless the card itself was the event target, so the key bubbled to the page's document-level Enter shortcut ("open the selected task full screen"). **Fix 1** (`ui-ng/src/app/agents/agents-page.component.ts`): Enter or Space from anything focusable inside a card or row selects that card, the way a click there already does (`pick`). A link, button or field inside keeps its own key. After the fix the same keys give `/next/agents?task=3`, and on phone they open `/next/task/3`, the card's own task. Regressions:

- Vitest `selects the card whose CPU reading has focus on Enter, rather than opening the previously selected task`. It fails on the old code (`1 failed | 41 passed`) and passes with the fix.
- Vitest `leaves Enter on a link inside a card to the link`.
- `ui-ng/e2e/next-agents.spec.js` `Agents: Enter or Space on a card's CPU reading selects that card, not the pinned task`.

**Everything else passed before and after the fix (58/58 checks after):**

- Desktop `/next/agents`:
  - The first card is selected by default.
  - Enter on a card selects it and the page stays on `/next/agents`; Space does the same. Both work the same on list rows.
  - Enter with nothing focused opens the selected task, as designed.
- Desktop `?pane=` on `/next/agents?task=3&pane=runs`:
  - The Runs tab is current, and it still is after a reload.
  - Clicking Details writes `?pane=details` and keeps `task=3`.
  - Back returns to `pane=runs` and the Runs tab; forward returns to Details.
- Phone `/next/agents`:
  - Enter and Space open that card's own task (`/next/task/3`, `/next/task/2`).
  - The `?task=3&pane=runs` deep link loads.
- Workstreams, `?node=8&pane=runs` at both widths:
  - Runs is current, and still is after a reload.
  - Details, back and forward behave as on Agents. At 390 px the tab strip sits in the closed phone inspector sheet; opening it with `o` shows Runs, and the same steps then pass.
  - Enter on tree node 11 selects node 11, not the previously selected node.
- The plan's example `?pane=logs` is not a tab name (`conversation`, `runs`, `changes`, `tree`, `details`). `g-task-detail` ignores it and shows Conversation, so the probe used `runs`.

**UI review for fix 1:**

- The change is keyboard behaviour only; no markup or style changed.
- `make ui-screenshots`-equivalent shots (`ui/e2e/screenshot.mjs --pages /next/agents,/next/workstreams --widths desktop,phone`, light and dark) are attached to the task as `contact-sheet.png`. `agents_probe.mjs` also shot `/next/agents` over the real daemon at the same four sizes: no horizontal overflow (`scrollWidth − innerWidth = 0` at all four).
- No "before" sheet was taken: the change renders the same pixels.
- `next-full-system.spec.js` passed (5/5), and so did `next-agents.spec.js` (2/2).
- The sandbox's file reader cannot display PNGs, and `LOOK=1` has no key here, so layout was checked by measurement, not by eye.
- The design book's three independent reviews (Design, Functional, Adversarial) could not be run by separate reviewers from inside this run. The Functional and Adversarial checks above are the author's own, so they are left to the pull request's review.

## Fixes

1. `fix(ui-ng): select an Agents card from Enter on its CPU reading`: F1 above, with Vitest and e2e regressions.

## Follow-ups

- F2 (P3): the container backend reports no `/api/sandboxes` row, so its runs read "not sampled … yet" for good. This is not filed: it is wording on a backend with no per-run reading, not a defect in PR #1488.
- Unconfined host sandboxes lose a daemonized child that leaves the run directory. This is a known design limit of attribution by working directory and ancestry; confined runs (the default with a data directory) keep it.
