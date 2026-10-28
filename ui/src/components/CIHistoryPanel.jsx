import { Typography } from "@mui/material";
import { formatBytes } from "./PackageCachePanel.jsx";

// What this deployment's stored CI history occupies, in the System
// pane's Storage section (pkg/ui/cistorage.go, grain/task-290).
//
// The four bounds on it are editable on Settings and take effect without
// a restart (grain/task-269). What that form cannot say is how much of
// the ceiling is in use, which is the number that decides which way to
// move it: an operator raising it because failures are being swept
// before anybody reads them, and one lowering it because a gigabyte of
// logs nobody opens is filling the volume, are looking at the same
// screen and need the same figure to tell them apart.
//
// So it is here rather than on the form, for the reason the package
// cache's own figures were here until they got a page of their own
// (PackageCachePanel's doc comment, grain/task-321): this is the pane
// that draws the disk bars it is filling, and this is the poll those
// bars are read by. It borrows that
// panel's formatBytes for the same reason -- two readings of one volume
// rendered in two different units is how a section like this stops being
// comparable.
//
// No button. Unlike the cache there is nothing to press: the sweep is
// the bounds, and changing what is kept is changing them.

const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

// oldestAge is how far back the evidence goes, in the words somebody
// asking "will Friday's failure still be readable on Monday" would use.
// "" when nothing is stored at all, which the caller says in its own
// words rather than dressing up as an age.
export function oldestAge(iso, now = Date.now()) {
  if (!iso) return "";
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return "";
  const ms = now - at;
  if (ms >= DAY_MS) {
    const days = Math.floor(ms / DAY_MS);
    return `${days} ${days === 1 ? "day" : "days"}`;
  }
  if (ms >= HOUR_MS) {
    const hours = Math.floor(ms / HOUR_MS);
    return `${hours} ${hours === 1 ? "hour" : "hours"}`;
  }
  return "under an hour";
}

// ciHistorySummary is what is stored, in one line: the bytes against the
// ceiling that sweeps them, how many runs still have logs, and how far
// back those logs go.
export function ciHistorySummary(history, now = Date.now()) {
  if (!history) return "";
  const bytes = history.bytes || 0;
  const runs = history.runs || 0;
  const parts = [
    history.ceiling
      ? `${formatBytes(bytes)} of ${formatBytes(history.ceiling)}`
      : `${formatBytes(bytes)}, no ceiling`,
  ];
  if (runs === 0) {
    // Nothing stored is a reading, not a gap: a deployment that has just
    // swept, and one whose CI has never run, both land here.
    parts.push("no run is holding step output");
    return parts.join(" · ");
  }
  parts.push(
    `${runs} ${runs === 1 ? "run" : "runs"} still ${runs === 1 ? "has" : "have"} step output`,
  );
  const age = oldestAge(history.oldestAt, now);
  if (age) parts.push(`the oldest of it ${age} old`);
  return parts.join(" · ");
}

// ciHistoryBounds is what this deployment has said it will keep, so that
// the figures above are read against something. Every number here is the
// one in force: a setting left blank shows grain's own default rather
// than a blank, and a bound an operator has switched off is left out
// rather than printed as a very large number.
export function ciHistoryBounds(history) {
  if (!history) return "";
  const parts = [];
  if (history.ceiling)
    parts.push(`up to ${formatBytes(history.ceiling)} of it`);
  else parts.push("every byte of it");
  if (history.ageDays) {
    parts.push(
      `none older than ${history.ageDays} ${history.ageDays === 1 ? "day" : "days"}`,
    );
  }
  parts.push(
    history.runsKept
      ? `${history.runsKept} runs' records behind it`
      : "every run's record behind it",
  );
  return `Kept: ${parts.join(", ")}.`;
}

// CIHistoryPanel is those two lines, and nothing when this deployment
// could not measure it -- a reading that failed costs the Storage
// section one entry rather than the pane its disk bars, which is the
// same bargain the proxy cache's figures strike.
export default function CIHistoryPanel({ history, now = Date.now() }) {
  if (!history) {
    return (
      <Typography variant="caption" color="text.secondary">
        What this deployment's CI history occupies could not be read.
      </Typography>
    );
  }
  return (
    <>
      <Typography variant="body2" color="text.secondary">
        {ciHistorySummary(history, now)}
      </Typography>
      <Typography variant="caption" color="text.secondary" component="div">
        {ciHistoryBounds(history)}
      </Typography>
    </>
  );
}
