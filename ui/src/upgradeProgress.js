// Reading GET /api/upgrade's step list (grain/task-37).
//
// pkg/upgrade decides an upgrade's whole plan before it starts and
// reports it as `steps`, each carrying a state ("pending", "running",
// "done", "failed", "skipped"), whatever it last had to say, and when
// it started and finished. Turning that into the two things the pane
// shows -- how far along the upgrade is, and how long the step it is on
// has been going -- is pure arithmetic, so it lives here where it can be
// tested without a render.
//
// Everything here tolerates a status that has no steps at all: that is
// what a status file written by an older grain looks like, and the pane
// falls back to its plain phase/detail display for one.

// upgradeProgress summarises a step list for the pane:
//
//   total     how many steps the upgrade plans to run
//   settled   how many are behind it (done, skipped, or failed)
//   percent   settled/total as a whole number, for the progress bar
//   current   the step running now, or the one that failed, or null
//   position  which number that step is, 1-based, for "step 2 of 5"
//
// A skipped step counts as settled: it is a step the upgrade will not
// spend any time on, so a bar that refused to count it would sit short
// of full on a deployment with no health check configured.
export function upgradeProgress(steps) {
  const list = Array.isArray(steps) ? steps : [];
  const total = list.length;
  const settled = list.filter((step) =>
    ["done", "skipped", "failed"].includes(step.state),
  ).length;
  let index = list.findIndex((step) => step.state === "running");
  if (index < 0) index = list.findIndex((step) => step.state === "failed");
  return {
    total,
    settled,
    percent: total === 0 ? 0 : Math.round((settled / total) * 100),
    current: index < 0 ? null : list[index],
    position: index < 0 ? 0 : index + 1,
  };
}

// stepElapsedMs is how long a step has been running, or how long it
// took: from its own startedAt to its finishedAt, or to now while it is
// still going. null when it never started (a pending step) or when the
// timestamps do not parse.
export function stepElapsedMs(step, now = Date.now()) {
  if (!step || !step.startedAt) return null;
  const started = new Date(step.startedAt).getTime();
  if (Number.isNaN(started)) return null;
  const end = step.finishedAt ? new Date(step.finishedAt).getTime() : now;
  if (Number.isNaN(end)) return null;
  return Math.max(0, end - started);
}

// formatElapsed writes a duration the way someone watching a build
// wants to read it: seconds until a minute, minutes and seconds until an
// hour, hours and minutes past that. Whole units only -- this is a
// number that changes every second on screen, and the digits that
// matter are the leading ones.
export function formatElapsed(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
