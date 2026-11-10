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

// Links to one comment in a task's conversation:
// /tasks/<taskId>#comment=<commentId>, where the id is the comment's own
// database id (ui.Comment.ID, the one the API returns). The server
// writes the same shape (model.CommentPath), and agents are told to.
//
// The path is root-relative everywhere it is stored or written, so it
// works behind whatever proxy serves the page; only Copy link makes it
// absolute, with the origin the reader is looking at it through.

// commentIdFromHash is the comment id a location hash names
// ("#comment=13" → "13"), or "" for any other hash.
export function commentIdFromHash(hash) {
  const m = /^#?comment=(\d+)$/.exec(String(hash || "").trim());
  return m ? m[1] : "";
}

// malformedCommentHash is whether hash is a #comment= fragment that names
// no comment -- "#comment=abc", "#comment=" -- as a link truncated or
// mistyped on its way to the address bar is.
export function malformedCommentHash(hash) {
  const h = String(hash || "").trim();
  return /^#?comment=/.test(h) && !commentIdFromHash(h);
}

// commentPath is the root-relative link to comment commentId on task
// taskId.
export function commentPath(taskId, commentId) {
  return `/tasks/${encodeURIComponent(taskId)}#comment=${commentId}`;
}

// commentUrl is commentPath made absolute against origin -- the page's
// own, by default -- which is what Copy link puts on the clipboard: a
// link pasted outside grain has no page to be relative to.
export function commentUrl(
  taskId,
  commentId,
  origin = typeof window !== "undefined" ? window.location.origin : "",
) {
  return `${origin}${commentPath(taskId, commentId)}`;
}

// inAppHref is whether a link in rendered markdown is one of the UI's
// own task pages -- /tasks/<id>, with or without a #comment= fragment or
// a query -- which Markdown follows in place rather than in a new tab.
export function inAppHref(href) {
  return /^\/tasks\/[^/?#\s]+\/?(?:[?#].*)?$/.test(String(href || ""));
}

// navigateInApp moves the single-page app to href without reloading it:
// the address is pushed, and App's popstate handler reads it as it does
// a Back or Forward, opening whatever task it names. A hashchange is
// dispatched as well, since pushState fires none, for the deep-link
// hook (useCommentDeepLink) to see a #comment= on a task already open --
// even the same one again, which scrolls back to it.
export function navigateInApp(href) {
  const before = window.location.pathname + window.location.search;
  window.history.pushState({}, "", href);
  if (window.location.pathname + window.location.search !== before) {
    window.dispatchEvent(new PopStateEvent("popstate", { state: {} }));
  }
  if (window.location.hash) {
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  }
}
