import { useEffect, useRef, useState } from "react";
import { Alert, Box, LinearProgress, Typography } from "@mui/material";
import api from "../api.js";
import Markdown from "./Markdown.jsx";

// PREVIEW_DELAY_MS is how long the branch box has to sit still before
// its target is asked for release notes. Asking pulls the target's image,
// so a branch being typed is not asked about one keystroke at a time.
export const PREVIEW_DELAY_MS = 800;

// NOT_IN_THIS_BUILD is pkg/upgrade.NotInThisBuild, the words the pane
// uses for a target with no notes to show for what it brings.
export const NOT_IN_THIS_BUILD = "release notes are not in this build";

// UpgradeReleaseNotes shows what an upgrade onto branch would bring,
// before anything is started: the release notes the target's own build
// carries, for every version newer than the one this deployment runs
// (GET /api/upgrade/release-notes, pkg/upgrade/releasenotes.go). The
// server pulls and asks the target image the way an upgrade asks it for
// schema-version, so this is what that build says about itself.
//
// A target older than the question, or a candidate built before its own
// version's notes were written, has none to show, and says so in the
// server's words rather than looking like a preview that failed.
export default function UpgradeReleaseNotes({ branch, onPreview }) {
  const target = (branch || "").trim();
  const [state, setState] = useState({
    target: "",
    preview: null,
    error: null,
  });
  const onPreviewRef = useRef(onPreview);
  onPreviewRef.current = onPreview;

  useEffect(() => {
    if (!target) return undefined;
    let live = true;
    const timer = setTimeout(async () => {
      try {
        const preview = await api(
          `/api/upgrade/release-notes?branch=${encodeURIComponent(target)}`,
        );
        if (live) {
          setState({ target, preview, error: null });
          onPreviewRef.current?.(target, preview);
        }
      } catch (err) {
        if (live) {
          setState({ target, preview: null, error: err });
          onPreviewRef.current?.(target, null);
        }
      }
    }, PREVIEW_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [target]);

  if (!target) return null;
  const current = state.target === target;

  return (
    <Box data-testid="upgrade-release-notes" sx={{ mt: 2 }}>
      <Typography variant="subtitle1" component="h3" sx={{ mt: 0 }}>
        What {target} brings
      </Typography>
      {!current && (
        <>
          <Typography variant="body2" color="text.secondary">
            Reading the release notes {target} carries…
          </Typography>
          <LinearProgress sx={{ mt: 1 }} />
        </>
      )}
      {current && state.error && (
        <Alert severity="warning" sx={{ mt: 1 }}>
          Could not read the release notes {target} carries:{" "}
          {state.error.message || String(state.error)}
        </Alert>
      )}
      {current && state.preview && <PreviewBody preview={state.preview} />}
    </Box>
  );
}

function PreviewBody({ preview }) {
  const notes = Array.isArray(preview.notes) ? preview.notes : [];
  return (
    <>
      <Typography variant="body2" color="text.secondary">
        {preview.from
          ? `This deployment runs ${preview.from}.`
          : "This deployment's build does not say which version it is, so every version the target has notes for is listed."}
        {preview.version ? ` The target is ${preview.version}.` : ""}
      </Typography>
      {preview.message && (
        // The only message that is not "not in this build" is a target
        // no newer than what runs: an upgrade onto it goes nowhere, or
        // back.
        <Alert
          severity={preview.notInThisBuild ? "info" : "warning"}
          sx={{ mt: 1 }}
        >
          {capitalize(preview.message)}.
        </Alert>
      )}
      {notes.map((note) => (
        <Box
          key={note.version}
          data-testid={`upgrade-release-note-${note.version}`}
          sx={{
            mt: 1,
            p: 1.5,
            border: 1,
            borderColor: "divider",
            borderRadius: 1,
            maxHeight: 360,
            overflow: "auto",
          }}
        >
          <Markdown>{note.markdown || `# ${note.version}`}</Markdown>
        </Box>
      ))}
    </>
  );
}

// capitalize starts the server's message as a sentence -- unless it
// starts with a version, whose "v" is part of its name ("v1.9.0 is not
// newer than v1.10.0").
function capitalize(text) {
  if (!text || /^v\d/.test(text)) return text;
  return text[0].toUpperCase() + text.slice(1);
}
