# Test run — 2026-11-02 — §2 Director writable grain/config checkout and `propose_config_change`

Plan: [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §2 (PR #334). Task 2444.

## Environment

- Checkout `8726f7f7` (`origin/main`, no sibling §2 fixes on `main` yet), `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` so the binary embeds the real UI.
- A real **`grain daemon`**: a fresh data dir with its internal grain/config (`<data-dir>/state-repo`), the daemon's own git proxy, SQLite, and `-sandbox-port-base=0`:

  ```
  grain daemon -data-dir /var/tmp/wt/data -host-sandboxes -sandbox-dir /var/tmp/wt/sbx \
    -sandbox-port-base=0 -ui-addr 127.0.0.1:8411 -reconcile 2s
  ```

- **Run host: host-directory sandboxes (`-host-sandboxes`), not Docker or Granule.** Docker was available. Host sandboxes were used to fit the time box: every push still goes through the daemon's real git proxy and `ModelAuthorizer` with the run's own minted sandbox token, which is what §2 is about. The isolation backend is not part of the code under test, but a Docker run host was **not** exercised here.
- **Agent: a stub custom backend, not a real model.** The sandbox has no agent credentials. Instead of the plan's in-process stub framework, it uses a protocol-2 custom command ([`stub-backend.py`](2026-11-02-director-config-change/stub-backend.py), [docs/custom-agent-backends.md](../custom-agent-backends.md)), registered with `PUT /api/deployment/agent-backends/stub {"framework":"custom","command":…}`. Each turn it starts the run's own `grain mcpserver` over stdio and makes the tool calls scripted in `/var/tmp/stub/script.json`: `run_command` for the edit and the pushes, and `propose_config_change`. It appends the turn's prompt, tool list and results to `log.jsonl`, so "the session was told" is read from the next turn's real prompt.
- Sessions: the **Director** (task 1), given grain/config in Writes with `PUT /api/console-sessions/director {"writes":[{"repo":"grain/config"}]}`, and a **Reader** console session (task 2), which has grain/config in Reads only (the default). The shipped Director has grain/config in **Reads only** (`model.DefaultConsoleSessions`), so a deployment has to grant Writes before any of §2 applies.
- Browser: Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) signed in with the access code, driving the real daemon's `/tasks/1` with [`walk-config-change.mjs`](2026-11-02-director-config-change/walk-config-change.mjs) (copy it into `ui/e2e/` to run it). Screenshots: `ui/e2e/screenshot.mjs --url … --code … --pages /tasks/1`, desktop (1440) and phone (390), light and dark.
- `sqlite3` installed with apt. The in-sandbox `grain` CLI needed no `GRAIN_SANDBOX_PROXY`.

## Results

| # | Step | Outcome |
|---|---|---|
| 1a | Director turn: `/home/debian/grain-state` (here `<sandbox>/grain-state`) exists | ✅ `origin http://127.0.0.1:37393/grain/config.git` for fetch **and** push, `user.name=grain director`, `user.email=grain@localhost`; the prompt has the "may propose changes" section; `propose_config_change` is in `tools/list` |
| 1b | `git push origin HEAD:refs/heads/grain/config/1/x` | ✅ `* [new branch] HEAD -> grain/config/1/x`; deleting it is allowed too |
| 1c | push to `main`, `grain/config/2/x`, `feature`, `grain/config/1` (prefix itself) | ✅ all `! [remote rejected]`. ⚠️ The message pointed at the wrong remedy (**F1**, fixed) |
| 1d | Reader (Reads only): push from a hand clone to `grain/config/2/x` and `main` | ✅ HTTP 403 `grain/config is read-only for this sandbox: it may fetch it but not push to it`; `propose_config_change` is not offered (`unknown tool`) |
| 1e | Reader has a `grain-state` checkout | ➖ none: the checkout is made only for the Director (`prepareCheckoutWithReuse` checks `DirectorSessionID`), see **F3** |
| 2a | Director edits `settings/schedules.yml` (title of `system:infra-review`), calls `propose_config_change` | ✅ `Proposed grain/config/1/rename-the-infrastructure-review-schedule (1c3cd84, 1 file(s)). It passed config validation.` Task `awaiting_reply`, `pending_question_comment_id=13`, `pending_config_change` stored (below) |
| 2b | Approved (as the access-code holder, over the API) | ✅ 200, merged `4e639b0`, branch deleted, the next turn's prompt has `- a human (debian): Approved and applied config change (1c3cd84e064b)…`. ❌ But the change was undone a tick later (**F2**) |
| 2c | Director sets `tables/grain_config.json` `environment_name`, proposes; a person clicks **Approve & Apply** in the browser | ✅ 200; `GET /api/settings` `environmentName`: `ops-moved-main` before, `walkthrough-env-c` right after the click and still after the next turn; merge `8b11c8a`; `git ls-remote` shows no `grain/config/*` refs; the next turn is told `Approved and applied config change (3519aee1d8dc): Name this environment walkthrough-env-c.` |
| 3a | Invalid tree (`templates: [ bad`) | ⚠️ not refused at propose time. It is **recorded and parked** with the validator's message: the tool says `It FAILED config validation… yaml: line 1: did not find expected ',' or ']'`, and the card shows `Validation failed: …` with **Approve disabled**. This is what the tool's own description and `configChangeRequestBody` say it does; the plan's "refused, not parked" does not match the implementation (**F4**) |
| 3b | Main moves in a conflicting way (operator commit on `main` editing the same line), then approve | ✅ 422 `the config change does not pass validation: the change conflicts with the current main: rebase it onto origin/main and propose it again`; the change stays pending |
| 3c | Session pushes again after proposing, then browser **Approve & Apply** | ✅ 409 `branch tip changed since diff was rendered`; still parked, `headSha` unchanged in the DB |
| 3d | Browser **Reject** with a reason | ✅ 200; `git ls-remote` lists only `HEAD` and `main`; `pending_config_change` NULL; the next turn is told `Rejected config change (f9838592f9d2): walkthrough: the YAML does not parse` |
| 4a | External agent token (`tasks:read,tasks:write`) approve / reject / propose | ✅ 403. With `externalAgentApi:false`: `external agent API is disabled`. With it on: `POST /api/tasks/{id}/config-change/approve is not part of the agent API`, and the same for reject and propose. Nothing changed |
| 4b | Another session's run with `api:tasks:write`, via `grain api POST /api/tasks/1/config-change/approve` from its sandbox | ✅ refused: `… is not part of the agent API`; the change stays pending |
| 4c | The session itself | ✅ it has no approve tool, and its sandbox has no daemon credential (HTTP 401 without the access code). Only the session's own `grain mcpserver` holds one, and it exposes only `propose_config_change` for its own task |
| 5a | Daemon restart while parked on the invalid change | ✅ after stop + start on the same data dir: `pending_question_comment_id=18`, branch `grain/config/1/broken` still pending, the task still `awaiting_reply`, the card rendered; the git proxy came back on a new port and the next turn's checkout had the new `origin` |
| 5b | A person's chat comment while a change is pending | ➖ it **does** un-park the session, the way a reply does to `request_secret` (the decision comment is what un-parks it normally); the turn runs with the change still pending, and here it re-proposed in that turn. Whether a turn that ends without re-proposing parks again was not checked |
| 5c | Notification / standing-task wakes | ➖ not walked live (no notification source or standing schedule targeted the Director in the time box). Covered by `model/notification_wake.go` and `standing_task.go` checks and their tests |
| 6 | Card layout at 1440 / 390, light / dark (invalid and valid change) | ✅ in all 8 views: Approve and Reject boxes are inside `.pending-config-change`; the card is inside the viewport; `scrollWidth` equals the viewport width (1440 / 390), so nothing scrolls sideways; no page errors. Desktop card 1152×532 at x=260, phone 361 px wide at x=14. Approve is enabled only for the valid change. **Long diff not shot**: the diffs here were 1–10 lines |

### `task_observation.pending_config_change`

After 2a (`sqlite3 data/store/grain.db "select task_id, pending_question_comment_id, substr(pending_config_change,1,600) from task_observation where task_id='1'"`):

```
1|13|{"branch":"grain/config/1/rename-the-infrastructure-review-schedule","base":"main","baseSha":"d8ab10c9c7d56cd38f9b57ae191cecf896a9ac35","headSha":"1c3cd84e064b2ed1fd2020e02a3bb125bd616772","summary":"Rename the infrastructure review schedule","description":"Walkthrough §2: a scripted Director edit.","files":["settings/schedules.yml"],"diff":"diff --git a/settings/schedules.yml …
```

After 3a: `18|{"ok":false,"error":"checking the candidate configuration: staterepo: parsing settings/templates.yml: yaml: line 1: did not find expected ',' or ']'"}` (`json_extract(…,'$.validation')`). It is unchanged after the restart. After 2b, 3d and 2c it is `NULL`, and at the end both tasks read `1|NULL|36`, `2|NULL|28`.

### Transcripts

Director's checkout and pushes (1a–1c, before the fix):

```
$ pwd; git -C grain-state remote -v; git -C grain-state config user.email
/var/tmp/wt/sbx/runs/1-1
origin  http://127.0.0.1:37393/grain/config.git (fetch)
origin  http://127.0.0.1:37393/grain/config.git (push)
grain@localhost
$ git push origin HEAD:refs/heads/grain/config/1/x
 * [new branch]      HEAD -> grain/config/1/x
$ git push origin HEAD:refs/heads/main
remote: refusing to update refs/heads/main in grain/config: a sandbox may push only its own task's branch, refs/heads/grain/509be6d8/task-1-director (push with `git push origin grain/509be6d8/task-1-director`). A task filed from a template declared in this repository's .grain/templates pushes other refs of it only where that template lists them under `push:`, on the default branch
 ! [remote rejected] HEAD -> main
$ git push origin HEAD:refs/heads/grain/config/2/x     # same refusal
$ git push origin HEAD:refs/heads/feature             # same refusal
$ git push origin HEAD:refs/heads/grain/config/1      # same refusal (prefix alone)
$ git push origin --delete grain/config/1/x
 - [deleted]         grain/config/1/x
```

After the F1 fix (rebuilt daemon, same session):

```
remote: refusing to update refs/heads/main in grain/config: a sandbox may push only its own task's branch, refs/heads/grain/509be6d8/task-1-director (push with `git push origin grain/509be6d8/task-1-director`), and its own config proposal branches, refs/heads/grain/config/1/<name>. A change reaches grain/config's main only through propose_config_change and a person's approval
```

Reader (Reads only):

```
$ git push origin HEAD:refs/heads/grain/config/2/x
remote: grain/config is read-only for this sandbox: it may fetch it but not push to it
fatal: unable to access 'http://127.0.0.1:37393/grain/config.git/': The requested URL returned error: 403
```

API decisions:

```
POST /api/tasks/1/config-change/approve   (browser, after a second push)    409 {"error":"branch tip changed since diff was rendered"}
POST /api/tasks/1/config-change/approve   (person, main conflicting)        422 {"error":"the config change does not pass validation: the change conflicts with the current main: rebase it onto origin/main and propose it again","validation":{"check":{"SchemaVersion":0,"Rows":null,"Warnings":null}}}
POST /api/tasks/1/config-change/approve   (gma_ token, agent API on)        403 {"error":"POST /api/tasks/{id}/config-change/approve is not part of the agent API (docs/agent-api.md)"}
POST /api/tasks/1/config-change/reject    (gma_ token, agent API on)        403 (same, reject)
POST /api/tasks/1/config-change           (gma_ token, agent API on)        403 (same, propose)
POST /api/tasks/1/config-change/approve   (no credential)                   401 single-tenant refusal
grain api POST /api/tasks/1/config-change/approve  (task 2, api:tasks:write) grain: {"error":"… is not part of the agent API (docs/agent-api.md)"}
POST /api/tasks/1/config-change/reject    (browser, with reason)            200
POST /api/tasks/1/config-change/approve   (browser, valid)                  200
```

grain/config history at the end (`git -C data/state-repo log --oneline`):

```
fdaa1ce Update grain state
8b11c8a Name this environment walkthrough-env-c          <- approve merge (browser)
3519aee Name this environment walkthrough-env-c          <- the session's proposal
c23eb84 operator: rename environment on main             <- 3b's conflicting move
8fdc2b7 Update grain state                               <- F2: reverts 4e639b0's schedule title
4e639b0 Rename the infrastructure review schedule        <- approve merge (API)
```

## Findings

**F1 — fixed. A refused grain/config push named the wrong remedy.** For a session holding proposal branches, a push to `main` (or another task's `grain/config/*`, or any other ref) was refused with the generic text. That text names the task's ordinary branch and `.grain/templates` `push:` sets, and says nothing about the `grain/config/<task>/<name>` branches the run may push or about `propose_config_change`. Reproduce: Director with grain/config in Writes, `git -C grain-state push origin HEAD:refs/heads/main`. Fix: `configProposalUpdates` also returns the run's prefix, and the refusal names `refs/heads/grain/config/<task>/<name>` and says main changes only through `propose_config_change` and a person's approval (`pkg/gitproxy/refupdates.go`). This is message text only: what is granted is unchanged. Regression: `TestAuthorizeRefUpdatesAllowsConfigProposalBranchesButNeverMain` asserts the new text. Verified live above. *Trust-boundary path (git proxy), so it gets a security review.*

**F2 — follow-up. Validation passes, and the session is told "Approved and applied", for an edit grain reverts on the next tick.** Changing a grain-owned column of a `system:*` schedule (here the title of `system:infra-review`) passes `ValidateCandidateConfig` with no warning, and the card shows all green. The approval merges it and the session is told it was applied. Then the import logs `changed content grain owns on one of its own schedules … this change is undone rather than kept`, the reconcile resyncs from the template, and the next export (`8fdc2b7`) writes the revert back to `main`. Reproduce: 2a + 2b above. Fix: the candidate validation should refuse, or at least warn, about grain-owned schedule columns, so the card and the tool say so before a person approves. This is in `pkg/staterepo` (trust boundary), so it is filed rather than fixed here.

**F3 — follow-up. A non-Director session with grain/config in Writes gets the proxy grant and the tool, but no checkout.** `prepareCheckoutWithReuse` makes `grain-state` only for `DirectorSessionID`, while `GitConfigProposalPrefix` and `ProposeConfigChange` key on Writes alone. The tool's script runs `cd grain-state`, so for such a session it would fail with "Could not commit and push". Seen live: the Reader got no checkout (it holds Reads only, so the tool was correctly absent). The Writes-without-Director case was not run. Either make the checkout for any session with grain/config in Writes (which is what PR #334's commit message says), or withhold the tool there.

**F4 — plan vs implementation, no change.** An invalid tree is recorded and parked with Approve disabled and the validator's message shown, rather than refused at propose time. The tool tells the session it may fix it and propose again. That is the designed behaviour (the tool description and `configChangeRequestBody` say so), and the plan's wording is what is off.

**F5 — cosmetic.** The 422 body's `validation.check` is serialized with Go field names (`SchemaVersion`, `Rows`, `Warnings`): `staterepo.CheckReport` has no JSON tags. The card does not read it.

## Not run, and why

- A Docker or Granule run host: host-directory sandboxes were used to fit the time box (see Environment).
- A real model: the sandbox has no agent credentials. The stub custom backend made the real MCP calls.
- Notification and standing-task wakes while pending (5c): no source of either targeted the Director. The unit tests cover them.
- A long diff in the card (6): the diffs walked were short. The layout checks pass at both widths, but a 480 px-max diff scroller full of long lines was not shot.
- A committed Playwright spec for the bounding boxes: the assertions ran against the real daemon with `walk-config-change.mjs` (kept beside this report) rather than as a spec over `demoServer`, which has no config repository.
