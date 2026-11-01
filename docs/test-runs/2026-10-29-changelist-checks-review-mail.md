# Changelist checks, review threads, Mail/Publish and reviewers walkthrough — 2026-10-29

- **Plan:** `docs/test-plans/2026-10-29.md` §1 ("Changelist tasks I": PRs #98, #109, #113, #125)
- **Date:** 2026-10-29, 15:38–16:45 UTC
- **Task:** grain/049f1e09/task-1795, base `main`
- **Commit tested:** `2f53e63a` (`main` at the start of the run, "Merge pull request #130"). The fixes below were then checked on the same tree with them applied (branch `grain/049f1e09/task-1795`).
- **Environment:**
  - Debian GNU/Linux 12 (bookworm) sandbox (`kubernetes-granule` backend), 7 vCPUs, 15 GiB, `linux/amd64`
  - Go `go1.26.10`, Node.js `v20.19.0`
  - Playwright `1.62.1` from `ui/package-lock.json`, headless Chromium `chromium-1234` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, viewports 1440×900 and 390×844 (`isMobile`, `hasTouch`)
  - Frontend: `cd ui && npm ci && npm run build`. Server: `go build -o /home/debian/bin/grain ./cmd/grain`, copied to `/var/tmp/walk/grain-base` (unmodified commit) and `/var/tmp/walk/grain-after` (each fix as it landed).
  - Servers, each with `TMPDIR=/var/tmp/walk`, so each had its own throwaway embedded SQLite store:
    - `:8431` — `grain-base demo -addr 127.0.0.1:8431 -open=false -github-sim -fake-ci -reconcile 2s -access-code-file code.txt`
    - `:8432` — the same flags on `grain-after`, restarted after each fix and before each pass that needed the seeded CL state.
  - The plan's shared command and this task's brief both add `-sandbox-port-base=0`. `grain demo` refuses it (`flag provided but not defined: -sandbox-port-base`): that flag is `grain daemon`'s, and the demo binds no sandbox ports. The plan's command is corrected on this branch.
  - API calls: a session cookie from `POST /api/auth/access-code` (`curl -c jar`), and for agent refusals an external-agent token (`POST /api/external-agents/tokens {"name":"walk-agent","scopes":["tasks:write","tasks:read"]}`, after `PUT /api/settings {"externalAgentApi":true}`).
  - Run's view: a real `grain mcpserver -sandbox-root /var/tmp/walk/ws -server http://127.0.0.1:8432 -task <id>` over stdio (`GRAIN_TOKEN=<access code>`), driven by a throwaway JSON-RPC script (`initialize`, `tools/list`, `tools/call`). With `-sandbox-root` set this is the control surface, as for a sandboxed run. `GRAIN_MCP_SURFACE=full` was used once to compare.
  - No Gemini key, so `LOOK=1` could not describe screenshots. Screenshots were taken with `node ui/e2e/screenshot.mjs` (`URL=… CODE=… PAGES=/tasks/16,/tasks/13 THEMES=light`, desktop 1440 and phone 390) before and after, and by the walkthrough scripts. Layout was checked with bounding boxes and `scrollWidth` rather than by eye.
- **How it was driven:** throwaway Playwright scripts (not committed) that sign in and then act only through visible controls, recording `pageerror`, responses of 400 or more, and every non-GET `/api/` request with its body. The REST API was used only to read back what a gesture had persisted, and for the agent-token refusals. Committed: `ui/e2e/cl-mail-publish.spec.js` (new), and the existing `cl-review-layout`, `cl-chip-layout` and `presubmit-badge-layout` specs: 13/13 pass in Chromium.

## Stubs

Everything ran against the real server, store, built frontend, browser and `grain mcpserver`. The only boundary replaced is the one the brief names: the code review/TAP edition is not in this repository, so the demo's in-process change provider (`pkg/demo`'s `demoChangelists`) stands in for it. On the base commit that stand-in answered only Observe, Submit and Sync (finding F1). On this branch it also answers Checks, Reply/Resolve, Publish and Mail from its remembered observations. Nothing leaves the process.

Credential expiry (gap 4) cannot be triggered in the demo: its provider has no credentials to expire, and the demo does not run `change.Watch`. It is exercised by a Go test over real components instead (step 12).

## Steps and outcomes

### CL tasks, badges and buttons (walkthrough 1–2)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | List the demo's CL tasks (`GET /api/tasks`, keep those with `changelists`) | Six: #13 (`cl/612400001`, `cl/612400017`; presubmit failed with two failing checks; changes requested), #14 (passed; LGTM alice), #15 (running; in review), #16 (not run; draft, `needsMailing`), #17 (unknown; needs approval: owners, readability), #18 (passed but behind; LGTM carol). All `awaiting_submit`. | PASS |
| 2 | Each task's `clReview` on the base build | Every one: `canMail:false, mailReason:"this deployment cannot mail CLs"`, `canPublish:false, publishReason:"this deployment cannot publish CL replies"`. `POST …/change-checks`, `…/change-mail`, `…/change-publish` and `…/change-review` all answer 404 "no checks/review provider on this deployment". **F1.** | FAIL → PASS |
| 3 | Open #13–#18 at 1440 and 390 px (fixed build) | Each task view has its CL chip(s), one presubmit badge and the review badge with the expected label. Mail is enabled only on #16 ("needs mailing"); Publish replies only on #13, as "Publish replies (2)" with the tooltip "Left as drafts: the run ended while its reviewer was still commenting". At 1440 px the actions sit at x=1152, 260×31; at 390 px they take the full width (357×40). `scrollWidth` = 390 on #13 and #16 at 390 px. No page errors. | PASS |
| 4 | Press Mail on #16 (1440 px) | `POST /api/tasks/16/change-mail {}`. On the base commit nothing reached the conversation (**F2**). Fixed: "Mailed cl/612400103." appears in the conversation, Mail goes disabled at once ("already mailed"), and the review badge reads "in review". | FAIL → PASS |
| 5 | Press Publish replies (2) on #13 | `POST /api/tasks/13/change-publish`. Fixed: "Published 2 replies on cl/612400001." appears, and the button goes disabled as "Publish replies" ("no replies are waiting"). Base: no conversation entry (**F2**). | FAIL → PASS |
| 6 | `/inbox` at 1440 and 390 px | The CL rows show chip, presubmit and review badges. Covered for layout by the existing inbox specs, which still pass. | PASS |

### Refusals to agents (walkthrough 3)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 7 | External-agent token (`gma_…`, `tasks:write`): `POST /api/tasks/16/change-mail`, `…/change-publish`, `…/change-sync` | 403 "an agent cannot mail a CL, publish its replies or sync it: who reviews a change, and when, is a person's decision". With the external agent API off, 403 "external agent API is disabled in Settings" comes first. | PASS |
| 8 | Same token: `POST /api/tasks/13/change-review` with another CL's thread | 403 "POST /api/tasks/{id}/change-review is not part of the agent API". The foreign-thread refusal itself is step 10's: "thread cl/612400999:c1 is not on this task's CL -- nothing was drafted." | PASS |
| 9 | Same token: `POST /api/tasks` and `PATCH /api/tasks/16` carrying `reviewers` | Both 403 "an agent cannot set reviewers: who reviews a change is a person's decision (changelists.md §5)". | PASS |

### The run's view over a live `grain mcpserver` (walkthrough 4, gap 1)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 10 | `grain mcpserver -sandbox-root … -server … -task 13`, then `tools/list` and calls | On the base commit `tools/list` (control surface) had `pull_request_status` and `wait_for_checks` but no `open_pull_request`, `reply_to_pull_request_comment` or `resolve_review_thread`, though the CL thread wake tells the run to call all three (**F4**). Fixed: all five are listed. `pull_request_status` returns the provider's text with "Health: failing." `wait_for_checks` returns "Verdict from demo: failed."; with `jobs: ["//devtools/grain/retry:lint"]` it lists only that check and adds "This wait was narrowed to …, so it does not satisfy this task's gate". A 3-second timeout is raised to the 30-second minimum and the answer says so. Replying to another CL's thread is refused; replying on #13's own CL is refused with "task 13 has no run to attribute a draft to" (the demo has no runs). `retrigger_ci`, `run_ci_job` and `get_ci_log` are not on the control surface, as designed; on the full surface each refuses in the provider's words. | FAIL → PASS |
| 11 | `open_pull_request` (with and without summary/body) on #13 | 500 "task 13 has no repo attached, so there is no branch to open a pull request for": the demo's CL tasks carry no change target, because no edition registers an `Opener` here. Observation O1. | N/A (O1) |
| 12 | Reading `task_run_check_wait.provider` | Not observable through the demo. The orchestrator writes it from a finished run's transcript, and the demo starts no runs. `TestChangeChecksToolsOverALiveServer` (`pkg/daemon`) does it over real components: the CL task's checks tools, as `mcpserver` registers them (`NewChangeChecksTools` over `DaemonChangeChecks`), run against a real `ui.Server` and SQLite store over HTTP. It reads each `wait_for_checks` answer back with `mcp.ReadCheckWait`, the parser telemetry uses, as provider `tap` with verdicts failed and passed. A narrowed wait never satisfies the gate, and the provider sees each wait's timeout. | PASS (Go test) |
| 13 | A run reading checks, then the task view | On the first fixed build, one `change-checks` status call on #15 made its `clReview` disappear from the task JSON and page: the review badge, Mail and Publish replies all vanished (**F3**). Fixed: after the same call #15 keeps `{"state":"in_review",…}`, and the presubmit badge still updates. | FAIL → PASS |

### Reviewers end to end (walkthrough 5, gap 3)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 14 | New task form: Title, Repos acme/widgets, Reviewers "alice, bob", Create task | `POST /api/tasks {…"reviewers":["alice","bob"]…}`; task #19 stores `["alice","bob"]`. | PASS |
| 15 | Processes → + New → Template: Name, Task title, Reviewers "carol", Add template | `POST /api/templates {…"reviewers":["carol"]…}`; the template stores `["carol"]`. | PASS |
| 16 | That template's Use… → Create task | `POST /api/tasks {"templateId":"template-3",…}` with no reviewers in the body; the filed task #25 has `["carol"]` from the template. | PASS |
| 17 | Task #19 → Edit → Reviewers "dave" → Save | `PATCH /api/tasks/19 {…"reviewers":["dave"]…}`; stored. | PASS |
| 18 | CL task #16 → Edit → Reviewers "erin, frank" → Save, then Mail | `PATCH` stores `["erin","frank"]`. Mail sends `{}`, and the conversation reads "Mailed cl/612400103 to erin, frank.": the task's reviewers are Mail's default. | PASS |
| 19 | Agent-authenticated `POST`/`PATCH` with reviewers | Step 9. | PASS |

### Credential-expiry health (walkthrough 6, gap 4)

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 20 | Provider answers `CredentialsExpiredError` | Covered by `TestChangeChecksToolsOverALiveServer`, since the demo cannot do it. The run's `pull_request_status` answers `credentials_expired`. The next `wait_for_checks` answers with "blocked since …" without asking the provider. One `credentials-expired.changelist` notification is raised, once, in the real store: the one `GET /api/notifications?open=1`, and so the Alerts bell, reads. A failing `change.Recheck` leaves it open; a passing one clears it, and `pull_request_status` answers again. | PASS (Go test) |
| 21 | System → Health | There is no System → Health surface for a change provider's health in this tree. `change.HealthOf` is read only by the API refusals and the poller. The notice reaches a person only through notifications. Observation O2. | N/A (O2) |

## Findings

### F1 — the demo's stand-in CL provider could not exercise Checks, Mail, Publish or replies (fixed)

**Repro (base):** `grain demo …`. Then `GET /api/tasks/16` gives `clReview.mailReason: "this deployment cannot mail CLs"`, and `POST /api/tasks/16/change-checks {"op":"status"}` gives 404 "this task's change has no checks provider on this deployment". `before/tasks_16-desktop-light.png` shows both buttons disabled.

**Fix:** `pkg/demo` `demoChangelists` now implements `change.Checks` (presubmit from the remembered observation, narrowed by `jobs`; a running presubmit times out at once), `Replier` (drafts kept per task and CL), `Publisher` (a run's drafts wait for a person, and Publish replies publishes them) and `Mailer` (marks the CL mailed and in review). Compile-time assertions pin the four interfaces. The failed CL's task (#13) is seeded with two drafts waiting, so Publish replies is offered. `cl-review-layout.spec.js` now expects Mail enabled on the draft CL.

### F2 — Mail and Publish replies left no trace in the conversation, and Mail stayed offered after mailing (fixed)

**Repro (base, with F1's provider):** press Mail on #16. The CL is mailed but nothing is written to the conversation, and `clReview.canMail` stays `true` until the provider's next poll, so Mail can be pressed again. The same is true of Publish replies. Sync and Submit both write a conversation entry as the person.

**Fix (`pkg/ui/change_review.go`):** `MailChange` writes "Mailed cl/N[ to reviewers][ (cc …)]." plus the provider's message, and clears the remembered `needs_mailing` (draft → in review) at once. `PublishReplies` writes "Published N replies on cl/…", plus what is still waiting and why. Both are attributed to the acting person. Regression tests: `TestMailAndPublishRecordThePressInTheConversation` (Go) and `ui/e2e/cl-mail-publish.spec.js`, which starts its own demo so its presses do not disturb the shared worker demo.

### F3 — a run reading its CL's checks erased the observed review (fixed)

**Repro (with F1's provider):** `POST /api/tasks/15/change-checks {"op":"status"}` (what a run's `pull_request_status` does), then `GET /api/tasks/15`: `clReview` is gone. `ChangeChecks` remembered the checks report as the CL's whole observation, dropping `ReviewState`, `LGTM`, `NeedsApproval`, `NeedsMailing`, `Submittable`, `Reasons`, `URL` and `SnapshotAt`. Until the next poll, the review badge, Mail, Publish replies and Submit's verdict were gone. A `jobs`-narrowed wait also overwrote the presubmit badge with the narrowed verdict.

**Fix (`pkg/ui/change_checks.go`):** only the presubmit half of the observation is replaced. A submittable verdict from an older snapshot is dropped. A narrowed wait leaves the badge alone. `TestChangeChecksKeepsTheObservedReview` fails without the fix.

### F4 — sandboxed CL runs had no way to upload a fix or answer a CL thread (fixed)

**Repro (base):** `grain mcpserver -sandbox-root <dir> -server <demo> -task 13`, then `tools/list`. It has no `open_pull_request`, `reply_to_pull_request_comment` or `resolve_review_thread`. The control roster (`pkg/daemon/mcp_surface.go`) drops them because `grain pr open/reply/resolve` stand in for them, but those cannot serve a CL: `grain pr open` posts no summary or body, and `grain pr reply`/`resolve` take an integer GitHub comment ID (`pkg/gitproxy/cli_actions.go`), never `cl/<n>:<id>`. The CL thread wake (`pkg/orchestrator/reviewthreads.go`) names all three tools.

**Fix:** the control roster keeps the three for changelist tasks only. `design/design/sandbox-first-agent-surface.design.md` §6 says so, and `TestControlRosterKeepsAChangelistTasksReviewTools` pins both the CL and the grain-ci pull-request rosters.

### Observations (not changed)

- **O1:** `open_pull_request` on a demo CL task answers 500 "no repo attached". The demo's CL tasks have no change target because nothing registers an `Opener` in core, so the request takes the pull-request path, and that path reports a client condition as a 500. Exercising CL upload needs an edition, or a demo `Opener`.
- **O2:** A change provider's credential block appears only as an operator notification. There is no System → Health row, although the plan expects one. The demo also never runs `change.Watch`, so a block raised there would not clear by itself.
- **O3:** `POST /api/tasks/{id}/change-checks` is scoped `internal` but answers a signed-in person's session (200). That is harmless, because it only reads, and it is how this walkthrough reproduced F3.

## Follow-ups

None filed. F1–F4 are fixed here. O1 and O2 are design questions for the edition and Settings work rather than bugs in what §1 shipped.
