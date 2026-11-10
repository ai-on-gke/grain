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
import { Alert, Snackbar } from "@mui/material";
import { commentIdFromHash, malformedCommentHash } from "../commentLinks.js";

// HIGHLIGHT_MS is how long a linked comment stays marked once it has
// been scrolled to (.comment-highlight in style.css fades over the same).
const HIGHLIGHT_MS = 2500;

// How long to keep looking for a comment the detail says exists but the
// page has not drawn yet: the chat window and the timeline mount after
// the detail lands, and a poll can replace either while it is looked for.
const FIND_INTERVAL_MS = 50;
const FIND_TRIES = 40;

// shown is whether el is on screen to be scrolled to: not folded inside
// a closed <details> (an interactive task's "Task history") and laid out.
function shown(el) {
  return !el.closest("details:not([open])") && el.getClientRects().length > 0;
}

// findCommentElement is the element drawing comment id under root --
// every place a conversation is drawn marks its comments with
// data-comment-id -- preferring one already on screen, since an
// interactive task draws each comment twice: in its chat and in the
// history folded up beneath it.
// id is digits only (commentIdFromHash), so it needs no escaping.
function findCommentElement(root, id) {
  const all = Array.from(root.querySelectorAll(`[data-comment-id="${id}"]`));
  return all.find(shown) || all[0] || null;
}

// revealComment opens every closed <details> el is folded inside, so a
// comment in a collapsed group can be scrolled to.
function revealComment(el) {
  for (
    let d = el.closest("details:not([open])");
    d;
    d = d.closest("details:not([open])")
  ) {
    d.open = true;
  }
}

// linkedComment is the comment the address links to on task taskId, as
// a fresh target, or null. A /tasks/<id> address naming some other task
// is that task's link, not this one's: following it from here is a
// navigation to that task, whose own pane looks for the comment. A
// #comment= naming no comment is a target with an empty id, which is
// said rather than ignored.
function linkedComment(taskId) {
  const hash = window.location.hash;
  const id = commentIdFromHash(hash);
  if (!id && !malformedCommentHash(hash)) return null;
  const m = /^\/tasks\/([^/]+)/.exec(window.location.pathname);
  if (m && taskId != null && decodeURIComponent(m[1]) !== String(taskId)) {
    return null;
  }
  return { id, hash: hash.slice(0, 40) };
}

// useCommentDeepLink follows the #comment=<id> on the address
// (commentLinks.js) for task t: once t's conversation has loaded it
// scrolls to that comment and highlights it briefly, and does the same
// again whenever the hash changes on the page already open. It returns
// the notice to show when the comment cannot be shown -- not in this
// task's conversation, or not drawn anywhere on the page -- in which case
// the page is left where it was.
//
// The task detail carries its whole conversation (ui.TaskDetail's
// comments), so whether a comment belongs to the task is answered off it
// rather than off the DOM: a comment far back in a long conversation is
// in the detail however the page draws it.
function useCommentDeepLink(t) {
  const taskId = useRef(t?.id);
  taskId.current = t?.id;
  // An object rather than the bare id, so the same link followed twice
  // scrolls twice.
  const [target, setTarget] = useState(() => linkedComment(t?.id));
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const read = () => {
      const next = linkedComment(taskId.current);
      if (next) setTarget(next);
    };
    window.addEventListener("hashchange", read);
    window.addEventListener("popstate", read);
    return () => {
      window.removeEventListener("hashchange", read);
      window.removeEventListener("popstate", read);
    };
  }, []);

  const comments = t?.comments;
  useEffect(() => {
    if (!target || !Array.isArray(comments)) return undefined;
    if (!target.id) {
      setNotice(`This link's ${target.hash} does not name a comment.`);
      setTarget(null);
      return undefined;
    }
    if (!comments.some((c) => String(c.id) === target.id)) {
      setNotice(`Comment ${target.id} is not in this task's conversation.`);
      setTarget(null);
      return undefined;
    }
    let tries = 0;
    let timer = null;
    const look = () => {
      const el = findCommentElement(document, target.id);
      if (!el) {
        tries += 1;
        if (tries < FIND_TRIES) {
          timer = setTimeout(look, FIND_INTERVAL_MS);
          return;
        }
        setNotice(`Comment ${target.id} is not shown on this page.`);
        setTarget(null);
        return;
      }
      revealComment(el);
      setTarget(null);
      // A frame for a <details> just opened to lay its contents out.
      requestAnimationFrame(() => {
        el.scrollIntoView?.({ block: "center" });
        // Focus goes to the comment too, as it does to a fragment's
        // target on a page the browser loads itself: a screen reader
        // reads the comment out, and Tab carries on from it (to its own
        // Copy link) rather than from wherever focus was.
        if (!el.hasAttribute("tabindex")) el.setAttribute("tabindex", "-1");
        el.focus?.({ preventScroll: true });
        el.classList.remove("comment-highlight");
        // Restarts the fade on a comment linked to twice in a row.
        void el.offsetWidth;
        el.classList.add("comment-highlight");
        setTimeout(
          () => el.classList.remove("comment-highlight"),
          HIGHLIGHT_MS,
        );
      });
    };
    look();
    return () => clearTimeout(timer);
  }, [target, comments]);

  return { notice, clearNotice: () => setNotice("") };
}

// CommentDeepLink is useCommentDeepLink for task t with its notice: a
// small one at the foot of the page that does not block anything and
// goes away by itself.
export default function CommentDeepLink({ t }) {
  const { notice, clearNotice } = useCommentDeepLink(t);
  return (
    <Snackbar
      open={Boolean(notice)}
      autoHideDuration={6000}
      onClose={(_, reason) => {
        if (reason !== "clickaway") clearNotice();
      }}
      anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
    >
      <Alert
        severity="info"
        variant="filled"
        onClose={clearNotice}
        data-testid="comment-link-notice"
      >
        {notice}
      </Alert>
    </Snackbar>
  );
}
