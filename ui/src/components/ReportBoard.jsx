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
  Fragment,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Box,
  Button,
  CircularProgress,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { attributesFor } from "../taskAttributes.js";
import { NO_NARROWING } from "../taskFilters.js";
import {
  DEFAULT_REPORT_WINDOW,
  REPORT_WINDOWS,
  boardSections,
  hasAbsoluteSpan,
  reportPath,
  reportScope,
  reportSectionLabel,
  reportWindow,
  scopeSummary,
  sectionTitle,
} from "../report.js";
import { formatSeconds } from "./metricsFormat.js";
import { childRenderKeys } from "../childOrder.js";
import { sectionPlaces } from "../sectionOrder.js";
import {
  MoveHandle,
  MoveSlot,
  MovingBar,
  usePickup,
} from "./ReorderPickup.jsx";
import {
  AgentsSection,
  BacklogSection,
  CapacitySection,
  ChecksSection,
  CIQueueingSection,
  GitHubCISection,
  CyclesSection,
  EmptyWindowNote,
  LatencySection,
  MergesSection,
  PromptsSection,
  SandboxesSection,
  ThroughputSection,
  ToolsSection,
} from "./MetricsSections.jsx";
import NarrowingBar from "./NarrowingBar.jsx";

// ReportBoard is grain's own measurement report -- throughput, latency,
// how attempts ended, what is still waiting -- over the tasks a
// narrowing names (GET /api/metrics; pkg/metrics computes it and `grain
// metrics` prints the same numbers at a terminal).
//
// It has two callers (grain/task-379):
//
//   - a board of kind "report" (grain/task-83), which prints the
//     sections that board names, scoped by the board's own query;
//   - the Metrics pane at /metrics (MetricsOverlay.jsx), which is this
//     with no board row behind it: the whole report, scoped by whatever
//     the URL or the toolbar says and by nothing at all when they say
//     nothing.
//
// One component rather than two, because with a scope, a window picker,
// a "not in force" line and a toolbar there is nothing left that told
// the pane apart from a board -- and forty lines of that copied into a
// second file is exactly the drift PanelBoard.jsx and report.js argue
// against everywhere else in this report. `board` is therefore optional:
// no board is "the whole report", which is what boardSections already
// falls back to for a board that names no sections.
//
// Unlike the Logs and Sandbox health panels on the System pane, there is
// no poll: a report costs a full scan of `task` and `task_run` every
// time it is asked for (README, "Measuring throughput and latency"), and
// nothing it shows moves fast enough to be worth that every few seconds.
// It loads once, reloads when the question or the window changes, and
// otherwise waits for the Refresh button.
//
// A report board is the first saved dashboard that is not a view of the
// backlog. The other four draw the task rows App already holds; this one
// asks GET /api/metrics, because a p90 is not something a browser can
// compute from a filtered list -- a percentile over a subset is not a
// subset of a percentile.
//
// # Nothing here is drawn twice
//
// Every section is the component the Metrics pane draws
// (MetricsSections.jsx), which is where they moved to the day this page
// was written. PanelBoard.jsx makes the same argument for panels and it
// is the same argument: a second page with its own percentile table would
// be a second answer to a question this codebase has answered once, and
// the two would disagree within a release. What this page adds is which
// sections to draw and what to ask for -- not how to draw any of them.
//
// # The query is a scope, and the page says what it could not use
//
// A board's query is one field for every kind (model.Board), so what it
// means here is what a set of conditions on tasks can honestly say about
// a report over those tasks: a repo, an author and a date range, which
// become the scope the daemon computes under (report.js has the full
// argument, including why a date range is read as the report's window
// rather than as a filter on filing dates).
//
// Everything else in the query -- a search, a state, a sort -- is dropped,
// and it is dropped *visibly*. A report board saved off a narrowed list
// will usually carry a condition or two a report cannot use, and the two
// dishonest options are refusing to render and pretending. So the page
// prints one line naming what it left out, which also answers the
// question its reader is about to ask, which is why the numbers are
// bigger than they expected.
//
// # The toolbar narrows the report itself
//
// The same NarrowingBar every task view carries (grain/task-111): "this
// report, but only acme/widgets, just for a minute" re-asks the endpoint
// with a different scope rather than filtering anything on screen. The
// throughput panel on a dashboard scopes itself the same way now
// (PanelBoard.jsx, grain/task-317) -- it could not until this change,
// because the endpoint took no scope to give it.
//
// Which narrowing that is depends on the caller. A board is narrowed by
// the one App holds for every task view, so moving between a board and
// the list beside it keeps the question. The pane has a narrowing of its
// own, held separately and cleared when it is opened from the rail
// (App's metricsNarrowing, paths.js): the deployment-wide report should
// not quietly inherit the question of whatever list the pane was opened
// over, and a link to a scoped report should not narrow that list on the
// way past. Both ride in the URL, so a report somebody has scoped is a
// link they can send either way.
export default function ReportBoard({
  board,
  config,
  // A report nobody has narrowed is the report over everything, which is
  // what the Metrics pane has always been -- and the props default so
  // that a caller can leave the narrowing to App, as every task view
  // does (PanelBoard.jsx's own defaults).
  narrowing = NO_NARROWING,
  onNarrow = () => {},
  // The tasks the toolbar's value menus are built from, and nothing
  // else: the report itself is the daemon's, computed under the scope
  // the narrowing becomes. A menu offers only the values the tasks it
  // is handed carry (taskFilters.js's optionsFor), so a toolbar handed
  // none offered "No values to choose from" under Repo -- the one
  // condition a report is most often scoped by could only be typed into
  // the address bar.
  tasks = [],
  onOpenTask,
  // Where a link into the task list goes when it is simply clicked (the
  // backlog's state counts) -- App's openPanelView, the dashboard's own.
  // Without it those links are followed as plain hrefs.
  onOpen,
  showError,
  onMoveSection,
  // The picked window, when the caller keeps it: the Metrics pane and a
  // report board both do, so that it can ride in the URL (App's
  // metricsWindow and boardWindow, paths.js). A caller that leaves both
  // off has the pick held here. Either way null is "nobody has picked
  // one".
  pickedWindow,
  onPickWindow,
}) {
  const attributes = useMemo(() => attributesFor(config), [config]);
  const scope = useMemo(
    () => reportScope(narrowing, attributes),
    [narrowing, attributes],
  );
  const sections = useMemo(() => boardSections(board), [board]);
  const zone = useTimeZone();

  // The window is the reader's own pick, and null until they make one --
  // at which point it is theirs and stops following the query. Before
  // that the report is over whatever span the question names, which is
  // the whole reason a board can name one and a panel's link can carry
  // one (panels.js's panelReportNarrowing).
  //
  // Picking the window the report would open on anyway is picking
  // nothing, and is held as null: it is how "Last 7 days" again takes the
  // Metrics pane back to its bare "/metrics".
  const [heldPick, setHeldPick] = useState(null);
  const controlled = onPickWindow !== undefined;
  const picked = controlled ? pickedWindow || null : heldPick;
  const setPicked = controlled ? onPickWindow : setHeldPick;
  const absolute = hasAbsoluteSpan(scope);
  const unpicked = reportWindow(scope, DEFAULT_REPORT_WINDOW);
  const window = picked || unpicked;

  const [report, setReport] = useState(null);
  const [loading, setLoading] = useState(false);
  // Why the newest fetch failed, or null. showError's banner clears
  // itself after a few seconds, and a report that never arrived went on
  // drawing "Loading the report" underneath it for good -- a page that
  // reads as slow rather than as broken, with nothing to say Refresh is
  // the way out. A failure after a report has landed keeps that report
  // on screen and only raises the banner, the metrics panel's own rule
  // (PanelBoard.jsx).
  const [failed, setFailed] = useState(null);
  // Only the newest fetch is allowed to land: narrowing twice quickly
  // otherwise races, and a slow report for a scope nobody is looking at
  // any more would overwrite the one they are.
  const seq = useRef(0);
  const path = reportPath({ scope, window, buckets: BUCKETS });

  const refresh = useCallback(async () => {
    const mine = ++seq.current;
    setLoading(true);
    try {
      const result = await api(path);
      if (seq.current === mine) {
        setReport(result);
        setFailed(null);
      }
    } catch (err) {
      if (seq.current === mine) setFailed(String(err?.message || err));
      showError?.(err);
    } finally {
      if (seq.current === mine) setLoading(false);
    }
  }, [path, showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const summary = scopeSummary(scope);
  return (
    <>
      {/* The toolbar is the shared one, so a report is narrowed by the
          same attributes with the same wording as a list -- and the
          narrowing rides in the URL like every other view's, so a report
          somebody has scoped is a link they can send. */}
      <NarrowingBar
        tasks={tasks}
        config={config}
        narrowing={narrowing}
        onNarrow={onNarrow}
        searchPlaceholder="Search tasks…"
      >
        {!absolute && (
          <FormControl size="small" sx={{ minWidth: 160 }}>
            <InputLabel id="report-window-label">Window</InputLabel>
            <Select
              labelId="report-window-label"
              label="Window"
              value={window}
              onChange={(e) =>
                setPicked(e.target.value === unpicked ? null : e.target.value)
              }
            >
              {REPORT_WINDOWS.map((w) => (
                <MenuItem key={w.value} value={w.value}>
                  {w.label}
                </MenuItem>
              ))}
              {!REPORT_WINDOWS.some((w) => w.value === window) && (
                <MenuItem key={window} value={window}>
                  {window.endsWith("d")
                    ? `Last ${window.slice(0, -1)} days`
                    : window}
                </MenuItem>
              )}
            </Select>
          </FormControl>
        )}
        <Button
          size="small"
          variant="outlined"
          onClick={refresh}
          disabled={loading}
        >
          Refresh
        </Button>
      </NarrowingBar>

      <section className="metrics-panel">
        {report === null && failed !== null && !loading ? (
          <Typography variant="body2" color="text.secondary" role="alert">
            The report could not be loaded ({failed}). Refresh to try again.
          </Typography>
        ) : report === null ? (
          <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
            <CircularProgress size={28} aria-label="Loading the report" />
          </Box>
        ) : (
          <Box>
            <Typography
              variant="caption"
              color="text.secondary"
              component="div"
              sx={{ mb: summary || scope.ignored.length > 0 ? 0.5 : 2 }}
            >
              {formatDateTime(report.since, zone)} →{" "}
              {formatDateTime(report.until, zone)}
              {" · "}
              {formatSeconds(report.windowSeconds)}
            </Typography>
            {summary && (
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
                sx={{ mb: scope.ignored.length > 0 ? 0.5 : 2 }}
              >
                Measured over the tasks {summary}.
              </Typography>
            )}
            {/* Said once, plainly, and not as a warning: a query with a
                state in it is an ordinary thing to have saved a report
                from, and the reader only needs to know it is not in
                force. */}
            {scope.ignored.length > 0 && (
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
                sx={{ mb: 2 }}
              >
                A report is measured over whole distributions, so the rest of
                this question is not in force: {scope.ignored.join(", ")}.
              </Typography>
            )}

            {/* A section the daemon could not build is empty below, and
                an empty section reads as a quiet window -- so the page
                names it, with the reference its cause is logged under. */}
            {report.failed?.length > 0 && (
              <Typography
                variant="body2"
                color="error"
                component="div"
                role="alert"
                sx={{ mb: 2 }}
              >
                Not available in this report:{" "}
                {report.failed
                  .map((f) => `${f.section} (${f.error})`)
                  .join(", ")}
                .
              </Typography>
            )}
            <ReportSections
              board={board}
              sections={sections}
              report={report}
              scope={scope}
              onOpenTask={onOpenTask}
              onOpen={onOpen}
              onMoveSection={onMoveSection}
            />
            <EmptyWindowNote report={report} />
          </Box>
        )}
      </section>
    </>
  );
}

// BUCKETS is how many points the trend lines carry -- twice pkg/metrics'
// own default of 12, because a sparkline is drawn from the shape of the
// series rather than read off point by point, and 24 is enough for that
// without the spans getting so short that a quiet deployment's line is
// mostly zeroes. PanelBoard.jsx's own sparklines carry the same count,
// so a panel and the full report draw the same shape of the same series.
const BUCKETS = 24;

// ReportSection draws one section of the report, and its own heading when
// the board gave it one. A section that named a kind this build does not
// have never reaches here -- report.js drops it rather than substituting
// another, since no section stands in for another and drawing the
// throughput because a row said "burndown" would be a lie about what is
// on screen.
function ReportSections({
  board,
  sections,
  report,
  scope,
  onOpenTask,
  onOpen,
  onMoveSection,
}) {
  const [dragKey, setDragKey] = useState(null);
  const [overKey, setOverKey] = useState(null);
  // A report board that names no sections prints the whole report
  // (report.js's boardSections) and has no rows in tables/board_section.json
  // to reorder or explain as missing.
  const namedSections = Boolean(board?.sections?.length);
  const enabled = Boolean(namedSections && onMoveSection);
  const { picked, toggle, cancel } = usePickup();
  const pickedPresent =
    enabled &&
    Boolean(picked?.ids?.length) &&
    sections.some((s) => s.key === picked.ids[0]);
  useEffect(() => {
    if (picked && !pickedPresent) cancel();
  }, [picked, pickedPresent, cancel]);
  useEffect(() => cancel, [board?.id, cancel]);
  const pickedKey = pickedPresent ? picked.ids[0] : null;
  const places = pickedKey !== null ? sectionPlaces(sections, pickedKey) : [];
  const offered = new Set(places);
  const renderKeys = childRenderKeys(sections);
  const name = (section, i) => `section ${i + 1}, “${sectionTitle(section)}”`;
  const stop = () => {
    setDragKey(null);
    setOverKey(null);
  };
  const move = (beforeKey) => {
    if (pickedKey !== null) onMoveSection(board, pickedKey, beforeKey);
    cancel();
  };
  const drop = (beforeKey) => (event) => {
    event.preventDefault();
    if (dragKey !== null) onMoveSection(board, dragKey, beforeKey);
    stop();
  };

  return (
    <>
      {pickedKey !== null && (
        <MovingBar
          count={1}
          noun="section"
          nowhere={places.length === 0}
          onCancel={cancel}
        />
      )}
      <ul className="report-sections">
        {sections.map((section, i) => (
          <Fragment key={renderKeys[i]}>
            {pickedKey !== null && offered.has(section.key) && (
              <MoveSlot
                label={`Move here, above ${name(section, i)}`}
                onMove={() => move(section.key)}
              />
            )}
            <ReportSection
              section={section}
              report={report}
              explain={namedSections}
              scope={scope}
              onOpenTask={onOpenTask}
              onOpen={onOpen}
              handle={
                enabled ? (
                  <MoveHandle
                    label={
                      pickedKey === section.key
                        ? `Stop moving ${name(section, i)}`
                        : `Move ${name(section, i)}`
                    }
                    picked={pickedKey === section.key}
                    disabled={sectionPlaces(sections, section.key).length === 0}
                    onToggle={() => toggle([section.key])}
                  />
                ) : null
              }
              grip={
                enabled
                  ? {
                      draggable: true,
                      onDragStart: () => setDragKey(section.key),
                      onDragEnd: stop,
                    }
                  : undefined
              }
              target={
                enabled
                  ? {
                      onDragOver: (event) => {
                        if (dragKey === null) return;
                        event.preventDefault();
                        setOverKey(section.key);
                      },
                      onDrop: drop(section.key),
                    }
                  : undefined
              }
              dragging={dragKey === section.key}
              over={dragKey !== null && overKey === section.key}
              picked={pickedKey === section.key}
            />
          </Fragment>
        ))}
        {pickedKey !== null && offered.has(null) && (
          <MoveSlot
            label="Move here, at the end of the report"
            onMove={() => move(null)}
          />
        )}
        {enabled && dragKey !== null && (
          <li
            className={`report-section-drop-end${overKey === "__end__" ? " report-section-drop-target" : ""}`}
            onDragOver={(event) => {
              event.preventDefault();
              setOverKey("__end__");
            }}
            onDrop={drop(null)}
          />
        )}
      </ul>
    </>
  );
}

function ReportSection({
  section,
  report,
  explain,
  scope,
  onOpenTask,
  onOpen,
  handle,
  grip,
  target,
  dragging,
  over,
  picked,
}) {
  const Body = SECTION_BODIES[section.kind];
  // Every section draws nothing at all when the report has nothing to
  // say in it (MetricsSections.jsx), which on the Metrics pane is the
  // section quietly not being there. A board, though, named this section
  // -- and gave it a handle, and often a title -- so drawing nothing
  // left a heading over an empty space, or a bare handle, that looked
  // like a section that failed to load (grain/task-1356). Whether the
  // body drew anything is only known once it has, so it is read back off
  // the DOM rather than guessed at from the report.
  const body = useRef(null);
  const [empty, setEmpty] = useState(false);
  useLayoutEffect(() => {
    const now = Boolean(body.current) && body.current.childNodes.length === 0;
    if (now !== empty) setEmpty(now);
  });
  if (!Body) return null;
  // A heading of the section's own name would be said twice, since every
  // section component already writes one; only a title the board chose
  // is worth printing above it. It is an h3 like those: subtitle2's own
  // element is an h6, which put a titled section three levels under the
  // untitled ones beside it in a screen reader's heading list.
  // A section the board did not name and that drew nothing is still a
  // list item until it is hidden: the Report pane renders every section,
  // and the empty ones were each an extra, silent entry in a screen
  // reader's "list, 13 items". `hidden` rather than not rendering it,
  // because the body has to be in the DOM for the effect above to see
  // whether it has drawn something yet.
  return (
    <li
      className={`report-section${dragging ? " report-section-dragging" : ""}${over ? " report-section-drop-target" : ""}${picked ? " report-section-picked" : ""}`}
      hidden={empty && !explain}
      {...target}
    >
      <div className="report-section-grip" {...grip}>
        {handle}
        {section.title && (
          <Typography
            component="h3"
            variant="subtitle2"
            sx={{ mt: 2.5, mb: -0.5 }}
          >
            {sectionTitle(section)}
          </Typography>
        )}
      </div>
      <div ref={body}>
        <Body
          report={report}
          scope={scope}
          onOpenTask={onOpenTask}
          onOpen={onOpen}
        />
      </div>
      {explain && empty && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2.5 }}>
          {reportSectionLabel(section.kind)}: nothing to report over this
          window.
        </Typography>
      )}
    </li>
  );
}

// SECTION_BODIES is the section vocabulary joined to the components that
// draw it: report.js's REPORT_SECTIONS is the list, model.ReportSections
// is the same list server-side, and this is the third line adding a
// section costs.
const SECTION_BODIES = {
  throughput: ThroughputSection,
  latency: LatencySection,
  runs: CapacitySection,
  backlog: BacklogSection,
  tools: ToolsSection,
  prompts: PromptsSection,
  checks: ChecksSection,
  ci_queueing: CIQueueingSection,
  github_ci: GitHubCISection,
  merges: MergesSection,
  cycles: CyclesSection,
  sandboxes: SandboxesSection,
  agents: AgentsSection,
};
