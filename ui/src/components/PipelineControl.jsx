import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Stack, TextField, Typography } from "@mui/material";
import CampaignIcon from "@mui/icons-material/Campaign";
import { resumeCancelsUpgrade, scheduledUpgradeHold } from "../alerts.js";
import api from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatTime } from "../time.js";
import NotifyRunningDialog from "./NotifyRunningDialog.jsx";

// PipelineControl is the operator's switch for the deployment-wide
// dispatch gate. It deliberately does not offer to stop anything already
// running: the server's pipeline pause prevents new work being picked up
// and lets every in-flight agent and CI run finish.
export default function PipelineControl({
  pause: configuredPause,
  tasks = [],
  onChanged,
  showError,
}) {
  const [pause, setPause] = useState(configuredPause || { paused: false });
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [notifyOpen, setNotifyOpen] = useState(false);
  const zone = useTimeZone();
  const runningTasks = useMemo(
    () =>
      (tasks || []).filter((t) => t.state === "running" && !t.consoleSession),
    [tasks],
  );

  // Keep this pane current when another control changes the same gate --
  // notably the Resume button in the banner above the application.
  useEffect(() => {
    setPause(configuredPause || { paused: false });
  }, [configuredPause]);

  async function change(method) {
    const question =
      method === "POST"
        ? "Pause the pipeline? Grain will stop starting new agent and CI runs. Runs already in flight will continue."
        : scheduledUpgradeHold(pause)
          ? resumeCancelsUpgrade(pause)
          : "Resume the pipeline? Grain will start dispatching queued agent and CI runs again.";
    if (!confirm(question)) return;

    setBusy(true);
    try {
      const opts = { method };
      if (method === "POST") {
        opts.body = JSON.stringify({ reason });
      }
      const response = await api("/api/pipeline-pause", opts);
      setPause(response.pause);
      if (method === "POST") setReason("");
      await onChanged?.();
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  }

  const since = pause.since ? ` since ${formatTime(pause.since, zone)}` : "";
  const reasonText = pause.reason ? ` Reason: ${pause.reason}` : "";
  const waiting = pause.untilTask
    ? ` Grain will resume automatically when task ${pause.untilTask} finishes.`
    : "";

  return (
    <>
      {pause.paused ? (
        <fieldset>
          <legend>Dispatch</legend>
          <Alert severity="warning" sx={{ mb: 2 }}>
            {pause.runNowPasses && (
              <strong className="pipeline-manual-mode">
                Paused — only tasks you Run now will start.{" "}
              </strong>
            )}
            The pipeline is paused{since}. No new agent runs are being started
            from the queue; runs already in flight are continuing.
            {pause.runNowPasses
              ? " Press Run now on a task, or select several and choose Run selected now, to start exactly those."
              : " Run now waits too: an upgrade is holding the queue, and its restart would discard the run."}
            {waiting}
            {reasonText}
          </Alert>
          <Button
            variant="contained"
            disabled={busy}
            onClick={() => change("DELETE")}
          >
            {busy ? "Resuming…" : "Resume pipeline"}
          </Button>
        </fieldset>
      ) : (
        <fieldset>
          <legend>Dispatch</legend>
          <Typography variant="body2" sx={{ mb: 2 }}>
            The pipeline is running. Pausing prevents Grain from starting new
            agent or CI runs while allowing every run already in flight to
            finish.
          </Typography>
          <Stack spacing={2} sx={{ maxWidth: 560 }}>
            <TextField
              label="Reason (optional)"
              value={reason}
              disabled={busy}
              onChange={(event) => setReason(event.target.value)}
              placeholder="For example: host maintenance"
              size="small"
            />
            <Button
              variant="contained"
              color="warning"
              disabled={busy}
              onClick={() => change("POST")}
              sx={{ alignSelf: "flex-start" }}
            >
              {busy ? "Pausing…" : "Pause pipeline"}
            </Button>
          </Stack>
        </fieldset>
      )}
      <fieldset style={{ marginTop: 24 }}>
        <legend>Running task agents</legend>
        <Typography variant="body2" sx={{ mb: 2 }}>
          Send a notice to every task agent currently in flight. The notice is
          added to each running task’s conversation and delivered on the agent’s
          next tool call.
        </Typography>
        <Button
          variant="outlined"
          startIcon={<CampaignIcon />}
          onClick={() => setNotifyOpen(true)}
        >
          {runningTasks.length > 0
            ? `Contact running agents (${runningTasks.length})`
            : "Contact running agents"}
        </Button>
      </fieldset>
      <NotifyRunningDialog
        open={notifyOpen}
        onClose={() => setNotifyOpen(false)}
        runningTasks={runningTasks}
        onSent={onChanged}
        showError={showError}
      />
    </>
  );
}
