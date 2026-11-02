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

import { Fragment, useEffect, useState } from "react";
import { Alert, Box, Button, Link } from "@mui/material";
import api from "../../api.js";
import { AlertsPanel } from "../AlertsButton.jsx";
import { InstallNoticeAlert } from "../InstallNotice.jsx";
import { SandboxOvercommitStatus } from "../SandboxHealthPage.jsx";
import UnbackedStateDot, { stateDot } from "../UnbackedStateDot.jsx";
import GitHubBudgetPanel from "./GitHubBudgetPanel.jsx";
import { HostdHealthNote } from "../HostdMissingNote.jsx";
import { settingLabel, settingLocation } from "../../settingsPlan.js";

// HealthTab is System → Health, System's default tab
// (design/design/settings-panel.design.md §2.1): is anything wrong? It is the
// alerts the bell already lists, each with the link it already carries,
// and the two settings states that used to sit at the top of every
// settings tab -- saved but not applied until a restart, and stored but
// not runnable. No new API: GET /api/config (the alerts) and GET
// /api/settings.
export default function HealthTab({
  config,
  alerts,
  showError,
  onChanged,
  onOpenSettings,
  onOpenSetup,
  onOpenPower,
  onOpenTask,
  onAct,
}) {
  const [settings, setSettings] = useState(null);
  // grain hostd's state (daemon.HostExecutorStatus), for a deployment
  // beside it: unreachable, or missing some of grain's own entries.
  const [hostExecutor, setHostExecutor] = useState(null);
  useEffect(() => {
    let live = true;
    api("/api/settings")
      .then((s) => live && setSettings(s))
      .catch(() => {});
    api("/api/settings/cli-run")
      .then((r) => live && setHostExecutor(r?.hostExecutor || null))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);
  const pending = settings?.pendingRestart || [];
  const unrunnable = settings?.unrunnable || [];
  const settingLink = (key) => {
    const label = settingLabel(key);
    const loc = settingLocation(key);
    if (!loc) return label;
    return (
      <Link
        href={loc.href}
        onClick={(evt) => {
          if (
            !onOpenSettings ||
            evt.button !== 0 ||
            evt.metaKey ||
            evt.ctrlKey ||
            evt.shiftKey ||
            evt.altKey
          )
            return;
          evt.preventDefault();
          onOpenSettings(loc.section, loc.target);
        }}
      >
        {label}
      </Link>
    );
  };
  return (
    <Box data-testid="system-health">
      <InstallNoticeAlert
        notice={config?.installNotice}
        testId="health-install-notice"
      />
      <HostdHealthNote hostExecutor={hostExecutor} />
      {stateDot(config) && (
        <Alert
          severity="warning"
          sx={{ mb: 1 }}
          data-testid="health-config-repo"
          action={
            <Button
              color="inherit"
              size="small"
              onClick={() => onOpenSettings?.("config-repo")}
            >
              Config repo
            </Button>
          }
        >
          <UnbackedStateDot config={config} /> A merged change reset settings on
          this deployment.
        </Alert>
      )}
      {pending.length > 0 && (
        <Alert
          severity="info"
          sx={{ mb: 1 }}
          data-testid="health-pending-restart"
          action={
            <Button color="inherit" size="small" onClick={onOpenPower}>
              Power
            </Button>
          }
        >
          Saved, not applied until restart:{" "}
          {pending.map((key, idx) => (
            <Fragment key={key}>
              {idx > 0 && ", "}
              {settingLink(key)}
            </Fragment>
          ))}
          .
        </Alert>
      )}
      {unrunnable.length > 0 && (
        <Alert
          severity="warning"
          sx={{ mb: 1 }}
          data-testid="health-unrunnable"
        >
          Stored, but not runnable:{" "}
          {unrunnable.map((u, idx) => (
            <Fragment key={u.key}>
              {idx > 0 && ", "}
              {settingLink(u.key)} (stored {u.stored}, running {u.running})
            </Fragment>
          ))}
          . grain is running with a value it can use instead; save it in
          Settings to store it.
        </Alert>
      )}
      <SandboxOvercommitStatus
        budget={settings?.sandboxCommitment?.budget}
        overcommit={
          settings?.sandboxCommitment?.budget?.overcommit ||
          settings?.overcommit
        }
        sx={{ mb: 1.5 }}
      />
      <GitHubBudgetPanel sx={{ mb: 1.5 }} />
      <AlertsPanel
        alerts={alerts}
        onChanged={onChanged}
        showError={showError}
        onOpenConnection={() => onOpenSettings?.("models")}
        onOpenSetup={onOpenSetup}
        onOpenConfigRepo={() => onOpenSettings?.("config-repo")}
        onOpenPower={onOpenPower}
        onOpenTask={onOpenTask}
        onAct={onAct}
      />
    </Box>
  );
}
