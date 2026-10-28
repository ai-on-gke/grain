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

import { useEffect, useRef, useState } from "react";
import CloseIcon from "@mui/icons-material/Close";
import { Button, Dialog, IconButton } from "@mui/material";
import { useIsPhone } from "../phone.js";
import { guardLeaving } from "../unsavedEdits.js";
import { SIDEBAR_WIDTH } from "../theme.js";
import PageHeader, { PageHeaderScope } from "./PageHeader.jsx";

// Shared overlay chrome, now MUI's own Dialog: it already closes on a
// backdrop click and Escape, and traps focus the way the old hand-rolled
// backdrop div never did. maxWidth/fullWidth stand in for panel/
// panel-detail's two widths from style.css.
//
// `pane` is the third width, and the one anything you *open* uses
// (grain/task-94): a task, a schedule, a template or a suite fills the
// whole content area beside the sidebar, top to bottom, instead of a
// box floating in the middle of it. An agent's own answer inside a task
// runs to headings, lists and fenced diffs, and reading that through a
// 900px porthole was the complaint. The three lists whose "+" button and
// whose rows open the same component (schedules, templates, suites) get
// it for both, so opening one item is the same gesture with the same
// result everywhere. Settings and System are panes too now
// (grain/task-115): both are a whole destination behind a tab strip
// rather than one form to fill in and dismiss, and a centered box was
// making a log tail, a sandbox table and a six-tab settings form share
// the same 600-900px porthole. What is left in a centered box is what
// really is a single *action*: Run a suite, an attempt's transcript.
// New task is neither shape any more -- it is SidePanel's full-height
// panel down the right-hand edge (grain/task-85), because a form of a
// dozen fields in a centered box grew past the bottom of the window and
// took its own Create button with it.
//
// `header` is the pane's own fixed chrome, above the part that scrolls:
// a title and a tab strip that stay put while the tab's content moves
// under them, so a pane the height of the screen never scrolls its own
// tabs off the top. Panes without one (a task, a schedule) pass nothing
// and scroll their whole body as before.
//
// A pane leaves by a back button in its top-left corner, not by an X in
// its top-right (grain/task-177). A pane is a destination -- opening a
// task or a schedule fills the whole content area beside the sidebar and
// puts its own URL in the address bar -- and the other destinations of
// that kind, a repo's page (RepoPage) and a repo's releases
// (RepoReleases), have always left by "← Repos" on the left. Two
// gestures for the same "go back where I came from", in opposite corners
// of the screen, was the inconsistency. `backLabel` names where that is
// for the panes that only ever open from one list; the default says just
// "Back", for the panes (a task, Settings, System, Metrics) that can be
// opened from more than one place and so cannot honestly name one.
//
// The centered shape keeps its X: New task, Run a suite and an attempt's
// transcript are single actions taken over the page you are already on,
// not somewhere you navigated to, and there is nothing to go "back" to.
// Escape closes a pane or dialog without the focused field ever losing
// focus, and an unmounted field fires no blur. Settings save each text
// field on blur, so a value typed and then dismissed with Escape was
// thrown away while the same value dismissed with "← Back" -- whose click
// blurs the field first -- was saved. Blurring the field before closing
// makes Escape commit it the way every other way out of the pane does.
// It returns the field it blurred, so a pane that is not left after all
// can hand focus back to it.
function blurFocusedField() {
  const el = document.activeElement;
  if (!el || typeof el.blur !== "function") return null;
  if (
    el.tagName === "INPUT" ||
    el.tagName === "TEXTAREA" ||
    el.isContentEditable
  ) {
    el.blur();
    return el;
  }
  return null;
}

// Asked before an edited pane is left by any of its own ways out --
// Escape, a backdrop click, "← Back", the X. A pane whose form saves
// only on Save (a schedule, a template, a suite) used to close on Escape
// and drop the edit without a word; one whose fields save as they are
// left (Settings, Team settings) passes no `isDirty` and never asks.
export const DISCARD_PROMPT =
  "You have unsaved changes here. Leave and discard them?";

// `animate` is whether the pane fades in as it opens, MUI's default. A
// pane that takes the place of one already on screen -- a task's detail
// replacing its own loading pane, or the next task opened from inside
// the last one -- passes false: each is a Dialog of its own, so the
// swap unmounts one and mounts the next, and fading the new one (and
// its backdrop) in from nothing flashed the page behind for a moment
// (grain/task-2179). Read once, at mount; it only ever governs entering.
//
// `label` is the dialog's accessible name. MUI's Dialog takes no name
// from what is drawn inside it, so without one a screen reader announced
// a task pane or the Bulk edit form as a bare "dialog" (found in the
// 2026-10-02 intake walkthrough). A plain string rather than a heading
// id, because the heading is not always there: a task pane being edited
// swaps its title heading for the Title field. On the paper, which is
// the element carrying role="dialog"; Dialog's own root is not.
export default function Overlay({
  onClose,
  label,
  wide = false,
  pane = false,
  backLabel = "Back",
  header = null,
  isDirty = null,
  animate = true,
  children,
}) {
  const [transitionDuration] = useState(() => (animate ? undefined : 0));
  // On a phone the rail is not beside the pane at all -- it is in
  // PhoneNav's drawer -- so there is nothing to leave room for and the
  // pane takes the whole width. Asked of phone.js rather than of the
  // `sm` breakpoint below, which a phone held sideways is wide enough to
  // clear while having no rail on screen to spare the width for.
  const isPhone = useIsPhone();
  // `isDirty` is the owner's own answer to "is there an edit here that
  // leaving would lose?", asked at the moment of leaving. The form's own
  // Cancel button calls onClose directly and is not asked: that is
  // already somebody saying "throw it away".
  const handleClose = (event, reason) => {
    const blurred = reason === "escapeKeyDown" ? blurFocusedField() : null;
    if (isDirty?.() && !window.confirm(DISCARD_PROMPT)) {
      // Staying: put focus back in the field Escape blurred. Left on
      // <body>, it is outside the dialog until MUI's focus trap notices,
      // and an Escape pressed before then never reaches the dialog.
      blurred?.focus();
      return;
    }
    onClose?.(event, reason);
  };
  // And the same question for browser Back and Forward, which leave
  // the pane without passing through any of the above: App asks it from
  // its popstate handler (unsavedEdits.js's guardLeaving).
  const latestIsDirty = useRef(isDirty);
  latestIsDirty.current = isDirty;
  const guarded = Boolean(isDirty);
  useEffect(() => {
    if (!guarded) return undefined;
    return guardLeaving(() => Boolean(latestIsDirty.current?.()));
  }, [guarded]);
  if (pane) {
    return (
      <Dialog
        open
        onClose={handleClose}
        PaperProps={{ "aria-label": label }}
        transitionDuration={transitionDuration}
        fullScreen
        scroll="paper"
        sx={{
          // The container is a flex row; pushing the paper to the end and
          // sizing it to "everything but the sidebar" is what leaves the
          // sidebar showing beside it rather than covered by it. Below
          // the sidebar's own breakpoint there is no room to spare, so
          // the pane takes the width outright.
          "& .MuiDialog-container": { justifyContent: "flex-end" },
          "& .MuiDialog-paper": {
            width: isPhone
              ? "100%"
              : {
                  xs: "100%",
                  // The rail's width as App.jsx last set it -- narrower
                  // when the rail is folded to its icons.
                  sm: `calc(100% - var(--sidebar-width, ${SIDEBAR_WIDTH}px))`,
                },
            borderLeft: 1,
            borderColor: "divider",
          },
        }}
      >
        {/* The pane's header rail: the same component every page in the
            main pane is headed by (PageHeader.jsx, grain/task-4915), with
            the way back where a page has its title. No bell, System or
            Settings on it: those are on the rail of the page this pane
            is opened over, and the pane is a step inside that page
            rather than a page of its own
            (docs/design/settings-panel.md §4.1). */}
        <PageHeader
          variant="pane"
          title={
            <span className="overlay-pane-back">
              <Button onClick={handleClose} sx={{ ml: -0.9 }}>
                &larr; {backLabel}
              </Button>
            </span>
          }
        />
        {header !== null && (
          <div className="overlay-pane-header">
            <PageHeaderScope>{header}</PageHeaderScope>
          </div>
        )}
        {/* The pane's own body scrolls, not the document: the paper is
            the viewport's full height and its content (a long timeline,
            a long form) is what overflows. With a header above it, this
            is the only part that moves. */}
        <div className="overlay-pane">
          <PageHeaderScope>{children}</PageHeaderScope>
        </div>
      </Dialog>
    );
  }
  // The centered box is a box on anything with room for one, and the
  // whole screen on a phone: New task is a form of a dozen fields, and
  // MUI's own centered shape would leave it a 32px margin on each side
  // and the rest of the page showing uselessly behind it. Full screen it
  // is the paper, not the document, that scrolls (scroll="paper"), so
  // the body below has to be the part that moves.
  const body = isPhone
    ? {
        padding: "1.25rem 1rem",
        paddingBottom: "calc(1.25rem + env(safe-area-inset-bottom, 0px))",
        flex: 1,
        minHeight: 0,
        overflowY: "auto",
      }
    : { padding: "1.5rem" };
  return (
    <Dialog
      open
      onClose={handleClose}
      PaperProps={{ "aria-label": label }}
      transitionDuration={transitionDuration}
      maxWidth={wide ? "md" : "sm"}
      fullWidth
      fullScreen={isPhone}
      scroll={isPhone ? "paper" : "body"}
      sx={
        isPhone
          ? { "& .MuiDialog-paper": { pt: "env(safe-area-inset-top, 0px)" } }
          : undefined
      }
    >
      <IconButton
        aria-label="Close dialog"
        onClick={handleClose}
        size="small"
        sx={{
          position: "absolute",
          // Absolute positioning ignores the paper's own padding, so the
          // safe-area inset the paper takes above has to be added here
          // too or the X lands under a notched phone's status bar.
          top: isPhone ? "calc(8px + env(safe-area-inset-top, 0px))" : 8,
          right: 8,
          color: "text.secondary",
        }}
      >
        <CloseIcon fontSize="small" />
      </IconButton>
      <div style={body}>
        <PageHeaderScope>{children}</PageHeaderScope>
      </div>
    </Dialog>
  );
}
