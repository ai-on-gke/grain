# Deep links to a single task comment — walkthrough, 2026-11-10

Walkthrough of [`docs/test-plans/2026-11-10.md`](../test-plans/2026-11-10.md) §5, "Deep links to a single task comment", task 3952. It covers PR #1053 (`38308fb4`, `7bcb964d`, `4653b54b`): `/tasks/<id>#comment=<id>` handled by `CommentDeepLink`, Copy link, in-place Markdown routing of `/tasks/<id>` links, `model.CommentPath` and Inbox question links.

Built from `HEAD` (`5eeb9b92`) per shared rule 1, and checked in Chromium (Playwright 1234 build, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).

## Before anything: `main` did not compile

`go build ./cmd/grain` failed on `5eeb9b92`:

```
pkg/ui/access_roles.go:1031:18: undefined: s
```

PR #1070 moved `roleApprovalWithin` onto `*Client`, and PR #1069 added a declared-push check to it that still called `s.templateDeclaredPush`. Each PR was green alone, but together they broke the build. The fix is one identifier, `c.templateDeclaredPush` (the `Server` method forwards to it anyway). It is in this branch as its own commit, because nothing in this walkthrough could be built without it. If `main` gets its own repair first, merging it is a no-op.

## Harnesses

Everything is in [`2026-11-10-comment-deep-links/`](2026-11-10-comment-deep-links/):

| File | What it drives |
|---|---|
| [`walk-comment-deep-links.spec.js`](2026-11-10-comment-deep-links/walk-comment-deep-links.spec.js) | Items 1, 2 and 4 over the worker's `grain demo -github-sim -fake-ci` (`test.use({ demoArgs })`). Copy it into `ui/e2e/` to run it. Output: [`walk-spec.out`](2026-11-10-comment-deep-links/walk-spec.out). |
| [`walk-insecure.mjs`](2026-11-10-comment-deep-links/walk-insecure.mjs) | Copy link over plain HTTP, plus every malformed fragment on a fresh load, against `grain demo -github-sim -fake-ci -addr 0.0.0.0:4272` opened at the sandbox's LAN address `http://192.168.9.4:4272`. That is not a secure context: `isSecureContext: false`, no `navigator.clipboard`. Output after the fix: [`insecure-after.json`](2026-11-10-comment-deep-links/insecure-after.json). |
| [`walk-item3.mjs`](2026-11-10-comment-deep-links/walk-item3.mjs) | Item 3 against `ui/e2e/consolerestserver`. Output: [`item3.out.json`](2026-11-10-comment-deep-links/item3.out.json). |

**Deviation for item 3:** the plan asks for a real `grain daemon -sandbox-port-base=0` with a fake agent. This sandbox has no agent CLI or model credentials for a real daemon to dispatch. Instead I used `consolerestserver`, which runs the real store, `ui.Server`, orchestrator `RunCycle` and host sandboxes. Its only scripted part is the agent: each scripted turn's tool calls go through a real `grain mcpserver` subprocess, the same path a dispatched agent's `ask_question` takes. To make this work, set `GRAIN_E2E_DEMO_BIN` to a built `grain`. Otherwise the server finds the sandbox's own in-sandbox `grain` CLI on `PATH`, which has no `mcpserver`, and every run fails with `"mcpserver" is not a command this sandbox's grain has`.

## Item 1 — every surface that draws comments

| Case | Result |
|---|---|
| Plain timeline comment (`/tasks/4#comment=1`) | In viewport, highlighted, no console errors ✅. Focus stays on the dialog container ❌ → **F2** |
| Interactive task, 200-message chat, 2nd message | The chat bubble (not the folded history copy) scrolls into view and is highlighted ✅. Focus stays in the composer ❌ → **F2** |
| A comment drawn only inside a collapsed Task history | Cannot be made over the demo: only grain's own resting/wake automation comments are left out of the chat, and the API files human comments. Covered by `CommentDeepLink.test.jsx` ("opens a collapsed group…"), which still passes. |
| Director session, by `/tasks/13#comment=4` and by `/session/director#comment=4` | Both land, in viewport and highlighted ✅ |
| Far up a long conversation | 120-comment timeline (existing spec) and 200-message chat: in viewport ✅. The task detail carries the whole conversation, so nothing is lazily loaded and missed. |
| Another task's comment id (`/tasks/4#comment=<task 7's>`) | "Comment 2 is not in this task's conversation." Stays at the top ✅ |
| Non-existent id (`#comment=99999999`) | "Comment 99999999 is not in this task's conversation." ✅ No delete route exists for comments, so "deleted" reads the same as non-existent. |
| Malformed (`#comment=abc`, `#comment=`, `#comment=12abc`, `#comment=-1`, `#comment=%31`) | **Before:** silently ignored, no notice, no highlight ❌ → **F3**. **After:** "This link's #comment=abc does not name a comment." `#Comment=1` (another case) is not our format and stays ignored. On fresh loads every malformed case gets its notice (`insecure-after.json`). In `walk-spec.out`, `#comment=12abc` reads `[]` only because that loop changes the hash in the same document every 2.5 s, and MUI's auto-hide timer, started by the first notice, closes the third (see "Not changed"). |

No `console.error` or page error on any of these over the loopback demo. Over the LAN address, Chromium logs "The Cross-Origin-Opener-Policy header has been ignored, because the URL's origin was untrustworthy" once per load. That is the server's COOP header on any non-HTTPS non-localhost origin, not a deep-link error.

## Item 2 — navigation semantics

- **Back/Forward:** I followed `[cross](/tasks/21#comment=211)` from task 22's comment in place. The URL changed with no reload (`window.__noReload` survives). Back returns to task 22 with the link visible. Forward returns to task 21, and the comment is back in the viewport and highlighted again ✅.
- **Modified clicks:** Ctrl-click, Shift-click and middle-click each open `/tasks/21#comment=211` in a new page while the source tab stays on `/tasks/22` ✅. On Linux Chromium, Meta-click is not a new-tab gesture. The handler correctly leaves it to the browser, which navigates the same tab with a full load.
- **Cross-task links in a comment** render without `target="_blank"` and are followed in place ✅. The existing spec covers this, and item 3 repeats it with an agent-written comment.
- **Reverse-proxy sub-path:** not supported by the UI at all. There is no `<base href>`, no configured path prefix, and every route and `/api` call is root-relative. A `/tasks/<id>#comment=<id>` link therefore behaves exactly as every other UI link does behind a proxy that maps the UI to its own host. That is what grain's "root-relative links" rule assumes. This is nothing specific to deep links.
- **Copy link** uses `window.location.origin` ✅ (`http://192.168.9.4:4272/tasks/4#comment=1`). **Over plain HTTP it copied nothing ❌ → F1.**

## Item 3 — Inbox and agent prompts end to end

- I filed T1 (`acme/widgets`) with 60 human comments. Its scripted run called `ask_question` (`summary: "Retry budget: 3 or 5?"`) through `grain mcpserver`, and the real orchestrator parked it `awaiting_reply` with the question as comment 62.
- `GET /api/inbox` (what `grain inbox` and agents read) gives `{"kind":"question","task":"2","summary":"Retry budget: 3 or 5?","path":"/tasks/2#comment=62"}`, which is the exact comment ✅.
- Opening that path lands on comment 62: in viewport, highlighted and (after F2) focused, with text "grain on behalf of 2 · automation … Should the retry budget be 3 or 5?" ✅.
- T2's scripted run called `comment_on_issue` with `Blocked on [see](/tasks/2#comment=62) first.` The comment renders `href="/tasks/2#comment=62"` with no `target`. Clicking it moves to `/tasks/2#comment=62` in place with no reload, and the question is in view, highlighted and focused ✅.
- The web Inbox's question row links its title to `/tasks/2`, not to the comment. The question is the newest entry, so with the default newest-first timeline it is at the top anyway, and the row also shows the question inline ("show question"). Filed as a follow-up rather than changed here (see below).
- The browser console showed four 404s, all `GET /api/repos/acme/widgets/github-branches`. The fixture has no GitHub; this is not a deep-link error.

## Item 4 — focus, announcement, contrast, 390 px

- **Focus:** before, focus never moved to the linked comment, and nothing was announced ❌ → **F2**.
- **Placement:** at 1440×900 and 390×844, in light and dark, the linked comment is in the viewport. `elementFromPoint` at its centre is the comment itself, not a sticky header or the phone's bottom bar ✅.
- **Contrast**, read off rendered pixels at the animation's peak:

| | Ring vs. surface | Body text vs. highlight, 18% (before) | Body text vs. highlight, 8% (after) |
|---|---|---|---|
| Light | 5.34:1 | 6.48:1 (timestamp 4.37:1 ❌) | 7.46:1 (timestamp 5.01:1) |
| Dark | 4.92:1 | **3.96:1 ❌** | 4.72:1 |

The dark theme's real backdrop is `rgb(58,60,63)`, because MUI's elevated Paper draws a white `background-image` overlay over `rgb(20,22,26)`. Measuring computed colours alone misses this, so the regression test measures pixels → **F4**.

Before the highlight is applied at all, the timeline timestamp (`.timeline-when`, `rgb(149,145,137)`) on that dark backdrop measures 3.52:1. That predates this feature and is filed as a follow-up.

## Findings and fixes

| # | Finding | Fix | Regression |
|---|---|---|---|
| F1 | Over plain HTTP (no Clipboard API), Copy link and every other `CopyButton` in the task pane said **Copied** but copied nothing. The fallback selected a `<textarea>` appended to `<body>`, outside the MUI Dialog, and the dialog's focus trap took focus straight back: the `copy` event fired with nothing selected. | `CopyButton`'s fallback writes the text through the `clipboardData` of the `copy` event that `execCommand("copy")` fires, so it needs no focus or selection. It reports a refusal instead of claiming Copied. | `comment-deep-link.spec.js` "Copy link copies the comment's address without the Clipboard API" (fails before: `[""]`); `CopyButton.test.jsx` (fallback writes the text; a refused copy does not say Copied) |
| F2 | The linked comment was scrolled to and highlighted, but focus stayed where it was, so a screen reader announced nothing and Tab carried on from elsewhere. | The comment takes focus (`tabindex="-1"`, `preventScroll`). The highlight, not an outline, marks it. Tab then goes to its own Copy link. | spec "the linked comment takes focus…" (fails before); `CommentDeepLink.test.jsx` |
| F3 | A `#comment=` naming no comment was silently ignored. Also, a successful jump left an earlier link's notice on screen. | "This link's #comment=abc does not name a comment." (`malformedCommentHash`), and a successful jump clears any notice. | same spec; `CommentDeepLink.test.jsx`; `commentLinks.test.js` |
| F4 | The 18% highlight tint dropped dark-theme body text to 3.96:1 and the light-theme timestamp to 4.37:1. | Tint is now 8%. The 2px accent ring (≥4.9:1) still marks the comment. | spec "a highlighted comment's text keeps 4.5:1 contrast in light and dark" (fails before: 3.95) |

I also promoted a Back/Forward case into `comment-deep-link.spec.js` ("Back and Forward retrace a comment link followed in place"). It passes before and after, as a guard.

**Not changed:** the notice Snackbar's 6 s auto-hide timer is not restarted when a second notice replaces an open one, so the second may show only briefly. This is MUI's behaviour and minor.

## Screenshots

`make ui-screenshots PAGES="/tasks/4#comment=1,/tasks/4#comment=abc"` before (`5eeb9b92` + the build fix) and after, at desktop 1440 and phone 390 in light and dark.

Pixel diffs between before and after are confined to the task list's relative timestamps, plus the new info notice fading in at the bottom centre of the phone `#comment=abc` shots. The highlight itself fades within the screenshot script's settle time. So I also shot it at its peak (250 ms) at 1440×900 and 390×844 in both themes, and sampled the shots: the ring colour (`rgb(133,102,41)` light, `rgb(217,164,65)` dark) is on the comment's edges in all four.

No Gemini key is available here (`LOOK=1`), and the file reader shows PNGs as bytes. So these checks are pixel measurements, not a person looking.

## Commands run

- `cd ui && npx vitest run`: 253 files, 3653 tests passed.
- `npx playwright test e2e/comment-deep-link.spec.js --project=chromium`: 10 passed.
- `go test ./pkg/model -run 'Path|Link'` and `go test ./pkg/ui -run 'Inbox|Approv|DeclaredPush'`: ok.
- `make fmt-frontend`: clean.
