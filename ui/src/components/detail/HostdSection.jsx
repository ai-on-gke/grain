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
import { Button, Stack, TextField } from "@mui/material";
import api from "../../api.js";

// HostdSection is which grain hostd this task's host work goes to
// (ui.TaskDetail.Hostd, model.SetTaskHostd): a hostd's name, or
// label=value[,label=value] for any hostd carrying them all, or the
// default hostd when it names none. It edits the pin too (PUT
// /api/tasks/{id}/hostd), from the task's next run on; each attempt's
// own hostd is on its timeline row.
export default function HostdSection({ t, act }) {
  const [draft, setDraft] = useState(null);
  const closed = t.state === "closed";
  const hostd = t.hostd || "";

  const put = (selector) =>
    act(
      () =>
        api(`/api/tasks/${t.id}/hostd`, {
          method: "PUT",
          body: JSON.stringify({ hostd: selector }),
        }),
      t.id,
    );
  const save = () => put(draft.trim()).then((ok) => ok && setDraft(null));

  return (
    <fieldset data-testid="hostd-section" style={{ minWidth: 0 }}>
      <legend>Hostd</legend>
      {draft === null ? (
        <p className="hint" data-testid="hostd-summary">
          {hostd ? (
            <>
              Host work goes to hostd{" "}
              <code style={{ overflowWrap: "anywhere" }}>{hostd}</code>, and
              waits for it rather than running anywhere else.
            </>
          ) : (
            "Host work goes to the default hostd."
          )}
        </p>
      ) : (
        <TextField
          size="small"
          label="Hostd"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          helperText="A hostd's name, or label=value[,label=value]. Empty for the default hostd. Applies from the next run on."
          autoComplete="off"
          fullWidth
          sx={{ mt: 1 }}
          inputProps={{ "data-testid": "hostd-input" }}
        />
      )}
      {!closed && (
        <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
          {draft !== null ? (
            <>
              <Button size="small" variant="contained" onClick={save}>
                Save hostd
              </Button>
              <Button size="small" onClick={() => setDraft(null)}>
                Cancel
              </Button>
            </>
          ) : (
            <>
              <Button size="small" onClick={() => setDraft(hostd)}>
                {hostd ? "Change hostd" : "Pin to a hostd"}
              </Button>
              {hostd && (
                <Button size="small" color="warning" onClick={() => put("")}>
                  Use default hostd
                </Button>
              )}
            </>
          )}
        </Stack>
      )}
    </fieldset>
  );
}
