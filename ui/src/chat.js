// chat.js turns one task's record into the messages a chat reads as: who
// said what, in order, oldest first (grain/task-414).
//
// There is no chat *session* on the wire to draw this from, and that is
// the whole point of doing it here. A task already carries a
// conversation -- its own body, then every comment on it, with the author
// of each -- and grain already carries both directions of it live: a
// comment posted while a run is in flight reaches the agent through
// orchestrator's addendaPoller, and what the agent says back is posted as
// a comment on the same task. So an interactive session needs no second
// transport, no terminal and no per-framework flags: it needs the
// conversation that is already there, rendered as one.
//
// What this adds to the raw arrays is only what a chat, rather than a
// history, wants:
//
//   - the task's own description is the opening message, by the person
//     who filed it. In a task's timeline that body is the heading above
//     the feed; in a chat it is the first thing said, and leaving it out
//     would start every conversation mid-sentence.
//   - a comment from a human is theirs (outgoing, on the right); anything
//     else -- an agent's relayed words, grain's own -- is the other side
//     of the conversation.
//   - an attempt is not an entry unless it ended badly. Every turn of a
//     chat is a run, so "Succeeded" between each pair of messages would
//     be one separator per message and would say nothing a reply does not
//     already say. A run that failed, was cancelled or gave up in setup
//     is the opposite: it is why no answer came, and without it the chat
//     simply goes quiet.
import { outcomeLabel } from "./state.js";

// chatEntries is the ordered list TaskChat renders, from one TaskDetail.
//
// Two kinds, told apart by `kind`: a "message" somebody said, and a
// "note" about the run behind it. Both carry `at` (a Date, or null when
// the record has no timestamp for it) and a `key` stable across polls, so
// a re-render of a growing conversation reuses the DOM it already has.
export function chatEntries(t) {
  const entries = [];

  if (t.description) {
    entries.push({
      key: "description",
      kind: "message",
      at: t.createdAt ? new Date(t.createdAt) : null,
      direction: t.authorKind === "human" ? "outgoing" : "incoming",
      sender: t.creator || t.author || "unknown",
      body: t.description,
      attachments: t.attachments || [],
    });
  }

  (t.comments || []).forEach((c, i) => {
    // Synthetic resting comments between console session exchanges
    // are not conversation messages and TaskChat already shows a persistent
    // header indicator ("waiting for your reply") when awaiting reply.
    // Only grain's own line (model.ConsoleSessionPrincipal): a person who
    // types the same words still sees what they sent.
    if (
      c.authorKind === "automation" &&
      c.author === "console-session" &&
      (c.body || "").trim() === "_Waiting for you._"
    )
      return;

    entries.push({
      // A comment's own id where there is one -- ui.Comment.ID, the
      // conversation's own order -- and the index for a shape that
      // predates it, rather than the body, which is not unique.
      key: `comment-${c.id ?? i}`,
      kind: "message",
      at: c.createdAt ? new Date(c.createdAt) : null,
      direction: c.authorKind === "human" ? "outgoing" : "incoming",
      // onBehalfOf is set when grain relayed somebody else's words: a
      // question from a dispatched run is grain speaking for an agent,
      // and the chat says so rather than putting grain's name on the
      // agent's sentence.
      sender: c.onBehalfOf
        ? `${c.author} on behalf of ${c.onBehalfOf}`
        : c.author || (c.authorKind === "human" ? "human" : "grain"),
      body: c.body || "",
      attachments: c.attachments || [],
    });
  });

  (t.attempts || []).forEach((a) => {
    // A live attempt is already on screen as the typing indicator, and a
    // successful one is the reply the reader is looking at.
    if (!a.finishedAt || a.outcome === "succeeded" || !a.outcome) return;
    entries.push({
      key: `attempt-${a.number}`,
      kind: "note",
      at: new Date(a.finishedAt),
      text: outcomeLabel(a.outcome),
      detail: a.detail || "",
      attempt: a,
    });
  });

  // Oldest first, always: a chat is read downwards, whichever way round
  // the reader has asked to see a task's timeline
  // (TimelineOrderContext). An entry with no timestamp sorts to the top
  // rather than jumping around between polls.
  entries.sort((a, b) => (a.at?.getTime() ?? 0) - (b.at?.getTime() ?? 0));
  return entries;
}

// awaitsReply is whether the chat is waiting on the person reading it --
// a run that asked a question and parked on the answer
// (model.StateAwaitingReply). It is the one state that changes what the
// composer says: the agent is not coming back until somebody types.
export function awaitsReply(t) {
  return t.state === "awaiting_reply";
}

// working is whether an agent is on the other end right now, which is
// what the typing indicator is for. Deliberately the same two states the
// rest of the UI treats as "grain has this in hand" -- a queued turn is
// as much "your message is being dealt with" as a running one, and the
// gap between them is a dispatch cycle nobody in a chat should have to
// know about.
export function working(t) {
  return t.state === "running" || t.state === "queued";
}

// typingNote is what the indicator says while an agent works: whatever
// the run last said it was doing (ui.Task.Activity, written by
// update_status and by every run_command's own description), or a plain
// fallback for a run that has not narrated itself yet.
//
// The fallback is not a guess about what it is doing. It says only that
// something is happening, which is exactly what a chat window with
// nothing in it cannot otherwise say.
export function typingNote(t) {
  if (t.state === "queued") return "queued";
  if (t.activity) {
    return t.activityBySetup ? `grain: ${t.activity}` : t.activity;
  }
  return "working";
}
