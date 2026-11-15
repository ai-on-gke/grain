# Walkthrough: proposal and grant admission — 2026-11-13

Plan: [`docs/test-plans/2026-11-13.md`](../test-plans/2026-11-13.md) §5, "Proposals and grants at admission". It covers PRs #1257, #1435, #1358, #1361, #1299, #1327, #1224 and #1341.

- **Commit walked:** `51844bb2c` (`origin/main` at 2026-11-13 04:03 UTC). The fixes below are on this branch.
- **Helpers:** [`2026-11-13-proposal-admission/`](2026-11-13-proposal-admission/).
- **Raw output:** [`out/before/`](2026-11-13-proposal-admission/out/before/) and [`out/after/`](2026-11-13-proposal-admission/out/after/).

## Results

| # | Item | SQLite | PostgreSQL | Outcome |
|---|---|---|---|---|
| 1 | Relay path, one run: CI-only grants, an over-maximum shape and a valid task | pass after fix | pass after fix | **F1 fixed.** Before the fix the agent and the proposer's conversation were never told. |
| 2 | Pre-change proposal rows refused at approval (`/next/`, classic, `POST /api/tasks/{id}/approve`, run-now) | pass | pass | No change needed. |
| 3 | Template `sandbox:` fill held to the maximum | pass | pass | **F2 fixed:** the note now credits the template's size to the template, not to the run. |
| 4 | `Add grant…` presets (`grain/*`, `repo#credential`) stored canonically and held by the next run | pass (`/next/`, classic) | pass (`/next/`, classic) | No change needed. |

> **Design note (D1):** the plan says (a) and (b) are "refused" and "only (c) is filed". PRs #1257 and #1435 chose something else:
> - A CI read of another repository is filed as that repository's `reads` entry.
> - Any other CI capability is dropped.
> - A sandbox shape above the maximum is lowered to the maximum.
>
> Each proposal says so in its body and is held for a person. No proposal reaches `proposed` holding a CI capability or an above-maximum shape. That keeps the work the run described, and this walk treats it as intended. What was missing was that the run and the person watching the proposer were told (F1).

## Environment

- **Binary:** `make frontend`, then `go build -o /home/debian/bin/grain ./cmd/grain` from this branch's `HEAD`. Item 1's "before" used the same build without the fix.
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes -max-workers 2 -as alice@example.com` on `127.0.0.1:4280`, with `-repo acme/widgets`.
- **Stores:** SQLite (`data/store/grain.db`), and PostgreSQL 15 (Debian `postgresql`, `-postgres-url postgres://grain:grain@127.0.0.1:5432/grain_walk`). The daemon log shows "acquired PostgreSQL single-writer advisory lock". Each PostgreSQL walk starts from a freshly created database.
- **Shape limits:** `sandboxShapeLimits` set with `PUT /api/settings` to `{"max":{"cpus":2,"memoryMb":4096,"diskGb":100}}`.
- **Browser:** Chromium (Playwright from `ui/node_modules`, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), 1440×900, light, signed in with the daemon's access code.

### Stubbed boundaries

These are the only ones; the sandbox has none of the real ones.

- **GitHub:** `ghsim.go`, copied from `2026-11-11-durable-tasks`. It is `pkg/github/githubsim` over a local bare `acme/widgets`.
- **The agent CLI:** `fake-agy.py`, a scripted Antigravity CLI. It drives the **real** `grain mcpserver` the daemon hands each run (`propose_task`, `comment_on_issue`, `nothing_needed`) and records every tool result and prompt under `runs/`.
- **Sandboxes:** host directories (`-host-sandboxes`). They have no shape of their own, so any task that sets a sandbox shape fails at sandbox setup. Shaped proposals were approved but not dispatched (O2). Item 4's tasks set no shape and ran.
- **The model credential:** a placeholder key file. Nothing calls a model.

## Steps and findings

### Item 1: the relay path end to end

Script: [`item1_relay.sh`](2026-11-13-proposal-admission/item1_relay.sh), run with `STORE=sqlite|postgres`.

One approved parent run made these calls:
- **A1:** `propose_task` with `capabilities: ["repo-read:acme/gadgets"]`.
- **A2:** `propose_task` with `capabilities: ["repo-write:acme/widgets"]`.
- **B:** `propose_task` with `sandbox_cpus 16, sandbox_memory_mb 65536, sandbox_disk_gb 500`.
- **C:** a valid `propose_task` with `sandbox_disk_gb 50`.
- `comment_on_issue` to close.

**What was filed (both stores, before and after the fix):**

| Task | Filed as | Note in the body |
|---|---|---|
| A1 (3) | `reads: ["acme/gadgets"]`, no repo grant | "filed with `reads: ["acme/gadgets"]` instead". Held for a person, since the proposer cannot read `acme/gadgets`. |
| A2 (4) | no `repo#write` grant | "filed without that grant: … is a CI capability, not a task grant … names it in its writes instead" |
| B (5) | 2 vCPU / 4096 MiB / 100 GiB | "sandbox_cpus 16 (lowered to 2), sandbox_memory_mb 65536 (lowered to 4096), sandbox_disk_gb 500 (lowered to 100)" |
| C (6) | 50 GiB, as asked | held for a person only because 50 GiB is above the proposer's own 30 GiB |

None of the four holds a grant that `model.TaskGrantCIOnlyRefusal` refuses, and none has a shape above the maximum.

**F1: the refusals never reached the agent or the proposer's conversation (fixed).**
- **Reproduction:** before the fix, every `propose_task` result was only `Recorded (n proposed task(s) so far this run). Each is filed as a real task …` ([`out/before/item1-sqlite-tool-results.jsonl`](2026-11-13-proposal-admission/out/before/item1-sqlite-tool-results.jsonl)).
- The parent's conversation held only the run's own closing note ([`out/before/item1-sqlite-parent-conversation.txt`](2026-11-13-proposal-admission/out/before/item1-sqlite-parent-conversation.txt)). The changes could be found only by opening each proposal.
- The plan's "visible to the agent (next prompt or tool result)" therefore failed, and so did "visible to the person (task conversation)" for the person watching the proposer.
- A background run's `propose_task` records the call. `relayProposedTasks` decides it only after the run has ended. The live path (`-interactive`) does get `CreateTask`'s 400.

**Fix, in two parts:**
- **`pkg/mcp/mock_tools.go` (`ciOnlyProposalGrantsOutcome`):** the recorded result now says, at the call, what filing will do with each CI capability. It uses the same `model.CanonicalLegacyGrants` → `model.TaskGrantCIOnlyRefusal` / `model.RepoReadTarget` path as the relay, and passes over `grain-ci` the same way. For example: "`repo-read:acme/gadgets` is a CI capability no task run is handed, so this proposal will be filed with reads: ["acme/gadgets"] in its place." Nothing recorded changes. The in-sandbox MCP server cannot read the deployment's `sandboxShapeLimits`, so it cannot say anything about the shape at the call (follow-up proposed).
- **`pkg/orchestrator/finish.go` (`noteProposalAdmissionChanges`):** after a batch, the relay posts one `grain` comment on the proposer's own task naming each proposal admission changed. That is where the person watching it reads it, and where the run reads it on its next turn:
  ```
  grain filed 3 proposals from this run without something they asked for:

  - task 3 "A1 CI-only cross-repo read": `repo-read:acme/gadgets` is a CI capability, so it was filed with `reads: ["acme/gadgets"]` instead.
  - task 4 "A2 CI-only repository write": `repo-write:acme/widgets` is a CI capability, not a task grant, so it was filed without it.
  - task 5 "B oversized sandbox": its sandbox is above this deployment's maximum, so it was filed at 2 vCPU, 4096 MiB, 100 GiB disk.
  ```
  A batch where nothing changed posts nothing. The valid proposal C is never named.

**After the fix:**
- Both stores show the tool-result notes for A1 and A2, none for B and C, and the parent comment above.
- Output: [`out/after/items1-3-sqlite.out`](2026-11-13-proposal-admission/out/after/items1-3-sqlite.out) and [`out/after/items1-3-postgres.out`](2026-11-13-proposal-admission/out/after/items1-3-postgres.out).

**Regression tests:**
- `pkg/orchestrator`: `TestRelayedProposalsAdmissionChangedAreNamedOnTheProposer`. It fails on `HEAD` with "parent has 0 admission notes, want 1".
- `pkg/mcp`: `TestRecordedProposalIsToldAboutItsCIOnlyCapabilities`.

### Item 2: pre-change rows refused at approval

Script: [`item2_approval.sh`](2026-11-13-proposal-admission/item2_approval.sh). The UI clicks are `item4_ui.mjs approve`, run by [`item4_grants.sh`](2026-11-13-proposal-admission/item4_grants.sh).

Rows seeded straight into the store (`task_grant` and `task.sandbox_*`), standing for proposals filed before #1257 and #1435:
- **3:** `https://grain.dev/capabilities/repo#write={"repo":"acme/widgets"}`
- **6:** legacy `repo-read:acme/gadgets`
- **4:** `sandbox_disk_gb 500`
- **5:** `sandbox_cpus 16, sandbox_memory_mb 65536`

| Route | 3 (repo#write) | 4 (disk 500) | 5 (cpus/mem) | 6 (repo-read) |
|---|---|---|---|---|
| `POST /api/tasks/{id}/approve` | 400, names the grant | 400 `sandboxDiskGb 500 (ceiling 100)` | 400 `sandboxCpus 16 (ceiling 2), sandboxMemoryMb 65536 (ceiling 4096)` | 400, names the grant and `reads: ["acme/gadgets"]` |
| `/next/task/{id}` Approve | 400, error banner names the grant | 400, banner names the dimension | — | 400, banner names the grant |
| classic `/tasks/{id}` Approve | 400, alert names the grant | 400, alert names the dimension | — | 400, alert names the grant |
| `POST /api/tasks/{id}/run-now` | — | — | — | 400, same refusal |

- Every row stayed `proposed` on both stores.
- Both UIs call `POST /api/tasks/{id}/approve`, from the task pane, the Inbox and classic's batch bar.
- Classic also showed its existing "host-directory sandbox has no shape" notice for the shaped rows. That notice is correct here (O2).

### Item 3: the template's `sandbox:` fill

Script: [`item3_template.sh`](2026-11-13-proposal-admission/item3_template.sh).

- **Templates:** `POST /api/templates` with `sandbox: {diskGb: 200}` ("big-disk") and `{diskGb: 60}` ("small-disk"). The deployment maximum is 100.
  - Writing a template holds it to grain's default ceiling (1024 GiB), not to the deployment's maximum. That is by design (`model.refuseCompositionSandboxAboveMax`), so the 200 GiB template is accepted.
- **Proposals:** one run proposed `template_id: big-disk` and `template_id: small-disk`, with no sizes of their own.
- **Big:** filed at `sandboxDiskGb 100` with a lowering note. Approval succeeds at 100.
- **Small:** filed at the template's 60 with no note.
- Results are the same on both stores.

**F2: the lowering note credited the template's size to the run (fixed).** The note said "the run that proposed this asked for a sandbox …", but the 200 GiB came from the template.
- `clampProposalSandboxShape` now takes whether the template filled a dimension. When it did, the note says "this proposal, with its template's `sandbox:` filled in, asks for a sandbox …".
- Output: [`out/after/items1-3-sqlite-final-build.out`](2026-11-13-proposal-admission/out/after/items1-3-sqlite-final-build.out).
- Regression test: `TestRelayedProposalTemplateSandboxIsHeldToTheMaximum`, which also checks the small template is filed at 60 with no note.

### Item 4: `Add grant…` presets

Script: [`item4_grants.sh`](2026-11-13-proposal-admission/item4_grants.sh) runs [`item4_ui.mjs`](2026-11-13-proposal-admission/item4_ui.mjs) on both stores.

**`/next/task/{id}`** (`g-grants-picker`):
- The API mode lists every built-in `grain/*` scope set: `grain/contributor`, `grain/delegated`, `grain/operator`, `grain/viewer` and the rest.
- Adding `api:grain/operator` stores `https://grain.dev/capabilities/api#grain/operator`.
- Method mode `https://grain.dev/capabilities/repo#credential` with `{"credential":"bot"}` stores `https://grain.dev/capabilities/repo#credential={"credential":"bot"}`.
- Raw `repo-read:acme/gadgets` is refused inline with "repo-read:acme/gadgets is a CI capability, not a task grant: … names it in its reads instead". Nothing is stored.

**Classic `/tasks/{id}`** (`GrantsPicker`):
- Searching `grain/operator` finds the `api:grain/operator` row, and clicking it stores the same canonical reference.
- "repo server › credential" opens a draft. Choosing `bot` in its credential select grants it, stored as above.
- The ungrantable-repo refusal was not walked on classic. It is covered by `ui/e2e/grant-matrix-api.spec.js` (#1361), and new coverage goes to `/next/`.

**The next run holds both.** Both tasks were approved and run. Each run's prompt carries:
- the `api#self-debug` and `api#test-quarantine` sections, which only the operator preset gives. A run without the preset (task 2) has neither;
- the `<capability grant="https://grain.dev/capabilities/repo#credential">` block saying git uses the "bot" GitHub token.

Results are the same on both stores ([`out/after/item2ui-item4-sqlite.out`](2026-11-13-proposal-admission/out/after/item2ui-item4-sqlite.out) and [`…-postgres.out`](2026-11-13-proposal-admission/out/after/item2ui-item4-postgres.out)).

### Observations (no change)

- **O1, harness:** `POST /api/tasks` takes `"approved": true`, not `"approve"`. The first item 1 attempt sat in `proposed` until approved by hand. The scripts now send `approved`.
- **O2, stubbed boundary:** host-directory sandboxes refuse every shaped task ("asks for 0 vCPU/0 MiB/100 GiB but a host-directory sandbox has no shape of its own"). So the lowered B and T-big were approved, but not dispatched, in this sandbox.
- **O3, classic picker:** rows in "Grants to add" are `menuitemcheckbox` toggles, and the menu stays open after a click. Clicking a held preset again removes it. That is by design (`aria-checked`), but a script has to press Escape between adds.

## Follow-up proposed

- **Tell a background `propose_task` call about the shape limit at the call.** The in-sandbox MCP server is not handed the deployment's `sandboxShapeLimits`, so the agent only hears about a lowered shape on the parent's conversation after the run (F1's second half). Handing the run its maximum would let the call say it at once. Filed with `depends_on: ["5357"]`.
