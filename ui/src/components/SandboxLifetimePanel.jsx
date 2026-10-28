import { Typography } from "@mui/material";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { formatSeconds } from "./metricsFormat.js";

// How many sandboxes this deployment has built, and how long they lived
// (pkg/ui/sandboxusage.go, grain/task-315).
//
// It is drawn in two places -- the System pane, under the live sandboxes
// it is the history of, and the measurement report's own Sandboxes
// section -- so the words for it live here rather than in either page.
// An operator reading the pane and an agent reading `grain metrics
// -json` are meant to be able to quote the same figure at each other,
// and two pages phrasing "2,413 sandboxes, 1,204 hours" their own way is
// the first step to their disagreeing about it.
//
// Every figure here is cumulative and none of them is over a window,
// which is why the date is part of the sentence and not a footnote: the
// counters are an aggregate, there is nothing to slice by time, and a
// number quoted without "since" is a number nobody can check.

// countOf renders a count with thousands separators. These get large --
// a fortnight of this deployment's CI is a four-figure number -- and
// "2413" beside "1204" is two numbers nobody reads at a glance.
export function countOf(n) {
  return (n || 0).toLocaleString();
}

// hoursOf renders sandbox-hours: a decimal place while the figure is
// small enough for one to mean something, none once it is not. A
// deployment three hours into counting wants "2.4 h"; one a fortnight in
// does not want "1204.3 h".
export function hoursOf(hours) {
  const h = hours || 0;
  if (h < 10) return `${h.toFixed(1)} h`;
  return `${Math.round(h).toLocaleString()} h`;
}

// sandboxKindLine is one kind in a phrase: what it built, what that cost
// in machine time, and what one of them costs on average -- which is the
// number that makes two kinds comparable when one is built fifty times
// as often as the other.
//
// The two honesty clauses are appended only when they are non-zero: a
// sandbox that was asked for and never came up ("never started"), and
// one grain never saw stop, whose time is missing from the hours beside
// it ("not seen to stop"). Printing either as a nought would be noise on
// the deployments where they are nought, which is most of them.
export function sandboxKindLine(kind) {
  if (!kind) return "";
  const parts = [
    `${countOf(kind.created)} built`,
    hoursOf(kind.hours),
    `${formatSeconds(kind.meanSeconds)} each`,
  ];
  if (kind.failed > 0) parts.push(`${countOf(kind.failed)} never started`);
  if (kind.unaccounted > 0)
    parts.push(`${countOf(kind.unaccounted)} not seen to stop`);
  return parts.join(" · ");
}

// sandboxLifetimeSummary is the headline: how many altogether, how many
// hours, and since when. "" when nothing has been counted, which the
// caller says in its own words rather than dressing up as a total of
// nought.
export function sandboxLifetimeSummary(lifetime, zone) {
  const total = lifetime?.total;
  if (!total || !total.attempted) return "";
  const since = formatDateTime(total.since, zone);
  const head = `${countOf(total.created)} sandbox${total.created === 1 ? "" : "es"} built, ${hoursOf(total.hours)} of machine time`;
  return since ? `${head}, since ${since}` : head;
}

// SandboxLifetimePanel is that headline with a line per kind under it.
//
// Nothing at all where there is no counter to read -- an older
// deployment, or a reading that failed -- and one sentence where there
// is a counter that has counted nothing yet: "no sandbox has been built
// since counting began" is a reading, and a table of noughts under it
// would read as a deployment that builds none.
export default function SandboxLifetimePanel({ lifetime }) {
  const zone = useTimeZone();
  if (!lifetime) return null;
  const summary = sandboxLifetimeSummary(lifetime, zone);
  if (!summary) {
    return (
      <Typography variant="caption" color="text.secondary" component="div">
        No sandbox has been built since this deployment started counting them.
      </Typography>
    );
  }
  return (
    <>
      <Typography variant="body2" color="text.secondary">
        {summary}
      </Typography>
      {(lifetime.kinds || []).map((kind) => (
        <Typography
          key={kind.kind}
          variant="caption"
          color="text.secondary"
          component="div"
        >
          {kind.kind === "ci" ? "CI" : "Agent"} — {sandboxKindLine(kind)}
        </Typography>
      ))}
    </>
  );
}
