# Test run — numeric task priorities (2026-11-01 plan §1)

**Plan:** `docs/test-plans/2026-11-01.md` §1 ("Numeric task priorities — dispatch order, every filing surface, and the `priority` / `priorityDispatch` split").
**Scope:** PRs #285, #295, #296, #297 (`98eb06ac`, `42d25d37`, `36e09cbd`, `a55ffd96`).
**Task:** 2216 (filed by the nightly plan, task 2084). This is attempt 2; attempt 1 was paused on a usage limit before it pushed anything.

## Environment

- Commit under test: `main` at `f54480e7`, plus this branch's fixes.
- Binary: `go build -o /home/debian/bin/grain ./cmd/grain` (`v0.0.0-20261101094223-f54480e7825a`), with the UI built first (`cd ui && npm ci && npm run build`). Go 1.26.10, Node 20.19.0.
- Demo: `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:18080`. The person token is the demo's access code. The external agent API was switched on with `PUT /api/settings {"externalAgentApi":true}`. Three credentials were issued through `POST /api/external-agents/tokens`:
  - `walk-admin`: `tasks:admin`, `tasks:read`
  - `walk-write`: `tasks:write`, `tasks:read`
  - `walk-repo`: `tasks:write@acme/widgets`, `tasks:read@acme/widgets`
- Daemon (step 4): a real `grain daemon`, started by `grain init workstation -sandbox-backend host` then `daemon.sh -sandbox-port-base=0 -poll-interval 1s`. It ran over a githubsim host and a bare upstream, with `maxWorkers: 1` set in Settings. This is the new `tests/e2e/priority_dispatch_order_test.go`.
- Browser: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), at 1440×900 and 390×844, light and dark (`emulateMedia({colorScheme})`).
- **Stubbed:** the model backend only. The daemon run uses a `custom` agent backend, a shell script that records the branch its sandbox checked out and ends its turn. Sandboxes (host), git, the store, dispatch and the API are all real.

## Results

| # | Item | Result |
|---|---|---|
| 1 | Real dispatch order 9 → 5 → 5 → 1, FIFO within a priority | **Pass** |
| 2a | New task field, list badge, pane chip, `Declared`, Edit to 7 (1440/390, light/dark) | **Pass** |
| 2b | Host CLI `grain create -priority` / `grain update -priority` | **Pass**, with a note on `0` (below) |
| 2c | Sandbox CLI `grain task propose --priority` | **Pass** in unit tests on this tree. The deployed sandbox CLI predates #285 (below) |
| 2d | Template `priority: 7` files a task carrying 7 | **Pass** |
| 2e | Do follow-up proposals inherit priority? | **They do not**: they default to 5 (below) |
| 3 | `priority` vs `priorityDispatch` wire compatibility | **Fail → fixed**: bug 2. **Security fail → fixed**: bug 1 |
| 4 | Trailing-JSON, array and empty `PATCH` bodies under `api:tasks:admin` | **Pass** |
| 5 | Upgrade: existing rows read back as 5 | **Pass** by code reading only. No live upgrade was run |

### 1. Dispatch order on a real daemon (plan step 4)

Four tasks were filed in the order 5 ("first five"), 1, 9, 5 ("second five") and approved, all under a pipeline pause (`POST /api/pipeline-pause`) so that they queued together. The pause was then lifted. The agent saw the branches in this order:

```
=== RUN   TestRealDaemonDispatchesByPriorityThenFIFO
    priority_dispatch_order_test.go:215: dispatch order: [nine first five second five one]
--- PASS: TestRealDaemonDispatchesByPriorityThenFIFO (16.25s)
```

Filing order alone would have given `first five, one, nine, second five`. The observed order is priority order, with the two 5s kept in filing order. The test is committed, so CI now covers plan step 4. I did not cover how this interacts with priority-dispatch (`Budget.AdmitsPriority`), retry backoff or run-now. `pkg/dispatch/prioritydispatch_test.go` covers those at unit level.

### 2a. Browser (plan step 2)

The new `ui/e2e/task-priority-walkthrough.spec.js` covers all four combinations of 1440/390 px and light/dark. Each run:
- files P1, P5 and P9 from `/new`. The POST carries `priority` for 1 and 9 and leaves it out for 5; the created task reads back each value.
- checks the list at `/`. P9 and P1 rows carry `task-priority-badge` reading `P9` / `P1`, and the 5 row has none. The badge's box lies inside the row's box and, at 390 px, inside the viewport.
- opens the P9 task. The pane's `detail-priority-badge` reads `priority: 9`, and `Declared` has `Task priority 9 (default 5)`.
- edits the task to 7 with Edit → Task priority → Save. The PATCH carries `"priority":7`, then the chip reads `priority: 7`, `Declared` reads `7 (default 5)`, and the list badge reads `P7`.
- checks the default. The P5 pane says `priority: 5`, and `Declared` has no Task priority row.

```
Running 4 tests using 1 worker
  4 passed (42.4s)
```

Row crops were shot at both widths and themes (`row-{1440,390}-{light,dark}.png`, about 8 KB each). The geometry above is asserted with bounding boxes. Neither file reader here renders PNGs, so I did not look at the crops myself.

### 2b. Host CLI

The host CLI's filing verb is `grain create`, not `grain task propose`: `grain task …` is sandbox-CLI only, and on the host it answers `unknown command "task"`. The plan's step 3 wording assumes one CLI.

| Command | Result |
|---|---|
| `grain create -title cli-8 -body b -repo acme/widgets -priority 8` | filed task 24 at priority 8 |
| `… -priority -1` | `grain: priority must be a positive integer`, exit 1 |
| `… -priority x` | `invalid value "x" for flag -priority: parse error`, exit 1 |
| `… -priority 2.5` | `invalid value "2.5" for flag -priority: parse error`, exit 1 |
| `… -priority 0` | **filed task 25 at priority 5**, exit 0 |
| `grain update -priority 9 12`, then `-priority 0 12` | 9, then back to 5 |
| `grain update -priority -3 12` | `grain: priority must be a positive integer` |

**Note: `0` is handled differently on each surface.** The REST API and host CLI take `0` to mean "the default" and file 5 without a word, while their refusal text says "positive integer". The Edit form refuses `0` ("a whole number, 1 or more", `edit-task-priority.spec.js`). The sandbox CLI says `--priority … must be a non-negative integer`. Nothing breaks, but the four surfaces disagree. I filed this as a follow-up; I did not change it here.

### 2c. Sandbox CLI

This tree's `pkg/sandboxcli` tests pass (`go test ./pkg/sandboxcli -run 'Propose|Action'`). They cover `--priority 8` being sent, and `--priority -1` being refused with `must be a non-negative integer`.

**This run's own sandbox CLI (`/usr/local/bin/grain`, image `ec10ed29`) predates #285.** It accepts and ignores `--priority`. I ran `grain task propose --title t --body b --priority -1` (then `x` and `2.5`) expecting a client-side refusal. Instead the old CLI filed a real proposal on the deployment, **[task #2277](/tasks/2277)** ("t", held for approval); the three calls deduplicated to that one task. **It should be rejected.** It carries no work.

### 2d. Template priority

- `POST /api/templates {"name":"walk-p7",…,"priority":7}` returned `template-3`, priority 7.
- `POST /api/tasks {"templateId":"template-3"}` filed task 26 at priority 7.
- With `"priority":2` alongside, it filed task 27 at 2: an explicit priority wins over the template's.
- A template with `"priority":-2` was refused: `priority must be a positive integer`.

### 2e. Do follow-ups inherit priority?

No. `pkg/orchestrator/finish.go` (`proposedTasks`) starts every proposal at `model.DefaultTaskPriority`. It takes the proposal's own `priority` argument when that is above 0, or else the template's when the proposal names one, and never the parent task's. A follow-up from a priority-9 task therefore files at 5 unless it asks for more. `98eb06ac`'s message says nothing either way, and the plan's "sync and finish carry it onto follow-ups" describes carrying an explicit `priority`, not inheriting one. I recorded this as the behaviour and changed nothing. The follow-up task asks the owner whether inheriting was meant.

### 3. Wire compatibility (plan "not proved" 3)

`PATCH /api/tasks/11` (or `12` for the person token):

| Token | Body | Status | Result |
|---|---|---|---|
| person | `{"priority":true}` | 200 | priority-dispatch attached, priority stays 5 ✔ |
| person | `{"priority":false}` | 200 | detached ✔ |
| person | `{"priority":7}` | 200 | priority 7 ✔ |
| person | `{"priorityDispatch":true}` | 200 | attached ✔ |
| person | `{"priority_dispatch":false}` / `true` | 200 | **nothing changed** ✘ (bug 2) |
| admin agent | `{"priority":3}` | 200 | priority 3 ✔ |
| admin agent | `{"Priority":4}` | 200 | priority 4 ✔ |
| admin agent | `{"priority":-1}` | 400 | `priority must be a positive integer` ✔ |
| admin agent | `{"priority":2.5}` | 400 | `json: cannot unmarshal number 2.5 into Go value of type int` ✔ |
| admin agent | `{"priority":0}` | 200 | reset to 5 (see 2b) |
| admin agent | `{"priorityDispatch":true}` | 403 | `may only edit priority … (got "priorityDispatch")` ✔ |
| admin agent | `{"priority_dispatch":true}` | 403 | same ✔ |
| admin agent | `{"priority":true}` | 403 | `may only edit numeric priority` ✔ |
| write agent | `{"priorityDispatch":true}` / `{"priority":true}` | 403 | `holds no api grant for scope capabilities:admin` ✔ |
| write agent | `{"priorityDiſpatch":true}` (U+017F) | **200** | **priority-dispatch attached** ✘ (bug 1) |
| write agent | `{"priority":6}` | 200 | priority 6 ✔ |

### 4. Smuggling bodies under `api:tasks:admin` (plan "not proved" 4)

| Body | Status | Message |
|---|---|---|
| `{"priority":3}{"approved":true}` | 403 | `requires a single JSON object editing priority` |
| `{"priority":3} {"title":"x"}` | 403 | same |
| `[{"priority":3}]` | 403 | same |
| `null` | 403 | same |
| *(empty)* | 403 | `requires a JSON object editing priority` |
| `{"priority":3,"title":"x"}` | 403 | `may only edit priority … (got "title")` |
| `{}` | 200 | no change |

The task row was unchanged after every refused body. Under the person token, the trailing-JSON body gets 400 `unexpected trailing JSON data after request body`, and the empty body gets 400 `EOF`. The array body gets 400 too, but its message spells out the Go type of `UpdateTaskRequest`'s internal decode struct. That is cosmetic, and I left it.

### 5. Upgrade

Migration 73 adds `task.priority INTEGER NOT NULL DEFAULT 5` and `template.priority … DEFAULT 0` (`pkg/model/migrations.go`). Existing tasks therefore read back as 5, and the demo's seeded tasks all read `"priority":5`. The list and `Declared` hide 5 on purpose, as the default. `pkg/cli/list_test.go`'s `TestPriorityFilterAndSort` covers `priority:gt:5` treating unset as 5. I did not migrate a real pre-73 store.

## Bugs

### Bug 1 (security, guard bypass): a body key spelt with ſ or K slips past the agent gates

encoding/json matches an object key to a struct field case-insensitively, under Unicode simple folding. In that folding U+017F `ſ` is an `s`, and U+212A (the Kelvin sign) is a `k`. The agent gates read body keys with `strings.ToLower`, which leaves `ſ` as it is. A key spelt with `ſ` therefore reaches the field it names and misses the check that refuses it. The affected gates are `patchTouchesTaskGrants`, `addedGrantsInBody`, `agentFilingEscalation` and `externalAgentFilingEscalation` in `pkg/ui/agent_api_gate.go`.

Reproduction against the demo:

```
$ curl -s -w '%{http_code}\n' -H "Authorization: Bearer $WRITE_AGENT" -H 'Content-Type: application/json' \
    -X PATCH $S/api/tasks/11 --data-binary $'{"priorityDi\xc5\xbfpatch":true}'
200 {"capabilities":["gcp-key","gemini-key","priority-dispatch","self-debug"],…}
# while {"priorityDispatch":true} from the same credential gets 403 "holds no api grant for scope capabilities:admin"

$ curl … -H "Authorization: Bearer $REPO_AGENT" -X POST $S/api/tasks \
    --data-binary $'{"title":"walk smuggle","description":"d","repo":"acme/widgets","autoApproveProposal\xc5\xbf":true}'
201 {"id":"20","autoApproveProposals":true}   # "autoApproveProposals":true gets 403 "an external agent cannot turn on autoApproveProposals"
$ … "reviewer\xc5\xbf":["mallory"]   → 201, reviewers ["mallory"]  # "reviewers" gets 403
$ … "write\xc5\xbf":[{"repo":"acme/other"}]   → past the gate; only the target-repo list stopped it (400)
```

The gates reach every key whose spelling contains an `s`: `writes`, `reads`, `autoApproveProposals`, `tasksApproved`, `networkAccess`, `proposedBy`, `proposalKey`, `reviewers`, `grants`, `priorityDispatch` / `priority_dispatch`. Only `tasksAdminTaskPatchBodyAllowed` was safe, because it uses `strings.EqualFold`. The Kelvin sign was already caught, because `ToLower(K)` is `k`.

**Fixed** in `b858ed7f`. A new `gateKey` folds each rune to the smallest in its folding orbit, as encoding/json's `foldRune` does, then lowercases it. All four gates compare by it. The regression test is `TestAgentGatesFoldBodyKeysAsEncodingJSONDoes` (`pkg/ui/task_priority_test.go`). Without the fix it fails 7 of its 8 cases; the Kelvin `keepRunning` case passes either way.

### Bug 2: `priority_dispatch` was ignored on `PATCH /api/tasks/{id}`

```
$ curl … -H "Authorization: Bearer $PERSON" -X PATCH $S/api/tasks/12 -d '{"priority_dispatch":true}'
200 {"capabilities":["gcp-key","gemini-key","self-debug"]}     # unchanged
```

The gate and the plan both treat `priority_dispatch` as a spelling of the toggle, but `UpdateTaskRequest.UnmarshalJSON` read only `priorityDispatch`. **Fixed** in `d3d98020`, which reads it alongside `keep_running` and `auto_approve_proposals`. The regression test is `TestUpdateTaskRequestReadsEveryPriorityDispatchSpelling`.

## Tests added

- `pkg/ui/task_priority_test.go`: `TestAgentGatesFoldBodyKeysAsEncodingJSONDoes`, `TestUpdateTaskRequestReadsEveryPriorityDispatchSpelling`.
- `tests/e2e/priority_dispatch_order_test.go`: `TestRealDaemonDispatchesByPriorityThenFIFO` (plan step 4).
- `ui/e2e/task-priority-walkthrough.spec.js` (plan step 2).

## Filed

- `0` as a priority is read differently on each surface (2b). Filed as a follow-up task.
- Whether follow-ups should inherit their parent's priority (2e). Included in the same follow-up as a question for the owner.
- [Task #2277](/tasks/2277) was filed by mistake by the old sandbox CLI (2c). It needs rejecting.
