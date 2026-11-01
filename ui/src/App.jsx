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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { kindNarrowing } from "./processFilters.js";
import api, {
  isUnreachable,
  RESTARTING_REASON,
  unreachableReason,
} from "./api.js";
import {
  buildPath,
  isRepoTab,
  parsePath,
  queryFromNarrowing,
  showsStateFilter,
} from "./paths.js";
import { CUSTOM_PREFIX } from "./taskAttributes.js";
import { useIsPhone } from "./phone.js";
import { keepIfSame, UNCHANGED } from "./poll.js";
import {
  NO_NARROWING,
  defaultFilters,
  defaultNarrowing,
  defaultSortFor,
  isDefaultSort,
} from "./taskFilters.js";
import {
  addPanelTask,
  boardColumns,
  boardNarrowing,
  editBoardTask,
  keepPanelQueryTask,
  kindForView,
  newBoardTask,
  normalizeBoards,
} from "./boards.js";
import { boardMoves } from "./boardOrder.js";
import { panelMove } from "./panelOrder.js";
import { sectionMove } from "./sectionOrder.js";
import { TimeZoneProvider } from "./TimeZoneContext.jsx";
import Sidebar from "./components/Sidebar.jsx";
import { useSidebarCollapsed } from "./sidebarCollapse.js";
import { SIDEBAR_COLLAPSED_WIDTH, SIDEBAR_WIDTH } from "./theme.js";
import PageHeader, {
  GlobalButtons,
  PageHeaderProvider,
} from "./components/PageHeader.jsx";
import PhoneNav from "./components/PhoneNav.jsx";
import InboxPage from "./components/InboxPage.jsx";
import TaskList from "./components/TaskList.jsx";
import TaskBoard from "./components/TaskBoard.jsx";
import GanttChart from "./components/GanttChart.jsx";
import TaskTreeBoard from "./components/TaskTreeBoard.jsx";
import BoardsPage from "./components/BoardsPage.jsx";
import BoardPage from "./components/BoardPage.jsx";
import ConsoleSessionPage from "./components/ConsoleSessionPage.jsx";
import SessionsPage from "./components/SessionsPage.jsx";
import PanelBoard from "./components/PanelBoard.jsx";
import ReportBoard from "./components/ReportBoard.jsx";
import RepoList from "./components/RepoList.jsx";
import RepoPage, { REPO_TABS } from "./components/RepoPage.jsx";
import PullRequestPage from "./components/PullRequestPage.jsx";
import ProcessesList from "./components/ProcessesList.jsx";
import { declaringRepos, triggersOf } from "./processes.js";
import { repoApiRoot } from "./repoApi.js";
import CIPage from "./components/CIPage.jsx";
import CIRunOverlay from "./components/CIRunOverlay.jsx";
import BatchActionsBar from "./components/BatchActionsBar.jsx";
import ErrorBanner from "./components/ErrorBanner.jsx";
import DroppedDefaultsNotice, {
  droppedDefaults,
} from "./components/DroppedDefaultsNotice.jsx";
import ConfigRefusalDialog from "./components/ConfigRefusal.jsx";
import DetailOverlay from "./components/DetailOverlay.jsx";
import TaskLoadingOverlay from "./components/TaskLoadingOverlay.jsx";
import { DISCARD_PROMPT } from "./components/Overlay.jsx";
import { leavingLosesEdits } from "./unsavedEdits.js";
import NewTaskOverlay from "./components/NewTaskOverlay.jsx";
import CustomizeSidebarOverlay from "./components/CustomizeSidebarOverlay.jsx";
import SettingsPage from "./components/SettingsPage.jsx";
import SystemPage from "./components/SystemPage.jsx";
import {
  DEFAULT_SETTINGS_SECTION,
  oldAdminDestination,
  settingsSectionFor,
  systemDestination,
  settingsSectionLabel,
  systemTabLabel,
} from "./settingsSections.js";
import RepoReleases from "./components/RepoReleases.jsx";
import LoadingScreen from "./components/LoadingScreen.jsx";
import ConfigUnavailable from "./components/ConfigUnavailable.jsx";
import ErrorBoundary from "./components/ErrorBoundary.jsx";
import AccessDenied from "./components/AccessDenied.jsx";
import DisconnectedBanner from "./components/DisconnectedBanner.jsx";
import { InstallNoticeBanner } from "./components/InstallNotice.jsx";
import AlertsButton from "./components/AlertsButton.jsx";
import { notificationAlerts, useOpenNotifications } from "./notifications.js";
import { collectAlerts, sortAlerts } from "./alerts.js";
import NewSessionPage, {
  NEW_SESSION_ID,
} from "./components/NewSessionPage.jsx";
import { PreferencesProvider, usePreferences } from "./PreferencesContext.jsx";
import { PinnedNavProvider, usePinnedNav } from "./PinnedNavContext.jsx";
import {
  notificationsEnabled,
  useInboxNotifications,
} from "./inboxNotifications.js";

// POLL_INTERVAL_MS is how long the UI can be out of date by.
//
// A task changes state when graind dispatches it, when a run finishes,
// and when a pull request merges -- none of which the browser is told
// about, so without this the screen only moves when somebody clicks. A
// few seconds is far below what anyone notices and the request is a
// list of a handful of rows against a store on the same machine.
// Exported for App.test.jsx, whose poll test runs a fake clock forward
// by exactly this rather than by a 3000 written out again beside it.
export const POLL_INTERVAL_MS = 3000;

// HIDDEN_POLL_EVERY is how many of those ticks a hidden tab lets pass
// between reads of the task list, and it reads nothing else. A
// background tab otherwise polls not at all, which is right for
// everything but the inbox's desktop notifications
// (inboxNotifications.js): they exist for the tab nobody is looking
// at, and with nothing fetched there was nothing new to announce. So
// a tab whose reader has turned them on still learns about new work
// every 30s. The read is the conditional /api/tasks the visible poll
// makes, so a quiet minute costs two 304s.
export const HIDDEN_POLL_EVERY = 10;

// CLOCK_INTERVAL_MS is how often the screen re-reads the clock when
// nothing else has moved.
//
// The poll above used to be the clock too. Every tick set freshly parsed
// objects into state, so the whole tree re-rendered every three seconds
// whether or not anything had changed, and everything that reads
// Date.now() as it draws -- a running task's "as of 2m ago" (state.js's
// relativeAge), the timeline's now-line and the right edge of its "last
// 7 days" -- came along for the ride.
//
// The polls now keep the state they already hold when the answer says
// the same thing (poll.js), which is the point of that change: a
// dashboard left open on a deployment where nothing is happening should
// cost nothing between one task moving and the next. That leaves the
// clock with nothing to advance it, which is what this interval is for
// and the whole of what it is for -- see the effect that runs it.
//
// Thirty seconds because the finest thing anything on screen says about
// elapsed time is a whole minute, so a label is never more than half a
// step behind, at a twentieth of the renders the poll was paying for.
export const CLOCK_INTERVAL_MS = 30000;

// GATE_INTERVAL_MS is how often the shell re-reads the base branches
// nothing is judging (GET /api/base-branch-gates, grain/task-470).
//
// A minute rather than POLL_INTERVAL_MS because of what the reading is.
// A gate finding appears when a branch has gone two hours with an
// unjudged merge on it, or three with a run that never concluded
// (pkg/ui/basebranchgate.go's own bounds), and it clears when a run
// starts. Nothing about it moves on the scale the task list moves on, so
// a third request every three seconds in every view -- which is what the
// rail's own count would otherwise cost -- would buy a reader nothing
// they could act on any sooner.
//
// It is still polled rather than fetched once at mount: this is the one
// thing on the inbox that arrives without anybody doing anything at all,
// on a screen somebody may leave open all day.
export const GATE_INTERVAL_MS = 60000;

// STAGING_INTERVAL_MS is how often the rail re-reads the persistent
// sandboxes (GET /api/persistent-sandboxes) for its Staging group's
// state dots. A staging environment restarting is worth seeing within
// seconds, but not worth a request on every poll tick.
export const STAGING_INTERVAL_MS = 15000;

// LOADING_PANE_DELAY_MS is how long a task's detail may take before its
// loading pane goes up (TaskLoadingOverlay). Short enough that a slow
// click still visibly registers (grain/task-1756); long enough that an
// ordinary one goes straight to the task with nothing in between.
export const LOADING_PANE_DELAY_MS = 300;

// urlItems is which of the list panes a parsed path opens, keyed the way
// keepIfListed names them: the ids a URL asked for, which nothing has
// yet confirmed exist.
function urlItems(parsed) {
  return {
    schedule: parsed.scheduleId || null,
    template: parsed.templateId || null,
    suite: parsed.suiteId || null,
    "suite run": parsed.suiteRunId || null,
    board: parsed.boardId || null,
    session: parsed.sessionId || null,
  };
}

// missingMessage is what a link to one of those that is not there says,
// in the words /tasks/:id and /ci/:id already use for theirs.
// BOARDS_REPO is where a task that changes a board is filed: the
// deployment's config repository, grain/config.
const BOARDS_REPO = "grain/config";

function missingMessage(kind, id) {
  return `no ${kind} "${id}"`;
}

// VIEW_LABELS names each view in the top bar's breadcrumb, in lower case
// like the rest of grain's navigation (the rail lower-cases its own
// labels in style.css). Written lower case rather than transformed, so
// the crumb's accessible name is what it shows.
const VIEW_LABELS = {
  inbox: "inbox",
  tasks: "tasks",
  boards: "boards",
  sessions: "sessions",
  session: "sessions",
  repos: "repositories",
  processes: "processes",
  ci: "ci",
  settings: "settings",
  system: "system",
};

const VIEW_GLYPHS = {
  inbox: "inbox",
  tasks: "tasks",
  boards: "boards",
  sessions: "sessions",
  session: "sessions",
  repos: "repos",
  processes: "processes",
  ci: "ci",
  settings: "settings",
  system: "system",
};

// repoTabAt is the repo page tab an address names: the path's own
// (/repos/acme/widgets/settings/ci), or, on a repo page's address from
// before the tab joined the path, the hash's (/repos/acme/widgets#ci).
// null is Overview, and anything that is not a repo page.
function repoTabAt(parsed, hash) {
  if (parsed.view !== "repos" || !parsed.repo) return null;
  if (parsed.showReleases || parsed.repoComparison) return null;
  if (parsed.repoTab) return parsed.repoTab;
  const fromHash = (hash || "").replace(/^#/, "");
  return isRepoTab(fromHash) ? fromHash : null;
}

// repoTabCrumb is the " / <tab>" a repo page's crumbs end in when one of
// its tabs other than Overview is showing -- "repositories /
// acme/widgets / merge queue" -- so the rail says where in the repo the
// reader is, as it does for a settings section or a system tab. Without
// it every tab read the same as Overview.
function repoTabCrumb(tab) {
  const found =
    tab && tab !== "overview" && REPO_TABS.find((t) => t.id === tab);
  return found ? ` / ${found.label.toLowerCase()}` : "";
}

// WhenRailReady holds the page back until the header rail's slot has
// mounted -- the same commit, since the rail's ref sets it before the
// browser paints -- so a page's header row is drawn into the rail from
// its first render rather than in its body first and moved after, which
// remounted it and threw away whatever the page had measured (a chat
// pinned to its newest message). Only ever on the way in: once the page
// is up it stays up, since a window crossing the phone breakpoint takes
// the rail away and brings it back, and unmounting the page for that
// one render threw away its state and left the chat's own timers
// reading a list that was gone.
function WhenRailReady({ ready, children }) {
  const [shown, setShown] = useState(ready);
  if (ready && !shown) setShown(true);
  return ready || shown ? children : null;
}

function AppContent({ onReload = () => window.location.reload() }) {
  // Which shell this browser gets. On anything from a tablet up the nav
  // rail stands permanently beside the pane, which is the layout every
  // view here is built around; on a phone there is no room for it, so
  // the same rail moves into PhoneNav's drawer behind a top bar
  // (phone.js explains where the line is drawn and why). Nothing below
  // this line differs between the two -- the phone layout is the same
  // views in a shell that fits, not a second UI.
  const isPhone = useIsPhone();
  const { addPinnedItem } = usePinnedNav();
  // ...and the same answer, published on <html> for the stylesheet, the
  // way AppThemeProvider publishes the resolved light/dark mode. The
  // rules a phone needs are mostly plain CSS -- padding, wrapping, how
  // wide a board column is -- and a second copy of phone.js's media
  // query in style.css would be free to drift from the first. This
  // attribute also reaches what a media query would reach anyway but a
  // class on the shell below would not: the overlay panes, which MUI
  // renders into a portal outside this tree.
  useEffect(() => {
    document.documentElement.dataset.phone = isPhone ? "true" : "false";
  }, [isPhone]);
  // The desktop rail folded down to its icons, remembered per browser
  // (sidebarCollapse.js). A phone has no rail to fold -- PhoneNav's
  // drawer draws it whole -- so neither the fold nor its "[" key apply.
  const [sidebarCollapsedChoice, , toggleSidebarCollapsed] =
    useSidebarCollapsed({ shortcut: !isPhone });
  const sidebarCollapsed = !isPhone && sidebarCollapsedChoice;
  // The pane anything you open fills starts where the rail ends
  // (Overlay.jsx), so it reads the rail's width off the same variable.
  useEffect(() => {
    document.documentElement.style.setProperty(
      "--sidebar-width",
      `${sidebarCollapsed ? SIDEBAR_COLLAPSED_WIDTH : SIDEBAR_WIDTH}px`,
    );
  }, [sidebarCollapsed]);
  const [config, setConfig] = useState(null);
  // The time zone this browser chose in Settings → Preferences, or "" for
  // none. It wins over the deployment's own clock (config.timeZone) for
  // every time printed here: until the 2026-10-23 settings walkthrough it
  // was stored and shown in its own picker but read by nothing else, so
  // picking one changed no time on any page. With neither this nor a
  // deployment clock set (config.timeZone is "" until somebody sets one),
  // the provider gets "" and time.js formats in the browser's own zone --
  // what "Browser default" in that picker promises.
  const { timeZone: preferredTimeZone } = usePreferences();
  // Why the last /api/config failed, while it is failing: once the first
  // load has settled with no config, this is what turns the splash into
  // ConfigUnavailable rather than leaving it up forever (grain/task-1435).
  const [configError, setConfigError] = useState(null);
  const [accessDenied, setAccessDenied] = useState(false);
  const [deniedUser, setDeniedUser] = useState(null);
  // Whether the first load has answered yet (the mount effect below).
  // Every list on this screen starts empty, and an empty list nobody has
  // fetched is not the same fact as an empty list the server sent -- so
  // until this is true the shell draws LoadingScreen rather than a page
  // that says, in prose and in counts, that this deployment has nothing.
  const [loaded, setLoaded] = useState(false);
  const [tasks, setTasks] = useState([]);
  const [schedules, setSchedules] = useState([]);
  const [templates, setTemplates] = useState([]);
  const [suites, setSuites] = useState([]);
  const [suiteRuns, setSuiteRuns] = useState([]);
  // Grain CI runs are held at shell level because the rail shows how many
  // are running even while another pane is open. The CI page still owns its
  // fuller, filterable view of the same endpoint.
  const [ciRuns, setCIRuns] = useState([]);
  // The base branches this deployment's merge queue is merging onto that
  // nothing is judging (pkg/ui/basebranchgate.go). Held at shell level
  // for the reason the CI runs are: the inbox lists them and the rail
  // counts them from every other pane.
  const [baseBranchGates, setBaseBranchGates] = useState([]);
  // Whether that list has answered once: until it has, an empty list is
  // not "nothing unjudged", and the inbox notifications below take its
  // first answer as their baseline rather than as news.
  const [gatesLoaded, setGatesLoaded] = useState(false);
  // The standing sessions this deployment keeps open -- the director
  // among them (grain/task-415) -- straight off GET /api/console-sessions,
  // with the capabilities that response offers for one beside them. These
  // are configured, they outlive every run of themselves, and
  // there is always at least one unless somebody has deliberately
  // removed it.
  const [sessions, setSessions] = useState([]);
  const [persistentSandboxes, setPersistentSandboxes] = useState([]);
  // Whether the sessions have answered once: until they have, a session
  // view opened on a link has nothing to say the session is missing.
  const [sessionsLoaded, setSessionsLoaded] = useState(false);
  const [sessionCapabilities, setSessionCapabilities] = useState([]);
  // The network settings a session may be given, off the same response:
  // the pane's own select draws these rather than hard-coding the two
  // this deployment happens to offer today.
  const [sessionNetworkAccesses, setSessionNetworkAccesses] = useState([]);
  // Which standing session's pane is open. Held rather than derived so
  // that a deployment with two of them opens the one that was clicked;
  // null means "the first", which is what the rail's own entry opens on
  // a deployment with one.
  const [openSessionId, setOpenSessionId] = useState(
    () => parsePath(window.location.pathname).sessionId || null,
  );
  // A request handed from a shortcut into a standing session's composer.
  // The object identity matters: clicking the same shortcut again is a new
  // request even though its words are the same, while ordinary session polls
  // must leave whatever the operator has typed alone.
  const [sessionPrompt, setSessionPrompt] = useState(null);
  // The saved dashboards (boards.js, grain/task-48), and which one is
  // open. They are a settings list like schedules and templates -- held
  // here, fetched once at mount and again after anything that could have
  // changed them -- but they show *tasks*, so an open one is a task view
  // with the shared narrowing over it rather than a pane of its own.
  const [boards, setBoards] = useState([]);

  const [openBoardId, setOpenBoardId] = useState(
    () => parsePath(window.location.pathname).boardId || null,
  );
  // Seeded from the URL like the narrowing beside it (paths.js's
  // stateFilterFromQuery): a state view is a list worth reloading and
  // linking to, and Back steps out of one.
  const [stateFilter, setStateFilter] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search).stateFilter ||
      "all",
  );
  // Customize sidebar is App's to open, not the rail's: on a phone the
  // rail lives in PhoneNav's drawer, which closes -- and unmounts the
  // rail -- on the very tap that asked for the overlay, so an overlay
  // held in Sidebar's own state went with it and never appeared
  // (2026-10-23 responsive walkthrough, bug 2).
  const [showCustomizeSidebar, setShowCustomizeSidebar] = useState(false);
  // A browser the server names no actor for and does not count as signed
  // in has nothing to work with: it gets AccessDenied, which sends it to
  // the access-code sign-in, rather than an empty app. grain has no
  // users, so there is no name to choose here
  // (design/design/settings-panel.design.md §6.1). A caller the server trusts
  // without a session (loopback) is named, and is let in; nor is a page
  // whose /api/config never got grain's answer at all held here.
  const isSignedOut =
    loaded && config?.authenticated === false && config?.actor === "";
  // See selectStateFilter below for what picking one in the rail does to
  // the narrowing: the two are one question about state now.

  // narrowing is how the task views are narrowed right now -- the search
  // text, the sort order and the attribute filters their shared toolbar
  // offers (taskFilters.js) -- lifted out of TaskList/TaskBoard and
  // seeded from the query string the page loaded with, which is also
  // where every change to it is written back (grain/task-317). A list
  // narrowed by several menus at once is a view worth linking to, so it
  // gets the same treatment as which sub-page is showing: loadable cold,
  // survives a reload, and the Back button undoes it.
  //
  // One narrowing, not one per view: the flat list and saved task views
  // all ask taskFilters.js's question, so moving between them keeps it
  // rather than silently dropping it.
  //
  // askedNarrowing is what has actually been *asked* -- by the link the
  // page loaded on, by the toolbar, by a board -- and null when nothing
  // has been. The narrowing in force is that or the deployment's own
  // default (taskFilters.js's defaultNarrowing), resolved here rather
  // than seeded into the state, because `config` arrives a fetch after
  // the first render and a default captured before it would be missing
  // the deployment's answer about closed tasks.
  const [askedNarrowing, setAskedNarrowing] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search).narrowing ||
      null,
  );
  // The default as the rail would have left it: a link that names a
  // state (?state=closed) and nothing else opens on the deployment's
  // default narrowing, and that default sets the closed and deferred
  // tasks aside -- so it is taken without its State condition whenever
  // the rail is asking a state, exactly as selectStateFilter below
  // would have taken it.
  // The Merged list also opens newest merged first rather than in
  // backlog order (taskFilters.js's defaultSortFor).
  const listDefault = useCallback(() => {
    const base = {
      ...defaultNarrowing(config),
      sort: defaultSortFor(stateFilter),
    };
    if (stateFilter === "all" || stateFilter === "blocked") return base;
    return {
      ...base,
      filters: (base.filters || []).filter((c) => c.attr !== "state"),
    };
  }, [config, stateFilter]);
  const narrowing = useMemo(
    () => askedNarrowing || listDefault(),
    [askedNarrowing, listDefault],
  );
  // narrow takes a patch rather than a whole narrowing, so a toolbar
  // control only has to name what it changed. A patch over the default
  // is a patch over what is on screen: the first thing anybody changes
  // keeps whatever the default was already asking.
  const narrow = useCallback(
    (patch) =>
      setAskedNarrowing((prev) => ({
        ...(prev || listDefault()),
        ...patch,
      })),
    [listDefault],
  );
  // selectStateFilter is a click on a state in the rail. The rail and
  // the toolbar's own State condition are one question asked in two
  // places now that the set-aside states are an ordinary condition
  // (taskFilters.js's defaultFilters), and a list narrowed to "State is
  // none of Deferred, Closed" and then asked for "Closed" would have
  // nothing in it at all -- which is the whole reason the checkboxes
  // used to be exempt from the rail.
  //
  // So the rail re-answers it rather than intersecting with it: picking
  // a state takes any State condition out of the narrowing, since the
  // rail is already saying which state the list is about, and picking
  // "All tasks" or "Blocked" -- neither of which is a state -- puts the
  // default condition back. Conditions on every other attribute survive
  // untouched, so a list narrowed to one repo stays narrowed to it while
  // the reader walks down the states.
  //
  // The sort follows the same rule as the State condition: an order
  // nobody chose (backlog order, or the Merged list's newest-merged-
  // first) is swapped for the picked state's own default, and an order
  // somebody did choose is left alone.
  const selectStateFilter = useCallback(
    (id) => {
      setStateFilter(id);
      setAskedNarrowing((prev) => {
        const start = prev || defaultNarrowing(config);
        const base = isDefaultSort(start.sort)
          ? { ...start, sort: defaultSortFor(id) }
          : start;
        const withoutState = (base.filters || []).filter(
          (c) => c.attr !== "state",
        );
        const asksAState = id !== "all" && id !== "blocked";
        if (asksAState) return { ...base, filters: withoutState };

        const defaults = defaultFilters(config);
        const defaultAttrs = new Set(defaults.map((c) => c.attr));
        const rest = withoutState.filter((c) => !defaultAttrs.has(c.attr));
        return { ...base, filters: [...defaults, ...rest] };
      });
    },
    [config],
  );
  // view switches the main pane between the flat task list, the repos
  // pane, and the schedules page.
  //
  // view, openRepo and settingsSection (below) all seed from the URL the
  // page loaded with rather than a fixed default, so a direct link to
  // /repos, /repos/acme/widgets or /settings lands on that sub-page
  // instead of always opening on the task list first (bwsalmon/agents
  // #548, grain/task-111).
  const [view, setView] = useState(
    () => parsePath(window.location.pathname).view,
  );
  // openRepo is which repo's own page is showing within the repos view
  // (null shows the repo list instead), and releasesOpen whether that
  // page's release pane is showing over it -- the repo page is the only
  // way into releases now that the list's own "Releases" button is gone.

  const [openRepo, setOpenRepo] = useState(
    () => parsePath(window.location.pathname).repo || null,
  );
  // repoTab is which of that page's tabs is showing, null for Overview:
  // /repos/<o>/<n>/settings/<tab> (design/design/settings-panel.design.md §2.3).
  // The tab used to ride in the hash (/repos/acme/widgets#ci), and a link
  // kept from then still opens its tab -- the first address written
  // below moves it into the path.
  const [repoTab, setRepoTab] = useState(() =>
    repoTabAt(parsePath(window.location.pathname), window.location.hash),
  );
  // repoComparison is which of that repo's pull requests is showing over
  // its page (PullRequestPage.jsx, grain/task-375): the base and head a
  // comparison names, or null for the repo page itself. It is the same
  // "one step inside the repo page" the releases pane and a release train
  // are, and is held here for the same reason -- so the address bar
  // carries it and a link to one diff opens cold.
  const [repoComparison, setRepoComparison] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search)
        .repoComparison || null,
  );
  const [releasesOpen, setReleasesOpen] = useState(
    () => parsePath(window.location.pathname).showReleases === true,
  );
  // openRelease is which release that pane is showing, when the address
  // names one -- held here with the rest so a reload, Back or a link
  // reopens it. null is "whichever is newest", which the pane picks.
  const [openRelease, setOpenRelease] = useState(
    () => parsePath(window.location.pathname).release || null,
  );
  // openScheduleId/openTemplateId/openSuiteId are which schedule,
  // template or suite is showing as a pane over its own list -- the
  // openTaskId (below) of the other three lists. They live here rather
  // than inside SchedulesList/TemplatesList/SuitesList so that buildPath
  // can name the open item in the URL the way it already names an open
  // task or an open repo (grain/task-139): opening one is the same
  // gesture with the same result in all four lists (grain/task-94), so
  // it is the same kind of link too.
  //
  // Each seeds straight from the URL the page loaded with, and needs no
  // fetch of its own to do it -- unlike a task's detail, which is a
  // round trip and so a mount effect below. App already holds all three
  // lists in full, so restoring one of these panes is just finding the
  // row this id names once its list has landed.
  const [openScheduleId, setOpenScheduleId] = useState(
    () => parsePath(window.location.pathname).scheduleId || null,
  );
  const [openTemplateId, setOpenTemplateId] = useState(
    () => parsePath(window.location.pathname).templateId || null,
  );
  const [openSuiteId, setOpenSuiteId] = useState(
    () => parsePath(window.location.pathname).suiteId || null,
  );
  // openSuiteRunId is the fourth of those panes: one run of a suite, and
  // every task it has filed (SuiteRunPane.jsx, grain/task-322). A run's
  // id is a number over the wire and this is the string the URL carries,
  // which is what the pane compares against.
  const [openSuiteRunId, setOpenSuiteRunId] = useState(
    () => parsePath(window.location.pathname).suiteRunId || null,
  );
  const [processResetSeq, setProcessResetSeq] = useState(0);
  // urlItemsRef holds the ids of those panes (and the open board's) that
  // came in on a URL -- the page load, or a Back/Forward -- and that no
  // list fetch has confirmed yet. droppedRef is set when a fetch closes
  // one of those panes because its item is not there, and is read by the
  // URL effect below: that correction replaces the address rather than
  // pushing, and when the id was one a URL named, says so, the way
  // /tasks/:id does for a task that is not there (grain/task-1432).
  const urlItemsRef = useRef(urlItems(parsePath(window.location.pathname)));
  const droppedRef = useRef(null);
  const [error, setError] = useState(null);
  // disconnection is null while the daemon is reachable, or
  // { reason, since } once a poll fails in the way api.js calls
  // unreachable -- a dead connection, a timeout, or a gateway in front
  // of the daemon saying the backend isn't there, as opposed to the
  // daemon's own answer to a request being an error (grain/task-26).
  // `since` is when the current streak of failures started, kept across
  // every failed tick in that streak rather than reset to each one, so
  // DisconnectedBanner's own "unreachable since" names when the trouble
  // began rather than when it was last confirmed.
  //
  // Each caller names itself ("shell" for the mount fetch and the main
  // poll, "session" for the session pane's own), and the banner stays up
  // while any of them is failing: the shell's poll still getting answers
  // must not take down a banner the session read's own 503s put up, or
  // the two would flap it on and off every tick (grain/task-1854's
  // per-poll counting, kept through the merge with task-26).
  const [disconnection, setDisconnection] = useState(null);
  const unreachableFrom = useRef(new Set());
  const unreachableStreak = useRef(new Map());
  // restartingRef is the last GET /api/config's `restarting`: the daemon
  // said it was draining for a restart, or running an upgrade that ends
  // in one. A disconnection that starts while it is set is that restart
  // (design/design/non-disruptive-upgrade.design.md, Stage 1), and the banner says
  // "grain is restarting" rather than that the deployment cannot be
  // reached.
  const restartingRef = useRef(false);
  const noteUnreachable = useCallback(
    (source, err, { immediate = false } = {}) => {
      const prev = unreachableStreak.current.get(source);
      const firstSeenAt = prev ? prev.firstSeenAt : new Date();
      const count = (prev ? prev.count : 0) + 1;
      unreachableStreak.current.set(source, { count, firstSeenAt });
      if (!immediate && !restartingRef.current && count < 2) {
        return false;
      }
      unreachableFrom.current.add(source);
      setDisconnection(
        (current) =>
          current ||
          (restartingRef.current
            ? {
                reason: RESTARTING_REASON,
                restarting: true,
                since: firstSeenAt,
              }
            : { reason: unreachableReason(err), since: firstSeenAt }),
      );
      return true;
    },
    [],
  );
  const noteReached = useCallback((source) => {
    unreachableStreak.current.delete(source);
    unreachableFrom.current.delete(source);
    if (unreachableFrom.current.size > 0) return;
    setDisconnection((prev) => (prev === null ? prev : null));
  }, []);
  const [openTaskId, setOpenTaskId] = useState(null);
  const [detail, setDetail] = useState(null);
  // pendingTaskId is the task a click has asked for whose detail is still
  // on its way. The pane opens on it as a loading pane
  // (TaskLoadingOverlay) if the fetch is slow, so the click visibly
  // registers however slow it is (grain/task-1756). It is kept apart from openTaskId, which
  // still only moves once the detail has landed: that is what the URL
  // effect and the poll read, and a task that fails to load should leave
  // the address bar where it was.
  const [pendingTaskId, setPendingTaskId] = useState(null);
  // taskLoadError is a task the address bar asked for that grain failed
  // to answer for with anything but a 404 -- a 500 while the daemon
  // restarts, a proxy blip, the network gone. The task may well be there
  // a moment later, so rather than dropping the reader back on the list
  // (and the /tasks/N they followed with it, so a reload no longer finds
  // the task), its pane stays open on the error with a Retry, and the
  // poll keeps trying behind it (grain/task-3173). Only a 404 means the
  // link is no good, and that still falls back to the list.
  const [taskLoadError, setTaskLoadError] = useState(null);
  // loadingShown is whether that loading pane is up yet. It waits
  // LOADING_PANE_DELAY_MS first: most details answer well inside that,
  // and putting the loading pane up for each of them, only to swap it
  // for the task a moment later, was the pane flickering on every click
  // (grain/task-2179). A click from inside an open task keeps that task
  // on screen for the same moment, rather than blanking it to a spinner.
  const [loadingShown, setLoadingShown] = useState(false);
  useEffect(() => {
    if (pendingTaskId === null) {
      setLoadingShown(false);
      return undefined;
    }
    const timer = setTimeout(
      () => setLoadingShown(true),
      LOADING_PANE_DELAY_MS,
    );
    return () => clearTimeout(timer);
  }, [pendingTaskId]);
  const showLoadingPane = pendingTaskId !== null && loadingShown;
  const showDetailPane =
    !showLoadingPane && openTaskId !== null && detail !== null;
  const showTaskErrorPane =
    !showLoadingPane &&
    !showDetailPane &&
    openTaskId !== null &&
    taskLoadError?.id === openTaskId;
  // taskPaneOnScreen is whether either task pane was up at the last
  // commit. A pane mounting while it is set is taking the place of one --
  // the task replacing its own loading pane, the next task opened from
  // inside this one -- and opens without the fade (Overlay's `animate`).
  const taskPaneOnScreen = useRef(false);
  useEffect(() => {
    taskPaneOnScreen.current =
      showLoadingPane || showDetailPane || showTaskErrorPane;
  });
  const [showNewTask, setShowNewTask] = useState(
    () => parsePath(window.location.pathname).showNewTask === true,
  );
  // newTaskFields is the custom field values the form opens carrying
  // (grain/task-53) -- null for the ordinary "+ New task" click, and a
  // {name: value} map for a /new link somebody followed, which is what
  // a task's own pane offers so the next task about the same customer
  // starts where the last one did.
  const [newTaskFields, setNewTaskFields] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search)
        .newTaskFields || null,
  );
  // newTaskRepo is the repo the "New task" button on a repo's own page
  // was opened from (bwsalmon/agents#474, grain/task-111); null means
  // "no override", so the overlay falls back to whichever repo page is
  // open, the same as the sidebar's own "+ New task" button does.
  const [newTaskRepo, setNewTaskRepo] = useState(null);
  // newTaskBoard is the board whose page "New task" was opened on, which
  // picks settings/new-task-form.yml's boards layer (newTaskForm.js) and
  // rides on the filing as boardId for the server's hidden defaults.
  // Null for the form opened anywhere else.
  const [newTaskBoard, setNewTaskBoard] = useState(null);
  // newTaskPrefill is a task somebody is being offered rather than one
  // they are writing from scratch: the "New board"/"Edit board" buttons
  // open the form with the title and the description already in it
  // (boards.js), so that asking for a dashboard is a click and an edit
  // rather than a paragraph nobody wants to compose. null is the
  // ordinary empty form.
  const [newTaskPrefill, setNewTaskPrefill] = useState(null);
  // The open Settings section and System tab (docs/design/settings-
  // panel.md §3.5), each "" for its view's bare path: Settings' section
  // list on a phone (General elsewhere, filled in below), and System's
  // Health. settingsAnchor is a card or field to scroll to once the
  // section is drawn.
  const [settingsSection, setSettingsSection] = useState(
    () => parsePath(window.location.pathname).settingsSection || "",
  );
  const [settingsAnchor, setSettingsAnchor] = useState("");
  // settingsItem is one item of the open section, in the path after it:
  // the capability server /settings/capabilities/<id> opens.
  const [settingsItem, setSettingsItem] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search)
        .settingsItem || "",
  );
  const [systemTab, setSystemTab] = useState(
    () => parsePath(window.location.pathname).systemTab || "",
  );
  // openCIRunId is which grain CI run is open on the CI page, null for
  // the list of them -- the openSuiteRunId of that view, and in the URL
  // for the same reasons (paths.js).
  const [openCIRunId, setOpenCIRunId] = useState(
    () => parsePath(window.location.pathname).ciRunId || null,
  );
  // ciRepo is the repo the CI page should open narrowed to, for a link
  // that names one: "All runs for this repo" on a repo's own page, and
  // any row of that list (RepoRuns.jsx, grain/task-351). Not in the URL,
  // unlike the run: it is where the page's own filter starts rather than
  // what the page is, and the page reseeds it every time it is opened
  // (CIPage.jsx's initialRepo).
  const [ciRepo, setCIRepo] = useState("");
  // metricsNarrowing is what the Metrics pane's report is scoped to --
  // the repo, the author and the span report.js reads out of a narrowing
  // -- and null for the deployment's whole report, which is what that
  // pane has always been (grain/task-379).
  //
  // A second narrowing beside askedNarrowing, deliberately. The shared
  // one is what the task views are asking; this one is what a report is
  // measured over, and the two must not be the same object: opening this
  // pane from the rail over a list narrowed to "failed tasks I filed"
  // would otherwise scope the deployment's own report to it without
  // anybody asking, and a link to a scoped report would narrow the list
  // waiting behind the pane. So only a link or the Metrics tab's own
  // toolbar sets it.
  //
  // In the URL, unlike systemCIRun's kind of pane state: a scoped report
  // is the thing a panel's heading links to and an operator pastes into
  // a standup note, so it has to survive being copied (paths.js's
  // metricsNarrowing).
  // processQuery is the Processes list's narrowing -- search, sort and
  // conditions, the kind chips among them (processFilters.js)
  // (design/design/settings-panel.design.md §5.4), in the URL so a filtered view
  // is a link. Null until somebody narrows it.
  const [processQuery, setProcessQuery] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search)
        .processQuery || null,
  );
  // processTriggers is every trigger the declaring repositories report
  // (GET /api/repos/{owner}/{name}/declarations), which the Processes
  // list shows read-only by path (§5.1). Fetched on the way into that
  // page, like the suite runs.
  const [processTriggers, setProcessTriggers] = useState([]);
  const [metricsNarrowing, setMetricsNarrowing] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search)
        .metricsNarrowing || null,
  );
  // metricsWindow is the pane's window picker, null until somebody picks
  // one, and in the URL beside the scope for the same reason
  // (grain/task-1584): "the last 30 days" is as much which report is on
  // screen as "only acme/widgets" is.
  const [metricsWindow, setMetricsWindow] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search)
        .metricsWindow || null,
  );
  // boardWindow is the same picker on an open report board, in that
  // board's URL for the same reason (grain/task-2393): a board that only
  // held its pick lost it to a reload, to Back from a task opened off
  // it, and to anybody the link was sent to. It belongs to the board
  // that is open, so opening another one -- or leaving -- drops it.
  const [boardWindow, setBoardWindow] = useState(
    () =>
      parsePath(window.location.pathname, window.location.search).boardWindow ||
      null,
  );
  // The pane's own toolbar, patching the pane's own narrowing -- `narrow`
  // above for the report rather than for the list. NO_NARROWING as the
  // base rather than the deployment's default: the default is a question
  // about which tasks a list shows, and a report over every task is what
  // this pane means by asking nothing.
  const narrowMetrics = useCallback(
    (patch) =>
      setMetricsNarrowing((prev) => ({ ...(prev || NO_NARROWING), ...patch })),
    [],
  );
  const [selected, setSelected] = useState(() => new Set());
  // The ids the flat task list is drawing right now (TaskList's
  // onShownChange), so the batch bar can say how much of a selection
  // made in another view is not on screen (BatchActionsBar's shownIds).
  const [shownIds, setShownIds] = useState(null);
  const polling = useRef(false);
  const activeCommitRef = useRef(null);
  // How many ticks of the main poll below to let pass before the next
  // one actually runs, while the daemon stays unreachable -- 1 is every
  // tick, same as always, and it doubles on each further failure up to
  // 10 (half a minute at POLL_INTERVAL_MS), so a halted host is asked
  // less and less often rather than every three seconds for as long as
  // it stays down. pollTick counts ticks against it; both reset to 1/0
  // the moment a tick gets through. The backoff spaces out the main
  // poll's own asking, not how soon the page notices grain is back: the
  // restart watch below keeps asking every POLL_INTERVAL_MS through the
  // outage, and the first answer it gets resets both and calls pollNow,
  // so the banner clears within a tick of grain answering rather than
  // up to half a minute later.
  const pollBackoff = useRef(1);
  const pollTick = useRef(0);
  const pollNow = useRef(null);
  const hiddenTick = useRef(0);

  // isUnreachable/unreachableReason first: a toast for a mutation that
  // failed because the daemon could not be reached at all should read
  // the same way the standing banner does, not the browser's own raw
  // "Failed to fetch".
  //
  // Two exceptions keep what the request itself said. A 502/503/504
  // whose body carries an error message has somebody explaining that
  // one request -- often grain itself, relaying an upstream service that
  // did not answer (a capability check, a branch read) -- and the generic
  // sentence would hide the explanation. And a network-level failure
  // keeps the engine's own words in brackets after the friendly sentence,
  // for whoever has to match the toast to a console line.
  //
  // And while DisconnectedBanner is already up, an unexplained
  // unreachable failure raises no toast at all: the banner says the same
  // thing, the action's own state (a composer's draft) is kept for a
  // retry, and the toast sat bottom-centre over a session's composer and
  // its Send / Delete session controls on phones and tablets
  // (grain/task-2009). A toast already showing such a failure goes when
  // the banner comes up, for the same reason.
  const disconnectedRef = useRef(false);
  disconnectedRef.current = disconnection !== null;
  const errorIsUnreachable = useRef(false);
  // Nor does a call the server refused for want of a sign-in, while the
  // signed-out screen is what is on screen: the page's own loads all 401
  // before anyone has signed in, and the toast over that screen said
  // again, as an error, what the screen already says (the 10-15 install
  // walkthrough). Before /api/config has answered too: those loads start
  // alongside it, and their 401s land before the screen that answers for
  // them is drawn.
  const signInOpenRef = useRef(false);
  signInOpenRef.current = !loaded || isSignedOut;
  const [configRefusal, setConfigRefusal] = useState(null);
  const showError = useCallback((err) => {
    if (err?.status === 401 && signInOpenRef.current) return;
    // A patch to commit is not a five-second toast: it is a dialog that
    // stays until it is read (components/ConfigRefusal.jsx).
    if (err?.configRefusal) {
      setConfigRefusal(err.configRefusal);
      return;
    }
    const message = String(err?.message || err);
    const unexplained = isUnreachable(err) && !err.explained;
    if (unexplained && disconnectedRef.current) return;
    errorIsUnreachable.current = unexplained;
    if (!unexplained) {
      setError(message);
    } else if (typeof err.status === "number") {
      setError(unreachableReason(err));
    } else {
      setError(`${unreachableReason(err)} (${message})`);
    }
  }, []);

  // dismissError takes the banner down if it still says `message`, for a
  // view whose failure is over (a settings box put back to its saved
  // value), without hiding a later, unrelated error.
  const dismissError = useCallback((message) => {
    setError((prev) => (prev === String(message) ? null : prev));
  }, []);

  useEffect(() => {
    if (disconnection !== null && errorIsUnreachable.current) setError(null);
  }, [disconnection]);

  useEffect(() => {
    if (error === null) return;
    const t = setTimeout(() => setError(null), 5000);
    return () => clearTimeout(t);
  }, [error]);

  const refreshList = useCallback(async () => {
    // Conditional: the server answers a poll that already holds the
    // current list with a 304 and no body (pkg/ui/task_list_cache.go),
    // which on a real deployment's history is 1.7 MB and a JSON parse
    // this tab does not do. There is nothing to set and nothing to
    // prune when it does -- the ids below would be the same ids -- so
    // the tick ends here.
    const next = await api("/api/tasks", { poll: true });
    if (next === UNCHANGED) return;
    // The array already held when this poll brought back the same tasks
    // (poll.js). It is the identity every view downstream is keyed on --
    // most of all the dashboard's, whose panels memoise their answers on
    // it (taskFilters.js's narrowTasks) -- so handing over a fresh array
    // of identical rows every three seconds is what made a quiet tab
    // expensive. Still worth doing beside the 304: that covers the
    // endpoint's own bytes, this covers a change those views do not care
    // about.
    setTasks((prev) => keepIfSame(prev, next));
    // Drop any selected id the store no longer reports, so a stale
    // selection never quietly outlives the task it named.
    const ids = new Set(next.map((t) => t.id));
    setSelected((prev) => {
      const kept = new Set([...prev].filter((id) => ids.has(id)));
      return kept.size === prev.size ? prev : kept;
    });
  }, []);

  // All three of these drop an open item the store no longer reports --
  // one deleted from its own pane, or a URL naming one that never
  // existed -- so the address bar never keeps naming a pane that isn't
  // there: refreshList's own pruning of `selected` above, applied to
  // each list's own open item. It hangs off the fetch rather than off an
  // effect watching the list because all three lists start out empty,
  // and an empty list nothing has fetched yet says nothing about
  // whether the item the URL names exists.
  //
  // keepIfListed is that pruning, shared: it is called from inside the
  // state updater, so it only ever records into refs (StrictMode may run
  // an updater twice, and the second run must not undo the first's
  // note). What it records is read by the URL effect below.
  //
  // An answer that is not a list at all (a half-deployed server, a
  // proxy's error object) says nothing about whether the open item
  // exists, so the updaters below keep it rather than calling .some on
  // it: a throw there is inside App's own render, above every page's
  // ErrorBoundary, and blanked the whole shell (grain/task-1553). The
  // page that cannot draw the list is what the boundary contains.
  const keepIfListed = useCallback((kind, prev, listed) => {
    if (prev === null) return prev;
    const fromUrl = urlItemsRef.current[kind] === prev;
    if (listed) {
      if (fromUrl) urlItemsRef.current[kind] = null;
      return prev;
    }
    const missing = droppedRef.current?.missing || [];
    droppedRef.current = {
      missing:
        fromUrl && !missing.includes(missingMessage(kind, prev))
          ? [...missing, missingMessage(kind, prev)]
          : missing,
    };
    return null;
  }, []);

  const refreshSchedules = useCallback(async () => {
    const next = await api("/api/schedules");
    setSchedules(next);
    setOpenScheduleId((prev) =>
      Array.isArray(next)
        ? keepIfListed(
            "schedule",
            prev,
            next.some((s) => s.id === prev),
          )
        : prev,
    );
  }, [keepIfListed]);

  const refreshTemplates = useCallback(async () => {
    const next = await api("/api/templates");
    setTemplates(next);
    setOpenTemplateId((prev) =>
      Array.isArray(next)
        ? keepIfListed(
            "template",
            prev,
            next.some((t) => t.id === prev),
          )
        : prev,
    );
  }, [keepIfListed]);

  const refreshSuites = useCallback(async () => {
    const next = await api("/api/suites");
    setSuites(next);
    setOpenSuiteId((prev) =>
      Array.isArray(next)
        ? keepIfListed(
            "suite",
            prev,
            next.some((s) => s.id === prev),
          )
        : prev,
    );
  }, [keepIfListed]);

  const refreshSuiteRuns = useCallback(async () => {
    const next = await api("/api/suite-runs");
    setSuiteRuns(next);
    setOpenSuiteRunId((prev) =>
      Array.isArray(next)
        ? keepIfListed(
            "suite run",
            prev,
            next.some((r) => String(r.id) === prev),
          )
        : prev,
    );
  }, [keepIfListed]);

  // The rail's CI badge is a count of what is going on right now
  // (Sidebar.jsx), so this asks for exactly that rather than for the
  // deployment's whole remembered history: a run that concluded last
  // week cannot change a number about the ones that have not concluded,
  // and a deployment keeping a fortnight of them was sending all of it
  // every few seconds to draw one figure (grain/task-289). The count
  // below still reads the conclusion, so a widened answer would not
  // break it.
  const refreshCIRuns = useCallback(async () => {
    const next = await api(
      "/api/ci/runs?conclusion=running&conclusion=pending",
    );
    setCIRuns((prev) => keepIfSame(prev, next?.runs || []));
  }, []);

  // The base branches nothing is judging. Kept when unchanged, like the
  // lists above: this answers the same way for hours at a time, and the
  // inbox should not re-render every minute to say so.
  const refreshBaseBranchGates = useCallback(async () => {
    const next = await api("/api/base-branch-gates");
    setBaseBranchGates((prev) => keepIfSame(prev, (next && next.gates) || []));
    setGatesLoaded(true);
  }, []);

  // The boards list, normalized on the way in: a board can be written by
  // an agent editing the state repository's dump, so this build can be
  // handed a kind it has never heard of or a row that is not a board at
  // all, and none of that is worth a blank screen (boards.js).
  const refreshBoards = useCallback(async () => {
    const next = normalizeBoards(await api("/api/boards"));
    // Kept when unchanged: a dashboard is polled on every tick while it
    // is the view (the poll effect below), and a board arriving as a new
    // object every three seconds would re-render every panel on it
    // whatever the tasks did.
    setBoards((prev) => keepIfSame(prev, next));
    setOpenBoardId((prev) =>
      keepIfListed(
        "board",
        prev,
        next.some((b) => b.id === prev),
      ),
    );
  }, [keepIfListed]);

  // refreshSessions re-fetches the standing sessions. Polled rather than
  // fetched once at mount, because the fact the
  // rail is waiting on can arrive without anybody doing anything here: a
  // session configured a moment ago has no task until the next cycle's
  // reconciler files one, and the chat cannot open until it does.
  //
  // Reads are sequenced: the background poll's read can still be out
  // when a restart answers with the session's replacement task, and
  // written after it, its older list would point the pane back at the
  // closed task and draw that conversation again. So a list is only
  // written if no read started later (or answer applied by
  // applySessions) has been written already; a stale one is dropped
  // and refreshSessions answers null.
  const sessionsSeq = useRef(0);
  const sessionsWritten = useRef(0);
  const applySessions = useCallback((next) => {
    sessionsSeq.current += 1;
    sessionsWritten.current = sessionsSeq.current;
    setSessions((prev) => keepIfSame(prev, next));
  }, []);
  const refreshSessions = useCallback(async () => {
    sessionsSeq.current += 1;
    const seq = sessionsSeq.current;
    const res = await api("/api/console-sessions");
    if (seq < sessionsWritten.current) return null;
    sessionsWritten.current = seq;
    const next = (res && res.sessions) || [];
    setSessions((prev) => keepIfSame(prev, next));
    setSessionsLoaded(true);
    // A session a URL named, once the list confirms it is there, is no
    // longer the URL's to report missing (urlItemsRef).
    if (
      urlItemsRef.current.session &&
      next.some((s) => s && s.id === urlItemsRef.current.session)
    ) {
      urlItemsRef.current.session = null;
    }
    setSessionCapabilities((prev) =>
      keepIfSame(prev, (res && res.capabilities) || []),
    );
    setSessionNetworkAccesses((prev) =>
      keepIfSame(prev, (res && res.networkAccessOptions) || []),
    );
    return next;
  }, []);

  // openSession is the standing session the "session" view is showing:
  // the one whose entry was clicked, or the first this deployment has,
  // so the rail's entry always opens something on the ordinary
  // deployment that has exactly one.
  //
  // The fallback to the first is only for "no session named": a link to
  // one by id -- a deep link, Back onto a session since deleted -- opens
  // that one or
  // nothing, never a different session's chat under the named one's URL,
  // where whatever is typed next goes to an agent the reader did not
  // pick. Nothing falls through to the list, which says the id is gone.
  //
  // NEW_SESSION_ID is neither: it is the chat that has no session yet
  // (NewSessionPage.jsx), drawn by the session view in place of a pane.
  const newSessionOpen = openSessionId === NEW_SESSION_ID;
  const openSession = newSessionOpen
    ? null
    : openSessionId
      ? sessions.find((s) => s.id === openSessionId) || null
      : sessions[0] || null;
  const openSessionTaskId = openSession?.taskId || null;
  const openSessionIdRef = useRef(null);
  openSessionIdRef.current = openSessionId;
  const openSessionTaskIdRef = useRef(null);
  openSessionTaskIdRef.current = openSessionTaskId;
  // The session's task, in full. The list this shell polls carries a
  // task's fields but not its conversation, and the conversation is the
  // whole of this pane -- so the open session's task is fetched the same
  // way an open task detail is, by id.
  const [sessionTask, setSessionTask] = useState(null);
  const sessionTaskRef = useRef(null);
  sessionTaskRef.current = sessionTask;
  const sessionTaskSeq = useRef(0);
  const loadSessionTask = useCallback(
    async (taskId = openSessionTaskId) => {
      const seq = ++sessionTaskSeq.current;
      if (!taskId) {
        setSessionTask(null);
        return;
      }
      let detail = await api(`/api/tasks/${taskId}`, {
        poll: true,
      });
      if (seq !== sessionTaskSeq.current) {
        return;
      }
      if (detail === UNCHANGED) {
        if (String(sessionTaskRef.current?.id) === String(taskId)) {
          return;
        }
        detail = await api(`/api/tasks/${taskId}`);
        if (seq !== sessionTaskSeq.current) {
          return;
        }
      }
      if (String(openSessionTaskIdRef.current) !== String(taskId)) {
        return;
      }
      setSessionTask((prev) => keepIfSame(prev, detail));
      // api.js caches ETags per path, so when the session's task is also
      // open in the task detail overlay, a 200 consumed here advances the
      // ETag the detail poll will send next ( returning 304 UNCHANGED ).
      // Share the fresh body across both states so neither poll starves
      // the other.
      setDetail((prev) =>
        prev && String(prev.id) === String(detail?.id)
          ? keepIfSame(prev, detail)
          : prev,
      );
    },
    [openSessionTaskId],
  );

  // sessionAct is `act` for the session pane: run the mutation, then
  // re-read the conversation it changed. Not `act` itself, which opens
  // the task detail over whatever the reader was looking at -- a message
  // sent in a chat should leave them in the chat.
  const sessionAct = useCallback(
    async (mutate) => {
      sessionTaskSeq.current += 1;
      try {
        await mutate();
      } catch (err) {
        showError(err);
        return false;
      }
      try {
        await loadSessionTask();
        await refreshList();
      } catch (err) {
        showError(err);
      }
      return true;
    },
    [loadSessionTask, refreshList, showError],
  );

  // refreshSessionSaved re-reads the sessions, the open session's task,
  // and the task list after a session control (Stop, Start a fresh chat,
  // capability toggle) completes so the chat's Stop button, state badge,
  // and separator reflect the mutation immediately rather than waiting
  // for the next poll tick.
  //
  // A control whose answer already lists the sessions (Start a fresh
  // chat answers with the replacement task in it) passes that list,
  // which is written at once: until it is, the pane is still pointed at
  // the task the restart closed.
  const refreshSessionSaved = useCallback(
    async (answered) => {
      const pointAt = (listed) => {
        const currentSession = openSessionIdRef.current
          ? listed.find((s) => s.id === openSessionIdRef.current) || null
          : listed[0] || null;
        openSessionTaskIdRef.current = currentSession?.taskId || null;
      };
      if (Array.isArray(answered)) {
        applySessions(answered);
        pointAt(answered);
      }
      const [nextSessions] = await Promise.all([
        refreshSessions(),
        refreshList(),
      ]);
      // null: a later read or answer has already been written, and the
      // pane already points where that one said.
      if (nextSessions) pointAt(nextSessions);
      await loadSessionTask(openSessionTaskIdRef.current);
    },
    [applySessions, refreshSessions, loadSessionTask, refreshList],
  );

  // refreshConfig re-fetches /api/config -- needed after adding a target
  // repo (RepoList's own "Add") or removing one (RepoPage's own
  // "Remove", bwsalmon/agents#473) since config is otherwise only ever fetched
  // once, at mount, and the repos pane's own list (repoRows) reads
  // config.targetRepos to decide which repos to show and which ones it
  // can offer to remove. It reads config.repoDefaultCapabilities for the
  // same reason, which is why saving a repo's own defaults refreshes
  // this too: a repo listed only because it carries a set stops being
  // listed the moment that set is emptied. A repo's standing
  // instructions are the third thing that can put it on that list
  // (config.reposWithPromptExtension), so saving those refreshes it as
  // well.
  //
  // And every save on the Settings pane (SettingsOverlay's onSaved),
  // since half of what that pane writes is read back out of this
  // response: the target repo allowlist, the deployment-wide default
  // capabilities and task defaults the new-task form starts from, and
  // the deployment's own prompt extension.
  const refreshConfig = useCallback(async () => {
    try {
      const next = await api("/api/config");
      if (next && typeof next === "object") {
        restartingRef.current = Boolean(next.restarting);
      }
      if (next?.accessDenied || next?.allowed === false) {
        setAccessDenied(true);
        setDeniedUser(next?.actor || null);
      }
      const nextCommit = next?.version?.commit;
      if (nextCommit) {
        if (activeCommitRef.current && activeCommitRef.current !== nextCommit) {
          onReload();
          return;
        }
        activeCommitRef.current = nextCommit;
      }
      // Kept when unchanged, for refreshList's reason and one of its own:
      // config is the second argument to every narrowing on screen, so a
      // new object each tick invalidates the panels' answers even when the
      // tasks are the identical array (taskFilters.js's narrowTasks keys
      // on both).
      setConfig((prev) => keepIfSame(prev, next));
      setConfigError(null);
    } catch (err) {
      setConfigError(err);
      if (
        String(err?.message || "").includes("403") ||
        String(err?.message || "")
          .toLowerCase()
          .includes("access denied")
      ) {
        setAccessDenied(true);
      }
      throw err;
    }
  }, [onReload]);

  // refreshAfterCreate is what the new-task form is given instead of
  // refreshList alone: filing a task now also writes a setting this
  // response carries. Picking which end of the backlog it joins is
  // remembered as the deployment's default for the next one
  // (ui.CreateTaskRequest.AtFront, config.newestFirst), and the form
  // seeds its picker from that -- so without re-reading the config here,
  // the choice would only show up on the next full page load.
  //
  // It is also handed the filed task, and a filing that left a default
  // grant off (ui.Task.DroppedDefaults) says so in a toast.
  const [filedWithoutDefaults, setFiledWithoutDefaults] = useState(null);
  const refreshAfterCreate = useCallback(
    async (created) => {
      if (droppedDefaults(created).length > 0) setFiledWithoutDefaults(created);
      await refreshList();
      await refreshConfig();
    },
    [refreshList, refreshConfig],
  );

  const toggleSelect = useCallback((id) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const setSelection = useCallback((ids, checked) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);

  const clearSelection = useCallback(() => setSelected(new Set()), []);

  // The selected tasks themselves, not just their ids: the batch bar's
  // "Bulk edit" form opens on the values the selection already holds
  // (BulkEditOverlay), which `selected` alone cannot answer. Resolved
  // against the whole list rather than the one a pane happens to be
  // showing. refreshList already drops any id the store has stopped
  // reporting, so nothing here can name a task that is gone.
  const selectedTasks = useMemo(
    () => tasks.filter((t) => selected.has(t.id)),
    [tasks, selected],
  );

  // openSeq numbers each openTask and closeDetail, so a detail fetch
  // that resolves after a later open (two Back presses, a dependency
  // link clicked while the pane is still loading) or after the pane was
  // closed is dropped instead of putting back a task the user has left.
  const openSeq = useRef(0);
  const openStandingSessionRef = useRef(() => {});

  // loadTask is openTask with one more say: fromUrl marks a load the
  // address bar asked for, whose failure keeps the pane (taskLoadError) instead
  // of leaving the page behind it to rewrite the URL.
  const loadTask = useCallback(
    async (id, fromUrl) => {
      const seq = ++openSeq.current;
      setPendingTaskId(id);
      try {
        const d = await api(`/api/tasks/${id}`);
        if (seq !== openSeq.current) return;
        setPendingTaskId(null);
        setTaskLoadError(null);
        if (d?.consoleSession) {
          setOpenTaskId(null);
          setDetail(null);
          openStandingSessionRef.current(d.consoleSession);
          return;
        }
        setOpenTaskId(id);
        setDetail(d);
      } catch (err) {
        if (seq !== openSeq.current) return;
        setPendingTaskId(null);
        if (fromUrl && err?.status !== 404) {
          setOpenTaskId(id);
          setDetail(null);
          setTaskLoadError({ id, message: err?.message || String(err) });
          return;
        }
        showError(err);
      }
    },
    [showError],
  );
  const openTask = useCallback((id) => loadTask(id, false), [loadTask]);

  const closeDetail = useCallback(() => {
    openSeq.current++;
    setPendingTaskId(null);
    setOpenTaskId(null);
    setDetail(null);
    setTaskLoadError(null);
  }, []);

  // urlTask is a task the address bar asked for -- a cold load of
  // /tasks/N, or Back/Forward landing on one -- that openTask is still
  // fetching. Until it arrives openTaskId is null, so the URL effect
  // below would compute the page *behind* the task and write that over
  // the address bar: replacing /tasks/N with "/" on a reload, so Back
  // left the page the reader came from, and pushing an extra entry on
  // Forward, which also dropped the rest of the forward stack
  // (grain/task-1356). That effect holds off while this is set. Seeded
  // from the URL itself because the effect's first run is in the same
  // commit as the mount effect that starts the fetch.
  const [urlTask, setUrlTask] = useState(
    () => parsePath(window.location.pathname).taskId || null,
  );
  const openTaskFromUrl = useCallback(
    async (id) => {
      setUrlTask(id);
      try {
        await loadTask(id, true);
      } finally {
        // Only this request's own claim: a quicker Back/Forward may
        // have asked for another task since.
        setUrlTask((cur) => (cur === id ? null : cur));
      }
    },
    [loadTask],
  );

  // openTaskFromMetrics is the one link out of the Metrics pane: the
  // report's backlog names the oldest queued task, and clicking it
  // should land on that task. It closes the metrics pane on the way --
  // both fill the same area beside the sidebar, and MetricsOverlay is
  // mounted after DetailOverlay here, so leaving it open would put the
  // task the click asked for behind the pane it was clicked in.
  //
  // The pane closes in the same update that opens the task, once the
  // task has arrived, rather than before the fetch: closing it first
  // rendered the bare list in between, which the URL effect pushed as a
  // history entry of its own, so Back from the task landed on "/" rather
  // than on the report it was opened from (grain/task-1359). A task
  // that fails to load leaves the report up, beside the error.
  const openTaskFromMetrics = useCallback(
    async (id) => {
      try {
        const d = await api(`/api/tasks/${id}`);
        if (d?.consoleSession) {
          setOpenTaskId(null);
          setDetail(null);
          openStandingSessionRef.current(d.consoleSession);
          return;
        }
        setOpenTaskId(id);
        setDetail(d);
      } catch (err) {
        showError(err);
      }
    },
    [showError],
  );

  // selectRelease is the releases pane telling App which release it is
  // showing. Its second argument is a release the address named that the
  // repo does not have: the pane shows the newest instead, and the
  // address is corrected the way one naming a missing task is -- replaced
  // rather than pushed, and saying what it could not find.
  const selectRelease = useCallback((release, missing) => {
    setOpenRelease((prev) => {
      if (missing) {
        const notes = droppedRef.current?.missing || [];
        const message = missingMessage("release", missing);
        droppedRef.current = {
          missing: notes.includes(message) ? notes : [...notes, message],
        };
      } else if (release === null && prev !== null) {
        droppedRef.current = droppedRef.current || { missing: [] };
      }
      return release;
    });
  }, []);

  // openRepoPage is the repo list's row click: show that repo's own page
  // (RepoPage, grain/task-111), which is where its tasks and everything
  // else about it live.
  const openRepoPage = useCallback((repo, tab = null) => {
    if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
    setOpenRepo(repo);
    setRepoTab(tab);
    setRepoComparison(null);
    setReleasesOpen(false);
    setOpenRelease(null);
    setView("repos");
  }, []);

  // openTriggerRepo is a Processes trigger row (§5.1): a trigger is
  // listed only by its file, so it opens the repository that declares it
  // on its Advanced tab, where its declarations are.
  const openTriggerRepo = useCallback(
    (repo) => openRepoPage(repo, "advanced"),
    [openRepoPage],
  );

  const closeRepoPage = useCallback(() => {
    setOpenRepo(null);
    setRepoTab(null);
    setRepoComparison(null);
    setReleasesOpen(false);
    setOpenRelease(null);
  }, []);

  // closeOpenItems clears whichever list's item pane is open -- only one
  // of the four can be, since each belongs to its own view (and the last
  // two both belong to the suites one, where a suite and a run of it are
  // never open together). An open
  // pane is a modal dialog over the sidebar (Overlay.jsx), so today
  // nothing can navigate with one open and this never fires; it is here
  // so that "which item is open" can't outlive the view it means
  // anything in and quietly reopen on the way back to that view.
  const closeOpenItems = useCallback(() => {
    setOpenScheduleId(null);
    setOpenTemplateId(null);
    setOpenSuiteId(null);
    setOpenSuiteRunId(null);
    setProcessResetSeq((n) => n + 1);
    // ...and the CI run being read, which is the same kind of pane one
    // step inside its own list (CIPage.jsx): a click on "CI" is a click
    // to the runs, not back to whichever one was open last time. The
    // repo that page was last opened narrowed to goes with it, for the
    // same reason: the rail's own entry is the whole deployment's runs.
    setOpenCIRunId(null);
    setCIRepo("");
  }, []);

  // openNewTaskForRepo is the repo page's own "New task" button: file a
  // task against that specific repo. It takes the repo explicitly rather
  // than reading openRepo, since the overlay outlives a navigation away
  // from the page it was opened on.
  const openNewTaskForRepo = useCallback((repo) => {
    setNewTaskRepo(repo);
    setNewTaskBoard(null);
    setNewTaskFields(null);
    setShowNewTask(true);
  }, []);

  // openBoard is a click on a board, in the rail or on the boards page:
  // show it, and ask its own question. A board *is* a narrowing
  // (boards.js), so opening one replaces whatever the toolbar was
  // asking -- which is the whole gesture, and is why it is done here
  // rather than left to the board's own view: the narrowing belongs to
  // App, and the URL is written from it.
  //
  // A board that asks nothing sets it back to null, which is the same
  // "nobody has asked" a plain "/" carries and is answered by the
  // deployment's own default above (boards.js, grain/task-309) -- so a
  // dashboard of panels and the lists its panels link to are narrowed by
  // one rule rather than by two.
  //
  // seededBoard records which board that has been done for, so the
  // effect below -- which is what a page *loaded* on /boards/board-3
  // goes through -- does not undo a narrowing this click just applied,
  // or a narrowing the reader has since made of the board on screen.
  //
  // It is state rather than a ref so that it lands in the same render as
  // the narrowing it seeds: the board panes below wait on it
  // (boardAwaitingSeed), and a ref written in the effect was visible to
  // a render the seeded narrowing had not reached yet.
  const [seededBoard, setSeededBoard] = useState(null);
  const openBoard = useCallback(
    (board) => {
      if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
      closeRepoPage();
      closeOpenItems();
      setSeededBoard(board.id);
      setAskedNarrowing(boardNarrowing(board));
      setOpenBoardId(board.id);
      setBoardWindow(null);
      setView("boards");
    },
    [closeRepoPage, closeOpenItems],
  );

  // A board opened by URL rather than by click: /boards/board-3 asks the
  // board's own question, and /boards/board-3?f=... asks the one the
  // link carries. Which is why this only seeds a board that arrived
  // asking nothing of its own -- a link that says what it wants is not a
  // link to be overruled by the row it names, and "asking nothing" is
  // exactly askedNarrowing being null (the list is on its default).
  useEffect(() => {
    if (view !== "boards" || openBoardId === null) return;
    if (seededBoard === openBoardId) return;
    const board = boards.find((b) => b.id === openBoardId);
    if (!board) return;
    setSeededBoard(openBoardId);
    if (askedNarrowing === null) setAskedNarrowing(boardNarrowing(board));
  }, [view, openBoardId, boards, askedNarrowing, seededBoard]);

  // The two buttons that change a board, and the whole of how a board is
  // made: they open the new-task form on a task that is already written
  // (boards.js explains why a board is a task rather than a form), filed
  // against the deployment's config repository (BOARDS_REPO).
  const openBoardTask = useCallback((prefill, targetRepo) => {
    setNewTaskPrefill(prefill);
    setNewTaskFields(null);
    setNewTaskRepo(targetRepo || BOARDS_REPO);
    setNewTaskBoard(null);
    setShowNewTask(true);
  }, []);

  // What "New board" offers is the view the reader is on as well as the
  // narrowing they have made of it (boards.js's kindForView): clicking
  // "+" while reading a timeline board asks for another timeline, which
  // is the difference between the button saving what is on screen and
  // the button saving half of it. Everywhere else it offers the list a
  // board is by default and names the other kinds, since with the fixed
  // board and timeline gone (grain/task-162) the rail's own "+" is
  // where somebody asks for either.
  //
  // The narrowing all three of these carry is what has been *asked*,
  // exactly as the address bar carries it below: a board's query and a
  // link's query are one encoding read by one parser, and a view nobody
  // has narrowed is spelt in it by saying nothing (boards.js's
  // boardNarrowing, grain/task-309). Writing the deployment's own
  // default into the prompt instead would file "add a panel narrowed to
  // `f=state:none:deferred|closed`" for a reader who has narrowed
  // nothing, and freeze into the row a condition the deployment is
  // entitled to change.
  const openNewBoardTask = useCallback(() => {
    openBoardTask(
      newBoardTask({
        narrowing: askedNarrowing,
        kind: kindForView(
          view,
          boards.find((b) => b.id === openBoardId),
        ),
        window: boardWindow,
      }),
      BOARDS_REPO,
    );
  }, [openBoardTask, askedNarrowing, view, boards, openBoardId, boardWindow]);

  const openEditBoardTask = useCallback(
    (board) => {
      openBoardTask(
        editBoardTask(board, {
          narrowing: askedNarrowing,
          window: boardWindow,
        }),
        BOARDS_REPO,
      );
    },
    [openBoardTask, askedNarrowing, boardWindow],
  );

  // "Add panel", on a dashboard (PanelBoard.jsx): the same road again,
  // asked about one panel -- and carrying the narrowing the reader has
  // made on the dashboard, so "put what I am looking at on here" is a
  // click rather than a paragraph.
  const openAddPanelTask = useCallback(
    (board) => {
      openBoardTask(
        addPanelTask(board, { narrowing: askedNarrowing, config }),
        BOARDS_REPO,
      );
    },
    [openBoardTask, askedNarrowing, config],
  );

  // "Keep", on a panel the reader has narrowed from that toolbar: the
  // same road once more, asked about the one panel they are pointing at
  // -- "narrow this panel like that, and keep it" (boards.js's
  // keepPanelQueryTask). It carries the same narrowing, which is the
  // point of it: the query is one the reader arrived at by trying, and
  // typing it out again into a prompt is where it gets lost.
  //
  // The resolved narrowing rather than the asked one, which is the pair
  // above's: this is the narrowing PanelBoard decided to offer "Keep"
  // over, so the prompt and the button that opened it are reading the
  // same thing. The two agree anyway -- with nobody asking, the resolved
  // narrowing is the default and keptQuery keeps nothing either way --
  // but a control and the task it files should not have to be argued
  // into agreement.
  const openKeepPanelQueryTask = useCallback(
    (board, panel) => {
      openBoardTask(
        keepPanelQueryTask(board, panel, { narrowing, config }),
        BOARDS_REPO,
      );
    },
    [openBoardTask, narrowing, config],
  );

  // ...and the one thing about a board this UI does write itself: where
  // it sits (grain/task-81). `place` is the drop -- which folder, and
  // which board to land above -- and boardOrder.js turns it into the
  // PATCHes that make it true, usually one and occasionally a folder's
  // worth when there is no gap left between two order keys to split.
  //
  // Written one request at a time and read back afterwards rather than
  // applied optimistically: the board list is a handful of rows, the
  // server is the thing that decides the order they come back in
  // (model.ListBoards), and a drag that appeared to work and then
  // snapped back on the next refresh would be worse than a drag that
  // takes a moment. A refresh follows a failure too, so a half-applied
  // renumber shows what actually landed rather than what was asked for.
  const moveBoard = useCallback(
    async (id, place) => {
      const moves = boardMoves(boards, id, place);
      if (moves.length === 0) return;
      try {
        for (const { id: boardId, ...fields } of moves) {
          await api(`/api/boards/${boardId}`, {
            method: "PATCH",
            body: JSON.stringify(fields),
          });
        }
        await refreshBoards();
      } catch (err) {
        showError(err);
        // Re-read anyway: a renumber that failed halfway through has
        // left the list in some state, and the honest thing to show is
        // that one. If this read fails too the banner is already up.
        try {
          await refreshBoards();
        } catch {
          /* nothing further to say */
        }
      }
    },
    [boards, refreshBoards, showError],
  );

  // ...and the second thing this UI writes itself: where a panel sits on
  // its dashboard (grain/task-368). The same call as moveBoard above,
  // one level down -- `beforeKey` is the panel to land above, or null
  // for the end, and panelOrder.js turns that into the board's whole
  // `panels` array with the new keys in it.
  //
  // One PATCH rather than moveBoard's one per row, because the array is
  // the unit /api/boards takes: a panel has no endpoint of its own, and
  // panelOrder.js's header says why it is not worth giving one. Read
  // back afterwards rather than applied optimistically, for moveBoard's
  // reason and one more: a panel is named by its position, so the panel
  // that just moved is called something else the moment it lands, and
  // the server is the only thing that knows what.
  const movePanel = useCallback(
    async (board, key, beforeKey) => {
      const panels = panelMove(board.panels || [], key, beforeKey);
      if (!panels) return;
      try {
        await api(`/api/boards/${board.id}`, {
          method: "PATCH",
          body: JSON.stringify({ panels }),
        });
        await refreshBoards();
      } catch (err) {
        showError(err);
        // Re-read anyway, moveBoard's own last word: whatever the
        // dashboard is now is the honest thing to show.
        try {
          await refreshBoards();
        } catch {
          /* nothing further to say */
        }
      }
    },
    [refreshBoards, showError],
  );

  // A report section is the same anonymous child-row order as a panel.
  // PATCH takes the board's whole sections array, then the server is read
  // back because position is also the section's temporary identity.
  const moveSection = useCallback(
    async (board, key, beforeKey) => {
      const sections = sectionMove(board.sections || [], key, beforeKey);
      if (!sections) return;
      try {
        await api(`/api/boards/${board.id}`, {
          method: "PATCH",
          body: JSON.stringify({ sections }),
        });
        await refreshBoards();
      } catch (err) {
        showError(err);
        try {
          await refreshBoards();
        } catch {
          /* nothing further to say */
        }
      }
    },
    [refreshBoards, showError],
  );

  // setViewAndCloseOpen is Sidebar's onSetView: any nav click leaves an
  // open repo -- and an open schedule, template or suite -- behind, so a
  // later click on that destination lands back on its list rather than
  // on whichever item was last open there.
  const setViewAndCloseOpen = useCallback(
    (v) => {
      closeRepoPage();
      closeOpenItems();
      // ...and an open board, for the reason the three panes above are
      // closed: a click on "Boards" itself is a click to the list of
      // them, not back to whichever one was last open.
      setOpenBoardId(null);
      setBoardWindow(null);
      setSeededBoard(null);
      setView(v);
    },
    [closeRepoPage, closeOpenItems],
  );

  // navigateFromRail is a click on the rail itself (Sidebar, and
  // PhoneNav's drawer, which draws the same element): somebody choosing
  // to leave whatever is showing, so an edit that leaving would drop is
  // asked about first -- a repo page's unsaved job box above all, which
  // has no pane of its own to ask (found in the 2026-10-12 repository
  // walkthrough). The rest of setViewAndCloseOpen's callers are
  // following a link something else drew, and ask nothing new.
  //
  // A pin of a System tab or a Settings section names it in `at`
  // (design/design/settings-panel.design.md §5.6): { systemTab } or
  // { settingsSection }, "" for the view's own default.
  const navigateFromRail = useCallback(
    (v, at = {}) => {
      if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return false;
      if (v === "system")
        setSystemTab(
          !isPhone && at.systemTab === "health" ? "" : at.systemTab || "",
        );
      if (v === "settings") {
        setSettingsSection(
          at.settingsSection || (isPhone ? "" : DEFAULT_SETTINGS_SECTION),
        );
        setSettingsItem("");
        setSettingsAnchor("");
      }
      // The rail's Processes entry is the whole list, whatever it was
      // last narrowed to; the pages it replaced, if anything still asks
      // for one, are that list narrowed to their kind (§5.4).
      const oldKind = {
        schedules: "schedule",
        templates: "template",
        suites: "suite",
      }[v];
      if (v === "processes" || oldKind) {
        setProcessQuery(oldKind ? kindNarrowing([oldKind]) : null);
        setViewAndCloseOpen("processes");
        return true;
      }
      setViewAndCloseOpen(v);
      return true;
    },
    [setViewAndCloseOpen, isPhone],
  );

  // openSchedule is a suite run's "Scheduled" chip (SuiteRunPane.jsx's
  // ScheduledChip): the schedule that started the run, opened over the
  // schedules list the way its own row there opens it, so Back returns
  // to the run.
  //
  // The schedule may have been deleted since it started the run. The id
  // is then held the way one a URL named is, and the list fetched again,
  // so keepIfListed drops it with the same 'no schedule "sched-3"' a deep
  // link to it gets, rather than leaving the address naming a pane that
  // never opens.
  const openSchedule = useCallback(
    (id) => {
      setViewAndCloseOpen("processes");
      urlItemsRef.current.schedule = id;
      setOpenScheduleId(id);
      refreshSchedules().catch(() => {});
    },
    [setViewAndCloseOpen, refreshSchedules],
  );

  // openSuiteRun is a task pane's way back to the suite run that filed
  // it (DetailOverlay's SuiteRunLink): the task closes and the run's own
  // pane opens over the suites page, the reverse of that pane's own
  // links to its tasks.
  const openSuiteRun = useCallback(
    (id) => {
      closeDetail();
      setViewAndCloseOpen("processes");
      urlItemsRef.current["suite run"] = id;
      setOpenSuiteRunId(id);
      refreshSuiteRuns().catch(() => {});
    },
    [closeDetail, setViewAndCloseOpen, refreshSuiteRuns],
  );

  // A desktop notification when something new arrives in the inbox
  // (inboxNotifications.js, grain/task-2324).
  useInboxNotifications({
    tasks,
    gates: baseBranchGates,
    tasksReady: loaded,
    gatesReady: gatesLoaded,
    onOpenTask: openTask,
    onOpenInbox: () => setViewAndCloseOpen("inbox"),
    onOpenRepo: openRepoPage,
  });

  // showStrandedValues is the Settings pane's "3 tasks still carry a
  // value this field no longer offers" followed (grain/task-201): the
  // pane counts them off the list this tab already holds, and clicking
  // the line is the trip to those tasks.
  //
  // It replaces the whole narrowing rather than adding a condition to
  // whatever was on screen. The default one sets the closed and
  // deferred tasks aside (taskFilters.js's defaultFilters) and the
  // count does not -- a closed task carrying a retired option is still
  // carrying it -- so the pane and the list it opens would otherwise be
  // counting two different things. The rail goes back to "All tasks"
  // for the same reason.
  const showStrandedValues = useCallback(
    (name) => {
      setStateFilter("all");
      setViewAndCloseOpen("tasks");
      narrow({
        search: "",
        filters: [
          { attr: `${CUSTOM_PREFIX}${name}`, op: "legacy", values: [] },
        ],
      });
    },
    [narrow, setViewAndCloseOpen],
  );
  // A named run opens over whichever page named it. With no run id this
  // remains the trip to the deployment-wide listing, optionally narrowed
  // to a repo for RepoRuns' "All runs" link.
  //
  // Existing /ci/<run> addresses remain loadable and copyable when the CI
  // page is underneath. Elsewhere the underlying page keeps its own URL:
  // these runs are bounded history and an overlay opened from a repo or
  // task is deliberately transient.
  const openCIRun = useCallback(
    (runId, repo = "") => {
      if (runId) {
        setOpenCIRunId(runId);
        return;
      }
      if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
      setViewAndCloseOpen("ci");
      setCIRepo(repo || "");
    },
    [setViewAndCloseOpen],
  );

  // openStandingSession is a click on a session in the rail: show its
  // pane, and remember which one, since a deployment may configure more
  // than the director alone.
  const openStandingSession = useCallback(
    (id) => {
      setOpenSessionId(id);
      setViewAndCloseOpen("session");
    },
    [setViewAndCloseOpen],
  );
  openStandingSessionRef.current = openStandingSession;

  // openNewSession is a click on "New session": the chat opens at once,
  // empty, and becomes a session when something is said in it
  // (NewSessionPage.jsx). Nothing is created or listed until then.
  const openNewSession = useCallback(() => {
    openStandingSession(NEW_SESSION_ID);
  }, [openStandingSession]);

  // startStandingSession is that chat's Create session, or its first
  // message: it creates the session under the name typed (`title`) --
  // or, with none, named by the daemon from the message -- with any
  // message already posted into it (POST /api/console-sessions), pins it
  // to the rail when asked, and opens the session it made. A failure is
  // reported here and rethrown, so the page keeps the message in its
  // composer.
  const startStandingSession = useCallback(
    async ({ title, message, attachments = [], model, pin = false }) => {
      let res;
      try {
        res = await api("/api/console-sessions", {
          method: "POST",
          body: JSON.stringify({
            ...(title ? { title } : {}),
            ...(message ? { message } : {}),
            attachments,
            ...(model ? { model } : {}),
          }),
        });
      } catch (err) {
        showError(err);
        throw err;
      }
      const listed = res?.sessions || [];
      if (listed.length) {
        applySessions(listed);
      }
      // The answer names what it created; the diff against what the
      // rail knew is for a daemon older than that field.
      const known = new Set(sessions.map((s) => s.id));
      const created =
        res?.created ||
        listed.filter((s) => !known.has(s.id)).at(-1)?.id ||
        listed.at(-1)?.id;
      if (created) {
        if (pin) addPinnedItem({ kind: "session", id: created });
        openStandingSession(created);
      }
      await Promise.all([refreshSessions(), refreshList()]);
    },
    [
      applySessions,
      refreshSessions,
      refreshList,
      openStandingSession,
      sessions,
      showError,
      addPinnedItem,
    ],
  );

  const deleteStandingSession = useCallback(
    async (deletedId) => {
      await Promise.all([refreshSessions(), refreshList()]);
      if (openSessionId === deletedId) {
        // null rather than "director": the first session this deployment
        // has, whatever it is called, and never a named one that may not
        // exist.
        setOpenSessionId(null);
        // ...and only a pane that was showing the deleted session moves
        // off it. The Sessions list deletes too (SessionsPage.jsx), and
        // somebody deleting a row there -- the session they last had
        // open, as often as not -- stays on the list they are reading
        // rather than being sent into whichever session comes first.
        if (view === "session") setViewAndCloseOpen("session");
      }
    },
    [refreshSessions, refreshList, openSessionId, view, setViewAndCloseOpen],
  );

  const consumeSessionPrompt = useCallback((request) => {
    setSessionPrompt((current) => (current === request ? null : current));
  }, []);

  // act runs a mutation, then re-fetches the task (and the list behind
  // it) so the screen reflects what the store now reports -- never the
  // value the UI optimistically assumed it wrote. A failure is reported
  // here and not rethrown, so it resolves to whether the mutation itself
  // landed, as actBatch does: a form or reply box keeps what was typed
  // into it on false rather than discarding it with the error
  // (grain/task-1699). A refresh failing after the write went through
  // is still reported, but counts as landed -- the edit is stored, and
  // sending it again would only post a reply twice.
  const act = useCallback(
    async (mutate, id) => {
      try {
        await mutate();
      } catch (err) {
        showError(err);
        return false;
      }
      try {
        await openTask(id);
        await refreshList();
      } catch (err) {
        showError(err);
      }
      return true;
    },
    [openTask, refreshList, showError],
  );

  // inboxAct is `act` without the overlay: run one mutation, then
  // refresh the list. The inbox's own row buttons (InboxPage.jsx) answer
  // a task where it sits, so opening its detail on the way -- which is
  // the whole difference between this and `act` -- would put a pane over
  // the list the reader is working down, once per click.
  // It resolves to whether the mutation landed, for the reason `act`
  // does.
  const inboxAct = useCallback(
    async (mutate) => {
      try {
        await mutate();
      } catch (err) {
        showError(err);
        return false;
      }
      try {
        await refreshList();
      } catch (err) {
        showError(err);
      }
      return true;
    },
    [refreshList, showError],
  );

  // actBatch is `act` (above) widened to many tasks at once: run one
  // mutation per id, in parallel, then refresh the list a single time
  // rather than once per task. Unlike `act` it never opens a detail
  // overlay -- a batch action has no one task to show. Failures are
  // collected rather than aborting the rest, since one bad id in a
  // multi-select should not stop the others from going through; it
  // reports whether every mutation landed so the caller can decide
  // whether the selection that drove it is still worth keeping.
  const actBatch = useCallback(
    async (ids, mutate) => {
      const results = await Promise.allSettled(ids.map((id) => mutate(id)));
      await refreshList();
      const failed = results.filter((r) => r.status === "rejected");
      if (failed.length > 0) {
        showError(
          new Error(
            `${failed.length} of ${ids.length} task(s) failed: ${failed[0].reason?.message || failed[0].reason}`,
          ),
        );
      }
      return failed.length === 0;
    },
    [refreshList, showError],
  );

  // Only clears the selection once a batch action fully succeeds -- on a
  // partial failure it stays as-is, so whoever is watching can see which
  // rows are still selected and retry rather than having to re-pick them.
  const runBatch = useCallback(
    (mutate) => {
      actBatch([...selected], mutate).then((ok) => {
        if (ok) clearSelection();
      });
    },
    [actBatch, selected, clearSelection],
  );

  // reorderTasks is TaskList's drag-and-drop drop handler (bwsalmon/
  // agents#476): afterId/beforeId are either the id of a task or null,
  // TaskList's own way of saying "no bound on this side" (dropped at the
  // very head or the very tail of the list), which the API expects as an
  // absent field rather than a literal null.
  const reorderTasks = useCallback(
    async (ids, afterId, beforeId) => {
      try {
        await api("/api/tasks/reorder", {
          method: "POST",
          body: JSON.stringify({
            ids,
            afterId: afterId || undefined,
            beforeId: beforeId || undefined,
          }),
        });
        await refreshList();
      } catch (err) {
        showError(err);
      }
    },
    [refreshList, showError],
  );

  // The first load: everything the shell is drawn from, asked for at
  // once and drawn only once it has all answered.
  //
  // Both halves of that are about the same thing -- an empty list is a
  // claim, and none of these lists is entitled to make it before the
  // fetch behind it lands. `boards` starting [] is what put "No boards
  // yet.", and the whole of the boards page's empty-state prose, on
  // screen for the length of the fetch on a page loaded at
  // /boards/<id>: the reader asked for a saved board and was told there
  // were none, until there was one. The rail made the same claim in
  // numbers ("All tasks 0", "Repos 0"), and the four other list panes
  // have the identical hole in them. Nor is it a pane-sized bug to fix
  // in the pane -- *which* page to draw is itself one of the questions
  // this fetch answers, since /boards/<id> can only find its board once
  // the boards are here -- so the answer is to not answer yet, which is
  // what the splash already means for config (LoadingScreen.jsx).
  //
  // Serially it was worse than one round trip's wait: config was
  // awaited first and the five lists only went out once it resolved.
  // Measured on a browser given a fraction of a CPU, which is what a
  // loaded CI runner amounts to, that reached past five seconds of
  // confident wrong answer (grain/task-235). In parallel the wait is
  // the slowest single request rather than config plus that -- so the
  // first *correct* paint lands sooner than it used to, even though the
  // splash now covers the whole of it.
  //
  // allSettled rather than all: a request that fails early must not cut
  // the wait short for the ones still coming, or the screen opens on
  // exactly the half-fetched page this is about. The first failure is
  // worth one error banner; the rest of the shell renders on whatever
  // did land, as it did before.
  useEffect(() => {
    (async () => {
      const results = await Promise.allSettled([
        refreshConfig(),
        refreshList(),
        refreshSchedules(),
        refreshTemplates(),
        refreshSuites(),
        refreshBoards(),
        refreshSessions(),
        refreshCIRuns(),
      ]);
      const failed = results.find((r) => r.status === "rejected");
      if (failed) {
        if (isUnreachable(failed.reason))
          noteUnreachable("shell", failed.reason, { immediate: true });
        else showError(failed.reason);
      } else {
        noteReached("shell");
      }
      setLoaded(true);
    })();
  }, [
    refreshConfig,
    refreshList,
    refreshSchedules,
    refreshTemplates,
    refreshSuites,
    refreshBoards,
    refreshCIRuns,
    refreshSessions,
    showError,
    noteUnreachable,
    noteReached,
  ]);

  // The suite runs, fetched on the way into the suites page rather than
  // at mount with the five lists above: they are the one list nothing
  // outside that page shows, so paying for them on every cold load of
  // every other view would buy nothing. Until now they arrived on the
  // first poll tick, which left the page's own runs list blank for a
  // few seconds after every visit -- and left a link straight to one
  // run's pane (/suites/runs/7) with nothing to open.
  useEffect(() => {
    if (view !== "processes") return;
    refreshSuiteRuns().catch(showError);
  }, [view, refreshSuiteRuns, showError]);

  // The triggers, read on the way into Processes from every repository
  // the loaded schedules, templates and suites were declared in. A
  // repository whose status cannot be read is left out rather than
  // reported: the rest of the page does not depend on it, and its own
  // page says what went wrong.
  const triggerRepos = declaringRepos({
    schedules,
    templates,
    suites,
    repos: config?.targetRepos,
  }).join("\n");
  useEffect(() => {
    if (view !== "processes") return;
    let cancelled = false;
    const repos = triggerRepos ? triggerRepos.split("\n") : [];
    Promise.all(
      repos.map((repo) =>
        api(`${repoApiRoot(repo)}/declarations`)
          .then((answer) => triggersOf(repo, answer))
          .catch(() => []),
      ),
    ).then((lists) => {
      if (!cancelled) setProcessTriggers(lists.flat());
    });
    return () => {
      cancelled = true;
    };
  }, [view, triggerRepos]);

  // The deployment's name in the browser tab (grain/task-69), beside
  // the sidebar badge Sidebar.jsx renders it as. A tab strip is where a
  // staging window and a production window actually get confused, and it
  // is the one piece of chrome the app cannot draw into -- so the name
  // goes first, since a narrow tab truncates its title from the end and
  // "grain — sta..." would say nothing this is for.
  useEffect(() => {
    const name = config?.environmentName;
    document.title = name ? `${name} — grain` : "grain";
  }, [config?.environmentName]);

  // Read into one boolean the poll effect lists among its dependencies:
  // a guard on state the effect does not depend on is checked against
  // the value it held at mount, and the poll went on re-reading every
  // list behind the access-denied screen.
  const pollDenied = Boolean(
    accessDenied || config?.accessDenied || config?.allowed === false,
  );
  useEffect(() => {
    async function poll() {
      if (polling.current || pollDenied) return;
      if (document.visibilityState === "hidden") {
        // See HIDDEN_POLL_EVERY. Quiet on failure: nobody is looking,
        // and the visible poll reports the outage the moment somebody
        // is.
        if (!notificationsEnabled()) return;
        hiddenTick.current += 1;
        if (hiddenTick.current % HIDDEN_POLL_EVERY !== 0) return;
        polling.current = true;
        try {
          await refreshList();
        } catch (err) {
          console.warn("grain: background poll failed", err);
        } finally {
          polling.current = false;
        }
        return;
      }
      // The backoff itself: skip this tick unless it is the one the
      // current streak of failures says to actually try. Ordinary
      // operation never skips -- pollBackoff stays 1, and n % 1 is
      // always 0.
      pollTick.current += 1;
      if (pollTick.current % pollBackoff.current !== 0) return;
      polling.current = true;
      try {
        // refreshConfig here (not just at mount) is what keeps
        // config.reconcilerDown -- and the banner it drives, below --
        // current: it is the one /api/config field that can flip from
        // false to true mid-session, on a process this same UI is still
        // otherwise happily serving (bwsalmon/agents#576).
        // config.agentPause is the second such field and the reason this
        // poll now also matters on the way back: a pause appears when the
        // agent's usage limit is met and disappears again when the
        // provider's window resets, with nothing on this end to notice
        // either (grain/task-132).
        await Promise.all([
          refreshList(),
          refreshConfig(),
          refreshCIRuns(),
          refreshSessions(),
        ]);
        if (openTaskId !== null && taskLoadError?.id === openTaskId) {
          // A task the address bar asked for that failed to load: try it
          // again, in full rather than conditionally (there is no body on
          // screen for an "unchanged" to mean anything against). Still
          // failing leaves the error pane up and the rest of the tick
          // going.
          const seq = openSeq.current;
          try {
            const detail = await api(`/api/tasks/${openTaskId}`);
            if (seq === openSeq.current) {
              setTaskLoadError(null);
              setDetail(detail);
            }
          } catch (err) {
            console.warn("grain: task still failing to load", err);
          }
        } else if (openTaskId !== null) {
          // Conditional for the same reason as refreshList: an open task
          // pane carries its comment and attempt history, and fetching,
          // parsing and comparing it every three seconds to say the same
          // thing is waste before re-rendering even enters the picture.
          const detail = await api(`/api/tasks/${openTaskId}`, { poll: true });
          if (detail !== UNCHANGED) {
            // Only over the same task's pane. The tick asked for the
            // task open when it started, and a click onto another one
            // can land while this read is still out; written then, the
            // old task's body would sit under the new one's selection,
            // and stay there while the new task's own conditional poll
            // answered UNCHANGED for a body it never delivered.
            setDetail((prev) =>
              prev && String(prev.id) === String(detail?.id)
                ? keepIfSame(prev, detail)
                : prev,
            );
            setSessionTask((prev) =>
              prev && String(prev.id) === String(detail?.id)
                ? keepIfSame(prev, detail)
                : prev,
            );
          }
        }
        if (view === "processes") {
          // Every list the page shows, and templates and suites for the
          // overlays' own pickers too: ScheduleOverlay's "Template" and
          // "Suite" and SuiteOverlay's templates can open at any time
          // while this page is on screen.
          await Promise.all([
            refreshSchedules(),
            refreshTemplates(),
            refreshSuites(),
            refreshSuiteRuns(),
          ]);
        } else if (view === "boards") {
          // Boards while a board or the list of them is showing, the
          // same treatment the process lists get above. Not on
          // every tick, even though the rail carries boards in every
          // view: a board arrives by an agent's pull request merging
          // somewhere else entirely, which is minutes of waiting and
          // not seconds, and paying for a third request every three
          // seconds in every view to shorten that is a poor trade. A
          // board that landed while another page was up appears on the
          // next visit to this one, or on a reload.
          await refreshBoards();
        }
        pollBackoff.current = 1;
        pollTick.current = 0;
        noteReached("shell");
      } catch (err) {
        if (isUnreachable(err)) {
          if (noteUnreachable("shell", err)) {
            pollBackoff.current = Math.min(pollBackoff.current * 2, 10);
          }
        } else {
          // Deliberately quiet -- see app.js's own poll for why.
          console.warn("grain: poll failed", err);
        }
      } finally {
        polling.current = false;
      }
    }
    const interval = setInterval(poll, POLL_INTERVAL_MS);
    pollNow.current = poll;
    const onVisible = () => {
      if (document.visibilityState === "visible") poll();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(interval);
      if (pollNow.current === poll) pollNow.current = null;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [
    openTaskId,
    taskLoadError,
    pollDenied,
    refreshBoards,
    refreshConfig,
    refreshCIRuns,
    refreshList,
    refreshSessions,
    refreshSchedules,
    refreshTemplates,
    refreshSuites,
    refreshSuiteRuns,
    noteUnreachable,
    noteReached,
    view,
  ]);

  // The other half of restartingRef: GET /api/restart-watch, a request
  // held open on the daemon that it answers the moment it is told to
  // stop. The poll above asks /api/config every POLL_INTERVAL_MS, and a
  // daemon with little to drain is gone within milliseconds of a
  // SIGTERM, so on its own the poll almost never heard "restarting"
  // and a plain restart read as "cannot be reached" (grain/task-3543).
  // One request at a time, asked again at once after a held answer and
  // no sooner than POLL_INTERVAL_MS after a quick one or a failure --
  // the poll, not this, is what reports the daemon unreachable. A hidden
  // tab stops asking, as it stops polling (HIDDEN_POLL_EVERY), and asks
  // again the moment it is shown.
  //
  // It is also how the page hears that grain is back. After a failed
  // ask, the next one is ?quick=1, which the daemon answers at once
  // rather than holding, and an answer that finds the main poll backed
  // off resets its backoff and polls now: otherwise the banner waited
  // for the poll's next backed-off tick, up to half a minute after
  // grain was answering again (grain/task-2258).
  const watchingRestart = Boolean(config) && !pollDenied;
  useEffect(() => {
    if (!watchingRestart) return undefined;
    let stopped = false;
    let asking = false;
    let timer = null;
    let failed = false;
    async function watch() {
      timer = null;
      if (stopped || asking || document.visibilityState === "hidden") return;
      asking = true;
      const started = Date.now();
      try {
        const answer = await api(
          failed ? "/api/restart-watch?quick=1" : "/api/restart-watch",
        );
        failed = false;
        if (stopped) return;
        if (pollBackoff.current > 1) {
          pollBackoff.current = 1;
          pollTick.current = 0;
          pollNow.current?.();
        }
        if (answer?.restarting) {
          restartingRef.current = true;
          // A request that failed while this answer was on its way
          // already put up "cannot be reached"; it was this restart.
          setDisconnection((prev) =>
            prev && !prev.restarting
              ? { ...prev, reason: RESTARTING_REASON, restarting: true }
              : prev,
          );
        }
      } catch {
        // Unreachable, refused or anything else: the poll says so.
        failed = true;
      }
      asking = false;
      if (stopped) return;
      const wait = Math.max(0, POLL_INTERVAL_MS - (Date.now() - started));
      timer = setTimeout(watch, wait);
    }
    const onVisible = () => {
      if (document.visibilityState === "visible" && timer === null) watch();
    };
    document.addEventListener("visibilitychange", onVisible);
    watch();
    return () => {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [watchingRestart]);

  // The base branches nothing is judging, on a slower clock of their own
  // (GATE_INTERVAL_MS above says why a minute). Its own effect rather
  // than a line in the poll above for that reason alone -- unlike the
  // session below, this is wanted in every view, since the rail counts
  // it wherever the reader is.
  //
  // A hidden tab reads them only while notifications are on, which is
  // what a gate finding reaching that tab is for; with them off it reads
  // nothing at all, as HIDDEN_POLL_EVERY's tab does, and catches up the
  // moment it is shown.
  useEffect(() => {
    const load = () => {
      if (document.visibilityState === "hidden" && !notificationsEnabled())
        return;
      refreshBaseBranchGates().catch((err) => {
        // Quiet, like the poll itself. A deployment whose store is
        // unreachable has a banner already, and a second one a minute
        // saying the same thing would only push the first off the
        // screen.
        console.warn("grain: base-branch gate poll failed", err);
      });
    };
    load();
    const interval = setInterval(load, GATE_INTERVAL_MS);
    const onVisible = () => {
      if (document.visibilityState === "visible") load();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(interval);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [refreshBaseBranchGates]);

  // The open session's conversation, polled while its pane is up. Its
  // own effect rather than a line in the poll above, for the reason the
  // pane is not a task detail: it is open on a task the shell is not
  // otherwise holding, and it is the only view that wants it.
  useEffect(() => {
    if (view !== "session") return undefined;
    let inFlight = false;
    const load = () => {
      if (inFlight) return;
      inFlight = true;
      loadSessionTask()
        .then(
          () => noteReached("session"),
          (err) => {
            if (isUnreachable(err)) {
              noteUnreachable("session", err);
            } else {
              // grain answered, if only to say no.
              noteReached("session");
              // Quiet, like the main poll: a failed tick is followed by
              // another one three seconds later, and a banner per tick
              // would be the whole screen for as long as the daemon stays
              // out of reach.
              console.warn("grain: session poll failed", err);
            }
          },
        )
        .finally(() => {
          inFlight = false;
        });
    };
    load();
    const interval = setInterval(load, POLL_INTERVAL_MS);
    return () => {
      clearInterval(interval);
      // Leaving the pane ends its poll, so whatever it last saw stops
      // counting towards the banner.
      noteReached("session");
    };
  }, [view, loadSessionTask, noteUnreachable, noteReached]);

  // The clock, ticking on its own (CLOCK_INTERVAL_MS above says why it
  // has to now).
  //
  // Nothing reads `clock`, deliberately: it is state so that setting it
  // renders, and what the views need from a render is the chance to read
  // the clock themselves, which they already do as they draw (their own
  // `now = Date.now()` defaults). A value passed down instead would be
  // the same tick with a prop threaded through every view to say it.
  const [, setClock] = useState(0);
  useEffect(() => {
    const tick = setInterval(() => setClock((n) => n + 1), CLOCK_INTERVAL_MS);
    return () => clearInterval(tick);
  }, []);

  // Opens whatever task /tasks/:id in the URL the page loaded with names
  // -- the view/settingsSection equivalent of this runs synchronously in
  // useState's own initializer above, but a task's detail needs an
  // /api/tasks/:id round trip first, which only a mount effect can do.
  // Runs once: openTask is a stable callback (its only dependency,
  // showError, never changes identity either), so this never re-fires
  // just because some other state the effect doesn't list changed.
  useEffect(() => {
    const { taskId } = parsePath(window.location.pathname);
    if (taskId) openTaskFromUrl(taskId);
  }, [openTaskFromUrl]);

  // Keeps the address bar in sync with view/openTaskId/openScheduleId/
  // settingsSection -- everything paths.js's buildPath encodes -- so every
  // way of reaching a sub-page (a sidebar click, closing an overlay, the
  // popstate handler below) ends up shareable/bookmarkable, not just a
  // page loaded fresh from one. Skips the update entirely once the
  // computed path already matches the address bar, which is what stops
  // this from fighting the popstate handler: a back/forward navigation
  // updates window.location itself before this effect ever runs, so by
  // the time it does there is nothing left to push.
  const mountedRef = useRef(false);
  // The address the page on screen answers to, so a Back the reader
  // takes back (an edited pane they chose to keep) can be put back.
  const shownUrlRef = useRef(window.location.pathname + window.location.search);
  // ...and that address's history entry, which carries the page behind
  // an open task (the effect below), so putting it back keeps that too.
  const shownEntryRef = useRef(window.history.state);
  useEffect(() => {
    if (urlTask) return;
    // Not before the first load has answered: the first address written
    // replaces rather than pushes, and an item the address names that
    // is not there is only known to be missing once its list has come.
    if (!loaded) return;
    const dropped = droppedRef.current;
    droppedRef.current = null;
    if (dropped?.missing.length) showError(dropped.missing.join("; "));
    // A Kanban board, a timeline or a report naming no query of its own
    // already opens on NO_NARROWING (boards.js's boardNarrowing), so the
    // empty narrowing it is seeded with is its own question rather than
    // one the reader has asked of it. Written as such it would hang
    // paths.js's "?f=none" on every bare link to one of those boards;
    // left unwritten, a reload re-seeds the same empty question.
    const openBoardRow =
      view === "boards" && openBoardId !== null
        ? boards.find((b) => b.id === openBoardId)
        : null;
    const boardAsksNothing =
      !!openBoardRow &&
      boardNarrowing(openBoardRow) !== null &&
      queryFromNarrowing(boardNarrowing(openBoardRow)) === "";
    const askedOnBoard =
      boardAsksNothing &&
      askedNarrowing &&
      queryFromNarrowing(askedNarrowing) === ""
        ? null
        : askedNarrowing;
    const where = {
      view,
      taskId: openTaskId,
      repo: openRepo,
      repoTab,
      repoComparison,
      showReleases: releasesOpen,
      release: releasesOpen ? openRelease : null,
      boardId: openBoardId,
      scheduleId: openScheduleId,
      templateId: openTemplateId,
      suiteId: openSuiteId,
      suiteRunId: openSuiteRunId,
      ciRunId: openCIRunId,
      sessionId: openSessionId,
      settingsSection,
      settingsItem,
      systemTab,
      // The Metrics pane's own scope, which is in the URL for the same
      // reason a narrowed list's is: it is what the report on screen is
      // measured over, so a report somebody scoped is a link they can
      // send (paths.js).
      metricsNarrowing,
      metricsWindow,
      boardWindow,
      showNewTask,
      newTaskFields,
      // What has been *asked*, not what is in force: a list nobody has
      // narrowed lives at "/" the way it always has, and the deployment
      // decides what that means when it is opened (defaultNarrowing).
      // From the first change onwards the query says the whole of what
      // the list is narrowed by, the default condition included, so a
      // link reopens exactly the list it was copied from -- including
      // one whose reader took that condition off. A reader who takes
      // every condition off is written as paths.js's empty marker, so
      // the list they cleared survives a reload rather than reopening on
      // the default it was cleared of.
      narrowing: askedOnBoard,
      stateFilter,
      processQuery,
    };
    const path = buildPath(where);
    // A task's address names only the task, not the page it was opened
    // over -- a board, a repo's page, the inbox -- so that page is kept in
    // the history entry's own state instead. Back and Forward onto the
    // entry read it (onPopState below), so the pane comes back over the
    // page it was opened on, and its own "← Back" returns there rather
    // than to the task list.
    const entry = openTaskId
      ? { behind: buildPath({ ...where, taskId: null }) }
      : null;
    if (path !== window.location.pathname + window.location.search) {
      // Going somewhere pushes; narrowing where you already are
      // replaces. A narrowing is a change of question rather than of
      // page, and the search box writes one per keystroke -- pushing
      // those would fill the history with a step per character and make
      // Back mean "delete the last letter I typed" rather than "the
      // page I was on". The URL is still the current one either way,
      // which is the whole point: it can be copied, reloaded and sent.
      // The toolbar's own Clear button is how a narrowing is undone.
      //
      // The very first correction (e.g. an unrecognized path normalized
      // back to "/") replaces rather than pushes too, so a mistyped or
      // stale URL doesn't leave an extra "back" step to it in history.
      //
      // A pick in the rail is going somewhere, even though it stays on
      // the same path: Queued and Closed are different lists, which is
      // what Back is expected to step between.
      const stateParam = (search) =>
        new URLSearchParams(search).get("state") || "";
      // Picking another tab of the repo page on screen is a change of
      // tab rather than of page too, as it was when the tab rode in the
      // hash: Back leaves the page rather than stepping through its tabs.
      const shown = parsePath(window.location.pathname);
      const otherTab =
        view === "repos" &&
        !!openRepo &&
        !openTaskId &&
        !showNewTask &&
        !releasesOpen &&
        !repoComparison &&
        shown.view === "repos" &&
        shown.repo === openRepo &&
        !shown.taskId &&
        !shown.showNewTask &&
        !shown.showReleases &&
        !shown.repoComparison;
      const samePage =
        otherTab ||
        (path.split("?")[0] === window.location.pathname &&
          stateParam(path.split("?")[1] || "") ===
            stateParam(window.location.search));
      //
      // So does closing a pane whose item a list fetch did not find
      // (keepIfListed above): the address it corrects names nothing, so
      // Back to it would only be corrected all over again.
      // An address Processes replaced (/schedules/sched-1, /suites) is
      // rewritten in place too: it is the same page under its new name
      // (design/design/settings-panel.design.md §5.4), and a step back to it would
      // only redirect forward again.
      const oldProcessPath =
        /^\/(schedules|templates|suites|processes\/triggers)(\/|$)/.test(
          window.location.pathname,
        ) ||
        // A bare /settings becoming /settings/models is the same page
        // renamed everywhere but a phone, where the bare path is the
        // section list: picking Models from it is a step Back has to
        // return from, as it does for every other section (2026-11-01
        // responsive walkthrough).
        (!isPhone &&
          view === "settings" &&
          settingsSection === DEFAULT_SETTINGS_SECTION &&
          window.location.pathname === "/settings") ||
        (view === "session" &&
          !openTaskId &&
          /^\/(tasks|director)(\/|$)/.test(window.location.pathname));
      if (mountedRef.current && !samePage && !dropped && !oldProcessPath) {
        const anchorHash =
          view === "settings" && settingsAnchor ? `#${settingsAnchor}` : "";
        window.history.pushState(entry, "", path + anchorHash);
      } else {
        // A correction of the address on screen -- the first write on
        // load -- keeps its #fragment, which is a
        // Settings section's anchor: path is built from the route and
        // never carries one. A repo page's tab in the hash -- an address
        // from before the tab joined the path -- is in the path now, so
        // it is dropped rather than kept to say the same thing twice.
        const { hash } = window.location;
        const keptHash =
          view === "repos" && openRepo && isRepoTab(hash.replace(/^#/, ""))
            ? ""
            : hash;
        window.history.replaceState(entry, "", path + keptHash);
      }
    }
    shownUrlRef.current = window.location.pathname + window.location.search;
    shownEntryRef.current = window.history.state;
    mountedRef.current = true;
  }, [
    view,
    openTaskId,
    openRepo,
    repoTab,
    repoComparison,
    releasesOpen,
    openRelease,
    openBoardId,
    openScheduleId,
    openTemplateId,
    openSuiteId,
    openSuiteRunId,
    openCIRunId,
    openSessionId,
    settingsSection,
    settingsAnchor,
    settingsItem,
    systemTab,
    metricsNarrowing,
    metricsWindow,
    boardWindow,
    showNewTask,
    newTaskFields,
    askedNarrowing,
    boards,
    stateFilter,
    processQuery,
    loaded,
    config,
    urlTask,
    showError,
    isPhone,
  ]);

  // Mirrors the browser's own back/forward buttons onto the same state
  // buildPath/parsePath already govern everything else through.
  useEffect(() => {
    function onPopState() {
      // A field that saves as it is left -- a repo's prompt extension and
      // setup command, Team settings' boxes -- is left by Back too. A
      // click anywhere else blurs it first, but Back, Alt+Left and a
      // phone's back gesture unmount it with focus still inside, and its
      // onBlur never runs: the edit was dropped without a word (found in
      // the 2026-10-12 repository walkthrough). Blurring it here, while
      // the page it belongs to is still mounted, commits it the way any
      // other way out would.
      const focused = document.activeElement;
      if (focused && focused !== document.body) focused.blur?.();
      // Back and Forward leave an open pane without going through any of
      // its own ways out, so its unsaved-edit question is asked here.
      // Keeping the edit means staying: the address bar has already
      // moved, so the pane's own address is pushed back over it.
      const here = window.location.pathname + window.location.search;
      if (
        here !== shownUrlRef.current &&
        leavingLosesEdits() &&
        !window.confirm(DISCARD_PROMPT)
      ) {
        window.history.pushState(
          shownEntryRef.current,
          "",
          shownUrlRef.current,
        );
        // Staying, so hand focus back to whatever the blur above took it
        // from. Left on <body>, it is outside the pane's dialog until
        // MUI's focus trap notices, and an Escape pressed before then
        // never reaches the dialog (the same race Overlay's handleClose
        // closes for Escape itself).
        if (focused && focused !== document.body && focused.isConnected)
          focused.focus?.();
        return;
      }
      shownUrlRef.current = here;
      shownEntryRef.current = window.history.state;
      let parsed = parsePath(window.location.pathname, window.location.search);
      // The page an open task was opened over, from its history entry
      // (the URL effect above): the task is the address's, everything
      // behind it is that page's.
      const behind = window.history.state?.behind;
      if (parsed.taskId && typeof behind === "string") {
        const at = behind.indexOf("?");
        parsed = {
          ...(at < 0
            ? parsePath(behind)
            : parsePath(behind.slice(0, at), behind.slice(at))),
          taskId: parsed.taskId,
        };
      }
      // A path that carries no narrowing puts the toolbar back to its
      // default -- Back out of a narrowed list is how you undo the
      // narrowing, so it cannot be the one thing that survives.
      setAskedNarrowing(parsed.narrowing || null);
      // Only where the list is what is on screen: an open task or an
      // overlay leaves the list behind it as it was, rail pick included.
      if (showsStateFilter(parsed)) setStateFilter(parsed.stateFilter || "all");
      setShowNewTask(parsed.showNewTask === true);
      setNewTaskFields(parsed.newTaskFields || null);
      // Back and Forward step between Settings sections and System tabs
      // as well as in and out of the views, since both are in the path.
      setSettingsSection(parsed.settingsSection || "");
      setSettingsItem(parsed.settingsItem || "");
      setSettingsAnchor(
        parsed.view === "settings" && window.location.hash
          ? decodeURIComponent(window.location.hash.slice(1))
          : "",
      );
      setSystemTab(parsed.systemTab || "");
      // Back out of a scoped report is how that scope is undone, exactly
      // as it is for a narrowed list above.
      setMetricsNarrowing(parsed.metricsNarrowing || null);
      setMetricsWindow(parsed.metricsWindow || null);
      setProcessQuery(parsed.processQuery || null);
      setView(parsed.view);
      setOpenRepo(parsed.repo || null);
      setRepoTab(repoTabAt(parsed, window.location.hash));
      setRepoComparison(parsed.repoComparison || null);
      setReleasesOpen(parsed.showReleases === true);
      setOpenRelease(parsed.release || null);
      // The board a Back/Forward landed on is seeded from the URL like
      // everything else here: the effect above applies its own
      // narrowing only when the path it arrived on narrowed nothing.
      setOpenBoardId(parsed.boardId || null);
      setBoardWindow(parsed.boardWindow || null);
      setSeededBoard(null);
      setOpenScheduleId(parsed.scheduleId || null);
      setOpenTemplateId(parsed.templateId || null);
      setOpenSuiteId(parsed.suiteId || null);
      setOpenSuiteRunId(parsed.suiteRunId || null);
      setProcessResetSeq((n) => n + 1);
      setOpenCIRunId(parsed.ciRunId || null);
      setOpenSessionId(parsed.sessionId || null);
      urlItemsRef.current = urlItems(parsed);
      if (parsed.taskId) {
        openTaskFromUrl(parsed.taskId);
      } else {
        closeDetail();
      }
    }
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [openTaskFromUrl, closeDetail]);

  // taskListPane is the flat task list plus the batch-actions bar under
  // it. The narrowing is the task list's own URL-backed question.
  //
  // `landmark` is false when the pane sits under a saved board's page,
  // which is the main landmark itself (BoardPage.jsx); the same holds
  // for the three panes below.
  const taskListPane = (list, filter, { landmark = true } = {}) => (
    <>
      <TaskList
        landmark={landmark}
        tasks={list}
        stateFilter={filter}
        config={config}
        narrowing={narrowing}
        onNarrow={narrow}
        onOpenTask={openTask}
        selected={selected}
        onToggleSelect={toggleSelect}
        onSelectAll={setSelection}
        onReorder={reorderTasks}
        onShownChange={setShownIds}
        onRefresh={refreshList}
        showError={showError}
      />
      <BatchActionsBar
        selection={selectedTasks}
        shownIds={shownIds}
        tasks={tasks}
        config={config}
        templates={templates}
        onRun={runBatch}
        onClear={clearSelection}
      />
    </>
  );

  // taskBoardPane is the board's columns plus the same batch-actions bar
  // the flat list uses, so a card's checkbox feeds the same selection.
  // A saved board can supply columns of its own; otherwise the browser's
  // shared board layout applies (board.js).
  //
  // It takes no state filter for the reason the board never had
  // one: the columns *are* the board's answer to which states it is
  // about, and a board pre-filtered to one state would be a single
  // column. The narrowing it does take is the one every task view
  // shares (see taskListPane), so a search survives the switch between
  // a repo's list and a repo's board as it does the switch between
  // pages.
  const taskBoardPane = (list, { columns, landmark = true } = {}) => (
    <>
      <TaskBoard
        landmark={landmark}
        tasks={list}
        fixedColumns={columns}
        config={config}
        narrowing={narrowing}
        onNarrow={narrow}
        onOpenTask={openTask}
        selected={selected}
        onToggleSelect={toggleSelect}
        onSelectAll={setSelection}
        onReorder={reorderTasks}
        onShownChange={setShownIds}
      />
      <BatchActionsBar
        selection={selectedTasks}
        shownIds={shownIds}
        tasks={tasks}
        config={config}
        templates={templates}
        onRun={runBatch}
        onClear={clearSelection}
      />
    </>
  );

  // taskGanttPane renders a `gantt` board's tasks as bars against a clock
  // (GanttChart.jsx, grain/task-55). It also sat behind /gantt until
  // grain/task-162 took the fixed destinations off the rail.
  //
  // No batch-actions bar under it, unlike the two above, because there is
  // no checkbox on a chart row to feed one (GanttChart's own doc comment
  // on why). A selection made on the list survives being looked at from
  // here -- App holds it either way -- and the bar comes back with the
  // view that offers the gesture.
  //
  // Like the board it takes no state filter: what a chart is about is the
  // range it is set to, and the narrowing over it is the one every task
  // view shares.
  const taskGanttPane = (list, { landmark = true } = {}) => (
    <GanttChart
      landmark={landmark}
      tasks={list}
      config={config}
      narrowing={narrowing}
      onNarrow={narrow}
      onOpenTask={openTask}
    />
  );

  // taskTreePane renders a `tree` board's tasks nested by who filed what
  // (TaskTreeBoard.jsx): the tree a task's own page draws, over the
  // board's narrowing, which it applies itself as the timeline does. No
  // batch bar, for the timeline's reason: a tree row has no checkbox to
  // feed one.
  const taskTreePane = (list, { landmark = true } = {}) => (
    <TaskTreeBoard
      landmark={landmark}
      tasks={list}
      config={config}
      narrowing={narrowing}
      onNarrow={narrow}
      onOpenTask={openTask}
    />
  );

  // openPanelView is a click on a panel's heading, or on one of the
  // state counts: leave the dashboard for the full view that panel is a
  // small copy of, asking exactly the question the panel was asking
  // (panels.js). The link carries the same thing as a URL, so this is
  // the in-app half of a link that can also be copied and sent
  // (DetailOverlay's own treatment).
  //
  // Three of the four panels name a task view. The throughput one names
  // the Metrics pane, and opens it over the dashboard scoped to exactly
  // the report the panel was drawing -- the pane's own narrowing, which
  // is why it can be handed one at all (panels.js's panelDestination).
  // It takes whatever panelDestination returns, so the click and the
  // href on the same heading are one decision rather than two.
  const openPanelView = useCallback(
    (destination) => {
      if (destination.view === "system") {
        setMetricsNarrowing(destination.metricsNarrowing || null);
        setMetricsWindow(destination.metricsWindow || null);
        setSystemTab(destination.systemTab || "");
        setViewAndCloseOpen("system");
        return;
      }
      setAskedNarrowing(destination.narrowing);
      setViewAndCloseOpen(destination.view);
    },
    [setViewAndCloseOpen],
  );

  // panelBoardPane is the dashboard itself (PanelBoard.jsx): every task,
  // and the two ways off it -- a panel's own view, and the task that
  // adds a panel.
  //
  // The narrowing it takes is the one every task view shares, exactly
  // as the three panes above take it (grain/task-111): a dashboard is
  // opened on its board's own question (openBoard), and the toolbar
  // over the panels is how the reader narrows that question further
  // without editing the board. What it narrows is what a panel with no
  // query of its own inherits (panels.js's panelNarrowing).
  const panelBoardPane = (board) => (
    <PanelBoard
      board={board}
      tasks={tasks}
      config={config}
      narrowing={narrowing}
      onNarrow={narrow}
      onOpenTask={openTask}
      onOpen={openPanelView}
      onAddPanel={openAddPanelTask}
      onKeepPanelQuery={openKeepPanelQueryTask}
      onMovePanel={movePanel}
    />
  );

  // reportBoardPane is a report board (ReportBoard.jsx, grain/task-83):
  // the measurement report rather than a view of these tasks. What it is
  // about is the narrowing -- the board's own question when the board is
  // opened, and whatever the toolbar has since been asked -- which it
  // sends to the daemon as a scope rather than applying here. The tasks
  // it is handed are only for that toolbar's menus, so "Repo" offers the
  // repos there are to scope it by.
  const reportBoardPane = (board) => (
    <ReportBoard
      board={board}
      config={config}
      tasks={tasks}
      narrowing={narrowing}
      onNarrow={narrow}
      onOpenTask={openTask}
      onOpen={openPanelView}
      showError={showError}
      onMoveSection={moveSection}
      pickedWindow={boardWindow}
      onPickWindow={setBoardWindow}
    />
  );

  // Every timestamp under here is printed in the deployment's own zone
  // rather than the browser's (grain/task-368) -- config is loaded
  // before the first paint, so nothing renders in one zone and corrects
  // itself into another.
  const openNewTask = () => {
    setNewTaskRepo(null);
    setNewTaskBoard(view === "boards" ? openBoardId : null);
    setNewTaskPrefill(null);
    setNewTaskFields(null);
    setShowNewTask(true);
  };

  // The board this page is showing, or null -- resolved against the
  // list rather than fetched, the same way an open schedule is: App
  // already holds every board.
  const openBoardRow = boards.find((b) => b.id === openBoardId) || null;

  // A board loaded cold from its URL is found here a render before the
  // effect above seeds its question into the narrowing, and a pane that
  // asks the daemon for something on mount -- a report board, or a
  // dashboard's metrics panel -- asked /api/metrics once over the whole
  // deployment before asking again under the board's own scope
  // (grain/task-1755). The answer was discarded, but the computation was
  // not. Until the seed lands those two panes wait; a board asking
  // nothing of its own (boardNarrowing null) seeds nothing, so has
  // nothing to wait for.
  const boardAwaitingSeed =
    openBoardRow !== null &&
    askedNarrowing === null &&
    seededBoard !== openBoardRow.id &&
    boardNarrowing(openBoardRow) !== null;

  // openNewTaskWithFields is the task pane's "File a task with these
  // fields" link (grain/task-53), followed in-app: the same /new path
  // the link's own href names, reached without a page load. Closing the
  // task under it is what makes the form the thing on screen -- an
  // overlay opened behind an open task would be invisible.
  const openNewTaskWithFields = useCallback(
    (fields) => {
      closeDetail();
      setNewTaskRepo(null);
      setNewTaskBoard(null);
      setNewTaskPrefill(null);
      setNewTaskFields(
        fields && Object.keys(fields).length > 0 ? fields : null,
      );
      setShowNewTask(true);
    },
    [closeDetail],
  );

  // openSettings opens Settings on a section, and openSystem System on a
  // tab. openSettings also takes the id an older caller names -- a System
  // tab of the old overlay ("workflow", "integrations") or Team settings'
  // "connections" -- and goes where /admin/<id> redirects (§3.5).
  const openSystem = useCallback(
    (tab = "") => {
      if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
      // A tab that has since moved into Settings (Upgrade) opens its
      // section there, as /system/<tab> does.
      const moved = tab ? systemDestination(tab) : null;
      if (moved?.view === "settings") {
        setSettingsSection(moved.settingsSection);
        setSettingsAnchor("");
        setViewAndCloseOpen("settings");
        return;
      }
      setSystemTab(tab === "health" && !isPhone ? "" : tab);
      setViewAndCloseOpen("system");
    },
    [setViewAndCloseOpen, isPhone],
  );
  const openSettings = useCallback(
    (section = DEFAULT_SETTINGS_SECTION, anchor = "") => {
      if (!section) {
        if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
        setSettingsSection("");
        setSettingsItem("");
        setSettingsAnchor("");
        setViewAndCloseOpen("settings");
        return;
      }
      const asked = DEPLOYMENT_SETTINGS_TAB[section] || section;
      const dest = settingsSectionFor(asked)
        ? { view: "settings", settingsSection: settingsSectionFor(asked) }
        : oldAdminDestination(asked);
      if (dest.view === "system") {
        openSystem(dest.systemTab);
        return;
      }
      if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
      setSettingsSection(dest.settingsSection);
      setSettingsItem("");
      setSettingsAnchor(anchor);
      setViewAndCloseOpen("settings");
    },
    [setViewAndCloseOpen, openSystem],
  );
  const openDeploymentSettings = openSettings;

  // A bare /settings is the section list on a phone and Models
  // everywhere else, so the address says Models there (§3.2).
  useEffect(() => {
    if (view === "settings" && !settingsSection && !isPhone)
      setSettingsSection(DEFAULT_SETTINGS_SECTION);
  }, [view, settingsSection, isPhone]);
  // And a /system/health is the bare /system everywhere but a phone,
  // where the bare path is the list of tabs (settingsSections.js's
  // systemTabFor keeps "health" for that reason).
  useEffect(() => {
    if (view === "system" && systemTab === "health" && !isPhone)
      setSystemTab("");
  }, [view, systemTab, isPhone]);

  const handleLogout = useCallback(async () => {
    let redirect = "";
    try {
      const res = await api("/api/auth/logout", { method: "POST" });
      if (typeof res?.redirect === "string") {
        redirect = res.redirect;
      }
    } catch {
      // Ignored
    }
    if (!redirect && config?.authMethod === "iap") {
      redirect = "/_gcp_iap/clear_login_cookie";
    }
    if (redirect) {
      window.location.assign(redirect);
      return;
    }
    // The page itself is what the daemon gates: loaded again without the
    // session it is the access-code sign-in.
    onReload();
  }, [config?.authMethod, onReload]);

  // The persistent sandboxes, for the rail's Staging group. Quiet on
  // failure: a deployment that supervises none answers an empty list,
  // and one whose daemon is unreachable has a banner already.
  useEffect(() => {
    let live = true;
    const load = async () => {
      // Not in a hidden tab: nobody is looking at the dots.
      if (document.visibilityState === "hidden") return;
      try {
        const res = await api("/api/persistent-sandboxes");
        if (live)
          setPersistentSandboxes((prev) =>
            keepIfSame(prev, Array.isArray(res) ? res : []),
          );
      } catch (err) {
        console.warn("grain: persistent sandbox poll failed", err);
      }
    };
    load();
    const interval = setInterval(load, STAGING_INTERVAL_MS);
    document.addEventListener("visibilitychange", load);
    return () => {
      live = false;
      clearInterval(interval);
      document.removeEventListener("visibilitychange", load);
    };
  }, []);

  // Open operator notifications (notifications.js, docs/design/
  // durable-tasks.md DT-5): rows of the alerts bell below, and of the
  // Inbox, whose rail chip counts them too.
  const [notifications, reloadNotifications] = useOpenNotifications();

  // The one nav there is, in both shells: on a phone PhoneNav puts this
  // exact element inside its drawer rather than beside the page.
  const sidebar = (
    <Sidebar
      pinFooter={isPhone}
      collapsed={sidebarCollapsed}
      onToggleCollapsed={isPhone ? undefined : toggleSidebarCollapsed}
      config={config}
      view={view}
      systemTab={systemTab}
      settingsSection={settingsSection}
      onSetView={navigateFromRail}
      tasks={tasks}
      notificationCount={notifications.length}
      schedules={schedules}
      templates={templates}
      suites={suites}
      processTriggers={processTriggers}
      ciRuns={ciRuns}
      boards={boards}
      openBoardId={openBoardId}
      onOpenBoard={openBoard}
      sessions={sessions}
      persistentSandboxes={persistentSandboxes}
      openSessionId={openSession?.id || null}
      onOpenSession={(id) => {
        if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
        openStandingSession(id);
      }}
      onNewSession={openNewSession}
      onOpenCustomize={() => setShowCustomizeSidebar(true)}
      onNewBoard={openNewBoardTask}
      stateFilter={stateFilter}
      onSetFilter={selectStateFilter}
      onOpenSystem={() => openSystem("")}
      onOpenUpgrade={() => openSettings("upgrade")}
      onOpenNewTask={openNewTask}
    />
  );

  // The title of the page header rail (components/PageHeader.jsx), as
  // " / "-separated crumbs: the overlay on top when there is one, then
  // an open task, then the view under them and where in it the reader
  // is. grain's own labels are lower case; a repository's, a board's or
  // a session's name is the reader's own and keeps its case.
  const viewTitle =
    view === "repos" && openRepo
      ? `repositories / ${openRepo}${
          releasesOpen
            ? " / releases"
            : repoComparison !== null
              ? " / pull request"
              : repoTabCrumb(repoTab)
        }`
      : view === "settings"
        ? `settings / ${settingsSectionLabel(settingsSection || DEFAULT_SETTINGS_SECTION).toLowerCase()}`
        : view === "system"
          ? `system / ${systemTabLabel(systemTab || "health").toLowerCase()}`
          : view === "boards" && openBoardRow
            ? `boards / ${openBoardRow.name}`
            : view === "session" && newSessionOpen
              ? "sessions / new chat"
              : view === "session" && openSession
                ? `sessions / ${openSession.title || openSession.name || openSession.id}`
                : VIEW_LABELS[view] || "tasks";
  const pageTitle = showNewTask
    ? "new task"
    : openTaskId
      ? `task ${openTaskId}`
      : viewTitle;
  const pageIcon =
    showNewTask || openTaskId
      ? "tasks"
      : view === "session" && openSession?.id === "director"
        ? "director"
        : VIEW_GLYPHS[view] || "tasks";

  // The alerts bell (components/AlertsButton.jsx): every standing
  // warning config describes for this reader -- pauses, a stopped
  // reconcile loop, settings not reaching the state repository, the
  // deployment's setup steps left, a red base branch whose repair pull
  // request is waiting -- where each used to be a banner across the top
  // of every page (grain/task-4008). At the far end of
  // every page's header rail, or on a phone in the bar PhoneNav keeps on
  // screen.
  const alerts = useMemo(
    () =>
      sortAlerts([
        ...collectAlerts(config, preferredTimeZone || config?.timeZone, tasks),
        ...notificationAlerts(notifications),
      ]),
    [config, preferredTimeZone, tasks, notifications],
  );
  const alertsButton = (
    <AlertsButton
      alerts={alerts}
      onChanged={() => Promise.all([refreshConfig(), reloadNotifications()])}
      showError={showError}
      onOpenConfigRepo={() => openSettings("config-repo")}
      onOpenPower={() => openSystem("power")}
      onOpenConnection={() => openDeploymentSettings("models")}
      onOpenSetup={({ tab, view: target }) =>
        target ? setViewAndCloseOpen(target) : openDeploymentSettings(tab)
      }
      onOpenTask={openTask}
      onAct={act}
      onOpenHealth={() => openSystem("health")}
    />
  );

  // The far end of every page's header rail: the bell, System and the
  // gear. A phone has no rail: PhoneNav's bar holds the bell and the
  // gear and System is the row at the foot of the drawer
  // (design/design/settings-panel.design.md §4.1).
  const globalButtons = (
    <GlobalButtons
      alerts={alertsButton}
      view={view}
      config={config}
      onOpenSystem={() => openSystem("")}
      onOpenSettings={() => openSettings()}
      onOpenAccess={() => openSettings("access")}
      onLogout={handleLogout}
    />
  );

  // The rail's middle box, once it has mounted: what PageHeaderActions
  // under it portals each page's own header row into. State rather than
  // a ref so the page re-renders into it the moment it exists.
  const [pageHeaderSlot, setPageHeaderSlot] = useState(null);

  // Which page the main pane is showing, as the page boundaries' resetKey:
  // a page that threw is retried as soon as this changes, so the rail or
  // Back recovers it without a reload (components/ErrorBoundary.jsx).
  const paneKey = JSON.stringify([
    view,
    openRepo,
    repoComparison,
    releasesOpen,
    openBoardId,
    openSessionId,
    stateFilter,
  ]);

  return (
    <TimeZoneProvider zone={preferredTimeZone || config?.timeZone}>
      <div className="app-shell">
        {/* The one standing banner left (grain/task-4008): while
          this is up, nothing else on the page -- the alerts panel's
          own claims about the deployment included -- can be trusted,
          since none of it could be re-read to confirm it. Every other
          standing warning is a row in the alerts panel the top bar's
          bell opens (alerts.js, components/AlertsButton.jsx). */}
        <DisconnectedBanner disconnection={disconnection} />
        {/* And the one fact about the deployment that stands above the
          top bar too: an install still in host mode, which v2.3.0 no
          longer supports and whose CLIs may have nowhere to run until
          the installer migrates it (components/InstallNotice.jsx). */}
        <InstallNoticeBanner notice={config?.installNotice} />
        {/* The rail and the page, under whatever the strip above is
          saying: see .app-body in style.css for why the two are a row
          of their own rather than children of the shell directly. */}
        <div className="app-body">
          {/* Nothing until the first load has answered -- see the mount
            effect for why an unfetched list must not be drawn as an
            empty one. config === null is still its own condition
            because a /api/config that fails leaves it null with the
            load settled, and every pane below reads it. */}
          {accessDenied || config?.accessDenied || config?.allowed === false ? (
            <AccessDenied user={deniedUser || config?.actor} />
          ) : isSignedOut ? (
            <AccessDenied
              signedOut
              refusal={config?.singleTenantRefusal || ""}
              identityNotAccepted={config?.identityNotAccepted || ""}
            />
          ) : loaded && config === null && configError !== null ? (
            <ConfigUnavailable error={configError} />
          ) : !loaded || config === null ? (
            <LoadingScreen />
          ) : (
            <>
              <ErrorBoundary label="navigation rail" resetKey={paneKey}>
                {isPhone ? (
                  <PhoneNav
                    config={config}
                    running={tasks.some((t) => t.state === "running")}
                    onOpenNewTask={openNewTask}
                    alerts={alertsButton}
                    onOpenSettings={() => openSettings("")}
                    settingsOpen={view === "settings"}
                  >
                    {sidebar}
                  </PhoneNav>
                ) : (
                  sidebar
                )}
              </ErrorBoundary>
              {/* Its own boundary, so a page that cannot draw what it
                was handed takes itself down and not the rail beside it;
                going anywhere else changes paneKey and tries again
                (grain/task-1435). The header rail is outside that
                boundary, in a frame of its own, so a page that threw
                still has its title and the bell, System and Settings
                over the fallback (components/PageHeader.jsx). Not on a
                phone, where PhoneNav's bar is the rail -- the bell and
                the gear are on it -- and each page keeps its own header
                row in its body, as it always has. */}
              <div className="page-frame">
                {!isPhone && (
                  <PageHeader
                    title={pageTitle}
                    icon={pageIcon}
                    globals={globalButtons}
                    onSlot={setPageHeaderSlot}
                  />
                )}
                <div className="page-frame-body">
                  <PageHeaderProvider slot={isPhone ? null : pageHeaderSlot}>
                    <WhenRailReady ready={isPhone || pageHeaderSlot !== null}>
                      <ErrorBoundary label="page" resetKey={paneKey}>
                        {view === "repos" &&
                        openRepo !== null &&
                        releasesOpen ? (
                          /* Keyed by the repo, like the two pages below: a repo
                     change under a mounted page (back/forward between two
                     repo URLs) remounts it, so no draft, open form or late
                     answer from the previous repo survives into this one. */
                          <RepoReleases
                            key={openRepo}
                            repo={openRepo}
                            config={config}
                            templates={templates}
                            release={openRelease}
                            baseBranchGates={baseBranchGates}
                            onSelectRelease={selectRelease}
                            onBack={() => {
                              setReleasesOpen(false);
                              setOpenRelease(null);
                            }}
                            onOpenTab={(tab) => openRepoPage(openRepo, tab)}
                            onOpenTask={openTask}
                            showError={showError}
                          />
                        ) : view === "repos" &&
                          openRepo !== null &&
                          repoComparison !== null ? (
                          /* One pull request of that repo -- the change one of its
                   branches brings to another (grain/task-375). A step
                   inside the repo's page like the two above, and back
                   from here is the repo rather than the repo list. */
                          <PullRequestPage
                            key={openRepo}
                            repo={openRepo}
                            comparison={repoComparison}
                            onBack={() => setRepoComparison(null)}
                            showError={showError}
                          />
                        ) : view === "repos" && openRepo !== null ? (
                          <RepoPage
                            key={openRepo}
                            repo={openRepo}
                            repoTab={repoTab}
                            onShowTab={setRepoTab}
                            tasks={tasks}
                            config={config}
                            baseBranchGates={baseBranchGates}
                            onOpenComparison={setRepoComparison}
                            onBack={closeRepoPage}
                            onOpenTask={openTask}
                            onNewTask={openNewTaskForRepo}
                            onOpenReleases={() => {
                              setOpenRelease(null);
                              setReleasesOpen(true);
                            }}
                            onOpenCIRun={openCIRun}
                            onRefreshConfig={refreshConfig}
                            onRefreshGates={refreshBaseBranchGates}
                            showError={showError}
                          />
                        ) : view === "repos" ? (
                          <RepoList
                            tasks={tasks}
                            config={config}
                            onOpenRepo={openRepoPage}
                            onOpenSettings={openDeploymentSettings}
                            onRefreshConfig={refreshConfig}
                            showError={showError}
                          />
                        ) : view === "processes" ? (
                          <ProcessesList
                            schedules={schedules}
                            templates={templates}
                            suites={suites}
                            suiteRuns={suiteRuns}
                            triggers={processTriggers}
                            config={config}
                            tasks={tasks}
                            query={processQuery || {}}
                            onQueryChange={setProcessQuery}
                            resetSeq={processResetSeq}
                            openScheduleId={openScheduleId}
                            onOpenSchedule={setOpenScheduleId}
                            openTemplateId={openTemplateId}
                            onOpenTemplate={setOpenTemplateId}
                            openSuiteId={openSuiteId}
                            onOpenSuite={setOpenSuiteId}
                            openSuiteRunId={openSuiteRunId}
                            onOpenRun={setOpenSuiteRunId}
                            onOpenScheduleFromRun={openSchedule}
                            onOpenTrigger={openTriggerRepo}
                            onOpenTask={openTask}
                            onRefreshSchedules={refreshSchedules}
                            onRefreshTemplates={refreshTemplates}
                            onRefreshSuites={refreshSuites}
                            onRefreshRuns={refreshSuiteRuns}
                            onRefreshTasks={refreshList}
                            showError={showError}
                          />
                        ) : view === "ci" ? (
                          /* Grain's own CI listing: the sandboxes it is spending
                   now and every run it still remembers. A selected run
                   is the shared overlay rendered below. */
                          <CIPage
                            config={config}
                            onOpenRun={setOpenCIRunId}
                            onOpenTask={openTask}
                            initialRepo={ciRepo}
                            showError={showError}
                          />
                        ) : view === "inbox" ? (
                          /* Every task, like the board: which ones belong here is
                   the page's own question (state.js's waitsOnUser), and
                   the sidebar's state filter is a question about the
                   flat list. */
                          <InboxPage
                            tasks={tasks}
                            config={config}
                            onOpenTask={openTask}
                            onOpenConfigRepo={() => openSettings("config-repo")}
                            notifications={notifications}
                            onNotificationsChanged={reloadNotifications}
                            onAct={inboxAct}
                            onActBatch={actBatch}
                            showError={showError}
                          />
                        ) : view === "boards" && openBoardRow !== null ? (
                          /* One saved board: the page says what it is and offers
                   the two buttons that change it, and the view under it
                   is the ordinary list, board or timeline over every
                   task -- the board's own narrowing is already in force
                   (openBoard above), so there is nothing to filter here
                   a second time. */
                          <BoardPage
                            board={openBoardRow}
                            onBack={() => setViewAndCloseOpen("boards")}
                            onEditBoard={openEditBoardTask}
                            onNewBoard={openNewBoardTask}
                          >
                            {/* The pane under the header is one of the three
                      ordinary panes, headed as they always are: the
                      board's own name is said once, above, by the page
                      itself. A kind this build does not have falls
                      through to the list the way normalizeBoard's own
                      fallback does (boards.js) -- a board written for a
                      newer build should open as a list of the tasks it
                      asks about rather than as a blank page. */}
                            {openBoardRow.kind === "kanban"
                              ? taskBoardPane(tasks, {
                                  columns: boardColumns(openBoardRow),
                                  landmark: false,
                                })
                              : openBoardRow.kind === "gantt"
                                ? /* The timeline's range and lanes are the reader's
                       browser setting rather than the board's, the same
                       argument that lets a Kanban board fall back to
                       this browser's columns (boards.js). */
                                  taskGanttPane(tasks, { landmark: false })
                                : openBoardRow.kind === "panels"
                                  ? /* A dashboard: several of the panes above at
                         once, each narrowed to its own question
                         (PanelBoard.jsx, grain/task-88). It takes every
                         task for the reason the board does -- what a
                         panel is about is its own narrowing. */
                                    boardAwaitingSeed
                                    ? null
                                    : panelBoardPane(openBoardRow)
                                  : openBoardRow.kind === "report"
                                    ? /* The one kind that is not a view of these
                           tasks at all (ReportBoard.jsx,
                           grain/task-83): the measurement report, asked
                           of the daemon scoped to what the narrowing
                           says. It takes no tasks for that reason --
                           a percentile is not something this page can
                           compute from rows it holds. */
                                      boardAwaitingSeed
                                      ? null
                                      : reportBoardPane(openBoardRow)
                                    : openBoardRow.kind === "tree"
                                      ? taskTreePane(tasks, { landmark: false })
                                      : taskListPane(tasks, "all", {
                                          landmark: false,
                                        })}
                          </BoardPage>
                        ) : view === "session" && newSessionOpen ? (
                          /* A chat with no session behind it yet: the first
                   message creates one (NewSessionPage.jsx). */
                          <NewSessionPage
                            config={config}
                            onStart={startStandingSession}
                          />
                        ) : view === "sessions" ||
                          (view === "session" && openSession === null) ? (
                          <SessionsPage
                            sessions={sessions}
                            tasks={tasks}
                            // Only once the list has actually been read: a
                            // first GET /api/console-sessions that failed (a
                            // daemon still starting its session manager answers
                            // 503) still settles `loaded`, and the page then
                            // told the reader a session that exists "may have
                            // been deleted" until the next poll found it.
                            missingSessionId={
                              view === "session" && loaded && sessionsLoaded
                                ? openSessionId
                                : null
                            }
                            onOpenSession={openStandingSession}
                            onNewSession={openNewSession}
                            // The same handler the session's own pane
                            // reports its delete to, so a session got rid
                            // of from this list leaves the shell as one
                            // deleted from inside leaves it.
                            onDeleted={deleteStandingSession}
                            showError={showError}
                          />
                        ) : view === "session" ? (
                          /* A standing session's own pane: the chat, and what the
                   agent on the other end of it may reach
                   (grain/task-415). `inboxAct` rather than `act` for the
                   chat's own mutations -- a message sent here should
                   leave the reader in the conversation they are having,
                   not open the task detail over it. */
                          <ConsoleSessionPage
                            session={openSession}
                            task={
                              openSessionTaskId &&
                              String(sessionTask?.id) ===
                                String(openSessionTaskId)
                                ? sessionTask
                                : null
                            }
                            tasks={tasks}
                            config={config}
                            capabilities={sessionCapabilities}
                            networkAccessOptions={
                              sessionNetworkAccesses.length
                                ? sessionNetworkAccesses
                                : undefined
                            }
                            act={sessionAct}
                            onOpenTask={openTask}
                            draftRequest={
                              sessionPrompt?.sessionId === openSession?.id
                                ? sessionPrompt
                                : null
                            }
                            onDraftConsumed={consumeSessionPrompt}
                            onSaved={refreshSessionSaved}
                            onDeleted={deleteStandingSession}
                            showError={showError}
                          />
                        ) : view === "settings" ? (
                          <ErrorBoundary label="settings">
                            <SettingsPage
                              config={config}
                              section={settingsSection}
                              anchor={settingsAnchor}
                              item={settingsItem}
                              onItem={setSettingsItem}
                              onSection={(next) => {
                                setSettingsAnchor("");
                                setSettingsItem("");
                                setSettingsSection(next);
                              }}
                              onSaved={refreshConfig}
                              onLogout={handleLogout}
                              onOpenSystem={openSystem}
                              onOpenRepo={openRepoPage}
                              showError={showError}
                              dismissError={dismissError}
                              tasks={tasks}
                              sessions={sessions}
                              boards={boards}
                              onShowStrandedValues={showStrandedValues}
                            />
                          </ErrorBoundary>
                        ) : view === "system" ? (
                          <ErrorBoundary label="system">
                            <SystemPage
                              config={config}
                              tab={systemTab}
                              onTab={(next) => setSystemTab(next)}
                              showError={showError}
                              onPipelineChanged={refreshConfig}
                              onConfigChanged={() =>
                                Promise.all([
                                  refreshConfig(),
                                  reloadNotifications(),
                                ])
                              }
                              onOpenSettings={openSettings}
                              onOpenSetup={({ tab, view: target }) =>
                                target
                                  ? setViewAndCloseOpen(target)
                                  : openSettings(tab)
                              }
                              alerts={alerts}
                              tasks={tasks}
                              metricsNarrowing={
                                metricsNarrowing || NO_NARROWING
                              }
                              onNarrowMetrics={narrowMetrics}
                              metricsWindow={metricsWindow}
                              onPickMetricsWindow={setMetricsWindow}
                              onOpenTask={openTaskFromMetrics}
                              onOpenView={openPanelView}
                              onAct={act}
                              onOpenSession={openStandingSession}
                            />
                          </ErrorBoundary>
                        ) : view === "boards" ? (
                          <BoardsPage
                            boards={boards}
                            onOpenBoard={(id) => {
                              const board = boards.find((b) => b.id === id);
                              if (board) openBoard(board);
                            }}
                            onNewBoard={openNewBoardTask}
                            onMoveBoard={moveBoard}
                          />
                        ) : (
                          <div className="main-column">
                            {taskListPane(tasks, stateFilter)}
                          </div>
                        )}
                      </ErrorBoundary>
                    </WhenRailReady>
                  </PageHeaderProvider>
                </div>
              </div>
            </>
          )}
        </div>
        {error !== null && <ErrorBanner message={error} />}
        {filedWithoutDefaults !== null && (
          <DroppedDefaultsNotice
            created={filedWithoutDefaults}
            onClose={() => setFiledWithoutDefaults(null)}
          />
        )}
        <ConfigRefusalDialog
          refusal={configRefusal}
          onClose={() => setConfigRefusal(null)}
        />
        {showLoadingPane && (
          <TaskLoadingOverlay
            taskId={pendingTaskId}
            onClose={closeDetail}
            animate={!taskPaneOnScreen.current}
          />
        )}
        {showTaskErrorPane && (
          <TaskLoadingOverlay
            taskId={openTaskId}
            error={taskLoadError.message}
            onRetry={() => loadTask(openTaskId, true)}
            onClose={closeDetail}
            animate={!taskPaneOnScreen.current}
          />
        )}
        {showDetailPane && (
          <ErrorBoundary
            variant="dialog"
            label="task pane"
            resetKey={openTaskId}
            onClose={closeDetail}
          >
            <DetailOverlay
              // Navigating through a dependency replaces the task without
              // first closing this pane. Remount its task-local editors so
              // an unsaved field edit from the old task cannot be submitted
              // to the new task's id.
              key={openTaskId}
              animate={!taskPaneOnScreen.current}
              task={detail}
              tasks={tasks}
              config={config}
              templates={templates}
              onClose={closeDetail}
              onOpenTask={openTask}
              onNewTaskFrom={openNewTaskWithFields}
              onOpenCIRun={openCIRun}
              onOpenSuiteRun={openSuiteRun}
              act={act}
              showError={showError}
            />
          </ErrorBoundary>
        )}
        {openCIRunId !== null && (
          <ErrorBoundary
            variant="dialog"
            label="CI run pane"
            resetKey={openCIRunId}
            onClose={() => setOpenCIRunId(null)}
          >
            <CIRunOverlay
              runId={openCIRunId}
              onClose={() => setOpenCIRunId(null)}
              onOpenRun={setOpenCIRunId}
              showError={showError}
            />
          </ErrorBoundary>
        )}
        {/* A cold /new link renders before the first parallel load
          returns. The form seeds several pieces of state from config and
          the task/template lists -- including the custom field values
          carried in the link -- so mounting it early would turn those
          values into blanks that later renders cannot restore. */}
        {showNewTask && loaded && config !== null && (
          <ErrorBoundary
            variant="dialog"
            label="new task form"
            onClose={() => setShowNewTask(false)}
          >
            <NewTaskOverlay
              tasks={tasks}
              config={config}
              templates={templates}
              defaultRepo={newTaskRepo !== null ? newTaskRepo : openRepo}
              prefill={newTaskPrefill}
              fields={newTaskFields}
              boardId={newTaskBoard || ""}
              onClose={() => {
                setShowNewTask(false);
                setNewTaskBoard(null);
                setNewTaskFields(null);
                setNewTaskPrefill(null);
              }}
              onCreated={refreshAfterCreate}
              onOpenSettings={(tab) => {
                setShowNewTask(false);
                openDeploymentSettings(tab);
              }}
              showError={showError}
            />
          </ErrorBoundary>
        )}
        {showCustomizeSidebar && (
          <CustomizeSidebarOverlay
            open={showCustomizeSidebar}
            onClose={() => setShowCustomizeSidebar(false)}
            sessions={sessions}
            boards={boards}
          />
        )}
      </div>
    </TimeZoneProvider>
  );
}

// DEPLOYMENT_SETTINGS_TAB maps a Team settings tab an older link or
// caller names onto the deployment Settings tab that took it over.
const DEPLOYMENT_SETTINGS_TAB = {
  connections: "models",
};

export default function App({
  onReload = () => window.location.reload(),
} = {}) {
  return (
    <PreferencesProvider>
      <PinnedNavProvider>
        <AppContent onReload={onReload} />
      </PinnedNavProvider>
    </PreferencesProvider>
  );
}
