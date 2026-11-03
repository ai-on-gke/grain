// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import LocalScriptApprovals from "./detail/LocalScriptApprovals.jsx";
import PendingConfigChange from "./detail/PendingConfigChange.jsx";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Box, Button, Typography } from "@mui/material";
import {
  ChatContainer,
  MainContainer,
  Message,
  MessageInput,
  MessageList,
  MessageSeparator,
  TypingIndicator,
} from "@chatscope/chat-ui-kit-react";
// The kit's own stylesheet, with its palette rewritten onto this
// shell's theme tokens as Vite loads it -- vite.config.js's
// chatscopeTheme plugin, src/chatscopeTheme.js's table, and the
// --cs-* block in style.css that says what those tokens are. The
// unminified sheet rather than styles.min.css: the rewrite is a
// colour-for-colour substitution that wants to be readable in a
// browser's devtools, and Vite minifies the result for the build
// anyway.
import "@chatscope/chat-ui-kit-styles/dist/default/styles.css";
import api from "../api.js";
import fileToAttachment from "../attachments.js";
import { awaitsReply, chatEntries, typingNote, working } from "../chat.js";
import { nameScrollbars } from "../chatScrollbars.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { isTaskPaused } from "../state.js";
import AttachmentLinks from "./AttachmentLinks.jsx";
import AttachmentPicker, {
  getPastedImages,
  getDroppedFiles,
} from "./AttachmentPicker.jsx";
import CopyButton from "./CopyButton.jsx";
import Markdown from "./Markdown.jsx";

// TaskChat is an interactive task's conversation as a chat window
// (grain/task-414): the messages in bubbles, oldest at the top, a
// composer pinned under them, and an indicator while an agent is working
// on the other end.
//
// # Why this, and not a terminal
//
// The other way to sit in front of an agent runs the framework's own
// CLI on a pseudo-terminal (pkg/console). That buys the CLI's whole
// surface -- its slash
// commands, its own approval prompts -- at the price of a lot of
// machinery that exists only for it: a PTY per session, a session manager
// in the daemon, five routes of its own, a byte ring with an offset, a
// second command line per framework with its headless flags stripped out,
// and a captured surface per binary to check those flags against.
//
// A chat needs none of it, because grain already carries a conversation
// in both directions. A comment posted while a run is in flight reaches
// the agent mid-run (orchestrator's addendaPoller); what the agent says
// back is posted as a comment on the same task. This component is that
// exchange drawn as what it is. Nothing about the run is special: same
// dispatch, same sandbox, same tool roster, same ending -- so there is no
// second way to run an agent to keep working, and a chat is legible on a
// phone, where a full-screen TUI is not.
//
// What it gives up is the CLI's own interface: a slash command typed here
// is text in a message, not a command, and an agent that wants a decision
// asks for one in words. That is the trade this pane is, and it is the
// reason the two can coexist -- a task that really needs the CLI still
// holds the `console` capability and gets the terminal.
//
// # Why a library
//
// @chatscope/chat-ui-kit-react (MIT) is the transcript, the bubbles,
// their grouping and the composer. A chat window is a solved shape with a
// dozen details in it nobody should re-derive -- keeping the view pinned
// to the newest message unless the reader has scrolled up, growing the
// input with what is typed, Enter to send and Shift+Enter for a newline,
// and the direction/position logic that makes a run of messages from one
// side read as one run. It is the same judgement the terminal pane made
// in taking xterm.js rather than parsing escape sequences by hand.
//
// What stays this repository's own is what is grain's rather than the
// chat's: the message bodies are Markdown.jsx, the same renderer every
// other relayed answer goes through, and attachments are
// AttachmentPicker/AttachmentLinks, since a file on a message here is a
// task attachment (bwsalmon/agents#522) rather than a chat upload -- the
// library's own attach button is off for exactly that reason.
export default function TaskChat({
  t,
  act,
  onStop = null,
  // onSend, when given, takes the message instead of the task's own
  // comments endpoint: ({ body, attachments }) => posted, where a
  // false return is a message that did not land and goes back in the
  // box. It is how a chat that has no task yet (NewSessionPage.jsx)
  // uses this same window: the first message is what creates the
  // task, so there is nothing to post it to until it has been sent.
  onSend = null,
  placeholder = "Message…",
  draftRequest = null,
  onDraftConsumed,
  actions = null,
  children = null,
}) {
  const zone = useTimeZone();
  // Keyed on `t` itself, which is exactly as stable as the conversation
  // is: App polls this task every three seconds and writes the result
  // through keepIfSame (poll.js), so an unchanged task keeps its
  // identity and this list -- its sort included -- is computed once per
  // actual change rather than once per render. See the transcript memo
  // below for what the render it is inside otherwise costs.
  const serverEntries = useMemo(() => chatEntries(t), [t]);
  // Optimistic outgoing comments currently in flight or awaiting the next
  // task snapshot that includes them, so a comment submitted while the
  // task transitions between running, awaiting_reply, and queued is never
  // hidden between the POST and the poll that catches up.
  const [pendingComments, setPendingComments] = useState([]);
  const pendingSeq = useRef(0);
  const sendChain = useRef(Promise.resolve());
  useEffect(() => {
    setPendingComments([]);
  }, [t.id]);
  useEffect(() => {
    if (pendingComments.length === 0) return;
    const serverComments = t.comments || [];
    const consumed = new Set();
    const remaining = pendingComments.filter((p) => {
      if (p.failed) return true;
      const idx = serverComments.findIndex(
        (c, i) =>
          !consumed.has(i) &&
          (c.authorKind === "human" || !c.authorKind) &&
          (typeof c.id === "number"
            ? c.id > p.afterMaxId
            : i >= p.afterCommentLen) &&
          (c.body || "") === (p.body || ""),
      );
      if (idx !== -1) {
        consumed.add(idx);
        return false;
      }
      return true;
    });
    if (remaining.length !== pendingComments.length) {
      setPendingComments(remaining);
    }
  }, [t.comments, pendingComments]);
  const entries = useMemo(() => {
    if (pendingComments.length === 0) return serverEntries;
    const serverComments = t.comments || [];
    const consumed = new Set();
    const unpersisted = pendingComments.filter((p) => {
      if (p.failed) return true;
      const idx = serverComments.findIndex(
        (c, i) =>
          !consumed.has(i) &&
          (c.authorKind === "human" || !c.authorKind) &&
          (typeof c.id === "number"
            ? c.id > p.afterMaxId
            : i >= p.afterCommentLen) &&
          (c.body || "") === (p.body || ""),
      );
      if (idx !== -1) {
        consumed.add(idx);
        return false;
      }
      return true;
    });
    return unpersisted.length === 0
      ? serverEntries
      : [
          ...serverEntries,
          ...unpersisted.map((p) => ({
            ...p,
            pending: p.failed ? "failed" : p.sent ? undefined : "sending",
          })),
        ];
  }, [serverEntries, pendingComments, t.comments]);
  const busy = working(t);
  const note = typingNote(t);
  const waiting = awaitsReply(t);
  // attachments is File objects picked but not yet sent, and draft is
  // what has been typed. Both are held here rather than in the library's
  // own input, because sending is two things at once -- upload the files,
  // post the comment -- and a poll landing mid-message must not lose
  // either (AttachmentPicker's own doc comment on why the files are read
  // only at send).
  const [attachments, setAttachmentsState] = useState([]);
  const attachmentsRef = useRef([]);
  const setAttachments = (next) => {
    const resolved =
      typeof next === "function" ? next(attachmentsRef.current) : next;
    attachmentsRef.current = resolved;
    setAttachmentsState(resolved);
  };
  // Two spellings of the same draft, because the box and the API want
  // different ones. `html` is what the library's contenteditable is
  // controlled by -- give it plain text and a message typed over several
  // lines (Shift+Enter) loses its line breaks the moment it is echoed
  // back. `text` is what gets posted, and comes from the element's
  // innerText rather than its textContent for the same reason in reverse:
  // textContent of "a<div>b</div>" is "ab", with the break the person
  // typed silently gone.
  const [draft, setDraftState] = useState(() => ({
    html: draftRequest?.text || "",
    text: draftRequest?.text || "",
  }));
  const draftRef = useRef({
    html: draftRequest?.text || "",
    text: draftRequest?.text || "",
  });
  const setDraft = (next) => {
    const resolved = typeof next === "function" ? next(draftRef.current) : next;
    draftRef.current = resolved;
    setDraftState(resolved);
  };
  // The library's contenteditable takes no props of its own -- MessageInput
  // spreads what it is given onto the wrapper around it, not onto the
  // editor -- so the box a screen reader lands on had no role and no
  // name: a bare editable <div>, whose placeholder is a CSS ::before and
  // announces nothing. Named here, after every render, because the
  // library can replace that element and React never manages these
  // attributes on it.
  const windowRef = useRef(null);
  const windowId = useId();
  useEffect(() => {
    const editor = windowRef.current?.querySelector(
      ".cs-message-input__content-editor",
    );
    if (!editor || editor.getAttribute("role") === "textbox") return;
    editor.setAttribute("role", "textbox");
    editor.setAttribute("aria-multiline", "true");
    editor.setAttribute("aria-label", "Message");
  });
  // The scrollbar thumbs are tab stops of the library's own, and named
  // here for the same reason as the editor above (chatScrollbars.js).
  useEffect(() => {
    nameScrollbars(windowRef.current, "Scroll messages");
  });
  useEffect(() => {
    const root = windowRef.current;
    if (!root) return undefined;
    // Capture, since scroll does not bubble: one listener sees the
    // transcript's and the composer's alike.
    const onScroll = () => nameScrollbars(root, "Scroll messages");
    root.addEventListener("scroll", onScroll, true);
    return () => root.removeEventListener("scroll", onScroll, true);
  }, []);
  const stopping = useRef(false);
  // What the sending state last came to, for a screen reader: the
  // bubbles say "Sending…" and "Not sent" in their headers, but nothing
  // announces a header changing, so the status region below says it.
  const [sendNotice, setSendNotice] = useState("");
  const sending = pendingComments.filter((p) => !p.failed && !p.sent).length;
  // A failed bubble's Retry and Discard, reached through a ref so the
  // memoised transcript below need not be rebuilt for a new closure on
  // every keystroke, and still calls this render's (current t, current
  // pending list) rather than the one that drew it.
  const bubbleActions = useRef({});

  // A shortcut elsewhere in the shell can bring somebody here with words
  // ready to edit. Apply each request once by identity; task polling must not
  // reset a draft already being changed in this composer.
  useEffect(() => {
    if (!draftRequest) return;
    setDraft({ html: draftRequest.text, text: draftRequest.text });
  }, [draftRequest]);

  // The transcript, held as one memoised element rather than built in
  // the render body below (grain/task-174).
  //
  // # What it is for
  //
  // MessageInput is controlled by `draft`, and `draft` is state on this
  // component, so every character typed re-renders the whole of this
  // function -- and the whole of this function used to be the
  // conversation: chatEntries(t) with its sort, then a <Message> per
  // entry, each one a <Markdown> and so a remark/rehype parse of a body
  // that is often kilobytes long. On a chat with a few dozen agent
  // answers in it, that was 112ms of script *between one character and
  // the next*, growing linearly with the conversation, which is what an
  // operator reported as "typing is very very slow"
  // (the 2026-09-17 chat-keystroke hand run has the numbers, before
  // and after). Nothing is sent to the daemon until Enter, so all of it
  // was main-thread work for a keystroke that changed one contenteditable.
  //
  // A memoised element rather than a memo'd child component is what
  // React can actually skip here: when the element handed to a fiber is
  // the same props object it had last time, React bails out of that
  // whole subtree -- so on a keystroke the list is not re-rendered, not
  // re-diffed and not re-parsed, and the only work left is the composer
  // that the keystroke was about.
  //
  // # Why not move `draft` into a child instead
  //
  // The obvious fix -- a small Composer component owning the draft, so
  // setDraft cannot reach the list at all -- is the one this library
  // forbids. ChatContainer picks its children *by component type*
  // (getChildren(children, [ConversationHeader, MessageList,
  // MessageInput, InputToolbox]) in chat-ui-kit-react's own utils.js):
  // anything that is not one of those four is silently dropped, so
  // <Composer/> wrapping a MessageInput does not render a composer at
  // all, it renders nothing. The kit does have an `as` escape hatch for
  // it, and it would still leave the draft unreachable by the two
  // controls that need it -- the Send button and AttachmentPicker sit
  // outside .task-chat-window, in a different parent, and the Send
  // button is disabled from draft.text. Memoising is the change that
  // does not fight the library's shape.
  //
  // The deps are everything drawn below: the entries, the zone their
  // timestamps are formatted in, the task id the attachment links are
  // built from, and the typing indicator's own two values. Not `draft`,
  // which is the point, and not `t`, whose every other field (activity
  // included) reaches this list only through those.
  const transcript = useMemo(
    () => (
      <MessageList
        typingIndicator={busy ? <TypingIndicator content={note} /> : null}
      >
        {entries.length === 0 && !busy && (
          <MessageSeparator content="No messages yet" />
        )}
        {entries.map((e) =>
          e.kind === "note" ? (
            <MessageSeparator
              key={e.key}
              // A run that ended without an answer, said in the one
              // line that explains the silence after it.
              content={e.detail ? `${e.text} · ${e.detail}` : `${e.text}`}
            />
          ) : (
            <Message
              key={e.key}
              className={
                e.pending
                  ? `task-chat-pending task-chat-${e.pending}`
                  : undefined
              }
              aria-busy={e.pending === "sending" ? "true" : undefined}
              model={{
                direction: e.direction,
                // "single" rather than the library's own grouping:
                // every message here carries its own header, since
                // who is speaking changes between a person, an
                // agent's relayed words and grain's own, and the
                // header is where that is said.
                position: "single",
                sender: e.sender,
              }}
            >
              <Message.Header
                sender={e.sender}
                sentTime={
                  e.pending === "sending"
                    ? "Sending…"
                    : e.pending === "failed"
                      ? "Not sent"
                      : e.at
                        ? formatDateTime(e.at, zone)
                        : ""
                }
              />
              <Message.CustomContent>
                <div className="task-chat-message-content">
                  <Markdown className="task-chat-body">{e.body}</Markdown>
                  {e.direction === "incoming" && e.body && (
                    <div className="task-chat-actions">
                      <CopyButton text={e.body} label="Copy response" />
                    </div>
                  )}
                </div>
                <AttachmentLinks taskId={t.id} attachments={e.attachments} />
                {e.pending === "failed" && (
                  <div className="task-chat-retry">
                    <Button
                      size="small"
                      onClick={() => bubbleActions.current.retry(e.key)}
                    >
                      Retry
                    </Button>
                    <Button
                      size="small"
                      onClick={() => bubbleActions.current.discard(e.key)}
                    >
                      Discard
                    </Button>
                  </div>
                )}
              </Message.CustomContent>
            </Message>
          ),
        )}
      </MessageList>
    ),
    [entries, busy, note, zone, t.id],
  );

  const send = async () => {
    const sent = draftRef.current;
    const files = attachmentsRef.current;
    if (!sent.text.trim() && files.length === 0) return;
    // Cleared synchronously before the first await so double-clicks on the
    // same draft are ignored while a second message typed while this one is
    // still in flight can be sent immediately without being dropped.
    setDraft({ html: "", text: "" });
    setAttachments([]);
    await post(sent, files);
  };

  // post sends one message, queued behind any still in flight so they
  // land in the order they were typed.
  const post = async (sent, files) => {
    const body = sent.text;
    const serverComments = t.comments || [];
    const afterCommentLen = serverComments.length;
    const afterMaxId = serverComments.reduce(
      (m, c) => (typeof c.id === "number" && c.id > m ? c.id : m),
      0,
    );
    const optimisticKey = `optimistic-${++pendingSeq.current}`;
    if (!onSend && body.trim()) {
      setPendingComments((cur) => [
        ...cur,
        {
          key: optimisticKey,
          kind: "message",
          at: new Date(),
          direction: "outgoing",
          sender: t.creator || t.author || "human",
          body,
          attachments: [],
          afterCommentLen,
          afterMaxId,
          sent: false,
          failed: false,
        },
      ]);
      setSendNotice("");
    }
    // A message that did not land goes back in the box rather than being
    // lost with the banner (grain/task-1360). If the box holds something
    // already -- typed since, or an earlier queued message that failed
    // and went back first -- it stays in the transcript marked "Not
    // sent", with Retry, rather than being dropped (2026-11-01 §3).
    const unsent = () => {
      if (
        !draftRef.current.text.trim() &&
        attachmentsRef.current.length === 0
      ) {
        setPendingComments((cur) => cur.filter((p) => p.key !== optimisticKey));
        setDraft(sent);
        setAttachments(files);
        setSendNotice("Message not sent. It is back in the message box.");
        return;
      }
      setPendingComments((cur) => {
        if (cur.some((p) => p.key === optimisticKey)) {
          return cur.map((p) =>
            p.key === optimisticKey ? { ...p, failed: true, sent, files } : p,
          );
        }
        return [
          ...cur,
          {
            key: optimisticKey,
            kind: "message",
            at: new Date(),
            direction: "outgoing",
            sender: t.creator || t.author || "human",
            body: body.trim() ? body : files.map((f) => f.name).join(", "),
            attachments: [],
            afterCommentLen,
            afterMaxId,
            failed: true,
            sent,
            files,
          },
        ];
      });
      setSendNotice("Message not sent. Retry it from the conversation.");
    };
    const task = sendChain.current.then(async () => {
      try {
        const uploaded = await Promise.all(
          files.map((f) => fileToAttachment(f)),
        );
        let posted = false;
        if (onSend) {
          posted = (await onSend({ body, attachments: uploaded })) !== false;
        } else {
          await act(async () => {
            await api(`/api/tasks/${t.id}/comments`, {
              method: "POST",
              body: JSON.stringify({
                body,
                attachments: uploaded,
              }),
            });
            posted = true;
          }, t.id);
        }
        // act() reports a failure and swallows it, so whether the message
        // landed is only known from here.
        if (!posted) {
          if (onSend) {
            setDraft((cur) => (cur.text.trim() ? cur : sent));
            setAttachments((cur) => (cur.length ? cur : files));
          } else {
            unsent();
          }
        } else {
          setPendingComments((cur) =>
            cur.map((p) =>
              p.key === optimisticKey ? { ...p, sent: true } : p,
            ),
          );
        }
        onDraftConsumed?.(draftRequest);
      } catch (err) {
        if (onSend) {
          setDraft((cur) => (cur.text.trim() ? cur : sent));
          setAttachments((cur) => (cur.length ? cur : files));
        } else {
          unsent();
        }
        throw err;
      }
    });
    sendChain.current = task.catch(() => {});
    await task;
  };

  const retry = (key) => {
    const failed = pendingComments.find((p) => p.key === key);
    if (!failed) return;
    setPendingComments((cur) => cur.filter((p) => p.key !== key));
    post(failed.sent, failed.files || []).catch(() => {});
  };

  const discard = (key) => {
    setPendingComments((cur) => cur.filter((p) => p.key !== key));
    setSendNotice("");
  };
  bubbleActions.current = { retry, discard };

  const stop = async () => {
    if (stopping.current) return;
    stopping.current = true;
    try {
      if (onStop) {
        await onStop();
        return;
      }
      await act(
        () =>
          api(`/api/tasks/${t.id}/stop`, {
            method: "POST",
          }),
        t.id,
      );
    } finally {
      stopping.current = false;
    }
  };

  const handlePaste = (evt) => {
    const files = getPastedImages(evt);
    if (files.length > 0) {
      evt.preventDefault();
      setAttachments((current) => [...current, ...files]);
    }
  };

  const handleDrop = (evt) => {
    const dropped = getDroppedFiles(evt);
    if (dropped.length > 0) {
      evt.preventDefault();
      setAttachments((current) => [...current, ...dropped]);
    }
  };

  const handleDragOver = (evt) => {
    evt.preventDefault();
  };

  return (
    <div
      className="task-chat"
      onPaste={handlePaste}
      onDrop={handleDrop}
      onDragOver={handleDragOver}
    >
      <Box
        sx={{
          display: "flex",
          alignItems: "baseline",
          justifyContent: "space-between",
          mb: 1,
        }}
      >
        <h3>Chat</h3>
        {waiting && (
          <Typography
            variant="caption"
            className="task-chat-waiting"
            role="status"
          >
            waiting for your reply
          </Typography>
        )}
      </Box>
      <span className="visually-hidden" role="status" aria-live="polite">
        {sending > 0
          ? `Sending ${sending} message${sending === 1 ? "" : "s"}…`
          : sendNotice}
      </span>
      <LocalScriptApprovals t={t} act={act} />
      <PendingConfigChange t={t} act={act} />
      <div className="task-chat-window" ref={windowRef} id={windowId}>
        <MainContainer>
          <ChatContainer>
            {transcript}
            <MessageInput
              value={draft.html}
              // innerText where there is one, and the textContent beside
              // it where there is not: jsdom implements no innerText at
              // all, so a component test would otherwise post undefined
              // for every message.
              onChange={(html, text, innerText) =>
                setDraft({ html, text: innerText ?? text })
              }
              onSend={send}
              attachButton={false}
              sendButton={false}
              placeholder={placeholder}
              // The library sends on Enter and inserts a newline on
              // Shift+Enter, which is what a chat does everywhere else;
              // the Send button below is for a touch keyboard that has no
              // convenient Enter, and for a message sent with only a file
              // on it.
              autoFocus
            />
          </ChatContainer>
        </MainContainer>
      </div>
      <div className="task-chat-compose">
        <AttachmentPicker files={attachments} onChange={setAttachments} />
        {busy && (
          <Button variant="outlined" color="error" onClick={stop}>
            Stop
          </Button>
        )}
        {/* Pause and Resume (ui.Client.Pause): beside Stop, which ends
            only this turn, because a pause is the other way to make a
            running task stop -- it asks the agent to push and hand over
            first, and keeps the task from being dispatched again until
            somebody resumes it. */}
        {/* Not on a console session (onStop: its own turn control) nor
            on a session not yet started (onSend: there is no task yet). */}
        {t?.id && t.state !== "closed" && !onStop && !onSend && (
          <Button
            variant="outlined"
            onClick={() =>
              act(
                () =>
                  api(
                    `/api/tasks/${t.id}/${isTaskPaused(t) ? "resume" : "pause"}`,
                    { method: "POST" },
                  ),
                t.id,
              )
            }
          >
            {isTaskPaused(t) ? "Resume" : "Pause"}
          </Button>
        )}
        <Button
          variant="outlined"
          onClick={send}
          disabled={!draft.text.trim() && attachments.length === 0}
        >
          Send
        </Button>
        {actions}
      </div>
      {children}
    </div>
  );
}
