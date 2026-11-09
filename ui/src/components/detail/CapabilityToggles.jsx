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
import { Alert, Button } from "@mui/material";
import api from "../../api.js";
import { isLegacyApiGrant } from "../../apiGrants.js";
import GrantsPicker from "../GrantsPicker.jsx";

export default function CapabilityToggles({ t, config, act }) {
  const held = t.capabilities || [];
  // Capability server grants, with their arguments, the agent API's grants
  // (apiGrants.js), and any bare capability id an older store still holds
  // are GrantsPicker's below, one list.
  const apiSelected = held.filter(isLegacyApiGrant);
  const [capabilitiesChanged, setCapabilitiesChanged] = useState(false);
  const [restarting, setRestarting] = useState(false);

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
      <legend>Access</legend>
      <GrantsPicker
        config={config}
        grants={held}
        onApiChange={(next) => applyChange(apiSelected, next)}
        onSubmit={submitGrant}
        onRemove={removeGrant}
        idPrefix={`task-${t.id}`}
        policyRepo={t.repo || ""}
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
