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

import { useEffect, useRef, useState } from "react";
import { Button, Link, Stack, Typography } from "@mui/material";
import api from "../../api.js";
import { newTaskPath } from "../../paths.js";
import {
  CustomFieldInputs,
  declaredFields,
  fieldRows,
  initialFieldValues,
  legacyRowTitle,
} from "../CustomFields.jsx";

// CustomFields is this deployment's own fields on this task
// (grain/task-53): what the task says about each of them, an editor for
// changing that, and the link that files the *next* task carrying the
// same answers.
//
// That link is the reason the panel is worth more than a couple of
// read-only rows. Custom fields are how a deployment records what its
// work is actually about -- a customer, a priority, a release -- and
// work of that kind arrives in runs: the same customer again, the same
// release again. Following it opens the new-task form with those values
// already in it, and because it is a real URL (paths.js's newTaskPath)
// it can be copied out of here and sent to somebody else, or kept as
// the bookmark for filing this kind of task.
//
// Renders nothing on a deployment that declares no fields *and* a task
// that carries no stale ones -- which is every deployment that has not
// asked for this.
export default function CustomFieldsSection({
  t,
  config,
  act,
  onNewTaskFrom,
  unsavedRef,
}) {
  const [editing, setEditing] = useState(false);
  const [values, setValues] = useState(t.customFields || {});
  const [saving, setSaving] = useState(false);
  const declared = declaredFields(config);
  const rows = fieldRows(config, t.customFields);

  const isDirty = () => {
    if (!editing) return false;
    const initial = initialFieldValues(config, t.customFields);
    return JSON.stringify(values) !== JSON.stringify(initial);
  };
  const isDirtyRef = useRef(isDirty);
  isDirtyRef.current = isDirty;
  useEffect(() => {
    if (!unsavedRef) return;
    unsavedRef.current = () => isDirtyRef.current();
    return () => {
      unsavedRef.current = null;
    };
  }, [unsavedRef]);

  if (declared.length === 0 && rows.length === 0) return null;

  const open = () => {
    // Seeded when the editor opens rather than kept in step with every
    // poll: what is in the boxes is what is being typed, and a refresh
    // landing mid-edit must not overwrite it. Every declared field is
    // present, so a blank one saves as "cleared" -- which is what the
    // server reads it as (ui.UpdateTaskRequest.CustomFields).
    setValues(initialFieldValues(config, t.customFields));
    setEditing(true);
  };
  const save = async () => {
    setSaving(true);
    try {
      const saved = await act(
        () =>
          api(`/api/tasks/${t.id}`, {
            method: "PATCH",
            body: JSON.stringify({ customFields: values }),
          }),
        t.id,
      );
      if (saved !== false) setEditing(false);
    } finally {
      setSaving(false);
    }
  };
  // The link carries what the task holds now, not what the editor is
  // showing: an unsaved edit is not something this task says yet, so it
  // is not something the next task should inherit.
  const newTaskHref = newTaskPath(t.customFields);

  return (
    <div className="declared">
      {editing ? (
        <>
          <CustomFieldInputs
            config={config}
            values={values}
            onChange={(name, value) =>
              setValues((prev) => ({ ...prev, [name]: value }))
            }
          />
          <Stack direction="row" spacing={1} justifyContent="flex-end">
            <Button size="small" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button size="small" disabled={saving} onClick={save}>
              {saving ? "Saving…" : "Save fields"}
            </Button>
          </Stack>
        </>
      ) : (
        <>
          {rows.map((r) => (
            <div className="declared-row" key={r.name}>
              <span className="declared-key">{r.label}</span>
              {/* A value its declaration has moved on from is shown
                  exactly like any other -- it is what somebody recorded
                  -- and says so on hover (CustomFields.jsx's
                  legacyRowTitle). The editor is where it can be picked
                  off. */}
              <span className="declared-value" title={legacyRowTitle(r)}>
                {r.value}
              </span>
            </div>
          ))}
          {rows.length === 0 && (
            <Typography variant="caption" color="text.secondary">
              No fields set on this task.
            </Typography>
          )}
          <Stack direction="row" spacing={1} sx={{ mt: 0.5 }}>
            {declared.length > 0 && (
              <Button size="small" onClick={open}>
                Edit fields
              </Button>
            )}
            <Link
              href={newTaskHref}
              // A real href so it can be copied and sent, and an
              // in-app open when it is simply clicked: a full page
              // load to reach an overlay over the list this task is
              // already on would be a reload nobody asked for.
              onClick={(e) => {
                if (!onNewTaskFrom) return;
                if (
                  e.button !== 0 ||
                  e.metaKey ||
                  e.ctrlKey ||
                  e.shiftKey ||
                  e.altKey
                ) {
                  return;
                }
                e.preventDefault();
                onNewTaskFrom(t.customFields || {});
              }}
              sx={{ alignSelf: "center", fontSize: "0.8rem" }}
            >
              File a task with these fields
            </Link>
          </Stack>
        </>
      )}
    </div>
  );
}
