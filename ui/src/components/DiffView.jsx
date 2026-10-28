import React, { useMemo, useState } from "react";
import {
  Box,
  Stack,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
} from "@mui/material";
import { Diff, Hunk, markEdits, parseDiff, tokenize } from "react-diff-view";
import "react-diff-view/style/index.css";
import { useIsPhone } from "../phone.js";

// Grain's own diff view -- what the repo page shows for a comparison the
// controller made itself (pkg/ui.RepoComparer), rather than sending the
// reader to a forge for it. It used to be the unified patch in a <pre>,
// which is the one thing a reviewer cannot do much with: every line of
// two files interleaved in one column, in one colour, with the `+` and
// the `-` at the left margin doing all the work.
//
// So the patch is parsed and drawn as two columns with the old file on
// the left and the new one on the right, insertions green and deletions
// red, and the words that actually moved within a changed line picked
// out darker (markEdits below). react-diff-view does all of that: it
// takes the same unified diff the API already returns -- no second
// request, no new server field -- and renders it as React elements, so
// nothing here has to inject HTML a diff's own contents got into.
//
// The unified layout is one column of the same thing, for a window with
// no room for two -- which is what a phone gets by default -- and the
// raw layout is the patch itself, untouched, for anyone who came here to
// copy it rather than to read it.
//
// The parse is the one thing that can fail on input this component does
// not control: gitdiff-parser throws on text that is not a patch at all,
// and returns a file with no hunks for a header-only entry (a rename, a
// mode change, a binary file). Neither is an error worth showing anyone,
// so a patch that will not parse falls back to exactly the <pre> this
// replaced, and a file with no hunks says so in a line under its own
// header.

// The library styles itself through CSS custom properties, which is what
// lets the diff follow the app's two palettes instead of shipping its
// own light-only greens and reds (theme.js owns the twin values). The
// gutter is the louder of each pair so the line numbers read as the
// change's edge; the "edit" colours are the within-line marks, which sit
// on top of the line colour and so have to be darker than it on the
// light ground and lighter on the dark one.
const PALETTE = {
  light: {
    "--diff-gutter-insert-background-color": "#cdf0d5",
    "--diff-gutter-delete-background-color": "#f8d3d6",
    "--diff-code-insert-background-color": "#e8f8ec",
    "--diff-code-delete-background-color": "#fdecee",
    "--diff-code-insert-edit-background-color": "#a7e3b6",
    "--diff-code-delete-edit-background-color": "#f5b2b7",
  },
  dark: {
    "--diff-gutter-insert-background-color": "#1d4a2c",
    "--diff-gutter-delete-background-color": "#5a2429",
    "--diff-code-insert-background-color": "#14301d",
    "--diff-code-delete-background-color": "#38181c",
    "--diff-code-insert-edit-background-color": "#2f6b41",
    "--diff-code-delete-edit-background-color": "#7d343a",
  },
};

// Tokenizing a file walks every change in it twice, once to build the
// tokens and once to diff the changed lines against each other, so the
// within-line marks are worth their cost on a file somebody is going to
// read and not on a 50,000-line generated one. Above this many changes
// the file is still drawn side by side, just without the word-level
// highlighting.
const MAX_TOKENIZED_CHANGES = 2000;

// A table row per changed line is a DOM node per changed line, and the
// server will hand over as much as 2 MiB of patch (cmd/grain's
// stateManager.Compare truncates there) -- tens of thousands of rows,
// which is a browser sitting still for seconds before anything appears.
// Above this much text the view opens raw, which is what it has always
// cost to show and is still readable; the toggle is right there for a
// reader who wants the tables anyway and is willing to wait for them.
const HUGE_DIFF_BYTES = 400 * 1024;

// gitdiff-parser is lenient to a fault: it throws on some text that is
// not a patch ("+work in progress") and quietly reads other text as a
// one-file patch with no hunks in it ("not a patch at all" becomes a
// file called "a"). Neither is worth drawing a table for, so the text is
// asked first whether it even claims to be a patch -- a line that starts
// the way git starts one -- and anything that does not is shown as it
// arrived.
const PATCH_HEADER = /^(diff --git |--- |\+\+\+ |@@ )/m;

function parseFiles(diff) {
  if (!diff || !PATCH_HEADER.test(diff)) {
    return null;
  }
  try {
    const files = parseDiff(diff, { nearbySequences: "zip" });
    return files && files.length > 0 ? files : null;
  } catch {
    return null;
  }
}

// What to call a file, in git's own words: a rename says both names, a
// delete is named by the name it had, everything else by the name it has.
function titleOf(file) {
  if (
    file.type === "rename" ||
    (file.oldPath &&
      file.oldPath !== file.newPath &&
      file.type !== "add" &&
      file.type !== "delete")
  ) {
    return `${file.oldPath} → ${file.newPath}`;
  }
  if (file.type === "delete") {
    return file.oldPath;
  }
  return file.newPath || file.oldPath;
}

function countChanges(file) {
  let insertions = 0;
  let deletions = 0;
  for (const hunk of file.hunks || []) {
    for (const change of hunk.changes || []) {
      if (change.type === "insert") {
        insertions += 1;
      } else if (change.type === "delete") {
        deletions += 1;
      }
    }
  }
  return { insertions, deletions };
}

// A file the patch names but carries no lines for. git says why in the
// header it did print, so this says the same thing in a sentence rather
// than leaving an empty box under the name -- and where the header does
// not say (a binary file is "Binary files a/x and b/x differ", which the
// parser keeps nothing of), it at least says that there is nothing to
// draw rather than drawing nothing.
function emptyFileNote(file) {
  if (file.type === "rename") {
    return "Renamed with no changes to its contents.";
  }
  if (file.oldMode && file.newMode && file.oldMode !== file.newMode) {
    return `File mode changed from ${file.oldMode} to ${file.newMode}.`;
  }
  return "No line changes to show.";
}

function FileDiff({ file, viewType }) {
  const { hunks } = file;
  const tokens = useMemo(() => {
    const changes = (hunks || []).reduce(
      (n, hunk) => n + (hunk.changes ? hunk.changes.length : 0),
      0,
    );
    if (!hunks || hunks.length === 0 || changes > MAX_TOKENIZED_CHANGES) {
      return undefined;
    }
    try {
      return tokenize(hunks, {
        enhancers: [markEdits(hunks, { type: "block" })],
      });
    } catch {
      // Word-level marks are a nicety; a file whose contents defeat the
      // tokenizer is still worth drawing without them.
      return undefined;
    }
  }, [hunks]);

  const { insertions, deletions } = countChanges(file);
  return (
    <Box
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        overflow: "hidden",
      }}
    >
      <Stack
        direction="row"
        spacing={1}
        alignItems="baseline"
        sx={{
          px: 1,
          py: 0.5,
          bgcolor: "action.hover",
          borderBottom: 1,
          borderColor: "divider",
        }}
      >
        <Typography
          variant="body2"
          sx={{
            fontFamily: "var(--font-mono, ui-monospace, monospace)",
            fontWeight: 600,
            wordBreak: "break-all",
            flexGrow: 1,
          }}
        >
          {titleOf(file)}
        </Typography>
        <Typography variant="caption" sx={{ color: "success.main" }}>
          +{insertions}
        </Typography>
        <Typography variant="caption" sx={{ color: "error.main" }}>
          -{deletions}
        </Typography>
      </Stack>
      {hunks && hunks.length > 0 ? (
        <Box sx={{ overflowX: "auto" }}>
          <Diff
            viewType={viewType}
            diffType={file.type}
            hunks={hunks}
            tokens={tokens}
          >
            {(rendered) =>
              rendered.map((hunk) => <Hunk key={hunk.content} hunk={hunk} />)
            }
          </Diff>
        </Box>
      ) : (
        <Typography variant="body2" color="text.secondary" sx={{ p: 1 }}>
          {emptyFileNote(file)}
        </Typography>
      )}
    </Box>
  );
}

// The patch as it arrived, which is what this whole component used to
// be: still the third option below, because a reviewer who wants to
// copy the patch, paste it into `git apply` or read the headers the
// tables leave out should not have to go to a forge for it.
function RawDiff({ diff, label, maxHeight }) {
  return (
    <Box
      component="pre"
      aria-label={label}
      sx={{
        m: 0,
        p: 2,
        overflow: "auto",
        ...(maxHeight ? { maxHeight } : {}),
        bgcolor: "action.hover",
        borderRadius: 1,
        fontSize: "0.75rem",
      }}
    >
      {diff}
    </Box>
  );
}

// label is what the whole thing answers to -- the caller's own name for
// this diff, so a page with more than one of them is still navigable by
// screen reader and assertable by the suites that already look one up by
// name ("Branch changes", RepoPage.jsx).
//
// maxHeight, if provided, confines the diff to a scrollable box; omitting
// it leaves the diff free to stretch the page vertically, which is what a
// pull request page needs so reviewers can scroll through files naturally.
export default function DiffView({ diff, label = "Diff", maxHeight = null }) {
  const files = useMemo(() => parseFiles(diff), [diff]);
  const isPhone = useIsPhone();
  // Null until somebody picks, so the default follows what is in front
  // of the reader: two columns need the width, a phone has room for one,
  // and a patch big enough to cost a second of layout opens as text.
  // A choice made by hand survives a rotation, which is the point of
  // keeping it separate from the questions rather than seeding state
  // from them.
  const [chosenView, setChosenView] = useState(null);
  const huge = diff.length > HUGE_DIFF_BYTES;
  const viewType = chosenView || (huge ? "raw" : isPhone ? "unified" : "split");

  if (!files) {
    return <RawDiff diff={diff} label={label} maxHeight={maxHeight} />;
  }

  return (
    <Box
      role="region"
      aria-label={label}
      sx={(theme) => ({
        ...PALETTE[theme.palette.mode === "dark" ? "dark" : "light"],
        "--diff-background-color": "transparent",
        "--diff-text-color": "inherit",
        "--diff-font-family": "var(--font-mono, ui-monospace, monospace)",
        // The rest of the library's palette, which PALETTE above did
        // not need to split by mode because every value here is a
        // token that already does. They were left at the library's
        // defaults until now, and its default for a selection is a
        // light blue -- so dragging across a diff in dark mode lit it
        // up in the one colour nothing else in the app uses. That is
        // the same hole the chat widget had (src/chatscopeTheme.js):
        // an imported stylesheet is themed completely or it is themed
        // until somebody notices the next part of it.
        "--diff-selection-background-color": "var(--accent-subtle)",
        "--diff-selection-text-color": "var(--text)",
        "--diff-gutter-selected-background-color": "var(--surface-active)",
        "--diff-code-selected-background-color": "var(--surface-active)",
        "--diff-omit-gutter-line-color": "var(--border-strong)",
        "& .diff": { fontSize: "0.75rem" },
        "& .diff-gutter": { cursor: "default" },
      })}
    >
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        spacing={1}
        sx={{ mb: 0.5 }}
      >
        <Typography variant="caption" color="text.secondary">
          {huge && !chosenView
            ? "A large patch, shown as text -- pick a layout to draw it."
            : ""}
        </Typography>
        <ToggleButtonGroup
          size="small"
          exclusive
          value={viewType}
          aria-label="Diff layout"
          onChange={(event, next) => next && setChosenView(next)}
        >
          <ToggleButton value="split">Side by side</ToggleButton>
          <ToggleButton value="unified">Inline</ToggleButton>
          <ToggleButton value="raw">Raw</ToggleButton>
        </ToggleButtonGroup>
      </Stack>
      {viewType === "raw" ? (
        <RawDiff diff={diff} label={label} maxHeight={maxHeight} />
      ) : (
        <Stack
          spacing={1}
          sx={maxHeight ? { overflow: "auto", maxHeight } : undefined}
        >
          {files.map((file, index) => (
            <FileDiff
              key={`${file.oldPath || ""}-${file.newPath || ""}-${index}`}
              file={file}
              viewType={viewType}
            />
          ))}
        </Stack>
      )}
    </Box>
  );
}
