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

import { useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  AlertTitle,
  Badge,
  Box,
  Button,
  Chip,
  IconButton,
  ListSubheader,
  Popover,
  Typography,
} from "@mui/material";
import ItemGlyph from "./ItemGlyph.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatTime } from "../time.js";
import {
  alertSentence,
  liftAgentPause,
  muiSeverity,
  resumePipeline,
  scopeLabel,
  worstSeverity,
} from "../alerts.js";
import {
  ackNotification,
  decideRelease,
  investigateNotification,
} from "../notifications.js";
import api from "../api.js";
import { promptForceMerge } from "../taskWrites.js";

// GROUP_AT is how many alerts it takes before the panel groups them by
// scope: below it, one list sorted by severity reads faster than headings
// over one or two rows each.
const GROUP_AT = 5;

// AlertsButton is the bell in the top bar and the panel it opens
// (grain/task-4008): every standing alert alerts.js's collectAlerts
// finds -- pauses, a stopped reconcile loop, settings not reaching the
// state repository, setup steps left -- in one list, where
// they used to be a banner each across the top of every page.
//
// The bell carries the count, coloured by the worst alert, and is quiet
// -- grey, no badge -- when there are none. A new critical alert is also
// said aloud to a screen reader, politely: it is the one kind that needs
// somebody to act, and a badge changing colour says nothing to somebody
// who cannot see it.
export default function AlertsButton({
  alerts,
  onChanged,
  showError,
  onOpenConnection,
  onOpenSetup,
  onOpenConfigRepo,
  onOpenTask,
  onAct,
}) {
  const [anchor, setAnchor] = useState(null);
  const count = alerts.length;
  const worst = worstSeverity(alerts);
  const color = worst ? muiSeverity(worst) : "default";
  const label =
    count === 0
      ? "Alerts: none"
      : `Alerts: ${count} active${worst === "critical" ? ", critical" : ""}`;
  const announcement = useCriticalAnnouncement(alerts);

  return (
    <>
      <IconButton
        size="small"
        data-testid="alerts-button"
        data-severity={worst || "none"}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={anchor ? "true" : "false"}
        title={label}
        onClick={(e) => setAnchor(e.currentTarget)}
        sx={{ color: worst ? `${color}.main` : "text.secondary" }}
      >
        <Badge
          badgeContent={count}
          color={worst ? color : "default"}
          invisible={count === 0}
          data-testid="alerts-count"
        >
          <ItemGlyph kind="alerts" size={18} />
        </Badge>
      </IconButton>
      {/* aria-live alone rather than role="status": a page may have a
          status of its own (the reorder bar's "Moving 1 task"), and this
          is not the page's status, only an announcement. */}
      <Box
        aria-live="polite"
        aria-atomic="true"
        data-testid="alerts-announcer"
        sx={visuallyHidden}
      >
        {announcement}
      </Box>
      <Popover
        open={Boolean(anchor)}
        anchorEl={anchor}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "right" }}
        transformOrigin={{ vertical: "top", horizontal: "right" }}
        slotProps={{
          paper: {
            role: "dialog",
            "aria-label": "Alerts",
            "data-testid": "alerts-panel",
            sx: {
              width: 440,
              // 32px, not 16: MUI keeps a popover its marginThreshold
              // (16px) from each edge of the window, so a paper allowed
              // all but 16px of a phone's width was pushed flush against
              // the right edge, its border and shadow cut off there and
              // a 16px gutter left on the other side.
              maxWidth: "calc(100vw - 32px)",
              maxHeight: "70vh",
            },
          },
        }}
      >
        <AlertsPanel
          alerts={alerts}
          onChanged={onChanged}
          showError={showError}
          onNavigate={(fn) => {
            setAnchor(null);
            fn();
          }}
          onOpenConnection={onOpenConnection}
          onOpenSetup={onOpenSetup}
          onOpenConfigRepo={onOpenConfigRepo}
          onOpenTask={onOpenTask}
          onAct={onAct}
        />
      </Popover>
    </>
  );
}

// useCriticalAnnouncement is the sentence the live region says: the
// newest critical alert's title, the first poll that carries it. Alerts
// already on screen when the page loads are not news, but the bell's own
// label already names them.
function useCriticalAnnouncement(alerts) {
  const seen = useRef(null);
  const [message, setMessage] = useState("");
  useEffect(() => {
    const critical = alerts.filter((a) => a.severity === "critical");
    if (seen.current === null) {
      seen.current = new Set(critical.map((a) => a.id));
      return;
    }
    const fresh = critical.filter((a) => !seen.current.has(a.id));
    seen.current = new Set(critical.map((a) => a.id));
    if (fresh.length > 0)
      setMessage(`Critical alert: ${fresh.map((a) => a.title).join("; ")}`);
  }, [alerts]);
  return message;
}

// Pixels spelled out: in sx a bare 1 is a fraction, so `width: 1` made
// this "hidden" box as wide as its bar, and a phone's page scrolled
// sideways by the bar's padding once a view drew inside app-body.
const visuallyHidden = {
  position: "absolute",
  width: "1px",
  height: "1px",
  overflow: "hidden",
  clip: "rect(0 0 0 0)",
  whiteSpace: "nowrap",
};

// AlertsPanel is the list itself: one row per alert, sorted by severity
// then recency (collectAlerts already did that), and grouped under the
// deployment and each connection once there are enough of them to need it.
export function AlertsPanel({
  alerts,
  onChanged,
  showError,
  onNavigate = (fn) => fn(),
  onOpenConnection,
  onOpenSetup,
  onOpenConfigRepo,
  onOpenTask,
  onAct,
}) {
  const groups = useMemo(() => groupAlerts(alerts), [alerts]);
  if (alerts.length === 0) {
    return (
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ p: 2 }}
        data-testid="alerts-empty"
      >
        No alerts. Pauses and warnings about this deployment show up here.
      </Typography>
    );
  }
  const row = (alert) => (
    <AlertRow
      key={alert.id}
      alert={alert}
      onChanged={onChanged}
      showError={showError}
      onNavigate={onNavigate}
      onOpenConnection={onOpenConnection}
      onOpenSetup={onOpenSetup}
      onOpenConfigRepo={onOpenConfigRepo}
      onOpenTask={onOpenTask}
      onAct={onAct}
    />
  );
  return (
    <Box sx={{ p: 1, display: "flex", flexDirection: "column", gap: 1 }}>
      {groups
        ? groups.map(([heading, rows]) => (
            <Box key={heading} data-testid="alerts-group">
              <ListSubheader
                disableSticky
                component="div"
                sx={{ px: 0.5, lineHeight: 2.2 }}
              >
                {heading}
              </ListSubheader>
              <Box sx={{ display: "flex", flexDirection: "column", gap: 1 }}>
                {rows.map(row)}
              </Box>
            </Box>
          ))
        : alerts.map(row)}
    </Box>
  );
}

// groupAlerts is alerts under a heading per scope -- "Deployment" first,
// then each connection in the order its worst alert sorts -- once there are
// GROUP_AT of them across more than one, or null to list them flat.
export function groupAlerts(alerts) {
  if (alerts.length < GROUP_AT) return null;
  const groups = new Map();
  for (const a of alerts) {
    const heading = scopeLabel(a.scope);
    if (!groups.has(heading)) groups.set(heading, []);
    groups.get(heading).push(a);
  }
  if (groups.size < 2) return null;
  const entries = [...groups.entries()];
  const deployment = entries.filter(([h]) => h === "Deployment");
  return [...deployment, ...entries.filter(([h]) => h !== "Deployment")];
}

// AlertRow is one alert: its severity's icon and colour, a title and its
// sentence, where it applies, since and until when, what it is costing,
// and the buttons its banner used to carry.
function AlertRow({
  alert,
  onChanged,
  showError,
  onNavigate,
  onOpenConnection,
  onOpenSetup,
  onOpenConfigRepo,
  onOpenTask,
  onAct,
}) {
  const zone = useTimeZone();
  const [busy, setBusy] = useState(null);

  async function run(action) {
    switch (action.kind) {
      case "open-setup":
        onNavigate(() => onOpenSetup?.(action));
        return;
      case "open-connection":
        onNavigate(() => onOpenConnection?.(action.connection));
        return;
      case "open-config-repo":
        onNavigate(() => onOpenConfigRepo?.());
        return;
      case "open-task":
        onNavigate(() => onOpenTask?.(action.taskId));
        return;
      case "force-merge-task":
        // promptForceMerge asks for the reason first, and the panel stays
        // open behind the prompt: onAct (App.jsx's act) reports a failure
        // and opens the task once the override is recorded.
        await promptForceMerge(action.task, onAct);
        return;
    }
    setBusy(action.kind);
    try {
      let done = true;
      if (action.kind === "lift-agent-pause")
        await liftAgentPause(action.entry);
      else if (action.kind === "ack-notification")
        await ackNotification(action.notification);
      else if (
        action.kind === "approve-release" ||
        action.kind === "reject-release"
      )
        done = await decideRelease(
          action.release,
          action.kind === "approve-release",
        );
      else if (action.kind === "investigate-notification") {
        const id = await investigateNotification(action.notification);
        if (id) onNavigate(() => onOpenTask?.(id));
      } else if (action.kind === "resume-pipeline")
        done = await resumePipeline(action.pause);
      else if (action.kind === "submit-task") {
        const submit = () =>
          api(`/api/tasks/${action.taskId}/submit`, { method: "POST" });
        // onAct reports its own failure, opens the task and refreshes the
        // list; without it (a test, a page that has no task list) post
        // directly and let onChanged refresh.
        if (onAct) done = await onAct(submit, action.taskId);
        else await submit();
      }
      // Refresh rather than drop the row locally: the pause lives in the
      // daemon, and config is polled from one place (App.jsx). Letting
      // that poll be what clears it keeps the panel showing what the
      // deployment actually says -- including a run dispatched straight
      // after the lift meeting the same limit and closing the gate again.
      if (done) await onChanged?.();
    } catch (err) {
      showError?.(err.message);
    } finally {
      setBusy(null);
    }
  }

  const when = [
    alert.since ? `Since ${formatTime(alert.since, zone)}` : null,
    alert.until ? `Until ${formatTime(alert.until, zone)}` : null,
  ].filter(Boolean);

  return (
    <Alert
      severity={muiSeverity(alert.severity)}
      variant="outlined"
      data-testid="alert-row"
      data-alert-id={alert.id}
      data-severity={alert.severity}
      sx={{ "& .MuiAlert-message": { width: "100%", minWidth: 0 } }}
    >
      <AlertTitle sx={{ mb: 0.25 }}>{alert.title}</AlertTitle>
      <Typography variant="body2" sx={{ overflowWrap: "anywhere" }}>
        {alertSentence(alert, zone)}
      </Typography>
      {alert.impact ? (
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ mt: 0.5 }}
          data-testid="alert-impact"
        >
          {alert.impact}
        </Typography>
      ) : null}
      <Box
        sx={{
          display: "flex",
          flexWrap: "wrap",
          alignItems: "center",
          gap: 0.75,
          mt: 0.75,
        }}
      >
        <Chip
          size="small"
          variant="outlined"
          label={scopeLabel(alert.scope)}
          data-testid="alert-scope"
        />
        {when.length > 0 ? (
          <Typography variant="caption" color="text.secondary">
            {when.join(" · ")}
          </Typography>
        ) : null}
        <Box sx={{ ml: "auto", display: "flex", flexWrap: "wrap", gap: 0.5 }}>
          {alert.actions.map((action) => (
            <Button
              key={action.kind}
              size="small"
              color="inherit"
              disabled={busy !== null}
              onClick={() => run(action)}
            >
              {busy === action.kind ? "Working…" : action.label}
            </Button>
          ))}
        </Box>
      </Box>
    </Alert>
  );
}
