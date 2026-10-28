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
import { Alert, Button, Stack, TextField } from "@mui/material";
import api from "../../api.js";
import SecretFileButton, { loadedFileNote } from "../SecretFileButton.jsx";

// PendingSecret is what a run's request_secret call turns into for the
// person reading the task: a box for the one credential it asked for,
// beside the request itself, which is the last thing on the timeline
// below.
//
// It is not the reply box, and that is the whole point. A reply is a
// comment -- stored in the task's conversation, visible to everyone who
// opens it, and fed back into the next run's prompt -- so a credential
// typed there would be handed to the agent that asked for it and written
// into grain's state in plain text. What is typed here goes to PUT
// /api/tasks/{id}/secret, straight into the encrypted secret store, and
// into no comment. Nothing reads a stored value back out (see
// SecretField, and pkg/secrets' own doc comment), so this is one-way:
// the run gets the *use* of the credential on its next attempt, never
// the material.
//
// t.pendingSecret is nil for almost every task, and this renders nothing
// for those.
//
// Upload beside the box is the same control the Settings pane's own
// secret fields carry (SecretFileButton, grain/task-160): a run can ask
// for a credential that lives in a file -- a GitHub App's .pem is the
// one this was built for -- and opening that file to paste it into a
// one-line box is where its line breaks get lost. It only fills the box;
// Set secret is still the press that stores it.
export default function PendingSecret({ t, act, showError }) {
  const [value, setValue] = useState("");
  const [loaded, setLoaded] = useState(null);
  const pending = t.pendingSecret;
  if (!pending) return null;

  // Unlike the reply box below, a rejected value stays in the box rather
  // than being cleared: this is usually something pasted out of a
  // password manager, and losing it to a 400 would mean going and
  // fetching it again. api throws on a non-2xx, so the early return is
  // the failure path -- act is only reached once the value is stored,
  // and is what re-reads the task (now queued again, with no secret
  // pending) and the list behind it.
  const submit = async () => {
    if (value.trim() === "") return;
    try {
      await api(`/api/tasks/${t.id}/secret`, {
        method: "PUT",
        body: JSON.stringify({ value }),
      });
    } catch (err) {
      showError(err);
      return;
    }
    setValue("");
    setLoaded(null);
    await act(() => Promise.resolve(), t.id);
  };

  return (
    <Alert severity="info" icon={false} sx={{ mb: 2 }}>
      <strong>This task&apos;s run asked for a secret.</strong>{" "}
      {pending.set
        ? `A value is already stored as ${pending.secret}/${pending.key}; setting one here replaces it.`
        : `Nothing is stored as ${pending.secret}/${pending.key} yet.`}{" "}
      The value goes straight into grain&apos;s secret store -- it is never
      shown to the agent, never added to this conversation, and cannot be read
      back out. Setting it puts the task back in the queue; replying below
      instead does the same without storing anything.
      <Stack direction="row" spacing={1} alignItems="flex-start" sx={{ mt: 1 }}>
        <TextField
          type="password"
          size="small"
          fullWidth
          autoComplete="off"
          label={pending.name}
          placeholder={
            pending.set
              ? "replace the stored value"
              : "paste a value, or upload the file it came in"
          }
          helperText={
            loaded
              ? loadedFileNote(loaded, "Set secret")
              : `stored as ${pending.secret}/${pending.key} -- write-only, never shown or read back`
          }
          InputLabelProps={{ shrink: true }}
          inputProps={{ "aria-label": `Value for ${pending.name}` }}
          value={value}
          onChange={(evt) => {
            setValue(evt.target.value);
            setLoaded(null);
          }}
          onKeyDown={(evt) => {
            if (evt.key === "Enter") {
              evt.preventDefault();
              submit();
            }
          }}
        />
        <SecretFileButton
          name={pending.name}
          showError={showError}
          onLoad={(text, file) => {
            setValue(text);
            setLoaded(file);
          }}
          sx={{ mt: 0.5 }}
        />
        <Button
          type="button"
          variant="contained"
          disabled={value.trim() === ""}
          onClick={submit}
          sx={{ mt: 0.5 }}
        >
          Set secret
        </Button>
      </Stack>
    </Alert>
  );
}
