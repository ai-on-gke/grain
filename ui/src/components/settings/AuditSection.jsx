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

import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import api from "../../api.js";
import { formatDateTime } from "../../time.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { useIsPhone } from "../../phone.js";
import SettingsCard from "./SettingsCard.jsx";

// AUDIT_LIMIT is how many entries each card asks for.
const AUDIT_LIMIT = 100;

// useAuditLog reads one audit endpoint. Each card reads its own, so a
// caller missing one scope (config audit is settings:read, the other two
// secrets:read) still sees the rest.
function useAuditLog(path) {
  const [state, setState] = useState({ entries: null, error: "" });
  useEffect(() => {
    let live = true;
    api(path)
      .then(
        (entries) => live && setState({ entries: entries || [], error: "" }),
      )
      .catch(
        (err) =>
          live && setState({ entries: [], error: err?.message || String(err) }),
      );
    return () => {
      live = false;
    };
  }, [path]);
  return state;
}

// AuditTable is one card's table, or why it is empty. A phone has no
// room for the columns side by side, so there each entry is a block: its
// first column (the time) on top, and every other column beneath it as a
// labelled line, leaving out the ones with nothing to say.
function AuditTable({ testId, what, state, columns, rowKey, rowTestId }) {
  const phone = useIsPhone();
  if (state.error) {
    return (
      <Alert severity="warning" data-testid={`${testId}-error`}>
        The {what} could not be read ({state.error}).
      </Alert>
    );
  }
  if (!state.entries) return null;
  if (state.entries.length === 0) {
    return (
      <Typography
        variant="body2"
        color="text.secondary"
        data-testid={`${testId}-empty`}
      >
        Nothing recorded yet.
      </Typography>
    );
  }
  if (phone) {
    const [head, ...rest] = columns;
    return (
      <Box data-testid={testId} data-layout="stacked">
        {state.entries.map((e, i) => (
          <Box
            key={rowKey(e, i)}
            data-testid={rowTestId?.(e, i)}
            sx={{
              py: 1,
              borderTop: i ? 1 : 0,
              borderColor: "divider",
              overflowWrap: "anywhere",
            }}
          >
            <Typography variant="caption" color="text.secondary">
              {head.value(e)}
            </Typography>
            {rest.map((c) => {
              const value = c.value(e);
              if (value === "—" || value === "") return null;
              return (
                <Box
                  key={c.label}
                  sx={{ display: "flex", gap: 1, typography: "body2" }}
                >
                  <Box
                    component="span"
                    sx={{ flex: "none", width: 72, color: "text.secondary" }}
                  >
                    {c.label}
                  </Box>
                  <Box component="span" sx={{ minWidth: 0 }}>
                    {value}
                  </Box>
                </Box>
              );
            })}
          </Box>
        ))}
      </Box>
    );
  }
  return (
    <Box sx={{ overflowX: "auto" }}>
      <Table size="small" data-testid={testId}>
        <TableHead>
          <TableRow>
            {columns.map((c) => (
              <TableCell key={c.label}>{c.label}</TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>
          {state.entries.map((e, i) => (
            <TableRow key={rowKey(e, i)} data-testid={rowTestId?.(e, i)}>
              {columns.map((c) => (
                <TableCell
                  key={c.label}
                  sx={
                    c.nowrap
                      ? { whiteSpace: "nowrap" }
                      : { overflowWrap: "break-word" }
                  }
                >
                  {c.value(e)}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Box>
  );
}

// taskText names the task (and run) an audit row was for, or nothing.
function taskText(e) {
  if (!e.taskId) return "";
  return e.runId ? `task ${e.taskId} (run ${e.runId})` : `task ${e.taskId}`;
}

// AuditSection is Settings → Audit (operator-attribution spec §4.4): who
// changed the deployment's configuration, newest first, beside the team
// audit (credential accesses and secret resolutions) and the secret
// audit. A config change names its actor by label -- an email for a
// verified person, "operator (access code)" for the shared code; the
// other two show their principal as stored, which is the principal's ID
// (§1.3.2).
export default function AuditSection() {
  const zone = useTimeZone();
  const config = useAuditLog(
    `/api/deployment/config-audit?limit=${AUDIT_LIMIT}`,
  );
  const team = useAuditLog(`/api/deployment/audit-logs?limit=${AUDIT_LIMIT}`);
  const secrets = useAuditLog("/api/deployment/secret-audit");
  const at = (value) => formatDateTime(value, zone);

  return (
    <>
      <SettingsCard title="Configuration changes" anchor="config-audit">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Each change to the deployment&apos;s settings, a repository, a
          schedule, a template and the like, with who made it. Summaries never
          carry a secret value.
        </Typography>
        <AuditTable
          testId="config-audit-table"
          what="configuration changes"
          state={config}
          rowKey={(e) => e.id}
          rowTestId={(e) => `config-audit-row-${e.id}`}
          columns={[
            { label: "When", nowrap: true, value: (e) => at(e.at) },
            { label: "Who", value: (e) => e.actorLabel || e.actor },
            {
              label: "Surface",
              value: (e) => <code>{e.surface}</code>,
            },
            { label: "Change", value: (e) => e.summary },
          ]}
        />
      </SettingsCard>
      <SettingsCard title="Credential access" anchor="team-audit">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Every credential the git and model proxies lent a run, and every
          secret resolved, newest first.
        </Typography>
        <AuditTable
          testId="team-audit-table"
          what="credential accesses"
          state={team}
          rowKey={(e, i) => `${e.timestamp}-${e.kind}-${i}`}
          rowTestId={(e, i) => `team-audit-row-${i}`}
          columns={[
            { label: "When", nowrap: true, value: (e) => at(e.timestamp) },
            { label: "Kind", nowrap: true, value: (e) => e.kind },
            {
              label: "Resource",
              value: (e) => (
                <>
                  {[e.action, e.resource].filter(Boolean).join(" ") || "—"}
                  {e.credential ? (
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="div"
                    >
                      via {e.credential}
                    </Typography>
                  ) : null}
                </>
              ),
            },
            { label: "Outcome", value: (e) => e.outcome || "—" },
            { label: "Principal", value: (e) => e.principal || "—" },
            { label: "Task", nowrap: true, value: (e) => taskText(e) || "—" },
          ]}
        />
      </SettingsCard>
      <SettingsCard title="Secret resolutions" anchor="secret-audit">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Each time a secret&apos;s value was read out of the store, and for
          whom.
        </Typography>
        <AuditTable
          testId="secret-audit-table"
          what="secret resolutions"
          state={secrets}
          rowKey={(e) => e.id}
          rowTestId={(e) => `secret-audit-row-${e.id}`}
          columns={[
            { label: "When", nowrap: true, value: (e) => at(e.timestamp) },
            {
              label: "Secret",
              value: (e) => (
                <code>{e.key ? `${e.secretName}/${e.key}` : e.secretName}</code>
              ),
            },
            { label: "Principal", value: (e) => e.principal || "—" },
            { label: "Task", nowrap: true, value: (e) => taskText(e) || "—" },
          ]}
        />
      </SettingsCard>
    </>
  );
}
