# Hand run: what one keystroke in a task's chat costs

An operator reported on 2026-09-17 that "typing is very very slow" in a
long-running interactive task's chat (grain/task-174). Nothing is sent to
the daemon between one character and the next -- a comment goes out on
Enter or on Send and at no other moment -- so whatever the delay was, it
was on the main thread, and the report said it showed up on an old
conversation rather than a fresh one. That is the shape of a cost that
grows with the transcript.

This is that cost measured before and after the fix, the way
`2026-09-07-panel-dashboard.md` measured the poll's: Chromium's own
`Performance.getMetrics` over CDP, against a production build. The
harness is in `2026-09-17-chat-keystroke/` and reproduces all of it.

## What was measured, and on what

`ui/src/components/TaskChat.jsx` mounted on a synthetic conversation of
N messages, in headless Chromium at 1440x900 with no CPU throttling, on
a four-vCPU grain sandbox with 7.9 GB of memory. Every other message is
a ~2.2 KB agent answer -- headings, a fenced diff, a table, backticked
paths, a list, a blockquote, which is what the relayed answers on this
deployment's interactive tasks actually look like -- and the ones
between them are a human's one-liners.

Two gestures, twenty of the first and ten of the second per run:

* **a keystroke**: one character typed into the composer, a frame apart,
  so the render and the paint it forces are inside the window being
  measured.
* **a poll**: a fresh task object handed down with a changed `activity`,
  which is what `App.jsx` does every `POLL_INTERVAL_MS` on a task whose
  live activity moved -- the one tick `poll.js`'s `keepIfSame` cannot
  bail out of.

Three runs of each, before and after. The tables give the median; the
spread across runs was about +-10% and is quoted where it matters.

**This is not the operator's machine.** It is a headless browser on an
idle four-core sandbox. The report that started this came off a host
that was swapping 12 GB at the time (task 173), where every number below
is worse and the stall is longer than the arithmetic suggests.

## Before: 112ms of script between one character and the next

Script time per keystroke, milliseconds:

| Messages in the chat | Before | After |
| --- | --- | --- |
| 6 | 24.7 | 3.8 |
| 21 | 62.0 | 3.4 |
| 41 | 112.0 | 3.2 |
| 81 | 186.5 | 2.8 |

The before column is the bug: **2.7ms of script per message in the
conversation, paid per character.** A working Director session after an
hour is a few dozen long answers, which is the 41-message row -- 112ms
between pressing a key and the character appearing, and the transcript
only ever grows. The 81-message row is the same session after two hours.

Wall clock per keystroke tracked it at a fixed distance (the harness
waits a frame per character, so ~17ms of the wall figure is the frame,
not the work): 40ms at 6 messages, 132-150ms at 41, 200-204ms at 81.
Nothing was fetched, nothing was parsed off the wire, and no state
outside the composer had changed.

Where it went: `MessageInput` is controlled by `draft`, `draft` is state
on `TaskChat`, and `TaskChat`'s render body *was* the transcript --
`chatEntries(t)` with its sort, then a `<Message>` per entry, each one a
`<Markdown>`, which is `react-markdown` with `remarkGfm` and
`remarkBreaks`. No `memo` anywhere on that path. So one character
re-parsed every message in the conversation, remark and rehype, from
source text to elements.

After the change the same measurement is **flat in N**: 2.8-3.8ms
whatever the length of the conversation, which is the composer's own
re-render and nothing else. At 41 messages that is 35x less script per
character; at 81, 66x. The flatness is the part that matters -- the cost
no longer has the conversation in it at all.

## The fix, and why this one

Two changes, in `ui/src/components/Markdown.jsx` and
`ui/src/components/TaskChat.jsx`:

1. **`Markdown` is `memo`'d.** Its props are a body string and a
   className, so the comparison is two `===` against a remark/rehype
   pass over kilobytes of prose. Worth it on its own merits and
   everywhere, not only here: the task timeline in `DetailOverlay`
   renders many of these at once under an overlay that polls every three
   seconds, and `InboxPage` does the same.
2. **The transcript is one memoised element** (`useMemo`), keyed on the
   entries, the time zone, the task id and the typing indicator's two
   values -- not on the draft. When the element React is handed is the
   same props object it had last time, React bails out of that whole
   subtree, so a keystroke re-renders the composer and nothing below it.
   `chatEntries(t)` is inside that memo and so is computed once per
   change of the task rather than once per render.

**The other route was tried first and does not work.** The task proposed
moving the draft state down into a small child rendering only
`MessageInput`, so `setDraft` could not reach the list at all. That is
the better shape in the abstract and this library forbids it:
`ChatContainer` picks its children *by component type* --
`getChildren(children, [ConversationHeader, MessageList, MessageInput,
InputToolbox])` in `@chatscope/chat-ui-kit-react`'s own `utils.js` --
and silently drops anything that is not one of the four. A `<Composer>`
wrapping a `MessageInput` renders no composer at all; it renders
nothing. The kit does have an `as` escape hatch that would smuggle one
through, and it would still leave the draft out of reach of the two
controls that need it: the Send button and `AttachmentPicker` sit
outside `.task-chat-window`, in a different parent, and the Send button
is disabled from `draft.text`. So: memoise, and say in the component why
the tidier-looking option is not available.

## The poll is a separate fault, and it is still there

The second gesture, script time per poll tick that changes the task's
activity:

| Messages in the chat | Before | After |
| --- | --- | --- |
| 6 | 24.6 | 5.4 |
| 21 | 56.9 | 10.1 |
| 41 | 108.1 | 12.2 |
| 81 | 161.0 | 22.6 |

8-9x better, because the `Markdown` memo bails the parse out of it: ~88%
of what a tick used to cost was re-parsing bodies that had not changed.
But it is **still linear in the conversation** -- 0.28ms per message
against 2.6ms before -- and that is the residue the memoised transcript
cannot touch: when `activity` moves, `t` is a new object, so
`chatEntries(t)` recomputes, the entries are new objects, the memo
misses by design, and every `Message`, `Message.Header` (an
`Intl.DateTimeFormat` each) and `AttachmentLinks` in the transcript
re-renders even though not one of them has changed.

It lands every three seconds on any task an agent is working in, which
is exactly the case the operator reported from, so it is worth fixing --
and it is a different fix (a stable per-entry identity across polls, or
narrowing what a poll hands the chat down to the conversation rather
than the whole task record) touching `poll.js` and `chat.js` rather than
the composer. Filed separately rather than widened into this one, as the
task asked.

## Regression coverage

Two files, both of which fail on the parent commit:

* `ui/src/components/TaskChat.renders.test.jsx` mocks `Markdown.jsx` to
  count every render of a message body. Without the fix, typing four
  characters into a 9-message chat renders 36 bodies and one character
  into a 41-message chat renders 41; with it, both are zero. The second
  test is the one that pins the property that actually broke -- the count
  per keystroke is *the same* on a 41-message conversation as on a
  3-message one -- rather than pinning a constant that would go stale.
* `ui/src/components/Markdown.memo.test.jsx` mocks `react-markdown` and
  counts parses: a parent re-rendering around an unchanged body parses
  nothing again, a changed body does.

A stopwatch assertion was deliberately not written. What broke here was
not a number, it was the number's dependence on N, and a millisecond
threshold in a jsdom test on a CI sandbox is the kind of red that says
"this branch broke something" about a branch that touched no JavaScript.

## Still owed

1. **A real conversation from this deployment's own store.** The bodies
   here are one synthetic answer repeated, at a length chosen to be
   typical. A transcript with a 40 KB answer in it, or one with a dozen
   fenced blocks, would cost more per message than 2.7ms -- the shape of
   the curve is what was measured, not its worst constant.
2. **A real machine under memory pressure.** Everything above is an idle
   sandbox. The host this was reported from was swapping, and what a
   200ms main-thread stall does to a browser that is also waiting on
   pages is not something a headless run on an idle box can say.
3. **The rest of the chat's own render path.** `Message.Header` formats
   a timestamp through `Intl.DateTimeFormat` per message per render;
   nothing above needed that fixed, and the poll work in the section
   above is where it will show up.
