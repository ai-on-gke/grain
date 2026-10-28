import { Chip, Stack, Tooltip } from "@mui/material";
import api from "../../api.js";
import { STATE_LABELS } from "../../state.js";
import TaskPicker from "../TaskPicker.jsx";

// What a chip cannot fit -- the depended-on task's state, and the tail
// of a long title -- goes here instead, so hovering a dependency tells
// you what it is without opening it. dep is missing
// when the depended-on task is not in the loaded list, and then its id
// is genuinely all this overlay knows about it.
function dependencyTooltip(id, dep, blocking) {
  if (!dep) return blocking ? `${id} — still open` : id;
  const state =
    dep.state === "closed" && !dep.endedAt
      ? "Cancelled"
      : STATE_LABELS[dep.state] || dep.state;
  return `${id} ${dep.title}${state ? ` — ${state}` : ""}${blocking ? " (blocking this task)" : ""}`;
}

// Dependencies is the "definition" and "signal" this whole feature is
// about, together: what a task has declared it depends on (chips,
// removable), which of those are still open (the "blocked" styling on a
// chip), and a way to add another -- attach/detach through /depends-on.
//
// One full-width chip per line, carrying the depended-on task's title:
// a wrapped row of id-only chips came out as bubbles a couple of
// characters wide, which named a dependency without telling you
// anything about it.
//
// A title longer than the pane is cut short with an ellipsis, and the
// id, the state and the × that removes the dependency keep their room:
// the title is the only part of the row that gives way (the tooltip
// above carries all of it). The fieldset's own min-width matters as much
// as the chip's: a fieldset is as wide as its content's min-content
// width unless told otherwise, so a nowrap title used to widen the
// whole section past the pane and carry the × off-screen with it.
export default function Dependencies({ t, tasks, act, onOpenTask }) {
  const dependsOn = t.dependsOn || [];
  const blockedBy = new Set(t.blockedBy || []);
  const byId = new Map((tasks || []).map((other) => [other.id, other]));

  const add = (picked) =>
    act(
      () =>
        api(`/api/tasks/${t.id}/depends-on`, {
          method: "POST",
          body: JSON.stringify({ id: picked.id, attach: true }),
        }),
      t.id,
    );

  return (
    <fieldset style={{ minWidth: 0 }}>
      <legend>Depends on</legend>
      {dependsOn.length === 0 && <p className="hint">No dependencies.</p>}
      <Stack spacing={0.6} sx={{ mb: dependsOn.length > 0 ? 1 : 0 }}>
        {dependsOn.map((id) => {
          const blocking = blockedBy.has(id);
          // A dependency the loaded list does not carry still gets its
          // chip, id only: dependsOn records ids, and hiding the row for
          // want of a title would hide the dependency itself.
          const dep = byId.get(id);
          let depState = "open";
          if (dep) {
            if (dep.state === "closed" && !dep.endedAt) {
              depState = "cancelled";
            } else if (dep.state === "failed") {
              depState = "failed";
            }
          }
          return (
            <Tooltip
              key={id}
              title={dependencyTooltip(id, dep, blocking)}
              placement="left"
            >
              <Chip
                variant={blocking ? "outlined" : "filled"}
                color={blocking ? "warning" : "default"}
                label={
                  <>
                    <span className="dep-id">{id}</span>
                    {dep && (
                      <>
                        {" "}
                        <span className="dep-title">{dep.title}</span>
                      </>
                    )}
                    {blocking && (
                      <>
                        {" "}
                        <span className="dep-state">({depState})</span>
                      </>
                    )}
                  </>
                }
                onClick={() => onOpenTask(id)}
                onDelete={() =>
                  act(
                    () =>
                      api(`/api/tasks/${t.id}/depends-on`, {
                        method: "POST",
                        body: JSON.stringify({ id, attach: false }),
                      }),
                    t.id,
                  )
                }
                deleteIcon={<span title={`Remove dependency on ${id}`}>×</span>}
                sx={{
                  width: "100%",
                  justifyContent: "space-between",
                  "& .MuiChip-label": {
                    flex: 1,
                    minWidth: 0,
                    display: "flex",
                    gap: "0.5ch",
                  },
                  "& .dep-id, & .dep-state": { flexShrink: 0 },
                  "& .dep-title": {
                    minWidth: 0,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  },
                  "& .MuiChip-deleteIcon": { flexShrink: 0 },
                }}
              />
            </Tooltip>
          );
        })}
      </Stack>
      <TaskPicker
        tasks={tasks || []}
        exclude={[t.id, ...dependsOn]}
        onPick={add}
        placeholder="Add a dependency…"
      />
    </fieldset>
  );
}
