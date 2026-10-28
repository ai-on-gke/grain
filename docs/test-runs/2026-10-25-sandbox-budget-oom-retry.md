# Sandbox budget admission, auto overcommit, and OOM-kill retry: walkthrough, 26 October 2026

grain/task-930 runs `docs/test-plans/2026-10-25.md` §4. It covers:

- Settings → Sandboxes and `/api/sandboxes`, driven live with Playwright
  over `grain demo`.
- The OOM-kill infrastructure retry, driven through the real
  `applySandboxOOM` path against a running demo's store and then viewed on
  `/tasks/<id>`.
- How an OOM-raised task interacts with budget admission, run through
  `dispatch.Cycle` over a real SQLite store.
- The `kubernetes-granule` Stage-1 off-cluster runtime, read on
  `origin/main` because it has not been ported to `candidate` yet.

## Summary

- **Settings → Sandboxes mostly works.** The page saves, the server
  checks values, and settings survive a reload. One bug was fixed: the six
  auto overcommit bound boxes put an invalid entry (`0`, `-2`) back
  without saying anything. Every other number box on the page says
  "… was not saved". I fixed it on this branch, then found the same fix
  had landed on `candidate` in parallel from the §7 walkthrough:
  `abac73447`, "fix(ui): refuse out-of-range auto overcommit bounds by
  name instead of dropping them". That version goes further, because it
  also refuses a half-typed `1e`. I rebased onto it and dropped my
  duplicate commit. The "After the fix" column below was observed with
  my version, which behaves the same for these inputs.
- **The OOM retry works as designed**:
  - A kill is recorded under the `oom` outcome. The task stays `queued`
    with no strike against it.
  - The conversation gets a note, and the row and API show the activity
    "retrying after OOM kill".
  - The second and third kills in a row each double `SandboxMemoryMB`
    (8192 → 16384 → 32768, the 4× ceiling).
  - `/tasks/<id>` shows the value in its "Sandbox memory (MiB)" row.

  One bug: the third kill's note said "It is the **second** OOM kill in a
  row" because the wording was hard-coded. **Fixed** (`fix(orchestrator)`
  commit).
- **An OOM-raised task larger than the whole budget does not deadlock.**
  - While anything else holds the budget, it waits with a quota-wait
    reason that names memory.
  - Once the ledger is empty, it is admitted alone (the `Sole` rule).

  The wait reason read like an ordinary wait for a slot, so it now adds
  ", more than the whole budget, so it starts only once no other sandbox
  is live" (`fix(budget)` commit). The remaining open question is
  **fairness**: smaller tasks are admitted past the oversized one, so on a
  busy deployment it can wait indefinitely. It stays visible but never
  starts. That is a design decision, raised with the owner below and not
  changed here.
- **`kubernetes-granule` Stage-1** (`d7d5e032b`, PR #4694, and the
  follow-up PRs) **is not on `candidate`** at `543ad3fd6`:
  `pkg/granule/forward.go` and `third_party/granule/pkg/forward` are
  absent. I read it on `origin/main` only. Nothing in it was tested live,
  since that needs a remote GKE cluster.

## Environment

| | |
|---|---|
| Branch | `grain/049f1e09/task-930`. The walkthrough ran on `origin/candidate` `543ad3fd6`, and the branch was later rebased onto `2a19c079c` |
| Host | Debian 12 sandbox VM, 7 vCPU, 15 GiB |
| Go / Node | go1.26.10 / v20.19.0 |
| Browser | Playwright Chromium 1234 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) |
| Demo | `bin/grain demo -observability -addr 127.0.0.1:4290 -access-code-file …`. The demo's budget is 4 vCPU / 16 GiB with 2 vCPU / 2 GiB reserved, and the overcommit state is synthetic: auto, CPU 2.75×, memory 1.25×, with one adjustment. |

Run notes for the next run:

- `make build` runs `npm ci --omit=dev` in `ui/`, which removes
  `@playwright/test`. Run `npm ci` again before any Playwright script.
- `pkill -f 'grain demo'` also matches the sandbox's own command wrapper
  and kills the shell. Use `pgrep -af bin/grain` and kill by pid.
- `LOOK=1` screenshots need `GEMINI_API_KEY`, and this sandbox has none.
  I compared before and after screenshots pixel by pixel with Pillow
  instead (`python3-pil` from apt).

## 1. Settings → Sandboxes and `/api/sandboxes`

I drove the page with a Playwright script: fill the box, blur it, then
read `PUT /api/settings`, `GET /api/settings`, the page's alerts and the
box's value back. I also called the API directly with curl.

### Overcommit mode (`auto` / `fixed`)

"auto" is a value of the **Overcommit mode** select. It is not something
you type into the CPU or memory ratio boxes, which are `type=number`.

| Step | PUT payload | Result |
|---|---|---|
| select `fixed` | `{"sandboxOvercommit":{"mode":"fixed","cpuMin":1,"cpuMax":6,"memoryMin":1,"memoryMax":2,"cpuPressureTarget":25,"memoryPressureTarget":5}}` | 200. Stored mode is `fixed`. The "Auto overcommit is active" banner goes away ✅ |
| select `auto` | the same, with `"mode":"auto"` | 200. The banner comes back: "live effective factors: CPU 2.75×, memory 1.25× (PSI: CPU 8.4%, memory 1.9%)" ✅ |
| curl `{"sandboxOvercommit":{"mode":"bogus"}}` | | 400 `sandbox overcommit mode "bogus": want "fixed" or "auto"` ✅ |

### CPU overcommit / Memory overcommit (the fixed ratios)

The results were the same for both boxes.

| Typed | PUT | Stored / shown |
|---|---|---|
| `1.5` | `{"sandboxCpuOvercommit":1.5}` | 1.5 ✅ |
| `0` | `{"sandboxCpuOvercommit":0}` | 0. This is valid: the help text says 0 leaves that dimension bounded by run counts alone ✅ |
| `-1` | none | Refused on the client: "CPU overcommit must be at least 0; -1 was not saved." The box goes back to its value ✅ |
| `auto` | none | The browser's number box holds nothing it can read. Nothing was saved and the box went back ✅ |
| `1e3` | `{"sandboxCpuOvercommit":1000}` | 1000 is accepted. There is no upper bound, here or on the server (see Findings) |
| empty | `{"sandboxCpuOvercommit":0}` | 0 ✅ |
| curl `-1` / `-0.5` | | 400 `sandboxCpuOvercommit must be 0 (unbounded) or more` ✅ |
| curl `"auto"` (a string) | | 400 `json: cannot unmarshal string into … float64` ✅. The message is accurate, though raw. |

### Auto bounds and PSI targets

| Typed | Before the fix | After the fix |
|---|---|---|
| Auto memory minimum `0` | No PUT, the box silently showed 1 again, no message ❌ | "Auto memory minimum must be at least 0.25; 0 was not saved." ✅ |
| Auto memory minimum `-2` | The same silent revert ❌ | "… must be at least 0.25; -2 was not saved." ✅ |
| Auto memory minimum `3` (max is 2) | PUT, 400 "sandbox overcommit memoryMin 3.00 is above memoryMax 2.00", shown as "Couldn't save Overcommit mode" | unchanged |
| CPU pressure target `150` | PUT, 400 "pressure targets are percentages, at most 100" | Refused on the client: "CPU PSI target (%) cannot be more than 100; 150 was not saved." No PUT ✅ |
| Auto memory minimum `0.5`, maximum `0.5` | saved | saved (a memory floor below 1× is allowed) |

### Default VM sizing

| Typed | Result |
|---|---|
| Sandbox vCPUs `4`, memory `16384`, disk `60` | Each one is saved. The commitment note recomputes to "4 workers + 1 merger + 1 CI job × 4 vCPU = 24 vCPU committed against the 4 this host has (6.0×)" ✅ |
| vCPUs `-1` | "Sandbox vCPUs must be at least 0; -1 was not saved." ✅ |
| memory `1.5` | "Sandbox memory (MiB) takes a whole number; 1.5 was not saved." ✅ |
| disk `0` | Saved as 0, and the box shows the default placeholder ✅ |
| curl `sandboxMemoryMb: -5` | 400 `sandboxMemoryMb must be 0 (unset) or at least 128` ✅ |

### Reload persistence

After a reload, the boxes read: mode `auto`, CPU `1.5`, memory `1.5`, auto
memory minimum and maximum `0.5`, vCPUs `4`, memory `16384`, disk empty
(0). These match `GET /api/settings` ✅.

### `/api/sandboxes` telemetry

- The response contains `host`, `budget`, `overcommit`, `reconciliation`,
  `packageCache`, `ciJobCache`, `lifetime` and the other keys. `budget`
  includes `mode`, `budgetCPUs` 4, `budgetMemoryMB` 14336, the committed
  figures, `withinCap`, and the overcommit state with its adjustment
  history ✅.
- **Demo-only inconsistency:** `budget.cpuOvercommit` and
  `memoryOvercommit` report the auto controller's 2.75× and 1.25×.
  `budget.summary` and `budgetCPUs` / `budgetMemoryMB` use 2.00× and
  1.00×. The demo builds a fixed budget at the default ratios
  (`pkg/demo/demo_observability.go`, `demoBudget`) next to a synthetic
  overcommit state, and `pkg/ui/sandbox_health.go` overlays the state's
  ratios onto the report. In a real daemon the budget is rebuilt from
  `Config.EffectiveOvercommit` on each tick, so the two agree. Not fixed.
  It only affects the demo.

## 2. OOM-kill retry, end to end

There is no demo flag that triggers an OOM kill. Instead, a throwaway
internal test (not committed) opened the running demo's
`/tmp/grain-demo-*/grain.db`. For demo task 11, "Extract pagination query
helper" (queued), it called `StartRun`, then `applySandboxOOM`, then
`FinishRun`, each with a fake `SandboxOOMReporter`. The reporter answered
"the sandbox container was OOM-killed (docker State.OOMKilled, exit 137)"
for a run that had failed with `exit status 137`. That is the same path
`execution.go` takes. The backend detection in the real code (docker
`State.OOMKilled`, granule exit 137 or a guest console line, k8s
`OOMKilled`) is covered by `TestGranuleOOMDetection`,
`TestContainerOOMDetection` and `TestK8sOOMDetection`.

| Kill | Outcome | State | Strike / backoff | `SandboxMemoryMB` | Note |
|---|---|---|---|---|---|
| 1 | `oom` | queued | 0 / 0 | 0 (default 8192) | "Run 11-oom-1: sandbox was OOM-killed under memory overcommit; retrying. The sandbox container was OOM-killed … Nothing about this task caused it, so it does not count against the task's failure streak, and the task has been queued again. …" |
| 2 | `oom` | queued | 0 / 0 | **16384** | … "It is the second OOM kill in a row, so the task's sandbox memory has been raised from 8192 MiB to 16384 MiB." |
| 3 | `oom` | queued | 0 / 0 | **32768** (ceiling: 4 × 8192) | … "It is the **second** OOM kill in a row, so … raised from 16384 MiB to 32768 MiB." ❌ This is the third kill. |

A fourth kill in a row is past `model.MaxOOMRetries` (3) and counts as an
ordinary failure. `TestAnOOMKilledAgentRunIsRequeuedWithoutAStrike`
covers that.

What `/tasks/11` and the API showed:

- `GET /api/tasks/11` and the `/api/tasks` row both had `state: queued`,
  `sandboxMemoryMb: 32768` and `activity: "retrying after OOM kill"` ✅.
- The `/tasks/11` page showed every note in the conversation, the run
  detail "sandbox was OOM-killed under memory overcommit; retrying (…)",
  the "retrying after OOM kill" activity, and a declared row reading
  "Sandbox memory (MiB) 32768" (it read 16384 after kill 2) ✅.

**Fix:** `raiseMemoryAfterOOM` now takes which kill in a row it is and
says "2nd" or "3rd", the same `ordinal` the past-the-limit detail uses.
The existing test now drives the third kill as well. It checks the queued
state, the 32768 MiB ceiling and the "3rd" note, and it fails on the old
wording.

## 3. An OOM-raised task larger than the budget

The ceiling (4 × the backend default) can be more than a small host's
whole budget. The demo shows this: a 32 GiB task against a 14 GiB budget.
`pkg/dispatch/oom_budget_test.go` (new) uses a 16 GiB host, a 2 GiB
reserve, 1.0× memory and a task at 32768 MiB:

1. With another 8 GiB agent sandbox held, `dispatch.Cycle` dispatches
   nothing. The task gets a `QuotaWaitCapability` grant wait whose reason
   is "memory: 8.0 GiB is committed by this deployment's live sandboxes of
   its 14.0 GiB sandbox budget (16.0 GiB host, 2.0 GiB reserved for the
   host itself), and this one wants 32.0 GiB, **more than the whole
   budget, so it starts only once no other sandbox is live**". The bold
   clause is new.
2. Once that sandbox is released, the next cycle admits the task alone
   (`BudgetVerdict.Sole`), and the ledger charges the full 32768 MiB.

So the queue does not deadlock, and the reason is visible on the task.
`TestBudgetRefusalSaysWhenOneSandboxIsBiggerThanTheWholeBudget` pins the
new clause in both dimensions. It appears only for a shape larger than
the whole budget.

**Starvation (not fixed, needs a decision).** A second experiment put the
same 32 GiB task first in the ready list and a 4 GiB task after it, with
8 GiB held. The cycle skipped the big task and **admitted the small one**:
live went to 12 GiB. The ready loop `continue`s past a candidate that
does not fit. `Ledger.SetAgentDemand` holds back CI jobs for the waiting
shape, but it does not hold back other agent tasks. A deployment with a
steady supply of smaller work may never drain to empty, so the OOM-raised
task waits indefinitely, visibly and without starting. Two possible
fixes, each with a cost:

- Hold later agent admissions while an oversized task is waiting. This
  costs throughput.
- Cap the OOM raise at the memory budget rather than at 4× the default.
  This costs the raise its point on small hosts.

## 4. `kubernetes-granule` Stage-1 off-cluster runtime

`git merge-base --is-ancestor b2155e294 HEAD` fails, and
`pkg/granule/forward.go` and `third_party/granule/pkg/forward/` are not
on `candidate`. What `d7d5e032b` on `origin/main` contains:

- A vendored `granule forward` exposed as `pkg/granule.RunKubectlForward`
  over `kubectl exec`.
- Discovery of `<data-dir>/kubeconfig` when `KUBECONFIG` is unset.
- `KubernetesSandboxes(cfg)` (where sandboxes run, used for budgeting)
  split from `InKubernetesPod()` (where the daemon runs).
- A supervised, reconnecting forward session per pod (Acquire, Readopt,
  Rebuild) that tunnels `sandboxnet.Fabric` ports into the guest.

On `candidate` the related pieces are the installer and e2e fixes
(`e5e88d986`) and the OOM detection in `pkg/orchestrator/oom.go`, whose
granule path reads `granule.DockerContainerExit` and the console tail.
The port is owed. I did not merge `main` into this branch.

## Changes on this branch

| Commit | What |
|---|---|
| `fix(budget): say when one sandbox is bigger than the whole budget` | `model.Budget` memory and CPU refusals add the whole-budget clause. Covered by a dispatch integration test and a budget unit test. |
| `fix(orchestrator): count the OOM kill that raised a task's memory` | The `raiseMemoryAfterOOM` note names the kill. The requeue test now covers the third kill. |

Since the UI fix is now `candidate`'s own (`abac73447`), this branch
does not touch `ui/`. While the fix was still on this branch, I took
screenshots with `make ui-screenshots PAGES=/settings/sandboxes` before
and after, at desktop and phone widths, in light and dark. The fix only
affects what happens on blur, so the rendered page was the same before
and after. A Pillow diff found differences only in two small regions: an
8 px strip at the bottom of the desktop sidebar, consistent with the
build-version stamp changing with the commit, and a 22 px icon at the top
left on phone. The refusal messages above were read live from the page's
alert after the fix.

## Findings not fixed

1. **Starvation of an oversized waiting task** (§3). This is a design
   decision for the owner.
2. **No upper bound on the fixed CPU and memory overcommit ratios.**
   `1e3` saves as 1000×, which in effect switches that dimension of the
   budget off without saying so. 0 already means "unbounded" and says so.
   An upper bound such as the auto maximum (6×) is a product call.
3. **A refused auto bound is reported as "Couldn't save Overcommit
   mode".** This happens for server-side refusals such as min above max.
   The whole `sandboxOvercommit` object is one setting, so the error is
   attributed to the select. It is accurate but imprecise.
4. **The demo's `/api/sandboxes` budget summary disagrees with its
   reported auto ratios** (§1). This is demo-only.
5. **The test plan's file names are out of date.**
   `pkg/sandbox/budget.go`, `pkg/sandbox/overcommit.go`,
   `pkg/orchestrator/sandbox_budget.go` and `pkg/ui/sandboxes.go` do not
   exist. The code is in `pkg/model/budget.go`, `pkg/model/ledger.go`,
   `pkg/model/overcommit.go`, `pkg/daemon/overcommit.go`,
   `pkg/daemon/sandbox_budget.go`, `pkg/dispatch/dispatch.go` and
   `pkg/ui/sandbox_health.go`. The plan's "doubles `SandboxMemoryMB`" is
   also not accurate for the first kill: the first kill only re-queues,
   and the second and third kills double.
