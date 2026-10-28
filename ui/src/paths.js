// paths.js maps App.jsx's own navigation state -- which of the four
// sidebar destinations is showing, whether a task's or a repo's own page
// is open, whether the settings or system overlay is open -- onto real
// URL paths and back, so each is directly loadable/bookmarkable/
// shareable instead of reachable only by clicking through from "/"
// (bwsalmon/agents#548).
//
// Kept as two pure functions rather than pulling in a routing library:
// with only these few (mostly orthogonal) bits of state to encode,
// App's own effects -- mount, a state change, a popstate event -- can
// call straight into them without a router component owning the page
// tree the way the rest of App already does by hand.
//
// Logs and Sandbox health used to be sidebar destinations of their own
// (bwsalmon/agents#457, bwsalmon/agents#536), then moved into Settings'
// own Debug tab (bwsalmon/agents#623) and so lost their own paths, the
// same way Upgrade lives there without ever having had a path of its
// own (and Secrets did too, until grain/task-110 gave each secret to
// whatever uses it). System (Logs, Sandbox health, Top and the reboot
// control together) is a sidebar destination again now (bwsalmon/
// agents#640), with /system as its own path -- a stale bookmark to the
// old /logs, /sandboxes or /debug still just falls back to the tasks
// view like any other unrecognized path. Metrics left that pane for a sidebar
// entry of its own (grain/task-173) and took /metrics with it, so the
// report an operator reads weekly is a link they can keep.

// How a task view is narrowed -- the search box, the sort order and the
// attribute filters its toolbar offers (taskFilters.js) -- rides in the
// query string of the paths that show one (grain/task-317). Those
// refinements were local component state until a list could be narrowed
// by half a dozen menus at once; at that point "the failed gcp-key
// tasks on acme/widgets" stops being a gesture somebody repeats and
// becomes a view worth bookmarking, linking to a colleague, and getting
// back to with the browser's own Back button -- which is exactly what
// every other piece of navigable state here already gets.
//
// It goes in the query rather than in the path because it is not a
// different page: /?repo=acme/widgets is the same task list as /, asked
// a narrower question, and the path stays what the view *is*.
import {
  NO_NARROWING,
  conditionFromQuery,
  isLegacyInteractiveClause,
} from "./taskFilters.js";
import { REPORT_WINDOWS } from "./report.js";
import {
  oldAdminDestination,
  settingsSectionFor,
  systemDestination,
} from "./settingsSections.js";
import {
  kindNarrowing,
  processNarrowingFromSearch,
  processNarrowingQuery,
} from "./processFilters.js";

// "inbox" is the same tasks again, narrowed to the ones waiting on the
// reader and shown with the answers they are waiting for (InboxPage.jsx,
// grain/task-20). It carries no narrowing of its own -- see
// showsTaskView below.
//
// "board" and "gantt" were two more: the same tasks as a Kanban board
// (TaskBoard.jsx, grain/task-287) and as bars against a clock
// (GanttChart.jsx, grain/task-55), each a fixed destination on the rail
// with a path of its own. Both are gone (grain/task-162) -- a Kanban
// board and a timeline are kinds a *saved* board can be, so the one
// everybody shared is now however many boards somebody wants, each
// under /boards/<id> and each carrying its own question. A kept link to
// /board or /gantt lands on the task list, the same fallback any other
// unrecognized path gets.
//
// "boards" is the saved dashboards (grain/task-48): /boards is the list
// of them and /boards/board-3 is one of them, which is the same
// list-then-item shape /schedules and /templates already have. A board
// carries a narrowing of its own, so its path takes a query the way "/"
// itself does -- /boards/board-3 opens the board's own question and
// /boards/board-3?q=flake opens a link somebody narrowed further before
// they sent it.
//
// "ci" is grain's own CI (grain/task-354): /ci is every run this
// deployment still remembers and /ci/9f2c1a3b8e4d5f60 is one of them,
// list-then-item again. A run is forgotten in the end -- the daemon
// keeps a few hundred and a restart of an in-memory store keeps none --
// so this is the one link here that can go stale on its own, and the
// pane answers a run it cannot find with the daemon's own account of
// why rather than a blank. It is still worth being a link: "come and
// look at why this is red" is sent to somebody while the run is very
// much still there, and it is the path a task's own CI verdict opens.
//
// Memory, Sandboxes and Caches were views of their own (/memory,
// /sandboxes, /caches); they are a Settings section and two System tabs
// now (docs/design/settings-panel.md §3.5), and parseSegments redirects
// their old paths there.
const VIEWS = [
  "inbox",
  "tasks",
  "boards",
  "sessions",
  "repos",
  "processes",
  "ci",
];

// REPO_TAB_IDS are RepoPage's tabs (REPO_TABS there), in its order: the
// <tab> of /repos/<o>/<n>/settings/<tab>. Overview is the page's own
// path, so buildPath never writes /settings/overview, but parsePath
// reads it.
export const REPO_TAB_IDS = [
  "overview",
  "ci",
  "merge-queue",
  "agents",
  "branches",
  "releases",
  "advanced",
];

export function isRepoTab(id) {
  return REPO_TAB_IDS.includes(id);
}

// parsePath turns a URL path into the {view, taskId, repo, repoTab,
// showReleases, release, scheduleId, templateId, suiteId, suiteRunId, ciRunId,
// settingsSection, systemTab, metricsNarrowing, metricsWindow}
// App needs in order to restore on load or on a back/forward
// navigation. Anything it
// doesn't recognize -- an unknown segment, a stray trailing slash --
// falls back to the default tasks view rather than erroring, the same as
// an unknown in-app route today just lands on "/".
//
// A repo's own page (RepoPage, grain/task-111) takes two segments
// rather than one, since a repo is named "owner/name" -- /repos/acme/
// widgets -- and its releases pane hangs off that as a third.
// An "owner" with no "name" is
// not a repo, so /repos/acme lands on the repo list the same way any
// other unrecognized path lands on "/".
//
// A schedule, a template and a suite each take one segment under their
// own list -- /schedules/sched-1 -- the way /tasks/42 already does
// (grain/task-139). Opening one of the four fills the same pane beside
// the sidebar (grain/task-94), so all four are the same kind of link:
// loadable cold, survives a reload, and back closes the pane rather
// than the page. One run of a suite is the same kind of link again, two
// segments down from the suites list -- /suites/runs/7, the pane that
// lists every task that run has filed (SuiteRunPane.jsx).
//
// `search` is window.location.search, and is where a narrowing comes
// from: parsePath reports one -- as `narrowing`, absent when the query
// asks for none -- only on the paths that actually show a task view,
// the same paths buildPath writes one onto. A query on any other path
// (a hand-edited /schedules?repo=acme/widgets, or the /settings the
// address bar showed while an overlay covered a narrowed list) is
// ignored rather than half-restored, which is the treatment /settings
// already gives the view it was opened over.
//
// /metrics is the one exception, and it is a separate field rather than
// an exception to that rule (grain/task-379). The Metrics pane shows a
// report rather than a task view, and it takes a scope now -- the repo,
// the author and the span report.js reads out of a narrowing -- so its
// query is parsed as `metricsNarrowing` and never as `narrowing`.
// Keeping the two apart is the whole point: showsTaskView still says no
// for /metrics, so opening the pane over a narrowed list does not hand
// the deployment-wide report that list's question, and a link to a
// scoped report does not narrow the list waiting behind the pane.
//
// The pane's window picker rides beside that scope as `window`
// (grain/task-1584) and is parsed as `metricsWindow`: "/metrics?
// window=30d" reopens the report over thirty days rather than the seven
// a reload used to put it back on. Only the picker's own options are
// read; anything else -- a typo, a span a newer build offers -- is
// dropped and the pane opens on its default, the same way an attribute
// this build does not have is dropped from a narrowing.
//
// A report board's picker rides the same way (grain/task-2393), as
// `boardWindow` beside the board's narrowing: "/boards/board-3?
// window=30d" is that board's report over thirty days. It is read on
// any open board, since the path does not say which kind the board is;
// a dashboard of panels has no picker to write one, and ignores one a
// hand-edited link carries.
//
// A narrowing naming something that has since gone is parsed as written
// and left to the view to resolve, and the two cases are resolved
// differently on purpose (taskFilters.js). A *value* that no task
// carries any more -- a repo that is no longer a target, a capability
// that was removed -- is a condition that matches nothing, and the list
// says so with the condition on screen and one Clear away: the link
// asked a real question and the honest answer to it is "nothing". An
// *attribute* this build does not have -- a custom one the deployment
// dropped, a link from a newer build -- is not a question this build can
// ask at all, so the clause is dropped and the rest of the link opens.
export function parsePath(pathname, search = "") {
  const parsed = parseSegments(pathname);
  const params = new URLSearchParams(search || "");
  // A pull request's own page is a path now, so the refs usually arrive
  // in it; the query below is the spelling links written before
  // grain/task-375 use, and is read only when the path names none.
  if (parsed.repoComparison) return parsed;
  const repoComparison = repoComparisonFromQuery(parsed, search);
  if (repoComparison) return { ...parsed, repoComparison };
  if (parsed.showNewTask) {
    const newTaskFields = parseNewTaskFields(search);
    return newTaskFields ? { ...parsed, newTaskFields } : parsed;
  }
  if (parsed.view === "system" && parsed.systemTab === "metrics") {
    const metricsNarrowing = parseNarrowing(search);
    const metricsWindow = metricsWindowFromQuery(params);
    return {
      ...parsed,
      ...(metricsNarrowing ? { metricsNarrowing } : {}),
      ...(metricsWindow ? { metricsWindow } : {}),
    };
  }
  // The Processes list's own query, read only where the list itself is
  // what is on screen. A redirect from /schedules brings its own kind,
  // which a query the old path carried does not override.
  if (
    parsed.view === "processes" &&
    !parsed.scheduleId &&
    !parsed.templateId &&
    !parsed.suiteId &&
    !parsed.suiteRunId
  ) {
    const processQuery = parsed.processQuery || processQueryFromSearch(params);
    return processQuery ? { ...parsed, processQuery } : parsed;
  }
  const narrowing = showsTaskView(parsed) ? parseNarrowing(search) : null;
  const stateFilter = showsStateFilter(parsed)
    ? stateFilterFromQuery(params)
    : null;
  const boardWindow =
    parsed.view === "boards" && parsed.boardId
      ? metricsWindowFromQuery(params)
      : null;
  return {
    ...parsed,
    ...(narrowing ? { narrowing } : {}),
    ...(stateFilter ? { stateFilter } : {}),
    ...(boardWindow ? { boardWindow } : {}),
  };
}

// metricsWindowFromQuery reads the Metrics pane's `window` -- or a
// report board's, which is the same picker -- or null for one that is
// absent or is not one of the picker's options.
function metricsWindowFromQuery(params) {
  const value = params.get("window");
  return REPORT_WINDOWS.some((w) => w.value === value) ? value : null;
}

// metricsQuery is the Metrics pane's whole query: its scope, then its
// window. A report board's is built the same way, from the board's
// narrowing and its own pick. A window nobody picked is null and left off, so the pane
// nobody has touched still lives at the bare "/metrics" -- and picking
// the window the report was already on puts it back to null
// (ReportBoard.jsx), which is how choosing "Last 7 days" again returns
// the address to the bare path. It is null rather than "7d" because the
// unpicked window is not always seven days: a scope can name a span of
// its own, and a link that picked 7d over it has to say so.
//
// The Metrics pane's scope never writes the empty marker (see
// narrowingQuery): the pane reads no scope as NO_NARROWING already, so
// a scope somebody cleared and one nobody set are the same report, and
// the bare "/system/metrics" is the address of both.
function metricsQuery(metricsNarrowing, metricsWindow, marksEmpty = false) {
  const query = narrowingQuery(metricsNarrowing, marksEmpty);
  if (!metricsWindow) return query;
  const param = `window=${encodeURIComponent(metricsWindow)}`;
  return query ? `${query}&${param}` : `?${param}`;
}

// The rail's own pick -- Queued, Closed, Blocked -- rides in the query
// as `state` beside the narrowing (grain/task-1355). It used to be held
// by App alone, and the rail takes the State condition *out* of the
// narrowing when it picks one (App's selectStateFilter), so the URL a
// state view left behind was the bare list with nothing set aside: a
// reload of "Closed" opened All tasks with the closed and deferred tasks
// mixed back in, and Back could not step out of a state at all.
//
// Only on the flat list: a board and a repo's own page are task views
// too, but the rail's state is not a question either of them asks.
// "all" is the absence of one, so it is never written and never read
// back as anything but absent. A value this build does not know opens
// as written, for parsePath's reason -- the list says it has nothing.
export function showsStateFilter(parsed) {
  return showsTaskView(parsed) && parsed.view === "tasks";
}

function stateFilterFromQuery(params) {
  const raw = (params.get("state") || "").trim();
  return /^[a-z_]+$/.test(raw) && raw !== "all" ? raw : null;
}

// A pull request is a comparison of two of a repository's refs, and it
// has a page of its own under that repository: /repos/acme/widgets/
// compare/main...grain/task-12 (grain/task-375), which is how github.com
// spells the same thing. A head is enough -- /repos/acme/widgets/compare/
// grain/task-12 -- and an omitted base means the repository's own default
// branch, resolved by the server rather than guessed at here.
//
// Written as the rest of the path rather than as two segments because a
// branch name carries "/" of its own (grain/task-12 is the shape grain
// gives them). "..." cannot appear in one -- git refuses consecutive dots
// in a ref name -- so it is a separator nothing can be mistaken for.
// A ref is decoded on the way in and written plainly on the way out: a
// branch name's "/" is a literal slash here, the way github.com's own
// compare URLs spell it, but a link written by something that escaped it
// (%2F -- which is what the query form this replaces always produced)
// names the same branch and must open the same page.
function comparisonFromRefs(refs) {
  const decode = (ref) => {
    try {
      return decodeURIComponent(ref).trim();
    } catch {
      return ref.trim();
    }
  };
  const at = refs.indexOf("...");
  if (at < 0) return refs ? { base: "", head: decode(refs) } : null;
  const base = decode(refs.slice(0, at));
  const head = decode(refs.slice(at + 3));
  return head ? { base, head } : null;
}

function comparisonRefs(comparison) {
  return comparison.base
    ? `${comparison.base}...${comparison.head}`
    : comparison.head;
}

// Before that page existed the same comparison was the repo page itself
// with the refs in its query (/repos/acme/widgets?base=main&head=grain/
// task-12), and a link somebody kept still opens it. Read before task
// narrowing because `base` was also the legacy spelling of a task-list
// filter; App's canonical URL effect then rewrites it to the path above.
function repoComparisonFromQuery(parsed, search) {
  if (parsed.view !== "repos" || !parsed.repo) return null;
  if (parsed.showReleases) return null;
  const params = new URLSearchParams(search || "");
  const base = params.get("base")?.trim() || "";
  const head = params.get("head")?.trim();
  return head ? { base, head } : null;
}

// FIELD_PARAM_PREFIX is how a custom field value rides in a query
// string: ?field.priority=high (grain/task-53). Prefixed rather than
// bare so a field can be called "sort" or "repo" without colliding with
// a narrowing parameter of the same name, and readable rather than
// packed into one JSON parameter, because this is a link somebody reads
// before they send it -- the same reason narrowingQuery leaves a "/"
// alone below.
const FIELD_PARAM_PREFIX = "field.";

// parseNewTaskFields reads the custom field values a /new link carries
// into the {name: value} map NewTaskOverlay seeds its inputs from, or
// null when it carries none.
//
// Every value is taken on trust, the way a narrowing's own filters are:
// whether "priority" is a field this deployment declares, and whether
// "urgent" is one of its options, are questions about the config, which
// paths.js has never seen. The form is what resolves that -- it renders
// the fields it has and ignores a value for one it does not, so a link
// written against a field that has since been deleted opens the form
// rather than an error.
// A field named more than once carries a set: ?field.areas=ui&field.
// areas=daemon is what a multi-valued field looks like here
// (model.CustomField.Multiple), and repeating the parameter is how a
// query string has always said several -- readable, and with no
// separator a value could contain. One value stays one value rather
// than an array of one, so a link written before any of this, and a
// link to a single-valued field, is unchanged.
function parseNewTaskFields(search) {
  const params = new URLSearchParams(search || "");
  const fields = {};
  for (const [key, value] of params.entries()) {
    if (!key.startsWith(FIELD_PARAM_PREFIX)) continue;
    const name = key.slice(FIELD_PARAM_PREFIX.length);
    if (!name || !value) continue;
    if (fields[name] === undefined) {
      fields[name] = value;
    } else if (Array.isArray(fields[name])) {
      fields[name].push(value);
    } else {
      fields[name] = [fields[name], value];
    }
  }
  return Object.keys(fields).length > 0 ? fields : null;
}

// newTaskPath is the link a task's own pane offers: the new-task form,
// opened carrying these custom field values (grain/task-53). Filing the
// next task about the same customer, at the same priority, is then one
// click from the last one rather than a form somebody fills in again
// from memory.
//
// A real URL rather than a click handler, because everything else this
// file encodes is one: it can be copied, bookmarked, pinned in a chat
// ("file the follow-ups here"), and it survives a reload.
export function newTaskPath(fields) {
  return buildPath({ view: "tasks", showNewTask: true, newTaskFields: fields });
}

// newTaskFieldsQuery is parseNewTaskFields' inverse, with the names in
// sorted order so the same set of values always produces the same URL --
// and a field holding several appended once per value, in the order it
// holds them, for the same reason.
function newTaskFieldsQuery(fields) {
  const params = new URLSearchParams();
  for (const name of Object.keys(fields || {}).sort()) {
    const value = fields[name];
    for (const one of Array.isArray(value) ? value : [value]) {
      if (one) params.append(FIELD_PARAM_PREFIX + name, one);
    }
  }
  const query = params.toString().replace(/%2F/g, "/");
  return query ? `?${query}` : "";
}

// itemId reads one item's id back out of its path segment. The rows
// that link to an item write that segment with encodeURIComponent
// (ListPrimitives' RowLink hrefs), so a repository-declared row's id --
// repo:template:acme:widgets:nightly (docs/declarations.md) -- arrives
// as repo%3Atemplate%3A... whenever the link is opened as a link: in a
// new tab, copied, or bookmarked. Undecoded, that named no item and the
// page fell back to the list with "no template" (grain/task-1533). The
// plain spelling pathFor writes decodes to itself, and a segment that is
// not valid percent-encoding is kept as it came.
function itemId(segment) {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

// processDestination is what /processes/<rest> opens: the list, or one
// item of it with its overlay on top (§5.4). A trigger is the one kind
// with no overlay of its own: it opens the file's repository on its
// Advanced tab, where the declarations it came from are listed.
function processDestination(rest) {
  const [kind, id, more] = rest;
  if (kind === "schedules" && id) {
    return { view: "processes", scheduleId: itemId(id) };
  }
  if (kind === "templates" && id) {
    return { view: "processes", templateId: itemId(id) };
  }
  if (kind === "suites" && id === "runs") {
    return more
      ? { view: "processes", suiteRunId: itemId(more) }
      : { view: "processes", processQuery: kindNarrowing(["suite"]) };
  }
  if (kind === "suites" && id) {
    return { view: "processes", suiteId: itemId(id) };
  }
  if (kind === "triggers" && id && more) {
    const first = itemId(id);
    const hosted = /[.:]/.test(first) && rest[3];
    const repo = hosted
      ? `${first}/${itemId(more)}/${itemId(rest[3])}`
      : `${first}/${itemId(more)}`;
    return { view: "repos", repo, repoTab: "advanced" };
  }
  return { view: "processes" };
}

// processPath is where one process opens (§5.4), for the links that
// build one outside the list itself: a task's "filed by schedule"
// hourglass, a suite run's Scheduled chip, a task's suite-run link.
// `kind` is a row kind (processes.js) or a path segment; a suite run is
// "run". Ids are written with encodeURIComponent, which itemId reads
// back.
export function processPath(kind, id) {
  const enc = encodeURIComponent(id);
  switch (kind) {
    case "schedule":
    case "schedules":
    case "workflow":
      return `/processes/schedules/${enc}`;
    case "template":
    case "templates":
      return `/processes/templates/${enc}`;
    case "suite":
    case "suites":
      return `/processes/suites/${enc}`;
    case "run":
    case "suiteRun":
      return `/processes/suites/runs/${enc}`;
    case "trigger": {
      // A trigger's id is "<owner>/<name>/<path>" (processes.js).
      return `/processes/triggers/${String(id)
        .split("/")
        .map(encodeURIComponent)
        .join("/")}`;
    }
    default:
      return "/processes";
  }
}

// processQueryFromSearch reads the list's own query (§5.4): its
// narrowing -- search, sort and conditions, the kind chips among them --
// so a filtered view is a link (processFilters.js).
function processQueryFromSearch(params) {
  return processNarrowingFromSearch(params);
}

function processQueryString(processQuery) {
  return processQuery ? processNarrowingQuery(processQuery) : "";
}

function parseSegments(pathname) {
  const segments = pathname.split("/").filter(Boolean);
  // Before the single-tenant reset every page lived under /t/<team>/,
  // and links people saved from then -- above all a pane's "File a task
  // with these fields", /t/<team>/new?field.… -- still name one. The
  // team means nothing now, so the rest of the path is read as it would
  // be on its own and App's replaceState pass writes it without the team.
  if (segments[0] === "t" && segments.length > 1) {
    return parseSegments(`/${segments.slice(2).join("/")}`);
  }
  // Settings and System are views (docs/design/settings-panel.md §3.5).
  // Every path that used to open one of their old overlays, or one of
  // the pages that moved into them, parses to where that is now; App's
  // replaceState pass then writes the new path, so Back never lands on
  // the old one.
  if (segments[0] === "settings") {
    const settingsSection = settingsSectionFor(segments[1]);
    // One item of a section -- a capability server, by its
    // capability_id -- is the segment after it
    // (/settings/capabilities/<id>), so a server's page is a link.
    const settingsItem =
      settingsSection === "capabilities" && segments.length > 2
        ? itemId(segments.slice(2).join("/"))
        : "";
    return settingsItem
      ? { view: "settings", settingsSection, settingsItem }
      : { view: "settings", settingsSection };
  }
  if (segments[0] === "admin") return oldAdminDestination(segments[1]);
  if (segments[0] === "system") return systemDestination(segments[1]);
  if (segments[0] === "memory") {
    return { view: "settings", settingsSection: "memory" };
  }
  if (segments[0] === "metrics") {
    return { view: "system", systemTab: "metrics" };
  }
  if (segments[0] === "sandboxes") {
    return { view: "system", systemTab: "sandboxes" };
  }
  if (
    segments[0] === "caches" ||
    segments[0] === "cache" ||
    segments[0] === "actions-cache"
  ) {
    return { view: "system", systemTab: "caches" };
  }
  // The new-task form, which is an overlay over the task list like
  // Settings above and so parses to the same "tasks, with something on
  // top of it" shape. It has a path of its own so that filing a task
  // carrying particular custom field values is a link somebody can send
  // (newTaskPath, and DetailOverlay's own "File a task with these
  // fields").
  if (segments[0] === "new") {
    return { view: "tasks", showNewTask: true };
  }
  if (segments[0] === "tasks" && segments[1]) {
    return { view: "tasks", taskId: segments[1] };
  }
  if (segments[0] === "tasks") {
    return { view: "tasks" };
  }
  // A standing chat is a first-class destination in the rail. Keep the
  // unqualified path for old links and single-session deployments, and name
  // the session when the click identified one so deployments with several
  // can reload and share the exact conversation that was open.
  if (segments[0] === "director" && !segments[1]) {
    return { view: "session", sessionId: "director" };
  }
  if (segments[0] === "sessions" || segments[0] === "session") {
    if (segments[1]) {
      return { view: "session", sessionId: segments[1] };
    }
    return segments[0] === "sessions"
      ? { view: "sessions" }
      : { view: "session" };
  }
  if (
    (segments[0] === "repos" || segments[0] === "repo") &&
    segments[1] &&
    segments[2]
  ) {
    // A repo off the default host is host/owner/name
    // (/repos/git.example.com/team/widgets). No GitHub owner holds a
    // "." or a ":", so a first segment that does is a host -- the rule
    // RepoList's repoHostOf reads a typed repo by.
    const hosted = /[.:]/.test(segments[1]) && segments[3];
    const repo = hosted
      ? `${segments[1]}/${segments[2]}/${segments[3]}`
      : `${segments[1]}/${segments[2]}`;
    const rest = segments.slice(hosted ? 4 : 3);
    // The releases pane, and which release it is showing when the path
    // names one (/repos/acme/widgets/releases/r2) -- so a reload or a
    // link opens that release rather than whichever is newest.
    if (rest[0] === "releases")
      return rest[1]
        ? {
            view: "repos",
            repo,
            showReleases: true,
            release: itemId(rest[1]),
          }
        : { view: "repos", repo, showReleases: true };
    // One tab of the repo page (docs/design/settings-panel.md §2.3), so
    // Settings → Repositories can link to where a setting lives:
    // /repos/acme/widgets/settings/ci. A tab this build does not have
    // opens the page on Overview, as a bare /repos/<o>/<n> does.
    if (rest[0] === "settings")
      return isRepoTab(rest[1])
        ? { view: "repos", repo, repoTab: rest[1] }
        : { view: "repos", repo };
    // One pull request of that repo (PullRequestPage.jsx,
    // grain/task-375), which is a comparison of two of its refs and
    // takes the whole of the rest of the path -- see comparisonFromRefs.
    if (rest[0] === "compare") {
      const comparison = comparisonFromRefs(rest.slice(1).join("/"));
      return comparison
        ? { view: "repos", repo, repoComparison: comparison }
        : { view: "repos", repo };
    }
    return { view: "repos", repo };
  }
  if (segments[0] === "repo") {
    return { view: "repos" };
  }
  if (segments[0] === "boards" && segments[1]) {
    return { view: "boards", boardId: itemId(segments[1]) };
  }
  // Processes (docs/design/settings-panel.md §5.4): schedules,
  // templates and suites are one page, and each item opens under it at
  // /processes/<kind>/<id>, <kind> plural so the old top-level paths
  // below redirect by a prefix swap. A suite's runs are under the suites
  // segment, ahead of the suite branch: a suite's own id is always
  // "suite-N" (model.newSuiteID), so "runs" can never be one.
  if (segments[0] === "processes") {
    return processDestination(segments.slice(1));
  }
  // The pages Processes replaced. /schedules is the list narrowed to
  // schedules; /schedules/<id> is that schedule open over it. App's
  // replaceState pass writes the new path, so Back never lands here.
  if (segments[0] === "schedules" || segments[0] === "templates") {
    if (segments[1]) return processDestination(segments);
    return {
      view: "processes",
      processQuery: kindNarrowing([
        segments[0] === "schedules" ? "schedule" : "template",
      ]),
    };
  }
  if (segments[0] === "suites") {
    if (segments[1] && (segments[1] !== "runs" || segments[2])) {
      return processDestination(segments);
    }
    return { view: "processes", processQuery: kindNarrowing(["suite"]) };
  }
  if (segments[0] === "ci" && segments[1]) {
    return { view: "ci", ciRunId: segments[1] };
  }
  if (VIEWS.includes(segments[0])) {
    return { view: segments[0] };
  }
  return { view: "tasks" };
}

// showsTaskView is whether a parsed path has a task view on screen --
// the flat list, a saved board, or the list on a repo's own page -- and so
// whether a narrowing means anything on it. An overlay's path
// (/settings, /system, /metrics), an open task and the releases pane all
// say no: each of those covers the list rather than narrowing it, and
// none of them survives in the URL as "the thing behind the overlay"
// today either.
//
// /metrics says no even though its own path carries a query now: what
// that query narrows is the report the pane draws, which is a scope of
// the pane's own (parsePath's `metricsNarrowing`) and not the question
// the list behind it is asking.
//
// The inbox says no as well, and it is the one that does show tasks. It
// is not a view over the backlog somebody refines -- it is one fixed
// question ("what is waiting on me?") whose answer is grouped by what
// each task wants, and a repo menu narrowing it would leave the count in
// the nav rail disagreeing with the page under it. /inbox is one link,
// the same for everybody.
function showsTaskView(parsed) {
  if (parsed.view === "settings" || parsed.view === "system") return false;
  // The new-task form covers the list the same way those three do, and
  // its own query says what the form is prefilled with rather than how
  // the list behind it is narrowed.
  if (parsed.showNewTask) return false;
  if (parsed.taskId) return false;
  if (parsed.view === "repos")
    return (
      !!parsed.repo &&
      !parsed.showReleases &&
      // A pull request's own page covers the repo page the way those two
      // do, and carries its refs rather than a narrowing.
      !parsed.repoComparison
    );
  // An open board shows one, so a query on it says what that board is
  // being asked right now, which starts as the board's own and is
  // whatever the toolbar has made of it since. The boards *list* shows
  // no tasks and takes none.
  if (parsed.view === "boards") return !!parsed.boardId;
  return parsed.view === "tasks";
}

// # How a narrowing is written down
//
// One condition per `f` parameter, "attribute:comparison:values", with
// several values joined by "|":
//
//   /?q=flake&sort=created:desc&f=repo:any:acme/widgets|acme/gadgets
//
// and the sort as its own comma-separated list of "attribute:direction"
// keys, in the order they break each other's ties.
//
// Written to be read. A link like the one above is pasted into a message
// to somebody, so the separators are characters a query string is
// allowed to carry literally (":", "|", "/", ",") and they are put back
// after URLSearchParams has escaped them. Only a value that would be
// mistaken for a separator is escaped -- see escapeValue -- which in
// practice is none of them: no repo, capability id or author contains
// one.
//
// Nothing here checks that an attribute or a comparison exists. What
// attributes there are depends on the deployment's config, which paths.js
// has never seen, and a link naming one this build does not have should
// open the list it names rather than an error: resolveConditions drops
// the clause and sortTasks ignores the key (taskFilters.js). What is
// checked is the *shape*, since a malformed clause is not a question at
// all.
const SEPARATORS = /[%:|,]/g;

const escapeValue = (v) =>
  String(v).replace(
    SEPARATORS,
    (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`,
  );

// LEGACY_SORTS are the orders the one-Select toolbar wrote before there
// were sort keys (grain/task-317, replaced in grain/task-42). A link
// somebody kept still opens the order it names.
const LEGACY_SORTS = {
  manual: [],
  newest: [{ attr: "created", dir: "desc" }],
  oldest: [{ attr: "created", dir: "asc" }],
  title: [{ attr: "title", dir: "asc" }],
  state: [{ attr: "state", dir: "asc" }],
  repo: [{ attr: "repo", dir: "asc" }],
  author: [{ attr: "author", dir: "asc" }],
};

// LEGACY_FILTERS are the attributes that same toolbar gave a parameter
// of its own -- "/?repo=acme/widgets" -- each of which is now the
// condition "repo is any of acme/widgets". Kept exactly as that build
// wrote them rather than derived from today's attribute list, since what
// they have to go on parsing is what was once written, not what is
// offered now.
const LEGACY_FILTERS = [
  "repo",
  "base",
  "capability",
  "author",
  "origin",
  "kind",
  "autoMerge",
];

// parseNarrowing reads the query into the {search, sort, filters} shape
// the task views take (taskFilters.js's NO_NARROWING), or null when the
// query narrows nothing -- so a plain "/" parses to exactly what it did
// before there was a query to read. Null is "nobody has asked", which is
// what App answers with the deployment's own default narrowing
// (taskFilters.js's defaultNarrowing); a query that asks anything at all
// is taken as the whole of what the list is narrowed by, so a link that
// leaves the default condition out is a link asking for a list without
// it.
// parseNarrowing is exported as narrowingFromQuery for one caller
// outside this file: a saved board stores its filtering and sorting in
// this exact encoding (boards.js, model.Board's own doc comment), so the
// string in the store and the string in the address bar are read by the
// same parser rather than by two that can disagree.
export function narrowingFromQuery(search) {
  return parseNarrowing(search);
}

// queryFromNarrowing is its inverse, exported for the same caller: the
// "Edit board" button hands an agent the query the reader is looking at
// this moment, which is the narrowing App is holding rather than
// anything the address bar has been told yet.
//
// It never writes the empty marker: an empty board query already has a
// meaning of its own (boards.js's boardNarrowing -- nobody has narrowed
// this board), and the stored row keeps it.
export function queryFromNarrowing(narrowing) {
  const query = narrowingQuery(narrowing, false);
  return query.startsWith("?") ? query.slice(1) : query;
}

function parseNarrowing(search) {
  const params = new URLSearchParams(search || "");
  const q = params.get("q") || "";
  const sort = parseSort(params.get("sort"));
  const filters = parseFilters(params);
  if (q === "" && sort.length === 0 && filters.length === 0)
    return params.getAll("f").includes(EMPTY_MARKER) ? NO_NARROWING : null;
  return { search: q, sort, filters };
}

function parseSort(raw) {
  if (!raw) return [];
  if (raw in LEGACY_SORTS) return LEGACY_SORTS[raw];
  const keys = [];
  for (const part of raw.split(",")) {
    const [attr, dir] = part.split(":");
    if (!attr) continue;
    keys.push({ attr, dir: dir === "desc" ? "desc" : "asc" });
  }
  return keys;
}

function parseFilters(params) {
  const filters = [];
  for (const raw of params.getAll("f")) {
    const cond = conditionFromQuery(raw);
    // A link written while task views defaulted to leaving interactive
    // tasks out still carries that clause; it means nothing now.
    if (cond && !isLegacyInteractiveClause(cond)) filters.push(cond);
  }
  for (const attr of LEGACY_FILTERS) {
    const value = params.get(attr);
    if (value) filters.push({ attr, op: "any", values: [value] });
  }
  return filters;
}

// narrowingQuery is parseNarrowing's inverse: "?q=ci&f=repo:any:acme/
// widgets" for what is actually narrowed, "" for what isn't -- so an
// un-narrowed list still lives at "/" rather than at "/?".
//
// "Isn't narrowed" is two different things, though, and only one of
// them is "": a null narrowing is nobody having asked, which App answers
// with the deployment's default or a board's own question, while an
// empty one is a reader who took every condition off and asked for
// everything. Written the same way, a reload, Back/Forward or a shared
// link turned the second into the first and the closed tasks the reader
// had just asked for vanished again. So an empty narrowing is written
// as "?f=none" (EMPTY_MARKER) -- a clause that is not a condition's
// shape, so no build ever read it as one -- and parseNarrowing reads it
// back as NO_NARROWING. `marksEmpty` turns that off for the callers to
// whom the two mean the same thing.
//
// Parameters are written in a fixed order (the search, the sort, then
// the conditions in the order the editor holds them) so the same
// narrowing always produces the same URL.
const EMPTY_MARKER = "none";

function narrowingQuery(narrowing, marksEmpty = true) {
  if (!narrowing) return "";
  const params = new URLSearchParams();
  if (narrowing.search) params.set("q", narrowing.search);
  const sort = narrowing.sort || [];
  if (sort.length > 0)
    params.set("sort", sort.map((k) => `${k.attr}:${k.dir}`).join(","));
  for (const c of narrowing.filters || []) {
    params.append(
      "f",
      `${c.attr}:${c.op}:${(c.values || []).map(escapeValue).join("|")}`,
    );
  }
  // The separators back as themselves: all four are legal in a query
  // string, and this is a link somebody reads before they send it.
  const query = params
    .toString()
    .replace(/%2F/g, "/")
    .replace(/%3A/g, ":")
    .replace(/%7C/g, "|")
    .replace(/%2C/g, ",");
  if (query) return `?${query}`;
  return marksEmpty ? `?f=${EMPTY_MARKER}` : "";
}

// buildPath is parsePath's inverse: the path App's current state should
// be showing at any moment. App diffs this against window.location on
// every relevant state change to decide whether the address bar needs
// updating at all.
//
// The narrowing rides along on the paths showsTaskView admits and is
// dropped from the rest, so opening a task or the settings pane over a
// narrowed list leaves the query behind for as long as that pane is up
// and closing it puts the query back -- App holds the narrowing itself
// throughout, the same way it goes on holding the view behind
// /settings.
//
// `metricsNarrowing` is the second one, and the only path that writes
// it is /metrics: it is the scope the Metrics pane's report is drawn
// over, held separately by App for the reason parsePath gives.
// `metricsWindow` follows it, the window the pane's picker is on.
// `boardWindow` is the same picker on an open report board, written
// after that board's narrowing.
export function buildPath({
  view,
  taskId,
  repo,
  repoTab,
  showReleases,
  release,
  boardId,
  scheduleId,
  templateId,
  suiteId,
  suiteRunId,
  ciRunId,
  sessionId,
  settingsSection,
  settingsItem,
  systemTab,
  metricsNarrowing,
  metricsWindow,
  boardWindow,
  showNewTask,
  newTaskFields,
  narrowing,
  stateFilter,
  repoComparison,
  processQuery,
}) {
  const query = narrowingQuery(narrowing);
  if (showNewTask) return `/new${newTaskFieldsQuery(newTaskFields)}`;
  if (taskId) return `/tasks/${taskId}`;
  // Settings and System are views with their section or tab in the
  // path. The bare /settings is the section list on a phone and General
  // elsewhere (App fills General in); the bare /system is Health. The
  // Metrics tab carries its own query rather than the one beside it:
  // what it says is which report is on screen (report.js's scope).
  if (view === "settings")
    return settingsSection
      ? `/settings/${encodeURIComponent(settingsSection)}${
          settingsItem ? `/${encodeURIComponent(settingsItem)}` : ""
        }`
      : "/settings";
  if (view === "system") {
    if (systemTab === "metrics")
      return `/system/metrics${metricsQuery(metricsNarrowing, metricsWindow)}`;
    return systemTab ? `/system/${encodeURIComponent(systemTab)}` : "/system";
  }
  // An open repo only means anything within the repos view -- App keeps
  // the two together, and the sidebar clears the repo on the way out of
  // that view, so this is belt and braces rather than a case that
  // arises. The same goes for each open item below: a schedule is only
  // showing while the schedules view is.
  if (view === "repos" && repo) {
    if (showReleases)
      return release
        ? `/repos/${repo}/releases/${encodeURIComponent(release)}`
        : `/repos/${repo}/releases`;
    if (repoComparison?.head)
      return `/repos/${repo}/compare/${comparisonRefs(repoComparison)}`;
    if (repoTab && repoTab !== "overview")
      return `/repos/${repo}/settings/${repoTab}${query}`;
    return `/repos/${repo}${query}`;
  }
  if (view === "boards" && boardId)
    return `/boards/${boardId}${metricsQuery(narrowing, boardWindow, true)}`;
  // A suite run's own pane wins over an open suite: the two never open
  // together, and a run is the pane on top if they ever did.
  if (view === "processes") {
    // Written as the ids are, the way /schedules/<id> was: an id the
    // address bar already shows decoded must not be rewritten encoded.
    if (suiteRunId) return `/processes/suites/runs/${suiteRunId}`;
    if (scheduleId) return `/processes/schedules/${scheduleId}`;
    if (templateId) return `/processes/templates/${templateId}`;
    if (suiteId) return `/processes/suites/${suiteId}`;
    return `/processes${processQueryString(processQuery)}`;
  }
  if (view === "ci" && ciRunId) return `/ci/${ciRunId}`;
  if (view === "sessions") return `/sessions`;
  if (view === "session")
    return sessionId ? `/session/${sessionId}` : `/session`;
  if (view === "tasks") {
    // The rail's state first, then the narrowing: "/?state=closed&f=..."
    // reads as which list it is and then how it is narrowed.
    const listQuery =
      stateFilter && stateFilter !== "all"
        ? `?state=${encodeURIComponent(stateFilter)}${query ? `&${query.slice(1)}` : ""}`
        : query;
    return `/${listQuery}`;
  }
  return `/${view}`;
}
