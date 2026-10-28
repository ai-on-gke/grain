import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Box, CircularProgress } from "@mui/material";
import api, { isUnreachable, unreachableReason } from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { REFRESH_MS, RunDetail, retryRequest } from "./CIPage.jsx";
import Overlay from "./Overlay.jsx";

function formatRunError(err) {
  if (isUnreachable(err) && !err?.explained) {
    return unreachableReason(err);
  }
  return err?.message || String(err);
}

// CIRunOverlay owns the complete lifetime of reading one grain CI run.
// That keeps the deployment-wide CI listing and a repo's local listing as
// equal entry points: both leave their page in place underneath this pane.
export default function CIRunOverlay({ runId, onClose, onOpenRun, showError }) {
  const [run, setRun] = useState(null);
  const [gone, setGone] = useState("");
  const zone = useTimeZone();
  const inFlight = useRef(0);
  const reqSeq = useRef(0);
  const unreachableStreak = useRef(0);

  const refresh = useCallback(
    async (arg = false) => {
      const opening =
        typeof arg === "boolean" ? arg : Boolean(arg && arg.opening);
      const background =
        typeof arg === "object" && arg !== null && Boolean(arg.background);
      if (background && inFlight.current > 0) return;
      const seq = ++reqSeq.current;
      inFlight.current += 1;
      try {
        const found = await api(`/api/ci/runs/${encodeURIComponent(runId)}`);
        if (seq !== reqSeq.current) return;
        setRun(found);
        setGone("");
        unreachableStreak.current = 0;
      } catch (err) {
        if (seq !== reqSeq.current) return;
        if (opening) {
          setGone(formatRunError(err));
          return;
        }
        if (background && isUnreachable(err)) {
          unreachableStreak.current += 1;
          if (unreachableStreak.current < 2) return;
        } else {
          unreachableStreak.current = isUnreachable(err) ? 2 : 0;
        }
        if (showError) showError(err);
      } finally {
        inFlight.current = Math.max(0, inFlight.current - 1);
      }
    },
    [runId, showError],
  );

  useEffect(() => {
    setRun(null);
    setGone("");
    unreachableStreak.current = 0;
    refresh(true);
  }, [refresh]);

  // A conclusion is final, so only unfinished runs keep asking for their
  // next completed job and step output.
  useEffect(() => {
    if (gone || run?.conclusion) return undefined;
    const interval = setInterval(
      () => refresh({ background: true }),
      REFRESH_MS,
    );
    return () => clearInterval(interval);
  }, [gone, run?.conclusion, refresh]);

  // One prompt rather than a confirm and then a form: dismissing it is
  // still "no, leave the run alone" (prompt answers null), and what is
  // typed instead is the note. It is worth asking for, because the run
  // this stops is usually one an agent is waiting on: a bare "cancelled"
  // reads to it like a supersede or a daemon restart, and the note is the
  // only thing that tells it what actually happened and what to do next.
  const cancel = async () => {
    const reason = prompt(
      "Stop this run? The jobs in flight are told to give up and their sandboxes are destroyed.\n\n" +
        "Optionally, say why -- whoever reads this run afterwards, including the agent waiting on the branch, is told.",
      "",
    );
    if (reason === null) return;
    try {
      await api(`/api/ci/runs/${encodeURIComponent(runId)}`, {
        method: "DELETE",
        body: JSON.stringify({ reason: reason.trim() }),
      });
    } catch (err) {
      showError(err);
    }
    refresh();
  };

  // Retry is POST /api/ci/runs with the run's own coordinates
  // (retryRequest), and the pane follows to the run it started -- which,
  // for a task's run, is the one now holding its merge.
  const retry = async (failedOnly) => {
    const body = retryRequest(run, failedOnly);
    if (!body) return;
    try {
      const started = await api("/api/ci/runs", {
        method: "POST",
        body: JSON.stringify(body),
      });
      if (started?.id && started.id !== runId && onOpenRun) {
        onOpenRun(started.id);
        return;
      }
    } catch (err) {
      showError(err);
    }
    refresh();
  };

  return (
    <Overlay onClose={onClose} pane label={`CI run ${runId}`}>
      {gone ? (
        <Alert severity="info">{gone}</Alert>
      ) : run?.id === runId ? (
        <RunDetail run={run} zone={zone} onCancel={cancel} onRetry={retry} />
      ) : (
        <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
          <CircularProgress size={28} aria-label="Loading this run" />
        </Box>
      )}
    </Overlay>
  );
}
