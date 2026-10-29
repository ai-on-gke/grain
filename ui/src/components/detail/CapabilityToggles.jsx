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
  Checkbox,
  Chip,
  FormControl,
  FormHelperText,
  ListItemText,
  MenuItem,
  Select,
} from "@mui/material";
import api from "../../api.js";
import { isCapabilityRef } from "../../capabilityArgs.js";
import { isApiGrant, withoutApiRows } from "../../apiGrants.js";
import GrantsPicker from "../GrantsPicker.jsx";
import {
  LEGACY_CAPABILITIES_HINT,
  NO_CAPABILITIES_HINT,
  capabilityRows,
  capabilityUnavailableHint,
} from "../../state.js";

export default function CapabilityToggles({ t, config, act }) {
  const capabilities = config?.capabilities || [];
  const held = t.capabilities || [];
  // Capability server grants, with their arguments, and the agent API's
  // grants (apiGrants.js) are GrantsPicker's below, one chip list; the
  // checklist is the legacy in-process ids.
  const selected = held.filter((id) => !isCapabilityRef(id) && !isApiGrant(id));
  const apiSelected = held.filter(isApiGrant);
  const [capabilitiesChanged, setCapabilitiesChanged] = useState(false);
  const [restarting, setRestarting] = useState(false);
  // A task can hold a grant the picker no longer offers -- a capability
  // renamed or dropped since it was attached ("scratch-repo", now
  // github-sandbox, bwsalmon/agents#612). capabilityRows (state.js)
  // gives it a row of its own purely to be turned off; without one it is
  // a chip nothing can untick, and a grant no provider is registered for
  // fails every run of the task holding it. SetCapability (pkg/ui,
  // client.go) is the other half -- it validates on attach only, so the
  // detach this row sends is accepted. The same call is what leaves an
  // unconfigured capability off the list altogether unless this task
  // already holds it (configuredCapabilities, state.js).
  const rows = withoutApiRows(capabilityRows(capabilities, selected));

  const grantPath = `/api/tasks/${t.id}/capabilities`;
  const noteChange = () => {
    if (t.state === "running") setCapabilitiesChanged(true);
  };
  // A grant with arguments is written straight away, so a refusal comes
  // back to the form that made it (with its field errors) rather than to
  // the page's error banner; act then refreshes the task.
  const submitGrant = async (spec, replacing) => {
    await api(grantPath, {
      method: "POST",
      body: JSON.stringify({
        ...spec,
        attach: true,
        replace: replacing || undefined,
      }),
    });
    noteChange();
    await act(() => Promise.resolve(), t.id);
  };
  const removeGrant = (grant) =>
    act(
      () =>
        api(grantPath, {
          method: "POST",
          body: JSON.stringify({ grant, attach: false }),
        }).then(noteChange),
      t.id,
    );

  const handleChange = (e) => applyChange(selected, e.target.value);
  const applyChange = (before, next) => {
    const added = next.filter((id) => !before.includes(id));
    const removed = before.filter((id) => !next.includes(id));
    if (added.length > 0 || removed.length > 0) {
      if (t.state === "running") setCapabilitiesChanged(true);
    }
    added.forEach((id) =>
      act(
        () =>
          api(`/api/tasks/${t.id}/capabilities`, {
            method: "POST",
            body: JSON.stringify({ id, attach: true }),
          }),
        t.id,
      ),
    );
    removed.forEach((id) =>
      act(
        () =>
          api(`/api/tasks/${t.id}/capabilities`, {
            method: "POST",
            body: JSON.stringify({ id, attach: false }),
          }),
        t.id,
      ),
    );
  };

  return (
    <fieldset>
      <legend>Legacy capabilities</legend>
      <FormControl fullWidth size="small">
        <Select
          multiple
          displayEmpty
          inputProps={{ "aria-label": "Legacy capabilities" }}
          value={selected}
          onChange={handleChange}
          renderValue={(sel) =>
            sel.length === 0 ? (
              <span className="hint">None</span>
            ) : (
              <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                {sel.map((id) => {
                  const c = rows.find((cap) => cap.id === id);
                  return <Chip key={id} size="small" label={c ? c.name : id} />;
                })}
              </Box>
            )
          }
        >
          {rows.length === 0 && (
            <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
          )}
          {rows.map((c) => {
            const unavailable = capabilityUnavailableHint(c);
            return (
              <MenuItem
                key={c.id}
                value={c.id}
                title={
                  unavailable
                    ? `${c.description}\n\n${unavailable}`
                    : c.description
                }
              >
                <Checkbox checked={selected.includes(c.id)} size="small" />
                <ListItemText
                  primary={c.name}
                  secondary={unavailable || null}
                  secondaryTypographyProps={{ color: "warning.main" }}
                />
              </MenuItem>
            );
          })}
        </Select>
        <FormHelperText>{LEGACY_CAPABILITIES_HINT}</FormHelperText>
      </FormControl>
      <GrantsPicker
        config={config}
        grants={held}
        onApiChange={(next) => applyChange(apiSelected, next)}
        onSubmit={submitGrant}
        onRemove={removeGrant}
        idPrefix={`task-${t.id}`}
        policySubject={{ kind: "task", repo: t.repo || "" }}
      />
      {t.state === "running" && capabilitiesChanged && (
        <Alert
          severity="info"
          sx={{ mt: 1, fontSize: "0.8rem", ".MuiAlert-message": { pt: 0.5 } }}
          action={
            <Button
              color="inherit"
              size="small"
              disabled={restarting}
              onClick={(e) => {
                e.preventDefault();
                setRestarting(true);
                act(async () => {
                  try {
                    await api(`/api/tasks/${t.id}/close`, {
                      method: "POST",
                      body: JSON.stringify({ close_pull_request: false }),
                    });
                    for (let i = 0; i < 60; i++) {
                      await new Promise((r) => setTimeout(r, 1000));
                      const taskState = await api(`/api/tasks/${t.id}`);
                      if (taskState.state === "closed") {
                        await api(`/api/tasks/${t.id}/reopen`, {
                          method: "POST",
                        });
                      }
                      try {
                        await api(`/api/tasks/${t.id}/run-now`, {
                          method: "POST",
                        });
                        break;
                      } catch (err) {
                        if (
                          err.message &&
                          String(err.message).includes("already running")
                        ) {
                          continue;
                        }
                        if (
                          err.message &&
                          String(err.message).includes("is closed")
                        ) {
                          continue; // wait for reopen to take effect
                        }
                        throw err;
                      }
                    }
                  } finally {
                    setRestarting(false);
                    setCapabilitiesChanged(false);
                  }
                }, t.id);
              }}
            >
              {restarting ? "Restarting…" : "Restart Task"}
            </Button>
          }
        >
          Changes take effect on the next run.
        </Alert>
      )}
    </fieldset>
  );
}
