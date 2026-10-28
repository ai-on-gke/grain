import { Button, Chip, Stack, Tooltip, Typography } from "@mui/material";
import api from "../api.js";

// A CL's Submit and Sync (the task's `clSubmit`, changelists.md §6-§7).
// Submit is a person's, and is disabled with every reason it cannot go
// yet -- there is no "submit anyway". The reasons are written out under
// the buttons as well as in the tooltip, since a disabled button's
// tooltip is unreachable on a phone.

// BehindBadge is "behind head" (or "conflicts with head") beside the
// cl/N chip.
export function BehindBadge({ submit }) {
  if (!submit || (!submit.behind && !submit.conflicted)) return null;
  const conflicted = submit.conflicted;
  return (
    <Tooltip
      title={
        conflicted
          ? "This CL conflicts with head: Sync it, or a run resolves the conflict."
          : "This CL is behind head: Sync brings it up to date."
      }
    >
      <Chip
        className="cl-behind-badge"
        data-testid="cl-behind-badge"
        size="small"
        color="warning"
        variant="outlined"
        label={conflicted ? "conflicts with head" : "behind head"}
        sx={{ verticalAlign: "middle" }}
      />
    </Tooltip>
  );
}

export default function ClSubmitActions({ t, act }) {
  const submit = t?.clSubmit;
  if (!submit || t.state === "closed") return null;
  const reasons = submit.reasons || [];
  return (
    <Stack spacing={0.5} data-testid="cl-submit-actions">
      <Stack direction="row" spacing={1} sx={{ flexWrap: "wrap", rowGap: 1 }}>
        <Tooltip
          title={
            submit.canSubmit
              ? `Submit ${submit.target}`
              : `Not submittable yet: ${reasons.join("; ")}`
          }
        >
          <span>
            <Button
              variant="contained"
              disabled={!submit.canSubmit}
              data-testid="cl-submit"
              onClick={() =>
                act(
                  () => api(`/api/tasks/${t.id}/submit`, { method: "POST" }),
                  t.id,
                )
              }
            >
              Submit
            </Button>
          </span>
        </Tooltip>
        <Tooltip
          title={
            submit.canSync
              ? `Sync ${submit.target} to head`
              : submit.syncReason || ""
          }
        >
          <span>
            <Button
              variant="outlined"
              disabled={!submit.canSync}
              data-testid="cl-sync"
              onClick={() =>
                act(
                  () =>
                    api(`/api/tasks/${t.id}/change-sync`, { method: "POST" }),
                  t.id,
                )
              }
            >
              Sync
            </Button>
          </span>
        </Tooltip>
      </Stack>
      {!submit.canSubmit && reasons.length > 0 && (
        <Typography
          variant="caption"
          color="text.secondary"
          data-testid="cl-submit-reasons"
          component="div"
        >
          Not submittable yet: {reasons.join("; ")}.
        </Typography>
      )}
    </Stack>
  );
}
