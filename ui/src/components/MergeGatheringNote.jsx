import { Alert, Typography } from "@mui/material";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { formatSeconds } from "./metricsFormat.js";

// count is "3 holds" / "1 hold": both spellings given, as in
// MergeBatchingNote beside it.
const count = (n, one, many) => `${n} ${n === 1 ? one : many}`;

// MergeGatheringNote is ui.Settings.mergeGathering on screen: what the
// merge queue's gather holds have actually done, under the field that
// says how long one may last (grain/task-440).
//
// It is here because the field above it is a duration nobody had any
// evidence about. A hold lives in the daemon's memory, and the row on
// the change it is holding is overwritten the moment the wait ends, so
// the only holds anything downstream ever saw were the ones that became
// a batch -- and those are the minority. The waits that produced
// nothing, which are exactly what the window's length has to be judged
// on, left no trace at all.
//
// The numbers rather than a verdict, like MergeBatchingNote: what the
// right window is depends on how long this deployment's own CI runs
// take to report. What the pane can do is put the three readings that
// decide it in one place -- holds that paid off and how quickly, holds
// that let go early, and holds that sat out the whole window for
// nothing.
export default function MergeGatheringNote({ gathering, sx }) {
  const zone = useTimeZone();
  // Absent until a hold has ended: a deployment whose queue has never
  // held anything, or one with the wait turned off, has nothing to read
  // (ui.Client.mergeGathering returns nil).
  if (!gathering || !gathering.holds) return null;

  return (
    <Alert severity="info" icon={false} sx={sx}>
      <Typography variant="body2">
        {count(gathering.holds, "hold", "holds")} since{" "}
        {formatDateTime(gathering.firstAt, zone)}: {gathering.batched} ended in
        a batch, {gathering.released} let go early when the run they were
        waiting on reported something other than a pass, and {gathering.expired}{" "}
        ran the full window and expired.
      </Typography>
      {gathering.batched > 0 ? (
        <Typography variant="body2" sx={{ mt: 1 }}>
          A wait that paid off took{" "}
          {formatSeconds(gathering.batchedWaitSeconds)} on average, against a
          window of {formatSeconds(gathering.windowSeconds)}.
        </Typography>
      ) : (
        <Typography variant="body2" sx={{ mt: 1 }}>
          No wait here has yet ended in a batch, so nothing these holds bought
          has been measured &mdash; only what they cost.
        </Typography>
      )}
      {gathering.expired > 0 ? (
        // The line the window is tuned on: holds that routinely run the
        // full window mean the run being waited for is simply slower
        // than the window, and the wait is latency with no batch at the
        // end of it (docs/merge-queue-batching.md).
        <Typography variant="body2" sx={{ mt: 1 }}>
          The {count(gathering.expired, "hold", "holds")} that expired waited{" "}
          {formatSeconds(gathering.expiredWaitSeconds)} each and bought nothing,{" "}
          {formatSeconds(gathering.expiredTotalSeconds)} of merge latency in
          all.
        </Typography>
      ) : (
        <Typography variant="body2" sx={{ mt: 1 }}>
          No hold has yet run its whole window: every wait ended in a batch or
          let go early, so the window is not what is bounding them.
        </Typography>
      )}
    </Alert>
  );
}
