import { Alert, Typography } from "@mui/material";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// count is "3 batches" / "1 batch": both spellings given, since English
// plurals here are not a suffix ("batch" -> "batches").
const count = (n, one, many) => `${n} ${n === 1 ? one : many}`;

// MergeBatchingNote is ui.Settings.mergeBatching on screen: what the
// merge queue's batches have actually cost and saved, under the field
// that says how big they are (grain/task-300).
//
// It is here because the field above it is a bet nobody had the numbers
// to settle. A batch of N is a wager that all N changes are good: it
// saves suites when it pays off and costs an extra one, plus the latency
// of the changes that were fine, when it does not. Three was picked as
// "small enough to be safe" and left to be measured -- and nothing
// measured it, because a batch's row is deleted the moment it ends. This
// is that record, counted.
//
// The saving is given as an upper bound rather than as a number, which
// is grain/task-501's correction: the suites the serial queue is charged
// for are tip moves, and one post-merge run per branch at a time means a
// busy branch's merges already share them (model.BaseBranchCI.Covers).
//
// The numbers rather than a verdict, like SandboxCommitmentNote beside
// it, because the right size depends on how often this deployment's own
// changes break each other and on how scarce its CI capacity is. What
// the pane can do is put the two readings that decide it in one place:
// batches that passed first time (the bet paying), and batches that had
// to be bisected (the bet failing). The last line is the one that
// catches the commonest case of all -- a limit no batch has ever reached
// is not a limit that is costing anything, however well or badly the
// batches do, and raising it would change nothing.
export default function MergeBatchingNote({ batching, limit, sx }) {
  const zone = useTimeZone();
  // Absent until a batch has finished: a deployment that has never formed
  // one has nothing to read, and zeroes drawn as a rate would read as
  // "every batch failed" (ui.Client.mergeBatching returns nil).
  if (!batching || !batching.batches) return null;

  const failed = batching.split + batching.culprits;
  const saved = batching.saved;
  return (
    <Alert severity="info" icon={false} sx={sx}>
      <Typography variant="body2">
        {count(batching.batches, "batch", "batches")} since{" "}
        {formatDateTime(batching.firstAt, zone)}: {batching.firstTry} of{" "}
        {batching.fresh} formed from the queue passed first time, {failed}{" "}
        failed and had to be bisected, and {batching.abandoned} ended with no
        answer at all.
      </Typography>
      {batching.changes > 0 && batching.serialSuites === 0 ? (
        // Nothing tests this deployment's base branches after a merge, so
        // the one-at-a-time queue being compared against spends nothing
        // per merge and a batch cannot save a suite however well it does
        // (model.MergeBatchRecord.PostMerge). Said rather than shown as a
        // negative saving, which would read as batching going wrong.
        <Typography variant="body2" sx={{ mt: 1 }}>
          {count(batching.changes, "change", "changes")} landed on{" "}
          {count(batching.suites, "suite", "suites")}. Nothing here tests a base
          branch after a merge, so merging one at a time would have spent no
          suites at all: what these batches bought was the combination judged
          before it landed, not capacity.
        </Typography>
      ) : (
        <Typography variant="body2" sx={{ mt: 1 }}>
          {count(batching.changes, "change", "changes")} landed on{" "}
          {count(batching.suites, "suite", "suites")}, where merging them one at
          a time would have spent at most {batching.serialSuites} &mdash;{" "}
          {saved === 0
            ? "no saving either way"
            : saved > 0
              ? `${count(saved, "suite", "suites")} saved at most`
              : `${count(-saved, "suite", "suites")} more than the queue would have spent`}
          . Both sides count the run grain starts on the base branch after a
          merge, which a batch pays once and a one-at-a-time queue pays per
          change &mdash; at most, because only one of those runs goes at a time
          per branch, so merges landing faster than a suite finishes already
          share one without any batching at all.
        </Typography>
      )}
      <Typography variant="body2" sx={{ mt: 1 }}>
        {batching.atLimit === 0
          ? `No batch has yet been held down by the limit of ${limit}: the biggest was ${batching.biggest}, so raising it would have changed nothing so far.`
          : `${count(batching.atLimit, "batch", "batches")} took every member the limit of ${limit} allowed with more still queued behind them.`}
      </Typography>
    </Alert>
  );
}
