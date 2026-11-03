# TaskChat optimistic and back-to-back sends, and the `priorityDispatch` gate — 2026-11-01

Plan: [`docs/test-plans/2026-11-01-late.md`](../test-plans/2026-11-01-late.md) §3 (PR #289, `74e6396d` and `1116754e`). Task 2245, filed by task 2227.

## Environment

- Tree: `origin/main` at `85b2724c`, branch `grain/049f1e09/task-2245`.
- Server: **`ui/e2e/consolerestserver`**: the real store, UI server and orchestrator cycle, with a scripted agent provider. There was no real agent backend.
  - Built with `make frontend && go build ./ui/e2e/consolerestserver`.
  - Run with `GRAIN_E2E_ACCESS_CODE=<code>`.
  - Playwright starts its own copy through `console-rest-fixtures.js`.
  - `/e2e/interactive-control` `holdNext` / `releaseRun` holds a session's run in Running, so no scripted reply lands mid-assertion.
- Browser: Playwright 1.6x Chromium, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- Slow and failing POSTs: Playwright `page.route` on `**/api/tasks/<id>/comments`. Each POST waits for the test to release it, and the first N answer `500 {"error":"walkthrough: injected failure"}`.
- Sandbox note: `make frontend` prunes `ui/node_modules` down to production dependencies, which removes `@playwright/test`. Run `npm ci` again in `ui/` after every `make frontend`, before running `npx playwright`.

## Pages, widths and themes shot

- **`make ui-screenshots PAGES=/session/shot`**, with `URL`/`CODE` pointed at a running consolerestserver. The session holds three messages, the third long enough to wrap at 390 px.
  - Widths: desktop (1440×900) and phone (390×844), each in light and dark.
  - Taken as `OUT=screenshots/before` on the base and `OUT=screenshots/after` with the fix.
  - These are settled conversations, so before and after differ only in timestamps and session content (`compare -metric AE`: 6280 px desktop-light, 581 px phone-dark). The fix changes only messages that are in flight or failed.
- **The spec's own shots** of the states `make ui-screenshots` cannot hold, in `test-results/`:
  - `pending-{desktop,phone}-{light,dark}.png`: two bubbles in flight;
  - `failed-retry.png`: one bubble marked "Not sent" with Retry and Discard.
- The sandbox cannot show PNGs to the agent: the file reader returns bytes, and there is no Gemini key for `LOOK=1`. **Layout was therefore checked with bounding boxes**, in `ui/e2e/task-chat-optimistic-sends.spec.js`, at each width and theme:
  - the transcript ends at or above the composer's top edge;
  - the newest bubble sits inside the transcript, above the composer;
  - the composer and bubbles fit inside the viewport width;
  - bubble tops strictly increase (order);
  - the first bubble's x and width move by at most 1–2 px when the server's copy replaces the optimistic one (no layout jump).

## "What is NOT proved", item by item

| # | Item | Result |
|---|---|---|
| 1 | Optimistic and queued bubbles at 1440 and 390 px, light and dark | **Finding F1, fixed.** Layout passes at all four (bounding boxes as above), with no duplicate once the server copies land. But an in-flight bubble looked exactly like a sent one. |
| 2a | Slow POST | **F1, fixed.** The bubble read as sent while the POST was still held, for as long as it was held. |
| 2b | Failing first POST, second queued behind it | Passes on base and after. The first goes back in the box and the error banner shows the server's message. The second posts, and the store holds only it. |
| 2c | Both queued POSTs fail | **Finding F2, fixed.** The second message was lost. |
| 3 | Burst of four, the second with an attachment | Passes. Order is kept in the UI and in `GET /api/tasks/<id>/messages`, and the attachment link renders. |
| 4 | Two tabs on one session, one posting while the other polls | Passes. Every transcript the polling tab drew was a prefix-extension of the one before, so nothing regressed to an older list. A reply from tab B appears in tab A after A's own messages. |
| 5 | Keyboard and focus | Passes. Enter sends, Shift+Enter keeps a line break (stored as `"line one\nline two"`), and focus stays in the composer after each send. The composer is `role=textbox`, `aria-label="Message"`. The sending state was not announced (**F1**). |
| 6 | `priorityDispatch` gate, live | Passes, with one difference from the plan's wording (below). |

### F1: an in-flight message read as sent, and was never announced

`TaskChat` appended the optimistic comment as an ordinary outgoing bubble, with the send time in its header. Nothing distinguished it from a message the server had accepted. Under a held POST it stayed like that for the whole delay, and screen readers were told nothing.

Reproduction (base, fails): the `optimistic and queued bubbles …` and `a slow POST …` cases in the new spec, or Vitest `marks a message in flight as sending …`.

**Fix.** A pending bubble is drawn as sending until its POST resolves:
- it carries the class `task-chat-pending task-chat-sending` and `aria-busy="true"`;
- its header says "Sending…" in place of the time;
- it is faded to 0.65 opacity.

A visually hidden `role="status"` `aria-live="polite"` region says `Sending N message(s)…` while any are in flight, and says when one failed.

### F2: when both queued sends failed, the second vanished

A failed message goes back in the box only if the box is empty (grain/task-1360). When the first and second both failed:
1. the first went back into the empty box;
2. the second found the box taken, so it was neither restored nor left in the transcript, and was gone.

Repro (base, fails): `when both queued sends fail, neither message is lost`. The box held only `Lost A`.

**Fix.** A failed message whose box is taken stays in the transcript, marked `task-chat-failed`, with the header "Not sent", a `--danger` outline, and **Retry** and **Discard** buttons. The live region says "Message not sent. Retry it from the conversation." Retry posts it again through the same ordered send chain. A failed bubble is never matched against server comments, so it cannot vanish when an older comment has the same body. The single-failure behaviour is unchanged: the message goes back in the box.

### Item 6: `PATCH /api/tasks/{id}` with agent tokens (curl)

Setup:
- Acting tasks were created with these grants: `2` holds `api:tasks:admin`; `5` holds `api:tasks:write` and `api:tasks:admin`; `6` holds those two plus `api:capabilities:admin`.
- Every call sent `X-Grain-Acting-Task: <id>` and the bearer access code, as an agent's sandbox does.
- The target was task `4`.

| Acting task | Body | Answer |
|---|---|---|
| 2 (tasks:admin) | `{"priorityDispatch":true}` | 403 `api:tasks:admin without api:tasks:write may only edit priority … (got "priorityDispatch")` |
| 2 | `{"priority_dispatch":true}` | 403, the same, `(got "priority_dispatch")` |
| 2 | `{"priority":true}` | 403 `… may only edit numeric priority …` |
| 2 | `{"priority":3}` | **200**, priority 3 |
| 5 (write+admin) | `{"priorityDispatch":true}` / `{"priority_dispatch":true}` / `{"priority":true}` | 403 `task 5 holds no api grant for scope capabilities:admin, which PATCH /api/tasks/{id} needs when editing task capabilities` (each) |
| 5 | `{"priority":2}` | **200** |
| 6 (+capabilities:admin), no reason | all three spellings | 403 `PATCH /api/tasks/{id} with capabilities is destructive (scope capabilities:admin): give a reason` (each) |
| 6, with `X-Grain-Acting-Reason` | `{"priorityDispatch":true}` | 403 `priority-dispatch lets a task jump the queue: only a person may grant it` |

Notes:
- The target's `priorityDispatch` stayed `null` throughout. Its priority was put back to 5 afterwards.
- **Difference from the plan's wording.** A token holding only `api:tasks:admin` is refused all three toggles, but by the earlier `tasks:admin`-only rule, so the message does not name `capabilities:admin`. The message names `capabilities:admin` once the token also holds `api:tasks:write`. Both refuse correctly. Nothing to change.

## Tests added

- `ui/e2e/task-chat-optimistic-sends.spec.js` (Chromium, consolerestserver) has 10 cases:
  - the bubble layout at 1440 and 390 px in light and dark;
  - a slow POST;
  - a failed first POST;
  - both POSTs failing, then Retry;
  - a burst with an attachment;
  - two tabs;
  - the keyboard.

  Each case deletes the session it made, unroutes, and releases any held run in `finally`.
- `ui/src/components/TaskChat.test.jsx` gains two Vitest regressions, one for F1 and one for F2. Both fail on base and pass with the fix.

## Runs

- `npm test` (Vitest): 230 files and 3603 tests passed.
- `npx playwright test e2e/task-chat-optimistic-sends.spec.js e2e/interactive-session-chat.spec.js --project=chromium`: 11 passed.
  - On base, before the fix: 6 of the new cases failed (the 4 layout cases, the slow POST, and both-fail) and 4 passed.
