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

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { Box, IconButton, Tooltip, Typography } from "@mui/material";
import MoreHorizIcon from "@mui/icons-material/MoreHoriz";
import ItemGlyph from "./ItemGlyph.jsx";
import UnbackedStateDot, { stateDot } from "./UnbackedStateDot.jsx";

// crumbSx is the one type style of the page title: every crumb and the
// slash between them share a family, size, line height, letter-spacing
// and weight, so the path reads as one line rather than several. Matches
// the old pane heading (ListHeader's h6 / .content-header h2: 1rem,
// 1.6 line-height, 600 weight). Set in full on each crumb rather than
// inherited from the rail, since a Typography with no variant brings
// body1's own size.
export const crumbSx = {
  fontFamily: (theme) => theme.typography.fontFamily,
  fontSize: "1rem",
  lineHeight: 1.6,
  letterSpacing: "normal",
  fontWeight: 600,
};

const TITLE_GLYPHS = {
  inbox: "inbox",
  tasks: "tasks",
  "new task": "tasks",
  repositories: "repos",
  repos: "repos",
  ci: "ci",
  processes: "processes",
  boards: "boards",
  sessions: "sessions",
  system: "system",
  settings: "settings",
};

function glyphForSegments(segments) {
  if (!segments || segments.length === 0) return null;
  const first = segments[0].toLowerCase();
  if (first === "sessions" && segments[1]?.toLowerCase() === "director") {
    return "director";
  }
  if (first.startsWith("task ")) return "tasks";
  return TITLE_GLYPHS[first] || null;
}

// The slot a page's own header row is drawn into. App provides it around
// the main pane only; a page rendered anywhere else -- a unit test that
// mounts it alone, a panel of a dashboard (PageHeaderScope), an overlay
// -- finds null here and keeps its header in its own body, as it did
// before there was a rail.
const PageHeaderSlot = createContext(null);

// PageHeaderScope cuts a subtree off from the rail: the panels of a
// dashboard are each a small task list with a header of their own, and
// those headers belong over their panels, not in the page's rail.
export function PageHeaderScope({ children }) {
  return (
    <PageHeaderSlot.Provider value={null}>{children}</PageHeaderSlot.Provider>
  );
}

// PageHeaderActions is how a page puts its own header row -- its count,
// buttons, search, filters, tabs -- into the rail rather than above its
// body (grain/task-4915). The same elements, portalled, so they keep
// every handler, class name and test id they had; with no rail to go to
// they render in place.
export function PageHeaderActions({ children }) {
  const slot = useContext(PageHeaderSlot);
  if (!slot) return children;
  return createPortal(children, slot);
}

// PageHeaderPageActions marks a page's own actions -- "New board", "Add
// repo", "+ New" -- inside what it puts in the rail. In the rail they
// are drawn after everything else the page put there and pushed to the
// right-hand end of the slot, just before the global buttons, whatever
// order the page's header and toolbar portalled them in
// (design/design/settings-panel.design.md §4.1). Out of the rail it is a plain
// inline row of them.
export function PageHeaderPageActions({ children }) {
  return (
    <span
      className="page-header-page-actions"
      data-testid="page-header-page-actions"
    >
      {children}
    </span>
  );
}

// useInPageHeader says whether this subtree's header row goes to a rail:
// a page's heading stays in its own body either way, inside its main
// landmark, and is only hidden from sight when the rail's title is
// already saying it on screen.
export function useInPageHeader() {
  return Boolean(useContext(PageHeaderSlot));
}

// PageHeaderProvider hands the rail's slot to everything under it. The
// rail is drawn by App, above the page, and the slot is its middle box;
// `slot` is that DOM node once it has mounted.
export function PageHeaderProvider({ slot, children }) {
  return (
    <PageHeaderSlot.Provider value={slot}>{children}</PageHeaderSlot.Provider>
  );
}

// GlobalButtons is the far-right cluster every page's rail ends in
// (design/design/settings-panel.design.md §4.1): the alerts bell, System with the
// unbacked-config-repo dot, and the Settings gear -- the only way into
// Settings. Each reads as selected while its view is showing. The test
// ids are the ones the old top bar's buttons had, so a test that clicks
// "top-bar-settings" still finds the gear.
export function GlobalButtons({
  alerts = null,
  view = "",
  config = null,
  onOpenSystem,
  onOpenSettings,
}) {
  const selectedSx = (on) => ({
    color: on ? "primary.main" : "text.secondary",
    bgcolor: on ? "action.selected" : "transparent",
  });
  return (
    <Box
      data-testid="page-header-globals"
      className="page-header-globals"
      sx={{ display: "flex", alignItems: "center", gap: 0.5, flex: "none" }}
    >
      {alerts}
      {onOpenSystem && (
        <Tooltip title="System">
          <IconButton
            size="small"
            aria-label="System"
            aria-pressed={view === "system"}
            data-testid="top-bar-system"
            onClick={onOpenSystem}
            sx={selectedSx(view === "system")}
          >
            <ItemGlyph kind="system" size={18} />
            {stateDot(config) ? (
              <UnbackedStateDot {...stateDot(config)} />
            ) : null}
          </IconButton>
        </Tooltip>
      )}
      {onOpenSettings && (
        <Tooltip title="Settings">
          <IconButton
            size="small"
            aria-label="Settings"
            aria-pressed={view === "settings"}
            data-testid="top-bar-settings"
            onClick={onOpenSettings}
            sx={selectedSx(view === "settings")}
          >
            <ItemGlyph kind="settings" size={18} />
          </IconButton>
        </Tooltip>
      )}
    </Box>
  );
}

// PageHeader is the one header rail every page in the main pane has, and
// the only one (grain/task-4915): what used to be two -- the global top
// bar over everything, "grain / <page>" with the bell, System and the
// gear, and each page's own header under it -- is this single row.
//
//   [title / crumbs]  [the page's own actions ...]  [more]  [globals]
//
// The title is always there, even on a page with no header of its own,
// a loading page or a page that threw. The middle is the slot pages
// portal into (PageHeaderActions). The globals are the bell, System and
// Settings on desktop; on a phone PhoneNav's bar already carries them,
// so App passes none.
//
// One row of one height: the slot wraps its items but is clipped to the
// first line, and when anything is clipped a "more" button opens the
// slot under the rail as a menu showing every row of it -- the same
// elements, so nothing about how they behave changes.
//
// `title` is a string ("settings / models") whose segments are split on
// " / " into crumbs, or an element drawn as it is (a pane's Back
// button). DOM order is title, page actions, globals, so Tab reaches the
// page's own controls before the buttons every page shares.
export default function PageHeader({
  title,
  icon = undefined,
  globals = null,
  onSlot = null,
  variant = "page",
  children = null,
}) {
  const slotRef = useRef(null);
  const [overflowing, setOverflowing] = useState(false);
  const [expanded, setExpanded] = useState(false);

  const setSlot = useCallback(
    (node) => {
      slotRef.current = node;
      onSlot?.(node);
    },
    [onSlot],
  );

  // Whether anything in the slot has wrapped past the first line. Asked
  // again whenever the slot or what is in it changes size -- a narrower
  // window, a page whose header grew a chip.
  useLayoutEffect(() => {
    const node = slotRef.current;
    if (!node) return undefined;
    const measure = () => {
      // Open, the slot is a menu of its own height and clips nothing,
      // which says nothing about the row it closes back into. Measuring
      // it then dropped "more" for a frame as it closed, and the focus
      // Escape hands back to it with it.
      if (node.classList.contains("page-header-slot-expanded")) return;
      const clipped = node.scrollHeight > node.clientHeight + 1;
      setOverflowing((was) => (was === clipped ? was : clipped));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return undefined;
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    const watchChildren = new MutationObserver(() => {
      for (const child of node.children) observer.observe(child);
      measure();
    });
    watchChildren.observe(node, { childList: true, subtree: true });
    for (const child of node.children) observer.observe(child);
    return () => {
      observer.disconnect();
      watchChildren.disconnect();
    };
  }, []);

  // The open menu closes the way a menu does: Escape, which hands focus
  // back to "more" so the keyboard is where it opened it, and a press
  // anywhere outside the rail. Left open, it lay over the top of the
  // page until "more" itself was found and pressed again. A press in a
  // popup the menu's own controls opened (a filter's popover, a
  // select's list) is not outside it: those are portalled out of the
  // rail, and closing under them would move their anchor. Both are
  // native listeners: what is in the slot is portalled in from the
  // page, so React hands its events to the page's tree, not this one.
  const headerRef = useRef(null);
  const moreRef = useRef(null);
  useEffect(() => {
    const header = headerRef.current;
    if (!expanded || !header) return undefined;
    const onPointerDown = (event) => {
      const target = event.target;
      if (!(target instanceof Element)) return;
      if (header.contains(target)) return;
      if (target.closest(".MuiPopover-root, .MuiModal-root, .MuiPopper-root"))
        return;
      setExpanded(false);
    };
    const onKeyDown = (event) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      event.preventDefault();
      setExpanded(false);
      moreRef.current?.focus();
    };
    document.addEventListener("pointerdown", onPointerDown);
    header.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      header.removeEventListener("keydown", onKeyDown);
    };
  }, [expanded]);

  const segments =
    typeof title === "string" ? title.split(" / ").filter(Boolean) : null;
  const resolvedIcon =
    icon !== undefined ? (
      typeof icon === "string" ? (
        <ItemGlyph kind={icon} size={20} />
      ) : (
        icon
      )
    ) : segments ? (
      (() => {
        const kind = glyphForSegments(segments);
        return kind ? <ItemGlyph kind={kind} size={20} /> : null;
      })()
    ) : null;

  return (
    <Box
      component="header"
      data-testid={variant === "pane" ? "pane-header" : "page-header"}
      className={`page-header page-header-${variant}`}
      ref={headerRef}
    >
      {resolvedIcon ? (
        <span className="header-icon">{resolvedIcon}</span>
      ) : null}
      <Box
        component="nav"
        aria-label={variant === "pane" ? "Pane" : "Page"}
        className="page-header-title"
        data-testid="page-header-title"
      >
        {segments
          ? segments.map((segment, i) => {
              const last = i === segments.length - 1;
              return (
                <Box
                  component="span"
                  key={`${i}-${segment}`}
                  sx={{ display: "inline-flex", minWidth: 0 }}
                >
                  {i > 0 && (
                    <Typography
                      component="span"
                      color="text.disabled"
                      sx={{ ...crumbSx, mx: 0.75 }}
                    >
                      /
                    </Typography>
                  )}
                  <Typography
                    component="span"
                    noWrap
                    data-testid={last ? "top-bar-page" : undefined}
                    aria-current={last ? "page" : undefined}
                    sx={{ ...crumbSx, minWidth: 0 }}
                  >
                    {segment}
                  </Typography>
                </Box>
              );
            })
          : title}
      </Box>
      <div
        ref={setSlot}
        data-testid="page-header-actions"
        className={`page-header-slot${expanded ? " page-header-slot-expanded" : ""}`}
      >
        {children}
      </div>
      {(overflowing || expanded) && (
        <Tooltip title={expanded ? "Fewer" : "More"}>
          <IconButton
            size="small"
            ref={moreRef}
            aria-label="More page actions"
            aria-expanded={expanded}
            data-testid="page-header-more"
            onClick={() => setExpanded((e) => !e)}
            sx={{
              flex: "none",
              color: "text.secondary",
              ml: expanded ? "auto" : undefined,
            }}
          >
            <MoreHorizIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      )}
      {globals}
    </Box>
  );
}
