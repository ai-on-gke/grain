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

import { Children, createContext, useContext, useRef, useState } from "react";
import {
  Box,
  Button,
  ButtonGroup,
  Chip,
  Divider,
  IconButton,
  List,
  ListItemButton as MuiListItemButton,
  ListItemIcon,
  ListItemText,
  Menu,
  MenuItem,
  Tooltip,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import ArrowDropDownIcon from "@mui/icons-material/ArrowDropDown";
import ChevronLeftIcon from "@mui/icons-material/ChevronLeft";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import TuneIcon from "@mui/icons-material/Tune";
import PushPinIcon from "@mui/icons-material/PushPin";
import PushPinOutlinedIcon from "@mui/icons-material/PushPinOutlined";
import WarningAmberIcon from "@mui/icons-material/WarningAmber";
import {
  DISPLAY_STATE_ORDER,
  INBOX_STATES,
  STATE_LABELS,
  displayState,
  inboxTasks,
  isTaskMerged,
  repoRows,
} from "../state.js";
import { SIDEBAR_COLLAPSED_WIDTH, SIDEBAR_WIDTH } from "../theme.js";
import { SIDEBAR_TOGGLE_KEY } from "../sidebarCollapse.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { usePinnedNav } from "../PinnedNavContext.jsx";
import { getPinnedItemInfo, samePin } from "../pinnedNav.js";
import { useHiddenSections } from "../sidebarSections.js";
import {
  STAGING_STATE_COLORS,
  stagingLink,
  stagingSandboxes,
  stagingState,
  stagingTitle,
} from "../staging.js";
import { formatDateTime } from "../time.js";
import CustomizeSidebarOverlay from "./CustomizeSidebarOverlay.jsx";
import { NEW_SESSION_ID } from "./NewSessionPage.jsx";
import GrainMark from "./GrainMark.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import UnbackedStateDot, { stateDot } from "./UnbackedStateDot.jsx";

// Sidebar replaces TopBar and Filters with the one nav Plane builds every
// view around: a fixed rail with the workspace identity up top, a state
// list styled like Plane's own status groups (a dot standing in for the
// state's badge color, a count on the right), and the deployment-level
// actions (settings) pinned to the bottom. Schedules and Releases
// live in their own nav pane / the repo pane (bwsalmon/agents#455,
// bwsalmon/agents#398), rather than their own buttons here.
//
// Upgrade lives as a section of Settings (/settings/upgrade), as
// Secrets did until grain/task-110 broke it up: every secret is now set
// from whatever uses it -- the agent credentials on Settings' Agents
// tab, a capability's own on its row on the Capabilities tab.
// Logs, Sandbox health and the reboot control (previously their own nav
// entries/a "danger zone" on the general tab -- bwsalmon/agents#457,
// bwsalmon/agents#536, bwsalmon/agents#395) went through Settings' own
// Debug tab for a while too (bwsalmon/agents#623), but moved back out
// to an entry of its own here, under Settings (bwsalmon/agents#640) --
// diagnosing a deployment gone wrong wants faster reach than a tab
// buried inside Settings' configuration form. That entry is called
// "System" (grain/task-12): it is the machine this deployment runs on,
// which an operator looks at to see what it is doing and not only when
// they have already decided something is broken.
//
// Metrics (GET /api/metrics) was a fourth tab in there and is a third
// footer entry now (grain/task-173): the throughput and latency report
// is the standing question "how is this deployment doing", asked when
// nothing is wrong at all, and it was the only thing behind that pane's
// tab strip that was not about something being wrong right now. Two
// clicks and a tab strip is the wrong price for a report somebody reads
// weekly, and burying it under the word "debug" -- what that pane was
// called then -- said the wrong thing about when to open it.
//
// All three footer entries open a full pane beside this rail rather
// than a dialog over the middle of the screen (grain/task-115), which is
// why they are nav entries carrying a selected state now rather than
// plain buttons: the rail stays visible under the pane, so it has to say
// which of the three is covering everything else.
// compactCount is a rail count that may run to five or six figures,
// written the way somebody says one out loud: 940, 1.2k, 24k, 1.4M.
//
// Only the Proxy cache entry needs it -- a cache holding tens of
// thousands of packages is ordinary, and the exact figure is neither
// something anybody acts on nor something that fits beside a label in
// 232px of rail. The counts above it are tasks, repos and sandboxes,
// which are things an operator deals with one at a time and which are
// written out in full for that reason; abbreviating "17" would only
// make it harder to read.
//
// A decimal below ten thousand and none above it: "2.5k" is a figure
// somebody reads at a glance, "24.3k" is three digits of noise on a
// number that moves every time a run installs anything.
export function compactCount(n) {
  if (typeof n !== "number" || !Number.isFinite(n) || n < 0) return "";
  const count = Math.round(n);
  if (count < 1000) return String(count);
  const abbreviate = (value, suffix) =>
    `${value < 10 ? Math.round(value * 10) / 10 : Math.round(value)}${suffix}`;
  // 999,500 and up round to a whole million: "1000k" is what a reader
  // would take for a bug.
  if (count >= 999500) return abbreviate(count / 1e6, "M");
  return abbreviate(count / 1000, "k");
}

// parseStampTime turns GET /api/config's committedAt into a Date, or
// null for a build whose stamp carried no readable time -- the server
// leaves the field off entirely in that case (pkg/ui/version.go), and a
// value that will not parse is treated the same way rather than printed
// as "Invalid Date".
function parseStampTime(iso) {
  if (!iso) return null;
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? null : at;
}

// buildStampLabel is the one line the footer prints: the release version
// (when present), the seven-character commit, and, after it, when that commit
// was made.
//
// UTC, to the minute. An operator comparing what a deployment is running
// against `git log` is comparing against commit times, which are the
// same everywhere -- and a stamp rendered in the reader's own zone would
// say something different in every browser looking at the same
// deployment. The tooltip gives the reading on the deployment's own
// clock (grain/task-368) for whoever wants a time they can compare with
// the rest of the UI.
//
// "-dirty" borrows git's own word for it (`git describe --dirty`): the
// build ran against uncommitted changes, so the hash beside it does not
// fully describe what is running. Never true of a CI build.
function buildStampLabel(version) {
  const parts = [];
  if (version.release) {
    parts.push(version.release);
  }
  if (version.edition) {
    parts.push(version.edition);
  }
  if (version.commit) {
    parts.push(version.commit.slice(0, 7) + (version.modified ? "-dirty" : ""));
  }
  const at = parseStampTime(version.committedAt);
  if (at) {
    parts.push(`${at.toISOString().slice(0, 16).replace("T", " ")}Z`);
  }
  return parts.join(" · ");
}

function buildStampTitle(version, zone) {
  const parts = [];
  if (version.commit) {
    parts.push(`Running commit ${version.commit}`);
  }
  if (version.release) {
    parts.push(`release ${version.release}`);
  }
  if (version.edition) {
    parts.push(`${version.edition} edition`);
  }
  const at = parseStampTime(version.committedAt);
  if (at) parts.push(`committed ${formatDateTime(at, zone)}`);
  if (version.modified)
    parts.push("built from a tree with uncommitted changes");
  return parts.join(", ");
}

// ListItemButton is every entry in the rail. MUI's `selected` only
// paints the entry; it says nothing to a screen reader, which heard the
// rail as twenty identical buttons with no way to tell which page was
// open. The entry that is selected is the page on screen, so it says so
// as `aria-current="page"` -- the same thing the page header rail's
// title says for the page it names (PageHeader.jsx).
//
// Folded down to its icons (`collapsed`, sidebarCollapse.js) the entry
// is the same button: its label is still inside it, hidden from sight
// rather than from a screen reader (style.css's .sidebar-collapsed), so
// its accessible name does not change with the rail's width. What the
// eye loses is put back as a tooltip to the right, on hover and on
// keyboard focus alike. The label is the entry's own ListItemText
// `primary` unless `label` says otherwise, and a `title` the entry
// already carried -- the waiting entries' explanation, a staging
// environment's address -- joins it there rather than standing beside
// it as a second, native tooltip.
const RailCollapsedContext = createContext(false);

function entryLabel(children) {
  const text = Children.toArray(children).find(
    (child) => child?.type === ListItemText,
  );
  const primary = text?.props?.primary;
  return typeof primary === "string" ? primary : "";
}

function ListItemButton({ selected, label, title, children, ...props }) {
  const collapsed = useContext(RailCollapsedContext);
  const name = label || entryLabel(children);
  const button = (
    <MuiListItemButton
      selected={selected}
      aria-current={selected ? "page" : undefined}
      title={collapsed && name ? undefined : title}
      {...props}
    >
      {children}
    </MuiListItemButton>
  );
  if (!collapsed || !name) return button;
  return (
    <Tooltip
      title={title ? `${name} -- ${title}` : name}
      placement="right"
      describeChild
    >
      {button}
    </Tooltip>
  );
}

// RailCount is the figure on the right-hand edge of an entry. Folded
// down to icons it is the badge on the icon's corner instead
// (style.css's .rail-count), and a zero is left off there: a badge on
// every icon reading "0" would hide the one that does not.
function RailCount({ value, color = "text.secondary", title, sx }) {
  return (
    <Typography
      variant="caption"
      color={color}
      title={title}
      className="rail-count"
      data-zero={!value || value === "0" ? "true" : undefined}
      sx={sx}
    >
      {value}
    </Typography>
  );
}

// FooterItem is one of the three destinations at the foot of the rail:
// its figure, its label, and whatever the entry hangs on the right-hand
// edge. The colour rule sits on the button rather than on the label, and
// the label inherits it: the glyph is drawn in `currentColor`
// (ItemGlyph.jsx), so figure and word brighten together when the pane is
// open, and there is one rule keeping them agreeing rather than two.
//
// Out here rather than inside Sidebar because a component declared in a
// render body is a new component type on every render, so React tears
// the entry down and builds it again each time the rail re-renders --
// which a click landing mid-poll can fall through.
function FooterItem({ kind, label, active, onClick, children }) {
  return (
    <ListItemButton
      selected={active}
      onClick={onClick}
      sx={{
        borderRadius: 1.5,
        py: 0.6,
        px: 0.9,
        color: active ? "text.primary" : "text.secondary",
      }}
    >
      <span className="nav-icon">
        <ItemGlyph kind={kind} />
      </span>
      <ListItemText
        primary={label}
        sx={{ ml: 1 }}
        primaryTypographyProps={{
          fontSize: "0.85rem",
          fontWeight: 500,
          color: "inherit",
        }}
      />
      {children}
    </ListItemButton>
  );
}

// NavItem is one state's entry under Tasks in the rail, out here for the
// reason FooterItem and BoardItem are. It used to be declared inside
// Sidebar's render body, which made it a new component type on every
// render of the rail: anything that re-rendered the rail -- a poll
// landing, the clock ticking -- unmounted and remounted every state
// entry, and a click that arrived between an entry being drawn and
// being pressed landed on a node no longer in the document and did
// nothing (grain/task-3942).
//
// `glyph` is for the one entry here that is a kind rather than a
// state: All tasks, which stands for the whole list the entries under
// it are slices of, and which had nothing in the icon column but the
// invisible spacer (grain/task-372). The rest keep their dot, because
// its colour is the state and no figure would say that.
//
// `waiting` is the other one-word difference on this list, and it is
// about the reader rather than about the entry: a state whose tasks
// are waiting on *them* is not the same kind of thing as a state grain
// is working through, and until grain/task-296 the rail spelled the
// two identically (state.js's INBOX_STATES -- "Awaiting reply",
// "Awaiting submit", "Failed", "Proposed"). It draws
// the count in the accent the inbox chip above already uses for the
// same fact, and says on the hover what the number means: a task in
// one of these states does not move again until somebody moves it, and
// a column that made "Awaiting submit" look like "Running" is how a
// finished change sat unmerged for an afternoon while the queue
// repaired four pull requests it could not fix.
function NavItem({
  id,
  label,
  dotClass,
  glyph,
  count,
  active,
  waiting,
  onSelect,
}) {
  return (
    <ListItemButton
      selected={active}
      onClick={() => onSelect(id)}
      title={
        waiting
          ? `${count} ${count === 1 ? "task is" : "tasks are"} waiting on you here -- nothing moves them until you do. The inbox above offers what to do about each.`
          : undefined
      }
      sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
    >
      <span className="nav-icon">
        {glyph ? (
          <ItemGlyph kind={glyph} />
        ) : (
          <span className={`dot ${dotClass}`} />
        )}
      </span>
      <ListItemText
        primary={label}
        sx={{ ml: 1 }}
        primaryTypographyProps={{
          noWrap: true,
          fontSize: "0.85rem",
          fontWeight: 500,
        }}
      />
      <RailCount
        value={count}
        color={active || waiting ? "primary" : "text.secondary"}
        sx={{
          fontVariantNumeric: "tabular-nums",
          fontWeight: waiting ? 600 : undefined,
        }}
      />
    </ListItemButton>
  );
}

// StagingItem is one staging environment in the rail -- a persistent
// sandbox exposing a port (staging.js) -- under Staging, or under Pinned
// once pinned. It is a link rather than a view: a staging environment is
// somebody's build of an app, not a grain page, so it opens in a tab of
// its own, and a modified click does whatever the browser does with one.
// The dot is the sandbox's state; a host port this page's proxy will not
// carry is drawn with a warning, and the hover says why and what to do.
// The pin beside it is a sibling of the link, not inside it, so pinning
// never also opens the environment. A pinned one is listed under Pinned
// as well as under Staging, the way a pinned view is.
function StagingItem({ sandbox, config, pinned, onTogglePin }) {
  const link = stagingLink(sandbox, config);
  const state = stagingState(sandbox);
  const title = stagingTitle(sandbox, link);
  return (
    <Box
      data-testid={`staging-item-${sandbox.name}`}
      data-state={state}
      sx={{
        display: "flex",
        alignItems: "center",
        "& .staging-pin": { opacity: pinned ? 0.8 : 0 },
        "&:hover .staging-pin, & .staging-pin:focus-visible": { opacity: 1 },
      }}
    >
      <ListItemButton
        component="a"
        href={link.href}
        target="_blank"
        rel="noopener noreferrer"
        title={title}
        sx={{
          borderRadius: 1.5,
          py: 0.6,
          px: 0.9,
          flex: "1 1 auto",
          minWidth: 0,
        }}
      >
        <span className="nav-icon">
          <Box
            component="span"
            className="dot"
            role="img"
            aria-label={state}
            sx={{ bgcolor: STAGING_STATE_COLORS[state] }}
          />
        </span>
        <ListItemText
          primary={sandbox.name}
          secondary={link.warning ? "not reachable through this proxy" : null}
          sx={{ ml: 1, my: 0 }}
          primaryTypographyProps={{
            noWrap: true,
            fontSize: "0.85rem",
            fontWeight: 500,
          }}
          secondaryTypographyProps={{
            noWrap: true,
            fontSize: "0.68rem",
            color: "warning.main",
          }}
        />
        {link.warning ? (
          <WarningAmberIcon
            color="warning"
            className="rail-count"
            aria-label="Host port not reachable through this proxy"
            sx={{ fontSize: "0.95rem", ml: 0.5 }}
          />
        ) : null}
      </ListItemButton>
      <IconButton
        className="staging-pin rail-hidden"
        size="small"
        aria-label={`${pinned ? "Unpin" : "Pin"} ${sandbox.name}`}
        aria-pressed={pinned}
        title={pinned ? "Unpin" : "Pin to the top of the rail"}
        onClick={onTogglePin}
        sx={{ p: 0.4, flex: "none" }}
      >
        {pinned ? (
          <PushPinIcon sx={{ fontSize: "0.9rem" }} />
        ) : (
          <PushPinOutlinedIcon sx={{ fontSize: "0.9rem" }} />
        )}
      </IconButton>
    </Box>
  );
}

export default function Sidebar({
  config,
  tasks,
  notificationCount = 0,
  schedules = [],
  templates = [],
  suites = [],
  processTriggers = [],
  ciRuns = [],
  boards = [],
  openBoardId = null,
  onOpenBoard,
  // The standing sessions this deployment keeps open -- the director
  // among them -- straight off GET /api/console-sessions. Defaulted so
  // every existing test that renders a rail without them gets the rail
  // it had before.
  sessions = [],
  // The persistent sandboxes off GET /api/persistent-sandboxes; the ones
  // exposing a port are the rail's Staging group (staging.js).
  persistentSandboxes = [],
  openSessionId = null,
  onOpenSession,
  onNewSession,
  view,
  systemTab = "",
  settingsSection = "",
  onSetView,
  stateFilter,
  onSetFilter,
  onNewBoard,
  // System's entry is the page header rail's icon beside the gear; on a
  // phone, where PhoneNav's bar has room for the gear alone, it is a row at the foot
  // of this rail instead (docs/design/settings-panel.md §4.3).
  onOpenSystem,
  // The build stamp at the foot is a link to Settings -> Upgrade.
  onOpenUpgrade,
  onOpenNewTask,
  pinnedItems: propPinnedItems,
  onOpenCustomize: propOnOpenCustomize,
  // Whether the footer (the build, and System on a phone) is pinned
  // under a list that scrolls on its own, rather than scrolled to along
  // with everything else. Beside the page the rail scrolls as one pane
  // (ba2bc7a66 took the "pane within a pane" out of it); in the phone
  // drawer (PhoneNav.jsx) the footer is pinned, because the drawer is
  // opened to go somewhere and the foot of the rail is a destination
  // as much as the list is -- left to the single pane it opened below
  // the fold at 375x812.
  pinFooter = false,
  // Folded down to an icon-only rail (sidebarCollapse.js), and the
  // toggle that folds it. Without onToggleCollapsed there is no toggle
  // at all -- the phone drawer, where the rail is never folded.
  collapsed: collapsedProp = false,
  onToggleCollapsed,
}) {
  const collapsed = Boolean(collapsedProp && onToggleCollapsed);
  const [createMenuAnchor, setCreateMenuAnchor] = useState(null);
  const createMenuOpen = Boolean(createMenuAnchor);
  const zone = useTimeZone();
  const {
    pinnedItems: contextPinnedItems,
    addPinnedItem,
    removePinnedItem,
  } = usePinnedNav();
  const pinnedItems =
    propPinnedItems !== undefined ? propPinnedItems : contextPinnedItems;
  const [showCustomize, setShowCustomize] = useState(false);
  const onOpenCustomize = propOnOpenCustomize || (() => setShowCustomize(true));
  // The sections this browser hides (sidebarSections.js). A hidden one
  // loses its row, and with it its rail count, folded or not; its route
  // still opens by URL, and a pin of it still shows under Pinned.
  const hiddenSections = useHiddenSections();
  const shows = (id) => !hiddenSections.includes(id);
  const visibleSessions = Array.isArray(sessions) ? sessions : [];
  const staging = stagingSandboxes(persistentSandboxes);
  const stagingPinned = (name) =>
    pinnedItems.some((p) => samePin(p, { kind: "staging", id: name }));
  const toggleStagingPin = (name) => {
    const pin = { kind: "staging", id: name };
    if (stagingPinned(name)) removePinnedItem(pin);
    else addPinnedItem(pin);
  };
  // The Sessions entry is lit for the list, for the session view with
  // none named, and for the chat that has no session yet (/session/new,
  // NewSessionPage.jsx): all three are "in Sessions" to the reader.
  const sessionsSelected =
    view === "sessions" ||
    (view === "session" &&
      (!openSessionId || openSessionId === NEW_SESSION_ID));
  const visibleSchedules = Array.isArray(schedules) ? schedules : [];
  const visibleTemplates = Array.isArray(templates) ? templates : [];
  const visibleSuites = Array.isArray(suites) ? suites : [];
  // Every row the Processes page lists (§4.2): its schedules, templates,
  // suites and the triggers it has read so far.
  const processCount =
    visibleSchedules.length +
    visibleTemplates.length +
    visibleSuites.length +
    (Array.isArray(processTriggers) ? processTriggers.length : 0);
  const visibleCIRuns = Array.isArray(ciRuns) ? ciRuns : [];
  const visibleBoards = Array.isArray(boards) ? boards : [];
  const asideRef = useRef(null);
  const tasksRef = useRef(null);
  const counts = {};
  let blocked = 0;
  let merged = 0;
  // How many tasks the Tasks entry says there are. A session's task is
  // left out of this total for the reason it is left out of the state
  // counts below: the list the entry opens does not show it
  // (taskFilters.js's isSessionTask), so a total that counted it would
  // promise a row the reader cannot find or filter for. Sessions have
  // their own count, beside the Sessions entry.
  let taskCount = 0;
  for (const t of tasks) {
    // A session's task sits in awaiting_reply between every exchange;
    // counted here it would be a permanent waiting entry per session, and
    // the list each entry opens leaves it out (taskFilters.js's
    // isSessionTask). Sessions are counted under Sessions below.
    if (t.consoleSession || t.interactive) continue;
    taskCount += 1;
    if (isTaskMerged(t)) {
      merged += 1;
    } else {
      // Both asked states are one entry, "Asked" (state.js's
      // displayState), which opens ?state=asked over both of them.
      const s = displayState(t.state);
      counts[s] = (counts[s] || 0) + 1;
    }
    if (t.blocked) blocked += 1;
  }
  const repoCount = repoRows(config, tasks).length;
  // time.Time's zero value is still a non-empty string on the wire, so
  // FinishedAt cannot distinguish a live run here. Conclusion is CI's
  // canonical completion signal: its zero value means the run is pending.
  const runningCICount = visibleCIRuns.filter((run) => !run.conclusion).length;
  // null where the deployment runs no proxy cache at all: the field is
  // absent then, and an entry with no number says that better than a
  // "0" claiming an empty cache would (pkg/ui, configResponse).
  const cachedPackages =
    typeof config?.packageCacheEntries === "number"
      ? config.packageCacheEntries
      : null;
  // The same null-means-no-cache reading one entry down, for the other
  // cache on the controller's disk (configResponse.actionsCacheEntries).
  const cachedActions =
    typeof config?.actionsCacheEntries === "number"
      ? config.actionsCacheEntries
      : null;
  const totalCached =
    cachedPackages !== null || cachedActions !== null
      ? (cachedPackages || 0) + (cachedActions || 0)
      : null;
  const cachesTitle =
    cachedPackages !== null && cachedActions !== null
      ? `${totalCached.toLocaleString()} cached items (${cachedPackages.toLocaleString()} packages, ${cachedActions.toLocaleString()} Actions cache entries)`
      : cachedPackages !== null
        ? `${cachedPackages.toLocaleString()} packages cached, Docker Hub layers included`
        : cachedActions !== null
          ? `${cachedActions.toLocaleString()} cache entries this deployment's CI jobs have left for each other`
          : undefined;
  // What the inbox holds: actionable tasks waiting on the reader
  // (state.js's inboxCount), the open operator notifications listed
  // above them, and the config repository's sync failure, which the
  // Inbox lists as a row of its own and counts in its heading
  // (InboxPage.jsx's StateSyncGroup). Without it a deployment whose
  // only trouble was that sync showed a bare Inbox entry here while
  // the page it opens said 1 (2026-10-29 intake walkthrough).
  const waitingTasks = inboxTasks(tasks).length;
  const syncFailing = config?.stateRepoSyncFailure ? 1 : 0;
  const inboxCount = waitingTasks + notificationCount + syncFailing;
  const inboxTitle = [
    waitingTasks > 0
      ? `${waitingTasks} task${waitingTasks === 1 ? "" : "s"} waiting on you`
      : "",
    notificationCount > 0
      ? `${notificationCount} notification${notificationCount === 1 ? "" : "s"}`
      : "",
    syncFailing ? "settings are not being exported" : "",
  ]
    .filter(Boolean)
    .join(", ");

  // Picking a state filter always lands on the task list -- there is
  // nothing for it to scope on the repo page.
  const selectState = (id) => {
    if (onSetView("tasks") === false) return;
    onSetFilter(id);
  };

  const toggleLabel = collapsed ? "Expand sidebar" : "Collapse sidebar";
  const toggle = onToggleCollapsed ? (
    <Tooltip
      title={`${toggleLabel} (${SIDEBAR_TOGGLE_KEY})`}
      placement="right"
      describeChild
    >
      <IconButton
        size="small"
        aria-label={toggleLabel}
        aria-expanded={!collapsed}
        aria-controls="sidebar-rail"
        aria-keyshortcuts={SIDEBAR_TOGGLE_KEY}
        data-testid="sidebar-toggle"
        onClick={onToggleCollapsed}
        sx={{ ml: collapsed ? 0 : "auto", flex: "none", p: 0.5 }}
      >
        {collapsed ? (
          <ChevronRightIcon fontSize="small" />
        ) : (
          <ChevronLeftIcon fontSize="small" />
        )}
      </IconButton>
    </Tooltip>
  ) : null;

  const aside = (
    <Box
      component="aside"
      id="sidebar-rail"
      ref={asideRef}
      className={collapsed ? "sidebar sidebar-collapsed" : "sidebar"}
      data-collapsed={collapsed ? "true" : "false"}
      sx={{
        width: collapsed ? SIDEBAR_COLLAPSED_WIDTH : SIDEBAR_WIDTH,
        flex: "none",
        display: "flex",
        flexDirection: "column",
        minHeight: 0,
        overflowY: "auto",
        // The width animates (style.css's .sidebar); nothing inside it
        // may push it wider or show a scrollbar while it does.
        overflowX: "hidden",
        gap: 1.4,
        p: collapsed ? "0.9rem 0.5rem" : "0.9rem 0.7rem",
        bgcolor: "background.paper",
        borderRight: 1,
        borderColor: "divider",
      }}
    >
      {/* The mark beside the wordmark is the one place the whole
        deployment's state shows as something other than a number: it
        sits still while nothing is running and its grains scatter and
        re-form while agents are working, so a glance at the sidebar
        says whether the machine is busy without reading the counts
        below it. See docs/brand.md. */}
      <Box
        sx={{
          display: "flex",
          flexDirection: collapsed ? "column" : "row",
          alignItems: "center",
          gap: 0.9,
          px: collapsed ? 0 : 0.5,
        }}
      >
        {/* Folded, the environment's name is too wide for the rail; the
          mark carries it as a warning dot instead, and says it on the
          hover, so the two tabs still do not look alike. */}
        <Box
          component="span"
          title={
            collapsed && config?.environmentName
              ? `Environment: ${config.environmentName}`
              : undefined
          }
          data-testid={
            collapsed && config?.environmentName
              ? "sidebar-environment-dot"
              : undefined
          }
          sx={{
            display: "inline-flex",
            position: "relative",
            "&::after":
              collapsed && config?.environmentName
                ? {
                    content: '""',
                    position: "absolute",
                    top: -1,
                    right: -1,
                    width: 9,
                    height: 9,
                    borderRadius: "50%",
                    bgcolor: "warning.main",
                    border: 2,
                    borderColor: "background.paper",
                  }
                : undefined,
          }}
        >
          <GrainMark size={32} animated={(counts.running || 0) > 0} />
        </Box>
        <Typography
          variant="subtitle1"
          fontWeight={600}
          letterSpacing="-0.01em"
          component="h1"
          className={collapsed ? "visually-hidden" : undefined}
          sx={{ m: 0 }}
        >
          grain
        </Typography>
        {/* The deployment's own name (grain/task-69), next to the
          wordmark because that is the one piece of chrome on screen in
          every view: an operator with a staging tab and a production
          tab open is one click from approving or merging on the wrong
          one, and the two are otherwise pixel-identical. Warning
          colour rather than the brand accent for the same reason --
          it should not blend into the mark beside it. Absent entirely
          when nothing is configured, which is grain's own shape for a
          single deployment with nothing to be told apart from. App.jsx
          puts the same name in the browser tab's title, for the tab
          strip this box cannot reach. */}
        {config?.environmentName && !collapsed ? (
          <Chip
            label={config.environmentName}
            size="small"
            color="warning"
            title={`Environment: ${config.environmentName}`}
            sx={{
              ml: "auto",
              maxWidth: 110,
              fontWeight: 600,
            }}
          />
        ) : null}
        {toggle}
      </Box>

      {collapsed ? (
        <Tooltip title="New task, session or board" placement="right">
          <Button
            variant="contained"
            size="small"
            aria-label="Create"
            aria-controls={createMenuOpen ? "create-options-menu" : undefined}
            aria-haspopup="true"
            aria-expanded={createMenuOpen ? "true" : undefined}
            onClick={(e) => setCreateMenuAnchor(e.currentTarget)}
            sx={{ minWidth: 0, px: 0, py: 0.5, my: 0.4 }}
          >
            <AddIcon fontSize="small" />
          </Button>
        </Tooltip>
      ) : (
        <ButtonGroup
          size="small"
          variant="contained"
          fullWidth
          sx={{ my: 0.4 }}
        >
          <Button
            onClick={onOpenNewTask}
            sx={{ flex: "1 1 auto", minWidth: 0, whiteSpace: "nowrap" }}
          >
            + New task
          </Button>
          <Button
            aria-label="Create options"
            aria-controls={createMenuOpen ? "create-options-menu" : undefined}
            aria-haspopup="true"
            aria-expanded={createMenuOpen ? "true" : undefined}
            onClick={(e) => setCreateMenuAnchor(e.currentTarget)}
            sx={{
              px: 0.5,
              width: "32px",
              minWidth: "32px",
              flex: "0 0 32px",
            }}
          >
            <ArrowDropDownIcon fontSize="small" />
          </Button>
        </ButtonGroup>
      )}

      <Menu
        id="create-options-menu"
        anchorEl={createMenuAnchor}
        open={createMenuOpen}
        onClose={() => setCreateMenuAnchor(null)}
        // Folded, the menu opens beside the rail's "+" rather than
        // under it, where the rail is too narrow to hold it.
        anchorOrigin={{
          vertical: collapsed ? "top" : "bottom",
          horizontal: "right",
        }}
        transformOrigin={{
          vertical: "top",
          horizontal: collapsed ? "left" : "right",
        }}
      >
        <MenuItem
          onClick={() => {
            setCreateMenuAnchor(null);
            onOpenNewTask?.();
          }}
        >
          <ListItemIcon sx={{ minWidth: 28 }}>
            <ItemGlyph kind="tasks" />
          </ListItemIcon>
          <ListItemText>New task</ListItemText>
        </MenuItem>
        <MenuItem
          onClick={() => {
            setCreateMenuAnchor(null);
            onNewSession?.();
          }}
        >
          <ListItemIcon sx={{ minWidth: 28 }}>
            <ItemGlyph kind="sessions" />
          </ListItemIcon>
          <ListItemText>New session</ListItemText>
        </MenuItem>
        {onNewBoard && (
          <MenuItem
            onClick={() => {
              setCreateMenuAnchor(null);
              onNewBoard();
            }}
          >
            <ListItemIcon sx={{ minWidth: 28 }}>
              <ItemGlyph kind="boards" />
            </ListItemIcon>
            <ListItemText>New board</ListItemText>
          </MenuItem>
        )}
      </Menu>

      <List
        component="nav"
        disablePadding
        sx={{
          display: "flex",
          flexDirection: "column",
          gap: 0.2,
          // The floor keeps a usable slice of the list on a drawer too
          // short for it and the footer both (a phone on its side): past
          // it, the <aside> overflows and scrolls as a whole again.
          ...(pinFooter
            ? { flex: "1 1 auto", minHeight: 120, overflowY: "auto" }
            : {}),
        }}
      >
        {/* Pinned section: this browser's customizable navigation items.
          Its heading row also carries the panel's one Customize control,
          which stays when Pinned itself is hidden so the panel can always
          be customized back. */}
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            justifyContent: collapsed ? "center" : undefined,
            pl: collapsed ? 0 : 0.9,
            pr: collapsed ? 0 : 0.4,
            mt: 0.2,
          }}
        >
          {shows("pinned") && (
            <Typography
              variant="caption"
              color="text.secondary"
              className="sidebar-section-heading rail-hidden"
            >
              Pinned
            </Typography>
          )}
          {collapsed ? (
            <Tooltip title="Customize sidebar" placement="right">
              <IconButton
                size="small"
                onClick={onOpenCustomize}
                aria-label="Customize sidebar"
                sx={{ p: 0.5 }}
              >
                <TuneIcon sx={{ fontSize: "1rem" }} />
              </IconButton>
            </Tooltip>
          ) : (
            <Button
              size="small"
              onClick={onOpenCustomize}
              sx={{
                ml: "auto",
                minWidth: 0,
                px: 0.8,
                fontSize: "0.75rem",
              }}
              title="Customize sidebar"
              aria-label="Customize sidebar"
            >
              Customize
            </Button>
          )}
        </Box>

        {(shows("pinned") ? pinnedItems : []).map((item, idx) => {
          const glyph = item.icon || item.glyph;
          const color = item.color;
          const iconStyle = color ? { color } : undefined;

          if (item.kind === "view") {
            if (item.id === "inbox") {
              return (
                <ListItemButton
                  key={`pinned-view-inbox-${idx}`}
                  selected={view === "inbox"}
                  onClick={() => onSetView("inbox")}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "inbox"} />
                  </span>
                  <ListItemText
                    primary="Inbox"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  {inboxCount > 0 && (
                    <Chip
                      label={inboxCount}
                      size="small"
                      color="primary"
                      title={inboxTitle}
                      className="rail-count"
                      sx={{
                        height: 18,
                        fontSize: "0.7rem",
                        fontWeight: 600,
                        "& .MuiChip-label": { px: 0.7 },
                      }}
                    />
                  )}
                </ListItemButton>
              );
            }
            if (item.id === "tasks") {
              return (
                <ListItemButton
                  key={`pinned-view-tasks-${idx}`}
                  selected={view === "tasks" && stateFilter === "all"}
                  onClick={() => {
                    if (onSetView("tasks") === false) return;
                    onSetFilter("all");
                  }}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "tasks"} />
                  </span>
                  <ListItemText
                    primary="Tasks"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      noWrap: true,
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  <RailCount
                    value={taskCount}
                    color={
                      view === "tasks" && stateFilter === "all"
                        ? "primary"
                        : "text.secondary"
                    }
                  />
                </ListItemButton>
              );
            }
            if (item.id === "repos") {
              return (
                <ListItemButton
                  key={`pinned-view-repos-${idx}`}
                  selected={view === "repos"}
                  onClick={() => onSetView("repos")}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "repos"} />
                  </span>
                  <ListItemText
                    primary="Repos"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  <RailCount value={repoCount} />
                </ListItemButton>
              );
            }
            if (item.id === "ci") {
              return (
                <ListItemButton
                  key={`pinned-view-ci-${idx}`}
                  selected={view === "ci"}
                  onClick={() => onSetView("ci")}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "ci"} />
                  </span>
                  <ListItemText
                    primary="CI"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  <RailCount value={runningCICount} />
                </ListItemButton>
              );
            }
            // A pin of a System tab or a Settings section
            // (docs/design/settings-panel.md §5.6) -- or of the bare
            // view. The old Sandboxes and Caches pins became the System
            // tabs of the same names, and keep their counts.
            if (item.id === "system" || item.id === "settings") {
              const info = getPinnedItemInfo(item);
              const sub =
                item.id === "system" ? item.tab || "" : item.section || "";
              const openTab = systemTab || "health";
              const openSection = settingsSection || "general";
              const here =
                item.id === "system"
                  ? view === "system" &&
                    (sub
                      ? openTab === sub
                      : !pinnedItems.some(
                          (p) =>
                            p.kind === "view" &&
                            p.id === "system" &&
                            p.tab &&
                            p.tab === openTab,
                        ))
                  : view === "settings" &&
                    (sub
                      ? openSection === sub
                      : !pinnedItems.some(
                          (p) =>
                            p.kind === "view" &&
                            p.id === "settings" &&
                            p.section &&
                            p.section === openSection,
                        ));
              let count = null;
              if (item.id === "system" && sub === "sandboxes") {
                count = <RailCount value={config?.sandboxCount || 0} />;
              } else if (
                item.id === "system" &&
                sub === "caches" &&
                totalCached !== null
              ) {
                count = (
                  <RailCount
                    value={compactCount(totalCached)}
                    title={cachesTitle}
                  />
                );
              }
              return (
                <ListItemButton
                  key={`pinned-view-${item.id}-${sub}-${idx}`}
                  selected={here}
                  onClick={() =>
                    onSetView(
                      item.id,
                      item.id === "system"
                        ? { systemTab: sub }
                        : { settingsSection: sub },
                    )
                  }
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={info.glyph} />
                  </span>
                  <ListItemText
                    primary={info.label}
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  {count}
                </ListItemButton>
              );
            }
            if (item.id === "processes") {
              return (
                <ListItemButton
                  key={`pinned-view-processes-${idx}`}
                  selected={view === "processes"}
                  onClick={() => onSetView("processes")}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "processes"} />
                  </span>
                  <ListItemText
                    primary="Processes"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  <RailCount value={processCount} />
                </ListItemButton>
              );
            }
            if (item.id === "boards") {
              return (
                <ListItemButton
                  key={`pinned-view-boards-${idx}`}
                  selected={view === "boards" && !openBoardId}
                  onClick={() => onSetView("boards")}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "boards"} />
                  </span>
                  <ListItemText
                    primary="Boards"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  <RailCount value={visibleBoards.length} />
                </ListItemButton>
              );
            }
            if (item.id === "sessions") {
              return (
                <ListItemButton
                  key={`pinned-view-sessions-${idx}`}
                  selected={sessionsSelected}
                  onClick={() => onSetView("sessions")}
                  sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
                >
                  <span className="nav-icon" style={iconStyle}>
                    <ItemGlyph kind={glyph || "sessions"} />
                  </span>
                  <ListItemText
                    primary="Sessions"
                    sx={{ ml: 1 }}
                    primaryTypographyProps={{
                      fontSize: "0.85rem",
                      fontWeight: 500,
                    }}
                  />
                  <RailCount value={visibleSessions.length} />
                </ListItemButton>
              );
            }
          }

          if (item.kind === "session") {
            const session = visibleSessions.find((s) => s.id === item.id);
            if (!session) return null;
            return (
              <ListItemButton
                key={`pinned-session-${session.id}-${idx}`}
                selected={view === "session" && openSessionId === session.id}
                onClick={() => onOpenSession && onOpenSession(session.id)}
                sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
              >
                <span className="nav-icon" style={iconStyle}>
                  <ItemGlyph
                    kind={
                      glyph ||
                      (session.id === "director" ? "director" : "sessions")
                    }
                  />
                </span>
                <ListItemText
                  primary={session.title || session.id}
                  sx={{ ml: 1 }}
                  primaryTypographyProps={{
                    noWrap: true,
                    fontSize: "0.85rem",
                    fontWeight: 500,
                  }}
                />
              </ListItemButton>
            );
          }

          if (item.kind === "staging") {
            const sandbox = staging.find((sb) => sb.name === item.id);
            if (!sandbox) return null;
            return (
              <StagingItem
                key={`pinned-staging-${sandbox.name}-${idx}`}
                sandbox={sandbox}
                config={config}
                pinned
                onTogglePin={() => toggleStagingPin(sandbox.name)}
              />
            );
          }

          if (item.kind === "board") {
            const board = visibleBoards.find((b) => b.id === item.id);
            if (!board) return null;
            return (
              <ListItemButton
                key={`pinned-board-${board.id}-${idx}`}
                selected={view === "boards" && openBoardId === board.id}
                onClick={() => onOpenBoard && onOpenBoard(board)}
                sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
              >
                <span className="nav-icon" style={iconStyle}>
                  {glyph ? (
                    <ItemGlyph kind={glyph} />
                  ) : (
                    <span
                      className="dot dot-all"
                      style={
                        color
                          ? { backgroundColor: color, borderColor: color }
                          : undefined
                      }
                    />
                  )}
                </span>
                <ListItemText
                  primary={board.name}
                  sx={{ ml: 1 }}
                  primaryTypographyProps={{
                    noWrap: true,
                    fontSize: "0.85rem",
                    fontWeight: 500,
                  }}
                />
              </ListItemButton>
            );
          }

          return null;
        })}
        <Divider sx={{ my: 0.7 }} />

        <Box sx={{ display: "flex", flexDirection: "column", gap: 0.2 }}>
          {/* Tasks navigation item & types breakdown */}
          {shows("tasks") && (
            <Box
              ref={tasksRef}
              sx={{ display: "flex", flexDirection: "column" }}
            >
              <ListItemButton
                selected={view === "tasks" && stateFilter === "all"}
                onClick={() => {
                  selectState("all");
                }}
                sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
              >
                <span className="nav-icon">
                  <ItemGlyph kind="tasks" />
                </span>
                <ListItemText
                  primary="Tasks"
                  sx={{ ml: 1 }}
                  primaryTypographyProps={{
                    noWrap: true,
                    fontSize: "0.85rem",
                    fontWeight: 500,
                  }}
                />
                <RailCount
                  value={taskCount}
                  color={
                    view === "tasks" && stateFilter === "all"
                      ? "primary"
                      : "text.secondary"
                  }
                  sx={{ fontVariantNumeric: "tabular-nums" }}
                />
              </ListItemButton>

              <Box
                sx={{
                  pl: collapsed ? 0 : 0.5,
                  display: "flex",
                  flexDirection: "column",
                  gap: 0.1,
                }}
              >
                {DISPLAY_STATE_ORDER.filter((s) => counts[s]).map((s) => (
                  <NavItem
                    onSelect={selectState}
                    key={s}
                    id={s}
                    label={STATE_LABELS[s]}
                    dotClass={`dot-${s}`}
                    count={counts[s]}
                    active={view === "tasks" && stateFilter === s}
                    waiting={s === "asked" || INBOX_STATES.includes(s)}
                  />
                ))}
                {merged > 0 && (
                  <NavItem
                    onSelect={selectState}
                    id="merged"
                    label="Merged"
                    dotClass="dot-merged"
                    count={merged}
                    active={view === "tasks" && stateFilter === "merged"}
                  />
                )}
                {blocked > 0 && (
                  <NavItem
                    onSelect={selectState}
                    id="blocked"
                    label="Blocked"
                    dotClass="dot-blocked"
                    count={blocked}
                    active={view === "tasks" && stateFilter === "blocked"}
                  />
                )}
              </Box>
            </Box>
          )}

          {shows("tasks") &&
            ["boards", "sessions", "processes", "ci", "repos"].some(shows) && (
              <Divider sx={{ my: 0.5 }} />
            )}

          {/* Boards panel destination */}
          {shows("boards") && (
            <ListItemButton
              selected={view === "boards" && !openBoardId}
              onClick={() => {
                onSetView("boards");
              }}
              sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
            >
              <span className="nav-icon">
                <ItemGlyph kind="boards" />
              </span>
              <ListItemText
                primary="Boards"
                sx={{ ml: 1 }}
                primaryTypographyProps={{
                  fontSize: "0.85rem",
                  fontWeight: 500,
                }}
              />
              <RailCount value={visibleBoards.length} />
            </ListItemButton>
          )}

          {/* Sessions panel destination */}
          {shows("sessions") && (
            <ListItemButton
              selected={sessionsSelected}
              onClick={() => {
                onSetView("sessions");
              }}
              sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
            >
              <span className="nav-icon">
                <ItemGlyph kind="sessions" />
              </span>
              <ListItemText
                primary="Sessions"
                sx={{ ml: 1 }}
                primaryTypographyProps={{
                  fontSize: "0.85rem",
                  fontWeight: 500,
                }}
              />
              <RailCount value={visibleSessions.length} />
            </ListItemButton>
          )}

          {/* Processes (docs/design/settings-panel.md §4.2): schedules,
            templates, suites and triggers in one entry, counted the
            way the page lists them. Then CI. */}
          {shows("processes") && (
            <ListItemButton
              selected={view === "processes"}
              onClick={() => {
                onSetView("processes");
              }}
              sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
            >
              <span className="nav-icon">
                <ItemGlyph kind="processes" />
              </span>
              <ListItemText
                primary="Processes"
                sx={{ ml: 1 }}
                primaryTypographyProps={{
                  fontSize: "0.85rem",
                  fontWeight: 500,
                }}
              />
              <RailCount value={processCount} />
            </ListItemButton>
          )}

          {shows("ci") && (
            <ListItemButton
              selected={view === "ci"}
              onClick={() => {
                onSetView("ci");
              }}
              sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
            >
              <span className="nav-icon">
                <ItemGlyph kind="ci" />
              </span>
              <ListItemText
                primary="CI"
                sx={{ ml: 1 }}
                primaryTypographyProps={{
                  fontSize: "0.85rem",
                  fontWeight: 500,
                }}
              />
              <RailCount value={runningCICount} />
            </ListItemButton>
          )}

          {shows("repos") && (
            <ListItemButton
              selected={view === "repos"}
              onClick={() => {
                onSetView("repos");
              }}
              sx={{ borderRadius: 1.5, py: 0.6, px: 0.9 }}
            >
              <span className="nav-icon">
                <ItemGlyph kind="repos" />
              </span>
              <ListItemText
                primary="Repos"
                sx={{ ml: 1 }}
                primaryTypographyProps={{
                  fontSize: "0.85rem",
                  fontWeight: 500,
                }}
              />
              <RailCount value={repoCount} />
            </ListItemButton>
          )}
        </Box>

        {/* Staging: every enabled persistent sandbox exposing a port --
          the deployment's staging environments -- each a link that
          opens it in a tab of its own (StagingItem). Absent on a
          deployment that runs none. */}
        {shows("staging") && staging.length > 0 && (
          <Box
            data-testid="staging-group"
            sx={{ display: "flex", flexDirection: "column", gap: 0.1 }}
          >
            <Divider sx={{ my: 0.7 }} />
            <Typography
              variant="caption"
              color="text.secondary"
              className="sidebar-section-heading rail-hidden"
              sx={{ pl: 0.9, mb: 0.2 }}
            >
              Staging
            </Typography>
            {staging.map((sb) => (
              <StagingItem
                key={`staging-${sb.name}`}
                sandbox={sb}
                config={config}
                pinned={stagingPinned(sb.name)}
                onTogglePin={() => toggleStagingPin(sb.name)}
              />
            ))}
          </Box>
        )}
      </List>

      {pinFooter && (
        <List
          component="nav"
          aria-label="System"
          disablePadding
          sx={{ flex: "none", mt: "auto" }}
        >
          <Divider sx={{ mb: 0.9 }} />
          <FooterItem
            kind="admin"
            label="System"
            active={view === "system"}
            onClick={onOpenSystem}
          >
            {stateDot(config) ? (
              <UnbackedStateDot {...stateDot(config)} />
            ) : null}
          </FooterItem>
        </List>
      )}

      {/* Which build of grain is answering: the commit it was built from
          and when that commit was made, out of the binary's own VCS
          stamp (GET /api/config's "version", pkg/version). */}
      {/* Folded, the stamp has no room and is left off: Settings'
        Upgrade section, behind the header rail's gear, says the same
        thing. */}
      {!collapsed &&
      (config?.version?.commit ||
        config?.version?.release ||
        config?.version?.edition) ? (
        <Typography
          variant="caption"
          component="a"
          href="/settings/upgrade"
          data-testid="build-stamp"
          onClick={(evt) => {
            if (!onOpenUpgrade) return;
            evt.preventDefault();
            onOpenUpgrade();
          }}
          color="text.secondary"
          noWrap
          title={buildStampTitle(config.version, zone)}
          sx={{
            display: "block",
            textDecoration: "none",
            px: 0.9,
            m: 0,
            // The foot of the rail on desktop, under the System row on a
            // phone (§4.3).
            mt: pinFooter ? 0 : "auto",
            pt: 0.6,
            fontSize: "0.68rem",
            opacity: 0.75,
            flex: "none",
            fontVariantNumeric: "tabular-nums",
          }}
        >
          {buildStampLabel(config.version)}
        </Typography>
      ) : null}
      {showCustomize && (
        <CustomizeSidebarOverlay
          open={showCustomize}
          onClose={() => setShowCustomize(false)}
          sessions={sessions}
          boards={boards}
        />
      )}
    </Box>
  );

  return (
    <RailCollapsedContext.Provider value={collapsed}>
      {aside}
    </RailCollapsedContext.Provider>
  );
}
