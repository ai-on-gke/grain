# Proposer role, bulk member add and the New Team entry (test plan 2026-10-06 §4)

**Date**: 2026-10-07
**Branch / base**: `grain/task-2382`, cut from `candidate`.
**Commit tested**: `dafd69778` (candidate tip at the start of the run). All three commits under test are on it:

| Commit | What | `git branch -r --contains` |
|---|---|---|
| `ea6453fd6` | `TeamRoleProposer` | `origin/candidate` |
| `5fb884a53` | "Add several at once" in Team settings › Members | `origin/candidate` |
| `15dd17df9` | one "+" on the switcher's New Team entry | `origin/candidate` |

`8fe0b904d` ("Anyone may propose") is on `origin/main` only and is §9's task, so it was not walked.

**Environment**:
- Debian sandbox, Linux x86_64, Go 1.26.6, Node 20.19.0.
- Playwright 1.62.1 with Chromium headless shell 1234, from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- The server was the real `grain demo` over its embedded SQLite store. It served the production frontend: `cd ui && npm ci && npm run build` into `pkg/ui/static`, embedded by the `go build` in `ui/e2e/global-setup.js`.

## Stubs

None of grain's own APIs or React were mocked.
- **Demo defaults:** demo's built-in GitHub simulator (no real GitHub), and no model sessions. Nothing here dispatches an agent.
- **MCP check:** an `agent.Result` carrying a `propose_task` tool call, handed to `orchestrator.ProcessResult` (the fake agent of §4.4). The repository was the `githubsim` one the orchestrator tests use.
- **Unit test:** `UseTemplateOverlay.test.jsx` mocks `api.js`, like every other component unit test here.

## Personas

Everyone was a password account signed in with `POST /api/auth/login`. Each browser context carried its own session cookie, and the Go test sent a real `grain_session` cookie. No identity header or claimed principal was used. The unauthenticated loopback context was used once, to set `adminAccounts: ["admin"]`, which is the bootstrap every demo needs.

| User | Role |
|---|---|
| `admin` | deployment admin |
| `carol` | admin of `default`, not a deployment admin |
| `lead@example.com` | admin of `default`, known by address (no account) |
| `mel` | member of `default` |
| `pat` | proposer on `default` |
| `vic` | viewer on `default` |

## Commands

```sh
cd ui && npm ci && npm run build
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/team-proposer-walkthrough.spec.js --project=chromium
npx vitest run src/components/UseTemplateOverlay.test.jsx
go test ./pkg/ui -run 'TestProposer' -count=1
go test ./cmd/grain -run TestUnapprovedNote -count=1
go test ./pkg/orchestrator -run TestProposalFromAnApprovedProposerTask -count=1
make security
# CLI, against `grain demo -addr 127.0.0.1:18099 -open=false`:
grain -server http://127.0.0.1:18099 -token "$PAT_SESSION" create -repo acme/widgets -title "CLI proposal from pat"
grain -server ... -token "$PAT_SESSION" create -repo acme/widgets -title "CLI pat asks to approve" -approve -auto-merge -position front
grain -server ... -token "$PAT_SESSION" create -repo acme/widgets -title "CLI pat interactive" -interactive
grain -server ... -token "$MEL_SESSION" create -repo acme/widgets -title "CLI mel approved" -approve
```

## Outcomes

### 1. Browser walkthrough (`ui/e2e/team-proposer-walkthrough.spec.js`, 7 tests, all pass)

**New task as `pat`.**
- The form shows the proposer note and no Approved box.
- Filing gives `proposed`, `approved: false`.

**Templates › Use… as `pat`**, on a template `mel` made with `autoMerge: true`:
- The task is filed `proposed`, unapproved.
- Before the fix the form had **no** proposer note and **did** offer the Approved box. This was **Bug 1**, now fixed.

**`pat`'s own proposal.**
- The pane offers only Edit and the "Reply..." comment box. There is no Approve, Run now, Close or Defer.
- The Capabilities picker is shown, disabled (`aria-disabled="true"`).
- The edit form has Title and Description, and no Review or Base branch. Saving sends exactly one `PATCH {"title","description"}`.
- A comment with `notes.txt` attached posts, and both show in the pane. The stored task carries the new title and the attachment.

**`mel`'s task, viewed by `pat`.** No Edit, Approve, Run now, Close, Defer or comment box.

**After approval.** `mel` presses Approve in the pane. When `pat` reloads, the Edit button and the comment box are gone.

**375 px.**
- The proposer note sits inside the viewport, and the page has no horizontal scroll.
- A task typed in and filed by pressing Enter on Create task lands `proposed`.
- The repo is chosen with `selectOption`, so this is not a pure Tab-order walk.

**`vic`.** `POST /api/tasks` returns 403.

**Bulk add as `carol`** (a team admin, not a deployment admin):
- Pasting two lines into the single Username box moves them into the bulk form.
- A 120-address list previews as "120 new" and offers "Add 120 people".
- The 25-line Google Group export previews as **"8 new, 1 already on the team, 2 invalid (1 duplicate merged)"**:
  - `mailto:linus@example.com` is read as `linus@example.com`.
  - `ADA@example.com` merges with `ada@example.com`.
  - `lead@example.com` is shown as "Already a member (Admin)".
  - `typo@` and `bad@@example.com` are Invalid.
- "Add 8 people" sends one `PUT {"addMembers":[…8 × member]}`. The admin is left out of it.
- After a reload, the table shows the eight as Member, and `carol` and `lead@example.com` as Admin. `GET /api/teams/default` agrees, with `pat` still proposer and `vic` still viewer.
- Bare words in an address list ("Ada", "Owner", a bare username such as `carol`) are set aside as labels. This is the behaviour `bulkMembers.js` documents, so a current admin in a Group export was given by address.

**Team switcher.** The New Team entry has exactly one icon and no "+" in its text, at 1400×900 and at 375×812 (where the switcher opens from the navigation menu).

### 2. API refusal table (`pkg/ui/proposer_session_refusals_test.go`, real session)

Each row is 403. Tasks, schedules, templates and teams marshal byte-for-byte the same before and after each request.

| Write | Result |
|---|---|
| `POST /tasks/{own}/capabilities` attach `gcp-key` | 403, unchanged |
| `PATCH {own}` `team`, `base`, `repo`, `autoApproveProposals`, `title`+`autoMerge` | 403, unchanged |
| `POST /tasks/{own}/move` to a team `pat` is a member of | 403, unchanged |
| approve, Run now, close on own; Run now on `mel`'s | 403, unchanged |
| `PATCH` / comment on `mel`'s task | 403, unchanged |
| `POST /api/tasks` `interactive: true` | 403, unchanged |
| `POST /api/console-sessions` | 403, unchanged |
| schedule create, PATCH, run, DELETE | 403, unchanged |
| template create, PATCH | 403, unchanged |
| `PUT /api/teams/infra` (self-promotion) | 403, unchanged |

`approved: true` on create is not refused, directly or through the auto-merging template. It returns 201 and files a task that is `proposed`, with `Approval` and `ApprovedAt` nil and the creator set to `pat`. The same session's allowed writes (rewording and commenting on its own proposal) succeed, so the 403s come from the role and not from a broken sign-in.

### 3. CLI

- `grain create` with `pat`'s session as `-token` (sent as a Bearer token) filed task 13 `proposed`, with creator `pat`.
- `-approve -auto-merge -position front` filed task 14 `proposed`. It kept `autoMerge: true` for the approver to see. Before the fix, the output was `state: proposed` alone, with no word that `-approve` had been overruled. This was **Bug 2**, now fixed: the CLI prints this on stderr:
  `grain: task 17 was filed as a proposal, not approved: it waits for a member or admin of team "default" to approve it`
- `-interactive` exits 1 with `you hold the proposer role on team "default", so cannot start an interactive task…`.
- `mel` with `-approve` gets `queued` and no note.

### 4. MCP: `propose_task` from an approved proposer-filed task

`pkg/orchestrator/proposer_task_proposals_test.go` builds the parent as `ui.Client.CreateTask` stores it for `pat`: `pat`'s origin and creator, team `infra`, and `mel`'s approval.

**Recorded.**
- If the parent was filed asking for auto-merge and approved as filed, the proposal is filed **approved** by the auto-submit principal (`model.AutoSubmits`).
- Otherwise the proposal is filed **unapproved**.
- Both are filed in `infra`.

**Intended.** The task names `docs/multi-team-architecture.md`, which does not exist in this tree. The proposer section lives in `docs/multi-tenancy-design.md` (the RBAC table and the paragraph after it). That paragraph says: "Proposals an agent files from a task a proposer filed are unaffected: that task only runs once a member or admin has approved it." The code agrees: the creator's role is not consulted, and the member's approval of the task as filed is what the proposals inherit. **No mismatch, no fix.**

One consequence worth knowing: a proposer may ask for auto-merge, and it is kept. A member who approves such a task without reading the auto-merge setting also lets that task's proposals queue themselves within `AutoSubmitLimits`. That is the documented "kept for the approver" design, not a bug.

## Findings

1. **Templates › Use… offered a proposer the Approved box, and no note.**
   - **Reproduction:** as a proposer on team T, open Templates, press Use… on a T template, and look under the form. With `approvedByDefault` the box was even pre-ticked. The server still filed the task proposed, so this was misleading, not an escalation.
   - **Fix:** `ui/src/components/UseTemplateOverlay.jsx` now uses `isProposerTeam(config, template.team)` like NewTaskOverlay. It hides the box, sends `approved: false` and shows the same `proposer-note`.
   - **Regression tests:** `UseTemplateOverlay.test.jsx` fails without the fix and passes with it. The e2e spec covers the same thing in the browser.
2. **`grain create -approve` as a proposer gave no sign the flag was overruled.**
   - **Reproduction:** §3 above.
   - **Fix:** `cmd/grain/taskcmd.go` `unapprovedNote` prints a note on stderr. `-json` stays a single object.
   - **Regression test:** `TestUnapprovedNote`.

No overlap with `2026-10-06-daily-exploratory-ui-evening.md` or `2026-10-07-teams-ui.md`. Neither filed either finding. The evening run's "Select-all … undersells a proposer" wording note is still unfiled and was not re-checked here.

## Notes for the deployment

- `docs/multi-team-architecture.md` is referenced by this deployment's repository notes and by the test plan, but it is not in the repo. The text it should hold is in `docs/multi-tenancy-design.md`. Either the note should name that file, or the document should be added.

## CI verdict

**Green.** Grain CI's full suite (`.grain/workflows/tests.yml`) passed on `2e297c03f`, run `f462ea454ff5173b`. The jobs were frontend, go, installer, lint, package cache, postgres, terraform and ui e2e. The new spec ran inside `ui e2e`.

**Known flake recurred.** The first run, `577ddecf949b038d`, failed `frontend` on `src/App.narrowing.test.jsx:89`: the `findByText("Long done")` 10 s timeout listed in the repository notes. Everything else in that run was green. The same file passed locally (12/12). An uncached `retrigger_ci` run, `53cc422641b147a3`, then passed `frontend` with 3115/3115 tests.

**Security suite.** `make security` passed locally: `go vet ./...` was clean and `tests/security` was ok in 160 s. No `.grain/baselines/` file was touched.
