import { useState } from "react";
import {
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { PageHeaderActions, useInPageHeader } from "./PageHeader.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import ModelPicker from "./ModelPicker.jsx";
import TaskChat from "./TaskChat.jsx";

// NEW_SESSION_ID is the id the shell gives a chat that has not been
// started yet: /session/new, and the one id the daemon refuses to give
// a real session (ui.draftConsoleSessionID).
export const NEW_SESSION_ID = "new";

// NewSessionPage is a chat before it is a session: the window a click
// on "New session" opens, empty, with the composer focused, the way a
// chat app opens a new chat -- and a "Session name" field above it.
//
// # Why the name is asked for
//
// The page used to be the composer alone, and the session was named
// from the first message (model.ConsoleSessionTitleFromMessage). People
// did not read it that way: they typed the name they wanted as the
// first message, and got a session called that with a "message" that
// was only a name. So the name is a field of its own, and it is the
// session's title. It can start the session by itself -- Create session,
// or Enter in the field -- which is POST /api/console-sessions with a
// title and no message; or the first message can, with the name beside
// it. Left blank, the name falls back to what the first message says,
// and the field's helper text says so.
//
// # Why nothing is saved until then
//
// Because this page is not a session. It is held in the URL and in
// nothing else: opening it creates no row, no task and no entry in the
// sessions list, so a chat somebody opened and walked away from leaves
// nothing behind, and the list holds only sessions somebody started.
//
// # Why the model and the pin are asked too
//
// The model is the one choice that cannot be made after the first
// message without the first message having gone to the wrong model. It
// is offered, not required: left alone, the session runs on the team's
// default. "Pin to sidebar" saves the trip to the rail's pin menu for a
// session somebody already knows they will come back to; it is off by
// default, as a session started any other way is unpinned.
export default function NewSessionPage({ config = {}, onStart }) {
  const inRail = useInPageHeader();
  const [chosenModel, setChosenModel] = useState("");
  const [sessionName, setSessionName] = useState("");
  const [pin, setPin] = useState(false);
  const [starting, setStarting] = useState(false);

  // The chat window draws a task; this one has none yet. A synthetic
  // one with nothing said in it draws as an empty transcript with the
  // composer under it, and no state the window would react to --
  // neither "waiting for your reply" nor a typing indicator.
  const draft = {
    id: NEW_SESSION_ID,
    state: "",
    comments: [],
    attempts: [],
  };

  // start is both ways in: the name alone (no message), or the first
  // message with whatever name was typed. It answers whether the
  // session started, so the composer keeps a message that did not go.
  const start = async (first = {}) => {
    if (starting) return false;
    setStarting(true);
    try {
      await onStart({
        title: sessionName.trim(),
        ...first,
        model: chosenModel || undefined,
        pin,
      });
      return true;
    } catch {
      // onStart reports the failure in the shell's banner (App.jsx's
      // startStandingSession); false puts the message back in the box.
      return false;
    } finally {
      setStarting(false);
    }
  };

  const send = ({ body, attachments }) => start({ message: body, attachments });

  const canCreate = sessionName.trim() !== "" && !starting;
  const create = (e) => {
    e?.preventDefault();
    if (canCreate) start();
  };

  return (
    <div className="console-session-page" role="main">
      {inRail && (
        <Typography
          component="h2"
          className="console-session-title visually-hidden"
        >
          New chat
        </Typography>
      )}
      <PageHeaderActions>
        <header className="content-header console-session-header">
          {!inRail && (
            <>
              <span className="header-icon">
                <ItemGlyph kind="sessions" size={20} />
              </span>
              <Typography component="h2" className="console-session-title">
                New chat
              </Typography>
            </>
          )}
        </header>
      </PageHeaderActions>
      <div className="console-session-body">
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ mt: 1, mb: 1, maxWidth: "46rem" }}
        >
          Name the session and create it, or say what you want and the session
          starts with your first message. Nothing is kept until you do one or
          the other.
        </Typography>
        <Box
          component="form"
          onSubmit={create}
          className="new-session-form"
          sx={{ mb: 1.5, maxWidth: "46rem" }}
        >
          <Stack
            direction={{ xs: "column", sm: "row" }}
            spacing={1.5}
            sx={{ alignItems: { xs: "stretch", sm: "flex-start" } }}
          >
            <TextField
              label="Session name"
              name="sessionName"
              value={sessionName}
              onChange={(e) => setSessionName(e.target.value)}
              size="small"
              fullWidth
              autoComplete="off"
              helperText="The session's title. Left blank, it is named from your first message."
            />
            <Button
              type="submit"
              variant="contained"
              disabled={!canCreate}
              sx={{ flexShrink: 0, whiteSpace: "nowrap", mt: { sm: "1px" } }}
            >
              Create session
            </Button>
          </Stack>
          <FormControlLabel
            control={
              <Checkbox
                checked={pin}
                onChange={(e) => setPin(e.target.checked)}
                size="small"
              />
            }
            label="Pin to sidebar"
          />
        </Box>
        <TaskChat
          t={draft}
          onSend={send}
          placeholder="Ask anything…"
          actions={
            <Box
              sx={{ ml: "auto", minWidth: "12rem", maxWidth: "20rem" }}
              className="new-session-model"
            >
              <ModelPicker
                config={config}
                name="sessionModel"
                value={chosenModel}
                onChange={setChosenModel}
                margin="none"
              />
            </Box>
          }
        />
        {starting && (
          <Typography variant="caption" color="text.secondary" role="status">
            Starting the session…
          </Typography>
        )}
      </div>
    </div>
  );
}
