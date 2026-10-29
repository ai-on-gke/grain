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

import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  List,
  ListItemButton,
  ListItemText,
  Tab,
  Tabs,
  Typography,
} from "@mui/material";
import api from "../api.js";
import LogsPage from "./LogsPage.jsx";
import SandboxHealthPage from "./SandboxHealthPage.jsx";
import ConnectedExecutors from "./ConnectedExecutors.jsx";
import CachesPage from "./CachesPage.jsx";
import TopPage from "./TopPage.jsx";
import PipelineControl from "./PipelineControl.jsx";
import ReportBoard from "./ReportBoard.jsx";
import ReleaseNotesPanel from "./ReleaseNotesPanel.jsx";
import HealthTab from "./system/HealthTab.jsx";
import { SYSTEM_TABS, systemTabLabel } from "../settingsSections.js";
import { revealFocusedTab, useRevealSelectedTab } from "../tabStrip.js";
import { useIsPhone } from "../phone.js";

export { SYSTEM_TABS };

export const POWER_ACTIONS = [
  {
    id: "restart",
    label: "Restart grain",
    enabledKey: "restartEnabled",
    path: "/api/host/restart",
    hint: "Restarts the grain service alone -- the machine it runs on, and anything else running on it, stays up.",
    confirm:
      "Restart grain? Every task currently running is interrupted, and this UI is unreachable for a few seconds while the service comes back.",
    requested:
      "Restart requested. grain is coming back up; this UI is unreachable for a few seconds and then picks up where it was.",
  },
  {
    id: "reboot",
    label: "Reboot host",
    enabledKey: "rebootEnabled",
    path: "/api/host/reboot",
    hint: "Reboots the machine grain itself is running on.",
    confirm:
      "Reboot the host machine? Every task currently running is interrupted, and this UI will be unreachable until the machine comes back up.",
    requested:
      "Reboot requested. This UI is unreachable until the host machine comes back up.",
  },
  {
    id: "shutdown",
    label: "Shut down host",
    enabledKey: "shutdownEnabled",
    path: "/api/host/shutdown",
    hint: "Powers the machine off and leaves it off. Nothing here can start it again -- that takes whatever runs the machine.",
    confirm:
      "Shut down the host machine? Every task currently running is interrupted, and grain stays down until somebody powers the machine back on by some other means.",
    requested:
      "Shutdown requested. grain stays down until somebody powers the machine back on.",
  },
];

function PowerTab({ config, showError }) {
  // Which power action the host last accepted. A dropped connection
  // counts as accepted: it is what a host going down looks like from
  // here.
  const [powerRequested, setPowerRequested] = useState(null);
  const powerAction = async (action) => {
    if (!window.confirm(action.confirm)) return;
    setPowerRequested(null);
    try {
      await api(action.path, { method: "POST" });
      setPowerRequested(action);
    } catch (err) {
      if (err instanceof TypeError) setPowerRequested(action);
      else showError(err);
    }
  };
  const availablePower = POWER_ACTIONS.filter(
    (a) => config && config[a.enabledKey],
  );
  if (availablePower.length === 0) {
    return (
      <Alert severity="info">
        Not available: restarting grain, and rebooting or shutting down the
        host, are not enabled for this deployment.
      </Alert>
    );
  }
  return (
    <fieldset>
      <legend>Danger zone</legend>
      {powerRequested && (
        <Alert severity="info" sx={{ mb: 2 }}>
          {powerRequested.requested}
        </Alert>
      )}
      {availablePower.map((action) => (
        <div key={action.id} className="power-action">
          <p className="hint">{action.hint}</p>
          <Button
            variant="outlined"
            color="error"
            onClick={() => powerAction(action)}
          >
            {action.label}
          </Button>
        </div>
      ))}
    </fieldset>
  );
}

// SystemPage is the System view (docs/design/settings-panel.md §2.1):
// status and operations only -- every setting is in Settings. It opens
// from the System icon beside the gear (the foot of the drawer on a
// phone), on Health.
//
// `tab` is the open tab ("" for Health), onTab picks another, and the
// metrics props are the Metrics tab's report scope and window, held by
// App because they are in the URL (/system/metrics?window=30d).
export default function SystemPage({
  config,
  tab,
  onTab,
  showError,
  onPipelineChanged,
  onConfigChanged,
  onOpenSettings,
  onOpenSetup,
  tasks = [],
  alerts = [],
  metricsNarrowing,
  onNarrowMetrics,
  metricsWindow,
  onPickMetricsWindow,
  onOpenTask,
  onAct,
  onOpenSession,
}) {
  const isPhone = useIsPhone();
  const current = tab || "health";
  const tabStripRef = useRevealSelectedTab();

  const tabs = isPhone ? null : (
    <Tabs
      ref={tabStripRef}
      value={current}
      onChange={(_, value) => onTab(value === "health" ? "" : value)}
      variant="scrollable"
      scrollButtons="auto"
      aria-label="System tabs"
      onFocus={revealFocusedTab}
      sx={{ borderBottom: 1, borderColor: "divider", mb: 2 }}
    >
      {SYSTEM_TABS.map((t) => (
        <Tab key={t.id} value={t.id} label={t.label} />
      ))}
    </Tabs>
  );

  // On a phone, the bare /system is the list of tabs and a tab is a page
  // under a back arrow, as Settings' sections are (§3.6).
  if (isPhone && !tab) {
    return (
      <div className="main-column" role="main" data-testid="system-page">
        <Box sx={{ p: 1.5 }}>
          <Typography variant="h5" component="h1" sx={{ mb: 1 }}>
            System
          </Typography>
          <List aria-label="System tabs">
            {SYSTEM_TABS.map((t) => (
              <ListItemButton
                key={t.id}
                sx={{ minHeight: 40 }}
                onClick={() => onTab(t.id === "health" ? "health" : t.id)}
              >
                <ListItemText primary={t.label} />
                <span aria-hidden="true">›</span>
              </ListItemButton>
            ))}
          </List>
        </Box>
      </div>
    );
  }

  let body = null;
  switch (current) {
    case "health":
      body = (
        <HealthTab
          config={config}
          alerts={alerts}
          showError={showError}
          onChanged={onConfigChanged}
          onOpenSettings={onOpenSettings}
          onOpenSetup={onOpenSetup}
          onOpenPower={() => onTab("power")}
          onOpenTask={onOpenTask}
          onAct={onAct}
        />
      );
      break;
    case "logs":
      body = <LogsPage showError={showError} />;
      break;
    case "sandboxes":
      body = (
        <>
          <SandboxHealthPage
            showError={showError}
            onOpenSession={onOpenSession}
          />
          <Box sx={{ mt: 3 }}>
            <ConnectedExecutors />
          </Box>
        </>
      );
      break;
    case "caches":
      body = <CachesPage showError={showError} />;
      break;
    case "top":
      body = <TopPage showError={showError} />;
      break;
    case "pipeline":
      body = (
        <PipelineControl
          pause={config?.pipelinePause}
          tasks={tasks}
          onChanged={onPipelineChanged}
          showError={showError}
        />
      );
      break;
    case "metrics":
      body = (
        <>
          <Typography variant="h6" component="h2" sx={{ mb: 1 }}>
            Metrics
          </Typography>
          <Box sx={{ mb: 3 }}>
            <ReleaseNotesPanel showError={showError} />
          </Box>
          <ReportBoard
            config={config}
            tasks={tasks}
            narrowing={metricsNarrowing}
            onNarrow={onNarrowMetrics}
            pickedWindow={metricsWindow}
            onPickWindow={onPickMetricsWindow}
            onOpenTask={onOpenTask}
            showError={showError}
          />
        </>
      );
      break;
    case "power":
      body = <PowerTab config={config} showError={showError} />;
      break;
    default:
      body = null;
  }

  return (
    <div className="main-column" role="main" data-testid="system-page">
      <Box
        data-testid="system-tab"
        data-tab={current}
        sx={{ p: { xs: 1.5, sm: 3 }, overflowY: "auto", minWidth: 0 }}
      >
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          {isPhone && (
            <Button
              aria-label="All of System"
              onClick={() => onTab("")}
              sx={{ minWidth: 40 }}
            >
              ←
            </Button>
          )}
          <Typography variant="h5" component="h1" sx={{ mb: 1 }}>
            {isPhone ? systemTabLabel(current) : "System"}
          </Typography>
        </Box>
        {tabs}
        {body}
      </Box>
    </div>
  );
}
