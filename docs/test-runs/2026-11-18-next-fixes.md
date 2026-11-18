# 2026-11-18 §8 walkthrough: `/next/` fixes (schedules, Fleet, Inbox, Mermaid, headings, Processes)

Plan: [`docs/test-plans/2026-11-18.md`](../test-plans/2026-11-18.md) §8, "What is NOT proved" items 1–8. Filed by [Task #7014](/tasks/7014) and walked by [Task #7059](/tasks/7059).

PRs in scope: #1762 (schedule firing base, held wording, `→ held`, Fleet live region), #1802 (alert bodies per raise, Mermaid lockdown, Agents inspector), #1440 (heading levels in split panes), #1826 (Inbox bulk verb, changelist diff preview, read-only composer), #1506 (QA publish settle), #1818 (Processes unsaved-edit guard), #1624 (disconnected banner coverage).

## Environment

| | |
|---|---|
| Commit walked | `848f5271c` (`main` with PR #1834), built with `make frontend && go build ./cmd/grain` |
| Fix commit | `fix(ui-ng): stop Agents cards and a workstream's section skipping a heading level` (this branch) |
| Real daemon (items 1, 2, 3, 6, 7) | `grain daemon -sandbox-port-base=0 -host-sandboxes` over **SQLite 3.40.1**. It runs the 2026-11-13 harness (`docs/test-runs/2026-11-13-grain-health-exit-codes/env.sh`: ghsim `127.0.0.1:4284`, daemon `127.0.0.1:4280`, a fake agent CLI). The agent wrapper `2026-11-18-next-fixes/fake-agent.sh` records each run's checkout. From item 6 on, the daemon is the `grain_headerverifier.go` build (`-ui-require-verifier=walk-header`), so a browser can sign in as a real `grain/viewer`. PostgreSQL was **not** walked; see "Not covered". |
| Demo (items 4, 5, 8) | `grain demo -github-sim -fake-ci -reconcile 2s` on `127.0.0.1:4290` (old build) and `127.0.0.1:4291` (fixed build) |
| Browser | Playwright Chromium 151.0.7922.34 (headless), Node 20.19.0 |
| Widths and themes | 1440×900 and 390×844, light and dark |
| Helpers | `docs/test-runs/2026-11-18-next-fixes/` (`lib.mjs`, `item1-firing-base.mjs`, `item2-fleet-live-region.mjs`, `item3-alert-reraise.mjs`, `item4-hostile-mermaid.mjs`, `item5-heading-outline.mjs`, `item6-roles-changelist.mjs`, `item7-unsaved-guards.mjs`, `item8-phone-sweep.mjs`, `fake-agent.sh`, `grain_headerverifier.go` with `//go:build ignore`). Run each from the repository root: `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright BASE=… CODE=… node docs/test-runs/2026-11-18-next-fixes/<item>.mjs` |

## Outcomes

| # | Item | Outcome |
|---|---|---|
| 1 | Firing base and held wording on a real daemon | **PASS** (SQLite), plus one finding (F1) |
| 2 | Fleet live region over a minute, two stacked held banners | **PASS** (real daemon) |
| 3 | Alert re-raise with real alerts while `?family=` fails | **PASS**, plus one finding (F2) |
| 4 | Hostile `%%{init}%%` in a comment and a doc, light and dark | **PASS** |
| 5 | Heading outline across panes | **FAIL → fixed** (F3, F4) |
| 6 | Read-only role and changelist task on a real daemon | **PASS** |
| 7 | Unsaved guards with real and failing saves | **PASS** (26/27; the miss is F1) |
| 8 | Phone sweep at 390 px, light and dark | **PASS** |

### Item 1: a schedule's firing base, held and fired, on a real daemon

Setup: `acme/widgets` has `main` (`d96e7e9`) and `release` (`e79e47d`, one commit ahead). There are two templates: `template-1` is bound to `acme/widgets@release`, and `template-2` is bound to `acme/widgets` with an empty base (the default branch). `sched-1` fires `template-1` and `sched-2` fires `template-2`, both every 60 minutes. After creating them, I wrote stale bases straight into the store: `sched-1.base='stale-main'` and `sched-2.base='release'`. This is the state from before the binding, where the stored base used to win.

1. `GET /api/schedules` reported `sched-1` base `release` and `sched-2` base `null`, not the stored values.
2. `/next/processes/schedule/sched-1` showed Base `release`. `/next/processes/schedule/sched-2` showed no Base row (the default branch); `stale-main` and `release` appeared on neither. Classic `/schedules` lists both schedules, with their templates and `acme/widgets`, but no base at all, so it cannot disagree.
3. **Run now** (`POST /api/schedules/{id}/run`) filed task 2 with base `release` and task 3 with no base.
4. **Orchestrator cycle** (stale bases written again, `next_run_at` in the past) filed tasks 4 and 6/8 into `release` and tasks 5 and 7/9 into the default branch. The firing also rewrote the stored bases to `release` and `NULL`. The run checkouts, as `fake-agent.sh` recorded them: task 8 had `HEAD` at `e79e47d` (the `release` tip) and task 9 at `d96e7e9` (the `main` tip).
5. **Publishing hold:** I set `sched-1.skip_when='publishing-held'` (the column a declared `skip-when:` sets), added a `base_branch_release` row for `acme/widgets@release` with 12 failures, and filed triage task 10 tagged `base-release-triage:acme/widgets:release`. The next cycle skipped the occurrence. `/api/schedules` gave `suppressed.reason = publishingHeld`. The detail page read **"Suppressed — publishing held by task 10 (acme/widgets release has published nothing since 2026-11-17 12:00 UTC) — Open task #10"**, and the skip row read **`→ held`**.
6. When I closed task 10, the held occurrence fired late as task 11. The row then read **`→ fired #11`** and the suppression banner was gone.

No console errors or 4xx/5xx responses on any page.

### Item 2: Fleet's live region (real findings, not a stub)

Setup: two real held findings, `acme/widgets@main` (triage task 15) and `acme/widgets@release` (triage task 14). I set `main`'s `failing_since` 30 s short of six hours, so its banner's age would tick during the minute. The script polled every 10 s for 70 s, taking an `ariaSnapshot()` of every visible `[role=status]` or `[aria-live]` element, and a `MutationObserver` recorded every change inside one.

- Every snapshot was the same two regions: `- status: "Publishing held · acme/widgets@main:"` and `- status: "Publishing held · acme/widgets@release:"`.
- The full banners were 596 and 603 characters. `main`'s detail text changed from **"5h ago"** to **"6h ago"** at t≈30 s.
- **0 changes** inside a live region over 70 s. The tick happened outside `role="status"`, so a screen reader re-announces nothing, and the titles name each branch.

### Item 3: alert re-raise with a real alert, `?family=` failing

The alert is the daemon's own `pipeline.not-publishing:acme/widgets@release`, raised by `automation:base-branch-release`. It re-raises only when its severity rises (`ensurePipelineNotification`), so I set up the re-raise like this: I cleared the alert (the release published), let a fresh warning be raised (5 releases, 6 h), and selected it in `/next/inbox`. Then I routed every `GET /api/notifications?family=…` to 503 and pushed the finding past a day (9 releases, 50 h). The daemon re-raised the same alert as critical (`raiseCount` 2).

- Before: "…since 2026-11-17 23:50 UTC, 6h ago: 5 releases…". After: "…since 2026-11-16 03:50 UTC, 50h ago: **9 releases**…". The 5-release body was no longer anywhere on the page.
- The page tried one blocked `?family=` read (`503 GET /api/notifications?open=1&family=pipeline.not-publishing`). Its only console error was that resource's 503, and nothing was thrown.

### Item 4: hostile Mermaid in a comment and a doc

The same block went into a comment on demo task 3 and into a memory document named `hostile-mermaid` (`/next/context`, Memory Documents):

```
%%{init: {"theme":"dark","themeVariables":{"primaryColor":"#ff00ff",…},"themeCSS":".node rect{fill:#ff00ff !important} svg{background:#00ff00 !important} text{fill:#00ff00 !important}","securityLevel":"loose","htmlLabels":true,"fontFamily":"Comic Sans MS","darkMode":true}}%%
flowchart LR
  A["<img src=x onerror=window.__pwned=1>"] --> B[click me]
  click B "javascript:window.__pwned=2"
```

The script clicked "click me" too. In all four cases (light comment, light doc, dark comment, dark doc) the result matched:

- Light: node fill `rgb(238, 243, 253)` and text `rgb(27, 29, 34)`. Dark: node fill `rgb(31, 42, 61)` and text `rgb(228, 230, 235)`. That is the page's palette, and no `#ff00ff` or `#00ff00` showed up anywhere.
- The font was `"Google Sans Text", …`. The SVG background stayed transparent, and no `<style>` carried the hostile values.
- `window.__pwned` stayed unset. The diagram held 0 `<foreignObject>`, 0 `<img>`/`<image>`, 0 `<script>` and no `on*` or `javascript:` attribute.

Not checked: a live theme switch on an already-drawn diagram. Each theme loaded fresh.

### Item 5: heading outline across panes (bugs F3 and F4, fixed)

`item5-heading-outline.mjs` collects the visible `h1`–`h6` and `[role=heading][aria-level]` elements, and runs axe's `heading-order`, `page-has-heading-one` and `empty-heading` rules. It covers three states: a task selected, **Edit** open, and **Shape with Director** open.

| Page | Before (`848f5271c`) | After (fix) |
|---|---|---|
| `/next/tasks?task=3` | PASS: `h1 Tasks › h2 #3 … › h3 Edit task #3 › h4 Shape with Director` | PASS |
| `/next/workstreams` (stream open, task picked) | **FAIL**: `h1 <stream> › h3 In this stream › h2 #8 …`; axe `heading-order: .ws-rel-head > h3` | PASS (`h2 In this stream`) |
| `/next/agents?task=3&pane=details` | **FAIL**: `h1 Agents › h3 #8 … › h3 #3 … › h2 #3 …`; axe `heading-order: #agent-title-8` | PASS (`h2 Running agents`, screen-reader only) |
| `/next/task/3` | PASS: `h1 #3 … › h2 Edit task #3 › h3 Shape with Director` | PASS |

Exactly one `h1` everywhere. The embedded task detail (PR #1440) was right on all three panes; what skipped were the neighbours around it.

### Item 6: a read-only role and a changelist task on a real daemon

`bob@example.com` signs in through `X-Walk-User` and holds `grain/viewer`, set with `PATCH /api/access/principals/user:bob@example.com` to `{"role":"grain/viewer"}`. His `/api/config` reports `role: grain/viewer`, `full: false` and 21 read scopes, and a comment `POST` from him gets 403.

- `/next/task/2` (closed): **no composer**, with "Your role grain/viewer does not hold tasks:write, which this needs. Ask an admin for a role that includes it."
- `/next/task/12` (open proposal): the composer is shown but **disabled**, Send is disabled, and the same note appears.
- `/next/inbox`: the group "Select all" controls exist. After selecting, **`bulk-approve` and `bulk-defer` are disabled**. Bob's session sent **no write request** and got no 4xx/5xx response.
- The changelist (repository-less) task #12, as the owner: selecting it in the Inbox showed Approve / Defer / Decline, and its Changes tab read "Proposals do not have a branch diff yet." There were **0 `/diff-preview` requests** and no 404s. A proposal has no change yet, so the tab's "This task's change is a changelist…" line (CI covers it over the demo's seeded changelist) didn't apply here.

### Item 7: unsaved guards with real saves (real daemon)

For each dialog (schedule `sched-2`, template `template-2`, suite `suite-1`), the script made a real save and then a save failed by a routed 500 (`walk: store refused the write`).

- A real save closed the dialog without a "Discard unsaved changes?" prompt. The template title and suite name were then on the server.
- A failed save kept the dialog open with the draft intact, showed "walk: store refused the write", and asked nothing. The next Escape asked **once**, and Discard closed both dialogs.
- 26 of 27 checks passed. The miss was the schedule's base: the save "succeeded" but the server kept the template's binding (F1).

### Item 8: phone sweep

I ran `make ui-screenshots` (via `node e2e/screenshot.mjs` from `ui/` with `BIN=`) over the old build. It shot `/next/processes/schedules`, `/next/fleet`, `/next/inbox`, `/next/tasks?task=3`, `/next/workstreams`, `/next/agents`, `/next/task/3` and `/next/processes/quality`, at desktop and phone in light and dark (`ui/screenshots/before/contact-sheet.png`). I shot it again on the real daemon for the held schedule `/next/processes/schedule/sched-1` and Fleet's two held banners, at phone width, full page (`ui/screenshots/realdaemon/contact-sheet.png`).

This sandbox's file reader shows PNGs only as bytes, and `LOOK=1` has no key here. So `item8-phone-sweep.mjs` checks the same pages in the DOM at 390×844 in light and dark: page `scrollWidth`, any visible element past the viewport outside a scroll container, and page errors. All 22 page/theme pairs passed (16 on the demo, 6 on the real daemon): `scrollWidth` 390/390, no element past the edge, body `rgb(255, 255, 255)` light and `rgb(24, 26, 32)` dark, no errors, no 4xx/5xx responses.

## Findings

**F1. A schedule bound to a repo-bound template silently discards the repo and base you edit (P2, not fixed; follow-up proposed).** The Processes schedule dialog offers Repository and Base branch for every schedule. When the schedule fires a template bound to a repository, the server replaces both with the binding (`Template.FiringTarget`, applied in `scheduleContentFromTemplate`) and answers 200:

```
curl -X PATCH $D/api/schedules/sched-2 -H 'Content-Type: application/json' \
  -d '{"base":"release","repo":"acme/other"}'
→ 200 {"repo":"acme/widgets","base":null,"templateId":"template-2",…}
```

The UI closes the dialog as if the edit were saved, and the detail page then shows the old target. The server rule is right; the dialog should show the bound target read-only, with a "set by template …" note, or the server should refuse the edit. That is a design choice across the schedule dialog, the API and classic, so it is a follow-up rather than a fix here.

**F2. A `pipeline.not-publishing` alert keeps a stale body until its severity rises (P3, not fixed; noted).** `ensurePipelineNotification` re-raises only on a severity rise. The alert raised while triage task 10 was open still said "Further releases on this branch are held pending resolution of triage task 10" after task 10 had closed, in bob's Inbox at about 05:43 UTC, until it escalated to critical at 05:44. Fleet reads the live finding and was right throughout. This is the level-triggered design working as written, but an Inbox reader can see "held" when nothing is held. Worth a look if it bites; no task filed.

**F3. `/next/agents` (ungrouped cards) skips from `h1` to `h3` (P1 under the book's "headings are in order" rule; fixed).** Reproduce on `848f5271c`: `grain demo`, open `/next/agents`, and run axe `heading-order`. It reports `#agent-title-8`. The fix adds a screen-reader-only `h2` "Running agents" when the list has no band header, and the section now has an accessible name in every case.

**F4. A workstream's "In this stream" section is an `h3` under the page's `h1` (P1; fixed).** Reproduce: open `/next/workstreams`, open a stream, pick a task, and run axe `heading-order`. It reports `.ws-rel-head > h3`. The fix makes it an `h2`, with the same CSS.

The fix for F3 and F4 is commit `fix(ui-ng): stop Agents cards and a workstream's section skipping a heading level`, with a new spec `ui-ng/e2e/next-pane-heading-outline.spec.js` and a line in the design book's heading rule.

- **Verification:** the new spec passes. So do `next-full-system.spec.js`, `next-agents.spec.js`, `next-agents-grid-scroll.spec.js` and `next-agent-details-scroll.spec.ts` (10 passed), the `src/app/agents` and `src/app/workstreams` unit tests (102 passed), and `make fmt-frontend`. The `item5` walk is clean on the fixed build.
- **Screenshots:** `ui/screenshots/after/` covers `/next/agents` and `/next/workstreams`; `before-wsdetail/` and `after-wsdetail/` cover an open stream, all at desktop and phone in light and dark.
- **Nothing moved on screen:** element boxes were identical on the old and new builds at 1440 and 390 px. "In this stream" sits at 882,297 and is 102×18, 13px/600, on both builds. The new `h2` is `position: absolute`, 1×1. The remaining pixel differences are live data: ages, CPU, and a card that was "Quiet" in one demo and "Working" in the other.

## Not covered

- **PostgreSQL:** the real-daemon items ran over SQLite only.
- **The three-review mandate** (Design, Functional, Adversarial) for the F3/F4 change needs reviewers other than the author. This run is the author, so those reviews were not done here.
- **QA studio publish settle (PR #1506) and the disconnected banner (PR #1624):** only rendered (`/next/processes/quality` in the sweep). They were not exercised beyond what CI covers.
