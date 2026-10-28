import { useCallback, useEffect, useState } from "react";

// Whether the desktop rail (Sidebar.jsx) is folded down to its icons.
// It is a choice about this screen rather than about the deployment, so
// it lives in this browser's localStorage the way the board's columns
// and the theme do, and is never sent anywhere. A phone never folds the
// rail: PhoneNav.jsx draws it in a drawer that is already out of the way.
export const SIDEBAR_COLLAPSED_STORAGE_KEY = "grain.sidebar.collapsed";

// The key that folds and unfolds the rail, the way an editor's sidebar
// toggle works. Said on the toggle's own tooltip, which is the one place
// the rail can tell a reader it exists.
export const SIDEBAR_TOGGLE_KEY = "[";

// readSidebarCollapsed is what was chosen here last, or false -- the
// rail open -- for a browser that has never chosen, or whose storage is
// unreadable or denied outright.
export function readSidebarCollapsed() {
  try {
    return localStorage.getItem(SIDEBAR_COLLAPSED_STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

// writeSidebarCollapsed keeps the choice. The open rail is the default,
// so choosing it removes the key rather than storing "false".
export function writeSidebarCollapsed(collapsed) {
  try {
    if (collapsed) localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, "true");
    else localStorage.removeItem(SIDEBAR_COLLAPSED_STORAGE_KEY);
  } catch {
    // Storage denied: the choice lasts as long as the page does.
  }
}

// isTypingTarget is whether a key pressed on el is somebody writing,
// which "[" is in a task description, a chat composer or a search box.
function isTypingTarget(el) {
  if (!el || typeof el !== "object") return false;
  if (el.isContentEditable) return true;
  const tag = (el.tagName || "").toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select";
}

// isSidebarToggleKey is whether a keydown is the rail's shortcut: the
// bare key, not typed into a field, not already handled.
export function isSidebarToggleKey(evt) {
  return (
    evt.key === SIDEBAR_TOGGLE_KEY &&
    !evt.defaultPrevented &&
    !evt.ctrlKey &&
    !evt.metaKey &&
    !evt.altKey &&
    !isTypingTarget(evt.target)
  );
}

// useSidebarCollapsed is the rail's folded state, kept in localStorage,
// and the toggle that flips it. With `shortcut` the "[" key flips it
// too; App.jsx turns that off on a phone, which has no rail to fold.
export function useSidebarCollapsed({ shortcut = true } = {}) {
  const [collapsed, setCollapsedState] = useState(readSidebarCollapsed);
  const setCollapsed = useCallback((next) => {
    setCollapsedState((prev) => {
      const value = typeof next === "function" ? next(prev) : Boolean(next);
      writeSidebarCollapsed(value);
      return value;
    });
  }, []);
  const toggle = useCallback(() => setCollapsed((c) => !c), [setCollapsed]);
  useEffect(() => {
    if (!shortcut) return undefined;
    const onKey = (evt) => {
      if (!isSidebarToggleKey(evt)) return;
      evt.preventDefault();
      toggle();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [shortcut, toggle]);
  return [collapsed, setCollapsed, toggle];
}
