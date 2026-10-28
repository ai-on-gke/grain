import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  Typography,
} from "@mui/material";
import DeleteOutlineIcon from "@mui/icons-material/DeleteOutline";
import api from "../api.js";
import { STATE_LABELS, askedCase } from "../state.js";
import ItemGlyph from "./ItemGlyph.jsx";
import {
  ListEmpty,
  ListHeader,
  ListSearchField,
  ListToolbar,
} from "./ListPrimitives.jsx";

// SessionsPage lists the standing interactive console sessions: the
// director, and any dedicated interactive agents created for this deployment.
//
// missingSessionId is a session somebody asked for by id -- a deep link,
// Back onto one since deleted -- that this deployment does not have. The
// list is what the pane shows instead of a different session's chat, and
// it says why, so the reader is not left wondering where theirs went.
//
// tasks is the shell's whole task list, unfiltered: a session's own task
// is left out of every task view (App.jsx's visibleTasks), so this list
// is where what it is doing shows -- each row says whether the agent is
// working or the conversation is waiting on you, which is the question
// somebody coming back to a session is asking.
//
// onDeleted is called with a session's id once this page has deleted
// it, and is what turns the row's delete control on: without it the
// rows open their sessions and nothing else. The delete itself -- the
// confirmation, then DELETE /api/console-sessions/{id} -- is this
// page's, the same exchange a session's own pane makes
// (ConsoleSessionPage.jsx's handleDelete), so the two read the same
// however a session is got rid of. Errors go to showError.
export default function SessionsPage({
  sessions = [],
  tasks = [],
  missingSessionId = null,
  onOpenSession,
  onNewSession,
  onDeleted,
  showError,
}) {
  const [search, setSearch] = useState("");
  // The id of the session whose delete is in flight, so that one row's
  // control is held while the request runs and the others are not.
  const [deletingId, setDeletingId] = useState(null);
  const q = search.trim().toLowerCase();
  const visible = sessions.filter(
    (s) =>
      q === "" ||
      (s.title || "").toLowerCase().includes(q) ||
      (s.id || "").toLowerCase().includes(q),
  );

  const taskById = new Map(tasks.map((t) => [t.id, t]));

  const deleteSession = async (session) => {
    if (
      !window.confirm(
        `Delete session "${session.title || session.id}"? This removes the session from this deployment.`,
      )
    ) {
      return;
    }
    setDeletingId(session.id);
    try {
      await api(`/api/console-sessions/${session.id}`, { method: "DELETE" });
      await onDeleted(session.id);
    } catch (err) {
      if (showError) showError(err);
    } finally {
      setDeletingId(null);
    }
  };

  return (
    // role="main": this page is the shell's content, and without the
    // landmark a screen reader's "jump to main" found nothing on it while
    // Tasks, Inbox and CI had one. A role rather than a <main> so the
    // element keeps .main-column's layout and not main's own padding.
    <div className="main-column" role="main">
      <ListHeader
        title="Sessions"
        count={visible.length}
        icon={<ItemGlyph kind="sessions" size={20} />}
        action={
          onNewSession ? (
            <Button
              size="small"
              variant="contained"
              sx={{ ml: "auto" }}
              onClick={() => onNewSession()}
            >
              + New session
            </Button>
          ) : null
        }
      />
      {missingSessionId ? (
        <Alert severity="warning" sx={{ mx: 1.2, mt: 1 }}>
          There is no session &ldquo;{missingSessionId}&rdquo; here. It may have
          been deleted; pick another below.
        </Alert>
      ) : null}
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ px: 1.2, pt: 1, maxWidth: "46rem" }}
      >
        Standing interactive sessions connect you directly to AI agents working
        within this deployment. A new session is named when you create it, and
        is kept here once you have created it or said something in it; the
        Director is the one grain ships.
      </Typography>

      {sessions.length > 0 && (
        <ListToolbar>
          <ListSearchField
            placeholder="Search sessions…"
            value={search}
            onChange={setSearch}
          />
        </ListToolbar>
      )}

      {visible.length === 0 ? (
        <ListEmpty>
          <Box
            component="span"
            sx={{
              display: "inline-flex",
              flexDirection: "column",
              alignItems: "center",
              gap: 1.2,
            }}
          >
            <span>
              {sessions.length === 0
                ? "No interactive sessions yet."
                : "No matching sessions."}
            </span>
            {onNewSession ? (
              <Button variant="contained" onClick={() => onNewSession()}>
                + New session
              </Button>
            ) : null}
          </Box>
        </ListEmpty>
      ) : (
        <ul className="template-list">
          {visible.map((session) => (
            // The way into a session from this list is a real <button>
            // holding the session's glyph and name -- the words its
            // accessible name repeats -- with the status chip (which it
            // is described by), the hint and the delete control beside
            // it rather than inside it. The row itself
            // used to be the button (an <li role="button">) with the bin
            // nested in it: a control inside a control, which a screen
            // reader flattens or skips, an <li> that was no longer a list
            // item, and a name ("Open session X") that did not include
            // the status the row showed -- all three flagged by axe in
            // the 2026-10-26 walkthrough. The <li> keeps the click so the
            // whole row, padding included, still opens for a pointer: the
            // button's own click -- a pointer's, or Enter and Space on it
            // -- bubbles up to it, and the bin stops its click at itself.
            <li
              key={session.id}
              className="template-row session-row"
              onClick={() => onOpenSession && onOpenSession(session.id)}
              style={{ cursor: "pointer" }}
            >
              <button
                type="button"
                className="session-row-open"
                aria-label={`Open session ${session.title || session.id}`}
                aria-describedby={`session-status-${session.id}`}
              >
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    marginRight: "0.5rem",
                  }}
                >
                  <ItemGlyph
                    kind={session.id === "director" ? "director" : "sessions"}
                    size={16}
                  />
                </span>
                <span className="template-name">
                  {session.title || session.id}
                </span>
              </button>
              <span className="chips" id={`session-status-${session.id}`}>
                <SessionStatus task={taskById.get(session.taskId)} />
              </span>
              <span className="template-title hint">
                {session.id === "director"
                  ? "Director standing session"
                  : "Standing interactive session"}
              </span>
              {/* Delete from the list, not only from inside the session
                  (grain/task-15): getting rid of one used to mean opening
                  it first and finding "Delete session" under its chat. An
                  icon rather than a worded button so a row stays one line
                  on a phone, named in full for a screen reader and by
                  title for a pointer, the way CustomizeSidebarOverlay's
                  bin is. Always shown rather than on hover: a finger has
                  no hover. The same confirmation the pane asks before
                  it, since the click ends a conversation. Its place on
                  the row, and what the row gives up for it on a phone,
                  is .session-row-actions in style.css. */}
              {onDeleted ? (
                <span className="session-row-actions">
                  <IconButton
                    size="small"
                    aria-label={`Delete session ${session.title || session.id}`}
                    title="Delete session"
                    disabled={deletingId === session.id}
                    onClick={(e) => {
                      e.stopPropagation();
                      deleteSession(session);
                    }}
                  >
                    <DeleteOutlineIcon fontSize="small" />
                  </IconButton>
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// sessionStatus is what a session's task's state means for the
// conversation it holds, or null when there is nothing worth saying (no
// task filed yet, or one in a state a session does not ordinarily sit
// in). awaiting_reply is the ordinary resting state of every session;
// as a session's it means the last word was the agent's, and it reads
// "Asked" with the reason as its tooltip, the way the state does on any
// other task (state.js's askedCase).
function sessionStatus(task) {
  switch (task?.state) {
    case "running":
      return { label: "Working", color: "info" };
    case "queued":
      return { label: "Starting", color: "info" };
    case "awaiting_reply":
      return {
        label: STATE_LABELS.awaiting_reply,
        title: askedCase(task)?.title,
        color: "default",
      };
    case "failed":
      return { label: "Last reply failed", color: "error" };
    default:
      return null;
  }
}

function SessionStatus({ task }) {
  const status = sessionStatus(task);
  if (!status) return null;
  return (
    <Chip
      size="small"
      variant="outlined"
      color={status.color}
      label={status.label}
      title={status.title}
      sx={{ ml: 0.5 }}
    />
  );
}
